-- subscription_user.sql · 面板侧订阅域
--
-- 事实源：openapi/openapi.yaml（已冻结）的 getUserSubscription / listSubscriptionTokens /
--         createSubscriptionToken / revokeSubscriptionToken / revokeAllSubscriptionTokens /
--         listSubscriptionFetchLog / listUserNodes 七个 operation；
--         data-model.md §5（订阅 token 两份存储、拉取审计）；
--         ADR 0015 §5.7（cn_mode 的口径与失效条件）；
--         表的真身在 0008_subscriptions.up.sql、0003_accounts.up.sql、0004_servers.up.sql，
--         cn_mode 那一列在 0017_subscription_fetch_log_cn_mode.up.sql。
--
-- 🔴 本文件与既有的 `subscriptions.sql` **职责不同，不是它的重写**：
--    subscriptions.sql 服务的是**订阅下发路径**（GET /s/{token}：解析 token、写审计、算 userinfo），
--    本文件服务的是**面板路径**（用户自己管理 token、看拉取记录、看概览）。
--    两者共用同几张表，但对「什么算有效」的判定必须一致 —— 下面每一条需要这个判定的查询
--    都逐字复用 ResolveSubscriptionToken 的四个条件，理由写在 §1 的开头。
--
-- 🔴 users 永不硬删（data-model §1.2 裁决 8）：本文件每一条读 users 的查询都带 deleted_at IS NULL。


-- ============================================================
-- §1 token 列表：唯一一处必须把「一键全撤」折进来的地方
-- ============================================================
--
-- 🔴 **为什么不复用 subscriptions.sql 的 ListSubscriptionTokens。**
--    那一条的过滤条件是 `revoked_at IS NULL`，而**一键全撤不写 revoked_at** ——
--    data-model §5 与 subscriptions.sql 的 RevokeAllSubscriptions 都逐字写着
--    「只写 users.sub_revoked_at，不动 subscription_tokens 的行：吊销本身是证据，删行等于毁证」。
--
--    于是用它渲染面板会得到一个**自相矛盾的界面**：用户点完「全部重置」，
--    列表里每一条 token 仍然显示为有效，而 /s/{token} 对它们全部 404。
--    没有任何报错、没有任何日志 —— 用户只会得出「重置没生效」并开工单，
--    或者更糟：他把那条已经失效的链接又发给了自己的另一台设备。
--
--    判定的四个条件与 ResolveSubscriptionToken 逐字同形（少一条就是一处漂移）：
--      1. t.revoked_at IS NULL          —— 单条吊销
--      2. t.expires_at 未到             —— 自身过期
--      3. t.issued_at > u.sub_revoked_at —— 一键全撤（Marzban 语义）
--      4. u.deleted_at IS NULL          —— 账号已注销
--    第 3 条是本条查询存在的全部理由。
--
-- ⚠️ 判定写成 `is_active` 一列而不是写进 WHERE：面板要**同时**展示已失效的 token
--    （openapi 的 SubscriptionToken 带 revoked_at 字段，说明列表不是只给有效的）。
--    把已吊销的行藏起来，用户就无法确认「我刚才撤的那条真的撤掉了」。
--
-- ⚠️ 刻意**不选 token_enc**。列表页渲染 masked 用的是 token_prefix（明文前 8 位，0008 原话），
--    密文一列在这里没有任何用处，而把 AES 密文捎进一个只需要展示的 handler，
--    等于给「顺手序列化进响应」留了一条路。需要还原明文的只有失联恢复那一条路径，
--    它走 subscriptions.sql 的 GetSubscriptionToken（SELECT *，含 token_enc）。
-- name: ListUserSubscriptionTokens :many
SELECT
  t.id,
  t.name,
  t.token_prefix,
  t.issued_at,
  t.expires_at,
  t.last_used_at,
  t.last_used_ip,
  t.revoked_at,
  t.revoked_reason,
  t.created_at,
  u.sub_revoked_at,
  (t.revoked_at IS NULL
   AND (t.expires_at IS NULL OR t.expires_at > now())
   AND (u.sub_revoked_at IS NULL OR t.issued_at > u.sub_revoked_at))::boolean AS is_active
FROM subscription_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.user_id = sqlc.arg(user_id)::bigint
  AND u.deleted_at IS NULL
ORDER BY t.created_at DESC, t.id DESC;


-- 签发前的名额闸门。
--
-- openapi 给 createSubscriptionToken 声明了 **403 ErrForbidden**，而用户侧唯一能触发 403 的
-- 理由就是「你的 token 已经到上限」—— 401 是没登录、422 是名字不合法、500 是我们的错。
-- 也就是说契约在这里隐含了一个上限，只是没有写数字。
--
-- ⚠️ **上限的具体数字未裁决**，所以它是参数不是常量：openapi、api-contract §14、
--    data-model §5 里都找不到「每用户最多 N 条订阅 token」。定这个数要看的是
--    「一个人到底有几台设备」，而那要等 P2 的真实数据。**需实测。**
--    在有数字之前，handler 侧应当取一个宽松值（例如 10）并把命中次数记进日志 ——
--    上限设得太紧的表现是用户装第四台设备时被拒，而他完全不知道为什么。
--
-- 判定与 ListUserSubscriptionTokens 的 is_active 逐字同形。用 count 而不是复用上面那条
-- 再在 Go 里数，是为了让「闸门」与「列表」在并发下不必一致 —— 闸门要的是此刻的数，
-- 列表要的是可展示的全集，两者的过滤条件相同但用途不同。
-- name: CountActiveSubscriptionTokens :one
SELECT count(*)::int AS active_tokens
FROM subscription_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.user_id = sqlc.arg(user_id)::bigint
  AND u.deleted_at IS NULL
  AND t.revoked_at IS NULL
  AND (t.expires_at IS NULL OR t.expires_at > now())
  AND (u.sub_revoked_at IS NULL OR t.issued_at > u.sub_revoked_at);


-- ℹ️ **本节没有 INSERT，也没有单条吊销的 UPDATE。** 这是刻意的，不是漏写：
--
--   · createSubscriptionToken 走 subscriptions.sql 的 **CreateSubscriptionToken**。
--     它 `RETURNING *`（把 token_hash / token_enc 一起带回），按上面 §1 的口径本该写一条窄的，
--     但**写入路径不该有第二条**：subscription_tokens 将来加一列（比如「绑定的客户端类型」），
--     两条 INSERT 只会有一条被改，而漏掉的那条插出来的行少一个字段、不报任何错。
--     读可以有多个投影，**写只能有一个事实源** —— 这条取舍与上面 ListUserSubscriptionTokens
--     的结论方向相反，因为代价不同：读写错了是多看见几列，写写错了是行本身就不对。
--
--   · revokeSubscriptionToken 走 subscriptions.sql 的 **RevokeSubscriptionToken**。
--     它的 `WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL` 让「不是你的」与
--     「已经撤过了」同为 0 行，而 openapi 给这个端点只声明了 404（没有 409）——
--     两者返回同一个 404 正是契约要的，也不给枚举留信号。


-- ============================================================
-- §2 一键全撤：契约要一个数字，既有查询给不出
-- ============================================================
--
-- 🔴 **为什么不复用 subscriptions.sql 的 RevokeAllSubscriptions。**
--    openapi 的 RevokeAllResult 是 `required: [revoked, sub_revoked_at]` ——
--    `revoked` 是**被吊销的 token 条数**。RevokeAllSubscriptions 只 RETURNING (id, sub_revoked_at)，
--    拿不到这个数。handler 若先 count 再 update，两条语句之间插进一次 createSubscriptionToken，
--    返回的条数就比实际撤掉的少一条 —— 而这个界面的全部作用就是让用户相信「都撤干净了」。
--
-- 🔴 **CTE 的求值顺序在这里是被依赖的语义，不是巧合**：同一条语句里的数据修改型 CTE
--    互相看不到对方的效果，所以 `live` 看到的是 UPDATE **之前**的快照 ——
--    也就是「本次真正被这一下撤掉的条数」。反过来写（先 UPDATE 再 count）会恒等于 0。
--
-- ⚠️ 与 RevokeAllSubscriptions 一样，**不动 subscription_tokens 的任何一行**：
--    吊销的证据是 users.sub_revoked_at 这个时刻加上每条 token 的 issued_at，
--    把 revoked_at 刷上去等于把「他撤过几次、每次撤掉了哪些」这段历史抹平。
--
-- 用户不存在 / 已注销时 `bumped` 为空 → 整条语句 0 行 → sqlc 的 :one 返回 ErrNoRows。
-- 这是对的：会话有效但用户已注销，是 401 不是 200。
-- name: RevokeAllUserSubscriptionTokens :one
WITH live AS (
  SELECT count(*)::int AS n
  FROM subscription_tokens t
  JOIN users u ON u.id = t.user_id
  WHERE t.user_id = sqlc.arg(user_id)::bigint
    AND u.deleted_at IS NULL
    AND t.revoked_at IS NULL
    AND (t.expires_at IS NULL OR t.expires_at > now())
    AND (u.sub_revoked_at IS NULL OR t.issued_at > u.sub_revoked_at)
),
bumped AS (
  UPDATE users
  SET sub_revoked_at = now(), updated_at = now()
  WHERE id = sqlc.arg(user_id)::bigint AND deleted_at IS NULL
  RETURNING id, sub_revoked_at
)
SELECT b.sub_revoked_at, l.n AS revoked
FROM bumped b CROSS JOIN live l;


-- ============================================================
-- §3 拉取审计：面板的自助查漏页
-- ============================================================
--
-- 🔴 **为什么不复用 subscriptions.sql 的 ListSubscriptionFetchLog。** 三条，每条都足够：
--
--   1. **少了 token 名字。** openapi 的 SubscriptionFetchLogEntry 有 `sub_token_name`。
--      没有它，页面上是一串「2026-08-30 12:31 · 1.2.3.4 · clash」——
--      用户看得出有人在拉，却看不出是哪条链接泄漏的，于是唯一能做的动作是「全部重置」，
--      而这会打断他所有正常设备。有名字他就能只撤那一条。
--      LEFT JOIN 而不是 JOIN：token_id 是 `ON DELETE SET NULL`，且 404 的那些拉取
--      本来就没有 token_id —— 用 JOIN 会把「有人拿着不存在的 token 在试」这一类记录
--      整个滤掉，而那恰恰是这张表最该显示的东西。
--
--   2. **少了 cn_mode。** 0017 加的那一列（ADR 0015 §5.7）。它不进响应体，
--      但一起选出来让 handler 可以在同一次查询里把它写进日志 —— 见 §4 的分母。
--
--   3. **分页口径不对。** 那一条是 `LIMIT $2 OFFSET $3`，而 openapi 给这个端点的参数是
--      **CursorQuery**（`{"id":…,"at":"…"}` 的 keyset），不是 page/offset。
--      OFFSET 在这张只增不减、按 request_at DESC 排的表上会**漏行**：
--      翻页期间新写入一条，第二页的首行就是第一页的末行往后挪一位的那一条，中间那条永远看不见。
--      对一张「用来发现别人在偷用你订阅」的表来说，漏掉一行就是漏掉那一次。
--
-- ⚠️ 游标的两个字段必须**同时**传或**同时**不传。只传 at 不传 id 时，行比较
--    `(request_at, id) < (at, NULL)` 求值为 NULL → 一行都不返回，而不是报错。
--    handler 解游标时必须校验两个字段都在（openapi 对 CursorQuery 的原话就是
--    「服务端必须校验解出的字段类型」）。
--
-- ⚠️ 排序键取 (request_at, id) 而不是单独 id：request_at 有 DEFAULT now()，同一毫秒内
--    可以有多条，id 是唯一的破平手键。走 subscription_fetch_log_user_idx (user_id, request_at DESC)。
--
-- ⚠️ openapi 对本端点的 description 写「默认 10 条」，而共享的 LimitQuery 参数
--    `default: 20`。两处不一致，**契约本身的冲突**。SQL 不做决定：limit 是参数。
--    handler 取哪个都能通过契约校验，但页面上应当取 10（description 是这个端点自己说的）。
-- name: ListUserSubscriptionFetchLog :many
SELECT
  f.id,
  f.request_at,
  f.request_ip,
  f.user_agent,
  f.client_flag,
  f.status_code,
  f.format,
  f.node_count,
  f.cn_mode,
  f.token_id,
  t.name         AS token_name,
  t.token_prefix AS token_prefix
FROM subscription_fetch_log f
LEFT JOIN subscription_tokens t ON t.id = f.token_id
WHERE f.user_id = sqlc.arg(user_id)::bigint
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR (f.request_at, f.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::bigint)
  )
ORDER BY f.request_at DESC, f.id DESC
LIMIT sqlc.arg(page_limit)::int;


-- ============================================================
-- §4 cn_mode：把 ADR 0015 的失效条件接上电
-- ============================================================
--
-- 🔴 **subscriptions.sql 的 InsertSubscriptionFetchLog 写不了 cn_mode。**
--    它是 0008 时代的 8 列插入，而 0017 加了第 9 列。sqlc 不会因此报错
--    （少写一个可空列是合法的 INSERT），go build 也不会 —— 现象是 cn_mode **永远是 NULL**，
--    而下面那条比率查询的分子恒为 0，于是 ADR 0015 §5.7 的失效条件
--    「`?cn=proxy` 使用率 > 20% ⇒ 白名单本身错了，整体回退」**永远不会触发**。
--
--    0017 的文件头逐字写着这件事的性质：「一条永远无法被触发的失效条件比没有失效条件更坏 ——
--    它让人以为有刹车。」所以订阅下发路径必须改用**本条**写审计。
--
-- ⚠️ 两条 INSERT 同时存在是上面 §1 刚刚拒绝过的那种「写有两个事实源」。这里接受它，
--    但接受的方式是**指定唯一入口**：订阅下发 handler 一律用本条，
--    InsertSubscriptionFetchLog 只留给已经存在的调用点，且应当在下一次触碰时删除。
--    如果两条长期并存，迟早有一条路径的 cn_mode 又变回 NULL —— 而那是静默的。
--
-- cn_mode 是**观测列**：取值不做 CHECK、不做归一化以外的校验（0017 的原话：
-- 「记录不认识的取值恰恰是观测列该做的事」）。handler 侧把 ?cn= 归一化成
-- 'direct' / 'proxy'，认不出来的原样写进去。
-- name: InsertUserSubscriptionFetchLog :one
INSERT INTO subscription_fetch_log (
  user_id, token_id, request_ip, user_agent, client_flag,
  status_code, format, node_count, cn_mode
) VALUES (
  sqlc.arg(user_id)::bigint,
  sqlc.narg(token_id)::bigint,
  sqlc.arg(request_ip)::inet,
  sqlc.arg(user_agent)::text,
  sqlc.narg(client_flag)::text,
  sqlc.arg(status_code)::smallint,
  sqlc.narg(format)::text,
  sqlc.narg(node_count)::smallint,
  sqlc.narg(cn_mode)::text
)
RETURNING id, request_at;


-- ADR 0015 §5.7 的失效条件，逐字落地。
--
-- 🔴 **分母按人不按次，两者差 5 倍**（0017 的 COMMENT ON COLUMN 原文）。
--    按次算会被少数几个疯狂重拉的客户端整个带偏：一台配置成每 5 分钟拉一次的路由器
--    一周就是 2016 次，而它只是一个人。
--
-- ⚠️ 分子的 `count(DISTINCT user_id) FILTER (WHERE cn_mode = 'proxy')` 里，
--    NULL 与 'direct' 都不计入分子但都计入分母 —— 这是对的：NULL 是「0017 之前的历史行」，
--    'direct' 是「他没选代理」，两者在「有多少人选了代理」这个问题上都是分母的一部分。
--
-- ⚠️ 窗口是参数不是常量（ADR 写的是 7 天）：换窗口不该要一次 sqlc 重新生成。
--    参数名叫 lookback 而不是 window —— `window` 是 PostgreSQL 保留字（窗口函数的
--    WINDOW 子句），sqlc 的解析器在 `sqlc.arg(window)` 上直接报 syntax error。
--    这类错误只在 `make gen-db` 时暴露，编辑器里看不出来。
--    走 subscription_fetch_log_at_idx (request_at)，0017 刻意没有为这条统计另建索引。
--
-- 分母为 0 时（这一周一次拉取都没有）NULLIF 让结果为 NULL 而不是除零异常。
-- handler 必须把 NULL 当成「无数据」而**不是** 0 —— 把无数据渲染成「0%，一切正常」，
-- 就是把「采集断了」伪装成「指标很好」，而那是这条失效条件最坏的失败模式。
-- name: GetCnProxyModeRatio :one
SELECT
  count(DISTINCT user_id) FILTER (WHERE cn_mode = 'proxy')::bigint AS proxy_users,
  count(DISTINCT user_id)::bigint                                  AS total_users,
  (count(DISTINCT user_id) FILTER (WHERE cn_mode = 'proxy')::numeric
     / NULLIF(count(DISTINCT user_id), 0))::numeric                AS proxy_ratio
FROM subscription_fetch_log
WHERE request_at > now() - sqlc.arg(lookback)::interval;


-- ============================================================
-- §5 订阅概览（getUserSubscription 的 summary 半边）
-- ============================================================
--
-- openapi 的 UserSubscription = { urls, summary }。`urls` 五条 URL 由 handler 用
-- 明文 token 与当前域名拼（域名会随 ADR 0002 的失联恢复换，所以它不该来自数据库），
-- 本条只负责 `summary`。
--
-- 🔴 **为什么不复用 users.sql 的 GetUserWithTraffic 再补两次查询。**
--    SubscriptionSummary 的 required 里有 `device_count`，而设备数在另一张表（且是 UNLOGGED 表）。
--    拆成两次查询意味着「配额」与「在线设备数」来自两个时刻，
--    而这个页面正是用户拿来核对「我开了 3 台，为什么说我超了」的地方 ——
--    两个时刻的组合可以显示出一个数据库里从未同时存在过的状态。
--    一条语句 = 一个快照。多出来的代价是一次索引扫描（user_device_state 是 UNLOGGED，
--    且按 user_id 前缀走主键），换掉的是一类无法复现的用户投诉。
--
-- 🔴 **device_count 用 count(DISTINCT device_ip)，不是 count(*)。**
--    user_device_state 的主键是 (user_id, server_id, device_ip)，同一个 IP 同时连三个节点
--    就是三行。而 data-model §8.5 裁决的计数单位是 **IP**（「同一台手机切换 Wi-Fi/蜂窝
--    会占两个名额」说的是 IP 变了，不是连了几个节点）。
--    ⚠️ 这与 servers.sql 的 ListAliveDeviceCounts 不一致 —— 那一条是 `count(*)` 按 user_id 分组，
--       把「一个 IP 连三个节点」算成 3。两者口径不同的表现是：**面板显示 2 台，节点按 5 台踢人**。
--       本文件不改 servers.sql，此处登记为待修（见交付说明）。
--
-- ⚠️ **设备数是软限制。** alivelist 拉取失败时 v2node 静默降级为「零在线设备」（B16 实证），
--    所以这个数字**偏小**是常态，绝不能拿它做任何拒绝服务的判定。页面上要写清楚
--    它是「按 IP 的近似值」，否则用户会拿它跟自己手上的设备台数对质。
--
-- ⚠️ 时间窗 5 分钟与 servers.sql 的 CleanupStaleDeviceState 同值（Cloud Scheduler 每 5 分钟
--    删 `last_seen_at < now() - 5 minutes` 的行）。带上这个条件而不是裸查全表，
--    是因为清理作业**可能没跑**（它是外部调度器，失败是静默的）——
--    条件写在查询里，扫描作业挂了也只影响存储不影响数字。
--
-- ⚠️ total_bytes 读 `u.transfer_enable`，那是 0016 之后的**生成列**（= _plan + _pack）。
--    读它是安全的（0016 给了 NOT NULL，读侧 Go 类型仍是 int64）；写它会在运行时炸。
--    两个分量也一并选出来：页面要能回答「我的 100 GB 里有多少是加油包、什么时候过期」，
--    而这正是 ADR 0013 §5.3 那个「加油包被吃掉了还是结转了」的静默失败在用户侧的样子。
-- name: GetUserSubscriptionSummary :one
SELECT
  p.name                 AS plan_name,
  p.kind                 AS plan_kind,
  ut.u                   AS upload_bytes,
  ut.d                   AS download_bytes,
  u.transfer_enable      AS total_bytes,
  u.transfer_enable_plan,
  u.transfer_enable_pack,
  u.pack_expire_at,
  u.expired_at,
  u.reset_at,
  u.device_limit,
  u.banned,
  u.sub_revoked_at,
  ut.online_at           AS traffic_last_active_at,
  ut.updated_at          AS traffic_reported_at,
  (SELECT count(DISTINCT d.device_ip)
     FROM user_device_state d
    WHERE d.user_id = u.id
      AND d.last_seen_at > now() - interval '5 minutes')::int AS device_count
FROM users u
JOIN user_traffic ut ON ut.user_id = u.id
LEFT JOIN plans p ON p.id = u.plan_id
WHERE u.id = sqlc.arg(user_id)::bigint AND u.deleted_at IS NULL;


-- ============================================================
-- §6 节点列表（listUserNodes）
-- ============================================================
--
-- 🔴 **为什么不复用 servers.sql 的 ListVisibleServersForUser。**
--    那一条按 `group_id` 取节点（正确），但只返回 servers 的列 ——
--    openapi 的 UserNode `required: [id, name, type, status]`，**status 拿不到**。
--    补一次 ListServerOnlineState 再在 Go 里做 map 合并，等于把
--    「节点在列表里但状态查不到」这种情况的处理散在 handler 里，而它恰恰是最常见的一种
--    （server_online_state 是 UNLOGGED 表，**崩溃后自动 TRUNCATE** —— 数据库重启一次，
--     所有节点的状态行就都没了，而 servers 里的行还在）。
--    LEFT JOIN 把这种情况变成一行里的 NULL，handler 只有一条路径。
--
-- 🔴 **status 不在 SQL 里算。** online / degraded / offline 的阈值**没有任何文档裁决过**
--    （查遍 openapi、api-contract、ADR 0011/0014：`degraded` 只出现在域名池与限流器语境，
--     与节点心跳无关）。在这里写死一个 `< 2 minutes` 会把一个未裁决的数字
--    固化进生成物，而改它要重跑 sqlc、要过 contract-drift 门禁。
--    所以本条交出**事实**（最后一次上报距今多少秒、节点是否被禁用），三态映射由 handler 做。
--    ⚠️ 这条映射一旦定下来，应当写进 api-contract 而不是留在 handler 常量里。**需实测**：
--       阈值取多少取决于节点 push 的真实周期与抖动，而节点还没上线。
--
-- ⚠️ **openapi 的 UserNode.type 说「权威来源是 servers.type」，而 servers 表上没有 type 这一列** ——
--    真名是 `protocol`（server_protocol 枚举，0004）。契约文字与 schema 冲突，
--    按硬规矩 5 以 openapi 的**定义**为准（字段名 type，字符串），值取 protocol。
--    ⚠️ 但 protocol 的取值是 'vless_reality' / 'hysteria2' / 'shadowsocks2022' / 'vless_xhttp_cdn'，
--       而契约的例子写的是 `vless` / `hysteria` —— **枚举值本身也对不上**，
--       且 openapi 没给 type 定 enum，所以这不是 drift 而是一处待定的映射。
--       直接下发 protocol 会把「我们用 REALITY 还是 XHTTP」告诉所有人，
--       而 vless_xhttp_cdn 是 ADR 0004 的**应急**通路 —— 它出现在用户列表里
--       等于对外宣告我们正在被封。handler 应当映射成粗粒度的展示名。
--
-- ⚠️ **不返回 host / port / protocol_settings。** 那是订阅内容体的东西，不是列表页的东西。
--    /api/v1/user/nodes 是带用户会话的普通 JSON 接口，它的响应会进浏览器缓存、进截图、
--    进用户发给客服的聊天记录；真正的连接参数只应当走 /s/{token} 那条路径。
--
-- ⚠️ multiplier_e9 在契约里是必留字段但第一阶段不引入倍率，
--    而 servers 表**刻意没有 rate 列**（0004 原话，引入倍率是一次 ADR 级决策 + 一次
--    stat_user_server 重建）。所以它由 handler 恒填 1000000000，SQL 这里没有它。
-- name: ListUserNodesWithState :many
SELECT
  s.id,
  s.name,
  s.region,
  s.protocol,
  s.sort_order,
  s.enabled,
  o.online_users,
  o.reported_at,
  o.last_push_at,
  (extract(epoch FROM (now() - o.reported_at)))::bigint AS seconds_since_report
FROM servers s
JOIN server_group_map m ON m.server_id = s.id
LEFT JOIN server_online_state o ON o.server_id = s.id
WHERE m.group_id = sqlc.arg(group_id)::bigint
  AND s.visible = true
  AND s.deleted_at IS NULL
ORDER BY s.sort_order, s.id;
