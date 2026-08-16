/**
 * 后台每个模块的统一外壳。与用户面板的 `RouteScaffold` 是两套 —— 它们的约束不一样：
 * 后台要显示「能看什么 / 能改什么 / 危险在哪」，用户面板不需要。
 */
import type { ReactNode } from 'react';
import {
  Badge,
  ErrorState,
  LoadingState,
  NotWiredNotice,
  PageHeader,
  StateSwitch,
} from '@babelplus/shared/ui';
import type { ShellState } from '@babelplus/shared';
import { useShellState } from '../lib/shell.ts';
import { dangerOps } from '../lib/danger.ts';

/** 移动端要求档位（page-inventory §2.3）。后台大多是 M3，工单/节点/订单是 M2。 */
export type MobileTier = 'M2' | 'M3';

export interface ModuleScaffoldProps {
  title: string;
  description: ReactNode;
  priority: 'P1' | 'P2' | 'P3';
  mobile: MobileTier;
  endpoints: readonly string[];
  /** 本模块涉及的危险操作编号（page-inventory §4.4）。 */
  danger?: readonly string[];
  todo: ReactNode;
  empty: ReactNode;
  children: ReactNode;
  actions?: ReactNode;
  defaultState?: ShellState;
}

const MOBILE_COPY: Record<MobileTier, string> = {
  M2: 'M2 · 手机上核心操作必须能完成，表格卡片化降级',
  M3: 'M3 · 桌面优先，手机上可读即可',
};

export function ModuleScaffold({
  title,
  description,
  priority,
  mobile,
  endpoints,
  danger = [],
  todo,
  empty,
  children,
  actions,
  defaultState = 'ready',
}: ModuleScaffoldProps) {
  const { state, errorKind } = useShellState(defaultState);
  const ops = dangerOps(danger);

  return (
    <>
      <PageHeader
        title={title}
        description={description}
        actions={actions}
        meta={
          <>
            <Badge tone={priority === 'P1' ? 'info' : 'neutral'}>{priority}</Badge>
            <Badge tone={mobile === 'M2' ? 'warn' : 'neutral'}>{MOBILE_COPY[mobile]}</Badge>
          </>
        }
      />

      <div className="mb-5 space-y-3">
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
        </NotWiredNotice>

        {ops.length > 0 ? <DangerChecklist /> : null}
        {ops.length > 0 ? (
          <ul className="space-y-2">
            {ops.map((op) => (
              <li
                key={op.code}
                className="rounded-lg border border-danger/30 bg-danger/5 p-3 text-xs leading-relaxed"
              >
                <div className="flex flex-wrap items-baseline gap-2">
                  <span className="rounded bg-danger/15 px-1.5 py-0.5 font-mono font-semibold text-danger">
                    {op.code}
                  </span>
                  <span className="font-medium text-fg">{op.title}</span>
                </div>
                <p className="mt-1 text-fg-muted">危害：{op.harm}</p>
                <p className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-fg-subtle">
                  <span>审计（改前值 / 改后值）</span>
                  {op.reason ? <span>必填原因</span> : null}
                  {op.confirmString ? <span>🔒 输入{op.confirmString}</span> : null}
                  {op.notify ? <span>📧 通知受影响用户</span> : null}
                  {op.separatePerm ? <span>独立权限位（默认不授予）</span> : null}
                </p>
                {op.extra ? <p className="mt-1 text-fg-muted">额外：{op.extra}</p> : null}
              </li>
            ))}
          </ul>
        ) : null}
      </div>

      <StateSwitch
        state={state}
        errorKind={errorKind}
        loading={<LoadingState slowHint={false} />}
        empty={empty}
        error={<ErrorState kind={errorKind} />}
      >
        {children}
      </StateSwitch>
    </>
  );
}

function DangerChecklist() {
  return (
    <p className="text-xs text-fg-subtle">
      本模块含危险操作。每一条在接线前都必须先有确认组件与审计写入，
      <strong className="font-medium text-fg-muted">不接受「先做功能，审计以后补」</strong> ——
      补的那天就是需要查审计的那天。
    </p>
  );
}

/** 版面占位块。 */
export function LayoutSlot({ label, hint }: { label: string; hint?: string }) {
  return (
    <div className="flex min-h-24 flex-col items-center justify-center gap-1 rounded-xl border border-dashed border-line bg-surface-alt/40 p-4 text-center">
      <span className="text-sm font-medium text-fg-muted">{label}</span>
      {hint ? <span className="max-w-2xl text-xs leading-relaxed text-fg-subtle">{hint}</span> : null}
    </div>
  );
}
