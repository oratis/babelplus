/**
 * 页脚。**备用域名列表是常驻展示位，不是可折叠的、不是只在出错时才出现的。**
 * ADR 0003 §5：「页面底部固定展示备用域名列表（用户遇到问题时第一眼能看到）」。
 * page-inventory §3.2.2 把它列为 dashboard 的硬要求，§3.2.1 要求登录页页脚也常驻。
 *
 * 所以它被做成布局级组件而不是页面级组件 —— 想漏掉都漏不掉。
 */
import { runtimeConfig } from '../lib/runtime-config.ts';
import { MirrorDomainList } from './states.tsx';
import { Icon } from './icons.tsx';
import { Eyebrow } from './primitives.tsx';

export function SiteFooter({ compact = false }: { compact?: boolean }) {
  const cfg = runtimeConfig();

  return (
    <footer className="mt-10 border-t border-line px-4 py-6 text-sm sm:px-6">
      <div className="mx-auto max-w-5xl">
        <Eyebrow className="mb-2">Recovery · 备用入口</Eyebrow>
        <MirrorDomainList />

        {!compact && (
          <nav className="mt-4 flex flex-wrap gap-x-5 gap-y-2 font-mono text-[11px] tracking-wide text-fg-muted">
            {cfg.docsUrl ? (
              <a href={cfg.docsUrl} className="inline-flex items-center gap-1 hover:text-fg" rel="noreferrer noopener">
                教程与排障 <Icon.External size={12} />
              </a>
            ) : null}
            {cfg.statusUrl ? (
              <a href={cfg.statusUrl} className="inline-flex items-center gap-1 hover:text-fg" rel="noreferrer noopener">
                服务状态 <Icon.External size={12} />
              </a>
            ) : null}
            {cfg.checkUrl ? (
              <a href={cfg.checkUrl} className="inline-flex items-center gap-1 hover:text-fg" rel="noreferrer noopener">
                网络诊断 <Icon.External size={12} />
              </a>
            ) : null}
          </nav>
        )}

        <p className="mt-4 text-xs leading-relaxed text-fg-subtle">
          教程与排障文档在<strong className="font-medium text-fg-muted">另一个域名</strong>上，免登录 ——
          面板打不开时它仍然可用。
        </p>
      </div>
    </footer>
  );
}
