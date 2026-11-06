/**
 * 秒级倒计时。认证四页都要它，且要的**不是同一件事**：
 *
 *  - 登录页：429 的解锁倒计时（`Retry-After`）
 *  - 注册页：发验证码的 60 秒重发间隔（后端 `emailCodeMinGap` 就是 60 秒）
 *  - 找回密码：per-IP 限流的解锁倒计时
 *
 * 抽出来是因为这段代码有一个**只在特定输入下才犯的错**：
 * 计时器必须在 `seconds` 变化时重建，否则闭包里捕获的是旧值，倒计时会卡住不动。
 * 抄第二遍的时候很容易写成 `useEffect(..., [])`，而它在开发机上看起来「好像是对的」
 * —— 数字确实会从 60 变成 59，然后就再也不动了。
 *
 * 🔴 秒数一律来自服务端（`Retry-After` 响应头，CORS 里已 expose），
 * **读不到就传 `null`，绝不自己编一个** —— 编出来的倒计时会在用户眼皮底下走错，
 * 走完了还是被拒绝，比不显示倒计时更伤。
 */
import { useCallback, useEffect, useRef, useState } from 'react';

export interface Countdown {
  /** 剩余秒数；`null` = 没有在倒计时。 */
  readonly seconds: number | null;
  /** 是否正在倒计时。写成布尔值只是为了让调用点读起来是句人话。 */
  readonly active: boolean;
  /** 开始倒计时。传 `null` 或非正数表示「不倒计时」。 */
  start(seconds: number | null | undefined): void;
  /** 立刻停止（例如用户切走了那一步）。 */
  clear(): void;
}

export function useCountdown(): Countdown {
  const [seconds, setSeconds] = useState<number | null>(null);

  useEffect(() => {
    if (seconds === null || seconds <= 0) return;
    const timer = window.setInterval(() => {
      setSeconds((prev) => (prev === null || prev <= 1 ? null : prev - 1));
    }, 1000);
    return () => window.clearInterval(timer);
  }, [seconds]);

  const start = useCallback((next: number | null | undefined): void => {
    setSeconds(next === null || next === undefined || next <= 0 ? null : Math.floor(next));
  }, []);

  const clear = useCallback((): void => setSeconds(null), []);

  return { seconds, active: seconds !== null, start, clear };
}

/**
 * 组件卸载后不再 `setState` 的守卫。
 *
 * 注册流程里每一步都是「发请求 → 回来改状态」，而用户完全可能在请求在途时
 * 点「上一步」或直接离开这一页。React 18 起卸载后 setState 不再报警告，
 * 于是这类问题**没有任何提示**，只会表现为偶发的状态错乱。
 */
export function useMountedRef(): { readonly current: boolean } {
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  return mounted;
}
