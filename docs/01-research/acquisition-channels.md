# 获客渠道与竞品调研：市场按 GB 卖 ¥0.1–0.5，我们的出口成本就要 ¥0.87；能打的不是价格，是「AI 可用 + 不跑路 + 有售后」，而闲鱼是四条渠道里法律暴露面最大的一条

> 日期：2026-09-02 · 性质：**调研** · 状态：已完成（2026-09-02）
> 事实基线：2026-09-02 两轮网络调研（合计约 80 次搜索与页面抓取，含 Chrome Web Store 页面直接读取）；
> 仓库侧成本与定价事实引自 [pricing-and-plans.md](../03-product/pricing-and-plans.md) §3.5
> （`c = $0.121/GiB`、`f_cap = $3.2916`、地板 1.20×）与 [competitor-conyss.md](competitor-conyss.md) §3
> 关联：[go-to-market-plan.md](../03-product/go-to-market-plan.md)（本调研的裁决产物）、
> [product-brief.md](../00-overview/product-brief.md) §4/§6（「内部使用」定位，本调研直接挑战它）、
> [payments.md](payments.md)（收款通道）、[ADR 0015](../05-adr/0015-client-strategy.md)（客户端策略）
> 证据口径：官方页面 / 商店页面直接读取 = **高**；多个推荐站交叉 = 中；单一聚合站或论坛转述 = **待核实**。
> ⚠️ 机场行业没有权威统计，几乎所有价格数据来自靠 aff 佣金盈利的推荐站，**存在利益偏差**。
> 凡标 **[估算]** 的是本文推算，不是查到的数。

---

## 1 · 结论速览

| 问题 | 结论 | 证据强度 |
|---|---|---|
| 市场按 GB 卖多少钱 | 中转机场月付 **¥0.07–0.20/GB**；不限时流量包 **¥0.15–0.5/GB**（100GB 包主流 ¥30–80）；IPLC 专线 **¥0.4–0.8/GB** | 中（多站交叉，§2） |
| ¥3/GB 在市场里是什么位置 | **是不限时包中位价的 6–20 倍、月付套餐的 15–40 倍**。唯一可对标的是海外住宅代理（$3–8/GB），但那是爬虫买家，不是消费者 | 高 |
| 机场靠什么获客 | Telegram 频道 + GitHub「机场推荐」仓库 + 测评站 aff（15–20%）+ 免费试用（100GB/7 天已是标配）| 中（§3） |
| 闲鱼 / 淘宝能不能卖 | 平台层面：关键词屏蔽 + 举报封号；法律层面：**有公开判例**，销售额 3.4 万判 9 个月、淘宝开店卖 VPN 判 5 年 6 个月 | 高（§4） |
| Chrome 扩展能不能做渠道 | 能，头部 VPN 扩展 200 万–900 万用户；但 **Chrome Web Store 大陆不可达，Edge Add-ons 可达**；扩展只能设 HTTP(S)/SOCKS 代理，不能跑 VLESS/REALITY | 高（§5） |
| 自研浏览器能不能做渠道 | Chromium fork 对 1–2 人团队不可行；Electron/WebView 壳 4–8 周可做，但那是客户端不是渠道；大陆场景法律暴露面最大 | 高（§6） |
| 国际 VPN 怎么获客 | NordVPN 首单佣金 40–100% + 续费涨价 3–7 倍 + YouTube 4.3K 创作者；Mullvad 反其道无 aff、€5 十七年不变 | 高（§7） |

---

## 2 · 机场定价基准（2025–2026）

### 2.1 月付套餐（按月重置）

| 档位 | 代表 | 价格 | 流量 | 折算 | 来源 |
|---|---|---|---|---|---|
| 超低价 | 锦云 | ¥4.8/月 | 50GB | ¥0.096/GB | [everett7623 清单](https://github.com/everett7623/airport-recommendations-2026) |
| 低价 | 夜煞云 / SSONE / 咖啡云 | ¥9.9–10/月 | 100–150GB | ¥0.066–0.099/GB | [dijiajichang](https://github.com/KaWaIDeSuNe/dijiajichang) |
| 中档 | COCODUCK / 速界 | ¥15–17/月 | 50–100GB | ¥0.17–0.30/GB | everett7623 |
| 专线 | WgetCloud | ¥69/月起 | 140GB | ¥0.49/GB | dijiajichang |
| 高端专线 | MESL ¥50 / ImmTelecom ¥72 / TAG ¥109 | — | — | — | everett7623 |
| 本仓一手走查 | conyss Lite / Pro / Ultra | ¥30 / 60 / 90 | 100 / 230 / 400GB | ¥0.30 / 0.26 / 0.225 | [competitor-conyss §3.2](competitor-conyss.md) |

**预付陷阱**（[clashnodes.net](https://www.clashnodes.net/)）：「¥7 起」广告价通常是年付均摊，真实月付是 2–3.6 倍。

**跑路数据**（[17zz 跑路名单](https://www.17zz.net/posts/details/96)）：2024–2025 列出 14 家跑路机场，2026 又列 6 家高风险；共性是月付 ¥5 / 年付 ¥30 级超低价、买一年送一年、终身套餐、激进返利。
**「不跑路」是可以卖的东西** —— 但它需要时间证明，不是一句话。

### 2.2 按量付费 / 不限时流量包（回答「有没有纯按 GB 卖的」）

**有，且已经是一个成熟细分品类**，定位是「备用机场」——买一次放着，主力挂了再用。
2026 年至少五篇专题：[clashsub](https://clashsub.org/top-airports-by-packages/)、
[gfwoff 20 家](https://gfwoff.org/pay-by-volume-proxy-providers/)、
[Kerry 笔记](https://kerrynotes.com/best-vpn-pay-by-traffic/)、[润土](https://runtushare.net/5454/)、
[NodeRadar](https://noderadar.online/guides/pay-as-you-go/)。

| 机场 | 流量包 | 价格 | 有效期 | 元/GB |
|---|---|---|---|---|
| 赔钱机场 | 1000GB | ¥18.9 | 不限时 | **¥0.019** |
| XFLTD | 120GB | ¥10 | 不限时 | ¥0.083 |
| 魔戒 | 130 / 210 / 420GB | ¥19.9 / 29.9 / 52 | 不限时 | ¥0.12–0.15 |
| 喵喵 | 100 / 500GB | ¥20 / 78 | 永久 | ¥0.16–0.20 |
| 八戒 | 100 / 220GB | ¥34.8 / 70.8 | 不限时 | ¥0.32–0.35 |
| CyberGuard | 220GB | ¥79 | 不限时 | ¥0.36 |
| Taishan | 200GB | ¥78 | 不限时 | ¥0.39 |
| 奈云 | 280GB | ¥138 | 不限时（复购不叠加） | ¥0.49 |
| Tolink | 100GB | ¥58.8 | 365 天 | ¥0.59（待核实） |
| NiceCloud | 100GB | ¥69 | 不限时（复购覆盖旧流量） | ¥0.69 |
| 91 | 150GB | ¥125 | 不限时 | ¥0.83 |
| 鲤云 / 财路云 | 100GB | ¥140 / 150 | 不限时 | ¥1.40–1.50（待核实） |

三条对本项目直接有用的观察：

1. **100GB 不限时包的价带是 ¥20–150，主流 ¥30–80，中位约 ¥0.35–0.5/GB。**
2. **不限时包相对同家月付的每 GB 溢价通常 2–6 倍**（全球云：不限时 ¥0.875 vs 订阅 ¥0.143）。
   市场已经接受「不过期要贵得多」这个逻辑。
3. **陷阱条款是常态**：复购不叠加 / 覆盖旧流量（NiceCloud、奈云）、写不限时实际 12 个月过期（91）。
   Kerry 笔记原话：「流量不会每月清零，不代表线路、域名、套餐规则一直不变」。
   → **规则透明本身可以做卖点。**

**付费体验包**已有先例：魔戒 **¥1/1GB**、可信云 **¥2/3GB/3 天**、财路云 **¥8/20GB** 限购。
这与 [pricing §3.4](../03-product/pricing-and-plans.md)「不做免费试用」并不冲突：**付费体验包不是免费试用**。

### 2.3 ¥3/GB 在坐标系里的位置

| 参照 | 元/GB | ¥3/GB 相对倍数 |
|---|---|---|
| 中转机场月付（§2.1 低价档） | 0.07–0.10 | **30–43×** |
| conyss Lite（本仓一手） | 0.30 | 10× |
| 不限时流量包中位 | 0.35–0.50 | **6–8.6×** |
| IPLC 专线 | 0.40–0.80 | 3.8–7.5× |
| 本仓轻量档（¥72/30 GiB） | 2.40/GiB ≈ 2.24/GB | 1.34× |
| 本仓加油包 | 1.20/GiB ≈ 1.12/GB | 2.7× |
| 本仓出口变动成本 `c` | 0.865/GiB ≈ 0.81/GB | 3.7× |
| 海外住宅代理按量付费（Bright Data $8.4、Oxylabs $15、Decodo $3–6） | ¥21–107 | 0.03–0.14× |
| Windscribe Build-a-Plan | $1/地区/月含 10GB，$3 起（≈¥0.7/GB） | 4× |

来源：[aimultiple 代理价格](https://aimultiple.com/proxy-pricing)、[humanbrowser 2026 住宅代理](https://humanbrowser.cloud/blog/best-residential-proxy-scraping-2026)、
[Windscribe 定价](https://windscribe.com/knowledge-base/articles/how-much-does-it-cost-to-use-windscribe)。

> 一句话：**¥3/GB 不是「稍贵」，是另一个价格数量级。** 它只能卖给两种人：
> 月用量 ≤ 15 GB 的轻用户（AI / 开发 / 查资料），以及被跑路和限速伤过、愿为「稳定 + 售后」付溢价的人。
> 这与 [product-brief §3](../00-overview/product-brief.md) 的场景优先级（AI 第一、流媒体最后）**完全一致**。

---

## 3 · 机场获客渠道（按实际权重）

| # | 渠道 | 机制 | 数据 | 证据 |
|---|---|---|---|---|
| 1 | **Telegram 频道 / 群** | 每家机场标配「公告频道 + 交流群 + 客服 bot」；测评频道收广告费 | 翻翻墙 @ffqchannel 自述近 4 万订阅（[t.me/s/ffqchannel](https://t.me/s/ffqchannel)），公开承认收机场广告费；报价未公开 | 待核实（tgstat 403） |
| 2 | **GitHub「机场推荐」仓库** | 2026 年同质化 SEO 仓库爆发；README 自认「部分链接维护者可获佣金」；**GitHub 墙内可达** | [everett7623](https://github.com/everett7623/airport-recommendations-2026)（40+ 家、月更）、[John19187](https://github.com/John19187/ji-chang-tui-jian)、[hotseo123](https://github.com/hotseo123/shida-jichang-tuijian)、[kjfx](https://github.com/kjfx/jichang) | 高 |
| 3 | **测评 / 导航站（aff 变现）** | 每月优惠码汇总、跑路名单、按量付费专题 | [jichangzhinan](https://jichangzhinan.org/)、[clashnodes](https://www.clashnodes.net/)、[kerrynotes 优惠码](https://kerrynotes.com/vpn-coupon-code/)、[一个朋友 免费机场月报](https://ygpy.net/vpn/test/2026/08.html) | 高 |
| 4 | **YouTube 测评** | 视频内挂 aff 链接 + 优惠码 | 报价无公开数据 | 无数据 |
| 5 | **免费试用** | 注册即送 | 神临 / 樱花云 **100GB/7 天**；佬大云 / kko **100GB/30 天**；自由航线 1000GB/30 天；老牌 SSRDOG 3GB/24h（[ygpy 2026-08](https://ygpy.net/vpn/test/2026/08.html)） | 高 |
| 6 | **邀请返利** | 面板内置 | 见 §3.1 | 中 |
| 7 | 抖音 / 小红书 / 淘宝 / 闲鱼 | — | 见 §4 | 高 |

### 3.1 邀请返利比例

- **面板机制**（[V2Board 文档](https://docs.v2board.com/use/commission.html)）：全局比例（示例 10%），可按用户单设，3 天确认期，提现可关闭。
- **行业分档**（[HackerTalk 机场 FAQ](https://hackertalk.net/posts/409698751648948224)）：一线专线 **3–8% 且只能站内消费**；二线 **8–10%**；普通中转 **约 15%**；**一般不超过 20%**。返利异常高被视为「没底气」，跑路名单印证。
- **极端实例**：hostloc 有机场招 aff「循环返利 40%」（[hostloc](https://hostloc.com/thread-1364327-1-1.html)，正文需登录，待核实）。
- **机场经济学**（[bulianglin](https://bulianglin.com/archives/air.html)）：10 台 VPS 约 $500/月；第三方收款抽约 10%；净利率约 30%。
  → 支付 10% + aff 20% 吃掉大半毛利，这是 aff 上限卡在 20% 的根本原因。

**对本项目**：[pricing §5](../03-product/pricing-and-plans.md) 已裁决返佣为**一次性定额**（轻量 ¥7.20 / 标准 ¥15.90 / 重度 ¥35.80），
且证明按订单金额 10% 循环会打穿 1.20× 地板。**外部 aff 站普遍要求循环比例，我们给不起** —— 这是 §3 第 2、3 条渠道对我们**半失效**的直接原因。

### 3.2 免费试用的行业漏洞

临时邮箱反复注册白嫖（[ygjc.cc](https://ygjc.cc/vpn/free)、[Kerry 免费机场](https://kerrynotes.com/free-proxy-vpn/)），
所以正规机场会要求邀请码 / 关注 TG / 验证码。本仓的邀请码 + 邮箱验证 + 速率限制三层（[user-journey §3.2](../03-product/user-journey.md)）与行业做法一致。

---

## 4 · 国内平台渠道（闲鱼 / 淘宝 / 抖音 / 小红书）：平台风险与法律风险

### 4.1 法律：有判例，且金额门槛很低

| 案例 | 行为 | 金额 | 罪名 | 结果 | 来源 |
|---|---|---|---|---|---|
| 吴向洋案 | **淘宝开店**卖 VPN | — | 非法经营罪 | **5 年 6 个月 + 罚金 50 万** | [腾讯新闻](https://news.qq.com/rain/a/20210312A05S6H00) |
| 「飞跃SS」案 | 网站卖账户 | 销售额 **3.4 万** | — | **9 个月** | [RFA](https://www.rfa.org/mandarin/yataibaodao/renquanfazhi/ql2-10112018093945.html) |
| 朱某案 | 多平台推广销售 | **4 万余** | 拒不履行信息网络安全管理义务罪 | 1 年 4 个月 | [网易 · 司法前沿](https://www.163.com/dy/article/G4T56KT10514C5MG.html) |
| 薛某案 | 架设服务器销售 | 47 万 | 非法经营罪 | 3 年缓 3 年 | 同上 |
| 卢某案 | 公司化运营 | 37 万 | 提供侵入工具罪 | 3 年 3 个月 | 同上 |
| 开江法院 2025-04 | 贩卖 VPN 链路 | 2704 万 | 提供侵入、非法控制计算机信息系统程序、工具罪 | 4 年 6 个月 + 罚金 50 万 | [达州长安网](https://www.dazhoupeace.gov.cn/fycz/20250416/2961965.html) |
| — | 卖 3000 元翻墙工具 | 3000 | 行政 | 拘留 15 日 | [网易](https://www.163.com/dy/article/KUCEJEM10556CVLI.html) |

**罪名与门槛**：

- 主流定性是**提供侵入、非法控制计算机信息系统程序、工具罪**（刑法 285 条第 3 款）。
  2011 年司法解释第三条：向 **20 人以上**提供即「情节严重」（[最高法入库案例](https://www.court.gov.cn/zixun/xiangqing/449641.html) 转述，高）；
  同条另有「违法所得 5000 元以上或造成经济损失 1 万元以上」（**待核实条文原文**）。
- 少数定**非法经营罪**（刑法 225 条），门槛按电信市场解释更高。
- 学界有「租售翻墙软件不构成 285 条」的反对意见（[陈蓟](https://www.houqilawyer.com/thickpointofview/info.aspx?itemid=2487)、[高艳东](https://zhuanlan.zhihu.com/p/668505651)），**但判例站在另一边**。

> 🔴 **对本项目的含义只有一句**：闲鱼 / 淘宝要求**实名 + 支付宝**，每笔交易在大陆平台留有完整证据链，
> 而「20 人以上」是一个**一个周末就能达到**的数字。这不是「有风险」，是四条渠道里**唯一一条把运营者身份直接钉在大陆平台上**的渠道。
> [product-brief §4](../00-overview/product-brief.md) 把「内部使用 + 邀请制」定义为**合规姿态**，§10 明写「若转向公开运营，需重新裁决合规与风控方案」。
> 闲鱼渠道就是那个「转向」。

### 4.2 平台规则

- 「VPN / 翻墙 / 梯子」是淘宝 / 闲鱼屏蔽词（[百度知道](https://zhidao.baidu.com/index/?word=%E5%9C%A8%E6%B7%98%E5%AE%9D%E4%B8%8A%E6%80%8E%E4%B9%88%E4%B9%B0VPN%E5%95%8A%EF%BC%9F%E5%85%B3%E9%94%AE%E5%AD%97%E8%A2%AB%E5%B0%81%E4%BA%86%EF%BC%81)）。
- 闲鱼违禁词三类：完全不能发 / 发了提示 / **隐性违禁词（可发但限流或封号）**；举报后 5 个工作日审核，确认即封号；违规下架不可恢复（[知乎](https://zhuanlan.zhihu.com/p/149141077)、[店托易](https://diantuoyi.com/article/18802.html)）。
- 市场实际做法（[搜狐 闲鱼暗语](https://www.sohu.com/a/673348233_463965)、[界面](https://www.jiemian.com/article/3848875.html)）：商品挂「网络加速 / 海外游戏加速 / 软件服务」，暗语「服务器」「梯子」，私信发兑换码或发卡网链接。
  **本文只记录事实，不把它当作可以照抄的做法** —— 这套做法解决的是平台审核，解决不了 §4.1。
- 抖音 / 小红书：未找到 2025 年 VPN 推广被处罚的具体公开案例（无数据）；2026 Q1 短视频合规数据把「跨平台引流诱导」列为高风险（[ByeRisk](https://www.byerisk.com/blog/2026-q1-short-video-compliance-penalty-data-report)）。

### 4.3 卡密 / 自动发卡生态

- 自动发卡网 = 虚拟商品自助售卖 + 即时发卡密，集成支付宝 / 微信 / 聚合支付（[百度百科](https://baike.baidu.com/item/%E8%87%AA%E5%8A%A8%E5%8F%91%E5%8D%A1%E7%BD%91/65746130)）；自建常用「独角数卡」+ Epusdt（[教程](https://www.skygv.com/index.php/2024/03/20/%E6%90%AD%E5%BB%BAepusdt%E6%8E%A5%E5%85%A5%E7%8B%AC%E8%A7%92%E6%95%B0%E5%8D%A1%E5%AE%9E%E7%8E%B0usdt%E6%94%B6%E6%AC%BEaapanel/)）。
- 发卡网自身的刑事风险：跑分 / 帮信 / 掩隐（[湖南案例](https://m.voc.com.cn/xhn/news/202412/21579847.html)、[澎湃](https://www.thepaper.cn/newsDetail_forward_27317293)）。
- **卡密只是把收款风险外包，不消除它；发卡网被端时订单记录就是证据。**

### 4.4 闲鱼作为收款通道的真实价值

必须公平地说一句：[payments.md](payments.md) 的结论是「微信支付明文禁止本类目、易支付随时跑路、USDT 对非技术用户是巨大转化漏斗」。
**闲鱼是唯一一条让非技术用户用支付宝一键付款、且资金不经过第三方跑路平台的通道** —— 这是它的全部吸引力，也是它把身份暴露出去的原因。两者是同一件事。

---

## 5 · Chrome 扩展作为渠道

### 5.1 头部 VPN 扩展的规模（2026-09-02 商店页面直接读取）

| 扩展 | 用户数 | 评分 / 评分数 | 最近更新 | 模式 |
|---|---|---|---|---|
| 1clickVPN | **900 万** | 4.6 / 42K | 2026-04-27 | 免费 + 付费 |
| Browsec | **800 万** | 4.5 / 40.2K | 2026-07-09 | 免费 4 地区 + Premium |
| Urban VPN | **600 万** | 4.7 / 66.9K | 2026-09-01 | 完全免费（P2P / 数据变现，已被曝售卖 AI 对话数据，[Tom's Guide](https://www.tomsguide.com/computing/vpns/this-vpn-is-harvesting-your-ai-conversations-and-6-million-people-are-using-it)） |
| Hola | 400 万 | 4.8 / 368K | 2026-08-26 | 免费（P2P 共享带宽） |
| Windscribe | 200 万 | 4.6 / 21K | 2026-08-11 | 免费 10GB/月 + Pro |
| Proton VPN | 200 万 | 4.3 / 4.1K | 2026-08-14 | 免费 + Plus |
| SetupVPN | 100 万 | 4.7 / 47.3K | 2025-01-06（停更 20 个月） | 「终身免费」 |
| Touch VPN | 曾 800 万+ 周活 | — | **2025-10-26 下架** | — |

Edge Add-ons 侧（[extpose](https://extpose.com/collections/vpn/edge/)，待核实）：ZenMate 110 万、Touch VPN 90 万、SetupVPN 63 万、Windscribe 48 万。

**规模来自免费**。每一个百万级扩展都是免费入口 + 付费升级；没有一个是纯付费产品。

### 5.2 技术边界（高，官方文档）

- `chrome.proxy` 只支持 `http / https / quic / socks4 / socks5` scheme；模式 `fixed_servers` / `pac_script` 等（[chrome.proxy](https://developer.chrome.com/docs/extensions/reference/api/proxy)）。
- **Chrome 对 SOCKS5 不支持任何认证**；带认证的只有 HTTP / HTTPS 代理，走 `webRequest.onAuthRequired`（MV3 需 `webRequestAuthProvider`，Chrome 108+）（[Chromium proxy.md](https://chromium.googlesource.com/chromium/src/+/HEAD/net/docs/proxy.md)）。
- HTTPS 代理（代理连接本身走 TLS）**只能**由扩展 / PAC / 策略配置，系统代理设置配不了；可协商 HTTP/2。
- **结论：扩展不能原生说 VLESS / Trojan / SS / REALITY。** 上游只有三种：
  ① 服务端暴露 HTTPS 代理端点（行业标准：Opera = SurfEasy HTTPS 代理 + Basic 认证，[逆向 gist](https://gist.github.com/spaze/558b7c4cd81afa7c857381254ae7bd10)；Proton / Windscribe 同构）；
  ② Native Messaging 本地内核（现成项目 [noctis](https://github.com/c0nn3ct-info/noctis)：扩展 + 原生助手 + sing-box/xray，支持 VLESS REALITY，118 stars）；
  ③ Service Worker 内隧道 —— **不可行**，扩展没有 API 能把自己变成网络栈出口。
- 开源模板：[StealthSurf-VPN/browser-extension](https://github.com/StealthSurf-VPN/browser-extension)（MIT，MV3，PAC + onAuthRequired + OAuth PKCE）；[pia-foss/extension-chrome](https://github.com/pia-foss/extension-chrome)。

### 5.3 HTTPS 代理在大陆的存活性

- TLS 指纹是优势：Chrome 自己发起的连接就是真 Chrome ClientHello，这正是 [NaiveProxy](https://github.com/klzgrad/naiveproxy) 的前提。2022-10 大封锁波（trojan / VLESS / gRPC）中 naiveproxy 未被报告封锁（[net4people #129](https://github.com/net4people/bbs/issues/129)）。
- 但 **Chrome 裸 HTTPS 代理 = 没有 padding 的 NaiveProxy**（技术判断）：naiveproxy 靠 padding 压平握手期包长；Chrome 内置客户端做不了。抵抗力弱于 REALITY，强于 trojan。**存活期需实测**。
- 主动探测靠 Caddy `forwardproxy` 的 `probe_resistance`（无凭据不回 407、回落到真网站）对抗。
- **Cloudflare 前置不可行**：ToS 2.2.1(j) 明禁 VPN / proxy（[Cloudflare Terms](https://www.cloudflare.com/terms/)，与 [ADR 0001](../05-adr/0001-cloudflare-tos-risk.md) 一致），且 CF 不转发 CONNECT。

### 5.4 商店政策与大陆分发

| 项 | 事实 | 来源 |
|---|---|---|
| MV3 | MV2 已彻底下线（2026-08-31 商店清除） | [gHacks](https://www.ghacks.net/2026/09/01/manifest-v2-is-dead-as-chrome-web-store-permanently-purges-legacy-extensions/) |
| VPN 类可否上架 | 可以，但**不能被 Featured** | [Featured 政策](https://developer.chrome.com/docs/webstore/program-policies/featured-products) |
| 2026-08-20 新规 | 每账号默认 2 个扩展槽位 | [CWS review updates](https://developer.chrome.com/blog/cws-review-updates-2026) |
| 审核 | 24–72 小时；注册 $5 一次性 | [review process](https://developer.chrome.com/docs/webstore/review-process) |
| 扩展内收费 | CWS Payments 2021-02 关闭，跳外部支付页是通行做法 | [deprecation](https://groups.google.com/a/chromium.org/g/chromium-extensions/c/sS3W-7QdaX4) |
| **CWS 大陆可达** | **不可达**（GFW 封锁） | [chromium-extensions 讨论](https://groups.google.com/a/chromium.org/g/chromium-extensions/c/LyIauk_x2eE) |
| **Edge Add-ons 大陆可达** | **可达**，且已有代理类扩展在售；审核 ≤ 7 个工作日；政策 1.8 允许第三方支付卖订阅 | [Edge 政策](https://learn.microsoft.com/en-us/legal/microsoft-edge/extensions/developer-policies)、[发布文档](https://learn.microsoft.com/en-us/microsoft-edge/extensions/publish/publish-extension) |
| crx 侧载 | Win / Mac 只能装商店扩展，例外是开发者模式（低信任、可能被关）与企业策略（需管理员） | [Chromium FAQ](https://www.chromium.org/developers/extensions-deployment-faq/) |
| 下架案例 | FreeVPN.One（10 万+，偷截图）2025-08；Touch VPN 2025-10 | [The Register](https://www.theregister.com/2025/08/21/freevpn_privacy_research/) |

**分发优先级**：Edge Add-ons（主，墙内唯一一键安装入口）→ Chrome Web Store（海外 / 已有梯子用户）→ 官网 zip + 开发者模式教程（兜底）。

### 5.5 工作量 [估算]

MVP 扩展（登录 → 拉节点 → `fixed_servers` → `onAuthRequired` → 分流白名单 → 跳官网付费）**1–2 人 × 2–3 周**；
服务端 HTTPS 代理入站（Caddy forwardproxy 或 Xray `http` inbound + TLS + accounts）+ 账号对接 **1 周**；
商店素材、隐私政策、两家审核往返 **1–2 周**。**合计 4–8 周到可售版本。**

---

## 6 · 内置代理的浏览器作为渠道

| 产品 | 规模 | 变现 | 来源 |
|---|---|---|---|
| Opera | Q4 2025 **2.84 亿 MAU**、ARPU $2.49、全年营收 $6.15 亿；订阅（含 VPN Pro）**< 10% 营收**，VPN Pro 订户数未披露 | 免费 VPN 3 地区仅浏览器流量；VPN Pro $1.99–4/月 | [Opera Q4 2025](https://investor.opera.com/news-releases/news-release-details/opera-reports-fourth-quarter-and-full-year-2025-results-ahead/)、[VPN Pro 定价](https://www.opera.com/features/vpn-pro/vpn-pricing-plan) |
| Aloha | Android 月下载约 40 万、月收入约 $5 万（待核实） | Premium $7.99/月，欧洲 2026 涨到 €19.99 | [trendapps](https://trendapps.dev/app/android/com-aloha-browser/) |
| Vivaldi | 2025-03 内置 Proton VPN 免费版 | 联名分成（未披露） | [Vivaldi 博客](https://vivaldi.com/blog/privacy-without-compromise-proton-vpn-is-now-built-into-vivaldi/) |
| Tor Browser | 日 200 万+；团队 8 人，每个 Firefox ESR 周期 rebase 数百补丁 | 非营利 | [Tor 15.0a1](https://blog.torproject.org/new-alpha-release-tor-browser-150a1/) |
| 中文「翻墙浏览器」 | 自由门 / 无界仍在 GitHub 分发；**国内没有成规模的自带节点浏览器，凡出现即被下架** | 免费 | [freesky](https://github.com/sglfree/freesky) |

**技术路线对 1–2 人团队的可行性**：

| 路线 | 工作量 [估算] | 复用现有 REALITY 节点 | 主要问题 |
|---|---|---|---|
| Chromium fork | **不可行** | — | 3500 万行；每 4–6 周 rebase 数个工作日；32 核 / 64GB 构建机；Brave 维持极小补丁集仍有 7–14 天滞后（[Brave rebases](https://github.com/brave/brave-browser/wiki/Chromium-rebases)、[omaha-consulting](https://omaha-consulting.com/how-to-fork-chromium)） |
| Electron 壳 + 内置 sing-box | 4–8 周 | 是 | 80–200 MB 包体；每 8 周跟版；`session.setProxy` 支持 socks5 / https（[Electron session](https://www.electronjs.org/docs/latest/api/session)） |
| Tauri v2 壳 | 4–8 周 | 是 | mac 需 macOS 14+ 且 WebKit 代理有 DNS prefetch / WebTransport 泄漏（[Mysk 2026-08](https://mysk.blog/2026/08/04/webkit-proxy-icloud-private-relay-ip-leak/)） |
| **启动器 + 本机 Chrome `--proxy-server`** | **1–2 周** | 是 | 独立 profile、品牌感弱；但零浏览器维护 |
| Android WebView + libbox | 4–8 周 | 是 | 只能官网 APK；Play 允许「Web browsing apps」用 VpnService |
| iOS WKWebView `proxyConfigurations` | 6–10 周 | 是 | **中国区 App Store 不可能**（2017 起全部下架，[TTP 报告](https://www.techtransparencyproject.org/articles/apple-censoring-its-app-store-china)） |

**签名与误报**（高）：macOS $99/年 + 公证；Windows **EV 证书已不再绕过 SmartScreen**（[Microsoft 2026-05](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/smartscreen-reputation)），推荐 Artifact Signing $9.99/月（对中国主体的身份验证**待核实**）；
Xray-core 官方 release 被 Defender 标 `Trojan:Script/Wacatac.B!ml`，issue 以 not planned 关闭（[Xray-core#4928](https://github.com/XTLS/Xray-core/issues/4928)）——任何随包内核都要自己编译、签名、每版提交样本。

> **浏览器不是渠道，是客户端。** 它不带来一个新用户；它只改变已经决定用我们的人怎么用。
> 而在大陆场景下，「自带节点的浏览器」是 §4.1 罪名最直接的物证。

---

## 7 · 国际消费级 VPN 的获客数据（作为方法参照，不是价格参照）

### 7.1 Affiliate

| 厂商 | 首单 | 续费 | 来源 |
|---|---|---|---|
| NordVPN | 1 月计划 **100%**；6 月 / 1 年 / 2 年 **40%** | 30% | [wecantrack](https://wecantrack.com/insights/vpn-affiliate-programs/) |
| Surfshark | 40% + 业绩奖励 | 不付 | 同上 |
| ExpressVPN | 固定 CPA：1 月 $13 / 6 月 $22 / 12 月 $36 | — | 同上 |

### 7.2 营销投入与单位经济

- NordVPN：YouTube always-on，**4.3K 创作者、40.7K 条赞助视频、近 30 天 863 条**（[creatordb](https://creatordb.app/brands/nordvpn.com)）；每视频 $50–1000 或 $15–20 / 千次观看（待核实）。Nord Security 2025 营收破 $10 亿（待核实）。
- Kape（Express / CyberGhost / PIA）：2022 营收 $6.235 亿、付费订户 740 万（[Sharecast](https://www.sharecast.com/news/aim-bulletin/kape-technologies-reports-record-results-for-2022--12745272.html)）；营销 / 营收 ≈ 22%、每订户年营收 ≈ $84–114（[估算]）。
- 行业估算：付费搜索 CAC $20–100+；月付流失 15–25%/月；**续费涨价是 CAC 回收的核心**：Surfshark $1.99 → 续费 $15.45（+676%）（[Windscribe 定价对比](https://windscribe.com/blog/vpn-cost/)）。

### 7.3 口碑增长型

- **Mullvad**：€5/月自 2009 年不变；官方原文「We do not have affiliates, and do not pay for influencers」（[Mullvad 政策](https://mullvad.net/en/help/policy-reviews-advertising-and-affiliates)）；约 40 人、无外部融资。
- **Proton VPN**：2025 年 62 个国家注册量暴涨 >100%，增长引擎 = **审查 / 封锁事件驱动 + 免费无限流量**；年报**完全不提中国**（[Proton 2025 年报](https://protonvpn.com/blog/eoy-report-2025)）。
- **Windscribe**：免费 10GB/月 + 反 aff 内容营销做差异化。

**对本项目最有用的一条**：Mullvad 证明**一个价格、无 aff、靠可信度**是一条真实存在的路 —— 而它恰好与 [pricing](../03-product/pricing-and-plans.md) 的一次性定额返佣、不打价格战、规则透明是同一个方向。

---

## 8 · 这些证据证明什么、不证明什么

**证明**：市场价带、渠道结构、扩展 / 浏览器的技术边界与商店政策、判例的存在与量刑区间、头部扩展的用户规模。

**不证明**：
1. 不证明闲鱼上「稳定梯子」类商品的**实际成交价与成交量** —— 没有任何公开数据，需实测（挂一件商品看询盘）。
2. 不证明 Chrome 裸 HTTPS 代理在大陆的**存活期** —— 需实测，建议 1–2 个测试域名跑两周对照 REALITY。
3. 不证明 Edge Add-ons 对 VPN 类扩展的**审核宽严** —— 有在售案例，但没有被拒 / 下架比例数据。
4. 不证明「20 人以上」之外的**违法所得门槛**条文原文（待核实）。
5. 不证明测评站 aff 的**真实转化** —— 它们自己也不披露。
6. 不证明 Telegram 渠道对**本项目目标用户**（闲鱼上的非技术用户）有效 —— [user-journey §3.3](../03-product/user-journey.md) 实测 Telegram 大陆异常率 99.1%，只对已有梯子的人有效。
