/*
 * 用户面板运行时配置 —— 部署时覆盖这一个文件，**不需要重新构建**。
 *
 * 为什么存在：ADR 0003 §5「部署流水线支持一键新增镜像域名」。
 * 域名被封时的恢复速度取决于「改一个文件」还是「重新构建 + 重新部署三套前端」。
 *
 * 部署要求：
 *   1. 以 `Cache-Control: no-cache` 下发（改了域名池但用户拿旧缓存 = 白改）
 *   2. 与 index.html 同源同域，不要放 CDN
 *   3. 值全部留空时，UI 会显示「备用域名尚未配置」而不是显示假域名 —— 这是有意的
 *
 * ⚠️ 下面的值**故意全部为空**：域名池尚未注册（page-inventory §8 登记的未决项）。
 *    编不出来的东西就不编。填上假域名会把失联的用户导向一个不存在的地址。
 */
window.__BP_RUNTIME_CONFIG__ = {
  /* API 主域名，例如 "https://api.example.com"。空串表示与页面同源。 */
  apiBaseUrl: '',

  /* API 备用域名池。GET 类请求超时后按顺序重试一次（page-inventory §2.2）。 */
  apiFallbackBaseUrls: [],

  /* 本面板自己的镜像域名 —— 页脚常驻展示位的数据源。 */
  /* 形如：{ label: "主站", url: "https://panel.example.com", note: "优先" } */
  mirrorDomains: [],

  /* 教程站（独立主域名，免登录）。 */
  docsUrl: '',

  /* 状态页。5xx 时引导过去，不要求用户提工单。 */
  statusUrl: '',

  /* 免登录网络诊断页 check.*。用户连不上时打不开面板，这才是排障主入口。 */
  checkUrl: '',

  /* 失联恢复通道（ADR 0002：邮件是唯一的那条）。 */
  supportEmail: '',

  /* ⚠️ 15000 是 page-inventory §2.2 的提案值，需按晚高峰三网 P95 实测校准。 */
  requestTimeoutMs: 15000,

  /* 慢加载提示条阈值。 */
  slowHintMs: 3000,

  configuredAt: '',
};
