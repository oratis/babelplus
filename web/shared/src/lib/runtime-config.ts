/**
 * 运行时配置。
 *
 * 为什么不是构建期常量：ADR 0003 §5 要求「部署流水线支持一键新增镜像域名」，
 * 而备用域名列表**恰恰是域名被封时唯一还能用的东西** —— 如果它是构建期烧进 bundle 的，
 * 新增一个镜像就要重新构建 + 重新部署三套前端，恢复速度直接被拖垮。
 *
 * 为什么不是 `fetch('/runtime-config.json')`：
 * 备用域名列表必须在**首屏、零网络往返**的前提下可见（page-inventory §2.2「网络不可达」态）。
 * 一次额外的 fetch 就是一个可能失败的点，而它失败的时刻恰好是最需要它的时刻。
 * 因此走 `index.html` 里的同步 `<script src="/runtime-config.js">`，
 * 该脚本只做一件事：给 `window.__BP_RUNTIME_CONFIG__` 赋值。
 * 部署时覆盖这一个文件即可，**不重新构建**。
 */
import type { ErrorKind } from './error-kind.ts';

export interface MirrorDomain {
  /** 展示名，例如「主站」「镜像 1」 */
  readonly label: string;
  /** 完整 URL，含协议 */
  readonly url: string;
  /** 可选备注，例如「优先尝试」 */
  readonly note?: string;
}

export interface RuntimeConfig {
  /** API 主域名。system-design §4.1：与 Web 域名池不同源。 */
  readonly apiBaseUrl: string;
  /** API 备用域名池。超时后按顺序重试一次（page-inventory §2.2）。 */
  readonly apiFallbackBaseUrls: readonly string[];
  /** 本前端自己的镜像域名列表 —— 页脚常驻展示位的数据源（ADR 0003 §5）。 */
  readonly mirrorDomains: readonly MirrorDomain[];
  /** 教程站。独立主域名，免登录（page-inventory §3.3）。 */
  readonly docsUrl: string;
  /** 状态页。5xx 时引导过去，**不要求用户提工单**。 */
  readonly statusUrl: string;
  /** 免登录诊断页 `check.*`。用户连不上时打不开面板，这才是排障主入口。 */
  readonly checkUrl: string;
  /** 失联时的最后一条路：邮件（ADR 0002 §1 唯一失联恢复通道）。 */
  readonly supportEmail: string;
  /**
   * 前端请求超时（毫秒）。
   * ⚠️ 15000 是 page-inventory §2.2 的**提案值，需实测**按晚高峰 P95 校准。
   * 常见的 5 秒阈值会在晚高峰把正常请求判死。
   */
  readonly requestTimeoutMs: number;
  /** 慢加载提示条出现的阈值（毫秒）。page-inventory §2.2 定为 3000。 */
  readonly slowHintMs: number;
  /** 骨架屏必须在此时间内出现（毫秒）。本地渲染，不等 API。仅作为自检用的声明值。 */
  readonly skeletonBudgetMs: number;
  /** 这份配置的生成时间，便于排查「部署了没生效」。 */
  readonly configuredAt?: string;
}

declare global {
  interface Window {
    __BP_RUNTIME_CONFIG__?: Partial<RuntimeConfig>;
  }
}

/**
 * 兜底默认值。**故意不含任何真实域名** ——
 * 编不出来的东西就不编（AGENTS.md）。域名池尚未注册，见 page-inventory §8。
 * 缺失时 UI 会显示「未配置」而不是显示一个假的域名把用户导到错误地址。
 */
const BASELINE: RuntimeConfig = {
  apiBaseUrl: '',
  apiFallbackBaseUrls: [],
  mirrorDomains: [],
  docsUrl: '',
  statusUrl: '',
  checkUrl: '',
  supportEmail: '',
  requestTimeoutMs: 15_000,
  slowHintMs: 3_000,
  skeletonBudgetMs: 200,
};

let resolved: RuntimeConfig | null = null;

function pickStrings(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const out = value.filter((v): v is string => typeof v === 'string' && v.length > 0);
  return out;
}

function pickMirrors(value: unknown): MirrorDomain[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const out: MirrorDomain[] = [];
  for (const item of value) {
    if (typeof item !== 'object' || item === null) continue;
    const rec = item as Record<string, unknown>;
    if (typeof rec['label'] !== 'string' || typeof rec['url'] !== 'string') continue;
    const note = typeof rec['note'] === 'string' ? rec['note'] : undefined;
    out.push(note === undefined ? { label: rec['label'], url: rec['url'] } : { label: rec['label'], url: rec['url'], note });
  }
  return out;
}

/**
 * 读取运行时配置。第一次调用后缓存 —— 配置在一次页面生命周期内不变。
 *
 * 合并顺序：`BASELINE` ← 应用传入的 `defaults`（构建期 env，可选）← `window.__BP_RUNTIME_CONFIG__`。
 * 运行时值优先级最高，这样运维改一个 `runtime-config.js` 就能生效。
 */
export function initRuntimeConfig(defaults: Partial<RuntimeConfig> = {}): RuntimeConfig {
  const injected: Partial<RuntimeConfig> =
    (typeof window !== 'undefined' && window.__BP_RUNTIME_CONFIG__) || {};

  const merged: Record<string, unknown> = { ...BASELINE, ...defaults };
  for (const [key, value] of Object.entries(injected)) {
    if (value === undefined || value === null) continue;
    if (key === 'mirrorDomains') {
      const mirrors = pickMirrors(value);
      if (mirrors) merged[key] = mirrors;
      continue;
    }
    if (key === 'apiFallbackBaseUrls') {
      const urls = pickStrings(value);
      if (urls) merged[key] = urls;
      continue;
    }
    merged[key] = value;
  }

  resolved = merged as unknown as RuntimeConfig;
  return resolved;
}

/** 取已初始化的配置。未初始化时按 BASELINE 惰性初始化，绝不抛错 —— 配置读不到不该炸掉整个页面。 */
export function runtimeConfig(): RuntimeConfig {
  return resolved ?? initRuntimeConfig();
}

/** 仅测试/调试用：清空缓存。 */
export function resetRuntimeConfig(): void {
  resolved = null;
}

/**
 * 「网络不可达」态要显示的那句话。写死在这里而不是让每个页面自己编，
 * 因为它是一句**产品承诺**：控制面故障不得被用户理解为数据面故障（system-design §1）。
 */
export const OFFLINE_REASSURANCE =
  '你的订阅仍然有效，已连接的设备不受影响。这里只是面板暂时打不开。';

/** 五类错误各自的标题与说明。集中放这里，避免每页各写一套文案。 */
export const ERROR_COPY: Record<ErrorKind, { title: string; description: string }> = {
  unauthorized: {
    title: '需要重新登录',
    description: '登录状态已过期。重新登录后回到这一页继续。',
  },
  forbidden: {
    title: '没有访问权限',
    description: '当前账号不能查看这一页。如果你认为这是错误，请提交工单。',
  },
  client: {
    title: '这个请求没能完成',
    description: '请求本身有问题，重试通常没用。换个条件再试，或把下面的请求号发给我们。',
  },
  server: {
    title: '我们这边出了问题',
    description: '不是你的账号或网络的问题。查看状态页了解当前情况，恢复后会自动可用。',
  },
  offline: {
    title: '连不上面板',
    description: '当前网络到面板的连接失败。可以试试下面的备用域名。',
  },
};
