# babel.plus · Agent 规则

本文件给在本仓库工作的 AI Agent 指路。**开始任何工作前先读完。**

---

## 1 · 这是什么项目

内部使用的流量中转服务（中国 → Cloudflare 边缘 → Google Cloud → 全球）。
当前处于 **P0 调研与设计阶段，仓库中只有文档，没有实现代码。**

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
