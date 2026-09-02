/**
 * 配额的四个阈值（正常 / < 10% / 用尽 / 过期），popup、options、onboarding 与浏览器共用同一套（spec §5）。
 *
 * 口径照抄用户面板 `DashboardPage`：`used = upload + download`，`hasQuota = total_bytes > 0`；
 * `total_bytes = 0` 是「不限流量」还是「还没配额度」契约没写明，面板选择不显示进度条，这里同样不判 low / exhausted。
 * `expired_at = null` 在契约里明确表示不限时套餐，不是「读不到」。
 */
import type { SubscriptionSummary } from './types.ts';

export const LOW_QUOTA_FRACTION = 0.1;

export interface QuotaView {
  readonly hasQuota: boolean;
  readonly usedBytes: number;
  readonly totalBytes: number;
  readonly remainingBytes: number;
  /** 0–1；无配额时为 0。 */
  readonly usedFraction: number;
  readonly expiredAt: string | null;
  /** 剩余整天数；不限时为 `null`；已过期为负数。 */
  readonly daysLeft: number | null;
  readonly expired: boolean;
  readonly exhausted: boolean;
  readonly low: boolean;
  readonly planName: string | null;
}

const DAY_MS = 86_400_000;

export function quotaView(summary: SubscriptionSummary | null, now: number = Date.now()): QuotaView | null {
  if (!summary) return null;
  const usedBytes = Math.max(0, summary.upload_bytes + summary.download_bytes);
  const totalBytes = Math.max(0, summary.total_bytes);
  const hasQuota = totalBytes > 0;
  const remainingBytes = hasQuota ? Math.max(0, totalBytes - usedBytes) : 0;
  const usedFraction = hasQuota ? Math.min(1, usedBytes / totalBytes) : 0;

  const expiredAt = summary.expired_at ?? null;
  const expiredAtMs = expiredAt ? Date.parse(expiredAt) : Number.NaN;
  const hasExpiry = Number.isFinite(expiredAtMs);
  const daysLeft = hasExpiry ? Math.floor((expiredAtMs - now) / DAY_MS) : null;
  const expired = hasExpiry && expiredAtMs <= now;
  const exhausted = hasQuota && usedBytes >= totalBytes;
  const low = hasQuota && !exhausted && remainingBytes / totalBytes < LOW_QUOTA_FRACTION;

  return {
    hasQuota,
    usedBytes,
    totalBytes,
    remainingBytes,
    usedFraction,
    expiredAt,
    daysLeft,
    expired,
    exhausted,
    low,
    planName: summary.plan_name ?? null,
  };
}
