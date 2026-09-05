# 设计系统「控制台」：五个界面一套色板、等宽字承载数据、状态用灯不用字

> 日期：2026-09-05 · 性质：**设计方案** · 状态：**设计稿 v1，已落地到代码**（2026-09-05；官网 / 用户面板 / 后台三套已按此重做并通过全部前端用例，扩展与桌面端只换了色板）
> 事实基线：2026-09-05 对三套前端的真实截图评审（本机 headless Chrome + `web/scripts/mock-api.mjs` 假后端，27 张基线、27 张改版后）；
> 约束来自 [ADR 0003 §5](../05-adr/0003-web-hosting-and-reachability.md)（零外部资源）与 [page-inventory §2.2–2.3](page-inventory.md)（三态、M1/M2/M3）
> 关联：[page-inventory.md](page-inventory.md)（§8 曾登记「视觉设计与组件库未定」——本文关掉视觉这一半）、
> [client-products-spec.md](client-products-spec.md) §3.5 / §4.3（扩展与浏览器的信息架构，本文不改）、
> [`web/shared/src/ui/tokens.css`](../../web/shared/src/ui/tokens.css)（唯一色板）、[`web/shared/src/ui/primitives.tsx`](../../web/shared/src/ui/primitives.tsx)（元件）
> 读者：改任何一个界面的人。**改颜色先改 tokens.css；加元件先看 §3 有没有。**

---

## 1 · 结论

**一套色板、两种字、四个签名元件，五个界面共用。** 视觉方向是「控制台」：深色优先、单一青色强调、
所有「数据」（IP、字节、日期、ID、小标、编号）走等宽栈，状态用 LED 点表达而不是用形容词。
它要回答的产品问题只有一个：**用户在最需要判断「我还剩多少、现在连没连上」的那一刻，一眼能看到数字与灯。**

改版前的三个问题（2026-09-05 基线截图，[§6](#6--改版前后)）：

| 问题 | 表现 | 处置 |
|---|---|---|
| 三套视觉语言 | 官网仿 openai 发布页（近黑纯白）、面板/后台是通用 SaaS 蓝（oklch）、扩展/桌面是纸墨色板（hex） | 全部指到 `tokens.css` 一份值；官网 / 扩展 / 桌面各留一份**注明来源**的副本（它们不能 import） |
| 数字没有层级 | 剩余流量是一条 2px 细线，「17 天」是一行小字；后台看板五个数字与解释文字同一号 | `Stat` 大数字（等宽、tabular、单位缩小）成为仪表盘与看板的主视觉；`Meter` 90% 转警告色 |
| 工程注释漏进用户界面 | 用户面板导航挂着 P2 标签，卡片标题旁写「结构照抄竞品，四个目标全改」 | 用户面板导航不再显示优先级；后台保留 M2 / P2（读者是运维）。卡片 hint 文案本轮未动（见 §8） |

---

## 2 · 令牌（`web/shared/src/ui/tokens.css`）

| 令牌 | 深色（基准） | 浅色 | 用途 |
|---|---|---|---|
| `--bp-bg` / `--bp-surface` / `--bp-surface-alt` | `#0a0e13` / `#10161d` / `#161e27` | `#f3f5f7` / `#ffffff` / `#ecf0f3` | 页面底 / 卡片 / 卡片内次级面 |
| `--bp-line` / `--bp-line-strong` | `#202b37` / `#2f3c4a` | `#d5dde5` / `#b6c2ce` | 细线；悬停边 |
| `--bp-fg` / `--bp-fg-muted` / `--bp-fg-subtle` | `#e7edf3` / `#98a8b8` / `#667889` | `#0f1721` / `#4e6072` / `#7b8a99` | 正文 / 说明 / 小标 |
| `--bp-accent` / `-strong` / `-fg` / `-soft` | `#38d2e6` / `#6be1f0` / `#04161a` / 12% | `#0b7c8c` / `#096878` / `#fff` / 10% | **唯一强调色**：主按钮、当前导航、链接、进度 |
| `--bp-ok` / `--bp-warn` / `--bp-danger` | `#3cd67f` / `#f0b24a` / `#f26b60` | `#1e8a55` / `#9a6700` / `#c33a2c` | 语义色，只用于状态，不当强调色用 |
| `--bp-grid` | 4.5% 白 | 6% 黑 | 细网格，只铺页首与认证页 |
| `--bp-radius` / `-sm` | 8 / 5 px | 同 | 卡片 / 徽标 |

字体：**零 webfont**。无衬线走系统栈（PingFang / Segoe / Noto CJK），等宽走 `ui-monospace, 'SF Mono', Menlo, 'Cascadia Mono', Consolas`。
「极客感」靠字重、字距（小标 0.12–0.14em）、等宽数字（`tabular-nums`）与 LED，不靠下载一套字体 —— 理由见 §7 代价 1。

Tailwind 应用（user / admin）用 `@theme inline` 把 `--color-*` 指到 `--bp-*`，工具类产出 `var()`，深浅切换由 tokens.css 的 media query 统一完成；
两份 `styles.css` 里不再有任何色值。

---

## 3 · 元件（`web/shared/src/ui/primitives.tsx`）

既有导出**签名不变**（`Card` / `CardTitle` / `PageHeader` / `Button` / `LinkButton` / `Badge` / `PriorityBadge` / `NotWiredNotice`），
页面与 691 个前端用例没有因为改版而改动。新增四个签名元件：

| 元件 | 长什么样 | 用在哪 | 不用在哪 |
|---|---|---|---|
| `Eyebrow` | 等宽、11px、0.12em 字距、大写、次级色 | 页面标题上方、导航分组、`Stat` 的标签、页脚「Recovery」 | 正文里 |
| `Led` | 7px 圆点 + 同色微辉光；`wait` 呼吸 | `Badge` 的 ok / warn / danger / info 自动带；品牌块旁表示会话状态 | 装饰 |
| `Stat` | 大数字 28px 等宽 + 缩小的单位 + 一行 hint | 仪表盘（剩余流量 / 剩余天数 / 设备）、后台看板 | 列表行内 |
| `Meter` | 6px 轨道；≥ 90% 转 warn、100% 转 danger | 流量进度、配额条 | 表示「进行中」的动画（那是骨架的事） |

其余变化：`Card` 加了阴影令牌与 8px 圆角；`CardTitle` 加了一条底线、标题 13px、hint 走等宽；
`Badge` 5px 圆角 + 11px；`Button` 6px 圆角、主按钮带阴影；`NotWiredNotice` 前缀改等宽字但文案仍是「尚未接线。」（有用例钉它）。

---

## 4 · 各界面的规则

### 4.1 共同

- **网格只出现在页首与认证页**（`.bp-grid-bg` / 官网 `.hero-wrap`），正文不铺——铺满就成了壁纸。
- 每个页面最多一个 `primary` 按钮；到期 / 用完时「续费」接管主按钮（user-journey §4.4，逻辑早就在，改版没动）。
- 语义色（ok / warn / danger）只表示状态；强调色不表示状态。
- 三态（骨架 / 空态 / 错误态）的实现与文案一字未动（page-inventory §2.2 是产品规则，不是视觉）。

### 4.2 用户面板

- 左侧导轨：品牌块（青色方块 + `console` 小标 + 绿灯）→ `Service`（概览 / 订阅与设备 / 套餐 / 订单 / 工单）→ `Account`（其余）。
  当前项：左侧 2px 竖线 + 强调色底。**不显示 P1/P2**。
- 仪表盘订阅卡：三个 `Stat` 并排（剩余流量 / 剩余天数 / 设备），下方 `Meter` + 百分比，再下方是「重置日 = 下单日」那句话。
  去掉了与大数字重复的「到期」一行。
- 认证页：网格底 + 居中品牌块 + 卡片；页脚常驻备用域名（ADR 0003 §5）。

### 4.3 后台

- 全宽控制台，导轨按四个职能分组（`Ops` / `Catalog` / `Infra` / `System`，`nav.ts` 加了 `group` 字段，顺序与编号不变）。
- 导航项右侧保留 `M2` / `P2` 等宽小标 —— 读者是运维。
- 表格：表头等宽小标（11px / 0.08em），数字列 `tabular-nums`，行悬停浅底；**列宽、排序、分页一律未动**。

### 4.4 官网

- 深色优先；首屏铺网格并向下淡出；`// PRODUCT` 等宽小标；超大紧字距标题；CTA 是 6px 圆角的实心青色 + 描边幽灵按钮。
- CTA 下一行「读数条」：两个客户端的平台清单（`macOS · Windows`、`Chrome · Edge`），文案来自 `content.ts`，**没有新增一个字**（`content.test.ts` 的红线继续成立）。
- 章节自动编号 `01`–`05`（CSS counter，零 JS），细线分隔；两个客户端做成两张细线卡；价目表等宽数字；「还没有」提示带琥珀灯。
- 仍然是**零 JavaScript 的静态页**，产物 12.1 KB HTML + 9.7 KB CSS。

### 4.5 扩展与桌面浏览器

只换色板（`base.css` / `style.css` 的 `:root` 与深色块），类名、信息架构（client-products-spec §3.5 / §4.3）一字未动。
它们的 LED（`.led--on` 等）与面板的 `Led` 现在是同一组色。**未在真机上看过**（§8）。

---

## 5 · 怎么改

```bash
# 改颜色：只改这一处，再同步三份副本（site/style.css、extension/ui/base.css、desktop/renderer/style.css 的 :root）
$EDITOR web/shared/src/ui/tokens.css

# 看效果（不需要真后端）：三个 dev server + 两个 mock 代理
cd web && pnpm dev:site & pnpm dev:user & pnpm dev:admin &
node scripts/mock-api.mjs --mode user  --port 5178 --upstream http://localhost:5177   # 打开 5178，任意邮箱密码登录
node scripts/mock-api.mjs --mode admin --port 5179 --upstream http://localhost:5174   # 打开 5179，直接进后台

# 上线前三道闸一条不少
pnpm -r typecheck && pnpm -r test && pnpm -r build && pnpm run lint:no-external
```

---

## 6 · 改版前后

截图（2026-09-05，本机 headless Chrome，mock 数据）：用户面板仪表盘、后台看板、官网首页各留深浅两色与手机宽度；
评审页 [artifact](https://claude.ai/code/artifact/) 里有并排对照。**mock 里的数字是编的，只用于看版式。**

---

## 7 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **没有 webfont，所以「等宽」在不同机器上长得不一样。** macOS 是 SF Mono，Windows 是 Cascadia / Consolas，安卓可能退到 Droid Sans Mono。
>    这是刻意的：ADR 0003 §5 要求自托管，而大陆跨境链路每天有 5 小时以上低于 1 Mbps（§4），一套子集化中文 webfont 仍有数百 KB。
>    **如果哪天要上 webfont，只准 `public/fonts/` 自托管 + 子集化 + `font-display: swap`，且先量首屏体积。**
> 2. **色板有四份副本。** `tokens.css` 是事实源，官网 / 扩展 / 桌面各自复制了一份（它们不能 import 那份文件：官网零 JS 单文件 CSS，扩展与桌面有 CSP）。
>    代价是改色要改四处，而没有任何检查在守它。
> 3. **`color-mix()` 与 `shadow-(--var)`** 要求较新的浏览器（Chrome 111+ / Safari 16.2+）。扩展的目标是 Chrome 108（vite 配置），
>    `base.css` 里没用 `color-mix`；面板与后台用了。老浏览器上 LED 的辉光会消失，不影响功能。
> 4. **深色优先意味着浅色是「第二次设计」。** 浅色令牌逐一对应但只在截图里看过两页（官网首页、仪表盘），其余页面的浅色没有逐页看。
> 5. **仪表盘去掉了「到期」一行**，同一信息现在只在 `Stat` 里出现一次。如果有人靠那一行的文字做过截图教程，教程要更新。
> 6. **改版没有碰文案**，所以「结构照抄竞品，四个目标全改」这类卡片 hint 仍然出现在用户面板里 —— 它们是产品内容的问题，不是视觉的问题，
>    但视觉改完之后它们更显眼了。

## 8 · 这次没有解决的

- [ ] 🔴 **扩展 popup 与桌面浏览器 chrome 没有在真机上看过**：扩展页面依赖 `chrome.*`，普通标签页里渲染不出；桌面端要 Electron。只换了色板，形态与间距未动。
- [ ] **用户面板的卡片 hint 里有工程注释**（§7 代价 6）—— 该由产品文案裁决删哪些，不在视觉改版范围。
- [ ] **浅色主题只逐页看了两页。**
- [ ] **组件库仍未选**（page-inventory §8、web/README §7 代价 5）：危险操作要的可访问确认对话框还是没有。本文只解决视觉，不解决交互组件。
- [ ] **色板副本没有一致性检查**（§7 代价 2）。一个 20 行的脚本就能比对四处 `:root`，本轮没写。
- [ ] `mock-api.mjs` 只覆盖看版式需要的端点，其余回 501；它不是契约测试，不要拿它当后端行为的依据。
