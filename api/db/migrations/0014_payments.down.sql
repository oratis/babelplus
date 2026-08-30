-- 0014 down · 收款地址池与入账流水
--
-- 顺序：payments 先于 pay_addresses —— 两者之间没有外键，但两者都外键指向 orders，
-- 而 payments 还指向 users / ledger_entries。按「引用者先走」的一贯写法排，
-- 避免将来有人往两表之间加外键时这支 down 才失败。
-- 索引随表一起消失，不必单独 DROP INDEX（与 0013 同）。
--
-- ⚠️ 枚举类型必须一并删掉：CI 的回滚断言把「残留枚举类型」当失败
-- （`.github/workflows/ci.yml` 的「断言回滚后归零」步骤），理由是重复灌库会报
-- `type "payment_state" already exists`，属于 down 没写干净。

DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS pay_addresses;

DROP TYPE IF EXISTS payment_state;
