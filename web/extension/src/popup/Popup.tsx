/**
 * popup：八个状态各一段，顺序与 spec §3.5 的表、mockup 的八张卡逐一对应。
 * 每个状态的主按钮是**唯一**的产品决定：Top up 只在「将尽」态成为主按钮（唯一的 upsell）。
 */
import { useState } from 'react';
import { ago, bytesShort, dayMonth, gb } from '../shared/format.ts';
import { detectLanguage, translator } from '../shared/i18n.ts';
import { deriveUiState, type UiState } from '../shared/state.ts';
import type { Snapshot } from '../shared/types.ts';
import { Banner, Bar, Btn, Kv, Meter, RegionPick, SignInForm, signInErrorText, type LedTone, type T } from '../ui/components.tsx';
import { useSnapshot, type SnapshotHandle } from '../ui/useSnapshot.ts';

const NO_ROUTE_TEXT = {
  'all-endpoints-failed': 'no_route_all_failed',
  'no-endpoints': 'no_route_no_endpoints',
  'auth-rejected': 'no_route_auth',
  'config-unavailable': 'no_route_config',
  'proxy-not-controllable': 'no_route_not_controllable',
} as const;

const TONE: Record<UiState['kind'], LedTone> = {
  'signed-out': 'off',
  off: 'off',
  connecting: 'wait',
  on: 'on',
  low: 'warn',
  exhausted: 'bad',
  expired: 'bad',
  'no-route': 'bad',
};

const LABEL = {
  'signed-out': 'status_signed_out',
  off: 'status_off',
  connecting: 'status_connecting',
  on: 'status_on',
  low: 'status_low',
  exhausted: 'status_exhausted',
  expired: 'status_expired',
  'no-route': 'status_no_route',
} as const;

export function Popup() {
  const handle = useSnapshot();
  const t = translator(detectLanguage());
  if (handle.loading || !handle.snapshot) {
    return (
      <div className="pop">
        <Bar t={t} tone="wait" label="…" />
      </div>
    );
  }
  return <PopupView t={t} handle={handle} snapshot={handle.snapshot} />;
}

export function PopupView({ t, handle, snapshot }: { t: T; handle: SnapshotHandle; snapshot: Snapshot }) {
  const ui = deriveUiState(snapshot);
  const [copied, setCopied] = useState(false);
  const open = (target: 'topup' | 'renew' | 'help' | 'backup' | 'options') => void handle.send({ type: 'open', target });
  const copyDiagnostics = async () => {
    const res = await handle.send({ type: 'diagnostics' });
    if (res.ok && res.text) {
      try {
        await navigator.clipboard.writeText(res.text);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      } catch {
        /* 剪贴板不可用：文本仍在 res.text，options 页有另一条导出路径 */
      }
    }
  };

  const foot = (leadAction?: 'get-a-pass') => (
    <div className="pop__foot">
      {leadAction === 'get-a-pass' ? (
        <button type="button" className="lead" onClick={() => open('topup')}>
          {t('get_a_pass')}
        </button>
      ) : null}
      {snapshot.signedIn && ui.kind !== 'low' && ui.kind !== 'exhausted' && ui.kind !== 'expired' ? (
        <button type="button" onClick={() => open('topup')}>
          {t('top_up')}
        </button>
      ) : null}
      <button type="button" onClick={() => open('help')}>
        {t('help')}
      </button>
      {snapshot.signedIn ? (
        <button type="button" onClick={() => void handle.send({ type: 'sign-out' })}>
          {t('sign_out')}
        </button>
      ) : null}
      <button type="button" onClick={() => open('options')} style={{ marginLeft: 'auto' }}>
        {t('options')}
      </button>
    </div>
  );

  const region = (value: string | null, disabled = false) => (
    <RegionPick
      t={t}
      regions={snapshot.regions}
      value={value}
      disabled={disabled}
      onChange={(next) => void handle.send({ type: 'connect', region: next })}
    />
  );

  let body: React.ReactNode;
  switch (ui.kind) {
    case 'signed-out':
      body = (
        <SignInForm
          t={t}
          busy={handle.busy}
          error={signInErrorText(t, handle.error ?? snapshot.lastError)}
          onSubmit={(email, password) => void handle.send({ type: 'sign-in', email, password })}
        />
      );
      break;
    case 'off':
      body = (
        <>
          <Meter t={t} quota={ui.quota} />
          <RegionPick
            t={t}
            regions={snapshot.regions}
            value={ui.region}
            onChange={(next) => void handle.send({ type: 'set-prefs', prefs: { region: next } })}
          />
          <Btn tone="go" disabled={handle.busy} onClick={() => void handle.send({ type: 'connect', region: ui.region })}>
            {t('connect')}
          </Btn>
          {handle.error ? <p className="error">{handle.error.message}</p> : null}
        </>
      );
      break;
    case 'connecting':
      body = (
        <>
          <Banner tone="info">{t('testing_endpoints', { n: Math.max(ui.probes.length, snapshot.regions.reduce((n, r) => n + r.endpointCount, 0) || 1) })}</Banner>
          {ui.probes.map((p) => (
            <Kv key={p.endpointId} k={p.label} v={p.ok ? (p.latencyMs === null ? t('untested') : `${p.latencyMs} ms ✓`) : `✗ ${p.error ?? ''}`} />
          ))}
          <Btn tone="wait" onClick={() => void handle.send({ type: 'cancel' })}>
            {t('cancel')}
          </Btn>
        </>
      );
      break;
    case 'on':
      body = (
        <>
          <Meter t={t} quota={ui.quota} />
          {region(ui.region)}
          <Kv k={t('your_exit_ip')} v={ui.exitIp ?? '—'} />
          <Kv k={t('this_session')} v={bytesShort(ui.sessionBytes)} />
          <Btn tone="stop" disabled={handle.busy} onClick={() => void handle.send({ type: 'disconnect' })}>
            {t('disconnect')}
          </Btn>
        </>
      );
      break;
    case 'low':
      body = (
        <>
          <Banner tone="warn">
            <b>{t('low_banner', { left: gb(ui.quota.remainingBytes) })}</b> {t('low_banner_tail')}
          </Banner>
          <Meter t={t} quota={ui.quota} tone="warn" />
          <Btn tone="warn" onClick={() => open('topup')}>
            {t('top_up')}
          </Btn>
          {ui.connected ? (
            <Btn tone="ghost" onClick={() => void handle.send({ type: 'disconnect' })}>
              {t('disconnect')}
            </Btn>
          ) : (
            <Btn tone="ghost" disabled={handle.busy} onClick={() => void handle.send({ type: 'connect', region: ui.region })}>
              {t('connect')}
            </Btn>
          )}
        </>
      );
      break;
    case 'exhausted':
      body = (
        <>
          <Banner tone="bad">
            <b>{t('exhausted_banner')}</b> {t('exhausted_banner_tail')}
          </Banner>
          <Meter t={t} quota={ui.quota} tone="bad" />
          <Btn tone="go" onClick={() => open('topup')}>
            {t('buy_more')}
          </Btn>
          <Btn tone="ghost" disabled>
            {t('connect')}
          </Btn>
        </>
      );
      break;
    case 'expired':
      body = (
        <>
          <Banner tone="bad">
            <b>{t('expired_banner', { date: dayMonth(ui.quota.expiredAt) })}</b>{' '}
            {ui.quota.hasQuota ? t('expired_unused', { unused: gb(ui.quota.remainingBytes) }) : ''}
          </Banner>
          <Banner tone="info">{t('carry_over_rule')}</Banner>
          <Btn tone="go" onClick={() => open('renew')}>
            {t('renew')}
          </Btn>
        </>
      );
      break;
    case 'no-route':
      body = (
        <>
          <Banner tone="bad">
            <b>{t('no_route_banner')}</b> {t(NO_ROUTE_TEXT[ui.reason], { n: ui.failedEndpoints })}
            {snapshot.lastError && ui.reason === 'config-unavailable' ? ` (${snapshot.lastError.message})` : ''}
          </Banner>
          <Kv k={t('last_success')} v={ago(ui.lastSuccessAt)} />
          <Btn tone="go" disabled={handle.busy} onClick={() => void handle.send({ type: 'connect', region: snapshot.prefs.region })}>
            {t('retry')}
          </Btn>
          {snapshot.links.backupPageUrl ? (
            <Btn tone="ghost" onClick={() => open('backup')}>
              {t('open_backup')}
            </Btn>
          ) : null}
          <Btn tone="ghost" onClick={() => void copyDiagnostics()}>
            {copied ? t('copied') : t('copy_diagnostics')}
          </Btn>
        </>
      );
      break;
  }

  return (
    <div className="pop" data-state={ui.kind}>
      <Bar t={t} tone={TONE[ui.kind]} label={t(LABEL[ui.kind])} />
      <div className="pop__body">{body}</div>
      {foot(ui.kind === 'signed-out' ? 'get-a-pass' : undefined)}
    </div>
  );
}
