package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/audit"
	"github.com/oratis/babelplus/api/internal/gen"
	mw "github.com/oratis/babelplus/api/internal/middleware"
)

// 管理面「订单与支付」这一组的测试。
//
// 打的是**吃窄接口的自由函数**与**纯函数**，不是 handler 方法 ——
// Server.db 是具体类型 *store.Store，塞不了假实现（order_test.go / task_test.go 同）。
// handler 方法这一层只断言「确实实现了」：Server 内嵌 Unimplemented，
// 漏实现不会在编译期暴露，只会悄悄退回 501。
//
// # 本文件里每一类用例为什么必须存在
//
//	TestAdminDangerousOpsRefuseIncompleteRequests
//	  🔴 任务书的硬要求，也是本文件存在的主要理由：**参数没收齐时不许提交**。
//	  四层强制里任何一层没过，都必须在**任何写入发生之前**停下来。
//	  写成表驱动是刻意的 —— 将来加一条危险操作时，漏掉某一层会在这里显形。
//
//	TestAuditWriteFailureRollsBackAdminBusinessWrite
//	  🔴 api-contract §6.3 第 1 条。用真的 audit.InTx + 真的审计条目构造，
//	  断言审计写失败时 Commit 一次都不发生。少了它，「业务成功、审计缺失」
//	  就是一个静默的可能 —— 而一条查不到的 D6 与「没发生过」不可区分。
//
//	TestParseTronEvidence
//	  D6 的幂等键只能从 evidence_url 解出来（契约没有 txid 字段）。
//	  解错的后果不是报错，是**一个永远有效但与链上对不上的幂等键**。
//
//	TestClassifyRefund / TestPlanClawback / TestSummarizeRefundBasis
//	  退款判档、扣减与佣金追回全部属于「算错了不会报错、只会少收/多退钱」那一类。
//
//	TestAdminMarkOrderPaid… / TestAdminRefundOrder… / TestAdminUpdatePayment…
//	  事务体的正常路径与每个错误码分支，且逐条断言**记账科目**与**写入顺序**：
//	  D6 记错科目会把「全系统最大的内部欺诈面」唯一的指示灯关掉，而账仍然是平的。

// ============================================================
// 本组 operation 必须真的落在 Server 上
// ============================================================

func TestAdminOrderPaymentOperationsAreImplemented(t *testing.T) {
	var s any = &Server{}
	if _, ok := s.(interface {
		ListAdminOrders(context.Context, gen.ListAdminOrdersRequestObject) (gen.ListAdminOrdersResponseObject, error)
		GetAdminOrder(context.Context, gen.GetAdminOrderRequestObject) (gen.GetAdminOrderResponseObject, error)
		MarkAdminOrderPaid(context.Context, gen.MarkAdminOrderPaidRequestObject) (gen.MarkAdminOrderPaidResponseObject, error)
		RefundAdminOrder(context.Context, gen.RefundAdminOrderRequestObject) (gen.RefundAdminOrderResponseObject, error)
		ListAdminPayments(context.Context, gen.ListAdminPaymentsRequestObject) (gen.ListAdminPaymentsResponseObject, error)
		ListAdminUnderpaidPayments(context.Context, gen.ListAdminUnderpaidPaymentsRequestObject) (gen.ListAdminUnderpaidPaymentsResponseObject, error)
		UpdateAdminPayment(context.Context, gen.UpdateAdminPaymentRequestObject) (gen.UpdateAdminPaymentResponseObject, error)
	}); !ok {
		t.Fatal("管理面订单/支付这一组里有 operation 没有被 Server 覆盖，仍落在 Unimplemented 的 501 上")
	}
}

// 这七个 operation 一个都不能出现在免登录表里 —— 它们读写的是全系统的钱。
func TestAdminOrderOperationsAreNotPublic(t *testing.T) {
	for _, name := range []string{
		"ListAdminOrders", "GetAdminOrder", "MarkAdminOrderPaid", "RefundAdminOrder",
		"ListAdminPayments", "ListAdminUnderpaidPayments", "UpdateAdminPayment",
	} {
		if PublicOperations[name] {
			t.Fatalf("%s 绝不能免登录", name)
		}
	}
}

// ============================================================
// 四层强制的守卫（纯函数）
// ============================================================

func adminTestAuth(perms mw.AdminPerms) *mw.AdminAuth {
	return &mw.AdminAuth{AdminID: 7, Email: "ops@babel.plus", Role: mw.RoleOwner, Perms: perms}
}

func TestGuardAdminReasonCountsRunesNotBytes(t *testing.T) {
	// 🔴 中文一个字三个字节。按字节算的话「补单」两个字就有 6 字节、
	//    「链上已确认」五个字 15 字节直接过关 —— 而 L2 要的是「写清楚为什么」。
	if g := guardAdminReason("链上已确认"); g == nil {
		t.Fatal("五个汉字（15 字节）必须被拒：L2 是按字符数不是字节数")
	}
	if g := guardAdminReason("链上到账已核对无误补单"); g != nil {
		t.Fatalf("十一个汉字应当通过：%s", g.Message)
	}
	if g := guardAdminReason("        "); g == nil {
		t.Fatal("八个空格必须被拒：它在审计日志里与没填等价")
	}
	if g := guardAdminReason("abcdefg"); g == nil {
		t.Fatal("七个字符必须被拒")
	}
	if g := guardAdminReason("abcdefgh"); g != nil {
		t.Fatalf("恰好八个字符应当通过：%s", g.Message)
	}
	g := guardAdminReason("短")
	if g.Layer != "L2" || g.Status != 422 {
		t.Fatalf("L2 被拒必须是 422，实为 %d / %s", g.Status, g.Layer)
	}
}

func TestGuardAdminConfirmationIsServerSideAndDoesNotEchoExpected(t *testing.T) {
	if g := guardAdminConfirmation("user@example.com", "user@example.com"); g != nil {
		t.Fatal("逐字相同必须通过")
	}
	// 邮箱在 users_email_uk 上按 lower(email) 唯一；因为大小写不同而拒绝一次 D6，
	// 换来的不是安全，是运维绕过流程。
	if g := guardAdminConfirmation("User@Example.com", "  user@example.com  "); g != nil {
		t.Fatal("大小写与首尾空白归一之后应当通过")
	}
	g := guardAdminConfirmation("user@example.com", "other@example.com")
	if g == nil {
		t.Fatal("确认串不匹配必须被拒")
	}
	if g.Layer != "L1" || g.Status != 422 {
		t.Fatalf("L1 被拒必须是 422，实为 %d / %s", g.Status, g.Layer)
	}
	// 🔴 回显期望值等于把 L1 从「你必须先知道这张单属于谁」降级成
	//    「先发一次错的，服务端会告诉你答案」。
	blob := g.Message
	for _, d := range g.Details {
		blob += d.Field + d.Reason
	}
	if strings.Contains(blob, "user@example.com") {
		t.Fatalf("L1 的拒绝响应绝不能回显期望的邮箱：%q", blob)
	}
	// 期望值为空时必须拒绝，不能因为「两边都空」而通过。
	if g := guardAdminConfirmation("", ""); g == nil {
		t.Fatal("期望值为空时必须拒绝")
	}
}

func TestGuardAdminPermissionDeniesByDefaultAndNamesTheContractGap(t *testing.T) {
	// 默认零值 = 一个权限位都没有。这是 admin_users 的 DDL 默认值，也是这里的默认结论。
	none := adminTestAuth(mw.AdminPerms{})
	if g := guardAdminPermission(none, mw.PermMarkOrderPaid, "手工标记订单已支付", ""); g == nil {
		t.Fatal("没有权限位时必须拒绝（默认不授予，即使团队只有一个人）")
	} else if g.Layer != "L4" || g.Status != 403 || g.Code != gen.AUTHPERMISSIONDENIED {
		t.Fatalf("L4 被拒必须是 403 AUTH_PERMISSION_DENIED，实为 %d / %s", g.Status, g.Code)
	}
	if g := guardAdminPermission(adminTestAuth(mw.AdminPerms{MarkOrderPaid: true}), mw.PermMarkOrderPaid, "手工标记订单已支付", ""); g != nil {
		t.Fatalf("持有权限位时应当放行：%s", g.Message)
	}
	// perm_refund 在契约的 AdminPermission 枚举里没有值 —— 运维会在页面上
	// 反复找一个不存在的开关，除非拒绝文案告诉他去哪儿开。
	g := guardAdminPermission(none, mw.PermRefund, "退款", "admin_users.perm_refund")
	if !strings.Contains(g.Message, "admin_users.perm_refund") {
		t.Fatalf("契约缺口必须写进拒绝文案：%q", g.Message)
	}
	// nil 身份必须被拒而不是 panic：Can 对 nil 接收者返回 false 是它的合约。
	if g := guardAdminPermission(nil, mw.PermRefund, "退款", ""); g == nil {
		t.Fatal("身份缺失时必须拒绝")
	}
}

func TestGuardAdminTotpPresent(t *testing.T) {
	if g := guardAdminTotpPresent("481920"); g != nil {
		t.Fatal("带了码就该放行（真正的校验在 RequireStepUp）")
	}
	for _, code := range []string{"", "   "} {
		g := guardAdminTotpPresent(code)
		if g == nil {
			t.Fatalf("缺 TOTP（%q）必须被拒", code)
		}
		// 缺头与错码必须分开：前端拿到 AUTH_TOTP_REQUIRED 才知道要弹输入框。
		if g.Code != gen.AUTHTOTPREQUIRED || g.Status != 403 {
			t.Fatalf("缺 TOTP 必须是 403 AUTH_TOTP_REQUIRED，实为 %d / %s", g.Status, g.Code)
		}
	}
}

// 🔴 任务书的硬要求：**每条危险操作都要有一个「参数没收齐时不许提交」的用例。**
//
// 这里把四层拆成四个缺口，逐个断言它会被挡下来。表驱动是为了让
// 「将来新增一条危险操作却漏了某一层」在这里显形。
func TestAdminDangerousOpsRefuseIncompleteRequests(t *testing.T) {
	full := mw.AdminPerms{MarkOrderPaid: true, Refund: true}

	cases := []struct {
		name      string
		op        string
		guard     *adminGuardFailure
		wantLayer string
		wantCode  gen.ErrorCode
	}{
		{
			name: "D6 权限位不足", op: "mark_paid",
			guard:     guardAdminPermission(adminTestAuth(mw.AdminPerms{Refund: true}), mw.PermMarkOrderPaid, "手工标记订单已支付", ""),
			wantLayer: "L4", wantCode: gen.AUTHPERMISSIONDENIED,
		},
		{
			name: "D6 reason 少于 8 字符", op: "mark_paid",
			guard:     guardAdminReason("补单"),
			wantLayer: "L2", wantCode: gen.VALIDATIONFAILED,
		},
		{
			name: "D6 确认串不匹配", op: "mark_paid",
			guard:     guardAdminConfirmation("owner@example.com", "someone-else@example.com"),
			wantLayer: "L1", wantCode: gen.VALIDATIONFAILED,
		},
		{
			name: "D6 缺 TOTP", op: "mark_paid",
			guard:     guardAdminTotpPresent(""),
			wantLayer: "L3", wantCode: gen.AUTHTOTPREQUIRED,
		},
		{
			name: "D7 权限位不足", op: "refund",
			guard:     guardAdminPermission(adminTestAuth(mw.AdminPerms{MarkOrderPaid: true}), mw.PermRefund, "退款", "admin_users.perm_refund"),
			wantLayer: "L4", wantCode: gen.AUTHPERMISSIONDENIED,
		},
		{
			name: "D7 reason 少于 8 字符", op: "refund",
			guard:     guardAdminReason("退了"),
			wantLayer: "L2", wantCode: gen.VALIDATIONFAILED,
		},
		{
			name: "D13 reason 少于 8 字符", op: "payment_patch",
			guard:     guardAdminReason("改一下"),
			wantLayer: "L2", wantCode: gen.VALIDATIONFAILED,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.guard == nil {
				t.Fatal("这条守卫放行了一个参数没收齐的危险操作")
			}
			if tc.guard.Layer != tc.wantLayer {
				t.Fatalf("层次错了：want %s got %s", tc.wantLayer, tc.guard.Layer)
			}
			if tc.guard.Code != tc.wantCode {
				t.Fatalf("错误码错了：want %s got %s", tc.wantCode, tc.guard.Code)
			}
			if e := tc.guard.opError(); e.Status != tc.guard.Status || e.Layer != tc.wantLayer {
				t.Fatal("守卫转成 opError 之后丢了状态或层次")
			}
		})
	}

	// 反面锚点：四层都齐时，四个守卫一个都不该拦。
	if guardAdminPermission(adminTestAuth(full), mw.PermMarkOrderPaid, "手工标记订单已支付", "") != nil ||
		guardAdminReason("链上到账已核对无误，网关回调丢失") != nil ||
		guardAdminConfirmation("owner@example.com", "owner@example.com") != nil ||
		guardAdminTotpPresent("481920") != nil {
		t.Fatal("四层都满足时不应当有任何守卫拦截")
	}
}

// ============================================================
// evidence_url → txid（ADR 0012 §16.1 与冻结契约的冲突落点）
// ============================================================

func TestParseTronEvidence(t *testing.T) {
	const txid = "7f3a2c9b1d4e6f80a1b2c3d4e5f60718293a4b5c6d7e8f9012345678abcdef01"

	cases := []struct {
		name         string
		in           string
		wantOK       bool
		wantTx       string
		wantIndex    int32
		wantExplicit bool
	}{
		{"tronscan 的 hash 路由", "https://tronscan.org/#/transaction/" + txid, true, txid, 0, false},
		{"带显式 log_index 后缀", "https://tronscan.org/#/transaction/" + txid + ":3", true, txid, 3, true},
		{"带 log_index 查询参数", "https://tronscan.org/#/transaction/" + txid + "?log_index=2", true, txid, 2, true},
		{"REST 形态的路径", "https://example.org/tx/" + txid, true, txid, 0, false},
		{"裸交易哈希", "  " + strings.ToUpper(txid) + "  ", true, txid, 0, false},
		// 🔴 契约给的示例本身就是省略号形态（`.../transaction/7f3a…`）——
		//    它必须被拒。放过它就等于放过一个「看起来像 txid」的幂等键。
		{"契约示例里的省略哈希", "https://tronscan.org/#/transaction/7f3a", false, "", 0, false},
		{"非十六进制", "https://tronscan.org/#/transaction/" + strings.Repeat("z", 64), false, "", 0, false},
		{"空串", "   ", false, "", 0, false},
		{"负的 log_index", "https://tronscan.org/#/transaction/" + txid + ":-1", false, "", 0, false},
		{"log_index 不是数字", "https://tronscan.org/#/transaction/" + txid + ":abc", false, "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseTronEvidence(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok: want %v got %v (%+v)", tc.wantOK, ok, got)
			}
			if !ok {
				return
			}
			if got.TxID != tc.wantTx {
				t.Fatalf("txid: want %s got %s", tc.wantTx, got.TxID)
			}
			if got.LogIndex != tc.wantIndex || got.LogIndexExplicit != tc.wantExplicit {
				t.Fatalf("log_index: want %d/%v got %d/%v",
					tc.wantIndex, tc.wantExplicit, got.LogIndex, got.LogIndexExplicit)
			}
		})
	}
}

// ============================================================
// 搜索串转义
// ============================================================

func TestAdminSearchPatternEscapesWildcards(t *testing.T) {
	// 🔴 不转义的话，一个 `%` 就是「返回全部订单」——
	//    而搜索框里输入 `%` 的人只是想搜一个百分号。
	got := adminSearchPattern(strPtr("%"))
	if got == nil || *got != `%\%%` {
		t.Fatalf(`"%%" 必须被转义成 "%%\%%%%"，实为 %v`, got)
	}
	// 反斜杠必须**先**换，否则后面加进去的反斜杠会被再转义一次。
	got = adminSearchPattern(strPtr(`a\_b`))
	if got == nil || *got != `%a\\\_b%` {
		t.Fatalf(`反斜杠与下划线的转义顺序错了：%v`, got)
	}
	if adminSearchPattern(nil) != nil || adminSearchPattern(strPtr("   ")) != nil {
		t.Fatal("空搜索必须是 nil（SQL 侧据此整条不过滤），不是空模式串")
	}
}

func strPtr(s string) *string { return &s }

// ============================================================
// 退款基数与判档
// ============================================================

func refundBasisRow(vWindow, consumedTime, consumedData, refundB, planUsed, quota int64, price *int64) dbgen.GetRefundBasisRow {
	return dbgen.GetRefundBasisRow{
		VWindow: vWindow, ConsumedTime: consumedTime, ConsumedData: consumedData,
		RefundB: refundB, PlanUsed: planUsed, TransferEnablePlan: quota,
		PriceMonthlyAtOrder: price,
	}
}

func TestSummarizeRefundBasisFlagsMissingPriceSnapshot(t *testing.T) {
	price := int64(3580)
	if _, err := summarizeRefundBasis(nil); err == nil {
		t.Fatal("0 行必须报错：锚点订单刚刚才被加锁读到，不可能没有窗口段")
	}
	s, err := summarizeRefundBasis([]dbgen.GetRefundBasisRow{
		refundBasisRow(10000, 2000, 1000, 7000, 1<<30, 100<<30, &price),
		refundBasisRow(10000, 2000, 1000, 7000, 1<<30, 100<<30, &price),
	})
	if err != nil || s.VWindow != 10000 || s.RefundB != 7000 || s.Segments != 2 {
		t.Fatalf("汇总不对：%+v %v", s, err)
	}
	if s.MissingPriceSnapshot {
		t.Fatal("两段都有快照时不该报缺快照")
	}
	// 🔴 一段缺月付标价快照 → consumed_time 少算，方向对我们不利且**没有任何迹象**。
	s, _ = summarizeRefundBasis([]dbgen.GetRefundBasisRow{
		refundBasisRow(10000, 2000, 1000, 7000, 0, 100<<30, &price),
		refundBasisRow(10000, 2000, 1000, 7000, 0, 100<<30, nil),
	})
	if !s.MissingPriceSnapshot {
		t.Fatal("任一段缺 price_monthly_at_order 都必须被标出来")
	}
}

func classAInput(now time.Time) classifyRefundInput {
	price := int64(3580)
	rows := []dbgen.GetRefundBasisRow{refundBasisRow(35800, 1000, 200, 34600, 1<<30, 100<<30, &price)}
	b, _ := summarizeRefundBasis(rows)
	return classifyRefundInput{
		OrderType: dbgen.OrderTypeNew, OrderStatus: dbgen.OrderStatusCompleted,
		CoversFrom:        pgtype.Timestamptz{Time: now.Add(-48 * time.Hour), Valid: true},
		SettledOrderCount: 1, CoolingOffUsed: 0, UserBanned: false,
		AlreadyRefunded: 0, Basis: b, Now: now,
	}
}

func TestClassifyRefundClassAGates(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	d, e := classifyRefund(classAInput(now))
	if e != nil {
		t.Fatalf("五道闸门全过应当是 Class A：%s", e.Message)
	}
	if d.Rule != refundRuleCoolingOff || d.MaxAmount != 35800 {
		// Class A 豁免 consumed_time 与 consumed_data 两项扣减，直接退 V_window。
		t.Fatalf("Class A 应当退全额 V_window=35800：%+v", d)
	}

	// 逐条打断五道闸门，每一条都必须把它降级成按比例退款。
	breakers := map[string]func(*classifyRefundInput){
		"first_settled_order": func(in *classifyRefundInput) { in.SettledOrderCount = 2 },
		"within_7_days": func(in *classifyRefundInput) {
			in.CoversFrom = pgtype.Timestamptz{Time: now.Add(-8 * 24 * time.Hour), Valid: true}
		},
		"traffic_under_cap":    func(in *classifyRefundInput) { in.Basis.PlanUsed = 50 << 30 },
		"not_banned":           func(in *classifyRefundInput) { in.UserBanned = true },
		"no_prior_cooling_off": func(in *classifyRefundInput) { in.CoolingOffUsed = 1 },
	}
	for gate, breaker := range breakers {
		t.Run("闸门 "+gate+" 不满足时降级为按比例", func(t *testing.T) {
			in := classAInput(now)
			breaker(&in)
			d, e := classifyRefund(in)
			if e != nil {
				t.Fatalf("不应当整体拒绝：%s", e.Message)
			}
			if d.Rule != refundRuleProrated || d.MaxAmount != 34600 {
				t.Fatalf("应当降级成 prorated / refund_b=34600：%+v", d)
			}
			if d.ClassAGates[gate] {
				t.Fatalf("闸门 %s 应当记为未通过", gate)
			}
		})
	}

	// 🔴 流量闸门是 min(套餐配额 10%, 10 GiB) —— 大配额下 10 GiB 才是有效上界。
	in := classAInput(now)
	in.Basis.TransferEnablePlan = 1000 << 30 // 10% = 100 GiB
	in.Basis.PlanUsed = 11 << 30             // 超过 10 GiB 的绝对上界
	if d, _ := classifyRefund(in); d.Rule != refundRuleProrated {
		t.Fatal("超过 10 GiB 绝对上界必须降级，哪怕它不到配额的 10%")
	}
}

func TestClassifyRefundClassCAndAlreadyRefunded(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	// Class C：一次性商品一律不退，且不该走到退款基数计算。
	for _, ot := range []dbgen.OrderType{dbgen.OrderTypeTrafficPack, dbgen.OrderTypeResetPack, dbgen.OrderTypeWalletTopup} {
		in := classAInput(now)
		in.OrderType = ot
		if _, e := classifyRefund(in); e == nil || e.Status != 422 {
			t.Fatalf("%s 必须 422 拒绝", ot)
		}
	}

	// 还没收到钱的订单没有可退款项。
	for _, st := range []dbgen.OrderStatus{
		dbgen.OrderStatusPending, dbgen.OrderStatusPaying, dbgen.OrderStatusUnderpaid,
		dbgen.OrderStatusCancelled, dbgen.OrderStatusExpired, dbgen.OrderStatusRefunded,
	} {
		in := classAInput(now)
		in.OrderStatus = st
		if _, e := classifyRefund(in); e == nil || e.Status != 409 {
			t.Fatalf("状态 %s 必须 409 拒绝", st)
		}
	}

	// 缺月付标价快照 → 转人工，不能当成 0 接着算。
	in := classAInput(now)
	in.Basis.MissingPriceSnapshot = true
	if _, e := classifyRefund(in); e == nil || e.Status != 422 {
		t.Fatal("缺月付标价快照必须拒绝并转人工")
	}

	// 🔴 **本文件最重要的一条算术断言**：GetRefundBasis 不看 refunds 表，
	//    所以「此前已退到余额」必须由 handler 扣掉。不扣的话对同一张单
	//    连调两次 partial 退款，第二次仍然算得出同样的额度 —— 同一笔钱退两遍。
	in = classAInput(now)
	in.SettledOrderCount = 2 // 走 prorated，refund_b = 34600
	in.AlreadyRefunded = 30000
	d, e := classifyRefund(in)
	if e != nil {
		t.Fatalf("还剩 4600 分可退，不该拒绝：%s", e.Message)
	}
	if d.MaxAmount != 4600 {
		t.Fatalf("可退上限必须扣掉已退的 30000：want 4600 got %d", d.MaxAmount)
	}

	// 扣到 0 或负数就是 Class C 的「不予退款」，且拒绝里必须带扣减明细。
	in.AlreadyRefunded = 34600
	_, e = classifyRefund(in)
	if e == nil || e.Status != 422 {
		t.Fatal("没有可退金额时必须 422")
	}
	fields := map[string]bool{}
	for _, d := range e.Details {
		fields[d.Field] = true
	}
	for _, want := range []string{"v_window", "consumed_time", "consumed_data", "refund_b", "already_refunded", "rule"} {
		if !fields[want] {
			t.Fatalf("拒绝响应必须把扣减明细算给操作者看，缺 %s：%+v", want, e.Details)
		}
	}
}

// ============================================================
// 佣金追回（ADR 0013 §3.5 第 4 步）
// ============================================================

func TestPlanClawback(t *testing.T) {
	rows := []dbgen.AdminListOrderCommissionsForClawbackRow{
		{ID: 1, InviterID: 11, Status: "pending", Amount: 3580, ClawbackAmount: 3580, InviterBalance: 0, OrderAmountPaid: 35800},
		{ID: 2, InviterID: 12, Status: "confirmed", Amount: 3580, ClawbackAmount: 1790, InviterBalance: 0, OrderAmountPaid: 35800},
		{ID: 3, InviterID: 13, Status: "transferred", Amount: 3580, ClawbackAmount: 3580, InviterBalance: 9999, OrderAmountPaid: 35800},
		{ID: 4, InviterID: 14, Status: "transferred", Amount: 3580, ClawbackAmount: 3580, InviterBalance: 1000, OrderAmountPaid: 35800},
		{ID: 5, InviterID: 15, Status: "transferred", Amount: 3580, ClawbackAmount: 3580, InviterBalance: 5000, OrderAmountPaid: 0},
	}
	got := planClawback(rows)
	if len(got) != 5 {
		t.Fatalf("五条佣金应当有五个处置：%d", len(got))
	}
	// pending / confirmed 的钱**还没进过 wallet_balances**（佣金是在划转那一刻
	// 才写分录 + 加余额的）。对它们扣余额就是从用户手里拿走另一笔钱。
	if !got[0].Void || !got[1].Void {
		t.Fatal("pending / confirmed 必须直接作废，不扣余额")
	}
	if got[0].Recover != 0 || got[1].Recover != 0 {
		t.Fatal("作废路径不该动余额")
	}
	// transferred 且余额够：全额扣回。
	if got[2].Void || got[2].Recover != 3580 || got[2].Shortfall != 0 {
		t.Fatalf("余额充足时应当全额追回：%+v", got[2])
	}
	// 🔴 余额不够：能扣多少扣多少，差额是我们的**真实损失**，必须显式记下来。
	//    不先看余额就去扣会撞 CHECK (balance >= 0)，那会**回滚整个退款事务** ——
	//    于是「佣金追不回来」这件小事变成「退款做不成」。
	if got[3].Recover != 1000 || got[3].Shortfall != 2580 {
		t.Fatalf("余额不足时应当部分追回并记差额：%+v", got[3])
	}
	// 🔴 amount_paid = 0 而又生成了佣金是一个数据错误。SQL 里的 greatest(1, ·)
	//    只防除零，此时退化成全额追回 —— 被除零保护吞掉就永远不会被发现。
	if !got[4].ZeroBase {
		t.Fatal("amount_paid = 0 必须被标出来告警")
	}
	if got[0].ZeroBase {
		t.Fatal("正常订单不该被标成 zero base")
	}
}

// ============================================================
// 假数据面
// ============================================================

// adminOpsFake 同时满足 D6 / D7 / D13 三个窄接口。
// 一个假实现覆盖三条路径，是为了让「某个方法被调了几次、参数是什么」
// 可以在同一个断言集合里比较 —— 三条路径共享账本与状态机。
type adminOpsFake struct {
	// 输入
	markPaidRow     dbgen.AdminGetOrderForMarkPaidRow
	markPaidErr     error
	refundRow       dbgen.AdminGetOrderForRefundRow
	refundErr       error
	user            dbgen.User
	userErr         error
	basisRows       []dbgen.GetRefundBasisRow
	commissions     []dbgen.AdminListOrderCommissionsForClawbackRow
	detailRow       dbgen.AdminGetOrderRow
	paymentRow      dbgen.AdminGetPaymentForUpdateRow
	paymentErr      error
	insertPaymentNo bool // true = 撞幂等锁（ErrNoRows）
	transitionNo    bool // true = CAS 影响 0 行
	createRefundErr error
	terminatePack   *int64 // 非 nil 时用它覆盖终止订阅返回的 pack 值
	walletErr       error
	walletErrUser   int64 // 非 0 时只让这个用户的余额写入失败
	voidNo          bool

	// 输出
	entries      []ledgerEntrySpec
	lines        []dbgen.CreateLedgerLineParams
	inserted     *dbgen.InsertPaymentIfNewParams
	attributed   *dbgen.AttributePaymentParams
	recorded     *dbgen.RecordOrderPaymentParams
	transitions  []dbgen.InsertOrderTransitionParams
	casCalls     []dbgen.TransitionOrderStatusParams
	refunds      []dbgen.CreateRefundParams
	refundStatus []dbgen.UpdateRefundStatusParams
	wallets      []dbgen.UpsertWalletBalanceParams
	terminated   int64
	voided       []int64
	patched      []dbgen.AdminUpdatePaymentStateParams
	nextEntryID  int64

	// acctIDs 给每个 code 发一个稳定的 id。
	// ⚠️ 不能用 len(code) 之类的「够用就行」的映射：`asset:crypto:tron:pool` 与
	//    `asset:manual_reconcile` 恰好都是 22 个字符 —— 而这两个科目分错正是
	//    D6 最要命的那个错误，用一个会把它们混为一谈的假实现来测它毫无意义。
	acctIDs   map[string]int64
	acctCodes map[int64]string
}

func (f *adminOpsFake) GetLedgerAccountByCode(_ context.Context, code string) (dbgen.LedgerAccount, error) {
	if f.acctIDs == nil {
		f.acctIDs, f.acctCodes = map[string]int64{}, map[int64]string{}
	}
	id, ok := f.acctIDs[code]
	if !ok {
		id = int64(len(f.acctIDs) + 1)
		f.acctIDs[code], f.acctCodes[id] = id, code
	}
	return dbgen.LedgerAccount{ID: id, Code: code, Currency: "CNY"}, nil
}

func (f *adminOpsFake) CreateLedgerEntry(_ context.Context, arg dbgen.CreateLedgerEntryParams) (dbgen.LedgerEntry, error) {
	f.nextEntryID++
	f.entries = append(f.entries, ledgerEntrySpec{EntryNo: arg.EntryNo, Description: arg.Description})
	return dbgen.LedgerEntry{ID: f.nextEntryID, EntryNo: arg.EntryNo}, nil
}

func (f *adminOpsFake) CreateLedgerLine(_ context.Context, arg dbgen.CreateLedgerLineParams) (dbgen.LedgerLine, error) {
	f.lines = append(f.lines, arg)
	return dbgen.LedgerLine{ID: int64(len(f.lines))}, nil
}

func (f *adminOpsFake) AdminGetOrderForMarkPaid(context.Context, string) (dbgen.AdminGetOrderForMarkPaidRow, error) {
	return f.markPaidRow, f.markPaidErr
}

func (f *adminOpsFake) InsertPaymentIfNew(_ context.Context, arg dbgen.InsertPaymentIfNewParams) (dbgen.Payment, error) {
	if f.insertPaymentNo {
		return dbgen.Payment{}, pgx.ErrNoRows
	}
	cp := arg
	f.inserted = &cp
	return dbgen.Payment{ID: 900, ExternalID: arg.ExternalID}, nil
}

func (f *adminOpsFake) AttributePayment(_ context.Context, arg dbgen.AttributePaymentParams) (dbgen.Payment, error) {
	cp := arg
	f.attributed = &cp
	return dbgen.Payment{ID: arg.PaymentID}, nil
}

func (f *adminOpsFake) RecordOrderPayment(_ context.Context, arg dbgen.RecordOrderPaymentParams) (dbgen.RecordOrderPaymentRow, error) {
	cp := arg
	f.recorded = &cp
	return dbgen.RecordOrderPaymentRow{ID: arg.ID, AmountPaid: arg.AmountPaid}, nil
}

func (f *adminOpsFake) TransitionOrderStatus(_ context.Context, arg dbgen.TransitionOrderStatusParams) (dbgen.TransitionOrderStatusRow, error) {
	f.casCalls = append(f.casCalls, arg)
	if f.transitionNo {
		return dbgen.TransitionOrderStatusRow{}, pgx.ErrNoRows
	}
	return dbgen.TransitionOrderStatusRow{ID: arg.OrderID, Status: arg.ToStatus}, nil
}

func (f *adminOpsFake) InsertOrderTransition(_ context.Context, arg dbgen.InsertOrderTransitionParams) (dbgen.OrderTransition, error) {
	f.transitions = append(f.transitions, arg)
	return dbgen.OrderTransition{ID: int64(len(f.transitions))}, nil
}

func (f *adminOpsFake) AdminGetOrder(context.Context, string) (dbgen.AdminGetOrderRow, error) {
	return f.detailRow, nil
}

func (f *adminOpsFake) AdminGetOrderForRefund(context.Context, string) (dbgen.AdminGetOrderForRefundRow, error) {
	return f.refundRow, f.refundErr
}

func (f *adminOpsFake) GetUserByID(context.Context, int64) (dbgen.User, error) {
	return f.user, f.userErr
}

func (f *adminOpsFake) GetRefundBasis(context.Context, int64) ([]dbgen.GetRefundBasisRow, error) {
	return f.basisRows, nil
}

func (f *adminOpsFake) CreateRefund(_ context.Context, arg dbgen.CreateRefundParams) (dbgen.Refund, error) {
	if f.createRefundErr != nil {
		return dbgen.Refund{}, f.createRefundErr
	}
	f.refunds = append(f.refunds, arg)
	return dbgen.Refund{ID: 555, OrderID: arg.OrderID, Amount: arg.Amount, Rule: arg.Rule}, nil
}

func (f *adminOpsFake) UpdateRefundStatus(_ context.Context, arg dbgen.UpdateRefundStatusParams) (dbgen.Refund, error) {
	f.refundStatus = append(f.refundStatus, arg)
	return dbgen.Refund{ID: arg.ID, Status: arg.Status}, nil
}

func (f *adminOpsFake) UpsertWalletBalance(_ context.Context, arg dbgen.UpsertWalletBalanceParams) (dbgen.WalletBalance, error) {
	if f.walletErr != nil && (f.walletErrUser == 0 || f.walletErrUser == arg.UserID) {
		return dbgen.WalletBalance{}, f.walletErr
	}
	f.wallets = append(f.wallets, arg)
	return dbgen.WalletBalance{UserID: arg.UserID, Balance: arg.Balance}, nil
}

func (f *adminOpsFake) AdminTerminateSubscriptionForRefund(_ context.Context, userID int64) (dbgen.AdminTerminateSubscriptionForRefundRow, error) {
	f.terminated = userID
	pack := f.refundRow.UserTransferEnablePack
	if f.terminatePack != nil {
		pack = *f.terminatePack
	}
	return dbgen.AdminTerminateSubscriptionForRefundRow{
		ID: userID, TransferEnablePlan: 0, TransferEnablePack: pack,
	}, nil
}

func (f *adminOpsFake) AdminListOrderCommissionsForClawback(context.Context, dbgen.AdminListOrderCommissionsForClawbackParams) ([]dbgen.AdminListOrderCommissionsForClawbackRow, error) {
	return f.commissions, nil
}

func (f *adminOpsFake) VoidCommission(_ context.Context, arg dbgen.VoidCommissionParams) (dbgen.Commission, error) {
	if f.voidNo {
		return dbgen.Commission{}, pgx.ErrNoRows
	}
	f.voided = append(f.voided, arg.ID)
	return dbgen.Commission{ID: arg.ID, Status: "voided"}, nil
}

func (f *adminOpsFake) AdminGetPaymentForUpdate(context.Context, int64) (dbgen.AdminGetPaymentForUpdateRow, error) {
	return f.paymentRow, f.paymentErr
}

func (f *adminOpsFake) AdminUpdatePaymentState(_ context.Context, arg dbgen.AdminUpdatePaymentStateParams) (dbgen.AdminUpdatePaymentStateRow, error) {
	f.patched = append(f.patched, arg)
	return dbgen.AdminUpdatePaymentStateRow{
		ID: arg.PaymentID, Provider: f.paymentRow.Provider, ExternalID: f.paymentRow.ExternalID,
		BeforeState: f.paymentRow.State, AfterState: arg.State, Txid: f.paymentRow.Txid,
		ReceivedAt: f.paymentRow.ReceivedAt,
	}, nil
}

// legAmount 找一条腿的金额（按科目 code）。找不到返回 (0,false)。
func (f *adminOpsFake) legAmount(code string) (int64, bool) {
	for _, l := range f.lines {
		if f.acctCodes[l.AccountID] == code {
			return l.Amount, true
		}
	}
	return 0, false
}

// ---- 假 sink ----

type fakeAdminSink struct {
	configured bool
	err        error
	got        []AdminOpRecord
}

func (f *fakeAdminSink) Name() string     { return "fake" }
func (f *fakeAdminSink) Configured() bool { return f.configured }
func (f *fakeAdminSink) Record(_ context.Context, r AdminOpRecord) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, r)
	return nil
}

// ============================================================
// D6 事务体
// ============================================================

const adminTestTxID = "7f3a2c9b1d4e6f80a1b2c3d4e5f60718293a4b5c6d7e8f9012345678abcdef01"

func adminTestMarkPaidFake() *adminOpsFake {
	addr := "TXWatchAddress0000000000000000000"
	chain := "tron"
	expected := int64(10_000_000) // 10 USDT
	return &adminOpsFake{
		markPaidRow: dbgen.AdminGetOrderForMarkPaidRow{
			ID: 42, TradeNo: "20260816T7K2M9Q4", UserID: 8, Type: dbgen.OrderTypeNew,
			Status: dbgen.OrderStatusPaying, AmountDue: 7150, AmountPaid: 0,
			PayAddress: &addr, PayChain: &chain, PayAmountUsdt6: &expected,
			UserEmail: "user@example.com",
		},
		detailRow: dbgen.AdminGetOrderRow{
			ID: 42, TradeNo: "20260816T7K2M9Q4", UserID: 8, Type: dbgen.OrderTypeNew,
			Status: dbgen.OrderStatusPaid, AmountDue: 7150, AmountPaid: 7150,
			UserEmail: "user@example.com",
		},
	}
}

func adminTestMarkPaidInput(sink AdminOpSink) adminMarkPaidInput {
	return adminMarkPaidInput{
		TradeNo: "20260816T7K2M9Q4", Confirmation: "user@example.com",
		Reason:      "链上到账已核对无误，网关回调丢失",
		EvidenceURL: "https://tronscan.org/#/transaction/" + adminTestTxID,
		Evidence:    tronEvidence{TxID: adminTestTxID},
		AdminID:     7, AdminEmail: "ops@babel.plus", RequestID: "req-1",
		Settings: defaultPaymentSettings(), Sink: sink,
		Now: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	}
}

func TestAdminMarkOrderPaidHappyPath(t *testing.T) {
	f := adminTestMarkPaidFake()
	sink := &fakeAdminSink{configured: true}
	view, entry, err := adminMarkOrderPaid(context.Background(), f, testLogger(), adminTestMarkPaidInput(sink))
	if err != nil {
		t.Fatalf("正常路径不该报错：%v", err)
	}

	// ---- 幂等键必须是 txid:log_index（ADR 0012 §16.1）----
	if f.inserted == nil {
		t.Fatal("必须抢一次入账幂等锁（§8.4 分支 0）")
	}
	if f.inserted.ExternalID != adminTestTxID+":0" {
		t.Fatalf("external_id 必须是 txid:log_index：%s", f.inserted.ExternalID)
	}
	if f.inserted.Provider != "chain_tron" || f.inserted.EnteredBy != "admin:7" {
		t.Fatalf("provider / entered_by 不对：%s / %s", f.inserted.Provider, f.inserted.EnteredBy)
	}
	// raw 是 NOT NULL 的取证材料：手工录入的取证材料就是「谁、凭什么、为什么」。
	var raw map[string]any
	if err := json.Unmarshal(f.inserted.Raw, &raw); err != nil {
		t.Fatalf("payments.raw 必须是合法 JSON：%v", err)
	}
	if raw["evidence_url"] == "" || raw["admin_id"] == nil || raw["reason"] == nil {
		t.Fatalf("payments.raw 缺取证字段：%v", raw)
	}

	// ---- 🔴 记账科目：manual_reconcile，不是 tron pool ----
	if _, ok := f.legAmount(acctTronPool); ok {
		t.Fatal("D6 绝不能记进 asset:crypto:tron:pool —— 那会把内部欺诈面唯一的指示灯关掉")
	}
	dr, ok := f.legAmount(acctManualReconcile)
	if !ok {
		t.Fatal("D6 的借方必须是 asset:manual_reconcile（ADR 0012 §16.2）")
	}
	cr, ok := f.legAmount(acctDeferredRevenue)
	if !ok {
		t.Fatal("D6 的贷方必须是 liability:deferred_revenue")
	}
	// 10 USDT × 7.1500 = ¥71.50 = 7150 分。借贷相等（postLedgerEntry 自己也断言了）。
	if dr != 7150 || cr != -7150 {
		t.Fatalf("折算金额不对：Dr %d / Cr %d（10 USDT × 7.15 应当是 7150 分）", dr, cr)
	}

	// ---- amount_paid 必须涨：它是将来退款时佣金追回的基数 ----
	if f.recorded == nil || f.recorded.AmountPaid != 7150 {
		t.Fatalf("必须记订单收款且金额为 7150 分：%+v", f.recorded)
	}

	// ---- CAS 推状态 + order_transitions ----
	if len(f.casCalls) != 1 || f.casCalls[0].FromStatus != dbgen.OrderStatusPaying ||
		f.casCalls[0].ToStatus != dbgen.OrderStatusPaid {
		t.Fatalf("必须走 paying → paid 的 DB 层 CAS：%+v", f.casCalls)
	}
	if len(f.transitions) != 1 || f.transitions[0].Actor != "admin:7" {
		t.Fatalf("必须同事务写一条 order_transitions，且 actor 指认到人：%+v", f.transitions)
	}

	// ---- §16.3 带外留痕 ----
	if len(sink.got) != 1 || sink.got[0].Action != "D6.order.mark_paid" || sink.got[0].AmountCNY != 7150 {
		t.Fatalf("必须同步打一次带外 sink：%+v", sink.got)
	}

	// ---- 审计条目完整（缺任何必填项都会让整个事务回滚）----
	if entry.Action != "D6.order.mark_paid" || entry.TargetType != "order" ||
		entry.TargetID != "20260816T7K2M9Q4" || entry.Reason == "" {
		t.Fatalf("审计条目不完整：%+v", entry)
	}
	before, ok := entry.Before.(adminMarkPaidSnapshot)
	if !ok || before.Status != string(dbgen.OrderStatusPaying) {
		t.Fatalf("审计必须记「当时订单是什么状态」：%+v", entry.Before)
	}
	after, ok := entry.After.(adminMarkPaidSnapshot)
	if !ok || after.Status != string(dbgen.OrderStatusPaid) ||
		after.ExternalID != adminTestTxID+":0" || after.EvidenceURL == "" || after.PostedCNYCents != 7150 {
		t.Fatalf("审计的 after 必须记下 external_id / evidence_url / 入账金额：%+v", entry.After)
	}
	// 🔴 记的必须是「兜底了没有」而不是「解出来了没有」：omitempty 只序列化 true，
	//    写反的话，风险最高的那批 D6（log_index 是猜的）在审计里什么都不显示。
	if !after.LogIndexDefaulted {
		t.Fatal("evidence_url 没给 log_index 时，审计必须把「按 0 兜底」这件事记下来")
	}
	blob, err := json.Marshal(after)
	if err != nil || !strings.Contains(string(blob), "log_index_defaulted") {
		t.Fatalf("兜底标记必须真的出现在序列化后的审计快照里：%s", blob)
	}
	if view.Order.TradeNo != "20260816T7K2M9Q4" {
		t.Fatalf("响应体不对：%+v", view)
	}
	// 🔴 管理面直出库里的原值，不压扁成契约的 6 个值。
	if string(view.Order.Status) != "paid" {
		t.Fatalf("管理面必须直出库里的 order_status：%s", view.Order.Status)
	}
}

// 🔴 「参数没收齐时不许提交」在**事务内**的那一半：
// L1 的权威判定用的是加锁的那一行，不匹配时**任何写入都不许发生**。
func TestAdminMarkOrderPaidRejectsWrongConfirmationBeforeAnyWrite(t *testing.T) {
	f := adminTestMarkPaidFake()
	sink := &fakeAdminSink{configured: true}
	in := adminTestMarkPaidInput(sink)
	in.Confirmation = "attacker@example.com"

	_, _, err := adminMarkOrderPaid(context.Background(), f, testLogger(), in)
	e := asAdminOpError(err)
	if e.Status != 422 || e.Layer != "L1" {
		t.Fatalf("确认串不匹配必须是 L1 / 422：%+v", e)
	}
	if f.inserted != nil || len(f.lines) != 0 || len(f.casCalls) != 0 || len(sink.got) != 0 {
		t.Fatal("L1 没过时不许发生任何写入，也不许打 sink")
	}
}

func TestAdminMarkOrderPaidStateAndIdempotencyGuards(t *testing.T) {
	t.Run("非 paying/underpaid 一律 409", func(t *testing.T) {
		for _, st := range []dbgen.OrderStatus{
			dbgen.OrderStatusPending, dbgen.OrderStatusPaid, dbgen.OrderStatusCompleted,
			dbgen.OrderStatusExpired, dbgen.OrderStatusCancelled, dbgen.OrderStatusRefunded,
		} {
			f := adminTestMarkPaidFake()
			f.markPaidRow.Status = st
			_, _, err := adminMarkOrderPaid(context.Background(), f, testLogger(),
				adminTestMarkPaidInput(&fakeAdminSink{configured: true}))
			if e := asAdminOpError(err); e.Status != 409 {
				t.Fatalf("状态 %s 必须 409，实为 %d", st, e.Status)
			}
			if f.inserted != nil {
				t.Fatalf("状态 %s 被拒时不许写流水", st)
			}
		}
	})

	t.Run("撞入账幂等锁 → 409 而不是重复入账", func(t *testing.T) {
		// 🔴 这正是 §16.1 要求「必须携带真实 txid」买到的性质：
		//    手工与自动天然互斥，点两次 = 第二次撞锁。
		f := adminTestMarkPaidFake()
		f.insertPaymentNo = true
		_, _, err := adminMarkOrderPaid(context.Background(), f, testLogger(),
			adminTestMarkPaidInput(&fakeAdminSink{configured: true}))
		e := asAdminOpError(err)
		if e.Status != 409 || !strings.Contains(e.Message, adminTestTxID) {
			t.Fatalf("撞幂等锁必须 409 并说清是哪一笔：%+v", e)
		}
		if len(f.lines) != 0 {
			t.Fatal("撞锁之后不该再写分录")
		}
	})

	t.Run("CAS 影响 0 行必须失败，不能静默成功", func(t *testing.T) {
		// order.go 的 transitionOrder 把 0 行当成 nil（收款路径上那是常态）；
		// 管理面相反：我们已经写了流水与分录，静默成功就是留下一条自相矛盾的审计。
		f := adminTestMarkPaidFake()
		f.transitionNo = true
		_, _, err := adminMarkOrderPaid(context.Background(), f, testLogger(),
			adminTestMarkPaidInput(&fakeAdminSink{configured: true}))
		if e := asAdminOpError(err); e.Status != 409 {
			t.Fatalf("CAS 0 行必须 409：%+v", e)
		}
	})

	t.Run("订单不存在 → 404", func(t *testing.T) {
		f := adminTestMarkPaidFake()
		f.markPaidErr = pgx.ErrNoRows
		_, _, err := adminMarkOrderPaid(context.Background(), f, testLogger(),
			adminTestMarkPaidInput(&fakeAdminSink{configured: true}))
		if e := asAdminOpError(err); e.Status != 404 {
			t.Fatalf("不存在必须 404：%+v", e)
		}
	})

	t.Run("没有收款地址或应收金额 → 422", func(t *testing.T) {
		f := adminTestMarkPaidFake()
		f.markPaidRow.PayAddress = nil
		if e := asAdminOpError(mustErr(adminMarkOrderPaid(context.Background(), f, testLogger(),
			adminTestMarkPaidInput(&fakeAdminSink{configured: true})))); e.Status != 422 {
			t.Fatalf("缺收款地址必须 422：%+v", e)
		}
		f = adminTestMarkPaidFake()
		f.markPaidRow.PayAmountUsdt6 = nil
		if e := asAdminOpError(mustErr(adminMarkOrderPaid(context.Background(), f, testLogger(),
			adminTestMarkPaidInput(&fakeAdminSink{configured: true})))); e.Status != 422 {
			t.Fatalf("缺应收金额必须 422：%+v", e)
		}
	})
}

// 🔴 ADR 0012 §16.3：带外留痕失败 → **让 D6 失败**。
// 打 sink 放在提交之前，所以 sink 失败会把已经写下去的流水与分录一起回滚。
func TestAdminMarkOrderPaidFailsWhenOutOfBandSinkFails(t *testing.T) {
	f := adminTestMarkPaidFake()
	sink := &fakeAdminSink{configured: true, err: errors.New("sink 打不通")}
	_, _, err := adminMarkOrderPaid(context.Background(), f, testLogger(), adminTestMarkPaidInput(sink))
	if err == nil {
		t.Fatal("带外留痕失败时 D6 必须失败（否则会留下一次没有带外记录的 D6）")
	}
	if e := asAdminOpError(err); e.Status != 500 {
		t.Fatalf("sink 失败应当落到 500：%+v", e)
	}
}

// 默认 sink 必须是「未配置」—— 它是 §16.3 裁决第 2 条的第二道锁。
func TestDefaultAdminOpSinkIsUnconfigured(t *testing.T) {
	if defaultAdminOpSink.Configured() {
		t.Fatal("默认带外 sink 必须是未配置（在它被端到端验证通过之前 D6 不可用）")
	}
	if err := defaultAdminOpSink.Record(context.Background(), AdminOpRecord{}); err == nil {
		t.Fatal("未配置的 sink 必须报错，不能静默成功")
	}
}

func mustErr(_ gen.AdminOrder, _ audit.Entry, err error) error { return err }

// ============================================================
// D7 事务体
// ============================================================

func adminTestRefundFake() *adminOpsFake {
	price := int64(3580)
	return &adminOpsFake{
		refundRow: dbgen.AdminGetOrderForRefundRow{
			ID: 42, TradeNo: "20260816T7K2M9Q4", UserID: 8, Type: dbgen.OrderTypeNew,
			Status: dbgen.OrderStatusCompleted, AmountPaid: 35800,
			CoversFrom:             pgtype.Timestamptz{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Valid: true},
			UserEmail:              "user@example.com",
			UserTransferEnablePlan: 100 << 30,
			UserTransferEnablePack: 20 << 30,
			CoolingOffUsed:         0,
			RefundedToBalance:      0,
			SettledOrderCount:      3, // 不是首单 → 走 prorated
		},
		user:      dbgen.User{ID: 8, Email: "user@example.com"},
		basisRows: []dbgen.GetRefundBasisRow{refundBasisRow(35800, 1000, 200, 34600, 1<<30, 100<<30, &price)},
		detailRow: dbgen.AdminGetOrderRow{
			ID: 42, TradeNo: "20260816T7K2M9Q4", UserID: 8, Type: dbgen.OrderTypeNew,
			Status: dbgen.OrderStatusRefunded, UserEmail: "user@example.com",
		},
	}
}

func adminTestRefundInput() adminRefundInput {
	return adminRefundInput{
		TradeNo: "20260816T7K2M9Q4", Reason: "用户申请退款，已核对使用量",
		AdminID: 7, AdminEmail: "ops@babel.plus",
		Now: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	}
}

func TestAdminRefundOrderHappyPath(t *testing.T) {
	f := adminTestRefundFake()
	view, entry, err := adminRefundOrder(context.Background(), f, testLogger(), adminTestRefundInput())
	if err != nil {
		t.Fatalf("正常路径不该报错：%v", err)
	}

	// ---- 退款记录先写：refunds_cooling_off_once 是真正的闸门 ----
	if len(f.refunds) != 1 {
		t.Fatalf("必须写一条 refunds：%+v", f.refunds)
	}
	r := f.refunds[0]
	if r.Destination != "balance" {
		t.Fatal("🔴 退款一律进不可提现余额（ADR 0013 §3.1），destination 只能是 balance")
	}
	if r.Rule != refundRuleProrated || r.Amount != 34600 {
		t.Fatalf("档位/金额不对：%s / %d（应当是 prorated / refund_b=34600）", r.Rule, r.Amount)
	}
	if r.UserID != 8 {
		t.Fatal("必须显式传 user_id：refunds_cooling_off_once 这条部分唯一索引建在它上面")
	}
	if r.OperatorID == nil || *r.OperatorID != 7 {
		t.Fatal("必须记下操作者")
	}
	// 余额路径是单事务、没有网关往返，不该停在 pending。
	if len(f.refundStatus) != 1 || f.refundStatus[0].Status != "done" {
		t.Fatalf("退款状态必须置为 done：%+v", f.refundStatus)
	}

	// ---- 记账：两条腿，绝不允许碰 expense:refund ----
	dr, ok := f.legAmount(acctDeferredRevenue)
	if !ok || dr != 34600 {
		t.Fatalf("借方必须是 liability:deferred_revenue 34600：%d %v", dr, ok)
	}
	cr, ok := f.legAmount(acctUserWallet)
	if !ok || cr != -34600 {
		t.Fatalf("贷方必须是 liability:user_wallet -34600：%d %v", cr, ok)
	}
	if _, ok := f.legAmount(acctRefundExpense); ok {
		t.Fatal("🔴 destination='balance' 的分录绝不允许碰 expense:refund —— 那会在损益表上凭空造出一笔从未发生的费用")
	}

	// ---- 余额是增量不是绝对值 ----
	if len(f.wallets) != 1 || f.wallets[0].Balance != 34600 || f.wallets[0].UserID != 8 {
		t.Fatalf("必须给用户加 34600 分余额：%+v", f.wallets)
	}

	// ---- 立即终止订阅 ----
	if f.terminated != 8 {
		t.Fatal("必须立即终止订阅（不做「退一部分钱继续用」）")
	}

	// ---- 状态推进 ----
	if len(f.casCalls) != 1 || f.casCalls[0].ToStatus != dbgen.OrderStatusRefunded {
		t.Fatalf("退满可退额度应当推进到 refunded：%+v", f.casCalls)
	}

	// ---- 审计：明细必须在里面（契约的成功响应放不下它）----
	after, ok := entry.After.(adminRefundAfter)
	if !ok {
		t.Fatalf("审计 after 类型不对：%T", entry.After)
	}
	if after.Basis.VWindow != 35800 || after.Basis.ConsumedTime != 1000 ||
		after.Basis.ConsumedData != 200 || after.Basis.RefundB != 34600 {
		t.Fatalf("退款扣减明细必须完整进审计：%+v", after.Basis)
	}
	if after.ClassAGates == nil || len(after.ClassAGates) != 5 {
		t.Fatalf("Class A 的五道闸门结论必须逐条进审计：%+v", after.ClassAGates)
	}
	// 🔴 加油包配额必须原封不动（§5.5）。
	if after.UserTransferEnablePack != 20<<30 {
		t.Fatalf("退款不得没收加油包配额：%d", after.UserTransferEnablePack)
	}
	if after.UserTransferEnablePlan != 0 {
		t.Fatalf("套餐配额必须清零：%d", after.UserTransferEnablePlan)
	}
	if entry.Action != "D7.order.refund" || entry.TargetID != "20260816T7K2M9Q4" || entry.Reason == "" {
		t.Fatalf("审计条目不完整：%+v", entry)
	}
	before, ok := entry.Before.(adminRefundBefore)
	if !ok || before.OrderStatus != string(dbgen.OrderStatusCompleted) {
		t.Fatalf("审计必须记下退款前的订单状态与用户订阅现状：%+v", entry.Before)
	}
	if string(view.Order.Status) != "refunded" {
		t.Fatalf("响应体状态不对：%s", view.Order.Status)
	}
}

func TestAdminRefundOrderGuards(t *testing.T) {
	t.Run("金额超过可退上限 → 422 且带扣减明细", func(t *testing.T) {
		f := adminTestRefundFake()
		in := adminTestRefundInput()
		over := int64(35801)
		in.Amount = &over
		e := asAdminOpError(mustErr(adminRefundOrder(context.Background(), f, testLogger(), in)))
		if e.Status != 422 {
			t.Fatalf("超额必须 422：%+v", e)
		}
		if len(e.Details) < 5 {
			t.Fatalf("必须把扣减明细算给操作者看：%+v", e.Details)
		}
		if len(f.refunds) != 0 || len(f.wallets) != 0 {
			t.Fatal("金额没过闸时不许发生任何写入")
		}
	})

	t.Run("金额为 0 或负数 → 422", func(t *testing.T) {
		for _, v := range []int64{0, -1} {
			f := adminTestRefundFake()
			in := adminTestRefundInput()
			amt := v
			in.Amount = &amt
			if e := asAdminOpError(mustErr(adminRefundOrder(context.Background(), f, testLogger(), in))); e.Status != 422 {
				t.Fatalf("金额 %d 必须 422", v)
			}
		}
	})

	t.Run("部分退款 → partially_refunded", func(t *testing.T) {
		f := adminTestRefundFake()
		in := adminTestRefundInput()
		part := int64(1000)
		in.Amount = &part
		if _, _, err := adminRefundOrder(context.Background(), f, testLogger(), in); err != nil {
			t.Fatalf("部分退款不该报错：%v", err)
		}
		if f.casCalls[0].ToStatus != dbgen.OrderStatusPartiallyRefunded {
			t.Fatalf("没退满应当是 partially_refunded：%s", f.casCalls[0].ToStatus)
		}
		// 订阅仍然立即终止 —— ADR 0013 §3.5 第 3 步是无条件的。
		if f.terminated != 8 {
			t.Fatal("部分退款同样要立即终止订阅（不做「退一部分钱继续用」）")
		}
	})

	t.Run("冷静期唯一索引冲突 → 409 并说清原因", func(t *testing.T) {
		// 🔴 读与插之间有窗口，「用户连点两次申请退款」是真实场景。
		//    真正的闸门是数据库，不是 cooling_off_used 那个数。
		f := adminTestRefundFake()
		f.refundRow.SettledOrderCount = 1 // 首单 → Class A
		f.createRefundErr = &pgconn.PgError{Code: "23505", ConstraintName: "refunds_cooling_off_once"}
		e := asAdminOpError(mustErr(adminRefundOrder(context.Background(), f, testLogger(), adminTestRefundInput())))
		if e.Status != 409 {
			t.Fatalf("唯一索引冲突必须映射成 409（不是 500）：%+v", e)
		}
		if !strings.Contains(e.Message, "一次") {
			t.Fatalf("409 必须说清楚原因：%s", e.Message)
		}
		if len(f.wallets) != 0 {
			t.Fatal("冲突时一分钱都不该动 —— 这就是把 CreateRefund 放在第一步的理由")
		}
	})

	t.Run("用户已注销 → 409", func(t *testing.T) {
		f := adminTestRefundFake()
		f.userErr = pgx.ErrNoRows
		if e := asAdminOpError(mustErr(adminRefundOrder(context.Background(), f, testLogger(), adminTestRefundInput()))); e.Status != 409 {
			t.Fatalf("已注销用户必须 409（余额不可提现，退无可退）：%+v", e)
		}
	})

	t.Run("订单不存在 → 404", func(t *testing.T) {
		f := adminTestRefundFake()
		f.refundErr = pgx.ErrNoRows
		if e := asAdminOpError(mustErr(adminRefundOrder(context.Background(), f, testLogger(), adminTestRefundInput()))); e.Status != 404 {
			t.Fatal("订单不存在必须 404")
		}
	})

	t.Run("终止订阅动了加油包 → 拒绝并回滚", func(t *testing.T) {
		// 「退款之后用户的加油包不见了」是一个字的差别造成的、
		// 用户不知道去投诉什么的故障。这条断言是它的唯一护栏。
		f := adminTestRefundFake()
		zero := int64(0)
		f.terminatePack = &zero
		if err := mustErr(adminRefundOrder(context.Background(), f, testLogger(), adminTestRefundInput())); err == nil {
			t.Fatal("加油包配额被改动必须让整笔退款失败")
		}
	})
}

func TestAdminRefundOrderCommissionClawback(t *testing.T) {
	t.Run("pending/confirmed 作废；transferred 扣余额并重分类差额", func(t *testing.T) {
		f := adminTestRefundFake()
		f.commissions = []dbgen.AdminListOrderCommissionsForClawbackRow{
			{ID: 1, InviterID: 11, Status: "pending", Amount: 3580, ClawbackAmount: 3580, OrderAmountPaid: 35800},
			{ID: 3, InviterID: 13, Status: "transferred", Amount: 3580, ClawbackAmount: 3580, InviterBalance: 1000, OrderAmountPaid: 35800},
		}
		if _, _, err := adminRefundOrder(context.Background(), f, testLogger(), adminTestRefundInput()); err != nil {
			t.Fatalf("不该报错：%v", err)
		}
		if len(f.voided) != 1 || f.voided[0] != 1 {
			t.Fatalf("pending 必须作废：%+v", f.voided)
		}
		// 邀请人余额只有 1000，追回 1000、差额 2580。
		var deducted bool
		for _, w := range f.wallets {
			if w.UserID == 13 && w.Balance == -1000 {
				deducted = true
			}
		}
		if !deducted {
			t.Fatalf("必须从邀请人余额里扣回 1000（余额上限）：%+v", f.wallets)
		}
		// 追不回来的部分改记 expense:refund（0018 的理由：把它继续挂在
		// expense:commission 下面，那个数字就不再是获客成本了）。
		if amt, ok := f.legAmount(acctRefundExpense); !ok || amt != 2580 {
			t.Fatalf("差额 2580 必须重分类到 expense:refund：%d %v", amt, ok)
		}
		if amt, ok := f.legAmount(acctCommissionExpense); !ok || amt >= 0 {
			t.Fatalf("expense:commission 必须被贷记（冲回）：%d %v", amt, ok)
		}
	})

	t.Run("邀请人余额被并发花掉（CHECK 冲突）→ 409 而不是 500", func(t *testing.T) {
		f := adminTestRefundFake()
		f.commissions = []dbgen.AdminListOrderCommissionsForClawbackRow{
			{ID: 3, InviterID: 13, Status: "transferred", Amount: 3580, ClawbackAmount: 3580, InviterBalance: 5000, OrderAmountPaid: 35800},
		}
		// 只让**邀请人**那次扣减撞 CHECK：用户那次是加钱，撞不上 balance >= 0。
		f.walletErr, f.walletErrUser = &pgconn.PgError{Code: "23514", ConstraintName: "wallet_balances_balance_check"}, 13
		e := asAdminOpError(mustErr(adminRefundOrder(context.Background(), f, testLogger(), adminTestRefundInput())))
		if e.Status != 409 {
			// 500 的现象是「退款偶尔报服务器错误」，没有人会想到去看余额。
			t.Fatalf("CHECK 冲突必须变成可重试的 409：%+v", e)
		}
	})

	t.Run("佣金状态被并发改走 → 整笔退款回滚", func(t *testing.T) {
		f := adminTestRefundFake()
		f.commissions = []dbgen.AdminListOrderCommissionsForClawbackRow{
			{ID: 1, InviterID: 11, Status: "pending", Amount: 3580, ClawbackAmount: 3580, OrderAmountPaid: 35800},
		}
		f.voidNo = true
		if e := asAdminOpError(mustErr(adminRefundOrder(context.Background(), f, testLogger(), adminTestRefundInput()))); e.Status != 409 {
			t.Fatal("少追一笔佣金就是给套利留的口子，必须回滚")
		}
	})
}

// ============================================================
// D13 事务体
// ============================================================

func TestAdminUpdatePayment(t *testing.T) {
	tradeNo := "20260816T7K2M9Q4"
	txid := adminTestTxID
	base := func() *adminOpsFake {
		return &adminOpsFake{
			paymentRow: dbgen.AdminGetPaymentForUpdateRow{
				ID: 501, Provider: "chain_tron", ExternalID: txid + ":0", EnteredBy: "scanner",
				State: dbgen.PaymentStateUnderpaid, TradeNo: &tradeNo, Txid: &txid,
				ReceivedAt: pgtype.Timestamptz{Time: time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC), Valid: true},
			},
		}
	}

	t.Run("正常路径：before_state 必须进审计", func(t *testing.T) {
		f := base()
		note := "已与用户核对，链上补足到账"
		view, entry, err := adminUpdatePayment(context.Background(), f, testLogger(), adminPaymentPatchInput{
			PaymentID: 501, State: dbgen.PaymentStatePaid, Reason: "补足到账已核对无误", Note: &note, AdminID: 7,
		})
		if err != nil {
			t.Fatalf("不该报错：%v", err)
		}
		if len(f.patched) != 1 || f.patched[0].State != dbgen.PaymentStatePaid {
			t.Fatalf("必须写一次状态：%+v", f.patched)
		}
		// 🔴 payments 没有 updated_at（0014 刻意），这次人工改动在行本身里
		//    不留任何痕迹 —— audit_logs 是唯一的记录，before_state 必须在里面。
		before, ok := entry.Before.(adminPaymentSnapshot)
		if !ok || before.State != string(dbgen.PaymentStateUnderpaid) {
			t.Fatalf("审计的 before 必须记下改前的 state：%+v", entry.Before)
		}
		after, ok := entry.After.(adminPaymentSnapshot)
		if !ok || after.State != string(dbgen.PaymentStatePaid) {
			t.Fatalf("审计的 after 不对：%+v", entry.After)
		}
		// note 在库里无处可存，只能落在审计里 —— **绝不塞进 raw**（那是取证材料）。
		if after.Note == nil || *after.Note != note {
			t.Fatalf("note 必须进审计快照：%+v", after.Note)
		}
		if entry.Action != "D13.payment.update" || entry.TargetType != "payment" ||
			entry.TargetID != "501" || entry.Reason == "" {
			t.Fatalf("审计条目不完整：%+v", entry)
		}
		if view.Id != 501 || view.TradeNo == nil || *view.TradeNo != tradeNo {
			t.Fatalf("响应体必须带上关联单号（它来自改前那次读）：%+v", view)
		}
	})

	t.Run("流水不存在 → 404", func(t *testing.T) {
		f := base()
		f.paymentErr = pgx.ErrNoRows
		_, _, err := adminUpdatePayment(context.Background(), f, testLogger(), adminPaymentPatchInput{
			PaymentID: 501, State: dbgen.PaymentStatePaid, Reason: "补足到账已核对无误",
		})
		if e := asAdminOpError(err); e.Status != 404 {
			t.Fatalf("必须 404：%+v", e)
		}
	})
}

func TestValidAdminPaymentStateIsAnExplicitTable(t *testing.T) {
	for _, v := range []gen.PaymentState{
		gen.PaymentStateWaiting, gen.PaymentStateConfirming, gen.PaymentStateUnderpaid,
		gen.PaymentStatePaid, gen.PaymentStateExpired,
	} {
		if _, ok := validAdminPaymentState(v); !ok {
			t.Fatalf("契约的 %s 在库里有对应值，必须接受", v)
		}
	}
	// 直接 dbgen.PaymentState(v) 会把一个未知值原样送进 SQL，得到 22P02 的 500，
	// 而不是一句「这个状态不认识」。
	if _, ok := validAdminPaymentState(gen.PaymentState("processing")); ok {
		t.Fatal("库里没有的状态必须被拒")
	}
}

// ============================================================
// 🔴 审计写失败 → 业务回滚（api-contract §6.3 第 1 条）
// ============================================================

// adminAuditFakeTx 内嵌 pgx.Tx 接口（值为 nil）：只实现本用例用得到的三个方法，
// 其余方法一旦被调用会 panic —— 那正是我们想要的信号。
type adminAuditFakeTx struct {
	pgx.Tx

	execs     []string
	failAudit error
	commits   int
	rollbacks int
	closed    bool
}

func (f *adminAuditFakeTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	if f.failAudit != nil && strings.Contains(sql, "INSERT INTO audit_logs") {
		return pgconn.CommandTag{}, f.failAudit
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (f *adminAuditFakeTx) Commit(context.Context) error {
	if f.closed {
		return pgx.ErrTxClosed
	}
	f.commits++
	f.closed = true
	return nil
}

func (f *adminAuditFakeTx) Rollback(context.Context) error {
	f.rollbacks++
	if f.closed {
		return pgx.ErrTxClosed
	}
	f.closed = true
	return nil
}

type adminAuditFakeBeginner struct{ tx *adminAuditFakeTx }

func (b *adminAuditFakeBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return b.tx, nil
}

func adminTestActor() audit.Actor {
	return audit.Actor{AdminID: 7, Email: "ops@babel.plus", IP: netip.MustParseAddr("203.0.113.9")}
}

// 🔴 **这一条是本组三个写端点全部走 audit.InTx 的理由。**
//
// 业务已经写完、审计写失败时，Commit 必须一次都不发生 —— 否则
// 「业务成功、审计缺失」就是一个静默的可能，而一条查不到的 D6
// 与「它没发生过」在事后不可区分。
func TestAuditWriteFailureRollsBackAdminBusinessWrite(t *testing.T) {
	tx := &adminAuditFakeTx{failAudit: errors.New("audit_logs 写不进去")}
	err := audit.InTx(context.Background(), &adminAuditFakeBeginner{tx: tx}, adminTestActor(),
		func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
			// 一次落在同一条事务上的业务写入。
			if err := q.UpdatePayAddressCursor(ctx, dbgen.UpdatePayAddressCursorParams{
				CursorTs: 1, PayAddressID: 2,
			}); err != nil {
				return audit.Entry{}, err
			}
			return audit.Entry{
				Action: "D6.order.mark_paid", TargetType: "order",
				TargetID: "20260816T7K2M9Q4", Reason: "链上到账已核对无误，网关回调丢失",
			}, nil
		})
	if err == nil {
		t.Fatal("审计写失败时整个操作必须失败")
	}
	if tx.commits != 0 {
		t.Fatalf("审计写失败之后绝不允许提交：commits=%d", tx.commits)
	}
	if tx.rollbacks == 0 {
		t.Fatal("必须回滚")
	}
	// 业务写入确实发生过（落在同一条事务上），所以回滚才有意义。
	var sawBusiness bool
	for _, s := range tx.execs {
		if strings.Contains(s, "pay_addresses") {
			sawBusiness = true
		}
	}
	if !sawBusiness {
		t.Fatal("这条用例必须真的先做一次业务写入，否则它证明不了「回滚」")
	}
}

// 「忘了写审计」的现象必须是**业务操作失败**，不是「操作成功但没留痕」。
// 这也间接钉住了本文件三个事务体返回的 Entry 必须字段完整。
func TestIncompleteAuditEntryRollsBackAdminBusinessWrite(t *testing.T) {
	tx := &adminAuditFakeTx{}
	err := audit.InTx(context.Background(), &adminAuditFakeBeginner{tx: tx}, adminTestActor(),
		func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
			if err := q.UpdatePayAddressCursor(ctx, dbgen.UpdatePayAddressCursorParams{
				CursorTs: 1, PayAddressID: 2,
			}); err != nil {
				return audit.Entry{}, err
			}
			// 缺 Action / TargetType / TargetID。
			return audit.Entry{Reason: "看起来写了原因，但这条记录指认不到任何东西"}, nil
		})
	if err == nil || tx.commits != 0 {
		t.Fatalf("审计条目不完整时必须回滚：err=%v commits=%d", err, tx.commits)
	}
}
