# Cloud Run 拦截 /healthz · 2026-08-17

> 日期：2026-08-17 · 性质：**证据型核查** · 状态：**已完成**
> 事实基线：`bp-api` 首次部署到 Cloud Run（`us-central1`，修订版 `bp-api-8c77fb3`）后的实测
> 关联：[monitoring.md](../../04-ops/monitoring.md)、[deploy.md](../../04-ops/deploy.md)、
> [api-contract.md](../../02-architecture/api-contract.md)

---

## 1 · 结论

**`/healthz` 在 Cloud Run 上不可用 —— Google Frontend 会拦截它，请求根本到不了容器。**

已把探活路径改为 **`/-/healthz`**。

---

## 2 · 证据：三种 404，来源各不相同

同一个部署、同一个 URL 前缀，只改路径字符串：

| 路径 | HTTP | 响应体 | 谁返的 |
|---|---|---|---|
| `/healthz` | 404 | `<!DOCTYPE html>` + `referrer-policy: no-referrer` | **Google Frontend** |
| `/healthzz`（未注册） | 404 | `404 page not found` | **我们的 chi 路由**（到了容器） |
| `/api/v1/plans`（已注册） | 401 | `{"error":{"code":"AUTH_TOKEN_INVALID",…}}` | **我们的鉴权中间件**（到了容器） |

关键在于**日志**：`/api/v1/plans` 在 Cloud Run 请求日志里有完整的 `httpRequest` 记录，
而 `/healthz` **连一条都没有** —— 包括 Cloud Run 自己的平台级请求日志。
这排除了「请求到了容器但应用没处理」这一可能：它压根没到 Cloud Run。

另外实测这些路径也 404（均为我们 chi 路由的纯文本 404，即到达了容器）：
`/healthz/`、`/HEALTHZ`、`/health`、`/api/v1/healthz`、`/_ah/health`、`/livez` ——
它们只是**没注册**，与 `/healthz` 的拦截是两回事。

---

## 3 · 为什么这件事重要

`/healthz` 是探活路径的事实标准（Kubernetes 惯例），因此：

1. **Cloud Monitoring 的 uptime check 若指向 `/healthz` 会永远失败**，
   而失败原因看起来像「服务挂了」——排障方向会被带偏。
2. 这类问题**只在真实部署后才会暴露**：本地容器跑 `/healthz` 完全正常
   （本地冒烟基线里它一直是 200）。
3. 症状具有迷惑性：返回的是 Google 的 HTML 404，很容易被当成
   「Cloud Run 服务没部署好」或「URL 写错了」，而不是「这条路径被保留了」。

---

## 4 · 处置

| 位置 | 改动 |
|---|---|
| `openapi/openapi.yaml` | 路径 `/healthz` → `/-/healthz`，并在规格里写明原因 |
| 生成物 | `make gen-api` + `gen-stubs` + `pnpm run gen:api` 已重新生成 |
| `infra/deploy/deploy-api.sh` | 部署后提示里的路径同步 |
| `docs/04-ops/monitoring.md` | uptime check 的路径同步 |
| `docs/04-ops/local-development.md` | 冒烟基线的路径同步 |

选 `/-/healthz` 的理由：`/-/` 前缀是 Prometheus 生态的惯例（`/-/healthy`、`/-/ready`），
不与任何平台保留路径冲突，且一眼能看出是运维端点而非业务端点。

---

## 5 · 复现方法

```bash
U=https://bp-api-cko3zfff5a-uc.a.run.app
curl -s $U/healthz  | head -1     # <!DOCTYPE html>  ← Google 的页面
curl -s $U/healthzz | head -1     # 404 page not found  ← 我们的 chi
curl -s $U/api/v1/plans           # {"error":…}  ← 我们的中间件

# 决定性的一步：查 Cloud Run 请求日志，/healthz 不会有任何记录
gcloud logging read 'resource.labels.service_name="bp-api"' \
  --project=oratis-491316 --limit=10 \
  --format='value(httpRequest.requestUrl,httpRequest.status)' --freshness=5m
```

---

## 6 · 代价

> ⚠️ 1. **路径改动会影响任何已经指向 `/healthz` 的外部配置。**
> 目前没有（服务刚首次部署），但如果将来接了外部监控，改路径要同步改那边。
> 2. 本次只证明了 `/healthz` 被拦截，**没有系统性地探测 Cloud Run 还保留了哪些路径**。
> 换句话说，下一个撞上同类问题的路径仍然可能存在。

## 7 · 这次没有解决的

- [ ] **Cloud Run 保留路径的完整清单未知。** 没有找到 Google 的官方文档说明这一点，
      本结论纯属实测所得。若后续再撞到类似的「请求不进容器」，应当回来补这份清单。
- [ ] 未验证 `/-/healthz` 是否也会在**其他**平台（Cloudflare、负载均衡器）上被特殊处理。
- [ ] `/readyz`（探库的就绪检查）**在规格里根本不存在** —— deploy-api.sh 的部署后提示
      引用了它，属于脚本与规格不一致。就绪检查要不要单独做，尚未裁决。
