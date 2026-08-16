/**
 * 模块 8 · 工单会话 `/admin/tickets/:id` —— P1 / M2。
 *
 * 🔴 `ticket_messages.is_internal` 是整个系统最容易出安全事故的一列。
 * 后台这边的对应纪律：**内部备注与对用户可见的回复必须在视觉上截然不同**，
 * 不能只差一个小标签。误把内部备注当回复发出去，是这个模块最可能出的事故。
 */
import { useParams } from 'react-router';
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function TicketDetailPage() {
  const { id } = useParams();

  return (
    <ModuleScaffold
      title="工单会话"
      description={
        <>
          工单 <code className="font-mono text-fg">{id ?? '—'}</code>
        </>
      }
      priority="P1"
      mobile="M2"
      endpoints={['getAdminTicket', 'updateAdminTicket', 'createAdminTicketMessage']}
      todo={
        <>
          回复框要有<strong className="font-medium text-fg">「对用户可见」/「内部备注」两个物理上分开的入口</strong>，
          不是一个开关。开关会被误触，两个按钮不会。发送前再显示一次目标（「这条用户会看到」）。
        </>
      }
      empty={
        <EmptyState
          title="找不到这个工单"
          description="它可能已经被合并或删除。"
          action={
            <LinkButton tone="primary" href="/admin/tickets">
              回到队列 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle>会话</CardTitle>
          <LayoutSlot
            label="用户消息 / 客服回复 / 内部备注（三种视觉，不是两种加标签）"
            hint="内部备注建议整块换底色 + 左侧竖条 + 明确文字标注，让人在扫读时也不会看错。"
          />
        </Card>
        <Card>
          <CardTitle>诊断上下文</CardTitle>
          <LayoutSlot
            label="建单时的快照：订阅 id · 套餐 · UA · 最近节点 · 设备数 · 最近拉取时间"
            hint="这是「报障当时的事实」。用户事后续了费，这份快照也不该变 —— 否则复盘时看到的是一个不存在过的状态。"
          />
        </Card>
        <Card>
          <CardTitle>处理</CardTitle>
          <LayoutSlot label="状态 · 优先级 · 指派 · 标签 · 快捷回复" />
        </Card>
      </div>
    </ModuleScaffold>
  );
}
