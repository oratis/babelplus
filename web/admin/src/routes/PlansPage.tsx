/**
 * 模块 4 · 套餐管理 `/admin/plans` —— P1 / M3。危险操作 **D8**（改价 / 下架）。
 *
 * 端点：`listAdminPlans` · `createAdminPlan` · `updateAdminPlan` · `deleteAdminPlan`。
 *
 * # 三条决定了这一页形态的事实
 *
 * 1. 🔴 **改套餐只影响新订单。** 已售出的 `transfer_enable` 在当前周期内不可撤回
 *    （定价修订 A2），历史订单的价格快照 `orders.price_monthly_at_order` 一行都不回改 ——
 *    退款扣减读的是那个快照而不是 `plans.price_monthly` 这个活列，否则涨价之后退款额会变小，
 *    用户会认为我们改价是为了少退钱。这句话写在 D8 确认面板的 `context` 里，
 *    是操作者按下按钮之前必须读到的那一条事实。
 *
 *    ⚠️ **这句话在服务端没有对应的 L1 确认串可以对质。** `PlanUpsert` 里没有 `confirmation`
 *    字段，所以服务端只能把这句话固化成常量写进每一条 D8 审计
 *    （`admin_catalog.go` 的 `planPricingScopeNotice` → `after.pricing_scope_note`）。
 *    这里**刻意不做一个前端自己比对的确认串输入框** —— 那个框收上来的值发不出去，
 *    它只会让人以为 L1 生效了。§6.2 的四层全部在服务端强制，前端只负责把参数收齐；
 *    一个收了参数却无处可送的输入框是这条原则的反面。缺口已登记（补法是给 PlanUpsert 加字段）。
 *
 * 2. 🔴 **建套餐必须选 `kind`，界面上不给默认值。** `plans.kind` 是 NOT NULL 且刻意没有
 *    DEFAULT（ADR 0013 §4.6）。默认成「周期套餐」会让加油包被静默写成周期套餐，
 *    于是 `POST /orders` 把它推导成 new/renew/upgrade、走进周期套餐的开通逻辑并凭空触发一次折抵 ——
 *    一次静默的错误分类，而且要等到有人买了才显形。所以类型用 `RadioGroup` 且初值是 `null`，
 *    没选就是没选，按钮点不动。
 *
 * 3. **价格数字全部来自 API，前端一个都不硬编码。** 定价现值（¥72 / ¥159 / ¥358）住在
 *    `docs/03-product/pricing-and-plans.md` 与数据库里；前端写死一份的后果是
 *    「后台显示的价和用户付的价不一样」，而这种不一致没有任何东西会报错。
 *
 * # 契约缺口（本页表达不出来的东西）
 *
 *  - `listAdminPlans` **没有任何 query 参数** —— 没有 limit / cursor / count。
 *    所以这一页是全量列表、没有分页器，筛选只能在客户端做（而客户端拿到的就是全部，
 *    因此这里的筛选不会漏行 —— 这与优惠码 / 公告 / 邀请那三页的游标分页不同）。
 *  - `DELETE /plans/{id}` **没有请求体** → 下架的审计里不会有原因（服务端已记 WARN）。
 *    这里也就不收原因：收一个发不出去的原因等于骗操作者。
 *  - 契约的 `Plan` 没有 `archived_at` 也没有 `sellable`，下架后唯一的信号是 `visible=false`。
 *    于是「已下架」与「只是不显示在套餐页」在 API 上不可区分，列表里只能显示后者。
 *  - `PlanUpsert` 没有 `group_id`（决定这个套餐能看到哪些节点）、没有 `reset_traffic_method`
 *    （由 kind 推：周期=按下单日月重置、加油包=永不重置）。两者都只能由服务端定死。
 */
import { useCallback, useMemo, useState, type ReactNode } from 'react';
import { unwrap, unwrapEmpty } from '@babelplus/shared/api';
import { Badge, Button, Card, CardTitle, EmptyState, formatBytes, formatCny } from './_imports.ts';
import { api } from '../lib/api.ts';
import { DangerousAction } from '../components/DangerousAction.tsx';
import {
  CheckboxField,
  DangerOpsNote,
  DataTable,
  IntField,
  ListLoading,
  ModuleHeader,
  QueryErrorState,
  RadioGroup,
  TextAreaField,
  TextField,
  Td,
  Tr,
  bytesToGibText,
  centsPreview,
  gibTextToBytes,
  parseInteger,
  useApiQuery,
  type Plan,
  type PlanPrice,
  type PlanUpsert,
} from './catalog-common.tsx';

/* ────────────────────────── 周期 ────────────────────────── */

/**
 * 本系统真正存在的五个周期。
 *
 * ⚠️ 契约的 `PlanPrice.period` 枚举里还有 `two_yearly` / `three_yearly`，而 `plans` 表
 * **根本没有这两列**、`order_period` 枚举里也没有这两个值（ADR 0013 §4.7 已登记）。
 * 服务端对这两个周期返回 422 并说明原因（不是静默丢弃）。这里干脆不给输入框：
 * 给了框、填了值、保存后被退回，操作者只会觉得这个后台坏了。
 */
const PLAN_PERIODS = [
  { key: 'monthly', label: '月付' },
  { key: 'quarterly', label: '季付' },
  { key: 'half_yearly', label: '半年付' },
  { key: 'yearly', label: '年付' },
  { key: 'onetime', label: '一次性' },
] as const;

type PeriodKey = (typeof PLAN_PERIODS)[number]['key'];

/** 契约枚举 → 中文。含表单里不给的那两个，因为**读**的时候库里可能有别人塞进去的值。 */
const PERIOD_LABEL: Readonly<Record<string, string>> = {
  monthly: '月付',
  quarterly: '季付',
  half_yearly: '半年付',
  yearly: '年付',
  two_yearly: '两年付',
  three_yearly: '三年付',
  onetime: '一次性',
};

const PLAN_TYPES = [
  {
    value: 'period' as const,
    label: '周期套餐（cycle）',
    hint: '按月/季/半年/年售卖，到期按下单日重置流量。必须有月付价 —— 退款金额按月单价折算。',
  },
  {
    value: 'traffic_pack' as const,
    label: '加油包（pack）',
    hint: '一次性购买的额外流量，**永不重置**。选错成周期套餐的话，它每月会白送一次流量。',
  },
];

/* ────────────────────────── 页面 ────────────────────────── */

/** 编辑器的目标：`null` 关闭、`'new'` 新建、`Plan` 编辑既有套餐。 */
type EditorTarget = Plan | 'new' | null;

export default function PlansPage() {
  const plans = useApiQuery(
    () => unwrap(api().GET('/api/v1/admin/plans')),
    [],
    '套餐列表加载失败',
  );
  const [editor, setEditor] = useState<EditorTarget>(null);
  const [typeFilter, setTypeFilter] = useState<'all' | 'period' | 'traffic_pack'>('all');

  const reload = plans.reload;
  const closeAndReload = useCallback(() => {
    setEditor(null);
    reload();
  }, [reload]);

  const items = plans.data ?? [];
  const shown = useMemo(
    () => (typeFilter === 'all' ? items : items.filter((p) => p.type === typeFilter)),
    [items, typeFilter],
  );

  return (
    <>
      <ModuleHeader
        title="套餐"
        description="价格、流量、设备数、上下架。价格数字全部来自 API。"
        priority="P1"
        mobile="M3"
        actions={
          <Button tone="primary" onClick={() => setEditor('new')}>
            新建套餐
          </Button>
        }
      />

      <DangerOpsNote codes={['D8']} />
      <PricingScopeNotice />

      {editor !== null ? (
        <div className="mb-5">
          <PlanEditor
            plan={editor === 'new' ? null : editor}
            onDone={closeAndReload}
            onCancel={() => setEditor(null)}
          />
        </div>
      ) : null}

      {plans.state === 'loading' ? <ListLoading /> : null}

      {plans.state === 'error' && plans.error !== null ? (
        <QueryErrorState error={plans.error} what="套餐列表" onRetry={plans.reload} />
      ) : null}

      {plans.state === 'ready' && items.length === 0 ? (
        <EmptyState
          title="还没有套餐"
          description="至少要有一个上架的套餐，用户面板的 /plan 才不是空的。"
          action={
            <Button tone="primary" onClick={() => setEditor('new')}>
              新建套餐
            </Button>
          }
        />
      ) : null}

      {plans.state === 'ready' && items.length > 0 ? (
        <Card>
          <CardTitle
            hint={`共 ${items.length} 个${typeFilter === 'all' ? '' : `，当前筛选下 ${shown.length} 个`}`}
          >
            套餐列表
          </CardTitle>

          {/* 客户端筛选在这一页是**安全**的：listAdminPlans 没有分页参数，
              手上这份就是全部。别的三页有游标分页，那里的「本页内筛选」会漏行，
              所以那三页的筛选控件旁边都写明了作用范围。 */}
          <div className="mb-3 flex flex-wrap gap-2">
            {(
              [
                ['all', '全部'],
                ['period', '周期套餐'],
                ['traffic_pack', '加油包'],
              ] as const
            ).map(([value, label]) => (
              <Button
                key={value}
                tone={typeFilter === value ? 'primary' : 'default'}
                onClick={() => setTypeFilter(value)}
              >
                {label}
              </Button>
            ))}
          </div>

          <DataTable
            head={['名称', '类型', '各周期价格', '流量', '设备数', '展示', '排序', '']}
          >
            {shown.map((plan) => (
              <Tr key={plan.id}>
                <Td>
                  <span className="font-medium text-fg">{plan.name}</span>
                  <span className="mt-0.5 block font-mono text-xs text-fg-subtle">#{plan.id}</span>
                </Td>
                <Td>
                  <Badge tone={plan.type === 'traffic_pack' ? 'warn' : 'neutral'}>
                    {plan.type === 'traffic_pack' ? '加油包' : '周期套餐'}
                  </Badge>
                </Td>
                <Td>
                  <PriceList prices={plan.prices} />
                </Td>
                <Td className="whitespace-nowrap">{formatBytes(plan.transfer_enable_bytes)}</Td>
                <Td className="whitespace-nowrap">
                  {/* 0 = 不限（契约用 0 表达「不限」，库里是 NULL）。
                      显示成「0 台」会让人以为这个套餐一台设备都连不上。 */}
                  {plan.device_limit > 0 ? `${plan.device_limit} 台` : '不限'}
                </Td>
                <Td>
                  {plan.visible === false ? (
                    <Badge tone="neutral">不展示</Badge>
                  ) : (
                    <Badge tone="ok">展示中</Badge>
                  )}
                </Td>
                <Td className="font-mono">{plan.sort ?? 0}</Td>
                <Td>
                  <Button onClick={() => setEditor(plan)}>编辑 / 下架</Button>
                </Td>
              </Tr>
            ))}
          </DataTable>
        </Card>
      ) : null}
    </>
  );
}

function PriceList({ prices }: { prices: readonly PlanPrice[] }) {
  if (prices.length === 0) {
    // 一个没有任何价格的套餐是买不了的。服务端建套餐时会拒，但库里可能有历史行。
    return <span className="text-danger">没有任何周期价格（这个套餐买不了）</span>;
  }
  return (
    <span className="flex flex-col gap-0.5">
      {prices.map((p) => (
        <span key={p.period} className="whitespace-nowrap">
          <span className="text-fg-muted">{PERIOD_LABEL[p.period] ?? p.period}</span>{' '}
          <span className="font-mono text-fg">{formatCny(p.amount)}</span>
        </span>
      ))}
    </span>
  );
}

/**
 * 「改价只影响新订单」的常驻说明。
 *
 * 放在页面上而不是只放在确认面板里：决定要不要改价的那一刻，人还没点开确认面板。
 */
function PricingScopeNotice() {
  return (
    <Card className="mb-5 border-l-4 border-l-warn">
      <h2 className="text-sm font-semibold text-fg">改套餐只影响新订单</h2>
      <ul className="mt-1.5 list-disc space-y-1 pl-5 text-sm leading-relaxed text-fg-muted">
        <li>
          已售出的流量额度在<strong className="font-medium text-fg">当前周期内不可撤回</strong>
          —— 调小 transfer_enable 不会把已经开通的额度收回来。
        </li>
        <li>
          历史订单的价格快照<strong className="font-medium text-fg">一行都不回改</strong>。
          退款扣减读的是下单时锁定的价格，不是这里的活列 ——
          否则涨价之后退款额会变小，用户会认为我们改价是为了少退钱。
        </li>
        <li>
          待支付订单按下单时锁定的金额结算。而 USDT 支付靠<strong className="font-medium text-fg">金额唯一性</strong>
          匹配，金额一变，已经付出去的那笔就对不上任何订单了。
        </li>
        <li>
          想让<strong className="font-medium text-fg">存量用户</strong>也享受新配额是另一个操作
          （D1 改用户权利，直接等于送钱），走另一条端点、另一套确认。
        </li>
      </ul>
    </Card>
  );
}

/* ────────────────────────── 表单 ────────────────────────── */

interface PlanForm {
  name: string;
  /** 🔴 `null` = 还没选。**不给默认值**，理由见文件头第 2 条。 */
  type: 'period' | 'traffic_pack' | null;
  description: string;
  /** 五个周期的价格，**单位：分**，空串 = 这个周期不卖。 */
  prices: Record<PeriodKey, string>;
  transferGib: string;
  deviceLimit: string;
  speedLimit: string;
  visible: boolean;
  sort: string;
}

function emptyForm(): PlanForm {
  return {
    name: '',
    type: null,
    description: '',
    prices: { monthly: '', quarterly: '', half_yearly: '', yearly: '', onetime: '' },
    transferGib: '',
    deviceLimit: '',
    speedLimit: '0',
    visible: true,
    sort: '0',
  };
}

function formFromPlan(plan: Plan): PlanForm {
  const prices: Record<PeriodKey, string> = {
    monthly: '',
    quarterly: '',
    half_yearly: '',
    yearly: '',
    onetime: '',
  };
  for (const p of plan.prices) {
    // 契约枚举里那两个本系统没有的周期（two_yearly / three_yearly）在这里被跳过：
    // 表单没有它们的输入框，硬塞进去会在保存时被服务端 422 退回。
    if (p.period in prices) prices[p.period as PeriodKey] = String(p.amount);
  }
  return {
    name: plan.name,
    type: plan.type,
    description: plan.description ?? '',
    prices,
    transferGib: bytesToGibText(plan.transfer_enable_bytes),
    deviceLimit: String(plan.device_limit),
    speedLimit: String(plan.speed_limit_mbps ?? 0),
    visible: plan.visible !== false,
    sort: String(plan.sort ?? 0),
  };
}

/** 表单 → `PlanUpsert`（不含 reason，reason 由 `DangerousAction` 收）。 */
export type PlanDraft =
  | { readonly ok: true; readonly value: Omit<PlanUpsert, 'reason'> }
  | { readonly ok: false; readonly problem: string };

/**
 * 表单校验。**导出是为了单测直接打它** —— 这是这一页里唯一「填的东西对不对」的判据，
 * 组件里再写一遍的话，测试绿着而按钮的实际行为可以是另一回事。
 *
 * ⚠️ 这里挡下的每一条**服务端也都挡**（`createAdminPlan` / `parsePlanPrices`）。
 * 它省的是一次注定被 422 退回的往返，不是安全边界。
 */
export function buildPlanDraft(form: PlanForm): PlanDraft {
  const name = form.name.trim();
  if (name === '') return { ok: false, problem: '套餐名不能为空。' };

  // 🔴 第一位判的就是它：没选类型时**不许提交**，见文件头第 2 条。
  if (form.type === null) {
    return {
      ok: false,
      problem:
        '还没选套餐类型。周期套餐与加油包在库里是两种东西（plans.kind），选错要等到有人买了才显形，所以这里没有默认值。',
    };
  }

  const prices: PlanPrice[] = [];
  for (const { key, label } of PLAN_PERIODS) {
    const raw = form.prices[key];
    if (raw.trim() === '') continue;
    const amount = parseInteger(raw);
    if (amount === null) return { ok: false, problem: `${label}价格不是一个整数（单位是分）。` };
    if (amount < 0) return { ok: false, problem: `${label}价格不能是负数。` };
    prices.push({ period: key, amount });
  }
  if (prices.length === 0) {
    return { ok: false, problem: '至少要填一个周期的价格，否则这是一个买不了的套餐。' };
  }
  if (form.type === 'period' && !prices.some((p) => p.period === 'monthly')) {
    // 库里那条 CHECK（plans_cycle_needs_monthly）才是真闸门；这里只是给一句人话。
    return {
      ok: false,
      problem: '周期套餐必须有月付价格：退款金额按月单价折算，没有月价的周期套餐退不了款。',
    };
  }

  const transferBytes = gibTextToBytes(form.transferGib);
  if (transferBytes === null) {
    return { ok: false, problem: '流量额度要填一个数字（GB，最多三位小数）。' };
  }
  const deviceLimit = parseInteger(form.deviceLimit);
  if (deviceLimit === null || deviceLimit < 0) {
    return { ok: false, problem: '设备数要填一个 ≥ 0 的整数（0 = 不限）。' };
  }
  const speedLimit = parseInteger(form.speedLimit);
  if (speedLimit === null || speedLimit < 0) {
    return { ok: false, problem: '限速要填一个 ≥ 0 的整数（0 = 不限速）。' };
  }
  const sort = parseInteger(form.sort);
  if (sort === null) return { ok: false, problem: '排序要填一个整数。' };

  return {
    ok: true,
    value: {
      name,
      type: form.type,
      description: form.description,
      prices,
      transfer_enable_bytes: transferBytes,
      device_limit: deviceLimit,
      speed_limit_mbps: speedLimit,
      visible: form.visible,
      sort,
    },
  };
}

function PlanEditor({
  plan,
  onDone,
  onCancel,
}: {
  plan: Plan | null;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [form, setForm] = useState<PlanForm>(() => (plan === null ? emptyForm() : formFromPlan(plan)));
  const draft = buildPlanDraft(form);

  const set = <K extends keyof PlanForm>(key: K, value: PlanForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));
  const setPrice = (key: PeriodKey, value: string) =>
    setForm((f) => ({ ...f, prices: { ...f.prices, [key]: value } }));

  return (
    <Card className="border-l-4 border-l-accent">
      <CardTitle hint={plan === null ? 'createAdminPlan（D8）' : 'updateAdminPlan（D8）'}>
        {plan === null ? '新建套餐' : `编辑套餐 #${plan.id} · ${plan.name}`}
      </CardTitle>

      {plan !== null ? (
        <>
          {/* 只读区与操作区分开：这几样在 PlanUpsert 里根本没有，改不了，
              但操作者需要知道它们的现值 —— 尤其是重置口径。 */}
          <dl className="mb-4 grid gap-x-6 gap-y-2 rounded-lg border border-line bg-surface-alt p-3 text-sm sm:grid-cols-2">
            <Field label="套餐 ID">
              <span className="font-mono">{plan.id}</span>
            </Field>
            <Field label="流量重置口径（只读）">
              {plan.reset_traffic_method ?? '—'}
              <span className="mt-0.5 block text-xs text-fg-subtle">
                由类型推导，接口传不进来：周期套餐按下单日月重置、加油包永不重置。
              </span>
            </Field>
          </dl>

          <p className="mb-4 rounded-lg border border-warn/30 bg-warn/5 p-3 text-xs leading-relaxed text-fg-muted">
            ⚠️ 保存是<strong className="font-medium text-fg">整体覆写</strong>，不是部分更新。
            名称 / 类型 / 各周期价格 / 流量 / 设备数在契约里都是必填，
            所以这个表单当前显示的每一个值都会原样写进去 —— 留空的周期价格就是
            <strong className="font-medium text-fg">删掉那个周期</strong>。
          </p>
        </>
      ) : null}

      <div className="space-y-4">
        <TextField
          label="套餐名"
          value={form.name}
          onChange={(v) => set('name', v)}
          placeholder="标准版"
        />

        <RadioGroup
          label="套餐类型（必选，没有默认值）"
          value={form.type}
          options={PLAN_TYPES}
          onChange={(v) => set('type', v)}
          hint={
            <>
              🔴 <strong className="font-medium text-fg">这一项刻意没有默认值。</strong>
              库里 <code className="font-mono">plans.kind</code> 是 NOT NULL 且没有 DEFAULT：
              默认成周期套餐会让加油包被静默写错，而错误要等到有人买了、系统凭空给他算了一次折抵才显形。
              {plan !== null ? ' 改已有套餐的类型会改变它的开通与重置逻辑，通常不该改。' : ''}
            </>
          }
        />

        <TextAreaField
          label="套餐说明（Markdown）"
          value={form.description}
          onChange={(v) => set('description', v)}
          rows={3}
          hint="展示在用户面板的套餐页上。"
        />

        <fieldset>
          <legend className="mb-1.5 block text-sm font-medium text-fg">
            各周期价格（单位：分）
          </legend>
          <p className="mb-3 text-xs leading-relaxed text-fg-muted">
            🔴 <strong className="font-medium text-fg">单位是分，不是元。</strong>
            ¥72 要填 <code className="font-mono">7200</code>。右侧会实时回显换算结果，保存前对一眼。
            留空 = 这个周期不卖。
            <span className="mt-1 block">
              契约的周期枚举里还有「两年付 / 三年付」，但本系统的 <code className="font-mono">plans</code>{' '}
              表没有那两列、<code className="font-mono">order_period</code> 枚举里也没有那两个值，
              所以这里不给输入框（填了也会被服务端退回）。
            </span>
          </p>
          <div className="grid gap-4 sm:grid-cols-2">
            {PLAN_PERIODS.map(({ key, label }) => (
              <IntField
                key={key}
                label={label}
                value={form.prices[key]}
                onChange={(v) => setPrice(key, v)}
                placeholder="留空 = 不卖"
                suffix={centsPreview(form.prices[key])}
              />
            ))}
          </div>
        </fieldset>

        <div className="grid gap-4 sm:grid-cols-2">
          <TextField
            label="流量额度（GB）"
            value={form.transferGib}
            onChange={(v) => set('transferGib', v)}
            placeholder="100"
            mono
            hint="契约里是字节，这里按 GiB 输入（1 GB = 1024³ 字节），支持三位小数。"
          />
          <IntField
            label="设备数"
            value={form.deviceLimit}
            onChange={(v) => set('deviceLimit', v)}
            placeholder="5"
            suffix={form.deviceLimit.trim() === '0' ? '不限' : '台'}
          />
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <IntField
            label="限速（Mbps）"
            value={form.speedLimit}
            onChange={(v) => set('speedLimit', v)}
            suffix={form.speedLimit.trim() === '0' ? '不限速' : 'Mbps'}
            hint="第一阶段全部填 0：定价用设备数做杠杆，不用限速。"
          />
          <IntField
            label="排序"
            value={form.sort}
            onChange={(v) => set('sort', v)}
            hint="数字小的排前面。"
          />
        </div>

        <CheckboxField
          label="在用户面板的套餐页展示"
          checked={form.visible}
          onChange={(v) => set('visible', v)}
          hint={
            <>
              取消勾选 = 不在套餐页列出，但<strong className="font-medium text-fg">仍可下单</strong>
              （拿到直链的人买得到）。要真正停售请用下面的「下架」。
            </>
          }
        />

        {!draft.ok ? (
          <p className="rounded-lg border border-line bg-surface-alt px-3 py-2 text-sm text-fg-muted">
            还不能提交：{draft.problem}
          </p>
        ) : null}

        <DangerousAction
          code="D8"
          // title 是折叠状态下那个按钮的字，submitLabel 是展开后真正提交的那个 ——
          // 两者刻意不同名：同名的话，「点开确认面板」与「确认执行」在屏幕上长得一样。
          title={plan === null ? '创建套餐' : '保存套餐改动'}
          submitLabel={plan === null ? '确认创建' : '确认保存'}
          // PlanUpsert.reason 是契约里的 required 字段，服务端按码位数校验（≥ 8）。
          // 登记表 D8 那一行没有「必填原因」，但契约有 —— 以契约为准，往严的方向取。
          requireReason
          disabled={!draft.ok}
          disabledReason={draft.ok ? undefined : draft.problem}
          context={<PlanChangeContext plan={plan} draft={draft} />}
          onSubmit={async (values) => {
            if (!draft.ok) return;
            const body: PlanUpsert = { ...draft.value, reason: values.reason ?? '' };
            if (plan === null) {
              await unwrap(api().POST('/api/v1/admin/plans', { body }));
            } else {
              await unwrap(
                api().PATCH('/api/v1/admin/plans/{id}', {
                  params: { path: { id: plan.id } },
                  body,
                }),
              );
            }
          }}
          onDone={onDone}
        />

        {plan !== null ? <PlanArchiveAction plan={plan} onDone={onDone} /> : null}

        <Button tone="ghost" onClick={onCancel}>
          关闭编辑器
        </Button>
      </div>
    </Card>
  );
}

/** D8 确认面板里那块「必须让操作者看见的事实」。 */
function PlanChangeContext({ plan, draft }: { plan: Plan | null; draft: PlanDraft }) {
  return (
    <>
      <p className="font-medium text-fg">改套餐只影响新订单。</p>
      <p className="mt-1 text-sm leading-relaxed text-fg-muted">
        已售出的流量额度在当前周期内不可撤回，历史订单的价格快照不会回改。
        要让存量用户享受新配额是另一个操作（D1 改用户权利）。
        <span className="mt-1 block text-xs text-fg-subtle">
          这句话会原样进这次操作的审计记录（服务端写的 <code className="font-mono">pricing_scope_note</code>）。
          ⚠️ 它<strong className="font-medium">不是</strong> §6.2 的 L1 确认串 —— 契约的{' '}
          <code className="font-mono">PlanUpsert</code> 里没有确认串字段，
          一个直接 curl 的人读不到这段话。缺口已登记。
        </span>
      </p>
      {plan !== null && draft.ok ? <PlanDiff before={plan} after={draft.value} /> : null}
    </>
  );
}

/**
 * 改动清单。D8 的登记表没有要求 diff（那是 D13），但它几乎不花成本，
 * 而「我以为我只改了排序」是改价事故最常见的开头。
 */
function PlanDiff({ before, after }: { before: Plan; after: Omit<PlanUpsert, 'reason'> }) {
  const rows: Array<{ label: string; from: string; to: string }> = [];
  const push = (label: string, from: string, to: string) => {
    if (from !== to) rows.push({ label, from, to });
  };

  push('名称', before.name, after.name);
  push(
    '类型',
    before.type === 'traffic_pack' ? '加油包' : '周期套餐',
    after.type === 'traffic_pack' ? '加油包' : '周期套餐',
  );
  for (const { key, label } of PLAN_PERIODS) {
    const b = before.prices.find((p) => p.period === key);
    const a = after.prices.find((p) => p.period === key);
    push(
      `${label}价`,
      b === undefined ? '不卖' : formatCny(b.amount),
      a === undefined ? '不卖' : formatCny(a.amount),
    );
  }
  push('流量', formatBytes(before.transfer_enable_bytes), formatBytes(after.transfer_enable_bytes));
  push('设备数', String(before.device_limit), String(after.device_limit));
  push('限速', String(before.speed_limit_mbps ?? 0), String(after.speed_limit_mbps ?? 0));
  push('展示', before.visible === false ? '否' : '是', after.visible === false ? '否' : '是');
  push('排序', String(before.sort ?? 0), String(after.sort ?? 0));

  if (rows.length === 0) {
    return (
      <p className="mt-3 text-sm text-fg-muted">
        与当前值相比<strong className="font-medium text-fg">没有任何改动</strong>
        —— 提交只会往审计表里写一条什么都没变的记录。
      </p>
    );
  }
  return (
    <div className="mt-3">
      <p className="text-sm font-medium text-fg">这次会改动 {rows.length} 项：</p>
      <ul className="mt-1 space-y-0.5 text-sm">
        {rows.map((r) => (
          <li key={r.label}>
            <span className="text-fg-muted">{r.label}</span>{' '}
            <span className="font-mono text-fg-subtle line-through">{r.from}</span>
            <span className="text-fg-muted"> → </span>
            <span className="font-mono font-medium text-fg">{r.to}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * 下架套餐（`deleteAdminPlan`，D8）。
 *
 * 🔴 **这是下架不是删除**：服务端做的是 archive（visible / sellable 一起置 false），
 * 老用户继续用、新用户买不到。文案必须说清楚，否则会有人为了「暂时停售」而不敢点，
 * 转去把价格改成一个天文数字 —— 那才是真的会伤到人的做法。
 *
 * ⚠️ **不收原因**：`DELETE /plans/{id}` 在契约上没有请求体，收上来的原因发不出去。
 * 服务端已经就此记了 WARN（`bp_admin_audit_no_reason`）。
 */
function PlanArchiveAction({ plan, onDone }: { plan: Plan; onDone: () => void }) {
  return (
    <DangerousAction
      code="D8"
      title="下架这个套餐"
      submitLabel="确认下架"
      context={
        <>
          <p className="font-medium text-fg">下架 ≠ 删除。</p>
          <ul className="mt-1 list-disc space-y-1 pl-5 text-sm leading-relaxed text-fg-muted">
            <li>
              已经买了这个套餐的用户<strong className="font-medium text-fg">继续正常使用</strong>
              ，到期也能续费；只是新用户买不到了。
            </li>
            <li>
              如果还有<strong className="font-medium text-fg">未结算的订单</strong>，服务端会拒绝这次下架
              （409）—— 那些人的钱可能已经在链上了，下架会让他们的支付走进「套餐不存在」。
            </li>
            <li>
              ⚠️ 这一条<strong className="font-medium text-fg">不会写下操作原因</strong>：
              契约给 DELETE 端点没有请求体，原因无处安放。审计里会有完整的改前值，但没有「为什么」。
            </li>
          </ul>
          <p className="mt-2 text-sm">
            目标：<span className="font-mono font-medium text-fg">#{plan.id} {plan.name}</span>
          </p>
        </>
      }
      onSubmit={async () => {
        await unwrapEmpty(
          api().DELETE('/api/v1/admin/plans/{id}', { params: { path: { id: plan.id } } }),
        );
      }}
      onDone={onDone}
    />
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs text-fg-muted">{label}</dt>
      <dd className="mt-0.5 text-fg">{children}</dd>
    </div>
  );
}
