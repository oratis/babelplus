import { defineConfig, loadEnv, type Plugin } from 'vite';
import { linksFromEnv } from './src/content.ts';
import { renderPage } from './src/render.ts';

/**
 * 构建期把 `content.ts` 渲染进 `index.html` 的 `<!--PAGE-->` 占位。
 *
 * 🔴 产出是**零 JavaScript 的静态页**，理由写在 src/render.ts 头部：这一页要在
 * 「人已经在中国、还没有可用代理」的那一刻打开，多一个可能失败的东西就多一分打不开的概率。
 */
function renderAtBuild(env: Record<string, string | undefined>): Plugin {
  return {
    name: 'bp-render-at-build',
    transformIndexHtml: {
      order: 'pre',
      handler(html) {
        return html.replace('<!--PAGE-->', renderPage({ links: linksFromEnv(env) }));
      },
    },
  };
}

export default defineConfig(({ mode }) => ({
  // 变量来源有两处：`.env.production`（部署脚本写进构建上下文）与进程环境（本机开发）。
  // 两处都空时页面把那几个链接渲染成不可点，**不编域名**。
  plugins: [renderAtBuild({ ...loadEnv(mode, process.cwd(), 'VITE_'), ...process.env })],
  build: {
    outDir: 'dist',
    cssCodeSplit: false,
    assetsInlineLimit: 8192,
    // 🔴 这个上限刻意压低：超了就该问「官网为什么需要这么多 JS」，而不是把上限调高。
    chunkSizeWarningLimit: 60,
    sourcemap: false,
  },
}));
