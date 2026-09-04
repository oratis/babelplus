/**
 * 完整 sing-box 配置的组装：订阅的出站 + **我们自己的入站与路由规则**。
 *
 * 订阅里既没有 `inbounds` 也没有 `route.rules`（roadmap B45）。B45 那条注释说
 * 「要么真机验证 tun，要么改成让客户端自带模板」—— 浏览器走的正是后者，而且更简单：
 * 我们要的不是 tun（那是接管整机流量，spec §4.4 明确不做），是一个**只监听回环的 mixed 入站**，
 * Electron 的 `session.setProxy` 指过去即可。
 *
 * 生成的配置在启动前由随包 sing-box 自己 `check` 一遍（`core.ts`）：
 * schema 是上游的自由，我们不假装知道它每个版本长什么样，**让二进制自己说了算**。
 */
import { singboxRules, type RuleInput } from './routing.ts';
import type { ParsedSubscription } from './subscription.ts';

/** 我们加的 direct 出站 tag。加前缀避免与订阅里的节点名撞车。 */
export const DIRECT_TAG = 'bp-direct';
/** 我们加的入站 tag。 */
export const INBOUND_TAG = 'bp-mixed-in';

export interface BuildConfigInput {
  readonly subscription: ParsedSubscription;
  readonly port: number;
  readonly rules: RuleInput;
  /** 用户选定的出口节点 tag；null / 不在订阅里 = 用订阅的选择器。 */
  readonly outbound: string | null;
  /** sing-box 日志级别。默认 warn —— info 会把每个连接的域名写进日志，那是不该落盘的东西。 */
  readonly logLevel?: 'trace' | 'debug' | 'info' | 'warn' | 'error';
}

export interface BuiltConfig {
  readonly config: Record<string, unknown>;
  /** 实际生效的代理出站 tag（选择器或用户选的节点），界面显示用。 */
  readonly proxyTag: string;
}

export function buildConfig(input: BuildConfigInput): BuiltConfig {
  const { subscription, port } = input;
  if (!Number.isInteger(port) || port <= 0 || port > 65535) {
    throw new Error(`入站端口不合法：${port}`);
  }

  const tags = new Set(
    subscription.outbounds.map((o) => o['tag']).filter((t): t is string => typeof t === 'string'),
  );
  // 用户选的节点不在订阅里（订阅换了、节点下线了）→ 退回选择器，**不报错**：
  // 一个已经不存在的偏好不该让人连不上网。
  const proxyTag = input.outbound && tags.has(input.outbound) ? input.outbound : subscription.selectorTag;

  const config: Record<string, unknown> = {
    log: { level: input.logLevel ?? 'warn', timestamp: true },
    inbounds: [
      {
        type: 'mixed',
        tag: INBOUND_TAG,
        // 🔴 只监听回环。监听 0.0.0.0 会让同一网络里的任何人白嫖这个用户的配额，
        // 而酒店 / 咖啡馆 WiFi 正是本产品的主场景。
        listen: '127.0.0.1',
        listen_port: port,
        // 本机入站不设认证：Chrome 对 SOCKS5 不支持认证（spec §1 边界 ②），
        // 而端口只对回环开放，认证在这里买不到任何东西。
      },
    ],
    outbounds: [...subscription.outbounds, { type: 'direct', tag: DIRECT_TAG }],
    route: {
      rules: singboxRules(input.rules, DIRECT_TAG, proxyTag),
      // final 指向代理 = 默认走代理（spec §3.4）。
      final: proxyTag,
      auto_detect_interface: true,
    },
  };

  return { config, proxyTag };
}

/** 落盘用的文本。缩进两格是给排障的人看的 —— 这份文件会被贴进工单。 */
export function serializeConfig(config: Record<string, unknown>): string {
  return `${JSON.stringify(config, null, 2)}\n`;
}

/**
 * 配置里含节点凭据（uuid / password / reality 公钥）。**诊断报告不许带它。**
 * 这个函数把配置脱敏成可以贴进工单的形状：保留结构与 tag，抹掉一切像凭据的值。
 */
export function redactConfig(config: Record<string, unknown>): Record<string, unknown> {
  const SECRET_KEYS = new Set([
    'uuid',
    'password',
    'public_key',
    'private_key',
    'short_id',
    'method',
    'server',
    'server_name',
  ]);
  const walk = (v: unknown): unknown => {
    if (Array.isArray(v)) return v.map(walk);
    if (typeof v === 'object' && v !== null) {
      const out: Record<string, unknown> = {};
      for (const [k, val] of Object.entries(v)) {
        out[k] = SECRET_KEYS.has(k) ? '<redacted>' : walk(val);
      }
      return out;
    }
    return v;
  };
  return walk(config) as Record<string, unknown>;
}
