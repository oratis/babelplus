/**
 * `/dashboard` —— P1。page-inventory §3.1 #5、§3.2.2。
 *
 * 结构照抄竞品（公告轮播 + 订阅卡 + 四快捷入口，这个结构是对的），内容全换。
 *
 * 三条硬要求：
 *  1. 订阅卡与公告是**两个独立请求，任一失败不影响另一个** —— 所以它们各自持有自己的三态，
 *     而不是整页一个 loading。这一条在组件划分上就要体现出来，接线时不要合并成一个请求。
 *  2. 「重置日 = 订单日」必须明示（tutorials-spec §5：这是高频误解）。
 *  3. 订阅卡 5xx 时说「**你已连接的设备不受影响**」，不能只说「加载失败」。
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
  LoadingState,
  runtimeConfig,
  SkeletonCard,
} from './_imports.ts';
import { LayoutSlot, RouteScaffold } from '../components/RouteScaffold.tsx';

export default function DashboardPage() {
  const cfg = runtimeConfig();

  return (
    <RouteScaffold
      title="概览"
      description="订阅还剩多少、有没有新公告、下一步该点哪里。"
      priority="P1"
      endpoints={['getCurrentUser', 'getUserSubscription', 'listNotices']}
      todo={
        <>
          订阅卡与公告必须是<strong className="font-medium text-fg">两个独立请求</strong>，
          任一失败不影响另一个（§3.2.2）。整页一个 loading 是错的实现。
        </>
      }
      loading={
        <div className="space-y-4">
          <SkeletonCard lines={4} />
          <SkeletonCard lines={2} />
          <LoadingState slowHint />
        </div>
      }
      empty={
        // §3.2.2 原话：「这是最重要的空态」。不显示「暂无订阅」。
        <EmptyState
          title="选一个套餐开始"
          description="你的账号还没有可用的订阅。选好套餐、付款完成后，这里会出现订阅链接和流量进度。"
          action={
            <LinkButton tone="primary" href="/plan">
              看看套餐 <Icon.ArrowRight size={14} />
            </LinkButton>
          }
          secondary={
            <>
              不确定选哪个？先看
              {cfg.docsUrl ? (
                <a href={cfg.docsUrl} className="mx-1 text-accent hover:underline" rel="noreferrer noopener">
                  用量说明
                </a>
              ) : (
                <span className="mx-1">用量说明</span>
              )}
              ，流量比设备数更容易估错。
            </>
          }
        />
      }
      error={
        <ErrorState
          kind="server"
          title="暂时读不到订阅状态"
          description="这只是面板读不到数据。订阅本身没有变化。"
        />
      }
    >
      <div className="space-y-4">
        {/* TODO(P1): 首连未完成的账号，首屏应该是**接入向导**而不是这张仪表盘
            （user-journey §1 裁决 3：四个平级快捷入口对新用户是四个岔路）。
            判据是「订阅拉取审计表里有没有记录」，不是「有没有付款」。 */}

        <Card>
          <CardTitle hint="TODO(P1): getUserSubscription">当前订阅</CardTitle>
          <LayoutSlot
            label="套餐名 · 到期日 + 剩余天数 · 已用 X / 总 Y 进度条 · 设备 n/N"
            hint="必须明示「重置日 = 订单日」——tutorials-spec §5 记录这是高频误解。竞品这里没有设备数。"
          />
          <div className="mt-3 flex flex-wrap gap-2">
            <Badge tone="neutral">流量进度条</Badge>
            <Badge tone="neutral">重置日说明</Badge>
            <Badge tone="info">设备数 n/N（相对竞品新增）</Badge>
          </div>
        </Card>

        <Card>
          <CardTitle hint="TODO(P2): listNotices?limit=3">公告</CardTitle>
          <LayoutSlot
            label="最近 3 条 + 「查看全部」→ /notice"
            hint="公告兼作域名广播位（§3.2.9）。无公告时整块隐藏，不占位。"
          />
          <div className="mt-3">
            <Link to="/notice" className="text-sm text-accent hover:underline">
              查看全部公告
            </Link>
          </div>
        </Card>

        <Card>
          <CardTitle hint="结构照抄竞品，四个目标全改">快捷入口</CardTitle>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            {/* 教程指向**站外**独立域名：面板被封时教程还在（§3.3）。 */}
            {cfg.docsUrl ? (
              <LinkButton href={cfg.docsUrl} external className="flex-col gap-1 py-3 text-xs">
                <Icon.External size={16} />
                查看教程
              </LinkButton>
            ) : (
              <Button className="flex-col gap-1 py-3 text-xs" disabled title="docsUrl 未配置">
                <Icon.External size={16} />
                查看教程
              </Button>
            )}
            <LinkButton href="/subscribe" className="flex-col gap-1 py-3 text-xs">
              <Icon.Link size={16} />
              一键订阅
            </LinkButton>
            <LinkButton href="/plan" className="flex-col gap-1 py-3 text-xs">
              <Icon.Package size={16} />
              续费
            </LinkButton>
            {/* 「遇到问题」指向诊断页而不是工单 —— 先自助，再开单。 */}
            <LinkButton href="/diagnose" className="flex-col gap-1 py-3 text-xs">
              <Icon.Stethoscope size={16} />
              遇到问题
            </LinkButton>
          </div>
        </Card>

        {/* 失联提示条由 SiteFooter 常驻提供（ADR 0003 §5），这里不重复渲染。 */}
      </div>
    </RouteScaffold>
  );
}
