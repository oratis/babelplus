/**
 * 代理认证：`chrome.webRequest.onAuthRequired` 的回填逻辑（边界 ②：Chrome 对 SOCKS5 不支持认证，
 * 所以上游只能是 HTTPS 代理 + Basic，凭据在这里回填）。
 *
 * 纯函数 `decideAuth` 承担全部判断，事件监听器只是把 chrome 的回调形状套上去。
 *
 * 三条规则：
 *  1. **只回应代理质询**（`isProxy === true`）。站点自己的 401（`WWW-Authenticate`）与我们无关，
 *     交还给 Chrome —— 否则用户登录任何带 Basic 认证的内网站点时都会被我们塞一对错凭据。
 *  2. **只回应我们的端点**（challenger 的 host:port 在凭据表里）。系统或别的扩展配的代理不归我们管。
 *  3. **同一个请求最多回填两次**，第三次质询直接取消并报告「凭据被拒」。不设上限的话，
 *     一个被重置了订阅的用户会让每个请求在 407 ↔ 回填之间打转，Chrome 的表现是页面永远在加载。
 */
export interface ProxyCredential {
  readonly host: string;
  readonly port: number;
  readonly username: string;
  readonly password: string;
}

export interface AuthChallenge {
  readonly requestId: string;
  readonly isProxy: boolean;
  readonly challenger: { readonly host: string; readonly port: number };
}

export type AuthDecision =
  | { readonly authCredentials: { readonly username: string; readonly password: string } }
  | { readonly cancel: true }
  | Record<string, never>;

export const MAX_AUTH_ATTEMPTS = 2;

export function decideAuth(
  challenge: AuthChallenge,
  credentials: readonly ProxyCredential[],
  attempts: Map<string, number>,
  onRejected?: () => void,
): AuthDecision {
  if (!challenge.isProxy) return {};
  const host = challenge.challenger.host.toLowerCase();
  const cred = credentials.find((c) => c.host.toLowerCase() === host && c.port === challenge.challenger.port);
  if (!cred) return {};

  const n = attempts.get(challenge.requestId) ?? 0;
  if (n >= MAX_AUTH_ATTEMPTS) {
    attempts.delete(challenge.requestId);
    onRejected?.();
    return { cancel: true };
  }
  attempts.set(challenge.requestId, n + 1);
  return { authCredentials: { username: cred.username, password: cred.password } };
}

/**
 * `attempts` 会随请求数增长。请求结束后 Chrome 不会通知我们（没申请 onCompleted），
 * 所以用一个上限做粗暴回收：超过就整个清掉 —— 代价是极端情况下某个请求多回填一次，
 * 换来的是 service worker 内存不随会话时长增长。
 */
export const ATTEMPTS_CAP = 2000;

export interface AuthListenerDeps {
  readonly getCredentials: () => Promise<readonly ProxyCredential[]>;
  readonly onRejected: () => void;
}

export type AsyncAuthListener = (
  details: AuthChallenge,
  callback: (response: AuthDecision) => void,
) => void;

export function createAuthListener(deps: AuthListenerDeps): AsyncAuthListener {
  const attempts = new Map<string, number>();
  return (details, callback) => {
    if (attempts.size > ATTEMPTS_CAP) attempts.clear();
    deps
      .getCredentials()
      .then((creds) => callback(decideAuth(details, creds, attempts, deps.onRejected)))
      .catch(() => callback({}));
  };
}
