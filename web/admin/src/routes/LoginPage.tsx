/**
 * `/admin/login` —— 闸 3（强制 TOTP）的落地页。page-inventory §4.1。
 *
 * 三道闸，缺一不可：
 *   闸 1 独立主域名（不做子域、不做路径混淆）
 *   闸 2 IP 白名单 / GCP IAP —— **在这个页面加载之前就已经拦过一次了**
 *   闸 3 强制 TOTP —— 就是这一页，**不接受关闭**
 *
 * IAP 与 TOTP 是两套独立凭据，任一泄漏不足以进入。所以这一页上
 * **不能有「跳过两步验证」「记住这台设备 30 天」之类的分支** —— 那等于把闸 3 拆了。
 */
import type { ReactNode } from 'react';
import { Button, Card } from './_imports.ts';

export default function LoginPage() {
  return (
    <div className="flex min-h-dvh items-start justify-center bg-bg px-4 pt-16">
      <div className="w-full max-w-sm space-y-4">
        <p className="text-center text-base font-semibold tracking-tight text-fg">babel.plus 后台</p>

        <Card>
          <h1 className="text-lg font-semibold text-fg">管理员登录</h1>
          <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
            你已经通过了网络层的准入。这里还需要密码和验证器上的 6 位码。
          </p>

          <div className="mt-5 space-y-4">
            <Labeled label="邮箱">
              <input type="email" name="email" autoComplete="username" className={INPUT} />
            </Labeled>
            <Labeled label="密码">
              <input type="password" name="password" autoComplete="current-password" className={INPUT} />
            </Labeled>
            <Labeled label="验证器 6 位码">
              <input
                type="text"
                name="totp"
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={6}
                className={`${INPUT} font-mono tracking-[0.4em]`}
              />
            </Labeled>

            {/* TODO(P1): 提交。TOTP 是**必填**，不是「如果开启了才要」。
                后端也必须拒绝没有 TOTP 的管理员账号，前端的必填只是第一层。 */}
            <Button tone="primary" className="w-full" disabled>
              登录
            </Button>
          </div>
        </Card>

        {/* 🔴 这段警告是给运维自己看的，必须留在页面上。 */}
        <div className="rounded-xl border border-danger/30 bg-danger/10 p-3 text-xs leading-relaxed text-danger">
          <p className="font-medium">IAP 有一个自我引用的失效模式。</p>
          <p className="mt-1">
            IAP 要求 Google 身份，而 google.com 在中国大陆自 2014 年起被完全封锁。
            <strong className="font-semibold"> 服务出故障时，身处大陆的运维自己也进不了后台。</strong>
            必须准备一条不依赖本服务的备用出网路径，并定期演练。这不是可选项。
          </p>
        </div>

        <p className="text-center text-xs text-fg-subtle">
          尚未接线。契约：<code className="font-mono">login</code>（管理面复用同一端点，
          由 <code className="font-mono">adminSession</code> + <code className="font-mono">adminIap</code> 两个 scheme 保护）。
        </p>
      </div>
    </div>
  );
}

const INPUT =
  'min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg ' +
  'focus-visible:border-accent focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent';

function Labeled({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1.5 block text-sm font-medium text-fg">{label}</span>
      {children}
    </label>
  );
}
