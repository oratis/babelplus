import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

const shared = (p: string) => fileURLToPath(new URL(`../shared/${p}`, import.meta.url));

/**
 * 用户面板（bp-web）的构建配置。
 *
 * **构建产物与后台完全分开**：`web/user/dist` 与 `web/admin/dist` 是两个目录、两次构建、
 * 两个部署目标、两个域名池。后台要独立域名 + IP 白名单 + IAP（page-inventory §4.1），
 * 同源就等于把那三道闸全部作废。
 */
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    // 用数组形式而不是对象：对象是前缀匹配，`@babelplus/shared` 会把 `/ui` `/api` 一起吃掉。
    alias: [
      { find: /^@babelplus\/shared\/ui$/, replacement: shared('src/ui/index.ts') },
      { find: /^@babelplus\/shared\/api$/, replacement: shared('api/index.ts') },
      { find: /^@babelplus\/shared$/, replacement: shared('src/index.ts') },
    ],
  },
  build: {
    outDir: 'dist',
    // 大陆跨境链路每天有 5 小时以上低于 1 Mbps（ADR 0003 §4）。
    // 体积是可达性问题，不只是性能问题 —— 超过这个数就该来看一眼为什么。
    chunkSizeWarningLimit: 400,
    sourcemap: true,
  },
  server: {
    port: 5173,
    fs: {
      // shared 在 vite root 之外，dev 时需要显式放行。
      allow: ['..'],
    },
  },
});
