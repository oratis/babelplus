/**
 * 模块 11 · 管理员账号 `/admin/admins` —— P1 / M3。
 *
 * TOTP 状态是这一页最重要的一列：闸 3 是**强制**的，
 * 所以「TOTP 未开启」的管理员是一个必须被立刻发现并处理的状态，不是一个可选项。
 * （⚠️ 在当前 schema 下这一列恒为「已开启」，为什么见 `TotpBadge` 的注释 ——
 *  那一段比这一列本身更值得读。）
 *
 * ══════════════════════════════════════════════════════════════════
 * 🔴 一、新建出来的管理员**登不进去**，开户是两步
 * ══════════════════════════════════════════════════════════════════
 *
 * `admin_users.totp_confirmed_at` 是 NOT NULL —— 数据库里**不存在**「已创建但还没绑 2FA」
 * 这个状态，secret 在 INSERT 那一刻就必须有值；而 `createAdmin` 的 201 响应体是
 * `AdminAccount`，**装不下绑定材料**，那串明文 secret 只能就地丢弃。
 * 唯一能拿到二维码 / secret 的端点是 `resetAdminTotp`。所以正确流程是：
 *
 *   ① 新建 → 拿到 id      ② 立刻对同一个 id 跑「重置 TOTP」→ 拿到绑定材料**当面交给本人**
 *
 * 少了第 ② 步，那个人**永远进不来**，而现场唯一的「解法」是直接改库 ——
 * 那正是权限系统存在的意义被绕过的那一刻。服务端为此在 201 上加了 `X-Next-Step` 头；
 * 这一页把它做成一块**挡在眼前的下一步卡片**，而不是一句提示语。
 *
 * ⚠️ `X-Next-Step` 不在 CORS 的 `Access-Control-Expose-Headers` 名单里
 * （`middleware/cors.go` 只暴露 X-Request-Id / Retry-After / ETag），
 * 所以**跨域部署时前端读不到它**。因此这一页的第 ② 步不依赖那个头 ——
 * 头只是一份佐证，读到就显示出来，读不到照样走。
 *
 * ══════════════════════════════════════════════════════════════════
 * 🔴 二、D 编号在两份来源里是对调的，这里按 §4.4 走
 * ══════════════════════════════════════════════════════════════════
 *
 * | | page-inventory §4.4（= `lib/danger.ts`） | openapi 的 summary |
 * |---|---|---|
 * | D15 | **删除管理员** | createAdmin / resetAdminTotp |
 * | D16 | **重置他人 TOTP** | deleteAdmin |
 *
 * `DangerousAction` 读的是 `lib/danger.ts`，也就是 §4.4 那一份。按 openapi 的编号传的话，
 * 「停用管理员」的面板会顶着「重置他人 TOTP」的标题与危害说明 —— 那比编号错本身更糟。
 * 所以：**停用 → D15，重置 TOTP → D16**。两边要求的层数恰好相同（🔒 确认串 + TOTP），
 * 所以这个选择不影响到底收哪几个参数，只影响操作者读到的是哪一段话。
 *
 * 而**新建管理员在 §4.4 里根本没有编号**。它不套 `DangerousAction`，理由见 `CreateAdminCard`。
 */
import { useId, useMemo, useState } from 'react';
import { ApiError, toApiError, unwrap, unwrapEmpty } from '@babelplus/shared/api';
import type { components } from '@babelplus/shared/api';
import { Badge, Button, Card, CardTitle, EmptyState, formatDateTime } from './_imports.ts';
import {
  DangerousAction,
  MIN_REASON_RUNES,
  TOTP_CODE_LENGTH,
  dangerErrorCopy,
  isPlausibleTotpCode,
  reasonRuneCount,
} from '../components/DangerousAction.tsx';
import { api } from '../lib/api.ts';
import {
  DangerOpsNote,
  FieldShell,
  ListSkeleton,
  MISSING,
  ModuleHeader,
  QueryErrorState,
  TextField,
  useApiQuery,
} from './UsersPage.tsx';

type AdminAccount = components['schemas']['AdminAccount'];
type AdminPermission = components['schemas']['AdminPermission'];
type TotpEnrollment = components['schemas']['TotpEnrollment'];

/** 刚拿到的绑定材料。**只在内存里活着**，不落 storage、不进 URL、不写日志。 */
interface Enrollment {
  readonly adminId: number;
  readonly email: string;
  readonly data: TotpEnrollment;
}

export default function AdminsPage() {
  const list = useApiQuery(
    () => unwrap(api().GET('/api/v1/admin/admins')),
    [],
    '管理员列表加载失败',
  );

  /** 刚建好、还没拿到绑定材料的那一位。它是这一页的「未完成事项」，必须挡在眼前。 */
  const [pendingEnrollment, setPendingEnrollment] = useState<{
    readonly admin: AdminAccount;
    readonly nextStep: string | null;
  } | null>(null);

  /** 刚生成的绑定材料（新建后或单独重置后）。同一时刻只留一份。 */
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);

  const admins = list.data ?? [];

  /**
   * 排序：**TOTP 未开启的置顶**，其余按 id。
   *
   * 「未开启」不是一个「待完善的设置」，是一道闸没关上 —— 它必须出现在第一行，
   * 而不是躺在第七行等人滚动到它。
   */
  const sorted = useMemo(
    () => [...admins].sort((a, b) => Number(a.totp_enabled) - Number(b.totp_enabled) || a.id - b.id),
    [admins],
  );

  return (
    <>
      <ModuleHeader
        title="管理员"
        description="账号、权限位、TOTP 状态。这一页上的每一条都是「谁能进后台」。"
        priority="P1"
        mobile="M3"
        extraMeta={list.state === 'ready' ? <Badge>在职 {admins.length} 人</Badge> : null}
      />

      <DangerOpsNote codes={['D15', 'D16']} />

      <div className="space-y-4">
        <CreateAdminCard
          onCreated={(admin, nextStep) => {
            setPendingEnrollment({ admin, nextStep });
            setEnrollment(null);
            list.reload();
          }}
        />

        {pendingEnrollment && !enrollment ? (
          <NextStepCard admin={pendingEnrollment.admin} nextStep={pendingEnrollment.nextStep} />
        ) : null}

        {enrollment ? (
          <EnrollmentCard enrollment={enrollment} onDismiss={() => setEnrollment(null)} />
        ) : null}

        {list.state === 'loading' ? <ListSkeleton rows={3} /> : null}

        {list.state === 'error' && list.error ? (
          <QueryErrorState error={list.error} what="管理员列表" onRetry={list.reload} />
        ) : null}

        {list.state === 'ready' && admins.length === 0 ? (
          <EmptyState
            title="没有管理员"
            description="这个状态不该出现 —— 你正在用一个管理员账号看这一页。多半是列表请求打到了别的环境。"
            action={<Button onClick={list.reload}>重新加载</Button>}
          />
        ) : null}

        {sorted.map((admin) => (
          <AdminCard
            key={admin.id}
            admin={admin}
            // 🔴 前端这道闸挡的是「最后一个在职管理员」。服务端**没有**这条判断，
            //    它只拒绝「停用自己」——而当列表里只剩一个人时，那一个人必然是你自己，
            //    所以两条闸在这一刻恰好重合。列表里有两个人时，前端就不该再拦了。
            isOnlyAdmin={admins.length <= 1}
            onDisabled={() => {
              setPendingEnrollment((prev) => (prev && prev.admin.id === admin.id ? null : prev));
              list.reload();
            }}
            onEnrolled={(data) => {
              setEnrollment({ adminId: admin.id, email: admin.email, data });
              setPendingEnrollment((prev) => (prev && prev.admin.id === admin.id ? null : prev));
              list.reload();
            }}
          />
        ))}

        <PermissionsCard />
        <SelfLockCard />
      </div>
    </>
  );
}

/* ────────────────────────────── 单个管理员 ────────────────────────────── */

function AdminCard({
  admin,
  isOnlyAdmin,
  onDisabled,
  onEnrolled,
}: {
  admin: AdminAccount;
  isOnlyAdmin: boolean;
  onDisabled: () => void;
  onEnrolled: (data: TotpEnrollment) => void;
}) {
  return (
    <Card>
      {/* 锚点：新建之后的「下一步」卡片直接指到这里。 */}
      <div id={`admin-${admin.id}`} className="scroll-mt-20">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <div className="min-w-0">
            <h3 className="text-base font-semibold break-words text-fg">{admin.email}</h3>
            <p className="mt-0.5 font-mono text-xs text-fg-subtle">#{admin.id}</p>
          </div>
          <TotpBadge enabled={admin.totp_enabled} />
        </div>

        <dl className="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-3">
          <div className="min-w-0">
            <dt className="text-xs font-medium text-fg-muted">最后登录</dt>
            <dd className="mt-0.5 text-sm text-fg">
              {admin.last_login_at ? formatDateTime(admin.last_login_at) : '从未登录'}
            </dd>
            {admin.last_login_at ? null : (
              <p className="mt-0.5 text-xs leading-relaxed text-warn">
                从未登录过的账号，多半是建好之后没跑「重置 TOTP」那一步 —— 他手上什么都没有。
              </p>
            )}
          </div>
          <div className="min-w-0">
            <dt className="text-xs font-medium text-fg-muted">创建时间</dt>
            <dd className="mt-0.5 text-sm text-fg">{formatDateTime(admin.created_at)}</dd>
          </div>
          <div className="min-w-0">
            <dt className="text-xs font-medium text-fg-muted">权限位</dt>
            <dd className="mt-0.5 flex flex-wrap gap-1 text-sm text-fg">
              {admin.permissions.length === 0 ? (
                <span className="text-fg-muted">{MISSING}</span>
              ) : (
                admin.permissions.map((p) => (
                  <Badge key={p} tone="warn">
                    {permissionLabel(p)}
                  </Badge>
                ))
              )}
            </dd>
          </div>
        </dl>

        <div className="mt-4 grid gap-4 lg:grid-cols-2">
          {/* D16（§4.4 编号）：重置他人 TOTP。 */}
          <DangerousAction
            code="D16"
            title="重置他的 TOTP（并拿到绑定材料）"
            submitLabel="生成新的绑定材料"
            // 登记表里 D16 没有勾「必填原因」，但服务端**要**（≥ 8 字符）。
            // 不显式加上的话，操作者会在打完确认串与 TOTP 之后吃一个 422，
            // 而那时他那个 TOTP 码已经报废了（step-up 是先验对再占用，占用不随业务失败回滚）。
            requireReason
            confirmation={admin.email}
            context={
              <>
                <p>
                  目标：<span className="font-medium text-fg">{admin.email}</span>
                  <span className="ml-2 font-mono text-xs text-fg-subtle">#{admin.id}</span>
                </p>
                <p className="mt-2">
                  🔴 <strong className="font-medium text-fg">旧验证码在提交的那一刻立刻失效，没有过渡窗口</strong>
                  （库里不存在「待确认」状态）。所以顺序只能是：
                  <strong className="font-medium text-fg">先确保你能把新的二维码交到他手上，再点提交</strong>。
                  顺序错了那个人就进不来了，而他自己没有任何自助恢复入口。
                </p>
                <p className="mt-2 text-xs text-fg-muted">
                  ⚠️ 登记表要求「📧 通知被重置者」，但
                  <strong className="font-medium text-fg">服务端不发这封信</strong>
                  （handler 里没有发信路径）—— 通知这件事现在是你的。
                </p>
              </>
            }
            onSubmit={async ({ confirmation, reason, totp }) => {
              const data = await unwrap(
                api().POST('/api/v1/admin/admins/{id}/reset-totp', {
                  params: {
                    path: { id: admin.id },
                    // L3 走请求头，不是 body。
                    header: { 'X-TOTP-Code': totp ?? '' },
                  },
                  body: { confirmation: confirmation ?? '', reason: reason ?? '' },
                }),
              );
              onEnrolled(data);
            }}
          />

          {/* D15（§4.4 编号）：删除管理员 —— 服务端实现是**软停用**。 */}
          <DangerousAction
            code="D15"
            title="停用这个管理员"
            submitLabel="停用"
            requireReason
            confirmation={admin.email}
            disabled={isOnlyAdmin}
            disabledReason="他是列表里唯一的在职管理员，而这个列表只列在职的 —— 也就是说他是你自己。停用自己会当场把你锁在门外，服务端也会拒绝（422）。"
            context={
              <>
                <p>
                  目标：<span className="font-medium text-fg">{admin.email}</span>
                  <span className="ml-2 font-mono text-xs text-fg-subtle">#{admin.id}</span>
                </p>
                <p className="mt-2">
                  🔴 这一条写的是<strong className="font-medium text-fg">停用（`disabled_at`），不是删除</strong>。
                  硬删会让他过去的每一条 D1–D16 审计记录变成认不出人的孤儿
                  （审计表的外键是 ON DELETE SET NULL，而契约的审计条目里只有 admin_id 没有邮箱），
                  而「事后能重建谁做了什么」正是审计存在的全部意义。
                </p>
                <p className="mt-2">
                  <strong className="font-medium text-fg">API 上没有撤销入口</strong>（冻结的契约没给），
                  停错了只能直接改库。停用后他会从这个列表里消失 —— 那就是成功的唯一反馈。
                </p>
                <p className="mt-2 text-xs text-fg-muted">
                  ⚠️ 想用同一个邮箱重建账号，需要迁移 0019 已经执行（在那之前停用的管理员会<strong className="font-medium text-fg">永久</strong>占住邮箱）。
                </p>
              </>
            }
            onSubmit={async ({ confirmation, reason, totp }) => {
              await unwrapEmpty(
                api().DELETE('/api/v1/admin/admins/{id}', {
                  params: {
                    path: { id: admin.id },
                    header: { 'X-TOTP-Code': totp ?? '' },
                  },
                  body: { confirmation: confirmation ?? '', reason: reason ?? '' },
                }),
              );
            }}
            onDone={onDisabled}
          />
        </div>
      </div>
    </Card>
  );
}

/**
 * TOTP 状态。
 *
 * 🔴 **「未开启」这个状态在当前 schema 下不可能出现，而这恰恰是要说出来的那件事。**
 * 服务端算的是 `totp_confirmed_at IS NOT NULL`，而那一列是 **NOT NULL**
 * （data-model §11.2：「数据库层面不存在没有 2FA 的管理员」）—— 所以它恒为 true。
 *
 * 也就是说这一列证明的是「库里有一份 secret」，**不是**「本人真的绑过验证器」：
 * 一个 `createAdmin` 建出来、没跑过 reset-totp 的账号在这里同样显示「已开启」，
 * 而他手上什么都没有。真正能识别那个人的信号是**「从未登录」**（见上面那一列）。
 *
 * 仍然保留红色分支：哪天有人放宽了那两列的 NOT NULL，这一格会自己开始说实话。
 */
function TotpBadge({ enabled }: { enabled: boolean }) {
  return enabled ? (
    <Badge tone="neutral">TOTP 已开启（库里有 secret）</Badge>
  ) : (
    <Badge tone="danger">TOTP 未开启 —— 一道闸没关上</Badge>
  );
}

const PERMISSION_LABELS: Readonly<Record<string, string>> = {
  'admin.order.mark_paid': 'D6 手工标记订单已支付',
  'admin.user.export': 'D14 导出用户 CSV',
};

function permissionLabel(p: AdminPermission): string {
  return PERMISSION_LABELS[p] ?? p;
}

/* ────────────────────────────── 新建管理员 ────────────────────────────── */

/** 只有这一个权限位能通过 API 授予，理由见 `CreateAdminCard` 的注释。 */
const GRANTABLE: AdminPermission = 'admin.user.export';

export interface CreateAdminGateInput {
  readonly email: string;
  readonly reason: string;
  readonly totp: string;
  readonly submitting: boolean;
}

export type CreateAdminBlockReason =
  | 'submitting'
  | 'email-missing'
  | 'email-malformed'
  | 'reason-too-short'
  | 'totp-missing';

/**
 * 「能不能提交」的纯函数判据。导出是为了单测直接打它，也为了组件与测试共用同一条规则 ——
 * 组件里再写一遍 `if` 的话，测试绿着而按钮的实际行为可以是另一回事。
 *
 * 🔴 **这里没有一条是安全边界。** 服务端强制的是 L2（原因 ≥ 8 码位）+ L3（TOTP）+ owner 角色；
 * 这几个判断只是省一次注定失败的往返 —— 尤其是 TOTP：一次因为原因太短而被退回的提交，
 * 会把那个码白白烧掉（step-up 先验对再占用，占用不随业务失败回滚）。
 */
export function createAdminBlockReason(input: CreateAdminGateInput): CreateAdminBlockReason | null {
  if (input.submitting) return 'submitting';
  if (input.email.trim() === '') return 'email-missing';
  if (!looksLikeEmail(input.email)) return 'email-malformed';
  if (reasonRuneCount(input.reason) < MIN_REASON_RUNES) return 'reason-too-short';
  if (!isPlausibleTotpCode(input.totp)) return 'totp-missing';
  return null;
}

/** 形态检查，不是校验：服务端有它自己的 `validEmail`，这里只挡住明显的手滑。 */
function looksLikeEmail(raw: string): boolean {
  const t = raw.trim();
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(t);
}

/**
 * 新建管理员。
 *
 * 🔴 **为什么这里不套 `DangerousAction`。**
 * §4.4 没有「新建管理员」这一条编号（D15 是删除、D16 是重置 TOTP），而 `DangerousAction`
 * 的四层要求全部从 `lib/danger.ts` 按编号查。硬套 D15 的后果是它会渲染一个
 * 「输入管理员邮箱以确认」的框 —— 而契约的 `AdminAccountCreateRequest` 只有
 * `{email, permissions, reason}`，**没有 confirmation 字段**：那个框里的字符串哪儿也去不了，
 * 它会是一道纯前端的假闸。而假闸比没有闸更坏，因为它让人以为这一步被守住了。
 *
 * （服务端在 `CreateAdmin` 里逐字写了同一件事：L1 在这个端点上**无法表达** ——
 *  L1 的形态是「服务端查出目标对象的标识串再比对」，而新建时那个对象还不存在。）
 *
 * 所以这里手写表单，只收服务端**真正强制**的两层，并且复用 `DangerousAction` 导出的
 * 同一批判据（`reasonRuneCount` / `isPlausibleTotpCode` / `dangerErrorCopy`），
 * 免得两处对「8 个字」「6 位码」各有各的算法。
 */
function CreateAdminCard({
  onCreated,
}: {
  onCreated: (admin: AdminAccount, nextStep: string | null) => void;
}) {
  const fieldId = useId();
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState('');
  const [exportPerm, setExportPerm] = useState(false);
  const [reason, setReason] = useState('');
  const [totp, setTotp] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const blocked = createAdminBlockReason({ email, reason, totp, submitting });

  async function submit(): Promise<void> {
    if (blocked !== null) return;
    setSubmitting(true);
    setError(null);
    try {
      // 这里刻意**不用** `unwrap`：201 的响应头 `X-Next-Step` 也要读，而 `unwrap` 只给 body。
      const result = await api().POST('/api/v1/admin/admins', {
        params: { header: { 'X-TOTP-Code': totp.trim() } },
        body: {
          email: email.trim(),
          permissions: exportPerm ? [GRANTABLE] : [],
          reason: reason.trim(),
        },
      });
      if (result.error !== undefined || result.data === undefined) {
        throw toApiError(result.response, result.error);
      }
      // 读不到就是 null（跨域时它不在 CORS 暴露名单里）——**不能**因此就不显示下一步。
      const nextStep = result.response.headers.get('X-Next-Step');
      onCreated(result.data.data, nextStep);
      setOpen(false);
      setEmail('');
      setExportPerm(false);
      setReason('');
      setTotp('');
    } catch (cause) {
      setError(
        cause instanceof ApiError
          ? cause
          : new ApiError({ status: 0, code: 'UNKNOWN', message: '新建管理员没能完成', cause }),
      );
    } finally {
      // 🔴 无论成败，这一次的 TOTP 码都算用过了：只要请求走到了 step-up 之后，
      //    它就报废了。留在输入框里会让下一次提交必然拿到 AUTH_TOTP_INVALID，
      //    而那句文案是「码不对或已用过」—— 操作者会以为自己的验证器坏了。
      setTotp('');
      setSubmitting(false);
    }
  }

  if (!open) {
    return (
      <Card>
        <CardTitle hint="createAdmin">新建管理员</CardTitle>
        <p className="mb-3 text-sm leading-relaxed text-fg-muted">
          新建出来的账号<strong className="font-medium text-fg">还不能登录</strong>：
          必须紧接着为他重置一次 TOTP，把二维码 / secret 当面交给本人。这一页会把那一步挡在你眼前。
        </p>
        <Button tone="primary" onClick={() => setOpen(true)}>
          新建管理员
        </Button>
      </Card>
    );
  }

  return (
    <Card className="border-l-4 border-l-danger">
      <CardTitle hint="createAdmin · 只有 owner 能做">新建管理员</CardTitle>

      <div className="space-y-4">
        <TextField
          id={`${fieldId}-email`}
          label="邮箱（Google 账号）"
          type="email"
          value={email}
          onChange={setEmail}
          placeholder="someone@example.com"
          hint={
            <>
              必须是他登录 IAP 用的那个 Google 账号 —— 管理面不认密码，只认 IAP 断言里的邮箱。
              写错的后果不是「他登不进」而是「另一个人能进」。
            </>
          }
        />

        <fieldset>
          <legend className="mb-1.5 text-sm font-medium text-fg">权限位</legend>
          <label className="flex cursor-pointer items-start gap-2 text-sm text-fg">
            <input
              type="checkbox"
              checked={exportPerm}
              onChange={(event) => setExportPerm(event.target.checked)}
              className="mt-1 size-4 accent-accent"
            />
            <span>
              <code className="font-mono">admin.user.export</code>（D14 导出用户 CSV）
              <span className="mt-0.5 block text-xs leading-relaxed text-fg-muted">
                默认不授予。这是唯一能通过这个接口授予的权限位。
              </span>
            </span>
          </label>

          <label className="mt-2 flex items-start gap-2 text-sm text-fg-muted opacity-70">
            <input type="checkbox" checked={false} disabled readOnly className="mt-1 size-4" />
            <span>
              <code className="font-mono">admin.order.mark_paid</code>（D6 手工标记订单已支付）
              <span className="mt-0.5 block text-xs leading-relaxed">
                <strong className="font-medium text-fg">授不了，服务端会 422。</strong>
                ADR 0012 §16.3 裁决：在 D6 的带外留痕 sink 被端到端验证通过之前，
                这个权限位对<strong className="font-medium text-fg">所有</strong>管理员保持关闭。要开它得先验证 sink，再由 DBA 改库。
              </span>
            </span>
          </label>

          <p className="mt-2 text-xs leading-relaxed text-fg-muted">
            契约里另外五个 <code className="font-mono">admin.*.write</code> 在库里
            <strong className="font-medium text-fg">没有对应的列</strong>
            （由 owner / admin / support 三个角色决定），传上去一律 422 —— 所以这里不列它们。
            另外两个直接动钱的位（D7 退款、D10 调余额）在契约的枚举里
            <strong className="font-medium text-fg">没有对应值</strong>，
            连看都看不见，只能改库授予。
          </p>
        </fieldset>

        <FieldShell
          id={`${fieldId}-reason`}
          label="新建原因（必填）"
          hint={
            <>
              至少 {MIN_REASON_RUNES} 个字（当前 {reasonRuneCount(reason)}）。
              <strong className="font-medium text-fg"> 它会原样进审计日志</strong>
              —— 写「新同事」不如写「运维交接：张三接手节点值班，需要 nodes 与 tickets 的只读+处理权」。
            </>
          }
        >
          <textarea
            id={`${fieldId}-reason`}
            name="reason"
            rows={3}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            className="w-full rounded-lg border border-line bg-surface px-3 py-2.5 text-base leading-relaxed text-fg focus-visible:border-accent focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent"
          />
        </FieldShell>

        <FieldShell
          id={`${fieldId}-totp`}
          label="你自己的验证器 6 位码"
          hint={
            <>
              新建管理员要求当次两步验证（§6.2 L3）。
              <strong className="font-medium text-fg"> 同一个码 5 分钟内只能用一次</strong>，
              且只要请求发出去过就算用过 —— 重试时请等验证器跳到下一个码。
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
            className="min-h-11 w-full rounded-lg border border-line bg-surface px-3 font-mono text-base tracking-[0.4em] text-fg focus-visible:border-accent focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent"
          />
        </FieldShell>

        {error ? <CreateError error={error} /> : null}

        <div className="flex flex-wrap items-center gap-2">
          <Button
            tone="danger"
            disabled={blocked !== null}
            aria-disabled={blocked !== null}
            onClick={() => void submit()}
          >
            {submitting ? '提交中…' : '创建（他还不能登录）'}
          </Button>
          <Button
            tone="ghost"
            disabled={submitting}
            onClick={() => {
              setOpen(false);
              setError(null);
              setTotp('');
            }}
          >
            取消
          </Button>
        </div>

        {blocked !== null && blocked !== 'submitting' ? (
          <p className="text-xs leading-relaxed text-fg-muted" data-testid="create-blocked-hint">
            {CREATE_BLOCK_COPY[blocked]}
          </p>
        ) : null}

        <p className="text-xs leading-relaxed text-fg-subtle">
          只有 <strong className="font-medium text-fg">owner</strong> 能新建管理员。
          角色不在契约的 <code className="font-mono">AdminAccount</code> 里，所以这一页看不出谁是 owner ——
          不是 owner 的话，你会在提交后收到一个 403。
        </p>
      </div>
    </Card>
  );
}

const CREATE_BLOCK_COPY: Readonly<Record<CreateAdminBlockReason, string>> = {
  submitting: '',
  'email-missing': '先填他的邮箱。',
  'email-malformed': '这不像一个邮箱地址（服务端也会拒绝）。',
  'reason-too-short': `还需要填写新建原因，至少 ${MIN_REASON_RUNES} 个字。`,
  'totp-missing': `还需要输入你自己验证器上的 ${TOTP_CODE_LENGTH} 位码。`,
};

/** 复用危险操作那张按 `ErrorCode` 分支的表 —— 两处各写一份，同一个码迟早说出两句话。 */
function CreateError({ error }: { error: ApiError }) {
  const copy = dangerErrorCopy(error);
  return (
    <div role="alert" className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-sm text-danger">
      <p className="font-medium">{copy.title}</p>
      <p className="mt-0.5 leading-relaxed">{copy.description}</p>
      {error.requestId ? <p className="mt-1 font-mono text-xs opacity-80">请求号 {error.requestId}</p> : null}
    </div>
  );
}

/* ────────────────────────── 新建之后的第 ② 步 ────────────────────────── */

/**
 * 「他现在登不进去」这块卡片是这一页最重要的一个 UI 决定。
 *
 * 它不是提示语，是一件**没做完的事**：新建只完成了一半，第二步不做那个人就永远进不来。
 * 所以它常驻在列表上方，直到那位管理员真的拿到绑定材料（或被停用）才消失。
 */
function NextStepCard({ admin, nextStep }: { admin: AdminAccount; nextStep: string | null }) {
  return (
    <Card className="border-l-4 border-l-warn">
      <h3 className="text-base font-semibold text-fg">
        下一步：{admin.email} 现在<strong className="text-warn"> 登不进去</strong>
      </h3>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        账号已创建（<code className="font-mono">#{admin.id}</code>），但他手上还没有任何验证器 ——
        而管理面的第二因子是<strong className="font-medium text-fg">强制</strong>的。
        请立刻在下面他的卡片里点「重置他的 TOTP」，拿到二维码 / secret，
        <strong className="font-medium text-fg">当面交给本人</strong>。
      </p>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <a
          href={`#admin-${admin.id}`}
          className="inline-flex min-h-11 items-center gap-2 rounded-lg bg-accent px-4 text-sm font-medium text-accent-fg hover:bg-accent-strong"
        >
          去给他生成绑定材料
        </a>
      </div>
      <p className="mt-3 font-mono text-[11px] leading-relaxed text-fg-subtle">
        {nextStep === null
          ? '服务端的 X-Next-Step 头这次没读到（跨域时它不在 CORS 暴露名单里）—— 不影响，下一步就是对同一个 id 跑 reset-totp。'
          : `服务端指定的下一步：${nextStep}`}
      </p>
    </Card>
  );
}

/**
 * 绑定材料。**明文只在这里出现一次**：不落库、不进日志、不进审计。
 *
 * 不生成二维码是有意的：后台不引第三方库（画二维码要么带一个依赖，要么手写一个编码器，
 * 而这两样都比「把 secret 念给对方」更容易出错）。真正要紧的是那句提醒 ——
 * **这串东西的唯一合法去处是那个人的验证器**，不是工单、不是聊天窗口、不是记事本。
 */
function EnrollmentCard({ enrollment, onDismiss }: { enrollment: Enrollment; onDismiss: () => void }) {
  return (
    <Card className="border-l-4 border-l-danger">
      <h3 className="text-base font-semibold text-fg">
        {enrollment.email} 的新绑定材料（<span className="text-danger">只显示这一次</span>）
      </h3>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        他的旧验证码<strong className="font-medium text-fg">已经失效了</strong>。
        把下面这串交到他手上、看着他在验证器里添加成功之后再关掉这块 ——
        关掉之后没有任何地方能再取到它（服务端不落明文），只能再重置一次。
      </p>

      <div className="mt-3 space-y-3">
        <CopyLine label="secret（手动添加用）" value={enrollment.data.secret} />
        <CopyLine label="otpauth:// URL（可粘进支持导入的验证器）" value={enrollment.data.otpauth_url} />
      </div>

      <p className="mt-3 rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-xs leading-relaxed text-danger">
        🔴 <strong className="font-semibold">不要把它贴进工单、聊天或邮件。</strong>
        它等价于这个管理员账号的第二因子 —— 贴出去之后，第二因子就不再是「他有什么」，
        而是「谁看过那条消息」。
      </p>

      <div className="mt-3">
        <Button onClick={onDismiss}>他已经绑好了，收起</Button>
      </div>
    </Card>
  );
}

/**
 * 一行可复制的机密串。
 *
 * `navigator.clipboard` 在非安全上下文里**不存在**，直接调用会抛 TypeError，
 * 按钮看起来像「点了没反应」。在这一块上这条失败路径格外贵：明文只出现一次，
 * 一次静默的复制失败 = 一把发不出去的钥匙。所以失败时明说「请手动选中」。
 */
function CopyLine({ label, value }: { label: string; value: string }) {
  const [state, setState] = useState<'idle' | 'ok' | 'manual'>('idle');
  return (
    <div className="min-w-0">
      <p className="text-xs font-medium text-fg-muted">{label}</p>
      <div className="mt-1 flex flex-wrap items-center gap-2">
        <code className="min-w-0 flex-1 overflow-x-auto rounded-lg border border-line bg-surface-alt px-3 py-2 font-mono text-xs break-all text-fg">
          {value}
        </code>
        <button
          type="button"
          onClick={() => {
            void navigator.clipboard
              ?.writeText(value)
              .then(() => setState('ok'))
              .catch(() => setState('manual'));
            if (!navigator.clipboard) setState('manual');
          }}
          className="inline-flex min-h-9 items-center rounded-lg border border-line bg-surface px-3 text-xs font-medium text-fg hover:bg-surface-alt"
        >
          复制
        </button>
        {state === 'ok' ? <span className="text-xs text-ok">已复制</span> : null}
        {state === 'manual' ? (
          <span className="text-xs text-warn">这个浏览器不让自动复制，请手动选中上面那串</span>
        ) : null}
      </div>
    </div>
  );
}

/* ────────────────────────────── 说明卡片 ────────────────────────────── */

function PermissionsCard() {
  return (
    <Card>
      <CardTitle>权限位</CardTitle>
      <p className="text-sm leading-relaxed text-fg-muted">
        完整 RBAC 不在第一阶段范围内，1–3 人团队可以只有一个角色。
        但 D6 那个权限位<strong className="font-medium text-fg">必须从第一天就存在</strong> ——
        它是全系统最大的内部欺诈面。
      </p>
      <ul className="mt-3 space-y-2 text-sm leading-relaxed text-fg-muted">
        <li>
          <code className="font-mono text-fg">admin.order.mark_paid</code>（D6）——
          有列、能看见，但<strong className="font-medium text-fg">现在授不了</strong>：
          ADR 0012 §16.3 裁决在带外留痕 sink 验证通过之前对所有人保持关闭。
        </li>
        <li>
          <code className="font-mono text-fg">admin.user.export</code>（D14）——
          唯一能通过这一页授予的权限位，默认不授予。
        </li>
        <li>
          <strong className="font-medium text-fg">D7 退款、D10 调余额</strong> ——
          库里有列（<code className="font-mono">perm_refund</code> /{' '}
          <code className="font-mono">perm_adjust_balance</code>），但契约的{' '}
          <code className="font-mono">AdminPermission</code> 枚举里没有对应值：
          <strong className="font-medium text-fg">
            这两个直接动钱的权限位，通过 API 既看不见也授不了
          </strong>
          ，只能由 DBA 改库。所以上面每个人的「权限位」那一列是
          <strong className="font-medium text-fg">不完整</strong>的 ——
          它没显示的，不等于他没有。
        </li>
        <li>
          <strong className="font-medium text-fg">角色（owner / admin / support）</strong> ——
          决定谁能碰这一页（新建 / 停用 / 重置 TOTP 都要 owner），但契约的{' '}
          <code className="font-mono">AdminAccount</code> 里没有这个字段，所以这一页显示不出来。
          不是 owner 的人会在提交时收到 403。
        </li>
      </ul>
    </Card>
  );
}

/** 自锁是这一页唯一「点一下就再也回不来」的后果，它值得一块独立的卡片。 */
function SelfLockCard() {
  return (
    <Card className="border-l-4 border-l-warn">
      <CardTitle>关于把自己锁在门外</CardTitle>
      <ul className="space-y-2 text-sm leading-relaxed text-fg-muted">
        <li>
          <strong className="font-medium text-fg">前端不知道哪一行是你自己</strong> ——
          契约里没有 <code className="font-mono">/admin/me</code>，
          <code className="font-mono">listAdmins</code> 也不指明「哪一行是我」。
          所以「停用自己」只会在服务端被拒（422），前端拦不住。
        </li>
        <li>
          <strong className="font-medium text-fg">这个列表只列在职的</strong>：
          停用过的管理员不会出现在这里（契约的{' '}
          <code className="font-mono">AdminAccount</code> 没有 disabled 字段，
          列出来会与在职的长得一模一样）。所以「他消失了」= 停用成功。
        </li>
        <li>
          <strong className="font-medium text-fg">没有撤销停用的入口</strong>，API 上没有，这一页也没有。
          停错了只能直接改库 —— 而直接改库正是权限系统存在的意义被绕过的那一刻。
        </li>
        <li>
          IAP 本身还有一条自我引用的失效模式：它要求 Google 身份，而 google.com 在大陆被封。
          <strong className="font-medium text-fg">服务出故障时，身处大陆的运维自己也进不了后台</strong> ——
          备用出网路径要提前准备并定期演练，这不是这一页能解决的，但它得被人记得。
        </li>
      </ul>
    </Card>
  );
}
