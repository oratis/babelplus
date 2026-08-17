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
-- name: BulkUpsertStatUserServer :exec
INSERT INTO stat_user_server (user_id, server_id, stat_date, u, d)
SELECT a.user_id, @server_id::bigint, (now() AT TIME ZONE 'Asia/Shanghai')::date,
       sum(b.u)::bigint, sum(c.d)::bigint
FROM unnest(@user_ids::bigint[])   WITH ORDINALITY AS a(user_id, n)
JOIN unnest(@up_bytes::bigint[])   WITH ORDINALITY AS b(u, n) ON b.n = a.n
JOIN unnest(@down_bytes::bigint[]) WITH ORDINALITY AS c(d, n) ON c.n = a.n
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

-- name: InsertTrafficResetLog :one
INSERT INTO traffic_reset_log (
  user_id, trigger_source, reset_method, old_u, old_d, new_transfer_enable, order_id, admin_user_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- 归零本周期，lifetime 不清零
-- name: ResetUserTraffic :one
UPDATE user_traffic
SET u_lifetime = u_lifetime + u,
    d_lifetime = d_lifetime + d,
    u = 0, d = 0, updated_at = now()
WHERE user_id = $1
RETURNING *;

-- 下一次重置时刻：从固定锚点乘算，避免月末漂移（data-model §6.2）。
-- 2026-01-31 + 1month + 1month = 2026-03-28（漂移）；anchor + 2*1month = 2026-03-31（不漂移）。
-- ⚠️ 本语句会触发 users_bump_user_rev_trg，但只改 reset_seq/reset_at 这两列，
--    不在触发器的监视列表里 —— 所以配额恢复**不会**自动传播到节点，
--    调用方必须显式 BumpUserRevForUser（data-model §6.2 的 ⚠️）。
-- name: AdvanceUserResetCycle :one
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
  transfer_enable = p.transfer_enable,
  updated_at = now()
FROM plans p
WHERE p.id = u.plan_id AND u.id = $1
RETURNING u.id, u.reset_seq, u.reset_at, u.transfer_enable;

-- name: ListTrafficResetLog :many
SELECT * FROM traffic_reset_log
WHERE user_id = $1
ORDER BY reset_at DESC
LIMIT $2 OFFSET $3;


-- ============================================================
-- 到期扫描（Cloud Scheduler 每分钟，命中 users_expiry_due_idx，平时 0 行）
-- ============================================================

-- 🔴 到期本身不产生任何写操作 —— 没有写就没有触发点。
--    这条 UPDATE 把「时间流逝」变成一次写，expiry_applied_at 进了触发器的监视列表，
--    因此它会自动 bump user_rev（data-model §8.4 补全 2）。
-- name: MarkExpiredUsers :many
UPDATE users
SET expiry_applied_at = now(), updated_at = now()
WHERE expired_at IS NOT NULL
  AND expired_at <= now()
  AND expiry_applied_at IS NULL
  AND deleted_at IS NULL
RETURNING id, email, expired_at;
