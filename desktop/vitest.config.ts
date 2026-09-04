/**
 * 只测纯逻辑（路由规则、配置组装、订阅解析、内核监护、状态机）。
 * 不开 `globals`：`describe` / `it` / `expect` 全部显式 import，与 web/shared 的约定一致。
 * 环境是 node —— 这里没有一行代码需要 DOM，渲染进程的验证靠真机（README 已登记）。
 */
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
    restoreMocks: true,
  },
});
