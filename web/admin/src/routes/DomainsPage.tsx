/**
 * 模块 17 · 域名池与可达性 `/admin/domains` —— P3 / M3。
 *
 * 这个模块的产出直接喂给用户面板的 `runtime-config.js`（备用域名列表）。
 * 所以它虽然排到 P3，但**它决定了域名被封时的恢复速度** ——
 * ADR 0003 §5 要求「部署流水线支持一键新增镜像域名」，这一页就是那个「一键」的入口。
 *
 * ⚠️ 一个诚实的限制：**我们没有做过自己的可达性实测。**
 * ADR 0003 §7 的第一条未决项就是这个。在实测数据接进来之前，
 * 这一页显示的「可达性」只能是我们自己的探活结果（境外视角），
 * **不等于中国大陆用户的实际体验** —— 页面上必须写明这一点，否则会误导决策。
 */
import { Card, CardTitle, EmptyState, Button } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function DomainsPage() {
  return (
    <ModuleScaffold
      title="域名池"
      description="镜像域名的探活状态、证书到期、最近异常。"
      priority="P3"
      mobile="M3"
      endpoints={['listAdminDomains', 'createAdminDomain', 'deleteAdminDomain']}
      danger={['D13']}
      todo={
        <>
          证书要<strong className="font-medium text-fg">钉 Let&apos;s Encrypt</strong>，
          页面上要显示每个域名当前证书的签发者 —— 签发者变了要告警，
          这是一个会静默发生、后果却很直接的变化。
        </>
      }
      empty={
        <EmptyState
          title="还没有登记域名"
          description="至少要有 2 个互为镜像的自有域名（ADR 0003 §5）。平台子域名一律不用 —— 被整体处置时没有任何补救手段。"
          action={
            <Button tone="primary" disabled>
              添加域名
            </Button>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P3): listAdminDomains">域名列表</CardTitle>
          <LayoutSlot
            label="域名 · 用途（面板 / 文档 / API / 后台）· 探活状态 · 证书签发者与到期 · 最近异常"
            hint="改动后要能一键同步到各前端的 runtime-config.js —— 这一环不通，前面所有设计都退化成「改代码重新构建」。"
          />
        </Card>

        <Card>
          <CardTitle>这里的「可达」是从哪里看的</CardTitle>
          <p className="text-sm leading-relaxed text-fg-muted">
            探活跑在我们自己的基础设施上，看到的是<strong className="font-medium text-fg">境外视角</strong>。
            它能告诉你「服务还在」，<strong className="font-medium text-fg">不能</strong>告诉你
            「中国大陆用户现在打得开」。这两件事经常不一致 ——
            域名被封时探活多半一切正常。
          </p>
          <p className="mt-2 text-sm leading-relaxed text-fg-muted">
            大陆侧实测尚未开展（ADR 0003 §7 第一条未决项）。在它落地之前，
            这一页的绿灯不能作为「用户能访问」的证据。
          </p>
        </Card>
      </div>
    </ModuleScaffold>
  );
}
