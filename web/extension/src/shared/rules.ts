/**
 * 服务端没下发规则表时的兜底（`rules.direct_suffixes` 为空或配置从未拉到过）。
 *
 * ⚠️ 这不是正式清单，正式清单由 `/user/proxy-config` 下发并带 `rules_rev`。
 * 兜底只放最没有争议的几类：国家顶级域、几家人人都会用的境内服务、银行与支付。
 * 方向是 spec §3.4 的「默认走代理」：不在这里 = 走代理。
 *
 * 不放任何猜测出来的域名 —— 猜错一条的后果是「淘宝打不开」这种最贵的工单。
 */
import type { ProxyRules } from './types.ts';

export const FALLBACK_RULES: ProxyRules = {
  direct_suffixes: [
    'cn',
    'taobao.com',
    'tmall.com',
    'alipay.com',
    'alicdn.com',
    'jd.com',
    'qq.com',
    'weixin.qq.com',
    'wechat.com',
    'baidu.com',
    'bdstatic.com',
    'amap.com',
    'autonavi.com',
    'meituan.com',
    'dianping.com',
    'bilibili.com',
    'zhihu.com',
    'douyin.com',
    'unionpay.com',
    'cmbchina.com',
    'icbc.com.cn',
    'ccb.com',
    'boc.cn',
  ],
  proxy_suffixes: [],
};

export function effectiveRules(fromServer: ProxyRules | null | undefined): ProxyRules {
  if (fromServer && fromServer.direct_suffixes.length > 0) return fromServer;
  return FALLBACK_RULES;
}
