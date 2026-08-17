/**
 * API 客户端。类型全部来自 `schema.d.ts`（由 `pnpm gen:api` 从 `openapi/openapi.yaml` 生成，
 * **生成物提交进仓库，CI 用 `git diff --exit-code` 卡漂移**）。这里只写生成器给不了的三件事：
 *
 *  1. **统一信封的拆封**（user / admin 两面是 `{data, meta}` / `{error, meta}`）
 *  2. **超时 + 备用域名故障转移**（page-inventory §2.2）
 *  3. **五类错误的归一**（同上，`ApiError.kind`）
 *
 * 刻意**不**做的事：不引第三方 HTTP 库、不做请求缓存层、不做全局 store。
 * 缓存与状态管理的选型还没裁决（page-inventory §8「视觉设计与组件库未定」），
 * 现在选等于替以后的人做决定。
 */
import createClient, { type Client, type Middleware } from 'openapi-fetch';
import type { components, paths } from './schema.d.ts';
import { errorKindFromStatus, type ErrorKind } from '../src/lib/error-kind.ts';

export type Meta = components['schemas']['Meta'];
export type ErrorBody = components['schemas']['ErrorBody'];
export type ErrorCode = components['schemas']['ErrorCode'];

export type ApiClient = Client<paths>;

/** 归一后的错误。页面只需要看 `kind`，不必自己解 HTTP 状态码。 */
export class ApiError extends Error {
  /** HTTP 状态码。`0` 表示请求没走到服务端（网络不可达 / 超时）。 */
  readonly status: number;
  readonly kind: ErrorKind;
  /** 契约里的 `ErrorCode` 枚举值；网络层失败时为 `NETWORK`。 */
  readonly code: string;
  /** `meta.request_id`（ULID）。**用户报障时直接贴这个串**，所以必须一路带到 UI 上。 */
  readonly requestId: string | undefined;
  readonly details: ErrorBody['details'];

  constructor(init: {
    status: number;
    code: string;
    message: string;
    requestId?: string | undefined;
    details?: ErrorBody['details'];
    cause?: unknown;
  }) {
    super(init.message, init.cause === undefined ? undefined : { cause: init.cause });
    this.name = 'ApiError';
    this.status = init.status;
    this.kind = errorKindFromStatus(init.status);
    this.code = init.code;
    this.requestId = init.requestId;
    this.details = init.details;
  }
}

/** 网络层失败（请求没走到服务端）。`status = 0` → `kind = 'offline'`。 */
export function networkError(message: string, cause?: unknown): ApiError {
  return new ApiError({ status: 0, code: 'NETWORK', message, cause });
}

/** openapi-fetch 的返回形状。用最小结构约束，避免把生成类型的联合类型摊开。 */
export interface FetchResult<D> {
  data?: D | undefined;
  error?: unknown;
  response: Response;
}

/**
 * 拆信封。成功时返回 `data.data`；失败时抛 `ApiError`。
 *
 * 为什么抛而不是返回 `Result`：页面里 90% 的调用点只关心成功路径，
 * 剩下 10% 用一个统一的错误边界接住。返回 `Result` 会让每个调用点都长出一段样板。
 */
export async function unwrap<D>(
  call: Promise<FetchResult<{ data: D; meta: Meta }>>,
): Promise<D> {
  const result = await call;
  if (result.error !== undefined || result.data === undefined) {
    throw toApiError(result.response, result.error);
  }
  return result.data.data;
}

/** 只要 meta（分页游标在这里）时用它。 */
export async function unwrapWithMeta<D>(
  call: Promise<FetchResult<{ data: D; meta: Meta }>>,
): Promise<{ data: D; meta: Meta }> {
  const result = await call;
  if (result.error !== undefined || result.data === undefined) {
    throw toApiError(result.response, result.error);
  }
  return result.data;
}

/** 204 之类没有响应体的端点。 */
export async function unwrapEmpty(call: Promise<FetchResult<unknown>>): Promise<void> {
  const result = await call;
  if (result.error !== undefined) {
    throw toApiError(result.response, result.error);
  }
}

function toApiError(response: Response | undefined, raw: unknown): ApiError {
  const status = response?.status ?? 0;
  const requestIdHeader = response?.headers.get('X-Request-Id') ?? undefined;

  if (typeof raw === 'object' && raw !== null && 'error' in raw) {
    const envelope = raw as { error?: Partial<ErrorBody>; meta?: Partial<Meta> };
    return new ApiError({
      status,
      code: String(envelope.error?.code ?? 'UNKNOWN'),
      message: envelope.error?.message ?? '请求失败',
      requestId: envelope.meta?.request_id ?? requestIdHeader,
      details: envelope.error?.details,
    });
  }

  return new ApiError({
    status,
    code: 'UNKNOWN',
    message: status === 0 ? '网络不可达' : `请求失败（HTTP ${status}）`,
    requestId: requestIdHeader,
  });
}

export interface CreateApiClientOptions {
  /** 主 API 域名。 */
  baseUrl: string;
  /**
   * 备用 API 域名池。**超时或网络失败后按顺序重试一次**（page-inventory §2.2）。
   * 只重试一次是有意的：重试 N 次会把「慢」放大成「更慢」，而慢在大陆是常态不是异常。
   */
  fallbackBaseUrls?: readonly string[];
  /** 单次尝试的超时。⚠️ 默认 15000 是提案值，**需实测**。 */
  timeoutMs?: number;
  /** 取当前 access token。返回 `null` 表示未登录，不加 `Authorization` 头。 */
  getAccessToken?: () => string | null;
  /** 收到 401 时的回调（清会话、跳登录）。 */
  onUnauthorized?: (error: ApiError) => void;
  /** 每次响应都会带上 request_id，可挂到日志/埋点上。 */
  onRequestId?: (requestId: string, response: Response) => void;
}

/**
 * 幂等方法才允许自动故障转移。
 * POST 不能盲目换域名重发 —— 「下单」「标记已支付」重发一次就是重复扣款。
 * 这条限制必须写死在客户端里，不能指望每个调用点自觉。
 */
const FAILOVER_SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);

function withBase(originalUrl: string, base: string): string {
  const target = new URL(base);
  const url = new URL(originalUrl);
  url.protocol = target.protocol;
  url.host = target.host;
  // base 带路径前缀时（例如反代到 /api 下）保留它。
  const prefix = target.pathname.replace(/\/$/, '');
  if (prefix && !url.pathname.startsWith(prefix)) {
    url.pathname = prefix + url.pathname;
  }
  return url.toString();
}

function combineSignals(userSignal: AbortSignal | null, timeoutMs: number): AbortSignal {
  const timeout = AbortSignal.timeout(timeoutMs);
  if (!userSignal) return timeout;
  // AbortSignal.any 在 Safari 17+/Chrome 124+ 可用。取不到时退化为只用超时信号。
  const anyFn = (AbortSignal as unknown as { any?: (s: AbortSignal[]) => AbortSignal }).any;
  return typeof anyFn === 'function' ? anyFn([userSignal, timeout]) : timeout;
}

/**
 * 带超时与故障转移的 fetch。openapi-fetch 用 `Request` 调它，
 * 所以这里先把 body 读成 ArrayBuffer 再重建 Request —— 直接 `new Request(url, req)`
 * 在 body 是流的情况下需要 `duplex: 'half'`，不同运行时行为不一致，读一次最稳。
 */
function createResilientFetch(opts: Required<Pick<CreateApiClientOptions, 'baseUrl'>> & {
  fallbackBaseUrls: readonly string[];
  timeoutMs: number;
}): (request: Request) => Promise<Response> {
  return async (request: Request): Promise<Response> => {
    const method = request.method.toUpperCase();
    const headers = new Headers(request.headers);
    const hasBody = method !== 'GET' && method !== 'HEAD';
    const body = hasBody ? await request.arrayBuffer() : undefined;

    const bases = FAILOVER_SAFE_METHODS.has(method)
      ? [opts.baseUrl, ...opts.fallbackBaseUrls]
      : [opts.baseUrl];

    let lastFailure: unknown;
    for (let i = 0; i < bases.length; i += 1) {
      const base = bases[i];
      if (!base) continue;
      const url = i === 0 ? request.url : withBase(request.url, base);
      try {
        return await fetch(url, {
          method,
          headers,
          body: body === undefined ? undefined : body.slice(0),
          credentials: request.credentials,
          mode: request.mode === 'navigate' ? 'cors' : request.mode,
          redirect: request.redirect,
          signal: combineSignals(request.signal, opts.timeoutMs),
        });
      } catch (cause) {
        lastFailure = cause;
        // 用户主动取消不该触发故障转移。
        if (request.signal?.aborted) break;
      }
    }

    const tried = bases.length;
    throw networkError(
      tried > 1
        ? `主域名与 ${tried - 1} 个备用域名都连不上（每次 ${opts.timeoutMs}ms 超时）`
        : `连不上 API（${opts.timeoutMs}ms 超时）`,
      lastFailure,
    );
  };
}

/**
 * 建一个 API 客户端。用户面板与后台各建各的 —— 它们的域名、凭据、失败处理都不一样，
 * 共用一个实例等于把两套故障域焊在一起。
 */
export function createApiClient(options: CreateApiClientOptions): ApiClient {
  const timeoutMs = options.timeoutMs ?? 15_000;
  const fallbackBaseUrls = options.fallbackBaseUrls ?? [];

  const client = createClient<paths>({
    baseUrl: options.baseUrl,
    fetch: createResilientFetch({ baseUrl: options.baseUrl, fallbackBaseUrls, timeoutMs }),
  });

  const auth: Middleware = {
    onRequest({ request }) {
      const token = options.getAccessToken?.() ?? null;
      if (token) request.headers.set('Authorization', `Bearer ${token}`);
      return request;
    },
    onResponse({ response }) {
      const requestId = response.headers.get('X-Request-Id');
      if (requestId) options.onRequestId?.(requestId, response);
      if (response.status === 401) {
        // TODO(P1): 契约写明 `AUTH_TOKEN_EXPIRED` 时前端应静默 refresh 一次
        //           （securitySchemes.userSession）。refresh 是一次性轮换，
        //           必须做单飞（并发请求只发一次 refresh），否则会互相把 refresh token 作废。
        options.onUnauthorized?.(
          new ApiError({ status: 401, code: 'AUTH_TOKEN_EXPIRED', message: '登录状态已过期' }),
        );
      }
      return response;
    },
  };

  client.use(auth);
  return client;
}
