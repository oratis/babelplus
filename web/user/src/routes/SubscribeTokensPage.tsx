/**
 * `/subscribe/tokens` —— P3。page-inventory §3.1 #19、§3.2.9。安全加固第 2 条。
 *
 * P1 只做 `/subscribe` 上的「一键全撤」，多 token 是 P3。
 * 理由写在文档里：多 token 的价值在「手机一个、电脑一个、丢了单独撤」，
 * 这对个位数设备的用户是奢侈品。
 */
import { Button, Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function SubscribeTokensPage() {
  return (
    <RouteScaffold
      title="订阅 token"
      description="给不同设备发不同的订阅链接，丢了可以单独撤销。"
      priority="P3"
      endpoints={['listSubscriptionTokens', 'createSubscriptionToken', 'revokeSubscriptionToken']}
      todo={
        <>
          新建 token 的明文<strong className="font-medium text-fg">只在创建响应里出现一次</strong>，
          之后只显示指纹 —— 这是这个功能的安全价值所在，实现时不要为了方便而把明文存下来回显。
        </>
      }
      empty={
        <EmptyState
          title="只有一个默认 token"
          description="大多数人不需要多个。如果你在多台设备上用同一个链接，丢了任何一台都得全部重导 —— 那时候再来分开发也不迟。"
          action={
            <LinkButton tone="primary" href="/subscribe">
              回到订阅页 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
          secondary="P1 阶段这一页只读，创建与吊销走 /subscribe 上的「一键全撤」。"
        />
      }
    >
      <Card>
        <CardTitle hint="TODO(P3): listSubscriptionTokens">token 列表</CardTitle>
        <LayoutSlot
          label="命名 · 创建时间 · 最后使用 · 单独吊销"
          hint="「最后使用」这一列本身就有排障价值：一个从没被使用过的 token 说明客户端根本没配对。"
        />
        <Button className="mt-3" tone="danger" disabled>
          吊销全部（等同 /subscribe 的「重置订阅」）
        </Button>
      </Card>
    </RouteScaffold>
  );
}
