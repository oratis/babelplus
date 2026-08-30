-- 0014 · 收款：一单一址的地址池 + 入账流水（ADR 0012）
--
-- 事实源：ADR 0012 §8.5（本文件逐字落地那一节的 DDL）、§8.2（幂等键的唯一取值来源）、
--         §5（一单一址、永不复用）、§6.3（累计口径）、§12（AML 判定落列）；
--         契约 openapi/openapi.yaml 的 `AdminPayment` 与 `PaymentState`（已冻结，不改）。
--
-- ⚠️ 编号从 0014 起而不是 0013：`0013_rate_limit` 已被 2026-08-23 合并的 PR #15 占用（ADR 0012 §8.5）。
--
-- ============================================================
-- 为什么这两张表非建不可（不依赖 openapi 的描述文字，只依赖既有 DDL 的形状）
-- ============================================================
--
-- `orders.gateway_ref` 是 `orders` 上的**一列**（`0006_orders.up.sql`，注释「网关侧交易号 / 链上 txid」），
-- 所以一张订单只能记住**一个** txid。而冻结契约强制要求支持的 `underpaid` 补足场景
-- （用户少付后向同一地址再打一笔）必然产生**一张订单对应两笔链上转账**。
-- **既有 schema 在结构上无法表达契约强制要求的状态 —— 这是一对多关系缺一张表。**
-- 不建它的后果不是「少一个功能」，是 `GET /admin/payments`、`/admin/payments/underpaid`、
-- `PATCH /admin/payments/{id}` 三个已冻结的 operation **今天无论如何都实现不了**。
--
-- 量纲：本文件的链上金额一律 `bigint`，单位 1e-6 USDT（`amount_usdt6`）。
-- 判定（paid / underpaid / 写销）**一律在这个整数域做**，不做任何跨类型比较 —— 见 ADR 0012 §17.3：
-- `orders.pay_amount_raw` 是 `numeric(38,18)`，其类型本身容得下链上不可能出现、且互不相等的值。

-- 与 openapi 的 `PaymentState` 逐字一致（`enum: [waiting, confirming, underpaid, paid, expired]`）。
-- 🔴 顺序与拼写都不许调整：生成物漂移由 CI 的 `git diff --exit-code` 卡住，
--    而「DB 枚举比契约多/少一个值」这类错误在 Go 侧表现为运行时 scan 失败，不是编译错误。
CREATE TYPE payment_state AS ENUM ('waiting','confirming','underpaid','paid','expired');


-- ---------- 收款地址池（一单一址，永不复用）----------
CREATE TABLE pay_addresses (
  id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  chain                text    NOT NULL,                 -- 'tron'
  address              text    NOT NULL,
  derivation_index     integer NOT NULL,                 -- m/44'/195'/0'/0/i 的 i

  -- NULL = 库存。一旦非 NULL 就**永不回退**（ADR 0012 §5.2「永不复用」）。
  -- 🔴 `UNIQUE` 是「一址一单」这半条不变量的数据库强制；另半条（一单一址）由 §17.2 的
  --    `orders_pay_addr_uk` 在 0015 里补上。两条合起来才让「归属」成为一次确定的查表，
  --    而不是一次带金额尾数的模糊匹配 —— 后者正是本 ADR 推翻掉的机制。
  -- `ON DELETE RESTRICT`：订单是收款的凭证，地址已收过钱还能让订单被删掉，等于凭空丢失归属证据。
  assigned_order_id    bigint  UNIQUE REFERENCES orders(id) ON DELETE RESTRICT,

  enabled              boolean NOT NULL DEFAULT true,

  -- Tether `isBlackListed(<我方地址>)` 的缓存。缓存而不是每次现查的理由：
  -- 这是 AML Layer 0 的每日巡检结果（ADR 0012 §12.2），每日一次、免费额度内；
  -- 把它做成收款路径上的同步外部调用，会让 TronGrid 抖动直接变成收不到钱。
  is_blacklisted       boolean NOT NULL DEFAULT false,
  blacklist_checked_at timestamptz,

  last_scanned_at      timestamptz,

  -- TronGrid 增量游标（毫秒时间戳）。存毫秒整数而不是 timestamptz，是因为它是**外部 API 的分页游标**，
  -- 要原样回传给 TronGrid；转成 timestamptz 再转回去会在毫秒边界上丢事件（ADR 0012 §10.1 自适应清单）。
  cursor_ts            bigint,

  created_at           timestamptz NOT NULL DEFAULT now(),

  UNIQUE (chain, address),
  -- 派生序号唯一：同一个 i 被派生两次 = 两条记录指向同一把私钥控制的地址，
  -- 而 §5.2 的「永不复用」是靠 i 单调递增来保证的。撞号在这里被拒绝，而不是在对账时才发现。
  UNIQUE (chain, derivation_index)
);
-- ⚠️ 这张表里**没有 private_key 列，而且永远不会有**（ADR 0012 §1 第 3 条）。
--    私钥离线派生、离线保管；服务器、Secret Manager、仓库三处都不持有。
--    加这一列的那一刻，这张表就从「收款清单」变成「一次入侵即全部资金失窃」的单点。


-- ---------- 入账流水（唯一的入账锁）----------
CREATE TABLE payments (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

  provider         text          NOT NULL,     -- 'chain_tron' | 'oxapay'（接口留位，第一阶段不启用）

  -- 链上：`txid || ':' || log_index`。
  -- 🔴 取值来源**只有链上事件**，与录入者无关（ADR 0012 §8.2）。
  --    推翻过的写法是 `'D6:' || audit_logs.id` —— 它根本不幂等：D6 点两次 = 两条 audit_logs
  --    = 两个 external_id = **两次入账、两次开通**；且它与扫链跨 provider 不去重，
  --    同一笔钱可以既被手工记成 ('manual','D6:123')、又被扫描记成 ('chain_tron','abc…:0')。
  --    「先手工上线、后补自动化」是 §18.3 计划内的必经状态，所以这不是理论场景。
  external_id      text          NOT NULL,

  -- 'scanner' | 'admin:<id>' | 'admin:<id>+scanner'。
  -- 只区分**录入者**，不参与幂等 —— 手工与自动因此天然互斥：谁先到谁插入成功，
  -- 后到的撞唯一索引走「已入账」分支，并在那里自动写 §16.2 的冲正分录。
  entered_by       text          NOT NULL,

  -- NULL = 未归属（打到我们地址但找不到订单）。`ON DELETE RESTRICT` 同上：钱的凭证不许被级联删除。
  order_id         bigint        REFERENCES orders(id) ON DELETE RESTRICT,
  user_id          bigint        REFERENCES users(id)  ON DELETE RESTRICT,

  chain            text,
  txid             text,
  log_index        integer,
  from_address     text,
  to_address       text,

  -- 链上实收，1e-6 USDT。**判定一律在这个整数域做**（ADR 0012 §17.3）。
  amount_usdt6     bigint,
  -- 按订单锁定汇率折算的人民币分，**仅记录、不参与再计算**（沿用 0006 的量纲铁律）。
  amount_cny_cents bigint,

  state            payment_state NOT NULL,
  confirmations    integer       NOT NULL DEFAULT 0,

  aml_checked_at   timestamptz,
  -- 用 text + CHECK 而不是 ENUM：data-model §2.2 的枚举策略只把「删掉一个值 = 业务语义变了」的集合
  -- 交给 ENUM；AML 判定档位会随风控经验增删，用 ENUM 会让每次加一档都变成一次 ALTER TYPE。
  -- ⚠️ 'unbound_source' 是**记录但仍然入账**的档（§12.2）—— 来源不明不等于赃款，
  --    在这个规模上因来源不明而不给用户开通，是把误伤成本转嫁给守法用户。
  aml_verdict      text CHECK (aml_verdict IN ('clean','blacklisted','unbound_source','quarantined')),

  ledger_entry_id  bigint        REFERENCES ledger_entries(id),

  -- 链上原始 event / 网关原始 payload。NOT NULL 是刻意的：取证材料缺一条就等于这条流水不可复核，
  -- 而入账争议（用户说打了、我们说没收到）只能靠原文解决。
  raw              jsonb         NOT NULL,

  received_at      timestamptz   NOT NULL DEFAULT now(),

  -- 🔴 入账幂等的唯一真相。不是应用层 `SELECT … IF NOT EXISTS` ——
  --    后者在两个 Cloud Run 实例并发处理同一次重投时会**双双通过**，
  --    结果是同一笔钱入账两次、开通两次。--max-instances=8 之下这不是小概率。
  UNIQUE (provider, external_id)
);

-- 一张订单的完整收款历史只能从这里读，不能从 orders 读（§8.3：`orders.gateway_ref` 已降级为
-- 「首笔到账 txid，仅供人工检索，不承担幂等」）。
CREATE INDEX payments_order_idx     ON payments (order_id);

-- 未归属队列：打到我们地址却匹配不到订单的钱。做成部分索引而不是全表索引，
-- 是因为这条查询的正常结果集应当是**空的** —— 它的成本必须接近零，才配得上「每天看一眼」。
CREATE INDEX payments_unmatched_idx ON payments (received_at DESC) WHERE order_id IS NULL;

-- §6.3 的累计口径 `SUM(amount_usdt6 WHERE to_address = …)` 与 §17.6(d) 断言 1 的外部锚点核对，
-- 都按地址聚合。地址独占之下这条索引同时服务这两处。
CREATE INDEX payments_addr_idx      ON payments (to_address, received_at DESC);

-- 刻意不建的东西，逐条记下理由：
--  · 没有 updated_at / 触发器：本表接近 append-only，唯一的原地更新是 §8.4 分支 ① 把
--    entered_by 追加成 'admin:<id>+scanner'，那是一次带审计分录的写，不需要时间戳兜底。
--  · `AdminPayment.expected_usdt6` / `shortfall_usdt6` **不落列**，由 join `orders` 算出
--    （未归属的 payment 没有 expected，openapi 里这两个字段本来就不在 required 列表）。
--    落成冗余列的代价是它会与 orders 漂移，而漂移的那天没有任何报错。
