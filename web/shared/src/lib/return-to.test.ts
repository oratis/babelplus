import { describe, expect, it } from 'vitest';
import { RETURN_TO_PARAM, loginUrlWithReturnTo, safeReturnTo } from './return-to.ts';

const ORIGIN = 'https://panel.example';

describe('safeReturnTo —— 开放重定向', () => {
  it('接受站内路径，并保留 query 与 hash', () => {
    expect(safeReturnTo('/order/ORD-1', ORIGIN)).toBe('/order/ORD-1');
    expect(safeReturnTo('/usage?range=30d', ORIGIN)).toBe('/usage?range=30d');
    expect(safeReturnTo('/ticket/abc#last', ORIGIN)).toBe('/ticket/abc#last');
  });

  // 这一组每一条都是真实的开放重定向向量。改动 safeReturnTo 时先看这里。
  it.each([
    ['绝对 URL', 'https://evil.example/'],
    ['协议相对 URL', '//evil.example/'],
    ['反斜杠变体（多数浏览器等同于 //）', '/\\evil.example/'],
    ['路径中夹反斜杠', '/dashboard\\@evil.example'],
    ['javascript 伪协议', 'javascript:alert(1)'],
    ['data 伪协议', 'data:text/html,<script>1</script>'],
    ['不以 / 开头的相对路径', 'dashboard'],
    ['前导换行绕过 startsWith 检查', '\n/dashboard'],
    ['内嵌制表符', '/dash\tboard'],
    ['空串', ''],
  ])('拒绝 %s', (_name, raw) => {
    expect(safeReturnTo(raw, ORIGIN)).toBeNull();
  });

  it('拒绝认证四页，避免登录成功后跳回登录页的来回', () => {
    expect(safeReturnTo('/auth/login', ORIGIN)).toBeNull();
    expect(safeReturnTo('/auth/register?x=1', ORIGIN)).toBeNull();
    // 归一化之后仍然落在 /auth/ 下的写法也要挡住。
    expect(safeReturnTo('/dashboard/../auth/login', ORIGIN)).toBeNull();
  });

  it('拒绝超长输入', () => {
    expect(safeReturnTo(`/${'a'.repeat(4096)}`, ORIGIN)).toBeNull();
  });
});

describe('loginUrlWithReturnTo', () => {
  it('把合法来路编码进查询串', () => {
    expect(loginUrlWithReturnTo('/auth/login', '/order/ORD-1?tab=pay')).toBe(
      `/auth/login?${RETURN_TO_PARAM}=${encodeURIComponent('/order/ORD-1?tab=pay')}`,
    );
  });

  it('来路非法时**不带** returnTo，而不是带一个已知非法的值过去', () => {
    expect(loginUrlWithReturnTo('/auth/login', 'https://evil.example')).toBe('/auth/login');
    expect(loginUrlWithReturnTo('/auth/login', null)).toBe('/auth/login');
  });
});
