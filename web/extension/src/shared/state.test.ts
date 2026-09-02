import { describe, expect, it } from 'vitest';
import { quotaView } from './quota.ts';
import { deriveUiState } from './state.ts';
import { DEFAULT_PREFS, OFFLINE_CONNECTION, type Snapshot, type SubscriptionSummary } from './types.ts';

const NOW = Date.parse('2026-09-02T10:00:00Z');
const GB = 1024 ** 3;

function summary(over: Partial<SubscriptionSummary> = {}): SubscriptionSummary {
  return {
    plan_name: 'Standard',
    upload_bytes: 2 * GB,
    download_bytes: 10 * GB,
    total_bytes: 20 * GB,
    expired_at: '2026-09-20T00:00:00Z',
    device_count: 1,
    device_limit: 3,
    ...over,
  };
}

function snap(over: Partial<Snapshot> = {}): Snapshot {
  return {
    version: '0.1.0',
    signedIn: true,
    subscription: summary(),
    subscriptionFetchedAt: '2026-09-02T09:58:00Z',
    connection: OFFLINE_CONNECTION,
    probes: [],
    regions: [{ code: 'HK', label: 'Hong Kong', latencyMs: 28, endpointCount: 2 }],
    prefs: DEFAULT_PREFS,
    links: { webUrl: 'https://web.example.invalid', backupPageUrl: '', helpUrl: '' },
    configFetchedAt: null,
    rulesRev: 1,
    lastError: null,
    ...over,
  };
}

describe('quotaView', () => {
  it('用量、剩余天数、四个阈值', () => {
    const q = quotaView(summary(), NOW);
    expect(q).toMatchObject({ hasQuota: true, usedBytes: 12 * GB, remainingBytes: 8 * GB, daysLeft: 17, expired: false, exhausted: false, low: false });
    expect(q?.usedFraction).toBeCloseTo(0.6);
  });
  it('< 10% 为 low；用尽为 exhausted 且不再 low', () => {
    expect(quotaView(summary({ download_bytes: 16.5 * GB }), NOW)).toMatchObject({ low: true, exhausted: false });
    expect(quotaView(summary({ download_bytes: 18 * GB }), NOW)).toMatchObject({ low: false, exhausted: true });
  });
  it('expired_at 过去 = 已过期；null = 不限时', () => {
    expect(quotaView(summary({ expired_at: '2026-09-01T00:00:00Z' }), NOW)?.expired).toBe(true);
    expect(quotaView(summary({ expired_at: undefined }), NOW)).toMatchObject({ expired: false, daysLeft: null });
  });
  it('total_bytes = 0 不判 low / exhausted（契约没说清它是不限量还是没配）', () => {
    expect(quotaView(summary({ total_bytes: 0, download_bytes: 50 * GB }), NOW)).toMatchObject({ hasQuota: false, low: false, exhausted: false });
  });
});

describe('deriveUiState —— 八个状态与优先级', () => {
  it('1 未登录压倒一切', () => {
    expect(deriveUiState(snap({ signedIn: false, connection: { ...OFFLINE_CONNECTION, status: 'on' } }), NOW).kind).toBe('signed-out');
  });
  it('2 已过期优先于用尽', () => {
    const s = snap({ subscription: summary({ expired_at: '2026-09-01T00:00:00Z', download_bytes: 18 * GB }) });
    expect(deriveUiState(s, NOW).kind).toBe('expired');
  });
  it('3 用尽优先于连接态', () => {
    const s = snap({ subscription: summary({ download_bytes: 18 * GB }), connection: { ...OFFLINE_CONNECTION, status: 'on' } });
    expect(deriveUiState(s, NOW).kind).toBe('exhausted');
  });
  it('4 全部端点不可达带原因与失败数', () => {
    const s = snap({ connection: { ...OFFLINE_CONNECTION, status: 'no-route', reason: 'all-endpoints-failed', failedEndpoints: 3, lastSuccessAt: '2026-09-02T09:46:00Z' } });
    expect(deriveUiState(s, NOW)).toEqual({ kind: 'no-route', reason: 'all-endpoints-failed', failedEndpoints: 3, lastSuccessAt: '2026-09-02T09:46:00Z' });
  });
  it('5 连接中带探测进度', () => {
    const probes = [{ endpointId: 1, region: 'HK', label: 'hk-a', ok: true, latencyMs: 28, exitIp: null, error: null }];
    const s = snap({ connection: { ...OFFLINE_CONNECTION, status: 'connecting' }, probes });
    expect(deriveUiState(s, NOW)).toEqual({ kind: 'connecting', probes });
  });
  it('6 将尽：无论连没连都是 low，但带 connected', () => {
    const s = snap({ subscription: summary({ download_bytes: 16.5 * GB }), connection: { ...OFFLINE_CONNECTION, status: 'on', region: 'HK' } });
    expect(deriveUiState(s, NOW)).toMatchObject({ kind: 'low', connected: true, region: 'HK' });
    expect(deriveUiState(snap({ subscription: summary({ download_bytes: 16.5 * GB }) }), NOW)).toMatchObject({ kind: 'low', connected: false });
  });
  it('7 已连接：会话用量 = 最新已用 − 连接时已用', () => {
    const s = snap({ connection: { ...OFFLINE_CONNECTION, status: 'on', region: 'HK', exitIp: '203.0.113.7', usedAtConnect: 11.5 * GB } });
    expect(deriveUiState(s, NOW)).toMatchObject({ kind: 'on', exitIp: '203.0.113.7', sessionBytes: 0.5 * GB });
  });
  it('8 已登录关闭；订阅还没拉到时 quota 为 null 而不是崩', () => {
    expect(deriveUiState(snap(), NOW).kind).toBe('off');
    expect(deriveUiState(snap({ subscription: null }), NOW)).toEqual({ kind: 'off', quota: null, region: null });
  });
});
