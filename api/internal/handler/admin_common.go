package handler

import (
	"context"
	"errors"

	"github.com/oratis/babelplus/api/internal/middleware"
)

// 管理面四个 handler 文件（admin_users / admin_orders / admin_nodes / admin_catalog）
// 共用的部件。
//
// **为什么单独一个文件**：这四个文件是并行交付的，各自写了一份同义的
// 「reason 下限常量 / 装配错误哨兵 / step-up 配置组装」。同义的多份不会编译失败，
// 只会在将来某次只改了其中一份时，让四层强制（api-contract §6.2）在不同端点上
// 松紧不一 —— 而那种不一致没有任何测试会说出来，因为每一份自己都是自洽的。
// 收敛到一处之后，「§6.2 的下限是多少」在代码里只有一个答案。

// ============================================================
// L2：操作原因的长度下限
// ============================================================

// adminReasonMinRunes 是 §6.2 L2 的下限：reason ≥ 8 字符。
//
// 🔴 数的是**字符（rune）不是字节**。按字节数会让「补单」这种 6 字节以下的中文
// 直接被拒，而「aaaaaaaa」这种 8 字节的废话通过 —— 方向正好反了：
// 中文原因是这个系统里最常见、也最可能真的说清楚事情的那一种。
//
// 曾经这个 8 在四个文件里各有一份（adminReasonMinRunes / adminReasonMinLen /
// catalogReasonMinRunes / adminNodeReasonMinLen）。四份都是 8，所以没人发现问题；
// 危险的是**下一次调整**：改一份等于让管理面一半的端点开始接受更短的理由，
// 而审计日志里那些理由要到事后复盘时才有人读。
const adminReasonMinRunes = 8

// ============================================================
// 装配错误的哨兵
// ============================================================

// errNoAdminAuth 表示上下文里没有管理员身份 —— 这是**装配错误**，不是「未授权」。
//
// 必须冒成 500 而不是 403：未通过管理面鉴权的请求根本到不了 handler
// （AuthenticateAdmin 会挡在 403）。把装配错误伪装成权限问题会让
// 「管理面鉴权忘了挂」表现为「所有管理员都没权限」，
// 于是运维会去查 IAP 配置、查权限位，唯独不会怀疑路由。
var errNoAdminAuth = errors.New("管理员身份缺失：路由未挂载管理面鉴权中间件")

// ============================================================
// L3：TOTP step-up
// ============================================================

// adminStepUpVerifier 把 `mw.AdminAuthConfig` 收窄成 step-up 用到的那一个方法。
//
// 收窄的目的是单测能塞假实现：L3 的几条纪律（权限不足时**绝不**调 RequireStepUp、
// 500 不能被压成 403、code 必须原样透传）都要在不起数据库的情况下被测到。
type adminStepUpVerifier interface {
	RequireStepUp(ctx context.Context, code string) *middleware.AuthError
}

// adminAuthConfig 组装 step-up 所需的**最小**管理面鉴权配置。
//
// ⚠️ **这是一处已登记的装配偏离，收敛之后仍然存在。** 正确形状是 Server 上有一个
// 由 main.go 注入的 `mw.AdminAuthConfig` 字段；`RequireStepUp` 是它的方法
// （要解密 totp_secret_enc、要写 used_totp，也就是要 DB 句柄与密钥），
// 而 Server 目前只有 cfg / db / logger / limiter。
//
// 与 main.go 那一份在 step-up 这条路径上**等价**：RequireStepUp 只用到
// DB（查 admin_users 取 totp_secret_enc 与 disabled 状态）、Replay（used_totp 防重放）、
// TOTPKey、Logger 四项；IAPAudience 与 Keys 只服务于 IAP 断言校验，
// 那一层早在中间件里跑完了。这里**刻意不填**那两项 —— 填了会让人以为
// 这条路径也能拿来做鉴权。
//
// fail-closed 仍然成立：BP_ADMIN_TOTP_ENC_KEY 没配 → TOTPKey 为空 →
// RequireStepUp 直接返回 AUTH_TOTP_REQUIRED，危险操作**做不了**
// （不是「不需要 TOTP」）。TestAdminStepUpRefusesWithoutUsableCode 钉着这一条。
//
// 🔴 残余风险：配置仍有两个来源（这里与 main.go）。将来若给 AdminAuthConfig 加了
// 影响 step-up 的字段（换 Replay 后端、注入时钟），只改 main.go 不会波及这里，
// 现象是「危险操作的 TOTP 用的是另一套配置」。本轮把 handler 侧的三份合成一份，
// 把「两个来源」从四降到二；彻底消除要改 server.go 与 cmd/server/main.go 的装配。
// TODO(P1): Server 持有 mw.AdminAuthConfig 并由 main.go 注入，然后删掉本函数。
func (s *Server) adminAuthConfig() middleware.AdminAuthConfig {
	store := &middleware.PgAdminStore{DB: s.db.Pool}
	return middleware.AdminAuthConfig{
		DB:      store,
		Replay:  store,
		TOTPKey: s.cfg.AdminTOTPEncKey,
		Logger:  s.logger,
	}
}
