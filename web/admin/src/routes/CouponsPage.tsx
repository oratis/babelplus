/**
 * 模块 13 · 优惠码 `/admin/coupons` —— P2 / M3。危险操作 **D8**（改价类，同套餐）。
 *
 * 端点：`listAdminCoupons` · `createAdminCoupon` · `updateAdminCoupon` · `deleteAdminCoupon`。
 *
 * # 三处「值本身表达不出来」的地方，全部写在界面上
 *
 * 1. 🔴 **`value` 的量纲跟着 `type` 走。** `fixed` 时是**分**，`percent` 时是**百分点整数**
 *    （`20` = 打八折）。同一个数字 `2000` 在两种类型下是 ¥20 和 20%（后者还会被服务端拒，
 *    上限 100）。所以类型用 `RadioGroup` 且**初值是 `null`** —— 有默认值的话，
 *    一个想建「减 20 元」的人会得到一张「打八折」的券，而八折在 ¥358 的年付上是减 ¥71.60。
 *
 * 2. 🔴 **库里没有 `enabled` 列。** 唯一能真正停掉一张码的机制是把 `ends_at` 设成现在
 *    （`admin_catalog.go` 的 `couponEndsAtForWrite`）。所以「启用」这个勾选框的语义是
 *    非对称的：取消勾选 = 立刻结束；勾上 = 「保持可用」而**不是**「延长一个已过期的活动」。
 *    这条不说出来，一次「重新启用」会让三个月前的双十一券复活。
 *
 * 3. ⚠️ **百分点只能是整数。** 库里存的是 bps（`1050` = 10.5%），而契约的 `value` 是整数百分点，
 *    表达不出半个百分点。服务端读到 10.5% 时会截断成 10 并记 WARN ——
 *    于是一个 10.5% 的码在后台显示成 10%，管理员照着显示值保存一下，它就真的变成 10% 了。
 *    列表里对这种码没有额外标记（契约里没有可以据以判断的字段），缺口已登记。
 *
 * # 契约缺口
 *
 *  - 空壳里那条产品约束写着「券必须有**总量上限与单用户次数上限**，两者都不填等于对外公开的
 *    无限折扣」。契约的 `CouponUpsert` **只有 `use_limit`（总量）**，
 *    **没有单用户次数上限**这个字段 —— 后者在这一页做不出来。总量这一半照做（并在留空时明说风险）。
 *  - 「使用记录」（谁在什么时候用了这张码）**没有端点**，契约里只有一个 `used_count` 计数。
 *  - `DELETE /coupons/{id}` 没有请求体 → 删除的审计里不会有原因（服务端已记 WARN）。
 */
import { useCallback, useId, useMemo, useState, type ReactNode } from 'react';
import { unwrap, unwrapEmpty, unwrapWithMeta } from '@babelplus/shared/api';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  cx,
  formatCny,
  formatDateTime,
} from './_imports.ts';
import { api } from '../lib/api.ts';
import { DangerousAction } from '../components/DangerousAction.tsx';
import {
  CheckboxField,
  DangerOpsNote,
  DataTable,
  FieldShell,
  IntField,
  ListLoading,
  ModuleHeader,
  PAGE_SIZE,
  Pager,
  QueryErrorState,
  RadioGroup,
  TextField,
  Td,
  Tr,
  centsPreview,
  parseInteger,
  useApiQuery,
  useCursorPager,
  useRememberedTotal,
  type Coupon,
  type CouponUpsert,
  type Plan,
} from './catalog-common.tsx';

/* ────────────────────────── 日期输入（本页与公告页共用） ────────────────────────── */

/**
 * `datetime-local` 输入。**本文件是两处里先用到它的那一页，公告页从这里 import。**
 *
 * 为什么不复制两份：复制的代价不是行数，是**同一个时区转换被写成两种**。
 * `<input type="datetime-local">` 给的是**本地时间**的 `YYYY-MM-DDTHH:mm`，
 * 而契约要的是 RFC3339（带时区）。少写一次 `toISOString()`，
 * 一张本该 0 点开始的券会在管理员所在时区的 8 点（或前一天 16 点）才生效 ——
 * 而这个偏差不会报错，只会让活动开始那一小时的用户看到「优惠码无效」。
 */
export function DateTimeField({
  label,
  value,
  onChange,
  hint,
}: {
  label: ReactNode;
  value: string;
  onChange: (value: string) => void;
  hint?: ReactNode;
}) {
  const id = useId();
  return (
    <FieldShell id={id} label={label} hint={hint}>
      <input
        id={id}
        type="datetime-local"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className={cx(
          'min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg',
          'focus-visible:border-accent focus-visible:outline-2 focus-visible:outline-offset-1',
          'focus-visible:outline-accent',
        )}
      />
    </FieldShell>
  );
}

/** RFC3339 → `datetime-local` 需要的本地时间串。解析不了就当没填。 */
export function isoToLocalInput(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/**
 * `datetime-local` 的本地时间串 → RFC3339。
 *
 * `undefined` = 没填（字段整个不发出去），`null` = 填了但看不懂（调用方要挡住提交）。
 * 两者分开而不是都返回 `undefined`：后者会把一次输入错误静默变成「没设有效期」。
 */
export function localInputToIso(raw: string): string | undefined | null {
  const s = raw.trim();
  if (s === '') return undefined;
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return null;
  return d.toISOString();
}

/* ────────────────────────── 页面 ────────────────────────── */

const COUPON_TYPES = [
  {
    value: 'fixed' as const,
    label: '固定金额（fixed）',
    hint: '减去一个固定的钱数。下面的「优惠额」按**分**填：减 ¥20 要填 2000。',
  },
  {
    value: 'percent' as const,
    label: '百分比（percent）',
    hint: '按比例减。下面的「优惠额」按**百分点整数**填：打八折要填 20（= 减 20%）。上限 100。',
  },
];

type EditorTarget = Coupon | 'new' | null;
type StatusFilter = 'all' | 'usable' | 'unusable';

export default function CouponsPage() {
  const pager = useCursorPager();
  const page = useApiQuery(
    () =>
      unwrapWithMeta(
        api().GET('/api/v1/admin/coupons', {
          params: {
            query: {
              limit: PAGE_SIZE,
              ...(pager.cursor === null ? {} : { cursor: pager.cursor }),
              // 总数只在第一页要一次：COUNT(*) 在 db-f1-micro 上是实打实的开销。
              ...(pager.cursor === null ? { count: true } : {}),
            },
          },
        }),
      ),
    [pager.cursor],
    '优惠码列表加载失败',
  );
  const total = useRememberedTotal(page.data?.meta);

  // 适用套餐要显示名字而不是一串 id。这条查询**失败也不挡页面** ——
  // 拿不到名字时退回显示 id，总比整页错误态强。
  const plans = useApiQuery(() => unwrap(api().GET('/api/v1/admin/plans')), [], '套餐列表加载失败');
  const planName = usePlanNames(plans.data);

  const [editor, setEditor] = useState<EditorTarget>(null);
  const [status, setStatus] = useState<StatusFilter>('all');

  const reload = page.reload;
  const closeAndReload = useCallback(() => {
    setEditor(null);
    reload();
  }, [reload]);

  const items = page.data?.data ?? [];
  const shown = useMemo(
    () =>
      status === 'all'
        ? items
        : items.filter((c) => (status === 'usable' ? c.enabled : !c.enabled)),
    [items, status],
  );

  return (
    <>
      <ModuleHeader
        title="优惠码"
        description="折扣券的发放与停用。长周期折扣已经写进套餐价格里了，优惠码是额外的运营手段。"
        priority="P2"
        mobile="M3"
        actions={
          <Button tone="primary" onClick={() => setEditor('new')}>
            新建优惠码
          </Button>
        }
      />

      <DangerOpsNote codes={['D8']} />

      <Card className="mb-5 border-l-4 border-l-warn">
        <h2 className="text-sm font-semibold text-fg">建券之前先看这三条</h2>
        <ul className="mt-1.5 list-disc space-y-1 pl-5 text-sm leading-relaxed text-fg-muted">
          <li>
            <strong className="font-medium text-fg">优惠额的单位跟着类型走：</strong>
            固定金额是<strong className="font-medium text-fg">分</strong>，百分比是
            <strong className="font-medium text-fg">百分点整数</strong>。
            所以类型是必选项，没有默认值。
          </li>
          <li>
            <strong className="font-medium text-fg">不填总量上限 = 一张对外公开的无限折扣券。</strong>
            码一旦被贴到论坛上就收不回来了，唯一的止损手段是把它停用。
          </li>
          <li>
            ⚠️ 契约里<strong className="font-medium text-fg">没有「单用户次数上限」</strong>这个字段，
            所以这一页做不出「每人限用一次」。同一个人可以把一张不限总量的券用到你发现为止。
          </li>
        </ul>
      </Card>

      {editor !== null ? (
        <div className="mb-5">
          <CouponEditor
            coupon={editor === 'new' ? null : editor}
            plans={plans.data ?? []}
            onDone={closeAndReload}
            onCancel={() => setEditor(null)}
          />
        </div>
      ) : null}

      {page.state === 'loading' ? <ListLoading /> : null}

      {page.state === 'error' && page.error !== null ? (
        <QueryErrorState error={page.error} what="优惠码列表" onRetry={page.reload} />
      ) : null}

      {page.state === 'ready' && items.length === 0 && pager.atFirstPage ? (
        <EmptyState
          title="还没有优惠码"
          description="长周期折扣已经写进套餐价格里了，优惠码是额外的运营手段，不是必需品。"
          action={
            <Button tone="primary" onClick={() => setEditor('new')}>
              新建优惠码
            </Button>
          }
        />
      ) : null}

      {page.state === 'ready' && items.length > 0 ? (
        <Card>
          <CardTitle hint="listAdminCoupons">券列表</CardTitle>

          <div className="mb-3 flex flex-wrap items-center gap-2">
            {(
              [
                ['all', '全部'],
                ['usable', '当前可用'],
                ['unusable', '不可用'],
              ] as const
            ).map(([value, label]) => (
              <Button
                key={value}
                tone={status === value ? 'primary' : 'default'}
                onClick={() => setStatus(value)}
              >
                {label}
              </Button>
            ))}
            {/* ⚠️ 这个筛选只作用在当前这一页上。契约的 listAdminCoupons 没有筛选参数，
                做成「服务端筛选」的样子会让人以为「筛不出来 = 不存在」，
                而那张码可能就在第 3 页。 */}
            <span className="text-xs text-fg-subtle">
              只过滤当前这一页（契约没有服务端筛选参数）
            </span>
          </div>

          <DataTable head={['码', '折扣', '适用套餐', '总量 / 已用', '有效期', '状态', '']}>
            {shown.map((c) => (
              <Tr key={c.id}>
                <Td>
                  <span className="font-mono font-medium text-fg">{c.code}</span>
                  <span className="mt-0.5 block font-mono text-xs text-fg-subtle">#{c.id}</span>
                </Td>
                <Td className="whitespace-nowrap font-mono">{discountText(c)}</Td>
                <Td>
                  {c.plan_ids === undefined || c.plan_ids.length === 0 ? (
                    <span className="text-fg-muted">不限套餐</span>
                  ) : (
                    <span className="flex flex-col gap-0.5">
                      {c.plan_ids.map((id) => (
                        <span key={id}>{planName(id)}</span>
                      ))}
                    </span>
                  )}
                </Td>
                <Td className="whitespace-nowrap font-mono">
                  {c.use_limit === undefined ? (
                    <span className="text-warn">不限 / {c.used_count ?? 0}</span>
                  ) : (
                    `${c.use_limit} / ${c.used_count ?? 0}`
                  )}
                </Td>
                <Td className="whitespace-nowrap text-xs">
                  <span className="block">开始 {formatDateTime(c.started_at)}</span>
                  <span className="block">结束 {formatDateTime(c.ended_at)}</span>
                </Td>
                <Td>
                  {c.enabled ? <Badge tone="ok">可用</Badge> : <Badge tone="neutral">不可用</Badge>}
                </Td>
                <Td>
                  <Button onClick={() => setEditor(c)}>编辑 / 删除</Button>
                </Td>
              </Tr>
            ))}
          </DataTable>

          <Pager
            meta={page.data?.meta ?? null}
            pager={pager}
            total={total}
            busy={page.state !== 'ready'}
          />
        </Card>
      ) : null}
    </>
  );
}

/** id → 套餐名。拿不到套餐列表时退回 `#id`，不显示一个空白格。 */
function usePlanNames(plans: readonly Plan[] | null | undefined): (id: number) => string {
  return useMemo(() => {
    const map = new Map<number, string>();
    for (const p of plans ?? []) map.set(p.id, p.name);
    return (id: number) => map.get(id) ?? `#${id}`;
  }, [plans]);
}

/**
 * 折扣的展示串。**量纲必须显示出来** —— 一个只写着 `2000` 的格子，
 * 没有人能确定它是 ¥20 还是 20%。
 */
export function discountText(c: Pick<Coupon, 'type' | 'value'>): string {
  return c.type === 'percent' ? `减 ${c.value}%` : `减 ${formatCny(c.value)}`;
}

/* ────────────────────────── 表单 ────────────────────────── */

interface CouponForm {
  code: string;
  /** 🔴 `null` = 还没选。优惠额的量纲跟着它走，所以不给默认值。 */
  type: 'fixed' | 'percent' | null;
  value: string;
  enabled: boolean;
  useLimit: string;
  startedAt: string;
  endedAt: string;
  planIds: readonly number[];
}

function emptyForm(): CouponForm {
  return {
    code: '',
    type: null,
    value: '',
    enabled: true,
    useLimit: '',
    startedAt: '',
    endedAt: '',
    planIds: [],
  };
}

function formFromCoupon(c: Coupon): CouponForm {
  return {
    code: c.code,
    type: c.type,
    value: String(c.value),
    enabled: c.enabled,
    useLimit: c.use_limit === undefined ? '' : String(c.use_limit),
    startedAt: isoToLocalInput(c.started_at),
    endedAt: isoToLocalInput(c.ended_at),
    planIds: c.plan_ids ?? [],
  };
}

export type CouponDraft =
  | { readonly ok: true; readonly value: Omit<CouponUpsert, 'reason'> }
  | { readonly ok: false; readonly problem: string };

/**
 * 表单校验。导出供单测直接打。
 *
 * ⚠️ 这里挡下的每一条服务端也都挡（`couponWriteParams` / `couponValueToDB`）。
 * 省的是一次注定被 422 退回的往返，不是安全边界。
 */
export function buildCouponDraft(form: CouponForm): CouponDraft {
  const code = form.code.trim();
  if (code === '') return { ok: false, problem: '优惠码不能为空。' };

  if (form.type === null) {
    return {
      ok: false,
      problem:
        '还没选类型。「优惠额」的单位跟着类型走（固定金额是分、百分比是百分点），所以这里没有默认值。',
    };
  }

  const value = parseInteger(form.value);
  if (value === null) return { ok: false, problem: '优惠额要填一个整数。' };
  if (value <= 0) {
    return { ok: false, problem: '优惠额必须大于 0（想停用这张码请取消勾选「启用」）。' };
  }
  if (form.type === 'percent' && value > 100) {
    return { ok: false, problem: '百分比折扣不能超过 100 个百分点。' };
  }

  let useLimit: number | undefined;
  if (form.useLimit.trim() !== '') {
    const n = parseInteger(form.useLimit);
    if (n === null || n < 1) {
      return { ok: false, problem: '总量上限至少是 1（不限总量请把这一格留空）。' };
    }
    useLimit = n;
  }

  const startedAt = localInputToIso(form.startedAt);
  if (startedAt === null) return { ok: false, problem: '开始时间看不懂。' };
  const endedAt = localInputToIso(form.endedAt);
  if (endedAt === null) return { ok: false, problem: '结束时间看不懂。' };
  if (startedAt !== undefined && endedAt !== undefined && endedAt <= startedAt) {
    return { ok: false, problem: '结束时间必须晚于开始时间。' };
  }

  return {
    ok: true,
    value: {
      code,
      type: form.type,
      value,
      enabled: form.enabled,
      ...(useLimit === undefined ? {} : { use_limit: useLimit }),
      ...(startedAt === undefined ? {} : { started_at: startedAt }),
      ...(endedAt === undefined ? {} : { ended_at: endedAt }),
      plan_ids: [...form.planIds],
    },
  };
}

function CouponEditor({
  coupon,
  plans,
  onDone,
  onCancel,
}: {
  coupon: Coupon | null;
  plans: readonly Plan[];
  onDone: () => void;
  onCancel: () => void;
}) {
  const [form, setForm] = useState<CouponForm>(() =>
    coupon === null ? emptyForm() : formFromCoupon(coupon),
  );
  const draft = buildCouponDraft(form);

  const set = <K extends keyof CouponForm>(key: K, value: CouponForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  const togglePlan = (id: number) =>
    setForm((f) => ({
      ...f,
      planIds: f.planIds.includes(id) ? f.planIds.filter((x) => x !== id) : [...f.planIds, id],
    }));

  return (
    <Card className="border-l-4 border-l-accent">
      <CardTitle hint={coupon === null ? 'createAdminCoupon（D8）' : 'updateAdminCoupon（D8）'}>
        {coupon === null ? '新建优惠码' : `编辑优惠码 #${coupon.id} · ${coupon.code}`}
      </CardTitle>

      {coupon !== null ? (
        <dl className="mb-4 grid gap-x-6 gap-y-2 rounded-lg border border-line bg-surface-alt p-3 text-sm sm:grid-cols-2">
          <div>
            <dt className="text-xs text-fg-muted">已使用次数（只读）</dt>
            <dd className="mt-0.5 font-mono text-fg">{coupon.used_count ?? 0}</dd>
          </div>
          <div>
            <dt className="text-xs text-fg-muted">当前是否可用（只读，服务端算的）</dt>
            <dd className="mt-0.5 text-fg">
              {coupon.enabled ? '可用' : '不可用'}
              <span className="mt-0.5 block text-xs text-fg-subtle">
                判据是「未开始 / 已结束 / 已用尽」三者都不成立，与用户兑换时的判据同源。
              </span>
            </dd>
          </div>
        </dl>
      ) : null}

      <div className="space-y-4">
        <TextField
          label="优惠码"
          value={form.code}
          onChange={(v) => set('code', v)}
          placeholder="NEWYEAR2026"
          mono
          hint="码不区分大小写（唯一索引是不区分大小写的），重复会被服务端退回。"
        />

        <RadioGroup
          label="类型（必选，没有默认值）"
          value={form.type}
          options={COUPON_TYPES}
          onChange={(v) => set('type', v)}
          hint="🔴 「优惠额」那一格的单位由这里决定。选错的后果是金额差一个量级，而不会有任何报错。"
        />

        <IntField
          // 🔴 标签跟着类型走。类型没选时**不能**默认写「分」——
          //    那等于替操作者选了一个他没选的量纲。
          label={
            form.type === null
              ? '优惠额（单位取决于类型，先选类型）'
              : form.type === 'percent'
                ? '优惠额（百分点，1–100）'
                : '优惠额（分）'
          }
          value={form.value}
          onChange={(v) => set('value', v)}
          placeholder={form.type === 'percent' ? '20' : '2000'}
          suffix={
            form.type === 'percent'
              ? form.value.trim() === ''
                ? ''
                : `= 减 ${form.value.trim()}%`
              : centsPreview(form.value)
          }
          hint={
            form.type === null
              ? '先选类型 —— 在选之前，这一格的单位是未定的。'
              : form.type === 'percent'
                ? '整数百分点。库里存的是 bps，半个百分点（10.5%）在契约里表达不出来。'
                : '单位是分。减 ¥20 要填 2000。'
          }
        />

        <IntField
          label="总量上限（留空 = 不限）"
          value={form.useLimit}
          onChange={(v) => set('useLimit', v)}
          placeholder="100"
          hint={
            form.useLimit.trim() === '' ? (
              <span className="text-warn">
                ⚠️ 留空 = 这张券可以被无限次使用。码一旦流出就只能靠停用止损。
              </span>
            ) : (
              '整个系统总共能用多少次。⚠️ 契约里没有「单用户次数上限」，做不出「每人限一次」。'
            )
          }
        />

        <div className="grid gap-4 sm:grid-cols-2">
          <DateTimeField
            label="开始时间（留空 = 立刻）"
            value={form.startedAt}
            onChange={(v) => set('startedAt', v)}
            hint="按你所在时区输入，提交时会转成带时区的时间戳。"
          />
          <DateTimeField
            label="结束时间（留空 = 永不过期）"
            value={form.endedAt}
            onChange={(v) => set('endedAt', v)}
          />
        </div>

        <CheckboxField
          label="启用这张券"
          checked={form.enabled}
          onChange={(v) => set('enabled', v)}
          hint={
            <>
              🔴 <strong className="font-medium text-fg">库里没有「启用」这一列。</strong>
              取消勾选是把结束时间设成<strong className="font-medium text-fg">现在</strong>（立刻停用，
              且会覆盖上面填的结束时间）；勾上是「保持可用」——
              对一张<strong className="font-medium text-fg">还没到期</strong>的券，它不会去延长有效期。
            </>
          }
        />

        <fieldset>
          <legend className="mb-1.5 block text-sm font-medium text-fg">适用套餐</legend>
          <p className="mb-2 text-xs leading-relaxed text-fg-muted">
            一个都不勾 = <strong className="font-medium text-fg">不限套餐</strong>，
            这张券对所有套餐都生效（包括以后新建的）。
          </p>
          {plans.length === 0 ? (
            <p className="text-sm text-fg-muted">
              套餐列表没能加载，这次只能建「不限套餐」的券。
            </p>
          ) : (
            <div className="flex flex-col gap-2">
              {plans.map((p) => (
                <CheckboxField
                  key={p.id}
                  label={`${p.name}（#${p.id}）`}
                  checked={form.planIds.includes(p.id)}
                  onChange={() => togglePlan(p.id)}
                />
              ))}
            </div>
          )}
        </fieldset>

        {!draft.ok ? (
          <p className="rounded-lg border border-line bg-surface-alt px-3 py-2 text-sm text-fg-muted">
            还不能提交：{draft.problem}
          </p>
        ) : null}

        <DangerousAction
          code="D8"
          // title 是折叠状态下那个按钮的字，submitLabel 是展开后真正提交的那个 ——
          // 两者刻意不同名：同名的话，「点开确认面板」与「确认执行」在屏幕上长得一样。
          title={coupon === null ? '创建优惠码' : '保存优惠码改动'}
          submitLabel={coupon === null ? '确认创建' : '确认保存'}
          // CouponUpsert.reason 是契约里的 required 字段，服务端按码位数校验（≥ 8）。
          requireReason
          disabled={!draft.ok}
          disabledReason={draft.ok ? undefined : draft.problem}
          context={<CouponContext draft={draft} coupon={coupon} />}
          onSubmit={async (values) => {
            if (!draft.ok) return;
            const body: CouponUpsert = { ...draft.value, reason: values.reason ?? '' };
            if (coupon === null) {
              await unwrap(api().POST('/api/v1/admin/coupons', { body }));
            } else {
              await unwrap(
                api().PATCH('/api/v1/admin/coupons/{id}', {
                  params: { path: { id: coupon.id } },
                  body,
                }),
              );
            }
          }}
          onDone={onDone}
        />

        {coupon !== null ? <CouponDeleteAction coupon={coupon} onDone={onDone} /> : null}

        <Button tone="ghost" onClick={onCancel}>
          关闭编辑器
        </Button>
      </div>
    </Card>
  );
}

/** D8 确认面板里的事实块：这张券实际会减多少、影响谁。 */
function CouponContext({ draft, coupon }: { draft: CouponDraft; coupon: Coupon | null }) {
  if (!draft.ok) return <p className="text-fg-muted">表单还没填完。</p>;
  const v = draft.value;
  return (
    <>
      <p className="font-medium text-fg">
        <span className="font-mono">{v.code}</span> · {discountText(v)}
      </p>
      <ul className="mt-1 list-disc space-y-1 pl-5 text-sm leading-relaxed text-fg-muted">
        <li>
          适用范围：
          {v.plan_ids === undefined || v.plan_ids.length === 0
            ? '不限套餐（含以后新建的套餐）'
            : `${v.plan_ids.length} 个指定套餐`}
        </li>
        <li>
          总量上限：
          {v.use_limit === undefined ? (
            <strong className="font-medium text-warn">不限（码流出后只能靠停用止损）</strong>
          ) : (
            `${v.use_limit} 次`
          )}
          {coupon === null ? null : `，已用 ${coupon.used_count ?? 0} 次`}
        </li>
        <li>
          有效期：{v.started_at === undefined ? '立刻' : formatDateTime(v.started_at)} —{' '}
          {v.enabled === false
            ? '现在（取消勾选「启用」= 立刻结束）'
            : v.ended_at === undefined
              ? '永不过期'
              : formatDateTime(v.ended_at)}
        </li>
        <li>
          已经用过这张券下单的订单<strong className="font-medium text-fg">不受影响</strong>：
          折扣金额在下单时就写进订单快照了。
        </li>
      </ul>
    </>
  );
}

/**
 * 删除优惠码（`deleteAdminCoupon`，D8）。
 *
 * ⚠️ **不收原因**：契约给 DELETE 端点没有请求体，收上来的原因发不出去。
 * 服务端的审计里会有被删掉的整行（那是这次删除唯一留下的证据），但没有「为什么」。
 */
function CouponDeleteAction({ coupon, onDone }: { coupon: Coupon; onDone: () => void }) {
  return (
    <DangerousAction
      code="D8"
      title="删除这张优惠码"
      submitLabel="确认删除"
      context={
        <>
          <p className="font-medium text-fg">
            将删除 <span className="font-mono">{coupon.code}</span>（{discountText(coupon)}，已用{' '}
            {coupon.used_count ?? 0} 次）。
          </p>
          <ul className="mt-1 list-disc space-y-1 pl-5 text-sm leading-relaxed text-fg-muted">
            <li>
              已经用过它下单的订单<strong className="font-medium text-fg">不受影响</strong>。
            </li>
            <li>
              如果只是想停售，<strong className="font-medium text-fg">取消勾选「启用」更好</strong>
              —— 删掉之后这张码就能被重新建出来了，而停用的码不会。
            </li>
            <li>
              ⚠️ 这一条<strong className="font-medium text-fg">不会写下操作原因</strong>：
              契约给 DELETE 端点没有请求体。审计里有被删掉的整行，但没有「为什么」。
            </li>
          </ul>
        </>
      }
      onSubmit={async () => {
        await unwrapEmpty(
          api().DELETE('/api/v1/admin/coupons/{id}', { params: { path: { id: coupon.id } } }),
        );
      }}
      onDone={onDone}
    />
  );
}
