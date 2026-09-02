# 01-research · 调研

外部事实的调查结果。**只放调研，不放我们自己的决策**（决策去 `05-adr/`）。

所有调研必须标注证据口径：一手实测/官方文档 = 高；多源交叉 = 中；单一二手源 = **待核实**。

| 文档 | 性质 | 状态 | 一句话结论 |
|---|---|---|---|
| [competitor-conyss.md](competitor-conyss.md) | 调研（一手走查） | 已完成 | 竞品是 v2board/Xboard 系；节点名即说明书、余额不可提现、长周期零折扣是可攻击点 |
| [reference-repos.md](reference-repos.md) | 调研 | 已完成 | Proxy_Skill 已在同一 GCP 项目部署 2 节点并实测出「单流 TCP 是真瓶颈」；Diogenes 文档体系可吸收 |
| [protocol-and-infra.md](protocol-and-infra.md) | 调研 | 已完成 | REALITY 主 / Hysteria2 副；Cloud Run 不能做数据面；Standard 网络层级是最大成本杠杆 |
| [panels-and-market.md](panels-and-market.md) | 调研 | 已完成 | 不 fork，自研 + 照抄 Xboard 数据模型与 UniProxy 契约 |
| [payments.md](payments.md) | 调研 | 已完成 | USDT 自托管为主、易支付为辅、Paddle 机会主义 |
| [admin-support-docs.md](admin-support-docs.md) | 调研 | 已完成 | 后台/工单/通知/文档站选型 |
| [acquisition-channels.md](acquisition-channels.md) | 调研 | 已完成（2026-09-02） | 市场按 GB 卖 ¥0.1–0.5、我们成本 ¥0.87；获客靠 TG + GitHub 推荐仓库 + aff 15–20%；闲鱼有判例且实名暴露；扩展只能设 HTTP(S) 代理、Edge Add-ons 大陆可达；Chromium fork 不可行。**§9 海外市场（同日追加）**：eSIM 已吃掉手机场景、iOS 是唯一在华可达的分发通道、支付走卡 / MoR、拒付触发线是「一月 5 笔」 |
