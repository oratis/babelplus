import { describe, expect, it } from 'vitest';
import { decide, hostOf, looksBlocked, normalizeHostList, singboxRules, CN_DIRECT_SUFFIXES } from './routing.ts';

const base = {
  mode: 'smart' as const,
  alwaysProxy: [] as string[],
  neverProxy: [] as string[],
  controlPlaneHosts: ['api.babel.plus', 'web.babel.plus'],
};

describe('decide —— 默认走代理，只有规则表里的才直连', () => {
  it('本地与单段主机名直连', () => {
    for (const h of ['localhost', 'router', 'printer.local', 'thing.localhost']) {
      expect(decide(h, base).route, h).toBe('direct');
    }
  });
  it('控制面直连：控制面故障不得升级为数据面故障', () => {
    expect(decide('api.babel.plus', base)).toEqual({ route: 'direct', reason: 'control-plane' });
    expect(decide('mirror.web.babel.plus', base).route).toBe('direct');
  });
  it('smart 模式下 CN 后缀直连，其余走代理', () => {
    expect(decide('www.taobao.com', base)).toEqual({ route: 'direct', reason: 'cn-direct' });
    expect(decide('anything.cn', base).route).toBe('direct');
    expect(decide('www.google.com', base)).toEqual({ route: 'proxy', reason: 'default-proxy' });
  });
  it('everything 模式下 CN 后缀也走代理，但本地与控制面仍直连', () => {
    const e = { ...base, mode: 'everything' as const };
    expect(decide('www.taobao.com', e).route).toBe('proxy');
    expect(decide('api.babel.plus', e).route).toBe('direct');
    expect(decide('localhost', e).route).toBe('direct');
  });
  it('用户的两个列表优先于服务端规则，never 优先于 always', () => {
    const withLists = { ...base, alwaysProxy: ['taobao.com'], neverProxy: ['www.google.com'] };
    expect(decide('taobao.com', withLists).route).toBe('proxy');
    expect(decide('www.google.com', withLists).route).toBe('direct');
    const conflict = { ...base, alwaysProxy: ['x.invalid'], neverProxy: ['x.invalid'] };
    expect(decide('x.invalid', conflict).route).toBe('direct');
  });
});

describe('singboxRules 与 decide 是同一张表', () => {
  /** 用生成的规则重放一遍判定：规则是有序的，第一条命中的就是结果，没命中则 final=proxy。 */
  function replay(host: string, rules: ReturnType<typeof singboxRules>): 'proxy' | 'direct' {
    for (const r of rules) {
      const suffixes = r.domain_suffix ?? [];
      if (suffixes.some((s) => host === s || host.endsWith(`.${s}`))) {
        return r.outbound === 'D' ? 'direct' : 'proxy';
      }
    }
    return 'proxy';
  }

  it('逐个主机对拍，两处结论一致', () => {
    const input = { ...base, alwaysProxy: ['news.cn'], neverProxy: ['bank.example.invalid'] };
    const rules = singboxRules(input, 'D', 'P');
    const hosts = [
      'www.google.com',
      'www.taobao.com',
      'anything.cn',
      'news.cn',
      'bank.example.invalid',
      'api.babel.plus',
      'deep.sub.jd.com',
      'example.invalid',
      ...CN_DIRECT_SUFFIXES.slice(0, 5).map((s) => `www.${s}`),
    ];
    for (const h of hosts) {
      expect(replay(h, rules), h).toBe(decide(h, input).route);
    }
  });

  it('第一条永远是私有网段直连 —— 本机入站自己也要能连出去', () => {
    expect(singboxRules(base, 'D', 'P')[0]).toEqual({ ip_is_private: true, outbound: 'D' });
  });

  it('everything 模式不下发 CN 直连表', () => {
    const rules = singboxRules({ ...base, mode: 'everything' }, 'D', 'P');
    expect(JSON.stringify(rules)).not.toContain('taobao.com');
  });
});

describe('normalizeHostList / hostOf / looksBlocked', () => {
  it('去协议、路径、端口、前导点，小写去重，非法项丢掉', () => {
    expect(normalizeHostList('https://A.Example.invalid:8443/x\n*.b.invalid, .c.invalid ;a.example.invalid')).toEqual([
      'a.example.invalid',
      'b.invalid',
      'c.invalid',
    ]);
    expect(normalizeHostList(['-bad.invalid', 'ok.invalid', 'has space.invalid'])).toEqual(['ok.invalid']);
  });
  it('hostOf 取不到时返回 null 而不是抛', () => {
    expect(hostOf('https://Example.INVALID/x')).toBe('example.invalid');
    expect(hostOf('not a url')).toBeNull();
  });
  it('looksBlocked 只用于提示条，命中后缀即真', () => {
    expect(looksBlocked('mail.google.com')).toBe(true);
    expect(looksBlocked('www.taobao.com')).toBe(false);
  });
});
