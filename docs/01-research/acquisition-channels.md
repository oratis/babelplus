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

---

## 9 · 海外市场（2026-09-02 追加调研）：eSIM 已经赢下手机场景，可赢的是笔电与酒店 WiFi

> 追加背景：2026-09-02 商业前提改为「仅在海外针对非中国公民售卖」（[go-to-market-plan.md](../03-product/go-to-market-plan.md)），
> 需要一份面向来华外国人市场的调研。本节约 55 次搜索 + 55 次页面抓取。
> ⚠️ **抓取出口 IP 在日本**，部分电商页返回日元报价，已逐条注明。

### 9.1 英文「China VPN」价格（2026-09）

| 服务 | 月付 | 年付折月 | 设备 | 退款 | 证据 |
|---|---|---|---|---|---|
| **Astrill**（公认的在华首选） | **$30.00** | $15.00 | 5 | **无退款**，7 天试用 | 高 · [astrill.com](https://www.astrill.com/) |
| **12VPX (12VPN)** | **$29.99** | $14.17 | 6 | — | 高 · [12vpx.com/pricing](https://12vpx.com/pricing) |
| ExpressVPN Basic | $12.99 | $4.99 | 10 | 30 天 | 高 · [security.org](https://www.security.org/vpn/expressvpn/) |
| NordVPN Basic | $14.99 | $5.49 | 10 | 30 天 | 高 · [security.org](https://www.security.org/vpn/nordvpn/) |
| Mullvad | **€5 统一价**，加密货币 −10% | 无年付折扣 | 5 | — | 高 · [mullvad.net/pricing](https://mullvad.net/en/pricing) |
| **LetsVPN**（快连） | $6.99；**周付 $2.99** | $5.00 | — | 1 小时试用 | 待核实（美区 App Store 转述） |
| UpVPN（按量） | **$0.05/次 + $0.04/GB + $0.02/小时**，余额不过期 | — | — | — | 高 · [upvpn.app](https://upvpn.app/one-time-payment-vpn/) |
| Windscribe Build-a-Plan | $1/地区/月 + $1 无限流量，最低 $3 | — | — | — | 高 |

**两条结论**：

1. **在华首选档是 $30/月**（Astrill、12VPX）。这个价位远高于我们四档里最贵的 $18.90。
2. 🔴 **行程制 / 按量计费在这个市场几乎无人做。** Astrill 明确不提供短期方案且不退款；
   ExpressVPN / NordVPN / Surfshark 事实上是拿「30 天退款保证」当一次性行程方案用。
   唯二例外是已退出大陆的 LetsVPN 周付与小众的 UpVPN。

### 9.2 🔴 2026-04 LetsVPN 退出中国大陆 —— 它腾出的正是「低价 + 短期」这个位置

- **2026-04-28** LetsVPN 宣布终止中国大陆业务，此前约 20 天技术恢复尝试失败，关闭大陆支付通道，退款自 04-08 起算（[corpus.lantern.io](https://corpus.lantern.io/findings/2026-anon-letsvpn-vpn__letsvpn-china-exit-2026/)，高）。
- ABC News 独立佐证：LetsVPN「深受在华外籍人士欢迎」，因 "continuous internet blockage" 暂停大陆服务（[ABC 2026-06-04](https://www.abc.net.au/news/2026-06-04/as-beijing-cracks-down-on-vpns-internet-users-in-china-adapt/106754254)，高）。
- 技术原因：**协议签名识别** —— 「一旦 GFW 提取到客户端握手特征，就能同时封掉所有共享该特征的连接」。
  **硬编码、数百万用户共享同一握手指纹的商业 VPN 是高价值易封目标。**
- GFW 能力升级的硬证据：2025-08-20 约 74 分钟内 GFW 无条件向 TCP 443 注入伪造 RST+ACK，
  注入包带前所未见的设备指纹（TTL 96/97/98），22/80/8443 不受影响（[gfw.report](https://gfw.report/blog/gfw_unconditional_rst_20250820/en/)，高）。

> **对本项目的含义有两条，方向相反，都要说**：
> ① 9.1 表里**没有任何一家商业 VPN 使用 VLESS + REALITY + XTLS-Vision**，而这是社区认为 2026 年仍稳的栈 —— 技术差异化是真的；
> ② 但 LetsVPN 的死因是**规模本身**（共享指纹），这意味着我们今天的优势有一部分来自「还没人注意到我们」，
> 它**随规模衰减**，不是护城河。

### 9.3 🔴 eSIM 才是真正的对手，而且它在手机场景上已经赢了

Airalo 官方帮助页原文（高 · [airalo.com](https://www.airalo.com/help/using-managing-esims/ZSEEHBT5HW6F/stay-connected-in-china-your-guide-to-unfiltered-internet-access-is-a-vpn-required/ZDZBNIKI4TFP)）：

> **"No. When using Airalo's China eSIM, Regional Asia eSIM, and Global eSIM, a VPN is NOT required."**
> 唯一限制条件：**"as long as you are using your eSIM data (and not the hotel Wi-Fi)"**

机制：手机连中国基站，数据经 GRX 隧道回落到运营商在**香港 / 新加坡**的归属网关出网，不经过中国境内互联网（待核实）。

| 供应商 | 套餐 | 价格 | $/GB |
|---|---|---|---|
| eSIM.dog | 1GB / 7 天 | **$0.57** | 3GB 档低至 **$0.19** |
| Trip.com（CMCC） | 起价 $0.41/天 | 30GB/30 天 ≈ $15.75 | ~$0.53 |
| Nomad | 50GB / 45 天 | ≈ $37.7 | $0.75 |
| Airalo | 10GB / 30 天 | $26.50 | $2.65 |
| Holafly（无限量） | 7 天 | $27.50 | — |

**7 天行程总价对照**：eSIM 低价档 **$3–12** · Airalo 3GB $12 · Holafly 无限 $27.50 ·
Astrill 月付 $30 · **AT&T Day Pass $70–84（且走中国境内网络，被墙）** · 随身 WiFi 含押金 $80–150。

🔴 **$0.19–2.65/GB 是我们无法竞争的价格。** 我们的 $0.378–0.833/GiB 只在 eSIM 的中高价档附近，
而 eSIM 卖的是**连接本身**，我们卖的是**在已有连接上的通道** —— 后者是附加品，不是替代品。

### 9.4 eSIM 的六个结构性缺口 —— 我们的市场就在这里

1. **必须入境前安装。** Apple 官方：**"eSIMs from non-China mainland carriers can't be installed while located in China mainland."**（高 · [support.apple.com/en-us/123879](https://support.apple.com/en-us/123879)）
2. **机型限制**：中国大陆在售 iPhone 只有 iPhone 17e 与 iPhone Air 支持 eSIM；多数国产安卓不支持（部分待核实）。
3. **热点共享被限**：Holafly 每天仅可共享 1GB（高）；部分线路有 TTL 限制导致笔电共享失败（待核实）。
4. 🔴 **笔电完全无法覆盖。** 多源一致：「If you need a laptop for work, cloud files, email, video calls… do not rely only on a phone eSIM」（高 · [chinadigitalnomads.com](https://chinadigitalnomads.com/the-best-vpns-and-esims-for-china/)）。
5. **eSIM 不加密** —— 绕墙 ≠ 隐私。
6. **延迟 150ms+**（待核实）。

> **战略含义（本节最重要的一句）**：
> **eSIM 已经吃掉「游客手机上网」，价格低到无法竞争。可赢的市场是「笔电 + 酒店/咖啡馆 WiFi + 办公场景 + 长住」** ——
> 而这批人恰好 ARPU 更高、留存更长。

### 9.5 ⚠️ 运营商漫游是否绕墙 —— 证据冲突，未解决

一方说美国运营商在华漫游走境内网络、GFW 生效；另一方说经归属运营商路由、社交媒体正常。
**没有找到任何非厂商、非联盟的权威来源。** 技术上取决于该运营商是 home-routing 还是 local breakout，逐家不同。
🔴 **这是整份调研里唯一既关键又无法靠桌面解决的技术问题，必须自己实测。**

### 9.6 市场规模：三套官方口径必须分清

| 口径 | 发布方 | 2025 |
|---|---|---|
| 外国人出入境**人次** | 国家移民局 | **8,203.5 万** |
| **入境外国人** | 国家移民局 | ~4,120 万（推算） |
| 入境游客-外国人 | 文旅部 | 3,517 万 |
| 入境游客总数（**含港澳台**） | 统计局 | 15,450 万 |

「1.545 亿入境」里约 **77% 是港澳台同胞**。可服务市场是 **3,500–4,100 万**那一行。

- **2026 H1 入境外国人 2,291.4 万人次，+20.4%**（高 · [人民网](https://en.people.cn/n3/2026/0728/c90000-20482298.html)、[NIA](https://www.nia.gov.cn/n897453/c1789835/content.html)）
- **免签入境占比：2024 年 2,011.5 万 → 2025 年 3,008 万（73.1%）→ 2026 H1 1,781.5 万（77.7%）**（高）
  → **每 10 个入境外国人有近 8 个是免签短期访客** —— 按定义就是不会实名办中国 SIM、不会开本地账号的人。这是最干净的需求代理指标。
- **停留期 15 → 30 天**（2024-11-30 生效，高）；单方面免签 **50 国**，含 2026-02-17 新增的英国与加拿大（高）；**240 小时过境免签 57 国 / 65 口岸**（高）
- 🔴 **客源国 TOP10 占 62%，其中只有一个西方市场（美国第 7）** —— 韩、俄、马、越、泰、新、美、日、蒙、澳。
  **按美/欧旅客画像做定价与文案，只覆盖少数量。**
- **在华外籍存量**：唯一权威全国口径是七普（2020-11-01）**845,697 人**（高 · [统计局公报](https://www.stats.gov.cn/sj/zxfb/202302/t20230203_1901088.html)）。
  2024–2026 年全国数据**找不到官方来源**；流传的「109.7 万」只能追溯到移民中介营销站，**不要引用**。
  北京长期常住外籍 2.2 万（十年前 3.7 万，−40%，高 · SCMP）；上海约 9.2 万（高 · SCMP）；
  **留学生 2024–25 学年 38 万、+15%**（高 · China Daily）。

### 9.7 🔴 证据质量警告：这个赛道的公开「实测」大多不是实测

- **greatfirewallguide.com 的成功率百分比不是实测。** 其方法论页自承
  **"This is documentation and protocol analysis, not packet capture from inside China"**、
  **"We do not operate test infrastructure inside China"**，并写明
  **"This site is funded by affiliate commissions… That is the conflict, stated plainly"**
  （高 · [methodology](https://greatfirewallguide.com/methodology)）。**不要引用它的数字。**
- **vpnMentor 的母公司是 Kape**，同时拥有 ExpressVPN / CyberGhost / PIA，其 About 页自认排名
  "may also take into consideration the common ownership… and affiliate commissions"（高 · [about-us](https://www.vpnmentor.com/about-us/)）。
- 相对可信的两家仍互相矛盾：Comparitech 从**深圳租用服务器**实测 59 款，推荐含 ExpressVPN，
  且结论是 **"almost all VPNs get blocked in China at some point"**（高）；
  Unusual Nomad 2026 年两次实地则称 ExpressVPN **"too inconsistent to recommend"**（高）。

> **这个赛道不存在可信的公开实测基准 —— 这本身就是产品机会**（我们有真节点，可以自己出数据）。

### 9.8 渠道：SEO 不可行，ASO 与论坛可行

| 渠道 | 判定 | 依据 |
|---|---|---|
| **Google SEO「best vpn for china」** | ❌ **不可行** | 前排被 Kape 系与高权重联盟站锁死；叠加 AI Overview，2026-03 核心更新后 **71% 联盟站排名下滑**，"best X for Y" 类查询流量中位数 **−41%**（待核实） |
| **免费「是否被墙」检测工具** | ❌ 已饱和 | Comparitech / vpnMentor / GreatFire / WebsitePulse / chinafirewalltest 全在做 |
| **App Store ASO** | ✅ **可行，且是唯一在华可触达通道**（见 9.9） | 约 70% 安装来自商店搜索；长尾词适合小团队（待核实） |
| **TripAdvisor / 外籍论坛** | ✅ **最被低估** | 真实旅客主动建议 **"best to use a small provider rather than a well-known one like ExpressVPN or Astrill"**（待核实，抓取被 403）—— 直接为小厂商背书 |
| **Reddit r/chinalife** | ⚠️ 有限但真实 | **15.6 万成员，年增 5.5 万（+54.5%）**（高 · gummysearch）。⚠️ 具体版规无法核实，Reddit 对本环境全面封锁 |
| **联盟 / 比价站** | ❌ 结构性排斥 | 首单佣金 30–100%，或固定 $40–100/单；NordVPN 月付新签 100% |
| Product Hunt | ⚠️ 一次性 | 2026 年 #1 需 500–1,800 upvotes，跌出前十基本无流量（待核实） |
| Google Ads 投放 VPN | 政策上**未见禁止** | 抓取 Google Ads 政策总页未见 VPN / 匿名服务 / 规避工具条目（高）。「vpn for china」的 CPC **找不到来源** |

🔴 **获客窗口**：旅客购买连接方案的时点是**出发前 1–2 周，且必须在入境前完成**（入境后官网被墙、Play 不可用）。
而 **Trip.com 已经把 eSIM 卖在机票 / 酒店预订流程里**，官方文案原文：
"Trip.com China eSIM comes with built-in VPN for foreigners…"（高）。**这是最难攻的结构性壁垒。**

### 9.9 🎯 应用商店：iOS 是唯一还能触达在华用户的分发通道

- ✅ **决定性发现**：**持非中国区 Apple ID 的用户在中国境内可以正常下载和更新 VPN app。**
  Apple 2017 年下架时的官方口径（VyprVPN 转述）：**"Users in China accessing a different territory's App Store… are not impacted; they can download the iOS app and continue to receive updates as before."**
  （高 · [vyprvpn.com](https://www.vyprvpn.com/blog/post/apple-removes-vyprvpn-major-vpn-apps-china-app-store)）
  → **这正好是我们的目标人群。** 对比：**Google Play 在大陆自 2012 年起完全被封**，安卓必须入境前装好或走 APK 侧载。
- 🔴 **Apple 5.4 是硬门槛**（页面更新日 2026-06-08，高 · [App Review Guidelines](https://developer.apple.com/app-store/review/guidelines/)）：
  > "Apps offering VPN services must utilize the NEVPNManager API and **may only be offered by developers enrolled as an organization**."

  需**法律实体**（单成员 LLC 可以，DBA / 个体户会被拒）+ **D-U-N-S 编号** + 公开官网 + 同域名工作邮箱。$99/年。
  **D-U-N-S 是最长的一根杆子**（Apple 说 5 个工作日，D&B 实际可能到 ~28 天）。
  权限本身不是瓶颈：Network Extensions entitlement 在 Xcode 里是自助开关，无审批（高）。
- **Google Play**：VpnService 声明表需提交两段各 ≤90 秒视频，且 "subject to Google's approval"（高）。
  显著披露必须是**独立弹窗**，不能与其它数据披露合并。
  🔴 唯一红线：**"Apps that facilitate proxy services to third parties"** 只能在那是核心用途时允许 ——
  **绝不能让用户设备成为他人流量的出口节点**。
- **抽成**：Apple Small Business Program **15%**（年 proceeds < $1M，我们必然符合）；
  美区外链购买当前 **0%**，但 2026-08-13 Apple 已提议 15% / 10% / **5%（SBP）**，**按 5% 建模不要按 0%**（高 · MacRumors）。
  EU：**2026-10-01 起每次安装 €0.50 的 Core Technology Fee 取消**，改为对店外数字交易收 5%（高 · Apple DMA 页）。
  Google Play 自 **2026-06-30**（美 / EEA / 英）：Play Billing **15%** / 替代计费 **6%** / 外链 **10%** —— **订阅上明显比 Apple 便宜**（高）。

### 9.10 支付：没有人禁止 VPN 这门技术，他们禁止的是「解锁」这门生意

> 本节由第二轮专项调研补全（约 100 次抓取，条款页均为直接拉取原文后 grep，非搜索摘要）。

#### 9.10.1 条款原文（全部实际抓取）

| 平台 | VPN 是否列名 | 原文 / 判定 |
|---|---|---|
| **Stripe 直连** | 🔴 **完全未提** | 受限清单（页面自记「Last updated 2026-05-13」）全文 28k 字符里 **"VPN" / "anonymiz" / "proxy" / "circumvent" / "unblock" 零命中**。唯一电信相关项是「Telecommunications manipulation equipment, including jamming devices」——指硬件干扰器。**高危法域只列古巴/伊朗/朝鲜/叙利亚/克里米亚/顿涅茨克/卢甘斯克，中国不在其中** |
| **Paddle** | ✅ 明确「受限」 | 「System Health Products… **VPN and Proxies (Restricted Category)**」，页面自记 Last Updated 2026-04-13。Restricted 的定义是**加强尽调**，不是拒绝 |
| **Polar** | ✅ 受限，但**相邻条款是禁止** | 受限含「VPN, VPS & VDS services」；⚠️ 禁止清单里有三条能被套用：**「API and IP cloaking services」「Telecommunication and eSIM Services」「Services to circumvent the rules, paywalls or terms of other services」** |
| **Creem** | ❌ 未提 VPN | 但禁止「Telecommunications and connectivity products such as eSIMs, SIM cards, mobile data plans… or internet service providers」。受限品类要求**「An established and proven track record is required」**并索取前一家支付商的拒付率 |
| **PayPro Global** | ✅ **写得最露骨，但它禁的是目的** | Exhibit B 原文：禁止「enabling consumers to **circumvent**… geographic or IP-based restrictions, **including through usage of VPN, proxy or anonymous user facilities**, or to gain access to… content for which the user has not expressly paid」。**而 PayPro Global 同时是 AdGuard VPN 的 MoR** |
| **Lemon Squeezy** | 未提 | 2024 年被 Stripe 收购；其团队 2026 年文章标题即「Why Stripe Managed Payments is the future」，明写「Our goal is to provide Lemon Squeezy users an easy way to migrate」。**仍开放注册，但不要在 2026 年基于它建设** |
| **Stripe Managed Payments** | 未提 | ✅ **该产品默认就在平台层禁止中国大陆买家**（受限买家国家列表含 China）。**我们的「不卖给中国」不是让步，是这个产品的默认姿态** |
| FastSpring / Gumroad / 2Checkout | — | **找不到条款来源**（页面 404 / 空）。但 Proxifier 跑在 2Checkout 上（实证） |

🔴 **本节最重要的一句**：
**没有任何一家禁止 VPN 这门技术，他们禁止的是「帮用户绕过访问控制去拿没付钱的内容」。**
PayPro Global 把 VPN 写成**手段**而不是**品类**，同时又给 AdGuard VPN 做 MoR —— 这就是证据。

#### 9.10.2 具名实证：VPN 与代理产品今天就跑在这些通道上

| 产品 | 通道 | 证据 |
|---|---|---|
| **AdGuard VPN** | **Paddle + PayPro Global**，自 **2015 年** | 高 · AdGuard 销售条款原文 + [Paddle 官方案例页](https://www.paddle.com/customers/how-adguard-scaled-to-150-million-global-users) |
| **Decodo（原 Smartproxy）** | **Paddle** | 高 · 其 pricing 页 FAQ 原文「All orders are processed by our online reseller **Paddle**… Merchant of Record」。**住宅代理比隐私 VPN 更难的品类** |
| **Windscribe** | 🔴 **Stripe 直连** + CoinPayments | 高 · 升级页实测加载 `js.stripe.com/v3/`，页面标题「Buy VPN with Crypto, PayPal, or Credit Card」。**这是「Stripe 一律封 VPN」这个说法最直接的反证** |
| Charles Proxy | Paddle | 高 |
| Proxifier | 2Checkout | 高 |
| IVPN | **自建 BTCPay + 自托管 Monero 节点** + 卡（**不收 Amex、不收预付卡/礼品卡**） | 高 · 其官方仓库原文 |
| Mullvad | 卡 + 现金 + 四种加密货币（**加密货币 −10%**）；现金与加密**不在 14 天退款保证内** | 高 |
| Astrill | 直连商户，收银联/微信/支付宝/比特币；**"All Astrill service sales are final and no refunds are possible."** | 高 |

#### 9.10.3 🔴 拒付：在我们这个规模上，触发线是「一个月 5 笔」，不是 1.5%

Visa **VAMP** 自 2025-06-01 生效，比率 = （TC40 欺诈 + 全部 TC15 争议）÷ 结算笔数，**仅 CNP**。
公开 fact sheet 强调的是 Excessive 档，但那一档**同时要求月争议数 ≥ 1,500** —— 在我们的规模上是噪音。

**真正适用我们的是 Stripe 文档里公开的 Non-Compliant 档**：

| 档位 | 笔数门槛 | 比率门槛 |
|---|---|---|
| **Non-Compliant** | 🔴 **5 笔** | 🔴 **0.5%** |
| Excessive（AP/加/欧/美） | 1,500 笔 | 2.2% → **2026-04-01 起 1.5%** |

Stripe 原文：「Visa assesses fees for merchants exceeding the Excessive threshold, and **may also assess fees for merchants exceeding the Non-Compliant threshold**」。
另有两条放大器：**Early Fraud Warning 即使没变成争议也计入 VAMP**；**同一笔交易若同时出现在 TC40 与 TC15，计两次**。

**Mastercard ECM**：100–299 笔且 1.5–2.99% 起罚，第 2–3 月 $1,000/月递增到第 19 月起 $100,000/月；
**分母用上月交易数**，增长期会虚高你的比率。

**各家 MoR 的私有阈值都比卡组织更严**：

| 平台 | 内部阈值 | 单笔拒付费 |
|---|---|---|
| **Paddle** | 「**Below 0.65% of transaction volume**」 | **$20**（CAD/AUD $40） |
| Polar | 对标 ~0.7%，可能**暂停账号** | $15，**无论输赢** |
| Creem | 「excessive」即可能**暂停账号** | $25 |
| Stripe 直连 | 行业线 0.75%，且有模型**提前**预测并主动联系 | $15 + $15（申诉赢了退还） |

🔴 **选 Paddle 必须先知道的两条**（其帮助页原文）：
「The system is fully automated, and **additional evidence submitted by sellers is not required or accepted**」——
**你不能为自己的争议举证**；以及「**Even if a dispute is won, it still counts towards your chargeback rate**」。

**VPN 订阅的典型拒付率：找不到可信来源。** 搜到的「4–10%」全部来自高风险收单的获客软文，无方法论，**当广告看**。

#### 9.10.4 费率（均为实拉价目页）

| 平台 | 基础 | 国际卡 | 订阅附加 | $10 单笔实际 | $60 年单实际 |
|---|---|---|---|---|---|
| **Creem** | 3.9% + $0.40 | 声称含 | 含 | **7.9%** | **4.6%** |
| **Stripe 直连** | 2.9% + $0.30 | +1.5% | +0.7% | 8.1% | 5.6% |
| **Paddle** | 5% + $0.50 | 含 | 含 | 10.0% | 5.8% |
| **Polar Starter** | 5% + $0.50 | +1.5% | 含 | 11.5% | 7.3% |
| Stripe Managed Payments | 2.9%+$0.30 **+3.5% MoR** | +1.5% | +0.7% | 11.6% | 9.1% |

⚠️ **订正**：Polar **不再是 4% + 40¢** —— 其文档原文「Organizations created on or after **May 27, 2026** start on Starter (5% + 50¢)」，4% 是老会员的祖父价，我们拿不到。

**两条结论**：

1. **Stripe 直连并不比 MoR 便宜多少**（5.6% vs Paddle 5.8%），而直连要自己承担每个法域的 VAT/GST 登记 —— 对 1–2 人团队是真实且无上界的持续成本。**MoR 的溢价在这里接近于零。**
2. 🔴 **年付比选哪家支付商更重要。** 把一个客户从「$10 × 12 次」变成「$60 × 1 次」，实际费率大约减半（固定费不再触发 12 次），
   **而且争议笔数降到 1/12** —— 在触发线是「5 笔」的前提下，这一条的分量远大于费率。

#### 9.10.5 卖家所在国

| 平台 | 中国大陆 | 香港 | 新加坡 / 台湾 |
|---|---|---|---|
| Paddle | ✅（不在其制裁清单内） | ✅ | ✅ |
| Creem | ✅（有转账限额） | ✅ | ✅ |
| Polar | ❌ | ✅ | ✅ |
| Stripe Managed Payments | ❌ | ✅ | ✅ |

#### 9.10.6 三条决定申请成败的事（比选哪家更重要）

1. 🔴 **绝不宣传「解锁」。** 每一条点名 VPN 的禁止条款针对的都是绕过访问控制，不是加密本身。
   **描述成 "consumer privacy VPN"；站点上不出现流媒体 logo、不写 "unblock Netflix"、不写任何地区解锁文案。**
   这一个编辑决定，决定你落在「受限-可审批」还是「禁止」。
2. **把「不卖给中国」做成技术强制而不只是条款**，并在申请里说明。
   而且可以直接引用：**Stripe 自家 MoR 默认就屏蔽中国买家** —— 我们不是在申请例外，是与最严格的一家姿态一致。
3. **按「5 笔」而不是「1.5%」做工程**：应用内一键取消、续费前提醒邮件、慷慨的按比例退款、
   **争议之前先退款**、不收 Amex、不收预付卡 / 礼品卡（照抄 IVPN）、主推年付、账单描述符要让人认得出。

### 9.11 法律：50 例样本里零例涉及外国籍

- **50 个行政处罚案例实证分析**：警告 38、罚款 12、警告+罚款 9、行政拘留 8 —— **警告与罚款占 82%**；
  适用《计算机信息网络国际联网管理暂行规定》第六条者 28/50，**罚款上限 1.5 万元**；
  作者将「单纯使用」「浏览普通境外网站」描述为执法**「极为罕见」**；**样本中零例涉及外国籍**
  （高 · [icourt.cc 转载刘洋/董佳男《拆解翻墙》](https://www.icourt.cc/prac-document/155525.html)）。
- 🔴 **《网络安全法》修正案 2026-01-01 生效**：**完全未涉及 VPN / 翻墙工具 / 跨境访问**；
  提高的是对网络运营者的罚则，并把域外管辖从「危害关键信息基础设施」**扩大到「危害我国网络安全的境外活动」**
  （高 · [Latham & Watkins](https://www.lw.com/en/insights/chinas-cybersecurity-law-amendments-increase-penalties-broaden-extraterritorial-enforcement)）。
  网上流传的「个人翻墙罚 5000 元」不被该律所分析支持；**但扩大的域外管辖条款本身，对向中国境内销售的服务商是真实、非琐碎的风险向量**。
- ⚠️ 「从没有外国人因翻墙被处罚」的最常见出处是一个挂着 NordVPN/Surfshark 联盟链接的旅游站，作者以个人见闻陈述，**无外部来源**。
  **可辩护的表述只能收窄为**：「在已公开的最大规模翻墙执法样本（50 例）中无一涉及外国籍，且作者将单纯个人使用的执法描述为极为罕见。」

### 9.12 本节找不到来源的

1. 「vpn for china」的 CPC 与月搜索量（需付费 SEO 工具）
2. r/chinalife 的具体版规（Reddit 对本环境全面封锁）
3. 外国人在华使用 VPN / eSIM / 漫游的**任何可信占比数据**（全部来自厂商营销）
4. **运营商漫游是否绕过 GFW** 的权威技术来源（§9.5，需自行实测）
5. VPN 订阅的典型拒付率；Mastercard ECP 现行阈值
6. 2024–2026 年在华外国人的官方全国存量；工作许可全国数据
7. 「去掉中国客户能否改善支付承保」的直接承保方声明（**未证实**）
