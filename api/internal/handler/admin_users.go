package handler

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/audit"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 管理面：用户（模块 2）与管理员账号（模块 11）。
//
// 12 个 operation：listAdminUsers · getAdminUser · updateAdminUser(D1) ·
// banAdminUser / unbanAdminUser(D2) · revokeAdminUserSubscriptions(D3) ·
// adjustAdminUserBalance(D10) · exportAdminUsers(D14) ·
// listAdmins · createAdmin(D15) · resetAdminTotp(D15) · deleteAdmin(D16)。
//
// ============================================================
// 🔴 读本文件任何一行之前必须先接受的四件事
// ============================================================
//
// **一、四层强制（api-contract §6.2）全部在服务端，一层都不能挪到前端。**
//
//	L1 确认串由服务端**自己查出**期望值再常数时间比对（拿请求体里的 email
//	去比对它自己恒等于通过）；L2 原因 ≥ 8 字符；L3 TOTP 走
//	middleware.AdminAuthConfig.RequireStepUp（含 used_totp 防重放）；
//	L4 独立权限位。前端的确认弹窗对一个直接 `curl` 的人是零。
//
// **二、审计与业务写入同一事务**（§6.3 第 1 条）。本文件所有写操作一律走
//
//	`audit.InTx`，一次都没有「先提交业务再补审计」。唯一的例外是 exportAdminUsers ——
//	它是纯读，没有业务事务可搭，走 `audit.Write`（audit 包为此保留了导出）。
//
// **三、users 永不硬删，admin_users 是软停用，两件事只是名字里都有「删」字。**
//
//	`deleteAdmin` 写的是 `admin_users.disabled_at`（DisableAdminAccount），
//	它**不碰 users 一行**。用户侧的「删除账号」是 users.sql 的 AnonymizeUser，
//	不在本文件里，本文件也没有任何一条通往它的路径。
//	写反的后果不对称：把 deleteAdmin 写成硬删会让这个人过去的每一条 D1–D16 审计
//	的 admin_user_id 变成 NULL（audit_logs 的外键是 ON DELETE SET NULL），
//	而 openapi 的 AuditLogEntry 把 admin_id 列进了 required 又没有 admin_email 字段，
//	那些记录在 API 上会变成认不出人的孤儿。
//
// **四、三处「实现不出来」的地方一律响亮地拒绝，绝不假装成功。**
//
//	· D10 缺 `expense:admin_adjust` 科目 → **503**（不是 500，理由见 adjustBalanceRetryAfter）。
//	  本轮同批的 0019 已经补了那一行；这道闸留着是因为迁移的执行模型没有版本表，
//	  「某个环境漏灌了」是真实可能的状态；
//	· createAdmin 的 permissions 里五个 `admin.*.write` 在库里没有列 → **422**；
//	· 导出命中行数上限 → **422 拒绝**，不发一份会被当成完整名单的半份 CSV。
//
// ============================================================
// 🔴 createAdmin 造出来的管理员**登不进去** —— 正确开户是两步
// ============================================================
//
// `admin_users.totp_confirmed_at` 是 NOT NULL，也就是说数据库里**不存在**
// 「已创建但还没绑 2FA」这个状态，secret 在 INSERT 的那一刻就必须有值；
// 而 createAdmin 的 201 响应体是 `AdminAccount`，**没有 TotpEnrollment** ——
// 于是那串明文 secret 无处可去，只能就地丢弃。
//
// 唯一能拿到绑定材料的端点是 `resetAdminTotp`（它的响应才是 TotpEnrollment）。
// 所以正确的开户流程是**两步**：
//
//	1. POST /api/v1/admin/admins            → 拿到新管理员的 id
//	2. POST /api/v1/admin/admins/{id}/reset-totp → 拿到二维码/secret，当面交给本人
//
// 少了第 2 步，那个人**永远进不来**，而现场唯一的「解法」是直接改库 ——
// 那正是权限系统存在的意义被绕过的那一刻。
// 本实现为此做了三件事（都在 CreateAdmin 里）：201 响应带 `X-Next-Step` 与
// `Warning` 头指向 reset-totp、日志里写一条固定文案的 WARN、
// 审计条目的 after 里带 `needs_totp_enrollment: true`。
// 真正的修法是给 createAdmin 的响应加 TotpEnrollment，那要改已冻结的 openapi。
//
// ============================================================
// ⚠️ 与冻结契约的四处偏差（已在交付说明里登记）
// ============================================================
//
//  1. **adjustAdminUserBalance 会发 503**，而 openapi 只给它声明了 403/404/422/500。
//     同 wallet.go 的 transferCommission503JSONResponse，理由一致：500 会让管理员
//     以为是偶发故障并反复重试，而每次重试都是一条 D10 审计。
//  2. **createAdmin 的邮箱冲突映射成 422 而不是 409** —— 契约给这个端点
//     没有声明 409（responses 只有 201/403/422/500）。
//  3. **exportAdminUsers 的 CSV 走 `download_url` 的 data: URI**。没有 export_jobs 表、
//     没有 GCS 签名 URL（十八支迁移逐张核过），而契约要求返回一个异步任务句柄。
//     不为了「像异步」返回 queued 再让前端轮询一个不存在的资源。
//  4. **banAdminUser 的 200 多带两个响应头**（生效延迟）。契约的 AdminUser 里
//     没有可以放这句话的字段，而「最长 60 秒后生效」漏说的后果是管理员
//     30 秒后看到人还在线、再点一次。
//
// 四处都不触发 contract-drift 门禁（那条门禁比对的是 openapi.yaml 的字节，
// 本文件一个字都没改它），所以只有这段注释与交付说明会记得它们。

// ============================================================
// 四层强制的公共部件
// ============================================================

// L2 的下限 adminReasonMinRunes 在 admin_common.go（管理面四个文件共用一份）。

// validAdminReason 校验并归一化 L2 的 reason。
//
// 先 TrimSpace 再数长度：一串空格能凑够任何长度下限，而它写进审计等于没写。
// 返回归一化后的串，调用方**必须**用它而不是原串写审计 ——
// 否则审计里会留下带首尾空白的原因，事后按 reason 分组统计时它们各成一类。
func validAdminReason(raw string) (string, bool) {
	r := strings.TrimSpace(raw)
	return r, utf8.RuneCountInString(r) >= adminReasonMinRunes
}

// confirmationMatches 是 L1 的比对。
//
// 🔴 **常数时间比较**（api-contract §6.2 逐字要求）。这里泄漏的不是密码而是
// 「你猜的这个串对了几个前缀字符」—— 而期望值是目标用户的邮箱，
// 一个能按字节探测邮箱的接口等于一台账号枚举机。
// 长度不同时 ConstantTimeCompare 直接返回 0（它自己就先比长度），
// 这一位泄漏是无法避免的，也无所谓：邮箱长度不是秘密。
//
// ⚠️ **两侧都 TrimSpace 但不改大小写。** 期望值取自 `users.email` / `admin_users.email`
// 那一份原样大小写；管理员是从后台页面上把它**复制**过来的，多一个尾随空格
// 是复制粘贴的常态，而大小写不同说明他是**手打**的 —— 那正是这一层要求确认的动作
// 本身（照着念一遍目标是谁），不该被静默宽容掉。
func confirmationMatches(expect, got string) bool {
	e := strings.TrimSpace(expect)
	g := strings.TrimSpace(got)
	if e == "" {
		// 🔴 期望值为空时**一律不通过**。没有这一行，
		// `ConstantTimeCompare("", "")` 会返回 1 —— 于是任何一条让期望值变成空串的路径
		// （查询漏选了 email 列、将来某个匿名化过的行）都会把 L1 变成
		// 「只要 confirmation 也留空就放行」。这是那种只在别处出错之后才显形的洞。
		return false
	}
	return subtle.ConstantTimeCompare([]byte(e), []byte(g)) == 1
}

// 装配错误的哨兵。它与 admin_common.go 的 errNoAdminAuth 一样必须冒出 500 而不是 403：
// 把装配错误伪装成权限问题会让「管理面鉴权忘了挂」表现为「所有管理员都没权限」，
// 而日志里一条异常都没有。
var errNoAuditableIP = errors.New("采集不到来源 IP，管理面写操作一律拒绝")

// adminActor 取出「谁在操作」，并组装审计用的 Actor。
//
// 🔴 **采集不到 IP 时整条操作失败，不回退到 0.0.0.0。** audit 包已经在
// validateActor 里挡了一道，这里提前挡是为了让业务写入**根本不发生**：
// 否则那次写会跑完再被回滚，白付一次代价。
// 与 auth.go 的 requestMetadata 刻意不同 —— 那边拿不到 IP 只记一条 WARN 然后继续
// （注册/登录本身不依赖 IP），而这张表是**证据**：一条写着 0.0.0.0 的审计记录
// 会在事后被当成真实来源读，而它其实什么都没说。
//
// Email 取 `admin_users` 那一份（mw.AdminAuth.Email 已经保证了这一点），
// 不是 IAP 断言里那一份：审计要留的证据是「本系统认为他是谁」。
func (s *Server) adminActor(ctx context.Context) (*middleware.AdminAuth, audit.Actor, error) {
	admin, ok := middleware.AdminFrom(ctx)
	if !ok || admin == nil {
		s.logger.ErrorContext(ctx, "管理面 handler 在没有管理员身份的上下文里被调用")
		return nil, audit.Actor{}, errNoAdminAuth
	}
	meta := s.requestMetadata(ctx)
	if meta.IP == nil {
		s.logger.ErrorContext(ctx,
			"bp_admin_audit_no_ip 采集不到来源 IP，本次管理面写操作被拒绝（未挂载 handler.RequestBinding()？）",
			"admin_id", admin.AdminID)
		return admin, audit.Actor{}, errNoAuditableIP
	}
	return admin, audit.Actor{
		AdminID:   admin.AdminID,
		Email:     admin.Email,
		IP:        *meta.IP,
		UserAgent: derefOr(meta.UserAgent, ""),
	}, nil
}

// adminStepUp 跑一次 L3。两个返回值最多有一个非 nil；都是 nil = 通过。
//
// 配置来自 admin_common.go 的 adminAuthConfig()（管理面四个文件共用一份组装）——
// 那里也记着「为什么是现场组装而不是 Server 上的字段」以及残余的漂移风险。
func (s *Server) adminStepUp(ctx context.Context, code string) (*gen.ErrForbiddenJSONResponse, *gen.ErrInternalJSONResponse) {
	authErr := s.adminAuthConfig().RequireStepUp(ctx, strings.TrimSpace(code))
	if authErr == nil {
		return nil, nil
	}
	if authErr.Status == http.StatusInternalServerError {
		// 装配错误 / used_totp 写不进去。**不能压成 403** ——
		// 那会让「防重放表挂了」看起来像「你的验证码不对」，管理员会一直重输。
		e := s.internalErr(ctx, "二次验证不可用", errors.New(authErr.Error()))
		return nil, &e
	}
	// 403：AUTH_TOTP_REQUIRED（没带头）或 AUTH_TOTP_INVALID（错码/重放）。
	// 两个码必须分开：前端拿到 REQUIRED 才知道要弹输入框，拿到 INVALID 是「重来」。
	f := s.forbidden(ctx, gen.ErrorCode(authErr.Code), authErr.Message)
	return &f, nil
}

// ============================================================
// L4：权限位与契约枚举的对应关系
// ============================================================
//
// 🔴 **契约与 schema 在权限模型上对不上，这是本组最大的一处缺口。**
//
//	openapi 的 `AdminPermission` 是 7 个枚举：
//	  admin.order.mark_paid · admin.user.export · admin.user.write · admin.node.write ·
//	  admin.plan.write · admin.ticket.write · admin.settings.write
//	而 `admin_users`（0002:51）上只有 4 个 boolean 列 + 一个 role：
//	  perm_mark_order_paid(D6) · perm_refund(D7) · perm_adjust_balance(D10) · perm_export_csv(D14)
//
//	对得上的只有两个：
//	  admin.order.mark_paid ↔ perm_mark_order_paid
//	  admin.user.export     ↔ perm_export_csv
//	· `perm_refund`(D7) 与 `perm_adjust_balance`(D10) 在契约里**没有对应枚举值** ——
//	  两个直接动钱的权限位通过 API 看不见也授不了（只能改库）。
//	· `admin.*.write` 那五个在库里**没有列**。
//
// 🔴 **响应里只列那两个对得上的，五个 `admin.*.write` 一个都不列。**
//
//	曾经考虑过「由 role 推出五个虚拟权限位一起返回」，这是错的：
//	服务端**没有任何一处**会去检查它们，返回它们等于告诉后台
//	「这个 support 没有 admin.user.write」，而他其实照样能改用户。
//	一个没人检查的权限位出现在响应里，比它不出现更危险 ——
//	前端会照着它画禁用态，于是「看起来管住了」而实际没有。
//	代价：AdminAccount.permissions 会比契约的枚举窄。这是**诚实的窄**。

// adminPermissionsView 把两个真实存在的列映射成契约的 permissions 数组。
//
// 返回值恒非 nil（契约里 permissions 是 required）：空数组序列化成 `[]`，
// 而 nil slice 会变成 `null` —— 前端 `perms.includes(...)` 会直接抛异常。
func adminPermissionsView(markOrderPaid, exportCSV bool) []gen.AdminPermission {
	out := make([]gen.AdminPermission, 0, 2)
	if markOrderPaid {
		out = append(out, gen.AdminOrderMarkPaid)
	}
	if exportCSV {
		out = append(out, gen.AdminUserExport)
	}
	return out
}

// errAdminPermUngrantable 是 createAdmin 收到一个授不了的权限位。
var errAdminPermUngrantable = errors.New("该权限位无法通过 API 授予")

// adminPermissionGrant 把请求里的 permissions 翻成两个 boolean 列。
//
// 三类输入，三种结果，**没有第四种「先假装成功以后再补」**：
//
//	admin.user.export      → perm_export_csv = true。这是唯一真正能授的一个。
//	admin.order.mark_paid  → **422**。不是因为没有列（它有列），而是因为
//	                         ADR 0012 §16.3 逐字裁决：「在这个带外 sink 被端到端
//	                         验证通过之前，perm_mark_order_paid 对**所有**管理员
//	                         保持 false，即 D6 不可用」，而 sink 至今没有验证通过。
//	                         服务端没有任何配置能表达「sink 已验证」，所以只能 fail-closed。
//	                         真要开它，走 §20 第 9 步：验证 sink → DBA 改库 → 回来删这一支。
//	五个 admin.*.write      → **422**，message 说明「该权限位由角色决定，库里没有对应列」。
//
// 🔴 绝不要对不认识的枚举值静默忽略然后返回 201。一个「返回了 201 但没有真正授予」
//
//	的权限接口，会让人以为某个管理员没有 D6 权限，而他其实有（或反过来）——
//	而这两个方向的错都只会在出事之后才被发现。
func adminPermissionGrant(reqs *[]gen.AdminPermission) (markOrderPaid, exportCSV bool, badPerm gen.AdminPermission, reason string, err error) {
	if reqs == nil {
		return false, false, "", "", nil
	}
	for _, p := range *reqs {
		switch p {
		case gen.AdminUserExport:
			exportCSV = true
		case gen.AdminOrderMarkPaid:
			return false, false, p,
				"D6（手工标记订单已支付）的带外留痕 sink 尚未端到端验证，ADR 0012 §16.3 裁决在那之前 perm_mark_order_paid 对所有管理员保持关闭；请先完成验证再由 DBA 开启",
				errAdminPermUngrantable
		case gen.AdminUserWrite, gen.AdminNodeWrite, gen.AdminPlanWrite,
			gen.AdminTicketWrite, gen.AdminSettingsWrite:
			return false, false, p,
				"该权限位在 admin_users 上没有对应列，由角色（owner/admin/support）决定，无法通过本接口授予",
				errAdminPermUngrantable
		default:
			return false, false, p, "不是契约 AdminPermission 枚举里的值", errAdminPermUngrantable
		}
	}
	return markOrderPaid, exportCSV, "", "", nil
}

// requireOwnerRole 是 D15 / D16 的角色闸。
//
// ⚠️ **这是一处本文件自己下的裁决，docs 里没有 role → 能力的映射表。**
//
//	理由：管理员账号管理是权限系统的**根**（能新建管理员的人就能给自己造一个
//	更高权限的账号，能重置别人 TOTP 的人就能拿到别人的钥匙）。
//	它没有任何 schema 列可以表达，`role` 是唯一可用的信号，
//	而在「谁能碰权限系统本身」这个问题上，把范围收到最小是唯一安全的默认。
//	D1/D2/D3 **刻意不加**这道闸：§6.2 的 L4 只点名了 D6 与 D14，
//	给客服加一道文档里没写过的拒绝，会让「support 封不了滥用者」成为一个没人预料的故障。
//
// ⚠️ 代价：`AdminAccount` 上**没有 role 字段**，所以这道闸在 API 上是不可见的 ——
//
//	一个 support 只能从 403 的文案里知道为什么。已登记为契约缺口。
func requireOwnerRole(a *middleware.AdminAuth) bool {
	return a != nil && a.Role == middleware.RoleOwner
}

// ============================================================
// 视图映射
// ============================================================

// adminUserFromListRow / adminUserFromDetailRow 把两种行映射成同一个 AdminUser。
//
// 🔴 **两个函数必须给出同形的投影。** 列表说「已封禁」而详情说「正常」是后台里
// 最难查的一类不一致 —— 它不报错，只是让人对着两个页面反复刷新。
// 两者的字段赋值顺序刻意写成一模一样，改一个必须照着改另一个。
//
// ⚠️ **uuid 不进响应。** 契约的 AdminUser 里没有这个字段，而 uuid 是节点侧的
// 连接凭据（拿到它 + 节点地址就能直接连）。GetAdminUserDetail 查出它是给
// D1 的审计快照用的，不是给响应用的。
func adminUserFromListRow(r dbgen.ListAdminUsersPageRow) gen.AdminUser {
	groupID := r.GroupID
	return gen.AdminUser{
		Id:                  r.ID,
		Email:               openapi_types.Email(r.Email),
		Banned:              r.Banned,
		CreatedAt:           ttime(r.CreatedAt),
		ExpiredAt:           tptr(r.ExpiredAt),
		SubRevokedAt:        tptr(r.SubRevokedAt),
		GroupId:             &groupID,
		PlanName:            r.PlanName,
		DeviceLimit:         r.DeviceLimit,
		TransferEnableBytes: ptrOf(r.TransferEnable),
		UploadBytes:         ptrOf(r.UploadBytes),
		DownloadBytes:       ptrOf(r.DownloadBytes),
		BalanceAmount:       ptrOf(r.BalanceAmount),
		InvitedByUserId:     r.InvitedBy,
	}
}

func adminUserFromDetailRow(r dbgen.GetAdminUserDetailRow) gen.AdminUser {
	groupID := r.GroupID
	return gen.AdminUser{
		Id:                  r.ID,
		Email:               openapi_types.Email(r.Email),
		Banned:              r.Banned,
		CreatedAt:           ttime(r.CreatedAt),
		ExpiredAt:           tptr(r.ExpiredAt),
		SubRevokedAt:        tptr(r.SubRevokedAt),
		GroupId:             &groupID,
		PlanName:            r.PlanName,
		DeviceLimit:         r.DeviceLimit,
		TransferEnableBytes: ptrOf(r.TransferEnable),
		UploadBytes:         ptrOf(r.UploadBytes),
		DownloadBytes:       ptrOf(r.DownloadBytes),
		BalanceAmount:       ptrOf(r.BalanceAmount),
		InvitedByUserId:     r.InvitedBy,
	}
}

// adminAccountView 把 admin_users 的一行映射成契约的 AdminAccount。
//
// ⚠️ `role` 与 `disabled_at` 都**发不出去**（契约的 AdminAccount 上没有字段）。
// role 的缺席意味着 requireOwnerRole 那道闸在前端是不可见的；
// disabled_at 的缺席意味着停用的管理员一旦被列出来，与在职的长得一模一样 ——
// 所以 ListAdmins 只列在职的（见那里的注释）。两者都已登记为契约缺口。
func adminAccountView(id int64, email, role string, markPaid, exportCSV, totpEnabled bool,
	lastLoginAt, createdAt pgtype.Timestamptz) gen.AdminAccount {
	_ = role // 契约里没有落点；留参数是为了让调用点显式面对这件事，而不是忘了它存在
	return gen.AdminAccount{
		Id:          id,
		Email:       openapi_types.Email(email),
		Permissions: adminPermissionsView(markPaid, exportCSV),
		TotpEnabled: totpEnabled,
		LastLoginAt: tptr(lastLoginAt),
		CreatedAt:   ttime(createdAt),
	}
}

// ============================================================
// listAdminUsers
// ============================================================

// escapeLikePattern 转义 LIKE/ILIKE 的三个元字符。
//
// 🔴 **不转义的后果不是「搜不到」而是「搜出全部」。** 一个只输入 `%` 的搜索框
// 会让这个端点返回全部用户，而它的下游是 D14 导出的同一批数据 ——
// 于是一个连 `admin.user.export` 权限位都没有的人，用搜索框就能把用户名单翻完。
// `_` 同理（单字符通配）。`\` 必须**第一个**替换，否则会把后面刚插入的转义反斜杠再转义一次。
//
// Postgres 的 LIKE 默认转义字符就是 `\`（不受 standard_conforming_strings 影响，
// 那条只管字符串字面量），所以这里不需要额外的 `ESCAPE` 子句。
func escapeLikePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(q)
}

// adminUserSearchFilter 把 `?q=` 归一化成 SQL 的 email_like 参数。
// 空/全空白 → nil（不加这一条筛选），而不是 `%%`：后者是一次无意义的全表 ILIKE。
func adminUserSearchFilter(q *gen.SearchQuery) *string {
	if q == nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(*q))
	if trimmed == "" {
		return nil
	}
	return ptrOf("%" + escapeLikePattern(trimmed) + "%")
}

// ListAdminUsers 实现 GET /api/v1/admin/users。
//
// ⚠️ **列表与 `?count=true` 的筛选条件必须逐字同形**（两条查询的 WHERE 已经写成同形，
// 这里传的两组参数也必须同形）。漂移的现象是「分页器说共 87 条，翻到底只有 71 条」，
// 没有任何报错，也没有任何机制会发现。
//
// ⚠️ 游标解不开时**从第一页开始并记一条 WARN**，不是 400 ——
// 契约给这个端点只声明了 403/500，没有 400 可用（同 wallet.go 的 ListWalletTransactions）。
func (s *Server) ListAdminUsers(ctx context.Context, req gen.ListAdminUsersRequestObject) (gen.ListAdminUsersResponseObject, error) {
	want, page := pageLimit(req.Params.Limit)
	emailLike := adminUserSearchFilter(req.Params.Q)

	arg := dbgen.ListAdminUsersPageParams{EmailLike: emailLike, PageLimit: page}
	if req.Params.Cursor != nil && *req.Params.Cursor != "" {
		if c, valid := decodePageCursor(string(*req.Params.Cursor)); valid {
			// 只用 id 那一半：users.id 是 IDENTITY 列，单键即全序，破平手键是多余的。
			// at 那一半仍然要求存在且是时间 —— 契约要求「服务端必须校验解出的字段类型」，
			// 而校验的方式就是解不出来就不用它。
			id := c.ID
			arg.CursorID = &id
		} else {
			s.logger.WarnContext(ctx, "管理面用户列表的游标无法解析，已从首页开始",
				"cursor_len", len(*req.Params.Cursor))
		}
	}

	rows, err := s.db.ListAdminUsersPage(ctx, arg)
	if err != nil {
		return gen.ListAdminUsers500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询用户列表失败", err),
		}, nil
	}

	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.AdminUser, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminUserFromListRow(r))
	}

	meta := s.meta(ctx)
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		c := encodePageCursor(last.ID, ttime(last.CreatedAt))
		meta.NextCursor = &c
	}
	if req.Params.Count != nil && bool(*req.Params.Count) {
		total, err := s.db.CountAdminUsersPage(ctx, dbgen.CountAdminUsersPageParams{
			// 与上面 arg 的三个筛选逐字同形。**这里少传一个就是「共 N 条」与列表说的是两件事。**
			// 刻意不带 CursorID：总数是全集的总数，不是「游标之后还剩几条」。
			EmailLike: emailLike,
		})
		if err != nil {
			// 🔴 计数失败**不让整页失败**：列表已经查出来了，用户要的是那份数据。
			// 少一个「共 N 条」是可见的降级，整页 500 是一次故障。
			s.logger.ErrorContext(ctx, "统计用户总数失败，本次响应不带 meta.total", "err", err)
		} else {
			meta.Total = &total
		}
	}
	return gen.ListAdminUsers200JSONResponse{Data: out, Meta: meta}, nil
}

// ============================================================
// getAdminUser
// ============================================================

// GetAdminUser 实现 GET /api/v1/admin/users/{id}。
//
// ⚠️ 已注销（deleted_at 非空）的用户在这里是 404 —— GetAdminUserDetail 的 WHERE
// 带 `deleted_at IS NULL`。这是对的：注销之后 users 上剩下的是匿名化过的壳，
// 把它当成一个用户展示出来只会让人以为「这个人还在」。
func (s *Server) GetAdminUser(ctx context.Context, req gen.GetAdminUserRequestObject) (gen.GetAdminUserResponseObject, error) {
	row, err := s.db.GetAdminUserDetail(ctx, req.Id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.GetAdminUser404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "用户不存在")}, nil
		}
		return gen.GetAdminUser500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询用户详情失败", err),
		}, nil
	}
	return gen.GetAdminUser200JSONResponse{Data: adminUserFromDetailRow(row), Meta: s.meta(ctx)}, nil
}

// ============================================================
// updateAdminUser（D1：L2 必填原因）
// ============================================================

// adminUserPatchQuerier 是 D1 事务体需要的最小数据库能力。
// 收窄成接口而不是吃 *dbgen.Queries：单测能塞假实现，不必起真库。
type adminUserPatchQuerier interface {
	UpdateAdminUserEntitlement(ctx context.Context, arg dbgen.UpdateAdminUserEntitlementParams) (dbgen.UpdateAdminUserEntitlementRow, error)
	GetAdminUserDetail(ctx context.Context, userID int64) (dbgen.GetAdminUserDetailRow, error)
}

// errAdminPatchEmpty：PATCH 里一个可改字段都没有。
var errAdminPatchEmpty = errors.New("没有任何字段需要修改")

// buildUserEntitlementParams 把契约的 AdminUserPatch 翻成 SQL 参数。纯函数。
//
// 🔴 **`transfer_enable_bytes` 传给 `transfer_enable_total`，不是传给 plan 分量。**
//
//	契约给的是用户看到的**总额**，而 `users.transfer_enable` 是生成列
//	（GENERATED ALWAYS AS (_plan + _pack) STORED，0016:66），赋值在运行时才炸。
//	SQL 内部算 `_plan = 总额 − 当前 _pack`，于是加油包分量（用户买过的东西）
//	原封不动，而总额精确等于请求值。这个减法必须在 SQL 里做才是原子的 ——
//	handler 先读再算会在两次调用之间被一次加油包购买插进来，结果是把刚买的包吃掉。
//
// ⚠️ **「置空」表达不出来**（契约缺口）：`expired_at` 与 `device_limit` 的 NULL
//
//	都是有意义的值（不限时 / 不限设备），而 JSON 的「字段缺席」与「字段为 null」
//	在 oapi-codegen 生成的 `*T` 上都是 nil，所以只能理解成「不改」。
//	后果：管理员**无法通过 API 把一个用户改成不限时**。补它要在 openapi 里加
//	`clear_expired_at` 之类的显式字段，而 openapi 已冻结。
//
// 第二个返回值是 uuid 解析失败的字段名（空串 = 没问题）。
func buildUserEntitlementParams(userID int64, p gen.AdminUserPatch) (dbgen.UpdateAdminUserEntitlementParams, string, error) {
	arg := dbgen.UpdateAdminUserEntitlementParams{
		UserID:              userID,
		DeviceLimit:         p.DeviceLimit,
		GroupID:             p.GroupId,
		TransferEnableTotal: p.TransferEnableBytes,
	}
	changed := p.DeviceLimit != nil || p.GroupId != nil || p.TransferEnableBytes != nil
	if p.ExpiredAt != nil {
		arg.ExpiredAt = tstz(*p.ExpiredAt)
		changed = true
	}
	if p.Uuid != nil {
		// pgtype.UUID.Scan 认标准文本形态。解不出来必须 422 而不是「当成不改」：
		// 静默忽略一个写错的 uuid 会让管理员以为换过了，而节点侧那把旧钥匙还能连。
		var u pgtype.UUID
		if err := u.Scan(strings.TrimSpace(*p.Uuid)); err != nil {
			return arg, "uuid", err
		}
		arg.NewUuid = u
		changed = true
	}
	if !changed {
		// 🔴 一个字段都没带的 PATCH 直接拒绝，不走事务。
		// 它在 SQL 里是一次 coalesce 全命中的空更新，会成功、会 bump updated_at、
		// 并且**会写一条 before == after 的 D1 审计**。D1 的审计记录是排查
		// 「谁把这个人的配额改了」时唯一的线索，往里灌空记录等于稀释它。
		return arg, "", errAdminPatchEmpty
	}
	return arg, "", nil
}

// updateAdminUserTx 是 D1 的事务体：改权利 + 取改后的完整视图 + 组审计条目。
//
// 顺序是被依赖的：UpdateAdminUserEntitlement 一条语句同时给出 before/after
// （同一条语句里数据修改型 CTE 互相看不到对方的效果，所以 before 必然是这次
// UPDATE 之前的快照，中间**不存在**任何时刻能让第三方插进来）；
// 之后再读一次 GetAdminUserDetail 只是为了拼 200 响应体 —— 它在**同一事务**里，
// 所以响应里的数字与刚写进去的那一份是同一个快照。
func updateAdminUserTx(ctx context.Context, q adminUserPatchQuerier, arg dbgen.UpdateAdminUserEntitlementParams, reason string) (gen.AdminUser, audit.Entry, error) {
	row, err := q.UpdateAdminUserEntitlement(ctx, arg)
	if err != nil {
		return gen.AdminUser{}, audit.Entry{}, err
	}
	detail, err := q.GetAdminUserDetail(ctx, arg.UserID)
	if err != nil {
		return gen.AdminUser{}, audit.Entry{}, err
	}
	entry := audit.Entry{
		Action:     "D1.user.update",
		TargetType: "user",
		TargetID:   strconv.FormatInt(row.ID, 10),
		Reason:     reason,
		Before: map[string]any{
			"uuid":                 uuidText(row.BeforeUuid),
			"group_id":             row.BeforeGroupID,
			"expired_at":           tptr(row.BeforeExpiredAt),
			"device_limit":         row.BeforeDeviceLimit,
			"transfer_enable_plan": row.BeforeTransferEnablePlan,
			"transfer_enable_pack": row.BeforeTransferEnablePack,
			"transfer_enable":      row.BeforeTransferEnable,
		},
		After: map[string]any{
			"uuid":                 uuidText(row.AfterUuid),
			"group_id":             row.AfterGroupID,
			"expired_at":           tptr(row.AfterExpiredAt),
			"device_limit":         row.AfterDeviceLimit,
			"transfer_enable_plan": row.AfterTransferEnablePlan,
			"transfer_enable_pack": row.AfterTransferEnablePack,
			"transfer_enable":      row.AfterTransferEnable,
			"email":                row.Email,
		},
	}
	return adminUserFromDetailRow(detail), entry, nil
}

// uuidText 把 pgtype.UUID 变成审计快照里的可读值。
// !Valid 时给 nil 而不是空串：一条写着 "" 的审计会被读成「当时 uuid 是空的」。
func uuidText(u pgtype.UUID) any {
	if !u.Valid {
		return nil
	}
	return u.String()
}

// UpdateAdminUser 实现 PATCH /api/v1/admin/users/{id}。**D1：L2 必填原因。**
func (s *Server) UpdateAdminUser(ctx context.Context, req gen.UpdateAdminUserRequestObject) (gen.UpdateAdminUserResponseObject, error) {
	if req.Body == nil {
		return gen.UpdateAdminUser422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空"),
		}, nil
	}
	// ---- L2 ----
	reason, ok := validAdminReason(req.Body.Reason)
	if !ok {
		return gen.UpdateAdminUser422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("必须填写操作原因，且不少于 %d 个字符", adminReasonMinRunes),
				detail("reason", "≥ 8 字符，会进审计日志")),
		}, nil
	}
	arg, badField, err := buildUserEntitlementParams(req.Id, *req.Body)
	if err != nil {
		if errors.Is(err, errAdminPatchEmpty) {
			return gen.UpdateAdminUser422JSONResponse{
				ErrUnprocessableJSONResponse: s.unprocessable(ctx, "没有任何字段需要修改"),
			}, nil
		}
		return gen.UpdateAdminUser422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "uuid 不是合法的 UUID", detail(badField, err.Error())),
		}, nil
	}

	_, actor, err := s.adminActor(ctx)
	if err != nil {
		return gen.UpdateAdminUser500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "无法确定操作者身份", err),
		}, nil
	}

	var view gen.AdminUser
	err = audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		var e audit.Entry
		var err error
		view, e, err = updateAdminUserTx(ctx, q, arg, reason)
		return e, err
	})
	switch {
	case err == nil:
		return gen.UpdateAdminUser200JSONResponse{Data: view, Meta: s.meta(ctx)}, nil
	case errors.Is(err, pgx.ErrNoRows):
		return gen.UpdateAdminUser404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "用户不存在")}, nil
	case isCheckViolation(err):
		// `transfer_enable_plan >= 0` 被拒 = 请求的总额小于该用户已有的加油包分量。
		// 只在**错误路径**上多读一次，把那个数字放进 message —— 让管理员一次就能改对，
		// 而不是二分猜一个下限。
		return gen.UpdateAdminUser422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, s.packFloorMessage(ctx, req.Id),
				detail("transfer_enable_bytes", "不能低于该用户的加油包分量")),
		}, nil
	default:
		return gen.UpdateAdminUser500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "修改用户失败", err),
		}, nil
	}
}

// packFloorMessage 组装 23514 的错误文案。读不到详情时退回一句通用的 ——
// 一个查不到具体数字的错误提示仍然比 500 有用。
func (s *Server) packFloorMessage(ctx context.Context, userID int64) string {
	row, err := s.db.GetAdminUserDetail(ctx, userID)
	if err != nil {
		s.logger.WarnContext(ctx, "取加油包分量失败，422 文案退回通用版", "user_id", userID, "err", err)
		return "总配额不能低于该用户已购买的加油包分量"
	}
	return fmt.Sprintf("该用户有 %d 字节加油包（已购买，不可被管理员的改配额操作抹掉），总配额不能低于它",
		row.TransferEnablePack)
}

// isCheckViolation 在 auth.go，与 isUniqueViolation 并排。
//
// **用它而不是自己先读一次再比大小**：后者是一次 TOCTOU —— 读与写之间被另一笔操作
// 插进来，判断就是错的。让数据库拒绝，然后把拒绝翻译成 422。

// ============================================================
// banAdminUser / unbanAdminUser（D2：L2 必填原因）
// ============================================================

// nodeUserPollSeconds 是节点拉用户表的周期。封禁/解封的生效延迟上限就是它。
const nodeUserPollSeconds = 60

type adminBanQuerier interface {
	AdminBanUser(ctx context.Context, arg dbgen.AdminBanUserParams) (dbgen.AdminBanUserRow, error)
	AdminUnbanUser(ctx context.Context, userID int64) (dbgen.AdminUnbanUserRow, error)
	GetAdminUserDetail(ctx context.Context, userID int64) (dbgen.GetAdminUserDetailRow, error)
}

// banAdminUserTx 是 D2 封禁的事务体。
//
// ⚠️ **重复封禁不是错误。** 查询里刻意没有 `AND banned = false` 的 CAS：
// 加了之后重复点击返回 0 行，而 0 行与「用户不存在」在 sqlc 的 :one 里是同一个
// ErrNoRows，handler 会把它翻成 404 —— 对一个**已经被封**的用户回 404 是谎话。
// 让它成功、让审计记下 (true → true)，多一条审计的代价远小于一次错误的 404。
// `banned_at` 由 SQL 的 coalesce 保住**第一次**被封的时刻。
func banAdminUserTx(ctx context.Context, q adminBanQuerier, userID int64, reason string) (gen.AdminUser, audit.Entry, error) {
	row, err := q.AdminBanUser(ctx, dbgen.AdminBanUserParams{UserID: userID, Reason: reason})
	if err != nil {
		return gen.AdminUser{}, audit.Entry{}, err
	}
	detail, err := q.GetAdminUserDetail(ctx, userID)
	if err != nil {
		return gen.AdminUser{}, audit.Entry{}, err
	}
	return adminUserFromDetailRow(detail), audit.Entry{
		Action:     "D2.user.ban",
		TargetType: "user",
		TargetID:   strconv.FormatInt(row.ID, 10),
		Reason:     reason,
		Before: map[string]any{
			"banned":        row.BeforeBanned,
			"banned_reason": row.BeforeBannedReason,
			"banned_at":     tptr(row.BeforeBannedAt),
		},
		After: map[string]any{
			"banned":        row.AfterBanned,
			"banned_reason": row.AfterBannedReason,
			"banned_at":     tptr(row.AfterBannedAt),
			"email":         row.Email,
			// 记下生效延迟：事后看审计时「封禁时刻」与「他最后一次连上节点的时刻」
			// 相差不到 60 秒是**正常**的，不记这一条会让人以为封禁没生效。
			"node_effective_delay_seconds": nodeUserPollSeconds,
		},
	}, nil
}

// unbanAdminUserTx 是 D2 解封的事务体。
//
// ⚠️ **对已注销（deleted_at 非空）的用户返回 0 行 → 404，这是刻意的。**
// AnonymizeUser 在注销时把 banned_reason 写成 'account_deleted' 并同时写了 deleted_at，
// 而 AdminUnbanUser 的 WHERE 带 `deleted_at IS NULL` —— 注销是用户自己的意愿，
// 管理员的解封按钮不该能推翻它。
func unbanAdminUserTx(ctx context.Context, q adminBanQuerier, userID int64, reason string) (gen.AdminUser, audit.Entry, error) {
	row, err := q.AdminUnbanUser(ctx, userID)
	if err != nil {
		return gen.AdminUser{}, audit.Entry{}, err
	}
	detail, err := q.GetAdminUserDetail(ctx, userID)
	if err != nil {
		return gen.AdminUser{}, audit.Entry{}, err
	}
	return adminUserFromDetailRow(detail), audit.Entry{
		Action:     "D2.user.unban",
		TargetType: "user",
		TargetID:   strconv.FormatInt(row.ID, 10),
		Reason:     reason,
		Before: map[string]any{
			"banned":        row.BeforeBanned,
			"banned_reason": row.BeforeBannedReason,
			"banned_at":     tptr(row.BeforeBannedAt),
		},
		After: map[string]any{
			"banned":                       row.AfterBanned,
			"banned_reason":                row.AfterBannedReason,
			"banned_at":                    tptr(row.AfterBannedAt),
			"email":                        row.Email,
			"node_effective_delay_seconds": nodeUserPollSeconds,
		},
	}, nil
}

// banEffectiveDelayResponse 是 banAdminUser 的 200，多带两个说明生效延迟的响应头。
//
// 🔴 openapi 在这个端点的 description 里逐字写了「节点 60 秒轮询，封禁最长 60 秒后
// 才在节点侧生效 —— 60 秒足够完成一次滥用行为」，而它的 200 响应体是 `AdminUser`，
// **没有任何字段能放这句话**。不说的后果是具体的：管理员在 30 秒后刷新看到
// 「他还在线」，于是再点一次封禁（多一条 D2 审计），或者认为功能坏了。
// 头是当前契约下唯一的在场位置；前端的成功提示仍然必须自己写这句话。
type banEffectiveDelayResponse struct {
	gen.BanAdminUser200JSONResponse
}

func (r banEffectiveDelayResponse) VisitBanAdminUserResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Node-Effective-Delay-Seconds", strconv.Itoa(nodeUserPollSeconds))
	w.Header().Set("Warning", `199 - "ban written; node config poll is 60s, so it takes up to 60s to take effect on nodes"`)
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(r.BanAdminUser200JSONResponse)
}

// unbanEffectiveDelayResponse 同上，解封侧。
// 解封的延迟同样是 60 秒，而它的后果方向相反但同样要说：
// 用户被告知「已解封」之后立刻去连、连不上，会再开一张工单。
type unbanEffectiveDelayResponse struct {
	gen.UnbanAdminUser200JSONResponse
}

func (r unbanEffectiveDelayResponse) VisitUnbanAdminUserResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Node-Effective-Delay-Seconds", strconv.Itoa(nodeUserPollSeconds))
	w.Header().Set("Warning", `199 - "unban written; node config poll is 60s, so it takes up to 60s to take effect on nodes"`)
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(r.UnbanAdminUser200JSONResponse)
}

// BanAdminUser 实现 POST /api/v1/admin/users/{id}/ban。**D2：L2 必填原因。**
func (s *Server) BanAdminUser(ctx context.Context, req gen.BanAdminUserRequestObject) (gen.BanAdminUserResponseObject, error) {
	if req.Body == nil {
		return gen.BanAdminUser422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空"),
		}, nil
	}
	reason, ok := validAdminReason(req.Body.Reason)
	if !ok {
		return gen.BanAdminUser422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("必须填写封禁原因，且不少于 %d 个字符", adminReasonMinRunes),
				detail("reason", "≥ 8 字符，会进审计日志")),
		}, nil
	}
	_, actor, err := s.adminActor(ctx)
	if err != nil {
		return gen.BanAdminUser500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "无法确定操作者身份", err),
		}, nil
	}

	var view gen.AdminUser
	err = audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		var e audit.Entry
		var err error
		view, e, err = banAdminUserTx(ctx, q, req.Id, reason)
		return e, err
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.BanAdminUser404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "用户不存在")}, nil
		}
		return gen.BanAdminUser500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "封禁用户失败", err),
		}, nil
	}
	return banEffectiveDelayResponse{
		gen.BanAdminUser200JSONResponse{Data: view, Meta: s.meta(ctx)},
	}, nil
}

// UnbanAdminUser 实现 POST /api/v1/admin/users/{id}/unban。**D2：L2 必填原因。**
func (s *Server) UnbanAdminUser(ctx context.Context, req gen.UnbanAdminUserRequestObject) (gen.UnbanAdminUserResponseObject, error) {
	if req.Body == nil {
		return gen.UnbanAdminUser422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空"),
		}, nil
	}
	reason, ok := validAdminReason(req.Body.Reason)
	if !ok {
		return gen.UnbanAdminUser422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("必须填写解封原因，且不少于 %d 个字符", adminReasonMinRunes),
				detail("reason", "≥ 8 字符，会进审计日志")),
		}, nil
	}
	_, actor, err := s.adminActor(ctx)
	if err != nil {
		return gen.UnbanAdminUser500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "无法确定操作者身份", err),
		}, nil
	}

	var view gen.AdminUser
	err = audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		var e audit.Entry
		var err error
		view, e, err = unbanAdminUserTx(ctx, q, req.Id, reason)
		return e, err
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 「不存在」与「已注销」同为 404，且文案不区分：
			// 区分开等于把「这个 id 是一个注销过的账号」做成一个可探测的信号。
			return gen.UnbanAdminUser404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "用户不存在")}, nil
		}
		return gen.UnbanAdminUser500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "解封用户失败", err),
		}, nil
	}
	return unbanEffectiveDelayResponse{
		gen.UnbanAdminUser200JSONResponse{Data: view, Meta: s.meta(ctx)},
	}, nil
}

// ============================================================
// revokeAdminUserSubscriptions（D3：L1 + L2 + L3）
// ============================================================

// errAdminConfirmationMismatch 是 L1 比对失败。用哨兵从事务闭包里传出来 ——
// InTx 只能返回 error，而它要映射成 422 而不是 500。
var errAdminConfirmationMismatch = errors.New("确认串与目标不符")

type adminRevokeSubsQuerier interface {
	LockAdminUserTarget(ctx context.Context, userID int64) (dbgen.LockAdminUserTargetRow, error)
	RevokeAllUserSubscriptionTokens(ctx context.Context, userID int64) (dbgen.RevokeAllUserSubscriptionTokensRow, error)
}

// revokeAdminUserSubsTx 是 D3 的事务体。
//
// 🔴 **L1 的比对必须在这里（拿到锁住的那一行之后），不能在事务外。**
//
//	LockAdminUserTarget 一条查询同时干三件事：取出 L1 要比对的期望值、
//	`FOR UPDATE` 锁住这一行、给出改前值（sub_revoked_at）。
//	比对用的期望值是**服务端自己查出来的** email —— 拿请求体里的 email
//	去比对它自己恒等于通过，那样的 L1 是装饰品。
//
// 改后值从 RevokeAllUserSubscriptionTokens 来：它用「数据修改型 CTE 互相看不到
// 对方效果」这条语义算出「本次真正撤掉几条」，不重写一遍。
func revokeAdminUserSubsTx(ctx context.Context, q adminRevokeSubsQuerier, userID int64, confirmation, reason string) (gen.RevokeAllResult, audit.Entry, error) {
	target, err := q.LockAdminUserTarget(ctx, userID)
	if err != nil {
		return gen.RevokeAllResult{}, audit.Entry{}, err
	}
	if !confirmationMatches(target.Email, confirmation) {
		return gen.RevokeAllResult{}, audit.Entry{}, errAdminConfirmationMismatch
	}
	row, err := q.RevokeAllUserSubscriptionTokens(ctx, userID)
	if err != nil {
		return gen.RevokeAllResult{}, audit.Entry{}, err
	}
	out := gen.RevokeAllResult{
		Revoked:      row.Revoked,
		SubRevokedAt: ttime(row.SubRevokedAt),
	}
	return out, audit.Entry{
		Action:     "D3.user.revoke_subscriptions",
		TargetType: "user",
		TargetID:   strconv.FormatInt(userID, 10),
		Reason:     reason,
		Before:     map[string]any{"sub_revoked_at": tptr(target.SubRevokedAt)},
		After: map[string]any{
			"sub_revoked_at": tptr(row.SubRevokedAt),
			// 撤掉的条数是这条审计里唯一能回答「这次操作到底影响了什么」的数字：
			// sub_revoked_at 只是个时间戳，0 条与 12 条在它上面长得一模一样。
			"revoked": row.Revoked,
			"email":   target.Email,
		},
	}, nil
}

// RevokeAdminUserSubscriptions 实现 POST /api/v1/admin/users/{id}/revoke-subs。
// **D3：L1 确认串 + L2 原因 + L3 TOTP。**
//
// 🔴 **四层的检查顺序是 L2 → L3 → L1，不是 L1 → L2 → L3。**
//
//	把 L1 放在 L3 之前看起来更友好（打错确认串不必浪费一个 TOTP 码），
//	但那会让这个端点变成一台**不需要 TOTP 的邮箱验证机**：
//	随便填一个 confirmation，从 422 与 404 的差别就能判断某个 id 的邮箱是不是它。
//	代价是确认串打错时那个 TOTP 码就用掉了，管理员要等最多 30 秒 —— 可接受。
func (s *Server) RevokeAdminUserSubscriptions(ctx context.Context, req gen.RevokeAdminUserSubscriptionsRequestObject) (gen.RevokeAdminUserSubscriptionsResponseObject, error) {
	if req.Body == nil {
		return gen.RevokeAdminUserSubscriptions422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空"),
		}, nil
	}
	// ---- L2 ----
	reason, ok := validAdminReason(req.Body.Reason)
	if !ok {
		return gen.RevokeAdminUserSubscriptions422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("必须填写操作原因，且不少于 %d 个字符", adminReasonMinRunes),
				detail("reason", "≥ 8 字符，会进审计日志")),
		}, nil
	}
	// ---- L3 ----
	if fb, ie := s.adminStepUp(ctx, req.Params.XTOTPCode); fb != nil {
		return gen.RevokeAdminUserSubscriptions403JSONResponse{ErrForbiddenJSONResponse: *fb}, nil
	} else if ie != nil {
		return gen.RevokeAdminUserSubscriptions500JSONResponse{ErrInternalJSONResponse: *ie}, nil
	}

	_, actor, err := s.adminActor(ctx)
	if err != nil {
		return gen.RevokeAdminUserSubscriptions500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "无法确定操作者身份", err),
		}, nil
	}

	var out gen.RevokeAllResult
	err = audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		var e audit.Entry
		var err error
		out, e, err = revokeAdminUserSubsTx(ctx, q, req.Id, req.Body.Confirmation, reason)
		return e, err
	})
	switch {
	case err == nil:
		return gen.RevokeAdminUserSubscriptions200JSONResponse{Data: out, Meta: s.meta(ctx)}, nil
	case errors.Is(err, errAdminConfirmationMismatch):
		return gen.RevokeAdminUserSubscriptions422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				"确认串与该用户的邮箱不一致",
				detail("confirmation", "必须逐字等于该用户的邮箱")),
		}, nil
	case errors.Is(err, pgx.ErrNoRows):
		return gen.RevokeAdminUserSubscriptions404JSONResponse{
			ErrNotFoundJSONResponse: s.notFound(ctx, "用户不存在"),
		}, nil
	default:
		return gen.RevokeAdminUserSubscriptions500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "吊销订阅失败", err),
		}, nil
	}
}

// ============================================================
// adjustAdminUserBalance（D10：L1 + L2 + L3 + 权限位）
// ============================================================

// adjustBalanceRetryAfter 是科目缺失时给的退避秒数。
//
// 取 300（5 分钟）而不是 30 秒：这里的「依赖」是一支**还没跑的 migration**
// （ledger_accounts 缺 `expense:admin_adjust`），30 秒后重试必然还是失败，
// 只会让管理员连点五次 —— 而每一次都是一条 D10 的审计尝试。
// 与 wallet.go 的 commissionTransferRetryAfter 同一个数字、同一条理由。
const adjustBalanceRetryAfter int32 = 300

// adjustBalance503JSONResponse 是 D10 的 503。
//
// 🔴 **openapi 给这个端点只声明了 403/404/422/500，没有 503。明知如此仍然发 503。**
//
//	500 说的是「我们出了个偶发故障」→ 管理员会重试，每次都失败，
//	而 D10 的每一次尝试都值得被记下来，于是审计表里堆起一串看不出所以然的失败；
//	503 + Retry-After 说的是「这个功能现在不可用」→ 前端可以直接把按钮置灰。
//	而这里的失败**不是偶发**：它是 ledger_accounts 里缺一行
//	（`expense:admin_adjust`），在那一行被灌进去之前**每一次都会失败**，
//	重试一百次也是一样。
//
// ℹ️ 本轮同批交付的 `0019_ledger_admin_adjust.up.sql` 已经补了那一行，
//
//	所以正常部署下这条路径不该被走到。**仍然保留它**：迁移的执行模型是
//	裸 psql 灌文件、没有版本表（deploy.yml 自己登记着这条 TODO），
//	「某个环境漏灌了 0019」是一个真实可能的状态，而那时的正确响应
//	仍然是 503 + Retry-After 而不是 500。
type adjustBalance503JSONResponse struct {
	gen.ErrDependencyDownJSONResponse
}

func (r adjustBalance503JSONResponse) VisitAdjustAdminUserBalanceResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", fmt.Sprint(r.Headers.RetryAfter))
	w.Header().Set("X-Request-Id", fmt.Sprint(r.Headers.XRequestId))
	w.WriteHeader(http.StatusServiceUnavailable)
	return json.NewEncoder(w).Encode(r.Body)
}

// balanceAdjustAccounts 取 D10 两条腿的科目 id。第二个返回值 false = 科目缺失，调用方必须 503。
//
// ⚠️ **两条路都要堵。** `GetAdminBalanceAdjustAccounts` 是
// `max(id) FILTER (...)::bigint` 的聚合，恒返回一行、缺科目时那一列是 NULL；
// 而显式的 `::bigint` cast 让 sqlc 把它判成了 NOT NULL，生成出来是**非指针 int64**，
// 于是 pgx 在 row.Scan 时报 `cannot scan NULL into *int64` —— handler 拿到的是
// 一个**错误**而不是一个零值。将来查询被改成 coalesce 或重新生成成指针时，
// 拿到的又会是 0。两种形态都判成「缺失」。
// isNullScanError 复用 wallet.go 那一份（它的守卫测试用 pgx 原文构造错误）。
func (s *Server) balanceAdjustAccounts(ctx context.Context, userID int64) (dbgen.GetAdminBalanceAdjustAccountsRow, bool) {
	row, err := s.db.GetAdminBalanceAdjustAccounts(ctx)
	if err != nil {
		if isNullScanError(err) || errors.Is(err, pgx.ErrNoRows) {
			s.logger.ErrorContext(ctx,
				"bp_ledger_account_missing 调余额所需的账本科目缺失（expense:admin_adjust），本次请求已拒绝。"+
					"这是**数据缺失不是代码缺陷**：修复方式是补一支 migration 插入 "+
					"('expense:admin_adjust','expense','CNY')，在那之前本端点持续返回 503",
				"user_id", userID, "err", err)
			return row, false
		}
		s.logger.ErrorContext(ctx, "查询账本科目失败", "user_id", userID, "err", err)
		return row, false
	}
	if row.AdjustAccountID == 0 || row.WalletAccountID == 0 {
		s.logger.ErrorContext(ctx,
			"bp_ledger_account_missing 调余额所需的账本科目缺失（返回了 0 而不是 NULL），本次请求已拒绝",
			"user_id", userID,
			"adjust_account_id", row.AdjustAccountID,
			"wallet_account_id", row.WalletAccountID)
		return row, false
	}
	return row, true
}

type adminBalanceAdjustQuerier interface {
	LockAdminUserTarget(ctx context.Context, userID int64) (dbgen.LockAdminUserTargetRow, error)
	GetWalletOverview(ctx context.Context, userID int64) (dbgen.GetWalletOverviewRow, error)
	CreateLedgerEntry(ctx context.Context, arg dbgen.CreateLedgerEntryParams) (dbgen.LedgerEntry, error)
	CreateLedgerLine(ctx context.Context, arg dbgen.CreateLedgerLineParams) (dbgen.LedgerLine, error)
	UpsertWalletBalance(ctx context.Context, arg dbgen.UpsertWalletBalanceParams) (dbgen.WalletBalance, error)
}

// adjustBalanceInput 是 D10 事务体的入参。
type adjustBalanceInput struct {
	UserID       int64
	Amount       int64 // 单位：分，可为负
	Confirmation string
	Reason       string
	Accounts     dbgen.GetAdminBalanceAdjustAccountsRow
	EntryNo      string
}

// adjustBalanceTx 是 D10 的事务体：锁 → 读改前 → 分录两条腿 → 改缓存 → 读改后。
//
// 🔴 **符号约定（弄反了不会报错，只会让钱反向）：** `amount > 0 = 给用户加钱`。
//
//	· `expense:admin_adjust` 那条腿的 amount = +amount（借 Dr：这笔凭空出现的钱记在
//	  「管理员调整」这个费用科目头上）；
//	· `liability:user_wallet` 那条腿的 amount = -amount（贷 Cr：我们欠用户的钱变多了），
//	  subject_id = user_id。
//	用户视角的余额是 `-SUM(amount)`，这个负号不是笔误（wallet.sql 开头逐条列过）。
//	负数调整时两条腿自动同时反号，`SUM(amount) = 0` 恒成立 —— 这正是
//	「一个科目双向承载调整/更正」不需要建两个科目的原因。
//
// 🔴 **扣成负数由数据库拒绝，不由 handler 判断。** `wallet_balances.balance >= 0`
//
//	是 0007 的 CHECK；负向调整超额时 UpsertWalletBalance 抛 23514，整个事务回滚
//	（分录一起回滚）。**不要**自己先读一次余额再比大小 —— 那是一次 TOCTOU：
//	读与写之间被另一笔消费插进来，判断就是错的。
//
// ⚠️ `UpsertWalletBalance` 的 balance 参数是**增量不是绝对值**
//
//	（ON CONFLICT 分支写的是 `balance + EXCLUDED.balance`）。
//	传绝对值的现象是余额被**重置**，而不是报错。
func adjustBalanceTx(ctx context.Context, q adminBalanceAdjustQuerier, in adjustBalanceInput) (dbgen.GetWalletOverviewRow, audit.Entry, error) {
	var zero dbgen.GetWalletOverviewRow

	// 1. L1 比对 + 行锁。锁的是 users 那一行而不是 wallet_balances：
	//    调余额时后者**可能还不存在**（新用户从没充过值，Upsert 走 INSERT 分支），
	//    对一行不存在的记录 FOR UPDATE 锁到的是空气，两个并发事务会双双通过。
	target, err := q.LockAdminUserTarget(ctx, in.UserID)
	if err != nil {
		return zero, audit.Entry{}, err
	}
	if !confirmationMatches(target.Email, in.Confirmation) {
		return zero, audit.Entry{}, errAdminConfirmationMismatch
	}

	// 2. 改前值。
	before, err := q.GetWalletOverview(ctx, in.UserID)
	if err != nil {
		return zero, audit.Entry{}, err
	}

	// 3. 分录：一条 entry + 两条腿。
	entry, err := q.CreateLedgerEntry(ctx, dbgen.CreateLedgerEntryParams{
		EntryNo:     in.EntryNo,
		Description: fmt.Sprintf("管理员调整用户余额（%+d 分）", in.Amount),
		RefType:     ptrOf("reconcile_adjust"),
		RefID:       ptrOf(in.UserID),
	})
	if err != nil {
		return zero, audit.Entry{}, err
	}
	if _, err := q.CreateLedgerLine(ctx, dbgen.CreateLedgerLineParams{
		EntryID:   entry.ID,
		AccountID: in.Accounts.AdjustAccountID,
		Amount:    in.Amount,
		Currency:  ledgerCurrencyCNY,
	}); err != nil {
		return zero, audit.Entry{}, err
	}
	if _, err := q.CreateLedgerLine(ctx, dbgen.CreateLedgerLineParams{
		EntryID:   entry.ID,
		AccountID: in.Accounts.WalletAccountID,
		SubjectID: ptrOf(in.UserID),
		Amount:    -in.Amount,
		Currency:  ledgerCurrencyCNY,
	}); err != nil {
		return zero, audit.Entry{}, err
	}

	// 4. 缓存表。增量。
	if _, err := q.UpsertWalletBalance(ctx, dbgen.UpsertWalletBalanceParams{
		UserID:      in.UserID,
		Currency:    ledgerCurrencyCNY,
		Balance:     in.Amount,
		LastEntryID: ptrOf(entry.ID),
	}); err != nil {
		return zero, audit.Entry{}, err
	}

	// 5. 改后值。**在同一事务里读**：事务外再读一次的话，一笔并发消费会让
	//    响应里的余额与刚写进去的分录对不上，而这是钱的数字。
	after, err := q.GetWalletOverview(ctx, in.UserID)
	if err != nil {
		return zero, audit.Entry{}, err
	}

	return after, audit.Entry{
		Action:     "D10.user.balance_adjust",
		TargetType: "user",
		TargetID:   strconv.FormatInt(in.UserID, 10),
		Reason:     in.Reason,
		// 🔴 balance_ledger 与 balance_cached **两个都要记**。
		// 两者不等本身就是一条必须写进审计的事实（缓存漂移），
		// 而只记一个的话，事后就再也分不清「当时缓存是不是已经歪了」。
		Before: map[string]any{
			"balance_ledger": before.BalanceLedger,
			"balance_cached": before.BalanceCached,
		},
		After: map[string]any{
			"balance_ledger": after.BalanceLedger,
			"balance_cached": after.BalanceCached,
			"amount":         in.Amount,
			"entry_id":       entry.ID,
			"entry_no":       entry.EntryNo,
			"email":          target.Email,
		},
	}, nil
}

// AdjustAdminUserBalance 实现 POST /api/v1/admin/users/{id}/balance-adjust。
// **D10：L1 + L2 + L3 + 权限位 perm_adjust_balance。**
func (s *Server) AdjustAdminUserBalance(ctx context.Context, req gen.AdjustAdminUserBalanceRequestObject) (gen.AdjustAdminUserBalanceResponseObject, error) {
	if req.Body == nil {
		return gen.AdjustAdminUserBalance422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空"),
		}, nil
	}
	// ---- L2 ----
	reason, ok := validAdminReason(req.Body.Reason)
	if !ok {
		return gen.AdjustAdminUserBalance422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("必须填写调整原因，且不少于 %d 个字符", adminReasonMinRunes),
				detail("reason", "≥ 8 字符，会进审计日志")),
		}, nil
	}
	if req.Body.Amount == 0 {
		// 0 元调整会写出一条两条腿都是 0 的分录、一次空的 Upsert，以及一条 D10 审计。
		// 拒绝它不是洁癖：D10 的审计记录是「谁动了用户的钱」唯一的线索，
		// 而一条金额为 0 的记录在事后读起来与「有人试图动钱但失败了」不可区分。
		return gen.AdjustAdminUserBalance422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "调整额不能为 0",
				detail("amount", "单位：分，可为负，但不能为 0")),
		}, nil
	}

	admin, actor, err := s.adminActor(ctx)
	if err != nil {
		return gen.AdjustAdminUserBalance500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "无法确定操作者身份", err),
		}, nil
	}
	// ---- L4：独立权限位 ----
	//
	// ⚠️ §6.2 的 L4 那一行只点名了 D6 与 D14，但 `admin_users.perm_adjust_balance`
	// 这一列是**为 D10 建的**（0002:64 的列注释逐字写着 D10），而它在契约的
	// AdminPermission 枚举里没有对应值 —— 也就是说这个权限位**只能改库授予**。
	// 仍然强制它：一个存在于 schema、却没有任何代码检查的权限位，
	// 会让所有人都以为 D10 被管住了。默认 false ⇒ D10 默认关闭，与 D6 同形。
	if !admin.Can(middleware.PermAdjustBalance) {
		s.logger.WarnContext(ctx, "无 perm_adjust_balance 的管理员尝试调整用户余额",
			"admin_id", admin.AdminID, "user_id", req.Id)
		return gen.AdjustAdminUserBalance403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"没有调整用户余额的权限（perm_adjust_balance 默认关闭，且契约里没有对应的可授予枚举值，只能由 DBA 开启）"),
		}, nil
	}
	// ---- L3 ----
	if fb, ie := s.adminStepUp(ctx, req.Params.XTOTPCode); fb != nil {
		return gen.AdjustAdminUserBalance403JSONResponse{ErrForbiddenJSONResponse: *fb}, nil
	} else if ie != nil {
		return gen.AdjustAdminUserBalance500JSONResponse{ErrInternalJSONResponse: *ie}, nil
	}

	// ---- 提前失败：在动 wallet_balances、动分录之前把两个科目取出来 ----
	//
	// 放在事务**之外**（交接说明把它列为「同一个 InTx 里的第三步」，这里比那更早）：
	// 科目缺失时连事务都不必开，也就不会有任何一次白跑的业务写入。
	// 与 wallet.go 的 commissionAccounts 同一形状。
	accounts, ok := s.balanceAdjustAccounts(ctx, req.Id)
	if !ok {
		return adjustBalance503JSONResponse{
			ErrDependencyDownJSONResponse: gen.ErrDependencyDownJSONResponse{
				Body: s.envelope(ctx, gen.INTERNALDEPENDENCYDOWN,
					"调整用户余额暂不可用（账本科目缺失），请联系运维补齐后再试"),
				Headers: gen.ErrDependencyDownResponseHeaders{
					RetryAfter: adjustBalanceRetryAfter,
					XRequestId: middleware.RequestIDFrom(ctx),
				},
			},
		}, nil
	}

	var after dbgen.GetWalletOverviewRow
	err = audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		var e audit.Entry
		var err error
		after, e, err = adjustBalanceTx(ctx, q, adjustBalanceInput{
			UserID:       req.Id,
			Amount:       req.Body.Amount,
			Confirmation: req.Body.Confirmation,
			Reason:       reason,
			Accounts:     accounts,
			EntryNo:      newEntryNo("BA"),
		})
		return e, err
	})
	switch {
	case err == nil:
		s.reportWalletAnomalies(ctx, req.Id, after)
		return gen.AdjustAdminUserBalance200JSONResponse{Data: walletView(after), Meta: s.meta(ctx)}, nil
	case errors.Is(err, errAdminConfirmationMismatch):
		return gen.AdjustAdminUserBalance422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				"确认串与该用户的邮箱不一致",
				detail("confirmation", "必须逐字等于该用户的邮箱")),
		}, nil
	case errors.Is(err, pgx.ErrNoRows):
		return gen.AdjustAdminUserBalance404JSONResponse{
			ErrNotFoundJSONResponse: s.notFound(ctx, "用户不存在"),
		}, nil
	case isCheckViolation(err):
		return gen.AdjustAdminUserBalance422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				"扣减后余额会变成负数，数据库拒绝了这次调整",
				detail("amount", "负向调整不能超过用户当前余额")),
		}, nil
	default:
		return gen.AdjustAdminUserBalance500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "调整用户余额失败", err),
		}, nil
	}
}

// ============================================================
// exportAdminUsers（D14：L2 + 独立权限位 admin.user.export + 5/h）
// ============================================================

const (
	// bucketAdminExportUsersHour 是导出的限流桶。窗口长度编进桶名（同 auth.go 的纪律）。
	bucketAdminExportUsersHour = "admin_export_users_1h"
	// exportUsersPerHour 是契约给的限额（openapi 的 description 逐字：「限流 5/h（Postgres 精确档）」）。
	exportUsersPerHour = 5

	// adminExportRowCap 是一次导出的硬上限。
	//
	// 🔴 **它不是分页而是闸。** 导出没有游标（一次性 CSV），而这个端点一次能带走
	//    全部用户的邮箱 —— §6.2 把 D14 单列成一层的理由就是这个。
	//    50000 的依据：当前用户量在四位数以下，这个值给了一个量级的余量，
	//    同时把「一次请求把整张 users 表读进内存」的最坏情况钉在一个可预算的数字上。
	adminExportRowCap = 50000
)

// ExportAdminUsers 实现 POST /api/v1/admin/users/export。
// **D14：L2 原因 + L4 独立权限位 `admin.user.export`（默认不授予）+ 5/h 限流。**
//
// 🔴 **命中行数上限时返回 422 拒绝，不发一份被截断的 CSV。**
//
//	考虑过三种「告知截断」的形态，两种被否掉：
//	  · 响应头（X-Export-Truncated）—— 头会被代理丢、被 curl -o 忽略，
//	    而落到磁盘上的那个 .csv 文件里**没有任何痕迹**说它不完整；
//	  · CSV 里加一行警告 —— 那会污染数据本身，任何解析器都会把它当成一条用户记录。
//	留下的只有「拒绝」：一份静默截断的 CSV 会被当成完整名单去做运营决策
//	（发邮件、算留存、判断某人是不是我们的用户），而「名单里没有他」与
//	「名单被截断了」在事后不可区分。
//	⚠️ 代价必须登记：这个端点在契约上**没有任何筛选参数**（请求体只有 reason），
//	   所以一旦用户数超过上限，导出就**彻底不可用**且没有绕过办法。
//	   真正的修法是加 export_jobs 表 + GCS 签名 URL 的异步导出（要一支迁移），
//	   或者给 openapi 加筛选参数（已冻结）。两者都不在本轮范围内。
//
// ⚠️ **CSV 走 `download_url` 的 data: URI**：没有 export_jobs 表、没有对象存储，
// 而契约要求返回一个异步任务句柄。不为了「像异步」返回 `queued` 再让前端
// 轮询一个不存在的资源 —— 那是一个永远不会完成的进度条。
func (s *Server) ExportAdminUsers(ctx context.Context, req gen.ExportAdminUsersRequestObject) (gen.ExportAdminUsersResponseObject, error) {
	if req.Body == nil {
		return gen.ExportAdminUsers422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空"),
		}, nil
	}
	// ---- L2 ----
	reason, ok := validAdminReason(req.Body.Reason)
	if !ok {
		return gen.ExportAdminUsers422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("必须填写导出原因，且不少于 %d 个字符", adminReasonMinRunes),
				detail("reason", "≥ 8 字符，会进审计日志")),
		}, nil
	}
	admin, actor, err := s.adminActor(ctx)
	if err != nil {
		return gen.ExportAdminUsers500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "无法确定操作者身份", err),
		}, nil
	}
	// ---- L4 ----
	if !admin.Can(middleware.PermExportCSV) {
		s.logger.WarnContext(ctx, "无 admin.user.export 权限的管理员尝试导出用户",
			"admin_id", admin.AdminID)
		return gen.ExportAdminUsers403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"没有导出用户的权限（admin.user.export 默认不授予）"),
		}, nil
	}
	// ---- 限流 ----
	//
	// subject 取 **admin_id 而不是 IP**：导出的风险是「一个人一次带走全部用户」，
	// 而同一个管理员换个网络就能绕过 per-IP 的桶。admin_id 是这条链路上
	// 唯一与「谁」绑定的稳定标识。
	if retry, limited := s.checkRateRules(ctx, rateRule{
		bucket:  bucketAdminExportUsersHour,
		subject: strconv.FormatInt(admin.AdminID, 10),
		limit:   exportUsersPerHour,
		window:  time.Hour,
	}); limited {
		return gen.ExportAdminUsers429JSONResponse{
			ErrRateLimitedJSONResponse: s.rateLimited(ctx, "导出过于频繁，请稍后再试", retry),
		}, nil
	}

	// 多取一行用来判断「是不是被截断了」。判据不能是「返回行数 == 上限」——
	// 用户数正好等于上限时那会误报一次拒绝。
	rows, err := s.db.ExportAdminUsersRows(ctx, dbgen.ExportAdminUsersRowsParams{
		PageLimit: adminExportRowCap + 1,
	})
	if err != nil {
		return gen.ExportAdminUsers500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "导出用户失败", err),
		}, nil
	}
	truncated := adminExportIsTruncated(len(rows))

	// ---- 审计（纯读，没有业务事务可搭，走 audit.Write）----
	//
	// 🔴 **被拒的那次也要记。** 一次命中上限的导出仍然把全表读进了内存 ——
	// 数据外流的动作已经发生过一半，而「读了但没发出去」与「没读过」
	// 在事后只有这条记录能区分。
	entry := audit.Entry{
		Action:     "D14.user.export",
		TargetType: "user",
		TargetID:   "*", // 目标是全集；target_id 是 NOT NULL text，不能留空
		Reason:     reason,
		After: map[string]any{
			"row_cap":   adminExportRowCap,
			"row_count": len(rows),
			"truncated": truncated,
			// 契约给这个端点没有任何筛选参数，所以筛选条件恒为「全部未注销用户」。
			// 仍然显式写进审计：哪天加了筛选参数而忘了改这里，这一行会是不对的，
			// 而一个恒定的字面量比一个缺失的字段更容易被发现。
			"filter":    "deleted_at IS NULL",
			"delivered": !truncated,
		},
	}
	if err := audit.Write(ctx, s.db.Pool, actor, entry); err != nil {
		// 🔴 审计写失败 → **不发数据**。这是 §6.3 第 1 条在没有业务事务时的等价形态：
		// 一次没有留痕的用户名单导出，与一次没有发生过的导出在事后不可区分。
		return gen.ExportAdminUsers500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "导出审计写入失败，本次导出已取消", err),
		}, nil
	}

	if truncated {
		s.logger.ErrorContext(ctx,
			"bp_admin_export_truncated 用户导出命中行数上限，已拒绝下发（一份被截断的名单会被当成完整名单使用）",
			"admin_id", admin.AdminID, "row_cap", adminExportRowCap)
		return gen.ExportAdminUsers422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("符合条件的用户超过 %d 行上限，本次导出已被拒绝（一份被截断的名单会被误当成完整名单）；"+
					"当前契约没有筛选参数，需要先落地异步导出", adminExportRowCap)),
		}, nil
	}

	csvBytes, err := buildUsersCSV(rows)
	if err != nil {
		return gen.ExportAdminUsers500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "生成导出文件失败", err),
		}, nil
	}
	requestID := middleware.RequestIDFrom(ctx)
	s.logger.InfoContext(ctx, "bp_admin_export_users 用户名单已导出",
		"admin_id", admin.AdminID, "row_count", len(rows), "bytes", len(csvBytes))

	return gen.ExportAdminUsers202JSONResponse{
		Data: gen.ExportJob{
			// id 取 request_id：没有 export_jobs 表，也就没有可持久化的任务 id。
			// 用 request_id 至少让这个 id 能在访问日志与审计里被找回来。
			Id:     requestID,
			Status: gen.Done,
			// data: URI —— 见函数头注释。ExpiresAt 留空：它不指向任何会过期的资源。
			DownloadUrl: ptrOf("data:text/csv;charset=utf-8;base64," +
				base64.StdEncoding.EncodeToString(csvBytes)),
		},
		Meta: s.meta(ctx),
	}, nil
}

// adminExportIsTruncated 判断这次导出是不是被上限截断了。
//
// 🔴 **判据是「取回的行数 > 上限」，不是「取回的行数 == 上限」。**
//
//	查询取的是 cap+1 行，所以取回 cap 行说明恰好取完、没有第 cap+1 行；
//	用 `== cap` 判会在用户数**正好等于**上限时误判一次拒绝 —— 一个只在
//	某个特定用户数上出现、并且第二天就自己消失的故障。
func adminExportIsTruncated(fetched int) bool {
	return fetched > adminExportRowCap
}

// adminExportCSVHeader 是导出的列头。**改它就是改一份对外交付物的格式**，
// 下游的表格与脚本按列名取值，删列或改名会静默地让某一列变成空。
var adminExportCSVHeader = []string{
	"id", "email", "banned", "created_at", "last_login_at",
	"plan_id", "plan_name", "group_id", "expired_at", "device_limit",
	"transfer_enable_plan", "transfer_enable_pack", "transfer_enable",
	"upload_bytes", "download_bytes", "balance_amount",
}

// buildUsersCSV 把导出行拼成 CSV。纯函数。
//
// ⚠️ **开头写 UTF-8 BOM。** 没有 BOM 时 Excel（简体中文环境）会按 GBK 解，
// 于是套餐名与备注变成乱码 —— 而这份文件的第一个读者几乎一定是用 Excel 打开它的。
// BOM 对 Go / Python / pandas 的 CSV 读取无影响（都会跳过或把它并进第一个字段名，
// 后者可以靠 `id` 这一列不参与匹配来规避）。
//
// ⚠️ **不导出 uuid**（ExportAdminUsersRows 也没选它）：uuid 是节点侧的连接凭据，
// 一份泄漏的导出 CSV 若含 uuid，等于把全部用户的账号一起送出去。
func buildUsersCSV(rows []dbgen.ExportAdminUsersRowsRow) ([]byte, error) {
	var buf strings.Builder
	buf.WriteString("\ufeff")
	w := csv.NewWriter(&buf)
	if err := w.Write(adminExportCSVHeader); err != nil {
		return nil, err
	}
	for _, r := range rows {
		rec := []string{
			strconv.FormatInt(r.ID, 10),
			r.Email,
			strconv.FormatBool(r.Banned),
			csvTime(r.CreatedAt),
			csvTime(r.LastLoginAt),
			csvInt64Ptr(r.PlanID),
			derefOr(r.PlanName, ""),
			strconv.FormatInt(r.GroupID, 10),
			csvTime(r.ExpiredAt),
			csvInt32Ptr(r.DeviceLimit),
			strconv.FormatInt(r.TransferEnablePlan, 10),
			strconv.FormatInt(r.TransferEnablePack, 10),
			strconv.FormatInt(r.TransferEnable, 10),
			strconv.FormatInt(r.UploadBytes, 10),
			strconv.FormatInt(r.DownloadBytes, 10),
			strconv.FormatInt(r.BalanceAmount, 10),
		}
		if err := w.Write(rec); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// csvTime 把可空时间写成 RFC3339；无值时是**空单元格**而不是 0001-01-01。
// 后者在表格里看起来像一个真实发生过的时刻，而且排序时会跑到最前面。
func csvTime(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return ""
	}
	return ts.Time.UTC().Format(time.RFC3339)
}

// csvInt64Ptr / csvInt32Ptr 同理：NULL 是空单元格，不是 0。
// 「device_limit 是 0」与「device_limit 没有值（不限设备）」是相反的两件事。
func csvInt64Ptr(v *int64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(*v, 10)
}

func csvInt32Ptr(v *int32) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(int64(*v), 10)
}

// ============================================================
// listAdmins
// ============================================================

// ListAdmins 实现 GET /api/v1/admin/admins。
//
// ⚠️ **只列在职的。** ListAdminAccounts 的 include_disabled 参数传 nil
// （openapi 给这个端点没有任何 query 参数，所以那条分支在契约下不可达）。
// 理由是契约的 `AdminAccount` **没有 disabled 字段**：把停用的也列出来，
// 它在前端与在职管理员长得一模一样 —— 而「谁还有后台权限」这个问题
// 正是这个页面存在的唯一理由。
//
// ⚠️ 没有分页参数，这是对的：管理员是个位数量级，分页器比数据还多。
func (s *Server) ListAdmins(ctx context.Context, _ gen.ListAdminsRequestObject) (gen.ListAdminsResponseObject, error) {
	rows, err := s.db.ListAdminAccounts(ctx, nil)
	if err != nil {
		return gen.ListAdmins500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "查询管理员列表失败", err),
		}, nil
	}
	out := make([]gen.AdminAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminAccountView(r.ID, r.Email, r.Role,
			r.PermMarkOrderPaid, r.PermExportCsv, r.TotpEnabled, r.LastLoginAt, r.CreatedAt))
	}
	return gen.ListAdmins200JSONResponse{Data: out, Meta: s.meta(ctx)}, nil
}

// ============================================================
// TOTP 绑定材料的生成（createAdmin / resetAdminTotp 共用）
// ============================================================

const (
	// totpSecretBytes 是新 secret 的熵。20 字节 = 160 位，RFC 4226 §4 的推荐值，
	// 也是所有 Authenticator app 的默认长度（base32 之后 32 个字符）。
	totpSecretBytes = 20
	// totpIssuerName 是 otpauth URI 里的 issuer，决定 Authenticator 列表里显示的名字。
	// 改它会让已绑定的管理员在 app 里看到一个新条目（旧的还在），不会失效但会造成困惑。
	totpIssuerName = "BabelPlus"
)

// newTOTPSecret 生成一枚新的 base32 secret（大写、无填充）。
//
// 🔴 **返回的是「明文形态」，它只有两个合法去处：加密后入库，以及一次性放进
// TotpEnrollment 响应体。不落日志、不进审计、不进错误信息。**
// audit_logs 是 append-only、永不删除的表，一份写进去的凭据是**永久**写进去的。
func newTOTPSecret() (string, error) {
	buf := make([]byte, totpSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 TOTP secret 失败: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// encryptTOTPSecret 把 base32 明文 secret 加密成 admin_users.totp_secret_enc。
//
// 🔴 **密文形态必须与 middleware/admin.go 的 decryptTOTPSecret 逐字对齐**：
//
//	nonce(12) || ciphertext || tag(16)，明文是 **base32 字符串**（不是原始字节）。
//	那一侧的注释写着「签发侧尚未实现，这条约定由本函数单方面定下，
//	实现签发时必须回来对齐」—— 这里就是那个签发侧。
//	对不齐的现象不是报错，是**所有新管理员的验证码都不对**，
//	而排查方向会先指向时钟、再指向 app，最后才会有人想到密文格式。
//
// 密钥来自 Secret Manager（BP_ADMIN_TOTP_ENC_KEY，32 字节）。
// 长度不对时直接失败：aes.NewCipher 会拒绝，我们把它包成一句人能读懂的话。
func encryptTOTPSecret(key []byte, secret string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("TOTP 加密密钥不可用（需要 32 字节 AES-256 密钥）: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("构造 GCM 失败: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce 失败: %w", err)
	}
	// Seal 的第一个参数是 dst：把 nonce 作为前缀，输出就是 nonce||ct||tag。
	return gcm.Seal(nonce, nonce, []byte(secret), nil), nil
}

// otpauthURL 拼 `otpauth://totp/...`，即二维码的内容。
//
// ⚠️ 三个参数（algorithm/digits/period）显式写出来而不是靠默认值：
// middleware 那侧写死了 SHA1 / 6 位 / 30 秒，而各家 app 的默认值并不完全一致。
// 少写一个的现象是「某些人的码永远不对」，且只在那一款 app 上复现。
//
// label 里的 issuer 前缀与 query 里的 issuer 参数都要有：前者决定
// Authenticator 里的分组显示，后者是较新的规范字段，两者都写才在新旧 app 上都对。
func otpauthURL(issuer, email, secret string) string {
	label := url.PathEscape(issuer + ":" + email)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// ============================================================
// createAdmin（D15：L1 + L3）
// ============================================================

// createAdminNextStepResponse 是 createAdmin 的 201，多带一个指向 reset-totp 的头。
//
// 见文件头「createAdmin 造出来的管理员登不进去」那一节：
// 响应体是 AdminAccount，装不下绑定材料，而少跑一次 reset-totp 那个人就进不来。
// 头不是好的通知渠道，但它是当前契约下唯一的在场位置。
type createAdminNextStepResponse struct {
	gen.CreateAdmin201JSONResponse
	NextStepPath string
}

func (r createAdminNextStepResponse) VisitCreateAdminResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", r.Headers.Location)
	w.Header().Set("X-Next-Step", "POST "+r.NextStepPath)
	w.Header().Set("Warning", `199 - "admin created without usable 2FA; call POST /api/v1/admin/admins/{id}/reset-totp to obtain the enrollment secret"`)
	w.WriteHeader(http.StatusCreated)
	return json.NewEncoder(w).Encode(r.Body)
}

// CreateAdmin 实现 POST /api/v1/admin/admins。**D15：L1 + L3。**
//
// ⚠️ **L1 在这个端点上无法表达**（契约缺口）：`AdminAccountCreateRequest` 只有
// `{email, permissions, reason}`，**没有 confirmation 字段**，而 openapi 的
// summary 又写着「D15：L1 + L3」。这不是可以自己补上的 —— L1 的形态是
// 「服务端查出目标对象的标识串再比对」，而新建时那个对象**还不存在**，
// 服务端没有任何东西可查。所以本端点实际强制的是 L2 + L3 + 角色闸。
//
// ⚠️ 三个 NOT NULL 列由 handler 现场生成：password_hash（argon2id 一次性随机口令，
// 明文**不进响应也不进日志** —— 第一阶段走 IAP，口令列只是占位）、
// totp_secret_enc、role（契约里没有这个字段，钉死 'support'：
// 「新建时忘了降权」比「新建后忘了升权」危险一个量级）。
func (s *Server) CreateAdmin(ctx context.Context, req gen.CreateAdminRequestObject) (gen.CreateAdminResponseObject, error) {
	if req.Body == nil {
		return gen.CreateAdmin422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空"),
		}, nil
	}
	// ---- L2 ----
	reason, ok := validAdminReason(req.Body.Reason)
	if !ok {
		return gen.CreateAdmin422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("必须填写新建原因，且不少于 %d 个字符", adminReasonMinRunes),
				detail("reason", "≥ 8 字符，会进审计日志")),
		}, nil
	}
	email := normalizeEmail(string(req.Body.Email))
	if !validEmail(email) {
		// oapi-codegen 生成的服务端**不做** format: email 校验（没挂 request validator），
		// 所以这里是唯一一道闸。
		return gen.CreateAdmin422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "邮箱格式不正确", detail("email", "格式不正确")),
		}, nil
	}
	// ---- L4：权限位映射（授不了的一律 422，绝不假装成功）----
	markPaid, exportCSV, badPerm, why, err := adminPermissionGrant(req.Body.Permissions)
	if err != nil {
		return gen.CreateAdmin422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("权限位 %s 无法通过本接口授予：%s", badPerm, why),
				detail("permissions", string(badPerm))),
		}, nil
	}

	admin, actor, err := s.adminActor(ctx)
	if err != nil {
		return gen.CreateAdmin500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "无法确定操作者身份", err),
		}, nil
	}
	// ---- 角色闸（见 requireOwnerRole 的注释）----
	if !requireOwnerRole(admin) {
		s.logger.WarnContext(ctx, "非 owner 角色尝试新建管理员",
			"admin_id", admin.AdminID, "role", admin.Role)
		return gen.CreateAdmin403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"只有 owner 可以新建管理员"),
		}, nil
	}
	// ---- L3 ----
	if fb, ie := s.adminStepUp(ctx, req.Params.XTOTPCode); fb != nil {
		return gen.CreateAdmin403JSONResponse{ErrForbiddenJSONResponse: *fb}, nil
	} else if ie != nil {
		return gen.CreateAdmin500JSONResponse{ErrInternalJSONResponse: *ie}, nil
	}

	// 一次性随机口令：只为填 NOT NULL 的 password_hash。
	// 明文在这个函数里生成、用一次、随栈消失 —— 它没有任何去处
	// （AdminAccount 里没有字段，日志里不能写），而这恰恰是对的：
	// 管理面走 IAP，口令这条路在鉴权链上根本不存在（mw.AdminRecord 上没有 password_hash）。
	throwaway, err := randomToken(32)
	if err != nil {
		return gen.CreateAdmin500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "生成初始口令失败", err),
		}, nil
	}
	pwHash, err := hashPassword(ctx, throwaway)
	if err != nil {
		return gen.CreateAdmin500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "计算口令哈希失败", err),
		}, nil
	}
	secret, err := newTOTPSecret()
	if err != nil {
		return gen.CreateAdmin500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "生成 TOTP secret 失败", err),
		}, nil
	}
	secretEnc, err := encryptTOTPSecret(s.cfg.AdminTOTPEncKey, secret)
	if err != nil {
		return gen.CreateAdmin500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "加密 TOTP secret 失败", err),
		}, nil
	}

	var created dbgen.CreateAdminAccountRow
	err = audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		row, err := q.CreateAdminAccount(ctx, dbgen.CreateAdminAccountParams{
			Email:         email,
			PasswordHash:  pwHash,
			TotpSecretEnc: secretEnc,
			// role 钉死 support：契约里没有这个字段，而给它一个更大的默认值是错的。
			Role: middleware.RoleSupport,
			// iap_subject 留空：第一次登录时 checkAdminUsable 允许「还没绑」，
			// 绑定发生在那一刻。这里猜一个 sub 是猜不出来的（它是 Google 侧的标识）。
			IapSubject:        nil,
			PermMarkOrderPaid: markPaid,
			// perm_refund(D7) 与 perm_adjust_balance(D10) 在契约的 AdminPermission
			// 枚举里**没有对应值**，所以本接口永远只能把它们建成 false。
			// 这不是保守默认，是「表达不出来」——已登记为契约缺口。
			PermRefund:        false,
			PermAdjustBalance: false,
			PermExportCsv:     exportCSV,
		})
		if err != nil {
			return audit.Entry{}, err
		}
		created = row
		return audit.Entry{
			Action:     "D15.admin.create",
			TargetType: "admin_user",
			TargetID:   strconv.FormatInt(row.ID, 10),
			Reason:     reason,
			// 创建操作没有 before：nil 写进库是 SQL NULL（不是 JSON 的 null），
			// `WHERE before_value IS NULL` 只命中前者。
			After: map[string]any{
				"email":                row.Email,
				"role":                 row.Role,
				"perm_mark_order_paid": row.PermMarkOrderPaid,
				"perm_refund":          row.PermRefund,
				"perm_adjust_balance":  row.PermAdjustBalance,
				"perm_export_csv":      row.PermExportCsv,
				// 🔴 **不记 secret，明文密文都不记。** 这里记的是「他还不能登录」这个事实 ——
				// 事后有人问「为什么这个管理员从来没登录过」，这一行就是答案。
				"needs_totp_enrollment": true,
			},
		}, nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			// ⚠️ 契约给这个端点**没有声明 409**（只有 201/403/422/500），所以映射成 422。
			//
			// message 必须点出「可能是被停用的管理员占着」，因为这件事取决于
			// **0019 有没有灌进去**，而调用方不可能知道：
			//   · 0019 之前：`admin_users_email_uk` 是**全表**唯一索引（0002:73，
			//     不像 users_email_uk 带 `WHERE deleted_at IS NULL`），
			//     于是停用之后那个邮箱**永久**不能再用，而 API 上没有 undelete；
			//   · 0019 之后：它变成 `WHERE disabled_at IS NULL` 的部分索引，
			//     邮箱可以重用，撞 23505 就真的只是「有一位在职管理员用着它」。
			// 不说这句话的话，「同一个人离职再入职」会变成一次查不出原因的 422。
			return gen.CreateAdmin422JSONResponse{
				ErrUnprocessableJSONResponse: s.unprocessable(ctx,
					"该邮箱已被占用；若它属于一位已停用的管理员，请先确认迁移 0019 已执行（在那之前停用的管理员会永久占住邮箱）",
					detail("email", "已存在")),
			}, nil
		}
		return gen.CreateAdmin500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "新建管理员失败", err),
		}, nil
	}

	nextStep := fmt.Sprintf("/api/v1/admin/admins/%d/reset-totp", created.ID)
	// 固定文案，供 monitoring 建 log-based metric：一个建完之后没跑 reset-totp 的
	// 管理员是**进不来**的，而这件事没有任何其他信号。
	s.logger.WarnContext(ctx,
		"bp_admin_created_without_enrollment 新管理员已创建，但他现在**登不进去** —— "+
			"必须立刻对同一个 id 跑 resetAdminTotp 并把绑定材料当面交给本人",
		"admin_id", created.ID, "email", created.Email, "next_step", nextStep,
		"created_by", admin.AdminID)

	body := gen.CreateAdmin201JSONResponse{}
	body.Body.Data = adminAccountView(created.ID, created.Email, created.Role,
		created.PermMarkOrderPaid, created.PermExportCsv, created.TotpEnabled,
		created.LastLoginAt, created.CreatedAt)
	body.Body.Meta = s.meta(ctx)
	body.Headers.Location = fmt.Sprintf("/api/v1/admin/admins/%d", created.ID)
	return createAdminNextStepResponse{CreateAdmin201JSONResponse: body, NextStepPath: nextStep}, nil
}

// ============================================================
// resetAdminTotp（D15：L1 + L3）
// ============================================================

type adminTotpResetQuerier interface {
	LockAdminAccountTarget(ctx context.Context, adminID int64) (dbgen.LockAdminAccountTargetRow, error)
	ResetAdminAccountTotp(ctx context.Context, arg dbgen.ResetAdminAccountTotpParams) (dbgen.ResetAdminAccountTotpRow, error)
}

// resetAdminTotpTx 是 D15 重置 TOTP 的事务体。
//
// 🔴 **审计的改前/改后值里只有时间戳，没有 secret（明文密文都不行）。**
//
//	audit_logs 是 append-only、永不删除的表，一份写进去的凭据是**永久**写进去的；
//	而这张表可能被导出、被拷进工单、被贴进聊天窗口。
//	「谁在什么时候给谁换了钥匙」已经是这条记录需要回答的全部问题。
//
// ⚠️ 换钥匙的那一刻旧钥匙立刻失效、新钥匙立刻生效，**没有**「两把都能用」的过渡窗口
//
//	（totp_confirmed_at 直接写 now()，因为它 NOT NULL、库里不存在「待确认」状态）。
//	所以后台必须**先**把二维码交到本人手上再点确认；顺序错了那个人就进不来了，
//	而他自己没有任何自助恢复入口。
func resetAdminTotpTx(ctx context.Context, q adminTotpResetQuerier, adminID int64, confirmation, reason string, secretEnc []byte) (dbgen.ResetAdminAccountTotpRow, audit.Entry, error) {
	target, err := q.LockAdminAccountTarget(ctx, adminID)
	if err != nil {
		return dbgen.ResetAdminAccountTotpRow{}, audit.Entry{}, err
	}
	if !confirmationMatches(target.Email, confirmation) {
		return dbgen.ResetAdminAccountTotpRow{}, audit.Entry{}, errAdminConfirmationMismatch
	}
	row, err := q.ResetAdminAccountTotp(ctx, dbgen.ResetAdminAccountTotpParams{
		AdminID:       adminID,
		TotpSecretEnc: secretEnc,
	})
	if err != nil {
		return dbgen.ResetAdminAccountTotpRow{}, audit.Entry{}, err
	}
	return row, audit.Entry{
		Action:     "D15.admin.reset_totp",
		TargetType: "admin_user",
		TargetID:   strconv.FormatInt(row.ID, 10),
		Reason:     reason,
		Before: map[string]any{
			"totp_confirmed_at": tptr(row.BeforeTotpConfirmedAt),
			"disabled_at":       tptr(row.BeforeDisabledAt),
		},
		After: map[string]any{
			"totp_confirmed_at": tptr(row.AfterTotpConfirmedAt),
			"email":             row.Email,
		},
	}, nil
}

// ResetAdminTotp 实现 POST /api/v1/admin/admins/{id}/reset-totp。**D15：L1 + L3。**
//
// 🔴 这个端点是**新管理员唯一的入场券**（见文件头）。它同时是「换钥匙」与「发钥匙」，
// 因为 `totp_confirmed_at NOT NULL` 让数据库里不存在「待绑 2FA」这个状态。
func (s *Server) ResetAdminTotp(ctx context.Context, req gen.ResetAdminTotpRequestObject) (gen.ResetAdminTotpResponseObject, error) {
	if req.Body == nil {
		return gen.ResetAdminTotp422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空"),
		}, nil
	}
	reason, ok := validAdminReason(req.Body.Reason)
	if !ok {
		return gen.ResetAdminTotp422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("必须填写重置原因，且不少于 %d 个字符", adminReasonMinRunes),
				detail("reason", "≥ 8 字符，会进审计日志")),
		}, nil
	}
	admin, actor, err := s.adminActor(ctx)
	if err != nil {
		return gen.ResetAdminTotp500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "无法确定操作者身份", err),
		}, nil
	}
	if !requireOwnerRole(admin) {
		s.logger.WarnContext(ctx, "非 owner 角色尝试重置管理员 TOTP",
			"admin_id", admin.AdminID, "role", admin.Role, "target_admin_id", req.Id)
		return gen.ResetAdminTotp403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"只有 owner 可以重置管理员的二次验证"),
		}, nil
	}
	// ---- L3 ----
	if fb, ie := s.adminStepUp(ctx, req.Params.XTOTPCode); fb != nil {
		return gen.ResetAdminTotp403JSONResponse{ErrForbiddenJSONResponse: *fb}, nil
	} else if ie != nil {
		return gen.ResetAdminTotp500JSONResponse{ErrInternalJSONResponse: *ie}, nil
	}

	secret, err := newTOTPSecret()
	if err != nil {
		return gen.ResetAdminTotp500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "生成 TOTP secret 失败", err),
		}, nil
	}
	secretEnc, err := encryptTOTPSecret(s.cfg.AdminTOTPEncKey, secret)
	if err != nil {
		return gen.ResetAdminTotp500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "加密 TOTP secret 失败", err),
		}, nil
	}

	var row dbgen.ResetAdminAccountTotpRow
	err = audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		var e audit.Entry
		var err error
		row, e, err = resetAdminTotpTx(ctx, q, req.Id, req.Body.Confirmation, reason, secretEnc)
		return e, err
	})
	switch {
	case err == nil:
	case errors.Is(err, errAdminConfirmationMismatch):
		return gen.ResetAdminTotp422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				"确认串与该管理员的邮箱不一致",
				detail("confirmation", "必须逐字等于该管理员的邮箱")),
		}, nil
	case errors.Is(err, pgx.ErrNoRows):
		return gen.ResetAdminTotp404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "管理员不存在")}, nil
	default:
		return gen.ResetAdminTotp500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "重置管理员 TOTP 失败", err),
		}, nil
	}

	// 🔴 日志里**只有 id 与邮箱，没有 secret**。这条日志的用途是让
	// 「谁的钥匙在什么时候被换过」在 Cloud Logging 里也能查到（审计表是另一份）。
	s.logger.WarnContext(ctx, "bp_admin_totp_reset 管理员的二次验证已重置，旧验证码立刻失效",
		"target_admin_id", row.ID, "target_email", row.Email, "by_admin_id", admin.AdminID)

	return gen.ResetAdminTotp200JSONResponse{
		// 明文 secret 出现且**仅**出现在这里一次：不落库、不进日志、不进审计。
		Data: gen.TotpEnrollment{
			Secret:     secret,
			OtpauthUrl: otpauthURL(totpIssuerName, row.Email, secret),
		},
		Meta: s.meta(ctx),
	}, nil
}

// ============================================================
// deleteAdmin（D16：L1 + L3）
// ============================================================

// errAdminSelfDelete：管理员停用自己。
var errAdminSelfDelete = errors.New("不能停用自己")

type adminDisableQuerier interface {
	LockAdminAccountTarget(ctx context.Context, adminID int64) (dbgen.LockAdminAccountTargetRow, error)
	DisableAdminAccount(ctx context.Context, adminID int64) (dbgen.DisableAdminAccountRow, error)
}

// deleteAdminTx 是 D16 的事务体。
//
// 🔴 **「删除」= 停用（写 disabled_at），不是 DELETE FROM admin_users。**
//
//	硬删会让这个人过去的**每一条** D1–D16 审计记录的 admin_user_id 变成 NULL
//	（audit_logs 的外键是 ON DELETE SET NULL），而契约的 AuditLogEntry
//	把 admin_id 列进了 required 又没有 admin_email 字段 —— 那些记录在 API 上
//	会变成认不出人的孤儿。§6.3 存在的全部意义是「事后能重建谁做了什么」，
//	一个会削弱它的删除按钮不该存在。
//	鉴权侧不缺任何东西：middleware 的 checkAdminUsable 读 `disabled_at IS NOT NULL`
//	直接 403，且 step-up 路径会**再跑一遍**同一个判断。
//
// ⚠️ 幂等：对已停用的再跑一次，SQL 的 coalesce 保住第一次停用的时刻，审计记成 (t → t)。
func deleteAdminTx(ctx context.Context, q adminDisableQuerier, targetID, actorID int64, confirmation, reason string) (audit.Entry, error) {
	target, err := q.LockAdminAccountTarget(ctx, targetID)
	if err != nil {
		return audit.Entry{}, err
	}
	if !confirmationMatches(target.Email, confirmation) {
		return audit.Entry{}, errAdminConfirmationMismatch
	}
	if targetID == actorID {
		// 🔴 **自我停用必须拒绝，而且要在 L1 之后拒**（先比对确认串，
		// 否则这里会变成一条不需要确认串的分支）。
		// 停用自己的直接后果是**当场把自己锁在门外**：middleware 的 checkAdminUsable
		// 读 disabled_at 立刻 403，而 API 上**没有 undelete 端点**，
		// 冻结的 openapi 也没给。恢复只能靠直接连库改一行 ——
		// 而直接改库正是权限系统存在的意义被绕过的那一刻。
		// （0019 之后同邮箱可以重建一个新账号，但那是**另一个 id**：
		//  他过去那些审计记录仍然指着被停用的那一条，指认不到新账号上。）
		return audit.Entry{}, errAdminSelfDelete
	}
	row, err := q.DisableAdminAccount(ctx, targetID)
	if err != nil {
		return audit.Entry{}, err
	}
	return audit.Entry{
		Action:     "D16.admin.disable",
		TargetType: "admin_user",
		TargetID:   strconv.FormatInt(row.ID, 10),
		Reason:     reason,
		Before: map[string]any{
			"disabled_at":          tptr(row.BeforeDisabledAt),
			"role":                 row.BeforeRole,
			"perm_mark_order_paid": row.BeforePermMarkOrderPaid,
			"perm_refund":          row.BeforePermRefund,
			"perm_adjust_balance":  row.BeforePermAdjustBalance,
			"perm_export_csv":      row.BeforePermExportCsv,
		},
		After: map[string]any{
			"disabled_at": tptr(row.AfterDisabledAt),
			// 🔴 email 必须进快照。停用之后这一行仍然在表里，但
			// `AuditLogEntry` 上只有 target_id（一个数字），
			// 事后翻审计的人靠它才知道「被停用的是谁」。
			"email": row.Email,
		},
	}, nil
}

// DeleteAdmin 实现 DELETE /api/v1/admin/admins/{id}。**D16：L1 + L3。软停用，不是硬删。**
func (s *Server) DeleteAdmin(ctx context.Context, req gen.DeleteAdminRequestObject) (gen.DeleteAdminResponseObject, error) {
	if req.Body == nil {
		return gen.DeleteAdmin422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空"),
		}, nil
	}
	reason, ok := validAdminReason(req.Body.Reason)
	if !ok {
		return gen.DeleteAdmin422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("必须填写停用原因，且不少于 %d 个字符", adminReasonMinRunes),
				detail("reason", "≥ 8 字符，会进审计日志")),
		}, nil
	}
	admin, actor, err := s.adminActor(ctx)
	if err != nil {
		return gen.DeleteAdmin500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "无法确定操作者身份", err),
		}, nil
	}
	if !requireOwnerRole(admin) {
		s.logger.WarnContext(ctx, "非 owner 角色尝试停用管理员",
			"admin_id", admin.AdminID, "role", admin.Role, "target_admin_id", req.Id)
		return gen.DeleteAdmin403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"只有 owner 可以停用管理员"),
		}, nil
	}
	// ---- L3 ----
	if fb, ie := s.adminStepUp(ctx, req.Params.XTOTPCode); fb != nil {
		return gen.DeleteAdmin403JSONResponse{ErrForbiddenJSONResponse: *fb}, nil
	} else if ie != nil {
		return gen.DeleteAdmin500JSONResponse{ErrInternalJSONResponse: *ie}, nil
	}

	err = audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		return deleteAdminTx(ctx, q, req.Id, admin.AdminID, req.Body.Confirmation, reason)
	})
	switch {
	case err == nil:
		// ⚠️ 这条日志说的两件事都不是废话：
		//  · **没有 undelete**（冻结的 openapi 没给这个端点），撤销只能改库；
		//  · 0019 之后这个邮箱可以被新账号重用，但 middleware 的
		//    `LookupAdminByIAPEmail` 目前**不带 `disabled_at IS NULL`** 且用 QueryRow，
		//    会静默取到先插入的那条**停用行**并 403 —— 也就是说
		//    「离职再入职」在登录路径上会变成一个稳定的 403，而不是「登不进去偶发」。
		//    修法是那条查询补上软删条件（不在本文件范围内，已在交付说明里登记）。
		s.logger.WarnContext(ctx,
			"bp_admin_disabled 管理员已停用；API 上没有撤销入口。若之后要用同一个邮箱重建，"+
				"先确认 middleware 的 LookupAdminByIAPEmail 已补上 disabled_at IS NULL，否则新账号登录会稳定 403",
			"target_admin_id", req.Id, "by_admin_id", admin.AdminID)
		return gen.DeleteAdmin204Response{
			Headers: gen.DeleteAdmin204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
		}, nil
	case errors.Is(err, errAdminConfirmationMismatch):
		return gen.DeleteAdmin422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				"确认串与该管理员的邮箱不一致",
				detail("confirmation", "必须逐字等于该管理员的邮箱")),
		}, nil
	case errors.Is(err, errAdminSelfDelete):
		return gen.DeleteAdmin422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				"不能停用自己：停用会立刻使你无法登录，而 API 上没有撤销停用的入口，恢复只能直接改库",
				detail("id", "不能等于当前操作者")),
		}, nil
	case errors.Is(err, pgx.ErrNoRows):
		return gen.DeleteAdmin404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "管理员不存在")}, nil
	default:
		return gen.DeleteAdmin500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "停用管理员失败", err),
		}, nil
	}
}
