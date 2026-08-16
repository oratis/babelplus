/**
 * 后台路由表 —— page-inventory §4.2 的 17 个模块。
 *
 * 路径保留 `/admin` 前缀（与文档一致），即使这个 SPA 部署在自己的主域名根路径上：
 * 前缀让日志、审计、反代规则里的路径一眼可辨，也让「后台 URL 被贴到工单里」这种事更容易发现。
 * **它不是安全措施** —— Xboard 用 `hash('crc32b', app.key)` 做路径混淆且无 2FA，
 * 那不是安全措施，只是拖延（page-inventory §4.1）。
 *
 * TODO(P1): 路由守卫 = IAP（代理层，前端零代码）+ 强制 TOTP（应用层，前端要做）。
 *           TOTP **不接受关闭**，所以守卫里没有「跳过」分支，一个都不要写。
 */
import { Navigate, Route, Routes } from 'react-router';

import { AdminLayout } from './layouts/AdminLayout.tsx';

import LoginPage from './routes/LoginPage.tsx';
import DashboardPage from './routes/DashboardPage.tsx';
import UsersPage from './routes/UsersPage.tsx';
import UserDetailPage from './routes/UserDetailPage.tsx';
import OrdersPage from './routes/OrdersPage.tsx';
import OrderDetailPage from './routes/OrderDetailPage.tsx';
import PlansPage from './routes/PlansPage.tsx';
import NodesPage from './routes/NodesPage.tsx';
import NodeDetailPage from './routes/NodeDetailPage.tsx';
import NodeKeysPage from './routes/NodeKeysPage.tsx';
import StatsPage from './routes/StatsPage.tsx';
import TicketsPage from './routes/TicketsPage.tsx';
import TicketDetailPage from './routes/TicketDetailPage.tsx';
import InvitesPage from './routes/InvitesPage.tsx';
import AuditPage from './routes/AuditPage.tsx';
import AdminsPage from './routes/AdminsPage.tsx';
import NoticesPage from './routes/NoticesPage.tsx';
import CouponsPage from './routes/CouponsPage.tsx';
import PaymentsPage from './routes/PaymentsPage.tsx';
import MailPage from './routes/MailPage.tsx';
import SettingsPage from './routes/SettingsPage.tsx';
import DomainsPage from './routes/DomainsPage.tsx';
import NotFoundPage from './routes/NotFoundPage.tsx';

export function App() {
  return (
    <Routes>
      {/* 闸 3：强制 TOTP。闸 1（独立域名）与闸 2（IAP / IP 白名单）不在前端。 */}
      <Route path="/admin/login" element={<LoginPage />} />

      <Route element={<AdminLayout />}>
        <Route path="/admin" element={<DashboardPage />} />
        <Route path="/admin/users" element={<UsersPage />} />
        <Route path="/admin/users/:id" element={<UserDetailPage />} />
        <Route path="/admin/orders" element={<OrdersPage />} />
        <Route path="/admin/orders/:trade_no" element={<OrderDetailPage />} />
        <Route path="/admin/plans" element={<PlansPage />} />
        <Route path="/admin/nodes" element={<NodesPage />} />
        <Route path="/admin/nodes/:id" element={<NodeDetailPage />} />
        <Route path="/admin/node-keys" element={<NodeKeysPage />} />
        <Route path="/admin/stats" element={<StatsPage />} />
        <Route path="/admin/tickets" element={<TicketsPage />} />
        <Route path="/admin/tickets/:id" element={<TicketDetailPage />} />
        <Route path="/admin/invites" element={<InvitesPage />} />
        <Route path="/admin/audit" element={<AuditPage />} />
        <Route path="/admin/admins" element={<AdminsPage />} />
        <Route path="/admin/notices" element={<NoticesPage />} />
        <Route path="/admin/coupons" element={<CouponsPage />} />
        <Route path="/admin/payments" element={<PaymentsPage />} />
        <Route path="/admin/mail" element={<MailPage />} />
        <Route path="/admin/settings" element={<SettingsPage />} />
        <Route path="/admin/domains" element={<DomainsPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>

      <Route path="/" element={<Navigate to="/admin" replace />} />
    </Routes>
  );
}
