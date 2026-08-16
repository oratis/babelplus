/**
 * 脚手架期的三态开关。见 `@babelplus/shared` 的 `resolveShellState` 注释。
 * 接线时整个文件删除，`useShellState()` 换成真实的请求状态。
 */
import { useLocation } from 'react-router';
import { resolveShellState, type ResolvedShellState, type ShellState } from '@babelplus/shared';

export function useShellState(fallback: ShellState = 'ready'): ResolvedShellState {
  const { search } = useLocation();
  return resolveShellState(search, fallback);
}
