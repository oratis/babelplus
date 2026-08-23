/**
 * 用户面板的会话单例。
 *
 * 会话存储用 `sessionStorage` 而不是 `localStorage`：token 是短票，关标签页就该丢；
 * 长期身份本该由 refresh token 负责，而 refresh token **不应该被 JS 读到**
 * （目标态是 httpOnly cookie，见 `api/internal/middleware/user.go` 的 `AllowCookie` 注释 ——
 * 那条路要先有 CSRF 防护才能走，所以现在还是头形态）。
 *
 * ⚠️ **后端目前只发一枚 token**：`api/internal/handler/auth.go` 的 `sessionTokens()` 里
 * `access_token` 与 `refresh_token` 是同一个值（契约里那对 JWT + refresh 是 P2 的目标态）。
 * 所以这里只有一个键，refresh 时把它当 `refresh_token` 发出去。
 * 拆成两枚之后，改动只落在这个文件与 `shared/api/session.ts`。
 *
 * 单飞逻辑不在这里，在 `@babelplus/shared/api` 的 `createSessionManager` —— 那里能被单测直接打。
 */
import {
  createSessionManager,
  requestSessionRefresh,
  webStorageSessionStore,
  type SessionManager,
} from '@babelplus/shared/api';
import { runtimeConfig } from '@babelplus/shared';

export const ACCESS_TOKEN_KEY = 'bp.access_token';

/** 未登录时跳这里。**只有一处字面量**，别处一律引这个常量。 */
export const LOGIN_PATH = '/auth/login';

let manager: SessionManager | null = null;

export function session(): SessionManager {
  if (manager) return manager;
  const cfg = runtimeConfig();
  const baseUrl = cfg.apiBaseUrl || window.location.origin;

  manager = createSessionManager({
    store: webStorageSessionStore(ACCESS_TOKEN_KEY, () => window.sessionStorage),
    refresh: (staleToken) =>
      requestSessionRefresh(staleToken, { baseUrl, timeoutMs: cfg.requestTimeoutMs }),
  });
  return manager;
}

/**
 * 仅测试用：丢掉单例。
 *
 * 生产代码**不要**调它 —— 换掉 manager 会让已经订阅的组件挂在一个没人再通知的旧实例上，
 * 表现为「登出了但界面没反应」。
 */
export function resetSessionForTests(): void {
  manager = null;
}
