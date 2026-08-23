/**
 * 后台的 API 客户端。**与用户面板是两个独立实例**，共用一个等于把两套故障域焊在一起。
 *
 * 三道闸（page-inventory §4.1），前端只负责其中一道：
 *   闸 1 独立主域名  —— 部署的事，前端管不了，但构建物必须分开（vite.config.ts）
 *   闸 2 IP 白名单 / GCP IAP —— **代理层的事，前端零代码**。IAP 会在请求到达应用前拦掉
 *   闸 3 强制 TOTP  —— 应用层第二因子，前端要做，且**不接受关闭**
 *
 * 🔴 IAP 引入一个必须写进 runbook 的自我引用失效模式：IAP 要求 Google 身份，
 * 而 `google.com` 在中国大陆自 2014 年起被完全封锁。**服务出故障时，身处大陆的运维自己也进不了后台。**
 * 必须准备一条不依赖本服务的备用出网路径并定期演练。这不是前端能解决的，但要写在这里让人看见。
 *
 * 后台**不做**故障转移到备用域名：后台的大陆可达性要求是「不要求」，
 * 而多一个可接受的入口就是多一个要防护的入口。
 *
 * 401 的分流在 `lib/iap.ts` —— IAP 拒绝与应用层拒绝的处置完全相反，见那个文件的表格。
 */
import {
  createApiClient,
  createSessionManager,
  requestSessionRefresh,
  webStorageSessionStore,
  type ApiClient,
  type ApiError,
  type SessionManager,
} from '@babelplus/shared/api';
import { loginUrlWithReturnTo, runtimeConfig } from '@babelplus/shared';
import { classifyAdminAuthFailure, reportAdminAuthFailure } from './iap.ts';
import { navigation } from './navigation.ts';

const ACCESS_TOKEN_KEY = 'bp.admin.access_token';

export const ADMIN_LOGIN_PATH = '/admin/login';

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

let client: ApiClient | null = null;

export function api(): ApiClient {
  if (client) return client;
  const cfg = runtimeConfig();
  client = createApiClient({
    baseUrl: cfg.apiBaseUrl || window.location.origin,
    // 有意留空：后台不做备用域名故障转移，见文件头注释。
    fallbackBaseUrls: [],
    timeoutMs: cfg.requestTimeoutMs,
    getAccessToken: () => session().getToken(),
    refreshAccessToken: (staleToken) => session().ensureFreshToken(staleToken),
    onAuthFailure: handleAuthFailure,
  });
  return client;
}

/**
 * 鉴权失败的分流。**判别本身在 `classifyAdminAuthFailure`（纯函数，有单测）**，
 * 这里只做副作用，这样「判错了」与「处置错了」在排查时是两个可以分开验证的问题。
 */
function handleAuthFailure(error: ApiError): void {
  const failure = classifyAdminAuthFailure(error);
  reportAdminAuthFailure(failure);

  if (!failure.signOutLocally) return;

  session().signOut('rejected');
  const from = `${window.location.pathname}${window.location.search}${window.location.hash}`;
  navigation.navigateTo(loginUrlWithReturnTo(ADMIN_LOGIN_PATH, from), { replace: true });
}

/** 仅测试用。 */
export function resetAdminApiForTests(): void {
  client = null;
  manager = null;
}
