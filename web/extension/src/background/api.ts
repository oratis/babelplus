/**
 * service worker 里的 API 客户端。复用 `@babelplus/shared/api`：统一信封拆封、超时 + 备用域名故障转移
 * （只对幂等方法）、401 静默 refresh 单飞 —— 这些都是用户面板已经踩过坑的实现，扩展不该再写第二份。
 *
 * 与面板的两点不同：
 *  1. token 存 `chrome.storage.local`（面板存 sessionStorage）：「开机自动连接」要求登录态跨浏览器重启。
 *     `SessionStore` 是同步接口而 chrome.storage 是异步的，所以用一份内存镜像 + 写穿透，
 *     service worker 每次唤醒先 `load()` 再用客户端。
 *  2. 域名池分两层：构建期 `VITE_BP_*` 只是兜底，运行时以 `/user/proxy-config` 的 `control_plane` 为准，
 *     `setBases` 一调，下一次请求就走新的池（客户端按 base 惰性重建）。
 *
 * 测试里不注入 fetch：shared 的传输层每次调用时才取全局 `fetch`，`vi.stubGlobal('fetch', …)` 就够了。
 */
import {
  createApiClient,
  createSessionManager,
  requestSessionRefresh,
  type ApiClient,
  type ApiError,
  type SessionManager,
  type SessionStore,
} from '@babelplus/shared/api';
import type { KeyValue } from './storage.ts';

export interface MirroredStore {
  readonly store: SessionStore;
  load(): Promise<void>;
}

/** 内存镜像 + 写穿透到 `kv`。`load()` 之前 `read()` 返回 `null`，所以调用方必须先 load。 */
export function mirroredSessionStore(kv: KeyValue, key: string): MirroredStore {
  let value: string | null = null;
  return {
    store: {
      read: () => value,
      write: (token) => {
        value = token;
        void (token === null ? kv.remove(key) : kv.set(key, token));
      },
    },
    async load() {
      value = (await kv.get<string>(key)) ?? null;
    },
  };
}

export interface ExtensionApiOptions {
  readonly store: SessionStore;
  readonly timeoutMs: number;
  readonly onAuthFailure?: ((error: ApiError) => void) | undefined;
}

export interface ExtensionApi {
  /** 当前域名池；空数组 = 未配置，任何请求都会失败。 */
  bases(): readonly string[];
  setBases(bases: readonly string[]): void;
  /** 未配置时抛 `NotConfiguredError`。 */
  client(): ApiClient;
  readonly session: SessionManager;
}

export class NotConfiguredError extends Error {
  readonly code = 'NOT_CONFIGURED';
  constructor() {
    super('No service address is configured in this build.');
    this.name = 'NotConfiguredError';
  }
}

export function createExtensionApi(initialBases: readonly string[], opts: ExtensionApiOptions): ExtensionApi {
  let bases: readonly string[] = initialBases.filter((b) => b.length > 0);
  let client: ApiClient | null = null;
  let builtFor = '';

  const session = createSessionManager({
    store: opts.store,
    refresh: (staleToken) => {
      const primary = bases[0];
      if (!primary) return Promise.resolve(null);
      return requestSessionRefresh(staleToken, { baseUrl: primary, timeoutMs: opts.timeoutMs });
    },
  });

  return {
    session,
    bases: () => bases,
    setBases(next) {
      bases = next.filter((b) => b.length > 0);
    },
    client() {
      const primary = bases[0];
      if (!primary) throw new NotConfiguredError();
      const signature = bases.join('\n');
      if (client && builtFor === signature) return client;
      client = createApiClient({
        baseUrl: primary,
        fallbackBaseUrls: bases.slice(1),
        timeoutMs: opts.timeoutMs,
        getAccessToken: () => session.getToken(),
        refreshAccessToken: (stale) => session.ensureFreshToken(stale),
        ...(opts.onAuthFailure ? { onAuthFailure: opts.onAuthFailure } : {}),
      });
      builtFor = signature;
      return client;
    },
  };
}
