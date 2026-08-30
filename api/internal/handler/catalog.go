package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 套餐页 / 公告 / 优惠码校验（listPlans · listNotices · verifyCoupon）。
//
// 三个都是读端点，但它们各自有一条「写错了不会报错、只会静默给出错误答案」的边界，
// 本文件的注释密度基本都花在那三条上：
//
//  1. **listPlans 必须带 user_id**（`ListPlansForUser` 的 WHERE 里那个子查询）。
//     漏了它，`sellable=false AND renewable=true` 的套餐（下架但允许老用户续费）
//     对它自己的订户也不可见 —— 于是「下架一个套餐」被静默实现成「给它的订户涨价」：
//     老用户打开套餐页看不到自己在用的那一档，唯一能点的按钮是买一个更贵的。
//
//  2. **listNotices 的游标必须带 pinned**。排序键是 (pinned DESC, created_at DESC, id DESC)，
//     而置顶公告的 created_at 通常比第一页的普通公告更旧；游标只带 (at,id) 时，
//     置顶公告会在**每一页**重新满足 `created_at < cursor_at` 而再出现一次。
//     这条是上一轮在真库上实测确认过的，不是推理。
//
//  3. **verifyCoupon 的「没法判」不等于「判过了」**。`VerifyCouponForUser` 用
//     `*_scope_unchecked` 两个布尔位显式表达「这张券有范围限制，但你没告诉我买什么」，
//     把它当成通过，用户会在套餐页看到「可用」、在下单时吃一个 422 ——
//     user-journey 把「校验说可以、下单说不行」列为最伤信任的一类反馈。
//
// ⚠️ 与 `orders.sql` 的分工在 catalog.sql 的文件头写清楚了：那边的
// `ListSellablePlans` / `GetPlan` / `GetCouponByCode` 是管理面与内部逻辑用的粗粒度读，
// 用户面三个 operation **一条都不能直接用**。最要命的是 `GetCouponByCode`：
// 它把有效期与用尽写进 WHERE 返回 0 行，而 0 行回答不了契约要求的 `reason`「为什么不可用」。

// ============================================================
// 游标（本文件与 order.go 共用）
// ============================================================

// keysetCursor 是本组端点（公告 / 订单）游标的线格式。
//
// api-contract §2.4 定的形状是 base64url 的 `{"id":…,"at":"…"}`，**不签名** ——
// 它只是位置不是凭据，签名只会让「翻页」多一把需要轮换的密钥。
// 但 §2.4 同时要求「服务端必须校验解出的字段类型」，所以三个字段全部用指针接：
// 指针能区分「字段缺失」与「字段是零值」，而这两者的正确处置完全不同
// （缺失 = 游标非法 → 400；零值 = 一个合法的边界值）。
//
// 🔴 `Pinned` 只在公告游标里出现。它不在 §2.4 的示例形状里，是本实现多带的一位 ——
// 游标是不透明串，多带一个分量不违反契约；而少带它会让置顶公告每页重复一次（见文件头第 2 条）。
type keysetCursor struct {
	ID     *int64     `json:"id"`
	At     *time.Time `json:"at"`
	Pinned *bool      `json:"pinned,omitempty"`
}

var errBadCursor = errors.New("游标非法")

// encodeKeysetCursor 编码游标。
//
// 用 RawURLEncoding（无 `=` 填充）而不是 StdEncoding：游标会出现在查询串里，
// `+` `/` `=` 三个字符都要转义，而转义过一次的游标被某些客户端二次转义之后就解不回来了。
func encodeKeysetCursor(c keysetCursor) string {
	b, err := json.Marshal(c)
	if err != nil {
		// 结构体里只有指针到基本类型，json.Marshal 不可能失败。
		// 真失败了返回空串（= 没有下一页）比 panic 好：翻不了页是可见的，panic 是 500。
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeKeysetCursor 解码并**校验**游标。
//
// 三道校验，每一道都对应一种「不校验会怎样」：
//   - base64 / JSON 解不开 → 400。不解开就当成「第一页」会让用户在翻到一半时
//     被无声地弹回开头，而他看到的现象是「翻页按钮没反应」。
//   - `DisallowUnknownFields` → 拒绝多余字段。游标是我们自己发出去的，
//     出现未知字段说明它被人手工构造过，此时最安全的回答是「这不是我发的游标」。
//   - id / at 缺失 → 400。少一个分量的行比较在 SQL 里求值为 NULL，
//     返回 0 行而**不报错** —— 用户看到的是「后面没有了」，而不是「游标坏了」。
func decodeKeysetCursor(raw string) (keysetCursor, error) {
	var c keysetCursor
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return c, errBadCursor
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, errBadCursor
	}
	if c.ID == nil || c.At == nil {
		return c, errBadCursor
	}
	return c, nil
}

// pageLimit 把契约的 `limit` 参数归一化成 SQL 要的 `page_limit`（= limit + 1）。
//
// 多取一行是 has_more 的判据（api-contract §2.4）。
// 🔴 **不要用「返回行数 == limit」判 has_more**：总数正好整除时，最后一页会被判成「还有下一页」，
// 用户点进去看到一页空数据 —— 而空页在前端通常长得像加载失败。
func pageLimit(limit *gen.LimitQuery) (want int, pageLimit int32) {
	want = defaultPageLimit
	if limit != nil {
		want = int(*limit)
	}
	// 契约给的是 minimum:1 / maximum:100，但校验中间件是否挂载不由本 handler 决定，
	// 而一个 limit=0 会让 SQL 的 `LIMIT 1` 只返回那行多余的探测行、正文为空。
	if want < 1 {
		want = 1
	}
	if want > maxPageLimit {
		want = maxPageLimit
	}
	return want, int32(want + 1)
}

const (
	defaultPageLimit = 20  // api-contract §2.4
	maxPageLimit     = 100 // 同上
)

// ============================================================
// listPlans
// ============================================================

type planLister interface {
	ListPlansForUser(ctx context.Context, userID int64) ([]dbgen.ListPlansForUserRow, error)
}

// ListPlans 实现 GET /api/v1/plans。
func (s *Server) ListPlans(ctx context.Context, _ gen.ListPlansRequestObject) (gen.ListPlansResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	plans, err := listPlansForUser(ctx, s.db, auth.UserID)
	if err != nil {
		return gen.ListPlans500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取套餐列表失败", err)}, nil
	}
	return gen.ListPlans200JSONResponse{Data: plans, Meta: s.meta(ctx)}, nil
}

// listPlansForUser 取套餐并映射成契约的 `Plan`。
func listPlansForUser(ctx context.Context, q planLister, userID int64) ([]gen.Plan, error) {
	rows, err := q.ListPlansForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	// 空切片而不是 nil：契约里 `data` 是 required 的数组，nil 会序列化成 `null`，
	// 而前端对 `null` 与 `[]` 的处理分支不同（多数会在 `.map` 上炸）。
	out := make([]gen.Plan, 0, len(rows))
	for _, r := range rows {
		out = append(out, planView(r))
	}
	return out, nil
}

func planView(r dbgen.ListPlansForUserRow) gen.Plan {
	currency := gen.PlanCurrencyCNY
	// plans 表**没有** currency 列 —— 全站单币种计价，这里是常量而不是漏读了一列。
	desc := r.ContentMd
	sort := r.SortOrder
	reset := string(r.ResetTrafficMethod)
	visible := true // 查询的 WHERE 里就是 visible = true，走到这里的行必然可见。

	p := gen.Plan{
		Id:                  r.ID,
		Name:                r.Name,
		Type:                planTypeView(r.Kind),
		Description:         &desc,
		TransferEnableBytes: r.TransferEnable,
		ResetTrafficMethod:  &reset,
		SpeedLimitMbps:      r.SpeedLimitMbps,
		Sort:                &sort,
		Currency:            &currency,
		Visible:             &visible,
		Prices:              planPrices(r.PriceMonthly, r.PriceQuarterly, r.PriceHalfYearly, r.PriceYearly, r.PriceOnetime),
	}
	// 契约把 device_limit 定成非空 int32，DB 里它可空且 NULL = 不限设备。
	// 映射成 0 而不是随便挑一个大数：0 在契约里没有「零台设备」的合理解释，
	// 前端只能把它读成「不限」；给一个具体数字（比如 999）反而会被当成真的上限显示出来。
	if r.DeviceLimit != nil {
		p.DeviceLimit = *r.DeviceLimit
	}
	return p
}

// planTypeView 把 plans.kind 映射成契约的 PlanType。
//
// 🔴 这个映射必须显式：DB 是 'cycle' / 'pack'，契约是 "period" / "traffic_pack"，
// 两套拼写没有一个字母重合。把 kind 直接 fmt 出去，前端拿到的是契约枚举里不存在的值，
// 而 JSON 没有类型检查 —— 现象是套餐页整块空白，不是报错。
func planTypeView(kind string) gen.PlanType {
	if kind == planKindPack {
		return gen.PlanTypeTrafficPack
	}
	return gen.PlanTypePeriod
}

// planPrices 把五个可空价格列摊成契约的 prices[]。
//
// NULL = 该周期不售（0002 的列注释），所以 NULL 的周期**整条不出现**，
// 而不是出现一条 amount = 0 —— 后者在页面上是「免费」，不是「不卖」。
//
// ⚠️ 契约的 `PlanPrice.period` 枚举含 `two_yearly` / `three_yearly`，而 plans 根本没有这两列
// （ADR 0013 §4.7 登记的四处契约/DB 不一致之一）。所以本函数只可能产出五个周期，
// **不要按契约枚举去反推列名**；修契约是上线前的独立动作，以 DB 为准（data-model §14.1）。
func planPrices(monthly, quarterly, halfYearly, yearly, onetime *int64) []gen.PlanPrice {
	out := make([]gen.PlanPrice, 0, 5)
	add := func(p gen.PlanPricePeriod, amount *int64) {
		if amount != nil {
			out = append(out, gen.PlanPrice{Period: p, Amount: *amount})
		}
	}
	add(gen.PlanPricePeriodMonthly, monthly)
	add(gen.PlanPricePeriodQuarterly, quarterly)
	add(gen.PlanPricePeriodHalfYearly, halfYearly)
	add(gen.PlanPricePeriodYearly, yearly)
	add(gen.PlanPricePeriodOnetime, onetime)
	return out
}

// ============================================================
// listNotices
// ============================================================

type noticeLister interface {
	ListNoticesPage(ctx context.Context, arg dbgen.ListNoticesPageParams) ([]dbgen.ListNoticesPageRow, error)
}

// ListNotices 实现 GET /api/v1/notices。
func (s *Server) ListNotices(ctx context.Context, req gen.ListNoticesRequestObject) (gen.ListNoticesResponseObject, error) {
	if _, ok := middleware.UserFrom(ctx); !ok {
		return nil, errNoUserAuth
	}

	want, limitPlusOne := pageLimit(req.Params.Limit)

	var cur keysetCursor
	if req.Params.Cursor != nil && *req.Params.Cursor != "" {
		c, err := decodeKeysetCursor(*req.Params.Cursor)
		if err != nil || c.Pinned == nil {
			// 🔴 缺 pinned 分量的游标一律**丢弃**，不能退化成「只按时间翻页」——
			//    那正是让置顶公告每页重复一次的写法（文件头第 2 条），
			//    而重复不报错，用户看到的是「他们把同一条公告发了三遍」。
			//
			// 为什么是退回第一页而不是 400：契约给 listNotices 只声明了 401 与 500，
			// 没有 400 可用，而 500 会把一个客户端问题谎报成服务端故障。
			// 代价是位置被静默丢掉，所以必须留一条 Warn ——
			// 「翻页按钮好像没反应」这类工单只能靠它回答。
			s.logger.WarnContext(ctx, "公告游标非法或缺少 pinned 分量，按第一页处理",
				"request_id", middleware.RequestIDFrom(ctx))
			cur = keysetCursor{}
		} else {
			cur = c
		}
	}

	rows, err := s.db.ListNoticesPage(ctx, noticePageParams(cur, limitPlusOne))
	if err != nil {
		return gen.ListNotices500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "读取公告失败", err)}, nil
	}

	data, next, hasMore := noticePage(rows, want)
	meta := s.meta(ctx)
	meta.HasMore = &hasMore
	meta.NextCursor = next
	return gen.ListNotices200JSONResponse{Data: data, Meta: meta}, nil
}

func noticePageParams(cur keysetCursor, limitPlusOne int32) dbgen.ListNoticesPageParams {
	p := dbgen.ListNoticesPageParams{PageLimit: limitPlusOne}
	if cur.At != nil && cur.ID != nil && cur.Pinned != nil {
		p.CursorAt = tstz(*cur.At)
		p.CursorID = cur.ID
		p.CursorPinned = cur.Pinned
	}
	return p
}

// noticePage 切掉多取的那一行、编下一页游标。
//
// 返回 `has_more` 与 `next_cursor` 两个值而不是一个：契约要求两者同时出现，
// 且「无更多数据时 next_cursor 必须是 null」（§2.4）—— 发一个指向空页的游标
// 会让前端的「加载更多」按钮永远存在。
func noticePage(rows []dbgen.ListNoticesPageRow, want int) ([]gen.Notice, *string, bool) {
	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.Notice, 0, len(rows))
	for i := range rows {
		r := rows[i]
		n := gen.Notice{
			Id:      r.ID,
			Title:   r.Title,
			Content: r.ContentMd,
			// 取切片元素的地址而不是循环变量的：Go 1.22 之后循环变量每轮独立，
			// 两种写法都对，但指向底层数组更难在将来被一次「改回 for _, r := range」弄错。
			Pinned: &rows[i].Pinned,
		}
		// notices 表没有 published_at 列，而契约里它是 required。
		// SQL 侧已经 coalesce(starts_at, created_at) 算好了，这里只做零值防御：
		// 两列都是 NOT NULL，理论上 Valid 恒为 true，但一个零值 time.Time 序列化出来是
		// "0001-01-01T00:00:00Z"，在页面上会排到所有公告的最后面而不是报错。
		if r.PublishedAt.Valid {
			n.PublishedAt = r.PublishedAt.Time.UTC()
		}
		out = append(out, n)
	}
	if !hasMore || len(rows) == 0 {
		return out, nil, false
	}
	last := rows[len(rows)-1]
	cur := keysetCursor{ID: &last.ID, Pinned: &last.Pinned}
	if last.CreatedAt.Valid {
		at := last.CreatedAt.Time.UTC()
		cur.At = &at
	}
	// 🔴 游标编的是 created_at，**不是 published_at**。
	// 排序键是 (pinned, created_at, id)，游标必须与排序键逐字同源；
	// 拿 coalesce 出来的 published_at 去比 created_at，在 starts_at 与 created_at
	// 不相等的公告（= 所有定时发布的公告）上会直接跳过或重复一批行。
	enc := encodeKeysetCursor(cur)
	if enc == "" {
		return out, nil, false
	}
	return out, &enc, true
}

// ============================================================
// verifyCoupon
// ============================================================

type couponVerifier interface {
	VerifyCouponForUser(ctx context.Context, arg dbgen.VerifyCouponForUserParams) (dbgen.VerifyCouponForUserRow, error)
	GetPlanForOrder(ctx context.Context, planID int64) (dbgen.GetPlanForOrderRow, error)
}

// VerifyCoupon 实现 POST /api/v1/coupons/verify。**只校验，不核销。**
func (s *Server) VerifyCoupon(ctx context.Context, req gen.VerifyCouponRequestObject) (gen.VerifyCouponResponseObject, error) {
	auth, ok := middleware.UserFrom(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	if req.Body == nil || strings.TrimSpace(req.Body.Code) == "" {
		return gen.VerifyCoupon422JSONResponse{ErrUnprocessableJSONResponse: s.unprocessable(ctx, "缺少优惠码", detail("code", "必填"))}, nil
	}

	var period *dbgen.OrderPeriod
	if req.Body.Period != nil && *req.Body.Period != "" {
		p, err := orderPeriodFromContract(*req.Body.Period)
		if err != nil {
			return gen.VerifyCoupon422JSONResponse{
				ErrUnprocessableJSONResponse: s.unprocessable(ctx, err.Error(), detail("period", *req.Body.Period)),
			}, nil
		}
		period = &p
	}

	res, err := verifyCouponForUser(ctx, s.db, auth.UserID, strings.TrimSpace(req.Body.Code), req.Body.PlanId, period)
	if err != nil {
		return gen.VerifyCoupon500JSONResponse{ErrInternalJSONResponse: s.internalErr(ctx, "校验优惠码失败", err)}, nil
	}
	return gen.VerifyCoupon200JSONResponse{Data: res, Meta: s.meta(ctx)}, nil
}

// couponEval 是一次优惠码判定的结果，createOrder 与 verifyCoupon 共用。
//
// 两个端点共用一份判定是硬要求：分开写的那天起，「校验说可用、下单说不可用」
// 就成了一个必然会发生的 bug，而它对用户是纯粹的背叛感（user-journey 点名）。
type couponEval struct {
	Row      dbgen.VerifyCouponForUserRow
	Found    bool
	Valid    bool
	Reason   string // 不可用时的中文原因（契约字段 CouponVerifyResult.reason）
	Discount int64  // 分；只有 Valid 且 gross 已知时才有意义
}

// evaluateCoupon 把 `VerifyCouponForUser` 的一行布尔位翻成「能不能用 + 为什么」。
//
// grossKnown 为 false 表示调用方还不知道订单原价（用户在套餐页只填了码）。
// 这一位存在的理由是两条判定必须等 gross：`min_amount` 与百分比折扣额。
//
// 🔴 判定顺序有意义。先答**这张券本身**的状态（未开始 / 已过期 / 已用尽），
// 再答**这个人**的资格（首单 / 次数），最后答**这个订单**的匹配（范围 / 门槛）。
// 顺序反过来的话，一张已经过期的券会先告诉用户「你不是新用户」——
// 一个既不准确、又让人去查自己账号的错误引导。
func evaluateCoupon(row dbgen.VerifyCouponForUserRow, grossKnown bool, gross int64) couponEval {
	e := couponEval{Row: row, Found: true}

	switch {
	case row.NotStarted:
		e.Reason = "优惠码尚未开始生效"
		return e
	case row.Ended:
		e.Reason = "优惠码已过期"
		return e
	case row.Exhausted:
		e.Reason = "优惠码已被领完"
		return e
	case row.FirstOrderOnly && row.UserSettledOrderCount > 0:
		e.Reason = "该优惠码仅限首次下单使用"
		return e
	case row.UsesPerUser > 0 && row.UserUsedCount >= int64(row.UsesPerUser):
		e.Reason = "该优惠码你已使用过，不能再次使用"
		return e
	case row.PlanOutOfScope:
		e.Reason = "该优惠码不适用于所选套餐"
		return e
	case row.PeriodOutOfScope:
		e.Reason = "该优惠码不适用于所选付费周期"
		return e
	}

	// 🔴 `*_scope_unchecked` = 「这张券有范围限制，但你没告诉我买什么，所以没校验」。
	//    **不能当成通过。** 当成通过的后果是套餐页显示「可用」、下单时 422 ——
	//    而用户此刻已经在结算页上，他看到的是「你们的优惠码是假的」。
	//    这里回一个 valid=false + 一句指路的文案，把这次失败提前到他还没决定买什么的时候。
	if row.PlanScopeUnchecked || row.PeriodScopeUnchecked {
		e.Reason = "该优惠码仅适用于部分套餐或部分周期，请先选好套餐与周期再校验"
		return e
	}

	if !grossKnown {
		if row.MinAmount > 0 {
			// 同上：门槛存在但比不了，就不能说「可用」。
			e.Reason = "该优惠码有最低消费门槛，请先选好套餐与周期再校验"
			return e
		}
		// 没有门槛、没有范围限制 —— 这张券对任何订单都成立，可以在不知道 gross 时就说「可用」。
		// 折扣额留空（契约里 discount_amount 是 optional），因为百分比券的折扣额依赖 gross。
		e.Valid = true
		return e
	}

	if gross < row.MinAmount {
		e.Reason = fmt.Sprintf("订单金额需满 ¥%s 才能使用该优惠码", yuan(row.MinAmount))
		return e
	}

	e.Valid = true
	e.Discount = couponDiscount(row, gross)
	return e
}

// couponDiscount 算折扣额（分）。**全程整数，绝不引入 float。**
//
// 单位随 type 变（0006 的列注释）：
//   - 'percentage' → value 是**基点 bps**，1000 = 10%，折扣 = floor(gross × value / 10000)
//   - 'fixed_amount' → value 直接是分
//
// 两处都用 floor 且封顶到 gross：折扣超过原价时 amount_gross − amount_discount 会变成负数，
// 而 orders 上的 `CHECK (amount_due >= 0)` 会把它变成一次 500 —— 一个本该是「折扣封顶」的产品规则，
// 变成用户看到的「下单失败」。
func couponDiscount(row dbgen.VerifyCouponForUserRow, gross int64) int64 {
	var d int64
	switch row.Type {
	case couponTypePercentage:
		d = gross * row.Value / 10000
	case couponTypeFixedAmount:
		d = row.Value
	default:
		// coupons.type 有 CHECK 约束，走到这里说明约束被改过。
		// 返回 0 折扣而不是报错：用户至少能按原价买到东西。
		return 0
	}
	if d < 0 {
		return 0
	}
	if d > gross {
		return gross
	}
	return d
}

const (
	couponTypePercentage  = "percentage"
	couponTypeFixedAmount = "fixed_amount"
)

// couponTypeView 把 DB 的 coupons.type 映射成契约的 CouponVerifyResultType。
// 拼写不同（'percentage'→"percent"，'fixed_amount'→"fixed"），映射只此一处。
func couponTypeView(t string) *gen.CouponVerifyResultType {
	var v gen.CouponVerifyResultType
	switch t {
	case couponTypePercentage:
		v = gen.Percent
	case couponTypeFixedAmount:
		v = gen.Fixed
	default:
		return nil
	}
	return &v
}

// verifyCouponForUser 组装契约的 CouponVerifyResult。
func verifyCouponForUser(
	ctx context.Context,
	q couponVerifier,
	userID int64,
	code string,
	planID *int64,
	period *dbgen.OrderPeriod,
) (gen.CouponVerifyResult, error) {
	out := gen.CouponVerifyResult{Code: code}

	row, err := q.VerifyCouponForUser(ctx, dbgen.VerifyCouponForUserParams{
		Code:   code,
		UserID: userID,
		PlanID: planID,
		Period: period,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// 「不存在」与「存在但不可用」都走 200 + valid=false —— 契约里 verifyCoupon 没有 404。
		// 文案上也不区分：区分等于把这个端点变成一台优惠码存在性探测器。
		reason := "优惠码无效"
		out.Reason = &reason
		return out, nil
	}
	if err != nil {
		return out, err
	}

	// gross 只有在同时给了 plan_id 与 period 时才算得出来。
	var gross int64
	grossKnown := false
	if planID != nil && period != nil {
		plan, err := q.GetPlanForOrder(ctx, *planID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// 套餐不存在：不在这里报错（校验优惠码不是校验套餐），
			// 退回「gross 未知」分支，由下单时的 422 给出准确的错误。
		case err != nil:
			return out, err
		default:
			if p := planPriceAtPeriod(plan.PriceMonthly, plan.PriceQuarterly, plan.PriceHalfYearly,
				plan.PriceYearly, plan.PriceOnetime, *period); p != nil {
				gross, grossKnown = *p, true
			}
		}
	}

	e := evaluateCoupon(row, grossKnown, gross)
	out.Valid = e.Valid
	out.Type = couponTypeView(row.Type)
	if e.Valid {
		if grossKnown {
			d := e.Discount
			out.DiscountAmount = &d
		}
		return out, nil
	}
	reason := e.Reason
	out.Reason = &reason
	return out, nil
}

// ============================================================
// 周期与价格：本文件与 order.go 共用
// ============================================================

const (
	planKindCycle = "cycle"
	planKindPack  = "pack"
)

// orderPeriodFromContract 把契约的周期字符串收成 DB 的 order_period。
//
// 🔴 契约的枚举比 DB 多两个值（`two_yearly` / `three_yearly`，ADR 0013 §4.7）。
// 不显式挡掉的后果不是「多支持了两个周期」，而是请求通过 spec 校验、
// 在 INSERT 时报 `invalid input value for enum order_period` —— 一条 500。
// 挡在这里，用户拿到的是一句能看懂的 422。
func orderPeriodFromContract(raw string) (dbgen.OrderPeriod, error) {
	switch raw {
	case string(dbgen.OrderPeriodMonthly):
		return dbgen.OrderPeriodMonthly, nil
	case string(dbgen.OrderPeriodQuarterly):
		return dbgen.OrderPeriodQuarterly, nil
	case string(dbgen.OrderPeriodHalfYearly):
		return dbgen.OrderPeriodHalfYearly, nil
	case string(dbgen.OrderPeriodYearly):
		return dbgen.OrderPeriodYearly, nil
	case string(dbgen.OrderPeriodOnetime):
		return dbgen.OrderPeriodOnetime, nil
	case "two_yearly", "three_yearly":
		return "", errors.New("暂不支持该付费周期（契约枚举含此值，但数据库与价目表都没有它）")
	default:
		return "", errors.New("未知的付费周期")
	}
}

// planPriceAtPeriod 取某个周期的标价（分）；返回 nil 表示**该周期不售**。
//
// 这个 switch 是 catalog.sql 那段注释里说的「period → 列 的映射写在 handler 里，只此一处」：
// SQL 侧刻意把五个价格列全交出来，是因为在 SQL 里写 `CASE $period ... END::bigint`
// 会让 sqlc 把该列判成 NOT NULL，于是「这个套餐不卖年付」这件正常的业务事实
// 变成一次运行时 scan 失败，而且只在有人第一次买那个周期时才炸。
func planPriceAtPeriod(monthly, quarterly, halfYearly, yearly, onetime *int64, p dbgen.OrderPeriod) *int64 {
	switch p {
	case dbgen.OrderPeriodMonthly:
		return monthly
	case dbgen.OrderPeriodQuarterly:
		return quarterly
	case dbgen.OrderPeriodHalfYearly:
		return halfYearly
	case dbgen.OrderPeriodYearly:
		return yearly
	case dbgen.OrderPeriodOnetime:
		return onetime
	default:
		return nil
	}
}

// yuan 把「分」渲染成给人看的元。只用于错误文案，**不用于任何计算**。
func yuan(cents int64) string {
	neg := ""
	if cents < 0 {
		neg, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", neg, cents/100, cents%100)
}
