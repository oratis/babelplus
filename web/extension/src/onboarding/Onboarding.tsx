/**
 * 安装后自动打开的三步（spec §3.5）：登录 → 选地区并连接 → 验证页显示出口 IP。
 * 第三步的四个方块是**用户点击才打开的外链**，扩展自己不请求它们
 * （web/scripts/check-no-external-assets.mjs 的白名单里有理由）。
 */
import { useEffect, useState } from 'react';
import { detectLanguage, translator } from '../shared/i18n.ts';
import { deriveUiState } from '../shared/state.ts';
import { Btn, Kv, RegionPick, SignInForm, signInErrorText } from '../ui/components.tsx';
import { useSnapshot } from '../ui/useSnapshot.ts';

const COPY = {
  en: {
    title: 'Welcome to babel.plus',
    lede: 'Three steps. Nothing to configure. If any step fails we say so here instead of dropping you into a browser that quietly does not work.',
    s1: 'Sign in with the email you bought your pass with',
    s1_p: 'Nothing else is asked — no name, no card, no account to create first.',
    s1_done: 'Signed in',
    s2: 'Pick a region and connect',
    s2_p: 'We test every endpoint and keep the fastest. If they all fail, you will see it on this screen.',
    s2_ready: 'Ready',
    s3: 'Your exit IP is',
    s3_p: 'Four things people check first. Click one and it opens — proof beats a success message.',
    s3_wait: 'Connect first to see your exit IP.',
    covers: 'This extension routes only this browser. Cursor, Slack, terminals and other apps are not affected.',
  },
  zh: {
    title: '欢迎使用 babel.plus',
    lede: '三步，不用配置。任何一步失败都会在这里说明，不会把你丢进一个悄悄不工作的浏览器。',
    s1: '用购买时的邮箱登录',
    s1_p: '不需要别的 —— 不要姓名、不要卡、不用先建账号。',
    s1_done: '已登录',
    s2: '选一个地区并连接',
    s2_p: '我们会测试每条线路并保留最快的一条。全部失败时，这一屏会告诉你。',
    s2_ready: '就绪',
    s3: '你的出口 IP 是',
    s3_p: '大家最先检查的四样东西。点一下就打开 —— 证据比一句「成功」有用。',
    s3_wait: '先连接，才能看到出口 IP。',
    covers: '本扩展只接管这个浏览器。Cursor、Slack、终端等其他程序不受影响。',
  },
} as const;

const TILES: readonly { readonly label: string; readonly url: string }[] = [
  { label: 'G', url: 'https://www.google.com/' },
  { label: 'YT', url: 'https://www.youtube.com/' },
  { label: 'WA', url: 'https://web.whatsapp.com/' },
  { label: 'AI', url: 'https://chatgpt.com/' },
];

export function Onboarding() {
  const handle = useSnapshot();
  const lang = detectLanguage();
  const t = translator(lang);
  const c = COPY[lang];
  const [region, setRegion] = useState<string | null>(null);
  const s = handle.snapshot;

  useEffect(() => {
    document.title = c.title;
  }, [c.title]);

  if (!s) return <div className="page">…</div>;
  const ui = deriveUiState(s);
  const signedIn = s.signedIn;
  const connected = ui.kind === 'on' || (ui.kind === 'low' && ui.connected);
  const exitIp = s.connection.exitIp;

  return (
    <div className="page">
      <h1>{c.title}</h1>
      <p className="lede">{c.lede}</p>
      <div className="runs">
        <section className={`run${signedIn ? ' run--done' : ''}`} aria-label="Step 1">
          <span className="run__step">Step 1</span>
          <h2 className="run__h">{c.s1}</h2>
          <p className="run__p">{c.s1_p}</p>
          {signedIn ? (
            <p className="banner banner--info" style={{ width: '100%', maxWidth: 320 }}>
              ✓ {c.s1_done}
            </p>
          ) : (
            <SignInForm
              t={t}
              busy={handle.busy}
              error={signInErrorText(t, handle.error ?? s.lastError)}
              onSubmit={(email, password) => void handle.send({ type: 'sign-in', email, password })}
            />
          )}
        </section>

        <section className={`run${!signedIn ? ' run--done' : ''}`} aria-label="Step 2">
          <span className="run__step">Step 2</span>
          <h2 className="run__h">{c.s2}</h2>
          <p className="run__p">{c.s2_p}</p>
          {connected ? (
            <div className="ring" role="status">
              {s.regions.find((r) => r.code === s.connection.region)?.latencyMs ?? '✓'}
              {s.regions.find((r) => r.code === s.connection.region)?.latencyMs !== null ? ' ms' : ''}
            </div>
          ) : null}
          <div className="stack">
            <RegionPick t={t} regions={s.regions} value={region ?? s.prefs.region} disabled={!signedIn} onChange={setRegion} />
            {ui.kind === 'connecting' ? (
              <>
                {s.probes.map((p) => (
                  <Kv key={p.endpointId} k={p.label} v={p.ok ? `${p.latencyMs ?? '—'} ms ✓` : `✗ ${p.error ?? ''}`} />
                ))}
                <Btn tone="wait" onClick={() => void handle.send({ type: 'cancel' })}>
                  {t('cancel')}
                </Btn>
              </>
            ) : ui.kind === 'no-route' ? (
              <>
                <p className="banner banner--bad">
                  <b>{t('no_route_banner')}</b> {s.lastError?.message ?? ''}
                </p>
                <Btn tone="go" disabled={!signedIn || handle.busy} onClick={() => void handle.send({ type: 'connect', region })}>
                  {t('retry')}
                </Btn>
              </>
            ) : connected ? (
              <Kv k={c.s2_ready} v={s.connection.region ?? ''} />
            ) : (
              <Btn tone="go" disabled={!signedIn || handle.busy} onClick={() => void handle.send({ type: 'connect', region })}>
                {t('connect')}
              </Btn>
            )}
          </div>
        </section>

        <section className={`run${!connected ? ' run--done' : ''}`} aria-label="Step 3">
          <span className="run__step">Step 3</span>
          <h2 className="run__h">
            {c.s3} <span className="mono">{connected ? (exitIp ?? '—') : '…'}</span>
          </h2>
          <p className="run__p">{connected ? c.s3_p : c.s3_wait}</p>
          <div className="tiles">
            {TILES.map((tile) => (
              <a key={tile.label} className="tile" href={tile.url} target="_blank" rel="noreferrer noopener" aria-disabled={!connected}>
                {tile.label}
              </a>
            ))}
          </div>
          <p className="hint">{c.covers}</p>
        </section>
      </div>
    </div>
  );
}
