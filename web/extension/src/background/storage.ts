/**
 * 存储层的最小抽象：`chrome.storage.local`（跨浏览器重启）与 `chrome.storage.session`（浏览器关闭即丢）。
 *
 * 抽成接口是为了让 controller 的测试用内存实现跑，而不是 mock 一个 `chrome` 全局；
 * 也为了把「哪些东西必须在关浏览器时消失」写成两个不同的对象，而不是一个字符串前缀。
 *
 * 分类（改动前先读 `KEY`）：
 *  - session：代理凭据。它们随 `/user/proxy-config` 轮换，丢了重拉即可；落到磁盘上没有任何收益。
 *  - local：会话 token（「开机自动连接」需要它跨重启；用户面板存 sessionStorage 是因为面板没有这个需求）、
 *    偏好、上次连接状态、配额快照、配置快照、控制面域名池。
 */
export interface KeyValue {
  get<T>(key: string): Promise<T | undefined>;
  set<T>(key: string, value: T): Promise<void>;
  remove(key: string): Promise<void>;
}

export const KEY = {
  token: 'session.token',
  prefs: 'prefs',
  connection: 'connection',
  subscription: 'subscription',
  subscriptionAt: 'subscription.fetchedAt',
  config: 'config',
  configAt: 'config.fetchedAt',
  apiBaseUrls: 'control.apiBaseUrls',
  links: 'control.links',
  lastError: 'lastError',
  /** session 存储。 */
  credentials: 'proxy.credentials',
} as const;

function chromeArea(area: chrome.storage.StorageArea): KeyValue {
  return {
    async get<T>(key: string) {
      const bag = await area.get(key);
      return bag[key] as T | undefined;
    },
    async set<T>(key: string, value: T) {
      await area.set({ [key]: value });
    },
    async remove(key: string) {
      await area.remove(key);
    },
  };
}

export function chromeLocalStorage(): KeyValue {
  return chromeArea(chrome.storage.local);
}

export function chromeSessionStorage(): KeyValue {
  return chromeArea(chrome.storage.session);
}

/** 测试与兜底用的内存实现。 */
export function memoryStorage(initial: Record<string, unknown> = {}): KeyValue & { dump(): Record<string, unknown> } {
  const map = new Map<string, unknown>(Object.entries(initial));
  return {
    async get<T>(key: string) {
      return map.get(key) as T | undefined;
    },
    async set<T>(key: string, value: T) {
      map.set(key, structuredClone(value));
    },
    async remove(key: string) {
      map.delete(key);
    },
    dump() {
      return Object.fromEntries(map);
    },
  };
}
