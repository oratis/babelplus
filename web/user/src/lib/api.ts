/**
 * 用户面板的 API 客户端单例。
 *
 * 会话存储用 `sessionStorage` 而不是 `localStorage`：access token 是 15 分钟短票，
 * 关标签页就该丢；长期身份由 refresh token 负责，而 refresh token
 * **不应该被 JS 读到**（目标态是 httpOnly cookie）。
 *
 * TODO(P1): refresh 流程。契约写明 access 15 分钟、refresh 30 天且**一次性轮换**
 *           （openapi.yaml securitySchemes.userSession）。轮换 + 并发请求 = 必须做单飞，
 *           否则两个并发的 401 会各发一次 refresh，后一次把前一次刚签发的 token 作废，
 *           用户被踢下线。这是这一整块最容易写错的地方，接线时单独设计并写测试。
 * TODO(P1): 与之配套的登录态 Context / 路由守卫（现在所有页面都可直达）。
 */
import { createApiClient, type ApiClient } from '@babelplus/shared/api';
import { runtimeConfig } from '@babelplus/shared';

const ACCESS_TOKEN_KEY = 'bp.access_token';

export function readAccessToken(): string | null {
  try {
    return window.sessionStorage.getItem(ACCESS_TOKEN_KEY);
  } catch {
    // 隐私模式下 sessionStorage 可能直接抛错。读不到就当未登录，不要炸掉整个应用。
    return null;
  }
}

export function writeAccessToken(token: string | null): void {
  try {
    if (token === null) window.sessionStorage.removeItem(ACCESS_TOKEN_KEY);
    else window.sessionStorage.setItem(ACCESS_TOKEN_KEY, token);
  } catch {
    /* 同上，忽略。 */
  }
}

let client: ApiClient | null = null;

export function api(): ApiClient {
  if (client) return client;
  const cfg = runtimeConfig();
  client = createApiClient({
    baseUrl: cfg.apiBaseUrl || window.location.origin,
    fallbackBaseUrls: cfg.apiFallbackBaseUrls,
    timeoutMs: cfg.requestTimeoutMs,
    getAccessToken: readAccessToken,
    onUnauthorized: () => {
      writeAccessToken(null);
      // TODO(P1): 这里应该导航到 /auth/login 并带上 returnTo，
      //           而不是硬跳转（硬跳转会丢掉用户已填但没提交的表单内容）。
    },
  });
  return client;
}
