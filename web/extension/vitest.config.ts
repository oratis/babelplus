/**
 * 复用 vite.config.ts 的别名（理由同 user/vitest.config.ts：别名两处维护会让测试指向另一份 shared）。
 * jsdom：popup 的八个状态要真的渲染出来才能断言主按钮文案。
 * `chrome` 全局不在这里注入 —— 每个用例用 `src/test/chrome-fake.ts` 自己装一份，
 * 这样「哪个用例依赖哪些 chrome API」在用例里可见，而不是藏在 setup 文件里。
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
