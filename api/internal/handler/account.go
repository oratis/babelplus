package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
)

// 账户设置：通知偏好与用户侧 2FA。
//
// 本文件只有两个 operation 是真正实现的（通知偏好的读与写）。
// 另外三个（enroll / verify / disable TOTP）**刻意保持 501**，
// 理由写在文件末尾那一节 —— 那不是没写完，是当前 schema 下写不出来，
// 而且冻结的契约自己就声明了 501。

// ============================================================
// 通知偏好
// ============================================================

// serviceBroadcastReason 是 service_broadcast 这个只读开关的解释文案。
//
// 🔴 **它必须解释「为什么不能关」，而不只是标记「不能关」。**
// 一个没有理由的灰色开关会被理解成 bug 或者「他们不想让我关」，
// 而真实理由恰恰是站在用户一边的：ADR 0002 认定邮件广播是**唯一**的失联恢复通道 ——
// 域名被封之后，我们能够到用户的路只剩这一条。他把它关掉的那天，
// 就是我们再也够不到他的那天。
const serviceBroadcastReason = "服务通告包含域名变更等无法通过其他渠道送达的信息，" +
	"是站点被屏蔽后唯一能联系到你的方式，因此不可关闭。到期提醒与流量提醒可以自由开关。"

// GetNotificationPrefs 返回通知偏好。
func (s *Server) GetNotificationPrefs(ctx context.Context, _ gen.GetNotificationPrefsRequestObject) (gen.GetNotificationPrefsResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}

	// 刻意不复用 users.sql 的 GetUserByID（SELECT *）：那会把 password_hash、
	// uuid（节点侧的连接凭据）、last_login_ip 一并带进一个只需要两个布尔值的 handler，
	// 而「设置页把 uuid 顺手序列化进响应」这类事故的代价远高于一条窄查询。
	row, err := s.db.GetUserNotificationPrefs(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.GetNotificationPrefs401JSONResponse{
				ErrUnauthorizedJSONResponse: s.unauthorizedDeletedUser(ctx, userID, "getNotificationPrefs"),
			}, nil
		}
		return gen.GetNotificationPrefs500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询通知偏好失败", err),
		}, nil
	}
	return gen.GetNotificationPrefs200JSONResponse{
		Data: notificationPrefsView(row.NotifyExpire, row.NotifyTraffic),
		Meta: s.meta(ctx),
	}, nil
}

// UpdateNotificationPrefs 部分更新通知偏好。
//
// 🔴 **两个字段都是可选的**（NotificationPrefsUpdate 没有 required 列表），
// 所以「只发 expire_remind」是一个合法请求。用 coalesce(narg, 旧值) 的那条 SQL
// 而不是 users.sql 的 UpdateUserNotifyPrefs（两个参数都必填）：
// 后者要求 handler 先读一遍再整体回写，而 read-modify-write 在用户于两个设备上
// 同时改设置时，会把后到的那次读到的旧值写回去 ——
// 现象是「刚关掉的开关过一会儿自己又开了」，没有报错、没有日志、无法复现。
//
// ⚠️ **契约要求的「客户端硬塞 service_broadcast → 422」这里没有实现。**
// 不是遗漏，是这一层拿不到原始请求体：oapi-codegen 的 strict handler 在调用
// 本函数**之前**就已经把 body 解成了 NotificationPrefsUpdate（那个结构体里
// 根本没有 service_broadcast 字段），r.Body 到这里已经读空。
// 要实现它需要在 strict 中间件里缓冲一次 body 并用 DisallowUnknownFields 重解，
// 或者挂上 oapi-codegen 的请求校验中间件 —— 两者都在 cmd/server 的装配里，
// 不在本轮的可写范围。**已登记在交付说明里。**
//
// 值得说清楚的是：契约要 422 的**目的**（「它必须在 API 层就不可写，
// 否则总有一天会有人把它做成可写的」）在这里是**完全达成**的 ——
// 生成的请求类型里没有这个字段，account.sql 里没有对应的列，
// users 表上也没有。多余字段被静默忽略，而它想写的东西在三层上都不存在。
// 缺的只是「告诉客户端他写错了」这一句话。
func (s *Server) UpdateNotificationPrefs(ctx context.Context, req gen.UpdateNotificationPrefsRequestObject) (gen.UpdateNotificationPrefsResponseObject, error) {
	userID, ok := s.currentUser(ctx)
	if !ok {
		return nil, errNoUserAuth
	}
	if req.Body == nil {
		return gen.UpdateNotificationPrefs422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}

	// 空请求体 `{}` 会走到这里并原地重写一行 users（updated_at 前进、产生一个死元组）。
	// 接受这个代价：换来的是 handler 只有一条路径。
	// 想靠 `WHERE ... IS DISTINCT FROM` 把无变化的请求挡掉的话，返回 0 行会和
	// 「用户不存在」撞成同一个 ErrNoRows，而 PUT 必须回 200 + 当前值。
	row, err := s.db.UpdateUserNotificationPrefs(ctx, dbgen.UpdateUserNotificationPrefsParams{
		UserID:        userID,
		ExpireRemind:  req.Body.ExpireRemind,
		TrafficRemind: req.Body.TrafficRemind,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.UpdateNotificationPrefs401JSONResponse{
				ErrUnauthorizedJSONResponse: s.unauthorizedDeletedUser(ctx, userID, "updateNotificationPrefs"),
			}, nil
		}
		return gen.UpdateNotificationPrefs500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "更新通知偏好失败", err),
		}, nil
	}

	// 用 RETURNING 的后像而不是「UPDATE 完再 SELECT 一次」：两次之间插进另一个 PUT 时，
	// 返回的组合会是一个数据库里从未同时存在过的状态。
	return gen.UpdateNotificationPrefs200JSONResponse{
		Data: notificationPrefsView(row.NotifyExpire, row.NotifyTraffic),
		Meta: s.meta(ctx),
	}, nil
}

// notificationPrefsView 组装响应。纯函数。
//
// 🔴 service_broadcast **不来自数据库**，它是 API 层的常量。
// 0003_accounts.up.sql 在 users 的通知偏好段落上逐字写着
// 「schema 上表达这条裁决的方式就是**不提供**『全部通知』总开关那一列」——
// 所以这里也**不要**为了跟响应体「对称」而去加一列再在 handler 里忽略它：
// 那一列一旦存在，总有一天会有人把它接到 PUT 上。
func notificationPrefsView(expire, traffic bool) gen.NotificationPrefs {
	return gen.NotificationPrefs{
		ExpireRemind:  expire,
		TrafficRemind: traffic,
		ServiceBroadcast: gen.LockedBoolean{
			Value:  true,
			Locked: true,
			Reason: serviceBroadcastReason,
		},
	}
}

// ============================================================
// 用户侧 TOTP：三个 operation 刻意保持 501
// ============================================================
//
// 🔴 **这三个方法存在的唯一目的是把「为什么还是 501」写在有人会看的地方。**
// 不写它们的话，Server 会继承 Unimplemented 的同名方法，行为完全一样，
// 但下一个人只能在 unimplemented.gen.go 里看到一行「尚未实现」，
// 然后花半天重新走一遍下面这段推导。
//
// ---- 一、冻结的契约自己就要求 501 ----
//
// openapi.yaml 对 enrollUserTotp 的 description 逐字是
// 「**P3，未实现。** 服务端返回 `501` 直到实现完成」，三个端点的 summary 也都带 (P3)。
// 也就是说返回 501 **不是** contract drift，而是契约明写的当前状态。
//
// ---- 二、schema 里没有可写的地方（逐列核过，不是没找到）----
//
//  1. **users 表上没有 totp_secret_enc / totp_confirmed_at / totp_enabled 中的任何一列。**
//     带 totp 的那两列只长在 admin_users 上（0002_foundation.up.sql:56–57），
//     而且都是 NOT NULL —— 那是「数据库层面不存在没有 2FA 的管理员」（data-model §11.2），
//     一条**与用户侧方向相反**的设计：管理员不许不开，用户是可选开
//     （api-contract §6.1 把用户侧 TOTP 标为「可选，P3」）。
//     所以 admin_users 那两列既不能复用，形状也不能照抄。
//
//  2. **`used_totp` 装不下用户侧的 code。** 它的主键是 (admin_user_id, code_hash)，
//     而 admin_user_id 是**指向 admin_users 的外键**（0015_payment_fixes.up.sql:92）。
//     🔴 把 users.id 塞进去有两重后果：外键直接违反（写不进去），
//     以及**假如**将来有人把外键去掉，两个 id 空间会重叠 ——
//     某个用户就能把某个管理员的 code「用掉」，或者反过来。
//     一张防重放表会变成跨界的拒绝服务面。
//     ⚠️ 这一条最容易看漏：api-contract 与任务书都只说「需 used_totp 表防重放」，
//     **没说是谁的表**。0015 把它纳入的理由逐字是「D6 是本 ADR 亲手扩大的欺诈面」，
//     而 D6 是管理面的「手工标记已支付」，从头到尾没有用户侧的份。
//
//  3. account.sql 的 TOTP 一节**一条查询都没有**，且文件里写明了这是核对之后的结论
//     而不是漏写。硬规矩要求复用既有查询、缺了要报告 —— 这里就是报告。
//
// ---- 三、落地时需要什么（写下来只为省一次重新推导，不构成已裁决的方案）----
//
//   - users.totp_secret_enc bytea NULL + users.totp_confirmed_at timestamptz NULL。
//     必须可空（用户侧是可选 2FA）。CurrentUser.totp_enabled 应当由
//     `totp_confirmed_at IS NOT NULL` 算出来，**不要**再加一列布尔值：
//     两个来源迟早不同步，而不同步的表现是「界面说没开，登录却要验证码」。
//   - enroll 与 verify 之间那份**尚未确认**的 secret 需要落点。复用上面两列、
//     用 totp_confirmed_at IS NULL 表示「绑定中」最省事，但那样一次半途而废的 enroll
//     会覆盖掉已经绑好的 secret ——所以 enroll 的写入必须带
//     `WHERE totp_confirmed_at IS NULL`，否则「打开一次绑定页」就等于把用户已有的
//     2FA 弄坏，而他要到下次登录才发现。
//   - 防重放要么新开一张用户侧的表，要么把 used_totp 改成
//     (subject_kind, subject_id, code_hash)。
//   - 校验与占用的**顺序与语义必须与 middleware/admin.go 的 RequireStepUp 一致**：
//     先验对再占用（反过来会让任何人拿一串随机 6 位数把 used_totp 灌满，
//     并把用户真正要用的那个 code 提前占掉 —— 一个免费的拒绝服务）；
//     code_hash 用密钥化 HMAC 并把主体 id 拌进去（6 位数字只有 10⁶ 种，
//     裸 sha256 等于没哈希）；「码错」与「码已用过」必须**不可区分**
//     （能区分就等于告诉重放者「这个码曾经是对的」）。
//
// 补齐这些需要一次 migration，而迁移不由本阶段加。

// EnrollUserTotp 见本节开头：契约声明 501，schema 无落点。
func (s *Server) EnrollUserTotp(ctx context.Context, _ gen.EnrollUserTotpRequestObject) (gen.EnrollUserTotpResponseObject, error) {
	s.noteTotpDemand(ctx, "enrollUserTotp")
	return nil, ErrNotImplemented
}

// VerifyUserTotp 见本节开头：契约声明 501，schema 无落点。
func (s *Server) VerifyUserTotp(ctx context.Context, _ gen.VerifyUserTotpRequestObject) (gen.VerifyUserTotpResponseObject, error) {
	s.noteTotpDemand(ctx, "verifyUserTotp")
	return nil, ErrNotImplemented
}

// DisableUserTotp 见本节开头：契约声明 501，schema 无落点。
//
// ⚠️ 这一个尤其**不能**退化成 204。「解绑 2FA」返回成功而实际什么都没做，
// 会让一个以为自己关掉了 2FA 的用户在下次登录时被挡在门外，
// 而他手上可能已经把 authenticator 里的条目删掉了。
func (s *Server) DisableUserTotp(ctx context.Context, _ gen.DisableUserTotpRequestObject) (gen.DisableUserTotpResponseObject, error) {
	s.noteTotpDemand(ctx, "disableUserTotp")
	return nil, ErrNotImplemented
}

// noteTotpDemand 记一次「有人来敲这个门」。
//
// INFO 而不是 WARN：501 是当前的正确行为，不是故障。
// 但这条日志是判断「用户侧 2FA 该不该从 P3 提前」的唯一数据 ——
// 一个从来没人调用的端点和一个每天被调用几十次的端点，优先级不该一样。
func (s *Server) noteTotpDemand(ctx context.Context, op string) {
	s.logger.InfoContext(ctx, "bp_user_totp_unimplemented 用户侧 2FA 尚未落地（P3，契约声明 501）", "op", op)
}
