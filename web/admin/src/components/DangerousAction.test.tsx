// @vitest-environment jsdom
//
// 门禁判据（`dangerBlockReason`）是纯函数，本可以在 node 环境里测完；
// 但「按钮到底有没有变灰、点下去会不会真的发请求」是另一回事 ——
// 判据绿着而组件里少接了一处 `disabled` 的话，纯函数测试一个字都不会说。
// 所以两层都测：纯函数定规则，组件测把规则接上了没有。

/**
 * 危险操作确认组件的测试。
 *
 * ⚠️ **这些用例证明的不是安全性。** §6.2 的四层全在服务端强制，
 * 这里钉住的只是「前端有没有把参数收齐、有没有在收齐之前就把请求发出去」。
 * 一个绕过前端直接 `curl` 的人不受这里任何一条约束 —— 那由 API 层的测试负责。
 *
 * 三条硬要求（任务书逐字）：
 *  · 确认串不匹配时不许提交
 *  · reason 少于 8 **码位**时不许提交（码位不是 `String.length`）
 *  · 缺 TOTP 时不许提交
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { ApiError } from '@babelplus/shared/api';
import {
  DangerousAction,
  MIN_REASON_RUNES,
  dangerBlockReason,
  reasonRuneCount,
  type DangerGateInput,
} from './DangerousAction.tsx';

afterEach(cleanup);

/** D6（手工标记订单已支付）—— 唯一一条四层全要的：确认串 + 原因 + TOTP + 独立权限位。 */
const TRADE_NO = '20260816T7K2M9Q4';

function gate(overrides: Partial<DangerGateInput> = {}): DangerGateInput {
  return {
    permission: 'unknown',
    needsConfirmation: true,
    expectedConfirmation: TRADE_NO,
    confirmation: TRADE_NO,
    needsReason: true,
    reason: '链上已确认到账，网关回调丢失',
    needsTotp: true,
    totp: '481920',
    submitting: false,
    disabled: false,
    ...overrides,
  };
}

describe('dangerBlockReason', () => {
  it('四层都齐了才放行', () => {
    expect(dangerBlockReason(gate())).toBeNull();
  });

  it('L1：确认串没逐字打对 → 挡住', () => {
    expect(dangerBlockReason(gate({ confirmation: '' }))).toBe('confirmation-mismatch');
    expect(dangerBlockReason(gate({ confirmation: TRADE_NO.slice(0, -1) }))).toBe('confirmation-mismatch');
    // 区分大小写：与服务端 `confirmationMatches` 同口径（它两侧 trim 但不改大小写）。
    expect(dangerBlockReason(gate({ confirmation: TRADE_NO.toLowerCase() }))).toBe('confirmation-mismatch');
    // 首尾空白两侧都 trim —— 期望值是从页面上复制来的，尾随空格是复制粘贴的常态。
    expect(dangerBlockReason(gate({ confirmation: ` ${TRADE_NO} ` }))).toBeNull();
  });

  it('L1：登记表要求确认串但调用方没给期望值 → 判成装配错误，而不是静默跳过这一层', () => {
    expect(dangerBlockReason(gate({ expectedConfirmation: null }))).toBe('missing-confirmation-target');
    expect(dangerBlockReason(gate({ expectedConfirmation: '   ' }))).toBe('missing-confirmation-target');
  });

  it(`L2：原因少于 ${MIN_REASON_RUNES} 码位 → 挡住`, () => {
    expect(dangerBlockReason(gate({ reason: '' }))).toBe('reason-too-short');
    expect(dangerBlockReason(gate({ reason: '补单' }))).toBe('reason-too-short');
    // 7 个字不够，8 个字够。
    expect(dangerBlockReason(gate({ reason: '链上已确认到账' }))).toBe('reason-too-short');
    expect(dangerBlockReason(gate({ reason: '链上已确认到账了' }))).toBeNull();
    // 一串空格凑不出长度：服务端先 TrimSpace 再数，这里同口径。
    expect(dangerBlockReason(gate({ reason: '          ' }))).toBe('reason-too-short');
  });

  it('L2：数的是**码位**不是 String.length —— 四个星标 emoji 有 8 个 code unit 但只有 4 个字', () => {
    const fourEmoji = '🔴🔴🔴🔴';
    // 这一行是本用例的全部理由：按 String.length 判会放行，按 rune 判会挡住，
    // 而服务端用的是 rune（utf8.RuneCountInString）。放行的后果是服务端退回一个
    // 「原因至少 8 个字符」的 422，而用户明明看见输入框里有八个字符宽的内容。
    expect(fourEmoji.length).toBe(8);
    expect(reasonRuneCount(fourEmoji)).toBe(4);
    expect(dangerBlockReason(gate({ reason: fourEmoji }))).toBe('reason-too-short');
  });

  it('L3：没有 6 位数字码 → 挡住', () => {
    expect(dangerBlockReason(gate({ totp: '' }))).toBe('totp-missing');
    expect(dangerBlockReason(gate({ totp: '12345' }))).toBe('totp-missing');
    expect(dangerBlockReason(gate({ totp: '1234567' }))).toBe('totp-missing');
    expect(dangerBlockReason(gate({ totp: '12345a' }))).toBe('totp-missing');
    expect(dangerBlockReason(gate({ totp: '481920' }))).toBeNull();
  });

  it('L4：确知没有权限位时，先说这一条 —— 不要让人先去改确认串', () => {
    expect(dangerBlockReason(gate({ permission: 'denied', confirmation: '' }))).toBe('permission-denied');
    // `unknown` 是默认值，而且**放行**：管理面没有任何端点会告诉前端权限位，
    // 前端猜「你没有」会让一个真的有权限的人对着灰按钮束手无策。
    expect(dangerBlockReason(gate({ permission: 'unknown' }))).toBeNull();
  });
});

describe('<DangerousAction>（D6：确认串 + 原因 + TOTP）', () => {
  function open(props: Partial<Parameters<typeof DangerousAction>[0]> = {}) {
    const onSubmit = vi.fn(async () => {});
    render(
      <DangerousAction
        code="D6"
        submitLabel="标记为已支付"
        confirmation={TRADE_NO}
        permissionName="admin.order.mark_paid"
        onSubmit={onSubmit}
        {...props}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: '手工标记订单已支付' }));
    return { onSubmit, submit: () => screen.getByRole('button', { name: '标记为已支付' }) as HTMLButtonElement };
  }

  function type(label: string, value: string) {
    fireEvent.change(screen.getByLabelText(label), { target: { value } });
  }

  it('确认串不匹配时不许提交', () => {
    const { onSubmit, submit } = open();
    type('操作原因（必填）', '链上已确认到账了');
    type('验证器 6 位码', '481920');

    type('输入订单号以确认', `${TRADE_NO}x`);
    expect(submit().disabled).toBe(true);
    fireEvent.click(submit());
    expect(onSubmit).not.toHaveBeenCalled();

    type('输入订单号以确认', TRADE_NO);
    expect(submit().disabled).toBe(false);
  });

  it(`reason 少于 ${MIN_REASON_RUNES} 码位时不许提交`, () => {
    const { onSubmit, submit } = open();
    type('输入订单号以确认', TRADE_NO);
    type('验证器 6 位码', '481920');

    type('操作原因（必填）', '链上已确认到账'); // 7 个字
    expect(submit().disabled).toBe(true);
    fireEvent.click(submit());
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('至少 8 个字');

    type('操作原因（必填）', '链上已确认到账了'); // 8 个字
    expect(submit().disabled).toBe(false);
  });

  it('缺 TOTP 时不许提交', () => {
    const { onSubmit, submit } = open();
    type('输入订单号以确认', TRADE_NO);
    type('操作原因（必填）', '链上已确认到账了');

    expect(submit().disabled).toBe(true);
    fireEvent.click(submit());
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('6 位码');

    type('验证器 6 位码', '481920');
    expect(submit().disabled).toBe(false);
  });

  it('四层齐了才提交，且三个值原样交给调用方（前端不替服务端做任何判断）', () => {
    const { onSubmit, submit } = open();
    type('输入订单号以确认', ` ${TRADE_NO} `);
    type('操作原因（必填）', '  链上 txid 7f3a 已确认到账  ');
    type('验证器 6 位码', '481920');

    fireEvent.click(submit());
    expect(onSubmit).toHaveBeenCalledTimes(1);
    // 只做与服务端同口径的 trim，不做别的加工。
    expect(onSubmit).toHaveBeenCalledWith({
      confirmation: TRADE_NO,
      reason: '链上 txid 7f3a 已确认到账',
      totp: '481920',
    });
  });

  it('确认串的提示必须说明「服务端会比对」，而不是让人以为这是个前端弹窗', () => {
    open();
    expect(screen.getByText(/这个串由服务端自己查出来后再比对/)).toBeTruthy();
  });

  it('权限位不足：按钮变灰但**不隐藏**，并说明缺的是授权而不是功能', () => {
    const { onSubmit, submit } = open({ permission: 'denied' });
    expect(submit().disabled).toBe(true);
    fireEvent.click(submit());
    expect(onSubmit).not.toHaveBeenCalled();

    // 「你没有这个权限」与「这个功能不存在」必须是两句话。
    expect(screen.getByText(/这不是功能缺失，也不是故障/)).toBeTruthy();
    expect(screen.getAllByText(/admin\.order\.mark_paid/).length).toBeGreaterThan(0);
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('功能是存在的，缺的是授权');
  });

  it('登记表要求确认串但调用方没给期望值 → 变灰并明说是装配错误，不静默跳过 L1', () => {
    const { onSubmit, submit } = open({ confirmation: null });
    type('操作原因（必填）', '链上已确认到账了');
    type('验证器 6 位码', '481920');

    expect(submit().disabled).toBe(true);
    fireEvent.click(submit());
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByTestId('danger-blocked-hint').textContent).toContain('装配错误');
  });
});

describe('<DangerousAction> 的兜底', () => {
  it('D 编号不在登记表里 → 按装配错误渲染，绝不按「无要求」放行', () => {
    render(<DangerousAction code="D99" submitLabel="做点什么" onSubmit={vi.fn(async () => {})} />);
    expect(screen.getByText('危险操作装配错误')).toBeTruthy();
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('提交失败后清空 TOTP —— 那个码已经在服务端报废了，拿它重试必然再失败一次', async () => {
    const onSubmit = vi.fn(async () => {
      throw new ApiError({ status: 409, code: 'STATE_CONFLICT', message: '订单状态已变' });
    });
    render(
      <DangerousAction code="D6" submitLabel="标记为已支付" confirmation={TRADE_NO} onSubmit={onSubmit} />,
    );
    fireEvent.click(screen.getByRole('button', { name: '手工标记订单已支付' }));
    fireEvent.change(screen.getByLabelText('输入订单号以确认'), { target: { value: TRADE_NO } });
    fireEvent.change(screen.getByLabelText('操作原因（必填）'), { target: { value: '链上已确认到账了' } });
    fireEvent.change(screen.getByLabelText('验证器 6 位码'), { target: { value: '481920' } });

    fireEvent.click(screen.getByRole('button', { name: '标记为已支付' }));
    expect(await screen.findByText('当前状态不允许这次操作')).toBeTruthy();
    expect((screen.getByLabelText('验证器 6 位码') as HTMLInputElement).value).toBe('');
  });
});
