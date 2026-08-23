/**
 * 用户面板的 API 客户端单例。
 *
 * 三条 TODO(P1) 在这一版里做掉了，各自落在哪：
 *  - **refresh 单飞** → `@babelplus/shared/api` 的 `createSessionManager`（可单测），
 *    这里只负责把它接到客户端的 `refreshAccessToken` 上；
 *  - **登录态 Context / 路由守卫** → `lib/auth.tsx` 的 `AuthProvider` 与 `RequireAuth`；
 *  - **401 跳登录带 returnTo** → 同样在 `RequireAuth` 里。
 *
 * 为什么 401 的跳转不写在这个文件里：这里只能拿到 `window.location`，做不到不丢表单内容的
 * 客户端路由跳转。`RequireAuth` 拿得到 router 的 location，跳转、returnTo 校验、
 * 「还没确定登录态就不要跳」这三件事在那里是同一段代码，拆开必然有一处对不上。
 * 这个文件要做的只是**把会话作废**，剩下的交给 React 树对 token 变化的订阅。
 */
import { createApiClient, type ApiClient, type ApiError } from '@babelplus/shared/api';
import { runtimeConfig } from '@babelplus/shared';
import { session } from './session.ts';

let client: ApiClient | null = null;

export function api(): ApiClient {
  if (client) return client;
  const cfg = runtimeConfig();
  client = createApiClient({
    baseUrl: cfg.apiBaseUrl || window.location.origin,
    fallbackBaseUrls: cfg.apiFallbackBaseUrls,
    timeoutMs: cfg.requestTimeoutMs,
    getAccessToken: () => session().getToken(),
    // 单飞在 SessionManager 里；客户端只负责「拿到新 token 就重放一次」。
    refreshAccessToken: (staleToken) => session().ensureFreshToken(staleToken),
    onAuthFailure: handleAuthFailure,
  });
  return client;
}

/**
 * 401 / 403 的最终处置（此时静默 refresh 与重放**都已经试过了**，见 `createTransport`）。
 *
 * 三条 early return，每一条都对应一个真实的误判：
 *  1. **403 不清会话。** 后端对被封禁的账号返回 403 `AUTH_PERMISSION_DENIED`
 *     （`middleware/user.go`），会话本身是有效的。清掉它 → 用户看到「登录已过期」→
 *     重新登录 → 登录接口又告诉他被封禁。中间件的注释里点名了这个来回。
 *  2. **登录接口的 401 不清会话。** `AUTH_INVALID_CREDENTIALS` 是「这次密码输错了」，
 *     不是「你的会话死了」。
 *  3. **本来就没登录时什么都不做。** 未登录访问需要登录的端点拿到 401 是预期结果。
 */
function handleAuthFailure(error: ApiError): void {
  if (error.status !== 401) return;
  if (error.code === 'AUTH_INVALID_CREDENTIALS') return;
  if (session().getToken() === null) return;
  session().signOut('rejected');
}

/** 仅测试用，理由同 `resetSessionForTests`。 */
export function resetApiClientForTests(): void {
  client = null;
}
