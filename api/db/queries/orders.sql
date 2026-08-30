-- orders.sql · 套餐、订单、优惠码、幂等、复式账、返佣
--
-- 事实源：data-model.md §6 / §7、payments.md §4.13
--
-- 🔴 金额一律 bigint 存**人民币分**。类型层面不给 float 留口子。
--    唯一的 numeric 是 pay_amount_raw / pay_amount_received（链上 USDT 数量）与 fx_usdt_per_cny，
--    它们不参与任何货币再计算。

-- ============================================================
-- 套餐
-- ============================================================

-- name: ListSellablePlans :many
SELECT * FROM plans
WHERE visible = true AND archived_at IS NULL
ORDER BY sort_order, id;

-- name: GetPlan :one
SELECT * FROM plans WHERE id = $1;

-- name: GetPlanByCode :one
SELECT * FROM plans WHERE code = $1 AND archived_at IS NULL;

-- kind 是第 19 个参数，且**必须显式传**（ADR 0013 §4.6）。
-- 0016 刻意没给它 DEFAULT：若默认成 'cycle'，每一个通过后台建出来的加油包都会被默默写成周期套餐，
-- 于是 POST /orders 把它推导成 new/renew/upgrade，走进周期套餐的开通逻辑并凭空触发一次折抵 ——
-- 一次静默的错误分类。无默认值意味着「建套餐时忘了填 kind」是数据库拒绝，当场就知道。
-- name: CreatePlan :one
INSERT INTO plans (
  code, name, group_id, transfer_enable, device_limit, speed_limit_mbps, reset_traffic_method,
  price_monthly, price_quarterly, price_half_yearly, price_yearly, price_onetime, price_reset,
  renewable, sellable, visible, sort_order, content_md, kind
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
RETURNING *;

-- 下架 ≠ 删除：历史订单引用 plans（data-model §13）
-- name: ArchivePlan :exec
UPDATE plans SET archived_at = now(), sellable = false, visible = false, updated_at = now()
WHERE id = $1 AND archived_at IS NULL;


-- ============================================================
-- 订单
-- ============================================================

-- ⚠️ orders_amount_balance CHECK 保证
--    amount_due = amount_gross - amount_discount - surplus_amount - amount_balance
--    在插入时就成立 —— 「三行金额加起来对不上」是数据库拒绝，不是对账时才发现。
--
-- 后四个参数是 0016 加的服务区间与窗口链（ADR 0013 §2.1），按 type 写死，**没有第四种写法**：
--   type      covers_from($16)              covers_to($17)      prev_order_id($18)
--   new       paid_at                       covers_from+period  NULL（窗口的根）
--   renew     greatest(paid_at, 旧covers_to) covers_from+period  旧的那一单
--   upgrade   paid_at                       旧 covers_to（不变） 被折抵掉的那一单
--   traffic_pack / reset_pack / wallet_topup 三列全 NULL
-- renew 取 greatest 而不是 paid_at：提前 10 天续年付的用户，他买的那一年是从**旧到期日**开始的；
-- 用 paid_at 会把每日单价摊薄 10/375 = 2.7%，而退款按天折算读的就是这个单价。
-- upgrade 的 covers_from 取 paid_at（不继承）：升级不买时间只换档位，D_total 必须是**本段**的长度，
-- 继承会让链式升级的折抵额凭空缩水（ADR 0013 §2.1 第 2 条的算例：1600 变成 800）。
--
-- price_monthly_at_order($19) 是下单时 plans.price_monthly 的快照（分）。退款扣减必须用它、
-- 不能读活列 —— 否则涨价之后老订单的退款额跟着变小，用户会认为我们改价来少退钱
-- （user-journey §10.2 硬要求 1）。这一列同时是 GetRefundBasis 里 consumed_time 的乘数。
-- name: CreateOrder :one
INSERT INTO orders (
  trade_no, user_id, type, plan_id, period, currency,
  amount_gross, amount_discount, surplus_amount, amount_balance, amount_due,
  surplus_order_ids, coupon_id, invited_by, expires_at,
  covers_from, covers_to, prev_order_id, price_monthly_at_order
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
RETURNING *;

-- name: GetOrderByTradeNo :one
SELECT * FROM orders WHERE trade_no = $1;

-- name: GetOrderByID :one
SELECT * FROM orders WHERE id = $1;

-- name: GetOrderForUpdate :one
SELECT * FROM orders WHERE id = $1 FOR UPDATE;

-- name: ListUserOrders :many
SELECT * FROM orders
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- 绑定链上收款地址与识别金额。
-- orders_pay_addr_amount_uk 保证「地址 + 唯一金额」在未终结订单里唯一
-- （EPUSDT 的金额尾数递增法），冲突时调用方递增尾数重试。
-- name: AttachPaymentAddress :one
UPDATE orders SET
  gateway = $2, pay_chain = $3, pay_address = $4,
  pay_amount_raw = $5, fx_usdt_per_cny = $6, fx_locked_at = now(),
  address_watch_until = $7,
  status = 'paying', updated_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- 状态迁移。⚠️ 调用方必须在同一事务里 InsertOrderTransition —— 状态机没有触发器兜底，
-- 漏写审计不会报错（这与 user_rev 不同：那里漏了是静默故障，这里漏了是证据缺失）。
-- name: UpdateOrderStatus :one
UPDATE orders SET
  status = $2,
  paid_at      = CASE WHEN $2::order_status IN ('paid','completed') THEN coalesce(paid_at, now()) ELSE paid_at END,
  completed_at = CASE WHEN $2::order_status = 'completed' THEN coalesce(completed_at, now()) ELSE completed_at END,
  cancelled_at = CASE WHEN $2::order_status IN ('cancelled','expired') THEN coalesce(cancelled_at, now()) ELSE cancelled_at END,
  updated_at = now()
WHERE id = $1
RETURNING *;

-- 链上到账入账。pay_amount_received 独立成列，underpaid 才能显式回答
-- 「已收到 X，还差 Y」（page-inventory §3.2 要求收银台展示这句话）。
-- name: RecordOrderPayment :one
UPDATE orders SET
  pay_amount_received = pay_amount_received + $2,
  amount_paid = amount_paid + $3,
  gateway_ref = coalesce($4, gateway_ref),
  updated_at = now()
WHERE id = $1
RETURNING id, amount_due, amount_paid, pay_amount_raw, pay_amount_received, status;

-- 超时取消（走 orders_expiry_idx 部分索引，只覆盖未终结订单）
-- name: ListExpiredUnpaidOrders :many
SELECT id, trade_no, user_id, status, expires_at
FROM orders
WHERE status IN ('pending','paying','underpaid') AND expires_at <= now()
ORDER BY expires_at
LIMIT $1;

-- 过期订单继续监听收款地址 ≥ 24h（user-journey §7）。到账后入账为**余额**而不是直接开通
-- —— 余额仅可消费不可提现，这个兜底在资金合规上是安全的。
-- 不做这一条，用户第一次付款的钱就真的进黑洞。
-- ⚠️ 走 orders_addr_watch_idx；索引谓词里不能有 now()（非 IMMUTABLE），
--    所以过滤条件写在 WHERE 里走范围扫描。
-- name: ListWatchedPaymentAddresses :many
SELECT id, trade_no, user_id, pay_chain, pay_address, pay_amount_raw, status, address_watch_until
FROM orders
WHERE pay_address IS NOT NULL
  AND address_watch_until > now()
ORDER BY address_watch_until;

-- name: InsertOrderTransition :one
INSERT INTO order_transitions (order_id, from_status, to_status, reason, actor)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListOrderTransitions :many
SELECT * FROM order_transitions WHERE order_id = $1 ORDER BY created_at;


-- ============================================================
-- 优惠码
-- ============================================================

-- value 的单位随 type 变：percentage = 基点 bps（1000 = 10%）；fixed_amount = 分。
-- 折扣计算必须在 Go 侧用整数运算，绝不引入 float。
-- name: GetCouponByCode :one
SELECT * FROM coupons
WHERE upper(code) = upper($1)
  AND (starts_at IS NULL OR starts_at <= now())
  AND (ends_at   IS NULL OR ends_at   >  now())
  AND (total_uses IS NULL OR used_count < total_uses);

-- name: IncrementCouponUse :one
UPDATE coupons SET used_count = used_count + 1
WHERE id = $1 AND (total_uses IS NULL OR used_count < total_uses)
RETURNING id, used_count, total_uses;

-- name: CountUserCouponUses :one
SELECT count(*)::bigint FROM orders
WHERE user_id = $1 AND coupon_id = $2
  AND status NOT IN ('cancelled','expired','failed');


-- ============================================================
-- 幂等与 webhook 重放防护
-- ============================================================

-- name: ClaimIdempotencyKey :one
INSERT INTO idempotency_keys (key, user_id, endpoint, request_hash, status)
VALUES ($1, $2, $3, $4, 'in_progress')
ON CONFLICT (key) DO NOTHING
RETURNING *;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE key = $1 AND expires_at > now();

-- name: CompleteIdempotencyKey :exec
UPDATE idempotency_keys
SET status = 'completed', response_code = $2, response_body = $3
WHERE key = $1;

-- 24 小时硬删（data-model §13）
-- name: CleanupExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE expires_at < now();

-- 🔴 UNIQUE (gateway, event_id) 是重放防护的核心：易支付回调可被伪造
--    （NewAPI 的真实漏洞），幂等靠数据库唯一约束比靠应用层判断可靠。
--    DO NOTHING 返回 0 行 = 这条事件已经处理过，直接返回成功，不要重复入账。
-- name: InsertWebhookEvent :one
INSERT INTO webhook_events (gateway, event_id, event_type, payload_hash, raw_body, signature_ok)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (gateway, event_id) DO NOTHING
RETURNING *;

-- name: MarkWebhookProcessed :exec
UPDATE webhook_events SET processed_at = now(), error = $2 WHERE id = $1;

-- 2 年硬删（data-model §13：拒付申诉的证据窗口）
-- name: CleanupOldWebhookEvents :execrows
DELETE FROM webhook_events WHERE received_at < now() - ($1::interval);


-- ============================================================
-- 复式账（append-only；纠错用反向冲正，不 UPDATE 不 DELETE）
-- ============================================================

-- name: GetLedgerAccountByCode :one
SELECT * FROM ledger_accounts WHERE code = $1;

-- name: CreateLedgerAccount :one
INSERT INTO ledger_accounts (code, kind, currency) VALUES ($1, $2, $3) RETURNING *;

-- name: CreateLedgerEntry :one
INSERT INTO ledger_entries (entry_no, description, ref_type, ref_id, reverses_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- 🔴 调用方必须保证 SUM(amount) = 0（借贷必相等）。
--    这条不变量在 schema 层表达不出来（跨行），只能靠 service 层 + 下面的巡检。
-- name: CreateLedgerLine :one
INSERT INTO ledger_lines (entry_id, account_id, subject_id, amount, currency)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- 巡检：借贷不平的分录。返回非空行 = 立即告警。
-- name: FindUnbalancedLedgerEntries :many
SELECT entry_id, sum(amount)::bigint AS imbalance
FROM ledger_lines
GROUP BY entry_id
HAVING sum(amount) <> 0;

-- name: ListLedgerLinesByEntry :many
SELECT * FROM ledger_lines WHERE entry_id = $1 ORDER BY id;

-- name: ListLedgerEntriesByRef :many
SELECT * FROM ledger_entries WHERE ref_type = $1 AND ref_id = $2 ORDER BY created_at;


-- ============================================================
-- 钱包余额
-- ============================================================

-- 缓存表，读路径用（面板每次打开都要读余额，不能每次扫分录）
-- name: GetWalletBalance :one
SELECT * FROM wallet_balances WHERE user_id = $1;

-- ⚠️ balance >= 0 是 CHECK，扣款扣穿会被数据库拒绝而不是写出负余额。
--    「余额不可提现」数据库强制不了 —— 它靠 ledger_accounts 里不存在
--    asset:bank ← liability:user_wallet 这条路径，且没有写提现代码。
-- name: UpsertWalletBalance :one
INSERT INTO wallet_balances (user_id, currency, balance, last_entry_id, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (user_id) DO UPDATE SET
  balance = wallet_balances.balance + EXCLUDED.balance,
  last_entry_id = EXCLUDED.last_entry_id,
  updated_at = now()
RETURNING *;

-- 唯一真相是视图，缓存表只是性能。每日 Cloud Scheduler 必须跑这条对账，
-- 返回非空行 = 立即告警，且**以视图为准**（data-model §7.1）。
-- name: ReconcileWalletBalances :many
SELECT b.user_id, b.currency,
       b.balance                     AS cached,
       coalesce(v.balance, 0)::bigint AS ledger
FROM wallet_balances b
LEFT JOIN user_wallet_balance v ON v.user_id = b.user_id AND v.currency = b.currency
WHERE b.balance IS DISTINCT FROM coalesce(v.balance, 0);


-- ============================================================
-- 返佣（两段式 + 冷静期）
-- ============================================================

-- 🔴 UNIQUE (order_id)：一单只产生一条佣金 —— 把「Cloud Tasks 至少一次投递导致
--    佣金重复发放」这个 ADR 0006 点名的风险，从应用逻辑降级成数据库拒绝。
-- name: CreateCommission :one
INSERT INTO commissions (order_id, inviter_id, invitee_id, rate_bps, amount, confirm_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (order_id) DO NOTHING
RETURNING *;

-- 到点确认（走 commissions_due_idx 部分索引，一次范围扫描）
-- name: ListCommissionsDueForConfirm :many
SELECT * FROM commissions
WHERE status = 'pending' AND confirm_at <= now()
ORDER BY confirm_at
LIMIT $1;

-- name: ConfirmCommission :one
UPDATE commissions SET status = 'confirmed', confirmed_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- 退款套利：订单被退时作废对应佣金
-- name: VoidCommission :one
UPDATE commissions SET status = 'voided', voided_reason = $2
WHERE id = $1 AND status IN ('pending','confirmed')
RETURNING *;

-- name: MarkCommissionTransferred :one
UPDATE commissions SET status = 'transferred'
WHERE id = $1 AND status = 'confirmed'
RETURNING *;

-- name: ListCommissionsByInviter :many
SELECT * FROM commissions
WHERE inviter_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: SumConfirmedCommissions :one
SELECT coalesce(sum(amount), 0)::bigint AS total
FROM commissions
WHERE inviter_id = $1 AND status = 'confirmed';


-- ============================================================
-- 退款
-- ============================================================

-- 退款基数：沿 prev_order_id 把整条订阅窗口链回溯出来，一次算清 V_window / consumed_time /
-- consumed_data / refund_B（ADR 0013 §3.2 的公式逐行落地）。
--
-- 🔴 **基数是 V_window（整条链的实付求和），不是 source.amount_paid、不是 V_source**（§4.3 边界 3）：
--    amount_paid 会吞掉被折抵掉的那部分（用户实实在在付过），amount_gross 会退他没付过的钱，
--    V_source 只覆盖最后一段。V_window 是唯一处处正确的量。
--    求和刻意**不含** surplus_amount（那是窗口内部的价值回收，算进去等于把同一笔钱数两遍，§2.2）
--    也**不含** amount_discount（优惠码面值不该被洗成可退现的信用）。
--
-- 为什么必须递归而不是按时间区间猜连续性：升级会在窗口中间插入新的一段，
-- 而「最后一笔已完成订单的 paid_at」这个锚点**会被升级重置**，退款基数因此每升级一次就凭空缩水一次
-- （ADR 0013 §7.1 C2 的原始缺陷）。prev_order_id 让窗口成为一条可以走完的链表。
--
-- 分段口径：每一段的时间价值按**该段自己的**月付标价快照折算，段末取 lead(covers_from)
-- （最后一段取 now()）。按比例、不取整到月 —— ceil 会制造一个 24 小时的全额退款窗口
-- （now() = paid_at 时 ceil(0/30) = 0，refund = V），而那个口子不受 Class A 四道闸门管辖。
--
-- ⚠️ price_monthly_at_order 允许为 NULL（0016 之前的历史订单没有快照）。求和里 coalesce 成 0
--    是因为 sum() 会**静默忽略** NULL —— 一条 NULL 会让整条链的 consumed_time 少算而没有任何迹象。
--    原始列一并返回，调用方看见 NULL **必须拒绝退款并转人工**，而不是当成 0 接着算。
--
-- ⚠️ 调用方必须传一笔 status = 'completed' 的周期订单 id（§2.2 的 source 选取）。
--    锚点不过滤 status 是照 ADR 原文；祖先则显式过滤，避免把一笔未完成的历史单算进实付。
--    传 traffic_pack / reset_pack / wallet_topup 一律不退（Class C），不该走到这里。
--
-- 每行是窗口链上的一段（按 covers_from 升序）；v_window / consumed_time / consumed_data /
-- refund_b 四列在所有行上取值相同，是整条链的汇总 —— 一次查询同时拿到明细与结论，
-- 结算页那三行（user-journey §10.2）与审计都读它。
-- name: GetRefundBasis :many
WITH RECURSIVE win AS (
    SELECT o.id, o.type, o.status, o.covers_from, o.covers_to, o.prev_order_id,
           o.amount_paid, o.amount_balance, o.price_monthly_at_order
    FROM orders o
    WHERE o.id = $1
  UNION ALL
    SELECT p.id, p.type, p.status, p.covers_from, p.covers_to, p.prev_order_id,
           p.amount_paid, p.amount_balance, p.price_monthly_at_order
    FROM orders p
    JOIN win w ON p.id = w.prev_order_id
    WHERE p.status = 'completed'
), seg AS (
  -- ⚠️ 显式 ::timestamptz 不是多余的：递归 CTE 里 sqlc 推不出窗口函数的返回类型，
  --    不写这个 cast，生成的 Go 字段会退化成 interface{}，调用方就得做类型断言。
  SELECT w.*,
         (lead(w.covers_from) OVER (ORDER BY w.covers_from, w.id))::timestamptz AS next_from
  FROM win w
), calc AS (
  SELECT seg.*,
         (seg.amount_paid + seg.amount_balance)::bigint AS segment_value,
         floor(coalesce(seg.price_monthly_at_order, 0)
               * greatest(0, extract(epoch FROM
                   (least(coalesce(seg.next_from, now()), now()) - seg.covers_from)) / 86400)
               / 30)::bigint AS segment_consumed_time
  FROM seg
), basis AS (
  -- consumed_data 只与「本周期已用的**套餐**流量」有关，与窗口链无关，所以整条链上是个常数。
  -- 分子截到 transfer_enable_plan：加油包的字节不参与退款扣减，因为加油包本来就不退（Class C）。
  -- 分母用 users.transfer_enable_plan（本周期实际发放的量，升级后按剩余天数折算过）而不是
  -- plans.transfer_enable；greatest(1, ·) 防除零（新注册用户配额是 0）。
  SELECT least(coalesce(ut.u, 0) + coalesce(ut.d, 0), u.transfer_enable_plan)::bigint AS plan_used,
         u.transfer_enable_plan,
         floor(coalesce(s.price_monthly_at_order, 0)
               * least(coalesce(ut.u, 0) + coalesce(ut.d, 0), u.transfer_enable_plan)::numeric
               / greatest(1, u.transfer_enable_plan))::bigint AS consumed_data
  FROM orders s
  JOIN users u          ON u.id = s.user_id
  LEFT JOIN user_traffic ut ON ut.user_id = u.id
  WHERE s.id = $1
)
SELECT
  calc.id   AS order_id,
  calc.type AS order_type,
  calc.covers_from,
  calc.covers_to,
  calc.next_from,
  calc.price_monthly_at_order,
  calc.segment_value,
  calc.segment_consumed_time,
  (sum(calc.segment_value)         OVER ())::bigint AS v_window,
  (sum(calc.segment_consumed_time) OVER ())::bigint AS consumed_time,
  basis.plan_used,
  basis.transfer_enable_plan,
  basis.consumed_data,
  -- Class B 的退款额。归零（refund_b = 0）即 Class C 的「不予退款」，不需要另写阈值表：
  -- 年付 75 折的用户恰好在第 270 天（9 个月）归零，与法务页的措辞逐字一致（§3.2）。
  -- Class A（首单 7 天善意窗口）豁免 consumed_time + consumed_data 两项扣减，即直接退 v_window，
  -- 那四道闸门由调用方判定，不在本查询里。
  greatest(0, (sum(calc.segment_value)         OVER ())
            - (sum(calc.segment_consumed_time) OVER ())
            - basis.consumed_data)::bigint AS refund_b
FROM calc
CROSS JOIN basis
ORDER BY calc.covers_from, calc.id;


-- ⚠️ user_id 是 0016 加的**冗余**列（NOT NULL，无默认值），必须显式写 ——
--    它存在的唯一理由是让 refunds_cooling_off_once 这条部分唯一索引成立，
--    「冷静期退款一生一次」因此是数据库拒绝，而不是应用代码的自觉（ADR 0013 §6.1）。
--    调用方必须传 orders.user_id，两者不一致会让那条唯一索引失效。
--    rule 有 DEFAULT 'manual'，但同样显式传：走的是哪一档退款规则是审计要回答的问题，
--    让它默认成 'manual' 等于把 cooling_off 的一生一次悄悄绕过去。
-- name: CreateRefund :one
INSERT INTO refunds (order_id, user_id, amount, destination, rule, reason, operator_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- orders_refund_le_paid CHECK 保证退款总额不会超过实收
-- name: AddOrderRefundedAmount :one
UPDATE orders SET amount_refunded = amount_refunded + $2, updated_at = now()
WHERE id = $1
RETURNING id, amount_paid, amount_refunded;

-- name: UpdateRefundStatus :one
UPDATE refunds SET status = $2, gateway_ref = coalesce($3, gateway_ref)
WHERE id = $1
RETURNING *;

-- name: ListRefundsByOrder :many
SELECT * FROM refunds WHERE order_id = $1 ORDER BY created_at DESC;
