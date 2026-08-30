package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
)

// 订单与收款的测试。
//
// 与 task_test.go / catalog_test.go 同一条纪律：测**纯函数**与**吃窄接口的自由函数**，
// 不测 handler 方法（Server.db 是具体类型 *store.Store，塞不了假实现）。
// handler 这一层只断言「确实实现了」—— Server 内嵌 Unimplemented，
// 漏实现不会在编译期暴露，只会悄悄退回 501。
//
// 本文件里带 🔴 的用例都是「不这么做会**静默**出错」的那一类，其中两条是任务书点名的：
//   - TestAssertPriceFloor…：1.20× 地板断言（定价修订 A7）
//   - TestProcessDeposit…ChainAmount…：回调反查（api-contract §8.1，回调不可信）

// ============================================================
// 本组 operation 必须真的落在 Server 上
// ============================================================

func TestCatalogOrderOperationsAreImplemented(t *testing.T) {
	var s any = &Server{}
	if _, ok := s.(interface {
		ListPlans(context.Context, gen.ListPlansRequestObject) (gen.ListPlansResponseObject, error)
		ListNotices(context.Context, gen.ListNoticesRequestObject) (gen.ListNoticesResponseObject, error)
		VerifyCoupon(context.Context, gen.VerifyCouponRequestObject) (gen.VerifyCouponResponseObject, error)
		CreateOrder(context.Context, gen.CreateOrderRequestObject) (gen.CreateOrderResponseObject, error)
		GetOrder(context.Context, gen.GetOrderRequestObject) (gen.GetOrderResponseObject, error)
		ListOrders(context.Context, gen.ListOrdersRequestObject) (gen.ListOrdersResponseObject, error)
		CancelOrder(context.Context, gen.CancelOrderRequestObject) (gen.CancelOrderResponseObject, error)
		PayOrder(context.Context, gen.PayOrderRequestObject) (gen.PayOrderResponseObject, error)
		GetOrderPayment(context.Context, gen.GetOrderPaymentRequestObject) (gen.GetOrderPaymentResponseObject, error)
		RecheckOrderPayment(context.Context, gen.RecheckOrderPaymentRequestObject) (gen.RecheckOrderPaymentResponseObject, error)
		HandlePaymentNotify(context.Context, gen.HandlePaymentNotifyRequestObject) (gen.HandlePaymentNotifyResponseObject, error)
	}); !ok {
		t.Fatal("套餐/订单/支付这一组里有 operation 没有被 Server 覆盖，仍落在 Unimplemented 的 501 上")
	}
}

// handlePaymentNotify 是**免登录**端点：它必须在 PublicOperations 里，
// 否则 deny-by-default 的装配会给它挂上会话鉴权 —— 而支付网关没有会话，
// 现象是「所有回调都 401」，且只在真的接了网关那天才会被发现。
func TestHandlePaymentNotifyIsPublic(t *testing.T) {
	if !PublicOperations["HandlePaymentNotify"] {
		t.Fatal("HandlePaymentNotify 必须在免登录表里（凭据是网关签名，不是会话）")
	}
	for _, name := range []string{"CreateOrder", "PayOrder", "GetOrder", "ListOrders", "CancelOrder",
		"GetOrderPayment", "RecheckOrderPayment", "ListPlans", "ListNotices", "VerifyCoupon"} {
		if PublicOperations[name] {
			t.Fatalf("%s 必须要求登录（它读/写的是某个人的钱和订单）", name)
		}
	}
}

// ============================================================
// 🔴 1.20× 地板断言（定价修订 A7）
// ============================================================

// heavyYearlyUSDT 是定价修订 §4.3 里**最薄的那一格**：重度 · 年付 · USDT 立减。
//
//	¥3541（= floor(floor(358×12×0.85)×0.97)），Q = 250 GiB，n = 12，FX = 7.15。
//	文档给的覆盖倍数：不扣返佣 1.2749×，扣掉 C6 的一次性返佣 ¥35.80 后 **1.2620×**。
func heavyYearlyUSDT(accrualCents int64) floorCheck {
	return floorCheck{
		PlanKind:            planKindCycle,
		Period:              dbgen.OrderPeriodYearly,
		TransferEnableBytes: 250 << 30,
		NetCents:            354100 - accrualCents,
		EffMonthsNum:        12,
		EffMonthsDen:        1,
		CnyPerUsdtE4:        71500,
		AccrualCents:        accrualCents,
	}
}

func TestAssertPriceFloorMatchesPricingTable(t *testing.T) {
	// 正常路径：C6 的一次性定额返佣（重度档月付标价 ¥358 的 10% = ¥35.80）。
	if _, _, err := assertPriceFloor(heavyYearlyUSDT(3580)); err != nil {
		t.Fatalf("最薄格（1.2620×）应当通过地板：%v", err)
	}

	// 🔴 这条是 C6 与 A7「互相咬合」的证据：**旧的「按订单金额 10%」口径**
	//    在同一格上是 1.1474×，破地板 0.053。它必须被拒。
	//    如果哪天有人把返佣改回按订单比例算，这条用例会先炸，而不是等到年度复盘。
	if _, _, err := assertPriceFloor(heavyYearlyUSDT(35410)); !errors.Is(err, errPriceFloorViolated) {
		t.Fatal("按订单金额 10% 的旧返佣口径会把最薄格打穿地板，必须被拒绝")
	}

	// 不扣返佣时是 1.2749×，同样通过 —— 用来锚定「扣项确实起了作用」。
	if _, _, err := assertPriceFloor(heavyYearlyUSDT(0)); err != nil {
		t.Fatalf("不扣返佣时（1.2749×）应当通过：%v", err)
	}
}

func TestAssertPriceFloorRejectsDeepDiscount(t *testing.T) {
	// 一张把最薄格再砍 10% 的优惠码：地板必须挡住它。
	// **拒绝下单，不是记日志放行** —— 一条破地板的订单是一笔确定的亏损，
	// 「记了日志的亏损」与「没记日志的亏损」在现金上完全一样。
	in := heavyYearlyUSDT(3580)
	in.NetCents -= 35410
	if _, _, err := assertPriceFloor(in); !errors.Is(err, errPriceFloorViolated) {
		t.Fatal("叠加深折扣之后必须破地板")
	}
}

// 🔴 静默边界：升级单的 amount_gross 被 D_left/D_total 缩过一次，
// 而周期月数没有跟着缩。不做这个缩放，**任何一次升级都会破地板并被拒绝**，
// 而失败形态是「升级按钮点了就报『价格低于成本下限』」—— 一个看起来像定价配错了的故障。
func TestAssertPriceFloorScalesUpgradeMonths(t *testing.T) {
	const (
		fullYearCents = 162080 // 标准 · 年付（覆盖 1.3284×）
		dTotal        = 365
		dLeft         = 100
	)
	upgradeGross := mulDiv(fullYearCents, dLeft, dTotal)

	base := floorCheck{
		PlanKind:            planKindCycle,
		Period:              dbgen.OrderPeriodYearly,
		TransferEnableBytes: 100 << 30,
		NetCents:            upgradeGross,
		CnyPerUsdtE4:        71500,
	}

	scaled := base
	scaled.EffMonthsNum = 12 * dLeft
	scaled.EffMonthsDen = dTotal
	if _, _, err := assertPriceFloor(scaled); err != nil {
		t.Fatalf("按剩余天数缩放之后，升级单应当与整单同样通过：%v", err)
	}

	naive := base
	naive.EffMonthsNum = 12 // 忘了缩放
	naive.EffMonthsDen = 1
	if _, _, err := assertPriceFloor(naive); !errors.Is(err, errPriceFloorViolated) {
		t.Fatal("不缩放的话升级单必然破地板 —— 这条用例存在的意义就是记住这一点")
	}
}

// 🔴 静默边界：月数无定义时必须**拒绝判定**，不能当成 1 个月接着算。
// 当成 1 个月是「拿月付成本去衡量一份不限时的服务」，结论必然是「通过」——
// 一个永远为真的断言等于没有断言。
func TestAssertPriceFloorRefusesUndefinedMonths(t *testing.T) {
	in := heavyYearlyUSDT(0)
	in.Period = dbgen.OrderPeriodOnetime
	_, _, err := assertPriceFloor(in)
	if err == nil {
		t.Fatal("onetime 没有月数定义，必须拒绝判定而不是放行")
	}
	if errors.Is(err, errPriceFloorViolated) {
		t.Fatal("这不是「破地板」，是「判不了」—— 两者的处置不同")
	}

	in2 := heavyYearlyUSDT(0)
	in2.CnyPerUsdtE4 = 0
	if _, _, err := assertPriceFloor(in2); err == nil {
		t.Fatal("汇率未知时必须拒绝判定（地板的分母之一就是它）")
	}

	// D_left = 0 的升级单：有效月数为 0，同样判不了 —— 而它本来也没有意义。
	in3 := heavyYearlyUSDT(0)
	in3.EffMonthsNum = 0
	if _, _, err := assertPriceFloor(in3); err == nil {
		t.Fatal("有效月数为 0 时必须拒绝判定")
	}
}

func TestAssertPriceFloorTrafficPack(t *testing.T) {
	// 加油包：¥1.20/GiB，100 GiB = ¥120。摊回 ¥200 充值触发的那次链上归集后覆盖 1.3142×。
	pack := floorCheck{
		PlanKind:            planKindPack,
		TransferEnableBytes: 100 << 30,
		NetCents:            12000,
		EffMonthsNum:        1,
		EffMonthsDen:        1,
		CnyPerUsdtE4:        71500,
	}
	if _, _, err := assertPriceFloor(pack); err != nil {
		t.Fatalf("加油包标价应当过地板：%v", err)
	}
	// 10 GiB 的小包同样是 ¥1.20/GiB，比例不变，也必须过 ——
	// 用 f + s 那套周期口径去套加油包会把所有小包判成破地板。
	small := pack
	small.TransferEnableBytes = 10 << 30
	small.NetCents = 1200
	if _, _, err := assertPriceFloor(small); err != nil {
		t.Fatalf("小规格加油包不能因为口径套错而被拒：%v", err)
	}
	// 半价甩卖的加油包必须被挡住。
	half := pack
	half.NetCents = 6000
	if _, _, err := assertPriceFloor(half); !errors.Is(err, errPriceFloorViolated) {
		t.Fatal("加油包打对折必须破地板")
	}
}

func TestCostModelMatchesPricingTable(t *testing.T) {
	// 三档月付总成本（定价修订 §4.3 的表，单位 1e-6 USD）：Q×c + f + s。
	// 逐格核对是为了让「有人改了 c / f / s 其中一个常量」立刻可见 ——
	// 改常量没有任何编译错误，只会让地板整体挪一格。
	cases := []struct {
		name  string
		bytes int64
		n     int64
		want  int64 // n × 月度总成本
	}{
		{"轻量 · 月付 $7.10", 30 << 30, 1, 7_100_000},
		{"标准 · 月付 $15.57", 100 << 30, 1, 15_570_000},
		{"重度 · 月付 $33.72", 250 << 30, 1, 33_720_000},
		// 年付：12 × (Q×c + f) + s = 12 × 32.25 + 1.47 = $388.47（重度档）。
		{"重度 · 年付 12 个月合计 $388.47", 250 << 30, 12, 388_470_000},
	}
	for _, c := range cases {
		if got := monthlyCostMicroUSDTimesN(c.bytes, c.n).Int64(); got != c.want {
			t.Fatalf("%s：期望 %d，实际 %d", c.name, c.want, got)
		}
	}

	// 🔴 s 必须留在分子里而不是先算 s/n。
	//    n=12 时 $1.47/12 = $0.1225，在整数微美元下是精确的；但换一个 n（比如 7）
	//    就会截断，而少算成本 = 地板变松，方向是错的。
	//    这条断言把「分子里留着 s」写成可执行的事实：n=7 时结果必须仍含完整的 1_470_000。
	if got := monthlyCostMicroUSDTimesN(0, 7).Int64(); got != 7*costFixedMicroUSD+costChannelMicroUSD {
		t.Fatalf("s 被提前除掉了：%d", got)
	}

	// 加油包用的是另一套口径（不摊 f、按 5.26% 摊回一次充值归集）。
	// 100 GiB：12,100,000 / (1 − 5.26%) = 12,771,797 微美元。
	if got := packCostMicroUSD(100 << 30).Int64(); got != 12_771_797 {
		t.Fatalf("加油包成本口径变了：%d", got)
	}
}

// ============================================================
// 报价与量纲
// ============================================================

func TestQuoteUSDT6(t *testing.T) {
	// ¥100.00 @ 7.15 + 1% 缓冲 = 100 × 1.01 / 7.15 = 14.1259... → 向上取整到 14.13 USDT。
	got, err := quoteUSDT6(10000, 71500, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got != 14_130_000 {
		t.Fatalf("报价应当是 14.13 USDT（14130000），实际 %d", got)
	}
	if usdt6Display(got) != "14.13" {
		t.Fatalf("amount_display 必须是两位小数字符串，实际 %q", usdt6Display(got))
	}

	// 🔴 一律 ceil。向下取整的后果是：用户按我们报的数转账，然后被我们判成少付 ——
	//    一次由我们造成、却由用户承担的 underpaid。
	for _, cents := range []int64{1, 7, 99, 101, 12345, 99999} {
		q, err := quoteUSDT6(cents, 71500, 100)
		if err != nil {
			t.Fatal(err)
		}
		if q%10_000 != 0 {
			t.Fatalf("报价必须取整到 0.01 USDT，实际 %d", q)
		}
		// 反向核对：报价折回人民币不得低于应付额。
		if back := usdt6ToCents(q, 71500); back < cents {
			t.Fatalf("¥%d 的报价折回来只有 ¥%d —— 取整方向反了", cents, back)
		}
	}

	if _, err := quoteUSDT6(0, 71500, 100); err == nil {
		t.Fatal("应付为 0 的订单不该走支付通道")
	}
	if _, err := quoteUSDT6(10000, 0, 100); err == nil {
		t.Fatal("汇率未配置时必须失败，而不是报出一个 0 元的账单")
	}
}

func TestNumericE4RoundTrip(t *testing.T) {
	n := numericFromE4(71500)
	got, ok := numericToE4(n)
	if !ok || got != 71500 {
		t.Fatalf("汇率往返失败：%v %v", got, ok)
	}
	// NULL 列必须能被识别 —— fx_usdt_per_cny 在订单走到 payOrder 之前恒为 NULL，
	// 而「刚下单就打开收银台」是最普通的一条路径。
	if _, ok := numericToE4(pgtype.Numeric{}); ok {
		t.Fatal("NULL 汇率必须报告为不可用，而不是 0")
	}
}

func TestPeriodMonths(t *testing.T) {
	for p, want := range map[dbgen.OrderPeriod]int64{
		dbgen.OrderPeriodMonthly:    1,
		dbgen.OrderPeriodQuarterly:  3,
		dbgen.OrderPeriodHalfYearly: 6,
		dbgen.OrderPeriodYearly:     12,
		dbgen.OrderPeriodOnetime:    0, // 🔴 无定义，调用方必须据此拒绝
	} {
		if got := periodMonths(p); got != want {
			t.Fatalf("%s 应当是 %d 个月，实际 %d", p, want, got)
		}
	}
}

// ============================================================
// 契约映射
// ============================================================

// 🔴 静默边界：契约的 OrderStatus 只有 6 个值且含 DB 里不存在的 processing，
// 而 DB 的 order_status 有 14 个。直接把 enum fmt 出去，前端会收到一个
// 它的类型里没有的字符串 —— JSON 没有类型检查，现象是订单卡片整块空白而不是报错。
func TestOrderStatusViewNeverLeaksRawEnum(t *testing.T) {
	allowed := map[gen.OrderStatus]bool{
		gen.OrderStatusPending: true, gen.OrderStatusProcessing: true,
		gen.OrderStatusCompleted: true, gen.OrderStatusCancelled: true,
		gen.OrderStatusExpired: true, gen.OrderStatusRefunded: true,
	}
	all := []dbgen.OrderStatus{
		dbgen.OrderStatusPending, dbgen.OrderStatusPaying, dbgen.OrderStatusUnderpaid,
		dbgen.OrderStatusPaid, dbgen.OrderStatusCompleted, dbgen.OrderStatusCancelled,
		dbgen.OrderStatusExpired, dbgen.OrderStatusFailed, dbgen.OrderStatusRefunding,
		dbgen.OrderStatusRefunded, dbgen.OrderStatusPartiallyRefunded,
		dbgen.OrderStatusChargeback, dbgen.OrderStatusChargebackWon, dbgen.OrderStatusChargebackLost,
	}
	if len(all) != 14 {
		t.Fatalf("order_status 应当有 14 个值，本用例列了 %d 个 —— DB 加了值就要同步这里", len(all))
	}
	for _, st := range all {
		if !allowed[orderStatusView(st)] {
			t.Fatalf("%s 映射出了契约枚举之外的值 %q", st, orderStatusView(st))
		}
	}
	// 一个契约里没有的 DB 值（模拟将来新增）也不能原样泄漏。
	if !allowed[orderStatusView(dbgen.OrderStatus("brand_new_state"))] {
		t.Fatal("未知状态必须落到兜底档，不能把原文发出去")
	}
	if orderStatusView(dbgen.OrderStatusPaying) != gen.OrderStatusProcessing {
		t.Fatal("paying 在契约里没有对应值，只能并进 processing")
	}
}

func TestPaymentStateView(t *testing.T) {
	cases := []struct {
		status   dbgen.OrderStatus
		received int64
		want     gen.PaymentState
	}{
		{dbgen.OrderStatusPending, 0, gen.PaymentStateWaiting},
		{dbgen.OrderStatusPaying, 0, gen.PaymentStateWaiting},
		{dbgen.OrderStatusPaying, 1, gen.PaymentStateConfirming},
		{dbgen.OrderStatusUnderpaid, 5, gen.PaymentStateUnderpaid},
		{dbgen.OrderStatusPaid, 5, gen.PaymentStatePaid},
		{dbgen.OrderStatusCompleted, 5, gen.PaymentStatePaid},
		{dbgen.OrderStatusRefunded, 5, gen.PaymentStatePaid}, // 钱确实到过
		{dbgen.OrderStatusExpired, 0, gen.PaymentStateExpired},
		{dbgen.OrderStatusCancelled, 0, gen.PaymentStateExpired},
	}
	for _, c := range cases {
		got := paymentStateView(dbgen.GetOrderCheckoutRow{Status: c.status, ReceivedUsdt6: c.received})
		if got != c.want {
			t.Fatalf("%s(received=%d) 应当是 %q，实际 %q", c.status, c.received, c.want, got)
		}
	}
}

func TestCheckoutView(t *testing.T) {
	set := defaultPaymentSettings()
	chain := payChainTron
	addr := "TXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	amt := int64(14_130_000)
	row := dbgen.GetOrderCheckoutRow{
		TradeNo: "BP2026", Status: dbgen.OrderStatusPaying,
		PayChain: &chain, PayAddress: &addr, PayAmountUsdt6: &amt,
		FxUsdtPerCny:  numericFromE4(71500),
		ExpiresAt:     ts(time.Now().Add(time.Minute)),
		ReceivedUsdt6: 0, ShortfallUsdt6: amt,
	}
	out := checkoutView(row, set)
	if out.State != gen.PaymentStateWaiting {
		t.Fatalf("有址无到账应当是 waiting，实际 %q", out.State)
	}
	if out.Chain == nil || *out.Chain != gen.TRC20 {
		t.Fatal("pay_chain='tron' 必须映射成契约枚举 TRC20")
	}
	if out.AmountDisplay == nil || *out.AmountDisplay != "14.13" {
		t.Fatalf("amount_display 必须是两位小数字符串，实际 %v", out.AmountDisplay)
	}
	if out.CnyPerUsdtE4 == nil || *out.CnyPerUsdtE4 != 71500 {
		t.Fatalf("cny_per_usdt_e4 必须在 Go 侧由 fx 列算出，实际 %v", out.CnyPerUsdtE4)
	}
	if out.ConfirmationsRequired == nil || *out.ConfirmationsRequired != defaultConfirmationsRequired {
		t.Fatal("confirmations_required 必须可配置下发（契约硬约束）")
	}
	// 🔴 只有 underpaid 才下发 shortfall：一个恒定出现的差额字段会诱使前端
	//    在 waiting 时也显示「还差 X」，而那时用户根本还没转账。
	if out.ShortfallUsdt6 != nil {
		t.Fatal("非 underpaid 状态不该下发 shortfall")
	}

	row.Status = dbgen.OrderStatusUnderpaid
	row.ReceivedUsdt6 = 1_000_000
	row.ShortfallUsdt6 = 13_130_000
	if out2 := checkoutView(row, set); out2.ShortfallUsdt6 == nil || *out2.ShortfallUsdt6 != 13_130_000 {
		t.Fatalf("underpaid 必须带 shortfall，实际 %v", out2.ShortfallUsdt6)
	}

	// 刚下单（无址无价无汇率）也必须能开收银台 —— 这是最普通的一条路径，
	// 而 fx_usdt_per_cny 在 payOrder 之前恒为 NULL。
	bare := dbgen.GetOrderCheckoutRow{TradeNo: "BP1", Status: dbgen.OrderStatusPending}
	if got := checkoutView(bare, set); got.State != gen.PaymentStateWaiting || got.CnyPerUsdtE4 != nil {
		t.Fatalf("刚下单的收银台应当是 waiting 且没有汇率，实际 %+v", got)
	}
}

// 🔴 ADR 0012 §5.4 把「金额尾数 = 订单识别码」整套机制删掉了，归属只看地址。
// 契约里 PaymentCheckout.note 的 description 与 user-journey §7 卡点 5 的原文案
// 都是被推翻的原文 —— 照它们写，用户从交易所提币时必然 underpaid。
func TestPaymentCheckoutNoteHasNoAmountMatchingWording(t *testing.T) {
	banned := []string{"小数点后四位", "四位小数", "尾数", "识别码", "一分不差", "0.0001"}
	for _, w := range banned {
		if strings.Contains(paymentCheckoutNote, w) {
			t.Fatalf("收银台文案不得出现被 ADR 0012 §5.4 推翻的金额匹配话术：%q", w)
		}
	}
	for _, must := range []string{"提币手续费", "永远认账"} {
		if !strings.Contains(paymentCheckoutNote, must) {
			t.Fatalf("收银台文案必须解释 %q（ADR 0012 §6.4）", must)
		}
	}
}

func TestOrderViewAmountMapping(t *testing.T) {
	period := dbgen.OrderPeriodYearly
	name := "标准"
	row := dbgen.GetUserOrderRow{
		TradeNo: "BP1", Type: dbgen.OrderTypeUpgrade, Status: dbgen.OrderStatusPaying,
		Period: &period, PlanID: i64p(3), PlanName: &name,
		AmountGross: 10000, AmountDiscount: 500, SurplusAmount: 1500, AmountBalance: 2000, AmountDue: 6000,
		CreatedAt: ts(time.Now()), FxLockedAt: ts(time.Now()),
	}
	o := orderView(row)
	if o.TotalAmount != 10000 || o.PayableAmount != 6000 {
		t.Fatalf("total ← amount_gross、payable ← amount_due，实际 %d / %d", o.TotalAmount, o.PayableAmount)
	}
	if *o.DiscountAmount != 500 || *o.SurplusAmount != 1500 || *o.BalanceAmount != 2000 {
		t.Fatal("三个抵扣项的映射错位了")
	}
	if o.RateLockedAt == nil {
		t.Fatal("rate_locked_at ← fx_locked_at")
	}
	if o.Type != gen.OrderTypeUpgrade {
		t.Fatal("type 映射错")
	}
	// 恒等式：amount_due = gross − discount − surplus − balance（orders 上的 CHECK）。
	if o.TotalAmount-*o.DiscountAmount-*o.SurplusAmount-*o.BalanceAmount != o.PayableAmount {
		t.Fatal("四项金额不满足 orders_amount_balance 的恒等式")
	}
}

func TestOrderPageCursor(t *testing.T) {
	base := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	rows := []dbgen.ListUserOrdersPageRow{
		{ID: 3, TradeNo: "c", Type: dbgen.OrderTypeNew, Status: dbgen.OrderStatusPending, CreatedAt: ts(base)},
		{ID: 2, TradeNo: "b", Type: dbgen.OrderTypeNew, Status: dbgen.OrderStatusPending, CreatedAt: ts(base)},
		{ID: 1, TradeNo: "a", Type: dbgen.OrderTypeNew, Status: dbgen.OrderStatusPending, CreatedAt: ts(base)},
	}
	data, next, more := orderPage(rows, 2)
	if len(data) != 2 || !more || next == nil {
		t.Fatalf("多取一行应当判出 has_more：len=%d more=%v", len(data), more)
	}
	cur, err := decodeKeysetCursor(*next)
	if err != nil {
		t.Fatal(err)
	}
	// 🔴 三张单的 created_at **完全相同**（下单失败重试的形态）。
	//    游标必须带 id，SQL 侧才能用行比较 (created_at,id) < (cursor_at,cursor_id)；
	//    只带时间戳的话这一整批会被跳过 —— 而漏的正好是「重试出来的那几张」。
	if *cur.ID != 2 {
		t.Fatalf("游标必须带上第 limit 行的 id（2），实际 %d", *cur.ID)
	}
	if !cur.At.Equal(base) {
		t.Fatalf("游标时间戳不对：%v", *cur.At)
	}

	if _, next, more := orderPage(rows[:2], 2); more || next != nil {
		t.Fatal("恰好一页时不该有 next_cursor")
	}
}

// ============================================================
// 余额抵扣
// ============================================================

func TestApplyBalance(t *testing.T) {
	d := &orderDraft{AmountGross: 10000, AmountDiscount: 1000, SurplusAmount: 2000}
	yes := true
	applyBalance(50000, d, &yes)
	// 抵扣封顶到「还需要付的部分」（7000），不是订单原价。
	// 抵多了 amount_due 会变负，而 orders 上的 CHECK 会把一条产品规则变成一次 500。
	if d.AmountBalance != 7000 {
		t.Fatalf("余额抵扣应当封顶到 7000，实际 %d", d.AmountBalance)
	}

	d2 := &orderDraft{AmountGross: 10000}
	applyBalance(300, d2, &yes)
	if d2.AmountBalance != 300 {
		t.Fatalf("余额不足时按余额抵，实际 %d", d2.AmountBalance)
	}

	d3 := &orderDraft{AmountGross: 10000}
	applyBalance(50000, d3, nil)
	if d3.AmountBalance != 0 {
		t.Fatal("没勾选 use_balance 时不得动用户的余额")
	}
}

// ============================================================
// 下单写入
// ============================================================

type fakeOrderWriter struct {
	order        dbgen.Order
	createErr    error
	couponRows   int
	couponErr    error
	transitions  []dbgen.InsertOrderTransitionParams
	createParams dbgen.CreateOrderParams
	couponCalls  []int64
}

func (f *fakeOrderWriter) CreateOrder(_ context.Context, arg dbgen.CreateOrderParams) (dbgen.Order, error) {
	f.createParams = arg
	if f.createErr != nil {
		return dbgen.Order{}, f.createErr
	}
	o := f.order
	o.TradeNo = arg.TradeNo
	o.Type = arg.Type
	o.AmountGross = arg.AmountGross
	o.AmountDue = arg.AmountDue
	return o, nil
}

func (f *fakeOrderWriter) InsertOrderTransition(_ context.Context, arg dbgen.InsertOrderTransitionParams) (dbgen.OrderTransition, error) {
	f.transitions = append(f.transitions, arg)
	return dbgen.OrderTransition{}, nil
}

func (f *fakeOrderWriter) IncrementCouponUse(_ context.Context, id int64) (dbgen.IncrementCouponUseRow, error) {
	f.couponCalls = append(f.couponCalls, id)
	if f.couponErr != nil {
		return dbgen.IncrementCouponUseRow{}, f.couponErr
	}
	return dbgen.IncrementCouponUseRow{ID: id}, nil
}

func TestWriteOrder(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	t.Run("正常路径：写单 + 核销优惠码 + 写状态审计", func(t *testing.T) {
		f := &fakeOrderWriter{order: dbgen.Order{ID: 11}}
		draft := &orderDraft{
			Type: dbgen.OrderTypeNew, Period: dbgen.OrderPeriodYearly, PlanID: 3,
			AmountGross: 10000, AmountDiscount: 1000, AmountDue: 9000,
			CouponID: i64p(77), PriceMonthly: i64p(900),
		}
		o, err := writeOrder(context.Background(), f, 42, "BP1", draft, now)
		if err != nil {
			t.Fatal(err)
		}
		if o.TradeNo != "BP1" {
			t.Fatal("返回的应当是写入的那一行，不是回查的")
		}
		if len(f.couponCalls) != 1 || f.couponCalls[0] != 77 {
			t.Fatalf("必须核销优惠码，实际 %v", f.couponCalls)
		}
		if f.createParams.ExpiresAt.Time != now.Add(orderPayWindow) {
			t.Fatalf("支付窗口应当是 30 分钟，实际 %v", f.createParams.ExpiresAt.Time)
		}
		if f.createParams.PriceMonthlyAtOrder == nil || *f.createParams.PriceMonthlyAtOrder != 900 {
			// price_monthly_at_order 是下单时的快照。事后回读活列的后果是
			// 涨价之后老订单的退款额跟着变小，用户会认为我们改价来少退钱。
			t.Fatal("price_monthly_at_order 必须写入下单时的快照")
		}
		if len(f.transitions) != 1 {
			t.Fatalf("必须写一条 order_transitions，实际 %d 条", len(f.transitions))
		}
		tr := f.transitions[0]
		if tr.FromStatus != nil {
			t.Fatal("从无到有，from_status 必须是 NULL")
		}
		if tr.ToStatus != dbgen.OrderStatusPending || tr.Actor != "user:42" {
			t.Fatalf("审计内容不对：%+v", tr)
		}
	})

	t.Run("错误分支：优惠码被并发抢完 → errCouponRaced（用户可去掉优惠码重试）", func(t *testing.T) {
		f := &fakeOrderWriter{couponErr: pgx.ErrNoRows}
		_, err := writeOrder(context.Background(), f, 1, "BP2",
			&orderDraft{CouponID: i64p(5), Period: dbgen.OrderPeriodMonthly}, now)
		if !errors.Is(err, errCouponRaced) {
			t.Fatalf("应当识别成「被抢完」，实际 %v", err)
		}
		if f.createParams.TradeNo != "" {
			t.Fatal("核销失败之后不该继续写订单")
		}
	})

	// 🔴 静默边界：优惠码在**创建时**核销，不是支付时。
	// 支付时核销会超卖 —— 两张 pending 单可以同时通过「还没用完」的检查，
	// 而超卖是不可见的；创建时核销的代价（下单后取消漏掉一次全局次数）是可见且有界的。
	t.Run("静默边界：核销发生在写订单之前", func(t *testing.T) {
		f := &fakeOrderWriter{}
		if _, err := writeOrder(context.Background(), f, 1, "BP3",
			&orderDraft{CouponID: i64p(9), Period: dbgen.OrderPeriodMonthly}, now); err != nil {
			t.Fatal(err)
		}
		if len(f.couponCalls) == 0 {
			t.Fatal("带优惠码的订单必须核销")
		}
	})

	t.Run("没有优惠码时不碰 coupons 表", func(t *testing.T) {
		f := &fakeOrderWriter{}
		if _, err := writeOrder(context.Background(), f, 1, "BP4",
			&orderDraft{Period: dbgen.OrderPeriodMonthly}, now); err != nil {
			t.Fatal(err)
		}
		if len(f.couponCalls) != 0 {
			t.Fatal("没有优惠码却核销了")
		}
	})
}

type fakeTradeNoProbe struct {
	taken map[string]bool
	calls int
	err   error
}

func (f *fakeTradeNoProbe) TradeNoExists(_ context.Context, no string) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.taken[no], nil
}

func TestNewTradeNo(t *testing.T) {
	f := &fakeTradeNoProbe{taken: map[string]bool{}}
	no, err := newTradeNo(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(no) < 6 || len(no) > 64 {
		t.Fatalf("单号长度必须落在契约的 6–64 之间，实际 %d（%q）", len(no), no)
	}
	if !strings.HasPrefix(no, "BP") {
		t.Fatalf("单号形态变了：%q", no)
	}
	f.err = errors.New("boom")
	if _, err := newTradeNo(context.Background(), f); err == nil {
		t.Fatal("探测失败必须上报，而不是硬插一个可能撞号的单号")
	}
}

// ============================================================
// 支付配置
// ============================================================

type fakeSettings struct {
	rows []dbgen.GetPaymentSettingsRow
	err  error
}

func (f *fakeSettings) GetPaymentSettings(context.Context, []string) ([]dbgen.GetPaymentSettingsRow, error) {
	return f.rows, f.err
}

func TestLoadPaymentSettings(t *testing.T) {
	ctx := context.Background()

	t.Run("表里没有配置时回落默认值，收银台照常可用", func(t *testing.T) {
		got := loadPaymentSettings(ctx, &fakeSettings{}, testLogger())
		if got != defaultPaymentSettings() {
			t.Fatalf("应当回落默认值，实际 %+v", got)
		}
	})

	t.Run("读表失败也不让端点失败（用默认阈值收一笔钱好过给用户一个 500）", func(t *testing.T) {
		got := loadPaymentSettings(ctx, &fakeSettings{err: errors.New("boom")}, testLogger())
		if got.WriteoffUsdt6 != defaultWriteoffUsdt6 {
			t.Fatal("读表失败时必须回落默认值")
		}
	})

	t.Run("正常路径：settings 覆盖默认值", func(t *testing.T) {
		got := loadPaymentSettings(ctx, &fakeSettings{rows: []dbgen.GetPaymentSettingsRow{
			{Key: settingsKeyPayment, Value: []byte(`{"confirm_policy":25,"writeoff_usdt6":1000000,"review_usdt6":4000000,"addr_low_water":16}`)},
			{Key: settingsKeyFX, Value: []byte(`{"cny_per_usdt_e4":72000,"buffer_bps":150}`)},
		}}, testLogger())
		if got.ConfirmationsRequired != 25 || got.WriteoffUsdt6 != 1_000_000 ||
			got.ReviewUsdt6 != 4_000_000 || got.AddrLowWater != 16 ||
			got.CnyPerUsdtE4 != 72000 || got.FxBufferBps != 150 {
			t.Fatalf("配置没有生效：%+v", got)
		}
	})

	// 🔴 静默边界：写销阈值 > 人工阈值时 B 档（人工队列）整档消失 ——
	// 所有少付都会走进「自动写销」，而那是我们直接放弃的钱。
	// 配置写反不会报错，只会让钱少一点点、每次少一点点。
	t.Run("静默边界：写销阈值大于人工阈值时回落默认值", func(t *testing.T) {
		got := loadPaymentSettings(ctx, &fakeSettings{rows: []dbgen.GetPaymentSettingsRow{
			{Key: settingsKeyPayment, Value: []byte(`{"writeoff_usdt6":9000000,"review_usdt6":1000000}`)},
		}}, testLogger())
		if got.WriteoffUsdt6 != defaultWriteoffUsdt6 || got.ReviewUsdt6 != defaultReviewUsdt6 {
			t.Fatalf("非法阈值必须被拒绝，实际 %+v", got)
		}
	})

	t.Run("JSON 坏掉时只影响那一个键", func(t *testing.T) {
		got := loadPaymentSettings(ctx, &fakeSettings{rows: []dbgen.GetPaymentSettingsRow{
			{Key: settingsKeyPayment, Value: []byte(`{{{`)},
			{Key: settingsKeyFX, Value: []byte(`{"cny_per_usdt_e4":70000}`)},
		}}, testLogger())
		if got.CnyPerUsdtE4 != 70000 {
			t.Fatal("一个键坏掉不该拖累另一个")
		}
		if got.WriteoffUsdt6 != defaultWriteoffUsdt6 {
			t.Fatal("坏掉的键必须回落默认值")
		}
	})
}

// ============================================================
// 复式账
// ============================================================

type fakeLedger struct {
	entries []dbgen.CreateLedgerEntryParams
	lines   []dbgen.CreateLedgerLineParams
	missing map[string]bool
	nextID  int64
}

func (f *fakeLedger) GetLedgerAccountByCode(_ context.Context, code string) (dbgen.LedgerAccount, error) {
	if f.missing[code] {
		return dbgen.LedgerAccount{}, pgx.ErrNoRows
	}
	return dbgen.LedgerAccount{ID: int64(len(code)), Code: code}, nil
}

func (f *fakeLedger) CreateLedgerEntry(_ context.Context, arg dbgen.CreateLedgerEntryParams) (dbgen.LedgerEntry, error) {
	f.entries = append(f.entries, arg)
	f.nextID++
	return dbgen.LedgerEntry{ID: f.nextID, EntryNo: arg.EntryNo}, nil
}

func (f *fakeLedger) CreateLedgerLine(_ context.Context, arg dbgen.CreateLedgerLineParams) (dbgen.LedgerLine, error) {
	f.lines = append(f.lines, arg)
	return dbgen.LedgerLine{}, nil
}

func TestPostLedgerEntry(t *testing.T) {
	ctx := context.Background()

	t.Run("正常路径：跨币种分录靠 fx_clearing 桥接，每个币种各自配平", func(t *testing.T) {
		f := &fakeLedger{}
		_, err := postLedgerEntry(ctx, f, ledgerEntrySpec{
			EntryNo: "RCV-1", Description: "d", RefType: "order", RefID: 1,
			Lines: []ledgerLineSpec{
				{AccountCode: acctTronPool, Currency: "USDT", Amount: 1000},
				{AccountCode: acctFxClearingUSDT, Currency: "USDT", Amount: -1000},
				{AccountCode: acctFxClearingCNY, Currency: "CNY", Amount: 715},
				{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: -715},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(f.lines) != 4 {
			t.Fatalf("应当写 4 条腿，实际 %d", len(f.lines))
		}
	})

	// 🔴 静默边界：`SUM(amount) = 0` 这条不变量 schema 层表达不出来（跨行）。
	// 断言放在写入之前，不平的分录**根本写不进去** —— 等第二天巡检报红时，
	// 它已经和真钱混在一起了。
	t.Run("静默边界：借贷不平的分录写不进去", func(t *testing.T) {
		f := &fakeLedger{}
		_, err := postLedgerEntry(ctx, f, ledgerEntrySpec{
			EntryNo: "BAD", Lines: []ledgerLineSpec{
				{AccountCode: acctUserWallet, Currency: "CNY", Amount: 100},
				{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: -99},
			},
		})
		if err == nil {
			t.Fatal("不平的分录必须被拒绝")
		}
		if len(f.entries) != 0 {
			t.Fatal("拒绝必须发生在写入之前")
		}
	})

	// 同一条断言的另一半：整体求和为 0、但**分币种**不平的分录也必须被拒。
	// ledger_accounts.currency 是 per-account 的，混着记会让每日断言天天报红。
	t.Run("静默边界：整体为 0 但分币种不平，同样拒绝", func(t *testing.T) {
		f := &fakeLedger{}
		_, err := postLedgerEntry(ctx, f, ledgerEntrySpec{
			EntryNo: "BAD2", Lines: []ledgerLineSpec{
				{AccountCode: acctTronPool, Currency: "USDT", Amount: 100},
				{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: -100},
			},
		})
		if err == nil {
			t.Fatal("跨币种直接对冲必须被拒绝（要走 fx_clearing 两条腿）")
		}
	})

	t.Run("错误分支：科目不存在时整条失败，不写半条分录", func(t *testing.T) {
		f := &fakeLedger{missing: map[string]bool{acctDeferredRevenue: true}}
		_, err := postLedgerEntry(ctx, f, ledgerEntrySpec{
			EntryNo: "X", Lines: []ledgerLineSpec{
				{AccountCode: acctUserWallet, Currency: "CNY", Amount: 100},
				{AccountCode: acctDeferredRevenue, Currency: "CNY", Amount: -100},
			},
		})
		if err == nil || !strings.Contains(err.Error(), "科目不存在") {
			t.Fatalf("应当报科目不存在，实际 %v", err)
		}
	})
}

// ============================================================
// 🔴 processDeposit：入账的唯一入口
// ============================================================

type fakeDeposit struct {
	fakeLedger

	payAddr      dbgen.GetPayAddressByAddressRow
	payAddrErr   error
	insertErr    error
	inserted     []dbgen.InsertPaymentIfNewParams
	existing     dbgen.Payment
	existingErr  error
	appendErr    error
	order        dbgen.GetOrderByPayAddressForUpdateRow
	orderErr     error
	attributed   []dbgen.AttributePaymentParams
	receipts     dbgen.SumAddressReceiptsRow
	transitions  []dbgen.TransitionOrderStatusParams
	auditRows    []dbgen.InsertOrderTransitionParams
	payerAddr    []dbgen.RecordOrderPayerAddressParams
	walletTopups []dbgen.UpsertWalletBalanceParams
}

func (f *fakeDeposit) GetPayAddressByAddress(context.Context, dbgen.GetPayAddressByAddressParams) (dbgen.GetPayAddressByAddressRow, error) {
	return f.payAddr, f.payAddrErr
}

func (f *fakeDeposit) InsertPaymentIfNew(_ context.Context, arg dbgen.InsertPaymentIfNewParams) (dbgen.Payment, error) {
	f.inserted = append(f.inserted, arg)
	if f.insertErr != nil {
		return dbgen.Payment{}, f.insertErr
	}
	return dbgen.Payment{ID: 501, Provider: arg.Provider, ExternalID: arg.ExternalID, AmountUsdt6: arg.AmountUsdt6}, nil
}

func (f *fakeDeposit) GetPaymentByExternalID(context.Context, dbgen.GetPaymentByExternalIDParams) (dbgen.Payment, error) {
	return f.existing, f.existingErr
}

func (f *fakeDeposit) AppendScannerToPaymentEntry(context.Context, dbgen.AppendScannerToPaymentEntryParams) (dbgen.AppendScannerToPaymentEntryRow, error) {
	if f.appendErr != nil {
		return dbgen.AppendScannerToPaymentEntryRow{}, f.appendErr
	}
	return dbgen.AppendScannerToPaymentEntryRow{ID: 1}, nil
}

func (f *fakeDeposit) GetOrderByPayAddressForUpdate(context.Context, string) (dbgen.GetOrderByPayAddressForUpdateRow, error) {
	return f.order, f.orderErr
}

func (f *fakeDeposit) AttributePayment(_ context.Context, arg dbgen.AttributePaymentParams) (dbgen.Payment, error) {
	f.attributed = append(f.attributed, arg)
	return dbgen.Payment{ID: arg.PaymentID}, nil
}

func (f *fakeDeposit) SumAddressReceipts(context.Context, string) (dbgen.SumAddressReceiptsRow, error) {
	return f.receipts, nil
}

func (f *fakeDeposit) TransitionOrderStatus(_ context.Context, arg dbgen.TransitionOrderStatusParams) (dbgen.TransitionOrderStatusRow, error) {
	f.transitions = append(f.transitions, arg)
	return dbgen.TransitionOrderStatusRow{ID: arg.OrderID, Status: arg.ToStatus}, nil
}

func (f *fakeDeposit) InsertOrderTransition(_ context.Context, arg dbgen.InsertOrderTransitionParams) (dbgen.OrderTransition, error) {
	f.auditRows = append(f.auditRows, arg)
	return dbgen.OrderTransition{}, nil
}

func (f *fakeDeposit) RecordOrderPayerAddress(_ context.Context, arg dbgen.RecordOrderPayerAddressParams) error {
	f.payerAddr = append(f.payerAddr, arg)
	return nil
}

func (f *fakeDeposit) UpsertWalletBalance(_ context.Context, arg dbgen.UpsertWalletBalanceParams) (dbgen.WalletBalance, error) {
	f.walletTopups = append(f.walletTopups, arg)
	return dbgen.WalletBalance{}, nil
}

const testPayAddress = "TPayAddr000000000000000000000000000"

func depositFixture(status dbgen.OrderStatus, expectedUsdt6, receivedUsdt6 int64) *fakeDeposit {
	f := &fakeDeposit{
		payAddr: dbgen.GetPayAddressByAddressRow{ID: 9, Chain: payChainTron, Address: testPayAddress},
		order: dbgen.GetOrderByPayAddressForUpdateRow{
			ID: 100, TradeNo: "BP100", UserID: 42, Status: status,
			PayAmountUsdt6: &expectedUsdt6, FxUsdtPerCny: numericFromE4(71500),
		},
		receipts: dbgen.SumAddressReceiptsRow{ReceivedUsdt6: receivedUsdt6, PaymentCount: 1},
	}
	return f
}

func depositIn(amountUsdt6 int64) depositInput {
	return depositInput{
		Provider: "chain_tron", EnteredBy: "scanner", Chain: payChainTron,
		ToAddress: testPayAddress,
		Transfer: ChainTransfer{
			TxID: "abc", LogIndex: 0, FromAddress: "TFrom", ToAddress: testPayAddress,
			AmountUSDT6: amountUsdt6, Confirmations: 20, Solidified: true,
			Raw: json.RawMessage(`{"ok":true}`),
		},
		Settings:      defaultPaymentSettings(),
		ActorOverride: "chain:abc",
	}
}

func TestProcessDepositWriteoffTier(t *testing.T) {
	// A 档：差额 1.0 USDT ≤ 写销上界 2.0 USDT → 直接 paying→paid。
	// **我们不去要一笔要不来的钱** —— 补足会被再扣一次同样的提币费，净到账 0。
	f := depositFixture(dbgen.OrderStatusPaying, 10_000_000, 9_000_000)
	if err := processDeposit(context.Background(), f, testLogger(), depositIn(9_000_000)); err != nil {
		t.Fatal(err)
	}
	if len(f.transitions) != 1 || f.transitions[0].ToStatus != dbgen.OrderStatusPaid {
		t.Fatalf("应当迁移到 paid，实际 %+v", f.transitions)
	}
	// 🔴 CAS：from-status 必须带上，否则扫链与 recheck 并发时 paying→paid 会跑两次。
	if f.transitions[0].FromStatus != dbgen.OrderStatusPaying {
		t.Fatal("状态迁移必须带 from-status（DB 层 CAS）")
	}
	if len(f.auditRows) != 1 {
		t.Fatal("每次迁移必须同事务写一条 order_transitions")
	}
	if f.auditRows[0].Actor != "chain:abc" {
		t.Fatalf("actor 应当是 chain:<txid>，实际 %q", f.auditRows[0].Actor)
	}
	// 写销必须留下账 —— 那笔差额是真的损失，不记就成了一笔查不出来的缺口。
	var sawWriteoff bool
	for _, e := range f.entries {
		if strings.HasPrefix(e.EntryNo, "WOF-") {
			sawWriteoff = true
		}
	}
	if !sawWriteoff {
		t.Fatal("A 档写销必须记 expense:payment_shortfall")
	}
}

func TestProcessDepositUnderpaidTiers(t *testing.T) {
	// B 档（2–5 USDT）与 C 档（>5 USDT）都落 underpaid，但用户看到的文案不同 ——
	// 文案在收银台侧，这里断言状态机不把它们并成 paid。
	for name, received := range map[string]int64{
		"B 档 · 人工队列": 6_500_000, // 差 3.5 USDT
		"C 档 · 提示补足": 3_000_000, // 差 7.0 USDT
	} {
		f := depositFixture(dbgen.OrderStatusPaying, 10_000_000, received)
		if err := processDeposit(context.Background(), f, testLogger(), depositIn(received)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(f.transitions) != 1 || f.transitions[0].ToStatus != dbgen.OrderStatusUnderpaid {
			t.Fatalf("%s：应当迁移到 underpaid，实际 %+v", name, f.transitions)
		}
	}
}

// 🔴 任务书点名的两条之一：**回调不可信 → 收到回调后必须反向查单**（api-contract §8.1）。
//
// 这条用例把「回调声称的金额」与「链上的权威金额」拉开 20 倍：
// processDeposit 的输入只有链上那一份，所以入库金额、累计额、状态判定
// 全部只能来自链上。任何一天有人把回调载荷里的数字接进来，这条用例就会炸。
func TestProcessDepositUsesChainAmountNotCallbackClaim(t *testing.T) {
	const (
		expected    = 10_000_000 // 应收 10 USDT
		claimedByGW = 10_000_000 // 回调声称「已全额到账」
		actualChain = 500_000    // 链上实际只有 0.5 USDT
	)
	_ = claimedByGW // 它在本函数里唯一的用途就是**没有用途**：不进任何计算。

	f := depositFixture(dbgen.OrderStatusPaying, expected, actualChain)
	if err := processDeposit(context.Background(), f, testLogger(), depositIn(actualChain)); err != nil {
		t.Fatal(err)
	}

	if len(f.inserted) != 1 || f.inserted[0].AmountUsdt6 == nil || *f.inserted[0].AmountUsdt6 != actualChain {
		t.Fatalf("落库金额必须是链上金额 %d，实际 %v", actualChain, f.inserted)
	}
	if len(f.transitions) != 1 || f.transitions[0].ToStatus != dbgen.OrderStatusUnderpaid {
		t.Fatalf("链上只到了 0.5 USDT，订单必须停在 underpaid（回调说全额到账也不算），实际 %+v", f.transitions)
	}
	for _, tr := range f.transitions {
		if tr.ToStatus == dbgen.OrderStatusPaid {
			t.Fatal("回调声称的金额把订单推成了 paid —— 这正是易支付回调伪造漏洞的形状")
		}
	}
	// 折算成人民币的那一列同样只能来自链上金额。
	if len(f.attributed) != 1 || f.attributed[0].AmountCnyCents == nil {
		t.Fatal("必须落 amount_cny_cents（记录用）")
	}
	if want := usdt6ToCents(actualChain, 71500); *f.attributed[0].AmountCnyCents != want {
		t.Fatalf("amount_cny_cents 应当按链上金额折算得 %d，实际 %d", want, *f.attributed[0].AmountCnyCents)
	}
}

// 🔴 静默边界：伪造的回调可能声称一个**不是我们的**收款地址。
// 先插 payments 再判归属的话，任何人都能往 payments_unmatched_idx 这条
// 「人工队列」里塞行 —— 而那张队列的正常结果集应当是空的，它的成本不能由攻击者定。
func TestProcessDepositRejectsForeignAddress(t *testing.T) {
	f := depositFixture(dbgen.OrderStatusPaying, 10_000_000, 0)
	f.payAddrErr = pgx.ErrNoRows

	err := processDeposit(context.Background(), f, testLogger(), depositIn(1_000_000))
	if !errors.Is(err, errDepositForeignAddress) {
		t.Fatalf("不是我们的地址必须被识别出来，实际 %v", err)
	}
	if len(f.inserted) != 0 {
		t.Fatal("不是我们的地址就不该在 payments 里留下任何行")
	}
	if len(f.transitions) != 0 {
		t.Fatal("更不该动订单状态")
	}
}

// 分支 ②：是我们的地址，但订单侧对不上。**钱照收**，进人工队列。
func TestProcessDepositUnmatchedOrderGoesToQueue(t *testing.T) {
	f := depositFixture(dbgen.OrderStatusPaying, 10_000_000, 0)
	f.orderErr = pgx.ErrNoRows

	if err := processDeposit(context.Background(), f, testLogger(), depositIn(1_000_000)); err != nil {
		t.Fatal(err)
	}
	if len(f.inserted) != 1 {
		t.Fatal("钱照收：流水必须落库")
	}
	if len(f.attributed) != 1 || f.attributed[0].OrderID != nil {
		t.Fatalf("归属不到订单时 order_id 必须保持 NULL，实际 %+v", f.attributed)
	}
	if f.attributed[0].AmlVerdict == nil || *f.attributed[0].AmlVerdict != "quarantined" {
		t.Fatal("未归属的到账必须标 quarantined 进人工队列")
	}
	if len(f.transitions) != 0 {
		t.Fatal("找不到订单就不该动任何订单状态")
	}
}

// 分支 0 的另一半：同一笔钱被重扫（游标回看 10 分钟就是靠它兜底）。
func TestProcessDepositIsIdempotent(t *testing.T) {
	f := depositFixture(dbgen.OrderStatusPaying, 10_000_000, 10_000_000)
	f.insertErr = pgx.ErrNoRows // ON CONFLICT DO NOTHING 命中
	f.existing = dbgen.Payment{ID: 7, EnteredBy: "scanner"}

	if err := processDeposit(context.Background(), f, testLogger(), depositIn(10_000_000)); err != nil {
		t.Fatal(err)
	}
	if len(f.attributed) != 0 {
		t.Fatal("已入账的钱不得重复归属")
	}
	if len(f.transitions) != 0 {
		t.Fatal("已入账的钱不得重复推进订单状态（开通挂在这次迁移上）")
	}
	if len(f.entries) != 0 {
		t.Fatal("已入账的钱不得重复记账")
	}
}

// 🔴 分支 ④：订单已过期。**不改订单状态、不回改成 paid**（ADR 0012 §7.3）——
// 那等于用一个已经过期的汇率开通，汇率敞口由我们承担。
// 钱必须入余额；不做这一条，用户第一次付款的钱就真的进黑洞。
func TestProcessDepositExpiredOrderCreditsWallet(t *testing.T) {
	f := depositFixture(dbgen.OrderStatusExpired, 10_000_000, 10_000_000)
	if err := processDeposit(context.Background(), f, testLogger(), depositIn(10_000_000)); err != nil {
		t.Fatal(err)
	}
	if len(f.transitions) != 0 {
		t.Fatalf("过期订单的状态不得被改动，实际 %+v", f.transitions)
	}
	if len(f.walletTopups) != 1 {
		t.Fatalf("过期订单的到账必须入余额，实际 %d 次", len(f.walletTopups))
	}
	got := f.walletTopups[0]
	if got.UserID != 42 || got.Currency != "CNY" {
		t.Fatalf("入余额的对象不对：%+v", got)
	}
	// UpsertWalletBalance 的 balance 参数是**增量**不是绝对值。
	if want := usdt6ToCents(10_000_000, 71500); got.Balance != want {
		t.Fatalf("入余额金额应当是 %d 分，实际 %d", want, got.Balance)
	}
	if len(f.payerAddr) != 1 {
		t.Fatal("必须回填付款方地址（扫链是唯一看得见付款方的地方）")
	}
}

// 分支 ⑤：超额支付同样入余额（§6.2）。
//
// 🔴 静默边界的另一半在下面那个子用例里：**一次转账就多付**的情况。
// 它落在 A 档（shortfall 为负 ≤ 写销阈值）里，如果只在「订单已 paid」的分支处理超额，
// 这笔钱会停在我们的地址上、没有任何记录指向用户 —— 而它从来不会报错。
func TestProcessDepositOverpayCreditsWallet(t *testing.T) {
	f := depositFixture(dbgen.OrderStatusPaid, 10_000_000, 12_000_000)
	if err := processDeposit(context.Background(), f, testLogger(), depositIn(12_000_000)); err != nil {
		t.Fatal(err)
	}
	if len(f.transitions) != 0 {
		t.Fatal("已付清的订单状态不该再动")
	}
	if len(f.walletTopups) != 1 {
		t.Fatal("超额部分必须入余额")
	}
}

func TestProcessDepositOverpayInOneTransfer(t *testing.T) {
	// 应收 10 USDT，一次转来 12 USDT：订单转 paid，多出的 2 USDT 入余额。
	f := depositFixture(dbgen.OrderStatusPaying, 10_000_000, 12_000_000)
	if err := processDeposit(context.Background(), f, testLogger(), depositIn(12_000_000)); err != nil {
		t.Fatal(err)
	}
	if len(f.transitions) != 1 || f.transitions[0].ToStatus != dbgen.OrderStatusPaid {
		t.Fatalf("多付也是付清，应当转 paid：%+v", f.transitions)
	}
	if len(f.walletTopups) != 1 {
		t.Fatal("多出来的 2 USDT 必须入余额，不能停在我们的地址上")
	}
	if want := usdt6ToCents(2_000_000, 71500); f.walletTopups[0].Balance != want {
		t.Fatalf("入余额的应当只是超出部分（%d 分），实际 %d", want, f.walletTopups[0].Balance)
	}

	// 0.01 USDT 以内的多付来自我们自己的向上取整，不值得为它开一条路径（§6.2）。
	f2 := depositFixture(dbgen.OrderStatusPaying, 10_000_000, 10_005_000)
	if err := processDeposit(context.Background(), f2, testLogger(), depositIn(10_005_000)); err != nil {
		t.Fatal(err)
	}
	if len(f2.walletTopups) != 0 {
		t.Fatal("取整误差量级的多付不该触发入余额")
	}
}

func TestProcessDepositNoExpectedAmountNeedsHuman(t *testing.T) {
	// 订单没有应收金额（pay_amount_usdt6 是判定的唯一依据）。
	// 钱记下来了，但没人能判断够不够 —— 必须停在原地等人看，而不是猜一个结论。
	f := depositFixture(dbgen.OrderStatusPaying, 0, 5_000_000)
	f.order.PayAmountUsdt6 = nil
	if err := processDeposit(context.Background(), f, testLogger(), depositIn(5_000_000)); err != nil {
		t.Fatal(err)
	}
	if len(f.inserted) != 1 {
		t.Fatal("钱必须记下来")
	}
	if len(f.transitions) != 0 {
		t.Fatal("判不了就不许推进状态")
	}
}

// 🔴 静默边界：aml_verdict 在入账时留 NULL（= 尚未判定）。
// 收银台与累计口径用的是 `coalesce(aml_verdict,'clean') <> 'blacklisted'`，
// 所以 NULL 会被正确地计入；写成 'clean' 是一句谎话（AML Layer 1 本轮没实现），
// 而 SQL 那边写成 `aml_verdict <> 'blacklisted'` 会把用户的钱从页面上抹掉。
func TestProcessDepositLeavesAmlUnjudged(t *testing.T) {
	f := depositFixture(dbgen.OrderStatusPaying, 10_000_000, 10_000_000)
	if err := processDeposit(context.Background(), f, testLogger(), depositIn(10_000_000)); err != nil {
		t.Fatal(err)
	}
	if f.inserted[0].AmlVerdict != nil {
		t.Fatal("入账时 AML 尚未判定，必须留 NULL 而不是谎报 clean")
	}
	if f.inserted[0].State != dbgen.PaymentStatePaid {
		t.Fatal("已固化的到账 payment.state 应当是 paid（TRON 的最终性是固化不是 N 个确认）")
	}
}

func TestTransitionOrderZeroRowsIsNotAnError(t *testing.T) {
	// 别的路径（扫链 / recheck）已经把订单推走了。CAS 影响 0 行是**设计内的常态**：
	// 它恰恰说明 CAS 起了作用，把第二次迁移挡住了。
	f := &fakeDeposit{}
	err := transitionOrder(context.Background(), &zeroRowTransition{fakeDeposit: f},
		1, dbgen.OrderStatusPaying, dbgen.OrderStatusPaid, "chain:x", "r")
	if err != nil {
		t.Fatalf("CAS 未命中不是错误：%v", err)
	}
	if len(f.auditRows) != 0 {
		t.Fatal("没有真的迁移就不该写审计")
	}
}

type zeroRowTransition struct{ *fakeDeposit }

func (z *zeroRowTransition) TransitionOrderStatus(context.Context, dbgen.TransitionOrderStatusParams) (dbgen.TransitionOrderStatusRow, error) {
	return dbgen.TransitionOrderStatusRow{}, pgx.ErrNoRows
}

// ============================================================
// 支付回调验签
// ============================================================

// 🔴 默认验签实现必须对**一切**回调失败。第一阶段没有接任何支付网关，
// 所以没有任何回调可能是真的；fail-open 的后果是任何人 POST 一个 JSON
// 就能触发我们的入账路径 —— 那正是 NewAPI 那次漏洞的形状。
func TestDefaultNotifyVerifierIsFailClosed(t *testing.T) {
	v := defaultNotifyVerifier
	if v.Configured() {
		t.Fatal("第一阶段不接网关，默认实现必须报告未配置")
	}
	eventID, verr := v.Verify(context.Background(), "epay", nil, []byte("{}"))
	if verr == nil {
		t.Fatal("默认实现必须拒绝一切回调")
	}
	if !errors.Is(verr, ErrPaymentNotifyUnverified) {
		t.Fatalf("失败原因必须可判别，实际 %v", verr)
	}
	if eventID != "" {
		t.Fatal("验签失败时不得交出 event_id（它会被当成重放防护的键）")
	}
}

func TestNotifyTradeNoHintIsOnlyAHint(t *testing.T) {
	body := gen.HandlePaymentNotifyJSONRequestBody{
		"out_trade_no": "BP20260830123456",
		"money":        "999999",
		"trade_status": "TRADE_SUCCESS",
	}
	got := notifyTradeNoHint(gen.HandlePaymentNotifyRequestObject{JSONBody: &body})
	if got != "BP20260830123456" {
		t.Fatalf("应当取出订单号线索，实际 %q", got)
	}
	// 载荷里的金额与状态**不该有任何读取入口** —— 这条断言是形状上的：
	// 本文件只暴露一个从载荷里取值的函数，而它只取订单号。
	empty := gen.HandlePaymentNotifyJSONRequestBody{"money": "1", "trade_status": "TRADE_SUCCESS"}
	if notifyTradeNoHint(gen.HandlePaymentNotifyRequestObject{JSONBody: &empty}) != "" {
		t.Fatal("没有订单号线索时必须返回空串，让反查直接失败")
	}
}
