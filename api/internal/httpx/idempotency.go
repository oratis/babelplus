package httpx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	dbgen "github.com/oratis/babelplus/api/db/gen"
)

// ---- 幂等骨架 ----
//
// 用途：`POST /api/v1/orders`、`POST /api/v1/orders/{n}/pay`、支付回调、流量入账
// 都要「同一个键重复调用只生效一次」（api-contract.md §9.1）。
//
// 三态而不是两态。天真的实现只分「首次」与「重放」，
// 漏掉的第三态是**并发**：同一个键的第二个请求在第一个还没写完结果时到达。
// 此时既不能执行（会重复扣款），也没有结果可重放。必须显式返回「进行中」让调用方回 409，
// 而不是傻等或当成首次执行。
//
// 🔴 本骨架治的是 `Idempotency-Key` 那一类（客户端生成键）。
// **`/push` 的重复上报治不了** —— v2node 上报增量字节且不带任何幂等键，
// 服务端无法区分「重试的同一批」与「下一个窗口的新一批」（api-contract.md §9.2 已量化承认）。
// 不要把这里的 helper 套到 /push 上假装解决了那个缺口。
//
// 与 webhook 重放防护的分工：支付回调另有 `webhook_events` 的
// `UNIQUE (gateway, event_id)`，那条路径靠数据库唯一约束，不走本文件。
// 本文件是「HTTP 层客户端提供键」的那一半。

// IdempotencyKeyHeader 是携带幂等键的请求头（openapi.yaml components.parameters.IdempotencyKeyHeader）。
const IdempotencyKeyHeader = "Idempotency-Key"

// 幂等键长度约束，与 OpenAPI 的 minLength/maxLength 一致。
// 改这里必须同步改 openapi.yaml —— 契约是事实源。
const (
	minIdempotencyKeyLen = 8
	maxIdempotencyKeyLen = 128
)

// idempotency_keys.status 的取值，与 0006_orders.up.sql 的 CHECK 约束一致。
const (
	idempotencyStatusInProgress = "in_progress"
	idempotencyStatusCompleted  = "completed"
)

// 调用方需要区分的四种失败。全部用 errors.Is 判断。
var (
	// ErrIdempotencyKeyMissing：请求没带 Idempotency-Key。→ 400 VALIDATION_MALFORMED_BODY
	ErrIdempotencyKeyMissing = errors.New("缺少 Idempotency-Key 请求头")

	// ErrIdempotencyKeyMalformed：键的长度或字符集不合法。→ 422 VALIDATION_FAILED
	ErrIdempotencyKeyMalformed = errors.New("Idempotency-Key 形态非法")

	// ErrIdempotencyMismatch：同一个键配了不同的载荷（或不同的用户 / 端点）。
	// → 409 STATE_IDEMPOTENCY_MISMATCH（api-contract.md §2.3）
	ErrIdempotencyMismatch = errors.New("同一 Idempotency-Key 的请求载荷不一致")

	// ErrIdempotencyInProgress：同键的上一次请求还在执行中。
	// → 409 STATE_CONFLICT，并带 Retry-After（建议 1–2 秒）。
	// **不要退化成「当作首次执行」** —— 那正是重复扣款的路径。
	ErrIdempotencyInProgress = errors.New("同一 Idempotency-Key 的请求正在处理中")

	// ErrIdempotencyKeyStale：键在主键上冲突了，但按 expires_at 已过期。
	//
	// 成因是 SQL 的一处不对称：ClaimIdempotencyKey 的 ON CONFLICT 认的是主键 `key`（不看过期），
	// 而 GetIdempotencyKey 带 `expires_at > now()` 过滤。于是过期未清理的行会卡住同名键。
	// → 409 STATE_CONFLICT。
	//
	// 🔴 这不是理论边界：CleanupExpiredIdempotencyKeys 必须真的被定时调起来
	// （Cloud Scheduler → /internal/tasks/*），否则 24 小时后开始出现无法解释的下单失败。
	// TODO(P2): 把 cleanup 挂进定时任务清单。
	ErrIdempotencyKeyStale = errors.New("Idempotency-Key 已过期但尚未清理")
)

// IdempotencyStore 是幂等骨架所需的最小数据库能力。
// 收窄成接口而不是吃 *store.Store，是为了单测不必起真库；
// *dbgen.Queries 与 *store.Store 都自动满足它。
type IdempotencyStore interface {
	ClaimIdempotencyKey(ctx context.Context, arg dbgen.ClaimIdempotencyKeyParams) (dbgen.IdempotencyKey, error)
	GetIdempotencyKey(ctx context.Context, key string) (dbgen.IdempotencyKey, error)
	CompleteIdempotencyKey(ctx context.Context, arg dbgen.CompleteIdempotencyKeyParams) error
}

// IdempotentRequest 描述一次待幂等化的请求。
type IdempotentRequest struct {
	// Key 是客户端提供的幂等键，用 ReadIdempotencyKey 从请求头取。
	Key string
	// UserID 为 nil 表示无用户上下文（如内部任务）。
	UserID *int64
	// Endpoint 是逻辑端点标识，建议直接用 operationID（如 "CreateOrder"）。
	// 用 operationID 而不是 URL path：path 里带 {id} 参数时同一端点会散成无数个值。
	Endpoint string
	// Body 是原始请求体字节。**必须是解析前的原文** ——
	// 用解析后再序列化的结果会让「JSON 键顺序不同但语义相同」变成载荷不一致。
	Body []byte
}

// Fingerprint 计算请求指纹，写进 idempotency_keys.request_hash。
//
// 三个分量都进哈希（用长度前缀分隔，避免拼接歧义：
// 否则 endpoint="ab" + body="c" 与 endpoint="a" + body="bc" 会撞成同一个指纹）。
// UserID 进指纹是为了让「A 的键被 B 拿去用」自动落到 ErrIdempotencyMismatch，
// 不依赖后面那次显式比对也能挡住。
func (req IdempotentRequest) Fingerprint() string {
	h := sha256.New()
	writeLenPrefixed(h, []byte(req.Endpoint))
	if req.UserID != nil {
		writeLenPrefixed(h, []byte("u"+strconv.FormatInt(*req.UserID, 10)))
	} else {
		writeLenPrefixed(h, []byte("anon"))
	}
	writeLenPrefixed(h, req.Body)
	return hex.EncodeToString(h.Sum(nil))
}

func writeLenPrefixed(h io.Writer, b []byte) {
	// sha256 的 Write 永不返回错误（hash.Hash 的契约），忽略返回值是安全的。
	_, _ = h.Write([]byte(strconv.Itoa(len(b)) + ":"))
	_, _ = h.Write(b)
}

// Outcome 是幂等判定的结果。
type Outcome int

const (
	// OutcomeExecute：本次是首次执行，调用方应当真的做业务，
	// 做完后**必须**调 CompleteIdempotent 落盘结果。
	OutcomeExecute Outcome = iota + 1
	// OutcomeReplay：已有完成的结果，调用方应当原样回放，**不要重新执行业务**。
	OutcomeReplay
)

func (o Outcome) String() string {
	switch o {
	case OutcomeExecute:
		return "execute"
	case OutcomeReplay:
		return "replay"
	default:
		return "unknown(" + strconv.Itoa(int(o)) + ")"
	}
}

// Attempt 是 BeginIdempotent 的结果。
type Attempt struct {
	Key     string
	Outcome Outcome

	// 以下两项仅在 Outcome == OutcomeReplay 时有意义。
	// Status 可能为 0 —— response_code 是可空列，历史行或写入方遗漏都会导致空值。
	Status int
	Body   []byte
}

// BeginIdempotent 抢占幂等键，判定「首次执行」还是「重放已有结果」。
//
// 用法：
//
//	att, err := httpx.BeginIdempotent(ctx, db, req)
//	switch {
//	case errors.Is(err, httpx.ErrIdempotencyMismatch): // → 409 STATE_IDEMPOTENCY_MISMATCH
//	case errors.Is(err, httpx.ErrIdempotencyInProgress): // → 409 STATE_CONFLICT + Retry-After
//	case err != nil: // → 500
//	case att.Outcome == httpx.OutcomeReplay:
//	    att.WriteReplay(w)
//	default:
//	    // 真的执行业务，然后 httpx.CompleteIdempotent(ctx, db, att.Key, 201, body)
//	}
//
// ⚠️ 抢占（这次写入）与业务本身**不在同一个事务里**。
// 这是刻意的：把它们放进一个事务，业务失败回滚会连抢占一起回滚，
// 于是重试会被当成首次执行 —— 而「业务已经扣了钱但整体回滚失败」这种半成功状态下，
// 那正好是重复扣款的路径。分开的代价是：业务失败后键会以 in_progress 状态残留到过期，
// 同键重试在 24 小时内一直拿到 ErrIdempotencyInProgress。
// TODO(P2): 需要「失败后允许重试」时，加一条 ReleaseIdempotencyKey 查询在业务失败路径上删键。
func BeginIdempotent(ctx context.Context, db IdempotencyStore, req IdempotentRequest) (*Attempt, error) {
	if err := ValidateIdempotencyKey(req.Key); err != nil {
		return nil, err
	}

	fingerprint := req.Fingerprint()

	// 先尝试抢占。ON CONFLICT DO NOTHING + RETURNING 在冲突时返回 0 行，
	// pgx 的 :one 把 0 行报成 ErrNoRows —— 那就是「键已存在」。
	_, err := db.ClaimIdempotencyKey(ctx, dbgen.ClaimIdempotencyKeyParams{
		Key:         req.Key,
		UserID:      req.UserID,
		Endpoint:    req.Endpoint,
		RequestHash: fingerprint,
	})
	if err == nil {
		return &Attempt{Key: req.Key, Outcome: OutcomeExecute}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	existing, err := db.GetIdempotencyKey(ctx, req.Key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 主键冲突了但按 expires_at 查不到 —— 过期未清理的残留行。见 ErrIdempotencyKeyStale。
			return nil, ErrIdempotencyKeyStale
		}
		return nil, err
	}

	// 🔴 归属校验，在指纹比对**之前**。
	//
	// idempotency_keys 的主键是 `key` 单列（0006_orders.up.sql），
	// 而 api-contract.md §9.1 写的是 `(key, user_id)` 唯一 —— **两者不一致**。
	// 按 DDL 的实际形态，用户 B 用到用户 A 的键时会命中 A 的行。
	// 若不校验归属就直接比指纹，理论上 B 构造出相同载荷即可**读到 A 的响应体**
	// （订单号、支付地址都在里面）。所以这里显式挡一道，不依赖指纹碰巧不同。
	// 归到 mismatch 而不是新错误码：对 B 而言「这个键被别人用了」本身就不该可知。
	if !sameOwner(existing.UserID, req.UserID) || existing.Endpoint != req.Endpoint {
		return nil, ErrIdempotencyMismatch
	}
	if existing.RequestHash != fingerprint {
		return nil, ErrIdempotencyMismatch
	}

	switch existing.Status {
	case idempotencyStatusCompleted:
		att := &Attempt{Key: req.Key, Outcome: OutcomeReplay, Body: existing.ResponseBody}
		if existing.ResponseCode != nil {
			att.Status = int(*existing.ResponseCode)
		}
		return att, nil
	case idempotencyStatusInProgress:
		return nil, ErrIdempotencyInProgress
	default:
		// CHECK 约束保证只有两个值；走到这里说明约束被改过或数据被手工污染。
		return nil, errors.New("idempotency_keys.status 取值意外: " + existing.Status)
	}
}

// CompleteIdempotent 落盘执行结果，供后续同键请求重放。
//
// 必须在业务成功之后调用。body 应当是**最终写给客户端的那份 JSON 字节**
// （含统一响应信封），这样重放时逐字节一致 —— 重放路径不该有任何重新序列化的机会。
func CompleteIdempotent(ctx context.Context, db IdempotencyStore, key string, status int, body []byte) error {
	code := int32(status)
	return db.CompleteIdempotencyKey(ctx, dbgen.CompleteIdempotencyKeyParams{
		Key:          key,
		ResponseCode: &code,
		ResponseBody: body,
	})
}

// WriteReplay 把已存的结果原样写回。
//
// 不加任何「这是重放」的自定义响应头：契约里没有这个头，
// 而重放对客户端本来就应当与首次执行不可区分 —— 那是幂等的定义。
func (a *Attempt) WriteReplay(w http.ResponseWriter) error {
	status := a.Status
	if status == 0 {
		// response_code 是可空列。没存到状态码时退回 200 而不是 0（0 会让 net/http panic）。
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if len(a.Body) == 0 {
		return nil
	}
	_, err := w.Write(a.Body)
	return err
}

// ReadIdempotencyKey 从请求头取幂等键并校验形态。
func ReadIdempotencyKey(r *http.Request) (string, error) {
	key := strings.TrimSpace(r.Header.Get(IdempotencyKeyHeader))
	if key == "" {
		return "", ErrIdempotencyKeyMissing
	}
	if err := ValidateIdempotencyKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// ValidateIdempotencyKey 校验键的长度与字符集。
//
// 字符集收窄到 ASCII 可见字符去掉空白：键会原样进主键、日志与错误消息，
// 放任控制字符进来等于给日志注入开了口子。UUID（契约建议的形态）完全在这个集合内。
func ValidateIdempotencyKey(key string) error {
	if key == "" {
		return ErrIdempotencyKeyMissing
	}
	if len(key) < minIdempotencyKeyLen || len(key) > maxIdempotencyKeyLen {
		return ErrIdempotencyKeyMalformed
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c <= 0x20 || c >= 0x7f {
			return ErrIdempotencyKeyMalformed
		}
	}
	return nil
}

// sameOwner 比较两个可空的 user_id。两边都为 nil 视为同一归属（内部任务场景）。
func sameOwner(stored, want *int64) bool {
	if stored == nil || want == nil {
		return stored == nil && want == nil
	}
	return *stored == *want
}
