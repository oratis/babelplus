/**
 * popup 上的数字格式。**英文优先**（商业前提），单位与用户面板一致：二进制 GB（1 GB = 1024³）。
 * 与 `@babelplus/shared` 的 `formatBytes` 不同处只有一点：它用 `zh-CN` 本地化并带单位，
 * 而 popup 的进度条要的是「12.4 / 20 GB」这种裸数字 + 一次单位。
 */

const GB = 1024 ** 3;
const MB = 1024 ** 2;

/** 字节 → GB 数字串，一位小数；小于 0.05 GB 显示 0.0。 */
export function gb(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  return (bytes / GB).toFixed(1);
}

/** 字节 → 带单位的短串（会话用量、探测体积）。 */
export function bytesShort(bytes: number | null): string {
  if (bytes === null || !Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes >= GB) return `${(bytes / GB).toFixed(2)} GB`;
  if (bytes >= MB) return `${Math.round(bytes / MB)} MB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${Math.round(bytes)} B`;
}

/** 到期日 → 「2 September」这种人读得懂的英文日期；解析失败返回 `—` 而不是 `Invalid Date`。 */
export function dayMonth(iso: string | null, locale = 'en-GB'): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return new Intl.DateTimeFormat(locale, { day: 'numeric', month: 'long' }).format(d);
}

/** 「14 min ago」。超过一天只说天数；未来时间当作刚刚。 */
export function ago(iso: string | null, now: number = Date.now()): string {
  if (!iso) return '—';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '—';
  const s = Math.max(0, Math.floor((now - t) / 1000));
  if (s < 60) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} h ago`;
  return `${Math.floor(h / 24)} d ago`;
}
