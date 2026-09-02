import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const shared = (p: string) => fileURLToPath(new URL(`../shared/${p}`, import.meta.url));

/**
 * 浏览器扩展（MV3）的构建配置。**一次构建出四个入口**：三个页面（popup / options / onboarding）
 * 与一个 service worker（background）。`dist/` 就是商店提交的解包目录 —— `public/` 里的
 * manifest.json、图标与 `_locales` 原样落到 dist 根。
 *
 * 两条与 SPA 不同、且踩过就会白屏的约束：
 *  1. `modulePreload.polyfill` 必须关：Vite 默认把 preload polyfill 注进每个入口，它引用 `document`，
 *     service worker 里没有 `document`，SW 会在第一行就抛错 —— 扩展装上后什么都不响应，且没有可见报错。
 *  2. background 的产物名必须稳定（manifest 里写死 `background.js`），页面入口才用带 hash 的名字。
 *
 * 没有 runtime-config.js：扩展页面不是从我们的域名加载的，部署时覆盖文件这条路不存在。
 * API 域名池走两层：构建期 `VITE_BP_*`（兜底）→ 运行时由 `/user/proxy-config` 的 `control_plane` 更新。
 */
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: [
      { find: /^@babelplus\/shared\/ui$/, replacement: shared('src/ui/index.ts') },
      { find: /^@babelplus\/shared\/api$/, replacement: shared('api/index.ts') },
      { find: /^@babelplus\/shared$/, replacement: shared('src/index.ts') },
    ],
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'chrome108',
    sourcemap: false,
    modulePreload: { polyfill: false },
    chunkSizeWarningLimit: 400,
    rollupOptions: {
      input: {
        popup: fileURLToPath(new URL('popup.html', import.meta.url)),
        options: fileURLToPath(new URL('options.html', import.meta.url)),
        onboarding: fileURLToPath(new URL('onboarding.html', import.meta.url)),
        background: fileURLToPath(new URL('src/background/index.ts', import.meta.url)),
      },
      output: {
        entryFileNames: (chunk) => (chunk.name === 'background' ? 'background.js' : 'assets/[name]-[hash].js'),
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
  server: {
    fs: { allow: ['..'] },
  },
});
