// @vitest-environment jsdom
//
// 需要 DOM：这一支验的是「四层的参数收齐之前，按钮到底点不点得动」，
// 而那只有把组件真的挂起来才能回答。纯函数那一层由 `DangerousAction.test.tsx` 负责。

/**
 * 模块 2 · 用户详情页的测试。
 *
 * 这一页有五个危险按钮（D1 · D2 封/解封 · D3 · D10），每一个都必须钉住两件事：
 *
 *  1. **参数没收齐时不许提交** —— 而且是「一个请求都不发」，不是「发了被服务端退回来」。
 *     这不是安全边界（服务端才是），它省下的是一次注定失败的往返；
 *     而在需要 TOTP 的那几条上，一次注定失败的往返会**烧掉一个验证码**
 *     （step-up 是先验对再占用，占用不随业务失败回滚）。
 *  2. **收齐之后发出去的是什么** —— reason 原样、confirmation 原样、
 *     TOTP 在**请求头** `X-TOTP-Code` 里而不是 body 里。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import UserDetailPage, { parseYuanToCents } from './UserDetailPage.tsx';

const REQUEST_ID = '01K2DETAILDETAILDETAILDE';
const GIB = 1024 * 1024 * 1024;

interface Call {
  readonly url: string;
  readonly method: string;
  readonly headers: Headers;
  readonly body: string;
}

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

function ok(data: unknown): Response {
  return new Response(JSON.stringify({ data, meta: { request_id: REQUEST_ID } }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

function fail(status: number, code: string, message: string, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify({ error: { code, message }, meta: { request_id: REQUEST_ID } }), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

const USER = {
  id: 7,
  email: 'alice@example.com',
  banned: false,
  plan_name: '标准月付',
  expired_at: '2026-12-01T00:00:00Z',
  transfer_enable_bytes: 200 * GIB,
  upload_bytes: GIB,
  download_bytes: 3 * GIB,
  device_limit: 3,
  group_id: 1,
  balance_amount: 1234,
  created_at: '2026-01-02T03:04:05Z',
};

const BANNED_USER = { ...USER, banned: true };

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/admin/users/7']}>
      <Routes>
        <Route path="/admin/users/:id" element={<UserDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

/** 只回用户详情、其余一律 500 的默认桩。写操作的用例自己覆盖它。 */
function stubDetail(user: unknown = USER): Call[] {
  return stubFetch((call) => (call.method === 'GET' ? ok(user) : fail(500, 'INTERNAL_ERROR', '不该走到这里')));
}

function writes(calls: readonly Call[]): readonly Call[] {
  return calls.filter((c) => c.method !== 'GET');
}

beforeEach(() => {
  resetRuntimeConfig();
  resetAdminApiForTests();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('parseYuanToCents（钱的解析，纯函数）', () => {
  it('元 → 分，两位小数以内', () => {
    expect(parseYuanToCents('12')).toBe(1200);
    expect(parseYuanToCents('12.3')).toBe(1230);
    expect(parseYuanToCents('12.34')).toBe(1234);
  });

  it('先乘再四舍五入，避开二进制浮点', () => {
    // 0.29 * 100 在 IEEE754 里是 28.999999999999996，直接取整会变成 28 分 —— 少一分钱。
    expect(parseYuanToCents('0.29')).toBe(29);
    expect(parseYuanToCents('1.15')).toBe(115);
  });

  it('空 / 负号 / 三位小数 / 非数字一律 null（= 不许提交）', () => {
    expect(parseYuanToCents('')).toBeNull();
    expect(parseYuanToCents('  ')).toBeNull();
    // 负号不走这里：方向由单选决定，一个能输负号的金额框会让「扣钱」有两种表达方式。
    expect(parseYuanToCents('-1')).toBeNull();
    expect(parseYuanToCents('1.234')).toBeNull();
    expect(parseYuanToCents('12e3')).toBeNull();
    expect(parseYuanToCents('十二')).toBeNull();
  });
});

describe('只读区', () => {
  it('渲染概况，并说明这里没有 token / uuid', async () => {
    stubDetail();
    renderPage();

    expect(await screen.findAllByText('alice@example.com')).toBeTruthy();
    expect(screen.getByText('标准月付')).toBeTruthy();
    expect(screen.getByText('4.00 GB / 200.00 GB')).toBeTruthy();
    // 余额在概况与「调整余额」卡片里各出现一次 —— 两处说的是同一个数，所以取全部。
    expect(screen.getAllByText('¥12.34').length).toBe(2);
    // ⚠️ testing-library 的文本匹配只看**直接子文本节点**（`getNodeText`），
    // 跨 <strong> 的正则匹配不到 —— 所以这里挑一段落在同一个文本节点里的话。
    expect(screen.getByText(/根本没有这两个字段/)).toBeTruthy();
  });

  it('历史区不假装能查：明说契约里没有按用户过滤的入口', async () => {
    stubDetail();
    renderPage();

    await screen.findByText('历史');
    // 正则里不用 `.*` 跨节点：JSX 换行会在 textContent 里留下 \n，而 `.` 不匹配换行。
    expect(screen.getByText(/按用户过滤的历史端点/)).toBeTruthy();
    const toOrders = screen.getByText(/去订单页搜/) as HTMLAnchorElement;
    expect(toOrders.getAttribute('href')).toBe('/admin/orders?q=alice%40example.com');
  });

  it('404 → 「找不到这个用户」，并说明注销是匿名化而不是删除（不是故障态）', async () => {
    stubFetch(() => fail(404, 'RESOURCE_NOT_FOUND', '用户不存在'));
    renderPage();

    expect(await screen.findByText('找不到这个用户')).toBeTruthy();
    // 这是一个正常结论，不该给「重试」按钮 —— 那会让人以为是加载失败。
    expect(screen.queryByRole('button', { name: '重试' })).toBeNull();
  });

  it('501 → 「尚未开放」而不是「服务端出错了」', async () => {
    stubFetch(() => fail(501, 'NOT_IMPLEMENTED', '还没实现'));
    renderPage();

    expect(await screen.findByText('尚未开放')).toBeTruthy();
  });

  it('地址里的 id 不是正整数 → 说链接坏了，不说这个人被删了', async () => {
    stubFetch(() => ok(USER));
    render(
      <MemoryRouter initialEntries={['/admin/users/abc']}>
        <Routes>
          <Route path="/admin/users/:id" element={<UserDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText('这个地址里的用户编号不合法')).toBeTruthy();
  });
});

describe('D1 改配额 / 到期', () => {
  async function openPanel(): Promise<void> {
    fireEvent.click(await screen.findByRole('button', { name: '改用户流量配额 / 到期时间' }));
  }

  it('一个字段都没改时不许提交（空 PATCH 会写出一条改前值等于改后值的审计）', async () => {
    const calls = stubDetail();
    renderPage();
    await openPanel();

    const submit = screen.getByRole('button', { name: /提交这 0 处改动/ }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    fireEvent.click(submit);
    expect(writes(calls).length).toBe(0);
  });

  it('改了字段但原因不足 8 个字 → 仍然不许提交', async () => {
    const calls = stubDetail();
    renderPage();
    await openPanel();

    fireEvent.change(screen.getByLabelText('总配额（GiB）'), { target: { value: '300' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '补配额' } });

    const submit = screen.getByRole('button', { name: /提交这 1 处改动/ }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    fireEvent.click(submit);
    expect(writes(calls).length).toBe(0);
  });

  it('收齐之后：GiB 换算成字节、reason 原样发出，响应就地覆盖不重拉', async () => {
    const calls = stubFetch((call) =>
      call.method === 'PATCH' ? ok({ ...USER, transfer_enable_bytes: 300 * GIB }) : ok(USER),
    );
    renderPage();
    await openPanel();

    fireEvent.change(screen.getByLabelText('总配额（GiB）'), { target: { value: '300' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '工单 #421：节点故障期间的流量补偿' },
    });
    fireEvent.click(screen.getByRole('button', { name: /提交这 1 处改动/ }));

    await waitFor(() => expect(writes(calls).length).toBe(1));
    const patch = writes(calls)[0];
    expect(patch?.method).toBe('PATCH');
    expect(JSON.parse(patch?.body ?? '{}')).toEqual({
      reason: '工单 #421：节点故障期间的流量补偿',
      transfer_enable_bytes: 300 * GIB,
    });
    // 覆盖生效：新配额直接显示出来，没有再发一次 GET。
    expect(await screen.findByText('4.00 GB / 300.00 GB')).toBeTruthy();
    expect(calls.filter((c) => c.method === 'GET').length).toBe(1);
  });
});

describe('D2 封禁 / 解封', () => {
  it('把 60 秒生效延迟写进确认框，并且没有原因不许提交', async () => {
    const calls = stubDetail();
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: '封禁用户' }));
    expect(screen.getByText(/最长 60 秒后才在节点侧生效/)).toBeTruthy();

    const submit = screen.getByRole('button', { name: '封禁' }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    fireEvent.click(submit);
    expect(writes(calls).length).toBe(0);
  });

  it('填了原因 → POST /ban，并把新状态就地覆盖', async () => {
    const calls = stubFetch((call) =>
      call.url.includes('/ban') ? ok(BANNED_USER) : ok(USER),
    );
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: '封禁用户' }));
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '滥用：同一账号 40 个 IP 并发' },
    });
    fireEvent.click(screen.getByRole('button', { name: '封禁' }));

    await waitFor(() => expect(writes(calls).length).toBe(1));
    expect(writes(calls)[0]?.url).toContain('/api/v1/admin/users/7/ban');
    expect(await screen.findByText('已封禁')).toBeTruthy();
  });

  it('🔴 换了路由参数之后，上一位的写操作结果不许留在另一个人的页面上', async () => {
    const inviter = { ...USER, id: 2, email: 'inviter@example.com', banned: false };
    stubFetch((call) => {
      if (call.url.includes('/users/7/ban')) return ok({ ...BANNED_USER, invited_by_user_id: 2 });
      if (call.url.includes('/users/2')) return ok(inviter);
      return ok({ ...USER, invited_by_user_id: 2 });
    });
    render(
      <MemoryRouter initialEntries={['/admin/users/7']}>
        <Routes>
          <Route path="/admin/users/:id" element={<UserDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    // 先在 7 号身上留下一次写操作的结果。
    fireEvent.click(await screen.findByRole('button', { name: '封禁用户' }));
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '滥用：同一账号 40 个 IP 并发' },
    });
    fireEvent.click(screen.getByRole('button', { name: '封禁' }));
    expect(await screen.findByText('已封禁')).toBeTruthy();

    // 再点「邀请人」跳到 2 号 —— 组件不会重新挂载，覆盖必须自己失效。
    fireEvent.click(screen.getByText('#2'));

    expect(await screen.findAllByText('inviter@example.com')).toBeTruthy();
    expect(screen.queryByText('已封禁')).toBeNull();
  });

  it('已封禁的用户看到的是「解封」，打到 /unban', async () => {
    const calls = stubFetch((call) => (call.url.includes('/unban') ? ok(USER) : ok(BANNED_USER)));
    renderPage();

    fireEvent.click(await screen.findByRole('button', { name: '解封用户' }));
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '误封，已核实是家庭共享出口 IP' },
    });
    fireEvent.click(screen.getByRole('button', { name: '解封' }));

    await waitFor(() => expect(writes(calls).length).toBe(1));
    expect(writes(calls)[0]?.url).toContain('/api/v1/admin/users/7/unban');
  });
});

describe('D3 吊销全部订阅 token', () => {
  async function openPanel(): Promise<void> {
    fireEvent.click(await screen.findByRole('button', { name: '一键吊销用户全部订阅 token' }));
  }

  it('确认串没有逐字打对时不许提交', async () => {
    const calls = stubDetail();
    renderPage();
    await openPanel();

    fireEvent.change(screen.getByLabelText('输入用户邮箱以确认'), {
      target: { value: 'ALICE@example.com' }, // 大小写不同 —— 说明是手打的，这一层要的正是「照着念一遍」
    });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '账号被共享，已核实' } });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '123456' } });

    const submit = screen.getByRole('button', { name: '吊销这个用户的全部订阅 token' }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    fireEvent.click(submit);
    expect(writes(calls).length).toBe(0);
  });

  it('TOTP 没填够 6 位时不许提交', async () => {
    const calls = stubDetail();
    renderPage();
    await openPanel();

    fireEvent.change(screen.getByLabelText('输入用户邮箱以确认'), { target: { value: 'alice@example.com' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '账号被共享，已核实' } });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '12345' } });

    expect(
      (screen.getByRole('button', { name: '吊销这个用户的全部订阅 token' }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(writes(calls).length).toBe(0);
  });

  it('收齐之后：TOTP 进请求头，confirmation 与 reason 进 body；结果显示撤掉几条', async () => {
    const calls = stubFetch((call) =>
      call.url.includes('/revoke-subs')
        ? ok({ revoked: 3, sub_revoked_at: '2026-08-30T10:00:00Z' })
        : ok(USER),
    );
    renderPage();
    await openPanel();

    fireEvent.change(screen.getByLabelText('输入用户邮箱以确认'), { target: { value: 'alice@example.com' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '账号被共享，已核实' } });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: '吊销这个用户的全部订阅 token' }));

    await waitFor(() => expect(writes(calls).length).toBe(1));
    const post = writes(calls)[0];
    expect(post?.headers.get('X-TOTP-Code')).toBe('123456');
    expect(JSON.parse(post?.body ?? '{}')).toEqual({
      confirmation: 'alice@example.com',
      reason: '账号被共享，已核实',
    });
    expect(await screen.findByText(/已吊销/)).toBeTruthy();
  });

  it('说明服务端并不会替你通知用户（登记表要求 📧，实现里没有）', async () => {
    stubDetail();
    renderPage();
    await openPanel();

    expect(screen.getByText(/服务端目前不发这封信/)).toBeTruthy();
  });
});

describe('D10 调整余额', () => {
  async function fillAmountAndOpen(yuan: string): Promise<void> {
    fireEvent.change(await screen.findByLabelText('金额（元）'), { target: { value: yuan } });
    fireEvent.click(screen.getByRole('button', { name: '调整用户余额' }));
  }

  it('必须先指明调哪一部分钱：选了佣金就不许提交，并说明这个入口改不了它', async () => {
    stubDetail();
    renderPage();

    fireEvent.change(await screen.findByLabelText('金额（元）'), { target: { value: '10' } });
    fireEvent.click(screen.getByLabelText(/佣金（可划转成余额，不可提现）/));

    expect(screen.getByText(/这个入口改不了佣金/)).toBeTruthy();
    expect(screen.getByText(/手工发放 \/ 作废佣金是 D11/)).toBeTruthy();
  });

  it('金额为空 / 为 0 时不许提交（服务端拒绝 0 元调整）', async () => {
    stubDetail();
    renderPage();

    await screen.findByText('调整余额');
    expect(screen.getByText(/先填一个金额/)).toBeTruthy();

    fireEvent.change(screen.getByLabelText('金额（元）'), { target: { value: '0' } });
    expect(screen.getByText(/服务端拒绝 0 元调整/)).toBeTruthy();
  });

  it('金额填好后给出「本次将 ±¥X」的预览（服务端没有单次上限，这是唯一的护栏）', async () => {
    stubDetail();
    renderPage();

    fireEvent.change(await screen.findByLabelText('金额（元）'), { target: { value: '12.34' } });
    expect(screen.getByText(/增加 ¥12\.34/)).toBeTruthy();
    expect(screen.getByText(/amount = 1234 分/)).toBeTruthy();

    fireEvent.click(screen.getByLabelText('从用户扣钱（−）'));
    expect(screen.getByText(/减少 ¥12\.34/)).toBeTruthy();
    expect(screen.getByText(/amount = -1234 分/)).toBeTruthy();
  });

  it('确认串 / 原因 / TOTP 没收齐时不许提交', async () => {
    const calls = stubDetail();
    renderPage();
    await fillAmountAndOpen('12.34');

    const submit = () => screen.getByRole('button', { name: '提交这次余额调整' }) as HTMLButtonElement;
    expect(submit().disabled).toBe(true);

    fireEvent.change(screen.getByLabelText('输入用户邮箱以确认'), { target: { value: 'alice@example.com' } });
    expect(submit().disabled).toBe(true);

    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '双花退款的手工冲正' } });
    expect(submit().disabled).toBe(true); // 还差 TOTP

    fireEvent.click(submit());
    expect(writes(calls).length).toBe(0);
  });

  it('收齐之后：金额按「分」发出，方向由单选决定', async () => {
    const calls = stubFetch((call) =>
      call.url.includes('/balance-adjust')
        ? ok({ balance_amount: 2468, commission_pending_amount: 0, commission_available_amount: 0 })
        : ok(USER),
    );
    renderPage();
    await fillAmountAndOpen('12.34');

    fireEvent.change(screen.getByLabelText('输入用户邮箱以确认'), { target: { value: 'alice@example.com' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '双花退款的手工冲正' } });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '654321' } });
    fireEvent.click(screen.getByRole('button', { name: '提交这次余额调整' }));

    await waitFor(() => expect(writes(calls).length).toBe(1));
    const post = writes(calls)[0];
    expect(post?.headers.get('X-TOTP-Code')).toBe('654321');
    expect(JSON.parse(post?.body ?? '{}')).toEqual({
      confirmation: 'alice@example.com',
      reason: '双花退款的手工冲正',
      amount: 1234,
    });
    expect(await screen.findByText('调整已入账')).toBeTruthy();
  });

  it('🔴 缺账本科目的 503 显示成「暂时不可用」，不是「服务器出错」', async () => {
    stubFetch((call) =>
      call.url.includes('/balance-adjust')
        ? fail(503, 'INTERNAL_DEPENDENCY_DOWN', '调整用户余额暂不可用（账本科目缺失），请联系运维补齐后再试', {
            'Retry-After': '300',
          })
        : ok(USER),
    );
    renderPage();
    await fillAmountAndOpen('12.34');

    fireEvent.change(screen.getByLabelText('输入用户邮箱以确认'), { target: { value: 'alice@example.com' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '双花退款的手工冲正' } });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '654321' } });
    fireEvent.click(screen.getByRole('button', { name: '提交这次余额调整' }));

    expect(await screen.findByText('余额调整暂时不可用（不是服务器故障）')).toBeTruthy();
    expect(screen.getByText(/现在重试多少次都是一样的结果/)).toBeTruthy();
    expect(screen.getByText(/300 秒后再试/)).toBeTruthy();
    // 那个 TOTP 码已经烧掉了，必须说出来 —— 否则下一次的 AUTH_TOTP_INVALID 会被当成验证器坏了。
    expect(screen.getByText(/已经作废了/)).toBeTruthy();
  });
});
