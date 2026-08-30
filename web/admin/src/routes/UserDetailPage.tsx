/**
 * 模块 2 · 用户详情 `/admin/users/:id` —— P1 / M3。五条危险操作全在这一页。
 *
 * 每一个危险按钮都走同一个 `DangerousAction` 组件：它负责把 §6.2 四层所需的参数
 * （L1 确认串 / L2 必填原因 / L3 TOTP）收齐，交给这里的 `onSubmit` 去发请求。
 * **页面里不许各写各的确认弹窗** —— 写五遍就会漏掉一遍，而漏掉的那一遍一定是最贵的那个按钮。
 *
 * 🔴 **四层全部在服务端强制。** 这一页所有的「按钮变灰」都只是省一次注定失败的往返：
 * 确认串由服务端自己查出期望值后常数时间比对，原因由服务端数码位，TOTP 由中间件校验并占用。
 * 前端只是把参数原样送上去 —— 对一个直接 `curl` 的人，这一页的每一行代码都等于零。
 *
 * # 这一页刻意做成「只读区与操作区分离」
 *
 * 上半页是**事实**（他是谁、有多少配额、到期没有），下半页是**动作**。
 * 混排的后果很具体：一个「改配额」的输入框紧挨着「当前配额」时，人会以为那是同一个东西的
 * 可编辑态，于是把它当成表单直接改 —— 而它其实是一条要写审计、要填原因的 D1。
 *
 * # 三处必须说出口的契约缺口（都不是本页能修的）
 *
 * 1. **「置空」表达不出来。** `expired_at` / `device_limit` 的 NULL 是有意义的值
 *    （不限时 / 不限设备），但 JSON 的「字段缺席」与「字段为 null」在服务端都是 nil，
 *    只能理解成「不改」。**所以后台无法把一个用户改成不限时。**
 * 2. **uuid 看得见改不着，也看不见当前值。** 契约的 `AdminUser` 没有 uuid 字段
 *    （它是节点侧的连接凭据，拿到它 + 节点地址就能直接连），所以这里只能写入新值。
 * 3. **历史（订单史 / 工单史 / 余额流水 / 设备列表 / 订阅拉取审计）没有按用户过滤的入口。**
 *    见下面 `HistoryCard` 的注释 —— 那一块**不假装**能查，它老老实实说去哪儿查。
 */
import { useId, useState, type ReactNode } from 'react';
import { Link, useParams } from 'react-router';
import { ApiError, unwrap } from '@babelplus/shared/api';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  Icon,
  LinkButton,
  cx,
  formatBytes,
  formatCny,
  formatDateTime,
} from './_imports.ts';
import { DangerousAction } from '../components/DangerousAction.tsx';
import { api } from '../lib/api.ts';
import {
  DangerOpsNote,
  FieldShell,
  ListSkeleton,
  MISSING,
  ModuleHeader,
  QueryErrorState,
  TextField,
  UserStatusBadges,
  formatCount,
  isNotImplemented,
  parseInteger,
  trafficText,
  useApiQuery,
  usedBytes,
  type AdminUser,
  type AdminUserPatch,
  type RevokeAllResult,
  type Wallet,
} from './UsersPage.tsx';

/** 1 GiB。配额在契约里是**字节**，而人是按 GiB 想的，所以输入框收 GiB、请求发字节。 */
const GIB = 1024 * 1024 * 1024;

/** 节点拉用户表的周期。封禁 / 解封的生效延迟上限就是它（服务端逐字写着同一个数）。 */
const NODE_POLL_SECONDS = 60;

export default function UserDetailPage() {
  const { id } = useParams();
  const userId = Number(id);
  const validId = Number.isSafeInteger(userId) && userId > 0;

  const query = useApiQuery(
    () => unwrap(api().GET('/api/v1/admin/users/{id}', { params: { path: { id: userId } } })),
    [userId],
    '用户详情加载失败',
  );

  /**
   * 写操作返回的新实体**就地覆盖**，不重新 GET。
   *
   * 三态纪律：D1 / D2 成功后服务端返回的就是**改后的完整 `AdminUser`**，
   * 再发一次 GET 既多一次往返，又会把这一页打回骨架屏 ——
   * 操作者刚点完封禁，眼前的人突然消失，第一反应是「是不是没生效」，然后再点一次。
   */
  const [patched, setPatched] = useState<AdminUser | null>(null);
  /**
   * 🔴 **覆盖必须按 id 收口。** 路由参数变了（点「邀请人」跳到另一个用户）时
   * 这个组件**不会重新挂载**，`patched` 会原样留着 —— 于是上一位的写操作结果
   * 会显示在另一个人的页面上，而页面上每一处（邮箱、封禁徽标、余额）都言之凿凿。
   * 这类错误没有任何报错，只会让人对着一个错的人按下一个危险按钮。
   */
  const user = patched !== null && patched.id === userId ? patched : query.data;

  function reloadUser(): void {
    setPatched(null);
    query.reload();
  }

  if (!validId) {
    // 路由参数不是一个正整数 —— 这不是「找不到」，是链接本身坏了。
    // 分开说，否则运维会去查这个 id 的用户为什么没了。
    return (
      <>
        <ModuleHeader
          title="用户详情"
          description="地址里的用户编号不合法"
          priority="P1"
          mobile="M3"
        />
        <EmptyState
          title="这个地址里的用户编号不合法"
          description={
            <>
              <code className="font-mono">{id ?? '（空）'}</code> 不是一个正整数。
              多半是链接被截断或手改过 —— 不是这个用户被删了。
            </>
          }
          action={
            <LinkButton tone="primary" href="/admin/users">
              回到用户列表 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      </>
    );
  }

  return (
    <>
      <ModuleHeader
        title="用户详情"
        description={
          user ? (
            <>
              <span className="font-medium text-fg">{user.email}</span>
              <span className="ml-2 font-mono text-xs text-fg-subtle">#{user.id}</span>
            </>
          ) : (
            <>
              用户 <code className="font-mono text-fg">#{userId}</code>
            </>
          )
        }
        priority="P1"
        mobile="M3"
        actions={
          <LinkButton href="/admin/users">
            <Icon.ArrowRight size={14} /> 用户列表
          </LinkButton>
        }
      />

      <DangerOpsNote codes={['D1', 'D2', 'D3', 'D10']} />

      {query.state === 'loading' ? <ListSkeleton rows={5} /> : null}

      {query.state === 'error' && query.error ? (
        <NotFoundOrError error={query.error} onRetry={query.reload} />
      ) : null}

      {user ? (
        <div className="space-y-4">
          <OverviewCard user={user} />
          <HistoryCard user={user} />

          <section aria-labelledby="danger-heading" className="space-y-4">
            <h2 id="danger-heading" className="pt-2 text-lg font-semibold tracking-tight text-fg">
              操作
            </h2>
            <p className="text-sm leading-relaxed text-fg-muted">
              下面每一条都会写进审计日志（谁、何时、改前值、改后值、你填的原因）。
              审计日志<strong className="font-medium text-fg">没有删除入口，也没有编辑入口</strong>。
            </p>

            <EntitlementCard user={user} onUpdated={setPatched} />
            <BanCard user={user} onUpdated={setPatched} />
            <RevokeSubsCard user={user} onDone={reloadUser} />
            <BalanceCard user={user} onDone={reloadUser} />
          </section>
        </div>
      ) : null}
    </>
  );
}

/**
 * 404 与其它错误分开渲染。
 *
 * 「这个用户不存在（或已注销）」是一个**正常结论**，不是故障 ——
 * 用红色错误块 + 「重试」按钮呈现它，会让人以为是加载失败而反复重试。
 */
function NotFoundOrError({ error, onRetry }: { error: ApiError; onRetry: () => void }) {
  if (error.code === 'RESOURCE_NOT_FOUND') {
    return (
      <EmptyState
        title="找不到这个用户"
        description={
          <>
            id 可能不对，也可能这个账号<strong className="font-medium text-fg">已经注销</strong>。
            注销是匿名化而不是删除：users 上剩下的是一个匿名壳，后台刻意查不到它 ——
            把它当成一个用户展示出来，只会让人以为「这个人还在」。
          </>
        }
        action={
          <LinkButton tone="primary" href="/admin/users">
            回到用户列表 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
      />
    );
  }
  return (
    <QueryErrorState
      error={error}
      what="用户详情"
      onRetry={isNotImplemented(error) ? undefined : onRetry}
    />
  );
}

/* ────────────────────────────── 只读区 ────────────────────────────── */

function Fact({ label, children, hint }: { label: string; children: ReactNode; hint?: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs font-medium text-fg-muted">{label}</dt>
      <dd className="mt-0.5 text-sm break-words text-fg">{children}</dd>
      {hint ? <p className="mt-0.5 text-xs leading-relaxed text-fg-subtle">{hint}</p> : null}
    </div>
  );
}

function OverviewCard({ user }: { user: AdminUser }) {
  const used = usedBytes(user);
  return (
    <Card>
      <CardTitle hint="getAdminUser">概况</CardTitle>
      <dl className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4">
        <Fact label="邮箱">{user.email}</Fact>
        <Fact label="状态">
          <UserStatusBadges user={user} />
        </Fact>
        <Fact label="套餐">{user.plan_name ?? MISSING}</Fact>
        <Fact label="分组 group_id" hint="决定他能看到哪些节点">
          {formatCount(user.group_id)}
        </Fact>
        <Fact label="流量（已用 / 配额）" hint={used === null ? undefined : `已用 ${formatBytes(used)}`}>
          {trafficText(user)}
        </Fact>
        <Fact
          label="到期"
          hint={
            user.expired_at
              ? undefined
              : '没有到期时间 = 不限时。（注意：这个状态只能由套餐给出，后台改不了 —— 契约表达不出「置空」。）'
          }
        >
          {user.expired_at ? formatDateTime(user.expired_at) : '不限时'}
        </Fact>
        <Fact label="设备数上限" hint={user.device_limit === undefined ? '没有值 = 不限设备' : undefined}>
          {user.device_limit === undefined ? '不限' : formatCount(user.device_limit)}
        </Fact>
        <Fact label="余额" hint="仅可消费，不可提现（product-brief §6）">
          {user.balance_amount === undefined ? MISSING : formatCny(user.balance_amount)}
        </Fact>
        <Fact label="注册时间">{formatDateTime(user.created_at)}</Fact>
        <Fact
          label="订阅吊销时间"
          hint={user.sub_revoked_at ? '这个时刻之前签发的订阅 token 全部失效' : undefined}
        >
          {user.sub_revoked_at ? formatDateTime(user.sub_revoked_at) : MISSING}
        </Fact>
        <Fact label="邀请人">
          {user.invited_by_user_id === undefined ? (
            MISSING
          ) : (
            <Link
              to={`/admin/users/${user.invited_by_user_id}`}
              className="font-mono text-accent underline-offset-4 hover:underline"
            >
              #{user.invited_by_user_id}
            </Link>
          )}
        </Fact>
      </dl>

      <p className="mt-4 rounded-lg border border-line bg-surface-alt px-3 py-2 text-xs leading-relaxed text-fg-muted">
        这里<strong className="font-medium text-fg">没有</strong>订阅 token，也没有 uuid ——
        契约的 <code className="font-mono">AdminUser</code> 根本没有这两个字段。
        它们是节点侧的连接凭据：能看到它们的后台，等于给了每个管理员一条 impersonate 的路。
      </p>
    </Card>
  );
}

/**
 * 历史。
 *
 * 🔴 **这一块什么都查不到，而它必须把这件事说清楚，不能留一片空白让人以为「他没有订单」。**
 *
 * 契约里没有任何一个按用户过滤的历史端点：
 *  · `listAdminOrders` 只有 `q`（服务端把它同时匹配 `trade_no` 与 `users.email`）——
 *    所以订单史只能**去订单页搜这个邮箱**，本页不做一个假的「他的订单」列表；
 *  · `listAdminTickets` 连 `q` 都没有，只有分页；
 *  · `listAdminAuditLog` 能按 `action` / `target_type` 过滤，但**没有 `target_id`** ——
 *    过滤出 `target_type=user` 是全体用户的记录，不是这一个人的；
 *  · 余额流水（`/user/wallet/transactions`）与设备列表（`/user/devices`）都是**用户面**端点，
 *    管理员读不到别人的；
 *  · 「订阅拉取审计」在契约里没有任何 operation。
 *
 * 假装能查的代价是具体的：一个空的「订单史」区块会被读成「这个人从没下过单」，
 * 而那正是判断「要不要给他补单」时最关键的一句话。
 */
function HistoryCard({ user }: { user: AdminUser }) {
  return (
    <Card>
      <CardTitle>历史</CardTitle>
      <p className="text-sm leading-relaxed text-fg-muted">
        契约里<strong className="font-medium text-fg">没有</strong>按用户过滤的历史端点，
        所以这一页不显示他的订单史 / 工单史 / 余额流水 / 设备列表 / 订阅拉取审计。
        <strong className="font-medium text-fg">这是缺口，不是「他没有记录」</strong> —— 去下面这些地方查：
      </p>
      <ul className="mt-3 space-y-2 text-sm leading-relaxed text-fg-muted">
        <li>
          <strong className="font-medium text-fg">订单史</strong> ——
          订单列表的搜索同时匹配订单号与用户邮箱，把这个邮箱粘进去即可：
          <LinkButton className="ml-2" href={`/admin/orders?q=${encodeURIComponent(user.email)}`}>
            去订单页搜 {user.email} <Icon.ArrowRight size={14} />
          </LinkButton>
        </li>
        <li>
          <strong className="font-medium text-fg">这个人身上发生过的管理操作</strong> ——
          审计日志能按 <code className="font-mono">target_type=user</code> 过滤，
          但<strong className="font-medium text-fg">不能</strong>按 target_id，
          所以要在结果里自己找 <code className="font-mono">#{user.id}</code>：
          <LinkButton className="ml-2" href="/admin/audit">
            去审计日志 <Icon.ArrowRight size={14} />
          </LinkButton>
        </li>
        <li>
          <strong className="font-medium text-fg">工单史 / 余额流水 / 设备列表</strong> ——
          管理面没有入口（前两个是用户面端点，工单列表没有按用户筛选的参数）。
          需要时只能按工单队列翻，或直接查库。
        </li>
      </ul>
      <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
        「订阅拉取审计是识别账号共享的唯一数据来源」这句话仍然成立，但那张表在契约上没有读取入口 ——
        排障时只能查库。补它需要给 openapi 加 operation（已冻结）。
      </p>
    </Card>
  );
}

/* ────────────────────────── D1：改配额 / 到期 ────────────────────────── */

/**
 * D1。
 *
 * 🔴 **`transfer_enable_bytes` 是「总额」不是「套餐分量」。** 服务端会用
 * `总额 − 当前加油包分量` 反算套餐分量，所以填一个小于用户已购加油包的数会被 422 拒绝
 * （错误文案里带着那个具体数字，照着改一次就对）。**不要**为了「安全」在前端猜一个下限：
 * 前端不知道加油包分量（契约的 `AdminUser` 里没有这个字段）。
 *
 * ⚠️ 一个字段都不填时按钮是灰的。服务端对空 PATCH 回 422，理由是它会写出一条
 * before == after 的 D1 审计 —— 而 D1 的审计是排查「谁把这个人的配额改了」时唯一的线索，
 * 往里灌空记录等于稀释它。前端在这里挡一下，只是省一次注定失败的往返。
 */
function EntitlementCard({ user, onUpdated }: { user: AdminUser; onUpdated: (next: AdminUser) => void }) {
  const fieldId = useId();
  const [expiredAt, setExpiredAt] = useState('');
  const [quotaGib, setQuotaGib] = useState('');
  const [deviceLimit, setDeviceLimit] = useState('');
  const [groupId, setGroupId] = useState('');
  const [uuid, setUuid] = useState('');

  const patch = buildPatch({ expiredAt, quotaGib, deviceLimit, groupId, uuid });
  const changedCount = Object.keys(patch.body).length;

  return (
    <Card>
      <CardTitle hint="updateAdminUser · D1">改配额 / 到期 / 分组 / uuid</CardTitle>

      <div className="grid gap-4 sm:grid-cols-2">
        <FieldShell
          id={`${fieldId}-expired`}
          label="到期时间"
          hint={
            <>
              当前：{user.expired_at ? formatDateTime(user.expired_at) : '不限时'}。
              留空 = 不改。<strong className="font-medium text-fg">改不成「不限时」</strong> ——
              契约表达不出「置空」（字段缺席与 null 在服务端都是「不改」）。
            </>
          }
        >
          <input
            id={`${fieldId}-expired`}
            name="expired_at"
            type="datetime-local"
            value={expiredAt}
            onChange={(event) => setExpiredAt(event.target.value)}
            className="min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg focus-visible:border-accent focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent"
          />
        </FieldShell>

        <TextField
          id={`${fieldId}-quota`}
          label="总配额（GiB）"
          value={quotaGib}
          onChange={setQuotaGib}
          placeholder="例如 200"
          hint={
            <>
              当前：{user.transfer_enable_bytes === undefined ? MISSING : formatBytes(user.transfer_enable_bytes)}。
              这是<strong className="font-medium text-fg">总额</strong>（套餐 + 加油包），
              服务端会自己反算套餐那一份，用户买过的加油包不会被抹掉。
              填得比加油包分量还小时服务端会 422，并告诉你那个数字。
            </>
          }
        />

        <TextField
          id={`${fieldId}-devices`}
          label="设备数上限"
          value={deviceLimit}
          onChange={setDeviceLimit}
          hint={<>当前：{user.device_limit === undefined ? '不限' : user.device_limit}。留空 = 不改。</>}
        />

        <TextField
          id={`${fieldId}-group`}
          label="分组 group_id"
          value={groupId}
          onChange={setGroupId}
          hint={
            <>
              当前：{formatCount(user.group_id)}。改它会改变
              <strong className="font-medium text-fg">这个人能看到哪些节点</strong>，
              节点最长 {NODE_POLL_SECONDS} 秒后才拉到新的用户表。
            </>
          }
        />

        <div className="sm:col-span-2">
          <TextField
            id={`${fieldId}-uuid`}
            label="新的 uuid（换连接凭据）"
            value={uuid}
            onChange={setUuid}
            mono
            placeholder="留空 = 不改"
            hint={
              <>
                🔴 <strong className="font-medium text-fg">看不到当前值</strong>：uuid 是节点侧的连接凭据，
                契约刻意不把它放进 <code className="font-mono">AdminUser</code>。
                换掉之后用户所有客户端里的旧配置会在节点下一次拉表（最长 {NODE_POLL_SECONDS} 秒）后失效，
                他必须重新拉订阅。格式不合法时服务端 422 —— 它不会「当成不改」静默吞掉。
              </>
            }
          />
        </div>
      </div>

      {patch.badField ? (
        <p className="mt-3 rounded-lg border border-warn/40 bg-warn/5 px-3 py-2 text-xs leading-relaxed text-warn">
          「{patch.badField}」填的不是一个合法的值，这一次不会把它发出去。
        </p>
      ) : null}

      <div className="mt-4">
        <DangerousAction
          code="D1"
          submitLabel={`提交这 ${changedCount} 处改动`}
          disabled={changedCount === 0 || patch.badField !== null}
          disabledReason={
            changedCount === 0
              ? '至少要改一个字段。一个字段都不带的 PATCH 会被服务端拒绝（它会写出一条改前值等于改后值的 D1 审计，而那会稀释这张表）。'
              : '先把填错的那一格改对。'
          }
          context={
            <>
              <p>
                目标：<span className="font-medium text-fg">{user.email}</span>
                <span className="ml-2 font-mono text-xs text-fg-subtle">#{user.id}</span>
              </p>
              <ul className="mt-2 space-y-1 text-sm">
                {Object.entries(patch.preview).map(([k, v]) => (
                  <li key={k}>
                    <span className="text-fg-muted">{k}：</span>
                    <span className="font-mono">{v}</span>
                  </li>
                ))}
              </ul>
              <p className="mt-2 text-xs leading-relaxed text-fg-muted">
                改动会 bump 相关节点的 <code className="font-mono">user_rev</code>，
                节点最长 {NODE_POLL_SECONDS} 秒后生效。
              </p>
            </>
          }
          onSubmit={async ({ reason }) => {
            const updated = await unwrap(
              api().PATCH('/api/v1/admin/users/{id}', {
                params: { path: { id: user.id } },
                body: { reason: reason ?? '', ...patch.body },
              }),
            );
            onUpdated(updated);
            setExpiredAt('');
            setQuotaGib('');
            setDeviceLimit('');
            setGroupId('');
            setUuid('');
          }}
        />
      </div>
    </Card>
  );
}

interface PatchDraft {
  /** 除 `reason` 外的可改字段。**用契约类型而不是 `Record`** —— 后者带索引签名，
   *  赋给 `AdminUserPatch` 时每个具名字段都会被判成 `string | number`，直接编译不过。 */
  readonly body: Omit<AdminUserPatch, 'reason'>;
  readonly preview: Record<string, string>;
  /** 填了但解不出合法值的那一格。**不能静默丢掉** —— 那会让人以为改过了。 */
  readonly badField: string | null;
}

/** 表单 → PATCH body。空串 = 不改；解不出来 = 报 badField，不发这一次。 */
function buildPatch(input: {
  expiredAt: string;
  quotaGib: string;
  deviceLimit: string;
  groupId: string;
  uuid: string;
}): PatchDraft {
  const body: Omit<AdminUserPatch, 'reason'> = {};
  const preview: Record<string, string> = {};
  let badField: string | null = null;

  if (input.expiredAt.trim() !== '') {
    // `datetime-local` 给的是**本地时间**，契约要 RFC3339。转换必须显式做：
    // 直接把 "2026-08-30T10:00" 当 ISO 发出去，服务端会按 UTC 解，人就差了一个时区。
    const at = new Date(input.expiredAt);
    if (Number.isNaN(at.getTime())) badField = '到期时间';
    else {
      body.expired_at = at.toISOString();
      preview['到期时间'] = `${formatDateTime(at.toISOString())}（本地时间输入，按 UTC 发出）`;
    }
  }
  if (input.quotaGib.trim() !== '') {
    const gib = Number(input.quotaGib.trim());
    if (!Number.isFinite(gib) || gib < 0) badField = badField ?? '总配额';
    else {
      const bytes = Math.round(gib * GIB);
      body.transfer_enable_bytes = bytes;
      preview['总配额'] = `${formatBytes(bytes)}（${bytes} 字节）`;
    }
  }
  if (input.deviceLimit.trim() !== '') {
    const n = parseInteger(input.deviceLimit);
    if (n === null || n < 0) badField = badField ?? '设备数上限';
    else {
      body.device_limit = n;
      preview['设备数上限'] = String(n);
    }
  }
  if (input.groupId.trim() !== '') {
    const n = parseInteger(input.groupId);
    if (n === null || n < 0) badField = badField ?? '分组 group_id';
    else {
      body.group_id = n;
      preview['分组 group_id'] = String(n);
    }
  }
  if (input.uuid.trim() !== '') {
    body.uuid = input.uuid.trim();
    // 🔴 uuid 不进 preview 的明文展示位？—— 相反：**要展示**。
    // 它是操作者刚刚亲手输入的串，藏起来只会让他无法在提交前核对自己有没有打错。
    preview['新 uuid'] = input.uuid.trim();
  }

  return { body, preview, badField };
}

/* ────────────────────────── D2：封禁 / 解封 ────────────────────────── */

/**
 * D2。
 *
 * 🔴 **生效延迟必须写在确认框里。** 节点每 {NODE_POLL_SECONDS} 秒拉一次用户表，
 * 所以封禁最长 60 秒后才在节点侧生效 —— 60 秒足够完成一次滥用行为。
 * 不说的后果是具体的：管理员 30 秒后刷新看到「他还在线」，于是再点一次封禁
 * （多一条 D2 审计），或者认为功能坏了。解封方向相反但同样要说：
 * 用户被告知「已解封」之后立刻去连、连不上，会再开一张工单。
 */
function BanCard({ user, onUpdated }: { user: AdminUser; onUpdated: (next: AdminUser) => void }) {
  const banned = user.banned;
  return (
    <Card>
      <CardTitle hint="banAdminUser / unbanAdminUser · D2">{banned ? '解封' : '封禁'}</CardTitle>
      <DangerousAction
        code="D2"
        title={banned ? '解封用户' : '封禁用户'}
        submitLabel={banned ? '解封' : '封禁'}
        context={
          <>
            <p>
              目标：<span className="font-medium text-fg">{user.email}</span>
              <span className="ml-2 font-mono text-xs text-fg-subtle">#{user.id}</span>
              {banned ? <Badge tone="danger">当前已封禁</Badge> : null}
            </p>
            <p className="mt-2">
              🔴 <strong className="font-medium text-fg">最长 {NODE_POLL_SECONDS} 秒后才在节点侧生效</strong>
              （配置下发是 {NODE_POLL_SECONDS} 秒轮询）。
              {banned
                ? '解封之后请告诉用户「一分钟内会恢复」，否则他立刻去连、连不上，会再开一张工单。'
                : '这一分钟足够完成一次滥用行为 —— 需要立刻断开的话，得从节点侧处理。'}
            </p>
            {banned ? (
              <p className="mt-2 text-xs text-fg-muted">
                ⚠️ 已注销的账号解封会得到 404：注销是用户自己的意愿，管理员的解封按钮不该推翻它。
              </p>
            ) : (
              <p className="mt-2 text-xs text-fg-muted">
                ⚠️ 对一个已经被封的人再点一次不是错误（服务端不做 CAS），
                它会成功并留下一条 (已封 → 已封) 的审计。
              </p>
            )}
          </>
        }
        onSubmit={async ({ reason }) => {
          // 两个端点分开写而不是把路径塞进一个变量：openapi-fetch 的类型是按**字面量路径**
          // 索引出来的，传一个联合类型的字符串会让 body 与响应一起退化成 never。
          const args = { params: { path: { id: user.id } }, body: { reason: reason ?? '' } };
          const updated = banned
            ? await unwrap(api().POST('/api/v1/admin/users/{id}/unban', args))
            : await unwrap(api().POST('/api/v1/admin/users/{id}/ban', args));
          onUpdated(updated);
        }}
      />
    </Card>
  );
}

/* ────────────────────────── D3：吊销全部订阅 token ────────────────────────── */

/**
 * D3：L1 确认串（用户邮箱）+ L2 原因 + L3 TOTP。
 *
 * ⚠️ 登记表里 D3 没有勾「必填原因」，但服务端**要**（`RevokeAdminUserSubscriptions` 的 L2 分支）。
 * 这里用 `requireReason` 显式补上 —— 少了它，操作者会在填完确认串与 TOTP 之后吃一个 422，
 * 而那时那个 TOTP 码已经报废了（step-up 是先验对再占用，占用不随业务失败回滚）。
 *
 * 🔴 登记表说这一条要求「📧 通知受影响用户」，而 `DangerousAction` 会照着说
 * 「通知由服务端发出」——**当前服务端并没有实现这封信**（handler 里没有任何发信路径）。
 * 所以这里在 `context` 里把真相说出来：撤销之后用户只会发现所有设备同时掉线，
 * 而他不知道为什么。这条差异不能只留在交付说明里，它决定了操作者要不要自己去通知。
 */
function RevokeSubsCard({ user, onDone }: { user: AdminUser; onDone: () => void }) {
  const [result, setResult] = useState<RevokeAllResult | null>(null);

  return (
    <Card>
      <CardTitle hint="revokeAdminUserSubscriptions · D3">吊销全部订阅 token</CardTitle>
      <DangerousAction
        code="D3"
        submitLabel="吊销这个用户的全部订阅 token"
        requireReason
        confirmation={user.email}
        context={
          <>
            <p>
              目标：<span className="font-medium text-fg">{user.email}</span>
              <span className="ml-2 font-mono text-xs text-fg-subtle">#{user.id}</span>
            </p>
            <p className="mt-2">
              他<strong className="font-medium text-fg">所有设备上的订阅链接会立刻失效</strong>，
              必须重新去面板拉一次。这必然产生工单 —— 通常只在确认账号被共享 / 泄漏时才做。
            </p>
            <p className="mt-2 text-xs text-fg-muted">
              🔴 登记表要求「自动通知受影响用户」，但<strong className="font-medium text-fg">服务端目前不发这封信</strong>
              （handler 里没有发信路径）。也就是说：你不通知，他就只会看到所有设备同时断掉。
            </p>
            {user.sub_revoked_at ? (
              <p className="mt-2 text-xs text-fg-muted">
                上一次吊销：{formatDateTime(user.sub_revoked_at)}。再做一次会把时间推到现在，
                并撤掉那之后新签发的 token。
              </p>
            ) : null}
          </>
        }
        onSubmit={async ({ confirmation, reason, totp }) => {
          const out = await unwrap(
            api().POST('/api/v1/admin/users/{id}/revoke-subs', {
              params: {
                path: { id: user.id },
                // L3 走请求头，不是 body。
                header: { 'X-TOTP-Code': totp ?? '' },
              },
              body: { confirmation: confirmation ?? '', reason: reason ?? '' },
            }),
          );
          setResult(out);
        }}
        onDone={onDone}
      />

      {result ? (
        <p className="mt-3 rounded-lg border border-ok/30 bg-ok/10 px-3 py-2 text-sm leading-relaxed text-ok">
          已吊销 <strong className="font-semibold">{result.revoked}</strong> 条 token，
          吊销时刻 {formatDateTime(result.sub_revoked_at)}。
          {result.revoked === 0
            ? '（0 条说明他当时没有任何有效 token —— 操作本身是成功的，吊销时刻已经写上了。）'
            : ''}
        </p>
      ) : null}
    </Card>
  );
}

/* ────────────────────────── D10：调整余额 ────────────────────────── */

type Pot = 'balance' | 'commission';
type Direction = 'credit' | 'debit';

/**
 * D10：L1 确认串（用户邮箱）+ L2 原因 + L3 TOTP + 权限位。
 *
 * # 「调哪一部分钱」必须由操作者显式指明
 *
 * 钱包里有两笔数：**余额**（`balance_amount`）与**佣金**（`commission_pending` /
 * `commission_available`）。这个端点只动前者 —— 契约的 `BalanceAdjustRequest` 只有
 * `{confirmation, reason, amount}`，服务端的分录也只挂在 `liability:user_wallet` 上。
 * 所以这里给一个必选的「调哪一部分」，而佣金那一项是**看得见但选不了**的：
 * 藏起来的话，操作者会以为自己刚给一个推广者补的是佣金，而实际补进了余额。
 *
 * ⚠️ 顺带纠正一处常见误解：**这个产品里没有「可提现」的钱。**
 * 余额仅可消费不可提现（product-brief §6，资金合规底线），佣金也不可提现 ——
 * 它只能划转成余额。所以「调可提现的那部分」这个动作在本系统里不存在。
 *
 * # 503 不是 500
 *
 * 缺 `expense:admin_adjust` 科目时服务端回 **503 + Retry-After**（不是 500）：
 * 那不是偶发故障，是一支没灌进去的 migration，重试一百次也是一样。
 * ⚠️ `DangerousAction` 的 `dangerErrorCopy` 没有 `INTERNAL_DEPENDENCY_DOWN` 这一支，
 * 它会按 `kind = 'server'` 说「服务端出错了」—— 那句话会让人去查后端异常。
 * 那个文案表在别人的文件里（本轮不能改），所以这里在**面板内**把正确的话说出来，
 * 并把按钮置灰，免得一次次重试白白烧掉 TOTP 码（step-up 是先验对再占用）。
 * TODO(P1)：给 `dangerErrorCopy` 加一支 `INTERNAL_DEPENDENCY_DOWN`，然后删掉这里的补丁。
 */
function BalanceCard({ user, onDone }: { user: AdminUser; onDone: () => void }) {
  const fieldId = useId();
  const [pot, setPot] = useState<Pot>('balance');
  const [direction, setDirection] = useState<Direction>('credit');
  const [yuan, setYuan] = useState('');
  const [wallet, setWallet] = useState<Wallet | null>(null);
  const [dependencyDown, setDependencyDown] = useState<ApiError | null>(null);

  const cents = parseYuanToCents(yuan);
  const signed = cents === null ? null : direction === 'credit' ? cents : -cents;

  const blocked =
    dependencyDown !== null
      ? '账本科目缺失，服务端现在拒绝所有调整（见上方说明）。'
      : pot !== 'balance'
        ? '这个入口改不了佣金，只能调余额。'
        : signed === null
          ? '先填一个金额（元，最多两位小数）。'
          : signed === 0
            ? '服务端拒绝 0 元调整：一条金额为 0 的 D10 审计，事后与「有人试图动钱但失败了」不可区分。'
            : null;

  return (
    <Card>
      <CardTitle hint="adjustAdminUserBalance · D10">调整余额</CardTitle>

      <p className="text-sm leading-relaxed text-fg-muted">
        当前余额：
        <strong className="font-medium text-fg">
          {user.balance_amount === undefined ? MISSING : formatCny(user.balance_amount)}
        </strong>
        。余额<strong className="font-medium text-fg">仅可消费，不可提现</strong>；佣金也不可提现，只能划转成余额。
      </p>

      <div className="mt-4 grid gap-4 sm:grid-cols-2">
        <RadioGroup
          name={`${fieldId}-pot`}
          label="调哪一部分钱（必选）"
          value={pot}
          onChange={setPot}
          hint="两笔数是分开记账的。选错的后果是：给推广者补的钱进了余额，而他等的是佣金。"
          options={[
            { value: 'balance', label: '余额（可消费，不可提现）' },
            {
              value: 'commission',
              label: '佣金（可划转成余额，不可提现）',
              note: '这个入口改不了它：契约的请求体里没有这个维度，服务端的分录只挂在钱包科目上。手工发放 / 作废佣金是 D11，在「邀请与返佣」那一页。',
            },
          ]}
        />

        <RadioGroup
          name={`${fieldId}-direction`}
          label="方向"
          value={direction}
          onChange={setDirection}
          hint="服务端收的是一个可正可负的整数（单位：分）。这里拆成两个按钮，是因为一个漏掉的负号等于反向送钱。"
          options={[
            { value: 'credit', label: '给用户加钱（+）' },
            { value: 'debit', label: '从用户扣钱（−）' },
          ]}
        />

        <div className="sm:col-span-2">
          <TextField
            id={`${fieldId}-amount`}
            label="金额（元）"
            value={yuan}
            onChange={setYuan}
            placeholder="例如 12.34"
            hint={
              <>
                最多两位小数；服务端的单位是<strong className="font-medium text-fg">分</strong>，这里替你换算。
                扣款超过用户当前余额时数据库会拒绝（余额不允许为负），服务端把它翻成 422。
              </>
            }
          />
        </div>
      </div>

      {signed !== null && signed !== 0 && pot === 'balance' ? (
        <p
          className={cx(
            'mt-3 rounded-lg border px-3 py-2 text-sm font-medium',
            signed > 0 ? 'border-warn/40 bg-warn/5 text-warn' : 'border-line bg-surface-alt text-fg',
          )}
        >
          本次将给 {user.email} 的余额{signed > 0 ? '增加' : '减少'} {formatCny(Math.abs(signed))}
          <span className="ml-2 font-mono text-xs">（amount = {signed} 分）</span>
        </p>
      ) : null}

      {dependencyDown ? (
        <DependencyDownNotice error={dependencyDown} onClear={() => setDependencyDown(null)} />
      ) : null}

      <div className="mt-4">
        <DangerousAction
          code="D10"
          submitLabel="提交这次余额调整"
          confirmation={user.email}
          permissionName="perm_adjust_balance"
          disabled={blocked !== null}
          disabledReason={blocked ?? undefined}
          context={
            <>
              <p>
                目标：<span className="font-medium text-fg">{user.email}</span>
                <span className="ml-2 font-mono text-xs text-fg-subtle">#{user.id}</span>
              </p>
              <p className="mt-2">
                调整的是<strong className="font-medium text-fg">余额</strong>
                （仅可消费、不可提现）。
                {signed === null ? null : (
                  <>
                    {' '}
                    金额：
                    <strong className="font-medium text-fg">
                      {signed > 0 ? '+' : '−'}
                      {formatCny(Math.abs(signed))}
                    </strong>
                    （{signed} 分）
                  </>
                )}
              </p>
              <p className="mt-2 text-xs leading-relaxed text-fg-muted">
                ⚠️ 登记表要求「单次金额上限」，而<strong className="font-medium text-fg">服务端目前没有这道闸</strong>
                （它只拒绝 0 元与扣成负数）。也就是说多打一个零不会被任何人拦住 ——
                提交前请把上面那行金额预览读一遍。
              </p>
              <p className="mt-2 text-xs leading-relaxed text-fg-muted">
                这一条挂在 <code className="font-mono">perm_adjust_balance</code> 上，默认关闭；
                它在契约的 <code className="font-mono">AdminPermission</code> 枚举里没有对应值，
                <strong className="font-medium text-fg">只能由 DBA 改库开启</strong>，管理员页面授不了。
              </p>
            </>
          }
          onSubmit={async ({ confirmation, reason, totp }) => {
            try {
              const next = await unwrap(
                api().POST('/api/v1/admin/users/{id}/balance-adjust', {
                  params: {
                    path: { id: user.id },
                    header: { 'X-TOTP-Code': totp ?? '' },
                  },
                  body: {
                    confirmation: confirmation ?? '',
                    reason: reason ?? '',
                    // 到这里 signed 必然非空非零（blocked 已经挡住了其余情形），
                    // 但类型上仍要收口 —— 用 0 兜底会把一次「前端算错了」变成一条 422，
                    // 而不是一条 0 元的分录。
                    amount: signed ?? 0,
                  },
                }),
              );
              setWallet(next);
              setYuan('');
            } catch (cause) {
              if (cause instanceof ApiError && cause.code === 'INTERNAL_DEPENDENCY_DOWN') {
                setDependencyDown(cause);
              }
              // 无论如何都往上抛：吞掉它会让组件以为这次成功了，把面板关掉。
              throw cause;
            }
          }}
          onDone={onDone}
        />
      </div>

      {wallet ? (
        <div className="mt-3 rounded-lg border border-ok/30 bg-ok/10 px-3 py-2 text-sm leading-relaxed text-ok">
          <p className="font-medium">调整已入账</p>
          <p className="mt-0.5">
            余额 {formatCny(wallet.balance_amount)} · 佣金（确认中）
            {formatCny(wallet.commission_pending_amount)} · 佣金（可划转）
            {formatCny(wallet.commission_available_amount)}
          </p>
        </div>
      ) : null}
    </Card>
  );
}

/** 503 的说明块。**「暂时不可用」不是「服务器出错」**，两者的处置完全不同。 */
function DependencyDownNotice({ error, onClear }: { error: ApiError; onClear: () => void }) {
  return (
    <div className="mt-3 rounded-lg border border-warn/40 bg-warn/5 px-3 py-2 text-sm leading-relaxed text-warn">
      <p className="font-medium">余额调整暂时不可用（不是服务器故障）</p>
      <p className="mt-1">
        服务端回的是 <code className="font-mono">503 INTERNAL_DEPENDENCY_DOWN</code>：
        账本里缺 <code className="font-mono">expense:admin_adjust</code> 这个科目
        （通常是某个环境漏灌了那支迁移）。
        <strong className="font-semibold"> 这不是偶发故障，现在重试多少次都是一样的结果</strong> ——
        需要运维补齐科目之后才能做。
        {error.retryAfterSeconds === undefined ? null : ` 服务端建议 ${error.retryAfterSeconds} 秒后再试。`}
      </p>
      <p className="mt-1 text-xs opacity-80">
        {error.message}
        {error.requestId ? ` · 请求号 ${error.requestId}` : ''}
      </p>
      <p className="mt-1 text-xs opacity-80">
        ⚠️ 刚才那个 TOTP 码已经作废了（校验通过后的占用不随业务失败回滚）。恢复之后请用新的一个码。
      </p>
      {/* 逃生口：科目补上之后不必刷新整页。**不自动解除** —— 自动重试会把
          「一次注定失败」变成「一串注定失败」，而每一次都要烧掉一个 TOTP 码。 */}
      <div className="mt-2">
        <Button onClick={onClear}>运维说补好了，解除禁用再试一次</Button>
      </div>
    </div>
  );
}

/** 「元」→「分」。空 / 负号 / 超过两位小数 / 非数字一律 `null`。 */
export function parseYuanToCents(raw: string): number | null {
  const t = raw.trim();
  if (t === '') return null;
  if (!/^\d+(\.\d{1,2})?$/.test(t)) return null;
  const n = Number(t);
  if (!Number.isFinite(n)) return null;
  // 先四舍五入再取整：0.29 * 100 在二进制浮点里是 28.999999999999996。
  return Math.round(n * 100);
}

function RadioGroup<T extends string>({
  name,
  label,
  value,
  onChange,
  options,
  hint,
}: {
  name: string;
  label: string;
  value: T;
  onChange: (next: T) => void;
  options: ReadonlyArray<{ readonly value: T; readonly label: string; readonly note?: string }>;
  hint?: ReactNode;
}) {
  return (
    <fieldset className="min-w-0">
      <legend className="mb-1.5 text-sm font-medium text-fg">{label}</legend>
      <div className="space-y-1.5">
        {options.map((o) => (
          <label key={o.value} className="flex cursor-pointer items-start gap-2 text-sm text-fg">
            <input
              type="radio"
              name={name}
              value={o.value}
              checked={value === o.value}
              onChange={() => onChange(o.value)}
              className="mt-1 size-4 accent-accent"
            />
            <span className="min-w-0">
              {o.label}
              {o.note ? <span className="mt-0.5 block text-xs leading-relaxed text-fg-muted">{o.note}</span> : null}
            </span>
          </label>
        ))}
      </div>
      {hint ? <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">{hint}</p> : null}
    </fieldset>
  );
}
