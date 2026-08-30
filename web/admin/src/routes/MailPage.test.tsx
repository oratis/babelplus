// @vitest-environment jsdom

/**
 * 邮件与送达页的测试。
 *
 * 这里最要紧的一条不是「表格渲染对不对」，而是**这一页在没有数据时说了什么**：
 *
 *  🔴 `email_log.delivered_at` 现在没有任何写入方（要靠 ESP 的投递回调，而 ESP 一行没接）。
 *     照着 `送达数 / 发信数` 算，这一页会安静地显示 **0%**。
 *     「采集断了」被渲染成一个指标数字，是这条失效条件最坏的失败模式 ——
 *     它不会报错、不会变红，只会让看的人做出一个基于虚构数据的决定（换 ESP、改域名、改文案）。
 *     所以有一条用例专门断言：**没有回执时显示「尚无数据」，且整页不出现 0.0%**。
 *
 * 其余用例钉的是两处「做不到」的呈现（模板 501、自定义正文 501），
 * 以及 D11b 在参数没收齐时不许提交。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetAdminApiForTests } from '../lib/api.ts';
import MailPage from './MailPage.tsx';

const REQUEST_ID = '01K2MAILMAILMAILMAIL0000';

interface LogFixture {
  id: number;
  recipient_domain: string;
  esp?: string;
  template_key?: string;
  bounce_code?: string;
  sent_at: string;
  delivered_at?: string;
}

/** 三条 qq.com、一条 163.com，全部没有送达回执 —— 这就是今天线上的形态。 */
const LOGS_NO_RECEIPTS: LogFixture[] = [
  { id: 1, recipient_domain: 'qq.com', esp: 'unconfigured', template_key: 'verify_code', sent_at: '2026-08-29T02:00:00Z' },
  { id: 2, recipient_domain: 'qq.com', esp: 'unconfigured', template_key: 'verify_code', sent_at: '2026-08-29T03:00:00Z' },
  {
    id: 3,
    recipient_domain: 'qq.com',
    esp: 'unconfigured',
    template_key: 'domain_broadcast',
    bounce_code: 'smtp-unconfigured',
    sent_at: '2026-08-29T04:00:00Z',
  },
  { id: 4, recipient_domain: '163.com', esp: 'unconfigured', template_key: 'verify_code', sent_at: '2026-08-29T05:00:00Z' },
];

const PLANS = [
  { id: 7, name: '月付', type: 'period', prices: [], transfer_enable_bytes: 100, device_limit: 5 },
  { id: 8, name: '年付', type: 'period', prices: [], transfer_enable_bytes: 200, device_limit: 5 },
];

interface Call {
  readonly url: string;
  readonly method: string;
  readonly body: unknown;
}

interface Routes {
  templates?: () => Response;
  logs?: () => Response;
  plans?: () => Response;
  users?: () => Response;
  broadcast?: () => Response;
}

function ok(data: unknown, meta: Record<string, unknown> = {}, status = 200): Response {
  return new Response(JSON.stringify({ data, meta: { request_id: REQUEST_ID, has_more: false, ...meta } }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorEnvelope(status: number, code: string, message: string): Response {
  return new Response(JSON.stringify({ error: { code, message }, meta: { request_id: REQUEST_ID } }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const NOT_IMPLEMENTED = () => errorEnvelope(501, 'NOT_IMPLEMENTED', '尚未实现');

function stubFetch(routes: Routes) {
  const calls: Call[] = [];
  const spy = vi.fn(async (input: unknown, init?: RequestInit) => {
    const url = new URL(String(input));
    const method = (init?.method ?? 'GET').toUpperCase();
    let body: unknown;
    if (init?.body !== undefined && init.body !== null) {
      const raw =
        typeof init.body === 'string' ? init.body : new TextDecoder().decode(init.body as ArrayBuffer);
      body = raw === '' ? undefined : JSON.parse(raw);
    }
    calls.push({ url: `${url.pathname}${url.search}`, method, body });

    const path = url.pathname;
    if (path === '/api/v1/admin/mail/templates') return (routes.templates ?? NOT_IMPLEMENTED)();
    if (path === '/api/v1/admin/mail/logs') return (routes.logs ?? (() => ok(LOGS_NO_RECEIPTS)))();
    if (path === '/api/v1/admin/plans') return (routes.plans ?? (() => ok(PLANS)))();
    if (path === '/api/v1/admin/users') return (routes.users ?? (() => ok([], { total: 3412 })))();
    if (path === '/api/v1/admin/mail/broadcast') {
      return (routes.broadcast ?? (() => ok({ queued: 312 }, {}, 202)))();
    }
    return errorEnvelope(500, 'INTERNAL_ERROR', `测试里没有路由 ${path}`);
  });
  vi.stubGlobal('fetch', spy);
  return { spy, calls };
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

describe('邮件模板（501）', () => {
  it('说「尚未开放」并说清楚缺的是 mail_templates 这张表', async () => {
    stubFetch({});
    render(<MailPage />);

    expect(await screen.findByTestId('not-implemented')).toBeTruthy();
    const notice = screen.getByTestId('not-implemented');
    expect(notice.textContent).toContain('mail_templates');
    expect(notice.textContent).toContain('501');
  });

  it('不做成看起来能用的编辑器 —— 页面上没有任何模板输入框', async () => {
    stubFetch({});
    render(<MailPage />);

    await screen.findByTestId('not-implemented');
    // 主题框（群发用）是有的，模板的主题 / 正文框一个都不该有。
    expect(screen.queryByLabelText('模板正文')).toBeNull();
    expect(screen.queryByRole('button', { name: '保存模板' })).toBeNull();
    expect(document.querySelectorAll('textarea').length).toBe(0);
  });

  it('后端真的实现了的话就按只读列表渲染（回滚保险，不是死代码）', async () => {
    // 日志里也有 verify_code 这个模板键，所以断言挑一个只可能出现在模板卡片里的串。
    stubFetch({
      templates: () =>
        ok([{ id: 1, key: 'verify_code', subject: '你的验证码', body: '…', enabled: true }]),
    });
    render(<MailPage />);

    expect(await screen.findByText('你的验证码')).toBeTruthy();
    expect(screen.getByText('启用中')).toBeTruthy();
    expect(screen.queryByTestId('not-implemented')).toBeNull();
    // 即便列表出来了，编辑仍然是 501 —— 这句话要留着。
    expect(screen.getByText(/那一条还是 501/)).toBeTruthy();
  });
});

describe('按收件域名的送达统计', () => {
  it('🔴 没有任何送达回执时显示「尚无数据」，绝不显示 0%', async () => {
    stubFetch({ logs: () => ok(LOGS_NO_RECEIPTS, { total: 4 }) });
    render(<MailPage />);

    await waitFor(() => expect(screen.getAllByTestId('domain-stat').length).toBe(2));

    const rows = screen.getAllByTestId('domain-stat');
    for (const row of rows) {
      expect(row.textContent).toContain('尚无数据');
    }
    // 这一行是本用例的全部意义：把「采集断了」渲染成一个指标数字是最坏的失败模式。
    expect(document.body.textContent).not.toContain('0.0%');

    const caveat = screen.getByTestId('delivery-caveat');
    expect(caveat.textContent).toContain('不是 0%');
    expect(caveat.textContent).toContain('delivered_at');
  });

  it('一旦真的有回执，比值就照常算（0% 那时才是一个该报警的真值）', async () => {
    stubFetch({
      logs: () =>
        ok([
          { ...LOGS_NO_RECEIPTS[0], delivered_at: '2026-08-29T02:00:05Z' },
          LOGS_NO_RECEIPTS[1],
          LOGS_NO_RECEIPTS[3],
        ]),
    });
    render(<MailPage />);

    await waitFor(() => expect(screen.getAllByTestId('domain-stat').length).toBe(2));
    // qq.com：2 封里 1 封有回执。
    expect(screen.getByText('50.0%')).toBeTruthy();
    // 163.com：1 封 0 回执 —— 现在这个 0% 是有意义的。
    expect(screen.getByText('0.0%')).toBeTruthy();
  });

  it('按域名分组，且 QQ 与 163 分开看（总体数字会掩盖单个域名的塌方）', async () => {
    stubFetch({ logs: () => ok(LOGS_NO_RECEIPTS, { total: 4 }) });
    render(<MailPage />);

    await waitFor(() => expect(screen.getAllByTestId('domain-stat').length).toBe(2));
    const [first, second] = screen.getAllByTestId('domain-stat');
    expect(first?.textContent).toContain('qq.com');
    expect(second?.textContent).toContain('163.com');
  });

  it('样本口径写在表上：这是已加载的那几页，不是全库聚合', async () => {
    stubFetch({ logs: () => ok(LOGS_NO_RECEIPTS, { total: 40_000 }) });
    render(<MailPage />);

    const caveat = await screen.findByTestId('delivery-caveat');
    expect(caveat.textContent).toContain('样本是已加载的 4 条日志');
    expect(caveat.textContent).toContain('40,000');
    // 「失败」不是「退信」：D11b 的 5% / 10% 阈值现在测不到，这句话必须在。
    expect(caveat.textContent).toContain('测不到');
  });

  it('ESP 全是 unconfigured 时先说这件事 —— 否则下面那张表会被当成「发得挺好」', async () => {
    stubFetch({ logs: () => ok(LOGS_NO_RECEIPTS) });
    render(<MailPage />);

    const notice = await screen.findByTestId('esp-unconfigured');
    expect(notice.textContent).toContain('还没有接任何一家 ESP');
  });

  it('接上 ESP 之后那条提示自己消失（写死的话它会一直喊，然后被所有人忽略）', async () => {
    stubFetch({
      logs: () => ok([{ ...LOGS_NO_RECEIPTS[0], esp: 'ses' }]),
    });
    render(<MailPage />);

    await waitFor(() => expect(screen.getAllByTestId('domain-stat').length).toBe(1));
    expect(screen.queryByTestId('esp-unconfigured')).toBeNull();
  });

  it('日志为空时说的是「还没发过邮件」，不是加载失败', async () => {
    stubFetch({ logs: () => ok([], { total: 0 }) });
    render(<MailPage />);

    expect(await screen.findByText('还没有发过任何邮件。')).toBeTruthy();
  });

  it('域名过滤走服务端（`recipient_domain`），大小写归一后再发', async () => {
    const { calls } = stubFetch({});
    render(<MailPage />);

    await screen.findByTestId('delivery-caveat');
    fireEvent.change(screen.getByLabelText('按收件域名过滤'), { target: { value: '  QQ.com ' } });
    fireEvent.click(screen.getByRole('button', { name: '应用' }));

    await waitFor(() =>
      expect(calls.some((c) => c.url.includes('recipient_domain=qq.com'))).toBe(true),
    );
  });
});

describe('群发（D11b）', () => {
  async function openPanel() {
    render(<MailPage />);
    await screen.findByTestId('broadcast-half');
    fireEvent.click(screen.getByRole('button', { name: '群发邮件' }));
  }

  const submit = () => screen.getByRole('button', { name: '确认群发' }) as HTMLButtonElement;

  it('说清楚只有模板键驱动的那一半可用，且连正文框都不给', async () => {
    stubFetch({});
    render(<MailPage />);

    const half = await screen.findByTestId('broadcast-half');
    expect(half.textContent).toContain('501');
    expect(half.textContent).toContain('写一封临时正文发出去做不到');
    // 有正文框 = 有人会往里写字，然后系统发出去的是另一封信。
    expect(screen.queryByLabelText('邮件正文')).toBeNull();
  });

  it('主题没填 → 不许提交，并说出是哪一项缺了', async () => {
    stubFetch({});
    await openPanel();

    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '域名切换通知全体用户' } });
    expect(submit().disabled).toBe(true);
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('还没有填邮件主题');
  });

  it('原因少于 8 码位 → 不许提交（契约要求 reason，登记表那一行没写，这里显式打开了 L2）', async () => {
    const { calls } = stubFetch({});
    await openPanel();

    fireEvent.change(screen.getByLabelText('邮件主题'), { target: { value: '域名变更通知' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '换域名' } });

    expect(submit().disabled).toBe(true);
    fireEvent.click(submit());
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('至少 8 个字');
  });

  it('「按套餐」但一个套餐都没选 → 不许提交（服务端会命中 0 人并 422）', async () => {
    stubFetch({});
    await openPanel();

    fireEvent.change(screen.getByLabelText('邮件主题'), { target: { value: '域名变更通知' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '换域名要通知到人' } });
    expect(submit().disabled).toBe(false);

    fireEvent.change(screen.getByLabelText('收件范围'), { target: { value: 'by_plan' } });
    await screen.findByText('月付');
    expect(submit().disabled).toBe(true);
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('至少选中一个套餐');

    fireEvent.click(screen.getByLabelText('月付'));
    expect(submit().disabled).toBe(false);
  });

  it('参数齐了才提交：body 送的是**模板键**，reason 原样带上', async () => {
    const { calls } = stubFetch({});
    await openPanel();

    fireEvent.change(screen.getByLabelText('邮件主题'), { target: { value: ' 域名变更通知 ' } });
    fireEvent.change(screen.getByLabelText('收件范围'), { target: { value: 'expiring_soon' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), {
      target: { value: '  主域名被墙，通知 7 天内到期的用户  ' },
    });
    fireEvent.click(submit());

    await screen.findByText('已入队 312 封。');
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.url).toBe('/api/v1/admin/mail/broadcast');
    expect(post?.body).toEqual({
      subject: '域名变更通知',
      body: 'domain_broadcast',
      audience: 'expiring_soon',
      reason: '主域名被墙，通知 7 天内到期的用户',
    });
  });

  it('确认面板里不给一个假的收件人数，只给标明了的上界', async () => {
    stubFetch({});
    await openPanel();

    const facts = await screen.findByTestId('recipient-upper-bound');
    expect(facts.textContent).toContain('这一步算不出确切收件人数');
    expect(facts.textContent).toContain('3,412');
    // 🔴 上界必须被标成上界。在一个写着「收件人数」的位置放一个更大的数，比不放更危险。
    expect(facts.textContent).toContain('这不是本次的收件人数');
  });

  it('「强制先发测试件」这条要求没实现，确认面板里明说', async () => {
    stubFetch({});
    await openPanel();

    const facts = await screen.findByTestId('recipient-upper-bound');
    expect(facts.parentElement?.textContent).toContain('强制先发测试件');
    expect(facts.parentElement?.textContent).toContain('没有实现');
  });

  it('429 按 ErrorCode 分支：说的是「操作太频繁」，不是「服务端出错了」', async () => {
    stubFetch({
      broadcast: () => errorEnvelope(429, 'QUOTA_RATE_LIMITED', '群发过于频繁，每小时最多 2 次'),
    });
    await openPanel();

    fireEvent.change(screen.getByLabelText('邮件主题'), { target: { value: '域名变更通知' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '主域名被墙要通知' } });
    fireEvent.click(submit());

    expect(await screen.findByText('操作太频繁')).toBeTruthy();
  });

  it('自定义正文的 501 若真的发生（白名单变了），说的是「还没上线」而不是故障', async () => {
    stubFetch({ broadcast: NOT_IMPLEMENTED });
    await openPanel();

    fireEvent.change(screen.getByLabelText('邮件主题'), { target: { value: '域名变更通知' } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '主域名被墙要通知' } });
    fireEvent.click(submit());

    expect(await screen.findByText('这个操作还没上线')).toBeTruthy();
  });
});
