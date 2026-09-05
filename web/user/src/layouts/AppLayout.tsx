/**
 * 登录后的主布局。**M1 移动优先**（page-inventory §2.3）：
 * 先按 375px 设计，桌面是放大版；<768px 用抽屉导航，≥768px 才出侧栏。
 *
 * 视觉（design-system.md §4.2）：左侧是一条「控制台导轨」——品牌 + 会话状态灯、等宽小标分组、
 * 当前项用左侧竖线 + 强调色标记。**面向用户的导航里不出现 P1/P2 这类内部优先级标记**：
 * 那是排期信息，不是产品信息；后台的导航才保留它们。
 *
 * 页脚的备用域名列表放在布局层而不是页面层 —— ADR 0003 §5 要求它常驻，
 * 放在布局里就不可能被某一页漏掉。
 */
import { useEffect, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router';
import { Button, Eyebrow, Icon, Led, SiteFooter, cx } from '@babelplus/shared/ui';
import { NAV } from './nav.ts';
import { useAuth } from '../lib/auth.tsx';

/**
 * 当前身份 + 登出。
 *
 * 放在布局层的理由和页脚一样：**想漏都漏不掉**。
 * 「登出」在共用电脑上是一个安全动作，不该藏在某一个二级页面里。
 */
function AccountBar({ compact = false }: { compact?: boolean }) {
  const { user, signOut } = useAuth();
  const [pending, setPending] = useState(false);

  async function onSignOut(): Promise<void> {
    setPending(true);
    try {
      await signOut();
    } finally {
      setPending(false);
    }
  }

  return (
    <div
      className={cx(
        'flex items-center gap-2',
        compact ? 'ml-auto' : 'mt-4 rounded-md border border-line bg-surface px-3 py-2',
      )}
    >
      {user ? (
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-fg-subtle" title={user.email}>
          {user.email}
        </span>
      ) : null}
      <Button
        tone="ghost"
        className="min-h-8 shrink-0 px-2 text-xs"
        onClick={() => void onSignOut()}
        disabled={pending}
      >
        {pending ? '正在登出…' : '登出'}
      </Button>
    </div>
  );
}

/** 导航分组：前五个是日常（P1），其余是账号周边。分组只是视觉分隔，顺序照 nav.ts。 */
const PRIMARY = new Set(['/dashboard', '/subscribe', '/plan', '/order', '/ticket']);

function NavList({ onNavigate }: { onNavigate?: () => void }) {
  const primary = NAV.filter((i) => PRIMARY.has(i.to));
  const secondary = NAV.filter((i) => !PRIMARY.has(i.to));
  const item = (it: (typeof NAV)[number]) => (
    <NavLink
      key={it.to}
      to={it.to}
      onClick={onNavigate}
      className={({ isActive }) =>
        cx(
          'relative flex min-h-10 items-center gap-2.5 rounded-md px-3 text-[13px] transition-colors',
          isActive
            ? 'bg-accent-soft font-medium text-accent before:absolute before:top-2 before:bottom-2 before:left-0 before:w-0.5 before:rounded-full before:bg-accent'
            : 'text-fg-muted hover:bg-surface-alt hover:text-fg',
        )
      }
    >
      <it.icon size={15} />
      <span>{it.label}</span>
    </NavLink>
  );
  return (
    <nav className="flex flex-col gap-4">
      <div>
        <Eyebrow className="mb-1.5 px-3">Service</Eyebrow>
        <div className="flex flex-col gap-0.5">{primary.map(item)}</div>
      </div>
      <div>
        <Eyebrow className="mb-1.5 px-3">Account</Eyebrow>
        <div className="flex flex-col gap-0.5">{secondary.map(item)}</div>
      </div>
    </nav>
  );
}

function Brand({ size = 'md' }: { size?: 'sm' | 'md' }) {
  return (
    <div className="flex items-center gap-2.5 px-3">
      <span
        className={cx(
          'grid place-items-center rounded-md bg-accent text-accent-fg',
          size === 'md' ? 'h-7 w-7' : 'h-6 w-6',
        )}
        aria-hidden="true"
      >
        <BrandGlyph size={size === 'md' ? 16 : 14} />
      </span>
      <span className="leading-none">
        <span className={cx('block font-semibold tracking-tight text-fg', size === 'md' ? 'text-[15px]' : 'text-sm')}>
          babel.plus
        </span>
        {size === 'md' ? (
          <span className="mt-1 flex items-center gap-1.5 font-mono text-[10px] tracking-widest text-fg-subtle uppercase">
            <Led tone="ok" /> console
          </span>
        ) : null}
      </span>
    </div>
  );
}

/** 品牌图形：与官网 / 客户端角标同源的经线球。 */
function BrandGlyph({ size }: { size: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" fill="none" stroke="currentColor" aria-hidden="true">
      <circle cx="16" cy="16" r="11" strokeWidth="3" />
      <ellipse cx="16" cy="16" rx="4.6" ry="11" strokeWidth="2" />
      <path d="M3 16h26" strokeWidth="2.4" strokeLinecap="round" />
    </svg>
  );
}

export function AppLayout() {
  const [drawerOpen, setDrawerOpen] = useState(false);
  const { pathname } = useLocation();

  // 路由变化时收起抽屉。不收的话手机上点完导航还挡着内容。
  useEffect(() => setDrawerOpen(false), [pathname]);

  return (
    <div className="min-h-dvh bg-bg">
      {/* 移动端顶栏 */}
      <header className="sticky top-0 z-30 flex min-h-14 items-center gap-2 border-b border-line bg-surface/95 px-2 backdrop-blur md:hidden">
        <Button
          tone="ghost"
          className="min-h-10 px-2"
          aria-label={drawerOpen ? '关闭导航' : '打开导航'}
          aria-expanded={drawerOpen}
          onClick={() => setDrawerOpen((v) => !v)}
        >
          {drawerOpen ? <Icon.Close size={20} /> : <Icon.Menu size={20} />}
        </Button>
        <Brand size="sm" />
        <AccountBar compact />
      </header>

      {drawerOpen ? (
        <div className="border-b border-line bg-surface px-3 py-3 md:hidden">
          <NavList onNavigate={() => setDrawerOpen(false)} />
        </div>
      ) : null}

      <div className="mx-auto flex w-full max-w-6xl gap-8 px-3 py-4 sm:px-5 md:py-6">
        {/* 桌面侧栏 */}
        <aside className="hidden w-56 shrink-0 md:block">
          <div className="sticky top-6">
            <div className="mb-5">
              <Brand />
            </div>
            <NavList />
            <AccountBar />
          </div>
        </aside>

        {/* min-w-0 是必须的：没有它，内部的长链接/表格会把 flex 容器撑出横向滚动，
            直接违反 M1「不得出现横向滚动」。 */}
        <main className="min-w-0 flex-1">
          <Outlet />
        </main>
      </div>

      <SiteFooter />
    </div>
  );
}
