/** 模块 1 · 运营看板 `/admin` —— P1 / M3。page-inventory §4.2。 */
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function DashboardPage() {
  return (
    <ModuleScaffold
      title="运营看板"
      description="今天发生了什么，有没有需要马上处理的。"
      priority="P1"
      mobile="M3"
      endpoints={['getAdminDashboard']}
      todo={
        <>
          「节点异常」与「待回工单」两块要能直接点进对应模块 ——
          看板的价值在于<strong className="font-medium text-fg">缩短从发现到处理的距离</strong>，
          不是把数字排好看。
        </>
      }
      empty={
        <EmptyState
          title="今天还没有数据"
          description="订单、注册、流量都是零。新部署的第一天这是正常的。"
          action={
            <LinkButton tone="primary" href="/admin/nodes">
              先检查节点 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Card>
          <CardTitle>今日订单 / 金额</CardTitle>
          <LayoutSlot label="笔数 · 金额（分，展示走 formatCny）" />
        </Card>
        <Card>
          <CardTitle>新注册</CardTitle>
          <LayoutSlot label="人数 · 来源邀请码" />
        </Card>
        <Card>
          <CardTitle>总流量</CardTitle>
          <LayoutSlot label="今日 u + d · 出口成本估算" hint="成本估算是判断定价是否成立的唯一依据。" />
        </Card>
        <Card>
          <CardTitle>待回工单</CardTitle>
          <LayoutSlot label="按 SLA 剩余时间排序的前 5 条" />
        </Card>
        <Card className="sm:col-span-2">
          <CardTitle hint="最该被一眼看到的一块">节点异常</CardTitle>
          <LayoutSlot
            label="最后上报时间超过阈值的节点"
            hint="节点失联的用户感知是「突然连不上」，而这时候用户多半也打不开面板来告诉我们。"
          />
        </Card>
      </div>
    </ModuleScaffold>
  );
}
