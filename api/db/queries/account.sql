-- account.sql · 账户设置：通知偏好与用户侧 2FA
--
-- 事实源：openapi/openapi.yaml（已冻结）的 getNotificationPrefs / updateNotificationPrefs /
--         enrollUserTotp / verifyUserTotp / disableUserTotp 五个 operation；
--         api-contract.md §5.3（给出了响应的逐字形状）与 §6.1 表格第 1022–1023 行；
--         data-model.md §4；users 表的真身在 0003_accounts.up.sql。
--
-- 🔴 users 永不硬删（data-model §1.2 裁决 8）：本文件每一条都带 deleted_at IS NULL。
--    漏掉它的后果是「已注销的账号还能读写自己的设置」—— 而且不会有任何报错，
--    因为软删的行在表里长得和正常行一模一样。


-- ============================================================
-- 通知偏好（getNotificationPrefs / updateNotificationPrefs）
-- ============================================================
--
-- 🔴 这里只有两列，没有第三列 —— 那不是遗漏，那就是裁决本身。
--    响应里的 service_broadcast 是 API 层的常量（LockedBoolean，value/locked 恒 true），
--    0003_accounts.up.sql 在 users 的通知偏好段落上逐字写着：
--    「schema 上表达这条裁决的方式就是不提供『全部通知』总开关那一列」。
--    所以**不要**为了跟响应体「对称」而给 users 加一列 notify_broadcast 再在 handler 里忽略它：
--    那一列一旦存在，总有一天会有人把它接到 PUT 上，而失联广播是 ADR 0002 认定的
--    唯一失联恢复通道 —— 用户把它关掉的那天，就是我们再也够不到他的那天。

-- 刻意不复用 users.sql 的 GetUserByID：那条是 SELECT *，会把 password_hash、
-- uuid（节点侧的连接凭据）、last_login_ip 一并带进一个只需要两个布尔值的 handler。
-- 「设置页把 uuid 顺手序列化进响应」这类事故的代价，远高于在这里多写一条窄查询。
-- name: GetUserNotificationPrefs :one
SELECT notify_expire, notify_traffic
FROM users
WHERE id = $1 AND deleted_at IS NULL;

-- 部分更新：NotificationPrefsUpdate 的两个字段**都是可选的**（openapi 里没有 required 列表，
-- 只有 additionalProperties: false），所以「只发 expire_remind」是一个合法请求。
--
-- ⚠️ 不要用 users.sql 里既有的 UpdateUserNotifyPrefs（:exec，两个参数都必填）。
--    用它就必须让 handler 先读一遍再整体回写，而 read-modify-write 在用户于两个设备上
--    同时改设置时，会把后到的那次读到的旧值写回去 ——
--    现象是「刚关掉的开关过一会儿自己又开了」，没有报错、没有日志、无法复现。
--    coalesce(narg, 旧值) 把「没传 = 不改」下沉进同一条语句，两次并发修改互不覆盖。
--
-- 用 :one 而不是 :exec：PUT 要回 200 + 修改后的完整 NotificationPrefs。
-- RETURNING 拿的是本次写入的后像；改成「UPDATE 完再 SELECT 一次」不只是多一次往返 ——
-- 两次之间插进另一个 PUT 时，返回的组合会是一个数据库里从未同时存在过的状态。
--
-- ℹ️ 这两列不在 0012_user_rev_triggers 盯着的列里（group_id / uuid / banned / expired_at /
--    transfer_enable / speed_limit_mbps / device_limit / deleted_at / expiry_applied_at），
--    所以改通知偏好**不会** bump node_rev.user_rev，节点的 ETag 不会因为用户点了个开关而失效。
--    这是对的：节点不关心谁要收邮件。（触发器在生成的 Go 代码里是隐形的，sqlc.yaml 已登记这个代价。）
--
-- 空请求体 `{}` 会走到这里并原地重写一行 users（updated_at 前进、产生一个死元组）。
-- 接受这个代价：换取的是 handler 只有一条路径。想靠 WHERE ... IS DISTINCT FROM 把无变化的
-- 请求挡掉的话，返回 0 行会和「用户不存在」撞成同一个 ErrNoRows，而 PUT 必须回 200 + 当前值。
-- name: UpdateUserNotificationPrefs :one
UPDATE users SET
  notify_expire  = coalesce(sqlc.narg(expire_remind)::boolean,  users.notify_expire),
  notify_traffic = coalesce(sqlc.narg(traffic_remind)::boolean, users.notify_traffic),
  updated_at     = now()
WHERE id = sqlc.arg(user_id)::bigint AND deleted_at IS NULL
RETURNING notify_expire, notify_traffic;


-- ============================================================
-- 用户侧 TOTP（enrollUserTotp / verifyUserTotp / disableUserTotp）
-- ============================================================
--
-- 🔴 本节**没有任何查询**。这是把 0001–0017 的 up.sql 全部读完、再用 psql \d 逐列核对之后的
--    结论，不是漏写。把过程写下来，是为了让下一个人不必再走一遍同样的路。
--
-- 实查三条：
--
--   1. users 表上**没有** totp_secret_enc / totp_confirmed_at / totp_enabled 中的任何一列。
--      带 totp 的那两列只长在 admin_users 上（0002_foundation.up.sql:56–57），而且都是
--      NOT NULL —— 那是「数据库层面不存在没有 2FA 的管理员」（data-model §11.2），
--      是一条**与用户侧方向相反**的设计：管理员不许不开，用户是可选开
--      （api-contract §6.1 把用户侧 TOTP 标为「可选，P3」）。
--      所以 admin_users 那两列既不能复用，形状也不能照抄。
--
--   2. used_totp（0015_payment_fixes.up.sql:89）的主键是 (admin_user_id, code_hash)，
--      admin_user_id 是 admin_users(id) 的外键。**它装不下用户侧的 code。**
--      ADR 0012 C25 把它纳入 0015 的理由逐字是「D6 是本 ADR 亲手扩大的欺诈面」，
--      而 D6 是管理面的「手工标记已支付」，从头到尾没有用户侧的份。
--      这一条最容易看漏 —— api-contract §1266 与任务书都只说「需 used_totp 表」，没说是谁的表。
--
--   3. openapi 对 enrollUserTotp 的 description 逐字是「**P3，未实现。** 服务端返回 501
--      直到实现完成」，三个端点的 summary 也都带 (P3)。
--      **契约自己就声明了它尚未落地**，所以「schema 里没有这些列」与冻结的契约是一致的，
--      不是 contract drift，不需要在这里记一条冲突。
--
-- 结论：这三个 operation 在当前 schema 下没有可写的 SQL。handler 阶段照契约返回 501。
-- 补齐它需要一次 migration，而迁移不由本阶段加。
--
-- 将来真要落地时缺的是下面这些。写在这里只为省下一次重新推导，
-- **不构成已裁决的方案** —— 真正的裁决要走 ADR：
--
--   - users.totp_secret_enc bytea NULL + users.totp_confirmed_at timestamptz NULL。
--     必须可空（用户侧是可选 2FA）。CurrentUser.totp_enabled 应当由
--     `totp_confirmed_at IS NOT NULL` 算出来，**不要**再加一列布尔值：
--     两个来源迟早不同步，而不同步的表现是「界面说没开，登录却要验证码」。
--
--   - enroll 与 verify 之间那份**尚未确认**的 secret 需要有地方待着。复用上面两列、
--     用 totp_confirmed_at IS NULL 表示「绑定中」是最省事的，但代价是一次半途而废的 enroll
--     会覆盖掉已经绑好的 secret。所以 enroll 的写入必须带 `WHERE totp_confirmed_at IS NULL`，
--     否则「打开一次绑定页」就等于把用户已有的 2FA 弄坏，而他要到下次登录才发现。
--
--   - 防重放要么新开一张用户侧的对应表，要么把 used_totp 改成
--     (subject_kind, subject_id, code_hash)。
--     🔴 **绝不能把 users.id 直接塞进 admin_user_id**：两个 id 空间会重叠，
--     结果是某个用户能把某个管理员的 code「用掉」，或反过来 —— 一个防重放表变成了跨界的
--     拒绝服务面。何况那一列上有指向 admin_users 的外键，塞进去会直接违反外键。
