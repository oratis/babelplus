# 商店提交清单（Chrome Web Store 主推，Edge Add-ons 同步）

> 日期：2026-09-02 · 性质：**执行手册** · 状态：**未提交**（E1 未完成之前不要提交：审核员装上后连不上任何东西，会按「功能不可用」拒掉，且拒审记录会跟着开发者账号）
> 事实基线：[acquisition-channels §5.4](../../../docs/01-research/acquisition-channels.md)（商店政策，2026-09-02 实查）；[go-to-market-plan 裁决 6](../../../docs/03-product/go-to-market-plan.md)（文案红线）
> 关联：[listing.en.md](listing.en.md)（商店文案与权限说明）、[privacy-policy.md](privacy-policy.md)、[../README.md](../README.md) §6（门）

## 0 · 文案红线（两家共用，改文案前先读）

**一律自称 consumer privacy / private access；绝不出现 unblock、解锁、bypass、流媒体品牌 logo、eSIM、电信字样。**
这一个编辑决定决定支付通道把我们放进「受限-可审批」还是「禁止」（go-to-market-plan §4.4），也决定商店审核把我们归到哪一类。

## 1 · Chrome Web Store

| 步骤 | 内容 | 状态 |
|---|---|---|
| 账号 | 开发者注册 $5 一次性；**用组织邮箱**（与 Apple 5.4 / D-U-N-S 同一个法律实体，see spec §7.2） | ☐ 需用户 |
| 包 | `pnpm build && pnpm package` → `babelplus-extension-<version>.zip`；manifest v3，`minimum_chrome_version: 108` | ✅ 脚本就绪 |
| 商品详情 | 名称 ≤ 45 字、简介 ≤ 132 字、详细描述 —— 全部在 [listing.en.md](listing.en.md) | ✅ 英文；中文未写 |
| 图标 | 128×128 PNG（`public/icons/icon128.png`） | ✅ |
| 截图 | 1280×800 或 640×400，≥ 1 张，建议 5 张（八个状态里挑：Off / Connected / Running low / No route / Options） | ☐ 需真机 |
| 宣传图 | 小图 440×280 必填 | ☐ |
| 类别 | Productivity → Workflow & Planning（VPN 类没有专属类目；**不选 Featured 申请**，VPN 类不会被 Featured） | ☐ |
| 隐私 | 单一用途声明 + 每个权限的理由（listing.en.md §3）+ 「不收集用户数据」的数据使用披露 + 隐私政策 URL（privacy-policy.md 须托管在我们的域名上） | ✅ 文案；☐ URL |
| 远程代码 | 声明「不使用远程代码」：PAC 是**数据**不是代码（`pacScript.data` 由 Chrome 的 PAC 解析器执行，不是扩展页面里的 JS），但审核员可能追问 —— listing.en.md §3 有一段现成的解释 | ✅ |
| 审核 | 24–72 h；每账号默认 2 个扩展槽位（2026-08-20 新规） | ☐ |

## 2 · Edge Add-ons

| 步骤 | 内容 | 状态 |
|---|---|---|
| 账号 | Partner Center 注册，免费 | ☐ 需用户 |
| 包 | 同一个 zip（Edge 支持同一份 MV3 manifest） | ✅ |
| 详情 | 同 CWS 文案；Edge 政策 1.8 允许第三方支付卖订阅，扩展内本来就不收款（跳官网） | ✅ |
| 审核 | ≤ 7 个工作日；**大陆可直接访问** —— 对已在中国的用户是唯一能一键安装的官方入口 | ☐ |

## 3 · 提交前必须为真的事（逐条打勾，任一不成立就别点提交）

- [ ] `GET /api/v1/user/proxy-config` 已不是 501（`unimplemented_test.go` 里那一行已删）
- [ ] 至少一个 HTTPS 入站在线，且 E0 的 100 MB 在 `stat_user_server` 里查得到
- [ ] 在一台干净的 Chrome 与一台干净的 Edge 上从零安装 → 登录 → 连接 → 打开 Google，各一次
- [ ] 隐私政策已托管在 `web.babel.plus`（或域名池里的域名）并与实现逐条对上
- [ ] 商店文案里 grep 不到 `unblock` / `解锁` / `bypass` / `Netflix` / `eSIM`
- [ ] `pnpm --filter @babelplus/extension test` 全绿，`pnpm lint:no-external` 通过
- [ ] 版本号：`package.json` 与 `public/manifest.json` 一致（`package.mjs` 会拦）
