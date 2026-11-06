/**
 * 认证四页共用的表单外壳与字段。**没有验证码组件，这是有意的**：
 *
 * - Cloudflare Turnstile 在中国大陆不可用（ADR 0003 §3.2，官方 China Network 可用产品清单里没有它）
 * - Google reCAPTCHA 依赖 `google.com`，大陆完全封锁
 * - hCaptcha 的大陆可达性**无任何 OONI 数据**，不可假设
 *
 * P1 的决定是**不上任何 captcha**（page-inventory §5.3、user-journey §3.2）：
 * 注册被邀请码封死，登录与找回密码用「IP + 账号」双维度速率限制 + 指数退避。
 * 引入一个可达性未知的第三方主机名，风险大于收益。
 * 若观察到刷量再补自托管 PoW —— 那也是自托管的，不是第三方的。
 */
import type { InputHTMLAttributes, ReactNode } from 'react';
import { Card, cx } from '@babelplus/shared/ui';

export function AuthCard({
  title,
  description,
  children,
  footer,
}: {
  title: string;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <Card>
      <h1 className="text-lg font-semibold text-fg">{title}</h1>
      {description ? <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">{description}</p> : null}
      <div className="mt-5 space-y-4">{children}</div>
      {footer ? <div className="mt-5 border-t border-line pt-4 text-sm text-fg-muted">{footer}</div> : null}
    </Card>
  );
}

export function Field({
  label,
  hint,
  error,
  className,
  ...rest
}: InputHTMLAttributes<HTMLInputElement> & {
  label: string;
  hint?: ReactNode;
  /**
   * 字段级错误。有值时接管 `hint` 的位置 —— 两条同时显示会让用户先读到无关的提示。
   *
   * 单独做成 prop 而不是让页面自己在下面塞一个 `<p>`：`aria-invalid` 与
   * `aria-describedby` 必须和这段文字一起出现，分开写迟早会漏，
   * 而漏掉的表现是屏幕阅读器用户完全听不到错在哪里。
   */
  error?: ReactNode;
}) {
  const id = `f-${rest.name ?? label}`;
  const noteId = `${id}-note`;
  const note = error ?? hint;
  return (
    <div>
      <label htmlFor={id} className="mb-1.5 block text-sm font-medium text-fg">
        {label}
      </label>
      <input
        id={id}
        aria-invalid={error ? true : undefined}
        aria-describedby={note ? noteId : undefined}
        // 16px 起步：iOS Safari 在 <16px 的输入框获得焦点时会自动放大页面，
        // 放大后 375px 布局就出现横向滚动，直接违反 M1。
        className={cx(
          'min-h-11 w-full rounded-lg border bg-surface px-3 text-base text-fg',
          error ? 'border-danger' : 'border-line',
          'placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-2',
          'focus-visible:outline-offset-1 focus-visible:outline-accent',
          className,
        )}
        {...rest}
      />
      {note ? (
        <p
          id={noteId}
          className={cx(
            'mt-1.5 text-xs leading-relaxed',
            error ? 'font-medium text-danger' : 'text-fg-muted',
          )}
        >
          {note}
        </p>
      ) : null}
    </div>
  );
}

/**
 * 表单级**非错误**提示位。
 *
 * 存在的理由只有一条，但它很具体：注册时「这个邮箱已经注册过了」**不是错误**，
 * 是「你走错门了，登录入口在这边」。用红色的 `FormError` 显示它，
 * 用户会以为自己填错了什么，然后改邮箱重试 —— 而正确的动作是去登录。
 */
export function FormNotice({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-lg border border-accent/30 bg-accent/10 px-3 py-2 text-sm leading-relaxed text-accent">
      {children}
    </p>
  );
}

/** 表单级错误位。分类文案由调用方给 —— §3.2.1 对每一页的错态都有具体要求。 */
export function FormError({ children }: { children: ReactNode }) {
  return (
    <p role="alert" className="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-sm text-danger">
      {children}
    </p>
  );
}

/** 「这一页还没接线」的行内声明，认证页用（比 NotWiredNotice 更紧凑）。 */
export function AuthTodo({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-lg border border-dashed border-line bg-surface-alt/60 px-3 py-2 text-xs leading-relaxed text-fg-muted">
      {children}
    </p>
  );
}
