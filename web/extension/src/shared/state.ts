/**
 * 把 service worker 的 Snapshot 折成 popup 的**八个状态**（spec §3.5 的表，mockup 逐个画过）。
 *
 * 优先级从上到下，每一条都是一个真实的产品判断，不是实现顺序：
 *  1. 未登录 —— 别的什么都谈不上
 *  2. 已过期 —— 比「用尽」优先：过期后配额数字没有意义，且续费与加油是两个不同动作
 *  3. 用尽 —— 服务端已切断（三态计时实测 17 s），开关必须禁用，不能让用户以为点一下就通
 *  4. 全部端点不可达 —— 故障要响（spec §3.3 规则 1），且它比「连接中」优先：探测已经结束了
 *  5. 连接中
 *  6. 将尽（< 10%）—— 主按钮换成 Top up，这是唯一的一处 upsell
 *  7. 已连接 / 8. 已登录关闭
 */
import { quotaView, type QuotaView } from './quota.ts';
import type { NoRouteReason, ProbeResult, Snapshot } from './types.ts';

export type UiState =
  | { readonly kind: 'signed-out' }
  | { readonly kind: 'expired'; readonly quota: QuotaView }
  | { readonly kind: 'exhausted'; readonly quota: QuotaView }
  | {
      readonly kind: 'no-route';
      readonly reason: NoRouteReason;
      readonly lastSuccessAt: string | null;
      readonly failedEndpoints: number;
    }
  | { readonly kind: 'connecting'; readonly probes: readonly ProbeResult[] }
  | { readonly kind: 'low'; readonly quota: QuotaView; readonly connected: boolean; readonly region: string | null }
  | {
      readonly kind: 'on';
      readonly quota: QuotaView | null;
      readonly region: string | null;
      readonly exitIp: string | null;
      readonly sessionBytes: number | null;
    }
  | { readonly kind: 'off'; readonly quota: QuotaView | null; readonly region: string | null };

export function deriveUiState(snapshot: Snapshot, now: number = Date.now()): UiState {
  if (!snapshot.signedIn) return { kind: 'signed-out' };

  const quota = quotaView(snapshot.subscription, now);
  if (quota?.expired) return { kind: 'expired', quota };
  if (quota?.exhausted) return { kind: 'exhausted', quota };

  const conn = snapshot.connection;
  if (conn.status === 'no-route') {
    return {
      kind: 'no-route',
      reason: conn.reason ?? 'all-endpoints-failed',
      lastSuccessAt: conn.lastSuccessAt,
      failedEndpoints: conn.failedEndpoints,
    };
  }
  if (conn.status === 'connecting') return { kind: 'connecting', probes: snapshot.probes };

  const region = conn.region ?? snapshot.prefs.region;
  if (quota?.low) return { kind: 'low', quota, connected: conn.status === 'on', region };

  if (conn.status === 'on') {
    const sessionBytes =
      quota && conn.usedAtConnect !== null ? Math.max(0, quota.usedBytes - conn.usedAtConnect) : null;
    return { kind: 'on', quota, region, exitIp: conn.exitIp, sessionBytes };
  }
  return { kind: 'off', quota, region };
}
