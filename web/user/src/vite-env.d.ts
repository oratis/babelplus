/// <reference types="vite/client" />

/**
 * 构建期兜底配置。真正生效的是 `/runtime-config.js`（运行时可改，不用重新构建）。
 * 这里只声明类型，值放 `.env.local` 或 CI 的构建变量里。
 * ⚠️ `VITE_` 前缀的变量会被打进 bundle，**任何凭据都不能放这里**。
 */
interface ImportMetaEnv {
  readonly VITE_API_BASE_URL?: string;
  readonly VITE_DOCS_URL?: string;
  readonly VITE_STATUS_URL?: string;
  readonly VITE_CHECK_URL?: string;
  readonly VITE_SUPPORT_EMAIL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
