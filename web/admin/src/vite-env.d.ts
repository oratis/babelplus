/// <reference types="vite/client" />

/**
 * ⚠️ `VITE_` 前缀的变量会被打进 bundle。后台的任何凭据、IAP 客户端密钥、
 * 内部主机名都**不能**放这里 —— 构建物是可以被拿到的。
 */
interface ImportMetaEnv {
  readonly VITE_ADMIN_API_BASE_URL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
