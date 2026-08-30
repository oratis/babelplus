-- orders_user.sql · 用户面的订单与收款（下单 / 取消 / 收银台 / 链上归属）
--
-- 事实源：openapi/openapi.yaml 的 `createOrder` `getOrder` `listOrders` `cancelOrder`
--         `payOrder` `getOrderPayment` `recheckOrderPayment` `handlePaymentNotify`；
--         ADR 0012（收款：一单一址、永不复用、归属只看地址）、
--         ADR 0013（计费：服务区间 covers_from/covers_to、窗口链 prev_order_id、升级折抵）；
--         列名逐列核自 0006_orders / 0007_ledger / 0014_payments / 0015_payment_fixes / 0016_billing_rules。
--
-- ⚠️ **本文件不重写 `orders.sql` 已有的东西。** 复用清单（下面的注释里逐条点名）：
--      CreateOrder（0016 之后的 19 列）· GetRefundBasis · InsertOrderTransition ·
--      RecordOrderPayment · IncrementCouponUse · CreateCommission · UpsertWalletBalance ·
--      ClaimIdempotencyKey / GetIdempotencyKey / CompleteIdempotencyKey · InsertWebhookEvent。
--    这里只写 `orders.sql` **没有**、或者它的版本**在用户面会出错**的那些。
--    「会出错」的三条各自写了理由，最要紧的是 `AttachPaymentAddress`：
--    它不写 `pay_amount_usdt6`（那一列是 0015 才加的），而少付判定**只读那一列**。
--
-- 🔴 量纲：`amount_*` 一律 bigint 人民币**分**；链上金额一律 bigint，单位 **1e-6 USDT**。
--    `orders.pay_amount_raw`（numeric(38,18)）是证据不是判据 —— 本文件的任何判定都不读它
--    （ADR 0012 §17.3）。它甚至没有出现在收银台那条查询的 SELECT 里，理由见那里。


-- ============================================================
-- 一、下单前要问数据库的四件事（createOrder）
-- ============================================================

-- 下单上下文：`orders.type` 的服务端推导（ADR 0013 §4.6）、余额抵扣上限、
-- 优惠码的首单判定、以及 C6 一次性返佣要用的邀请关系 —— 一次查询全部拿到。
-- 合成一条而不是四条，是因为它们必须是**同一个时刻**的快照：
-- 分四次读，中间用户的订阅正好过期，就会推导出 'renew' 却按 'new' 发权利。
--
-- 🔴 `subscription_active` 的表达式与 `users_available_idx` 的谓词**逐字同形**
--    （`coalesce(expired_at, 'infinity'::timestamptz) > now()`）。
--    不能写成 `expired_at IS NOT NULL AND expired_at > now()`：`0003_accounts.up.sql:21`
--    明写「expired_at NULL = 不限时套餐」，按后者写会把管理员开的不限时账号判成新用户，
--    于是给他推导出 'new'、再卖他一次首单价 —— 而他手上那份订阅还在跑。
--    （ADR 0013 §4.6 的伪码写的是 `users.expired_at <= now()`，NULL 参与比较得 NULL、
--     整条 ELSIF 为假，落到下一档；两种写法在这一点上结论一致，这里取索引同形的那种。）
--
-- settled_order_count 用 paid/completed 而不是 count(*)：一张点了按钮就没管的 pending 单
-- 不该让用户失去「首单」身份（新人券、Class A 冷静期退款都挂在这个身份上）。
--
-- commission_count 是 C6「一次性返佣」的闸：邀请返佣一个 invitee **一生只计提一次**
-- （commissions 的 UNIQUE(order_id) 只管住「一单一条」，管不住「一人一条」）。
-- 它同时是 A7 地板断言分子的输入之一，见下面的 GetOrderCommissionAccrual。
-- name: GetUserOrderContext :one
SELECT
  u.id, u.plan_id, u.group_id, u.expired_at, u.reset_at, u.subscription_anchor_at,
  u.transfer_enable_plan, u.transfer_enable_pack, u.pack_expire_at,
  u.device_limit, u.speed_limit_mbps,
  u.invited_by,
  inv.commission_rate_bps AS inviter_commission_rate_bps,
  cur.code               AS current_plan_code,
  cur.kind               AS current_plan_kind,
  cur.price_monthly      AS current_plan_price_monthly,
  cur.transfer_enable    AS current_plan_transfer_enable,
  coalesce(w.balance, 0)::bigint AS balance_cents,
  (u.plan_id IS NOT NULL
     AND coalesce(u.expired_at, 'infinity'::timestamptz) > now())::boolean AS subscription_active,
  (SELECT count(*) FROM orders o
    WHERE o.user_id = u.id AND o.status IN ('paid','completed'))::bigint AS settled_order_count,
  (SELECT count(*) FROM commissions c
    WHERE c.invitee_id = u.id)::bigint AS commission_count
FROM users u
LEFT JOIN wallet_balances w ON w.user_id = u.id
LEFT JOIN users inv         ON inv.id = u.invited_by
LEFT JOIN plans cur         ON cur.id = u.plan_id
WHERE u.id = sqlc.arg(user_id)::bigint AND u.deleted_at IS NULL;

-- 升级折抵的基数（ADR 0013 §2.2）：当前订阅窗口的**最后一笔已完成周期订单** = `source`。
-- 一次查询同时给出 V_source / D_total / D_left，因为这三个量必须来自同一行、同一个 now()。
--
--   V_source = amount_paid + amount_balance + surplus_amount
--   D_total  = ceil((covers_to − covers_from) / 1 day)，下限 1
--   D_left   = clamp(ceil((covers_to − now()) / 1 day), 0, D_total)
--
-- 🔴 V_source **必须**含 surplus_amount。只取 amount_paid 的话，链式升级会凭空吞掉用户的钱：
--    ADR 0013 §4.2 的算例里 D22 那一步的折抵会从正确的 1600 变成 800。
--    （退款的基数是另一个量 V_window，**不含** surplus_amount，由 orders.sql 的
--     GetRefundBasis 沿 prev_order_id 递归求和。两个基数不要混用。）
--
-- 🔴 `ORDER BY covers_from DESC NULLS LAST` 里的 NULLS LAST 不是装饰：
--    Postgres 的 DESC 默认 **NULLS FIRST**，而 ADR §2.2 给的伪码只写了 `ORDER BY covers_from DESC`。
--    一旦有一行 covers_from 为 NULL 的已完成周期单（0016 之前的历史单、或将来某条漏写这两列的
--    开通路径），它会永远排在最前，于是 d_total/d_left 全为 0、折抵额算成 0 ——
--    **失败形态是用户在结算页看到「当前套餐剩余价值 ¥0」，没有任何报错。**
--
-- covers_to IS NULL（不限时套餐 onetime）时 d_total / d_left 无定义，这里返回 0 并把
--    covers_to 原样交出去：调用方**必须看 covers_to 判 422**，不许把 0 当成「剩 0 天」接着算
--    （ADR 0013 §2.2 的裁决：P1 阶段不售不限时套餐，升级到/从它一律 422）。
--    不用 greatest(1, …) 兜 NULL 的原因是 **PG 的 greatest/least 会忽略 NULL**
--    （与 Oracle 相反）：`greatest(1, ceil(NULL))` 得 1 而不是 NULL —— 那正好把
--    「无定义」伪装成「1 天」，是本文件最不愿意留下的那种静默错误。
--
-- 走 orders_user_idx (user_id, created_at DESC) 定位用户；covers_from 的排序它盖不住，
--    仍是一次 sort。几十人量级下无意义，登记以免被后人当成索引全命中（ADR §2.2 同样登记过）。
-- name: GetSubscriptionSource :one
SELECT
  o.id, o.trade_no, o.type, o.status, o.plan_id, o.period,
  o.covers_from, o.covers_to, o.prev_order_id, o.price_monthly_at_order,
  o.amount_paid, o.amount_balance, o.surplus_amount,
  (o.amount_paid + o.amount_balance + o.surplus_amount)::bigint AS v_source,
  (CASE WHEN o.covers_from IS NULL OR o.covers_to IS NULL THEN 0
        ELSE greatest(1, ceil(extract(epoch FROM (o.covers_to - o.covers_from)) / 86400))
   END)::bigint AS d_total,
  (CASE WHEN o.covers_from IS NULL OR o.covers_to IS NULL THEN 0
        ELSE least(
               greatest(1, ceil(extract(epoch FROM (o.covers_to - o.covers_from)) / 86400)),
               greatest(0, ceil(extract(epoch FROM (o.covers_to - now())) / 86400)))
   END)::bigint AS d_left
FROM orders o
WHERE o.user_id = sqlc.arg(user_id)::bigint
  AND o.status = 'completed'
  AND o.type IN ('new','renew','upgrade')
ORDER BY o.covers_from DESC NULLS LAST, o.id DESC
LIMIT 1;

-- A7 地板断言的**分子里那个最容易被忘掉的项**：本单的返佣计提（分）。
--
-- 定价修订 §C6 把邀请返佣从「按订单金额 10%」改成「**一次性、按该用户首单档位的月付标价 10%**」
-- （¥7.20 / ¥15.90 / ¥35.80）。改口径的理由有两条，第二条才是结构性的：
--   ① 按订单金额算会把 4 格打穿 1.20× 地板（最差 1.1474×）；
--   ② 旧口径下返佣落在 `commissions` 表、**订单成交之后**，而地板断言在下单服务里 ——
--      硬规则被写在它要防的东西不会经过的地方。改成一次性定额后金额在**下单时**就已知，
--      才第一次有可能进断言的分子。
--
-- 断言全式（定价修订 A7，下单服务在写 `orders` 之前跑，不满足则拒绝创建订单）：
--
--     ((amount_due − accrual_cents) / 周期月数 n / FX) / 月度总成本(档位, 周期)  ≥  1.20
--
-- 这条查询只负责 `accrual_cents`。另外三个量**不在数据库里**，调用方自己备齐：
--   · 周期月数 n     ← order_period 的常量映射（monthly=1 / quarterly=3 / half_yearly=6 / yearly=12）
--   · FX             ← 下单时锁定的 CNY/USDT，与写进 orders.fx_usdt_per_cny 的是同一个数
--   · 月度总成本     ← 成本模型 `Q × 0.121 + f + s/n`（定价修订 §4.3 的三档表），Go 侧常量表
-- ⚠️ 别把成本模型落库：它是一份会随实测修订的假设，落库会让「改一个假设」变成一次 migration，
--    而每一次 migration 都会让人倾向于不改。
--
-- 计提条件三取一为假即 0：没有邀请人 / 这个 invitee 已经计提过 / 他已经有付过款的订单
-- （即本单不是首单，「首单档位」这个口径就无从谈起）。
-- rate_bps 取邀请人自己的 commission_rate_bps，NULL 时用系统默认（$default_rate_bps，
-- 第一阶段 1000 = 10%，来自 settings，不硬编码在 SQL 里）。
--
-- ⚠️ `floor(...)::bigint` 的两步都不能省：`::bigint` 作用在 numeric 上是**四舍五入**不是截断
--    （`1.5::bigint = 2`），少了外面的 floor，返佣会在半分处向上取整、进而让分子变小、
--    让一个恰好卡在地板上的格子被误判为破地板。这条与 GetRefundBasis 的写法保持一致。
-- name: GetOrderCommissionAccrual :one
SELECT
  u.invited_by,
  coalesce(inv.commission_rate_bps, sqlc.arg(default_rate_bps)::integer)::integer AS rate_bps,
  (CASE
     WHEN u.invited_by IS NULL THEN 0
     WHEN EXISTS (SELECT 1 FROM commissions c WHERE c.invitee_id = u.id) THEN 0
     WHEN EXISTS (SELECT 1 FROM orders o
                   WHERE o.user_id = u.id AND o.status IN ('paid','completed')) THEN 0
     ELSE floor(coalesce(p.price_monthly, 0)::numeric
                * coalesce(inv.commission_rate_bps, sqlc.arg(default_rate_bps)::integer)
                / 10000)
   END)::bigint AS accrual_cents
FROM users u
LEFT JOIN users inv ON inv.id = u.invited_by
JOIN plans p        ON p.id = sqlc.arg(plan_id)::bigint
WHERE u.id = sqlc.arg(user_id)::bigint AND u.deleted_at IS NULL;

-- 单号占用探测。`orders.trade_no` 有 UNIQUE，撞号本来就会被数据库拒绝 ——
-- 这条查询存在的理由不是防撞号，是让**生成器**能在插入之前重试，
-- 而不是把一次撞号变成一条 500（用户看到的是「下单失败」，重试还撞的概率却接近零）。
-- name: TradeNoExists :one
SELECT EXISTS (SELECT 1 FROM orders WHERE trade_no = sqlc.arg(trade_no)::text)::boolean AS taken;


-- ============================================================
-- 二、订单读（getOrder / listOrders）
-- ============================================================

-- getOrder。🔴 **必须同时按 trade_no 与 user_id 过滤。**
-- `orders.sql` 的 `GetOrderByTradeNo` 只按单号查，那是给内部逻辑与管理面用的；
-- 用户面直接用它 = 越权读单（trade_no 是对外可见的路径参数，猜到别人的单号就能看到别人的金额）。
-- 查不到时返回 0 行，handler 一律映射成 404 `RESOURCE_NOT_FOUND` ——
-- **不要**区分「不存在」与「不是你的」，那个区别本身就是一个可枚举单号的信息泄露面。
--
-- `Order` schema 的字段映射（单位全部是分）：
--   total_amount ← amount_gross   discount_amount ← amount_discount
--   surplus_amount ← surplus_amount   balance_amount ← amount_balance
--   payable_amount ← amount_due    rate_locked_at ← fx_locked_at
--
-- ⚠️ 契约的 `OrderStatus` 枚举是 6 个值且含 DB 里不存在的 `processing`，
--    缺 `paying` / `underpaid` / `paid`（ADR 0013 §4.7 登记的四处不一致之一）。
--    DB 的 order_status 有 14 个值。**以 DB 为准**（data-model §14.1），
--    handler 的序列化必须显式列出映射表，不能把 enum 直接 fmt 出去 ——
--    否则用户在收银台轮询时会拿到一个契约里没有的字符串，前端没有对应分支。
-- name: GetUserOrder :one
SELECT
  o.id, o.trade_no, o.user_id, o.type, o.status, o.plan_id, o.period, o.currency,
  o.amount_gross, o.amount_discount, o.surplus_amount, o.amount_balance,
  o.amount_due, o.amount_paid, o.amount_refunded,
  o.coupon_id, o.gateway, o.pay_chain, o.pay_address, o.pay_amount_usdt6,
  o.fx_usdt_per_cny, o.fx_locked_at,
  o.expires_at, o.address_watch_until, o.paid_at, o.completed_at, o.cancelled_at,
  o.covers_from, o.covers_to, o.prev_order_id, o.surplus_order_ids,
  o.created_at, o.updated_at,
  p.name AS plan_name
FROM orders o
LEFT JOIN plans p ON p.id = o.plan_id
WHERE o.trade_no = sqlc.arg(trade_no)::text
  AND o.user_id  = sqlc.arg(user_id)::bigint;

-- listOrders，游标分页（api-contract §2.4；**用户面不返 total**，所以这里没有配套的 COUNT ——
-- 想加 count 的人请先读 §2.4：`COUNT(*)` 在 db-f1-micro 上是实打实的开销，
-- 后台需要「共 N 条」，用户面不需要）。
--
-- 排序键 (created_at DESC, id DESC)，游标就是 api-contract §2.4 示例里的 `{"id":…,"at":"…"}`。
-- 行比较 `(created_at, id) < (cursor_at, cursor_id)` 在两列同向 DESC 时等价于「排在它后面」；
-- 用它而不是 `created_at < $x OR (created_at = $x AND id < $y)`，是因为同一毫秒创建的两张单
-- 在后一种写法里会漏行 —— 而「下单失败重试」恰恰会在同一毫秒产生两张单。
--
-- has_more 的算法：调用方传 limit+1，拿到 limit+1 行就说明还有下一页，
-- 把第 limit+1 行**丢掉**、用第 limit 行的 (created_at, id) 编游标。
-- 不要用「返回行数 == limit」判 has_more：正好整除时会多给一页空数据。
--
-- 走 orders_user_idx (user_id, created_at DESC)。id 那一位盖不住，量级下无所谓。
-- name: ListUserOrdersPage :many
SELECT
  o.id, o.trade_no, o.type, o.status, o.plan_id, o.period, o.currency,
  o.amount_gross, o.amount_discount, o.surplus_amount, o.amount_balance, o.amount_due,
  o.fx_locked_at, o.expires_at, o.paid_at, o.created_at,
  p.name AS plan_name
FROM orders o
LEFT JOIN plans p ON p.id = o.plan_id
WHERE o.user_id = sqlc.arg(user_id)::bigint
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR (o.created_at, o.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::bigint)
  )
ORDER BY o.created_at DESC, o.id DESC
LIMIT sqlc.arg(page_limit)::integer;


-- ============================================================
-- 三、状态迁移（cancelOrder / payOrder / 入账）
-- ============================================================

-- 🔴 唯一允许的状态迁移写法：**DB 层 CAS**（ADR 0012 §7.2 逐字要求）。
--    `WHERE id = $1 AND status = $2` 影响 0 行 = 并发冲突或非法迁移，调用方**必须**当作失败处理，
--    不得退化成 `UPDATE … WHERE id = $1`。
--
-- 为什么不用 `orders.sql` 的 `UpdateOrderStatus`：它的 WHERE 里只有 id，没有 from-status。
--    在收款路径上这一条差别是致命的：扫链与 recheck 会**并发**处理同一笔到账
--    （两条路径共用 ProcessDeposit，但触发时刻各自独立），无 CAS 的写法会让
--    `paying → paid` 执行两次 —— 而「开通」挂在这次迁移上，用户拿到两份权利、账上少一次收入。
--    那条查询留给单线程的内部任务用（超时扫描），本文件的四条路径一律走这一条。
--
-- 三个时间戳的 coalesce 与 `UpdateOrderStatus` 逐字一致：重复迁移不会把 paid_at 往后推 ——
-- 首次到账时刻是退款冷静期与佣金确认期的起算点，被覆盖一次就等于给用户多送一段冷静期。
--
-- ⚠️ 调用方必须在**同一事务**里写一条 `order_transitions`（复用 orders.sql 的 InsertOrderTransition）。
--    状态机没有触发器兜底，漏写审计不会报错 —— 这与 user_rev 不同：那里漏了是静默故障，
--    这里漏了是证据缺失，而拒付申诉与「我明明付了」的工单只能靠这张表回答。
-- name: TransitionOrderStatus :one
UPDATE orders SET
  status = sqlc.arg(to_status)::order_status,
  paid_at = CASE WHEN sqlc.arg(to_status)::order_status IN ('paid','completed')
                 THEN coalesce(paid_at, now()) ELSE paid_at END,
  completed_at = CASE WHEN sqlc.arg(to_status)::order_status = 'completed'
                 THEN coalesce(completed_at, now()) ELSE completed_at END,
  cancelled_at = CASE WHEN sqlc.arg(to_status)::order_status IN ('cancelled','expired')
                 THEN coalesce(cancelled_at, now()) ELSE cancelled_at END,
  updated_at = now()
WHERE id = sqlc.arg(order_id)::bigint
  AND status = sqlc.arg(from_status)::order_status
RETURNING id, trade_no, user_id, status, paid_at, completed_at, cancelled_at;

-- cancelOrder。契约：**仅 `pending` 可取消**，其余状态一律 409。
-- 用户面必须按 (trade_no, user_id) 定位，理由同 GetUserOrder（越权）。
-- 0 行有两种可能：不是你的单（→ 404）、状态不是 pending（→ 409 STATE_CONFLICT）。
-- 调用方分不出来，所以**先 GetUserOrder 判存在与归属，再调这条判状态** —— 两次读一次写，
-- 换来的是两个不同的错误码，而「取消失败」不给原因是 user-journey 点名的那类死胡同。
--
-- 一并把 CTE 里的 plan_name 取出来，是为了让 200 的响应体与 getOrder 同形而不用再查一次。
--
-- ⚠️ `pending` 状态下订单**还没有收款地址**（地址在 payOrder 才分配，ADR 0012 §5.1），
--    所以取消不需要、也**不允许**释放地址：`pay_addresses.assigned_order_id` 是一次性单调赋值，
--    「永不复用」就是靠它不回退来保证的（§5.2）。任何「取消时把地址还回池子」的想法都要
--    先读那一节：回收会让第 8 天到账的钱在数据上分不清属于哪张单。
-- name: CancelUserPendingOrder :one
WITH cancelled AS (
  UPDATE orders SET
    status = 'cancelled',
    cancelled_at = coalesce(cancelled_at, now()),
    updated_at = now()
  WHERE trade_no = sqlc.arg(trade_no)::text
    AND user_id  = sqlc.arg(user_id)::bigint
    AND status   = 'pending'
  RETURNING id, trade_no, user_id, type, status, plan_id, period, currency,
            amount_gross, amount_discount, surplus_amount, amount_balance, amount_due,
            fx_locked_at, expires_at, paid_at, cancelled_at, created_at
)
SELECT c.id, c.trade_no, c.user_id, c.type, c.status, c.plan_id, c.period, c.currency,
       c.amount_gross, c.amount_discount, c.surplus_amount, c.amount_balance, c.amount_due,
       c.fx_locked_at, c.expires_at, c.paid_at, c.cancelled_at, c.created_at,
       p.name AS plan_name
FROM cancelled c
LEFT JOIN plans p ON p.id = c.plan_id;


-- ============================================================
-- 四、payOrder：分配地址、锁汇率、落判据列
-- ============================================================

-- ADR 0012 §5.1 的分配算法，逐字落地。**这是 §5 的全部。**
--
-- `FOR UPDATE SKIP LOCKED` + `LIMIT 1`：两个并发的 payOrder 各自拿到不同的地址，
-- 而不是一个等另一个（等待会让收银台在库存充足时也变慢），更不是两个拿到同一个。
--
-- 无行返回 = 地址库存耗尽 → **503 `INTERNAL_DEPENDENCY_DOWN`**（契约在 payOrder 上定义了 503）。
-- 不要退化成「复用一个已分配的地址」：一址一单是归属的全部依据（§5.2）。
-- 剩余可用地址 < 8 时告警（settings 的 `addr_low_water`），派生是离线批量动作，来不及现派。
--
-- `assigned_order_id` 上的 UNIQUE 同时兜住第二件事：**同一张订单不可能拿到第二个地址**。
-- 所以 payOrder 重复调用（Idempotency-Key 之外的重试）会撞唯一约束而不是悄悄多占一个地址 ——
-- 调用方应当先读订单的 pay_address，非空就直接返回既有收银台数据。
--
-- 过滤 `is_blacklisted`：那是 Tether `isBlackListed(<我方地址>)` 每日巡检的缓存（0014 的列注释）。
-- 往一个被 Tether 冻结的地址上收钱，钱会到账但取不出来 —— 这一条过滤是零成本的保险。
-- name: AssignPayAddressToOrder :one
WITH picked AS (
  SELECT a.id
  FROM pay_addresses a
  WHERE a.chain = sqlc.arg(chain)::text
    AND a.enabled = true
    AND a.is_blacklisted = false
    AND a.assigned_order_id IS NULL
  ORDER BY a.id
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
UPDATE pay_addresses p
SET assigned_order_id = sqlc.arg(order_id)::bigint
FROM picked
WHERE p.id = picked.id
RETURNING p.id, p.chain, p.address, p.derivation_index, p.cursor_ts, p.last_scanned_at;

-- 库存水位：`addr_low_water`（默认 8）的观测点。派生一次 32 个约够 9 个月，
-- 而派生是**离线**动作（私钥不在服务器上，0014 那张表里永远不会有 private_key 列），
-- 所以这个数字必须提前很久看到，不能等收银台报 503 才知道。
-- name: CountAvailablePayAddresses :one
SELECT count(*)::bigint AS available
FROM pay_addresses
WHERE chain = sqlc.arg(chain)::text
  AND enabled = true AND is_blacklisted = false AND assigned_order_id IS NULL;

-- 把收款参数落到订单上，并 CAS 迁移 `pending → paying`。
--
-- 🔴 **不要用 `orders.sql` 的 `AttachPaymentAddress`。** 那条查询写于 0015 之前，
--    它落 `pay_amount_raw` 但**不落 `pay_amount_usdt6`** —— 而 0015 §17.3 把判据整体搬到了
--    后面那一列（「paid / underpaid / 写销的判定**只读这一列**」，见该列的 COMMENT）。
--    用它的后果：`pay_amount_usdt6` 恒为 NULL，于是 `shortfall = NULL - received` 也是 NULL，
--    三档规则（写销 / 人工 / 提示补足）全部落空，订单永远停在 paying。
--    **sqlc 与 go build 都不会说一个字**，因为那条查询本身完全合法。
--
-- 报价口径（ADR 0012 §5.3，调用方算好再传进来，SQL 不做浮点）：
--     amount_usdt6 = ceil(amount_due_cents × 1e6 × (1 + fx_buffer) / (cny_per_usdt_e4 × 100))
--     amount_usdt6 = ceil(amount_usdt6 / 10000) × 10000        -- 取整到 0.01 USDT
--   一律 ceil：舍入误差落在我们这边比落在用户那边更容易解释（最大多收 ≈ ¥0.071）。
--   **尾数不再承载识别功能** —— 金额尾数匹配那套机制随 ADR 0012 一起被删掉了（§5.4），
--   归属只看地址。契约 payOrder 描述里那三条「金额末位是订单识别码」的硬约束是被推翻的原文（§3.6）。
--
-- `fx_usdt_per_cny` 这一列名与它承载的值**方向相反**：0006 起的列名是 usdt/cny，
--   而 ADR 0012 §5.3 与契约字段 `cny_per_usdt_e4` 都是「1 USDT 折多少 CNY」（≈7.15）。
--   这里落的是**后者**（与报价公式同一个数），否则报价会差 51 倍。列名是 0006 留下的错误，
--   改名要动生成物与既有查询，不在本轮范围 —— 登记在此，读这一列的人先看这段。
--
-- `address_watch_until` = expires_at + 7 天（settings 可配，ADR 0012 §11.1）。
--   但那只是**自动扫描的时长，不是认账的时长**：一址一单永不复用之下，第 8 天、第 800 天
--   到账的钱归属仍然唯一确定，超窗到账由每日链上余额对账兜住（≤24h 发现）。
--   契约 handlePaymentNotify 描述里的「继续监听 ≥ 24 小时」是这条的下限，不是上限。
-- name: AttachOrderPaymentQuote :one
UPDATE orders SET
  gateway             = sqlc.arg(gateway)::text,
  pay_chain           = sqlc.arg(pay_chain)::text,
  pay_address         = sqlc.arg(pay_address)::text,
  pay_amount_usdt6    = sqlc.arg(pay_amount_usdt6)::bigint,
  pay_amount_raw      = sqlc.arg(pay_amount_raw)::numeric,
  fx_usdt_per_cny     = sqlc.arg(cny_per_usdt)::numeric,
  fx_locked_at        = now(),
  address_watch_until = sqlc.arg(address_watch_until)::timestamptz,
  status              = 'paying',
  updated_at          = now()
WHERE id = sqlc.arg(order_id)::bigint
  AND status = 'pending'
RETURNING id, trade_no, user_id, status, gateway, pay_chain, pay_address,
          pay_amount_usdt6, fx_usdt_per_cny, fx_locked_at, expires_at, address_watch_until;

-- `PayOrderRequest.method = 'balance'` 的扣款。
--
-- `wallet_balances.balance` 上有 `CHECK (balance >= 0)`，所以扣穿会被**数据库**拒绝；
-- 但那是一次约束违反（500 级的错误），不是一个可以翻译成 422 的答复。
-- 这里把「够不够」写进 WHERE：影响 0 行 = 余额不足或压根没有钱包行 → 422，一次往返。
--
-- ⚠️ 余额的**唯一真相是分录**（`user_wallet_balance` 视图），本表只是读路径的缓存
--    （data-model §7.1）。所以这条 UPDATE 必须与它对应的 ledger_entries / ledger_lines
--    写在同一个事务里（复用 orders.sql 的 CreateLedgerEntry / CreateLedgerLine），
--    并把该分录 id 落进 last_entry_id 供增量对账。只扣缓存不写分录，
--    每日 `ReconcileWalletBalances` 会报红，而那时钱已经花出去了。
--
-- ⚠️ 余额**只可消费不可提现**（product-brief §6）。这一条数据库强制不了 ——
--    它靠的是 `ledger_accounts` 里不存在 `asset:bank ← liability:user_wallet` 这条路径，
--    以及没有人写提现代码。**在一条只能靠人守的约束上，新增任何出金路径前先读 ADR 0013 §4.3 边界 1。**
-- name: SpendWalletBalance :one
UPDATE wallet_balances SET
  balance       = balance - sqlc.arg(amount_cents)::bigint,
  last_entry_id = sqlc.arg(ledger_entry_id)::bigint,
  updated_at    = now()
WHERE user_id = sqlc.arg(user_id)::bigint
  AND balance >= sqlc.arg(amount_cents)::bigint
RETURNING user_id, currency, balance, last_entry_id;


-- ============================================================
-- 五、收银台（getOrderPayment / recheckOrderPayment / payOrder 的响应体）
-- ============================================================

-- `PaymentCheckout` 的全部数据源。三个 operation 共用这一条：
-- payOrder 写完之后读一次、getOrderPayment 轮询读、recheckOrderPayment 扫完链之后再读。
-- **共用是硬要求**：三处各写一份「状态怎么算」的逻辑，漂移的那天就是用户看到
-- 「已支付」但订阅没开通的那天。
--
-- 契约字段 → 本行的映射，以及必须在 Go 侧完成的两件事：
--   trade_no               ← trade_no
--   chain / address        ← pay_chain（'tron' → 契约枚举 "TRC20"）/ pay_address
--   amount_usdt6           ← pay_amount_usdt6
--   amount_display         ← **Go 侧**由 amount_usdt6 格式化成两位小数字符串（§5.3）。
--                            契约明写它是**字符串不是数值** —— 不给浮点留口子，所以 SQL 不产出它。
--   cny_per_usdt_e4        ← **Go 侧**由 fx_usdt_per_cny × 1e4 得到（见下面为什么不在 SQL 里乘）
--   quote_expires_at       ← expires_at
--   confirmations_required ← settings（**必须可配置下发，不能硬编码在前端**，契约硬约束 3）
--   received_usdt6         ← received_usdt6
--   shortfall_usdt6        ← shortfall_usdt6（仅 state = underpaid 时下发）
--   state                  ← 见下面的映射表
--   note                   ← 文案，Go 侧常量（ADR 0012 §6.4 的提币手续费说明，
--                            **不是**契约描述里那段解释四位小数尾数的话 —— 尾数机制已被 §5.4 删除）
--
-- ⚠️ 为什么不在 SQL 里算 `fx_usdt_per_cny * 10000`：加了 `::bigint` 之后 sqlc 会把该列判成
--    NOT NULL，而这一列在订单走到 payOrder 之前**恒为 NULL**（下单只锁价不锁址）。
--    于是「用户刚下单就打开收银台」这条最普通的路径会变成一次运行时 scan 失败。
--    同理，本查询里所有可能为 NULL 的量都以**原始列**交出去，只有能 coalesce 到确定值的
--    聚合量才带 cast。这条规则在本仓库有先例：GetRefundBasis 的注释记录了反向的坑
--    （不写 cast 会退化成 interface{}）—— 两边都要看，判断依据是「这个值可能为 NULL 吗」。
--
-- ⚠️ 本 SELECT **刻意不含 `pay_amount_raw`**。它是 numeric(38,18)，类型本身容得下链上不可能出现、
--    且互不相等的值（ADR 0012 §17.3）；把它摆在 `pay_amount_usdt6` 旁边的同一个结构体里，
--    就是在邀请下一个人拿它做判定。判据只有一个，那就只给一个。
--
-- `state` 的映射（契约 PaymentState = waiting|confirming|underpaid|paid|expired）：
--   order.status='pending'                                → waiting（还没发起支付，无址无价）
--   'paying' 且 received_usdt6 = 0                        → waiting
--   'paying' 且 confirming_count > 0                      → confirming（有到账但未固化）
--   'paying' 且 received > 0 且 confirming_count = 0      → confirming（等下一轮扫描定档）
--   'underpaid'                                           → underpaid（带 shortfall_usdt6）
--   'paid' / 'completed'                                  → paid
--   'expired' / 'cancelled'                               → expired
-- 🔴 `PAYMENT_UNDERPAID` **不是错误**，是订单状态 —— 它走 200 而不是错误通道（契约原文）。
--
-- 累计口径逐字来自 ADR 0012 §6.3：`SUM(payments.amount_usdt6 WHERE to_address = order.pay_address)`，
-- **按地址聚合而不是按 order_id**。差别在补足场景：一笔迟到的、扫描还没来得及归属到订单的到账，
-- 按地址已经能算进来，按 order_id 会漏 —— 而用户此刻正盯着「还差 Y」那个数字。
-- 排除 aml_verdict='blacklisted' 的行：那种钱不入账（§8.4），也就不该出现在「已收到」里。
-- `coalesce(aml_verdict,'clean')` 而不是 `aml_verdict <> 'blacklisted'`：后者对 NULL 得 NULL，
-- 会把**尚未做 AML 判定**的正常到账整行滤掉，用户的钱在页面上凭空消失。
--
-- LEFT JOIN LATERAL 而不是三个标量子查询：聚合只扫一次 payments_addr_idx (to_address, received_at DESC)，
-- 且三个数来自同一次扫描 —— 分开写会让 received 与 shortfall 在并发入账时对不上。
-- name: GetOrderCheckout :one
SELECT
  o.id, o.trade_no, o.user_id, o.status,
  o.gateway, o.pay_chain, o.pay_address, o.pay_amount_usdt6,
  o.fx_usdt_per_cny, o.fx_locked_at,
  o.amount_due, o.amount_paid,
  o.expires_at, o.address_watch_until, o.paid_at,
  agg.received_usdt6,
  agg.payment_count,
  agg.confirming_count,
  agg.min_confirmations,
  greatest(0, coalesce(o.pay_amount_usdt6, 0) - agg.received_usdt6)::bigint AS shortfall_usdt6
FROM orders o
LEFT JOIN LATERAL (
  SELECT
    coalesce(sum(pm.amount_usdt6), 0)::bigint                          AS received_usdt6,
    count(*)::bigint                                                   AS payment_count,
    count(*) FILTER (WHERE pm.state = 'confirming')::bigint            AS confirming_count,
    coalesce(min(pm.confirmations), 0)::integer                        AS min_confirmations
  FROM payments pm
  WHERE pm.to_address = o.pay_address
    AND coalesce(pm.aml_verdict, 'clean') <> 'blacklisted'
) agg ON true
WHERE o.trade_no = sqlc.arg(trade_no)::text
  AND o.user_id  = sqlc.arg(user_id)::bigint;

-- recheckOrderPayment 的**冷却闸**（ADR 0012 §10.4）。
--
-- 契约给 recheck 定义了 429，但那一节的裁决是：**不要用它。**
-- 原文的判据是 monitoring §3.2「我们的规模下任何 429 都是异常」，而「每订单 6 次/小时」
-- 会在一个完全正常的场景里触发：一个刚转完账、盯着页面的新用户每 30 秒点一次，5 分钟就点满。
-- **给一个害怕的人回 429，是这个按钮所有可能行为里最差的一种。**
-- 裁决：20 秒冷却窗口内的重复 recheck **直接返回上一次扫描的结果**（200 + PaymentCheckout），
-- 只有跨过窗口才真的打 TronGrid。外部配额一样受保护，用户永远拿到 200。
--
-- 冷却做成一次 CAS 而不是先读后判：两个并发 recheck 在「先 SELECT last_scanned_at、
-- 再决定要不要扫」的写法下会**双双通过**，于是两次外部调用 —— 而外部配额正是要保护的东西。
-- 影响 1 行 = 拿到扫描权；0 行 = 还在冷却窗口内，直接回上一次的 GetOrderCheckout 结果。
--
-- 按 `assigned_order_id` 定位而不是按 address：那一列是 UNIQUE，一订单一行，
-- 天然不会误锁到别人的地址；也顺带表达了「订单还没分配地址时 recheck 无事可做」（0 行）。
--
-- 20 秒是拍的，须按 TronGrid 实际额度调整，所以走参数不写死（ADR 0012 §10.4 登记为未决项）。
-- name: TryClaimAddressScan :one
UPDATE pay_addresses SET
  last_scanned_at = now()
WHERE assigned_order_id = sqlc.arg(order_id)::bigint
  AND (last_scanned_at IS NULL
       OR last_scanned_at < now() - sqlc.arg(cooldown)::interval)
RETURNING id, chain, address, cursor_ts, last_scanned_at;

-- 扫描游标推进。`cursor_ts` 存**毫秒整数**而不是 timestamptz，因为它是 TronGrid 的分页游标，
-- 要原样回传；转成 timestamptz 再转回去会在毫秒边界上丢事件（0014 的列注释）。
-- 推进时按 ADR 0012 §10.5 往回退 10 分钟重扫（幂等索引兜底），防止边界漏读 ——
-- 回退量由调用方在传值前扣掉，SQL 不替它算，免得两处各有一份「回退多少」的常量。
-- name: UpdatePayAddressCursor :exec
UPDATE pay_addresses SET
  cursor_ts       = sqlc.arg(cursor_ts)::bigint,
  last_scanned_at = now()
WHERE id = sqlc.arg(pay_address_id)::bigint;

-- 支付相关的运行时配置。ADR 0012 §9.2 把它们放在 `settings` 的 JSONB 里
-- （key = 'payment.providers'，内含 confirm_policy / writeoff_usdt6 / review_usdt6 / addr_low_water），
-- 改配置走 D13（二次确认 + 展示 diff + 审计），**不重新部署**。
--
-- 为什么不硬编码：`confirmations_required` 是契约点名要求「必须可配置下发，不能硬编码在前端」的字段；
-- 而两个少付阈值直接决定我们**放弃多少钱**（写销档上界 2.0 USDT/单），
-- 这种数字写在二进制里意味着每次调参都要一次发布，于是没人调，于是阈值永远是拍脑袋那个值。
--
-- ⚠️ TRON 的最终性是「固化」不是「N 个确认」：服务端的实际判据是固化标志，
--    下发的 `confirmations_required`（19）**只用于前端展示进度**（ADR 0012 §10.5）。
--    把它当成判据写进入账逻辑是错的。
-- name: GetPaymentSettings :many
SELECT key, value, updated_at
FROM settings
WHERE key = ANY (sqlc.arg(keys)::text[]);


-- ============================================================
-- 六、链上归属与入账（handlePaymentNotify / chain-scan / recheck / D6 共用）
-- ============================================================
--
-- 🔴 四条路径**必须调用同一个 `ProcessDeposit`**（ADR 0012 §8.4 硬约束 1）。
--    不同的触发源，同一段代码。两条路径一旦漂移，漂移的那天就是出事的那天。
--    下面这组查询就是那一段代码的全部数据面，顺序即 §8.4 的五个分支。

-- 分支 0：入账幂等锁。**这是全系统唯一的入账锁。**
--
-- `ON CONFLICT (provider, external_id) DO NOTHING` 返回 0 行 = 这笔钱已经入过账，
-- 走 §8.4 分支 ①，**不重复入账、不重复开通**，对外静默返回 200。
--
-- 🔴 幂等靠**数据库唯一索引**，不靠应用层的 `SELECT … IF NOT EXISTS`：后者在两个 Cloud Run 实例
--    并发处理同一次重投时会**双双通过**，结果是同一笔钱入账两次、开通两次。
--    `--max-instances=8` 之下这不是小概率。契约在 handlePaymentNotify 的描述里逐字写了这一条。
--
-- 🔴 `external_id` 的取值来源**只有链上事件**（`txid || ':' || log_index`），与录入者无关。
--    被推翻过的写法是 `'D6:' || audit_logs.id` —— 它根本不幂等：D6 点两次 = 两条 audit_logs
--    = 两个 external_id = 两次入账；且它与扫链跨 provider 不去重，同一笔钱可以既被手工记成
--    ('manual','D6:123')、又被扫描记成 ('chain_tron','abc…:0')。
--    「先手工上线、后补自动化」是计划内的必经状态，所以这不是理论场景（§8.2）。
--
-- `entered_by` 只区分**录入者**（'scanner' / 'admin:<id>'），**不参与幂等** ——
--    手工与自动因此天然互斥：谁先到谁插入成功，后到的撞唯一索引走「已入账」分支。
--
-- `raw` 是 NOT NULL 且必须是原始 event / payload 原文：入账争议（用户说打了、我们说没收到）
--    只能靠原文解决，缺一条就等于这条流水不可复核。
-- name: InsertPaymentIfNew :one
INSERT INTO payments (
  provider, external_id, entered_by,
  order_id, user_id,
  chain, txid, log_index, from_address, to_address,
  amount_usdt6, amount_cny_cents,
  state, confirmations,
  aml_checked_at, aml_verdict, ledger_entry_id, raw
) VALUES (
  $1,$2,$3,
  $4,$5,
  $6,$7,$8,$9,$10,
  $11,$12,
  $13,$14,
  $15,$16,$17,$18
)
ON CONFLICT (provider, external_id) DO NOTHING
RETURNING *;

-- 分支 ①：读既有行，判断要不要写 §16.2 的冲正分录。
-- name: GetPaymentByExternalID :one
SELECT * FROM payments
WHERE provider = sqlc.arg(provider)::text
  AND external_id = sqlc.arg(external_id)::text;

-- 分支 ① 的后半：手工录入（D6）之后扫描又扫到同一笔钱。
-- 把 entered_by 追加成 'admin:<id>+scanner'，并由调用方在**同一事务**里写冲正分录
-- （Dr asset:crypto:tron:pool / Cr asset:manual_reconcile，ADR 0012 §16.2）。
--
-- 这条为什么重要：`asset:manual_reconcile` 的余额长期非零 = 有人标了「已支付」但钱没进来。
-- 它把「全系统最大的内部欺诈面」（page-inventory 对 D6 的定性）变成一个可以每天看一眼的数字。
-- 漏了这次冲正，那个数字会永久挂账、天天报红，于是没人再看它 —— 一个天天报红的告警等于没有告警。
--
-- WHERE 里的两个 LIKE 让这条 UPDATE 幂等：只对「手工录入过、且还没被扫描追加过」的行生效，
-- 影响 0 行 = 不需要冲正（本来就是 scanner 录的，或已经追加过）。
-- name: AppendScannerToPaymentEntry :one
UPDATE payments SET
  entered_by = entered_by || '+scanner'
WHERE provider = sqlc.arg(provider)::text
  AND external_id = sqlc.arg(external_id)::text
  AND entered_by LIKE 'admin:%'
  AND entered_by NOT LIKE '%+scanner'
RETURNING id, provider, external_id, entered_by, order_id, user_id, amount_usdt6, amount_cny_cents;

-- 归属：**只看地址，一次确定的查表。**（ADR 0012 §5 / §17.2）
--
-- `orders_pay_addr_uk`（0015 建的部分唯一索引，条件是 pay_address IS NOT NULL、**不限状态**）
-- 保证这条查询至多一行。不限状态是刻意的：地址与订单的绑定在订单终结之后依然成立，
-- 否则过期订单的地址会被重新分配，那笔迟到的钱就归属不到人 —— 而「钱进黑洞」是
-- user-journey 判定为最不可挽回的一类失败。
--
-- `FOR UPDATE`：入账要改订单状态与累计额，必须先把这一行锁住。
-- 并发的两笔到账（补足场景）会在这里排队，而不是各自算一遍 received 然后都判成 paid。
--
-- 0 行 = 这个地址不是任何订单的收款地址 → §8.4 分支 ②：order_id 保持 NULL、
-- aml_verdict='quarantined'、进人工队列。**钱照收，只是暂时找不到人。**
-- name: GetOrderByPayAddressForUpdate :one
SELECT
  o.id, o.trade_no, o.user_id, o.status, o.plan_id, o.period, o.currency,
  o.amount_gross, o.amount_discount, o.surplus_amount, o.amount_balance,
  o.amount_due, o.amount_paid,
  o.pay_chain, o.pay_address, o.pay_amount_usdt6, o.fx_usdt_per_cny, o.fx_locked_at,
  o.expires_at, o.address_watch_until, o.paid_at,
  o.covers_from, o.covers_to, o.prev_order_id, o.price_monthly_at_order,
  o.coupon_id, o.invited_by
FROM orders o
WHERE o.pay_address = sqlc.arg(pay_address)::text
FOR UPDATE;

-- 「这是不是我们的地址」+ Tether 黑名单缓存。
-- 分支 ② 要靠它区分两种「找不到订单」：
--   本表有这个地址 → 是我们的地址、只是订单侧对不上 → 进人工队列，必须有人看
--   本表没有       → 根本不是打给我们的（回调伪造 / 网关串号）→ 不入账，按告警处理
-- 后一种正是易支付回调伪造漏洞（NewAPI 的真实案例）会走到的地方，
-- 而契约在 handlePaymentNotify 上写死了「**收到回调后必须反向查单**，以链上的权威金额为准」。
-- name: GetPayAddressByAddress :one
SELECT
  a.id, a.chain, a.address, a.derivation_index, a.assigned_order_id,
  a.enabled, a.is_blacklisted, a.blacklist_checked_at,
  a.last_scanned_at, a.cursor_ts
FROM pay_addresses a
WHERE a.chain = sqlc.arg(chain)::text
  AND a.address = sqlc.arg(address)::text;

-- 把一笔已插入的流水归属到订单/用户，并落 AML 判定与分录 id。
-- 分成两步（先 InsertPaymentIfNew 抢幂等锁、再归属）而不是一条 INSERT 写全，
-- 是因为**幂等锁必须在归属之前拿到**：归属要读 orders 并加锁，那是一段可能失败、可能重试的逻辑，
-- 把它放在唯一索引之前，等于让两个并发实例都走完归属再撞锁 —— 撞得晚不如撞得早。
--
-- ⚠️ `aml_verdict = 'unbound_source'` 是**记录但仍然入账**的档（ADR 0012 §12.2）：
--    来源不明不等于赃款，在这个规模上因来源不明而不给用户开通，是把误伤成本转嫁给守法用户。
--    只有 'blacklisted' 才不入账。
-- name: AttributePayment :one
UPDATE payments SET
  order_id         = sqlc.narg(order_id)::bigint,
  user_id          = sqlc.narg(user_id)::bigint,
  amount_cny_cents = sqlc.narg(amount_cny_cents)::bigint,
  state            = sqlc.arg(state)::payment_state,
  confirmations    = sqlc.arg(confirmations)::integer,
  aml_checked_at   = now(),
  aml_verdict      = sqlc.narg(aml_verdict)::text,
  ledger_entry_id  = sqlc.narg(ledger_entry_id)::bigint
WHERE id = sqlc.arg(payment_id)::bigint
RETURNING *;

-- 累计口径（ADR 0012 §6.3）：**打到该地址的任何一笔钱都累加进这张订单。**
-- 每次入账后按这个数重新评估三档规则：
--   shortfall = pay_amount_usdt6 − received_usdt6（均为 1e-6 USDT 整数，判定只在整数域做）
--   A 档 shortfall ≤ writeoff_usdt6（默认 2,000,000）→ 直接 paying→paid，差额记 expense:payment_shortfall
--   B 档 writeoff < shortfall ≤ review_usdt6（默认 5,000,000）→ paying→underpaid，进人工队列，
--        页面文案明写「我们正在人工处理，**无需再次转账**」
--   C 档 shortfall > review_usdt6 → paying→underpaid，提示向**同一地址**补足，
--        并提醒「提币手续费从你填的金额里扣，请填 Y + 手续费」
-- A 档存在的理由是一条结构性的事实：要求用户补足 1.5 USDT 是走不通的 ——
-- 补足会被再扣一次同样的提币费，净到账 0。**我们不去要一笔要不来的钱。**
--
-- 与 GetOrderCheckout 里那个 LATERAL 是同一个口径，两处必须一起改。
-- 单独留一条是因为入账路径拿不到 trade_no + user_id（它只有地址）。
-- name: SumAddressReceipts :one
SELECT
  coalesce(sum(pm.amount_usdt6), 0)::bigint               AS received_usdt6,
  count(*)::bigint                                        AS payment_count,
  count(*) FILTER (WHERE pm.state = 'confirming')::bigint AS confirming_count,
  coalesce(min(pm.confirmations), 0)::integer             AS min_confirmations
FROM payments pm
WHERE pm.to_address = sqlc.arg(pay_address)::text
  AND coalesce(pm.aml_verdict, 'clean') <> 'blacklisted';

-- 一张订单的完整收款历史。🔴 **只能从这里读，不能从 `orders` 读**（ADR 0012 §8.3）：
-- `orders.gateway_ref` 已降级为「首笔到账 txid，仅供人工检索，**不承担幂等**」，
-- 而 underpaid 补足场景必然是一张订单对应两笔链上转账 —— 那一列结构上装不下。
-- 走 payments_order_idx。
-- name: ListOrderPayments :many
SELECT
  pm.id, pm.provider, pm.external_id, pm.entered_by,
  pm.chain, pm.txid, pm.log_index, pm.from_address, pm.to_address,
  pm.amount_usdt6, pm.amount_cny_cents, pm.state, pm.confirmations,
  pm.aml_checked_at, pm.aml_verdict, pm.ledger_entry_id, pm.received_at
FROM payments pm
WHERE pm.order_id = sqlc.arg(order_id)::bigint
ORDER BY pm.received_at, pm.id;

-- 分支 ④/⑤ 的支撑：过期单迟到的钱、以及超额的钱，都要回填付款方地址。
-- `orders.pay_from_address` 是 0016 加的列，ADR 0013 §9 的失效条件（归集时按 txid 回填）靠它执行。
-- 只写一次（coalesce 保留首次值）：一张订单可能有多笔到账，而「谁付的钱」以第一笔为准 ——
-- 后面的补足如果来自另一个地址，那是需要人看一眼的异常，不该被静默覆盖掉。
--
-- ⚠️ 分支 ④（订单已 expired）**不改订单状态、不回改成 paid**：
--    `paid → completed` 是唯一的权益发放路径，把过期单改回 paid 等于用一个已经过期的汇率开通，
--    汇率敞口由我们承担（ADR 0012 §7.3）。钱按**到账时刻**重新取汇率折算，
--    走 orders.sql 的 UpsertWalletBalance 入余额，并发一封「已入账为余额，可用于重新下单」的邮件。
--    契约在 handlePaymentNotify 描述里逐字写了这条：「期间到账的资金**入账为余额，不直接开通订阅**」。
--    **不做这一条，用户第一次付款的钱就真的进黑洞。**
-- name: RecordOrderPayerAddress :exec
UPDATE orders SET
  pay_from_address = coalesce(pay_from_address, sqlc.arg(pay_from_address)::text),
  gateway_ref      = coalesce(gateway_ref, sqlc.narg(gateway_ref)::text),
  updated_at       = now()
WHERE id = sqlc.arg(order_id)::bigint;
