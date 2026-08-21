-- users.sql · 账户、会话、邀请码、邮箱验证
--
-- 事实源：data-model.md §4 / §13
--
-- 🔴 users 永不硬删（data-model §1.2 裁决 8）：「删除账号」= 匿名化。
--    订单、账、统计的外键因此永不悬空。

-- ============================================================
-- 注册与登录
-- ============================================================

-- ⚠️ 走 users_email_uk (lower(email))。PG 的 text 区分大小写，
--    这里必须 lower() 两侧同形，否则同一邮箱能注册两次（ADR 0005 点名的陷阱）。
-- name: GetUserByEmail :one
SELECT * FROM users
WHERE lower(email) = lower($1) AND deleted_at IS NULL;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: GetUserByUUID :one
SELECT * FROM users WHERE uuid = $1 AND deleted_at IS NULL;

-- 新注册用户 transfer_enable = 0，可用性判定里 0 + 0 < 0 为假 —— 他天然被排除在节点用户列表外。
-- 配额本身就是准入开关，不需要再加一个「已付费」布尔列（data-model §4.1）。
-- name: CreateUser :one
INSERT INTO users (email, password_hash, group_id, invited_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- 1:1 热写拆表必须同事务创建，否则 ListAvailableUsersByServer 的 JOIN 永远查不到他
-- name: CreateUserTraffic :one
INSERT INTO user_traffic (user_id) VALUES ($1)
ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
RETURNING *;

-- name: MarkEmailVerified :exec
UPDATE users SET email_verified_at = now(), updated_at = now()
WHERE id = $1 AND email_verified_at IS NULL;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, password_algo = $3, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: TouchUserLogin :exec
UPDATE users SET last_login_at = now(), last_login_ip = $2, updated_at = now()
WHERE id = $1;


-- ============================================================
-- 订阅权利（改这些列会触发 users_bump_user_rev_trg 自动传播到节点）
-- ============================================================

-- name: GetUserWithTraffic :one
SELECT
  u.id, u.email, u.uuid, u.plan_id, u.group_id,
  u.transfer_enable, u.expired_at, u.speed_limit_mbps, u.device_limit,
  u.banned, u.banned_reason, u.banned_at,
  u.subscription_anchor_at, u.reset_seq, u.reset_at,
  u.sub_revoked_at, u.email_verified_at,
  u.notify_expire, u.notify_traffic,
  u.created_at,
  ut.u, ut.d, ut.u_lifetime, ut.d_lifetime, ut.online_at, ut.last_node_id
FROM users u
JOIN user_traffic ut ON ut.user_id = u.id
WHERE u.id = $1 AND u.deleted_at IS NULL;

-- 开通 / 续费 / 升级后写权利。subscription_anchor_at 首次开通才写，续费与升级都不改
-- —— 从固定锚点乘算重置日才不会月末漂移（data-model §6.2）。
-- name: ApplyUserEntitlement :one
UPDATE users SET
  plan_id                = $2,
  group_id               = $3,
  transfer_enable        = $4,
  expired_at             = $5,
  speed_limit_mbps       = $6,
  device_limit           = $7,
  subscription_anchor_at = coalesce(users.subscription_anchor_at, $8),
  reset_at               = $9,
  expiry_applied_at      = NULL,
  updated_at             = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- 流量包：只叠加配额。
-- ⚠️ 已知缺口（data-model §16）：当前 transfer_enable 是单列，周期重置时会被套餐值整个覆盖，
--    所以流量包在重置时**保不住**。修复需要拆成 transfer_enable_plan + transfer_enable_pack
--    两列，而那依赖一条尚未裁决的产品规则（user-journey §10.1 标为待裁决）。
-- name: AddUserTransferQuota :one
UPDATE users SET transfer_enable = transfer_enable + $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING id, transfer_enable;

-- banned 与 banned_at 由 users_banned_consistency 绑死：「什么时候被封的」永远可查
-- name: BanUser :one
UPDATE users SET banned = true, banned_reason = $2, banned_at = now(), updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: UnbanUser :one
UPDATE users SET banned = false, banned_reason = NULL, banned_at = NULL, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: UpdateUserNotifyPrefs :exec
UPDATE users SET notify_expire = $2, notify_traffic = $3, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: UpdateUserRemarks :exec
UPDATE users SET remarks = $2, updated_at = now() WHERE id = $1;


-- ============================================================
-- 后台列表与匿名化
-- ============================================================

-- ⚠️ sqlc 不做动态查询（ADR 0006 §15.4）。后台的多条件任意组合筛选
--    要么退化成手写 pgx，要么用这种「NULL = 不过滤」的固定形状。这里选后者。
-- name: ListUsersForAdmin :many
SELECT
  u.id, u.email, u.plan_id, u.group_id, u.transfer_enable, u.expired_at,
  u.banned, u.created_at, u.last_login_at,
  ut.u, ut.d
FROM users u
JOIN user_traffic ut ON ut.user_id = u.id
WHERE u.deleted_at IS NULL
  AND (sqlc.narg(plan_id)::bigint  IS NULL OR u.plan_id = sqlc.narg(plan_id)::bigint)
  AND (sqlc.narg(banned)::boolean  IS NULL OR u.banned  = sqlc.narg(banned)::boolean)
  AND (sqlc.narg(email_like)::text IS NULL OR u.email ILIKE sqlc.narg(email_like)::text)
ORDER BY u.id DESC
LIMIT $1 OFFSET $2;

-- name: CountUsersForAdmin :one
SELECT count(*)::bigint FROM users u
WHERE u.deleted_at IS NULL
  AND (sqlc.narg(plan_id)::bigint  IS NULL OR u.plan_id = sqlc.narg(plan_id)::bigint)
  AND (sqlc.narg(banned)::boolean  IS NULL OR u.banned  = sqlc.narg(banned)::boolean)
  AND (sqlc.narg(email_like)::text IS NULL OR u.email ILIKE sqlc.narg(email_like)::text);

-- 「删除账号」= 匿名化，不是 DELETE（data-model §13 / §13.1）。
-- 可识别信息被抹掉，行本身留下 —— 既满足「按请求删除个人数据」的实质，又不破坏账与统计。
-- name: AnonymizeUser :one
UPDATE users SET
  email         = 'deleted+' || id::text || '@invalid',
  password_hash = '',
  remarks       = NULL,
  last_login_ip = NULL,
  banned        = true,
  banned_reason = 'account_deleted',
  banned_at     = coalesce(banned_at, now()),
  deleted_at    = now(),
  updated_at    = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING id, email, deleted_at;


-- ============================================================
-- 会话（refresh token 轮换链）
-- ============================================================

-- ⚠️ access JWT 在其有效期内不可撤销（data-model §4.1）。「全部登出」= 撤销全部
--    user_sessions 行 + 最长一个 access token TTL（建议 15 分钟）后真正生效。
-- name: CreateUserSession :one
INSERT INTO user_sessions (user_id, refresh_hash, user_agent, created_ip, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetUserSessionByHash :one
SELECT s.*, u.banned, u.deleted_at AS user_deleted_at
FROM user_sessions s
JOIN users u ON u.id = s.user_id
WHERE s.refresh_hash = $1
  AND s.revoked_at IS NULL
  AND s.expires_at > now();

-- 轮换：撤旧的并指向新的
-- name: RotateUserSession :exec
UPDATE user_sessions
SET revoked_at = now(), replaced_by_id = $2, last_used_at = now()
WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeUserSession :exec
UPDATE user_sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeAllUserSessions :execrows
UPDATE user_sessions SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: ListUserSessions :many
SELECT id, user_agent, created_ip, issued_at, last_used_at, expires_at
FROM user_sessions
WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
ORDER BY expires_at DESC;

-- 过期后 30 天硬删（data-model §13）
-- name: CleanupExpiredSessions :execrows
DELETE FROM user_sessions WHERE expires_at < now() - interval '30 days';


-- ============================================================
-- 邮箱验证码 / 找回密码
-- ============================================================

-- name: CreateEmailVerification :one
INSERT INTO email_verifications (email, user_id, purpose, code_hash, expires_at, request_ip)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetLatestEmailVerification :one
SELECT * FROM email_verifications
WHERE lower(email) = lower($1) AND purpose = $2 AND consumed_at IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: IncrementVerificationAttempts :one
UPDATE email_verifications SET attempts = attempts + 1
WHERE id = $1
RETURNING id, attempts, max_attempts;

-- name: ConsumeEmailVerification :exec
UPDATE email_verifications SET consumed_at = now()
WHERE id = $1 AND consumed_at IS NULL;

-- 30 天硬删（data-model §13）
-- name: CleanupOldEmailVerifications :execrows
DELETE FROM email_verifications WHERE created_at < now() - interval '30 days';


-- ============================================================
-- 邀请码
-- ============================================================

-- name: GetInviteCodeByCode :one
SELECT * FROM invite_codes
WHERE code = $1
  AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > now())
  AND used_count < max_uses;

-- ⚠️ invite_codes_user_single_use 约束把「用户码恒为一次性核销」写进了数据库：
--    owner_user_id IS NOT NULL 时 max_uses 必须 = 1。将来任何一处代码想放宽都会被拒绝。
-- name: CreateInviteCode :one
INSERT INTO invite_codes (code, owner_user_id, max_uses, expires_at, note)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- 核销：used_count 的自增与 invite_code_uses 的插入必须同事务，
-- invite_codes_uses CHECK 保证 used_count 不会越过 max_uses
-- name: RedeemInviteCode :one
UPDATE invite_codes SET used_count = used_count + 1
WHERE id = $1 AND used_count < max_uses AND revoked_at IS NULL
RETURNING *;

-- name: RecordInviteCodeUse :one
INSERT INTO invite_code_uses (invite_code_id, user_id, request_ip)
VALUES ($1, $2, $3)
RETURNING *;

-- name: ListInviteCodesByOwner :many
SELECT * FROM invite_codes
WHERE owner_user_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: RevokeInviteCode :exec
UPDATE invite_codes SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- 巡检：「每用户未核销码 ≤ 3」无法用声明式约束表达（PG 没有跨行 CHECK），
-- 落在应用层 + 这条巡检 SQL（data-model §4.1）
-- name: FindUsersOverInviteCodeQuota :many
SELECT owner_user_id, count(*)::bigint AS unused
FROM invite_codes
WHERE owner_user_id IS NOT NULL AND revoked_at IS NULL AND used_count = 0
GROUP BY owner_user_id
HAVING count(*) > 3;

-- name: ListInvitedUsers :many
SELECT id, email, created_at FROM users
WHERE invited_by = $1 AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;


-- ============================================================
-- 账户体系补充（internal/handler/auth.go 用）
-- ============================================================

-- 与 GetInviteCodeByCode 的区别：**不过滤任何状态**。
-- GET /api/v1/invite/verify 必须区分「无效」与「已用尽」（api-contract §5.1：
-- 两者的用户动作完全不同 —— 前者去要一个新码，后者去催邀请人再生成一个），
-- 而 GetInviteCodeByCode 把 used_count >= max_uses 一并滤成了 ErrNoRows，
-- 用它就**不可能**满足这条契约。核销路径仍然走 RedeemInviteCode 的原子 UPDATE。
-- name: GetInviteCodeAnyState :one
SELECT * FROM invite_codes WHERE code = $1;

-- 注册时 users.group_id 是 NOT NULL 外键，必须给一个值。
-- 这个值只是**占位**：新用户 transfer_enable = 0，在 ListAvailableUsersByServer 的
-- 「u + d < transfer_enable」判定里天然为假，所以他落在哪个分组都看不到节点。
-- 真正的分组在 ApplyUserEntitlement（开通套餐）时按 plan.group_id 覆盖。
-- 优先 'basic'，否则取 id 最小的一个 —— 排序确定，避免不同实例挑到不同分组。
-- name: GetRegistrationGroupID :one
SELECT id FROM server_groups ORDER BY (code = 'basic') DESC, id LIMIT 1;

-- 只用于**找回密码**：那条链路的输入只有 token，没有邮箱，无法按 (email, purpose) 定位。
--
-- 🔴 因此重置令牌必须是高熵随机串（32 字节 CSPRNG），**绝不能是 6 位数字验证码**：
--    按 code_hash 全表定位意味着一个随便猜的 6 位数会命中**任意用户**的待重置记录 ——
--    这个查询的形状本身就把「重置令牌用什么」这件事定死了。
-- name: GetEmailVerificationByCodeHash :one
SELECT * FROM email_verifications
WHERE code_hash = $1 AND purpose = $2
  AND consumed_at IS NULL AND expires_at > now();

-- 发码限流（api-contract §10.2「精确档」）。**不需要 rate_limit 表**：
-- email_verifications 本身就逐条记录了每次发码的 created_at，
-- 「窗口内条数」与「上一条时刻」一次查询就能拿到，且天然跨 Cloud Run 实例一致
-- （进程内计数会被 max-instances 放大 8 倍，而这里是发信配额，8 倍是真实损失）。
-- name: CountRecentEmailVerifications :one
SELECT
  count(*)::bigint          AS sent_in_window,
  max(created_at)::timestamptz AS last_sent_at
FROM email_verifications
WHERE lower(email) = lower($1) AND purpose = $2 AND created_at > $3;

-- per IP 维度（forgot 的 10/h）。同上，靠既有记录而不是额外的计数表。
-- name: CountRecentEmailVerificationsByIP :one
SELECT count(*)::bigint FROM email_verifications
WHERE request_ip = $1 AND purpose = $2 AND created_at > $3;

-- 改密码后「其余会话失效」（api-contract §5.1）：当前这条留着，其余全撤。
-- 与 RevokeAllUserSessions 分开而不是加一个可空参数：
-- 「排除哪一条」写成参数时，传 NULL 会让 id <> NULL 恒为 NULL，一条都撤不掉 ——
-- 而现象是「改完密码别的设备还在线」，没有任何报错。
-- name: RevokeOtherUserSessions :execrows
UPDATE user_sessions SET revoked_at = now()
WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL;


-- ---------- 邮件发送日志 / 送达率探针 ----------
-- ⚠️ email_log 的归属其实是「运营」（0011_ops），本该放在一个 ops.sql 里。
--    暂时落在这里，因为目前只有 /auth/email-code 与 /auth/password/forgot 写它，
--    而 api-contract §5.1 把「每次发码写一条 email_probe」定为该端点**存在的第二个理由**
--    （ADR 0002：邮件是唯一失联恢复通道，收不到验证码的用户就是封锁当天必然失联的用户）。
--    等 ops 面的查询成文时应当整体迁走。

-- name: CreateEmailLog :one
INSERT INTO email_log (user_id, to_email, to_domain, esp, template, subject, status, sent_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- 用户回填验证码的时刻。sent_at → redeemed_at 的差值就是**真实端到端送达时延**，
-- 这是 ADR 0002 §7 要求的送达率实测数据里唯一无法从 ESP 回调拿到的一项。
-- name: MarkEmailProbeRedeemed :exec
-- ⚠️ 子查询必须起别名：不起别名时 `template` 同时可解析到外层 UPDATE 的目标表，
--    PG 直接报 "column reference is ambiguous"。
UPDATE email_log SET redeemed_at = now()
WHERE id = (
  SELECT el.id FROM email_log el
  WHERE lower(el.to_email) = lower($1) AND el.template = $2 AND el.redeemed_at IS NULL
  ORDER BY el.created_at DESC
  LIMIT 1
);
