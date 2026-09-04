/**
 * 配额的四个阈值（正常 / < 10% / 用尽 / 过期）。
 *
 * 口径与用户面板 `DashboardPage`、扩展 `shared/quota.ts` 逐条一致 —— 三处显示同一个数字，
 * 对不上就会有工单。`total_bytes = 0` 是「不限流量」还是「还没配额度」契约没写明，
 * 三处都选择不显示进度条、也不判 low/exhausted。
 *
 * 🔴 这个数字**只用于显示与断开判定**，不用于计费。计费口径唯一来源是节点上报
 * （data-model §9），客户端本地累计永远不参与。
 */
import type { SubscriptionSummary } from '../shared/types.ts';

export const LOW_FRACTION = 0.1;
const DAY_MS = 86_400_000;

export interface QuotaView {
  readonly hasQuota: boolean;
  readonly usedBytes: number;
  readonly totalBytes: number;
  readonly remainingBytes: number;
  readonly usedFraction: number;
  readonly expiredAt: string | null;
  readonly daysLeft: number | null;
  readonly expired: boolean;
  readonly exhausted: boolean;
  readonly low: boolean;
  readonly planName: string | null;
}

export function quotaView(summary: SubscriptionSummary | null, now: number = Date.now()): QuotaView | null {
  if (!summary) return null;
  const usedBytes = Math.max(0, summary.upload_bytes + summary.download_bytes);
  const totalBytes = Math.max(0, summary.total_bytes);
  const hasQuota = totalBytes > 0;
  const remainingBytes = hasQuota ? Math.max(0, totalBytes - usedBytes) : 0;
  const expiredAt = summary.expired_at ?? null;
  const expiredAtMs = expiredAt ? Date.parse(expiredAt) : Number.NaN;
  const hasExpiry = Number.isFinite(expiredAtMs);
  const exhausted = hasQuota && usedBytes >= totalBytes;
  return {
    hasQuota,
    usedBytes,
    totalBytes,
    remainingBytes,
    usedFraction: hasQuota ? Math.min(1, usedBytes / totalBytes) : 0,
    expiredAt,
    daysLeft: hasExpiry ? Math.floor((expiredAtMs - now) / DAY_MS) : null,
    expired: hasExpiry && expiredAtMs <= now,
    exhausted,
    low: hasQuota && !exhausted && remainingBytes / totalBytes < LOW_FRACTION,
    planName: summary.plan_name ?? null,
  };
}

/** 字节 → 「12.4」这种 GB 数字串（二进制 GB，与面板同口径）。 */
export function gb(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  return (bytes / 1024 ** 3).toFixed(1);
}

/** 字节 → 带单位的短串（标签页用量）。 */
export function bytesShort(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(2)} GB`;
  if (bytes >= 1024 ** 2) return `${Math.round(bytes / 1024 ** 2)} MB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${Math.round(bytes)} B`;
}
