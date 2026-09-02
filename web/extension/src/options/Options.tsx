/**
 * options 页：mockup 里的四个开关 + 一个诊断按钮，一页到底。
 * 「Off」那一段不是路由模式，是断开（types.ts 的 RoutingMode 注释）。
 * mockup 里的「Share anonymous connection reports」**没有做**：v1 没有任何上报通道，
 * 一个存起来但什么都不做的开关是对用户撒谎，所以不放。
 */
import { useEffect, useState } from 'react';
import { detectLanguage, translator } from '../shared/i18n.ts';
import type { RoutingMode } from '../shared/types.ts';
import { useSnapshot } from '../ui/useSnapshot.ts';

const COPY = {
  en: {
    title: 'babel.plus — Options',
    lede: 'Default routing is proxy unless told otherwise: more of the web is broken here than works.',
    routing: 'Routing',
    routing_d: 'Smart sends blocked sites through us and keeps Chinese sites direct, so local services stay fast.',
    smart: 'Smart',
    everything: 'Everything',
    off: 'Off',
    always: 'Always route these sites',
    always_d: 'One host per line. Added automatically when you accept a “route it” prompt.',
    never: 'Never route these sites',
    never_d: 'Banking and government sites often refuse foreign addresses.',
    save: 'Save',
    saved: 'Saved',
    hosts: 'hosts',
    startup: 'Connect when the browser starts',
    startup_d: 'Reconnects to your last region without opening the popup.',
    diag: 'Copy diagnostics',
    diag_d: 'Endpoint probe results, version, last config refresh. Paste it into a ticket. Contains no credentials, no addresses, no page URLs.',
    copy: 'Copy',
    copied: 'Copied',
    signout: 'Sign out',
    signout_d: 'Clears the session and proxy settings on this browser.',
    version: 'Version',
    not_signed_in: 'Not signed in. Open the toolbar popup to sign in.',
  },
  zh: {
    title: 'babel.plus — 选项',
    lede: '默认走代理，除非你另有指定：在这里打不开的站点比打得开的多。',
    routing: '路由',
    routing_d: 'Smart 让被屏蔽的站点走我们，中国站点直连，本地服务保持快。',
    smart: 'Smart',
    everything: '全部',
    off: '关闭',
    always: '一律走代理的站点',
    always_d: '一行一个主机名。接受「走代理」提示时会自动加进来。',
    never: '一律直连的站点',
    never_d: '银行与政务站点常拒绝境外地址。',
    save: '保存',
    saved: '已保存',
    hosts: '个',
    startup: '浏览器启动时自动连接',
    startup_d: '不打开弹窗也会按上次的地区重连。',
    diag: '复制诊断信息',
    diag_d: '线路探测结果、版本、上次配置刷新时间。贴进工单即可。不含凭据、地址与页面 URL。',
    copy: '复制',
    copied: '已复制',
    signout: '退出登录',
    signout_d: '清除这个浏览器上的会话与代理设置。',
    version: '版本',
    not_signed_in: '未登录。打开工具栏弹窗登录。',
  },
} as const;

export function Options() {
  const handle = useSnapshot();
  const lang = detectLanguage();
  const t = translator(lang);
  const c = COPY[lang];
  const [always, setAlways] = useState('');
  const [never, setNever] = useState('');
  const [saved, setSaved] = useState<'always' | 'never' | null>(null);
  const [copied, setCopied] = useState(false);
  const s = handle.snapshot;

  useEffect(() => {
    if (!s) return;
    setAlways(s.prefs.alwaysProxy.join('\n'));
    setNever(s.prefs.neverProxy.join('\n'));
  }, [s?.prefs.alwaysProxy, s?.prefs.neverProxy, s]);

  useEffect(() => {
    document.title = c.title;
  }, [c.title]);

  if (!s) return <div className="page">…</div>;

  const setMode = (mode: RoutingMode) => void handle.send({ type: 'set-prefs', prefs: { mode } });
  const saveList = async (which: 'always' | 'never') => {
    const value = which === 'always' ? always : never;
    const list = value.split(/\r?\n/);
    await handle.send({ type: 'set-prefs', prefs: which === 'always' ? { alwaysProxy: list } : { neverProxy: list } });
    setSaved(which);
    setTimeout(() => setSaved(null), 1500);
  };
  const copy = async () => {
    const res = await handle.send({ type: 'diagnostics' });
    if (res.ok && res.text) {
      await navigator.clipboard.writeText(res.text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    }
  };
  const connected = s.connection.status === 'on';

  return (
    <div className="page">
      <h1>{c.title}</h1>
      <p className="lede">{c.lede}</p>
      {!s.signedIn ? <p className="banner banner--info">{c.not_signed_in}</p> : null}
      <div className="sheet">
        <div className="row">
          <div className="row__l">
            <span className="row__t">{c.routing}</span>
            <span className="row__d">{c.routing_d}</span>
          </div>
          <div className="seg" role="group" aria-label={c.routing}>
            <button type="button" aria-pressed={s.prefs.mode === 'smart'} onClick={() => setMode('smart')}>
              {c.smart}
            </button>
            <button type="button" aria-pressed={s.prefs.mode === 'everything'} onClick={() => setMode('everything')}>
              {c.everything}
            </button>
            <button type="button" aria-pressed={!connected} onClick={() => void handle.send({ type: 'disconnect' })}>
              {c.off}
            </button>
          </div>
        </div>
        <div className="row">
          <div className="row__l">
            <span className="row__t">{c.always}</span>
            <span className="row__d">{c.always_d}</span>
            <textarea className="list" aria-label={c.always} value={always} onChange={(e) => setAlways(e.currentTarget.value)} />
          </div>
          <button type="button" className="small-btn" onClick={() => void saveList('always')}>
            {saved === 'always' ? c.saved : c.save}
          </button>
        </div>
        <div className="row">
          <div className="row__l">
            <span className="row__t">{c.never}</span>
            <span className="row__d">{c.never_d}</span>
            <textarea className="list" aria-label={c.never} value={never} onChange={(e) => setNever(e.currentTarget.value)} />
          </div>
          <button type="button" className="small-btn" onClick={() => void saveList('never')}>
            {saved === 'never' ? c.saved : c.save}
          </button>
        </div>
        <div className="row">
          <div className="row__l">
            <span className="row__t">{c.startup}</span>
            <span className="row__d">{c.startup_d}</span>
          </div>
          <button
            type="button"
            className="sw"
            role="switch"
            aria-checked={s.prefs.autoConnect}
            aria-label={c.startup}
            onClick={() => void handle.send({ type: 'set-prefs', prefs: { autoConnect: !s.prefs.autoConnect } })}
          />
        </div>
        <div className="row">
          <div className="row__l">
            <span className="row__t">{c.diag}</span>
            <span className="row__d">{c.diag_d}</span>
          </div>
          <button type="button" className="small-btn" onClick={() => void copy()}>
            {copied ? c.copied : c.copy}
          </button>
        </div>
        {s.signedIn ? (
          <div className="row">
            <div className="row__l">
              <span className="row__t">{c.signout}</span>
              <span className="row__d">{c.signout_d}</span>
            </div>
            <button type="button" className="small-btn" onClick={() => void handle.send({ type: 'sign-out' })}>
              {t('sign_out')}
            </button>
          </div>
        ) : null}
        <div className="row">
          <div className="row__l">
            <span className="row__t">{c.version}</span>
          </div>
          <span className="mono">{s.version}</span>
        </div>
      </div>
    </div>
  );
}
