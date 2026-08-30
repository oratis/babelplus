/**
 * 危险操作的统一确认组件 —— api-contract §6.2「四层强制」的**前端一侧**。
 *
 * 🔴 **先把这个组件不做的事说清楚，因为它比它做的事更重要。**
 *
 * §6.2 那四层（L1 确认串 / L2 必填原因 / L3 TOTP step-up / L4 独立权限位）
 * **全部在服务端强制**。这个组件不校验任何东西，它只做一件事：
 * **把四层所需的参数收齐，交给服务端**。它里面所有的「按钮变灰」都只是省一次注定失败的往返，
 * 不是安全边界 —— 对一个直接 `curl` 的人，这里的每一行代码都等于零。
 *
 * 这一点必须写在代码里而不是只留在文档里，因为它决定了两件事的写法：
 *
 *  1. **前端不许「模拟」服务端的判断然后直接调 API。** 比如不许在前端比对完确认串
 *     就认为 L1 过了、不许因为「本地看着有权限」就跳过服务端的 403。
 *     每一次提交都必须把 `confirmation` / `reason` / `X-TOTP-Code` 原样发出去。
 *  2. **文案要说实话。** 确认串输入框的提示写的是「服务端会比对」，不是「请再确认一次」——
 *     后者会让人以为这是个可以被绕过的前端弹窗，从而低估这一步的意义。
 *
 * # 为什么是内联面板而不是模态对话框
 *
 * 与 `AuthFailureBanner` 同一条理由：模态盖住页面时，操作者就看不到自己刚才在看什么了，
 * 而「刚才在看什么」（这个用户的账单、这个节点的在线人数）正是判断这一步该不该做的主要依据。
 * D4 的登记表里写着「确认框内必须显示当前在线人数」—— 那个数字通过 `context` 传进来，
 * 就显示在按钮上方，与页面上的其他事实并排。
 *
 * # 与 `lib/danger.ts` 的分工
 *
 * `danger.ts` 是 page-inventory §4.4 那张表的逐字誊本（标题 / 危害 / 各项要求）。
 * 这里读它，但**不改它**。唯一的例外见下面 `DANGER_STEP_UP_CODES` 的注释：
 * 那张表里没有 L3（TOTP）这一列，而 §6.2 有。
 */
import { useCallback, useId, useState, type ReactNode } from 'react';
import { ApiError } from '@babelplus/shared/api';
import { Button, Card, cx } from '@babelplus/shared/ui';
import { DANGER, type DangerOp } from '../lib/danger.ts';

/* ────────────────────────── 四层的前端参数 ────────────────────────── */

/**
 * L2 的下限：**8 个码位**。
 *
 * 🔴 数的是**码位（rune）不是 `String.length`**。后端是 `utf8.RuneCountInString`
 * （`api/internal/handler/admin_users.go` 的 `validAdminReason`），
 * 而 JS 的 `String.length` 数的是 UTF-16 code unit：
 *
 *  - 「链上已确认到账」7 个字 → `length` 也是 7，两边一致；
 *  - 「补单✅确认无误」含一个 emoji → `length` 会多算 1（代理对），前端可能放行一条服务端要拒的原因；
 *  - 反过来更糟：前端按 `length` 判**不足**而挡住一条服务端本会接受的原因，
 *    操作者只会觉得「这个框坏了」。
 *
 * `[...s].length` 数的是码位，与 Go 的 rune 一一对应。
 */
export const MIN_REASON_RUNES = 8;

/**
 * L2 的上限。契约只给了 `minLength`，后端自己加了 2000
 * （`admin_catalog.go` 的 `catalogReasonMaxRunes`，理由是审计表 append-only、永不删除）。
 * 前端跟着它，免得写完两千字才被服务端退回来。
 *
 * ⚠️ 这个上限**只在 catalog 那一组端点上**被服务端强制，其余端点目前不判。
 * 前端统一按最严的来：宽松的那一侧出错时没人会注意到。
 */
export const MAX_REASON_RUNES = 2000;

/** TOTP 是 6 位十进制数字（`middleware/admin.go` 的 `plausibleTOTPCode`）。 */
export const TOTP_CODE_LENGTH = 6;

/**
 * 需要 L3（TOTP step-up）的 D 编号 —— api-contract §6.2 的表：D3 D5 D6 D10 D15 D16。
 *
 * ⚠️ **这是一处刻意登记的重复。** 它本该是 `lib/danger.ts` 里 `DangerOp` 的一个字段，
 * 与 `reason` / `confirmString` / `separatePerm` 并排 —— 那张表誊的是 page-inventory §4.4，
 * 而 §4.4 没有「L3」这一列，L3 是 api-contract §6.2 才补上的。
 * 现在两份来源在两个文件里，改一份不会波及另一份。
 * TODO(P1)：给 `DangerOp` 加 `totp?: boolean` 并把这里删掉，让「这一条要不要 TOTP」只有一个答案。
 *
 * 在那之前，调用方可以用 `requireTotp` 显式覆盖 —— 覆盖是**加**不是减：见 `stepUpRequired`。
 */
export const DANGER_STEP_UP_CODES: readonly string[] = ['D3', 'D5', 'D6', 'D10', 'D15', 'D16'];

/**
 * L4 权限位在前端的三态。
 *
 * 🔴 **`unknown` 是诚实的默认值，不是「还没加载完」。**
 * 管理面**没有任何端点会告诉前端当前管理员持有哪些权限位** ——
 * 契约里既没有 `/admin/me`，`listAdmins` 又是 owner 专属且不指明「哪一行是我」。
 * 所以除非调用方从别处确知（例如刚吃过一个 `AUTH_PERMISSION_DENIED`），
 * 一律是 `unknown`：**放行到服务端去判**。
 *
 * 反过来做（前端猜没有权限就变灰）会造出一个更坏的形态：
 * 一个真的有权限的人看着一个灰按钮，而没有任何东西告诉他这只是前端在猜。
 */
export type DangerPermissionState = 'granted' | 'denied' | 'unknown';

/** 交给调用方去发请求的那三个值。没要求的那一层是 `undefined`，**不是空串**。 */
export interface DangerousActionValues {
  /** L1。原样放进 body 的 `confirmation`。 */
  readonly confirmation?: string;
  /** L2。原样放进 body 的 `reason`（已 `trim`，与服务端归一化口径一致）。 */
  readonly reason?: string;
  /** L3。放进请求头 `X-TOTP-Code`，**不是 body**。 */
  readonly totp?: string;
}

/** 提交被挡住的原因。`null` = 可以提交。 */
export type DangerBlockReason =
  /** 调用方显式禁用（例如「这个订单已经退过款了」）。 */
  | 'disabled'
  /** L4：确知没有权限位。 */
  | 'permission-denied'
  /** 正在提交。 */
  | 'submitting'
  /** **装配错误**：登记表要求确认串，但调用方没告诉我们期望值是什么。 */
  | 'missing-confirmation-target'
  /** L1：确认串还没逐字打对。 */
  | 'confirmation-mismatch'
  /** L2：原因不足 8 码位。 */
  | 'reason-too-short'
  /** L2：原因超过上限。 */
  | 'reason-too-long'
  /** L3：还没输入 6 位码。 */
  | 'totp-missing';

export interface DangerGateInput {
  readonly permission: DangerPermissionState;
  readonly needsConfirmation: boolean;
  /** L1 期望值（服务端会自己查一份出来比对，这里这份只用来点亮按钮）。 */
  readonly expectedConfirmation: string | null;
  readonly confirmation: string;
  readonly needsReason: boolean;
  readonly reason: string;
  readonly needsTotp: boolean;
  readonly totp: string;
  readonly submitting: boolean;
  readonly disabled: boolean;
}

/** 原因的码位数（先去首尾空白，与服务端 `strings.TrimSpace` 后再数的口径一致）。 */
export function reasonRuneCount(raw: string): number {
  return [...raw.trim()].length;
}

/**
 * L1 的前端比对。**必须与服务端 `confirmationMatches` 同口径**：
 * 两侧 `trim`，**区分大小写**。
 *
 * 大小写敏感不是疏忽：期望值是从这一页上**复制**下来的原样字符串，
 * 大小写不同说明操作者是**手打**的 —— 而「照着念一遍目标是谁」正是这一层要求的动作本身，
 * 静默宽容掉等于把这一层删了。（服务端注释里写了同一条理由。）
 */
export function confirmationMatches(expected: string, got: string): boolean {
  const e = expected.trim();
  return e.length > 0 && e === got.trim();
}

/** 形态检查，与服务端 `plausibleTOTPCode` 同口径：恰好 6 位十进制数字。 */
export function isPlausibleTotpCode(raw: string): boolean {
  const code = raw.trim();
  return code.length === TOTP_CODE_LENGTH && /^[0-9]+$/.test(code);
}

/**
 * 纯函数形态的「能不能提交」。导出是为了单测直接打它，也为了别处能复用同一条判据 ——
 * 组件里再写一遍 `if` 的话，测试绿着而按钮的实际行为可以是另一回事。
 *
 * 顺序有意义：先说**结构性**的原因（禁用 / 无权限 / 装配错误），再说**填写**的原因。
 * 反过来会让一个没有权限的人先被告知「确认串没打对」，照着改半天，最后才发现根本不该他做。
 */
export function dangerBlockReason(input: DangerGateInput): DangerBlockReason | null {
  if (input.disabled) return 'disabled';
  if (input.permission === 'denied') return 'permission-denied';
  if (input.submitting) return 'submitting';

  if (input.needsConfirmation) {
    const expected = input.expectedConfirmation?.trim() ?? '';
    if (expected.length === 0) return 'missing-confirmation-target';
    if (!confirmationMatches(expected, input.confirmation)) return 'confirmation-mismatch';
  }

  if (input.needsReason) {
    const n = reasonRuneCount(input.reason);
    if (n < MIN_REASON_RUNES) return 'reason-too-short';
    if (n > MAX_REASON_RUNES) return 'reason-too-long';
  }

  if (input.needsTotp && !isPlausibleTotpCode(input.totp)) return 'totp-missing';

  return null;
}

/* ────────────────────────────── 组件 ────────────────────────────── */

export interface DangerousActionProps {
  /** page-inventory §4.4 的编号，如 `'D6'`。用来从 `lib/danger.ts` 取标题、危害与各项要求。 */
  code: string;
  /** 按钮上的动作名，如「标记为已支付」。 */
  submitLabel: string;
  /**
   * 真正发请求的地方。**参数原样交给它，它自己去调 API** ——
   * 组件不知道也不该知道该打哪个端点。抛出的 `ApiError` 会被渲染成表单级错误。
   */
  onSubmit: (values: DangerousActionValues) => Promise<void>;
  /**
   * L1 期望值（这个用户的邮箱 / 这个节点的名字 / 这个订单号）。
   *
   * 登记表要求确认串而这里没给（或给了空串）时，组件**变灰并说明是装配错误** ——
   * 不是静默跳过 L1。跳过的现象是「这一条的确认串要求悄悄消失了」，而没有任何东西会报错。
   */
  confirmation?: string | null;
  /** 覆盖登记表的 L2。省略时按 `DANGER[code].reason`。 */
  requireReason?: boolean;
  /** 覆盖 §6.2 的 L3。省略时按 `DANGER_STEP_UP_CODES`。 */
  requireTotp?: boolean;
  /** L4 状态。默认 `'unknown'`，理由见 `DangerPermissionState`。 */
  permission?: DangerPermissionState;
  /** L4 权限位的名字，如 `'admin.order.mark_paid'`。用于禁用态的解释。 */
  permissionName?: string;
  /**
   * 这一条独有的、**必须让操作者看见**的事实。
   * D4「当前在线 128 人」、D11b「收件人 3,412 位」、D13 的配置 diff、D7 的退款金额。
   */
  context?: ReactNode;
  /** 覆盖登记表的标题（同一个 D 编号在不同页面上可能是不同的具体动作）。 */
  title?: string;
  /** 提交成功后的回调（刷新列表、跳走）。 */
  onDone?: () => void;
  /** 业务上暂时不能做（如订单状态不对）。**与「没权限」分开**，文案完全不同。 */
  disabled?: boolean;
  /** `disabled` 时显示的原因。禁用而不说明理由，等于让人去猜。 */
  disabledReason?: ReactNode;
}

export function DangerousAction({
  code,
  submitLabel,
  onSubmit,
  confirmation = null,
  requireReason,
  requireTotp,
  permission = 'unknown',
  permissionName,
  context,
  title,
  onDone,
  disabled = false,
  disabledReason,
}: DangerousActionProps) {
  const op = DANGER[code];
  const fieldId = useId();

  const [open, setOpen] = useState(false);
  const [confirmationInput, setConfirmationInput] = useState('');
  const [reason, setReason] = useState('');
  const [totp, setTotp] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const reset = useCallback(() => {
    setConfirmationInput('');
    setReason('');
    setTotp('');
    setError(null);
  }, []);

  // 登记表里查不到这个编号 = 装配错误。**变灰并说出来**，不要静默按「无要求」渲染 ——
  // 后者会让一次拼错的编号（'D6 ' / 'd6'）把四层要求全部悄悄摘掉。
  if (!op) {
    return (
      <Card className="border-l-4 border-l-danger">
        <p className="text-sm font-semibold text-fg">危险操作装配错误</p>
        <p className="mt-1 text-sm leading-relaxed text-fg-muted">
          <code className="font-mono">{code}</code> 不在 <code className="font-mono">lib/danger.ts</code>{' '}
          的登记表里，因此无法确定它要求哪几层强制。按钮<strong className="font-semibold">不会</strong>被渲染出来。
        </p>
      </Card>
    );
  }

  const needsConfirmation = op.confirmString !== undefined;
  const needsReason = requireReason ?? op.reason ?? false;
  const needsTotp = stepUpRequired(code, requireTotp);

  const blocked = dangerBlockReason({
    permission,
    needsConfirmation,
    expectedConfirmation: confirmation,
    confirmation: confirmationInput,
    needsReason,
    reason,
    needsTotp,
    totp,
    submitting,
    disabled,
  });

  async function handleSubmit() {
    if (blocked !== null) return;
    setSubmitting(true);
    setError(null);
    try {
      await onSubmit({
        ...(needsConfirmation ? { confirmation: confirmationInput.trim() } : {}),
        ...(needsReason ? { reason: reason.trim() } : {}),
        ...(needsTotp ? { totp: totp.trim() } : {}),
      });
      setOpen(false);
      reset();
      onDone?.();
    } catch (cause) {
      setError(cause instanceof ApiError ? cause : new ApiError({
        status: 0,
        code: 'UNKNOWN',
        message: '操作没能完成',
        cause,
      }));
      // 🔴 无论成败，这一次的 TOTP 码都算用过了。
      //
      // `RequireStepUp` 是**先验对、再占用**，而占用那次写入**不在业务事务里**
      // （`middleware/admin.go` 里写明了：「业务操作失败回滚时，code 仍然算用过了」）。
      // 也就是说只要请求走到了 step-up 之后，这个码就报废了，
      // 拿它重试必然拿到 `AUTH_TOTP_INVALID` —— 而那个码的文案是「码不对或已用过」，
      // 操作者会以为是自己的验证器坏了。清空输入框 + 下面那句提示，就是为了断掉这条误判。
      setTotp('');
    } finally {
      setSubmitting(false);
    }
  }

  if (!open) {
    return (
      <div>
        <Button tone="danger" onClick={() => setOpen(true)}>
          {title ?? op.title}
        </Button>
        {/* 折叠状态下也要把「为什么点不动」说出来 —— 见 PermissionNotice 的注释。 */}
        <BlockedHint
          blocked={disabled ? 'disabled' : permission === 'denied' ? 'permission-denied' : null}
          op={op}
          permissionName={permissionName}
          disabledReason={disabledReason}
          expectedConfirmation={confirmation}
        />
      </div>
    );
  }

  return (
    <Card className="border-l-4 border-l-danger">
      <div className="flex flex-wrap items-baseline gap-2">
        <span className="rounded bg-danger/15 px-1.5 py-0.5 font-mono text-xs font-semibold text-danger">
          {op.code}
        </span>
        <h3 className="text-base font-semibold text-fg">{title ?? op.title}</h3>
      </div>
      <p className="mt-1 text-sm leading-relaxed text-danger">危害：{op.harm}</p>
      {op.extra ? <p className="mt-1 text-xs leading-relaxed text-fg-muted">额外要求：{op.extra}</p> : null}
      {op.notify ? (
        <p className="mt-1 text-xs leading-relaxed text-fg-muted">
          这一条要求<strong className="font-medium text-fg">自动通知受影响用户</strong>。通知由服务端发出，不是这里的一个复选框。
        </p>
      ) : null}

      {/* 这一条独有的事实（在线人数 / 收件人数 / diff / 金额）。
          D4 的登记表明写「确认框内必须显示当前在线人数」——
          调用方没传时这里是空的，而空着本身就是评审时看得见的缺口。 */}
      {context ? (
        <div className="mt-3 rounded-lg border border-line bg-surface-alt p-3 text-sm leading-relaxed text-fg">
          {context}
        </div>
      ) : null}

      <PermissionNotice permission={permission} permissionName={permissionName} separatePerm={op.separatePerm} />

      <div className="mt-4 space-y-4">
        {needsConfirmation ? (
          <Field
            id={`${fieldId}-confirmation`}
            label={`输入${op.confirmString}以确认`}
            hint={
              confirmation === null || confirmation.trim().length === 0 ? (
                <span className="text-danger">
                  装配错误：这一条要求确认串，但调用方没有把期望值传给
                  <code className="font-mono"> confirmation </code>。在修好之前不能提交。
                </span>
              ) : (
                <>
                  逐字输入 <code className="font-mono text-fg">{confirmation}</code>（区分大小写）。
                  <strong className="font-medium text-fg">
                    {' '}
                    这个串由服务端自己查出来后再比对（§6.2 L1）
                  </strong>
                  ，这里的比对只是提前告诉你打对了没有 —— 前端拦不住任何人。
                </>
              )
            }
          >
            <input
              id={`${fieldId}-confirmation`}
              name="confirmation"
              value={confirmationInput}
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => setConfirmationInput(event.target.value)}
              className={cx(CONTROL, 'min-h-11 font-mono')}
            />
          </Field>
        ) : null}

        {needsReason ? (
          <Field
            id={`${fieldId}-reason`}
            label="操作原因（必填）"
            hint={
              <>
                至少 {MIN_REASON_RUNES} 个字（当前 {reasonRuneCount(reason)}）。
                <strong className="font-medium text-fg"> 它会原样进审计日志</strong>
                ，而读它的人是事后复盘的你自己 —— 写「补单」不如写「链上 txid 7f3a… 已确认到账，网关回调丢失」。
              </>
            }
          >
            <textarea
              id={`${fieldId}-reason`}
              name="reason"
              rows={3}
              value={reason}
              onChange={(event) => setReason(event.target.value)}
              className={cx(CONTROL, 'py-2.5 leading-relaxed')}
            />
          </Field>
        ) : null}

        {needsTotp ? (
          <Field
            id={`${fieldId}-totp`}
            label="验证器 6 位码"
            hint={
              <>
                这一条要求当次两步验证（§6.2 L3）。
                <strong className="font-medium text-fg"> 同一个码 5 分钟内只能用一次</strong>
                ，且只要请求发出去过就算用过（校验通过后的占用不随业务失败回滚）——
                重试时请等验证器跳到下一个码。
              </>
            }
          >
            <input
              id={`${fieldId}-totp`}
              name="totp"
              value={totp}
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={TOTP_CODE_LENGTH}
              onChange={(event) => setTotp(event.target.value)}
              className={cx(CONTROL, 'min-h-11 font-mono tracking-[0.4em]')}
            />
          </Field>
        ) : null}

        {error ? <SubmitError error={error} /> : null}

        <div className="flex flex-wrap items-center gap-2">
          <Button
            tone="danger"
            disabled={blocked !== null}
            aria-disabled={blocked !== null}
            onClick={() => void handleSubmit()}
          >
            {submitting ? '提交中…' : submitLabel}
          </Button>
          <Button
            tone="ghost"
            disabled={submitting}
            onClick={() => {
              setOpen(false);
              reset();
            }}
          >
            取消
          </Button>
        </div>

        <BlockedHint
          blocked={blocked}
          op={op}
          permissionName={permissionName}
          disabledReason={disabledReason}
          expectedConfirmation={confirmation}
        />

        <p className="text-xs leading-relaxed text-fg-subtle">
          这次操作会写进审计日志（谁、何时、改前值、改后值
          {needsReason ? '、你填的原因' : ''}）。审计日志没有删除入口，也没有编辑入口。
        </p>
      </div>
    </Card>
  );
}

/* ────────────────────────── 组件内部的零件 ────────────────────────── */

const CONTROL =
  'w-full rounded-lg border border-line bg-surface px-3 text-base text-fg ' +
  'placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-2 ' +
  'focus-visible:outline-offset-1 focus-visible:outline-accent disabled:opacity-50';
// 16px 起步（`text-base`）：iOS Safari 在 <16px 的输入框获得焦点时会自动放大页面。
// 后台是 M3 桌面优先，但工单 / 节点 / 订单是 M2 —— 手机上要能紧急停用节点。

/**
 * 提交失败的表单级错误位。`role="alert"` 是必须的：提交失败时焦点还在按钮上，
 * 不播报的话屏幕阅读器用户只会觉得「点了没反应」。
 */
function SubmitError({ error }: { error: ApiError }) {
  const copy = dangerErrorCopy(error);
  return (
    <div role="alert" className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-sm text-danger">
      <p className="font-medium">{copy.title}</p>
      <p className="mt-0.5 leading-relaxed">{copy.description}</p>
      {/* 请求号要露出来：危险操作出问题时，这是唯一能把前端这一次点击与
          服务端那条审计记录对上的串。 */}
      {error.requestId ? <p className="mt-1 font-mono text-xs opacity-80">请求号 {error.requestId}</p> : null}
    </div>
  );
}

function Field({
  id,
  label,
  hint,
  children,
}: {
  id: string;
  label: string;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div>
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium text-fg">
        {label}
      </label>
      {children}
      {hint ? <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </div>
  );
}

/**
 * L4 的说明位。
 *
 * 🔴 **权限不足时按钮变灰但不消失。** 「你没有这个权限」和「这个功能不存在」
 * 对操作者是两件完全不同的事：前者他知道该去找谁开权限，后者他会去提一个功能需求。
 * 藏起来的按钮会把前者变成后者，而且**无法被投诉** —— 没人会报告一个他看不见的东西。
 */
function PermissionNotice({
  permission,
  permissionName,
  separatePerm,
}: {
  permission: DangerPermissionState;
  permissionName?: string;
  separatePerm?: boolean;
}) {
  if (permission === 'denied') {
    return (
      <p className="mt-3 rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-xs leading-relaxed text-danger">
        当前管理员账号<strong className="font-semibold">没有</strong>执行这一条所需的权限位
        {permissionName ? (
          <>
            （<code className="font-mono">{permissionName}</code>）
          </>
        ) : null}
        。这不是功能缺失，也不是故障 —— 需要另一个有权限的人给你开。
      </p>
    );
  }
  if (separatePerm) {
    return (
      <p className="mt-3 rounded-lg border border-line bg-surface-alt px-3 py-2 text-xs leading-relaxed text-fg-muted">
        这一条挂在<strong className="font-medium text-fg">独立权限位</strong>
        {permissionName ? (
          <>
            （<code className="font-mono">{permissionName}</code>）
          </>
        ) : null}
        上，默认不授予。
        {permission === 'unknown'
          ? ' 前端无法预先知道你有没有这个位（管理面没有任何端点会告诉前端权限位），所以按钮是亮的 —— 有没有由服务端说了算。'
          : null}
      </p>
    );
  }
  return null;
}

/** 按钮点不动时，把**第一条**挡住它的原因说出来。 */
function BlockedHint({
  blocked,
  op,
  permissionName,
  disabledReason,
  expectedConfirmation,
}: {
  blocked: DangerBlockReason | null;
  op: DangerOp;
  permissionName?: string;
  disabledReason?: ReactNode;
  expectedConfirmation?: string | null;
}) {
  if (blocked === null || blocked === 'submitting') return null;

  const text = ((): ReactNode => {
    switch (blocked) {
      case 'disabled':
        return disabledReason ?? '当前状态下不能执行这一条。';
      case 'permission-denied':
        return (
          <>
            你没有执行这一条所需的权限位
            {permissionName ? (
              <>
                （<code className="font-mono">{permissionName}</code>）
              </>
            ) : null}
            。功能是存在的，缺的是授权。
          </>
        );
      case 'missing-confirmation-target':
        return (
          <>
            装配错误：这一条要求输入{op.confirmString}，但页面没有把期望值传进来。
            这是代码缺陷，不是你的操作问题。
          </>
        );
      case 'confirmation-mismatch':
        return (
          <>
            还需要逐字输入{op.confirmString}
            {expectedConfirmation ? (
              <>
                （<code className="font-mono">{expectedConfirmation}</code>，区分大小写）
              </>
            ) : null}
            。
          </>
        );
      case 'reason-too-short':
        return `还需要填写操作原因，至少 ${MIN_REASON_RUNES} 个字。`;
      case 'reason-too-long':
        return `操作原因太长了，最多 ${MAX_REASON_RUNES} 个字。`;
      case 'totp-missing':
        return `还需要输入验证器上的 ${TOTP_CODE_LENGTH} 位码。`;
      default:
        return null;
    }
  })();

  if (text === null) return null;

  return (
    <p className="mt-2 text-xs leading-relaxed text-fg-muted" data-testid="danger-blocked-hint">
      {text}
    </p>
  );
}

/** `requireTotp` 覆盖：显式传 `true` 会**加上** L3，传 `false` 只能去掉登记表之外的要求。 */
function stepUpRequired(code: string, override: boolean | undefined): boolean {
  const listed = DANGER_STEP_UP_CODES.includes(code);
  if (override === undefined) return listed;
  // 🔴 覆盖不能把 §6.2 明文要求的 L3 关掉。允许「传 false 就不要 TOTP」等于给每个调用点
  // 留了一个关掉第二因子的开关，而这种开关最终一定会被某个赶时间的人打开。
  return listed || override;
}

/**
 * `ErrorCode` → 文案。**按 code 分支，不按 HTTP 状态码分支**（api-contract §2.3）。
 * 导出是为了别的危险操作调用点复用同一份 —— 各写各的会让同一个码在两页上说两句不同的话。
 */
export function dangerErrorCopy(error: ApiError): { title: string; description: string } {
  switch (error.code) {
    case 'NOT_IMPLEMENTED':
      return {
        title: '这个操作还没上线',
        description: '后端这一条还没实现。不是你的填写有问题，重试也不会有变化。',
      };
    case 'AUTH_TOTP_REQUIRED':
      return {
        title: '这一条需要两步验证',
        description: '服务端要求当次 TOTP。若你已经输入了码却看到这句，多半是后台的 TOTP 密钥没配好，请找运维。',
      };
    case 'AUTH_TOTP_INVALID':
      return {
        title: '验证码不正确，或已经被用过',
        description:
          '这两种情况服务端有意不区分。上一次提交若已经发出去过，那个码就报废了 —— 等验证器跳到下一个码再试。',
      };
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '这个账号不能执行这一条',
        description: '身份是通过的，缺的是权限位或角色。重试与重新登录都不会有帮助。',
      };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return {
        title: '服务端退回了这次提交',
        description: fieldReasons(error) ?? error.message,
      };
    case 'STATE_CONFLICT':
      // D5 的两步强制走的就是这个码：「新密钥尚未被节点使用过」。
      // 服务端的 message 写得比任何前端兜底文案都具体，所以原样显示。
      return { title: '当前状态不允许这次操作', description: error.message };
    case 'RESOURCE_NOT_FOUND':
      return { title: '找不到操作对象', description: '它可能刚被别人改过或删掉了。刷新一下再看。' };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '操作太频繁',
        description:
          error.retryAfterSeconds === undefined
            ? '稍后再试。'
            : `${error.retryAfterSeconds} 秒后可以再试。`,
      };
    default:
      break;
  }
  switch (error.kind) {
    case 'offline':
      return {
        title: '请求没能到达服务端',
        description:
          '这次操作可能已经执行了（响应也可能只是在回程丢了）。刷新页面确认当前状态，不要直接重试。',
      };
    case 'server':
      return { title: '服务端出错了', description: '不是你的填写有问题。稍后再试，并把请求号一起报出来。' };
    default:
      return { title: '操作没能完成', description: error.message };
  }
}

function fieldReasons(error: ApiError): string | null {
  const details = error.details;
  if (!details || details.length === 0) return null;
  return details.map((d) => `${d.field}：${d.reason}`).join('；');
}
