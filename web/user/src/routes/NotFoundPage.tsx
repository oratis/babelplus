/**
 * 404。
 *
 * 一个**不能想当然**的细节：SPA 的 404 页在静态托管上只有在配置了
 * 「一切未匹配路径回 index.html」时才会被渲染。没配的话用户看到的是托管平台的默认 404，
 * 而那个页面上没有备用域名列表 —— 恰好在用户已经迷路的时候。
 * 部署清单里这一条要单独勾。
 */
import { Card, Icon, LinkButton, MirrorDomainList } from './_imports.ts';

export default function NotFoundPage() {
  return (
    <div className="space-y-4">
      <Card>
        <h1 className="text-lg font-semibold text-fg">这个地址不存在</h1>
        <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
          链接可能过期了，或者是从旧版面板复制过来的。你的订阅和已连接的设备都不受影响。
        </p>
        <div className="mt-4 flex flex-wrap gap-2">
          <LinkButton tone="primary" href="/dashboard">
            回到概览 <Icon.ArrowRight size={14} />
          </LinkButton>
          <LinkButton href="/subscribe">去复制订阅链接</LinkButton>
        </div>
      </Card>

      <MirrorDomainList />
    </div>
  );
}
