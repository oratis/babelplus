/**
 * 官网只有一类测试，而它恰恰是最该测的一类：**文案红线**。
 * go-to-market 裁决 6 与 §4.4 的结论是「产品必须自称 consumer privacy，
 * 绝不出现 unblock / 解锁 / 流媒体品牌 / eSIM / 电信字样」—— 那一个编辑决定
 * 决定我们落在支付通道的「受限-可审批」还是「禁止」。
 * 人工评审记不住这种事，所以让测试记。
 */
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
});
