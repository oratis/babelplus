package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/httpx"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 订单与收款（createOrder · getOrder · listOrders · cancelOrder ·
// payOrder · getOrderPayment · recheckOrderPayment · handlePaymentNotify）。
//
// 这是全仓库唯一会动钱的路径，所以下面六条纪律贯穿全文件。每一条都配了
// 「不这么做会发生什么」，因为它们全部属于**不会编译失败、不会报错、只会算错**的那一类。
//
//  1. 🔴 **1.20× 地板断言在写 orders 之前跑，失败一律拒绝下单**（定价修订 A7）。
//     不是记日志放行 —— 一条破地板的订单是一笔确定的、按当期成本模型算得出来的亏损，
//     而「记了日志的亏损」与「没记日志的亏损」在现金上完全一样。
//     断言的分子必须扣掉本单的返佣计提：C6 把返佣改成一次性定额之后金额在下单时就已知，
//     这是两条改动互相咬合的地方（旧口径下返佣落在订单成交之后，硬规则被写在
//     它要防的东西不会经过的地方）。
//
//  2. 🔴 **回调不可信 —— 收到回调后必须反向查单**（api-contract §8.1，先例是 NewAPI 的
//     易支付回调伪造漏洞）。`handlePaymentNotify` 里**没有一处**读取回调声称的金额或状态；
//     权威金额只来自链上（`ChainScanner`）。`recheckOrderPayment` 是同一段逻辑的用户侧入口。
//
//  3. 🔴 **归属只看地址，不看金额**（ADR 0012 §5 / §17.2）。一单一址、永不复用，
//     由 `pay_addresses.assigned_order_id` 的 UNIQUE 与 `orders_pay_addr_uk` 双向强制。
//     契约 payOrder 描述里那三条「金额末位是订单识别码 / `+0.0001` 递增」是被 §5.4 推翻的原文，
//     本文件与用户可见文案里都不得出现金额匹配话术。
//
//  4. 🔴 **状态迁移只走 `TransitionOrderStatus`（DB 层 CAS）**，不用 orders.sql 的
//     `UpdateOrderStatus`（它的 WHERE 没有 from-status）。扫链与 recheck 会并发处理同一笔到账，
//     无 CAS 的写法让 `paying → paid` 跑两次 —— 而开通挂在这次迁移上。
//     每一次迁移**同事务**写一条 `order_transitions`：状态机没有触发器兜底，
//     漏写不会报错，而「我明明付了」的工单只能靠这张表回答。
//
//  5. 🔴 **入账只有一个入口 `processDeposit`**（ADR 0012 §8.4 硬约束 1）。
//     recheck 与 webhook 两条路径调的是同一个函数；两条路径一旦漂移，漂移的那天就是出事的那天。
//
//  6. **不连任何第三方 endpoint。** ADR 0012 的状态是「提案，未批准」，
//     链上访问走 task.go 已建好的 `ChainScanner` 接口，默认实现返回「未配置」。
//     未配置 → 优雅退出并如实告诉用户「还没查到」，**不返回 5xx**：
//     「ESP/RPC 还没接」是计划内的状态，把它变成故障告警只会训练所有人忽略告警。
//
// ⚠️ 本轮**没有实现**的一步，已在 markOrderPaid 的注释里逐字登记：`paid → completed` 的
//    权益开通（写配额 / 写到期 / bump user_rev）。它需要一条本轮不存在的查询
//    （首次开通的 reset_at 计算），详见那里。

// ============================================================
// 常量
// ============================================================

const (
	// 支付窗口 30 分钟（契约 Order.expires_at 的 description、ADR 0012 §15.1 的汇率 TTL）。
	// 汇率锁定与支付窗口是**同一个 TTL** —— 两个数字会各自漂移，而漂移的后果是
	// 「报价还在页面上，但订单已经过期了」。
	orderPayWindow = 30 * time.Minute

	// 地址自动监听时长 = expires_at + 7 天（ADR 0012 §11.1）。
	// ⚠️ 这是**自动扫描的时长，不是认账的时长**：一址一单永不复用之下，
	//    第 8 天、第 800 天到账的钱归属仍然唯一确定。契约里的「≥ 24 小时」是下限不是上限。
	addressWatchAfterExpiry = 7 * 24 * time.Hour

	// 链与网关标识。第一阶段只有 TRC20 一条（ADR 0012 §1）。
	payChainTron   = "tron"
	gatewayUsdtTrc = "usdt_trc20"

	// recheck 冷却窗口（ADR 0012 §10.4）。
	// 🔴 窗口内的重复 recheck **返回上一次的结果 + 200**，不是 429 ——
	//    原文：「给一个害怕的人回 429，是这个按钮所有可能行为里最差的一种」。
	//    20 秒是拍的，须按 TronGrid 实际额度调整（该节登记为未决项）。
	recheckCooldown = 20 * time.Second

	// 超额多付的忽略阈值：0.01 USDT（= 报价的取整粒度，折人民币约 ¥0.072）。
	// ADR 0012 §6.2 的原话是「不是不可表示，只是不值得为它开一条路径」——
	// 这个量级的差额来自我们自己的向上取整，它本来就该落在我们这边。
	usdt6OverpayIgnore = 10_000

	// C6 的默认返佣费率（基点）。邀请人自己有 commission_rate_bps 时用他的，
	// 这个值只在他没有时兜底。第一阶段 10%（定价修订 §C6）。
	defaultCommissionRateBps = 1000
)

// 支付相关的运行时配置：**默认值在这里，真值在 settings 里**（ADR 0012 §9.2）。
//
// 为什么两处都要有：settings 是空表时（第一次部署、或者有人删了那一行）不能让收银台整个不可用，
// 但也不能让默认值成为事实上的唯一配置 —— 所以取默认值时**必须记一条 Warn**，
// 让「配置没生效」这件事在日志里可见，而不是靠有人想起来去查表。
const (
	settingsKeyPayment = "payment.providers"
	settingsKeyFX      = "payment.fx"

	defaultWriteoffUsdt6 = 2_000_000 // A 档自动写销上界 2.0 USDT（ADR 0012 §6.1）
	defaultReviewUsdt6   = 5_000_000 // B 档人工队列上界 5.0 USDT
	defaultAddrLowWater  = 8         // 可用地址低水位告警线
	// TRON 的最终性是「固化」而不是 N 个确认，服务端的实际判据是固化标志；
	// 这个 19 **只用于前端展示进度**（ADR 0012 §10.5）。把它当判据写进入账逻辑是错的。
	defaultConfirmationsRequired = 19

	// 🔴 汇率：**本轮没有汇率源**。ADR 0012 §15.2 要求双源 + 偏差熔断，那需要外部调用，
	//    而本轮的纪律是不连任何第三方 endpoint。所以这里落一个可配置的静态值，
	//    默认取定价修订 §4.1 的 FX = 7.15（该文档自己标注「待核实，且已知可能高估 6.1%」）。
	//    这个数同时是地板断言的分母之一 —— 汇率错了地板就是错的，
	//    所以上线前接汇率源是硬前置，已在 notes 与 §15.2 各记一处。
	defaultCnyPerUsdtE4 = 71_500 // 7.1500，基数 1e4

	// 报价缓冲 1%（ADR 0012 §15.2「明记不藏」，对应 revenue:fx_buffer 科目）。
	defaultFxBufferBps = 100
)

// 成本模型（定价修订 §4.3），单位 1e-6 USD。
//
// ⚠️ **不要把这张表落库。** 它是一份会随实测修订的假设；落库会让「改一个假设」
//
//	变成一次 migration，而每一次 migration 都会让人倾向于不改。
//
// 月度总成本(档位, 周期) = Q × c + f + s / n，其中
//
//	c = u × k = $0.11 × 1.10 = $0.121 / 计费 GiB   （u 高置信：官方目录价；k 是设定值，无实测）
//	f = $2.00 / 人 / 月                              （满编演示值；真值是 F_当期 / N_当期，上限 $3.2916）
//	s = $1.47 / 笔                                   （USDT 路径一手链上实测的上界）
//
// 这里按 `plans.transfer_enable` 现算而不是查三档常量表，是因为常量表在遇到
// 一个不在表里的新档位时只有两种行为：崩溃，或者**跳过断言** —— 而后者是静默的，
// 正好发生在「运营新建了一个套餐」这个最需要断言的时刻。
const (
	costPerGiBMicroUSD  = 121_000
	costFixedMicroUSD   = 2_000_000
	costChannelMicroUSD = 1_470_000
	bytesPerGiB         = int64(1) << 30

	// 加油包不摊 f、也不单独摊一笔 s：它的成本口径是「¥1.20/GiB 对 c 的名义覆盖 1.3870×，
	// 摊回 ¥200 充值触发的那一次链上归集（$1.47 / $27.972 = 5.256%）后 1.3142×」。
	// 于是加油包的等效成本 = Q×c / (1 − 5.256%)。取 526 bps（5.26%）而不是 525.6：
	// 向上取整让成本更高、断言更严，舍入误差落在我们这边。
	packChannelShareBps = 526

	// 1.20× 地板（定价修订 §3.2）。写成 120/100 的整数比，不引入浮点 ——
	// 一个用 float 判定的地板会在某一格上因为最后一位而给出与手算不同的结论，
	// 而那一格恰恰是最薄的那一格。
	priceFloorNum = 120
	priceFloorDen = 100
)

// 收银台文案（ADR 0012 §6.4）。
//
// 🔴 这段话**推翻**了 user-journey §7 卡点 5 的原文案与契约里 `PaymentCheckout.note`
//
//	的 description（那两处解释的是「四位小数尾数是订单识别码」）。尾数机制随 §5.4 一起删掉了，
//	照原文案填写金额对 Binance 式提币是**反的** —— 按它填必然 underpaid。
//	最后一句是 §11 的产品化表达，也是本裁决最应该被用户看见的一句。
const paymentCheckoutNote = "若你从交易所提币，请在提币金额里填「上面这个数 + 你的提币手续费」" +
	"—— 手续费是从你填的金额里扣的，不是另外加收（Binance 的 TRC20 提币费当前为 1.5 USDT，" +
	"各交易所不同，以你的提币页显示为准）。这个地址永远认账：无论多久之后到账、无论金额多少，" +
	"都会自动记到这张订单或你的账户余额上。"

// ============================================================
// 支付回调验签：可注入的外部依赖
// ============================================================

// ErrPaymentNotifyUnverified 表示这次回调没有通过验签，或者根本没有可用的验签实现。
var ErrPaymentNotifyUnverified = errors.New("支付回调验签失败")

// PaymentNotifyVerifier 验证 `POST /api/v1/payment/notify/{provider}` 的真伪。
//
// 抽成接口而不是写死某个网关的签名算法，理由与 task.go 的 ChainScanner 相同：
// ADR 0012 §1 裁决**第一阶段自扫链、不接任何支付网关**，所以今天没有任何 provider 有密钥。
//
// 🔴 默认实现对**每一次**回调返回失败（→ 401）。这是 fail-closed，而且是唯一正确的默认：
// 没有配置任何网关 = 没有任何回调可能是真的。反过来（默认放行）意味着
// 任何人 POST 一个 JSON 就能触发我们的入账路径 —— 那正是 NewAPI 那次漏洞的形状。
type PaymentNotifyVerifier interface {
	// Name 写进日志与 webhook_events.gateway。
	Name() string
	// Configured 为 false 时 handler 一律 401，不解析载荷。
	Configured() bool
	// Verify 校验签名。返回的 eventID 写进 webhook_events.event_id（重放防护的键）；
	// 网关不提供 event id 时按 0006 的注释用 `trade_no || ':' || status`。
	Verify(ctx context.Context, provider string, headers http.Header, body []byte) (eventID string, err error)
}

type unconfiguredNotifyVerifier struct{}

func (unconfiguredNotifyVerifier) Name() string     { return "unconfigured" }
func (unconfiguredNotifyVerifier) Configured() bool { return false }
func (unconfiguredNotifyVerifier) Verify(context.Context, string, http.Header, []byte) (string, error) {
	return "", ErrPaymentNotifyUnverified
}

// 默认实现。与 task.go 的两个默认依赖同一条纪律：**只读，生产代码里不允许重新赋值** ——
// 包级可变状态在并发测试里是共享的。单测的注入方式是把自己的实现直接传给自由函数。
var defaultNotifyVerifier PaymentNotifyVerifier = unconfiguredNotifyVerifier{}

func (s *Server) notifyVerifier() PaymentNotifyVerifier { return defaultNotifyVerifier }

// ============================================================
// 数据面：窄接口
// ============================================================

type ledgerQuerier interface {
	GetLedgerAccountByCode(ctx context.Context, code string) (dbgen.LedgerAccount, error)
	CreateLedgerEntry(ctx context.Context, arg dbgen.CreateLedgerEntryParams) (dbgen.LedgerEntry, error)
	CreateLedgerLine(ctx context.Context, arg dbgen.CreateLedgerLineParams) (dbgen.LedgerLine, error)
}

type settingsReader interface {
	GetPaymentSettings(ctx context.Context, keys []string) ([]dbgen.GetPaymentSettingsRow, error)
}

type createOrderWriter interface {
	ledgerQuerier
	CreateOrder(ctx context.Context, arg dbgen.CreateOrderParams) (dbgen.Order, error)
	InsertOrderTransition(ctx context.Context, arg dbgen.InsertOrderTransitionParams) (dbgen.OrderTransition, error)
	IncrementCouponUse(ctx context.Context, id int64) (dbgen.IncrementCouponUseRow, error)
	SpendWalletBalance(ctx context.Context, arg dbgen.SpendWalletBalanceParams) (dbgen.SpendWalletBalanceRow, error)
}

// depositQuerier 是 `processDeposit` 的全部数据面，顺序即 ADR 0012 §8.4 的五个分支。
type depositQuerier interface {
	ledgerQuerier
	InsertPaymentIfNew(ctx context.Context, arg dbgen.InsertPaymentIfNewParams) (dbgen.Payment, error)
	GetPaymentByExternalID(ctx context.Context, arg dbgen.GetPaymentByExternalIDParams) (dbgen.Payment, error)
	AppendScannerToPaymentEntry(ctx context.Context, arg dbgen.AppendScannerToPaymentEntryParams) (dbgen.AppendScannerToPaymentEntryRow, error)
	GetOrderByPayAddressForUpdate(ctx context.Context, payAddress string) (dbgen.GetOrderByPayAddressForUpdateRow, error)
	GetPayAddressByAddress(ctx context.Context, arg dbgen.GetPayAddressByAddressParams) (dbgen.GetPayAddressByAddressRow, error)
	AttributePayment(ctx context.Context, arg dbgen.AttributePaymentParams) (dbgen.Payment, error)
	SumAddressReceipts(ctx context.Context, payAddress string) (dbgen.SumAddressReceiptsRow, error)
	TransitionOrderStatus(ctx context.Context, arg dbgen.TransitionOrderStatusParams) (dbgen.TransitionOrderStatusRow, error)
	InsertOrderTransition(ctx context.Context, arg dbgen.InsertOrderTransitionParams) (dbgen.OrderTransition, error)
	RecordOrderPayerAddress(ctx context.Context, arg dbgen.RecordOrderPayerAddressParams) error
	RecordOrderPayment(ctx context.Context, arg dbgen.RecordOrderPaymentParams) (dbgen.RecordOrderPaymentRow, error)
	UpsertWalletBalance(ctx context.Context, arg dbgen.UpsertWalletBalanceParams) (dbgen.WalletBalance, error)
}

// ============================================================
// 响应构造
// ============================================================

// orderNotFound 是订单相关端点的 404。
//
// 🔴 **不区分「不存在」与「不是你的」。** trade_no 是路径参数、对外可见、可枚举，
// 两种情况给不同的答复等于把「这个单号存在吗」做成一个免登录可用的探测器。
// 所有按 trade_no 定位的查询都同时按 user_id 过滤（`GetUserOrder` / `GetOrderCheckout`），
// 0 行统一落到这里。
func (s *Server) orderNotFound(ctx context.Context) gen.ErrNotFoundJSONResponse {
	return gen.ErrNotFoundJSONResponse{
		Body:    s.envelope(ctx, gen.RESOURCENOTFOUND, "订单不存在"),
		Headers: gen.ErrNotFoundResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}
}

// orderStatusView 把 DB 的 order_status（14 值）映射成契约的 OrderStatus（6 值）。
//
// 🔴 必须是一张显式的表，不能 `gen.OrderStatus(row.Status)`。
// 契约的枚举缺 `paying` / `underpaid` / `paid`，多一个 DB 里不存在的 `processing`
// （ADR 0013 §4.7 登记的四处不一致之一）。直接 fmt 出去的后果是前端收到一个
// 它的类型里没有的字符串 —— JSON 没有类型检查，现象是订单卡片整块空白，不是报错。
//
// 三处并档各有理由：
//   - `paying` / `underpaid` / `paid` → "processing"：契约里唯一表达「钱在路上」的值。
//     用户能看到的是订单页的状态标签，而收银台的精细状态由 PaymentCheckout.state 给
//     （那个枚举是全的，含 underpaid）。
//   - `failed` → "cancelled"：终态、未收钱、可重下单，与 cancelled 的用户动作完全一致。
//   - `refunding` / `partially_refunded` / `chargeback*` → "refunded"：钱在往回走。
//
// ⚠️ 并档是有损的。修契约（补齐 DB 的 14 个值）是上线前的独立动作，
//
//	在那之前**不要**在业务判断里读这个映射后的值 —— 判断一律读 DB 的 status。
func orderStatusView(st dbgen.OrderStatus) gen.OrderStatus {
	switch st {
	case dbgen.OrderStatusPending:
		return gen.OrderStatusPending
	case dbgen.OrderStatusPaying, dbgen.OrderStatusUnderpaid, dbgen.OrderStatusPaid:
		return gen.OrderStatusProcessing
	case dbgen.OrderStatusCompleted:
		return gen.OrderStatusCompleted
	case dbgen.OrderStatusCancelled, dbgen.OrderStatusFailed:
		return gen.OrderStatusCancelled
	case dbgen.OrderStatusExpired:
		return gen.OrderStatusExpired
	case dbgen.OrderStatusRefunding, dbgen.OrderStatusRefunded,
		dbgen.OrderStatusPartiallyRefunded, dbgen.OrderStatusChargeback,
		dbgen.OrderStatusChargebackWon, dbgen.OrderStatusChargebackLost:
		return gen.OrderStatusRefunded
	default:
		// 枚举被加了值而这张表没跟上。回 processing 而不是空串：
		// 空串在前端会落到「未知状态」的兜底分支，而 processing 至少不会让用户
		// 以为订单被取消了。同时这条 default 是本函数存在的意义 —— 它保证
		// **任何**新增的 DB 状态都不会以原文泄漏到契约外。
		return gen.OrderStatusProcessing
	}
}

// orderTypeView 把 DB 的 order_type（6 值）映射成契约的 OrderType（4 值）。
// `reset_pack` / `wallet_topup` 在契约里没有对应值（ADR 0013 §4.7）；
// 两者都不经 POST /orders 产生，所以在用户面订单列表里它们只可能来自别的写入路径，
// 并到 traffic_pack 是「一次性购买」这个最接近的语义。
func orderTypeView(t dbgen.OrderType) gen.OrderType {
	switch t {
	case dbgen.OrderTypeNew:
		return gen.OrderTypeNew
	case dbgen.OrderTypeRenew:
		return gen.OrderTypeRenew
	case dbgen.OrderTypeUpgrade:
		return gen.OrderTypeUpgrade
	default:
		return gen.OrderTypeTrafficPack
	}
}

// orderView 组装契约的 `Order`。金额映射（单位全部是分）：
//
//	total_amount ← amount_gross      discount_amount ← amount_discount
//	surplus_amount ← surplus_amount  balance_amount  ← amount_balance
//	payable_amount ← amount_due      rate_locked_at  ← fx_locked_at
func orderView(r dbgen.GetUserOrderRow) gen.Order {
	o := gen.Order{
		TradeNo:        r.TradeNo,
		Type:           orderTypeView(r.Type),
		Status:         orderStatusView(r.Status),
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
	return o
}

// orderListView 是列表行的映射。它与 orderView 共用同一批字段名，
// 但**不能**合并成一个函数：两条查询的投影不同（列表不返支付通道与服务区间），
// 硬合并只能靠把列表行填进详情结构体，而那会让「列表里少了一个字段」
// 变成「详情里那个字段恒为零值」。
func orderListView(r dbgen.ListUserOrdersPageRow) gen.Order {
	o := gen.Order{
		TradeNo:        r.TradeNo,
		Type:           orderTypeView(r.Type),
		Status:         orderStatusView(r.Status),
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
	return o
}

// cancelledOrderView 把取消返回的那一行拼成与 getOrder 同形的响应体。
// 复用取消语句自带的 plan_name，省掉一次回查（那次回查在并发下还会读到别的状态）。
func cancelledOrderView(r dbgen.CancelUserPendingOrderRow) gen.Order {
	o := gen.Order{
		TradeNo:        r.TradeNo,
		Type:           orderTypeView(r.Type),
		Status:         orderStatusView(r.Status),
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
	return o
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

// ============================================================
// 支付配置
// ============================================================

type paymentSettings struct {
	WriteoffUsdt6         int64
	ReviewUsdt6           int64
	AddrLowWater          int64
	ConfirmationsRequired int32
	CnyPerUsdtE4          int64
	FxBufferBps           int64
}

func defaultPaymentSettings() paymentSettings {
	return paymentSettings{
		WriteoffUsdt6:         defaultWriteoffUsdt6,
		ReviewUsdt6:           defaultReviewUsdt6,
		AddrLowWater:          defaultAddrLowWater,
		ConfirmationsRequired: defaultConfirmationsRequired,
		CnyPerUsdtE4:          defaultCnyPerUsdtE4,
		FxBufferBps:           defaultFxBufferBps,
	}
}

// loadPaymentSettings 读 settings 里的两把钥匙，读不到就用默认值并记 Warn。
//
// 🔴 **读失败不让端点失败。** 收银台在配置表出问题时仍然必须能开 ——
// 用默认阈值收一笔钱，比让一个已经准备转账的用户看到 500 要好得多。
// 代价是「配置没生效」会被静默吃掉，所以两条路径（查询报错 / 行不存在）各记一条 Warn。
func loadPaymentSettings(ctx context.Context, q settingsReader, log *slog.Logger) paymentSettings {
	set := defaultPaymentSettings()
	rows, err := q.GetPaymentSettings(ctx, []string{settingsKeyPayment, settingsKeyFX})
	if err != nil {
		log.WarnContext(ctx, "读取支付配置失败，本次使用内置默认值（阈值与汇率可能与运营预期不一致）", "err", err)
		return set
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Key] = true
		switch r.Key {
		case settingsKeyPayment:
			var v struct {
				ConfirmPolicy *int32 `json:"confirm_policy"`
				Writeoff      *int64 `json:"writeoff_usdt6"`
				Review        *int64 `json:"review_usdt6"`
				AddrLowWater  *int64 `json:"addr_low_water"`
			}
			if err := json.Unmarshal(r.Value, &v); err != nil {
				log.WarnContext(ctx, "支付配置 JSON 解析失败，该键回落默认值", "key", r.Key, "err", err)
				continue
			}
			if v.ConfirmPolicy != nil {
				set.ConfirmationsRequired = *v.ConfirmPolicy
			}
			if v.Writeoff != nil {
				set.WriteoffUsdt6 = *v.Writeoff
			}
			if v.Review != nil {
				set.ReviewUsdt6 = *v.Review
			}
			if v.AddrLowWater != nil {
				set.AddrLowWater = *v.AddrLowWater
			}
		case settingsKeyFX:
			var v struct {
				CnyPerUsdtE4 *int64 `json:"cny_per_usdt_e4"`
				BufferBps    *int64 `json:"buffer_bps"`
			}
			if err := json.Unmarshal(r.Value, &v); err != nil {
				log.WarnContext(ctx, "汇率配置 JSON 解析失败，该键回落默认值", "key", r.Key, "err", err)
				continue
			}
			if v.CnyPerUsdtE4 != nil && *v.CnyPerUsdtE4 > 0 {
				set.CnyPerUsdtE4 = *v.CnyPerUsdtE4
			}
			if v.BufferBps != nil && *v.BufferBps >= 0 {
				set.FxBufferBps = *v.BufferBps
			}
		}
	}
	// 🔴 写销阈值必须 ≤ 人工阈值，否则 B 档（人工队列）整档消失：
	//    所有少付都会走进「自动写销」，而那是**我们直接放弃的钱**。
	//    配置写反不会报错，只会让钱少一点点、每次少一点点。
	if set.WriteoffUsdt6 > set.ReviewUsdt6 {
		log.ErrorContext(ctx, "支付配置非法：写销阈值大于人工复核阈值，B 档人工队列会整档消失，本次回落默认值",
			"writeoff_usdt6", set.WriteoffUsdt6, "review_usdt6", set.ReviewUsdt6)
		set.WriteoffUsdt6 = defaultWriteoffUsdt6
		set.ReviewUsdt6 = defaultReviewUsdt6
	}
	if !seen[settingsKeyFX] {
		log.WarnContext(ctx, "settings 里没有汇率配置，使用内置默认汇率（上线前必须接汇率源，ADR 0012 §15.2）",
			"cny_per_usdt_e4", set.CnyPerUsdtE4)
	}
	if !seen[settingsKeyPayment] {
		log.WarnContext(ctx, "settings 里没有支付配置，使用内置默认阈值", "key", settingsKeyPayment)
	}
	return set
}

// ============================================================
// 金额：报价、汇率、整数换算
// ============================================================

// numericFromE4 把「基数 1e4 的定点整数」写成 numeric。
//
// ⚠️ `orders.fx_usdt_per_cny` 这个列名与它承载的值**方向相反**：列名写的是 usdt/cny，
//
//	落进去的是「1 USDT 折多少 CNY」（≈7.15，与契约字段 `cny_per_usdt_e4` 同口径）。
//	写反了报价差 51 倍。列名是 0006 留下的错误，改名要动生成物与既有查询，不在本轮范围。
func numericFromE4(e4 int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(e4), Exp: -4, Valid: true}
}

// numericToE4 把 numeric 读回基数 1e4 的整数。ok 为 false 表示这一列是 NULL 或不可表示。
//
// 用 big.Int 逐位缩放而不是走 float64：这一列是汇率，
// 而一个经过 float 的汇率会在某些值上把报价差出最后一分钱 —— 那一分钱会变成一次 underpaid。
func numericToE4(n pgtype.Numeric) (int64, bool) {
	if !n.Valid || n.NaN || n.Int == nil {
		return 0, false
	}
	// 目标：value × 1e4 = Int × 10^Exp × 1e4
	shift := int(n.Exp) + 4
	v := new(big.Int).Set(n.Int)
	if shift >= 0 {
		v.Mul(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(shift)), nil))
	} else {
		v.Quo(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-shift)), nil))
	}
	if !v.IsInt64() {
		return 0, false
	}
	return v.Int64(), true
}

// quoteUSDT6 按 ADR 0012 §5.3 算应收链上金额（1e-6 USDT）。
//
//	amount_usdt6 = ceil(amount_due_cents × 1e6 × (1 + fx_buffer) / (cny_per_usdt_e4 × 100))
//	amount_usdt6 = ceil(amount_usdt6 / 10000) × 10000      -- 取整到 0.01 USDT
//
// 两处**一律 ceil**：舍入误差落在我们这边比落在用户那边更容易解释
// （取整到 0.01 USDT 的最大多收 ≈ ¥0.071）。反过来向下取整，
// 用户会按我们报的数转账、然后被判成少付 —— 一次由我们造成、却由用户承担的 underpaid。
//
// 🔴 **尾数不承载任何识别功能。** 归属只看地址（§5.4 删除了金额匹配机制）。
func quoteUSDT6(amountDueCents, cnyPerUsdtE4, bufferBps int64) (int64, error) {
	if amountDueCents <= 0 {
		return 0, errors.New("应付金额必须为正")
	}
	if cnyPerUsdtE4 <= 0 {
		return 0, errors.New("汇率未配置")
	}
	// ⚠️ ADR §5.3 写的是 `cents × 1e6 × (1+buffer) / (cny_per_usdt_e4 × 100)`，
	//    这条式子**漏了汇率自己的 1e4 定点基数** —— 照它算出来的报价小 10000 倍
	//    （¥100 的订单会报出 0.0014 USDT）。这里补回来，量纲逐项对齐：
	//
	//	usdt6 = (cents / 100) ÷ (e4 / 1e4) × 1e6 × (1 + bps/1e4)
	//	      = cents × 1e10 × (10000 + bps) ÷ (e4 × 100 × 10000)
	//
	// 全程 big.Int：分子在 ¥10,000 的订单上就是 1e19 量级，已经越过 int64 上界，
	// 而溢出在这里的表现不是报错，是**报价变成负数**。
	num := new(big.Int).SetInt64(amountDueCents)
	num.Mul(num, new(big.Int).Exp(big.NewInt(10), big.NewInt(10), nil))
	num.Mul(num, big.NewInt(10_000+bufferBps))
	den := new(big.Int).SetInt64(cnyPerUsdtE4)
	den.Mul(den, big.NewInt(100))    // 分 → 元
	den.Mul(den, big.NewInt(10_000)) // 抵掉 bufferBps 的 1e4 基数

	q := ceilDivBig(num, den)
	// 取整到 0.01 USDT = 10000 个 1e-6 单位。
	q = ceilDivBig(q, big.NewInt(10_000))
	q.Mul(q, big.NewInt(10_000))
	if !q.IsInt64() {
		return 0, errors.New("报价溢出")
	}
	return q.Int64(), nil
}

func ceilDivBig(a, b *big.Int) *big.Int {
	q, r := new(big.Int).QuoRem(a, b, new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// usdt6ToCents 按锁定汇率把链上金额折成人民币分（floor）。
//
// floor 而不是 round：这个数写进 `payments.amount_cny_cents`，而它会被用来给用户记余额。
// 向上取整等于凭空多给，且每一笔都多给一点点 —— 一个不会有人发现的漏。
func usdt6ToCents(amountUsdt6, cnyPerUsdtE4 int64) int64 {
	if amountUsdt6 <= 0 || cnyPerUsdtE4 <= 0 {
		return 0
	}
	v := new(big.Int).SetInt64(amountUsdt6)
	v.Mul(v, big.NewInt(cnyPerUsdtE4))
	v.Mul(v, big.NewInt(100))              // CNY → 分
	v.Quo(v, big.NewInt(1_000_000*10_000)) // 抵掉 usdt6 的 1e6 与汇率的 1e4
	if !v.IsInt64() {
		return 0
	}
	return v.Int64()
}

// usdt6Display 把 1e-6 USDT 渲染成两位小数字符串（ADR 0012 §5.3）。
//
// 契约明写 `amount_display` 是**字符串不是数值** —— 不给浮点留口子。
// 报价已经取整到 0.01 USDT，所以两位小数是无损的。
func usdt6Display(v int64) string {
	neg := ""
	if v < 0 {
		neg, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d", neg, v/1_000_000, (v%1_000_000)/10_000)
}

// periodMonths 给出一个周期折算多少个月。
//
// 这张表是地板断言分母的一半，DB 里没有它（order_period 只是个枚举）。
// `onetime` 返回 0 表示「月数无定义」：**调用方必须据此拒绝**，
// 不许当成 1 个月接着算 —— 那会把一个不限时套餐按月付成本去过地板，结论必然是「通过」。
func periodMonths(p dbgen.OrderPeriod) int64 {
	switch p {
	case dbgen.OrderPeriodMonthly:
		return 1
	case dbgen.OrderPeriodQuarterly:
		return 3
	case dbgen.OrderPeriodHalfYearly:
		return 6
	case dbgen.OrderPeriodYearly:
		return 12
	default:
		return 0
	}
}

// ============================================================
// 🔴 1.20× 地板断言（定价修订 A7）
// ============================================================

// floorCheck 是一次地板断言的输入。字段拆得细，是为了断言失败时的日志能直接回答
// 「哪一项把它压下去的」—— 一条只说「破地板」的告警，值班的人只能去猜。
type floorCheck struct {
	PlanKind            string
	Period              dbgen.OrderPeriod
	TransferEnableBytes int64

	// NetCents = amount_gross − amount_discount − accrual_cents。见下面 assertPriceFloor 的推导。
	NetCents int64

	// 有效月数写成分数 EffMonthsNum / EffMonthsDen。
	// 整单（new / renew / traffic_pack）：Num = n，Den = 1。
	// 升级单：Num = n × D_left，Den = D_total —— 升级买的是「同一个周期档的剩余那一段」。
	EffMonthsNum int64
	EffMonthsDen int64

	CnyPerUsdtE4 int64
	AccrualCents int64 // 只为日志，判定用的是已经扣过它的 NetCents
}

var errPriceFloorViolated = errors.New("订单未通过 1.20× 成本覆盖地板")

// monthlyCostMicroUSDTimesN 返回 n × 月度总成本（1e-6 USD），保持精确不做除法。
//
//	n × (Q×c + f + s/n) = n × (Q×c + f) + s
//
// 把 s 留在分子里而不是先算 s/n：n=12 时 s/n = $0.1225，整数微美元下截断掉的部分
// 乘回 12 个月正好是我们少算的成本 —— 少算成本 = 地板变松，方向是错的。
func monthlyCostMicroUSDTimesN(transferEnableBytes, n int64) *big.Int {
	q := gibCostMicroUSD(transferEnableBytes)
	q.Add(q, big.NewInt(costFixedMicroUSD))
	q.Mul(q, big.NewInt(n))
	q.Add(q, big.NewInt(costChannelMicroUSD))
	return q
}

// gibCostMicroUSD = ceil(bytes / 1 GiB × c)，单位 1e-6 USD。
// ceil 而不是 floor：截断出来的那点配额也是真的要付出口费的，
// 而成本算小了会让一个本该被拒的订单通过。
func gibCostMicroUSD(transferEnableBytes int64) *big.Int {
	if transferEnableBytes <= 0 {
		return big.NewInt(0)
	}
	v := new(big.Int).SetInt64(transferEnableBytes)
	v.Mul(v, big.NewInt(costPerGiBMicroUSD))
	return ceilDivBig(v, big.NewInt(bytesPerGiB))
}

// packCostMicroUSD 是加油包的等效成本（1e-6 USD）：Q×c / (1 − 5.26%)。
//
// 加油包不摊 f（它不占一个「人月」），也不单独摊一整笔 s ——
// 它的支付成本是「¥200 余额充值触发的那一次链上归集」按比例摊回来的
// （定价修订 §4.3 与 C13：名义覆盖 1.3870×，摊回后 1.3142×）。
func packCostMicroUSD(transferEnableBytes int64) *big.Int {
	q := gibCostMicroUSD(transferEnableBytes)
	q.Mul(q, big.NewInt(10_000))
	return ceilDivBig(q, big.NewInt(10_000-packChannelShareBps))
}

// assertPriceFloor 跑 A7 的地板断言。返回 nil 表示通过。
//
// 断言（定价修订 A7 的落点原文）：
//
//	((amount_due − 本单返佣计提) / 周期月数 / FX) / 月度总成本(档位, 周期) ≥ 1.20
//
// 🔴 **与原文的一处有意偏差，理由必须留在这里：分子用的是
//
//	   `amount_gross − amount_discount − accrual`，不是 `amount_due`。**
//
//		amount_due = gross − discount − surplus − balance。后两项都不是「我们少收的钱」：
//		  · balance（余额抵扣）是**我们已经收到、正握在手里**的钱，它就是这一单的收入；
//		    按字面用 amount_due，一个用余额付掉一半的订单会被判成破地板并被拒绝 ——
//		    而它在现金上与全额付款完全一样。
//		  · surplus（升级折抵）是用户在**上一单**里已经付过、且已经过过一次地板的价值；
//		    把它从分子里扣掉、同时又不把对应的时间从分母里扣掉，任何一次升级都必然破地板。
//		    （分母那一半由 EffMonths 的分数形式处理：升级买的是 n × D_left / D_total 个月。）
//		只有 discount（优惠码）是真的少收，所以它留在扣项里 —— 而优惠码叠加长周期折扣
//		正是 A7 要防的那件事。
//
//		定价修订 §6 自己把「余额支付路径是否同样过闸」登记为**未决**，本实现选了
//		「余额算作收入」这一侧，并在这里留下推导，等那次 api-contract 修订来确认或推翻。
//
// 全程整数 / big.Int：一个用 float 判定的地板会在最薄的那一格上给出与手算不同的结论。
// 交叉相乘后的判据是
//
//	NetCents × Den × 1e10 ≥ 120 × (n×(Q×c+f)+s) × Num × FX_e4
//
// 自检（定价修订 §4.3 的最薄格，重度·年付·USDT）：
//
//	NetCents = 354100 − 3580 = 350520，n = 12，Q = 250 GiB，FX_e4 = 71500
//	→ 覆盖 1.2620×，与文档逐字一致（旧的按订单金额返佣口径是 1.1474×，破地板）。
func assertPriceFloor(in floorCheck) (ratioNum, ratioDen *big.Int, err error) {
	if in.EffMonthsDen <= 0 || in.EffMonthsNum <= 0 {
		return nil, nil, errors.New("有效月数无定义，无法判定成本覆盖")
	}
	if in.CnyPerUsdtE4 <= 0 {
		return nil, nil, errors.New("汇率未知，无法判定成本覆盖")
	}

	// 判据由下面这条不等式交叉相乘而来，全程整数，一步除法都不做：
	//
	//	(NetCents/100) / (Num/Den) / (FX_e4/1e4) / (cost_micro/1e6)  ≥  120/100
	//
	//	⟺  NetCents × Den × 1e10 × 100  ≥  120 × 100 × Num × FX_e4 × cost_micro
	//
	// 四个基数各自的来源：100（分→元）、Den/Num（有效月数的分数形式）、
	// 1e4（汇率的定点基数）、1e6（成本的微美元基数），120/100 是地板本身。
	lhs := new(big.Int).SetInt64(in.NetCents)
	lhs.Mul(lhs, big.NewInt(in.EffMonthsDen))
	lhs.Mul(lhs, new(big.Int).Exp(big.NewInt(10), big.NewInt(10), nil))
	lhs.Mul(lhs, big.NewInt(priceFloorDen))

	var costTimesN *big.Int
	if in.PlanKind == planKindPack {
		// 加油包没有「周期月数」这个概念：Num/Den 恒为 1/1，
		// 成本用摊回 ¥200 充值归集之后的口径（定价修订 §4.3 与 C13）。
		costTimesN = packCostMicroUSD(in.TransferEnableBytes)
	} else {
		n := periodMonths(in.Period)
		if n <= 0 {
			// 🔴 月数无定义（onetime）时**拒绝判定**，不许当成 1 个月接着算 ——
			//    那会拿月付成本去衡量一份不限时的服务，结论必然是「通过」。
			return nil, nil, errors.New("该周期没有月数定义，无法判定成本覆盖")
		}
		// monthlyCostMicroUSDTimesN 返回的是 n × 月度成本（为了让 s/n 不做截断除法），
		// 所以左边要同乘一个 n 把它约回来。漏掉这一步，长周期会被判得过松 n 倍。
		costTimesN = monthlyCostMicroUSDTimesN(in.TransferEnableBytes, n)
		lhs.Mul(lhs, big.NewInt(n))
	}

	rhs := new(big.Int).Set(costTimesN)
	rhs.Mul(rhs, big.NewInt(priceFloorNum))
	rhs.Mul(rhs, big.NewInt(100))
	rhs.Mul(rhs, big.NewInt(in.EffMonthsNum))
	rhs.Mul(rhs, big.NewInt(in.CnyPerUsdtE4))

	if lhs.Cmp(rhs) < 0 {
		return lhs, rhs, errPriceFloorViolated
	}
	return lhs, rhs, nil
}

// ============================================================
// createOrder
// ============================================================

// CreateOrder 实现 POST /api/v1/orders。
func (s *Server) CreateOrder(ctx context.Context, req gen.CreateOrderRequestObject) (gen.CreateOrderResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	if req.Body == nil {
		return gen.CreateOrder422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体为空")}, nil
	}

	att, idemResp := s.beginOrderIdempotency(ctx, auth.UserID, "CreateOrder", req.Params.IdempotencyKey, req.Body)
	if idemResp != nil {
		if r, ok := idemResp.(gen.CreateOrderResponseObject); ok {
			return r, nil
		}
		return gen.CreateOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "幂等判定返回了不匹配的响应类型", nil)}, nil
	}
	if att.Outcome == httpx.OutcomeReplay {
		// 重放：把上次落盘的响应体原样解回来。
		// meta.request_id 会是**上一次**的 —— 这是对的：幂等的定义就是
		// 「重放与首次执行对客户端不可区分」，而那次执行确实发生在那个 request_id 下。
		var resp gen.CreateOrder201JSONResponse
		if err := json.Unmarshal(att.Body, &resp.Body); err != nil {
			return gen.CreateOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "重放幂等结果失败", err)}, nil
		}
		resp.Headers.Location = orderLocation(resp.Body.Data.TradeNo)
		return resp, nil
	}

	order, plan, errResp := s.createOrderOnce(ctx, auth.UserID, *req.Body)
	if errResp != nil {
		return errResp, nil
	}

	var resp gen.CreateOrder201JSONResponse
	resp.Body.Data = createdOrderView(order, plan)
	resp.Body.Meta = s.meta(ctx)
	resp.Headers.Location = orderLocation(order.TradeNo)

	if body, err := json.Marshal(resp.Body); err == nil {
		if err := httpx.CompleteIdempotent(ctx, s.db, att.Key, 201, body); err != nil {
			// 落盘失败不影响这一单 —— 订单已经创建，钱的路径是对的。
			// 但同键重试会一直拿到 in_progress（→ 409），所以必须记 Error。
			s.logger.ErrorContext(ctx, "幂等结果落盘失败，同键重试将持续 409",
				"err", err, "trade_no", order.TradeNo)
		}
	}
	return resp, nil
}

func orderLocation(tradeNo string) string { return "/api/v1/orders/" + url.PathEscape(tradeNo) }

// beginOrderIdempotency 抢占幂等键，把四种失败翻成契约里的响应。
//
// 返回的第二个值非 nil 时调用方直接返回它。用 `any` 是因为 CreateOrder 与 PayOrder
// 的响应类型不同但错误分支完全一样 —— 两份拷贝迟早会有一份忘了 mismatch 分支，
// 而那一份的现象是「用同一个键改了金额，第二次也成功了」。
func (s *Server) beginOrderIdempotency(ctx context.Context, userID int64, endpoint, key string, body any) (*httpx.Attempt, any) {
	// 🔴 指纹用的是**解码后再规范序列化**的载荷，不是原始字节。
	//    httpx 的注释要求原文，但生成代码在 handler 之前就把 Body 读完了（strict handler 解 JSON），
	//    到这里已经没有原文可拿。规范序列化的性质比原文更强：它让「键顺序不同、语义相同」
	//    的两次重试判成同一个载荷，而那正是我们想要的幂等；它唯一失去的是
	//    「原文里有而结构体里没有的字段」——那些字段本来就不影响这一单。
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, s.idempotencyErrResponse(ctx, endpoint, httpx.ErrIdempotencyKeyMalformed)
	}
	att, err := httpx.BeginIdempotent(ctx, s.db, httpx.IdempotentRequest{
		Key:      key,
		UserID:   &userID,
		Endpoint: endpoint,
		Body:     payload,
	})
	if err != nil {
		return nil, s.idempotencyErrResponse(ctx, endpoint, err)
	}
	return att, nil
}

// idempotencyErrResponse 把幂等骨架的四种失败翻成对应端点的响应类型。
//
// 端点只有两个，写成 switch 而不是泛型：泛型在这里只会让「哪个端点有哪些状态码」
// 这件事更难看清 —— 而契约给 createOrder 与 payOrder **都没有声明 400**，
// 所以「缺 Idempotency-Key」这一档只能落到 422（api-contract §2.3 的
// VALIDATION_MALFORMED_BODY 在这两条路由上没有通道）。
func (s *Server) idempotencyErrResponse(ctx context.Context, endpoint string, err error) any {
	var (
		conflict *gen.ErrConflictJSONResponse
		unproc   *gen.ErrUnprocessableJSONResponse
		internal *gen.ErrInternalJSONResponse
	)
	switch {
	case errors.Is(err, httpx.ErrIdempotencyKeyMissing):
		r := s.unprocessable(ctx, "缺少 Idempotency-Key 请求头")
		unproc = &r
	case errors.Is(err, httpx.ErrIdempotencyKeyMalformed):
		r := s.unprocessable(ctx, "Idempotency-Key 形态非法（长度 8–128 的可见 ASCII）")
		unproc = &r
	case errors.Is(err, httpx.ErrIdempotencyMismatch):
		r := gen.ErrConflictJSONResponse{
			Body:    s.envelope(ctx, gen.STATEIDEMPOTENCYMISMATCH, "同一 Idempotency-Key 的请求载荷不一致"),
			Headers: gen.ErrConflictResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
		}
		conflict = &r
	case errors.Is(err, httpx.ErrIdempotencyInProgress), errors.Is(err, httpx.ErrIdempotencyKeyStale):
		// 🔴 in_progress：同键的上一次还在跑。**绝不能退化成「当作首次执行」** ——
		//    那正是重复扣款的路径。
		r := s.conflict(ctx, "同一 Idempotency-Key 的请求正在处理中，请稍后重试")
		conflict = &r
	default:
		r := s.internalErr(ctx, "幂等键处理失败", err)
		internal = &r
	}

	if endpoint == "CreateOrder" {
		switch {
		case conflict != nil:
			return gen.CreateOrder409JSONResponse{ErrConflictJSONResponse: *conflict}
		case unproc != nil:
			return gen.CreateOrder422JSONResponse{ErrUnprocessableJSONResponse: *unproc}
		default:
			return gen.CreateOrder500JSONResponse{ErrInternalJSONResponse: *internal}
		}
	}
	switch {
	case conflict != nil:
		return gen.PayOrder409JSONResponse{ErrConflictJSONResponse: *conflict}
	case unproc != nil:
		return gen.PayOrder422JSONResponse{ErrUnprocessableJSONResponse: *unproc}
	default:
		return gen.PayOrder500JSONResponse{ErrInternalJSONResponse: *internal}
	}
}

// orderDraft 是地板断言通过之后、真正写库之前的一张订单。
type orderDraft struct {
	Type            dbgen.OrderType
	Period          dbgen.OrderPeriod
	PlanID          int64
	AmountGross     int64
	AmountDiscount  int64
	SurplusAmount   int64
	AmountBalance   int64
	AmountDue       int64
	CouponID        *int64
	SurplusOrderIDs []int64
	PrevOrderID     *int64
	CoversTo        pgtype.Timestamptz
	PriceMonthly    *int64
	InvitedBy       *int64
	AccrualCents    int64

	// upgradeDays 只在升级单上有值，唯一的用途是把地板断言的分母缩到「本段」——
	// 升级买的是同一个周期档里剩下的那几天，不是一整个周期。
	upgradeDays upgradeDays
}

// createOrderOnce 是下单的全部业务。返回值第三项非 nil 时是终止响应。
func (s *Server) createOrderOnce(ctx context.Context, userID int64, body gen.CreateOrderRequest) (dbgen.Order, dbgen.GetPlanForOrderRow, gen.CreateOrderResponseObject) {
	var zeroOrder dbgen.Order
	var zeroPlan dbgen.GetPlanForOrderRow

	period, err := orderPeriodFromContract(string(body.Period))
	if err != nil {
		return zeroOrder, zeroPlan, gen.CreateOrder422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, err.Error(), detail("period", string(body.Period))),
		}
	}

	uctx, err := s.db.GetUserOrderContext(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		// 会话有效但用户已注销。401 而不是 404：他的问题是「这个身份不能再用了」。
		return zeroOrder, zeroPlan, gen.CreateOrder401JSONResponse{
			ErrUnauthorizedJSONResponse: s.unauthorized(ctx, gen.AUTHTOKENINVALID, "账号不存在或已注销"),
		}
	}
	if err != nil {
		return zeroOrder, zeroPlan, gen.CreateOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取下单上下文失败", err)}
	}

	plan, err := s.db.GetPlanForOrder(ctx, body.PlanId)
	if errors.Is(err, pgx.ErrNoRows) {
		return zeroOrder, zeroPlan, gen.CreateOrder422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "套餐不存在", detail("plan_id", strconv.FormatInt(body.PlanId, 10))),
		}
	}
	if err != nil {
		return zeroOrder, zeroPlan, gen.CreateOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取套餐失败", err)}
	}

	draft, errResp := s.buildOrderDraft(ctx, userID, uctx, plan, period, body)
	if errResp != nil {
		return zeroOrder, zeroPlan, errResp
	}

	// ---- 🔴 地板断言：在写 orders 之前，失败拒绝下单 ----
	set := loadPaymentSettings(ctx, s.db, s.logger)
	check := floorCheck{
		PlanKind:            plan.Kind,
		Period:              draft.Period,
		TransferEnableBytes: plan.TransferEnable,
		NetCents:            draft.AmountGross - draft.AmountDiscount - draft.AccrualCents,
		EffMonthsNum:        1,
		EffMonthsDen:        1,
		CnyPerUsdtE4:        set.CnyPerUsdtE4,
		AccrualCents:        draft.AccrualCents,
	}
	if plan.Kind != planKindPack {
		n := periodMonths(draft.Period)
		check.EffMonthsNum = n
		check.EffMonthsDen = 1
		if draft.Type == dbgen.OrderTypeUpgrade && draft.upgradeDays.total > 0 {
			// 升级单买的是「同一个周期档的剩余那一段」：有效月数 = n × D_left / D_total。
			// 不做这个缩放，任何一次升级都会因为「按整周期的成本比一段时间的收入」而破地板。
			check.EffMonthsNum = n * draft.upgradeDays.left
			check.EffMonthsDen = draft.upgradeDays.total
		}
	}
	if lhs, rhs, err := assertPriceFloor(check); err != nil {
		// 🔴 告警 + 拒绝，不是记日志放行。
		//    metric 字段给日志告警一个可以 key 的名字（monitoring.md 的 bp_* 约定）。
		s.logger.ErrorContext(ctx, "订单未通过 1.20× 成本覆盖地板，已拒绝创建",
			"metric", "bp_price_floor_violation",
			"user_id", userID, "plan_id", plan.ID, "plan_code", plan.Code,
			"period", string(draft.Period), "order_type", string(draft.Type),
			"amount_gross", draft.AmountGross, "amount_discount", draft.AmountDiscount,
			"surplus_amount", draft.SurplusAmount, "amount_balance", draft.AmountBalance,
			"amount_due", draft.AmountDue, "accrual_cents", draft.AccrualCents,
			"transfer_enable_bytes", plan.TransferEnable,
			"eff_months_num", check.EffMonthsNum, "eff_months_den", check.EffMonthsDen,
			"cny_per_usdt_e4", set.CnyPerUsdtE4,
			"lhs", bigStr(lhs), "rhs", bigStr(rhs), "err", err)
		return zeroOrder, zeroPlan, gen.CreateOrder422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				"该组合（套餐 / 周期 / 优惠码）的价格低于成本下限，无法下单，请更换周期或去掉优惠码"),
		}
	}

	tradeNo, err := newTradeNo(ctx, s.db)
	if err != nil {
		return zeroOrder, zeroPlan, gen.CreateOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "生成订单号失败", err)}
	}

	var created dbgen.Order
	var couponRaced bool
	var balanceShort bool
	txErr := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		o, err := writeOrder(ctx, q, userID, tradeNo, draft, time.Now())
		if err != nil {
			if errors.Is(err, errCouponRaced) {
				couponRaced = true
			}
			if errors.Is(err, errBalanceReserveFailed) {
				balanceShort = true
			}
			return err
		}
		created = o
		return nil
	})
	if couponRaced {
		return zeroOrder, zeroPlan, gen.CreateOrder422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "优惠码刚刚被用完了，请去掉优惠码后重试", detail("coupon_code", "已用尽")),
		}
	}
	// 余额是在下单这一刻真的扣走的，所以「余额不足」是一个 422 而不是 500。
	// 触发它的典型场景是并发下单：两张单各自算出同一笔余额可抵，但只有一张扣得动。
	if balanceShort {
		return zeroOrder, zeroPlan, gen.CreateOrder422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "钱包余额不足，请去掉余额抵扣后重试", detail("use_balance", "余额不足")),
		}
	}
	if txErr != nil {
		return zeroOrder, zeroPlan, gen.CreateOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "创建订单失败", txErr)}
	}
	return created, plan, nil
}

func bigStr(v *big.Int) string {
	if v == nil {
		return ""
	}
	return v.String()
}

// upgradeDays 是升级折抵的两个天数，随 draft 传到地板断言那一步。
type upgradeDays struct{ left, total int64 }

var errCouponRaced = errors.New("优惠码已被并发抢完")

// buildOrderDraft 完成 orders.type 推导、定价、折抵、优惠码与余额抵扣。
func (s *Server) buildOrderDraft(
	ctx context.Context,
	userID int64,
	uctx dbgen.GetUserOrderContextRow,
	plan dbgen.GetPlanForOrderRow,
	period dbgen.OrderPeriod,
	body gen.CreateOrderRequest,
) (*orderDraft, gen.CreateOrderResponseObject) {
	reject := func(msg string, d ...gen.ErrorDetail) gen.CreateOrderResponseObject {
		return gen.CreateOrder422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, msg, d...)}
	}

	// ---- 可售性。判错了是 422 不是 404（GetPlanForOrder 刻意不过滤 archived/sellable，
	//      因为升级折抵的分母就在一个已下架套餐那一行上）。----
	if plan.ArchivedAt.Valid {
		return nil, reject("该套餐已下架")
	}
	ownsThisPlan := uctx.PlanID != nil && *uctx.PlanID == plan.ID
	if !plan.Sellable && !(plan.Renewable && ownsThisPlan) {
		// 「下架但允许老用户续费」的套餐对它自己的订户可售，对别人不可售。
		return nil, reject("该套餐已停止销售")
	}

	draft := &orderDraft{PlanID: plan.ID, Period: period, InvitedBy: uctx.InvitedBy}

	// ---- orders.type 推导（ADR 0013 §4.6）----
	switch {
	case plan.Kind == planKindPack:
		draft.Type = dbgen.OrderTypeTrafficPack
		// 加油包只有一次性价格；周期在契约里是必填，这里统一收成 onetime，
		// 而不是把用户传的 monthly 原样落库 —— 一个 period='monthly' 的加油包单
		// 会让将来任何按周期聚合的报表把它算成一份月付收入。
		draft.Period = dbgen.OrderPeriodOnetime
	case period == dbgen.OrderPeriodOnetime:
		// 🔴 不限时周期（plans.price_onetime）在 schema 层面是可售的，但 ADR 0013 §2.2 裁决
		//    **P1 阶段不售**：covers_to 为 NULL 时 D_total / D_left 无定义，
		//    升级与退款两条路径都会踩空。不写死就是一个空指针等着人踩。
		return nil, reject("暂不销售不限时套餐")
	case !uctx.SubscriptionActive:
		draft.Type = dbgen.OrderTypeNew
	case ownsThisPlan:
		draft.Type = dbgen.OrderTypeRenew
	default:
		draft.Type = dbgen.OrderTypeUpgrade
	}

	if draft.Type == dbgen.OrderTypeUpgrade {
		return s.buildUpgradeDraft(ctx, userID, uctx, plan, draft, body)
	}

	// ---- 整单定价 ----
	price := planPriceAtPeriod(plan.PriceMonthly, plan.PriceQuarterly, plan.PriceHalfYearly,
		plan.PriceYearly, plan.PriceOnetime, draft.Period)
	if price == nil {
		// NULL = 该周期不售（0002 的列注释），不是「价格是 0」。
		return nil, reject("该套餐不支持所选付费周期", detail("period", string(draft.Period)))
	}
	draft.AmountGross = *price
	draft.PriceMonthly = plan.PriceMonthly

	if errResp := s.applyCoupon(ctx, userID, plan.ID, draft, body.CouponCode); errResp != nil {
		return nil, errResp
	}
	applyBalance(uctx.BalanceCents, draft, body.UseBalance)
	draft.AmountDue = draft.AmountGross - draft.AmountDiscount - draft.SurplusAmount - draft.AmountBalance

	if errResp := s.loadAccrual(ctx, userID, plan.ID, draft); errResp != nil {
		return nil, errResp
	}
	return draft, nil
}

// buildUpgradeDraft 走 ADR 0013 §4.2 的升级折抵。
func (s *Server) buildUpgradeDraft(
	ctx context.Context,
	userID int64,
	uctx dbgen.GetUserOrderContextRow,
	plan dbgen.GetPlanForOrderRow,
	draft *orderDraft,
	body gen.CreateOrderRequest,
) (*orderDraft, gen.CreateOrderResponseObject) {
	reject := func(msg string, d ...gen.ErrorDetail) gen.CreateOrderResponseObject {
		return gen.CreateOrder422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, msg, d...)}
	}

	if plan.Kind != planKindCycle {
		return nil, reject("只能升级到周期套餐")
	}
	// 🔴 边界 4（§4.3）：升级单不接受优惠码。amount_gross 已被 D_left/D_total 缩过一次，
	//    百分比券再作用一次是两层比例复合；而且它开了「先升级把 gross 缩小、再用固定额券」的组合口。
	if body.CouponCode != nil && strings.TrimSpace(*body.CouponCode) != "" {
		return nil, reject("升级订单不支持使用优惠码", detail("coupon_code", "升级单不可用"))
	}

	src, err := s.db.GetSubscriptionSource(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		// 没有可作折抵源的已完成周期单 —— 例如账号是后台直接开的（page-inventory 把 D1
		// 定性为「直接等于送钱」）。送出去的东西不产生折抵权（§2.2）。
		return nil, reject("找不到可折抵的当前订阅，请等当前订阅到期后再购买")
	}
	if err != nil {
		return nil, gen.CreateOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取折抵源失败", err)}
	}
	// 🔴 必须看 covers_to 判 422，**不许把 d_total = 0 当成「剩 0 天」接着算**。
	//    查询在 covers_to IS NULL 时返回 0，那是「无定义」不是「零」（§2.2）。
	if !src.CoversTo.Valid || src.DTotal <= 0 {
		return nil, reject("当前订阅是不限时套餐或缺少服务区间，无法折抵升级")
	}
	if src.Period == nil {
		return nil, reject("当前订阅缺少付费周期，无法折抵升级")
	}

	// **`period = source.period` 是抗套利的一道闸**：新套餐按同一个周期档的标价折算，
	// 用户不能把年付折扣的信用拿去按月付标价买东西。
	srcPeriod := *src.Period
	draft.Period = srcPeriod

	priceNew := planPriceAtPeriod(plan.PriceMonthly, plan.PriceQuarterly, plan.PriceHalfYearly,
		plan.PriceYearly, plan.PriceOnetime, srcPeriod)
	if priceNew == nil {
		return nil, reject("目标套餐不支持当前订阅的付费周期", detail("period", string(srcPeriod)))
	}

	// price_cur 取**当前套餐**在 source.period 上的标价。当前套餐完全可能已经下架，
	// 所以这里用的 GetPlanForOrder 刻意不过滤 archived / sellable。
	var priceCur int64
	if uctx.PlanID != nil {
		cur, err := s.db.GetPlanForOrder(ctx, *uctx.PlanID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, gen.CreateOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取当前套餐失败", err)}
		}
		if err == nil {
			if p := planPriceAtPeriod(cur.PriceMonthly, cur.PriceQuarterly, cur.PriceHalfYearly,
				cur.PriceYearly, cur.PriceOnetime, srcPeriod); p != nil {
				priceCur = *p
			}
		}
	}
	if *priceNew <= priceCur {
		// 边界 1：不允许中途降级。降级必然 surplus_raw > amount_gross；
		// 不给找零用户觉得被坑，给找零就开了「买高档 → 降级 → 提走差额余额」的套现环 ——
		// 而「余额不可提现」在数据库层面无法强制（data-model §7.1）。
		return nil, reject("不支持降级到更低档位的套餐，可在当前订阅到期后再切换")
	}

	// surplus_raw = floor(V_source × D_left / D_total)；amount_gross = floor(price_new × D_left / D_total)。
	// 一律 floor 且**不引入 bps 中间量**：先算 r_bps 再乘会把误差从 ≤1 分放大到 V/10000（§2.3）。
	draft.AmountGross = mulDiv(*priceNew, src.DLeft, src.DTotal)
	surplusRaw := mulDiv(src.VSource, src.DLeft, src.DTotal)
	// 边界 2：折抵只能抵到 0，不产生找零。
	draft.SurplusAmount = minInt64(surplusRaw, draft.AmountGross)
	draft.AmountDiscount = 0
	draft.CouponID = nil
	draft.SurplusOrderIDs = []int64{src.ID}
	draft.PrevOrderID = &src.ID
	draft.CoversTo = src.CoversTo // upgrade 继承旧的 covers_to：升级不买时间，只换档位。
	draft.PriceMonthly = plan.PriceMonthly
	draft.upgradeDays = upgradeDays{left: src.DLeft, total: src.DTotal}

	applyBalance(uctx.BalanceCents, draft, body.UseBalance)
	draft.AmountDue = draft.AmountGross - draft.AmountDiscount - draft.SurplusAmount - draft.AmountBalance

	if errResp := s.loadAccrual(ctx, userID, plan.ID, draft); errResp != nil {
		return nil, errResp
	}
	return draft, nil
}

// applyCoupon 把优惠码判定并落进 draft。
func (s *Server) applyCoupon(ctx context.Context, userID, planID int64, draft *orderDraft, code *string) gen.CreateOrderResponseObject {
	if code == nil || strings.TrimSpace(*code) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(*code)
	period := draft.Period
	row, err := s.db.VerifyCouponForUser(ctx, dbgen.VerifyCouponForUserParams{
		Code:   trimmed,
		UserID: userID,
		PlanID: &planID,
		Period: &period,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.CreateOrder422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "优惠码无效", detail("coupon_code", "无效")),
		}
	}
	if err != nil {
		return gen.CreateOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "校验优惠码失败", err)}
	}
	// 与 verifyCoupon 走的是**同一个** evaluateCoupon —— 分开写的那天起，
	// 「校验说可用、下单说不可用」就成了必然会发生的 bug。
	e := evaluateCoupon(row, true, draft.AmountGross)
	if !e.Valid {
		return gen.CreateOrder422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, e.Reason, detail("coupon_code", e.Reason)),
		}
	}
	draft.AmountDiscount = e.Discount
	id := row.ID
	draft.CouponID = &id
	return nil
}

// applyBalance 算余额抵扣。
//
// 抵扣额封顶到「还需要付的部分」而不是订单原价：抵多了 amount_due 会变负，
// 而 orders 上的 `CHECK (amount_due >= 0)` 会把一条产品规则变成一次 500。
func applyBalance(balanceCents int64, draft *orderDraft, useBalance *bool) {
	if useBalance == nil || !*useBalance || balanceCents <= 0 {
		return
	}
	remain := draft.AmountGross - draft.AmountDiscount - draft.SurplusAmount
	if remain <= 0 {
		return
	}
	draft.AmountBalance = minInt64(balanceCents, remain)
}

// loadAccrual 取本单的返佣计提（分），供地板断言的分子扣减。
func (s *Server) loadAccrual(ctx context.Context, userID, planID int64, draft *orderDraft) gen.CreateOrderResponseObject {
	row, err := s.db.GetOrderCommissionAccrual(ctx, dbgen.GetOrderCommissionAccrualParams{
		UserID:         userID,
		PlanID:         planID,
		DefaultRateBps: defaultCommissionRateBps,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// 用户不存在（并发注销）。返佣按 0 算而不是报错 —— 0 让地板断言更严，方向是安全的；
		// 真正的「用户不存在」会在写 orders 时被外键挡住。
		return nil
	}
	if err != nil {
		// 🔴 **不能吞掉这个错误按 0 继续。** accrual 是分子的扣项，
		//    按 0 算会让断言变松，于是「查不到返佣」这件事会以「多放行一批破地板的订单」的形式出现。
		return gen.CreateOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取返佣计提失败", err)}
	}
	draft.AccrualCents = row.AccrualCents
	return nil
}

// writeOrder 在一个事务里写订单、核销优惠码、写状态审计。
func writeOrder(ctx context.Context, q createOrderWriter, userID int64, tradeNo string, draft *orderDraft, now time.Time) (dbgen.Order, error) {
	// 优惠码核销放在**创建时**而不是支付时。
	//   · 支付时核销会超卖：两张 pending 单可以同时通过「还没用完」的检查。
	//   · 创建时核销的代价是「下单后取消」会漏掉一次全局次数 —— 这一次漏是可见的、有界的，
	//     而超卖是不可见的。两害相权取可见的那个。
	//     （**每用户**次数不受影响：CountUserCouponUses 口径按 orders 算，且排除 cancelled。）
	if draft.CouponID != nil {
		if _, err := q.IncrementCouponUse(ctx, *draft.CouponID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return dbgen.Order{}, errCouponRaced
			}
			return dbgen.Order{}, err
		}
	}

	period := draft.Period
	planID := draft.PlanID

	// 🔴 `orders.surplus_order_ids` 是 `bigint[] NOT NULL DEFAULT '{}'`，而这里把它作为
	//    **显式参数**传进 INSERT —— 显式 NULL 会覆盖掉 DEFAULT，不会退回 '{}'。
	//    `draft.SurplusOrderIDs` 只有 upgrade 单会填（buildUpgradeDraft），
	//    new / renew 单它是 nil slice → SQL NULL → 23502 非空约束违反 → **下单必 500**。
	//
	//    也就是说：升级单能下，而**所有新购与续费都下不了单**，且失败得毫无线索
	//    （用户看到「内部错误」，日志里是一句 constraint 名）。
	//    1,263 个进程内单测一个都抓不到它 —— 假的 CreateOrder 不执行 NOT NULL。
	//    它是在真库上下第一单的那一刻暴露的。
	surplusOrderIDs := draft.SurplusOrderIDs
	if surplusOrderIDs == nil {
		surplusOrderIDs = []int64{}
	}

	order, err := q.CreateOrder(ctx, dbgen.CreateOrderParams{
		TradeNo:         tradeNo,
		UserID:          userID,
		Type:            draft.Type,
		PlanID:          &planID,
		Period:          &period,
		Currency:        "CNY",
		AmountGross:     draft.AmountGross,
		AmountDiscount:  draft.AmountDiscount,
		SurplusAmount:   draft.SurplusAmount,
		AmountBalance:   draft.AmountBalance,
		AmountDue:       draft.AmountDue,
		SurplusOrderIds: surplusOrderIDs,
		CouponID:        draft.CouponID,
		InvitedBy:       draft.InvitedBy,
		ExpiresAt:       tstz(now.Add(orderPayWindow)),
		// covers_from / covers_to 在**开通**时才写死（ADR 0013 §2.1 的四行表），
		// 唯一的例外是 upgrade 的 covers_to —— 它继承折抵源，而折抵源在下单这一刻就确定了。
		CoversTo:            draft.CoversTo,
		PrevOrderID:         draft.PrevOrderID,
		PriceMonthlyAtOrder: draft.PriceMonthly,
	})
	if err != nil {
		return dbgen.Order{}, err
	}

	// 🔴 余额抵扣必须在这里真的把钱扣走，否则 amount_balance 就是白送的。
	//    放在 CreateOrder 之后是因为分录的 ref_id 需要订单 id。
	if err := reserveOrderBalance(ctx, q, userID, order.ID, order.TradeNo, draft.AmountBalance); err != nil {
		return dbgen.Order{}, err
	}

	// 状态机没有触发器兜底，漏写审计不会报错 —— 而「我明明下过单」的工单只能靠这张表回答。
	actor := "user:" + strconv.FormatInt(userID, 10)
	reason := "创建订单"
	if _, err := q.InsertOrderTransition(ctx, dbgen.InsertOrderTransitionParams{
		OrderID:    order.ID,
		FromStatus: nil, // 从无到有，from_status 为 NULL。
		ToStatus:   dbgen.OrderStatusPending,
		Reason:     &reason,
		Actor:      actor,
	}); err != nil {
		return dbgen.Order{}, err
	}
	return order, nil
}

// newTradeNo 生成对外单号并探测占用。
//
// `orders.trade_no` 上有 UNIQUE，撞号本来就会被数据库拒绝 —— 这次探测不是为了防撞号，
// 是为了让撞号变成一次重试而不是一条 500（用户看到的是「下单失败」，而重试成功的概率接近 1）。
func newTradeNo(ctx context.Context, q interface {
	TradeNoExists(ctx context.Context, tradeNo string) (bool, error)
},
) (string, error) {
	for range 5 {
		suffix, err := randomDigits(8)
		if err != nil {
			return "", err
		}
		no := "BP" + time.Now().UTC().Format("20060102150405") + suffix
		taken, err := q.TradeNoExists(ctx, no)
		if err != nil {
			return "", err
		}
		if !taken {
			return no, nil
		}
	}
	return "", errors.New("连续 5 次生成的订单号都已占用")
}

// createdOrderView 把刚写进去的行拼成响应体。用写入返回的行而不是回查一次：
// 回查会读到一个**可能已经变了**的状态（并发的取消或超时扫描），
// 而 201 的语义是「我刚创建了这个东西」，它必须描述创建的那一刻。
func createdOrderView(o dbgen.Order, plan dbgen.GetPlanForOrderRow) gen.Order {
	name := plan.Name
	out := gen.Order{
		TradeNo:        o.TradeNo,
		Type:           orderTypeView(o.Type),
		Status:         orderStatusView(o.Status),
		Currency:       gen.OrderCurrencyCNY,
		TotalAmount:    o.AmountGross,
		PayableAmount:  o.AmountDue,
		DiscountAmount: &o.AmountDiscount,
		SurplusAmount:  &o.SurplusAmount,
		BalanceAmount:  &o.AmountBalance,
		PlanId:         o.PlanID,
		PlanName:       &name,
	}
	if o.Period != nil {
		p := string(*o.Period)
		out.Period = &p
	}
	if o.CreatedAt.Valid {
		out.CreatedAt = o.CreatedAt.Time.UTC()
	}
	out.ExpiresAt = tsPtr(o.ExpiresAt)
	out.PaidAt = tsPtr(o.PaidAt)
	out.RateLockedAt = tsPtr(o.FxLockedAt)
	return out
}

func mulDiv(v, num, den int64) int64 {
	if den == 0 {
		return 0
	}
	r := new(big.Int).SetInt64(v)
	r.Mul(r, big.NewInt(num))
	r.Quo(r, big.NewInt(den))
	if !r.IsInt64() {
		return 0
	}
	return r.Int64()
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// ============================================================
// getOrder / listOrders / cancelOrder
// ============================================================

// GetOrder 实现 GET /api/v1/orders/{trade_no}。
func (s *Server) GetOrder(ctx context.Context, req gen.GetOrderRequestObject) (gen.GetOrderResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	// 🔴 `GetUserOrder` 同时按 trade_no 与 user_id 过滤。
	//    orders.sql 的 GetOrderByTradeNo 只按单号查，用户面用它就是越权读单。
	row, err := s.db.GetUserOrder(ctx, dbgen.GetUserOrderParams{TradeNo: req.TradeNo, UserID: auth.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.GetOrder404JSONResponse{ErrNotFoundJSONResponse: s.orderNotFound(ctx)}, nil
	}
	if err != nil {
		return gen.GetOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取订单失败", err)}, nil
	}
	return gen.GetOrder200JSONResponse{Data: orderView(row), Meta: s.meta(ctx)}, nil
}

// ListOrders 实现 GET /api/v1/orders。
func (s *Server) ListOrders(ctx context.Context, req gen.ListOrdersRequestObject) (gen.ListOrdersResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	want, limitPlusOne := pageLimit(req.Params.Limit)

	params := dbgen.ListUserOrdersPageParams{UserID: auth.UserID, PageLimit: limitPlusOne}
	if req.Params.Cursor != nil && *req.Params.Cursor != "" {
		cur, err := decodeKeysetCursor(*req.Params.Cursor)
		if err != nil {
			s.logger.WarnContext(ctx, "订单游标非法，按第一页处理", "request_id", middleware.RequestIDFrom(ctx))
		} else {
			// 🔴 行比较 `(created_at, id) < (cursor_at, cursor_id)` —— 两个分量**必须同时传**。
			//    只传其中一个时行比较在 SQL 里求值为 NULL，返回 0 行而不报错，
			//    用户看到的是「后面没有了」。
			params.CursorAt = tstz(*cur.At)
			params.CursorID = cur.ID
		}
	}

	rows, err := s.db.ListUserOrdersPage(ctx, params)
	if err != nil {
		return gen.ListOrders500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取订单列表失败", err)}, nil
	}

	data, next, hasMore := orderPage(rows, want)
	meta := s.meta(ctx)
	meta.HasMore = &hasMore
	meta.NextCursor = next
	// 用户面**不返 total**（api-contract §2.4）：COUNT(*) 在 db-f1-micro 上是实打实的开销，
	// 而这个页面不需要「共 N 条」。
	return gen.ListOrders200JSONResponse{Data: data, Meta: meta}, nil
}

func orderPage(rows []dbgen.ListUserOrdersPageRow, want int) ([]gen.Order, *string, bool) {
	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.Order, 0, len(rows))
	for i := range rows {
		out = append(out, orderListView(rows[i]))
	}
	if !hasMore || len(rows) == 0 {
		return out, nil, false
	}
	last := rows[len(rows)-1]
	cur := keysetCursor{ID: &last.ID}
	if last.CreatedAt.Valid {
		at := last.CreatedAt.Time.UTC()
		cur.At = &at
	}
	enc := encodeKeysetCursor(cur)
	if enc == "" {
		return out, nil, false
	}
	return out, &enc, true
}

// CancelOrder 实现 POST /api/v1/orders/{trade_no}/cancel。
func (s *Server) CancelOrder(ctx context.Context, req gen.CancelOrderRequestObject) (gen.CancelOrderResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	// **两次读一次写**：先确认存在与归属（0 行 → 404），再让条件 UPDATE 判状态（0 行 → 409）。
	// 合成一次的话两种失败都是 0 行，用户拿到的是一个不说原因的错误 ——
	// 而「取消失败」不给原因是 user-journey 点名的那类死胡同。
	if _, err := s.db.GetUserOrder(ctx, dbgen.GetUserOrderParams{TradeNo: req.TradeNo, UserID: auth.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.CancelOrder404JSONResponse{ErrNotFoundJSONResponse: s.orderNotFound(ctx)}, nil
		}
		return gen.CancelOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取订单失败", err)}, nil
	}

	var cancelled dbgen.CancelUserPendingOrderRow
	var notPending bool
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		row, err := q.CancelUserPendingOrder(ctx, dbgen.CancelUserPendingOrderParams{
			TradeNo: req.TradeNo, UserID: auth.UserID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			notPending = true
			return err
		}
		if err != nil {
			return err
		}
		cancelled = row
		// ⚠️ pending 单**还没有收款地址**（地址在 payOrder 才分配），
		//    所以取消不需要、也不允许释放地址：`pay_addresses.assigned_order_id`
		//    是一次性单调赋值，「永不复用」就是靠它不回退保证的（ADR 0012 §5.2）。
		from := dbgen.OrderStatusPending
		reason := "用户取消"
		if _, err := q.InsertOrderTransition(ctx, dbgen.InsertOrderTransitionParams{
			OrderID:    row.ID,
			FromStatus: &from,
			ToStatus:   dbgen.OrderStatusCancelled,
			Reason:     &reason,
			Actor:      "user:" + strconv.FormatInt(auth.UserID, 10),
		}); err != nil {
			return err
		}
		// 下单时锁定的余额必须在**同一事务**里退回：先迁移后退款而中间死掉，
		// 这笔钱就再也没有路径回到用户手上（订单已是 cancelled，不会再被扫到）。
		return releaseOrderBalance(ctx, q, row.UserID, row.ID, row.TradeNo, row.AmountBalance, "订单取消")
	})
	if notPending {
		return gen.CancelOrder409JSONResponse{ErrConflictJSONResponse: s.conflict(ctx, "只有待支付的订单可以取消")}, nil
	}
	if err != nil {
		return gen.CancelOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "取消订单失败", err)}, nil
	}
	return gen.CancelOrder200JSONResponse{Data: cancelledOrderView(cancelled), Meta: s.meta(ctx)}, nil
}

// ============================================================
// 收银台视图（payOrder / getOrderPayment / recheckOrderPayment 共用）
// ============================================================

// checkoutView 把 `GetOrderCheckout` 的一行翻成契约的 PaymentCheckout。
//
// 🔴 三个 operation 共用这一个函数是硬要求（orders_user.sql 的注释逐字写着）：
// 三处各写一份「状态怎么算」的逻辑，漂移的那天就是用户看到「已支付」但订阅没开通的那天。
func checkoutView(row dbgen.GetOrderCheckoutRow, set paymentSettings) gen.PaymentCheckout {
	out := gen.PaymentCheckout{
		TradeNo: row.TradeNo,
		State:   paymentStateView(row),
	}
	if row.PayChain != nil && *row.PayChain == payChainTron {
		chain := gen.TRC20
		out.Chain = &chain
	}
	out.Address = row.PayAddress
	if row.PayAmountUsdt6 != nil {
		amt := *row.PayAmountUsdt6
		disp := usdt6Display(amt)
		out.AmountUsdt6 = &amt
		out.AmountDisplay = &disp
	}
	if e4, ok := numericToE4(row.FxUsdtPerCny); ok {
		// cny_per_usdt_e4 在 Go 侧算而不是在 SQL 里乘 1e4：带 cast 的表达式列会被 sqlc 判成 NOT NULL，
		// 而这一列在订单走到 payOrder 之前恒为 NULL —— 「刚下单就打开收银台」会变成 scan 失败。
		out.CnyPerUsdtE4 = &e4
	}
	out.QuoteExpiresAt = tsPtr(row.ExpiresAt)
	received := row.ReceivedUsdt6
	out.ReceivedUsdt6 = &received
	confirmations := set.ConfirmationsRequired
	out.ConfirmationsRequired = &confirmations
	note := paymentCheckoutNote
	out.Note = &note
	if out.State == gen.PaymentStateUnderpaid {
		// 只有 underpaid 才下发差额：其它状态下这个数没有意义，
		// 而一个恒定出现的 shortfall 字段会诱使前端在 waiting 时也显示「还差 X」。
		shortfall := row.ShortfallUsdt6
		out.ShortfallUsdt6 = &shortfall
	}
	return out
}

// paymentStateView 是 orders_user.sql 注释里那张映射表的唯一实现。
//
// 🔴 `PAYMENT_UNDERPAID` **不是错误**，是订单状态 —— 它走 200 而不是错误通道（契约原文）。
func paymentStateView(row dbgen.GetOrderCheckoutRow) gen.PaymentState {
	switch row.Status {
	case dbgen.OrderStatusPending:
		return gen.PaymentStateWaiting
	case dbgen.OrderStatusPaying:
		if row.ReceivedUsdt6 <= 0 {
			return gen.PaymentStateWaiting
		}
		// 有到账但还没定档（未固化 / 等下一轮扫描）都是 confirming ——
		// 这一档存在的意义是让用户看到「钱到了，我们在等链上确认」，
		// 而不是继续盯着「等待支付」怀疑自己转错了地址。
		return gen.PaymentStateConfirming
	case dbgen.OrderStatusUnderpaid:
		return gen.PaymentStateUnderpaid
	case dbgen.OrderStatusPaid, dbgen.OrderStatusCompleted,
		dbgen.OrderStatusRefunding, dbgen.OrderStatusRefunded,
		dbgen.OrderStatusPartiallyRefunded, dbgen.OrderStatusChargeback,
		dbgen.OrderStatusChargebackWon, dbgen.OrderStatusChargebackLost:
		// 退款中/已退款的订单，从「这次付款成功了吗」的角度看答案就是「成功了」。
		// 契约的 PaymentState 没有退款档，把它们塞进 expired 会让用户以为钱没到。
		return gen.PaymentStatePaid
	default: // cancelled / expired / failed
		return gen.PaymentStateExpired
	}
}

// ============================================================
// payOrder
// ============================================================

// PayOrder 实现 POST /api/v1/orders/{trade_no}/pay。
func (s *Server) PayOrder(ctx context.Context, req gen.PayOrderRequestObject) (gen.PayOrderResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	if req.Body == nil {
		return gen.PayOrder422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体为空")}, nil
	}

	// 🔴 endpoint 里必须带上 trade_no。幂等指纹只由 (endpoint, body) 组成，
	//    而 body 是 `{"method":"..."}` —— trade_no 是路径参数，不在里面。
	//    少了它，同一个 Idempotency-Key 用在**第二张订单**上会命中第一张的缓存，
	//    直接重放出第一张单的收款地址与金额：用户对着 A 单的地址付了 B 单的钱。
	att, idemResp := s.beginOrderIdempotency(ctx, auth.UserID, "PayOrder:"+req.TradeNo, req.Params.IdempotencyKey, req.Body)
	if idemResp != nil {
		if r, ok := idemResp.(gen.PayOrderResponseObject); ok {
			return r, nil
		}
		return gen.PayOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "幂等判定返回了不匹配的响应类型", nil)}, nil
	}
	if att.Outcome == httpx.OutcomeReplay {
		var resp gen.PayOrder200JSONResponse
		if err := json.Unmarshal(att.Body, &resp); err != nil {
			return gen.PayOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "重放幂等结果失败", err)}, nil
		}
		return resp, nil
	}

	resp, errResp := s.payOrderOnce(ctx, auth.UserID, req.TradeNo, req.Body.Method)
	if errResp != nil {
		return errResp, nil
	}
	if body, err := json.Marshal(resp); err == nil {
		if err := httpx.CompleteIdempotent(ctx, s.db, att.Key, 200, body); err != nil {
			s.logger.ErrorContext(ctx, "幂等结果落盘失败，同键重试将持续 409", "err", err, "trade_no", req.TradeNo)
		}
	}
	return resp, nil
}

func (s *Server) payOrderOnce(ctx context.Context, userID int64, tradeNo string, method gen.PayOrderRequestMethod) (gen.PayOrder200JSONResponse, gen.PayOrderResponseObject) {
	var zero gen.PayOrder200JSONResponse

	set := loadPaymentSettings(ctx, s.db, s.logger)
	row, err := s.db.GetOrderCheckout(ctx, dbgen.GetOrderCheckoutParams{TradeNo: tradeNo, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, gen.PayOrder404JSONResponse{ErrNotFoundJSONResponse: s.orderNotFound(ctx)}
	}
	if err != nil {
		return zero, gen.PayOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取订单失败", err)}
	}

	// 已经发起过支付：直接回既有收银台数据。
	// 🔴 这是重试路径的正确形状 —— 再分配一个地址会撞
	//    `pay_addresses_assigned_order_id_key`（一单一址是数据库拒绝，不是应用自觉），
	//    而那次撞库对用户是一条 500。
	if row.PayAddress != nil && *row.PayAddress != "" {
		return gen.PayOrder200JSONResponse{Data: checkoutView(row, set), Meta: s.meta(ctx)}, nil
	}

	switch row.Status {
	case dbgen.OrderStatusPending:
		// 唯一可以发起支付的状态。
	case dbgen.OrderStatusExpired:
		return zero, gen.PayOrder409JSONResponse{ErrConflictJSONResponse: gen.ErrConflictJSONResponse{
			Body:    s.envelope(ctx, gen.PAYMENTORDEREXPIRED, "订单已过期，请重新下单"),
			Headers: gen.ErrConflictResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
		}}
	default:
		return zero, gen.PayOrder409JSONResponse{ErrConflictJSONResponse: s.conflict(ctx, "订单当前状态不能发起支付")}
	}
	if row.ExpiresAt.Valid && !row.ExpiresAt.Time.After(time.Now()) {
		// 状态还是 pending，但支付窗口已经过了 —— 超时扫描每分钟才跑一次，
		// 这中间的窗口里必须由这里挡住：用一个过期的锁定汇率去收款，汇率敞口由我们承担。
		return zero, gen.PayOrder409JSONResponse{ErrConflictJSONResponse: gen.ErrConflictJSONResponse{
			Body:    s.envelope(ctx, gen.PAYMENTORDEREXPIRED, "订单已过期，请重新下单"),
			Headers: gen.ErrConflictResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
		}}
	}
	if row.AmountDue <= 0 {
		// 折抵 + 余额把应付抵到 0 的订单不该走支付通道。
		// 它需要的是直接开通，而开通路径本轮未实现（见 markOrderPaid）。
		return zero, gen.PayOrder422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "该订单无需支付")}
	}

	if method == gen.Balance {
		return s.payWithBalance(ctx, userID, row, set)
	}
	return s.payWithUSDT(ctx, userID, row, set)
}

// payWithBalance 用钱包余额付掉 amount_due。
func (s *Server) payWithBalance(ctx context.Context, userID int64, row dbgen.GetOrderCheckoutRow, set paymentSettings) (gen.PayOrder200JSONResponse, gen.PayOrderResponseObject) {
	var zero gen.PayOrder200JSONResponse
	var insufficient bool
	var raced bool

	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		// 🔴 顺序不能反：`SpendWalletBalance` 的 ledger_entry_id 是**非空**参数 ——
		//    分录必须先建再扣缓存。只扣缓存不写分录，每日 ReconcileWalletBalances 会报红，
		//    而那时钱已经花出去了（余额的唯一真相是分录，wallet_balances 只是读缓存）。
		entry, err := postLedgerEntry(ctx, q, ledgerEntrySpec{
			EntryNo:     ledgerEntryNo("PAY", row.TradeNo),
			Description: "余额支付订单 " + row.TradeNo,
			RefType:     "order",
			RefID:       row.ID,
			Lines: []ledgerLineSpec{
				// 借 = 正。用户的钱包负债减少（余额 = −SUM(liability:user_wallet)）。
				{AccountCode: acctUserWallet, Currency: "CNY", Amount: row.AmountDue, SubjectID: &userID},
				// 贷 = 负。服务还没交付，先挂递延收入。
				{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: -row.AmountDue},
			},
		})
		if err != nil {
			return err
		}
		if _, err := q.SpendWalletBalance(ctx, dbgen.SpendWalletBalanceParams{
			UserID: userID, AmountCents: row.AmountDue, LedgerEntryID: entry.ID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// 「够不够」写进了 WHERE：0 行 = 余额不足或没有钱包行 → 422。
				// 靠 CHECK (balance >= 0) 兜的话拿到的是一次约束违反（500），翻译不成这句话。
				insufficient = true
			}
			return err
		}
		// 🔴 **不能走 markOrderPaid**：它内部的 transitionOrder 把 0 行当作
		//    「别的路径已经推走了」而返回 nil —— 那条宽容规则是为扫链与 recheck 并发写的，
		//    在这里是致命的：钱已经在上面扣掉了，CAS 却静默失败，于是事务照常提交，
		//    用户被扣了一次款而订单没有推进。并发两次 payOrder 就会扣两次钱只开通一次。
		//    这里必须用严格 CAS：0 行 = 回滚整个事务（分录与扣款一起撤销）→ 409。
		if _, err := q.TransitionOrderStatus(ctx, dbgen.TransitionOrderStatusParams{
			OrderID: row.ID, FromStatus: dbgen.OrderStatusPending, ToStatus: dbgen.OrderStatusPaid,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				raced = true
			}
			return err
		}
		from := dbgen.OrderStatusPending
		reason := "余额支付"
		if _, err := q.InsertOrderTransition(ctx, dbgen.InsertOrderTransitionParams{
			OrderID:    row.ID,
			FromStatus: &from,
			ToStatus:   dbgen.OrderStatusPaid,
			Reason:     &reason,
			Actor:      "user:" + strconv.FormatInt(userID, 10),
		}); err != nil {
			return err
		}
		s.logger.ErrorContext(ctx, "订单已收款并置为 paid，但权益开通（paid → completed）本轮未实现，需要人工开通",
			"metric", "bp_order_paid_not_provisioned", "trade_no", row.TradeNo, "order_id", row.ID)
		return nil
	})
	if insufficient {
		return zero, gen.PayOrder422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "余额不足")}
	}
	if raced {
		return zero, gen.PayOrder409JSONResponse{ErrConflictJSONResponse: s.conflict(ctx, "订单状态已变化，请刷新后重试")}
	}
	if err != nil {
		return zero, gen.PayOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "余额支付失败", err)}
	}

	fresh, err := s.db.GetOrderCheckout(ctx, dbgen.GetOrderCheckoutParams{TradeNo: row.TradeNo, UserID: userID})
	if err != nil {
		return zero, gen.PayOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取收银台状态失败", err)}
	}
	return gen.PayOrder200JSONResponse{Data: checkoutView(fresh, set), Meta: s.meta(ctx)}, nil
}

// payWithUSDT 分配一个专属收款地址、锁汇率、落判据列。
func (s *Server) payWithUSDT(ctx context.Context, userID int64, row dbgen.GetOrderCheckoutRow, set paymentSettings) (gen.PayOrder200JSONResponse, gen.PayOrderResponseObject) {
	var zero gen.PayOrder200JSONResponse

	amountUsdt6, err := quoteUSDT6(row.AmountDue, set.CnyPerUsdtE4, set.FxBufferBps)
	if err != nil {
		return zero, gen.PayOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "计算链上报价失败", err)}
	}

	watchUntil := time.Now().Add(orderPayWindow)
	if row.ExpiresAt.Valid {
		watchUntil = row.ExpiresAt.Time
	}
	watchUntil = watchUntil.Add(addressWatchAfterExpiry)

	var (
		outOfStock bool
		raced      bool
	)
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		addr, err := q.AssignPayAddressToOrder(ctx, dbgen.AssignPayAddressToOrderParams{
			Chain: payChainTron, OrderID: row.ID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// 🔴 地址库存耗尽。**绝不能退化成「复用一个已分配的地址」** ——
				//    一址一单是归属的全部依据（ADR 0012 §5.2）。
				outOfStock = true
			}
			return err
		}
		// 🔴 用 `AttachOrderPaymentQuote` 而**不是** orders.sql 的 `AttachPaymentAddress`：
		//    后者写于 0015 之前，不落 `pay_amount_usdt6`，而 paid / underpaid / 写销的判定
		//    **只读那一列**。用错的后果是 shortfall 恒为 NULL、三档规则全部落空、
		//    订单永远停在 paying —— 而 sqlc 与 go build 一个字都不会说。
		if _, err := q.AttachOrderPaymentQuote(ctx, dbgen.AttachOrderPaymentQuoteParams{
			OrderID:        row.ID,
			Gateway:        gatewayUsdtTrc,
			PayChain:       payChainTron,
			PayAddress:     addr.Address,
			PayAmountUsdt6: amountUsdt6,
			// pay_amount_raw 是**证据不是判据**（ADR 0012 §17.3），落一份同值的原始数量。
			PayAmountRaw:      pgtype.Numeric{Int: big.NewInt(amountUsdt6), Exp: -6, Valid: true},
			CnyPerUsdt:        numericFromE4(set.CnyPerUsdtE4),
			AddressWatchUntil: tstz(watchUntil),
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// CAS 没命中：订单在这几毫秒里被取消或超时了。
				raced = true
			}
			return err
		}

		from := dbgen.OrderStatusPending
		reason := "发起 USDT 支付"
		_, err = q.InsertOrderTransition(ctx, dbgen.InsertOrderTransitionParams{
			OrderID:    row.ID,
			FromStatus: &from,
			ToStatus:   dbgen.OrderStatusPaying,
			Reason:     &reason,
			Actor:      "user:" + strconv.FormatInt(userID, 10),
		})
		return err
	})
	if outOfStock {
		s.logger.ErrorContext(ctx, "收款地址库存耗尽，无法开出收银台（派生是离线批量动作，需要人工补充）",
			"metric", "bp_pay_address_exhausted", "chain", payChainTron, "trade_no", row.TradeNo)
		return zero, gen.PayOrder503JSONResponse{
			ErrDependencyDownJSONResponse: s.dependencyDown(ctx, "收款地址暂时不可用，请稍后重试", errors.New("pay_addresses 库存为 0")),
		}
	}
	if raced {
		return zero, gen.PayOrder409JSONResponse{ErrConflictJSONResponse: s.conflict(ctx, "订单状态已变化，请刷新后重试")}
	}
	if err != nil {
		return zero, gen.PayOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "分配收款地址失败", err)}
	}

	// 低水位观测点。私钥离线、派生是离线批量动作，**必须提前很久看到** ——
	// 等收银台报 503 才知道，就已经有人付不了钱了。
	if avail, err := s.db.CountAvailablePayAddresses(ctx, payChainTron); err == nil && avail < set.AddrLowWater {
		s.logger.WarnContext(ctx, "可用收款地址低于水位线，需要离线派生补充",
			"metric", "bp_pay_address_low_water", "chain", payChainTron,
			"available", avail, "low_water", set.AddrLowWater)
	}

	fresh, err := s.db.GetOrderCheckout(ctx, dbgen.GetOrderCheckoutParams{TradeNo: row.TradeNo, UserID: userID})
	if err != nil {
		return zero, gen.PayOrder500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取收银台状态失败", err)}
	}
	// 地址已经写进订单，收银台数据一律从 GetOrderCheckout 读（单一事实源）。
	return gen.PayOrder200JSONResponse{Data: checkoutView(fresh, set), Meta: s.meta(ctx)}, nil
}

// ============================================================
// getOrderPayment / recheckOrderPayment
// ============================================================

// GetOrderPayment 实现 GET /api/v1/orders/{trade_no}/payment（收银台轮询）。
func (s *Server) GetOrderPayment(ctx context.Context, req gen.GetOrderPaymentRequestObject) (gen.GetOrderPaymentResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	set := loadPaymentSettings(ctx, s.db, s.logger)
	row, err := s.db.GetOrderCheckout(ctx, dbgen.GetOrderCheckoutParams{TradeNo: req.TradeNo, UserID: auth.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.GetOrderPayment404JSONResponse{ErrNotFoundJSONResponse: s.orderNotFound(ctx)}, nil
	}
	if err != nil {
		return gen.GetOrderPayment500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取收银台状态失败", err)}, nil
	}
	return gen.GetOrderPayment200JSONResponse{Data: checkoutView(row, set), Meta: s.meta(ctx)}, nil
}

// RecheckOrderPayment 实现 POST /api/v1/orders/{trade_no}/recheck ——
// 「我已付款，帮我查一下」，page-inventory 称它为「用户侧的最后防线」。
//
// 🔴 与支付回调走**同一段**入账逻辑（processDeposit）：回调不可信，权威金额只来自链上。
// 🔴 冷却窗口内**不返回 429**，直接回上一次的结果 + 200（ADR 0012 §10.4 的裁决原文：
//
//	「给一个害怕的人回 429，是这个按钮所有可能行为里最差的一种」）。
func (s *Server) RecheckOrderPayment(ctx context.Context, req gen.RecheckOrderPaymentRequestObject) (gen.RecheckOrderPaymentResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	set := loadPaymentSettings(ctx, s.db, s.logger)

	row, err := s.db.GetOrderCheckout(ctx, dbgen.GetOrderCheckoutParams{TradeNo: req.TradeNo, UserID: auth.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.RecheckOrderPayment404JSONResponse{ErrNotFoundJSONResponse: s.orderNotFound(ctx)}, nil
	}
	if err != nil {
		return gen.RecheckOrderPayment500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取收银台状态失败", err)}, nil
	}

	scanned, scanErr := s.rescanOrderAddress(ctx, row, set)
	if scanErr != nil {
		// 「已配置但打不通」才是 ErrDependencyDown 的语义。未配置走的是上面那条优雅退出。
		return gen.RecheckOrderPayment503JSONResponse{
			ErrDependencyDownJSONResponse: s.dependencyDown(ctx, "链上查询暂时不可用，请稍后再试", scanErr),
		}, nil
	}
	if scanned {
		// 扫过之后必须重读：状态可能刚刚从 paying 变成 paid。
		if fresh, err := s.db.GetOrderCheckout(ctx, dbgen.GetOrderCheckoutParams{TradeNo: req.TradeNo, UserID: auth.UserID}); err == nil {
			row = fresh
		}
	}
	return gen.RecheckOrderPayment200JSONResponse{Data: checkoutView(row, set), Meta: s.meta(ctx)}, nil
}

// rescanOrderAddress 拿扫描权、打链、入账。返回 (是否真的扫了, 错误)。
func (s *Server) rescanOrderAddress(ctx context.Context, row dbgen.GetOrderCheckoutRow, set paymentSettings) (bool, error) {
	if row.PayAddress == nil || *row.PayAddress == "" {
		// 订单还没分配地址，recheck 无事可做。不是错误 —— 用户点得早而已。
		return false, nil
	}

	// 🔴 冷却做成一次 CAS 而不是「先读 last_scanned_at 再决定」：
	//    两个并发 recheck 在后者下会**双双通过**，于是两次外部调用 ——
	//    而外部配额正是要保护的东西。0 行 = 还在冷却窗口内。
	addr, err := s.db.TryClaimAddressScan(ctx, dbgen.TryClaimAddressScanParams{
		OrderID:  row.ID,
		Cooldown: pgtype.Interval{Microseconds: int64(recheckCooldown / time.Microsecond), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	scanner := s.chainScanner()
	if !scanner.Configured() {
		// ADR 0012 仍是提案，第一阶段不连 RPC。如实退出，**不返回 5xx** ——
		// 用户拿到的是「还没查到」，而不是一个看起来像我们坏了的错误。
		s.logger.WarnContext(ctx, "链上扫描器未配置，recheck 只回既有状态", "scanner", scanner.Name(), "trade_no", row.TradeNo)
		return false, nil
	}

	transfers, err := scanner.Scan(ctx, addr.Chain, addr.Address, lookbackCursor(addr.CursorTs))
	if err != nil {
		return false, err
	}

	var newest int64
	allOK := true
	for _, t := range transfers {
		if err := s.processDepositTx(ctx, depositInput{
			Provider:      "chain_" + addr.Chain,
			EnteredBy:     "scanner",
			Chain:         addr.Chain,
			Transfer:      t,
			ToAddress:     addr.Address,
			Settings:      set,
			ActorOverride: "chain:" + t.TxID,
		}); err != nil {
			// 钱到了但我们没记下来，比拉取失败严重得多。不中断（同一地址上别的转账还能记），
			// 但**不推进游标** —— 下一轮重扫，靠 UNIQUE (provider, external_id) 去重。
			allOK = false
			s.logger.ErrorContext(ctx, "链上到账入账失败（游标不推进，下轮重扫）",
				"trade_no", row.TradeNo, "txid", t.TxID, "err", err)
			continue
		}
		if t.BlockTimeMS > newest {
			newest = t.BlockTimeMS
		}
	}
	if allOK && newest > 0 {
		if err := s.db.UpdatePayAddressCursor(ctx, dbgen.UpdatePayAddressCursorParams{
			PayAddressID: addr.ID, CursorTs: newest,
		}); err != nil {
			s.logger.WarnContext(ctx, "推进扫描游标失败（下一轮重扫同一段，由幂等索引兜底）",
				"pay_address_id", addr.ID, "err", err)
		}
	}
	return true, nil
}

// ============================================================
// handlePaymentNotify（**免登录端点**）
// ============================================================

// HandlePaymentNotify 实现 POST /api/v1/payment/notify/{provider}。
//
// 这是 authmap 里 public 表上的端点：凭据是网关的签名，不是会话。三条纪律：
//
//  1. 🔴 **验签失败 → 401，且默认实现对一切回调都验签失败。**
//     第一阶段没有接任何支付网关（ADR 0012 §1），所以没有任何回调可能是真的。
//     fail-open 的后果是任何人 POST 一个 JSON 就能触发入账路径。
//  2. 🔴 **回调里的金额与状态一个字都不读。** 收到回调只当成一次「去链上看看」的触发信号，
//     权威金额来自 ChainScanner（api-contract §8.1「收到回调后必须反向查单」；
//     先例是 NewAPI 的易支付回调伪造漏洞，pricing-and-plans §4.1 记录在案）。
//  3. **重放静默返回 200。** 幂等靠 `webhook_events` 的 UNIQUE (gateway, event_id)，
//     不靠应用层判断 —— 两个 Cloud Run 实例并发处理同一次重投时后者会双双通过。
//
// 响应体是纯文本（多数网关要求 `success`），**不套统一信封**（契约原文）。
func (s *Server) HandlePaymentNotify(ctx context.Context, req gen.HandlePaymentNotifyRequestObject) (gen.HandlePaymentNotifyResponseObject, error) {
	verifier := s.notifyVerifier()
	log := s.logger.With("provider", req.Provider, "verifier", verifier.Name(),
		"request_id", middleware.RequestIDFrom(ctx))

	body, headers := notifyPayload(ctx, req)

	if !verifier.Configured() {
		log.WarnContext(ctx, "收到支付回调，但没有配置任何网关验签实现，一律拒绝（第一阶段自扫链，不接网关）",
			"metric", "bp_pay_notify_rejected")
		return gen.HandlePaymentNotify401JSONResponse{
			ErrUnauthorizedJSONResponse: s.unauthorized(ctx, gen.PAYMENTSIGNATUREINVALID, "回调验签失败"),
		}, nil
	}
	eventID, err := verifier.Verify(ctx, req.Provider, headers, body)
	if err != nil || eventID == "" {
		log.WarnContext(ctx, "支付回调验签失败", "metric", "bp_pay_notify_rejected", "err", err)
		return gen.HandlePaymentNotify401JSONResponse{
			ErrUnauthorizedJSONResponse: s.unauthorized(ctx, gen.PAYMENTSIGNATUREINVALID, "回调验签失败"),
		}, nil
	}

	set := loadPaymentSettings(ctx, s.db, s.logger)
	evt, err := s.db.InsertWebhookEvent(ctx, dbgen.InsertWebhookEventParams{
		Gateway:     req.Provider,
		EventID:     eventID,
		PayloadHash: fmt.Sprintf("%x", sha256Sum(body)),
		RawBody:     string(body),
		SignatureOk: true,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// DO NOTHING 命中 = 这条事件已经处理过。静默 200，不重复入账。
		log.InfoContext(ctx, "支付回调重复投递，已幂等丢弃", "event_id", eventID)
		return gen.HandlePaymentNotify200TextResponse("success"), nil
	}
	if err != nil {
		return gen.HandlePaymentNotify500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "记录支付回调失败", err)}, nil
	}

	// 🔴 反向查单：回调只提供「去看哪一张单」的线索，其余全部来自链上。
	//    tradeNoHint 是**不可信输入** —— 它只被用来定位订单，不被用来判定任何金额或状态。
	tradeNo := notifyTradeNoHint(req)
	procErr := s.reverseQueryAndProcess(ctx, log, tradeNo, set)

	var errMsg *string
	if procErr != nil {
		m := procErr.Error()
		errMsg = &m
		log.ErrorContext(ctx, "支付回调反查失败", "metric", "bp_pay_notify_recheck_failed", "trade_no", tradeNo, "err", procErr)
	}
	if err := s.db.MarkWebhookProcessed(ctx, dbgen.MarkWebhookProcessedParams{ID: evt.ID, Error: errMsg}); err != nil {
		log.WarnContext(ctx, "回写 webhook_events.processed_at 失败", "event_id", eventID, "err", err)
	}

	// 即使反查失败也回 200：网关的重投不会让链上多一笔钱，
	// 而非 2xx 只会招来更多重投（同一个 event_id 的重投还会被幂等丢弃）。
	// 真正的兜底是每分钟一次的 chain-scan 定时任务。
	return gen.HandlePaymentNotify200TextResponse("success"), nil
}

// reverseQueryAndProcess 按订单号定位订单，然后**只以链上为准**入账。
func (s *Server) reverseQueryAndProcess(ctx context.Context, log *slog.Logger, tradeNo string, set paymentSettings) error {
	if tradeNo == "" {
		return errors.New("回调载荷里没有可用的订单号线索")
	}
	// 这里用 orders.sql 的 GetOrderByTradeNo（不带 user_id）是**正确**的：
	// 回调没有会话主体，"越权读单" 的判据是「谁在读」，而这条路径的读者是系统自己。
	// 用户面的任何按 trade_no 取单一律走 GetUserOrder（见 orderNotFound 的注释）。
	order, err := s.db.GetOrderByTradeNo(ctx, tradeNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("回调声称的订单号不存在: %s", tradeNo)
	}
	if err != nil {
		return err
	}
	if order.PayAddress == nil || *order.PayAddress == "" {
		return fmt.Errorf("订单尚未分配收款地址: %s", tradeNo)
	}

	scanner := s.chainScanner()
	if !scanner.Configured() {
		// 没有链上来源就没有权威金额。**绝不能退回去信回调里的数字。**
		log.WarnContext(ctx, "链上扫描器未配置，回调无法反查，本次不入账", "scanner", scanner.Name(), "trade_no", tradeNo)
		return nil
	}
	chain := payChainTron
	if order.PayChain != nil && *order.PayChain != "" {
		chain = *order.PayChain
	}
	transfers, err := scanner.Scan(ctx, chain, *order.PayAddress, nil)
	if err != nil {
		return err
	}
	for _, t := range transfers {
		if err := s.processDepositTx(ctx, depositInput{
			Provider:      "chain_" + chain,
			EnteredBy:     "scanner",
			Chain:         chain,
			Transfer:      t,
			ToAddress:     *order.PayAddress,
			Settings:      set,
			ActorOverride: "chain:" + t.TxID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// notifyPayload 还原回调的原始载荷与请求头，供验签与取证使用。
//
// 两种 Content-Type 分开处理：
//   - JSON：生成代码把它解成了 map，这里**规范序列化**回去（键按字典序）。
//     它不是逐字节的原文 —— 原文在生成代码里就被 Decode 掉了。
//     对 HMAC-over-raw-body 这类验签算法这是不够的，接第一个真实网关时
//     必须改成在中间件层缓存原始 body（已在 notes 登记）。
//   - form：生成代码调过 r.ParseForm()，所以 r.PostForm 是全的，
//     按键排序拼回 `a=1&b=2` 是**大多数易支付系网关签名的原文形态**。
func notifyPayload(ctx context.Context, req gen.HandlePaymentNotifyRequestObject) ([]byte, http.Header) {
	headers := http.Header{}
	if r, ok := boundRequestFrom(ctx); ok {
		headers = r.Header.Clone()
		if len(r.PostForm) > 0 {
			keys := make([]string, 0, len(r.PostForm))
			for k := range r.PostForm {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var b strings.Builder
			for i, k := range keys {
				if i > 0 {
					b.WriteByte('&')
				}
				b.WriteString(k)
				b.WriteByte('=')
				b.WriteString(r.PostForm.Get(k))
			}
			return []byte(b.String()), headers
		}
	}
	if req.JSONBody != nil {
		if b, err := json.Marshal(map[string]any(*req.JSONBody)); err == nil {
			return b, headers
		}
	}
	return []byte("{}"), headers
}

// notifyTradeNoHint 从回调里挑出订单号线索。
//
// 🔴 只当**线索**用。这个值来自未经我们控制的一方，
// 它唯一被允许影响的是「去查哪一张单」，不允许影响任何金额、状态或归属判定。
func notifyTradeNoHint(req gen.HandlePaymentNotifyRequestObject) string {
	if req.JSONBody == nil {
		return ""
	}
	m := map[string]any(*req.JSONBody)
	// 各家网关的字段名不同，按常见顺序试。
	for _, k := range []string{"trade_no", "out_trade_no", "order_no", "orderNo", "tradeNo"} {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// ============================================================
// 🔴 processDeposit：入账的唯一入口（ADR 0012 §8.4）
// ============================================================

type depositInput struct {
	Provider      string
	EnteredBy     string // 'scanner' | 'admin:<id>'
	Chain         string
	ToAddress     string
	Transfer      ChainTransfer
	Settings      paymentSettings
	ActorOverride string // 写进 order_transitions.actor
}

// errDepositForeignAddress 表示这笔「到账」打的不是我们的地址 —— 回调伪造或网关串号。
var errDepositForeignAddress = errors.New("到账地址不属于本系统")

// processDepositTx 把一笔到账放进一个事务里入账。
//
// 🔴 **审计/账本写入与业务写入同事务，写失败整体回滚**（§8.4 硬约束 3）。
func (s *Server) processDepositTx(ctx context.Context, in depositInput) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		return processDeposit(ctx, q, s.logger, in)
	})
}

// processDeposit 是 §8.4 的五个分支，四条触发路径（webhook / recheck / chain-scan / D6）
// **必须**都调它。不同的触发源，同一段代码 —— 两条路径一旦漂移，漂移的那天就是出事的那天。
func processDeposit(ctx context.Context, q depositQuerier, log *slog.Logger, in depositInput) error {
	t := in.Transfer
	externalID := t.TxID + ":" + strconv.FormatInt(int64(t.LogIndex), 10)
	log = log.With("provider", in.Provider, "external_id", externalID, "to_address", in.ToAddress)

	// ---- 前置：这是不是我们的地址？----
	//
	// §8.4 的伪码是「先 INSERT 抢幂等锁，再归属」。这里在 INSERT **之前**多做一次
	// `GetPayAddressByAddress`，是一处有意的偏差，理由：
	//   · 「先插入」那条规则保护的是**归属**这段可能失败、可能重试的逻辑（它要读 orders 并加锁）；
	//     而「这是不是我们的地址」是一次带索引的确定查表，不存在并发下双双通过的问题。
	//   · 反过来，如果先插入，一次伪造的回调就能在 `payments` 里留下一条
	//     order_id 为 NULL 的行 —— 而那张表的 `payments_unmatched_idx` 是**人工队列**，
	//     它的正常结果集应当是空的。让任何人都能往人工队列里塞行，
	//     等于把「每天看一眼」的成本交给攻击者定。
	addr, err := q.GetPayAddressByAddress(ctx, dbgen.GetPayAddressByAddressParams{
		Chain: in.Chain, Address: in.ToAddress,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		log.ErrorContext(ctx, "到账地址不属于本系统，不入账（疑似回调伪造或网关串号）",
			"metric", "bp_pay_foreign_address")
		return errDepositForeignAddress
	}
	if err != nil {
		return err
	}

	raw := t.Raw
	if len(raw) == 0 {
		// payments.raw 是 NOT NULL 且刻意如此：入账争议（用户说打了、我们说没收到）
		// 只能靠原文解决。扫描器没给原文时至少把我们看到的字段落下来。
		raw = mustMarshalFallbackRaw(t)
	}

	// ---- 分支 0：抢幂等锁。**这是全系统唯一的入账锁。** ----
	state := dbgen.PaymentStateConfirming
	if t.Solidified {
		// TRON 的最终性是「固化」不是 N 个确认（§10.5）。confirmations 只用于展示。
		state = dbgen.PaymentStatePaid
	}
	chain, txid, logIndex := in.Chain, t.TxID, t.LogIndex
	from, to, amount := t.FromAddress, in.ToAddress, t.AmountUSDT6
	payment, err := q.InsertPaymentIfNew(ctx, dbgen.InsertPaymentIfNewParams{
		Provider:      in.Provider,
		ExternalID:    externalID,
		EnteredBy:     in.EnteredBy,
		Chain:         &chain,
		Txid:          &txid,
		LogIndex:      &logIndex,
		FromAddress:   &from,
		ToAddress:     &to,
		AmountUsdt6:   &amount,
		State:         state,
		Confirmations: t.Confirmations,
		// aml_verdict 留 NULL = **尚未判定**。这不是遗漏：AML Layer 1（付款方地址筛查）
		// 本轮没有实现，写 'clean' 会是一句谎话。SumAddressReceipts 与收银台那条 LATERAL
		// 都用 `coalesce(aml_verdict,'clean') <> 'blacklisted'`，所以 NULL 会被正确地当成可计入 ——
		// 写成 `aml_verdict <> 'blacklisted'` 才是把用户的钱从页面上抹掉的那种写法。
		Raw: raw,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return handleAlreadyProcessed(ctx, q, log, in, externalID)
	}
	if err != nil {
		return err
	}

	// ---- 归属：只看地址，一次确定的查表；FOR UPDATE 让并发到账排队 ----
	order, err := q.GetOrderByPayAddressForUpdate(ctx, in.ToAddress)
	if errors.Is(err, pgx.ErrNoRows) {
		// 分支 ②：是我们的地址，但订单侧对不上（地址还没分配出去，或者订单被删过）。
		// **钱照收**，只是暂时找不到人 —— 进人工队列。
		quarantined := "quarantined"
		if _, err := q.AttributePayment(ctx, dbgen.AttributePaymentParams{
			PaymentID: payment.ID, State: state, Confirmations: t.Confirmations,
			AmlVerdict: &quarantined,
		}); err != nil {
			return err
		}
		log.ErrorContext(ctx, "收到打到本系统地址、但归属不到订单的款项，已进人工队列",
			"metric", "bp_pay_unmatched", "pay_address_id", addr.ID, "amount_usdt6", t.AmountUSDT6)
		return nil
	}
	if err != nil {
		return err
	}

	// 锁定汇率折算成分。fx 在 payOrder 才落，缺失时按当前配置折算并记 Warn ——
	// 这个数只写进 payments.amount_cny_cents（记录用），不参与 paid / underpaid 判定
	// （判定只在 1e-6 USDT 的整数域做）。
	fxE4, ok := numericToE4(order.FxUsdtPerCny)
	if !ok || fxE4 <= 0 {
		fxE4 = in.Settings.CnyPerUsdtE4
		log.WarnContext(ctx, "订单没有锁定汇率，本笔到账按当前配置折算入账记录", "trade_no", order.TradeNo)
	}
	cents := usdt6ToCents(t.AmountUSDT6, fxE4)

	// 收款凭证（§17.6(b) 的跨币种桥接）：USDT 与 CNY 两条腿各自配平，
	// 两者累积的净额就是「以 CNY 标价、以 USDT 收款」的汇率敞口。
	entry, err := postLedgerEntry(ctx, q, ledgerEntrySpec{
		EntryNo:     ledgerEntryNo("RCV", externalID),
		Description: "链上到账 " + externalID,
		RefType:     "order",
		RefID:       order.ID,
		Lines: []ledgerLineSpec{
			{AccountCode: acctTronPool, Currency: "USDT", Amount: t.AmountUSDT6, SubjectID: &addr.ID},
			{AccountCode: acctFxClearingUSDT, Currency: "USDT", Amount: -t.AmountUSDT6},
			{AccountCode: acctFxClearingCNY, Currency: "CNY", Amount: cents},
			{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: -cents},
		},
	})
	if err != nil {
		return err
	}

	if _, err := q.AttributePayment(ctx, dbgen.AttributePaymentParams{
		PaymentID:      payment.ID,
		OrderID:        &order.ID,
		UserID:         &order.UserID,
		AmountCnyCents: &cents,
		State:          state,
		Confirmations:  t.Confirmations,
		LedgerEntryID:  &entry.ID,
	}); err != nil {
		return err
	}

	// 付款方地址只写一次（coalesce 保留首笔）：ADR 0013 §9 的失效条件靠这一列执行，
	// 而**扫链是唯一看得见付款方的地方**。
	if t.FromAddress != "" {
		if err := q.RecordOrderPayerAddress(ctx, dbgen.RecordOrderPayerAddressParams{
			OrderID: order.ID, PayFromAddress: t.FromAddress, GatewayRef: &txid,
		}); err != nil {
			return err
		}
	}

	return settleDeposit(ctx, q, log, in, order, entry.ID)
}

// handleAlreadyProcessed 是 §8.4 分支 ①。
func handleAlreadyProcessed(ctx context.Context, q depositQuerier, log *slog.Logger, in depositInput, externalID string) error {
	existing, err := q.GetPaymentByExternalID(ctx, dbgen.GetPaymentByExternalIDParams{
		Provider: in.Provider, ExternalID: externalID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if existing.ID == 0 {
		// 撞了唯一索引却读不回来：只可能是同一事务外的并发删除，或者索引与查询口径不一致。
		// 不再往下走（后面的追认分支需要那一行），但也不失败 —— 钱本来就没被重复入账。
		log.WarnContext(ctx, "撞到入账幂等锁但读不回既有流水，跳过追认分支")
		return nil
	}
	log.InfoContext(ctx, "这笔到账已经入过账，幂等丢弃（游标回看 10 分钟就是靠它兜底）",
		"payment_id", existing.ID, "entered_by", existing.EnteredBy)

	if in.EnteredBy != "scanner" {
		return nil
	}
	// 手工录入（D6）之后扫描又扫到同一笔钱：把 entered_by 追成 'admin:<id>+scanner'。
	// WHERE 里的两个 LIKE 让这条 UPDATE 幂等，0 行 = 不需要（本来就是 scanner 录的，或已追加过）。
	row, err := q.AppendScannerToPaymentEntry(ctx, dbgen.AppendScannerToPaymentEntryParams{
		Provider: in.Provider, ExternalID: externalID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// 🔴 **这里缺一条 §16.2 的冲正分录，本轮没有写，理由必须留在这里：**
	//
	//	§16.2 的原文是「Dr asset:crypto:tron:pool / Cr asset:manual_reconcile」，
	//	但 0015 的科目 seed 把这两个科目定成了**不同币种**（pool = USDT，manual_reconcile = CNY），
	//	而 §17.6(d) 的每日断言按 (entry_id, currency) 分组查借贷是否相等 ——
	//	按字面写这两条腿，写出来的分录当天就会报红。
	//	正确的形状应当是走 §17.6(b) 的 fx_clearing 桥接（四条腿），但那需要知道
	//	D6 当初那条分录长什么样，而**D6（手工录入）在本仓库还不存在**，
	//	所以这条冲正今天没有确定的对手方。
	//
	//	现状是安全的：D6 不存在 ⇒ 没有任何 payments 行的 entered_by 会是 'admin:%' ⇒
	//	这个分支不可达。实现 D6 的那一次**必须**同时把这条冲正补上，
	//	否则 `asset:manual_reconcile` 会永久挂账、天天报红 ——
	//	而一个天天报红的告警等于没有告警，它偏偏又是把「全系统最大的内部欺诈面」
	//	变成可观测数字的唯一手段。
	log.ErrorContext(ctx, "扫描追认了一笔手工录入的到账，但 §16.2 的冲正分录尚未实现（D6 未落地，冲正无对手方）",
		"metric", "bp_pay_manual_reconcile_pending", "payment_id", row.ID, "entered_by", row.EnteredBy)
	return nil
}

// settleDeposit 是 §8.4 的分支 ③④⑤：按累计额重新评估三档规则并推进订单状态。
func settleDeposit(
	ctx context.Context,
	q depositQuerier,
	log *slog.Logger,
	in depositInput,
	order dbgen.GetOrderByPayAddressForUpdateRow,
	entryID int64,
) error {
	// 🔴 累计**按地址不按 order_id**（§6.3）：一笔迟到的、还没归属到订单的到账，
	//    按地址算得进、按 order_id 算不进 —— 而用户此刻正盯着「还差 Y」那个数字。
	sum, err := q.SumAddressReceipts(ctx, in.ToAddress)
	if err != nil {
		return err
	}
	var expected int64
	if order.PayAmountUsdt6 != nil {
		expected = *order.PayAmountUsdt6
	}
	shortfall := expected - sum.ReceivedUsdt6

	actor := in.ActorOverride
	if actor == "" {
		actor = "system"
	}

	switch order.Status {
	case dbgen.OrderStatusPaying, dbgen.OrderStatusUnderpaid:
		if expected <= 0 {
			// 订单没有应收金额（0015 的 pay_amount_usdt6 是判定的唯一依据）。
			// 钱已经记下来了，但没人能判断够不够 —— 必须人工看。
			log.ErrorContext(ctx, "订单缺少应收金额，到账已记录但无法判定是否付清（需人工处理）",
				"metric", "bp_pay_no_expected_amount", "trade_no", order.TradeNo)
			return nil
		}
		switch {
		case shortfall <= in.Settings.WriteoffUsdt6:
			// A 档：够了，或者差额小到不值得去要。
			// **我们不去要一笔要不来的钱** —— 补足会被再扣一次同样的提币费，净到账 0（§6.1）。
			if shortfall > 0 {
				if err := postShortfallWriteoff(ctx, q, order, shortfall, in.Settings.CnyPerUsdtE4); err != nil {
					return err
				}
				log.WarnContext(ctx, "少付在写销档内，已自动写销并按已付清处理",
					"metric", "bp_pay_shortfall_writeoff", "trade_no", order.TradeNo,
					"shortfall_usdt6", shortfall)
			}
			// 一次转账就多付了（§6.2）：≤ 0.01 USDT 是取整误差量级，不值得为它开一条路径；
			// 超过就必须入余额。**漏掉这一档，多付的钱会停在我们的地址上没有任何记录指向用户** ——
			// 而余额只可消费不可提现，入账在资金合规上是安全的。
			if excess := -shortfall; excess > usdt6OverpayIgnore {
				if err := creditWalletFromDeposit(ctx, q, log, in, order, entryID, excess,
					"超额付款，超出部分入余额"); err != nil {
					return err
				}
			}
			// 🔴 `amount_paid` 必须在这里涨。它是**四个下游的共同基数**：
			//    退款额（GetRefundBasis 的 segment_value）、升级折抵（GetSubscriptionSource 的
			//    v_source）、佣金追回的分母、以及营收看板。留在 0 的后果不是报错而是四处静默归零 ——
			//    用户退款时会拿到「已经没有可退金额」的 422，而他明明付过钱。
			//
			//    这条纪律此前只写在 D6 手工标记路径上（admin_orders.go），**正常的链上收款路径漏了**。
			//    金额取 `order.AmountDue`（本单的应付人民币）而不是把到账 USDT 再折一次：
			//    折算会引入第二次舍入，而写销档里的差额已经作为我们的费用单独入账了
			//    （postShortfallWriteoff），不该再从用户的已付额里扣一遍。
			if _, err := q.RecordOrderPayment(ctx, dbgen.RecordOrderPaymentParams{
				ID: order.ID,
				// pay_amount_received 是 numeric(38,18) 的 USDT 数量（0006 的量纲铁律：
				// 它不参与任何货币再计算）。累计到账是 1e-6 USDT 的整数，指数 -6 精确无损。
				PayAmountReceived: pgtype.Numeric{Int: big.NewInt(sum.ReceivedUsdt6), Exp: -6, Valid: true},
				AmountPaid:        order.AmountDue,
				GatewayRef:        &in.Transfer.TxID,
			}); err != nil {
				return err
			}
			return markOrderPaid(ctx, q, log, order.ID, order.TradeNo, order.Status, actor,
				fmt.Sprintf("链上到账 %d（差额 %d，写销档）", sum.ReceivedUsdt6, shortfall))
		case shortfall <= in.Settings.ReviewUsdt6:
			// B 档：交给人。页面文案明写「无需再次转账」（收银台的 underpaid 分支）。
			log.ErrorContext(ctx, "少付落在人工复核档，已进人工队列（用户无需再次转账）",
				"metric", "bp_pay_underpaid_review", "trade_no", order.TradeNo, "shortfall_usdt6", shortfall)
			return transitionOrder(ctx, q, order.ID, order.Status, dbgen.OrderStatusUnderpaid, actor,
				fmt.Sprintf("少付 %d（人工复核档）", shortfall))
		default:
			// C 档：提示向**同一地址**补足。归属只看地址，所以补足无歧义。
			log.WarnContext(ctx, "少付超过人工复核档，提示用户向同一地址补足",
				"metric", "bp_pay_underpaid_topup", "trade_no", order.TradeNo, "shortfall_usdt6", shortfall)
			return transitionOrder(ctx, q, order.ID, order.Status, dbgen.OrderStatusUnderpaid, actor,
				fmt.Sprintf("少付 %d（需补足）", shortfall))
		}

	case dbgen.OrderStatusExpired, dbgen.OrderStatusCancelled:
		// 分支 ④：**不改订单状态、不回改成 paid**（§7.3）。
		// `paid → completed` 是唯一的权益发放路径；把过期单改回 paid 等于用一个已经过期的汇率开通，
		// 汇率敞口由我们承担。钱按到账时刻的汇率折算入余额。
		// **不做这一条，用户第一次付款的钱就真的进黑洞。**
		return creditWalletFromDeposit(ctx, q, log, in, order, entryID, in.Transfer.AmountUSDT6,
			"订单已过期，款项入余额")

	case dbgen.OrderStatusPaid, dbgen.OrderStatusCompleted:
		// 分支 ⑤：超额。同样入余额 + 发信（§6.2）。
		if shortfall >= 0 {
			// 没有超额（比如同一笔钱被重扫但订单早已 paid），不重复入余额。
			return nil
		}
		// 订单在这笔到账**之前**就已经付清，所以整笔都是超额。
		return creditWalletFromDeposit(ctx, q, log, in, order, entryID, in.Transfer.AmountUSDT6,
			"超额付款，超出部分入余额")

	default:
		log.WarnContext(ctx, "到账落在一个不处理的订单状态上，只记流水",
			"trade_no", order.TradeNo, "status", string(order.Status))
		return nil
	}
}

// creditWalletFromDeposit 把一笔到账折成余额。
//
// ⚠️ 余额**只可消费不可提现**（product-brief §6）。这条约束数据库强制不了 ——
//
//	它靠的是 ledger_accounts 里不存在 `asset:bank ← liability:user_wallet` 这条路径，
//	以及没有人写提现代码。新增任何出金路径前先读 ADR 0013 §4.3 边界 1。
//
// TODO(P1): 契约要求同时发一封「已入账为余额，可用于重新下单」的邮件。
// 邮件走 email_outbox + RunMailSendTask，而那张表的写入查询不在本轮范围 ——
// 目前只记 Warn，用户拿不到通知，只能自己打开面板看到余额。已在 notes 登记。
func creditWalletFromDeposit(
	ctx context.Context,
	q depositQuerier,
	log *slog.Logger,
	in depositInput,
	order dbgen.GetOrderByPayAddressForUpdateRow,
	entryID int64,
	amountUsdt6 int64,
	reason string,
) error {
	cents := usdt6ToCents(amountUsdt6, in.Settings.CnyPerUsdtE4)
	if cents <= 0 {
		return nil
	}
	// 这笔钱在上面的收款凭证里已经贷记了 `liability:deferred_revenue`；
	// 这里把它从递延收入搬到用户余额，两条腿都是 CNY，自成一条平的分录。
	if _, err := postLedgerEntry(ctx, q, ledgerEntrySpec{
		EntryNo:     ledgerEntryNo("BAL", order.TradeNo+":"+strconv.FormatInt(entryID, 10)),
		Description: reason + " " + order.TradeNo,
		RefType:     "order",
		RefID:       order.ID,
		Lines: []ledgerLineSpec{
			{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: cents},
			{AccountCode: acctUserWallet, Currency: "CNY", Amount: -cents, SubjectID: &order.UserID},
		},
	}); err != nil {
		return err
	}
	// UpsertWalletBalance 的 balance 参数是**增量**不是绝对值（ON CONFLICT 里是 `+ EXCLUDED.balance`）。
	if _, err := q.UpsertWalletBalance(ctx, dbgen.UpsertWalletBalanceParams{
		UserID: order.UserID, Currency: "CNY", Balance: cents, LastEntryID: &entryID,
	}); err != nil {
		return err
	}
	log.WarnContext(ctx, reason+"（邮件通知尚未实现，用户需自行查看余额）",
		"metric", "bp_pay_credited_to_wallet", "trade_no", order.TradeNo,
		"user_id", order.UserID, "amount_cents", cents)
	return nil
}

// postShortfallWriteoff 记 A 档写销的损失，并把递延收入补齐到订单的应收口径。
func postShortfallWriteoff(ctx context.Context, q depositQuerier, order dbgen.GetOrderByPayAddressForUpdateRow, shortfallUsdt6, fxE4 int64) error {
	cents := usdt6ToCents(shortfallUsdt6, fxE4)
	_, err := postLedgerEntry(ctx, q, ledgerEntrySpec{
		EntryNo:     ledgerEntryNo("WOF", order.TradeNo),
		Description: "少付自动写销 " + order.TradeNo,
		RefType:     "order",
		RefID:       order.ID,
		Lines: []ledgerLineSpec{
			// 差额是 1e-6 USDT 量纲的链上少收，不是人民币费用（0015 的科目注释）。
			{AccountCode: acctShortfall, Currency: "USDT", Amount: shortfallUsdt6},
			{AccountCode: acctFxClearingUSDT, Currency: "USDT", Amount: -shortfallUsdt6},
			{AccountCode: acctFxClearingCNY, Currency: "CNY", Amount: cents},
			{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: -cents},
		},
	})
	return err
}

// transitionOrder 走 DB 层 CAS 并同事务写审计。
//
// 🔴 0 行 = 并发冲突或非法迁移，**必须当作失败处理**，不得退化成 `UPDATE ... WHERE id = $1`。
func transitionOrder(ctx context.Context, q depositQuerier, orderID int64, from, to dbgen.OrderStatus, actor, reason string) error {
	if from == to {
		return nil
	}
	if _, err := q.TransitionOrderStatus(ctx, dbgen.TransitionOrderStatusParams{
		OrderID: orderID, FromStatus: from, ToStatus: to,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 别的路径已经把它推走了（扫链与 recheck 并发）。这是**设计内的常态**，不是错误。
			return nil
		}
		return err
	}
	fromCopy := from
	_, err := q.InsertOrderTransition(ctx, dbgen.InsertOrderTransitionParams{
		OrderID:    orderID,
		FromStatus: &fromCopy,
		ToStatus:   to,
		Reason:     &reason,
		Actor:      actor,
	})
	return err
}

// markOrderPaid 把订单推进到 `paid`。
//
// 🔴 **本轮到此为止：`paid → completed` 的权益开通没有实现。**
//
//	契约与 ADR 0012 §8.4 硬约束 2 要求开通是一个事务：写配额 + 写到期 + 改状态 + bump user_rev。
//	写配额与到期有现成的查询（users.sql 的 ApplyUserEntitlement / AddUserTransferQuota，
//	user_rev 由 0012 的触发器自动 bump），但**缺一条查询**：首次开通时的
//	`reset_at` / `subscription_anchor_at` / `covers_from` / `covers_to` 计算。
//	AdvanceUserResetCycle 里那段 `CASE reset_traffic_method ...` 是这套口径的唯一实现，
//	而在 Go 侧再抄一份就是本仓库反复警告的那种漂移（两处各写一遍，改一处不改另一处，
//	现象是「有些用户的重置日差一个月」且没有任何报错）。
//
//	所以这里选择**响亮地停在 paid**：钱已收到、状态已推进、审计已写、账已平，
//	差的是发权利那一步，而它每一次都会打出一条 ERROR。
//	这与 task.go 的 runChainScan「只记录收到了钱，不开通任何权利」是同一条分工，
//	方向也一样安全：多记一笔流水没有副作用，先开通后记账才会在重投时开通两次。
//
// TODO(P1): 补一条「首次开通」的查询（在 SQL 里算 covers_from/covers_to/reset_at），
// 然后在这里同事务调 ApplyUserEntitlement + AddUserTransferQuota + CreateCommission
// + IncrementCouponUse 之外的开通动作，并把状态推到 completed。
func markOrderPaid(ctx context.Context, q depositQuerier, log *slog.Logger, orderID int64, tradeNo string, from dbgen.OrderStatus, actor, reason string) error {
	if err := transitionOrder(ctx, q, orderID, from, dbgen.OrderStatusPaid, actor, reason); err != nil {
		return err
	}
	log.ErrorContext(ctx, "订单已收款并置为 paid，但权益开通（paid → completed）本轮未实现，需要人工开通",
		"metric", "bp_order_paid_not_provisioned", "trade_no", tradeNo, "order_id", orderID)
	return nil
}

// ============================================================
// 复式账：一条分录 + N 条腿
// ============================================================

// 科目 code（0015 的 seed 逐字）。写成常量而不是散在调用点，
// 是因为拼错一个 code 的现象是运行时「科目不存在」，而它只会在第一笔真钱上出现。
const (
	acctTronPool        = "asset:crypto:tron:pool"
	acctFxClearingUSDT  = "equity:fx_clearing:USDT"
	acctFxClearingCNY   = "equity:fx_clearing:CNY"
	acctDeferredRevenue = "liability:deferred_revenue"
	acctUserWallet      = "liability:user_wallet"
	acctShortfall       = "expense:payment_shortfall"
)

// ---- 钱包余额的锁定与退回（orders.amount_balance 的真实资金动作）----
//
// 🔴 `orders.amount_balance` 从前是一个**纯记账字段**：它被减进 `amount_due`，
//    但对应的钱从来没有离开过钱包。后果是同一笔余额可以被无限次重复抵扣，
//    而且**没有任何自动信号会发现它** —— `ReconcileWalletBalances` 比的是
//    「分录聚合 vs wallet_balances 缓存」，两边都没扣，所以对账始终一致。
//
// 为什么锁在**下单**而不是**支付**：链上支付一旦到账就不能再失败。
// 若把扣款推迟到 settleDeposit，用户在下单与到账之间把余额花在别处，
// 我们就会拿着一笔已经到账的链上款却扣不动余额 —— 那一刻没有正确的处置方式。
// 下单时扣则失败得起：`SpendWalletBalance` 把「够不够」写进 WHERE，
// 0 行就是一次干净的 422，订单根本不会被创建。这同时封住并发下单
// 各自拿到全额余额的超卖（两条并发事务只有一条能让 `balance >= amount` 成立）。
//
// 代价：下单后未支付的订单会占住余额，直到用户取消或支付窗口到期。
// 两条释放路径都在下面 releaseOrderBalance 里，且都与状态迁移同事务。

// errBalanceReserveFailed 表示钱包余额不足以覆盖本单要抵扣的部分。
var errBalanceReserveFailed = errors.New("余额不足")

// reserveOrderBalance 在下单事务里把 amount_balance 真的从钱包扣走并落一条分录。
func reserveOrderBalance(ctx context.Context, q createOrderWriter, userID, orderID int64, tradeNo string, amount int64) error {
	if amount <= 0 {
		return nil
	}
	// 顺序与 payWithBalance 一致：分录先建，再扣缓存 —— SpendWalletBalance 的
	// ledger_entry_id 是非空参数，且余额的唯一真相是分录。
	entry, err := postLedgerEntry(ctx, q, ledgerEntrySpec{
		EntryNo:     ledgerEntryNo("BALHOLD", tradeNo),
		Description: "下单锁定钱包余额 " + tradeNo,
		RefType:     "order",
		RefID:       orderID,
		Lines: []ledgerLineSpec{
			// 借 = 正：用户的钱包负债减少（余额 = −SUM(liability:user_wallet)）。
			{AccountCode: acctUserWallet, Currency: "CNY", Amount: amount, SubjectID: &userID},
			// 贷 = 负：服务还没交付，先挂递延收入。与 payWithBalance 同形。
			{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: -amount},
		},
	})
	if err != nil {
		return err
	}
	if _, err := q.SpendWalletBalance(ctx, dbgen.SpendWalletBalanceParams{
		UserID: userID, AmountCents: amount, LedgerEntryID: entry.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errBalanceReserveFailed
		}
		return err
	}
	return nil
}

// balanceReleaser 是 releaseOrderBalance 需要的能力集合。
// 取消（CancelOrder）与超时关闭（runOrderTimeout）两条路径共用它。
type balanceReleaser interface {
	ledgerQuerier
	UpsertWalletBalance(ctx context.Context, arg dbgen.UpsertWalletBalanceParams) (dbgen.WalletBalance, error)
}

// releaseOrderBalance 把 reserveOrderBalance 锁定的余额原样退回，分录方向完全相反。
//
// **必须与订单状态迁移同事务**：先迁移后退款而中间进程死掉，用户的钱就没了，
// 而且订单已经是 cancelled/expired，没有任何路径会再回来退它。
func releaseOrderBalance(ctx context.Context, q balanceReleaser, userID, orderID int64, tradeNo string, amount int64, why string) error {
	if amount <= 0 {
		return nil
	}
	entry, err := postLedgerEntry(ctx, q, ledgerEntrySpec{
		EntryNo:     ledgerEntryNo("BALREL", tradeNo),
		Description: why + "，退回锁定的钱包余额 " + tradeNo,
		RefType:     "order",
		RefID:       orderID,
		Lines: []ledgerLineSpec{
			{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: amount},
			{AccountCode: acctUserWallet, Currency: "CNY", Amount: -amount, SubjectID: &userID},
		},
	})
	if err != nil {
		return err
	}
	// 退回走 Upsert 而不是 SpendWalletBalance 的反向：钱包行可能已经被清成 0，
	// 也可能压根不存在（余额全部锁在这张单上），Upsert 两种情况都对。
	_, err = q.UpsertWalletBalance(ctx, dbgen.UpsertWalletBalanceParams{
		UserID: userID, Currency: "CNY", Balance: amount, LastEntryID: &entry.ID,
	})
	return err
}

type ledgerLineSpec struct {
	AccountCode string
	Currency    string
	Amount      int64 // 有符号最小货币单位：正 = 借 Dr，负 = 贷 Cr
	SubjectID   *int64
}

type ledgerEntrySpec struct {
	EntryNo     string
	Description string
	RefType     string
	RefID       int64
	Lines       []ledgerLineSpec
}

// postLedgerEntry 写一条分录及其各条腿，并在写之前断言借贷相等。
//
// 🔴 `SUM(amount) = 0` 这条不变量在 schema 层表达不出来（跨行），只能靠 service 层 +
// `FindUnbalancedLedgerEntries` 巡检。这里的断言让不平的分录**根本写不进去**，
// 而不是等第二天巡检报红 —— 那时它已经和真钱混在一起了。
//
// 按币种分组断言：`ledger_accounts.currency` 是 per-account 的，
// 跨币种的分录（USDT 收款对 CNY 递延收入）靠 §17.6(b) 的 fx_clearing 两条腿桥接，
// 每个币种各自配平。整体求和为 0 但分币种不平的分录会让每日断言天天报红。
func postLedgerEntry(ctx context.Context, q ledgerQuerier, spec ledgerEntrySpec) (dbgen.LedgerEntry, error) {
	sums := map[string]int64{}
	for _, l := range spec.Lines {
		sums[l.Currency] += l.Amount
	}
	for cur, sum := range sums {
		if sum != 0 {
			return dbgen.LedgerEntry{}, fmt.Errorf("分录 %s 在币种 %s 上借贷不平（差额 %d）", spec.EntryNo, cur, sum)
		}
	}

	refType := spec.RefType
	refID := spec.RefID
	entry, err := q.CreateLedgerEntry(ctx, dbgen.CreateLedgerEntryParams{
		EntryNo:     spec.EntryNo,
		Description: spec.Description,
		RefType:     &refType,
		RefID:       &refID,
	})
	if err != nil {
		return dbgen.LedgerEntry{}, err
	}
	for _, l := range spec.Lines {
		acct, err := q.GetLedgerAccountByCode(ctx, l.AccountCode)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// 0015 的 seed 缺了这个科目。**必须失败**：让分录写一半比写不进去更糟。
				return dbgen.LedgerEntry{}, fmt.Errorf("账本科目不存在: %s", l.AccountCode)
			}
			return dbgen.LedgerEntry{}, err
		}
		if _, err := q.CreateLedgerLine(ctx, dbgen.CreateLedgerLineParams{
			EntryID:   entry.ID,
			AccountID: acct.ID,
			SubjectID: l.SubjectID,
			Amount:    l.Amount,
			Currency:  l.Currency,
		}); err != nil {
			return dbgen.LedgerEntry{}, err
		}
	}
	return entry, nil
}

// ledgerEntryNo 生成 `ledger_entries.entry_no`（UNIQUE）。
//
// 带上业务键（单号 / external_id）而不是纯随机：entry_no 是人在对账时用来
// 「从一条分录找回它对应的那件事」的唯一线索，随机串会把那次查找变成一次全表扫。
func ledgerEntryNo(prefix, key string) string {
	return fmt.Sprintf("%s-%s-%s", prefix, time.Now().UTC().Format("20060102150405.000"), key)
}

// sha256Sum 给 webhook_events.payload_hash 用。
// 存哈希而不是只存原文：`raw_body` 已经有原文了，哈希的用途是让
// 「同一份载荷被投递了几次」在不解析 JSON 的情况下可比 —— 而重放防护的键是 event_id，
// 这一列只是取证时的旁证。
func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}
