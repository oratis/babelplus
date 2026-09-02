/// <reference types="vite/client" />

/**
 * 构建期兜底配置。**只是兜底**：真正的域名池由 `/api/v1/user/proxy-config` 的 `control_plane` 在运行时下发，
 * 这些值只用于「第一次登录之前」与「配置从未拉到过」两种时刻。都可为空 —— 为空时 popup 显示「未配置」，
 * 不显示一个编出来的域名（AGENTS.md §3：编一个值比留空危害大得多）。
 */
interface ImportMetaEnv {
  /** API 主域名，含协议。 */
  readonly VITE_BP_API_BASE_URL?: string;
  /** API 备用域名，逗号分隔。 */
  readonly VITE_BP_API_FALLBACK_URLS?: string;
  /** 用户面板（Top up / Renew 跳这里）。 */
  readonly VITE_BP_WEB_URL?: string;
  /** 备用域名页（「全部端点不可达」态的出口）。 */
  readonly VITE_BP_BACKUP_PAGE_URL?: string;
  /** 帮助 / 教程站。 */
  readonly VITE_BP_HELP_URL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
