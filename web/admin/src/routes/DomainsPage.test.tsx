// @vitest-environment jsdom

/**
 * 域名池的接线测试。
 *
 * 🔴 **这一页现在只有一件事要证明：501 被诚实地显示成「尚未开放」，
 * 而且页面上没有任何看起来能用的增删入口。**
 *
 * 三个端点（list / create / delete）全部落在 `unimplemented.gen.go` 上，
 * 卡住它们的是两件**未做的裁决**（`domains` 表不存在、ADR 0011 §7.2 与冻结契约的
 * `Domain` 是两套字段模型），不是工时。一个点下去只会得到 501 的按钮比没有按钮更糟 ——
 * 它把「这件事还没决定」显示成「这件事坏了」，于是有人去提工单、去查日志。
 *
 * 所以第二个用例是**结构性**的：501 态下整页一个 `<button>` 都不许有。
 * 有人哪天加了一个「添加域名」按钮，这一条会红。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import DomainsPage from './DomainsPage.tsx';

const DOMAINS = '/api/v1/admin/domains';

type StubHandler = () => Response;

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function ok(data: unknown): unknown {
  return { data, meta: { request_id: '01K2DOMDOMDOMDOMDOMDOMDOMD' } };
}

function fail(status: number, code: string, message = '出错了'): Response {
  return jsonResponse(status, {
    error: { code, message },
    meta: { request_id: '01K2ERRERRERRERRERRERRERRE' },
  });
}

function stubFetch(routes: Record<string, StubHandler>): number[] {
  const seen: number[] = [];
  const spy = vi.fn(async (input: string | URL | Request) => {
    const url = new URL(String(input instanceof Request ? input.url : input));
    const route = routes[url.pathname];
    if (route === undefined) throw new Error(`未预期的请求：${url.pathname}`);
    seen.push(seen.length + 1);
    return route();
  });
  vi.stubGlobal('fetch', spy);
  return seen;
}

/** 服务端真实的 501（`cmd/server/main.go` 的 responseErrorHandler 直接写出去的码）。 */
function notImplemented(): Response {
  return fail(501, 'NOT_IMPLEMENTED', '尚未实现');
}

beforeEach(() => {
  resetRuntimeConfig();
  resetAdminApiForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('域名池', () => {
  it('501 → 「尚未开放」，并说清卡在哪（表不存在 + 两套字段模型 + 裁决未批准）', async () => {
    stubFetch({ [DOMAINS]: notImplemented });
    render(<DomainsPage />);

    // 两处：页头的徽标 + 说明块的标题。两处都要有 —— 徽标让人在滚动前就知道，
    // 说明块负责讲清楚卡在哪。
    expect(await screen.findAllByText('尚未开放')).toHaveLength(2);
    expect(screen.getByText(/这不是故障，重试也不会有变化/)).toBeTruthy();
    expect(screen.getByText(/卡住它的不是工时，是两件还没做的裁决/)).toBeTruthy();
    expect(screen.getByText(/表不存在/)).toBeTruthy();
    expect(screen.getByText(/提案，未批准/)).toBeTruthy();

    // 501 不是故障，所以不许套用 5xx 的那套「我们这边出了问题 / 去看状态页」。
    expect(screen.queryByText('服务端出错了')).toBeNull();
  });

  it('🔴 501 态下整页没有任何按钮 —— 一个点下去只会 501 的按钮比没有按钮更糟', async () => {
    stubFetch({ [DOMAINS]: notImplemented });
    const { container } = render(<DomainsPage />);
    await screen.findAllByText('尚未开放');

    expect(container.querySelectorAll('button')).toHaveLength(0);
    expect(screen.getByText(/为什么这一页没有「添加域名」按钮/)).toBeTruthy();
  });

  it('大陆可达性与探活机制这两条限制必须写在页面上', async () => {
    stubFetch({ [DOMAINS]: notImplemented });
    render(<DomainsPage />);
    await screen.findAllByText('尚未开放');

    expect(screen.getByText(/大陆侧实测尚未开展/)).toBeTruthy();
    expect(screen.getByText(/目前零机制支撑/)).toBeTruthy();
  });

  it('真的 500 时走的是故障态（有重试），不是「尚未开放」', async () => {
    stubFetch({ [DOMAINS]: () => fail(500, 'INTERNAL_ERROR', '读取域名池失败') });
    render(<DomainsPage />);

    expect(await screen.findByText('服务端出错了')).toBeTruthy();
    expect(screen.queryByText('尚未开放')).toBeNull();
    expect(screen.getByRole('button', { name: '重试' })).toBeTruthy();
  });

  it('哪天后端落地了：只读列表能渲染，但仍然没有增删按钮', async () => {
    stubFetch({
      [DOMAINS]: () =>
        jsonResponse(
          200,
          ok([
            {
              id: 1,
              hostname: 'panel.example',
              role: 'web',
              enabled: true,
              reachable: true,
              last_checked_at: '2026-08-30T00:00:00Z',
              created_at: '2026-08-01T00:00:00Z',
            },
            {
              id: 2,
              hostname: 'api.example',
              role: 'api',
              enabled: true,
              created_at: '2026-08-01T00:00:00Z',
            },
          ]),
        ),
    });
    const { container } = render(<DomainsPage />);

    expect(await screen.findByText('panel.example')).toBeTruthy();
    expect(screen.getByText('api.example')).toBeTruthy();
    // reachable 缺席 ≠ 不通：没探过就是「—」。
    expect(screen.getAllByText('—').length).toBeGreaterThan(0);
    // 证书签发者 / 到期在契约里没有字段，这一点要说出来。
    expect(screen.getByText(/证书签发者与到期时间/)).toBeTruthy();
    expect(container.querySelectorAll('button')).toHaveLength(0);
  });

  it('后端落地但池子是空的 → 说清「至少 2 个自有域名」，且不假装能在这里登记', async () => {
    stubFetch({ [DOMAINS]: () => jsonResponse(200, ok([])) });
    render(<DomainsPage />);

    expect(await screen.findByText('域名池是空的')).toBeTruthy();
    expect(screen.getByText(/这一页现在登记不了域名/)).toBeTruthy();
  });
});
