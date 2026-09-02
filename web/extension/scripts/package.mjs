/**
 * 把 dist/ 打成商店提交用的 zip：`babelplus-extension-<version>.zip`（版本取自 public/manifest.json）。
 *
 * 先做三条只读检查，任一不过就不打包 —— 这些都是提交后才会被审核拒掉、来回一周的错误：
 *  1. dist/manifest.json 存在且 version 与 package.json 一致；
 *  2. manifest 里引用的每个文件（background、popup、options、图标）都在 dist 里；
 *  3. dist 里没有 sourcemap、没有 .DS_Store 之类的杂物。
 *
 * 用 zip 命令而不是 npm 包：macOS 与 CI 的 ubuntu 都自带；不为了打包多引一个依赖。
 *
 * 用法：pnpm build && pnpm package
 */
import { spawnSync } from 'node:child_process';
import { existsSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');
const DIST = join(ROOT, 'dist');

function fail(msg) {
  console.error(`✗ ${msg}`);
  process.exit(1);
}

if (!existsSync(join(DIST, 'manifest.json'))) fail('dist/manifest.json 不存在，先 pnpm build');
const manifest = JSON.parse(readFileSync(join(DIST, 'manifest.json'), 'utf8'));
const pkg = JSON.parse(readFileSync(join(ROOT, 'package.json'), 'utf8'));
if (manifest.version !== pkg.version) fail(`manifest.version=${manifest.version} 与 package.json version=${pkg.version} 不一致`);

const referenced = [
  manifest.background?.service_worker,
  manifest.action?.default_popup,
  manifest.options_ui?.page,
  ...Object.values(manifest.icons ?? {}),
  ...Object.values(manifest.action?.default_icon ?? {}),
  '_locales/en/messages.json',
].filter(Boolean);
for (const rel of referenced) {
  if (!existsSync(join(DIST, rel))) fail(`manifest 引用的 ${rel} 不在 dist 里`);
}

function* walk(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) yield* walk(full);
    else yield full;
  }
}
let total = 0;
for (const file of walk(DIST)) {
  const rel = file.slice(DIST.length + 1);
  if (rel.endsWith('.map')) fail(`dist 里有 sourcemap：${rel}（vite.config 的 sourcemap 应为 false）`);
  if (rel.includes('.DS_Store') || rel.startsWith('.')) fail(`dist 里有杂物：${rel}`);
  total += statSync(file).size;
}

const out = join(ROOT, `babelplus-extension-${manifest.version}.zip`);
rmSync(out, { force: true });
const zip = spawnSync('zip', ['-r', '-X', '-q', out, '.'], { cwd: DIST, stdio: 'inherit' });
if (zip.status !== 0) fail('zip 失败（macOS / ubuntu 自带 zip；Windows 用 WSL）');
console.log(`✓ ${out}\n  解包大小 ${(total / 1024).toFixed(0)} KB · manifest ${manifest.manifest_version} · version ${manifest.version}`);
