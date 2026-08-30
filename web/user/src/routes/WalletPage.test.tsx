/**
 * `/wallet` 的接线测试。
 *
 * 🔴 **这个文件存在的首要理由是第二个用例：三个金额永远不许被加成一个数。**
 *
 * `getWallet` 返回三个数：余额、确认中的佣金、可划转的佣金。它们**不是同一笔钱**：
 * 确认中的佣金还在退款冷静期内（可能被作废），可划转的佣金还没进余额（要点一次划转）。
 * 而「给用户一个总额看起来更友好」是一个**极其自然**的改动 ——
 * 下一个人很可能会好心地加一行「合计」。加上的那一刻，
 * 「我明明有 ¥50 为什么下单只抵了 ¥20」就成了必然发生的工单。
 * 用例的判据是**反面的**：合计值的字符串一个字都不许出现在页面上。
 *
 * 第三个用例守 ADR 0013 的另一半：**「可提现」必须作为一个独立的、恒为 0 的数字出现**。
 * `handler/wallet.go` 的 `walletView` 里那段裁决逐字写着：契约的 `Wallet` 只有一个
 * `balance_amount`，装不下两个数，所以 `balance_amount` **就是**不可提现余额，
 * 可提现恒为 0 且不加进来。把这一行从页面上删掉（「反正永远是 0，占地方」）之后，
 * 「哪一部分能提」这个问题就再也没有地方可以问了 —— 而那正是
 * 「退款进余额 ⇒ 那部分应该能提出来」这条误解得以生长的空间。
 *
 * 其余用例：常驻声明、空态、流水的方向按符号判、501、分页失败不清空已加载的部分。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import WalletPage from './WalletPage.tsx';
import {
  fail,
  jsonResponse,
  meRoute,
  notImplemented,
  ok,
  okPage,
  renderSignedIn,
  resetAll,
  stubFetch,
} from './account-test-utils.tsx';

const WALLET_PATH = '/api/v1/user/wallet';
const TX_PATH = '/api/v1/user/wallet/transactions';

/** 三个数刻意取成互不相同、且它们的和也是一个独特的串（¥53.10）。 */
const WALLET = {
  balance_amount: 1000, // ¥10.00
  commission_pending_amount: 720, // ¥7.20（轻量档的一次性定额）
  commission_available_amount: 3590, // ¥35.90
};
/** 1000 + 720 + 3590 = 5310 分 = ¥53.10。**这个串不许出现在页面上。** */
const FORBIDDEN_SUM = '¥53.10';
/** 余额 + 可划转佣金 = ¥45.90。这是更容易被误合并的一对，同样不许出现。 */
const FORBIDDEN_PARTIAL_SUM = '¥45.90';

/**
 * 流水的每一个金额都刻意**避开**上面三个余额的展示串 ——
 * 否则 `getByText('¥10.00')` 会同时命中「余额」和某一行的 `balance_after`，
 * 用例就变成了在测「这个串出现过」而不是「余额显示对了」。
 */
const TRANSACTIONS = [
  {
    id: 3,
    type: 'refund',
    amount: 2000, // +¥20.00
    balance_after: 2800, // ¥28.00
    created_at: '2026-08-28T00:00:00Z',
  },
  {
    id: 2,
    type: 'consume',
    amount: -7200, // -¥72.00
    balance_after: 800, // ¥8.00
    note: '标准 · 月付',
    created_at: '2026-08-20T00:00:00Z',
  },
];

beforeEach(resetAll);
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('WalletPage', () => {
  it('成功：三个金额各自成行，常驻的「不可提现」声明在页面上', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [WALLET_PATH]: () => jsonResponse(200, ok(WALLET)),
      [TX_PATH]: () => jsonResponse(200, okPage(TRANSACTIONS)),
    });
    renderSignedIn(<WalletPage />);

    await waitFor(() => expect(screen.getByText('¥10.00')).toBeTruthy());
    expect(screen.getByText('¥7.20')).toBeTruthy();
    expect(screen.getByText('¥35.90')).toBeTruthy();
    // 资金合规底线常驻，不折叠、不藏在条款里。
    expect(screen.getByText('余额只能用于消费，不能提现。')).toBeTruthy();
  });

  it('🔴 三个金额**不相加** —— 合计与部分合计都不许出现在页面上', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [WALLET_PATH]: () => jsonResponse(200, ok(WALLET)),
      [TX_PATH]: () => jsonResponse(200, okPage(TRANSACTIONS)),
    });
    const { container } = renderSignedIn(<WalletPage />);

    await waitFor(() => expect(screen.getByText('¥10.00')).toBeTruthy());

    expect(container.textContent).not.toContain(FORBIDDEN_SUM);
    expect(container.textContent).not.toContain(FORBIDDEN_PARTIAL_SUM);
    // 并且要**明写**为什么不给总数 —— 不写的话，下一个人只会觉得这是漏了。
    expect(screen.getByText(/不相加/)).toBeTruthy();
  });

  it('🔴 「可提现」是一行独立的、恒为 0 的金额，不是被省略掉的概念', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [WALLET_PATH]: () => jsonResponse(200, ok(WALLET)),
      [TX_PATH]: () => jsonResponse(200, okPage(TRANSACTIONS)),
    });
    renderSignedIn(<WalletPage />);

    await waitFor(() => expect(screen.getByText('可提现余额')).toBeTruthy());
    expect(screen.getByText('¥0.00')).toBeTruthy();
    // 退款那一笔也要就地说清楚它退到哪了（ADR 0013 ①）。
    expect(screen.getByText(/退款一律退到余额/)).toBeTruthy();
  });

  it('流水：方向按金额符号判，不按类型标签判', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [WALLET_PATH]: () => jsonResponse(200, ok(WALLET)),
      [TX_PATH]: () => jsonResponse(200, okPage(TRANSACTIONS)),
    });
    renderSignedIn(<WalletPage />);

    // 后端在 ref_type 映射不确定时「按符号给方向」并记 WARN —— 类型标签可能不精确，符号一定对。
    await waitFor(() => expect(screen.getByText('+¥20.00')).toBeTruthy());
    expect(screen.getByText('-¥72.00')).toBeTruthy();
    expect(screen.getByText('退款入账')).toBeTruthy();
    expect(screen.getByText('消费')).toBeTruthy();
  });

  it('流水为空 → 空态给下一步动作，而余额照常显示', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [WALLET_PATH]: () => jsonResponse(200, ok(WALLET)),
      [TX_PATH]: () => jsonResponse(200, okPage([])),
    });
    renderSignedIn(<WalletPage />);

    await waitFor(() => expect(screen.getByText('还没有余额记录')).toBeTruthy());
    expect(screen.getByRole('link', { name: /看看邀请返佣/ })).toBeTruthy();
    expect(screen.getByText('¥10.00')).toBeTruthy();
  });

  it('🔴 余额 501 → 「该功能尚未开放」，流水那一块照常渲染', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [WALLET_PATH]: () => notImplemented(),
      [TX_PATH]: () => jsonResponse(200, okPage(TRANSACTIONS)),
    });
    renderSignedIn(<WalletPage />);

    await waitFor(() => expect(screen.getByText('该功能尚未开放')).toBeTruthy());
    expect(screen.queryByText(/查看状态页/)).toBeNull();
    // 两个请求各挂各的：整页一个 loading 的写法会让下一条断言红。
    expect(await screen.findByText('退款入账')).toBeTruthy();
  });

  it('翻页失败时**不清空**已经加载出来的流水', async () => {
    let page = 0;
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [WALLET_PATH]: () => jsonResponse(200, ok(WALLET)),
      [TX_PATH]: () => {
        page += 1;
        return page === 1
          ? jsonResponse(200, okPage(TRANSACTIONS, { has_more: true, next_cursor: 'CURSOR' }))
          : fail(500, 'INTERNAL_ERROR', '下一页读不出来');
      },
    });
    renderSignedIn(<WalletPage />);

    fireEvent.click(await screen.findByRole('button', { name: '加载更多' }));

    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    // 用户已经在看的东西不该因为下一页失败而消失。
    expect(screen.getByText('退款入账')).toBeTruthy();
    expect(screen.getByText('+¥20.00')).toBeTruthy();
  });

  it('封禁账号（401 AUTH_PERMISSION_DENIED）→ 说封禁，不说「重新登录」', async () => {
    stubFetch({
      '/api/v1/user/me': meRoute(),
      [WALLET_PATH]: () => fail(401, 'AUTH_PERMISSION_DENIED', '账号已被封禁'),
      [TX_PATH]: () => jsonResponse(200, okPage([])),
    });
    renderSignedIn(<WalletPage />);

    await waitFor(() => expect(screen.getByText('这个账号已被封禁')).toBeTruthy());
  });
});
