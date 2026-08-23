/**
 * 「非 React 代码需要跳一次路由」的桥。
 *
 * API 客户端在收到 401 时要把用户送去登录页，但它是一个普通模块，拿不到 react-router 的
 * `navigate`。两种常见的错误做法：
 *
 *  1. `window.location.assign('/auth/login')` —— 整页重载。用户已填未提交的表单内容全部丢失，
 *     而 401 最容易发生在「填了十分钟工单正要提交」的那一刻。
 *  2. 把 navigate 存成模块级全局变量 —— 能用，但两个 SPA 共用 `shared` 时会共享同一个槽位，
 *     测试之间也会互相串味（上一个用例留下的 navigate 还挂着）。
 *
 * 所以这里给的是**工厂**：每个应用建自己的一座桥，测试里建一次性的桥。
 * 拿不到 navigate 时（React 树还没挂载 / 已卸载）才退化成整页跳转 —— 那时本来也没有表单可丢。
 */

export interface NavigateOptions {
  readonly replace?: boolean;
}

export type Navigator = (to: string, options?: NavigateOptions) => void;

export interface NavigationBridge {
  /** React 树里调用：挂上真正的 navigate；卸载时传 `null` 摘掉。 */
  setNavigator(navigate: Navigator | null): void;
  /** 任意模块调用。没挂 navigate 时退化为整页跳转。 */
  navigateTo(to: string, options?: NavigateOptions): void;
  /** 仅测试用：现在挂着 navigate 吗。 */
  hasNavigator(): boolean;
}

export function createNavigationBridge(fallback?: (to: string, options?: NavigateOptions) => void): NavigationBridge {
  let current: Navigator | null = null;

  const hardNavigate =
    fallback ??
    ((to: string, options?: NavigateOptions) => {
      if (typeof window === 'undefined') return;
      if (options?.replace) window.location.replace(to);
      else window.location.assign(to);
    });

  return {
    setNavigator(navigate) {
      current = navigate;
    },
    navigateTo(to, options) {
      if (current) current(to, options);
      else hardNavigate(to, options);
    },
    hasNavigator() {
      return current !== null;
    },
  };
}
