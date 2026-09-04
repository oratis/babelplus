/**
 * 会员中心 API 客户端。**契约类型从生成物取**（`web/shared/api/schema.d.ts`，
 * 由 `pnpm gen:api` 从 `openapi/openapi.yaml` 生成并入库，CI 用 `git diff --exit-code` 卡漂移）——
 * 这样契约改了字段名，这里 `tsc` 就红，而不是等到用户点不动。
 * 这是一个**只在类型层**的跨目录引用，运行时没有任何依赖。
 *
 * 与扩展的差别：扩展复用 `@babelplus/shared/api`（openapi-fetch + 故障转移 + refresh 单飞），
 * 浏览器这里是一份**更小的手写实现** —— 它只调三个端点，而把 openapi-fetch 与整个 web 工作区
 * 拖进 Electron 项目的代价（两个 lockfile、两套构建）远大于省下的这一百行。
 * 代价记在 README：401 静默 refresh 只做一次、不做单飞（浏览器是单进程单会话，并发 refresh 的场景不成立）。
 */
import type { components } from '../../../web/shared/api/schema';

export type SubscriptionSummary = components['schemas']['SubscriptionSummary'];
export type SubscriptionUrls = components['schemas']['SubscriptionUrls'];

export interface UserSubscription {
  readonly urls: SubscriptionUrls;
  readonly summary: SubscriptionSummary;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

export interface ApiOptions {
  /** API 域名池：第一个是主域名，其余在**幂等请求**超时/不可达时依次重试一次。 */
  readonly baseUrls: readonly string[];
  readonly timeoutMs?: number;
  readonly getToken: () => string | null;
  readonly setToken: (token: string | null) => void;
  readonly fetchImpl?: typeof fetch;
}

interface Envelope<T> {
  data?: T;
  error?: { code?: string; message?: string };
  meta?: { request_id?: string };
}

const IDEMPOTENT = new Set(['GET', 'HEAD']);

export class Api {
  private readonly o: ApiOptions;

  constructor(options: ApiOptions) {
    this.o = options;
  }

  private get bases(): readonly string[] {
    return this.o.baseUrls.filter((b) => b.length > 0);
  }

  private async raw(path: string, init: RequestInit & { method: string }): Promise<Response> {
    const doFetch = this.o.fetchImpl ?? fetch;
    const bases = IDEMPOTENT.has(init.method) ? this.bases : this.bases.slice(0, 1);
    if (bases.length === 0) throw new ApiError(0, 'NOT_CONFIGURED', '这个构建里没有配置服务地址');
    let last: unknown;
    for (const base of bases) {
      try {
        return await doFetch(new URL(path, base).toString(), {
          ...init,
          signal: AbortSignal.timeout(this.o.timeoutMs ?? 15_000),
        });
      } catch (cause) {
        last = cause;
      }
    }
    throw new ApiError(0, 'NETWORK', `连不上服务（试过 ${bases.length} 个地址）：${String(last)}`);
  }

  /** 带鉴权的请求；401 时静默 refresh 一次再重放一次。 */
  private async call<T>(path: string, method: string, body?: unknown): Promise<T> {
    const send = async (): Promise<Response> => {
      const headers: Record<string, string> = { Accept: 'application/json' };
      const token = this.o.getToken();
      if (token) headers['Authorization'] = `Bearer ${token}`;
      if (body !== undefined) headers['Content-Type'] = 'application/json';
      return this.raw(path, {
        method,
        headers,
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
    };

    let res = await send();
    if (res.status === 401 && this.o.getToken() && !path.endsWith('/auth/refresh')) {
      const refreshed = await this.refresh();
      // refresh 失败 = 会话真的死了。**不重试**，让调用方看到 401 并要求重新登录。
      if (refreshed) res = await send();
    }
    return this.unwrap<T>(res);
  }

  private async unwrap<T>(res: Response): Promise<T> {
    if (res.status === 204) return undefined as T;
    let parsed: Envelope<T> | null = null;
    const text = await res.text();
    try {
      parsed = text ? (JSON.parse(text) as Envelope<T>) : null;
    } catch {
      parsed = null;
    }
    if (!res.ok) {
      const code = parsed?.error?.code ?? (res.status === 501 ? 'NOT_IMPLEMENTED' : 'UNKNOWN');
      const message = parsed?.error?.message ?? `请求失败（HTTP ${res.status}）`;
      throw new ApiError(res.status, code, message);
    }
    if (parsed && 'data' in parsed && parsed.data !== undefined) return parsed.data;
    // 订阅面是**裸响应**（不套信封），走 fetchText 而不是这里。走到这说明契约变了。
    throw new ApiError(res.status, 'MALFORMED', '响应不是统一信封');
  }

  private async refresh(): Promise<boolean> {
    const stale = this.o.getToken();
    if (!stale) return false;
    try {
      const res = await this.raw('/api/v1/auth/refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ refresh_token: stale }),
      });
      if (!res.ok) {
        this.o.setToken(null);
        return false;
      }
      const parsed = (await res.json()) as Envelope<{ access_token?: string }>;
      const next = parsed.data?.access_token;
      if (typeof next !== 'string' || next.length === 0) {
        this.o.setToken(null);
        return false;
      }
      this.o.setToken(next);
      return true;
    } catch {
      // 网络层失败：会话**可能仍然有效**，不要把一次跨境抖动表现为「无故被登出」。
      return false;
    }
  }

  async login(email: string, password: string): Promise<void> {
    const tokens = await this.call<{ access_token: string }>('/api/v1/auth/login', 'POST', { email, password });
    this.o.setToken(tokens.access_token);
  }

  async logout(): Promise<void> {
    try {
      await this.call<void>('/api/v1/auth/logout', 'POST');
    } finally {
      this.o.setToken(null);
    }
  }

  getSubscription(): Promise<UserSubscription> {
    return this.call<UserSubscription>('/api/v1/user/subscription', 'GET');
  }

  /**
   * 拉订阅正文。**这条是订阅面**：裸响应、不套信封、按 UA / flag 分格式，
   * 所以不能走 `unwrap`。URL 直接来自会员中心下发的 `urls.singbox`，我们不自己拼 token。
   */
  async fetchSubscriptionBody(url: string): Promise<string> {
    const doFetch = this.o.fetchImpl ?? fetch;
    const res = await doFetch(url, {
      method: 'GET',
      headers: { Accept: 'application/json' },
      signal: AbortSignal.timeout(this.o.timeoutMs ?? 15_000),
    });
    if (!res.ok) {
      throw new ApiError(res.status, res.status === 404 ? 'SUB_NOT_FOUND' : 'UNKNOWN', `订阅拉取失败（HTTP ${res.status}）`);
    }
    return res.text();
  }
}
