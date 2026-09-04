/**
 * 内核监护的用例。`spawnImpl` 与 `waitImpl` 注入，所以不需要真的 sing-box。
 * 每条对应 core.ts 文件头列的一条规矩。
 */
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Core, type CoreEvent, type SpawnedProcess } from './core.ts';

interface FakeProc extends SpawnedProcess {
  exit(code: number | null, signal?: string | null): void;
  readonly args: readonly string[];
  killed: boolean;
}

function fakeSpawner(opts: { checkExit?: number; onRun?: (p: FakeProc) => void } = {}) {
  const spawned: FakeProc[] = [];
  const impl = (_bin: string, args: readonly string[]): SpawnedProcess => {
    let exitCb: ((code: number | null, signal: string | null) => void) | null = null;
    let stderrCb: ((line: string) => void) | null = null;
    let exited = false;
    const p: FakeProc = {
      pid: 1234,
      args,
      killed: false,
      kill: () => {
        p.killed = true;
      },
      onExit: (cb) => {
        exitCb = cb;
      },
      onStderr: (cb) => {
        stderrCb = cb;
      },
      // 真进程只会退出一次。假的也必须只退一次，否则用例会「多收一个 failed 事件」而看不出是自己造的。
      exit: (code, signal = null) => {
        if (exited) return;
        exited = true;
        exitCb?.(code, signal);
      },
    };
    spawned.push(p);
    if (args[0] === 'check') {
      // check 是同步收尾的短命进程：注册完回调后立刻给结果。
      queueMicrotask(() => {
        if (opts.checkExit !== 0 && opts.checkExit !== undefined) stderrCb?.('FATAL: decode config: bad json');
        p.exit(opts.checkExit ?? 0);
      });
    } else {
      opts.onRun?.(p);
    }
    return p;
  };
  return { impl, spawned };
}

let dir: string;
beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), 'bp-core-test-'));
});
afterEach(async () => {
  // 某个用例中途失败时假定时器会漏给下一个用例，表现为下一个用例莫名超时 5 s。
  vi.useRealTimers();
  await rm(dir, { recursive: true, force: true });
});

describe('Core.start', () => {
  it('check 不过就不启动，也不留下跑着的进程', async () => {
    const { impl, spawned } = fakeSpawner({ checkExit: 1 });
    const core = new Core({ binary: 'sing-box', spawnImpl: impl, waitImpl: async () => true, workDir: dir });
    await expect(core.start({ log: {} }, 1080)).rejects.toThrow(/bad json/);
    expect(spawned.map((p) => p.args[0])).toEqual(['check']);
    expect(core.running).toBe(false);
  });

  it('配置真的写到了盘上，且 check 指向那个文件', async () => {
    const { impl, spawned } = fakeSpawner({ checkExit: 0 });
    const core = new Core({ binary: 'sing-box', spawnImpl: impl, waitImpl: async () => true, workDir: dir });
    await core.start({ log: { level: 'warn' } }, 1080);
    const path = join(dir, 'sing-box.json');
    expect(JSON.parse(await readFile(path, 'utf8'))).toEqual({ log: { level: 'warn' } });
    expect(spawned[0]?.args).toEqual(['check', '-c', path]);
    expect(spawned[1]?.args).toEqual(['run', '-c', path]);
    await core.stop();
  });

  it('端口一直不通 → 抛 port-unavailable 并杀掉进程', async () => {
    const { impl, spawned } = fakeSpawner({ checkExit: 0 });
    const core = new Core({ binary: 'sing-box', spawnImpl: impl, waitImpl: async () => false, workDir: dir });
    await expect(core.start({}, 1080)).rejects.toMatchObject({ reason: 'port-unavailable' });
    expect(spawned[1]?.killed).toBe(true);
    expect(core.running).toBe(false);
  });
});

describe('Core 的崩溃处置', () => {
  it('非预期退出会重启，且事件里带上第几次与退避时长', async () => {
    vi.useFakeTimers();
    const events: CoreEvent[] = [];
    const { impl, spawned } = fakeSpawner({ checkExit: 0 });
    const core = new Core({
      binary: 'sing-box',
      spawnImpl: impl,
      waitImpl: async () => true,
      workDir: dir,
      onEvent: (e) => events.push(e),
    });
    await core.start({}, 1080);
    expect(events.at(-1)).toEqual({ type: 'started', port: 1080 });

    spawned[1]?.exit(1, null);
    await vi.advanceTimersByTimeAsync(600);
    expect(events.map((e) => e.type)).toContain('restarting');
    const restarting = events.find((e) => e.type === 'restarting');
    expect(restarting).toMatchObject({ attempt: 1, delayMs: 500 });
    expect(spawned.filter((p) => p.args[0] === 'run')).toHaveLength(2);
    vi.useRealTimers();
    await core.stop();
  });

  it('到重启上限后进 failed —— 无限重启会把配置错误变成一台一直 fork 的机器', async () => {
    vi.useFakeTimers();
    const events: CoreEvent[] = [];
    const { impl, spawned } = fakeSpawner({ checkExit: 0 });
    const core = new Core({
      binary: 'sing-box',
      spawnImpl: impl,
      waitImpl: async () => true,
      workDir: dir,
      maxRestarts: 2,
      onEvent: (e) => events.push(e),
    });
    await core.start({}, 1080);
    for (let i = 0; i < 4; i += 1) {
      spawned.filter((p) => p.args[0] === 'run').at(-1)?.exit(1, null);
      await vi.advanceTimersByTimeAsync(9000);
    }
    expect(events.filter((e) => e.type === 'failed')).toHaveLength(1);
    expect(spawned.filter((p) => p.args[0] === 'run')).toHaveLength(3); // 首次 + 2 次重启
    vi.useRealTimers();
    await core.stop();
  });

  it('主动 stop 之后不再重启', async () => {
    const events: CoreEvent[] = [];
    const { impl, spawned } = fakeSpawner({ checkExit: 0 });
    const core = new Core({
      binary: 'sing-box',
      spawnImpl: impl,
      waitImpl: async () => true,
      workDir: dir,
      onEvent: (e) => events.push(e),
    });
    await core.start({}, 1080);
    await core.stop();
    spawned[1]?.exit(0, 'SIGTERM');
    await new Promise((r) => setTimeout(r, 50));
    expect(events.filter((e) => e.type === 'restarting')).toHaveLength(0);
  });
});
