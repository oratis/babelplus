# evidence · 证据平面

实测原始数据：测速输出、抓包、探活 JSON、截图、SHA256SUMS。
**与正文文档分离** —— 结论写进正文，原始数据留在这里。

## 约定

- 目录名：`<topic>-YYYYMMDD`，例 `hy2-throughput-20260816/`
- 每个证据目录**必须有 `README.md`**，写清楚：
  - 采集条件（时间、网络、运营商、工具版本）
  - **这些证据证明什么、不证明什么** ← 最重要的一节
- 二进制/大文件附 `SHA256SUMS.txt`
- **失败样本要保留**，不要只留成功的

## 当前待采集（全部为 P0 阻塞项）

- [x] ~~`egress-cost-*` — GCP 出口单价~~ **✅ 已完成** → [gcp-egress-pricing-20260817](gcp-egress-pricing-20260817/)
      （单价已定；**实际账单核对**仍待有真实用量后做）
- [ ] `protocol-throughput-*` — REALITY vs Hysteria2，电信/联通/移动 × 晚高峰
- [ ] `region-ab-*` — asia-east1 vs asia-northeast1
- [ ] `nettier-ab-*` — Standard vs Premium 网络层级
- [ ] `email-deliverability-*` — QQ/163/126/Sina 送达率
- [ ] `domain-reachability-*` — 候选托管平台与域名的三网可达性（连续一周，覆盖晚高峰）
