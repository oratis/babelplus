/**
 * `catalog-common` 的纯函数。不需要 DOM，走包级默认的 node 环境。
 */
import { describe, expect, it } from 'vitest';
import { GIB, bytesToGibText, gibTextToBytes } from './catalog-common.tsx';

describe('gibTextToBytes', () => {
  // 🔴 这一组守的是一个「看起来像是校验规则」的缺陷。
  //    从前的实现是 `(milli * GIB) / scale` 再判 Number.isSafeInteger：
  //    GIB = 2^30，scale = 1000 = 2^3 × 5^3，而 5^3 = 125 永远除不尽 2^30。
  //    于是只有 milli 是 125 的倍数时才通过 —— 0.5 碰巧过，0.1 / 0.2 / 1.1 全被拒。
  //    现象是「套餐编辑器存不了」，而提示词还写着「填一个数字」。
  it('🔴 一位小数必须全部被接受，不只是 .5', () => {
    for (const s of ['0.1', '0.2', '0.3', '0.4', '0.5', '0.6', '0.7', '0.8', '0.9', '1.1', '2.3']) {
      expect(gibTextToBytes(s), `${s} GB 应当可以填`).not.toBeNull();
    }
  });

  it('整数 GiB 精确无误差', () => {
    expect(gibTextToBytes('1')).toBe(GIB);
    expect(gibTextToBytes('100')).toBe(100 * GIB);
  });

  it('小数结果四舍五入到整字节，且落在合理范围内', () => {
    const half = gibTextToBytes('0.5');
    expect(half).toBe(GIB / 2);
    const tenth = gibTextToBytes('0.1');
    expect(tenth).toBe(Math.round(GIB / 10));
    expect(Number.isSafeInteger(tenth!)).toBe(true);
  });

  it('非法输入仍然拒绝', () => {
    for (const s of ['', ' ', 'abc', '-1', '1.2345', '1e3', '1..2']) {
      expect(gibTextToBytes(s), `${JSON.stringify(s)} 不该被接受`).toBeNull();
    }
  });

  it('与 bytesToGibText 往返：整数 GiB 原样回来', () => {
    expect(bytesToGibText(gibTextToBytes('7')!)).toBe('7');
  });
});
