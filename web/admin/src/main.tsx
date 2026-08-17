import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import { initRuntimeConfig } from '@babelplus/shared';
import { App } from './App.tsx';
import './styles.css';

/**
 * 后台的运行时配置里**没有 mirrorDomains** —— 后台的大陆可达性要求是「不要求」
 * （page-inventory §4.1），可达性预算全部留给用户面板与文档站。
 * 给后台配镜像域名等于凭空多开几个需要防护的入口。
 */
initRuntimeConfig({
  apiBaseUrl: import.meta.env.VITE_ADMIN_API_BASE_URL ?? '',
  mirrorDomains: [],
  apiFallbackBaseUrls: [],
});

const container = document.getElementById('root');
if (!container) throw new Error('#root 不存在：index.html 被改坏了');

createRoot(container).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
);
