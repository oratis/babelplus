package handler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oratis/babelplus/api/internal/gen"
)

// ============================================================
// 通知偏好
// ============================================================

// 🔴 service_broadcast 是一个**只读为 true** 的开关，而且必须带上「为什么」。
//
// 一个没有理由的灰色开关会被理解成 bug 或者「他们不想让我关」，
// 而真实理由恰恰站在用户一边：ADR 0002 认定邮件广播是唯一的失联恢复通道 ——
// 域名被封之后，我们能够到用户的路只剩这一条。
func TestNotificationPrefsViewLocksServiceBroadcast(t *testing.T) {
	p := notificationPrefsView(false, false)

	if !p.ServiceBroadcast.Value {
		t.Error("service_broadcast.value = false —— 它在语义上恒为 true")
	}
	if !p.ServiceBroadcast.Locked {
		t.Error("service_broadcast.locked = false —— 前端会把它渲染成可点的开关")
	}
	if strings.TrimSpace(p.ServiceBroadcast.Reason) == "" {
		t.Fatal("service_broadcast.reason 为空：一个不给理由的灰色开关会被当成 bug")
	}
	// 文案必须解释后果，而不只是说「不可关闭」。
	if !strings.Contains(p.ServiceBroadcast.Reason, "域名") {
		t.Errorf("reason 没有说清楚它承载什么信息：%q", p.ServiceBroadcast.Reason)
	}

	// 另外两个开关必须原样反映数据库里的值。
	if p.ExpireRemind || p.TrafficRemind {
		t.Errorf("两个可写开关没有原样反映：%+v", p)
	}
	on := notificationPrefsView(true, true)
	if !on.ExpireRemind || !on.TrafficRemind {
		t.Errorf("两个可写开关没有原样反映：%+v", on)
	}
	// service_broadcast 不随另外两个变。
	if !on.ServiceBroadcast.Value || !on.ServiceBroadcast.Locked {
		t.Error("service_broadcast 受到了另外两个开关的影响")
	}
}

// NotificationPrefsUpdate 在**类型上**就没有 service_broadcast 字段 ——
// 这是契约「它必须在 API 层就不可写」那句话的真正落点。
// 这条测试守的是「将来有人给生成类型加回这个字段」。
func TestNotificationPrefsUpdateHasNoBroadcastField(t *testing.T) {
	var u gen.NotificationPrefsUpdate
	// 只有两个可写字段。任何人给 NotificationPrefsUpdate 加上第三个开关，
	// 这个复合字面量就会编译失败 —— 那正是我们要的信号。
	u = gen.NotificationPrefsUpdate{ExpireRemind: ptrOf(true), TrafficRemind: ptrOf(false)}
	if u.ExpireRemind == nil || !*u.ExpireRemind {
		t.Fatal("unreachable")
	}
}

func TestUpdateNotificationPrefsRejectsMissingBody(t *testing.T) {
	srv := &Server{logger: testLogger()}
	resp, err := srv.UpdateNotificationPrefs(withUser(1), gen.UpdateNotificationPrefsRequestObject{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if _, ok := resp.(gen.UpdateNotificationPrefs422JSONResponse); !ok {
		t.Fatalf("缺请求体应当 422，got %T", resp)
	}
}

func TestNotificationPrefsMissingSessionIsInternal(t *testing.T) {
	srv := &Server{logger: testLogger()}
	if _, err := srv.GetNotificationPrefs(context.Background(), gen.GetNotificationPrefsRequestObject{}); !errors.Is(err, errNoUserAuth) {
		t.Errorf("GetNotificationPrefs 缺身份时 err = %v", err)
	}
	if _, err := srv.UpdateNotificationPrefs(context.Background(), gen.UpdateNotificationPrefsRequestObject{}); !errors.Is(err, errNoUserAuth) {
		t.Errorf("UpdateNotificationPrefs 缺身份时 err = %v", err)
	}
}

// ============================================================
// 用户侧 TOTP：三个 operation 必须仍然是 501
// ============================================================

// 🔴 **这三条不是「等实现」的占位测试，它们锁住的是一个安全结论。**
//
// 当前 schema 下用户侧 TOTP 写不出来：users 表上没有 totp_secret_enc /
// totp_confirmed_at，而 used_totp 的主键第一列是**指向 admin_users 的外键**。
// 在补齐这些之前，任何「先让接口通起来」的实现都只有两种形态，两种都更坏：
//   - 假装成功（enroll 返一个 secret 但没地方存）→ 用户扫了码，下次登录进不去；
//   - disable 返 204 但什么都没做 → 用户以为关掉了 2FA，
//     可能已经把 authenticator 里的条目删了。
//
// 而 openapi 自己就写着「**P3，未实现。** 服务端返回 501 直到实现完成」，
// 所以 501 是**契约要求的当前行为**，不是欠账。
func TestUserTotpStaysUnimplemented(t *testing.T) {
	srv := &Server{logger: testLogger()}
	ctx := withUser(1)

	if _, err := srv.EnrollUserTotp(ctx, gen.EnrollUserTotpRequestObject{}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("enrollUserTotp err = %v, want ErrNotImplemented（→ 501）", err)
	}
	if _, err := srv.VerifyUserTotp(ctx, gen.VerifyUserTotpRequestObject{}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("verifyUserTotp err = %v, want ErrNotImplemented（→ 501）", err)
	}
	if _, err := srv.DisableUserTotp(ctx, gen.DisableUserTotpRequestObject{}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("disableUserTotp err = %v, want ErrNotImplemented（→ 501）", err)
	}
}

// disableUserTotp 尤其不能退化成 204：「解绑成功」而实际什么都没做，
// 会让用户在下次登录时被自己的 2FA 挡在门外。
func TestDisableUserTotpNeverReturnsSuccess(t *testing.T) {
	srv := &Server{logger: testLogger()}
	resp, err := srv.DisableUserTotp(withUser(1), gen.DisableUserTotpRequestObject{})
	if resp != nil {
		t.Fatalf("disableUserTotp 返回了一个响应对象 %T —— 它必须是 501，不能是 204", resp)
	}
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("err = %v", err)
	}
}

// 501 与 500 在监控上必须分开：脚手架阶段有一百多个端点未实现，
// 把它们都算成 500 会让 5xx 告警长期为红，真正的故障反而被淹没
// （main.go 的 responseErrorHandler 就是按这条建的）。
func TestNotImplementedIsDistinctFromInternal(t *testing.T) {
	if errors.Is(ErrNotImplemented, errNoUserAuth) || errors.Is(errNoUserAuth, ErrNotImplemented) {
		t.Fatal("「未实现」与「装配错误」共用了同一个哨兵 —— 501 会被算进 5xx 告警")
	}
}
