import { describe, expect, it } from 'vitest';
import { buildConfig, DIRECT_TAG, INBOUND_TAG, redactConfig, serializeConfig } from './config.ts';
import type { ParsedSubscription } from './subscription.ts';

const sub: ParsedSubscription = {
  selectorTag: 'babel.plus',
  regions: [
    { tag: 'HK-1 · REALITY', label: 'HK-1 · REALITY' },
    { tag: 'HK-1 · HY2', label: 'HK-1 · HY2' },
  ],
  outbounds: [
    { type: 'selector', tag: 'babel.plus', outbounds: ['HK-1 · REALITY', 'HK-1 · HY2'], default: 'HK-1 · REALITY' },
    { type: 'vless', tag: 'HK-1 · REALITY', server: '35.215.158.52', server_port: 443, uuid: 'u-1' },
    { type: 'hysteria2', tag: 'HK-1 · HY2', server: '35.215.158.52', server_port: 443, password: 'p-1' },
  ],
};

const rules = { mode: 'smart' as const, alwaysProxy: [], neverProxy: [], controlPlaneHosts: ['api.babel.plus'] };

describe('buildConfig', () => {
  it('注入只监听回环的 mixed 入站 —— 监听 0.0.0.0 会让同一 WiFi 的人白嫖配额', () => {
    const { config } = buildConfig({ subscription: sub, port: 31234, rules, outbound: null });
    const inbounds = config['inbounds'] as Record<string, unknown>[];
    expect(inbounds).toHaveLength(1);
    expect(inbounds[0]).toMatchObject({ type: 'mixed', tag: INBOUND_TAG, listen: '127.0.0.1', listen_port: 31234 });
  });

  it('订阅的出站原样保留，另加一个 direct 出站给直连规则用', () => {
    const { config } = buildConfig({ subscription: sub, port: 1080, rules, outbound: null });
    const outbounds = config['outbounds'] as Record<string, unknown>[];
    expect(outbounds).toHaveLength(sub.outbounds.length + 1);
    expect(outbounds.slice(0, 3)).toEqual(sub.outbounds);
    expect(outbounds.at(-1)).toEqual({ type: 'direct', tag: DIRECT_TAG });
  });

  it('final 指向选择器 = 默认走代理；用户选了具体节点则指向它', () => {
    const a = buildConfig({ subscription: sub, port: 1080, rules, outbound: null });
    expect((a.config['route'] as Record<string, unknown>)['final']).toBe('babel.plus');
    expect(a.proxyTag).toBe('babel.plus');

    const b = buildConfig({ subscription: sub, port: 1080, rules, outbound: 'HK-1 · HY2' });
    expect((b.config['route'] as Record<string, unknown>)['final']).toBe('HK-1 · HY2');
    expect(b.proxyTag).toBe('HK-1 · HY2');
  });

  it('用户选的节点已经不在订阅里 → 退回选择器，**不报错**', () => {
    const { config, proxyTag } = buildConfig({ subscription: sub, port: 1080, rules, outbound: '已经下线的节点' });
    expect(proxyTag).toBe('babel.plus');
    expect((config['route'] as Record<string, unknown>)['final']).toBe('babel.plus');
  });

  it('端口不合法直接抛 —— 下发 0 会让 sing-box 绑到随机端口', () => {
    for (const port of [0, -1, 70000, 1.5]) {
      expect(() => buildConfig({ subscription: sub, port, rules, outbound: null })).toThrow(/端口/);
    }
  });

  it('日志默认 warn：info 会把每个连接的域名写进日志，那是不该落盘的东西', () => {
    const { config } = buildConfig({ subscription: sub, port: 1080, rules, outbound: null });
    expect((config['log'] as Record<string, unknown>)['level']).toBe('warn');
  });
});

describe('redactConfig', () => {
  it('抹掉一切像凭据的值，保留结构与 tag —— 诊断报告要能贴进工单', () => {
    const { config } = buildConfig({ subscription: sub, port: 1080, rules, outbound: null });
    const text = JSON.stringify(redactConfig(config));
    for (const secret of ['u-1', 'p-1', '35.215.158.52']) {
      expect(text, secret).not.toContain(secret);
    }
    expect(text).toContain('HK-1 · REALITY');
    expect(text).toContain(INBOUND_TAG);
  });
});

describe('serializeConfig', () => {
  it('两格缩进 + 结尾换行（这份文件会被贴进工单）', () => {
    const out = serializeConfig({ a: 1 });
    expect(out).toBe('{\n  "a": 1\n}\n');
  });
});
