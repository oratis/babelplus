/**
 * 模块 5 · 节点管理 `/admin/nodes` —— P1 / **M2**。
 * M2 的理由很具体：**手机上要能紧急停用节点**。节点出事的时候人不一定在电脑前。
 */
import { Card, CardTitle, EmptyState, Button } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function NodesPage() {
  return (
    <ModuleScaffold
      title="节点"
      description="线路的启停、健康与今日流量。手机上要能完成紧急停用。"
      priority="P1"
      mobile="M2"
      endpoints={['listAdminNodes', 'createAdminNode', 'enableAdminNode', 'disableAdminNode']}
      danger={['D4']}
      todo={
        <>
          停用确认框里<strong className="font-medium text-fg">必须显示当前在线人数</strong> ——
          「停用节点」和「让 37 个人立刻掉线」是同一件事，但只有后一种说法能让人停下来想一秒。
        </>
      }
      empty={
        <EmptyState
          title="还没有节点"
          description="没有节点，订阅拉下来是空列表，用户连不上任何东西。"
          action={
            <Button tone="primary" disabled>
              新建节点
            </Button>
          }
        />
      }
    >
      <Card>
        <CardTitle hint="TODO(P1): listAdminNodes">节点列表</CardTitle>
        <LayoutSlot
          label="名称 · 地区 · 协议 · 在线人数 · 最后上报 · 今日流量 · 健康"
          hint="7 列，<768px 卡片化（M2）。「最后上报」超过 push_interval 的数倍就该标黄 —— 节点侧的 60s 轮询是这个判据的基准。"
        />
      </Card>
    </ModuleScaffold>
  );
}
