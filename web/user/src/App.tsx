/**
 * 用户面板路由表 —— page-inventory §3.1 的 20 条路由，一条不多一条不少。
 *
 * 刻意**不做**路由级代码分割：现在每页都是空壳（1–3 KB），拆成 20 个 chunk
 * 只会把一次往返变成二十次，而大陆跨境链路的成本主要在往返次数上（ADR 0003 §4）。
 * 页面接线后体积起来了再按实际大小评估，那时才有数据可依。
 *
 * 路由守卫在这里落地：`AppLayout` 下的 16 条路由全部包在 `RequireAuth` 里。
 * 认证四页留在守卫外面（它们本来就是给未登录用户看的），`/` 的重定向也在外面 ——
 * 它只是把人送到 `/dashboard`，到了那里自然会被守卫接住。
 *
 * ⚠️ 守卫是 **layout route**（`<Route element={<RequireAuth />}>`）而不是包在每个页面上：
 * 逐页包的写法总有一天会漏掉新加的那一页，而漏掉的表现是「这一页不用登录也能看」——
 * 一个不会有人报 bug 的缺陷。
 */
import { Navigate, Route, Routes } from 'react-router';

import { AppLayout } from './layouts/AppLayout.tsx';
import { AuthLayout } from './layouts/AuthLayout.tsx';
import { AuthProvider, RequireAuth } from './lib/auth.tsx';

import LoginPage from './routes/auth/LoginPage.tsx';
import RegisterPage from './routes/auth/RegisterPage.tsx';
import ForgotPage from './routes/auth/ForgotPage.tsx';
import ResetPage from './routes/auth/ResetPage.tsx';

import DashboardPage from './routes/DashboardPage.tsx';
import SubscribePage from './routes/SubscribePage.tsx';
import SubscribeTokensPage from './routes/SubscribeTokensPage.tsx';
import PlanPage from './routes/PlanPage.tsx';
import OrderListPage from './routes/OrderListPage.tsx';
import OrderDetailPage from './routes/OrderDetailPage.tsx';
import TicketListPage from './routes/TicketListPage.tsx';
import TicketDetailPage from './routes/TicketDetailPage.tsx';
import ProfilePage from './routes/ProfilePage.tsx';
import ProfileTwoFactorPage from './routes/ProfileTwoFactorPage.tsx';
import UsagePage from './routes/UsagePage.tsx';
import WalletPage from './routes/WalletPage.tsx';
import InvitePage from './routes/InvitePage.tsx';
import NodePage from './routes/NodePage.tsx';
import NoticePage from './routes/NoticePage.tsx';
import DiagnosePage from './routes/DiagnosePage.tsx';
import NotFoundPage from './routes/NotFoundPage.tsx';

export function App() {
  return (
    <AuthProvider>
      <Routes>
        {/* ① 认证四页（page-inventory §3.2.1）——免登录 */}
        <Route element={<AuthLayout />}>
          <Route path="/auth/login" element={<LoginPage />} />
          <Route path="/auth/register" element={<RegisterPage />} />
          <Route path="/auth/forgot" element={<ForgotPage />} />
          <Route path="/auth/reset" element={<ResetPage />} />
        </Route>

        {/* ② 登录后的 16 条 —— 整段在 RequireAuth 之下 */}
        <Route element={<RequireAuth />}>
          <Route element={<AppLayout />}>
            <Route path="/dashboard" element={<DashboardPage />} />
            <Route path="/subscribe" element={<SubscribePage />} />
            <Route path="/subscribe/tokens" element={<SubscribeTokensPage />} />
            <Route path="/plan" element={<PlanPage />} />
            <Route path="/order" element={<OrderListPage />} />
            <Route path="/order/:trade_no" element={<OrderDetailPage />} />
            <Route path="/ticket" element={<TicketListPage />} />
            <Route path="/ticket/:public_id" element={<TicketDetailPage />} />
            <Route path="/profile" element={<ProfilePage />} />
            <Route path="/profile/2fa" element={<ProfileTwoFactorPage />} />
            <Route path="/usage" element={<UsagePage />} />
            <Route path="/wallet" element={<WalletPage />} />
            <Route path="/invite" element={<InvitePage />} />
            <Route path="/node" element={<NodePage />} />
            <Route path="/notice" element={<NoticePage />} />
            <Route path="/diagnose" element={<DiagnosePage />} />
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Route>

        <Route path="/" element={<Navigate to="/dashboard" replace />} />
      </Routes>
    </AuthProvider>
  );
}
