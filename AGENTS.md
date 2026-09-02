# babel.plus · Agent 规则

本文件给在本仓库工作的 AI Agent 指路。**开始任何工作前先读完。**

---

## 1 · 这是什么项目

内部使用的流量中转服务（中国 → Cloudflare 边缘 → Google Cloud → 全球）。
当前处于 **P1 数据面：第一台节点已端到端接通，出口标准 3.5/8**（2026-09-02 实数）：

- `api/`（Go）：**130 个 operation 实现 121 个**，剩 **9 个 501**（2026-09-02 实数：`operations.txt` 130 行，
  `unimplemented_test.go` 的表 9 条 —— 5 条缺表 `domains` / `mail_templates`，3 条契约自己声明未实现，
  **1 条是浏览器扩展的 `getUserProxyConfig`，挂在 E0 计量验证之后**；
  清单钉在 `api/internal/handler/unimplemented_test.go`，**改它之前先读那一条为什么被拦住**）。
  **40 个 `_test.go` / 600 个顶层 `Test` 函数**，19 组迁移 / 343 条 sqlc 查询。
- `web/`（双 SPA + 一个浏览器扩展）：**691 个前端用例 / 56 个文件全绿**（`pnpm test`；其中扩展 61 / 7；
  ⚠️ 本机 Node ≥ 25 会因内置 Web Storage 抢占全局而假红 19 个用例，
  加 `NODE_OPTIONS=--no-experimental-webstorage` 即可，CI 用 Node 24）。
  用户面板 20 条业务路由全部接线；后台 23 页接线 21 页
  （不接的两页：`DomainsPage` 三个端点都是 501、`NotFoundPage` 是静态页）。
  `TODO(P1)` **22 处 / 16 个文件**。
  🔴 `web/extension/`（MV3，Chrome / Edge）**能装、能登录、能显示配额，但一个字节都还转发不了**：
  它唯一的服务端端点 `GET /api/v1/user/proxy-config` 是 501，门是 HTTPS 入站的每用户计量能否进 UniProxy
  上报路径（roadmap B66，需真机实测）。见 [web/extension/README.md §6](web/extension/README.md)。
- `infra/`：建机与部署脚本全部带 dry-run，**第一台节点 `bp-node-hk1` 就是用它们建成并接通的**
  （证据 [node-bringup-20260901](docs/evidence/node-bringup-20260901/)）。

🔴 **写代码之前必须先知道的三件事，它们是本项目当前的真实状态**：

1. **自有节点 1 台：`bp-node-hk1`**（asia-east2-a，Standard，`35.215.158.52`，v2node **v0.4.3 钉死**，
   升到 v0.4.5 会让 mihomo / sing-box / 官方 xray 客户端全部连不上，roadmap B62）。
   **REALITY 与 Hysteria2 两条通路端到端可用**（2026-09-02 起；SS-2022 未启用）。
   P1 出口标准 **6/8**，剩下三条（72 h 观察窗 2026-09-05T07:05Z 到点、密钥两步轮换需在后台登录后做、
   路由验收判据重定）**都不需要再写代码**，见 [roadmap §4.3](docs/00-overview/roadmap.md)。
   🔴 v2node 对多节点是「一个错、全部不起」且退出码 0（roadmap B64）；节点上只剩一个用户时谁都踢不掉（B63，哨兵用户在位）。
   ⚠️ 单节点、单协议、单区域：任何一条出问题就是全线中断。
2. **生产跑的就是 master。** `bp-api` 的 serving revision 是 `bp-api-f76487f`（master 最近一次代码提交），
   `bp-db` 在迁移版本 19。用户面实测可用（注册 → 登录 → 下单 → 取消）。
   ⚠️ 但**「已部署」不等于「可以卖」**：真实收款 0 笔，「下单 → 付款 → 自动开通」一次都没真跑过
   （运维账号的套餐是直接 SQL 开的），支付按**未批准**的 ADR 0012/0013 实现完了但不许开。
3. **管理面走 IAP，不走用户面的 login。** `admin.babel.plus` 经 GCLB + IAP 实测可登录
   （2026-08-31 起）。`AuthenticateAdmin` 验的是 `x-goog-iap-jwt-assertion` 的签名，
   45 条 admin 路径里**没有** login/session/me —— **不要试图用用户面的 `login` 端点去接管理面。**
   内部面：`BP_INTERNAL_OIDC_AUDIENCE` 与 `BP_INTERNAL_TASK_CALLERS` 已配，
   9 条 `/internal/tasks/*` 由 8 条 Cloud Scheduler 带 OIDC 令牌调用，**实测 200**；无凭据仍是 403。

**「仓库中只有文档」这句话到 2026-08-21 为止已经不成立**，
阶段判定见 [`docs/00-overview/launch-readiness-review-20260830.md`](docs/00-overview/launch-readiness-review-20260830.md)
（更早的时点快照：[`launch-readiness-review-20260821.md`](docs/00-overview/launch-readiness-review-20260821.md)，
As-Built，不回改）。

先读 [`docs/00-overview/product-brief.md`](docs/00-overview/product-brief.md)（做什么、不做什么），
再读 [`docs/README.md`](docs/README.md)（文档体系约定）。

---

## 2 · 写文档前必读

[`docs/README.md`](docs/README.md) 定义了强制约定，其中最容易被忽略的三条：

1. **不用 YAML frontmatter。** 元信息写在 H1 之后的 `>` 引用块里，
   字段为 `日期 / 性质 / 状态 / 关联`，`性质` 取受控词表，`状态` 必须带日期。
2. **每份裁决与计划强制两个尾节：`代价` 与 `这次没有解决的`。** 不允许省略。
3. **始终区分「设计目标 / 当前实现 / 测试结果」。**
   `02-architecture/as-built-*.md` 只写**已经存在**的东西。

---

## 3 · 事实纪律（本项目最重要的规则）

这个领域充斥着过时的社区传闻，**照抄传闻会导致真实的架构错误**。已经踩到的例子：

> 社区普遍引用的 Cloudflare ToS「第 2.8 条禁止代理非 HTML 内容」
> **在 2023-05-16 就已被删除**，真正生效的是完全不同的另一条款。
> 按传闻做架构决策会同时选错防御方向和风险评估。

因此：

- **凡结论必带出处与日期。** 官方文档 > 一手实测 > 源码 > 社区共识。
- **区分证据等级**：一手实测/官方页面=高；多源交叉=中；单一二手源=**待核实**。
- **不确定就写「待核实」或「需实测」，不要编。** 编一个数字比留空危害大得多。
- 中国网络可达性类的断言，除非有可信来源，一律标 **需实测**。

---

## 4 · 操作红线

| 禁止 | 原因 |
|---|---|
| 修改/删除任何 `vpn-*` 命名的 GCP 资源 | 是现役代理节点，见 [as-built-gcp.md](docs/02-architecture/as-built-gcp.md) |
| 修改现有防火墙规则 | 会影响 `vpn-us`/`vpn-jp` 与其他工作负载 |
| 触碰 `anthropic-relay` / `lisa-cloud` / `lisa-web` 三个 Cloud Run 服务 | 与本项目无关的现有服务 |
| 复用 Compute 默认服务账号 | 权限过大且被现有工作负载共用 |
| 把凭据写进仓库 | 一律走 Secret Manager 或 `.env`（已 gitignore） |
| 在竞品站点执行下单/支付/重置等副作用操作 | 调研只做只读走查 |

新建 GCP 资源一律 **`bp-` 前缀** + **`bp-node` 网络标签**。

---

## 5 · 常用命令

清点 GCP 现状（变更前后各跑一次做 diff）：

```bash
P=oratis-491316
gcloud compute instances list --project=$P
gcloud compute firewall-rules list --project=$P
gcloud run services list --project=$P
```

---

## 6 · 中文

文档正文用简体中文，**路径、字段名、命令、错误信息、协议名一律保持英文原样，不翻译。**
提交信息用中文。
