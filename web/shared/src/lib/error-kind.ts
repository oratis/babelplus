/**
 * 错误分类。page-inventory §2.2 硬要求：**必须区分五类**，文案与动作各不相同。
 * 「把『你的账号过期了』和『我们的服务器挂了』显示成同一句话，会直接变成工单。」
 *
 * 这个枚举被 API 客户端与 UI 组件共用，所以单独放一个无依赖的文件，避免 api ↔ ui 循环引用。
 */
export type ErrorKind =
  /** 401 —— 未登录 / 会话过期 */
  | 'unauthorized'
  /** 403 —— 已登录但无权限（后台 IAP、用户被封禁） */
  | 'forbidden'
  /** 4xx（除 401/403）—— 请求本身有问题，重试没用 */
  | 'client'
  /** 5xx —— 服务端故障，引导到 status.*，**不要求用户提工单** */
  | 'server'
  /** 网络不可达 / 超时 —— 展示静态内嵌备用域名列表，且必须说明数据面不受影响 */
  | 'offline';

/** HTTP 状态码 → ErrorKind。`status = 0` 约定为「请求没走到服务端」。 */
export function errorKindFromStatus(status: number): ErrorKind {
  if (status === 0) return 'offline';
  if (status === 401) return 'unauthorized';
  if (status === 403) return 'forbidden';
  if (status >= 500) return 'server';
  if (status >= 400) return 'client';
  // 2xx/3xx 走到这里说明调用方用错了，按 client 处理而不是静默。
  return 'client';
}
