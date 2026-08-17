/**
 * `/ticket/:public_id` —— P1，无替代。page-inventory §3.1 #11、§3.2.6。
 *
 * 🔴 安全提示（§3.2.6 末尾）：`ticket_messages.is_internal` 是整个系统最容易出安全事故的一列。
 * **用户侧查询必须走固定带 `is_internal = false` 的视图或方法，不接受调用方传参决定。**
 * 前端这边的对应纪律：**永远不要渲染任何标着 internal 的消息，即使 API 误发了**
 * —— 客户端做一次兜底过滤，成本是一行，代价是零。
 *
 * 附件上传失败时**保留已输入的正文，绝不清空**（§3.2.6 错态）。
 */
import { useParams } from 'react-router';
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function TicketDetailPage() {
  const { public_id: publicId } = useParams();

  return (
    <RouteScaffold
      title="工单会话"
      description={
        <>
          工单号 <code className="font-mono text-fg">{publicId ?? '—'}</code>
        </>
      }
      priority="P1"
      endpoints={['getTicket', 'createTicketMessage', 'closeTicket']}
      todo={
        <>
          回复框的草稿必须<strong className="font-medium text-fg">在附件上传失败时保留</strong>；
          并且客户端要对消息列表做一次 <code className="font-mono">is_internal</code> 兜底过滤 ——
          内部备注一旦渲染出来就是事故。
        </>
      }
      empty={
        <EmptyState
          title="找不到这个工单"
          description="工单号可能不对，或者它不属于当前账号。"
          action={
            <LinkButton tone="primary" href="/ticket">
              回到工单列表 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): getTicket">会话</CardTitle>
          <LayoutSlot
            label="消息流（用户 / 客服左右分列）· 状态标签 · 时间"
            hint="只渲染 is_internal = false 的消息，客户端再兜底过滤一次。"
          />
        </Card>

        <Card>
          <CardTitle hint="建单时抓的快照，事后不随账号变化而变">诊断上下文</CardTitle>
          <LayoutSlot
            label="订阅 id · 套餐 · 客户端 UA · 最近节点 · 设备数 · 最近一次订阅拉取时间"
            hint="工单记录的是「报障当时的事实」。用户事后续费或换节点，不应该改变这份快照。"
          />
        </Card>

        <Card>
          <CardTitle hint="TODO(P1): createTicketMessage">回复</CardTitle>
          <LayoutSlot
            label="富文本框 + 附件上传 + 发送"
            hint="上传失败要单独提示并保留正文。用户打字打了五分钟，一次上传失败清空重来是最招骂的实现。"
          />
        </Card>
      </div>
    </RouteScaffold>
  );
}
