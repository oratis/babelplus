/**
 * 认证四页的布局。
 *
 * 页脚的备用域名列表在这里**尤其**重要：page-inventory §3.2.1 把
 * 「页脚常驻备用域名列表」直接写进了 `/auth/login` 的关键 UI ——
 * 打不开登录页的用户是最需要备用域名的那一群，而他们此刻还没登录，
 * 面板里其他任何地方都到不了。
 *
 * 视觉：细网格背景只铺在这一层（design-system.md §4.1「网格只出现在页首与认证页」），
 * 卡片浮在网格上；品牌块带一颗状态灯与等宽小标，与面板导轨同款。
 */
import { Outlet } from 'react-router';
import { Eyebrow, Led, SiteFooter } from '@babelplus/shared/ui';

export function AuthLayout() {
  return (
    <div className="flex min-h-dvh flex-col bg-bg">
      <main className="bp-grid-bg flex flex-1 items-start justify-center px-4 pt-12 pb-8 sm:pt-20">
        <div className="w-full max-w-sm">
          <div className="mb-6 flex flex-col items-center gap-2">
            <span className="grid h-9 w-9 place-items-center rounded-md bg-accent text-accent-fg" aria-hidden="true">
              <svg width="18" height="18" viewBox="0 0 32 32" fill="none" stroke="currentColor">
                <circle cx="16" cy="16" r="11" strokeWidth="3" />
                <ellipse cx="16" cy="16" rx="4.6" ry="11" strokeWidth="2" />
                <path d="M3 16h26" strokeWidth="2.4" strokeLinecap="round" />
              </svg>
            </span>
            <p className="text-lg font-semibold tracking-tight text-fg">babel.plus</p>
            <Eyebrow className="flex items-center gap-1.5">
              <Led tone="ok" /> account console
            </Eyebrow>
          </div>
          <Outlet />
        </div>
      </main>
      <SiteFooter compact />
    </div>
  );
}
