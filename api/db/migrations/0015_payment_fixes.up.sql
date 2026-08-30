-- 0015 · 收款上线前的五处 schema 修复 + 科目 seed（ADR 0012 §17）
--
-- 事实源：ADR 0012 §17.2（一单一址的唯一索引）、§17.3（判定量纲回到 bigint）、
--         §17.4（char(3) 装不下 'USDT'）、§17.5（used_totp）、§17.6（账本科目与 seed）；
--         落地清单逐字见 §20 第 4 步。
--
-- 这五项的共同点：**每一项都会在「第一笔真实收款」那一刻炸，而且都不是编译期错误。**
-- 分成独立一支 migration 而不是并进 0014，是因为 0014 只加新表（可以先合、先派生地址），
-- 而本支要改四张已有表的列类型，回滚风险不同 —— 合在一起会让「只回滚风险高的那一半」不可能。
--
-- 🔴 本文件内部有一处顺序是硬要求：**先把 currency 拓宽，再插科目 seed**。
--    seed 里有 currency='USDT'（4 字符），插进 char(3) 会直接报
--    `value too long for type character(3)`。反过来的顺序在 CI 上会当场失败。

-- ============================================================
-- ① §17.2 · 归属从「地址 + 金额尾数」回到「地址」
-- ============================================================
--
-- 旧索引 `orders_pay_addr_amount_uk (pay_address, pay_amount_raw)` 是金额尾数递增匹配机制的
-- 数据库表达（EPUSDT 那一套）。本裁决删掉了那套机制（ADR 0012 §5.4），索引必须跟着走 ——
-- 留着它不只是冗余：它允许**同一个地址挂多张未终结订单**，而一单一址之下这是非法状态，
-- 留着等于把「到账归属到哪张订单」重新变成一次模糊匹配。
--
-- 旁证（ADR 0012 §17.2）：`orders.pay_address` 的列注释本来就是「**本单专属收款地址**」
-- （`0006_orders.up.sql:58`）—— 既有 schema 的作者当初假设的就是一单一址，金额尾数是后来焊上去的。
-- 本节是把 schema 和机制重新对齐。
--
-- ⚠️ 新索引的条件比旧索引**宽**：旧的只约束 status IN ('pending','paying','underpaid')，
--    新的对**任何**状态的订单都生效。这是刻意的 —— §11.1「这个地址永远认账」要求地址与订单的
--    绑定在订单终结之后依然成立，否则过期订单的地址会被重新分配，而那笔迟到的钱就归属不到人。
DROP INDEX orders_pay_addr_amount_uk;
CREATE UNIQUE INDEX orders_pay_addr_uk ON orders (pay_address) WHERE pay_address IS NOT NULL;


-- ============================================================
-- ② §17.3 · 判定量纲回到 bigint
-- ============================================================
--
-- 冻结契约里金额是 `amount_usdt6: integer(int64)`，`amount_display` 明确是**字符串**
-- （原文：「展示用字符串，不是数值类型 —— 不给浮点留口子」）。
-- 但 DB 侧 `orders.pay_amount_raw` 是 `numeric(38,18)`，**类型本身容得下链上不可能出现、
-- 且互不相等的值**（1e-18 量级的尾数）。拿它跟链上实收比大小，是拿一个可以承载噪声的类型
-- 去做一个必须精确的判定。
ALTER TABLE orders ADD COLUMN pay_amount_usdt6 bigint;
COMMENT ON COLUMN orders.pay_amount_usdt6 IS
  '本单应收，1e-6 USDT 整数。🔴 paid / underpaid / 写销的判定**只读这一列**（ADR 0012 §17.3）。
   pay_amount_raw 保留为链上等值比对与记录证据，按 0006 的量纲铁律「不参与任何货币再计算」。';


-- ============================================================
-- ③ §17.4 · char(3) 装不下 'USDT'
-- ============================================================
--
-- 实查四处全部是 char(3)：`orders.currency`（0006:41）、`ledger_accounts.currency`（0007:18）、
-- `ledger_lines.currency`（0007:39）、`wallet_balances.currency`（0007:57）。
-- **Postgres 向 char(3) 插入 'USDT'（4 字符）直接报 `value too long for type character(3)`** ——
-- 这不是静默截断（`bpchar` 只截断尾随空格）。也就是说：不改这四列，
-- §17.6(b) 那张跨币种收款凭证**一行都写不进去**。
--
-- ⚠️ 与 ADR 原文的一处冲突，实测后按实测走：§17.4 只给了四条裸 ALTER，
--    但 `ledger_lines.currency` 被 `user_wallet_balance` 视图（0007）选中，
--    直接 ALTER 会报 `cannot alter type of a column used by a view or rule`。
--    所以这里必须 DROP VIEW → ALTER → 按 0007 原文重建。视图定义**一字未改**，
--    重建后 0007.down 的 `DROP VIEW IF EXISTS user_wallet_balance` 仍然对得上。
ALTER TABLE orders          ALTER COLUMN currency TYPE varchar(8);
ALTER TABLE ledger_accounts ALTER COLUMN currency TYPE varchar(8);
ALTER TABLE wallet_balances ALTER COLUMN currency TYPE varchar(8);

DROP VIEW user_wallet_balance;
ALTER TABLE ledger_lines    ALTER COLUMN currency TYPE varchar(8);
-- 0007 原文，逐字重建（唯一真相：余额是分录的聚合）
CREATE VIEW user_wallet_balance AS
SELECT l.subject_id AS user_id, l.currency, -SUM(l.amount) AS balance
FROM ledger_lines l JOIN ledger_accounts a ON a.id = l.account_id
WHERE a.code = 'liability:user_wallet'
GROUP BY l.subject_id, l.currency;


-- ============================================================
-- ④ §17.5 · used_totp：D6 的防重放
-- ============================================================
--
-- api-contract §1261 与生成的 `api.gen.go`（8 处）都要求「同一 code 5 分钟内只能用一次
-- （防重放，需 used_totp 表）」，而实查 44 张表里**没有它**。
-- 没有它，ADR 0012 §20 的验收项「用 curl 逐个绕过 L1/L2/L3/L4，四次全部失败」
-- **结构上无法通过** —— L3（TOTP）会被同一个 code 重放穿过去。
--
-- **它属于 D6，而 D6 是 ADR 0012 亲手扩大的欺诈面，所以它属于这一批。**
CREATE TABLE used_totp (
  -- CASCADE 而不是 RESTRICT：这是易失的防重放痕迹，不是账目凭证。
  -- 管理员被删时把他的 5 分钟窗口一起带走没有任何证据损失（真正的证据在 audit_logs）。
  admin_user_id bigint NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,

  -- 不存明文 code。明文落库等于凭空多出一份「6 位数字 + 时间」的对照表，
  -- 而 TOTP 的窗口很短、取值空间很小 —— 一份历史明文表配上时钟就能反推 secret 的对齐关系。
  code_hash     bytea  NOT NULL,

  used_at       timestamptz NOT NULL DEFAULT now(),

  -- 主键即防重放：第二次用同一个 code 撞主键失败。
  -- 🔴 靠数据库拒绝而不是靠应用层 SELECT-then-INSERT —— 后者在并发重放下会双双通过，
  --    而「并发重放」正是自动化攻击的默认形态。
  PRIMARY KEY (admin_user_id, code_hash)
);
-- 由 /internal/tasks/* 定期清理 `used_at < now() - interval '10 minutes'` 的行。
-- 10 分钟而不是 5 分钟：TOTP 校验允许 ±1 个时间步的漂移，按 5 分钟清会在边界上放过一次重放。
CREATE INDEX used_totp_gc_idx ON used_totp (used_at);


-- ============================================================
-- ⑤ §17.6 · 科目 seed
-- ============================================================
--
-- 实查 `0007_ledger.up.sql`：`INSERT INTO ledger_accounts` 出现 **0 次**，科目表是空的。
-- **没有这段 seed，第一笔收款会因为找不到科目而失败** —— 而且是在用户已经把 USDT 打过来之后失败。
--
-- 币种是 per-account 的（§17.6(b)：「`ledger_accounts.currency` 是 per-account 的，
-- 所以必须建**两个** fx_clearing 科目，不能共用一个 code」），所以下面每一行的 currency
-- 都必须与它实际承载的分录腿一致，写错的后果是分录按 (entry_id, currency) 分组之后**不平**，
-- 而 §17.6(d) 断言 2 每天都会因此报红。
--
-- 前 7 行来自 §17.6(c) 的科目表（`asset:crypto:tron:cold` 按 §13.1「第一阶段一次都不归集」**不建**）。
-- 后 3 行不在那张表里，但被本批裁决自己的分录逐字点名，缺一条就是一次运行时 500：
--   · liability:user_wallet     —— 0007 的 user_wallet_balance 视图把这个 code **硬编码**在 WHERE 里；
--                                  §6.2 超额入余额、ADR 0013 §3.5 退款入余额都走它
--   · liability:deferred_revenue —— §17.6(b) 收款凭证的贷方腿，也是 §16.2 D6 凭证的贷方腿
--   · expense:refund            —— ADR 0013 §3.5 明确：只用于 destination='original'
--                                  以及追不回来的佣金；destination='balance' 绝不许碰它
--
-- ⚠️ 一处 ADR 未裁决、由本文件补的选择：`expense:chain_fee` 的币种取 **TRX**。
--    §17.6(c) 只写了「出金那天的手续费与能量租赁」而没写币种；TRON 上这两样都是 TRX 计价，
--    记成 USDT 会凭空引入一次从未发生的兑换。**推翻条件**：出金那天若手续费实际以别的方式支付，
--    按 fx_clearing 的先例**另建一个科目**，而不是改这一行的币种 —— 改币种会让历史分录的
--    (entry_id, currency) 分组事后失衡。
INSERT INTO ledger_accounts (code, kind, currency) VALUES
  -- §17.6(c)：全部收款地址合计，`subject_id = pay_addresses.id` 分账（不归集之下这是必须的）
  ('asset:crypto:tron:pool',    'asset',     'USDT'),
  -- §16.2 D6 专用。🔴 余额长期非零 = 有人标了「已支付」但钱没进来 ——
  -- 这把「全系统最大的内部欺诈面」变成一个可以每天看一眼的数字。分录是 CNY 计价（§16.2 原文）。
  ('asset:manual_reconcile',    'asset',     'CNY'),
  -- §17.6(b) 跨币种桥接的两条腿。两者累积的净额就是「以 CNY 标价、以 USDT 收款」的汇率敞口。
  ('equity:fx_clearing:USDT',   'equity',    'USDT'),
  ('equity:fx_clearing:CNY',    'equity',    'CNY'),
  -- §14：出金那天的链上手续费与能量租赁，第一阶段为空。币种见上面的 ⚠️。
  ('expense:chain_fee',         'expense',   'TRX'),
  -- §6.1 A 档写销。差额是 1e-6 USDT 量纲的链上少收，不是人民币费用。
  ('expense:payment_shortfall', 'expense',   'USDT'),
  -- §15.2 的 1% 汇率缓冲，**明记不藏**。缓冲加在 CNY 标价上，所以是 CNY。
  ('revenue:fx_buffer',         'revenue',   'CNY'),
  -- 下面三条见上面的说明段
  ('liability:user_wallet',     'liability', 'CNY'),
  ('liability:deferred_revenue','liability', 'CNY'),
  ('expense:refund',            'expense',   'CNY');
