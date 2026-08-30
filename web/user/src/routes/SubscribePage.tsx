/**
 * `/subscribe` —— P1，**无替代**，本项目最重的一页。page-inventory §3.1 #6、§3.2.3。
 *
 * 竞品把订阅链接埋在 dashboard 的按钮里、把重置埋在 profile 里、完全没有设备列表。
 * 我们把这三件事合成一页，因为它们是同一个心智单元：「谁在用我的订阅」。
 *
 * 四个请求，**四套互相独立的三态**（§2.2 硬规则）：
 * 订阅链接 / 在线设备 / 拉取审计各自持有自己的 loading 与 error，
 * 套餐上限 N 只是订阅那一份的**附属信息**。合并成整页一个 loading 的后果是
 * **任意一个 5xx 都会把订阅链接一起藏掉** —— 而用户来这一页十有八九就是为了复制那条链接。
 *
 * 三条必须做的事（§3.2.3），各自落在哪：
 *  1. **计数口径写在页面上** —— `DeviceQuotaNote`，文案逐字来自 ADR 0015 §6.2。
 *     ⚠️ 设备数是**软限制**：`GetUserAlive` 在节点拿不到 alivelist 时把在线列表置空并返回
 *     nil error（ADR 0015 §6.1 / evidence v2node-401-behavior §3），限制会**静默失败开放**。
 *     所以文案只能是**容量承诺**，不能是执行承诺 —— §6.2 点名禁止的三句话一句都不许写回来。
 *  2. **必须有用户自助「踢下线」** —— `DeviceSection`，且响应里的 `effective_within_seconds`
 *     必须显示出来：配置下发是 60 秒轮询，不说这一句，用户会连点五次然后开工单。
 *  3. **达到上限不显示红色报错** —— 那是升档转化位不是错误提示，所以用 `accent` 不用 `danger`。
 *
 * 🔴 **订阅接口失败时，链接从本地缓存回显**，并标注「可能已过期」（§3.2.3 错态）。
 * 缓存放 `sessionStorage` 而不是 `localStorage`：订阅链接等同于凭据，它的寿命不该超过
 * 承载它的那个会话（token 本身也存在 sessionStorage，见 `lib/session.ts`）。
 * 并且**按用户 id 隔离**：公用电脑上换了账号登录，上一个人的链接不许回显给下一个人。
 *
 * 🔴 **不暴露 `?flag=singbox_profile`。** ADR 0015 裁决 ② 把 sing-box 完整配置定为
 * 「闸门全绿之前只经 `?flag=singbox_profile` 按人显式发放」，且契约的 `SubscriptionUrls`
 * 里根本没有这条 URL。所以这一页只渲染后端真的给了的那几条 —— **自己拼一条出来，
 * 就是把一个尚未验证的配置形态推给了所有人**，而闸门存在的意义正是不让这件事发生。
 */
import { useEffect, useState } from 'react';
import { Link } from 'react-router';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  Icon,
  LinkButton,
  cx,
} from './_imports.ts';
import { formatDateTime, maskSecret, runtimeConfig } from './_imports.ts';
import {
  getSubscription,
  listDevices,
  unwrap,
  type ApiError,
  type UserDevice,
  type UserSubscription,
  type components,
} from '@babelplus/shared/api';
import { api } from '../lib/api.ts';
import { useAuth } from '../lib/auth.tsx';
import {
  DangerConfirm,
  ListSkeleton,
  PendingNotice,
  QueryError,
  fallbackErrorCopy,
  isNotImplemented,
  toApiErrorLike,
  useClipboard,
  useResource,
  type CopyState,
  type ErrorCopy,
  type ResourceHandle,
} from './subscribe/_shared.tsx';

type SubscriptionUrls = components['schemas']['SubscriptionUrls'];
type SubscriptionSummary = components['schemas']['SubscriptionSummary'];
type FetchLogEntry = components['schemas']['SubscriptionFetchLogEntry'];
type KickResult = components['schemas']['KickDevicesResult'];

/** §3.2.3：最近 **10** 次拉取。这一页不做翻页 —— 再往前的记录对自助排查没有增量。 */
const FETCH_LOG_LIMIT = 10;

/* 模块级 loader：函数标识稳定，不会每渲染一次重发一次请求。 */
const loadSubscription = (): Promise<UserSubscription> => getSubscription(api());
const loadDevices = (): Promise<UserDevice[]> => listDevices(api());
const loadFetchLog = (): Promise<FetchLogEntry[]> =>
  unwrap(
    api().GET('/api/v1/user/subscription/fetch-log', {
      params: { query: { limit: FETCH_LOG_LIMIT } },
    }),
  );

export default function SubscribePage() {
  const { user } = useAuth();
  const subscription = useResource(loadSubscription);
  const devices = useResource(loadDevices);
  const fetchLog = useResource(loadFetchLog);

  const userId = user?.id ?? null;
  const summary = subscription.state.status === 'ready' ? subscription.state.data.summary : null;

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">订阅与设备</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">
          复制订阅链接、看谁在用、把不认识的踢掉。
        </p>
      </header>

      <div className="space-y-4">
        <SubscriptionLinkSection sub={subscription} userId={userId} />
        <ClientGuideSection />
        <DeviceSection devices={devices} summary={summary} />
        <SecuritySection
          fetchLog={fetchLog}
          onRevokedAll={() => {
            // 撤销之后缓存里那条链接已经**必然 404** 了，留着它等于在下一次 5xx 时
            // 把一条已知无效的链接当作「上次读到的」再拿给用户导入一遍。
            clearCachedUrls(userId);
            subscription.reload();
            fetchLog.reload();
          }}
        />
      </div>
    </>
  );
}

/* ═══════════════════════════ ① 订阅链接 ═══════════════════════════ */

type FormatKey = 'short' | 'clash' | 'singbox' | 'base64' | 'long';

const FORMAT_META: Record<FormatKey, { label: string; hint: string }> = {
  // 默认对外发 short（契约 `SubscriptionUrls` 的原话）：短、无 query、不易被聊天软件截断。
  short: { label: '通用短链接', hint: '大多数客户端都认它。默认用这一条。' },
  clash: { label: 'Clash YAML', hint: '强制按 Clash 配置下发。Clash Verge Rev 这类客户端用。' },
  singbox: { label: 'sing-box 节点清单', hint: 'sing-box 系客户端（Karing 等）自己生成完整配置。' },
  base64: { label: 'base64 分享链接', hint: '最老的一种格式，别的都不认时再试它。' },
  long: { label: '完整地址', hint: '短链被中间设备拦掉时用这一条，内容与短链完全相同。' },
};

function availableFormats(urls: SubscriptionUrls): FormatKey[] {
  const out: FormatKey[] = [];
  if (urls.short) out.push('short');
  if (urls.clash) out.push('clash');
  if (urls.singbox) out.push('singbox');
  if (urls.base64) out.push('base64');
  if (urls.long) out.push('long');
  return out;
}

function urlFor(urls: SubscriptionUrls, key: FormatKey): string {
  switch (key) {
    case 'clash':
      return urls.clash ?? urls.short;
    case 'singbox':
      return urls.singbox ?? urls.short;
    case 'base64':
      return urls.base64 ?? urls.short;
    case 'long':
      return urls.long || urls.short;
    default:
      return urls.short;
  }
}

function SubscriptionLinkSection({
  sub,
  userId,
}: {
  sub: ResourceHandle<UserSubscription>;
  userId: number | null;
}) {
  // 成功读到就存一份。这份缓存的**唯一用途**是下一次读失败时回显（§3.2.3 错态）。
  useEffect(() => {
    if (sub.state.status === 'ready') writeCachedUrls(userId, sub.state.data.urls);
  }, [sub.state, userId]);

  if (sub.state.status === 'loading') {
    return (
      <Card>
        <CardTitle>订阅链接</CardTitle>
        <ListSkeleton rows={2} />
      </Card>
    );
  }

  if (sub.state.status === 'error') {
    const error = sub.state.error;
    if (isNotImplemented(error)) {
      return (
        <PendingNotice what="订阅链接" requestId={error.requestId}>
          你已经导入过的客户端不受影响，它们用的是本地那份配置。
        </PendingNotice>
      );
    }
    const cached = readCachedUrls(userId);
    if (cached !== null) {
      return <LinkCard urls={cached.urls} staleSince={cached.savedAt} error={error} onRetry={sub.reload} />;
    }
    return (
      <QueryError
        error={error}
        what="订阅链接"
        copy={subscriptionErrorCopy(error)}
        onRetry={sub.reload}
      />
    );
  }

  const urls = sub.state.data.urls;

  // 🔴 空串是**正常响应**，不是错误：后端在「一条有效 token 都没有」时返回空串而不是
  // 现签一条（`usersub.go` 的 `subscriptionURLsFor`：GET 不该签发凭据）。
  // 它最常见的出现时机就是用户刚点完下面的「重置订阅」。
  if (!urls.short) return <NoLinkEmpty />;

  return <LinkCard urls={urls} staleSince={null} error={null} onRetry={sub.reload} />;
}

function NoLinkEmpty() {
  return (
    <EmptyState
      title="现在没有可用的订阅链接"
      description="你的账号下没有有效的订阅链接 —— 如果你刚做过「重置订阅」，这是预期结果。新建一条，然后在每台设备上重新导入。"
      action={
        <LinkButton tone="primary" href="/subscribe/tokens">
          新建一条订阅链接 <Icon.ArrowRight size={14} />
        </LinkButton>
      }
      secondary="新链接的明文只显示一次，建好之后先复制走再关页面。"
    />
  );
}

function LinkCard({
  urls,
  staleSince,
  error,
  onRetry,
}: {
  urls: SubscriptionUrls;
  /** 非 null 表示这是缓存回显，值是缓存写入时刻。 */
  staleSince: string | null;
  error: ApiError | null;
  onRetry: () => void;
}) {
  const formats = availableFormats(urls);
  const [format, setFormat] = useState<FormatKey>('short');
  const [revealed, setRevealed] = useState(false);
  const clipboard = useClipboard();

  const active = formats.includes(format) ? format : 'short';
  const url = urlFor(urls, active);

  return (
    <Card>
      <CardTitle hint={staleSince === null ? '默认打码显示' : '来自本地缓存'}>订阅链接</CardTitle>

      {/* 🔴 §3.2.3 错态：读不到就把上次读到的拿出来，但必须**明说它可能已经过期**。
          不说这一句的话，用户导入一条失效链接后会以为是客户端的问题，然后去重装客户端。 */}
      {staleSince !== null ? (
        <div role="alert" className="mb-3 rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm leading-relaxed text-warn">
          <p className="font-medium">以下是这台设备上次读到的链接，可能已经过期。</p>
          <p className="mt-1">
            读取时间 {formatDateTime(staleSince)}。
            {error === null ? null : ` 这次没能读到最新的：${subscriptionErrorCopy(error).title}。`}
            {' '}你的订阅本身没有变化，已连接的设备不受影响。
          </p>
          <Button className="mt-2" onClick={onRetry}>
            再试一次
          </Button>
        </div>
      ) : null}

      <div className="rounded-lg border border-line bg-surface-alt p-3">
        {/* 默认打码：订阅链接等同于凭据，而用户经常在办公室 / 咖啡馆截图求助。
            打的是 **token** 不是域名 —— 域名恰恰是我们希望他能看清并核对的那一半
            （失联轮换域名时，「我在用哪个镜像」是第一个要问的问题）。 */}
        <code data-testid="subscribe-url" className="block break-all font-mono text-sm text-fg">
          {revealed ? url : maskSubscribeUrl(url)}
        </code>
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {/* 复制的永远是**明文**，与是否展开无关。 */}
          <Button tone="primary" onClick={() => clipboard.copy(url)}>
            <Icon.Copy size={14} /> 复制
          </Button>
          <Button onClick={() => setRevealed((v) => !v)}>{revealed ? '隐藏' : '显示明文'}</Button>
          <CopyHint state={clipboard.state} />
        </div>
        {/* TODO(P1): 二维码。移动端「用另一台设备扫一下」比复制粘贴顺手得多，
            但这里不引二维码库（`web` 至今零运行时第三方依赖，`scripts/check-no-external-assets.mjs`
            还禁止外链资源），自己写一个 QR 编码器不是这一轮该做的事。 */}
      </div>

      {formats.length > 1 ? (
        <div className="mt-3">
          <div className="flex flex-wrap gap-2">
            {formats.map((key) => (
              <Button
                key={key}
                tone={key === active ? 'primary' : 'default'}
                aria-pressed={key === active}
                onClick={() => {
                  setFormat(key);
                  // 换格式就重新打码：上一条已经展开过，不代表这一条也该展开。
                  setRevealed(false);
                }}
              >
                {FORMAT_META[key].label}
              </Button>
            ))}
          </div>
          <p className="mt-2 text-xs leading-relaxed text-fg-muted">{FORMAT_META[active].hint}</p>
        </div>
      ) : null}

      <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
        这条链接等同于你的账号凭据。转发给别人 = 把订阅给了别人，
        下面的「最近拉取记录」能看出来是不是有人在用。
      </p>
    </Card>
  );
}

/** 复制结果。失败必须**可见**：非安全上下文（http 的镜像域名）下 `navigator.clipboard` 根本不存在。 */
function CopyHint({ state }: { state: CopyState }) {
  if (state === 'idle') return null;
  if (state === 'ok') return <span className="text-sm text-ok">已复制</span>;
  return (
    <span role="alert" className="text-sm text-warn">
      这个浏览器不让我们写剪贴板，请点「显示明文」后手动选中复制。
    </span>
  );
}

/**
 * 订阅链接的 `ErrorCode` → 文案。**这一块唯一按 code 分支的地方。**
 *
 * 5xx / 连不上时必须说「你已连接的设备不受影响」（§3.2.3、system-design §1）：
 * 面板是控制面，节点是数据面，把前者的故障说成后者会让用户以为服务没了并去申请退款。
 */
function subscriptionErrorCopy(error: ApiError): ErrorCopy {
  if (error.code === 'NOT_IMPLEMENTED') return fallbackErrorCopy(error);
  if (error.kind === 'server' || error.kind === 'offline') {
    return {
      title: '暂时读不到订阅链接',
      description:
        '订阅本身没有变化，已连接的设备不受影响 —— 这只是面板读不到数据。这台设备上没有可回显的旧链接，所以只能稍后再试。',
    };
  }
  return fallbackErrorCopy(error);
}

/* ═══════════════════════════ ② 客户端引导 ═══════════════════════════ */

type Platform = 'ios' | 'android' | 'windows' | 'macos' | 'other';

/**
 * 按 UA 猜平台。**只用来高亮，绝不用来隐藏别的卡片** ——
 * iPadOS 13 起把自己报成 Macintosh，猜错是常态；猜错时若把正确的那张卡片藏了，
 * 用户就彻底没路可走了。四张卡片永远都在，猜中的那张只是排在前面并高亮。
 */
function guessPlatform(ua: string): Platform {
  if (/iPhone|iPad|iPod/i.test(ua)) return 'ios';
  if (/Android/i.test(ua)) return 'android';
  if (/Windows/i.test(ua)) return 'windows';
  if (/Mac OS X|Macintosh/i.test(ua)) return 'macos';
  return 'other';
}

/**
 * 客户端首推。来自 [ADR 0015](docs/05-adr/0015-client-strategy.md) 裁决 ①：
 * **iOS 首推 Karing**（免费、末次提交 2026-08-12 已核实；Shadowrocket 闭源、
 * 无可达官方文档，连「是否现役」都答不了，且要外区 Apple ID）。
 * Karing 同时覆盖 iOS / Android / macOS / Windows，是工单里可以一句话打发的跨平台兜底。
 *
 * ⚠️ ADR 0015 的状态是**提案，未批准**，但 §1 明写「①③④ 的对外部分今天就能落」。
 * 闸门 B1（中国区 App Store 能不能搜到 Karing）**尚未跑过**，所以这里
 * **不写任何一句「在 App Store 搜得到」**—— 那是我们此刻证明不了的事。
 */
const CLIENTS: ReadonlyArray<{
  readonly platform: Platform;
  readonly os: string;
  readonly app: string;
  readonly note: string;
}> = [
  { platform: 'ios', os: 'iPhone / iPad', app: 'Karing', note: '免费，不需要外区 Apple ID。' },
  { platform: 'android', os: 'Android', app: 'Karing', note: '与 iOS 同一个客户端，配置方式一样。' },
  { platform: 'windows', os: 'Windows', app: 'Clash Verge Rev', note: '开源，导入订阅后开一个开关就行。' },
  { platform: 'macos', os: 'macOS', app: 'Clash Verge Rev', note: 'Karing 也有 macOS 版，两个都行。' },
];

function ClientGuideSection() {
  const cfg = runtimeConfig();
  const guessed = guessPlatform(typeof navigator === 'undefined' ? '' : navigator.userAgent);
  // 猜中的排前面，其余保持原顺序 —— 但一张都不少。
  const ordered = [...CLIENTS].sort(
    (a, b) => Number(b.platform === guessed) - Number(a.platform === guessed),
  );

  return (
    <Card>
      <CardTitle hint={guessed === 'other' ? '四个平台' : '已高亮你这台设备的平台'}>客户端</CardTitle>

      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {ordered.map((client) => (
          <div
            key={client.platform}
            className={cx(
              'rounded-lg border p-3',
              client.platform === guessed ? 'border-accent/40 bg-accent/5' : 'border-line',
            )}
          >
            <div className="flex flex-wrap items-baseline gap-2">
              <span className="text-sm font-medium text-fg">{client.os}</span>
              <Badge tone={client.platform === guessed ? 'info' : 'neutral'}>{client.app}</Badge>
            </div>
            <p className="mt-1 text-xs leading-relaxed text-fg-muted">{client.note}</p>
          </div>
        ))}
      </div>

      {/* 教程指向**站外**独立域名：面板域名被封时教程还在（§3.3）。
          面板里只放链接，不重复放内容 —— 链接是廉价的。 */}
      <div className="mt-3 flex flex-wrap gap-2">
        {cfg.docsUrl ? (
          <LinkButton href={cfg.docsUrl} external>
            看配置教程 <Icon.External size={14} />
          </LinkButton>
        ) : (
          <Button disabled title="docsUrl 未配置">
            看配置教程（教程站未配置）
          </Button>
        )}
      </div>

      {/* TODO(P1): 各客户端「一键导入」深链（`clash://`、`sing-box://import-remote-profile?url=…`）。
          现在**刻意不做**：tutorials-spec §9 / user-journey §12 把 scheme 逐客户端列为**待核实**，
          只有 sing-box 那一条已核实。编一个没注册的 scheme 出来，点了没反应且没有任何报错 ——
          那正是 user-journey 记的 L2 失败模式。要做就要连「3 秒无跳转自动展开手动路径」一起做，
          在 scheme 核实之前，上面的「复制」就是手动路径本身。 */}
      <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
        导入方式都是一样的：复制上面的链接，在客户端里选「从链接导入 / 添加订阅」粘贴进去。
      </p>
    </Card>
  );
}

/* ═══════════════════════════ ③ 在线设备 ═══════════════════════════ */

function DeviceSection({
  devices,
  summary,
}: {
  devices: ResourceHandle<UserDevice[]>;
  /** 来自订阅那一个请求。`null` = 那个请求还没好或失败了 —— **这一块照常显示**。 */
  summary: SubscriptionSummary | null;
}) {
  const [confirming, setConfirming] = useState<UserDevice | null>(null);
  const [kickAllOpen, setKickAllOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [writeError, setWriteError] = useState<ApiError | null>(null);
  const [lastKick, setLastKick] = useState<KickResult | null>(null);

  async function runKick(action: () => Promise<KickResult>, done: () => void): Promise<void> {
    // 单飞。api-contract §9.1 的幂等总表里没有这两个端点，服务端不认 `Idempotency-Key`；
    // 好在 DELETE 本身是幂等的（再踢一次同一个 IP 不会产生第二个后果），
    // 所以这里挡住的是「连点五次 → 五个请求」，不是重复副作用。
    if (pending) return;
    setPending(true);
    setWriteError(null);
    try {
      const result = await action();
      setLastKick(result);
      done();
      devices.reload();
    } catch (cause) {
      // 失败不关确认框：关掉了用户就不知道自己那一下有没有生效，然后会再点一次。
      setWriteError(toApiErrorLike(cause, '下线失败'));
    } finally {
      setPending(false);
    }
  }

  const deviceLimit = summary?.device_limit ?? null;
  // 用**列表长度**而不是 `summary.device_count`：那两个数来自两个请求、两个时刻，
  // 而这一页正是用户拿来核对「我开了几台」的地方 —— 顶上写 3、下面列 2 行，
  // 他会认定面板在骗他。宁可与另一个数字不一致，也不能与**同屏那份列表**不一致。
  const count = devices.state.status === 'ready' ? devices.state.data.length : null;
  // ⚠️ `device_limit = 0` 在契约里表示**不限设备**（`users.device_limit` 为 NULL，
  // 而契约的 int32 装不下「不限」，后端只能填 0 —— 见 `usersub.go` 的 subscriptionSummaryView）。
  // 渲染成「0 台」会让一个不限设备的用户以为自己一台都不能连。
  const unlimited = deviceLimit === 0;
  const atLimit = deviceLimit !== null && deviceLimit > 0 && count !== null && count >= deviceLimit;

  return (
    <Card>
      <CardTitle
        hint={
          count === null
            ? undefined
            : deviceLimit === null
              ? `${count} 台在线`
              : `${count} / ${unlimited ? '不限' : deviceLimit}`
        }
      >
        在线设备
      </CardTitle>

      <DeviceQuotaNote />

      {/* 🔴 达到上限：**升档转化位，不是错误提示**（§3.2.3 第 3 条）。
          所以是 accent（信息）不是 danger（报错），也不带 role="alert"。 */}
      {atLimit ? (
        <div
          data-testid="device-limit-notice"
          className="mb-3 flex flex-wrap items-center gap-2 rounded-lg border border-accent/30 bg-accent/10 p-3 text-sm text-accent"
        >
          <span>已经用满了这个套餐的容量。需要更多可以升级套餐，或者在下面把不用的踢掉。</span>
          <LinkButton href="/plan">看看套餐</LinkButton>
        </div>
      ) : null}

      {lastKick === null ? null : (
        // 契约要求响应**必须携带生效延迟提示**，那就必须显示出来：
        // 配置下发是 60 秒轮询，不说这一句，用户会连点五次然后开工单（user-journey §12.2）。
        <p role="status" className="mb-3 rounded-lg border border-ok/30 bg-ok/10 px-3 py-2 text-sm text-ok">
          已请求下线 {lastKick.removed} 个连接，最长 {lastKick.effective_within_seconds} 秒生效
          —— 节点是按轮询拿配置的，不是立刻断开。
        </p>
      )}

      <DeviceList
        devices={devices}
        onKick={(device) => setConfirming(device)}
        kickDisabled={pending}
      />

      <DangerConfirm
        open={confirming !== null}
        title={confirming === null ? '' : `把 ${confirming.ip} 踢下线`}
        consequences={[
          '这个 IP 上正在进行的连接会被断开，最长 60 秒生效。',
          '它随时可以再连回来 —— 「踢下线」不是封禁，只是让出一个名额。',
          '如果这就是你自己这台设备，你会掉线一次。',
        ]}
        confirmLabel="确认踢下线"
        pending={pending}
        error={writeError === null ? null : fallbackErrorCopy(writeError)}
        onCancel={() => {
          setConfirming(null);
          setWriteError(null);
        }}
        onConfirm={() => {
          const target = confirming;
          if (target) void runKick(() => kickDevice(target.id), () => setConfirming(null));
        }}
      />

      <div className="mt-3">
        <Button
          tone="danger"
          disabled={pending || count === null || count === 0}
          onClick={() => setKickAllOpen(true)}
        >
          全部下线
        </Button>
        <DangerConfirm
          open={kickAllOpen}
          title="把所有设备踢下线"
          consequences={[
            '所有 IP 上正在进行的连接都会被断开，最长 60 秒生效。',
            '客户端通常会自动重连，所以名额可能很快又被自己占回去 —— 先把不用的客户端关掉再点。',
            '订阅链接不受影响，不需要重新导入。要让链接本身失效，用下面的「重置订阅」。',
          ]}
          confirmLabel="确认全部下线"
          pending={pending}
          error={writeError === null ? null : fallbackErrorCopy(writeError)}
          onCancel={() => {
            setKickAllOpen(false);
            setWriteError(null);
          }}
          onConfirm={() => void runKick(kickAllDevices, () => setKickAllOpen(false))}
        />
      </div>
    </Card>
  );
}

/**
 * 🔴 计数口径。**这段文字是产品设计的一部分，不是补充说明** —— 删掉它会直接变成工单。
 *
 * 逐字对齐 ADR 0015 §6.2 的对外文案（套餐页 / 本页 / 教程共用一段），它把设备数表述成
 * **容量承诺**而不是执行承诺，理由是 §6.1：`GetUserAlive` 拿不到 alivelist 时（含 401/403）
 * 把在线列表置成空 map 并返回 nil error，限制会**静默失败开放**。
 *
 * §6.2 点名**不许写**的三句，一句都不许被「顺手补充」回来：
 *   ①「严格限制 / 超出将被封禁 / 系统会自动踢下线」—— 我们证明不了它执行过；
 *   ②「这个限制目前可能不生效」—— 诚实过头就是教人钻空子；
 *   ③ 把设备数写进服务条款、计费口径、退款理由。
 * 「超出容量时的具体行为」这一句也**不许展开成断言**：拒新还是抢占是 user-journey §12.2
 * 的待核实项，读 v2node 的 limiter 源码（约 1 小时）之前，我们不知道答案。
 */
function DeviceQuotaNote() {
  return (
    <div className="mb-3 rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm leading-relaxed text-warn">
      <p className="font-medium">这里数的是出口 IP，不是设备。</p>
      <p className="mt-1">
        我们按套餐上的这个数字为你规划容量。同一个路由器后面的多台设备算作 1 台；
        一台设备在 Wi-Fi 与蜂窝网络之间切换时，可能短暂算作 2 台。
        名额不够用时，先在下面把不用的踢掉（最长 60 秒生效）。
      </p>
    </div>
  );
}

function DeviceList({
  devices,
  onKick,
  kickDisabled,
}: {
  devices: ResourceHandle<UserDevice[]>;
  onKick: (device: UserDevice) => void;
  kickDisabled: boolean;
}) {
  const cfg = runtimeConfig();

  if (devices.state.status === 'loading') return <ListSkeleton rows={2} />;

  if (devices.state.status === 'error') {
    return (
      <QueryError
        error={devices.state.error}
        what="在线设备列表"
        copy={deviceErrorCopy(devices.state.error)}
        onRetry={devices.reload}
      />
    );
  }

  const list = devices.state.data;

  // §3.2.3 的空态 ①：「还没有设备连接过」。**这是新用户最重要的一步** ——
  // 跨不过去，前面所有环节都白做。所以这里给的是教程而不是「暂无数据」。
  if (list.length === 0) {
    return (
      <EmptyState
        title="还没有设备连接过"
        description="订阅链接已经生成好了，但还没有任何客户端用它连上来。照教程配一次，三分钟就能连上。"
        action={
          cfg.docsUrl ? (
            <LinkButton tone="primary" href={cfg.docsUrl} external>
              3 分钟接入 <Icon.External size={14} />
            </LinkButton>
          ) : (
            <Button tone="primary" disabled title="docsUrl 未配置">
              3 分钟接入
            </Button>
          )
        }
        secondary="刚配好也可能要等一会儿：在线名单是节点按 60 秒轮询上报的。"
      />
    );
  }

  return (
    <ul className="space-y-2">
      {list.map((device) => (
        <li
          key={device.id}
          className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-line px-3 py-2.5"
        >
          <div className="min-w-0 flex-1">
            {/* TODO(P1): IP 归属地（§3.2.3 要的是「IP 归属地」而不是裸 IP）。
                契约的 `UserDevice` 只有 `ip`，没有任何地理字段 —— 前端自己查第三方
                geo 接口既是外部依赖（`check-no-external-assets` 禁止），也会把用户 IP
                发给第三方。要做得后端补字段。 */}
            <p className="font-mono text-sm text-fg">{device.ip}</p>
            <p className="mt-0.5 text-xs text-fg-subtle">
              {device.node_name ? `${device.node_name} · ` : ''}
              最后活跃 {formatDateTime(device.last_seen_at)}
              {device.first_seen_at ? ` · 首次 ${formatDateTime(device.first_seen_at)}` : ''}
            </p>
          </div>
          <Button tone="danger" disabled={kickDisabled} onClick={() => onKick(device)}>
            踢下线
          </Button>
        </li>
      ))}
    </ul>
  );
}

function deviceErrorCopy(error: ApiError): ErrorCopy {
  if (error.code === 'NOT_IMPLEMENTED') return fallbackErrorCopy(error);
  if (error.kind === 'server' || error.kind === 'offline') {
    return {
      title: '读不到在线设备',
      description:
        '这只是面板读不到在线名单，你的连接与订阅都不受影响。名单本身也有最多 60 秒的延迟。',
    };
  }
  return fallbackErrorCopy(error);
}

function kickDevice(id: number): Promise<KickResult> {
  return unwrap(api().DELETE('/api/v1/user/devices/{id}', { params: { path: { id } } }));
}

function kickAllDevices(): Promise<KickResult> {
  return unwrap(api().DELETE('/api/v1/user/devices'));
}

/* ═══════════════════════════ ④ 安全 ═══════════════════════════ */

function SecuritySection({
  fetchLog,
  onRevokedAll,
}: {
  fetchLog: ResourceHandle<FetchLogEntry[]>;
  onRevokedAll: () => void;
}) {
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [revoked, setRevoked] = useState<number | null>(null);

  async function doRevokeAll(): Promise<void> {
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      const result = await revokeAllTokens();
      setConfirmOpen(false);
      setRevoked(result.revoked);
      onRevokedAll();
    } catch (cause) {
      setError(toApiErrorLike(cause, '重置订阅失败'));
    } finally {
      setPending(false);
    }
  }

  return (
    <Card>
      <CardTitle hint="订阅被白嫖时你自己就能处理">安全</CardTitle>

      {revoked === null ? null : (
        <p role="status" className="mb-3 rounded-lg border border-ok/30 bg-ok/10 px-3 py-2 text-sm text-ok">
          已撤销 {revoked} 条订阅链接。旧链接现在起一律失效，新建一条后每台设备都要重新导入。
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <Button tone="danger" onClick={() => setConfirmOpen(true)} disabled={pending}>
          重置订阅
        </Button>
        <Link to="/subscribe/tokens" className="text-sm text-accent hover:underline">
          多 token 管理（每台设备一条链接）
        </Link>
      </div>

      <DangerConfirm
        open={confirmOpen}
        title="重置订阅（撤销全部订阅链接）"
        // 🔴 后果必须说全，而且必须**说准**：`revokeAllSubscriptionTokens` 只写
        // `users.sub_revoked_at`（`usersub.go`），它作废的是**订阅链接**，不是节点上的连接。
        // 写成「所有设备立刻掉线」会让用户以为按下去就断网，然后在发现设备还在跑时
        // 认定「没生效」并反复点 —— 真正让设备下线的是上面的「全部下线」。
        consequences={[
          '当前所有订阅链接立即失效，包括你手上这台设备正在用的那一条。',
          '每一台设备都必须导入新链接，否则下次更新订阅时会拉不到配置。',
          '重置之后这一页会没有可用链接，要到「多 token 管理」里新建一条。',
          '已经建立的连接不会因此断开；要让设备立刻下线，用上面的「全部下线」。',
          '这一步没有撤销键。',
        ]}
        confirmLabel="确认重置订阅"
        // 🔴 最高危的那一个：要求勾选「我明白后果」。
        // 后台侧的同一操作是 D3（🔒 输入用户邮箱 + 审计 + 邮件通知，api-contract §6.2）；
        // 用户对自己账号操作可以轻一些 —— 轻的是**验证强度**，不是**信息量**。
        requireAck
        pending={pending}
        error={error === null ? null : fallbackErrorCopy(error)}
        onCancel={() => {
          setConfirmOpen(false);
          setError(null);
        }}
        onConfirm={() => void doRevokeAll()}
      />

      <div className="mt-4 border-t border-line pt-4">
        <h3 className="text-sm font-medium text-fg">最近 {FETCH_LOG_LIMIT} 次订阅拉取</h3>
        <p className="mt-1 mb-3 text-xs leading-relaxed text-fg-muted">
          谁用你的链接拉过配置，全在这里。看到不认识的 IP，就点上面的「重置订阅」——
          这件事你自己就能处理，不用开工单。
        </p>
        <FetchLogList fetchLog={fetchLog} />
      </div>
    </Card>
  );
}

function revokeAllTokens(): Promise<components['schemas']['RevokeAllResult']> {
  return unwrap(api().POST('/api/v1/user/subscription/revoke-all'));
}

function FetchLogList({ fetchLog }: { fetchLog: ResourceHandle<FetchLogEntry[]> }) {
  if (fetchLog.state.status === 'loading') return <ListSkeleton rows={3} />;

  if (fetchLog.state.status === 'error') {
    return (
      <QueryError
        error={fetchLog.state.error}
        what="订阅拉取记录"
        copy={fallbackErrorCopy(fetchLog.state.error)}
        onRetry={fetchLog.reload}
      />
    );
  }

  const list = fetchLog.state.data;

  // §3.2.3 的空态 ②：「还没有拉取记录」。**与「还没有设备连接过」含义不同，要分开显示** ——
  // 前者说的是「客户端根本没配上」，后者说的是「配上了但没连上」，下一步完全不同。
  if (list.length === 0) {
    return (
      <p className="rounded-lg border border-dashed border-line px-3 py-3 text-sm text-fg-muted">
        还没有任何客户端用这条链接拉过配置。如果你已经在客户端里导入过，试着手动「更新订阅」一次
        —— 拉取成功后这里就会出现一条记录。
      </p>
    );
  }

  return (
    <ul className="space-y-1.5">
      {list.map((entry) => (
        <li key={entry.id} className="rounded-lg border border-line px-3 py-2 text-xs">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-mono text-fg">{entry.request_ip}</span>
            <span className="text-fg-subtle">{formatDateTime(entry.request_at)}</span>
            {entry.sub_token_name ? <Badge>{entry.sub_token_name}</Badge> : null}
            {entry.format ? <Badge tone="neutral">{entry.format}</Badge> : null}
          </div>
          {entry.user_agent ? (
            // UA 原样显示、不解析成「iPhone」之类：解析错了会让用户排除掉一台**真的**在用的设备。
            <p className="mt-0.5 truncate font-mono text-fg-subtle" title={entry.user_agent}>
              {entry.user_agent}
            </p>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

/* ═══════════════════════ 链接打码与本地缓存 ═══════════════════════ */

/**
 * 打码。打的是 **token**，不是域名。
 *
 * `maskSecret` 的默认口径（前 8 后 4）套在 URL 上恰好是反的：它会留下 `https://`
 * 和 token 的尾巴，把域名整段吃掉 —— 而域名是我们希望用户看清并核对的那一半
 * （失联轮换域名时，「我现在用的是哪个镜像」是第一个要问的问题）。
 */
export function maskSubscribeUrl(url: string): string {
  if (!url) return '—';
  try {
    const parsed = new URL(url);
    const token = parsed.searchParams.get('token');
    if (token !== null) {
      parsed.searchParams.set('token', maskToken(token));
      // `searchParams.set` 会把 • 编码成 %E2%80%A2，解回来只是为了给人看。
      return decodeURIComponent(parsed.toString());
    }
    const segments = parsed.pathname.split('/');
    const last = segments[segments.length - 1];
    if (last) {
      segments[segments.length - 1] = maskToken(last);
      parsed.pathname = segments.join('/');
      return decodeURIComponent(parsed.toString());
    }
    return url;
  } catch {
    // 不是一个能解析的 URL（后端换了形态、或者这是缓存里的旧数据）：退回全站口径，
    // **不原样显示** —— 打码失败时露出明文是最糟的失败方向。
    return maskSecret(url);
  }
}

function maskToken(token: string): string {
  return token.length <= 6 ? '•'.repeat(token.length) : `${token.slice(0, 6)}${'•'.repeat(12)}`;
}

interface CachedUrls {
  readonly urls: SubscriptionUrls;
  readonly savedAt: string;
}

/**
 * 缓存键。带版本号：`SubscriptionUrls` 的形状变了以后，旧结构必须自然失效
 * 而不是被当成新结构读出来（一条读不动的缓存比没有缓存更糟）。
 */
const CACHE_KEY = 'bp.subscribe.urls.v1';

/**
 * 读缓存。**按用户 id 隔离** —— 公用电脑上换个账号登录，
 * 上一个人的订阅链接不许回显给下一个人（`session.signOut` 只清 token，不清这里）。
 */
export function readCachedUrls(userId: number | null): CachedUrls | null {
  if (userId === null) return null;
  try {
    const raw = window.sessionStorage.getItem(CACHE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as { userId?: unknown; urls?: unknown; savedAt?: unknown };
    if (parsed.userId !== userId) return null;
    const urls = parsed.urls as SubscriptionUrls | undefined;
    if (!urls || typeof urls.short !== 'string' || urls.short === '') return null;
    return { urls, savedAt: typeof parsed.savedAt === 'string' ? parsed.savedAt : '' };
  } catch {
    // 隐私模式下 sessionStorage 可能直接抛。缓存读不到只是少一个兜底，不该炸掉整页。
    return null;
  }
}

export function writeCachedUrls(userId: number | null, urls: SubscriptionUrls): void {
  if (userId === null) return;
  try {
    if (!urls.short) {
      // 空串（没有有效 token）不值得缓存，而且**必须把旧的清掉**：
      // 用户刚重置过订阅，旧链接已经必然 404。
      window.sessionStorage.removeItem(CACHE_KEY);
      return;
    }
    window.sessionStorage.setItem(
      CACHE_KEY,
      JSON.stringify({ userId, urls, savedAt: new Date().toISOString() }),
    );
  } catch {
    /* 存不下就算了 —— 缓存是兜底，不是功能。 */
  }
}

export function clearCachedUrls(userId: number | null): void {
  if (userId === null) return;
  try {
    window.sessionStorage.removeItem(CACHE_KEY);
  } catch {
    /* 同上。 */
  }
}
