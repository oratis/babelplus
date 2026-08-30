/**
 * `/admin/login` —— 后台的**准入状态页**。page-inventory §4.1。
 *
 * 三道闸，缺一不可：
 *   闸 1 独立主域名（不做子域、不做路径混淆）
 *   闸 2 IP 白名单 / GCP IAP —— **在这个页面加载之前就已经拦过一次了**
 *   闸 3 强制 TOTP —— **不接受关闭**
 *
 * IAP 与 TOTP 是两套独立凭据，任一泄漏不足以进入。所以这一页上
 * **不能有「跳过两步验证」「记住这台设备 30 天」之类的分支** —— 那等于把闸 3 拆了。
 *
 * ─────────────────────────────────────────────────────────────────────────
 * 🔴 **这一页没有可用的密码框，而且这不是「还没接线」。**
 *
 * 接线时逐条核对了服务端，结论是**管理面根本没有登录端点**：
 *
 *  1. `api/cmd/server/authmap.go` 的 `adminOperations` 分支只做一件事：
 *     `mw.AuthenticateAdmin(ctx, adminCfg, r)`。
 *  2. 那个函数（`api/internal/middleware/admin.go`）**从不读 `Authorization` 头**：
 *     它验 `x-goog-iap-jwt-assertion` 的签名，再拿断言里的 email 去查 `admin_users`。
 *  3. 它用的 `AdminRecord` **刻意不含 `password_hash`**，注释写着
 *     「管理面走 IAP，根本不该有任何代码路径能读到密码哈希」。
 *  4. 契约里 61 个 `/api/v1/admin/*` 端点里，**没有任何一个是 login / session / me**。
 *  5. 闸 3 的 TOTP 不是登录时的第二因子，而是**每个危险操作**上的
 *     `X-TOTP-Code` step-up（api-contract §6.2 L3）—— 它的输入框在
 *     `components/DangerousAction.tsx` 里，不在这一页。
 *
 * 那把这三个框接到 `login`（这一页原本的注释是这么写的）行不行？**不行，而且是错的**：
 * `login` 在 openapi 里是 `tags: [user]` / `security: []`，handler 查的是 **`users` 表**
 * （`auth.go` 的 `GetUserByEmail`），发的是一枚**用户面**的 access token。
 * 管理面既不读那枚 token，管理员的账号也不在那张表里。
 * 接上去的结果是：点「登录」→ 提示成功 → 后台每一页照样 403。
 * 那正是本仓最不接受的一种界面 —— **看起来能用的**。
 *
 * 所以这一页保留三个框但**禁用**，并把上面这段原因写在框旁边。
 * 「你没有这个权限」「这个功能还没做」「这个功能按设计就不存在」是三件不同的事，
 * 显示成同一个灰按钮等于让运维去猜。
 *
 * 契约里的 `adminSession`（bearer JWT）scheme 目前**没有对应实现**。
 * TODO(P1)：它落地时（口令必须校验 `admin_users.password_hash`，不是 `users`），
 *           把下面的表单启用，登录成功后仍然由 `lib/auth.tsx` 的探测决定准入。
 * ─────────────────────────────────────────────────────────────────────────
 */
import { useState, type ReactNode } from 'react';
import { Navigate, useLocation } from 'react-router';
import { RETURN_TO_PARAM, safeReturnTo } from '@babelplus/shared';
import { Button, Card, Skeleton } from './_imports.ts';
import { ADMIN_LOGIN_PATH } from '../lib/api.ts';
import { AdmissionNotice, useAdminAuth } from '../lib/auth.tsx';

/** 准入通过后的默认落点。 */
const ADMIN_HOME = '/admin';

export default function LoginPage() {
  const { status, failure, probeError, recheck } = useAdminAuth();
  const { search } = useLocation();

  // 三个受控但禁用的框。留着状态是为了将来 `adminSession` 落地时只用去掉 `disabled` ——
  // 也为了现在就能看出「它们确实什么都不做」。
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [totp, setTotp] = useState('');

  if (status === 'admitted') {
    // 已经准入了还停在这一页没有意义：守卫会放行任何后台页面。
    return <Navigate to={returnTarget(search)} replace />;
  }

  return (
    <div className="flex min-h-dvh items-start justify-center bg-bg px-4 py-10">
      <div className="w-full max-w-2xl space-y-4">
        <p className="text-center text-base font-semibold tracking-tight text-fg">babel.plus 后台</p>

        {/* 这一页的主体是「服务端认不认你」，不是一个表单。
            `failure` 非空同时覆盖两种情形：被明确拒绝（denied），
            以及 `status = 0` 那个说不清是网络还是 IAP 跨域被拦的态（仍是 unknown）。
            两者的判别与文案都由 `classifyAdminAuthFailure` 一处产生，这里不再分叉。 */}
        {failure ? (
          <AdmissionNotice failure={failure} onRecheck={() => void recheck()} />
        ) : probeError ? (
          <Card className="border-l-4 border-l-warn">
            <h1 className="text-base font-semibold text-fg">没能确认你的后台准入状态</h1>
            <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
              这不代表你被拒绝了 —— 请求发出去了，但没拿到一个能判断的答复
              （{probeError.message}）。
            </p>
            <div className="mt-4">
              <Button tone="primary" onClick={() => void recheck()}>
                重新检查
              </Button>
            </div>
            {probeError.requestId ? (
              <p className="mt-3 font-mono text-xs text-fg-subtle">请求号 {probeError.requestId}</p>
            ) : null}
          </Card>
        ) : (
          <Card>
            <div role="status" aria-busy="true" className="space-y-2">
              <span className="sr-only">正在确认后台准入状态</span>
              <Skeleton className="h-3.5 w-full" />
              <Skeleton className="h-3.5 w-full" />
              <Skeleton className="h-3.5 w-2/5" />
            </div>
          </Card>
        )}

        <Card>
          <h2 className="text-lg font-semibold text-fg">管理员登录</h2>
          <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
            后台的准入完全由 <strong className="font-medium text-fg">IAP（你的 Google 身份）</strong>{' '}
            决定：请求到达我们的服务时，身份已经在网络层证明过了。
          </p>

          {/* 🔴 禁用而不隐藏，并把原因说全。见文件头。 */}
          <div className="mt-3 rounded-lg border border-dashed border-line bg-surface-alt/60 p-3 text-xs leading-relaxed text-fg-muted">
            <p className="font-medium text-fg">下面三个框是禁用的，而且不是「还没接线」。</p>
            <p className="mt-1">
              服务端没有管理员密码登录这条路：
              <code className="font-mono text-fg"> AuthenticateAdmin </code>
              只验 IAP 断言再查 <code className="font-mono text-fg">admin_users</code>，
              它读的那份记录<strong className="font-medium text-fg">刻意不含密码哈希</strong>；
              61 个 <code className="font-mono text-fg">/admin/*</code> 端点里也没有 login / session / me。
            </p>
            <p className="mt-1">
              闸 3 的 TOTP 也不在这里 —— 它是<strong className="font-medium text-fg">每个危险操作</strong>
              上的 <code className="font-mono text-fg">X-TOTP-Code</code>（契约 §6.2 L3），
              输入框在危险操作的确认面板里。
            </p>
            <p className="mt-1">
              把这三个框接到用户面的 <code className="font-mono text-fg">login</code> 是错的：
              那个端点查的是 <code className="font-mono text-fg">users</code> 表、发的是一枚管理面根本不读的 token，
              结果会是「提示登录成功，然后每一页照样 403」。
            </p>
          </div>

          <div className="mt-5 space-y-4">
            <Labeled label="邮箱">
              <input
                type="email"
                name="email"
                autoComplete="username"
                value={email}
                disabled
                onChange={(event) => setEmail(event.target.value)}
                className={INPUT}
              />
            </Labeled>
            <Labeled label="密码">
              <input
                type="password"
                name="password"
                autoComplete="current-password"
                value={password}
                disabled
                onChange={(event) => setPassword(event.target.value)}
                className={INPUT}
              />
            </Labeled>
            <Labeled label="验证器 6 位码">
              <input
                type="text"
                name="totp"
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={6}
                value={totp}
                disabled
                onChange={(event) => setTotp(event.target.value)}
                className={`${INPUT} font-mono tracking-[0.4em]`}
              />
            </Labeled>

            <Button tone="primary" className="w-full" disabled>
              登录（管理面没有这条路）
            </Button>
          </div>
        </Card>

        {/* 🔴 这段警告是给运维自己看的，必须留在页面上。 */}
        <div className="rounded-xl border border-danger/30 bg-danger/10 p-3 text-xs leading-relaxed text-danger">
          <p className="font-medium">IAP 有一个自我引用的失效模式。</p>
          <p className="mt-1">
            IAP 要求 Google 身份，而 google.com 在中国大陆自 2014 年起被完全封锁。
            <strong className="font-semibold"> 服务出故障时，身处大陆的运维自己也进不了后台。</strong>
            必须准备一条不依赖本服务的备用出网路径，并定期演练。这不是可选项。
          </p>
        </div>
      </div>
    </div>
  );
}

/**
 * 准入通过后回哪去。
 *
 * `safeReturnTo` 已经挡掉了开放重定向（只接受站内路径）。这里再多挡一条：
 * **不接受回到这一页自己** —— 否则 `?returnTo=/admin/login` 会让准入通过的人
 * 在这一页与自己之间来回跳一次，看起来像闪烁。
 */
function returnTarget(search: string): string {
  const raw = new URLSearchParams(search).get(RETURN_TO_PARAM);
  const safe = safeReturnTo(raw);
  if (safe === null) return ADMIN_HOME;
  if (safe === ADMIN_LOGIN_PATH || safe.startsWith(`${ADMIN_LOGIN_PATH}?`)) return ADMIN_HOME;
  return safe;
}

const INPUT =
  'min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg ' +
  'focus-visible:border-accent focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent ' +
  'disabled:cursor-not-allowed disabled:opacity-50';

function Labeled({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1.5 block text-sm font-medium text-fg">{label}</span>
      {children}
    </label>
  );
}
