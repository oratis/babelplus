-- 0003 · 账户
--
-- 事实源：data-model.md §4
-- 职责：谁是用户、他买到了什么权利、他现在用掉了多少、他怎么登录、他从哪来。

CREATE TABLE users (
  id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

  -- ---- 身份 ----
  email                 text        NOT NULL,
  password_hash         text        NOT NULL,           -- argon2id 编码串（含参数与 salt）
  password_algo         text        NOT NULL DEFAULT 'argon2id',
  email_verified_at     timestamptz,
  uuid                  uuid        NOT NULL DEFAULT gen_random_uuid(),  -- 节点侧连接凭据

  -- ---- 订阅权利（节点每 60 秒读的就是这几列）----
  plan_id               bigint      REFERENCES plans(id)         ON DELETE SET NULL,
  group_id              bigint      NOT NULL
                                    REFERENCES server_groups(id) ON DELETE RESTRICT,
  transfer_enable       bigint      NOT NULL DEFAULT 0 CHECK (transfer_enable >= 0),  -- 字节
  expired_at            timestamptz,                    -- NULL = 不限时套餐
  speed_limit_mbps      integer     CHECK (speed_limit_mbps > 0),  -- NULL = 不限速
  device_limit          integer     CHECK (device_limit    > 0),   -- NULL = 不限
  banned                boolean     NOT NULL DEFAULT false,
  banned_reason         text,
  banned_at             timestamptz,

  -- ---- 周期锚点（重置与到期都从这里算，见 data-model §6.2）----
  subscription_anchor_at timestamptz,                   -- 首次开通时刻；续费与升级都不改
  reset_seq             integer     NOT NULL DEFAULT 0, -- 已重置次数
  reset_at              timestamptz,                    -- 下次重置时刻（物化，供索引）
  expiry_applied_at     timestamptz,                    -- 到期已被扫描处理（见 data-model §8.4）

  -- ---- 订阅吊销 ----
  sub_revoked_at        timestamptz,                    -- 一键全撤：早于此刻签发的 token 全失效

  -- ---- 邀请与返佣 ----
  invited_by            bigint      REFERENCES users(id) ON DELETE SET NULL,
  commission_rate_bps   integer     CHECK (commission_rate_bps BETWEEN 0 AND 10000), -- NULL = 用系统默认

  -- ---- 通知偏好 ----
  -- ⚠️ 只管到期与流量两类；失联广播（新域名）不受此控制 —— schema 上表达这条裁决的方式
  --    就是不提供「全部通知」总开关那一列（user-journey §12）。
  notify_expire         boolean     NOT NULL DEFAULT true,
  notify_traffic        boolean     NOT NULL DEFAULT true,

  -- ---- 运维 ----
  last_login_at         timestamptz,
  last_login_ip         inet,
  remarks               text,                            -- 仅管理员可见
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  deleted_at            timestamptz,                     -- 软删除；见 data-model §13，永不硬删

  CONSTRAINT users_banned_consistency CHECK ((banned) = (banned_at IS NOT NULL))
);

-- 邮箱唯一：大小写不敏感 + 软删除后释放邮箱。
-- ⚠️ PG 的 text 默认区分大小写（不像 MySQL 的 utf8mb4_unicode_ci），
--    不加 lower() 同一邮箱能注册两次 —— ADR 0005 点名的 MySQL→PG 陷阱。
CREATE UNIQUE INDEX users_email_uk    ON users (lower(email)) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX users_uuid_uk     ON users (uuid);
CREATE        INDEX users_invited_idx ON users (invited_by) WHERE invited_by IS NOT NULL;

-- 节点拉用户列表（data-model §12.2）：group_id 定位 + expired_at 用 coalesce 把 NULL 当 +∞。
-- ⚠️ 查询必须与本表达式逐字同形（coalesce(expired_at,'infinity'::timestamptz) > now()），
--    写成 `expired_at IS NULL OR expired_at > now()` 的 OR 会让规划器放弃索引。
CREATE INDEX users_available_idx
  ON users (group_id, (coalesce(expired_at, 'infinity'::timestamptz)))
  WHERE banned = false AND deleted_at IS NULL;

-- 到期扫描与重置扫描（Cloud Scheduler 每分钟跑，平时命中 0 行）
CREATE INDEX users_expiry_due_idx ON users (expired_at)
  WHERE expired_at IS NOT NULL AND expiry_applied_at IS NULL AND deleted_at IS NULL;
CREATE INDEX users_reset_due_idx  ON users (reset_at)
  WHERE reset_at IS NOT NULL AND deleted_at IS NULL;


-- ---------- 热写字段拆表（1:1）----------
-- 【加固】抄 Remnawave：u/d/online_at 从 users 拆出来，减少行锁竞争。
-- P3 情景 40 行/秒的更新如果落在 users 上，会与「读用户列表」「改套餐」「写 last_login_at」
-- 争同一批行版本；拆表后 users 的写频率降到接近零。
CREATE TABLE user_traffic (
  user_id      bigint      PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  u            bigint      NOT NULL DEFAULT 0 CHECK (u >= 0),   -- 本周期上行字节
  d            bigint      NOT NULL DEFAULT 0 CHECK (d >= 0),   -- 本周期下行字节
  u_lifetime   bigint      NOT NULL DEFAULT 0,                  -- 累计（重置不清零）
  d_lifetime   bigint      NOT NULL DEFAULT 0,
  online_at    timestamptz,                                     -- 最后一次有流量的时刻
  last_node_id bigint,                                          -- 提示用，刻意不建外键
  updated_at   timestamptz NOT NULL DEFAULT now()
);
-- 🔴 本表刻意只有主键，不建任何二级索引，也禁止建任何触发器。
--    理由见 data-model §8.4：它是每 60 秒 × 节点数 × 活跃用户被写的表，
--    一个 ROW 级触发器会把 user_rev bump 从「偶发」变成「每次 push 都发生」，ETag 就彻底失效。


-- ---------- 邀请码 ----------
CREATE TABLE invite_codes (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code          text    NOT NULL,               -- 大写，剔除 0/O/1/I/l 等易混字符
  owner_user_id bigint  REFERENCES users(id) ON DELETE CASCADE,  -- NULL = 管理员种子码
  max_uses      integer NOT NULL DEFAULT 1 CHECK (max_uses >= 1),
  used_count    integer NOT NULL DEFAULT 0 CHECK (used_count >= 0),
  expires_at    timestamptz,
  revoked_at    timestamptz,
  note          text,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT invite_codes_uses CHECK (used_count <= max_uses),
  -- user-journey §3.2 裁决：用户码恒为一次性核销；只有管理员种子码可 1–N 次
  CONSTRAINT invite_codes_user_single_use CHECK (owner_user_id IS NULL OR max_uses = 1)
);
CREATE UNIQUE INDEX invite_codes_code_uk ON invite_codes (code);
CREATE INDEX invite_codes_owner_idx ON invite_codes (owner_user_id)
  WHERE revoked_at IS NULL AND owner_user_id IS NOT NULL;

CREATE TABLE invite_code_uses (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  invite_code_id bigint NOT NULL REFERENCES invite_codes(id) ON DELETE RESTRICT,
  user_id        bigint NOT NULL REFERENCES users(id)        ON DELETE RESTRICT,
  request_ip     inet,
  used_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id)                              -- 一个用户只能被一个码带进来
);
CREATE INDEX invite_code_uses_code_idx ON invite_code_uses (invite_code_id);


-- ---------- 会话（refresh token）----------
CREATE TABLE user_sessions (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id        bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  refresh_hash   bytea  NOT NULL,               -- sha256(refresh_token)，不存明文
  user_agent     text,
  created_ip     inet,
  issued_at      timestamptz NOT NULL DEFAULT now(),
  last_used_at   timestamptz,
  expires_at     timestamptz NOT NULL,
  revoked_at     timestamptz,
  replaced_by_id bigint REFERENCES user_sessions(id) ON DELETE SET NULL  -- 轮换链
);
CREATE UNIQUE INDEX user_sessions_hash_uk ON user_sessions (refresh_hash);
CREATE INDEX user_sessions_user_idx ON user_sessions (user_id, expires_at DESC)
  WHERE revoked_at IS NULL;
-- 过期后 30 天硬删（data-model §13）
CREATE INDEX user_sessions_expiry_idx ON user_sessions (expires_at);


-- ---------- 邮箱验证码 / 找回密码 ----------
CREATE TABLE email_verifications (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  email        text     NOT NULL,
  user_id      bigint   REFERENCES users(id) ON DELETE CASCADE,  -- 注册场景为 NULL
  purpose      verification_purpose NOT NULL,
  code_hash    bytea    NOT NULL,
  attempts     smallint NOT NULL DEFAULT 0,
  max_attempts smallint NOT NULL DEFAULT 5,
  expires_at   timestamptz NOT NULL,
  consumed_at  timestamptz,
  request_ip   inet,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_verifications_lookup_idx
  ON email_verifications (lower(email), purpose, created_at DESC);
-- 30 天硬删（data-model §13）
CREATE INDEX email_verifications_created_idx ON email_verifications (created_at);
