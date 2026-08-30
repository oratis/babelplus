// @vitest-environment jsdom
//
// 仓库级 vitest 配置是 `node`（`lib/iap.test.ts` 测的是纯函数）。这一支要挂组件，
// 所以用文件级 docblock 单独提高环境，而不是把整个包的默认环境改掉。

/**
 * 工单队列的测试。走**真实链路**：组件 → `api()` → 传输层 → 被替换掉的全局 `fetch`。
 * 不 mock 取数函数 —— 那样测的就只是「我写的 mock 会不会返回我塞进去的东西」。
 *
 * 钉住的几条里，有三条是**关于诚实的**，它们比「列表能不能渲染」更容易在重构里丢掉：
 *  · 筛不出东西时说的是「已加载的 N 条里没有」，不是「没有这样的工单」；
 *  · 队列自己声明它**不是**按 SLA 排的；
 *  · 每一行**不是链接**（列表没有数字主键，而会话端点只认数字主键）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useParams } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import TicketsPage from './TicketsPage.tsx';

const REQUEST_ID = '01K2TICKETTICKETTICKET00';

interface TicketFixture {
  public_id: string;
  subject: string;
  category: string;
  status: string;
  level?: number;
  created_at: string;
  last_reply_at?: string;
}

const OPEN_TICKET: TicketFixture = {
  public_id: 'BP-7K2M9Q',
  subject: '订阅拉不到节点',
  category: 'subscription',
  status: 'open',
  level: 3,
  created_at: '2026-08-20T02:00:00Z',
  last_reply_at: '2026-08-25T02:00:00Z',
};

const CLOSED_TICKET: TicketFixture = {
  public_id: 'BP-ZZZ111',
  subject: '付了款没到账',
  category: 'billing',
  status: 'closed',
  level: 2,
  created_at: '2026-07-01T02:00:00Z',
};

function okPage(items: TicketFixture[], meta: Record<string, unknown> = {}): Response {
  return new Response(JSON.stringify({ data: items, meta: { request_id: REQUEST_ID, has_more: false, ...meta } }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorEnvelope(status: number, code: string, message: string): Response {
  return new Response(JSON.stringify({ error: { code, message }, meta: { request_id: REQUEST_ID } }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** 每次调用都造一个新的 `Response`：body 是一次性的流，复用会在第二次读时炸掉。 */
function respondWith(make: () => Response) {
  const spy = vi.fn(async () => make());
  vi.stubGlobal('fetch', spy);
  return spy;
}

function DetailProbe() {
  const { id } = useParams();
  return <div data-testid="detail">{id}</div>;
}

function renderQueue() {
  return render(
    <MemoryRouter initialEntries={['/admin/tickets']}>
      <Routes>
        <Route path="/admin/tickets" element={<TicketsPage />} />
        <Route path="/admin/tickets/:id" element={<DetailProbe />} />
      </Routes>
    </MemoryRouter>,
  );
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

describe('工单队列', () => {
  it('渲染队列，并显示管理面独有的总数（用户面永不返 total）', async () => {
    respondWith(() => okPage([OPEN_TICKET, CLOSED_TICKET], { total: 87 }));
    renderQueue();

    await waitFor(() => expect(screen.getAllByTestId('ticket-row').length).toBe(2));
    expect(screen.getByText('订阅拉不到节点')).toBeTruthy();
    expect(screen.getByText('BP-7K2M9Q')).toBeTruthy();
    // 状态说的是「球在谁那边」，不是数据库里的词。
    expect(screen.getAllByText('待客服回复').length).toBeGreaterThan(0);
    expect(screen.getByText('全库 87 张')).toBeTruthy();
  });

  it('空态说的是「队列是空的」，并把矛头指向用户面的工单入口', async () => {
    respondWith(() => okPage([]));
    renderQueue();

    expect(await screen.findByText('队列是空的')).toBeTruthy();
    expect(screen.queryAllByTestId('ticket-row').length).toBe(0);
  });

  it('501 → 说「尚未开放」，不渲染成一次故障', async () => {
    respondWith(() => errorEnvelope(501, 'NOT_IMPLEMENTED', '尚未实现'));
    renderQueue();

    expect(await screen.findByText('尚未开放')).toBeTruthy();
    // 501 归一后的 kind 是 server，若走了 ErrorState 就会说「我们这边出了问题」并把人推去状态页。
    expect(screen.queryByText('我们这边出了问题')).toBeNull();
  });

  it('403 按 ErrorCode 分支：缺的是角色，重新登录没有帮助', async () => {
    respondWith(() => errorEnvelope(403, 'AUTH_PERMISSION_DENIED', '当前角色不能读工单'));
    renderQueue();

    expect(await screen.findByText('这个账号看不了工单')).toBeTruthy();
    expect(screen.getByText(/重新登录不会有帮助/)).toBeTruthy();
  });

  it('队列自己声明它不是按 SLA 排的 —— 一个只排了几页的队列不许自称 SLA 队列', async () => {
    respondWith(() => okPage([OPEN_TICKET], { total: 1 }));
    renderQueue();

    await screen.findByTestId('ticket-row');
    const caveat = screen.getByTestId('queue-caveat');
    expect(caveat.textContent).toContain('排序不是 SLA 排序');
    expect(caveat.textContent).toContain('静默');
  });

  it('每一行不是链接，并说清楚为什么（列表没有数字主键，会话端点只认数字主键）', async () => {
    respondWith(() => okPage([OPEN_TICKET]));
    renderQueue();

    await screen.findByTestId('ticket-row');
    // 🔴 把 public_id 拼进详情页地址是这里最容易犯的错：请求校验层会回 400，
    //    而 400 在界面上看起来像「这个工单不存在」。
    expect(document.querySelector(`a[href="/admin/tickets/${OPEN_TICKET.public_id}"]`)).toBeNull();
    expect(screen.getByText(/不是链接/)).toBeTruthy();
  });

  it('「按数字 id 打开」：非数字不许提交，数字才跳转', async () => {
    respondWith(() => okPage([OPEN_TICKET]));
    renderQueue();

    await screen.findByTestId('ticket-row');
    const input = screen.getByLabelText('按数字 id 打开会话');
    const open = () => screen.getByRole('button', { name: '打开' }) as HTMLButtonElement;

    fireEvent.change(input, { target: { value: 'BP-7K2M9Q' } });
    expect(open().disabled).toBe(true);
    fireEvent.click(open());
    expect(screen.queryByTestId('detail')).toBeNull();

    fireEvent.change(input, { target: { value: '1024' } });
    expect(open().disabled).toBe(false);
    fireEvent.click(open());
    expect(screen.getByTestId('detail').textContent).toBe('1024');
  });

  it('筛选是在浏览器里做的：筛不出东西时说「已加载的 N 条里没有」，不说「没有」', async () => {
    respondWith(() => okPage([OPEN_TICKET, CLOSED_TICKET], { total: 87 }));
    renderQueue();

    await waitFor(() => expect(screen.getAllByTestId('ticket-row').length).toBe(2));

    fireEvent.change(screen.getByLabelText('状态'), { target: { value: 'closed' } });
    expect(screen.getAllByTestId('ticket-row').length).toBe(1);
    expect(screen.getByText('付了款没到账')).toBeTruthy();

    fireEvent.change(screen.getByLabelText('工单号 / 主题'), { target: { value: '不存在的关键词' } });
    expect(screen.queryAllByTestId('ticket-row').length).toBe(0);
    // 🔴 这一句是本用例的全部意义。
    expect(screen.getByText(/当前筛选条件在已加载的 2 条里没有命中/)).toBeTruthy();
    expect(screen.getByText(/全库没有符合条件的工单/)).toBeTruthy();
  });

  it('「静默最久的在前」会把已关闭的沉底 —— 它们不需要人处理', async () => {
    respondWith(() => okPage([OPEN_TICKET, CLOSED_TICKET]));
    renderQueue();

    await waitFor(() => expect(screen.getAllByTestId('ticket-row').length).toBe(2));
    fireEvent.change(screen.getByLabelText('排序'), { target: { value: 'waiting' } });

    const rows = screen.getAllByTestId('ticket-row');
    // 已关闭那张更老（2026-07-01），按纯静默时长排会排在前面；沉底规则把它压到后面。
    expect(rows[0]?.textContent).toContain('BP-7K2M9Q');
    expect(rows[1]?.textContent).toContain('BP-ZZZ111');
  });
});
