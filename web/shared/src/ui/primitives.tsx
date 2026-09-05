/**
 * 最小组件原语。刻意不引组件库 —— page-inventory §8 明确记着「视觉设计与组件库未定」，
 * 现在装一个 shadcn/Refine 等于替以后的人做了裁决。这里只提供页面骨架必需的几个盒子，
 * 外加「控制台」视觉的四个签名元件：`Eyebrow`（等宽小标）、`Led`（状态点）、`Stat`（大数字）、`Meter`（进度表）。
 *
 * 视觉规范：docs/03-product/design-system.md。色值只来自 tokens.css，本文件不含任何十六进制色。
 * 既有导出的**签名不变**（Card / CardTitle / PageHeader / Button / LinkButton / Badge / PriorityBadge / NotWiredNotice），
 * 页面与测试不需要因为改版而改动。
 */
import type { AnchorHTMLAttributes, ButtonHTMLAttributes, ReactNode } from 'react';

export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ');
}

/* ────────────────────────────── 容器 ────────────────────────────── */

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
        'rounded-lg border border-line bg-surface p-4 shadow-(--bp-shadow) sm:p-5',
        className,
      )}
    >
      {children}
    </As>
  );
}

export function CardTitle({ children, hint }: { children: ReactNode; hint?: ReactNode }) {
  return (
    <header className="mb-3 flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 border-b border-line pb-2.5">
      <h2 className="text-[13px] font-semibold tracking-tight text-fg">{children}</h2>
      {hint ? <p className="font-mono text-[11px] tracking-wide text-fg-subtle">{hint}</p> : null}
    </header>
  );
}

/** 等宽小标。放在标题上方，说明这一页 / 这一块属于哪个区域，是这套视觉的第一识别元素。 */
export function Eyebrow({ children, className }: { children: ReactNode; className?: string }) {
  return <p className={cx('bp-eyebrow', className)}>{children}</p>;
}

export function PageHeader({
  title,
  description,
  actions,
  meta,
  eyebrow,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  meta?: ReactNode;
  eyebrow?: ReactNode;
}) {
  return (
    <header className="mb-5 flex flex-col gap-3 sm:mb-6 sm:flex-row sm:items-end sm:justify-between">
      <div className="min-w-0">
        {eyebrow ? <Eyebrow className="mb-1.5">{eyebrow}</Eyebrow> : null}
        <h1 className="text-[22px] font-semibold tracking-tight text-fg sm:text-[26px]">{title}</h1>
        {description ? <p className="mt-1 max-w-2xl text-sm text-fg-muted">{description}</p> : null}
        {meta ? <div className="mt-2.5 flex flex-wrap gap-1.5">{meta}</div> : null}
      </div>
      {actions ? <div className="flex shrink-0 flex-wrap gap-2">{actions}</div> : null}
    </header>
  );
}

/* ────────────────────────────── 按钮 ────────────────────────────── */

type ButtonTone = 'primary' | 'default' | 'ghost' | 'danger';

const TONE: Record<ButtonTone, string> = {
  primary: 'bg-accent text-accent-fg hover:bg-accent-strong shadow-(--bp-shadow)',
  default: 'border border-line bg-surface text-fg hover:border-line-strong hover:bg-surface-alt',
  ghost: 'text-fg-muted hover:bg-surface-alt hover:text-fg',
  danger: 'border border-danger/40 bg-danger/10 text-danger hover:bg-danger/20',
};

const BUTTON_BASE =
  'inline-flex min-h-11 items-center justify-center gap-2 rounded-md px-4 text-sm font-medium ' +
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

/* ────────────────────────────── 状态 ────────────────────────────── */

type BadgeTone = 'neutral' | 'ok' | 'warn' | 'danger' | 'info';

const BADGE_TONE: Record<BadgeTone, string> = {
  neutral: 'border-line bg-surface-alt text-fg-muted',
  ok: 'border-ok/30 bg-ok/10 text-ok',
  warn: 'border-warn/30 bg-warn/10 text-warn',
  danger: 'border-danger/30 bg-danger/10 text-danger',
  info: 'border-accent/30 bg-accent/10 text-accent',
};

const BADGE_LED: Partial<Record<BadgeTone, LedTone>> = { ok: 'ok', warn: 'warn', danger: 'danger', info: 'accent' };

/** 带 LED 点的状态标：ok / warn / danger / info 自动带一颗对应色的灯，neutral 不带。 */
export function Badge({ tone = 'neutral', children }: { tone?: BadgeTone; children: ReactNode }) {
  const led = BADGE_LED[tone];
  return (
    <span
      className={cx(
        'inline-flex items-center gap-1.5 rounded-sm border px-1.5 py-0.5 text-[11px] font-medium leading-4',
        BADGE_TONE[tone],
      )}
    >
      {led ? <Led tone={led} /> : null}
      {children}
    </span>
  );
}

/** 优先级标记。脚手架里每页顶部挂一个，评审时一眼能看出哪些是 P1。 */
export function PriorityBadge({ level }: { level: 'P1' | 'P2' | 'P3' }) {
  return <Badge tone={level === 'P1' ? 'info' : 'neutral'}>{level}</Badge>;
}

export type LedTone = 'off' | 'ok' | 'warn' | 'danger' | 'accent' | 'wait';

/**
 * 状态点。有颜色就有微弱辉光，`wait` 会呼吸。
 * 它承担的是一眼判断「在线 / 降级 / 失联」——文字是解释，灯才是信号。
 */
export function Led({ tone = 'off', className, label }: { tone?: LedTone; className?: string; label?: string }) {
  return (
    <i
      className={cx('bp-led', tone !== 'off' && `bp-led--${tone}`, className)}
      aria-hidden={label ? undefined : true}
      aria-label={label}
      role={label ? 'img' : undefined}
    />
  );
}

/* ────────────────────────────── 数据 ────────────────────────────── */

/** 大数字：等宽、tabular、单位缩小。仪表盘与看板的主视觉。 */
export function Stat({
  label,
  value,
  unit,
  hint,
  tone,
  className,
}: {
  label: ReactNode;
  value: ReactNode;
  unit?: ReactNode;
  hint?: ReactNode;
  tone?: 'ok' | 'warn' | 'danger';
  className?: string;
}) {
  return (
    <div className={cx('min-w-0', className)}>
      <Eyebrow>{label}</Eyebrow>
      <p
        className={cx(
          'bp-stat__value mt-1',
          tone === 'warn' && 'text-warn',
          tone === 'danger' && 'text-danger',
          tone === 'ok' && 'text-ok',
        )}
      >
        {value}
        {unit ? <span className="bp-stat__unit">{unit}</span> : null}
      </p>
      {hint ? <p className="mt-1 text-xs text-fg-muted">{hint}</p> : null}
    </div>
  );
}

/** 进度表。`percent` 超过 90 自动转警告色，100 转危险色；调用方可用 `tone` 覆盖。 */
export function Meter({
  percent,
  tone,
  label,
  className,
}: {
  percent: number;
  tone?: 'accent' | 'warn' | 'danger';
  label?: string;
  className?: string;
}) {
  const p = Math.max(0, Math.min(100, percent));
  const auto: 'accent' | 'warn' | 'danger' = p >= 100 ? 'danger' : p >= 90 ? 'warn' : 'accent';
  const t = tone ?? auto;
  return (
    <div
      className={cx('bp-track', className)}
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(p)}
    >
      <div className={cx('bp-track__fill', t !== 'accent' && `bp-track__fill--${t}`)} style={{ width: `${p}%` }} />
    </div>
  );
}

/** 等宽片段：IP、字节、ID、时间戳 —— 凡是「数据」都走它。 */
export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cx('font-mono text-[0.94em] tabular-nums', className)}>{children}</span>;
}

/* ────────────────────────────── 脚手架提示 ────────────────────────────── */

/**
 * 「这一页还没接线」的显式声明。
 * 写成组件而不是注释，是因为**评审时看得见**比藏在代码里更诚实 ——
 * 谁打开这一页都立刻知道它是壳，不会误以为功能已经好了。
 */
export function NotWiredNotice({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-md border border-dashed border-line bg-surface-alt/60 p-3 text-xs leading-relaxed text-fg-muted">
      <span className="font-mono text-[11px] tracking-wide text-fg">尚未接线。</span> {children}
    </div>
  );
}
