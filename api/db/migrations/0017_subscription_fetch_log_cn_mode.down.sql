-- 0017 down · subscription_fetch_log.cn_mode
--
-- 列注释随列一起消失，不必单独 `COMMENT … IS NULL`（这与 0016.down 里那几条不同：
-- 那边摘的是**既有列**上新加的注释，列本身不删，所以必须显式摘掉）。
--
-- ⚠️ 回滚会丢掉已采集的 cn_mode 观测数据，且不可恢复。可以接受的理由只有一条：
--    这张表本来就是 90 天硬删的日志表（data-model §5.4 / §13），它不是任何东西的真相源。

ALTER TABLE subscription_fetch_log DROP COLUMN cn_mode;
