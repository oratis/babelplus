/**
 * 模块 15 · 邮件与送达 `/admin/mail` —— P2 / M3。
 *
 * 邮件不是「一个通知渠道」，是**唯一的失联恢复通道**（ADR 0002 §1）。
 * 所以这一页的送达统计不是运营指标，是**基础设施健康度**。
 *
 * 🔴 D11b 群发：退信率 ≥ 5% 进入服务商审查、≥ 10% 可能暂停发信。
 * 一次群发翻车 = 失联恢复通道被自己搞坏。所以强制先发测试件。
 */
import { Card, CardTitle, EmptyState, Button } from './_imports.ts';
import { LayoutSlot, ModuleScaffold } from '../components/ModuleScaffold.tsx';

export default function MailPage() {
  return (
    <ModuleScaffold
      title="邮件与送达"
      description="模板、发送日志、退信率。这一页看的是生命线的健康度。"
      priority="P2"
      mobile="M3"
      endpoints={['listAdminMailTemplates', 'updateAdminMailTemplate', 'listAdminMailLogs', 'broadcastAdminMail']}
      danger={['D11b']}
      todo={
        <>
          送达统计必须<strong className="font-medium text-fg">按收件域名分开看（QQ / 163 / 126 各一行）</strong>。
          总体送达率 95% 可能掩盖「QQ 邮箱 40%」这种情况，而我们的用户大量在用 QQ 邮箱。
        </>
      }
      empty={
        <EmptyState
          title="还没有发过邮件"
          description="注册验证码是第一个会用到邮件的流程。它同时也是送达率的免费持续采样。"
          action={
            <Button tone="primary" disabled>
              发一封测试件
            </Button>
          }
        />
      }
    >
      <div className="space-y-4">
        <Card>
          <CardTitle hint="TODO(P2): listAdminMailTemplates">模板</CardTitle>
          <LayoutSlot label="验证码 · 密码重置 · 到期提醒 · 流量提醒 · 工单回复 · 域名变更" />
        </Card>
        <Card>
          <CardTitle hint="基础设施健康度，不是运营指标">送达</CardTitle>
          <LayoutSlot
            label="按收件域名分组的送达 / 退信 / 投诉率"
            hint="注册完成率按收件域名分组的那张表也在这里 —— 它是 ADR 0002 §7 要求的送达率实测数据。"
          />
        </Card>
        <Card>
          <CardTitle hint="D11b">群发</CardTitle>
          <LayoutSlot
            label="收件范围 · 强制测试件 · 收件人数确认 · 频率上限"
            hint="发送按钮在测试件未发出前必须是禁用的。这不是提示，是禁用。"
          />
        </Card>
      </div>
    </ModuleScaffold>
  );
}
