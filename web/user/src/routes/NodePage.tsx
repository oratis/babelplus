/**
 * `/node` —— P2，**从竞品的 P1 降级**。page-inventory §3.1 #16、§3.2.9。
 *
 * 降级理由：竞品 58 个节点需要一个列表页，我们第一阶段是个位数，客户端里就能看全。
 * **不引入倍率**（product-brief §6），所以倍率列直接不显示 —— 不是「显示 1x」，是不存在这一列。
 */
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function NodePage() {
  return (
    <RouteScaffold
      title="节点"
      description="有哪些线路、现在通不通。日常用不到这一页 —— 客户端里就能看全。"
      priority="P2"
      endpoints={['listUserNodes']}
      todo={
        <>
          <strong className="font-medium text-fg">不显示倍率列。</strong>
          我们不引入倍率，所以这里不是「都显示 1x」，而是这一列根本不存在 ——
          显示出来会让用户以为将来会有差别。
        </>
      }
      empty={
        <EmptyState
          title="当前没有可用节点"
          description="这通常是我们这边的问题，不是你的账号。已连接的设备可能仍在用最后一次成功拉取到的配置。"
          action={
            <LinkButton tone="primary" href="/diagnose">
              跑一遍诊断 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <Card>
        <CardTitle hint="TODO(P2): listUserNodes">节点列表</CardTitle>
        <LayoutSlot
          label="名称 · 地区 · 在线状态"
          hint="三列就够。协议参数、负载这些是后台的事，用户看了只会误判。"
        />
      </Card>
    </RouteScaffold>
  );
}
