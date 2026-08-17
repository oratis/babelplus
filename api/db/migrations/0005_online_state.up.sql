-- 0005 · 在线态（UNLOGGED）
--
-- 事实源：data-model.md §8（在线态两表）、ADR 0005 §8「用 UNLOGGED 表，不买 Redis」
--
-- 裁决理由：Memorystore Basic M1 1 GiB = $35.77/月，比整个数据库还贵 3.7 倍。
-- UNLOGGED 的三条语义恰好就是在线态应该有的语义：
--   1. 不写 WAL —— 写入快
--   2. 崩溃后自动 TRUNCATE —— 在线态本来就该丢
--   3. 不复制到只读副本 —— ⚠️ 将来加只读副本做报表时，在线态查询不能路由过去
--
-- ⚠️ 这两张表刻意不建外键：它们是派生的易失状态，不参与引用完整性；
--    崩溃 TRUNCATE 时也不该去校验任何东西。

CREATE UNLOGGED TABLE server_online_state (
  server_id    bigint  PRIMARY KEY,
  online_users integer NOT NULL DEFAULT 0,
  last_push_at timestamptz,
  cpu_pct      real    CHECK (cpu_pct BETWEEN 0 AND 100),  -- 百分比 0–100，沿用 Xboard /status 校验区间
  mem_total    bigint, mem_used  bigint,                   -- 字节
  swap_total   bigint, swap_used bigint,
  disk_total   bigint, disk_used bigint,
  reported_at  timestamptz NOT NULL DEFAULT now()
);

-- 设备在线态。
-- 🔴 主键锁死了「按 IP 计设备数」这个口径（data-model §8.5 / §15.5）：
--    同一台手机切换 Wi-Fi/蜂窝会占两个名额。撤回条件是 P2 阶段设备数工单 > 总量 10%。
CREATE UNLOGGED TABLE user_device_state (
  user_id      bigint NOT NULL,
  server_id    bigint NOT NULL,
  device_ip    inet   NOT NULL,
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, server_id, device_ip)
);
-- Cloud Scheduler 每 5 分钟 DELETE ... WHERE last_seen_at < now() - interval '5 minutes'
CREATE INDEX user_device_state_seen_idx ON user_device_state (last_seen_at);
