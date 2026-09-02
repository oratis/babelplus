/**
 * 诊断报告：popup「Copy diagnostics」与 options 页导出的同一份 JSON，直接贴进工单（tutorials-spec 的排障定位）。
 *
 * 🔴 **里面不许出现的东西**：会话 token、代理凭据、端点主机名、用户邮箱、任何页面 URL。
 * 端点只以 id + 地区 + 延迟出现 —— 报告会被粘到工单、聊天软件、截图里，按「会被公开」来写。
 * `assertNoSecrets` 是这条纪律的测试钩子，不是运行时防线。
 */
import { deriveUiState } from './state.ts';
import type { Snapshot } from './types.ts';

export interface DiagnosticsEnv {
  readonly uiLanguage: string;
  readonly userAgent: string;
  readonly now: string;
}

export interface DiagnosticsReport {
  readonly product: 'babel.plus-extension';
  readonly version: string;
  readonly generatedAt: string;
  readonly uiLanguage: string;
  readonly userAgent: string;
  readonly state: string;
  readonly signedIn: boolean;
  readonly connection: {
    readonly status: string;
    readonly region: string | null;
    readonly reason: string | null;
    readonly connectedAt: string | null;
    readonly lastSuccessAt: string | null;
    readonly failedEndpoints: number;
  };
  readonly probes: readonly {
    readonly endpointId: number;
    readonly region: string;
    readonly ok: boolean;
    readonly latencyMs: number | null;
    readonly error: string | null;
  }[];
  readonly quota: {
    readonly usedBytes: number;
    readonly totalBytes: number;
    readonly expiredAt: string | null;
    readonly fetchedAt: string | null;
  } | null;
  readonly config: { readonly fetchedAt: string | null; readonly rulesRev: number | null };
  readonly prefs: {
    readonly mode: string;
    readonly alwaysProxyCount: number;
    readonly neverProxyCount: number;
    readonly autoConnect: boolean;
  };
  readonly lastError: { readonly code: string; readonly message: string } | null;
}

export function buildDiagnostics(snapshot: Snapshot, env: DiagnosticsEnv): DiagnosticsReport {
  const sub = snapshot.subscription;
  return {
    product: 'babel.plus-extension',
    version: snapshot.version,
    generatedAt: env.now,
    uiLanguage: env.uiLanguage,
    userAgent: env.userAgent,
    state: deriveUiState(snapshot, Date.parse(env.now) || Date.now()).kind,
    signedIn: snapshot.signedIn,
    connection: {
      status: snapshot.connection.status,
      region: snapshot.connection.region,
      reason: snapshot.connection.reason,
      connectedAt: snapshot.connection.connectedAt,
      lastSuccessAt: snapshot.connection.lastSuccessAt,
      failedEndpoints: snapshot.connection.failedEndpoints,
    },
    probes: snapshot.probes.map((p) => ({
      endpointId: p.endpointId,
      region: p.region,
      ok: p.ok,
      latencyMs: p.latencyMs,
      error: p.error,
    })),
    quota: sub
      ? {
          usedBytes: sub.upload_bytes + sub.download_bytes,
          totalBytes: sub.total_bytes,
          expiredAt: sub.expired_at ?? null,
          fetchedAt: snapshot.subscriptionFetchedAt,
        }
      : null,
    config: { fetchedAt: snapshot.configFetchedAt, rulesRev: snapshot.rulesRev },
    prefs: {
      mode: snapshot.prefs.mode,
      alwaysProxyCount: snapshot.prefs.alwaysProxy.length,
      neverProxyCount: snapshot.prefs.neverProxy.length,
      autoConnect: snapshot.prefs.autoConnect,
    },
    lastError: snapshot.lastError,
  };
}

export function diagnosticsText(report: DiagnosticsReport): string {
  return JSON.stringify(report, null, 2);
}
