/**
 * 官网的两条红线，由测试钉住 —— 人工评审记不住这种事。
 *
 *  1. **文案红线**（go-to-market 裁决 6 / §4.4）：一律自称 consumer privacy，
 *     绝不出现 unblock / 解锁 / 流媒体品牌 / eSIM / 电信字样。
 *     每一条点名 VPN 的支付禁令针对的都是「帮用户绕过访问控制拿没付钱的内容」，不是加密本身；
 *     这一个编辑决定决定我们落在「受限-可审批」还是「禁止」。
 *  2. 🔴 **不在官网暴露订阅配置**（2026-09-04 用户裁决）：软件是服务主体，
 *     订阅是会员中心里的服务。公开页面不出现 Clash / sing-box / YAML / 订阅 / 节点 / 协议名。
 */
import { describe, expect, it } from 'vitest';
import { AVAILABILITY, CONTENT, linksFromEnv, PLANS } from './content.ts';

/** 把所有对外文案摊平成一段文本 —— 新增字段会自动进入检查范围。 */
function allCopy(): string {
  return JSON.stringify(CONTENT).toLowerCase();
}

describe('文案红线：支付通道看的是这些词', () => {
  it('不出现「绕过 / 解锁」这一类词', () => {
    for (const word of ['unblock', 'unblocking', 'bypass', 'circumvent', 'get around', 'evade', 'censorship-free', '解锁', '翻墙']) {
      expect(allCopy(), word).not.toContain(word);
    }
  });

  it('不出现流媒体品牌、eSIM、电信字样', () => {
    for (const word of ['netflix', 'hulu', 'disney', 'bbc iplayer', 'esim', 'sim card', 'telecom', 'mobile data plan']) {
      expect(allCopy(), word).not.toContain(word);
    }
  });

  it('不出现「保证 / 一定能用」这类无据承诺', () => {
    for (const word of ['guarantee', 'guaranteed', 'always works', '100%', 'never blocked', 'undetectable']) {
      expect(allCopy(), word).not.toContain(word);
    }
  });

  it('把边界说出来了：只接管一个浏览器', () => {
    expect(allCopy()).toContain('nothing else on your computer');
    expect(CONTENT.limits.items[0]?.toLowerCase()).toContain('routes one browser');
  });
});

describe('🔴 订阅配置不在官网暴露（软件是服务主体）', () => {
  it('公开文案里不出现任何配置 / 协议 / 订阅字样', () => {
    for (const word of [
      'clash',
      'sing-box',
      'singbox',
      'yaml',
      'yml',
      'subscription link',
      'subscribe',
      'config file',
      'v2ray',
      'vless',
      'reality',
      'hysteria',
      'shadowsocks',
      'socks5',
      'proxy url',
      '订阅',
      '节点',
    ]) {
      expect(allCopy(), word).not.toContain(word);
    }
  });

  it('购买与配置都指向会员中心，而不是站点自己给一份配置', () => {
    expect(CONTENT.plansNote.toLowerCase()).toContain('in your account');
  });
});

describe('可售状态是显式布尔，不是文案里的形容词', () => {
  it('两个客户端与结账都还没开 —— 站点必须据此渲染，而不是摆一个点不动的按钮', () => {
    expect(AVAILABILITY).toEqual({ browser: false, extension: false, checkout: false });
  });
});

describe('通行证', () => {
  it('四档与 go-to-market §3.2 一致', () => {
    expect(PLANS.map((p) => p.price)).toEqual(['$2.50', '$4.50', '$8.90', '$18.90']);
    expect(PLANS.map((p) => p.data)).toEqual(['3 GB', '10 GB', '20 GB', '50 GB']);
  });
  it('不承诺不限量', () => {
    expect(allCopy()).not.toContain('unlimited');
  });
});

describe('linksFromEnv', () => {
  it('缺省是空串 —— 空则渲染成不可点，不编一个域名', () => {
    expect(linksFromEnv({})).toEqual({ account: '', help: '', status: '' });
    expect(linksFromEnv({ VITE_BP_ACCOUNT_URL: ' https://web.example.invalid ' }).account).toBe('https://web.example.invalid');
  });
});
