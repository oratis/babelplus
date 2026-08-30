// Package audit 把管理面的审计写入与业务写入绑在**同一个事务**里。
//
// api-contract.md §6.3 的三条硬规则，第一条是承重的：
//
//	「审计写入与业务写入在同一个事务里。审计写失败 → 整个操作回滚。
//	  这是『审计不可绕过』唯一可靠的实现方式；异步写审计等于承认审计可能缺失。」
//
// # 为什么是独立的包，不是 middleware 里的一个函数
//
// 三条理由，第三条是决定性的：
//
//  1. **职责不同。** middleware 回答「这个请求是谁发的」，每请求跑一次；
//     审计回答「他做了什么」，只在写操作里跑，而且要拿到业务事务的句柄。
//  2. **依赖方向不同。** 审计需要 pgx.Tx 与 dbgen.Queries；
//     把它塞进 middleware 会让整个中间件包多背一份数据库写入能力，
//     而中间件包目前只读不写（TouchKeyLastUsed 是唯一的例外，且是异步允许失败的）。
//  3. 🔴 **放在 middleware 里会诱发一个错误的实现。** 中间件天然运行在
//     handler 之外，一旦审计 helper 长在那里，「在中间件里统一写审计」
//     就会变成一个看起来很干净的选择 —— 而那正是 §6.3 禁止的：
//     中间件拿不到业务事务，只能另开一次写，于是「业务成功、审计失败」
//     变成一个静默的可能。独立成包并且**只接受事务句柄**，
//     让「在事务外写审计」这件事需要刻意绕路才能做到。
//
// # 一处与契约的**已知偏差**，必须记下来
//
// §6.3 给的 DDL 是 `admin_audit_log(... request_id text not null ...)`，
// 而库里实际存在的表是 `audit_logs`（0011_ops.up.sql），列名与列集都不同：
// 没有 request_id，多了 admin_email_snapshot。本包写的是**实表**。
//
// 🔴 `request_id` 目前**没有落库**。它是「把一条审计记录接回访问日志/trace」
// 的唯一钥匙，缺了它，排查「这次 D6 到底是哪个请求」只能靠时间戳去猜。
// 补它需要一条新 migration 加列 + 重新生成 db/gen —— 两者都不在本轮范围内。
// TODO(P2): migration 加 `audit_logs.request_id text`，然后在 Actor 上加回该字段。
//
// # append-only
//
// 表本身由 DB 层 REVOKE UPDATE/DELETE 强制（0011 的注释）。
// 本包也只提供 INSERT：没有 Update，没有 Delete，将来也不要加。
// §6.1 写得很直白：「一个能被清理的审计日志等于没有审计日志」。
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dbgen "github.com/oratis/babelplus/api/db/gen"
)

// Actor 是「谁做的」。字段全部由调用方从 mw.AdminAuth + *http.Request 填。
//
// 本包刻意**不 import middleware**：反过来的依赖（middleware → audit）
// 将来可能需要（比如登录审计），留着单向依赖会在那天变成循环。
// 多写三行赋值换一个不会打结的依赖图。
type Actor struct {
	// AdminID 写进 audit_logs.admin_user_id（外键，ON DELETE SET NULL）。
	AdminID int64
	// Email 写进 admin_email_snapshot。**这是快照，不是外键** ——
	// 管理员被删之后 admin_user_id 会变成 NULL，那时这一列是唯一还能指认到人的证据。
	// 取 admin_users 里那一份，不要取 IAP 断言里那一份（见 mw.AdminAuth.Email）。
	Email string
	// IP 是来源地址，走 mw.ClientIP 取（是否信任 XFF 由配置决定）。
	IP netip.Addr
	// UserAgent 可为空。
	UserAgent string
}

// Entry 是「做了什么」。
type Entry struct {
	// Action 形如 `D6.order.mark_paid`（0011 的列注释给的就是这个形态）。
	Action string
	// TargetType / TargetID 形如 `order` / `20260816T7K2M9Q4`。
	// TargetID 是 text 而不是数字：不同实体的主键类型不同（订单是 trade_no）。
	TargetType string
	TargetID   string
	// Before / After 存**变更字段的完整快照，不是 diff**（§6.3 第 2 条：
	// 「diff 需要靠对面的数据重建，而对面的数据可能已经被改了三次」）。
	// nil 表示这一侧不适用（创建操作没有 before，删除操作没有 after），写进库是 SQL NULL。
	Before any
	After  any
	// Reason 对应 §6.2 L2（必填原因，≥ 8 字符）。长度校验是 handler 的事 ——
	// 本包不做，因为「哪些操作要求 reason」是业务问题，写死在这里会让
	// 不要求 reason 的操作也被卡住。
	Reason string
}

// maxUserAgentLen 是写库前的 UA 截断长度。
//
// user_agent 是 text（无上限），而 UA 是**完全由调用方控制的字符串**。
// 不截断的话，一次带 8 MB UA 头的管理操作会往审计表里塞 8 MB ——
// 而这张表是 append-only、永不删除的。512 足够容纳任何真实浏览器的 UA。
const maxUserAgentLen = 512

// insertSQL 是唯一的写入语句。
//
// 手写 SQL 而不是 sqlc：本轮不允许改 api/db/gen/，而 db/queries/ 下
// 目前没有任何 audit_logs 的查询。
// TODO(P2): 搬进 db/queries/audit.sql 并 `make gen-db`，
// 手写 SQL 的风险是列改名后没有编译期信号。
const insertSQL = `
INSERT INTO audit_logs (
	admin_user_id, admin_email_snapshot, action, target_type, target_id,
	before_value, after_value, reason, request_ip, user_agent
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

// Execer 是写一条审计所需的最小能力。
// pgx.Tx、*pgxpool.Pool、dbgen.DBTX 都满足它。
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Beginner 是开事务的能力。*pgxpool.Pool 满足它。
//
// 不直接吃 *store.Store：一是单测不必起真库，二是避免 store → audit 的反向依赖
// 在将来变成循环。
type Beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Write 在给定的执行器上写一条审计。
//
// ⚠️ **直接调用它是有风险的**：传进来的 Execer 如果是连接池而不是事务，
// 审计就落在业务写入之外，§6.3 第 1 条随即失效。
// 正常路径请用 InTx —— 它在类型上就保证了两者同事务。
// 保留 Write 导出，是因为「一次纯粹的读操作也要留痕」（如 D14 导出）
// 确实没有业务事务可搭。
func Write(ctx context.Context, db Execer, a Actor, e Entry) error {
	if err := validate(a, e); err != nil {
		return err
	}
	before, err := marshalSnapshot("before", e.Before)
	if err != nil {
		return err
	}
	after, err := marshalSnapshot("after", e.After)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, insertSQL,
		a.AdminID, a.Email, e.Action, e.TargetType, e.TargetID,
		before, after, nullable(e.Reason), a.IP, nullable(truncateUTF8(a.UserAgent, maxUserAgentLen)))
	if err != nil {
		return fmt.Errorf("写审计日志失败: %w", err)
	}
	return nil
}

// InTx 跑一次「业务写入 + 审计写入」的原子操作。
//
// fn 里做业务写入，并**返回**这次操作的审计条目 —— 顺序是刻意的：
// after 快照往往要等业务写完才拿得到（比如订单状态机跑完之后的新状态）。
//
// 🔴 **签名上不给「不写审计」留出口**：fn 必须返回一个 Entry，
// 而 Entry 的三个必填字段为空时 validate 会让整个事务回滚。
// 也就是说「忘了写审计」的现象是**业务操作失败**，不是「操作成功但没留痕」。
// 这正是 §6.3 想要的形状：审计不可绕过。
//
// 用法：
//
//	err := audit.InTx(ctx, st.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
//	        before, err := q.GetOrder(ctx, tradeNo)
//	        ...
//	        return audit.Entry{Action: "D6.order.mark_paid", TargetType: "order", TargetID: tradeNo,
//	                Before: before, After: after, Reason: req.Reason}, nil
//	})
func InTx(ctx context.Context, db Beginner, a Actor, fn func(context.Context, *dbgen.Queries) (Entry, error)) error {
	// 先校验 Actor：一个缺 IP 或缺 admin_id 的调用连事务都不必开。
	// 放在这里而不是只在 Write 里，是为了让业务写入根本不发生 ——
	// 否则失败虽然会回滚，但白跑一趟。
	if err := validateActor(a); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	// 已提交时 Rollback 返回 ErrTxClosed，忽略即可（与 store.InTx 同）。
	defer func() { _ = tx.Rollback(ctx) }()

	entry, err := fn(ctx, dbgen.New(tx))
	if err != nil {
		return err
	}
	// 审计写在业务写之后、提交之前。失败 → 不提交 → 业务写入一起回滚。
	if err := Write(ctx, tx, a, entry); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}
	return nil
}

func validate(a Actor, e Entry) error {
	if err := validateActor(a); err != nil {
		return err
	}
	// 三个 NOT NULL 列。空串能过 NOT NULL，但一条 action 为空的审计记录
	// 与没有这条记录是等价的 —— 在这里挡住，而不是留给读日志的人去猜。
	if e.Action == "" {
		return errors.New("审计条目缺少 action")
	}
	if e.TargetType == "" || e.TargetID == "" {
		return errors.New("审计条目缺少 target_type / target_id")
	}
	return nil
}

func validateActor(a Actor) error {
	if a.AdminID <= 0 {
		return errors.New("审计条目缺少 admin_user_id")
	}
	if a.Email == "" {
		return errors.New("审计条目缺少 admin_email_snapshot")
	}
	// request_ip 是 NOT NULL inet。
	//
	// 这里**不像** handler/subscription.go 的 clientAddr 那样回退到 0.0.0.0：
	// 那张表（subscription_fetch_log）是启发式的共享检测，记错一条无所谓；
	// 这张表是证据。一条写着 0.0.0.0 的审计记录会在事后被当成真实来源读，
	// 而它其实什么都没说。宁可让操作失败 —— 那至少是**响亮**的。
	if !a.IP.IsValid() {
		return errors.New("审计条目缺少合法的来源 IP")
	}
	return nil
}

// marshalSnapshot 把快照编成 jsonb。
//
// nil → nil（SQL NULL），而不是 JSON 的 `null` 字面量：两者在 jsonb 里
// 是不同的值，查询 `WHERE before_value IS NULL` 只命中前者。
func marshalSnapshot(field string, v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("审计 %s 快照序列化失败: %w", field, err)
	}
	return b, nil
}

// nullable 把空串变成 SQL NULL。
// reason / user_agent 都是可空列，存空串会让「没填」与「填了个空」不可区分。
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// truncateUTF8 按字节截断但不切断 UTF-8 字符。
//
// 直接 s[:n] 会在多字节字符中间切开，产生非法 UTF-8 ——
// 而 Postgres 的 text 列会**拒绝**非法 UTF-8（不是静默替换），
// 于是一次带中文 UA 的管理操作会因为审计写失败而整个回滚。
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
