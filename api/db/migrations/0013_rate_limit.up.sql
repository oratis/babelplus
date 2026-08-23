-- 0013 · 限流计数（UNLOGGED）
--
-- 事实源：api-contract.md §10.1（分面限额）、§10.2「精确档：Postgres UNLOGGED 表
--         rate_limit(key, window_start, count)，INSERT … ON CONFLICT DO UPDATE 原子递增」
--         ADR 0005 §8「在线态用 UNLOGGED 表，不买 Redis」
--
-- 为什么这张表必须存在：Cloud Run --max-instances=8，进程内计数会被放大 8 倍。
-- 对「防雪崩」类限流这 8 倍是可接受的（api-contract §10.2 的近似档），
-- 但对**凭据爆破**与**邮件配额消耗**它是真实的安全损失 —— 一个 5/min 的登录限额
-- 实际变成 40/min。这两类必须走跨实例一致的计数，也就是这张表。
--
-- ============================================================
-- 为什么是 UNLOGGED（ADR 0005 §8 的同一条裁决，逐条对照本表）
-- ============================================================
--
-- 1. **不写 WAL。** 本表的写入模式是「每次受限请求一条 upsert」，是纯热写。
--    不进 WAL 意味着不占 db-f1-micro 的 IO 预算，也不进备份 ——
--    没有任何人想从备份里恢复一份「三小时前谁被限流了」。
--
-- 2. **崩溃 / 非正常关机后自动 TRUNCATE。** 这条是本表接受 UNLOGGED 的关键，
--    因为它是一次**故意的取舍**而不是顺带的好处：数据库崩溃重启后，
--    全部限流计数归零，等于一次全局的限流重置。
--    接受它的理由有三条，缺一条都不该接受：
--      · 窗口本来就短（本表 CHECK 强制 ≤ 1 小时），丢失的最多是一小时的计数；
--      · 能让 Cloud SQL 崩溃重启的攻击者，已经不需要靠爆破登录来达成目的了；
--      · 真正的对照组是「没有这张表」（今天的现状），而不是「一张永不丢失的表」。
--    ⚠️ 但要记住一个**非攻击**的触发源：Cloud SQL 的维护重启是计划内事件，
--       它同样会清空本表。也就是说限流窗口会周期性地被无声重置。
--
-- 3. **不复制到只读副本。** 与 0005 同一条警告，且后果更重：
--    在线态查不到只是「显示 0 台设备」，限流计数查不到是**限流器静默失效**
--    （空表 = 每次都是窗口内第一次 = 永远放行）。
--    🔴 将来加只读副本时，限流查询**必须**钉死在主库上。
--
-- 反方意见（记录在案，不是没想过）：限流是安全控制，安全控制用一张「崩溃即清空」的表
-- 看起来是自相矛盾的。之所以仍然选它，是因为上面第 3 点里那句话 ——
-- 对照组是「今天完全没有限流」。为一个还不存在的控制去买 $35.77/月的 Redis
-- （比整个数据库贵 3.7 倍）或者付 WAL 的代价，是把成本花在了错误的位置。

CREATE UNLOGGED TABLE rate_limit (
  -- bucket 同时编码「限哪个端点的哪个维度、窗口多长」，例如
  -- login_ip_1m / login_email_1h / email_code_ip_1h / invite_ip_1m。
  -- 窗口长度必须进 bucket 名：一行只持有一个窗口，把 1m 与 1h 塞进同一个 bucket
  -- 会让两条规则互相覆盖对方的 window_start。
  bucket         text   NOT NULL,

  -- subject 是**摘要，不是明文**。IP 还好，email 明文落库等于凭空多出一份
  -- 「谁在什么时候试过登录」的可枚举名单，而这张表的备份/权限约束比 users 弱
  -- （它是 UNLOGGED，谁都可能为了排障去 select）。
  -- 应用层用 HMAC-SHA256(pepper, bucket || subject) 生成，pepper 不落库。
  subject        bytea  NOT NULL,

  window_start   timestamptz NOT NULL DEFAULT now(),

  -- 窗口长度随行存储，让每一行自解释：清理作业不必知道各 bucket 的策略。
  -- 🔴 上限 3600 秒不是随手取的，它是下面那条清理语句的**正确性前提**：
  --    清理用 `window_start < now() - interval '1 hour'` 这个可走索引的粗筛，
  --    只有在「没有任何 bucket 的窗口超过 1 小时」时它才不会误删活跃窗口。
  --    把这条不变量交给 CHECK 而不是交给注释 —— 将来谁想加一个 24 小时的桶，
  --    会在 INSERT 上被拒绝，而不是在某天发现限流悄悄失效。
  window_seconds integer NOT NULL CHECK (window_seconds BETWEEN 1 AND 3600),

  hits           integer NOT NULL DEFAULT 0,

  PRIMARY KEY (bucket, subject)
);

-- 清理用。DELETE 的粗筛条件是 window_start < now() - <最长窗口>，走这条索引。
CREATE INDEX rate_limit_window_start_idx ON rate_limit (window_start);

-- 刻意不建外键、不建 updated_at 触发器：本表是派生的易失计数，
-- 不参与引用完整性；崩溃 TRUNCATE 时也不该去校验任何东西（与 0005 同）。
