/**
 * 最小组件原语。刻意不引组件库 —— page-inventory §8 明确记着「视觉设计与组件库未定」，
 * 现在装一个 shadcn/Refine 等于替以后的人做了裁决。这里只提供页面骨架必需的几个盒子。
 */
import type { AnchorHTMLAttributes, ButtonHTMLAttributes, ReactNode } from 'react';

export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ');
}

export function Card({
  children,
  className,
  as: As = 'section',
}: {
  children: ReactNode;
  className?: string;
  as?: 'section' | 'div' | 'article';
}) {
  return (
    <As
      className={cx(
        'rounded-xl border border-line bg-surface p-4 sm:p-5',
        className,
      )}
    >
      {children}
    </As>
  );
}

export function CardTitle({ children, hint }: { children: ReactNode; hint?: ReactNode }) {
  return (
    <header className="mb-3 flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
      <h2 className="text-base font-semibold text-fg">{children}</h2>
      {hint ? <p className="text-xs text-fg-muted">{hint}</p> : null}
    </header>
  );
}

export function PageHeader({
  title,
  description,
  actions,
  meta,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  meta?: ReactNode;
}) {
  return (
    <header className="mb-5 flex flex-col gap-3 sm:mb-6 sm:flex-row sm:items-start sm:justify-between">
      <div className="min-w-0">
        <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">{title}</h1>
        {description ? <p className="mt-1 max-w-2xl text-sm text-fg-muted">{description}</p> : null}
        {meta ? <div className="mt-2 flex flex-wrap gap-2">{meta}</div> : null}
      </div>
      {actions ? <div className="flex shrink-0 flex-wrap gap-2">{actions}</div> : null}
    </header>
  );
}

type ButtonTone = 'primary' | 'default' | 'ghost' | 'danger';

const TONE: Record<ButtonTone, string> = {
  primary: 'bg-accent text-accent-fg hover:bg-accent-strong',
  default: 'border border-line bg-surface text-fg hover:bg-surface-alt',
  ghost: 'text-fg-muted hover:bg-surface-alt hover:text-fg',
  danger: 'border border-danger/40 bg-danger/10 text-danger hover:bg-danger/20',
};

const BUTTON_BASE =
  'inline-flex min-h-11 items-center justify-center gap-2 rounded-lg px-4 text-sm font-medium ' +
  'transition-colors disabled:cursor-not-allowed disabled:opacity-50 ' +
  'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent';

/**
 * `min-h-11`（44px）不是随便挑的：用户面板是 M1 移动优先，
 * 44px 是主流移动平台的最小可点区域建议值。桌面上略大一点无所谓，手机上小一点就是误触。
 */
export function Button({
  tone = 'default',
  className,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { tone?: ButtonTone }) {
  return <button type="button" className={cx(BUTTON_BASE, TONE[tone], className)} {...rest} />;
}

export function LinkButton({
  tone = 'default',
  className,
  external,
  ...rest
}: AnchorHTMLAttributes<HTMLAnchorElement> & { tone?: ButtonTone; external?: boolean }) {
  const externalProps = external ? { target: '_blank', rel: 'noreferrer noopener' } : {};
  return <a className={cx(BUTTON_BASE, TONE[tone], className)} {...externalProps} {...rest} />;
}

type BadgeTone = 'neutral' | 'ok' | 'warn' | 'danger' | 'info';

const BADGE_TONE: Record<BadgeTone, string> = {
  neutral: 'border-line bg-surface-alt text-fg-muted',
  ok: 'border-ok/30 bg-ok/10 text-ok',
  warn: 'border-warn/30 bg-warn/10 text-warn',
  danger: 'border-danger/30 bg-danger/10 text-danger',
  info: 'border-accent/30 bg-accent/10 text-accent',
};

export function Badge({ tone = 'neutral', children }: { tone?: BadgeTone; children: ReactNode }) {
  return (
    <span
      className={cx(
        'inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium',
        BADGE_TONE[tone],
      )}
    >
      {children}
    </span>
  );
}

/** 优先级标记。脚手架里每页顶部挂一个，评审时一眼能看出哪些是 P1。 */
export function PriorityBadge({ level }: { level: 'P1' | 'P2' | 'P3' }) {
  return <Badge tone={level === 'P1' ? 'info' : 'neutral'}>{level}</Badge>;
}

/**
 * 「这一页还没接线」的显式声明。
 * 写成组件而不是注释，是因为**评审时看得见**比藏在代码里更诚实 ——
 * 谁打开这一页都立刻知道它是壳，不会误以为功能已经好了。
 */
export function NotWiredNotice({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-line bg-surface-alt/60 p-3 text-xs leading-relaxed text-fg-muted">
      <span className="font-medium text-fg">尚未接线。</span> {children}
    </div>
  );
}
