# ADR 0013 · 计费三条规则统一到一条恒等式：用户的净支出恒等于他按月付标价实际消费的量

> 日期：2026-08-23 · 性质：**架构裁决** · 状态：**提案，未批准**（2026-08-23）
> 事实基线：仓库 HEAD **`618bf1cc89b`**（2026-08-23，PR #15 / #16 / #17 已合入；
> `api/db/migrations/0001…0013`、
> `api/db/queries/{users,orders,stats,servers,subscriptions}.sql`、`api/sqlc.yaml`、
> `.github/workflows/ci.yml`、`openapi/openapi.yaml` 全部逐条复核，行号见正文；
> ⚠️ **迁移号 `0013` 已被 2026-08-23 合入的 `0013_rate_limit` 占用，本 ADR 的迁移一律是 `0014`**）；
> 出口成本读自 `docs/evidence/egress-billing-20260820/`（BigQuery `loopback-500616.billing_export`，gross 口径）；
> 链上手续费与 Tether 执法统计读自 `docs/03-product/pricing-and-plans.md` §4.2/§4.3（2026-08-16 一手链上实测）；
> **PostgreSQL 行为为 2026-08-23 本文作者在 `postgres:17` 容器内一手实测**（§7.2 四项，逐条给了报错原文）
> 关联：[0005](0005-database-selection.md)、[0006](0006-api-stack.md)、
> [0012](0012-payment-gateway.md)（收款形态与订单状态机，本 ADR 与其 §7 对齐）、
> `docs/01-research/payments.md` §4.3/§4.6/§4.7/§4.12、
> `docs/02-architecture/data-model.md` §6/§6.2/§7.1/§8.4/§13/§16、
> `docs/03-product/pricing-and-plans.md` §3.2/§3.4/§4.2/§4.3/§5/§6、
> `docs/03-product/user-journey.md` §10.1/§10.2/§11.3、`docs/03-product/page-inventory.md` §4.4/§5.4、
> roadmap B24 / B25 / B26 / B37

---

## 1 · 裁决

### 1.1 一句话结论

**退款、升级折抵、加油包结转三件事，用同一条不变量定义：无论用户走哪条路径（直接持有、升级后持有、中途退款、退款后再买），他最终的净支出恒等于**

```
净支出 = Σ 各档位的（实际持有天数 ÷ 30 × 该档位月付标价）
       + Σ 各档位的（本周期实际消耗的套餐流量 ÷ 套餐配额 × 该档位月付标价）
```

**长周期折扣只有靠「持有到折扣的归零点之后」才能兑现；任何提前退出的路径都不会比按月付标价买便宜，也不会比它贵。**
这条恒等式是本 ADR 的全部内容 —— 三条具体规则都是它的推论，抗套利不再靠一张「我们想到的攻击」清单，
而是靠「所有路径同价」这条结构性质。

### 1.2 三条具体裁决

| # | 问题 | 裁决 | 一句话理由 |
|---|---|---|---|
| **①** | 退款政策 | **退款额 = 本订阅窗口实付总额 − 已服务天数按月付标价的折算 − 本周期已用套餐流量按月付标价的折算，下限为 0**；**一律退到不可提现的钱包余额**；首单 7 天内且已用 ≤ min(套餐配额 10%, 10 GiB) 可豁免上述扣减而全退（一账号一生一次）；流量包/重置包/充值不退；**唯一现金例外是我方终止服务** | 两项扣减合起来使**退款用户与不退款用户的单位消费价格完全相同**，于是退款对我们**没有任何增量敞口**（§3.2 给了证明）—— 「可以退得宽」这个结论不再依赖「退到余额不花现金」这条错误推理 |
| **②** | 升级折抵 | **只按剩余天数折抵金额，配额按本周期剩余天数折算发放**；折抵基数 `V_source = amount_paid + amount_balance + surplus_amount`；折抵不产生找零；**禁止中途降级**；升级单不收优惠码 | `payments` §4.6 的 `min(剩余天数比, 剩余流量比)` 两个比例量纲不同（跨订单 vs 周期内），在我们 DDL 层面的默认路径（长周期订单 + 月度重置）上会给出 **0 折抵**；且 §4.6 原文自相矛盾，从来不是一条成形的建议 |
| **③** | 流量包与周期重置 | **保留（跨周期结转）**。`users.transfer_enable` 拆成 `transfer_enable_plan` + `transfer_enable_pack`，`transfer_enable` 改为二者之和的 **`STORED NOT NULL` 生成列**；消耗顺序**先套餐后加油包**；加油包 12 个月有效 | 清零会让政策与自己的销售动线（80%/95% 告警时卖包）打架；生成列让「两个分量与总额不一致」在 schema 层不可表达，这在 **D1「管理员直接改配额」这条绕过应用层的人工写路径存在**的前提下是唯一正确的选择 |

### 1.3 与草稿相比，本裁决改了什么

本 ADR 由一份设计草稿经正反双方核查后裁定。**被推翻的地基条款有三条**，全部来自反方：

| 草稿原样 | 本裁决 | 出处 |
|---|---|---|
| `refund_B = greatest(0, V − ceil(elapsed_days/30) × M)`，锚点 `source.paid_at` | **整条换掉**：改为窗口内分段的**纯按比例**扣减 + 一项流量扣减，锚点改为订单自己的服务区间 `covers_from` | §7.1 C1 / C2 |
| 「退到余额现金支出为零，所以退多少可以给得宽」 | **删除这条推理**。宽松度改由 §3.2 的零增量敞口证明支撑 | §7.1 C9 |
| 「4 处写路径会在 `sqlc generate` / `go build` 阶段炸出来，编译器替我们做了全量检查」 | **实测为假**（`sqlc generate` exit 0、`go build` 通过）。改为在 CI 里新增一个真正执行写语句的作业 | §7.1 C5 |

**迁移 `0016` 的规模因此扩大**（编号分配：`0013` 已被 `0013_rate_limit` 占用，`0014`/`0015` 由同批 [ADR 0012](0012-payment-gateway.md) 的两次迁移预定，本 ADR 取 `0016`；若实现顺序不同，以先合入者占号、后者顺延——golang-migrate 按文件名排序，迁移间无内容耦合）：
从草稿的「3 表 / 4 个 query / 1 个触发器，不加表」变为
**6 张表（`users` · `orders` · `plans` · `refunds` · `commissions` · `traffic_reset_log`）
/ 8 个 sqlc query / 1 个触发器 / 1 个 CI 作业，仍不加表**（清单见 §6）。

---

## 2 · 共用地基：订单的服务区间与订阅窗口

三条规则纠缠在一起的根源，是它们都要回答「用户手上这份订阅现在处在什么位置」。
草稿用「最后一笔已完成订单的 `paid_at`」当锚点，反方证明了这个锚点会被**升级动作重置**（§7.1 C2）。
本节把地基换成**每笔订单自己的服务区间**。

### 2.1 三列新字段（`orders`）

```sql
ALTER TABLE orders
  ADD COLUMN covers_from   timestamptz,          -- 本单买到的服务从何时开始生效
  ADD COLUMN covers_to     timestamptz,          -- 到何时结束；NULL = 不限时（onetime）
  ADD COLUMN prev_order_id bigint REFERENCES orders(id) ON DELETE RESTRICT;  -- 本单接续/替换的上一单
```

下单成功（进入 `completed`）时按类型写死，**没有第四种写法**：

| `orders.type` | `covers_from` | `covers_to` | `prev_order_id` |
|---|---|---|---|
| `new` | `paid_at` | `covers_from + period` | `NULL`（窗口的根） |
| `renew` | `greatest(paid_at, 旧 covers_to)` | `covers_from + period` | 旧的那一单 |
| `upgrade` | `paid_at` | **旧 `covers_to`（不变）** | 被折抵掉的那一单（= `surplus_order_ids[1]`） |
| `traffic_pack` / `reset_pack` / `wallet_topup` | `NULL` | `NULL` | `NULL` |

三条设计选择各有一条不可省略的理由：

1. **`renew` 的 `covers_from` 取 `greatest(paid_at, 旧 covers_to)`，而不是 `paid_at`。**
   提前 10 天续年付的用户，他买的那一年是从旧到期日开始的，不是从付款日开始的。
   草稿用 `paid_at` 会把每日单价摊薄 `10/375 = 2.7%`，并把这条误差登记为「代价 4，本次不修」。
   这里一并修掉，**成本是零** —— 因为这两列本来就是修 §7.1 C2 所必需的。
2. **`upgrade` 的 `covers_from` 取 `paid_at`，`covers_to` 继承。**
   升级不买时间，只换档位。这个定义让 §4 的折抵比例 `D_left / D_total` 与草稿的
   `source.paid_at` 锚点**逐字等价**，所以草稿 §4.2 那张「链式升级价值守恒」的验算表**继续成立**
   （§4.2 重新验算了一遍）。
   ⚠️ 反方的替代方案（§6.1，`upgrade` 继承 `covers_from`）在这一点上是**错的**：
   继承之后 `D_total` 会退回到整个窗口的长度，D22 那一步的折抵会从正确的 1600 变成 800，
   凭空吞掉用户 800 分 —— 恰好是草稿自己警告过的那个错误。**故此条只部分采纳。**
3. **`prev_order_id` 让「订阅窗口」成为一条可以递归走完的链表**，
   而不是靠时间区间猜contiguity。窗口 = 从 `source` 沿 `prev_order_id` 一路回溯到 `prev_order_id IS NULL` 的全部订单。

### 2.2 两个基数，不要混用

| 量 | 用途 | 定义 |
|---|---|---|
| `V_source` | **升级折抵**的基数 | `source.amount_paid + source.amount_balance + source.surplus_amount` |
| `V_window` | **退款**的基数 | `Σ (o.amount_paid + o.amount_balance)`，`o` 遍历整个窗口链 |

**这两个量不同，且都必须存在。**
`V_source` 计入 `surplus_amount`，因为链式升级要价值守恒（§4.2）；
`V_window` **不计** `surplus_amount`，因为 surplus 是窗口内部的价值回收，
把它算进求和会把同一笔钱数两遍。核对：§3.3 的三段升级算例里
`V_window = 3000 + 1500 + 4800 = 9300` 分，恰好等于用户真金白银掏出来的 ¥93.00。

两者都**不含** `amount_discount`，所以优惠码的面值不会被洗成可退款或可折抵的信用
（`first_order_only` 新人券的原意得以保持）。

```sql
-- source：当前订阅窗口的最后一笔已完成周期订单
source = SELECT * FROM orders
         WHERE user_id = $1 AND status = 'completed'
           AND type IN ('new','renew','upgrade')
         ORDER BY covers_from DESC, id DESC LIMIT 1;
         -- user_id 的过滤走 orders_user_idx (user_id, created_at DESC)；
         -- covers_from 的排序它盖不住，仍是一次 sort（几十人量级下无意义，登记以免被后人当成索引命中）

D_total = greatest(1, ceil(extract(epoch FROM (source.covers_to - source.covers_from)) / 86400))
D_left  = least(D_total, greatest(0,
              ceil(extract(epoch FROM (source.covers_to - now())) / 86400)))
```

> **若不存在这样的 `source`**（管理员用后台 D1 直接改配额与到期时间开通的账号 ——
> page-inventory §4.4 把 D1 标注为「**直接等于送钱**，也是内部欺诈面」）：
> `V_source = V_window = 0`，**不可折抵、不可退款**。送出去的东西不产生退款权。

> **若 `source.covers_to IS NULL`**（不限时套餐，`order_period = 'onetime'` / `plans.price_onetime`）：
> `D_total` / `D_left` 无定义。**裁决：P1 阶段不售不限时套餐**（`plans.sellable = false`），
> 升级到/从不限时套餐一律 `422`，退款只走 §3.3 的流量项。
> 这一条草稿与正反双方都没有处理，而 `users.expired_at` 在 `0003_accounts.up.sql:21`
> 明写「NULL = 不限时套餐」、`order_period` 里 `onetime` 存在、`plans.price_onetime` 也存在
> —— **schema 层面它已经是可售的**，不写死就是一个空指针等着人踩。

### 2.3 舍入口径

一律 `floor`，且**不引入 bps 中间量**：`floor(V * D_left / D_total)` 的误差 ≤ **1 分**；
先算 `r_bps = floor(D_left*10000/D_total)` 再乘会把误差放大到 `V/10000`（¥900 的订单 = 9 分）。
bigint 溢出核对：`V ≤ 10^7 分 × D_left ≤ 400` = `4×10^9`，离 bigint 上限 `9.2×10^18` 有 9 个数量级余量。

---

## 3 · ① 退款

### 3.1 退到哪：三条理由把「原路退回 USDT」按死

| # | 理由 | 证据等级 |
|---|---|---|
| 1 | **主动转出触碰的法律面比被动收款严重一档。** 银发〔2021〕237 号点名泰达币；法发〔2021〕22 号**第十一条**把「通过虚拟货币**转换财物**、套现」按掩饰隐瞒犯罪所得罪处置。收款是被动接受，转出是主动发起 —— 后者才落在「转换财物」这个动词上 | **高**（条文原文已取回，gov.cn 链接在 pricing §4.3）。仍**待核实**的是「客户用已持有的 USDT 付款是否落入列举」，但那是**收款端**的问题，不是退款端 |
| 2 | **成本**：一笔 TRC20 USDT 转出实测 6.43 TRX ≈ **$2.13**（收款方已持 U）/ 13.03 TRX ≈ **$4.31**（未持 U），租能量可压到 4.42 TRX ≈ **$1.47**。对照竞品 ¥30（≈$4.2）的月付档，这是 **35%–103%** 的侵蚀 | **高**（2026-08-16 一手链上实测） |
| 3 | **反向污染**：退给用户的 U 来自我方资金池，而 TRON USDT 合约 2020-06-26 → 2026-08-15 累计 `AddedBlackList` **8,043** 次、`DestroyedBlackFunds` **1,164 次 / 581,237,667 USDT**，近 30 天新增拉黑 **264 个地址**（≈8.8 个/天，且常为批量）。池子里混入一笔受污染的 U，退款就把这个 taint 转移给一个没有 `isBlackListed` 前置筛查能力的熟人 | **高**（2026-08-16 一手链上统计，pricing §4.3） |

> **草稿把「付款方地址多为交易所热钱包，往回转会把钱弄丢」列为第一理由，本裁决把它降为脚注。**
> 它建立在「中国用户从交易所直接提币到收款地址」这个推论上，而我们至今 **0 笔真实入账**，0 个地址样本。
> 上面三条都不需要样本。
> 正方还提出第四条「我们的收款架构里根本不存在『原路』」（共享地址 + 金额尾数匹配 + 到账即归集）
> —— **本裁决不采用它**，因为 schema 里这一点是自相矛盾的：
> `0006_orders.up.sql:58` 的 `pay_address` 列注释写的是「**本单专属收款地址**」，
> 而同文件 `:86` 的 `orders_pay_addr_amount_uk` 注释写的是「EPUSDT 的**金额尾数递增法**」。
> 一单一地址与共享地址两种形态在同一份 DDL 里并存，**收款形态其实还没定死**（登记在 §11）。
> 拿一条自己都没定的架构事实当承重墙，是本 ADR 不该犯的错误。
>
> ⚠️ **2026-08-23 更新（与 [ADR 0012](0012-payment-gateway.md) 对齐）**：同日的 0012 §1 已经裁掉这处矛盾 ——
> **一单一址、地址永不复用、归属只看地址不看金额**，并 `DROP orders_pay_addr_amount_uk`；
> 同时裁决**第一阶段一次都不归集**（0012 §1 第 4 条）。
> 于是正方第四条的两个前提（共享地址、到账即归集）**都被推翻**：驳回结论不变，
> 理由从「架构没定死」换成「架构定的恰好是相反的那一种」。
> **对本 ADR 的实质影响只有一条**：0012 之下「原路」在技术上是存在的（钱还留在那个一单一址上，私钥离线所以取用是人工动作），
> 所以 §3.1 的三条理由必须自己站得住 —— 它们本来就一条都不依赖收款形态。
> 0012 仍是**提案，未批准**；若它被否决，本段回退到上面「形态未定」的判断。

**裁决：`refunds.destination` 的默认且唯一常规取值是 `'balance'`。**

**唯一例外 —— 我方终止服务**：永久关停，或**同一自然月内累计不可用 ≥ 72 小时**。此时允许 `destination = 'original'`，且：

- 用户必须提供**自有地址**并勾选「此地址不是交易所充值地址」；
- 退款数量按**链上实收比例**算，**不碰汇率**：`refund_usdt = pay_amount_received × refund_cents / amount_paid`。
  这样 `orders.fx_usdt_per_cny`「只作记录与申诉证据，不参与再计算」那条注释（`0006_orders.up.sql:61`）继续成立；
- 网络费从退款额里扣，条款写明。

> 为什么这条例外不可省略：服务没了的时候，「只退不可提现的余额」等于什么都没退。
> 内部熟人关系撑得住「不退现金」，撑不住「服务停了还不退现金」。

### 3.2 退多少：一条公式，以及它为什么让敞口恒为零

草稿的论证是「退到余额在复式账上是 `deferred_revenue → user_wallet` 两条负债对冲，资产侧不动，
所以现金支出为零，所以退多少可以给得宽」。**这条推理是错的，本裁决删掉它。**
反方指出：余额的**唯一用途**是买服务，而服务 = 出口流量 + 固定设施；一笔 ¥X 的余额退款的真实成本是
`X × (成本 ÷ 售价)`，只是推迟到他花掉它的那天。而 product-brief 把本项目定位为
「**成本分摊与可持续，不是利润最大化**」—— 成本分摊意味着 `售价 → 成本`，比值 → 1。
**我们越忠实于自己的定位，「退到余额」的真实代价就越接近「退现金」。**

正确的支撑不是会计口径，而是**定价**。公式如下：

```
-- 窗口链（沿 prev_order_id 回溯）
WITH RECURSIVE win AS (
    SELECT o.* FROM orders o WHERE o.id = $source_id
  UNION ALL
    SELECT p.* FROM orders p JOIN win w ON p.id = w.prev_order_id
                             WHERE p.status = 'completed'
), seg AS (
  SELECT w.*,
         lead(w.covers_from) OVER (ORDER BY w.covers_from, w.id) AS next_from
  FROM win w
)
V_window      = Σ (amount_paid + amount_balance)                        -- 分
consumed_time = Σ floor( price_monthly_at_order
                         * greatest(0, extract(epoch FROM
                             (least(coalesce(next_from, now()), now()) - covers_from)) / 86400)
                         / 30 )                                        -- 分，不取整到月
plan_used     = least(ut.u + ut.d, users.transfer_enable_plan)          -- 只算套餐流量，不算加油包
consumed_data = floor( source.price_monthly_at_order
                       * plan_used::numeric / greatest(1, users.transfer_enable_plan) )

-- Class B · 常规退款（对任何一笔周期订单，无窗口上限）
refund_B = greatest(0, V_window - consumed_time - consumed_data)

-- Class A · 首单善意窗口（豁免上面两项扣减，全额 = V_window）
--   ① source 是该账号**第一笔** completed 订单
--   ② now() - source.covers_from <= interval '7 days'
--   ③ ut.u + ut.d <= least(users.transfer_enable_plan / 10, 10 * 1024^3)
--   ④ users.banned = false
--   ⑤ 该账号此前没有 rule='cooling_off' 的 refunds 记录（由唯一索引强制，§6.1）

-- Class C · 一律不退
--   orders.type IN ('traffic_pack','reset_pack','wallet_topup')
--   或 users.banned = true 且封禁原因属违规使用
--   或 refund_B <= 0
```

**`consumed_time` 不再取整到月**（草稿是 `ceil(elapsed/30) × M`）。三条理由：

1. **`ceil` 制造了一个 24 小时的全额退款窗口。** `now() = paid_at` 时 `ceil(0/30) = 0`，
   `refund_B = V` —— 一个**不限首单、不限次数、无流量闸门**的全额退款口，
   而 Class A 那四道闸门一道都不适用。这是反方最强的一击，成立。
2. **`ceil` 让草稿自己的法务措辞变成假话。** 草稿 §3.5 写「年付享 75 折，因此**服务满 9 个月后**不再有可退金额」，
   而 `ceil` 版本的归零点落在**第 241 天**（`ceil(241/30) = 9`），即 8 个月零 1 天。
   按比例版本的归零点是 `9.0 M ÷ M × 30 = 270 天`，**恰好 9 个月，与条款逐字一致**。
3. **按比例是可加的，`ceil` 不是。** 只有可加的量才能在「窗口内换过档位」时正确分段求和，
   而不发生跨段的重复计费（§3.3 的三段算例验证了这一点）。

**`consumed_data` 是新增的一项，它是整条政策的承重墙。**
草稿的 Class B **没有任何流量闸门**，于是「退多少」这个维度上的宽松度没有对价。加上这一项之后：

> **定理（零增量敞口）**：对任意用户，走「购买 → 使用 → 退款」这条路径的净支出，
> 恒 ≥ 走「购买 → 使用 → 不退款」这条路径在同等消费下的应付额。
>
> **证明**：净支出 = `V_window − refund_B = consumed_time + consumed_data`（未触底时）。
> 第一项是他按月付标价持有各档位的时间价格，第二项是他按月付标价消耗的流量价格。
> 一个不退款的用户在同样的时间与流量下，付的是 `V_window ≥ consumed_time + consumed_data`
> （否则 `refund_B ≤ 0`，落入 Class C）。∎

推论是本节的实用结论：**一个满配额跑完就退款的用户，`consumed_data` 恰好等于一整个月的标价，
`refund_B` 归零 —— 他与一个满配额跑完不退款的用户，处境完全相同。**
所以退款政策带来的额外出口成本是 **0**，与套餐定价是否合理无关（定价是否覆盖成本是 pricing §7 的问题，不是本 ADR 的）。

**流量口径的两个细节**：
- 分子用 `plan_used = least(u+d, transfer_enable_plan)`，与 §5.3 的展示口径逐字一致 ——
  加油包的字节不参与退款扣减，因为加油包本来就不退（Class C）。
- 分母用 `users.transfer_enable_plan`（**本周期实际发放的**套餐配额）而不是 `plans.transfer_enable`，
  因为周期中途升级后前者是按剩余天数折算过的（§4.4）。`greatest(1, ·)` 防除零
  （新注册用户 `transfer_enable = 0`，见 `users.sql:24`）。

**四个数字的出处**：

| 数字 | 值 | 怎么来的 |
|---|---|---|
| 冷静期窗口 | **7 天** | payments §4.7 建议 7 天，沿用。USDT 零拒付，不需要卡通道那 120 天的拒付窗口 |
| Class A 流量闸门 | **min(套餐配额 10%, 10 GiB)** | 出口现金上界 = 10 GiB × $0.0979/GiB = **$0.98/次**；叠加「一账号一生一次」+ 50 人上界 → 年度 **$49**（≈ 5.1 个月的 `bp-db` 账单，`db-f1-micro` $9.53/月）。payments §4.7 写的是 5 GB，放宽到 10 GiB 的理由是熟人语境下「试了两天就说不能退」的关系成本高于 $0.49 |
| 佣金冷静期 | **15 天** | 沿用，但**作用被降级**：它不再是闸门（Class B 无窗口上限，任何有限的冷静期都挡不住），只用来减少「已 `confirmed` 之后才发生退款」的笔数。真正的闸门是 §3.5 的**按比例追回** |
| 加油包有效期 | **12 个月** | 见 §5.4 |

**长周期折扣的归零点**（把 pricing §3.2 的折扣代进 `consumed_time`，无需另写阈值表）：

| 周期 | 折扣 | 实付（以月付价 M 计） | `refund_B` 归零于 | 周期总长 | 直观含义 |
|---|---|---|---|---|---|
| 月付 | 基准 | 1.0 M | 第 **30** 天 | 30 天 | 全程按天线性退，到期时恰好归零 |
| 季付 | 9 折 | 2.7 M | 第 **81** 天 | 90 天 | 用满 2.7 个月即等价 |
| 半年付 | 85 折 | 5.1 M | 第 **153** 天 | 180 天 | 用满 5.1 个月即等价 |
| **年付** | **75 折** | **9.0 M** | 第 **270** 天 | 365 天 | **用满 9 个月，年付的钱就等于按月付价用完了** |

**这一列同时是 pricing §6 代价 3「年付 75 折把风险前置」的正面答复**：
年付用户在第 9 个月之前始终有正的退款额，第 270 天起归零 —— 而这不是拍脑袋定的界限，
是「已用部分按月付标价重算」这一口径的自然结果，**在法务页上可以一句话讲清**。

> 另一半答复来自会计口径：退到余额在复式账上是 `Dr liability:deferred_revenue / Cr liability:user_wallet`，
> `0007_ledger.up.sql:10–12` 亲口写了 `ledger_accounts` 里不存在 `asset:bank ← liability:user_wallet` 这条路径，
> **资产侧一分钱不动，所以不需要留退款准备金。** pricing §6 代价 3 的后半句因此不再存在。
> ⚠️ 这是一条**现金流事实**，不是「可以退得宽」的理由 —— 后者由上面的零增量敞口定理支撑。

### 3.3 三段升级链的算例（验证恒等式）

设轻量月付 ¥30 = 3000 分、标准月付 ¥60 = 6000 分、重度月付 ¥120 = 12000 分（**排版示意，取整数便于心算；实际定价 ¥72/¥159/¥358 见同批[定价修订](../03-product/pricing-and-plans-revision-20260823.md)，恒等式与具体价格无关**）。
用户 D0 买轻量月付，D15 升标准，D22 升重度，**D23 申请退款**，流量消耗轻微（`consumed_data ≈ 0`）：

| 订单 | `covers_from` | `amount_paid + amount_balance` | 段长 | `price_monthly_at_order` | 该段 `consumed_time` |
|---|---|---|---|---|---|
| D0 · new 轻量月付 | D0 | 3000 | 15 天 | 3000 | `3000 × 15/30` = 1500 |
| D15 · upgrade 标准 | D15 | 1500 | 7 天 | 6000 | `6000 × 7/30` = 1400 |
| D22 · upgrade 重度 | D22 | 4800 | 1 天 | 12000 | `12000 × 1/30` = 400 |
| **合计** | — | **`V_window` = 9300** | 23 天 | — | **`consumed_time` = 3300** |

`refund_B = 9300 − 3300 − 0 = 6000` 分 = **¥60.00**。
**自检**：用户真金白银掏了 ¥93.00，拿回 ¥60.00，净支出 **¥33.00**；
而他按月付标价实际消费的是 15 天轻量（¥15）+ 7 天标准（¥14）+ 1 天重度（¥4）= **¥33.00**。
**逐分相等 —— §1.1 的恒等式在链式升级 + 退款的组合下成立。**

对照草稿的口径：草稿的退款基数是 `V_source = 4800 + 1600 = 6400`，`ceil(23/30) = 1`，
`M = 12000` → `refund = 0`。**同一个用户，草稿判他一分不退，本裁决判退 ¥60。**
草稿的答案错在把「他升到重度只用了 1 天」按整月的重度价收费。

### 3.4 升级不再能重置计时（反方攻击二的落点）

草稿把 `elapsed_days` 锚在「最后一笔订单的 `paid_at`」上，于是**升级会把已服务时长归零**。
反方给的算例：轻量年付用户走到第 241 天（此刻直接退款应得 ¥0），改为「先升级到标准、同日退款」，
现金出 ¥91.73、余额进 ¥183.45，**净套回原订单面值 ¥270 的 34%**。
本裁决按 §3.2 重算同一条路径：

| 路径 | `V_window` | `consumed_time` | `refund_B` | 净支出 |
|---|---|---|---|---|
| 直接退款 | 27000 | `3000 × 241/30` = 24100 | 2900 | ¥241.00 |
| 先升级再同日退款 | 27000 + 9173 = 36173 | `3000 × 241/30 + 6000 × 0/30` = 24100 | 12073 | ¥241.00 |

**两条路径净支出逐分相等，套利消失。**（升级单：`D_left = 124`，`surplus = floor(27000×124/365) = 9172`，
`amount_gross = floor(54000×124/365) = 18345`，`amount_due = 9173`。）
注意本裁决在这里比反方自己的修法**更准确**：反方建议 `greatest(1, ceil(elapsed/30))`，
按那个公式同一条路径仍能净得 ¥31.72；按比例分段求和才让它恰好归零。

### 3.5 退款的副作用（必须与退款写在同一个事务里）

```sql
BEGIN;
  -- 1. 记录退款
  INSERT INTO refunds (order_id, user_id, amount, destination, status, rule, reason, operator_id)
  VALUES ($source_id, $uid, $refund, 'balance', 'done', $rule, $reason, $admin);
  -- rule ∈ ('cooling_off','prorated','service_terminated','manual')
  -- rule='cooling_off' 由 refunds_cooling_off_once 唯一索引保证一账号一生一次（§6.1）

  -- 2. 记账：预收账款 → 用户余额（两条负债对冲，资产侧不动）
  --    🔴 destination='balance' 的分录**只有**这两条腿，绝不允许碰 expense:refund。
  --       一旦记进去，损益表上会凭空出现一笔从未发生的费用，
  --       而「无现金流出」这个论证的账面证据就没了。
  --       expense:refund 只用于 destination='original'，以及第 4 步追不回来的佣金。
  INSERT INTO ledger_entries / ledger_lines ...;   -- Dr liability:deferred_revenue / Cr liability:user_wallet
  UPDATE wallet_balances SET balance = balance + $refund ...;

  -- 3. 立即终止订阅（不做「退一部分钱继续用」）
  UPDATE users SET
    plan_id              = NULL,
    transfer_enable_plan = 0,          -- ⚠️ 只清套餐配额
    expired_at           = now(),
    reset_at             = NULL,
    expiry_applied_at    = NULL,
    updated_at           = now()
  WHERE id = $uid;
  -- transfer_enable_pack 与 pack_expire_at **保留不动**：加油包没退钱，不能没收（§5.5）
  -- users_bump_user_rev_trg 会因 transfer_enable_plan / expired_at 变化自动 bump
  -- → 用户在 ≤ 60 秒内掉线（节点 push 周期 60 秒，servers.sql:5/:196）

  -- 4. 佣金按比例追回，**不论状态**
  clawback = ceil(c.amount * $refund::numeric / o.amount_paid)
  --   status='pending'                 → 直接 voided（全额）
  --   status IN ('confirmed','transferred') → 从 wallet_balances 扣回 clawback；
  --   wallet_balances 有 CHECK (balance >= 0)（data-model §7.1），扣不动的部分
  --   记 expense:refund 并写审计日志，由管理员人工处理（几十人量级，管理员认识每个人）
  UPDATE commissions SET status='voided', voided_reason='order_refunded' WHERE ... ;

  UPDATE orders SET status = 'refunded' | 'partially_refunded' WHERE id = $source_id;
  -- ⚠️ 与 ADR 0012 §7.1 对齐：那份裁决把 refunding / refunded / partially_refunded 列为
  --    「退款政策拍板前不实现」—— **本 ADR 就是那次拍板**，这三个态由此启用。
  --    本流程是单事务，**不经过 refunding**（§11 已登记 refunds.status 的 pending/failed 在余额路径上用不到），
  --    转移仍走 0012 §7.2 的 CAS 写法：WHERE id = $id AND status = $from，影响 0 行即当作失败。
  INSERT INTO order_transitions (...);
COMMIT;
```

**佣金三条硬规则**（草稿完全没有，正方提出，本裁决全部采纳）：

1. **佣金基数写死为 `orders.amount_paid`**，不含 `amount_balance` 与 `surplus_amount`。
   一句话同时封住「用余额刷佣金」与「用折抵刷佣金」。
   （这条同时补上了 payments §4.3 里「实付金额」一直没有定义的空白，也是 roadmap **B37** 的一部分。）
2. **退款按比例追回佣金，不论 `status`。** 理由：Class B **没有窗口上限**（年付到第 270 天都还有退款额），
   任何有限的冷静期都挡不住「等到第 16 天佣金 `confirmed` 之后再退款」。
   草稿 §7 抗套利表里「佣金冷静期 15 天 > 退款窗口 7 天」这一格因此是**假的安全感**，本裁决把它删掉重写。
3. **`ALTER TABLE commissions ADD CONSTRAINT commissions_no_self_invite CHECK (inviter_id <> invitee_id)`**
   —— 核对 `0007_ledger.up.sql:64–81` 确认现在没有这条约束。它挡不住「同一个人的两个账号」，
   但把最蠢的形态（自己邀请自己）变成数据库拒绝，成本一行。

**语义澄清（写进列 COMMENT，随迁移强制）**：
`refunds.amount` 记「**进到余额的总额**」，`orders.amount_refunded` 记「**真的退出去的现金**」。
两者只有 `destination='original'` 时相等。不写这条，第一个做对账的人会把 `sum(refunds.amount)` 当成现金流出；
`orders_refund_le_paid` 这条 CHECK 也会与「退到余额可能超过 `amount_paid`」冲突
（`V_window` 含 `amount_balance`）。

### 3.6 法务页可以直接用的条款（page-inventory §5.4「法务页」那一行的填空）

> **退款政策**
> 1. **首次订阅 7 天内**，若本周期已用流量不超过套餐配额的 10%（且不超过 10 GiB），可申请全额退款。**每个账号仅限一次。**
> 2. 超出上述条件的周期套餐，退款额 = 本次订阅期内你实际支付的总额 − 已服务天数按该档位**月付标价**折算的金额 − 本周期已消耗的套餐流量按月付标价折算的金额。计算结果为零或负数时不予退款。
>    换句话说：**你最终付的钱，等于你按月付价实际用掉的量。** 长周期折扣需要用满对应时长才能兑现 —— 年付享 75 折，因此**服务满 9 个月后不再有可退金额**。
> 3. **流量包、流量重置包、钱包充值不予退款。** 钱包余额仅可用于消费，不可提现、不可转让。
> 4. **退款一律退回站内钱包余额。** 唯一例外：本服务永久停止运营，或同一自然月内累计不可用达 72 小时 —— 此时可按第 2 条金额原路退回同链同币，网络手续费由退款额中扣除。
> 5. 违反服务条款（转售、超范围共享订阅）而被停用的账号，不予退款。
> 6. 退款生效后订阅立即终止，最长 60 秒内全部设备断开连接；**已购买的流量包配额予以保留**。
> 7. 若你通过邀请链接产生过返佣，退款时按同比例追回该笔返佣。

> **附带收益（正方提出，采纳登记）**：payments §4.7 开篇写「本类目**必须有明文退款政策，否则拒付时无从申辩**」，
> pricing §4 把 Paddle 列为「机会主义，值得申请，但不可放在关键路径」。
> 没有明文退款政策，这个期权连申请都递不出去。本节把它从「不可能」变成「可申请」，成本为零。

---

## 4 · ② 升级折抵

### 4.1 payments §4.6 从未成形 —— 本 ADR 是第一次给出公式

草稿把这一节写成「推翻 payments §4.6」。**措辞不准确**：`docs/README.md` §4 的「推翻」程序是针对 **ADR** 的，
而 payments 是**调研**。更要紧的是，§4.6 的原文自相矛盾：

> 补差价 = `新套餐价 − 旧套餐价 × (剩余天数/总天数) × (剩余流量/总流量)`，两个比例取**较小值**

**公式里写的是乘号，散文说的是取较小值，两种读法在同一行并存。** 两种都不能用：

- **`min` 读法**：两个比例量纲不同 —— `剩余天数/总天数` 是**跨订单**的（分母最长 365 天），
  `剩余流量/总流量` 是**周期内**的（分母 `plans.transfer_enable`，每 30 天重置）。
  一个年付用户在第 200 天升级、恰好本周期最后一天且配额用完：
  `165/365 = 0.452` 与 `0` 取 min = **0 折抵，165 天的已付时间被一笔勾销**；
  而第二天（新周期第 1 天）同一个人折抵 0.452。**同一份订阅的价值在 24 小时内从 0 跳到 45%。**
- **乘积读法更差**：剩 50% 天数 + 剩 50% 流量 → 折抵 0.25，用户被吞掉一半剩余价值，
  它在**完全正常**的场景下也系统性低估，没有反例可言 —— 它就是**总是**错的。

而且 §4.6 的前提在我们的 schema 里从第一天起就不成立：
`0001_enum_types.up.sql:17` 对 `monthly_on_order_day` 写着「Xboard 1：按订单日按月 ← 竞品实测的实际行为，**我们的默认**」，
`0002_foundation.up.sql:24` 的 `reset_traffic_method` 确实是 `NOT NULL DEFAULT 'monthly_on_order_day'`，
而 `order_period` 含 `yearly`、pricing §3.2 把年付 75 折定为主推档。
**「长周期订单 + 月度配额重置」不是边缘情况，是 DDL 层面的默认路径**，
而 Stripe 式 proration（§4.6 的出处）假定订阅周期 = 计费周期 = 配额周期。

**§4.6 还有第二处必须一并处理**（正方补出，采纳）：同一格的后半句
「**到期日不变，流量配额立即提升到新档**」。§4.4 把它改成按本周期剩余天数折算发放。
**从金额看，被换掉的这半句才是贵的那半句**（见 §4.4 的恒等式）。

### 4.2 公式

**模型：升级不改到期日、不改重置日、不改 `subscription_anchor_at`**（user-journey §10.2 的提案，采纳），
只改 `plan_id` / `group_id` / `device_limit` / `speed_limit_mbps` / `transfer_enable_plan`。

```
输入：uid, plan_new
前置：source 存在且 source.covers_to IS NOT NULL（§2.2）；plan_new.kind = 'cycle'；plan_new.sellable；
      price_new = plan_new 在 **source.period** 上的标价（分），NOT NULL 否则 422
      price_cur = 当前套餐在 source.period 上的标价（分）
      price_new > price_cur                        -- 否则 422 DOWNGRADE_NOT_ALLOWED

V_source, D_total, D_left   见 §2.2

surplus_raw  = floor(V_source * D_left / D_total)
amount_gross = floor(price_new * D_left / D_total)

surplus_amount  = least(surplus_raw, amount_gross)                     -- 折抵只能抵到 0，不产生找零
amount_discount = 0                                                    -- 升级单不接受优惠码
amount_balance  = least(user_balance, amount_gross - surplus_amount)   -- use_balance = true 时
amount_due      = amount_gross - amount_discount - surplus_amount - amount_balance   -- 恒 ≥ 0

写入 orders：
  type = 'upgrade'，plan_id = plan_new.id，period = source.period，
  surplus_amount，surplus_order_ids = ARRAY[source.id]，prev_order_id = source.id，
  covers_from = paid_at，covers_to = source.covers_to，
  price_monthly_at_order = plan_new.price_monthly     -- 下单时快照，见 §6.1
```

**`period = source.period` 是抗套利的一道闸**：新套餐按**同一个周期档**的标价折算，
所以年付用户升级时 `price_new` 用的是新套餐的**年付价（已含 75 折）**。
用户不能把年付折扣的信用拿去按月付标价买东西。

**验算 —— 折抵链的价值守恒**（轻量月付 3000、标准月付 6000、重度月付 12000）：

| 事件 | `covers_from` → `covers_to` | `V_source` | `D_total` | `D_left` | `surplus` | `amount_gross` | `amount_due` |
|---|---|---|---|---|---|---|---|
| D0 买轻量月付 | D0 → D30 | — | — | — | 0 | 3000 | **3000** |
| D15 升标准 | D15 → D30 | 3000 | 30 | 15 | 1500 | 3000 | **1500** |
| D22 升重度 | D22 → D30 | 1500 + 1500 = **3000** | **15** | 8 | **1600** | 6400 | **4800** |

D22 那一行的自检：用户此刻手上是「标准 ¥60/月、剩 8 天」，真值 `6000 × 8/30 = 1600`
—— **折抵额恰好等于真值**。链式升级不漏也不多，靠的是两件事：
① `V_source` 把 `surplus_amount` 计入（若只取 `amount_paid` = 1500，D22 的折抵会算成 800，**凭空吞掉用户 800 分**）；
② `upgrade` 的 `covers_from = paid_at`，使 `D_total` 在 D22 是 15 而不是 30。
**反方建议的「`upgrade` 继承 `covers_from`」会让 `D_total` 变回 30，折抵算成 800 —— 故该建议只部分采纳（§7.1 C16）。**

### 4.3 五个边界，逐条给规则

| # | 边界 | 规则 | 理由 |
|---|---|---|---|
| **1** | **降级** | **不允许中途降级。** `price_new ≤ price_cur` → `422 DOWNGRADE_NOT_ALLOWED`。套餐页对低档位显示「到期后可切换」，不显示「降级」按钮 | 降级必然 `surplus_raw > amount_gross`；不给找零用户觉得被坑，给找零就开了「买高档 → 降级 → 提走差额余额」的套现环，而 data-model §7.1 明确登记「余额不可提现在数据库层面**无法强制**，真正的守卫是 code review」。**在一条只能靠人守的约束上，规则本身必须留得更紧。** **撤回条件**：P2 阶段「买了高档用不完」的工单 ≥ 3 例，则改为「允许降级，但差额不折抵不退款，且下个周期才生效」 |
| **2** | **折抵额超过新订单金额** | `surplus_amount = least(surplus_raw, amount_gross)`，**差额不退、不转余额、不结转**。UI 显示被截断后的数值 + 一句「折抵最多抵到 0 元」 | 见边界 1。同时让 `orders_amount_balance` 这条 CHECK 永远不会以异常的形式炸出来 |
| **3** | **折抵后又退款** | **退款基数是 `V_window`（整条窗口链的实付求和），不是 `V_source`、不是 `amount_paid`。** 见 §3.3 算例 | `amount_paid` 会吞掉被折抵掉的部分（用户实实在在付过），`amount_gross` 会退回他没付过的钱，`V_source` 只覆盖最后一段。`V_window` 是唯一处处正确的量 |
| **3b** | **首单全退 + 长周期折扣的组合** | **Class A 只对该账号的第一笔订单开放，且一生一次**（§3.2 条件 ①⑤，并由 `refunds_cooling_off_once` 唯一索引强制） | ⚠️ **草稿给的理由是错的，本裁决删掉。** 草稿写「买年付 → 立刻升级 → Class A 全退 → 拿着按年付价买到的余额按月消费」，上界「25% × 一笔年付额」。反方指出这条套利**不存在**：年付 9.0 M 退成 9.0 M 余额，按月付价只能买 **9 个月**，而不折腾直接持有年付是 **12 个月** —— 走这条路用户拿到的服务更少。**规则保留，理由换成**：Class A 豁免的是 `consumed_time + consumed_data` 两项扣减，即豁免的正是恒等式本身，所以它**必须被配额**；「首单 + 一生一次」就是配额方式 |
| **4** | **折抵与优惠码叠加** | 升级单 `coupon_id` 必须为 NULL，否则 `422`。`amount_discount = 0` | `amount_gross` 已被 `D_left/D_total` 缩过一次；百分比券再作用一次是两层比例复合，用户算不清，且给了「先升级把 gross 缩小、再用固定额券」的组合口 |
| **5** | **折抵源已被退款** | `source.status` 必须严格等于 `'completed'`。`refunded` / `partially_refunded` / `chargeback_lost` 一律不可作为折抵源 → `V_source = 0` | 否则一份价值被折抵和退款各消费一次 |

### 4.4 当期配额：按**本周期**剩余天数折算发放

`amount_gross` 按「剩余**订阅**天数」折算，配额按「剩余**周期**天数」折算 —— 两个不同的比例，各自作用在正确的东西上。

```
cycle_days = CASE plans.reset_traffic_method
               WHEN 'yearly_on_order_day' THEN 365
               WHEN 'yearly_jan_first'    THEN 365
               WHEN 'never'               THEN NULL      -- 不折算，直接给满
               ELSE 30                                   -- monthly_* / follow_system
             END
cycle_left = least(cycle_days, greatest(0,
               ceil(extract(epoch FROM (users.reset_at - now())) / 86400)))

Δ = plan_new.transfer_enable - plan_cur.transfer_enable          -- 升级恒 > 0

UPDATE users SET
  transfer_enable_plan = transfer_enable_plan
                       + CASE WHEN cycle_days IS NULL THEN Δ
                              ELSE floor(Δ * cycle_left / cycle_days) END,
  plan_id = $new, group_id = $g, device_limit = $dl, speed_limit_mbps = $sl,
  updated_at = now()
WHERE id = $uid;
-- reset_at / reset_seq / subscription_anchor_at / expired_at 一律不动
-- 下个周期重置时 transfer_enable_plan 被置为 plan_new.transfer_enable 满额（§5.3），自然衔接
```

**这不是「少给」，它是一个恒等式。** 设周期长 `C`、本周期剩余天数 `cycle_left`、剩余订阅天数 `D_left`、新旧配额差 `Δ`：

```
按本节折算，用户在剩余订阅期内累计拿到的 Δ
  = Δ·cycle_left/C  +  Δ·(D_left − cycle_left)/C
  = Δ · D_left / C                       ← 恰好等于他按 D_left/D_total 付钱买到的周期数

不折算（payments §4.6 的「立即提升到新档」）
  = Δ·1 + Δ·(D_left − cycle_left)/C
  = Δ · D_left/C  +  Δ·(C − cycle_left)/C  ← 多出「本周期已过天数/C」份 Δ，白送
```

**折算让「付的钱 ↔ 拿的配额」在整个剩余订阅期上恒等；不折算才是每次升级白送 `Δ × 已过周期天数 / C`。**
把 `C = 30`、`cycle_left = 2` 代入，白送 `28/30 ≈ 0.93 Δ`。
换成现金：假设重度档 500 GiB/周期，一次的敞口是 `500 GiB × $0.0979/GiB = $49`，折算后 `500 × 2/30 × $0.0979 = $3.3`
—— **同一个动作，成本差 15 倍**。（500 GiB 是为了算这个比例而取的假设值，**配额数字未定**，pricing §7 仍挂着；
比例结论与具体数字无关。）

### 4.5 结算页三行（user-journey §10.2 硬要求）

> ⚠️ 下面的金额是**排版示意，不是价格提案** —— 价格数字至今未定（pricing §7）。
> 唯一有意义的关系是 `amount_due = amount_gross − surplus_amount`。

```
当前套餐剩余价值（轻量 · 年付 · 剩 137 天）      − ¥ 337.81     ← surplus_amount
升级到「标准 · 年付」（按剩余 137 天折算）        + ¥ 675.62     ← amount_gross
──────────────────────────────────────────────
本次实付                                            ¥ 337.81     ← amount_due
```

加一行**在金额块之外**的配额说明（不破坏「三行缺一不可」）：

> 本周期立即增加 **X GiB**（按本周期剩余 **N** 天折算），**下个周期起为完整的 Y GiB**。到期日不变，仍是 2027-03-01。

「剩余价值」旁的展开链接文案（user-journey §10.2 硬要求 1）：

> 剩余价值 = 你为当前订阅实际支付的金额 × 剩余天数 ÷ 该笔订单覆盖的总天数。
> 「实际支付」包含你上次升级时被折抵掉的部分，所以多次升级不会让你吃亏。

### 4.6 `orders.type` 的服务端推导（`POST /orders` 缺的那一段契约）

`CreateOrderRequest` 只有 `{plan_id, period, coupon_code, use_balance}`（`openapi.yaml:5898–5912`），`type` 必须服务端推导：

```
IF plans.kind = 'pack'                                   THEN 'traffic_pack'
ELSIF users.plan_id IS NULL OR users.expired_at <= now()  THEN 'new'
ELSIF plan_id = users.plan_id                             THEN 'renew'
ELSIF price_new > price_cur                               THEN 'upgrade'
ELSE 422 DOWNGRADE_NOT_ALLOWED
```

`'reset_pack'`（`plans.price_reset`，重置当期 `u/d` 但不加配额）**没有下单入口**，见 §11。
`'wallet_topup'` 走独立端点，不经 `POST /orders`。

⚠️ **`plans.kind` 不能给 `DEFAULT`。** 反方核出：`api/db/queries/orders.sql:24` 的 `CreatePlan`
显式列出 18 个列，其中没有 `kind`。若 `kind` 带 `DEFAULT 'cycle'`，
**每一个通过后台建出来的加油包套餐都会被默默写成 `kind='cycle'`**，
于是加油包被推导成 `new`/`renew`/`upgrade`，走进周期套餐的开通逻辑 —— 静默故障。
**裁决：`kind text NOT NULL CHECK (kind IN ('cycle','pack'))`，无 `DEFAULT`，并在同一批把 `CreatePlan` 改成 19 个参数。**
（`plans` 当前是空表，加无默认值的 NOT NULL 列不会失败。）

### 4.7 OpenAPI 与 DB 枚举的**四处**不一致（`POST /orders` / `GET /plans` 上线前必须修）

读 `openapi/openapi.yaml` 与 `0001_enum_types.up.sql` 对照，这四处现在就是错的：

| 位置 | OpenAPI | DB `CREATE TYPE` / DDL | 后果 |
|---|---|---|---|
| `PlanPrice.period`（`openapi.yaml:5747`） | 含 **`two_yearly`, `three_yearly`** | `plans` **根本没有** `price_two_yearly` / `price_three_yearly` 列（`0002:28–33` 只有 monthly/quarterly/half_yearly/yearly/onetime/reset） | 套餐页的价格结构体在类型层面允许我们**展示一个数据库存不下的周期**。落在 **`GET /plans`** 上 —— 草稿说「三处都恰好落在 `POST /orders` 链路」是错的 |
| `CreateOrderRequest.period`（`openapi.yaml:**5907**`，草稿写的 5906 差一行） | 同上 | `order_period` 只有 5 个值 | 请求通过 spec 校验，**在 INSERT 时报 `invalid input value for enum`** |
| `OrderStatus`（`openapi.yaml:5834`） | 6 个值，含 DB 里不存在的 **`processing`**；缺 `paying`/`underpaid`/`paid`/`refunding`/`partially_refunded`/`chargeback*` | `order_status` 有 14 个值 | 收银台轮询拿到 `underpaid` 时前端无类型；同一份文件里 `PaymentState`（`:5924`）的 description 明写「**必须含 `underpaid`**」，两者当面打架 |
| `Order.type`（`openapi.yaml:5845`） | `[new, renew, upgrade, traffic_pack]` | `order_type` 有 6 个（多 `reset_pack`, `wallet_topup`） | 钱包充值单在用户面订单列表里无法序列化 |

**修法**：以 DB 枚举为准（data-model §14.1「本文为准」），删 `two_yearly`/`three_yearly`/`processing`，补齐其余。
同时把 `Order.surplus_amount` 与 `createOrder` 上那两段「⚠️ 现在没有契约」的 description 换成本 ADR §4.2 的公式与链接。

---

## 5 · ③ 流量包与周期重置

### 5.1 裁决：保留（跨周期结转），并拆列

user-journey §10.1 建议保留，理由是「清零会让月末买包的用户直接烧钱」。**采纳，并补三条它没写的理由**：

1. **清零会让政策与自己的销售动线打架。** user-journey §10.1 与 §11.1 把流量包的购买触发点设在
   「流量用到 80% / 95% 的提醒」里 —— 那正是周期末。清零政策等于教用户「别在告警时买，等下个周期初再买」，
   而下个周期初他不缺流量，于是**不买了**。竞品实证显示流量重置包近半年复购
   **6 次（¥23 档）+ 1 次（¥14 档）**（competitor-conyss §3.3），这是被 pricing §3.3 认定的「重要的第二营收曲线」。
2. **这不是新设计，是仓库里已经写好答案的缺口。** 同一个缺口在三处独立登记：
   `api/db/queries/users.sql:86–88`（`AddUserTransferQuota` 上方的「⚠️ 已知缺口」注释，逐字写着
   「修复需要拆成 `transfer_enable_plan` + `transfer_enable_pack` 两列，而那依赖一条**尚未裁决的产品规则**」）、
   data-model §16、user-journey §10.1（「标：待裁决」）。**ADR 0013 ③ 就是那次裁决。**
   连带效果：roadmap 的 **B24 / B25 / B26 三条连号一次关掉**，
   而这三条是 **28 条开放阻塞项**（launch-readiness-review-20260821 的 2026-08-21 时点快照；
   roadmap 其后已增补到 B46）里**唯一一组不依赖域名、支付网关、ESP 三个外部依赖**的。
3. **敞口可封**：`pack_expire_at`（12 个月）+ 一条监控指标（§9）。

### 5.2 列定义

```sql
ALTER TABLE users
  ADD COLUMN transfer_enable_plan bigint NOT NULL DEFAULT 0 CHECK (transfer_enable_plan >= 0),
  ADD COLUMN transfer_enable_pack bigint NOT NULL DEFAULT 0 CHECK (transfer_enable_pack >= 0),
  ADD COLUMN pack_expire_at       timestamptz;

UPDATE users SET transfer_enable_plan = transfer_enable;   -- 回填：现有值全部视为套餐配额

ALTER TABLE users DROP COLUMN transfer_enable;
ALTER TABLE users ADD COLUMN transfer_enable bigint
  GENERATED ALWAYS AS (transfer_enable_plan + transfer_enable_pack) STORED NOT NULL;
--                                                                  ^^^^^^^^
--  🔴 这两个词不是可选的，见 §7.1 C6 · §7.2：漏了它，读侧 Go 类型全线 int64 → *int64
```

**为什么用生成列而不是让应用代码维护总额**：漏更新总额的后果是**静默的**（用户配额算错而没有任何报错）。
生成列让「两个分量与总额不一致」这个状态**在 schema 层不可表达**。
**决定性的一条理由是 D1**：page-inventory §4.4 的 D1「改用户流量配额 / 到期时间」被标注为
「**直接等于送钱**；也是内部欺诈面」—— 这是一条**管理员会绕过应用层直接改配额**的既定路径。
触发器只在 SQL 写入时生效（这点两者相同），但**生成列还能保证从 psql 手改也算不出不一致的总额**，
因为「总额」根本不是一个可被赋值的东西。
另一条：`0012_user_rev_triggers.up.sql` 自己带着撤回条件（「当所有写路径都收敛到 3–5 个明确的 service 方法时，
应当把触发器改回显式调用并删掉它」）—— 往一个计划中要拆掉的机制上再挂一条业务不变量是错的；
生成列不需要撤回条件，因为它是**约束**不是**行为**。

**三条落地细节（其中两条是草稿写反了的）**：

1. ✅ **`users_bump_user_rev()` 必须改。** 它是 `AFTER` 触发器，`NEW.transfer_enable` 在 AFTER 阶段有值，
   所以现有代码其实能跑；但这依赖一条容易被后人改坏的语义。**显式把监视列表改成两个分量**：
   ```
   ELSIF (OLD.uuid, OLD.banned, OLD.expired_at,
          OLD.transfer_enable_plan, OLD.transfer_enable_pack,
          OLD.speed_limit_mbps, OLD.device_limit, OLD.deleted_at, OLD.expiry_applied_at)
      IS DISTINCT FROM (NEW.… 同构 …) THEN
   ```
   ⚠️ **改了它就必须同步改 `0014.down`**，见 §6.2。
2. ❌ **草稿写「读侧完全透明，`ListAvailableUsersByServer` / `ResolveSubscriptionToken` / `GetUser` 不用改」——
   只有加了 `NOT NULL` 才成立。** 见 §7.1 C6 · §7.2。
3. ❌ **草稿写「三条写 `transfer_enable` 的 query 会在 `sqlc generate` / `go build` 阶段炸出来，
   编译器替我们做了全量检查」—— 实测为假。** 见 §7.1 C5。**写 `users.transfer_enable` 的只有三条（`users.sql:74` `ApplyUserEntitlement` · `users.sql:90` `AddUserTransferQuota` · `stats.sql:215` `AdvanceUserResetCycle`，2026-08-23 全库 `grep` 复核），它们必须靠人改对，并靠新增的 CI 作业兜底。**

### 5.3 消耗顺序与重置公式

节点侧不存在「扣桶」这个动作 —— `user_traffic` 只累加 `u`/`d`，
可用性判定是 `ut.u + ut.d < u.transfer_enable`（`servers.sql:149`）。所以消耗顺序是**展示与结转时的虚拟口径**：

```
total_enable = transfer_enable_plan + transfer_enable_pack       -- = transfer_enable
used         = u + d
plan_used    = least(used, transfer_enable_plan)                 -- §3.2 的 consumed_data 用的就是它
pack_used    = greatest(0, used - transfer_enable_plan)
```

**先扣套餐、后扣加油包。** 唯一正确的顺序：套餐配额**会过期**（每周期重置清零），加油包配额**会结转**。
先消耗会过期的那份，对用户永远不亏，也是唯一不会引发「你怎么先扣我买的包」工单的顺序。

**周期重置必须写成一条语句**（改写 data-model §6.2 / `stats.sql:204–219`）。
反方核出两个洞：现有 `AdvanceUserResetCycle` 只有 `FROM plans p`，**拿不到 `u`/`d`**，无处算 `carry_pack`；
而它与 `ResetUserTraffic` 是 `Querier` 上两个互不相干的方法，**顺序没有定死** ——
若调用方先跑 `ResetUserTraffic`（`u=0, d=0`），`carry_pack` 恒等于 `transfer_enable_pack`，
**加油包永远不被消耗，只增不减，且完全静默**。

**裁决：把两条语句合并成一条，让「顺序」这件事不可表达。**

```sql
-- name: AdvanceUserResetCycle :one
WITH cur AS (
  SELECT ut.user_id, ut.u, ut.d
  FROM user_traffic ut
  WHERE ut.user_id = $1
  FOR UPDATE                                   -- 同一快照，且挡住并发上报
), zeroed AS (
  UPDATE user_traffic ut
  SET u_lifetime = ut.u_lifetime + cur.u,
      d_lifetime = ut.d_lifetime + cur.d,
      u = 0, d = 0, updated_at = now()
  FROM cur WHERE ut.user_id = cur.user_id
  RETURNING ut.user_id
)
UPDATE users u SET
  reset_seq = u.reset_seq + 1,
  reset_at  = CASE p.reset_traffic_method
    WHEN 'never'                THEN NULL
    WHEN 'monthly_first'        THEN date_trunc('month', now()) + interval '1 month'
    WHEN 'yearly_jan_first'     THEN date_trunc('year',  now()) + interval '1 year'
    WHEN 'monthly_on_order_day' THEN u.subscription_anchor_at + (u.reset_seq + 1) * interval '1 month'
    WHEN 'yearly_on_order_day'  THEN u.subscription_anchor_at + (u.reset_seq + 1) * interval '1 year'
    ELSE u.subscription_anchor_at + (u.reset_seq + 1) * interval '1 month'   -- follow_system
  END,
  transfer_enable_plan = p.transfer_enable,                       -- ← 只覆盖套餐分量
  transfer_enable_pack = greatest(0, u.transfer_enable_pack
                          - greatest(0, (cur.u + cur.d) - u.transfer_enable_plan)),   -- ← carry_pack
  updated_at = now()
FROM plans p, cur
WHERE p.id = u.plan_id AND u.id = cur.user_id
RETURNING u.id, u.reset_seq, u.reset_at,
          u.transfer_enable_plan, u.transfer_enable_pack, u.transfer_enable,
          cur.u AS old_u, cur.d AS old_d;      -- 直接喂给 InsertTrafficResetLog，不用二次查询
```

`carry_pack` 里的 `greatest(0, …)` 不是防御性代码，是必需的：v2node 每 60 秒上报一次
（`servers.sql:5`、`:196`），`u+d` 会**越过** `transfer_enable` 若干字节才被判定耗尽，
于是 `pack_used` 可能 > `transfer_enable_pack`。

验算三例（套餐 100 GiB、包 50 GiB）：

| 本周期 `u+d` | `plan_used` | `pack_used` | `carry_pack` | 重置后总额 |
|---|---|---|---|---|
| 60 GiB | 60 | 0 | 50 | 100 + 50 = 150 |
| 130 GiB | 100 | 30 | 20 | 100 + 20 = 120 |
| 160 GiB（上报越界） | 100 | 60 | **0** | 100 + 0 = 100 |

> ⚠️ 合并之后，重置同时改 `transfer_enable_plan` 与 `transfer_enable_pack`，两列都在触发器监视列表里，
> **所以重置本身会 bump `user_rev`**。这修掉了 `stats.sql:201–203` 记录的一个已知坑
> （「本语句只改 reset_seq/reset_at，不在触发器监视列表里，调用方必须显式 `BumpUserRevForUser`」）
> —— 改完之后那条注释和那次显式调用都可以删掉。
> **但删之前要确认**：`reset_traffic_method = 'never'` 的用户重置时两列都不变，此时仍不会 bump（本来也不需要）。
> `ResetUserTraffic` 保留，但收窄为**管理员手工重置 / `reset_pack`** 专用。

### 5.4 `reset_traffic_method` 如何对齐

**语义收窄：`plans.reset_traffic_method` 从此只管 `transfer_enable_plan`，对 `_pack` 无效力。**
这句话必须写进 `0001_enum_types.up.sql` 的 `reset_method` 注释与 `plans` 列注释。

| `reset_method` | `_plan` | `_pack` | `pack_expire_at` |
|---|---|---|---|
| `monthly_first` / `monthly_on_order_day` / `follow_system` | 每 30 天覆盖为 `plans.transfer_enable` | `carry_pack` 结转 | 独立，不受重置影响 |
| `yearly_jan_first` / `yearly_on_order_day` | 每 365 天覆盖 | 同上 | 同上 |
| `never`（不限时套餐，P1 不售，见 §2.2） | 不重置（`reset_at IS NULL`） | 不重置，**只增不减** | **必须靠这一列封口**，否则无限累积 |

**加油包有效期 = 12 个自然月**，买新包时统一顺延：

```sql
-- name: AddUserTransferQuota :one   （trigger_source = 'pack'，该取值 0006 迁移里已存在）
UPDATE users SET
  transfer_enable_pack = transfer_enable_pack + $2,
  pack_expire_at = greatest(coalesce(pack_expire_at, now()), now() + interval '12 months'),
  updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING id, transfer_enable_plan, transfer_enable_pack, transfer_enable;
```

12 个月的出处：等于最长的售卖周期（年付），这样「买年付时顺手买的包」在整个订阅期内都有效。
清理由每日一次的 Cloud Scheduler 作业执行：

```sql
UPDATE users SET transfer_enable_pack = 0, pack_expire_at = NULL, updated_at = now()
WHERE pack_expire_at IS NOT NULL AND pack_expire_at <= now() AND deleted_at IS NULL;
-- 这条 UPDATE 会经触发器 bump user_rev（transfer_enable_pack 在监视列表里）✅
```

> 🔴 **依赖警告：`bp-*` 的 Cloud Scheduler 作业目前是 0 条。**
> 本节的过期清理、data-model §6.2 的每分钟重置扫描、§3.5 的佣金确认与追回，
> **三件事共用一套尚不存在的调度基础设施。** 这不是本 ADR 能解决的，登记在 §10 代价 6 与 §11。

### 5.5 加油包的四条边界

1. **不改到期日、不改重置日、不改设备数**（user-journey §10.1 表格，采纳原样）。
2. **订阅到期时不清零。** `ListAvailableUsersByServer` 有 `expired_at > now()` 闸门，到期用户即使有包也连不上；
   包配额**保留**，续费后立刻可用，直到 `pack_expire_at`。
   > **附带收益（正方提出，采纳登记）**：user-journey §11.3 的数据保留是
   > 「到期后 0–90 天：账号保留、订阅 token 保留但拉取返回空、**续费即恢复**」，§11.4 的度量里有「90 天内回流率」。
   > 「你还有 X GiB 没用完，续费就能接着用」是一个**不依赖邮件**的回流钩子 ——
   > 而 ESP 至今未接通（`auth.go` 的 `TODO(P1)`），`bp_mail_bounce` 是仍缺的 3 条指标之一。
   > 在唯一的失联恢复通道还没通的当下，这条有额外权重。`pack_expire_at`（12 个月）远长于 token 保留期（90 天）。
3. **退款终止订阅时不清零**（§3.5 第 3 步）—— 包没退钱，不能没收。
4. **加油包本身不退款**（Class C）。理由：它是一次性消耗品，且**下单即生效**，无法判定「已用了多少包」
   （消耗顺序是虚拟口径，包和套餐的字节在 `u`/`d` 里混在一起）。这一条必须写进法务页，
   否则会有人拿「买了包但套餐先到期了」来要退款。

---

## 6 · 迁移 `0016` 与代码改动清单

### 6.1 `0016_billing_rules.up.sql`

```sql
-- 0014 · 计费与退款规则（ADR 0013）
--    ⚠️ 编号取 0014 而不是 0013：`0013_rate_limit`（PR #15，2026-08-23）已占用 0013。

-- ---- ③ 流量包配额拆列 ----
ALTER TABLE users
  ADD COLUMN transfer_enable_plan bigint NOT NULL DEFAULT 0 CHECK (transfer_enable_plan >= 0),
  ADD COLUMN transfer_enable_pack bigint NOT NULL DEFAULT 0 CHECK (transfer_enable_pack >= 0),
  ADD COLUMN pack_expire_at       timestamptz;
UPDATE users SET transfer_enable_plan = transfer_enable;
ALTER TABLE users DROP COLUMN transfer_enable;
ALTER TABLE users ADD COLUMN transfer_enable bigint
  GENERATED ALWAYS AS (transfer_enable_plan + transfer_enable_pack) STORED NOT NULL;
COMMENT ON COLUMN users.transfer_enable IS
  '生成列（STORED）：= _plan + _pack。不可赋值；对外与 subscription-userinfo 的 total= 保持单一口径。';
CREATE INDEX users_pack_expiry_due_idx ON users (pack_expire_at)
  WHERE pack_expire_at IS NOT NULL AND deleted_at IS NULL;

-- ---- ①② 订单的服务区间与窗口链 ----
ALTER TABLE orders
  ADD COLUMN covers_from            timestamptz,
  ADD COLUMN covers_to              timestamptz,
  ADD COLUMN prev_order_id          bigint REFERENCES orders(id) ON DELETE RESTRICT,
  ADD COLUMN price_monthly_at_order bigint CHECK (price_monthly_at_order >= 0),
  ADD COLUMN pay_from_address       text;      -- 链上付款方地址，归集时按 txid 回填（§9 的失效条件靠它执行）
COMMENT ON COLUMN orders.covers_from IS
  '本单买到的服务生效时刻。new/upgrade = paid_at；renew = greatest(paid_at, 旧 covers_to)。ADR 0013 §2.1';
COMMENT ON COLUMN orders.covers_to IS
  '本单服务结束时刻；NULL = 不限时（onetime）。upgrade 继承被折抵单的值。ADR 0013 §2.1';
COMMENT ON COLUMN orders.price_monthly_at_order IS
  '下单时 plans.price_monthly 的快照（分）。退款扣减必须用它，不能读活列 —— 否则涨价后退款额变小，
   用户会认为我们改价来少退钱（user-journey §10.2 硬要求 1）。ADR 0013 §3.2';
COMMENT ON COLUMN orders.amount_refunded IS
  '只记真的退出去的现金（destination=original）。退到余额时恒为 0；退款总额的唯一真相源是 refunds.amount。';
CREATE INDEX orders_prev_idx ON orders (prev_order_id) WHERE prev_order_id IS NOT NULL;

-- ---- ② 区分周期套餐与加油包（orders.type 推导需要）；🔴 不给 DEFAULT，见 §4.6 ----
ALTER TABLE plans
  ADD COLUMN kind text NOT NULL CHECK (kind IN ('cycle','pack'));
-- 月付标价是退款扣减的乘数，不能为 NULL（0002 的注释：NULL = 该周期不售）
ALTER TABLE plans
  ADD CONSTRAINT plans_cycle_needs_monthly CHECK (kind <> 'cycle' OR price_monthly IS NOT NULL);

-- ---- ① 退款规则可机检、可审计，且「一生一次」由数据库强制 ----
ALTER TABLE refunds
  ADD COLUMN rule    text NOT NULL DEFAULT 'manual'
      CHECK (rule IN ('cooling_off','prorated','service_terminated','manual')),
  ADD COLUMN user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX refunds_cooling_off_once ON refunds (user_id) WHERE rule = 'cooling_off';
CREATE INDEX refunds_rule_idx ON refunds (rule, created_at DESC);

-- ---- ① 佣金：自邀在数据库层被拒绝 ----
ALTER TABLE commissions
  ADD CONSTRAINT commissions_no_self_invite CHECK (inviter_id <> invitee_id);

-- ---- ③ 重置审计能分开看两个分量 ----
ALTER TABLE traffic_reset_log
  ADD COLUMN new_transfer_enable_pack bigint NOT NULL DEFAULT 0;
COMMENT ON COLUMN traffic_reset_log.new_transfer_enable IS '重置后的**总额**（_plan + _pack），不是 plan 分量';

-- ---- 触发器：监视列表换成两个分量（§5.2 细节 1）----
CREATE OR REPLACE FUNCTION users_bump_user_rev() RETURNS trigger AS $$ … $$ LANGUAGE plpgsql;
```

`refunds.user_id` 加 `NOT NULL` 在空表上没有问题（`refunds` 当前 0 行）。
它同时解决反方 C14：草稿刻意不给 `refunds` 冗余 `user_id`（「它只能通过订单归属到人」），
代价是 Class A 的「一生一次」**只能靠应用代码不写错**。加了这一列 + 部分唯一索引之后，
这条规则变成**数据库拒绝**，与本项目「让非法状态在 schema 层不可表达」的一贯取向一致。

### 6.2 `0016_billing_rules.down.sql`（顺序是硬要求）

反方在 `postgres:17` 上实测、本文作者复现（§7.1 C7 · §7.2）：**按草稿描述的顺序跑完 down，`users` 表的任何 UPDATE 都会失败**，
报 `ERROR: record "old" has no field "transfer_enable_plan"`，且 CI 结构上抓不到。
`down` 必须按下面的顺序，**先把触发器函数换回 0012 的函数体，再删分量列**：

```sql
-- 1. 🔴 第一步：把 users_bump_user_rev() 还原成 0012 的函数体（监视 OLD.transfer_enable）
CREATE OR REPLACE FUNCTION users_bump_user_rev() RETURNS trigger AS $$ … 0012 原文 … $$ LANGUAGE plpgsql;

-- 2. 恢复普通列，再回填，再删分量列（顺序反了会丢数据）
ALTER TABLE users DROP COLUMN transfer_enable;
ALTER TABLE users ADD COLUMN transfer_enable bigint NOT NULL DEFAULT 0;
UPDATE users SET transfer_enable = transfer_enable_plan + transfer_enable_pack;
ALTER TABLE users DROP COLUMN transfer_enable_plan, DROP COLUMN transfer_enable_pack,
                  DROP COLUMN pack_expire_at;
DROP INDEX IF EXISTS users_pack_expiry_due_idx;

-- 3. 其余对称回退（orders 五列 / plans.kind + CHECK / refunds 两列 + 两索引 /
--    commissions CHECK / traffic_reset_log 一列）
```

> ⚠️ **第 1 步只在「先跑 `0014.down`、观察、再决定要不要继续」这种真实生产回滚里才救得了命** ——
> CI 逆序继续跑过 `0013_rate_limit.down`，到 `0012.down` 的第一句就是
> `DROP TRIGGER … DROP FUNCTION users_bump_user_rev()`（`0012_user_rev_triggers.down.sql:3–4` 实查），
> **把证物销毁了**，而作业最后只断言「表 0 · 视图 0 · 枚举 0」，**从不写一行数据**。
> 所以这一条必须靠 §6.4 的新 CI 作业兜底。

### 6.3 迁移的执行风险

| 项 | 评估 |
|---|---|
| `ADD COLUMN … GENERATED … STORED NOT NULL` 会**重写全表** | `users` 当前是**空表**（0 个产品用户；`api/internal/handler/operations.txt` 有 128 个 operation，`POST /orders` 整条链路尚未实现）。**迁移窗口只有现在是零成本** —— 等到有付费用户再拆列，就要在活数据上做 `DROP COLUMN` |
| `DROP COLUMN` + 同名 `ADD COLUMN` 会改变列序 | `SELECT *` 的 sqlc query（`ApplyUserEntitlement … RETURNING *`、`BanUser … RETURNING *`）靠列名映射不靠列序；`sqlc generate` 会重排结构体字段，CI 的 `git diff --exit-code` 会捕获生成物漂移 |
| ~~生成列不能被 `UPDATE` 赋值 → 3 处写路径会在 `sqlc generate` / `go build` 阶段炸出来~~ | **🔴 实测为假，整行作废。** `sqlc/sqlc:1.31.1` generate **exit 0**、`go build` 通过、生成物里原封不动留着非法 UPDATE。第一次暴露点是**生产环境里第一笔付款成功之后的 `ApplyUserEntitlement`**：用户付了 USDT，订单进 `paid`，开通权利时 500。见 §7.1 C5 与 §6.4 |
| `plans.kind` 无 `DEFAULT` | `plans` 是空表，加 NOT NULL 无默认值列不会失败；`CreatePlan` 必须同批改（§4.6） |

### 6.4 CI 必须新增一个真的检查

现有 `migrations` 作业只灌 `*.up.sql` / `*.down.sql` 再数表、索引、视图的个数，
**从头到尾不执行任何一条 `db/queries/*.sql`，也不写一行数据**（`.github/workflows/ci.yml:227–307`）。
本 ADR 引入的两类错误（写生成列、down 之后触发器指向已删列）**都落在这个盲区里**。

**裁决：在 `migrations` 作业里追加两步，复用现有的 `postgres:17` service：**

```yaml
- name: 语义校验全部写语句（不执行，只做解析与规划）
  run: |
    # 灌完全部 up 之后，对每条 db/queries/*.sql 里的 INSERT/UPDATE/DELETE 跑一次 EXPLAIN。
    # 生成列不可写这类错误在 EXPLAIN 阶段就会报，而 sqlc 与 go build 都拦不住。
    # 失败提示：本地跑 make db-explain 复现。

- name: 回滚后再写一次
  run: |
    # 0014.down 之后（0013_rate_limit.down / 0012.down 之前）执行：
    psql -v ON_ERROR_STOP=1 -c "UPDATE users SET updated_at = now() WHERE false;"
    # 抓 §6.2 那条 record "old" has no field 的触发器失配。
```

> 这一条不是锦上添花。本项目 AGENTS.md §3 的事实纪律要求
> **一条从未被验证过的检查应默认视为不工作** —— 草稿正是在这里写了「编译器替我们做了全量检查」，
> 削弱了唯一那道防线（实施者的细心）。

### 6.5 必须同批改的 8 个 sqlc query

| 文件:行 | 现状 | 改成 |
|---|---|---|
| `db/queries/users.sql:74` `ApplyUserEntitlement` | `transfer_enable = $4` | `transfer_enable_plan = $4`（开通/续费/升级写套餐配额） |
| `db/queries/users.sql:90` `AddUserTransferQuota` | `transfer_enable = transfer_enable + $2`；上方 `:86–88` 注释写着「已知缺口」 | `transfer_enable_pack = transfer_enable_pack + $2` + `pack_expire_at` 顺延（§5.4）。**`:86–88` 那段「已知缺口」整段删掉** |
| `db/queries/stats.sql:204–219` `AdvanceUserResetCycle` | `FROM plans p`，只改 `transfer_enable`，且与 `ResetUserTraffic` 顺序未定 | **整条换成 §5.3 的合并 CTE**；删掉 `:201–203` 关于「调用方必须显式 `BumpUserRevForUser`」的注释 |
| `db/queries/stats.sql:186` `InsertTrafficResetLog` | 8 个参数 | 9 个（补 `new_transfer_enable_pack`） |
| `db/queries/stats.sql:191` `ResetUserTraffic` | 周期重置与手工重置共用 | 收窄为**管理员手工重置 / `reset_pack`** 专用，注释写明 |
| `db/queries/orders.sql:24` `CreatePlan` | 18 个列，无 `kind` | 19 个（补 `kind`），见 §4.6 |
| `db/queries/orders.sql:45` `CreateOrder` | 15 个列 | 19 个（补 `covers_from`, `covers_to`, `prev_order_id`, `price_monthly_at_order`） |
| **新增** `db/queries/orders.sql` `GetRefundBasis` | — | §3.2 的 `WITH RECURSIVE win …` 窗口链查询，返回 `V_window` / 分段明细 |

**不需要改**（全部是读侧，加了 `NOT NULL` 之后生成列透明）：
`servers.sql:149`、`servers.sql:259–260`、`stats.sql:59–60`、`subscriptions.sql:28`、`users.sql:57`、`users.sql:121`。

---

## 7 · 辩论与裁决

本节是本 ADR 的核心价值：让「为什么不是另一种做法」留在记录里。
反方 20 条、正方 15 条，逐条给裁决，**没有一条被无视**。

### 7.1 反方（`adr-0013-billing-rules.con.md`）

| # | 反方论点 | 裁决 | 理由与落点 |
|---|---|---|---|
| **C1** | `ceil(elapsed_days/30)` 在订单头 24 小时 = 0，Class B 退化为**无首单限、无次数限、无流量闸门**的全额退款窗口 | **采纳** | 成立，纯算术。**落点：§3.2 整条公式换掉** —— `consumed_time` 改为按比例（不取整到月），并新增 `consumed_data` 流量项。反方自己的修法 `greatest(1, ceil(...))` 只把洞缩小 3 倍，本裁决按比例分段求和让它恰好归零（§3.4 算例）。附带修正：草稿法务页「服务满 9 个月」在 `ceil` 版本下落在第 241 天（8.03 个月），按比例版本落在第 270 天，**与条款逐字一致** |
| **C2** | 升级重置 `source.paid_at`，年付第 241 天「先升级再同日退款」净套回面值 34%（现金出 ¥91.73、余额进 ¥183.45） | **采纳** | 成立。**落点：§2.1 新增 `orders.covers_from/covers_to/prev_order_id`；§3.2 的 `consumed_time` 沿窗口链分段求和。** 重算后两条路径净支出均为 ¥241.00，**逐分相等**（§3.4） |
| **C3** | 无闸门窗口的成本上界「只由带宽决定」，100 Mbps × 24h ≈ 1,030 GiB ≈ **$101/次** | **部分采纳** | 结构性结论采纳（无闸门 → 上界不由政策决定），**但数字是错的**：`ListAvailableUsersByServer` 的 `ut.u + ut.d < u.transfer_enable`（`servers.sql:149`）让节点在配额耗尽时停止服务，所以真实上界是 **`套餐配额 × $0.0979/GiB` + 一个 push 周期的溢出**。溢出 = 60 秒 × 100 Mbps ≈ **0.70 GiB ≈ $0.07**。以 500 GiB 重度档计上界是 **$49**，不是 $101，而 1,030 GiB 的配额档不存在。**落点：§3.2 的 `consumed_data` 让这个上界进一步塌成 0 增量** |
| **C4** | PostgreSQL 确实拒绝写生成列（认可草稿） | **采纳（无争议）** | 本文作者 2026-08-23 在 `postgres:17` 复现：`ERROR: column "te" can only be updated to DEFAULT / DETAIL: Column "te" is a generated column.` |
| **C5** | `sqlc/sqlc:1.31.1` generate **exit 0**、`go build` 通过 → 草稿的「编译器替我们做了全量检查」为假；首次暴露点是生产付款后的 `ApplyUserEntitlement` | **采纳** | 成立且已复核成因：`api/sqlc.yaml` 只有 `engine`/`schema`/`queries`，**没有 `database:` 段**，内置 postgresql 引擎只做语法与列名解析。**落点：§6.3 该行整行作废；§6.4 新增 CI 作业；§5.2 细节 3 改写为「必须靠人改对」** |
| **C6** | `STORED` 漏 `NOT NULL` → `attnotnull = f`，配合 `emit_pointers_for_null_types: true` 让读侧 Go 类型全线 `int64 → *int64`，含 `ResolveSubscriptionToken`（即 `subscription-userinfo: total=` 的数据来源） | **采纳** | 本文作者一手实测确认两点：不加 `NOT NULL` 时 `attnotnull = f`；**PG 17 接受 `GENERATED ALWAYS AS (…) STORED NOT NULL`，加上后 `attnotnull = t`**。`api/sqlc.yaml` 的 `emit_pointers_for_null_types: true` 也已核对（配置注释写明「这条是任务书点名的硬要求」）。**落点：§5.2 与 §6.1 的 DDL 加两个词** |
| **C7** | 按草稿顺序跑完 down，`users` 表任何 UPDATE 报 `record "old" has no field "transfer_enable_plan"`；CI 结构上抓不到（`0012.down` 先销毁证物，且从不写数据） | **采纳** | 本文作者用等价最小用例复现：`ERROR: record "old" has no field "a" / CONTEXT: PL/pgSQL function bump() line 3 at IF`。CI 盲区已核对（`ci.yml:227–307`）。**落点：§6.2 的 down 顺序（先 `CREATE OR REPLACE FUNCTION` 回 0012 版本）+ §6.4 的第二步检查** |
| **C8** | `AdvanceUserResetCycle` 只有 `FROM plans p`，拿不到 `u`/`d`；且与 `ResetUserTraffic` 的先后顺序未定，先跑后者会让加油包**只增不减**且完全静默 | **采纳** | 已核对 `stats.sql:204–219`（只有 `FROM plans p`）与 `:191 ResetUserTraffic`。**落点：§5.3 把两条语句合并成一条带 `FOR UPDATE` 的 CTE，让「顺序」不可表达** —— 这比反方建议的「写明顺序」更强，理由与本项目选生成列的理由同源 |
| **C9** | §3.1「退到余额 → 现金支出为零 → 退多少可以给得宽」在**成本分摊**定位下不成立（`成本/售价 → 1`），由它派生的宽松度（Class A、Class B 无窗口）应收回 | **采纳论证，部分采纳结论** | 推理批评完全成立，**§3.2 已删掉这条推理**。但「收回宽松度」不采纳：本裁决改用**零增量敞口定理**（§3.2）支撑宽松度 —— 加上 `consumed_data` 之后，退款用户与不退款用户的单位消费价格完全相同，敞口恒为 0，与 `成本/售价` 比值无关。Class B 的「无窗口上限」因此**保留**（它现在有对价）；Class A **保留但重新定价**（§7.1 C12 / §10 代价 2）。会计事实（无现金流出 → 不需要退款准备金，pricing §6 代价 3 后半句消解）降级为**现金流事实**，不再充当政策理由 |
| **C10** | 多条失效条件不可观测：pack 敞口无指标、`bp-*` 告警 0 条、预算口径已被自用出口 1.5 倍突破、`orders` 没有 `from` 地址列 | **采纳** | 逐条核对属实：`orders` 表 `0006:31–78` 只有 `pay_address`（我们的收款地址）、`gateway_ref`、`pay_amount_received`，**没有付款方地址**；evidence §4.1 的 08-17→08-20 速率外推是 **约 7,800 GiB/月 ≈ $764/月 gross**，已是 `$500/月` 预算的 **1.53 倍**。**落点：§6.1 加 `orders.pay_from_address`；§9 的阈值改成绝对值 $100/月，不再写「预算的 20%」；不可观测的条目移入 §11** |
| **C11** | 佣金 15 天冷静期挡不住无窗口的 Class B；`invites/transfer` 划转后 `wallet_balances` 的 `CHECK (balance >= 0)` 让冲正在数据库层不可能 | **采纳** | 成立。**落点：§3.5 第 4 步改为按比例追回、不论 `status`；15 天的作用降级为「减少已确认后才退款的笔数」。** 扣不动的部分记 `expense:refund` + 审计日志 + 管理员人工处理（几十人量级，管理员认识每个人），登记在 §10 代价 3。反方建议的「`confirm_at` 拉到归零日（年付 240 天）」**不采纳**：那会让邀请人等 8 个月才拿到佣金，返佣作为增长手段就废了 |
| **C12** | 边界 3b 的套利不存在，「上界 = 25% × 一笔年付额」是凭空的（年付 9.0 M 退成 9.0 M 余额只能买 9 个月，直接持有是 12 个月） | **采纳** | 算术成立。**落点：§4.3 边界 3b 保留规则、删掉理由与数字，换成正确的理由**（Class A 豁免的是恒等式本身，必须被配额）。一条用虚构套利支撑的规则，下次有人质疑它时没有辩护余地 |
| **C13** | OpenAPI 第 4 处不一致 `PlanPrice.period`（`:5747`，落在 `GET /plans` 不在 `POST /orders`）；且草稿写的 `:5906` 差一行 | **采纳** | 逐条复核属实：`grep` 只在 `:5747` 与 `:5907` 命中 `two_yearly`，`OrderStatus` 在 `:5834`，`Order.type` 在 `:5845`。**落点：§4.7 改为四处，并补上「`plans` 根本没有 `price_two_yearly` 列」这条更要紧的后果** |
| **C14** | `refunds` 没有 `user_id`，Class A「一生一次」纯靠应用代码不写错 | **采纳** | **落点：§6.1 给 `refunds` 加 `user_id NOT NULL` + `CREATE UNIQUE INDEX refunds_cooling_off_once ON refunds (user_id) WHERE rule='cooling_off'`。** 反方给的两个选项里选「加列」而不是「写进代价」，理由与本项目一贯取向一致：让非法状态在 schema 层不可表达 |
| **C15** | 事实基线 commit 过时（草稿写 `6b7415b04ab`，实际 `2c32b3f65d3`） | **采纳** | 已核对：反方提出时 HEAD = `2c32b3f65d340f55d4cc7f15ebb9a9a5b4f41ba0`，其后合入 PR #12/#13/#14。⚠️ **核准时（2026-08-23）master 又前进到 `618bf1cc89b`**，其间合入 PR #15（`0013_rate_limit` 表 + per-IP 限流 + `bp_node_alive` 心跳日志）、#16（镜像溯源 label + `check-cert-issuer.sh`）、#17（前端登录守卫 + 108 个前端测试）。本文引用的行号已按 `618bf1cc89b` 逐条复核，**唯一因此改变的结论是迁移号：0013 已被占用，本 ADR 的迁移改为 `0014`**。**落点：本文档头、§6** |
| **C16** | 替代方案：`covers_from`/`covers_to`，且 **`upgrade` 继承 `covers_from`** | **部分采纳** | 两列**采纳**（并追加 `prev_order_id`）。**但「`upgrade` 继承 `covers_from`」驳回** —— 继承会让 `D_total` 退回整个窗口长度，§4.2 验算表 D22 那一步的折抵从正确的 1600 变成 800，**凭空吞掉用户 800 分**，正是草稿自己警告过的错误。本裁决取 `upgrade.covers_from = paid_at`：对折抵与草稿逐字等价，对退款则由 `prev_order_id` 链上的分段求和承担计时，两个目标一次满足（§2.1 理由 2） |
| **C17** | 替代 Class A：改为「首单差额券」（已付金额转成 90 天有效的不可提现余额） | **驳回** | 它与 Class B 在**机制上完全相同**（钱 → 不可提现余额），只是把有效期从无限收到 90 天，却付出了「7 天无理由全退」这句话的全部关系价值。而 Class A 的增量代价在新公式下已经很小：豁免的是 `consumed_time + consumed_data`，以 7 天 / 10 GiB 闸门计上界 = `M × (7/30 + 10 GiB/配额)`，示意值（M=¥30、配额 100 GiB）约 **¥10/次**，一账号一生一次；出口现金上界 **$0.98/次**、50 人年度 **$49**。反方自己在其 §7 代价 5 也写了这条方案「若前 10 个用户里出现 1 例因此流失应当撤回」—— 与其设计一个自带撤回条件的方案，不如直接采纳更好的那个 |
| **C18** | 现在就加 `orders.pay_from_address` | **采纳** | 边际成本为零，且让 §9 的失效条件从「一句无人执行的话」变成一条 `GROUP BY` 就能出的数。即便本裁决已把「热钱包」理由降为脚注，这一列仍是 AML 筛查（pricing §7）所需 |
| **C19** | CI 加一个真的检查（`EXPLAIN` 全部写语句 + 回滚后写一次） | **采纳** | **落点：§6.4** |
| **C20** | `plans.kind DEFAULT 'cycle'` + `CreatePlan` 不写 `kind` → 后台建出的加油包被静默写成 `cycle`，被 `orders.type` 推导成周期套餐 | **采纳** | 已核对 `orders.sql:24` 确为 18 列且无 `kind`。**落点：§4.6 与 §6.1 —— `kind` 取 `NOT NULL` 无 `DEFAULT`，`CreatePlan` 同批改成 19 参** |

### 7.2 本文作者的一手复核（2026-08-23，`postgres:17` 容器）

反方的实测结论涉及本 ADR 的地基，按 AGENTS.md §3「一条从未被验证过的检查应默认视为不工作」，
裁决前重跑了四项。**四项全部复现**：

| 项 | 断言 | 复核结果 |
|---|---|---|
| C4 | 生成列不可写 | ✅ `ERROR: column "te" can only be updated to DEFAULT` / `DETAIL: Column "te" is a generated column.` |
| C6a | `STORED` 不加 `NOT NULL` → `attnotnull = f` | ✅ `pg_attribute.attnotnull = f` |
| C6b | `STORED NOT NULL` 语法被 PG 17 接受且 `attnotnull = t` | ✅ 接受，`attnotnull = t`，且 `SELECT` 出的值正确（100 + 50 = 150） |
| C7 | 删掉 plpgsql 触发器函数引用的列之后，该表任何 UPDATE 失败 | ✅ `ERROR: record "old" has no field "a"` / `CONTEXT: SQL expression "(OLD.a) IS DISTINCT FROM (NEW.a)" / PL/pgSQL function bump() line 3 at IF` |

**未复核项**：C5（`sqlc generate` exit 0）未在本机重跑 —— 但成因已从 `api/sqlc.yaml` 直接读出
（无 `database:` 段），且结论方向是「检查不存在」，按事实纪律应默认接受。**标：需实测**（跑一次 §6.4 的新 CI 作业即可）。

### 7.3 正方（`adr-0013-billing-rules.pro.md`）

| # | 正方论点 | 裁决 | 理由与落点 |
|---|---|---|---|
| **P1** | 把 §3.2 理由 1（交易所热钱包，0 样本）换成三条不需要样本的：(a) 收款架构里不存在「原路」、(b) 法发〔2021〕22 号第十一条「转换财物」、(c) Tether 反向污染 1,164 次 `DestroyedBlackFunds` | **部分采纳** | (b)(c) **采纳并上移**（§3.1 理由 1 与 3），(b) 的证据等级按正方意见从「待核实」升为「高」——待核实的是收款端而非退款端。**(a) 驳回**：它建立在「共享地址 + 金额尾数匹配」上，而 `0006_orders.up.sql:58` 的 `pay_address` 列注释写的是「**本单专属收款地址**」，同文件 `:86` 的索引注释写的是「EPUSDT 的金额尾数递增法」—— **同一份 DDL 里两种收款形态并存，这条架构事实还没定死**。拿一条自己都没定的事实当承重墙是本 ADR 不该犯的错。矛盾本身登记在 §11 |
| **P2** | ① 直接消解 pricing §6 代价 3 的后半句「退款准备金不能被当作可用现金」 | **采纳，但降级** | 事实成立（`0007_ledger.up.sql:10–12`）。**落点：§3.2 末尾的引用块。** 降级的理由见 C9：它是**现金流事实**，不能再充当「所以可以退得宽」的理由 |
| **P3** | 写死 `expense:refund` 只用于 `destination='original'`，`destination='balance'` 只有两条负债对冲 | **采纳** | **落点：§3.5 第 2 步的注释，并随迁移写进列/账户注释。** 补充一处正方也提到的例外：§3.5 第 4 步追不回来的佣金记 `expense:refund` —— 那记的是真实损失，与本条不冲突 |
| **P4** | payments §4.6 原文自相矛盾（乘号 vs 取较小值），且 §4.6 是**调研**不是裁决，「推翻」措辞不准确 | **采纳** | **落点：§4.1 改标题为「从未成形」，并两种读法一起打**（乘积读法在完全正常场景下也系统性低估，是「总是错」而非「有反例」） |
| **P5** | §4.6 还有第二处必须一并处理：「到期日不变，**流量配额立即提升到新档**」 | **采纳** | **落点：§4.1 末段 + §4.4。** 且从金额看这才是贵的那半句 |
| **P6** | 用恒等式 `Δ·D_left/C` 替掉「15 倍」算例 | **采纳** | **落点：§4.4 正文给恒等式，15 倍算例降为它的一个取值** |
| **P7** | 对照表证明本 ADR 在「退多少」维度上比 payments §4.7 更宽（7 天窗外 §4.7 是「不退」；流量闸门 5 GB → 10 GiB） | **采纳** | **落点：§3.2 的归零点表 + §10 代价 1。** 这是对「政策太抠、伤熟人关系」这一攻击的直接答复，且有文档对照可证 |
| **P8** | ③ 不是新设计，是三处独立登记的缺口的既定解法；B24/B25/B26 是 28 条开放项里唯一一组零外部依赖的 | **采纳** | 已核对 `users.sql:86–88` 的注释原文与 roadmap `:553–555`。**落点：§5.1 理由 2** |
| **P9** | 生成列优于触发器，决定性差别是 **D1 这条管理员绕过应用层的人工写路径** | **采纳** | 这是选生成列最硬的一条理由，草稿没写。**落点：§5.2** |
| **P10** | 加油包 12 个月结转同时是一个**不依赖邮件**的回流钩子（ESP 未接通） | **采纳** | **落点：§5.5 第 2 条的引用块** |
| **P11** | 明文退款政策是「接卡通道」这个期权的前置，成本为零 | **采纳** | **落点：§3.6 末尾的引用块** |
| **P12** | 佣金三条补法：基数写死 `amount_paid`、按比例追回不论状态、`CHECK(inviter_id <> invitee_id)` | **全部采纳** | **落点：§3.5 与 §6.1。** 同时补上 payments §4.3 里「实付金额」一直没有定义的空白（roadmap B37 的一部分） |
| **P13** | `ceil(elapsed_days/30)` 在 `elapsed=0` 时为 0 → 修法 `greatest(1, ceil(...))` | **采纳问题，驳回修法** | 问题与 C1 同一条。**修法驳回**：按那个公式，§3.4 的升级路径仍能净得 ¥31.72，且它把「不满一个月按一个月收」这条不连续性留在了政策里。按比例分段求和同时解决两者 |
| **P14** | `plans.price_monthly` 可为 NULL（`0002:28` 只有 `CHECK (>= 0)`），会让退款公式算出 NULL | **采纳** | 已核对属实。**落点：§6.1 的 `plans_cycle_needs_monthly` CHECK。** 采纳正方建议的「加 CHECK」而非「应用层兜底」，理由同 §5.2 |
| **P15** | 加 `orders.price_monthly_at_order` 快照列 —— `plans.price_monthly` 是活列，涨价后退款额变小，用户会认为我们改价来少退钱 | **采纳** | 这是本 ADR 唯一一条**防的是无法事后补救的争议**的列。**落点：§6.1，并写进列 COMMENT。** 正方自己给的撤回条件（若 P2 决定「价格一经发布不可修改，改价必须新建 `plans` 行并归档旧行」，此列变冗余）一并登记在 §10 代价 5 |

### 7.4 一份 ADR 里塞了三个决策，违反 `docs/README.md` §4 规矩 1 吗

**形式上是，实质上不是，保留一份。** 三条共用**同一次迁移 `0016`**，拆成三份 ADR 会让
「哪份 ADR 对应哪段 DDL」不可追溯，而那正是规矩 1 想保护的东西。
（顺带：0013 之后的 ADR 号 0014 / 0015 当日已分别被 SLO 与客户端策略占用，本来也拆不出连号。）
更重要的是，§1.1 的恒等式表明被裁决的**其实是一个决策**：
「用户的净支出如何定义」—— 退款、折抵、结转都是它的推论。H1 已按此改成单句结论。

---

## 8 · 抗套利自检（重做）

草稿的自检表是一张「我们想到的攻击」清单，其中**两格是假的**（「邀请自己」被 C11 推翻、「边界 3b」被 C12 推翻）。
本裁决不再依赖清单，而是依赖 §3.2 的零增量敞口定理。清单只作为**定理的抽样验证**：

| 攻击 | 净支出 | 被哪一条挡住 |
|---|---|---|
| 买年付享 75 折 → 用 1 个月 → 退款 | 30 天 × 月付标价 | `consumed_time` 按月付标价计（§3.2），年付折扣只有持有到第 270 天才兑现 |
| 年付第 241 天 → 先升级 → 同日退款 | 与不升级**逐分相同**（¥241.00） | `covers_from` + 窗口链分段求和（§3.4）。**这是 C2 的落点** |
| 下单当天立刻退款 | ≈ 0（他也没消费） | `consumed_time` 按比例，无 `ceil` 造成的 24 小时全额窗口（§3.2）。**这是 C1 的落点** |
| 满配额跑完再退款 | 一整个月的标价，`refund_B = 0` | `consumed_data`（§3.2）。他与不退款的用户处境完全相同 |
| 升级前把当期流量跑完，再按天数拿高折抵 | 折抵不退钱、不找零；当期新增配额按周期剩余天数折算 | §4.3 边界 2 + §4.4 |
| 买高档 → 降级拿差额余额 → 套现 | 不可达 | **禁止中途降级**（边界 1）+ 余额不可提现（`ledger_accounts` 无出金路径） |
| 链式升级把折抵反复放大 | 见 §4.2 验算表 | `V_source` 计入 `surplus_amount`，`upgrade.covers_from = paid_at`；`surplus_order_ids` + `prev_order_id` 双留痕 |
| 用优惠码把 gross 缩小再折抵 | 不可达 | 升级单不接受优惠码（边界 4） |
| 邀请自己 → 拿 10% 佣金 → 第 16 天退款 | 佣金按比例追回 | ~~冷静期 15 天 > 退款窗口 7 天~~ **（这条是假的，C11）** → §3.5 第 4 步的**比例追回，不论状态** + `commissions_no_self_invite` |
| 首单全退 + 长周期折扣组合 | 见 §4.3 边界 3b | Class A 限首单 + 一生一次，**由 `refunds_cooling_off_once` 唯一索引强制**（C14） |
| 月末买加油包 → 立刻退包 | 不可达 | 加油包不退（Class C / §5.5 第 4 条） |
| 充值余额 → 退款套现 | 不可达 | 充值不退（Class C）。这一条是合规红线不是政策选择 —— 退了就等于实现了提现 |
| 囤积加油包等涨价 | 12 个月归零 | `pack_expire_at`（§5.4） |
| 后台 D1 直接改配额后申请退款 | 不可退 | `source` 不存在 → `V_window = 0`（§2.2） |

---

## 9 · 失效条件（每一条都配了观测手段）

反方 C10 指出草稿的失效条件表「读起来很完整，但多条没有任何观测手段 —— 一条无法被观测到触发的失效条件，
不是失效条件，是措辞」。本表的每一行都写明**谁在什么时候会看到它被突破**。

| 裁决 | 失效条件 | 观测手段（本 ADR 一并要求建的） |
|---|---|---|
| ① 只退余额 | 若前 20 笔真实入账中「交易所热钱包」占比 < 30% | `SELECT count(*) FROM orders WHERE pay_from_address IN (…)` —— **靠 §6.1 新增的 `orders.pay_from_address` 列**。⚠️ 即便结果为低，也**不构成放宽例外范围的理由**（§3.1 的三条理由都不依赖它）；这条统计的真实用途是 AML 筛查策略（pricing §7） |
| ① Class A 的 7 天 / 10 GiB | 年度 Class A 出口成本 > **$100**（≈102 次 × 10 GiB × $0.0979），或 Class A 退款率 > 10% | `SELECT count(*), min(created_at) FROM refunds WHERE rule='cooling_off'` —— 走 `refunds_rule_idx`。**必须建成一条每月一次的 Cloud Scheduler → 结构化日志 → log-based metric**，否则这条阈值永远不会有人看到被突破 |
| ① 零增量敞口定理 | 若引入**不按流量计费的档位**（不限量套餐），`consumed_data` 的分母消失，定理不成立 | 定价评审时人工判定。`plans.transfer_enable CHECK (> 0)`（`0002:21`）当前保证分母恒 > 0 |
| ② 按剩余天数折抵 | 若引入**不限时套餐**（`reset_method='never'` / `price_onetime`）作为主力档，「剩余天数」失去分母 | §2.2 已裁定 P1 不售（`sellable=false`）。改售时本条整节需重写 |
| ② 禁止降级 | P2 阶段「买了高档用不完」工单 ≥ 3 例 | 工单 `category` 统计（与 page-inventory §7 代价 3「设备数工单 > 10%」同型） |
| ③ 加油包结转 | `SUM(users.transfer_enable_pack)` 折算出口成本 > **$100/月（绝对值）** | ⚠️ **阈值口径按 C10 改了**：草稿写的是「单月出口预算的 20%」，而预算 `$500/月 · EXCLUDE_ALL_CREDITS` 已被自用出口 **约 7,800 GiB/月 ≈ $764/月 gross**（evidence §4.1，08-17→08-20 速率外推）**1.53 倍突破**，拿一个已被突破的预算的 20% 当阈值没有含义。观测手段：一条每日的 `SELECT sum(transfer_enable_pack) FROM users` → 结构化日志 → 第 8 条 log-based metric（现有 7 条，见 §11） |
| 全部 | **GFS 抵扣 2027-06-15 到期**后现金口径突变 | 本 ADR 所有成本论证用 gross（$0.0979/GiB），所以到期本身不改结论 —— 但「Class A 每年 $49 可以接受」这类判断届时要按真实现金重新看一遍。抵扣池由同账户 Vertex AI 主导，2026-07 单月被烧掉 $25,925.83 |

---

## 10 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **「只退到不可提现的余额」在熟人语境下会被至少一个人当面质疑。**
>    几十人量级、邀请制，意味着提退款的人大概率是我们认识的人，
>    而「你只能退店内积分」这句话在朋友之间比在商业机场里更难说出口。
>    §3.1 的三条理由必须写进法务页**并且能口头复述** —— 三条里只有理由 3（污染）是能口头讲的
>    （「我们退给你的 U 可能是被污染的」），理由 1 是法律条文、理由 2 是手续费表，都更抽象。
>    **这条取舍在「有一个用户因为退款问题不再用了」时就要重新评估** —— 在几十人的池子里流失一个熟人，
>    成本高于任何一笔退款。
>    缓解：§3.2 的对照表可以直接给用户看 —— 在「能拿回多少」这个维度上，
>    本 ADR 比 payments §4.7 的调研建议**更宽**（7 天窗外 §4.7 是「不退」，本 ADR 是按比例退到第 270 天；
>    流量闸门 5 GB → 10 GiB）。
> 2. **Class A 事实上是一个 7 天 / 10 GiB 的免费试用，而 pricing §3.4 明确写着「不做免费试用」。**
>    量化后的代价有两笔：**出口现金上界 $0.98/次、50 人年度 $49**（≈ 5.1 个月的 `bp-db` 账单；
>    对照 2026-08-17→08-20 实测日均出口 256.2 GiB ≈ $25.1/天，**Class A 一整年的上界等于不到 2 天的出口账单**）；
>    以及**放弃的收入** = 被豁免的 `consumed_time + consumed_data`，上界 `M × (7/30 + 10 GiB ÷ 配额)`，
>    示意值（M=¥30、配额 100 GiB）约 **¥10/次**，一账号一生一次。
>    **这确实是对 §3.4 的一处放宽，需要显式接受或否决，不能装作没这回事。**
>    区别于真正的免费试用：**用户必须先全额付款**，拿回的是必须继续在我们这里消费的信用，现金从未离开账户。
> 3. **佣金按比例追回会在「已划转并花掉」时追不动。**
>    `wallet_balances` 有 `CHECK (balance >= 0)`（data-model §7.1），负余额存不下；
>    page-inventory §5.4 的 `/invite` 又有「划转到余额」（`POST /api/v1/invites/transfer`）。
>    本裁决的处理是「扣到 0 为止，余下记 `expense:refund` + 审计日志 + 管理员人工处理」。
>    **代价可量化**：单笔上界 = 10% × 订单额。**当月出现 ≥ 2 笔追不动的佣金时，
>    应当改为「佣金划转到余额前先冻结到 `refund_B` 归零之日」**，那会让返佣的体验明显变差。
> 4. **退款事务从 5 步变成 7 步，且新增一次跨表读（窗口链的 `WITH RECURSIVE`）。**
>    这是全系统最长的一条写事务。几十人量级下不会有锁竞争，
>    **但当月退款笔数超过 20 笔时，要拆成「退款 + 异步追回佣金」两段**，而那会引入一个中间态。
> 5. **`orders` 加了 5 列，与草稿「不新增任何表也不新增非必要列」的克制取向有张力。**
>    `covers_from` / `covers_to` / `prev_order_id` 是修 C1/C2 的必需品；
>    `price_monthly_at_order` 防的是「涨价后退款额变小」这类无法事后补救的争议；
>    `pay_from_address` 让 §9 的第一条失效条件可执行。
>    **但它开了「往 `orders` 加快照列」的口子** —— 下一个人可能接着加 `price_yearly_at_order`、`discount_rate_at_order`。
>    **撤回条件**：若 P2 决定「价格一经发布不可修改，改价必须新建 `plans` 行并归档旧行」，
>    `price_monthly_at_order` 立刻变成冗余，应当删掉。
> 6. **本 ADR 定的三条规则，有三个定时作业一个都还没建。**
>    `bp-*` Cloud Scheduler **0 条**、`bp-*` 告警策略 **0 条**（`bp-alerts` Pub/Sub topic 与
>    email 通道「ops alerts (wangharp)」已存在，log-based metrics 已建 7 条、仍缺 3 条）。
>    周期重置（每分钟）、加油包过期清理（每日）、佣金 15 天确认 —— 外加 §9 要求的两条统计作业。
>    **规则写完不等于规则生效**，这一条必须在排期里体现。
> 7. **生成列 `transfer_enable` 让 `users` 的每一次写都多算一次加法并重写行**，
>    并且**这张表以后不能再用 `INSERT … SELECT *` 之类的整表拷贝**（生成列不可写），备份/迁移脚本要显式列名。
>    `users` 的写频率已被 `user_traffic` 拆表压到接近零（data-model §4.1），所以性能影响可忽略。
>    **更实的代价是 §7.1 C5 揭示的那条**：生成列的「不可写」保护**没有任何自动化闸门**兜着 ——
>    `sqlc` 与 `go build` 都拦不住，只有 §6.4 新增的 CI 作业能抓，
>    而那个作业在本 ADR 落地之前**从未运行过**，按事实纪律应默认视为不工作，直到第一次跑绿。
> 8. **升级的两个比例（钱按剩余订阅天数、配额按剩余周期天数）会让至少一部分用户算不明白。**
>    §4.5 的展开说明能缓解不能消除。**代价可量化**：若 P2 阶段「折抵算错了吧」类工单 ≥ 3 例，
>    应退回一个更笨但更好解释的方案 —— 「升级 = 按新套餐重新起算周期，旧订单剩余按天折抵」，
>    代价是周期末升级的用户会白扔已付天数（正是 user-journey §10.2 反对的那种隐性惩罚）。
> 9. **所有金额都是相对量（「月付标价的倍数」「剩余天数比例」），因为绝对价格至今未定**（pricing §7）。
>    公式在价格定下来之后**不需要改**，但 §3.2 的「$0.98/次」「$49/年」、§4.4 的「$49 vs $3.3」、
>    §10 代价 2 的「¥10/次」都是用假设配额（10 GiB / 100 GiB / 500 GiB）算的比例演示，
>    **配额定稿后必须回填真实数字**。
> 10. **本 ADR 的全部出口成本论证共享同一个单一数据源** ——
>    `loopback-500616.billing_export` 的一个 54 天窗口（2026-06-28 → 08-20，3,399.0 GiB / $332.91），
>    而 evidence §5「不证明什么」第 1 条明说这是**运维自用流量**、标准导出没有 `resource` 字段、无法归到实例。
>    **一旦产品用户的流量结构与运维自用不同（几乎必然），$0.0979/GiB 就要重算**，
>    §9 与本节里所有以它为单位的数字随之重算。

## 11 · 这次没有解决的

- [ ] 🔴 **收款形态本身没定死。** `0006_orders.up.sql:58` 的 `pay_address` 列注释写「**本单专属收款地址**」，
      同文件 `:86` 的 `orders_pay_addr_amount_uk` 注释写「EPUSDT 的**金额尾数递增法**」（= 共享地址），
      pricing §4.2 第 5 条又给了「质押资金账户 + `DelegateResource`，到账即归集」。**三种形态并存。**
      不在本次范围内，因为它是支付网关选型（roadmap **B21**）的一部分，而网关未选型。
      **但它直接决定 §3.1 的例外条款能不能执行**：若最终是「一单一地址且不做即时归集」，
      「原路退回」在技术上存在，例外范围可以更宽。
- [ ] **`from` 地址的交易所占比无任何样本。** §6.1 已加 `orders.pay_from_address` 让统计可执行，
      但我们至今 **0 笔真实入账**。不在本次范围内，因为它需要真实收款，属实测而非设计。
      **前 20 笔订单必须做这个统计**（§9 第一行）。
- [ ] **`SUM(transfer_enable_pack)` 的敞口没有监控。** §9 要求它成为第 8 条 log-based metric，
      但现有 7 条指标是围绕节点与邮件建的，加这一条需要应用侧先有写路径（一个每日作业写一行结构化日志）。
      不在本次范围内，属监控设计；**但它是 §9 唯一一条目前无法观测的失效条件**。
- [ ] **`reset_pack`（`plans.price_reset`，重置当期 `u/d` 但不加配额）没有下单入口。**
      `order_type` 枚举里有它，`CreateOrderRequest` 里没有表达它的字段（`order_period` 里没有 `reset`）。
      不在本次范围内，因为它与 ③ 的裁决正交：加油包**加配额**、重置包**清计数**，
      两条路径的产品定位是否重叠本身需要先裁决（竞品把它们合成了一个「流量重置包 ¥23」）。
- [ ] **拒付（chargeback）路径完全没设计。** `order_status` 有 4 个 chargeback 态，本 ADR 一个都没用到。
      不在本次范围内，因为 USDT 零拒付，而卡通道（Paddle）在 pricing §4 被明确列为「不可放在关键路径」。
      **若真的接了卡通道，佣金冷静期必须从 15 天拉到 30 天以上**（payments §4.3），
      且 §3.5 的比例追回要覆盖 `chargeback_lost`。
- [ ] **`refunds.status` 的 `'pending'` / `'failed'` 在余额退款路径上永远用不到**（§3.5 是一个原子事务，直接写 `'done'`）。
      它们只在 `destination='original'` 时有意义，而那条路径需要一次**跨越事务边界的链上转账**，
      本 ADR 没有为它设计状态机。不在本次范围内：它属于「我方终止服务」这个低频例外分支。
- [ ] **不限时套餐（`price_onetime` / `reset_method='never'`）在 schema 层已经可售，但 P1 裁定不售。**
      `users.expired_at` 明写「NULL = 不限时套餐」，`order_period` 含 `onetime`。
      若将来改售，§2.2 / §4 / §9 的「剩余天数」整块不适用，需要改按剩余流量折抵。
      不在本次范围内，因为「是否卖不限时套餐」是 pricing §7 仍未决的价格决策。
- [ ] **退款申请的用户侧入口没有设计。** page-inventory 的路由里没有「申请退款」这一页，
      `api-contract` 用户面也没有对应端点 —— 只有管理面的 `POST /admin/orders/{trade_no}/refund`（D7）。
      **裁决：默认走工单**（几十人量级下够用，且比自助退款更适合熟人关系 —— 能先问一句为什么），
      但需要在 page-inventory 的工单 `category` 里加一个 `refund` 取值。这一条改动不在本 ADR 的迁移里。
      ⚠️ **「靠管理员审批慢」不能作为设计防线** —— §3.2 的公式在任何审批时延下都成立，这是刻意的。
- [ ] **「违规使用不退」里的「共享超限」没有阈值。** 它指向 `subscription_fetch_log` 的共享检测，
      而 data-model §16 已登记「阈值 20 是占位，**需实测**，必须先跑满第一批 20 个用户采基线」。
      在有基线之前，条款只能写「转售或明显超范围共享」，**不能写具体数字**，判定由管理员人工做并进审计日志。
- [ ] **年度 Class A 成本上界用「50 人」估算，而用户量级只有「几十人」这个模糊说法。**
      product-brief §4 只说「规模有上限」，没有数字。50 是本 ADR 拍的上界，**待核实** ——
      若实际上限是 100 人，§10 代价 2 的 $49 要翻倍到 $98。
      同一个数字也支配 §3.2 定理之外的一切规模判断。
- [ ] **没有测 `GENERATED … STORED` 在 `users` 有真实行数时的 `ALTER TABLE` 锁时长。**
      本次复核的 `users` 只有 1 行。「即便到 P3 的几十行，重写也是毫秒级」这个判断大概率对，
      但**未实测**。不在本次范围内：几十行的量级下不值得搭环境。
- [ ] **`openapi.yaml` 缺一道自动化闸门**：从 `migrations/*.up.sql` 的 `CREATE TYPE` 生成期望值，
      与 openapi 的 enum 对拍。§4.7 那四处不一致全部是人工比对出来的，
      下一次改枚举时同样的漂移会重演。不在本次范围内，属 CI 设计（与 §6.4 是同一类工作，可以一起做）。
- [ ] **`page-inventory.md` 后台「出口成本估算」写的是 `GCP Premium ≈ ¥1.65/GB`（≈$0.23/GiB）**，
      与实测的 $0.0979/GiB 差 2.3 倍。它与本 ADR 无关，但本 ADR 的每一条成本论证都会被后台这个数字当面否掉。
      不在本次范围内，属 page-inventory 的更新。
