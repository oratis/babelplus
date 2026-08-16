/**
 * 骨架阶段的「三态」开关。
 *
 * 这个脚手架里每一页都还没有业务逻辑，但**三态本身是设计交付物**（page-inventory §2.2 逐页指定了空态文案）。
 * 为了让人能真的看见这三个状态，而不是靠想象评审，所有页面统一从 URL 查询串读状态：
 *
 *   `?state=loading` / `?state=empty` / `?state=error&error=server` / `?state=ready`
 *
 * 接上真实 API 后，把 `useShellState()` 换成真的请求状态即可，页面结构一行不用改。
 * 这是脚手架的**临时开关，不是产品功能**，接线时应删除对 `state` 查询参数的读取。
 */
import type { ErrorKind } from './error-kind.ts';

export type ShellState = 'loading' | 'empty' | 'error' | 'ready';

export interface ResolvedShellState {
  readonly state: ShellState;
  readonly errorKind: ErrorKind;
}

const STATES: readonly ShellState[] = ['loading', 'empty', 'error', 'ready'];
const KINDS: readonly ErrorKind[] = ['unauthorized', 'forbidden', 'client', 'server', 'offline'];

export function resolveShellState(search: string, fallback: ShellState = 'ready'): ResolvedShellState {
  const params = new URLSearchParams(search);
  const raw = params.get('state');
  const state = STATES.find((s) => s === raw) ?? fallback;
  const rawKind = params.get('error');
  const errorKind = KINDS.find((k) => k === rawKind) ?? 'server';
  return { state, errorKind };
}
