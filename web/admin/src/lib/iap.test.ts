/**
 * 「IAP 的 401 与应用层的 401 是两回事」——这条判别的测试。
 *
 * 判错的代价不对称，所以两个方向都要钉住：
 *  - 把**平台层**拒绝判成应用层 → 前端清掉一个完全有效的会话并跳 `/admin/login`，
 *    而那一页本身也在 IAP 后面，一样打不开。运维在错的地方反复输密码。
 *  - 把**应用层**拒绝判成平台层 → 提示「去 IAP 重新登录」，而 IAP 明明是通的。
 *
 * 输入用真的 `Response` 走一遍 `toApiError`，而不是手搓 `ApiError`：
 * 判别依据（Content-Type、重定向、信封形状）全在响应对象上，
 * 跳过那一层就等于把被测逻辑的一半换成了测试里的假设。
 */
import { describe, expect, it } from 'vitest';
import { IAP_GENERATED_HEADER, toApiError, networkError } from '@babelplus/shared/api';
import { classifyAdminAuthFailure } from './iap.ts';

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function appEnvelope(code: string) {
  return { error: { code, message: '会话无效或已过期' }, meta: { request_id: '01K2ADMINADMINADMINADMINAD' } };
}

describe('classifyAdminAuthFailure', () => {
  it('IAP 返回的 HTML 登录页 → 平台层，**不清本地会话**', () => {
    const response = new Response('<html>Sign in with Google</html>', {
      status: 401,
      headers: { 'Content-Type': 'text/html; charset=utf-8' },
    });
    const failure = classifyAdminAuthFailure(toApiError(response, '<html>…</html>'));

    expect(failure.kind).toBe('edge');
    expect(failure.signOutLocally).toBe(false);
    expect(failure.evidence).toContain('text/html');
  });

  it('带 IAP 标记头 → 平台层', () => {
    const body = appEnvelope('AUTH_TOKEN_INVALID');
    const failure = classifyAdminAuthFailure(
      toApiError(jsonResponse(401, body, { [IAP_GENERATED_HEADER]: 'true' }), body),
    );
    expect(failure.kind).toBe('edge');
    expect(failure.signOutLocally).toBe(false);
  });

  it('IAP 的 403（认证过但没权限）不会被状态码抢先判成应用层权限不足', () => {
    const response = new Response('<html>You do not have access</html>', {
      status: 403,
      headers: { 'Content-Type': 'text/html' },
    });
    const failure = classifyAdminAuthFailure(toApiError(response, '<html>…</html>'));
    expect(failure.kind).toBe('edge');
  });

  it('我们自己的 401 信封 → 应用层，清会话并跳登录', () => {
    const body = appEnvelope('AUTH_TOKEN_INVALID');
    const failure = classifyAdminAuthFailure(toApiError(jsonResponse(401, body), body));

    expect(failure.kind).toBe('app');
    expect(failure.signOutLocally).toBe(true);
    expect(failure.requestId).toBe('01K2ADMINADMINADMINADMINAD');
  });

  it.each([
    ['AUTH_TOTP_REQUIRED', '两步验证'],
    ['AUTH_TOTP_INVALID', '6 位码'],
    ['AUTH_PERMISSION_DENIED', '权限'],
  ])('应用层 403 %s → forbidden，文案按 code 分支且不清会话', (code, fragment) => {
    const body = { error: { code, message: 'x' }, meta: { request_id: '01K2X' } };
    const failure = classifyAdminAuthFailure(toApiError(jsonResponse(403, body), body));

    expect(failure.kind).toBe('forbidden');
    expect(failure.signOutLocally).toBe(false);
    expect(failure.description).toContain(fragment);
  });

  it('请求没走到服务端 → ambiguous-network，**必须把两种可能都说出来**', () => {
    const failure = classifyAdminAuthFailure(networkError('连不上 API'));

    expect(failure.kind).toBe('ambiguous-network');
    expect(failure.signOutLocally).toBe(false);
    // 跨域时 IAP 的响应会被浏览器在 CORS 阶段拦掉，前端只看得到一个 TypeError。
    // 断言成「网络不通」就是在猜，所以文案里两种可能都要在。
    expect(failure.description).toContain('IAP');
    expect(failure.description).toContain('网络');
  });
});
