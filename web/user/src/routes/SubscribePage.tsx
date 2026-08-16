/**
 * `/subscribe` —— P1，**无替代**，本项目最重的一页。page-inventory §3.1 #6、§3.2.3。
 *
 * 竞品把订阅链接埋在 dashboard 的按钮里、把重置埋在 profile 里、完全没有设备列表。
 * 我们把这三件事合成一页，因为它们是同一个心智单元：「谁在用我的订阅」。
 *
 * 三条必须做的事（§3.2.3）：
 *  1. **计数口径写在页面上。** 契约 `listUserDevices` 的描述原文：
 *     「口径是**按 IP** 不是按设备」。同一台手机在 Wi-Fi 与蜂窝间切换会占两个名额，
 *     在设备数 = 2 的档位上，一个人一台手机一台电脑就已经超限。
 *     **这不是 bug，是口径，必须直说。**
 *  2. **必须有用户自助「踢下线」**，不能只等 TTL 过期。
 *  3. **达到上限时不显示红色报错**，显示升档引导 —— 这是转化位不是错误提示。
 */
import { Link } from 'react-router';
import {
  Badge,
  Button,
  Card,
  CardTitle,
  EmptyState,
  ErrorState,
  Icon,
  LinkButton,
  maskSecret,
  runtimeConfig,
} from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function SubscribePage() {
  const cfg = runtimeConfig();

  return (
    <RouteScaffold
      title="订阅与设备"
      description="复制订阅链接、看谁在用、把不认识的踢掉。"
      priority="P1"
      endpoints={[
        'getUserSubscription',
        'listUserDevices',
        'kickUserDevice',
        'kickAllUserDevices',
        'revokeAllSubscriptionTokens',
        'listSubscriptionFetchLog',
      ]}
      todo={
        <>
          最重要的一条：<strong className="font-medium text-fg">订阅接口 5xx 时，链接必须从本地缓存回显</strong>
          并标注「以下为上次读取的链接，可能已过期」。用户来这一页十有八九就是为了复制链接，
          读不到就是最糟的体验（§3.2.3 错态）。
        </>
      }
      empty={
        <EmptyState
          title="还没有设备连接过"
          description="订阅链接已经生成好了，但还没有任何客户端用它拉取过配置。照教程配一次，三分钟就能连上。"
          action={
            cfg.docsUrl ? (
              <LinkButton tone="primary" href={cfg.docsUrl} external>
                3 分钟接入 <Icon.External size={14} />
              </LinkButton>
            ) : (
              <Button tone="primary" disabled title="docsUrl 未配置">
                3 分钟接入
              </Button>
            )
          }
          secondary="这是新用户最重要的一步 —— 跨不过去，前面所有环节都白做。"
        />
      }
      error={
        <ErrorState
          kind="server"
          title="读不到最新的订阅信息"
          description="下面显示的是这台设备上次读到的链接，可能已经过期。如果导入后连不上，稍后再刷新一次。"
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P1): getUserSubscription">订阅链接</CardTitle>
          <div className="rounded-lg border border-line bg-surface-alt p-3">
            {/* 默认打码：订阅链接等同于凭据，而用户经常在办公室/咖啡馆截图求助。 */}
            <code className="block break-all font-mono text-sm text-fg-muted">
              {maskSecret('https://example.invalid/s/TOKEN_PLACEHOLDER_NOT_REAL')}
            </code>
            <div className="mt-3 flex flex-wrap gap-2">
              <Button disabled>
                <Icon.Copy size={14} /> 复制
              </Button>
              <Button disabled>显示明文</Button>
              <Button disabled>二维码</Button>
            </div>
          </div>
          <div className="mt-3 flex flex-wrap gap-2">
            <Badge>Clash YAML</Badge>
            <Badge>sing-box JSON</Badge>
            <Badge>base64</Badge>
          </div>
          {/* TODO(P1): 三种格式切换 + 各客户端「一键导入」深链。
              导入深链是 scheme URL（clash://、sing-box://…），移动端点了直接进客户端。
              桌面端 scheme 不一定注册，要同时给「复制链接」兜底。 */}
        </Card>

        <Card>
          <CardTitle hint="按 UA 猜平台，高亮对应卡片">客户端引导</CardTitle>
          <LayoutSlot
            label="iOS / Android / Windows / macOS 四张卡片"
            hint="每张卡片链到教程站对应篇目。教程内容不在面板里重复放 —— 链接是廉价的（§3.3）。"
          />
        </Card>

        <Card>
          <CardTitle hint="TODO(P1): listUserDevices / kickUserDevice">在线设备</CardTitle>

          {/* 🔴 口径声明。这段文字是产品设计的一部分，不是补充说明，删掉它会直接变成工单。 */}
          <div className="mb-3 rounded-lg border border-warn/30 bg-warn/10 p-3 text-sm leading-relaxed text-warn">
            <p className="font-medium">这里数的是 IP，不是设备。</p>
            <p className="mt-1">
              同一台手机在 Wi-Fi 和流量之间切换，会各占一个名额，过一段时间自动释放。
              如果名额不够用，先在下面把不用的踢掉。
            </p>
          </div>

          <LayoutSlot
            label="列表：IP 归属地 · 最后活跃 · 协议 · 「踢下线」"
            hint="顶部 n / N 计数。>4 列在 <768px 必须卡片化，不允许横向滚动表格（§2.3 M1 硬规则）。"
          />

          {/* 达到上限时的样子：这是升档转化位，不是错误提示。所以用 info 不用 danger。 */}
          <div className="mt-3 rounded-lg border border-accent/30 bg-accent/10 p-3 text-sm text-accent">
            达到上限时显示这一条：「已达上限，需要更多设备可以升级套餐」+「踢掉一个」。
            <span className="ml-1 font-medium">不要用红色报错。</span>
          </div>
        </Card>

        <Card>
          <CardTitle hint="TODO(P1): revokeAllSubscriptionTokens / listSubscriptionFetchLog">安全</CardTitle>
          <div className="flex flex-wrap gap-2">
            {/* TODO(P1): 二次确认 —— 一键全撤会让**所有设备立即失效**。
                后台侧的同一操作是 D3（🔒 输入用户邮箱 + 审计 + 邮件通知），
                用户自己操作可以轻一些，但不能没有确认。 */}
            <Button tone="danger" disabled>
              重置订阅（所有设备需重新导入）
            </Button>
            <Link to="/subscribe/tokens" className="inline-flex items-center text-sm text-accent hover:underline">
              多 token 管理（P3）
            </Link>
          </div>
          <LayoutSlot
            className="mt-3"
            label="最近 10 次订阅拉取：时间 / IP 归属地 / UA"
            hint="这张表本来就要建（识别账号共享的唯一数据来源）。直接展示给用户，边际成本为零 —— 用户自己就能发现订阅被白嫖然后自助重置，不用开工单。"
          />
        </Card>
      </div>
    </RouteScaffold>
  );
}
