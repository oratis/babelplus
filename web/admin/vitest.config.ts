/**
 * 后台的测试配置。
 *
 * `environment: 'node'`：这里测的是「IAP 的 401 与应用层的 401 怎么分」，
 * 输入是 `Response` 对象、输出是一个判定结果，没有一处需要 DOM。
 * 判别逻辑刻意写成纯函数（`lib/iap.ts` 的 `classifyAdminAuthFailure`）就是为了这一点：
 * 「判错了」与「处置错了」要能分开验证。
 */
import { defineConfig, mergeConfig } from 'vitest/config';
import viteConfig from './vite.config.ts';

export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: 'node',
      include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
      restoreMocks: true,
    },
  }),
);
