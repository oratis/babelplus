/**
 * `/subscribe/tokens` —— P3。page-inventory §3.1 #19、§3.2.9。安全加固第 2 条。
 *
 * 多 token 的价值在「手机一个、电脑一个，丢了单独撤」。对个位数设备的用户是奢侈品，
 * 所以它是 P3：主路径仍然是 `/subscribe` 上那一条链接 + 「重置订阅」。
 *
 * 🔴 **这一页唯一不能改错的东西：明文只出现一次。**
 * 新建 token 的明文只在 `createSubscriptionToken` 的 201 响应里出现一次
 * （后端 `usersub.go`：DB 里只有 `token_hash` 与 `token_prefix`，明文不进日志、不进别的列），
 * 之后列表里只有 `masked`。所以这一页：
 *  - 明文**只放在组件 state 里**，不写 storage、不进 URL、不重新拉取；
 *  - 用户点掉那块提示之后，它在前端也就**真的没有了**；
 *  - 列表刷新出来的行**永远只显示 `masked`**。
 * 「为了方便回显而把明文存下来」会一次性抹掉这个功能的全部安全价值，
 * 而且不会有任何报错。SubscribeTokensPage.test.tsx 钉死了这一条。
 *
 * 写操作的幂等：api-contract §9.1 的幂等总表里**没有**这三个端点，服务端不认
 * `Idempotency-Key`（只有 `POST /orders` 与 `/orders/{n}/pay` 认）。发一个过去只会让代码
 * 看起来比实际更安全。所以这里的「幂等」就是**单飞 + 禁用按钮**，缺口如实留在 notes 里。
 */
import { useState, type FormEvent } from 'react';
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
import { formatDateTime } from './_imports.ts';
import { unwrap, unwrapEmpty, type ApiError, type components } from '@babelplus/shared/api';
import { api } from '../lib/api.ts';
import { clearCachedUrls } from './SubscribePage.tsx';
import {
  DangerConfirm,
  ListSkeleton,
  QueryError,
  fallbackErrorCopy,
  toApiErrorLike,
  useClipboard,
  useResource,
  type ErrorCopy,
  type ResourceHandle,
} from './subscribe/_shared.tsx';

type SubscriptionToken = components['schemas']['SubscriptionToken'];
type SubscriptionTokenCreated = components['schemas']['SubscriptionTokenCreated'];

/** 与后端 `subTokenNameMaxRunes` 同值（`usersub.go`）。前端先挡一次，省一次往返。 */
const NAME_MAX = 64;

function listTokens(): Promise<SubscriptionToken[]> {
  return unwrap(api().GET('/api/v1/user/subscription/tokens'));
}

function createToken(name: string): Promise<SubscriptionTokenCreated> {
  return unwrap(api().POST('/api/v1/user/subscription/tokens', { body: { name } }));
}

function revokeToken(id: number): Promise<void> {
  return unwrapEmpty(api().DELETE('/api/v1/user/subscription/tokens/{id}', { params: { path: { id } } }));
}

function revokeAllTokens(): Promise<components['schemas']['RevokeAllResult']> {
  return unwrap(api().POST('/api/v1/user/subscription/revoke-all'));
}

export default function SubscribeTokensPage() {
  const tokens = useResource(listTokens);

  /**
   * 刚签发出来的那一条。**只在 state 里**，不落任何存储 —— 见文件头。
   * 它活在列表的三态之外：列表重拉时它必须还在，否则用户刚拿到的明文会被一次刷新冲掉。
   */
  const [justCreated, setJustCreated] = useState<SubscriptionTokenCreated | null>(null);

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">订阅 token</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">
          给不同设备发不同的订阅链接，丢了可以单独撤销。
        </p>
      </header>

      <div className="space-y-4">
        {justCreated ? (
          <PlaintextOnce created={justCreated} onDismiss={() => setJustCreated(null)} />
        ) : null}

        <CreateTokenCard
          onCreated={(created) => {
            setJustCreated(created);
            tokens.reload();
          }}
        />

        <TokenListCard tokens={tokens} />
      </div>
    </>
  );
}

/* ─────────────────────── 明文（只此一次） ─────────────────────── */

/**
 * 新签发的明文。
 *
 * 默认**直接显示明文**而不是打码 —— 这与 `/subscribe` 上那条常驻链接的默认相反，
 * 是有意的：那一条随时能再看一遍，这一条关掉就没有了。让用户先看到、复制走，
 * 比防肩窥重要（而且这一块只在他刚点完「新建」的那几秒里存在）。
 */
function PlaintextOnce({
  created,
  onDismiss,
}: {
  created: SubscriptionTokenCreated;
  onDismiss: () => void;
}) {
  const clipboard = useClipboard();

  return (
    <Card className="border-2 border-accent/50">
      <CardTitle hint="关掉之后无法再取回">「{created.name}」的订阅链接已生成</CardTitle>

      <p className="mb-3 text-sm leading-relaxed text-warn">
        这串链接<strong className="font-medium">只显示这一次</strong>。
        现在就把它导入到那台设备，或者先复制到一个安全的地方 —— 我们这边只存了它的哈希，
        关掉这一块之后连我们也拿不回来，只能撤销后重发一条。
      </p>

      <div className="rounded-lg border border-line bg-surface-alt p-3">
        <code className="block break-all font-mono text-sm text-fg">{created.subscribe_url}</code>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button tone="primary" onClick={() => clipboard.copy(created.subscribe_url)}>
          <Icon.Copy size={14} /> 复制链接
        </Button>
        <Button onClick={onDismiss}>我已保存，关掉</Button>
        <CopyHint state={clipboard.state} />
      </div>
    </Card>
  );
}

/** 复制结果。失败必须**可见**：非安全上下文（http 镜像域名）下 `navigator.clipboard` 根本不存在。 */
function CopyHint({ state }: { state: 'idle' | 'ok' | 'failed' }) {
  if (state === 'idle') return null;
  if (state === 'ok') return <span className="text-sm text-ok">已复制</span>;
  return (
    <span role="alert" className="text-sm text-warn">
      这个浏览器不让我们写剪贴板，请手动选中上面的链接复制。
    </span>
  );
}

/* ─────────────────────────── 新建 ─────────────────────────── */

function CreateTokenCard({ onCreated }: { onCreated: (created: SubscriptionTokenCreated) => void }) {
  const [name, setName] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  async function onSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    // 单飞。**新建刻意没有二次确认**：二次确认是给不可逆操作的，
    // 而这一条随手就能撤销；在一个已经要求先起名字的表单上再加一道「确定吗」，
    // 只会训练用户闭着眼点确认 —— 那会连带削弱下面两个真正危险的确认框。
    if (pending) return;
    const trimmed = name.trim();
    if (trimmed === '') return;
    setPending(true);
    setError(null);
    try {
      const created = await createToken(trimmed);
      setName('');
      onCreated(created);
    } catch (cause) {
      setError(toApiErrorLike(cause, '新建订阅链接失败'));
    } finally {
      setPending(false);
    }
  }

  const copy = error === null ? null : createErrorCopy(error);

  return (
    <Card>
      <CardTitle hint="给它起个能认出设备的名字">新建一条</CardTitle>
      <form className="space-y-3" onSubmit={(event) => void onSubmit(event)}>
        <div>
          <label htmlFor="token-name" className="mb-1.5 block text-sm font-medium text-fg">
            备注名
          </label>
          {/* `text-base`（16px）不是随便挑的：iOS Safari 在 <16px 的输入框获得焦点时会
              自动放大页面，放大后 375px 布局就出现横向滚动，直接违反 M1 移动优先。 */}
          <input
            id="token-name"
            name="name"
            required
            maxLength={NAME_MAX}
            disabled={pending}
            placeholder="例如：iPhone、公司电脑"
            value={name}
            onChange={(event) => setName(event.target.value)}
            className="min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent disabled:opacity-50"
          />
          <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">
            名字只给你自己看。它的用处在丢设备的那一天 —— 到时候你要能一眼认出该撤哪一条。
          </p>
        </div>

        {copy ? (
          <div role="alert" className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-sm text-danger">
            <span className="font-medium">{copy.title}</span>
            <br />
            {copy.description}
          </div>
        ) : null}

        <Button type="submit" tone="primary" disabled={pending || name.trim() === ''}>
          {pending ? '正在签发…' : '新建订阅链接'}
        </Button>
      </form>
    </Card>
  );
}

/**
 * 新建的 `ErrorCode` → 文案。
 *
 * `QUOTA_DEVICE_LIMIT` 在这个端点上表示的是**订阅链接条数**到顶（后端 `usersub.go`
 * 复用了这个码，上限 10 条），不是设备数到顶 —— 直接抄全站「设备数」的文案会把用户
 * 指向订阅页去踢设备，而那里踢多少条都没用。**按 code 分支的同时要看清这个码在这个端点的含义。**
 */
function createErrorCopy(error: ApiError): ErrorCopy {
  switch (error.code) {
    case 'QUOTA_DEVICE_LIMIT':
      return {
        title: '订阅链接的条数到上限了',
        // message 里带着后端配置的具体条数，原样转述；**分支依据是 code，不是 message**。
        description: `${error.message}。先撤销一条不再使用的，再来新建。`,
      };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return { title: '备注名不合法', description: `${error.message}（1–${NAME_MAX} 个字符）` };
    default:
      return fallbackErrorCopy(error);
  }
}

/* ─────────────────────────── 列表 ─────────────────────────── */

function TokenListCard({ tokens }: { tokens: ResourceHandle<SubscriptionToken[]> }) {
  /** 正在确认撤销的那一条。`null` = 没有打开任何确认框。 */
  const [confirming, setConfirming] = useState<SubscriptionToken | null>(null);
  const [revokeAllOpen, setRevokeAllOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [writeError, setWriteError] = useState<ApiError | null>(null);
  const [revokedAllCount, setRevokedAllCount] = useState<number | null>(null);

  async function doRevoke(token: SubscriptionToken): Promise<void> {
    if (pending) return;
    setPending(true);
    setWriteError(null);
    try {
      await revokeToken(token.id);
      // 🔴 订阅 URL 有一份 sessionStorage 缓存（SubscribePage 用它避免每次进页面都重新拉）。
      //    撤销之后不清掉，`/subscribe` 会继续把**一条已经失效的链接**当有效的展示出来 ——
      //    而用户撤销 token 的场景通常正是「这条链接可能已经泄漏」，
      //    此时给他看一条旧链接是最坏的结果。
      clearCachedUrls();
      setConfirming(null);
      tokens.reload();
    } catch (cause) {
      // 失败不关框：关掉了用户就不知道自己那一下有没有生效，然后会再点一次。
      setWriteError(toApiErrorLike(cause, '撤销失败'));
    } finally {
      setPending(false);
    }
  }

  async function doRevokeAll(): Promise<void> {
    if (pending) return;
    setPending(true);
    setWriteError(null);
    try {
      const result = await revokeAllTokens();
      clearCachedUrls();
      setRevokeAllOpen(false);
      setRevokedAllCount(result.revoked);
      tokens.reload();
    } catch (cause) {
      setWriteError(toApiErrorLike(cause, '全部撤销失败'));
    } finally {
      setPending(false);
    }
  }

  if (tokens.state.status === 'loading') {
    return (
      <Card>
        <CardTitle>已签发的链接</CardTitle>
        <ListSkeleton rows={3} />
      </Card>
    );
  }

  if (tokens.state.status === 'error') {
    return (
      <QueryError
        error={tokens.state.error}
        what="订阅 token 列表"
        copy={fallbackErrorCopy(tokens.state.error)}
        onRetry={tokens.reload}
        extra={<LinkButton href="/subscribe">回到订阅页</LinkButton>}
      />
    );
  }

  const list = tokens.state.data;
  const active = list.filter((t) => !t.revoked_at);

  if (list.length === 0) {
    return (
      <EmptyState
        title="还没有单独签发过链接"
        description="大多数人不需要多个。如果你在多台设备上用同一条链接，丢了任何一台都得全部重导 —— 那时候再来分开发也不迟。"
        action={
          <LinkButton tone="primary" href="/subscribe">
            回到订阅页 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
        secondary="上面可以直接新建一条，起个名字就行。"
      />
    );
  }

  return (
    <Card>
      <CardTitle hint={`${active.length} 条有效 · 共 ${list.length} 条`}>已签发的链接</CardTitle>

      {revokedAllCount === null ? null : (
        <p role="status" className="mb-3 rounded-lg border border-ok/30 bg-ok/10 px-3 py-2 text-sm text-ok">
          已撤销 {revokedAllCount} 条订阅链接。每一台设备都要重新导入新链接才能继续更新订阅。
        </p>
      )}

      <ul className="space-y-2">
        {list.map((token) => {
          const revoked = Boolean(token.revoked_at);
          return (
            <li
              key={token.id}
              className={cx('rounded-lg border px-3 py-2.5', revoked ? 'border-line opacity-60' : 'border-line')}
            >
              <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                <span className="min-w-0 flex-1 text-sm font-medium text-fg">{token.name}</span>
                {/* 列表里只有 masked，永远不是明文 —— 见文件头。 */}
                <code className="font-mono text-xs text-fg-muted">{token.masked}</code>
                {revoked ? <Badge>已失效</Badge> : <Badge tone="ok">有效</Badge>}
              </div>
              <p className="mt-1 text-xs text-fg-subtle">
                创建于 {formatDateTime(token.created_at)}
                {' · '}
                {/* 「从未使用」本身就有排障价值：说明那台设备根本没配对成功，
                    而不是「配好了但连不上」—— 这两种的下一步完全不同。 */}
                {token.last_used_at ? `最后使用 ${formatDateTime(token.last_used_at)}` : '从未被使用过'}
                {token.revoked_at ? ` · 失效于 ${formatDateTime(token.revoked_at)}` : ''}
              </p>
              {!revoked ? (
                <Button className="mt-2" tone="danger" onClick={() => setConfirming(token)} disabled={pending}>
                  撤销这一条
                </Button>
              ) : null}
            </li>
          );
        })}
      </ul>

      <DangerConfirm
        open={confirming !== null}
        title={confirming === null ? '' : `撤销「${confirming.name}」`}
        consequences={[
          '这条链接立即失效，用它的那台设备下次更新订阅时会失败（表现为拉不到配置）。',
          '那台设备要重新导入一条新链接才能继续用。',
          '其它设备不受影响。',
          '已经建立的连接不会因此断开 —— 要让设备立刻下线，用订阅页的「全部下线」。',
        ]}
        confirmLabel="确认撤销"
        pending={pending}
        error={writeError === null ? null : fallbackErrorCopy(writeError)}
        onCancel={() => {
          setConfirming(null);
          setWriteError(null);
        }}
        onConfirm={() => {
          if (confirming) void doRevoke(confirming);
        }}
      />

      <div className="mt-4 border-t border-line pt-4">
        <Button tone="danger" onClick={() => setRevokeAllOpen(true)} disabled={pending || active.length === 0}>
          撤销全部（等同订阅页的「重置订阅」）
        </Button>
        <DangerConfirm
          open={revokeAllOpen}
          title="撤销全部订阅链接"
          // 🔴 后果必须说全，而且必须**说准**：`revokeAllSubscriptionTokens` 只写
          // `users.sub_revoked_at`（`usersub.go`），它作废的是**订阅链接**，不是节点上的连接。
          // 写成「所有设备立刻掉线」会让用户以为按下去就断网，然后在发现设备还在跑时
          // 认定「没生效」并反复点 —— 真正让设备下线的是订阅页的「全部下线」。
          consequences={[
            '现在所有的订阅链接立即失效，包括你手上这台设备正在用的那一条。',
            '每一台设备都必须重新导入新链接，否则下次更新订阅就会失败。',
            '已经建立的连接不会因此断开；要让设备立刻下线，用订阅页的「全部下线」（最长 60 秒生效）。',
            '这一步没有撤销键。',
          ]}
          confirmLabel="全部撤销"
          requireAck
          pending={pending}
          error={writeError === null ? null : fallbackErrorCopy(writeError)}
          onCancel={() => {
            setRevokeAllOpen(false);
            setWriteError(null);
          }}
          onConfirm={() => void doRevokeAll()}
        />
      </div>
    </Card>
  );
}
