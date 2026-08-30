/**
 * 注册 / 找回 / 重置三页共用的零件。
 *
 * 放在 `routes/auth/` 而不是 `components/` 或 `shared/src/ui`：这三页是同一条链路
 * （注册要发码、找回要发信、重置失效后要**在本页**重发），它们共享的是**这条链路的规则**，
 * 不是通用 UI。放进公共 UI 包会让「口令长度」「邀请码归一化」这类与后端逐行对齐的常量
 * 离它们的事实源（`api/internal/handler/auth.go`）更远。
 *
 * 🔴 本文件里的每一个常量都能在后端找到对应行。**改这里之前先改那里**，
 * 前后端两套规则的具体后果是：前端拦下一个后端完全接受的输入（用户被自己人挡在门外），
 * 或者前端放过一个后端一定拒绝的输入（一次注定失败的往返，且错误文案是兜底的那句）。
 */
import { useCallback, useEffect, useState } from 'react';
import { ApiError } from '@babelplus/shared/api';
import { runtimeConfig } from '@babelplus/shared';

/* ─────────────────────────── 口令策略 ─────────────────────────── */

/**
 * 口令长度区间。事实源：`handler/auth.go` 的 `minPasswordRunes` / `maxPasswordRunes`
 * （8–128），且那两个常量的注释写明与 openapi 的 minLength/maxLength 一致。
 *
 * **后端只校验长度，没有「必须含大小写数字符号」那类组合规则**（`validPassword` 的注释
 * 论证过：组合规则会把用户推向 `Passw0rd!` 这种可预测形态）。所以前端也不许加，
 * 加了就是前端独有的一道闸 —— 用户改到满意的口令，提交时才发现被自己这边挡住。
 */
export const PASSWORD_MIN_LENGTH = 8;
export const PASSWORD_MAX_LENGTH = 128;

/**
 * 按**码位**数，不是 `String.length`。后端用的是 `utf8.RuneCountInString`：
 * 一个中文字在 Go 那边算 1，在 JS 的 `.length` 里也算 1，但 emoji（代理对）
 * 在 Go 那边算 1、`.length` 算 2。用 `.length` 会让「8 个 emoji 的口令」
 * 前端放行、后端拒收 —— 一个只有少数人踩得到、且没人想得到原因的 422。
 */
export function countRunes(text: string): number {
  return [...text].length;
}

export type PasswordProblem = 'too-short' | 'too-long';

/** 口令长度问题；没问题返回 `null`。**这是前端唯一的口令硬校验。** */
export function passwordProblem(password: string): PasswordProblem | null {
  const n = countRunes(password);
  if (n < PASSWORD_MIN_LENGTH) return 'too-short';
  if (n > PASSWORD_MAX_LENGTH) return 'too-long';
  return null;
}

export function passwordProblemText(problem: PasswordProblem): string {
  return problem === 'too-short'
    ? `密码至少 ${PASSWORD_MIN_LENGTH} 个字符。`
    : `密码最多 ${PASSWORD_MAX_LENGTH} 个字符。`;
}

/**
 * 口令强度估算。**纯本地计算，不调用任何第三方服务**
 * （page-inventory §3.2.1 的注册页要求里有强度条；ADR 0003 §3.2 禁止引入
 * 可达性未知的第三方主机名 —— 一个「查这个口令泄漏过没有」的在线接口
 * 恰好是那类东西，而且它还要把口令的哈希前缀发出去）。
 *
 * 🔴 强度**只是提示，不是提交门槛**。返回 0 也照样能提交，只要长度合法 ——
 * 因为后端就是这么判的（见 `PASSWORD_MIN_LENGTH` 的注释）。
 */
export interface PasswordStrength {
  /** 0 = 不合格（长度不够），1 弱 / 2 中 / 3 强。 */
  readonly score: 0 | 1 | 2 | 3;
  readonly label: string;
}

export function passwordStrength(password: string): PasswordStrength {
  const n = countRunes(password);
  if (n < PASSWORD_MIN_LENGTH) return { score: 0, label: '太短' };

  // 只数「有几类字符」，不要求「必须有哪一类」—— 后者就是被否掉的组合规则。
  const classes = [/[a-z]/, /[A-Z]/, /[0-9]/, /[^a-zA-Z0-9]/].filter((re) => re.test(password)).length;
  const long = n >= 12;
  const veryLong = n >= 16;

  // 长度的权重高于字符种类：一个 20 位的全小写口令比 8 位的 `P@ssw0rd` 难猜得多。
  if (veryLong || (long && classes >= 3)) return { score: 3, label: '强' };
  if (long || classes >= 3) return { score: 2, label: '中' };
  return { score: 1, label: '弱' };
}

/** 强度条。`score = 0` 时不渲染 —— 那时页面上已经有一条「太短」的硬提示，两条会互相打架。 */
export function PasswordStrengthBar({ password }: { password: string }) {
  if (password === '') return null;
  const { score, label } = passwordStrength(password);
  if (score === 0) return null;

  const tone = score === 3 ? 'bg-ok' : score === 2 ? 'bg-warn' : 'bg-danger';
  return (
    <div className="mt-2">
      <div className="flex items-center gap-2">
        <div className="flex h-1 flex-1 gap-1" aria-hidden="true">
          {[1, 2, 3].map((slot) => (
            <span
              key={slot}
              className={`h-full flex-1 rounded-full ${slot <= score ? tone : 'bg-line'}`}
            />
          ))}
        </div>
        <span className="text-xs text-fg-muted">强度：{label}</span>
      </div>
      {/* 强度只是提示。写出来是为了让用户知道「弱」也能提交，不必在这里反复试。 */}
      <p className="mt-1 text-xs text-fg-subtle">
        更长比更复杂有用。强度弱也能注册，我们只强制长度。
      </p>
    </div>
  );
}

/* ─────────────────────────── 邀请码 ─────────────────────────── */

/**
 * 长度区间的事实源：`handler/auth.go` 的 `plausibleInviteCode`（4–32）。
 * 后端对区间外的码**不查库**直接判 invalid，所以前端也不必为这种输入发请求。
 */
export const INVITE_CODE_MIN_LENGTH = 4;
export const INVITE_CODE_MAX_LENGTH = 32;

/**
 * 归一化：大写 + 去首尾空白。与 `handler/auth.go` 的 `normalizeInviteCode` 逐字对应。
 *
 * 为什么前端也要做一遍：`0003_accounts.up.sql` 注明码入库即为大写。
 * 用户从邮件/聊天窗口复制时，大小写与前后空格都可能变样 ——
 * 不归一化就把原样送出去，后端虽然也会归一化（所以注册不会错），
 * 但**页面上的字符数校验与「看起来像不像一个码」的判断会用错的值**，
 * 用户会看到「邀请码无效」而他手里的码其实完全有效。
 */
export function normalizeInviteCode(raw: string): string {
  return raw.trim().toUpperCase();
}

/** 长度落在区间内才值得去查 —— 与后端 `plausibleInviteCode` 同一条判断。 */
export function plausibleInviteCode(code: string): boolean {
  return code.length >= INVITE_CODE_MIN_LENGTH && code.length <= INVITE_CODE_MAX_LENGTH;
}

/* ─────────────────────────── 限流倒计时 ─────────────────────────── */

/**
 * 契约给 `/auth/email-code` 的「同一邮箱两次间隔 ≥ 60 s」
 * （api-contract §10.1，实现在 `handler/auth.go` 的 `emailCodeMinGap`）。
 *
 * 🔴 这个值可以写在前端，而 429 的秒数**不可以**（必须读 `Retry-After`）——
 * 两者的区别是：60 秒是契约写死的规则，任何时候都成立；
 * 而 429 的剩余秒数是服务端此刻的状态，前端猜一个出来就会在用户眼皮底下走错
 * （`LoginPage` 那条注释说的是同一件事）。
 * 发码成功后本地起 60 秒，省掉一次注定 429 的往返，不是在编秒数。
 */
export const EMAIL_CODE_MIN_GAP_SECONDS = 60;

export interface Countdown {
  /** 剩余秒数；`null` = 没有在倒计时（也包括「读不到 Retry-After」）。 */
  readonly seconds: number | null;
  /** 传 `undefined` / `null` / `<= 0` 一律当作「不显示倒计时」。 */
  start(seconds: number | null | undefined): void;
  clear(): void;
}

/** 秒级倒计时。三页都要用（429、发码冷却），所以只实现一次。 */
export function useCountdown(): Countdown {
  const [seconds, setSeconds] = useState<number | null>(null);

  useEffect(() => {
    if (seconds === null || seconds <= 0) return;
    const timer = window.setInterval(() => {
      setSeconds((prev) => (prev === null || prev <= 1 ? null : prev - 1));
    }, 1000);
    return () => window.clearInterval(timer);
  }, [seconds]);

  const start = useCallback((next: number | null | undefined) => {
    setSeconds(next === undefined || next === null || next <= 0 ? null : Math.floor(next));
  }, []);

  const clear = useCallback(() => setSeconds(null), []);

  return { seconds, start, clear };
}

/* ─────────────────────────── 错误归一 ─────────────────────────── */

/** 非 `ApiError` 的异常（组件自己抛的、第三方抛的）也要能进同一张文案表。 */
export function asApiError(cause: unknown, fallbackMessage: string): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({ status: 0, code: 'UNKNOWN', message: fallbackMessage, cause });
}

/**
 * 501 的错误码。
 *
 * ⚠️ 它**不在** openapi 的 `ErrorCode` 枚举里 —— 它由 `api/cmd/server/main.go` 的
 * `responseErrorHandler` 直接写出（`writeErr(..., 501, "NOT_IMPLEMENTED", ...)`）。
 * 所以类型上它只是一个普通字符串，`ApiError.code` 恰好是 `string` 而不是枚举，接得住。
 *
 * 为什么每一页都要处理它：本仓大多数端点当前落在 `Unimplemented` 的 501 上。
 * 不认这个码的话，用户看到的是兜底那句「未知错误」，然后重试、再重试、开工单 ——
 * 而正确的信息是「这个功能还没上线，重试没有用」。
 */
export const NOT_IMPLEMENTED_CODE = 'NOT_IMPLEMENTED';

export interface ErrorCopy {
  readonly title: string;
  readonly description: string;
}

/**
 * 三页共用的**兜底**文案分支：501、5xx、离线、以及没有 code 可依的情况。
 *
 * 每一页仍然各有一张自己的 code 表（那里的分支是这一页独有的，且必须逐条对着
 * page-inventory §3.2.1 写）；这里只收各页都一样的那几条，避免抄三遍抄漏一条。
 */
export function fallbackErrorCopy(error: ApiError): ErrorCopy {
  if (error.code === NOT_IMPLEMENTED_CODE) {
    return {
      title: '该功能尚未开放',
      description: '这一步还没有上线，重试没有用。可以先做别的，或者联系我们问问进度。',
    };
  }
  switch (error.code) {
    case 'VALIDATION_MALFORMED_BODY':
      return { title: '请求格式不对', description: '这多半是我们这边的问题，请把请求号发给我们。' };
    case 'INTERNAL_ERROR':
    case 'INTERNAL_DEPENDENCY_DOWN':
      return { title: '我们这边出了问题', description: '不是你的输入有问题，稍后再试一次。' };
    default:
      break;
  }
  switch (error.kind) {
    case 'offline':
      return { title: '连不上面板', description: '当前网络到面板的连接失败。可以试试页脚的备用域名。' };
    case 'server':
      return { title: '我们这边出了问题', description: '不是你的账号或网络的问题，稍后再试一次。' };
    default:
      return { title: '这一步没能完成', description: error.message };
  }
}

/** 422 的 `details[]` 里取某个字段的 `reason`。契约形状：`{field, reason}`。 */
export function detailReason(error: ApiError, field: string): string | null {
  const hit = error.details?.find((d) => d.field === field);
  return hit ? hit.reason : null;
}

/* ─────────────────────── 发信域名白名单引导 ─────────────────────── */

/**
 * 「把发信域名加白名单」引导。**这不是装饰**：
 *
 * - roadmap §5.2 2.C 第 3 条把「注册成功页与找回密码成功页引导用户把发信域名加进
 *   QQ 邮箱白名单」列为待办项，不是可选项；
 * - 依据是 QQ 官方文档原文：域名白名单下「该域名下各个邮箱发来的信件都将不受反垃圾规则的
 *   影响」，且**白名单优先级高于黑名单与反垃圾规则**
 *   （admin-support-docs §3.2 记录，user-journey §3.3 引用）；
 * - 而 ADR 0002 已把邮件从「配置项」升级为**唯一的失联恢复通道** ——
 *   收不到我们邮件的用户，就是域名被封那天必然失联的用户。
 *
 * 所以这一块出现在三个位置：注册的验证码步骤（60 秒后自动展开）、注册成功页、找回成功页。
 */
export function MailboxWhitelistGuide({
  defaultOpen = false,
  summary = '没收到？三步把发信域名加进白名单',
}: {
  defaultOpen?: boolean;
  summary?: string;
}) {
  const cfg = runtimeConfig();
  // 发信域名从 supportEmail 推出来，**不硬编码**。
  // TODO(P1): 运行时配置里还没有独立的 `sendingDomain` 字段，而 ESP 尚未选定
  //（roadmap §5.2 2.C 第 1 条：两家 ESP 互为备份，⏳）。选定后发信域名可能与
  // supportEmail 不同域，那时应当加一个配置项，而不是继续在这里推断。
  const sendingDomain = cfg.supportEmail.includes('@')
    ? cfg.supportEmail.slice(cfg.supportEmail.lastIndexOf('@') + 1)
    : '';

  return (
    <details
      open={defaultOpen}
      className="rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm leading-relaxed text-warn"
    >
      <summary className="cursor-pointer font-medium">{summary}</summary>

      <p className="mt-2">
        QQ / 163 / 126 邮箱经常把新的发信域名判成垃圾。加白名单是官方记载的、优先级高于
        反垃圾规则的一步 —— 做一次，以后所有账号邮件都不会再被拦。
      </p>

      <ol className="mt-2 list-decimal space-y-1 pl-5">
        <li>
          打开 QQ 邮箱网页版 → <span className="font-medium">设置 → 反垃圾</span>
        </li>
        <li>
          在<span className="font-medium">「设置域名白名单」</span>里填入{' '}
          {sendingDomain ? (
            <code className="font-mono">{sendingDomain}</code>
          ) : (
            // 编不出来的东西就不编：域名池尚未确定，写一个假域名会让用户把白名单
            // 加到一个我们永远不会用的域名上 —— 比不给这一步更糟。
            <span className="font-medium">我们邮件里的发件人域名（收到后照抄 @ 后面那一段）</span>
          )}
        </li>
        <li>保存，再回到这一页重新发送一次</li>
      </ol>

      <p className="mt-2">
        163 / 126 在「设置 → 反垃圾 → 白名单」里，位置类似。先去垃圾箱看一眼也值得 ——
        它可能已经在那里了。
      </p>

      {/* TODO(P1): tutorials-spec §2 的「账户与账单」四篇里还没有「收不到邮件怎么办」这一篇，
          所以这里链到教程站首页而不是一个具体 slug —— 链到一份不存在的页面比不链更糟。
          地址从运行时配置的 docsUrl 拼，不硬编码（ADR 0003 §5：加镜像不重新构建）。 */}
      {cfg.docsUrl ? (
        <p className="mt-2">
          <a
            href={cfg.docsUrl}
            className="underline underline-offset-2"
            rel="noreferrer noopener"
            target="_blank"
          >
            教程站还有更详细的图文说明
          </a>
        </p>
      ) : null}
    </details>
  );
}
