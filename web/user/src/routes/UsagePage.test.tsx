/**
 * `/usage` 的接线测试。
 *
 * 🔴 **这个文件存在的首要理由是第二个用例：空态的判据是「全是 0」，不是「数组为空」。**
 * `handler/usersub.go` 的 `buildUsageSeries` 把整个窗口**补齐成 days 个点**
 * （缺的那天填 0），所以新账号拿到的是 **30 个零点**而不是空数组。
 * 写成 `points.length === 0` 的人会觉得自己写对了 —— 代码能跑、类型也对 ——
 * 但新用户看到的是一张全是零的柱状图，而 page-inventory §3.2.7 逐字禁止这件事：
 * 「不显示空白图表 —— 一张全是零的柱状图看起来像坏了」。
 * 这条退化没有任何报错，只会表现为「新用户以为面板坏了」。
 *
 * 第三个用例守 §3.2.7 的另一条：**柱子不要从 0 动画增长到实际值**
 * （慢网络下会被误读成数据错误）。判据是「首次渲染时高度就是最终值」——
 * 有人加一个 `transition-[height]` 或把初值设成 0 再 `useEffect` 改，这一条就会红。
 *
 * 其余用例守三态独立：三个请求（曲线 / 周期进度 / 拉取审计）各挂各的，
 * 任何一个 501 或 5xx 都不许把另外两个吞掉。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, screen, waitFor } from '@testing-library/react';
import UsagePage from './UsagePage.tsx';
import {
  fail,
  jsonResponse,
  meRoute,
  notImplemented,
  ok,
  okPage,
  renderSignedIn,
  resetAll,
  stubFetch,
  type StubHandler,
} from './account-test-utils.tsx';

const USAGE_PATH = '/api/v1/user/usage';
const SUB_PATH = '/api/v1/user/subscription';
const LOG_PATH = '/api/v1/user/subscription/fetch-log';

const GIB = 1024 ** 3;

/** 后端口径：窗口一定被补齐，所以「没数据」= days 个零点。 */
function zeroFilledSeries(days: number) {
  return {
    range: '30d',
    points: Array.from({ length: days }, (_, i) => ({
      date: `2026-08-${String(i + 1).padStart(2, '0')}`,
      upload_bytes: 0,
      download_bytes: 0,
    })),
    total_upload_bytes: 0,
    total_download_bytes: 0,
  };
}

const SERIES_WITH_DATA = {
  range: '30d',
  points: [
    { date: '2026-08-01', upload_bytes: 0, download_bytes: 0 },
    { date: '2026-08-02', upload_bytes: 1 * GIB, download_bytes: 3 * GIB },
    { date: '2026-08-03', upload_bytes: 0, download_bytes: 4 * GIB },
  ],
  total_upload_bytes: 1 * GIB,
  total_download_bytes: 7 * GIB,
};

const SUBSCRIPTION = {
  urls: { short: 'https://api.example/s/token', long: 'https://api.example/x' },
  summary: {
    plan_name: '标准',
    upload_bytes: 1 * GIB,
    download_bytes: 7 * GIB,
    total_bytes: 100 * GIB,
    expired_at: '2099-01-01T00:00:00Z',
    reset_at: '2099-01-01T00:00:00Z',
    device_limit: 3,
    device_count: 1,
  },
};

const FETCH_LOG = [
  {
    id: 9,
    request_at: '2026-08-29T10:00:00Z',
    request_ip: '203.0.113.9',
    user_agent: 'clash-verge/1.7',
  },
];

/** 三条路都成功的基线，用例只覆盖它关心的那一条。 */
function allOk(overrides: Record<string, StubHandler> = {}) {
  return stubFetch({
    '/api/v1/user/me': meRoute(),
    [USAGE_PATH]: () => jsonResponse(200, ok(SERIES_WITH_DATA)),
    [SUB_PATH]: () => jsonResponse(200, ok(SUBSCRIPTION)),
    [LOG_PATH]: () => jsonResponse(200, okPage(FETCH_LOG)),
    ...overrides,
  });
}

beforeEach(resetAll);
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('UsagePage', () => {
  it('成功：合计走 formatBytes，上传/下载分开列，柱子逐日渲染', async () => {
    allOk();
    renderSignedIn(<UsagePage />);

    // 8 GiB 合计。这一页不做任何自制换算，数字必须与 formatBytes 的口径一致。
    await waitFor(() => expect(screen.getByText('8.00 GB')).toBeTruthy());
    expect(screen.getByText('7.00 GB')).toBeTruthy(); // 下载
    expect(screen.getByText('1.00 GB')).toBeTruthy(); // 上传
    expect(screen.getAllByRole('listitem').length).toBeGreaterThanOrEqual(3);
  });

  it('🔴 全是 0 的补齐序列 → 空态，**不画一张全是零的柱状图**', async () => {
    allOk({ [USAGE_PATH]: () => jsonResponse(200, ok(zeroFilledSeries(30))) });
    renderSignedIn(<UsagePage />);

    await waitFor(() => expect(screen.getByText('用满一天后这里会出现流量曲线')).toBeTruthy());
    // 空态必须给下一步动作（§2.2），不是一句「暂无数据」。
    expect(screen.getByRole('link', { name: /先看当前用量/ })).toBeTruthy();
    // 判据写成 `points.length === 0` 的话，这里会渲染出 30 根柱子。
    expect(screen.queryByLabelText(/共 0 B/)).toBeNull();
  });

  it('🔴 柱子首次渲染时高度就是最终值 —— 不从 0 动画增长', async () => {
    allOk();
    const { container } = renderSignedIn(<UsagePage />);

    await waitFor(() => expect(screen.getByText('8.00 GB')).toBeTruthy());

    const bars = Array.from(container.querySelectorAll('[aria-label$="GB"], [aria-label$="B"]'))
      .filter((el): el is HTMLElement => el instanceof HTMLElement && el.style.height !== '');
    expect(bars.length).toBeGreaterThan(0);
    // 峰值那一根（8-03 的 4 GiB）应当是 100%，而不是 0%。
    expect(bars.some((bar) => bar.style.height === '100%')).toBe(true);
    // 任何一根都不许带过渡类 —— 有过渡就意味着它是从别的值长过来的。
    for (const bar of bars) {
      expect(bar.className).not.toMatch(/transition|animate/);
    }
  });

  it('周期进度：「重置日 = 下单日」照说不误，配额进度按已用比例渲染', async () => {
    allOk();
    renderSignedIn(<UsagePage />);

    await waitFor(() => expect(screen.getByText(/重置日 = 你的下单日/)).toBeTruthy());
    const bar = screen.getByRole('progressbar', { name: '本周期流量使用进度' });
    expect(bar.getAttribute('aria-valuenow')).toBe('8');
  });

  it('🔴 曲线 501 → 「该功能尚未开放」，而周期进度与拉取审计照常显示', async () => {
    allOk({ [USAGE_PATH]: () => notImplemented() });
    renderSignedIn(<UsagePage />);

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    // 另外两个请求各挂各的 —— 整页一个 loading 的写法会让这两条断言全红。
    expect(await screen.findByText(/重置日 = 你的下单日/)).toBeTruthy();
    expect(screen.getByText('203.0.113.9')).toBeTruthy();
  });

  it('拉取审计 5xx → 只有那一块报错，曲线不受影响', async () => {
    allOk({ [LOG_PATH]: () => fail(500, 'INTERNAL_ERROR', '读不出来') });
    renderSignedIn(<UsagePage />);

    await waitFor(() => expect(screen.getByText('8.00 GB')).toBeTruthy());
    expect(screen.getByRole('button', { name: '重试' })).toBeTruthy();
  });

  it('拉取审计为空 → 空态引导去订阅页拿链接（这是「订阅被白嫖」的自助入口）', async () => {
    allOk({ [LOG_PATH]: () => jsonResponse(200, okPage([])) });
    renderSignedIn(<UsagePage />);

    await waitFor(() => expect(screen.getByText('还没有人拉取过你的订阅')).toBeTruthy());
    expect(screen.getByRole('link', { name: /去拿订阅链接/ })).toBeTruthy();
  });

  it('「按节点分布」不画假图 —— 明写它缺一张表', async () => {
    allOk();
    renderSignedIn(<UsagePage />);

    await waitFor(() => expect(screen.getAllByText(/stat_user_server/).length).toBeGreaterThan(0));
    expect(screen.getByText(/尚未接线/)).toBeTruthy();
    // 不许出现一个空的占位图表：那看起来像「这个功能坏了」。
    expect(screen.queryByRole('img')).toBeNull();
  });
});
