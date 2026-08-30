/**
 * `/profile/2fa` —— P3。page-inventory §3.1 #20、§3.2.9。
 * 管理员 TOTP 是 P1 强制（§4.1 三道闸之一），用户侧是**可选项**。
 *
 * 🔴 **这一页的三个端点仍然是刻意的 501。**
 * `enrollUserTotp` / `verifyUserTotp` / `disableUserTotp` 在后端是
 * `unimplemented.gen.go` 里的桩（openapi 对 `enrollUserTotp` 的描述逐字：
 * 「**P3，未实现。** 服务端返回 `501` 直到实现完成」），而 `db/queries/account.sql`
 * 的 TOTP 一节明写「本节**没有任何查询**……这是把 0001–0017 的 up.sql 全部读完、
 * 再用 psql \d 逐列核对之后的结论」—— **数据库里根本没有落点**。
 *
 * 所以这一页的产品要求是「**显示尚未开放**」，而不是「做一个看起来能用的绑定界面」。
 * 具体落成三条：
 *
 *  1. **首屏就是「尚未开放」**，不是先给一个「开始设置」按钮再让用户撞 501。
 *     让人走三步才发现功能不存在，比第一眼就告诉他更糟。
 *  2. **不在挂载时自动探测。** `enrollUserTotp` 是 `POST` —— 后端真做出来之后，
 *     每打开一次这一页就会重新生成一次 secret，把用户已经绑好的验证器作废。
 *     探测必须是用户主动点的，且按钮文案说清楚它是「检查」不是「开始设置」。
 *  3. **探测意外成功时不显示 secret。** 二维码要求**本地生成**（绝不调用在线二维码服务：
 *     那等于把 TOTP 密钥发给第三方，而且是一个大陆可达性未知的第三方主机名，ADR 0003 §5），
 *     而仓库里还没有本地二维码渲染件 —— 也就是说**绑定流程前端也没做完**。
 *     此时把 secret 印在屏幕上，用户既完成不了绑定，密钥还白白暴露了一次。
 *
 * TODO(P3)：后端三条 501 落地 + 本地二维码渲染件（不引在线服务）到位之后，
 * 这一页才换成真正的绑定流程：二维码 + 可复制的明文 secret + 6 位码验证 + 10 个一次性恢复码。
 * **恢复码是其中不能省的一环** —— 没有恢复码的 2FA 会把丢手机的用户永久锁在门外，
 * 而找回流程要走人工，成本高得多。
 */
import { useState } from 'react';
import { Badge, Button, Card, CardTitle, Icon, LinkButton } from './_imports.ts';
import { unwrap, type ApiError } from '@babelplus/shared/api';
import { useAuth } from '../lib/auth.tsx';
import { api } from '../lib/api.ts';
import { asApiError } from './ticket-common.tsx';
import {
  PendingFeatureNotice,
  WriteError,
  commonWriteErrorCopy,
  fallbackWriteErrorCopy,
  isNotImplemented,
} from './account-common.tsx';

type ProbeState =
  | { status: 'idle' }
  | { status: 'pending' }
  /** 501：确认了「还没开放」。这是**当前唯一会真实发生**的结果。 */
  | { status: 'not-implemented'; error: ApiError }
  /** 后端开了、但前端流程没做完。见文件头第 3 条。 */
  | { status: 'backend-ahead' }
  | { status: 'error'; error: ApiError };

export default function ProfileTwoFactorPage() {
  const { user } = useAuth();
  const [probe, setProbe] = useState<ProbeState>({ status: 'idle' });

  async function checkAvailability(): Promise<void> {
    if (probe.status === 'pending') return;
    setProbe({ status: 'pending' });
    try {
      // 响应体（TotpEnrollment：`secret` + `otpauth_url`）**故意不接住**。
      // 拿到它也没有用得上它的界面，而把它存进 state 就等于把密钥留在内存里等着被渲染出来。
      await unwrap(api().POST('/api/v1/user/2fa/enroll'));
      setProbe({ status: 'backend-ahead' });
    } catch (cause) {
      const error = asApiError(cause, '检查失败');
      setProbe(
        isNotImplemented(error)
          ? { status: 'not-implemented', error }
          : { status: 'error', error },
      );
    }
  }

  // `totp_enabled` 是 CurrentUser 上的**可选**字段：缺失时不等于「未开启」，
  // 只是这一版后端没返。所以缺失时什么都不说，不猜。
  const enabled = user?.totp_enabled;

  return (
    <>
      <header className="mb-5 sm:mb-6">
        <div className="flex flex-wrap items-center gap-2">
          <h1 className="text-xl font-semibold tracking-tight text-fg sm:text-2xl">两步验证</h1>
          {enabled === true ? <Badge tone="ok">已开启</Badge> : null}
        </div>
        <p className="mt-1 max-w-2xl text-sm text-fg-muted">
          用 TOTP 应用生成的 6 位码作为第二道验证。
        </p>
      </header>

      <div className="space-y-4">
        {/* 首屏即结论。用虚线中性卡片而不是红色错误框：「还没做」不是故障，
            用户也无事可做，红色警告在这里是误报（理由同 ticket-common 的 NotImplementedNotice）。 */}
        <PendingFeatureNotice what="用户侧两步验证">
          <p>
            绑定、验证、解绑三个接口都还没上线，数据库里也还没有存放密钥的位置。
            在它开放之前，保护账号最有效的做法是：用一个<strong className="font-medium text-fg">只给这个站用</strong>的长密码，
            并留意到期与异常登录的邮件通知。
          </p>
          <p className="mt-2">
            管理员账号的两步验证是<strong className="font-medium text-fg">另一套</strong>、且是强制开启的 —— 这一页只管用户侧。
          </p>
        </PendingFeatureNotice>

        {enabled === true ? (
          <Card>
            <CardTitle hint="来自 /api/v1/user/me">当前状态</CardTitle>
            <p className="text-sm leading-relaxed text-fg-muted">
              你的账号上标着<strong className="font-medium text-fg">已开启</strong>两步验证，但用户侧的解绑接口同样还没上线。
              需要关掉它请提工单（分类选「账号本身」）。
            </p>
          </Card>
        ) : null}

        <Card>
          <CardTitle hint="enrollUserTotp">再确认一次</CardTitle>
          <p className="text-sm leading-relaxed text-fg-muted">
            上面那句话是照契约写的。如果你想确认后端此刻是不是已经开放了，可以点一下 ——
            这会向绑定接口发一次请求，然后把它的
            <strong className="font-medium text-fg">真实回答</strong>显示在下面。
          </p>
          <div className="mt-3 flex flex-wrap items-center gap-3">
            <Button onClick={() => void checkAvailability()} disabled={probe.status === 'pending'}>
              {probe.status === 'pending' ? '正在检查…' : '检查是否已开放'}
            </Button>
            {probe.status === 'not-implemented' ? (
              <span className="text-sm text-fg-muted">
                服务端回答：尚未开放
                {probe.error.requestId ? (
                  <span className="ml-2 font-mono text-xs text-fg-subtle">
                    请求号 {probe.error.requestId}
                  </span>
                ) : null}
              </span>
            ) : null}
          </div>

          {probe.status === 'backend-ahead' ? (
            <div className="mt-3 rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm leading-relaxed text-warn">
              <p className="font-medium">后端已经开放了，但这一页的绑定流程还没做完。</p>
              <p className="mt-1">
                差的是<strong className="font-medium">本地二维码渲染</strong> —— 二维码必须在你的浏览器里生成，
                绝不能调用在线二维码服务（那等于把密钥发给第三方）。
                在那之前请先别在这里绑定；需要现在就开启的话，提工单找我们。
              </p>
            </div>
          ) : null}

          {probe.status === 'error' ? <ProbeError error={probe.error} /> : null}
        </Card>

        <Card>
          <CardTitle hint="做的时候不能省">恢复码</CardTitle>
          <p className="text-sm leading-relaxed text-fg-muted">
            绑定流程做出来的时候会附带 10 个一次性恢复码，并要求你确认「我已保存」。
            没有恢复码的两步验证会把丢手机的用户永久锁在门外，而找回要走人工，成本高得多。
          </p>
          <LinkButton className="mt-3" href="/profile">
            回到账号设置 <Icon.ArrowRight size={14} />
          </LinkButton>
        </Card>
      </div>
    </>
  );
}

/**
 * 探测请求的非 501 失败。**按 `ErrorCode` 分支**，不按状态码 ——
 * 501 已经在调用处被 `isNotImplemented` 摘走了，这里剩下的是网络、限流、封禁与 5xx。
 */
function ProbeError({ error }: { error: ApiError }) {
  const copy = commonWriteErrorCopy(error) ?? fallbackWriteErrorCopy(error, '没能问到服务端');
  return (
    <div className="mt-3">
      <WriteError title={copy.title} description={copy.description} />
    </div>
  );
}
