/**
 * `/notice` —— P2，但**它本身是恢复路径**。page-inventory §3.1 #17、§3.2.9。
 *
 * 存在的真正理由：公告兼作**域名广播位**。
 * 竞品的置顶公告第一条就是「官网域名（防失联）」，2023-06-03 发布并长期置顶。
 * dashboard 只轮播 3 条，历史公告必须可查 —— 否则去年那条域名公告就找不回来了。
 *
 * 因为它是恢复路径，所以这一页比别的页更该：体积小、依赖少、失败时仍然显示备用域名列表。
 */
import { Card, CardTitle, EmptyState, Icon, LinkButton, MirrorDomainList } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function NoticePage() {
  return (
    <RouteScaffold
      title="公告"
      description="服务变更、维护窗口、域名更新都发在这里。"
      priority="P2"
      endpoints={['listNotices']}
      todo={
        <>
          置顶公告要能独立于分页始终排在最前 —— 域名广播那条不能因为翻页而消失。
        </>
      }
      empty={
        <EmptyState
          title="还没有公告"
          description="有服务变更或域名更新时会发在这里，同时也会给你发邮件。"
          action={
            <LinkButton tone="primary" href="/dashboard">
              回到概览 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P2): listNotices（游标分页）">全部公告</CardTitle>
          <LayoutSlot
            label="置顶区 + 按发布日期倒序的列表 + 详情展开"
            hint="域名类公告要有视觉上的区分度 —— 它和「本周维护」不是一个重要级别。"
          />
        </Card>

        {/* 这一页是恢复路径，所以正文里也放一次备用域名，不只靠页脚。 */}
        <MirrorDomainList />
      </div>
    </RouteScaffold>
  );
}
