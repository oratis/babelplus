/**
 * 模块 7 · 流量统计 `/admin/stats` —— P1 / M3。**成本核算的唯一依据。**
 *
 * 只有日 / 月聚合，没有明细（落明细是这个业务的性能命门）。
 * 所以这一页能回答「这个月出口花了多少钱」，回答不了「某用户 14:32 用了多少」。
 */
import { Card, CardTitle, EmptyState, Icon, LinkButton, Button } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function StatsPage() {
  return (
    <ModuleScaffold
      title="流量统计"
      description="日 / 月聚合，以及按当前单价折算的出口成本。"
      priority="P1"
      mobile="M3"
      endpoints={['getAdminStats', 'exportAdminStats']}
      danger={['D14']}
      todo={
        <>
          出口单价<strong className="font-medium text-fg">必须是可配置项而不是常量</strong> ——
          它会随网络层级、区域和用量档位变化，写死在前端的那天起这一页给出的成本就是错的。
        </>
      }
      empty={
        <EmptyState
          title="还没有聚合数据"
          description="节点上报后按天聚合，所以第一天是空的。"
          action={
            <LinkButton tone="primary" href="/admin/nodes">
              看看节点有没有在上报 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
      actions={
        <Button tone="danger" disabled>
          导出 CSV（D14）
        </Button>
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): getAdminStats">全局</CardTitle>
          <LayoutSlot label="日 / 月曲线 · 上传 / 下载分色" />
        </Card>
        <Card>
          <CardTitle>按用户 / 按节点</CardTitle>
          <LayoutSlot
            label="两张排行表"
            hint="注意：user × server 的交叉维度目前没有表（用户面板 /usage 的「按节点分布」也卡在这里）。"
          />
        </Card>
        <Card>
          <CardTitle>出口成本估算</CardTitle>
          <LayoutSlot
            label="流量 × 可配置单价"
            hint="这是判断定价是否成立的唯一依据。单价变了这一页要跟着变，历史月份按当时单价保留。"
          />
        </Card>
      </div>
    </ModuleScaffold>
  );
}
