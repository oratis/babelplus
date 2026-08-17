import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import { initRuntimeConfig } from '@babelplus/shared';
import { App } from './App.tsx';
import './styles.css';

/**
 * 运行时配置必须在渲染前初始化 —— 页脚的备用域名列表在首屏就要正确。
 *
 * `import.meta.env` 里的值只是**构建期兜底**，运行时 `/runtime-config.js` 的值优先。
 * 顺序反过来（构建期覆盖运行时）会让「改一个文件就能加镜像域名」这条失效。
 */
initRuntimeConfig({
  apiBaseUrl: import.meta.env.VITE_API_BASE_URL ?? '',
  docsUrl: import.meta.env.VITE_DOCS_URL ?? '',
  statusUrl: import.meta.env.VITE_STATUS_URL ?? '',
  checkUrl: import.meta.env.VITE_CHECK_URL ?? '',
  supportEmail: import.meta.env.VITE_SUPPORT_EMAIL ?? '',
});

const container = document.getElementById('root');
if (!container) throw new Error('#root 不存在：index.html 被改坏了');

createRoot(container).render(
  <StrictMode>
    {/*
      用 BrowserRouter 而不是 HashRouter：竞品的 `#/dashboard` 形态让
      `/auth/reset?token=` 这类邮件落地链接变得别扭，且 hash 不会随请求发给服务端，
      未来做服务端渲染或边缘重定向时会挡路。
      ⚠️ 部署要求：静态托管必须配置 SPA fallback（一切 404 回 index.html），
      否则用户刷新 /order/xxx 会拿到 404。
    */}
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
);
