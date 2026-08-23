/**
 * 登录后的主布局。**M1 移动优先**（page-inventory §2.3）：
 * 先按 375px 设计，桌面是放大版；<768px 用抽屉导航，≥768px 才出侧栏。
 *
 * 页脚的备用域名列表放在布局层而不是页面层 —— ADR 0003 §5 要求它常驻，
 * 放在布局里就不可能被某一页漏掉。
 */
import { useEffect, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router';
import { Button, Icon, SiteFooter, cx } from '@babelplus/shared/ui';
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
    <div className={cx('flex items-center gap-2', compact ? 'ml-auto' : 'mt-4 px-3')}>
      {user ? (
        <span className="min-w-0 flex-1 truncate text-xs text-fg-subtle" title={user.email}>
          {user.email}
        </span>
      ) : null}
      <Button
        tone="ghost"
        className="min-h-9 shrink-0 px-2 text-xs"
        onClick={() => void onSignOut()}
        disabled={pending}
      >
        {pending ? '正在登出…' : '登出'}
      </Button>
    </div>
  );
}

function NavList({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <nav className="flex flex-col gap-0.5">
      {NAV.map((item) => (
        <NavLink
          key={item.to}
          to={item.to}
          onClick={onNavigate}
          className={({ isActive }) =>
            cx(
              'flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm transition-colors',
              isActive
                ? 'bg-accent/10 font-medium text-accent'
                : 'text-fg-muted hover:bg-surface-alt hover:text-fg',
            )
          }
        >
          <item.icon size={16} />
          <span>{item.label}</span>
          {item.priority !== 'P1' ? (
            <span className="ml-auto text-[10px] font-medium text-fg-subtle">{item.priority}</span>
          ) : null}
        </NavLink>
      ))}
    </nav>
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
      <header className="sticky top-0 z-30 flex min-h-14 items-center gap-2 border-b border-line bg-surface/95 px-3 backdrop-blur md:hidden">
        <Button
          tone="ghost"
          className="px-2"
          aria-label={drawerOpen ? '关闭导航' : '打开导航'}
          aria-expanded={drawerOpen}
          onClick={() => setDrawerOpen((v) => !v)}
        >
          {drawerOpen ? <Icon.Close size={20} /> : <Icon.Menu size={20} />}
        </Button>
        <span className="font-semibold tracking-tight text-fg">babel.plus</span>
        <AccountBar compact />
      </header>

      {drawerOpen ? (
        <div className="border-b border-line bg-surface px-3 py-2 md:hidden">
          <NavList onNavigate={() => setDrawerOpen(false)} />
        </div>
      ) : null}

      <div className="mx-auto flex w-full max-w-6xl gap-6 px-3 py-4 sm:px-5 md:py-6">
        {/* 桌面侧栏 */}
        <aside className="hidden w-52 shrink-0 md:block">
          <div className="sticky top-6">
            <p className="mb-4 px-3 text-lg font-semibold tracking-tight text-fg">babel.plus</p>
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
