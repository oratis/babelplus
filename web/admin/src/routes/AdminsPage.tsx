/**
 * 模块 11 · 管理员账号 `/admin/admins` —— P1 / M3。
 *
 * TOTP 状态是这一页最重要的一列：闸 3 是**强制**的，
 * 所以「TOTP 未开启」的管理员是一个必须被立刻发现并处理的状态，不是一个可选项。
 */
import { Card, CardTitle, EmptyState, Button } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function AdminsPage() {
  return (
    <ModuleScaffold
      title="管理员"
      description="账号、角色、TOTP 状态。"
      priority="P1"
      mobile="M3"
      endpoints={['listAdmins', 'createAdmin', 'deleteAdmin', 'resetAdminTotp']}
      danger={['D15', 'D16']}
      todo={
        <>
          🔴 <strong className="font-medium text-fg">禁止删除最后一个管理员。</strong>
          这条要在前端和后端各拦一次 —— 前端拦是为了不让人白点，后端拦是因为前端不可信，
          而这个错误的后果是把自己永久锁在门外。
        </>
      }
      empty={
        <EmptyState
          title="没有管理员"
          description="这个状态不该出现。如果你看到它，说明数据有问题 —— 因为你正在用一个管理员账号看这一页。"
          action={
            <Button tone="primary" disabled>
              新建管理员
            </Button>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): listAdmins">管理员列表</CardTitle>
          <LayoutSlot
            label="邮箱 · 角色 · 最后登录 · TOTP 状态"
            hint="TOTP 未开启要标红并置顶。它不是一个「待完善的设置」，是一道闸没关上。"
          />
        </Card>

        <Card>
          <CardTitle>权限位</CardTitle>
          <LayoutSlot
            label="D6（手工标记订单已支付）· D14（导出用户 CSV）两个独立权限位"
            hint="完整 RBAC 不在第一阶段范围内，1–3 人团队可以只有一个角色。但 D6 那个权限位必须从第一天就存在。"
          />
        </Card>
      </div>
    </ModuleScaffold>
  );
}
