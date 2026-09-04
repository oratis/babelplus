import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { Store } from './store.ts';

let dir: string;
beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), 'bp-store-'));
});
afterEach(async () => {
  await rm(dir, { recursive: true, force: true });
});

describe('Store', () => {
  it('没有文件时回到空状态，不抛', async () => {
    const s = new Store(dir);
    expect(await s.load()).toMatchObject({ token: null, onboarded: false });
  });

  it('文件坏了也回到空状态 —— 一个坏掉的偏好文件不该让浏览器打不开', async () => {
    await writeFile(join(dir, 'state.json'), '{ this is not json');
    const s = new Store(dir);
    expect((await s.load()).token).toBeNull();
  });

  it('偏好里的垃圾值被规整掉，而不是原样信任', async () => {
    await writeFile(
      join(dir, 'state.json'),
      JSON.stringify({ token: 42, prefs: { mode: 'nonsense', alwaysProxy: [1, 'ok.invalid'], launchAtStart: 'yes' } }),
    );
    const s = new Store(dir);
    const loaded = await s.load();
    expect(loaded.token).toBeNull();
    expect(loaded.prefs.mode).toBe('smart');
    expect(loaded.prefs.alwaysProxy).toEqual(['ok.invalid']);
    expect(loaded.prefs.launchAtStart).toBe(false);
  });

  /**
   * 🔴 回归：两处会同时写（Api 的 setToken 回调 + 调用方自己的 update）。
   * 修之前它们共用一个临时文件名，先完成的 rename 把它移走，后一次 ENOENT ——
   * **而丢掉的那次正好可能是 token**（表现：登录成功但重启后又要登录）。
   */
  it('并发写不互相踩，最后一次赢，且落盘内容与内存一致', async () => {
    const s = new Store(dir);
    await s.load();
    await Promise.all([
      s.update({ token: 'a' }),
      s.update({ lastConnectedAt: '2026-09-04T00:00:00Z' }),
      s.update({ token: 'b' }),
      s.update({ onboarded: true }),
    ]);
    await s.flush();
    const onDisk = JSON.parse(await readFile(join(dir, 'state.json'), 'utf8')) as Record<string, unknown>;
    expect(onDisk['token']).toBe('b');
    expect(onDisk['onboarded']).toBe(true);
    expect(onDisk['lastConnectedAt']).toBe('2026-09-04T00:00:00Z');
    expect(onDisk).toEqual(JSON.parse(JSON.stringify(s.current)));
  });

  it('写完之后能被另一个实例读回来（token 跨重启存活是「开机自动连接」的前提）', async () => {
    const a = new Store(dir);
    await a.load();
    await a.update({ token: 'tok', prefs: { ...a.current.prefs, mode: 'everything' } });
    const b = new Store(dir);
    const loaded = await b.load();
    expect(loaded.token).toBe('tok');
    expect(loaded.prefs.mode).toBe('everything');
  });
});
