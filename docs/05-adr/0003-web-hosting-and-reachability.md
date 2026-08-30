# 0003 · 裁决：控制面托管按实测可达性选型，且必须用自有域名 + 镜像

> 日期：2026-08-16 · 性质：**架构裁决** · 状态：**设计稿 v1，待实施**（2026-08-16）
> 事实基线：OONI aggregation API 实测（`probe_cc=CN`，2025-08-16 → 2026-08-16）+ 各厂商官方文档
> 证据口径：OONI 大样本聚合 = 高；GreatFire 单次测试 = 低；厂商营销页 = 不采信
> 关联：[0001 CF ToS 裁决](0001-cloudflare-tos-risk.md)、[0002 通知通道裁决](0002-notification-channels.md)、
> [tutorials-spec.md](../03-product/tutorials-spec.md) §6、
> [0011](0011-domain-blackout-detection.md)（承接本文 §7 最后一条所说的「需合并处理」，**提案，未批准**；落点见 §7）
> ⚠️ 本裁决**修正**了 [system-design.md](../02-architecture/system-design.md) §2 中
> 「Web/教程站放 Cloudflare」的初始假设，理由见 §3.2。

---

## 1 · 裁决

1. **教程站与用户面板托管在实测可达性最好的平台上，当前数据指向 GitHub Pages / Netlify，
   而不是 Cloudflare Pages 或 Vercel。**
2. **一律使用自有域名，绝不使用平台子域名**（`*.pages.dev` / `*.vercel.app` / `*.github.io`）。
3. **搜索用本地索引（Pagefind / MiniSearch），不用 Algolia DocSearch。**
4. **放弃 ICP 备案与中国境内托管这条路线。**
5. **按「域名一定会被封」来设计**：多域名镜像 + 域名池 + [ADR 0002](0002-notification-channels.md) 的邮件广播。

---

## 2 · 实测数据

OONI aggregation API，`probe_cc=CN`，近 12 个月，`web_connectivity` 测试。

### 2.1 先建立基线（否则数字无法解读）

| 对照域名 | 测量数 | 异常率 |
|---|---|---|
| `www.qq.com` | 1,531 | 2% |
| `www.baidu.com` | 814 | 6% |
| `example.com` | 11 | 9% |
| `github.com` | 2,973 | **37%** |
| `twitter.com` | 17,143 | 88% |
| `www.google.com` | 902 | 95% |

**判读标准：~2–9% = 正常；~85–95% = 硬封锁。**

### 2.2 静态托管平台

| 平台 | 测量数 | 异常率 | 判定 |
|---|---|---|---|
| **GitHub Pages** (`*.github.io`) | 102,163 | **8.9%** | ✅ **等同基线，平台层面未被封** |
| **Netlify** (`*.netlify.app`) | 11,554 | **25.8%** | ⚠️ 可用但明显劣化（跨境拥塞，非审查） |
| **Cloudflare Pages** (`*.pages.dev`) | 65,634 | **85.4%** | ❌ 落在硬封锁区间 |
| **Vercel** (`*.vercel.app`) | 43,028 | **99.1%** | ❌ **比 google.com 还高** |

细分证据：

- **GitHub Pages**：1,435 个有效主机名中，**1,402 个基本正常（<20% 异常），仅 4 个被封**。
  被封的那 4 个是**因内容被封**，不是因平台 —— 见 §2.3。
- **Vercel**：675 个有效主机名中，**675 个全部 >80% 异常，零例外**。
  这个结果没有任何「样本偏差」的解释空间。GreatFire 记录其**自 2021-10-16 起被封**。
- **Cloudflare / Vercel 的封锁机制是按主机名（SNI）**，不是按 IP ——
  `pages.dev` 与 `workers.dev` 都呈现双峰分布（大部分被封、少数完全正常），
  且 Cloudflare 的 anycast IP 与其整个 CDN 共用。

### 2.3 决定性发现：**内容类别的风险远大于平台选择**

我们要做的是**中转服务的文档站**。同一数据集里，这一类内容的异常率：

| 域名 | 测量数 | 异常率 | 托管 |
|---|---|---|---|
| `www.v2ray.com` | 924 | **91%** | 自有域名 |
| `terminus2049.github.io` | 1,056 | **86%** | **GitHub Pages** |
| `www.expressvpn.com` | 1,045 | **86%** | 自有域名 |
| `bridges.torproject.org` | 1,296 | **85%** | 自有域名 |
| `guide.v2fly.org` | 1,102 | **61%** | 自有域名 |
| `toutyrater.github.io`（中文 V2Ray 配置教程） | 1,011 | **61%** | **GitHub Pages** |
| `getoutline.org` | 1,140 | 61% | 自有域名 |
| `nordvpn.com` / `protonvpn.com` / `openvpn.net` | ~1,070 各 | 58% | 自有域名 |
| — *GitHub Pages 平台基线* | *102,163* | *8.9%* | — |

> `toutyrater.github.io` 是托管在 GitHub Pages 上的中文 V2Ray 配置教程，
> **异常率 61%，而平台基线是 9%**。
> **平台没问题，被针对的是内容。**

**这条改变了整个问题的性质**：我们不是在选「哪个平台不会被封」，
而是在选「哪个平台不会**额外**给我们添乱」，然后**假定我们的域名迟早会被封**。

---

## 3 · 逐项裁决理由

### 3.1 为什么不用 Vercel

99.1% 异常率、675/675 主机名全部被封、GreatFire 记录自 2021-10 起封锁。
社区流传的 `cname-china.vercel-dns.com` 自定义域名绕过法，
**在 Vercel 官方文档中不存在**（其员工在相关 issue 中链接的文章讨论的是尼日利亚、
迪拜、秘鲁、缅甸、捷克的 ISP 封锁，通篇未提中国）。**不能把项目押在一个无文档的偏方上。**

### 3.2 为什么不用 Cloudflare Pages（修正先前假设）

**Cloudflare 官方 FAQ 明文写着 Pages 在中国大陆不可用**：

> "Pages is not available in Mainland China due to pages.dev certificate not residing
> within Mainland China."
> — [developers.cloudflare.com/china-network/faq](https://developers.cloudflare.com/china-network/faq/)

其 China Network 的[可用产品清单](https://developers.cloudflare.com/china-network/reference/available-products/)中
**没有 Pages**，同时不可用的还有 **Turnstile**（若打算用它做人机验证，需另选方案）。

加上 85.4% 的实测异常率，Pages 出局。

> 注意：这**不影响** [ADR 0001](0001-cloudflare-tos-risk.md) 的结论。
> Cloudflare 的 DNS 与全球网络（自定义域名）仍然可用且合规，
> 只是**不能指望 Pages 这个具体产品**，也不能指望在中国境内的加速。

#### ⚠️ 一处未消解的调研分歧

[admin-support-docs.md](../01-research/admin-support-docs.md) 的结论推荐
**Astro Starlight + Pagefind 部署在 Cloudflare Pages + 自有域名**，与本节相反。
两边的论据都成立，分歧点在于**自有域名能否规避 `*.pages.dev` 的封锁**：

| 立场 | 论据 |
|---|---|
| 可以用 Pages | 85.4% 的异常率是针对 `*.pages.dev` 主机名的；封锁机制是 SNI 级的，自有域名是不同的 SNI；且 OONI 对 `pages.dev` 的测试列表被钓鱼域名严重污染 |
| 不该用 Pages | Cloudflare 官方 FAQ 明文写 Pages 在中国大陆不可用；`pages.dev` 证书不在中国境内这一理由**与用哪个域名无关** |

#### ✅ 分歧已消解（2026-08-16 补充）：封锁是 SNI 级的，自有域名可用

后续调研取到了 OONI **原始测量**，直接回答了这个问题。

**证据一：Cloudflare 的 IP 段完全可达。**
测量 `20260815212321.839713_CN_webconnectivity_8fdfbd7c4910ae58`（**AS4837 联通，2026-08-15**）
对 `cdnjs.cloudflare.com`：`blocking=False, accessible=True, dns_consistency=consistent`，
TCP 443 对 `104.17.25.14` 与 `104.17.24.14` 均成功，TLS `failure=None`。
测量 `20260814211927.294040_...`（**AS4134 电信，2026-08-14**）：TCP 与 TLS 在 **IPv4 与 IPv6 上均成功**。
→ **`104.16.0.0/13` 与 `2606:4700::/32` 从电信与联通均完全可达。**

**证据二：`workers.dev` 是按 SNI 封的，不是按 IP 封的。**
测量 `20260807061238.146747_...`（**AS9808 移动，2026-08-07**）：

```
dns_consistency = consistent            ← DNS 未被污染
DNS A -> 104.18.12.15, 104.18.13.15     ← 返回真实 Cloudflare IP
TCP 104.18.12.15:443 -> success: True   ← TCP 握手成功
TLS ... server_name=workers.dev -> failure = connection_reset
blocking = http-failure   accessible = False
```

教科书式的 GFW SNI 触发 RST 注入。**IP 没问题，被封的是名字。**

**证据三：聚合数据同样支持这个结论。**

| 目标 | 异常 | 正常 | 判定 |
|---|---|---|---|
| `workers.dev` | 201 | 3 | 被封（~98.5%）|
| `pages.dev` | 65 | 5 | 被封（~93%）|
| `www.cloudflare.com` | **0** | 6 | **未被封** |
| `cdnjs.cloudflare.com` | 67 | **1095** | **未被封** |

**结论：admin 调研的推荐是对的，本 ADR §2.2 的悲观判断需要限定范围。**

- 85.4% / 93% 的异常率**只适用于 `*.pages.dev` 这个主机名**，不适用于其 IP。
- **自有域名 + Cloudflare Pages 的组合是可用的** —— 正是因为 IP 可达而只有名字被封，
  §3.5「必须用自有域名」这一条从「稳妥做法」升级为**该架构成立的前提**。
- Cloudflare 官方 FAQ 说 Pages 在大陆不可用，指的是**无法接入 China Network 获得境内加速**，
  不等于「从大陆访问不到」。这两件事此前被我混为一谈了。

> 同一份数据还印证了 §3.4 与 Turnstile 的判断，并给出一条新的硬性要求：
> **`one.one.one.one` 115/115 全异常、`cloudflare-dns.com` 41,969/41,969 dnscheck 失败** ——
> Cloudflare 的 DoH 从大陆完全不可用。若前端或客户端依赖它做 DNS，会静默失效。
>
> **修订后的处置：教程站与面板可以用 Cloudflare Pages，但必须自有域名、
> 必须钉 Let's Encrypt 证书（见 [ADR 0004](0004-transport-hardening.md) §3.4）、
> 且不得依赖 Cloudflare DoH 与 Turnstile。**
> 平台间的**性能**差异（而非可达性差异）仍需实测，保留在 §7。

> 顺带记录 admin 调研中与本 ADR 一致的部分：**Pagefind 的中文分词已确认可用**，
> 且索引作为静态文件随页面一起分发 —— **搜索的可达性等同于页面的可达性**，
> 这正是 §3.4 选择本地索引的理由。

### 3.3 为什么放弃 ICP 备案路线

- ICP **备案** 仅限「informational purposes only」；**许可证**需要中国境内公司主体
  （合资或本地公司），外国个人仅在「在华有固定住所」时可申请个人网站。
- AWS 中国区**开户就要求中国营业执照**；Cloudflare China Network 要求
  **JD Cloud 对每个域名做内容审核**，且 ICP 被吊销即可「随时无责任暂停」。
- 备案办理周期 **4–8 周**，需在页脚展示备案号。
- 关键：ICP 的适用范围包括「通过 CDN 向中国访客交付」，**不能用 CDN 绕开备案**。

> 结合 §2.3 —— 中转服务文档几乎不可能通过 JD Cloud 的内容审核。
> **这条路不是「难走」，是「走不通」。标注：机制有官方文档支撑，
> 但「中转类文档必被拒」这一点我们没有直接来源，属推断。**

### 3.4 为什么不用 Algolia DocSearch

Algolia 官方定价页明示：**Free / Grow / Grow Plus 三档的托管位置只有
「US, UK, EU West」**，只有企业版 Elevate 才是「Global」（且最近也只到香港）。

即：中国大陆用户在搜索框里每敲一个字，都要跨太平洋往返一次。
**这不是「可能慢」，这是架构上的最差情况。**

> 需要澄清的是：**没有任何证据表明 Algolia 被封** ——
> GreatFire 与 OONI 对 `algolia.net` / `algolia.com` **均无任何测量数据**。
> 问题不是「被封」，是「太远」。而这两者的失效方式不同：
> 被封会快速失败，太远会一直挂着。**对用户体验而言后者更糟。**

因此用**本地索引**（索引随静态资源一起下发，不产生第二次跨境往返）。
Chinese 分词需注意：MiniSearch 支持 `Intl.Segmenter`，Pagefind 有 CJK 支持，
但**两者的中文分词质量均未实测**。

### 3.5 为什么必须用自有域名

数据里的封锁机制自始至终是**按主机名（SNI）**的。
自有域名是我们**能控制、能轮换**的主机名；`foo.github.io` 不是。
一旦平台子域名被整体处置，我们没有任何补救手段。

---

## 4 · 跨境性能的客观下限

即使不被封，跨境交付本身就有硬上限。这一点有同行评审文献支撑：

> Zhu, Man, Wang, Qian, Ensafi, Halderman, Duan,
> **"Characterizing Transnational Internet Performance and the Great Bottleneck of China"**,
> Proc. ACM Meas. Anal. Comput. Syst. (SIGMETRICS) 2020, DOI [10.1145/3379479](https://doi.org/10.1145/3379479)

其摘要中的关键结论：中国大陆是显著异常值，瓶颈**影响 79% 的收发对**，
**超过 70% 的收发对每天有 5 小时以上速度低于 1 Mbps**，
且瓶颈位置「deep inside China」。

两个直接推论：

1. **问题是昼夜性的** —— 白天随手测「我这边能开啊」毫无意义，必须测晚高峰。
2. **瓶颈在中国境内** —— 换更近的境外区域（HK/SG/JP/TW）**解决不了**这个问题。

---

## 5 · 落地要求

- [ ] 教程站与面板各准备 **≥2 个自有域名**，互为镜像，内容自动同步。
- [ ] 页面底部固定展示**备用域名列表**（用户遇到问题时第一眼能看到）。
- [ ] 搜索索引本地化，构建期生成，随静态资源分发。
- [ ] 字体、图标等一切外部资源**自托管**，消除第三方依赖
      （理由是消除不可控依赖，**不是**因为 Google Fonts 被封 —— 见 [ADR 0002](0002-notification-channels.md) §3.3）。
- [ ] 不使用 Cloudflare Turnstile（中国大陆不可用），人机验证另选方案。
- [ ] 部署流水线支持**一键新增镜像域名**（域名被封时的恢复速度取决于此）。

---

## 6 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **多域名镜像 = 成倍的域名成本、证书管理、内容同步与监控复杂度**，
>    且每个镜像域名都可能被单独封锁，需要逐个监控。
> 2. **放弃 ICP 意味着永远无法获得中国境内加速。**
>    §4 的文献说明这是一个**每天 5 小时以上、低于 1 Mbps** 的客观劣化，
>    我们只能缓解（减小页面体积、本地索引、激进缓存），不能消除。
> 3. **本裁决高度依赖 OONI 数据，而 OONI 自 2023-07-07 起在中国被封锁**，
>    探针数量少且自选择（部分可能挂在 VPN 后，会**低估**异常率），
>    测试列表偏向审查敏感与钓鱼域名（会**高估** `pages.dev` / `vercel.app` 的异常率）。
>    **两个偏差方向相反，净效应未知。这是本裁决最大的不确定性。**
> 4. **选 GitHub Pages 引入了新的依赖风险** —— GitHub 自身在中国的异常率是 **37%**
>    （高于 `github.io` 的 8.9%），且历史上有 2013 年 DNS 劫持、2015 年「大炮」DDoS 的先例。
>    平台层面它今天很好，但它**不是一个中立的、无政治风险的选择**。

## 7 · 这次没有解决的

- [ ] **未做我们自己的实测。** 上述全部结论来自第三方测量平台。
      必须租用中国电信/联通/移动的短期 VPS（或用商业中国监测服务），
      对候选域名做**连续一周、覆盖晚高峰**的实测。§4 的文献说明单次白天测试会误导。
- [ ] HK / SG / JP / TW 从三大运营商的 p50/p95 延迟无可信数据，**需实测**。
- [ ] MiniSearch 与 Pagefind 在我们实际中文内容上的分词质量**未实测**。
- [ ] Netlify 25.8% 的异常率成因未确认（跨境拥塞 vs 部分主机名被封）。
- [ ] 用户面板（动态 SPA + API 调用）与教程站（纯静态）是否应选同一平台，未裁决。
- [ ] 人机验证方案未定（Turnstile 出局后的替代：hCaptcha？自建？邀请制是否已足够？）。
- [ ] 未评估「域名被封」的自动检测与镜像切换机制（与 [system-design.md](../02-architecture/system-design.md) §9 重复，需合并处理）。
      > **2026-08-29 补登落点：本条要求的「合并处理」已经有人做了 —— [ADR 0011](0011-domain-blackout-detection.md)。**
      > 检测见 0011 §3，镜像切换见 §5.2（按「可逆性 × 分池」划线）与 §8（客户端 fallback 的三处必改）；
      > 域名池规模与布局由同批 [ADR 0010](0010-domain-strategy.md) 裁决，0011 §6 沿用不重复。
      > ⚠️ 本文 §3.2 的修订块（「教程站与面板可以用 CF Pages，但必须钉 LE」）另由 0010 §11 判定为
      > 「条件在 CF Pages 上不可满足」，**不推翻**。
      > 🔴 **本条不划掉**：0011 与 0010 的状态都是**提案，未批准**（2026-08-23）。
