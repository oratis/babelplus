/**
 * API 客户端。类型全部来自 `schema.d.ts`（由 `pnpm gen:api` 从 `openapi/openapi.yaml` 生成，
 * **生成物提交进仓库，CI 用 `git diff --exit-code` 卡漂移**）。这里只写生成器给不了的四件事：
 *
 *  1. **统一信封的拆封**（user / admin 两面是 `{data, meta}` / `{error, meta}`）
 *  2. **超时 + 备用域名故障转移**（page-inventory §2.2），且**只对幂等方法**
 *  3. **五类错误的归一**（同上，`ApiError.kind`）
 *  4. **401 → 静默 refresh 一次 → 重放原请求一次**（单飞在 `session.ts`）
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

/* ───────────────────────── 平台层拒绝 vs 应用层拒绝 ───────────────────────── */

/**
 * IAP 自己生成的响应带的头。**需实测** —— 本仓从未真的跑过 IAP，
 * 头名取自 GCP 文档而不是抓包，所以它只是**若干条证据里的第一条**，
 * 判别逻辑不允许只依赖它（见 `detectEdgeRejection`）。
 */
export const IAP_GENERATED_HEADER = 'x-goog-iap-generated-response';

/**
 * 判定「这次拒绝来自平台层而不是我们的应用」时所依据的那条证据。
 * 记下来是为了让 UI 能说人话、让日志能复盘 —— 只给一个 boolean 的话，
 * 判错时没有任何线索能追。
 */
export type EdgeRejectionSignal =
  /** 响应带 IAP 自己的标记头。 */
  | 'iap-header'
  /** 请求被 3xx 跟到了别处（IAP 未认证时会往 Google 登录页跳）。 */
  | 'redirected'
  /** 响应体根本不是 JSON（IAP / 反代 / 运营商注入页返回的是 HTML）。 */
  | 'non-json-body'
  /** 是 JSON，但没有我们信封里的 `error.code`。 */
  | 'not-envelope';

export interface EdgeRejection {
  readonly signal: EdgeRejectionSignal;
  readonly contentType: string | undefined;
  /** 跟随重定向后最终落在哪个 URL。**只在 UI 上提示，不做任何自动跳转。** */
  readonly finalUrl: string | undefined;
}

/** 归一后的错误。页面只需要看 `kind`，不必自己解 HTTP 状态码。 */
export class ApiError extends Error {
  /** HTTP 状态码。`0` 表示请求没走到服务端（网络不可达 / 超时）。 */
  readonly status: number;
  readonly kind: ErrorKind;
  /** 契约里的 `ErrorCode` 枚举值；网络层失败时为 `NETWORK`，非信封响应为 `UNKNOWN`。 */
  readonly code: string;
  /** `meta.request_id`（ULID）。**用户报障时直接贴这个串**，所以必须一路带到 UI 上。 */
  readonly requestId: string | undefined;
  readonly details: ErrorBody['details'];
  /**
   * 401 / 403 且判定为**平台层**拒绝时的证据；应用层拒绝为 `null`。
   * 后台（IAP 在前）靠它区分「IAP 会话过期」与「应用会话过期」——
   * 两者的处置完全不同，混成一句「登录已过期」会让运维在错的地方反复重登。
   */
  readonly edge: EdgeRejection | null;
  /** `Retry-After` 响应头（秒）。CORS 里已经 expose（api/internal/middleware/cors.go）。 */
  readonly retryAfterSeconds: number | undefined;

  constructor(init: {
    status: number;
    code: string;
    message: string;
    requestId?: string | undefined;
    details?: ErrorBody['details'];
    edge?: EdgeRejection | null;
    retryAfterSeconds?: number | undefined;
    cause?: unknown;
  }) {
    super(init.message, init.cause === undefined ? undefined : { cause: init.cause });
    this.name = 'ApiError';
    this.status = init.status;
    this.kind = errorKindFromStatus(init.status);
    this.code = init.code;
    this.requestId = init.requestId;
    this.details = init.details;
    this.edge = init.edge ?? null;
    this.retryAfterSeconds = init.retryAfterSeconds;
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

interface EnvelopeShape {
  error?: Partial<ErrorBody>;
  meta?: Partial<Meta>;
}

/** 是不是我们的失败信封：一个对象，`error` 是对象，且 `error.code` 是非空字符串。 */
function asEnvelope(raw: unknown): EnvelopeShape | null {
  if (typeof raw !== 'object' || raw === null) return null;
  const candidate = raw as EnvelopeShape;
  const error = candidate.error;
  if (typeof error !== 'object' || error === null) return null;
  if (typeof error.code !== 'string' || error.code.length === 0) return null;
  return candidate;
}

/**
 * 判断 401/403 是不是平台层（IAP / 反代 / 运营商注入页）拒绝的。
 *
 * 🔴 一条必须写在这里的限制：**跨域时这个判别多半用不上。**
 * IAP 生成的响应不会带我们的 CORS 头，浏览器会在 JS 看到它之前就把它挡掉，
 * fetch 直接抛 `TypeError` → 走到 `networkError`（`status = 0`）。
 * 也就是说跨域部署下，IAP 拒绝与「网络不可达」在前端**无法区分**，
 * UI 必须把两种可能都说出来，而不是断言其中一种（见 admin/src/lib/iap.ts）。
 * 同源部署（SPA 与 API 在同一个 IAP 后面）时下面的判别才完整可用。**这一整段需实测。**
 */
function detectEdgeRejection(response: Response | undefined, raw: unknown): EdgeRejection | null {
  if (!response) return null;
  if (response.status !== 401 && response.status !== 403) return null;

  const contentType = response.headers.get('Content-Type') ?? undefined;
  const finalUrl = response.url || undefined;

  if (response.headers.has(IAP_GENERATED_HEADER)) {
    return { signal: 'iap-header', contentType, finalUrl };
  }
  // 我们的 API 从不对 JSON 请求做 3xx。被跟到别处 = 有人在中间做登录跳转。
  if (response.redirected) {
    return { signal: 'redirected', contentType, finalUrl };
  }
  if (contentType !== undefined && !contentType.includes('json')) {
    return { signal: 'non-json-body', contentType, finalUrl };
  }
  if (asEnvelope(raw) === null) {
    // 是（或声称是）JSON，但没有 `error.code`。**不是我们的应用产生的。**
    return { signal: 'not-envelope', contentType, finalUrl };
  }
  return null;
}

function parseRetryAfter(response: Response | undefined): number | undefined {
  const raw = response?.headers.get('Retry-After');
  if (!raw) return undefined;
  const seconds = Number.parseInt(raw.trim(), 10);
  // 契约里 Retry-After 是秒数形态。HTTP-date 形态解析失败就当没有，
  // **不要猜一个值** —— 猜出来的倒计时会在用户眼皮底下走错。
  return Number.isFinite(seconds) && seconds >= 0 ? seconds : undefined;
}

/**
 * 响应 + 原始错误体 → 归一后的 `ApiError`。
 * 导出是为了可测：它是「五类错误归一」与「平台层判别」两条规则的唯一实现处。
 */
export function toApiError(response: Response | undefined, raw: unknown): ApiError {
  const status = response?.status ?? 0;
  const requestIdHeader = response?.headers.get('X-Request-Id') ?? undefined;
  const edge = detectEdgeRejection(response, raw);
  const retryAfterSeconds = parseRetryAfter(response);

  const envelope = asEnvelope(raw);
  if (envelope) {
    return new ApiError({
      status,
      code: String(envelope.error?.code ?? 'UNKNOWN'),
      message: envelope.error?.message ?? '请求失败',
      requestId: envelope.meta?.request_id ?? requestIdHeader,
      details: envelope.error?.details,
      edge,
      retryAfterSeconds,
    });
  }

  return new ApiError({
    status,
    code: 'UNKNOWN',
    message: status === 0 ? '网络不可达' : `请求失败（HTTP ${status}）`,
    requestId: requestIdHeader,
    edge,
    retryAfterSeconds,
  });
}

/* ───────────────────────────── 传输层 ───────────────────────────── */

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
  /**
   * 401 时用旧 token 换新 token，换到了就**重放一次**原请求。
   * **单飞是实现方的责任**（`createSessionManager().ensureFreshToken`），这里只负责重放一次。
   * 返回 `null` = 别重试了。
   */
  refreshAccessToken?: (staleToken: string | null) => Promise<string | null>;
  /** 哪些 `ErrorCode` 值得去 refresh。默认见 `REFRESHABLE_AUTH_CODES`。 */
  isRefreshableCode?: (code: string) => boolean;
  /**
   * 401 / 403 时的回调（清会话、跳登录、提示走 IAP）。
   *
   * **403 也进这个回调**：后台的平台层拒绝可能以 403 出现，而它和 401 的处置是同一套判别。
   * 调用方自己按 `error.status` / `error.edge` 分流 —— 把分流留在这里会让两个面板共用一套策略，
   * 而它们的策略恰恰不同。
   */
  onAuthFailure?: (error: ApiError) => void;
  /** 每次响应都会带上 request_id，可挂到日志/埋点上。 */
  onRequestId?: (requestId: string, response: Response) => void;
}

/**
 * 幂等方法才允许自动故障转移。
 * POST 不能盲目换域名重发 —— 「下单」「标记已支付」重发一次就是重复扣款。
 * 这条限制必须写死在客户端里，不能指望每个调用点自觉。
 */
const FAILOVER_SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);

/**
 * 触发静默 refresh 的错误码。
 *
 * 契约只说 `AUTH_TOKEN_EXPIRED`，但**后端目前根本不会产生这个码** ——
 * `middleware/user.go` 的 `authFailure()` 对「不存在 / 已吊销 / 已过期」一律回
 * `AUTH_TOKEN_INVALID`，理由写在那个函数的注释里（区分开就等于泄漏「这枚 token 存在过」）。
 * 只认 `AUTH_TOKEN_EXPIRED` 的话，静默 refresh 这条路径**一次都不会被走到**。
 *
 * 所以两个码都认。代价是「会话真的死了」时会多一次注定失败的 refresh 请求 ——
 * 但那是**一次**（单飞），换来的是这条路径在当前后端上真的能用。
 *
 * 名单外的两个码是刻意排除的，不是漏了：
 *  - `AUTH_INVALID_CREDENTIALS` —— 登录接口的 401。拿它去 refresh 毫无意义。
 *  - `AUTH_PERMISSION_DENIED`   —— 账号被封禁。refresh 换不回来一个没被封的身份，
 *    而把封禁显示成「登录过期」会让用户不停重登并开工单（middleware/user.go 明写了这条）。
 */
export const REFRESHABLE_AUTH_CODES: readonly string[] = ['AUTH_TOKEN_EXPIRED', 'AUTH_TOKEN_INVALID'];

/** refresh 端点自己 401 时**绝不能**再去 refresh，否则是无限递归。 */
const REFRESH_PATH = '/api/v1/auth/refresh';

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
 * 一次「已经拆开、可以重发」的请求。
 *
 * 为什么不直接传 `Request`：`Request` 的 body 是一次性的流，读过就没了。
 * 而我们有两条路径需要**重发同一个请求**（故障转移、refresh 后重放），
 * 所以在最外层把 body 读成 `ArrayBuffer` 一次，之后每次尝试各切一份。
 */
interface PreparedRequest {
  method: string;
  url: string;
  headers: Headers;
  body: ArrayBuffer | undefined;
  credentials: RequestCredentials;
  mode: RequestMode;
  redirect: RequestRedirect;
  signal: AbortSignal | null;
}

async function prepareRequest(request: Request): Promise<PreparedRequest> {
  const method = request.method.toUpperCase();
  const hasBody = method !== 'GET' && method !== 'HEAD';
  return {
    method,
    url: request.url,
    headers: new Headers(request.headers),
    body: hasBody ? await request.arrayBuffer() : undefined,
    credentials: request.credentials,
    mode: request.mode === 'navigate' ? 'cors' : request.mode,
    redirect: request.redirect,
    signal: request.signal ?? null,
  };
}

export interface TransportOptions {
  baseUrl: string;
  fallbackBaseUrls: readonly string[];
  timeoutMs: number;
  refreshAccessToken?: ((staleToken: string | null) => Promise<string | null>) | undefined;
  isRefreshableCode?: ((code: string) => boolean) | undefined;
  fetchImpl?: typeof fetch | undefined;
}

/** 带超时与故障转移的发送。**幂等方法才多试备用域名**，见 `FAILOVER_SAFE_METHODS`。 */
function createSender(opts: TransportOptions): (req: PreparedRequest) => Promise<Response> {
  // 每次调用时才取 fetch：建客户端时就把全局 fetch 抓进闭包的话，
  // 测试里换掉全局实现就换不动了（而生产环境下两者没有区别）。
  const doFetch: typeof fetch = (input, init) => (opts.fetchImpl ?? fetch)(input, init);

  return async (req: PreparedRequest): Promise<Response> => {
    const bases = FAILOVER_SAFE_METHODS.has(req.method)
      ? [opts.baseUrl, ...opts.fallbackBaseUrls]
      : [opts.baseUrl];

    let lastFailure: unknown;
    for (let i = 0; i < bases.length; i += 1) {
      const base = bases[i];
      if (!base) continue;
      const url = i === 0 ? req.url : withBase(req.url, base);
      try {
        return await doFetch(url, {
          method: req.method,
          headers: req.headers,
          body: req.body === undefined ? undefined : req.body.slice(0),
          credentials: req.credentials,
          mode: req.mode,
          redirect: req.redirect,
          signal: combineSignals(req.signal, opts.timeoutMs),
        });
      } catch (cause) {
        lastFailure = cause;
        // 用户主动取消不该触发故障转移。
        if (req.signal?.aborted) break;
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

function bearerToken(headers: Headers): string | null {
  const raw = headers.get('Authorization');
  if (!raw) return null;
  const value = raw.startsWith('Bearer ') ? raw.slice(7).trim() : '';
  return value.length > 0 ? value : null;
}

/** 偷看一眼 401 的错误码。**必须 clone**：响应体要原封不动交给 openapi-fetch。 */
async function peekErrorCode(response: Response): Promise<string | null> {
  const contentType = response.headers.get('Content-Type') ?? '';
  if (!contentType.includes('json')) return null;
  try {
    const body = (await response.clone().json()) as unknown;
    const envelope = asEnvelope(body);
    return envelope?.error?.code ?? null;
  } catch {
    return null;
  }
}

/**
 * 传输层：故障转移 + 401 静默 refresh 后**重放一次**。
 *
 * 🔴 为什么重放对 POST 也是安全的，而故障转移不是 —— 这两件事看起来都是「重发一次」，
 * 但风险完全不同，混为一谈会得出错误的结论：
 *
 *  - **故障转移**发生在「没收到响应」之后。请求可能已经到达服务端并被完整处理，
 *    只是响应在回程丢了。换个域名重发 = 可能重复下单。所以只对幂等方法做。
 *  - **401 后重放**发生在「收到了一个明确的 401」之后。这个 401 由鉴权中间件产生，
 *    它在 handler 之前返回（`middleware/user.go` 的 `RequireUser`：鉴权失败直接写响应，
 *    `next.ServeHTTP` 根本不会被调用）。**服务端没有执行任何业务逻辑。**
 *    换上新 token 重放一次，不会产生第二笔副作用。
 *
 * 重放**只做一次**，且重放后无论结果如何都不再刷新。
 *
 * 导出是为了可测：「POST 不故障转移」这条规则的测试直接打这里，
 * 不必绕过 openapi-fetch 的类型层去构造一个 HEAD/OPTIONS 调用。
 */
export function createTransport(opts: TransportOptions): (request: Request) => Promise<Response> {
  const send = createSender(opts);
  const isRefreshable = opts.isRefreshableCode ?? ((code: string) => REFRESHABLE_AUTH_CODES.includes(code));

  return async (request: Request): Promise<Response> => {
    const prepared = await prepareRequest(request);
    const first = await send(prepared);

    if (first.status !== 401) return first;
    if (!opts.refreshAccessToken) return first;
    // refresh 端点自己 401：不能再刷，否则递归。
    if (new URL(prepared.url).pathname.endsWith(REFRESH_PATH)) return first;

    const code = await peekErrorCode(first);
    // 拿不到码（非 JSON / 不是信封）说明这多半是平台层拒绝，refresh 换不回来任何东西。
    if (code === null || !isRefreshable(code)) return first;

    const staleToken = bearerToken(prepared.headers);
    const nextToken = await opts.refreshAccessToken(staleToken);
    if (nextToken === null || nextToken === staleToken) return first;

    const retryHeaders = new Headers(prepared.headers);
    retryHeaders.set('Authorization', `Bearer ${nextToken}`);
    return send({ ...prepared, headers: retryHeaders });
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
    fetch: createTransport({
      baseUrl: options.baseUrl,
      fallbackBaseUrls,
      timeoutMs,
      refreshAccessToken: options.refreshAccessToken,
      isRefreshableCode: options.isRefreshableCode,
    }),
  });

  const auth: Middleware = {
    onRequest({ request }) {
      const token = options.getAccessToken?.() ?? null;
      if (token) request.headers.set('Authorization', `Bearer ${token}`);
      return request;
    },
    async onResponse({ response }) {
      const requestId = response.headers.get('X-Request-Id');
      if (requestId) options.onRequestId?.(requestId, response);

      // 走到这里时，401 的静默 refresh 与重放**都已经发生过了**（见 createTransport）。
      // 也就是说这是最终结果，回调方可以直接按它做处置。
      if ((response.status === 401 || response.status === 403) && options.onAuthFailure) {
        let raw: unknown = undefined;
        try {
          raw = await response.clone().json();
        } catch {
          /* 非 JSON —— detectEdgeRejection 会据此判成平台层拒绝。 */
        }
        options.onAuthFailure(toApiError(response, raw));
      }
      return response;
    },
  };

  client.use(auth);
  return client;
}
