package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 钱包 / 邀请码 / 返佣。
//
// ============================================================
// 🔴 读本文件任何一行之前必须先接受的三件事
// ============================================================
//
// **一、余额的唯一真相是分录，wallet_balances 只是缓存**（data-model §7.1）。
//    两者不一致时**服务 ledger 的数**并告警。服务缓存的数等于把一个已知错误的
//    数字当真，而钱的数字错了不能靠「明天对账会发现」。
//
// **二、「余额不可提现」在数据库层面无法强制**（data-model §7.1 逐字）。
//    它的实现方式是 ledger_accounts 里**不存在** `asset:bank ← liability:user_wallet`
//    这条路径，且没有写提现代码。也就是说这条规则的守卫**只有 code review 与接口形状**。
//    本文件对它的贡献是 walletView 里那一行注释与 walletAnomalies 里那条断言 ——
//    可提现余额是一个**字面量 0**，哪天有人要做提现，他必须先改掉它。
//
// **三、金额一律 bigint 人民币分，禁止浮点**（api-contract §2.6）。
//    `ledger_lines.amount` 是有符号的：正 = 借 Dr，负 = 贷 Cr。
//    给用户加钱是贷方（负数），所以用户视角的变动额是 `-amount`。
//    这个负号在 wallet.sql 里出现三次，每一处都不是笔误；本文件不再翻转任何符号
//    （SQL 已经把 delta 与 balance_after 都翻好了）。

// ============================================================
// 余额概览
// ============================================================

// GetWallet 返回余额与佣金概览。
func (s *Server) GetWallet(ctx context.Context, _ gen.GetWalletRequestObject) (gen.GetWalletResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	row, err := s.db.GetWalletOverview(ctx, userID)
	if err != nil {
		return gen.GetWallet500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询钱包概览失败", err),
		}, nil
	}
	s.reportWalletAnomalies(ctx, userID, row)

	return gen.GetWallet200JSONResponse{Data: walletView(row), Meta: s.meta(ctx)}, nil
}

// walletView 把概览行映射成契约的 Wallet。纯函数。
//
// 🔴 **`balance_amount` 取的是 non_withdrawable_amount，不是「可提现 + 不可提现」的和。**
//
//	ADR 0013 ① 裁决「退款**一律退到不可提现的钱包余额**」。这句话在传播时最容易变形成
//	「退款进余额 ⇒ 余额里有一部分是退回来的钱 ⇒ 那部分应该能提出来」，
//	而**把两个数加成一个再发出去，正是那条裁决要防的误解本身**：
//	一旦响应里只剩一个数字，「哪一部分能提」这个问题就再也没有地方可以问了。
//
//	⚠️ **契约的 Wallet 只有一个 balance_amount 字段，装不下两个数。**
//	这不是可以绕过的：openapi 已冻结（CI 有逐字节 contract-drift 门禁）。
//	在这个约束下唯一诚实的实现是：
//	  · balance_amount **就是**不可提现余额（契约给这个字段的描述逐字是
//	    「余额，单位：分。**仅可消费不可提现**」—— 契约自己已经把它定义成不可提现的那一份）；
//	  · 可提现余额恒为 0，**不加进来**；
//	  · 一旦 withdrawable_amount 不再是 0（有人做了提现功能却没回来改这里），
//	    walletAnomalies 会把它变成一条 ERROR 日志，而不是静默地少给用户一笔钱。
//	把 withdrawable 加进 balance_amount 才是最坏的选择：那会让「可提现」这个概念
//	在用户可见的地方彻底消失，而钱包页面上的数字反而看起来更大、更「对」。
//
// ⚠️ **来源拆分（from_refund / from_commission / from_order）查出来了但发不出去** ——
//
//	契约的 Wallet 没有对应字段。wallet.sql 选它们是为了让页面能回答
//	「我这 ¥38 是哪来的」，而这个问题在当前契约下无法回答。
//	不为此新增响应字段（会撞 contract-drift 门禁）；已登记在交付说明里。
//	它们仍然会出现在 reportWalletAnomalies 的告警日志里 —— 对账时那才是最有用的三个数。
func walletView(r dbgen.GetWalletOverviewRow) gen.Wallet {
	return gen.Wallet{
		BalanceAmount:             r.NonWithdrawableAmount,
		CommissionPendingAmount:   r.CommissionPendingAmount,
		CommissionAvailableAmount: r.CommissionAvailableAmount,
	}
}

// walletAnomalies 列出这一行里所有「不该发生」的情况。纯函数，便于单测。
//
// 每一条都对应一个真实的失效模式，不是防御式编程：
//
//  1. `withdrawable_amount != 0` —— 有人做了提现却没回来改钱包接口的形状。
//     这一刻用户的可提现余额在响应里是**看不见**的（契约没有字段），
//     所以只能靠这条日志把它喊出来。
//  2. `balance_cached != balance_ledger` —— 缓存漂移。每日 ReconcileWalletBalances
//     也会发现它，但那是明天；用户**现在**正看着这个数字。
//  3. `balance_ledger != non_withdrawable` —— 今天这两列是同一个表达式。
//     哪天有人把余额拆成两个池子却没改 walletView，这条会先响。
//  4. `balance_ledger < 0` —— wallet_balances 有 `CHECK (balance >= 0)`，
//     但**分录聚合没有这个 CHECK**。负数意味着某处扣款只写了分录没走 SpendWalletBalance。
func walletAnomalies(r dbgen.GetWalletOverviewRow) []string {
	var out []string
	if r.WithdrawableAmount != 0 {
		out = append(out, "withdrawable_amount 不为 0：提现功能可能已落地，但 walletView 仍然按「可提现余额恒为 0」发响应")
	}
	if r.BalanceCached != r.BalanceLedger {
		out = append(out, "wallet_balances 缓存与分录聚合不一致，本次以分录为准")
	}
	if r.BalanceLedger != r.NonWithdrawableAmount {
		out = append(out, "balance_ledger 与 non_withdrawable_amount 不再相等：余额已被拆成多个池子，walletView 需要重新裁决")
	}
	if r.BalanceLedger < 0 {
		out = append(out, "分录聚合出的余额为负：可能有扣款只写了分录没走 SpendWalletBalance")
	}
	return out
}

// reportWalletAnomalies 把 walletAnomalies 的结果写成 ERROR 日志。
//
// 用 ERROR 而不是 WARN：这四条里任何一条成立都意味着**用户看到的钱数可能不对**，
// 而钱的数字是唯一一类「用户会截图、会引用、会拿去争论」的数字。
func (s *Server) reportWalletAnomalies(ctx context.Context, userID int64, r dbgen.GetWalletOverviewRow) {
	for _, a := range walletAnomalies(r) {
		s.logger.ErrorContext(ctx, "bp_wallet_anomaly "+a,
			"user_id", userID,
			"balance_ledger", r.BalanceLedger,
			"balance_cached", r.BalanceCached,
			"withdrawable", r.WithdrawableAmount,
			"non_withdrawable", r.NonWithdrawableAmount,
			"from_refund", r.FromRefund,
			"from_commission", r.FromCommission,
			"from_order", r.FromOrder,
		)
	}
}

// ============================================================
// 余额流水
// ============================================================

// ListWalletTransactions 返回余额流水。
//
// 流水**不是一张表**，是 liability:user_wallet 那些分录腿的投影。
// 再建一张 wallet_transactions 就是第二份真相，而 data-model §7.1 已经为
// 「wallet_balances 是缓存」付了一次每日对账的代价，不该再付第二次。
func (s *Server) ListWalletTransactions(ctx context.Context, req gen.ListWalletTransactionsRequestObject) (gen.ListWalletTransactionsResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	want, page := listPageLimit(req.Params.Limit, defaultPageLimit)
	arg := dbgen.ListWalletTransactionsParams{UserID: userID, PageLimit: page}
	if req.Params.Cursor != nil {
		if c, valid := decodePageCursor(string(*req.Params.Cursor)); valid {
			// ⚠️ 只用 id 那一半。ledger_lines.id 是 IDENTITY 列，本身就是全序
			// 且与插入顺序一致，破平手键是多余的。at 那一半仍然要求存在且是时间 ——
			// 契约要求「服务端必须校验解出的字段类型」，而校验的方式就是解不出来就不用它。
			id := c.ID
			arg.CursorID = &id
		} else {
			s.logger.WarnContext(ctx, "余额流水的游标无法解析，已从首页开始",
				"user_id", userID, "cursor_len", len(*req.Params.Cursor))
		}
	}

	rows, err := s.db.ListWalletTransactions(ctx, arg)
	if err != nil {
		return gen.ListWalletTransactions500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询余额流水失败", err),
		}, nil
	}

	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.WalletTransaction, 0, len(rows))
	for _, r := range rows {
		typ, exact := walletTxType(r.RefType, r.Delta)
		if !exact {
			// 映射不确定时**按符号给方向**并记一条 WARN。
			// 方向必须对（钱进来还是出去是用户唯一真正在意的事），
			// 而类型标签错了只是文案不精确。
			s.logger.WarnContext(ctx, "余额流水的 ref_type 无法映射到契约枚举",
				"user_id", userID, "line_id", r.ID, "ref_type", derefOr(r.RefType, "<null>"), "delta", r.Delta)
		}
		out = append(out, gen.WalletTransaction{
			Id:           r.ID,
			Type:         typ,
			Amount:       r.Delta,
			BalanceAfter: r.BalanceAfter,
			CreatedAt:    ttime(r.CreatedAt),
			Note:         noteOf(r.Description),
		})
	}

	meta := s.meta(ctx)
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		c := encodePageCursor(last.ID, ttime(last.CreatedAt))
		meta.NextCursor = &c
	}
	return gen.ListWalletTransactions200JSONResponse{Data: out, Meta: meta}, nil
}

// walletTxType 把 (ref_type, delta) 映射成契约的 WalletTransaction.type。
// 第二个返回值 false = 这次映射是按符号兜的底，不是确定的对应关系。
//
// ⚠️ **两套词汇表不是一一对应的**（wallet.sql §2 已逐条登记）：
//
//	契约枚举   recharge / consume / refund / commission_transfer / admin_adjust / expired_order_credit
//	ref_type   order / refund / commission / reconcile_adjust
//
//	· `order` 一个值要按 delta 的符号劈成两个：正 = 充值进余额，负 = 余额抵扣订单；
//	· `expired_order_credit`（订单过期后到账入余额，data-model 的兜底）
//	  在 ref_type 里**没有专属值**，它现在也走 order —— 于是它在流水页上
//	  会显示成 recharge。要区分就得让写入方给一个更细的 ref_type，
//	  那是写入侧的一次改动，不是这里能补的；已登记在交付说明里。
//	· 认不出来的 ref_type（含 NULL）按符号兜底，**不映射成 admin_adjust** ——
//	  把一笔来源不明的钱说成「管理员调整」是编造事实，
//	  而按符号给方向至少每一个字都是真的。
func walletTxType(refType *string, delta int64) (gen.WalletTransactionType, bool) {
	if refType != nil {
		switch *refType {
		case "refund":
			return gen.Refund, true
		case "commission":
			return gen.CommissionTransfer, true
		case "reconcile_adjust":
			return gen.AdminAdjust, true
		case "order":
			if delta < 0 {
				return gen.Consume, true
			}
			return gen.Recharge, true
		}
	}
	if delta < 0 {
		return gen.Consume, false
	}
	return gen.Recharge, false
}

// noteOf 把分录摘要变成可选的 note。
//
// ⚠️ description 会**直接出现在用户的流水页上**。写分录的地方要注意别把内部术语
// 或对方用户的身份写进去 —— 它不是内部字段（wallet.sql §2 的原话）。
// 这里不做任何加工：加工等于在两个地方各有一份文案，而只有一份会被改。
func noteOf(desc string) *string {
	if strings.TrimSpace(desc) == "" {
		return nil
	}
	d := desc
	return &d
}

func derefOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

// ============================================================
// 邀请码
// ============================================================

const (
	// inviteCodeQuota 是「每用户未核销码 ≤ N」的 N（data-model §4.1）。
	//
	// 它是**运营参数**不是常量语义：改它不该要一次 sqlc 重新生成 + contract-drift 复核，
	// 所以 CreateUserInviteCode 把它做成了参数。3 这个数字来自 data-model §4.1。
	inviteCodeQuota int32 = 3

	// inviteCodeAlphabet 是生成邀请码的字符集。
	//
	// 0003 的列注释：「大写，剔除 0/O/1/I/l 等易混字符」。剔除的理由是这个码
	// **会被人手抄、会被念出来**（邀请是熟人动作）——
	// 一个 0/O 分不清的码会变成一次「你这码是不是给错了」的对话。
	// 剔掉 0 O 1 I L 之后剩 31 个字符。
	inviteCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

	// inviteCodeLen 是码长。8 位 × log2(31) ≈ 39.6 bit。
	// 熵不是安全边界（邀请码本来就靠唯一索引 + 名额闸门，不靠猜不到），
	// 而是撞码概率：几千条码在 31⁸ ≈ 8.5e11 的空间里撞一次的概率可以忽略。
	inviteCodeLen = 8

	// createInviteRetries 是撞码重试次数。同 createSubTokenRetries：
	// 重试存在的理由不是概率，是「唯一索引冲突不该变成 500」。
	createInviteRetries = 3
)

// CreateInviteCode 生成一条一次性邀请码。
func (s *Server) CreateInviteCode(ctx context.Context, _ gen.CreateInviteCodeRequestObject) (gen.CreateInviteCodeResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	var row dbgen.InviteCode
	for attempt := 0; ; attempt++ {
		code, err := randomInviteCode()
		if err != nil {
			return gen.CreateInviteCode500JSONResponse{
				ErrInternalJSONResponse: s.internalErr(ctx, "生成邀请码失败", err),
			}, nil
		}
		// 🔴 **名额闸门在 INSERT 的 WHERE 里，不是先 count 再 insert。**
		// 后者在两条语句之间的并发请求会双双通过（TOCTOU），而「并发」在这里不是
		// 理论问题：用户在生成按钮上连点两下就够了。
		// ⚠️ 这条收窄了竞态但**没有关闭**它（READ COMMITTED 下两个事务都能看到 count=2）。
		// 不加 advisory lock 是因为代价与收益不成比例：输掉竞态的后果是「多了一条邀请码」，
		// 而 users.sql 的 FindUsersOverInviteCodeQuota 巡检本来就在找这种行。
		// 🔴 名额规则一旦带上金钱含义（比如每个码带奖励），这段推理立刻失效，必须补锁。
		row, err = s.db.CreateUserInviteCode(ctx, dbgen.CreateUserInviteCodeParams{
			Code:        code,
			OwnerUserID: userID,
			MaxUnused:   inviteCodeQuota,
			// max_uses 由 SQL 写死 1（user-journey §3.2 裁决：用户码恒为一次性核销）。
			// expires_at / note 留空：契约的创建请求没有这两个字段，
			// 凭空给一个有效期等于让用户的邀请在某天突然失效，而他没同意过任何期限。
		})
		if err == nil {
			break
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// 0 行 = 名额已满。openapi 给这个端点声明的 403 就是它。
			s.logger.InfoContext(ctx, "邀请码名额已满", "user_id", userID, "quota", inviteCodeQuota)
			return gen.CreateInviteCode403JSONResponse{
				ErrForbiddenJSONResponse: s.forbidden(ctx, gen.QUOTARATELIMITED,
					fmt.Sprintf("未使用的邀请码最多 %d 条。请等已生成的码被用掉之后再生成新的", inviteCodeQuota)),
			}, nil
		}
		if isUniqueViolation(err) && attempt < createInviteRetries {
			s.logger.WarnContext(ctx, "邀请码撞码，重试", "user_id", userID, "attempt", attempt+1)
			continue
		}
		return gen.CreateInviteCode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "写入邀请码失败", err),
		}, nil
	}

	return gen.CreateInviteCode201JSONResponse{
		Data: inviteCodeView(dbgen.ListUserInviteCodesRow{
			ID:        row.ID,
			Code:      row.Code,
			MaxUses:   row.MaxUses,
			UsedCount: row.UsedCount,
			ExpiresAt: row.ExpiresAt,
			RevokedAt: row.RevokedAt,
			Note:      row.Note,
			CreatedAt: row.CreatedAt,
		}, s.inviteBaseURL(ctx), time.Now()),
		Meta: s.meta(ctx),
	}, nil
}

// randomInviteCode 生成一条邀请码。
//
// 用拒绝采样而不是 `b % 31`：256 不是 31 的整数倍，直接取模会让前 8 个字符
// 比后 23 个多出约 3% 的出现概率。对邀请码而言这点偏斜无害，
// 但它是零成本可以避免的，而「邀请码分布有偏」这种事没人会去复查。
// （与 auth.go 的 randomDigits 同一手法。）
func randomInviteCode() (string, error) {
	const n = len(inviteCodeAlphabet) // 31
	// 248 = 31 × 8，丢弃 248–255。
	const limit = byte(248)
	out := make([]byte, 0, inviteCodeLen)
	buf := make([]byte, 1)
	for len(out) < inviteCodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成邀请码随机数失败: %w", err)
		}
		if buf[0] >= limit {
			continue
		}
		out = append(out, inviteCodeAlphabet[int(buf[0])%n])
	}
	return string(out), nil
}

// ListInviteCodes 列出用户的邀请码（**含已吊销与已过期的**）。
//
// 🔴 不复用 users.sql 的 ListInviteCodesByOwner：它带 `revoked_at IS NULL`，
// 于是 InviteCode.status 的 `disabled` 永远发不出去 ——
// 用户以为吊销的码「消失了」，再生成一个，然后撞上名额闸门得到一个
// 他完全无法理解的 403。
func (s *Server) ListInviteCodes(ctx context.Context, _ gen.ListInviteCodesRequestObject) (gen.ListInviteCodesResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	rows, err := s.db.ListUserInviteCodes(ctx, userID)
	if err != nil {
		return gen.ListInviteCodes500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询邀请码列表失败", err),
		}, nil
	}

	base := s.inviteBaseURL(ctx)
	now := time.Now()
	out := make([]gen.InviteCode, 0, len(rows))
	for _, r := range rows {
		out = append(out, inviteCodeView(r, base, now))
	}
	return gen.ListInviteCodes200JSONResponse{Data: out, Meta: s.meta(ctx)}, nil
}

// inviteCodeView 映射一条邀请码。纯函数（now 与 base 由调用方注入）。
//
// ⚠️ **契约的三值枚举 [ok, exhausted, disabled] 装不下「已过期」**，
// 只能把它并进 disabled。这是契约的表达力不足，不是这里的选择 ——
// 页面上应当用文案把两者区分开：**吊销是他自己撤的，过期是他忘了用，动作不同**。
// 已登记在交付说明里。
//
// 判定顺序与 auth.go 的 classifyInviteCode 保持一致（先吊销/过期，再用尽）：
// 一个「被吊销且已用完」的码报 disabled 而不是 exhausted，因为吊销是更强的陈述。
//
// ⚠️ `use_limit` 的契约描述是「0 = 不限」，而用户码的 max_uses 恒为 1
// （invite_codes_user_single_use 这条 CHECK 强制），所以这里永远发 1。
func inviteCodeView(r dbgen.ListUserInviteCodesRow, base string, now time.Time) gen.InviteCode {
	v := gen.InviteCode{
		Id:        r.ID,
		Code:      r.Code,
		CreatedAt: ttime(r.CreatedAt),
		UseLimit:  ptrOf(r.MaxUses),
		UsedCount: ptrOf(r.UsedCount),
	}
	switch {
	case r.RevokedAt.Valid:
		v.Status = gen.InviteCodeStatusDisabled
	case r.ExpiresAt.Valid && !r.ExpiresAt.Time.After(now):
		v.Status = gen.InviteCodeStatusDisabled
	case r.UsedCount >= r.MaxUses:
		v.Status = gen.InviteCodeStatusExhausted
	default:
		v.Status = gen.InviteCodeStatusOk
	}
	// invite_url 只对还能用的码有意义 —— 给一条已吊销的码配一个可点的链接，
	// 用户会把它发出去，然后被邀请的人在注册页拿到一个「邀请码无效」。
	if v.Status == gen.InviteCodeStatusOk && base != "" {
		u := base + "/register?invite=" + r.Code
		v.InviteUrl = &u
	}
	return v
}

// inviteBaseURL 是邀请链接的域名 —— **Web 前端的域名，不是 API 的**。
//
// ⚠️ 与 subscription.go 的 subDeps 同一处将就：config 里没有独立的
// 「Web 主域名」配置项，这里取 CORS 白名单的第一项。
// TODO(P2): 加 BP_WEB_BASE_URL。借用 CORS 白名单意味着「放行某个 Origin」
// 与「邀请链接印哪个域名」被绑死了，而 ADR 0002 的失联恢复会轮换 Web 域名。
// config 不在本轮的可写范围内，故只登记不动手。
//
// 取不到时返回空串，invite_url 就不下发（契约里它是可选字段）——
// 发一条 `/register?invite=XXXX` 的相对链接给用户，他复制到聊天软件里就是一条死链。
func (s *Server) inviteBaseURL(_ context.Context) string {
	if len(s.cfg.AllowedOrigins) == 0 {
		return ""
	}
	return strings.TrimRight(s.cfg.AllowedOrigins[0], "/")
}

// ============================================================
// 佣金
// ============================================================

// ListCommissions 列出返佣记录。
//
// 🔴 **返佣口径是一次性定额**（定价修订 C6）：该用户**首单档位的月付标价 × 10%**
// = ¥7.20 / ¥15.90 / ¥35.80，与订单实际周期无关，每位被邀请用户只发一次。
// 本端点只读不算 —— 计提在 orders.sql 的 CreateCommission，
// 而那条的 rate_bps / amount 是调用方算好传进去的，**没有任何东西保证符合 C6**。
// 这条风险登记在 wallet.sql §4 与交付说明里。
//
// 🔴 **order_trade_no 是被邀请人的订单号，出现在邀请人页面上。**
// ListUserCommissions 已经用 `inviter_id` 把归属钉死；
// 但任何**其他**按 trade_no 取单的用户面 handler 都必须自己带 user_id 条件 ——
// orders.sql 的 GetOrderByTradeNo 没有 user_id 约束，那是一条现成的 IDOR 入口。
func (s *Server) ListCommissions(ctx context.Context, req gen.ListCommissionsRequestObject) (gen.ListCommissionsResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	want, page := listPageLimit(req.Params.Limit, defaultPageLimit)
	arg := dbgen.ListUserCommissionsParams{InviterID: userID, PageLimit: page}
	if req.Params.Cursor != nil {
		if c, valid := decodePageCursor(string(*req.Params.Cursor)); valid {
			id := c.ID
			arg.CursorID = &id
		} else {
			s.logger.WarnContext(ctx, "佣金列表的游标无法解析，已从首页开始",
				"user_id", userID, "cursor_len", len(*req.Params.Cursor))
		}
	}

	rows, err := s.db.ListUserCommissions(ctx, arg)
	if err != nil {
		return gen.ListCommissions500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询佣金列表失败", err),
		}, nil
	}

	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.Commission, 0, len(rows))
	for _, r := range rows {
		out = append(out, commissionView(r))
	}

	meta := s.meta(ctx)
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		c := encodePageCursor(last.ID, ttime(last.CreatedAt))
		meta.NextCursor = &c
	}
	return gen.ListCommissions200JSONResponse{Data: out, Meta: meta}, nil
}

// commissionView 映射一条佣金。纯函数。
//
// ⚠️ **status 缺一格，而缺的那一格必须留在列表里。**
//
//	契约：[pending, confirmed, settled]；DB：pending / confirmed / transferred / voided。
//	transferred → settled 是显然的。**voided 在契约里没有对应值**，而两个候选都是谎话：
//	  · 映射成 settled = 告诉用户「这笔已经到账了」，而它永远不会到；
//	  · 映射成 pending = 让他一直等一笔永远不会来的钱。
//	唯一诚实的做法是**保留这一行、并让它在前端可以被单独渲染**。
//	当前契约下能做到的是：status 发 pending（不谎称已结算），
//	而 order_trade_no 与 amount 照常给 —— 用户至少能看到这一笔存在、金额多少，
//	并且它不会被算进任何「可划转」的合计（GetWalletOverview 的 available
//	只数 confirmed，voided 一分都不进）。
//	🔴 这条冲突必须登记进 api-contract §14；根治要么给契约加一个枚举值，
//	要么把 voided 的行在用户面过滤掉（后者会让「我明明看到过一笔佣金」变成悬案）。
func commissionView(r dbgen.ListUserCommissionsRow) gen.Commission {
	c := gen.Commission{
		Id:          r.ID,
		Amount:      r.Amount,
		CreatedAt:   ttime(r.CreatedAt),
		ConfirmedAt: tptr(r.ConfirmedAt),
		Status:      commissionStatus(r.Status),
	}
	if r.OrderTradeNo != "" {
		no := r.OrderTradeNo
		c.OrderTradeNo = &no
	}
	return c
}

// commissionStatus 把 DB 的四态映射成契约的三态。见 commissionView 的注释。
func commissionStatus(dbStatus string) gen.CommissionStatus {
	switch dbStatus {
	case "confirmed":
		return gen.CommissionStatusConfirmed
	case "transferred":
		return gen.CommissionStatusSettled
	case "voided":
		// 见 commissionView：两个候选都是谎话，选伤害较小的那个。
		return gen.CommissionStatusPending
	default:
		return gen.CommissionStatusPending
	}
}

// ---- 划转 ----

// commissionTransferRetryAfter 是科目缺失时给的退避秒数。
//
// 取 300（5 分钟）而不是 task.go 的 30 秒：那 30 秒是给 Cloud Scheduler 的
// 「下一轮之前重投」，而这里的「依赖」是一支**还没跑的 migration** ——
// 30 秒后重试必然还是失败，只会让用户连点五次。
const commissionTransferRetryAfter int32 = 300

// transferCommission503JSONResponse 是 transferCommission 的 503 响应。
//
// 🔴 **openapi 给这个端点只声明了 401/409/422/500，没有 503。**
// 明知如此仍然发 503，理由是这两个状态码对用户的含义完全不同：
//   - 500 说的是「我们出了个偶发故障」→ 用户会重试，每次都失败，然后开工单；
//   - 503 + Retry-After 说的是「这个功能现在不可用，稍后再来」→ 前端可以
//     直接把按钮置灰并显示等待时间。
//
// 而这里的失败**不是偶发**：它是 ledger_accounts 缺 `expense:commission` 这一行
// （0018 补的那支 migration 没跑），在 migration 落地之前**每一次都会失败**。
// wallet.sql §5 的原话就是「在那条 migration 落地之前，transferCommission
// 应当返回 501/503 而不是 500 —— 500 会让用户以为是偶发故障并反复重试」。
//
// ⚠️ 这是一处**刻意的契约偏差**，必须登记进 api-contract §14。
// 它不触发 contract-drift 门禁（那条门禁比对的是 openapi.yaml 的字节，
// 而本文件一个字都没改它），所以只有这段注释与交付说明会记得它。
type transferCommission503JSONResponse struct {
	gen.ErrDependencyDownJSONResponse
}

func (r transferCommission503JSONResponse) VisitTransferCommissionResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", fmt.Sprint(r.Headers.RetryAfter))
	w.Header().Set("X-Request-Id", fmt.Sprint(r.Headers.XRequestId))
	w.WriteHeader(http.StatusServiceUnavailable)
	return json.NewEncoder(w).Encode(r.Body)
}

// 划转过程中的三种可判定失败。用哨兵错误在事务闭包里传出来，
// 因为 InTx 只能返回 error，而这三种要映射成三个不同的 HTTP 码。
var (
	// errTransferNoCommission：一条可划转的佣金都没有 → 422。
	errTransferNoCommission = errors.New("没有可划转的佣金")
	// errTransferAmountMismatch：金额不等于任何一个前缀和 → 422。
	errTransferAmountMismatch = errors.New("划转金额与可划转的佣金条目对不上")
	// errTransferRaced：MarkCommissionsTransferredBulk 改动的行数少于预期 → 409。
	errTransferRaced = errors.New("佣金状态在划转过程中被改动")
)

// TransferCommission 把已确认的佣金划转到余额。**佣金只能划转到余额，不可提现。**
func (s *Server) TransferCommission(ctx context.Context, req gen.TransferCommissionRequestObject) (gen.TransferCommissionResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	if req.Body == nil || req.Body.Amount < 1 {
		return gen.TransferCommission422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "划转金额必须是正整数（单位：分）",
				detail("amount", "必须 ≥ 1")),
		}, nil
	}
	amount := req.Body.Amount

	// ---- 步 0：提前失败 ----
	//
	// 🔴 **在动 commissions.status、动 wallet_balances 之前先把两个科目取出来。**
	// 划转的贷方腿是 liability:user_wallet（0015 seed 里有），
	// 借方腿是 expense:commission —— **0015 的 seed 里没有它**
	// （0015 逐字照抄 ADR 0012 §17.6(c) 的科目表，而佣金整个属于 ADR 0013 §3.5，
	//  两份 ADR 的交界处没有任何东西会说话）。
	// 少一条腿的分录会当天就让 FindUnbalancedLedgerEntries 报红，
	// 而那时已经无法知道该以佣金表还是以分录为准 —— 所以宁可一条都不写。
	accounts, ok2 := s.commissionAccounts(ctx, userID)
	if !ok2 {
		return transferCommission503JSONResponse{
			ErrDependencyDownJSONResponse: gen.ErrDependencyDownJSONResponse{
				Body: s.envelope(ctx, gen.INTERNALDEPENDENCYDOWN,
					"佣金划转暂不可用，请稍后再试"),
				Headers: gen.ErrDependencyDownResponseHeaders{
					RetryAfter: commissionTransferRetryAfter,
					XRequestId: middleware.RequestIDFrom(ctx),
				},
			},
		}, nil
	}

	var picked []int64
	var available int64
	var prefixes []int64
	var wallet dbgen.GetWalletOverviewRow

	// ---- 步 1–5：一个事务 ----
	//
	// 🔴 **五步必须同一事务。** 拆开的后果不是数字错，是**账不平**：
	// 改了 commissions 却没写分录 → 用户的佣金消失且余额没增加；
	// 写了分录却没改 commissions → 同一笔佣金可以再划一次。
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		// FOR UPDATE，按 (confirmed_at, id) 定序。
		// 不锁的话，用户在两个标签页同时点划转，同一条佣金会被划两次 ——
		// 第二次的 UPDATE 命中 0 行，但**分录已经写了两遍**，用户凭空多出一份余额。
		// 顺序必须确定，否则两个并发事务按不同顺序锁行就会死锁。
		locked, err := q.LockTransferableCommissions(ctx, userID)
		if err != nil {
			return err
		}
		if len(locked) == 0 {
			return errTransferNoCommission
		}

		// 累加和在 Go 里做：PostgreSQL 直接拒绝 `FOR UPDATE` 与窗口函数同处一条 SELECT
		// （`FOR UPDATE is not allowed with window functions`）。
		picked, available, prefixes = pickCommissionsForAmount(locked, amount)
		if picked == nil {
			return errTransferAmountMismatch
		}

		rows, err := q.MarkCommissionsTransferredBulk(ctx, dbgen.MarkCommissionsTransferredBulkParams{
			Ids:       picked,
			InviterID: userID,
		})
		if err != nil {
			return err
		}
		// 🔴 **必须断言 rows == len(ids)，不等就回滚整个事务。**
		// 语句里的 `status = 'confirmed'` 与 `inviter_id` 是防线不是保证：
		// rows 少了就意味着有别人抢先改了状态，此时继续写分录就是凭空造钱。
		if rows != int64(len(picked)) {
			return fmt.Errorf("%w: 期望 %d 行，实际 %d 行", errTransferRaced, len(picked), rows)
		}

		entry, err := q.CreateLedgerEntry(ctx, dbgen.CreateLedgerEntryParams{
			EntryNo:     newEntryNo("CT"),
			Description: fmt.Sprintf("佣金划转到余额（%d 笔）", len(picked)),
			RefType:     ptrOf("commission"),
			// ref_id 取任一条 commission.id（wallet.sql §5 原话）。
			// 取第一条而不是最后一条：ledger_entries_ref_idx 上按 (ref_type, ref_id)
			// 回查时，第一条是这批里确认得最早的那一条，与人的直觉一致。
			RefID: ptrOf(picked[0]),
		})
		if err != nil {
			return err
		}
		// 两条腿：符号相反、绝对值相等、币种同为 CNY。
		// `SUM(lines.amount) = 0`（按 (entry_id, currency) 分组）是 0007 的核心不变量，
		// schema 表达不出来（跨行），只能靠这里。
		if _, err := q.CreateLedgerLine(ctx, dbgen.CreateLedgerLineParams{
			EntryID:   entry.ID,
			AccountID: accounts.ExpenseAccountID,
			Amount:    available, // 借 Dr：我们为拉新付出的成本
			Currency:  ledgerCurrencyCNY,
		}); err != nil {
			return err
		}
		if _, err := q.CreateLedgerLine(ctx, dbgen.CreateLedgerLineParams{
			EntryID:   entry.ID,
			AccountID: accounts.WalletAccountID,
			SubjectID: &userID,    // liability:user_wallet 按 subject_id 分账
			Amount:    -available, // 贷 Cr：欠用户的钱变多了
			Currency:  ledgerCurrencyCNY,
		}); err != nil {
			return err
		}

		// ⚠️ UpsertWalletBalance 的 balance 参数是**增量**不是绝对值
		// （ON CONFLICT 分支写的是 `balance + EXCLUDED.balance`）。
		// 传绝对值的现象是：第一次划转正确，第二次把余额覆盖成只剩这一笔。
		if _, err := q.UpsertWalletBalance(ctx, dbgen.UpsertWalletBalanceParams{
			UserID:      userID,
			Currency:    ledgerCurrencyCNY,
			Balance:     available,
			LastEntryID: &entry.ID,
		}); err != nil {
			return err
		}

		// 在**同一事务**里读一次概览：响应里的余额与刚写进去的分录是同一个快照。
		// 事务外再读一次的话，另一笔并发消费会让用户看到一个「划转之后反而变少了」的数字。
		wallet, err = q.GetWalletOverview(ctx, userID)
		return err
	})

	switch {
	case err == nil:
	case errors.Is(err, errTransferNoCommission):
		return gen.TransferCommission422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "当前没有可划转的佣金"),
		}, nil
	case errors.Is(err, errTransferAmountMismatch):
		// 🔴 **不发明部分划转语义。** commissions 的 status 是**整行**的状态，
		// 没有 amount_transferred 这样的列 —— 一条 ¥15.90 的佣金要么整条 transferred，
		// 要么原封不动。而契约的 CommissionTransferRequest 只有一个自由金额，
		// 形状上允许「划走 ¥3」。两者不兼容（roadmap B37，佣金状态机未设计）。
		// 唯一不撒谎的解读是「amount 必须等于按确认时间从旧到新取前 k 条的累加和」，
		// 对不上就 422 并把可接受的金额告诉用户 —— 让他能一次改对，
		// 而不是在一个自由输入框上反复试。
		return gen.TransferCommission422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("佣金只能整条划转，金额必须等于「按确认时间从旧到新取前 N 条」的合计。可划转合计 %d 分", available),
				detail("amount", "可接受的金额："+formatPrefixSums(prefixes))),
		}, nil
	case errors.Is(err, errTransferRaced):
		s.logger.WarnContext(ctx, "佣金划转遇到并发改动，已回滚", "user_id", userID, "err", err)
		return gen.TransferCommission409JSONResponse{
			ErrConflictJSONResponse: s.conflict(ctx, "佣金状态刚刚发生变化，请刷新后重试"),
		}, nil
	default:
		return gen.TransferCommission500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "佣金划转失败", err),
		}, nil
	}

	s.logger.InfoContext(ctx, "佣金已划转到余额",
		"user_id", userID, "count", len(picked), "amount_cents", available)
	s.reportWalletAnomalies(ctx, userID, wallet)
	return gen.TransferCommission200JSONResponse{Data: walletView(wallet), Meta: s.meta(ctx)}, nil
}

// ledgerCurrencyCNY 是本文件写出的每一条分录腿的币种。
//
// 钉死 CNY：liability:user_wallet 与 expense:commission 在 seed 里都是 CNY，
// 而分录按 `(entry_id, currency)` 分组之后才平 —— 两条腿币种不同的话，
// 各自那一组都不平，且 FindUnbalancedLedgerEntries 会同时报两条。
const ledgerCurrencyCNY = "CNY"

// commissionAccounts 取两个科目 id。第二个返回值 false = 科目缺失，调用方必须 503。
//
// 🔴 **「科目缺失」在这里的形态是「扫描失败」，不是「0 行」，也不是 NULL 指针。**
//
//	GetCommissionTransferAccounts 是 `max(id) FILTER (...)::bigint` 的聚合，
//	恒返回一行，缺科目时那一列是 NULL。wallet.sql 的注释预期
//	「emit_pointers_for_null_types 下是 *int64，判 nil 即可」，
//	但**生成出来的是非指针 int64** —— 显式的 `::bigint` cast 让 sqlc 把它判成了 NOT NULL
//	（同一个坑在 orders_user.sql 的注释里也登记过：「带 cast 的表达式列会被 sqlc 判成 NOT NULL」）。
//	于是 pgx 在 row.Scan 时报 `cannot scan NULL into *int64`，
//	handler 拿到的是一个**错误**而不是一个零值。
//
//	所以这里两条路都堵：
//	  · 错误里带「cannot scan NULL」→ 科目缺失（当前的真实形态）；
//	  · 将来查询被改成 `coalesce(..., 0)` 或重新生成成指针 → 值为 0 也判成缺失。
//	字符串匹配是丑的，但 pgx 没有为这种情况提供可 errors.Is 的类型，
//	而把「科目缺失」误判成 500 的代价是用户对着一个永远不会成功的按钮反复重试。
func (s *Server) commissionAccounts(ctx context.Context, userID int64) (dbgen.GetCommissionTransferAccountsRow, bool) {
	row, err := s.db.GetCommissionTransferAccounts(ctx)
	if err != nil {
		if isNullScanError(err) || errors.Is(err, pgx.ErrNoRows) {
			s.logger.ErrorContext(ctx,
				"bp_ledger_account_missing 佣金划转所需的账本科目缺失（expense:commission），本次请求已拒绝。"+
					"这是**数据缺失不是代码缺陷**：修复方式是跑 migration 0018_ledger_commission_account，"+
					"在那之前本端点会持续返回 503",
				"user_id", userID, "err", err)
			return row, false
		}
		s.logger.ErrorContext(ctx, "查询账本科目失败", "user_id", userID, "err", err)
		return row, false
	}
	if row.ExpenseAccountID == 0 || row.WalletAccountID == 0 {
		s.logger.ErrorContext(ctx,
			"bp_ledger_account_missing 佣金划转所需的账本科目缺失（返回了 0 而不是 NULL），本次请求已拒绝。"+
				"这是数据缺失不是代码缺陷：跑 migration 0018_ledger_commission_account",
			"user_id", userID,
			"expense_account_id", row.ExpenseAccountID,
			"wallet_account_id", row.WalletAccountID)
		return row, false
	}
	return row, true
}

// isNullScanError 识别 pgx 的「NULL 扫进非指针目标」错误。
//
// pgx v5 的原文是 `cannot scan NULL into %T`（pgtype/int.go 等 90 余处）。
// 它没有专门的错误类型，也没有 sentinel，所以只能匹配文本。
// ⚠️ 升级 pgx 时这条会静默失效（现象是科目缺失重新变回 500），
// 守卫在 wallet_test.go 的 TestIsNullScanError 里 —— 那条测试用 pgx 的原文构造错误。
func isNullScanError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "cannot scan NULL")
}

// pickCommissionsForAmount 从锁住的佣金里挑出**累加和恰好等于 amount** 的前 k 条。
//
// 返回 (ids, sum, 所有前缀和)。ids 为 nil 表示没有任何一个前缀和等于 amount。
// 第三个返回值给 422 的错误信息用 —— 让用户一次就能改对。
//
// 🔴 **只支持整条划转，且只支持「前 k 条」。**
//
//	commissions 没有 amount_transferred 列，一条佣金要么整条 transferred 要么不动；
//	而契约的请求体只有一个自由金额、**没有 id 列表**，所以服务端无从知道
//	用户勾选了哪几条。在「只有一个金额」这个输入下，
//	唯一确定的解释就是 LockTransferableCommissions 的排序（confirmed_at, id）下的前缀和。
//	任何「凑出这个金额的任意子集」的实现都是在发明语义：
//	同一个金额可能有多个子集能凑出来，而选哪一个会影响到「哪几笔佣金被划走了」，
//	那是用户看得见的差别。
//
// ⚠️ 金额恰好为 0 的佣金（不应该存在，commissions.amount 没有正数 CHECK）
//
//	会让两个相邻的前缀和相同。此时取**较短**的那个前缀（先匹配先返回），
//	因为多划一条金额为 0 的佣金没有任何意义，却会把它标成 transferred。
func pickCommissionsForAmount(rows []dbgen.LockTransferableCommissionsRow, amount int64) ([]int64, int64, []int64) {
	prefixes := make([]int64, 0, len(rows))
	ids := make([]int64, 0, len(rows))
	var sum int64
	var matched []int64
	for _, r := range rows {
		sum += r.Amount
		ids = append(ids, r.ID)
		prefixes = append(prefixes, sum)
		if matched == nil && sum == amount {
			matched = append([]int64(nil), ids...)
		}
	}
	if matched == nil {
		return nil, sum, prefixes
	}
	// 返回的第二个值是**这次划转的金额**（= amount），不是全部可划转合计。
	// 两者在「全部划转」时相等，在划前 k 条时不等 —— 分录金额必须用前者。
	return matched, amount, prefixes
}

// formatPrefixSums 把可接受的金额列表渲染成一句人能读的话。
//
// 截断到前 maxShown 个：一个邀请了几十个人的用户会有几十个前缀和，
// 把它们全塞进错误信息只会让错误信息本身变得读不了。
func formatPrefixSums(prefixes []int64) string {
	const maxShown = 10
	if len(prefixes) == 0 {
		return "（当前没有可划转的佣金）"
	}
	shown := prefixes
	suffix := ""
	if len(shown) > maxShown {
		shown = shown[:maxShown]
		suffix = fmt.Sprintf(" …（共 %d 个可选值，最大 %d）", len(prefixes), prefixes[len(prefixes)-1])
	}
	parts := make([]string, 0, len(shown))
	for _, p := range shown {
		parts = append(parts, fmt.Sprintf("%d", p))
	}
	return strings.Join(parts, " / ") + suffix
}

// entryNoAlphabet 与邀请码同字符集：entry_no 的**随机后缀**会被人抄进对账单与工单，
// 0/O 与 1/I/l 分不清会变成一次「你给的这个号查不到」。
// ⚠️ 只约束后缀 —— 中间那段是 UTC 时刻，它必然含 0 与 1，
// 而时刻是对账时第一眼要看的东西，不能为了字符集把它去掉。
const entryNoAlphabet = inviteCodeAlphabet

// newEntryNo 生成 ledger_entries.entry_no。
//
// 形如 `CT20260830T091533-7KQ2M9`：前缀说明用途（CT = commission transfer），
// 中间是 UTC 时刻（对账时第一眼要看的就是时间），后缀是 6 位随机。
//
// ⚠️ entry_no 上有 UNIQUE。撞号需要同一秒内同一个后缀，概率约 31⁻⁶ ≈ 1.1e-9；
// 真撞上会让整个划转事务回滚并返回 500，用户重试即可 ——
// **刻意不做重试**：重试要在事务外重开事务，而那会让「锁住的佣金」被释放一次，
// 为了一个十亿分之一的事件引入一条几乎从不执行的并发路径，代价方向反了。
func newEntryNo(prefix string) string {
	suffix := make([]byte, 6)
	buf := make([]byte, 1)
	n := len(entryNoAlphabet)
	for i := 0; i < len(suffix); {
		if _, err := rand.Read(buf); err != nil {
			// 随机源不可用时退化成时间戳纳秒 —— 仍然满足唯一性的常见情形，
			// 而让一次划转因为读不到随机数而失败是把代价放错了地方。
			return prefix + time.Now().UTC().Format("20060102T150405") + fmt.Sprintf("-%09d", time.Now().Nanosecond())
		}
		if buf[0] >= 248 { // 248 = 31 × 8
			continue
		}
		suffix[i] = entryNoAlphabet[int(buf[0])%n]
		i++
	}
	return prefix + time.Now().UTC().Format("20060102T150405") + "-" + string(suffix)
}
