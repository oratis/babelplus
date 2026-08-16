/**
 * 模块 6 · 节点密钥 `/admin/node-keys` —— P1 / M3。安全加固第 1 条的落地页。
 *
 * 这个模块存在的理由：Xboard / SSPanel 系用**全局共享 token**，
 * 持 token 者可冒充任意节点。我们改成每节点独立密钥，`node_id` 从密钥推导，
 * 请求里带的 `node_id` 一律忽略 —— 这是**根治点，不是缓解**。
 *
 * 🔴 D5 的 UI 层硬约束：**禁止一步完成轮换。**
 * 强制两步：先发新密钥 → 确认节点已用新密钥上报 → 再撤旧的。
 * 一步完成的话，节点会在下一次轮询时失联，而它失联之后你就没法再让它换密钥了。
 */
import { Card, CardTitle, EmptyState, Button } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function NodeKeysPage() {
  return (
    <ModuleScaffold
      title="节点密钥"
      description="每节点一把独立密钥。DB 只存 sha256，签发时的明文只出现一次。"
      priority="P1"
      mobile="M3"
      endpoints={['listAdminNodeKeys', 'createAdminNodeKey', 'revokeAdminNodeKey']}
      danger={['D5']}
      todo={
        <>
          轮换向导必须是<strong className="font-medium text-fg">三屏而不是一个按钮</strong>：
          ① 签发新密钥（显示明文，仅此一次）→ ② 等待该节点用新密钥上报（页面轮询确认）→ ③ 吊销旧密钥。
          第 ② 步没通过就不允许进入第 ③ 步。
        </>
      }
      empty={
        <EmptyState
          title="还没有签发过密钥"
          description="节点没有密钥就拉不到配置，也上报不了流量。新建节点后第一件事就是给它发一把。"
          action={
            <Button tone="primary" disabled>
              签发密钥
            </Button>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): listAdminNodeKeys">密钥列表</CardTitle>
          <LayoutSlot
            label="节点 · 指纹 · scope · 签发时间 · 最后使用 · 状态"
            hint="一个节点可以同时持有多把有效密钥 —— 这正是两步轮换能安全进行的前提。"
          />
        </Card>

        <Card>
          <CardTitle>过渡态：query 形态的 token</CardTitle>
          <div className="rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm leading-relaxed text-warn">
            <p className="font-medium">这是有期限的过渡态，不是目标态。</p>
            <p className="mt-1">
              v2node 很可能把 token 挂在 query string 上而不发 Authorization 头。
              过渡期允许，但三条约束：query 形态也必须是每节点独立密钥；每次经 query 认证写一条
              WARN 结构化日志（带 key_id），使其可见可计数；
              <strong className="font-semibold">全量切换前必须关闭</strong>。
            </p>
            <p className="mt-1">
              这一页要有一块「最近 24 小时经 query 认证的次数」，
              否则「必须关闭」会变成一句没人跟进的话。
            </p>
          </div>
        </Card>
      </div>
    </ModuleScaffold>
  );
}
