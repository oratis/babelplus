-- 0009 · 统计
--
-- 事实源：data-model.md §9
-- 职责：把「流量不落明细流水」这条性能命门变成一张表。
--
-- 🔴 裁决：只落一张日聚合实表 stat_user_server，stat_user 与 stat_server 是它的视图。
--    Xboard 写三处（user.u/d + stat_user + stat_server），三份数字可能对不上且没有任何机制发现；
--    我们写两处（user_traffic + stat_user_server），视图恒等于实表，对不上在结构上不可能。
--
-- ⚠️ 不按倍率分桶（Xboard v2_stat_user 的唯一键是 (server_rate, user_id, record_at)）。
--    第一阶段不引入倍率（product-brief §6）。引入倍率时这张表要重建。

CREATE TABLE stat_user_server (
  user_id   bigint NOT NULL REFERENCES users(id)   ON DELETE CASCADE,
  -- ON DELETE RESTRICT：节点的成本历史不能因为节点被删而消失 —— 所以节点只软删，从不真删
  server_id bigint NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
  stat_date date   NOT NULL,   -- 口径：按 Asia/Shanghai 切天（报表要显式声明这一点）
  u         bigint NOT NULL DEFAULT 0,   -- 字节
  d         bigint NOT NULL DEFAULT 0,   -- 字节
  PRIMARY KEY (user_id, server_id, stat_date)
);
-- 按节点做出口成本核算 / 按天做全局曲线，都要能不带 user_id 扫
CREATE INDEX stat_user_server_server_date_idx ON stat_user_server (server_id, stat_date);
CREATE INDEX stat_user_server_date_idx        ON stat_user_server (stat_date);
-- ⚠️ 刻意不建任何包含 u/d 的索引（data-model §12.3）。

-- 视图恒等于实表：接口名与 system-design §6.3 一致，但不多写一份数字。
-- 触发改成物化视图的阈值：当「全站年度按节点聚合」超过 1 秒时（data-model §9.2）。
CREATE VIEW stat_user AS
  SELECT user_id, stat_date, sum(u) AS u, sum(d) AS d
  FROM stat_user_server GROUP BY user_id, stat_date;

CREATE VIEW stat_server AS
  SELECT server_id, stat_date, sum(u) AS u, sum(d) AS d
  FROM stat_user_server GROUP BY server_id, stat_date;
