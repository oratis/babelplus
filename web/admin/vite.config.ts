import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

const shared = (p: string) => fileURLToPath(new URL(`../shared/${p}`, import.meta.url));

/**
 * 后台（bp-admin）的构建配置。
 *
 * 🔴 **产物必须独立**：`web/admin/dist` 部署到与用户面板**不同的主域名**，
 * 前面加 IP 白名单 / GCP IAP，应用层再叠强制 TOTP（page-inventory §4.1 三道闸）。
 * 同源部署会让这三道闸同时失效，且用户面板的 XSS 会直接变成后台失守。
 * 后台的**大陆可达性是「不要求」** —— 可达性预算全部留给用户面板与文档站。
 */
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: [
      { find: /^@babelplus\/shared\/ui$/, replacement: shared('src/ui/index.ts') },
      { find: /^@babelplus\/shared\/api$/, replacement: shared('api/index.ts') },
      { find: /^@babelplus\/shared$/, replacement: shared('src/index.ts') },
    ],
  },
  build: {
    outDir: 'dist',
    chunkSizeWarningLimit: 700,
    sourcemap: true,
  },
  server: {
    port: 5174,
    fs: {
      allow: ['..'],
    },
  },
});
