/**
 * 「目录与运营」四页（套餐 · 优惠码 · 公告 · 邀请）共用的东西。
 *
 * 为什么单开一个文件，而不是塞进 `@babelplus/shared/src/ui` 或者复制四份：
 *
 *  - `shared/ui` 是三个前端的公共资产，多个人同时接线会撞在同一个文件上；
 *    而这里的每一样都带着**这四页特有的产品约束**（游标分页要带总数、
 *    危险操作的业务字段与四层参数分开收），拿到别的页面上既用不着也会误导。
 *  - 复制四份的代价不是行数，是**漂移**：四页各写一份 `ErrorCode` 文案表之后，
 *    同一个 `NOT_IMPLEMENTED` 会在四页上说四句不同的话，而没有任何东西会报错。
 *
 * 分组与后端 `api/internal/handler/admin_catalog.go` 一致（同一组端点、同一批约束），
 * 这样「服务端改了什么」与「前端要跟着改哪里」是同一个边界。
 *
 * ⚠️ **不跨包 import 用户面板。** 用户面 `routes/ticket-common.tsx` 里有形状相近的
 * `useApiQuery` / `QueryErrorState`，这里是照着它的思路重写的一份，不是 import ——
 * 两个 SPA 是两套故障域（`lib/api.ts` 文件头），共用一份代码就等于把它们焊在一起。
 */
import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from 'react';
import { ApiError, type Meta, type components } from '@babelplus/shared/api';
import { runtimeConfig } from '@babelplus/shared';
import { PageHeader } from '@babelplus/shared/ui';
import { Badge, Button, Card, ErrorState, LoadingState, SkeletonCard, cx, formatCny } from './_imports.ts';
import { asApiError } from '../lib/auth.tsx';
import { dangerOps } from '../lib/danger.ts';
// `_imports.ts` 只转发 UI 与格式化，`runtimeConfig` 不在里面 —— 直接从 shared 取，
// 不去改那个文件：它是四页共用的转发出口，同时接线的人都会碰到它。

/* ────────────────────────── 契约类型（以 schema.d.ts 为准） ────────────────────────── */

export type Plan = components['schemas']['Plan'];
export type PlanUpsert = components['schemas']['PlanUpsert'];
export type PlanPrice = components['schemas']['PlanPrice'];
export type Coupon = components['schemas']['Coupon'];
export type CouponUpsert = components['schemas']['CouponUpsert'];
export type Notice = components['schemas']['Notice'];
export type NoticeUpsert = components['schemas']['NoticeUpsert'];
export type InviteCode = components['schemas']['InviteCode'];
export type Commission = components['schemas']['Commission'];

export type { Meta };

/* ────────────────────────── 请求的三态 ────────────────────────── */

export type QueryState = 'loading' | 'ready' | 'error';

export interface ApiQuery<T> {
  readonly state: QueryState;
  readonly data: T | null;
  readonly error: ApiError | null;
  /** 重新发一次。错误态的「重试」按钮、以及写操作成功后的刷新都用它。 */
  reload(): void;
}

/**
 * 一个请求 = 一套三态。**刻意不做全局缓存层** ——
 * `shared/api/queries.ts` 的文件头写了缓存与状态管理的选型还没裁决，
 * 在这里引一个等于替以后的人做决定。
 *
 * `run` 不要求调用方 memo：用 ref 取最新的那一份，重跑与否只由 `deps` 决定。
 * 要求 memo 的话，每个调用点都得包一层 `useCallback`，而漏包的表现是**死循环请求**。
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
        if (!alive) return;
        setData(value);
        setState('ready');
      })
      .catch((cause: unknown) => {
        if (!alive) return;
        // 迟到的响应不许覆盖新一轮的状态，所以先判 alive 再 set。
        setError(asApiError(cause, fallbackMessage));
        setState('error');
      });
    return () => {
      alive = false;
    };
  }, [...deps, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  return { state, data, error, reload };
}

/* ────────────────────────── 错误分支（按 code，不按状态码） ────────────────────────── */

/**
 * 501 的判据。
 *
 * `NOT_IMPLEMENTED` **不在 openapi 的 `ErrorCode` enum 里**（错误映射层直接写出去的），
 * 所以只能按字符串比。按状态码判也行，但那会把将来任何一个真的 501 也算进来 ——
 * 而「端点还没写」与「端点写了但依赖挂了」对操作者是两句不同的话：
 * 前者重试一万次也没用，后者过五分钟可能就好了。
 */
export const NOT_IMPLEMENTED_CODE = 'NOT_IMPLEMENTED';

export function isNotImplemented(error: ApiError | null | undefined): boolean {
  return error?.code === NOT_IMPLEMENTED_CODE;
}

/**
 * `ErrorCode` → 文案。**这四页唯一按 code 分支的读侧文案表**
 * （api-contract §2.3：禁止匹配 `message` 做分支）。
 *
 * 写侧（危险操作提交失败）用 `DangerousAction` 里的 `dangerErrorCopy`，
 * 两张表**有意分开**：读失败说的是「这一块加载不出来」，写失败说的是
 * 「你这一下没生效」—— 把它们合成一张表，必然有一半的文案在另一半的语境里是错的。
 */
export function catalogErrorCopy(
  error: ApiError,
  options: { fallbackTitle?: string } = {},
): { title: string; description: string } {
  switch (error.code) {
    case NOT_IMPLEMENTED_CODE:
      return {
        title: '这一块后端还没上线',
        description: '不是你的操作有问题，重试也不会有变化。',
      };
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '当前管理员账号看不到这一块',
        description:
          '身份是通过的，缺的是角色或权限位（配置类写操作只给 owner / admin）。重新登录不会有帮助。',
      };
    case 'RESOURCE_NOT_FOUND':
      return { title: '找不到这条记录', description: '它可能刚被别人改过或删掉了。刷新一下再看。' };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return { title: '服务端退回了这次请求', description: fieldReasons(error) ?? error.message };
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
      return {
        title: '连不上后台 API',
        description:
          '🔴 后台**不做**备用域名故障转移（多一个入口就是多一个要防护的入口）。' +
          '若你在大陆境内，先确认自己的出网路径 —— IAP 要求的 Google 身份本身就要能出去。',
      };
    case 'server':
      return { title: '服务端出错了', description: '不是你的操作有问题。稍后再试，并把请求号一起报出来。' };
    case 'unauthorized':
      return { title: '需要重新准入', description: '会话状态已经变了，刷新页面会重新走一次准入探测。' };
    default:
      return { title: options.fallbackTitle ?? '加载失败', description: error.message };
  }
}

function fieldReasons(error: ApiError): string | null {
  const details = error.details;
  if (!details || details.length === 0) return null;
  return details.map((d) => `${d.field}：${d.reason}`).join('；');
}

/**
 * 501 专用的提示块。
 *
 * **刻意不用 `ErrorState`**：501 的 `kind` 是 `server`，而 `ErrorState` 在 server 态下会说
 * 「我们这边出了问题」并把人推去状态页 —— 状态页上一切正常，看的人只会更困惑。
 * 「还没做」不是故障，红色警告框在这里是误报。
 */
export function NotImplementedNotice({
  what,
  why,
  requestId,
}: {
  what: string;
  /** 为什么还没做。**必须给** —— 「尚未开放」四个字会让人以为再等等就有了。 */
  why: ReactNode;
  requestId?: string | undefined;
}) {
  return (
    <Card className="border-dashed">
      <h3 className="text-base font-semibold text-fg">{what}尚未开放</h3>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">{why}</p>
      {requestId ? <p className="mt-3 font-mono text-xs text-fg-subtle">请求号 {requestId}</p> : null}
    </Card>
  );
}

/** 读请求失败时的整块错误态。501 走上面那块，其余走全站统一的 `ErrorState`。 */
export function QueryErrorState({
  error,
  what,
  notImplementedWhy,
  onRetry,
}: {
  error: ApiError;
  what: string;
  notImplementedWhy?: ReactNode;
  onRetry?: () => void;
}) {
  if (isNotImplemented(error)) {
    return (
      <NotImplementedNotice
        what={what}
        why={notImplementedWhy ?? '后端这一条还没实现。不是你的操作有问题，重试也不会有变化。'}
        requestId={error.requestId}
      />
    );
  }
  const copy = catalogErrorCopy(error, { fallbackTitle: `${what}没能加载` });
  return (
    <ErrorState
      kind={error.kind}
      title={copy.title}
      description={copy.description}
      requestId={error.requestId}
      onRetry={onRetry}
    />
  );
}

/** 列表区的骨架屏。四页统一，免得每页各挑一个行数。 */
export function ListLoading() {
  return (
    <LoadingState>
      <SkeletonCard lines={5} />
    </LoadingState>
  );
}

/* ────────────────────────── 游标分页 ────────────────────────── */

/** 一页多少条。后台是桌面优先（M3），一屏能看下 20 行。 */
export const PAGE_SIZE = 20;

export interface CursorPager {
  /** 当前页的起始游标。第一页是 `null`（不传 `cursor` 参数）。 */
  readonly cursor: string | null;
  /** 第几页，从 1 起。**只用于显示**，不参与请求。 */
  readonly pageNumber: number;
  readonly atFirstPage: boolean;
  next(cursor: string): void;
  prev(): void;
  /** 改筛选条件时回到第一页。**不回到第一页的话，旧游标会在新条件下解出一段无意义的位置。** */
  reset(): void;
}

/**
 * 游标分页器。
 *
 * 🔴 **游标分页没有「跳到第 7 页」。** 游标是「从这一行之后再取 N 条」，
 * 不是偏移量。所以这里只有上一页 / 下一页，且「上一页」是靠**记住来路**实现的
 * （压栈），不是靠一个反向游标 —— 契约里没有反向游标。
 *
 * 把它做成 `?page=7` 的样子需要 OFFSET 分页，而 OFFSET 在这个库上（db-f1-micro）
 * 恰恰是我们要避开的东西。缺口是真的，形态是有意的。
 */
export function useCursorPager(): CursorPager {
  // 栈里存的是**每一页的起始游标**，栈底恒为 null（第一页）。
  const [stack, setStack] = useState<readonly (string | null)[]>([null]);
  const cursor = stack.length > 0 ? (stack[stack.length - 1] ?? null) : null;

  const next = useCallback((c: string) => setStack((s) => [...s, c]), []);
  const prev = useCallback(() => setStack((s) => (s.length > 1 ? s.slice(0, -1) : s)), []);
  const reset = useCallback(() => setStack([null]), []);

  return { cursor, pageNumber: stack.length, atFirstPage: stack.length === 1, next, prev, reset };
}

/**
 * 分页器。
 *
 * ⚠️ **管理面可以返总数，用户面不行**（`Meta.total` 的契约注释：仅管理面 `?count=true`）。
 * 但 `COUNT(*)` 在 db-f1-micro 上是实打实的开销，所以**只在第一页要一次**，
 * 后续页沿用第一次拿到的那个数（见 `useRememberedTotal`）。
 * 这意味着翻页期间有人新增了记录时，总数会短暂偏旧 —— 比每页都付一次 COUNT 划算，
 * 且「共 87 条」这个数字在后台的用途是估量级，不是对账。
 */
export function Pager({
  meta,
  pager,
  total,
  busy,
}: {
  meta: Meta | null;
  pager: CursorPager;
  total: number | null;
  busy?: boolean;
}) {
  const nextCursor = meta?.next_cursor ?? null;
  const hasMore = meta?.has_more === true && nextCursor !== null;

  return (
    <div className="mt-4 flex flex-wrap items-center justify-between gap-3 text-sm text-fg-muted">
      <span>
        第 {pager.pageNumber} 页
        {total === null ? null : <> · 共 {total.toLocaleString('zh-CN')} 条</>}
      </span>
      <span className="flex gap-2">
        <Button onClick={pager.prev} disabled={pager.atFirstPage || busy === true}>
          上一页
        </Button>
        <Button
          onClick={() => {
            // 🔴 判据是 `next_cursor` 而不是「这一页够不够 20 条」：
            //    最后一页恰好 20 条时，按条数判会给出一个点不动的「下一页」。
            if (nextCursor !== null) pager.next(nextCursor);
          }}
          disabled={!hasMore || busy === true}
        >
          下一页
        </Button>
      </span>
    </div>
  );
}

/**
 * 记住第一页那次 `count=true` 的总数。
 *
 * 翻到第二页后 `meta.total` 就没有了（我们不再要），但分页器还要显示它。
 * 回到第一页会重新取一次 —— 那正是「刷新总数」的自然入口。
 */
export function useRememberedTotal(meta: Meta | null | undefined): number | null {
  const [total, setTotal] = useState<number | null>(null);
  const seen = meta?.total;
  useEffect(() => {
    if (typeof seen === 'number') setTotal(seen);
  }, [seen]);
  return total;
}

/* ────────────────────────── 表格 ────────────────────────── */

/**
 * 表格外壳。**横向滚动必须落在这一层**，不能让 body 横滚 ——
 * 后台是 M3（桌面优先、手机上可读即可），而「可读」的下限是不出现全页横滚。
 */
export function DataTable({ head, children }: { head: readonly string[]; children: ReactNode }) {
  return (
    <div className="-mx-4 overflow-x-auto sm:mx-0">
      <table className="w-full min-w-[40rem] border-collapse text-sm">
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

/* ────────────────────────── 表单原语 ────────────────────────── */

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
  label: ReactNode;
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

export function TextField({
  label,
  value,
  onChange,
  hint,
  placeholder,
  mono,
  disabled,
}: {
  label: ReactNode;
  value: string;
  onChange: (value: string) => void;
  hint?: ReactNode;
  placeholder?: string;
  mono?: boolean;
  disabled?: boolean;
}) {
  const id = useId();
  return (
    <FieldShell id={id} label={label} hint={hint}>
      <input
        id={id}
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        autoComplete="off"
        spellCheck={false}
        onChange={(e) => onChange(e.target.value)}
        className={cx(CONTROL, 'min-h-11', mono === true && 'font-mono')}
      />
    </FieldShell>
  );
}

export function TextAreaField({
  label,
  value,
  onChange,
  hint,
  rows = 6,
  placeholder,
}: {
  label: ReactNode;
  value: string;
  onChange: (value: string) => void;
  hint?: ReactNode;
  rows?: number;
  placeholder?: string;
}) {
  const id = useId();
  return (
    <FieldShell id={id} label={label} hint={hint}>
      <textarea
        id={id}
        rows={rows}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className={cx(CONTROL, 'py-2.5 leading-relaxed')}
      />
    </FieldShell>
  );
}

/**
 * 整数输入。
 *
 * 🔴 **值是字符串，不是 number。** 受控 `<input type="number">` 上把值存成 number
 * 会在输入过程中（空串、只打了一个负号、末尾小数点）反复被归一成 0 或 NaN，
 * 表现是「打字打到一半光标跳了 / 数字自己变了」。存字符串、提交时再 parse，
 * 是这一类表单唯一不会咬人的写法。
 */
export function IntField({
  label,
  value,
  onChange,
  hint,
  placeholder,
  suffix,
}: {
  label: ReactNode;
  value: string;
  onChange: (value: string) => void;
  hint?: ReactNode;
  placeholder?: string;
  /** 单位或换算后的实时回显（如「= ¥72.00」）。 */
  suffix?: ReactNode;
}) {
  const id = useId();
  return (
    <FieldShell id={id} label={label} hint={hint}>
      <div className="flex items-center gap-2">
        <input
          id={id}
          value={value}
          placeholder={placeholder}
          inputMode="numeric"
          autoComplete="off"
          onChange={(e) => onChange(e.target.value)}
          className={cx(CONTROL, 'min-h-11 font-mono')}
        />
        {suffix ? <span className="shrink-0 text-sm whitespace-nowrap text-fg-muted">{suffix}</span> : null}
      </div>
    </FieldShell>
  );
}

export function CheckboxField({
  label,
  checked,
  onChange,
  hint,
}: {
  label: ReactNode;
  checked: boolean;
  onChange: (checked: boolean) => void;
  hint?: ReactNode;
}) {
  const id = useId();
  return (
    <div>
      <label htmlFor={id} className="flex items-start gap-2 text-sm font-medium text-fg">
        <input
          id={id}
          type="checkbox"
          checked={checked}
          onChange={(e) => onChange(e.target.checked)}
          className="mt-0.5 size-4 shrink-0 accent-[var(--color-accent,currentColor)]"
        />
        <span>{label}</span>
      </label>
      {hint ? <p className="mt-1.5 ml-6 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </div>
  );
}

/**
 * 单选组。
 *
 * 🔴 **`value` 允许是 `null`（还没选）**，这是这个组件存在的全部理由。
 * `<select>` 天然有一个「第一项就是默认值」的形态，而本组里有一处
 * （建套餐的 `type`）**绝对不能有默认值**：后端 `plans.kind` 是 NOT NULL 且
 * 刻意不给 DEFAULT（ADR 0013 §4.6），默认成周期套餐会让加油包被静默写错，
 * 且要等到有人买了才显形。没选就是没选，按钮点不动。
 */
export function RadioGroup<T extends string>({
  label,
  value,
  options,
  onChange,
  hint,
}: {
  label: ReactNode;
  value: T | null;
  options: ReadonlyArray<{ readonly value: T; readonly label: string; readonly hint?: string }>;
  onChange: (value: T) => void;
  hint?: ReactNode;
}) {
  const name = useId();
  return (
    <fieldset>
      <legend className="mb-1.5 block text-sm font-medium text-fg">{label}</legend>
      <div className="flex flex-col gap-2">
        {options.map((opt) => (
          <label
            key={opt.value}
            className={cx(
              'flex cursor-pointer items-start gap-2 rounded-lg border p-3 text-sm',
              value === opt.value ? 'border-accent bg-accent/5' : 'border-line bg-surface',
            )}
          >
            <input
              type="radio"
              name={name}
              value={opt.value}
              checked={value === opt.value}
              onChange={() => onChange(opt.value)}
              className="mt-0.5 size-4 shrink-0"
            />
            <span className="min-w-0">
              <span className="font-medium text-fg">{opt.label}</span>
              {opt.hint ? <span className="mt-0.5 block text-xs text-fg-muted">{opt.hint}</span> : null}
            </span>
          </label>
        ))}
      </div>
      {hint ? <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </fieldset>
  );
}

/* ────────────────────────── 数值解析 ────────────────────────── */

/**
 * 十进制整数解析。空串 / 非法输入 → `null`（**不是 0**）。
 *
 * 🔴 `Number('')` 是 0、`parseInt('12abc')` 是 12 —— 两者都会把一次输入错误
 * 静默变成一个看起来合理的数字，而这里的数字是**价格与配额**。
 */
export function parseInteger(raw: string): number | null {
  const s = raw.trim();
  if (s === '') return null;
  if (!/^-?[0-9]+$/.test(s)) return null;
  const n = Number(s);
  return Number.isSafeInteger(n) ? n : null;
}

/** 分 → 「= ¥72.00」的实时回显。看不懂的数字（空 / 非法）不回显。 */
export function centsPreview(raw: string): string {
  const n = parseInteger(raw);
  return n === null ? '' : `= ${formatCny(n)}`;
}

/** GiB ↔ 字节。契约里 `transfer_enable_bytes` 是字节，而人只会按 GB 想事情。 */
export const GIB = 1024 * 1024 * 1024;

export function bytesToGibText(bytes: number): string {
  if (bytes % GIB === 0) return String(bytes / GIB);
  return (bytes / GIB).toFixed(3);
}

/** 支持一位小数的 GiB 输入（如 0.5 GB 的试用包）。整数运算，不留浮点尾数。 */
export function gibTextToBytes(raw: string): number | null {
  const s = raw.trim();
  if (s === '') return null;
  if (!/^[0-9]+(\.[0-9]{1,3})?$/.test(s)) return null;
  const [whole = '0', frac = ''] = s.split('.');
  const scale = 1000;
  const milli = Number(whole) * scale + Number((frac + '000').slice(0, 3));
  if (!Number.isSafeInteger(milli)) return null;
  const bytes = (milli * GIB) / scale;
  return Number.isSafeInteger(bytes) ? bytes : null;
}

/* ────────────────────────── 域名提取（D12 用） ────────────────────────── */

/**
 * 从公告正文里抽出**所有链接的目标主机名**。
 *
 * 🔴 这是 D12「强制预览」的核心：公告兼域名广播位，而读公告的人此刻正处在
 * 「面板打不开、正在找备用地址」的状态，**戒备心最低**。
 * 写错一个字母的域名就是把这群人导向一个陌生站点。
 * 所以预览里要把域名**单独拎出来逐条核对** —— 混在正文里读，
 * `babe1plus.com` 与 `babelplus.com` 没有人能一眼分辨。
 *
 * 覆盖三种写法：markdown 链接 `[x](url)`、裸 URL、以及 `<url>`。
 * **宁可多报不可漏报**：多报一个非链接的串只是让人多核对一行，
 * 漏报一个真链接则让这一层完全失效。
 */
export function extractLinkHosts(content: string): readonly string[] {
  const hosts: string[] = [];
  const seen = new Set<string>();
  // 协议必须显式：没有协议的 `example.com` 在 markdown 里不是链接，
  // 抓它会把「我们的域名是 example.com」这种正文也报成链接，噪声盖过信号。
  const re = /\b(?:https?|ftp):\/\/([^\s/?#'")<>\]]+)/gi;
  for (const m of content.matchAll(re)) {
    const authority = m[1];
    if (authority === undefined) continue;
    // 去掉 userinfo（`https://babelplus.com@evil.example/` 的真实目标是 evil.example，
    // 这正是钓鱼链接最常用的一招 —— 必须按浏览器的口径取 host，不能取 @ 前面那段）。
    const at = authority.lastIndexOf('@');
    const hostPort = at >= 0 ? authority.slice(at + 1) : authority;
    const host = hostPort.replace(/:\d+$/, '').toLowerCase();
    if (host === '' || seen.has(host)) continue;
    seen.add(host);
    hosts.push(host);
  }
  return hosts;
}

/**
 * 这个主机名在不在**运行时配置的镜像域名列表**里。
 *
 * 只是一条提示，不是判据：镜像列表来自 `window.__BP_RUNTIME_CONFIG__`，
 * 一个刚上线还没进配置的新镜像会被标成「不在列表里」—— 那恰恰是要人去核对的情形。
 * 反过来，**标成「在列表里」也不代表这条链接是对的**（路径可能是错的）。
 */
export function isKnownMirrorHost(host: string): boolean {
  return runtimeConfig().mirrorDomains.some((d) => mirrorHost(d.url) === host);
}

/** `MirrorDomain.url` 是完整 URL（含协议），这里只取主机名。解析不了就返回空串（永不匹配）。 */
function mirrorHost(url: string): string {
  try {
    return new URL(url).hostname.toLowerCase();
  } catch {
    return '';
  }
}

/* ────────────────────────── 页头与危险操作登记 ────────────────────────── */

/**
 * 已接线页面的页头。
 *
 * 🔴 **接好线的页面不能再用 `ModuleScaffold`。** 那个外壳里有一块
 * `<NotWiredNotice>「尚未接线」`，还有一个 `useShellState` 造出来的假三态切换器 ——
 * 页面真的接上 API 之后，那两样都在**说假话**：前者说这页是壳，后者让人以为
 * 自己点出来的空态是真的空。留着比删掉更糟，因为它看起来还挺勤勉。
 *
 * 保留的是它真正有价值的两样：优先级 / 移动端档位徽标，以及危险操作登记表。
 */
export function ModuleHeader({
  title,
  description,
  priority,
  mobile,
  actions,
}: {
  title: string;
  description: ReactNode;
  priority: 'P1' | 'P2' | 'P3';
  mobile: 'M2' | 'M3';
  actions?: ReactNode;
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
            {mobile === 'M2'
              ? 'M2 · 手机上核心操作必须能完成'
              : 'M3 · 桌面优先，手机上可读即可'}
          </Badge>
        </>
      }
    />
  );
}

/**
 * 本页涉及的危险操作登记（page-inventory §4.4 的逐字誊本，取自 `lib/danger.ts`）。
 *
 * 为什么接完线还要留着它：这张表是**操作者在按下按钮之前**唯一能读到的
 * 「这一条为什么危险」。`DangerousAction` 展开后也会显示同样的话，
 * 但那时人已经决定要做了 —— 决定之前看到和决定之后看到，不是同一件事。
 */
export function DangerOpsNote({ codes }: { codes: readonly string[] }) {
  const ops = dangerOps(codes);
  if (ops.length === 0) return null;
  return (
    <ul className="mb-5 space-y-2">
      {ops.map((op) => (
        <li
          key={op.code}
          className="rounded-lg border border-danger/30 bg-danger/5 p-3 text-xs leading-relaxed"
        >
          <div className="flex flex-wrap items-baseline gap-2">
            <span className="rounded bg-danger/15 px-1.5 py-0.5 font-mono font-semibold text-danger">
              {op.code}
            </span>
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
