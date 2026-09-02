# 03-product · 产品

套餐、用户旅程、页面清单、教程规格。**不放实现细节。**

| 文档 | 性质 | 状态 |
|---|---|---|
| [pricing-and-plans.md](pricing-and-plans.md) | 设计方案 | **设计稿 v1**（2026-08-30）— 三档 **¥72 / ¥159 / ¥358**（30/100/250 GiB，2/5/10 设备）已定案，推导见其 §3.5。**不给「设计冻结稿」**：`nettier-ab-*` 实测未做（launch-readiness-review-20260821 §4 的第二个定稿前置）|
| [pricing-and-plans-revision-20260823.md](pricing-and-plans-revision-20260823.md) | 设计方案 | **已归档**（2026-08-30）—— 定价定稿的修订说明（含推导附录与 22 条辩论裁决）。**§3 的指令已于 2026-08-30 全部执行并并入上面那份活文档**，自此只作裁决谱系保留。⚠️ 本行 2026-08-29 补登记（文件随 PR #18 进 master 时漏登本表）；原状态写「设计方案」，那是 [docs/README §2.1](../README.md) 的**性质**词、不在 §2.2 的**状态**受控词表里，2026-08-30 一并改正 |
| [tutorials-spec.md](tutorials-spec.md) | 设计方案 | 设计稿 v1（2026-08-16） |
| [user-journey.md](user-journey.md) | 设计方案 | 设计稿 v1（2026-08-16） |
| [page-inventory.md](page-inventory.md) | 设计方案 | 设计稿 v1（2026-08-16） |
| [go-to-market-plan.md](go-to-market-plan.md) | 设计方案 | **提案，未批准**（2026-09-02 第二版）—— 前提改为**海外销售 / 非中国公民 / 不做身份验证**：闲鱼作废；SKU 改美元按行程（$2.50 / $4.50 / $8.90 / $18.90），¥3/GB 在两个主力档上成立；🔴 eSIM 已吃掉手机场景，可赢的是笔电 + 酒店 WiFi；支付翻转为卡 / MoR（触发 ADR 0012 失效条件 5），**拒付触发线是「一月 5 笔」不是 1.5%**；渠道第一位是 App Store 不是 SEO |
| [client-products-spec.md](client-products-spec.md) | 设计方案 | **提案，未批准**（2026-09-02）—— 扩展与浏览器的完整产品形态与执行方案：扩展用 PAC 候选串白拿域名池故障转移、popup 八个状态；浏览器只做「零配置」与「按站点可见」。🔴 §7 已重写：**iOS 提到第一位**（非中国区 Apple ID 在华可下载更新），前置是 Apple 组织账号 + D-U-N-S。配套 mockup：[client-products-mockup.html](client-products-mockup.html) **2026-09-02 §6.4 实施状态**：扩展代码已落地（[web/extension/](../../web/extension/)，61 个用例），服务端端点 501，门是 roadmap B66 |
