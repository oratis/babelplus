package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/config"
	"github.com/oratis/babelplus/api/internal/gen"
)

// 钱包 / 邀请码 / 佣金的单测。
//
// 三条被点名必须各有用例的边界都在这里：
//   - 余额分开返（TestWalletViewNeverMergesWithdrawable / TestWalletAnomalies*）
//   - 科目缺失返 503（TestCommissionTransfer503Shape / TestIsNullScanError）
//   - 踢设备用同一表达式在 usersub_test.go（那是 SQL 层的不变量）

// ============================================================
// 余额：可提现与不可提现绝不能合成一个数
// ============================================================

// 🔴 ADR 0013 ① 裁决「退款一律退到**不可提现**的余额」。
// 把两个数加成一个再发出去，正是那条裁决要防的误解本身 ——
// 响应里只剩一个数字之后，「哪一部分能提」这个问题就再也没有地方可以问了。
func TestWalletViewNeverMergesWithdrawable(t *testing.T) {
	row := dbgen.GetWalletOverviewRow{
		BalanceLedger:             3800,
		NonWithdrawableAmount:     3800,
		WithdrawableAmount:        0,
		FromRefund:                1000,
		FromCommission:            800,
		FromOrder:                 2000,
		CommissionPendingAmount:   720,
		CommissionAvailableAmount: 1590,
		BalanceCached:             3800,
	}
	w := walletView(row)
	if w.BalanceAmount != 3800 {
		t.Fatalf("balance_amount = %d, want 3800（= 不可提现余额）", w.BalanceAmount)
	}

	// 关键断言：可提现余额变成非 0 时，balance_amount **不能**跟着变大。
	// 变大就说明有人把两个池子加到了一起。
	row.WithdrawableAmount = 500
	if got := walletView(row).BalanceAmount; got != 3800 {
		t.Fatalf("可提现余额从 0 变成 500 之后 balance_amount 变成了 %d —— "+
			"两个池子被合成了一个数字，而那正是 ADR 0013 ① 要防的误解", got)
	}

	if w.CommissionPendingAmount != 720 || w.CommissionAvailableAmount != 1590 {
		t.Errorf("佣金两段式没有原样下发：%+v", w)
	}
}

// 「可提现余额是字面量 0」这条规则的守卫只有 code review 与接口形状
// （data-model §7.1：「『余额不可提现』在数据库层面无法强制」）。
// 所以它一旦不再是 0，必须变成一条能被看见的告警。
func TestWalletAnomaliesFlagsNonZeroWithdrawable(t *testing.T) {
	got := walletAnomalies(dbgen.GetWalletOverviewRow{
		BalanceLedger: 100, NonWithdrawableAmount: 100, BalanceCached: 100,
		WithdrawableAmount: 1,
	})
	if !containsSubstr(got, "withdrawable_amount 不为 0") {
		t.Fatalf("可提现余额非 0 时没有告警：%v", got)
	}
}

func TestWalletAnomaliesFlagsCacheDrift(t *testing.T) {
	// 缓存漂移必须**当场**说出来。每日 ReconcileWalletBalances 也会发现它，
	// 但那是明天，而用户现在正看着这个数字。
	got := walletAnomalies(dbgen.GetWalletOverviewRow{
		BalanceLedger: 100, NonWithdrawableAmount: 100, BalanceCached: 90,
	})
	if !containsSubstr(got, "缓存与分录聚合不一致") {
		t.Fatalf("缓存漂移没有告警：%v", got)
	}
}

func TestWalletAnomaliesFlagsNegativeLedger(t *testing.T) {
	// wallet_balances 有 CHECK (balance >= 0)，但**分录聚合没有**。
	got := walletAnomalies(dbgen.GetWalletOverviewRow{
		BalanceLedger: -5, NonWithdrawableAmount: -5, BalanceCached: -5,
	})
	if !containsSubstr(got, "余额为负") {
		t.Fatalf("负余额没有告警：%v", got)
	}
}

func TestWalletAnomaliesQuietWhenHealthy(t *testing.T) {
	got := walletAnomalies(dbgen.GetWalletOverviewRow{
		BalanceLedger: 1234, NonWithdrawableAmount: 1234, BalanceCached: 1234,
	})
	if len(got) != 0 {
		t.Fatalf("健康数据也在告警，会把真告警淹掉：%v", got)
	}
}

func containsSubstr(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// ============================================================
// 流水类型映射
// ============================================================

func TestWalletTxType(t *testing.T) {
	cases := []struct {
		name    string
		refType *string
		delta   int64
		want    gen.WalletTransactionType
		exact   bool
	}{
		// order 一个 ref_type 要按符号劈成两个契约值。
		{"充值进余额", ptrOf("order"), 5000, gen.Recharge, true},
		{"余额抵扣订单", ptrOf("order"), -5000, gen.Consume, true},
		{"退款", ptrOf("refund"), 3000, gen.Refund, true},
		{"佣金划转", ptrOf("commission"), 1590, gen.CommissionTransfer, true},
		{"对账调整", ptrOf("reconcile_adjust"), -1, gen.AdminAdjust, true},
		// 认不出来的按符号兜底，且必须被标成「不确定」让调用方打日志。
		{"未知 ref_type", ptrOf("mystery"), 100, gen.Recharge, false},
		{"NULL ref_type 出账", nil, -100, gen.Consume, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, exact := walletTxType(c.refType, c.delta)
			if got != c.want || exact != c.exact {
				t.Errorf("walletTxType = (%q,%v), want (%q,%v)", got, exact, c.want, c.exact)
			}
		})
	}
}

// 兜底**不能**映射成 admin_adjust：把一笔来源不明的钱说成「管理员调整」是编造事实。
// 按符号给方向至少每个字都是真的。
func TestWalletTxTypeFallbackNeverClaimsAdminAdjust(t *testing.T) {
	for _, d := range []int64{-100, 0, 100} {
		if got, _ := walletTxType(ptrOf("brand_new_ref"), d); got == gen.AdminAdjust {
			t.Fatalf("delta=%d 的未知 ref_type 被说成了 admin_adjust", d)
		}
	}
}

func TestNoteOfDropsBlank(t *testing.T) {
	if noteOf("") != nil || noteOf("   ") != nil {
		t.Fatal("空摘要应当不下发，而不是发一个空串")
	}
	if n := noteOf("佣金划转"); n == nil || *n != "佣金划转" {
		t.Fatalf("note = %v", n)
	}
}

// ============================================================
// 邀请码
// ============================================================

func TestRandomInviteCodeAlphabet(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := randomInviteCode()
		if err != nil {
			t.Fatalf("randomInviteCode: %v", err)
		}
		if len(c) != inviteCodeLen {
			t.Fatalf("长度 = %d, want %d", len(c), inviteCodeLen)
		}
		for _, ch := range c {
			if !strings.ContainsRune(inviteCodeAlphabet, ch) {
				t.Fatalf("码 %q 含字符集外的字符 %q", c, ch)
			}
		}
		// 易混字符必须一个都不出现：这个码会被人手抄、会被念出来。
		if strings.ContainsAny(c, "01OIL") {
			t.Fatalf("码 %q 含易混字符", c)
		}
		// 归一化之后必须等于自身（auth.go 的 normalizeInviteCode 会转大写去空白），
		// 否则「生成的码」与「查询用的码」长得不一样。
		if normalizeInviteCode(c) != c {
			t.Fatalf("码 %q 归一化后变成 %q", c, normalizeInviteCode(c))
		}
		seen[c] = true
	}
	if len(seen) < 190 {
		t.Fatalf("200 次生成只得到 %d 个不同的码，随机性可疑", len(seen))
	}
}

func TestInviteCodeView(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	base := "https://web.example"

	cases := []struct {
		name     string
		row      dbgen.ListUserInviteCodesRow
		want     gen.InviteCodeStatus
		wantLink bool
	}{
		{
			name:     "可用",
			row:      dbgen.ListUserInviteCodesRow{ID: 1, Code: "ABCD2345", MaxUses: 1, UsedCount: 0},
			want:     gen.InviteCodeStatusOk,
			wantLink: true,
		},
		{
			name:     "已用尽",
			row:      dbgen.ListUserInviteCodesRow{ID: 2, Code: "ABCD2346", MaxUses: 1, UsedCount: 1},
			want:     gen.InviteCodeStatusExhausted,
			wantLink: false,
		},
		{
			// 🔴 已吊销的码必须**出现在列表里**且状态是 disabled。
			// 过滤掉它们，用户会以为码「消失了」→ 再生成一个 → 撞上名额闸门
			// 得到一个他完全无法理解的 403。
			name:     "已吊销",
			row:      dbgen.ListUserInviteCodesRow{ID: 3, Code: "ABCD2347", MaxUses: 1, RevokedAt: ts(now.Add(-time.Hour))},
			want:     gen.InviteCodeStatusDisabled,
			wantLink: false,
		},
		{
			// ⚠️ 契约的三值枚举装不下「已过期」，只能并进 disabled。
			// 页面要用文案区分：吊销是他自己撤的，过期是他忘了用，动作不同。
			name:     "已过期",
			row:      dbgen.ListUserInviteCodesRow{ID: 4, Code: "ABCD2348", MaxUses: 1, ExpiresAt: ts(now.Add(-time.Second))},
			want:     gen.InviteCodeStatusDisabled,
			wantLink: false,
		},
		{
			name:     "吊销且用尽 → 以吊销为准",
			row:      dbgen.ListUserInviteCodesRow{ID: 5, Code: "ABCD2349", MaxUses: 1, UsedCount: 1, RevokedAt: ts(now.Add(-time.Hour))},
			want:     gen.InviteCodeStatusDisabled,
			wantLink: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := inviteCodeView(c.row, base, now)
			if v.Status != c.want {
				t.Errorf("status = %q, want %q", v.Status, c.want)
			}
			if c.wantLink && (v.InviteUrl == nil || !strings.Contains(*v.InviteUrl, c.row.Code)) {
				t.Errorf("可用的码没有可点的邀请链接：%v", v.InviteUrl)
			}
			// 不可用的码不给链接：用户会把它发出去，然后被邀请的人拿到「邀请码无效」。
			if !c.wantLink && v.InviteUrl != nil {
				t.Errorf("不可用的码却给了链接 %q", *v.InviteUrl)
			}
		})
	}
}

func TestInviteBaseURLFallsBackToEmpty(t *testing.T) {
	srv := &Server{cfg: &config.Config{Env: "test"}, logger: testLogger()}
	if got := srv.inviteBaseURL(context.Background()); got != "" {
		t.Fatalf("没有配 Origin 时应返回空串（相对链接复制到聊天软件里是死链），got %q", got)
	}
	srv.cfg.AllowedOrigins = []string{"https://web.example/"}
	if got := srv.inviteBaseURL(context.Background()); got != "https://web.example" {
		t.Fatalf("尾斜杠没去掉：%q", got)
	}
}

// ============================================================
// 佣金状态映射
// ============================================================

func TestCommissionStatusMapping(t *testing.T) {
	cases := map[string]gen.CommissionStatus{
		"pending":     gen.CommissionStatusPending,
		"confirmed":   gen.CommissionStatusConfirmed,
		"transferred": gen.CommissionStatusSettled,
		// ⚠️ voided 在契约里没有对应值。映射成 settled 是谎话（钱永远不会到），
		// 所以只能落到 pending，并在交付说明里登记这处冲突。
		"voided": gen.CommissionStatusPending,
	}
	for in, want := range cases {
		if got := commissionStatus(in); got != want {
			t.Errorf("commissionStatus(%q) = %q, want %q", in, got, want)
		}
	}
	// 🔴 关键：voided 绝不能被说成「已结算」。
	if commissionStatus("voided") == gen.CommissionStatusSettled {
		t.Fatal("作废的佣金被渲染成已结算 —— 那是在告诉用户一笔永远不会到的钱已经到了")
	}
}

func TestCommissionViewKeepsTradeNo(t *testing.T) {
	v := commissionView(dbgen.ListUserCommissionsRow{
		ID: 1, OrderTradeNo: "BP20260830ABC", Amount: 1590, Status: "confirmed",
		ConfirmedAt: ts(time.Now()), CreatedAt: ts(time.Now()),
	})
	if v.OrderTradeNo == nil || *v.OrderTradeNo != "BP20260830ABC" {
		t.Fatalf("order_trade_no = %v", v.OrderTradeNo)
	}
	if v.Amount != 1590 {
		t.Errorf("amount = %d（一次性定额 C6：¥15.90 档）", v.Amount)
	}
}

// ============================================================
// 划转：只支持整条，且必须是前缀和
// ============================================================

func lockedRow(id, amount int64) dbgen.LockTransferableCommissionsRow {
	return dbgen.LockTransferableCommissionsRow{ID: id, Amount: amount, ConfirmedAt: ts(time.Now())}
}

func TestPickCommissionsForAmount(t *testing.T) {
	rows := []dbgen.LockTransferableCommissionsRow{
		lockedRow(11, 720),
		lockedRow(12, 1590),
		lockedRow(13, 3580),
	}

	t.Run("全部划转", func(t *testing.T) {
		ids, sum, _ := pickCommissionsForAmount(rows, 720+1590+3580)
		if len(ids) != 3 || sum != 5890 {
			t.Fatalf("ids = %v, sum = %d", ids, sum)
		}
	})
	t.Run("前一条", func(t *testing.T) {
		ids, sum, _ := pickCommissionsForAmount(rows, 720)
		if len(ids) != 1 || ids[0] != 11 || sum != 720 {
			t.Fatalf("ids = %v, sum = %d", ids, sum)
		}
	})
	t.Run("前两条", func(t *testing.T) {
		ids, _, _ := pickCommissionsForAmount(rows, 2310)
		if len(ids) != 2 || ids[0] != 11 || ids[1] != 12 {
			t.Fatalf("ids = %v", ids)
		}
	})

	// 🔴 **不发明部分划转语义。** commissions 没有 amount_transferred 列，
	// 一条佣金要么整条 transferred 要么不动。金额对不上就 422，
	// 绝不「尽量划一点」。
	t.Run("部分金额被拒", func(t *testing.T) {
		ids, sum, prefixes := pickCommissionsForAmount(rows, 300)
		if ids != nil {
			t.Fatalf("¥3 的部分划转被接受了：%v", ids)
		}
		if sum != 5890 {
			t.Errorf("失败时第二个返回值应是可划转合计 %d，got %d", 5890, sum)
		}
		if len(prefixes) != 3 || prefixes[0] != 720 || prefixes[2] != 5890 {
			t.Errorf("前缀和 = %v", prefixes)
		}
	})

	// 非前缀的子集（跳过第一条取第二条）同样被拒 —— 请求体里只有一个金额，
	// 服务端无从知道用户勾选了哪几条，任何「凑出这个金额的子集」都是发明。
	t.Run("非前缀子集被拒", func(t *testing.T) {
		if ids, _, _ := pickCommissionsForAmount(rows, 1590); ids != nil {
			t.Fatalf("跳过第一条的子集被接受了：%v", ids)
		}
	})

	t.Run("没有可划转的", func(t *testing.T) {
		ids, sum, prefixes := pickCommissionsForAmount(nil, 100)
		if ids != nil || sum != 0 || len(prefixes) != 0 {
			t.Fatalf("空输入的返回 = (%v,%d,%v)", ids, sum, prefixes)
		}
	})

	// 金额为 0 的佣金会让相邻前缀和相同：取较短的那个，
	// 多划一条 0 元佣金没有意义却会把它标成 transferred。
	t.Run("零元佣金取较短前缀", func(t *testing.T) {
		z := []dbgen.LockTransferableCommissionsRow{lockedRow(1, 720), lockedRow(2, 0)}
		ids, _, _ := pickCommissionsForAmount(z, 720)
		if len(ids) != 1 {
			t.Fatalf("ids = %v, 期望只取第一条", ids)
		}
	})
}

func TestFormatPrefixSums(t *testing.T) {
	if got := formatPrefixSums(nil); !strings.Contains(got, "没有可划转") {
		t.Errorf("空列表文案 = %q", got)
	}
	if got := formatPrefixSums([]int64{720, 2310}); got != "720 / 2310" {
		t.Errorf("= %q", got)
	}
	// 邀请了几十个人的用户会有几十个前缀和，错误信息本身不能变得读不了。
	many := make([]int64, 40)
	for i := range many {
		many[i] = int64(i+1) * 100
	}
	got := formatPrefixSums(many)
	if !strings.Contains(got, "…") || !strings.Contains(got, "共 40 个") {
		t.Errorf("长列表没有被截断：%q", got)
	}
}

// ============================================================
// 科目缺失 → 503，不是 500
// ============================================================

// 🔴 「科目缺失」在当前生成代码下的形态是**扫描失败**，不是 0 行也不是 nil 指针：
// GetCommissionTransferAccounts 的 `max(id) FILTER (...)::bigint` 因为那个显式 cast
// 被 sqlc 判成 NOT NULL，于是 pgx 报 `cannot scan NULL into *int64`。
// 认不出这条错误的后果是把一个「永远不会成功」的请求报成 500，
// 而用户会对着一个自己看得见余额的按钮反复重试。
func TestIsNullScanError(t *testing.T) {
	// pgx v5 的原文（pgtype/int.go 等处）：`cannot scan NULL into %T`，
	// 外面还会包一层 `can't scan into dest[0]: `。
	pgxLike := fmt.Errorf("can't scan into dest[0]: %w", errors.New("cannot scan NULL into *int64"))
	if !isNullScanError(pgxLike) {
		t.Fatal("没认出 pgx 的 NULL 扫描错误 —— 科目缺失会退化成 500")
	}
	if isNullScanError(nil) {
		t.Error("nil 被判成了扫描错误")
	}
	if isNullScanError(errors.New("connection refused")) {
		t.Error("连接错误被误判成科目缺失")
	}
}

// 503 的形状必须完整：Retry-After（不然前端不知道等多久）+ X-Request-Id（报障要贴）
// + 统一信封的 INTERNAL_DEPENDENCY_DOWN。
func TestCommissionTransfer503Shape(t *testing.T) {
	srv := testServer()
	resp := transferCommission503JSONResponse{
		ErrDependencyDownJSONResponse: gen.ErrDependencyDownJSONResponse{
			Body: srv.envelope(context.Background(), gen.INTERNALDEPENDENCYDOWN, "佣金划转暂不可用，请稍后再试"),
			Headers: gen.ErrDependencyDownResponseHeaders{
				RetryAfter: commissionTransferRetryAfter,
				XRequestId: "req-123",
			},
		},
	}

	// 编译期确认它确实是 transferCommission 的合法响应。
	var _ gen.TransferCommissionResponseObject = resp

	rec := httptest.NewRecorder()
	if err := resp.VisitTransferCommissionResponse(rec); err != nil {
		t.Fatalf("Visit: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d, want 503（500 会让用户以为是偶发故障并反复重试）", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "300" {
		t.Errorf("Retry-After = %q, want 300", got)
	}
	if got := rec.Header().Get("X-Request-Id"); got != "req-123" {
		t.Errorf("X-Request-Id = %q", got)
	}
	var env gen.ErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应体不是统一信封：%v（%s）", err, rec.Body.String())
	}
	if env.Error.Code != gen.INTERNALDEPENDENCYDOWN {
		t.Errorf("code = %q, want INTERNAL_DEPENDENCY_DOWN", env.Error.Code)
	}
	// 退避时间必须明显长于「下一轮 Cloud Scheduler」量级：这里的依赖是
	// 一支还没跑的 migration，30 秒后重试必然还是失败。
	if commissionTransferRetryAfter < 60 {
		t.Errorf("Retry-After = %d 秒，太短：科目缺失要等一次 migration，不是等几十秒",
			commissionTransferRetryAfter)
	}
}

// ============================================================
// 分录编号
// ============================================================

func TestNewEntryNo(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		no := newEntryNo("CT")
		if !strings.HasPrefix(no, "CT") {
			t.Fatalf("entry_no = %q，缺前缀", no)
		}
		if strings.ContainsAny(no, " ") {
			t.Fatalf("entry_no %q 含空格 —— 它会被抄进对账单与工单", no)
		}
		// 只有**随机后缀**要求无易混字符：中间那段是 UTC 时刻，
		// 它必然含 0 与 1，而时刻本身是对账时第一眼要看的东西，不能为了字符集牺牲。
		suffix := no[strings.LastIndexByte(no, '-')+1:]
		if strings.ContainsAny(suffix, "01OIL") {
			t.Fatalf("entry_no %q 的随机后缀含易混字符", no)
		}
		seen[no] = true
	}
	if len(seen) < 95 {
		t.Fatalf("100 次生成只得到 %d 个不同的编号（entry_no 上有 UNIQUE）", len(seen))
	}
}

// 两条腿的币种必须同为 CNY —— 分录按 (entry_id, currency) 分组之后才平。
func TestLedgerCurrencyIsCNY(t *testing.T) {
	if ledgerCurrencyCNY != "CNY" {
		t.Fatalf("ledgerCurrencyCNY = %q", ledgerCurrencyCNY)
	}
	if len(ledgerCurrencyCNY) != 3 {
		t.Fatalf("ledger_accounts.currency 是 char(3)，%q 装不进去", ledgerCurrencyCNY)
	}
}
