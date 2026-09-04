import { describe, expect, it } from 'vitest';
import { parseSubscription, SubscriptionError } from './subscription.ts';

/** 形状照 api/internal/subgen/singbox.go 的实际产出（选择器排第一位，没有 inbounds）。 */
const real = JSON.stringify({
  log: { level: 'warn' },
  outbounds: [
    { type: 'selector', tag: 'babel.plus', outbounds: ['HK-1 · REALITY', 'HK-1 · HY2'], default: 'HK-1 · REALITY' },
    { type: 'vless', tag: 'HK-1 · REALITY', server: '203.0.113.10', server_port: 443, uuid: 'u' },
    { type: 'hysteria2', tag: 'HK-1 · HY2', server: '203.0.113.10', server_port: 443, password: 'p' },
  ],
  route: { final: 'babel.plus' },
});

describe('parseSubscription', () => {
  it('认出选择器与地区列表，出站原样保留', () => {
    const p = parseSubscription(real);
    expect(p.selectorTag).toBe('babel.plus');
    expect(p.regions.map((r) => r.tag)).toEqual(['HK-1 · REALITY', 'HK-1 · HY2']);
    expect(p.outbounds).toHaveLength(3);
  });

  it('订阅里没有节点 = 账号状态（到期 / 被封 / 配额耗尽），不是解析错误', () => {
    const empty = JSON.stringify({ outbounds: [{ type: 'selector', tag: 'g', outbounds: [] }] });
    try {
      parseSubscription(empty);
      throw new Error('应该抛');
    } catch (cause) {
      expect(cause).toBeInstanceOf(SubscriptionError);
      expect((cause as SubscriptionError).reason).toBe('empty');
    }
  });

  it('没有选择器时退化为「用第一个节点」，而不是失败', () => {
    const noSelector = JSON.stringify({
      outbounds: [{ type: 'vless', tag: 'only-node', server: 'h', server_port: 443 }],
    });
    const p = parseSubscription(noSelector);
    expect(p.selectorTag).toBe('only-node');
    expect(p.regions.map((r) => r.tag)).toEqual(['only-node']);
  });

  it('不是 JSON / 顶层不是对象 / 没有 outbounds → malformed', () => {
    for (const bad of ['not json', '[]', '{}', JSON.stringify({ outbounds: [] })]) {
      try {
        parseSubscription(bad);
        throw new Error(`应该抛：${bad}`);
      } catch (cause) {
        expect(cause, bad).toBeInstanceOf(SubscriptionError);
        expect((cause as SubscriptionError).reason, bad).toBe('malformed');
      }
    }
  });
});
