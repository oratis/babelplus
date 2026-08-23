/**
 * 鉴权失败的常驻提示条。
 *
 * 为什么是**页面顶部的横幅**而不是某个页面里的错误态：平台层拒绝会让**所有**请求一起失败，
 * 挂在某一页上等于要求运维先猜对该看哪一页。横幅挂在 `<App>` 里，哪个路由下都在。
 *
 * 为什么不是模态对话框：它盖住页面时运维就看不到自己刚才在做什么了，
 * 而「刚才在做什么」是判断这次失败要不要紧的主要依据。
 */
import { useSyncExternalStore } from 'react';
import { Button, cx } from '@babelplus/shared/ui';
import {
  getAdminAuthFailure,
  reportAdminAuthFailure,
  subscribeAdminAuthFailure,
} from '../lib/iap.ts';

const TONE: Record<string, string> = {
  edge: 'border-warn/40 bg-warn/10 text-warn',
  'ambiguous-network': 'border-warn/40 bg-warn/10 text-warn',
  forbidden: 'border-danger/40 bg-danger/10 text-danger',
  app: 'border-danger/40 bg-danger/10 text-danger',
};

export function AuthFailureBanner() {
  const failure = useSyncExternalStore(subscribeAdminAuthFailure, getAdminAuthFailure, getAdminAuthFailure);
  if (!failure) return null;

  return (
    <div
      role="alert"
      className={cx(
        'sticky top-0 z-40 border-b px-3 py-2 text-xs leading-relaxed',
        TONE[failure.kind] ?? TONE['app'],
      )}
    >
      <div className="mx-auto flex max-w-5xl items-start gap-3">
        <div className="min-w-0 flex-1">
          <p className="font-semibold">{failure.title}</p>
          <p className="mt-0.5">{failure.description}</p>
          {/* 判定依据要露出来。判错的时候，这一行是唯一能追的线索。 */}
          <p className="mt-1 font-mono text-[11px] opacity-80">
            判定依据：{failure.evidence}
            {failure.requestId ? ` · 请求号 ${failure.requestId}` : ''}
          </p>
        </div>
        <Button
          tone="ghost"
          className="min-h-8 shrink-0 px-2 text-xs"
          aria-label="关闭提示"
          onClick={() => reportAdminAuthFailure(null)}
        >
          关闭
        </Button>
      </div>
    </div>
  );
}
