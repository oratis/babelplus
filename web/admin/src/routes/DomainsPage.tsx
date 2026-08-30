/**
 * 模块 17 · 域名池与可达性 `/admin/domains` —— P3 / M3。
 *
 * 这个模块的产出直接喂给用户面板的 `runtime-config.js`（备用域名列表）。
 * 所以它虽然排到 P3，但**它决定了域名被封时的恢复速度** ——
 * ADR 0003 §5 要求「部署流水线支持一键新增镜像域名」，这一页就是那个「一键」的入口。
 *
 * # 🔴 三个端点都是 501，而且这一页**不做成看起来能用的样子**
 *
 * `listAdminDomains` / `createAdminDomain` / `deleteAdminDomain` 全部落在
 * `handler/unimplemented.gen.go` 上。卡住它们的不是工时，是**两件还没做的裁决**：
 *
 *  1. **`domains` 表不存在。** 迁移 0001–0019 里没有它，也没有任何查询层。
 *  2. **两套互不兼容的字段模型。** 冻结契约里的 `Domain` 是
 *     `{id, hostname, role: api|web|subscribe|admin, enabled, reachable, last_checked_at}`；
 *     而 ADR 0011 §7.2 给的条目是
 *     `{host, url, label, state: active|degraded|suspect|blocked|retired, order, platform, registrable}`
 *     —— 后者多出的 `platform` 与 `registrable` 不是装饰：0011 §2.1 与 §6.1 要求
 *     故障转移**优先跳到不同平台、不同可注册域名**的候选，没有这两个字段就做不到。
 *     0011 自己写明「这是一次破坏性变更，不是保持兼容」。
 *
 * ADR 0010 与 0011 的状态都是**提案，未批准**（2026-08-23），
 * api-contract §14 那一条也明写「本条不划掉」。
 * 0011 §14 还要求在现有三个端点之外补 `PATCH /admin/domains/{id}`、
 * `POST …/mark-blocked`、`POST …/promote`（新的 D17，要 L1+L2+L3）。
 *
 * 所以现在按冻结契约做一版能点的增删界面，等裁决落地要整个推翻 ——
 * 而在推翻之前，它会让人以为「域名池已经能管了」。
 * **这一页因此没有添加按钮、没有删除按钮，一个都没有。**
 *
 * ⚠️ 另一个必须写在页面上的诚实限制：**我们没有做过大陆侧的可达性实测。**
 * ADR 0003 §7 的第一条未决项就是这个。在实测数据接进来之前，
 * 这一页显示的「可达性」只能是我们自己的探活结果（境外视角），
 * **不等于中国大陆用户的实际体验**。
 */
import { PageHeader } from '@babelplus/shared/ui';
import type { components } from '@babelplus/shared/api';
import { unwrap } from '@babelplus/shared/api';
import { Badge, Card, CardTitle, formatDateTime } from './_imports.ts';
import {
  CaveatNotice,
  ListSkeleton,
  MISSING,
  QueryErrorState,
  isNotImplemented,
  useOpsQuery,
} from './ops-common.tsx';
import { api } from '../lib/api.ts';

type Domain = components['schemas']['Domain'];

function loadDomains(): Promise<Domain[]> {
  return unwrap(api().GET('/api/v1/admin/domains'));
}

/**
 * 501 时说明卡在哪。
 *
 * 🔴 这段话必须说出「卡在裁决上」而不是「排期还没到」——
 * 后者会让人每周来点一次看看好了没有，前者才让人知道**该去推动什么**。
 */
const WHY_NOT_IMPLEMENTED = (
  <>
    <p>
      卡住它的不是工时，是两件还没做的裁决：
    </p>
    <ul className="mt-2 space-y-1.5">
      <li>
        · <strong className="font-medium text-fg"><code className="font-mono">domains</code> 表不存在。</strong>
        迁移 0001–0019 里没有它，也没有对应的查询层。
      </li>
      <li>
        · <strong className="font-medium text-fg">字段模型有两套，且不兼容。</strong>
        冻结契约里的 <code className="font-mono">Domain</code> 是{' '}
        <code className="font-mono">hostname / role / enabled / reachable</code>；
        ADR 0011 §7.2 要的是{' '}
        <code className="font-mono">host / url / label / state / order / platform / registrable</code>。
        多出来的 <code className="font-mono">platform</code> 与{' '}
        <code className="font-mono">registrable</code> 不是装饰 —— 故障转移必须优先跳到
        <strong className="font-medium text-fg">不同平台、不同可注册域名</strong>的候选，
        少了这两个字段就会在同一个封锁坑里连试三次。0011 自己写明这是破坏性变更。
      </li>
    </ul>
    <p className="mt-2">
      ADR 0010 与 0011 的状态都是<strong className="font-medium text-fg">提案，未批准</strong>
      （2026-08-23），api-contract §14 的对应条目也明写「本条不划掉」。
      先落哪一套模型定了，这三个端点才有得写。
    </p>
  </>
);

export default function DomainsPage() {
  const query = useOpsQuery<Domain[]>(loadDomains, [], '域名池加载失败');
  const rows = query.data ?? [];
  const notImplemented = isNotImplemented(query.error);

  return (
    <>
      <PageHeader
        title="域名池"
        description="镜像域名的探活状态、证书到期、最近异常。"
        meta={
          <>
            <Badge tone="neutral">P3</Badge>
            <Badge tone="neutral">M3 · 桌面优先，手机上可读即可</Badge>
            {notImplemented ? <Badge tone="warn">尚未开放</Badge> : null}
          </>
        }
      />

      <div className="space-y-4">
        {query.state === 'loading' ? <ListSkeleton rows={4} /> : null}

        {query.state === 'error' && query.error ? (
          <QueryErrorState
            error={query.error}
            what="域名池"
            why={WHY_NOT_IMPLEMENTED}
            onRetry={notImplemented ? undefined : query.reload}
          />
        ) : null}

        {/* 服务端目前一定是 501，所以下面这段在今天不会渲染。
            仍然写出来，是因为它是**只读**视图：哪天 `domains` 表落地了，
            列表立刻能看，而增删仍然要等模型裁决 —— 两件事的解锁时间不一样。 */}
        {query.state === 'ready' ? (
          rows.length === 0 ? (
            <Card>
              <CardTitle>域名池是空的</CardTitle>
              <p className="text-sm leading-relaxed text-fg-muted">
                至少要有 2 个互为镜像的自有域名（ADR 0003 §5）。
                平台子域名一律不用 —— 被整体处置时没有任何补救手段。
                <strong className="font-medium text-fg"> 这一页现在登记不了域名</strong>：
                写入端点也是 501，理由见下。
              </p>
            </Card>
          ) : (
            <Card>
              <CardTitle hint={`${rows.length} 个`}>域名列表（只读）</CardTitle>
              <div className="overflow-x-auto">
                <table className="w-full min-w-[36rem] text-sm">
                  <thead>
                    <tr className="border-b border-line text-left text-xs text-fg-muted">
                      <th className="py-2 pr-3 font-medium">域名</th>
                      <th className="py-2 pr-3 font-medium">用途</th>
                      <th className="py-2 pr-3 font-medium">启用</th>
                      <th className="py-2 pr-3 font-medium">探活（境外视角）</th>
                      <th className="py-2 pr-3 font-medium">最后探活</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((d) => (
                      <tr key={d.id} className="border-b border-line/60">
                        <td className="py-2 pr-3 font-mono">{d.hostname}</td>
                        <td className="py-2 pr-3">{d.role}</td>
                        <td className="py-2 pr-3">{d.enabled ? '是' : '否'}</td>
                        <td className="py-2 pr-3">
                          {d.reachable === undefined ? MISSING : d.reachable ? '通' : '不通'}
                        </td>
                        <td className="py-2 pr-3">{formatDateTime(d.last_checked_at)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <CaveatNotice>
                这张表里没有<strong className="font-medium text-fg">证书签发者与到期时间</strong> ——
                契约的 <code className="font-mono">Domain</code> 上没有这两个字段。
                而「证书签发者变了」是一个会静默发生、后果却很直接的变化（要钉 Let&apos;s Encrypt），
                现在这一页看不见它。
              </CaveatNotice>
            </Card>
          )
        ) : null}

        <Card>
          <CardTitle>为什么这一页没有「添加域名」按钮</CardTitle>
          <p className="text-sm leading-relaxed text-fg-muted">
            因为写入端点（<code className="font-mono">createAdminDomain</code> /{' '}
            <code className="font-mono">deleteAdminDomain</code>）也是 501，
            而且<strong className="font-medium text-fg">按现在这份冻结契约做出来的表单是要被推翻的</strong>：
            ADR 0011 §7.2 的条目模型与契约里的 <code className="font-mono">Domain</code> 不是一套。
            一个点下去只会得到 501 的按钮，比没有按钮更糟 ——
            它把「这件事还没决定」显示成了「这件事坏了」。
          </p>
          <p className="mt-2 text-sm leading-relaxed text-fg-muted">
            这段时间里，镜像域名列表仍然由各前端的{' '}
            <code className="font-mono">runtime-config.js</code> 直接下发（覆盖那一个文件即可，不必重新构建）。
            那条路径是通的，只是没有后台界面。
          </p>
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
          <p className="mt-2 text-sm leading-relaxed text-fg-muted">
            ⚠️ 更前一步：<strong className="font-medium text-fg">探活机制本身还不存在</strong>。
            契约里 <code className="font-mono">last_checked_at</code> 的说明就写着这一条，
            那个字段目前只能手工维护。所以 product-brief §8 承诺的
            「域名失联恢复 ≤ 30 分钟」<strong className="font-medium text-fg">目前零机制支撑</strong>。
          </p>
        </Card>
      </div>
    </>
  );
}
