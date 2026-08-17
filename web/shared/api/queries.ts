/**
 * 一小组**已接线**的只读查询。
 *
 * 这个文件的作用不是「封装 API」（页面直接用 `client.GET(...)` 就够了），
 * 而是**让生成物真的被类型检查器走一遍**：如果 `openapi.yaml` 改了字段名或路径，
 * `pnpm typecheck` 会在这里红掉，而不是等到运行时。
 * 每加一个页面接线时，把对应查询挪到这里或直接在页面里调，两种都可以。
 *
 * 写操作（下单、支付、踢设备、后台的 16 条危险操作）**故意不放在这里** ——
 * 它们需要二次确认、幂等键与审计上下文，接线时逐个设计，不适合先摆一个方便调用的壳。
 */
import type { components } from './schema.d.ts';
import { type ApiClient, unwrap, unwrapWithMeta, type Meta } from './client.ts';

export type CurrentUser = components['schemas']['CurrentUser'];
export type UserSubscription = components['schemas']['UserSubscription'];
export type Notice = components['schemas']['Notice'];
export type Plan = components['schemas']['Plan'];
export type UserDevice = components['schemas']['UserDevice'];
export type UsageSeries = components['schemas']['UsageSeries'];
export type DiagnoseResult = components['schemas']['DiagnoseResult'];

/** 当前用户 + 订阅摘要。`/dashboard` 与全局布局都要它。 */
export function getCurrentUser(client: ApiClient): Promise<CurrentUser> {
  return unwrap(client.GET('/api/v1/user/me'));
}

/** 订阅详情（链接、配额、重置日）。`/subscribe` 与 `/dashboard` 共用。 */
export function getSubscription(client: ApiClient): Promise<UserSubscription> {
  return unwrap(client.GET('/api/v1/user/subscription'));
}

/**
 * 公告。dashboard 取 3 条，`/notice` 取全部。
 * 公告兼作**域名广播位**（page-inventory §3.2.9），所以它的失败不能静默吞掉。
 */
export function listNotices(
  client: ApiClient,
  query: { limit?: number; cursor?: string } = {},
): Promise<{ data: Notice[]; meta: Meta }> {
  return unwrapWithMeta(client.GET('/api/v1/notices', { params: { query } }));
}

/** 套餐。`/plan` 用。 */
export function listPlans(client: ApiClient): Promise<Plan[]> {
  return unwrap(client.GET('/api/v1/plans'));
}

/**
 * 在线设备。⚠️ 契约明写口径是**按 IP** 不是按设备
 * —— 页面上必须写明这一点（page-inventory §3.2.3 第 1 条）。
 */
export function listDevices(client: ApiClient): Promise<UserDevice[]> {
  return unwrap(client.GET('/api/v1/user/devices'));
}

/** 用量曲线（P2）。新账号没有数据是常态，空态要按 §3.2.7 写。 */
export function getUsage(client: ApiClient, range: '7d' | '30d' | '90d' = '30d'): Promise<UsageSeries> {
  return unwrap(client.GET('/api/v1/user/usage', { params: { query: { range } } }));
}

/** 账号侧自助诊断（P2）。 */
export function getDiagnose(client: ApiClient): Promise<DiagnoseResult> {
  return unwrap(client.GET('/api/v1/user/diagnose'));
}
