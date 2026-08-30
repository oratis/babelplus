# 怎么给 babel.plus 提交代码

> 日期：2026-08-17 · 性质：**执行手册** · 状态：**As-Built**（2026-08-17 —— 生成物一致性在开发机上实测过，见 §3.3）
> 事实基线：本文引用的命令来自 `api/Makefile`、`web/package.json`、`.github/workflows/ci.yml`
> 读者：第一次往这个仓库提 PR 的人。**动手前把 §1 到 §4 读完**，剩下的用到再翻。
> 关联：[local-development.md](docs/04-ops/local-development.md)（怎么跑起来）、
> [docs/README.md](docs/README.md)（写文档的约定）、[AGENTS.md](AGENTS.md)（Agent 规则与操作红线）

---

## 1 · 先把它跑起来

**只需要 Docker。不需要在本机装 Go、sqlc、oapi-codegen、psql。**

完整步骤在 [`docs/04-ops/local-development.md`](docs/04-ops/local-development.md) ——
包括两个必须先知道的环境坑（Docker Hub 拉不到 golang 镜像、Docker Desktop 注入的代理端口是错的）。
不要跳过那一节自己敲 `docker run`，`Makefile` 里已经把绕过写好了。

```bash
cd api && make help        # 列出所有目标
make check                 # fmt-check + vet + build + test，CI 跑的就是这个
make migrate-verify        # 起库 → 灌全部 up → 全部 down → 确认归零 → 拆库

cd ../web && pnpm install  # 前端走宿主机的 node/pnpm，不受上面那两个坑影响
pnpm -r build
```

---

## 2 · 提交信息用中文

正文、标题、PR 描述**一律简体中文**；
路径、字段名、命令、错误信息、协议名、`operationID` **保持英文原样，不翻译**（AGENTS.md §6）。

一行标题写清楚「改了什么」，不写「修复问题」这种等于没说的话。
需要交代取舍时正文另起一段，说明**为什么这么改**而不是复述 diff。

```
补全订阅 token 的吊销校验：issued_at < sub_revoked_at 一律 404

不返回 403 —— 403 会告诉爆破方「这个 token 存在过」，
而 404 对存在与不存在的 token 表现一致。见 api-contract.md §4.2 第 6 条。
```

不要用 `[skip ci]`。ADR 0006 §14.2 把整条契约防线押在 CI 那一行 `git diff --exit-code` 上，
**跳过 CI 就是跳过唯一的防线。**

---

## 3 · 生成物必须和源文件一起提交

这是本仓库最容易犯、也最难查的一类错误，单独一节。

### 3.1 四处生成物

| 生成物 | 源 | 工具 | 重新生成 |
|---|---|---|---|
| `api/internal/gen/api.gen.go` | `openapi/openapi.yaml` | oapi-codegen | `cd api && make gen-api` |
| `api/db/gen/*.go` | `api/db/migrations/` + `api/db/queries/` | sqlc | `cd api && make gen-db` |
| `api/internal/handler/unimplemented.gen.go`<br>`api/internal/handler/operations.txt` | 上面生成的接口 | `scripts/gen_stubs.py` | `cd api && make gen-stubs` |
| `web/shared/api/schema.d.ts` | `openapi/openapi.yaml` | openapi-typescript | `cd web && pnpm run gen:api` |

一条命令全做完（**包含收尾的 `gofmt`，不能省**）：

```bash
cd api && make generate
cd ../web && pnpm run gen:api
```

### 3.2 为什么入库而不是构建时生成

ADR 0006 §13：**code review 时能看见契约变化本身。**
一个 PR 改了 `openapi.yaml` 却没有对应的生成物 diff，是最强的「这个改动没想清楚」信号。

### 3.3 CI 会怎么卡你

`contract-drift` 作业重新跑一遍上面四个生成器，然后 `git diff --exit-code`。
有差异就是红的，错误信息里会直接写要敲哪两条命令。

> 2026-08-17 在开发机上实测过一遍：sqlc 1.31.1、oapi-codegen v2.4.1、`gen_stubs.py`
> 重新生成后与仓库里的版本**逐字节一致**。
> ⚠️ 其中 `gen_stubs.py` 的原始输出有一处多余空行，靠 `make generate` 尾部的 `gofmt` 收掉。
> **手动只跑 `gen-stubs` 而不 `fmt`，会得到一行假漂移。**

### 3.4 版本变了怎么办

三个生成器的版本分别钉在 `api/Makefile`（oapi-codegen）、`.github/workflows/ci.yml`（sqlc 镜像）、
`web/package.json`（openapi-typescript）。
升级任何一个都可能改变生成物 —— **升级和重新生成必须在同一个 PR 里**，
否则表现为「谁都没改代码但 CI 红了」。

---

## 4 · CI 会跑什么

`.github/workflows/ci.yml`。**PR 上按路径过滤，push 到 `master` 与手动触发一律跑全量** ——
过滤器判错的后果是「全绿但什么都没跑」，那比多等几分钟贵得多。

| 作业 | 触发路径 | 做什么 |
|---|---|---|
| `go` | `api/**` `openapi/**` | `go build` / `go vet` / `go test` / `gofmt -l` |
| `contract-drift` | `api/**` `web/**` `openapi/**` | §3 的四处生成物 |
| `migrations` | `api/**` | 起 `postgres:17`，正序灌全部 up，逆序灌全部 down，断言残留表 / 视图 / 枚举类型均为 0；**另跑两步真打数据库的检查**（2026-08-30 新增）：`api/scripts/db_explain.py` 对 `db/gen/*.sql.go` 里的 **343 条**常量 SQL 逐条 `EXPLAIN (GENERIC_PLAN)`（其中 **172 条**是写语句），以及 `api/scripts/ci_post_rollback_write.sql` 的**回滚后写探针** |
| `openapi-lint` | `openapi/**` | `redocly lint` |
| `web` | `web/**` `openapi/**` | `pnpm install --frozen-lockfile` → `pnpm -r build` → `pnpm -r typecheck` |
| `shellcheck` | `infra/**` | 扫全部 `*.sh` |
| `docker-build` | `api/**` | 构建部署镜像（不推送），报告体积 |
| `ci-ok` | 总是 | 汇总门禁，分支保护只勾这一个 |

分支保护请**只勾 `ci-ok`**。逐个勾上面 8 个作业会让被路径过滤跳过的作业永远 pending，PR 卡死。

部署是另一条工作流（`.github/workflows/deploy.yml`），**手动触发**，走 Workload Identity Federation，
部署前后各跑一次隔离核对。它的参数一字不差地来自 [deploy.md](docs/04-ops/deploy.md)，
**改那些参数之前先去读那份手册的 §5.1**。

---

## 5 · 什么时候要写 ADR

写在 [`docs/05-adr/`](docs/05-adr/)，编号四位、只增不减。

**必须写**：

- 选型（语言、库、托管、数据库、协议、支付通道）—— 凡是「换掉它要动很多地方」的选择。
- 任何触及**部署形态**的改动（新增可部署单元、改隔离边界、改 Cloud Run 参数的依据）。
- 任何触及**协议与传输层**的改动 —— ADR 0004 的调参依据是安全属性，不是性能偏好。
- **推翻已有裁决时**。规矩是：不删不改旧 ADR、不加 DEPRECATED，而是写一份新的，
  在头部写 `**推翻 [NNNN 号 §x](NNNN-….md)**`，并逐条交代旧理由在新架构下的落点
  （不再适用 / 保留 / 行为变化）。见 [docs/README.md §4](docs/README.md)。

**不用写**：修 bug、加一个已在契约里的端点实现、补测试、改文案、重构但不改边界。

**每份 ADR 强制两个尾节，不允许省略**：

```markdown
## N · 代价
> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
> 1. …（量化，带数字）
> 2. …（写明什么情况下这个取舍不再成立）

## N+1 · 这次没有解决的
- [ ] …（每条说清楚为什么不在本次范围内）
```

写文档的其余约定（不用 YAML frontmatter、文档头格式、受控词表、命名）在
[`docs/README.md`](docs/README.md)。**动笔前必读。**

---

## 6 · 三条红线

1. **凭据不进仓库。** 一律走环境变量 / Secret Manager。
   `.env`、`*.pem`、`*.key`、`service-account*.json` 已在 `.gitignore` 里 —— 但别指望它兜底。
2. **不碰 GCP 项目里的现有资源。** `vpn-us` / `vpn-jp` 两台机器、
   `anthropic-relay` / `lisa-cloud` / `lisa-web` 三个 Cloud Run 服务、10 条现有防火墙规则，
   一条都不许增删改。新建资源一律 `bp-` 前缀 + `bp-node` 网络标签。完整清单见
   [AGENTS.md §4](AGENTS.md) 与 [as-built-gcp.md](docs/02-architecture/as-built-gcp.md)。
3. **不编数字。** 凡未核实标 **待核实**，需实测标 **需实测**。
   AGENTS.md §3 讲了原因：这个领域的社区传闻会直接导致架构错误，
   **编一个数字比留空危害大得多。**

---

## 7 · 代价

> ⚠️ 这一选择的代价，必须留在记录里而不是被措辞掩盖：
>
> 1. **`contract-drift` 让每个改契约的 PR 都变慢。** 它要装 Go、拉 sqlc 容器、跑一次 `pnpm install`，
>    是全流水线里最慢的作业。换来的是 ADR 0006 §14.2 点名的那条**唯一**的防线 ——
>    没有它，前后端类型漂移只有在生产环境才会被发现。
> 2. **生成物入库让 diff 变得很吵。** 改一行 `openapi.yaml` 可能带出几百行生成代码的 diff，
>    review 时要靠路径过滤跳过它们。**若某次 review 因为噪音漏掉了真实改动，
>    这条取舍就该重新评估**（退路是构建时生成 + 契约快照测试）。
> 3. **CI 里钉死了三个生成器的版本，升级是一次人工任务。** 不钉的代价更大：
>    上游发一个新版本就会让所有人的 CI 在没改代码的情况下变红。
> 4. **PR 上的路径过滤依赖一个第三方 action（`dorny/paths-filter`），且没有钉 commit SHA。**
>    浮动 tag 意味着上游被投毒时我们会跟着中招。缓解是 `push` 到 `master` 时不使用过滤结果
>    （全量跑），所以过滤器失效最多让 PR 漏跑，不会让主干漏跑。**钉 SHA 仍然是应该补的。**

## 8 · 这次没有解决的

- [ ] **没有 lint 卡 `fmt.Sprintf` 出现在 SQL 上下文里。** ADR 0006 §14.4 明确要求加这条规则
      （动态查询是 SQL 注入最容易出现的地方），目前 CI 里没有任何静态检查覆盖它。
- [ ] **没有集成测试。** ⚠️ **2026-08-30 订正前半句**：单测早已不止 `httpx` 一处 ——
      `api/` 下有 **20 个 `_test.go`、195 个顶层 `Test` 函数**
      （`find api -name '*_test.go' | wc -l`、`grep -rhoE '^func Test[A-Za-z0-9_]+' api --include='*_test.go' | wc -l`，
      2026-08-30 实数），覆盖 handler / middleware / ratelimit / subgen / httpx / config / cmd 七个包。
      **但这一条的实质并没有被解决**，后半句原封不动仍然成立：跑**真实 Postgres** 的 handler 测试、
      以及 ADR 0006 §12 点名的「起真实 v2node 容器验兼容性」**都还不存在** ——
      **那是本项目唯一能证明 UniProxy 抄对了的测试。**
      现有 195 个用例全部是进程内的单元测试（假 `Querier` / `httptest`），
      一条真实的数据库连接和一个真实的 v2node 容器都没有。
      > 唯一贴近真库的是 CI `migrations` 作业里 2026-08-30 新增的两步
      > （`api/scripts/db_explain.py` 对 194 条生成 SQL 跑 `EXPLAIN (GENERIC_PLAN)`、
      > `api/scripts/ci_post_rollback_write.sql` 的回滚后写探针，见 commit `a4604c9396f`）——
      > 它们打的是真 Postgres，但**测的是 schema 与 SQL 而不是 handler**，不构成集成测试。
      >
      > **2026-08-30 二次订正：上面的三个数都要改，而这一条的实质仍然一个字没变。**
      > 现为 **37 个 `_test.go` / 573 个顶层 `Test` 函数**（同两条命令实数），
      > `db_explain.py` 抽出的是 **343 条**语句、其中 **172 条**写语句。
      > **而「573 个用例全部是进程内单元测试」与「真实 Postgres / 真实 v2node 容器都不存在」
      > 逐字仍然成立** —— 测试数翻了近三倍，本条要的那两样**依旧各是 0**。
      > 🔴 **这正是本条值得反复读的原因：`测试数` 是工作量指标，`测试形态` 才是本条的完成度指标。**
- [ ] **`deploy.yml` 一次都没有真正执行过。** 它的参数来自状态为「待实施」的 deploy.md，
      WIF 的 provider 与 service account 都还是 TODO。首次执行必然撞上偏差，
      **撞到就回写 deploy.md，不要在工作流里悄悄改一个能跑通的写法了事。**
- [ ] **没有 commit message 的机器校验**（没有 commitlint / hook）。§2 的约定靠自觉。
- [ ] **没有 CODEOWNERS 与 PR 模板。** 1–2 人团队暂时不需要，人一多就要补。
