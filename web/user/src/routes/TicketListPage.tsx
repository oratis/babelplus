/**
 * `/ticket` —— P1，无替代。page-inventory §3.1 #10、§3.2.6。
 *
 * 空态**不是**「暂无工单」，而是排障决策树的入口 —— 这是把 12+ 篇排障文档
 * 接到工单入口上的唯一位置。文档写了没人看，等于没写。
 */
import { Card, CardTitle, EmptyState, Icon, LinkButton, Button, runtimeConfig } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function TicketListPage() {
  const cfg = runtimeConfig();

  return (
    <RouteScaffold
      title="工单"
      description="自己解决不了的，交给我们。"
      priority="P1"
      endpoints={['listTickets', 'createTicket']}
      todo={
        <>
          新建工单表单：分类必选（<code className="font-mono">subscription</code> /{' '}
          <code className="font-mono">node-down</code> / <code className="font-mono">billing</code> /{' '}
          <code className="font-mono">account</code>），
          <strong className="font-medium text-fg">选定分类后先弹出对应的排障文档链接</strong>，
          再给一个「我已经看过了，还是要提单」的按钮。
        </>
      }
      empty={
        // §3.2.6：空态是决策树入口，不是「暂无工单」。
        <EmptyState
          title="大部分问题在这里能自己解决"
          description="连不上、订阅拉不到、流量对不上 —— 这三类占了绝大多数。排障文档按现象分类，照着走一遍通常几分钟就好了。"
          action={
            cfg.docsUrl ? (
              <LinkButton tone="primary" href={cfg.docsUrl} external>
                打开排障决策树 <Icon.External size={14} />
              </LinkButton>
            ) : (
              <Button tone="primary" disabled title="docsUrl 未配置">
                打开排障决策树
              </Button>
            )
          }
          secondary={
            <>
              还是不行？{' '}
              <span className="text-accent">新建工单</span>{' '}
              —— 建单时会自动带上你的账号快照，不用自己描述环境。
            </>
          }
        />
      }
    >
      <Card>
        <CardTitle hint="TODO(P1): listTickets">工单列表</CardTitle>
        <LayoutSlot
          label="public_id · 主题 · 分类 · 优先级 · 状态 · 最后回复"
          hint="6 列，<768px 卡片化。列表用 public_id 而不是自增 id —— 自增 id 会泄漏工单总量。"
        />
      </Card>
    </RouteScaffold>
  );
}
