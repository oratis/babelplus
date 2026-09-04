/**
 * sing-box 进程的监护：check → spawn → 等端口 → 崩了重启（退避）→ 退出时收干净。
 *
 * 三条规矩，每一条都有对应的用例：
 *
 *  1. **check 不过就不启动，更不设代理。** 配置 schema 是上游的自由，我们不假装知道
 *     每个版本长什么样；随包的二进制自己说了算。这条挡住的是「配置错了但进程起来了、
 *     然后所有请求都失败」这种最难查的形态。
 *  2. **内核死了不撤代理。** 撤掉 = 静默直连 = 用户以为自己被保护着而实际没有
 *     （spec §3.3 规则 1 与 §9 代价 4 是同一条纪律）。所以进程死后代理仍指着那个死端口，
 *     页面会明确失败，界面同时打出横幅。
 *  3. **重启有上限。** 无限重启会把一个配置错误变成一台一直在 fork 的机器；
 *     到上限后进入 `failed`，等用户点重试。
 *
 * `spawnImpl` 与 `waitImpl` 可注入，所以整套逻辑在 Node 里可测，不需要真的 sing-box。
 */
import { spawn as nodeSpawn, type ChildProcess } from 'node:child_process';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { serializeConfig } from './config.ts';
import { waitForPort } from './ports.ts';

export interface SpawnedProcess {
  readonly pid: number | undefined;
  kill(): void;
  onExit(cb: (code: number | null, signal: string | null) => void): void;
  onStderr(cb: (line: string) => void): void;
}

export type SpawnImpl = (bin: string, args: readonly string[]) => SpawnedProcess;

export interface CoreOptions {
  /** sing-box 可执行文件路径。 */
  readonly binary: string;
  /** 起来算数的超时。 */
  readonly startTimeoutMs?: number;
  /** 最多重启几次（本次连接内）。 */
  readonly maxRestarts?: number;
  readonly spawnImpl?: SpawnImpl;
  readonly waitImpl?: (port: number, timeoutMs: number) => Promise<boolean>;
  /** 运行目录（放配置文件）。默认建临时目录。 */
  readonly workDir?: string;
  readonly onEvent?: (e: CoreEvent) => void;
}

export type CoreEvent =
  | { readonly type: 'started'; readonly port: number }
  | { readonly type: 'exited'; readonly code: number | null; readonly signal: string | null }
  | { readonly type: 'restarting'; readonly attempt: number; readonly delayMs: number }
  | { readonly type: 'failed'; readonly detail: string }
  | { readonly type: 'stderr'; readonly line: string };

function defaultSpawn(bin: string, args: readonly string[]): SpawnedProcess {
  const child: ChildProcess = nodeSpawn(bin, [...args], { stdio: ['ignore', 'ignore', 'pipe'] });
  return {
    pid: child.pid,
    kill: () => {
      child.kill('SIGTERM');
    },
    onExit: (cb) => child.on('exit', cb),
    onStderr: (cb) => {
      child.stderr?.setEncoding('utf8');
      child.stderr?.on('data', (chunk: string) => {
        for (const line of chunk.split('\n')) if (line.trim()) cb(line.trim());
      });
    },
  };
}

/** `sing-box check` 的结果。`ok=false` 时 `detail` 是二进制自己的报错，原样带给界面。 */
export interface CheckResult {
  readonly ok: boolean;
  readonly detail: string;
}

export async function checkConfig(binary: string, configPath: string, spawnImpl?: SpawnImpl): Promise<CheckResult> {
  const impl = spawnImpl ?? defaultSpawn;
  return new Promise<CheckResult>((resolve) => {
    let stderr = '';
    const p = impl(binary, ['check', '-c', configPath]);
    p.onStderr((line) => {
      stderr += `${line}\n`;
    });
    p.onExit((code) => resolve({ ok: code === 0, detail: stderr.trim() }));
  });
}

export class Core {
  private readonly opts: Required<Pick<CoreOptions, 'binary'>> & CoreOptions;
  private proc: SpawnedProcess | null = null;
  private dir: string | null = null;
  private configPath: string | null = null;
  private restarts = 0;
  private stopping = false;
  private port_: number | null = null;

  constructor(opts: CoreOptions) {
    this.opts = opts;
  }

  get port(): number | null {
    return this.port_;
  }

  get restartCount(): number {
    return this.restarts;
  }

  get running(): boolean {
    return this.proc !== null;
  }

  private emit(e: CoreEvent): void {
    this.opts.onEvent?.(e);
  }

  /**
   * 写配置 → check → 起进程 → 等端口。任一步失败都抛，且**不会留下半个跑着的进程**。
   * 抛出的 Error 上带 `reason`，调用方据此决定界面上说什么。
   */
  async start(config: Record<string, unknown>, port: number): Promise<void> {
    this.stopping = false;
    this.restarts = 0;
    this.dir = this.opts.workDir ?? (await mkdtemp(join(tmpdir(), 'bp-browser-')));
    this.configPath = join(this.dir, 'sing-box.json');
    await writeFile(this.configPath, serializeConfig(config), { mode: 0o600 });

    const check = await checkConfig(this.opts.binary, this.configPath, this.opts.spawnImpl);
    if (!check.ok) {
      const err = new Error(check.detail || 'sing-box check 失败');
      (err as { reason?: string }).reason = 'config-rejected';
      throw err;
    }
    this.port_ = port;
    await this.launch();
  }

  private async launch(): Promise<void> {
    const impl = this.opts.spawnImpl ?? defaultSpawn;
    const wait = this.opts.waitImpl ?? waitForPort;
    const p = impl(this.opts.binary, ['run', '-c', this.configPath ?? '']);
    this.proc = p;
    p.onStderr((line) => this.emit({ type: 'stderr', line }));
    p.onExit((code, signal) => {
      this.proc = null;
      this.emit({ type: 'exited', code, signal });
      if (!this.stopping) void this.onUnexpectedExit();
    });

    const ok = await wait(this.port_ ?? 0, this.opts.startTimeoutMs ?? 8000);
    if (!ok) {
      this.stopping = true;
      p.kill();
      this.proc = null;
      const err = new Error(`sing-box 起来了但 127.0.0.1:${this.port_} 没有开始接受连接`);
      (err as { reason?: string }).reason = 'port-unavailable';
      throw err;
    }
    this.emit({ type: 'started', port: this.port_ ?? 0 });
  }

  /** 非预期退出：退避重启，到上限后 failed。**期间不撤代理**（见文件头第 2 条）。 */
  private async onUnexpectedExit(): Promise<void> {
    const max = this.opts.maxRestarts ?? 5;
    if (this.restarts >= max) {
      this.emit({ type: 'failed', detail: `内核连续退出 ${this.restarts} 次，已停止重启` });
      return;
    }
    this.restarts += 1;
    const delayMs = Math.min(500 * 2 ** (this.restarts - 1), 8000);
    this.emit({ type: 'restarting', attempt: this.restarts, delayMs });
    await new Promise((r) => setTimeout(r, delayMs));
    if (this.stopping) return;
    try {
      await this.launch();
    } catch (cause) {
      this.emit({ type: 'failed', detail: cause instanceof Error ? cause.message : String(cause) });
    }
  }

  /** 停止并清理临时目录（配置里有节点凭据，不能留在盘上）。 */
  async stop(): Promise<void> {
    this.stopping = true;
    this.proc?.kill();
    this.proc = null;
    this.port_ = null;
    if (this.dir && !this.opts.workDir) {
      await rm(this.dir, { recursive: true, force: true }).catch(() => undefined);
    }
    this.dir = null;
    this.configPath = null;
  }
}
