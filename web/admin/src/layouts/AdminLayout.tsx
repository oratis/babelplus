/**
 * 后台主布局。**M3 桌面优先**，但工单 / 节点 / 订单三个模块是 M2 ——
 * 手机上要能紧急停用节点、要能回工单。所以导航在小屏上不是「藏起来」而是「收成抽屉」。
 *
 * 视觉（design-system.md §4.3）：全宽「控制台」，左侧导轨按四个职能分组（运营 / 目录 / 基础设施 / 系统），
 * 导航项右侧保留 M2 / P2 这类内部标记 —— 后台的读者就是运维，这些是给他们看的。
 * 顶栏常驻一条身份提示：后台的所有操作都进审计日志，写出来比不写好。
 */
import { useEffect, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router';
import { Button, Eyebrow, Icon, Led, cx } from '@babelplus/shared/ui';
import { ADMIN_NAV, type AdminNavGroup } from './nav.ts';

const GROUP_LABEL: Record<AdminNavGroup, string> = {
  ops: 'Ops · 运营',
  catalog: 'Catalog · 目录',
  infra: 'Infra · 基础设施',
  system: 'System · 系统',
};
const GROUP_ORDER: AdminNavGroup[] = ['ops', 'catalog', 'infra', 'system'];

function NavList({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <nav className="flex flex-col gap-4">
      {GROUP_ORDER.map((group) => {
        const items = ADMIN_NAV.filter((i) => i.group === group);
        if (items.length === 0) return null;
        return (
          <div key={group}>
            <Eyebrow className="mb-1.5 px-3">{GROUP_LABEL[group]}</Eyebrow>
            <div className="flex flex-col gap-0.5">
              {items.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.to === '/admin'}
                  onClick={onNavigate}
                  className={({ isActive }) =>
                    cx(
                      'relative flex min-h-9 items-center gap-2.5 rounded-md px-3 text-[13px] transition-colors',
                      isActive
                        ? 'bg-accent-soft font-medium text-accent before:absolute before:top-1.5 before:bottom-1.5 before:left-0 before:w-0.5 before:rounded-full before:bg-accent'
                        : 'text-fg-muted hover:bg-surface-alt hover:text-fg',
                    )
                  }
                >
                  <item.icon size={14} />
                  <span className="truncate">{item.label}</span>
                  <span className="ml-auto flex gap-1 font-mono text-[10px] tracking-wide text-fg-subtle">
                    {item.mobile === 'M2' ? <span title="手机上必须可操作">M2</span> : null}
                    {item.priority !== 'P1' ? <span>{item.priority}</span> : null}
                  </span>
                </NavLink>
              ))}
            </div>
          </div>
        );
      })}
    </nav>
  );
}

function Brand({ size = 'md' }: { size?: 'sm' | 'md' }) {
  return (
    <div className="flex items-center gap-2.5 px-3">
      <span
        className={cx('grid place-items-center rounded-md bg-fg text-bg', size === 'md' ? 'h-7 w-7' : 'h-6 w-6')}
        aria-hidden="true"
      >
        <svg width={size === 'md' ? 16 : 14} height={size === 'md' ? 16 : 14} viewBox="0 0 32 32" fill="none" stroke="currentColor">
          <circle cx="16" cy="16" r="11" strokeWidth="3" />
          <ellipse cx="16" cy="16" rx="4.6" ry="11" strokeWidth="2" />
          <path d="M3 16h26" strokeWidth="2.4" strokeLinecap="round" />
        </svg>
      </span>
      <span className="leading-none">
        <span className={cx('block font-semibold tracking-tight text-fg', size === 'md' ? 'text-[15px]' : 'text-sm')}>
          babel.plus 后台
        </span>
        {size === 'md' ? (
          <span className="mt-1 flex items-center gap-1.5 font-mono text-[10px] tracking-widest text-fg-subtle uppercase">
            <Led tone="accent" /> admin · iap
          </span>
        ) : null}
      </span>
    </div>
  );
}

export function AdminLayout() {
  const [drawerOpen, setDrawerOpen] = useState(false);
  const { pathname } = useLocation();

  useEffect(() => setDrawerOpen(false), [pathname]);

  return (
    <div className="min-h-dvh bg-bg">
      <header className="sticky top-0 z-30 flex min-h-12 items-center gap-2 border-b border-line bg-surface/95 px-2 backdrop-blur lg:hidden">
        <Button
          tone="ghost"
          className="min-h-9 px-2"
          aria-label={drawerOpen ? '关闭导航' : '打开导航'}
          aria-expanded={drawerOpen}
          onClick={() => setDrawerOpen((v) => !v)}
        >
          {drawerOpen ? <Icon.Close size={20} /> : <Icon.Menu size={20} />}
        </Button>
        <Brand size="sm" />
      </header>

      {drawerOpen ? (
        <div className="border-b border-line bg-surface px-3 py-3 lg:hidden">
          <NavList onNavigate={() => setDrawerOpen(false)} />
        </div>
      ) : null}

      <div className="flex">
        <aside className="hidden w-60 shrink-0 border-r border-line bg-surface/40 lg:block">
          <div className="sticky top-0 flex max-h-dvh flex-col overflow-y-auto p-3">
            <div className="mb-5 pt-2">
              <Brand />
            </div>
            <NavList />
            <p className="mt-6 border-t border-line pt-3 font-mono text-[10.5px] leading-relaxed text-fg-subtle">
              AUDIT · 这里的每一次改动都会写进审计日志（谁、何时、改前值、改后值）。
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
