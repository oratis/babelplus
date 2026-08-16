-- 0002 · 根表：server_groups / plans / admin_users
--
-- 事实源：data-model.md §8（server_groups）、§6（plans）、§11（admin_users）
-- 这三张表被 users / servers / orders / tickets 正向引用，必须先建（data-model §2.3 拓扑序）。

-- ---------- 节点分组 ----------
CREATE TABLE server_groups (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code       text NOT NULL UNIQUE,       -- 'basic' / 'all'
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- ---------- 套餐 ----------
CREATE TABLE plans (
  id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code                 text    NOT NULL UNIQUE,        -- 'lite' / 'standard' / 'heavy'
  name                 text    NOT NULL,
  group_id             bigint  NOT NULL REFERENCES server_groups(id) ON DELETE RESTRICT,

  transfer_enable      bigint  NOT NULL CHECK (transfer_enable > 0),  -- 字节/周期
  device_limit         integer CHECK (device_limit > 0),              -- pricing §3.1：2 / 5 / 10
  speed_limit_mbps     integer CHECK (speed_limit_mbps > 0),          -- 第一阶段全 NULL
  reset_traffic_method reset_method NOT NULL DEFAULT 'monthly_on_order_day',

  -- 价格：人民币分，NULL = 该周期不售。
  -- ⚠️ 刻意不抄 Xboard 的 prices JSON（其值为元、浮点，panels §1.2 标注为一处倒退）。
  price_monthly        bigint CHECK (price_monthly      >= 0),
  price_quarterly      bigint CHECK (price_quarterly    >= 0),
  price_half_yearly    bigint CHECK (price_half_yearly  >= 0),
  price_yearly         bigint CHECK (price_yearly       >= 0),
  price_onetime        bigint CHECK (price_onetime      >= 0),
  price_reset          bigint CHECK (price_reset        >= 0),   -- 流量重置包

  renewable            boolean NOT NULL DEFAULT true,   -- 老用户能否续费
  sellable             boolean NOT NULL DEFAULT true,   -- 新用户能否购买
  visible              boolean NOT NULL DEFAULT true,   -- 是否出现在套餐页
  sort_order           integer NOT NULL DEFAULT 0,
  content_md           text    NOT NULL DEFAULT '',
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  archived_at          timestamptz                      -- 下架 ≠ 删除，见 data-model §13
);

CREATE INDEX plans_visible_idx ON plans (sort_order, id)
  WHERE visible = true AND archived_at IS NULL;

-- ---------- 管理员 ----------
-- 【加固】管理员不是 users 上的一个 flag（data-model §4.1）：
-- 用户鉴权链与后台鉴权链在数据层就是分离的，堵死「一个全局 auth 中间件 + if 分支」这个 Xboard 病灶。
CREATE TABLE admin_users (
  id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  email             text NOT NULL,
  password_hash     text NOT NULL,                 -- argon2id
  -- 强制 TOTP：两列都 NOT NULL，数据库层面不存在「没有 2FA 的管理员」（data-model §11.2）
  totp_secret_enc   bytea       NOT NULL,          -- AES-256-GCM，密钥在 Secret Manager
  totp_confirmed_at timestamptz NOT NULL,
  iap_subject       text,                          -- GCP IAP assertion 的 sub，绑 Google 身份
  role              text NOT NULL CHECK (role IN ('owner','admin','support')),

  -- 危险权限位：默认全部 false，必须显式授予
  perm_mark_order_paid boolean NOT NULL DEFAULT false,   -- D6：全系统最大的内部欺诈面
  perm_refund          boolean NOT NULL DEFAULT false,   -- D7
  perm_adjust_balance  boolean NOT NULL DEFAULT false,   -- D10
  perm_export_csv      boolean NOT NULL DEFAULT false,   -- D14

  last_login_at     timestamptz,
  last_login_ip     inet,
  disabled_at       timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX admin_users_email_uk ON admin_users (lower(email));
