// Package handler 实现 OpenAPI 生成的 StrictServerInterface。
//
// 本文件由 `make gen-stubs` 从 internal/gen/api.gen.go 自动生成，**不要手改**。
//
// 设计：Unimplemented 提供全部 operation 的默认实现（一律返回 ErrNotImplemented）。
// 真实实现放在同包的其他文件里，通过 Server 结构体嵌入 Unimplemented 并覆盖对应方法。
//
// 这样做的好处：改 openapi.yaml 新增 operation 后重新生成本文件，Server 依然能编译
// 通过（新 operation 自动落到 501），而不是整个包编译失败。
// 代价是**漏实现不会在编译期暴露** —— 靠 operations.txt 与集成测试兜底。
package handler

import (
	"context"
	"errors"

	"github.com/oratis/babelplus/api/internal/gen"
)

// ErrNotImplemented 由尚未实现的 operation 返回，错误映射会把它转成 501。
var ErrNotImplemented = errors.New("not implemented")

// Unimplemented 是 StrictServerInterface 的全量默认实现。
type Unimplemented struct{}

var _ gen.StrictServerInterface = (*Unimplemented)(nil)

// GetHealthz 尚未实现。
func (Unimplemented) GetHealthz(_ context.Context, _ gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdmins 尚未实现。
func (Unimplemented) ListAdmins(_ context.Context, _ gen.ListAdminsRequestObject) (gen.ListAdminsResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateAdmin 尚未实现。
func (Unimplemented) CreateAdmin(_ context.Context, _ gen.CreateAdminRequestObject) (gen.CreateAdminResponseObject, error) {
	return nil, ErrNotImplemented
}

// DeleteAdmin 尚未实现。
func (Unimplemented) DeleteAdmin(_ context.Context, _ gen.DeleteAdminRequestObject) (gen.DeleteAdminResponseObject, error) {
	return nil, ErrNotImplemented
}

// ResetAdminTotp 尚未实现。
func (Unimplemented) ResetAdminTotp(_ context.Context, _ gen.ResetAdminTotpRequestObject) (gen.ResetAdminTotpResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminAuditLog 尚未实现。
func (Unimplemented) ListAdminAuditLog(_ context.Context, _ gen.ListAdminAuditLogRequestObject) (gen.ListAdminAuditLogResponseObject, error) {
	return nil, ErrNotImplemented
}

// AdjustAdminCommission 尚未实现。
func (Unimplemented) AdjustAdminCommission(_ context.Context, _ gen.AdjustAdminCommissionRequestObject) (gen.AdjustAdminCommissionResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminCoupons 尚未实现。
func (Unimplemented) ListAdminCoupons(_ context.Context, _ gen.ListAdminCouponsRequestObject) (gen.ListAdminCouponsResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateAdminCoupon 尚未实现。
func (Unimplemented) CreateAdminCoupon(_ context.Context, _ gen.CreateAdminCouponRequestObject) (gen.CreateAdminCouponResponseObject, error) {
	return nil, ErrNotImplemented
}

// DeleteAdminCoupon 尚未实现。
func (Unimplemented) DeleteAdminCoupon(_ context.Context, _ gen.DeleteAdminCouponRequestObject) (gen.DeleteAdminCouponResponseObject, error) {
	return nil, ErrNotImplemented
}

// UpdateAdminCoupon 尚未实现。
func (Unimplemented) UpdateAdminCoupon(_ context.Context, _ gen.UpdateAdminCouponRequestObject) (gen.UpdateAdminCouponResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetAdminDashboard 尚未实现。
func (Unimplemented) GetAdminDashboard(_ context.Context, _ gen.GetAdminDashboardRequestObject) (gen.GetAdminDashboardResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminDomains 尚未实现。
func (Unimplemented) ListAdminDomains(_ context.Context, _ gen.ListAdminDomainsRequestObject) (gen.ListAdminDomainsResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateAdminDomain 尚未实现。
func (Unimplemented) CreateAdminDomain(_ context.Context, _ gen.CreateAdminDomainRequestObject) (gen.CreateAdminDomainResponseObject, error) {
	return nil, ErrNotImplemented
}

// DeleteAdminDomain 尚未实现。
func (Unimplemented) DeleteAdminDomain(_ context.Context, _ gen.DeleteAdminDomainRequestObject) (gen.DeleteAdminDomainResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminInvites 尚未实现。
func (Unimplemented) ListAdminInvites(_ context.Context, _ gen.ListAdminInvitesRequestObject) (gen.ListAdminInvitesResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateAdminInvite 尚未实现。
func (Unimplemented) CreateAdminInvite(_ context.Context, _ gen.CreateAdminInviteRequestObject) (gen.CreateAdminInviteResponseObject, error) {
	return nil, ErrNotImplemented
}

// BroadcastAdminMail 尚未实现。
func (Unimplemented) BroadcastAdminMail(_ context.Context, _ gen.BroadcastAdminMailRequestObject) (gen.BroadcastAdminMailResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminMailLogs 尚未实现。
func (Unimplemented) ListAdminMailLogs(_ context.Context, _ gen.ListAdminMailLogsRequestObject) (gen.ListAdminMailLogsResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminMailTemplates 尚未实现。
func (Unimplemented) ListAdminMailTemplates(_ context.Context, _ gen.ListAdminMailTemplatesRequestObject) (gen.ListAdminMailTemplatesResponseObject, error) {
	return nil, ErrNotImplemented
}

// UpdateAdminMailTemplate 尚未实现。
func (Unimplemented) UpdateAdminMailTemplate(_ context.Context, _ gen.UpdateAdminMailTemplateRequestObject) (gen.UpdateAdminMailTemplateResponseObject, error) {
	return nil, ErrNotImplemented
}

// RevokeAdminNodeKey 尚未实现。
func (Unimplemented) RevokeAdminNodeKey(_ context.Context, _ gen.RevokeAdminNodeKeyRequestObject) (gen.RevokeAdminNodeKeyResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminNodes 尚未实现。
func (Unimplemented) ListAdminNodes(_ context.Context, _ gen.ListAdminNodesRequestObject) (gen.ListAdminNodesResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateAdminNode 尚未实现。
func (Unimplemented) CreateAdminNode(_ context.Context, _ gen.CreateAdminNodeRequestObject) (gen.CreateAdminNodeResponseObject, error) {
	return nil, ErrNotImplemented
}

// DeleteAdminNode 尚未实现。
func (Unimplemented) DeleteAdminNode(_ context.Context, _ gen.DeleteAdminNodeRequestObject) (gen.DeleteAdminNodeResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetAdminNode 尚未实现。
func (Unimplemented) GetAdminNode(_ context.Context, _ gen.GetAdminNodeRequestObject) (gen.GetAdminNodeResponseObject, error) {
	return nil, ErrNotImplemented
}

// UpdateAdminNode 尚未实现。
func (Unimplemented) UpdateAdminNode(_ context.Context, _ gen.UpdateAdminNodeRequestObject) (gen.UpdateAdminNodeResponseObject, error) {
	return nil, ErrNotImplemented
}

// DisableAdminNode 尚未实现。
func (Unimplemented) DisableAdminNode(_ context.Context, _ gen.DisableAdminNodeRequestObject) (gen.DisableAdminNodeResponseObject, error) {
	return nil, ErrNotImplemented
}

// EnableAdminNode 尚未实现。
func (Unimplemented) EnableAdminNode(_ context.Context, _ gen.EnableAdminNodeRequestObject) (gen.EnableAdminNodeResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminNodeKeys 尚未实现。
func (Unimplemented) ListAdminNodeKeys(_ context.Context, _ gen.ListAdminNodeKeysRequestObject) (gen.ListAdminNodeKeysResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateAdminNodeKey 尚未实现。
func (Unimplemented) CreateAdminNodeKey(_ context.Context, _ gen.CreateAdminNodeKeyRequestObject) (gen.CreateAdminNodeKeyResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminNotices 尚未实现。
func (Unimplemented) ListAdminNotices(_ context.Context, _ gen.ListAdminNoticesRequestObject) (gen.ListAdminNoticesResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateAdminNotice 尚未实现。
func (Unimplemented) CreateAdminNotice(_ context.Context, _ gen.CreateAdminNoticeRequestObject) (gen.CreateAdminNoticeResponseObject, error) {
	return nil, ErrNotImplemented
}

// DeleteAdminNotice 尚未实现。
func (Unimplemented) DeleteAdminNotice(_ context.Context, _ gen.DeleteAdminNoticeRequestObject) (gen.DeleteAdminNoticeResponseObject, error) {
	return nil, ErrNotImplemented
}

// UpdateAdminNotice 尚未实现。
func (Unimplemented) UpdateAdminNotice(_ context.Context, _ gen.UpdateAdminNoticeRequestObject) (gen.UpdateAdminNoticeResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminOrders 尚未实现。
func (Unimplemented) ListAdminOrders(_ context.Context, _ gen.ListAdminOrdersRequestObject) (gen.ListAdminOrdersResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetAdminOrder 尚未实现。
func (Unimplemented) GetAdminOrder(_ context.Context, _ gen.GetAdminOrderRequestObject) (gen.GetAdminOrderResponseObject, error) {
	return nil, ErrNotImplemented
}

// MarkAdminOrderPaid 尚未实现。
func (Unimplemented) MarkAdminOrderPaid(_ context.Context, _ gen.MarkAdminOrderPaidRequestObject) (gen.MarkAdminOrderPaidResponseObject, error) {
	return nil, ErrNotImplemented
}

// RefundAdminOrder 尚未实现。
func (Unimplemented) RefundAdminOrder(_ context.Context, _ gen.RefundAdminOrderRequestObject) (gen.RefundAdminOrderResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminPayments 尚未实现。
func (Unimplemented) ListAdminPayments(_ context.Context, _ gen.ListAdminPaymentsRequestObject) (gen.ListAdminPaymentsResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminUnderpaidPayments 尚未实现。
func (Unimplemented) ListAdminUnderpaidPayments(_ context.Context, _ gen.ListAdminUnderpaidPaymentsRequestObject) (gen.ListAdminUnderpaidPaymentsResponseObject, error) {
	return nil, ErrNotImplemented
}

// UpdateAdminPayment 尚未实现。
func (Unimplemented) UpdateAdminPayment(_ context.Context, _ gen.UpdateAdminPaymentRequestObject) (gen.UpdateAdminPaymentResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminPlans 尚未实现。
func (Unimplemented) ListAdminPlans(_ context.Context, _ gen.ListAdminPlansRequestObject) (gen.ListAdminPlansResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateAdminPlan 尚未实现。
func (Unimplemented) CreateAdminPlan(_ context.Context, _ gen.CreateAdminPlanRequestObject) (gen.CreateAdminPlanResponseObject, error) {
	return nil, ErrNotImplemented
}

// DeleteAdminPlan 尚未实现。
func (Unimplemented) DeleteAdminPlan(_ context.Context, _ gen.DeleteAdminPlanRequestObject) (gen.DeleteAdminPlanResponseObject, error) {
	return nil, ErrNotImplemented
}

// UpdateAdminPlan 尚未实现。
func (Unimplemented) UpdateAdminPlan(_ context.Context, _ gen.UpdateAdminPlanRequestObject) (gen.UpdateAdminPlanResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetAdminSettings 尚未实现。
func (Unimplemented) GetAdminSettings(_ context.Context, _ gen.GetAdminSettingsRequestObject) (gen.GetAdminSettingsResponseObject, error) {
	return nil, ErrNotImplemented
}

// UpdateAdminSettings 尚未实现。
func (Unimplemented) UpdateAdminSettings(_ context.Context, _ gen.UpdateAdminSettingsRequestObject) (gen.UpdateAdminSettingsResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetAdminStats 尚未实现。
func (Unimplemented) GetAdminStats(_ context.Context, _ gen.GetAdminStatsRequestObject) (gen.GetAdminStatsResponseObject, error) {
	return nil, ErrNotImplemented
}

// ExportAdminStats 尚未实现。
func (Unimplemented) ExportAdminStats(_ context.Context, _ gen.ExportAdminStatsRequestObject) (gen.ExportAdminStatsResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminTickets 尚未实现。
func (Unimplemented) ListAdminTickets(_ context.Context, _ gen.ListAdminTicketsRequestObject) (gen.ListAdminTicketsResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetAdminTicket 尚未实现。
func (Unimplemented) GetAdminTicket(_ context.Context, _ gen.GetAdminTicketRequestObject) (gen.GetAdminTicketResponseObject, error) {
	return nil, ErrNotImplemented
}

// UpdateAdminTicket 尚未实现。
func (Unimplemented) UpdateAdminTicket(_ context.Context, _ gen.UpdateAdminTicketRequestObject) (gen.UpdateAdminTicketResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateAdminTicketMessage 尚未实现。
func (Unimplemented) CreateAdminTicketMessage(_ context.Context, _ gen.CreateAdminTicketMessageRequestObject) (gen.CreateAdminTicketMessageResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListAdminUsers 尚未实现。
func (Unimplemented) ListAdminUsers(_ context.Context, _ gen.ListAdminUsersRequestObject) (gen.ListAdminUsersResponseObject, error) {
	return nil, ErrNotImplemented
}

// ExportAdminUsers 尚未实现。
func (Unimplemented) ExportAdminUsers(_ context.Context, _ gen.ExportAdminUsersRequestObject) (gen.ExportAdminUsersResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetAdminUser 尚未实现。
func (Unimplemented) GetAdminUser(_ context.Context, _ gen.GetAdminUserRequestObject) (gen.GetAdminUserResponseObject, error) {
	return nil, ErrNotImplemented
}

// UpdateAdminUser 尚未实现。
func (Unimplemented) UpdateAdminUser(_ context.Context, _ gen.UpdateAdminUserRequestObject) (gen.UpdateAdminUserResponseObject, error) {
	return nil, ErrNotImplemented
}

// AdjustAdminUserBalance 尚未实现。
func (Unimplemented) AdjustAdminUserBalance(_ context.Context, _ gen.AdjustAdminUserBalanceRequestObject) (gen.AdjustAdminUserBalanceResponseObject, error) {
	return nil, ErrNotImplemented
}

// BanAdminUser 尚未实现。
func (Unimplemented) BanAdminUser(_ context.Context, _ gen.BanAdminUserRequestObject) (gen.BanAdminUserResponseObject, error) {
	return nil, ErrNotImplemented
}

// RevokeAdminUserSubscriptions 尚未实现。
func (Unimplemented) RevokeAdminUserSubscriptions(_ context.Context, _ gen.RevokeAdminUserSubscriptionsRequestObject) (gen.RevokeAdminUserSubscriptionsResponseObject, error) {
	return nil, ErrNotImplemented
}

// UnbanAdminUser 尚未实现。
func (Unimplemented) UnbanAdminUser(_ context.Context, _ gen.UnbanAdminUserRequestObject) (gen.UnbanAdminUserResponseObject, error) {
	return nil, ErrNotImplemented
}

// SendEmailCode 尚未实现。
func (Unimplemented) SendEmailCode(_ context.Context, _ gen.SendEmailCodeRequestObject) (gen.SendEmailCodeResponseObject, error) {
	return nil, ErrNotImplemented
}

// Login 尚未实现。
func (Unimplemented) Login(_ context.Context, _ gen.LoginRequestObject) (gen.LoginResponseObject, error) {
	return nil, ErrNotImplemented
}

// Logout 尚未实现。
func (Unimplemented) Logout(_ context.Context, _ gen.LogoutRequestObject) (gen.LogoutResponseObject, error) {
	return nil, ErrNotImplemented
}

// ForgotPassword 尚未实现。
func (Unimplemented) ForgotPassword(_ context.Context, _ gen.ForgotPasswordRequestObject) (gen.ForgotPasswordResponseObject, error) {
	return nil, ErrNotImplemented
}

// ResetPassword 尚未实现。
func (Unimplemented) ResetPassword(_ context.Context, _ gen.ResetPasswordRequestObject) (gen.ResetPasswordResponseObject, error) {
	return nil, ErrNotImplemented
}

// RefreshToken 尚未实现。
func (Unimplemented) RefreshToken(_ context.Context, _ gen.RefreshTokenRequestObject) (gen.RefreshTokenResponseObject, error) {
	return nil, ErrNotImplemented
}

// RegisterAccount 尚未实现。
func (Unimplemented) RegisterAccount(_ context.Context, _ gen.RegisterAccountRequestObject) (gen.RegisterAccountResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetClientSubscription 尚未实现。
func (Unimplemented) GetClientSubscription(_ context.Context, _ gen.GetClientSubscriptionRequestObject) (gen.GetClientSubscriptionResponseObject, error) {
	return nil, ErrNotImplemented
}

// VerifyCoupon 尚未实现。
func (Unimplemented) VerifyCoupon(_ context.Context, _ gen.VerifyCouponRequestObject) (gen.VerifyCouponResponseObject, error) {
	return nil, ErrNotImplemented
}

// VerifyInviteCode 尚未实现。
func (Unimplemented) VerifyInviteCode(_ context.Context, _ gen.VerifyInviteCodeRequestObject) (gen.VerifyInviteCodeResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListNotices 尚未实现。
func (Unimplemented) ListNotices(_ context.Context, _ gen.ListNoticesRequestObject) (gen.ListNoticesResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListOrders 尚未实现。
func (Unimplemented) ListOrders(_ context.Context, _ gen.ListOrdersRequestObject) (gen.ListOrdersResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateOrder 尚未实现。
func (Unimplemented) CreateOrder(_ context.Context, _ gen.CreateOrderRequestObject) (gen.CreateOrderResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetOrder 尚未实现。
func (Unimplemented) GetOrder(_ context.Context, _ gen.GetOrderRequestObject) (gen.GetOrderResponseObject, error) {
	return nil, ErrNotImplemented
}

// CancelOrder 尚未实现。
func (Unimplemented) CancelOrder(_ context.Context, _ gen.CancelOrderRequestObject) (gen.CancelOrderResponseObject, error) {
	return nil, ErrNotImplemented
}

// PayOrder 尚未实现。
func (Unimplemented) PayOrder(_ context.Context, _ gen.PayOrderRequestObject) (gen.PayOrderResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetOrderPayment 尚未实现。
func (Unimplemented) GetOrderPayment(_ context.Context, _ gen.GetOrderPaymentRequestObject) (gen.GetOrderPaymentResponseObject, error) {
	return nil, ErrNotImplemented
}

// RecheckOrderPayment 尚未实现。
func (Unimplemented) RecheckOrderPayment(_ context.Context, _ gen.RecheckOrderPaymentRequestObject) (gen.RecheckOrderPaymentResponseObject, error) {
	return nil, ErrNotImplemented
}

// HandlePaymentNotify 尚未实现。
func (Unimplemented) HandlePaymentNotify(_ context.Context, _ gen.HandlePaymentNotifyRequestObject) (gen.HandlePaymentNotifyResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListPlans 尚未实现。
func (Unimplemented) ListPlans(_ context.Context, _ gen.ListPlansRequestObject) (gen.ListPlansResponseObject, error) {
	return nil, ErrNotImplemented
}

// PushUniProxyAlive 尚未实现。
func (Unimplemented) PushUniProxyAlive(_ context.Context, _ gen.PushUniProxyAliveRequestObject) (gen.PushUniProxyAliveResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetUniProxyAliveList 尚未实现。
func (Unimplemented) GetUniProxyAliveList(_ context.Context, _ gen.GetUniProxyAliveListRequestObject) (gen.GetUniProxyAliveListResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetUniProxyConfig 尚未实现。
func (Unimplemented) GetUniProxyConfig(_ context.Context, _ gen.GetUniProxyConfigRequestObject) (gen.GetUniProxyConfigResponseObject, error) {
	return nil, ErrNotImplemented
}

// PushUniProxyTraffic 尚未实现。
func (Unimplemented) PushUniProxyTraffic(_ context.Context, _ gen.PushUniProxyTrafficRequestObject) (gen.PushUniProxyTrafficResponseObject, error) {
	return nil, ErrNotImplemented
}

// PushUniProxyStatus 尚未实现。
func (Unimplemented) PushUniProxyStatus(_ context.Context, _ gen.PushUniProxyStatusRequestObject) (gen.PushUniProxyStatusResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetUniProxyUsers 尚未实现。
func (Unimplemented) GetUniProxyUsers(_ context.Context, _ gen.GetUniProxyUsersRequestObject) (gen.GetUniProxyUsersResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListTickets 尚未实现。
func (Unimplemented) ListTickets(_ context.Context, _ gen.ListTicketsRequestObject) (gen.ListTicketsResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateTicket 尚未实现。
func (Unimplemented) CreateTicket(_ context.Context, _ gen.CreateTicketRequestObject) (gen.CreateTicketResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetTicket 尚未实现。
func (Unimplemented) GetTicket(_ context.Context, _ gen.GetTicketRequestObject) (gen.GetTicketResponseObject, error) {
	return nil, ErrNotImplemented
}

// CloseTicket 尚未实现。
func (Unimplemented) CloseTicket(_ context.Context, _ gen.CloseTicketRequestObject) (gen.CloseTicketResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateTicketMessage 尚未实现。
func (Unimplemented) CreateTicketMessage(_ context.Context, _ gen.CreateTicketMessageRequestObject) (gen.CreateTicketMessageResponseObject, error) {
	return nil, ErrNotImplemented
}

// DisableUserTotp 尚未实现。
func (Unimplemented) DisableUserTotp(_ context.Context, _ gen.DisableUserTotpRequestObject) (gen.DisableUserTotpResponseObject, error) {
	return nil, ErrNotImplemented
}

// EnrollUserTotp 尚未实现。
func (Unimplemented) EnrollUserTotp(_ context.Context, _ gen.EnrollUserTotpRequestObject) (gen.EnrollUserTotpResponseObject, error) {
	return nil, ErrNotImplemented
}

// VerifyUserTotp 尚未实现。
func (Unimplemented) VerifyUserTotp(_ context.Context, _ gen.VerifyUserTotpRequestObject) (gen.VerifyUserTotpResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListCommissions 尚未实现。
func (Unimplemented) ListCommissions(_ context.Context, _ gen.ListCommissionsRequestObject) (gen.ListCommissionsResponseObject, error) {
	return nil, ErrNotImplemented
}

// TransferCommission 尚未实现。
func (Unimplemented) TransferCommission(_ context.Context, _ gen.TransferCommissionRequestObject) (gen.TransferCommissionResponseObject, error) {
	return nil, ErrNotImplemented
}

// KickAllUserDevices 尚未实现。
func (Unimplemented) KickAllUserDevices(_ context.Context, _ gen.KickAllUserDevicesRequestObject) (gen.KickAllUserDevicesResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListUserDevices 尚未实现。
func (Unimplemented) ListUserDevices(_ context.Context, _ gen.ListUserDevicesRequestObject) (gen.ListUserDevicesResponseObject, error) {
	return nil, ErrNotImplemented
}

// KickUserDevice 尚未实现。
func (Unimplemented) KickUserDevice(_ context.Context, _ gen.KickUserDeviceRequestObject) (gen.KickUserDeviceResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetUserDiagnose 尚未实现。
func (Unimplemented) GetUserDiagnose(_ context.Context, _ gen.GetUserDiagnoseRequestObject) (gen.GetUserDiagnoseResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListInviteCodes 尚未实现。
func (Unimplemented) ListInviteCodes(_ context.Context, _ gen.ListInviteCodesRequestObject) (gen.ListInviteCodesResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateInviteCode 尚未实现。
func (Unimplemented) CreateInviteCode(_ context.Context, _ gen.CreateInviteCodeRequestObject) (gen.CreateInviteCodeResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetCurrentUser 尚未实现。
func (Unimplemented) GetCurrentUser(_ context.Context, _ gen.GetCurrentUserRequestObject) (gen.GetCurrentUserResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListUserNodes 尚未实现。
func (Unimplemented) ListUserNodes(_ context.Context, _ gen.ListUserNodesRequestObject) (gen.ListUserNodesResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetNotificationPrefs 尚未实现。
func (Unimplemented) GetNotificationPrefs(_ context.Context, _ gen.GetNotificationPrefsRequestObject) (gen.GetNotificationPrefsResponseObject, error) {
	return nil, ErrNotImplemented
}

// UpdateNotificationPrefs 尚未实现。
func (Unimplemented) UpdateNotificationPrefs(_ context.Context, _ gen.UpdateNotificationPrefsRequestObject) (gen.UpdateNotificationPrefsResponseObject, error) {
	return nil, ErrNotImplemented
}

// ChangePassword 尚未实现。
func (Unimplemented) ChangePassword(_ context.Context, _ gen.ChangePasswordRequestObject) (gen.ChangePasswordResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetUserSubscription 尚未实现。
func (Unimplemented) GetUserSubscription(_ context.Context, _ gen.GetUserSubscriptionRequestObject) (gen.GetUserSubscriptionResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListSubscriptionFetchLog 尚未实现。
func (Unimplemented) ListSubscriptionFetchLog(_ context.Context, _ gen.ListSubscriptionFetchLogRequestObject) (gen.ListSubscriptionFetchLogResponseObject, error) {
	return nil, ErrNotImplemented
}

// RevokeAllSubscriptionTokens 尚未实现。
func (Unimplemented) RevokeAllSubscriptionTokens(_ context.Context, _ gen.RevokeAllSubscriptionTokensRequestObject) (gen.RevokeAllSubscriptionTokensResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListSubscriptionTokens 尚未实现。
func (Unimplemented) ListSubscriptionTokens(_ context.Context, _ gen.ListSubscriptionTokensRequestObject) (gen.ListSubscriptionTokensResponseObject, error) {
	return nil, ErrNotImplemented
}

// CreateSubscriptionToken 尚未实现。
func (Unimplemented) CreateSubscriptionToken(_ context.Context, _ gen.CreateSubscriptionTokenRequestObject) (gen.CreateSubscriptionTokenResponseObject, error) {
	return nil, ErrNotImplemented
}

// RevokeSubscriptionToken 尚未实现。
func (Unimplemented) RevokeSubscriptionToken(_ context.Context, _ gen.RevokeSubscriptionTokenRequestObject) (gen.RevokeSubscriptionTokenResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetUserUsage 尚未实现。
func (Unimplemented) GetUserUsage(_ context.Context, _ gen.GetUserUsageRequestObject) (gen.GetUserUsageResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetWallet 尚未实现。
func (Unimplemented) GetWallet(_ context.Context, _ gen.GetWalletRequestObject) (gen.GetWalletResponseObject, error) {
	return nil, ErrNotImplemented
}

// ListWalletTransactions 尚未实现。
func (Unimplemented) ListWalletTransactions(_ context.Context, _ gen.ListWalletTransactionsRequestObject) (gen.ListWalletTransactionsResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetNodeConfigV2 尚未实现。
func (Unimplemented) GetNodeConfigV2(_ context.Context, _ gen.GetNodeConfigV2RequestObject) (gen.GetNodeConfigV2ResponseObject, error) {
	return nil, ErrNotImplemented
}

// RunAliveGcTask 尚未实现。
func (Unimplemented) RunAliveGcTask(_ context.Context, _ gen.RunAliveGcTaskRequestObject) (gen.RunAliveGcTaskResponseObject, error) {
	return nil, ErrNotImplemented
}

// RunChainScanTask 尚未实现。
func (Unimplemented) RunChainScanTask(_ context.Context, _ gen.RunChainScanTaskRequestObject) (gen.RunChainScanTaskResponseObject, error) {
	return nil, ErrNotImplemented
}

// RunExpireCheckTask 尚未实现。
func (Unimplemented) RunExpireCheckTask(_ context.Context, _ gen.RunExpireCheckTaskRequestObject) (gen.RunExpireCheckTaskResponseObject, error) {
	return nil, ErrNotImplemented
}

// RunMailSendTask 尚未实现。
func (Unimplemented) RunMailSendTask(_ context.Context, _ gen.RunMailSendTaskRequestObject) (gen.RunMailSendTaskResponseObject, error) {
	return nil, ErrNotImplemented
}

// RunOrderTimeoutTask 尚未实现。
func (Unimplemented) RunOrderTimeoutTask(_ context.Context, _ gen.RunOrderTimeoutTaskRequestObject) (gen.RunOrderTimeoutTaskResponseObject, error) {
	return nil, ErrNotImplemented
}

// RunRemindSweepTask 尚未实现。
func (Unimplemented) RunRemindSweepTask(_ context.Context, _ gen.RunRemindSweepTaskRequestObject) (gen.RunRemindSweepTaskResponseObject, error) {
	return nil, ErrNotImplemented
}

// RunStatRollupTask 尚未实现。
func (Unimplemented) RunStatRollupTask(_ context.Context, _ gen.RunStatRollupTaskRequestObject) (gen.RunStatRollupTaskResponseObject, error) {
	return nil, ErrNotImplemented
}

// RunTrafficBatchTask 尚未实现。
func (Unimplemented) RunTrafficBatchTask(_ context.Context, _ gen.RunTrafficBatchTaskRequestObject) (gen.RunTrafficBatchTaskResponseObject, error) {
	return nil, ErrNotImplemented
}

// RunTrafficResetTask 尚未实现。
func (Unimplemented) RunTrafficResetTask(_ context.Context, _ gen.RunTrafficResetTaskRequestObject) (gen.RunTrafficResetTaskResponseObject, error) {
	return nil, ErrNotImplemented
}

// GetShortSubscription 尚未实现。
func (Unimplemented) GetShortSubscription(_ context.Context, _ gen.GetShortSubscriptionRequestObject) (gen.GetShortSubscriptionResponseObject, error) {
	return nil, ErrNotImplemented
}
