/*
 * 后台运行时配置。部署时覆盖，不重新构建。
 *
 * 与用户面板的关键差异：**没有 mirrorDomains，也没有 apiFallbackBaseUrls**。
 * 后台的大陆可达性要求是「不要求」（page-inventory §4.1）——
 * 给后台配镜像等于凭空多开几个需要 IP 白名单 / IAP 防护的入口。
 *
 * 🔴 这个文件里**不能**出现任何凭据、内部主机名或 IAP 配置。
 *    它是公开可读的静态资源，浏览器能拿到的东西攻击者也能拿到。
 */
window.__BP_RUNTIME_CONFIG__ = {
  /* 管理面 API 域名。必须是与用户面 API 不同的主域名（openapi.yaml servers[1]）。 */
  apiBaseUrl: '',

  apiFallbackBaseUrls: [],
  mirrorDomains: [],

  docsUrl: '',
  statusUrl: '',
  checkUrl: '',
  supportEmail: '',

  requestTimeoutMs: 15000,
  slowHintMs: 3000,

  configuredAt: '',
};
