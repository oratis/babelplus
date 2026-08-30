/**
 * 模块 2 · 用户管理 `/admin/users` —— P1 / M3。**全系统最危险的模块**（page-inventory §4.2）。
 *
 * 三条「刻意不做」的能力，必须写在这里而不是被当成遗漏（§4.3）：
 *  ❌ 按用户查「访问了哪些网站」—— 不落目的地址日志。**后台不存在这个查询入口，
 *     这是刻意的，既是隐私承诺也是自我保护。**
 *  ❌ 以用户身份登录（impersonate）—— 一旦有这个按钮，管理员就能看到用户的订阅 token。
 *  ❌ 流量明细流水 —— 只存日 / 月聚合，查不到「某用户 14:32 用了多少」。
 *
 * # 接线后与脚手架时期的三处差别，每一处都是「说实话」
 *
 * 1. **搜索只按邮箱模糊匹配。** 脚手架里写着「搜索要支持邮箱精确匹配与邀请码反查」，
 *    而契约给 `listAdminUsers` 的 query 只有 `limit / cursor / count / q`，
 *    服务端把 `q` 翻成 `users.email ILIKE '%q%'`（`admin_users.go` 的 `adminUserSearchFilter`）——
 *    **既不是精确匹配，也没有邀请码反查**。界面必须把这句话说出来：
 *    否则「搜不到」会被读成「这个人不存在」，而真相可能是他用另一个邮箱注册的。
 * 2. **没有状态 / 套餐筛选。** SQL 里有 `banned` 与 `plan_id` 两个筛选位
 *    （`admin_accounts.sql` 的 ListAdminUsersPage），但 openapi 没有对应的 query 参数，
 *    handler 也就永远传不进去。**不在前端假装筛选** —— 对一份游标分页的列表做本地筛选，
 *    会得出「全系统只有 3 个被封的人」这种结论，而实际只是当前这一页里有 3 个。
 * 3. **订阅 token 一个字都不显示。** 这不需要前端自律：契约的 `AdminUser` **根本没有这个字段**
 *    （服务端的 `adminUserFromListRow` 连 uuid 都刻意不投影出来）。
 *    列表页因此不可能泄漏它 —— 这是 impersonate 之外的第二条泄漏路径被从**数据形状上**堵死。
 *
 * # 这个文件为什么装着一堆公共零件
 *
 * 模块 2 的两页（列表 / 详情）共用三态、错误文案、表格与表单原语。
 * 邻居们把这些放在 `*-common.tsx`（`ops-common` / `catalog-common` / `node-common`），
 * 而本轮**只允许改分配到的三个文件**，新开一个 `users-common.tsx` 不在授权内。
 * 所以它们先落在这里，由 `UserDetailPage.tsx` 直接 import。
 * TODO(P2)：接线全部完成、形态稳定后，把下面这一段整体上提为 `users-common.tsx`。
 */
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { Link } from 'react-router';
import { ApiError, unwrap, unwrapWithMeta, type Meta } from '@babelplus/shared/api';
import type { components } from '@babelplus/shared/api';
import { PageHeader } from '@babelplus/shared/ui';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  ErrorState,
  Icon,
  LinkButton,
  Skeleton,
  cx,
  formatBytes,
  formatDateTime,
} from './_imports.ts';
import { DangerousAction } from '../components/DangerousAction.tsx';
import { dangerOps } from '../lib/danger.ts';
import { api } from '../lib/api.ts';

/* ══════════════════════════ 模块 2 的公共零件 ══════════════════════════ */

export type AdminUser = components['schemas']['AdminUser'];
export type AdminUserPatch = components['schemas']['AdminUserPatch'];
export type ExportJob = components['schemas']['ExportJob'];
export type Wallet = components['schemas']['Wallet'];
export type RevokeAllResult = components['schemas']['RevokeAllResult'];

/** 任何 catch 到的东西 → `ApiError`。非 `ApiError` 也要有 `kind` 才能走统一文案。 */
export function asApiError(cause: unknown, fallbackMessage: string): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({ status: 0, code: 'UNKNOWN', message: fallbackMessage, cause });
}

/* ────────────────────────────── 请求三态 ────────────────────────────── */

export type QueryState = 'loading' | 'ready' | 'error';

export interface ApiQuery<T> {
  readonly state: QueryState;
  readonly data: T | null;
  readonly error: ApiError | null;
  /** 重新发一次。错误态的「重试」与写操作之后的刷新都用它。 */
  reload(): void;
}

/**
 * 一个请求 = 一套三态。**刻意不引 react-query** ——
 * `shared/api/client.ts` 的文件头写明缓存与状态管理的选型还没裁决（page-inventory §8），
 * 现在装一个等于替以后的人做决定。
 *
 * `run` 不要求调用方 memo：用 ref 取最新的那一份，重跑与否**只由 `deps` 决定**。
 * 要求 memo 的话每个调用点都得包一层 `useCallback`，而漏包的表现是**死循环请求** ——
 * 在后台这意味着一页打开就开始刷服务端，而 `db-f1-micro` 的连接池只有 25 条。
 */
export function useApiQuery<T>(
  run: () => Promise<T>,
  deps: readonly unknown[],
  fallbackMessage = '加载失败',
): ApiQuery<T> {
  const [nonce, setNonce] = useState(0);
  const [state, setState] = useState<QueryState>('loading');
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);

  const runRef = useRef(run);
  runRef.current = run;

  useEffect(() => {
    let alive = true;
    setState('loading');
    setError(null);
    void runRef
      .current()
      .then((value) => {
        // 迟到的响应不许覆盖新一轮的状态，所以先判 alive 再 set。
        if (!alive) return;
        setData(value);
        setState('ready');
      })
      .catch((cause: unknown) => {
        if (!alive) return;
        setData(null);
        setError(asApiError(cause, fallbackMessage));
        setState('error');
      });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  return { state, data, error, reload };
}

/* ────────────────────────── 501：这两页目前不该出现 ────────────────────────── */

/**
 * 501 的判据。
 *
 * `NOT_IMPLEMENTED` **不在 openapi 的 `ErrorCode` enum 里** —— 它由
 * `cmd/server/main.go` 的 `responseErrorHandler` 直接写出去（501 + 该码），所以只能按字符串比。
 * **不按状态码判**：「这一条还没写」与「这一条今天坏了」对运维是两句完全不同的话。
 *
 * ⚠️ 模块 2 的 8 个 operation **全部已实现**，所以正常部署下这条分支走不到。
 * 仍然处理它，因为它有一个真实的触发场景：**前端已经发版、后端还没滚上去**。
 * 那时若按「服务端出错了」显示，运维会去查后端日志，而那里什么异常都没有。
 */
export const NOT_IMPLEMENTED_CODE = 'NOT_IMPLEMENTED';

export function isNotImplemented(error: ApiError | null | undefined): boolean {
  return error?.code === NOT_IMPLEMENTED_CODE;
}

export function NotImplementedNotice({
  what,
  why,
  requestId,
}: {
  what: string;
  why: ReactNode;
  requestId?: string | undefined;
}) {
  return (
    <Card className="border-dashed">
      <h3 className="text-base font-semibold text-fg">尚未开放</h3>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        {what}的后端还没实现（服务端明确回 <code className="font-mono">501 NOT_IMPLEMENTED</code>）。
        这不是故障，重试也不会有变化。
      </p>
      <div className="mt-3 rounded-lg border border-line bg-surface-alt p-3 text-sm leading-relaxed text-fg-muted">
        {why}
      </div>
      {requestId ? <p className="mt-3 font-mono text-xs text-fg-subtle">请求号 {requestId}</p> : null}
    </Card>
  );
}

/* ────────────────────────── 读路径的错误文案 ────────────────────────── */

export interface ErrorCopy {
  readonly title: string;
  readonly description: string;
}

/**
 * `ErrorCode` → 文案。**按 code 分支，不按 HTTP 状态码分支**（api-contract §2.3）。
 *
 * ⚠️ 这是**读**路径的表。写路径（危险操作）用 `DangerousAction` 的 `dangerErrorCopy` ——
 * 同一个码在两条路径上要说的话不同：读到 403 是「你看不了」，写到 403 是「你改不了，去找人开权限」。
 */
export function usersErrorCopy(error: ApiError, fallbackTitle = '没能加载'): ErrorCopy {
  switch (error.code) {
    case NOT_IMPLEMENTED_CODE:
      return { title: '尚未开放', description: '这一条后端还没实现。重试也不会有变化。' };
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '这个账号看不了这一块',
        description:
          '身份是通过的（IAP 认下了你），缺的是角色或权限位。重新登录不会有帮助，需要另一个人给你开。',
      };
    case 'RESOURCE_NOT_FOUND':
      return {
        title: '找不到这个用户',
        description:
          'id 可能不对，也可能这个账号已经注销 —— 注销后剩下的是匿名化的壳，后台刻意查不到它（否则会让人以为「这个人还在」）。',
      };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return { title: '查询条件不合法', description: fieldReasons(error) ?? error.message };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '请求太频繁',
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
      // 🔴 后台站在 IAP 后面，跨域时 IAP 的拒绝在 JS 里与网络不可达**无法区分**
      //    （`shared/api/client.ts` 的 `detectEdgeRejection` 写明了这条限制）。
      //    所以这句必须把两种可能都说出来 —— 断言成「网络不好」会让人去查网络，
      //    而真正该做的是重新过一次 IAP。
      return {
        title: '请求没能到达服务端',
        description:
          '可能是网络不通，也可能是 IAP 会话过期后把请求挡在了应用之前（跨域时前端分不出这两者）。先在新标签页打开一次后台，确认 IAP 还认你。',
      };
    case 'server':
      return { title: '服务端出错了', description: '不是你的操作有问题。稍后再试，并把请求号一起报出来。' };
    case 'unauthorized':
      return { title: '管理面不认这个身份', description: '准入被拒。页面顶部的横幅里有该怎么处置的判断。' };
    default:
      return { title: fallbackTitle, description: error.message };
  }
}

function fieldReasons(error: ApiError): string | null {
  const details = error.details;
  if (!details || details.length === 0) return null;
  return details.map((d) => `${d.field}：${d.reason}`).join('；');
}

/** 读请求失败时的整块错误态。501 走 `NotImplementedNotice`，其余走全站统一的 `ErrorState`。 */
export function QueryErrorState({
  error,
  what,
  why,
  copy,
  onRetry,
}: {
  error: ApiError;
  what: string;
  why?: ReactNode;
  copy?: ErrorCopy;
  onRetry?: () => void;
}) {
  if (isNotImplemented(error)) {
    return (
      <NotImplementedNotice
        what={what}
        why={
          why ?? (
            <>
              模块 2 的 8 个 operation 在本轮<strong className="font-medium text-fg">已经全部实现</strong>，所以看到这一条多半是
              <strong className="font-medium text-fg">前端已发版而后端还没滚上去</strong>。
              先确认 API 的版本，再看后端日志（那里不会有异常）。
            </>
          )
        }
        requestId={error.requestId}
      />
    );
  }
  const resolved = copy ?? usersErrorCopy(error, `${what}没能加载`);
  return (
    <ErrorState
      kind={error.kind}
      title={resolved.title}
      description={resolved.description}
      requestId={error.requestId}
      onRetry={onRetry}
    />
  );
}

/* ────────────────────────── 页头与危险操作登记 ────────────────────────── */

/**
 * 已接线页面的页头。
 *
 * 🔴 **接好线的页面不能再用 `ModuleScaffold`。** 那个外壳里有一块「尚未接线」的声明，
 * 还有一个 `useShellState` 造出来的**假三态切换器** —— 页面真的接上 API 之后，
 * 前者在说假话，后者会让人以为自己点出来的空态是真的空。
 * 留着比删掉更糟，因为它看起来还挺勤勉。保留的只有优先级 / 移动端档位徽标。
 */
export function ModuleHeader({
  title,
  description,
  priority,
  mobile,
  actions,
  extraMeta,
}: {
  title: string;
  description: ReactNode;
  priority: 'P1' | 'P2' | 'P3';
  mobile: 'M2' | 'M3';
  actions?: ReactNode;
  extraMeta?: ReactNode;
}) {
  return (
    <PageHeader
      title={title}
      description={description}
      actions={actions}
      meta={
        <>
          <Badge tone={priority === 'P1' ? 'info' : 'neutral'}>{priority}</Badge>
          <Badge tone={mobile === 'M2' ? 'warn' : 'neutral'}>
            {mobile === 'M2' ? 'M2 · 手机上核心操作必须能完成' : 'M3 · 桌面优先，手机上可读即可'}
          </Badge>
          {extraMeta}
        </>
      }
    />
  );
}

/**
 * 本页涉及的危险操作登记（page-inventory §4.4 的逐字誊本，取自 `lib/danger.ts`）。
 *
 * 为什么接完线还要留着它：这张表是**操作者按下按钮之前**唯一能读到的「这一条为什么危险」。
 * `DangerousAction` 展开后也会显示同样的话，但那时人已经决定要做了 ——
 * 决定之前看到和决定之后看到，不是同一件事。
 */
export function DangerOpsNote({ codes }: { codes: readonly string[] }) {
  const ops = dangerOps(codes);
  if (ops.length === 0) return null;
  return (
    <ul className="mb-5 space-y-2">
      {ops.map((op) => (
        <li key={op.code} className="rounded-lg border border-danger/30 bg-danger/5 p-3 text-xs leading-relaxed">
          <div className="flex flex-wrap items-baseline gap-2">
            <span className="rounded bg-danger/15 px-1.5 py-0.5 font-mono font-semibold text-danger">{op.code}</span>
            <span className="font-medium text-fg">{op.title}</span>
          </div>
          <p className="mt-1 text-fg-muted">危害：{op.harm}</p>
          <p className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-fg-subtle">
            <span>审计（改前值 / 改后值）</span>
            {op.reason ? <span>必填原因</span> : null}
            {op.confirmString ? <span>🔒 输入{op.confirmString}</span> : null}
            {op.notify ? <span>📧 通知受影响用户</span> : null}
            {op.separatePerm ? <span>独立权限位（默认不授予）</span> : null}
          </p>
          {op.extra ? <p className="mt-1 text-fg-muted">额外：{op.extra}</p> : null}
        </li>
      ))}
    </ul>
  );
}

/* ────────────────────────────── 展示零件 ────────────────────────────── */

/**
 * 「这一格没有值」。
 *
 * 🔴 **`—` 与 `0` 是两件事。** 契约里 `AdminUser` 的流量 / 余额 / 配额全是**可选字段**，
 * 而「这个用户今天没跑流量」是有字段且等于 0。把缺字段渲染成 0 的后果是：
 * 某一天服务端少投影了一个字段，而后台平静地显示「已用 0 B」。
 */
export const MISSING = '—';

export function formatCount(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return MISSING;
  return n.toLocaleString('zh-CN');
}

/** 已用流量 = 上行 + 下行。任一缺席就整体作废 —— 半个和数比没有数更能骗人。 */
export function usedBytes(user: AdminUser): number | null {
  const up = user.upload_bytes;
  const down = user.download_bytes;
  if (up === undefined || down === undefined) return null;
  return up + down;
}

/** 「已用 / 配额」。配额缺席显示 `—`（**不是「不限」**：那是另一件事，见下面 quotaNote）。 */
export function trafficText(user: AdminUser): string {
  const used = usedBytes(user);
  const quota = user.transfer_enable_bytes;
  return `${used === null ? MISSING : formatBytes(used)} / ${quota === undefined ? MISSING : formatBytes(quota)}`;
}

export function isExpired(user: AdminUser, now = Date.now()): boolean {
  if (!user.expired_at) return false;
  const at = Date.parse(user.expired_at);
  return Number.isFinite(at) && at <= now;
}

/**
 * 状态徽标。
 *
 * 三件事**分开显示**，因为它们的处置完全不同：
 *  · 已封禁（D2 的结果，管理员做的）
 *  · 已过期（时间到了，用户自己续费就能解决）
 *  · 订阅已吊销（D3 的结果，用户所有设备失效但账号本身正常）
 * 合成一个「异常」会让工单第一句话变成「他到底怎么了」。
 */
export function UserStatusBadges({ user }: { user: AdminUser }) {
  const expired = isExpired(user);
  return (
    <span className="inline-flex flex-wrap gap-1">
      {user.banned ? <Badge tone="danger">已封禁</Badge> : null}
      {expired ? <Badge tone="warn">已过期</Badge> : null}
      {user.sub_revoked_at ? <Badge tone="warn">订阅已吊销</Badge> : null}
      {!user.banned && !expired && !user.sub_revoked_at ? <Badge tone="ok">正常</Badge> : null}
    </span>
  );
}

/* ────────────────────────────── 表格 ────────────────────────────── */

/**
 * 表格外壳。**横向滚动落在这一层**，不能让 body 横滚 ——
 * 后台是 M3（桌面优先、手机上可读即可），而「可读」的下限是不出现全页横滚。
 */
export function DataTable({ head, children }: { head: readonly string[]; children: ReactNode }) {
  return (
    <div className="-mx-4 overflow-x-auto sm:mx-0">
      <table className="w-full min-w-[52rem] border-collapse text-sm">
        <thead>
          <tr className="border-b border-line text-left text-xs font-medium text-fg-muted">
            {head.map((h) => (
              <th key={h} scope="col" className="px-3 py-2 whitespace-nowrap">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

export function Td({ children, className }: { children: ReactNode; className?: string }) {
  return <td className={cx('px-3 py-2 align-top', className)}>{children}</td>;
}

export function Tr({ children }: { children: ReactNode }) {
  return <tr className="border-b border-line/60 last:border-0">{children}</tr>;
}

/** 列表骨架。行数按真实页长给，撑出与加载完差不多的高度，避免内容一到就整页跳。 */
export function ListSkeleton({ rows = 6 }: { rows?: number }) {
  return (
    <div className="space-y-2" data-testid="list-skeleton">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-12 w-full" />
      ))}
    </div>
  );
}

/* ────────────────────────────── 表单原语 ────────────────────────────── */

const CONTROL =
  'w-full rounded-lg border border-line bg-surface px-3 text-base text-fg ' +
  'placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-2 ' +
  'focus-visible:outline-offset-1 focus-visible:outline-accent disabled:opacity-50';
// 16px 起步（`text-base`）：iOS Safari 在 <16px 的输入框获得焦点时会自动放大页面。
// 后台是 M3 桌面优先，但放大后 375px 布局出横滚这件事在哪一档都不该发生。

export function FieldShell({
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
    <div className="min-w-0">
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium text-fg">
        {label}
      </label>
      {children}
      {hint ? <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </div>
  );
}

export function TextField({
  id,
  label,
  value,
  onChange,
  hint,
  placeholder,
  type = 'text',
  mono,
  disabled,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (next: string) => void;
  hint?: ReactNode;
  placeholder?: string;
  type?: 'text' | 'number' | 'datetime-local' | 'email';
  mono?: boolean;
  disabled?: boolean;
}) {
  return (
    <FieldShell id={id} label={label} hint={hint}>
      <input
        id={id}
        name={id}
        type={type}
        value={value}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
        className={cx(CONTROL, 'min-h-11', mono ? 'font-mono' : null)}
      />
    </FieldShell>
  );
}

/** 十进制整数解析。空串 / 非整数 / 非有限数一律 `null`（= 不改这个字段）。 */
export function parseInteger(raw: string): number | null {
  const t = raw.trim();
  if (t === '') return null;
  if (!/^-?\d+$/.test(t)) return null;
  const n = Number(t);
  return Number.isSafeInteger(n) ? n : null;
}

/* ══════════════════════════ 页面 ══════════════════════════ */

/** 一页多少条。后台桌面优先（M3），一屏能看下 20 行。 */
const PAGE_SIZE = 20;

interface UsersPage {
  readonly data: readonly AdminUser[];
  readonly meta: Meta;
}

/**
 * 拉一页用户。
 *
 * ⚠️ **`count=true` 只在第一页要一次。** `COUNT(*)` 在 db-f1-micro（0.6 GiB RAM）上是
 * 实打实的开销（契约在 `CountQuery` 上逐字写了这条），不能让每次翻页都付。
 * 代价：翻页期间有人注册的话，「共 N 条」会短暂偏旧 —— 这个数字在后台的用途是估量级，不是对账。
 */
function listUsersPage(cursor: string | null, q: string, count: boolean): Promise<UsersPage> {
  const query: { limit: number; cursor?: string; count?: boolean; q?: string } = { limit: PAGE_SIZE };
  if (cursor !== null) query.cursor = cursor;
  if (count) query.count = true;
  // 空搜索不传 `q`：服务端对全空白的 q 也会归一成「不筛选」，但少发一个参数
  // 意味着**日志里能一眼看出这是不是一次搜索** —— 而全表 ILIKE 是要被注意到的。
  if (q.trim() !== '') query.q = q.trim();
  return unwrapWithMeta(api().GET('/api/v1/admin/users', { params: { query } }));
}

export default function UsersPage() {
  /**
   * 游标栈。
   *
   * 🔴 **游标分页没有「跳到第 7 页」。** 游标是「从这一行之后再取 N 条」，不是偏移量；
   * 「上一页」靠**记住来路**（压栈）实现，因为契约里没有反向游标。
   * 做成 `?page=7` 需要 OFFSET，而 OFFSET 在这个库上恰恰是要避开的东西。
   */
  const [stack, setStack] = useState<readonly (string | null)[]>([null]);
  const cursor = stack.length > 0 ? (stack[stack.length - 1] ?? null) : null;

  /** 已提交的搜索词（回车 / 点按钮才提交）。**不做输入即搜** —— 每一次都是一趟全表 ILIKE。 */
  const [submittedQ, setSubmittedQ] = useState('');
  const [draftQ, setDraftQ] = useState('');

  const page = useApiQuery(
    () => listUsersPage(cursor, submittedQ, cursor === null),
    [cursor, submittedQ],
    '用户列表加载失败',
  );

  const total = useRememberedTotal(page.data?.meta, submittedQ);
  const rows = page.data?.data ?? [];
  const nextCursor = page.data?.meta.next_cursor ?? null;
  const hasMore = page.data?.meta.has_more === true && nextCursor !== null;
  const searching = submittedQ.trim() !== '';

  function submitSearch(next: string): void {
    setSubmittedQ(next);
    // 改筛选条件必须回到第一页：旧游标在新条件下解出的是一段无意义的位置。
    setStack([null]);
  }

  return (
    <>
      <ModuleHeader
        title="用户"
        description="全系统最危险的模块。这里的每一次改动都直接影响钱或服务可用性。"
        priority="P1"
        mobile="M3"
        extraMeta={total === null ? null : <Badge>共 {total.toLocaleString('zh-CN')} 人</Badge>}
      />

      <DangerOpsNote codes={['D14']} />

      <div className="space-y-4">
        <ExportUsersCard />

        <Card>
          <CardTitle
            hint={
              page.state === 'ready'
                ? `本页 ${rows.length} 人${total === null ? '' : ` / 共 ${total.toLocaleString('zh-CN')} 人`}`
                : undefined
            }
          >
            用户列表
          </CardTitle>

          <SearchBar
            value={draftQ}
            onChange={setDraftQ}
            onSubmit={() => submitSearch(draftQ)}
            onClear={() => {
              setDraftQ('');
              submitSearch('');
            }}
            busy={page.state === 'loading'}
          />

          {page.state === 'loading' ? (
            <div className="mt-4">
              <ListSkeleton rows={6} />
            </div>
          ) : null}

          {page.state === 'error' && page.error ? (
            <div className="mt-4">
              <QueryErrorState error={page.error} what="用户列表" onRetry={page.reload} />
            </div>
          ) : null}

          {page.state === 'ready' && rows.length === 0 ? (
            <div className="mt-4">
              {searching ? (
                // 🔴 搜索无结果与「一个用户都没有」是两件事，混起来会让人得出错误结论。
                <EmptyState
                  title="没有匹配的用户"
                  description={
                    <>
                      搜索只按<strong className="font-medium text-fg">邮箱模糊匹配</strong>
                      （服务端是 <code className="font-mono">email ILIKE %关键词%</code>）。
                      它<strong className="font-medium text-fg">不能</strong>按邀请码反查，也不搜备注与 uuid ——
                      所以「搜不到」不等于「这个人不存在」。
                    </>
                  }
                  action={
                    <Button
                      tone="primary"
                      onClick={() => {
                        setDraftQ('');
                        submitSearch('');
                      }}
                    >
                      清空搜索
                    </Button>
                  }
                />
              ) : (
                <EmptyState
                  title="还没有用户"
                  description="邀请制注册，所以第一批用户需要先在「邀请与返佣」里发种子码。"
                  action={
                    <LinkButton tone="primary" href="/admin/invites">
                      去发邀请码 <Icon.ArrowRight size={14} />
                    </LinkButton>
                  }
                />
              )}
            </div>
          ) : null}

          {page.state === 'ready' && rows.length > 0 ? (
            <>
              <div className="mt-4">
                <UsersTable rows={rows} />
              </div>
              <div className="mt-4 flex flex-wrap items-center justify-between gap-3 text-sm text-fg-muted">
                <span>
                  第 {stack.length} 页
                  {total === null ? null : <> · 共 {total.toLocaleString('zh-CN')} 人</>}
                </span>
                <span className="flex gap-2">
                  <Button onClick={() => setStack((s) => (s.length > 1 ? s.slice(0, -1) : s))} disabled={stack.length === 1}>
                    上一页
                  </Button>
                  <Button
                    onClick={() => {
                      // 🔴 判据是 `next_cursor` 而不是「这一页够不够 20 条」：
                      //    最后一页恰好 20 条时，按条数判会给出一个点不动的「下一页」。
                      if (nextCursor !== null) setStack((s) => [...s, nextCursor]);
                    }}
                    disabled={!hasMore}
                  >
                    下一页
                  </Button>
                </span>
              </div>
            </>
          ) : null}
        </Card>

        <SearchLimitsCard />
        <ForbiddenQueriesCard />
      </div>
    </>
  );
}

/**
 * 记住第一页那次 `count=true` 拿到的总数。
 *
 * 翻到第二页后 `meta.total` 就没有了（我们不再要它），但页头还要显示。
 * 换搜索词时必须清掉：上一个词的总数配在新词的列表上，是一句确凿的假话。
 */
function useRememberedTotal(meta: Meta | undefined, resetKey: string): number | null {
  const [total, setTotal] = useState<number | null>(null);
  const seen = meta?.total;
  // 顺序有意义：先清（换了搜索词），再写（新一页的总数）。反过来的话，
  // 两个 effect 在同一次提交里跑时会把刚拿到的总数清成 null。
  useEffect(() => {
    setTotal(null);
  }, [resetKey]);
  useEffect(() => {
    if (typeof seen === 'number') setTotal(seen);
  }, [seen]);
  return total;
}

function SearchBar({
  value,
  onChange,
  onSubmit,
  onClear,
  busy,
}: {
  value: string;
  onChange: (next: string) => void;
  onSubmit: () => void;
  onClear: () => void;
  busy: boolean;
}) {
  const id = useId();
  return (
    <form
      className="mt-3 flex flex-wrap items-end gap-2"
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit();
      }}
    >
      <div className="min-w-0 flex-1 basis-64">
        <TextField
          id={`${id}-q`}
          label="搜索邮箱"
          value={value}
          onChange={onChange}
          placeholder="例如 someone@example.com 或 example.com"
          hint={
            <>
              服务端做的是 <code className="font-mono">email ILIKE %关键词%</code>：
              <strong className="font-medium text-fg">模糊、不区分大小写、只匹配邮箱</strong>。
              <code className="font-mono">%</code> 与 <code className="font-mono">_</code>{' '}
              会被转义成普通字符，所以输入 <code className="font-mono">%</code> 不会列出全部人。
            </>
          }
        />
      </div>
      <div className="flex gap-2 pb-6">
        <Button type="submit" tone="primary" disabled={busy}>
          搜索
        </Button>
        <Button type="button" onClick={onClear} disabled={busy && value === ''}>
          清空
        </Button>
      </div>
    </form>
  );
}

function UsersTable({ rows }: { rows: readonly AdminUser[] }) {
  return (
    <DataTable head={['邮箱', '套餐', '流量（已用 / 配额）', '到期', '设备数', '状态', '注册时间', '邀请人']}>
      {rows.map((u) => (
        <Tr key={u.id}>
          <Td>
            <Link
              to={`/admin/users/${u.id}`}
              className="font-medium text-accent underline-offset-4 hover:underline"
            >
              {u.email}
            </Link>
            <span className="mt-0.5 block font-mono text-xs text-fg-subtle">#{u.id}</span>
          </Td>
          <Td>{u.plan_name ?? MISSING}</Td>
          <Td className="whitespace-nowrap">{trafficText(u)}</Td>
          {/* 🔴 这两格的「没有值」是**有意义的值**，不是缺数据：
              `expired_at` 为空 = 不限时，`device_limit` 为空 = 不限设备。
              渲染成 `—` 会让人以为「这个人的到期时间没读出来」，
              而它与详情页说的话也会对不上（那边写的是「不限时」）。 */}
          <Td className="whitespace-nowrap">{u.expired_at ? formatDateTime(u.expired_at) : '不限时'}</Td>
          <Td>{u.device_limit === undefined ? '不限' : formatCount(u.device_limit)}</Td>
          <Td>
            <UserStatusBadges user={u} />
          </Td>
          <Td className="whitespace-nowrap">{formatDateTime(u.created_at)}</Td>
          <Td>
            {u.invited_by_user_id === undefined ? (
              MISSING
            ) : (
              <Link
                to={`/admin/users/${u.invited_by_user_id}`}
                className="font-mono text-xs text-accent underline-offset-4 hover:underline"
              >
                #{u.invited_by_user_id}
              </Link>
            )}
          </Td>
        </Tr>
      ))}
    </DataTable>
  );
}

/* ────────────────────────── D14 导出 CSV ────────────────────────── */

/**
 * D14：导出用户 CSV。
 *
 * 三件必须让操作者在点之前就知道的事，全部写在 `context` 里：
 *
 * 1. **导出是全量的，没有任何筛选参数。** 契约给这个端点的请求体只有 `reason`，
 *    所以搜索框里的关键词**不会**带进导出 —— 一份「我筛过了」的心理预期会让人
 *    低估这份文件的敏感度。
 * 2. **命中 5 万行上限时服务端 422 拒绝，不发半份 CSV。** 见下面 EXPORT_CAP 的注释。
 * 3. **5/h 限流，按 admin_id 计**（换网络绕不过）。
 */
const EXPORT_CAP = 50000;

function ExportUsersCard() {
  const [job, setJob] = useState<ExportJob | null>(null);

  return (
    <Card>
      <CardTitle hint="exportAdminUsers · D14">导出用户 CSV</CardTitle>

      <DangerousAction
        code="D14"
        submitLabel="生成 CSV"
        // 登记表里 D14 没有勾「必填原因」，但服务端**要**（`ExportAdminUsers` 的 L2 分支，
        // ≥ 8 字符）。这里显式加上：少了它，操作者会在填完之后吃一个 422，
        // 而那条 422 的文案讲的是「原因太短」——他会以为自己写了原因。
        requireReason
        permissionName="admin.user.export"
        context={
          <>
            <p>
              <strong className="font-medium text-fg">这是一次全量导出</strong>：契约给这个端点的请求体只有
              <code className="font-mono"> reason </code>
              一个字段，<strong className="font-medium text-fg">没有任何筛选参数</strong> ——
              上面搜索框里的关键词不会带进来。导出的是全部未注销用户的邮箱与配额。
            </p>
            <p className="mt-2">
              超过 {EXPORT_CAP.toLocaleString('zh-CN')} 行时服务端
              <strong className="font-medium text-fg">直接拒绝（422），不会发一份被截断的 CSV</strong>。
              理由：一份静默截断的名单会被当成完整名单去做运营决策，而「名单里没有他」与「名单被截断了」
              在事后不可区分。真到了那一天，需要先落地异步导出，前端这里没有绕过办法。
            </p>
            <p className="mt-2">
              限流 <strong className="font-medium text-fg">5 次 / 小时</strong>，按管理员账号计（换个网络绕不过）。
              审计会记下：谁、何时、多少行、是否下发。
            </p>
          </>
        }
        onSubmit={async ({ reason }) => {
          const created = await unwrap(
            api().POST('/api/v1/admin/users/export', { body: { reason: reason ?? '' } }),
          );
          setJob(created);
        }}
      />

      {job ? <ExportResult job={job} onDismiss={() => setJob(null)} /> : null}
    </Card>
  );
}

/**
 * 导出结果。
 *
 * ⚠️ **`download_url` 是一条 `data:` URI，不是一个可以稍后再来取的链接。**
 * 服务端没有 export_jobs 表也没有对象存储（handler 里逐字写了这条偏差），
 * 整份 CSV 就在这个字符串里 —— 关掉这一页它就没了，得重新导一次（并再吃一次限流额度）。
 * 所以这里既给下载按钮，也把「现在就存下来」说出口。
 */
function ExportResult({ job, onDismiss }: { job: ExportJob; onDismiss: () => void }) {
  const url = job.download_url;
  return (
    <div className="mt-4 rounded-lg border border-warn/40 bg-warn/5 p-3 text-sm leading-relaxed">
      <p className="font-medium text-fg">
        导出已生成（任务 <code className="font-mono text-xs">{job.id}</code>，状态 {job.status}）
      </p>
      {url ? (
        <>
          <p className="mt-1 text-fg-muted">
            文件就在这个页面里（服务端用 <code className="font-mono">data:</code> URI 直接把 CSV 带回来了，
            没有可以稍后再取的下载地址）。
            <strong className="font-medium text-fg"> 现在就存下来，关掉页面它就没了。</strong>
          </p>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <a
              href={url}
              download="babelplus-users.csv"
              className="inline-flex min-h-11 items-center gap-2 rounded-lg bg-accent px-4 text-sm font-medium text-accent-fg hover:bg-accent-strong"
            >
              下载 CSV
            </a>
            <Button tone="ghost" onClick={onDismiss}>
              收起
            </Button>
          </div>
          <p className="mt-2 text-xs text-fg-subtle">
            这份文件里是<strong className="font-medium text-fg">全部用户的邮箱</strong>。一旦落到磁盘上，它就脱离了审计范围 ——
            审计能证明你导出过，证明不了它之后去了哪里。
          </p>
        </>
      ) : (
        <p className="mt-1 text-fg-muted">
          服务端没有给下载地址（<code className="font-mono">download_url</code> 为空）。
          这与契约声明的异步任务形态一致，但当前实现是同步返回文件的 —— 若持续如此，去看服务端日志。
        </p>
      )}
    </div>
  );
}

/* ────────────────────────── 两块「说实话」的卡片 ────────────────────────── */

/** 搜索与筛选的真实能力边界。不写出来的话，「筛出 0 条」会被读成「没有这样的人」。 */
function SearchLimitsCard() {
  return (
    <Card>
      <CardTitle>这个列表能筛什么、不能筛什么</CardTitle>
      <ul className="space-y-2 text-sm leading-relaxed text-fg-muted">
        <li>
          ✅ <strong className="font-medium text-fg">邮箱模糊匹配</strong> —— 唯一的筛选维度。
          服务端把关键词转义后做 <code className="font-mono">ILIKE %kw%</code>，
          <code className="font-mono">%</code> / <code className="font-mono">_</code> / <code className="font-mono">\</code>{' '}
          都是普通字符。
        </li>
        <li>
          ❌ <strong className="font-medium text-fg">邮箱精确匹配</strong> ——
          契约没有这个参数。搜 <code className="font-mono">a@b.com</code> 也会带出{' '}
          <code className="font-mono">xa@b.com</code>。
        </li>
        <li>
          ❌ <strong className="font-medium text-fg">按邀请码反查</strong> ——
          契约没有这个参数（脚手架里曾把它列为 TODO）。列表里的「邀请人」是<strong className="font-medium text-fg">用户 id</strong>，
          从邀请码找人要去「邀请与返佣」那一页。
        </li>
        <li>
          ❌ <strong className="font-medium text-fg">按状态 / 套餐筛选</strong> ——
          SQL 里有这两个筛选位，但 openapi 没有对应的 query 参数，服务端传不进去。
          <strong className="font-medium text-fg">这里也不做本地筛选</strong>：
          对一份游标分页的列表做本地筛选，会得出「全系统只有 3 个被封的人」这种结论，
          而实际只是当前这一页里有 3 个。
        </li>
      </ul>
    </Card>
  );
}

/** 「不做」的能力要在 UI 上被看见，否则半年后会有人当成遗漏补上去。 */
function ForbiddenQueriesCard() {
  return (
    <Card>
      <CardTitle>这里刻意查不到的三件事</CardTitle>
      <ul className="space-y-2 text-sm leading-relaxed text-fg-muted">
        <li>
          <strong className="font-medium text-fg">用户访问了哪些网站</strong> ——
          我们不落目的地址日志。这个查询入口不存在，既是对用户的隐私承诺，也是对我们自己的保护。
        </li>
        <li>
          <strong className="font-medium text-fg">以用户身份登录</strong> ——
          一旦有这个按钮，管理员就能看到用户的订阅 token。第一阶段不做；
          将来若要做，必须审计 + 用户侧可见记录。
        </li>
        <li>
          <strong className="font-medium text-fg">流量明细流水</strong> ——
          只存日 / 月聚合（落明细是这个业务的性能命门）。查不到「某用户 14:32 用了多少」。
        </li>
      </ul>
      <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
        顺带一条不需要自律的：<strong className="font-medium text-fg">订阅 token 不在这一页的任何地方</strong> ——
        契约的 <code className="font-mono">AdminUser</code> 根本没有那个字段（连 uuid 都没有），
        所以它不是「我们记得别显示」，而是数据形状上就拿不到。
      </p>
    </Card>
  );
}
