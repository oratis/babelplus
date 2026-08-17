/** 模块 13 · 优惠码 `/admin/coupons` —— P2 / M3。改价类操作同 D8。 */
import { Card, CardTitle, EmptyState, Button } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function CouponsPage() {
  return (
    <ModuleScaffold
      title="优惠码"
      description="折扣券的发放与使用记录。"
      priority="P2"
      mobile="M3"
      endpoints={['listAdminCoupons', 'createAdminCoupon', 'updateAdminCoupon', 'deleteAdminCoupon']}
      danger={['D8']}
      todo={
        <>
          券必须有<strong className="font-medium text-fg">总量上限与单用户次数上限</strong>，
          两者都不填的券等于对外公开的无限折扣。
        </>
      }
      empty={
        <EmptyState
          title="还没有优惠码"
          description="长周期折扣已经写进套餐价格里了，优惠码是额外的运营手段，不是必需品。"
          action={
            <Button tone="primary" disabled>
              新建优惠码
            </Button>
          }
        />
      }
    >
      <Card>
        <CardTitle hint="TODO(P2): listAdminCoupons">券列表</CardTitle>
        <LayoutSlot label="码 · 折扣 · 适用套餐 · 总量 / 已用 · 有效期 · 使用记录" />
      </Card>
    </ModuleScaffold>
  );
}
