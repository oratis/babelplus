-- 0015 down · 收款上线前的五处 schema 修复 + 科目 seed
--
-- 🔴 顺序是硬要求，理由与 up 那条镜像对称：**seed 必须先删干净，才能把 currency 收回 char(3)**。
--    科目里有 'USDT' / 'TRX'（4 与 3 字符），先窄化会在 'USDT' 那一行上失败。
--
-- ⚠️ 生产回滚的两条前提，CI 的空库上都碰不到，所以写在这里而不是靠人记：
--    1. `DELETE FROM ledger_accounts` 会被 `ledger_lines.account_id` 的 ON DELETE RESTRICT 挡住
--       —— 只要账上已经有分录，这支 down 就跑不动。**这是正确行为**：
--       科目表被抽掉之后，历史分录会变成读不出科目名的孤儿，那比回滚失败严重得多。
--       真要回滚，必须先把分录处理掉，而那是一次人工决策，不该由一支 migration 替人做。
--    2. `ALTER COLUMN currency TYPE char(3)` 在 orders / ledger_lines / wallet_balances 上
--       如果已经有 'USDT' 行，同样会失败。同上，这也是正确行为。

-- ---- ⑤ 科目 seed（必须最先删）----
-- 只删本文件插入的那 10 行，不 TRUNCATE：将来若有人手工加了科目，回滚这一支不该把它带走。
DELETE FROM ledger_accounts WHERE code IN (
  'asset:crypto:tron:pool',
  'asset:manual_reconcile',
  'equity:fx_clearing:USDT',
  'equity:fx_clearing:CNY',
  'expense:chain_fee',
  'expense:payment_shortfall',
  'revenue:fx_buffer',
  'liability:user_wallet',
  'liability:deferred_revenue',
  'expense:refund'
);

-- ---- ④ used_totp ----
-- 索引随表一起消失，不必单独 DROP INDEX。
DROP TABLE IF EXISTS used_totp;

-- ---- ③ currency 收回 char(3) ----
-- ledger_lines 上同样要先拆视图再改类型（`cannot alter type of a column used by a view or rule`），
-- 改完按 0007 原文重建 —— 重建的是同一份定义，所以 0007.down 的 DROP VIEW 仍然对得上。
DROP VIEW user_wallet_balance;
ALTER TABLE ledger_lines    ALTER COLUMN currency TYPE char(3);
CREATE VIEW user_wallet_balance AS
SELECT l.subject_id AS user_id, l.currency, -SUM(l.amount) AS balance
FROM ledger_lines l JOIN ledger_accounts a ON a.id = l.account_id
WHERE a.code = 'liability:user_wallet'
GROUP BY l.subject_id, l.currency;

ALTER TABLE wallet_balances ALTER COLUMN currency TYPE char(3);
ALTER TABLE ledger_accounts ALTER COLUMN currency TYPE char(3);
ALTER TABLE orders          ALTER COLUMN currency TYPE char(3);

-- ---- ② 判定量纲列 ----
COMMENT ON COLUMN orders.pay_amount_usdt6 IS NULL;
ALTER TABLE orders DROP COLUMN pay_amount_usdt6;

-- ---- ① 归属索引换回金额尾数版 ----
-- 逐字还原 0006 的定义（含那条部分索引条件），否则「回滚后再灌一次 up」会得到两个不同的库。
DROP INDEX orders_pay_addr_uk;
CREATE UNIQUE INDEX orders_pay_addr_amount_uk ON orders (pay_address, pay_amount_raw)
  WHERE pay_address IS NOT NULL AND status IN ('pending','paying','underpaid');
