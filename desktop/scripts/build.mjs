/**
 * 构建：三个 esbuild 产物 + 静态文件拷贝。
 *
 * 为什么不用 electron-vite / 不用 tsc 直出：
 *  - 主进程与预加载**必须是 CJS**（sandbox 下的 preload 只能是 CJS，主进程用 CJS 省掉
 *    「ESM 主进程 + CJS preload」两套模块系统并存的坑）；
 *  - 渲染进程**必须是经典脚本**（`loadFile` 的页面是 file:// 源，Chromium 会以 CORS 为由
 *    拒绝加载 `type="module"` 的脚本 —— 现象是一个白窗口加一条控制台报错，
 *    第一次踩会以为是自己的代码没跑）。所以渲染层打成 IIFE。
 *
 * esbuild 一支就能满足这三条，且没有配置文件。tsc 只负责类型检查（`pnpm typecheck`）。
 */
import { build } from 'esbuild';
import { cp, mkdir, rm } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');
const OUT = join(ROOT, 'out');

await rm(OUT, { recursive: true, force: true });
await mkdir(OUT, { recursive: true });

const common = { bundle: true, sourcemap: false, logLevel: 'info', target: 'node20' };

await build({
  ...common,
  entryPoints: [join(ROOT, 'src/main/index.ts')],
  outfile: join(OUT, 'main.cjs'),
  platform: 'node',
  format: 'cjs',
  // electron 由运行时提供；不能打进包里。
  external: ['electron'],
});

await build({
  ...common,
  entryPoints: [join(ROOT, 'src/preload/index.ts')],
  outfile: join(OUT, 'preload.cjs'),
  platform: 'node',
  format: 'cjs',
  external: ['electron'],
});

await build({
  ...common,
  entryPoints: [join(ROOT, 'src/renderer/index.ts')],
  outfile: join(OUT, 'renderer.js'),
  platform: 'browser',
  format: 'iife',
  target: 'chrome120',
});

await cp(join(ROOT, 'src/renderer/index.html'), join(OUT, 'index.html'));
await cp(join(ROOT, 'src/renderer/style.css'), join(OUT, 'style.css'));
console.log('✓ out/ 已构建（main.cjs · preload.cjs · renderer.js · index.html · style.css）');
