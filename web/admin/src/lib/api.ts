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
 */
import { createApiClient, type ApiClient } from '@babelplus/shared/api';
import { runtimeConfig } from '@babelplus/shared';

const ACCESS_TOKEN_KEY = 'bp.admin.access_token';

export function readAccessToken(): string | null {
  try {
    return window.sessionStorage.getItem(ACCESS_TOKEN_KEY);
  } catch {
    return null;
  }
}

export function writeAccessToken(token: string | null): void {
  try {
    if (token === null) window.sessionStorage.removeItem(ACCESS_TOKEN_KEY);
    else window.sessionStorage.setItem(ACCESS_TOKEN_KEY, token);
  } catch {
    /* 忽略 */
  }
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
    getAccessToken: readAccessToken,
    onUnauthorized: () => {
      writeAccessToken(null);
      // TODO(P1): 跳 /admin/login。注意 IAP 的 401 与应用层的 401 是两回事 ——
      //           IAP 拦截时返回的是 Google 的登录跳转，不是我们的信封格式，
      //           前端要能区分，否则会显示成「登录状态过期」而实际是 IAP 会话过期。
    },
  });
  return client;
}
