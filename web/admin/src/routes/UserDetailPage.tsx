/** 模块 2 · 用户详情 `/admin/users/:id` —— P1 / M3。五条危险操作全在这一页。 */
import { useParams } from 'react-router';
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function UserDetailPage() {
  const { id } = useParams();

  return (
    <ModuleScaffold
      title="用户详情"
      description={
        <>
          用户 <code className="font-mono text-fg">{id ?? '—'}</code>
        </>
      }
      priority="P1"
      mobile="M3"
      endpoints={[
        'getAdminUser',
        'updateAdminUser',
        'banAdminUser',
        'unbanAdminUser',
        'revokeAdminUserSubscriptions',
        'adjustAdminUserBalance',
      ]}
      danger={['D1', 'D2', 'D3', 'D10']}
      todo={
        <>
          每一个危险按钮都要走同一个 <code className="font-mono">DangerAction</code> 组件：
          它负责确认串校验、必填原因、以及把改前值 / 改后值一起提交给审计。
          <strong className="font-medium text-fg">不要在页面里各写各的确认弹窗</strong> ——
          写五遍就会漏掉一遍，而漏掉的那一遍一定是最贵的那个按钮。
        </>
      }
      empty={
        <EmptyState
          title="找不到这个用户"
          description="id 可能不对，或者账号已经被删除。"
          action={
            <LinkButton tone="primary" href="/admin/users">
              回到用户列表 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): getAdminUser">概况</CardTitle>
          <LayoutSlot label="邮箱 · 状态 · 套餐 · 配额 / 已用 · 到期 · 设备数 · 邀请人 · 备注" />
        </Card>
        <Card>
          <CardTitle>历史</CardTitle>
          <LayoutSlot
            label="订单史 · 工单史 · 订阅拉取审计 · 余额流水 · 设备列表"
            hint="订阅拉取审计是识别账号共享的唯一数据来源，排障时先看这一张。"
          />
        </Card>
        <Card>
          <CardTitle hint="全部走 DangerAction">操作</CardTitle>
          <LayoutSlot
            label="改配额 / 到期（D1）· 封禁（D2）· 吊销订阅 token（D3）· 调整余额（D10）"
            hint="D2 封禁后用户 60 秒内断网 —— 配置下发是 60s 轮询，确认框里要写清楚这个时延。"
          />
        </Card>
      </div>
    </ModuleScaffold>
  );
}
