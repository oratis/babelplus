/**
 * `ErrorCode` → 用户可读文案的**唯一一张表**。
 *
 * 这张表是 README §8 那条未决项的落地：接线之前已经有两处**手抄的**局部文案表
 * （`user/.../LoginPage.tsx` 的 `loginErrorCopy`、`admin/src/lib/iap.ts` 的
 * `forbiddenDescription`），当时记了一句「第三处出现之前应当收成一张表」。
 * 这一轮要接的页面有五个，每个都要按 code 分支 —— 再手抄五份就没救了。
 *
 * 关键在 `satisfies Record<ErrorCode, ErrorCopy>`：`ErrorCode` 是**从契约生成的**
 * 联合类型，契约里新增一个码而这里没补文案，`pnpm typecheck` 立刻红。
 * 手抄的表做不到这一点 —— 抄漏一个码只会静默落到兜底分支，
 * 用户看到「未知错误」，而没有任何测试或构建会提醒。
 *
 * 页面仍然可以覆盖单个码的文案（`describeApiError` 的第二个参数）：
 * 同一个 `QUOTA_RATE_LIMITED`，登录页要说「解锁倒计时」，发验证码要说「多久后可重发」。
 * 覆盖是**逐码**的，不是整表替换，所以不会退回到「每页各写一套」。
 */
import type { ErrorCode } from './client.ts';
import { ApiError } from './client.ts';
import { ERROR_COPY } from '../src/lib/runtime-config.ts';

export interface ErrorCopy {
  readonly title: string;
  readonly description: string;
}

/**
 * 501 的错误码。
 *
 * ⚠️ 它**不在契约的 `ErrorCode` enum 里**，是 `api/cmd/server/main.go` 的
 * `responseErrorHandler` 直接写出来的字符串（`writeErr(…, 501, "NOT_IMPLEMENTED", …)`）。
 * 所以下面那张表不能包含它 —— 表的类型约束恰恰是「只装 enum 成员」。
 *
 * 前端要认它，是因为当前 128 个 operation 里有一大半是 501 stub，
 * 而「这个功能还没做」与「服务器挂了」对用户是两件完全不同的事：
 * 前者不该出现重试按钮，也不该引导到状态页。
 */
export const NOT_IMPLEMENTED_CODE = 'NOT_IMPLEMENTED';

/** 这次失败是不是「端点还没实现」。状态码与错误码都认，任一命中即可。 */
export function isNotImplemented(error: ApiError): boolean {
  return error.status === 501 || error.code === NOT_IMPLEMENTED_CODE;
}

/**
 * 全量文案表。**每一条都必须能独立成句**，因为它可能被单独显示在一个错误条里。
 *
 * 写法上守两条：
 *  - 不说「请求失败」这类没有信息量的话，一律说「发生了什么 + 下一步做什么」；
 *  - 不泄漏账号是否存在（防枚举）—— 见 `AUTH_INVALID_CREDENTIALS`。
 */
export const CODE_COPY = {
  AUTH_INVALID_CREDENTIALS: {
    title: '邮箱或密码不正确',
    description: '两者中有一个不对。为了防止账号被逐个试探，我们不说是哪一个。',
  },
  AUTH_TOKEN_EXPIRED: {
    title: '登录状态已过期',
    description: '重新登录一次就好，之前填的内容可能需要重填。',
  },
  AUTH_TOKEN_INVALID: {
    title: '这个凭据已经失效了',
    description: '可能是登录过期，也可能是链接用过一次了。重新来一遍即可。',
  },
  AUTH_TOTP_REQUIRED: {
    title: '需要两步验证码',
    description: '这个账号开启了两步验证，请输入验证器里当前的 6 位数字。',
  },
  AUTH_TOTP_INVALID: {
    title: '两步验证码不正确',
    description: '验证器里的数字每 30 秒变一次，等下一轮再输一次。',
  },
  AUTH_PERMISSION_DENIED: {
    title: '这个账号不能做这件事',
    description: '账号可能已被封禁，或者这项操作不对当前账号开放。重试不会有帮助。',
  },
  VALIDATION_FAILED: {
    title: '填写有误',
    description: '有字段没通过校验。按下面的提示改一处再提交。',
  },
  VALIDATION_MALFORMED_BODY: {
    title: '这个请求我们读不懂',
    description: '多半是页面版本过旧。刷新一次再试，还不行就把请求号发给我们。',
  },
  RESOURCE_NOT_FOUND: {
    title: '找不到这个东西',
    description: '它可能已经被删除，或者地址里的编号不对。',
  },
  STATE_CONFLICT: {
    title: '当前状态下做不了这一步',
    description: '这件事可能已经完成过了。刷新一次看看最新状态。',
  },
  STATE_IDEMPOTENCY_MISMATCH: {
    title: '这次提交和上一次不一致',
    description: '同一张单子被用不同的内容提交了两次。刷新页面重新来一次。',
  },
  QUOTA_RATE_LIMITED: {
    title: '操作太频繁了',
    description: '短时间内做了太多次，稍等一会儿再试。',
  },
  QUOTA_DEVICE_LIMIT: {
    title: '设备数已达上限',
    description: '先把不用的设备踢下线，或者升级到设备数更多的套餐。',
  },
  PAYMENT_UNDERPAID: {
    title: '付款金额不足',
    description: '收到的金额少于订单金额。补齐差额或联系我们处理。',
  },
  PAYMENT_ORDER_EXPIRED: {
    title: '这张订单已经过期',
    description: '重新下一单即可，过期的订单不会扣款。',
  },
  PAYMENT_SIGNATURE_INVALID: {
    title: '支付回调校验没通过',
    description: '这笔回调不可信，我们没有采信它。如果你确实付了款，把请求号发给我们。',
  },
  NODE_KEY_INVALID: {
    title: '节点密钥无效',
    description: '这是节点侧的凭据问题，与你的账号无关。',
  },
  NODE_SCOPE_DENIED: {
    title: '节点密钥没有这项权限',
    description: '这是节点侧的授权问题，与你的账号无关。',
  },
  NODE_ID_MISMATCH: {
    title: '节点编号对不上',
    description: '这是节点侧的配置问题，与你的账号无关。',
  },
  INTERNAL_ERROR: {
    title: '我们这边出了问题',
    description: '不是你的账号或网络的问题，稍后再试一次。',
  },
  INTERNAL_DEPENDENCY_DOWN: {
    title: '一个依赖的服务暂时不可用',
    description: '不是你的账号或网络的问题。恢复后会自动可用。',
  },
} satisfies Record<ErrorCode, ErrorCopy>;

/**
 * 挑一条文案。优先级：**逐码覆盖 → 全量表 → 501 → 五类归一**。
 *
 * 最后一档（按 `kind`）不可省：网络层失败根本没有 `code`（`ApiError.code` 是 `NETWORK`），
 * 非信封响应是 `UNKNOWN`，两者都不在表里。
 */
export function describeApiError(
  error: ApiError,
  overrides: Readonly<Partial<Record<string, ErrorCopy>>> = {},
): ErrorCopy {
  const override = overrides[error.code];
  if (override) return override;

  if (isNotImplemented(error)) {
    return {
      title: '这个功能还没开放',
      description: '后端接口还在实现中。重试没有用，做好了会直接可用，不用你再做什么。',
    };
  }

  const known = (CODE_COPY as Readonly<Record<string, ErrorCopy>>)[error.code];
  if (known) return known;

  const byKind = ERROR_COPY[error.kind];
  return { title: byKind.title, description: byKind.description };
}

/**
 * 任意异常 → `ApiError`。
 *
 * 每个提交处理函数都要写一遍 `cause instanceof ApiError ? … : new ApiError(…)`，
 * 抄五遍不如放一处 —— 而且抄错的那一遍会把真实的 `ApiError` 吞成「未知错误」。
 */
export function asApiError(cause: unknown, fallbackMessage: string): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({ status: 0, code: 'UNKNOWN', message: fallbackMessage, cause });
}

/**
 * 取 `VALIDATION_FAILED` 的字段级明细里某个字段的 `reason`。
 *
 * 🔴 分支必须按 **`reason`** 走，不能按 `message` —— 与「禁止匹配 message」同一条理由
 * （api-contract §2.3）。后端给的 reason 是稳定的英文常量
 * （`invite_code_exhausted` / `email_code_expired` / …），message 是随时可能改的中文。
 */
export function detailReason(error: ApiError, field: string): string | null {
  const details = error.details;
  if (!details) return null;
  for (const d of details) {
    if (d.field === field) return d.reason;
  }
  return null;
}
