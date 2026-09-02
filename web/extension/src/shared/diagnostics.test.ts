import { describe, expect, it } from 'vitest';
import { buildDiagnostics, diagnosticsText } from './diagnostics.ts';
import { DEFAULT_PREFS, OFFLINE_CONNECTION, type Snapshot } from './types.ts';

const snapshot: Snapshot = {
  version: '0.1.0',
  signedIn: true,
  subscription: { upload_bytes: 1, download_bytes: 2, total_bytes: 30, expired_at: '2026-10-01T00:00:00Z', device_count: 1, device_limit: 3 },
  subscriptionFetchedAt: '2026-09-02T10:00:00Z',
  connection: { ...OFFLINE_CONNECTION, status: 'on', region: 'HK', exitIp: '203.0.113.7' },
  probes: [{ endpointId: 1, region: 'HK', label: 'hk-a.example.invalid', ok: true, latencyMs: 28, exitIp: '203.0.113.7', error: null }],
  regions: [],
  prefs: { ...DEFAULT_PREFS, alwaysProxy: ['secret-site.example.invalid'] },
  links: { webUrl: 'https://web.example.invalid', backupPageUrl: '', helpUrl: '' },
  configFetchedAt: '2026-09-02T09:59:00Z',
  rulesRev: 7,
  lastError: null,
};

describe('diagnostics', () => {
  it('带上定位需要的字段，去掉一切能标识用户与端点的字段', () => {
    const report = buildDiagnostics(snapshot, { uiLanguage: 'en-US', userAgent: 'UA', now: '2026-09-02T10:05:00Z' });
    expect(report).toMatchObject({ product: 'babel.plus-extension', version: '0.1.0', state: 'on', config: { fetchedAt: '2026-09-02T09:59:00Z', rulesRev: 7 } });
    expect(report.quota).toEqual({ usedBytes: 3, totalBytes: 30, expiredAt: '2026-10-01T00:00:00Z', fetchedAt: '2026-09-02T10:00:00Z' });
    expect(report.probes).toEqual([{ endpointId: 1, region: 'HK', ok: true, latencyMs: 28, error: null }]);
    expect(report.prefs).toEqual({ mode: 'smart', alwaysProxyCount: 1, neverProxyCount: 0, autoConnect: false });
    const text = diagnosticsText(report);
    expect(text).not.toContain('hk-a.example.invalid');
    expect(text).not.toContain('secret-site');
    expect(text).not.toContain('203.0.113.7');
  });
});
