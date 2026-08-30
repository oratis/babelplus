/**
 * 模块 5 · 节点详情 `/admin/nodes/:id` —— P1 / M2。
 *
 * 🔴 D9「改节点协议参数」有一个很阴的失败模式：
 * Xray 保留了 `clients` → `users` 这类**静默别名**，写错不报错，只是行为不符预期。
 * 所以**保存前必须做 JSON schema 校验**，并保留上一版可一键回滚 ——
 * 「保存成功但节点静默不可用」是最难排查的一类故障。
 *
 * # 🔴 上面这条要求，当前契约做不到 —— 说清楚它为什么做不到
 *
 * `AdminNodeUpsert` 的全部字段是：`name` `type` `host` `port` `region` `enabled`
 * `group_ids` `multiplier_e9` `reason`。**没有任何一个协议参数 JSON 字段**，
 * 也没有任何端点返回上一版。于是 D9 在这一页上只剩下「改连接信息」这一半：
 * 协议参数编辑器、schema 校验、一键回滚三样**都不是没接线，是接不了**。
 * 这条缺口写在界面上（`<ContractGapNotice>`）而不是只写在注释里 ——
 * 悄悄不显示会让缺口从评审视野里消失，下一个人只会以为「产品文档写多了」。
 *
 * 能做的那一半照做：**保存前把改动列成 diff 摆在确认按钮上方**。
 * D9 的危害是「改错了不报错」，而 diff 是这一页上唯一能让人在提交前
 * 看见「我到底改了什么」的东西。
 *
 * # 为什么这一页也有启停按钮
 *
 * 本页的 `ModuleScaffold` 只声明了 `getAdminNode` / `updateAdminNode` /
 * `deleteAdminNode`，启停在列表页。但**只放删除、不放停用**会造出一个很坏的局面：
 * 人在详情页上确认「就是这台机器出事了」，眼前唯一能让它停下来的按钮是**删除**。
 * 停用是可逆的，删除不是。所以这里多接了 enable / disable 两条
 * （它们已实现，且与列表页是同一对端点）。
 */
import { useEffect, useId, useMemo, useState, type ReactNode } from 'react';
import { useNavigate, useParams } from 'react-router';
import { ApiError, unwrap, unwrapEmpty } from '@babelplus/shared/api';
import { PageHeader } from '@babelplus/shared/ui';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  Icon,
  LinkButton,
  LoadingState,
  formatBytes,
  formatDateTime,
} from './_imports.ts';
import { DangerousAction } from '../components/DangerousAction.tsx';
import { api } from '../lib/api.ts';
import {
  ContractGapNotice,
  DangerSummary,
  FormAlert,
  NODE_PROTOCOLS,
  NodeEnabledBadge,
  NodeHealthBadge,
  QueryErrorState,
  SelectField,
  TextField,
  Unknown,
  asApiError,
  nodeHealth,
  useApiQuery,
  type AdminNode,
  type AdminNodeUpsert,
} from './node-common.tsx';

function getAdminNode(id: number): Promise<AdminNode> {
  return unwrap(api().GET('/api/v1/admin/nodes/{id}', { params: { path: { id } } }));
}

export default function NodeDetailPage() {
  const { id: rawId } = useParams();
  const navigate = useNavigate();

  // 地址栏里的 id 可能是任何东西。`Number.parseInt('12abc')` 会给出 12，
  // 所以用严格判据 —— 拿一个猜出来的 id 去查，查到的是**另一台机器**的详情，
  // 而这一页上有删除按钮。
  const id = /^\d+$/.test(rawId ?? '') ? Number(rawId) : null;

  const query = useApiQuery(
    () => (id === null ? Promise.reject(new Error('id 不是数字')) : getAdminNode(id)),
    [id],
    '节点详情加载失败',
  );
  const node = query.data;

  const [pending, setPending] = useState(false);
  const [toggleError, setToggleError] = useState<ApiError | null>(null);

  /**
   * 切启停。**故意会抛** —— 停用走 `<DangerousAction>`，
   * 那个组件要自己接住 `ApiError` 才能按 `ErrorCode` 渲染文案（`dangerErrorCopy`）。
   * 在这里 catch 掉的话，一次失败的停用在确认面板上看起来和成功一模一样：
   * 面板关闭、没有报错，而那台机器还在跑。
   */
  async function toggleEnabled(nodeId: number, next: boolean): Promise<void> {
    const path = next ? '/api/v1/admin/nodes/{id}/enable' : '/api/v1/admin/nodes/{id}/disable';
    const updated = await unwrap(api().POST(path, { params: { path: { id: nodeId } } }));
    // 三态纪律：写成功后把新实体补进来，**不把整页打回 loading** ——
    // 刚停用完一台出事的机器，页面变成骨架屏会让人以为没生效。
    query.patch(() => updated);
  }

  /** 启用不是危险操作，没有确认面板接错误，所以这一条自己接。 */
  async function enableNode(nodeId: number): Promise<void> {
    setPending(true);
    setToggleError(null);
    try {
      await toggleEnabled(nodeId, true);
    } catch (cause) {
      setToggleError(asApiError(cause, '启用节点失败'));
    } finally {
      setPending(false);
    }
  }

  if (id === null) {
    return (
      <>
        <PageHeader title="节点详情" description="地址栏里的 id 不是一个数字。" />
        <EmptyState
          title="这不是一个节点 id"
          description={`「${rawId ?? ''}」不是数字。节点 id 是整数主键，多半是链接被截断了。`}
          action={
            <LinkButton tone="primary" href="/admin/nodes">
              回到节点列表 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      </>
    );
  }

  return (
    <>
      <PageHeader
        title="节点详情"
        description={
          <>
            节点 <code className="font-mono text-fg">{node?.name ?? rawId ?? '—'}</code>
          </>
        }
        meta={
          <>
            <Badge tone="info">P1</Badge>
            <Badge tone="warn">M2 · 手机上核心操作必须能完成，表格卡片化降级</Badge>
            {node ? <NodeEnabledBadge enabled={node.enabled} /> : null}
            {node ? <NodeHealthBadge health={nodeHealth(node)} /> : null}
          </>
        }
        actions={
          <LinkButton href="/admin/nodes">
            <Icon.ArrowRight size={14} /> 节点列表
          </LinkButton>
        }
      />

      <DangerSummary codes={['D4', 'D9']} />

      {query.state === 'loading' ? <LoadingState /> : null}

      {/* 404 单独一支：这不是「加载失败」，而是「这台机器不在了」——
          后者要给的是一条回列表的路，不是一个重试按钮。 */}
      {query.state === 'error' && query.error?.code === 'RESOURCE_NOT_FOUND' ? (
        <EmptyState
          title="找不到这个节点"
          description="它可能已经被删除。"
          action={
            <LinkButton tone="primary" href="/admin/nodes">
              回到节点列表 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      ) : null}

      {query.state === 'error' && query.error && query.error.code !== 'RESOURCE_NOT_FOUND' ? (
        <QueryErrorState error={query.error} what="节点详情" onRetry={query.reload} />
      ) : null}

      {query.state === 'ready' && node ? (
        <div className="space-y-4">
          {/* 只读区与操作区分开：读的时候不该有任何东西可以被误点。 */}
          <ReadOnlyCard node={node} />
          <LoadCard node={node} />

          <EditCard node={node} onSaved={(updated) => query.patch(() => updated)} />

          <Card className="border-l-4 border-l-danger">
            <CardTitle hint="D4 · 会让这台机器上的用户在 60 秒内掉线">危险操作</CardTitle>
            <div className="mt-4 space-y-4">
              <div>
                {node.enabled ? (
                  <DangerousAction
                    code="D4"
                    title="停用这个节点"
                    submitLabel="确认停用"
                    confirmation={node.name}
                    context={<StopContext node={node} kind="disable" />}
                    onSubmit={async () => {
                      await toggleEnabled(node.id, false);
                    }}
                  />
                ) : (
                  <div>
                    <Button onClick={() => void enableNode(node.id)} disabled={pending}>
                      {pending ? '启用中…' : '启用'}
                    </Button>
                    <p className="mt-2 text-xs leading-relaxed text-fg-muted">
                      启用不是 D4：它不会让任何人掉线。但要注意这台机器
                      {node.group_ids && node.group_ids.length > 0
                        ? '会立刻进入对应分组用户的订阅'
                        : '不属于任何分组，启用后仍然对所有用户不可见'}
                      。
                    </p>
                  </div>
                )}
                {toggleError ? (
                  <div className="mt-2">
                    <FormAlert>{toggleError.message}</FormAlert>
                  </div>
                ) : null}
              </div>

              <DangerousAction
                code="D4"
                title="删除这个节点"
                submitLabel="确认删除"
                confirmation={node.name}
                requireReason
                context={<StopContext node={node} kind="delete" />}
                onSubmit={async (values) => {
                  // 与停用不同，删除的确认串与原因**是服务端自己查出节点名再比对的**
                  //（§6.2 L1），前端这一份只是提前告诉你打对了没有。
                  await unwrapEmpty(
                    api().DELETE('/api/v1/admin/nodes/{id}', {
                      params: { path: { id: node.id } },
                      body: {
                        confirmation: values.confirmation ?? '',
                        reason: values.reason ?? '',
                      },
                    }),
                  );
                }}
                onDone={() => void navigate('/admin/nodes', { replace: true })}
              />
            </div>
          </Card>
        </div>
      ) : null}
    </>
  );
}

/* ────────────────────────────── 只读区 ────────────────────────────── */

function ReadOnlyCard({ node }: { node: AdminNode }) {
  return (
    <Card>
      <CardTitle hint="getAdminNode">基础</CardTitle>
      <dl className="mt-4 grid grid-cols-2 gap-x-4 gap-y-3 text-sm sm:grid-cols-3 lg:grid-cols-4">
        <Item label="id">
          <span className="font-mono">#{node.id}</span>
        </Item>
        <Item label="名称">{node.name}</Item>
        <Item label="协议">
          <span className="font-mono text-xs">{node.type}</span>
        </Item>
        <Item label="地区">{node.region || <Unknown title="没有填地区" />}</Item>
        <Item label="Host">
          <span className="font-mono text-xs">{node.host ?? '—'}</span>
        </Item>
        <Item label="端口">
          <span className="font-mono text-xs">{node.port ?? '—'}</span>
        </Item>
        <Item label="分组">
          {node.group_ids && node.group_ids.length > 0 ? (
            <span className="font-mono text-xs">{node.group_ids.join(', ')}</span>
          ) : (
            <span className="text-warn" title="不属于任何分组的节点对所有用户不可见">
              无分组
            </span>
          )}
        </Item>
        <Item label="启停">
          <NodeEnabledBadge enabled={node.enabled} />
        </Item>
        <Item label="最后上报">
          {node.last_push_at ? formatDateTime(node.last_push_at) : <Unknown title="从没上报过" />}
        </Item>
        <Item label="最后负载上报">
          {node.last_status_at ? formatDateTime(node.last_status_at) : <Unknown title="从没报过负载" />}
        </Item>
        <Item label="config_rev">
          <RevValue value={node.config_rev} />
        </Item>
        <Item label="user_rev">
          <RevValue value={node.user_rev} />
        </Item>
      </dl>

      <div className="mt-4 space-y-3">
        <ContractGapNotice>
          <strong className="font-medium text-fg">排序、标签、上下架、倍率这四样这一页没有。</strong>
          page-inventory §4.3 把它们列进了「能改什么」，但契约的{' '}
          <code className="font-mono">AdminNode</code> / <code className="font-mono">AdminNodeUpsert</code>{' '}
          里没有对应字段（<code className="font-mono">multiplier_e9</code> 有字段但服务端恒返 null ——
          product-brief §6 定的是第一阶段不引入倍率，库里根本没建这一列）。
        </ContractGapNotice>
      </div>
    </Card>
  );
}

/** `config_rev` / `user_rev` 是 ETag 的版本源。null 有专门的含义，不能显示成 0。 */
function RevValue({ value }: { value: number | undefined }) {
  if (typeof value === 'number') return <span className="font-mono text-xs">{value}</span>;
  return (
    <span
      className="text-warn"
      title="node_rev 里没有这一行：这台机器建的时候漏了 InitNodeRev，它的 ETag 从此不工作"
    >
      缺失
    </span>
  );
}

/**
 * 负载快照。
 *
 * 🔴 **没有 `load_status` 时不画一份全 0 的**。服务端刻意在没上报过时不给这个对象
 *（`adminNodeView` 的注释：全 0 在后台看起来是「这台机器很空闲」，
 * 恰恰是把「我们不知道」渲染成了最让人放心的那个样子）。前端跟着它。
 */
function LoadCard({ node }: { node: AdminNode }) {
  const load = node.load_status;
  return (
    <Card>
      <CardTitle hint={load ? `快照时间 ${formatDateTime(node.last_status_at)}` : undefined}>负载</CardTitle>
      {load ? (
        <dl className="mt-4 grid grid-cols-2 gap-x-4 gap-y-3 text-sm sm:grid-cols-4">
          <Item label="CPU">{load.cpu.toFixed(1)}%</Item>
          <Item label="内存">
            <Usage used={load.mem.used} total={load.mem.total} />
          </Item>
          <Item label="Swap">
            <Usage used={load.swap.used} total={load.swap.total} />
          </Item>
          <Item label="磁盘">
            <Usage used={load.disk.used} total={load.disk.total} />
          </Item>
        </dl>
      ) : (
        <p className="mt-3 text-sm leading-relaxed text-fg-muted">
          这台机器<strong className="font-medium text-fg">没有负载快照</strong>。
          两种可能：它从没报过负载（签发密钥时没给{' '}
          <code className="font-mono">node:status:write</code> 这个 scope 就报不了），
          或者数据库重启把那张 UNLOGGED 表清空了。
          <strong className="font-medium text-fg"> 这里不显示 0</strong> —— 「我们不知道」
          和「它很空闲」是两件事。
        </p>
      )}
    </Card>
  );
}

function Usage({ used, total }: { used: number; total: number }) {
  if (total <= 0) return <Unknown title="上报的 total 是 0，算不出占比" />;
  const pct = Math.round((used / total) * 100);
  return (
    <span className={pct >= 90 ? 'text-warn' : undefined}>
      {formatBytes(used)} / {formatBytes(total)}（{pct}%）
    </span>
  );
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-fg-subtle">{label}</dt>
      <dd className="mt-0.5 truncate text-fg">{children}</dd>
    </div>
  );
}

/* ────────────────────────────── 编辑区（D9） ────────────────────────────── */

interface EditForm {
  name: string;
  type: string;
  host: string;
  port: string;
  region: string;
  groupIds: string;
}

function formFromNode(node: AdminNode): EditForm {
  return {
    name: node.name,
    type: node.type,
    host: node.host ?? '',
    port: node.port === undefined ? '' : String(node.port),
    region: node.region ?? '',
    groupIds: (node.group_ids ?? []).join(', '),
  };
}

/** `"1, 2, 2"` → `[1, 2]`。解析不出来时返回 null，调用方据此挡住提交。 */
function parseGroupIds(raw: string): number[] | null {
  const parts = raw
    .split(/[,，\s]+/)
    .map((s) => s.trim())
    .filter((s) => s !== '');
  const out: number[] = [];
  for (const p of parts) {
    if (!/^\d+$/.test(p)) return null;
    const n = Number(p);
    if (n <= 0) return null;
    if (!out.includes(n)) out.push(n);
  }
  return out;
}

function EditCard({ node, onSaved }: { node: AdminNode; onSaved: (node: AdminNode) => void }) {
  const fieldId = useId();
  const [form, setForm] = useState<EditForm>(() => formFromNode(node));

  // 服务端返回新实体后，表单要跟着走 —— 否则保存完之后表单里还是旧值，
  // 而那些旧值会在下一次保存时被当成「改动」再写一遍。
  useEffect(() => setForm(formFromNode(node)), [node]);

  const groupIds = parseGroupIds(form.groupIds);
  const portNumber = Number.parseInt(form.port.trim(), 10);
  const portOk = Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65535;

  const changes = useMemo(
    () => diffNode(node, form, groupIds, portOk ? portNumber : null),
    [node, form, groupIds, portOk, portNumber],
  );

  const shapeProblem =
    form.name.trim() === ''
      ? '名称不能为空。'
      : !portOk
        ? '端口必须是 1–65535 之间的整数。'
        : groupIds === null
          ? '分组只能填正整数，用逗号或空格分隔。'
          : null;

  const blocked = shapeProblem ?? (changes.length === 0 ? '还没有任何改动。' : null);

  return (
    <Card>
      <CardTitle hint="D9 · updateAdminNode（写操作会 bump config_rev）">连接信息</CardTitle>

      <div className="mt-4 grid gap-4 sm:grid-cols-2">
        <TextField
          id={`${fieldId}-name`}
          label="名称"
          value={form.name}
          onChange={(v) => setForm((f) => ({ ...f, name: v }))}
          hint="改名会改掉删除节点时要输的确认串 —— 它比对的是当前的 name。"
        />
        <SelectField
          id={`${fieldId}-type`}
          label="协议"
          value={form.type}
          onChange={(v) => setForm((f) => ({ ...f, type: v }))}
          options={
            // 库里可能躺着一个不在这四个里的值（历史数据）。**不能把它从下拉里抹掉** ——
            // 抹掉的话，一次「只想改端口」的保存会顺手把协议换成列表里的第一个。
            NODE_PROTOCOLS.some((p) => p.value === form.type)
              ? NODE_PROTOCOLS
              : [{ value: form.type, label: `${form.type}（不在已知的四个协议里）` }, ...NODE_PROTOCOLS]
          }
        />
        <TextField
          id={`${fieldId}-host`}
          label="Host"
          value={form.host}
          onChange={(v) => setForm((f) => ({ ...f, host: v }))}
          mono
        />
        <TextField
          id={`${fieldId}-port`}
          label="端口"
          value={form.port}
          onChange={(v) => setForm((f) => ({ ...f, port: v }))}
          mono
          inputMode="numeric"
        />
        <TextField
          id={`${fieldId}-region`}
          label="地区"
          value={form.region}
          onChange={(v) => setForm((f) => ({ ...f, region: v }))}
        />
        <TextField
          id={`${fieldId}-groups`}
          label="分组 id"
          value={form.groupIds}
          onChange={(v) => setForm((f) => ({ ...f, groupIds: v }))}
          mono
          hint="逗号分隔。清空 = 不属于任何分组，那样这台机器对所有用户不可见。"
        />
      </div>

      <div className="mt-4 space-y-3">
        <ContractGapNotice>
          <strong className="font-medium text-fg">协议参数 JSON 编辑器不在这里，也接不上。</strong>
          契约的 <code className="font-mono">AdminNodeUpsert</code> 只有 name / type / host / port /
          region / enabled / group_ids / multiplier_e9 / reason，
          <strong className="font-medium text-fg">没有任何协议参数字段</strong>，
          也没有端点返回上一版。所以 D9 要求的「保存前 JSON schema 校验 + 上一版一键回滚」
          现在做不了 —— 那条要求本身是对的（Xray 保留了 <code className="font-mono">clients</code> →{' '}
          <code className="font-mono">users</code> 这类静默别名，写错不报错），
          补法是给契约加一个协议参数字段与一个版本历史端点。
        </ContractGapNotice>
        <p className="text-xs leading-relaxed text-fg-subtle">
          这里<strong className="font-medium text-fg-muted">不发 enabled 字段</strong>：
          PATCH 带上它就等于绕过 D4 的确认流程把一台机器停掉，
          而「改一下端口」顺手把人踢下线正是最不该发生的那种意外。启停走上面的危险操作区。
        </p>
      </div>

      <div className="mt-4">
        <DangerousAction
          code="D9"
          title="保存连接信息"
          submitLabel="确认保存"
          requireReason
          disabled={blocked !== null}
          disabledReason={blocked}
          context={<ChangeList changes={changes} />}
          onSubmit={async (values) => {
            // 🔴 **只发改动过的字段**（name / type 除外，契约要求必填）。
            //    把整张表单发回去的话，别人在我打开这一页之后改的字段
            //    会被我手里的旧值悄悄覆盖 —— 而 PATCH 没有任何并发保护。
            const body: AdminNodeUpsert = {
              name: form.name.trim(),
              type: form.type,
              reason: values.reason ?? '',
            };
            for (const c of changes) {
              if (c.field === 'host') body.host = form.host.trim();
              if (c.field === 'port') body.port = portNumber;
              if (c.field === 'region') body.region = form.region.trim();
              if (c.field === 'group_ids') body.group_ids = groupIds ?? [];
            }
            const updated = await unwrap(
              api().PATCH('/api/v1/admin/nodes/{id}', {
                params: { path: { id: node.id } },
                body,
              }),
            );
            onSaved(updated);
          }}
        />
      </div>
    </Card>
  );
}

interface FieldChange {
  field: string;
  label: string;
  before: string;
  after: string;
}

/** 表单与当前实体的差异。**D9 要求「展示 diff」，这是它在当前契约下能做到的那一半。** */
function diffNode(
  node: AdminNode,
  form: EditForm,
  groupIds: number[] | null,
  port: number | null,
): FieldChange[] {
  const out: FieldChange[] = [];
  const push = (field: string, label: string, before: string, after: string) => {
    if (before !== after) out.push({ field, label, before, after });
  };

  push('name', '名称', node.name, form.name.trim());
  push('type', '协议', node.type, form.type);
  push('host', 'Host', node.host ?? '', form.host.trim());
  push('port', '端口', node.port === undefined ? '' : String(node.port), port === null ? '' : String(port));
  push('region', '地区', node.region ?? '', form.region.trim());
  if (groupIds !== null) {
    push('group_ids', '分组', (node.group_ids ?? []).join(', '), groupIds.join(', '));
  }
  return out;
}

function ChangeList({ changes }: { changes: readonly FieldChange[] }) {
  if (changes.length === 0) return <p>还没有任何改动。</p>;
  return (
    <div>
      <p className="font-medium">这次会改这 {changes.length} 处：</p>
      <ul className="mt-2 space-y-1">
        {changes.map((c) => (
          <li key={c.field} className="font-mono text-xs">
            <span className="text-fg-muted">{c.label}：</span>
            <span className="text-danger line-through">{c.before || '（空）'}</span>
            <span className="mx-1.5 text-fg-subtle">→</span>
            <span className="text-ok">{c.after || '（空）'}</span>
          </li>
        ))}
      </ul>
      <p className="mt-2 text-xs leading-relaxed text-fg-muted">
        保存会 bump <code className="font-mono">config_rev</code>，
        各节点在下一次 60 秒轮询时拉到新配置。
      </p>
    </div>
  );
}

/* ────────────────────────────── 危险操作的事实块 ────────────────────────────── */

function StopContext({ node, kind }: { node: AdminNode; kind: 'disable' | 'delete' }) {
  return (
    <div className="space-y-2">
      <p>
        {kind === 'delete' ? '删除' : '停用'}会让{' '}
        <strong className="font-semibold">这台机器上的所有在线用户在 60 秒内掉线</strong>。
        {kind === 'delete' ? (
          <>
            {' '}
            删除还会<strong className="font-semibold">连带删掉它的全部密钥</strong>，
            并且这台机器再也不会出现在任何订阅里。
          </>
        ) : (
          ' 停用是可逆的：改回启用后，节点在下一次轮询时恢复服务。'
        )}
      </p>
      <p className="text-fg-muted">
        <code className="font-mono text-fg">{node.name}</code>（#{node.id} · {node.host ?? '—'}
        {node.port ? `:${node.port}` : ''}）
        {node.last_push_at
          ? `，最后上报 ${formatDateTime(node.last_push_at)}`
          : '，从没上报过 —— 它可能本来就没在跑'}
        。
      </p>
      <ContractGapNotice>
        <strong className="font-medium text-fg">当前在线人数这里显示不出来。</strong>
        D4 的登记表要求「确认框内必须显示当前在线人数」，但契约的{' '}
        <code className="font-mono">AdminNode</code> 里没有这个字段。
        {kind === 'delete' ? (
          <>
            {' '}
            服务端知道这个数：确认串打错时，那条 422
            的消息里会报出「上报在线 N 人、我们观测到 M 人」——
            <strong className="font-medium text-fg">两个数差得多说明这台机器状态不可信，不要删</strong>。
          </>
        ) : (
          <>
            {' '}
            契约给 <code className="font-mono">disableAdminNode</code> 连请求体都没有，
            所以这里打进去的确认串<strong className="font-medium text-fg">发不到服务端</strong>，
            审计里的 reason 也会是空的。这一层只是让你停一秒看清楚要停哪台。
          </>
        )}
      </ContractGapNotice>
      {kind === 'delete' ? (
        <p className="text-xs leading-relaxed text-fg-muted">
          删除是<strong className="font-medium text-fg">软删除</strong>（服务端标记{' '}
          <code className="font-mono">deleted_at</code>），但后台没有恢复入口 ——
          就当它不可逆。只是想让它停下来的话，用上面的停用。
        </p>
      ) : null}
    </div>
  );
}
