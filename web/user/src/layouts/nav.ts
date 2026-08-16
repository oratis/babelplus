/**
 * 用户面板导航。顺序按 page-inventory §3.1 的路由总表，
 * P1 在前、P2/P3 在后 —— 导航顺序本身就是一次优先级声明。
 */
import { Icon } from '@babelplus/shared/ui';

export interface NavItem {
  readonly to: string;
  readonly label: string;
  readonly icon: (typeof Icon)[keyof typeof Icon];
  readonly priority: 'P1' | 'P2' | 'P3';
}

export const NAV: readonly NavItem[] = [
  { to: '/dashboard', label: '概览', icon: Icon.Dashboard, priority: 'P1' },
  { to: '/subscribe', label: '订阅与设备', icon: Icon.Link, priority: 'P1' },
  { to: '/plan', label: '套餐', icon: Icon.Package, priority: 'P1' },
  { to: '/order', label: '订单', icon: Icon.Receipt, priority: 'P1' },
  { to: '/ticket', label: '工单', icon: Icon.Ticket, priority: 'P1' },
  { to: '/usage', label: '用量', icon: Icon.Chart, priority: 'P2' },
  { to: '/wallet', label: '钱包', icon: Icon.Wallet, priority: 'P2' },
  { to: '/invite', label: '邀请', icon: Icon.Gift, priority: 'P2' },
  { to: '/node', label: '节点', icon: Icon.Server, priority: 'P2' },
  { to: '/notice', label: '公告', icon: Icon.Megaphone, priority: 'P2' },
  { to: '/diagnose', label: '诊断', icon: Icon.Stethoscope, priority: 'P2' },
  { to: '/profile', label: '账号', icon: Icon.User, priority: 'P1' },
];
