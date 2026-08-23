/**
 * `shared` 的测试配置。
 *
 * `environment: 'node'` —— 这个包里被测的三块（API 客户端、会话单飞、returnTo 校验）
 * 都不碰 DOM。Node 26 原生提供 `fetch` / `Request` / `Response` / `AbortSignal.timeout`，
 * 所以传输层能按它在浏览器里的样子被测，不需要 jsdom 也不需要任何 fetch mock 库。
 *
 * 不开 `globals`：`describe` / `it` / `expect` 全部显式 import。
 * 这个包的 tsconfig 是 `"types": []`，开 globals 就得往里塞 `vitest/globals`，
 * 而那会让**生产代码**也能看见测试全局变量。
 */
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'node',
    include: ['api/**/*.test.ts', 'src/**/*.test.ts'],
  },
});
