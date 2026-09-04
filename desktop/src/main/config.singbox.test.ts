/**
 * 🔴 **用真二进制校验我们生成的配置。**
 *
 * 这是本目录里唯一一条不测「我们的逻辑」而测「我们对上游的假设」的用例，也是最值钱的一条：
 * sing-box 的配置 schema 是上游的自由，每个版本都可能改。单元测试再多也只能证明
 * 「我们生成了自己想要的 JSON」，证明不了「sing-box 认这份 JSON」。
 *
 * 有内核就跑，没有就跳过（CI 不下载 100 MB 的二进制）。跑不跑得到，输出里都会说。
 * 本机首次运行：`pnpm core`。
 */
import { existsSync } from 'node:fs';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { buildConfig, serializeConfig } from './config.ts';
import { checkConfig } from './core.ts';
import { parseSubscription } from './subscription.ts';

const key = `${process.platform}-${process.arch}`;
const BIN = join(
  import.meta.dirname,
  '..',
  '..',
  'vendor',
  key,
  process.platform === 'win32' ? 'sing-box.exe' : 'sing-box',
);
const hasCore = existsSync(BIN);

/** 与 api/internal/subgen/singbox.go 的产出同形（REALITY + Hysteria2 各一个节点）。 */
const SUBSCRIPTION = JSON.stringify({
  log: { level: 'warn' },
  outbounds: [
    { type: 'selector', tag: 'babel.plus', outbounds: ['HK-1 · REALITY', 'HK-1 · HY2'], default: 'HK-1 · REALITY' },
    {
      type: 'vless',
      tag: 'HK-1 · REALITY',
      server: '203.0.113.10',
      server_port: 443,
      uuid: '8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92',
      flow: 'xtls-rprx-vision',
      tls: {
        enabled: true,
        server_name: 'www.bing.com',
        utls: { enabled: true, fingerprint: 'chrome' },
        reality: { enabled: true, public_key: '7Xk1RmVQK0nZ2pYb4sX9tJc3vN6hL8dF1gQ5wE7rT0A', short_id: '6ba85179e30d4fc2' },
      },
    },
    {
      type: 'hysteria2',
      tag: 'HK-1 · HY2',
      server: '203.0.113.10',
      server_port: 443,
      password: 'p-1',
      tls: { enabled: true, server_name: 'hk1.babel.plus' },
      obfs: { type: 'salamander', password: 'o-1' },
    },
  ],
  route: { final: 'babel.plus' },
});

describe.skipIf(!hasCore)('sing-box 真二进制校验（本机有 vendor/ 时才跑）', () => {
  it('我们组出来的完整配置，sing-box check 通过', async () => {
    const dir = await mkdtemp(join(tmpdir(), 'bp-sbcheck-'));
    try {
      const { config } = buildConfig({
        subscription: parseSubscription(SUBSCRIPTION),
        port: 34567,
        outbound: null,
        rules: {
          mode: 'smart',
          alwaysProxy: ['news.example.invalid'],
          neverProxy: ['bank.example.invalid'],
          controlPlaneHosts: ['api.babel.plus', 'web.babel.plus'],
        },
      });
      const path = join(dir, 'sing-box.json');
      await writeFile(path, serializeConfig(config));
      const result = await checkConfig(BIN, path);
      expect(result.detail).toBe('');
      expect(result.ok).toBe(true);
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });

  it('everything 模式的配置也通过（规则表不同，schema 同样要认）', async () => {
    const dir = await mkdtemp(join(tmpdir(), 'bp-sbcheck-'));
    try {
      const { config } = buildConfig({
        subscription: parseSubscription(SUBSCRIPTION),
        port: 34568,
        outbound: 'HK-1 · HY2',
        rules: { mode: 'everything', alwaysProxy: [], neverProxy: [], controlPlaneHosts: [] },
      });
      const path = join(dir, 'sing-box.json');
      await writeFile(path, serializeConfig(config));
      const result = await checkConfig(BIN, path);
      expect(result.ok, result.detail).toBe(true);
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });

  it('故意写坏一处，check 必须**不过** —— 否则这条防线是假的', async () => {
    const dir = await mkdtemp(join(tmpdir(), 'bp-sbcheck-'));
    try {
      const path = join(dir, 'sing-box.json');
      await writeFile(path, JSON.stringify({ inbounds: [{ type: '不存在的入站类型' }] }));
      const result = await checkConfig(BIN, path);
      expect(result.ok).toBe(false);
      expect(result.detail.length).toBeGreaterThan(0);
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });
});

if (!hasCore) {
  // eslint-disable-next-line no-console
  console.warn(`[跳过] 没找到 ${BIN} —— 跑 \`pnpm core\` 之后这三条会真的校验 schema。`);
}
