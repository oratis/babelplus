-- 0016 · 计费三条规则的 schema 落地：退款 / 升级折抵 / 流量包（ADR 0013）
--
-- 事实源：ADR 0013 §6.1（本文件逐字落地那一节）、§2.1（订单的服务区间）、§5.2（配额拆列）、
--         §3.2 与 §3.5（退款基数与副作用）、§4.6（orders.type 的服务端推导）。
-- ⚠️ 与 ADR 原文的一处不一致，按文件名为准：§6.1 代码块内部的头注释写着「0014 · 计费与退款规则」，
--    那是同批 ADR 之间抢号时留下的旧文本；§6 标题与 §6.1 小节标题写的都是 `0016_billing_rules`，
--    ADR 0015 §5.7 的编号分配段也把 0014/0015 判给了 ADR 0012。本文件取 **0016**。
--    §6.2 里出现的「0014.down」同理，指的就是本文件的 down。
--
-- ============================================================
-- 🔴 为什么这支迁移的窗口**只有现在**（ADR 0013 §6.3，必须读完再动手）
-- ============================================================
--
-- 下面把 `users.transfer_enable` 从普通列换成 `GENERATED ALWAYS AS (…) STORED`，
-- 而 `ADD COLUMN … GENERATED … STORED` 与它前面的 `DROP COLUMN` **都会重写全表**，
-- 期间持有 ACCESS EXCLUSIVE 锁 —— 表上任何读写都被挡住。
--
-- **`users` 现在是空表**（0 个产品用户；`POST /orders` 整条链路尚未实现，
-- `api/internal/handler/operations.txt` 里 128 个 operation 大多还是 501 stub）。
-- 空表上的全表重写成本是零：没有锁等待、没有停机、没有回填风险。
--
-- **等到有付费用户再拆列，代价是另一个量级**：那时这条 `DROP COLUMN` + 重写会落在
-- 活数据与在线节点拉取（每 60 秒一次 `ListAvailableUsersByServer`）之上，
-- 而 `users` 正是节点侧可用性判定要读的那张表。届时要么停服务，要么写一套双写迁移。
-- **所以这不是「顺手做了」，是「现在不做就得付 100 倍的价钱」。**
--
-- 第二条同源风险，一并登记：`DROP COLUMN` + 同名 `ADD COLUMN` 会改变列序。
-- 靠列序的代码会静默错位；本仓库的 `SELECT *` 类 sqlc query（`ApplyUserEntitlement … RETURNING *`、
-- `BanUser … RETURNING *`）**靠列名映射不靠列序**，且 `sqlc generate` 会重排结构体字段、
-- 由 CI 的 `git diff --exit-code` 捕获生成物漂移。
--
-- ⚠️ 第三条，最容易被漏掉的：**生成列不可被 UPDATE 赋值，但 sqlc 与 go build 都拦不住写它**
-- （ADR 0013 §6.3 实测：`sqlc/sqlc:1.31.1` generate exit 0、`go build` 通过、
-- 生成物里原封不动留着非法 UPDATE）。第一次暴露点是**生产环境里第一笔付款成功之后的
-- `ApplyUserEntitlement`**：用户付了 USDT，订单进 paid，开通权利时 500。
-- 所以 ADR 0013 §6.5 的 8 条 sqlc query 必须与本迁移**同批**改，
-- 并靠 §6.4 新增的 CI 作业（对每条写语句跑 EXPLAIN）兜底。**本文件管不住这一条，它只负责把话说清楚。**


-- ============================================================
-- ③ 流量包配额拆列（ADR 0013 §5.1 / §5.2）
-- ============================================================
--
-- 这是仓库里三处独立登记的同一个缺口：`api/db/queries/users.sql:86–88` 的「⚠️ 已知缺口」注释
-- （逐字写着「修复需要拆成 transfer_enable_plan + transfer_enable_pack 两列，
-- 而那依赖一条**尚未裁决的产品规则**」）、data-model §16、user-journey §10.1。
-- **ADR 0013 ③ 就是那次裁决。**
ALTER TABLE users
  ADD COLUMN transfer_enable_plan bigint NOT NULL DEFAULT 0 CHECK (transfer_enable_plan >= 0),
  ADD COLUMN transfer_enable_pack bigint NOT NULL DEFAULT 0 CHECK (transfer_enable_pack >= 0),
  ADD COLUMN pack_expire_at       timestamptz;

COMMENT ON COLUMN users.transfer_enable_plan IS
  '套餐配额（字节）。**会过期**：每个周期重置时被 plans.transfer_enable 覆写清零。
   退款终止订阅时只清这一列（ADR 0013 §3.5）。';
COMMENT ON COLUMN users.transfer_enable_pack IS
  '加油包配额（字节）。**会结转**：跨周期保留，由 pack_expire_at 封顶。
   消耗顺序是先套餐后加油包 —— 先消耗会过期的那份，对用户永远不亏（ADR 0013 §5.3）。';
COMMENT ON COLUMN users.pack_expire_at IS
  '加油包配额的兜底过期时刻（12 个月）。结转是无限期负债，这一列是它唯一的封口（ADR 0013 §5.1 第 3 条）。';

-- 回填：现有值全部视为套餐配额。（空表上是 0 行，但写出来才让「重灌一次库」也得到同一个结果。）
UPDATE users SET transfer_enable_plan = transfer_enable;

ALTER TABLE users DROP COLUMN transfer_enable;
ALTER TABLE users ADD COLUMN transfer_enable bigint
  GENERATED ALWAYS AS (transfer_enable_plan + transfer_enable_pack) STORED NOT NULL;
--                                                                  ^^^^^^^^
-- 🔴 `NOT NULL` 这两个词不是可选的（ADR 0013 §5.2 细节 2 · §7.2 实测）：漏了它，
--    读侧 Go 类型会从 int64 全线退化成 *int64，而「读侧完全透明、读侧 query 不用改」
--    这条结论**只有在加了 NOT NULL 时才成立**。
--
-- **为什么用生成列而不是让应用代码维护总额**：漏更新总额的后果是**静默的**
-- （用户配额算错而没有任何报错）。生成列让「两个分量与总额不一致」这个状态在 schema 层不可表达。
-- 决定性的一条理由是 D1：page-inventory §4.4 把「改用户流量配额 / 到期时间」标注为
-- 「**直接等于送钱**；也是内部欺诈面」—— 这是一条**管理员会绕过应用层直接改配额**的既定路径。
-- 触发器只在 SQL 写入时生效（这点两者相同），但生成列还能保证**从 psql 手改也算不出不一致的总额**，
-- 因为「总额」根本不是一个可被赋值的东西。
-- 另一条：`0012_user_rev_triggers.up.sql` 自己带着撤回条件（「当所有写路径都收敛到 3–5 个
-- 明确的 service 方法时，应当把触发器改回显式调用并删掉它」）—— 往一个计划中要拆掉的机制上
-- 再挂一条业务不变量是错的；生成列不需要撤回条件，因为它是**约束**不是**行为**。
COMMENT ON COLUMN users.transfer_enable IS
  '生成列（STORED）：= _plan + _pack。不可赋值；对外与 subscription-userinfo 的 total= 保持单一口径。';

-- 加油包到期扫描（与 users_expiry_due_idx 同形：Cloud Scheduler 每分钟跑，平时命中 0 行）。
CREATE INDEX users_pack_expiry_due_idx ON users (pack_expire_at)
  WHERE pack_expire_at IS NOT NULL AND deleted_at IS NULL;


-- ============================================================
-- ①② 订单的服务区间与窗口链（ADR 0013 §2.1）
-- ============================================================
--
-- 三条规则纠缠在一起的根源，是它们都要回答「用户手上这份订阅现在处在什么位置」。
-- 草稿用「最后一笔已完成订单的 paid_at」当锚点，而**那个锚点会被升级动作重置**（§7.1 C2）——
-- 升级一次，退款基数就凭空缩水一次。本节把地基换成**每笔订单自己的服务区间**。
ALTER TABLE orders
  ADD COLUMN covers_from            timestamptz,
  ADD COLUMN covers_to              timestamptz,
  ADD COLUMN prev_order_id          bigint REFERENCES orders(id) ON DELETE RESTRICT,
  ADD COLUMN price_monthly_at_order bigint CHECK (price_monthly_at_order >= 0),
  -- 链上付款方地址，归集时按 txid 回填（ADR 0013 §9 的失效条件靠它执行）。
  ADD COLUMN pay_from_address       text;

COMMENT ON COLUMN orders.covers_from IS
  '本单买到的服务生效时刻。new/upgrade = paid_at；renew = greatest(paid_at, 旧 covers_to)。ADR 0013 §2.1';
COMMENT ON COLUMN orders.covers_to IS
  '本单服务结束时刻；NULL = 不限时（onetime）。upgrade 继承被折抵单的值。ADR 0013 §2.1';
COMMENT ON COLUMN orders.prev_order_id IS
  '本单接续/替换的上一单。订阅窗口 = 从 source 沿这一列回溯到 NULL 的全部订单 ——
   让「窗口」成为一条可以递归走完的链表，而不是靠时间区间猜连续性。ADR 0013 §2.1 第 3 条';
COMMENT ON COLUMN orders.price_monthly_at_order IS
  '下单时 plans.price_monthly 的快照（分）。退款扣减必须用它，不能读活列 —— 否则涨价后退款额变小，
   用户会认为我们改价来少退钱（user-journey §10.2 硬要求 1）。ADR 0013 §3.2';
COMMENT ON COLUMN orders.pay_from_address IS
  '链上付款方地址，归集时按 txid 回填。ADR 0013 §9 的失效条件靠它执行。';

-- 这两列是同一批修复里唯一改了**既有列语义**的地方，所以 COMMENT 在这里是强制的（ADR 0013 §3.5）：
-- 不写这条，第一个做对账的人会把 sum(refunds.amount) 当成现金流出，
-- 而 orders_refund_le_paid 这条 CHECK 也会与「退到余额可能超过 amount_paid」冲突（V_window 含 amount_balance）。
COMMENT ON COLUMN orders.amount_refunded IS
  '只记真的退出去的现金（destination=original）。退到余额时恒为 0；退款总额的唯一真相源是 refunds.amount。';
COMMENT ON COLUMN refunds.amount IS
  '本次退款**进到余额的总额**（分）。与 orders.amount_refunded 只有 destination=''original'' 时相等。ADR 0013 §3.5';

-- 窗口链回溯（§3.2 的 WITH RECURSIVE）走这条索引。部分索引：绝大多数订单是窗口的根，prev 为 NULL。
CREATE INDEX orders_prev_idx ON orders (prev_order_id) WHERE prev_order_id IS NOT NULL;


-- ============================================================
-- ② 区分周期套餐与加油包（orders.type 的服务端推导要读它）
-- ============================================================
--
-- 🔴 **刻意不给 DEFAULT**（ADR 0013 §4.6）：默认值会让「新建套餐时忘了填 kind」变成一次静默的
-- 错误分类，而分类错的后果是 `POST /orders` 把加油包推导成 upgrade，凭空触发一次折抵。
-- `plans` 现在是空表，所以加 NOT NULL 无默认值列不会失败；`CreatePlan` 必须同批改（§6.5）。
ALTER TABLE plans
  ADD COLUMN kind text NOT NULL CHECK (kind IN ('cycle','pack'));
COMMENT ON COLUMN plans.kind IS
  '''cycle'' = 周期套餐（买时间）；''pack'' = 加油包（买流量，不动时间）。
   orders.type 的服务端推导读这一列；刻意无 DEFAULT，见 ADR 0013 §4.6。';

-- 月付标价是退款扣减的乘数，不能为 NULL（0002 的注释：NULL = 该周期不售）。
-- 一个 kind='cycle' 但 price_monthly IS NULL 的套餐，会让 §3.2 的退款公式除到一个不存在的数上。
ALTER TABLE plans
  ADD CONSTRAINT plans_cycle_needs_monthly CHECK (kind <> 'cycle' OR price_monthly IS NOT NULL);


-- ============================================================
-- ① 退款规则可机检、可审计，且「一生一次」由数据库强制
-- ============================================================
--
-- `refunds.user_id` 加 NOT NULL 在空表上没有问题（refunds 当前 0 行）。
-- 草稿刻意不给 refunds 冗余 user_id（「它只能通过订单归属到人」），
-- 代价是 Class A 冷静期退款的「一生一次」**只能靠应用代码不写错**。
-- 加了这一列 + 下面那条部分唯一索引之后，这条规则变成**数据库拒绝**，
-- 与本项目「让非法状态在 schema 层不可表达」的一贯取向一致。
ALTER TABLE refunds
  ADD COLUMN rule    text NOT NULL DEFAULT 'manual'
      CHECK (rule IN ('cooling_off','prorated','service_terminated','manual')),
  ADD COLUMN user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT;

COMMENT ON COLUMN refunds.rule IS
  '本次退款适用的规则档：cooling_off（冷静期全额，一生一次）/ prorated（按剩余价值）/
   service_terminated（我方终止服务）/ manual（人工裁量）。ADR 0013 §3.2';
COMMENT ON COLUMN refunds.user_id IS
  '退款归属的用户。冗余自 orders.user_id，存在的唯一理由是让下面那条部分唯一索引成立 ——
   「冷静期退款一生一次」因此是数据库拒绝，而不是应用代码的自觉。ADR 0013 §6.1';

CREATE UNIQUE INDEX refunds_cooling_off_once ON refunds (user_id) WHERE rule = 'cooling_off';
CREATE INDEX refunds_rule_idx ON refunds (rule, created_at DESC);


-- ============================================================
-- ① 佣金：自邀在数据库层被拒绝
-- ============================================================
--
-- 核对 `0007_ledger.up.sql:64–81` 确认此前没有这条约束。
-- 它挡不住「同一个人的两个账号」（那要靠 §8 的其他手段），
-- 但把最蠢的形态（自己邀请自己）变成数据库拒绝，成本一行。
ALTER TABLE commissions
  ADD CONSTRAINT commissions_no_self_invite CHECK (inviter_id <> invitee_id);


-- ============================================================
-- ③ 重置审计能分开看两个分量
-- ============================================================
--
-- 不拆这一列，重置日志只剩一个总额，而「加油包被吃掉了还是结转了」正好落在总额里看不见 ——
-- 那恰恰是本 ADR ③ 要防的那个静默失败（§5.3：调用顺序错了会让加油包只增不减，且完全静默）。
ALTER TABLE traffic_reset_log
  ADD COLUMN new_transfer_enable_pack bigint NOT NULL DEFAULT 0;
COMMENT ON COLUMN traffic_reset_log.new_transfer_enable_pack IS
  '重置后的加油包分量（结转值）。与 new_transfer_enable（总额）配合，才能事后判断结转是否算对。ADR 0013 §6.1';
COMMENT ON COLUMN traffic_reset_log.new_transfer_enable IS '重置后的**总额**（_plan + _pack），不是 plan 分量';


-- ============================================================
-- 触发器：监视列表换成两个分量（ADR 0013 §5.2 细节 1）
-- ============================================================
--
-- `users_bump_user_rev()` 是 AFTER 触发器，`NEW.transfer_enable` 在 AFTER 阶段有值，
-- 所以不改它其实也能跑 —— 但那依赖一条**容易被后人改坏的语义**（生成列在 BEFORE 阶段是 NULL）。
-- 显式把监视列表改成两个分量，让这条依赖不再存在。
-- 漏 bump 的后果是静默的：节点永远拿旧用户表，没有报错、没有告警，只有「加了流量但那个人用不了」。
--
-- ⚠️ 改了它就**必须同步改 0016.down**，见 §6.2 与本目录 0016_billing_rules.down.sql 的第 1 步。
CREATE OR REPLACE FUNCTION users_bump_user_rev() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    PERFORM bump_user_rev(NEW.group_id);
  ELSIF TG_OP = 'DELETE' THEN
    PERFORM bump_user_rev(OLD.group_id);
  ELSIF OLD.group_id IS DISTINCT FROM NEW.group_id THEN
    PERFORM bump_user_rev(OLD.group_id);
    PERFORM bump_user_rev(NEW.group_id);
  ELSIF (OLD.uuid, OLD.banned, OLD.expired_at,
         OLD.transfer_enable_plan, OLD.transfer_enable_pack,
         OLD.speed_limit_mbps, OLD.device_limit, OLD.deleted_at, OLD.expiry_applied_at)
     IS DISTINCT FROM
        (NEW.uuid, NEW.banned, NEW.expired_at,
         NEW.transfer_enable_plan, NEW.transfer_enable_pack,
         NEW.speed_limit_mbps, NEW.device_limit, NEW.deleted_at, NEW.expiry_applied_at) THEN
    PERFORM bump_user_rev(NEW.group_id);
  END IF;
  RETURN NULL;
END $$ LANGUAGE plpgsql;
