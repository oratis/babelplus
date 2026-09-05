# 自用机队出网归因 · 2026-09-05

> 日期：2026-09-05 · 性质：**证据型核查** · 状态：**已完成（本次采集，12 h 窗口）**
> 事实基线：VPC Flow Logs（`default` 子网 us-west1 / asia-northeast1，2026-09-04 12:47Z 由
> `optimize-vpn.sh` p0 打开，15 min 聚合、采样 0.5）；聚合脚本 `oratis/Proxy_Skill` 的
> `ops/gcp/flow-report.py`（2026-09-05 05:28 交付，本次原样执行 `flow-report.py 12 0.5`）
> 证据口径：Flow Logs 按采样率折算 = 中（采样 0.5 会漏掉部分短连接）；
> 8 月阶跃的归因来自 Proxy_Skill `ops/README.md` 的转述 = **中（本次未复现）**
> 关联：[egress-billing-20260820 §4.1](../egress-billing-20260820/)（那次无解释的 4 倍阶跃）、
> [ADR 0017 §6](../../05-adr/0017-personal-fleet-in-repo.md)（日报是成本闸）、
> [personal-fleet-runbook §1.6](../../04-ops/personal-fleet-runbook.md)

---

## 1 · 这些证据证明什么、不证明什么

**证明**：
1. 出网的 97% 那一块**现在可以按客户端 × 目的 × 端口 × 小时拆开看**——此前唯一的观测是账单里的总 GiB。
2. 12 h 窗口（2026-09-04 21:14 → 09-05 08:5x CST）出网 **31.35 GiB**（`vpn-jp` 20.41 / `vpn-us` 10.94），
   折日均约 63 GiB，与 as-built-personal-fleet §4.4 的 75 GiB/日同量级。
3. 用量高度集中：**一个北京联通（AS4808）客户端占下行 9.99 GiB / 上行 6.86 GiB**，其余三个客户端合计不到 4 GiB。
4. 节点回源的 top 25 里 **Cloudflare（AS13335）占绝大多数**，其次是 Google（`35.190.46.x`，北卡）与 AWS（`3.163.158.x`）——
   即出网大头是经代理访问的 Cloudflare 前置站点，**Flow Logs 看不透 Cloudflare 共享 IP 背后是哪个域名**。

**不证明**：
1. **不证明域名。** 目的 IP 是 Cloudflare 边缘时，归因到域名要靠客户端侧记账
   （Proxy_Skill `ops/mihomo-sampler.py`，09-05 03:3x 起跑 24 h，产物不入库）。
2. **不证明 8 月 17–20 日那次阶跃的原因。** Proxy_Skill `ops/README.md`（2026-09-05）写「事后定位：`114.246.97.x` 那台
   Windows 经 JP-HY2 拉 Windows Update / Steam / R2」——本次**未复现**该结论，证据等级为中。
3. **不证明计费字节。** Flow Logs 的 `bytes_sent` 按采样率折算，与账单的 egress 字节不是同一口径；用它定方向，不用它对账。
4. **不证明 12 h 以外的分布。** 窗口里 09:00–20:00 CST 全部为零是因为 Flow Logs 从 21:14 才开始，不是没流量。

## 2 · 数字（原始输出见 [flow-report-12h.masked.txt](flow-report-12h.masked.txt)，客户端 IP 末段已打码）

| 项 | 值 |
|---|---|
| 窗口 | 12 h，88,803 条 flow 记录，采样率 0.5 已折算 |
| 出网合计 | **31.35 GiB**（`vpn-jp` 20.41 · `vpn-us` 10.94） |
| 峰值小时（CST） | 00:00 7.49 GiB · 03:00 5.82 · 22:00 5.71 |
| 最大客户端 | `111.199.185.x`（AS4808 北京）下行 9.99 GiB / 上行 6.86 GiB |
| 回源 top 3 | `172.64.146.x`（CF）1.85 · `104.18.53.x`（CF）1.76 · `35.190.46.x`（Google）1.64 GiB |

## 3 · 对计划的影响

- 日报的 C 组只报总量（runbook §3.2）；**归因这一步现在有了可重复的工具**，但它跑在本机、要 gcloud 身份，
  没有进 Worker。要日粒度自动归因，得把 Flow Logs 建一条 sink 到 BigQuery——**本次没做**（见 §4）。
- 客户端集中度这么高，意味着**砍流量的杠杆在客户端分流规则上**（Proxy_Skill `ops/gen-shadowrocket.py` 已按此加了
  Apple / Microsoft / 飞书等直连规则），不在节点侧。

## 4 · 这次没有解决的

- [ ] Flow Logs → BigQuery sink 未建；`flow-report.py` 每次从 Logging API 翻页，窗口一长就慢。
- [ ] 上行 6.86 GiB（客户端 → 节点）对一个代理客户端来说偏大，**是什么在上传没有查**。
- [ ] `mihomo-sampler.py` 的 24 h 域名记账产物在 Proxy_Skill `ops/traffic/`（gitignore），没有并入本仓证据。
- [ ] Flow Logs 本身的 Logging 用量与费用没有核过（50 GiB/项目/月免费额度之内与否）。

## 5 · 复现

```bash
cd /Users/oratis/Documents/Codex/VPN/Proxy_Skill
python3 ops/gcp/flow-report.py 12 0.5     # 只读；需 gcloud 已登录 wangharp@gmail.com
```
