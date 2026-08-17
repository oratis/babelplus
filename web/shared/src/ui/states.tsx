/**
 * 加载 / 空 / 错误三态。**全站唯一实现**，页面不许各写各的。
 * 规则逐条来自 page-inventory §2.2，注释里标了出处，改之前先去改那份文档。
 */
import { useEffect, useState, type ReactNode } from 'react';
import type { ErrorKind } from '../lib/error-kind.ts';
import { ERROR_COPY, OFFLINE_REASSURANCE, runtimeConfig } from '../lib/runtime-config.ts';
import { Button, Card, LinkButton, cx } from './primitives.tsx';
import { Icon } from './icons.tsx';

/* ────────────────────────────── 加载 ────────────────────────────── */

/**
 * 骨架块。**不用居中 spinner** —— §2.2：跨境 API 往返常在数百毫秒到数秒，
 * spinner 在这个时长区间会被用户读成「卡死」。
 */
export function Skeleton({ className }: { className?: string }) {
  return (
    <div
      className={cx('animate-pulse rounded-md bg-skeleton', className)}
      aria-hidden="true"
    />
  );
}

export function SkeletonText({ lines = 3 }: { lines?: number }) {
  return (
    <div className="space-y-2">
      {Array.from({ length: lines }, (_, i) => (
        <Skeleton key={i} className={cx('h-3.5', i === lines - 1 ? 'w-2/5' : 'w-full')} />
      ))}
    </div>
  );
}

export function SkeletonCard({ lines = 3 }: { lines?: number }) {
  return (
    <Card>
      <Skeleton className="mb-3 h-4 w-28" />
      <SkeletonText lines={lines} />
    </Card>
  );
}

/**
 * 慢加载提示条。§2.2：超过 **3 秒**仍无响应，骨架下方插入「跨境链路较慢，正在重试」+ 备用域名。
 * 措辞是有意的 —— SIGMETRICS 2020 说大陆 >70% 的收发对每天有 5 小时以上低于 1 Mbps，
 * **慢是常态，不是异常，不能当故障报**。
 */
export function SlowHint({ afterMs }: { afterMs?: number }) {
  const cfg = runtimeConfig();
  const delay = afterMs ?? cfg.slowHintMs;
  const [shown, setShown] = useState(false);

  useEffect(() => {
    const timer = window.setTimeout(() => setShown(true), delay);
    return () => window.clearTimeout(timer);
  }, [delay]);

  if (!shown) return null;

  return (
    <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn">
      <span>跨境链路较慢，正在重试。可以先去下面的备用域名试试。</span>
      <MirrorDomainInline />
    </div>
  );
}

/**
 * 加载态外壳：骨架 + 3 秒后的慢提示。
 * 骨架必须在 200ms 内出现（§2.2），所以它是**本地渲染的，不等任何请求**。
 */
export function LoadingState({ children, slowHint = true }: { children?: ReactNode; slowHint?: boolean }) {
  return (
    <div aria-busy="true" aria-live="polite">
      {children ?? <SkeletonCard />}
      {slowHint ? <SlowHint /> : null}
    </div>
  );
}

/* ────────────────────────────── 空 ────────────────────────────── */

/**
 * 空态。**`action` 是必填 prop，不是可选** —— §2.2：
 * 「必须给出下一步动作按钮，禁止只显示『暂无数据』」。
 * 类型系统在这里替我们守住这条规则。
 */
export function EmptyState({
  title,
  description,
  action,
  secondary,
}: {
  title: string;
  description?: ReactNode;
  action: ReactNode;
  secondary?: ReactNode;
}) {
  return (
    <Card className="text-center">
      <h3 className="text-base font-semibold text-fg">{title}</h3>
      {description ? (
        <p className="mx-auto mt-1.5 max-w-md text-sm leading-relaxed text-fg-muted">{description}</p>
      ) : null}
      <div className="mt-4 flex flex-wrap items-center justify-center gap-2">{action}</div>
      {secondary ? <div className="mt-3 text-xs text-fg-muted">{secondary}</div> : null}
    </Card>
  );
}

/* ────────────────────────────── 错误 ────────────────────────────── */

export function ErrorState({
  kind,
  title,
  description,
  requestId,
  onRetry,
  extra,
}: {
  kind: ErrorKind;
  title?: string;
  description?: ReactNode;
  /** `meta.request_id`。用户报障时直接贴这个串，所以一定要显示出来。 */
  requestId?: string;
  onRetry?: () => void;
  extra?: ReactNode;
}) {
  const cfg = runtimeConfig();
  const copy = ERROR_COPY[kind];

  return (
    <Card className={cx('border-l-4', kind === 'server' || kind === 'offline' ? 'border-l-warn' : 'border-l-danger')}>
      <div className="flex items-start gap-3">
        <span className="mt-0.5 shrink-0 text-fg-muted">
          {kind === 'offline' ? <Icon.Offline size={18} /> : <Icon.Alert size={18} />}
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-base font-semibold text-fg">{title ?? copy.title}</h3>
          <p className="mt-1 text-sm leading-relaxed text-fg-muted">{description ?? copy.description}</p>

          {/* 控制面故障不得被用户理解为数据面故障（system-design §1）。 */}
          {(kind === 'offline' || kind === 'server') && (
            <p className="mt-2 rounded-lg bg-ok/10 px-3 py-2 text-sm text-ok">{OFFLINE_REASSURANCE}</p>
          )}

          <div className="mt-3 flex flex-wrap gap-2">
            {onRetry ? (
              <Button tone="primary" onClick={onRetry}>
                重试
              </Button>
            ) : null}

            {/* 5xx 引导到状态页，**不要求用户提工单** —— 服务端故障时工单洪峰毫无价值。 */}
            {kind === 'server' && cfg.statusUrl ? (
              <LinkButton href={cfg.statusUrl} external>
                查看状态页 <Icon.External size={14} />
              </LinkButton>
            ) : null}

            {kind === 'unauthorized' ? <LinkButton href="/auth/login">去登录</LinkButton> : null}

            {kind === 'offline' && cfg.checkUrl ? (
              <LinkButton href={cfg.checkUrl} external>
                网络诊断 <Icon.External size={14} />
              </LinkButton>
            ) : null}

            {extra}
          </div>

          {/* 网络不可达 → 展示**静态内嵌**的备用域名列表（不经 API）。 */}
          {kind === 'offline' ? <MirrorDomainList className="mt-4" /> : null}

          {requestId ? (
            <p className="mt-3 font-mono text-xs text-fg-subtle">
              请求号 {requestId} —— 提工单时贴上它，我们能直接定位。
            </p>
          ) : null}
        </div>
      </div>
    </Card>
  );
}

/* ─────────────────────── 备用域名（恢复路径） ─────────────────────── */

/**
 * 备用域名列表。数据来自**运行时配置**（`window.__BP_RUNTIME_CONFIG__`），不硬编码 ——
 * ADR 0003 §5：「部署流水线支持一键新增镜像域名」，硬编码等于每加一个镜像都要重新构建三套前端。
 *
 * 未配置时显示的是「还没配」而不是假域名。把用户导向一个不存在的地址比什么都不显示更糟。
 */
export function MirrorDomainList({ className }: { className?: string }) {
  const cfg = runtimeConfig();

  if (cfg.mirrorDomains.length === 0) {
    return (
      <div className={cx('rounded-lg border border-dashed border-line p-3 text-xs text-fg-muted', className)}>
        <span className="font-medium text-fg">备用域名尚未配置。</span>{' '}
        部署时覆盖 <code className="font-mono">/runtime-config.js</code> 的{' '}
        <code className="font-mono">mirrorDomains</code> 即可，不需要重新构建。
        {cfg.supportEmail ? <> 现在失联的话，请查收 {cfg.supportEmail} 的来信。</> : null}
      </div>
    );
  }

  return (
    <div className={cx('rounded-lg border border-line bg-surface-alt p-3', className)}>
      <p className="mb-2 text-xs font-medium text-fg">备用域名（任选其一，内容相同）</p>
      <ul className="space-y-1.5">
        {cfg.mirrorDomains.map((m) => (
          <li key={m.url} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm">
            <a
              href={m.url}
              className="break-all font-mono text-accent underline-offset-2 hover:underline"
              rel="noreferrer noopener"
            >
              {m.url}
            </a>
            <span className="text-xs text-fg-muted">{m.label}</span>
            {m.note ? <span className="text-xs text-fg-subtle">{m.note}</span> : null}
          </li>
        ))}
      </ul>
      {cfg.supportEmail ? (
        <p className="mt-2 text-xs text-fg-muted">
          全都打不开时请查收 <span className="font-mono">{cfg.supportEmail}</span> 的来信 ——
          邮件是唯一的失联恢复通道（ADR 0002）。
        </p>
      ) : null}
    </div>
  );
}

/** 内联紧凑版，给慢提示条用。 */
export function MirrorDomainInline() {
  const cfg = runtimeConfig();
  const first = cfg.mirrorDomains[0];
  if (!first) return null;
  return (
    <a href={first.url} className="font-mono underline underline-offset-2" rel="noreferrer noopener">
      {first.url}
    </a>
  );
}

/* ───────────────────────── 三态分发 ───────────────────────── */

export type ShellState = 'loading' | 'empty' | 'error' | 'ready';

/**
 * 三态分发器。页面统一写成
 * `<StateSwitch state={s} loading={…} empty={…} error={…}>{正文}</StateSwitch>`，
 * 这样「有没有认真做空态」在 code review 里是**结构上可见**的，不用逐页去猜。
 */
export function StateSwitch({
  state,
  errorKind = 'server',
  loading,
  empty,
  error,
  children,
}: {
  state: ShellState;
  errorKind?: ErrorKind;
  loading?: ReactNode;
  empty: ReactNode;
  error?: ReactNode;
  children: ReactNode;
}) {
  if (state === 'loading') return <>{loading ?? <LoadingState />}</>;
  if (state === 'empty') return <>{empty}</>;
  if (state === 'error') return <>{error ?? <ErrorState kind={errorKind} />}</>;
  return <>{children}</>;
}
