-- 0007 · 钱包与返佣（复式记账）
--
-- 事实源：data-model.md §7，整体沿用 payments.md §4.13 的复式记账设计。
--
-- 核心不变量：
--   ∀ entry: SUM(lines.amount) = 0                 -- 借贷必相等
--   ∀ user:  余额 = -SUM(lines.amount WHERE account = 'liability:user_wallet' AND subject_id = uid)
--   ∀ time:  余额 >= 0
--
-- ⚠️ 「余额不可提现」在数据库层面无法强制。它的实现方式是：ledger_accounts 里
--    **不存在** asset:bank ← liability:user_wallet 这条分录路径，且**没有写提现代码**。
--    数据库能保证的只有 balance >= 0。真正的守卫是 code review 与审计日志。

CREATE TABLE ledger_accounts (
  id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code     text    NOT NULL UNIQUE,   -- 'liability:user_wallet' / 'revenue:subscription' / ...
  kind     text    NOT NULL CHECK (kind IN ('asset','liability','equity','revenue','expense')),
  currency char(3) NOT NULL
);

-- append-only：由 DB 层 REVOKE UPDATE/DELETE 强制（data-model §11.1），纠错用反向冲正
CREATE TABLE ledger_entries (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  entry_no    text NOT NULL UNIQUE,
  description text NOT NULL,
  ref_type    text,                    -- order | refund | commission | reconcile_adjust
  ref_id      bigint,
  reverses_id bigint REFERENCES ledger_entries(id),   -- 冲正指向原分录
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ledger_entries_ref_idx ON ledger_entries (ref_type, ref_id);

CREATE TABLE ledger_lines (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  entry_id   bigint  NOT NULL REFERENCES ledger_entries(id) ON DELETE RESTRICT,
  account_id bigint  NOT NULL REFERENCES ledger_accounts(id) ON DELETE RESTRICT,
  subject_id bigint,                   -- user_id（liability:user_wallet 分账用）
  amount     bigint  NOT NULL,         -- 有符号最小货币单位：正 = 借 Dr，负 = 贷 Cr。🔴 禁止 float
  currency   char(3) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ledger_lines_entry_idx   ON ledger_lines (entry_id);
CREATE INDEX ledger_lines_subject_idx ON ledger_lines (account_id, subject_id);

-- 唯一真相：余额是分录的聚合
CREATE VIEW user_wallet_balance AS
SELECT l.subject_id AS user_id, l.currency, -SUM(l.amount) AS balance
FROM ledger_lines l JOIN ledger_accounts a ON a.id = l.account_id
WHERE a.code = 'liability:user_wallet'
GROUP BY l.subject_id, l.currency;

-- 性能缓存：面板每次打开都要读余额，不能每次扫分录。
-- ⚠️ 这是缓存不是真相。每日 Cloud Scheduler 必须跑一次与 user_wallet_balance 的比对，
--    返回非空行 = 立即告警，且以视图为准（data-model §7.1）。
CREATE TABLE wallet_balances (
  user_id           bigint  PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  currency          char(3) NOT NULL DEFAULT 'CNY',
  balance           bigint  NOT NULL DEFAULT 0 CHECK (balance >= 0),  -- 分；不可为负
  last_entry_id     bigint,          -- 已计入的最后一条分录，用于增量对账
  updated_at        timestamptz NOT NULL DEFAULT now()
);


CREATE TABLE commissions (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id     bigint NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  inviter_id   bigint NOT NULL REFERENCES users(id)  ON DELETE RESTRICT,
  invitee_id   bigint NOT NULL REFERENCES users(id)  ON DELETE RESTRICT,
  rate_bps     integer NOT NULL CHECK (rate_bps BETWEEN 0 AND 10000),  -- 1000 = 10%
  amount       bigint  NOT NULL CHECK (amount >= 0),                   -- 分
  -- 两段式：确认中 → 已获得（pricing §5）
  status       text    NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending','confirmed','transferred','voided')),
  confirm_at   timestamptz NOT NULL,        -- 冷静期到期时刻（防退款套利）
  confirmed_at timestamptz,
  voided_reason text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  -- 🔴 一单只产生一条佣金 —— 把「Cloud Tasks 至少一次投递导致佣金重复发放」这个
  --    ADR 0006 点名的风险，从应用逻辑降级成数据库拒绝。
  UNIQUE (order_id)
);
CREATE INDEX commissions_inviter_idx ON commissions (inviter_id, status);
CREATE INDEX commissions_due_idx     ON commissions (confirm_at) WHERE status = 'pending';


CREATE TABLE refunds (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id    bigint NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  amount      bigint NOT NULL CHECK (amount > 0),     -- 分
  destination text   NOT NULL CHECK (destination IN ('balance','original')),
  status      text   NOT NULL DEFAULT 'pending'
              CHECK (status IN ('pending','done','failed','cancelled')),
  gateway_ref text,
  reason      text,
  operator_id bigint REFERENCES admin_users(id) ON DELETE SET NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refunds_order_idx ON refunds (order_id, created_at DESC);
