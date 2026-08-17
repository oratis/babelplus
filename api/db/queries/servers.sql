-- servers.sql · 节点面（UniProxy 契约）与节点管理
--
-- 事实源：api-contract.md §3（冻结契约）、data-model.md §8 / §12.1 / §12.2
--
-- 🔴 全系统唯一的高频读路径在本文件里：每节点每 60 秒 4 次请求。
--    稳态命中 304 时只做两次索引查找（AuthenticateServerKey + GetNodeRev），
--    **不碰 users 与 user_traffic**。

-- ============================================================
-- 鉴权：Bearer 节点密钥（data-model §12.1 步 1）
-- ============================================================

-- 走 server_keys_hash_uk 唯一索引 —— 应用层不存在逐字节比较密钥的地方，时序信道天然消失。
-- ⚠️ scopes 白名单的判定放在中间件（硬编码路由 → scope 映射），不在 SQL 里做前缀匹配。
-- name: AuthenticateServerKey :one
SELECT
  k.id            AS key_id,
  k.server_id,
  k.scopes,
  k.key_prefix,
  k.expires_at,
  s.code          AS server_code,
  s.enabled       AS server_enabled
FROM server_keys k
JOIN servers s ON s.id = k.server_id
WHERE k.key_hash = $1
  AND k.revoked_at IS NULL
  AND (k.expires_at IS NULL OR k.expires_at > now())
  AND s.deleted_at IS NULL;

-- 密钥使用留痕。刻意与鉴权分开：鉴权在读路径上必须零写，
-- 这条由 handler 异步（或降采样）调用，写失败不影响请求。
-- name: TouchServerKey :exec
UPDATE server_keys
SET last_used_at = now(), last_used_ip = $2
WHERE id = $1;

-- name: ListServerKeys :many
SELECT id, server_id, name, key_prefix, scopes, issued_at, expires_at,
       last_used_at, last_used_ip, revoked_at, revoked_reason, created_by
FROM server_keys
WHERE server_id = $1
ORDER BY issued_at DESC;

-- name: CreateServerKey :one
INSERT INTO server_keys (server_id, name, key_prefix, key_hash, scopes, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- ⚠️ 轮换强制两步（data-model §8.3）：先 CreateServerKey → 确认节点已用新密钥上报
--    → 才 RevokeServerKey 旧的。一步完成 = 节点在下一次 60 秒轮询时失联。
-- name: RevokeServerKey :one
UPDATE server_keys
SET revoked_at = now(), revoked_reason = $2
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- 巡检：同一节点同时有效的密钥 > 2（data-model §8.3 的应用层规则，用这条兜底告警）
-- name: CountActiveServerKeysPerServer :many
SELECT server_id, count(*) AS active_keys
FROM server_keys
WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
GROUP BY server_id
HAVING count(*) > 2;


-- ============================================================
-- ETag：版本号驱动，不哈希响应体（data-model §12.1 步 2 / api-contract §3.8）
-- ETag 形状 W/"<node_id>-c<config_rev>" 与 W/"<node_id>-u<user_rev>"，两个端点各用各的
-- ============================================================

-- name: GetNodeRev :one
SELECT server_id, config_rev, user_rev, config_rev_at, user_rev_at
FROM node_rev
WHERE server_id = $1;

-- name: InitNodeRev :one
INSERT INTO node_rev (server_id) VALUES ($1)
ON CONFLICT (server_id) DO UPDATE SET server_id = EXCLUDED.server_id
RETURNING *;

-- 改节点配置后必须调用（改 host/port/protocol_settings 等）
-- name: BumpConfigRev :one
UPDATE node_rev
SET config_rev = config_rev + 1, config_rev_at = now()
WHERE server_id = $1
RETURNING config_rev, config_rev_at;

-- 按分组 bump user_rev。与 migration 0012 的 bump_user_rev() 同义，
-- 供不经过 users 触发器的路径（如 server_group_map 变更）显式调用。
-- name: BumpUserRevByGroup :exec
UPDATE node_rev
SET user_rev = user_rev + 1, user_rev_at = now()
WHERE server_id IN (SELECT server_id FROM server_group_map WHERE group_id = $1);

-- name: BumpUserRevForUser :exec
UPDATE node_rev
SET user_rev = user_rev + 1, user_rev_at = now()
WHERE server_id IN (
  SELECT m.server_id FROM server_group_map m
  JOIN users u ON u.group_id = m.group_id
  WHERE u.id = $1
);


-- ============================================================
-- GET /api/v1/server/UniProxy/config — 节点自身配置
-- ============================================================

-- protocol_settings 里放 REALITY 私钥 / short_id / obfs-password 等协议细节，
-- 由 handler 按 protocol 展开成 Xboard 形状的裸 JSON（不加响应信封）。
-- name: GetServerConfig :one
SELECT
  s.id, s.code, s.name, s.protocol,
  s.host, s.port, s.server_port, s.region,
  s.parent_id, s.protocol_settings, s.tags,
  s.enabled, s.visible, s.sort_order
FROM servers s
WHERE s.id = $1 AND s.deleted_at IS NULL;


-- ============================================================
-- GET /api/v1/server/UniProxy/user — 该节点的用户列表
-- ============================================================

-- 可用性判定一条 SQL 覆盖（data-model §1.2 裁决 1 / api-contract §3.4）：
--   u + d < transfer_enable
--   AND (expired_at IS NULL OR expired_at > now())
--   AND NOT banned
--   AND group_id ∈ 该节点所属分组
--
-- ⚠️ coalesce(u.expired_at,'infinity'::timestamptz) > now() 必须与 users_available_idx
--    的表达式**逐字同形**，否则索引不会被用上。写成 `expired_at IS NULL OR ...` 的 OR
--    会让规划器直接放弃索引。
-- ⚠️ ut.u + ut.d < u.transfer_enable 跨表跨列，无法索引，只能过滤 —— 这是已知且接受的。
-- name: ListAvailableUsersByServer :many
SELECT
  u.id,
  u.uuid,
  u.speed_limit_mbps,
  u.device_limit
FROM users u
JOIN user_traffic ut    ON ut.user_id = u.id
JOIN server_group_map m ON m.group_id = u.group_id
WHERE m.server_id = $1
  AND u.banned = false
  AND u.deleted_at IS NULL
  AND coalesce(u.expired_at, 'infinity'::timestamptz) > now()
  AND ut.u + ut.d < u.transfer_enable
ORDER BY u.id;


-- ============================================================
-- POST /api/v1/server/UniProxy/alive — 在线设备上报（UNLOGGED 表）
-- GET  /api/v1/server/UniProxy/alivelist
-- ============================================================

-- 批量 upsert。device_ips 用 text[] 传入后在 SQL 里转 inet：
-- 避免 inet[] 在 pgx/sqlc 两侧的映射分歧，代价是一次显式 cast。
--
-- ⚠️ 两个数组用 WITH ORDINALITY 按下标配对，而不是 unnest(a, b)：
--    sqlc v1.31 的内建 catalog **没有多参数 unnest**，写成 unnest(a,b) 会在
--    `sqlc generate` 阶段直接报 `function unnest(unknown, unknown) does not exist`。
--    调用方必须保证两个数组等长（Go 侧 assert）。
-- ⚠️ DISTINCT 不能少：同一批里重复的 (user_id, ip) 会让 ON CONFLICT DO UPDATE 报
--    「cannot affect row a second time」。
-- ⚠️ 口径是**按 IP**，不是按设备（data-model §8.5 / §15.5）。
-- name: BulkUpsertUserDeviceState :exec
INSERT INTO user_device_state (user_id, server_id, device_ip, last_seen_at)
SELECT DISTINCT a.user_id, @server_id::bigint, b.ip::inet, now()
FROM unnest(@user_ids::bigint[])  WITH ORDINALITY AS a(user_id, n)
JOIN unnest(@device_ips::text[])  WITH ORDINALITY AS b(ip, n) ON b.n = a.n
ON CONFLICT (user_id, server_id, device_ip)
DO UPDATE SET last_seen_at = now();

-- 响应只含 device_limit IS NOT NULL 的用户（api-contract §3.7）
-- name: ListAliveDeviceCounts :many
SELECT s.user_id, count(*)::bigint AS alive
FROM user_device_state s
JOIN users u ON u.id = s.user_id
WHERE s.last_seen_at > now() - interval '2 minutes'
  AND u.device_limit IS NOT NULL
GROUP BY s.user_id;

-- 某用户的在线设备（面板「在线设备」列表 / 踢下线前的展示）
-- name: ListUserDevices :many
SELECT user_id, server_id, device_ip, last_seen_at
FROM user_device_state
WHERE user_id = $1 AND last_seen_at > now() - interval '5 minutes'
ORDER BY last_seen_at DESC;

-- name: DeleteUserDevice :exec
DELETE FROM user_device_state
WHERE user_id = $1 AND server_id = $2 AND device_ip = $3;

-- 全部下线。⚠️ 生效有延迟（最长一个 push 周期 = 60 秒），响应必须带这条提示。
-- name: DeleteAllUserDevices :exec
DELETE FROM user_device_state WHERE user_id = $1;

-- Cloud Scheduler 每 5 分钟跑（data-model §8.5）
-- name: CleanupStaleDeviceState :execrows
DELETE FROM user_device_state WHERE last_seen_at < now() - interval '5 minutes';


-- ============================================================
-- POST /api/v1/server/UniProxy/status — 节点负载（只写快照，不建历史表）
-- ============================================================

-- 校验（cpu 0–100、其余 ≥ 0）在 handler 做，schema 只保住 cpu_pct 的区间。
-- name: UpsertServerOnlineState :exec
INSERT INTO server_online_state (
  server_id, online_users, cpu_pct,
  mem_total, mem_used, swap_total, swap_used, disk_total, disk_used, reported_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
ON CONFLICT (server_id) DO UPDATE SET
  online_users = EXCLUDED.online_users,
  cpu_pct      = EXCLUDED.cpu_pct,
  mem_total    = EXCLUDED.mem_total,
  mem_used     = EXCLUDED.mem_used,
  swap_total   = EXCLUDED.swap_total,
  swap_used    = EXCLUDED.swap_used,
  disk_total   = EXCLUDED.disk_total,
  disk_used    = EXCLUDED.disk_used,
  reported_at  = now();

-- /push 到达时记一次心跳（online_users 由 status 上报覆盖，这里只动 last_push_at）
-- name: TouchServerPush :exec
INSERT INTO server_online_state (server_id, last_push_at, reported_at)
VALUES ($1, now(), now())
ON CONFLICT (server_id) DO UPDATE SET last_push_at = now();

-- name: GetServerOnlineState :one
SELECT * FROM server_online_state WHERE server_id = $1;

-- name: ListServerOnlineState :many
SELECT * FROM server_online_state ORDER BY server_id;


-- ============================================================
-- 节点管理（后台）
-- ============================================================

-- name: GetServer :one
SELECT * FROM servers WHERE id = $1 AND deleted_at IS NULL;

-- name: GetServerByCode :one
SELECT * FROM servers WHERE code = $1 AND deleted_at IS NULL;

-- name: ListServers :many
SELECT * FROM servers
WHERE deleted_at IS NULL
ORDER BY sort_order, id;

-- 用户订阅里出现的节点（走 servers_visible_idx）
-- name: ListVisibleServersForUser :many
SELECT s.*
FROM servers s
JOIN server_group_map m ON m.server_id = s.id
WHERE m.group_id = $1
  AND s.visible = true AND s.enabled = true AND s.deleted_at IS NULL
ORDER BY s.sort_order, s.id;

-- name: CreateServer :one
INSERT INTO servers (
  code, name, protocol, host, port, server_port, region,
  parent_id, protocol_settings, tags, enabled, visible, sort_order
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- ⚠️ 调用方必须在同一事务里 BumpConfigRev，否则节点会一直拿旧配置的 304。
-- name: UpdateServer :one
UPDATE servers SET
  name = $2, protocol = $3, host = $4, port = $5, server_port = $6, region = $7,
  parent_id = $8, protocol_settings = $9, tags = $10,
  enabled = $11, visible = $12, sort_order = $13, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- 只软删：stat_user_server.server_id 是 ON DELETE RESTRICT，成本历史不能因为删节点而消失
-- name: SoftDeleteServer :exec
UPDATE servers SET deleted_at = now(), enabled = false, visible = false, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;


-- ============================================================
-- 节点分组
-- ============================================================

-- name: ListServerGroups :many
SELECT * FROM server_groups ORDER BY id;

-- name: GetServerGroupByCode :one
SELECT * FROM server_groups WHERE code = $1;

-- name: CreateServerGroup :one
INSERT INTO server_groups (code, name) VALUES ($1, $2) RETURNING *;

-- ⚠️ 调用方必须随后 BumpUserRevByGroup —— 分组成员变化改变了节点可见用户集合，
--    而 server_group_map 上没有触发器。
-- name: AddServerToGroup :exec
INSERT INTO server_group_map (server_id, group_id) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: RemoveServerFromGroup :exec
DELETE FROM server_group_map WHERE server_id = $1 AND group_id = $2;

-- name: ListGroupsOfServer :many
SELECT g.* FROM server_groups g
JOIN server_group_map m ON m.group_id = g.id
WHERE m.server_id = $1
ORDER BY g.id;
