/**
 * `returnTo` 的构造与校验。
 *
 * 这个文件的全部理由是一句话：**未登录跳登录页要带上「回哪去」，而「回哪去」是用户可控输入。**
 * 路由守卫会把当前地址塞进查询串，登录成功后再跳回去 —— 如果不校验，
 * `?returnTo=https://evil.example/` 就是一个开放重定向，
 * 而这个面板的用户群（翻墙工具用户）恰好是最会被钓鱼的一群：
 * 钓鱼链接的前半截是我们**真实的**登录域名，用户核对域名这一步会通过。
 *
 * 校验采用白名单式：只接受「以单个 `/` 开头的站内路径」，其余一律丢弃回默认页。
 * 不做「把外链改写成站内链接」这种修复 —— 修复一个恶意输入等于给攻击者留一条协商通道。
 */

/** 查询参数名。前端各处必须用同一个常量，别处再写一次字面量就会出现拼写漂移。 */
export const RETURN_TO_PARAM = 'returnTo';

/**
 * 明确**不**接受作为 returnTo 的路径前缀。
 *
 * 认证四页自己就是「未登录才能看」的页面，让它们进 returnTo 只会造出
 * 「登录成功 → 跳回登录页 → 已登录 → 再跳走」的来回，用户看到一次闪烁。
 */
const DENIED_PREFIXES = ['/auth/'];

/**
 * 校验并归一化 returnTo。不合格返回 `null`（调用方自己决定默认落点）。
 *
 * 逐条拒绝的理由：
 *  - 不以 `/` 开头 —— `https://evil.example`、`javascript:…`、`data:…` 全在这一条里被挡掉；
 *  - 以 `//` 开头 —— 协议相对 URL，`//evil.example` 会被浏览器解析成外站；
 *  - 第二个字符是 `\` —— 多数浏览器把 `/\` 与 `//` 等价处理，这是绕过上一条的经典写法；
 *  - 含反斜杠 / 控制字符 / 空白 —— 归一化差异是这类校验最常见的绕过面，直接不接受；
 *  - 解析后 origin 与当前页不同 —— 兜底断言，前面几条漏了也还有这一层。
 */
export function safeReturnTo(raw: string | null | undefined, origin?: string): string | null {
  if (typeof raw !== 'string' || raw.length === 0) return null;
  if (raw.length > 2048) return null;
  if (!raw.startsWith('/')) return null;
  if (raw.startsWith('//')) return null;
  if (raw.includes('\\')) return null;
  // eslint 风格的字符类在这里不合适：要挡的是**全部** C0/C1 控制字符与空白。
  for (const ch of raw) {
    const code = ch.codePointAt(0) ?? 0;
    if (code <= 0x20 || code === 0x7f || (code >= 0x80 && code <= 0x9f)) return null;
  }
  // 兜底：真的解析一遍。base 只用于解析，结果只取路径部分，绝不把 origin 拼回去。
  const base = origin ?? currentOrigin();
  let parsed: URL;
  try {
    parsed = new URL(raw, base);
  } catch {
    return null;
  }
  if (parsed.origin !== new URL(base).origin) return null;

  // 黑名单判在**解析后的 pathname** 上：判在原串上的话 `/auth?x=1`、`/auth/../auth/login`
  // 这类写法会绕过去，而它们最终都落在同一个页面。
  const path = parsed.pathname;
  for (const prefix of DENIED_PREFIXES) {
    const bare = prefix.replace(/\/$/, '');
    if (path === bare || path.startsWith(prefix)) return null;
  }

  return `${path}${parsed.search}${parsed.hash}`;
}

/** 当前页面的 origin。非浏览器环境（测试、将来的 SSR）给一个不会被用到的占位。 */
function currentOrigin(): string {
  if (typeof window !== 'undefined' && window.location?.origin) return window.location.origin;
  return 'http://localhost';
}

/**
 * 拼出「跳登录并记住来路」的地址。
 *
 * `from` 传的是当前地址（`pathname + search + hash`）。校验不过就不带 returnTo ——
 * 带一个已知非法的值过去，只会把校验责任推给下一个人。
 */
export function loginUrlWithReturnTo(loginPath: string, from: string | null | undefined): string {
  const safe = safeReturnTo(from);
  if (safe === null) return loginPath;
  const sep = loginPath.includes('?') ? '&' : '?';
  return `${loginPath}${sep}${RETURN_TO_PARAM}=${encodeURIComponent(safe)}`;
}
