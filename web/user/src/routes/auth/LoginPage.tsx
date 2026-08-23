/**
 * `/auth/login` —— P1，无替代。page-inventory §3.1 #1、§3.2.1。
 *
 * 三条错态规则（§3.2.1 表格），**都不能省**：
 *   401 → 「邮箱或密码不正确」，**不区分是哪个错**（防账号枚举）
 *   429 → 显示解锁倒计时，而不是「请稍后再试」这种没信息量的话
 *   网络不可达 → §2.2 的备用域名块（页脚已常驻，这里再给一次就近的）
 *
 * 分支按 **`ErrorCode`** 而不是 message 走（api-contract §2.3 明令禁止匹配 message）。
 * 有一个分支特别容易漏：后端对**被封禁**的账号回的是 401 + `AUTH_PERMISSION_DENIED`
 * 而不是 403（`handler/auth.go` 的 Login：契约没给这个端点定义 403）。
 * 按 HTTP 状态码分支的写法会把封禁显示成「密码不对」，用户于是反复重试并开工单 ——
 * `middleware/user.go` 的注释里点名了这条来回。
 *
 * 这一页已接线，所以**不再读 `?state=` 调试开关**（README §7 代价 3：接线时必须删掉）。
 */
import { useEffect, useState, type FormEvent } from 'react';
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router';
import { Button, ErrorState, MirrorDomainList } from '@babelplus/shared/ui';
import { RETURN_TO_PARAM, safeReturnTo } from '@babelplus/shared';
import { ApiError } from '@babelplus/shared/api';
import { AuthCard, AuthTodo, Field, FormError } from '../../components/AuthForm.tsx';
import { useAuth } from '../../lib/auth.tsx';

/** 登录成功后没有合法 returnTo 时的落点。 */
const DEFAULT_LANDING = '/dashboard';

export default function LoginPage() {
  const { status, signIn } = useAuth();
  const navigate = useNavigate();
  const [params] = useSearchParams();

  // returnTo 在**进入这一页时**就校验掉。留到提交后再校验的话，
  // 中间任何一次早退路径（已登录直接跳）都会绕过校验。
  const returnTo = safeReturnTo(params.get(RETURN_TO_PARAM)) ?? DEFAULT_LANDING;

  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [lockSeconds, setLockSeconds] = useState<number | null>(null);

  // 429 的解锁倒计时。秒数取自 `Retry-After` 响应头 ——
  // 它在 CORS 的 Expose-Headers 里（api/internal/middleware/cors.go），所以跨域也读得到。
  // 读不到就**不显示倒计时**，绝不自己编一个秒数。
  useEffect(() => {
    if (lockSeconds === null || lockSeconds <= 0) return;
    const timer = window.setInterval(() => {
      setLockSeconds((prev) => (prev === null || prev <= 1 ? null : prev - 1));
    }, 1000);
    return () => window.clearInterval(timer);
  }, [lockSeconds]);

  if (status === 'authenticated') return <Navigate to={returnTo} replace />;

  async function onSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (pending || lockSeconds !== null) return;
    setPending(true);
    setError(null);
    try {
      await signIn(email, password);
      navigate(returnTo, { replace: true });
    } catch (cause) {
      const apiError =
        cause instanceof ApiError
          ? cause
          : new ApiError({ status: 0, code: 'UNKNOWN', message: '登录失败', cause });
      setError(apiError);
      setLockSeconds(apiError.retryAfterSeconds ?? null);
      setPending(false);
    }
  }

  const copy = error ? loginErrorCopy(error, lockSeconds) : null;
  const disabled = pending || lockSeconds !== null;

  return (
    <div className="space-y-4">
      <AuthCard
        title="登录"
        description="用注册时的邮箱登录。"
        footer={
          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
            <Link to="/auth/forgot" className="text-accent hover:underline">
              忘记密码
            </Link>
            <Link to="/auth/register" className="text-accent hover:underline">
              用邀请码注册
            </Link>
          </div>
        }
      >
        <form className="space-y-4" onSubmit={(event) => void onSubmit(event)}>
          <Field
            label="邮箱"
            name="email"
            type="email"
            autoComplete="username"
            inputMode="email"
            placeholder="you@example.com"
            required
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
          <Field
            label="密码"
            name="password"
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />

          {copy ? (
            <FormError>
              <span className="font-medium">{copy.title}</span>
              <br />
              {copy.description}
              {error?.requestId ? (
                <>
                  <br />
                  <span className="font-mono text-xs">请求号 {error.requestId}</span>
                </>
              ) : null}
            </FormError>
          ) : null}

          <Button tone="primary" className="w-full" type="submit" disabled={disabled}>
            {pending ? '正在登录…' : lockSeconds !== null ? `${lockSeconds} 秒后可再试` : '登录'}
          </Button>
        </form>

        <AuthTodo>
          这一页<strong className="font-medium">没有人机验证组件，且这是设计结论不是遗漏</strong> ——
          Turnstile 大陆不可用、reCAPTCHA 依赖 google.com、hCaptcha 无实测数据（ADR 0003 §3.2、
          page-inventory §5.3）。P1 靠「IP + 账号」双维度速率限制 + 指数退避，
          而那套限流<strong className="font-medium">后端还没做</strong>（handler/auth.go 的 TODO(P1)：
          精确档限流要一张 rate_limit 表，当前 migration 里没有）。
        </AuthTodo>

        {/* 「记住我」暂缺，不是忘了：后端只发一枚 30 天的不透明会话 token
            （handler/auth.go 的 sessionTokens），没有「短会话 / 长会话」两档可选。
            做一个只改前端存储位置（session→local）的开关，等于把 30 天的凭据写进
            localStorage 而用户以为自己选的是「更方便」。TODO(P2)：后端支持会话时长后再上。 */}
      </AuthCard>

      {/* 连不上时额外给一次备用域名：登录页打不开的人是最需要它的人，
          而他们此刻还没登录，面板里其他任何地方都到不了。 */}
      {error?.kind === 'offline' ? <ErrorState kind="offline" /> : null}

      <MirrorDomainList />
    </div>
  );
}

/**
 * `ErrorCode` → 文案。**唯一按 code 分支的地方**，页面里不许再写第二处。
 * （README §8 的最后一条未决项就是「前端没有按 code 分支的文案表」，这是第一块。）
 */
function loginErrorCopy(
  error: ApiError,
  lockSeconds: number | null,
): { title: string; description: string } {
  switch (error.code) {
    case 'AUTH_INVALID_CREDENTIALS':
      return {
        title: '邮箱或密码不正确',
        description: '两者中有一个不对。为了防止账号被逐个试探，我们不说是哪一个。',
      };
    case 'AUTH_PERMISSION_DENIED':
      return {
        title: '这个账号已被封禁',
        description: '密码是对的，但账号不可用。重新登录不会有帮助，请通过邮件联系我们。',
      };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '尝试次数过多',
        description:
          lockSeconds === null
            ? '这个邮箱或这个网络地址短时间内登录了太多次，稍后再试。'
            : `这个邮箱或这个网络地址短时间内登录了太多次，${lockSeconds} 秒后可以再试。`,
      };
    case 'VALIDATION_FAILED':
      return { title: '填写有误', description: error.message };
    default:
      break;
  }
  // 没有 code 可依（网络层失败 / 非信封响应）时按五类归一走。
  switch (error.kind) {
    case 'offline':
      return { title: '连不上面板', description: '当前网络到面板的连接失败。可以试试下面的备用域名。' };
    case 'server':
      return { title: '我们这边出了问题', description: '不是你的账号或网络的问题，稍后再试一次。' };
    default:
      return { title: '登录没能完成', description: error.message };
  }
}
