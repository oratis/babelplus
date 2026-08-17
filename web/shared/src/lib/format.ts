/**
 * 格式化。**契约里的单位约定必须在这里被尊重**（openapi.yaml info.description）：
 * 金额是「分」的 int64、USDT 是 1e-6、流量是字节 int64、倍率是 1e9 定点。
 * 任何地方都不得把它们当浮点算。
 */

const CN = 'zh-CN';

/** 字节 → 人类可读。二进制单位（KiB 口径），与客户端里显示的口径一致，避免用户对不上数。 */
export function formatBytes(bytes: number | null | undefined, fractionDigits = 2): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes)) return '—';
  if (bytes < 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'] as const;
  let value = bytes;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  const unit = units[unitIndex] ?? 'B';
  return `${value.toFixed(unitIndex === 0 ? 0 : fractionDigits)} ${unit}`;
}

/**
 * 「分」→ 人民币展示。
 * **不用浮点做除法后再四舍五入** —— 整数除模，避免 `12345 / 100` 这类精度事故。
 */
export function formatCny(cents: number | null | undefined): string {
  if (cents === null || cents === undefined || !Number.isFinite(cents)) return '—';
  const negative = cents < 0;
  const abs = Math.abs(Math.trunc(cents));
  const yuan = Math.floor(abs / 100);
  const fen = abs % 100;
  const body = `${yuan.toLocaleString(CN)}.${String(fen).padStart(2, '0')}`;
  return `${negative ? '-' : ''}¥${body}`;
}

/** 1e-6 USDT → 展示串。收银台要精确到分位做金额唯一性匹配，所以默认保留 6 位再裁尾。 */
export function formatUsdt(amountUsdt6: number | null | undefined, fractionDigits = 6): string {
  if (amountUsdt6 === null || amountUsdt6 === undefined || !Number.isFinite(amountUsdt6)) return '—';
  const negative = amountUsdt6 < 0;
  const abs = Math.abs(Math.trunc(amountUsdt6));
  const whole = Math.floor(abs / 1_000_000);
  const frac = String(abs % 1_000_000).padStart(6, '0').slice(0, Math.max(0, Math.min(6, fractionDigits)));
  const body = frac.length > 0 ? `${whole}.${frac}` : String(whole);
  return `${negative ? '-' : ''}${body} USDT`;
}

/** 1e9 定点倍率 → 展示串。第一阶段倍率恒为 1x（product-brief §6：不引入倍率）。 */
export function formatMultiplier(multiplierE9: number | null | undefined): string {
  if (multiplierE9 === null || multiplierE9 === undefined || !Number.isFinite(multiplierE9)) return '—';
  return `${(multiplierE9 / 1e9).toFixed(2).replace(/\.?0+$/, '')}x`;
}

/** RFC3339 → 本地时间串。解析失败返回 `—` 而不是 `Invalid Date`。 */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return new Intl.DateTimeFormat(CN, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(d);
}

/** 只到日。到期日这类字段用它，避免让用户去读时分秒。 */
export function formatDate(iso: string | null | undefined): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return new Intl.DateTimeFormat(CN, { year: 'numeric', month: '2-digit', day: '2-digit' }).format(d);
}

/** Unix 秒 → 展示串。UniProxy 面与订阅体里的时间是秒，不是 RFC3339，别混。 */
export function formatUnixSeconds(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds)) return '—';
  return formatDateTime(new Date(seconds * 1000).toISOString());
}

/** 距今天数（向上取整）。到期提醒用。过期返回负数，调用方自己决定怎么说。 */
export function daysUntil(iso: string | null | undefined, now: Date = new Date()): number | null {
  if (!iso) return null;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return null;
  return Math.ceil((d.getTime() - now.getTime()) / 86_400_000);
}

/**
 * 订阅链接 / token 打码。
 * `/subscribe` 默认打码显示（page-inventory §3.2.3）—— 订阅链接等同于凭据，
 * 用户常在咖啡馆或办公室截图求助，默认明文就是默认泄漏。
 */
export function maskSecret(value: string | null | undefined, head = 8, tail = 4): string {
  if (!value) return '—';
  if (value.length <= head + tail) return '•'.repeat(value.length);
  return `${value.slice(0, head)}${'•'.repeat(12)}${value.slice(-tail)}`;
}

/** 百分比（0–100），用于流量进度条。分母为 0 时返回 0 而不是 NaN。 */
export function percent(used: number | null | undefined, total: number | null | undefined): number {
  if (!used || !total || total <= 0) return 0;
  return Math.min(100, Math.max(0, (used / total) * 100));
}
