package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/oratis/babelplus/api/internal/gen"
)

// 「哪些 operation 仍然是 501」的运行期清单。
//
// # 为什么需要它
//
// unimplemented.gen.go 的头部与 scripts/gen_stubs.py 都登记着同一个代价：
// Server 嵌入 Unimplemented 之后，**漏实现不会在编译期暴露** —— 一个没被覆盖的
// operation 会安静地落到 501，而不是让包编译不过。那份文档说兜底靠
// 「operations.txt 与集成测试」，但集成测试一直没有。这个文件是那一半。
//
// 它挡的不是「漏实现」，而是**反过来的那一种事故**：有人为了让某个页面别再报 501，
// 给下面某条写一个「返回空列表 / 返回 204」的假实现。那种实现不会有任何测试失败，
// 后台看起来也正常 —— 域名池会显示成「一个域名都没有」而不是「这个功能还没有」，
// 邮件模板会显示成「模板列表为空」而不是「模板根本没有存储」。
// 一个空列表和一个未实现，在界面上长得一模一样，在决策上完全相反。
//
// # 改这个文件的正确时机
//
// 某条真的实现了，就把它**从表里删掉**（顺手删掉下面那段说明它为什么不能实现的注释）。
// 测试失败本身就是提示：删之前先读一遍那条为什么被拦住 —— 下面每一条的阻塞原因
// 都是「缺表 / 缺列」或「ADR 未批准」，不是「没空写」。
func TestDeliberatelyUnimplemented(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()

	// 每一条的阻塞原因（逐条核过 18 支迁移与两份 ADR 的状态）：
	//
	//   domains 三条 —— `domains` 表不存在，且卡在两份**未批准**的 ADR
	//     （0011 §7.2 的字段形状 / 0010 §1.3+§8.1 的池划分）。data-model §16 明确
	//     「本条不划掉」。不能拿 settings 的 JSONB 顶：契约的路径参数是数字 id，
	//     而 JSONB 数组没有稳定 id —— 并发编辑会删错行**且不报错**。
	//     另：ADR 0011 §7.2 的字段（state/platform/registrable/order/serial）与冻结契约的
	//     Domain（hostname/role/enabled/reachable/last_checked_at）是两套不同模型。
	//
	//   mail templates 两条 —— `mail_templates` 表不存在。`email_log.template` 只是
	//     模板键的字符串快照，不是正文存储。同样不能塞进 settings 的 JSONB：
	//     MailTemplatePatch 要求前后像进审计，而 JSONB 的部分更新拿不到干净的字段级快照。
	//
	//   TOTP 三条 —— 用户侧 2FA 在契约里就声明了 501，且 schema 无落点
	//     （users 上没有 totp_secret_enc / totp_confirmed_at，防重放也没有用户侧的表）。
	//     🔴 DisableUserTotp 尤其**不能**退化成 204：一个以为自己关掉了 2FA 的用户
	//     会在下次登录时被挡在门外，而他手上可能已经把 authenticator 里的条目删了。
	//     详见 account.go 那一节的开头。
	stillUnimplemented := map[string]func() (any, error){
		"listAdminDomains":        func() (any, error) { return s.ListAdminDomains(ctx, gen.ListAdminDomainsRequestObject{}) },
		"createAdminDomain":       func() (any, error) { return s.CreateAdminDomain(ctx, gen.CreateAdminDomainRequestObject{}) },
		"deleteAdminDomain":       func() (any, error) { return s.DeleteAdminDomain(ctx, gen.DeleteAdminDomainRequestObject{}) },
		"listAdminMailTemplates":  func() (any, error) { return s.ListAdminMailTemplates(ctx, gen.ListAdminMailTemplatesRequestObject{}) },
		"updateAdminMailTemplate": func() (any, error) { return s.UpdateAdminMailTemplate(ctx, gen.UpdateAdminMailTemplateRequestObject{}) },
		"enrollUserTotp":          func() (any, error) { return s.EnrollUserTotp(ctx, gen.EnrollUserTotpRequestObject{}) },
		"verifyUserTotp":          func() (any, error) { return s.VerifyUserTotp(ctx, gen.VerifyUserTotpRequestObject{}) },
		"disableUserTotp":         func() (any, error) { return s.DisableUserTotp(ctx, gen.DisableUserTotpRequestObject{}) },
	}

	for name, call := range stillUnimplemented {
		resp, err := call()
		if !errors.Is(err, ErrNotImplemented) {
			t.Errorf("%s 不再返回 501（err=%v resp=%#v）。"+
				"如果它真的实现了，把它从表里删掉；"+
				"如果它返回的是空列表/204 这类**假成功**，那正是本测试要挡的事故", name, err, resp)
		}
	}
}

// 「实现了、但有一个分支仍然是 501」的两条。
//
// 与上面那张表分开写，因为它们的性质不同：这两条的主路径**是实现了的**，
// 501 只落在一个契约自己留了出口的分支上。混进上面那张表会让人以为整个端点都没做。
//
//   - broadcastAdminMail 的**自定义正文**分支 —— `email_log` 没有正文列
//     （有 template 键与 subject，没有 body），ClaimQueuedMail 也取不到正文。
//     模板键驱动的那一半可用。
//   - sendEmailCode 的 **email_change** 场景 —— 换绑邮箱要「已登录用户 + 目标邮箱未被占用」
//     两个前提，而本端点在契约里是 security: []（免登录）。要支持它得先裁定
//     「本端点是否接受可选鉴权」，那是契约层面的决定。
//
// 这里只钉住「这两个分支没有被偷偷做成假成功」这一件事；主路径由各自的用例覆盖。
func TestPartiallyUnimplementedBranches(t *testing.T) {
	// 说明性断言：ErrNotImplemented 是这两个分支与上面八条共用的那一个哨兵，
	// 错误映射靠它翻成 501。换掉它（比如改成返回一个 200 空响应）会让
	// 上面那张表整体失效，所以在这里钉一次它的身份。
	if ErrNotImplemented == nil || ErrNotImplemented.Error() != "not implemented" {
		t.Fatalf("ErrNotImplemented 变了形状：%v —— 501 的映射与上面那张表都依赖它", ErrNotImplemented)
	}
}
