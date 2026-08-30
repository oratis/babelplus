-- wallet.sql · 钱包、邀请码、返佣（面板侧）
--
-- 事实源：openapi/openapi.yaml（已冻结）的 getWallet / listWalletTransactions /
--         createInviteCode / listInviteCodes / listCommissions / transferCommission 六个 operation；
--         ADR 0013 §3.5 与 §4.3（退款一律进不可提现余额）；
--         data-model.md §7.1（余额的唯一真相是分录，wallet_balances 只是缓存）；
--         docs/03-product/pricing-and-plans-revision-20260823.md C6（返佣改一次性定额）；
--         表的真身在 0007_ledger.up.sql、0003_accounts.up.sql，科目 seed 在 0015_payment_fixes.up.sql，
--         commissions_no_self_invite 约束在 0016_billing_rules.up.sql。
--
-- ============================================================
-- 🔴 量纲与不变量（写任何一条钱相关的代码前必须先接受）
-- ============================================================
--
-- · 所有金额一律 **bigint 人民币分**。禁止浮点（api-contract §2.6）。
-- · 余额的唯一真相是 `ledger_lines` 的聚合：
--     余额 = -SUM(amount) WHERE account = 'liability:user_wallet' AND subject_id = uid
--   `wallet_balances` 是**性能缓存不是真相**（data-model §7.1 原话），
--   两者不一致时**以视图为准**。
-- · `ledger_lines.amount` 是有符号的：正 = 借 Dr，负 = 贷 Cr。给用户加钱是贷方（负数），
--   所以用户视角的变动额是 `-amount`。这个负号出现在本文件三处，每一处都不是笔误。


-- ============================================================
-- §1 余额概览（getWallet）
-- ============================================================
--
-- 🔴 **为什么这条必须返回两个数字而不是一个。**
--    ADR 0013 ① 裁决「退款**一律退到不可提现的钱包余额**」。这句话在传播时最容易变形成
--    「退款进余额 ⇒ 余额里有一部分是退回来的钱 ⇒ 那部分应该能提出来」——
--    ADR §21 自己也记着「『只退到不可提现的余额』在熟人语境下会被至少一个人当面质疑」。
--
--    数据库拦不住这个误解：data-model §7.1 逐字写着「**『余额不可提现』在数据库层面无法强制**」，
--    它的实现方式是 ledger_accounts 里**不存在** `asset:bank ← liability:user_wallet` 这条路径，
--    且没有写提现代码。也就是说这条规则的守卫**只有 code review 与接口形状**。
--
--    所以接口形状要把话说死：`withdrawable_amount` 是一个**字面量 0**，不是聚合。
--    它出现在 SELECT 列表里就是在声明「本系统里可提现的钱是零，这不是算出来的，是设计」。
--    哪一天有人真要做提现，他必须先改掉这个 0 —— 而那一次修改会被 review 看见。
--    只返一个 `balance_amount`，那次修改就只是在 handler 里悄悄多加一个按钮。
--
-- ⚠️ 同时给出**来源拆分**（refund / commission / 其它），因为页面上要能回答
--    「我这 ¥38 是哪来的」。来源只存在于 ledger_entries.ref_type，wallet_balances 里没有 ——
--    这是本条必须扫分录、不能只读缓存表的唯一硬理由。
--
-- ⚠️ **本条扫分录，与 data-model §7.1「面板每次打开都要读余额，不能每次扫分录」张力何在**：
--    那条针对的是全表聚合。这里带 `account_id + subject_id` 两个等值条件，
--    走 ledger_lines_subject_idx，扫的是**这一个用户自己的几条腿**。
--    **撤回条件**：单用户的 liability:user_wallet 分录腿超过 ~1000 条（约等于每天一笔用十年），
--    或本条 p95 超过 20 ms 时，把来源拆分物化到 wallet_balances 上再加三列。
--
-- ⚠️ 同时返回 `balance_cached` 是为了让漂移**在每次打开钱包页时**就暴露，
--    而不是等每天一次的 ReconcileWalletBalances（orders.sql）。
--    两者不等时按 data-model §7.1 服务 ledger 的数并告警 —— 服务缓存的数等于
--    把一个已知错误的数字当真，而钱的数字错了不能靠「明天对账会发现」。
--
-- ⚠️ 币种钉死 'CNY'：liability:user_wallet 这个科目在 0015 的 seed 里就是 CNY，
--    per-account 的币种（§17.6(b)）。此处不带币种参数是刻意的 ——
--    多币种钱包是一次产品裁决，不是一个查询参数。
--    落在这个科目上的非 CNY 分录腿是 bug，且不会被本条数进来（宁可少算也不混算）。
--
-- 聚合无 GROUP BY 恒返回一行，所以 :one 在「用户一分钱都没有」时也不会 ErrNoRows。
-- name: GetWalletOverview :one
WITH wallet AS (
  SELECT
    (- coalesce(sum(l.amount), 0))::bigint AS balance_ledger,
    (- coalesce(sum(l.amount) FILTER (WHERE e.ref_type = 'refund'), 0))::bigint     AS from_refund,
    (- coalesce(sum(l.amount) FILTER (WHERE e.ref_type = 'commission'), 0))::bigint AS from_commission,
    (- coalesce(sum(l.amount) FILTER (WHERE e.ref_type = 'order'), 0))::bigint      AS from_order
  FROM ledger_lines l
  JOIN ledger_entries  e ON e.id = l.entry_id
  JOIN ledger_accounts a ON a.id = l.account_id
  WHERE a.code = 'liability:user_wallet'
    AND l.subject_id = sqlc.arg(user_id)::bigint
    AND l.currency = 'CNY'
),
commission AS (
  -- 两段式（pricing §5）：pending = 确认中（冷静期未到），confirmed = 已获得可划转。
  -- transferred 已经变成余额了，不能再数一遍；voided 是退款套利被作废的，永远不该出现在任何合计里。
  SELECT
    coalesce(sum(c.amount) FILTER (WHERE c.status = 'pending'),   0)::bigint AS pending,
    coalesce(sum(c.amount) FILTER (WHERE c.status = 'confirmed'), 0)::bigint AS available
  FROM commissions c
  WHERE c.inviter_id = sqlc.arg(user_id)::bigint
)
SELECT
  w.balance_ledger,
  w.balance_ledger AS non_withdrawable_amount,
  -- 🔴 字面量，不是聚合。见本节开头。
  0::bigint        AS withdrawable_amount,
  w.from_refund,
  w.from_commission,
  w.from_order,
  cm.pending   AS commission_pending_amount,
  cm.available AS commission_available_amount,
  coalesce((SELECT b.balance FROM wallet_balances b
             WHERE b.user_id = sqlc.arg(user_id)::bigint), 0)::bigint AS balance_cached
FROM wallet w CROSS JOIN commission cm;


-- ============================================================
-- §2 余额流水（listWalletTransactions）
-- ============================================================
--
-- 🔴 **流水不是一张表，是分录的投影。** 没有 wallet_transactions 表，也不该有 ——
--    再建一张就是第二份真相，而 data-model §7.1 已经因为「wallet_balances 是缓存」
--    付了一次每日对账的代价，不该再付第二次。
--
-- 🔴 **balance_after 必须用窗口函数算，不能在 Go 里累加。**
--    openapi 的 WalletTransaction `required` 里有 `balance_after`。
--    在 Go 里从当前余额往回减，等于用「现在的余额」重建「当时的余额」——
--    只要有一条分录在两次查询之间写进来，整页的历史余额就全错了，
--    而且错的方式是**每一行都错同一个数**，看起来像一个系统性 bug 而不是竞态。
--    窗口函数在同一个快照里算完，页与页之间也自洽。
--
-- ⚠️ **代价登记**：`sum() OVER (ORDER BY id)` 要扫这个用户的全部历史腿才能算出第一页的
--    balance_after，翻到第 10 页也是同样的全扫。走 ledger_lines_subject_idx，
--    单用户几十到几百行时可以忽略。**撤回条件同 §1**：单用户腿数破千，
--    或本条 p95 超过 50 ms 时，改成在 ledger_lines 上物化一列 running_balance
--    （代价是它必须与分录同事务写，且成为第三个可能漂移的地方）。
--
-- ⚠️ 游标只用 `id`，不用 (created_at, id)。ledger_lines.id 是 IDENTITY 列，
--    本身就是全序且与插入顺序一致，破平手键是多余的。
--    openapi 的 CursorQuery 解出来是 `{"id":…,"at":"…"}`，handler 用 id 那半即可，
--    at 那半留着校验类型（契约要求「服务端必须校验解出的字段类型」）。
--
-- ⚠️ **type 的映射是 handler 的事，不是 SQL 的事，而且映射不完整。**
--    openapi 的 WalletTransaction.type 枚举是
--      recharge / consume / refund / commission_transfer / admin_adjust / expired_order_credit
--    而 ledger_entries.ref_type 的取值是 order / refund / commission / reconcile_adjust
--    （0007 的列注释）。**两套词汇表不是一一对应的**：
--      · `order` 一个值要按 amount 的符号劈成 recharge（充值进余额）与 consume（余额抵扣订单）；
--      · `expired_order_credit`（超额入余额，data-model §730 的兜底）在 ref_type 里没有专属值，
--        它现在也走 order；要区分就得让写入方给一个更细的 ref_type。
--      · `reconcile_adjust` 对应 admin_adjust。
--    所以本条把 `ref_type`、`delta` 的符号、`description` 三样都交出去，
--    让 handler 有足够信息做映射 —— 而不是在 SQL 里 CASE 出一个会悄悄漏一类的枚举。
--
-- ⚠️ `description` 是给人看的分录摘要，会出现在用户的流水页上。写分录的地方要注意
--    别把内部术语或对方用户的身份写进去 —— 它不是内部字段。
-- name: ListWalletTransactions :many
WITH legs AS (
  SELECT
    l.id,
    l.created_at,
    e.ref_type,
    e.ref_id,
    e.entry_no,
    e.description,
    (-l.amount)::bigint                                    AS delta,
    (- (sum(l.amount) OVER (ORDER BY l.id)))::bigint       AS balance_after
  FROM ledger_lines l
  JOIN ledger_entries  e ON e.id = l.entry_id
  JOIN ledger_accounts a ON a.id = l.account_id
  WHERE a.code = 'liability:user_wallet'
    AND l.subject_id = sqlc.arg(user_id)::bigint
    AND l.currency = 'CNY'
)
SELECT id, created_at, ref_type, ref_id, entry_no, description, delta, balance_after
FROM legs
WHERE sqlc.narg(cursor_id)::bigint IS NULL OR id < sqlc.narg(cursor_id)::bigint
ORDER BY id DESC
LIMIT sqlc.arg(page_limit)::int;


-- ============================================================
-- §3 邀请码（createInviteCode / listInviteCodes）
-- ============================================================
--
-- 🔴 **为什么不复用 users.sql 的 CreateInviteCode。**
--    那一条是通用插入：`(code, owner_user_id, max_uses, expires_at, note)` 五个参数全开。
--    用户面调用它必须自己传 `max_uses = 1`，而传错的后果不是报错 ——
--    `invite_codes_user_single_use` 这条 CHECK 会拒绝 owner_user_id IS NOT NULL 且 max_uses <> 1，
--    所以传错**会**报错。真正拦不住的是另一件事：**名额**。
--
--    data-model §4.1 记着「每用户未核销码 ≤ 3」这条规则「无法用声明式约束表达
--    （PG 没有跨行 CHECK），落在应用层 + 巡检 SQL」。而 openapi 给 createInviteCode
--    声明了 **403 ErrForbidden** —— 那个 403 就是这条名额规则。
--    handler 若先 count 再 insert，两条语句之间的并发请求会双双通过（TOCTOU），
--    而「并发」在这里不是理论问题：用户在生成按钮上连点两下就够了。
--
--    本条把 count 放进 INSERT 的 WHERE：**超额时插不进去，返回 0 行 → 403**，
--    一次往返，不需要事务里的读后写。
--
-- ⚠️ **这条收窄了竞态但没有关闭它。** READ COMMITTED 下两个并发事务都能看到 count = 2，
--    于是都插入，结果是 4 条未核销码。要真正关闭需要在同一事务里先取一把
--    以 owner_user_id 为键的 advisory lock，或者把隔离级别提到 SERIALIZABLE。
--    **本文件不加锁**，因为代价与收益不成比例：输掉这次竞态的后果是「多了一条邀请码」，
--    而 users.sql 的 FindUsersOverInviteCodeQuota 巡检本来就在找这种行 ——
--    机制已经存在，不必为一个 3 变 4 的偏差给每次生成邀请码都加一把锁。
--    ⚠️ 如果哪天名额规则变成有金钱含义的东西（比如每个码带奖励），这段推理立刻失效，
--       那时必须补锁。
--
-- ⚠️ max_uses 写死 1，不是参数：user-journey §3.2 裁决「用户码恒为一次性核销；
--    只有管理员种子码可 1–N 次」。让它成为参数就等于把一条产品裁决交给调用点决定。
--    管理员的批量种子码走 users.sql 的 CreateInviteCode（owner_user_id 传 NULL）。
--
-- ⚠️ 名额上限是参数（`max_unused`）不是常量：3 这个数字来自 data-model §4.1，
--    但它是一条**运营参数**，改它不该要一次 sqlc 重新生成 + contract-drift 复核。
--
-- ⚠️ code 由 handler 生成（大写、剔除 0/O/1/I/l 等易混字符，0003 的列注释），
--    不在 SQL 里造 —— 随机串的字符集是一条产品决定，且要能单元测试。
--    唯一索引 invite_codes_code_uk 会在撞码时报唯一冲突，handler 重试即可。
-- name: CreateUserInviteCode :one
INSERT INTO invite_codes (code, owner_user_id, max_uses, expires_at, note)
SELECT
  sqlc.arg(code)::text,
  sqlc.arg(owner_user_id)::bigint,
  1,
  sqlc.narg(expires_at)::timestamptz,
  sqlc.narg(note)::text
WHERE (
  SELECT count(*)
  FROM invite_codes ic
  WHERE ic.owner_user_id = sqlc.arg(owner_user_id)::bigint
    AND ic.revoked_at IS NULL
    AND ic.used_count = 0
) < sqlc.arg(max_unused)::int
RETURNING id, code, owner_user_id, max_uses, used_count, expires_at, revoked_at, note, created_at;


-- 🔴 **为什么不复用 users.sql 的 ListInviteCodesByOwner。**
--    它带 `revoked_at IS NULL`，而 openapi 的 InviteCode.status 枚举是
--    **[ok, exhausted, disabled]** —— `disabled` 这个取值只可能来自被吊销或已过期的码。
--    过滤掉它们，那个枚举值就永远发不出去，而用户会以为自己吊销掉的码「消失了」，
--    于是再生成一个（然后撞上 §3 的名额闸门，得到一个他完全无法理解的 403）。
--
-- ⚠️ status 由 handler 从四列推，SQL 不推：
--      revoked_at IS NOT NULL 或 expires_at 已过 → disabled
--      used_count >= max_uses                    → exhausted
--      否则                                       → ok
--    ⚠️ 契约的三值枚举**装不下「已过期」**这个状态，只能并进 disabled。
--       这是契约的表达力不足，不是本查询的选择；页面上应当用文案区分
--       （「已吊销」与「已过期」的用户动作不同：前者是他自己撤的，后者是他忘了用）。
--
-- ⚠️ 不返回 invite_url：那要拼当前域名，而域名会随 ADR 0002 的失联恢复更换。
--    把它拼进数据库查询等于把一个会变的东西固化进一个不该变的地方。
-- name: ListUserInviteCodes :many
SELECT
  ic.id,
  ic.code,
  ic.max_uses,
  ic.used_count,
  ic.expires_at,
  ic.revoked_at,
  ic.note,
  ic.created_at
FROM invite_codes ic
WHERE ic.owner_user_id = sqlc.arg(owner_user_id)::bigint
ORDER BY ic.created_at DESC, ic.id DESC;


-- ============================================================
-- §4 佣金记录（listCommissions）
-- ============================================================
--
-- 🔴 **返佣口径是一次性定额，不是订单金额的 10%**（定价修订 C6，2026-08-23 全盘采纳）：
--    金额 = **该用户首单档位的月付标价 × 10%**，即 ¥7.20 / ¥15.90 / ¥35.80，
--    **与该订单的实际周期无关，每位被邀请用户只发一次**。
--    改这个口径的理由不是省钱：按订单金额 10% 会把 24 格价格表里的 **4 格**打穿 1.20× 成本地板
--    （最差 1.1474×），而返佣落在 orders 之后、**不流经下单服务的地板断言**——
--    「硬规则被写在它要防的东西不会经过的地方」。定额之后金额在下单时已知，
--    可以进断言的分子。
--
--    ⚠️ **本文件不写计提。** 计提发生在订单完成时，走 orders.sql 的 CreateCommission
--       （`UNIQUE (order_id)` 把「Cloud Tasks 至少一次投递导致重复发放」降级成数据库拒绝）。
--       但那条的 `rate_bps` 与 `amount` 是调用方算好传进去的，**没有任何东西保证它们符合 C6**：
--       rate_bps 仍然是「按比例」的形状，而定额口径下它只是一个记录值（1000 = 10%，
--       乘的是月付标价不是订单金额）。这一点必须写在计提处的注释里，否则下一个人
--       会照着列名把它乘回订单金额上，而结果只是每笔少了几块钱 —— 静默、且直到年度复盘才会发现。
--       0016 加的 `commissions_no_self_invite` 只挡住了最蠢的形态（自己邀请自己），
--       挡不住这个。
--
-- ⚠️ **status 的映射对不上，且缺一格。**
--    openapi 的 Commission.status 枚举是 **[pending, confirmed, settled]**，
--    而 commissions.status 的 CHECK 是 **pending / confirmed / transferred / voided**。
--    transferred → settled 是显然的；**voided 在契约里没有对应值**。
--    handler 不能把 voided 映射成三者中任意一个（映射成 settled 是谎话，
--    映射成 pending 会让用户一直等一笔永远不会到的钱）。
--    唯一诚实的做法是**在列表里保留 voided 的行但用独立文案渲染**，
--    并把这条冲突登记进 api-contract §14 那张未裁决表。本查询把原始 status 交出去。
--
-- ⚠️ **order_trade_no 是一个安全敏感字段。** 它是**被邀请人**的订单号，
--    出现在**邀请人**的页面上（契约要求的，Commission.order_trade_no）。
--    🔴 orders.sql 的 `GetOrderByTradeNo` 是 `SELECT * FROM orders WHERE trade_no = $1`，
--       **没有 user_id 约束**。只要有任何一个用户面 handler 用它按 trade_no 取单
--       而不再校验归属，本字段就把「查看他人订单」的入口直接送到了邀请人手上。
--       用户面按 trade_no 取单**必须**带 user_id 条件。这条登记在交付说明里。
--
-- ⚠️ 游标同 §2：commissions.id 是 IDENTITY，单键即全序。
-- name: ListUserCommissions :many
SELECT
  c.id,
  o.trade_no AS order_trade_no,
  c.amount,
  c.rate_bps,
  c.status,
  c.confirm_at,
  c.confirmed_at,
  c.voided_reason,
  c.created_at
FROM commissions c
JOIN orders o ON o.id = c.order_id
WHERE c.inviter_id = sqlc.arg(inviter_id)::bigint
  AND (sqlc.narg(cursor_id)::bigint IS NULL OR c.id < sqlc.narg(cursor_id)::bigint)
ORDER BY c.id DESC
LIMIT sqlc.arg(page_limit)::int;


-- ============================================================
-- §5 佣金划转到余额（transferCommission）
-- ============================================================
--
-- 🔴 **这个端点在当前 schema 下写不出一条平衡的分录。**
--    划转的贷方腿是 `liability:user_wallet`（0015 seed 里有）。借方腿是「我们为拉新付出的成本」，
--    即一个 `expense:commission` 科目 —— **0015 的 seed 里没有它**，
--    ledger_accounts 现有的十个 code 里也没有任何一个语义上能当它用
--    （expense:refund 被 ADR 0013 §3.5 明确限定为「只用于 destination='original' 以及
--     追不回来的佣金」，把划转记在它下面会让退款支出与获客支出混成一个数）。
--
--    后果：handler 走到 GetLedgerAccountByCode 时拿到 ErrNoRows，
--    **而那时用户已经点了「划转」**。所以下面这条 :one 存在的全部意义是**提前失败**：
--    在动 commissions.status、动 wallet_balances 之前先把两个科目一起取出来，
--    任一为 NULL 就整条拒绝，绝不写半条分录。
--    ⚠️ 真正的修复是一次 migration（补 `('expense:commission','expense','CNY')`），
--       迁移不由本阶段加。**在那条 migration 落地之前，transferCommission 应当返回 501/503
--       而不是 500** —— 500 会让用户以为是偶发故障并反复重试。
--
-- 聚合恒返回一行；两列都可能为 NULL，emit_pointers_for_null_types 下是 *int64，判 nil 即可。
-- name: GetCommissionTransferAccounts :one
SELECT
  max(id) FILTER (WHERE code = 'expense:commission')::bigint     AS expense_account_id,
  max(id) FILTER (WHERE code = 'liability:user_wallet')::bigint  AS wallet_account_id
FROM ledger_accounts
WHERE code IN ('expense:commission', 'liability:user_wallet');


-- 可划转的佣金，按确认时间从旧到新锁住。
--
-- 🔴 **划转的最小粒度是一条 commission，不是一分钱。**
--    commissions 的 status 是**整行**的状态，没有 amount_transferred 这样的列 ——
--    一条 ¥15.90 的佣金要么整条 transferred，要么原封不动。
--    而 openapi 的 CommissionTransferRequest 只有一个 `amount: minimum 1`，
--    形状上允许「划走 ¥3」。**两者不兼容**，这是契约与 schema 的一处实打实的冲突。
--
--    可行的解读只有一种：`amount` 必须等于**按本条顺序取前 k 条的累加和**中的某一个值，
--    否则 422（openapi 给这个端点声明了 422，用途正在于此）。
--    页面上应当只提供「全部划转」以及每条佣金旁边的勾选，不要给自由输入框 ——
--    自由输入框会让绝大多数请求以 422 结束，而用户看不出自己错在哪。
--    ⚠️ openapi 自己在 listCommissions 上标注了「佣金结算的状态机端点**未设计**（§14）」，
--       本条冲突属于同一个未裁决区。裁决落地前不要为了「支持任意金额」去加
--       amount_transferred 列 —— 那会让一条佣金同时处在两个状态里。
--
-- 🔴 `FOR UPDATE` 是必需的，且**不能与窗口函数同处一条 SELECT**
--    （PostgreSQL 直接报 `FOR UPDATE is not allowed with window functions`）。
--    所以累加和在 Go 里做，SQL 只负责按确定顺序锁行。
--    不锁的话，用户在两个标签页同时点划转，同一条佣金会被划两次 ——
--    第二次的 UPDATE 命中 0 行（下面那条带 status='confirmed' 条件），
--    但**分录已经写了两遍**，用户凭空多出一份余额。
--
-- ⚠️ 排序用 (confirmed_at, id)：confirmed_at 可能有并列（同一次批量确认），id 破平手。
--    顺序必须确定，否则两个并发事务按不同顺序锁行就会死锁。
-- name: LockTransferableCommissions :many
SELECT c.id, c.amount, c.confirmed_at
FROM commissions c
WHERE c.inviter_id = sqlc.arg(inviter_id)::bigint
  AND c.status = 'confirmed'
ORDER BY c.confirmed_at, c.id
FOR UPDATE;


-- 🔴 **为什么不循环调用 orders.sql 的 MarkCommissionTransferred。**
--    那一条一次一行。N 条佣金 N 次往返本身只是慢，真正的问题是**部分成功**：
--    第 3 条撞上并发被拒时，前 2 条已经 transferred 了，而分录还没写 ——
--    用户的佣金消失且余额没增加。一条语句 + 一次 rows 比对，
--    让「要么全改要么全不改」成为一次可判定的事实。
--
-- 🔴 **调用方必须断言 `rows == len(ids)`，不等就回滚整个事务。**
--    带 `status = 'confirmed'` 与 `inviter_id` 两个条件是防线，不是保证：
--    rows 少了就意味着有别人抢先改了状态（或者 id 根本不属于这个人），
--    此时继续写分录就是凭空造钱。这条断言写在 handler 里，SQL 只能把数字给出来。
--
-- ⚠️ commissions 上**没有** updated_at 列（0007 逐列核过），所以这里不写时间戳。
--    「什么时候划转的」的证据在 ledger_entries.created_at 上 —— 那才是账。
-- name: MarkCommissionsTransferredBulk :execrows
UPDATE commissions
SET status = 'transferred'
WHERE id = ANY(sqlc.arg(ids)::bigint[])
  AND inviter_id = sqlc.arg(inviter_id)::bigint
  AND status = 'confirmed';


-- ℹ️ **本节没有写分录、也没有改 wallet_balances 的查询。** 那三步复用既有的：
--      orders.sql 的 CreateLedgerEntry（ref_type = 'commission'，ref_id = 任一条 commission.id）
--    → orders.sql 的 CreateLedgerLine × 2（借 expense:commission / 贷 liability:user_wallet，
--      贷方腿的 amount 为负、subject_id = 用户 id）
--    → orders.sql 的 UpsertWalletBalance（它的 balance 参数是**增量**不是绝对值，
--      ON CONFLICT 分支写的是 `balance + EXCLUDED.balance`）
--    四步必须与 MarkCommissionsTransferredBulk **同一个事务**。
--    拆开的后果不是数字错，是**账不平**：FindUnbalancedLedgerEntries 每天都会报红，
--    而那时已经无法知道该以佣金表还是以分录为准。
--
-- ⚠️ 两条腿的 amount 必须**符号相反、绝对值相等**（SUM(lines.amount) = 0 是 0007 的核心不变量），
--    且两条腿的 currency 都必须是 'CNY' —— 分录按 (entry_id, currency) 分组之后才平。
