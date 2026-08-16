/**
 * 模块 5 · 节点详情 `/admin/nodes/:id` —— P1 / M2。
 *
 * 🔴 D9「改节点协议参数」有一个很阴的失败模式：
 * Xray 保留了 `clients` → `users` 这类**静默别名**，写错不报错，只是行为不符预期。
 * 所以**保存前必须做 JSON schema 校验**，并保留上一版可一键回滚 ——
 * 「保存成功但节点静默不可用」是最难排查的一类故障。
 */
import { useParams } from 'react-router';
import { Card, CardTitle, EmptyState, Icon, LinkButton } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function NodeDetailPage() {
  const { id } = useParams();

  return (
    <ModuleScaffold
      title="节点详情"
      description={
        <>
          节点 <code className="font-mono text-fg">{id ?? '—'}</code>
        </>
      }
      priority="P1"
      mobile="M2"
      endpoints={['getAdminNode', 'updateAdminNode', 'deleteAdminNode']}
      danger={['D4', 'D9']}
      todo={
        <>
          协议参数编辑器要有 <strong className="font-medium text-fg">JSON schema 校验 + 上一版一键回滚</strong>。
          没有回滚的话，改坏参数的恢复路径是「凭记忆手打回去」，而那时节点已经在掉线了。
        </>
      }
      empty={
        <EmptyState
          title="找不到这个节点"
          description="它可能已经被删除。"
          action={
            <LinkButton tone="primary" href="/admin/nodes">
              回到节点列表 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): getAdminNode">基础</CardTitle>
          <LayoutSlot label="名称 · 地区 · 分组 · 标签 · 排序 · 上下架" />
        </Card>
        <Card>
          <CardTitle hint="D9 · 保存前 schema 校验">协议参数</CardTitle>
          <LayoutSlot
            label="JSON 编辑器 + 校验 + 与上一版的 diff + 回滚"
            hint="REALITY(TCP:443) 主 + Hysteria2(UDP:443) 加速 + SS-2022 兜底。TCP 路径开 mux，UDP 路径不开。"
          />
        </Card>
      </div>
    </ModuleScaffold>
  );
}
