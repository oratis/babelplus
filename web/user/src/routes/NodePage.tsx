/**
 * `/node` —— P2，**从竞品的 P1 降级**。page-inventory §3.1 #16、§3.2.9。
 *
 * 降级理由：竞品 58 个节点需要一个列表页，我们第一阶段是个位数，客户端里就能看全。
 *
 * 🔴 **不显示倍率列。** `UserNode.multiplier_e9` 在契约里是存在的（抄 Remnawave 留的字段），
 * `@babelplus/shared` 里也有现成的 `formatMultiplier` —— 两样东西摆在这儿，
 * 「顺手补上一列」是这一页最容易被改错的地方。但 product-brief §6 定的是**不引入倍率**，
 * 所以这里不是「都显示 1x」，而是**这一列根本不存在**：显示出来会让用户以为将来会有差别，
 * 然后开始比较哪条线路「更划算」。NodePage.test.tsx 钉死了这一条。
 *
 * 这一页只有一个请求，但错误态仍然**按 `ErrorCode` 分支**而不是按 HTTP 状态码：
 * 501（后端还没写）与 500（写了但炸了）对用户是两句不同的话，
 * 而它们归一后的 `kind` 都是 `server`。分支在 `QueryError` 里。
 */
import { Badge, Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { unwrap, type ApiError, type components } from '@babelplus/shared/api';
import { api } from '../lib/api.ts';
import {
  ListSkeleton,
  QueryError,
  fallbackErrorCopy,
  useResource,
  type ErrorCopy,
  type ResourceHandle,
} from './subscribe/_shared.tsx';

type UserNode = components['schemas']['UserNode'];

/** 契约：`GET /api/v1/user/nodes` → `UserNode[]`。 */
function listUserNodes(): Promise<UserNode[]> {
  return unwrap(api().GET('/api/v1/user/nodes'));
}

const STATUS_META: Record<UserNode['status'], { label: string; tone: 'ok' | 'warn' | 'danger' }> = {
  // 「在线 / 不稳定 / 离线」是用户能据以做决定的三个词。
  // 不写「正常」—— 在线不等于快，我们没有测速数据，说「正常」是一个证明不了的承诺。
  online: { label: '在线', tone: 'ok' },
  degraded: { label: '不稳定', tone: 'warn' },
  offline: { label: '离线', tone: 'danger' },
};

export default function NodePage() {
  const nodes = useResource(listUserNodes);

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">节点</h1>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">
          有哪些线路、现在通不通。日常用不到这一页 —— 客户端里就能看全。
        </p>
      </header>

      <NodeSection nodes={nodes} />
    </>
  );
}

/** 定义在模块级而不是页面组件体内：写在里面的话每次渲染都是一个新组件，整棵子树会被卸载重建。 */
function NodeSection({ nodes }: { nodes: ResourceHandle<UserNode[]> }) {
  if (nodes.state.status === 'loading') {
    return (
      <Card>
        <CardTitle>节点列表</CardTitle>
        <ListSkeleton rows={4} />
      </Card>
    );
  }

  if (nodes.state.status === 'error') {
    return (
      <QueryError
        error={nodes.state.error}
        what="节点列表"
        copy={nodeErrorCopy(nodes.state.error)}
        onRetry={nodes.reload}
        extra={<LinkButton href="/diagnose">跑一遍诊断</LinkButton>}
      />
    );
  }

  const list = nodes.state.data;

  // §3.2.9 的空态：**不是**「暂无数据」。没有可用节点几乎总是我们这边的问题，
  // 所以先替用户排除掉「是不是我账号出问题了」，再给一个真的能往下走的动作。
  if (list.length === 0) {
    return (
      <EmptyState
        title="当前没有可用节点"
        description="这通常是我们这边的问题，不是你的账号。已连接的设备可能仍在用最后一次成功拉取到的配置。"
        action={
          <LinkButton tone="primary" href="/diagnose">
            跑一遍诊断 <Icon.ArrowRight size={14} />
          </LinkButton>
        }
      />
    );
  }

  const onlineCount = list.filter((node) => node.status === 'online').length;

  return (
    <Card>
      <CardTitle hint={`共 ${list.length} 条线路 · ${onlineCount} 条在线`}>节点列表</CardTitle>

      {/* 全部离线时先说一句。用户看到一屏「离线」的第一反应是「我的账号是不是废了」，
          而这一页恰好能回答这个问题 —— 不回答，下一步就是一张工单。 */}
      {onlineCount === 0 ? (
        <p className="mb-3 rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm leading-relaxed text-warn">
          所有线路当前都显示离线。这通常是我们这边的问题，不是你的账号或订阅出了错。
        </p>
      ) : null}

      {/* 从一开始就是卡片列表而不是表格：三列的表格在 375px 上也会挤（§2.3 M1），
          而「先做表格再改卡片」从来没有人回头做。 */}
      <ul className="space-y-2">
        {list.map((node) => {
          const meta = STATUS_META[node.status] ?? { label: node.status, tone: 'warn' as const };
          return (
            <li
              key={node.id}
              className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-lg border border-line px-3 py-2.5"
            >
              <span className="min-w-0 flex-1 truncate text-sm font-medium text-fg">{node.name}</span>
              {/* 地区缺失时留空，不编一个「未知地区」—— 后者读起来像一条真实信息。 */}
              {node.region ? <span className="text-sm text-fg-muted">{node.region}</span> : null}
              <Badge tone={meta.tone}>{meta.label}</Badge>
            </li>
          );
        })}
      </ul>

      {/* 协议参数、负载、倍率一律不显示：
          前两者是后台的事（用户看了只会误判「这个协议是不是更快」），倍率见文件头。 */}
      <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
        这里只列出线路本身的状态。具体用哪一条由客户端自动选择，你不需要在这里挑。
      </p>
    </Card>
  );
}

/**
 * 节点列表的 `ErrorCode` → 文案。**这一页唯一按 code 分支的地方。**
 *
 * 只覆盖一条与别处说法不同的：5xx / 连不上时必须说「已连接的设备不受影响」。
 * 面板（控制面）与节点（数据面）是两套东西 —— 在**节点页**上把前者的故障说成后者，
 * 是这一页最贵的一种误导：用户会以为线路全没了，然后去申请退款。
 * 其余（501、封禁、限流…）落到 `fallbackErrorCopy`。
 */
function nodeErrorCopy(error: ApiError): ErrorCopy {
  if (error.code === 'NOT_IMPLEMENTED') return fallbackErrorCopy(error);
  if (error.kind === 'server' || error.kind === 'offline') {
    return {
      title: '读不到节点列表',
      description: '线路本身没有变化，已连接的设备不受影响 —— 这只是面板读不到这份清单。',
    };
  }
  return fallbackErrorCopy(error);
}
