import { Card, Icon, LinkButton } from './_imports.ts';

export default function NotFoundPage() {
  return (
    <Card>
      <h1 className="text-lg font-semibold text-fg">这个地址不存在</h1>
      <p className="mt-1.5 text-sm leading-relaxed text-fg-muted">
        后台的路由都在左侧导航里。如果你是从书签进来的，那个路径可能已经改了。
      </p>
      <LinkButton className="mt-4" tone="primary" href="/admin">
        回到看板 <Icon.ArrowRight size={14} />
      </LinkButton>
    </Card>
  );
}
