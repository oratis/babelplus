/**
 * refresh 单飞的测试。
 *
 * 为什么这一块值得单独测：后端的 refresh 是**一次性轮换**
 * （`handler/auth.go` 的 `RefreshToken`：同一个事务里签发新会话 + 把旧会话 revoke 掉），
 * 而它签发的 `access_token` 与 `refresh_token` 是同一个值。
 * 两个并发的 401 各发一次 refresh → 第二次用的是第一次刚作废的 token → 401 → 用户掉线。
 * 也就是说「只发一次」不是性能优化，是正确性；用例写在这里就是为了让以后有人
 * 「顺手去掉这层 promise 复用」时 CI 会红。
 */
import { describe, expect, it, vi } from 'vitest';
import {
  createSessionManager,
  memorySessionStore,
  requestSessionRefresh,
  type SignOutReason,
} from './session.ts';

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('createSessionManager —— 单飞', () => {
  it('并发 5 个 401 只触发一次 refresh，且全部拿到同一枚新 token', async () => {
    const gate = deferred<string | null>();
    const refresh = vi.fn(() => gate.promise);
    const manager = createSessionManager({ store: memorySessionStore('old'), refresh });

    const waiters = Array.from({ length: 5 }, () => manager.ensureFreshToken('old'));
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(manager.isRefreshing()).toBe(true);

    gate.resolve('new');
    const results = await Promise.all(waiters);

    expect(results).toEqual(['new', 'new', 'new', 'new', 'new']);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(refresh).toHaveBeenCalledWith('old');
    expect(manager.getToken()).toBe('new');
    expect(manager.isRefreshing()).toBe(false);
  });

  it('刷完之后的下一轮会重新发（单飞是「同时」而不是「只此一次」）', async () => {
    const refresh = vi.fn(async (stale: string) => `${stale}+`);
    const manager = createSessionManager({ store: memorySessionStore('t0'), refresh });

    await manager.ensureFreshToken('t0');
    await manager.ensureFreshToken('t0+');

    expect(refresh).toHaveBeenCalledTimes(2);
    expect(manager.getToken()).toBe('t0++');
  });

  it('服务端拒绝 refresh（返回 null）时，等待者全部失败、会话被清空、登出回调只触发一次', async () => {
    const gate = deferred<string | null>();
    const reasons: SignOutReason[] = [];
    const manager = createSessionManager({
      store: memorySessionStore('dead'),
      refresh: () => gate.promise,
      onSignedOut: (reason) => reasons.push(reason),
    });

    const waiters = [manager.ensureFreshToken('dead'), manager.ensureFreshToken('dead')];
    gate.resolve(null);

    expect(await Promise.all(waiters)).toEqual([null, null]);
    expect(manager.getToken()).toBeNull();
    expect(reasons).toEqual(['refresh-rejected']);
  });

  it('refresh 抛异常（网络层没走到服务端）时**不登出** —— 跨境抖一下不该表现为被踢下线', async () => {
    const reasons: SignOutReason[] = [];
    const manager = createSessionManager({
      store: memorySessionStore('alive'),
      refresh: () => Promise.reject(new Error('fetch failed')),
      onSignedOut: (reason) => reasons.push(reason),
    });

    expect(await manager.ensureFreshToken('alive')).toBeNull();
    expect(manager.getToken()).toBe('alive');
    expect(reasons).toEqual([]);
  });

  it('失败请求带的是旧 token、而当前已经换过了 → 直接给新 token 重试，不再刷', async () => {
    const refresh = vi.fn(async () => 'never');
    const manager = createSessionManager({ store: memorySessionStore('current'), refresh });

    // 这正是「在途请求」的时序：它出发时手里是 stale，回来时 store 里已经是 current。
    expect(await manager.ensureFreshToken('stale')).toBe('current');
    expect(refresh).not.toHaveBeenCalled();
  });

  it('已登出（无 token）时不发 refresh —— 未登录访问受保护端点拿 401 是预期结果', async () => {
    const refresh = vi.fn(async () => 'nope');
    const manager = createSessionManager({ store: memorySessionStore(null), refresh });

    expect(await manager.ensureFreshToken(null)).toBeNull();
    expect(refresh).not.toHaveBeenCalled();
  });

  // 这一组钉的是「在途 refresh 的结果什么时候还作数」。
  // 摘掉 `inflight` 引用挡不住它 —— 那个 async 闭包还在飞，照样会执行到写回那一步。
  it('登出后，在途 refresh 的结果**不许写回** —— 否则用户点了登出却还留着一枚有效 token', async () => {
    const gate = deferred<string | null>();
    const manager = createSessionManager({
      store: memorySessionStore('old'),
      refresh: () => gate.promise,
    });

    const waiter = manager.ensureFreshToken('old');
    manager.signOut('user');
    expect(manager.getToken()).toBeNull();

    gate.resolve('new'); // 在途 refresh 这时才回来
    expect(await waiter).toBeNull(); // 别拿它去重试
    expect(manager.getToken()).toBeNull(); // 共用电脑上这一条就是会话泄漏
  });

  it('登出后在途 refresh 回来，也不许再发一次 onTokenChange', async () => {
    const gate = deferred<string | null>();
    const seen: Array<string | null> = [];
    const manager = createSessionManager({
      store: memorySessionStore('old'),
      refresh: () => gate.promise,
      onTokenChange: (token) => seen.push(token),
    });

    const waiter = manager.ensureFreshToken('old');
    manager.signOut('user');
    gate.resolve('new');
    await waiter;

    // 只有登出那一次 null；绝不能出现 [null, 'new'] 这种「登出又活过来」的序列。
    expect(seen).toEqual([null]);
  });

  it('重新登录后，上一轮在途 refresh 不覆盖新会话，而是把新 token 交回去重试', async () => {
    const gate = deferred<string | null>();
    const manager = createSessionManager({
      store: memorySessionStore('old'),
      refresh: () => gate.promise,
    });

    const waiter = manager.ensureFreshToken('old');
    manager.setToken('fresh-login'); // 用户在别处重新登录了
    gate.resolve('rotated-from-old');
    await waiter;

    expect(manager.getToken()).toBe('fresh-login');
    expect(await waiter).toBe('fresh-login');
  });

  it('订阅者能看到 token 变化与登出', async () => {
    const seen: Array<string | null> = [];
    const manager = createSessionManager({
      store: memorySessionStore('a'),
      refresh: async () => 'b',
    });
    const unsubscribe = manager.subscribe((token) => seen.push(token));

    await manager.ensureFreshToken('a');
    manager.signOut('user');
    unsubscribe();
    manager.setToken('c');

    expect(seen).toEqual(['b', null]);
  });
});

describe('requestSessionRefresh', () => {
  const options = { baseUrl: 'https://api.example', timeoutMs: 1_000 };

  it('把 token 作为 refresh_token 发出去，取回 access_token', async () => {
    const fetchImpl = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
      expect(String(url)).toBe('https://api.example/api/v1/auth/refresh');
      expect(init?.method).toBe('POST');
      expect(JSON.parse(String(init?.body))).toEqual({ refresh_token: 'old' });
      return new Response(
        JSON.stringify({ data: { access_token: 'new', refresh_token: 'new' }, meta: {} }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      );
    }) as unknown as typeof fetch;

    expect(await requestSessionRefresh('old', { ...options, fetchImpl })).toBe('new');
  });

  it('服务端拒绝（401）→ 返回 null，**不抛** —— 抛的语义是「没走到服务端」', async () => {
    const fetchImpl = (async () =>
      new Response(JSON.stringify({ error: { code: 'AUTH_TOKEN_INVALID', message: 'x' }, meta: {} }), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      })) as unknown as typeof fetch;

    expect(await requestSessionRefresh('old', { ...options, fetchImpl })).toBeNull();
  });

  it('网络层失败 → 抛出去，交给 SessionManager 判定为「别登出」', async () => {
    const fetchImpl = (async () => {
      throw new TypeError('fetch failed');
    }) as unknown as typeof fetch;

    await expect(requestSessionRefresh('old', { ...options, fetchImpl })).rejects.toThrow('fetch failed');
  });
});
