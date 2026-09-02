/**
 * 三个页面共用的小组件。没有第三方组件库：popup 只有 340px 宽、八个状态，
 * 引一个库换来的是几十 KB 与一套我们不控制的样式变量。
 */
import { useState, type FormEvent, type ReactNode } from 'react';
import { gb } from '../shared/format.ts';
import type { MessageKey } from '../shared/i18n.ts';
import type { QuotaView } from '../shared/quota.ts';
import type { RegionOption } from '../shared/types.ts';

export type T = (key: MessageKey, vars?: Record<string, string | number>) => string;

export type LedTone = 'off' | 'on' | 'warn' | 'bad' | 'wait';

export function Bar({ t, tone, label }: { t: T; tone: LedTone; label: string }) {
  return (
    <div className="pop__bar">
      <div className="pop__brand">
        <i className="glyph" aria-hidden="true" />
        {t('brand')}
      </div>
      <div className="pop__status" role="status">
        <i className={`led${tone === 'off' ? '' : ` led--${tone}`}`} aria-hidden="true" />
        {label}
      </div>
    </div>
  );
}

export function Banner({ tone, children }: { tone: 'warn' | 'bad' | 'info'; children: ReactNode }) {
  return (
    <div className={`banner banner--${tone}`} role={tone === 'info' ? 'note' : 'alert'}>
      {children}
    </div>
  );
}

export function Meter({ t, quota, tone }: { t: T; quota: QuotaView | null; tone?: 'warn' | 'bad' }) {
  if (!quota) {
    return (
      <div className="meter">
        <div className="meter__row">
          <span className="meter__val">—</span>
          <span className="meter__days">{t('quota_hint')}</span>
        </div>
      </div>
    );
  }
  const days =
    quota.daysLeft === null ? t('no_expiry') : quota.daysLeft === 1 ? t('day_left') : t('days_left', { n: Math.max(0, quota.daysLeft) });
  return (
    <div className="meter">
      <div className="meter__row">
        <span className="meter__val">
          {quota.hasQuota ? (
            <>
              {gb(quota.usedBytes)} <em>/ {gb(quota.totalBytes)} GB</em>
            </>
          ) : (
            <>
              {gb(quota.usedBytes)} <em>GB · {t('unlimited')}</em>
            </>
          )}
        </span>
        <span className="meter__days">{days}</span>
      </div>
      {quota.hasQuota ? (
        <div
          className="track"
          role="progressbar"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.round(quota.usedFraction * 100)}
        >
          <div className={`fill${tone ? ` fill--${tone}` : ''}`} style={{ width: `${Math.round(quota.usedFraction * 100)}%` }} />
        </div>
      ) : null}
    </div>
  );
}

export function RegionPick({
  t,
  regions,
  value,
  disabled,
  onChange,
}: {
  t: T;
  regions: readonly RegionOption[];
  value: string | null;
  disabled?: boolean;
  onChange: (region: string | null) => void;
}) {
  const current = regions.find((r) => r.code === value) ?? null;
  return (
    <label className="pick">
      <span className="sr-only">{t('region')}</span>
      <select
        aria-label={t('region')}
        value={value ?? ''}
        disabled={disabled}
        onChange={(e) => onChange(e.currentTarget.value === '' ? null : e.currentTarget.value)}
      >
        <option value="">{t('fastest')}</option>
        {regions.map((r) => (
          <option key={r.code} value={r.code}>
            {r.label}
          </option>
        ))}
      </select>
      <span className="pick__ms">
        {current ? (current.latencyMs === null ? t('untested') : `${current.latencyMs} ms`) : ''}
      </span>
    </label>
  );
}

export function Kv({ k, v }: { k: string; v: ReactNode }) {
  return (
    <div className="kv">
      <span className="kv__k">{k}</span>
      <span className="kv__v">{v}</span>
    </div>
  );
}

export function Btn({
  tone,
  children,
  onClick,
  disabled,
  type = 'button',
}: {
  tone: 'go' | 'stop' | 'warn' | 'ghost' | 'wait';
  children: ReactNode;
  onClick?: () => void;
  disabled?: boolean;
  type?: 'button' | 'submit';
}) {
  return (
    <button type={type} className={`btn btn--${tone}`} onClick={onClick} disabled={disabled}>
      {children}
    </button>
  );
}

export function SignInForm({
  t,
  busy,
  error,
  onSubmit,
}: {
  t: T;
  busy: boolean;
  error: string | null;
  onSubmit: (email: string, password: string) => void;
}) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!email || !password || busy) return;
    onSubmit(email.trim(), password);
  };
  return (
    <form onSubmit={submit} className="stack" style={{ display: 'flex', flexDirection: 'column', gap: 13 }}>
      <label className="field">
        <span className="field__lab">{t('email')}</span>
        <input
          className="field__in"
          type="email"
          autoComplete="username"
          required
          value={email}
          onChange={(e) => setEmail(e.currentTarget.value)}
        />
      </label>
      <label className="field">
        <span className="field__lab">{t('password')}</span>
        <input
          className="field__in"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(e) => setPassword(e.currentTarget.value)}
        />
      </label>
      {error ? (
        <p className="error" role="alert">
          {error}
        </p>
      ) : null}
      <Btn tone="go" type="submit" disabled={busy}>
        {busy ? t('signing_in') : t('sign_in')}
      </Btn>
    </form>
  );
}

/** 登录错误码 → 文案。**前端禁止匹配 message 做分支**（契约 ErrorCode 的说明），所以只看 code。 */
export function signInErrorText(t: T, error: { code: string; message: string } | null): string | null {
  if (!error) return null;
  switch (error.code) {
    case 'AUTH_INVALID_CREDENTIALS':
      return t('invalid_credentials');
    case 'QUOTA_RATE_LIMITED':
      return t('rate_limited');
    case 'NETWORK':
    case 'SEND_FAILED':
    case 'NO_RESPONSE':
      return t('network_error');
    case 'NOT_CONFIGURED':
      return `${t('not_configured')}: ${error.message}`;
    default:
      return `${t('signin_failed')}: ${error.message}`;
  }
}
