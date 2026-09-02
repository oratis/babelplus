import { describe, expect, it, vi } from 'vitest';
import type { ProxyEndpoint } from '../shared/types.ts';
import { orderByProbe, probeEndpoints } from './probe.ts';
import { memoryProxyPort } from './proxy.ts';

const ep = (id: number, probe_url?: string): ProxyEndpoint => ({
  id,
  host: `e${id}.example.invalid`,
  port: 443,
  scheme: 'https',
  region: 'HK',
  label: `E${id}`,
  auth: { username: 'u', password: 'p' },
  ...(probe_url ? { probe_url } : {}),
});

describe('probeEndpoints', () => {
  it('逐台设「只有这一台」的 PAC 再取 probe_url；成功记延迟与出口 IP，失败记原因，无 probe_url 记未测', async () => {
    const proxy = memoryProxyPort();
    let clock = 1000;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      const url = new URL(String(input));
      const id = url.searchParams.get('ep');
      if (id === '2') throw new TypeError('Failed to fetch');
      clock += id === '1' ? 30 : 5;
      return new Response(JSON.stringify({ ip: `203.0.113.${id}` }), { headers: { 'Content-Type': 'application/json' } });
    });
    const progress: number[] = [];
    const results = await probeEndpoints(
      [ep(1, 'https://p.example.invalid/ip?ep=1'), ep(2, 'https://p.example.invalid/ip?ep=2'), ep(3)],
      { proxy, fetchImpl: fetchImpl as unknown as typeof fetch, timeoutMs: 500, controlPlaneHosts: [], now: () => clock, onProgress: (r) => progress.push(r.length) },
    );
    expect(results).toEqual([
      { endpointId: 1, region: 'HK', label: 'E1', ok: true, latencyMs: 30, exitIp: '203.0.113.1', error: null },
      { endpointId: 2, region: 'HK', label: 'E2', ok: false, latencyMs: null, exitIp: null, error: 'TypeError' },
      { endpointId: 3, region: 'HK', label: 'E3', ok: true, latencyMs: null, exitIp: null, error: null },
    ]);
    expect(progress).toEqual([1, 2, 3]);
    // 两次 set，各只含一台，且不含 DIRECT 兜底
    const sets = proxy.history.filter((h): h is { op: 'set'; pac: string } => h.op === 'set');
    expect(sets).toHaveLength(2);
    expect(sets[0]?.pac).toContain('"HTTPS e1.example.invalid:443"');
    expect(sets[1]?.pac).toContain('"HTTPS e2.example.invalid:443"');
    // 请求带缓存穿透参数且不带 cookie
    expect(fetchImpl.mock.calls[0]?.[1]).toMatchObject({ cache: 'no-store', credentials: 'omit' });
  });

  it('取消信号：中止后不再探测剩余端点', async () => {
    const proxy = memoryProxyPort();
    const abort = new AbortController();
    const fetchImpl = vi.fn(async () => {
      abort.abort();
      return new Response(JSON.stringify({ ip: '1.1.1.1' }), { headers: { 'Content-Type': 'application/json' } });
    });
    const results = await probeEndpoints([ep(1, 'https://p.example.invalid/ip'), ep(2, 'https://p.example.invalid/ip')], {
      proxy,
      fetchImpl: fetchImpl as unknown as typeof fetch,
      timeoutMs: 500,
      controlPlaneHosts: [],
      now: () => 0,
      signal: abort.signal,
    });
    expect(results).toHaveLength(1);
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });
});

describe('orderByProbe', () => {
  it('可用的按延迟升序，未测的排后，失败的不进', () => {
    const endpoints = [ep(1), ep(2), ep(3), ep(4)];
    const ordered = orderByProbe(endpoints, [
      { endpointId: 1, region: 'HK', label: 'E1', ok: true, latencyMs: 40, exitIp: null, error: null },
      { endpointId: 2, region: 'HK', label: 'E2', ok: true, latencyMs: null, exitIp: null, error: null },
      { endpointId: 3, region: 'HK', label: 'E3', ok: false, latencyMs: null, exitIp: null, error: 'timeout' },
      { endpointId: 4, region: 'HK', label: 'E4', ok: true, latencyMs: 12, exitIp: null, error: null },
    ]);
    expect(ordered.map((e) => e.id)).toEqual([4, 1, 2]);
  });
});
