/** 后台导航。顺序与编号照 page-inventory §4.2 的模块总表。 */
import { Icon } from '@babelplus/shared/ui';

export interface AdminNavItem {
  readonly to: string;
  readonly label: string;
  readonly icon: (typeof Icon)[keyof typeof Icon];
  readonly priority: 'P1' | 'P2' | 'P3';
  /** M2 的模块手机上必须能操作 —— 导航里标出来，别在小屏上把它们藏掉。 */
  readonly mobile: 'M2' | 'M3';
}

export const ADMIN_NAV: readonly AdminNavItem[] = [
  { to: '/admin', label: '运营看板', icon: Icon.Dashboard, priority: 'P1', mobile: 'M3' },
  { to: '/admin/users', label: '用户', icon: Icon.Users, priority: 'P1', mobile: 'M3' },
  { to: '/admin/orders', label: '订单', icon: Icon.Receipt, priority: 'P1', mobile: 'M2' },
  { to: '/admin/plans', label: '套餐', icon: Icon.Package, priority: 'P1', mobile: 'M3' },
  { to: '/admin/nodes', label: '节点', icon: Icon.Server, priority: 'P1', mobile: 'M2' },
  { to: '/admin/node-keys', label: '节点密钥', icon: Icon.Key, priority: 'P1', mobile: 'M3' },
  { to: '/admin/stats', label: '流量统计', icon: Icon.Chart, priority: 'P1', mobile: 'M3' },
  { to: '/admin/tickets', label: '工单', icon: Icon.Ticket, priority: 'P1', mobile: 'M2' },
  { to: '/admin/invites', label: '邀请与返佣', icon: Icon.Gift, priority: 'P1', mobile: 'M3' },
  { to: '/admin/audit', label: '审计日志', icon: Icon.Scroll, priority: 'P1', mobile: 'M3' },
  { to: '/admin/admins', label: '管理员', icon: Icon.Shield, priority: 'P1', mobile: 'M3' },
  { to: '/admin/notices', label: '公告', icon: Icon.Megaphone, priority: 'P2', mobile: 'M3' },
  { to: '/admin/coupons', label: '优惠码', icon: Icon.Coupon, priority: 'P2', mobile: 'M3' },
  { to: '/admin/payments', label: '支付与对账', icon: Icon.Coin, priority: 'P2', mobile: 'M3' },
  { to: '/admin/mail', label: '邮件与送达', icon: Icon.Mail, priority: 'P2', mobile: 'M3' },
  { to: '/admin/settings', label: '系统配置', icon: Icon.Settings, priority: 'P2', mobile: 'M3' },
  { to: '/admin/domains', label: '域名池', icon: Icon.Globe, priority: 'P3', mobile: 'M3' },
];
