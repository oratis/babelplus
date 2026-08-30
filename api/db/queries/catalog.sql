-- catalog.sql · 套餐页、公告、优惠码校验（用户面读路径）
--
-- 事实源：openapi/openapi.yaml 的 `listPlans` / `listNotices` / `verifyCoupon` 三个 operation
--         与 `Plan` / `PlanPrice` / `Notice` / `CouponVerifyResult` 三个 schema（契约冻结）；
--         列名逐列核自 0002_foundation / 0006_orders / 0011_ops / 0016_billing_rules。
--
-- 🔴 金额一律 bigint 存**人民币分**（api-contract §2.6）。本文件不产生任何货币计算，
--    只把原始列交出去 —— 折扣与折抵的算术全部在 Go 侧用整数做（见 orders_user.sql 的注释）。
--
-- ⚠️ 与 `orders.sql` 的分工：那边的 `ListSellablePlans` / `GetPlan` / `GetCouponByCode`
--    是**管理面与内部逻辑**用的粗粒度读（`SELECT *`、校验条件写进 WHERE）。
--    用户面三个 operation 不能直接用它们，理由逐条写在下面每条查询的头上；
--    最要命的一条是 `GetCouponByCode` 把有效期与用尽判定写在 WHERE 里 ——
--    那条查询返回 0 行时，调用方**无法回答 `CouponVerifyResult.reason`「为什么不可用」**。


-- ============================================================
-- 套餐（listPlans / createOrder 的定价输入）
-- ============================================================

-- listPlans。返回的行按 `Plan` schema 逐字段映射，映射关系只此一处，写清楚：
--
--   Plan.type                 ← plans.kind：'cycle' → "period"，'pack' → "traffic_pack"
--   Plan.description          ← plans.content_md
--   Plan.transfer_enable_bytes← plans.transfer_enable
--   Plan.sort                 ← plans.sort_order
--   Plan.currency             ← 常量 "CNY"（plans **没有** currency 列，全站单币种计价）
--   Plan.prices[]             ← 五个 price_* 列里非 NULL 的那些，NULL = 该周期不售（0002 的列注释）
--
-- ⚠️ `PlanPrice.period` 的契约枚举含 `two_yearly` / `three_yearly`，而 `plans` **根本没有**
--    这两列，`order_period` 枚举里也没有这两个值（ADR 0013 §4.7 第 1 行登记的四处不一致之一）。
--    契约冻结不能改，所以这条查询只可能产出五个周期；调用方**不得**凭契约枚举去反推列名。
--    以 DB 为准（data-model §14.1），修契约是上线前的独立动作。
--
-- 为什么要 user_id：一个 `sellable = false` 但 `renewable = true` 的套餐（下架但允许老用户续费，
-- 0002 把这两个开关拆开就是为了表达这件事）**必须对它自己的订户可见**。
-- 漏掉这一条，「下架」会被静默实现成「强制升级」—— 老用户打开套餐页看不到自己的套餐，
-- 唯一能点的按钮是买一个更贵的。这不是少显示一行，是一次涨价。
--
-- 走 plans_visible_idx (sort_order, id) WHERE visible AND archived_at IS NULL；
-- 套餐总数是个位数，这条索引的意义只在于让排序稳定，不在于性能。
-- name: ListPlansForUser :many
SELECT
  p.id, p.code, p.name, p.kind, p.content_md,
  p.transfer_enable, p.device_limit, p.speed_limit_mbps, p.reset_traffic_method,
  p.price_monthly, p.price_quarterly, p.price_half_yearly, p.price_yearly, p.price_onetime,
  p.sort_order, p.sellable, p.renewable
FROM plans p
WHERE p.archived_at IS NULL
  AND p.visible = true
  AND (
    p.sellable = true
    OR (p.renewable = true
        AND p.id IS NOT DISTINCT FROM (SELECT u.plan_id FROM users u
                                        WHERE u.id = sqlc.arg(user_id)::bigint
                                          AND u.deleted_at IS NULL))
  )
ORDER BY p.sort_order, p.id;

-- createOrder 的定价输入。**同一条查询要被调用两次**：一次取 `plan_new`，
-- 一次取用户当前套餐（升级折抵要拿「当前套餐在 source.period 上的标价」，ADR 0013 §4.2）。
--
-- 🔴 刻意**不过滤** `archived_at`，也不过滤 `sellable`：
--    当前套餐完全可能已经下架，而升级折抵的分母 `price_cur` 就在那一行上。
--    把这两个条件写进 WHERE，等于让「我们下架了一个套餐」变成「它的所有订户都升不了级」——
--    而且失败形态是 404，看上去像订单出了问题，实际是套餐管理动作的远端副作用。
--    可售性由调用方读 `sellable` / `renewable` / `archived_at` 自己判，判错了是 422 不是 404。
--
-- ⚠️ 为什么把五个价格列全交出去，而不是在 SQL 里 `CASE $period WHEN … END AS price_at_period`：
--    那个 CASE 的结果在「该周期不售」时是 NULL，而要让 sqlc 给出确定的 Go 类型就必须写 `::bigint`，
--    加了 cast 之后 sqlc 判定该列 NOT NULL —— 于是「这个套餐不卖年付」这件**正常的业务事实**
--    会变成一次运行时 scan 失败（`cannot scan NULL into int64`），且只在有人第一次买那个周期时才炸。
--    用一次 Go 侧的 switch 换掉一个运行时崩溃是划算的；period → 列 的映射写在 handler 里，只此一处。
--
-- price_monthly 单独说明：它是 `orders.price_monthly_at_order` 的快照源（0016 的列注释），
-- 也是 C6 一次性返佣定额的乘数（返佣 = price_monthly × rate_bps / 10000，见
-- orders_user.sql 的 GetOrderCommissionAccrual）。取到之后必须原样写进订单，不能事后回读活列。
-- name: GetPlanForOrder :one
SELECT
  p.id, p.code, p.name, p.kind, p.group_id,
  p.transfer_enable, p.device_limit, p.speed_limit_mbps, p.reset_traffic_method,
  p.price_monthly, p.price_quarterly, p.price_half_yearly, p.price_yearly, p.price_onetime,
  p.price_reset, p.sellable, p.renewable, p.visible, p.archived_at
FROM plans p
WHERE p.id = sqlc.arg(plan_id)::bigint;


-- ============================================================
-- 公告（listNotices）
-- ============================================================

-- listNotices，游标分页（api-contract §2.4：用户面默认游标、**不返总数**）。
--
-- 字段映射：Notice.content ← notices.content_md；
--           Notice.published_at ← **coalesce(starts_at, created_at)**。
-- ⚠️ `notices` 没有 published_at 列（0011 建表逐列核过），而契约里它是 required。
--    取 `starts_at` 是因为它就是「定时发布」那一列；为空时退回 `created_at`，
--    因为「立刻发布」的公告在 starts_at 上留空。这两列都不动，映射只在 SELECT 里发生 ——
--    落一列冗余的 published_at 会与这两列漂移，而漂移的那天没有任何报错。
--
-- 🔴 游标必须带 **pinned**，不能只带 (created_at, id)。
--    排序键是 (pinned DESC, created_at DESC, id DESC)：置顶公告排在前面，而它们的 created_at
--    通常比第一页的普通公告更**旧**。游标只带时间戳的话，翻到第二页时
--    「pinned 且更旧」的行会重新满足 `created_at < cursor_at` —— 置顶公告在每一页都再出现一次。
--    这与 api-contract §2.4 示例里的 `{"id":…,"at":"…"}` 形状有出入：游标是**不透明串不是契约字段**
--    （§2.4 原文只要求服务端校验解出的字段类型），所以这里多带一位 `pinned`，
--    并在 handler 的编解码里显式校验它是 boolean。
--
-- 行比较 `(a,b,c) < (x,y,z)` 在**三列同向 DESC** 时才等价于「排在它后面」；
--    boolean 的序是 false < true，所以 pinned=true 的行天然排在前面，与 ORDER BY pinned DESC 一致。
--    任何一列改成 ASC，这个写法就**静默**错位（不报错，只是漏行/重行）—— 改排序必须同时改这里。
--
-- 刻意**不把 sort_order 放进排序键**（notices_visible_idx 里有它）：那会让游标变成四个分量，
--    而 sort_order 服务的是管理面的人工排版，用户面用「置顶 + 时间倒序」已经够。
--    代价是这条查询只吃到 notices_visible_idx 的前缀，剩下靠一次 sort ——
--    公告是几十行量级的表，这个代价是零。登记在此，免得后人把它当成索引全命中。
-- name: ListNoticesPage :many
SELECT
  n.id, n.title, n.content_md, n.level, n.pinned,
  coalesce(n.starts_at, n.created_at)::timestamptz AS published_at,
  n.created_at
FROM notices n
WHERE n.visible = true
  AND (n.starts_at IS NULL OR n.starts_at <= now())
  AND (n.ends_at   IS NULL OR n.ends_at   >  now())
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR (n.pinned, n.created_at, n.id)
       < (sqlc.narg(cursor_pinned)::boolean, sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::bigint)
  )
ORDER BY n.pinned DESC, n.created_at DESC, n.id DESC
LIMIT sqlc.arg(page_limit)::integer;


-- ============================================================
-- 优惠码（verifyCoupon —— 只校验、**不核销**）
-- ============================================================

-- verifyCoupon。契约要求返回 `CouponVerifyResult{code, valid, discount_amount, type, reason}`，
-- 其中 **`reason` 是「不可用时的中文原因」** —— 这一条决定了本查询的形状。
--
-- 🔴 为什么不能用 `orders.sql` 的 `GetCouponByCode`：那条把
--    「未开始 / 已过期 / 总次数用尽」三件事写进了 WHERE，返回 0 行。
--    0 行只能回答「不可用」，回答不了「为什么」，而 `reason` 是契约里的字段。
--    把判定从 WHERE 挪到 SELECT，是为了让每一种不可用**各自有一个可命名的布尔位**，
--    handler 只做「布尔位 → 中文文案」的映射，不再重新推理规则。
--    （核销走 `orders.sql` 的 `IncrementCouponUse`，它的 WHERE 里带 total_uses 的 CAS，
--     那里返回 0 行的语义是「被并发抢完了」，与本查询不是一回事。）
--
-- 参数：code 必填；plan_id / period 可空（`CouponVerifyRequest` 里这两个是 optional）。
-- 缺参时**不能把「没法判」当成「判过了」**：`*_out_of_scope` 只在参数存在且真的不在范围内时为 true，
-- 另有 `*_scope_unchecked` 显式标出「这张券有范围限制，但你没告诉我买什么，所以没校验」。
-- 少了这一对标志位，前端会在套餐页拿一个「valid: true」的答复，到下单时才被 422 打回来 ——
-- 而 user-journey 把「校验说可以、下单说不行」列为最伤信任的一类反馈。
--
-- 单位随 type 变（0006 的列注释）：type='percentage' → value 是**基点 bps**（1000 = 10%）；
-- type='fixed_amount' → value 是**分**。契约的 `CouponVerifyResult.type` 枚举写的是
-- `[fixed, percent]`，与 DB 的 `('percentage','fixed_amount')` **拼写不同** ——
-- 映射只此一处：'percentage' → "percent"，'fixed_amount' → "fixed"。
-- 折扣额一律在 Go 侧用整数算（percentage: floor(gross × value / 10000)），绝不引入 float。
--
-- min_amount 与 uses_per_user 的判定同样交给调用方：前者要等 amount_gross 算出来才有意义，
-- 后者要拿 user_used_count 与 c.uses_per_user 比 —— 两个数都在返回行里。
--
-- 走 coupons_code_uk (upper(code))，两侧必须同形 upper()，否则退化成全表扫。
-- name: VerifyCouponForUser :one
SELECT
  c.id, c.code, c.name, c.type, c.value, c.min_amount,
  c.scope_plan_ids, c.scope_periods,
  c.total_uses, c.used_count, c.uses_per_user, c.first_order_only,
  c.starts_at, c.ends_at,

  (c.starts_at IS NOT NULL AND c.starts_at >  now())::boolean AS not_started,
  (c.ends_at   IS NOT NULL AND c.ends_at   <= now())::boolean AS ended,
  (c.total_uses IS NOT NULL AND c.used_count >= c.total_uses)::boolean AS exhausted,

  -- scope_plan_ids / scope_periods 为空数组 = 不限（0006 的列注释）。
  -- cardinality() 而不是 array_length()：后者对空数组返回 **NULL** 不是 0，
  -- 于是 `array_length(x,1) > 0` 是 NULL，整个 AND 塌成 NULL，布尔位变成三态 —— 静默错判。
  (cardinality(c.scope_plan_ids) > 0
     AND sqlc.narg(plan_id)::bigint IS NOT NULL
     AND NOT (sqlc.narg(plan_id)::bigint = ANY (c.scope_plan_ids)))::boolean AS plan_out_of_scope,
  (cardinality(c.scope_periods) > 0
     AND sqlc.narg(period)::order_period IS NOT NULL
     AND NOT (sqlc.narg(period)::order_period = ANY (c.scope_periods)))::boolean AS period_out_of_scope,
  (cardinality(c.scope_plan_ids) > 0 AND sqlc.narg(plan_id)::bigint IS NULL)::boolean AS plan_scope_unchecked,
  (cardinality(c.scope_periods) > 0 AND sqlc.narg(period)::order_period IS NULL)::boolean AS period_scope_unchecked,

  -- 本人已用次数（对 c.uses_per_user）。口径与 orders.sql 的 CountUserCouponUses 逐字一致：
  -- 排除 cancelled / expired / failed —— 一张下单后被取消的订单不该吃掉用户的使用次数，
  -- 否则「点错了取消重下」会让新人券凭空消失一次。
  (SELECT count(*) FROM orders o
    WHERE o.user_id = sqlc.arg(user_id)::bigint
      AND o.coupon_id = c.id
      AND o.status NOT IN ('cancelled','expired','failed'))::bigint AS user_used_count,

  -- first_order_only 的判据：这个人有没有过**已付款**的订单。
  -- 用 paid/completed 而不是「有没有订单」：一张 pending 的订单只是点了一下按钮，
  -- 拿它取消新人资格，等于让用户因为犹豫而失去优惠。
  (SELECT count(*) FROM orders o2
    WHERE o2.user_id = sqlc.arg(user_id)::bigint
      AND o2.status IN ('paid','completed'))::bigint AS user_settled_order_count

FROM coupons c
WHERE upper(c.code) = upper(sqlc.arg(code)::text);
