/**
 * 自助诊断页的接线测试。
 *
 * 每个用例为什么必须存在：
 *
 *  1. **缺项显示成灰色「检测不可用」，不是红色「有问题」**（`检测失败 ≠ 检测到失败`）——
 *     这是这一页最容易被顺手改错的一条。把三种呈现写成
 *     `check?.ok === true ? 绿 : 红` 是最自然的简化，代码更短、看着更整洁，
 *     而它的后果是：一个账号完全正常的用户，因为服务端这一项没算出来，
 *     被告诉「你的流量用完了」并被推去付款。红绿两态的实现**永远不会报错**。
 *     附带钉死：检测不可用时**不给动作按钮**。
 *
 *  2. **`GEOIP,CN` 那条指引不许回潮** —— tutorials-spec §4.2 的排障表里写着
 *     「国内网站变慢/打不开 → 分流规则没生效，GEOIP,CN 的位置问题」，
 *     但 B46 实测：规则表里带 `GEOIP,CN` 时 mihomo **拒绝加载整份配置**，
 *     该规则已从下发里去掉（代价：国内流量现在也走节点）。
 *     照着文档把那条抄回排障树，就是让用户去查一条我们根本没下发的规则。
 *     以后有人「按文档补全排障树」时，这个用例会拦住他。
 *
 *  3. **初始态不发请求** —— §3.2.8 的空态是「还没跑过检查」+「全程只读」。
 *     改成进页面就自动跑，那句「点一下」的承诺就没了，而这一页的用户
 *     恰恰是已经在怀疑「是不是面板把我怎么了」的人。
 *
 *  4. **501 说「尚未开放」** —— 501 按状态码归一成 5xx，只按状态码分支的实现
 *     会把「后端还没写」说成服务故障并把人推去一个一切正常的状态页。
 *
 *  5. **排障树在自检失败时照样能用** —— 两块内容各自独立的三态。
 *     API 挂掉恰恰是最需要排障指引的时刻，把树放进自检的就绪态里，
 *     它就会在最需要它的时候消失。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { resetRuntimeConfig } from '@babelplus/shared';
import { resetApiClientForTests } from '../lib/api.ts';
import { resetSessionForTests } from '../lib/session.ts';
import DiagnosePage from './DiagnosePage.tsx';

const REQUEST_ID = '01K2DIAGDIAGDIAGDIAGDIAGDI';

const DELAY_NOTE = '流量与设备数来自节点每分钟一次的上报，可能有 1–2 分钟延迟；';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorResponse(status: number, code: string, message: string): Response {
  return jsonResponse(status, { error: { code, message }, meta: { request_id: REQUEST_ID } });
}

function diagnoseResponse(checks: unknown[], extra: Record<string, unknown> = {}): Response {
  return jsonResponse(200, {
    data: { checks, data_delay_note: DELAY_NOTE, ...extra },
    meta: { request_id: REQUEST_ID },
  });
}

const ALL_OK = [
  { key: 'account_active', ok: true, detail: { banned: false } },
  { key: 'not_expired', ok: true, detail: { expired_at: '2026-12-01T00:00:00Z' } },
  { key: 'traffic_left', ok: true, detail: { used_bytes: 1_000_000, total_bytes: 100_000_000_000 } },
  { key: 'device_under_limit', ok: true, detail: { device_count: 2, device_limit: 3, counted_by: 'ip' } },
];

let fetchCalls = 0;

function stubDiagnose(response: () => Response): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string | URL | Request) => {
      const path = new URL(String(input)).pathname;
      if (path !== '/api/v1/user/diagnose') throw new Error(`未预期的请求：${path}`);
      fetchCalls += 1;
      return response();
    }),
  );
}

function renderDiagnose() {
  return render(
    <MemoryRouter initialEntries={['/diagnose']}>
      <DiagnosePage />
    </MemoryRouter>,
  );
}

/** 点「开始检查」并等结果落地。 */
async function runChecks(): Promise<void> {
  fireEvent.click(screen.getByRole('button', { name: '开始检查' }));
  await waitFor(() => expect(screen.getByText('账号自检')).toBeTruthy());
}

/** 取某一行检查（徽章 + 说明 + 动作都在同一个 `li` 里）。 */
function row(label: string): HTMLElement {
  const li = screen.getByText(label).closest('li');
  if (!(li instanceof HTMLElement)) throw new Error(`没有找到检查行：${label}`);
  return li;
}

function rowText(label: string): string {
  return row(label).textContent ?? '';
}

beforeEach(() => {
  fetchCalls = 0;
  resetRuntimeConfig();
  resetSessionForTests();
  resetApiClientForTests();
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  delete window.__BP_RUNTIME_CONFIG__;
});

describe('DiagnosePage · 账号自检', () => {
  it('进页面不跑检查，点了才跑', async () => {
    stubDiagnose(() => diagnoseResponse(ALL_OK));
    renderDiagnose();

    expect(screen.getByText('还没跑过检查')).toBeTruthy();
    expect(fetchCalls).toBe(0);

    await runChecks();
    expect(fetchCalls).toBe(1);
  });

  it('四项都通过 → 四条「正常」，并原样显示服务端那句口径说明', async () => {
    stubDiagnose(() => diagnoseResponse(ALL_OK, { subscription_last_fetched_at: '2026-08-28T10:00:00Z' }));
    renderDiagnose();
    await runChecks();

    expect(screen.getAllByText('正常').length).toBe(4);
    expect(screen.getByText('四项都正常')).toBeTruthy();
    // `data_delay_note` 契约里写明「不是装饰」：前端不许改写或缩写它。
    expect(screen.getByText(DELAY_NOTE)).toBeTruthy();
  });

  it('🔴 某一项没出现在响应里 → 灰色「检测不可用」，**不是红色「有问题」，也不给动作按钮**', async () => {
    // 服务端只回了三项：流量那一项这次没算出来。
    stubDiagnose(() => diagnoseResponse(ALL_OK.filter((c) => c.key !== 'traffic_left')));
    renderDiagnose();
    await runChecks();

    const traffic = row('流量余额');
    // 徽章按**整段文本精确匹配**，不是 substring —— 说明句里也有「有问题」三个字，
    // 用 contains 判会把这个用例判成永远通过。
    expect(within(traffic).getByText('检测不可用')).toBeTruthy();
    expect(within(traffic).queryByText('有问题')).toBeNull();
    // 「我们没能检查」不该把用户推去付款 —— 他的账号可能根本没问题。
    expect(within(traffic).queryByRole('link', { name: /买流量包/ })).toBeNull();
    expect(traffic.textContent).toContain('不代表它有问题');

    // 其余三项照常显示，缺一项不影响别的项（各项互相独立）。
    expect(screen.getAllByText('正常').length).toBe(3);
    expect(screen.queryByText('有问题')).toBeNull();
  });

  it('不通过的项 → 红色 + 一句人话 + 一个动作，且诊断码里记成 0', async () => {
    stubDiagnose(() =>
      diagnoseResponse([
        ALL_OK[0],
        { key: 'not_expired', ok: false, detail: { expired_at: '2026-08-01T00:00:00Z' } },
        ALL_OK[2],
        ALL_OK[3],
      ]),
    );
    renderDiagnose();
    await runChecks();

    const expiry = row('订阅有效期');
    expect(within(expiry).getByText('有问题')).toBeTruthy();
    expect(expiry.textContent).toContain('订阅已经到期');
    expect(screen.getByRole('link', { name: /去续费/ }).getAttribute('href')).toBe('/plan');
    expect(screen.getByText('1 项有问题')).toBeTruthy();
    // A1E0T1D1：字母是检查项，1 通过 / 0 不通过 / x 这次没结果。
    expect(screen.getByTestId('diagnose-code').textContent).toBe('DG1-A1E0T1D1');
  });

  it('缺项在诊断码里记成 x，而不是记成失败', async () => {
    stubDiagnose(() => diagnoseResponse(ALL_OK.filter((c) => c.key !== 'device_under_limit')));
    renderDiagnose();
    await runChecks();

    expect(screen.getByTestId('diagnose-code').textContent).toBe('DG1-A1E1T1Dx');
  });

  it('诊断码带进建单链接：?from=diagnose&code=…&category=…', async () => {
    stubDiagnose(() =>
      diagnoseResponse([
        { key: 'account_active', ok: false, detail: { banned: true, banned_reason: '疑似共享' } },
        ...ALL_OK.slice(1),
      ]),
    );
    renderDiagnose();
    await runChecks();

    // 封禁不引导重新登录 —— 重登换不回来一个没被封的身份。
    expect(rowText('账号状态')).toContain('重新登录不会有帮助');

    const href = screen.getByRole('link', { name: /带着诊断码提工单/ }).getAttribute('href') ?? '';
    const params = new URLSearchParams(href.slice(href.indexOf('?') + 1));
    expect(params.get('from')).toBe('diagnose');
    expect(params.get('code')).toBe('DG1-A0E1T1D1');
    expect(params.get('category')).toBe('account');
  });

  it('一项都没回 → 空态，仍然不是红色失败', async () => {
    stubDiagnose(() => diagnoseResponse([]));
    renderDiagnose();

    fireEvent.click(screen.getByRole('button', { name: '开始检查' }));
    await waitFor(() => expect(screen.getByText('这次没有拿到任何检查结果')).toBeTruthy());
    expect(screen.getByRole('button', { name: '重新检查' })).toBeTruthy();
    expect(screen.queryByText('有问题')).toBeNull();
  });

  it('501 NOT_IMPLEMENTED → 「该功能尚未开放」，不是「我们这边出了问题」', async () => {
    stubDiagnose(() => errorResponse(501, 'NOT_IMPLEMENTED', '尚未实现'));
    renderDiagnose();

    fireEvent.click(screen.getByRole('button', { name: '开始检查' }));
    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    expect(screen.queryByText('我们这边出了问题')).toBeNull();
  });

  it('5xx → 统一错误态可重试，且排障树照样能用', async () => {
    stubDiagnose(() => errorResponse(500, 'INTERNAL_ERROR', '内部错误'));
    renderDiagnose();

    fireEvent.click(screen.getByRole('button', { name: '开始检查' }));
    await waitFor(() => expect(screen.getByText('我们这边出了问题')).toBeTruthy());

    // API 挂掉恰恰是最需要排障指引的时刻 —— 树不能跟着一起消失。
    expect(screen.getByText('客户端能拉到节点列表吗？')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '拉不到 / 列表是空的' }));
    expect(screen.getByText('订阅类问题')).toBeTruthy();
  });
});

describe('DiagnosePage · 排障决策树', () => {
  it('三个问题落到叶子，叶子通向建单并带上来源码', () => {
    stubDiagnose(() => diagnoseResponse(ALL_OK));
    renderDiagnose();

    fireEvent.click(screen.getByRole('button', { name: '能拉到' }));
    fireEvent.click(screen.getByRole('button', { name: '延迟正常' }));
    fireEvent.click(screen.getByRole('button', { name: '能打开，但很慢' }));

    expect(screen.getByText('速度慢')).toBeTruthy();
    // §4.3：这一篇的价值是把单流 vs 多流讲清楚，不是「请稍后再试」。
    expect(screen.getByText(/单流/)).toBeTruthy();

    const href = screen.getByRole('link', { name: /还是不行，提工单/ }).getAttribute('href') ?? '';
    const params = new URLSearchParams(href.slice(href.indexOf('?') + 1));
    expect(params.get('from')).toBe('diagnose');
    expect(params.get('code')).toBe('DIAG-TREE-SPEED');
    expect(params.get('category')).toBe('node-down');
  });

  it('🔴 「部分网站打不开」这一支里**不出现 GEOIP** —— B46 实证那条指引对不上实现', () => {
    stubDiagnose(() => diagnoseResponse(ALL_OK));
    const { container } = renderDiagnose();

    fireEvent.click(screen.getByRole('button', { name: '能拉到' }));
    fireEvent.click(screen.getByRole('button', { name: '延迟正常' }));
    fireEvent.click(screen.getByRole('button', { name: '部分网站打不开' }));

    const text = container.textContent ?? '';
    // 带 GEOIP,CN 时 mihomo 拒绝加载整份配置，该规则已从下发里去掉。
    // 让用户去查一条我们根本没下发的规则，只会白花他半小时。
    expect(text).not.toContain('GEOIP');
    // 取而代之的是现在就能说清楚的事实。
    expect(text).toContain('没有国内直连分流');
  });

  it('上一步能退回去（选错一步是这棵树最常见的操作）', () => {
    stubDiagnose(() => diagnoseResponse(ALL_OK));
    renderDiagnose();

    fireEvent.click(screen.getByRole('button', { name: '能拉到' }));
    expect(screen.getByText('节点的延迟显示正常吗？')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '上一步' }));
    expect(screen.getByText('客户端能拉到节点列表吗？')).toBeTruthy();
  });

  it('自测提示常驻：开着 TUN / fake-ip 时 ping 的结果不可信', () => {
    stubDiagnose(() => diagnoseResponse(ALL_OK));
    const { container } = renderDiagnose();
    expect(container.textContent).toContain('结果全都不可信');
  });

  it('docsUrl 未配置时不编一个假链接出来', () => {
    stubDiagnose(() => diagnoseResponse(ALL_OK));
    renderDiagnose();

    fireEvent.click(screen.getByRole('button', { name: '拉不到 / 列表是空的' }));
    expect(screen.getByRole('button', { name: /打开排障文档（未配置）/ })).toBeTruthy();
    expect(screen.queryByRole('link', { name: /打开排障文档/ })).toBeNull();
  });

  it('配了 docsUrl 就给站外链接（面板被封时文档站还在）', () => {
    window.__BP_RUNTIME_CONFIG__ = { docsUrl: 'https://docs.example' };
    resetRuntimeConfig();
    stubDiagnose(() => diagnoseResponse(ALL_OK));
    renderDiagnose();

    fireEvent.click(screen.getByRole('button', { name: '拉不到 / 列表是空的' }));
    const link = screen.getByRole('link', { name: /打开排障文档/ });
    expect(link.getAttribute('href')).toBe('https://docs.example');
    expect(link.getAttribute('rel')).toContain('noreferrer');
  });
});
