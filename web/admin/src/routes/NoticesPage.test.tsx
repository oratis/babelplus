// @vitest-environment jsdom
//
// 需要 DOM：这一页最要紧的那道闸（D12 强制预览）就是「勾完了才点得动」，
// 而按钮点不点得动只有把组件挂起来才能回答。

/**
 * 公告页（模块 12，D12）的测试。
 *
 * 🔴 这一页的核心不是 CRUD，是**域名广播**：读公告的人正处在「面板打不开、
 * 正在找备用地址」的状态，戒备心最低。写错一个字母就是把他们导向陌生站点。
 * 所以下面几条用例钉的是同一件事：
 *
 *   · 正文里每一个链接的目标主机名都被单独列出来；
 *   · 逐条勾选核对 + 一次通读确认之后，发布按钮才点得动；
 *   · **正文改一个字，之前的核对全部作废**（否则「先核对、再手滑改、直接发」这条路是通的）。
 *
 * ⚠️ 这道闸只在前端，服务端不知道你有没有预览过。它挡的不是攻击者，是正要手滑的自己。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { resetRuntimeConfig } from '@babelplus/shared';
import NoticesPage, { buildNoticeDraft, previewBlockReason } from './NoticesPage.tsx';
import { resetAdminApiForTests } from '../lib/api.ts';
import { reportAdminAuthFailure } from '../lib/iap.ts';
import type { Notice } from './catalog-common.tsx';

const REQ = '01K2NOTICENOTICENOTICEN0';

/** 一条没有链接的公告：留着它是为了让「新建公告」按钮在页面上唯一。 */
const PLAIN: Notice = {
  id: 3,
  title: '维护窗口通知',
  content: '本周日 02:00–04:00 维护，期间可能短暂不可用。',
  pinned: false,
  published_at: '2026-08-01T02:00:00Z',
};

const DOMAIN_NOTICE: Notice = {
  id: 4,
  title: '备用访问地址（请收藏）',
  // 三种写法都要被抓到：markdown 链接、裸 URL、带 userinfo 的钓鱼写法。
  content:
    '备用地址：[镜像一](https://mirror-a.example.net) 或 https://mirror-b.example.net\n' +
    '不要访问 https://babelplus.com@evil.example/login',
  pinned: true,
  published_at: '2026-08-20T00:00:00Z',
};

/* ────────────────────────── fetch 替身 ────────────────────────── */

interface Call {
  readonly method: string;
  readonly path: string;
  readonly query: URLSearchParams;
  readonly body: unknown;
}

let calls: Call[] = [];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errorEnvelope(status: number, code: string, message: string): Response {
  return json({ error: { code, message }, meta: { request_id: REQ } }, status);
}

function stubFetch(handler: (call: Call) => Response) {
  const spy = vi.fn(async (input: unknown, init?: RequestInit) => {
    const url = new URL(String(input));
    const method = (init?.method ?? 'GET').toUpperCase();
    let body: unknown;
    const raw = init?.body;
    if (raw !== undefined && raw !== null) {
      const text = typeof raw === 'string' ? raw : new TextDecoder().decode(raw as ArrayBuffer);
      try {
        body = JSON.parse(text);
      } catch {
        body = text;
      }
    }
    const call: Call = { method, path: url.pathname, query: url.searchParams, body };
    calls.push(call);
    return handler(call);
  });
  vi.stubGlobal('fetch', spy);
  return spy;
}

function stubList(notices: readonly Notice[], meta: Record<string, unknown> = {}) {
  return stubFetch((call) => {
    if (call.method === 'GET') return json({ data: notices, meta: { request_id: REQ, ...meta } });
    if (call.method === 'DELETE') return new Response(null, { status: 204 });
    return json({ data: notices[0] ?? PLAIN, meta: { request_id: REQ } }, 201);
  });
}

function writes() {
  return calls.filter((c) => c.method !== 'GET');
}

beforeEach(() => {
  calls = [];
  resetRuntimeConfig();
  resetAdminApiForTests();
  reportAdminAuthFailure(null);
  window.sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/* ────────────────────────── 纯函数 ────────────────────────── */

describe('buildNoticeDraft', () => {
  it('标题与正文都非空才放行', () => {
    expect(buildNoticeDraft({ title: '', content: '正文', pinned: false, publishedAt: '' }).ok).toBe(
      false,
    );
    expect(
      buildNoticeDraft({ title: '标题', content: '   ', pinned: false, publishedAt: '' }).ok,
    ).toBe(false);
    expect(
      buildNoticeDraft({ title: '标题', content: '正文', pinned: true, publishedAt: '' }).ok,
    ).toBe(true);
  });

  it('发布时间留空 = 字段整个不发出去（= 立刻发布 / 不改）', () => {
    const draft = buildNoticeDraft({ title: '标题', content: '正文', pinned: false, publishedAt: '' });
    expect(draft.ok).toBe(true);
    if (!draft.ok) return;
    expect('published_at' in draft.value).toBe(false);
  });
});

describe('previewBlockReason', () => {
  it('有域名没勾完 → 挡住', () => {
    expect(
      previewBlockReason({ hosts: ['a.example', 'b.example'], verified: ['a.example'], acknowledged: true }),
    ).toBe('unverified-hosts');
  });

  it('域名勾完了但没通读确认 → 仍然挡住', () => {
    expect(
      previewBlockReason({ hosts: ['a.example'], verified: ['a.example'], acknowledged: false }),
    ).toBe('not-acknowledged');
  });

  it('没有域名的公告也要通读确认（强制预览不因为没链接而消失）', () => {
    expect(previewBlockReason({ hosts: [], verified: [], acknowledged: false })).toBe(
      'not-acknowledged',
    );
    expect(previewBlockReason({ hosts: [], verified: [], acknowledged: true })).toBeNull();
  });
});

/* ────────────────────────── 列表三态 ────────────────────────── */

describe('公告列表', () => {
  it('把正文里的链接目标域名单独列出来（含 userinfo 钓鱼写法的真实目标）', async () => {
    stubList([DOMAIN_NOTICE]);
    render(<NoticesPage />);

    expect(await screen.findByText('备用访问地址（请收藏）')).toBeTruthy();
    expect(screen.getByText('mirror-a.example.net')).toBeTruthy();
    expect(screen.getByText('mirror-b.example.net')).toBeTruthy();
    // 🔴 `https://babelplus.com@evil.example/` 的真实目标是 evil.example ——
    //    按浏览器的口径取 host，不能取 @ 前面那段。
    expect(screen.getByText('evil.example')).toBeTruthy();
    expect(screen.queryByText('babelplus.com')).toBeNull();
    // 徽标文本在 <td> 与 <span> 上各匹配一次（祖先也算）。
    expect(screen.getAllByText('置顶').length).toBeGreaterThan(0);
  });

  it('没有链接的公告显示「无链接」', async () => {
    stubList([PLAIN]);
    render(<NoticesPage />);
    expect(await screen.findByText('无链接')).toBeTruthy();
  });

  it('列表为空 → 空态给出下一步动作', async () => {
    stubList([]);
    render(<NoticesPage />);
    expect(await screen.findByText('还没有公告')).toBeTruthy();
  });

  it('501 → 说「还没上线」', async () => {
    stubFetch(() => errorEnvelope(501, 'NOT_IMPLEMENTED', '尚未实现'));
    render(<NoticesPage />);
    expect(await screen.findByText('公告列表尚未开放')).toBeTruthy();
  });

  it('403 AUTH_PERMISSION_DENIED → 说明重新登录没有用', async () => {
    stubFetch(() => errorEnvelope(403, 'AUTH_PERMISSION_DENIED', '角色不足'));
    render(<NoticesPage />);
    expect(await screen.findByText('当前管理员账号看不到这一块')).toBeTruthy();
  });
});

/* ────────────────────────── D12：强制预览 ────────────────────────── */

describe('D12 · 发布公告', () => {
  const CONTENT =
    '备用地址：https://mirror-a.example.net 与 https://mirror-b.example.net，请收藏。';

  async function openEditorWith(content: string) {
    stubList([PLAIN]);
    render(<NoticesPage />);
    await screen.findByText('维护窗口通知');
    fireEvent.click(screen.getByRole('button', { name: '新建公告' }));
    fireEvent.change(screen.getByLabelText('标题'), { target: { value: '备用访问地址' } });
    fireEvent.change(screen.getByLabelText('正文（Markdown）'), { target: { value: content } });
    fireEvent.click(screen.getByRole('button', { name: '发布公告' }));
  }

  function submitButton() {
    return screen.getByRole('button', { name: '确认发布' });
  }

  it('🔴 域名没逐条核对完 → 点不动，也不会发出任何写请求', async () => {
    await openEditorWith(CONTENT);

    expect(submitButton().getAttribute('aria-disabled')).toBe('true');
    fireEvent.click(submitButton());
    expect(writes()).toHaveLength(0);
    expect(screen.getAllByText(/逐条核对勾选/).length).toBeGreaterThan(0);
  });

  it('域名勾完但没通读确认 → 仍然点不动', async () => {
    await openEditorWith(CONTENT);

    fireEvent.click(screen.getByLabelText(/mirror-a\.example\.net/));
    fireEvent.click(screen.getByLabelText(/mirror-b\.example\.net/));

    expect(submitButton().getAttribute('aria-disabled')).toBe('true');
    fireEvent.click(submitButton());
    expect(writes()).toHaveLength(0);
  });

  it('勾完 + 通读确认 → POST 一次，body 是标题与正文原文', async () => {
    await openEditorWith(CONTENT);

    fireEvent.click(screen.getByLabelText(/mirror-a\.example\.net/));
    fireEvent.click(screen.getByLabelText(/mirror-b\.example\.net/));
    fireEvent.click(screen.getByLabelText(/逐字读过/));

    expect(submitButton().getAttribute('aria-disabled')).toBe('false');
    fireEvent.click(submitButton());

    await waitFor(() => expect(writes()).toHaveLength(1));
    const post = writes()[0]!;
    expect(post.method).toBe('POST');
    expect(post.path).toBe('/api/v1/admin/notices');
    const body = post.body as Record<string, unknown>;
    expect(body['title']).toBe('备用访问地址');
    expect(body['content']).toBe(CONTENT);
    // 🔴 NoticeUpsert 契约里没有 reason 字段，所以这份 body 里不该有它。
    expect('reason' in body).toBe(false);
  });

  it('🔴 核对完之后改动正文 → 之前的核对作废，按钮重新变灰', async () => {
    await openEditorWith(CONTENT);

    fireEvent.click(screen.getByLabelText(/mirror-a\.example\.net/));
    fireEvent.click(screen.getByLabelText(/mirror-b\.example\.net/));
    fireEvent.click(screen.getByLabelText(/逐字读过/));
    expect(submitButton().getAttribute('aria-disabled')).toBe('false');

    // 手滑改了一个字母：babelplus → babe1plus 这类改动正是这一层要挡的。
    fireEvent.change(screen.getByLabelText('正文（Markdown）'), {
      target: { value: CONTENT.replace('mirror-a', 'mirror-c') },
    });

    expect(submitButton().getAttribute('aria-disabled')).toBe('true');
    fireEvent.click(submitButton());
    expect(writes()).toHaveLength(0);
  });

  it('确认面板里不出现「操作原因」框（契约的 NoticeUpsert 没有 reason 字段）', async () => {
    await openEditorWith(CONTENT);
    expect(screen.queryByLabelText('操作原因（必填）')).toBeNull();
    expect(screen.getAllByText(/不会写下原因|不会写下操作原因/).length).toBeGreaterThan(0);
  });

  it('正文里没有链接时，预览明说「可能是漏了备用地址」', async () => {
    await openEditorWith('本周日维护，期间可能短暂不可用。');
    expect(screen.getByText(/说明它漏了/)).toBeTruthy();
  });
});

describe('D12 · 删除公告', () => {
  it('确认面板里列出这条公告承载的域名，确认后发 DELETE', async () => {
    stubList([DOMAIN_NOTICE]);
    render(<NoticesPage />);
    await screen.findByText('备用访问地址（请收藏）');

    fireEvent.click(screen.getByRole('button', { name: '编辑 / 删除' }));
    fireEvent.click(screen.getByRole('button', { name: '删除这条公告' }));

    expect(screen.getByText(/正在找备用地址的用户就看不到它们了/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '确认删除' }));

    await waitFor(() => {
      const del = calls.filter((c) => c.method === 'DELETE');
      expect(del).toHaveLength(1);
      expect(del[0]!.path).toBe('/api/v1/admin/notices/4');
    });
  });
});
