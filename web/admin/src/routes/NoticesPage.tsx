/**
 * 模块 12 · 公告管理 `/admin/notices` —— P2 / M3。
 *
 * 🔴 公告兼**域名广播位**（D12）。写错一个字母的域名，就是把失联的用户导向一个陌生站点 ——
 * 而这群用户此刻正处在「面板打不开、正在找备用地址」的状态，戒备心最低。
 * 所以 D12 要求**强制预览**，且预览里要把所有链接的目标域名单独列出来核对。
 */
import { Card, CardTitle, EmptyState, Button } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function NoticesPage() {
  return (
    <ModuleScaffold
      title="公告"
      description="服务变更、维护窗口，以及最重要的：域名广播。"
      priority="P2"
      mobile="M3"
      endpoints={['listAdminNotices', 'createAdminNotice', 'updateAdminNotice', 'deleteAdminNotice']}
      danger={['D12']}
      todo={
        <>
          发布前的预览要<strong className="font-medium text-fg">单独列出正文里所有链接的目标域名</strong>，
          逐条核对后才能发。域名公告是用户在失联时唯一的指路牌，指错一次的代价不可逆。
        </>
      }
      empty={
        <EmptyState
          title="还没有公告"
          description="建议第一条就是域名公告并置顶 —— 竞品那条 2023 年发的域名公告至今仍置顶，这个做法是对的。"
          action={
            <Button tone="primary" disabled>
              新建公告
            </Button>
          }
        />
      }
    >
      <Card>
        <CardTitle hint="TODO(P2): listAdminNotices">公告列表</CardTitle>
        <LayoutSlot label="标题 · 发布状态 · 置顶 · 发布时间" />
      </Card>
    </ModuleScaffold>
  );
}
