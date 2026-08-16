# web/ —— 两个 SPA 的脚手架：用户面板 20 条路由 + 后台 17 个模块，全部是可导航的空壳

> 日期：2026-08-17 · 性质：**实现（脚手架）** · 状态：**已构建通过，业务逻辑为零**
> 事实基线：类型由 `openapi/openapi.yaml`（OpenAPI 3.1，128 个 operation）生成；
> 页面清单与三态规则来自 [page-inventory.md](../docs/03-product/page-inventory.md)；
> 可达性约束来自 [ADR 0003](../docs/05-adr/0003-web-hosting-and-reachability.md)
> 关联：[user-journey.md](../docs/03-product/user-journey.md)、[api-contract.md](../docs/02-architecture/api-contract.md)
> 读者：接手写前端的人。本文说明**目录怎么分、为什么这么分、哪些东西不能动**。
> ⚠️ **所有页面都是空壳。** 每一页都有布局、标题、加载 / 空 / 错误三态占位，
> 业务逻辑一律标 `TODO(P1)`。没有一处假装实现，也没有一处假数据。

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
| `pnpm lint:no-external` | 扫描 `dist`，确认没有任何第三方主机名（见 §5） |
| `pnpm dev:user` / `pnpm dev:admin` | 5173 / 5174 |

生成物 `shared/api/schema.d.ts` **必须提交**。它不在 `.gitignore` 的忽略范围内，
和 `api/internal/gen/`、`api/db/gen/` 是同一条纪律。

`.github/workflows/ci.yml` 的 `contract-drift` 作业会在 `web/` 里跑
`pnpm install --frozen-lockfile && pnpm run gen:api`，然后 `git add web/shared/api` 再比 index
（这样连「新增了生成文件但忘了 git add」也盖得住）；
`web` 作业跑 `pnpm -r build`、`pnpm -r typecheck`、`pnpm run --if-present lint:no-external`。
**改这三个脚本名要同步改那个文件。**

## 3 · API 客户端

`openapi-typescript` 7.13.0 生成类型（**7.x 完整支持 OpenAPI 3.1**，6.x 不行），
`openapi-fetch` 0.17 做调用。契约是全仓事实源，前端这边只补生成器给不了的三件事：

1. **统一信封的拆封。** user / admin 两面是 `{data, meta}` / `{error, meta}`，
   而 openapi-fetch 的返回也叫 `data` —— `unwrap()` 负责这层歧义。
2. **超时 + 备用域名故障转移。** 默认超时 15 秒（page-inventory §2.2 的**提案值，需实测**
   按晚高峰 P95 校准），超时后向备用域名重试**一次**。
   🔴 **只有 GET / HEAD / OPTIONS 会故障转移** —— POST 换个域名重发一次就是重复下单。
   这条限制写死在客户端里，不指望每个调用点自觉。
3. **五类错误归一**（401 / 403 / 4xx / 5xx / 网络不可达）。
   page-inventory §2.2 的原话：把「你的账号过期了」和「我们的服务器挂了」显示成同一句话，
   会直接变成工单。

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
>    **但这个判断会随接线而失效** —— 页面长出内容后必须重新评估，
>    目前 user 产物 296 KB / gzip 93.5 KB 已经不算小。
> 3. **`?state=` 这个三态开关会进生产构建。** 它是可见的、可被任何人触发的调试入口。
>    它不泄漏数据（页面本来就没数据），但接线时**必须删掉**，
>    否则会变成一个能伪造「空态」「错误态」的开关。
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

- [ ] 🔴 **登录态与路由守卫完全没做。** 现在所有页面都可直达（评审方便，上线危险）。
      配套的 refresh 单飞（access 15 分钟 + refresh 一次性轮换）是最容易写错的一块，
      详见 `user/src/lib/api.ts` 的 `TODO(P1)`。
- [ ] 🔴 **危险操作的确认组件不存在。** 后台 16 条 D 项现在只有清单展示，没有可用的
      `DangerAction`（确认串校验 + 必填原因 + 改前值 / 改后值提交给审计）。
      **不接受「先做功能，审计以后补」** —— 补的那天就是需要查审计的那天。
- [ ] **公开站（落地页 / 文档站 / 备用域名页 / `check.*` / `status.*`）不在本目录。**
      page-inventory §5 的 7 个页面是第三套前端，需要单独的目录与托管决策。
      其中**备用域名页**（<20 KB、无 JS、部署在每一个镜像域名上）是最后一道防线，
      优先级高于这里的很多东西。
- [ ] **没有测试。** 一行都没有。空壳阶段单元测试价值有限，
      但 `shared/api/client.ts` 的故障转移逻辑（尤其是「POST 不转移」这条）
      **应该有测试**，它是一条会被后来者好心改错的规则。
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
      `ErrorCode` 从 OpenAPI 生成了类型，但前端目前没有按 code 分支的文案表 ——
      接线时会需要，且它应该由契约驱动而不是手抄。
