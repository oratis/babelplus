# babel.plus · Agent 规则

本文件给在本仓库工作的 AI Agent 指路。**开始任何工作前先读完。**

---

## 1 · 这是什么项目

内部使用的流量中转服务（中国 → Cloudflare 边缘 → Google Cloud → 全球）。
当前处于 **P0 收尾 / P1 软件侧基本完成、基础设施侧为零**（2026-08-30 实数）：

- `api/`（Go）：**128 个 operation 实现 120 个**，剩 **8 个 501**
  （5 条缺表 `domains` / `mail_templates`，3 条契约自己声明未实现；
  清单钉在 `api/internal/handler/unimplemented_test.go`，**改它之前先读那一条为什么被拦住**）。
  **37 个 `_test.go` / 573 个顶层 `Test` 函数**，19 组迁移 / 47 张表 / 343 条 sqlc 查询。
- `web/`（双 SPA）：**623 个前端用例 / 48 个文件全绿**（`pnpm test`）。
  用户面板 **20 条业务路由全部接线**（`App.tsx` 共 22 条 `path=`，另两条是 `/` 重定向与
  `*` 的静态 `NotFoundPage`）；后台 23 页**接线 21 页**
  （不接的两页：`DomainsPage` 三个端点都是 501、`NotFoundPage` 是静态页）。
  `TODO(P1)` **22 处 / 16 个文件**。
- `infra/`：建机与部署脚本 **18 支 / 10,705 行，全部带 dry-run，一台节点都没建过**。

🔴 **写代码之前必须先知道的三件事，它们是本项目当前的真实状态**：

1. **自有节点 0 台。** `gcloud compute instances list --project=oratis-491316` 只返回
   既有的 `vpn-us` / `vpn-jp`。**P1 的八条出口标准全部要求一台在跑的机器，所以 P1 = 0/8。**
2. **生产跑的就是 master（2026-08-31 起）。** `bp-api` 的 serving revision 是
   `bp-api-87886e4`，`bp-db` 在迁移版本 19。用户面实测可用（注册 → 登录 → 下单 → 取消）。
   此前这一条长期写着「落后 14 个提交、实现数 18/128」，**那句话已经不成立**。
   ⚠️ 但**「已部署」不等于「可以卖」**：自有节点 0 台，买了套餐也没有节点可连（见第 1 条）。
3. **管理面在生产上整体关闭，而且必须如此。** 生产 `bp-api` 没有配 `BP_ADMIN_IAP_AUDIENCE`，
   按 fail-closed 设计 61 个 `/admin/*` 一律 403（**实测**：连伪造的
   `x-goog-iap-jwt-assertion` 头也是 403 —— 它验签名，不信头的存在）。
   更根本的是**管理面根本没有登录端点**（45 条 admin 路径里没有 login/session/me，
   `AuthenticateAdmin` 从不读 `Authorization`，它验 IAP 断言）——
   见 [roadmap B51](docs/00-overview/roadmap.md)。**不要试图用用户面的 `login` 端点去接管理面。**
   ⚠️ **内部面已经不在这一条里了**（2026-08-31 起）：`BP_INTERNAL_OIDC_AUDIENCE` 与
   `BP_INTERNAL_TASK_CALLERS` 已配上，9 条 `/internal/tasks/*` 由 8 条 Cloud Scheduler
   带 OIDC 令牌调用，**实测 200**；无凭据调它仍是 403。

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
