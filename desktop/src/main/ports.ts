/**
 * 回环端口分配。
 *
 * 用「向内核要一个临时端口、读出来、关掉、再让 sing-box 绑它」这个老办法。
 * 它有一个已知的竞态窗口（关掉到 sing-box 绑上之间，别的进程可能抢走），
 * 但替代方案（固定端口）的代价更大：固定端口会与别的软件撞车，而撞车的现象是
 * 「装了某某软件之后浏览器就上不了网」—— 那种报障没人查得动。
 *
 * 竞态由调用方兜：`core.ts` 在 sing-box 起不来时会换一个端口重试。
 */
import { createServer } from 'node:net';

export async function pickLoopbackPort(): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const srv = createServer();
    srv.unref();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address();
      if (addr === null || typeof addr === 'string') {
        srv.close(() => reject(new Error('拿不到临时端口')));
        return;
      }
      const { port } = addr;
      srv.close(() => resolve(port));
    });
  });
}

/** 等一个回环端口变得可连。用于「内核起来了没有」——**不轮询日志**，日志格式是上游的自由。 */
export async function waitForPort(port: number, timeoutMs: number, stepMs = 100): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  const { connect } = await import('node:net');
  while (Date.now() < deadline) {
    const ok = await new Promise<boolean>((resolve) => {
      const sock = connect({ port, host: '127.0.0.1' });
      const done = (v: boolean) => {
        sock.destroy();
        resolve(v);
      };
      sock.once('connect', () => done(true));
      sock.once('error', () => done(false));
      sock.setTimeout(Math.min(stepMs * 5, 1000), () => done(false));
    });
    if (ok) return true;
    await new Promise((r) => setTimeout(r, stepMs));
  }
  return false;
}
