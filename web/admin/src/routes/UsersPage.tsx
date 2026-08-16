/**
 * 模块 2 · 用户管理 `/admin/users` —— P1 / M3。**全系统最危险的模块**（page-inventory §4.2）。
 *
 * 三条「刻意不做」的能力，必须写在这里而不是被当成遗漏（§4.3）：
 *  ❌ 按用户查「访问了哪些网站」—— 不落目的地址日志。**后台不存在这个查询入口，
 *     这是刻意的，既是隐私承诺也是自我保护。**
 *  ❌ 以用户身份登录（impersonate）—— 一旦有这个按钮，管理员就能看到用户的订阅 token。
 *  ❌ 流量明细流水 —— 只存日 / 月聚合，查不到「某用户 14:32 用了多少」。
 */
import { Card, CardTitle, EmptyState, Icon, LinkButton, Button } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function UsersPage() {
  return (
    <ModuleScaffold
      title="用户"
      description="全系统最危险的模块。这里的每一次改动都直接影响钱或服务可用性。"
      priority="P1"
      mobile="M3"
      endpoints={['listAdminUsers', 'exportAdminUsers']}
      danger={['D14']}
      todo={
        <>
          搜索要支持邮箱精确匹配与邀请码反查。
          <strong className="font-medium text-fg">列表默认不显示订阅 token，任何形式都不显示</strong> ——
          这是 impersonate 之外的第二条泄漏路径。
        </>
      }
      empty={
        <EmptyState
          title="还没有用户"
          description="邀请制注册，所以第一批用户需要先在「邀请与返佣」里发种子码。"
          action={
            <LinkButton tone="primary" href="/admin/invites">
              去发邀请码 <Icon.ArrowRight size={14} />
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
          <CardTitle hint="TODO(P1): listAdminUsers">用户列表</CardTitle>
          <LayoutSlot
            label="邮箱 · 套餐 · 流量 · 到期 · 设备数 · 状态 · 注册时间 · 邀请人"
            hint="8 列，M3 桌面优先。管理面可以传 ?count=true 拿总数（用户面不提供这个能力）。"
          />
        </Card>

        {/* 「不做」的能力要在 UI 上被看见，否则半年后会有人当成遗漏补上去。 */}
        <Card>
          <CardTitle>这里刻意查不到的三件事</CardTitle>
          <ul className="space-y-2 text-sm leading-relaxed text-fg-muted">
            <li>
              <strong className="font-medium text-fg">用户访问了哪些网站</strong> ——
              我们不落目的地址日志。这个查询入口不存在，既是对用户的隐私承诺，也是对我们自己的保护。
            </li>
            <li>
              <strong className="font-medium text-fg">以用户身份登录</strong> ——
              一旦有这个按钮，管理员就能看到用户的订阅 token。第一阶段不做；
              将来若要做，必须审计 + 用户侧可见记录。
            </li>
            <li>
              <strong className="font-medium text-fg">流量明细流水</strong> ——
              只存日 / 月聚合（落明细是这个业务的性能命门）。查不到「某用户 14:32 用了多少」。
            </li>
          </ul>
        </Card>
      </div>
    </ModuleScaffold>
  );
}
