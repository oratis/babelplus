/**
 * 用户面板的测试配置。
 *
 * 复用 `vite.config.ts` 而不是重抄一份 `resolve.alias`：
 * 别名一旦两处维护，测试里 `@babelplus/shared` 指向的就可能不是构建时那一份，
 * 而这种偏差表现为「测试全绿但线上是坏的」。
 *
 * `environment: 'jsdom'`：这里测的是路由守卫，需要真的把组件渲染出来
 * 才能验证「加载态不误跳」——「渲染过登录页没有」这件事只有 DOM 能回答。
 */
import { defineConfig, mergeConfig } from 'vitest/config';
import viteConfig from './vite.config.ts';

export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: 'jsdom',
      include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
      restoreMocks: true,
    },
  }),
);
