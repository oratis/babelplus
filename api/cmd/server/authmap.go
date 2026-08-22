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
// 🔴 管理面鉴权**尚未实现**，所以 authMiddleware 对这批 operation 直接返回
// ErrNotImplemented（501）—— 与它们当前 handler 的行为逐字节一致，
// 但把「放行」变成了「拒绝」。这是刻意的 fail-closed：
// 上一版这里是原样放行，于是任何人实现某个 admin handler 的那一刻，
// 就等于上线了一个无鉴权的管理端点，而代码 diff 里看不出任何异常。
//
// 实现管理面时：加一个 adminAuth 中间件，把下面 case 里的 501 换成它，
// **不要**只改 handler。
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
// （internalOidc：Google 签发的 OIDC ID token）。
//
// 与 adminOperations 同样处理：鉴权未实现 → 501 fail-closed。
// 这批端点比管理面更危险 —— 它们没有人类界面，路径也不出现在前端代码里，
// 一个无鉴权的 POST /internal/tasks/traffic-reset 可以被任何人用来清空全站流量计数。
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
func authMiddleware(nodeCfg mw.NodeAuthConfig, userCfg mw.UserAuthConfig) gen.StrictMiddlewareFunc {
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

		case adminOperations[operationID], internalTaskOperations[operationID]:
			// 见两张表的注释：鉴权未实现，一律 501。
			// 走 error 通道而不是直接写响应，是为了复用 responseErrorHandler 的
			// ErrNotImplemented → 501 映射 —— 监控的 5xx 告警规则正是按「排除 501」建的，
			// 这里若自己写一个 500 会让 70 个端点长期把告警刷红。
			return func(context.Context, http.ResponseWriter, *http.Request, any) (any, error) {
				return nil, handler.ErrNotImplemented
			}

		default:
			return func(context.Context, http.ResponseWriter, *http.Request, any) (any, error) {
				return nil, handler.ErrNotImplemented
			}
		}
	}
}
