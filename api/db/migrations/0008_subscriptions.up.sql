-- 0008 · 订阅
--
-- 事实源：data-model.md §5
-- 职责：让客户端凭一条 URL 拿到节点列表；让泄漏可以被发现、被精确止血。
--
-- 🔴 订阅 token 独立成表（不是 users 上的一列）：可多条、可命名、可单独吊销；
--    配合 users.sub_revoked_at 一键全撤。绝不用 query string 明文 token。

CREATE TABLE subscription_tokens (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id        bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,

  -- 哈希 + 密文两份（data-model §5.2）：
  --   token_hash 负责查找（唯一索引，O(1)，且应用层不存在逐字节比较 → 时序信道天然消失）
  --   token_enc  负责展示（ADR 0002 的失联恢复要用户自己拼 https://{新域名}/s/{token}，
  --              token 若不可再次展示，每换一次域名就要给所有用户重签）
  -- ⚠️ 这层加密防的是「只拿到数据库」（备份泄漏 / 只读注入 / 快照误共享），
  --    **不防** bp-api 实例被攻破 —— 那时密钥与数据库一起丢。
  token_hash     bytea  NOT NULL,        -- sha256(token)
  token_enc      bytea  NOT NULL,        -- AES-256-GCM(token)，密钥在 Secret Manager 不落 DB
  token_prefix   text   NOT NULL,        -- 明文前 8 位：面板列表与日志定位用

  name           text   NOT NULL DEFAULT '',   -- 用户自命名：'家里的台式机'
  issued_at      timestamptz NOT NULL DEFAULT now(),   -- 与 users.sub_revoked_at 比对
  expires_at     timestamptz,                          -- NULL = 不过期
  last_used_at   timestamptz,
  last_used_ip   inet,
  revoked_at     timestamptz,
  revoked_reason text,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX subscription_tokens_hash_uk ON subscription_tokens (token_hash);
CREATE INDEX subscription_tokens_user_idx ON subscription_tokens (user_id)
  WHERE revoked_at IS NULL;


-- 🔴 每次拉取都写这张表。system-design §5.2 原话：这是唯一能识别「账号共享」的数据来源。
--    面板侧把最近 10 次拉取（时间 / IP / UA）直接展示给用户，边际成本为零 ——
--    用户自己就能发现订阅被白嫖并自助重置，不用开工单。
CREATE TABLE subscription_fetch_log (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id      bigint   NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_id     bigint   REFERENCES subscription_tokens(id) ON DELETE SET NULL,
  request_ip   inet     NOT NULL,
  user_agent   text     NOT NULL DEFAULT '',
  client_flag  text,                   -- 归一化后的客户端：clash / singbox / karing / unknown
  status_code  smallint NOT NULL,      -- 200 / 304 / 404（不存在一律 404，见 ADR 0006）
  format       text,                   -- clash / singbox / base64
  node_count   smallint,               -- 本次下发的节点数；0 = 到期/超配额的伪节点响应
  request_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX subscription_fetch_log_user_idx ON subscription_fetch_log (user_id, request_at DESC);
-- 全库唯一需要定时清理的持久表：90 天硬删（data-model §5.4 / §13）
CREATE INDEX subscription_fetch_log_at_idx   ON subscription_fetch_log (request_at);
