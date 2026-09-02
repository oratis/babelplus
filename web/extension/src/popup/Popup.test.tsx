/**
 * popup 的八个状态逐个渲染，断言状态标签与主按钮 —— 这是 spec §3.5「八个状态每个都必须有设计」的测试形态。
 */
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { translator } from '../shared/i18n.ts';
import { DEFAULT_PREFS, OFFLINE_CONNECTION, type Snapshot, type SubscriptionSummary } from '../shared/types.ts';
import type { SnapshotHandle } from '../ui/useSnapshot.ts';
import { PopupView } from './Popup.tsx';

const GB = 1024 ** 3;
const NOW = Date.now();
const soon = new Date(NOW + 18.5 * 86_400_000).toISOString();

function summary(over: Partial<SubscriptionSummary> = {}): SubscriptionSummary {
  return { plan_name: 'Standard', upload_bytes: 2 * GB, download_bytes: 10 * GB, total_bytes: 20 * GB, expired_at: soon, device_count: 1, device_limit: 3, ...over };
}

function snap(over: Partial<Snapshot> = {}): Snapshot {
  return {
    version: '0.1.0',
    signedIn: true,
    subscription: summary(),
    subscriptionFetchedAt: null,
    connection: OFFLINE_CONNECTION,
    probes: [],
    regions: [{ code: 'HK', label: 'Hong Kong', latencyMs: 28, endpointCount: 2 }],
    prefs: DEFAULT_PREFS,
    links: { webUrl: 'https://web.example.invalid', backupPageUrl: 'https://backup.example.invalid', helpUrl: '' },
    configFetchedAt: null,
    rulesRev: 1,
    lastError: null,
    ...over,
  };
}

function handle(snapshot: Snapshot): SnapshotHandle & { send: ReturnType<typeof vi.fn> } {
  const send = vi.fn(async () => ({ ok: true as const, snapshot }));
  return { snapshot, loading: false, busy: false, error: null, send, clearError: () => undefined };
}

const t = translator('en');

afterEach(cleanup);

function renderState(s: Snapshot) {
  const h = handle(s);
  render(<PopupView t={t} handle={h} snapshot={s} />);
  return h;
}

describe('Popup —— 八个状态', () => {
  it('1 未登录：登录表单，页脚 Get a pass', () => {
    renderState(snap({ signedIn: false }));
    expect(screen.getByRole('status').textContent).toContain('Signed out');
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Get a pass →' })).toBeTruthy();
  });

  it('2 已登录关闭：配额条 + 地区 + Connect', () => {
    const h = renderState(snap());
    expect(screen.getByRole('status').textContent).toContain('Off');
    expect(screen.getByRole('progressbar').getAttribute('aria-valuenow')).toBe('60');
    expect(screen.getByText('18 days left')).toBeTruthy();
    screen.getByRole('button', { name: 'Connect' }).click();
    expect(h.send).toHaveBeenCalledWith({ type: 'connect', region: null });
  });

  it('3 连接中：探测进度 + Cancel', () => {
    const h = renderState(
      snap({
        connection: { ...OFFLINE_CONNECTION, status: 'connecting' },
        probes: [{ endpointId: 1, region: 'HK', label: 'hk-a', ok: true, latencyMs: 28, exitIp: null, error: null }],
      }),
    );
    expect(screen.getByRole('status').textContent).toContain('Connecting');
    expect(screen.getByText('28 ms ✓')).toBeTruthy();
    screen.getByRole('button', { name: 'Cancel' }).click();
    expect(h.send).toHaveBeenCalledWith({ type: 'cancel' });
  });

  it('4 已连接：出口 IP、会话用量、Disconnect', () => {
    const h = renderState(snap({ connection: { ...OFFLINE_CONNECTION, status: 'on', region: 'HK', exitIp: '203.0.113.7', usedAtConnect: 11.75 * GB } }));
    expect(screen.getByRole('status').textContent).toContain('Connected');
    expect(screen.getByText('203.0.113.7')).toBeTruthy();
    expect(screen.getByText('256 MB')).toBeTruthy();
    screen.getByRole('button', { name: 'Disconnect' }).click();
    expect(h.send).toHaveBeenCalledWith({ type: 'disconnect' });
  });

  it('5 将尽：主按钮变成 Top up（唯一的 upsell），连接保持', () => {
    const h = renderState(snap({ subscription: summary({ download_bytes: 16.5 * GB }), connection: { ...OFFLINE_CONNECTION, status: 'on', region: 'HK' } }));
    expect(screen.getByRole('status').textContent).toContain('Running low');
    expect(screen.getByRole('alert').textContent).toContain('1.5 GB left.');
    screen.getByRole('button', { name: 'Top up' }).click();
    expect(h.send).toHaveBeenCalledWith({ type: 'open', target: 'topup' });
    expect(screen.getByRole('button', { name: 'Disconnect' })).toBeTruthy();
  });

  it('6 用尽：Buy more data，Connect 禁用', () => {
    renderState(snap({ subscription: summary({ download_bytes: 18 * GB }) }));
    expect(screen.getByRole('status').textContent).toContain('Used up');
    expect(screen.getByRole('button', { name: 'Buy more data' })).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Connect' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('7 已过期：说明规则的那一刻就是它生效的那一刻；Renew', () => {
    const h = renderState(snap({ subscription: summary({ expired_at: '2026-09-02T00:00:00Z' }) }));
    expect(screen.getByRole('status').textContent).toContain('Expired');
    expect(screen.getByRole('alert').textContent).toContain('Your pass ended on 2 September.');
    expect(screen.getByText('Carry-over only works if you renew before a pass ends.')).toBeTruthy();
    screen.getByRole('button', { name: 'Renew' }).click();
    expect(h.send).toHaveBeenCalledWith({ type: 'open', target: 'renew' });
  });

  it('8 全部端点不可达：故障要响 —— 明说没有静默直连，给 Retry / 备用页 / 诊断', () => {
    const h = renderState(snap({ connection: { ...OFFLINE_CONNECTION, status: 'no-route', reason: 'all-endpoints-failed', failedEndpoints: 3, lastSuccessAt: new Date(NOW - 14 * 60_000).toISOString() } }));
    expect(screen.getByRole('status').textContent).toContain('No route');
    expect(screen.getByRole('alert').textContent).toContain('All 3 endpoints failed');
    expect(screen.getByRole('alert').textContent).toContain('nothing silently fell back to a direct connection');
    expect(screen.getByText('14 min ago')).toBeTruthy();
    screen.getByRole('button', { name: 'Retry' }).click();
    expect(h.send).toHaveBeenCalledWith({ type: 'connect', region: null });
    expect(screen.getByRole('button', { name: 'Open backup address page' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Copy diagnostics' })).toBeTruthy();
  });

  it('8b 服务端 501 时的不可达态把原因说出来，而不是一句「连不上」', () => {
    renderState(snap({ connection: { ...OFFLINE_CONNECTION, status: 'no-route', reason: 'config-unavailable' }, lastError: { code: 'NOT_IMPLEMENTED', message: 'HTTP 501' } }));
    expect(screen.getByRole('alert').textContent).toContain("Couldn't fetch your proxy configuration");
    expect(screen.getByRole('alert').textContent).toContain('HTTP 501');
  });
});
