/**
 * 诊断报告：设置页「Copy diagnostics」导出的那份 JSON，直接贴进工单。
 *
 * 🔴 **里面不许出现的东西**：会话 token、节点地址、uuid / 密码 / REALITY 公钥、
 * 用户邮箱、任何页面 URL 或标题。报告会被粘到工单、聊天软件、截图里，按「会被公开」来写。
 * `diagnostics.test.ts` 是这条纪律的执行形式，不是说明。
 *
 * 形状与扩展的 `shared/diagnostics.ts` 刻意保持同构（spec §5「同一个诊断 JSON 结构」）：
 * 支持看两份报告时不用换脑子。差别只有一处 —— 浏览器多一个 `core` 段（随包内核的状态），
 * 那是扩展没有的东西。
 */
import type { ConnectionState, Prefs, SubscriptionSummary, TabState } from '../shared/types.ts';
import { quotaView } from './quota.ts';

export interface DiagnosticsInput {
  readonly version: string;
  readonly platform: string;
  readonly signedIn: boolean;
  readonly connection: ConnectionState;
  readonly subscription: SubscriptionSummary | null;
  readonly subscriptionFetchedAt: string | null;
  readonly prefs: Prefs;
  readonly tabs: readonly TabState[];
  /** 随包内核的绝对路径；只用来判断「找到没找到」，**不进报告**。 */
  readonly corePath: string | null;
  readonly now: string;
}

export interface DiagnosticsReport {
  readonly product: 'babel.plus-browser';
  readonly version: string;
  readonly platform: string;
  readonly generatedAt: string;
  readonly signedIn: boolean;
  readonly connection: {
    readonly status: string;
    readonly outbound: string | null;
    readonly startedAt: string | null;
    readonly restarts: number;
    readonly lastError: { readonly reason: string; readonly detail: string } | null;
  };
  readonly core: { readonly bundled: boolean; readonly port: 'assigned' | 'none' };
  readonly quota: {
    readonly usedBytes: number;
    readonly totalBytes: number;
    readonly expiredAt: string | null;
    readonly low: boolean;
    readonly exhausted: boolean;
    readonly expired: boolean;
    readonly fetchedAt: string | null;
  } | null;
  readonly prefs: {
    readonly mode: string;
    readonly alwaysProxyCount: number;
    readonly neverProxyCount: number;
    readonly launchAtStart: boolean;
    readonly outboundPinned: boolean;
  };
  readonly tabs: { readonly total: number; readonly proxy: number; readonly direct: number; readonly failed: number };
}

export function buildDiagnostics(input: DiagnosticsInput): DiagnosticsReport {
  const q = quotaView(input.subscription, Date.parse(input.now) || Date.now());
  const count = (r: string): number => input.tabs.filter((t) => t.route === r).length;
  return {
    product: 'babel.plus-browser',
    version: input.version,
    platform: input.platform,
    generatedAt: input.now,
    signedIn: input.signedIn,
    connection: {
      status: input.connection.status,
      // 出口是节点的**展示名**（例 `HK-1 · REALITY`），不是主机名 —— 排障要它，且它不泄露地址。
      outbound: input.connection.outbound,
      startedAt: input.connection.startedAt,
      restarts: input.connection.restarts,
      lastError: input.connection.lastError
        ? { reason: input.connection.lastError.reason, detail: input.connection.lastError.detail }
        : null,
    },
    core: { bundled: input.corePath !== null, port: input.connection.port === null ? 'none' : 'assigned' },
    quota: q
      ? {
          usedBytes: q.usedBytes,
          totalBytes: q.totalBytes,
          expiredAt: q.expiredAt,
          low: q.low,
          exhausted: q.exhausted,
          expired: q.expired,
          fetchedAt: input.subscriptionFetchedAt,
        }
      : null,
    prefs: {
      mode: input.prefs.mode,
      // **只报个数不报内容**：这两个列表是用户访问过什么的强指纹。
      alwaysProxyCount: input.prefs.alwaysProxy.length,
      neverProxyCount: input.prefs.neverProxy.length,
      launchAtStart: input.prefs.launchAtStart,
      outboundPinned: input.prefs.outbound !== null,
    },
    tabs: { total: input.tabs.length, proxy: count('proxy'), direct: count('direct'), failed: count('failed') },
  };
}

export function diagnosticsText(report: DiagnosticsReport): string {
  return `${JSON.stringify(report, null, 2)}\n`;
}
