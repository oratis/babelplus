// @vitest-environment jsdom
//
// 需要 DOM：门禁判据是纯函数（下面第一组用例直接打它），但「按钮到底有没有变灰、
// 点下去会不会真的发请求」是另一回事 —— 判据绿着而组件里少接了一处 `disabled` 的话，
// 纯函数测试一个字都不会说。所以两层都测。

/**
 * 模块 11 · 管理员账号页的测试。
 *
 * 这一页钉住的四件事，每一件写错都不会有人报 bug（而后果都很贵）：
 *
 *  1. **新建之后的第 ② 步不能丢** —— 新建出来的管理员登不进去，必须紧接着重置一次 TOTP
 *     才拿得到绑定材料。界面要把这一步挡在眼前，而不是发一句提示语。
 *  2. **参数没收齐时不许提交** —— 尤其是需要 TOTP 的三条：一次注定失败的往返会烧掉一个码。
 *  3. **绑定材料只显示一次，并且要说清楚它不能贴进工单。**
 *  4. **「停用」不是「删除」** —— 服务端写的是 `disabled_at`；界面文案不能说成删除。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import AdminsPage, { createAdminBlockReason } from './AdminsPage.tsx';

const REQUEST_ID = '01K2ADMINSADMINSADMINSAD';

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

function ok(data: unknown, extraHeaders: Record<string, string> = {}, status = 200): Response {
  return new Response(JSON.stringify({ data, meta: { request_id: REQUEST_ID } }), {
    status,
    headers: { 'Content-Type': 'application/json', ...extraHeaders },
  });
}

function fail(status: number, code: string, message: string): Response {
  return new Response(JSON.stringify({ error: { code, message }, meta: { request_id: REQUEST_ID } }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const OWNER = {
  id: 1,
  email: 'owner@example.com',
  permissions: ['admin.user.export'],
  totp_enabled: true,
  created_at: '2026-01-01T00:00:00Z',
  last_login_at: '2026-08-29T12:00:00Z',
};

/** 第二个人：库里有 secret（所以 totp_enabled 恒 true），但**从未登录** —— 那才是「他还没绑」的信号。 */
const SUPPORT = {
  id: 2,
  email: 'support@example.com',
  permissions: [],
  totp_enabled: true,
  created_at: '2026-08-01T00:00:00Z',
};

/** 假想中的「TOTP 未开启」。当前 schema 下不可能出现，但排序规则必须为它准备好。 */
const NO_TOTP = { ...SUPPORT, id: 3, email: 'no-totp@example.com', totp_enabled: false };

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/admin/admins']}>
      <AdminsPage />
    </MemoryRouter>,
  );
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

/* ────────────────────────── 纯函数：新建的门禁 ────────────────────────── */

describe('createAdminBlockReason', () => {
  const base = { email: 'new@example.com', reason: '运维交接：张三接手节点值班', totp: '123456', submitting: false };

  it('参数收齐时放行', () => {
    expect(createAdminBlockReason(base)).toBeNull();
  });

  it('结构性原因排在填写原因之前（正在提交时不去说「原因太短」）', () => {
    expect(createAdminBlockReason({ ...base, reason: '', submitting: true })).toBe('submitting');
  });

  it('邮箱空 / 形态不对分开说', () => {
    expect(createAdminBlockReason({ ...base, email: '  ' })).toBe('email-missing');
    expect(createAdminBlockReason({ ...base, email: 'not-an-email' })).toBe('email-malformed');
  });

  it('原因按**码位**数，不是 String.length', () => {
    expect(createAdminBlockReason({ ...base, reason: '七个字的原因啊' })).toBe('reason-too-short');
    // 8 个 emoji = 8 个码位（`String.length` 会数成 16）。服务端数的是 rune，两边必须一致。
    expect(createAdminBlockReason({ ...base, reason: '✅✅✅✅✅✅✅✅' })).toBeNull();
  });

  it('TOTP 必须恰好 6 位十进制', () => {
    expect(createAdminBlockReason({ ...base, totp: '12345' })).toBe('totp-missing');
    expect(createAdminBlockReason({ ...base, totp: '1234567' })).toBe('totp-missing');
    expect(createAdminBlockReason({ ...base, totp: '12345a' })).toBe('totp-missing');
  });
});

/* ────────────────────────────── 列表 ────────────────────────────── */

describe('管理员列表', () => {
  it('渲染邮箱 / 权限位 / 最后登录，并把「从未登录」标出来', async () => {
    stubFetch(() => ok([OWNER, SUPPORT]));
    renderPage();

    expect(await screen.findByText('owner@example.com')).toBeTruthy();
    expect(screen.getByText('support@example.com')).toBeTruthy();
    expect(screen.getByText('D14 导出用户 CSV')).toBeTruthy();
    expect(screen.getByText('从未登录')).toBeTruthy();
    expect(screen.getByText(/建好之后没跑「重置 TOTP」那一步/)).toBeTruthy();
    expect(screen.getByText('在职 2 人')).toBeTruthy();
  });

  it('TOTP 未开启的置顶 —— 它不是待完善的设置，是一道闸没关上', async () => {
    stubFetch(() => ok([OWNER, NO_TOTP]));
    renderPage();

    await screen.findByText('owner@example.com');
    const headings = screen.getAllByRole('heading', { level: 3 }).map((h) => h.textContent);
    expect(headings[0]).toBe('no-totp@example.com');
    expect(screen.getByText('TOTP 未开启 —— 一道闸没关上')).toBeTruthy();
  });

  it('说明这一列证明的只是「库里有 secret」，不是「本人真的绑过」', async () => {
    stubFetch(() => ok([OWNER]));
    renderPage();

    expect(await screen.findByText('TOTP 已开启（库里有 secret）')).toBeTruthy();
  });

  it('空列表 → 说这个状态不该出现（看这一页的人自己就是管理员）', async () => {
    stubFetch(() => ok([]));
    renderPage();

    expect(await screen.findByText('没有管理员')).toBeTruthy();
  });

  it('403 / 501 分开说，且 501 不给「重试」', async () => {
    stubFetch(() => fail(501, 'NOT_IMPLEMENTED', '还没实现'));
    renderPage();

    expect(await screen.findByText('尚未开放')).toBeTruthy();
    expect(screen.queryByRole('button', { name: '重试' })).toBeNull();
  });

  it('403 → 「这个账号看不了这一块」', async () => {
    stubFetch(() => fail(403, 'AUTH_PERMISSION_DENIED', '只有 owner 可以'));
    renderPage();

    expect(await screen.findByText('这个账号看不了这一块')).toBeTruthy();
  });
});

/* ────────────────────────────── 新建 ────────────────────────────── */

describe('新建管理员', () => {
  async function openForm(): Promise<void> {
    fireEvent.click(await screen.findByRole('button', { name: '新建管理员' }));
  }

  it('参数没收齐时按钮是灰的，而且一个请求都不发', async () => {
    const calls = stubFetch(() => ok([OWNER]));
    renderPage();
    await openForm();

    const submit = () => screen.getByRole('button', { name: /创建/ }) as HTMLButtonElement;
    expect(submit().disabled).toBe(true);
    expect(screen.getByTestId('create-blocked-hint').textContent).toContain('先填他的邮箱');

    fireEvent.change(screen.getByLabelText('邮箱（Google 账号）'), { target: { value: 'new@example.com' } });
    expect(screen.getByTestId('create-blocked-hint').textContent).toContain('至少 8 个字');

    fireEvent.change(screen.getByLabelText('新建原因（必填）'), {
      target: { value: '运维交接：张三接手节点值班' },
    });
    expect(screen.getByTestId('create-blocked-hint').textContent).toContain('6 位码');

    fireEvent.click(submit());
    expect(writes(calls).length).toBe(0);
  });

  it('收齐之后：TOTP 进请求头，permissions 默认空数组', async () => {
    const created = { id: 9, email: 'new@example.com', permissions: [], totp_enabled: true, created_at: '2026-08-30T00:00:00Z' };
    const calls = stubFetch((call) =>
      call.method === 'POST'
        ? ok(created, { 'X-Next-Step': 'POST /api/v1/admin/admins/9/reset-totp' }, 201)
        : ok([OWNER]),
    );
    renderPage();
    await openForm();

    fireEvent.change(screen.getByLabelText('邮箱（Google 账号）'), { target: { value: 'new@example.com' } });
    fireEvent.change(screen.getByLabelText('新建原因（必填）'), {
      target: { value: '运维交接：张三接手节点值班' },
    });
    fireEvent.change(screen.getByLabelText('你自己的验证器 6 位码'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: /创建/ }));

    await waitFor(() => expect(writes(calls).length).toBe(1));
    const post = writes(calls)[0];
    expect(post?.headers.get('X-TOTP-Code')).toBe('123456');
    expect(JSON.parse(post?.body ?? '{}')).toEqual({
      email: 'new@example.com',
      permissions: [],
      reason: '运维交接：张三接手节点值班',
    });
  });

  it('🔴 建完之后把「他现在登不进去」挡在眼前，并显示服务端指定的下一步', async () => {
    const created = { id: 9, email: 'new@example.com', permissions: [], totp_enabled: true, created_at: '2026-08-30T00:00:00Z' };
    stubFetch((call) =>
      call.method === 'POST'
        ? ok(created, { 'X-Next-Step': 'POST /api/v1/admin/admins/9/reset-totp' }, 201)
        : ok([OWNER]),
    );
    renderPage();
    await openForm();

    fireEvent.change(screen.getByLabelText('邮箱（Google 账号）'), { target: { value: 'new@example.com' } });
    fireEvent.change(screen.getByLabelText('新建原因（必填）'), {
      target: { value: '运维交接：张三接手节点值班' },
    });
    fireEvent.change(screen.getByLabelText('你自己的验证器 6 位码'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: /创建/ }));

    expect(await screen.findByText(/登不进去/)).toBeTruthy();
    expect(screen.getByText('去给他生成绑定材料')).toBeTruthy();
    expect(screen.getByText(/POST \/api\/v1\/admin\/admins\/9\/reset-totp/)).toBeTruthy();
  });

  it('D6 那个权限位在界面上看得见但选不了（ADR 0012 §16.3：sink 没验证前对所有人关闭）', async () => {
    stubFetch(() => ok([OWNER]));
    renderPage();
    await openForm();

    const boxes = screen.getAllByRole('checkbox') as HTMLInputElement[];
    const markPaid = boxes[1];
    expect(markPaid?.disabled).toBe(true);
    expect(screen.getByText(/授不了，服务端会 422/)).toBeTruthy();
  });

  it('422 走同一张 ErrorCode 文案表，并把服务端的话原样显示', async () => {
    const message = '该邮箱已被占用；若它属于一位已停用的管理员，请先确认迁移 0019 已执行';
    stubFetch((call) => (call.method === 'POST' ? fail(422, 'VALIDATION_FAILED', message) : ok([OWNER])));
    renderPage();
    await openForm();

    fireEvent.change(screen.getByLabelText('邮箱（Google 账号）'), { target: { value: 'owner@example.com' } });
    fireEvent.change(screen.getByLabelText('新建原因（必填）'), {
      target: { value: '运维交接：张三接手节点值班' },
    });
    fireEvent.change(screen.getByLabelText('你自己的验证器 6 位码'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: /创建/ }));

    expect(await screen.findByText(message)).toBeTruthy();
    // 失败之后 TOTP 输入框必须被清空：那个码已经报废了（占用不随业务失败回滚）。
    expect((screen.getByLabelText('你自己的验证器 6 位码') as HTMLInputElement).value).toBe('');
  });
});

/* ────────────────────────── D16 重置他人 TOTP ────────────────────────── */

describe('D16 重置他人 TOTP', () => {
  async function openPanel(): Promise<void> {
    fireEvent.click(await screen.findByRole('button', { name: '重置他的 TOTP（并拿到绑定材料）' }));
  }

  it('确认串 / 原因 / TOTP 没收齐时不许提交', async () => {
    const calls = stubFetch(() => ok([OWNER]));
    renderPage();
    await openPanel();

    const submit = () => screen.getByRole('button', { name: '生成新的绑定材料' }) as HTMLButtonElement;
    expect(submit().disabled).toBe(true);

    fireEvent.change(screen.getByLabelText('输入管理员邮箱以确认'), { target: { value: 'owner@example.co' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '他换手机了，验证器没迁移' } });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '123456' } });
    // 确认串少一个字符 —— 仍然不许提交。
    expect(submit().disabled).toBe(true);

    fireEvent.click(submit());
    expect(writes(calls).length).toBe(0);
  });

  it('收齐之后：拿到绑定材料并明说只显示这一次、不许贴进工单', async () => {
    const calls = stubFetch((call) =>
      call.url.includes('/reset-totp')
        ? ok({ secret: 'JBSWY3DPEHPK3PXP', otpauth_url: 'otpauth://totp/BabelPlus:owner@example.com?secret=JBSWY3DPEHPK3PXP' })
        : ok([OWNER]),
    );
    renderPage();
    await openPanel();

    fireEvent.change(screen.getByLabelText('输入管理员邮箱以确认'), { target: { value: 'owner@example.com' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '他换手机了，验证器没迁移' } });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: '生成新的绑定材料' }));

    await waitFor(() => expect(writes(calls).length).toBe(1));
    const post = writes(calls)[0];
    expect(post?.headers.get('X-TOTP-Code')).toBe('123456');
    expect(JSON.parse(post?.body ?? '{}')).toEqual({
      confirmation: 'owner@example.com',
      reason: '他换手机了，验证器没迁移',
    });

    expect(await screen.findByText('JBSWY3DPEHPK3PXP')).toBeTruthy();
    expect(screen.getByText(/只显示这一次/)).toBeTruthy();
    expect(screen.getByText(/不要把它贴进工单、聊天或邮件。/)).toBeTruthy();
  });

  it('说清楚旧码当场失效，没有「两把都能用」的过渡窗口', async () => {
    stubFetch(() => ok([OWNER]));
    renderPage();
    await openPanel();

    expect(screen.getByText(/旧验证码在提交的那一刻立刻失效/)).toBeTruthy();
    expect(screen.getByText(/服务端不发这封信/)).toBeTruthy();
  });
});

/* ────────────────────────── D15 停用管理员 ────────────────────────── */

describe('D15 停用管理员（软停用，不是删除）', () => {
  it('只剩一个在职管理员时按钮点不动，并说明那个人就是你自己', async () => {
    stubFetch(() => ok([OWNER]));
    renderPage();

    await screen.findByText('owner@example.com');
    expect(screen.getByText(/他是列表里唯一的在职管理员/)).toBeTruthy();
  });

  it('两个人时可以展开，但确认串 / 原因 / TOTP 没收齐不许提交', async () => {
    const calls = stubFetch(() => ok([OWNER, SUPPORT]));
    renderPage();

    await screen.findByText('support@example.com');
    const buttons = screen.getAllByRole('button', { name: '停用这个管理员' });
    fireEvent.click(buttons[1] as HTMLElement); // support 那一行

    const submit = () => screen.getByRole('button', { name: '停用' }) as HTMLButtonElement;
    expect(submit().disabled).toBe(true);

    fireEvent.change(screen.getByLabelText('输入管理员邮箱以确认'), { target: { value: 'support@example.com' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '离职交接完成，回收后台权限' } });
    expect(submit().disabled).toBe(true); // 还差 TOTP
    fireEvent.click(submit());
    expect(writes(calls).length).toBe(0);
  });

  it('收齐之后发 DELETE，文案说的是「停用」而不是「删除」', async () => {
    const calls = stubFetch((call) =>
      call.method === 'DELETE' ? new Response(null, { status: 204 }) : ok([OWNER, SUPPORT]),
    );
    renderPage();

    await screen.findByText('support@example.com');
    fireEvent.click(screen.getAllByRole('button', { name: '停用这个管理员' })[1] as HTMLElement);
    fireEvent.change(screen.getByLabelText('输入管理员邮箱以确认'), { target: { value: 'support@example.com' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '离职交接完成，回收后台权限' } });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '123456' } });

    expect(screen.getByText(/停用（`disabled_at`），不是删除/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '停用' }));

    await waitFor(() => expect(writes(calls).length).toBe(1));
    const del = writes(calls)[0];
    expect(del?.method).toBe('DELETE');
    expect(del?.url).toContain('/api/v1/admin/admins/2');
    expect(del?.headers.get('X-TOTP-Code')).toBe('123456');
    expect(JSON.parse(del?.body ?? '{}')).toEqual({
      confirmation: 'support@example.com',
      reason: '离职交接完成，回收后台权限',
    });
  });
});

/* ────────────────────────── 页面必须说出口的话 ────────────────────────── */

describe('说明卡片', () => {
  it('把「谁能进后台」相关的四条缺口写在页面上', async () => {
    stubFetch(() => ok([OWNER, SUPPORT]));
    renderPage();

    await screen.findByText('owner@example.com');
    expect(screen.getByText('关于把自己锁在门外')).toBeTruthy();
    expect(screen.getByText(/前端不知道哪一行是你自己/)).toBeTruthy();
    expect(screen.getByText(/这个列表只列在职的/)).toBeTruthy();
    expect(screen.getByText(/没有撤销停用的入口/)).toBeTruthy();
    // 「权限位」这三个字在每张管理员卡片上也有一处，所以按那句独有的话取。
    expect(screen.getByText(/这两个直接动钱的权限位，通过 API 既看不见也授不了/)).toBeTruthy();
  });
});
