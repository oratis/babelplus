-- subscriptions.sql · 订阅 token 与拉取审计
--
-- 事实源：data-model.md §5
--
-- 🔴 token 独立成表、可多条、可命名、可单独吊销；DB 只存哈希（查找）+ 密文（展示）。
-- 🔴 每次拉取都写 subscription_fetch_log —— 这是唯一能识别「账号共享」的数据来源。

-- ============================================================
-- token 校验（订阅拉取的唯一入口）
-- ============================================================

-- 完整判定（data-model §5.1）。四个条件缺一不可：
--   1. token 自身未吊销、未过期
--   2. users.sub_revoked_at 的一键全撤：早于此刻签发的 token 全失效（语义抄 Marzban，
--      不更换标识符即可让全部旧链接失效）
--   3. 用户未注销
-- ⚠️ token 不存在与 token 无效必须返回同一个 404（ADR 0006），不给枚举留信号。
-- name: ResolveSubscriptionToken :one
SELECT
  t.id            AS token_id,
  t.user_id,
  t.name          AS token_name,
  t.token_prefix,
  t.issued_at,
  u.uuid,
  u.group_id,
  u.plan_id,
  u.transfer_enable,
  u.expired_at,
  u.device_limit,
  u.speed_limit_mbps,
  u.banned
FROM subscription_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.token_hash = $1
  AND t.revoked_at IS NULL
  AND (t.expires_at IS NULL OR t.expires_at > now())
  AND (u.sub_revoked_at IS NULL OR t.issued_at > u.sub_revoked_at)
  AND u.deleted_at IS NULL;

-- 拉取成功后留痕（与写 fetch_log 分开：这条允许失败）
-- name: TouchSubscriptionToken :exec
UPDATE subscription_tokens
SET last_used_at = now(), last_used_ip = $2
WHERE id = $1;


-- ============================================================
-- token 管理（面板）
-- ============================================================

-- token_enc 是 AES-256-GCM 密文，密钥在 Secret Manager 不落 DB。
-- 需要可逆加密而不是只存哈希的理由（data-model §5.2）：ADR 0002 的失联恢复靠邮件广播
-- 新域名，用户拿到新域名后自己拼 https://{新域名}/s/{token}。token 若不可再次展示，
-- 每换一次域名就要给所有用户重签 —— 恢复面的核心机制会因为一个存储决策而失效。
-- name: CreateSubscriptionToken :one
INSERT INTO subscription_tokens (user_id, token_hash, token_enc, token_prefix, name, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListSubscriptionTokens :many
SELECT id, user_id, token_enc, token_prefix, name,
       issued_at, expires_at, last_used_at, last_used_ip, created_at
FROM subscription_tokens
WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: GetSubscriptionToken :one
SELECT * FROM subscription_tokens WHERE id = $1 AND user_id = $2;

-- 粒度一：吊销单条 —— 只有那一台设备要重新导入
-- name: RevokeSubscriptionToken :one
UPDATE subscription_tokens
SET revoked_at = now(), revoked_reason = $3
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
RETURNING *;

-- 粒度二：一键全撤 —— 🔴 **所有设备都要重新导入**，确认弹窗必须写死这句话（page-inventory §3.2）。
-- ⚠️ 只写 users.sub_revoked_at，不动 subscription_tokens 的行：吊销本身是证据，删行等于毁证。
-- name: RevokeAllSubscriptions :one
UPDATE users SET sub_revoked_at = now(), updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING id, sub_revoked_at;


-- ============================================================
-- 拉取审计
-- ============================================================

-- 🔴 每次拉取都写一条，包括 304 与 404。status_code / client_flag / node_count
--    是事后判断「他到底拿到了什么」的全部依据。
-- name: InsertSubscriptionFetchLog :one
INSERT INTO subscription_fetch_log (
  user_id, token_id, request_ip, user_agent, client_flag, status_code, format, node_count
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, request_at;

-- 面板「最近 N 次拉取」（时间 / IP / UA）。边际成本为零，
-- 但让用户自己就能发现订阅被白嫖并自助重置，不用开工单（page-inventory §3.2.7）。
-- name: ListSubscriptionFetchLog :many
SELECT id, token_id, request_ip, user_agent, client_flag, status_code, format, node_count, request_at
FROM subscription_fetch_log
WHERE user_id = $1
ORDER BY request_at DESC
LIMIT $2 OFFSET $3;

-- 共享检测（data-model §5.3）。
-- ⚠️ 阈值是参数不是常量，因为 20 只是占位数字：中国移动网络公网 IP 变动极频繁，
--    单个正常用户 7 天内出现几十个不同 IP 完全可能。**必须先采基线再定阈值。需实测。**
-- name: DetectSubscriptionSharing :many
SELECT
  u.id AS user_id,
  u.email,
  count(*)::bigint                      AS fetches,
  count(DISTINCT f.request_ip)::bigint  AS distinct_ips,
  count(DISTINCT f.client_flag)::bigint AS distinct_clients
FROM subscription_fetch_log f
JOIN users u ON u.id = f.user_id
WHERE f.request_at > now() - ($1::interval)
  AND f.status_code = 200
GROUP BY u.id, u.email
HAVING count(DISTINCT f.request_ip) > $2::bigint
ORDER BY count(DISTINCT f.request_ip) DESC;

-- 全库唯一需要定时清理的持久表：90 天（data-model §5.4）。
-- Cloud Scheduler 每天跑一次。
-- name: CleanupOldSubscriptionFetchLog :execrows
DELETE FROM subscription_fetch_log WHERE request_at < now() - ($1::interval);
