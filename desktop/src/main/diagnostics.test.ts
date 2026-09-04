import { describe, expect, it } from 'vitest';
import { buildDiagnostics, diagnosticsText, type DiagnosticsInput } from './diagnostics.ts';
import { DEFAULT_PREFS, OFFLINE, type TabState } from '../shared/types.ts';

const GB = 1024 ** 3;

const tabs: TabState[] = [
  { id: 1, title: 'Some private page title', url: 'https://intranet.example.invalid/secret', route: 'proxy', bytes: 12, loading: false, blockedHost: null },
  { id: 2, title: '公司内网', url: 'https://hr.example.invalid/', route: 'direct', bytes: 3, loading: false, blockedHost: null },
  { id: 3, title: 'failed', url: 'https://x.example.invalid/', route: 'failed', bytes: 0, loading: false, blockedHost: 'x.example.invalid' },
];

const input: DiagnosticsInput = {
  version: '0.1.0',
  platform: 'darwin-arm64',
  signedIn: true,
  connection: {
    ...OFFLINE,
    status: 'on',
    port: 34567,
    outbound: 'HK-1 · REALITY',
    startedAt: '2026-09-04T08:00:00Z',
    restarts: 1,
  },
  subscription: {
    plan_name: 'Standard',
    upload_bytes: 1 * GB,
    download_bytes: 18.5 * GB,
    total_bytes: 20 * GB,
    expired_at: '2026-10-01T00:00:00Z',
    device_count: 1,
    device_limit: 3,
  },
  subscriptionFetchedAt: '2026-09-04T07:58:00Z',
  prefs: { ...DEFAULT_PREFS, alwaysProxy: ['secret-employer.example.invalid'], neverProxy: ['bank.example.invalid'] },
  tabs,
  corePath: '/somewhere/vendor/darwin-arm64/sing-box',
  now: '2026-09-04T08:05:00Z',
};

describe('诊断报告', () => {
  it('带上排障需要的字段', () => {
    const r = buildDiagnostics(input);
    expect(r).toMatchObject({
      product: 'babel.plus-browser',
      version: '0.1.0',
      platform: 'darwin-arm64',
      signedIn: true,
      core: { bundled: true, port: 'assigned' },
    });
    expect(r.connection).toMatchObject({ status: 'on', outbound: 'HK-1 · REALITY', restarts: 1 });
    expect(r.quota).toMatchObject({ usedBytes: 19.5 * GB, totalBytes: 20 * GB, low: true, exhausted: false });
    expect(r.tabs).toEqual({ total: 3, proxy: 1, direct: 1, failed: 1 });
  });

  it('🔴 不含任何能标识用户或他访问过什么的东西', () => {
    const text = diagnosticsText(buildDiagnostics(input));
    for (const secret of [
      'intranet.example.invalid',
      'hr.example.invalid',
      'x.example.invalid',
      'Some private page title',
      '公司内网',
      'secret-employer.example.invalid',
      'bank.example.invalid',
      '/somewhere/vendor',
      '34567',
    ]) {
      expect(text, secret).not.toContain(secret);
    }
    expect(text).not.toMatch(/token|password|uuid/i);
  });

  it('两个自定义列表**只报个数不报内容** —— 它们是访问过什么的强指纹', () => {
    const r = buildDiagnostics(input);
    expect(r.prefs).toEqual({
      mode: 'smart',
      alwaysProxyCount: 1,
      neverProxyCount: 1,
      launchAtStart: false,
      outboundPinned: false,
    });
  });

  it('没登录 / 没内核 / 没订阅时也能出报告，不抛', () => {
    const r = buildDiagnostics({
      ...input,
      signedIn: false,
      subscription: null,
      connection: { ...OFFLINE, lastError: { reason: 'core-missing', detail: '找不到随包内核' } },
      corePath: null,
      tabs: [],
    });
    expect(r.quota).toBeNull();
    expect(r.core).toEqual({ bundled: false, port: 'none' });
    expect(r.connection.lastError).toEqual({ reason: 'core-missing', detail: '找不到随包内核' });
    expect(r.tabs).toEqual({ total: 0, proxy: 0, direct: 0, failed: 0 });
  });
});
