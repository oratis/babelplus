/**
 * `/invite` —— P2。page-inventory §3.1 #15、§3.2.9。
 *
 * 两段式佣金（`确认中` / `累计获得`）照抄竞品 —— 退款冷静期防套利，这个设计是对的。
 *
 * 三条约束来自 user-journey §3.1：
 *  - 用户码**恒为一次性**（多次可用的码贴到论坛就等于开放注册）
 *  - 生成资格挂在「**有有效订阅**」上，不是「有账号」（否则邀请制退化成链式开放注册）
 *  - 每用户同时持有的未核销码上限 3 个（提案值，待运营校准）
 */
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function InvitePage() {
  return (
    <RouteScaffold
      title="邀请"
      description="生成邀请码，被邀请人消费后你拿佣金。"
      priority="P2"
      endpoints={['listInviteCodes', 'createInviteCode', 'listCommissions', 'transferCommission']}
      todo={
        <>
          生成按钮的可用性挂在<strong className="font-medium text-fg">「当前有有效订阅」</strong>上，
          不是「已登录」。未付费账号的可生成配额为 0 —— 否则邀请制会退化成链式开放注册。
        </>
      }
      empty={
        <EmptyState
          title="还没有邀请码"
          description="每个码只能用一次，有效期 30 天。被邀请人下单后，你会拿到一笔佣金。"
          action={
            <LinkButton tone="primary" href="/invite">
              生成一个 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
          secondary="需要有效订阅才能生成 —— 这是邀请制不退化成开放注册的那道闸。"
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P2): listInviteCodes / createInviteCode">我的邀请码</CardTitle>
          <LayoutSlot
            label="码 · 状态（未用 / 已核销 / 已过期）· 创建时间 · 复制"
            hint="一码一人，所以返佣归属天然无歧义。同时持有的未核销码上限 3 个（提案值）。"
          />
        </Card>

        <Card>
          <CardTitle hint="TODO(P2): listCommissions / transferCommission">佣金</CardTitle>
          <LayoutSlot
            label="确认中 / 累计获得 两段式 · 「划转到余额」"
            hint="两段式是退款冷静期的防套利设计，照抄竞品。划转后只能消费不能提现（见 /wallet 的常驻声明）。"
          />
        </Card>
      </div>
    </RouteScaffold>
  );
}
