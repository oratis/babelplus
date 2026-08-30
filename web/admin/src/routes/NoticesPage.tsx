/**
 * 模块 12 · 公告管理 `/admin/notices` —— P2 / M3。危险操作 **D12**。
 *
 * 端点：`listAdminNotices` · `createAdminNotice` · `updateAdminNotice` · `deleteAdminNotice`。
 *
 * 🔴 **公告兼域名广播位。** 写错一个字母的域名，就是把失联的用户导向一个陌生站点 ——
 * 而这群用户此刻正处在「面板打不开、正在找备用地址」的状态，**戒备心最低**。
 * 所以 D12 的登记表要求**强制预览**，本页把它实现成一道真正的闸：
 *
 *   正文里出现的**每一个链接目标主机名**都被单独列出来，逐条勾选核对之后，
 *   再加一次「整篇预览我已逐字读过」，发布按钮才点得动。
 *
 * 三个实现选择，每一个都有理由：
 *
 *  1. **预览里不渲染 Markdown，只显示原文。** 渲染成可点的链接恰恰会掩盖问题：
 *     `[备用地址](https://babe1plus.com)` 渲染之后显示的是「备用地址」四个字，
 *     而要核对的那个 `1` 藏在 href 里。核对域名要看的是**原文**。
 *  2. **主机名单独拎出来一行一个。** 混在正文里读，`babe1plus.com` 与 `babelplus.com`
 *     没有人能一眼分辨；拎出来并排放，差异才显形。
 *  3. **改一个字就要重新核对。** 正文一变，已勾选的核对全部作废 ——
 *     否则「先核对、再手滑改一个字母、直接发布」这条路径是通的，而它正是这一层要挡的。
 *
 * # 契约缺口（写在界面上，不藏着）
 *
 *  - 🔴 `NoticeUpsert` **没有 `reason` 字段** → D12 的操作原因**写不进审计**。
 *    服务端每次都会记一条 WARN（`bp_admin_audit_no_reason`），审计里有完整的前后像
 *    （含正文全文），但没有「为什么改」。这里**不收原因** —— 收一个发不出去的原因是骗人。
 *    补法是给 `NoticeUpsert` 加 reason。
 *  - `NoticeUpsert` 里没有 level / visible / ends_at / sort_order，它们落库表默认值。
 *  - 「把发布时间改回『立刻发布』」表达不出来：JSON 的「缺席」与「null」在服务端都是 nil，
 *    所以清空发布时间那一格 = 不改它，而不是改回立刻。同一处缺口在 D1 的 expired_at 上也有。
 */
import { useCallback, useState } from 'react';
import { unwrap, unwrapEmpty, unwrapWithMeta } from '@babelplus/shared/api';
import { Badge, Button, Card, CardTitle, EmptyState, formatDateTime } from './_imports.ts';
import { api } from '../lib/api.ts';
import { DangerousAction } from '../components/DangerousAction.tsx';
import {
  CheckboxField,
  DangerOpsNote,
  DataTable,
  ListLoading,
  ModuleHeader,
  PAGE_SIZE,
  Pager,
  QueryErrorState,
  TextAreaField,
  TextField,
  Td,
  Tr,
  extractLinkHosts,
  isKnownMirrorHost,
  useApiQuery,
  useCursorPager,
  useRememberedTotal,
  type Notice,
  type NoticeUpsert,
} from './catalog-common.tsx';
// 日期输入住在优惠码页（那是先用到它的一页）。复制一份的代价是同一个时区转换
// 被写成两种，而时区差一小时的定时发布不会报错，只会让公告晚一小时出现。
import { DateTimeField, isoToLocalInput, localInputToIso } from './CouponsPage.tsx';

type EditorTarget = Notice | 'new' | null;

export default function NoticesPage() {
  const pager = useCursorPager();
  const page = useApiQuery(
    () =>
      unwrapWithMeta(
        api().GET('/api/v1/admin/notices', {
          params: {
            query: {
              limit: PAGE_SIZE,
              ...(pager.cursor === null ? {} : { cursor: pager.cursor }),
              // 总数只在第一页要一次（COUNT(*) 在 db-f1-micro 上是实打实的开销）。
              ...(pager.cursor === null ? { count: true } : {}),
            },
          },
        }),
      ),
    [pager.cursor],
    '公告列表加载失败',
  );
  const total = useRememberedTotal(page.data?.meta);
  const [editor, setEditor] = useState<EditorTarget>(null);

  const reload = page.reload;
  const closeAndReload = useCallback(() => {
    setEditor(null);
    reload();
  }, [reload]);

  const items = page.data?.data ?? [];

  return (
    <>
      <ModuleHeader
        title="公告"
        description="服务变更、维护窗口，以及最重要的：域名广播。"
        priority="P2"
        mobile="M3"
        actions={
          <Button tone="primary" onClick={() => setEditor('new')}>
            新建公告
          </Button>
        }
      />

      <DangerOpsNote codes={['D12']} />

      <Card className="mb-5 border-l-4 border-l-danger">
        <h2 className="text-sm font-semibold text-fg">公告是域名广播位</h2>
        <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
          读到域名公告的人，此刻正处在「面板打不开、正在找备用地址」的状态 ——
          <strong className="font-medium text-fg">这是他戒备心最低的时候</strong>。
          写错一个字母的域名就是把这群人导向一个陌生站点，而他们会照着输入密码。
          所以发布前必须逐条核对正文里每一个链接的目标域名；指错一次的代价不可逆。
        </p>
      </Card>

      {editor !== null ? (
        <div className="mb-5">
          <NoticeEditor
            notice={editor === 'new' ? null : editor}
            onDone={closeAndReload}
            onCancel={() => setEditor(null)}
          />
        </div>
      ) : null}

      {page.state === 'loading' ? <ListLoading /> : null}

      {page.state === 'error' && page.error !== null ? (
        <QueryErrorState error={page.error} what="公告列表" onRetry={page.reload} />
      ) : null}

      {page.state === 'ready' && items.length === 0 && pager.atFirstPage ? (
        <EmptyState
          title="还没有公告"
          description="建议第一条就是域名公告并置顶 —— 用户失联时，公告是他唯一的指路牌。"
          action={
            <Button tone="primary" onClick={() => setEditor('new')}>
              新建公告
            </Button>
          }
        />
      ) : null}

      {page.state === 'ready' && items.length > 0 ? (
        <Card>
          <CardTitle hint="listAdminNotices">公告列表</CardTitle>
          <DataTable head={['标题', '正文里的域名', '置顶', '发布时间', '']}>
            {items.map((n) => {
              const hosts = extractLinkHosts(n.content);
              return (
                <Tr key={n.id}>
                  <Td>
                    <span className="font-medium text-fg">{n.title}</span>
                    <span className="mt-0.5 block font-mono text-xs text-fg-subtle">#{n.id}</span>
                  </Td>
                  <Td>
                    {hosts.length === 0 ? (
                      <span className="text-fg-subtle">无链接</span>
                    ) : (
                      <span className="flex flex-col gap-0.5">
                        {hosts.map((h) => (
                          <HostChip key={h} host={h} />
                        ))}
                      </span>
                    )}
                  </Td>
                  <Td>{n.pinned === true ? <Badge tone="info">置顶</Badge> : null}</Td>
                  <Td className="whitespace-nowrap text-xs">{formatDateTime(n.published_at)}</Td>
                  <Td>
                    <Button onClick={() => setEditor(n)}>编辑 / 删除</Button>
                  </Td>
                </Tr>
              );
            })}
          </DataTable>

          <Pager
            meta={page.data?.meta ?? null}
            pager={pager}
            total={total}
            busy={page.state !== 'ready'}
          />
        </Card>
      ) : null}
    </>
  );
}

/**
 * 一个主机名 + 它在不在运行时配置的镜像列表里。
 *
 * ⚠️ 「在列表里」**不等于这条链接是对的**（路径可能是错的），
 * 「不在列表里」也**不等于错**（新镜像还没进配置）。它只是一条提示，不是判据。
 */
function HostChip({ host }: { host: string }) {
  const known = isKnownMirrorHost(host);
  return (
    <span className="flex flex-wrap items-baseline gap-1.5">
      <code className="font-mono text-sm text-fg">{host}</code>
      {known ? (
        <Badge tone="neutral">在镜像列表里</Badge>
      ) : (
        <Badge tone="warn">不在镜像列表里</Badge>
      )}
    </span>
  );
}

/* ────────────────────────── 表单 ────────────────────────── */

interface NoticeForm {
  title: string;
  content: string;
  pinned: boolean;
  publishedAt: string;
}

function formFromNotice(n: Notice | null): NoticeForm {
  if (n === null) return { title: '', content: '', pinned: false, publishedAt: '' };
  return {
    title: n.title,
    content: n.content,
    pinned: n.pinned === true,
    publishedAt: isoToLocalInput(n.published_at),
  };
}

export type NoticeDraft =
  | { readonly ok: true; readonly value: NoticeUpsert }
  | { readonly ok: false; readonly problem: string };

/** 服务端的上限（`noticeTitleMaxRunes` / `noticeContentMaxRunes`）。按码位数，与 Go 的 rune 同口径。 */
export const NOTICE_TITLE_MAX_RUNES = 200;
export const NOTICE_CONTENT_MAX_RUNES = 50000;

/** 表单校验。导出供单测直接打。 */
export function buildNoticeDraft(form: NoticeForm): NoticeDraft {
  const title = form.title.trim();
  if (title === '') return { ok: false, problem: '公告标题不能为空。' };
  if ([...title].length > NOTICE_TITLE_MAX_RUNES) {
    return { ok: false, problem: `标题最多 ${NOTICE_TITLE_MAX_RUNES} 个字。` };
  }
  if (form.content.trim() === '') return { ok: false, problem: '公告正文不能为空。' };
  if ([...form.content].length > NOTICE_CONTENT_MAX_RUNES) {
    return { ok: false, problem: `正文最多 ${NOTICE_CONTENT_MAX_RUNES} 个字。` };
  }
  const publishedAt = localInputToIso(form.publishedAt);
  if (publishedAt === null) return { ok: false, problem: '发布时间看不懂。' };

  return {
    ok: true,
    value: {
      title,
      content: form.content,
      pinned: form.pinned,
      ...(publishedAt === undefined ? {} : { published_at: publishedAt }),
    },
  };
}

/**
 * 强制预览这一道闸的判据。**纯函数，导出供单测直接打** ——
 * 组件里再写一遍 `if` 的话，测试绿着而按钮的实际行为可以是另一回事。
 *
 * `null` = 可以发布。
 */
export type PreviewBlockReason = 'unverified-hosts' | 'not-acknowledged';

export function previewBlockReason(input: {
  readonly hosts: readonly string[];
  readonly verified: readonly string[];
  readonly acknowledged: boolean;
}): PreviewBlockReason | null {
  if (input.hosts.some((h) => !input.verified.includes(h))) return 'unverified-hosts';
  if (!input.acknowledged) return 'not-acknowledged';
  return null;
}

function NoticeEditor({
  notice,
  onDone,
  onCancel,
}: {
  notice: Notice | null;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [form, setForm] = useState<NoticeForm>(() => formFromNotice(notice));
  // 🔴 已核对的主机名与「已通读」都是**对当前这一份正文**的确认。
  //    正文一变它们就作废，见 setContent。
  const [verified, setVerified] = useState<readonly string[]>([]);
  const [acknowledged, setAcknowledged] = useState(false);

  const draft = buildNoticeDraft(form);
  const hosts = extractLinkHosts(form.content);
  const blocked = previewBlockReason({ hosts, verified, acknowledged });

  const set = <K extends keyof NoticeForm>(key: K, value: NoticeForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  /** 改正文 = 之前的核对全部作废。这是「强制预览」不被绕过的唯一写法。 */
  const setContent = (content: string) => {
    setForm((f) => ({ ...f, content }));
    setVerified([]);
    setAcknowledged(false);
  };

  const toggleHost = (host: string) =>
    setVerified((v) => (v.includes(host) ? v.filter((h) => h !== host) : [...v, host]));

  const problem = !draft.ok
    ? draft.problem
    : blocked === 'unverified-hosts'
      ? '下面预览里的每一个域名都要逐条核对勾选之后才能发布。'
      : blocked === 'not-acknowledged'
        ? '还要确认整篇预览已经读过（D12 要求强制预览）。'
        : undefined;

  return (
    <Card className="border-l-4 border-l-accent">
      <CardTitle hint={notice === null ? 'createAdminNotice（D12）' : 'updateAdminNotice（D12）'}>
        {notice === null ? '新建公告' : `编辑公告 #${notice.id}`}
      </CardTitle>

      <p className="mb-4 rounded-lg border border-warn/30 bg-warn/5 p-3 text-xs leading-relaxed text-fg-muted">
        ⚠️ 这次操作<strong className="font-medium text-fg">不会写下原因</strong>：契约的{' '}
        <code className="font-mono">NoticeUpsert</code> 里没有 reason 字段。
        审计里会有完整的前后像（含正文全文），但没有「为什么改」。
        {notice !== null ? (
          <span className="mt-1 block">
            保存是<strong className="font-medium text-fg">整体覆写</strong>：标题与正文按这个表单原样写入。
            发布时间留空 = 不改（改回「立刻发布」在契约上表达不出来）。
          </span>
        ) : null}
      </p>

      <div className="space-y-4">
        <TextField
          label="标题"
          value={form.title}
          onChange={(v) => set('title', v)}
          placeholder="备用访问地址（请收藏）"
        />

        <TextAreaField
          label="正文（Markdown）"
          value={form.content}
          onChange={setContent}
          rows={10}
          hint={
            <>
              链接请写完整 URL（带 <code className="font-mono">https://</code>）——
              下面的预览按协议头抓链接，不带协议的裸域名<strong className="font-medium text-fg">抓不到</strong>，
              也就不会被要求核对。
              <span className="mt-1 block">
                🔴 改动正文会<strong className="font-medium text-fg">清空下面已勾选的核对</strong>。
              </span>
            </>
          }
        />

        <div className="grid gap-4 sm:grid-cols-2">
          <DateTimeField
            label="发布时间（留空 = 立刻发布）"
            value={form.publishedAt}
            onChange={(v) => set('publishedAt', v)}
            hint="填未来的时间 = 定时发布，在那之前用户看不到。"
          />
          <div className="flex items-end">
            <CheckboxField
              label="置顶"
              checked={form.pinned}
              onChange={(v) => set('pinned', v)}
              hint="域名公告应当一直置顶 —— 用户需要它的那一刻，是没耐心翻列表的。"
            />
          </div>
        </div>

        <NoticePreview
          title={form.title}
          content={form.content}
          hosts={hosts}
          verified={verified}
          onToggleHost={toggleHost}
          acknowledged={acknowledged}
          onAcknowledge={(v) => setAcknowledged(v)}
        />

        {problem !== undefined ? (
          <p className="rounded-lg border border-line bg-surface-alt px-3 py-2 text-sm text-fg-muted">
            还不能提交：{problem}
          </p>
        ) : null}

        <DangerousAction
          code="D12"
          title={notice === null ? '发布公告' : '保存公告改动'}
          submitLabel={notice === null ? '确认发布' : '确认保存'}
          // 🔴 **不传 requireReason**：NoticeUpsert 契约里没有 reason 字段，
          //    收上来的原因发不出去。登记表 D12 那一行也没有「必填原因」。
          disabled={problem !== undefined}
          disabledReason={problem}
          context={
            <>
              <p className="font-medium text-fg">{form.title.trim() || '（无标题）'}</p>
              <p className="mt-1 text-sm text-fg-muted">
                {form.pinned ? '将置顶。' : '不置顶。'}
                {form.publishedAt.trim() === ''
                  ? '发布时间：立刻。'
                  : `发布时间：${form.publishedAt}（本地时区）。`}
              </p>
              {hosts.length === 0 ? (
                <p className="mt-2 text-sm text-fg-muted">正文里没有链接。</p>
              ) : (
                <div className="mt-2">
                  <p className="text-sm font-medium text-fg">
                    这条公告会把用户指向以下 {hosts.length} 个域名：
                  </p>
                  <ul className="mt-1 space-y-0.5">
                    {hosts.map((h) => (
                      <li key={h}>
                        <HostChip host={h} />
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </>
          }
          onSubmit={async () => {
            if (!draft.ok) return;
            if (notice === null) {
              await unwrap(api().POST('/api/v1/admin/notices', { body: draft.value }));
            } else {
              await unwrap(
                api().PATCH('/api/v1/admin/notices/{id}', {
                  params: { path: { id: notice.id } },
                  body: draft.value,
                }),
              );
            }
          }}
          onDone={onDone}
        />

        {notice !== null ? <NoticeDeleteAction notice={notice} onDone={onDone} /> : null}

        <Button tone="ghost" onClick={onCancel}>
          关闭编辑器
        </Button>
      </div>
    </Card>
  );
}

/**
 * D12 的强制预览。
 *
 * 🔴 **正文按原文显示，不渲染 Markdown。** 渲染之后 `[备用地址](https://babe1plus.com)`
 * 显示的是「备用地址」四个字，而要核对的那个 `1` 藏在 href 里 —— 渲染恰恰掩盖了要核对的东西。
 */
function NoticePreview({
  title,
  content,
  hosts,
  verified,
  onToggleHost,
  acknowledged,
  onAcknowledge,
}: {
  title: string;
  content: string;
  hosts: readonly string[];
  verified: readonly string[];
  onToggleHost: (host: string) => void;
  acknowledged: boolean;
  onAcknowledge: (value: boolean) => void;
}) {
  return (
    <section className="rounded-xl border border-danger/30 bg-danger/5 p-3">
      <h3 className="text-sm font-semibold text-fg">强制预览（D12）</h3>
      <p className="mt-1 text-xs leading-relaxed text-fg-muted">
        这一道闸只在前端 —— 服务端不知道你有没有预览过。它挡的不是攻击者，
        是<strong className="font-medium text-fg">正要手滑的自己</strong>。
      </p>

      <div className="mt-3 rounded-lg border border-line bg-surface p-3">
        <p className="text-sm font-semibold text-fg">{title.trim() || '（无标题）'}</p>
        {/* whitespace-pre-wrap + break-words：原文照排，长 URL 不撑出横滚。 */}
        <pre className="mt-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words text-fg-muted">
          {content === '' ? '（正文是空的）' : content}
        </pre>
      </div>

      <div className="mt-3">
        <p className="text-sm font-medium text-fg">
          正文里的链接目标域名（{hosts.length} 个）
        </p>
        {hosts.length === 0 ? (
          <p className="mt-1 text-xs leading-relaxed text-fg-muted">
            没有抓到带协议头的链接。如果这条公告本来就该带备用地址，
            现在<strong className="font-medium text-fg">说明它漏了</strong>；
            也可能是链接写成了不带 <code className="font-mono">https://</code> 的裸域名 —— 那样抓不到。
          </p>
        ) : (
          <ul className="mt-2 space-y-2">
            {hosts.map((h) => (
              <li key={h}>
                <CheckboxField
                  label={<HostChip host={h} />}
                  checked={verified.includes(h)}
                  onChange={() => onToggleHost(h)}
                />
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="mt-3 border-t border-line pt-3">
        <CheckboxField
          label="上面这份预览我已经逐字读过，域名逐个核对过"
          checked={acknowledged}
          onChange={onAcknowledge}
        />
      </div>
    </section>
  );
}

/**
 * 删除公告（`deleteAdminNotice`，D12）。
 *
 * 删一条域名公告，等于把失联用户的指路牌撤掉，所以它与发布同属 D12。
 * ⚠️ 同样不收原因：契约给 DELETE 端点没有请求体。
 */
function NoticeDeleteAction({ notice, onDone }: { notice: Notice; onDone: () => void }) {
  const hosts = extractLinkHosts(notice.content);
  return (
    <DangerousAction
      code="D12"
      title="删除这条公告"
      submitLabel="确认删除"
      context={
        <>
          <p className="font-medium text-fg">将删除：{notice.title}</p>
          {hosts.length > 0 ? (
            <>
              <p className="mt-2 text-sm text-fg-muted">
                🔴 这条公告里有 {hosts.length} 个域名。删掉之后，
                <strong className="font-medium text-fg">正在找备用地址的用户就看不到它们了</strong>。
                确认这些地址已经在别处广播过：
              </p>
              <ul className="mt-1 space-y-0.5">
                {hosts.map((h) => (
                  <li key={h}>
                    <HostChip host={h} />
                  </li>
                ))}
              </ul>
            </>
          ) : (
            <p className="mt-2 text-sm text-fg-muted">这条公告里没有链接。</p>
          )}
          <p className="mt-2 text-xs leading-relaxed text-fg-subtle">
            ⚠️ 这一条不会写下操作原因（契约给 DELETE 端点没有请求体）。
            审计里有被删掉的整条公告（含正文全文），但没有「为什么」。
          </p>
        </>
      }
      onSubmit={async () => {
        await unwrapEmpty(
          api().DELETE('/api/v1/admin/notices/{id}', { params: { path: { id: notice.id } } }),
        );
      }}
      onDone={onDone}
    />
  );
}
