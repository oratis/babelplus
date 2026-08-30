package handler

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base32"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/audit"
	"github.com/oratis/babelplus/api/internal/config"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
	"github.com/oratis/babelplus/api/internal/store"
)

// 管理面「用户与管理员」这一组的测试。
//
// 与 order_test.go / task_test.go 同一条纪律：测**纯函数**、**吃窄接口的事务体**，
// 以及那些在碰数据库之前就该返回的 handler 分支。
//
// 🔴 **本文件里一大半用例的断言机制是同一件事：`Server.db` 是 nil（或一个
//
//	Pool 为 nil 的空壳）。** 也就是说，只要被测的 handler 分支**多走一步**
//	去碰数据库，测试就会 panic 而不是失败 —— 于是「参数没收齐时不许提交」
//	这条要求不是靠断言表达的，是靠**物理上不存在可提交的对象**表达的。
//	这比断言「某个 mock 没有被调用」强：后者要求我先想到该断言哪一次调用。
//
// # 每一组用例为什么必须存在
//
//	四层强制（api-contract §6.2）——
//	  L1 TestConfirmationMatches* / Test*ConfirmationMismatchDoesNotWrite
//	     确认串比对必须在**服务端**、必须常数时间、期望值为空时必须不通过，
//	     且不匹配时**一次业务写入都不许发生**。前端的确认弹窗对 curl 是零。
//	  L2 TestValidAdminReason + Test*RejectsShortReason（八个危险操作逐个）
//	     reason < 8 字符时必须在开事务之前就返回 422。
//	  L3 TestAdminStepUp* + Test*WithoutTotpIsRefused（四个 L3 操作逐个）
//	     缺 TOTP 头 / 码形态不合法 / step-up 依赖没配 —— 三种都必须拒绝，
//	     且都在碰数据库之前。
//	  L4 TestAdminPermissionGrant* / TestAdjustBalanceWithoutPermission /
//	     TestExportWithoutPermission / Test*RequiresOwnerRole
//
//	审计（§6.3）——
//	  TestRevokeSubsRollsBackWhenAuditWriteFails 是本文件最重要的一条：
//	  业务已经写完、审计写失败时，Commit **一次都不能发生**。
//	  TestResetAdminTotpAuditNeverCarriesSecret：audit_logs 是 append-only
//	  永不删除的表，一份写进去的凭据是永久写进去的。
//
//	静默失效——
//	  TestEscapeLikePattern：一个 `%` = 返回全部用户；
//	  TestAdjustBalanceLegsSumToZero：符号弄反不会报错，只会让钱反向；
//	  TestBuildUsersCSVNullsAreEmptyCells：NULL 写成 0 会让「不限设备」变成「限 0 台」；
//	  TestAdminExportIsTruncated：`== cap` 与 `> cap` 差一个用户数就是一次误拒。

// ============================================================
// 本组 operation 必须真的落在 Server 上
// ============================================================

func TestAdminUserOperationsAreImplemented(t *testing.T) {
	var s any = &Server{}
	if _, ok := s.(interface {
		ListAdminUsers(context.Context, gen.ListAdminUsersRequestObject) (gen.ListAdminUsersResponseObject, error)
		GetAdminUser(context.Context, gen.GetAdminUserRequestObject) (gen.GetAdminUserResponseObject, error)
		UpdateAdminUser(context.Context, gen.UpdateAdminUserRequestObject) (gen.UpdateAdminUserResponseObject, error)
		BanAdminUser(context.Context, gen.BanAdminUserRequestObject) (gen.BanAdminUserResponseObject, error)
		UnbanAdminUser(context.Context, gen.UnbanAdminUserRequestObject) (gen.UnbanAdminUserResponseObject, error)
		RevokeAdminUserSubscriptions(context.Context, gen.RevokeAdminUserSubscriptionsRequestObject) (gen.RevokeAdminUserSubscriptionsResponseObject, error)
		AdjustAdminUserBalance(context.Context, gen.AdjustAdminUserBalanceRequestObject) (gen.AdjustAdminUserBalanceResponseObject, error)
		ExportAdminUsers(context.Context, gen.ExportAdminUsersRequestObject) (gen.ExportAdminUsersResponseObject, error)
		ListAdmins(context.Context, gen.ListAdminsRequestObject) (gen.ListAdminsResponseObject, error)
		CreateAdmin(context.Context, gen.CreateAdminRequestObject) (gen.CreateAdminResponseObject, error)
		DeleteAdmin(context.Context, gen.DeleteAdminRequestObject) (gen.DeleteAdminResponseObject, error)
		ResetAdminTotp(context.Context, gen.ResetAdminTotpRequestObject) (gen.ResetAdminTotpResponseObject, error)
	}); !ok {
		t.Fatal("管理面用户/管理员这一组里有 operation 没有被 Server 覆盖，仍落在 Unimplemented 的 501 上")
	}
}

// 这 12 个 operation 一个都不能是免登录的。漏一个的现象是**任何人都能列出全部用户**。
func TestAdminUserOperationsAreNotPublic(t *testing.T) {
	for _, name := range []string{
		"ListAdminUsers", "GetAdminUser", "UpdateAdminUser", "BanAdminUser", "UnbanAdminUser",
		"RevokeAdminUserSubscriptions", "AdjustAdminUserBalance", "ExportAdminUsers",
		"ListAdmins", "CreateAdmin", "DeleteAdmin", "ResetAdminTotp",
	} {
		if PublicOperations[name] {
			t.Errorf("%s 出现在免登录表里 —— 管理面端点一个都不能免登录", name)
		}
	}
}

// ============================================================
// 测试夹具
// ============================================================

func testAdminLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// adminTestServer 造一个**没有数据库**的 Server。
//
// 🔴 `db` 保持 nil 是刻意的：任何一个被测分支只要往数据库多走一步就会 panic，
// 于是「不许提交」这件事由 Go 的运行时来证明，不由我的断言来证明。
func adminTestServer() *Server {
	return &Server{cfg: &config.Config{}, logger: testAdminLogger()}
}

// adminStepUpServer 造一个能跑到 step-up 的 Server：db 是空壳（Pool 为 nil），
// TOTP 密钥长度合法。RequireStepUp 在「缺码 / 码形态不合法」两种情况下
// **不碰数据库**就返回，所以空壳足够；一旦它真去查库，Pool 为 nil 会 panic ——
// 那正是我们想要的信号。
func adminStepUpServer() *Server {
	return &Server{
		cfg:    &config.Config{AdminTOTPEncKey: make([]byte, 32)},
		db:     &store.Store{},
		logger: testAdminLogger(),
	}
}

// adminUserCtx 造一个带「管理员身份 + 原始请求」的上下文。
// 没有原始请求时 adminActor 会拒绝（审计缺 IP），所以两者必须一起给。
func adminUserCtx(role string, perms middleware.AdminPerms) context.Context {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/1/ban", nil)
	r.RemoteAddr = "203.0.113.9:41234"
	ctx := context.WithValue(context.Background(), ctxKeyBoundRequest{}, r)
	return middleware.WithAdmin(ctx, &middleware.AdminAuth{
		AdminID: 7,
		Email:   "ops@babel.plus",
		Role:    role,
		Perms:   perms,
	})
}

func testAdminActor() audit.Actor {
	return audit.Actor{
		AdminID: 7,
		Email:   "ops@babel.plus",
		IP:      netip.MustParseAddr("203.0.113.9"),
	}
}

const testTargetEmail = "user@example.com"

func admTS(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// ============================================================
// L2：必填原因
// ============================================================

func TestValidAdminReason(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"太短", "补单", false},
		{"七个 ASCII", "1234567", false},
		{"八个 ASCII 刚好", "12345678", true},
		{"八个汉字", "用户申诉已核实无误", true},
		{"全空白凑长度", "          ", false},
		{"空白包着的短原因", "  短  ", false},
		{"空串", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := validAdminReason(c.in)
			if ok != c.want {
				t.Fatalf("validAdminReason(%q) = %v，期望 %v", c.in, ok, c.want)
			}
			if strings.TrimSpace(got) != got {
				t.Errorf("归一化后仍带首尾空白：%q —— 审计里会按原因分组统计，带空白的会各成一类", got)
			}
		})
	}
}

// 🔴 数的必须是字符不是字节：按字节数会拒掉「用户申诉已核实」这种真的说清了事情的
// 中文原因（21 字节但只有 7 字），同时放过「aaaaaaaa」。方向正好反了。
func TestValidAdminReasonCountsRunesNotBytes(t *testing.T) {
	seven := "用户申诉已核实" // 7 个字 / 21 字节
	if _, ok := validAdminReason(seven); ok {
		t.Fatal("7 个汉字应当被拒（它有 21 字节，按字节数会误放行）")
	}
	eight := "用户申诉已核实过"
	if _, ok := validAdminReason(eight); !ok {
		t.Fatal("8 个汉字应当通过")
	}
}

// 八个危险/写操作逐个来一遍：reason 不合格时必须**在碰数据库之前**返回 422。
// Server.db 是 nil —— 多走一步就 panic。
func TestDangerousOpsRejectShortReasonBeforeTouchingDB(t *testing.T) {
	s := adminTestServer()
	ctx := context.Background()
	short := "太短"

	t.Run("D1 updateAdminUser", func(t *testing.T) {
		resp, err := s.UpdateAdminUser(ctx, gen.UpdateAdminUserRequestObject{
			Id: 1, Body: &gen.AdminUserPatch{Reason: short, DeviceLimit: ptrOf(int32(3))},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.UpdateAdminUser422JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 422", resp)
		}
	})
	t.Run("D2 ban", func(t *testing.T) {
		resp, err := s.BanAdminUser(ctx, gen.BanAdminUserRequestObject{
			Id: 1, Body: &gen.ReasonRequest{Reason: short},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.BanAdminUser422JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 422", resp)
		}
	})
	t.Run("D2 unban", func(t *testing.T) {
		resp, err := s.UnbanAdminUser(ctx, gen.UnbanAdminUserRequestObject{
			Id: 1, Body: &gen.ReasonRequest{Reason: short},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.UnbanAdminUser422JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 422", resp)
		}
	})
	t.Run("D3 revoke-subs", func(t *testing.T) {
		resp, err := s.RevokeAdminUserSubscriptions(ctx, gen.RevokeAdminUserSubscriptionsRequestObject{
			Id:     1,
			Params: gen.RevokeAdminUserSubscriptionsParams{XTOTPCode: "123456"},
			Body:   &gen.ConfirmedReasonRequest{Confirmation: testTargetEmail, Reason: short},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.RevokeAdminUserSubscriptions422JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 422", resp)
		}
	})
	t.Run("D10 balance-adjust", func(t *testing.T) {
		resp, err := s.AdjustAdminUserBalance(ctx, gen.AdjustAdminUserBalanceRequestObject{
			Id:     1,
			Params: gen.AdjustAdminUserBalanceParams{XTOTPCode: "123456"},
			Body:   &gen.BalanceAdjustRequest{Amount: 100, Confirmation: testTargetEmail, Reason: short},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.AdjustAdminUserBalance422JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 422", resp)
		}
	})
	t.Run("D14 export", func(t *testing.T) {
		resp, err := s.ExportAdminUsers(ctx, gen.ExportAdminUsersRequestObject{
			Body: &gen.ReasonRequest{Reason: short},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.ExportAdminUsers422JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 422", resp)
		}
	})
	t.Run("D15 createAdmin", func(t *testing.T) {
		resp, err := s.CreateAdmin(ctx, gen.CreateAdminRequestObject{
			Params: gen.CreateAdminParams{XTOTPCode: "123456"},
			Body:   &gen.AdminAccountCreateRequest{Email: "new@babel.plus", Reason: short},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.CreateAdmin422JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 422", resp)
		}
	})
	t.Run("D15 resetAdminTotp", func(t *testing.T) {
		resp, err := s.ResetAdminTotp(ctx, gen.ResetAdminTotpRequestObject{
			Id:     1,
			Params: gen.ResetAdminTotpParams{XTOTPCode: "123456"},
			Body:   &gen.ConfirmedReasonRequest{Confirmation: "a@b.c", Reason: short},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.ResetAdminTotp422JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 422", resp)
		}
	})
	t.Run("D16 deleteAdmin", func(t *testing.T) {
		resp, err := s.DeleteAdmin(ctx, gen.DeleteAdminRequestObject{
			Id:     1,
			Params: gen.DeleteAdminParams{XTOTPCode: "123456"},
			Body:   &gen.ConfirmedReasonRequest{Confirmation: "a@b.c", Reason: short},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.DeleteAdmin422JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 422", resp)
		}
	})
}

// D10 的 amount = 0 同样必须在开事务之前挡住：
// 一条金额为 0 的分录 + 一条 D10 审计，在事后与「有人试图动钱但失败了」不可区分。
func TestAdjustBalanceRejectsZeroAmount(t *testing.T) {
	s := adminTestServer()
	resp, err := s.AdjustAdminUserBalance(context.Background(), gen.AdjustAdminUserBalanceRequestObject{
		Id:     1,
		Params: gen.AdjustAdminUserBalanceParams{XTOTPCode: "123456"},
		Body: &gen.BalanceAdjustRequest{
			Amount: 0, Confirmation: testTargetEmail, Reason: "对账差额补正处理",
		},
	})
	assertNoErr(t, err)
	if _, ok := resp.(gen.AdjustAdminUserBalance422JSONResponse); !ok {
		t.Fatalf("resp = %T，期望 422", resp)
	}
}

// ============================================================
// L1：确认串
// ============================================================

func TestConfirmationMatches(t *testing.T) {
	cases := []struct {
		name        string
		expect, got string
		want        bool
	}{
		{"逐字相同", testTargetEmail, testTargetEmail, true},
		{"复制粘贴带尾随空格", testTargetEmail, " " + testTargetEmail + " ", true},
		{"大小写不同（说明是手打的）", testTargetEmail, "USER@example.com", false},
		{"完全不同", testTargetEmail, "other@example.com", false},
		{"只填了前缀", testTargetEmail, "user@", false},
		{"确认串为空", testTargetEmail, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := confirmationMatches(c.expect, c.got); got != c.want {
				t.Fatalf("confirmationMatches(%q, %q) = %v，期望 %v", c.expect, c.got, got, c.want)
			}
		})
	}
}

// 🔴 期望值为空时必须**不通过**。没有这道闸，`ConstantTimeCompare("", "")` 返回 1，
// 于是任何一条让期望值变空的路径（查询漏选 email、匿名化过的行）
// 都会把 L1 变成「confirmation 也留空就放行」。
func TestConfirmationRejectsEmptyExpectation(t *testing.T) {
	if confirmationMatches("", "") {
		t.Fatal("期望值为空时必须不通过 —— 否则 L1 在期望值缺失时自动放行")
	}
	if confirmationMatches("   ", "   ") {
		t.Fatal("期望值只有空白时同样必须不通过")
	}
}

// ============================================================
// L3：TOTP step-up
// ============================================================

// 缺码、码形态不合法、依赖没配 —— 三种都必须拒绝，且**都不碰数据库**
// （adminStepUpServer 的 Pool 是 nil，真去查库会 panic）。
func TestAdminStepUpRefusesWithoutUsableCode(t *testing.T) {
	ctx := middleware.WithAdmin(context.Background(), &middleware.AdminAuth{AdminID: 7, Email: "ops@babel.plus"})

	t.Run("没带 X-TOTP-Code", func(t *testing.T) {
		fb, ie := adminStepUpServer().adminStepUp(ctx, "")
		if ie != nil {
			t.Fatalf("不该是 500：%+v", ie)
		}
		if fb == nil {
			t.Fatal("缺码必须拒绝")
		}
		if got := fb.Body.Error.Code; got != gen.AUTHTOTPREQUIRED {
			t.Fatalf("code = %s，期望 AUTH_TOTP_REQUIRED（前端要靠它决定弹不弹输入框）", got)
		}
	})

	t.Run("码形态不合法", func(t *testing.T) {
		fb, ie := adminStepUpServer().adminStepUp(ctx, "12ab56")
		if ie != nil {
			t.Fatalf("不该是 500：%+v", ie)
		}
		if fb == nil || fb.Body.Error.Code != gen.AUTHTOTPINVALID {
			t.Fatalf("形态不合法的码必须返回 AUTH_TOTP_INVALID，实际 %+v", fb)
		}
	})

	// 🔴 缺配置的现象必须是「危险操作做不了」，不能是「危险操作不需要 TOTP」。
	t.Run("TOTP 密钥没配", func(t *testing.T) {
		s := &Server{cfg: &config.Config{}, db: &store.Store{}, logger: testAdminLogger()}
		fb, ie := s.adminStepUp(ctx, "123456")
		if ie != nil {
			t.Fatalf("不该是 500：%+v", ie)
		}
		if fb == nil || fb.Body.Error.Code != gen.AUTHTOTPREQUIRED {
			t.Fatalf("缺密钥时必须拒绝，实际 %+v", fb)
		}
	})
}

// 四个 L3 操作逐个：不带 X-TOTP-Code 时必须 403，且**没有任何东西被提交**
// （Pool 为 nil，开事务会 panic）。
func TestL3OpsRefuseWithoutTotp(t *testing.T) {
	s := adminStepUpServer()
	ctx := adminUserCtx(middleware.RoleOwner, middleware.AdminPerms{AdjustBalance: true})
	reason := "已与用户电话确认无误"

	t.Run("D3 revoke-subs", func(t *testing.T) {
		resp, err := s.RevokeAdminUserSubscriptions(ctx, gen.RevokeAdminUserSubscriptionsRequestObject{
			Id:     1,
			Params: gen.RevokeAdminUserSubscriptionsParams{XTOTPCode: ""},
			Body:   &gen.ConfirmedReasonRequest{Confirmation: testTargetEmail, Reason: reason},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.RevokeAdminUserSubscriptions403JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 403", resp)
		}
	})
	t.Run("D10 balance-adjust", func(t *testing.T) {
		resp, err := s.AdjustAdminUserBalance(ctx, gen.AdjustAdminUserBalanceRequestObject{
			Id:     1,
			Params: gen.AdjustAdminUserBalanceParams{XTOTPCode: ""},
			Body:   &gen.BalanceAdjustRequest{Amount: 500, Confirmation: testTargetEmail, Reason: reason},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.AdjustAdminUserBalance403JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 403", resp)
		}
	})
	t.Run("D15 resetAdminTotp", func(t *testing.T) {
		resp, err := s.ResetAdminTotp(ctx, gen.ResetAdminTotpRequestObject{
			Id:     9,
			Params: gen.ResetAdminTotpParams{XTOTPCode: ""},
			Body:   &gen.ConfirmedReasonRequest{Confirmation: "peer@babel.plus", Reason: reason},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.ResetAdminTotp403JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 403", resp)
		}
	})
	t.Run("D16 deleteAdmin", func(t *testing.T) {
		resp, err := s.DeleteAdmin(ctx, gen.DeleteAdminRequestObject{
			Id:     9,
			Params: gen.DeleteAdminParams{XTOTPCode: ""},
			Body:   &gen.ConfirmedReasonRequest{Confirmation: "peer@babel.plus", Reason: reason},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.DeleteAdmin403JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 403", resp)
		}
	})
	// D15 createAdmin 走同一条 step-up，但它的 L4（owner）在 L3 之前，
	// 所以单独用 owner 身份再来一次。
	t.Run("D15 createAdmin", func(t *testing.T) {
		resp, err := s.CreateAdmin(ctx, gen.CreateAdminRequestObject{
			Params: gen.CreateAdminParams{XTOTPCode: ""},
			Body:   &gen.AdminAccountCreateRequest{Email: "new@babel.plus", Reason: reason},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.CreateAdmin403JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 403", resp)
		}
	})
}

// ============================================================
// L4：权限位与角色
// ============================================================

func TestAdminPermissionsViewOnlyReportsEnforcedBits(t *testing.T) {
	// 🔴 五个 admin.*.write 在库里没有列、在服务端没有任何检查点，
	// 所以它们一个都不能出现在响应里 —— 报一个没人检查的权限位，
	// 会让前端照着它画禁用态，于是「看起来管住了」而实际没有。
	got := adminPermissionsView(true, true)
	want := []gen.AdminPermission{gen.AdminOrderMarkPaid, gen.AdminUserExport}
	if len(got) != len(want) {
		t.Fatalf("permissions = %v，期望恰好 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("permissions = %v，期望 %v", got, want)
		}
	}
	// 空数组必须是 `[]` 不是 `null`：前端 `perms.includes(...)` 会在 null 上抛异常。
	empty := adminPermissionsView(false, false)
	if empty == nil {
		t.Fatal("没有任何权限位时必须返回空数组而不是 nil（nil 会序列化成 null）")
	}
	b, _ := json.Marshal(empty)
	if string(b) != "[]" {
		t.Fatalf("空权限序列化成 %s，期望 []", b)
	}
}

func TestAdminPermissionGrant(t *testing.T) {
	t.Run("不传 permissions 时全部为 false", func(t *testing.T) {
		mp, ex, _, _, err := adminPermissionGrant(nil)
		if err != nil || mp || ex {
			t.Fatalf("缺省应当一个权限位都不授予，得到 mark_paid=%v export=%v err=%v", mp, ex, err)
		}
	})
	t.Run("admin.user.export 可以授予", func(t *testing.T) {
		mp, ex, _, _, err := adminPermissionGrant(&[]gen.AdminPermission{gen.AdminUserExport})
		if err != nil {
			t.Fatalf("不该报错：%v", err)
		}
		if mp || !ex {
			t.Fatalf("期望只授予 export，得到 mark_paid=%v export=%v", mp, ex)
		}
	})
	// 🔴 ADR 0012 §16.3：带外 sink 端到端验证通过之前，perm_mark_order_paid
	// 对**所有**管理员保持 false。一个能通过 API 打开它的端点直接推翻那条裁决。
	t.Run("admin.order.mark_paid 被 ADR 0012 §16.3 挡住", func(t *testing.T) {
		_, _, bad, why, err := adminPermissionGrant(&[]gen.AdminPermission{gen.AdminOrderMarkPaid})
		if !errors.Is(err, errAdminPermUngrantable) {
			t.Fatalf("err = %v，期望 errAdminPermUngrantable", err)
		}
		if bad != gen.AdminOrderMarkPaid {
			t.Fatalf("bad = %s，期望 admin.order.mark_paid", bad)
		}
		if !strings.Contains(why, "16.3") {
			t.Errorf("拒绝理由里必须点出 ADR 0012 §16.3，否则没人知道怎么解开：%q", why)
		}
	})
	// 五个 admin.*.write 在库里没有列。**绝不能静默忽略然后返回 201** ——
	// 那会让人以为某个管理员没有某个权限，而他其实有（或反过来）。
	for _, p := range []gen.AdminPermission{
		gen.AdminUserWrite, gen.AdminNodeWrite, gen.AdminPlanWrite,
		gen.AdminTicketWrite, gen.AdminSettingsWrite,
	} {
		t.Run(string(p)+" 无法授予", func(t *testing.T) {
			_, _, bad, _, err := adminPermissionGrant(&[]gen.AdminPermission{p})
			if !errors.Is(err, errAdminPermUngrantable) {
				t.Fatalf("err = %v，期望 errAdminPermUngrantable", err)
			}
			if bad != p {
				t.Fatalf("bad = %s，期望 %s", bad, p)
			}
		})
	}
	t.Run("枚举外的值", func(t *testing.T) {
		_, _, _, _, err := adminPermissionGrant(&[]gen.AdminPermission{"admin.everything"})
		if !errors.Is(err, errAdminPermUngrantable) {
			t.Fatalf("err = %v，期望 errAdminPermUngrantable", err)
		}
	})
}

// D10 的权限位是 `perm_adjust_balance`。它在契约的枚举里没有对应值（只能改库授予），
// 但列是存在的 —— 一个存在于 schema、却没有任何代码检查的权限位，
// 会让所有人都以为 D10 被管住了。
func TestAdjustBalanceRefusedWithoutPermission(t *testing.T) {
	s := adminStepUpServer()
	ctx := adminUserCtx(middleware.RoleOwner, middleware.AdminPerms{AdjustBalance: false})
	resp, err := s.AdjustAdminUserBalance(ctx, gen.AdjustAdminUserBalanceRequestObject{
		Id:     1,
		Params: gen.AdjustAdminUserBalanceParams{XTOTPCode: "123456"},
		Body: &gen.BalanceAdjustRequest{
			Amount: 500, Confirmation: testTargetEmail, Reason: "对账差额补正处理",
		},
	})
	assertNoErr(t, err)
	r, ok := resp.(gen.AdjustAdminUserBalance403JSONResponse)
	if !ok {
		t.Fatalf("resp = %T，期望 403", resp)
	}
	if r.Body.Error.Code != gen.AUTHPERMISSIONDENIED {
		t.Fatalf("code = %s，期望 AUTH_PERMISSION_DENIED", r.Body.Error.Code)
	}
}

// D14 导出：`admin.user.export` 默认不授予（§6.2 L4 点名的两个之一）。
func TestExportRefusedWithoutPermission(t *testing.T) {
	s := adminTestServer()
	ctx := adminUserCtx(middleware.RoleOwner, middleware.AdminPerms{ExportCSV: false})
	resp, err := s.ExportAdminUsers(ctx, gen.ExportAdminUsersRequestObject{
		Body: &gen.ReasonRequest{Reason: "季度对账需要用户名单"},
	})
	assertNoErr(t, err)
	if _, ok := resp.(gen.ExportAdminUsers403JSONResponse); !ok {
		t.Fatalf("resp = %T，期望 403（db 为 nil，走到查询就会 panic）", resp)
	}
}

// D15 / D16 的角色闸：只有 owner 能碰管理员账号本身。
func TestAdminAccountOpsRequireOwnerRole(t *testing.T) {
	s := adminTestServer()
	ctx := adminUserCtx(middleware.RoleSupport, middleware.AdminPerms{})
	reason := "该同事已离职需要停用"

	t.Run("createAdmin", func(t *testing.T) {
		resp, err := s.CreateAdmin(ctx, gen.CreateAdminRequestObject{
			Params: gen.CreateAdminParams{XTOTPCode: "123456"},
			Body:   &gen.AdminAccountCreateRequest{Email: "new@babel.plus", Reason: reason},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.CreateAdmin403JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 403", resp)
		}
	})
	t.Run("deleteAdmin", func(t *testing.T) {
		resp, err := s.DeleteAdmin(ctx, gen.DeleteAdminRequestObject{
			Id:     9,
			Params: gen.DeleteAdminParams{XTOTPCode: "123456"},
			Body:   &gen.ConfirmedReasonRequest{Confirmation: "peer@babel.plus", Reason: reason},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.DeleteAdmin403JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 403", resp)
		}
	})
	t.Run("resetAdminTotp", func(t *testing.T) {
		resp, err := s.ResetAdminTotp(ctx, gen.ResetAdminTotpRequestObject{
			Id:     9,
			Params: gen.ResetAdminTotpParams{XTOTPCode: "123456"},
			Body:   &gen.ConfirmedReasonRequest{Confirmation: "peer@babel.plus", Reason: reason},
		})
		assertNoErr(t, err)
		if _, ok := resp.(gen.ResetAdminTotp403JSONResponse); !ok {
			t.Fatalf("resp = %T，期望 403", resp)
		}
	})
}

// ============================================================
// 搜索：LIKE 元字符转义
// ============================================================

// 🔴 不转义时一个 `%` 就等于「返回全部用户」，而这个端点的下游是 D14 的同一批数据 ——
// 于是一个连 admin.user.export 权限位都没有的人，用搜索框就能把名单翻完。
func TestEscapeLikePattern(t *testing.T) {
	cases := map[string]string{
		"%":           `\%`,
		"_":           `\_`,
		`\`:           `\\`,
		"a%b_c":       `a\%b\_c`,
		`c:\temp%`:    `c:\\temp\%`,
		"user@ex.com": "user@ex.com",
	}
	for in, want := range cases {
		if got := escapeLikePattern(in); got != want {
			t.Errorf("escapeLikePattern(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestAdminUserSearchFilter(t *testing.T) {
	t.Run("空与全空白不加筛选", func(t *testing.T) {
		if adminUserSearchFilter(nil) != nil {
			t.Error("q 缺席时不该加筛选")
		}
		blank := gen.SearchQuery("   ")
		if got := adminUserSearchFilter(&blank); got != nil {
			t.Errorf("全空白应当不加筛选（否则是一次无意义的全表 ILIKE），得到 %q", *got)
		}
	})
	t.Run("百分号被转义后不再是通配", func(t *testing.T) {
		q := gen.SearchQuery("%")
		got := adminUserSearchFilter(&q)
		if got == nil {
			t.Fatal("不该返回 nil")
		}
		if *got != `%\%%` {
			t.Fatalf("email_like = %q，期望 %q —— 少一个转义就等于返回全部用户", *got, `%\%%`)
		}
	})
}

// ============================================================
// D1：编辑用户
// ============================================================

func TestBuildUserEntitlementParams(t *testing.T) {
	t.Run("总额传给 transfer_enable_total", func(t *testing.T) {
		// 🔴 契约给的是**总额**，而 users.transfer_enable 是生成列不可赋值；
		// SQL 内部算 `_plan = 总额 − 当前 _pack`。传错字段的现象是运行时炸，
		// 而 sqlc generate 与 go build 都是 exit 0。
		arg, _, err := buildUserEntitlementParams(42, gen.AdminUserPatch{
			Reason: "扩容后统一调整配额", TransferEnableBytes: ptrOf(int64(500 << 30)),
		})
		if err != nil {
			t.Fatalf("不该报错：%v", err)
		}
		if arg.TransferEnableTotal == nil || *arg.TransferEnableTotal != 500<<30 {
			t.Fatalf("TransferEnableTotal = %v，期望 %d", arg.TransferEnableTotal, int64(500<<30))
		}
		if arg.UserID != 42 {
			t.Fatalf("UserID = %d", arg.UserID)
		}
	})
	t.Run("一个字段都没带时拒绝", func(t *testing.T) {
		// 空 PATCH 在 SQL 里是一次 coalesce 全命中的空更新，会成功并写一条
		// before == after 的 D1 审计 —— 而 D1 的审计是排查「谁改了配额」的唯一线索。
		_, _, err := buildUserEntitlementParams(1, gen.AdminUserPatch{Reason: "看看会怎么样"})
		if !errors.Is(err, errAdminPatchEmpty) {
			t.Fatalf("err = %v，期望 errAdminPatchEmpty", err)
		}
	})
	t.Run("uuid 写错时报错而不是当成不改", func(t *testing.T) {
		// 静默忽略一个写错的 uuid 会让管理员以为换过了，而节点侧那把旧钥匙还能连。
		_, field, err := buildUserEntitlementParams(1, gen.AdminUserPatch{
			Reason: "换 uuid 以断开泄漏的客户端", Uuid: ptrOf("not-a-uuid"),
		})
		if err == nil {
			t.Fatal("非法 uuid 必须报错")
		}
		if field != "uuid" {
			t.Fatalf("field = %q，期望 uuid", field)
		}
	})
	t.Run("合法 uuid 与到期时间", func(t *testing.T) {
		exp := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
		arg, _, err := buildUserEntitlementParams(1, gen.AdminUserPatch{
			Reason:    "补偿故障期间的时长",
			Uuid:      ptrOf("6ba7b810-9dad-11d1-80b4-00c04fd430c8"),
			ExpiredAt: &exp,
		})
		if err != nil {
			t.Fatalf("不该报错：%v", err)
		}
		if !arg.NewUuid.Valid {
			t.Fatal("uuid 没有被解析进参数")
		}
		if !arg.ExpiredAt.Valid || !arg.ExpiredAt.Time.Equal(exp) {
			t.Fatalf("ExpiredAt = %+v，期望 %v", arg.ExpiredAt, exp)
		}
	})
}

// ============================================================
// D2：封禁 / 解封
// ============================================================

type fakeBanQuerier struct {
	banRow    dbgen.AdminBanUserRow
	unbanRow  dbgen.AdminUnbanUserRow
	detail    dbgen.GetAdminUserDetailRow
	banErr    error
	unbanErr  error
	banCalls  int
	banReason string
}

func (f *fakeBanQuerier) AdminBanUser(_ context.Context, arg dbgen.AdminBanUserParams) (dbgen.AdminBanUserRow, error) {
	f.banCalls++
	f.banReason = arg.Reason
	return f.banRow, f.banErr
}

func (f *fakeBanQuerier) AdminUnbanUser(context.Context, int64) (dbgen.AdminUnbanUserRow, error) {
	f.banCalls++
	return f.unbanRow, f.unbanErr
}

func (f *fakeBanQuerier) GetAdminUserDetail(context.Context, int64) (dbgen.GetAdminUserDetailRow, error) {
	return f.detail, nil
}

func TestBanAdminUserTxAuditCarriesBeforeAndAfter(t *testing.T) {
	first := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	q := &fakeBanQuerier{
		banRow: dbgen.AdminBanUserRow{
			BeforeBanned: false, BeforeBannedAt: pgtype.Timestamptz{},
			ID: 42, Email: testTargetEmail,
			AfterBanned: true, AfterBannedReason: ptrOf("批量注册滥用"), AfterBannedAt: admTS(first),
		},
		detail: dbgen.GetAdminUserDetailRow{ID: 42, Email: testTargetEmail, Banned: true},
	}
	view, entry, err := banAdminUserTx(context.Background(), q, 42, "批量注册滥用已核实")
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if view.Id != 42 || !view.Banned {
		t.Fatalf("响应视图不对：%+v", view)
	}
	if entry.Action != "D2.user.ban" || entry.TargetType != "user" || entry.TargetID != "42" {
		t.Fatalf("审计条目定位不对：%+v", entry)
	}
	if entry.Reason != "批量注册滥用已核实" {
		t.Fatalf("审计没有带上原因：%q", entry.Reason)
	}
	before := entry.Before.(map[string]any)
	after := entry.After.(map[string]any)
	if before["banned"] != false || after["banned"] != true {
		t.Fatalf("改前/改后值不对：before=%v after=%v", before, after)
	}
	// 生效延迟必须进审计：事后看「封禁时刻」与「他最后一次连上节点的时刻」
	// 相差不到 60 秒是**正常**的，不记这一条会让人以为封禁没生效。
	if after["node_effective_delay_seconds"] != nodeUserPollSeconds {
		t.Errorf("审计里应当记下节点生效延迟，实际 %v", after["node_effective_delay_seconds"])
	}
}

// ⚠️ 重复封禁不是错误：查询刻意没有 `AND banned = false` 的 CAS，
// 因为 0 行会与「用户不存在」一起塌成 ErrNoRows，而对一个**已经被封**的用户
// 回 404 是谎话。这里断言 (true → true) 能正常返回并留下审计。
func TestBanAdminUserTxIsIdempotent(t *testing.T) {
	first := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	q := &fakeBanQuerier{
		banRow: dbgen.AdminBanUserRow{
			BeforeBanned: true, BeforeBannedAt: admTS(first),
			ID: 42, Email: testTargetEmail,
			AfterBanned: true, AfterBannedAt: admTS(first),
		},
		detail: dbgen.GetAdminUserDetailRow{ID: 42, Email: testTargetEmail, Banned: true},
	}
	_, entry, err := banAdminUserTx(context.Background(), q, 42, "再封一次确认状态")
	if err != nil {
		t.Fatalf("重复封禁不该报错：%v", err)
	}
	before := entry.Before.(map[string]any)
	after := entry.After.(map[string]any)
	if before["banned"] != true || after["banned"] != true {
		t.Fatalf("重复封禁的审计应当是 (true → true)：before=%v after=%v", before, after)
	}
	// banned_at 必须保住第一次被封的时刻：重复封禁不该把「他从什么时候起被封」改写成今天。
	got := after["banned_at"].(*time.Time)
	if got == nil || !got.Equal(first) {
		t.Fatalf("banned_at = %v，期望保住首次封禁时刻 %v", got, first)
	}
}

func TestUnbanAdminUserTxAudit(t *testing.T) {
	q := &fakeBanQuerier{
		unbanRow: dbgen.AdminUnbanUserRow{
			BeforeBanned: true, BeforeBannedReason: ptrOf("误封"), BeforeBannedAt: admTS(time.Now()),
			ID: 42, Email: testTargetEmail, AfterBanned: false,
		},
		detail: dbgen.GetAdminUserDetailRow{ID: 42, Email: testTargetEmail},
	}
	_, entry, err := unbanAdminUserTx(context.Background(), q, 42, "确认为误封已复核")
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if entry.Action != "D2.user.unban" {
		t.Fatalf("action = %q", entry.Action)
	}
	after := entry.After.(map[string]any)
	if after["banned"] != false {
		t.Fatalf("解封后 banned 应当是 false：%v", after)
	}
}

// ============================================================
// D3：吊销订阅
// ============================================================

type fakeRevokeQuerier struct {
	target      dbgen.LockAdminUserTargetRow
	lockErr     error
	revokeRow   dbgen.RevokeAllUserSubscriptionTokensRow
	revokeCalls int
}

func (f *fakeRevokeQuerier) LockAdminUserTarget(context.Context, int64) (dbgen.LockAdminUserTargetRow, error) {
	return f.target, f.lockErr
}

func (f *fakeRevokeQuerier) RevokeAllUserSubscriptionTokens(context.Context, int64) (dbgen.RevokeAllUserSubscriptionTokensRow, error) {
	f.revokeCalls++
	return f.revokeRow, nil
}

func TestRevokeAdminUserSubsTx(t *testing.T) {
	at := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	q := &fakeRevokeQuerier{
		target:    dbgen.LockAdminUserTargetRow{ID: 42, Email: testTargetEmail},
		revokeRow: dbgen.RevokeAllUserSubscriptionTokensRow{SubRevokedAt: admTS(at), Revoked: 3},
	}
	out, entry, err := revokeAdminUserSubsTx(context.Background(), q, 42, testTargetEmail, "订阅链接被公开分享")
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if out.Revoked != 3 || !out.SubRevokedAt.Equal(at) {
		t.Fatalf("响应不对：%+v", out)
	}
	if entry.Action != "D3.user.revoke_subscriptions" {
		t.Fatalf("action = %q", entry.Action)
	}
	// 撤掉的条数是这条审计里唯一能回答「这次操作到底影响了什么」的数字。
	if entry.After.(map[string]any)["revoked"] != int32(3) {
		t.Fatalf("审计里缺少 revoked 条数：%+v", entry.After)
	}
}

// 🔴 确认串不匹配时**一次业务写入都不许发生**。
func TestRevokeAdminUserSubsTxConfirmationMismatchDoesNotWrite(t *testing.T) {
	q := &fakeRevokeQuerier{target: dbgen.LockAdminUserTargetRow{ID: 42, Email: testTargetEmail}}
	_, _, err := revokeAdminUserSubsTx(context.Background(), q, 42, "someone-else@example.com", "订阅链接被公开分享")
	if !errors.Is(err, errAdminConfirmationMismatch) {
		t.Fatalf("err = %v，期望 errAdminConfirmationMismatch", err)
	}
	if q.revokeCalls != 0 {
		t.Fatalf("确认串不匹配却调用了 %d 次吊销 —— L1 必须在写之前", q.revokeCalls)
	}
}

// 🔴 期望值必须来自**服务端查出来的那一行**，不是请求体。
// 这条用例把 target.Email 与 confirmation 都设成空串：如果实现里
// 「拿请求体比对请求体」，它会通过；正确实现必须拒绝。
func TestRevokeAdminUserSubsTxRejectsEmptyTargetEmail(t *testing.T) {
	q := &fakeRevokeQuerier{target: dbgen.LockAdminUserTargetRow{ID: 42, Email: ""}}
	_, _, err := revokeAdminUserSubsTx(context.Background(), q, 42, "", "订阅链接被公开分享")
	if !errors.Is(err, errAdminConfirmationMismatch) {
		t.Fatalf("err = %v，期望拒绝：期望值为空时 L1 不能自动放行", err)
	}
	if q.revokeCalls != 0 {
		t.Fatal("不该发生任何写入")
	}
}

// ============================================================
// D10：调余额
// ============================================================

type fakeBalanceQuerier struct {
	target     dbgen.LockAdminUserTargetRow
	before     dbgen.GetWalletOverviewRow
	after      dbgen.GetWalletOverviewRow
	overviewN  int
	entryCalls int
	lines      []dbgen.CreateLedgerLineParams
	upserts    []dbgen.UpsertWalletBalanceParams
	upsertErr  error
}

func (f *fakeBalanceQuerier) LockAdminUserTarget(context.Context, int64) (dbgen.LockAdminUserTargetRow, error) {
	return f.target, nil
}

func (f *fakeBalanceQuerier) GetWalletOverview(context.Context, int64) (dbgen.GetWalletOverviewRow, error) {
	f.overviewN++
	if f.overviewN == 1 {
		return f.before, nil
	}
	return f.after, nil
}

func (f *fakeBalanceQuerier) CreateLedgerEntry(_ context.Context, arg dbgen.CreateLedgerEntryParams) (dbgen.LedgerEntry, error) {
	f.entryCalls++
	return dbgen.LedgerEntry{ID: 900, EntryNo: arg.EntryNo}, nil
}

func (f *fakeBalanceQuerier) CreateLedgerLine(_ context.Context, arg dbgen.CreateLedgerLineParams) (dbgen.LedgerLine, error) {
	f.lines = append(f.lines, arg)
	return dbgen.LedgerLine{}, nil
}

func (f *fakeBalanceQuerier) UpsertWalletBalance(_ context.Context, arg dbgen.UpsertWalletBalanceParams) (dbgen.WalletBalance, error) {
	f.upserts = append(f.upserts, arg)
	return dbgen.WalletBalance{}, f.upsertErr
}

func newBalanceInput(amount int64) adjustBalanceInput {
	return adjustBalanceInput{
		UserID: 42, Amount: amount, Confirmation: testTargetEmail,
		Reason:   "客服补偿故障期间的损失",
		Accounts: dbgen.GetAdminBalanceAdjustAccountsRow{AdjustAccountID: 11, WalletAccountID: 22},
		EntryNo:  "BA20260830T100000-ABCDEF",
	}
}

func newBalanceQuerier() *fakeBalanceQuerier {
	return &fakeBalanceQuerier{
		target: dbgen.LockAdminUserTargetRow{ID: 42, Email: testTargetEmail},
		before: dbgen.GetWalletOverviewRow{BalanceLedger: 1000, BalanceCached: 1000, NonWithdrawableAmount: 1000},
		after:  dbgen.GetWalletOverviewRow{BalanceLedger: 1500, BalanceCached: 1500, NonWithdrawableAmount: 1500},
	}
}

// 🔴 两条腿必须符号相反、绝对值相等、币种相同。弄反不会报错，只会让钱反向；
// 少一条腿会让 FindUnbalancedLedgerEntries 第二天报红，而那时钱已经和真钱混在一起了。
func TestAdjustBalanceLegsSumToZero(t *testing.T) {
	for _, amount := range []int64{500, -500} {
		q := newBalanceQuerier()
		_, _, err := adjustBalanceTx(context.Background(), q, newBalanceInput(amount))
		if err != nil {
			t.Fatalf("amount=%d 不该报错：%v", amount, err)
		}
		if len(q.lines) != 2 {
			t.Fatalf("amount=%d 写了 %d 条腿，期望 2 条", amount, len(q.lines))
		}
		var sum int64
		for _, l := range q.lines {
			sum += l.Amount
			if l.Currency != ledgerCurrencyCNY {
				t.Fatalf("币种 = %q，期望 CNY", l.Currency)
			}
		}
		if sum != 0 {
			t.Fatalf("amount=%d 时两条腿之和 = %d，必须为 0", amount, sum)
		}
		// expense:admin_adjust 那条腿是 +amount（借），liability:user_wallet 是 -amount（贷）。
		// 用户视角的余额是 -SUM(amount)，所以 wallet 那条腿的负号不是笔误。
		if q.lines[0].AccountID != 11 || q.lines[0].Amount != amount {
			t.Fatalf("第一条腿应当是 expense:admin_adjust 且 amount=%d，实际 %+v", amount, q.lines[0])
		}
		if q.lines[1].AccountID != 22 || q.lines[1].Amount != -amount {
			t.Fatalf("第二条腿应当是 liability:user_wallet 且 amount=%d，实际 %+v", -amount, q.lines[1])
		}
		if q.lines[1].SubjectID == nil || *q.lines[1].SubjectID != 42 {
			t.Fatal("liability:user_wallet 那条腿必须带 subject_id，否则这笔钱记不到人头上")
		}
	}
}

// ⚠️ UpsertWalletBalance 的 balance 参数是**增量不是绝对值**
// （ON CONFLICT 分支写的是 `balance + EXCLUDED.balance`）。
// 传绝对值的现象是余额被**重置**，而不是报错。
func TestAdjustBalanceUpsertsDeltaNotAbsolute(t *testing.T) {
	q := newBalanceQuerier()
	if _, _, err := adjustBalanceTx(context.Background(), q, newBalanceInput(500)); err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if len(q.upserts) != 1 {
		t.Fatalf("upsert 调了 %d 次", len(q.upserts))
	}
	if q.upserts[0].Balance != 500 {
		t.Fatalf("upsert 的 balance = %d，期望增量 500（不是调整后的绝对值 1500）", q.upserts[0].Balance)
	}
}

// 🔴 balance_ledger 与 balance_cached **两个都要记**：两者不等本身就是
// 一条必须写进审计的事实（缓存漂移），只记一个的话事后再也分不清
// 「当时缓存是不是已经歪了」。
func TestAdjustBalanceAuditRecordsBothBalances(t *testing.T) {
	q := newBalanceQuerier()
	q.before = dbgen.GetWalletOverviewRow{BalanceLedger: 1000, BalanceCached: 990}
	_, entry, err := adjustBalanceTx(context.Background(), q, newBalanceInput(500))
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if entry.Action != "D10.user.balance_adjust" {
		t.Fatalf("action = %q", entry.Action)
	}
	before := entry.Before.(map[string]any)
	if before["balance_ledger"] != int64(1000) || before["balance_cached"] != int64(990) {
		t.Fatalf("改前值必须同时记两个余额口径：%v", before)
	}
	after := entry.After.(map[string]any)
	for _, k := range []string{"balance_ledger", "balance_cached", "amount", "entry_no"} {
		if _, ok := after[k]; !ok {
			t.Errorf("改后值缺少 %s", k)
		}
	}
}

// 🔴 确认串不匹配时不许写任何分录。
func TestAdjustBalanceConfirmationMismatchWritesNothing(t *testing.T) {
	q := newBalanceQuerier()
	in := newBalanceInput(500)
	in.Confirmation = "someone-else@example.com"
	_, _, err := adjustBalanceTx(context.Background(), q, in)
	if !errors.Is(err, errAdminConfirmationMismatch) {
		t.Fatalf("err = %v，期望 errAdminConfirmationMismatch", err)
	}
	if q.entryCalls != 0 || len(q.lines) != 0 || len(q.upserts) != 0 {
		t.Fatalf("确认串不匹配却动了账本：entry=%d lines=%d upserts=%d", q.entryCalls, len(q.lines), len(q.upserts))
	}
	if q.overviewN != 0 {
		t.Fatal("确认串不匹配时连余额都不该读 —— L1 是这条链路的第一道闸")
	}
}

// 扣穿由数据库的 CHECK 拒绝（23514），handler 只负责把它翻成 422。
// **不要自己先读一次余额再比大小** —— 那是一次 TOCTOU。
func TestIsPgCheckViolation(t *testing.T) {
	if !isCheckViolation(&pgconn.PgError{Code: "23514"}) {
		t.Fatal("23514 必须被识别成 CHECK 冲突")
	}
	if isCheckViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("23505 是唯一约束，不该被当成 CHECK 冲突（它要翻成 409/422 的另一支）")
	}
	if isCheckViolation(errors.New("boom")) {
		t.Fatal("普通错误不该被识别成 CHECK 冲突 —— 那会把 500 静默变成 422")
	}
}

// ============================================================
// D14：导出
// ============================================================

// 🔴 判据是 `> cap` 不是 `== cap`：查询取的是 cap+1 行，
// 用 `== cap` 判会在用户数**正好等于**上限时误判一次拒绝。
func TestAdminExportIsTruncated(t *testing.T) {
	if adminExportIsTruncated(adminExportRowCap) {
		t.Fatal("取回恰好 cap 行说明取完了，不是截断")
	}
	if !adminExportIsTruncated(adminExportRowCap + 1) {
		t.Fatal("取回 cap+1 行说明还有更多，必须判为截断")
	}
	if adminExportIsTruncated(0) {
		t.Fatal("空结果不是截断")
	}
}

func TestBuildUsersCSV(t *testing.T) {
	created := time.Date(2026, 3, 1, 8, 30, 0, 0, time.UTC)
	rows := []dbgen.ExportAdminUsersRowsRow{{
		ID: 1, Email: testTargetEmail, Banned: false, CreatedAt: admTS(created),
		GroupID: 3, TransferEnablePlan: 100, TransferEnablePack: 20, TransferEnable: 120,
		UploadBytes: 7, DownloadBytes: 8, BalanceAmount: 900,
	}}
	out, err := buildUsersCSV(rows)
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	s := string(out)
	// Excel（简体中文）没有 BOM 会按 GBK 解，套餐名与备注变成乱码 ——
	// 而这份文件的第一个读者几乎一定是用 Excel 打开它的。
	if !strings.HasPrefix(s, "\ufeff") {
		t.Error("CSV 开头缺少 UTF-8 BOM")
	}
	lines := strings.Split(strings.TrimRight(strings.TrimPrefix(s, "\ufeff"), "\r\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("行数 = %d，期望 1 行表头 + 1 行数据：%q", len(lines), s)
	}
	if strings.TrimRight(lines[0], "\r") != strings.Join(adminExportCSVHeader, ",") {
		t.Fatalf("表头 = %q", lines[0])
	}
	if !strings.Contains(lines[1], created.Format(time.RFC3339)) {
		t.Errorf("时间列没有按 RFC3339 输出：%q", lines[1])
	}
}

// 🔴 NULL 必须是**空单元格**不是 0。
// 「device_limit 是 0」与「device_limit 没有值（不限设备）」是相反的两件事，
// 而一份把 NULL 写成 0 的名单会让运营以为所有人都被限成 0 台设备。
func TestBuildUsersCSVNullsAreEmptyCells(t *testing.T) {
	out, err := buildUsersCSV([]dbgen.ExportAdminUsersRowsRow{{
		ID: 1, Email: testTargetEmail, CreatedAt: admTS(time.Now()),
		// LastLoginAt / ExpiredAt / PlanID / DeviceLimit 全部留 NULL
	}})
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	line := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")[1]
	cells := strings.Split(strings.TrimRight(line, "\r"), ",")
	idx := map[string]int{}
	for i, h := range adminExportCSVHeader {
		idx[h] = i
	}
	for _, col := range []string{"last_login_at", "expired_at", "plan_id", "device_limit"} {
		if got := cells[idx[col]]; got != "" {
			t.Errorf("%s 列 = %q，NULL 必须是空单元格（写 0 会让「不限」变成「限 0」）", col, got)
		}
	}
	if got := cells[idx["created_at"]]; got == "" {
		t.Error("created_at 是 NOT NULL 列，不该是空的")
	}
}

// ⚠️ **导出里不能出现 uuid**：uuid 是节点侧的连接凭据，
// 一份泄漏的 CSV 若含 uuid，等于把全部用户的账号一起送出去。
func TestExportCSVNeverCarriesCredentials(t *testing.T) {
	for _, h := range adminExportCSVHeader {
		switch h {
		case "uuid", "password_hash", "remarks", "token", "secret":
			t.Fatalf("导出列头里出现了 %q —— 凭据/内部备注不该进导出", h)
		}
	}
}

// ============================================================
// D15：TOTP 绑定材料
// ============================================================

// 🔴 密文形态必须与 middleware/admin.go 的 decryptTOTPSecret 逐字对齐：
// nonce(12) || ciphertext || tag(16)，**明文是 base32 字符串不是原始字节**。
// 对不齐的现象不是报错，是「所有新管理员的验证码都不对」——
// 而排查方向会先指向时钟、再指向 app，最后才会有人想到密文格式。
func TestEncryptTOTPSecretMatchesMiddlewareFormat(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	secret, err := newTOTPSecret()
	if err != nil {
		t.Fatalf("生成 secret 失败：%v", err)
	}
	enc, err := encryptTOTPSecret(key, secret)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}

	// 照着 middleware 那一侧的解法解回来。
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	if len(enc) <= gcm.NonceSize() {
		t.Fatalf("密文太短：%d 字节", len(enc))
	}
	plain, err := gcm.Open(nil, enc[:gcm.NonceSize()], enc[gcm.NonceSize():], nil)
	if err != nil {
		t.Fatalf("解密失败（密文形态与 middleware 对不上）：%v", err)
	}
	if string(plain) != secret {
		t.Fatalf("解出来的明文 = %q，期望 %q", plain, secret)
	}
	// 明文必须是能被 base32 解开的串 —— middleware 那侧解完还要再解一次 base32。
	if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret); err != nil {
		t.Fatalf("secret 不是合法 base32：%v", err)
	}
}

func TestEncryptTOTPSecretRejectsBadKey(t *testing.T) {
	// 密钥长度不对时必须失败而不是用一把短密钥凑合 —— 后者会让所有 secret
	// 在密钥补齐之后统统解不开。
	if _, err := encryptTOTPSecret(make([]byte, 16+1), "AAAA"); err == nil {
		t.Fatal("非法长度的密钥必须报错")
	}
	if _, err := encryptTOTPSecret(nil, "AAAA"); err == nil {
		t.Fatal("空密钥必须报错")
	}
}

func TestNewTOTPSecretIsUnique(t *testing.T) {
	a, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("两次生成的 secret 相同 —— 随机源坏了")
	}
	if len(a) != 32 {
		t.Fatalf("secret 长度 = %d，期望 32（160 位 base32 之后）", len(a))
	}
}

// 三个参数（algorithm/digits/period）必须显式写出来：middleware 那侧写死了
// SHA1 / 6 位 / 30 秒，而各家 app 的默认值并不完全一致。
// 少写一个的现象是「某些人的码永远不对」，且只在那一款 app 上复现。
func TestOtpauthURL(t *testing.T) {
	u := otpauthURL(totpIssuerName, "ops@babel.plus", "JBSWY3DPEHPK3PXP")
	for _, want := range []string{
		"otpauth://totp/", "secret=JBSWY3DPEHPK3PXP", "issuer=BabelPlus",
		"algorithm=SHA1", "digits=6", "period=30",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("otpauth URL 里缺少 %q：%s", want, u)
		}
	}
	if !strings.Contains(u, "BabelPlus:ops@babel.plus") && !strings.Contains(u, "BabelPlus%3Aops@babel.plus") {
		t.Errorf("label 里应当带 issuer 前缀（决定 Authenticator 里的分组显示）：%s", u)
	}
}

// ============================================================
// D15：重置 TOTP
// ============================================================

type fakeTotpResetQuerier struct {
	target     dbgen.LockAdminAccountTargetRow
	row        dbgen.ResetAdminAccountTotpRow
	resetCalls int
	gotEnc     []byte
}

func (f *fakeTotpResetQuerier) LockAdminAccountTarget(context.Context, int64) (dbgen.LockAdminAccountTargetRow, error) {
	return f.target, nil
}

func (f *fakeTotpResetQuerier) ResetAdminAccountTotp(_ context.Context, arg dbgen.ResetAdminAccountTotpParams) (dbgen.ResetAdminAccountTotpRow, error) {
	f.resetCalls++
	f.gotEnc = arg.TotpSecretEnc
	return f.row, nil
}

// 🔴 **审计里绝不能出现 secret（明文密文都不行）。**
// audit_logs 是 append-only、永不删除的表，一份写进去的凭据是**永久**写进去的，
// 而这张表可能被导出、被拷进工单、被贴进聊天窗口。
func TestResetAdminTotpAuditNeverCarriesSecret(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	enc := []byte{0xde, 0xad, 0xbe, 0xef}
	q := &fakeTotpResetQuerier{
		target: dbgen.LockAdminAccountTargetRow{ID: 9, Email: "peer@babel.plus"},
		row: dbgen.ResetAdminAccountTotpRow{
			ID: 9, Email: "peer@babel.plus", AfterTotpConfirmedAt: admTS(time.Now()),
		},
	}
	_, entry, err := resetAdminTotpTx(context.Background(), q, 9, "peer@babel.plus", "手机丢失需要重新绑定", enc)
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	blob, err := json.Marshal(map[string]any{"before": entry.Before, "after": entry.After, "reason": entry.Reason})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, "deadbeef", "\xde\xad\xbe\xef"} {
		if strings.Contains(string(blob), forbidden) {
			t.Fatalf("审计快照里出现了凭据材料：%s", blob)
		}
	}
	if entry.Action != "D15.admin.reset_totp" {
		t.Fatalf("action = %q", entry.Action)
	}
	// 时间戳仍然要在：审计要能证明「谁在什么时候给谁换了钥匙」。
	if _, ok := entry.After.(map[string]any)["totp_confirmed_at"]; !ok {
		t.Error("改后值里应当有 totp_confirmed_at")
	}
}

func TestResetAdminTotpTxConfirmationMismatchDoesNotWrite(t *testing.T) {
	q := &fakeTotpResetQuerier{target: dbgen.LockAdminAccountTargetRow{ID: 9, Email: "peer@babel.plus"}}
	_, _, err := resetAdminTotpTx(context.Background(), q, 9, "wrong@babel.plus", "手机丢失需要重新绑定", []byte{1})
	if !errors.Is(err, errAdminConfirmationMismatch) {
		t.Fatalf("err = %v，期望 errAdminConfirmationMismatch", err)
	}
	if q.resetCalls != 0 {
		t.Fatal("确认串不匹配却换了钥匙 —— 那个人会当场进不来")
	}
}

// ============================================================
// D16：停用管理员
// ============================================================

type fakeDisableQuerier struct {
	target       dbgen.LockAdminAccountTargetRow
	row          dbgen.DisableAdminAccountRow
	disableCalls int
}

func (f *fakeDisableQuerier) LockAdminAccountTarget(context.Context, int64) (dbgen.LockAdminAccountTargetRow, error) {
	return f.target, nil
}

func (f *fakeDisableQuerier) DisableAdminAccount(context.Context, int64) (dbgen.DisableAdminAccountRow, error) {
	f.disableCalls++
	return f.row, nil
}

func TestDeleteAdminTxIsSoftDisable(t *testing.T) {
	now := time.Now().UTC()
	q := &fakeDisableQuerier{
		target: dbgen.LockAdminAccountTargetRow{ID: 9, Email: "peer@babel.plus"},
		row: dbgen.DisableAdminAccountRow{
			ID: 9, Email: "peer@babel.plus", BeforeRole: middleware.RoleAdmin,
			BeforeDisabledAt: pgtype.Timestamptz{}, AfterDisabledAt: admTS(now),
		},
	}
	entry, err := deleteAdminTx(context.Background(), q, 9, 7, "peer@babel.plus", "该同事已离职需要停用")
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if q.disableCalls != 1 {
		t.Fatalf("停用调用了 %d 次", q.disableCalls)
	}
	// 🔴 action 必须说的是「停用」而不是「删除」：硬删会让这个人过去每一条审计的
	// admin_user_id 变成 NULL（外键 ON DELETE SET NULL），那些记录会认不出人。
	if entry.Action != "D16.admin.disable" {
		t.Fatalf("action = %q，期望 D16.admin.disable", entry.Action)
	}
	before := entry.Before.(map[string]any)
	if before["disabled_at"] != (*time.Time)(nil) {
		t.Fatalf("改前的 disabled_at 应当是 nil：%v", before["disabled_at"])
	}
	after := entry.After.(map[string]any)
	if after["disabled_at"] == nil {
		t.Fatal("改后的 disabled_at 必须有值")
	}
	// 🔴 email 必须进快照：AuditLogEntry 上只有 target_id（一个数字），
	// 事后翻审计的人靠这一列才知道「被停用的是谁」——
	// 而管理员账号恰恰是那种「过两年没人记得 id=9 是谁」的实体。
	if after["email"] != "peer@babel.plus" {
		t.Errorf("改后值里必须带 email，实际 %v", after["email"])
	}
}

// 🔴 停用自己 = 当场把自己锁在门外，而 API 上没有 undelete，
// 那个邮箱也无法再次使用（索引不是部分索引）。恢复只能靠直接改库。
func TestDeleteAdminTxRefusesSelfDisable(t *testing.T) {
	q := &fakeDisableQuerier{target: dbgen.LockAdminAccountTargetRow{ID: 7, Email: "ops@babel.plus"}}
	_, err := deleteAdminTx(context.Background(), q, 7, 7, "ops@babel.plus", "手滑点错了这一下")
	if !errors.Is(err, errAdminSelfDelete) {
		t.Fatalf("err = %v，期望 errAdminSelfDelete", err)
	}
	if q.disableCalls != 0 {
		t.Fatal("自我停用竟然执行了")
	}
}

func TestDeleteAdminTxConfirmationMismatchDoesNotWrite(t *testing.T) {
	q := &fakeDisableQuerier{target: dbgen.LockAdminAccountTargetRow{ID: 9, Email: "peer@babel.plus"}}
	_, err := deleteAdminTx(context.Background(), q, 9, 7, "someone@babel.plus", "该同事已离职需要停用")
	if !errors.Is(err, errAdminConfirmationMismatch) {
		t.Fatalf("err = %v，期望 errAdminConfirmationMismatch", err)
	}
	if q.disableCalls != 0 {
		t.Fatal("确认串不匹配却停用了")
	}
}

// ============================================================
// 🔴 审计写失败 ⇒ 业务写入必须一起回滚（api-contract §6.3 第 1 条）
// ============================================================
//
// 这一组用真实的 `audit.InTx` + 真实的 sqlc 生成代码 + 一条假事务跑完整条链路。
// 假事务记录「审计写了几次、Commit 了几次、Rollback 了几次」——
// 审计写失败时 Commit 必须**一次都没发生**。
//
// 少了这条，「业务成功、审计缺失」就是一个静默的可能，
// 而一条查不到的管理操作在事后与「没发生过」不可区分。

// fakeAdminTx 内嵌 pgx.Tx（值为 nil）：只实现这条链路用得到的方法，
// 其余方法一旦被调用会 panic —— 那正是我们想要的信号，
// 说明代码开始依赖一条这里没有覆盖到的数据库能力。
type fakeAdminTx struct {
	pgx.Tx

	queries     []string
	auditWrites int
	failAudit   error
	commits     int
	rollbacks   int
	closed      bool
}

func (f *fakeAdminTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "INSERT INTO audit_logs") {
		f.auditWrites++
		if f.failAudit != nil {
			return pgconn.CommandTag{}, f.failAudit
		}
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (f *fakeAdminTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.queries = append(f.queries, sql)
	return fakeAdminRow{}
}

func (f *fakeAdminTx) Commit(context.Context) error {
	if f.closed {
		return pgx.ErrTxClosed
	}
	f.commits++
	f.closed = true
	return nil
}

func (f *fakeAdminTx) Rollback(context.Context) error {
	f.rollbacks++
	if f.closed {
		return pgx.ErrTxClosed
	}
	f.closed = true
	return nil
}

// fakeAdminRow 按目标指针的类型填一个可用的值。
//
// 未知类型时**报错而不是跳过**：这条链路上多出一列（查询被改了）时，
// 测试必须响亮地失败，而不是悄悄用零值继续跑下去。
type fakeAdminRow struct{}

func (fakeAdminRow) Scan(dest ...any) error {
	now := time.Now().UTC()
	for _, d := range dest {
		switch v := d.(type) {
		case *int64:
			*v = 42
		case *int32:
			*v = 3
		case *bool:
			*v = false
		case *string:
			// LockAdminUserTarget 的 email 就是 L1 的期望值。
			*v = testTargetEmail
		case **string:
			*v = nil
		case **int64:
			*v = nil
		case **int32:
			*v = nil
		case *pgtype.Timestamptz:
			*v = pgtype.Timestamptz{Time: now, Valid: true}
		case *pgtype.UUID:
			*v = pgtype.UUID{}
		default:
			return errors.New("fakeAdminRow 遇到没覆盖的目标类型，查询的列集变了？")
		}
	}
	return nil
}

type fakeAdminBeginner struct {
	tx     *fakeAdminTx
	begins int
}

func (b *fakeAdminBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	b.begins++
	return b.tx, nil
}

// runRevokeInTx 跑一次「D3 事务体 + 审计」，与 handler 里那一段逐字同形。
func runRevokeInTx(tx *fakeAdminTx) error {
	b := &fakeAdminBeginner{tx: tx}
	return audit.InTx(context.Background(), b, testAdminActor(),
		func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
			_, e, err := revokeAdminUserSubsTx(ctx, q, 42, testTargetEmail, "订阅链接被公开分享")
			return e, err
		})
}

func TestRevokeSubsCommitsWhenAuditSucceeds(t *testing.T) {
	// 先证明这套夹具在正常路径下确实会提交 —— 否则下面那条「不提交」的断言
	// 会因为「什么都没跑起来」而假通过。
	tx := &fakeAdminTx{}
	if err := runRevokeInTx(tx); err != nil {
		t.Fatalf("正常路径不该报错：%v", err)
	}
	if tx.auditWrites != 1 {
		t.Fatalf("审计写了 %d 次，期望 1 次", tx.auditWrites)
	}
	if tx.commits != 1 {
		t.Fatalf("Commit 了 %d 次，期望 1 次", tx.commits)
	}
	// 审计必须落在**同一个事务句柄**上：如果哪天有人把 Write 的第一个参数
	// 换成连接池，上面两条仍然会过，而 §6.3 第 1 条已经失效。
	if len(tx.queries) < 2 {
		t.Fatalf("业务写入没有落在这条事务上：%v", tx.queries)
	}
}

// 🔴 本文件最重要的一条。
func TestRevokeSubsRollsBackWhenAuditWriteFails(t *testing.T) {
	tx := &fakeAdminTx{failAudit: errors.New("audit_logs 写不进去")}
	err := runRevokeInTx(tx)
	if err == nil {
		t.Fatal("审计写失败时整个操作必须失败 —— 否则「业务成功、审计缺失」是一个静默的可能")
	}
	if tx.commits != 0 {
		t.Fatalf("审计写失败却 Commit 了 %d 次；业务写入必须一起回滚", tx.commits)
	}
	if tx.rollbacks == 0 {
		t.Fatal("审计写失败后没有回滚")
	}
	// 业务写入确实发生过（然后被回滚）—— 证明这条用例走的是「写完之后审计失败」
	// 那条路径，而不是「根本没走到写入」。
	if len(tx.queries) < 2 {
		t.Fatalf("业务写入没有发生，这条用例没有测到它想测的东西：%v", tx.queries)
	}
}

// 「忘了写审计」的现象必须是**业务操作失败**，不是「操作成功但没留痕」。
// 这里把 Entry 的必填字段清空，模拟一个漏填 action 的事务体。
func TestIncompleteAuditEntryRollsBack(t *testing.T) {
	tx := &fakeAdminTx{}
	b := &fakeAdminBeginner{tx: tx}
	err := audit.InTx(context.Background(), b, testAdminActor(),
		func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
			if _, err := q.AdminUnbanUser(ctx, 42); err != nil {
				return audit.Entry{}, err
			}
			return audit.Entry{}, nil // 忘了填 action / target
		})
	if err == nil {
		t.Fatal("审计条目不完整时整个事务必须失败")
	}
	if tx.commits != 0 {
		t.Fatalf("Commit 了 %d 次", tx.commits)
	}
}

// 缺来源 IP 的调用**连事务都不该开**：audit_logs.request_ip 是证据，
// 一条写着 0.0.0.0 的审计记录会在事后被当成真实来源读，而它其实什么都没说。
func TestAuditRejectsActorWithoutIP(t *testing.T) {
	tx := &fakeAdminTx{}
	b := &fakeAdminBeginner{tx: tx}
	err := audit.InTx(context.Background(), b,
		audit.Actor{AdminID: 7, Email: "ops@babel.plus"}, // 没有 IP
		func(context.Context, *dbgen.Queries) (audit.Entry, error) {
			t.Fatal("不该跑到业务写入")
			return audit.Entry{}, nil
		})
	if err == nil {
		t.Fatal("缺 IP 的 Actor 必须被拒绝")
	}
	if b.begins != 0 {
		t.Fatalf("开了 %d 次事务；缺 IP 时连事务都不该开", b.begins)
	}
}

// adminActor 这一侧的同一条规则：没挂 RequestBinding（拿不到原始请求）时，
// 管理面写操作必须失败，而不是写一条来源不明的审计。
func TestAdminActorRefusesWithoutRequestBinding(t *testing.T) {
	s := adminTestServer()
	ctx := middleware.WithAdmin(context.Background(), &middleware.AdminAuth{AdminID: 7, Email: "ops@babel.plus"})
	_, _, err := s.adminActor(ctx)
	if !errors.Is(err, errNoAuditableIP) {
		t.Fatalf("err = %v，期望 errNoAuditableIP", err)
	}
}

// 没挂管理面鉴权中间件是**装配错误**，必须以 500 暴露而不是伪装成 403：
// 后者会让人去查 IAP 配置，查错方向。
func TestAdminActorRefusesWithoutAdminIdentity(t *testing.T) {
	s := adminTestServer()
	_, _, err := s.adminActor(context.Background())
	if !errors.Is(err, errNoAdminAuth) {
		t.Fatalf("err = %v，期望 errNoAdminAuth", err)
	}
}

// ============================================================
// 视图映射
// ============================================================

// 列表说「已封禁」而详情说「正常」是后台里最难查的一类不一致 ——
// 它不报错，只是让人对着两个页面反复刷新。两个映射必须给出同形的投影。
func TestAdminUserViewsAgree(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expired := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	list := adminUserFromListRow(dbgen.ListAdminUsersPageRow{
		ID: 42, Email: testTargetEmail, Banned: true, CreatedAt: admTS(created),
		ExpiredAt: admTS(expired), GroupID: 3, PlanName: ptrOf("重度"),
		DeviceLimit: ptrOf(int32(5)), TransferEnable: 120, UploadBytes: 7,
		DownloadBytes: 8, BalanceAmount: 900, InvitedBy: ptrOf(int64(1)),
	})
	detail := adminUserFromDetailRow(dbgen.GetAdminUserDetailRow{
		ID: 42, Email: testTargetEmail, Banned: true, CreatedAt: admTS(created),
		ExpiredAt: admTS(expired), GroupID: 3, PlanName: ptrOf("重度"),
		DeviceLimit: ptrOf(int32(5)), TransferEnable: 120, UploadBytes: 7,
		DownloadBytes: 8, BalanceAmount: 900, InvitedBy: ptrOf(int64(1)),
	})
	a, _ := json.Marshal(list)
	b, _ := json.Marshal(detail)
	if string(a) != string(b) {
		t.Fatalf("列表与详情的投影不一致：\n列表 %s\n详情 %s", a, b)
	}
}

// ⚠️ uuid 是节点侧的连接凭据，契约的 AdminUser 里也没有它的位置。
// GetAdminUserDetail 查出它只是给 D1 的审计快照用的。
func TestAdminUserViewNeverLeaksUUID(t *testing.T) {
	u := pgtype.UUID{Valid: true}
	copy(u.Bytes[:], []byte{0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8})
	view := adminUserFromDetailRow(dbgen.GetAdminUserDetailRow{ID: 1, Email: testTargetEmail, Uuid: u})
	blob, _ := json.Marshal(view)
	if strings.Contains(string(blob), "6ba7b810") {
		t.Fatalf("AdminUser 响应里出现了 uuid：%s", blob)
	}
}

func TestAdminAccountViewReportsOnlyRealPermissions(t *testing.T) {
	acc := adminAccountView(9, "peer@babel.plus", middleware.RoleAdmin, false, true, true,
		pgtype.Timestamptz{}, admTS(time.Now()))
	if len(acc.Permissions) != 1 || acc.Permissions[0] != gen.AdminUserExport {
		t.Fatalf("permissions = %v，期望只有 admin.user.export", acc.Permissions)
	}
	if !acc.TotpEnabled {
		t.Error("totp_enabled 应当透传")
	}
	if acc.LastLoginAt != nil {
		t.Error("从没登录过时 last_login_at 必须是 null，不能是 0001-01-01")
	}
}

// ============================================================
// 小工具
// ============================================================

func assertNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		// handler 一律返回 (response, nil)：返回非 nil error 会让生成代码
		// 兜底成一个 code 不在契约枚举里的 500。
		t.Fatalf("handler 不该返回 error：%v", err)
	}
}
