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
    // 🔴 这三条是「归一化之后才出现的 //」。它们以**单个** `/` 开头，不含反斜杠与控制字符，
    // 解析后 origin 也仍是本站 —— 前面每一条检查都过得去。
    // 但 `/..` 被解析掉之后 pathname 变成 `//evil.example`，这是协议相对 URL，
    // 交给 navigate() 再解析一次就落到 https://evil.example/。
    // 原因是所有前置检查作用在**原串**上，而返回的是**归一化后的串**。
    ['归一化后暴露出协议相对 URL', '/..//evil.example'],
    ['多级 .. 之后暴露出协议相对 URL', '/a/b/../../..//evil.example'],
    ['百分号编码的 .. 之后暴露出协议相对 URL', '/%2e%2e//evil.example'],
  ])('拒绝 %s', (_name, raw) => {
    expect(safeReturnTo(raw, ORIGIN)).toBeNull();
  });

  it('任何非 null 结果都必须是单斜杠开头的站内路径（对结果本身的不变量）', () => {
    const probes = [
      '/dashboard',
      '/..//evil.example',
      '/a/b/../../..//evil.example',
      '/order/ORD-1?tab=pay',
      '/dashboard#/../auth/login',
      '/%2e%2e//evil.example',
    ];
    for (const raw of probes) {
      const result = safeReturnTo(raw, ORIGIN);
      if (result === null) continue;
      expect(result.startsWith('/'), `${raw} → ${result}`).toBe(true);
      expect(result.startsWith('//'), `${raw} → ${result}`).toBe(false);
      // 决定性判据：把结果按浏览器的规则再解析一次，必须还在本站。
      expect(new URL(result, ORIGIN).origin, `${raw} → ${result}`).toBe(ORIGIN);
    }
  });

  it('拒绝认证四页，避免登录成功后跳回登录页的来回', () => {
    expect(safeReturnTo('/auth/login', ORIGIN)).toBeNull();
    expect(safeReturnTo('/auth/register?x=1', ORIGIN)).toBeNull();
    // 归一化之后仍然落在 /auth/ 下的写法也要挡住。
    expect(safeReturnTo('/dashboard/../auth/login', ORIGIN)).toBeNull();
    // react-router 的路由匹配默认大小写不敏感，`/AUTH/login` 照样会匹配到那条路由，
    // 所以黑名单也必须大小写不敏感，否则改个大小写就绕过去了。
    expect(safeReturnTo('/AUTH/login', ORIGIN)).toBeNull();
    expect(safeReturnTo('/Auth/Register', ORIGIN)).toBeNull();
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
