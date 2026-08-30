# web/ —— 两个 SPA：登录态与路由守卫已接线，20 条路由 + 17 个后台模块里绝大多数仍是空壳

> 日期：2026-08-17（**2026-08-30 改写 H1 与本引用块**，理由见下方「⚠️ 状态订正」） · 性质：**机制说明** ·
> 状态：**执行中**（2026-08-30）—— 登录态、路由守卫与三页已接线；其余页面仍是空壳。
> （性质与状态两个词都取自 [docs/README §2.1 / §2.2](../docs/README.md) 的受控词表：
> 原写「实现（脚手架）」与「已构建通过，业务逻辑为零」，两个都不在词表里。）
> 事实基线：类型由 `openapi/openapi.yaml`（OpenAPI 3.1，128 个 operation）生成；
> 页面清单与三态规则来自 [page-inventory.md](../docs/03-product/page-inventory.md)；
> 可达性约束来自 [ADR 0003](../docs/05-adr/0003-web-hosting-and-reachability.md)；
> 接线与测试的实况见本文 §8、§9，数字于 2026-08-30 用 `pnpm -r test` 与
> `grep -rho "TODO(P1)" web` 复核
> 关联：[user-journey.md](../docs/03-product/user-journey.md)、[api-contract.md](../docs/02-architecture/api-contract.md)
> 读者：接手写前端的人。本文说明**目录怎么分、为什么这么分、哪些东西不能动**。
>
> ⚠️ **状态订正（2026-08-30）。** 原 H1 是「全部是可导航的空壳」，原状态是「业务逻辑为零」，
> 而**本文自己的 §8 与 §9 早已推翻了这句话**：2026-08-23 起有
> `AuthProvider` + `RequireAuth` 三态守卫、16 条受保护路由整段在守卫之下、
> `shared/api/session.ts` 的 refresh 单飞，以及 **108 个前端用例 / 7 个文件**
> （`pnpm -r test` 2026-08-30 复跑：shared 67 + user 33 + admin 8，全绿）；
> 2026-08-29 又接线了 `DashboardPage` 与 `TicketListPage` 两页。
> **一份自称「业务逻辑为零」的文档，正下方列着 108 个测试和一套会话实现** —— 这里改掉的是那个矛盾。
>
> ⚠️ **但「绝大多数仍是空壳」是真的，不要读成「前端做完了」**：
> `web/` 下仍有 **44 处 `TODO(P1)` 散在 30 个文件里**（2026-08-30 实数），
> 47 个路由组件里只有 `/auth/login`、`/dashboard`、`/ticket` 三页真正调过 API；
> **后台 17 个模块一页都没接线**，而且接不了 —— 61 个 `/admin/*` operation
> 在 `api/cmd/server/authmap.go` 里仍是 501 fail-closed（口径：master `a4604c9396f`）。
> **2026-08-30 同日更新**：另一条并行工作流已在**未提交的工作树**里把 `authmap.go`
> 接上 `mw.AuthenticateAdmin` / `mw.AuthenticateInternal` 并补了 5 个测试文件。
> **未提交，所以本文仍按 HEAD 描述**；且**接线 ≠ 端点可用** ——
> 61 个 admin handler 绝大多数仍是 `Unimplemented` 的 501，后台照样接不了线。
> 见 [roadmap B48](../docs/00-overview/roadmap.md)。
> 每一页都有布局、标题、加载 / 空 / 错误三态占位；未接线处一律标 `TODO(P1)`，
> **没有一处假装实现，也没有一处假数据**（这一条从第一天起就没变过）。

---

## 1 · 目录

```
web/
├── shared/          @babelplus/shared —— 源码直出，无构建物
│   ├── api/
│   │   ├── schema.d.ts   ← openapi-typescript 生成，**提交进仓库**
│   │   ├── client.ts     信封拆封 + 超时 + 备用域名故障转移 + 五类错误归一
│   │   └── queries.ts    一小组已接线的只读查询（让生成物真的过一遍类型检查）
│   └── src/
│       ├── lib/          运行时配置、格式化、错误分类、三态解析
│       └── ui/           三态组件、备用域名列表、页脚、内联 SVG 图标
├── user/            @babelplus/user  —— 用户面板，产物 user/dist
├── admin/           @babelplus/admin —— 后台，产物 admin/dist
└── scripts/         构建产物的第三方资源检查
```

**产物必须分开，这不是组织习惯是安全边界。** 后台要独立主域名 + IP 白名单 / IAP + 强制 TOTP
（page-inventory §4.1 的三道闸）。与用户面板同源部署会让三道闸同时失效，
且用户面板的任何一个 XSS 都会直接变成后台失守。

## 2 · 命令

| 命令 | 作用 |
|---|---|
| `pnpm install` | 装依赖（workspace 根目录是 `web/`） |
| `pnpm gen:api` | 从 `../openapi/openapi.yaml` 生成 `shared/api/schema.d.ts` |
| `pnpm gen:api:check` | 生成后 `git diff --exit-code`，本地自查漂移 |
| `pnpm -r build` | 两个 SPA 各自 `tsc --noEmit` + `vite build` |
| `pnpm -r typecheck` | 只类型检查，不产出 |
| `pnpm -r test` | 三个包各自跑 `vitest run`（见 §9） |
| `pnpm lint:no-external` | 扫描 `dist`，确认没有任何第三方主机名（见 §5） |
| `pnpm dev:user` / `pnpm dev:admin` | 5173 / 5174 |

生成物 `shared/api/schema.d.ts` **必须提交**。它不在 `.gitignore` 的忽略范围内，
和 `api/internal/gen/`、`api/db/gen/` 是同一条纪律。

`.github/workflows/ci.yml` 的 `contract-drift` 作业会在 `web/` 里跑
`pnpm install --frozen-lockfile && pnpm run gen:api`，然后 `git add web/shared/api` 再比 index
（这样连「新增了生成文件但忘了 git add」也盖得住）；
`web` 作业跑 `pnpm -r build`、`pnpm -r typecheck`、`pnpm -r test`、`pnpm run --if-present lint:no-external`。
**改这四个脚本名要同步改那个文件。**
它还有一步 `grep` 确认 `web/` 下至少有一个包声明了 `build` / `typecheck` / `test` ——
`pnpm -r run <script>` 对没有该脚本的包是**静默跳过**的，
删掉一个包的 `test` 脚本之后 `pnpm -r test` 仍然退出 0。

## 3 · API 客户端

`openapi-typescript` 7.13.0 生成类型（**7.x 完整支持 OpenAPI 3.1**，6.x 不行），
`openapi-fetch` 0.17 做调用。契约是全仓事实源，前端这边只补生成器给不了的四件事：

1. **统一信封的拆封。** user / admin 两面是 `{data, meta}` / `{error, meta}`，
   而 openapi-fetch 的返回也叫 `data` —— `unwrap()` 负责这层歧义。
2. **超时 + 备用域名故障转移。** 默认超时 15 秒（page-inventory §2.2 的**提案值，需实测**
   按晚高峰 P95 校准），超时后向备用域名重试**一次**。
   🔴 **只有 GET / HEAD / OPTIONS 会故障转移** —— POST 换个域名重发一次就是重复下单。
   这条限制写死在客户端里，不指望每个调用点自觉。
3. **五类错误归一**（401 / 403 / 4xx / 5xx / 网络不可达）。
   page-inventory §2.2 的原话：把「你的账号过期了」和「我们的服务器挂了」显示成同一句话，
   会直接变成工单。
4. **401 → 静默 refresh 一次 → 重放原请求一次**。单飞在 `shared/api/session.ts`。

### 3.1 重放为什么对 POST 是安全的，而故障转移不是

两件事看起来都是「重发一次」，风险完全不同，混为一谈会得出错误结论：

- **故障转移**发生在「**没收到响应**」之后。请求可能已经到达服务端并被完整处理，
  只是响应在回程丢了。换域名重发 = 可能重复下单。**所以只对 GET / HEAD / OPTIONS 做。**
- **401 后重放**发生在「收到了一个明确的 401」之后。这个 401 由鉴权中间件产生，
  它在 handler 之前返回（`api/internal/middleware/user.go` 的 `RequireUser`：
  鉴权失败直接写响应，`next.ServeHTTP` 根本不会被调用）。**服务端没有执行任何业务逻辑。**
  换上新 token 重放一次不会产生第二笔副作用。

### 3.2 会话：以后端实际行为为准，不是以契约文字为准

读 `api/internal/handler/auth.go` 与 `api/internal/middleware/user.go` 之后确认的三条，
它们直接决定了前端怎么写：

| 契约（openapi / api-contract §5）怎么说 | 后端**实际**怎么做 | 前端因此怎么写 |
|---|---|---|
| access JWT 15 分钟 + refresh 30 天，两枚 | `sessionTokens()` 里 `access_token` 与 `refresh_token` **是同一个值**（一枚 30 天的不透明会话 token；DB 里没有任何 JWT 载体） | 只存**一枚** token，refresh 时把它当 `refresh_token` 发出去 |
| 401 `AUTH_TOKEN_EXPIRED` 触发前端静默 refresh | `middleware/user.go` 的 `authFailure()` **一律返回 `AUTH_TOKEN_INVALID`**，`AUTH_TOKEN_EXPIRED` 目前没有任何代码路径会产生 | 两个码都触发 refresh（`REFRESHABLE_AUTH_CODES`）。只认前者的话，这条路径**一次都不会被走到** |
| refresh 一次性轮换 | 属实，且**旧 token 在 refresh 成功的那一刻立即失效**；复用检测还没做（`RefreshToken` 的 `TODO(P2)`） | 单飞不是优化是正确性；且要处理「在途请求手里是旧 token」的时序（`ensureFreshToken` 的 `staleToken` 参数） |

**没做**：请求缓存层、全局 store。组件库与状态管理的选型还没裁决
（page-inventory §8「视觉设计与组件库未定」），现在选等于替以后的人做决定。

## 4 · 三态是结构，不是约定

`EmptyState` 的 `action` 是**必填 prop**。page-inventory §2.2 写着
「空态必须给出下一步动作按钮，禁止只显示『暂无数据』」——
做成类型约束之后，漏掉空态编译就不过，不用靠评审时的记忆力。

同理，用户面板的 `RouteScaffold` 与后台的 `ModuleScaffold` 都把 `empty` 列为必填。

**看这三态**：任何页面地址后加

- `?state=loading` —— 骨架屏（**不用居中 spinner**；3 秒后自动插入慢加载提示条）
- `?state=empty` —— 空态
- `?state=error&error=offline` —— 五类错误任选（`unauthorized` / `forbidden` / `client` / `server` / `offline`）

这是**脚手架期的临时开关**（`resolveShellState`），接线时删掉，页面结构一行不用改。

## 5 · 大陆可达性约束在代码里的落点

| ADR 0003 的要求 | 落在哪 |
|---|---|
| 字体、图标一切外部资源自托管 | `styles.css` 用**系统字体栈**（连自己的字体文件都不下）；图标是 `ui/icons.tsx` 里的内联 SVG；favicon 是 `data:` URI |
| 不用 Cloudflare Turnstile | `components/AuthForm.tsx` 文件头写明了「没有验证码组件是设计结论不是遗漏」；`scripts/check-no-external-assets.mjs` 把 `challenges.cloudflare.com` 列为硬失败 |
| 不依赖 Cloudflare DoH | 同上，`cloudflare-dns.com` / `one.one.one.one` 列为硬失败 |
| 页脚常驻备用域名列表，内容从运行时配置读 | `ui/footer.tsx` 挂在**布局层**（想漏都漏不掉）；数据来自 `window.__BP_RUNTIME_CONFIG__` |
| 一键新增镜像域名，不重新构建 | `public/runtime-config.js`，`index.html` 里**同步加载**，部署时只覆盖这一个文件 |
| `web/user` 移动端适配 | M1 移动优先：抽屉导航、`min-h-11`（44px 可点区）、输入框 16px 起（否则 iOS 会放大页面导致横向滚动）、`body { overflow-x: hidden }` |

`pnpm lint:no-external` 有三层检查（点名主机名 / HTML 与 CSS 的取用位置 / 其余绝对 URL 需登记理由）。
第三层的白名单里**每一条都要写清楚为什么它不会被请求** ——
写不出理由的那一条，通常就是真的会被请求的那一条。

### 运行时配置的部署要求

- `runtime-config.js` 必须以 `Cache-Control: no-cache` 下发。
  改了域名池而用户拿的是旧缓存，等于白改 —— 而且恰好在最需要它的时候失效。
- 它与 `index.html` 同源同域，**不要放 CDN**。
- 后台的这份配置里**没有 mirrorDomains**：后台的大陆可达性要求是「不要求」，
  多一个镜像就是多一个要防护的入口。
- 🔴 这个文件是公开可读的静态资源，**不能放任何凭据或内部主机名**。

### 静态托管的部署要求

两个 SPA 都用 `BrowserRouter`，所以托管必须配 **SPA fallback（一切未匹配路径回 `index.html`）**。
不配的话，用户刷新 `/order/xxx` 会拿到平台的默认 404 页 ——
而那个页面上没有备用域名列表，恰好在用户已经迷路的时候。

## 6 · 版本选择说明

| 包 | 版本 | 说明 |
|---|---|---|
| `openapi-typescript` | 7.13.0 | **7.x 才完整支持 OpenAPI 3.1**，这是硬要求 |
| `vite` | 8.2.1 | 打包器是 Rolldown（不再是 Rollup + esbuild） |
| `react` | 19.2.8 | — |
| `react-router` | 7.18.2 | 用 `react-router` 而不是 `react-router-dom`（v7 后者只是 shim） |
| `tailwindcss` | 4.3.3 | CSS-first 配置，`@theme` + `@source` |
| `typescript` | **5.9.3** | ⚠️ 见下 |
| `vitest` | 4.1.11 | peer 写着 `vite: ^6 \|\| ^7 \|\| ^8`，是唯一不用降 Vite 8 就能用的运行器 |
| `jsdom` | 30.0.1 | 只有 `user` 装：路由守卫要真的渲染出来才能验证「加载态不误跳」 |
| `@testing-library/react` | 16.3.2 | peer 支持 React 19；只有 `user` 装 |

**TypeScript 钉在 5.9.3 而不是当前 latest 的 7.0.2**：7.0 是 Go 重写的原生编译器，
它与 Vite / React 类型定义 / Tailwind 工具链的组合**在本机没有实测过**。
脚手架的价值在于「跑得通」，不在于「用最新的」。
升级 7.x 是一个独立的、需要单独验证的动作，**需实测**。

## 7 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **`shared/` 是源码直出的包，不产出构建物。** 好处是改完立刻生效、不用管构建顺序；
>    代价是两个 SPA 各自把它编译一遍，且 `shared` 的 `build` 脚本实际上只是 `tsc --noEmit`
>    —— 名不副实。如果将来有第三个消费方（比如公开站），应该改成真正产出 `dist` 的包。
> 2. **没有做路由级代码分割。** 现在每页 1–3 KB，拆成 20 个 chunk 只会把一次往返变成二十次，
>    而大陆跨境链路的成本主要在往返次数上。
>    **但这个判断会随接线而失效** —— 页面长出内容后必须重新评估。
>    2026-08-23 接上登录态与守卫之后，user 产物从 296.5 KB / gzip 93.5 KB
>    涨到 **315.4 KB / gzip 100.6 KB**（+18.9 KB / +7.1 KB gzip，本机实测），
>    而这只是**一页**（`/auth/login`）真正接线的代价。
>    剩下 19 页接完，越过 `chunkSizeWarningLimit` 的 400 KB 几乎是必然。
> 3. **`?state=` 这个三态开关会进生产构建。** 它是可见的、可被任何人触发的调试入口。
>    它不泄漏数据（页面本来就没数据），但接线时**必须删掉**，
>    否则会变成一个能伪造「空态」「错误态」的开关。
>    ✅ `/auth/login` 接线时已经删掉了对 `?state=` 的读取（2026-08-23）；
>    其余 19 页仍在读，各自接线时逐页删。
> 4. **`_imports.ts` 这层转发是权衡。** 它让 20 个页面文件少了重复的 import 块，
>    代价是多了一层间接、IDE 的「跳转到定义」多一跳。它只做转发不加行为，
>    真觉得碍事可以整层删掉，改回直接 import。
> 5. **组件库没选。** 这里手写的 `Button` / `Card` / `Badge` 只够撑起骨架，
>    没有表单校验、没有虚拟列表、没有可访问的下拉与对话框。
>    后台的 16 条危险操作全都需要一个**真正可用的确认对话框**（焦点管理 + 键盘 + 屏幕阅读器），
>    那个组件现在不存在。这是接线前必须先解决的一块。
> 6. **`danger.ts` 把 page-inventory §4.4 的 16 条抄进了代码，于是有了两份事实源。**
>    文档改了而代码没改的风险是真实存在的。
>    换来的是「哪些按钮需要哪些防护」在代码里可检查，不靠评审记忆。
>    这笔交易我认为划算，但它确实是一笔交易。

## 8 · 这次没有解决的

- [x] ~~🔴 **登录态与路由守卫完全没做。** 现在所有页面都可直达（评审方便，上线危险）。~~
      **2026-08-23 做掉**：`user/src/lib/auth.tsx` 的 `AuthProvider` + `RequireAuth`，
      `AppLayout` 下的 16 条路由整段在守卫之下（layout route，不是逐页包 ——
      逐页包总有一天会漏掉新加的那一页，而漏掉的表现是「这一页不用登录也能看」，
      一个不会有人报 bug 的缺陷）。守卫是**三态**不是布尔值：
      「还没确定」渲染骨架**不跳转**，「确定未登录」才跳 `/auth/login?returnTo=…`，
      returnTo 走 `shared/src/lib/return-to.ts` 的白名单校验（有开放重定向向量的单测）。
      refresh 单飞在 `shared/api/session.ts`，见 §3.2 那张「契约怎么说 / 后端实际怎么做」的表。
      **后台侧没做应用层守卫** —— 61 个 `/admin/*` operation 现在全是 501 fail-closed
      （`api/cmd/server/authmap.go`），加守卫只会让 17 个模块在评审时都打不开。
- [ ] 🔴 **危险操作的确认组件不存在。** 后台 16 条 D 项现在只有清单展示，没有可用的
      `DangerAction`（确认串校验 + 必填原因 + 改前值 / 改后值提交给审计）。
      **不接受「先做功能，审计以后补」** —— 补的那天就是需要查审计的那天。
- [ ] **公开站（落地页 / 文档站 / 备用域名页 / `check.*` / `status.*`）不在本目录。**
      page-inventory §5 的 7 个页面是第三套前端，需要单独的目录与托管决策。
      其中**备用域名页**（<20 KB、无 JS、部署在每一个镜像域名上）是最后一道防线，
      优先级高于这里的很多东西。
- [x] ~~**没有测试。** 一行都没有。~~
      **2026-08-23 做掉第一批**：vitest 4.1.11，108 个用例，见 §9。
      README 点名的那条（`client.ts` 的「POST 不转移」）已经写成方法矩阵，
      改错了 CI 会指出改错的是哪一个方法。
      **仍然没有**：组件级测试（除路由守卫外）、E2E、可访问性自动检查。
- [ ] **没有 lint / format 配置。** ESLint 与 Prettier 都没装。
      与 `api/` 侧的工具链约定应该统一裁决，不该前端自己选一套。
- [ ] **组件库未选型**（page-inventory §8 的原始未决项）。
      后台倾向 Refine + shadcn，用户面板未选型；两者移动端要求差异很大（M1 vs M3），
      共用一套能省事但可能两头不讨好。
- [ ] **i18n 未做**，初期只做简体中文。中英混排的空格规则也没统一约定，
      现在是逐处手写，不一致。
- [ ] **超时 15 秒、慢提示 3 秒都是提案值，需实测。**
      应按晚高峰（19:00–24:00 CST）三网实测 P95 校准。在此之前所有超时文案都是猜的。
- [ ] **无障碍只做了最基础的一层**（`aria-busy` / `aria-live` / `role="alert"` / 可见焦点环）。
      抽屉导航没有焦点陷阱，没有跳转到主内容的链接，没做过屏幕阅读器实测。
- [ ] **`shared` 与 `api/` 的错误码没有共享事实源。**
      `ErrorCode` 从 OpenAPI 生成了类型，但前端目前没有按 code 分支的**统一**文案表 ——
      接线时会需要，且它应该由契约驱动而不是手抄。
      现在有两处按 code 分支的局部文案表（`user/src/routes/auth/LoginPage.tsx` 的
      `loginErrorCopy`、`admin/src/lib/iap.ts` 的 `forbiddenDescription`），
      **它们是手抄的**，抄漏一个码只会静默落到兜底分支。第三处出现之前应当收成一张表。

- [ ] **`AUTH_TOKEN_EXPIRED` 目前是一个没有产生方的错误码。**
      前端已经按它分支（`REFRESHABLE_AUTH_CODES`），但后端一律回 `AUTH_TOKEN_INVALID`。
      P2 的 access JWT 落地时两者才真正分开 —— 在那之前，
      **不要**把它当成「已经验证过的静默 refresh 路径」，见 §3.2。

- [ ] 🔴 **后台的「IAP 401 vs 应用层 401」判别没有真实环境验证，需实测。**
      判别逻辑与单测在 `admin/src/lib/iap.ts` / `iap.test.ts`，但：
      ① 本仓从未真的跑过 IAP，`x-goog-iap-generated-response` 这个头名取自 GCP 文档不是抓包；
      ② **跨域部署时这套判别多半用不上** —— IAP 生成的响应不带我们的 CORS 头，
      浏览器会在 JS 看到它之前就拦掉，`fetch` 抛 `TypeError` → 归一成 `status = 0`。
      所以后台把 `status = 0` 单独归成 `ambiguous-network` 并在 UI 上**同时**说出两种可能，
      而不是断言其中一种。
      ③ 它现在**没有任何活的调用点**：61 个 `/admin/*` operation 全是 501 stub。

- [ ] **本文的章节顺序违反 [docs/README §4.1](../docs/README.md)，2026-08-30 登记但未修。**
      规矩是「`代价` 与 `这次没有解决的` 永远是最后两个编号节，且**物理最后**」，
      而本文是 `§7 代价 → §8 这次没有解决的 → §9 测试` —— §9 排在了它们后面。
      **不修的理由是它会连带打断引用**：`§7` / `§8` / `§9` 在本文内部与
      [roadmap §9 B27 / §7.2](../docs/00-overview/roadmap.md) 等处都被按号引用，
      改序就要重编号。正确修法是把 §9 的内容并进 §3–§6 之后再重编号，属独立一次修订。
      **在那之前，本条就是这个违规的登记处。**
- [ ] **「记住我」没做。** 后端只发一枚 30 天的不透明会话 token，没有「短会话 / 长会话」两档。
      做一个只改前端存储位置（`sessionStorage` → `localStorage`）的开关，
      等于把 30 天的凭据写进 localStorage 而用户以为自己选的是「更方便」。P2 后端支持后再上。

---

## 9 · 测试（2026-08-23 起）

`vitest run`，三个包各跑各的。总计 **108 个用例 / 7 个文件**，本机 `pnpm -r test` 全绿
（2026-08-23 实测：shared 67 / user 33 / admin 8）。

| 文件 | 环境 | 测什么 |
|---|---|---|
| `shared/api/client.test.ts` | node | 故障转移方法矩阵、超时、五类错误归一、平台层拒绝判别、401→refresh→重放 |
| `shared/api/session.test.ts` | node | refresh 单飞、失败语义（拒绝 vs 网络失败）、在途请求的旧 token、**登出/重登与在途 refresh 的竞态** |
| `shared/src/lib/return-to.test.ts` | node | returnTo 的开放重定向向量（含**归一化之后才出现的** `//`） |
| `user/src/App.routes.test.tsx` | jsdom | **对真实 `App` 路由表逐条**核对守卫覆盖：18 条受保护路径 + 4 条免登录 |
| `user/src/lib/auth.test.tsx` | jsdom | 路由守卫三态：确定未登录跳转带 returnTo、**加载态不误跳**、网络失败不跳登录 |
| `user/src/routes/auth/LoginPage.test.tsx` | jsdom | 登录接线：returnTo 落点、按 `ErrorCode` 分支（封禁 ≠ 密码错）、429 倒计时 |
| `admin/src/lib/iap.test.ts` | node | IAP 401 与应用层 401 的判别 |

### 为什么第一批是这五个

不是「先测好测的」，是**先测改错了没人会发现的**：

1. **「POST 不故障转移」是一条安全规则，不是性能规则。** 非幂等请求换个域名重发一次，
   「下单」「标记已支付」就变成了两笔。而它极容易被好心改错 ——
   「为什么只有 GET 能重试？把 POST 也加上不是更稳吗」听起来完全合理。
   所以测试写成**方法矩阵**（POST/PUT/PATCH/DELETE 各一条，GET/HEAD/OPTIONS 各一条），
   改错了 CI 会直接指出改错的是哪一个方法。
2. **refresh 单飞的失败模式是「用户被踢下线」，而它只在并发时出现。**
   手工点不出来，代码走查也看不出来（那段代码看起来完全正常）。
3. **returnTo 的失败模式是开放重定向**，而这个面板的用户群恰好是最会被钓鱼的一群：
   钓鱼链接的前半截是我们**真实的**登录域名，用户核对域名这一步会通过。
4. **守卫的「加载态不误跳」在正常网络下测不出来** —— 本地开发时 `/me` 几毫秒就回来了，
   闪一下谁都看不见。而在大陆跨境链路上它是数秒。
5. **IAP 判错的代价不对称**：把平台层拒绝判成应用层，运维会在打不开的登录页上反复输密码，
   而这通常发生在服务已经出故障、时间最紧的时候。

### 有意没做的

- **不 mock `fetch` 库**。Node 26 原生提供 `fetch` / `Request` / `Response` / `AbortSignal.timeout`，
  传输层能按它在浏览器里的样子被测。多一个 mock 库就多一层「测的是 mock 不是代码」的风险。
- **不开 `globals`**。`describe` / `it` / `expect` 全部显式 import ——
  `shared` 的 tsconfig 是 `"types": []`，开 globals 就得让**生产代码**也看见测试全局变量。
- **超时用真的短超时（20ms）而不是假时钟**。`AbortSignal.timeout` 是平台 API，
  不走 JS 的 `setTimeout`，假时钟推不动它。

### 复核时补上的三条（2026-08-23）

第一批测试写完之后做了一次独立复核（把规则**故意改坏**，看测试会不会红）。
方法矩阵、单飞、三态守卫三条都如期变红 —— 但复核同时翻出两个**测试没覆盖到的真缺陷**，
都已修掉并补了会红的回归用例：

1. **`safeReturnTo` 的开放重定向绕过（安全）。** `?returnTo=/..//evil.example`
   以单个 `/` 开头、不含反斜杠与控制字符、解析后 origin 也仍是本站 ——
   原来的每一条检查都过得去。但 `/..` 被解析掉之后 pathname 变成 `//evil.example`，
   这是**协议相对 URL**，交给 `navigate()` 再解析一次就落到 `https://evil.example/`。
   根因是**所有前置检查作用在原串上，而返回的是归一化后的串**，两者不是同一个东西。
   修法：对最终交出去的值重跑一遍结构判定。
   同时把 `/auth/` 黑名单改成大小写不敏感 —— react-router 的路由匹配默认
   `caseSensitive: false`，`/AUTH/login` 照样匹配得到那条路由。

2. **登出被在途 refresh 撤销（安全）。** `signOut()` 只把 `inflight` 引用摘掉，
   但那个 async 闭包**还在飞**，回来照样执行到写回那一步 ——
   于是「点了登出 → 在途 refresh 返回 → token 被写回 sessionStorage」。
   UI 看着是登出了（订阅者只对 `null` 反应），下次打开页面却自动登录回去。
   在共用电脑上这是一次真实的会话泄漏。
   修法：加一个**只由外部写入递增**的会话世代号，refresh 回来时先对世代号，
   过期就丢掉结果。重新登录时同理 —— 上一轮 refresh 不会覆盖新会话。

3. **守卫覆盖没有被逐条钉住。** `auth.test.tsx` 渲染的是一棵**测试自建的**两条路由的树，
   验证的是 `RequireAuth` 组件的行为，验证不了 `App.tsx` 那张表有没有漏掉某一条。
   把某条路由挪出守卫，那一支照样全绿。补 `App.routes.test.tsx`：
   拿真实 `App` 渲染，18 条受保护路径逐条断言落到登录页，4 条认证页逐条断言可直达。
