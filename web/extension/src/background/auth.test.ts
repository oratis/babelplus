import { describe, expect, it, vi } from 'vitest';
import { createAuthListener, decideAuth, MAX_AUTH_ATTEMPTS, type ProxyCredential } from './auth.ts';

const creds: ProxyCredential[] = [{ host: 'a.example.invalid', port: 443, username: 'u1', password: 'p1' }];
const challenge = (over: Partial<Parameters<typeof decideAuth>[0]> = {}) => ({
  requestId: 'r1',
  isProxy: true,
  challenger: { host: 'a.example.invalid', port: 443 },
  ...over,
});

describe('decideAuth', () => {
  it('只回应代理质询：站点自己的 401 交还给 Chrome', () => {
    expect(decideAuth(challenge({ isProxy: false }), creds, new Map())).toEqual({});
  });
  it('只回应我们的端点：别的代理不归我们管（含大小写与端口）', () => {
    expect(decideAuth(challenge({ challenger: { host: 'other.invalid', port: 443 } }), creds, new Map())).toEqual({});
    expect(decideAuth(challenge({ challenger: { host: 'a.example.invalid', port: 8443 } }), creds, new Map())).toEqual({});
    expect(decideAuth(challenge({ challenger: { host: 'A.EXAMPLE.INVALID', port: 443 } }), creds, new Map())).toEqual({
      authCredentials: { username: 'u1', password: 'p1' },
    });
  });
  it('同一请求最多回填两次，第三次取消并报告凭据被拒', () => {
    const attempts = new Map<string, number>();
    const onRejected = vi.fn();
    for (let i = 0; i < MAX_AUTH_ATTEMPTS; i += 1) {
      expect(decideAuth(challenge(), creds, attempts, onRejected)).toHaveProperty('authCredentials');
    }
    expect(decideAuth(challenge(), creds, attempts, onRejected)).toEqual({ cancel: true });
    expect(onRejected).toHaveBeenCalledTimes(1);
    // 计数已清，另一个请求从零开始
    expect(decideAuth(challenge({ requestId: 'r2' }), creds, attempts, onRejected)).toHaveProperty('authCredentials');
  });
});

describe('createAuthListener', () => {
  it('异步取凭据后回调；取失败时回空对象而不是让请求挂死', async () => {
    const listener = createAuthListener({ getCredentials: async () => creds, onRejected: () => undefined });
    const decision = await new Promise((resolve) => listener(challenge(), resolve));
    expect(decision).toEqual({ authCredentials: { username: 'u1', password: 'p1' } });

    const failing = createAuthListener({ getCredentials: async () => Promise.reject(new Error('storage gone')), onRejected: () => undefined });
    const fallback = await new Promise((resolve) => failing(challenge(), resolve));
    expect(fallback).toEqual({});
  });
});
