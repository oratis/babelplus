-- 0018 down · 撤掉 expense:commission 科目
--
-- 🔴 **只删本文件插入的那一行。** 0015 seed 的 10 条由 0015.down 负责 ——
--    这里多删一条，0015.down 就会静默少删一条（DELETE ... IN (...) 不报「没删到」），
--    而两支 down 各自看都成功。回滚的正确性没有断言在盯，只有这条纪律。
--
-- ⚠️ 与 0015.down 同源的一条生产前提，写在这里而不是靠人记：
--    `ledger_lines.account_id` 是 ON DELETE RESTRICT（0007:36），
--    所以**只要账上已经有一条佣金划转分录，这支 down 就跑不动**。
--    这是正确行为：科目被抽掉之后，历史分录会变成读不出科目名的孤儿。
--    真要回滚必须先处理分录，而那是一次人工决策，不该由一支 migration 替人做。
--    CI 的空库上碰不到这一条（migrate-verify 从不写一行数据）。
--
-- ⚠️ 顺序上本文件先于 0015.down 执行（逆序回滚），所以 0015.down 那句
--    `ALTER TABLE ledger_accounts ALTER COLUMN currency TYPE char(3)` 不受本行影响。
--    即便执行到那里本行还在，'CNY' 也是 3 字符、窄化不会失败 ——
--    但那意味着回滚后库里留下一条谁都不认领的孤儿科目，所以仍然必须在这里删掉。

DELETE FROM ledger_accounts WHERE code = 'expense:commission';
