-- stats.sql · 流量入账与统计
--
-- 事实源：data-model.md §8.4（bump 规则）、§9（统计）、api-contract.md §3.5（POST /push）
--
-- 🔴 流量不落明细流水。每次入账只写两处：user_traffic（本周期计数）+ stat_user_server（日聚合）。
--    Xboard 写三处，三份数字可能对不上且没有任何机制发现。
-- 🔴 真实写放大是 2×（data-model §9.1 对 ADR 0005 的修正）：P3 情景 80 行/秒，
--    行版本膨胀与 autovacuum 压力相应翻倍。

-- ============================================================
-- 批量累加流量（/push 的队列侧处理器调用，不在 HTTP 请求路径上）
-- ============================================================

-- 用 unnest 一条语句吃下整批，避免 N 次往返 —— 连接数是硬约束（每实例池 max=2）。
--
-- 三件事在一条语句里完成：
--   1. input   —— 先按 user_id 聚合，防止同一批里出现重复 user_id 导致 UPDATE...FROM 静默丢数
--   2. upd     —— 累加。RETURNING 里 ut.u/ut.d 是**新值**，减去增量即得旧值
--   3. bumped  —— 只有「跨越阈值那一次」才 bump user_rev（ADR 0006：累加不得 bump，
--                 否则节点每 60 秒都收 200 而不是 304，ETag 归零）
--
-- ⚠️ bumped 写成 data-modifying CTE 而不是 `SELECT bump_user_rev(...)`：
--    PostgreSQL 不执行**未被引用的**普通 CTE，而 data-modifying CTE 一定执行。
--    写成前者会得到一个「有时不 bump」的静默故障。
--
-- ⚠️ 三个数组用 WITH ORDINALITY 按下标配对，而不是 unnest(a, b, c)：
--    sqlc v1.31 的内建 catalog **没有多参数 unnest**，写成 unnest(a,b,c) 会在
--    `sqlc generate` 阶段直接报 `function unnest(unknown, unknown, unknown) does not exist`。
--    调用方必须保证三个数组等长（Go 侧 assert）。
--
-- ⚠️ 本语句必须与 BulkUpsertStatUserServer 在同一事务里。
-- name: BulkAddUserTraffic :many
WITH input AS (
  SELECT a.user_id, sum(b.u)::bigint AS u, sum(c.d)::bigint AS d
  FROM unnest(@user_ids::bigint[])   WITH ORDINALITY AS a(user_id, n)
  JOIN unnest(@up_bytes::bigint[])   WITH ORDINALITY AS b(u, n) ON b.n = a.n
  JOIN unnest(@down_bytes::bigint[]) WITH ORDINALITY AS c(d, n) ON c.n = a.n
  GROUP BY a.user_id
),
upd AS (
  UPDATE user_traffic ut
     SET u          = ut.u + i.u,
         d          = ut.d + i.d,
         u_lifetime = ut.u_lifetime + i.u,
         d_lifetime = ut.d_lifetime + i.d,
         online_at  = now(),
         last_node_id = @server_id::bigint,
         updated_at = now()
    FROM input i
   WHERE ut.user_id = i.user_id
  RETURNING ut.user_id,
            (ut.u + ut.d)                   AS total_after,
            (ut.u - i.u) + (ut.d - i.d)     AS total_before
),
crossed AS (
  SELECT DISTINCT u.group_id
  FROM upd
  JOIN users u ON u.id = upd.user_id
  WHERE upd.total_before <  u.transfer_enable
    AND upd.total_after  >= u.transfer_enable
),
bumped AS (
  UPDATE node_rev nr
     SET user_rev = nr.user_rev + 1, user_rev_at = now()
   WHERE nr.server_id IN (
     SELECT m.server_id FROM server_group_map m JOIN crossed c ON c.group_id = m.group_id
   )
  RETURNING nr.server_id
)
SELECT upd.user_id, upd.total_before, upd.total_after,
       (SELECT count(*) FROM bumped)::bigint AS bumped_servers
FROM upd
ORDER BY upd.user_id;

-- 日聚合 UPSERT。stat_date 按 Asia/Shanghai 切天（data-model §9.3 的口径声明）。
-- 同样先 GROUP BY 去重：同一批里重复 user_id 会让 ON CONFLICT 报
-- 「cannot affect row a second time」。
-- 🔴 **必须 JOIN user_traffic，不能直接 INSERT 上报里的 user_id。**
--
-- 本查询与 servers.sql 的 AddNodeTrafficBatch **在同一个事务里**，但两者的容错度
-- 曾经不一样：AddNodeTrafficBatch 是 `UPDATE user_traffic ... FROM input`，
-- user_traffic 里没有对应行就静默跳过；而这里是裸 INSERT，
-- stat_user_server.user_id 带 `REFERENCES users(id)` 外键，遇到不存在的 user_id
-- 会抛 23503 并**回滚整个事务** —— 也就是把这一批里其余所有正常用户的流量一起丢掉。
--
-- 上报体完全由节点控制（v2node bug、节点指错环境、节点主机被拿下、
-- DR 从旧备份恢复后节点仍持有较新的用户列表），一条 `{"999999999":[1,1]}` 就够。
-- 而 v2node **不看状态码也不重发**，所以丢的是永久丢；只要那个坏 id 还在，
-- 之后每一批都同样全灭 → 该节点上所有用户可以无限白嫖流量。
--
-- JOIN user_traffic 让两条语句覆盖**完全相同**的 user_id 集合：
-- user_traffic.user_id 本身是 users 的外键，有行就一定有用户，外键必然满足。
-- 未知 user_id 于是与 AddNodeTrafficBatch 一样被静默丢弃，
-- 而调用方靠 AddNodeTrafficBatch 返回的 updated_users < 数组长度发现它（handler 已打日志）。
-- name: BulkUpsertStatUserServer :exec
INSERT INTO stat_user_server (user_id, server_id, stat_date, u, d)
SELECT a.user_id, @server_id::bigint, (now() AT TIME ZONE 'Asia/Shanghai')::date,
       sum(b.u)::bigint, sum(c.d)::bigint
FROM unnest(@user_ids::bigint[])   WITH ORDINALITY AS a(user_id, n)
JOIN unnest(@up_bytes::bigint[])   WITH ORDINALITY AS b(u, n) ON b.n = a.n
JOIN unnest(@down_bytes::bigint[]) WITH ORDINALITY AS c(d, n) ON c.n = a.n
JOIN user_traffic ut ON ut.user_id = a.user_id
GROUP BY a.user_id
ON CONFLICT (user_id, server_id, stat_date)
DO UPDATE SET u = stat_user_server.u + EXCLUDED.u,
              d = stat_user_server.d + EXCLUDED.d;

-- 单用户累加（后台手工调整 / 补账用；正常路径一律走批量版本）
-- name: AddUserTraffic :one
UPDATE user_traffic
SET u = u + $2, d = d + $3,
    u_lifetime = u_lifetime + $2, d_lifetime = d_lifetime + $3,
    online_at = now(), last_node_id = $4, updated_at = now()
WHERE user_id = $1
RETURNING user_id, u, d, u_lifetime, d_lifetime;


-- ============================================================
-- 读：面板与后台
-- ============================================================

-- 单用户 N 天曲线（走主键前缀，≤ 300 行）
-- name: GetUserDailyTraffic :many
SELECT stat_date, u, d
FROM stat_user
WHERE user_id = $1 AND stat_date BETWEEN $2 AND $3
ORDER BY stat_date;

-- 单用户按节点拆分（「我的流量都花在哪个节点上」）
-- name: GetUserTrafficByServer :many
SELECT server_id, sum(u)::bigint AS u, sum(d)::bigint AS d
FROM stat_user_server
WHERE user_id = $1 AND stat_date BETWEEN $2 AND $3
GROUP BY server_id
ORDER BY (sum(u) + sum(d)) DESC;

-- 按节点做出口成本核算（走 stat_user_server_server_date_idx）。
-- ⚠️ 全站年度聚合超过 1 秒就改物化视图（data-model §9.2）。
-- name: GetServerDailyTraffic :many
SELECT stat_date, u, d
FROM stat_server
WHERE server_id = $1 AND stat_date BETWEEN $2 AND $3
ORDER BY stat_date;

-- 全站日报（走 stat_user_server_date_idx）
-- name: GetGlobalDailyTraffic :many
SELECT stat_date, sum(u)::bigint AS u, sum(d)::bigint AS d
FROM stat_user_server
WHERE stat_date BETWEEN $1 AND $2
GROUP BY stat_date
ORDER BY stat_date;

-- 流量榜（后台找异常大户）
-- name: ListTopTrafficUsers :many
SELECT user_id, sum(u)::bigint AS u, sum(d)::bigint AS d
FROM stat_user_server
WHERE stat_date BETWEEN $1 AND $2
GROUP BY user_id
ORDER BY (sum(u) + sum(d)) DESC
LIMIT $3;

-- 3 年硬删（data-model §13）
-- name: CleanupOldStats :execrows
DELETE FROM stat_user_server WHERE stat_date < $1;


-- ============================================================
-- 流量重置（Cloud Scheduler 每分钟触发，命中 users_reset_due_idx）
-- ============================================================

-- name: ListUsersDueForReset :many
SELECT u.id, u.plan_id, u.reset_at, u.reset_seq, u.subscription_anchor_at,
       p.reset_traffic_method, p.transfer_enable AS plan_transfer_enable,
       ut.u, ut.d
FROM users u
JOIN user_traffic ut ON ut.user_id = u.id
LEFT JOIN plans p    ON p.id = u.plan_id
WHERE u.reset_at IS NOT NULL
  AND u.reset_at <= now()
  AND u.deleted_at IS NULL
ORDER BY u.reset_at
LIMIT $1;

-- 重置审计。new_transfer_enable 是**总额**（_plan + _pack），new_transfer_enable_pack 是其中的结转分量。
-- 两个都要写：只留总额的话，「加油包被吃掉了还是结转了」正好落在总额里看不见 ——
-- 而那恰恰是 ADR 0013 ③ 要防的那个静默失败（§5.3：顺序错了会让加油包只增不减，且完全无报错）。
-- 事后要判断结转算没算对，只能靠这两个数配合 old_u/old_d 反推。
-- name: InsertTrafficResetLog :one
INSERT INTO traffic_reset_log (
  user_id, trigger_source, reset_method, old_u, old_d,
  new_transfer_enable, new_transfer_enable_pack, order_id, admin_user_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- 归零本周期，lifetime 不清零。
--
-- 🔴 **只用于管理员手工重置与 reset_pack（`plans.price_reset`）两条路径**（ADR 0013 §5.3 裁决）。
--    周期重置**不要**调它 —— 走 AdvanceUserResetCycle，那条已经把归零并进了同一条语句。
--    原因不是洁癖：这两条语句曾经是 Querier 上两个互不相干的方法，**顺序没有定死**，
--    而先跑本语句（u=0, d=0）会让 carry_pack 恒等于 transfer_enable_pack，
--    于是**加油包永远不被消耗、只增不减，且完全静默**。合并之后那个错误不可表达，
--    但前提是周期重置这条路径上不再有人单独调用本语句。
--
--    这两条路径的语义与周期重置也确实不同：它们只清当期用量、**不动配额、不推进 reset_seq**
--    （reset_pack 卖的就是「把 u/d 清零」，不卖时间也不卖配额）。
-- name: ResetUserTraffic :one
UPDATE user_traffic
SET u_lifetime = u_lifetime + u,
    d_lifetime = d_lifetime + d,
    u = 0, d = 0, updated_at = now()
WHERE user_id = $1
RETURNING *;

-- 周期重置：归零当期用量 + 推进重置时刻 + 重发套餐配额 + 结转加油包，**一条语句做完**。
--
-- 下一次重置时刻从固定锚点乘算，避免月末漂移（data-model §6.2）：
-- 2026-01-31 + 1month + 1month = 2026-03-28（漂移）；anchor + 2*1month = 2026-03-31（不漂移）。
--
-- 🔴 **为什么归零必须并进来，而不是让调用方先跑 ResetUserTraffic**（ADR 0013 §5.3）：
--    carry_pack 要用「本周期已用量」算，而 ResetUserTraffic 会把它清成 0。
--    两条语句放在 Querier 上就是两个互不相干的方法，**先跑哪条无从表达**；
--    先跑归零的那个顺序会让 carry_pack 恒等于 transfer_enable_pack，
--    于是加油包只增不减 —— 而且**完全静默**，没有报错、没有告警，只有账对不上。
--    合并成一条 CTE 之后，「顺序」这件事在类型层面不存在了。
--    cur 上的 FOR UPDATE 取的是同一个快照，同时挡住并发的节点上报把 u/d 改到中途。
--
-- 消耗顺序是先套餐后加油包（ADR 0013 §5.3）：套餐配额**会过期**（本语句就在清它），
-- 加油包**会结转**。先消耗会过期的那份，对用户永远不亏。所以
--   pack_used  = greatest(0, (u + d) - transfer_enable_plan)
--   carry_pack = greatest(0, transfer_enable_pack - pack_used)
-- 外层那个 greatest(0, …) 不是防御性代码：v2node 每 60 秒才上报一次，
-- u+d 会**越过** transfer_enable 若干字节才被判定耗尽，pack_used 因此可能大于 transfer_enable_pack。
--
-- ⚠️ transfer_enable_plan 与 transfer_enable_pack 都在 users_bump_user_rev_trg 的监视列表里，
--    所以本语句自己就会 bump user_rev，配额恢复会自动传播到节点 ——
--    调用方**不需要**再显式 BumpUserRevForUser（0016 之前需要，那条要求已随本语句作废）。
--    唯一的例外是 reset_traffic_method = 'never'：两列都不变时不 bump，而那种用户本来也不需要。
-- name: AdvanceUserResetCycle :one
WITH cur AS (
  SELECT ut.user_id, ut.u, ut.d
  FROM user_traffic ut
  WHERE ut.user_id = $1
  FOR UPDATE
), zeroed AS (
  UPDATE user_traffic ut
  SET u_lifetime = ut.u_lifetime + cur.u,
      d_lifetime = ut.d_lifetime + cur.d,
      u = 0, d = 0, updated_at = now()
  FROM cur WHERE ut.user_id = cur.user_id
  RETURNING ut.user_id
)
UPDATE users u SET
  reset_seq = u.reset_seq + 1,
  reset_at = CASE p.reset_traffic_method
    WHEN 'never'                THEN NULL
    WHEN 'monthly_first'        THEN date_trunc('month', now()) + interval '1 month'
    WHEN 'yearly_jan_first'     THEN date_trunc('year',  now()) + interval '1 year'
    WHEN 'monthly_on_order_day' THEN u.subscription_anchor_at + (u.reset_seq + 1) * interval '1 month'
    WHEN 'yearly_on_order_day'  THEN u.subscription_anchor_at + (u.reset_seq + 1) * interval '1 year'
    ELSE u.subscription_anchor_at + (u.reset_seq + 1) * interval '1 month'  -- follow_system
  END,
  transfer_enable_plan = p.transfer_enable,                        -- 只覆盖套餐分量
  transfer_enable_pack = greatest(0, u.transfer_enable_pack
                          - greatest(0, (cur.u + cur.d) - u.transfer_enable_plan)),   -- carry_pack
  updated_at = now()
FROM plans p, cur
WHERE p.id = u.plan_id AND u.id = cur.user_id
RETURNING u.id, u.reset_seq, u.reset_at,
          u.transfer_enable_plan, u.transfer_enable_pack, u.transfer_enable,
          cur.u AS old_u, cur.d AS old_d;   -- 直接喂给 InsertTrafficResetLog，不用二次查询

-- name: ListTrafficResetLog :many
SELECT * FROM traffic_reset_log
WHERE user_id = $1
ORDER BY reset_at DESC
LIMIT $2 OFFSET $3;


-- ============================================================
-- 到期扫描 —— **已迁走，本文件不再有这条查询**
-- ============================================================
--
-- 曾经的 `MarkExpiredUsers` 已删除，唯一实现是 tasks.sql 的 `SweepExpiredUsers`。
-- 两条 UPDATE 语义完全重合（同样的 SET、同样的四个 WHERE 条件），只差 RETURNING：
-- 后者多带 `group_id`，而 group_id 正是 expire-check 显式 bump node_rev 唯一需要的东西。
--
-- 🔴 **留下这段墓碑而不是静静删掉**，是因为「到期不是写操作、必须靠扫描把时间流逝变成一次写」
--    这条推理会让下一个人在 stats.sql 里找不到它时以为它根本不存在，然后照着
--    data-model §8.4 补全 2 再写一条 —— 而两条并存的后果不是重复劳动：
--    两个调用方会各自扫到一半的行（第一条把 expiry_applied_at 写非 NULL，
--    第二条的 `expiry_applied_at IS NULL` 就再也匹配不到），
--    于是**只有其中一条的调用方会去 bump node_rev**，另一半用户到期后节点收 304、
--    永远不知道该踢掉他们。完整推理在 tasks.sql 的 SweepExpiredUsers 注释里。
