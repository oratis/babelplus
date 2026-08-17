/**
 * 每一页的统一外壳：标题 + 优先级 + 待接线声明 + 三态。
 *
 * 抽出来是为了让「三态有没有认真做」在 code review 里**结构上可见** ——
 * `empty` 是必填 prop，漏了空态编译就不过。page-inventory §2.2 那条
 * 「空态必须给出下一步动作按钮，禁止只显示『暂无数据』」由此变成类型约束而不是口头约定。
 */
import type { ReactNode } from 'react';
import {
  ErrorState,
  LoadingState,
  NotWiredNotice,
  PageHeader,
  PriorityBadge,
  StateSwitch,
} from '@babelplus/shared/ui';
import type { ShellState } from '@babelplus/shared';
import { useShellState } from '../lib/shell.ts';

export interface RouteScaffoldProps {
  title: string;
  description: ReactNode;
  priority: 'P1' | 'P2' | 'P3';
  /** 契约里对应的 operationId（openapi.yaml 是全仓事实源），接线时照这个找。 */
  endpoints: readonly string[];
  /** 这一页还差什么。写给下一个接手的人看。 */
  todo: ReactNode;
  /** 空态。**必填** —— 见文件头注释。 */
  empty: ReactNode;
  /** 就绪态正文（骨架里通常是版面占位）。 */
  children: ReactNode;
  loading?: ReactNode;
  error?: ReactNode;
  actions?: ReactNode;
  meta?: ReactNode;
  /** 不带 `?state=` 时的默认态。 */
  defaultState?: ShellState;
}

export function RouteScaffold({
  title,
  description,
  priority,
  endpoints,
  todo,
  empty,
  children,
  loading,
  error,
  actions,
  meta,
  defaultState = 'ready',
}: RouteScaffoldProps) {
  const { state, errorKind } = useShellState(defaultState);

  return (
    <>
      <PageHeader
        title={title}
        description={description}
        actions={actions}
        meta={
          <>
            <PriorityBadge level={priority} />
            {meta}
          </>
        }
      />

      <div className="mb-5">
        <NotWiredNotice>
          {todo}
          {endpoints.length > 0 ? (
            <span className="mt-1 block">
              契约：
              {endpoints.map((op, i) => (
                <span key={op}>
                  {i > 0 ? '、' : ''}
                  <code className="font-mono text-fg">{op}</code>
                </span>
              ))}
            </span>
          ) : null}
          <span className="mt-1 block text-fg-subtle">
            三态自查：在地址栏加 <code className="font-mono">?state=loading</code> /{' '}
            <code className="font-mono">?state=empty</code> /{' '}
            <code className="font-mono">?state=error&amp;error=offline</code>
          </span>
        </NotWiredNotice>
      </div>

      <StateSwitch
        state={state}
        errorKind={errorKind}
        loading={loading ?? <LoadingState />}
        empty={empty}
        // 页面自定义的错误态只覆盖「服务端故障」这一类（各页对 5xx 有各自的说法）。
        // 401 / 403 / 网络不可达三类的处置是全站统一的 —— 尤其是网络不可达，
        // 它必须展示静态内嵌的备用域名列表，任何一页都不该自己发挥。
        error={
          error && errorKind === 'server' ? (
            error
          ) : (
            <ErrorState kind={errorKind} requestId="（接线后填 meta.request_id）" />
          )
        }
      >
        {children}
      </StateSwitch>
    </>
  );
}

/** 就绪态里的版面占位块。**不是假数据** —— 只画出未来内容的位置和形状。 */
export function LayoutSlot({ label, hint, className }: { label: string; hint?: string; className?: string }) {
  return (
    <div
      className={
        'flex min-h-24 flex-col items-center justify-center gap-1 rounded-xl border border-dashed border-line ' +
        'bg-surface-alt/40 p-4 text-center ' +
        (className ?? '')
      }
    >
      <span className="text-sm font-medium text-fg-muted">{label}</span>
      {hint ? <span className="max-w-md text-xs leading-relaxed text-fg-subtle">{hint}</span> : null}
    </div>
  );
}
