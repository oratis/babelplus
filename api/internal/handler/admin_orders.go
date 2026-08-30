package handler

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/audit"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 管理面「订单与支付」（listAdminOrders · getAdminOrder · markAdminOrderPaid(D6) ·
// refundAdminOrder(D7) · listAdminPayments · listAdminUnderpaidPayments ·
// updateAdminPayment(D13)）。
//
// 这个文件里有**两个直接动钱、且钱的去向由操作者一句话决定**的端点，所以它的纪律
// 与 order.go 不同：order.go 防的是「算错」，这里防的是「有人故意」。
// 下面七条贯穿全文，每条都配了「不这么做会发生什么」。
//
//  1. 🔴 **四层强制（api-contract §6.2）全部在服务端做，而且是 deny-by-default。**
//     L1 确认串由服务端自己查出期望值再常数时间比对 —— 前端的确认弹窗对一个直接
//     `curl` 的人是零；L2 原因 ≥ 8 字符；L3 当次 TOTP（含 used_totp 防重放）；
//     L4 独立权限位。缺任何一层的现象必须是**这个操作做不了**，不能是「少一道校验」。
//
//  2. 🔴 **审计写入与业务写入同一个事务**（§6.3 第 1 条）。三个写端点一律走
//     `audit.InTx`：它的签名不给「不写审计」留出口，忘了写的现象是**业务操作失败**。
//     本文件**不出现** `s.db.InTx` —— 那条路径能写业务而不写审计，正是 §6.3 禁止的形状。
//
//  3. 🔴 **D6 的幂等键只能来自链上**（ADR 0012 §16.1）：`external_id = txid:log_index`。
//     冻结契约的 `MarkPaidRequest` 没有 txid 字段，所以只能从 `evidence_url` 解；
//     **解不出就 422**。退化成 `'D6:'||audit_id` 之类的伪键在 0014 的表注释里被逐字推翻过：
//     它根本不幂等，点两次 = 两次入账、两次开通，而且与扫链跨 provider 不去重。
//
//  4. 🔴 **D6 的记账科目是 `asset:manual_reconcile`，不是 `asset:crypto:tron:pool`**（§16.2）。
//     因为手工标记的那一刻钱可能根本没到。这个科目的余额长期非零 = 有人标了「已支付」
//     但钱没进来 —— 这是把「全系统最大的内部欺诈面」变成一个可以每天看一眼的数字的
//     **唯一**手段。混进真实到账的科目里，这盏灯就永远亮着，等于把它关掉。
//
//  5. 🔴 **退款一律进不可提现余额**（ADR 0013 §3.1/§3.5），退款额**复用**
//     `GetRefundBasis`（WITH RECURSIVE 窗口链）。本文件一行都不重算：那条查询里有三处
//     非显然的正确性（V_window 不含 surplus/discount、分段按各自的月付标价快照折算、
//     两步 floor 不能省），在 Go 里抄一份就是给这三处各留一次抄错的机会。
//
//  6. 🔴 **「冷静期退款一生一次」的闸门是数据库，不是应用代码的自觉**
//     （`refunds_cooling_off_once` 部分唯一索引）。`cooling_off_used` 那个数只用来
//     提前说人话；读与插之间有窗口，而「用户连点两次申请退款」是真实场景。
//     所以 23505 必须映射成 409 **并说清楚原因**，不能变成 500。
//
//  7. **管理面的 `OrderStatus` 直出库里的原值**，不压扁成契约那 6 个值。
//     这是一处**刻意的契约偏离**，登记在 adminOrderStatusView 上 —— 后台是全系统唯一
//     能看见 `refunding` / `chargeback_*` 的地方，压扁会让后台看不见拒付。
//
// # 本文件里已登记的五处「装配/契约缺口」（notes 里同样有）
//
//	① Server 上没有 mw.AdminAuthConfig 字段（server.go 不在本轮可写范围），
//	   所以 step-up 用 s.cfg + s.db.Pool 就地组装 —— 见 admin_common.go 的 adminAuthConfig。
//	② D6 的带外 sink（§16.3）默认未配置，于是 D6 整体不可用。这是裁决要的形状。
//	③ §16.2 的**冲正分录**仍未实现（order.go 的 handleAlreadyProcessed 明写它缺对手方，
//	   而 order.go 不在本轮可写范围）。后果是 asset:manual_reconcile 会挂账。
//	④ `perm_refund` 在 openapi 的 AdminPermission 枚举里没有对应值 —— 权限位在库里有列，
//	   但**通过 API 授不了**，只能由迁移/SQL 直接置位。
//	⑤ 退款扣减明细在契约上无处可放（响应是 AdminOrder）。明细进审计与 422 details。

// ============================================================
// 可注入的外部依赖：D6 的带外留痕 sink（ADR 0012 §16.3）
// ============================================================

// AdminOpRecord 是一条要送进带外 sink 的管理操作记录。
//
// 字段刻意只有「事后要凭它复盘」的那几项，不放整行订单快照：
// sink 的对面是我们**控制不到**的存储，往那里送的东西越少越好判断它是否泄漏了什么。
type AdminOpRecord struct {
	Action     string
	AdminID    int64
	AdminEmail string
	TargetType string
	TargetID   string
	Reason     string
	Evidence   string
	AmountCNY  int64
	RequestID  string
	At         time.Time
}

// AdminOpSink 是 ADR 0012 §16.3 要求的**带外**留痕通道。
//
// 🔴 「带外」的定义在裁决里写得很死：**不能是我们自己的 Postgres**。
// 草稿的写法是「同事务 enqueue、事务外发送」，而 enqueue 落在同一个库里 ——
// 一个能改 Postgres 的攻击者在邮件发出去之前把队列行删掉即可，
// 于是「带外」的其实只有收件箱，不是发信路径。
//
// 抽成接口的理由与 order.go 的 ChainScanner / PaymentNotifyVerifier 完全一样：
// 本轮纪律是代码里不出现任何第三方 endpoint，「打到哪」是装配层的问题。
type AdminOpSink interface {
	// Name 写进日志，形如 "resend" / "gcs-append"。
	Name() string
	// Configured 为 false 时 D6 **整体不可用**（不是「跳过 sink」）。
	Configured() bool
	// Record 同步送出一条记录。返回 error 即 D6 失败。
	Record(ctx context.Context, rec AdminOpRecord) error
}

type unconfiguredAdminOpSink struct{}

func (unconfiguredAdminOpSink) Name() string     { return "unconfigured" }
func (unconfiguredAdminOpSink) Configured() bool { return false }
func (unconfiguredAdminOpSink) Record(context.Context, AdminOpRecord) error {
	return errors.New("带外留痕 sink 未配置")
}

// defaultAdminOpSink 是进程内的默认实现：**未配置**。
//
// 🔴 这个默认值就是 §16.3 裁决第 2 条的落地：「在这个 sink 被端到端验证通过之前，
// `perm_mark_order_paid` 对所有管理员保持 false，即 D6 不可用」。
// 权限位默认关是 0002:62 的既有事实，本行是它的第二道锁 ——
// 万一有人把权限位打开了（比如为了测试），D6 仍然做不成。
// 两道锁而不是一道，是因为这两件事会被不同的人在不同的时间打开。
var defaultAdminOpSink AdminOpSink = unconfiguredAdminOpSink{}

func (s *Server) adminOpSink() AdminOpSink { return defaultAdminOpSink }

// ============================================================
// L3 step-up 的装配（见文件头缺口 ①）
// ============================================================

// adminStepUpVerifier 与 step-up 配置的组装（adminAuthConfig）都在 admin_common.go：
// 管理面四个文件曾各自组装一份 mw.AdminAuthConfig，现在合并成一处。
// 那里也记着残余的漂移风险（main.go 仍有独立的一份）与 TODO(P1)。
var adminStepUpFor = func(s *Server) adminStepUpVerifier { return s.adminAuthConfig() }

// ============================================================
// 四层强制的守卫（api-contract §6.2）
// ============================================================

// L2 的下限 adminReasonMinRunes 在 admin_common.go（管理面四个文件共用一份）。
//
// 按 **rune** 数而不是字节数：中文一个字三个字节，按字节算的话「补单」两个字
// 就已经 6 字节、「链上已确认」五个字 15 字节直接过关 —— 而 L2 要的是
// 「写清楚为什么」，不是「凑够字节」。按 rune 算，中文八个字才过，英文八个字母才过。

// adminGuardFailure 是四层强制里某一层的一次拒绝。
//
// 做成一个带 Layer 的值而不是直接返回 HTTP 响应，有两个理由：
//  1. 四个守卫都是**纯函数**，可以脱离数据库单测 —— 而「参数没收齐时不许提交」
//     恰恰是本组最需要被测到的分支；
//  2. Layer 进服务端日志。一次被拒的危险操作，我们要知道它卡在第几层：
//     卡在 L4 是「这个人本来就不该有这个按钮」，卡在 L1 是「他填错了确认串」，
//     两者在事后复盘时的含义完全不同。
type adminGuardFailure struct {
	Layer   string
	Status  int
	Code    gen.ErrorCode
	Message string
	Details []gen.ErrorDetail
}

func (g *adminGuardFailure) opError() *adminOpError {
	return &adminOpError{
		Status:  g.Status,
		Code:    g.Code,
		Message: g.Message,
		Details: g.Details,
		Layer:   g.Layer,
	}
}

// guardAdminPermission 是 L4。
//
// 🔴 判据是 `auth.Can(perm)`，而 mw.AdminAuth.Can 对**未知权限位一律返回 false** ——
// 也就是说「加了一个权限位但忘了在 Can 里加分支」的现象是「这个操作谁都做不了」，
// 不是「谁都能做」。这里必须写成 `if !Can { deny }`，写反就是静默放行。
//
// contractGap 非空时，说明这个权限位在冻结契约的 AdminPermission 枚举里**没有对应值**
// （perm_refund / perm_adjust_balance 两个直接动钱的位都是这种情况）——
// 库里有列、能判，但**通过 API 授不了**。拒绝文案必须把这句话说出来，
// 否则运维会在「管理员管理」页面上反复找一个根本不存在的开关。
func guardAdminPermission(auth *middleware.AdminAuth, perm middleware.AdminPermission, what, contractGap string) *adminGuardFailure {
	if auth.Can(perm) {
		return nil
	}
	msg := "你没有「" + what + "」的权限位"
	if contractGap != "" {
		msg += "；该权限位在当前 API 契约里没有对应的枚举值，只能由数据库直接置位（" + contractGap + "）"
	}
	return &adminGuardFailure{
		Layer: "L4", Status: http.StatusForbidden,
		Code: gen.AUTHPERMISSIONDENIED, Message: msg,
	}
}

// guardAdminReason 是 L2。
//
// 空白字符不算数（`strings.TrimSpace` 之后再数 rune）：八个空格是一个合法的
// 「≥ 8 字符」，但它在审计日志里与没填完全等价 —— 而 reason 是审计里
// 唯一一列由人写的、能回答「当时为什么这么做」的东西。
func guardAdminReason(reason string) *adminGuardFailure {
	trimmed := strings.TrimSpace(reason)
	if len([]rune(trimmed)) >= adminReasonMinRunes {
		return nil
	}
	return &adminGuardFailure{
		Layer: "L2", Status: http.StatusUnprocessableEntity,
		Code:    gen.VALIDATIONFAILED,
		Message: fmt.Sprintf("必须填写操作原因，且不少于 %d 个字符（它会原样进审计日志）", adminReasonMinRunes),
		Details: []gen.ErrorDetail{detail("reason", fmt.Sprintf("当前 %d 个字符", len([]rune(trimmed))))},
	}
}

// guardAdminConfirmation 是 L1。
//
// 🔴 期望值 `expected` **必须由服务端自己查出来**（订单所属用户的邮箱），
// 不能由请求带进来 —— 那样比对的是「用户填的」和「用户填的」。
//
// 常数时间比较：这条比对的另一端由调用方完全控制，而期望值是一个我们不想让人
// 逐字符试出来的串（邮箱本身不是秘密，但「这张单属于谁」在管理面之外是）。
// 与 middleware/admin.go 的 aud 比对同一条纪律：这条路径上的比较一律常数时间，
// 保持一致比逐个论证便宜。
//
// 大小写归一是刻意的：邮箱在 users_email_uk 上就是按 lower(email) 唯一的，
// 而管理员是从别处**拷**一个邮箱过来填的。因为大小写不同而拒绝一次 D6，
// 换来的不是安全，是运维绕过流程（比如直接改库）。
func guardAdminConfirmation(expected, got string) *adminGuardFailure {
	e := []byte(strings.ToLower(strings.TrimSpace(expected)))
	g := []byte(strings.ToLower(strings.TrimSpace(got)))
	if len(e) > 0 && subtle.ConstantTimeCompare(e, g) == 1 {
		return nil
	}
	return &adminGuardFailure{
		Layer: "L1", Status: http.StatusUnprocessableEntity,
		Code:    gen.VALIDATIONFAILED,
		Message: "确认串与订单所属用户的邮箱不一致",
		// 🔴 **不回显期望值**。回显等于把 L1 从「你必须先知道这张单属于谁」
		//    降级成「你先发一次错的，服务端会告诉你正确答案」。
		Details: []gen.ErrorDetail{detail("confirmation", "必须逐字等于该订单所属用户的邮箱")},
	}
}

// guardAdminTotpPresent 是 L3 的**快速路径**，不是判定本身。
//
// 判定由 mw.RequireStepUp 做（解密 secret、校验码、占用 used_totp 防重放）。
// 这里只挡「头都没带」这一种，目的是让这一种情况**不必开一次数据库往返**，
// 同时让它可以脱库单测 —— 「缺 TOTP 时不许提交」是本组必须有的用例之一。
// 返回的 code / message 与 RequireStepUp 的同一分支逐字相同，两者不得分叉。
func guardAdminTotpPresent(code string) *adminGuardFailure {
	if strings.TrimSpace(code) != "" {
		return nil
	}
	return &adminGuardFailure{
		Layer: "L3", Status: http.StatusForbidden,
		Code: gen.AUTHTOTPREQUIRED, Message: "该操作需要二次验证",
	}
}

// ============================================================
// 端点内部的错误载体
// ============================================================

// adminOpError 把「这次操作该回几、说什么」从事务体里带出来。
//
// 事务体是自由函数（吃窄接口，可单测），它不该知道 gen.XxxJSONResponse 这些
// 每个 operation 各不相同的类型；handler 方法拿到它之后按 Status 分派。
//
// 🔴 它实现 error，所以从 `audit.InTx` 的 fn 里 return 出来会**回滚整个事务**。
// 这正是想要的：一次 409（比如订单状态在我们读完之后被改了）必须让已经写下去的
// 流水与分录一起消失，而不是留下半条记录。
type adminOpError struct {
	Status  int
	Code    gen.ErrorCode
	Message string
	Details []gen.ErrorDetail
	Layer   string
	// Err 是底层错误，只进服务端日志，不回给调用方。
	Err error
}

func (e *adminOpError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *adminOpError) Unwrap() error { return e.Err }

func adminNotFound(msg string) *adminOpError {
	return &adminOpError{Status: http.StatusNotFound, Code: gen.RESOURCENOTFOUND, Message: msg}
}

func adminConflict(msg string) *adminOpError {
	return &adminOpError{Status: http.StatusConflict, Code: gen.STATECONFLICT, Message: msg}
}

func adminUnprocessable(msg string, d ...gen.ErrorDetail) *adminOpError {
	return &adminOpError{Status: http.StatusUnprocessableEntity, Code: gen.VALIDATIONFAILED, Message: msg, Details: d}
}

func adminInternal(msg string, err error) *adminOpError {
	return &adminOpError{Status: http.StatusInternalServerError, Code: gen.INTERNALERROR, Message: msg, Err: err}
}

// asAdminOpError 把事务体返回的 error 还原成 adminOpError。
//
// audit.InTx 会原样把 fn 的 error 传出来，但**审计写入自己失败时**返回的是
// 一个普通 error（「写审计日志失败: …」）—— 那必须落到 500，而且必须被记下来：
// 它是 §6.3 第 1 条真的生效了的证据，不是一次普通故障。
func asAdminOpError(err error) *adminOpError {
	var e *adminOpError
	if errors.As(err, &e) {
		return e
	}
	return adminInternal("管理操作失败", err)
}

// isCheckViolation（23514）在 auth.go，与 isUniqueViolation 并排。
//
// 本组用得着它的地方只有一处：佣金追回时扣 `wallet_balances`。那张表有
// `CHECK (balance >= 0)`，而我们读余额与扣余额之间有窗口（读的时候锁的是
// commissions 不是 wallet_balances）。撞上它必须变成一个「请重试」的 409 ——
// 让它冒成 500 的话，现象是「退款偶尔报服务器错误」，没有人会想到去看余额。

// ============================================================
// 身份与审计 actor
// ============================================================

// errNoAdminAuth（中间件没把管理员身份注入上下文 = 装配错误 → 500，不是 403）
// 在 admin_common.go。

// adminAuditActor 组装审计的「谁做的」。
//
// 🔴 IP 取不到时**返回错误而不是回退到 0.0.0.0**。audit 包的 validateActor 也会拒，
// 这里提前拦一次只为让日志说清楚原因：audit_logs 是证据，一条写着 0.0.0.0 的记录
// 会在事后被当成真实来源读，而它其实什么都没说。
// 缺 IP 的唯一成因是 handler.RequestBinding 没挂上 —— 那是装配错误，应当响亮。
func (s *Server) adminAuditActor(ctx context.Context) (audit.Actor, *middleware.AdminAuth, error) {
	a, ok := middleware.AdminFrom(ctx)
	if !ok || a == nil {
		return audit.Actor{}, nil, errNoAdminAuth
	}
	meta := s.requestMetadata(ctx)
	act := audit.Actor{AdminID: a.AdminID, Email: a.Email}
	if meta.IP == nil {
		return audit.Actor{}, nil, errors.New("取不到来源 IP：未挂载 handler.RequestBinding()，管理操作的审计记录会缺来源")
	}
	act.IP = *meta.IP
	if meta.UserAgent != nil {
		act.UserAgent = *meta.UserAgent
	}
	return act, a, nil
}

// adminReadAuth 是只读端点的身份检查。读端点不写审计（§6.3 只要求写操作留痕），
// 但仍然要确认中间件挂上了 —— 否则「管理面鉴权漏挂」在读端点上完全没有症状。
func (s *Server) adminReadAuth(ctx context.Context) (*middleware.AdminAuth, error) {
	a, ok := middleware.AdminFrom(ctx)
	if !ok || a == nil {
		return nil, errNoAdminAuth
	}
	return a, nil
}

// ============================================================
// 视图：DB 行 → 契约类型
// ============================================================

// adminOrderStatusView 把库里的 order_status 交给管理面。
//
// 🔴 **这是一处刻意的契约偏离，必须留在这里。**
// 契约的 `OrderStatus` 只有 6 个值（且含库里根本不存在的 `processing`），
// 库里有 14 个。用户面的 orderStatusView 把 14 压成 6 是对的 —— 用户不需要知道
// `underpaid` 与 `paying` 的区别。
//
// 管理面**不能**这么做：后台是全系统唯一能看见 `refunding` / `chargeback` /
// `chargeback_won` / `chargeback_lost` 的地方，把它们压成 `refunded`
// 会让后台**看不见拒付**。而拒付是要在 120 天窗口内申辩的东西 ——
// 一个看不见拒付的后台，与没有拒付处理流程是一回事。
//
// 代价（登记）：管理面响应里会出现 openapi 枚举之外的值，前端生成的联合类型
// 对不上。这是已知的，且修法是改契约（加 8 个值），不是在这里压扁。
func adminOrderStatusView(st dbgen.OrderStatus) gen.OrderStatus {
	return gen.OrderStatus(st)
}

// adminOrderDetailView 把详情行拼成契约的 AdminOrder。
func adminOrderDetailView(r dbgen.AdminGetOrderRow) gen.AdminOrder {
	o := gen.Order{
		TradeNo:        r.TradeNo,
		Type:           orderTypeView(r.Type),
		Status:         adminOrderStatusView(r.Status),
		Currency:       gen.OrderCurrencyCNY,
		TotalAmount:    r.AmountGross,
		PayableAmount:  r.AmountDue,
		DiscountAmount: &r.AmountDiscount,
		SurplusAmount:  &r.SurplusAmount,
		BalanceAmount:  &r.AmountBalance,
		PlanId:         r.PlanID,
		PlanName:       r.PlanName,
	}
	if r.Period != nil {
		p := string(*r.Period)
		o.Period = &p
	}
	if r.CreatedAt.Valid {
		o.CreatedAt = r.CreatedAt.Time.UTC()
	}
	o.ExpiresAt = tsPtr(r.ExpiresAt)
	o.PaidAt = tsPtr(r.PaidAt)
	o.RateLockedAt = tsPtr(r.FxLockedAt)
	return gen.AdminOrder{Order: o, UserId: r.UserID, UserEmail: openapi_types.Email(r.UserEmail)}
}

// adminOrderListView 是列表行的映射。
//
// 与 adminOrderDetailView **不合并**，理由同 order.go 的 orderView / orderListView：
// 两条查询的投影不同，硬合并只能靠把列表行填进详情结构体，
// 而那会让「列表少了一个字段」变成「详情里那个字段恒为零值」。
func adminOrderListView(r dbgen.AdminListOrdersPageRow) gen.AdminOrder {
	o := gen.Order{
		TradeNo:        r.TradeNo,
		Type:           orderTypeView(r.Type),
		Status:         adminOrderStatusView(r.Status),
		Currency:       gen.OrderCurrencyCNY,
		TotalAmount:    r.AmountGross,
		PayableAmount:  r.AmountDue,
		DiscountAmount: &r.AmountDiscount,
		SurplusAmount:  &r.SurplusAmount,
		BalanceAmount:  &r.AmountBalance,
		PlanId:         r.PlanID,
		PlanName:       r.PlanName,
	}
	if r.Period != nil {
		p := string(*r.Period)
		o.Period = &p
	}
	if r.CreatedAt.Valid {
		o.CreatedAt = r.CreatedAt.Time.UTC()
	}
	o.ExpiresAt = tsPtr(r.ExpiresAt)
	o.PaidAt = tsPtr(r.PaidAt)
	o.RateLockedAt = tsPtr(r.FxLockedAt)
	return gen.AdminOrder{Order: o, UserId: r.UserID, UserEmail: openapi_types.Email(r.UserEmail)}
}

// adminPaymentListView 映射支付流水行。
//
// ⚠️ `created_at ← received_at`：0014 的 `payments` **没有 created_at 这一列**。
// ⚠️ `received_usdt6` 是**这张订单按地址累计的实收**，不是这一行的金额 ——
// 补足场景下一张订单对应两笔链上转账，取本行金额会让第一行永远显示「少付」。
func adminPaymentListView(r dbgen.AdminListPaymentsPageRow) gen.AdminPayment {
	p := gen.AdminPayment{
		Id:         r.ID,
		Provider:   r.Provider,
		ExternalId: r.ExternalID,
		State:      gen.PaymentState(r.State),
		Txid:       r.Txid,
		TradeNo:    r.TradeNo,
	}
	if r.ReceivedAt.Valid {
		p.CreatedAt = r.ReceivedAt.Time.UTC()
	}
	p.ExpectedUsdt6 = r.ExpectedUsdt6
	received := r.ReceivedUsdt6
	shortfall := r.ShortfallUsdt6
	p.ReceivedUsdt6 = &received
	p.ShortfallUsdt6 = &shortfall
	return p
}

// adminPaymentUnderpaidView 映射少付队列行。
//
// 与 adminPaymentListView 分开：少付清单的 trade_no 是 NOT NULL（它 INNER JOIN orders），
// 而流水列表的是可空（打到我们地址但归属不到订单的钱，那是**另一个**人工队列）。
// 两者用同一个函数就必须在其中一边做一次「把 string 变成 *string」的转换，
// 而那正是「列表里那批最需要人看的行」消失的起点。
func adminPaymentUnderpaidView(r dbgen.AdminListUnderpaidPaymentsPageRow) gen.AdminPayment {
	tradeNo := r.TradeNo
	received := r.ReceivedUsdt6
	shortfall := r.ShortfallUsdt6
	p := gen.AdminPayment{
		Id:             r.ID,
		Provider:       r.Provider,
		ExternalId:     r.ExternalID,
		State:          gen.PaymentState(r.State),
		Txid:           r.Txid,
		TradeNo:        &tradeNo,
		ExpectedUsdt6:  r.ExpectedUsdt6,
		ReceivedUsdt6:  &received,
		ShortfallUsdt6: &shortfall,
	}
	if r.ReceivedAt.Valid {
		p.CreatedAt = r.ReceivedAt.Time.UTC()
	}
	return p
}

// adminPaymentPatchedView 拼 D13 的 200 响应体。
//
// 两个来源：可变字段取**改后**那一行，`trade_no` / `expected_usdt6` 取改前的读
// （AdminUpdatePaymentState 的 RETURNING 里没有它们，它只 touch payments 一张表）。
// `received_usdt6` / `shortfall_usdt6` 在这条路径上**取不到**（需要按地址的 LATERAL 聚合），
// 留 nil —— 它们是 optional 字段，留空好过填一个只对这一行成立的数。
func adminPaymentPatchedView(before dbgen.AdminGetPaymentForUpdateRow, after dbgen.AdminUpdatePaymentStateRow) gen.AdminPayment {
	p := gen.AdminPayment{
		Id:            after.ID,
		Provider:      after.Provider,
		ExternalId:    after.ExternalID,
		State:         gen.PaymentState(after.AfterState),
		Txid:          after.Txid,
		TradeNo:       before.TradeNo,
		ExpectedUsdt6: before.ExpectedUsdt6,
	}
	if after.ReceivedAt.Valid {
		p.CreatedAt = after.ReceivedAt.Time.UTC()
	}
	return p
}

// adminSearchPattern 把 `?q=` 变成一个 ILIKE 模式串。
//
// 🔴 **必须先转义 `\` `%` `_`。** 不转义的话，一个 `%` 就是「返回全部订单」，
// 一个 `_` 就是「任意一个字符」—— 而搜索框里输入 `%` 的人只是想搜一个百分号。
// 转义顺序也是硬的：`\` 必须**先**换，否则后面加进去的反斜杠会被再转义一次。
// SQL 侧没有 ESCAPE 子句，用的是 PostgreSQL 的默认转义符 `\`。
func adminSearchPattern(q *string) *string {
	if q == nil {
		return nil
	}
	raw := strings.TrimSpace(*q)
	if raw == "" {
		return nil
	}
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	pattern := "%" + r.Replace(raw) + "%"
	return &pattern
}

// ============================================================
// listAdminOrders
// ============================================================

// ListAdminOrders 实现 GET /api/v1/admin/orders。
func (s *Server) ListAdminOrders(ctx context.Context, req gen.ListAdminOrdersRequestObject) (gen.ListAdminOrdersResponseObject, error) {
	if _, err := s.adminReadAuth(ctx); err != nil {
		return nil, err
	}
	want, limitPlusOne := pageLimit(req.Params.Limit)
	params := dbgen.AdminListOrdersPageParams{
		PageLimit: limitPlusOne,
		QLike:     adminSearchPattern(req.Params.Q),
	}
	if req.Params.Cursor != nil && *req.Params.Cursor != "" {
		// 坏游标按第一页处理并记 WARN —— 本端点在契约上**一个 4xx 都没声明**
		// （只有 403 / 500），所以「游标坏了」没有出口。三个候选里这是最不坏的：
		// 用户看到的是他刚才那一页，下次点「下一页」自愈。代价是静默，
		// 所以这条 WARN 是它唯一的痕迹。
		if cur, ok := decodePageCursor(*req.Params.Cursor); ok {
			params.CursorAt = tstz(cur.At)
			params.CursorID = &cur.ID
		} else {
			s.logger.WarnContext(ctx, "管理面订单游标非法，按第一页处理",
				"request_id", middleware.RequestIDFrom(ctx), "cursor_len", len(*req.Params.Cursor))
		}
	}

	rows, err := s.db.AdminListOrdersPage(ctx, params)
	if err != nil {
		return gen.ListAdminOrders500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取管理面订单列表失败", err)}, nil
	}

	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	data := make([]gen.AdminOrder, 0, len(rows))
	for i := range rows {
		data = append(data, adminOrderListView(rows[i]))
	}

	meta := s.meta(ctx)
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		if last.CreatedAt.Valid {
			// 游标编的是 (created_at, id)，与 ORDER BY 逐字同源。
			// 两个分量必须同时给：只给一个时 SQL 的行比较求值为 NULL，
			// 返回 0 行而**不报错** —— 现象是「后面没有了」。
			enc := encodePageCursor(last.ID, last.CreatedAt.Time.UTC())
			if enc != "" {
				meta.NextCursor = &enc
			}
		}
	}
	if req.Params.Count != nil && *req.Params.Count {
		// COUNT(*) 的 WHERE 与列表逐字一致（两条查询写在一起，改一处必须改两处）。
		// 不一致的现象是「分页器说共 87 条，翻到底只有 71 条」，没有任何报错。
		total, err := s.db.AdminCountOrdersFiltered(ctx, params.QLike)
		if err != nil {
			// 🔴 计数失败**不让列表失败**：`?count=true` 是锦上添花，
			//    而 COUNT(*) 在 db-f1-micro 上是实打实的开销、也是最先超时的那一个。
			s.logger.WarnContext(ctx, "管理面订单计数失败，本次不返回 total", "err", err)
		} else {
			meta.Total = &total
		}
	}
	return gen.ListAdminOrders200JSONResponse{Data: data, Meta: meta}, nil
}

// ============================================================
// getAdminOrder
// ============================================================

// GetAdminOrder 实现 GET /api/v1/admin/orders/{trade_no}。
//
// 与用户面 GetOrder 的关键区别：**不按 user_id 过滤**。用户面按单号查是越权读单，
// 管理面按单号查就是它的职责 —— 两条查询长得像但语义相反，所以 SQL 层也是分开写的。
func (s *Server) GetAdminOrder(ctx context.Context, req gen.GetAdminOrderRequestObject) (gen.GetAdminOrderResponseObject, error) {
	if _, err := s.adminReadAuth(ctx); err != nil {
		return nil, err
	}
	row, err := s.db.AdminGetOrder(ctx, req.TradeNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.GetAdminOrder404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "订单不存在")}, nil
	}
	if err != nil {
		return gen.GetAdminOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取订单详情失败", err)}, nil
	}
	return gen.GetAdminOrder200JSONResponse{Data: adminOrderDetailView(row), Meta: s.meta(ctx)}, nil
}

// ============================================================
// 🔴 D6 · markAdminOrderPaid —— 全系统最大的内部欺诈面
// ============================================================

// tronEvidence 是从 `evidence_url` 里解出来的链上定位。
type tronEvidence struct {
	TxID     string
	LogIndex int32
	// LogIndexExplicit 记录 log_index 是**解出来的**还是**兜底的 0**。
	// 见 parseTronEvidence 的注释：兜底的 0 是一个必须被记下来的风险。
	LogIndexExplicit bool
}

// tronTxIDLen 是 TRON 交易哈希的十六进制长度（32 字节）。
const tronTxIDLen = 64

// parseTronEvidence 从证据 URL 里解出 txid（以及可选的 log_index）。
//
// 🔴 **它存在的理由是一处冻结契约与 ADR 0012 §16.1 的硬冲突。**
// §16.1 要求 D6 必须携带真实 txid（「没有 txid 的手工入账走 D10 调整余额，不走 D6」），
// 而冻结的 `MarkPaidRequest` 只有 confirmation / reason / evidence_url —— 没有 txid 字段。
// 契约不能改，所以只能从 evidence_url 解。
//
// 认得三种形态（tronscan 是 hash 路由，txid 在 fragment 里而不是 path 里）：
//
//	https://tronscan.org/#/transaction/<64 位十六进制>
//	https://tronscan.org/#/transaction/<txid>:<log_index>
//	https://tronscan.org/#/transaction/<txid>?log_index=<n>
//	<txid>                                        （裸哈希，运维直接粘的形态）
//
// 🔴 **解不出 txid 一律返回 false，调用方必须 422。**
// 0014 的表注释逐字推翻过 `'D6:' || audit_logs.id` 这种伪 external_id：
// 它根本不幂等（点两次 = 两个键 = 两次入账两次开通），而且与扫链跨 provider 不去重。
//
// ⚠️ **log_index 缺省为 0 是一个已登记的残余风险。** external_id = txid:log_index，
// 而「手工与自动天然互斥」这条性质要求 D6 编出来的键与扫链编出来的**逐字节相同**。
// 一次 TRC20 提币通常只有一个 Transfer 事件（log_index = 0），所以缺省值在绝大多数
// 情况下是对的；但如果那笔交易里我们的转账不是第一个事件，扫链会用另一个键插入，
// 于是 deferred_revenue 被贷记两次。调用方在这种情况下必须打 ERROR 并给出可观测指标 ——
// 真正的修法是在 URL 里带上 `:<log_index>`，本函数已经支持。
func parseTronEvidence(raw string) (tronEvidence, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return tronEvidence{}, false
	}

	seg, query := s, ""
	if u, err := url.Parse(s); err == nil {
		// tronscan 用 `#/transaction/<txid>`：txid 落在 Fragment 上，Path 是空的。
		// 两处都要看，否则换一种前端路由（或直接给 REST 形态的 URL）就解不出来。
		if u.Fragment != "" {
			seg = u.Fragment
			if i := strings.IndexByte(seg, '?'); i >= 0 {
				query, seg = seg[i+1:], seg[:i]
			}
		} else if u.Path != "" {
			seg, query = u.Path, u.RawQuery
		}
	}
	seg = strings.Trim(seg, "/")
	if i := strings.LastIndexByte(seg, '/'); i >= 0 {
		seg = seg[i+1:]
	}

	ev := tronEvidence{}
	if i := strings.IndexByte(seg, ':'); i >= 0 {
		n, err := strconv.ParseInt(seg[i+1:], 10, 32)
		if err != nil || n < 0 {
			return tronEvidence{}, false
		}
		ev.LogIndex, ev.LogIndexExplicit = int32(n), true
		seg = seg[:i]
	} else if query != "" {
		if v, err := url.ParseQuery(query); err == nil {
			for _, key := range []string{"log_index", "log", "index"} {
				if raw := v.Get(key); raw != "" {
					n, err := strconv.ParseInt(raw, 10, 32)
					if err != nil || n < 0 {
						return tronEvidence{}, false
					}
					ev.LogIndex, ev.LogIndexExplicit = int32(n), true
					break
				}
			}
		}
	}

	// 形态校验必须严：一个「看起来像 txid」的串会变成一个永久有效的、
	// 与链上任何东西都对不上的幂等键。TRON 的交易哈希恒为 64 位十六进制。
	if len(seg) != tronTxIDLen {
		return tronEvidence{}, false
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return tronEvidence{}, false
		}
	}
	ev.TxID = strings.ToLower(seg)
	return ev, true
}

// acctManualReconcile 是 D6 专用的借方科目（ADR 0012 §16.2，0015 的 seed 里币种是 CNY）。
//
// 🔴 **不要复用 order.go 的 acctTronPool。** 手工标记的那一刻钱可能根本没到，
// 记进真实到账的科目里，这盏「有人标了已支付但钱没进来」的指示灯就永远亮着。
const acctManualReconcile = "asset:manual_reconcile"

// adminMarkPaidQuerier 是 D6 事务体的全部数据面。
//
// 🔴 它**不复用** order.go 的 processDeposit，而是复用它那一组查询。理由有两条，
// 第二条是决定性的：
//
//  1. processDeposit 的收款凭证borrow 的是 `asset:crypto:tron:pool`，
//     而 §16.2 要求 D6 走 `asset:manual_reconcile`（见上）。
//  2. processDeposit 的入口是 `GetPayAddressByAddress` + `GetOrderByPayAddressForUpdate`
//     —— 它按**地址**找单。D6 的入口是单号，而且必须在 `AdminGetOrderForMarkPaid`
//     的 `FOR UPDATE OF o` 之下做 L1 比对与状态判定。把 D6 塞进 processDeposit
//     需要给它加一个「已经有订单了」的分支，那等于在全系统唯一的入账函数上
//     开一个只有管理员走的旁路 —— 而那个函数存在的全部意义是四条路径共用同一段代码。
//
// 共用的是**查询**（§8.4 分支 0 的幂等锁、CAS、账本三条），不是那个函数。
type adminMarkPaidQuerier interface {
	ledgerQuerier
	AdminGetOrderForMarkPaid(ctx context.Context, tradeNo string) (dbgen.AdminGetOrderForMarkPaidRow, error)
	InsertPaymentIfNew(ctx context.Context, arg dbgen.InsertPaymentIfNewParams) (dbgen.Payment, error)
	AttributePayment(ctx context.Context, arg dbgen.AttributePaymentParams) (dbgen.Payment, error)
	RecordOrderPayment(ctx context.Context, arg dbgen.RecordOrderPaymentParams) (dbgen.RecordOrderPaymentRow, error)
	TransitionOrderStatus(ctx context.Context, arg dbgen.TransitionOrderStatusParams) (dbgen.TransitionOrderStatusRow, error)
	InsertOrderTransition(ctx context.Context, arg dbgen.InsertOrderTransitionParams) (dbgen.OrderTransition, error)
	AdminGetOrder(ctx context.Context, tradeNo string) (dbgen.AdminGetOrderRow, error)
}

// adminMarkPaidInput 是 D6 事务体的入参。
type adminMarkPaidInput struct {
	TradeNo      string
	Confirmation string
	Reason       string
	EvidenceURL  string
	Evidence     tronEvidence
	AdminID      int64
	AdminEmail   string
	RequestID    string
	Settings     paymentSettings
	Sink         AdminOpSink
	Now          time.Time
}

// adminMarkPaidSnapshot 是进审计的订单快照（§6.3 第 2 条：存**完整快照**，不存 diff）。
type adminMarkPaidSnapshot struct {
	TradeNo         string `json:"trade_no"`
	UserID          int64  `json:"user_id"`
	UserEmail       string `json:"user_email"`
	Status          string `json:"status"`
	AmountDue       int64  `json:"amount_due_cents"`
	AmountPaid      int64  `json:"amount_paid_cents"`
	PayAmountUsdt6  *int64 `json:"pay_amount_usdt6"`
	PaymentRowCount int64  `json:"payment_row_count"`
	ReceivedUsdt6   int64  `json:"received_usdt6_before"`
	// 下面几项只在 after 上有意义，是「这次 D6 究竟做了什么」的全部内容。
	ExternalID  string `json:"external_id,omitempty"`
	EvidenceURL string `json:"evidence_url,omitempty"`
	// 🔴 记的是「**兜底**了没有」而不是「解出来了没有」，因为 omitempty 只序列化 true：
	//    需要在审计里一眼看见的是**有风险**的那一种（log_index 是猜的，
	//    扫链可能用另一个 external_id 再入一次账），不是安全的那一种。
	//    写反的话，风险最高的那批 D6 恰恰在审计里什么都不显示。
	LogIndexDefaulted bool  `json:"log_index_defaulted,omitempty"`
	PostedCNYCents    int64 `json:"posted_cny_cents,omitempty"`
	LedgerEntryID     int64 `json:"ledger_entry_id,omitempty"`
	PaymentID         int64 `json:"payment_id,omitempty"`
}

// adminMarkOrderPaid 是 D6 的事务体。由 audit.InTx 调用，返回的 Entry 与业务写入同事务。
//
// 步骤顺序是硬的，每一步的位置都有理由：
//
//	1 加锁读订单     → L1 的期望值与状态判定都必须来自**加锁的那一行**
//	2 L1 权威比对     → 预检已经在事务外做过一次（为了不白烧一个 TOTP code），
//	                    但那一次读的是没锁的行；判定必须以这一次为准
//	3 状态闸门       → 只有 paying / underpaid 可以被标记
//	4 抢幂等锁       → §8.4 分支 0，全系统唯一的入账锁。撞上 = 409（这笔钱已经入过账）
//	5 记账（§16.2） → Dr asset:manual_reconcile / Cr liability:deferred_revenue
//	6 归属流水       → 把分录 id 落回 payments
//	7 记订单收款     → amount_paid 必须涨，否则将来退款时佣金追回的基数是 0
//	8 CAS 推状态     → 0 行 = 有人在我们眼皮底下改了这张单，**必须失败**
//	9 带外留痕       → §16.3：同步打一次外部 sink，失败让 D6 失败
func adminMarkOrderPaid(ctx context.Context, q adminMarkPaidQuerier, log *slog.Logger, in adminMarkPaidInput) (gen.AdminOrder, audit.Entry, error) {
	var empty gen.AdminOrder

	// ---- 1 加锁读 ----
	row, err := q.AdminGetOrderForMarkPaid(ctx, in.TradeNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, audit.Entry{}, adminNotFound("订单不存在")
	}
	if err != nil {
		return empty, audit.Entry{}, adminInternal("读取订单失败", err)
	}

	// ---- 2 L1 权威比对 ----
	if g := guardAdminConfirmation(row.UserEmail, in.Confirmation); g != nil {
		log.ErrorContext(ctx, "D6 被四层强制拒绝",
			"metric", "bp_admin_d6_denied", "layer", g.Layer,
			"admin_id", in.AdminID, "trade_no", in.TradeNo)
		return empty, audit.Entry{}, g.opError()
	}

	// ---- 3 状态闸门 ----
	//
	// 只放 paying / underpaid：
	//   · pending 还没有收款地址（地址在 payOrder 才分配），也没有应收金额，
	//     「补一笔它本来就还没开始收的钱」不是 D6 而是 D10；
	//   · expired / cancelled 的到账走 ADR 0012 §8.4 分支 ④（入余额，不回改状态）——
	//     把过期单改回 paid 等于用一个已经过期的汇率开通，敞口由我们承担；
	//   · paid / completed 已经收过了，再标一次就是二次入账。
	switch row.Status {
	case dbgen.OrderStatusPaying, dbgen.OrderStatusUnderpaid:
	default:
		return empty, audit.Entry{}, adminConflict(
			"订单当前状态是 " + string(row.Status) + "，不能手工标记为已支付" +
				"（只有 paying / underpaid 可以；过期单的到账按 ADR 0012 §8.4 入余额，没有 txid 的手工入账走 D10 调整余额）")
	}

	if row.PayAddress == nil || *row.PayAddress == "" {
		return empty, audit.Entry{}, adminUnprocessable("订单没有收款地址，无法作为链上到账入账（这张单还没走到收银台）")
	}
	if row.PayAmountUsdt6 == nil || *row.PayAmountUsdt6 <= 0 {
		return empty, audit.Entry{}, adminUnprocessable("订单没有应收 USDT 金额，无法判定这笔钱是不是它的")
	}
	expected := *row.PayAmountUsdt6

	// 🔴 「标记之前就能看见这张单其实已经有链上到账了」—— 那种情况下正确的动作是
	//    等扫描，不是手工标。不做硬拦截（underpaid 补足之后手工标是合法场景），
	//    但必须留下 ERROR：它与 bp_pay_d6 一起构成 §16.4「D6 的频次是健康指标」。
	if row.PaymentRowCount > 0 {
		log.ErrorContext(ctx, "D6 作用在一张已经有链上流水的订单上，请先确认扫描是否只是延迟",
			"metric", "bp_pay_d6_on_existing_payments", "trade_no", in.TradeNo,
			"payment_row_count", row.PaymentRowCount, "received_usdt6", row.ReceivedUsdt6)
	}

	before := adminMarkPaidSnapshot{
		TradeNo: row.TradeNo, UserID: row.UserID, UserEmail: row.UserEmail,
		Status: string(row.Status), AmountDue: row.AmountDue, AmountPaid: row.AmountPaid,
		PayAmountUsdt6: row.PayAmountUsdt6, PaymentRowCount: row.PaymentRowCount,
		ReceivedUsdt6: row.ReceivedUsdt6,
	}

	// 折算成分。fx 优先取订单锁定的那一份 —— 用当前配置折算一张三十天前的单，
	// 记进账的是一个从未对这张单生效过的汇率。
	fxE4, ok := numericToE4(row.FxUsdtPerCny)
	if !ok || fxE4 <= 0 {
		fxE4 = in.Settings.CnyPerUsdtE4
		log.WarnContext(ctx, "订单没有锁定汇率，D6 按当前配置折算记账", "trade_no", in.TradeNo)
	}
	cents := usdt6ToCents(expected, fxE4)
	if cents <= 0 {
		return empty, audit.Entry{}, adminUnprocessable("按订单锁定汇率折算出的金额为 0，拒绝入账")
	}

	// ---- 4 抢幂等锁（§8.4 分支 0）----
	externalID := in.Evidence.TxID + ":" + strconv.FormatInt(int64(in.Evidence.LogIndex), 10)
	if !in.Evidence.LogIndexExplicit {
		// 见 parseTronEvidence 的 ⚠️：兜底的 0 与扫链编出来的键可能对不上，
		// 而那会让 deferred_revenue 被贷记两次。必须可观测。
		log.ErrorContext(ctx, "D6 的 evidence_url 没有给出 log_index，按 0 兜底；若该笔转账不是交易里的第一个事件，扫链会用另一个 external_id 再入一次账",
			"metric", "bp_pay_d6_log_index_defaulted", "trade_no", in.TradeNo, "txid", in.Evidence.TxID)
	}

	provider, chain := "chain_tron", "tron"
	if row.PayChain != nil && *row.PayChain != "" {
		chain = *row.PayChain
	}
	enteredBy := "admin:" + strconv.FormatInt(in.AdminID, 10)
	toAddr := *row.PayAddress
	txid := in.Evidence.TxID
	logIndex := in.Evidence.LogIndex
	amount := expected
	// payments.raw 是 NOT NULL 且刻意如此（0014：取证材料缺一条就等于这条流水不可复核）。
	// 手工录入的取证材料就是「谁、什么时候、凭什么证据、说了什么理由」——
	// 这与「不要把 AdminPaymentPatch.note 塞进 raw」不冲突：那一条禁止的是
	// **往链上原文里掺人写的字**，而这一行本来就没有链上原文。
	raw, err := json.Marshal(map[string]any{
		"source":       "admin.D6.mark_paid",
		"trade_no":     in.TradeNo,
		"admin_id":     in.AdminID,
		"admin_email":  in.AdminEmail,
		"evidence_url": in.EvidenceURL,
		"reason":       in.Reason,
		"request_id":   in.RequestID,
		"marked_at":    in.Now.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return empty, audit.Entry{}, adminInternal("构造 payments.raw 失败", err)
	}

	payment, err := q.InsertPaymentIfNew(ctx, dbgen.InsertPaymentIfNewParams{
		Provider: provider, ExternalID: externalID, EnteredBy: enteredBy,
		OrderID: &row.ID, UserID: &row.UserID,
		Chain: &chain, Txid: &txid, LogIndex: &logIndex,
		ToAddress: &toAddr, AmountUsdt6: &amount, AmountCnyCents: &cents,
		// state = paid：D6 的语义就是「我确认这笔钱到了」。confirming 会让
		// 后台的对账页把它显示成「还在等确认」，而它永远不会有第二次状态更新。
		State: dbgen.PaymentStatePaid, Confirmations: 0,
		Raw: raw,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// 撞上唯一索引 = 这笔链上转账已经入过账（可能是扫链先到，也可能是有人点了两次）。
		// 🔴 这正是 §16.1 要求「必须携带真实 txid」买到的那个性质：手工与自动天然互斥。
		return empty, audit.Entry{}, adminConflict(
			"这笔链上转账（" + externalID + "）已经入过账，不能重复标记；若订单状态没有跟上，应当排查入账流程而不是再标一次")
	}
	if err != nil {
		return empty, audit.Entry{}, adminInternal("写入支付流水失败", err)
	}

	// ---- 5 记账（ADR 0012 §16.2 逐字）----
	//
	// 🔴 借方是 asset:manual_reconcile 而**不是** asset:crypto:tron:pool ——
	//    手工标记的那一刻钱可能根本没到。两条腿都是 CNY，按 (entry_id, currency)
	//    分组各自配平（§17.6(a) 的修订不变量）。
	//
	// 🔴 **冲正尚未实现，这是一处必须知道的挂账。** §16.2 裁决「冲正由 ProcessDeposit
	//    在 §8.4 分支 ① 自动写（Dr asset:crypto:tron:pool / Cr asset:manual_reconcile）」，
	//    而 order.go 的 handleAlreadyProcessed 明写它缺对手方：pool 是 USDT、
	//    manual_reconcile 是 CNY，按字面写这两条腿当天就会被每日断言判为不平，
	//    正确形状要走 fx_clearing 桥接。order.go 不在本轮可写范围。
	//    后果：真钱到账之后 asset:manual_reconcile **不会自动归零**，
	//    它会挂着这一笔直到有人补上那条冲正。已在 notes 登记。
	entry, err := postLedgerEntry(ctx, q, ledgerEntrySpec{
		EntryNo:     ledgerEntryNo("D6", externalID),
		Description: "D6 手工标记已支付 " + in.TradeNo,
		RefType:     "order",
		RefID:       row.ID,
		Lines: []ledgerLineSpec{
			{AccountCode: acctManualReconcile, Currency: "CNY", Amount: cents},
			{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: -cents},
		},
	})
	if err != nil {
		return empty, audit.Entry{}, adminInternal("写入 D6 记账分录失败", err)
	}

	// ---- 6 把分录 id 落回流水 ----
	if _, err := q.AttributePayment(ctx, dbgen.AttributePaymentParams{
		PaymentID: payment.ID, OrderID: &row.ID, UserID: &row.UserID,
		AmountCnyCents: &cents, State: dbgen.PaymentStatePaid, Confirmations: 0,
		LedgerEntryID: &entry.ID,
	}); err != nil {
		return empty, audit.Entry{}, adminInternal("回填流水的分录 id 失败", err)
	}

	// ---- 7 记订单收款 ----
	//
	// 🔴 `amount_paid` 必须涨。它是佣金追回的**基数**（ADR 0013 §3.5 硬规则 1），
	//    留在 0 的话，将来这张单退款时 `greatest(1, o.amount_paid)` 的除零保护会把
	//    追回额退化成「全额追回」，而那是一个被保护措施吞掉的数据错误。
	if _, err := q.RecordOrderPayment(ctx, dbgen.RecordOrderPaymentParams{
		ID: row.ID,
		// pay_amount_received 是 numeric(38,18) 的 USDT 数量（0006 的量纲铁律：
		// 它不参与任何货币再计算）。expected 是 1e-6 USDT 的整数，指数 -6 精确无损。
		PayAmountReceived: pgtype.Numeric{Int: big.NewInt(expected), Exp: -6, Valid: true},
		AmountPaid:        cents,
		GatewayRef:        &txid,
	}); err != nil {
		return empty, audit.Entry{}, adminInternal("记录订单收款失败", err)
	}

	// ---- 8 CAS 推状态 ----
	if err := adminTransitionOrder(ctx, q, row.ID, row.Status, dbgen.OrderStatusPaid,
		enteredBy, "D6 手工标记已支付："+in.Reason); err != nil {
		return empty, audit.Entry{}, err
	}
	// 与 order.go 的 markOrderPaid 同一条分工：**本轮到 paid 为止**，
	// `paid → completed` 的权益开通还没有实现（缺「首次开通」那条算 covers_from /
	// covers_to / reset_at 的查询）。响亮地停在这里，每次打一条 ERROR。
	log.ErrorContext(ctx, "D6 已把订单置为 paid，但权益开通（paid → completed）尚未实现，需要人工开通",
		"metric", "bp_order_paid_not_provisioned", "trade_no", in.TradeNo, "order_id", row.ID)

	// ---- 9 带外留痕（§16.3）----
	//
	// 🔴 放在**提交之前**是刻意的：sink 失败 → 返回 error → 整个事务回滚。
	//    反过来（提交后再打 sink）在 sink 失败时会留下一个「已经发生但没留痕」的 D6，
	//    而那正是这条裁决要防的东西。
	//    代价是可能**多记**一条：事务在 sink 成功之后、Commit 之前失败时，
	//    带外那份记录说发生过一次其实没发生的 D6。这个方向是安全的 ——
	//    多记会引来一次核对，少记谁也不会知道。
	if err := in.Sink.Record(ctx, AdminOpRecord{
		Action: "D6.order.mark_paid", AdminID: in.AdminID, AdminEmail: in.AdminEmail,
		TargetType: "order", TargetID: in.TradeNo, Reason: in.Reason,
		Evidence: in.EvidenceURL, AmountCNY: cents, RequestID: in.RequestID, At: in.Now,
	}); err != nil {
		log.ErrorContext(ctx, "D6 的带外留痕失败，整笔操作回滚（ADR 0012 §16.3）",
			"metric", "bp_admin_d6_sink_failed", "sink", in.Sink.Name(), "err", err)
		return empty, audit.Entry{}, adminInternal("带外留痕失败，本次标记已回滚", err)
	}

	// 回读一次拼响应体与审计的 after 快照。在事务内回读而不是拿 before 拼一个
	// 「我以为会变成这样」的值：审计的 after 必须是**库里真的是什么**。
	after, err := q.AdminGetOrder(ctx, in.TradeNo)
	if err != nil {
		return empty, audit.Entry{}, adminInternal("回读订单失败", err)
	}
	afterSnap := adminMarkPaidSnapshot{
		TradeNo: after.TradeNo, UserID: after.UserID, UserEmail: after.UserEmail,
		Status: string(after.Status), AmountDue: after.AmountDue, AmountPaid: after.AmountPaid,
		PayAmountUsdt6: after.PayAmountUsdt6, PaymentRowCount: after.PaymentCount,
		ReceivedUsdt6: after.ReceivedUsdt6,
		ExternalID:    externalID, EvidenceURL: in.EvidenceURL,
		LogIndexDefaulted: !in.Evidence.LogIndexExplicit,
		PostedCNYCents:    cents, LedgerEntryID: entry.ID, PaymentID: payment.ID,
	}

	log.ErrorContext(ctx, "D6 手工标记订单已支付（§16.4：目标 0–2 次/年，> 5 次/季 是红线）",
		"metric", "bp_pay_d6", "admin_id", in.AdminID, "trade_no", in.TradeNo,
		"external_id", externalID, "cny_cents", cents)

	return adminOrderDetailView(after), audit.Entry{
		Action:     "D6.order.mark_paid",
		TargetType: "order",
		TargetID:   in.TradeNo,
		Before:     before,
		After:      afterSnap,
		Reason:     in.Reason,
	}, nil
}

// adminTransitionOrder 是管理面的状态迁移：DB 层 CAS + 同事务写 order_transitions。
//
// 🔴 **不复用 order.go 的 transitionOrder**，两条理由，第二条是决定性的：
//
//  1. 那个函数吃 depositQuerier（入账路径的 12 个方法），管理面既没有也不该有
//     那套能力 —— 把它拉进来意味着 D6/D7 的窄接口上凭空多出 `AttributePayment`
//     之外的一堆入账方法，单测的假实现要为它们各写一个不会被调用的桩。
//  2. **0 行的语义相反。** transitionOrder 把 0 行当成 nil（「别的路径已经把它推走了」）
//     —— 在收款路径上那是设计内的常态（扫链与 recheck 会并发处理同一笔到账）。
//     在管理面上不是：0 行意味着有人在我们加锁读完之后改了这张单，
//     而我们已经写了流水与分录。此时静默成功就是把一次真实的并发冲突
//     变成一条自相矛盾的审计记录。必须失败并回滚。
func adminTransitionOrder(ctx context.Context, q interface {
	TransitionOrderStatus(ctx context.Context, arg dbgen.TransitionOrderStatusParams) (dbgen.TransitionOrderStatusRow, error)
	InsertOrderTransition(ctx context.Context, arg dbgen.InsertOrderTransitionParams) (dbgen.OrderTransition, error)
}, orderID int64, from, to dbgen.OrderStatus, actor, reason string) error {
	if from == to {
		return nil
	}
	if _, err := q.TransitionOrderStatus(ctx, dbgen.TransitionOrderStatusParams{
		OrderID: orderID, FromStatus: from, ToStatus: to,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return adminConflict("订单状态在本次操作期间被改动过（期望 " + string(from) + "），已回滚，请重新打开这张单再试")
		}
		return adminInternal("推进订单状态失败", err)
	}
	fromCopy := from
	if _, err := q.InsertOrderTransition(ctx, dbgen.InsertOrderTransitionParams{
		OrderID: orderID, FromStatus: &fromCopy, ToStatus: to, Reason: &reason, Actor: actor,
	}); err != nil {
		// 状态机没有触发器兜底，漏写这条不会报错 —— 而拒付申诉与「我明明付了」
		// 的工单只能靠 order_transitions 回答。写不进去必须整体回滚。
		return adminInternal("写入订单状态流水失败", err)
	}
	return nil
}

// MarkAdminOrderPaid 实现 POST /api/v1/admin/orders/{trade_no}/mark-paid（🔴 D6）。
//
// 四层强制的**检查顺序**是刻意的：L4 → L2 → 证据解析 → L1 预检 → L3 → 事务。
//
//	L4 最先：它不需要任何数据库往返，而且「这个人本来就不该有这个按钮」
//	         应当在他填的东西被读之前就回答完。
//	L3 最后：RequireStepUp 成功即**占用**那个 code（防重放）。放在最前面的话，
//	         一次填错确认串就会烧掉一个 code，管理员要等 30 秒才能重试 ——
//	         而那种摩擦的真实后果是有人去关掉 TOTP。
//	L1 做两次：事务外那一次是预检（省掉上面那个 code），事务内那一次是权威判定
//	         （读的是加锁的行）。两次用的是同一个 guardAdminConfirmation。
func (s *Server) MarkAdminOrderPaid(ctx context.Context, req gen.MarkAdminOrderPaidRequestObject) (gen.MarkAdminOrderPaidResponseObject, error) {
	actor, auth, err := s.adminAuditActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.MarkAdminOrderPaid500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "无法组装审计记录", err)}, nil
	}
	if req.Body == nil {
		return s.markPaidErr(ctx, adminUnprocessable("请求体缺失")), nil
	}
	body := *req.Body

	// ---- L4 独立权限位 ----
	if g := guardAdminPermission(auth, middleware.PermMarkOrderPaid, "手工标记订单已支付",
		""); g != nil {
		s.logAdminGuard(ctx, "D6.order.mark_paid", req.TradeNo, auth, g)
		return s.markPaidErr(ctx, g.opError()), nil
	}
	// 🔴 §16.3 裁决第 2 条的第二道锁：带外 sink 没有被端到端验证通过之前 D6 不可用。
	//    即使有人为了测试把权限位打开了，这里仍然拒绝。
	sink := s.adminOpSink()
	if !sink.Configured() {
		s.logger.ErrorContext(ctx, "D6 被拒：带外留痕 sink 未配置（ADR 0012 §16.3 要求它端到端验证通过之前 D6 不可用）",
			"metric", "bp_admin_d6_sink_unconfigured", "admin_id", auth.AdminID, "trade_no", req.TradeNo)
		return s.markPaidErr(ctx, &adminOpError{
			Status: http.StatusForbidden, Code: gen.AUTHPERMISSIONDENIED, Layer: "L4",
			Message: "手工标记已支付当前不可用：ADR 0012 §16.3 要求带外留痕通道先通过端到端验证。没有 txid 的手工入账请走「调整余额」",
		}), nil
	}

	// ---- L2 必填原因 ----
	if g := guardAdminReason(body.Reason); g != nil {
		s.logAdminGuard(ctx, "D6.order.mark_paid", req.TradeNo, auth, g)
		return s.markPaidErr(ctx, g.opError()), nil
	}

	// ---- 证据 → txid（ADR 0012 §16.1 与冻结契约的冲突就落在这里）----
	if body.EvidenceUrl == nil || strings.TrimSpace(*body.EvidenceUrl) == "" {
		return s.markPaidErr(ctx, adminUnprocessable(
			"必须提供链上交易证据 URL：ADR 0012 §16.1 要求 D6 携带真实 txid，而入账幂等键只能从它解出来",
			detail("evidence_url", "形如 https://tronscan.org/#/transaction/<64 位交易哈希>，多事件交易可写 <txid>:<log_index>"))), nil
	}
	evidence, ok := parseTronEvidence(*body.EvidenceUrl)
	if !ok {
		return s.markPaidErr(ctx, adminUnprocessable(
			"证据 URL 里解不出链上交易哈希。**不接受**没有 txid 的手工入账：入账幂等键只能来自链上，"+
				"用录入动作的 id 造一个键根本不幂等（点两次 = 两次入账两次开通）。没有 txid 的手工入账请走「调整余额」",
			detail("evidence_url", "需要 64 位十六进制交易哈希；可写 <txid>:<log_index> 指明是交易里的第几个事件"))), nil
	}

	// ---- L1 预检（非权威，只为不白烧一个 TOTP code）----
	preview, err := s.db.AdminGetOrder(ctx, req.TradeNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.markPaidErr(ctx, adminNotFound("订单不存在")), nil
	}
	if err != nil {
		return s.markPaidErr(ctx, adminInternal("读取订单失败", err)), nil
	}
	if g := guardAdminConfirmation(preview.UserEmail, body.Confirmation); g != nil {
		s.logAdminGuard(ctx, "D6.order.mark_paid", req.TradeNo, auth, g)
		return s.markPaidErr(ctx, g.opError()), nil
	}

	// ---- L3 TOTP step-up ----
	if g := guardAdminTotpPresent(req.Params.XTOTPCode); g != nil {
		s.logAdminGuard(ctx, "D6.order.mark_paid", req.TradeNo, auth, g)
		return s.markPaidErr(ctx, g.opError()), nil
	}
	if authErr := adminStepUpFor(s).RequireStepUp(ctx, req.Params.XTOTPCode); authErr != nil {
		s.logger.WarnContext(ctx, "D6 的 TOTP step-up 未通过",
			"metric", "bp_admin_d6_denied", "layer", "L3",
			"admin_id", auth.AdminID, "trade_no", req.TradeNo, "code", authErr.Code)
		return s.markPaidErr(ctx, &adminOpError{
			Status: authErr.Status, Code: gen.ErrorCode(authErr.Code), Message: authErr.Message, Layer: "L3",
		}), nil
	}

	// ---- 业务写入 + 审计，同一个事务 ----
	var out gen.AdminOrder
	in := adminMarkPaidInput{
		TradeNo: req.TradeNo, Confirmation: body.Confirmation, Reason: body.Reason,
		EvidenceURL: strings.TrimSpace(*body.EvidenceUrl), Evidence: evidence,
		AdminID: auth.AdminID, AdminEmail: auth.Email,
		RequestID: middleware.RequestIDFrom(ctx),
		Settings:  loadPaymentSettings(ctx, s.db, s.logger),
		Sink:      sink, Now: time.Now(),
	}
	err = audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		view, entry, err := adminMarkOrderPaid(ctx, q, s.logger, in)
		if err != nil {
			return audit.Entry{}, err
		}
		out = view
		return entry, nil
	})
	if err != nil {
		return s.markPaidErr(ctx, asAdminOpError(err)), nil
	}
	return gen.MarkAdminOrderPaid200JSONResponse{Data: out, Meta: s.meta(ctx)}, nil
}

// markPaidErr 把 adminOpError 映射成本 operation 声明过的那几个响应。
func (s *Server) markPaidErr(ctx context.Context, e *adminOpError) gen.MarkAdminOrderPaidResponseObject {
	switch e.Status {
	case http.StatusForbidden:
		return gen.MarkAdminOrderPaid403JSONResponse{ErrForbiddenJSONResponse: s.forbidden(ctx, e.Code, e.Message)}
	case http.StatusNotFound:
		return gen.MarkAdminOrderPaid404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, e.Message)}
	case http.StatusConflict:
		return gen.MarkAdminOrderPaid409JSONResponse{ErrConflictJSONResponse: s.conflict(ctx, e.Message)}
	case http.StatusUnprocessableEntity:
		return gen.MarkAdminOrderPaid422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, e.Message, e.Details...)}
	default:
		return gen.MarkAdminOrderPaid500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "D6 手工标记已支付失败", e)}
	}
}

// logAdminGuard 记一次被四层强制拦下的危险操作。
//
// 🔴 **被拒的危险操作同样要留痕，但留在服务端日志而不是 audit_logs。**
// audit_logs 记的是「做过什么」，一条没有发生的操作写进去会稀释它；
// 而「有人在反复试探 D6」这件事必须看得见 —— 它在日志里，且带固定的 metric 名，
// 可以直接建成 log-based metric。
func (s *Server) logAdminGuard(ctx context.Context, action, target string, auth *middleware.AdminAuth, g *adminGuardFailure) {
	s.logger.WarnContext(ctx, "管理面危险操作被四层强制拦下",
		"metric", "bp_admin_danger_denied", "action", action, "layer", g.Layer,
		"admin_id", auth.AdminID, "admin_email", auth.Email, "target", target,
		"request_id", middleware.RequestIDFrom(ctx))
}

// ============================================================
// D7 · refundAdminOrder（ADR 0013 §3）
// ============================================================

// 退款档（ADR 0013 §3.2）。值直接写进 `refunds.rule`，DDL 的 CHECK 认这四个。
const (
	refundRuleCoolingOff = "cooling_off"
	refundRuleProrated   = "prorated"
)

// coolingOffWindow / coolingOffTrafficCap 是 Class A 的两道闸门（ADR 0013 §3.2）。
//
// 7 天沿用 payments §4.7；流量闸门 min(套餐配额 10%, 10 GiB) 的出处是同一节的
// 数字表：出口现金上界 = 10 GiB × $0.0979/GiB = $0.98/次，叠加「一账号一生一次」
// 与 50 人上界之后年度 $49。这两个常数改动前先读那张表。
const (
	coolingOffWindow     = 7 * 24 * time.Hour
	coolingOffTrafficCap = int64(10) << 30 // 10 GiB
)

// refundBasisSummary 是 GetRefundBasis 的汇总（那四列在所有行上取值相同）。
type refundBasisSummary struct {
	VWindow            int64 `json:"v_window_cents"`
	ConsumedTime       int64 `json:"consumed_time_cents"`
	ConsumedData       int64 `json:"consumed_data_cents"`
	RefundB            int64 `json:"refund_b_cents"`
	PlanUsed           int64 `json:"plan_used_bytes"`
	TransferEnablePlan int64 `json:"transfer_enable_plan_bytes"`
	Segments           int   `json:"segments"`
	// MissingPriceSnapshot：窗口链上有一段的 price_monthly_at_order 是 NULL
	// （0016 之前的历史订单）。SQL 里 coalesce 成 0 是为了不让 sum() 静默吞掉整条链，
	// 但那意味着 consumed_time 少算 —— **少算的方向对用户有利、对我们不利**，
	// 而且没有任何迹象。调用方看见它必须**拒绝退款并转人工**，不能当成 0 接着算。
	MissingPriceSnapshot bool `json:"missing_price_snapshot"`
}

// summarizeRefundBasis 把 GetRefundBasis 的多行汇总成一个结论。
func summarizeRefundBasis(rows []dbgen.GetRefundBasisRow) (refundBasisSummary, error) {
	if len(rows) == 0 {
		// 0 行只可能是锚点订单不存在 —— 而我们刚刚才加锁读到它。
		return refundBasisSummary{}, errors.New("退款基数查询没有返回任何窗口段")
	}
	s := refundBasisSummary{
		VWindow: rows[0].VWindow, ConsumedTime: rows[0].ConsumedTime,
		ConsumedData: rows[0].ConsumedData, RefundB: rows[0].RefundB,
		PlanUsed: rows[0].PlanUsed, TransferEnablePlan: rows[0].TransferEnablePlan,
		Segments: len(rows),
	}
	for i := range rows {
		if rows[i].PriceMonthlyAtOrder == nil {
			s.MissingPriceSnapshot = true
		}
	}
	return s, nil
}

// refundDecision 是判档的结果。
type refundDecision struct {
	Rule string
	// MaxAmount 是这一次**还能退**的上限（已扣掉此前退到余额的部分）。
	MaxAmount int64
	// ClassAGates 记录 Class A 五道闸门各自的结论，进审计与日志。
	// 记全部而不是只记「过没过」：事后要回答的问题是「他凭什么拿到全额退款」。
	ClassAGates map[string]bool `json:"class_a_gates"`
}

// classifyRefundInput 是判档的全部输入。做成结构体而不是一串参数：
// 五道闸门里漏传一个的现象是「所有人都能拿全额退款」，而位置参数漏传不会报错。
type classifyRefundInput struct {
	OrderType         dbgen.OrderType
	OrderStatus       dbgen.OrderStatus
	CoversFrom        pgtype.Timestamptz
	SettledOrderCount int64
	CoolingOffUsed    int64
	UserBanned        bool
	AlreadyRefunded   int64
	Basis             refundBasisSummary
	Now               time.Time
}

// classifyRefund 判档并算出本次可退上限（ADR 0013 §3.2）。
//
// 返回 error 即 Class C「一律不退」，错误本身带着该回几与说什么。
func classifyRefund(in classifyRefundInput) (refundDecision, *adminOpError) {
	// ---- Class C 之一：一次性商品不退 ----
	switch in.OrderType {
	case dbgen.OrderTypeTrafficPack, dbgen.OrderTypeResetPack, dbgen.OrderTypeWalletTopup:
		return refundDecision{}, adminUnprocessable(
			"流量包 / 流量重置包 / 钱包充值不予退款（ADR 0013 §3.6 条款 3）。" +
				"这类订单也不该走退款基数计算：它们不在订阅窗口链上")
	}

	// 只有真的收到过钱的订单才谈得上退。pending / paying / underpaid 还没收全，
	// cancelled / expired 从来没收；refunded / partially_refunded 由下面的额度闸门管。
	switch in.OrderStatus {
	case dbgen.OrderStatusPaid, dbgen.OrderStatusCompleted,
		dbgen.OrderStatusPartiallyRefunded:
	default:
		return refundDecision{}, adminConflict(
			"订单当前状态是 " + string(in.OrderStatus) + "，没有可退的款项（只有 paid / completed / partially_refunded 可退）")
	}

	if in.Basis.MissingPriceSnapshot {
		// 见 refundBasisSummary.MissingPriceSnapshot。
		return refundDecision{}, adminUnprocessable(
			"该订单的订阅窗口链上有一段缺少月付标价快照（0016 之前的历史订单），" +
				"按 0 计算会让已消费时间被少算、退款额偏高且无迹可循。请转人工核算后走「调整余额」")
	}

	gates := map[string]bool{
		"first_settled_order":  in.SettledOrderCount <= 1,
		"within_7_days":        in.CoversFrom.Valid && in.Now.Sub(in.CoversFrom.Time) <= coolingOffWindow,
		"traffic_under_cap":    in.Basis.PlanUsed <= minInt64(in.Basis.TransferEnablePlan/10, coolingOffTrafficCap),
		"not_banned":           !in.UserBanned,
		"no_prior_cooling_off": in.CoolingOffUsed == 0,
	}
	classA := true
	for _, ok := range gates {
		if !ok {
			classA = false
		}
	}

	d := refundDecision{Rule: refundRuleProrated, MaxAmount: in.Basis.RefundB, ClassAGates: gates}
	if classA {
		// Class A 豁免 consumed_time 与 consumed_data 两项扣减，直接退 V_window。
		d.Rule, d.MaxAmount = refundRuleCoolingOff, in.Basis.VWindow
	}

	// 🔴 **扣掉此前已经退到余额的部分。** GetRefundBasis **不看 refunds 表** ——
	//    V_window 是「这条窗口链上一共实付过多少」，refund_B 是「按已消费扣减后还剩多少」，
	//    两者都没有减去上一次退款。不在这里减的后果是：对同一张单调用两次 partial 退款，
	//    第二次仍然算得出同样的额度，于是同一笔钱可以被退两遍甚至更多。
	//    `refunded_to_balance` 是 refunds.amount 的求和，正是退款总额的唯一真相源
	//    （`orders.amount_refunded` 只记真的退出去的现金，余额路径上恒为 0，不能用）。
	d.MaxAmount -= in.AlreadyRefunded
	if d.MaxAmount <= 0 {
		return refundDecision{}, adminUnprocessable(
			"按 ADR 0013 §3.2 计算，这张单已经没有可退金额（计算结果为零或负数时不予退款）",
			refundBreakdownDetails(in.Basis, in.AlreadyRefunded, d.Rule)...)
	}
	return d, nil
}

// refundBreakdownDetails 把扣减明细拼成 error.details。
//
// 🔴 **这是「把扣减明细算给操作者看」在冻结契约下唯一的落点。**
// 成功响应的 schema 是 `AdminOrder`，上面没有任何地方能放 V_window / consumed_time /
// consumed_data 这三行；而 user-journey §10.2 与本 ADR 都要求这三行要能给人看。
// 于是明细走两条路：① 被拒时进 422 的 details（操作者当场看得到算式）；
// ② 成功时进 audit_logs 的 after 快照（`GET /admin/audit` 能读回来）。
// 缺口已登记：契约需要一个 refund 预览端点或在响应上加一个 breakdown 对象。
func refundBreakdownDetails(b refundBasisSummary, alreadyRefunded int64, rule string) []gen.ErrorDetail {
	return []gen.ErrorDetail{
		detail("v_window", fmt.Sprintf("本次订阅期内实付合计 %d 分（%d 段窗口链求和，不含折抵与优惠码面值）", b.VWindow, b.Segments)),
		detail("consumed_time", fmt.Sprintf("已服务时间按月付标价折算 %d 分", b.ConsumedTime)),
		detail("consumed_data", fmt.Sprintf("已消耗套餐流量按月付标价折算 %d 分（已用 %d / 配额 %d 字节）", b.ConsumedData, b.PlanUsed, b.TransferEnablePlan)),
		detail("refund_b", fmt.Sprintf("常规退款额 = max(0, v_window − consumed_time − consumed_data) = %d 分", b.RefundB)),
		detail("already_refunded", fmt.Sprintf("此前已退到余额 %d 分", alreadyRefunded)),
		detail("rule", "本次适用档位："+rule),
	}
}

// clawbackAction 是对一条佣金的处置（ADR 0013 §3.5 第 4 步）。
type clawbackAction struct {
	CommissionID int64  `json:"commission_id"`
	InviterID    int64  `json:"inviter_id"`
	Status       string `json:"status"`
	// Clawback 是按比例算出来的应追回额（SQL 里 ceil，向上取整）。
	Clawback int64 `json:"clawback_cents"`
	// Void 为 true 表示这条佣金还没进过用户钱包，直接作废即可（pending / confirmed）。
	Void bool `json:"void"`
	// Recover / Shortfall 只在 transferred 上有意义：钱已经在钱包里，
	// 能扣多少扣多少，扣不动的部分是我们的真实损失。
	Recover   int64 `json:"recover_cents"`
	Shortfall int64 `json:"shortfall_cents"`
	// ZeroBase 记录「amount_paid = 0 却生成了佣金」这个数据错误（见 planClawback）。
	ZeroBase bool `json:"zero_base"`
}

// planClawback 把每条佣金分成三种处置。**纯函数**，因为它是本组最容易算错、
// 而算错之后完全不报错的一段。
//
// 三条硬规则（ADR 0013 §3.5，草稿完全没有）：
//
//  1. 基数写死 `orders.amount_paid`（SQL 里已经算好 clawback_amount），
//     不含 amount_balance 与 surplus_amount —— 一句话同时封住「用余额刷佣金」
//     与「用折抵刷佣金」。
//  2. **不论状态**都追回。Class B 没有窗口上限（年付到第 270 天都还有退款额），
//     任何有限的佣金冷静期都挡不住「等到第 16 天 confirmed 之后再退款」。
//  3. `amount_paid = 0` 而又生成了佣金**必须告警**：SQL 里的 `greatest(1, ·)`
//     只防除零，此时本式退化成「全额追回」（方向对我们有利），但那是个数据错误 ——
//     被除零保护吞掉就永远不会被发现。
//
// pending / confirmed 直接作废：这两个状态的钱**还没进过 wallet_balances**
// （佣金是在划转那一刻才写分录 + 加余额的，见 wallet.sql 的 LockTransferableCommissions）。
// 对它们扣余额就是从用户手里拿走另一笔钱。ADR §3.5 把 confirmed 与 transferred 写在一起，
// 但 `VoidCommission` 的 status 守卫（`IN ('pending','confirmed')`）与划转路径的实现
// 一起说明了 confirmed 尚未入账 —— 以实现为准，并在这里记下这处解读。
func planClawback(rows []dbgen.AdminListOrderCommissionsForClawbackRow) []clawbackAction {
	out := make([]clawbackAction, 0, len(rows))
	for _, r := range rows {
		a := clawbackAction{
			CommissionID: r.ID, InviterID: r.InviterID, Status: r.Status,
			Clawback: r.ClawbackAmount, ZeroBase: r.OrderAmountPaid == 0,
		}
		switch r.Status {
		case "transferred":
			a.Recover = minInt64(r.ClawbackAmount, r.InviterBalance)
			if a.Recover < 0 {
				a.Recover = 0
			}
			a.Shortfall = r.ClawbackAmount - a.Recover
		default:
			a.Void = true
		}
		out = append(out, a)
	}
	return out
}

// adminRefundQuerier 是 D7 事务体的全部数据面。
type adminRefundQuerier interface {
	ledgerQuerier
	AdminGetOrderForRefund(ctx context.Context, tradeNo string) (dbgen.AdminGetOrderForRefundRow, error)
	GetUserByID(ctx context.Context, id int64) (dbgen.User, error)
	GetRefundBasis(ctx context.Context, id int64) ([]dbgen.GetRefundBasisRow, error)
	CreateRefund(ctx context.Context, arg dbgen.CreateRefundParams) (dbgen.Refund, error)
	UpdateRefundStatus(ctx context.Context, arg dbgen.UpdateRefundStatusParams) (dbgen.Refund, error)
	UpsertWalletBalance(ctx context.Context, arg dbgen.UpsertWalletBalanceParams) (dbgen.WalletBalance, error)
	AdminTerminateSubscriptionForRefund(ctx context.Context, userID int64) (dbgen.AdminTerminateSubscriptionForRefundRow, error)
	AdminListOrderCommissionsForClawback(ctx context.Context, arg dbgen.AdminListOrderCommissionsForClawbackParams) ([]dbgen.AdminListOrderCommissionsForClawbackRow, error)
	VoidCommission(ctx context.Context, arg dbgen.VoidCommissionParams) (dbgen.Commission, error)
	TransitionOrderStatus(ctx context.Context, arg dbgen.TransitionOrderStatusParams) (dbgen.TransitionOrderStatusRow, error)
	InsertOrderTransition(ctx context.Context, arg dbgen.InsertOrderTransitionParams) (dbgen.OrderTransition, error)
	AdminGetOrder(ctx context.Context, tradeNo string) (dbgen.AdminGetOrderRow, error)
}

type adminRefundInput struct {
	TradeNo    string
	Amount     *int64
	Reason     string
	AdminID    int64
	AdminEmail string
	Now        time.Time
}

// adminRefundBefore / adminRefundAfter 是审计快照。
type adminRefundBefore struct {
	TradeNo           string `json:"trade_no"`
	UserID            int64  `json:"user_id"`
	UserEmail         string `json:"user_email"`
	OrderStatus       string `json:"order_status"`
	AmountPaid        int64  `json:"amount_paid_cents"`
	RefundedToBalance int64  `json:"refunded_to_balance_cents"`
	UserBanned        bool   `json:"user_banned"`
	// 退款前用户手上有什么。这一份取自**决定退多少钱那一刻**的读，
	// 不是终止订阅那条语句自己返回的前像（两者读的是不同时刻）。
	UserPlanID             *int64     `json:"user_plan_id"`
	UserExpiredAt          *time.Time `json:"user_expired_at"`
	UserTransferEnablePlan int64      `json:"user_transfer_enable_plan"`
	UserTransferEnablePack int64      `json:"user_transfer_enable_pack"`
}

type adminRefundAfter struct {
	OrderStatus string             `json:"order_status"`
	Rule        string             `json:"rule"`
	RefundID    int64              `json:"refund_id"`
	Amount      int64              `json:"refund_amount_cents"`
	MaxAmount   int64              `json:"max_refundable_cents"`
	Basis       refundBasisSummary `json:"basis"`
	ClassAGates map[string]bool    `json:"class_a_gates"`
	Clawbacks   []clawbackAction   `json:"commission_clawbacks"`
	// 订阅终止的后像。`transfer_enable_pack` 必须在这里看得见没被动过 ——
	// 「退款之后用户的加油包不见了」是一个字的差别造成的、用户不知道去投诉什么的故障。
	UserTransferEnablePlan int64 `json:"user_transfer_enable_plan_after"`
	UserTransferEnablePack int64 `json:"user_transfer_enable_pack_after"`
	WalletBalanceAfter     int64 `json:"user_wallet_balance_after_cents"`
}

// adminRefundOrder 是 D7 的事务体（ADR 0013 §3.5 的五步，全部在一个事务里）。
func adminRefundOrder(ctx context.Context, q adminRefundQuerier, log *slog.Logger, in adminRefundInput) (gen.AdminOrder, audit.Entry, error) {
	var empty gen.AdminOrder

	row, err := q.AdminGetOrderForRefund(ctx, in.TradeNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, audit.Entry{}, adminNotFound("订单不存在")
	}
	if err != nil {
		return empty, audit.Entry{}, adminInternal("读取订单失败", err)
	}

	// 用户是否被封禁是 Class A 的第四道闸门（也是 §3.6 条款 5 的输入）。
	// GetUserByID 带 `deleted_at IS NULL`：已注销用户没有钱包可退。
	user, err := q.GetUserByID(ctx, row.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, audit.Entry{}, adminConflict(
			"该订单所属用户已注销，退款无处可退（余额只可消费不可提现）")
	}
	if err != nil {
		return empty, audit.Entry{}, adminInternal("读取用户失败", err)
	}
	if user.Banned {
		// **不自动判成 Class C。** §3.6 条款 5 的原文是「违反服务条款（转售、超范围共享订阅）
		// 而被停用的账号，不予退款」—— 「封禁原因属不属于违规使用」是一次人的判断，
		// 机器判不了。这里的做法是：Class A 的闸门自动关掉（下面的 gates），
		// 常规退款仍然可以做，但必须留下这条 ERROR 与审计里的 user_banned 标记，
		// 让复盘时一眼看见「这笔退款发生在一个被封禁的账号上」。
		log.ErrorContext(ctx, "对一个处于封禁状态的账号执行退款，请复核封禁原因是否属违规使用（ADR 0013 §3.6 条款 5）",
			"metric", "bp_admin_refund_banned_user", "user_id", row.UserID, "trade_no", in.TradeNo,
			"banned_reason", user.BannedReason)
	}

	basisRows, err := q.GetRefundBasis(ctx, row.ID)
	if err != nil {
		return empty, audit.Entry{}, adminInternal("计算退款基数失败", err)
	}
	basis, err := summarizeRefundBasis(basisRows)
	if err != nil {
		return empty, audit.Entry{}, adminInternal("退款基数不可用", err)
	}

	decision, opErr := classifyRefund(classifyRefundInput{
		OrderType: row.Type, OrderStatus: row.Status, CoversFrom: row.CoversFrom,
		SettledOrderCount: row.SettledOrderCount, CoolingOffUsed: row.CoolingOffUsed,
		UserBanned: user.Banned, AlreadyRefunded: row.RefundedToBalance,
		Basis: basis, Now: in.Now,
	})
	if opErr != nil {
		return empty, audit.Entry{}, opErr
	}

	// `RefundRequest.amount` 可选，缺省全额。给了就必须落在 (0, MaxAmount] 里 ——
	// **不是「按管理员说的退」**：那样 D7 就退化成一个可以填任意金额的转账按钮。
	amount := decision.MaxAmount
	if in.Amount != nil {
		amount = *in.Amount
		if amount <= 0 || amount > decision.MaxAmount {
			return empty, audit.Entry{}, adminUnprocessable(
				fmt.Sprintf("退款金额必须在 1 到 %d 分之间（按 ADR 0013 §3.2 算出的本次可退上限）", decision.MaxAmount),
				refundBreakdownDetails(basis, row.RefundedToBalance, decision.Rule)...)
		}
	}

	before := adminRefundBefore{
		TradeNo: row.TradeNo, UserID: row.UserID, UserEmail: row.UserEmail,
		OrderStatus: string(row.Status), AmountPaid: row.AmountPaid,
		RefundedToBalance: row.RefundedToBalance, UserBanned: user.Banned,
		UserPlanID: row.UserPlanID, UserExpiredAt: tsPtr(row.UserExpiredAt),
		UserTransferEnablePlan: row.UserTransferEnablePlan,
		UserTransferEnablePack: row.UserTransferEnablePack,
	}

	// ---- 1 记录退款 ----
	//
	// 🔴 **先插 refunds，再动钱。** `refunds_cooling_off_once` 这条部分唯一索引
	//    是「冷静期退款一生一次」的真正闸门（不是上面那个 cooling_off_used ——
	//    读与插之间有窗口，而用户连点两次「申请退款」是真实场景）。
	//    把它放在第一步，冲突时还没有任何钱被动过。
	reason := in.Reason
	operator := in.AdminID
	refund, err := q.CreateRefund(ctx, dbgen.CreateRefundParams{
		OrderID: row.ID, UserID: row.UserID, Amount: amount,
		Destination: "balance", Rule: decision.Rule,
		Reason: &reason, OperatorID: &operator,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return empty, audit.Entry{}, adminConflict(
				"该账号已经使用过一次冷静期全额退款（每个账号仅限一次，由数据库唯一索引强制）。" +
					"若本次应当走常规按比例退款，说明判档输入有误，请核对首单与 7 天窗口")
		}
		return empty, audit.Entry{}, adminInternal("写入退款记录失败", err)
	}
	// 余额路径是单事务、没有外部网关往返，所以不经过 pending。
	// 显式改成 done 而不是让它停在 DEFAULT 'pending'：一条永远停在 pending 的退款
	// 会让对账页把它算成「在途」。
	if _, err := q.UpdateRefundStatus(ctx, dbgen.UpdateRefundStatusParams{
		ID: refund.ID, Status: "done",
	}); err != nil {
		return empty, audit.Entry{}, adminInternal("更新退款状态失败", err)
	}

	// ---- 2 记账 + 加余额 ----
	//
	// 🔴 `destination='balance'` 的分录**只有这两条腿，绝不允许碰 expense:refund**
	//    （ADR 0013 §3.5 第 2 步）。一旦记进去，损益表上会凭空出现一笔从未发生的费用，
	//    而「无现金流出」这个论证的账面证据就没了。
	entry, err := postLedgerEntry(ctx, q, ledgerEntrySpec{
		EntryNo:     ledgerEntryNo("RFD", in.TradeNo+":"+strconv.FormatInt(refund.ID, 10)),
		Description: "退款入余额 " + in.TradeNo,
		RefType:     "order",
		RefID:       row.ID,
		Lines: []ledgerLineSpec{
			{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: amount},
			{AccountCode: acctUserWallet, Currency: "CNY", Amount: -amount, SubjectID: &row.UserID},
		},
	})
	if err != nil {
		return empty, audit.Entry{}, adminInternal("写入退款分录失败", err)
	}
	// UpsertWalletBalance 的 balance 参数是**增量**不是绝对值（ON CONFLICT 里是 `+ EXCLUDED.balance`）。
	wallet, err := q.UpsertWalletBalance(ctx, dbgen.UpsertWalletBalanceParams{
		UserID: row.UserID, Currency: "CNY", Balance: amount, LastEntryID: &entry.ID,
	})
	if err != nil {
		return empty, audit.Entry{}, adminInternal("增加用户余额失败", err)
	}

	// ---- 3 立即终止订阅 ----
	term, err := q.AdminTerminateSubscriptionForRefund(ctx, row.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, audit.Entry{}, adminConflict("该订单所属用户已注销，无法终止订阅")
	}
	if err != nil {
		return empty, audit.Entry{}, adminInternal("终止订阅失败", err)
	}
	if term.TransferEnablePack != row.UserTransferEnablePack {
		// 加油包是单独付过钱的、且不在退款基数里（consumed_data 的分子截到
		// transfer_enable_plan）。终止订阅顺手没收它，是在退款的同时又拿走一笔
		// 用户已付的东西 —— 而用户不会知道去投诉什么。这条断言是它的唯一护栏。
		return empty, audit.Entry{}, adminInternal("终止订阅时加油包配额被改动（ADR 0013 §5.5 禁止）",
			fmt.Errorf("pack %d → %d", row.UserTransferEnablePack, term.TransferEnablePack))
	}

	// ---- 4 佣金按比例追回，不论状态 ----
	commissions, err := q.AdminListOrderCommissionsForClawback(ctx, dbgen.AdminListOrderCommissionsForClawbackParams{
		RefundAmount: amount, OrderID: row.ID,
	})
	if err != nil {
		return empty, audit.Entry{}, adminInternal("读取待追回佣金失败", err)
	}
	actions := planClawback(commissions)
	for _, a := range actions {
		if a.ZeroBase {
			// 见 planClawback 硬规则 3。
			log.ErrorContext(ctx, "佣金的订单实付金额为 0，追回额退化成全额；这是一个必须排查的数据错误",
				"metric", "bp_commission_zero_base", "commission_id", a.CommissionID,
				"trade_no", in.TradeNo, "order_id", row.ID)
		}
		if a.Void {
			voided := "order_refunded"
			if _, err := q.VoidCommission(ctx, dbgen.VoidCommissionParams{ID: a.CommissionID, VoidedReason: &voided}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					// status 守卫没命中：这条佣金在我们锁它之后被改走了。
					// 整笔退款回滚 —— 少追一笔佣金是给套利留的口子。
					return empty, audit.Entry{}, adminConflict("佣金状态在本次退款期间被改动过，已回滚，请重试")
				}
				return empty, audit.Entry{}, adminInternal("作废佣金失败", err)
			}
			continue
		}
		if err := adminClawbackTransferred(ctx, q, a, in.TradeNo, row.ID); err != nil {
			return empty, audit.Entry{}, err
		}
		if a.Shortfall > 0 {
			log.ErrorContext(ctx, "佣金追回不足额：邀请人余额不够，差额已记为真实损失，需要人工处理",
				"metric", "bp_commission_clawback_shortfall", "commission_id", a.CommissionID,
				"inviter_id", a.InviterID, "shortfall_cents", a.Shortfall, "trade_no", in.TradeNo)
		}
	}

	// ---- 5 推进订单状态 ----
	//
	// 「退完了没有」以**本次之后还有没有可退额**为准：amount 达到了本次上限
	// 就是 refunded，否则是 partially_refunded。用 amount 与 amount_paid 比是错的 ——
	// 退款基数是 V_window（含 amount_balance），两者根本不是同一个量。
	target := dbgen.OrderStatusPartiallyRefunded
	if amount >= decision.MaxAmount {
		target = dbgen.OrderStatusRefunded
	}
	if err := adminTransitionOrder(ctx, q, row.ID, row.Status, target,
		"admin:"+strconv.FormatInt(in.AdminID, 10),
		fmt.Sprintf("D7 退款 %d 分（%s）：%s", amount, decision.Rule, in.Reason)); err != nil {
		return empty, audit.Entry{}, err
	}

	after, err := q.AdminGetOrder(ctx, in.TradeNo)
	if err != nil {
		return empty, audit.Entry{}, adminInternal("回读订单失败", err)
	}

	// 明细同时进日志：契约的成功响应放不下它（见 refundBreakdownDetails）。
	log.WarnContext(ctx, "D7 退款完成（退到不可提现余额，订阅已立即终止，用户最长 60 秒后掉线）",
		"metric", "bp_admin_refund", "admin_id", in.AdminID, "trade_no", in.TradeNo,
		"rule", decision.Rule, "amount_cents", amount, "max_cents", decision.MaxAmount,
		"v_window", basis.VWindow, "consumed_time", basis.ConsumedTime,
		"consumed_data", basis.ConsumedData, "refund_b", basis.RefundB,
		"already_refunded", row.RefundedToBalance)

	return adminOrderDetailView(after), audit.Entry{
		Action:     "D7.order.refund",
		TargetType: "order",
		TargetID:   in.TradeNo,
		Before:     before,
		After: adminRefundAfter{
			OrderStatus: string(after.Status), Rule: decision.Rule, RefundID: refund.ID,
			Amount: amount, MaxAmount: decision.MaxAmount, Basis: basis,
			ClassAGates: decision.ClassAGates, Clawbacks: actions,
			UserTransferEnablePlan: term.TransferEnablePlan,
			UserTransferEnablePack: term.TransferEnablePack,
			WalletBalanceAfter:     wallet.Balance,
		},
		Reason: in.Reason,
	}, nil
}

// adminClawbackTransferred 从邀请人钱包里扣回一条**已划转**佣金。
//
// 两条腿是划转那条分录的反向（划转是 Dr expense:commission / Cr liability:user_wallet）。
// 追不回来的部分**改记 expense:refund**：ADR 0013 §3.5 把 expense:refund 的用途写死为
// 「destination='original' 的退款，以及第 4 步追不回来的佣金」，而 0018 建 expense:commission
// 的理由是「它除以新增用户数就是 CAC」—— 把一笔追不回来的钱继续挂在 CAC 下面，
// 那个数字就不再是获客成本了。所以这里做的是**科目重分类**（借 expense:refund /
// 贷 expense:commission），总费用不变、桶变了。不是再记一笔新费用：那会把同一笔钱数两遍。
func adminClawbackTransferred(ctx context.Context, q adminRefundQuerier, a clawbackAction, tradeNo string, orderID int64) error {
	lines := make([]ledgerLineSpec, 0, 4)
	inviter := a.InviterID
	if a.Recover > 0 {
		lines = append(lines,
			ledgerLineSpec{AccountCode: acctUserWallet, Currency: "CNY", Amount: a.Recover, SubjectID: &inviter},
			ledgerLineSpec{AccountCode: acctCommissionExpense, Currency: "CNY", Amount: -a.Recover},
		)
	}
	if a.Shortfall > 0 {
		lines = append(lines,
			ledgerLineSpec{AccountCode: acctRefundExpense, Currency: "CNY", Amount: a.Shortfall},
			ledgerLineSpec{AccountCode: acctCommissionExpense, Currency: "CNY", Amount: -a.Shortfall},
		)
	}
	if len(lines) == 0 {
		return nil
	}
	entry, err := postLedgerEntry(ctx, q, ledgerEntrySpec{
		EntryNo:     ledgerEntryNo("CLB", tradeNo+":"+strconv.FormatInt(a.CommissionID, 10)),
		Description: "退款追回佣金 " + tradeNo,
		RefType:     "commission",
		RefID:       a.CommissionID,
		Lines:       lines,
	})
	if err != nil {
		return adminInternal("写入佣金追回分录失败", err)
	}
	if a.Recover <= 0 {
		return nil
	}
	neg := -a.Recover
	if _, err := q.UpsertWalletBalance(ctx, dbgen.UpsertWalletBalanceParams{
		UserID: inviter, Currency: "CNY", Balance: neg, LastEntryID: &entry.ID,
	}); err != nil {
		if isCheckViolation(err) {
			// wallet_balances 的 CHECK (balance >= 0)。我们读余额时锁的是 commissions
			// 不是 wallet_balances，所以邀请人在这中间花掉一笔钱是可能的。
			// 让它冒成 500 的现象是「退款偶尔报服务器错误」，没有人会想到去看余额。
			return adminConflict("邀请人的余额在本次退款期间发生了变化，佣金追不动，已回滚，请重试")
		}
		return adminInternal("扣减邀请人余额失败", err)
	}
	return nil
}

// 佣金相关的两个科目（0018 建的 expense:commission；expense:refund 在 0015）。
const (
	acctCommissionExpense = "expense:commission"
	acctRefundExpense     = "expense:refund"
)

// RefundAdminOrder 实现 POST /api/v1/admin/orders/{trade_no}/refund（D7）。
//
// 四层强制在 D7 上的适用：
//
//	L1 —— **不适用**。§6.2 的表里 D7 不在「标 🔒」那一列，冻结的 `RefundRequest`
//	      上也没有 confirmation 字段。这不是遗漏，是契约的选择。
//	L2 —— 适用（§6.2 的 L2 行里有 D7）。
//	L3 —— **不适用**（§6.2 的 L3 行里没有 D7）。
//	L4 —— `admin_users.perm_refund`。⚠️ 这个权限位在 openapi 的 `AdminPermission`
//	      枚举里**没有对应值**：库里有列、这里判得了，但**通过 API 授不了**，
//	      只能由迁移或 DBA 直接置位。缺口已登记，拒绝文案会把这句话告诉操作者，
//	      否则运维会在「管理员管理」页上反复找一个不存在的开关。
//	      仍然判它而不是「契约没说就不判」：一个直接把钱变成余额的按钮，
//	      默认应当是**所有人都点不动**。
func (s *Server) RefundAdminOrder(ctx context.Context, req gen.RefundAdminOrderRequestObject) (gen.RefundAdminOrderResponseObject, error) {
	actor, auth, err := s.adminAuditActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.RefundAdminOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "无法组装审计记录", err)}, nil
	}
	if req.Body == nil {
		return s.refundErr(ctx, adminUnprocessable("请求体缺失")), nil
	}
	body := *req.Body

	if g := guardAdminPermission(auth, middleware.PermRefund, "退款",
		"admin_users.perm_refund"); g != nil {
		s.logAdminGuard(ctx, "D7.order.refund", req.TradeNo, auth, g)
		return s.refundErr(ctx, g.opError()), nil
	}
	if g := guardAdminReason(body.Reason); g != nil {
		s.logAdminGuard(ctx, "D7.order.refund", req.TradeNo, auth, g)
		return s.refundErr(ctx, g.opError()), nil
	}

	var out gen.AdminOrder
	in := adminRefundInput{
		TradeNo: req.TradeNo, Amount: body.Amount, Reason: body.Reason,
		AdminID: auth.AdminID, AdminEmail: auth.Email, Now: time.Now(),
	}
	err = audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		view, entry, err := adminRefundOrder(ctx, q, s.logger, in)
		if err != nil {
			return audit.Entry{}, err
		}
		out = view
		return entry, nil
	})
	if err != nil {
		return s.refundErr(ctx, asAdminOpError(err)), nil
	}
	return gen.RefundAdminOrder200JSONResponse{Data: out, Meta: s.meta(ctx)}, nil
}

func (s *Server) refundErr(ctx context.Context, e *adminOpError) gen.RefundAdminOrderResponseObject {
	switch e.Status {
	case http.StatusForbidden:
		return gen.RefundAdminOrder403JSONResponse{ErrForbiddenJSONResponse: s.forbidden(ctx, e.Code, e.Message)}
	case http.StatusNotFound:
		return gen.RefundAdminOrder404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, e.Message)}
	case http.StatusConflict:
		return gen.RefundAdminOrder409JSONResponse{ErrConflictJSONResponse: s.conflict(ctx, e.Message)}
	case http.StatusUnprocessableEntity:
		return gen.RefundAdminOrder422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, e.Message, e.Details...)}
	default:
		return gen.RefundAdminOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "D7 退款失败", e)}
	}
}

// ============================================================
// 支付与对账（模块 14）
// ============================================================

// ListAdminPayments 实现 GET /api/v1/admin/payments。
func (s *Server) ListAdminPayments(ctx context.Context, req gen.ListAdminPaymentsRequestObject) (gen.ListAdminPaymentsResponseObject, error) {
	if _, err := s.adminReadAuth(ctx); err != nil {
		return nil, err
	}
	want, limitPlusOne := pageLimit(req.Params.Limit)
	params := dbgen.AdminListPaymentsPageParams{PageLimit: limitPlusOne}
	if req.Params.Cursor != nil && *req.Params.Cursor != "" {
		if cur, ok := decodePageCursor(*req.Params.Cursor); ok {
			// 排序键是 (received_at, id) —— `payments` 没有 created_at 这一列。
			params.CursorAt = tstz(cur.At)
			params.CursorID = &cur.ID
		} else {
			s.logger.WarnContext(ctx, "管理面支付流水游标非法，按第一页处理",
				"request_id", middleware.RequestIDFrom(ctx), "cursor_len", len(*req.Params.Cursor))
		}
	}

	rows, err := s.db.AdminListPaymentsPage(ctx, params)
	if err != nil {
		return gen.ListAdminPayments500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取支付流水失败", err)}, nil
	}
	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	data := make([]gen.AdminPayment, 0, len(rows))
	for i := range rows {
		data = append(data, adminPaymentListView(rows[i]))
	}
	meta := s.meta(ctx)
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		if last.ReceivedAt.Valid {
			if enc := encodePageCursor(last.ID, last.ReceivedAt.Time.UTC()); enc != "" {
				meta.NextCursor = &enc
			}
		}
	}
	if req.Params.Count != nil && *req.Params.Count {
		total, err := s.db.AdminCountPaymentsFiltered(ctx, params.State)
		if err != nil {
			s.logger.WarnContext(ctx, "支付流水计数失败，本次不返回 total", "err", err)
		} else {
			meta.Total = &total
		}
	}
	return gen.ListAdminPayments200JSONResponse{Data: data, Meta: meta}, nil
}

// ListAdminUnderpaidPayments 实现 GET /api/v1/admin/payments/underpaid。
//
// 🔴 判据是**订单口径**不是单笔口径（`累计实收 < 应收` 且订单仍未终结），
// 这一条完全在 SQL 里。handler 这一层要知道的是它的后果：
// 这个页面契约上写着「常驻的对账入口，不是异常处理页」，所以它的正常状态是**空的**。
// 按 `p.state = 'underpaid'` 过滤的写法会让它永远清不空 —— `payments` 近乎 append-only，
// 补足到账之后没有任何机制回头改第一行的 state。
//
// ⚠️ 未归属的钱（order_id IS NULL）**不在这个清单里**，它是另一个队列。
// 两者的人工动作完全不同：少付是「联系用户补差价或写销」，未归属是「这笔钱是谁的」。
func (s *Server) ListAdminUnderpaidPayments(ctx context.Context, req gen.ListAdminUnderpaidPaymentsRequestObject) (gen.ListAdminUnderpaidPaymentsResponseObject, error) {
	if _, err := s.adminReadAuth(ctx); err != nil {
		return nil, err
	}
	want, limitPlusOne := pageLimit(req.Params.Limit)
	params := dbgen.AdminListUnderpaidPaymentsPageParams{PageLimit: limitPlusOne}
	if req.Params.Cursor != nil && *req.Params.Cursor != "" {
		if cur, ok := decodePageCursor(*req.Params.Cursor); ok {
			params.CursorAt = tstz(cur.At)
			params.CursorID = &cur.ID
		} else {
			s.logger.WarnContext(ctx, "少付队列游标非法，按第一页处理",
				"request_id", middleware.RequestIDFrom(ctx), "cursor_len", len(*req.Params.Cursor))
		}
	}

	rows, err := s.db.AdminListUnderpaidPaymentsPage(ctx, params)
	if err != nil {
		return gen.ListAdminUnderpaidPayments500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取少付队列失败", err)}, nil
	}
	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	data := make([]gen.AdminPayment, 0, len(rows))
	for i := range rows {
		data = append(data, adminPaymentUnderpaidView(rows[i]))
	}
	meta := s.meta(ctx)
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		if last.ReceivedAt.Valid {
			if enc := encodePageCursor(last.ID, last.ReceivedAt.Time.UTC()); enc != "" {
				meta.NextCursor = &enc
			}
		}
	}
	if req.Params.Count != nil && *req.Params.Count {
		total, err := s.db.AdminCountUnderpaidPayments(ctx)
		if err != nil {
			s.logger.WarnContext(ctx, "少付队列计数失败，本次不返回 total", "err", err)
		} else {
			meta.Total = &total
		}
	}
	return gen.ListAdminUnderpaidPayments200JSONResponse{Data: data, Meta: meta}, nil
}

// ============================================================
// D13 · updateAdminPayment
// ============================================================

// adminPaymentPatchQuerier 是 D13 事务体的数据面。
type adminPaymentPatchQuerier interface {
	AdminGetPaymentForUpdate(ctx context.Context, paymentID int64) (dbgen.AdminGetPaymentForUpdateRow, error)
	AdminUpdatePaymentState(ctx context.Context, arg dbgen.AdminUpdatePaymentStateParams) (dbgen.AdminUpdatePaymentStateRow, error)
}

type adminPaymentPatchInput struct {
	PaymentID int64
	State     dbgen.PaymentState
	Reason    string
	Note      *string
	AdminID   int64
}

// adminPaymentSnapshot 是 D13 的审计快照。
//
// 🔴 **`before_state` 必须在里面。** `payments` 上没有 `updated_at`（0014 刻意，
// 表接近 append-only），也就是说这次人工改动在**行本身里不留任何痕迹** ——
// 改完之后谁也看不出这一行被人动过，`audit_logs` 是唯一的记录。
type adminPaymentSnapshot struct {
	ID          int64   `json:"id"`
	Provider    string  `json:"provider"`
	ExternalID  string  `json:"external_id"`
	EnteredBy   string  `json:"entered_by"`
	State       string  `json:"state"`
	OrderID     *int64  `json:"order_id"`
	TradeNo     *string `json:"trade_no"`
	AmountUsdt6 *int64  `json:"amount_usdt6"`
	Txid        *string `json:"txid"`
	// Note 只在 after 上出现：`AdminPaymentPatch.note` 在库里**无处可存**
	// （payments 没有备注列），所以它只能落在审计里。
	// **绝不塞进 `raw`** —— 那是链上原始 event / 网关原始 payload，是取证材料，
	// 往里掺人写的字会毁掉它「这条流水可复核」的性质。
	Note *string `json:"note,omitempty"`
}

func adminPaymentSnapshotOf(r dbgen.AdminGetPaymentForUpdateRow, state dbgen.PaymentState) adminPaymentSnapshot {
	return adminPaymentSnapshot{
		ID: r.ID, Provider: r.Provider, ExternalID: r.ExternalID, EnteredBy: r.EnteredBy,
		State: string(state), OrderID: r.OrderID, TradeNo: r.TradeNo,
		AmountUsdt6: r.AmountUsdt6, Txid: r.Txid,
	}
}

// adminUpdatePayment 是 D13 的事务体。
func adminUpdatePayment(ctx context.Context, q adminPaymentPatchQuerier, log *slog.Logger, in adminPaymentPatchInput) (gen.AdminPayment, audit.Entry, error) {
	var empty gen.AdminPayment

	before, err := q.AdminGetPaymentForUpdate(ctx, in.PaymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, audit.Entry{}, adminNotFound("支付流水不存在")
	}
	if err != nil {
		return empty, audit.Entry{}, adminInternal("读取支付流水失败", err)
	}

	after, err := q.AdminUpdatePaymentState(ctx, dbgen.AdminUpdatePaymentStateParams{
		PaymentID: in.PaymentID, State: in.State,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, audit.Entry{}, adminConflict("支付流水在本次操作期间消失了，已回滚")
	}
	if err != nil {
		return empty, audit.Entry{}, adminInternal("更新支付流水状态失败", err)
	}

	// 🔴 人工改 state **不会**推进订单、也不会开通任何权益。
	//    改成 paid 之后订单仍然停在原状态 —— 这一点必须响亮，否则操作者会以为
	//    「我把它标成 paid 了，用户应该能用了」，然后等着一个永远不会发生的开通。
	if in.State == dbgen.PaymentStatePaid && before.State != dbgen.PaymentStatePaid {
		log.ErrorContext(ctx, "人工把支付流水改成 paid，但这不会推进订单状态、也不会开通权益；要补单请走 D6（手工标记订单已支付）",
			"metric", "bp_admin_payment_state_forced", "payment_id", in.PaymentID,
			"trade_no", before.TradeNo, "from", string(before.State), "admin_id", in.AdminID)
	}

	beforeSnap := adminPaymentSnapshotOf(before, after.BeforeState)
	afterSnap := adminPaymentSnapshotOf(before, after.AfterState)
	afterSnap.Note = in.Note

	return adminPaymentPatchedView(before, after), audit.Entry{
		Action:     "D13.payment.update",
		TargetType: "payment",
		TargetID:   strconv.FormatInt(in.PaymentID, 10),
		Before:     beforeSnap,
		After:      afterSnap,
		Reason:     in.Reason,
	}, nil
}

// validAdminPaymentState 判断契约给的 state 是不是库里 `payment_state` 的合法值。
//
// 契约的 PaymentState 与库里的枚举**恰好逐值相同**（waiting / confirming /
// underpaid / paid / expired），所以这里是一张显式的表而不是直接转换：
// 直接 `dbgen.PaymentState(v)` 在契约将来加一个值时会把它原样送进 SQL，
// 得到的是一次 22P02 的 500，而不是一句「这个状态不认识」。
func validAdminPaymentState(v gen.PaymentState) (dbgen.PaymentState, bool) {
	switch v {
	case gen.PaymentStateWaiting:
		return dbgen.PaymentStateWaiting, true
	case gen.PaymentStateConfirming:
		return dbgen.PaymentStateConfirming, true
	case gen.PaymentStateUnderpaid:
		return dbgen.PaymentStateUnderpaid, true
	case gen.PaymentStatePaid:
		return dbgen.PaymentStatePaid, true
	case gen.PaymentStateExpired:
		return dbgen.PaymentStateExpired, true
	default:
		return "", false
	}
}

// UpdateAdminPayment 实现 PATCH /api/v1/admin/payments/{id}（D13：L2 必填原因）。
func (s *Server) UpdateAdminPayment(ctx context.Context, req gen.UpdateAdminPaymentRequestObject) (gen.UpdateAdminPaymentResponseObject, error) {
	actor, auth, err := s.adminAuditActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.UpdateAdminPayment500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "无法组装审计记录", err)}, nil
	}
	if req.Body == nil {
		return s.paymentPatchErr(ctx, adminUnprocessable("请求体缺失")), nil
	}
	body := *req.Body

	if g := guardAdminReason(body.Reason); g != nil {
		s.logAdminGuard(ctx, "D13.payment.update", strconv.FormatInt(req.Id, 10), auth, g)
		return s.paymentPatchErr(ctx, g.opError()), nil
	}

	// 🔴 `state` 缺失时**拒绝**，不返回一个什么都没改的 200。
	//    `AdminPaymentPatch` 只有 state / note / reason 三个字段，而 `payments`
	//    **没有备注列** —— note 无处可存（它只进审计的 after 快照）。
	//    一个只带 note 的请求如果回 200 + 原样的 AdminPayment，操作者会认为
	//    这条备注已经挂在这笔流水上了，而下一个打开这一页的人什么也看不到。
	//    这正是「绝不假装成功」要挡的形状。缺口已登记。
	if body.State == nil {
		return s.paymentPatchErr(ctx, adminUnprocessable(
			"本端点唯一能写入的字段是 state；payments 表没有备注列，note 只会进审计日志，不会挂在这笔流水上",
			detail("state", "必填"))), nil
	}
	state, ok := validAdminPaymentState(*body.State)
	if !ok {
		return s.paymentPatchErr(ctx, adminUnprocessable("不认识的支付状态 "+string(*body.State),
			detail("state", "只接受 waiting / confirming / underpaid / paid / expired"))), nil
	}

	var out gen.AdminPayment
	in := adminPaymentPatchInput{
		PaymentID: req.Id, State: state, Reason: body.Reason, Note: body.Note, AdminID: auth.AdminID,
	}
	err = audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		view, entry, err := adminUpdatePayment(ctx, q, s.logger, in)
		if err != nil {
			return audit.Entry{}, err
		}
		out = view
		return entry, nil
	})
	if err != nil {
		return s.paymentPatchErr(ctx, asAdminOpError(err)), nil
	}
	return gen.UpdateAdminPayment200JSONResponse{Data: out, Meta: s.meta(ctx)}, nil
}

// paymentPatchErr 映射响应。
//
// ⚠️ 本 operation 在契约上**没有 409**，而事务体里确实存在一个 409 的成因
// （流水在读与写之间消失）。把它降级成 422 会把一次并发冲突说成「你的参数不对」，
// 所以这里让它落到 500 —— 它本来就是一个不该发生的状态，而 500 会被计入错误率。
// 缺口已登记：updateAdminPayment 需要一个 409。
func (s *Server) paymentPatchErr(ctx context.Context, e *adminOpError) gen.UpdateAdminPaymentResponseObject {
	switch e.Status {
	case http.StatusForbidden:
		return gen.UpdateAdminPayment403JSONResponse{ErrForbiddenJSONResponse: s.forbidden(ctx, e.Code, e.Message)}
	case http.StatusNotFound:
		return gen.UpdateAdminPayment404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, e.Message)}
	case http.StatusUnprocessableEntity:
		return gen.UpdateAdminPayment422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, e.Message, e.Details...)}
	default:
		return gen.UpdateAdminPayment500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "D13 编辑支付记录失败", e)}
	}
}
