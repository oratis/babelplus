/**
 * 订阅解析：把会员中心下发的 sing-box JSON 拆成「出站列表 + 默认选择器」。
 *
 * 🔴 **这里只读不改**。订阅是服务端的既成产物（`api/internal/subgen/singbox.go`），
 * 客户端擅自改写出站参数 = 两处实现漂移，而漂移的现象是「换了个客户端就连不上」。
 * 我们只做三件事：验形状、找选择器、把地区列表抽出来给界面。
 *
 * 已知形状（subgen 实现，2026-09-02 读）：
 *   { log, outbounds: [ {type:"selector", tag, outbounds:[...], default}, ...节点 ], route: { final } }
 * **没有 inbounds、没有 route.rules** —— 两者都由 `config.ts` 补，理由见 types.ts 头部。
 */
import type { RegionOption } from '../shared/types.ts';

export interface ParsedSubscription {
  /** 原样保留的出站数组（含选择器）。 */
  readonly outbounds: readonly Record<string, unknown>[];
  /** 选择器的 tag —— 默认走它。 */
  readonly selectorTag: string;
  /** 可选的出口节点（选择器的成员），给界面做地区切换。 */
  readonly regions: readonly RegionOption[];
}

export class SubscriptionError extends Error {
  readonly reason: 'malformed' | 'empty';
  constructor(reason: 'malformed' | 'empty', message: string) {
    super(message);
    this.name = 'SubscriptionError';
    this.reason = reason;
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/**
 * 节点名 → 展示用地区标签。订阅里的名字形如 `HK-1 · REALITY`；
 * 取不出结构就原样显示 —— **不猜地区**，猜错比不显示更糟。
 */
function labelOf(tag: string): string {
  return tag.trim() || tag;
}

export function parseSubscription(raw: string): ParsedSubscription {
  let doc: unknown;
  try {
    doc = JSON.parse(raw);
  } catch (cause) {
    throw new SubscriptionError('malformed', `订阅不是合法 JSON：${cause instanceof Error ? cause.message : cause}`);
  }
  if (!isRecord(doc)) throw new SubscriptionError('malformed', '订阅顶层不是对象');

  const outbounds = doc['outbounds'];
  if (!Array.isArray(outbounds) || outbounds.length === 0) {
    throw new SubscriptionError('malformed', '订阅里没有 outbounds');
  }
  const records = outbounds.filter(isRecord);
  if (records.length !== outbounds.length) throw new SubscriptionError('malformed', 'outbounds 里有非对象元素');

  const selector = records.find((o) => o['type'] === 'selector');
  const nodes = records.filter((o) => o['type'] !== 'selector');

  // 一个节点都没有 = 账号到期 / 被封 / 配额耗尽时服务端下发的形态（user-journey 的伪节点告知）。
  // 这不是解析错误，是一种要在界面上说清楚的**账号状态**，所以单独一个 reason。
  if (nodes.length === 0) {
    throw new SubscriptionError('empty', '订阅里没有可用节点（账号可能已到期、被封或配额耗尽）');
  }

  let selectorTag: string;
  let members: string[];
  if (selector && typeof selector['tag'] === 'string') {
    selectorTag = selector['tag'];
    const m = selector['outbounds'];
    members = Array.isArray(m) ? m.filter((x): x is string => typeof x === 'string') : [];
  } else {
    // 没有选择器（不该发生，但服务端形状是它的自由）：退化为「直接用第一个节点」。
    const first = nodes[0]?.['tag'];
    if (typeof first !== 'string') throw new SubscriptionError('malformed', '节点没有 tag');
    selectorTag = first;
    members = [first];
  }

  const nodeTags = new Set(nodes.map((n) => n['tag']).filter((t): t is string => typeof t === 'string'));
  const regionTags = members.filter((t) => nodeTags.has(t));
  const regions: RegionOption[] = (regionTags.length > 0 ? regionTags : [...nodeTags]).map((tag) => ({
    tag,
    label: labelOf(tag),
  }));

  return { outbounds: records, selectorTag, regions };
}
