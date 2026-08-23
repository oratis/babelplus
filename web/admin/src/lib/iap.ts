/**
 * **IAP 的 401 与应用层的 401 是两回事。** 这个文件负责把它们分开。
 *
 * 三道闸里，闸 2 是 IP 白名单 / GCP IAP（page-inventory §4.1）。IAP 挡在应用前面，
 * 它拒绝时请求**根本没到我们的服务**：没有 request_id、没有统一信封、没有 ErrorCode。
 * 而应用层的 401 是我们自己写的 JSON 信封。两者的处置完全相反：
 *
 * | | 平台层（IAP）拒绝 | 应用层拒绝 |
 * |---|---|---|
 * | 用户该做什么 | 重新完成 **Google 身份**认证（IAP 登录） | 重新输入后台密码 + TOTP |
 * | 前端该做什么 | **什么都不要清**，提示去 IAP 重新登录 | 清掉本地会话，跳 `/admin/login` |
 * | 跳 `/admin/login` 有用吗 | **没用** —— 那一页本身也在 IAP 后面，一样打不开 | 有用 |
 *
 * 把前者显示成「登录状态已过期」的后果不是文案难看，是**运维在错误的地方反复重登**：
 * 输一百次后台密码也不会让 IAP 放行，而这通常发生在服务已经出故障、时间最紧的时候。
 *
 * 🔴 **一条必须说清楚的能力上限（需实测）：跨域部署时这个判别多半用不上。**
 * IAP 生成的响应不带我们的 CORS 头，浏览器会在 JS 看到它之前就拦掉，
 * `fetch` 直接抛 `TypeError` → 归一成 `status = 0` 的「网络不可达」。
 * 所以 `status = 0` 在后台被单独归成 `ambiguous-network` 而不是 `offline`：
 * 它**可能**是网络不通，也**可能**是 IAP 会话过期被跨域拦下，
 * 而在大陆这两件事还会同时发生（IAP 依赖 google.com，本身就连不上）。
 * UI 必须把两种可能都说出来 —— 断言其中一种就是在猜。
 * 本仓从未真的跑过 IAP，上面这一整段的实际表现**需实测**。
 */
import type { ApiError } from '@babelplus/shared/api';

export type AdminAuthFailureKind =
  /** 平台层拒绝（IAP / 反代 / 注入页）。 */
  | 'edge'
  /** 我们的应用返回的 401：会话过期或无效。 */
  | 'app'
  /** 我们的应用返回的 403：身份没问题但被拒（封禁 / 缺 TOTP / TOTP 错）。 */
  | 'forbidden'
  /** 请求没走到服务端。**在后台这不等于「网络不可达」**，见文件头。 */
  | 'ambiguous-network';

export interface AdminAuthFailure {
  readonly kind: AdminAuthFailureKind;
  readonly title: string;
  readonly description: string;
  /** 判定依据。写进 UI 与日志 —— 判错时这是唯一能追的线索。 */
  readonly evidence: string;
  /** 清掉本地会话并跳 `/admin/login` 吗。平台层拒绝时**一律 false**。 */
  readonly signOutLocally: boolean;
  readonly requestId: string | undefined;
}

/**
 * 判别。**纯函数，无副作用** —— 它是这条规则的唯一实现处，也是单测直接打的地方。
 *
 * 判定顺序有意义：`edge` 必须排在 `403` 前面。IAP 对「认证过但没权限」返回的是 403，
 * 先判状态码会把它错认成应用层的权限不足。
 */
export function classifyAdminAuthFailure(error: ApiError): AdminAuthFailure {
  const requestId = error.requestId;

  if (error.status === 0) {
    return {
      kind: 'ambiguous-network',
      title: '请求没能到达后台',
      description:
        '有两种可能，前端分不出是哪一种：① 网络到 API 不通；② IAP 会话已过期，' +
        '浏览器在跨域检查时就把 IAP 的响应拦掉了。先在新标签页打开后台域名确认 IAP 是否还认你，' +
        '再判断是不是网络问题。',
      evidence: '请求未走到服务端（status = 0）',
      signOutLocally: false,
      requestId,
    };
  }

  if (error.edge) {
    return {
      kind: 'edge',
      title: '被平台层挡下了，不是后台密码的问题',
      description:
        '这个响应不是我们的服务产生的 —— 多半是 IAP 认为你的 Google 身份已过期。' +
        '重新输入后台密码没有用。请在新标签页打开后台域名，完成 Google 登录后再回来刷新。',
      evidence: edgeEvidenceText(error),
      // 🔴 不清本地会话：应用会话很可能完全有效，清掉只会让运维在 IAP 通过之后还要再登一次。
      signOutLocally: false,
      requestId,
    };
  }

  if (error.status === 403) {
    return {
      kind: 'forbidden',
      title: '身份通过了，但这个操作被拒绝',
      description: forbiddenDescription(error.code),
      evidence: `应用层 403 · ${error.code}`,
      signOutLocally: false,
      requestId,
    };
  }

  return {
    kind: 'app',
    title: '后台登录状态已过期',
    description: '请重新输入后台密码与验证器上的 6 位码。',
    evidence: `应用层 ${error.status} · ${error.code}`,
    signOutLocally: true,
    requestId,
  };
}

function edgeEvidenceText(error: ApiError): string {
  const signal = error.edge?.signal;
  switch (signal) {
    case 'iap-header':
      return '响应带 IAP 自己的标记头（需实测：头名取自 GCP 文档，未抓包核实）';
    case 'redirected':
      return `请求被重定向到 ${error.edge?.finalUrl ?? '别处'}（我们的 API 从不对 JSON 请求做 3xx）`;
    case 'non-json-body':
      return `响应体不是 JSON（Content-Type: ${error.edge?.contentType ?? '缺失'}）`;
    case 'not-envelope':
      return '响应是 JSON 但没有我们信封里的 error.code';
    default:
      return '响应不符合统一信封';
  }
}

/** 403 的三个应用层来源，文案各不相同（契约 `ErrForbidden` 列了它们）。 */
function forbiddenDescription(code: string): string {
  switch (code) {
    case 'AUTH_TOTP_REQUIRED':
      return '这个操作需要先完成两步验证。TOTP 是强制的，没有跳过分支。';
    case 'AUTH_TOTP_INVALID':
      return '验证器上的 6 位码不正确或已过期。等下一个周期再输一次。';
    case 'AUTH_PERMISSION_DENIED':
      return '当前管理员账号没有执行这个操作的权限。';
    default:
      return '当前身份不被允许执行这个操作。';
  }
}

/* ─────────────────── 观察到的最近一次鉴权失败（给 UI 用） ─────────────────── */

let current: AdminAuthFailure | null = null;
const listeners = new Set<() => void>();

export function reportAdminAuthFailure(failure: AdminAuthFailure | null): void {
  current = failure;
  for (const listener of listeners) listener();
}

export function subscribeAdminAuthFailure(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** `useSyncExternalStore` 的 getSnapshot：必须返回稳定引用，所以直接给出模块级变量。 */
export function getAdminAuthFailure(): AdminAuthFailure | null {
  return current;
}
