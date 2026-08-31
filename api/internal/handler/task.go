package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/httpx"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 内部定时任务面（`/internal/tasks/*`，api-contract §7 的九个 operation）。
//
// 调用方是 Cloud Scheduler 与 Cloud Tasks，凭据是 Google 签发的 OIDC ID token
// （mw.AuthenticateInternal 已挂在这九条路由上，身份从 mw.InternalCallerFrom 取）。
//
// ---- 本文件的五条实现纪律 ----
//
//  1. **每个任务的业务体都是一个吃窄接口的自由函数**（runAliveGc / runExpireCheck / …），
//     handler 方法只负责取依赖、翻译错误、写响应。
//     理由与 node.go 的第 1 条相同：Server.db 是具体类型 *store.Store，塞不了假实现；
//     窄接口是这里唯一能被单测覆盖的形状（httpx.IdempotencyStore 是同一手法的先例）。
//     *dbgen.Queries 与 *store.Store 都自动满足这些接口，所以生产路径没有额外一层。
//
//  2. 🔴 **重复投递是常态，不是异常**（api-contract §7 第 3 条）。
//     每个任务要么天然幂等（DELETE … WHERE 时间条件 / UPDATE … WHERE 状态条件），
//     要么用一把显式的键抢占。**没有第三种**。新增任务时先回答「重投两次会怎样」。
//
//  3. **Scheduler 触发的任务不因「没配好外部依赖」而返回非 2xx。**
//     Cloud Scheduler 的失败会进告警，而「ESP 还没接」是一个已知的、计划内的状态 ——
//     让它每分钟刷一条告警，结果是所有人学会忽略这个告警，
//     然后真的故障发生时也被一起忽略。未配置 → 200 + WARN 日志；
//     配置了但打不通 → 503 + Retry-After（那才是 ErrDependencyDown 的语义）。
//
//  4. **响应体是裸 JSON，不套统一信封**（api-contract §7 的表格最后一列：`{"ok":true}`）。
//     错误路径才用信封（生成代码里 403/500/503 绑的就是 ErrorEnvelope）。
//
//  5. **外部依赖一律注入**：链上 RPC 与 ESP 都是接口，默认实现返回「未配置」。
//     不要在这一层出现任何第三方 endpoint 字面量 —— ADR 0012 现在是「提案，未批准」，
//     把 TronGrid 的地址写进代码等于替一个还没批准的裁决做了实现选择。

// ============================================================
// 可注入的外部依赖
// ============================================================

// ErrChainScannerNotConfigured 表示链上扫描器没有接线。
//
// 它是**正常状态**而不是故障：ADR 0012 还是提案，第一阶段不连任何链上 RPC。
// 调用方据此走「优雅退出」而不是「报错」——见本文件纪律第 3 条。
var ErrChainScannerNotConfigured = errors.New("链上扫描器未配置")

// ErrMailSenderNotConfigured 表示 ESP 没有接线（auth.go 的 TODO(P1) 是同一个缺口）。
var ErrMailSenderNotConfigured = errors.New("ESP 未配置")

// ChainTransfer 是一笔链上转账，扫描器返回、handler 落库。
//
// 字段的取舍全部围绕「幂等键从哪来」与「凭什么判定收到了钱」：
type ChainTransfer struct {
	// TxID + LogIndex 组成 payments.external_id（`txid:log_index`）。
	// 🔴 幂等键的取值来源**只有链上事件**（ADR 0012 §8.2）：
	// 用「录入动作的 ID」当键的写法根本不幂等 —— 点两次 = 两个键 = 两次入账。
	TxID     string
	LogIndex int32

	FromAddress string
	ToAddress   string

	// AmountUSDT6 是 1e-6 USDT 的整数。判定一律在这个整数域做（ADR 0012 §17.3）——
	// numeric(38,18) 那一列容得下链上不可能出现、且互不相等的噪声值。
	AmountUSDT6 int64

	// Confirmations 只用于展示进度（openapi 的 confirmations_required 下发 19）。
	Confirmations int32

	// 🔴 Solidified 才是**服务端的实际判据**（ADR 0012 §10.5）：
	// TRON 的最终性是「固化」而不是 N 个确认。拿 Confirmations >= 19 当判据，
	// 在链重组时会开通一个没付钱的订阅。
	Solidified bool

	// BlockTimeMS 用来推进 pay_addresses.cursor_ts（毫秒整数，原样回传给上游 API）。
	BlockTimeMS int64

	// Raw 是链上原始事件。payments.raw 是 NOT NULL 且刻意如此：
	// 入账争议（用户说打了、我们说没收到）只能靠原文解决。
	Raw json.RawMessage
}

// ChainScanner 是链上拉取能力。
//
// 抽成接口的两个理由，第二个是决定性的：
//  1. 单测不必联网；
//  2. **ADR 0012 是「提案，未批准」** —— 接口让「用哪家 RPC」这个还没裁决的问题
//     停留在装配层，而不是被今天的一行 URL 常量替我们裁掉。
type ChainScanner interface {
	// Name 写进日志与 payments.provider 的推导，形如 "trongrid"。
	Name() string
	// Configured 为 false 时 handler 优雅退出（200），不调 Scan。
	Configured() bool
	// Scan 拉取 address 上自 cursorMS 之后的转入。cursorMS 为 nil 表示首次扫描。
	Scan(ctx context.Context, chain, address string, cursorMS *int64) ([]ChainTransfer, error)
}

// MailMessage 是一封待发的信。
type MailMessage struct {
	To       string
	Subject  string
	Template string
	UserID   *int64
	// Body 是渲染好的纯文本正文。渲染发生在入口侧：队列信在 runMailSend 按模板键渲染
	// （bodyForQueuedTemplate），验证码信在 issueVerification 用明文码渲染 ——
	// 码只在签发那一刻存在，库里只有哈希，所以那类信不能走队列（mailwire.go 有全套理由）。
	Body string
}

// MailSender 是发信能力。同样是接口而不是具体实现：ESP 未选型（ADR 0002 §7 要求
// 先用真实送达率数据再选），把 SES 或 Resend 写死在这里等于替那个决定做了选择。
type MailSender interface {
	// Name 写进 email_log.esp（NOT NULL 列），未配置时也要有值 ——
	// 「这批信当时准备用谁发」在事后排查里比 NULL 有用。
	Name() string
	Configured() bool
	// Send 返回 ESP 侧的消息 ID，回写 email_log.provider_msg_id。
	Send(ctx context.Context, msg MailMessage) (providerMsgID string, err error)
}

type unconfiguredChainScanner struct{}

func (unconfiguredChainScanner) Name() string     { return "unconfigured" }
func (unconfiguredChainScanner) Configured() bool { return false }
func (unconfiguredChainScanner) Scan(context.Context, string, string, *int64) ([]ChainTransfer, error) {
	return nil, ErrChainScannerNotConfigured
}

type unconfiguredMailSender struct{}

func (unconfiguredMailSender) Name() string     { return "unconfigured" }
func (unconfiguredMailSender) Configured() bool { return false }
func (unconfiguredMailSender) Send(context.Context, MailMessage) (string, error) {
	return "", ErrMailSenderNotConfigured
}

// 默认实现。**只读，生产代码里不允许重新赋值** ——
// 包级可变状态在多测试并发时是共享的，改一处会让另一个测试莫名其妙地挂。
// 单测的注入方式是把自己的实现直接传给 runChainScan / runMailSend，不碰这两个变量。
//
// TODO(P1): 扫描器的装配注入同发信（server.go 的 New 里装 Server 字段）——
// 但它先等 ADR 0012 批准（用哪家 RPC 未裁决），批准前保持未配置。
var (
	defaultChainScanner ChainScanner = unconfiguredChainScanner{}
	defaultMailSender   MailSender   = unconfiguredMailSender{}
)

func (s *Server) chainScanner() ChainScanner { return defaultChainScanner }

// mailSender 返回装配好的 ESP 实现；未配置（Server.mail 为 nil）时回退到
// unconfiguredMailSender，发信路径按「ESP 未配置」优雅跳过。
func (s *Server) mailSender() MailSender {
	if s.mail != nil {
		return s.mail
	}
	return defaultMailSender
}

// ============================================================
// 常量
// ============================================================

const (
	// 一次 order-timeout 最多处理 orderTimeoutBatchSize × orderTimeoutMaxPasses 张订单。
	// 分批而不是一条 UPDATE 扫全表：单条语句要持有全部命中行的锁到提交，
	// 积压时那就是一次长事务卡住整个下单路径。封顶让单次请求的时长可预测 ——
	// Scheduler 的调用有超时，跑不完剩下的下一分钟继续，没有任何状态会丢。
	orderTimeoutBatchSize = 200
	orderTimeoutMaxPasses = 5

	// 每小时一次的重置扫描。整批在一个事务里跑（见 runTrafficReset 的注释），
	// 所以这个数字同时是「最长事务持有多少行锁」的上限。
	trafficResetBatchSize = 200

	// 提醒扫描。
	remindSweepBatchSize      = 500
	remindExpireWithinDays    = 3  // 到期前 3 天提醒
	remindTrafficThresholdPct = 80 // 用量达到配额的 80% 提醒

	// email_log.template 的取值。0011 的列注释给的示例就是这个形态
	// （'verify_code' / 'domain_broadcast' / 'expire_remind'）。
	// 🔴 它同时是提醒的幂等键的一部分（user_id, template, 当天），改字符串
	//    = 当天所有人再收一遍。
	templateExpireRemind  = "expire_remind"
	templateTrafficRemind = "traffic_remind"

	subjectExpireRemind  = "您的订阅即将到期"
	subjectTrafficRemind = "您的流量即将用尽"

	// 一次 chain-scan 扫多少个地址。1 分钟一次 × 100 个地址，
	// 对上游 API 的额度是可算的；地址按 last_scanned_at 轮转，不会饿死。
	chainScanBatchSize = 100

	// 游标往回退 10 分钟重扫，防止边界漏读（ADR 0012 §10.5）。
	// 重复由 payments 的 UNIQUE (provider, external_id) 兜底，所以回看是免费的。
	chainScanLookbackMS = int64(10 * 60 * 1000)

	// 数据保留期（data-model §13）。写成常量而不是散在调用点，
	// 是因为这些数字是**合规承诺**，改动要能一眼看见改了哪一个。
	retentionStatsDays             = 3 * 365 // 统计 3 年
	retentionWebhookEventsDays     = 2 * 365 // 拒付申诉的证据窗口 2 年
	retentionSubscriptionFetchDays = 90      // 订阅拉取日志 90 天

	// 503 时给的退避秒数。比 Scheduler 的最小间隔（1 分钟）小，
	// 让重投落在下一个自然周期之前 —— 收款延迟对用户是可见的。
	dependencyDownRetryAfter int32 = 30
)

// ============================================================
// 响应构造
// ============================================================

// taskAck 是内部面的成功响应体（裸 {"ok":true}，不套信封）。
func taskAck() gen.InternalTaskAckJSONResponse {
	return gen.InternalTaskAckJSONResponse{Ok: true}
}

// taskAckSkipped 表示这次是重复投递、已幂等丢弃。
// 仍然是 200：对 Cloud Tasks 而言「已经处理过」就是成功，回非 2xx 只会招来更多重投。
func taskAckSkipped() gen.InternalTaskAckJSONResponse {
	skip := true
	return gen.InternalTaskAckJSONResponse{Ok: true, IdempotentSkip: &skip}
}

// dependencyDown 构造 503。**必带 Retry-After**（openapi 的 ErrDependencyDown 原文要求）。
func (s *Server) dependencyDown(ctx context.Context, msg string, err error) gen.ErrDependencyDownJSONResponse {
	s.logger.ErrorContext(ctx, msg, "err", err, "request_id", middleware.RequestIDFrom(ctx))
	return gen.ErrDependencyDownJSONResponse{
		Body: s.envelope(ctx, gen.INTERNALDEPENDENCYDOWN, msg),
		Headers: gen.ErrDependencyDownResponseHeaders{
			RetryAfter: dependencyDownRetryAfter,
			XRequestId: middleware.RequestIDFrom(ctx),
		},
	}
}

// taskLogger 给任务日志统一挂上调用方身份。
//
// mw.InternalCallerFrom 返回 false 表示这条路由**没挂**内部面鉴权（装配错误）。
// 这里不因此失败请求 —— 到不了 handler 的请求根本不存在，
// 而把装配错误变成 500 会让「谁调的」这个次要信息毁掉整个任务。
// 但它必须在日志里可见：caller=<none> 出现就是接线掉了。
func (s *Server) taskLogger(ctx context.Context, task string) *slog.Logger {
	caller := "<none>"
	if c, ok := middleware.InternalCallerFrom(ctx); ok {
		caller = c.Email
	}
	return s.logger.With("task", task, "caller", caller, "request_id", middleware.RequestIDFrom(ctx))
}

// ============================================================
// 1 · alive-gc（Scheduler，5 分钟）
// ============================================================

type aliveGcQuerier interface {
	CleanupStaleDeviceState(ctx context.Context) (int64, error)
	CleanupUsedTotp(ctx context.Context) (int64, error)
	CleanupExpiredIdempotencyKeys(ctx context.Context) (int64, error)
}

type aliveGcResult struct {
	DeviceRows      int64
	TotpRows        int64
	IdempotencyRows int64
}

// runAliveGc 跑三条同频率、同性质的清理。
//
// 🔴 契约对本端点只写了第一条（`DELETE FROM user_alive WHERE seen_at < …`，
//
//	 实际的表叫 user_device_state）。另外两条放进来是刻意的，理由是**它们没有别的家**，
//	 而且两处 schema 注释都逐字点名了「由 /internal/tasks/* 定期清理」：
//
//	- used_totp：0015_payment_fixes.up.sql:110。不清理不会有安全问题（主键仍拒绝重放），
//	  但这张表只增不减，且它在 D6 那条高频路径上。
//	- idempotency_keys：httpx/idempotency.go 的 ErrIdempotencyKeyStale 注释写着
//	  「🔴 这不是理论边界：CleanupExpiredIdempotencyKeys 必须真的被定时调起来，
//	  否则 24 小时后开始出现无法解释的下单失败」。**在此之前没有任何代码调它。**
//	  这是本轮把它接上的唯一机会 —— 新开一个 GC 端点要改 openapi（契约冻结）。
//
// 三条都是「按时间条件 DELETE」，天然幂等，重投无副作用。
//
// ⚠️ 一条失败不中断其余两条：它们互不依赖，而「因为 TOTP 表清不掉所以在线态也不清」
//
//	会把一个小问题放大成设备数限制失效。错误合并后一起返回，让 Scheduler 重试整体。
func runAliveGc(ctx context.Context, q aliveGcQuerier, log *slog.Logger) (aliveGcResult, error) {
	var res aliveGcResult
	var errs []error

	if n, err := q.CleanupStaleDeviceState(ctx); err != nil {
		errs = append(errs, fmt.Errorf("清理在线设备态失败: %w", err))
	} else {
		res.DeviceRows = n
	}

	if n, err := q.CleanupUsedTotp(ctx); err != nil {
		errs = append(errs, fmt.Errorf("清理 TOTP 防重放痕迹失败: %w", err))
	} else {
		res.TotpRows = n
	}

	if n, err := q.CleanupExpiredIdempotencyKeys(ctx); err != nil {
		errs = append(errs, fmt.Errorf("清理过期幂等键失败: %w", err))
	} else {
		res.IdempotencyRows = n
	}

	log.InfoContext(ctx, "在线态清理完成",
		"device_rows", res.DeviceRows,
		"totp_rows", res.TotpRows,
		"idempotency_rows", res.IdempotencyRows)
	return res, errors.Join(errs...)
}

// RunAliveGcTask 实现 POST /internal/tasks/alive-gc。
func (s *Server) RunAliveGcTask(ctx context.Context, _ gen.RunAliveGcTaskRequestObject) (gen.RunAliveGcTaskResponseObject, error) {
	log := s.taskLogger(ctx, "alive-gc")
	if _, err := runAliveGc(ctx, s.db, log); err != nil {
		return gen.RunAliveGcTask500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "在线态清理失败", err),
		}, nil
	}
	return gen.RunAliveGcTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}, nil
}

// ============================================================
// 2 · expire-check（Scheduler，5 分钟）
// ============================================================

type expireCheckQuerier interface {
	SweepExpiredUsers(ctx context.Context) ([]dbgen.SweepExpiredUsersRow, error)
	ExpireTrafficPacks(ctx context.Context) ([]dbgen.ExpireTrafficPacksRow, error)
	BumpUserRevByGroups(ctx context.Context, groupIds []int64) (int64, error)
}

type expireCheckResult struct {
	ExpiredUsers  int
	ExpiredPacks  int
	Groups        int
	BumpedServers int64
}

// runExpireCheck 把「时间流逝」变成一次写，并把结果推给节点。
//
// 🔴 **本任务是 ETag 设计里唯一一条没有第二个写入者能兜底的路径**
//
//	（api-contract §2 第 7 条 / §3.8 bump 规则第 4 条）。
//	封禁、改套餐、改分组都是有人按了按钮的写操作；到期没有按钮。
//	不跑它，到期用户会一直留在 ListAvailableUsersByServer 的结果里，
//	而节点因为 user_rev 没变永远收 304 —— 没有报错、没有告警，只有「到期了还能用」。
//
// 🔴 **为什么在触发器已经会 bump 的情况下还要显式 bump 一次。**
//
//	0012 的 users_bump_user_rev_trg 把 expiry_applied_at 放进了监视列表，
//	所以 SweepExpiredUsers 这条 UPDATE 自己就会 bump。那为什么不省掉这一步？
//
//	因为 0012 的文件头**自带撤回条件**：「当所有写路径都收敛到 3–5 个明确的 service
//	方法时，应当把触发器改回显式调用并删掉它」。那一天到来时，删触发器的人会逐个检查
//	写路径并补上显式调用 —— 而**到期这条路径在 Go 代码里看不到任何写**
//	（sqlc.yaml 已登记「触发器在生成的 Go 代码里是隐形的」）。
//	它正是那次重构里最可能被漏掉的一条，而漏掉的现象是静默的。
//
//	代价核算：user_rev 一次前进 2 而不是 1。节点每 60 秒只比对一次 ETag，
//	中间前进了几次它看不见 —— **额外的那一次不产生任何多余请求**。代价确实是零。
//
// 加油包到期（ExpireTrafficPacks）放在同一个任务里：它就是到期，只不过到期的是配额。
// 0016 为它建了 users_pack_expiry_due_idx 却一直没有调用方，这里补上。
func runExpireCheck(ctx context.Context, q expireCheckQuerier, log *slog.Logger) (expireCheckResult, error) {
	var res expireCheckResult

	expired, err := q.SweepExpiredUsers(ctx)
	if err != nil {
		return res, fmt.Errorf("扫描到期用户失败: %w", err)
	}
	res.ExpiredUsers = len(expired)

	packs, err := q.ExpireTrafficPacks(ctx)
	if err != nil {
		return res, fmt.Errorf("回收到期加油包失败: %w", err)
	}
	res.ExpiredPacks = len(packs)

	// 两批用户合起来去重，一条语句 bump 完。
	// 去重不是优化：同一个分组被 bump 两次就是 user_rev 白白多走一格，
	// 而分组数远小于用户数（一个分组通常几百人），去重把往返压到 1 次。
	groups := make(map[int64]struct{}, len(expired)+len(packs))
	for _, u := range expired {
		groups[u.GroupID] = struct{}{}
	}
	for _, u := range packs {
		groups[u.GroupID] = struct{}{}
	}
	if len(groups) == 0 {
		// 平时的正常结果就是这里 —— 命中 0 行，什么都不做。
		// 不打 Info 日志：每 5 分钟一条「没事发生」会把日志淹掉，
		// 而「任务有没有在跑」应当由 Cloud Scheduler 自己的执行记录回答。
		return res, nil
	}

	ids := make([]int64, 0, len(groups))
	for g := range groups {
		ids = append(ids, g)
	}
	res.Groups = len(ids)

	bumped, err := q.BumpUserRevByGroups(ctx, ids)
	if err != nil {
		// 🔴 这里失败意味着「用户已被标记到期，但节点不知道」。
		// 返回错误让 Scheduler 重投：SweepExpiredUsers 是幂等的（expiry_applied_at
		// 已非 NULL 的行不会再被选中），所以重投只会重跑 bump —— 而那正是我们要的。
		// ⚠️ 代价：重投时 expired/packs 都是空的，于是 groups 也是空的，bump **不会**重跑。
		//    真正的兜底是触发器已经 bump 过一次（见上面的长注释）。
		//    这条错误的价值因此是**告警**，不是自愈。
		return res, fmt.Errorf("bump user_rev 失败（到期已标记但节点未收到）: %w", err)
	}
	res.BumpedServers = bumped

	log.InfoContext(ctx, "到期扫描完成",
		"expired_users", res.ExpiredUsers,
		"expired_packs", res.ExpiredPacks,
		"groups", res.Groups,
		"bumped_servers", res.BumpedServers)
	return res, nil
}

// RunExpireCheckTask 实现 POST /internal/tasks/expire-check。
//
// 整体一个事务：标记到期与 bump 必须同生共死。分开的话，
// 「标记成功、bump 失败」会留下一个**在数据库里看起来已处理、但节点永远不知道**的状态，
// 而下一轮扫描不会再选中那些行（expiry_applied_at 已非 NULL）——
// 于是那批用户直到下一次任意 users 写入之前都还能上网。
func (s *Server) RunExpireCheckTask(ctx context.Context, _ gen.RunExpireCheckTaskRequestObject) (gen.RunExpireCheckTaskResponseObject, error) {
	log := s.taskLogger(ctx, "expire-check")
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		_, txErr := runExpireCheck(ctx, q, log)
		return txErr
	})
	if err != nil {
		return gen.RunExpireCheckTask500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "到期扫描失败", err),
		}, nil
	}
	return gen.RunExpireCheckTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}, nil
}

// ============================================================
// 3 · order-timeout（Scheduler，1 分钟）
// ============================================================

type orderTimeoutQuerier interface {
	balanceReleaser
	ExpireTimedOutOrders(ctx context.Context, batch int32) ([]dbgen.ExpireTimedOutOrdersRow, error)
}

type orderTimeoutResult struct {
	Orders        int
	WatchExtended int
	Passes        int
}

// runOrderTimeout 关闭超时未付的订单，**但绝不停止监听它们的收款地址**。
//
// 🔴 后半句才是这个任务的重点。user-journey §7 把「钱进黑洞」判定为最不可挽回的失败模式：
//
//	用户在倒计时结束前一秒付了款，我们关掉订单、回收/停扫地址，那笔 USDT 就真的
//	没有任何人会发现 —— 用户投诉时我们连「有没有收到」都答不出来。
//	ExpireTimedOutOrders 用 greatest(...) 把 address_watch_until 顶到至少 24 小时之后，
//	而且**只延长不缩短**（ADR 0012 §11.1 在绑定地址时给的默认是 7 天，不能被这里改小）。
//
// 分批 + 多趟：单条 UPDATE 扫全表要把全部命中行的锁持有到提交，积压时那是一次长事务，
// 会卡住整个下单路径。跑不完剩下的下一分钟继续，没有任何状态会丢
// （状态就在 orders.status 与 expires_at 里，不在内存里）。
//
// 幂等：语句的 WHERE 带 `status IN ('pending','paying','underpaid')`，
// 处理过的订单已是 expired，重投必然命中 0 行。
// orderTimeoutTx 把「一批的事务边界」参数化，好让单测不需要一个真数据库。
// 生产侧传的是 Store.InTx，测试侧传的是「直接调用假实现」。
type orderTimeoutTx func(ctx context.Context, fn func(orderTimeoutQuerier) error) error

func runOrderTimeout(ctx context.Context, inTx orderTimeoutTx, log *slog.Logger) (orderTimeoutResult, error) {
	var res orderTimeoutResult

	for pass := 0; pass < orderTimeoutMaxPasses; pass++ {
		var batch int
		// 🔴 一批一个事务。ExpireTimedOutOrders 自己是原子的，但它不再是本批的全部动作 ——
		//    下单锁定的余额要在同一批里退回，两者必须一起成功或一起回滚。
		//    仍然**一批一事务**而不是整趟一个事务：后者会让多趟之间互相持锁（原注释的理由仍然成立）。
		err := inTx(ctx, func(q orderTimeoutQuerier) error {
			rows, err := q.ExpireTimedOutOrders(ctx, orderTimeoutBatchSize)
			if err != nil {
				return fmt.Errorf("关闭超时订单失败（第 %d 批）: %w", pass+1, err)
			}
			batch = len(rows)

			for _, o := range rows {
				if o.PayAddress != nil {
					res.WatchExtended++
				}
				// 下单时锁定的余额在这里退回，与本批的状态迁移同事务。
				// 先迁移后退款而中间失败，这笔钱就再也没有路径回到用户手上
				// （订单已是 expired，下一趟的 WHERE 不会再命中它）。
				if err := releaseOrderBalance(ctx, q, o.UserID, o.ID, o.TradeNo, o.AmountBalance, "支付窗口到期"); err != nil {
					return fmt.Errorf("退回订单 %s 锁定的余额失败: %w", o.TradeNo, err)
				}
				// 每张订单一条日志：订单是钱，量也小（正常一分钟内个位数）。
				// 带上 from_status 是因为 underpaid → expired 与 pending → expired
				// 的人工处理方式完全不同 —— 前者用户已经付了一部分钱。
				log.InfoContext(ctx, "订单支付窗口到期已关闭",
					"order_id", o.ID, "trade_no", o.TradeNo, "user_id", o.UserID,
					"from_status", string(o.FromStatus),
					"balance_released", o.AmountBalance,
					"watch_until", timestamptzString(o.AddressWatchUntil))
			}
			return nil
		})
		if err != nil {
			return res, err
		}

		res.Passes = pass + 1
		res.Orders += batch
		if batch < orderTimeoutBatchSize {
			break
		}
	}

	if res.Orders > 0 {
		log.InfoContext(ctx, "超时订单清理完成",
			"orders", res.Orders, "watch_extended", res.WatchExtended, "passes", res.Passes)
	}
	return res, nil
}

// RunOrderTimeoutTask 实现 POST /internal/tasks/order-timeout。
//
// **一批一个事务**：ExpireTimedOutOrders 单条 CTE 自己是原子的，但它不再是一批的全部动作
// —— 下单锁定的钱包余额要在同一批里退回。整趟包一个事务会让多趟之间互相持锁，所以按批包。
func (s *Server) RunOrderTimeoutTask(ctx context.Context, _ gen.RunOrderTimeoutTaskRequestObject) (gen.RunOrderTimeoutTaskResponseObject, error) {
	log := s.taskLogger(ctx, "order-timeout")
	inTx := func(ctx context.Context, fn func(orderTimeoutQuerier) error) error {
		return s.db.InTx(ctx, func(q *dbgen.Queries) error { return fn(q) })
	}
	if _, err := runOrderTimeout(ctx, inTx, log); err != nil {
		return gen.RunOrderTimeoutTask500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "关闭超时订单失败", err),
		}, nil
	}
	return gen.RunOrderTimeoutTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}, nil
}

// ============================================================
// 4 · traffic-reset（Scheduler，每小时）
// ============================================================

type trafficResetQuerier interface {
	SuspendResetForPlanlessUsers(ctx context.Context) (int64, error)
	ListUsersDueForReset(ctx context.Context, limit int32) ([]dbgen.ListUsersDueForResetRow, error)
	AdvanceUserResetCycle(ctx context.Context, userID int64) (dbgen.AdvanceUserResetCycleRow, error)
	InsertTrafficResetLog(ctx context.Context, arg dbgen.InsertTrafficResetLogParams) (dbgen.TrafficResetLog, error)
}

type trafficResetResult struct {
	Suspended int64
	Due       int
	Reset     int
	Skipped   int
}

// runTrafficReset 推进到期用户的流量周期。
//
// 🔴 **只调 AdvanceUserResetCycle，绝不调 ResetUserTraffic**（ADR 0013 §5.3）。
//
//	后者现在收窄为「管理员手工重置 / reset_pack」专用。
//	在周期重置路径上先调它会把 u/d 清成 0，于是 carry_pack 恒等于 transfer_enable_pack，
//	加油包**只增不减且完全静默** —— 没有报错、没有告警，只有账对不上。
//	合并成一条 CTE 之后「顺序」这件事在类型层面不存在了，前提是这里不再有人绕过它。
//
// 幂等：AdvanceUserResetCycle 会把 reset_at 推到下一个周期，
// 于是同一个用户在同一小时内不会被 ListUsersDueForReset 选中第二次。
// 契约给的幂等键 `(user_id, reset_period)` 在实现上就是这条推进 —— 不需要额外的键表。
//
// 🔴 先跑 SuspendResetForPlanlessUsers 的理由（顺序不能换）：
//
//	ListUsersDueForReset 用 LEFT JOIN plans，会把 plan_id 为 NULL 的用户也选出来；
//	而 AdvanceUserResetCycle 的 `FROM plans p, cur WHERE p.id = u.plan_id` 是交叉连接，
//	对这些人匹配 0 行 → :one 报 ErrNoRows。不摘出去的话，每小时选中、每小时报错，
//	**永远不收敛**，而真正的问题（这个人没套餐了）被淹在噪声里。
//
// ⚠️ 单个用户失败（ErrNoRows）跳过而不是中断整批：一个没套餐的用户不该让另外 199 个
//
//	人的配额发不下去。真正的数据库错误（连接断、约束冲突）仍然中断 ——
//	那时继续跑只会把同一个错误重复 199 次。
func runTrafficReset(ctx context.Context, q trafficResetQuerier, log *slog.Logger) (trafficResetResult, error) {
	var res trafficResetResult

	suspended, err := q.SuspendResetForPlanlessUsers(ctx)
	if err != nil {
		return res, fmt.Errorf("摘除无套餐用户的重置排期失败: %w", err)
	}
	res.Suspended = suspended
	if suspended > 0 {
		log.WarnContext(ctx, "有用户排了重置但已无套餐，已摘除排期", "users", suspended)
	}

	due, err := q.ListUsersDueForReset(ctx, trafficResetBatchSize)
	if err != nil {
		return res, fmt.Errorf("查询待重置用户失败: %w", err)
	}
	res.Due = len(due)

	for _, u := range due {
		row, advErr := q.AdvanceUserResetCycle(ctx, u.ID)
		if errors.Is(advErr, pgx.ErrNoRows) {
			// 上面那条 SuspendResetForPlanlessUsers 之后仍然 0 行，
			// 说明用户在这两条语句之间被改了（套餐刚被删 / 刚被软删）。
			// 跳过即可 —— 下一轮扫描要么选不中他，要么被 Suspend 摘掉。
			res.Skipped++
			log.WarnContext(ctx, "用户重置未命中（套餐或用户在扫描期间发生变化）", "user_id", u.ID)
			continue
		}
		if advErr != nil {
			return res, fmt.Errorf("推进用户 %d 的重置周期失败: %w", u.ID, advErr)
		}

		// 审计与业务同事务（handler 把整批包在 InTx 里）。
		// 不写这条，「加油包被吃掉了还是结转了」事后无从判断 ——
		// 那正是 ADR 0013 ③ 要防的静默失败，而总额一列看不出来。
		if _, logErr := q.InsertTrafficResetLog(ctx, dbgen.InsertTrafficResetLogParams{
			UserID:                u.ID,
			TriggerSource:         "scheduler",
			ResetMethod:           u.ResetTrafficMethod,
			OldU:                  row.OldU,
			OldD:                  row.OldD,
			NewTransferEnable:     row.TransferEnable,
			NewTransferEnablePack: row.TransferEnablePack,
		}); logErr != nil {
			return res, fmt.Errorf("写用户 %d 的重置审计失败: %w", u.ID, logErr)
		}
		res.Reset++
	}

	if res.Reset > 0 || res.Skipped > 0 {
		log.InfoContext(ctx, "流量周期重置完成",
			"due", res.Due, "reset", res.Reset, "skipped", res.Skipped, "suspended", res.Suspended)
	}
	return res, nil
}

// RunTrafficResetTask 实现 POST /internal/tasks/traffic-reset。
//
// 整批一个事务：重置与它的审计必须同生共死（ADR 0013 §6.1 建 traffic_reset_log
// 的全部理由就是事后能判断结转算没算对；只写了一半的审计比没有审计更糟）。
//
// ⚠️ 已知代价：批内任一用户的审计写失败会回滚整批，包括已经成功重置的那些人。
//
//	他们会在下一小时被重新选中并重置 —— **幂等在这里是自愈的**，
//	因为 reset_at 也一起回滚了。这是选择整批事务而不是逐用户事务的前提；
//	trafficResetBatchSize 同时是「这个事务最长持有多少行锁」的上限。
//	TODO(P2): 用户量让这个事务变长时改成逐用户事务（那时 s.db.InTx 要下沉到循环里）。
func (s *Server) RunTrafficResetTask(ctx context.Context, _ gen.RunTrafficResetTaskRequestObject) (gen.RunTrafficResetTaskResponseObject, error) {
	log := s.taskLogger(ctx, "traffic-reset")
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		_, txErr := runTrafficReset(ctx, q, log)
		return txErr
	})
	if err != nil {
		return gen.RunTrafficResetTask500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "流量周期重置失败", err),
		}, nil
	}
	return gen.RunTrafficResetTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}, nil
}

// ============================================================
// 5 · traffic-batch（Cloud Tasks，每次 push）
// ============================================================

// trafficBatchQuerier 组合幂等骨架与阈值对账。
// 复用 httpx.IdempotencyStore 而不是另造一套抢占（本轮任务书的明确要求）：
// 见下面 runTrafficBatch 对「claimed_at 抢占」如何落在既有实现上的说明。
type trafficBatchQuerier interface {
	httpx.IdempotencyStore
	BumpUserRevForExhaustedUsers(ctx context.Context) (dbgen.BumpUserRevForExhaustedUsersRow, error)
}

type trafficBatchResult struct {
	Skipped        bool
	ExhaustedUsers int64
	BumpedServers  int64
}

// runTrafficBatch 处理一次流量入账任务。
//
// ============================================================
// 🔴 先说清楚这个实现**不是**契约描述的那件事，差在哪里，以及为什么
// ============================================================
//
// api-contract §8.2 的目标形态：`/push` 只 `INSERT traffic_batch` + 入队（预算 < 50 ms），
// 累加、日聚合、阈值判定全部在这个任务里做，幂等靠
// `UPDATE traffic_batch SET claimed_at = now() WHERE batch_id = $1 AND claimed_at IS NULL`。
//
// **那条链路今天不存在**：`traffic_batch` 表没有落进 0001–0017 任何一支 migration，
// `/push` 仍是请求内同步累加（node.go:257 起的长注释登记了这个临时形态与它的两条硬理由）。
// 建表需要新 migration，不在本轮文件范围内。
//
// 所以这个 handler 今天做两件真事：
//
//  1. **把 Cloud Tasks 的重投挡住**。`claimed_at` 那一列不存在，但它要的语义
//     ——「同一个 batch_id 只有第一个到达者能执行」——正是 httpx 幂等骨架的
//     `ClaimIdempotencyKey`（`ON CONFLICT DO NOTHING` + `RETURNING`）。
//     两者都是「靠数据库的原子写决胜负」，不是应用层 SELECT-then-INSERT
//     （后者在两个 Cloud Run 实例并发时会双双通过）。复用它而不是另造一张表：
//     多一套幂等实现就是多一套会漂移的语义。
//     ⚠️ 这治的是 §9.2 表格里「Cloud Tasks 重投」那一行。**「v2node 超时重试」那一行治不了**
//     ——它上报增量字节且不带任何幂等键，服务端无法区分「重试的同一批」与「下一个
//     60 秒窗口的新一批」。不要因为这里有幂等就以为那个缺口被补上了。
//
//  2. **给「跨阈值那一次必须 bump」做兜底对账**。这是本任务真正的业务价值：
//     api-contract §3.8 bump 规则第 3 条漏掉的后果是「配额耗尽的用户永远不会从节点
//     列表消失 = 免费无限上网」，而且**完全静默**。那一条现在写在
//     servers.sql 的 AddNodeTrafficBatch 里，有三种漏法（node_rev 缺行、
//     走了别的入账路径、将来两条路径并存的窗口期）。
//     BumpUserRevForExhaustedUsers 用「该被踢的用户所在分组的 user_rev_at
//     早于该分组最近一次流量写入」这个判据补一次，理由与误报分析见那条查询的注释。
//
// 幂等的可观测性：重复投递返回 idempotent_skip=true（契约 InternalTaskAck 的字段），
// 而不是假装什么都没发生 —— Cloud Tasks 的重投率是需要能看见的。
func runTrafficBatch(
	ctx context.Context,
	q trafficBatchQuerier,
	log *slog.Logger,
	batchID string,
) (trafficBatchResult, error) {
	var res trafficBatchResult

	att, err := httpx.BeginIdempotent(ctx, q, httpx.IdempotentRequest{
		Key: batchID,
		// UserID 留空：调用方是 Cloud Tasks 不是用户。
		Endpoint: "RunTrafficBatchTask",
		// Body 就是 batch_id 本身。载荷指纹在这里退化成「键 = 内容」，
		// 于是 ErrIdempotencyMismatch 在本任务上**不可能**发生 —— 下面仍然处理它，
		// 因为「不可能发生」的分支静默吞掉才是真正危险的写法。
		Body: []byte(batchID),
	})
	switch {
	case err == nil && att.Outcome == httpx.OutcomeExecute:
		// 落到下面真的执行。

	case err == nil: // OutcomeReplay
		log.InfoContext(ctx, "流量入账任务重复投递，已幂等丢弃", "batch_id", batchID)
		res.Skipped = true
		return res, nil

	case errors.Is(err, httpx.ErrIdempotencyInProgress):
		// 同一个 batch_id 的上一次还在执行。丢弃而不是等待：
		// 等待会占住 Cloud Run 的请求配额与数据库连接（每实例池 max=2），
		// 而 Cloud Tasks 本来就会再投一次。
		log.WarnContext(ctx, "流量入账任务与上一次同批并发，已丢弃", "batch_id", batchID)
		res.Skipped = true
		return res, nil

	case errors.Is(err, httpx.ErrIdempotencyKeyStale),
		errors.Is(err, httpx.ErrIdempotencyMismatch):
		// Stale：幂等键过期未清理（alive-gc 里的 CleanupExpiredIdempotencyKeys 没跑）。
		// Mismatch：键就是载荷，撞了说明有人手工往 idempotency_keys 写了行。
		// 两者都当成「已处理」丢弃：本任务的业务体是幂等的对账，少跑一次没有损失，
		// 而返回 5xx 会让 Cloud Tasks 拿同一个坏键无限重投。
		log.ErrorContext(ctx, "流量入账任务的幂等键状态异常，已丢弃", "batch_id", batchID, "err", err)
		res.Skipped = true
		return res, nil

	default:
		return res, fmt.Errorf("抢占流量入账任务失败: %w", err)
	}

	row, err := q.BumpUserRevForExhaustedUsers(ctx)
	if err != nil {
		return res, fmt.Errorf("配额耗尽对账失败: %w", err)
	}
	res.ExhaustedUsers = row.ExhaustedUsers
	res.BumpedServers = row.BumpedServers

	if row.BumpedServers > 0 {
		// 🔴 这条日志不是噪声，是**告警线索**：正常路径下 AddNodeTrafficBatch
		// 已经在跨阈值那一刻 bump 过了，这里应当恒为 0。
		// 持续非 0 说明有一条入账路径漏了 bump —— 去查那条路径，不要调大这里的阈值。
		log.WarnContext(ctx, "配额耗尽对账补 bump 了 user_rev（正常路径应为 0，请排查漏 bump 的入账路径）",
			"batch_id", batchID,
			"exhausted_users", row.ExhaustedUsers,
			"bumped_servers", row.BumpedServers)
	}

	// 落盘结果供后续同键重投回放。失败只影响「同批重投会被再执行一次」的窗口，
	// 而业务体是幂等的对账，再跑一次没有副作用 —— 所以不失败请求。
	if err := httpx.CompleteIdempotent(ctx, q, att.Key, 200, trafficBatchAckBody); err != nil {
		log.WarnContext(ctx, "流量入账任务的幂等键落盘失败", "batch_id", batchID, "err", err)
	}
	return res, nil
}

// trafficBatchAckBody 是回放时写回的响应体，与 taskAck() 序列化后逐字节一致。
var trafficBatchAckBody = []byte(`{"ok":true}`)

// RunTrafficBatchTask 实现 POST /internal/tasks/traffic-batch。
func (s *Server) RunTrafficBatchTask(ctx context.Context, req gen.RunTrafficBatchTaskRequestObject) (gen.RunTrafficBatchTaskResponseObject, error) {
	log := s.taskLogger(ctx, "traffic-batch")

	// 🔴 载荷坏掉时**丢弃并回 200**，不回 5xx。
	//
	// 这是「毒消息」处理：Cloud Tasks 对 5xx 会一直重投，而一个 batch_id 缺失或
	// 形态非法的任务重投一万次仍然非法 —— 结果是这个队列被一条永远处理不了的消息
	// 堵死，后面**合法**的任务全部被拖慢。
	// 载荷是我们自己（`/push`）生成的，所以非法载荷是我们自己的 bug：
	// 它该以 ERROR 日志的形式被看见，不该以队列积压的形式被感受到。
	if req.Body == nil {
		log.ErrorContext(ctx, "流量入账任务请求体缺失，已丢弃")
		return gen.RunTrafficBatchTask200JSONResponse{InternalTaskAckJSONResponse: taskAckSkipped()}, nil
	}
	if err := httpx.ValidateIdempotencyKey(req.Body.BatchId); err != nil {
		log.ErrorContext(ctx, "流量入账任务的 batch_id 形态非法，已丢弃",
			"batch_id_len", len(req.Body.BatchId), "err", err)
		return gen.RunTrafficBatchTask200JSONResponse{InternalTaskAckJSONResponse: taskAckSkipped()}, nil
	}

	res, err := runTrafficBatch(ctx, s.db, log, req.Body.BatchId)
	if err != nil {
		return gen.RunTrafficBatchTask500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "流量入账任务失败", err),
		}, nil
	}
	if res.Skipped {
		return gen.RunTrafficBatchTask200JSONResponse{InternalTaskAckJSONResponse: taskAckSkipped()}, nil
	}
	return gen.RunTrafficBatchTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}, nil
}

// ============================================================
// 6 · stat-rollup（Scheduler，每小时 + 每日）
// ============================================================

type statRollupQuerier interface {
	ReconcileWalletBalances(ctx context.Context) ([]dbgen.ReconcileWalletBalancesRow, error)
	FindUnbalancedLedgerEntries(ctx context.Context) ([]dbgen.FindUnbalancedLedgerEntriesRow, error)
	CountActiveServerKeysPerServer(ctx context.Context) ([]dbgen.CountActiveServerKeysPerServerRow, error)
	CleanupOldStats(ctx context.Context, statDate pgtype.Date) (int64, error)
	CleanupExpiredSessions(ctx context.Context) (int64, error)
	CleanupOldEmailVerifications(ctx context.Context) (int64, error)
	CleanupOldWebhookEvents(ctx context.Context, dollar1 pgtype.Interval) (int64, error)
	CleanupOldSubscriptionFetchLog(ctx context.Context, dollar1 pgtype.Interval) (int64, error)
}

type statRollupResult struct {
	WalletMismatches  int
	UnbalancedEntries int
	KeyOverLimit      int
	PurgedRows        int64
}

// runStatRollup 是每日维护窗口：三条巡检 + 五条保留期清理。
//
// 🔴 **契约说的「(scope, record_at) upsert 聚合」在本项目里没有对应的表，而且是刻意的。**
//
//	data-model §9 的裁决：只落一张日聚合实表 stat_user_server，
//	stat_user 与 stat_server 是它的**视图**（「Xboard 写三处，三份数字可能对不上
//	且没有任何机制发现；我们写两处，视图恒等于实表，对不上在结构上不可能」）。
//	也就是说聚合在 `/push` 落库那一刻就完成了，这里没有二次聚合可做。
//	往这里加一张 rollup 表会**重新引入**那条被明确推翻的设计。
//
// 那么这个每日窗口该做什么？做那些「schema 注释里写着必须定时跑、但没有任何代码调」的事。
// 逐条都有出处，不是凑数：
//
//   - ReconcileWalletBalances —— 0007 的表注释：「⚠️ 这是缓存不是真相。
//     每日 Cloud Scheduler 必须跑一次与 user_wallet_balance 的比对，
//     返回非空行 = 立即告警，且以视图为准」。
//   - FindUnbalancedLedgerEntries —— 复式记账的核心不变量 `∀ entry: SUM(lines.amount) = 0`。
//     返回非空行意味着账本本身坏了。
//   - CountActiveServerKeysPerServer —— data-model §8.3 的「同时有效 ≤ 2」是应用层规则，
//     这条巡检是它唯一的兜底（servers.sql 的注释逐字写着「用这条兜底告警」）。
//   - 五条 Cleanup —— data-model §13 的保留期。它们是**合规承诺**，不是清理洁癖。
//
// ⚠️ 巡检失败与清理失败都不中断其余步骤：它们互不依赖，
//
//	而「因为账本巡检查不动所以过期会话也不删」是把一个问题放大成两个。
func runStatRollup(ctx context.Context, q statRollupQuerier, log *slog.Logger, now time.Time) (statRollupResult, error) {
	var res statRollupResult
	var errs []error

	// ---- 巡检：返回非空行 = 有人要在今天之内看一眼 ----

	if rows, err := q.ReconcileWalletBalances(ctx); err != nil {
		errs = append(errs, fmt.Errorf("钱包余额对账失败: %w", err))
	} else if len(rows) > 0 {
		res.WalletMismatches = len(rows)
		// 🔴 ERROR 而不是 WARN：缓存与分录不一致意味着用户看到的余额是错的，
		// 而余额可以用来抵扣订单 —— 这是钱。以视图为准（data-model §7.1），
		// 但**修正不在这里做**：自动改钱是比对不上更危险的事。
		for _, r := range rows {
			log.ErrorContext(ctx, "钱包余额缓存与分录不一致（以分录为准，需人工冲正）",
				"user_id", r.UserID, "currency", r.Currency, "cached", r.Cached, "ledger", r.Ledger)
		}
	}

	if rows, err := q.FindUnbalancedLedgerEntries(ctx); err != nil {
		errs = append(errs, fmt.Errorf("账本平衡巡检失败: %w", err))
	} else if len(rows) > 0 {
		res.UnbalancedEntries = len(rows)
		for _, r := range rows {
			log.ErrorContext(ctx, "账本分录借贷不平（复式记账的核心不变量被破坏）",
				"entry_id", r.EntryID, "imbalance", r.Imbalance)
		}
	}

	if rows, err := q.CountActiveServerKeysPerServer(ctx); err != nil {
		errs = append(errs, fmt.Errorf("节点密钥巡检失败: %w", err))
	} else if len(rows) > 0 {
		res.KeyOverLimit = len(rows)
		for _, r := range rows {
			// WARN 而不是 ERROR：多一把有效密钥不会立刻出事，但它说明某次轮换
			// 只做了第一步（签发新的）就没有再管 —— 而旧密钥仍然能拉全部用户列表。
			log.WarnContext(ctx, "节点同时有效的密钥超过 2 把（某次轮换没做完第二步）",
				"server_id", r.ServerID, "active_keys", r.ActiveKeys)
		}
	}

	// ---- 保留期清理（data-model §13）----

	statCutoff := now.AddDate(0, 0, -retentionStatsDays)
	if n, err := q.CleanupOldStats(ctx, pgtype.Date{Time: statCutoff, Valid: true}); err != nil {
		errs = append(errs, fmt.Errorf("清理过期统计失败: %w", err))
	} else {
		res.PurgedRows += n
	}

	if n, err := q.CleanupExpiredSessions(ctx); err != nil {
		errs = append(errs, fmt.Errorf("清理过期会话失败: %w", err))
	} else {
		res.PurgedRows += n
	}

	if n, err := q.CleanupOldEmailVerifications(ctx); err != nil {
		errs = append(errs, fmt.Errorf("清理过期邮箱验证码失败: %w", err))
	} else {
		res.PurgedRows += n
	}

	if n, err := q.CleanupOldWebhookEvents(ctx, days(retentionWebhookEventsDays)); err != nil {
		errs = append(errs, fmt.Errorf("清理过期回调事件失败: %w", err))
	} else {
		res.PurgedRows += n
	}

	if n, err := q.CleanupOldSubscriptionFetchLog(ctx, days(retentionSubscriptionFetchDays)); err != nil {
		errs = append(errs, fmt.Errorf("清理过期订阅拉取日志失败: %w", err))
	} else {
		res.PurgedRows += n
	}

	log.InfoContext(ctx, "每日巡检与保留期清理完成",
		"wallet_mismatches", res.WalletMismatches,
		"unbalanced_entries", res.UnbalancedEntries,
		"key_over_limit", res.KeyOverLimit,
		"purged_rows", res.PurgedRows)
	return res, errors.Join(errs...)
}

// days 把天数转成 pgtype.Interval。
//
// 用 Days 而不是换算成 Microseconds：Postgres 的 interval 里 day 是**不定长**的
// （夏令时切换那天是 23 或 25 小时）。保留期是「多少天之前」，按天算才是对的语义，
// 换成微秒会在时区切换时差出一小时 —— 一个永远查不出来、也不值得查的偏差。
func days(n int32) pgtype.Interval {
	return pgtype.Interval{Days: n, Valid: true}
}

// RunStatRollupTask 实现 POST /internal/tasks/stat-rollup。
func (s *Server) RunStatRollupTask(ctx context.Context, _ gen.RunStatRollupTaskRequestObject) (gen.RunStatRollupTaskResponseObject, error) {
	log := s.taskLogger(ctx, "stat-rollup")
	if _, err := runStatRollup(ctx, s.db, log, time.Now()); err != nil {
		return gen.RunStatRollupTask500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "每日巡检与清理失败", err),
		}, nil
	}
	return gen.RunStatRollupTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}, nil
}

// ============================================================
// 7 · remind-sweep（Scheduler，每日）
// ============================================================

type remindSweepQuerier interface {
	ListRemindableExpiringUsers(ctx context.Context, arg dbgen.ListRemindableExpiringUsersParams) ([]dbgen.ListRemindableExpiringUsersRow, error)
	ListRemindableTrafficUsers(ctx context.Context, arg dbgen.ListRemindableTrafficUsersParams) ([]dbgen.ListRemindableTrafficUsersRow, error)
	EnqueueReminderMails(ctx context.Context, arg dbgen.EnqueueReminderMailsParams) ([]dbgen.EnqueueReminderMailsRow, error)
}

type remindSweepResult struct {
	ExpireCandidates  int
	TrafficCandidates int
	ExpireQueued      int
	TrafficQueued     int
}

// runRemindSweep 每日扫一遍「该提醒的人」并入队。
//
// 幂等：两条查询都带 `NOT EXISTS (… email_log … 当天 …)`，
// 也就是契约给的 `(user_id, remind_kind, day)`。
// ⚠️ 它是**建议性**的去重，不是数据库强制（email_log 上没有对应的唯一索引）——
//
//	并发投递时会重复发一封。理由与代价见 tasks.sql 里那两条查询的注释。
//
// 🔴 只处理「到期」与「流量」两类，因为 users 上只有这两个开关。
//
//	`service_broadcast`（失联广播）**不受用户开关控制**，也不在这里 ——
//	0003_accounts.up.sql 表达这条裁决的方式就是根本不给 users 加那一列
//	（「用户把它关掉的那天，就是我们再也够不到他的那天」）。
//	要加广播时先读 account.sql 那段注释，再决定它该不该走这个任务。
//
// 本任务只入队（写 email_log，status='queued'），不发信 ——
// 发信是 mail-send 的事。分开的理由是重试粒度：一封信发失败不该让整批扫描重来。
func runRemindSweep(
	ctx context.Context,
	q remindSweepQuerier,
	sender MailSender,
	log *slog.Logger,
) (remindSweepResult, error) {
	var res remindSweepResult

	expiring, err := q.ListRemindableExpiringUsers(ctx, dbgen.ListRemindableExpiringUsersParams{
		WithinDays: remindExpireWithinDays,
		Template:   templateExpireRemind,
		Batch:      remindSweepBatchSize,
	})
	if err != nil {
		return res, fmt.Errorf("查询待提醒的到期用户失败: %w", err)
	}
	res.ExpireCandidates = len(expiring)

	if len(expiring) > 0 {
		ids := make([]int64, 0, len(expiring))
		emails := make([]string, 0, len(expiring))
		for _, u := range expiring {
			ids = append(ids, u.ID)
			emails = append(emails, u.Email)
		}
		queued, qErr := q.EnqueueReminderMails(ctx, dbgen.EnqueueReminderMailsParams{
			Esp:      sender.Name(),
			Template: templateExpireRemind,
			Subject:  subjectExpireRemind,
			UserIds:  ids,
			Emails:   emails,
		})
		if qErr != nil {
			return res, fmt.Errorf("到期提醒入队失败: %w", qErr)
		}
		res.ExpireQueued = len(queued)
	}

	traffic, err := q.ListRemindableTrafficUsers(ctx, dbgen.ListRemindableTrafficUsersParams{
		ThresholdPct: remindTrafficThresholdPct,
		Template:     templateTrafficRemind,
		Batch:        remindSweepBatchSize,
	})
	if err != nil {
		return res, fmt.Errorf("查询待提醒的流量用户失败: %w", err)
	}
	res.TrafficCandidates = len(traffic)

	if len(traffic) > 0 {
		ids := make([]int64, 0, len(traffic))
		emails := make([]string, 0, len(traffic))
		for _, u := range traffic {
			ids = append(ids, u.ID)
			emails = append(emails, u.Email)
		}
		queued, qErr := q.EnqueueReminderMails(ctx, dbgen.EnqueueReminderMailsParams{
			Esp:      sender.Name(),
			Template: templateTrafficRemind,
			Subject:  subjectTrafficRemind,
			UserIds:  ids,
			Emails:   emails,
		})
		if qErr != nil {
			return res, fmt.Errorf("流量提醒入队失败: %w", qErr)
		}
		res.TrafficQueued = len(queued)
	}

	if res.ExpireQueued > 0 || res.TrafficQueued > 0 {
		log.InfoContext(ctx, "提醒扫描完成",
			"expire_queued", res.ExpireQueued,
			"traffic_queued", res.TrafficQueued,
			"esp", sender.Name())
	}
	if !sender.Configured() && (res.ExpireQueued > 0 || res.TrafficQueued > 0) {
		// 入队本身是对的（队列是持久的，ESP 接上之后 mail-send 会把它们发出去），
		// 但必须让「信在排队却没人发」可见 —— 否则这个数字会一直涨到有人投诉才被发现。
		log.WarnContext(ctx, "提醒已入队但 ESP 未配置，这些信要等接线之后才会发出",
			"queued", res.ExpireQueued+res.TrafficQueued)
	}
	return res, nil
}

// RunRemindSweepTask 实现 POST /internal/tasks/remind-sweep。
//
// 整体一个事务：`NOT EXISTS` 的检查与 INSERT 之间的窗口，在同一个事务里至少不会被
// **本进程自己**的两次扫描撞上（两类提醒共用同一张 email_log）。
// 跨进程的并发仍然挡不住 —— 那要靠一条部分唯一索引，见 tasks.sql 的 TODO(P2)。
func (s *Server) RunRemindSweepTask(ctx context.Context, _ gen.RunRemindSweepTaskRequestObject) (gen.RunRemindSweepTaskResponseObject, error) {
	log := s.taskLogger(ctx, "remind-sweep")
	sender := s.mailSender()
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		_, txErr := runRemindSweep(ctx, q, sender, log)
		return txErr
	})
	if err != nil {
		return gen.RunRemindSweepTask500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "提醒扫描失败", err),
		}, nil
	}
	return gen.RunRemindSweepTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}, nil
}

// ============================================================
// 8 · mail-send（Cloud Tasks，按需）
// ============================================================

type mailSendQuerier interface {
	ClaimQueuedMail(ctx context.Context, arg dbgen.ClaimQueuedMailParams) (dbgen.ClaimQueuedMailRow, error)
	MarkMailSent(ctx context.Context, arg dbgen.MarkMailSentParams) error
	MarkMailSendFailed(ctx context.Context, arg dbgen.MarkMailSendFailedParams) error
}

type mailSendResult struct {
	// Skipped：抢占没抢到（重复投递或 id 不存在）。
	Skipped bool
	// NotConfigured：ESP 还没接线，信留在队列里。
	NotConfigured bool
	Sent          bool
	// DependencyDown：ESP 已配置但打不通 —— 这一项为 true 时调用方回 503 而不是 500。
	DependencyDown bool
}

// runMailSend 发一封信。
//
// 🔴 **ESP 未配置时优雅退出，返回 (skipped, nil)，不报错。**
//
//	ESP 未选型是一个已知的、计划内的状态（auth.go 的 TODO(P1) 是同一个缺口，
//	ADR 0002 §7 要求先拿真实送达率数据再选型）。让它每次投递都刷一条 ERROR，
//	结果是所有人学会忽略这个告警 —— 然后真的故障发生时也被一起忽略。
//	信留在队列里（status 仍是 'queued'），接线之后重投就会发出去。
//
// 🔴 **抢占在发信之前**（ClaimQueuedMail 把 queued → sent）。
//
//	这是 at-most-once，一个刻意的取舍：反过来（先发信、成功后改状态）在
//	「发信成功但回写失败」时会重发，而重发验证码意味着两个都有效的 code 同时在飞。
//	本方向的代价是「发信失败但状态已是 sent」，靠 MarkMailSendFailed 改成 failed，
//	于是失败在 email_log 里**可见** —— 那张表本来就是 ADR 0002 §7 的送达率数据源。
//
// 幂等：抢占影响 0 行（pgx.ErrNoRows）= 这封信已被上一次投递领走。
// 这正是契约给的幂等键 `mail_queue.id`，只不过表叫 email_log（见 tasks.sql 的说明）。
func runMailSend(
	ctx context.Context,
	q mailSendQuerier,
	sender MailSender,
	log *slog.Logger,
	mailID int64,
) (mailSendResult, error) {
	var res mailSendResult

	if !sender.Configured() {
		res.NotConfigured = true
		log.WarnContext(ctx, "ESP 未配置，本次发信跳过（信留在队列里，接线后重投即可发出）",
			"mail_id", mailID, "esp", sender.Name())
		return res, nil
	}

	row, err := q.ClaimQueuedMail(ctx, dbgen.ClaimQueuedMailParams{ID: mailID, Esp: sender.Name()})
	if errors.Is(err, pgx.ErrNoRows) {
		// 两种情况撞在同一个返回上：这封信已被领走（Cloud Tasks 重投，常态），
		// 或者这个 id 根本不存在（我们自己入队时写错了，异常）。
		// 分开需要多一次 SELECT，而两者的处理方式相同（丢弃）——
		// 所以只在日志里留下 id，让「不存在」那一类能被事后查出来。
		log.InfoContext(ctx, "发信任务未抢到队列行（重复投递或 id 不存在），已幂等丢弃", "mail_id", mailID)
		res.Skipped = true
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("抢占待发邮件失败: %w", err)
	}

	body, renderable := bodyForQueuedTemplate(row.Template)
	if !renderable {
		// 队列里出现渲染不了的模板键，两种来路：verify_code / password_reset 的记账行
		// （ESP 未配置时期只记账不发；正文需要明文码，而码只存哈希且早已过期），
		// 或者新模板忘了加渲染分支。两种都不该重试 —— 重试也渲染不出来。
		// 标 failed 让它在送达率统计里可见，ERROR 让第二种来路有人看。
		if markErr := q.MarkMailSendFailed(ctx, dbgen.MarkMailSendFailedParams{
			ID:         row.ID,
			BounceCode: truncateBounceCode("unrenderable_template:" + row.Template),
		}); markErr != nil {
			log.ErrorContext(ctx, "标记不可渲染邮件失败（该行会停留在 sent）", "mail_id", row.ID, "err", markErr)
		}
		log.ErrorContext(ctx, "队列信的模板无法渲染正文，已标 failed（不重试）",
			"metric", "bp_mail_unrenderable", "mail_id", row.ID, "template", row.Template)
		res.Skipped = true
		return res, nil
	}

	msgID, sendErr := sender.Send(ctx, MailMessage{
		To:       row.ToEmail,
		Subject:  row.Subject,
		Template: row.Template,
		UserID:   row.UserID,
		Body:     body,
	})
	if sendErr != nil {
		// 把状态改回 failed，让这封信在送达率统计里算作失败而不是成功。
		// 这条回写失败了也不改变结论（那封信确实没发出去），所以只记日志。
		if markErr := q.MarkMailSendFailed(ctx, dbgen.MarkMailSendFailedParams{
			ID:         row.ID,
			BounceCode: truncateBounceCode(sendErr.Error()),
		}); markErr != nil {
			log.ErrorContext(ctx, "发信失败后回写 failed 状态也失败（该行会停留在 sent，送达率统计会偏高）",
				"mail_id", row.ID, "err", markErr)
		}
		// 🔴 返回错误 → 503 + Retry-After。ESP 已配置却打不通才是真正的
		//    ErrDependencyDown，它值得告警、也值得 Cloud Tasks 重投。
		res.DependencyDown = true
		return res, fmt.Errorf("ESP 发信失败: %w", sendErr)
	}

	if msgID != "" {
		if err := q.MarkMailSent(ctx, dbgen.MarkMailSentParams{ID: row.ID, ProviderMsgID: msgID}); err != nil {
			// 信已经发出去了，只是消息 ID 没落库。绝不能因此返回错误 ——
			// 重投会让用户收到第二封。丢的只是「后续投递回调对不回本行」的能力。
			log.WarnContext(ctx, "发信成功但回写 provider_msg_id 失败（投递回调将无法归位）",
				"mail_id", row.ID, "err", err)
		}
	}

	res.Sent = true
	log.InfoContext(ctx, "发信完成",
		"mail_id", row.ID, "template", row.Template, "to_domain", row.ToDomain, "esp", sender.Name())
	return res, nil
}

// truncateBounceCode 把 ESP 的错误文本截到 bounce_code 该有的长度。
//
// bounce_code 是 text（无上限），而错误文本可能带完整的 HTTP 响应体。
// 128 足够容纳真实的 SMTP 退信码（0011 的列注释举的例子是网易的 '554 HL:IPB'）。
//
// 🔴 **必须退到 rune 边界**，不能裸切字节。
//
//	PostgreSQL 的 text 列拒收非法 UTF-8（22021 invalid byte sequence，不是静默截断），
//	而 ESP 的错误消息完全可能带中文或表情。切在多字节字符中间的后果是
//	MarkMailSendFailed 整条语句失败 → 这封信停留在 'sent' → 送达率统计把
//	一封没发出去的信算成成功。而那份统计正是「选哪家 ESP」的唯一依据。
//	一个只在特定错误文本下才出现的 bug，正是最难被发现的那一类。
func truncateBounceCode(s string) string {
	const maxBounceCodeLen = 128
	if len(s) <= maxBounceCodeLen {
		return s
	}
	cut := maxBounceCodeLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// RunMailSendTask 实现 POST /internal/tasks/mail-send。
func (s *Server) RunMailSendTask(ctx context.Context, req gen.RunMailSendTaskRequestObject) (gen.RunMailSendTaskResponseObject, error) {
	log := s.taskLogger(ctx, "mail-send")

	// 毒消息处理，理由同 RunTrafficBatchTask：载荷是我们自己生成的，
	// 非法载荷是我们的 bug，该以 ERROR 日志出现，不该以队列积压出现。
	if req.Body == nil || req.Body.MailQueueId <= 0 {
		log.ErrorContext(ctx, "发信任务载荷非法，已丢弃")
		return gen.RunMailSendTask200JSONResponse{InternalTaskAckJSONResponse: taskAckSkipped()}, nil
	}

	res, err := runMailSend(ctx, s.db, s.mailSender(), log, req.Body.MailQueueId)
	if err != nil {
		if res.DependencyDown {
			return gen.RunMailSendTask503JSONResponse{
				ErrDependencyDownJSONResponse: s.dependencyDown(ctx, "ESP 暂时不可达", err),
			}, nil
		}
		return gen.RunMailSendTask500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "发信任务失败", err),
		}, nil
	}
	if res.Skipped {
		return gen.RunMailSendTask200JSONResponse{InternalTaskAckJSONResponse: taskAckSkipped()}, nil
	}
	return gen.RunMailSendTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}, nil
}

// ============================================================
// 9 · chain-scan（Scheduler，1 分钟）
// ============================================================

type chainScanQuerier interface {
	// 支付配置（写销 / 人工复核阈值与折算汇率）由本任务读一次，再随每笔到账传给入账路径。
	//
	// 🔴 不读的后果不是报错而是**零值**：WriteoffUsdt6 为 0 会让所有取整量级的少付
	//    都被推进人工复核队列；CnyPerUsdtE4 为 0 会让 usdt6ToCents 恒得 0，
	//    于是「订单已过期 → 款项入余额」那条分支记了流水却入了 0 元余额。
	//    两种都不抛错，只是把结论静默算歪 —— 正是本文件反复防的那一类失败。
	settingsReader

	ListScannableChainAddresses(ctx context.Context, batch int32) ([]dbgen.ListScannableChainAddressesRow, error)
	TouchPayAddressScan(ctx context.Context, arg dbgen.TouchPayAddressScanParams) error
}

// depositFunc 是「把一笔链上到账入账」的注入点。生产实现是 order.go 的
// `(*Server).processDepositTx`：一笔一个事务，流水 / 分录 / 归属 / 状态迁移 / 审计
// 要么全成要么全无（ADR 0012 §8.4 硬约束 3）。
//
// 🔴 **入账只有 processDeposit 一条路径**（§8.4 硬约束 1）。
//
//	本任务曾经自己写过一条（tasks.sql 的 RecordChainPayment 那组），两条并不等价：
//	那条按 order_id 累计、且 order_id/user_id 是非空参数，于是**结构上记不下**
//	§8.4 分支 ②「钱打到了我们的地址、但归属不到任何订单」——
//	而那正是 §1 点名要消灭的「钱进黑洞」，是所有失败模式里最不可挽回的一种。
//	所以合并的方向是单向的：扫链改调 processDeposit，那组查询删除。
//
// ⚠️ 做成参数而不是让 runChainScan 自己开事务：事务边界属于 Server
//
//	（只有 *store.Store 有 InTx），而这个自由函数存在的全部意义是能被单测直接调
//	（本文件纪律第 1 条）。
type depositFunc func(ctx context.Context, in depositInput) error

type chainScanResult struct {
	NotConfigured bool
	// Addresses 是本轮真正尝试扫描的地址数（不含被拉黑而跳过的）。
	Addresses int
	Transfers int
	// Deposited 是成功交给入账路径的转账笔数。
	//
	// ⚠️ 这里**不区分「新落库」与「幂等丢弃」**（合并前是 Recorded / Duplicates 两个计数）。
	//    幂等命中在 processDeposit 里是设计内的常态，被它自己吞掉并返回 nil；
	//    扫描侧要报出这个区分只能自己再猜一遍，而「同一件事两处各算一遍」
	//    正是这次合并要消灭的东西。真要看新增了多少条流水，数据在 payments 里。
	Deposited int
	// Failures 是**出过问题的地址数**，不是失败次数。
	// 按地址计而不是按事件计，是因为它唯一的用途是判断
	// 「上游整体不可达（→ 503）」还是「个别抖动（→ 记日志继续）」——
	// 按次数计的话，一个地址上 3 笔转账入账失败会让计数超过地址总数，
	// 那个判据就废了。
	Failures int
	// AllFailed 让调用方不必重算判据。两处各算一遍必然有一天算歪。
	AllFailed bool
}

// runChainScan 自扫链，把链上到账交给入账路径。
//
// ⚠️ ADR 0012 的状态是「提案，未批准」。本轮**不连任何第三方 RPC**：
//
//	scanner 未配置时直接优雅退出（200 + WARN），不调 Scan、不返回 503。
//	503 留给「已配置但打不通」—— 那才是 ErrDependencyDown 的语义，
//	也才值得每分钟一次的告警。
//
// 三条 ADR 0012 的规则在这里落地：
//
//  1. **归属只看地址不看金额**（§5.4）：ListScannableChainAddresses 直接
//     pay_addresses ⋈ orders，一次确定的查表。代码里没有任何金额匹配。
//  2. **一单一址、永不复用**（§5.2）：由 schema 强制（pay_addresses.assigned_order_id
//     UNIQUE + 0015 的 orders_pay_addr_uk），这里不需要也不应该再判一次。
//  3. **过期订单的地址继续监听 ≥ 24h**（§11.1 / user-journey §7）：
//     扫描范围里的 `address_watch_until > now()` 就是它，order-timeout 负责把窗口顶上去。
//
// 🔴 **本任务不自己入账。** 每笔转账原样递给 deposit（= processDeposit），
//
//	由它做幂等锁、归属、复式账、状态迁移与审计。剩给本函数的只有三件事：
//	决定扫谁、把链上那一笔递过去、以及**决定游标推不推进**。
//	第三件是本函数独有的纪律，也是它唯一能独自搞砸一笔真钱的地方 ——
//	见下面 addrFailed 的注释。
//	（「收到钱之后开通权利」那一步仍未实现，processDeposit 会响亮地停在 paid 并打 ERROR。）
//
// ⚠️ 单个地址失败不中断整轮：一个地址拉取超时不该让另外 99 个地址这一轮都不扫。
//
//	全部失败时才返回错误（→ 503），因为那说明是上游整体不可达而不是个别抖动。
func runChainScan(
	ctx context.Context,
	q chainScanQuerier,
	scanner ChainScanner,
	deposit depositFunc,
	log *slog.Logger,
) (chainScanResult, error) {
	var res chainScanResult

	if !scanner.Configured() {
		res.NotConfigured = true
		log.WarnContext(ctx, "链上扫描器未配置，本轮跳过（ADR 0012 仍是提案，第一阶段不连 RPC）",
			"scanner", scanner.Name())
		return res, nil
	}

	addrs, err := q.ListScannableChainAddresses(ctx, chainScanBatchSize)
	if err != nil {
		return res, fmt.Errorf("查询待扫描收款地址失败: %w", err)
	}
	if len(addrs) == 0 {
		return res, nil
	}

	// 配置在确认「这一轮有地址要扫」之后才读：没有地址时不必每分钟碰一次 settings。
	// 读失败不致命 —— loadPaymentSettings 回落默认值并记 Warn，
	// 用默认阈值收一笔钱好过因为配置表抖动而整轮不扫（order.go 的同一条裁决）。
	set := loadPaymentSettings(ctx, q, log)

	var lastErr error
	for _, a := range addrs {
		if a.IsBlacklisted {
			// Tether 把我方地址拉黑了（AML Layer 0，ADR 0012 §12.2）。
			// 继续扫没有意义：这个地址上的 USDT 已经动不了。
			// 不 return —— 别的地址还好着，而这一条是运营要处理的事不是任务要处理的事。
			// 也不计入 res.Addresses：它没有被「尝试扫描」，
			// 混进分母会让「全部失败」的判据被一个运营问题稀释掉。
			log.ErrorContext(ctx, "收款地址已被 Tether 拉黑，跳过扫描（资金已冻结，需人工处置）",
				"pay_address_id", a.PayAddressID, "order_id", a.OrderID, "trade_no", a.TradeNo)
			continue
		}
		res.Addresses++

		transfers, scanErr := scanner.Scan(ctx, a.Chain, a.Address, lookbackCursor(a.CursorTs))
		if scanErr != nil {
			res.Failures++
			lastErr = scanErr
			log.WarnContext(ctx, "扫描收款地址失败，跳过（下一轮会重扫，游标未推进）",
				"pay_address_id", a.PayAddressID, "chain", a.Chain, "err", scanErr)
			continue
		}
		res.Transfers += len(transfers)

		// 🔴 addrFailed 一旦为 true 就**永不复位**。
		//    写成「失败时把 newestMS 归零」是不够的：后面一笔成功的转账会把它重新抬上去，
		//    于是游标越过了那笔没入账成功的钱 —— 而越过去的表现是
		//    「用户付了钱、链上有记录、我们的库里没有」，只有投诉时才会被发现。
		var addrFailed bool
		var newestMS int64
		for _, t := range transfers {
			// 🔴 只负责把链上那一笔**原样**递过去：不做任何金额比较、状态判断或分录。
			//    ToAddress 取我们库里的 a.Address 而不是 t.ToAddress —— 归属的依据
			//    必须来自我们自己的表，而 t.ToAddress 是扫描器从外部数据解析出来的。
			//    ActorOverride 用链上 txid：审计里的 actor 必须能指回那笔真实交易，
			//    "system" 之类的占位在事后追溯里等于没有。
			if depErr := deposit(ctx, depositInput{
				Provider:      "chain_" + a.Chain,
				EnteredBy:     "scanner",
				Chain:         a.Chain,
				ToAddress:     a.Address,
				Transfer:      t,
				Settings:      set,
				ActorOverride: "chain:" + t.TxID,
			}); depErr != nil {
				// 入账失败比拉取失败严重得多：钱到了但我们没记下来。
				// 仍然不中断（这个地址上别的转账还能记），但用 ERROR，且不推进游标 ——
				// 下一轮会把同一段重扫，靠 UNIQUE (provider, external_id) 去重。
				//
				// errDepositForeignAddress 也走这里：地址是从我们自己表里查出来的，
				// 它出现只可能是地址刚被删改或串了链，属于必须有人看的状态 ——
				// 同样不推进游标（卡在原地会一直报警，而跳过去只会安静地丢钱）。
				addrFailed = true
				lastErr = depErr
				log.ErrorContext(ctx, "链上到账入账失败（下一轮重扫，游标不推进）",
					"pay_address_id", a.PayAddressID, "order_id", a.OrderID,
					"trade_no", a.TradeNo, "txid", t.TxID, "err", depErr)
				continue
			}
			if t.BlockTimeMS > newestMS {
				newestMS = t.BlockTimeMS
			}
			res.Deposited++
		}
		if addrFailed {
			res.Failures++
		}

		// 只有这个地址这一轮**全部**入账成功才推进游标；否则传 nil 保持原值。
		var cursor *int64
		if !addrFailed && newestMS > 0 {
			c := newestMS
			cursor = &c
		}
		if err := q.TouchPayAddressScan(ctx, dbgen.TouchPayAddressScanParams{
			ID: a.PayAddressID, CursorTs: cursor,
		}); err != nil {
			log.WarnContext(ctx, "推进扫描游标失败（下一轮会重扫同一段，由幂等索引兜底）",
				"pay_address_id", a.PayAddressID, "err", err)
		}
	}

	log.InfoContext(ctx, "链上扫描完成",
		"scanner", scanner.Name(),
		"addresses", res.Addresses, "transfers", res.Transfers,
		"deposited", res.Deposited, "failures", res.Failures)

	// 全部地址都出了问题 = 上游整体不可达，值得 503 + Retry-After 让 Scheduler 退避。
	// 部分失败只记日志：退避会让**好的**那部分也停下来。
	res.AllFailed = res.Addresses > 0 && res.Failures == res.Addresses
	if res.AllFailed {
		return res, fmt.Errorf("全部 %d 个收款地址扫描失败: %w", res.Addresses, lastErr)
	}
	return res, nil
}

// lookbackCursor 把存的游标往回退 10 分钟（ADR 0012 §10.5）。
//
// 回看防的是「事件在游标那一毫秒的两侧同时落地」导致的漏读，
// 而重复由 payments 的 UNIQUE (provider, external_id) 兜底 —— 所以回看是免费的。
// 游标为 nil（从没扫过）时保持 nil：那意味着「从头扫」，不是「从 -10 分钟扫」。
func lookbackCursor(cursorMS *int64) *int64 {
	if cursorMS == nil {
		return nil
	}
	back := *cursorMS - chainScanLookbackMS
	if back < 0 {
		back = 0
	}
	return &back
}

// mustMarshalFallbackRaw 在扫描器没给原文时兜一份最小的取证材料。
// json.Marshal 对这个纯值结构不可能失败；真失败了也只能落一个空对象，
// 因为 payments.raw 是 NOT NULL，返回错误会让这笔钱记不进去。
//
// ⚠️ 入账合并之后它**唯一的调用方是 order.go 的 processDeposit**（本文件已不再自己落库）。
//
//	留在这里是因为它和 ChainTransfer 同源 —— 兜底对象的字段就是这个结构的字段，
//	搬走会让「加了字段忘了加进兜底」这件事跨文件发生。
func mustMarshalFallbackRaw(t ChainTransfer) []byte {
	b, err := json.Marshal(struct {
		TxID          string `json:"txid"`
		LogIndex      int32  `json:"log_index"`
		FromAddress   string `json:"from_address"`
		ToAddress     string `json:"to_address"`
		AmountUSDT6   int64  `json:"amount_usdt6"`
		Confirmations int32  `json:"confirmations"`
		Solidified    bool   `json:"solidified"`
		BlockTimeMS   int64  `json:"block_time_ms"`
		Note          string `json:"note"`
	}{
		TxID: t.TxID, LogIndex: t.LogIndex,
		FromAddress: t.FromAddress, ToAddress: t.ToAddress,
		AmountUSDT6: t.AmountUSDT6, Confirmations: t.Confirmations,
		Solidified: t.Solidified, BlockTimeMS: t.BlockTimeMS,
		Note: "扫描器未提供链上原始事件，本对象由服务端按已知字段重建",
	})
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

// RunChainScanTask 实现 POST /internal/tasks/chain-scan。
func (s *Server) RunChainScanTask(ctx context.Context, _ gen.RunChainScanTaskRequestObject) (gen.RunChainScanTaskResponseObject, error) {
	log := s.taskLogger(ctx, "chain-scan")
	// 🔴 入账口子只有 processDeposit 一个（ADR 0012 §8.4 硬约束 1）：
	//    定时扫链、recheck、支付回调反查、D6 手工录入四条触发路径共用它。
	res, err := runChainScan(ctx, s.db, s.chainScanner(), s.processDepositTx, log)
	if err != nil {
		if res.AllFailed {
			// 上游整体不可达 → 503 + Retry-After（openapi 对本端点定义了这个响应）。
			return gen.RunChainScanTask503JSONResponse{
				ErrDependencyDownJSONResponse: s.dependencyDown(ctx, "链上数据源暂时不可达", err),
			}, nil
		}
		return gen.RunChainScanTask500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "链上扫描失败", err),
		}, nil
	}
	return gen.RunChainScanTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}, nil
}

// ============================================================
// 小工具
// ============================================================

// timestamptzString 把可空时间戳变成日志友好的字符串。
// 直接把 pgtype.Timestamptz 丢给 slog 会打出结构体字面量（含 InfinityModifier），
// 而日志里真正要看的只有那个时刻。
func timestamptzString(t pgtype.Timestamptz) string {
	if !t.Valid {
		return "<null>"
	}
	return t.Time.UTC().Format(time.RFC3339)
}
