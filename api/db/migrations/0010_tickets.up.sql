-- 0010 · 工单（8 张表 + 1 个视图）
--
-- 事实源：data-model.md §10（tickets / ticket_messages 两张主表 + 四处修改）
--        admin-support-docs.md §2.4（另外 6 张辅助表，原样采纳）
-- 职责：让每张工单自带诊断路径。

-- ---------- 分类 ----------
CREATE TABLE ticket_categories (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  slug          text NOT NULL UNIQUE,           -- 'subscription' / 'node-down' / 'billing' / 'account'
  name_zh       text NOT NULL,
  name_en       text,
  -- 每个分类可覆盖默认 SLA；NULL 表示继承全局默认
  sla_first_response_minutes integer,
  sla_resolution_minutes     integer,
  default_priority           ticket_priority NOT NULL DEFAULT 'normal',
  sort_order    integer NOT NULL DEFAULT 0,
  is_active     boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now()
);


-- ---------- 工单主表 ----------
CREATE TABLE tickets (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id      text NOT NULL UNIQUE,          -- 'BP-7K2M9Q'：对外只暴露短码，防枚举
  -- ON DELETE RESTRICT 与「users 永不硬删」（data-model §13）一致，此约束实际永不触发，留作最后一道保险
  user_id        bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  category_id    bigint REFERENCES ticket_categories(id) ON DELETE SET NULL,

  subject        text            NOT NULL,
  status         ticket_status   NOT NULL DEFAULT 'open',
  priority       ticket_priority NOT NULL DEFAULT 'normal',
  channel        ticket_channel  NOT NULL DEFAULT 'web',

  assignee_id    bigint REFERENCES admin_users(id) ON DELETE SET NULL,
  assigned_at    timestamptz,

  -- 建单瞬间的诊断快照：存 JSONB 而非外键，因为这是「当时的事实」不是「当前的关联」。
  -- 期望形如 {"subscription_id":123,"plan":"pro","client":"Clash Verge Rev 2.5.2",
  --           "os":"Windows 11","node_id":45,"last_seen_ip_country":"CN"}
  context        jsonb NOT NULL DEFAULT '{}'::jsonb,

  first_response_at   timestamptz,   -- 首次由 agent 发出的公开回复时间
  first_response_due  timestamptz,   -- 建单时按 SLA 算出的截止时间
  resolution_due      timestamptz,
  resolved_at         timestamptz,
  closed_at           timestamptz,
  last_user_reply_at  timestamptz,
  last_agent_reply_at timestamptz,

  satisfaction_rating  smallint CHECK (satisfaction_rating BETWEEN 1 AND 5),
  satisfaction_comment text,

  -- ⚠️ 三个 telegram 列与 ticket_channel='telegram' 第一阶段不启用
  --    （ADR 0002：api.telegram.org 大陆异常率 99.1%）。列保留，不实现。
  telegram_chat_id           bigint,
  telegram_message_thread_id bigint,
  email_message_id           text,

  tags           text[]  NOT NULL DEFAULT '{}',
  -- 冗余计数：写消息时在同一事务内 UPDATE，**不用触发器**（data-model §10.1 修改 4）
  message_count  integer NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT tickets_resolved_consistency
    CHECK ((status IN ('resolved','closed')) = (resolved_at IS NOT NULL)),
  CONSTRAINT tickets_closed_consistency
    CHECK ((status = 'closed') = (closed_at IS NOT NULL))
);

-- 客服工作台的主查询：按状态 + 优先级 + SLA 排序。
-- priority DESC 依赖 ticket_priority 的 ENUM 声明序（low < normal < high < urgent）。
CREATE INDEX tickets_queue_idx ON tickets (status, priority DESC, first_response_due)
  WHERE status NOT IN ('resolved','closed');
CREATE INDEX tickets_user_idx     ON tickets (user_id, created_at DESC);
CREATE INDEX tickets_assignee_idx ON tickets (assignee_id, status)
  WHERE status NOT IN ('resolved','closed');
CREATE INDEX tickets_tags_idx    ON tickets USING gin (tags);
CREATE INDEX tickets_context_idx ON tickets USING gin (context jsonb_path_ops);
-- Telegram topic 反查（收到 bot 消息时定位工单）
CREATE UNIQUE INDEX tickets_tg_thread_idx
  ON tickets (telegram_chat_id, telegram_message_thread_id)
  WHERE telegram_message_thread_id IS NOT NULL;


-- ---------- 工单消息 ----------
CREATE TABLE ticket_messages (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id     bigint NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  actor_type    ticket_actor NOT NULL,
  -- 二选一：user_id 或 admin_user_id；actor_type='system' 时两者皆空
  user_id       bigint REFERENCES users(id)       ON DELETE SET NULL,
  admin_user_id bigint REFERENCES admin_users(id) ON DELETE SET NULL,

  body          text NOT NULL,
  body_format   text NOT NULL DEFAULT 'markdown'
                CHECK (body_format IN ('markdown','plain','html')),
  -- 🔴 全系统最容易出安全事故的一列。用户侧查询必须走 ticket_messages_public 视图，
  --    不接受调用方传 is_internal 参数（data-model §10.1 修改 1）。
  is_internal   boolean NOT NULL DEFAULT false,
  channel       ticket_channel NOT NULL DEFAULT 'web',
  external_id   text,                             -- 幂等去重（Telegram / 邮件 webhook 重复投递）
  created_at    timestamptz NOT NULL DEFAULT now(),
  edited_at     timestamptz,

  CONSTRAINT ticket_messages_actor_consistency CHECK (
    (actor_type = 'user'   AND user_id IS NOT NULL AND admin_user_id IS NULL) OR
    (actor_type = 'agent'  AND admin_user_id IS NOT NULL AND user_id IS NULL) OR
    (actor_type = 'system' AND user_id IS NULL AND admin_user_id IS NULL)
  ),
  -- 用户消息永远不能是内部备注
  CONSTRAINT ticket_messages_internal_only_agent
    CHECK (NOT (is_internal AND actor_type = 'user'))
);
CREATE INDEX ticket_messages_ticket_idx ON ticket_messages (ticket_id, created_at);
CREATE UNIQUE INDEX ticket_messages_external_idx
  ON ticket_messages (channel, external_id) WHERE external_id IS NOT NULL;

-- 🔴 用户侧查询只能走这个视图。视图是机制，「在 repository 层强制」只是约定。
--    部署时：用户侧 API 的 role 只被授予本视图的 SELECT，ticket_messages 表本身不授权。
CREATE VIEW ticket_messages_public AS
  SELECT id, ticket_id, actor_type, user_id, admin_user_id,
         body, body_format, channel, created_at, edited_at
  FROM ticket_messages WHERE is_internal = false;


-- ---------- 附件 ----------
CREATE TABLE ticket_attachments (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  message_id    bigint NOT NULL REFERENCES ticket_messages(id) ON DELETE CASCADE,
  storage_key   text   NOT NULL,        -- GCS 对象键，不存公开 URL
  filename      text   NOT NULL,
  content_type  text   NOT NULL,
  size_bytes    bigint NOT NULL CHECK (size_bytes > 0),
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_attachments_message_idx ON ticket_attachments (message_id);


-- ---------- SLA 策略 ----------
-- 全局 / 按套餐的 SLA 定义；分类上的覆盖优先级更高
CREATE TABLE sla_policies (
  id                         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name                       text NOT NULL UNIQUE,
  -- NULL 表示适用于所有套餐；否则仅适用于该套餐（付费用户 SLA 更短）
  plan_id                    bigint REFERENCES plans(id) ON DELETE CASCADE,
  priority                   ticket_priority NOT NULL,
  first_response_minutes     integer NOT NULL CHECK (first_response_minutes > 0),
  resolution_minutes         integer NOT NULL CHECK (resolution_minutes > 0),
  business_hours_only        boolean NOT NULL DEFAULT false,   -- 24/7 支持则为 false
  timezone                   text NOT NULL DEFAULT 'Asia/Shanghai',
  is_active                  boolean NOT NULL DEFAULT true,
  created_at                 timestamptz NOT NULL DEFAULT now(),
  UNIQUE (plan_id, priority)
);

-- SLA 违约记录；单独建表以便统计，而不是在 tickets 上加布尔列
CREATE TABLE ticket_sla_breaches (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id    bigint NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  breach_type  text   NOT NULL CHECK (breach_type IN ('first_response','resolution')),
  due_at       timestamptz NOT NULL,
  breached_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (ticket_id, breach_type)
);


-- ---------- 状态流转审计 ----------
CREATE TABLE ticket_events (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id     bigint NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  actor_type    ticket_actor NOT NULL,
  admin_user_id bigint REFERENCES admin_users(id) ON DELETE SET NULL,
  event_type    text NOT NULL,   -- 'status_changed' / 'assigned' / 'priority_changed' / 'tagged' / 'merged'
  from_value    text,
  to_value      text,
  metadata      jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_events_ticket_idx ON ticket_events (ticket_id, created_at);


-- ---------- 客服快捷回复 ----------
CREATE TABLE canned_responses (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  slug        text NOT NULL UNIQUE,
  title       text NOT NULL,
  body        text NOT NULL,          -- 支持 {{user_name}} {{subscription_url}} 等占位符
  locale      text NOT NULL DEFAULT 'zh-CN',
  category_id bigint REFERENCES ticket_categories(id) ON DELETE SET NULL,
  usage_count integer NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now()
);
