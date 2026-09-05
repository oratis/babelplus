/**
 * 页面统一的导入出口。
 *
 * 存在的理由只有一个：20 个页面文件里重复 8 行 import 没有信息量，
 * 而这一层让「页面能用哪些东西」有一个可以审的清单。
 * 它**不是**抽象层 —— 只做转发，不加任何行为。
 */
export {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  ErrorState,
  Eyebrow,
  Icon,
  Led,
  LinkButton,
  Meter,
  Mono,
  Stat,
  LoadingState,
  MirrorDomainList,
  NotWiredNotice,
  PriorityBadge,
  Skeleton,
  SkeletonCard,
  SkeletonText,
  StateSwitch,
  cx,
} from '@babelplus/shared/ui';

export {
  daysUntil,
  formatBytes,
  formatCny,
  formatDate,
  formatDateTime,
  formatUsdt,
  maskSecret,
  percent,
  runtimeConfig,
} from '@babelplus/shared';
