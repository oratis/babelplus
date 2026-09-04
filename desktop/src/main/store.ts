/**
 * 落盘状态：会话 token 与偏好。
 *
 * ⚠️ **token 落盘是一个自觉的取舍**，不是疏忽：不落盘就没有「开机自动连接」，
 * 而这个产品的第一场景是「落地当晚打开笔记本就要能用」。代价写在 README：
 * 拿到这台机器的人可以拿走会话。缓解只有一条 —— 文件权限 0600，且**只存会话 token，
 * 不存密码**；重置订阅即全部失效。
 *
 * 用一个 JSON 文件而不是 electron-store：多一个依赖换来的只是同样的一个 JSON 文件。
 */
import { mkdir, readFile, writeFile, rename } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { DEFAULT_PREFS, type Prefs } from '../shared/types.ts';

export interface PersistedState {
  readonly token: string | null;
  readonly prefs: Prefs;
  /** 上次成功连接的时间，仅用于界面显示。 */
  readonly lastConnectedAt: string | null;
  /** 首次运行引导是否走完。 */
  readonly onboarded: boolean;
}

export const EMPTY_STATE: PersistedState = {
  token: null,
  prefs: DEFAULT_PREFS,
  lastConnectedAt: null,
  onboarded: false,
};

function coercePrefs(v: unknown): Prefs {
  if (typeof v !== 'object' || v === null) return DEFAULT_PREFS;
  const r = v as Record<string, unknown>;
  const list = (x: unknown): string[] =>
    Array.isArray(x) ? x.filter((s): s is string => typeof s === 'string') : [];
  return {
    mode: r['mode'] === 'everything' ? 'everything' : 'smart',
    alwaysProxy: list(r['alwaysProxy']),
    neverProxy: list(r['neverProxy']),
    outbound: typeof r['outbound'] === 'string' ? r['outbound'] : null,
    launchAtStart: r['launchAtStart'] === true,
  };
}

export class Store {
  private readonly file: string;
  private state: PersistedState = EMPTY_STATE;
  /**
   * 🔴 写入必须串行。两处会同时写：`Api` 的 `setToken` 回调（登录成功那一刻，同步触发）
   * 与调用方自己的 `update`。并发时两次写用同一个临时文件名 —— 先完成的那次 rename 把它移走，
   * 后一次 rename 就 ENOENT，**而那次丢掉的正好可能是 token**。
   * 由这条队列 + 每次唯一的临时名一起挡住（`store.test.ts` 有回归用例）。
   */
  private queue: Promise<unknown> = Promise.resolve();
  private seq = 0;

  constructor(dir: string, name = 'state.json') {
    this.file = join(dir, name);
  }

  get path(): string {
    return this.file;
  }

  async load(): Promise<PersistedState> {
    try {
      const raw = await readFile(this.file, 'utf8');
      const parsed = JSON.parse(raw) as Record<string, unknown>;
      this.state = {
        token: typeof parsed['token'] === 'string' ? parsed['token'] : null,
        prefs: coercePrefs(parsed['prefs']),
        lastConnectedAt: typeof parsed['lastConnectedAt'] === 'string' ? parsed['lastConnectedAt'] : null,
        onboarded: parsed['onboarded'] === true,
      };
    } catch {
      // 文件不存在 / 坏了：回到空状态。**不抛** —— 一个坏掉的偏好文件不该让浏览器打不开。
      this.state = EMPTY_STATE;
    }
    return this.state;
  }

  get current(): PersistedState {
    return this.state;
  }

  async update(patch: Partial<PersistedState>): Promise<PersistedState> {
    // 内存状态立刻合并（读的人马上就要用），落盘排进队列。
    this.state = { ...this.state, ...patch };
    const snapshot = this.state;
    this.seq += 1;
    const tmp = `${this.file}.${process.pid}.${this.seq}.tmp`;
    const write = this.queue.then(async () => {
      await mkdir(dirname(this.file), { recursive: true });
      // 先写临时文件再 rename：断电/崩溃时不会留下半个 JSON，而半个 JSON 会让下次启动回到空状态（=被登出）。
      await writeFile(tmp, `${JSON.stringify(snapshot, null, 2)}\n`, { mode: 0o600 });
      await rename(tmp, this.file);
    });
    // 队列本身不能因为某一次失败而断掉，否则后面的写全被拖住。
    this.queue = write.catch(() => undefined);
    await write;
    return this.state;
  }

  /** 等所有排队中的写落盘。登录之后必须调一次 —— 否则「刚登录就退出」会丢掉 token。 */
  async flush(): Promise<void> {
    await this.queue;
  }
}
