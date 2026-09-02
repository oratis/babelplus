import { describe, expect, it } from 'vitest';
import { buildPac, buildSingleEndpointPac, hostsOfUrls, normalizeHostList } from './pac.ts';

/**
 * 在 Node 里执行生成的 PAC：把 Chrome 的 PAC 内置函数补上，然后调用 FindProxyForURL。
 * 这不是「像 Chrome 一样」执行，但足以钉住三条规则：末位无 DIRECT、直连清单、优先级。
 */
function runPac(pac: string, host: string): string {
  const helpers = `
    function dnsDomainIs(host, domain) { return host.length >= domain.length && host.slice(-domain.length) === domain; }
    function isPlainHostName(host) { return host.indexOf('.') === -1; }
    function shExpMatch(str, pattern) {
      var re = new RegExp('^' + pattern.replace(/[.+^$(){}|[\\]\\\\]/g, '\\\\$&').replace(/\\*/g, '.*').replace(/\\?/g, '.') + '$');
      return re.test(str);
    }
    function isInNet(ip, net, mask) {
      var toN = function (s) { var p = s.split('.'); return ((+p[0]) << 24 | (+p[1]) << 16 | (+p[2]) << 8 | (+p[3])) >>> 0; };
      return (toN(ip) & toN(mask)) === (toN(net) & toN(mask));
    }
  `;
  // eslint-disable-next-line @typescript-eslint/no-implied-eval
  const fn = new Function('host', `${helpers}\n${pac}\nreturn FindProxyForURL("https://" + host + "/", host);`);
  return fn(host) as string;
}

const endpoints = [
  { host: 'a.example.invalid', port: 443 },
  { host: 'b.example.invalid', port: 8443 },
];
const rules = { direct_suffixes: ['cn', 'taobao.com'], proxy_suffixes: ['sub.taobao.com'] };
const base = { endpoints, rules, mode: 'smart' as const, alwaysProxy: [], neverProxy: [], controlPlaneHosts: ['api.example.invalid'] };

describe('buildPac', () => {
  it('候选串按给定顺序排列，且末位不是 DIRECT', () => {
    const pac = buildPac(base);
    expect(pac).toContain('"HTTPS a.example.invalid:443; HTTPS b.example.invalid:8443"');
    expect(pac).not.toMatch(/HTTPS [^"]*; *DIRECT/);
    expect(runPac(pac, 'www.google.com')).toBe('HTTPS a.example.invalid:443; HTTPS b.example.invalid:8443');
  });

  it('空端点列表拒绝生成 —— 不能生成一份只剩 DIRECT 的脚本', () => {
    expect(() => buildPac({ ...base, endpoints: [] })).toThrow(/至少一个端点/);
  });

  it('本地与私有地址一律直连', () => {
    const pac = buildPac(base);
    for (const h of ['localhost', 'router', 'printer.local', '10.1.2.3', '192.168.0.1', '172.20.0.5', '127.0.0.1', '169.254.1.1', '100.64.0.9']) {
      expect(runPac(pac, h), h).toBe('DIRECT');
    }
    expect(runPac(pac, '8.8.8.8')).not.toBe('DIRECT');
  });

  it('控制面主机直连：控制面故障不得升级为数据面故障', () => {
    const pac = buildPac(base);
    expect(runPac(pac, 'api.example.invalid')).toBe('DIRECT');
    expect(runPac(pac, 'mirror.api.example.invalid')).toBe('DIRECT');
  });

  it('smart 模式：服务端直连表直连，代理例外优先于直连表', () => {
    const pac = buildPac(base);
    expect(runPac(pac, 'www.baidu.cn')).toBe('DIRECT');
    expect(runPac(pac, 'taobao.com')).toBe('DIRECT');
    expect(runPac(pac, 'sub.taobao.com')).not.toBe('DIRECT');
    expect(runPac(pac, 'deep.sub.taobao.com')).not.toBe('DIRECT');
  });

  it('everything 模式：忽略服务端规则表，只留本地与控制面', () => {
    const pac = buildPac({ ...base, mode: 'everything' });
    expect(runPac(pac, 'www.baidu.cn')).not.toBe('DIRECT');
    expect(runPac(pac, 'api.example.invalid')).toBe('DIRECT');
    expect(runPac(pac, 'localhost')).toBe('DIRECT');
  });

  it('用户的两个列表优先级高于服务端规则表', () => {
    const pac = buildPac({ ...base, alwaysProxy: ['taobao.com'], neverProxy: ['www.google.com'] });
    expect(runPac(pac, 'taobao.com')).not.toBe('DIRECT');
    expect(runPac(pac, 'www.google.com')).toBe('DIRECT');
  });

  it('用户列表在 PAC 里的是规整过的主机名，脚本不会因为一行 URL 而炸掉', () => {
    const pac = buildPac({ ...base, neverProxy: ['https://Bank.Example.INVALID/login', ' .foo.invalid '] });
    expect(pac).toContain('"bank.example.invalid"');
    expect(pac).toContain('"foo.invalid"');
    expect(runPac(pac, 'bank.example.invalid')).toBe('DIRECT');
  });

  it('端点主机名与端口非法时拒绝', () => {
    expect(() => buildPac({ ...base, endpoints: [{ host: 'bad host', port: 443 }] })).toThrow(/主机名/);
    expect(() => buildPac({ ...base, endpoints: [{ host: 'a.example.invalid', port: 70000 }] })).toThrow(/端口/);
  });
});

describe('buildSingleEndpointPac', () => {
  it('只含一台，且一切非本地流量都走它（探测时不许有第二台兜底）', () => {
    const pac = buildSingleEndpointPac({ host: 'a.example.invalid', port: 443 }, ['api.example.invalid']);
    expect(pac).toContain('"HTTPS a.example.invalid:443"');
    expect(runPac(pac, 'www.baidu.cn')).toBe('HTTPS a.example.invalid:443');
    expect(runPac(pac, 'api.example.invalid')).toBe('DIRECT');
  });
});

describe('normalizeHostList / hostsOfUrls', () => {
  it('去协议、路径、端口、前导点，小写并去重', () => {
    expect(normalizeHostList('https://A.Example.invalid:8443/x\n*.b.invalid, .c.invalid ;a.example.invalid')).toEqual([
      'a.example.invalid',
      'b.invalid',
      'c.invalid',
    ]);
  });
  it('非法项丢掉而不是猜', () => {
    expect(normalizeHostList(['-bad.invalid', 'ok.invalid', 'has space.invalid'])).toEqual(['ok.invalid']);
  });
  it('hostsOfUrls 跳过非法 URL', () => {
    expect(hostsOfUrls(['https://api.example.invalid', 'not a url', 'https://Web.Example.invalid/x'])).toEqual([
      'api.example.invalid',
      'web.example.invalid',
    ]);
  });
});
