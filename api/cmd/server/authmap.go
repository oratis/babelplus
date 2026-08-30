// 本文件是**鉴权装配的唯一事实源**：128 个 operation 各需要哪种凭据。
//
// 分类直接来自 openapi.yaml 里每个 operation 的 `security` 段（全局 security 为 []，
// 每个 operation 都显式声明了自己的方案，没有任何一个是「继承默认」的）。
// 五张表互不相交、并集恰好等于 gen.StrictServerInterface 的全部方法 ——
// 这一条由 TestOperationAuthCoverage 在测试阶段强制，不是靠人肉核对。
//
// 🔴 为什么要有这个覆盖性测试：上一版这里把节点面的
// `PushUniProxyStatus` 写成了 `GetUniProxyStatus`（不存在的 operationID）。
// 表里查不到 → 该 operation 被当作「非节点面」**原样放行，不做任何鉴权**。
// 当时无害（handler 仍返回 501），但实现 /status 的那一刻它就是一个无鉴权写端点。
// 这类拼写错误在运行时完全静默，只有反射比对能抓住。
package main

import (
	"context"
	"net/http"

	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/handler"
	mw "github.com/oratis/babelplus/api/internal/middleware"
)

// nodeOperationScopes 是节点面 operation 到所需 scope 的映射。
//
// 与其他四张表不同，它是 map[string]string 而不是 map[string]bool ——
// 节点密钥是**按 scope 白名单**授权的，「是不是节点面」与「需要哪个 scope」
// 必须在同一处声明，分开写就会出现「表里有这个 operation，但 scope 字段忘了改」。
//
// 共 6 个。
var nodeOperationScopes = map[string]string{
	"GetUniProxyConfig":    "node:config:read",   // GET  /api/v1/server/UniProxy/config
	"GetUniProxyUsers":     "node:users:read",    // GET  /api/v1/server/UniProxy/user
	"GetUniProxyAliveList": "node:alive:read",    // GET  /api/v1/server/UniProxy/alivelist
	"PushUniProxyTraffic":  "node:traffic:write", // POST /api/v1/server/UniProxy/push
	"PushUniProxyAlive":    "node:alive:write",   // POST /api/v1/server/UniProxy/alive
	"PushUniProxyStatus":   "node:status:write",  // POST /api/v1/server/UniProxy/status
}

// userSessionOperations 是需要**用户会话**的 operation 全集。
//
// 免登录的那 11 个在 handler.PublicOperations 里（那份清单同时被 handler 包用到）。
// 两张表分开维护而不是取补集：deny-by-default 的补集算法在「新增了一个
// 管理面 operation」时会把它误算成用户面，而管理面凭据完全不同。
// 共 41 个。
var userSessionOperations = map[string]bool{
	"CancelOrder":                 true, // POST /api/v1/orders/{trade_no}/cancel
	"ChangePassword":              true, // PUT /api/v1/user/password
	"CloseTicket":                 true, // POST /api/v1/tickets/{public_id}/close
	"CreateInviteCode":            true, // POST /api/v1/user/invite/codes
	"CreateOrder":                 true, // POST /api/v1/orders
	"CreateSubscriptionToken":     true, // POST /api/v1/user/subscription/tokens
	"CreateTicket":                true, // POST /api/v1/tickets
	"CreateTicketMessage":         true, // POST /api/v1/tickets/{public_id}/messages
	"DisableUserTotp":             true, // DELETE /api/v1/user/2fa
	"EnrollUserTotp":              true, // POST /api/v1/user/2fa/enroll
	"GetCurrentUser":              true, // GET /api/v1/user/me
	"GetNotificationPrefs":        true, // GET /api/v1/user/notification-prefs
	"GetOrder":                    true, // GET /api/v1/orders/{trade_no}
	"GetOrderPayment":             true, // GET /api/v1/orders/{trade_no}/payment
	"GetTicket":                   true, // GET /api/v1/tickets/{public_id}
	"GetUserDiagnose":             true, // GET /api/v1/user/diagnose
	"GetUserSubscription":         true, // GET /api/v1/user/subscription
	"GetUserUsage":                true, // GET /api/v1/user/usage
	"GetWallet":                   true, // GET /api/v1/user/wallet
	"KickAllUserDevices":          true, // DELETE /api/v1/user/devices
	"KickUserDevice":              true, // DELETE /api/v1/user/devices/{id}
	"ListCommissions":             true, // GET /api/v1/user/commissions
	"ListInviteCodes":             true, // GET /api/v1/user/invite/codes
	"ListNotices":                 true, // GET /api/v1/notices
	"ListOrders":                  true, // GET /api/v1/orders
	"ListPlans":                   true, // GET /api/v1/plans
	"ListSubscriptionFetchLog":    true, // GET /api/v1/user/subscription/fetch-log
	"ListSubscriptionTokens":      true, // GET /api/v1/user/subscription/tokens
	"ListTickets":                 true, // GET /api/v1/tickets
	"ListUserDevices":             true, // GET /api/v1/user/devices
	"ListUserNodes":               true, // GET /api/v1/user/nodes
	"ListWalletTransactions":      true, // GET /api/v1/user/wallet/transactions
	"Logout":                      true, // POST /api/v1/auth/logout
	"PayOrder":                    true, // POST /api/v1/orders/{trade_no}/pay
	"RecheckOrderPayment":         true, // POST /api/v1/orders/{trade_no}/recheck
	"RevokeAllSubscriptionTokens": true, // POST /api/v1/user/subscription/revoke-all
	"RevokeSubscriptionToken":     true, // DELETE /api/v1/user/subscription/tokens/{id}
	"TransferCommission":          true, // POST /api/v1/user/commissions/transfer
	"UpdateNotificationPrefs":     true, // PUT /api/v1/user/notification-prefs
	"VerifyCoupon":                true, // POST /api/v1/coupons/verify
	"VerifyUserTotp":              true, // POST /api/v1/user/2fa/verify
}

// adminOperations 是管理面 operation 全集（adminSession 或 adminIap）。
//
// 凭据是 IAP 断言 + admin_users 查身份（mw.AuthenticateAdmin）。
// 危险操作（api-contract §6.2 L3）额外要一次当次 TOTP —— 那一层是**按操作**的，
// 由 handler 自己调 cfg.RequireStepUp，不在这张表里。
//
// 🔴 这批端点里有 D6（手工标记订单已支付）—— api-contract 称之为
// 「全系统最大的内部欺诈面」。它们此前一律返 501（fail-closed），
// 因为鉴权还没实现；把 501 换成真鉴权的那一刻，这张表就成了
// 「谁能碰这 61 个端点」的唯一声明。新增 admin operation 时必须同时加进这里，
// 漏了会落到 default 分支 —— 仍然是 501，但 TestOperationAuthCoverage 会先在 CI 里报错。
//
// 共 61 个。
var adminOperations = map[string]bool{
	"AdjustAdminCommission":        true, // POST /api/v1/admin/commissions/{id}/adjust
	"AdjustAdminUserBalance":       true, // POST /api/v1/admin/users/{id}/balance-adjust
	"BanAdminUser":                 true, // POST /api/v1/admin/users/{id}/ban
	"BroadcastAdminMail":           true, // POST /api/v1/admin/mail/broadcast
	"CreateAdmin":                  true, // POST /api/v1/admin/admins
	"CreateAdminCoupon":            true, // POST /api/v1/admin/coupons
	"CreateAdminDomain":            true, // POST /api/v1/admin/domains
	"CreateAdminInvite":            true, // POST /api/v1/admin/invites
	"CreateAdminNode":              true, // POST /api/v1/admin/nodes
	"CreateAdminNodeKey":           true, // POST /api/v1/admin/nodes/{id}/keys
	"CreateAdminNotice":            true, // POST /api/v1/admin/notices
	"CreateAdminPlan":              true, // POST /api/v1/admin/plans
	"CreateAdminTicketMessage":     true, // POST /api/v1/admin/tickets/{id}/messages
	"DeleteAdmin":                  true, // DELETE /api/v1/admin/admins/{id}
	"DeleteAdminCoupon":            true, // DELETE /api/v1/admin/coupons/{id}
	"DeleteAdminDomain":            true, // DELETE /api/v1/admin/domains/{id}
	"DeleteAdminNode":              true, // DELETE /api/v1/admin/nodes/{id}
	"DeleteAdminNotice":            true, // DELETE /api/v1/admin/notices/{id}
	"DeleteAdminPlan":              true, // DELETE /api/v1/admin/plans/{id}
	"DisableAdminNode":             true, // POST /api/v1/admin/nodes/{id}/disable
	"EnableAdminNode":              true, // POST /api/v1/admin/nodes/{id}/enable
	"ExportAdminStats":             true, // GET /api/v1/admin/stats/export
	"ExportAdminUsers":             true, // POST /api/v1/admin/users/export
	"GetAdminDashboard":            true, // GET /api/v1/admin/dashboard
	"GetAdminNode":                 true, // GET /api/v1/admin/nodes/{id}
	"GetAdminOrder":                true, // GET /api/v1/admin/orders/{trade_no}
	"GetAdminSettings":             true, // GET /api/v1/admin/settings
	"GetAdminStats":                true, // GET /api/v1/admin/stats
	"GetAdminTicket":               true, // GET /api/v1/admin/tickets/{id}
	"GetAdminUser":                 true, // GET /api/v1/admin/users/{id}
	"ListAdminAuditLog":            true, // GET /api/v1/admin/audit
	"ListAdminCoupons":             true, // GET /api/v1/admin/coupons
	"ListAdminDomains":             true, // GET /api/v1/admin/domains
	"ListAdminInvites":             true, // GET /api/v1/admin/invites
	"ListAdminMailLogs":            true, // GET /api/v1/admin/mail/logs
	"ListAdminMailTemplates":       true, // GET /api/v1/admin/mail/templates
	"ListAdminNodeKeys":            true, // GET /api/v1/admin/nodes/{id}/keys
	"ListAdminNodes":               true, // GET /api/v1/admin/nodes
	"ListAdminNotices":             true, // GET /api/v1/admin/notices
	"ListAdminOrders":              true, // GET /api/v1/admin/orders
	"ListAdminPayments":            true, // GET /api/v1/admin/payments
	"ListAdminPlans":               true, // GET /api/v1/admin/plans
	"ListAdminTickets":             true, // GET /api/v1/admin/tickets
	"ListAdminUnderpaidPayments":   true, // GET /api/v1/admin/payments/underpaid
	"ListAdminUsers":               true, // GET /api/v1/admin/users
	"ListAdmins":                   true, // GET /api/v1/admin/admins
	"MarkAdminOrderPaid":           true, // POST /api/v1/admin/orders/{trade_no}/mark-paid
	"RefundAdminOrder":             true, // POST /api/v1/admin/orders/{trade_no}/refund
	"ResetAdminTotp":               true, // POST /api/v1/admin/admins/{id}/reset-totp
	"RevokeAdminNodeKey":           true, // DELETE /api/v1/admin/node-keys/{key_id}
	"RevokeAdminUserSubscriptions": true, // POST /api/v1/admin/users/{id}/revoke-subs
	"UnbanAdminUser":               true, // POST /api/v1/admin/users/{id}/unban
	"UpdateAdminCoupon":            true, // PATCH /api/v1/admin/coupons/{id}
	"UpdateAdminMailTemplate":      true, // PATCH /api/v1/admin/mail/templates/{id}
	"UpdateAdminNode":              true, // PATCH /api/v1/admin/nodes/{id}
	"UpdateAdminNotice":            true, // PATCH /api/v1/admin/notices/{id}
	"UpdateAdminPayment":           true, // PATCH /api/v1/admin/payments/{id}
	"UpdateAdminPlan":              true, // PATCH /api/v1/admin/plans/{id}
	"UpdateAdminSettings":          true, // PATCH /api/v1/admin/settings
	"UpdateAdminTicket":            true, // PATCH /api/v1/admin/tickets/{id}
	"UpdateAdminUser":              true, // PATCH /api/v1/admin/users/{id}
}

// internalTaskOperations 是 Cloud Scheduler / Cloud Tasks 调用的内部任务端点
// （internalOidc：Google 签发的 OIDC ID token，见 mw.AuthenticateInternal）。
//
// 这批端点比管理面更危险 —— 它们没有人类界面，路径也不出现在前端代码里，
// 而**保护它们的是 OIDC 校验，不是路径保密**：/internal/tasks/* 与公网端点
// 跑在同一个 Cloud Run service 上，一个无鉴权的 POST /internal/tasks/traffic-reset
// 可以被任何人用来清空全站流量计数。
//
// 共 9 个。
var internalTaskOperations = map[string]bool{
	"RunAliveGcTask":      true, // POST /internal/tasks/alive-gc
	"RunChainScanTask":    true, // POST /internal/tasks/chain-scan
	"RunExpireCheckTask":  true, // POST /internal/tasks/expire-check
	"RunMailSendTask":     true, // POST /internal/tasks/mail-send
	"RunOrderTimeoutTask": true, // POST /internal/tasks/order-timeout
	"RunRemindSweepTask":  true, // POST /internal/tasks/remind-sweep
	"RunStatRollupTask":   true, // POST /internal/tasks/stat-rollup
	"RunTrafficBatchTask": true, // POST /internal/tasks/traffic-batch
	"RunTrafficResetTask": true, // POST /internal/tasks/traffic-reset
}

// authMiddleware 按 operationID 分派凭据校验。
//
// 分派而不是「每套面各挂一条 chi 子路由」的理由见 buildRouter 的注释。
// 五个分支覆盖全部 128 个 operation，default 分支**不可达** ——
// 但它仍然存在且 fail-closed：新增 operation 而忘了分类时，
// 运行时会 501 而不是无鉴权放行（测试会先一步在 CI 里报错）。
//
// 🔴 四套凭据配置刻意作为**四个独立参数**传进来，而不是打包成一个结构体。
// ADR 0006 §10.3 第 1 条把「一个全局 auth 中间件 + 身份类型 if 分支」列为禁止事项：
// 打包之后，「把 adminCfg 的 DB 塞给节点分支」这类改动在编译期看不出任何异常。
// 分开传意味着每个分支只能拿到它那一套。
func authMiddleware(
	nodeCfg mw.NodeAuthConfig,
	userCfg mw.UserAuthConfig,
	adminCfg mw.AdminAuthConfig,
	internalCfg mw.InternalAuthConfig,
) gen.StrictMiddlewareFunc {
	return func(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
		switch {
		case handler.PublicOperations[operationID]:
			// 免登录：订阅 token / 邮箱验证码 / 网关签名各自在 handler 里校验自己的凭据。
			return f

		case nodeOperationScopes[operationID] != "":
			scope := nodeOperationScopes[operationID]
			return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
				auth, authErr := mw.AuthenticateNode(ctx, nodeCfg, r, scope)
				if authErr != nil {
					mw.WriteAuthError(w, r, authErr)
					// 返回 nil,nil：响应已经写完，生成代码不应再写一次。
					return nil, nil
				}
				return f(mw.WithNodeAuth(ctx, auth), w, r, request)
			}

		case userSessionOperations[operationID]:
			return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
				auth, authErr := mw.AuthenticateUser(ctx, userCfg, r)
				if authErr != nil {
					mw.WriteAuthError(w, r, authErr)
					return nil, nil
				}
				return f(mw.WithUser(ctx, auth), w, r, request)
			}

		case adminOperations[operationID]:
			// 🔴 **两道闸的语义在这里彻底分开了。**
			//
			// 这个分支从前是「鉴权未实现，一律 501」——
			// 一条把「没凭据」与「handler 没写」压成同一个响应的捷径。
			// 现在两件事各归各：
			//
			//	鉴权（这里）      → 凭据不对就 403，请求根本进不了 handler；
			//	实现（Unimplemented）→ 凭据对了但 handler 还没写，仍然落到 501。
			//
			// 所以「61 个 admin 端点大多还没实现」这件事**不再**是它们的防线；
			// 防线是 mw.AuthenticateAdmin。反过来说也成立：从今天起，
			// 实现某个 admin handler 不再等于上线一个无鉴权端点 ——
			// 那正是这次拆分要买下的东西。
			//
			// 未配置 BP_ADMIN_IAP_AUDIENCE 时 AuthenticateAdmin 整体拒绝（fail-closed，
			// 见 admin.go），所以「配置漏了」的现象是「管理面进不去」而不是「谁都进得去」。
			return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
				auth, authErr := mw.AuthenticateAdmin(ctx, adminCfg, r)
				if authErr != nil {
					mw.WriteAuthError(w, r, authErr)
					// 返回 nil,nil：响应已经写完，生成代码不应再写一次。
					return nil, nil
				}
				return f(mw.WithAdmin(ctx, auth), w, r, request)
			}

		case internalTaskOperations[operationID]:
			// 与管理面同一次拆分（见上）。凭据是 Google 签发的 OIDC ID token。
			//
			// 这条链**不与另外三套共用任何代码路径**，配置也各自独立：
			// 内部面的 aud 是 Cloud Run 服务默认 URL，管理面的 aud 是 IAP 后端服务资源路径，
			// 两者形态都不一样，混用只会永远匹配不上。
			return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
				caller, authErr := mw.AuthenticateInternal(ctx, internalCfg, r)
				if authErr != nil {
					mw.WriteAuthError(w, r, authErr)
					return nil, nil
				}
				return f(mw.WithInternalCaller(ctx, caller), w, r, request)
			}

		default:
			return func(context.Context, http.ResponseWriter, *http.Request, any) (any, error) {
				return nil, handler.ErrNotImplemented
			}
		}
	}
}
