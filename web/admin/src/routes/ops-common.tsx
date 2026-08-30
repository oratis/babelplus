/**
 * 后台「看板 / 统计 / 审计 / 设置 / 域名」五页接线时共用的小零件。
 *
 * 为什么新开一个文件，而不是放进 `@babelplus/shared/src/ui` 或 `admin/src/components`：
 * 后台 17 个模块正在**并行**接线，公共目录是所有人都要改的文件，动它必然撞车。
 * 这里的每一样都只服务于这五页，等全部接线完、形态稳定了再决定要不要上提。
 * （用户面板走过同一条路：`user/src/routes/ticket-common.tsx` 与 `subscribe/_shared.tsx`。）
 *
 * 🔴 **刻意不放各页的文案表。** 按 `ErrorCode` 分支的文案是**产品内容**，
 * 每页说法不同（「审计日志没能加载」与「配置没能保存」不是一句话）。
 * 合成一张表的代价是「为了复用而把话说得更含糊」，而含糊的错误提示等于没有提示。
 * 这里只放五页真正一模一样的那部分：三态、501 的呈现、兜底文案。
 *
 * ⚠️ **写操作的错误文案不在这里** —— 危险操作走 `components/DangerousAction.tsx`
 * 的 `dangerErrorCopy`。同一个码在读路径与写路径上要说的话不同：
 * 读到 403 是「你看不了」，写到 403 是「你改不了，去找人开权限」。
 */
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { ApiError } from '@babelplus/shared/api';
import { Button, Card, ErrorState, Skeleton, cx } from '@babelplus/shared/ui';
import { runtimeConfig } from '@babelplus/shared';

/* ────────────────────────────── 请求三态 ────────────────────────────── */

export type OpsQueryState = 'loading' | 'ready' | 'error';

export interface OpsQuery<T> {
  readonly state: OpsQueryState;
  readonly data: T | null;
  readonly error: ApiError | null;
  /** 重新拉一次。错误态的「重试」与写操作成功后的刷新都用它。 */
  reload(): void;
  /**
   * 拿服务端刚返回的实体**就地替换**，不重新发请求。
   *
   * 存在的理由是三态纪律：`PATCH /admin/settings` 成功后返回的就是**全量新配置**，
   * 再发一次 GET 既多一次往返，又会把页面打回骨架屏 ——
   * 操作者刚点完保存，眼前的配置突然消失，第一反应是「是不是没存上」。
   */
  replace(next: T): void;
}

/**
 * 最小的「拉一次数据」hook。
 *
 * 刻意不引 react-query：`shared/api/client.ts` 的文件头写明缓存与状态管理的选型
 * **还没裁决**（page-inventory §8），现在装一个等于替以后的人做决定。
 * 这里只做四件必须做的事：三态、卸载后不 setState、可重拉、可就地替换。
 *
 * `load` 不要求调用方 memo：用 ref 取最新的那一份，重跑与否**只由 `deps` 决定**。
 * 要求 memo 的话每个调用点都得包一层 `useCallback`，而漏包的表现是**死循环请求** ——
 * 在后台这意味着一页打开就开始刷服务端，而 `db-f1-micro` 的连接池只有 2 条。
 */
export function useOpsQuery<T>(
  load: () => Promise<T>,
  deps: readonly unknown[] = [],
  fallbackMessage = '加载失败',
): OpsQuery<T> {
  const [nonce, setNonce] = useState(0);
  const [state, setState] = useState<OpsQueryState>('loading');
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);

  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    let alive = true;
    setState('loading');
    setError(null);
    void loadRef
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
        setError(toApiErrorLike(cause, fallbackMessage));
        setState('error');
      });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  const replace = useCallback((next: T) => {
    setData(next);
    setState('ready');
    setError(null);
  }, []);

  return { state, data, error, reload, replace };
}

/** 任何 catch 到的东西 → `ApiError`。非 `ApiError` 也要有 `kind` 才能走统一文案。 */
export function toApiErrorLike(cause: unknown, message = '请求失败'): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({ status: 0, code: 'UNKNOWN', message, cause });
}

/* ────────────────────────────── 501 ────────────────────────────── */

/**
 * 501 的判据。
 *
 * `NOT_IMPLEMENTED` **不在 openapi 的 `ErrorCode` enum 里** —— 它由
 * `api/cmd/server/main.go` 的 `responseErrorHandler` 直接写出去（501 + 该码），
 * 所以只能按字符串比。**不按状态码判**：将来真的出现一个「实现了但依赖挂了」的 501 时，
 * 「这一条还没写」与「这一条今天坏了」对运维是两句完全不同的话。
 */
export const NOT_IMPLEMENTED_CODE = 'NOT_IMPLEMENTED';

export function isNotImplemented(error: ApiError | null | undefined): boolean {
  return error?.code === NOT_IMPLEMENTED_CODE;
}

/**
 * 501 专用的提示块。
 *
 * **刻意不用 `ErrorState`**：501 归一后的 `kind` 是 `server`，而 `ErrorState` 在 server 态下
 * 会说「我们这边出了问题」并把人推去状态页 —— 状态页上一切正常，运维只会更困惑。
 * 「还没做」不是故障，红色警告框在这里是误报。
 *
 * 🔴 `why` 是**必填**。一句光秃秃的「尚未开放」会让人以为是排期问题，
 * 于是每周来点一次看看好了没有。后台这几条 501 卡的是**表不存在 / 裁决没做**，
 * 说清楚卡在哪，读的人才知道该去推动什么。
 */
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

/* ────────────────────────────── 读路径的错误文案 ────────────────────────────── */

export interface OpsErrorCopy {
  readonly title: string;
  readonly description: string;
}

/**
 * 读请求失败的兜底文案。**按 `ErrorCode` 分支，不按 HTTP 状态码分支**（api-contract §2.3）。
 *
 * 页面自己的表先分支，剩下的才落到这里 —— 所以这里只写「在任何一页上说法都一样」的那几条。
 */
export function opsErrorCopy(error: ApiError, fallbackTitle = '没能加载'): OpsErrorCopy {
  switch (error.code) {
    case NOT_IMPLEMENTED_CODE:
      return {
        title: '尚未开放',
        description: '这一条后端还没实现。不是你的操作有问题，重试也不会有变化。',
      };
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '这个账号看不了这一块',
        description:
          '身份是通过的（IAP 认下了你），缺的是角色或权限位。重新登录不会有帮助，需要另一个人给你开。',
      };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      // ⚠️ 统计端点的参数错误会以 **500 + VALIDATION_FAILED** 出现：
      //    契约给 getAdminStats 只声明了 403/500，服务端只能用 500 承载参数错误
      //    （handler 的 `statsBadParam`，缺口已登记）。按 code 分支才认得出来，
      //    按状态码分支会把「时间跨度填太长」说成「服务端炸了」。
      return { title: '查询条件不合法', description: fieldReasons(error) ?? error.message };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '请求太频繁',
        description:
          error.retryAfterSeconds === undefined
            ? '稍后再试。'
            : `${error.retryAfterSeconds} 秒后可以再试。`,
      };
    case 'RESOURCE_NOT_FOUND':
      return { title: '找不到这个对象', description: '它可能刚被别人改过或删掉了。' };
    default:
      break;
  }
  switch (error.kind) {
    case 'offline':
      // 🔴 后台站在 IAP 后面，跨域时 IAP 的拒绝在 JS 里与网络不可达**无法区分**
      //    （`shared/api/client.ts` 的 `detectEdgeRejection` 写明了这条限制）。
      //    所以这句话必须把两种可能都说出来，不能断言其中一种 ——
      //    断言成「网络不好」的后果是运维去查网络，而真正该做的是重新过一次 IAP。
      return {
        title: '请求没能到达服务端',
        description:
          '可能是网络不通，也可能是 IAP 会话过期后把请求挡在了应用之前（跨域时前端分不出这两者）。先在新标签页打开一次后台确认 IAP 还认你。',
      };
    case 'server':
      return { title: '服务端出错了', description: '不是你的操作有问题。稍后再试，并把请求号一起报出来。' };
    case 'unauthorized':
      return {
        title: '管理面不认这个身份',
        description: '准入被拒。页面顶部的横幅里有该怎么处置的判断。',
      };
    default:
      return { title: fallbackTitle, description: error.message };
  }
}

function fieldReasons(error: ApiError): string | null {
  const details = error.details;
  if (!details || details.length === 0) return null;
  return details.map((d) => `${d.field}：${d.reason}`).join('；');
}

/**
 * 读请求失败时的整块错误态。501 走 `NotImplementedNotice`，其余走全站统一的 `ErrorState`。
 *
 * `copy` 让调用页覆盖标题与说明（每页的说法不同），不传就用兜底表。
 */
export function QueryErrorState({
  error,
  what,
  why,
  copy,
  onRetry,
}: {
  error: ApiError;
  /** 「审计日志」「流量统计」这种名词，进文案。 */
  what: string;
  /** 501 时说明卡在哪。不传就说一句通用的。 */
  why?: ReactNode;
  copy?: OpsErrorCopy;
  onRetry?: () => void;
}) {
  if (isNotImplemented(error)) {
    return (
      <NotImplementedNotice
        what={what}
        why={why ?? '这一条挂在一个还没落地的依赖上。具体卡在哪，去 api-contract §14 的缺口清单查。'}
        requestId={error.requestId}
      />
    );
  }
  const resolved = copy ?? opsErrorCopy(error, `${what}没能加载`);
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

/* ────────────────────────────── 骨架 ────────────────────────────── */

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

/* ────────────────────────────── 筛选原语 ────────────────────────────── */

const CONTROL =
  'w-full rounded-lg border border-line bg-surface px-3 text-base text-fg ' +
  'placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-2 ' +
  'focus-visible:outline-offset-1 focus-visible:outline-accent disabled:opacity-50';
// 16px 起步（`text-base`）：iOS Safari 在 <16px 的输入框获得焦点时会自动放大页面。
// 后台大多是 M3 桌面优先，但同一套控件会被 M2 的页面复用，索性统一。

export function FilterField({
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

export function FilterText({
  id,
  label,
  value,
  onChange,
  hint,
  placeholder,
  type = 'text',
}: {
  id: string;
  label: string;
  value: string;
  onChange: (next: string) => void;
  hint?: ReactNode;
  placeholder?: string;
  type?: 'text' | 'date' | 'number';
}) {
  return (
    <FilterField id={id} label={label} hint={hint}>
      <input
        id={id}
        name={id}
        type={type}
        value={value}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        onChange={(event) => onChange(event.target.value)}
        className={cx(CONTROL, 'min-h-11')}
      />
    </FilterField>
  );
}

export function FilterSelect<T extends string>({
  id,
  label,
  value,
  options,
  onChange,
  hint,
}: {
  id: string;
  label: string;
  value: T;
  options: ReadonlyArray<{ readonly value: T; readonly label: string }>;
  onChange: (next: T) => void;
  hint?: ReactNode;
}) {
  return (
    <FilterField id={id} label={label} hint={hint}>
      <select
        id={id}
        name={id}
        value={value}
        onChange={(event) => onChange(event.target.value as T)}
        className={cx(CONTROL, 'min-h-11')}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </FilterField>
  );
}

/* ────────────────────────────── 杂项 ────────────────────────────── */

/**
 * 「这一格没有值」。
 *
 * 🔴 **`—` 与 `0` 是两件事，任何一页都不许把它们混为一谈。**
 * 看板的五条查询是并发取的，任一条失败时那一格**没有字段**（契约里 `AdminDashboard`
 * 的字段全是可选的），而「今天没有收入」是**有字段且等于 0**。
 * 把缺字段渲染成 0 的后果是：数据库挂了半天，而看板一直平静地显示「今日收入 ¥0.00」。
 */
export const MISSING = '—';

/** 整数计数的展示。`undefined` → `—`，`0` → `0`。 */
export function formatCount(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return MISSING;
  return n.toLocaleString('zh-CN');
}

/**
 * JSON 值 → 展示串。审计的 before/after 快照与配置项的值共用。
 *
 * 循环引用与 `undefined` 都不该出现（两边都来自 `JSON.parse`），但真出现时
 * 宁可显示一个记号也不要让整页白屏 —— 审计页是**事后复盘**用的，
 * 一条坏记录不能把其余几万条一起带走。
 */
export function formatJsonValue(value: unknown): string {
  if (value === undefined) return MISSING;
  try {
    return JSON.stringify(value, null, 2) ?? String(value);
  } catch {
    return '（无法序列化的值）';
  }
}

/** 只读的 JSON 展示块。宽内容自己横向滚动，不让整页出现横向滚动条。 */
export function JsonBlock({ value, label }: { value: unknown; label?: string }) {
  return (
    <div className="min-w-0">
      {label ? <p className="mb-1 text-xs font-medium text-fg-muted">{label}</p> : null}
      <pre className="max-h-64 overflow-auto rounded-lg border border-line bg-surface-alt p-2 font-mono text-xs leading-relaxed text-fg">
        {formatJsonValue(value)}
      </pre>
    </div>
  );
}

/**
 * 「这一块的结论只能到这个程度」的说明条。
 *
 * 后台有好几处**必须**说明自己看到的不是全部事实：探活是境外视角、
 * 统计被截断到 5000 行、审计的 `request_id` 没落库。
 * 这些话不写出来，读的人会默认屏幕上的就是全部 —— 而那正是误判的开始。
 */
export function CaveatNotice({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-warn/30 bg-warn/5 p-3 text-xs leading-relaxed text-fg-muted">
      {children}
    </div>
  );
}

/** 支持邮箱（失联时唯一不依赖本服务的通道，ADR 0002 §1）。取不到就不显示，不编。 */
export function SupportHint() {
  const email = runtimeConfig().supportEmail;
  if (!email) return null;
  return (
    <p className="text-xs text-fg-subtle">
      需要人工介入时：<span className="font-mono text-fg-muted">{email}</span>
    </p>
  );
}

/** 「重试」按钮。错误态之外的地方（例如局部失败）也要能重来一次。 */
export function RetryButton({ onClick, children = '重试' }: { onClick: () => void; children?: ReactNode }) {
  return (
    <Button tone="default" onClick={onClick}>
      {children}
    </Button>
  );
}
