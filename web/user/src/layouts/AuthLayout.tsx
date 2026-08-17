/**
 * 认证四页的布局。
 *
 * 页脚的备用域名列表在这里**尤其**重要：page-inventory §3.2.1 把
 * 「页脚常驻备用域名列表」直接写进了 `/auth/login` 的关键 UI ——
 * 打不开登录页的用户是最需要备用域名的那一群，而他们此刻还没登录，
 * 面板里其他任何地方都到不了。
 */
import { Outlet } from 'react-router';
import { SiteFooter } from '@babelplus/shared/ui';

export function AuthLayout() {
  return (
    <div className="flex min-h-dvh flex-col bg-bg">
      <main className="flex flex-1 items-start justify-center px-4 pt-10 sm:pt-16">
        <div className="w-full max-w-sm">
          <p className="mb-6 text-center text-lg font-semibold tracking-tight text-fg">babel.plus</p>
          <Outlet />
        </div>
      </main>
      <SiteFooter compact />
    </div>
  );
}
