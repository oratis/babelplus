/**
 * `/profile` —— P1，但**大幅瘦身**。page-inventory §3.1 #12、§3.2.9。
 * 只剩三块：改密码、通知开关、Telegram 绑定（P3）。
 * 钱包搬去 `/wallet`，重置订阅搬去 `/subscribe`。
 *
 * 🔴 两条不能省的：
 *  1. **Telegram 绑定必须写明大陆不可达。** OONI 实测 `api.telegram.org` 异常 48 / 正常 0，
 *     Telegram 整体 anomaly 12,215 / ok 253 ≈ 98%。
 *     **不能让用户误以为绑了就能收到通知。**
 *  2. **「服务不可用」类通知不受开关控制**（user-journey §1 裁决 4）——
 *     邮件是唯一失联恢复通道，生命线不能被用户关掉。开关旁边要写清楚这一点。
 *     契约把这条做成了类型约束：`NotificationPrefsUpdate` 里**根本没有** `service_broadcast`，
 *     而 `NotificationPrefs` 里它是个 `LockedBoolean`（`value` 给渲染、`locked` 与 `reason`
 *     给解释为什么不能关）。前端只负责把 `reason` 显示出来，不自己编理由。
 *
 * 🔴 **改密码的错误分支是这一页最容易被改错的地方。**
 * 后端在**原密码不正确**时返回的是 **401 + `AUTH_INVALID_CREDENTIALS`**
 * （`handler/auth.go` 的 `ChangePassword`），不是 422。按 HTTP 状态码分支的写法
 * 会把它显示成「登录已过期，请重新登录」—— 而用户的登录状态好得很，
 * 他会去重新登录一次，回来再输错一次，然后开工单。ProfilePage.test.tsx 钉死了这一条。
 * （`lib/api.ts` 的 `handleAuthFailure` 已经为这个 code 留了 early return，会话不会被清掉。）
 *
 * 三块内容**各自持有自己的状态**：改密码表单失败不该让通知开关消失，
 * 通知偏好读不出来也不该挡住改密码（怀疑账号被盗的人第一件事就是改密码）。
 *
 * `getCurrentUser` 这一页**不自己发**：`AuthProvider` 启动时已经取过 `/api/v1/user/me`。
 * 这里重复发一次只会在最慢的链路上多一次往返。
 */
import { useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { Badge, Button, Card, CardTitle, Icon, cx, formatDate } from './_imports.ts';
import { unwrap, unwrapEmpty, type ApiError, type components } from '@babelplus/shared/api';
import { useAuth } from '../lib/auth.tsx';
import { api } from '../lib/api.ts';
import {
  asApiError,
  useApiQuery,
  useRetryCountdown,
  type ApiQuery,
} from './ticket-common.tsx';
import {
  PendingFeatureNotice,
  QuerySection,
  WriteError,
  WriteOk,
  commonWriteErrorCopy,
  fallbackWriteErrorCopy,
  isNotImplemented,
  type NotificationPrefs,
} from './account-common.tsx';

type NotificationPrefsUpdate = components['schemas']['NotificationPrefsUpdate'];

/** 契约没写长度上限，但后端 `validPassword` 是 8–128，前端照同一个区间做即时校验。 */
const PASSWORD_MIN = 8;
const PASSWORD_MAX = 128;

const loadPrefs = (): Promise<NotificationPrefs> =>
  unwrap(api().GET('/api/v1/user/notification-prefs'));

export default function ProfilePage() {
  const prefs = useApiQuery(loadPrefs, [], '通知偏好加载失败');

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">账号</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">
          密码、通知、绑定。其余的搬到了各自的页面。
        </p>
      </header>

      <div className="space-y-4">
        <IdentityCard />
        <ChangePasswordCard />
        <NotificationCard prefs={prefs} />
        <TelegramCard />
        <TwoFactorEntryCard />
      </div>
    </>
  );
}

/* ─────────────────────────── 身份（无额外请求） ─────────────────────────── */

/**
 * 邮箱与注册时间。数据来自 `AuthProvider` 已经拿到的那一份 `CurrentUser`。
 *
 * 没有「读不到账号信息」的空态：能渲染到这一页就说明守卫已经确认过登录态，
 * `user` 必然非空。真为它写一个空态，等于给一个不可达的分支写文案。
 */
function IdentityCard() {
  const { user } = useAuth();
  if (!user) return null;

  return (
    <Card>
      <CardTitle hint="来自登录时已取到的账号信息，这一页不重复请求">账号</CardTitle>
      <dl className="space-y-2.5 text-sm">
        <div className="flex flex-wrap items-baseline justify-between gap-x-3">
          <dt className="text-fg-muted">邮箱</dt>
          <dd className="font-mono text-fg">{user.email}</dd>
        </div>
        <div className="flex flex-wrap items-baseline justify-between gap-x-3">
          <dt className="text-fg-muted">注册时间</dt>
          <dd className="text-fg">{formatDate(user.created_at)}</dd>
        </div>
      </dl>
      {/* 改邮箱契约里没有端点（api-contract 的用户面没有这条），所以这里不放按钮 ——
          放一个点了没反应的按钮比不放更糟。走工单的「账号本身」分类。
          放在 <dl> **外面**：dl 只接受 dt / dd / div，塞个 <p> 进去是非法 HTML。 */}
      <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
        需要更换邮箱请提工单（分类选「账号本身」）—— 换绑涉及订阅归属，我们人工核对后再改。
      </p>
    </Card>
  );
}

/* ─────────────────────────── 改密码 ─────────────────────────── */

type PasswordFormState = 'idle' | 'pending' | 'done';

function ChangePasswordCard() {
  const [oldPassword, setOldPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  // 🔴 二次确认。改密码会**把其余会话全部踢掉**（契约 204 的描述逐字：「已修改，其余会话失效」），
  // 而这件事必须**提交前**说，不能让用户在别的设备上被登出之后才发现。
  // 做成必须勾选而不是一句提示：提示会被略过，勾选不会。
  const [acknowledged, setAcknowledged] = useState(false);
  const [formState, setFormState] = useState<PasswordFormState>('idle');
  const [error, setError] = useState<ApiError | null>(null);
  const countdown = useRetryCountdown();

  // 本地校验**先于**请求：长度与两次输入是否一致，服务端也会查，
  // 但为此跑一趟跨境往返只为了拿回一句「两次输入不一致」是浪费用户的时间。
  const tooShort = newPassword.length > 0 && newPassword.length < PASSWORD_MIN;
  const tooLong = newPassword.length > PASSWORD_MAX;
  const mismatch = confirmPassword.length > 0 && confirmPassword !== newPassword;
  const sameAsOld = newPassword.length > 0 && newPassword === oldPassword;
  const localError = tooShort
    ? `新密码至少 ${PASSWORD_MIN} 位`
    : tooLong
      ? `新密码最多 ${PASSWORD_MAX} 位`
      : mismatch
        ? '两次输入的新密码不一致'
        : sameAsOld
          ? '新密码和原密码一样，等于没改'
          : null;

  const ready =
    oldPassword.length > 0 &&
    newPassword.length >= PASSWORD_MIN &&
    newPassword.length <= PASSWORD_MAX &&
    confirmPassword === newPassword &&
    !sameAsOld &&
    acknowledged;

  async function onSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    // 单飞：**这就是这个写端点当前全部的「幂等」**。api-contract §9.1 的幂等总表里
    // 没有 `PUT /api/v1/user/password`，服务端不认 `Idempotency-Key`
    // （生成类型里这个 operation 的 `header` 是 `never`，硬发一个过去只会让代码
    // 看起来比实际更安全）。所以这里老老实实只挡住重复点击。
    if (formState === 'pending' || countdown.seconds !== null) return;
    if (!ready) return;

    setFormState('pending');
    setError(null);
    try {
      await unwrapEmpty(
        api().PUT('/api/v1/user/password', {
          body: { old_password: oldPassword, new_password: newPassword },
        }),
      );
      // 成功后立刻清空三个框：密码明文在 DOM 里多留一秒是多一秒的肩窥风险。
      setOldPassword('');
      setNewPassword('');
      setConfirmPassword('');
      setAcknowledged(false);
      setFormState('done');
    } catch (cause) {
      const apiError = asApiError(cause, '修改密码失败');
      setError(apiError);
      countdown.start(apiError.retryAfterSeconds);
      setFormState('idle');
      // 输入框**不清空**（除了成功那一支）：原密码打错一个字符就得三个框重打，是最招骂的实现。
    }
  }

  if (isNotImplemented(error)) {
    return (
      <Card>
        <CardTitle>改密码</CardTitle>
        <PendingFeatureNotice what="改密码" requestId={error?.requestId} />
      </Card>
    );
  }

  const copy = error ? passwordErrorCopy(error, countdown.seconds) : null;

  return (
    <Card>
      <CardTitle hint="需要原密码">改密码</CardTitle>

      <form className="space-y-4" onSubmit={(event) => void onSubmit(event)}>
        <PasswordField
          label="原密码"
          name="old-password"
          autoComplete="current-password"
          value={oldPassword}
          onChange={setOldPassword}
          disabled={formState === 'pending'}
        />
        <PasswordField
          label="新密码"
          name="new-password"
          autoComplete="new-password"
          value={newPassword}
          onChange={setNewPassword}
          disabled={formState === 'pending'}
          hint={`${PASSWORD_MIN}–${PASSWORD_MAX} 位。我们不强制大小写和符号 —— 长度比字符种类更管用。`}
        />
        <PasswordField
          label="确认新密码"
          name="confirm-password"
          autoComplete="new-password"
          value={confirmPassword}
          onChange={setConfirmPassword}
          disabled={formState === 'pending'}
        />

        {/* 🔴 提交前的明示 + 二次确认。 */}
        <label className="flex cursor-pointer items-start gap-2.5 rounded-lg border border-line bg-surface-alt p-3">
          <input
            type="checkbox"
            className="mt-0.5 size-4 shrink-0 accent-[var(--color-accent,currentColor)]"
            checked={acknowledged}
            disabled={formState === 'pending'}
            onChange={(event) => setAcknowledged(event.target.checked)}
          />
          <span className="text-sm leading-relaxed text-fg-muted">
            我知道<strong className="font-medium text-fg">改完之后其它设备会全部退出登录</strong>
            ，需要用新密码重新登录一次。当前这台设备不受影响。
          </span>
        </label>

        {localError ? <WriteError title={localError} description="" /> : null}
        {copy ? <WriteError title={copy.title} description={copy.description} /> : null}
        {formState === 'done' ? (
          <WriteOk>
            密码已修改。其它设备已被登出，下次在那些设备上要用新密码登录；这台设备不用重新登录。
          </WriteOk>
        ) : null}

        <div className="flex flex-wrap items-center gap-3">
          <Button
            tone="primary"
            type="submit"
            disabled={!ready || formState === 'pending' || countdown.seconds !== null}
          >
            {formState === 'pending' ? '正在修改…' : '修改密码'}
          </Button>
          {countdown.seconds !== null ? (
            <span className="text-sm text-fg-muted">{countdown.seconds} 秒后可再试</span>
          ) : null}
        </div>
      </form>
    </Card>
  );
}

/**
 * 改密码的 `ErrorCode` → 文案。**这一页唯一按 code 分支的地方。**
 *
 * 🔴 `AUTH_INVALID_CREDENTIALS` 这一支是整个文件存在感最强的三行：
 * 它来自 **401**，而 401 归一成 `kind = 'unauthorized'`，
 * 落到兜底分支就会说「登录状态已过期，重新登录后回到这一页继续」。
 * 那是假的 —— 用户的会话好好的，只是原密码打错了。
 */
function passwordErrorCopy(
  error: ApiError,
  retrySeconds: number | null,
): { title: string; description: string } {
  if (error.code === 'AUTH_INVALID_CREDENTIALS') {
    return {
      title: '原密码不正确',
      description: '你的登录状态没有问题，只是这一栏填错了。忘了原密码可以用「忘记密码」重置。',
    };
  }
  const shared = commonWriteErrorCopy(error, { retrySeconds });
  if (shared) return shared;
  return fallbackWriteErrorCopy(error, '密码没能修改');
}

/**
 * 密码输入框。**不复用 `ticket-common.tsx` 的 `TextField`** —— 那个是 `type="text"`，
 * 而密码框还需要 `autoComplete` 才能让密码管理器正确识别「原 / 新」两栏
 * （少了它，1Password 之类会把新密码填进原密码栏）。
 * `text-base`（16px）起步的理由同 `TextField`：iOS Safari 在 <16px 的输入框获得焦点时
 * 会自动放大页面，放大后 375px 布局就出现横向滚动。
 */
function PasswordField({
  label,
  name,
  value,
  onChange,
  hint,
  autoComplete,
  disabled,
}: {
  label: string;
  name: string;
  value: string;
  onChange: (value: string) => void;
  hint?: string;
  autoComplete: 'current-password' | 'new-password';
  disabled?: boolean;
}) {
  const id = `pf-${name}`;
  return (
    <div>
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium text-fg">
        {label}
      </label>
      <input
        id={id}
        name={name}
        type="password"
        autoComplete={autoComplete}
        value={value}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
        className={cx(
          'min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg',
          'placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-2',
          'focus-visible:outline-offset-1 focus-visible:outline-accent disabled:opacity-50',
        )}
      />
      {hint ? <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </div>
  );
}

/* ─────────────────────────── 通知开关 ─────────────────────────── */

function NotificationCard({ prefs }: { prefs: ApiQuery<NotificationPrefs> }) {
  return (
    <Card>
      <CardTitle hint="每个开关单独保存">通知</CardTitle>
      <QuerySection query={prefs} what="通知偏好">
        {(data) => <NotificationSwitches prefs={prefs} data={data} />}
      </QuerySection>
    </Card>
  );
}

type PrefKey = 'expire_remind' | 'traffic_remind';

function NotificationSwitches({
  prefs,
  data,
}: {
  prefs: ApiQuery<NotificationPrefs>;
  data: NotificationPrefs;
}) {
  // 「哪一个开关正在保存」而不是一个全局 boolean：两个开关各自独立，
  // 一个在保存时不该把另一个也变灰。
  const [saving, setSaving] = useState<PrefKey | null>(null);
  const [error, setError] = useState<ApiError | null>(null);

  async function toggle(key: PrefKey, next: boolean): Promise<void> {
    if (saving !== null) return;
    setSaving(key);
    setError(null);
    try {
      // **只发改动的那一个字段。** SQL 那边是 `coalesce(narg, 当前值)`
      // （`db/queries/account.sql`），所以省略的字段保持不变 ——
      // 把两个字段一起发过去的话，另一个标签页刚改过的那一个会被这次请求悄悄改回去。
      //
      // ⚠️ **不做乐观更新。** 先把开关拨过去再等响应，失败时要拨回来，
      // 而「拨回来」这个动画在慢链路上看起来就像开关自己动了。
      // 这里的做法是：请求期间开关禁用，成功后用**服务端返回的后像**覆盖本地值。
      const body: NotificationPrefsUpdate =
        key === 'expire_remind' ? { expire_remind: next } : { traffic_remind: next };
      const updated = await unwrap(api().PUT('/api/v1/user/notification-prefs', { body }));
      prefs.patch(() => updated);
    } catch (cause) {
      setError(asApiError(cause, '保存失败'));
    } finally {
      setSaving(null);
    }
  }

  const copy = error ? notificationErrorCopy(error) : null;

  return (
    <div className="space-y-3">
      <ToggleRow
        label="到期提醒"
        description="订阅快到期时发一封邮件。"
        checked={data.expire_remind}
        busy={saving === 'expire_remind'}
        disabled={saving !== null}
        onChange={(next) => void toggle('expire_remind', next)}
      />
      <ToggleRow
        label="流量提醒"
        description="流量用到 80% 和用完时各发一封。"
        checked={data.traffic_remind}
        busy={saving === 'traffic_remind'}
        disabled={saving !== null}
        onChange={(next) => void toggle('traffic_remind', next)}
      />
      {/* 🔴 生命线。`locked = true` 时渲染成**不可操作**且给出服务端的 `reason`。
          做成「前端隐藏的开关」是不够的 —— 契约在类型上就不给它写入口
          （`NotificationPrefsUpdate` 里没有这个字段），这里只是把那条裁决显示出来。 */}
      <LockedRow
        label="服务通告"
        value={data.service_broadcast.value}
        locked={data.service_broadcast.locked}
        reason={data.service_broadcast.reason}
      />

      {copy ? <WriteError title={copy.title} description={copy.description} /> : null}

      <p className="rounded-lg border border-line bg-surface-alt p-3 text-sm leading-relaxed text-fg-muted">
        服务不可用、域名变更这类通知<strong className="font-medium text-fg">不受这些开关控制</strong>。
        邮件是我们和你之间唯一还能用的通道，这条生命线不提供关闭选项。
      </p>
    </div>
  );
}

/**
 * 通知开关的 `ErrorCode` → 文案。**与改密码那张表分开写，不合并。**
 * 两者说的不是一件事：改密码失败要区分「原密码错」与「会话过期」，
 * 开关保存失败只需要说「这次没存上，开关还是原来的值」。
 */
function notificationErrorCopy(error: ApiError): { title: string; description: string } {
  if (isNotImplemented(error)) {
    return {
      title: '该功能尚未开放',
      description: '通知偏好的保存接口还没上线。开关的显示值是服务端当前的真实值，没有被改动。',
    };
  }
  const shared = commonWriteErrorCopy(error);
  if (shared) return shared;
  const fallback = fallbackWriteErrorCopy(error, '这个开关没能保存');
  return { title: fallback.title, description: `${fallback.description} 开关仍是原来的值。` };
}

function ToggleRow({
  label,
  description,
  checked,
  busy,
  disabled,
  onChange,
}: {
  label: string;
  description: string;
  checked: boolean;
  busy: boolean;
  disabled: boolean;
  onChange: (next: boolean) => void;
}) {
  return (
    <label className="flex cursor-pointer items-start justify-between gap-3 rounded-lg border border-line p-3">
      <span className="min-w-0">
        <span className="block text-sm font-medium text-fg">{label}</span>
        <span className="mt-0.5 block text-xs leading-relaxed text-fg-muted">{description}</span>
      </span>
      <span className="flex shrink-0 items-center gap-2">
        {busy ? <span className="text-xs text-fg-subtle">保存中…</span> : null}
        {/* 原生 checkbox：屏幕阅读器与键盘操作免费得到正确行为，
            自绘一个 `role="switch"` 的 div 需要自己补 5 件事，而其中一件必然会漏。 */}
        <input
          type="checkbox"
          className="size-5 accent-[var(--color-accent,currentColor)]"
          checked={checked}
          disabled={disabled}
          onChange={(event) => onChange(event.target.checked)}
        />
      </span>
    </label>
  );
}

function LockedRow({
  label,
  value,
  locked,
  reason,
}: {
  label: string;
  value: boolean;
  locked: boolean;
  reason: string;
}) {
  return (
    <div className="flex items-start justify-between gap-3 rounded-lg border border-line bg-surface-alt p-3">
      <div className="min-w-0">
        <p className="flex flex-wrap items-center gap-2 text-sm font-medium text-fg">
          {label}
          {locked ? <Badge tone="info">不可关闭</Badge> : null}
        </p>
        <p className="mt-0.5 text-xs leading-relaxed text-fg-muted">{reason}</p>
      </div>
      {/* 用 `disabled` 而不是把开关藏起来：用户需要看到它是**开着**的，
          否则「我没开这个，为什么给我发邮件」是下一张工单。 */}
      <input
        type="checkbox"
        className="mt-0.5 size-5 shrink-0 accent-[var(--color-accent,currentColor)]"
        checked={value}
        disabled
        readOnly
        aria-label={`${label}（不可关闭）`}
      />
    </div>
  );
}

/* ─────────────────────────── Telegram（P3） ─────────────────────────── */

/**
 * 🔴 这段警告不是可选的。契约里也没有任何 Telegram 绑定端点（P3 还没设计），
 * 所以按钮是**真的**不可用，不是样式上的禁用。
 */
function TelegramCard() {
  return (
    <Card>
      <CardTitle hint="P3">Telegram 绑定</CardTitle>
      <div className="rounded-lg border border-danger/30 bg-danger/10 p-3 text-sm leading-relaxed text-danger">
        <p className="font-medium">Telegram 在中国大陆基本不可用。</p>
        <p className="mt-1">
          实测数据里 Telegram 的异常率约 98%。绑定了也大概率收不到通知 ——
          如果你人在大陆，请把邮箱当成唯一的通知渠道。
        </p>
      </div>
      <Button className="mt-3" disabled title="契约里还没有绑定端点">
        绑定 Telegram（P3）
      </Button>
    </Card>
  );
}

/* ─────────────────────────── 两步验证入口 ─────────────────────────── */

function TwoFactorEntryCard() {
  const { user } = useAuth();
  // `totp_enabled` 是 CurrentUser 上的可选字段。**没有这个字段 ≠ 没开启** ——
  // 它可能只是这一版后端没返。所以缺失时不说「未开启」，只说「去看看」。
  const enabled = user?.totp_enabled;

  return (
    <Card>
      <CardTitle
        hint={
          enabled === undefined ? (
            'P3'
          ) : enabled ? (
            <Badge tone="ok">已开启</Badge>
          ) : (
            <Badge tone="neutral">未开启</Badge>
          )
        }
      >
        两步验证
      </CardTitle>
      <p className="text-sm leading-relaxed text-fg-muted">
        用户侧 TOTP 是可选项（管理员侧是强制的）。
      </p>
      <Link
        to="/profile/2fa"
        className="mt-3 inline-flex items-center gap-1 text-sm text-accent hover:underline"
      >
        去设置 <Icon.ArrowRight size={14} />
      </Link>
    </Card>
  );
}
