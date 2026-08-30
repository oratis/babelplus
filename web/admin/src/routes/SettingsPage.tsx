/**
 * 模块 16 · 系统配置 `/admin/settings` —— P2 / M3。KV 表 + 热生效。
 *
 * # 三条把这一页的形状定死的服务端事实
 *
 * 1. 🔴 **写侧是纯 UPDATE，不是 UPSERT** —— 通过 API **建不出新键**
 *    （`updateAdminSettings` 的注释：`settings.key` 是自由文本主键、没有白名单表，
 *    写成 UPSERT 的话一次手滑的 `{"expire_remid": …}` 会静默新建一个永远读不到的键，
 *    而真正想改的那个原封不动 —— 页面显示「已保存」，行为没有任何变化）。
 *    所以这一页**没有「添加配置项」按钮**：新键要走迁移，而迁移是要被 review 的。
 *
 * 2. 🔴 **凭据不在这张表里，将来也不许放进来。** `ListAdminSettings` 不做任何过滤，
 *    任何写进 settings 的东西都会原样出现在管理面响应体里 ——
 *    也就是浏览器缓存与 devtools 里。这条规则不是靠过滤保证的，是靠**不写进去**保证的。
 *
 * 3. **PATCH 成功后返回的是全量新配置**，所以保存后走 `query.replace(...)` 就地替换，
 *    不再发一次 GET：重发会把页面打回骨架屏，而操作者刚点完保存，
 *    眼前的配置突然消失，第一反应是「是不是没存上」。
 *
 * # D13 要求的 diff 在哪
 *
 * page-inventory §4.4 给 D13 的额外要求是「展示 diff」。这里的 diff 是**逐键的新旧值**，
 * 显示在确认面板里（`DangerousAction` 的 `context`），不是「改了 5 个键」这种摘要 ——
 * 配置回滚时唯一能用的东西就是那份逐键旧值。
 *
 * ⚠️ **原因是必填的**，虽然 `lib/danger.ts` 的 D13 那一行没写 `reason`：
 * `SettingsPatchRequest.reason` 在契约里是 required，服务端 `catalogCheckReason`
 * 还要求至少 8 个码位。所以这里显式传 `requireReason`。
 * （组件的覆盖规则是「只能加不能减」，加是允许的。）
 */
import { useMemo, useState } from 'react';
import { PageHeader } from '@babelplus/shared/ui';
import type { components } from '@babelplus/shared/api';
import { unwrap } from '@babelplus/shared/api';
import { Badge, Button, Card, CardTitle, EmptyState, Icon, LinkButton, cx } from './_imports.ts';
import { DangerousAction, type DangerousActionValues } from '../components/DangerousAction.tsx';
import {
  CaveatNotice,
  ListSkeleton,
  QueryErrorState,
  formatJsonValue,
  useOpsQuery,
} from './ops-common.tsx';
import { api } from '../lib/api.ts';

type SettingsMap = components['schemas']['SettingsMap'];

function loadSettings(): Promise<SettingsMap> {
  return unwrap(api().GET('/api/v1/admin/settings'));
}

function patchSettings(
  values: SettingsMap,
  reason: string,
  totp: string | undefined,
): Promise<SettingsMap> {
  // TOTP 走请求头不是 body（§6.2 L3）。D13 目前不在 step-up 名单里，
  // 所以这里通常是 undefined —— 保留这一路是为了名单变化时不用改调用点。
  const headers = totp ? { 'X-TOTP-Code': totp } : undefined;
  return unwrap(api().PATCH('/api/v1/admin/settings', { body: { values, reason }, headers }));
}

/** 一个键的当前编辑态。`text` 是文本框里的原文，`error` 是它解不出 JSON 时的说明。 */
interface Edit {
  readonly text: string;
  readonly parsed?: unknown;
  readonly error?: string;
}

/** JSON 值 → 文本框里的原文。对象缩进两格，标量一行。 */
function toText(value: unknown): string {
  return formatJsonValue(value);
}

/**
 * 判断「改了没有」。用规范化后的 JSON 串比 —— 对象键顺序不同会被当成有改动，
 * 而那只会多提交一个值相同的键（服务端照写，审计里 before/after 相等），
 * **不会漏掉真正的改动**。反过来（漏判）才是危险的。
 */
function sameValue(a: unknown, b: unknown): boolean {
  try {
    return JSON.stringify(a) === JSON.stringify(b);
  } catch {
    return false;
  }
}

/** JSON 类型标签。让人一眼看出这个键该填 `"true"` 还是 `true`。 */
function jsonKind(value: unknown): string {
  if (value === null) return 'null';
  if (Array.isArray(value)) return 'array';
  return typeof value;
}

/** 按第一个 `.` 之前的段分组。没有点的键归「未分组」。 */
function groupOf(key: string): string {
  const i = key.indexOf('.');
  return i > 0 ? key.slice(0, i) : '未分组';
}

export default function SettingsPage() {
  const query = useOpsQuery<SettingsMap>(loadSettings, [], '系统配置加载失败');
  const settings = query.data;
  const [edits, setEdits] = useState<Record<string, Edit>>({});

  const keys = useMemo(() => (settings ? Object.keys(settings).sort() : []), [settings]);

  const groups = useMemo(() => {
    const acc = new Map<string, string[]>();
    for (const k of keys) {
      const g = groupOf(k);
      acc.set(g, [...(acc.get(g) ?? []), k]);
    }
    return [...acc.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [keys]);

  /** 改动了、且解得出 JSON 的键。这是真正要发出去的那一份。 */
  const changes = useMemo(() => {
    if (!settings) return [] as Array<{ key: string; before: unknown; after: unknown }>;
    const out: Array<{ key: string; before: unknown; after: unknown }> = [];
    for (const [key, edit] of Object.entries(edits)) {
      if (edit.error !== undefined) continue;
      if (!(key in settings)) continue;
      const before = settings[key];
      if (sameValue(before, edit.parsed)) continue;
      out.push({ key, before, after: edit.parsed });
    }
    return out.sort((a, b) => a.key.localeCompare(b.key));
  }, [edits, settings]);

  const broken = Object.entries(edits).filter(([, e]) => e.error !== undefined);

  function editKey(key: string, text: string) {
    setEdits((prev) => {
      const next = { ...prev };
      try {
        next[key] = { text, parsed: JSON.parse(text) as unknown };
      } catch (cause) {
        // 🔴 这不是「校验」，是「组不出请求体」：`values` 里要放的是 JSON 值，
        //    一段解不出来的文本没有对应的值可发。文案必须说清这一点，
        //    否则读的人会以为前端在替服务端把关（§6.2：把关全在服务端）。
        next[key] = { text, error: cause instanceof Error ? cause.message : '不是合法 JSON' };
      }
      return next;
    });
  }

  function revertKey(key: string) {
    setEdits((prev) => {
      const next = { ...prev };
      delete next[key];
      return next;
    });
  }

  return (
    <>
      <PageHeader
        title="系统配置"
        description="注册开关、邀请策略、SLA、通知开关。改动全局立即生效。"
        meta={
          <>
            <Badge tone="neutral">P2</Badge>
            <Badge tone="neutral">M3 · 桌面优先，手机上可读即可</Badge>
            <Badge tone="danger">D13 改系统配置</Badge>
          </>
        }
        actions={
          Object.keys(edits).length > 0 ? (
            <Button onClick={() => setEdits({})}>放弃全部改动</Button>
          ) : undefined
        }
      />

      <div className="space-y-4">
        {query.state === 'loading' ? <ListSkeleton rows={6} /> : null}

        {query.state === 'error' && query.error ? (
          <QueryErrorState
            error={query.error}
            what="系统配置"
            why={
              <>
                配置读写挂在 <code className="font-mono">GET/PATCH /admin/settings</code> 上。
              </>
            }
            onRetry={query.reload}
          />
        ) : null}

        {query.state === 'ready' && keys.length === 0 ? (
          <EmptyState
            title="配置表是空的"
            description="所有键都在用代码里的默认值。首次部署时这是正常的 —— 迁移 0011 只建了 settings 表，没有塞任何种子值。"
            action={
              <LinkButton tone="primary" href="/admin">
                回到看板 <Icon.ArrowRight size={14} />
              </LinkButton>
            }
            secondary="这一页也不能在这里新建键：写侧是纯 UPDATE，建键必须走迁移。"
          />
        ) : null}

        {query.state === 'ready' && keys.length > 0 && settings ? (
          <>
            {groups.map(([group, groupKeys]) => (
              <Card key={group}>
                <CardTitle hint={`${groupKeys.length} 项`}>{group}</CardTitle>
                <div className="space-y-4">
                  {groupKeys.map((key) => {
                    const edit = edits[key];
                    const current = settings[key];
                    const text = edit?.text ?? toText(current);
                    const dirty = edit !== undefined && !sameValue(current, edit.parsed);
                    return (
                      <div key={key} className="min-w-0">
                        <div className="mb-1.5 flex flex-wrap items-baseline gap-2">
                          <label htmlFor={`setting-${key}`} className="font-mono text-sm font-medium text-fg">
                            {key}
                          </label>
                          <Badge tone="neutral">{jsonKind(current)}</Badge>
                          {edit?.error !== undefined ? <Badge tone="danger">不是合法 JSON</Badge> : null}
                          {dirty && edit?.error === undefined ? <Badge tone="warn">已改动</Badge> : null}
                          {edit !== undefined ? (
                            <button
                              type="button"
                              className="text-xs text-accent hover:underline"
                              onClick={() => revertKey(key)}
                            >
                              还原
                            </button>
                          ) : null}
                        </div>
                        <textarea
                          id={`setting-${key}`}
                          name={key}
                          rows={Math.min(10, Math.max(2, text.split('\n').length))}
                          value={text}
                          spellCheck={false}
                          onChange={(e) => editKey(key, e.target.value)}
                          className={cx(
                            'w-full rounded-lg border bg-surface px-3 py-2 font-mono text-sm leading-relaxed text-fg',
                            'focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent',
                            edit?.error !== undefined ? 'border-danger' : 'border-line',
                          )}
                        />
                        {edit?.error !== undefined ? (
                          <p className="mt-1 text-xs leading-relaxed text-danger">
                            这段文本解不出 JSON（{edit.error}），因此<strong className="font-medium">组不出请求体</strong>里的值 ——
                            没法把它发给服务端。这不是前端在替服务端校验，只是没有值可发。
                          </p>
                        ) : (
                          <p className="mt-1 text-xs leading-relaxed text-fg-subtle">
                            填 JSON 值本身：字符串要带引号（<code className="font-mono">&quot;on&quot;</code>），
                            布尔写 <code className="font-mono">true</code> / <code className="font-mono">false</code>。
                          </p>
                        )}
                      </div>
                    );
                  })}
                </div>
              </Card>
            ))}

            {broken.length > 0 ? (
              <CaveatNotice>
                有 {broken.length} 个键的文本解不出 JSON（{broken.map(([k]) => k).join('、')}）。
                在改好之前它们<strong className="font-medium text-fg">不会被提交</strong>，
                其余改动仍然可以保存。
              </CaveatNotice>
            ) : null}

            <Card>
              <CardTitle hint="D13 · 全局立即生效">保存改动</CardTitle>
              <DangerousAction
                code="D13"
                title={`保存 ${changes.length} 项配置改动`}
                submitLabel="写入配置"
                requireReason
                permissionName="admin.settings.write"
                disabled={changes.length === 0}
                disabledReason="还没有任何可提交的改动（改动了但解不出 JSON 的键不算）。"
                context={<SettingsDiff changes={changes} />}
                onSubmit={async (values: DangerousActionValues) => {
                  const payload: Record<string, unknown> = {};
                  for (const c of changes) payload[c.key] = c.after;
                  const next = await patchSettings(payload, values.reason ?? '', values.totp);
                  // 服务端回的是全量新配置，就地替换即可（见文件头第 3 条）。
                  query.replace(next);
                  setEdits({});
                }}
              />
              <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
                只有上面列出的键会被发出去。服务端是纯 UPDATE：不认识的键会让整个请求 422 并列出是哪几个，
                <strong className="font-medium text-fg-muted">不会静默新建</strong>。
              </p>
            </Card>
          </>
        ) : null}

        <Card>
          <CardTitle>这一页做不到的两件事</CardTitle>
          <ul className="space-y-2 text-sm leading-relaxed text-fg-muted">
            <li>
              · <strong className="font-medium text-fg">「当前值 vs 默认值」对照做不了。</strong>
              契约的 <code className="font-mono">SettingsMap</code> 只有 key → value。
              库里其实还有 <code className="font-mono">description</code> /
              <code className="font-mono"> updated_by</code> / <code className="font-mono">updated_at</code>
              三列，但响应里没有它们的位置 —— 于是排障时最常问的「这个值是谁改的」，
              只能去审计日志里按 <code className="font-mono">D13.</code> 前缀查。
            </li>
            <li>
              · <strong className="font-medium text-fg">新建配置项做不到，这是刻意的。</strong>
              一个新键（尤其是凭据类的键）必须走迁移，而迁移会被 review。
            </li>
          </ul>
        </Card>
      </div>
    </>
  );
}

/** D13 的硬要求：保存前展示 diff。**逐键**新旧值，不是「改了几个键」。 */
function SettingsDiff({ changes }: { changes: Array<{ key: string; before: unknown; after: unknown }> }) {
  if (changes.length === 0) {
    return <p>没有要提交的改动。</p>;
  }
  return (
    <div className="space-y-3">
      <p>
        这次会写入 <strong className="font-medium">{changes.length}</strong> 个键，
        <strong className="font-medium">全局立即生效</strong>：
      </p>
      {changes.map((c) => (
        <div key={c.key} className="min-w-0">
          <p className="font-mono text-xs font-medium text-fg">{c.key}</p>
          <div className="mt-1 grid gap-2 sm:grid-cols-2">
            <pre className="overflow-auto rounded border border-line bg-surface px-2 py-1 font-mono text-xs text-fg-muted">
              {formatJsonValue(c.before)}
            </pre>
            <pre className="overflow-auto rounded border border-accent/40 bg-surface px-2 py-1 font-mono text-xs text-fg">
              {formatJsonValue(c.after)}
            </pre>
          </div>
        </div>
      ))}
    </div>
  );
}
