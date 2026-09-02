/**
 * 端点探测：逐个把代理设成「只有这一台」，经它取 `probe_url`，记延迟与出口 IP。
 *
 * 为什么是逐个而不是并行：代理设置是浏览器级的一份，没有「这一个 fetch 走 A、那一个走 B」的 API。
 * 三台各 5 s 超时最坏 15 s，popup 的「连接中」态逐行显示进度（spec §3.5 状态 3）。
 *
 * 探测用的 PAC 只含一台且末位无 DIRECT（`buildSingleEndpointPac`）：候选串会把失败悄悄转到下一台，
 * 那样测出来的延迟不知道是谁的。
 *
 * 没有 `probe_url` 的端点记为「未测」（`ok: true, latencyMs: null`）：我们没法替服务端证明它可用，
 * 但也没有证据说它不可用；排序时放在实测可用的端点之后。
 */
import { buildSingleEndpointPac } from '../shared/pac.ts';
import type { ProbeResult, ProxyEndpoint } from '../shared/types.ts';
import type { ProxyPort } from './proxy.ts';

export interface ProbeDeps {
  readonly proxy: ProxyPort;
  readonly fetchImpl: typeof fetch;
  readonly timeoutMs: number;
  readonly controlPlaneHosts: readonly string[];
  readonly now: () => number;
  readonly signal?: AbortSignal | undefined;
  readonly onProgress?: ((results: readonly ProbeResult[]) => void) | undefined;
}

function errorLabel(cause: unknown): string {
  if (cause instanceof DOMException && cause.name === 'AbortError') return 'timeout';
  if (cause instanceof DOMException && cause.name === 'TimeoutError') return 'timeout';
  if (cause instanceof Error) return cause.name || 'error';
  return 'error';
}

function pickIp(body: unknown): string | null {
  if (typeof body !== 'object' || body === null) return null;
  const ip = (body as { ip?: unknown }).ip;
  return typeof ip === 'string' && ip.length > 0 ? ip : null;
}

export async function probeEndpoints(endpoints: readonly ProxyEndpoint[], deps: ProbeDeps): Promise<ProbeResult[]> {
  const results: ProbeResult[] = [];
  for (const ep of endpoints) {
    if (deps.signal?.aborted) break;
    const base = { endpointId: ep.id, region: ep.region, label: ep.label ?? ep.region };
    if (!ep.probe_url) {
      results.push({ ...base, ok: true, latencyMs: null, exitIp: null, error: null });
      deps.onProgress?.(results);
      continue;
    }
    try {
      await deps.proxy.setPac(buildSingleEndpointPac({ host: ep.host, port: ep.port }, deps.controlPlaneHosts));
      const url = new URL(ep.probe_url);
      url.searchParams.set('_', String(deps.now()));
      const t0 = deps.now();
      const timeout = AbortSignal.timeout(deps.timeoutMs);
      const anyFn = (AbortSignal as unknown as { any?: (s: AbortSignal[]) => AbortSignal }).any;
      const signal =
        deps.signal && typeof anyFn === 'function' ? anyFn([deps.signal, timeout]) : timeout;
      const res = await deps.fetchImpl(url.toString(), { cache: 'no-store', credentials: 'omit', signal });
      const latencyMs = Math.max(0, Math.round(deps.now() - t0));
      if (!res.ok) {
        results.push({ ...base, ok: false, latencyMs, exitIp: null, error: `http_${res.status}` });
      } else {
        let ip: string | null = null;
        try {
          ip = pickIp(await res.json());
        } catch {
          ip = null;
        }
        results.push({ ...base, ok: true, latencyMs, exitIp: ip, error: null });
      }
    } catch (cause) {
      results.push({ ...base, ok: false, latencyMs: null, exitIp: null, error: errorLabel(cause) });
    }
    deps.onProgress?.(results);
  }
  return results;
}

/** 可用端点按延迟升序；未测的排在实测可用的后面；失败的不进结果。 */
export function orderByProbe(endpoints: readonly ProxyEndpoint[], probes: readonly ProbeResult[]): ProxyEndpoint[] {
  const byId = new Map(probes.map((p) => [p.endpointId, p]));
  const usable = endpoints.filter((e) => byId.get(e.id)?.ok);
  return usable.sort((a, b) => {
    const la = byId.get(a.id)?.latencyMs;
    const lb = byId.get(b.id)?.latencyMs;
    if (la === null || la === undefined) return lb === null || lb === undefined ? 0 : 1;
    if (lb === null || lb === undefined) return -1;
    return la - lb;
  });
}
