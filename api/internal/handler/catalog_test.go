package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
)

// 套餐 / 公告 / 优惠码三个 operation 的测试。
//
// 与 task_test.go 同一条纪律：测的是**吃窄接口的自由函数与纯函数**，
// 不是 handler 方法 —— Server.db 是具体类型 *store.Store，塞不了假实现。
// handler 方法这一层只断言「它确实被实现了」（见 TestCatalogOrderOperationsAreImplemented），
// 因为 Server 内嵌 Unimplemented，**漏实现不会在编译期暴露**，只会悄悄退回 501。
//
// 每个 operation 三件事：正常路径、错误码分支、以及那条「不这么做会静默出错」的边界。

// ============================================================
// listPlans
// ============================================================

type fakePlanLister struct {
	rows []dbgen.ListPlansForUserRow
	err  error
	uid  int64
}

func (f *fakePlanLister) ListPlansForUser(_ context.Context, userID int64) ([]dbgen.ListPlansForUserRow, error) {
	f.uid = userID
	return f.rows, f.err
}

func i32p(v int32) *int32 { return &v }
func i64p(v int64) *int64 { return &v }

func TestListPlansForUser(t *testing.T) {
	t.Run("正常路径：字段逐条映射，NULL 价格的周期整条不出现", func(t *testing.T) {
		f := &fakePlanLister{rows: []dbgen.ListPlansForUserRow{{
			ID: 7, Code: "std", Name: "标准", Kind: planKindCycle, ContentMd: "**说明**",
			TransferEnable: 100 << 30, DeviceLimit: i32p(5), SpeedLimitMbps: nil,
			ResetTrafficMethod: dbgen.ResetMethodMonthlyOnOrderDay,
			PriceMonthly:       i64p(6000), PriceQuarterly: nil,
			PriceHalfYearly: nil, PriceYearly: i64p(61200), PriceOnetime: nil,
			SortOrder: 3, Sellable: true, Renewable: true,
		}}}
		out, err := listPlansForUser(context.Background(), f, 42)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if f.uid != 42 {
			t.Fatalf("user_id 没有透传：%d", f.uid)
		}
		if len(out) != 1 {
			t.Fatalf("应当返回 1 个套餐，实际 %d", len(out))
		}
		p := out[0]
		if p.Type != gen.PlanTypePeriod {
			t.Fatalf("kind='cycle' 应当映射成 period，实际 %q", p.Type)
		}
		if p.Description == nil || *p.Description != "**说明**" {
			t.Fatal("description 应当来自 content_md")
		}
		if p.TransferEnableBytes != 100<<30 {
			t.Fatalf("transfer_enable_bytes 应当是整数字节，实际 %d", p.TransferEnableBytes)
		}
		if p.Currency == nil || *p.Currency != gen.PlanCurrencyCNY {
			t.Fatal("currency 是常量 CNY（plans 表没有这一列）")
		}
		if len(p.Prices) != 2 {
			t.Fatalf("只有 monthly 与 yearly 非 NULL，应当出 2 条价格，实际 %d", len(p.Prices))
		}
		for _, pr := range p.Prices {
			if pr.Amount == 0 {
				// NULL = 该周期不售，不是「免费」。出现 amount=0 就说明我们把
				// 「不卖」渲染成了「白送」。
				t.Fatal("不售的周期必须整条不出现，而不是 amount=0")
			}
		}
	})

	// 🔴 静默边界：device_limit 在 DB 里可空（NULL = 不限设备），契约里却是非空 int32。
	// 随便填一个大数（999）会被前端当成真的上限显示出来，而 0 在契约里没有
	// 「零台设备」的合理解释，只能被读成「不限」。
	t.Run("静默边界：device_limit 为 NULL 时映射成 0（不限），不是编造一个上限", func(t *testing.T) {
		f := &fakePlanLister{rows: []dbgen.ListPlansForUserRow{{
			ID: 1, Kind: planKindPack, DeviceLimit: nil, PriceOnetime: i64p(1200),
			ResetTrafficMethod: dbgen.ResetMethodNever,
		}}}
		out, _ := listPlansForUser(context.Background(), f, 1)
		if out[0].DeviceLimit != 0 {
			t.Fatalf("NULL device_limit 应当映射成 0，实际 %d", out[0].DeviceLimit)
		}
		if out[0].Type != gen.PlanTypeTrafficPack {
			t.Fatalf("kind='pack' 应当映射成 traffic_pack，实际 %q", out[0].Type)
		}
	})

	t.Run("空结果返回空切片而不是 nil（nil 序列化成 null，前端会在 .map 上炸）", func(t *testing.T) {
		out, err := listPlansForUser(context.Background(), &fakePlanLister{}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if out == nil {
			t.Fatal("必须是空切片而不是 nil")
		}
	})

	t.Run("错误分支：查询失败原样上抛，由 handler 翻成 500", func(t *testing.T) {
		_, err := listPlansForUser(context.Background(), &fakePlanLister{err: errors.New("boom")}, 1)
		if err == nil {
			t.Fatal("查询失败必须上报")
		}
	})
}

func TestPlanTypeView(t *testing.T) {
	// 两套拼写没有一个字母重合，映射写反不会报错，只会让套餐页整块空白。
	if planTypeView("cycle") != gen.PlanTypePeriod {
		t.Fatal("'cycle' → period")
	}
	if planTypeView("pack") != gen.PlanTypeTrafficPack {
		t.Fatal("'pack' → traffic_pack")
	}
}

// ============================================================
// 游标
// ============================================================

func TestDecodeKeysetCursor(t *testing.T) {
	at := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	id := int64(99)
	pinned := true
	enc := encodeKeysetCursor(keysetCursor{ID: &id, At: &at, Pinned: &pinned})

	got, err := decodeKeysetCursor(enc)
	if err != nil {
		t.Fatalf("往返失败：%v", err)
	}
	if got.ID == nil || *got.ID != 99 || got.At == nil || !got.At.Equal(at) {
		t.Fatalf("往返值不对：%+v", got)
	}
	if got.Pinned == nil || !*got.Pinned {
		t.Fatal("pinned 分量必须往返保留")
	}

	for name, raw := range map[string]string{
		"不是 base64": "!!!not-base64!!!",
		"不是 JSON":   base64.RawURLEncoding.EncodeToString([]byte("nope")),
		"缺 id":      base64.RawURLEncoding.EncodeToString([]byte(`{"at":"2026-08-30T00:00:00Z"}`)),
		"缺 at":      base64.RawURLEncoding.EncodeToString([]byte(`{"id":1}`)),
		"多了未知字段（不是我们发的游标）": base64.RawURLEncoding.EncodeToString([]byte(`{"id":1,"at":"2026-08-30T00:00:00Z","evil":1}`)),
		"at 类型不对": base64.RawURLEncoding.EncodeToString([]byte(`{"id":1,"at":123}`)),
	} {
		if _, err := decodeKeysetCursor(raw); err == nil {
			t.Fatalf("%s：应当被拒绝", name)
		}
	}
}

func TestPageLimit(t *testing.T) {
	// 🔴 page_limit 必须是 limit+1：has_more 靠多取的那一行判定。
	// 用「返回行数 == limit」判的话，总数正好整除时会多给一页空数据。
	if want, pl := pageLimit(nil); want != defaultPageLimit || pl != defaultPageLimit+1 {
		t.Fatalf("默认应当是 %d / %d，实际 %d / %d", defaultPageLimit, defaultPageLimit+1, want, pl)
	}
	over := gen.LimitQuery(1000)
	if want, _ := pageLimit(&over); want != maxPageLimit {
		t.Fatalf("超上限应当收到 %d，实际 %d", maxPageLimit, want)
	}
	zero := gen.LimitQuery(0)
	if want, pl := pageLimit(&zero); want != 1 || pl != 2 {
		// limit=0 时 SQL 的 LIMIT 1 只会取回那行多余的探测行，正文为空。
		t.Fatalf("limit=0 应当收到 1，实际 %d / %d", want, pl)
	}
}

// ============================================================
// listNotices
// ============================================================

func noticeRow(id int64, pinned bool, created time.Time) dbgen.ListNoticesPageRow {
	return dbgen.ListNoticesPageRow{
		ID: id, Title: "t", ContentMd: "c", Level: "info", Pinned: pinned,
		PublishedAt: ts(created), CreatedAt: ts(created),
	}
}

func TestNoticePage(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	t.Run("正常路径：恰好一页时 has_more=false、next_cursor 为 nil", func(t *testing.T) {
		rows := []dbgen.ListNoticesPageRow{noticeRow(1, false, base), noticeRow(2, false, base)}
		data, next, more := noticePage(rows, 2)
		if len(data) != 2 || more || next != nil {
			t.Fatalf("不该有下一页：len=%d more=%v next=%v", len(data), more, next)
		}
	})

	t.Run("多取的那一行被丢掉，游标取第 limit 行", func(t *testing.T) {
		rows := []dbgen.ListNoticesPageRow{
			noticeRow(1, true, base.Add(-48*time.Hour)),
			noticeRow(2, false, base.Add(-time.Hour)),
			noticeRow(3, false, base.Add(-2*time.Hour)), // 多取的探测行
		}
		data, next, more := noticePage(rows, 2)
		if len(data) != 2 || !more || next == nil {
			t.Fatalf("应当有下一页：len=%d more=%v", len(data), more)
		}
		cur, err := decodeKeysetCursor(*next)
		if err != nil {
			t.Fatalf("游标解不开：%v", err)
		}
		if *cur.ID != 2 {
			t.Fatalf("游标应当取第 limit 行（id=2），实际 %d", *cur.ID)
		}
	})

	// 🔴 静默边界：排序键是 (pinned DESC, created_at DESC, id DESC)，
	// 而置顶公告的 created_at 通常比第一页的普通公告更**旧**。
	// 游标只带 (at, id) 时，pinned 的行会在第二页重新满足 created_at < cursor_at
	// —— 置顶公告在每一页都再出现一次，而且完全不报错。
	t.Run("静默边界：游标必须带 pinned 分量，否则置顶公告会每页重复一次", func(t *testing.T) {
		rows := []dbgen.ListNoticesPageRow{
			noticeRow(10, true, base.Add(-72*time.Hour)), // 置顶且更旧
			noticeRow(11, false, base),
			noticeRow(12, false, base.Add(-time.Hour)),
		}
		_, next, _ := noticePage(rows, 2)
		if next == nil {
			t.Fatal("应当有下一页")
		}
		cur, err := decodeKeysetCursor(*next)
		if err != nil {
			t.Fatalf("游标解不开：%v", err)
		}
		if cur.Pinned == nil {
			t.Fatal("公告游标缺少 pinned 分量 —— 置顶公告会在下一页重复出现")
		}
		if *cur.Pinned {
			t.Fatal("第 limit 行是普通公告，pinned 应当是 false")
		}
	})

	// 🔴 静默边界：游标编的必须是 created_at 而不是 published_at。
	// published_at 是 coalesce(starts_at, created_at) 算出来的展示值，
	// 而排序键与行比较用的是 created_at；两者对所有「定时发布」的公告都不相等，
	// 拿前者当游标会整批跳过或重复一批行。
	t.Run("静默边界：游标用 created_at，不用 coalesce 出来的 published_at", func(t *testing.T) {
		created := base.Add(-100 * time.Hour)
		published := base // starts_at 晚于 created_at 的定时发布公告
		rows := []dbgen.ListNoticesPageRow{
			{ID: 1, Pinned: false, CreatedAt: ts(created), PublishedAt: ts(published)},
			{ID: 2, Pinned: false, CreatedAt: ts(created.Add(-time.Hour)), PublishedAt: ts(published)},
		}
		_, next, _ := noticePage(rows, 1)
		if next == nil {
			t.Fatal("应当有下一页")
		}
		cur, _ := decodeKeysetCursor(*next)
		if !cur.At.Equal(created) {
			t.Fatalf("游标必须是 created_at(%v)，实际 %v", created, *cur.At)
		}
	})

	t.Run("published_at 与 content 的映射（表里没有 published_at 列，契约里它必填）", func(t *testing.T) {
		created := base.Add(-10 * time.Hour)
		published := base.Add(-time.Hour)
		data, _, _ := noticePage([]dbgen.ListNoticesPageRow{
			{ID: 5, Title: "标题", ContentMd: "正文", Pinned: true, CreatedAt: ts(created), PublishedAt: ts(published)},
		}, 5)
		if data[0].Content != "正文" {
			t.Fatal("content 应当来自 content_md")
		}
		if !data[0].PublishedAt.Equal(published) {
			t.Fatalf("published_at 应当来自 coalesce(starts_at, created_at)，实际 %v", data[0].PublishedAt)
		}
		if data[0].Pinned == nil || !*data[0].Pinned {
			t.Fatal("pinned 应当如实下发")
		}
	})
}

// ============================================================
// verifyCoupon
// ============================================================

func couponRow() dbgen.VerifyCouponForUserRow {
	return dbgen.VerifyCouponForUserRow{
		ID: 1, Code: "NEW10", Name: "新人券",
		Type: couponTypePercentage, Value: 1000, // 1000 bps = 10%
		UsesPerUser: 1,
	}
}

func TestEvaluateCoupon(t *testing.T) {
	t.Run("正常路径：百分比券按基点算，floor", func(t *testing.T) {
		e := evaluateCoupon(couponRow(), true, 6099)
		if !e.Valid {
			t.Fatalf("应当可用：%s", e.Reason)
		}
		if e.Discount != 609 { // floor(6099 * 1000 / 10000)
			t.Fatalf("折扣应当是 609（floor），实际 %d", e.Discount)
		}
	})

	t.Run("固定额券的 value 直接是分，且封顶到原价", func(t *testing.T) {
		row := couponRow()
		row.Type = couponTypeFixedAmount
		row.Value = 10000
		e := evaluateCoupon(row, true, 6000)
		if e.Discount != 6000 {
			// 不封顶的话 amount_gross − amount_discount 变负，
			// orders 的 CHECK(amount_due >= 0) 会把一条产品规则变成一次 500。
			t.Fatalf("折扣必须封顶到原价 6000，实际 %d", e.Discount)
		}
	})

	t.Run("错误分支：每一种不可用都有自己的中文原因", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*dbgen.VerifyCouponForUserRow)
			want   string
		}{
			{"未开始", func(r *dbgen.VerifyCouponForUserRow) { r.NotStarted = true }, "尚未开始"},
			{"已过期", func(r *dbgen.VerifyCouponForUserRow) { r.Ended = true }, "已过期"},
			{"已用尽", func(r *dbgen.VerifyCouponForUserRow) { r.Exhausted = true }, "已被领完"},
			{"非首单", func(r *dbgen.VerifyCouponForUserRow) {
				r.FirstOrderOnly = true
				r.UserSettledOrderCount = 1
			}, "首次下单"},
			{"本人已用过", func(r *dbgen.VerifyCouponForUserRow) { r.UserUsedCount = 1 }, "已使用过"},
			{"套餐不在范围", func(r *dbgen.VerifyCouponForUserRow) { r.PlanOutOfScope = true }, "所选套餐"},
			{"周期不在范围", func(r *dbgen.VerifyCouponForUserRow) { r.PeriodOutOfScope = true }, "付费周期"},
		}
		for _, c := range cases {
			row := couponRow()
			c.mutate(&row)
			e := evaluateCoupon(row, true, 6000)
			if e.Valid {
				t.Fatalf("%s：应当判为不可用", c.name)
			}
			if !strings.Contains(e.Reason, c.want) {
				t.Fatalf("%s：reason 应当含 %q，实际 %q", c.name, c.want, e.Reason)
			}
		}
	})

	t.Run("判定顺序：已过期的券先说过期，而不是先说「你不是新用户」", func(t *testing.T) {
		row := couponRow()
		row.Ended = true
		row.FirstOrderOnly = true
		row.UserSettledOrderCount = 3
		e := evaluateCoupon(row, true, 6000)
		if !strings.Contains(e.Reason, "已过期") {
			// 顺序反过来会让用户去查自己的账号，而问题根本不在他身上。
			t.Fatalf("应当先答券本身的状态，实际 %q", e.Reason)
		}
	})

	// 🔴 静默边界：`*_scope_unchecked` = 「这张券有范围限制，但你没告诉我买什么」。
	// 把它当成通过，用户会在套餐页看到「可用」、在结算页吃一个 422 ——
	// user-journey 把「校验说可以、下单说不行」列为最伤信任的一类反馈。
	t.Run("静默边界：范围未校验不等于校验通过", func(t *testing.T) {
		row := couponRow()
		row.PlanScopeUnchecked = true
		e := evaluateCoupon(row, false, 0)
		if e.Valid {
			t.Fatal("范围未校验时不能回 valid=true，否则套餐页说可用、下单 422")
		}
		if !strings.Contains(e.Reason, "先选好套餐") {
			t.Fatalf("reason 应当指路，实际 %q", e.Reason)
		}

		row2 := couponRow()
		row2.PeriodScopeUnchecked = true
		if evaluateCoupon(row2, false, 0).Valid {
			t.Fatal("周期范围未校验同样不能算通过")
		}
	})

	// 同一条道理的另一半：门槛存在但没有 gross 可比。
	t.Run("静默边界：有 min_amount 却不知道订单金额时，同样不能说「可用」", func(t *testing.T) {
		row := couponRow()
		row.MinAmount = 5000
		if evaluateCoupon(row, false, 0).Valid {
			t.Fatal("门槛没法比的时候不能回 valid=true")
		}
		e := evaluateCoupon(row, true, 4999)
		if e.Valid || !strings.Contains(e.Reason, "50.00") {
			t.Fatalf("低于门槛应当给出具体金额，实际 valid=%v reason=%q", e.Valid, e.Reason)
		}
	})

	t.Run("无门槛无范围的券，在不知道订单金额时也可以说「可用」（但不给折扣额）", func(t *testing.T) {
		e := evaluateCoupon(couponRow(), false, 0)
		if !e.Valid {
			t.Fatalf("应当可用：%s", e.Reason)
		}
		if e.Discount != 0 {
			t.Fatal("gross 未知时折扣额没有意义，必须留空")
		}
	})
}

type fakeCouponVerifier struct {
	row     dbgen.VerifyCouponForUserRow
	rowErr  error
	plan    dbgen.GetPlanForOrderRow
	planErr error
	gotArg  dbgen.VerifyCouponForUserParams
}

func (f *fakeCouponVerifier) VerifyCouponForUser(_ context.Context, arg dbgen.VerifyCouponForUserParams) (dbgen.VerifyCouponForUserRow, error) {
	f.gotArg = arg
	return f.row, f.rowErr
}

func (f *fakeCouponVerifier) GetPlanForOrder(context.Context, int64) (dbgen.GetPlanForOrderRow, error) {
	return f.plan, f.planErr
}

func TestVerifyCouponForUser(t *testing.T) {
	t.Run("正常路径：给了 plan_id + period 时算出折扣额", func(t *testing.T) {
		f := &fakeCouponVerifier{
			row:  couponRow(),
			plan: dbgen.GetPlanForOrderRow{ID: 3, PriceYearly: i64p(61200)},
		}
		period := dbgen.OrderPeriodYearly
		out, err := verifyCouponForUser(context.Background(), f, 42, "NEW10", i64p(3), &period)
		if err != nil {
			t.Fatal(err)
		}
		if !out.Valid {
			t.Fatalf("应当可用：%v", out.Reason)
		}
		if out.DiscountAmount == nil || *out.DiscountAmount != 6120 {
			t.Fatalf("折扣额应当是 6120，实际 %v", out.DiscountAmount)
		}
		if out.Type == nil || *out.Type != gen.Percent {
			// DB 是 'percentage'，契约是 "percent" —— 拼写不同，映射只此一处。
			t.Fatalf("type 应当映射成 percent，实际 %v", out.Type)
		}
		if f.gotArg.UserID != 42 || f.gotArg.Code != "NEW10" {
			t.Fatalf("参数没有透传：%+v", f.gotArg)
		}
	})

	// 错误码分支：verifyCoupon 契约里**没有 404**。
	// 「不存在」与「存在但不可用」都走 200 + valid=false，且文案不区分 ——
	// 区分等于把这个端点变成一台优惠码存在性探测器。
	t.Run("错误分支：码不存在时走 200 + valid=false，不泄漏存在性", func(t *testing.T) {
		f := &fakeCouponVerifier{rowErr: pgx.ErrNoRows}
		out, err := verifyCouponForUser(context.Background(), f, 1, "NOPE", nil, nil)
		if err != nil {
			t.Fatal("不存在不是错误")
		}
		if out.Valid || out.Reason == nil {
			t.Fatal("应当 valid=false 且给出原因")
		}
		if strings.Contains(*out.Reason, "不存在") {
			t.Fatalf("文案不得区分「不存在」，实际 %q", *out.Reason)
		}
	})

	t.Run("静默边界：套餐查不到时退回「gross 未知」，不把优惠码校验变成套餐校验", func(t *testing.T) {
		f := &fakeCouponVerifier{row: couponRow(), planErr: pgx.ErrNoRows}
		period := dbgen.OrderPeriodMonthly
		out, err := verifyCouponForUser(context.Background(), f, 1, "NEW10", i64p(999), &period)
		if err != nil {
			t.Fatalf("套餐不存在不应让优惠码校验失败：%v", err)
		}
		if out.DiscountAmount != nil {
			t.Fatal("gross 未知时不应给折扣额")
		}
	})

	t.Run("错误分支：数据库错误上抛", func(t *testing.T) {
		f := &fakeCouponVerifier{rowErr: errors.New("boom")}
		if _, err := verifyCouponForUser(context.Background(), f, 1, "X", nil, nil); err == nil {
			t.Fatal("数据库错误必须上报")
		}
	})
}

// ============================================================
// 周期与价格
// ============================================================

func TestOrderPeriodFromContract(t *testing.T) {
	for _, ok := range []string{"monthly", "quarterly", "half_yearly", "yearly", "onetime"} {
		if _, err := orderPeriodFromContract(ok); err != nil {
			t.Fatalf("%s 应当被接受：%v", ok, err)
		}
	}
	// 🔴 契约的枚举比 DB 多这两个值（ADR 0013 §4.7）。不挡的后果不是「多支持两个周期」，
	// 而是请求通过 spec 校验、在 INSERT 时报 invalid input value for enum —— 一条 500。
	for _, bad := range []string{"two_yearly", "three_yearly", "weekly", ""} {
		if _, err := orderPeriodFromContract(bad); err == nil {
			t.Fatalf("%q 必须被拒绝（DB 的 order_period 里没有它）", bad)
		}
	}
}

func TestPlanPriceAtPeriod(t *testing.T) {
	m, q, h, y, o := i64p(1), i64p(2), i64p(3), i64p(4), i64p(5)
	cases := map[dbgen.OrderPeriod]int64{
		dbgen.OrderPeriodMonthly:    1,
		dbgen.OrderPeriodQuarterly:  2,
		dbgen.OrderPeriodHalfYearly: 3,
		dbgen.OrderPeriodYearly:     4,
		dbgen.OrderPeriodOnetime:    5,
	}
	for p, want := range cases {
		got := planPriceAtPeriod(m, q, h, y, o, p)
		if got == nil || *got != want {
			t.Fatalf("%s 应当取到 %d，实际 %v", p, want, got)
		}
	}
	// NULL = 该周期不售，必须原样返回 nil 让调用方 422，而不是当成 0 元。
	if planPriceAtPeriod(nil, nil, nil, nil, nil, dbgen.OrderPeriodYearly) != nil {
		t.Fatal("不售的周期必须返回 nil")
	}
}

func TestYuan(t *testing.T) {
	for cents, want := range map[int64]string{0: "0.00", 5: "0.05", 100: "1.00", 61200: "612.00", -150: "-1.50"} {
		if got := yuan(cents); got != want {
			t.Fatalf("yuan(%d) = %q，期望 %q", cents, got, want)
		}
	}
}
