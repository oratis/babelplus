-- 0006 · 订单
--
-- 事实源：data-model.md §6
-- 职责：卖什么、卖多少钱、这笔钱到没到、到了之后把什么权利写进 users。
--
-- 🔴 量纲铁律：本文件里所有 amount_* / value / min_amount 一律 bigint 存**人民币分**。
--    唯一的非整数是 pay_amount_raw / pay_amount_received（链上 USDT 数量）与 fx_usdt_per_cny，
--    它们是与链上余额的等值比对与记录证据，**不参与任何货币再计算**。

CREATE TABLE coupons (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code             text    NOT NULL,                 -- 存 upper()
  name             text    NOT NULL DEFAULT '',
  type             text    NOT NULL CHECK (type IN ('percentage','fixed_amount')),
  value            bigint  NOT NULL CHECK (value > 0),   -- percentage = bps（1000=10%）；fixed = 分
  scope_plan_ids   bigint[] NOT NULL DEFAULT '{}',       -- 空 = 不限套餐
  scope_periods    order_period[] NOT NULL DEFAULT '{}', -- 空 = 不限周期
  min_amount       bigint  NOT NULL DEFAULT 0,           -- 分
  total_uses       integer,                              -- NULL = 不限
  used_count       integer NOT NULL DEFAULT 0,
  uses_per_user    integer NOT NULL DEFAULT 1,
  first_order_only boolean NOT NULL DEFAULT false,
  starts_at        timestamptz,
  ends_at          timestamptz,
  visible          boolean NOT NULL DEFAULT false,
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX coupons_code_uk ON coupons (upper(code));


CREATE TABLE orders (
  id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  trade_no          text        NOT NULL UNIQUE,  -- 对外单号，即 page-inventory 的 /order/:trade_no
  user_id           bigint      NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  type              order_type  NOT NULL,
  plan_id           bigint      REFERENCES plans(id) ON DELETE RESTRICT,
  period            order_period,
  status            order_status NOT NULL DEFAULT 'pending',

  -- ---- 金额：全部人民币分 ----
  currency          char(3) NOT NULL DEFAULT 'CNY',
  amount_gross      bigint  NOT NULL DEFAULT 0 CHECK (amount_gross    >= 0),  -- 标价
  amount_discount   bigint  NOT NULL DEFAULT 0 CHECK (amount_discount >= 0),  -- 优惠码
  surplus_amount    bigint  NOT NULL DEFAULT 0 CHECK (surplus_amount  >= 0),  -- 升级折抵
  amount_balance    bigint  NOT NULL DEFAULT 0 CHECK (amount_balance  >= 0),  -- 余额抵扣
  amount_due        bigint  NOT NULL           CHECK (amount_due      >= 0),  -- 网关应收
  amount_paid       bigint  NOT NULL DEFAULT 0 CHECK (amount_paid     >= 0),
  amount_refunded   bigint  NOT NULL DEFAULT 0 CHECK (amount_refunded >= 0),
  surplus_order_ids bigint[] NOT NULL DEFAULT '{}',   -- 被折抵掉的历史订单（Xboard 同名字段）

  coupon_id         bigint  REFERENCES coupons(id) ON DELETE SET NULL,
  invited_by        bigint  REFERENCES users(id)   ON DELETE SET NULL,  -- 下单瞬间的邀请人快照

  -- ---- 支付通道 ----
  gateway           text,                 -- 'usdt_trc20' | 'usdt_erc20' | 'usdt_bep20' | 'epay:*'
  gateway_ref       text,                 -- 网关侧交易号 / 链上 txid
  pay_chain         text,                 -- 'tron' | 'ethereum' | 'bsc'
  pay_address       text,                 -- 本单专属收款地址
  pay_amount_raw    numeric(38,18),       -- 链上应收数量（含四位小数的订单识别尾数）
  pay_amount_received numeric(38,18) NOT NULL DEFAULT 0,   -- 实收，underpaid 判定用
  fx_usdt_per_cny   numeric(20,10),       -- 下单时锁定汇率，只作记录与申诉证据，不参与再计算
  fx_locked_at      timestamptz,

  -- ---- 时间 ----
  expires_at           timestamptz NOT NULL,       -- 支付窗口（倒计时）
  address_watch_until  timestamptz,                -- 收款地址继续监听到此刻（≥ 24h，user-journey §7）
  paid_at              timestamptz,
  completed_at         timestamptz,
  cancelled_at         timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),

  -- 「三行金额加起来对不上」在插入时就被数据库拒绝，而不是在对账时才发现。
  -- ⚠️ payments.md §4.13 的等式漏了 surplus_amount 这一项，这里补上。
  CONSTRAINT orders_amount_balance
    CHECK (amount_due = amount_gross - amount_discount - surplus_amount - amount_balance),
  CONSTRAINT orders_refund_le_paid CHECK (amount_refunded <= amount_paid)
);

CREATE INDEX orders_user_idx ON orders (user_id, created_at DESC);
CREATE INDEX orders_expiry_idx ON orders (status, expires_at)
  WHERE status IN ('pending','paying','underpaid');
CREATE UNIQUE INDEX orders_gateway_ref_uk ON orders (gateway, gateway_ref)
  WHERE gateway_ref IS NOT NULL;
-- 地址 + 唯一金额的组合，在「未终结」的订单里必须唯一（EPUSDT 的金额尾数递增法）
CREATE UNIQUE INDEX orders_pay_addr_amount_uk ON orders (pay_address, pay_amount_raw)
  WHERE pay_address IS NOT NULL AND status IN ('pending','paying','underpaid');
-- ⚠️ 不能写成 WHERE address_watch_until > now()：now() 不是 IMMUTABLE，建索引会直接报
--    functions in index predicate must be marked IMMUTABLE。
--    把列放进索引键，让查询条件走范围扫描。
CREATE INDEX orders_addr_watch_idx ON orders (pay_address, address_watch_until)
  WHERE pay_address IS NOT NULL;


-- 状态流转审计（不可变，沿用 payments.md §4.13）
-- append-only 由 DB 层 REVOKE UPDATE/DELETE 强制，见 data-model §11.1
CREATE TABLE order_transitions (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id    bigint NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  from_status order_status,
  to_status   order_status NOT NULL,
  reason      text,
  actor       text NOT NULL,        -- system | webhook:<gw> | admin:<id> | user:<id> | chain:<txid>
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX order_transitions_order_idx ON order_transitions (order_id, created_at);


-- 幂等与 webhook 重放防护（沿用 payments.md §4.13）
CREATE TABLE idempotency_keys (
  key           text PRIMARY KEY,
  user_id       bigint,
  endpoint      text NOT NULL,
  request_hash  text NOT NULL,
  status        text NOT NULL CHECK (status IN ('in_progress','completed')),
  response_code integer,
  response_body jsonb,
  created_at    timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL DEFAULT now() + interval '24 hours'
);
CREATE INDEX idempotency_keys_expiry_idx ON idempotency_keys (expires_at);

CREATE TABLE webhook_events (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  gateway       text NOT NULL,
  event_id      text NOT NULL,      -- 通道 event id；无则用 (trade_no || ':' || status)
  event_type    text,
  payload_hash  text NOT NULL,      -- sha256(raw_body)
  raw_body      text NOT NULL,      -- 存原文，对账与申诉用
  signature_ok  boolean NOT NULL,
  processed_at  timestamptz,
  error         text,
  received_at   timestamptz NOT NULL DEFAULT now(),
  -- 🔴 重放防护的核心：易支付回调可被伪造（NewAPI 的真实漏洞），
  --    幂等靠数据库唯一约束比靠应用层判断可靠。
  UNIQUE (gateway, event_id)
);
-- 2 年硬删（data-model §13：拒付申诉的证据窗口）
CREATE INDEX webhook_events_received_idx ON webhook_events (received_at);


-- 流量重置审计（抄 Xboard v2_traffic_reset_logs）
CREATE TABLE traffic_reset_log (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id        bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  trigger_source text   NOT NULL CHECK (trigger_source IN ('scheduler','order','admin','pack')),
  reset_method   reset_method,
  old_u          bigint NOT NULL,
  old_d          bigint NOT NULL,
  new_transfer_enable bigint NOT NULL,
  order_id       bigint REFERENCES orders(id) ON DELETE SET NULL,
  admin_user_id  bigint,
  reset_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX traffic_reset_log_user_idx ON traffic_reset_log (user_id, reset_at DESC);
