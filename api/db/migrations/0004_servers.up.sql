-- 0004 · 节点
--
-- 事实源：data-model.md §8
-- 职责：节点是谁、它能看到哪些用户、它凭什么证明自己是它、它该不该重新拉一次列表。

CREATE TABLE servers (
  id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code              text            NOT NULL UNIQUE,   -- 'bp-node-hk1'，与 GCE 实例名一致
  name              text            NOT NULL,          -- 面向用户的显示名（兼域名广播位）
  protocol          server_protocol NOT NULL,

  host              text     NOT NULL,       -- 客户端连的地址（IP 或域名）
  port              integer  NOT NULL CHECK (port        BETWEEN 1 AND 65535),  -- 客户端连的端口
  server_port       integer           CHECK (server_port BETWEEN 1 AND 65535),  -- 节点实际监听（端口跳跃时不同）
  region            text     NOT NULL,       -- 'asia-east2' / 'asia-northeast1'
  parent_id         bigint   REFERENCES servers(id) ON DELETE SET NULL,  -- 中转链
  protocol_settings jsonb    NOT NULL DEFAULT '{}'::jsonb,
  tags              text[]   NOT NULL DEFAULT '{}',

  enabled           boolean  NOT NULL DEFAULT true,   -- 是否给节点端下发配置（= 是否运行）
  visible           boolean  NOT NULL DEFAULT true,   -- 是否出现在用户订阅里
  sort_order        integer  NOT NULL DEFAULT 0,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz                       -- 只软删：stat_user_server 是 ON DELETE RESTRICT
);
CREATE INDEX servers_visible_idx ON servers (sort_order)
  WHERE visible = true AND enabled = true AND deleted_at IS NULL;

-- ⚠️ 刻意不建 rate（倍率）列：product-brief §6 裁定第一阶段不引入倍率。
--    引入倍率是一次 ADR 级决策 + 一次 stat_user_server 重建（data-model §16）。

-- 节点 ↔ 分组：改用真关系表，不抄 Xboard 的 group_ids JSON 数组（data-model §8.1）。
-- 收益是引用完整性：删组时数据库会拒绝，而 JSON 数组里的孤儿 id 没有任何人会发现。
CREATE TABLE server_group_map (
  server_id bigint NOT NULL REFERENCES servers(id)       ON DELETE CASCADE,
  group_id  bigint NOT NULL REFERENCES server_groups(id) ON DELETE RESTRICT,
  PRIMARY KEY (server_id, group_id)
);
CREATE INDEX server_group_map_group_idx ON server_group_map (group_id);


-- ---------- 每节点独立密钥（system-design §5.1 加固第 1 条）----------
-- 绝不用 query string 明文 token；DB 只存 sha256 哈希，明文只在签发时返回一次。
-- 用 sha256 而不是 argon2 的理由见 data-model §8.2：密钥是我们签发的 256 bit 随机串，
-- 慢哈希抗离线爆破的价值为零，而 80 并发 × 64 MiB = 5 GiB 内存是实打实的代价。
CREATE TABLE server_keys (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  server_id      bigint NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  name           text   NOT NULL DEFAULT '',
  key_prefix     text   NOT NULL,          -- 'bpk_a1b2c3d4' 明文前缀，仅用于日志与 UI 定位
  key_hash       bytea  NOT NULL,          -- sha256(完整密钥)；明文只在签发时返回一次
  scopes         text[] NOT NULL DEFAULT '{uniproxy}',   -- 硬编码路由白名单的键
  issued_at      timestamptz NOT NULL DEFAULT now(),
  expires_at     timestamptz,
  last_used_at   timestamptz,
  last_used_ip   inet,
  revoked_at     timestamptz,
  revoked_reason text,
  created_by     bigint REFERENCES admin_users(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX server_keys_hash_uk ON server_keys (key_hash);
CREATE INDEX server_keys_server_idx ON server_keys (server_id) WHERE revoked_at IS NULL;
-- ⚠️ 刻意不建 UNIQUE (server_id) WHERE revoked_at IS NULL（data-model §8.3）：
--    轮换强制两步（先签发新的 → 确认节点已用新密钥上报 → 再撤旧的），
--    这要求同一节点在一段时间内有两把有效密钥。「同时有效 ≤ 2」是应用层规则 + 巡检 SQL。


-- ---------- ETag 的版本号来源（ADR 0006 的硬要求）----------
-- ETag 由版本号驱动，不哈希响应体。/config 只参与 config_rev，/user 只参与 user_rev。
CREATE TABLE node_rev (
  server_id     bigint PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
  config_rev    bigint NOT NULL DEFAULT 1,
  user_rev      bigint NOT NULL DEFAULT 1,
  config_rev_at timestamptz NOT NULL DEFAULT now(),
  user_rev_at   timestamptz NOT NULL DEFAULT now()
);
