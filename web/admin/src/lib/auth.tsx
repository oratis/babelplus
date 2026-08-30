/**
 * 后台的**准入**上下文 + 路由守卫。
 *
 * 这个文件刻意叫「准入（admission）」而不是「会话（session）」。用户面板那套
 * 「有没有 token」在这里根本不成立 —— 管理面的凭据形态与用户面**完全不同**：
 *
 * | | 用户面板 | 管理面 |
 * |---|---|---|
 * | 凭据在哪 | 我们自己发的 access token，存在 `sessionStorage` | **浏览器里的 Google/IAP cookie，前端读不到也管不了** |
 * | 谁校验 | `middleware/user.go`，读 `Authorization: Bearer` | `middleware/admin.go`，读 `x-goog-iap-jwt-assertion` |
 * | 前端能自查吗 | 能：`getToken() === null` 就是未登录 | **不能**：前端手上一个字节的凭据都没有 |
 * | TOTP 在哪 | 账号设置里的可选项 | **每个危险操作**的 `X-TOTP-Code`（§6.2 L3），不是登录的第二因子 |
 *
 * 🔴 **所以后台不存在「登录」这个动作，也就没有本地登录态可读。**
 * `api/internal/middleware/admin.go` 的 `AuthenticateAdmin` 只做两件事：
 * 验 IAP 断言的签名 → 拿断言里的 email 查 `admin_users`。它**从不读 Authorization 头**；
 * `AdminRecord` 甚至刻意不含 `password_hash`（「管理面走 IAP，根本不该有任何代码路径能读到密码哈希」）。
 * 详见 `routes/LoginPage.tsx` 里那段说明。
 *
 * 于是前端唯一诚实的做法是**探测**：发一个最便宜的管理面 GET，看服务端认不认我们。
 * 这带来一个必须接受的代价 —— 每次打开后台都要等一次往返才知道能不能进，
 * 中间只能显示骨架。**不缓存这个结论**（不写 sessionStorage）：
 * 一个被缓存的「你是管理员」结论，在管理员刚被禁用的那一刻正好是错的，
 * 而那一刻恰恰是唯一要紧的时刻。
 *
 * # 三态，不是布尔
 *
 * 与用户面板 `web/user/src/lib/auth.tsx` 同一条理由，但边界不同：
 *
 *  - `unknown`   —— 探测还没回来 / 探测因**网络或 5xx** 失败。渲染骨架或可重试错误态，**绝不跳转**。
 *  - `admitted`  —— 探测成功。服务端认我们是管理员。
 *  - `denied`    —— 探测被**明确拒绝**（401/403）。渲染判别结果，动作按 `lib/iap.ts` 的表分流。
 *
 * `status = 0`（请求没走到服务端）**不算 denied**，留在 `unknown`：
 * 在后台它既可能是网络不通、也可能是 IAP 会话过期被跨域拦掉（`lib/iap.ts` 文件头那张表），
 * 前端分不出来。把它判成「被拒」等于替一次网络抖动做了一个安全结论。
 *
 * ⚠️ 已知的一处冗余：探测被拒时，同一句判定会同时出现在守卫的说明块与常驻的
 * `<AuthFailureBanner />` 上（后者挂在 `<Routes>` 外面，任何请求的鉴权失败都归它管）。
 * **刻意不去抑制横幅**：抑制的实现只能是「守卫接管时把横幅清掉」，
 * 而那会连带清掉同一时刻由别的请求报上来的失败 —— 用一次少见的重复，
 * 换掉一次可能的漏报。（这条也影响测试写法，见 `App.routes.test.tsx` 里 findAll 的注释。）
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
import { ApiError, unwrap } from '@babelplus/shared/api';
import { loginUrlWithReturnTo } from '@babelplus/shared';
import { Button, Card, ErrorState, SkeletonText } from '@babelplus/shared/ui';
import { ADMIN_LOGIN_PATH, api } from './api.ts';
import {
  classifyAdminAuthFailure,
  reportAdminAuthFailure,
  type AdminAuthFailure,
} from './iap.ts';

export type AdminAdmissionStatus = 'unknown' | 'admitted' | 'denied';

export interface AdminAuthContextValue {
  readonly status: AdminAdmissionStatus;
  /**
   * 判别结果。`denied` 时必有；`unknown` 时**也可能有** ——
   * 那是 `status = 0` 的「说不清」态（`kind = 'ambiguous-network'`），
   * 它有专门的文案要说，但它不是一个「被拒」的结论。
   */
  readonly failure: AdminAuthFailure | null;
  /**
   * 探测失败（网络 / 5xx / 501）。只在 `status === 'unknown'` 时可能有值。
   * **它不表示被拒** —— 所以看到它的地方一律不许跳转、不许清任何东西。
   */
  readonly probeError: ApiError | null;
  /** 重新探测一次。错误态与「重新检查」按钮用。 */
  recheck(): Promise<void>;
}

const AdminAuthContext = createContext<AdminAuthContextValue | null>(null);

export function useAdminAuth(): AdminAuthContextValue {
  const value = useContext(AdminAuthContext);
  // 抛错而不是给一个「未准入」的默认值：忘了套 Provider 是装配错误，
  // 而默认值会把装配错误伪装成「IAP 把你挡住了」，于是运维去查 IAP 配置 —— 查错方向。
  if (!value) throw new Error('useAdminAuth 必须在 <AdminAuthProvider> 内使用');
  return value;
}

/**
 * 准入探测打的端点。
 *
 * 选审计日志而不是看板，三条理由：
 *  1. **便宜**：`limit=1` 是一次带索引的单行读；`GET /admin/dashboard` 是五个并发聚合查询
 *     （`admin_catalog.go` 的 `loadAdminDashboard`），而它会在每次打开后台时被白白跑一遍。
 *  2. **不挑角色**：审计模块按契约 §6.1 **只有 GET**，handler 里也没有角色判别，
 *     owner / admin / support 三个角色都读得到。拿一个 owner 专属端点（如 `listAdmins`）
 *     做探针，会把一个完全正常的 support 管理员判成「被拒」。
 *  3. **不挑权限位**：§6.2 L4 的两个权限位（D6 / D14）都不在这条路径上。
 *
 * ⚠️ 探针端点必须是**已实现**的。打到一个 501 的端点上，探测会永远落在
 * `unknown + probeError`，后台整体打不开 —— 而现象看起来像「后端挂了」。
 */
export const ADMIN_ADMISSION_PROBE_PATH = '/api/v1/admin/audit';

/** 探测一次。成功即返回，失败抛 `ApiError`。导出是为了让守卫之外的地方也能复用同一条判据。 */
export async function probeAdminAdmission(): Promise<void> {
  await unwrap(api().GET(ADMIN_ADMISSION_PROBE_PATH, { params: { query: { limit: 1 } } }));
}

export function AdminAuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<AdminAdmissionStatus>('unknown');
  const [failure, setFailure] = useState<AdminAuthFailure | null>(null);
  const [probeError, setProbeError] = useState<ApiError | null>(null);

  const recheck = useCallback(async (): Promise<void> => {
    setProbeError(null);
    // 已经准入过就不要打回骨架：重新检查通常发生在页面已经渲染出来之后，
    // 把整个后台闪回骨架屏会让人以为自己被登出了。
    setStatus((prev) => (prev === 'admitted' ? prev : 'unknown'));

    try {
      await probeAdminAdmission();
      // 🔴 一次成功的管理面请求**证明**了之前那条鉴权失败已经过期，横幅该撤。
      // 不撤的话，运维在 IAP 重新登录之后仍然看着一条「被平台层挡下了」的红条，
      // 于是继续折腾一个已经好了的问题。
      reportAdminAuthFailure(null);
      setFailure(null);
      setStatus('admitted');
    } catch (cause) {
      const error = asApiError(cause, '确认后台准入状态失败');

      // 只有服务端**明确说不**（401/403）才算 denied。
      if (error.status === 401 || error.status === 403) {
        setFailure(classifyAdminAuthFailure(error));
        setProbeError(null);
        setStatus('denied');
        return;
      }

      // `status = 0` 有专门的文案（可能是网络、也可能是 IAP 跨域被拦），但结论仍是「不知道」。
      setFailure(error.status === 0 ? classifyAdminAuthFailure(error) : null);
      setProbeError(error);
      setStatus('unknown');
    }
  }, []);

  useEffect(() => {
    void recheck();
  }, [recheck]);

  const value = useMemo<AdminAuthContextValue>(
    () => ({ status, failure, probeError, recheck }),
    [status, failure, probeError, recheck],
  );

  return <AdminAuthContext.Provider value={value}>{children}</AdminAuthContext.Provider>;
}

/** 任何 catch 到的东西 → `ApiError`。`status = 0` 会被归一成 `kind = 'offline'`。 */
export function asApiError(cause: unknown, fallbackMessage: string): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({ status: 0, code: 'UNKNOWN', message: fallbackMessage, cause });
}

/**
 * 路由守卫。用作 layout route 的 element（内部渲染 `<Outlet />`），也可以直接包一棵子树。
 *
 * 🔴 **除 `kind = 'app'` 外一律不跳 `/admin/login`。** 这是本文件最容易写错的一处：
 * 「被拒了就回登录页」在用户面板是对的，在后台是**有害**的 ——
 * `/admin/login` 自己也站在 IAP 后面，IAP 不放行时那一页同样打不开；
 * 而就算打得开，那一页上也没有任何能修复 IAP 会话的东西。
 * 把 IAP 拒绝跳成登录页的真实后果是：运维在一个帮不上忙的页面上反复重登，
 * 而这通常发生在服务已经出故障、时间最紧的时候。
 */
export function RequireAdmin({ children }: { children?: ReactNode }) {
  const { status, failure, probeError, recheck } = useAdminAuth();
  const location = useLocation();

  if (status === 'admitted') return <>{children ?? <Outlet />}</>;

  if (status === 'denied' && failure) {
    if (failure.signOutLocally) {
      // 只有应用层 401 会走到这里（`classifyAdminAuthFailure` 里唯一 signOutLocally=true 的分支）。
      // returnTo 取自 router 的 location 而不是 `window.location`：
      // 后者在跳转过程中可能还停在上一页。
      const from = `${location.pathname}${location.search}${location.hash}`;
      return <Navigate to={loginUrlWithReturnTo(ADMIN_LOGIN_PATH, from)} replace />;
    }
    return <GuardShell failure={failure} onRecheck={() => void recheck()} />;
  }

  // 以下都是 `unknown`：**不知道** ≠ 被拒，所以一步都不跳。
  if (failure) return <GuardShell failure={failure} onRecheck={() => void recheck()} />;
  if (probeError) {
    return (
      <div className="mx-auto w-full max-w-2xl px-3 py-8">
        <ErrorState
          kind={probeError.kind}
          title="没能确认你的后台准入状态"
          description="这不代表你被拒绝了。可能是网络到 API 的连接出了问题，或者服务端这一刻不可用。重试一次通常就好。"
          requestId={probeError.requestId}
          onRetry={() => void recheck()}
        />
      </div>
    );
  }
  return <AdmissionPending />;
}

/**
 * 「正在确认准入」的占位。
 *
 * 用骨架而不是 spinner（page-inventory §2.2）；带 `role="status"` + `aria-busy`
 * 是因为这一屏可能停留数秒（跨境往返 + IAP 一跳），屏幕阅读器用户需要知道它不是卡死。
 */
function AdmissionPending() {
  return (
    <div className="mx-auto w-full max-w-2xl px-3 py-8" role="status" aria-busy="true">
      <span className="sr-only">正在确认后台准入状态</span>
      <Card>
        <SkeletonText lines={4} />
      </Card>
    </div>
  );
}

/** 守卫里显示说明块时的页面外壳（登录页有自己的版心，所以外壳不在 `AdmissionNotice` 里）。 */
function GuardShell({ failure, onRecheck }: { failure: AdminAuthFailure; onRecheck: () => void }) {
  return (
    <div className="mx-auto w-full max-w-2xl px-3 py-8">
      <AdmissionNotice failure={failure} onRecheck={onRecheck} />
    </div>
  );
}

/**
 * 准入被拒 / 说不清时的整块说明。守卫与 `/admin/login` 共用同一块 ——
 * 两处各写一份的话，迟早有一处会说出与判别结果不一致的话。
 *
 * 导出是为了 `LoginPage` 能直接用它：那一页的全部作用就是把这块内容显示出来。
 */
export function AdmissionNotice({
  failure,
  onRecheck,
}: {
  failure: AdminAuthFailure;
  onRecheck: () => void;
}) {
  return (
    <Card className="border-l-4 border-l-danger">
      <h1 className="text-base font-semibold text-fg">{failure.title}</h1>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">{failure.description}</p>

      <div className="mt-4 flex flex-wrap gap-2">
        {/* 平台层的两种情形都要给「整页重载」这个动作，而且它是**真的有用**的那一个：
            XHR 被 IAP 拒绝时浏览器不会跳登录，而一次**文档级导航**到同一个域名
            会让 IAP 用 302 把你送去 Google 登录。这正是 fetch 做不到的事。 */}
        {failure.kind === 'edge' || failure.kind === 'ambiguous-network' ? (
          <Button tone="primary" onClick={() => window.location.reload()}>
            重新走 Google 登录（整页重载）
          </Button>
        ) : null}
        <Button onClick={onRecheck}>重新检查</Button>
      </div>

      {failure.kind === 'forbidden' ? (
        // 这句必须说出口。403 在这里的含义是「IAP 认了你的 Google 身份，
        // 但 admin_users 里没有你这一行（或你被禁用了）」——
        // 换个密码、重登一次、清缓存，一个都帮不上忙，而这三件事是人的默认反应。
        <p className="mt-3 rounded-lg border border-line bg-surface-alt px-3 py-2 text-xs leading-relaxed text-fg-muted">
          重新登录、换浏览器、清缓存都不会改变这个结果：你的 Google 身份已经通过了 IAP，
          是<strong className="font-medium text-fg"> 本系统 </strong>不认你是管理员。
          需要有人在 <code className="font-mono">admin_users</code> 里给你开一行（或解除禁用）。
        </p>
      ) : null}

      {/* 判定依据要露出来。判错的时候，这一行是唯一能追的线索。 */}
      <p className="mt-3 font-mono text-[11px] leading-relaxed text-fg-subtle">
        判定依据：{failure.evidence}
        {failure.requestId ? ` · 请求号 ${failure.requestId}` : ''}
      </p>
    </Card>
  );
}
