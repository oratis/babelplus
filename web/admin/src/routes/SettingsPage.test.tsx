// @vitest-environment jsdom

/**
 * 系统配置的接线测试。
 *
 * 🔴 **两个用例是任务书逐字要求的「参数没收齐时不许提交」：**
 * 原因不足 8 个码位时按钮变灰、且点下去**一个请求都不发**；
 * 某个键的文本解不出 JSON 时它不进 `values`，按钮同样变灰。
 *
 * ⚠️ 这些用例**证明不了安全性** —— §6.2 的四层全在服务端强制，
 * 这里钉的只是「前端有没有在收齐之前就把请求发出去」。
 *
 * 另一条容易退化的是**保存成功后不许再发一次 GET**：
 * PATCH 返回的就是全量新配置，重发会把页面打回骨架屏 ——
 * 操作者刚点完保存，眼前的配置突然消失，第一反应是「是不是没存上」。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import SettingsPage from './SettingsPage.tsx';

const SETTINGS = '/api/v1/admin/settings';

interface Seen {
  readonly method: string;
  readonly url: URL;
  readonly body: string | null;
}

type StubHandler = (req: Seen) => Response;

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function ok(data: unknown): unknown {
  return { data, meta: { request_id: '01K2SETSETSETSETSETSETSETS' } };
}

function fail(
  status: number,
  code: string,
  message = '出错了',
  details?: Array<{ field: string; reason: string }>,
): Response {
  const error: Record<string, unknown> = { code, message };
  if (details) error['details'] = details;
  return jsonResponse(status, { error, meta: { request_id: '01K2ERRERRERRERRERRERRERRE' } });
}

function stubFetch(routes: Record<string, StubHandler>): Seen[] {
  const seen: Seen[] = [];
  const spy = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input instanceof Request ? input.url : input));
    const method = (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase();
    const route = routes[url.pathname];
    if (route === undefined) throw new Error(`未预期的请求：${method} ${url.pathname}`);
    // 传输层把 body 读成 ArrayBuffer 再逐次 slice（为了能重发），所以这里要解回来。
    const raw = init?.body;
    const body =
      typeof raw === 'string'
        ? raw
        : raw instanceof ArrayBuffer
          ? new TextDecoder().decode(raw)
          : null;
    const req: Seen = { method, url, body };
    seen.push(req);
    return route(req);
  });
  vi.stubGlobal('fetch', spy);
  return seen;
}

const SETTINGS_MAP = {
  'register.enabled': true,
  'register.invite_only': true,
  'sla.first_response_minutes': 120,
  site_name: 'babel.plus',
};

/** 够 8 个码位的原因。少一个字服务端就会退回来。 */
const GOOD_REASON = '关闭注册以应对批量注册滥用';

beforeEach(() => {
  resetRuntimeConfig();
  resetAdminApiForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('系统配置 · 读', () => {
  it('按前缀分组渲染所有键，值是 JSON 原文', async () => {
    stubFetch({ [SETTINGS]: () => jsonResponse(200, ok(SETTINGS_MAP)) });
    render(<SettingsPage />);

    const registerToggle = await screen.findByLabelText('register.enabled');
    expect((registerToggle as HTMLTextAreaElement).value).toBe('true');
    expect((screen.getByLabelText('site_name') as HTMLTextAreaElement).value).toBe('"babel.plus"');
    // 分组：`register` / `sla` / 未分组。
    expect(screen.getByText('register')).toBeTruthy();
    expect(screen.getByText('未分组')).toBeTruthy();
  });

  it('配置表是空的 → 空态要说清「这里也建不了键」', async () => {
    stubFetch({ [SETTINGS]: () => jsonResponse(200, ok({})) });
    render(<SettingsPage />);

    expect(await screen.findByText('配置表是空的')).toBeTruthy();
    expect(screen.getByText(/建键必须走迁移/)).toBeTruthy();
  });

  it('读失败 → 错误态；501 → 「尚未开放」', async () => {
    const view = render(<SettingsPage />);
    view.unmount();

    stubFetch({ [SETTINGS]: () => fail(500, 'INTERNAL_ERROR', '读取系统配置失败') });
    const first = render(<SettingsPage />);
    expect(await screen.findByText('服务端出错了')).toBeTruthy();
    first.unmount();

    stubFetch({ [SETTINGS]: () => fail(501, 'NOT_IMPLEMENTED', '尚未实现') });
    render(<SettingsPage />);
    expect(await screen.findByText('尚未开放')).toBeTruthy();
  });
});

describe('系统配置 · D13 写入', () => {
  it('🔴 原因不足 8 个码位 → 按钮变灰，点下去一个请求都不发', async () => {
    const seen = stubFetch({ [SETTINGS]: () => jsonResponse(200, ok(SETTINGS_MAP)) });
    render(<SettingsPage />);
    await screen.findByLabelText('register.enabled');

    fireEvent.change(screen.getByLabelText('register.enabled'), { target: { value: 'false' } });
    fireEvent.click(screen.getByRole('button', { name: '保存 1 项配置改动' }));
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '关掉' } });

    const submit = screen.getByRole('button', { name: '写入配置' });
    expect(submit.hasAttribute('disabled')).toBe(true);
    fireEvent.click(submit);

    // 只有挂载时那一次 GET。
    expect(seen).toHaveLength(1);
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('至少 8 个字');
  });

  it('🔴 某个键解不出 JSON → 它不进 values，按钮变灰并说明原因', async () => {
    const seen = stubFetch({ [SETTINGS]: () => jsonResponse(200, ok(SETTINGS_MAP)) });
    render(<SettingsPage />);
    await screen.findByLabelText('register.enabled');

    fireEvent.change(screen.getByLabelText('register.enabled'), { target: { value: 'flase' } });

    expect(screen.getByText('不是合法 JSON')).toBeTruthy();
    // 改动数仍然是 0 —— 解不出来的那个键不算改动。
    fireEvent.click(screen.getByRole('button', { name: '保存 0 项配置改动' }));
    expect(screen.getByRole('button', { name: '写入配置' }).hasAttribute('disabled')).toBe(true);
    expect(seen).toHaveLength(1);
  });

  it('参数齐了 → 只发改动的那个键，成功后就地替换（不再发一次 GET）', async () => {
    const seen = stubFetch({
      [SETTINGS]: (req) =>
        req.method === 'PATCH'
          ? jsonResponse(200, ok({ ...SETTINGS_MAP, 'register.enabled': false }))
          : jsonResponse(200, ok(SETTINGS_MAP)),
    });
    render(<SettingsPage />);
    await screen.findByLabelText('register.enabled');

    fireEvent.change(screen.getByLabelText('register.enabled'), { target: { value: 'false' } });
    fireEvent.click(screen.getByRole('button', { name: '保存 1 项配置改动' }));

    // D13 的硬要求：保存前展示逐键 diff。
    expect(screen.getByText(/这次会写入/)).toBeTruthy();
    expect(screen.getAllByText('register.enabled').length).toBeGreaterThan(1);

    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: GOOD_REASON } });
    fireEvent.click(screen.getByRole('button', { name: '写入配置' }));

    await waitFor(() => expect(seen).toHaveLength(2));
    const patch = seen[1];
    expect(patch?.method).toBe('PATCH');
    expect(JSON.parse(patch?.body ?? '{}')).toEqual({
      // 只发改动的那一个键；没动过的三个一个都不发。
      values: { 'register.enabled': false },
      reason: GOOD_REASON,
    });

    // 🔴 成功后不许再发 GET：服务端回的就是全量新配置。
    await waitFor(() =>
      expect((screen.getByLabelText('register.enabled') as HTMLTextAreaElement).value).toBe('false'),
    );
    expect(seen).toHaveLength(2);
  });

  it('422 → 把服务端说的「哪个键不认识」原样显示出来', async () => {
    const seen = stubFetch({
      [SETTINGS]: (req) =>
        req.method === 'PATCH'
          ? fail(422, 'VALIDATION_FAILED', '有不认识的配置键', [
              { field: 'values', reason: '不认识的配置键：register.enabld' },
            ])
          : jsonResponse(200, ok(SETTINGS_MAP)),
    });
    render(<SettingsPage />);
    await screen.findByLabelText('register.enabled');

    fireEvent.change(screen.getByLabelText('register.enabled'), { target: { value: 'false' } });
    fireEvent.click(screen.getByRole('button', { name: '保存 1 项配置改动' }));
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: GOOD_REASON } });
    fireEvent.click(screen.getByRole('button', { name: '写入配置' }));

    await waitFor(() => expect(seen).toHaveLength(2));
    expect(await screen.findByText('服务端退回了这次提交')).toBeTruthy();
    expect(screen.getByText('values：不认识的配置键：register.enabld')).toBeTruthy();
  });

  it('「放弃全部改动」把编辑态清干净，不发任何请求', async () => {
    const seen = stubFetch({ [SETTINGS]: () => jsonResponse(200, ok(SETTINGS_MAP)) });
    render(<SettingsPage />);
    await screen.findByLabelText('register.enabled');

    fireEvent.change(screen.getByLabelText('register.enabled'), { target: { value: 'false' } });
    fireEvent.click(screen.getByRole('button', { name: '放弃全部改动' }));

    expect((screen.getByLabelText('register.enabled') as HTMLTextAreaElement).value).toBe('true');
    expect(seen).toHaveLength(1);
  });
});
