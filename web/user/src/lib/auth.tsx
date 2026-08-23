/**
 * 登录态 Context + 路由守卫。
 *
 * 这个文件解决的是脚手架里最大的功能缺口：**所有页面都可直达**。
 *
 * 核心是一个三态而不是布尔值。「登录了没有」在前端不是一个二值问题，
 * 而是三值：**还不知道 / 确定登录了 / 确定没登录**。用布尔值表达会产生一个具体的坏体验 ——
 * 初值只能取 false，于是每次刷新页面都会先渲染一次登录页再跳回来。
 * 用户看到的是一次闪烁；如果他此刻正在填表单，看到的是表单没了。
 *
 * 三态的边界也定得很死：
 *  - 有 token 但 `/api/v1/user/me` 还没回来 → `unknown`，**渲染骨架，不跳转**；
 *  - `/me` 回 401 → `anonymous`，跳登录并带 returnTo；
 *  - `/me` 因为**网络不可达或 5xx** 而失败 → 仍然是 `unknown`，带一个 `bootstrapError`，
 *    渲染错误态 + 重试。**绝不跳登录** —— 「跨境链路抖了一下」和「你没登录」是两件事，
 *    把前者显示成后者会让用户在能用的会话上反复输密码（page-inventory §2.2 那条五类错误
 *    分类规则的直接推论）。
 */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import { Navigate, Outlet, useLocation } from 'react-router';
import {
  ApiError,
  getCurrentUser,
  unwrap,
  unwrapEmpty,
  type CurrentUser,
} from '@babelplus/shared/api';
import { loginUrlWithReturnTo } from '@babelplus/shared';
import { Card, ErrorState, SkeletonText } from '@babelplus/shared/ui';
import { api } from './api.ts';
import { LOGIN_PATH, session } from './session.ts';

export type AuthStatus = 'unknown' | 'authenticated' | 'anonymous';

export interface AuthContextValue {
  readonly status: AuthStatus;
  readonly user: CurrentUser | null;
  /**
   * 只在 `status === 'unknown'` 时可能有值：确认登录态这一步**失败了**。
   * 它**不表示未登录** —— 所以看到它的地方一律不许跳登录页。
   */
  readonly bootstrapError: ApiError | null;
  signIn(email: string, password: string): Promise<void>;
  signOut(): Promise<void>;
  reload(): Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function useAuth(): AuthContextValue {
  const value = useContext(AuthContext);
  // 抛错而不是返回一个「未登录」的默认值：忘了套 Provider 是装配错误，
  // 而一个默认值会把装配错误伪装成「用户没登录」，然后所有人被跳到登录页。
  if (!value) throw new Error('useAuth 必须在 <AuthProvider> 内使用');
  return value;
}

export function AuthProvider({ children }: { children: ReactNode }) {
  // 初值同步读 token：没有 token 时**立刻**就是 anonymous，不经过 unknown。
  // 这样「未登录用户打开受保护页面」是一次直接跳转，不是「骨架 → 跳转」。
  const [status, setStatus] = useState<AuthStatus>(() =>
    session().getToken() === null ? 'anonymous' : 'unknown',
  );
  const [user, setUser] = useState<CurrentUser | null>(null);
  const [bootstrapError, setBootstrapError] = useState<ApiError | null>(null);

  const reload = useCallback(async (): Promise<void> => {
    if (session().getToken() === null) {
      setUser(null);
      setBootstrapError(null);
      setStatus('anonymous');
      return;
    }
    setBootstrapError(null);
    setStatus((prev) => (prev === 'authenticated' ? prev : 'unknown'));
    try {
      const me = await getCurrentUser(api());
      setUser(me);
      setStatus('authenticated');
    } catch (cause) {
      const error = asApiError(cause, '确认登录状态失败');
      if (error.status === 401) {
        // 客户端那边已经把会话作废了（api.ts 的 handleAuthFailure），这里只同步 UI 状态。
        setUser(null);
        setStatus('anonymous');
        return;
      }
      // 网络 / 5xx / 其他：**留在 unknown**，让守卫渲染可重试的错误态。
      setUser(null);
      setBootstrapError(error);
      setStatus('unknown');
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  // 任何地方把会话作废（API 层收到 401、用户点登出），都从这里同步到 UI。
  // 订阅而不是各处手动 setState：漏一处就会出现「已经登出了但页面还在」。
  useEffect(
    () =>
      session().subscribe((token) => {
        if (token !== null) return;
        setUser(null);
        setBootstrapError(null);
        setStatus('anonymous');
      }),
    [],
  );

  const signIn = useCallback(async (email: string, password: string): Promise<void> => {
    const tokens = await unwrap(
      api().POST('/api/v1/auth/login', { body: { email, password } }),
    );
    // 后端两个字段是同一个值（auth.go 的 sessionTokens）。存 access_token。
    session().setToken(tokens.access_token);
    setBootstrapError(null);
    setStatus('unknown');
    const me = await getCurrentUser(api());
    setUser(me);
    setStatus('authenticated');
  }, []);

  const signOut = useCallback(async (): Promise<void> => {
    try {
      // 尽力而为地撤销服务端会话（`Logout` 只撤这一条，不动其他设备）。
      // 失败也要往下走：本地不清掉的话，用户点了「登出」却还是登录状态，
      // 这在共用电脑上是真实的泄漏。
      await unwrapEmpty(api().POST('/api/v1/auth/logout'));
    } catch {
      /* 见上。 */
    }
    session().signOut('user');
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({ status, user, bootstrapError, signIn, signOut, reload }),
    [status, user, bootstrapError, signIn, signOut, reload],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

function asApiError(cause: unknown, fallbackMessage: string): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({ status: 0, code: 'UNKNOWN', message: fallbackMessage, cause });
}

/**
 * 路由守卫。用作 layout route 的 element（内部渲染 `<Outlet />`），
 * 也可以直接包一个子树。
 */
export function RequireAuth({ children }: { children?: ReactNode }) {
  const { status, bootstrapError, reload } = useAuth();
  const location = useLocation();

  if (status === 'unknown') {
    if (bootstrapError) {
      return (
        <div className="mx-auto w-full max-w-2xl px-3 py-8">
          <ErrorState
            kind={bootstrapError.kind}
            title="没能确认你的登录状态"
            description="这不代表你已经登出。可能是网络到面板的连接出了问题，重试一次通常就好。"
            requestId={bootstrapError.requestId}
            onRetry={() => void reload()}
          />
        </div>
      );
    }
    return <AuthPending />;
  }

  if (status === 'anonymous') {
    // returnTo 取自 router 的 location 而不是 `window.location`：
    // 前者是这次导航真正要去的地址，后者在跳转过程中可能还停在上一页。
    const from = `${location.pathname}${location.search}${location.hash}`;
    return <Navigate to={loginUrlWithReturnTo(LOGIN_PATH, from)} replace />;
  }

  return <>{children ?? <Outlet />}</>;
}

/**
 * 「正在确认登录态」的占位。
 *
 * 用骨架而不是 spinner，理由与 §2.2 一致；带 `role="status"` + `aria-busy`
 * 是因为这一屏可能停留数秒（跨境往返），屏幕阅读器用户需要知道它不是卡死。
 */
function AuthPending() {
  return (
    <div className="mx-auto w-full max-w-2xl px-3 py-8" role="status" aria-busy="true">
      <span className="sr-only">正在确认登录状态</span>
      <Card>
        <SkeletonText lines={4} />
      </Card>
    </div>
  );
}
