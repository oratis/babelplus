// @vitest-environment jsdom
//
// 这一支需要 DOM：它验的是「这一页渲染出了什么、按钮点不点得动」，
// 而那只有真的把组件挂起来才能回答。包级默认环境是 `node`（`lib/iap.test.ts` 测纯函数），
// 所以这里用文件级 docblock 单独提高环境。

/**
 * 模块 2 · 用户列表页的测试。
 *
 * 走**真实链路**：组件 → `api()` → `shared/api` 的传输层 → 被替换掉的全局 `fetch`。
 * 不 mock 请求函数，因为这一页最容易错的几件事恰恰在链路里：
 * 信封拆得对不对、`ErrorCode` 分支走的是 code 还是状态码、游标有没有带上。
 *
 * ⚠️ **这些用例证明的不是安全性。** §6.2 的四层全部在服务端强制；
 * 这里钉住的只是「前端有没有在参数收齐之前就把请求发出去」。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import UsersPage from './UsersPage.tsx';

const REQUEST_ID = '01K2USERSUSERSUSERSUSERS';

interface Call {
  readonly url: string;
  readonly method: string;
  readonly headers: Headers;
  readonly body: string;
}

/**
 * 每次调用都造一个**新的** `Response`：body 是一次性的流，
 * 复用同一个对象会在第二次读时炸掉。
 */
function stubFetch(handler: (call: Call) => Response): Call[] {
  const calls: Call[] = [];
  const spy = vi.fn(async (input: unknown, init: RequestInit = {}) => {
    const raw = init.body;
    const call: Call = {
      url: String(input),
      method: (init.method ?? 'GET').toUpperCase(),
      headers: new Headers((init.headers as HeadersInit | undefined) ?? {}),
      body:
        raw instanceof ArrayBuffer
          ? new TextDecoder().decode(raw)
          : typeof raw === 'string'
            ? raw
            : '',
    };
    calls.push(call);
    return handler(call);
  });
  vi.stubGlobal('fetch', spy);
  return calls;
}

function ok(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

function fail(status: number, code: string, message: string, details?: unknown): Response {
  return new Response(
    JSON.stringify({ error: { code, message, details }, meta: { request_id: REQUEST_ID } }),
    { status, headers: { 'Content-Type': 'application/json' } },
  );
}

function usersEnvelope(
  data: unknown[],
  meta: Record<string, unknown> = {},
): Record<string, unknown> {
  return { data, meta: { request_id: REQUEST_ID, ...meta } };
}

const ALICE = {
  id: 7,
  email: 'alice@example.com',
  banned: false,
  plan_name: '标准月付',
  expired_at: '2026-12-01T00:00:00Z',
  transfer_enable_bytes: 200 * 1024 * 1024 * 1024,
  upload_bytes: 1024 * 1024 * 1024,
  download_bytes: 3 * 1024 * 1024 * 1024,
  device_limit: 3,
  group_id: 1,
  balance_amount: 1234,
  created_at: '2026-01-02T03:04:05Z',
  invited_by_user_id: 2,
};

const BOB = {
  id: 8,
  email: 'bob@example.com',
  banned: true,
  created_at: '2026-02-02T03:04:05Z',
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/admin/users']}>
      <UsersPage />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  resetRuntimeConfig();
  resetAdminApiForTests();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('用户列表', () => {
  it('渲染每一行的八列，并把总数显示出来', async () => {
    stubFetch(() => ok(usersEnvelope([ALICE, BOB], { has_more: false, total: 2 })));
    renderPage();

    expect(await screen.findByText('alice@example.com')).toBeTruthy();
    expect(screen.getByText('bob@example.com')).toBeTruthy();
    expect(screen.getByText('标准月付')).toBeTruthy();
    // 「已用 / 配额」是一格，缺字段时那一半是 —— 而不是 0。
    expect(screen.getByText('4.00 GB / 200.00 GB')).toBeTruthy();
    expect(screen.getByText('— / —')).toBeTruthy();
    // 状态三件事分开显示。
    expect(screen.getByText('已封禁')).toBeTruthy();
    // 总数是**第二次渲染**才有的：`useRememberedTotal` 在 effect 里落它。
    // 用 findAll 等一等，不然这条断言会随机器快慢时红时绿。
    expect((await screen.findAllByText('共 2 人')).length).toBeGreaterThan(0);
  });

  it('第一页带 count=true，翻页不再带（COUNT(*) 不能每页都付）', async () => {
    const calls = stubFetch(() =>
      ok(usersEnvelope([ALICE], { has_more: true, next_cursor: 'CURSOR2' })),
    );
    renderPage();
    await screen.findByText('alice@example.com');

    expect(calls[0]?.url).toContain('count=true');

    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() => expect(calls.length).toBe(2));
    expect(calls[1]?.url).toContain('cursor=CURSOR2');
    expect(calls[1]?.url).not.toContain('count=true');
  });

  it('「下一页」的判据是 next_cursor 而不是本页条数', async () => {
    stubFetch(() => ok(usersEnvelope([ALICE], { has_more: false })));
    renderPage();
    await screen.findByText('alice@example.com');

    expect(screen.getByRole('button', { name: '下一页' }).hasAttribute('disabled')).toBe(true);
    expect(screen.getByRole('button', { name: '上一页' }).hasAttribute('disabled')).toBe(true);
  });

  it('一个用户都没有 → 引导去发邀请码（邀请制注册）', async () => {
    stubFetch(() => ok(usersEnvelope([], { has_more: false, total: 0 })));
    renderPage();

    expect(await screen.findByText('还没有用户')).toBeTruthy();
    expect(screen.getByText('去发邀请码')).toBeTruthy();
  });

  it('搜索无结果 → 说清楚「只按邮箱模糊匹配」，不说成「这个人不存在」', async () => {
    const calls = stubFetch((call) =>
      call.url.includes('q=') ? ok(usersEnvelope([], { has_more: false })) : ok(usersEnvelope([ALICE])),
    );
    renderPage();
    await screen.findByText('alice@example.com');

    fireEvent.change(screen.getByLabelText('搜索邮箱'), { target: { value: 'nobody@example.com' } });
    fireEvent.click(screen.getByRole('button', { name: '搜索' }));

    expect(await screen.findByText('没有匹配的用户')).toBeTruthy();
    expect(screen.getAllByText(/不能/).length).toBeGreaterThan(0);
    expect(calls[1]?.url).toContain('q=nobody%40example.com');
    // 换搜索词必须回到第一页：旧游标在新条件下解出的是一段无意义的位置。
    expect(calls[1]?.url).not.toContain('cursor=');
  });
});

describe('读路径的错误分支（按 ErrorCode，不按状态码）', () => {
  it('403 AUTH_PERMISSION_DENIED → 说「重新登录不会有帮助」', async () => {
    stubFetch(() => fail(403, 'AUTH_PERMISSION_DENIED', '没有权限'));
    renderPage();

    expect(await screen.findByText('这个账号看不了这一块')).toBeTruthy();
    expect(screen.getByText(new RegExp(REQUEST_ID))).toBeTruthy();
  });

  it('501 NOT_IMPLEMENTED → 「尚未开放」，不是红色故障，也没有「重试」', async () => {
    stubFetch(() => fail(501, 'NOT_IMPLEMENTED', '还没实现'));
    renderPage();

    expect(await screen.findByText('尚未开放')).toBeTruthy();
    // 501 走的是虚线说明块，不是 ErrorState —— 后者会把人推去状态页，而那里一切正常。
    expect(screen.queryByRole('button', { name: '重试' })).toBeNull();
  });

  it('500 → 「服务端出错了」并给出请求号', async () => {
    stubFetch(() => fail(500, 'INTERNAL_ERROR', '炸了'));
    renderPage();

    expect(await screen.findByText('服务端出错了')).toBeTruthy();
    expect(screen.getByRole('button', { name: '重试' })).toBeTruthy();
  });
});

describe('D14 导出用户 CSV', () => {
  /** 展开导出面板，返回面板里的提交按钮。 */
  async function openExportPanel(): Promise<HTMLButtonElement> {
    fireEvent.click(await screen.findByRole('button', { name: '导出用户数据 CSV' }));
    return screen.getByRole('button', { name: '生成 CSV' }) as HTMLButtonElement;
  }

  it('原因不足 8 个字时不许提交，而且一个请求都不发', async () => {
    const calls = stubFetch(() => ok(usersEnvelope([ALICE])));
    renderPage();
    await screen.findByText('alice@example.com');

    const submit = await openExportPanel();
    expect(submit.disabled).toBe(true);

    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '七个字的原因' } });
    expect((screen.getByRole('button', { name: '生成 CSV' }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole('button', { name: '生成 CSV' }));
    // 列表那一次之外，一个请求都不许发出去。
    expect(calls.filter((c) => c.method === 'POST').length).toBe(0);
  });

  it('原因够长 → 提交，并把 reason 原样发出去；成功后给出可下载的 CSV', async () => {
    const csv = 'data:text/csv;charset=utf-8;base64,aWQsZW1haWwK';
    const calls = stubFetch((call) =>
      call.method === 'POST'
        ? ok({ data: { id: 'job-1', status: 'done', download_url: csv }, meta: { request_id: REQUEST_ID } })
        : ok(usersEnvelope([ALICE])),
    );
    renderPage();
    await screen.findByText('alice@example.com');

    await openExportPanel();
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '财务对账需要一份当前用户名单' },
    });
    const submit = screen.getByRole('button', { name: '生成 CSV' }) as HTMLButtonElement;
    expect(submit.disabled).toBe(false);
    fireEvent.click(submit);

    const link = (await screen.findByText('下载 CSV')) as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe(csv);
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.url).toContain('/api/v1/admin/users/export');
    expect(JSON.parse(post?.body ?? '{}')).toEqual({ reason: '财务对账需要一份当前用户名单' });
  });

  it('命中 5 万行上限的 422 → 把「拒绝」的理由原样显示出来，不是一句「提交失败」', async () => {
    const message =
      '符合条件的用户超过 50000 行上限，本次导出已被拒绝（一份被截断的名单会被误当成完整名单）；当前契约没有筛选参数，需要先落地异步导出';
    stubFetch((call) =>
      call.method === 'POST'
        ? fail(422, 'VALIDATION_FAILED', message)
        : ok(usersEnvelope([ALICE])),
    );
    renderPage();
    await screen.findByText('alice@example.com');

    await openExportPanel();
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '财务对账需要一份当前用户名单' },
    });
    fireEvent.click(screen.getByRole('button', { name: '生成 CSV' }));

    expect(await screen.findByText(message)).toBeTruthy();
    expect(screen.queryByText('下载 CSV')).toBeNull();
  });
});

describe('这一页必须说出口的几件事', () => {
  it('把「刻意查不到的三件事」与搜索能力边界都写在页面上', async () => {
    stubFetch(() => ok(usersEnvelope([ALICE])));
    renderPage();
    await screen.findByText('alice@example.com');

    expect(screen.getByText('这里刻意查不到的三件事')).toBeTruthy();
    expect(screen.getByText('用户访问了哪些网站')).toBeTruthy();
    expect(screen.getByText('以用户身份登录')).toBeTruthy();
    expect(screen.getByText('流量明细流水')).toBeTruthy();
    expect(screen.getByText('这个列表能筛什么、不能筛什么')).toBeTruthy();
    expect(screen.getByText('按邀请码反查')).toBeTruthy();
  });
});
