/**
 * `/diagnose` —— P2。page-inventory §3.1 #18、§3.2.8；排障树来自 tutorials-spec §4。
 *
 * 它和 `check.*` 的分工必须先说清楚，否则会做重：
 *   `/diagnose`（面板域名，要登录）回答「**我的账号**有没有问题」
 *   `check.*`（公开域名，免登录）回答「**我的网络**有没有走代理、走的是谁」
 * 用户连不上时通常打不开面板 —— 所以 `check.*` 是排障主入口，这一页是它的补充。
 *
 * 🔴 一条容易做错的三态规则：某项检查**本身失败**时显示灰色「检测不可用」，
 * **不能显示成红色失败** —— 检测失败和检测到失败是两回事。
 * 在这一版里它的具体形态是：**契约里的四个 key 有一个没出现在响应里** ——
 * 那说明服务端这一项没算出来，而不是「你这一项不合格」。见 `CheckRow` 的 `missing` 分支。
 *
 * 两块内容互相独立，各自的状态互不影响（page-inventory §2.2 三态纪律）：
 *   ① **账号自检**（`getUserDiagnose`，要网络）
 *   ② **排障决策树**（纯前端常量，不发任何请求）——
 *      这一块**必须在自检失败时照样能用**：面板 API 挂掉恰恰是最需要排障指引的时刻。
 *      所以它不在自检的就绪态里，也不共用一个 loading。
 *
 * 这一页已接线，所以不再读 `?state=` 调试开关（README §7 代价 3）。
 */
import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  Icon,
  LinkButton,
  LoadingState,
  SkeletonCard,
  formatBytes,
  formatDateTime,
  runtimeConfig,
} from './_imports.ts';
import { getDiagnose, type DiagnoseResult } from '@babelplus/shared/api';
import { api } from '../lib/api.ts';
import {
  // 从 ticket-common 借三样通用件（`useApiQuery` 的三态、按 ErrorCode 分支的错误块）。
  // 借而不是再写一份，是为了让「501 显示成『尚未开放』而不是『我们这边出了问题』」
  // 这条规则**只有一个实现处** —— 抄第二份出来，两份迟早会不一致。
  QueryErrorState,
  useApiQuery,
  type TicketCategory,
} from './ticket-common.tsx';

type DiagnoseCheck = DiagnoseResult['checks'][number];
type CheckKey = DiagnoseCheck['key'];

export default function DiagnosePage() {
  const cfg = runtimeConfig();
  // 「还没跑过检查」是这一页刻意保留的初始态（§3.2.8 三态）：
  // 自检虽然全程只读，但**用户点一下再跑**能让「这一页不会动我的账号」这句话可信。
  const [started, setStarted] = useState(false);

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">自助诊断</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">
          先看看是不是账号这边的问题。前四项在客户端里一个都看不到 —— 这一页的价值就在这里。
        </p>
      </header>

      <div className="space-y-4">
        {/* 先把 check.* 推出去：用户连不上时打不开这一页，那才是主入口。 */}
        <Card>
          <p className="text-sm leading-relaxed text-fg-muted">
            如果你现在<strong className="font-medium text-fg">完全连不上</strong>，
            要看的是免登录的网络诊断页，而不是这里 —— 这一页只能告诉你账号有没有问题。
          </p>
          <div className="mt-3">
            {cfg.checkUrl ? (
              <LinkButton href={cfg.checkUrl} external>
                打开网络诊断 <Icon.External size={14} />
              </LinkButton>
            ) : (
              <Button disabled title="checkUrl 未配置">
                打开网络诊断（未配置）
              </Button>
            )}
          </div>
        </Card>

        {started ? (
          <AccountChecks />
        ) : (
          <EmptyState
            title="还没跑过检查"
            description="点一下，我们会逐项检查你的账号状态。全程只读，不会改动任何东西。"
            action={
              <Button tone="primary" onClick={() => setStarted(true)}>
                开始检查
              </Button>
            }
          />
        )}

        <TroubleshootingTree />
      </div>
    </>
  );
}

/* ─────────────────────────── 账号自检 ─────────────────────────── */

/**
 * 四项自检。
 *
 * ⚠️ **没有做到「逐项流式出结果」**（§3.2.8 的加载态原话）：契约只有
 * `GET /api/v1/user/diagnose` 一个端点，四项在**一次响应里一起回来**，
 * 前端没有可以逐项渲染的中间态。假装分四批出现（定时器错开）只是动画，
 * 不会让任何一项更早可用，还会让慢网络下的用户以为前面几项已经算完了。
 * 要真做，需要契约侧拆成四个端点或一条 SSE —— 本轮不改 `openapi/`。
 */
function AccountChecks() {
  const query = useApiQuery(() => getDiagnose(api()), [], '自检失败');

  if (query.state === 'loading') {
    return (
      <LoadingState>
        <SkeletonCard lines={5} />
      </LoadingState>
    );
  }

  if (query.state === 'error' && query.error) {
    // 501 由 QueryErrorState 内部分流到「该功能尚未开放」，不会显示成「我们这边出了问题」。
    return <QueryErrorState error={query.error} what="账号自检" onRetry={query.reload} />;
  }

  const result = query.data;
  if (!result) return null;

  const checks = result.checks;
  // 空数组 = 服务端一项都没算出来。**这仍然不是红色失败** —— 同一条规则：
  // 「检测失败」和「检测到失败」是两回事。
  if (checks.length === 0) {
    return (
      <EmptyState
        title="这次没有拿到任何检查结果"
        description="服务端没有返回任何一项检查。这说明检测本身没跑起来，不代表你的账号有问题。"
        action={
          <Button tone="primary" onClick={query.reload}>
            重新检查
          </Button>
        }
      />
    );
  }

  const failed = CHECK_ORDER.filter((key) => findCheck(checks, key)?.ok === false);

  return (
    <Card>
      <CardTitle
        hint={
          <Badge tone={failed.length === 0 ? 'ok' : 'warn'}>
            {failed.length === 0 ? '四项都正常' : `${failed.length} 项有问题`}
          </Badge>
        }
      >
        账号自检
      </CardTitle>

      <ul className="divide-y divide-line">
        {CHECK_ORDER.map((key) => (
          <CheckRow key={key} spec={CHECK_SPECS[key]} check={findCheck(checks, key)} />
        ))}
      </ul>

      {/* 🔴 契约对 `data_delay_note` 的原话是「**不是装饰**」：流量数字有三个天然不一致的
          口径（面板 / 客户端 subscription-userinfo / 邮件快照）。原样显示服务端那句话，
          **不改写、不缩写** —— 改写等于让前端和服务端各说一套。 */}
      <p className="mt-3 rounded-lg bg-surface-alt/60 px-3 py-2 text-xs leading-relaxed text-fg-muted">
        {result.data_delay_note}
      </p>

      <dl className="mt-3 grid gap-x-6 gap-y-2 text-xs sm:grid-cols-2">
        <div className="flex justify-between gap-3 sm:block">
          <dt className="text-fg-muted">最近一次订阅拉取</dt>
          <dd className="text-fg sm:mt-0.5">
            {result.subscription_last_fetched_at ? (
              formatDateTime(result.subscription_last_fetched_at)
            ) : (
              // 「从来没拉过」是一条**很强的**线索：客户端根本没连上订阅域名，
              // 问题在导入那一步，不在节点。所以不能显示成「—」。
              <span className="text-warn">没有记录 —— 客户端可能从没成功拉到过订阅</span>
            )}
          </dd>
        </div>
        <div className="flex justify-between gap-3 sm:block">
          <dt className="text-fg-muted">最近一次流量上报</dt>
          <dd className="text-fg sm:mt-0.5">
            {result.traffic_last_reported_at ? formatDateTime(result.traffic_last_reported_at) : '没有记录'}
          </dd>
        </div>
      </dl>

      <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line pt-3">
        <Button onClick={query.reload}>重新检查</Button>
        <DiagnoseCodeAction result={result} />
      </div>

      {/* TODO(P2)：§3.2.8 的检查项表里还有三项**契约里没有**：
          「订阅可拉取」（服务端自测拉一次自己的订阅，回 HTTP 状态与字节数）、
          「最近拉取记录的 IP」（陌生 IP → 引导重置 token）、「节点健康」（已知问题提示）。
          `DiagnoseCheck.key` 的枚举只有上面四个，编三行假的「待检测」出来
          比不显示更糟 —— 用户会一直等一个永远不会有结果的检查。 */}
      <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
        订阅可拉取自测、拉取来源 IP、节点健康这三项还没上线；在那之前，
        「是不是节点的问题」用上面的网络诊断页判断更准。
      </p>
    </Card>
  );
}

/** 固定的展示顺序。按响应里的顺序渲染会让页面在两次检查之间跳动。 */
const CHECK_ORDER: readonly CheckKey[] = ['account_active', 'not_expired', 'traffic_left', 'device_under_limit'];

function findCheck(checks: readonly DiagnoseCheck[], key: CheckKey): DiagnoseCheck | undefined {
  return checks.find((check) => check.key === key);
}

interface CheckSpec {
  readonly label: string;
  /** 判据。写出来是为了让用户能拿它和客户端里看到的数字对质。 */
  readonly basis: string;
  /** 通过时的一句人话。 */
  readonly okText: string;
  /** 不通过时的一句人话 + 一个动作。 */
  readonly failText: string;
  readonly action: { readonly label: string; readonly href: string } | null;
  /** 不通过时把 `detail` 里的哪些事实摊出来。 */
  readonly facts: (detail: Record<string, unknown>) => string | null;
}

const CHECK_SPECS: Record<CheckKey, CheckSpec> = {
  account_active: {
    label: '账号状态',
    basis: 'banned = false',
    okText: '账号可用。',
    // 封禁不是「你哪里做错了」的口吻，也不引导重新登录 ——
    // 重登换不回来一个没被封的身份（middleware/user.go 点名了这条来回）。
    failText: '这个账号已被封禁，订阅链接会一直拉不到内容。重新登录不会有帮助。',
    action: { label: '提工单申诉', href: ticketHref('DIAG-ACCOUNT-BANNED', 'account') },
    facts: (detail) => {
      const reason = readString(detail, 'banned_reason');
      const at = readString(detail, 'banned_at');
      const parts: string[] = [];
      if (reason) parts.push(`原因：${reason}`);
      if (at) parts.push(`时间：${formatDateTime(at)}`);
      return parts.length > 0 ? parts.join('　') : null;
    },
  },
  not_expired: {
    label: '订阅有效期',
    basis: 'expired_at 未过',
    okText: '订阅在有效期内。',
    failText: '订阅已经到期，节点会拒绝连接。',
    action: { label: '去续费', href: '/plan' },
    facts: (detail) => {
      // `unlimited` 是**不限时套餐**，不是「读不到到期时间」——
      // 两者显示成同一句话会让不限时用户以为自己的套餐有问题。
      if (readBoolean(detail, 'unlimited')) return '不限时套餐，不会到期。';
      const expiredAt = readString(detail, 'expired_at');
      return expiredAt ? `到期时间：${formatDateTime(expiredAt)}` : null;
    },
  },
  traffic_left: {
    label: '流量余额',
    basis: 'u + d < transfer_enable',
    okText: '还有可用流量。',
    failText: '流量已经用完，节点会拒绝连接。',
    action: { label: '买流量包', href: '/plan' },
    facts: (detail) => {
      const used = readNumber(detail, 'used_bytes');
      const total = readNumber(detail, 'total_bytes');
      if (used === null && total === null) return null;
      // 三个原始数字一并给出：用户拿客户端里的数字来质问时，
      // 我们要能指着同一行说「面板用的是这两个数」。
      return `已用 ${formatBytes(used)} / 总 ${formatBytes(total)}`;
    },
  },
  device_under_limit: {
    label: '设备数',
    basis: '在线数 ≤ device_limit',
    okText: '设备数在限额内。',
    // ⚠️ 设备数是**软限制**且系统性偏小（alivelist 拉取失败时 v2node 静默降级为
    // 「零在线设备」）。所以这一项**只展示不拦截**，措辞不能说成「已被限制」。
    failText: '在线设备数超过了套餐限额。这一项不会直接断你的连接，但可能让部分设备连不上。',
    action: { label: '管理设备', href: '/subscribe' },
    facts: (detail) => {
      if (readBoolean(detail, 'unlimited')) return '这个套餐不限设备数。';
      const count = readNumber(detail, 'device_count');
      const limit = readNumber(detail, 'device_limit');
      if (count === null) return null;
      const head = limit === null ? `在线 ${count}` : `在线 ${count} / 限额 ${limit}`;
      // 口径必须跟着数字走，否则用户会拿它跟手上的设备台数对质。
      return `${head}（按 IP 统计：同一台设备切换 Wi-Fi / 蜂窝会算两台）`;
    },
  },
};

/**
 * 一行检查。三种呈现，**第三种是这一页最容易被顺手改错的地方**：
 *
 *  - `ok = true`  → 绿色「正常」
 *  - `ok = false` → 红色「有问题」+ 人话 + 动作
 *  - **这一项没出现在响应里** → **灰色「检测不可用」**，且**不给动作按钮**
 *
 * 第三种绝不能并进第二种（写成 `check?.ok !== true` 就会并进去）。
 * 「我们没能检查」和「我们检查出你不合格」对用户是两句完全不同的话：
 * 前者不该让他去续费、去踢设备、去提工单 —— 他的账号可能根本没问题。
 */
function CheckRow({ spec, check }: { spec: CheckSpec; check: DiagnoseCheck | undefined }) {
  const missing = check === undefined;
  const ok = check?.ok === true;
  const detail = (check?.detail ?? {}) as Record<string, unknown>;
  const facts = missing ? null : spec.facts(detail);

  return (
    <li className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2.5">
      <span>
        {missing ? (
          <Badge tone="neutral">检测不可用</Badge>
        ) : ok ? (
          <Badge tone="ok">正常</Badge>
        ) : (
          <Badge tone="danger">有问题</Badge>
        )}
      </span>
      <span className="text-sm font-medium text-fg">{spec.label}</span>
      <span className="w-full text-xs leading-relaxed text-fg-muted sm:w-auto sm:flex-1">
        {missing ? '这一项这次没有检查出结果，不代表它有问题。' : ok ? spec.okText : spec.failText}
        {facts ? <span className="ml-1 text-fg-subtle">{facts}</span> : null}
      </span>
      <span className="ml-auto text-xs text-fg-subtle" title={spec.basis}>
        {/* 动作按钮只在**确实不通过**时出现。检测不可用时给按钮 =
            让用户为一个不存在的问题去付款 / 去踢设备。 */}
        {!missing && !ok && spec.action ? (
          <LinkButton href={spec.action.href} className="min-h-9 px-3 text-xs">
            {spec.action.label} <Icon.ArrowRight size={12} />
          </LinkButton>
        ) : (
          <span className="font-mono">{spec.basis}</span>
        )}
      </span>
    </li>
  );
}

/* ─────────────────────────── 诊断码 ─────────────────────────── */

/**
 * 诊断码：把四项结果压成一个短串，建单时带进正文（§3.2.8 表格最后一行）。
 *
 * 形状 `DG1-A1E0T1Dx`：字母是检查项，`1` 通过、`0` 不通过、`x` 这次没结果。
 * 为什么不塞 JSON 或 base64：这个串会被用户抄进工单、被客服念出来、
 * 被贴进搜索框 —— 它必须**肉眼可读且能手抄**。
 * 服务端本来就会在建单时重新采集完整快照，这个串只是给人对齐用的索引。
 *
 * ⚠️ 长度必须留在 64 字符以内：列表页的 `sanitizeOriginValue` 会截断超出的部分
 * （`ORIGIN_VALUE_MAX`），截断后的码对不上任何东西。当前是 12 个字符。
 */
const CHECK_CODE_LETTER: Record<CheckKey, string> = {
  account_active: 'A',
  not_expired: 'E',
  traffic_left: 'T',
  device_under_limit: 'D',
};

export function diagnoseCode(result: DiagnoseResult): string {
  const body = CHECK_ORDER.map((key) => {
    const check = findCheck(result.checks, key);
    const flag = check === undefined ? 'x' : check.ok ? '1' : '0';
    return `${CHECK_CODE_LETTER[key]}${flag}`;
  }).join('');
  return `DG1-${body}`;
}

function DiagnoseCodeAction({ result }: { result: DiagnoseResult }) {
  const code = diagnoseCode(result);
  // 有哪一项不通过时，建单分类跟着那一项走 —— 客服少问一句「你是哪类问题」。
  const failedKey = CHECK_ORDER.find((key) => findCheck(result.checks, key)?.ok === false);
  const category: TicketCategory = failedKey === undefined ? 'subscription' : CHECK_TICKET_CATEGORY[failedKey];

  return (
    <>
      <LinkButton href={ticketHref(code, category)}>
        带着诊断码提工单 <Icon.ArrowRight size={14} />
      </LinkButton>
      <span className="font-mono text-xs text-fg-subtle" data-testid="diagnose-code">
        {code}
      </span>
    </>
  );
}

const CHECK_TICKET_CATEGORY: Record<CheckKey, TicketCategory> = {
  account_active: 'account',
  not_expired: 'billing',
  traffic_left: 'billing',
  device_under_limit: 'subscription',
};

/**
 * 建单链接。`from=diagnose` + `code=…`（+ 分类）——
 * 列表页的 `readTicketOrigin` 会校验分类、清洗 `from` / `code`，
 * 并把来源写进正文开头，让客服第一屏就知道用户是从哪一步走过来的。
 */
function ticketHref(code: string, category: TicketCategory): string {
  return `/ticket?${new URLSearchParams({ from: 'diagnose', code, category }).toString()}`;
}

/* ─────────────────────────── 排障决策树 ─────────────────────────── */

interface TreeQuestion {
  readonly kind: 'question';
  readonly prompt: string;
  readonly options: ReadonlyArray<{ readonly label: string; readonly next: string }>;
}

interface TreeLeaf {
  readonly kind: 'leaf';
  readonly title: string;
  /** 一段人话。**内容取自 tutorials-spec §4.2 / §4.3 的篇目要点**，不是随手写的。 */
  readonly body: string;
  /** 文档站里的哪一片。**不拼具体文章 slug** —— tutorials-spec 没定 slug，编一个就是导到 404。 */
  readonly docsSection: string;
  readonly category: TicketCategory;
  /** 带进工单正文的来源码。 */
  readonly code: string;
}

type TreeNode = TreeQuestion | TreeLeaf;

const TREE_ROOT = 'q-list';

/**
 * 决策树，逐条对应 tutorials-spec §4.1 的那张 mermaid 图。
 *
 * 🔴 一处**刻意与 tutorials-spec 不一致**的地方：§4.2 的 DNS/分流表里有一条
 * 「国内网站变慢/打不开 → 分流规则没生效，`GEOIP,CN` 的位置问题」，
 * 这条**对不上现在的实现**，所以没有照抄进来：
 * B46 实测（mihomo v1.19.30，全新配置目录 + 断网）规则表里带 `GEOIP,CN` 时
 * **整份配置被拒绝加载**（要去下 8.6 MB 的 MMDB，而需要它的人正是「人在大陆、
 * 刚装客户端、还没有可用代理」的那一刻）。据此 `GEOIP,CN,DIRECT` 已从下发规则里去掉，
 * 代价写在 roadmap B46 里：**国内流量现在也走节点**。
 * 也就是说「国内站点变慢」现在是**设计后果而不是配置故障**，
 * 让用户去查一条我们根本没下发的规则，只会白花他半小时。
 */
const TREE: Record<string, TreeNode> = {
  'q-list': {
    kind: 'question',
    prompt: '客户端能拉到节点列表吗？',
    options: [
      { label: '拉不到 / 列表是空的', next: 'leaf-sub' },
      { label: '能拉到', next: 'q-latency' },
    ],
  },
  'q-latency': {
    kind: 'question',
    prompt: '节点的延迟显示正常吗？',
    options: [
      { label: '全部超时', next: 'leaf-conn-all' },
      { label: '部分超时', next: 'leaf-conn-part' },
      { label: '延迟正常', next: 'q-web' },
    ],
  },
  'q-web': {
    kind: 'question',
    prompt: '连上之后能打开网页吗？',
    options: [
      { label: '完全打不开', next: 'leaf-dns' },
      { label: '能打开，但很慢', next: 'leaf-speed' },
      { label: '部分网站打不开', next: 'leaf-rules' },
    ],
  },
  'leaf-sub': {
    kind: 'leaf',
    title: '订阅类问题',
    body:
      '三件事按顺序查：订阅域名现在通不通（面板能打开不代表订阅域名能）；' +
      '订阅 token 是不是被重置过（重置后所有设备都要重新导入）；' +
      '以及客户端是不是让「拉订阅」这个请求自己走了代理 —— ' +
      '没连上的时候走代理去拉订阅是个鸡生蛋问题，规则里要放行订阅域名直连。',
    docsSection: '排障 · 订阅类（3 篇）',
    category: 'subscription',
    code: 'DIAG-TREE-SUB',
  },
  'leaf-conn-all': {
    kind: 'leaf',
    title: '全部节点超时',
    body:
      '这是最高频的一类，成因分三种：本地网络（换个网络试一次最快，手机热点就行）、' +
      '客户端配置（版本过旧、TUN 没开、系统代理没生效）、以及节点侧。' +
      '公司或校园网下常常是出口端口受限，切到 443 / 8443 这类常用端口通常就好。',
    docsSection: '排障 · 连接类（4 篇）',
    category: 'node-down',
    code: 'DIAG-TREE-CONN-ALL',
  },
  'leaf-conn-part': {
    kind: 'leaf',
    title: '部分节点超时',
    body:
      '先换一个节点确认是不是只有那一个。单个节点超时通常是节点维护、线路被封，' +
      '或者你的本地 ISP 到那条线路的绕行 —— 同一个节点在不同运营商下表现不一样是常态。',
    docsSection: '排障 · 连接类（4 篇）',
    category: 'node-down',
    code: 'DIAG-TREE-CONN-PART',
  },
  'leaf-dns': {
    kind: 'leaf',
    title: 'DNS 与分流',
    body:
      '延迟正常却一个网页都打不开，多半卡在 DNS 上。' +
      '先用检测站点看有没有 DNS 泄漏，再确认客户端的 fake-ip 配置是不是被改过。',
    docsSection: '排障 · DNS 与分流类',
    category: 'node-down',
    code: 'DIAG-TREE-DNS',
  },
  'leaf-speed': {
    kind: 'leaf',
    title: '速度慢',
    body:
      '先问一句：你是用什么测的？浏览器下载单个文件是**单流**，speedtest 这类工具是**多线程聚合**，' +
      '两者差好几倍是物理规律不是服务问题 —— 单流吞吐受跨境 TCP 拥塞控制限制，' +
      '8 并发能聚合到 8 倍。我们实测把单流从 370 KB/s 提到约 1700 KB/s（4.6 倍）的办法是' +
      '切到 Hysteria2 那条路径。晚高峰整体变慢则是跨境链路拥塞，换节点或换协议是唯一的手段。',
    docsSection: '排障 · 速度类（2 篇）',
    category: 'node-down',
    code: 'DIAG-TREE-SPEED',
  },
  'leaf-rules': {
    kind: 'leaf',
    title: '部分网站打不开',
    body:
      '按规则的命中顺序排查：规则表是从上往下匹配的，第一条命中就停，' +
      '所以一条范围过大的规则会把后面的全遮住。' +
      '另外有一条现在就能说清楚的事实：我们下发的规则表里**没有国内直连分流**，' +
      '所以访问国内站点也会走节点，慢是预期之内的，不是你的配置出了问题。',
    docsSection: '排障 · DNS 与分流类',
    category: 'node-down',
    code: 'DIAG-TREE-RULES',
  },
};

/**
 * 排障决策树。**纯前端，不发任何请求** —— 它必须在自检挂掉、甚至 API 全挂的时候照样能用。
 * 页面里唯一的外部依赖是 `docsUrl`（站外独立域名，面板被封时它还在，§3.3）。
 */
function TroubleshootingTree() {
  const cfg = runtimeConfig();
  // 走过的路径。存整条而不只存当前节点，是为了「上一步」能退回去 ——
  // 排障树最常见的操作就是「上一个问题我好像选错了」。
  const [path, setPath] = useState<readonly string[]>([TREE_ROOT]);
  const currentId = path[path.length - 1] ?? TREE_ROOT;
  const node = TREE[currentId] ?? TREE[TREE_ROOT];
  if (!node) return null;

  return (
    <Card>
      <CardTitle hint="tutorials-spec §4">连不上？两三个问题定位</CardTitle>

      {node.kind === 'question' ? (
        <>
          <p className="text-sm font-medium text-fg">{node.prompt}</p>
          <div className="mt-3 flex flex-wrap gap-2">
            {node.options.map((option) => (
              <Button key={option.next} onClick={() => setPath((prev) => [...prev, option.next])}>
                {option.label}
              </Button>
            ))}
          </div>
        </>
      ) : (
        <>
          <p className="text-sm font-semibold text-fg">{node.title}</p>
          <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">{node.body}</p>
          <p className="mt-2 text-xs text-fg-subtle">
            文档站里的位置：<span className="text-fg-muted">{node.docsSection}</span>
          </p>
          <div className="mt-3 flex flex-wrap gap-2">
            {cfg.docsUrl ? (
              <LinkButton tone="primary" href={cfg.docsUrl} external>
                打开排障文档 <Icon.External size={14} />
              </LinkButton>
            ) : (
              <Button tone="primary" disabled title="docsUrl 未配置">
                打开排障文档（未配置）
              </Button>
            )}
            {/* 叶子节点必须通向建单，并把来源带进正文（?from=diagnose&code=…）——
                否则这棵树就只是一篇没人点的说明。 */}
            <LinkButton href={ticketHref(node.code, node.category)}>
              还是不行，提工单 <Icon.ArrowRight size={14} />
            </LinkButton>
          </div>
        </>
      )}

      {/* §4.4：开了 TUN / fake-ip 之后 ping、nslookup、curl 的结果全部不可信
          （会被客户端劫持）。不写这一句，用户会拿一个假的 ping 结果来提工单，
          而客服要花半小时才能问出他开着 TUN。 */}
      <p className="mt-4 border-t border-line pt-3 text-xs leading-relaxed text-fg-subtle">
        自测提示：开着 TUN / fake-ip 时，<code className="font-mono">ping</code>、
        <code className="font-mono">nslookup</code>、<code className="font-mono">curl</code>{' '}
        的结果全都不可信（请求会被客户端劫持）。用客户端内置的延迟测试，或上面那个网络诊断页。
      </p>

      {path.length > 1 ? (
        <div className="mt-3 flex flex-wrap gap-2">
          <Button tone="ghost" onClick={() => setPath((prev) => prev.slice(0, -1))}>
            上一步
          </Button>
          <Button tone="ghost" onClick={() => setPath([TREE_ROOT])}>
            重新开始
          </Button>
        </div>
      ) : null}
    </Card>
  );
}

/* ─────────────────────── detail 里的字段读取 ─────────────────────── */

/*
 * `DiagnoseCheck.detail` 在契约里是 `{ [key: string]: unknown }` —— 没有形状保证。
 * 所以每个字段都单独判类型，读不到就返回 null 让调用方省掉那一句，
 * **绝不 `as number` 硬转**：一个 `undefined` 被当成数字，页面上就会出现 "NaN GB"。
 */

function readNumber(detail: Record<string, unknown>, key: string): number | null {
  const value = detail[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

function readString(detail: Record<string, unknown>, key: string): string | null {
  const value = detail[key];
  return typeof value === 'string' && value.trim() !== '' ? value : null;
}

function readBoolean(detail: Record<string, unknown>, key: string): boolean {
  return detail[key] === true;
}
