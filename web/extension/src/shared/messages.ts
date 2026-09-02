/**
 * 页面 ↔ service worker 的消息协议。
 *
 * 只有 service worker 持有状态（它是唯一在浏览器关掉 popup 之后还活着的东西）；
 * 三个页面都是「发一条请求、拿一份 Snapshot」的薄壳，页面之间不直接通信。
 * service worker 状态变化时主动广播一份 Snapshot（`SNAPSHOT_EVENT`），打开着的页面据此重绘。
 */
import type { Prefs, Snapshot } from './types.ts';

export type OpenTarget = 'topup' | 'renew' | 'help' | 'backup' | 'options' | 'onboarding';

export type Request =
  | { readonly type: 'snapshot' }
  | { readonly type: 'sign-in'; readonly email: string; readonly password: string }
  | { readonly type: 'sign-out' }
  | { readonly type: 'connect'; readonly region?: string | null }
  | { readonly type: 'disconnect' }
  | { readonly type: 'cancel' }
  | { readonly type: 'refresh' }
  | { readonly type: 'set-prefs'; readonly prefs: Partial<Prefs> }
  | { readonly type: 'diagnostics' }
  | { readonly type: 'open'; readonly target: OpenTarget };

export type Response =
  | { readonly ok: true; readonly snapshot: Snapshot; readonly text?: string }
  | { readonly ok: false; readonly error: { readonly code: string; readonly message: string } };

export const SNAPSHOT_EVENT = 'bp:snapshot' as const;

export interface SnapshotEvent {
  readonly type: typeof SNAPSHOT_EVENT;
  readonly snapshot: Snapshot;
}

export function isSnapshotEvent(value: unknown): value is SnapshotEvent {
  return (
    typeof value === 'object' &&
    value !== null &&
    (value as { type?: unknown }).type === SNAPSHOT_EVENT &&
    typeof (value as { snapshot?: unknown }).snapshot === 'object'
  );
}

export function isRequest(value: unknown): value is Request {
  return typeof value === 'object' && value !== null && typeof (value as { type?: unknown }).type === 'string';
}

/**
 * 页面侧的发送封装。`chrome.runtime.sendMessage` 在 service worker 没有响应时会 resolve 成 `undefined`
 * （不是 reject），所以这里把它归一成一个 `ok: false` —— popup 不该因为 SW 正在重启而卡在转圈上。
 */
export async function sendRequest(request: Request): Promise<Response> {
  try {
    const res = (await chrome.runtime.sendMessage(request)) as Response | undefined;
    if (!res) {
      return { ok: false, error: { code: 'NO_RESPONSE', message: 'The extension background did not respond.' } };
    }
    return res;
  } catch (cause) {
    return {
      ok: false,
      error: { code: 'SEND_FAILED', message: cause instanceof Error ? cause.message : String(cause) },
    };
  }
}
