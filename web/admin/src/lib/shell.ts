/** 脚手架期的三态开关。接线时删除，换成真实请求状态。 */
import { useLocation } from 'react-router';
import { resolveShellState, type ResolvedShellState, type ShellState } from '@babelplus/shared';

export function useShellState(fallback: ShellState = 'ready'): ResolvedShellState {
  const { search } = useLocation();
  return resolveShellState(search, fallback);
}
