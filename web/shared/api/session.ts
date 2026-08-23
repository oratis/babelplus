/**
 * 会话管理：token 存放 + **refresh 单飞（single-flight）**。
 *
 * ⚠️ 写这个文件之前读过后端实现，下面几条是**后端的实际行为**，不是契约文档的说法：
 *
 *  1. `api/internal/handler/auth.go` 的 `sessionTokens()` 里，**`access_token` 与 `refresh_token`
 *     是同一个值** —— 一枚不透明会话 token。契约（openapi `SessionTokens`）写的是
 *     「access JWT 15 分钟 + refresh 30 天」，DB 里没有任何 JWT 载体，那一段是 P2 的目标态。
 *     所以本模块只存**一枚** token，refresh 时把它当 `refresh_token` 发出去。
 *  2. `RefreshToken` 在同一个事务里签发新会话并把旧会话 `revoked_at` 置上（轮换链）。
 *     **旧 token 在 refresh 成功的那一刻立即失效。**
 *  3. 因此并发 refresh 是真实的踢人场景：两个 401 各发一次 refresh，
 *     第二次用的是第一次刚作废的 token → 401 → 用户被登出。单飞不是优化，是正确性。
 *  4. 后端的复用检测**还没做**（auth.go 的 `TODO(P2)`：唯一那条按 hash 查会话的 SQL
 *     在 SQL 层就滤掉了 `revoked_at IS NOT NULL` 的行，所以「拿到一枚已轮换的 token」
 *     与「token 根本不存在」在服务端不可区分）。也就是说前端一旦并发 refresh，
 *     服务端不会把整条轮换链撤掉，但用户照样掉线 —— 后果只轻一点，不是没有。
 *  5. `middleware/user.go` 的 `authFailure()` **一律返回 `AUTH_TOKEN_INVALID`**，
 *     契约里那个 `AUTH_TOKEN_EXPIRED` 目前**没有任何代码路径会产生**（它是给 P2 的 access JWT 留的）。
 *     只认 `AUTH_TOKEN_EXPIRED` 去触发静默 refresh，等于这条路径永远不会被走到。
 *     判定名单见 `client.ts` 的 `REFRESHABLE_AUTH_CODES`。
 *
 * 还有一条由 ①② 组合出来的、容易被漏掉的时序：**refresh 成功会让所有「在途请求」手里的
 * token 当场作废**（它们带的是旧值）。这些请求随后会 401，如果每个都再去 refresh 一次，
 * 就又回到了互相作废。`ensureFreshToken` 因此收一个 `staleToken` 参数：
 * 只有「你失败时用的 token 就是我现在存着的这枚」才值得刷新，否则直接把新 token 给它去重试。
 */

/** token 的存放位置。抽成接口是为了让测试不碰 `window`，也为了将来换 httpOnly cookie 时只改这里。 */
export interface SessionStore {
  read(): string | null;
  write(token: string | null): void;
}

/** 内存存储。测试用，以及 SSR / 隐私模式下 storage 不可用时的兜底。 */
export function memorySessionStore(initial: string | null = null): SessionStore {
  let value = initial;
  return {
    read: () => value,
    write: (token) => {
      value = token;
    },
  };
}

/**
 * `sessionStorage` / `localStorage` 存储。
 *
 * `pick` 是个函数而不是直接传 `Storage`：Safari 隐私模式下**读 `window.sessionStorage` 这个属性本身**
 * 就可能抛异常，在模块顶层求值会让整个应用白屏。
 */
export function webStorageSessionStore(key: string, pick: () => Storage | null | undefined): SessionStore {
  return {
    read() {
      try {
        return pick()?.getItem(key) ?? null;
      } catch {
        return null;
      }
    },
    write(token) {
      try {
        const storage = pick();
        if (!storage) return;
        if (token === null) storage.removeItem(key);
        else storage.setItem(key, token);
      } catch {
        /* 隐私模式下写不进去就算了，不要炸掉整个应用。 */
      }
    },
  };
}

/** 为什么会话结束了。**不同原因的文案与落点不同**，所以不能糊成一个 boolean。 */
export type SignOutReason =
  /** 用户自己点了登出。 */
  | 'user'
  /** 服务端明确拒绝了这枚 token（401），且 refresh 也换不回来。 */
  | 'rejected'
  /** refresh 被服务端拒绝（会话真的死了 / 已被轮换过）。 */
  | 'refresh-rejected';

export interface SessionManagerOptions {
  readonly store: SessionStore;
  /**
   * 拿旧 token 换新 token。
   *
   * **返回值与抛异常是两回事，调用方必须分得清**：
   *  - 返回新 token → 换到了；
   *  - 返回 `null` → 服务端明确拒绝（401）。会话死了，该登出；
   *  - 抛异常 → 网络层没走到服务端（超时 / 不可达）。会话**可能完全有效**，
   *    此时登出会把一次跨境抖动表现为「无故被踢下线」—— 大陆链路上这不是罕见事件。
   */
  readonly refresh: (staleToken: string) => Promise<string | null>;
  readonly onTokenChange?: (token: string | null) => void;
  readonly onSignedOut?: (reason: SignOutReason) => void;
}

export interface SessionManager {
  getToken(): string | null;
  setToken(token: string | null): void;
  /** 主动登出（只清本地状态，撤销服务端会话是调用方的事）。 */
  signOut(reason?: SignOutReason): void;
  /**
   * 单飞刷新。`staleToken` 是那次失败请求实际带上的 token。
   * 返回可用于重试的 token；返回 `null` 表示「别重试了」。
   */
  ensureFreshToken(staleToken: string | null): Promise<string | null>;
  subscribe(listener: (token: string | null) => void): () => void;
  /** 仅供测试与调试：此刻有没有一次正在进行的 refresh。 */
  isRefreshing(): boolean;
}

export function createSessionManager(options: SessionManagerOptions): SessionManager {
  const listeners = new Set<(token: string | null) => void>();
  let inflight: Promise<string | null> | null = null;
  // 每轮 refresh 一个序号。收尾时只有「我还是当前这一轮」才把 inflight 清掉 ——
  // 无条件清的话，「登出 → 重新登录 → 新的一轮已经开始」时，
  // 上一轮的收尾会把新一轮的单飞槽位抹掉，于是又能并发出两次 refresh。
  let refreshSeq = 0;

  function emit(token: string | null): void {
    options.onTokenChange?.(token);
    for (const listener of listeners) listener(token);
  }

  function setToken(token: string | null): void {
    const normalized = token === null || token === '' ? null : token;
    if (options.store.read() === normalized) return;
    options.store.write(normalized);
    emit(normalized);
  }

  function signOut(reason: SignOutReason = 'user'): void {
    // 先把在途 refresh 的引用摘掉：它还在飞，但结果已经不该再写回 token。
    inflight = null;
    const had = options.store.read() !== null;
    options.store.write(null);
    if (had) emit(null);
    options.onSignedOut?.(reason);
  }

  return {
    getToken: () => options.store.read(),
    setToken,
    signOut,
    isRefreshing: () => inflight !== null,

    ensureFreshToken(staleToken) {
      const current = options.store.read();

      // 已经登出了：不要为一个没有身份的请求去刷新。
      // 未登录用户访问需要登录的端点会拿到 401，那是**预期结果**，不是会话过期。
      if (current === null) return Promise.resolve(null);

      // 别人已经刷过了（典型情形：这个请求在 refresh 完成前就发出去了，手里是旧 token）。
      // 直接把新 token 交回去重试，**不要再刷一次** —— 再刷就把刚拿到的新 token 也作废了。
      if (staleToken !== null && staleToken !== current) return Promise.resolve(current);

      // 单飞：从这里到 `inflight` 赋值完成之间没有 await，其他调用方插不进来。
      if (inflight) return inflight;

      const seq = (refreshSeq += 1);
      inflight = (async (): Promise<string | null> => {
        try {
          const next = await options.refresh(current);
          if (next === null || next === '') {
            signOut('refresh-rejected');
            return null;
          }
          setToken(next);
          return next;
        } catch {
          // 网络层失败。**不登出**：会话很可能还活着，只是这次没连上。
          // 让这一次请求以「连不上」的形态失败，页面会显示离线态与备用域名，
          // 用户重试一次就好 —— 而不是回到登录页重新输密码。
          return null;
        } finally {
          if (refreshSeq === seq) inflight = null;
        }
      })();

      return inflight;
    },

    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

/** `POST /api/v1/auth/refresh` 的响应形状（契约 `SessionTokens` 的最小子集）。 */
interface SessionTokensBody {
  data?: { access_token?: unknown; refresh_token?: unknown };
}

export interface RefreshRequestOptions {
  readonly baseUrl: string;
  readonly timeoutMs: number;
  /** 仅测试注入。 */
  readonly fetchImpl?: typeof fetch;
}

/**
 * 直接打 `POST /api/v1/auth/refresh`。
 *
 * **刻意不走 `createApiClient`**，三个理由，每一个单独都足够：
 *  1. 那个客户端在 401 时会去调 refresh —— 用它刷新就是无限递归；
 *  2. 它会自动加 `Authorization` 头，而 refresh 端点是 `security: []`，多带一个头没有意义；
 *  3. **它对 GET 会故障转移到备用域名，而这是 POST**。轮换类请求换域名重发的后果是
 *     两条轮换链，比连不上更糟。走裸 fetch 让这条路径没有任何隐式行为。
 *
 * 返回新 token；服务端明确拒绝（401/4xx）返回 `null`；网络层失败**抛异常**（见 `SessionManagerOptions.refresh`）。
 */
export async function requestSessionRefresh(
  staleToken: string,
  options: RefreshRequestOptions,
): Promise<string | null> {
  const doFetch = options.fetchImpl ?? fetch;
  const url = new URL('/api/v1/auth/refresh', options.baseUrl || currentOrigin()).toString();

  const response = await doFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ refresh_token: staleToken }),
    signal: AbortSignal.timeout(options.timeoutMs),
  });

  if (!response.ok) return null;

  let body: SessionTokensBody;
  try {
    body = (await response.json()) as SessionTokensBody;
  } catch {
    // 200 但不是我们的信封：当作服务端没给出可用会话。**不抛**——
    // 抛异常的语义是「没走到服务端」，而这里明明走到了。
    return null;
  }

  // 后端目前两个字段是同一个值（见文件头 ①）。取 access_token 为准，
  // 缺了再退到 refresh_token —— P2 拆成两枚之后，要存的仍然是 access_token。
  const access = body.data?.access_token;
  if (typeof access === 'string' && access.length > 0) return access;
  const refresh = body.data?.refresh_token;
  if (typeof refresh === 'string' && refresh.length > 0) return refresh;
  return null;
}

function currentOrigin(): string {
  if (typeof window !== 'undefined' && window.location?.origin) return window.location.origin;
  return 'http://localhost';
}
