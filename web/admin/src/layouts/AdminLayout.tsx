/**
 * 后台主布局。**M3 桌面优先**，但工单 / 节点 / 订单三个模块是 M2 ——
 * 手机上要能紧急停用节点、要能回工单。所以导航在小屏上不是「藏起来」而是「收成抽屉」。
 *
 * 顶栏常驻一条身份提示：后台的所有操作都进审计日志，写出来比不写好。
 */
import { useEffect, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router';
import { Button, Icon, cx } from '@babelplus/shared/ui';
import { ADMIN_NAV } from './nav.ts';

function NavList({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <nav className="flex flex-col gap-0.5">
      {ADMIN_NAV.map((item) => (
        <NavLink
          key={item.to}
          to={item.to}
          end={item.to === '/admin'}
          onClick={onNavigate}
          className={({ isActive }) =>
            cx(
              'flex min-h-10 items-center gap-2.5 rounded-lg px-3 text-sm transition-colors',
              isActive
                ? 'bg-accent/10 font-medium text-accent'
                : 'text-fg-muted hover:bg-surface-alt hover:text-fg',
            )
          }
        >
          <item.icon size={15} />
          <span className="truncate">{item.label}</span>
          <span className="ml-auto flex gap-1 text-[10px] font-medium text-fg-subtle">
            {item.mobile === 'M2' ? <span title="手机上必须可操作">M2</span> : null}
            {item.priority !== 'P1' ? <span>{item.priority}</span> : null}
          </span>
        </NavLink>
      ))}
    </nav>
  );
}

export function AdminLayout() {
  const [drawerOpen, setDrawerOpen] = useState(false);
  const { pathname } = useLocation();

  useEffect(() => setDrawerOpen(false), [pathname]);

  return (
    <div className="min-h-dvh bg-bg">
      <header className="sticky top-0 z-30 flex min-h-12 items-center gap-2 border-b border-line bg-surface/95 px-3 backdrop-blur lg:hidden">
        <Button
          tone="ghost"
          className="px-2"
          aria-label={drawerOpen ? '关闭导航' : '打开导航'}
          aria-expanded={drawerOpen}
          onClick={() => setDrawerOpen((v) => !v)}
        >
          {drawerOpen ? <Icon.Close size={20} /> : <Icon.Menu size={20} />}
        </Button>
        <span className="font-semibold tracking-tight text-fg">babel.plus 后台</span>
      </header>

      {drawerOpen ? (
        <div className="border-b border-line bg-surface px-3 py-2 lg:hidden">
          <NavList onNavigate={() => setDrawerOpen(false)} />
        </div>
      ) : null}

      <div className="flex">
        <aside className="hidden w-56 shrink-0 border-r border-line lg:block">
          <div className="sticky top-0 max-h-dvh overflow-y-auto p-3">
            <p className="mb-4 px-3 pt-2 text-base font-semibold tracking-tight text-fg">babel.plus 后台</p>
            <NavList />
            <p className="mt-4 rounded-lg border border-line bg-surface-alt p-2.5 text-[11px] leading-relaxed text-fg-subtle">
              这里的每一次改动都会写进审计日志（谁、何时、改前值、改后值）。
              审计日志<strong className="font-medium text-fg-muted">没有删除入口，也没有编辑入口</strong>。
            </p>
          </div>
        </aside>

        <main className="min-w-0 flex-1 px-3 py-4 sm:px-6 sm:py-6">
          <div className="mx-auto max-w-5xl">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  );
}
