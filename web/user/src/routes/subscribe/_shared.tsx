/**
 * `/subscribe`、`/subscribe/tokens`、`/node` 三页接线时共用的小零件。
 *
 * 为什么放在这个目录而不是 `web/shared/src/ui`：多个页面正在**并行**接线，
 * 共享 UI 目录是所有人都要改的文件，动它必然撞车。这里的东西只服务于这三页；
 * 等全部接线完成、形态稳定了，再决定要不要上提到 shared。
 *
 * 这里刻意**不**放各页的文案表 —— 按 `ErrorCode` 分支的文案是**产品内容**，
 * 每页说法不同，合成一张表就会出现「为了复用而把话说得更含糊」。
 * 这里只放三页真正一模一样的那部分：兜底文案（`fallbackErrorCopy`）。
 */
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { ApiError } from '@babelplus/shared/api';
import { Button, Card } from '@babelplus/shared/ui';

/* ───────────────────────────── 请求三态 ───────────────────────────── */

/**
 * 一次请求的三态。
 *
 * 🔴 为什么每个请求各持一份而不是整页一个 loading：page-inventory §2.2 的硬规则。
 * 一页上「订阅链接」「在线设备」「拉取记录」是三个独立的端点，
 * 合并成一个整页 loading 的后果是**任意一个 5xx 都会把订阅链接一起藏掉** ——
 * 而用户来这一页十有八九就是为了复制那条链接。
 */
export type Resource<T> =
  | { readonly status: 'loading'; readonly data: null; readonly error: null }
  | { readonly status: 'ready'; readonly data: T; readonly error: null }
  | { readonly status: 'error'; readonly data: null; readonly error: ApiError };

export interface ResourceHandle<T> {
  readonly state: Resource<T>;
  /** 重新拉一次（写操作成功后调用）。 */
  reload: () => void;
}

/**
 * 最小的「拉一次数据」hook。
 *
 * 刻意不引 react-query：`web/shared/api/client.ts` 的文件头写明缓存与状态管理的选型
 * **还没裁决**（page-inventory §8），现在装一个等于替以后的人做决定。
 * 这里只做三件必须做的事：三态、卸载后不 setState、可重拉。
 */
export function useResource<T>(load: () => Promise<T>): ResourceHandle<T> {
  const [state, setState] = useState<Resource<T>>({ status: 'loading', data: null, error: null });
  const [generation, setGeneration] = useState(0);

  // load 每次渲染都是新函数。放进 deps 会无限循环，用 ref 取最新的那一份。
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    let cancelled = false;
    setState({ status: 'loading', data: null, error: null });
    void (async () => {
      try {
        const data = await loadRef.current();
        if (!cancelled) setState({ status: 'ready', data, error: null });
      } catch (cause) {
        if (!cancelled) setState({ status: 'error', data: null, error: toApiErrorLike(cause) });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [generation]);

  const reload = useCallback(() => setGeneration((g) => g + 1), []);
  return { state, reload };
}

/** 任何异常 → `ApiError`。非 `ApiError`（渲染期 bug、JSON 解析失败）也要有 `kind` 才能走统一文案。 */
export function toApiErrorLike(cause: unknown, message = '请求失败'): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({ status: 0, code: 'UNKNOWN', message, cause });
}

/* ───────────────────────────── 错误文案兜底 ───────────────────────────── */

export interface ErrorCopy {
  readonly title: string;
  readonly description: string;
}

/**
 * 三页共用的兜底文案。**页面自己的表先分支，剩下的才落到这里。**
 *
 * 🔴 `NOT_IMPLEMENTED` 必须在这里被认出来。后端当前有 120+ 个端点返 501
 * （`api/cmd/server/main.go` 的 `responseErrorHandler`：501 表示「还没写」，
 * 500 表示「写了但炸了」）。501 的 `kind` 是 `server`，若不单独分支，
 * 用户会看到「我们这边出了问题，稍后再试一次」—— 一句会让人反复刷新的假话。
 */
export function fallbackErrorCopy(error: ApiError): ErrorCopy {
  switch (error.code) {
    case 'NOT_IMPLEMENTED':
      return {
        title: '该功能尚未开放',
        description: '这一块还在开发中，不是你的账号或网络的问题。上线前不用反复刷新。',
      };
    case 'AUTH_TOKEN_EXPIRED':
    case 'AUTH_TOKEN_INVALID':
      return { title: '登录状态已失效', description: '请重新登录后再试。' };
    case 'AUTH_PERMISSION_DENIED':
      return { title: '这个账号已被封禁', description: '重新登录不会有帮助，请通过邮件联系我们。' };
    case 'QUOTA_RATE_LIMITED':
      return {
        title: '操作太频繁',
        description:
          error.retryAfterSeconds === undefined
            ? '短时间内请求了太多次，稍后再试。'
            : `短时间内请求了太多次，${error.retryAfterSeconds} 秒后可以再试。`,
      };
    case 'VALIDATION_FAILED':
    case 'VALIDATION_MALFORMED_BODY':
      return { title: '填写有误', description: error.message };
    default:
      break;
  }
  switch (error.kind) {
    case 'offline':
      return { title: '连不上面板', description: '当前网络到面板的连接失败。可以试试页脚的备用域名。' };
    case 'server':
      return { title: '我们这边出了问题', description: '不是你的账号或网络的问题，稍后再试一次。' };
    case 'unauthorized':
      return { title: '登录状态已失效', description: '请重新登录后再试。' };
    default:
      return { title: '这一步没能完成', description: error.message };
  }
}

/* ───────────────────────────── 危险操作的二次确认 ───────────────────────────── */

export interface DangerConfirmProps {
  open: boolean;
  title: string;
  /** 后果清单。**必填** —— 见组件注释：没有后果的确认框只是多点一下。 */
  consequences: readonly ReactNode[];
  confirmLabel: string;
  /** 要求勾选「我明白后果」才能确认。最高危的那两个操作用它。 */
  requireAck?: boolean;
  pending?: boolean;
  /** 确认后失败的话，错误留在框里显示，**不关框** —— 关掉了用户就不知道自己那一下有没有生效。 */
  error?: ErrorCopy | null;
  onCancel: () => void;
  onConfirm: () => void;
}

/**
 * 危险操作的二次确认。
 *
 * `consequences` 是**必填数组**，不是可选说明：一个只写「确定吗？」的确认框
 * 只是把误触从一次变成两次，并不让用户知道自己在做什么。
 * 后台侧同类操作是 D3（🔒 输入用户邮箱 + 审计 + 邮件通知，api-contract §6.2），
 * 用户对自己账号操作可以轻一些 —— 轻的是**验证强度**，不是**信息量**。
 *
 * 不用 `<dialog>`：jsdom 对它的支持不完整，而这块逻辑必须能测。
 */
export function DangerConfirm({
  open,
  title,
  consequences,
  confirmLabel,
  requireAck = false,
  pending = false,
  error = null,
  onCancel,
  onConfirm,
}: DangerConfirmProps) {
  const [acked, setAcked] = useState(false);

  // 每次重新打开都要求重新勾选。留着上次的勾会让「关掉再点开」变成一次静默的确认。
  useEffect(() => {
    if (!open) setAcked(false);
  }, [open]);

  if (!open) return null;

  const blocked = pending || (requireAck && !acked);

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={title}
      className="mt-3 rounded-xl border-2 border-danger/50 bg-danger/5 p-4"
    >
      <h3 className="text-base font-semibold text-fg">{title}</h3>
      <ul className="mt-2 space-y-1 text-sm leading-relaxed text-fg-muted">
        {consequences.map((line, i) => (
          <li key={i} className="flex gap-2">
            <span aria-hidden="true" className="text-danger">
              ·
            </span>
            <span>{line}</span>
          </li>
        ))}
      </ul>

      {requireAck ? (
        <label className="mt-3 flex items-start gap-2 text-sm text-fg">
          <input
            type="checkbox"
            className="mt-1"
            checked={acked}
            onChange={(event) => setAcked(event.target.checked)}
          />
          <span>我明白上面的后果</span>
        </label>
      ) : null}

      {error ? (
        <p role="alert" className="mt-3 rounded-lg bg-danger/10 px-3 py-2 text-sm text-danger">
          <span className="font-medium">{error.title}</span>
          <br />
          {error.description}
        </p>
      ) : null}

      <div className="mt-4 flex flex-wrap gap-2">
        <Button tone="danger" disabled={blocked} onClick={onConfirm}>
          {pending ? '正在执行…' : confirmLabel}
        </Button>
        <Button onClick={onCancel} disabled={pending}>
          取消
        </Button>
      </div>
    </div>
  );
}

/* ───────────────────────────── 复制到剪贴板 ───────────────────────────── */

export type CopyState = 'idle' | 'ok' | 'failed';

/**
 * 复制。
 *
 * `navigator.clipboard` 在**非安全上下文**（http 的镜像域名、部分内嵌浏览器）下不存在 ——
 * 而备用域名恰恰可能是这种环境。所以失败必须是一个**可见的状态**，
 * 让页面能把明文展开让用户手动选中，而不是静默地什么都没发生。
 */
export function useClipboard(): { state: CopyState; copy: (text: string) => void } {
  const [state, setState] = useState<CopyState>('idle');

  useEffect(() => {
    if (state === 'idle') return;
    const timer = window.setTimeout(() => setState('idle'), 3000);
    return () => window.clearTimeout(timer);
  }, [state]);

  const copy = useCallback((text: string) => {
    const clipboard = navigator.clipboard as Clipboard | undefined;
    if (!clipboard || typeof clipboard.writeText !== 'function') {
      setState('failed');
      return;
    }
    void clipboard.writeText(text).then(
      () => setState('ok'),
      () => setState('failed'),
    );
  }, []);

  return { state, copy };
}

/* ───────────────────────────── 列表骨架 ───────────────────────────── */

/** 列表加载骨架。**不用 spinner** —— §2.2：跨境往返常在数百毫秒到数秒，spinner 会被读成「卡死」。 */
export function ListSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div aria-busy="true" aria-live="polite" className="space-y-2">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="h-12 animate-pulse rounded-lg bg-skeleton" aria-hidden="true" />
      ))}
    </div>
  );
}

/** 卡片内的小号错误块。整页级的 `ErrorState` 太重，一个区块用不上它的备用域名清单。 */
export function InlineError({
  copy,
  requestId,
  onRetry,
}: {
  copy: ErrorCopy;
  requestId?: string | undefined;
  onRetry?: (() => void) | undefined;
}) {
  return (
    <Card className="border-l-4 border-l-warn">
      <p role="alert" className="text-sm">
        <span className="font-medium text-fg">{copy.title}</span>
        <br />
        <span className="text-fg-muted">{copy.description}</span>
      </p>
      {requestId ? (
        <p className="mt-2 font-mono text-xs text-fg-subtle">
          请求号 {requestId} —— 提工单时贴上它，我们能直接定位。
        </p>
      ) : null}
      {onRetry ? (
        <Button className="mt-3" onClick={onRetry}>
          重试
        </Button>
      ) : null}
    </Card>
  );
}
