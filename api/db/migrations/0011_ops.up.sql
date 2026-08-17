-- 0011 · 运营
--
-- 事实源：data-model.md §11
-- 职责：谁在管这个系统、他做了什么、系统对外说了什么、邮件到没到。

-- append-only：由 DB 层 REVOKE UPDATE/DELETE/TRUNCATE 强制（data-model §11.1）。
-- ⚠️ 这道机制防的是「应用代码写错」与「后台不小心加了删除入口」，
--    **不防**持有 bp_migrate（DDL 权限）或 Cloud SQL 实例管理权限的人。
--    真正的不可篡改需要外送到 Cloud Logging append-only sink 或带对象锁的 GCS（P4）。
CREATE TABLE audit_logs (
  id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  admin_user_id        bigint REFERENCES admin_users(id) ON DELETE SET NULL,
  admin_email_snapshot text NOT NULL,      -- 快照：管理员被删也留得住证据
  action               text NOT NULL,      -- 'D6.order.mark_paid' / 'D2.user.ban' / ...
  target_type          text NOT NULL,      -- 'user' | 'order' | 'server' | 'server_key' | ...
  target_id            text NOT NULL,
  before_value         jsonb,              -- 改前
  after_value          jsonb,              -- 改后
  reason               text,
  request_ip           inet NOT NULL,
  user_agent           text,
  created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_created_idx ON audit_logs (created_at DESC);
CREATE INDEX audit_logs_admin_idx   ON audit_logs (admin_user_id, created_at DESC);
CREATE INDEX audit_logs_target_idx  ON audit_logs (target_type, target_id, created_at DESC);


CREATE TABLE notices (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  title      text NOT NULL,
  content_md text NOT NULL,
  level      text NOT NULL DEFAULT 'info' CHECK (level IN ('info','warning','critical')),
  pinned     boolean NOT NULL DEFAULT false,
  visible    boolean NOT NULL DEFAULT true,
  starts_at  timestamptz,
  ends_at    timestamptz,
  sort_order integer NOT NULL DEFAULT 0,
  created_by bigint REFERENCES admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notices_visible_idx ON notices (pinned DESC, sort_order, created_at DESC)
  WHERE visible = true;


-- ⚠️ 面板内知识库已被删除（page-inventory §3.1：竞品把教程放在登录墙后，
--    而用户最需要教程时恰恰打不开面板）。正文在 docs.* 静态站的 git 仓库里。
--    本表只做「工单分类 → 排障文档」的索引，第一阶段 body_md 恒为空。
CREATE TABLE knowledge_articles (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  slug         text NOT NULL UNIQUE,
  title        text NOT NULL,
  summary      text NOT NULL DEFAULT '',
  external_url text,                     -- docs.* 上的规范 URL，第一阶段的唯一真相
  body_md      text NOT NULL DEFAULT '', -- 第一阶段恒为空
  category_id  bigint REFERENCES ticket_categories(id) ON DELETE SET NULL,
  visible      boolean NOT NULL DEFAULT true,
  sort_order   integer NOT NULL DEFAULT 0,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);


-- 邮件发送日志 = user-journey §3.3 的 email_probe（合并成一张表，不建两张，见 data-model §11.3）。
-- 理由：恰恰是「其他邮件」（域名广播）的送达率才是生死攸关的那一个。
CREATE TABLE email_log (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id        bigint REFERENCES users(id) ON DELETE SET NULL,
  to_email       text NOT NULL,
  to_domain      text NOT NULL,          -- 冗余：'qq.com' / 'gmail.com'，按域名分组统计送达率
  esp            text NOT NULL,          -- 发信服务商：'ses' / 'resend' / ...
  template       text NOT NULL,          -- 'verify_code' / 'domain_broadcast' / 'expire_remind'
  subject        text NOT NULL,
  provider_msg_id text,
  status         text NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued','sent','delivered','bounced','complained','failed')),
  bounce_code    text,                   -- 例如网易的 '554 HL:IPB'
  bounce_type    text CHECK (bounce_type IN ('hard','soft','block')),
  sent_at        timestamptz,
  delivered_at   timestamptz,
  -- 探针专用：用户回填验证码的时刻。sent_at → redeemed_at 的差值就是真实端到端送达时延
  redeemed_at    timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_log_domain_idx ON email_log (to_domain, template, created_at DESC);
CREATE INDEX email_log_user_idx   ON email_log (user_id, created_at DESC);


CREATE TABLE settings (
  key         text PRIMARY KEY,
  value       jsonb NOT NULL,
  description text NOT NULL DEFAULT '',
  updated_by  bigint REFERENCES admin_users(id) ON DELETE SET NULL,
  updated_at  timestamptz NOT NULL DEFAULT now()
);
