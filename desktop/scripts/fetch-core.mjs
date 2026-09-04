/**
 * 取随包内核（sing-box）到 `vendor/<platform>-<arch>/`，校验 SHA-256 后解包。
 *
 * 🔴 **校验和是 TOFU（trust on first use），不是上游签名。** 2026-09-04 实查：
 * sing-box 的 GitHub release **没有发布 checksums 文件**（`releases/tags/v1.14.0` 的资产列表里
 * 只有各平台的压缩包）。所以下面这三个值是**本仓在 2026-09-04 下载后自己算的**，
 * 它能挡住「以后某次下载被换掉」，挡不住「第一次下载就是被换过的」。
 *
 * 这一条与 [client-products-spec §4.5](../../docs/03-product/client-products-spec.md) 的
 * 「sing-box 自编译」是同一件事的两端：**分发（B3）之前必须改成自编译**，
 * 理由不只是校验和，还有杀软误报（Xray-core 官方 release 被 Defender 标 Wacatac 的那条先例）。
 * 在那之前，这个脚本只用于开发机跑起来。
 *
 * 二进制**不入库**（`.gitignore` 里有 `desktop/vendor/`）：仓库里不该出现来历不明的二进制。
 *
 * 用法：node scripts/fetch-core.mjs [--platform=darwin] [--arch=arm64] [--force]
 */
import { createHash } from 'node:crypto';
import { createWriteStream } from 'node:fs';
import { chmod, mkdir, mkdtemp, readFile, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { pipeline } from 'node:stream/promises';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const VERSION = '1.14.0';
const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');
const VENDOR = join(ROOT, 'vendor');

/** key = `${platform}-${arch}`。值是 2026-09-04 下载后本机 `shasum -a 256` 的结果。 */
const RELEASES = {
  'darwin-arm64': {
    asset: `sing-box-${VERSION}-darwin-arm64.tar.gz`,
    sha256: 'a150c94012ff768b7261939cd236b9c8554127f45137230295d23a5660225cc9',
  },
  'darwin-x64': {
    asset: `sing-box-${VERSION}-darwin-amd64.tar.gz`,
    sha256: '6cf26fc3501f3117cf781e9405cf5338f60add6da5affae39421af6800ebbcb4',
  },
  'win32-x64': {
    asset: `sing-box-${VERSION}-windows-amd64.zip`,
    sha256: '3ffb56267da14e287be48bd10cf7e6505260125bad940b75101fbb4d5d58e5d6',
  },
};

const args = new Map(
  process.argv.slice(2).map((a) => {
    const [k, v] = a.replace(/^--/, '').split('=');
    return [k, v ?? 'true'];
  }),
);
const platform = args.get('platform') ?? process.platform;
const arch = args.get('arch') ?? process.arch;
const key = `${platform}-${arch}`;
const rel = RELEASES[key];

if (!rel) {
  console.error(
    `✗ 没有为 ${key} 钉过校验和。\n` +
      `  支持的平台：${Object.keys(RELEASES).join(' / ')}。\n` +
      `  要加一个平台：下载对应资产、算 sha256、把它写进本文件的 RELEASES —— **不要跳过校验**。`,
  );
  process.exit(1);
}

const destDir = join(VENDOR, key);
const binName = platform === 'win32' ? 'sing-box.exe' : 'sing-box';
const destBin = join(destDir, binName);

if (args.get('force') !== 'true') {
  const existing = await stat(destBin).catch(() => null);
  if (existing) {
    console.log(`✓ 已存在：${destBin}（--force 可重下）`);
    process.exit(0);
  }
}

const url = `https://github.com/SagerNet/sing-box/releases/download/v${VERSION}/${rel.asset}`;
const work = await mkdtemp(join(tmpdir(), 'bp-core-'));
const archive = join(work, rel.asset);

console.log(`▸ 下载 ${url}`);
const res = await fetch(url, { redirect: 'follow' });
if (!res.ok || !res.body) {
  console.error(`✗ 下载失败：HTTP ${res.status}`);
  process.exit(1);
}
await pipeline(res.body, createWriteStream(archive));

const digest = createHash('sha256').update(await readFile(archive)).digest('hex');
if (digest !== rel.sha256) {
  console.error(`✗ 校验和不符\n  期望 ${rel.sha256}\n  实得 ${digest}\n  **不要绕过这一步**：内核是要接管全部浏览流量的东西。`);
  await rm(work, { recursive: true, force: true });
  process.exit(1);
}
console.log(`✓ sha256 与钉住的值一致`);

await mkdir(destDir, { recursive: true });
const unpack = rel.asset.endsWith('.zip')
  ? spawnSync('unzip', ['-o', '-j', archive, `*/${binName}`, '-d', destDir], { stdio: 'inherit' })
  : spawnSync('tar', ['-xzf', archive, '-C', destDir, '--strip-components=1', `${rel.asset.replace(/\.tar\.gz$/, '')}/${binName}`], {
      stdio: 'inherit',
    });
if (unpack.status !== 0) {
  console.error('✗ 解包失败（macOS / Linux 需要 tar，Windows 需要 unzip）');
  process.exit(1);
}
if (platform !== 'win32') await chmod(destBin, 0o755);
await rm(work, { recursive: true, force: true });

const version = spawnSync(destBin, ['version'], { encoding: 'utf8' });
console.log(`✓ ${destBin}`);
if (version.stdout) console.log(version.stdout.trim().split('\n')[0]);
