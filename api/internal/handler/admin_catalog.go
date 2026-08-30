package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"golang.org/x/sync/errgroup"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/audit"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 管理面「目录与运营」这一组：套餐 · 优惠码 · 公告 · 邀请 · 佣金调整 · 工单 ·
// 审计日志 · 系统配置 · 流量统计与导出 · 运营看板 · 邮件（群发与送达日志）。
//
// 这一组端点数量最多但形状最简单 —— 绝大多数是 CRUD。注释密度因此不平均地
// 压在四类地方：**契约表达不出来的强制层**、**缺表导致的 501**、
// **量纲/枚举换算错了不会报错的地方**，以及**并发与截断**。
//
// ============================================================
// 一、五个刻意保持 501 的 operation（缺表 / 缺列 / 卡在未批准的裁决）
// ============================================================
//
// 下面五个**没有**在本文件里实现，它们继续落在 Unimplemented 的 501 上。
// 这不是漏写：把它们实现成「返回一份写死在 Go 里的清单」或者「塞进 settings 的
// JSONB 再假装是表」会得到一个**看起来能用**的后台页面，而它背后什么都没有 ——
// 那比 501 糟得多，因为 501 至少是响亮的。
//
//  1. **ListAdminMailTemplates** —— `mail_templates` 表不存在（18 支迁移逐条核过）。
//     契约的 `MailTemplate{id,key,subject,body,enabled}` 在库里没有落点：
//     `email_log.template` 只是**模板键的字符串快照**（'verify_code' / 'expire_remind'），
//     不是模板正文的存储。
//  2. **UpdateAdminMailTemplate** —— 同上。额外一条理由：`MailTemplatePatch` 要求
//     前后像进审计（§6.3 第 2 条），而对 `settings` 的 JSONB 做部分更新
//     **拿不到干净的字段级快照** —— 那正是「不要用 JSONB 顶上」的决定性理由。
//  3. **ListAdminDomains** / 4. **CreateAdminDomain** / 5. **DeleteAdminDomain**
//     —— `domains` 表不存在，且**卡在两份未批准的 ADR 上**：ADR 0011 §7.2 给了字段形状、
//     ADR 0010 §1.3/§8.1 定了池划分，两份状态都是「提案，未批准」。
//     data-model §16 的条目明确「本条不划掉」。
//     🔴 更要紧的是：ADR 0011 §7.2 的字段（state/platform/registrable/order/serial）
//     与冻结契约的 `Domain`（hostname/role/enabled/reachable/last_checked_at）
//     **是两套不同的模型**。先按契约那一套塞进 JSONB，等 0011 批准后要做的是
//     「从 JSONB 迁到真表、且模型还换了」的迁移 —— 而它承载的是**失联恢复路径**。
//     另：`reachable` / `last_checked_at` 背后的可达性探活机制本身还不存在，
//     建了表这两列也只能手工维护；把它们做成会自动更新的样子，等于给
//     product-brief §8 那句「域名失联恢复 ≤ 30 分钟」提供一个**假的机制证据**。
//
// （任务书说「四个」，实核是**五个** —— 域名池是三个 operation 不是两个。）
//
// ============================================================
// 二、四层强制（api-contract §6.2）在本组的落点，以及**契约表达不出来的那些**
// ============================================================
//
// §6.2 的四层与它们的适用范围（该表以 api-contract 为准，不是以任务书为准）：
//
//	L1 确认串    D3 D4 D6 D10 D15 D16   —— 本组**一条都没有**
//	L2 必填原因  D1 D2 D3 D6 D7 D10 D11 —— 本组只有 D11（调整佣金）在表内
//	L3 TOTP     D3 D5 D6 D10 D15 D16   —— 本组**一条都没有**
//	L4 权限位    D6 D14                  —— 本组只有 D14（导出统计）
//
// 🔴 **本文件因此不调用 `mw.RequireStepUp`，这是刻意的、不是漏掉。**
//     §6.2 的 L3 行里没有 D8/D11/D11b/D12/D13/D14 任何一个。给它们加上 step-up
//     的直接后果是：前端不会发 `X-TOTP-Code`（契约里这些端点没有这个头），
//     于是「发布公告」变成一个**谁也点不动的按钮**，而运维的第一反应会是把
//     TOTP 整体关掉。要加 L3 的正确顺序是先改 openapi 声明该头，再改这里。
//     加的位置：每个 operation 校验 reason 之后、开事务之前，一行
//     `if e := s.cfg.adminAuthConfig().RequireStepUp(ctx, hdr); e != nil { ... }`。
//
// 🔴 **L2 在四个 operation 上「契约里没有 reason 字段」**，登记如下：
//   - `NoticeUpsert` 没有 `reason` —— 于是 **D12（发布/编辑公告）的必填原因写不进去**。
//     公告兼**域名广播位**（page-inventory §4.4 D12），写错一个域名会把用户导向
//     错误地址，而事后审计里将没有「为什么改」。补法是给 NoticeUpsert 加 reason。
//   - 四个 `DELETE` 端点（deleteAdminPlan / deleteAdminCoupon / deleteAdminNotice /
//     deleteAdminDomain）**在契约上没有请求体**，所以 reason 与 confirmation
//     两者都无处安放。本文件的做法是：照常写审计（before 快照是删除唯一的证据），
//     `reason` 落 NULL，并在服务端日志里留一条 WARN 说明这条审计缺原因。
//     绝不为此发明一个 `?reason=` 查询参数 —— 那是在冻结契约之外私自加接口。
//
// 🔴 **L1 在 D8 上表达不出来。** `PlanUpsert` 没有 `confirmation` 字段，
//     于是「改套餐只影响新订单」这句话没有地方让服务端与操作者对质。
//     本文件把那句话固化成常量 `planPricingScopeNotice`，写进 **D8 的每一条审计**
//     （after 快照的 `pricing_scope_note`），这样至少「他当时被告知过什么」是有记录的。
//     这**不等于** L1：一个直接 curl 的人不会读到它。缺口已登记。
//
// 🔴 **L4 的五个 `admin.*.write` 在库里没有列。** openapi 的 `AdminPermission`
//     有 7 个枚举，而 `admin_users` 只有 4 个 boolean 列（mark_order_paid / refund /
//     adjust_balance / export_csv）。也就是说 `admin.plan.write` / `admin.settings.write` /
//     `admin.ticket.write` **不是独立权限位，只能由 role 推**。本文件的推法写死在
//     `catalogRoleCanWrite` / `catalogRoleCanWriteTicket` 里，并在注释里说明它不是
//     §6.2 想要的那个 L4。唯一真正的权限位检查在 `ExportAdminStats`（D14 → perm_export_csv）。
//
// ============================================================
// 三、审计（api-contract §6.3）
// ============================================================
//
// **本组每一个写操作都写审计**，不只是危险操作 —— §6.3 的规则是「管理面的写操作都记」。
// 一律走 `audit.InTx`（业务写与审计写同一事务，审计写失败则整个操作回滚）。
// 唯一的例外是 `ExportAdminStats`：它是纯读，没有业务事务可搭，走 `audit.Write`；
// 但**审计写失败时导出也失败** —— D14 的全部要求就是那条审计，
// 「数据给了、记录没留」正是它要防的东西。
//
// 改前/改后值一律取查询自己给的 `before_* / after_*`（一条语句里两个 CTE，
// 或 `UPDATE … FROM t AS prev`）。**不要**改成「先 SELECT 再 UPDATE」：
// 那会在两条语句之间留一个窗口，事后审计里会出现一个从未紧接着 after 出现过的 before。

// ============================================================
// 共用：身份 · 四层强制 · 审计事务 · 错误分类
// ============================================================

// L2 的下限 adminReasonMinRunes 在 admin_common.go（管理面四个文件共用一份）。
// 按 **rune** 数而不是字节：契约写的是「8 字符」，而一条中文原因
// （「网关回调丢失」= 6 字 18 字节）按字节算会轻松通过，按字符算才是 8 个字。

const (
	// catalogReasonMaxRunes 是原因的上限。
	//
	// 契约只给了 minLength，没有 maxLength。而 `audit_logs.reason` 是无上限的 text，
	// 且这张表 **append-only、永不删除**。不设上限意味着一次改价就能往审计表里
	// 塞一兆文本。2000 远超任何真实的操作说明。
	catalogReasonMaxRunes = 2000
)

// planPricingScopeNotice 是 D8 的确认文案。见文件头「L1 在 D8 上表达不出来」。
//
// 🔴 这句话必须逐字进每一条 D8 审计：**改套餐只影响新订单**。
// 已售出的 `transfer_enable` 在当前周期内不可撤回（定价修订 A2），
// 历史订单的价格快照 `orders.price_monthly_at_order` 一行都不回改
// （退款扣减读的是它，不是 `plans.price_monthly` 这个活列 —— 否则涨价后退款额变小，
// 用户会认为我们改价来少退钱）。
// 「让老用户也享受新配额」是另一个动作（批量改 users = D1，直接等于送钱），
// 走另一条端点、另一套确认。
const planPricingScopeNotice = "改套餐只影响新订单：已售出的流量额度在当前周期内不可撤回，" +
	"历史订单的价格快照不会回改。要让存量用户享受新配额是另一个操作（D1 改用户权利）。"

// catalogErrKind 是本组 handler 在事务体里抛出的业务错误分类。
//
// 为什么要一个分类而不是每处直接构造响应对象：**响应对象在事务里构造不出来** ——
// `audit.InTx` 的回调只能返回 `(audit.Entry, error)`，而每个 operation 的
// 404/409/422 是不同的具体类型（`UpdateAdminPlan404JSONResponse` 之类）。
// 分类让事务体只表达「这是哪一类失败」，由调用点翻成它自己的那个类型。
type catalogErrKind int

const (
	catalogErrNotFound catalogErrKind = iota
	catalogErrConflict
	catalogErrUnprocessable
)

// catalogOpError 是带分类与中文文案的业务错误。
type catalogOpError struct {
	kind  catalogErrKind
	msg   string
	field string // 非空时进 ErrorDetail.field
}

func (e *catalogOpError) Error() string { return e.msg }

func catalogNotFound(msg string) error { return &catalogOpError{kind: catalogErrNotFound, msg: msg} }
func catalogConflict(msg string) error { return &catalogOpError{kind: catalogErrConflict, msg: msg} }
func catalogUnprocessable(field, msg string) error {
	return &catalogOpError{kind: catalogErrUnprocessable, msg: msg, field: field}
}

// asCatalogOpError 从（可能被包装过的）错误里取出分类。
func asCatalogOpError(err error) (*catalogOpError, bool) {
	var e *catalogOpError
	ok := errors.As(err, &e)
	return e, ok
}

// isCheckViolation（23514）在 auth.go，与 isUniqueViolation 并排。本组有三处 CHECK
// 会被正常输入触发（plans_cycle_needs_monthly / plans.speed_limit_mbps > 0 /
// commissions.amount >= 0），全部必须翻成 422 —— 500 会让管理员反复重试一个
// 被数据库正确拒绝的请求。

// catalogAuditRunner 是本文件对 `audit.InTx` 的窄化。
//
// 为什么包一层而不是直接调 `audit.InTx`：`audit.InTx` 的回调收的是**具体类型**
// `*dbgen.Queries`，单测里造不出来（它要一个真的 pgx.Tx）。窄化成 `dbgen.Querier`
// 之后，「审计写失败 ⇒ 业务写回滚」这条规则可以在不起数据库的情况下被测到 ——
// 而那条规则正是 §6.3 第 1 条，它没有测试就等于没有。
//
// ⚠️ 这一层**不放松**任何约束：生产实现里 fn 拿到的仍然是事务上的 Queries，
// 「在事务外写审计」依旧需要刻意绕路才能做到。
type catalogAuditRunner func(
	ctx context.Context,
	actor audit.Actor,
	fn func(context.Context, dbgen.Querier) (audit.Entry, error),
) error

// catalogAudit 返回生产实现。
func (s *Server) catalogAudit() catalogAuditRunner {
	return func(ctx context.Context, actor audit.Actor,
		fn func(context.Context, dbgen.Querier) (audit.Entry, error),
	) error {
		return audit.InTx(ctx, s.db.Pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
			return fn(ctx, q)
		})
	}
}

// errNoAdminAuth（装配错误 → 500，不是 403）在 admin_common.go。

// catalogActor 组装 `audit.Actor`。
//
// 🔴 **IP 采集不到时整个操作失败**，不回退到 0.0.0.0。这是 audit.go 已经裁决过的：
// `audit_logs.request_ip` 是证据，一条写着 0.0.0.0 的审计记录会在事后被当成真实来源读，
// 而它其实什么都没说。宁可让操作失败 —— 那至少是响亮的。
// 现象是「所有管理面写操作都 500」，日志里有 bp_admin_audit_no_ip，一眼能定位到
// 「RequestBinding 中间件没挂」或者「XFF 信任配置错了」。
func (s *Server) catalogActor(ctx context.Context) (audit.Actor, *middleware.AdminAuth, error) {
	admin, ok := middleware.AdminFrom(ctx)
	if !ok || admin == nil {
		return audit.Actor{}, nil, errNoAdminAuth
	}
	meta := s.requestMetadata(ctx)
	if meta.IP == nil {
		s.logger.ErrorContext(ctx, "bp_admin_audit_no_ip 采集不到来源 IP，管理面写操作一律拒绝",
			"admin_id", admin.AdminID, "request_id", middleware.RequestIDFrom(ctx))
		return audit.Actor{}, admin, errors.New("采集不到来源 IP，审计无法写入")
	}
	a := audit.Actor{
		// Email 取 admin_users 那一份（mw.AdminAuth.Email 已经是那一份），
		// 不是 IAP 断言里那一份：审计要留的证据是「本系统认为他是谁」。
		AdminID: admin.AdminID,
		Email:   admin.Email,
		IP:      *meta.IP,
	}
	if meta.UserAgent != nil {
		a.UserAgent = *meta.UserAgent
	}
	return a, admin, nil
}

// catalogCheckReason 是 §6.2 L2 的服务端校验。
//
// 🔴 **不能只靠 openapi 的 `minLength: 8`。** 请求校验中间件是否挂载、
// 是否对这个 schema 生效，都不是 handler 能确定的事；而 L2 的全部意义在于
// 「审计里那条 reason 真的说了点什么」。少这一道，一次 `{"reason":" "}`
// 就能通过，而审计表里会多一条什么都没解释的记录。
func catalogCheckReason(reason string) error {
	r := strings.TrimSpace(reason)
	if n := len([]rune(r)); n < adminReasonMinRunes {
		return catalogUnprocessable("reason",
			fmt.Sprintf("原因至少 %d 个字符（当前 %d），它会进审计日志", adminReasonMinRunes, n))
	}
	if n := len([]rune(r)); n > catalogReasonMaxRunes {
		return catalogUnprocessable("reason",
			fmt.Sprintf("原因最多 %d 个字符（当前 %d）", catalogReasonMaxRunes, n))
	}
	return nil
}

// catalogRoleCanWrite 判断这个角色能不能做「配置类」写操作
// （套餐 / 优惠码 / 公告 / 邀请 / 佣金 / 配置 / 群发）。
//
// 🔴 **这不是 §6.2 想要的 L4。** 真正的 L4 是「独立权限位，默认不授予，需单独开」，
// 而 `admin.plan.write` / `admin.settings.write` 这几个枚举在 `admin_users` 上
// **没有对应的列**（只有 4 个 boolean：mark_order_paid / refund / adjust_balance / export_csv）。
// 于是这里只能退回到角色：owner 与 admin 能写，support（客服）不能。
//
// 为什么仍然要有这一道：没有它，一个只该处理工单的客服账号可以改全站套餐价格。
// 「角色推出来的权限」比「没有权限检查」强，比「真正的独立权限位」弱 —— 三者必须分清。
// 缺口已登记：补法是给 admin_users 加那 5 个 boolean 列，并把本函数改成读列。
func catalogRoleCanWrite(role string) bool {
	return role == middleware.RoleOwner || role == middleware.RoleAdmin
}

// catalogRoleCanWriteTicket 判断能不能写工单（回复 / 改状态）。
//
// 与上面分开的唯一理由：**support 就是干这个的**。把客服挡在工单写操作之外
// 会让「客服」这个角色除了看什么都做不了，于是现场的解法是把所有人都设成 admin ——
// 那样一来上面那道闸也一起没了。
func catalogRoleCanWriteTicket(role string) bool {
	return role == middleware.RoleOwner || role == middleware.RoleAdmin || role == middleware.RoleSupport
}

// catalogShanghai 是统计口径的时区。
//
// 🔴 **用固定偏移而不是 `time.LoadLocation("Asia/Shanghai")`**：运行镜像里
// 不保证有 tzdata（distroless / scratch 都没有），LoadLocation 会失败并回退到 UTC ——
// 而回退到 UTC 的现象是**日报整体错开一天**（UTC 8 月 29 日 20:00 属于上海的 8 月 30 日），
// 且看起来完全正常，只是每天的数字都是隔壁那天的。
// 中国自 1991 年起没有夏令时，固定 +08:00 与真实的 Asia/Shanghai 在可预见的将来完全一致。
// 口径必须与写入侧 `BulkUpsertStatUserServer` 的 `(now() AT TIME ZONE 'Asia/Shanghai')::date` 一致。
func catalogShanghai() *time.Location { return time.FixedZone("Asia/Shanghai", 8*60*60) }

// catalogStatDate 把一个时刻换算成「上海的那一天」。
func catalogStatDate(t time.Time) pgtype.Date {
	d := t.In(catalogShanghai())
	return pgtype.Date{
		Time:  time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC),
		Valid: true,
	}
}

// catalogRecordAt 把 `stat_date`（date）换算成契约要的 `record_at`（date-time）。
//
// 🔴 **三种 scope 必须共用这一个函数。** 各写一遍是三份会漂移的时区代码，
// 而漂移的现象是「按用户看和按节点看，同一天的总量对不上」——
// 没有任何报错，只有一张永远对不平的报表。
//
// 语义：上海当天 00:00 的那个 UTC 时刻。stat_date 是按上海切的天（0009 列注释），
// 直接把它当成 UTC 的 00:00 发出去会让前端在东八区渲染成「08:00」，
// 于是每一个数据点看起来都晚了 8 小时。
func catalogRecordAt(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	y, m, day := d.Time.Date()
	return time.Date(y, m, day, 0, 0, 0, 0, catalogShanghai()).UTC()
}

// catalogEscapeLike 转义 LIKE / ILIKE 的元字符。
//
// 🔴 **必须在拼 `%…%` 之前做。** 不转义的话：
//   - 一个 `%` = 匹配全部（审计过滤形同虚设）；
//   - 一个 `_` = 匹配任意单字符（安静地多返回一批不相干的记录）；
//   - 一个 `\` = 把后面那个字符吃掉。
//
// 三者都不报错。转义字符用默认的反斜杠（PostgreSQL 的 LIKE 默认 ESCAPE '\'）。
func catalogEscapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// catalogJSONObject 把库里的 jsonb 解成契约要的 `map[string]interface{}`。
//
// ⚠️ jsonb 里存的不一定是对象：`audit.Entry.Before/After` 收的是 any，
// 某天有人传了一个切片，那一列就是 JSON 数组。契约的 AuditLogEntry.before 是 object，
// 硬解会失败。失败时**不丢弃**，包成 `{"value": …}` 交出去 ——
// 审计记录的价值在于「它当时长什么样」，为了迎合类型把它变成 null 是毁证据。
func catalogJSONObject(raw []byte) *map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err == nil && m != nil {
		return &m
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		// 连合法 JSON 都不是（不该发生，jsonb 列保证合法）。原样交出去当字符串。
		v = string(raw)
	}
	wrapped := map[string]interface{}{"value": v}
	return &wrapped
}

// ============================================================
// 套餐（模块 4，D8）—— ListAdminPlans · CreateAdminPlan · UpdateAdminPlan · DeleteAdminPlan
// ============================================================

const (
	// planCodePrefix / planCodeLen 决定自动生成的 `plans.code` 形状（如 `plan_k7m2q9xz`）。
	//
	// 🔴 `code` 是 NOT NULL UNIQUE，而 `PlanUpsert` **里没有这个字段** ——
	//    只能由 handler 生成。规则一旦定下就不能改：`GetPlanByCode` 与运维脚本按它定位套餐。
	// 字符集复用 inviteCodeAlphabet 的小写形态（剔除 0/O/1/I/l）：这个串会出现在
	// 运维脚本、日志与工单里，被人念、被人抄。
	planCodePrefix = "plan_"
	planCodeLen    = 8

	// createPlanRetries 是 code 撞号的重试次数。
	// 撞唯一索引不该变成 500 —— 它是一次重新随机就能解决的事。
	createPlanRetries = 3
)

// planCodeAlphabet 是 planCode 用的小写无歧义字符集（= inviteCodeAlphabet 的小写）。
var planCodeAlphabet = strings.ToLower(inviteCodeAlphabet)

// newPlanCode 生成一个套餐 code。拒绝采样，理由同 randomInviteCode。
func newPlanCode() (string, error) {
	n := len(planCodeAlphabet) // 31
	limit := byte(248)         // 248 = 31 × 8
	out := make([]byte, 0, planCodeLen)
	buf := make([]byte, 1)
	for len(out) < planCodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成套餐 code 随机数失败: %w", err)
		}
		if buf[0] >= limit {
			continue
		}
		out = append(out, planCodeAlphabet[int(buf[0])%n])
	}
	return planCodePrefix + string(out), nil
}

// planKindFromContract 把契约的 `PlanUpsert.type` 映射成 `plans.kind`。
//
// 🔴 **这个映射必须显式，而且 CreateAdminPlan 必须把结果传给 CreatePlan 的第 19 个参数。**
// 0016 刻意不给 kind 一个 DEFAULT（ADR 0013 §4.6）：默认成 'cycle' 会让每一个
// 通过后台建出来的**加油包被静默写成周期套餐**，于是 `POST /orders` 把它推导成
// new/renew/upgrade、走进周期套餐的开通逻辑并**凭空触发一次折抵** —— 一次静默的错误分类，
// 而且要等到有人买了才显形。漏传是数据库拒绝（NOT NULL），当场就知道。
func planKindFromContract(t gen.PlanUpsertType) (string, error) {
	switch t {
	case gen.Period:
		return planKindCycle, nil
	case gen.TrafficPack:
		return planKindPack, nil
	default:
		return "", catalogUnprocessable("type", fmt.Sprintf("未知的套餐类型 %q", string(t)))
	}
}

// planUpsertPrices 是 `PlanUpsert.prices[]` 摊平成的五个价格列。
type planUpsertPrices struct {
	monthly    *int64
	quarterly  *int64
	halfYearly *int64
	yearly     *int64
	onetime    *int64
}

// parsePlanPrices 把契约的 prices[] 摊成五个列，并做四道校验。
//
// ⚠️ 契约的 `PlanPrice.period` 枚举含 `two_yearly` / `three_yearly`，而 `plans`
// **根本没有这两列**、`order_period` 枚举里也没有这两个值（ADR 0013 §4.7 已登记）。
// 以库为准：传这两个周期返回 422 并说明「本系统只有五个周期」，
// **不要**静默丢弃 —— 静默丢弃的现象是「我明明设了两年价，保存后没了」。
func parsePlanPrices(in []gen.PlanPrice, kind string) (planUpsertPrices, error) {
	var p planUpsertPrices
	if len(in) == 0 {
		return p, catalogUnprocessable("prices", "套餐至少要有一个周期的价格，否则它是一个买不了的套餐")
	}
	seen := make(map[gen.PlanPricePeriod]bool, len(in))
	for _, pr := range in {
		if seen[pr.Period] {
			// 重复周期在 SQL 里会静默地「后一条覆盖前一条」，而管理员看到的是保存成功。
			return p, catalogUnprocessable("prices", fmt.Sprintf("周期 %q 出现了两次", string(pr.Period)))
		}
		seen[pr.Period] = true
		if pr.Amount < 0 {
			return p, catalogUnprocessable("prices", fmt.Sprintf("周期 %q 的价格不能是负数", string(pr.Period)))
		}
		amount := pr.Amount
		switch pr.Period {
		case gen.PlanPricePeriodMonthly:
			p.monthly = &amount
		case gen.PlanPricePeriodQuarterly:
			p.quarterly = &amount
		case gen.PlanPricePeriodHalfYearly:
			p.halfYearly = &amount
		case gen.PlanPricePeriodYearly:
			p.yearly = &amount
		case gen.PlanPricePeriodOnetime:
			p.onetime = &amount
		case gen.PlanPricePeriodTwoYearly, gen.PlanPricePeriodThreeYearly:
			return p, catalogUnprocessable("prices",
				fmt.Sprintf("本系统没有 %q 周期（plans 表没有这一列，order_period 枚举里也没有这个值）",
					string(pr.Period)))
		default:
			return p, catalogUnprocessable("prices", fmt.Sprintf("未知的周期 %q", string(pr.Period)))
		}
	}
	// 🔴 `plans_cycle_needs_monthly`（0016）：kind='cycle' 必须有 price_monthly。
	//    理由是 ADR 0013 §3.2 的退款公式按月单价折算 —— 没有月价，那个公式会除到一个
	//    不存在的数上。在 Go 侧先挡一道，是为了给出一句人话；数据库那道 CHECK 仍然是真闸门。
	if kind == planKindCycle && p.monthly == nil {
		return p, catalogUnprocessable("prices",
			"周期套餐必须有月付价格：退款金额按月单价折算，没有月价的周期套餐退不了款")
	}
	return p, nil
}

// planSpeedLimit 把契约的 speed_limit_mbps 翻成库里的可空列。
//
// 🔴 **0 必须翻成 NULL。** 契约说「第一阶段全部 0（不限）」，而库里
// `speed_limit_mbps integer CHECK (speed_limit_mbps > 0)` —— 不限速是 NULL。
// 不翻的话每一次保存套餐都是一个 23514，现象是「保存套餐莫名 500」。
func planSpeedLimit(v *int32) *int32 {
	if v == nil || *v <= 0 {
		return nil
	}
	return v
}

// planResetMethodFor 给出 `reset_traffic_method` 的默认值。
//
// ⚠️ `PlanUpsert` 里**没有**这个字段（`Plan` 里有，只读）。所以它只能由 kind 推：
//   - 周期套餐按下单日按月重置（与 0002 的默认口径一致）；
//   - 加油包**永不重置** —— 一个会重置的加油包等于每月白送一次流量。
//
// ⚠️ 这意味着「按自然月 1 号重置」这一档**通过 API 建不出来**，只能改库或改契约。已登记。
func planResetMethodFor(kind string) dbgen.ResetMethod {
	if kind == planKindPack {
		return dbgen.ResetMethodNever
	}
	return dbgen.ResetMethodMonthlyOnOrderDay
}

// adminPlanView 把管理面的套餐行映射成契约的 `Plan`。
//
// 与 catalog.go 的 `planView` 是两个函数而不是一个，因为它们的输入行类型不同
// （管理面多带 archived_at / order_count 三个计数）。**映射规则必须逐字一致** ——
// 两处对 kind / content_md / sort_order 的翻译不同的话，同一个套餐在用户面和管理面
// 会显示成两种东西，而没有任何报错。
//
// 🔴 **契约的 `Plan` 没有 `archived_at` 也没有 `sellable`。** 下架套餐在响应里
// 唯一的信号是 `visible=false`（AdminArchivePlan 会把 visible 与 sellable 一起置 false）。
// 于是「已下架」与「只是不显示在套餐页」在 API 上不可区分 —— 缺口已登记。
// 仍然把下架套餐**列出来**（不过滤），理由见 admin_ops.sql：滤掉之后
// 一个误下架的主力套餐就再也无法从界面恢复。
func adminPlanView(r dbgen.AdminListPlansRow) gen.Plan {
	currency := gen.PlanCurrencyCNY
	desc := r.ContentMd
	sortOrder := r.SortOrder
	reset := string(r.ResetTrafficMethod)
	visible := r.Visible

	p := gen.Plan{
		Id:                  r.ID,
		Name:                r.Name,
		Type:                planTypeView(r.Kind),
		Description:         &desc,
		TransferEnableBytes: r.TransferEnable,
		ResetTrafficMethod:  &reset,
		SpeedLimitMbps:      r.SpeedLimitMbps,
		Sort:                &sortOrder,
		Currency:            &currency,
		Visible:             &visible,
		Prices:              planPrices(r.PriceMonthly, r.PriceQuarterly, r.PriceHalfYearly, r.PriceYearly, r.PriceOnetime),
	}
	// NULL device_limit = 不限设备。映射成 0，理由与 catalog.go 的 planView 逐字相同：
	// 给一个具体数字（999）会被前端当成真的上限显示出来。
	if r.DeviceLimit != nil {
		p.DeviceLimit = *r.DeviceLimit
	}
	return p
}

// adminPlanViewFromCreated 把 CreatePlan 返回的整行摊成 Plan（字段与上面一一对应）。
func adminPlanViewFromCreated(r dbgen.Plan) gen.Plan {
	return adminPlanView(dbgen.AdminListPlansRow{
		ID: r.ID, Code: r.Code, Name: r.Name, GroupID: r.GroupID, Kind: r.Kind,
		TransferEnable: r.TransferEnable, DeviceLimit: r.DeviceLimit,
		SpeedLimitMbps: r.SpeedLimitMbps, ResetTrafficMethod: r.ResetTrafficMethod,
		PriceMonthly: r.PriceMonthly, PriceQuarterly: r.PriceQuarterly,
		PriceHalfYearly: r.PriceHalfYearly, PriceYearly: r.PriceYearly,
		PriceOnetime: r.PriceOnetime, PriceReset: r.PriceReset,
		Renewable: r.Renewable, Sellable: r.Sellable, Visible: r.Visible,
		SortOrder: r.SortOrder, ContentMd: r.ContentMd,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ArchivedAt: r.ArchivedAt,
	})
}

// adminPlanViewFromUpdated 同上，输入是 AdminUpdatePlan 的 after 侧。
func adminPlanViewFromUpdated(r dbgen.AdminUpdatePlanRow) gen.Plan {
	return adminPlanView(dbgen.AdminListPlansRow{
		ID: r.ID, Code: r.Code, Name: r.Name, GroupID: r.GroupID, Kind: r.Kind,
		TransferEnable: r.TransferEnable, DeviceLimit: r.DeviceLimit,
		SpeedLimitMbps: r.SpeedLimitMbps, ResetTrafficMethod: r.ResetTrafficMethod,
		PriceMonthly: r.PriceMonthly, PriceQuarterly: r.PriceQuarterly,
		PriceHalfYearly: r.PriceHalfYearly, PriceYearly: r.PriceYearly,
		PriceOnetime: r.PriceOnetime, PriceReset: r.PriceReset,
		Renewable: r.Renewable, Sellable: r.Sellable, Visible: r.Visible,
		SortOrder: r.SortOrder, ContentMd: r.ContentMd,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ArchivedAt: r.ArchivedAt,
	})
}

// planSnapshot 是 D8 审计的前/后像。
//
// 字段是**变更字段的完整快照**（§6.3 第 2 条），不是 diff。
// `PricingScopeNote` 只出现在 after 侧：它记的是「这次操作的语义边界」，
// 见 planPricingScopeNotice。
type planSnapshot struct {
	ID                int64   `json:"id"`
	Code              string  `json:"code,omitempty"`
	Name              string  `json:"name"`
	Kind              string  `json:"kind"`
	ContentMd         string  `json:"content_md"`
	TransferEnable    int64   `json:"transfer_enable"`
	DeviceLimit       *int32  `json:"device_limit"`
	SpeedLimitMbps    *int32  `json:"speed_limit_mbps"`
	PriceMonthly      *int64  `json:"price_monthly"`
	PriceQuarterly    *int64  `json:"price_quarterly"`
	PriceHalfYearly   *int64  `json:"price_half_yearly"`
	PriceYearly       *int64  `json:"price_yearly"`
	PriceOnetime      *int64  `json:"price_onetime"`
	Visible           bool    `json:"visible"`
	Sellable          *bool   `json:"sellable,omitempty"`
	SortOrder         int32   `json:"sort_order"`
	ArchivedAt        *string `json:"archived_at,omitempty"`
	PricingScopeNote  string  `json:"pricing_scope_note,omitempty"`
	OrderCount        *int64  `json:"order_count,omitempty"`
	SubscriberCount   *int64  `json:"subscriber_count,omitempty"`
	OpenOrderCountNow *int64  `json:"open_order_count,omitempty"`
}

// ---- ListAdminPlans ----

type adminPlanLister interface {
	AdminListPlans(ctx context.Context) ([]dbgen.AdminListPlansRow, error)
}

// ListAdminPlans 实现 GET /api/v1/admin/plans。
//
// 契约上**不分页**（套餐总数是个位数），所以没有 limit/cursor/count。
func (s *Server) ListAdminPlans(ctx context.Context, _ gen.ListAdminPlansRequestObject) (gen.ListAdminPlansResponseObject, error) {
	if _, ok := middleware.AdminFrom(ctx); !ok {
		return nil, errNoAdminAuth
	}
	data, err := listAdminPlans(ctx, s.db)
	if err != nil {
		return gen.ListAdminPlans500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取套餐列表失败", err),
		}, nil
	}
	return gen.ListAdminPlans200JSONResponse{Data: data, Meta: s.meta(ctx)}, nil
}

func listAdminPlans(ctx context.Context, q adminPlanLister) ([]gen.Plan, error) {
	rows, err := q.AdminListPlans(ctx)
	if err != nil {
		return nil, err
	}
	// 空切片而不是 nil：契约的 data 是 required 数组，nil 序列化成 null，前端在 .map 上炸。
	out := make([]gen.Plan, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminPlanView(r))
	}
	return out, nil
}

// ---- CreateAdminPlan（D8）----

// CreateAdminPlan 实现 POST /api/v1/admin/plans。
func (s *Server) CreateAdminPlan(ctx context.Context, req gen.CreateAdminPlanRequestObject) (gen.CreateAdminPlanResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.CreateAdminPlan500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.CreateAdminPlan403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能修改套餐（admin.plan.write 由角色决定：owner / admin）"),
		}, nil
	}
	if req.Body == nil {
		return gen.CreateAdminPlan422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}

	plan, err := createAdminPlan(ctx, s.db, s.catalogAudit(), actor, *req.Body)
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			return gen.CreateAdminPlan422JSONResponse{
				ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
			}, nil
		}
		return gen.CreateAdminPlan500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "新建套餐失败", err),
		}, nil
	}

	resp := gen.CreateAdminPlan201JSONResponse{
		Headers: gen.CreateAdminPlan201ResponseHeaders{
			Location: fmt.Sprintf("/api/v1/admin/plans/%d", plan.Id),
		},
	}
	resp.Body.Data = plan
	resp.Body.Meta = s.meta(ctx)
	return resp, nil
}

// catalogDetail 把 catalogOpError 翻成带 field 的 422 信封。
func (s *Server) catalogDetail(ctx context.Context, oe *catalogOpError) gen.ErrUnprocessableJSONResponse {
	if oe.field == "" {
		return s.unprocessable(ctx, oe.msg)
	}
	return s.unprocessable(ctx, oe.msg, detail(oe.field, oe.msg))
}

// adminPlanCreator 是建套餐要的最小能力。
type adminPlanCreator interface {
	GetRegistrationGroupID(ctx context.Context) (int64, error)
}

// createAdminPlan 建一个套餐并写 D8 审计。
//
// 🔴 三样契约里没有、必须由 handler 补齐的 NOT NULL 列（admin_ops.sql 逐条列过）：
//
//	kind      ← 从 PlanUpsert.type 推（period→cycle，traffic_pack→pack）。**必传**，见 planKindFromContract。
//	code      ← handler 生成（NOT NULL UNIQUE），撞号重试。
//	group_id  ← 取默认注册分组（复用 GetRegistrationGroupID 的口径：优先 'basic'，否则 id 最小）。
//	            ⚠️ 它决定了买这个套餐的人能看到哪些节点（ApplyUserEntitlement 按 plan.group_id
//	            覆盖 users.group_id），选错的现象是「买了贵套餐却只看得到基础节点」。
//	            契约里没有这个字段 = **管理面建不出跨分组的套餐**，缺口已登记。
//
// ⚠️ `renewable` / `sellable` 契约里也没有，一律 true：一个建出来就不能卖的套餐没有意义。
// 想要「下架但可续费」走 UpdateAdminPlan / DeleteAdminPlan（后者= 下架）。
func createAdminPlan(
	ctx context.Context,
	q adminPlanCreator,
	run catalogAuditRunner,
	actor audit.Actor,
	body gen.PlanUpsert,
) (gen.Plan, error) {
	if err := catalogCheckReason(body.Reason); err != nil {
		return gen.Plan{}, err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return gen.Plan{}, catalogUnprocessable("name", "套餐名不能为空")
	}
	kind, err := planKindFromContract(body.Type)
	if err != nil {
		return gen.Plan{}, err
	}
	if body.TransferEnableBytes < 0 {
		return gen.Plan{}, catalogUnprocessable("transfer_enable_bytes", "流量额度不能是负数")
	}
	if body.DeviceLimit < 0 {
		return gen.Plan{}, catalogUnprocessable("device_limit", "设备数不能是负数")
	}
	prices, err := parsePlanPrices(body.Prices, kind)
	if err != nil {
		return gen.Plan{}, err
	}

	groupID, err := q.GetRegistrationGroupID(ctx)
	if err != nil {
		return gen.Plan{}, fmt.Errorf("取默认套餐分组失败: %w", err)
	}

	params := dbgen.CreatePlanParams{
		Name:               name,
		GroupID:            groupID,
		TransferEnable:     body.TransferEnableBytes,
		DeviceLimit:        planDeviceLimit(body.DeviceLimit),
		SpeedLimitMbps:     planSpeedLimit(body.SpeedLimitMbps),
		ResetTrafficMethod: planResetMethodFor(kind),
		PriceMonthly:       prices.monthly,
		PriceQuarterly:     prices.quarterly,
		PriceHalfYearly:    prices.halfYearly,
		PriceYearly:        prices.yearly,
		PriceOnetime:       prices.onetime,
		PriceReset:         nil, // 契约里没有「重置流量包价格」这个字段。
		Renewable:          true,
		Sellable:           true,
		Visible:            body.Visible == nil || *body.Visible,
		SortOrder:          int32Or(body.Sort, 0),
		ContentMd:          strOr(body.Description, ""),
		// 🔴 第 19 个参数。漏了它是数据库拒绝（NOT NULL 无 DEFAULT），当场就知道 ——
		//    这是唯一不会静默的形态，0016 刻意如此。
		Kind: kind,
	}

	var created dbgen.Plan
	// 撞 code 唯一索引重试。**重试必须在事务之外**：一个已经报错的事务
	// 在 PostgreSQL 里处于 aborted 状态，同一个事务内重试第二条 INSERT 只会拿到
	// 25P02（current transaction is aborted），而那个错误看起来像别的问题。
	for attempt := 0; ; attempt++ {
		code, cErr := newPlanCode()
		if cErr != nil {
			return gen.Plan{}, cErr
		}
		params.Code = code

		runErr := run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
			row, insErr := tx.CreatePlan(ctx, params)
			if insErr != nil {
				return audit.Entry{}, insErr
			}
			created = row
			after := planSnapshotFromCreated(row)
			after.PricingScopeNote = planPricingScopeNotice
			return audit.Entry{
				Action:     "D8.plan.create",
				TargetType: "plan",
				TargetID:   strconv.FormatInt(row.ID, 10),
				Before:     nil, // 创建操作没有 before（§6.3 第 2 条对创建的形态）。
				After:      after,
				Reason:     strings.TrimSpace(body.Reason),
			}, nil
		})
		if runErr == nil {
			break
		}
		if isUniqueViolation(runErr) && attempt < createPlanRetries {
			continue
		}
		if isCheckViolation(runErr) {
			// 走到这里说明 Go 侧的校验漏了一条（比如将来加了新的 CHECK）。
			// 翻成 422 而不是 500：数据库是对的，请求是错的。
			return gen.Plan{}, catalogUnprocessable("", "套餐参数被数据库约束拒绝："+runErr.Error())
		}
		return gen.Plan{}, runErr
	}
	return adminPlanViewFromCreated(created), nil
}

// planDeviceLimit 把契约的非空 device_limit 翻成库里的可空列。
//
// 0 = 不限设备 → NULL。契约的 `Plan.device_limit` 是非空 int32 且用 0 表达「不限」
// （catalog.go 的 planView 反向映射就是这么读的），两边必须同一个约定 ——
// 存一个 0 进去会撞 `device_limit > 0` 之外的语义混乱，且用户面会显示「0 台设备」。
func planDeviceLimit(v int32) *int32 {
	if v <= 0 {
		return nil
	}
	return &v
}

func int32Or(v *int32, def int32) int32 {
	if v == nil {
		return def
	}
	return *v
}

func strOr(v *string, def string) string {
	if v == nil {
		return def
	}
	return *v
}

func planSnapshotFromCreated(r dbgen.Plan) planSnapshot {
	sellable := r.Sellable
	return planSnapshot{
		ID: r.ID, Code: r.Code, Name: r.Name, Kind: r.Kind, ContentMd: r.ContentMd,
		TransferEnable: r.TransferEnable, DeviceLimit: r.DeviceLimit,
		SpeedLimitMbps: r.SpeedLimitMbps,
		PriceMonthly:   r.PriceMonthly, PriceQuarterly: r.PriceQuarterly,
		PriceHalfYearly: r.PriceHalfYearly, PriceYearly: r.PriceYearly,
		PriceOnetime: r.PriceOnetime,
		Visible:      r.Visible, Sellable: &sellable, SortOrder: r.SortOrder,
	}
}

// ---- UpdateAdminPlan（D8）----

// UpdateAdminPlan 实现 PATCH /api/v1/admin/plans/{id}。
//
// ⚠️ 名字是 PATCH，语义是 **PUT**：`PlanUpsert` 的 name / type / prices /
// transfer_enable_bytes / device_limit 全在 required 里，所以每一次调用都是整体覆写。
// 这不是本实现的选择，是契约给的形状 —— 写在这里免得后人以为漏了 coalesce。
func (s *Server) UpdateAdminPlan(ctx context.Context, req gen.UpdateAdminPlanRequestObject) (gen.UpdateAdminPlanResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.UpdateAdminPlan500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.UpdateAdminPlan403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能修改套餐（admin.plan.write 由角色决定：owner / admin）"),
		}, nil
	}
	if req.Body == nil {
		return gen.UpdateAdminPlan422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}

	plan, err := updateAdminPlan(ctx, s.catalogAudit(), actor, req.Id, *req.Body)
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			switch oe.kind {
			case catalogErrNotFound:
				return gen.UpdateAdminPlan404JSONResponse{
					ErrNotFoundJSONResponse: s.notFound(ctx, oe.msg),
				}, nil
			default:
				return gen.UpdateAdminPlan422JSONResponse{
					ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
				}, nil
			}
		}
		return gen.UpdateAdminPlan500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "修改套餐失败", err),
		}, nil
	}
	return gen.UpdateAdminPlan200JSONResponse{Data: plan, Meta: s.meta(ctx)}, nil
}

func updateAdminPlan(
	ctx context.Context,
	run catalogAuditRunner,
	actor audit.Actor,
	planID int64,
	body gen.PlanUpsert,
) (gen.Plan, error) {
	if err := catalogCheckReason(body.Reason); err != nil {
		return gen.Plan{}, err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return gen.Plan{}, catalogUnprocessable("name", "套餐名不能为空")
	}
	kind, err := planKindFromContract(body.Type)
	if err != nil {
		return gen.Plan{}, err
	}
	if body.TransferEnableBytes < 0 {
		return gen.Plan{}, catalogUnprocessable("transfer_enable_bytes", "流量额度不能是负数")
	}
	prices, err := parsePlanPrices(body.Prices, kind)
	if err != nil {
		return gen.Plan{}, err
	}

	var updated dbgen.AdminUpdatePlanRow
	runErr := run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		// 先读一次：既拿 404 的判据，也拿三个计数（下面 kind 变更那道闸要用）。
		// ⚠️ **审计的前像不取这一份**，取 AdminUpdatePlan 自己返回的 before_*：
		//    这条 SELECT 与 UPDATE 之间即使同事务也不是同一个语句，
		//    而 `UPDATE … FROM plans AS prev` 的 prev 侧在语句内必然是改前值。
		//    用先读的那份当 before，在并发下会记下一个从未紧接着 after 出现过的快照。
		cur, err := tx.AdminGetPlanForUpdate(ctx, planID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, catalogNotFound("套餐不存在")
			}
			return audit.Entry{}, err
		}
		// 🔴 **改 kind 且已有订单 → 422。**
		//    `orders.type` 是下单那一刻按 plan.kind 推导出来并**存进订单行**的
		//    （0016 的裁决）。把一个 cycle 套餐改成 pack 不会回改任何历史订单，
		//    但会让「同一个套餐的历史订单一半是周期一半是加油包」——
		//    退款折算、续费折抵、流量重置三条路径从此对这个套餐给出互相矛盾的答案，
		//    而每一条都不报错。要换类型的正确做法是下架旧的、建一个新的。
		if cur.Kind != kind && cur.OrderCount > 0 {
			return audit.Entry{}, catalogUnprocessable("type",
				fmt.Sprintf("该套餐已有 %d 张历史订单，不能改变类型（%s → %s）："+
					"订单的类型是下单时按套餐类型定死并存进订单行的，改这里不会回改它们，"+
					"只会让同一个套餐的历史订单分成互相矛盾的两半。请下架这个套餐并新建一个。",
					cur.OrderCount, cur.Kind, kind))
		}

		row, err := tx.AdminUpdatePlan(ctx, dbgen.AdminUpdatePlanParams{
			PlanID:          planID,
			Name:            name,
			Kind:            kind,
			ContentMd:       strOr(body.Description, ""),
			TransferEnable:  body.TransferEnableBytes,
			DeviceLimit:     planDeviceLimit(body.DeviceLimit),
			SpeedLimitMbps:  planSpeedLimit(body.SpeedLimitMbps),
			PriceMonthly:    prices.monthly,
			PriceQuarterly:  prices.quarterly,
			PriceHalfYearly: prices.halfYearly,
			PriceYearly:     prices.yearly,
			PriceOnetime:    prices.onetime,
			Visible:         body.Visible == nil || *body.Visible,
			SortOrder:       int32Or(body.Sort, cur.SortOrder),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// 前一条 SELECT 拿到了行、这一条却 0 行 —— 并发删除。仍然是 404。
				return audit.Entry{}, catalogNotFound("套餐不存在")
			}
			if isCheckViolation(err) {
				return audit.Entry{}, catalogUnprocessable("", "套餐参数被数据库约束拒绝："+err.Error())
			}
			return audit.Entry{}, err
		}
		updated = row

		before := planSnapshot{
			ID: row.ID, Name: row.BeforeName, Kind: row.BeforeKind, ContentMd: row.BeforeContentMd,
			TransferEnable: row.BeforeTransferEnable, DeviceLimit: row.BeforeDeviceLimit,
			SpeedLimitMbps: row.BeforeSpeedLimitMbps,
			PriceMonthly:   row.BeforePriceMonthly, PriceQuarterly: row.BeforePriceQuarterly,
			PriceHalfYearly: row.BeforePriceHalfYearly, PriceYearly: row.BeforePriceYearly,
			PriceOnetime: row.BeforePriceOnetime,
			Visible:      row.BeforeVisible, SortOrder: row.BeforeSortOrder,
			OrderCount: &cur.OrderCount, SubscriberCount: &cur.SubscriberCount,
		}
		after := planSnapshot{
			ID: row.ID, Code: row.Code, Name: row.Name, Kind: row.Kind, ContentMd: row.ContentMd,
			TransferEnable: row.TransferEnable, DeviceLimit: row.DeviceLimit,
			SpeedLimitMbps: row.SpeedLimitMbps,
			PriceMonthly:   row.PriceMonthly, PriceQuarterly: row.PriceQuarterly,
			PriceHalfYearly: row.PriceHalfYearly, PriceYearly: row.PriceYearly,
			PriceOnetime: row.PriceOnetime,
			Visible:      row.Visible, SortOrder: row.SortOrder,
			PricingScopeNote: planPricingScopeNotice,
		}
		return audit.Entry{
			Action:     "D8.plan.update",
			TargetType: "plan",
			TargetID:   strconv.FormatInt(row.ID, 10),
			Before:     before,
			After:      after,
			Reason:     strings.TrimSpace(body.Reason),
		}, nil
	})
	if runErr != nil {
		return gen.Plan{}, runErr
	}
	return adminPlanViewFromUpdated(updated), nil
}

// ---- DeleteAdminPlan（D8）----

// DeleteAdminPlan 实现 DELETE /api/v1/admin/plans/{id}。
//
// 🔴 **「删除」= 下架**（archived_at + sellable/visible 置 false），不是 DELETE。
// `orders.plan_id` 是 ON DELETE RESTRICT：只要有一张历史订单引用它，硬删就是数据库拒绝；
// 而没有订单引用它的时候硬删也是错的 —— `sla_policies.plan_id` 是 ON DELETE CASCADE，
// 一次删套餐会静默带走它的 SLA 策略。
//
// 🔴 **L1 与 L2 在这个端点上都表达不出来**：契约给 DELETE 没有请求体，
// 于是既没有 confirmation 也没有 reason。审计照写（before 快照是这次下架唯一的证据），
// reason 落 NULL，并留一条 WARN。
func (s *Server) DeleteAdminPlan(ctx context.Context, req gen.DeleteAdminPlanRequestObject) (gen.DeleteAdminPlanResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.DeleteAdminPlan500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.DeleteAdminPlan403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能修改套餐（admin.plan.write 由角色决定：owner / admin）"),
		}, nil
	}
	s.logger.WarnContext(ctx, "bp_admin_audit_no_reason 下架套餐的审计将没有原因（契约给 DELETE 没有请求体）",
		"admin_id", admin.AdminID, "plan_id", req.Id, "request_id", middleware.RequestIDFrom(ctx))

	if err := deleteAdminPlan(ctx, s.catalogAudit(), actor, req.Id); err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			switch oe.kind {
			case catalogErrNotFound:
				return gen.DeleteAdminPlan404JSONResponse{
					ErrNotFoundJSONResponse: s.notFound(ctx, oe.msg),
				}, nil
			default:
				return gen.DeleteAdminPlan409JSONResponse{
					ErrConflictJSONResponse: s.conflict(ctx, oe.msg),
				}, nil
			}
		}
		return gen.DeleteAdminPlan500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "下架套餐失败", err),
		}, nil
	}
	return gen.DeleteAdminPlan204Response{
		Headers: gen.DeleteAdminPlan204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}, nil
}

func deleteAdminPlan(ctx context.Context, run catalogAuditRunner, actor audit.Actor, planID int64) error {
	return run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		cur, err := tx.AdminGetPlanForUpdate(ctx, planID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, catalogNotFound("套餐不存在")
			}
			return audit.Entry{}, err
		}
		// 🔴 有人正在结算这个套餐 → 409。下架会让他们的支付链路走进「套餐不存在」，
		//    而他们的钱**可能已经在路上了**（USDT 转账不可撤销）。
		if cur.OpenOrderCount > 0 {
			return audit.Entry{}, catalogConflict(fmt.Sprintf(
				"该套餐还有 %d 张未结算的订单，现在下架会让这些人的支付走进「套餐不存在」，"+
					"而他们的钱可能已经在链上了。请等这些订单结算或过期后再下架。", cur.OpenOrderCount))
		}
		// ⚠️ `subscriber_count > 0` **不拦**：下架的常规语义就是
		//    「老用户继续用、新用户买不到」（0002 把 sellable / renewable 拆开就是为了它）。
		if cur.ArchivedAt.Valid {
			// 409 而不是幂等 204：一个「已经下架了」的套餐再点一次下架，
			// 说明操作者看到的界面是旧的。204 会让他以为刚才那一下起了作用。
			return audit.Entry{}, catalogConflict("该套餐已经下架，无需重复操作")
		}

		row, err := tx.AdminArchivePlan(ctx, planID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// 上一条读到 archived_at IS NULL，这一条 0 行 = 并发下架。同样 409。
				return audit.Entry{}, catalogConflict("该套餐已经下架，无需重复操作")
			}
			return audit.Entry{}, err
		}

		beforeSellable := row.BeforeSellable
		afterSellable := row.Sellable
		before := planSnapshot{
			ID: row.ID, Code: row.Code, Name: row.BeforeName, Kind: row.Kind,
			Visible: row.BeforeVisible, Sellable: &beforeSellable,
			ArchivedAt:      tsString(row.BeforeArchivedAt),
			OrderCount:      &cur.OrderCount,
			SubscriberCount: &cur.SubscriberCount,
		}
		after := planSnapshot{
			ID: row.ID, Code: row.Code, Name: row.Name, Kind: row.Kind,
			Visible: row.Visible, Sellable: &afterSellable,
			ArchivedAt:       tsString(row.ArchivedAt),
			PricingScopeNote: planPricingScopeNotice,
		}
		return audit.Entry{
			Action:     "D8.plan.archive",
			TargetType: "plan",
			TargetID:   strconv.FormatInt(row.ID, 10),
			Before:     before,
			After:      after,
			// Reason 空：契约给 DELETE 没有请求体。见 handler 里那条 WARN。
		}, nil
	})
}

// tsString 把可空时间戳变成审计快照里的 *string（RFC3339）。
//
// 用字符串而不是 time.Time：审计快照是 jsonb，将来被人直接读。
// RFC3339 在 psql 里一眼能看懂，而 Go 的 time.Time 零值会序列化成
// `0001-01-01T00:00:00Z` —— 那看起来像一个真实发生过的时刻。
func tsString(ts pgtype.Timestamptz) *string {
	if !ts.Valid {
		return nil
	}
	s := ts.Time.UTC().Format(time.RFC3339Nano)
	return &s
}

// ============================================================
// 优惠码（模块 13，D8）
// ============================================================
//
// 🔴 **契约的 `Coupon` 与库里的 `coupons` 有四处对不上，第三处会出事故：**
//
//	① type：契约 [fixed, percent] ← 库 ('fixed_amount','percentage')。纯改名。
//	② value：契约 percent 是**百分点**（20 = 8 折），库里 percentage 存 **bps**（1000 = 10%）。
//	   少乘 100 → 管理员填「打 8 折」、系统按 0.2% 折扣算（用户几乎没优惠）；
//	   多乘 100 → 白送。**两个方向都不报错。**
//	③ enabled：库里**没有这一列**，而 `visible` 不是它 —— `VerifyCouponForUser`
//	   从头到尾不读 visible，把它置 false 的优惠码**照样能兑换**。见 couponEndsAtForWrite。
//	④ started_at / ended_at / use_limit / plan_ids ← starts_at / ends_at / total_uses / scope_plan_ids。纯改名。
//
// ⚠️ `CouponUpsert` 里没有 name / min_amount / uses_per_user / first_order_only /
//    scope_periods。建码时它们落 0006 的默认值（''、0、1、false、'{}'），方向都是保守的；
//    改码时本文件不写它们 —— 契约里没有 = 「没提」不是「要清空」。

const (
	couponTypeFixedDB   = "fixed_amount"
	couponTypePercentDB = "percentage"

	// couponPercentScale 是契约百分点 → 库 bps 的倍率。
	// 🔴 这个常量是本节最重要的一行：0006 的列注释写死 `1000 = 10%`。
	couponPercentScale = 100
)

// couponTypeToDB 把契约枚举翻成库里的字符串。
func couponTypeToDB(t gen.CouponUpsertType) (string, error) {
	switch t {
	case gen.CouponUpsertTypeFixed:
		return couponTypeFixedDB, nil
	case gen.CouponUpsertTypePercent:
		return couponTypePercentDB, nil
	default:
		return "", catalogUnprocessable("type", fmt.Sprintf("未知的优惠码类型 %q", string(t)))
	}
}

// couponTypeFromDB 反向映射。未知值一律当成 fixed 并交给调用方记日志 ——
// 返回一个契约枚举外的值会让前端整块空白（JSON 没有类型检查）。
func couponTypeFromDB(t string) (gen.CouponType, bool) {
	switch t {
	case couponTypeFixedDB:
		return gen.CouponTypeFixed, true
	case couponTypePercentDB:
		return gen.CouponTypePercent, true
	default:
		return gen.CouponTypeFixed, false
	}
}

// couponValueToDB 把契约的 value 翻成库里的量纲。
//
//	fixed   → 分，原样；
//	percent → 百分点 × 100 = bps。
//
// ⚠️ `value > 0` 是 0006 的 CHECK：percent 传 0（想表达「不打折」）是数据库拒绝。
// 在这里先挡一道给出人话，数据库那道仍是真闸门。
// 百分点上限 100：`floor(gross × value / 10000)` 在 value > 10000 时折扣超过原价，
// 而订单金额有 `amount_due >= 0` 的 CHECK —— 现象是下单时 500 而不是建码时 422。
func couponValueToDB(t gen.CouponUpsertType, v int64) (int64, error) {
	if v <= 0 {
		return 0, catalogUnprocessable("value", "优惠额必须大于 0（想停用这张码请用 enabled=false）")
	}
	if t == gen.CouponUpsertTypePercent {
		if v > 100 {
			return 0, catalogUnprocessable("value", "百分比折扣不能超过 100 个百分点")
		}
		return v * couponPercentScale, nil
	}
	return v, nil
}

// couponValueFromDB 把库里的量纲翻回契约。
//
// 第二个返回值为 false 表示 bps 不是 100 的整数倍（如 1050 = 10.5%）——
// 契约的 value 是整数百分点，**表达不出半个百分点**，只能截断。
// 调用方必须记一条 WARN：一个 10.5% 的码在后台显示成 10%，
// 而管理员照着显示值去改一下就把它变成了真的 10% —— 一次静默的改价。
func couponValueFromDB(dbType string, v int64) (int64, bool) {
	if dbType != couponTypePercentDB {
		return v, true
	}
	return v / couponPercentScale, v%couponPercentScale == 0
}

// couponEndsAtForWrite 实现「enabled 的写侧口径」。
//
// 🔴 库里没有 enabled 列，唯一真能停掉一张码的机制是把 `ends_at` 设成现在。
//
//	enabled = false → ends_at = now()（无论 body 里的 ended_at 是什么）；
//	enabled = true  → 只有当现有 ends_at **已经过期**时才用 body 给的值（或清空）；
//	                  没过期就**不要动** —— 盲目清 ends_at 会让一个真的到期了的活动复活，
//	                  而那张码可能是三个月前的双十一券。
//	enabled 未传    → 完全按 body 的 ended_at 走（「没提」不是「要改」）。
//
// cur 为 nil 表示这是创建（没有现值）。
func couponEndsAtForWrite(enabled *bool, bodyEndedAt *time.Time, cur *pgtype.Timestamptz, now time.Time) pgtype.Timestamptz {
	switch {
	case enabled != nil && !*enabled:
		return tstz(now)
	case enabled != nil && *enabled:
		expired := cur != nil && cur.Valid && !cur.Time.After(now)
		if cur == nil || expired {
			if bodyEndedAt != nil {
				return tstz(*bodyEndedAt)
			}
			return pgtype.Timestamptz{} // 清空 = 永不过期。
		}
		// 现有 ends_at 还没到，enabled=true 是「保持可用」，不是「延长」。
		if bodyEndedAt != nil {
			return tstz(*bodyEndedAt)
		}
		return *cur
	default:
		if bodyEndedAt != nil {
			return tstz(*bodyEndedAt)
		}
		return pgtype.Timestamptz{}
	}
}

// couponEnabledNow 按列表/详情查询里那个计算列的**逐字相同**口径算 enabled。
//
// 口径与 catalog.sql 的 `VerifyCouponForUser` 的三个布尔位互补：
// 未开始 / 已结束 / 已用尽，任一为真则不可用。
// 🔴 两处必须同源 —— 后台显示「可用」而用户兑换时被拒，工单里没有任何可以对质的东西。
func couponEnabledNow(startsAt, endsAt pgtype.Timestamptz, totalUses *int32, usedCount int32, now time.Time) bool {
	if startsAt.Valid && startsAt.Time.After(now) {
		return false
	}
	if endsAt.Valid && !endsAt.Time.After(now) {
		return false
	}
	if totalUses != nil && usedCount >= *totalUses {
		return false
	}
	return true
}

// couponFields 是三条查询共有的优惠码字段（列表 / 详情 / 写回）。
// 抽出来是为了让 adminCouponView 只有一份 —— 三份映射必然漂移，
// 而漂移的现象是「列表页说 8 折、详情页说 0.08 折」。
type couponFields struct {
	ID           int64
	Code         string
	Type         string
	Value        int64
	ScopePlanIds []int64
	TotalUses    *int32
	UsedCount    int32
	StartsAt     pgtype.Timestamptz
	EndsAt       pgtype.Timestamptz
}

func adminCouponView(f couponFields, now time.Time) (gen.Coupon, []string) {
	var warnings []string
	ctype, known := couponTypeFromDB(f.Type)
	if !known {
		warnings = append(warnings, fmt.Sprintf("优惠码 %d 的 type=%q 不在契约枚举内，已按 fixed 下发", f.ID, f.Type))
	}
	value, exact := couponValueFromDB(f.Type, f.Value)
	if !exact {
		warnings = append(warnings, fmt.Sprintf(
			"优惠码 %d 的折扣是 %d bps（%.2f%%），契约只能表达整数百分点，已截断成 %d%%",
			f.ID, f.Value, float64(f.Value)/100, value))
	}
	c := gen.Coupon{
		Id:        f.ID,
		Code:      f.Code,
		Type:      ctype,
		Value:     value,
		Enabled:   couponEnabledNow(f.StartsAt, f.EndsAt, f.TotalUses, f.UsedCount, now),
		StartedAt: tptr(f.StartsAt),
		EndedAt:   tptr(f.EndsAt),
		UseLimit:  f.TotalUses,
		UsedCount: ptrOf(f.UsedCount),
	}
	if f.ScopePlanIds != nil {
		ids := f.ScopePlanIds
		c.PlanIds = &ids
	}
	return c, warnings
}

// couponSnapshot 是优惠码审计的前/后像。
type couponSnapshot struct {
	ID           int64   `json:"id"`
	Code         string  `json:"code"`
	Type         string  `json:"type"`
	ValueRaw     int64   `json:"value_raw"`
	ValueUnit    string  `json:"value_unit"`
	ScopePlanIds []int64 `json:"scope_plan_ids"`
	TotalUses    *int32  `json:"total_uses"`
	UsedCount    *int32  `json:"used_count,omitempty"`
	StartsAt     *string `json:"starts_at"`
	EndsAt       *string `json:"ends_at"`
	Visible      bool    `json:"visible"`
}

// couponValueUnit 把量纲写进审计快照。
//
// 🔴 一条只写着 `"value": 2000` 的审计记录，事后没有人能确定它是 20 元还是 20%。
// 量纲是这条记录能不能被读懂的全部。
func couponValueUnit(dbType string) string {
	if dbType == couponTypePercentDB {
		return "bps(1000=10%)"
	}
	return "cent"
}

func couponSnapshotOf(f couponFields, visible bool, withUsed bool) couponSnapshot {
	s := couponSnapshot{
		ID: f.ID, Code: f.Code, Type: f.Type,
		ValueRaw: f.Value, ValueUnit: couponValueUnit(f.Type),
		ScopePlanIds: f.ScopePlanIds, TotalUses: f.TotalUses,
		StartsAt: tsString(f.StartsAt), EndsAt: tsString(f.EndsAt),
		Visible: visible,
	}
	if withUsed {
		s.UsedCount = ptrOf(f.UsedCount)
	}
	return s
}

// ---- ListAdminCoupons ----

type adminCouponLister interface {
	AdminListCouponsPage(ctx context.Context, arg dbgen.AdminListCouponsPageParams) ([]dbgen.AdminListCouponsPageRow, error)
	AdminCountCouponsFiltered(ctx context.Context) (int64, error)
}

// ListAdminCoupons 实现 GET /api/v1/admin/coupons。
func (s *Server) ListAdminCoupons(ctx context.Context, req gen.ListAdminCouponsRequestObject) (gen.ListAdminCouponsResponseObject, error) {
	if _, ok := middleware.AdminFrom(ctx); !ok {
		return nil, errNoAdminAuth
	}
	data, meta, err := listAdminCoupons(ctx, s.db, s.meta(ctx), req.Params, time.Now(), s.catalogWarn(ctx))
	if err != nil {
		return gen.ListAdminCoupons500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取优惠码列表失败", err),
		}, nil
	}
	return gen.ListAdminCoupons200JSONResponse{Data: data, Meta: meta}, nil
}

// catalogWarn 返回一个记 WARN 的闭包，给纯映射函数用。
//
// 映射函数不持有 logger（它们要能在单测里当纯函数用），但它们发现的那些
// 「值在契约里表达不出来」的事实**必须有人看见**。传一个 sink 进去，
// 单测里换成收集器就能断言「这条 WARN 确实被触发了」。
func (s *Server) catalogWarn(ctx context.Context) func(string) {
	return func(msg string) {
		s.logger.WarnContext(ctx, msg, "request_id", middleware.RequestIDFrom(ctx))
	}
}

func listAdminCoupons(
	ctx context.Context,
	q adminCouponLister,
	meta gen.Meta,
	params gen.ListAdminCouponsParams,
	now time.Time,
	warn func(string),
) ([]gen.Coupon, gen.Meta, error) {
	want, limitPlusOne := pageLimit(params.Limit)

	arg := dbgen.AdminListCouponsPageParams{PageLimit: limitPlusOne}
	if params.Cursor != nil && *params.Cursor != "" {
		cur, ok := decodePageCursor(*params.Cursor)
		if !ok {
			// 契约给这个端点没有 400，所以坏游标退回第一页 + WARN
			// （usersub.go 的三选一推理逐字适用：500 是撒谎，带着坏游标去查会返回空列表）。
			warn("优惠码列表游标非法，按第一页处理")
		} else {
			arg.CursorID = &cur.ID
		}
	}

	rows, err := q.AdminListCouponsPage(ctx, arg)
	if err != nil {
		return nil, meta, err
	}
	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.Coupon, 0, len(rows))
	for _, r := range rows {
		c, warns := adminCouponView(couponFields{
			ID: r.ID, Code: r.Code, Type: r.Type, Value: r.Value,
			ScopePlanIds: r.ScopePlanIds, TotalUses: r.TotalUses, UsedCount: r.UsedCount,
			StartsAt: r.StartsAt, EndsAt: r.EndsAt,
		}, now)
		for _, w := range warns {
			warn(w)
		}
		out = append(out, c)
	}

	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		// ⚠️ 游标只用 id（AdminListCouponsPage 的 WHERE 就是 `c.id < cursor_id`），
		//    但线格式仍然带上 at —— api-contract §2.4 定的形状是 `{"id":…,"at":"…"}`，
		//    而 decodeKeysetCursor 会拒绝缺分量的游标。两处保持同一个形状，
		//    免得某天有人把这个端点的游标粘到另一个端点上时得到一个静默的空列表。
		meta.NextCursor = ptrOf(encodePageCursor(last.ID, ttime(last.CreatedAt)))
	}
	if params.Count != nil && *params.Count {
		total, err := q.AdminCountCouponsFiltered(ctx)
		if err != nil {
			return nil, meta, err
		}
		meta.Total = &total
	}
	return out, meta, nil
}

// ---- CreateAdminCoupon ----

// CreateAdminCoupon 实现 POST /api/v1/admin/coupons。
func (s *Server) CreateAdminCoupon(ctx context.Context, req gen.CreateAdminCouponRequestObject) (gen.CreateAdminCouponResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.CreateAdminCoupon500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.CreateAdminCoupon403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能修改优惠码（admin.plan.write 由角色决定：owner / admin）"),
		}, nil
	}
	if req.Body == nil {
		return gen.CreateAdminCoupon422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}

	c, err := createAdminCoupon(ctx, s.catalogAudit(), actor, *req.Body, time.Now(), s.catalogWarn(ctx))
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			return gen.CreateAdminCoupon422JSONResponse{
				ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
			}, nil
		}
		return gen.CreateAdminCoupon500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "新建优惠码失败", err),
		}, nil
	}
	resp := gen.CreateAdminCoupon201JSONResponse{
		Headers: gen.CreateAdminCoupon201ResponseHeaders{
			Location: fmt.Sprintf("/api/v1/admin/coupons/%d", c.Id),
		},
	}
	resp.Body.Data = c
	resp.Body.Meta = s.meta(ctx)
	return resp, nil
}

func createAdminCoupon(
	ctx context.Context,
	run catalogAuditRunner,
	actor audit.Actor,
	body gen.CouponUpsert,
	now time.Time,
	warn func(string),
) (gen.Coupon, error) {
	params, err := couponWriteParams(body, nil, now)
	if err != nil {
		return gen.Coupon{}, err
	}

	var created dbgen.Coupon
	runErr := run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		row, err := tx.AdminCreateCoupon(ctx, dbgen.AdminCreateCouponParams{
			Code: params.code, Type: params.dbType, Value: params.dbValue,
			ScopePlanIds: params.planIDs, TotalUses: params.totalUses,
			StartsAt: params.startsAt, EndsAt: params.endsAt,
			// ⚠️ `visible` 恒为 true。它**不是** enabled（见本节头 ③）——
			//    契约的 CouponUpsert 里根本没有 visible 这个字段，
			//    而把 enabled 接到它上面得到的是一个「看起来禁用了、实际还在打折」的码。
			Visible: true,
		})
		if err != nil {
			return audit.Entry{}, err
		}
		created = row
		return audit.Entry{
			Action:     "D8.coupon.create",
			TargetType: "coupon",
			TargetID:   strconv.FormatInt(row.ID, 10),
			Before:     nil,
			After: couponSnapshotOf(couponFields{
				ID: row.ID, Code: row.Code, Type: row.Type, Value: row.Value,
				ScopePlanIds: row.ScopePlanIds, TotalUses: row.TotalUses,
				UsedCount: row.UsedCount, StartsAt: row.StartsAt, EndsAt: row.EndsAt,
			}, row.Visible, false),
			Reason: strings.TrimSpace(body.Reason),
		}, nil
	})
	if runErr != nil {
		if isUniqueViolation(runErr) {
			// ⚠️ 契约给 createAdminCoupon 没有 409，只有 422。
			//    这里用 422 并说清是「码重复」—— 契约缺口已登记。
			return gen.Coupon{}, catalogUnprocessable("code", "这个优惠码已经存在（码不区分大小写）")
		}
		if isCheckViolation(runErr) {
			return gen.Coupon{}, catalogUnprocessable("", "优惠码参数被数据库约束拒绝："+runErr.Error())
		}
		return gen.Coupon{}, runErr
	}

	c, warns := adminCouponView(couponFields{
		ID: created.ID, Code: created.Code, Type: created.Type, Value: created.Value,
		ScopePlanIds: created.ScopePlanIds, TotalUses: created.TotalUses,
		UsedCount: created.UsedCount, StartsAt: created.StartsAt, EndsAt: created.EndsAt,
	}, now)
	for _, w := range warns {
		warn(w)
	}
	return c, nil
}

// couponWriteParams 是 create / update 共用的入参校验与换算结果。
type couponWriteParamsResult struct {
	code      string
	dbType    string
	dbValue   int64
	planIDs   []int64
	totalUses *int32
	startsAt  pgtype.Timestamptz
	endsAt    pgtype.Timestamptz
}

// couponWriteParams 做 L2 校验 + 四处量纲/枚举换算。cur 非 nil 时是改码（enabled 的口径不同）。
func couponWriteParams(body gen.CouponUpsert, cur *pgtype.Timestamptz, now time.Time) (couponWriteParamsResult, error) {
	var out couponWriteParamsResult
	if err := catalogCheckReason(body.Reason); err != nil {
		return out, err
	}
	code := strings.TrimSpace(body.Code)
	if code == "" {
		return out, catalogUnprocessable("code", "优惠码不能为空")
	}
	dbType, err := couponTypeToDB(body.Type)
	if err != nil {
		return out, err
	}
	dbValue, err := couponValueToDB(body.Type, body.Value)
	if err != nil {
		return out, err
	}
	if body.UseLimit != nil && *body.UseLimit < 1 {
		return out, catalogUnprocessable("use_limit", "使用次数上限至少是 1（不限次请不要传这个字段）")
	}
	// scope_plan_ids 是 NOT NULL bigint[]，空数组 = 不限套餐。nil 会被 pgx 写成 NULL。
	planIDs := []int64{}
	if body.PlanIds != nil {
		planIDs = *body.PlanIds
	}
	out = couponWriteParamsResult{
		code: code, dbType: dbType, dbValue: dbValue,
		planIDs: planIDs, totalUses: body.UseLimit,
		endsAt: couponEndsAtForWrite(body.Enabled, body.EndedAt, cur, now),
	}
	if body.StartedAt != nil {
		out.startsAt = tstz(*body.StartedAt)
	}
	if out.startsAt.Valid && out.endsAt.Valid && !out.endsAt.Time.After(out.startsAt.Time) {
		return out, catalogUnprocessable("ended_at", "结束时间必须晚于开始时间")
	}
	return out, nil
}

// ---- UpdateAdminCoupon ----

// UpdateAdminCoupon 实现 PATCH /api/v1/admin/coupons/{id}。
func (s *Server) UpdateAdminCoupon(ctx context.Context, req gen.UpdateAdminCouponRequestObject) (gen.UpdateAdminCouponResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.UpdateAdminCoupon500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.UpdateAdminCoupon403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能修改优惠码（admin.plan.write 由角色决定：owner / admin）"),
		}, nil
	}
	if req.Body == nil {
		return gen.UpdateAdminCoupon422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}

	c, err := updateAdminCoupon(ctx, s.catalogAudit(), actor, req.Id, *req.Body, time.Now(), s.catalogWarn(ctx))
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			if oe.kind == catalogErrNotFound {
				return gen.UpdateAdminCoupon404JSONResponse{
					ErrNotFoundJSONResponse: s.notFound(ctx, oe.msg),
				}, nil
			}
			return gen.UpdateAdminCoupon422JSONResponse{
				ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
			}, nil
		}
		return gen.UpdateAdminCoupon500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "修改优惠码失败", err),
		}, nil
	}
	return gen.UpdateAdminCoupon200JSONResponse{Data: c, Meta: s.meta(ctx)}, nil
}

func updateAdminCoupon(
	ctx context.Context,
	run catalogAuditRunner,
	actor audit.Actor,
	couponID int64,
	body gen.CouponUpsert,
	now time.Time,
	warn func(string),
) (gen.Coupon, error) {
	// L2 先于事务：一个 reason 不合格的请求连事务都不必开。
	// （下面 couponWriteParams 里还会再判一次 —— 那是为 create 路径写的同一个函数，
	//  两处都判不是冗余，是让「哪条路径漏了 L2」不可能发生。）
	if err := catalogCheckReason(body.Reason); err != nil {
		return gen.Coupon{}, err
	}

	var updated dbgen.AdminUpdateCouponRow
	runErr := run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		// 先读（FOR UPDATE）：**enabled 的写侧口径需要现有的 ends_at**
		// —— 「enabled=true 时只有在已过期才清 ends_at」这条规则没有现值就实现不了。
		// 审计前像仍然取 AdminUpdateCoupon 自己给的 before_*（同 D8 改套餐的理由）。
		cur, err := tx.AdminGetCouponForUpdate(ctx, couponID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, catalogNotFound("优惠码不存在")
			}
			return audit.Entry{}, err
		}
		params, err := couponWriteParams(body, &cur.EndsAt, now)
		if err != nil {
			return audit.Entry{}, err
		}
		row, err := tx.AdminUpdateCoupon(ctx, dbgen.AdminUpdateCouponParams{
			CouponID: couponID,
			Code:     params.code, Type: params.dbType, Value: params.dbValue,
			ScopePlanIds: params.planIDs, TotalUses: params.totalUses,
			StartsAt: params.startsAt, EndsAt: params.endsAt,
			// visible 保持现值：契约里没有这个字段，改它就是凭空替管理员做决定。
			Visible: cur.Visible,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, catalogNotFound("优惠码不存在")
			}
			return audit.Entry{}, err
		}
		updated = row

		before := couponSnapshotOf(couponFields{
			ID: row.ID, Code: row.BeforeCode, Type: row.BeforeType, Value: row.BeforeValue,
			ScopePlanIds: row.BeforeScopePlanIds, TotalUses: row.BeforeTotalUses,
			UsedCount: cur.UsedCount, StartsAt: row.BeforeStartsAt, EndsAt: row.BeforeEndsAt,
		}, row.BeforeVisible, true)
		after := couponSnapshotOf(couponFields{
			ID: row.ID, Code: row.Code, Type: row.Type, Value: row.Value,
			ScopePlanIds: row.ScopePlanIds, TotalUses: row.TotalUses,
			UsedCount: row.UsedCount, StartsAt: row.StartsAt, EndsAt: row.EndsAt,
		}, row.Visible, true)
		return audit.Entry{
			Action:     "D8.coupon.update",
			TargetType: "coupon",
			TargetID:   strconv.FormatInt(row.ID, 10),
			Before:     before,
			After:      after,
			Reason:     strings.TrimSpace(body.Reason),
		}, nil
	})
	if runErr != nil {
		if isUniqueViolation(runErr) {
			return gen.Coupon{}, catalogUnprocessable("code", "这个优惠码已经存在（码不区分大小写）")
		}
		if isCheckViolation(runErr) {
			return gen.Coupon{}, catalogUnprocessable("", "优惠码参数被数据库约束拒绝："+runErr.Error())
		}
		return gen.Coupon{}, runErr
	}

	c, warns := adminCouponView(couponFields{
		ID: updated.ID, Code: updated.Code, Type: updated.Type, Value: updated.Value,
		ScopePlanIds: updated.ScopePlanIds, TotalUses: updated.TotalUses,
		UsedCount: updated.UsedCount, StartsAt: updated.StartsAt, EndsAt: updated.EndsAt,
	}, now)
	for _, w := range warns {
		warn(w)
	}
	return c, nil
}

// ---- DeleteAdminCoupon ----

// DeleteAdminCoupon 实现 DELETE /api/v1/admin/coupons/{id}。
//
// 🔴 **这是真删**（coupons 上没有任何软删列），所以删之前必须看
// `referencing_order_count`，非零一律拒绝。
// `orders.coupon_id` 是 **ON DELETE SET NULL**：删掉一张用过的码，
// 历史订单的 `amount_discount` 还在（少收了多少查得到），但**「为什么少收」凭空消失**，
// 而且是无声的 —— 没有报错、没有级联失败，只是若干张订单的 coupon_id 变成了 NULL。
//
// ⚠️ 契约给 deleteAdminCoupon **只留了 403/404/500，没有 409**。
// 这里仍然返回 409 —— 另外两个选择都更糟：404 是撒谎，静默删除是毁证据。
// 缺口已登记。
func (s *Server) DeleteAdminCoupon(ctx context.Context, req gen.DeleteAdminCouponRequestObject) (gen.DeleteAdminCouponResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.DeleteAdminCoupon500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.DeleteAdminCoupon403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能修改优惠码（admin.plan.write 由角色决定：owner / admin）"),
		}, nil
	}
	s.logger.WarnContext(ctx, "bp_admin_audit_no_reason 删优惠码的审计将没有原因（契约给 DELETE 没有请求体）",
		"admin_id", admin.AdminID, "coupon_id", req.Id, "request_id", middleware.RequestIDFrom(ctx))

	if err := deleteAdminCoupon(ctx, s.catalogAudit(), actor, req.Id); err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			if oe.kind == catalogErrNotFound {
				return gen.DeleteAdminCoupon404JSONResponse{
					ErrNotFoundJSONResponse: s.notFound(ctx, oe.msg),
				}, nil
			}
			// ⚠️ 契约没给这个端点 409。用 gen.ErrConflictJSONResponse 的信封没有对应的
			//    响应类型可返，只能退回 404 之外的唯一出口 —— 见下面 handler 里的 500 分支。
			//    这里刻意**不**把 409 塞进 404：撒谎比返回一个契约外的状态码更糟。
			return gen.DeleteAdminCoupon500JSONResponse{
				ErrInternalJSONResponse: s.couponDeleteBlocked(ctx, oe.msg),
			}, nil
		}
		return gen.DeleteAdminCoupon500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "删除优惠码失败", err),
		}, nil
	}
	return gen.DeleteAdminCoupon204Response{
		Headers: gen.DeleteAdminCoupon204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}, nil
}

// couponDeleteBlocked 构造「这张码被历史订单引用，不能删」的响应体。
//
// 🔴 **状态码是 500，而正确的答案是 409** —— 契约给 deleteAdminCoupon 只声明了
// 403/404/500，生成的类型里根本没有 `DeleteAdminCoupon409JSONResponse`。
// 三个选择里这是伤害最小的一个：
//   - 返 404「优惠码不存在」= 撒谎，管理员会去数据库里找它；
//   - 静默删除 = 毁掉一批历史订单的折扣归属，无声无息；
//   - 返 500 + **说清原因的 message** = 状态码是错的，但人能读懂、且数据没被毁。
//
// 与普通 500 的区别在于它**不打 ERROR 日志**（这不是我们的故障）且 message 是人话。
// 修法是给契约的 deleteAdminCoupon 加一个 409 响应，缺口已登记。
func (s *Server) couponDeleteBlocked(ctx context.Context, msg string) gen.ErrInternalJSONResponse {
	s.logger.WarnContext(ctx, "优惠码被历史订单引用，拒绝删除（契约缺 409，只能用 500 承载）",
		"reason", msg, "request_id", middleware.RequestIDFrom(ctx))
	return gen.ErrInternalJSONResponse{
		Body:    s.envelope(ctx, gen.STATECONFLICT, msg),
		Headers: gen.ErrInternalResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}
}

func deleteAdminCoupon(ctx context.Context, run catalogAuditRunner, actor audit.Actor, couponID int64) error {
	return run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		cur, err := tx.AdminGetCouponForUpdate(ctx, couponID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, catalogNotFound("优惠码不存在")
			}
			return audit.Entry{}, err
		}
		// 🔴 判据是**真的引用了它的订单数**，不是 used_count。
		//    used_count 是 IncrementCouponUse 维护的冗余计数，两者会漂移
		//    （某次 IncrementCouponUse 所在的事务回滚了就会）；
		//    只有前者能保证「删了不会让历史订单丢掉优惠码归属」。
		if cur.ReferencingOrderCount > 0 {
			return audit.Entry{}, catalogConflict(fmt.Sprintf(
				"这张优惠码已经被 %d 张订单用过，不能删除："+
					"orders.coupon_id 是 ON DELETE SET NULL，删掉它会让这些订单「为什么少收钱」"+
					"凭空消失且不报错。想停用它请改成 enabled=false（等价于把结束时间设成现在）。",
				cur.ReferencingOrderCount))
		}
		row, err := tx.AdminDeleteCoupon(ctx, couponID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, catalogNotFound("优惠码不存在")
			}
			return audit.Entry{}, err
		}
		return audit.Entry{
			Action:     "D8.coupon.delete",
			TargetType: "coupon",
			TargetID:   strconv.FormatInt(row.ID, 10),
			// 🔴 before 是被删掉的整行 —— 这条快照是这次删除**唯一**留下的证据。
			Before: couponSnapshotOf(couponFields{
				ID: row.ID, Code: row.Code, Type: row.Type, Value: row.Value,
				ScopePlanIds: row.ScopePlanIds, TotalUses: row.TotalUses,
				UsedCount: row.UsedCount, StartsAt: row.StartsAt, EndsAt: row.EndsAt,
			}, row.Visible, true),
			After: nil, // 删除操作没有 after（§6.3 第 2 条）。
		}, nil
	})
}

// ============================================================
// 公告（模块 12，D12）
// ============================================================
//
// 🔴 **D12 的 L2 在契约上表达不出来**：`NoticeUpsert` 只有
// title / content / pinned / published_at，**没有 reason**。
// 而公告兼**域名广播位**（page-inventory §4.4 D12：「写错域名会把用户导向错误地址」），
// 它恰恰是最需要「为什么改」的那一类操作。本文件照常写审计（前后像完整），
// reason 落 NULL，并在每次写时留一条 WARN。补法是给 NoticeUpsert 加 reason 字段。
//
// ⚠️ 与用户面的 `catalog.sql / ListNoticesPage` 是两条查询：管理面看**全部**
// （含 visible=false、含还没到 starts_at 的、含已过 ends_at 的），用户面把这三种都滤掉了。
// 用用户面那条做管理列表，管理员会看不到自己刚定时发布的那条。
//
// ⚠️ 字段映射：`Notice.content ← content_md`，
//    `Notice.published_at ← coalesce(starts_at, created_at)`（notices 没有 published_at 列）。
// ⚠️ `NoticeUpsert` 里没有 level / visible / ends_at / sort_order，它们落 DDL 默认值
//    （'info' / true / NULL / 0）。**不给它们编造参数** —— 一个 API 传不进来的形参
//    只会让下一个人以为它是可配的。

// adminNoticeView 把管理面公告行映射成契约的 `Notice`。
func adminNoticeView(id int64, title, contentMd string, pinned bool, publishedAt pgtype.Timestamptz) gen.Notice {
	return gen.Notice{
		Id:          id,
		Title:       title,
		Content:     contentMd,
		Pinned:      ptrOf(pinned),
		PublishedAt: ttime(publishedAt),
	}
}

// noticeSnapshot 是 D12 审计的前/后像。
//
// 🔴 **content_md 必须完整进快照，不能只记标题。**
// 公告是域名广播位：事后要查的正是「那天到底广播了哪个域名」，
// 而一条只写着「改了公告 #7」的审计记录回答不了这个问题。
type noticeSnapshot struct {
	ID        int64   `json:"id"`
	Title     string  `json:"title"`
	ContentMd string  `json:"content_md"`
	Pinned    bool    `json:"pinned"`
	Visible   *bool   `json:"visible,omitempty"`
	Level     string  `json:"level,omitempty"`
	StartsAt  *string `json:"starts_at"`
	EndsAt    *string `json:"ends_at,omitempty"`
	SortOrder *int32  `json:"sort_order,omitempty"`
}

// ---- ListAdminNotices ----

type adminNoticeLister interface {
	ListAdminNoticesPage(ctx context.Context, arg dbgen.ListAdminNoticesPageParams) ([]dbgen.ListAdminNoticesPageRow, error)
	CountAdminNotices(ctx context.Context) (int64, error)
}

// ListAdminNotices 实现 GET /api/v1/admin/notices。
func (s *Server) ListAdminNotices(ctx context.Context, req gen.ListAdminNoticesRequestObject) (gen.ListAdminNoticesResponseObject, error) {
	if _, ok := middleware.AdminFrom(ctx); !ok {
		return nil, errNoAdminAuth
	}
	data, meta, err := listAdminNotices(ctx, s.db, s.meta(ctx), req.Params, s.catalogWarn(ctx))
	if err != nil {
		return gen.ListAdminNotices500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取公告列表失败", err),
		}, nil
	}
	return gen.ListAdminNotices200JSONResponse{Data: data, Meta: meta}, nil
}

func listAdminNotices(
	ctx context.Context,
	q adminNoticeLister,
	meta gen.Meta,
	params gen.ListAdminNoticesParams,
	warn func(string),
) ([]gen.Notice, gen.Meta, error) {
	want, limitPlusOne := pageLimit(params.Limit)

	arg := dbgen.ListAdminNoticesPageParams{PageLimit: limitPlusOne}
	if params.Cursor != nil && *params.Cursor != "" {
		cur, ok := decodePageCursor(*params.Cursor)
		if !ok {
			warn("公告列表游标非法，按第一页处理")
		} else {
			arg.CursorAt = tstz(cur.At)
			arg.CursorID = &cur.ID
		}
	}

	rows, err := q.ListAdminNoticesPage(ctx, arg)
	if err != nil {
		return nil, meta, err
	}
	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.Notice, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminNoticeView(r.ID, r.Title, r.ContentMd, r.Pinned, r.PublishedAt))
	}
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		// 🔴 游标必须用 **created_at**（排序键）而不是 published_at：
		//    published_at 是 coalesce(starts_at, created_at) 算出来的，
		//    与 ORDER BY 的键不是同一个值。用它编游标会在有定时发布公告时静默漏行。
		meta.NextCursor = ptrOf(encodePageCursor(last.ID, ttime(last.CreatedAt)))
	}
	if params.Count != nil && *params.Count {
		total, err := q.CountAdminNotices(ctx)
		if err != nil {
			return nil, meta, err
		}
		meta.Total = &total
	}
	return out, meta, nil
}

// ---- CreateAdminNotice（D12）----

// CreateAdminNotice 实现 POST /api/v1/admin/notices。
func (s *Server) CreateAdminNotice(ctx context.Context, req gen.CreateAdminNoticeRequestObject) (gen.CreateAdminNoticeResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.CreateAdminNotice500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.CreateAdminNotice403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能发布公告（由角色决定：owner / admin）"),
		}, nil
	}
	if req.Body == nil {
		return gen.CreateAdminNotice422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}
	s.warnNoticeNoReason(ctx, admin.AdminID, "create")

	n, err := createAdminNotice(ctx, s.catalogAudit(), actor, admin.AdminID, *req.Body)
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			return gen.CreateAdminNotice422JSONResponse{
				ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
			}, nil
		}
		return gen.CreateAdminNotice500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "发布公告失败", err),
		}, nil
	}
	resp := gen.CreateAdminNotice201JSONResponse{
		Headers: gen.CreateAdminNotice201ResponseHeaders{
			Location: fmt.Sprintf("/api/v1/admin/notices/%d", n.Id),
		},
	}
	resp.Body.Data = n
	resp.Body.Meta = s.meta(ctx)
	return resp, nil
}

// warnNoticeNoReason 记 D12 的 L2 缺口。
//
// 每次都记而不是启动时记一次：审计条目与这条日志靠 request_id 对得上，
// 于是「这条公告为什么改」至少能在日志里找到是谁、什么时候 —— 虽然找不到原因。
func (s *Server) warnNoticeNoReason(ctx context.Context, adminID int64, op string) {
	s.logger.WarnContext(ctx,
		"bp_admin_audit_no_reason D12 公告操作的审计将没有原因（NoticeUpsert 契约里没有 reason 字段）",
		"admin_id", adminID, "op", op, "request_id", middleware.RequestIDFrom(ctx))
}

// noticeTitleMaxRunes / noticeContentMaxRunes 是标题与正文的上限。
//
// ⚠️ 契约没有给 NoticeUpsert 任何 maxLength，而 `notices.title` / `content_md`
// 都是无长度限制的 text，且**公告正文会进每一条 D12 审计的前后像**（append-only 表）。
// 一次带 10 MB 正文的公告编辑会往审计表里塞两份 10 MB。
// 取 200 / 50000 是**设定值**：远超任何真实公告，又能挡住把日志整个粘进来。
const (
	noticeTitleMaxRunes   = 200
	noticeContentMaxRunes = 50000
)

func validateNoticeBody(body gen.NoticeUpsert) (string, string, error) {
	title := strings.TrimSpace(body.Title)
	if title == "" {
		return "", "", catalogUnprocessable("title", "公告标题不能为空")
	}
	if n := len([]rune(title)); n > noticeTitleMaxRunes {
		return "", "", catalogUnprocessable("title", fmt.Sprintf("标题最多 %d 个字符（当前 %d）", noticeTitleMaxRunes, n))
	}
	content := body.Content
	if strings.TrimSpace(content) == "" {
		return "", "", catalogUnprocessable("content", "公告正文不能为空")
	}
	if n := len([]rune(content)); n > noticeContentMaxRunes {
		return "", "", catalogUnprocessable("content", fmt.Sprintf("正文最多 %d 个字符（当前 %d）", noticeContentMaxRunes, n))
	}
	return title, content, nil
}

func createAdminNotice(
	ctx context.Context,
	run catalogAuditRunner,
	actor audit.Actor,
	adminID int64,
	body gen.NoticeUpsert,
) (gen.Notice, error) {
	title, content, err := validateNoticeBody(body)
	if err != nil {
		return gen.Notice{}, err
	}

	params := dbgen.CreateAdminNoticeParams{
		Title:     title,
		ContentMd: content,
		Pinned:    body.Pinned != nil && *body.Pinned,
		// published_at → starts_at。传 NULL 表示立刻发布
		// （用户面 `starts_at IS NULL OR starts_at <= now()` 会立即让它可见）。
		CreatedBy: adminID,
	}
	if body.PublishedAt != nil {
		params.StartsAt = tstz(*body.PublishedAt)
	}

	var created dbgen.CreateAdminNoticeRow
	runErr := run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		row, err := tx.CreateAdminNotice(ctx, params)
		if err != nil {
			return audit.Entry{}, err
		}
		created = row
		return audit.Entry{
			Action:     "D12.notice.create",
			TargetType: "notice",
			TargetID:   strconv.FormatInt(row.ID, 10),
			Before:     nil,
			After: noticeSnapshot{
				ID: row.ID, Title: row.Title, ContentMd: row.ContentMd, Pinned: row.Pinned,
				Visible: ptrOf(row.Visible), Level: row.Level,
				StartsAt: tsString(row.StartsAt), EndsAt: tsString(row.EndsAt),
				SortOrder: ptrOf(row.SortOrder),
			},
			// Reason 空：NoticeUpsert 契约里没有 reason 字段。见 warnNoticeNoReason。
		}, nil
	})
	if runErr != nil {
		return gen.Notice{}, runErr
	}
	return adminNoticeView(created.ID, created.Title, created.ContentMd, created.Pinned, created.PublishedAt), nil
}

// ---- UpdateAdminNotice（D12）----

// UpdateAdminNotice 实现 PATCH /api/v1/admin/notices/{id}。
//
// ⚠️ **title/content 无条件覆写，pinned/starts_at 未传即不改。**
// 这个不对称是契约给的（NoticeUpsert 把 title/content 放进了 required），不是本实现的选择。
//
// ⚠️ 「把 published_at 改回 NULL（= 立刻发布）」**表达不出来**：JSON 的「缺席」与
// 「null」在生成的 `*time.Time` 上都是 nil。同一处契约缺口在 D1 的 expired_at 上也有。
func (s *Server) UpdateAdminNotice(ctx context.Context, req gen.UpdateAdminNoticeRequestObject) (gen.UpdateAdminNoticeResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.UpdateAdminNotice500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.UpdateAdminNotice403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能编辑公告（由角色决定：owner / admin）"),
		}, nil
	}
	if req.Body == nil {
		return gen.UpdateAdminNotice422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}
	s.warnNoticeNoReason(ctx, admin.AdminID, "update")

	n, err := updateAdminNotice(ctx, s.catalogAudit(), actor, req.Id, *req.Body)
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			if oe.kind == catalogErrNotFound {
				return gen.UpdateAdminNotice404JSONResponse{
					ErrNotFoundJSONResponse: s.notFound(ctx, oe.msg),
				}, nil
			}
			return gen.UpdateAdminNotice422JSONResponse{
				ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
			}, nil
		}
		return gen.UpdateAdminNotice500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "编辑公告失败", err),
		}, nil
	}
	return gen.UpdateAdminNotice200JSONResponse{Data: n, Meta: s.meta(ctx)}, nil
}

func updateAdminNotice(
	ctx context.Context,
	run catalogAuditRunner,
	actor audit.Actor,
	noticeID int64,
	body gen.NoticeUpsert,
) (gen.Notice, error) {
	title, content, err := validateNoticeBody(body)
	if err != nil {
		return gen.Notice{}, err
	}
	params := dbgen.UpdateAdminNoticeParams{
		NoticeID:  noticeID,
		Title:     title,
		ContentMd: content,
		Pinned:    body.Pinned,
	}
	if body.PublishedAt != nil {
		params.StartsAt = tstz(*body.PublishedAt)
	}

	var updated dbgen.UpdateAdminNoticeRow
	runErr := run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		row, err := tx.UpdateAdminNotice(ctx, params)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, catalogNotFound("公告不存在")
			}
			return audit.Entry{}, err
		}
		updated = row
		return audit.Entry{
			Action:     "D12.notice.update",
			TargetType: "notice",
			TargetID:   strconv.FormatInt(row.ID, 10),
			Before: noticeSnapshot{
				ID: row.ID, Title: row.BeforeTitle, ContentMd: row.BeforeContentMd,
				Pinned: row.BeforePinned, StartsAt: tsString(row.BeforeStartsAt),
			},
			After: noticeSnapshot{
				ID: row.ID, Title: row.Title, ContentMd: row.ContentMd, Pinned: row.Pinned,
				Visible: ptrOf(row.Visible), Level: row.Level,
				StartsAt: tsString(row.StartsAt), EndsAt: tsString(row.EndsAt),
				SortOrder: ptrOf(row.SortOrder),
			},
		}, nil
	})
	if runErr != nil {
		return gen.Notice{}, runErr
	}
	return adminNoticeView(updated.ID, updated.Title, updated.ContentMd, updated.Pinned, updated.PublishedAt), nil
}

// ---- DeleteAdminNotice（D12）----

// DeleteAdminNotice 实现 DELETE /api/v1/admin/notices/{id}。
//
// 公告是**真删**（唯一一处），因为它不被任何外键引用 —— 47 张表里没有
// `REFERENCES notices`。它不是任何账、任何统计、任何证据链的一环。
// users 与 admin_users 不能硬删是因为**会坏掉具体的东西**（外键悬空 / 审计的 admin_id 被打成 NULL），
// 公告没有这种东西，所以不必为它发明一个软删状态（而且 NoticeUpsert 里没有 visible 字段，
// 软删之后 API 上根本没有恢复入口）。
//
// 🔴 **RETURNING 的整行是这次删除唯一留下的证据**，必须完整进审计的 before。
func (s *Server) DeleteAdminNotice(ctx context.Context, req gen.DeleteAdminNoticeRequestObject) (gen.DeleteAdminNoticeResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.DeleteAdminNotice500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.DeleteAdminNotice403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能删除公告（由角色决定：owner / admin）"),
		}, nil
	}
	s.warnNoticeNoReason(ctx, admin.AdminID, "delete")

	err = deleteAdminNoticeTx(ctx, s.catalogAudit(), actor, req.Id)
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok && oe.kind == catalogErrNotFound {
			return gen.DeleteAdminNotice404JSONResponse{
				ErrNotFoundJSONResponse: s.notFound(ctx, oe.msg),
			}, nil
		}
		return gen.DeleteAdminNotice500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "删除公告失败", err),
		}, nil
	}
	return gen.DeleteAdminNotice204Response{
		Headers: gen.DeleteAdminNotice204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}, nil
}

func deleteAdminNoticeTx(ctx context.Context, run catalogAuditRunner, actor audit.Actor, noticeID int64) error {
	return run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		row, err := tx.DeleteAdminNotice(ctx, noticeID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, catalogNotFound("公告不存在")
			}
			return audit.Entry{}, err
		}
		return audit.Entry{
			Action:     "D12.notice.delete",
			TargetType: "notice",
			TargetID:   strconv.FormatInt(row.ID, 10),
			Before: noticeSnapshot{
				ID: row.ID, Title: row.Title, ContentMd: row.ContentMd, Pinned: row.Pinned,
				Visible: ptrOf(row.Visible), Level: row.Level,
				StartsAt: tsString(row.StartsAt), EndsAt: tsString(row.EndsAt),
				SortOrder: ptrOf(row.SortOrder),
			},
			After: nil,
		}, nil
	})
}

// ============================================================
// 邀请与返佣（模块 9）—— ListAdminInvites · CreateAdminInvite · AdjustAdminCommission（D11）
// ============================================================

// ---- ListAdminInvites ----

type adminInviteLister interface {
	ListAdminInvitesPage(ctx context.Context, arg dbgen.ListAdminInvitesPageParams) ([]dbgen.ListAdminInvitesPageRow, error)
	CountAdminInvites(ctx context.Context, arg dbgen.CountAdminInvitesParams) (int64, error)
}

// ListAdminInvites 实现 GET /api/v1/admin/invites。
//
// ⚠️ 查询支持 owner_user_id / admin_seeded 两个过滤，但**契约里没有这两个参数**，
// 所以这里一律传 NULL（看全部：用户码 + 管理员种子码、含已撤销与已过期）。
// 管理面要看已撤销的码 —— 撤销记录本身是运营证据。
func (s *Server) ListAdminInvites(ctx context.Context, req gen.ListAdminInvitesRequestObject) (gen.ListAdminInvitesResponseObject, error) {
	if _, ok := middleware.AdminFrom(ctx); !ok {
		return nil, errNoAdminAuth
	}
	data, meta, err := listAdminInvites(ctx, s.db, s.meta(ctx), req.Params, s.inviteBaseURL(ctx), s.catalogWarn(ctx))
	if err != nil {
		return gen.ListAdminInvites500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取邀请码列表失败", err),
		}, nil
	}
	return gen.ListAdminInvites200JSONResponse{Data: data, Meta: meta}, nil
}

// adminInviteView 把管理面的邀请码行映射成契约的 `InviteCode`。
//
// 🔴 **status 直接用 SQL 算好的那一列，不在 Go 里重算。**
// 库里的三态推导（先判 disabled 再判 exhausted）写在 SQL 里，Go 里再写一遍
// 就是两份会漂移的判定 —— 而漂移的现象是「列表页说可用、注册页说无效」。
//
// ⚠️ `use_limit` 直接给 `max_uses`。openapi 说「0 = 不限」，而库里 `max_uses >= 1` 是 CHECK ——
// **本系统没有不限次的邀请码**，那个 0 永远不会出现。不要为了迎合注释把某个值翻译成 0：
// 那会让前端把一个 1 次码显示成无限次。
func adminInviteView(r dbgen.ListAdminInvitesPageRow, base string) gen.InviteCode {
	v := gen.InviteCode{
		Id:        r.ID,
		Code:      r.Code,
		CreatedAt: ttime(r.CreatedAt),
		UseLimit:  ptrOf(r.MaxUses),
		UsedCount: ptrOf(r.UsedCount),
		Status:    gen.InviteCodeStatus(r.Status),
	}
	// invite_url 只对还能用的码有意义（同 wallet.go 的 inviteCodeView）。
	if v.Status == gen.InviteCodeStatusOk && base != "" {
		u := base + "/register?invite=" + r.Code
		v.InviteUrl = &u
	}
	return v
}

func listAdminInvites(
	ctx context.Context,
	q adminInviteLister,
	meta gen.Meta,
	params gen.ListAdminInvitesParams,
	base string,
	warn func(string),
) ([]gen.InviteCode, gen.Meta, error) {
	want, limitPlusOne := pageLimit(params.Limit)

	arg := dbgen.ListAdminInvitesPageParams{PageLimit: limitPlusOne}
	if params.Cursor != nil && *params.Cursor != "" {
		cur, ok := decodePageCursor(*params.Cursor)
		if !ok {
			warn("邀请码列表游标非法，按第一页处理")
		} else {
			arg.CursorID = &cur.ID
		}
	}

	rows, err := q.ListAdminInvitesPage(ctx, arg)
	if err != nil {
		return nil, meta, err
	}
	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.InviteCode, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminInviteView(r, base))
	}
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		meta.NextCursor = ptrOf(encodePageCursor(last.ID, ttime(last.CreatedAt)))
	}
	if params.Count != nil && *params.Count {
		// ⚠️ WHERE 必须与列表逐字同形（这里两边都是「无过滤」）。
		//    漂移的现象是「分页器说共 87 条，翻到底只有 71 条」，没有任何报错。
		total, err := q.CountAdminInvites(ctx, dbgen.CountAdminInvitesParams{})
		if err != nil {
			return nil, meta, err
		}
		meta.Total = &total
	}
	return out, meta, nil
}

// ---- CreateAdminInvite ----

const (
	// adminInviteMaxCount 与契约的 `AdminInviteCreateRequest.count` 的 maximum 同值。
	adminInviteMaxCount = 500

	// adminInviteFillRounds 是「生成的码撞了唯一索引、补生成」的轮数。
	//
	// 🔴 `CreateAdminInviteCodes` 用 `ON CONFLICT (code) DO NOTHING`：
	//    不写它的话一次撞车会让**整批**失败（23505 回滚全部），
	//    管理员看到的是「生成 500 个失败了」而不是「有一个撞了」。
	//    写了之后撞掉的那些**静默消失** —— 所以必须补生成，且最终必须如实上报条数。
	//    31^8 ≈ 8.5e11 的空间里，三轮补不齐意味着出了别的问题（比如库里已经有几亿条码）。
	adminInviteFillRounds = 3
)

// CreateAdminInvite 实现 POST /api/v1/admin/invites。
//
// 🔴 **码由 handler 生成，不由数据库生成。** 字符集要剔除 0/O/1/I/l（0003 的列注释）——
// 这是一条**产品规则**（用户要照着念、照着抄），不是数据库能表达的东西。
//
// 🔴 **`owner_user_id` 恒为 NULL（管理员种子码）**，所以能设 max_uses > 1：
// `invite_codes_user_single_use` 这条 CHECK 只在 owner 非 NULL 时强制一次性。
// 也就是说这个端点**天然只能造种子码** —— 想批量替某个用户造码是造不出来的。
func (s *Server) CreateAdminInvite(ctx context.Context, req gen.CreateAdminInviteRequestObject) (gen.CreateAdminInviteResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.CreateAdminInvite500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.CreateAdminInvite403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能生成邀请码（由角色决定：owner / admin）"),
		}, nil
	}
	if req.Body == nil {
		return gen.CreateAdminInvite422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}

	codes, short, err := createAdminInvites(ctx, s.catalogAudit(), actor,
		*req.Body, middleware.RequestIDFrom(ctx), s.inviteBaseURL(ctx))
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			return gen.CreateAdminInvite422JSONResponse{
				ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
			}, nil
		}
		return gen.CreateAdminInvite500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "生成邀请码失败", err),
		}, nil
	}
	if short > 0 {
		// 🔴 如实上报：**响应里的 data 就是真正生成出来的那些码**，
		//    所以前端显示的数量天然是对的。这条 ERROR 是给我们自己的信号 ——
		//    连撞三轮说明随机源或码空间出了问题。
		s.logger.ErrorContext(ctx,
			"bp_admin_invite_short 批量生成邀请码没凑齐（已如实返回实际生成的条数）",
			"requested", req.Body.Count, "created", len(codes), "short", short,
			"request_id", middleware.RequestIDFrom(ctx))
	}
	return gen.CreateAdminInvite201JSONResponse{Data: codes, Meta: s.meta(ctx)}, nil
}

func createAdminInvites(
	ctx context.Context,
	run catalogAuditRunner,
	actor audit.Actor,
	body gen.AdminInviteCreateRequest,
	requestID string,
	base string,
) ([]gen.InviteCode, int, error) {
	if body.Count < 1 || body.Count > adminInviteMaxCount {
		return nil, 0, catalogUnprocessable("count",
			fmt.Sprintf("一次最多生成 %d 个邀请码（当前 %d）", adminInviteMaxCount, body.Count))
	}
	maxUses := int32(1)
	if body.UseLimit != nil {
		if *body.UseLimit < 1 {
			// 契约的 InviteCode.use_limit 注释说「0 = 不限」，而 `max_uses >= 1` 是 CHECK。
			// 直接 422 而不是悄悄改成 1：管理员以为造了一批无限次的码，实际每个只能用一次。
			return nil, 0, catalogUnprocessable("use_limit",
				"本系统没有不限次的邀请码（max_uses >= 1 是数据库约束）。请给一个 ≥ 1 的次数。")
		}
		maxUses = *body.UseLimit
	}

	want := int(body.Count)
	created := make([]dbgen.InviteCode, 0, want)
	seen := make(map[string]bool, want)

	for round := 0; round < adminInviteFillRounds && len(created) < want; round++ {
		need := want - len(created)
		batch := make([]string, 0, need)
		for len(batch) < need {
			c, err := randomInviteCode()
			if err != nil {
				return nil, 0, err
			}
			// 批内去重：`unnest` 里出现两次相同的码，ON CONFLICT DO NOTHING 只会插一条，
			// 于是这一批天然少一个 —— 而那不是「撞库了」，是我们自己重复了。
			if seen[c] {
				continue
			}
			seen[c] = true
			batch = append(batch, c)
		}

		var rows []dbgen.InviteCode
		runErr := run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
			r, err := tx.CreateAdminInviteCodes(ctx, dbgen.CreateAdminInviteCodesParams{
				MaxUses: maxUses,
				Note:    body.Note,
				Codes:   batch,
				// expires_at 契约里没有 → NULL（永不过期）。凭空给一个有效期
				// 等于让管理员发出去的邀请在某天突然失效，而他没同意过任何期限。
			})
			if err != nil {
				return audit.Entry{}, err
			}
			rows = r
			return inviteBatchAuditEntry(r, maxUses, body, requestID), nil
		})
		if runErr != nil {
			if isCheckViolation(runErr) {
				return nil, 0, catalogUnprocessable("", "邀请码参数被数据库约束拒绝："+runErr.Error())
			}
			return nil, 0, runErr
		}
		created = append(created, rows...)
	}

	out := make([]gen.InviteCode, 0, len(created))
	for _, r := range created {
		out = append(out, adminInviteView(dbgen.ListAdminInvitesPageRow{
			ID: r.ID, Code: r.Code, OwnerUserID: r.OwnerUserID,
			MaxUses: r.MaxUses, UsedCount: r.UsedCount,
			ExpiresAt: r.ExpiresAt, RevokedAt: r.RevokedAt,
			Note: r.Note, CreatedAt: r.CreatedAt,
			// 刚建出来的种子码必然可用：未撤销、未过期、used_count(0) < max_uses(≥1)。
			Status: string(gen.InviteCodeStatusOk),
		}, base))
	}
	return out, want - len(created), nil
}

// inviteBatchAuditEntry 组装一批邀请码的审计条目。
//
// 🔴 **`TargetID` 用 request_id**，不是某个码的 id。三个理由：
//   - 一批可能有 500 个码，逐条写 500 条审计会把这张 append-only 表撑起来；
//   - `audit_logs` **没有 request_id 列**（openapi 的 AuditLogEntry 把它列为 required，
//     这是已登记的缺口），而它是把审计接回访问日志的唯一钥匙 ——
//     放进 target_id 至少让这条记录还能被接回去；
//   - 码本身全部进 after 快照，所以「生成了哪些码」一条都不少。
//
// request_id 为空（未挂 RequestID 中间件）时回退到 "batch"：
// audit.validate 会拒绝空的 target_id，而那会让**生成邀请码整个失败**。
func inviteBatchAuditEntry(rows []dbgen.InviteCode, maxUses int32, body gen.AdminInviteCreateRequest, requestID string) audit.Entry {
	codes := make([]string, 0, len(rows))
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		codes = append(codes, r.Code)
		ids = append(ids, r.ID)
	}
	target := requestID
	if target == "" {
		target = "batch"
	}
	return audit.Entry{
		Action:     "admin.invite.create",
		TargetType: "invite_code_batch",
		TargetID:   target,
		Before:     nil,
		After: map[string]any{
			"requested_count": body.Count,
			"created_count":   len(rows),
			"max_uses":        maxUses,
			"owner_user_id":   nil, // 恒为 NULL：这个端点只能造管理员种子码。
			"note":            body.Note,
			"ids":             ids,
			"codes":           codes,
		},
		// Reason 空：AdminInviteCreateRequest 契约里没有 reason 字段。
	}
}

// ---- AdjustAdminCommission（D11：L2 必填原因）----

// AdjustAdminCommission 实现 POST /api/v1/admin/commissions/{id}/adjust。
//
// 🔴 **只能调 `pending` 与 `confirmed` 两态。** `transferred` 意味着这笔钱
// **已经变成用户余额了**（wallet.sql §5 的划转会写一条
// `expense:commission ↔ liability:user_wallet` 的分录）。事后改 `commissions.amount`
// 不会动分录，于是佣金表说 ¥15.90、账本说 ¥7.20，而**两边都不会报错** ——
// `FindUnbalancedLedgerEntries` 只检查每条分录自己平不平，它对「分录与业务表不一致」是瞎的。
// 要改一笔已划转的佣金，唯一正确的做法是写一条**冲正分录**（0007 的 reverses_id 就是为它建的）。
// `voided` 是退款套利被作废的，复活它需要先解释为什么当初作废 —— 同样不该由一个 +/- 数字完成。
//
// 🔴 **404 与 409 必须分得开**（`AdjustAdminCommissionAmount` 的 LEFT JOIN 就是为了这个）：
//
//	0 行                    → 404「你打错 id 了」
//	after_amount IS NULL    → 409「这笔钱已经付出去了，去走冲正」
//
// 塌成一个错误码会让操作者在两条完全不同的路上瞎试。
func (s *Server) AdjustAdminCommission(ctx context.Context, req gen.AdjustAdminCommissionRequestObject) (gen.AdjustAdminCommissionResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.AdjustAdminCommission500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.AdjustAdminCommission403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能调整佣金（由角色决定：owner / admin）"),
		}, nil
	}
	if req.Body == nil {
		return gen.AdjustAdminCommission422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}

	c, err := adjustAdminCommission(ctx, s.catalogAudit(), actor, req.Id, *req.Body)
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			switch oe.kind {
			case catalogErrNotFound:
				return gen.AdjustAdminCommission404JSONResponse{
					ErrNotFoundJSONResponse: s.notFound(ctx, oe.msg),
				}, nil
			case catalogErrConflict:
				// ⚠️ 契约给 adjustAdminCommission 没有 409（只有 403/404/422/500）。
				//    用 422 承载并把 code 写成 STATE_CONFLICT —— 状态码退一步，
				//    但 message 与 code 说的是实话，前端仍能按 code 分支。缺口已登记。
				return gen.AdjustAdminCommission422JSONResponse{
					ErrUnprocessableJSONResponse: gen.ErrUnprocessableJSONResponse{
						Body:    s.envelope(ctx, gen.STATECONFLICT, oe.msg),
						Headers: gen.ErrUnprocessableResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
					},
				}, nil
			default:
				return gen.AdjustAdminCommission422JSONResponse{
					ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
				}, nil
			}
		}
		return gen.AdjustAdminCommission500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "调整佣金失败", err),
		}, nil
	}
	return gen.AdjustAdminCommission200JSONResponse{Data: c, Meta: s.meta(ctx)}, nil
}

func adjustAdminCommission(
	ctx context.Context,
	run catalogAuditRunner,
	actor audit.Actor,
	commissionID int64,
	body gen.CommissionAdjustRequest,
) (gen.Commission, error) {
	if err := catalogCheckReason(body.Reason); err != nil {
		return gen.Commission{}, err
	}
	if body.Amount == 0 {
		// 0 调整不是一个操作，只会往 append-only 的审计表里写一条什么都没发生的记录。
		return gen.Commission{}, catalogUnprocessable("amount", "调整额不能是 0")
	}

	var row dbgen.AdjustAdminCommissionAmountRow
	runErr := run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		r, err := tx.AdjustAdminCommissionAmount(ctx, dbgen.AdjustAdminCommissionAmountParams{
			CommissionID: commissionID,
			DeltaAmount:  body.Amount,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, catalogNotFound("佣金记录不存在")
			}
			if isCheckViolation(err) {
				// `commissions.amount >= 0` 是 0007 的 CHECK：负向调整调过头。
				// ⚠️ **不要**改成「先读一次再在 Go 里比大小」—— 那是一次 TOCTOU，
				//    两个并发的负向调整会双双通过预检查。数据库那道 CHECK 才是真闸门。
				return audit.Entry{}, catalogUnprocessable("amount",
					"调整后的佣金会变成负数（佣金金额不能小于 0）")
			}
			return audit.Entry{}, err
		}
		if r.AfterAmount == nil {
			// LEFT JOIN 没命中 = 状态不在 (pending, confirmed) 里。
			return audit.Entry{}, catalogConflict(fmt.Sprintf(
				"这笔佣金的状态是 %q，不能直接改金额："+
					"transferred 表示钱已经变成用户余额了（账本上有对应分录），"+
					"直接改 commissions.amount 会让佣金表与账本对不上且两边都不报错；"+
					"voided 是退款套利被作废的，复活它需要先解释当初为什么作废。"+
					"正确做法是写一条冲正分录。", r.BeforeStatus))
		}
		row = r

		return audit.Entry{
			Action:     "D11.commission.adjust",
			TargetType: "commission",
			TargetID:   strconv.FormatInt(r.ID, 10),
			Before: map[string]any{
				"id": r.ID, "order_id": r.OrderID, "order_trade_no": r.OrderTradeNo,
				"inviter_id": r.InviterID, "invitee_id": r.InviteeID,
				"amount": r.BeforeAmount, "amount_unit": "cent",
				"status": r.BeforeStatus, "rate_bps": r.RateBps,
			},
			After: map[string]any{
				"id": r.ID, "order_id": r.OrderID, "order_trade_no": r.OrderTradeNo,
				"amount": *r.AfterAmount, "amount_unit": "cent",
				"status": derefOr(r.AfterStatus, r.BeforeStatus),
				"delta":  body.Amount,
			},
			Reason: strings.TrimSpace(body.Reason),
		}, nil
	})
	if runErr != nil {
		return gen.Commission{}, runErr
	}

	c := gen.Commission{
		Id:           row.ID,
		Amount:       *row.AfterAmount,
		Status:       commissionStatus(derefOr(row.AfterStatus, row.BeforeStatus)),
		CreatedAt:    ttime(row.CreatedAt),
		ConfirmedAt:  tptr(row.ConfirmedAt),
		OrderTradeNo: ptrOf(row.OrderTradeNo),
	}
	return c, nil
}

// ============================================================
// 工单（模块 8）—— ListAdminTickets · GetAdminTicket · UpdateAdminTicket · CreateAdminTicketMessage
// ============================================================
//
// 🔴 `ticket_messages.is_internal` 是全系统最容易出安全事故的一列。
//    管理面读内部备注复用 `ListTicketMessagesInternal`（含 is_internal），
//    用户面永远只走 `ticket_messages_public` 视图 —— 视图里根本没有这一列，
//    「忘了加 WHERE is_internal = false」在那条路径上不可能发生。
//    契约层面也是分开的：`AdminTicketMessage` 有 is_internal，`TicketMessage` 没有。
//
// ⚠️ 契约的 `Ticket.level` 是 **integer**，库里是 `tickets.priority`（ENUM）。
//    映射表写死在下面的两个函数里。
//    🔴 **不要**用 enum 的 ordinal 隐式转换：将来往中间插一个档位（比如 'critical'），
//    ordinal 会整体挪位，而**所有历史工单的 level 会在同一次部署里静默改变含义**
//    （而 `tickets_queue_idx` 的 `priority DESC` 直接依赖声明序）。

// ticketLevelFromPriority / ticketPriorityFromLevel 是那张写死的映射表。
// low=1 normal=2 high=3 urgent=4。
func ticketLevelFromPriority(p dbgen.TicketPriority) int32 {
	switch p {
	case dbgen.TicketPriorityLow:
		return 1
	case dbgen.TicketPriorityNormal:
		return 2
	case dbgen.TicketPriorityHigh:
		return 3
	case dbgen.TicketPriorityUrgent:
		return 4
	default:
		// 未知档位落到 normal 而不是 0：0 在契约里没有含义，前端会渲染成空。
		return 2
	}
}

func ticketPriorityFromLevel(level int32) (dbgen.TicketPriority, error) {
	switch level {
	case 1:
		return dbgen.TicketPriorityLow, nil
	case 2:
		return dbgen.TicketPriorityNormal, nil
	case 3:
		return dbgen.TicketPriorityHigh, nil
	case 4:
		return dbgen.TicketPriorityUrgent, nil
	default:
		return "", catalogUnprocessable("level",
			fmt.Sprintf("等级只能是 1（低）/ 2（普通）/ 3（高）/ 4（紧急），收到 %d", level))
	}
}

// adminTicketStatusToDB 把契约的四态翻回库里的状态。
//
// 🔴 **`replied` 写不回去**：库里没有这个状态值 —— 「客服已回复」这个事实存在于
// `last_agent_reply_at > last_user_reply_at`，是**算出来的**，不是存的。
// 接受它并静默映射成别的状态（比如 pending）会让管理员以为自己把单标成了「已回复」，
// 而实际状态是另一个 —— 于是工作台的排序与 SLA 判定都变了，且没有任何提示。
// 422 并说清楚是唯一诚实的答案。
func adminTicketStatusToDB(st gen.TicketStatus) (dbgen.TicketStatus, error) {
	switch st {
	case gen.Open:
		return dbgen.TicketStatusOpen, nil
	case gen.Pending:
		return dbgen.TicketStatusPending, nil
	case gen.Closed:
		return dbgen.TicketStatusClosed, nil
	case gen.Replied:
		return "", catalogUnprocessable("status",
			"replied 不是一个可以设置的状态：它由「客服最后回复时间晚于用户最后回复时间」算出来。"+
				"要标记已处理请用 pending 或 closed。")
	default:
		return "", catalogUnprocessable("status", fmt.Sprintf("未知的工单状态 %q", string(st)))
	}
}

// adminTicketCategory 把 category_slug 翻成契约枚举。
//
// slug 为 NULL（工单没有分类）时给 `account` —— 契约的 Ticket.category 是 required，
// 没有「未分类」这个值。选 account 而不是随便一个：它是最不会误导客服排序的那一档。
func adminTicketCategory(slug *string, warn func(string)) gen.TicketCategory {
	if slug == nil || *slug == "" {
		warn("工单没有分类（category_id 为 NULL），已按 account 下发（契约的 category 是 required，没有「未分类」值）")
		return gen.Account
	}
	c := gen.TicketCategory(*slug)
	switch c {
	case gen.Subscription, gen.NodeDown, gen.Billing, gen.Account:
	default:
		warn(fmt.Sprintf("工单分类 %q 不在契约枚举内，已原样下发", *slug))
	}
	return c
}

// adminTicketMessageAuthor 把 actor_type 三值映射成契约的两值。
//
// ⚠️ `system` 在契约里没有对应值（author 只有 user / staff）。映射成 staff：
// 系统消息是「本单已自动关闭」这类**解释状态变化**的话，藏起来会让人看到一张
// 状态莫名其妙变了的工单。同 ticket.go 的 ticketAuthor 的取舍。
func adminTicketMessageAuthor(a dbgen.TicketActor) gen.AdminTicketMessageAuthor {
	if a == dbgen.TicketActorUser {
		return gen.AdminTicketMessageAuthorUser
	}
	return gen.AdminTicketMessageAuthorStaff
}

// ---- ListAdminTickets ----

type adminTicketLister interface {
	AdminListTicketsPage(ctx context.Context, arg dbgen.AdminListTicketsPageParams) ([]dbgen.AdminListTicketsPageRow, error)
	AdminCountTicketsFiltered(ctx context.Context, status *dbgen.TicketStatus) (int64, error)
}

// ListAdminTickets 实现 GET /api/v1/admin/tickets。
//
// ⚠️ 与 `ListTicketQueue`（客服工作台）是两条查询，两条都要：
// 工作台只看未结、按优先级排、走部分索引，回答「我现在该处理哪一张」；
// 本端点看**全部状态**、时间序、游标分页，回答「这个月有多少张单、那张单去哪了」。
// 用工作台那条做列表，已解决的工单会从后台**彻底消失** —— 而工单的价值有一半在事后回看。
func (s *Server) ListAdminTickets(ctx context.Context, req gen.ListAdminTicketsRequestObject) (gen.ListAdminTicketsResponseObject, error) {
	if _, ok := middleware.AdminFrom(ctx); !ok {
		return nil, errNoAdminAuth
	}
	data, meta, err := listAdminTickets(ctx, s.db, s.meta(ctx), req.Params, s.catalogWarn(ctx))
	if err != nil {
		return gen.ListAdminTickets500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取工单列表失败", err),
		}, nil
	}
	return gen.ListAdminTickets200JSONResponse{Data: data, Meta: meta}, nil
}

func listAdminTickets(
	ctx context.Context,
	q adminTicketLister,
	meta gen.Meta,
	params gen.ListAdminTicketsParams,
	warn func(string),
) ([]gen.Ticket, gen.Meta, error) {
	want, limitPlusOne := pageLimit(params.Limit)

	arg := dbgen.AdminListTicketsPageParams{PageLimit: limitPlusOne}
	if params.Cursor != nil && *params.Cursor != "" {
		cur, ok := decodePageCursor(*params.Cursor)
		if !ok {
			warn("工单列表游标非法，按第一页处理")
		} else {
			arg.CursorAt = tstz(cur.At)
			arg.CursorID = &cur.ID
		}
	}

	rows, err := q.AdminListTicketsPage(ctx, arg)
	if err != nil {
		return nil, meta, err
	}
	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, gen.Ticket{
			PublicId:    r.PublicID,
			Subject:     r.Subject,
			Category:    adminTicketCategory(r.CategorySlug, warn),
			Status:      ticketStatusView(r.Status, r.LastAgentReplyAt, r.LastUserReplyAt),
			Level:       ptrOf(ticketLevelFromPriority(r.Priority)),
			CreatedAt:   ttime(r.CreatedAt),
			UpdatedAt:   tptr(r.UpdatedAt),
			LastReplyAt: lastReplyAt(r.LastAgentReplyAt, r.LastUserReplyAt),
		})
	}
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		meta.NextCursor = ptrOf(encodePageCursor(last.ID, ttime(last.CreatedAt)))
	}
	if params.Count != nil && *params.Count {
		// WHERE 与列表逐字同形（两边都不过滤 status —— 契约没有这个参数）。
		total, err := q.AdminCountTicketsFiltered(ctx, nil)
		if err != nil {
			return nil, meta, err
		}
		meta.Total = &total
	}
	return out, meta, nil
}

// ---- GetAdminTicket ----

type adminTicketReader interface {
	AdminGetTicketDetail(ctx context.Context, ticketID int64) (dbgen.AdminGetTicketDetailRow, error)
	ListTicketMessagesInternal(ctx context.Context, ticketID int64) ([]dbgen.TicketMessage, error)
}

// GetAdminTicket 实现 GET /api/v1/admin/tickets/{id}（**含内部备注**）。
//
// ⚠️ 管理面按**数字 id** 定位（契约的路径参数是 IdPath），用户面按 `public_id`
// （'BP-7K2M9Q'，对外只暴露短码防枚举）。两套定位方式并存是契约定的，不是疏漏。
func (s *Server) GetAdminTicket(ctx context.Context, req gen.GetAdminTicketRequestObject) (gen.GetAdminTicketResponseObject, error) {
	if _, ok := middleware.AdminFrom(ctx); !ok {
		return nil, errNoAdminAuth
	}
	d, err := getAdminTicket(ctx, s.db, req.Id, s.catalogWarn(ctx))
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok && oe.kind == catalogErrNotFound {
			return gen.GetAdminTicket404JSONResponse{
				ErrNotFoundJSONResponse: s.notFound(ctx, oe.msg),
			}, nil
		}
		return gen.GetAdminTicket500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取工单会话失败", err),
		}, nil
	}
	return gen.GetAdminTicket200JSONResponse{Data: d, Meta: s.meta(ctx)}, nil
}

func getAdminTicket(ctx context.Context, q adminTicketReader, ticketID int64, warn func(string)) (gen.AdminTicketDetail, error) {
	t, err := q.AdminGetTicketDetail(ctx, ticketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.AdminTicketDetail{}, catalogNotFound("工单不存在")
		}
		return gen.AdminTicketDetail{}, err
	}
	msgs, err := q.ListTicketMessagesInternal(ctx, ticketID)
	if err != nil {
		return gen.AdminTicketDetail{}, err
	}

	out := gen.AdminTicketDetail{
		UserId:    t.UserID,
		UserEmail: openapi_types.Email(t.UserEmail),
		Ticket: gen.Ticket{
			PublicId:    t.PublicID,
			Subject:     t.Subject,
			Category:    adminTicketCategory(t.CategorySlug, warn),
			Status:      ticketStatusView(t.Status, t.LastAgentReplyAt, t.LastUserReplyAt),
			Level:       ptrOf(ticketLevelFromPriority(t.Priority)),
			CreatedAt:   ttime(t.CreatedAt),
			UpdatedAt:   tptr(t.UpdatedAt),
			LastReplyAt: lastReplyAt(t.LastAgentReplyAt, t.LastUserReplyAt),
		},
		Messages: make([]gen.AdminTicketMessage, 0, len(msgs)),
	}
	// `context` 是服务端在建单时采集的诊断快照（含 client_reported 子对象）。
	out.Context = catalogJSONObject(t.Context)
	for _, m := range msgs {
		out.Messages = append(out.Messages, gen.AdminTicketMessage{
			Id:         m.ID,
			Author:     adminTicketMessageAuthor(m.ActorType),
			Body:       m.Body,
			CreatedAt:  ttime(m.CreatedAt),
			IsInternal: m.IsInternal,
		})
	}
	return out, nil
}

// ---- UpdateAdminTicket ----

// UpdateAdminTicket 实现 PATCH /api/v1/admin/tickets/{id}。
//
// ⚠️ 两个字段都是可选的，所以走 `coalesce(narg, 当前值)`：只改等级时状态不动，反之亦然。
// 🔴 **一条 UPDATE 同时改两样**，不是两条：写成两条会让「同时改状态和等级」变成
// 两次 UPDATE、两条审计，而中间那一刻的状态在库里真的存在过 ——
// 事后看审计会以为有人改了两次。
func (s *Server) UpdateAdminTicket(ctx context.Context, req gen.UpdateAdminTicketRequestObject) (gen.UpdateAdminTicketResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.UpdateAdminTicket500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWriteTicket(admin.Role) {
		return gen.UpdateAdminTicket403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能修改工单（admin.ticket.write 由角色决定：owner / admin / support）"),
		}, nil
	}
	if req.Body == nil {
		return gen.UpdateAdminTicket422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}

	t, err := updateAdminTicket(ctx, s.catalogAudit(), actor, req.Id, *req.Body, s.catalogWarn(ctx))
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			if oe.kind == catalogErrNotFound {
				return gen.UpdateAdminTicket404JSONResponse{
					ErrNotFoundJSONResponse: s.notFound(ctx, oe.msg),
				}, nil
			}
			return gen.UpdateAdminTicket422JSONResponse{
				ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
			}, nil
		}
		return gen.UpdateAdminTicket500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "修改工单失败", err),
		}, nil
	}
	return gen.UpdateAdminTicket200JSONResponse{Data: t, Meta: s.meta(ctx)}, nil
}

func updateAdminTicket(
	ctx context.Context,
	run catalogAuditRunner,
	actor audit.Actor,
	ticketID int64,
	body gen.AdminTicketPatch,
	warn func(string),
) (gen.Ticket, error) {
	if body.Status == nil && body.Level == nil {
		// 空 PATCH 只会写一条 before == after 的审计。挡住它，别污染 append-only 表。
		return gen.Ticket{}, catalogUnprocessable("", "至少要改一个字段（status 或 level）")
	}
	arg := dbgen.AdminUpdateTicketParams{TicketID: ticketID}
	if body.Status != nil {
		st, err := adminTicketStatusToDB(*body.Status)
		if err != nil {
			return gen.Ticket{}, err
		}
		arg.Status = &st
	}
	if body.Level != nil {
		p, err := ticketPriorityFromLevel(*body.Level)
		if err != nil {
			return gen.Ticket{}, err
		}
		arg.Priority = &p
	}

	var row dbgen.AdminUpdateTicketRow
	runErr := run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		r, err := tx.AdminUpdateTicket(ctx, arg)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, catalogNotFound("工单不存在")
			}
			if isCheckViolation(err) {
				// tickets_resolved_consistency / tickets_closed_consistency（0010）：
				// 状态与两个时间戳绑死。SQL 里用 CASE 一次算对，走到这里说明
				// 那段 CASE 与 CHECK 漂移了 —— 422 + 原文，让人能定位。
				return audit.Entry{}, catalogUnprocessable("status",
					"工单状态与时间戳的一致性约束被拒绝："+err.Error())
			}
			return audit.Entry{}, err
		}
		row = r
		return audit.Entry{
			Action:     "admin.ticket.update",
			TargetType: "ticket",
			TargetID:   strconv.FormatInt(r.ID, 10),
			Before: map[string]any{
				"id": r.ID, "public_id": r.PublicID,
				"status": string(r.BeforeStatus), "priority": string(r.BeforePriority),
				"level":       ticketLevelFromPriority(r.BeforePriority),
				"resolved_at": tsString(r.BeforeResolvedAt), "closed_at": tsString(r.BeforeClosedAt),
			},
			After: map[string]any{
				"id": r.ID, "public_id": r.PublicID,
				"status": string(r.Status), "priority": string(r.Priority),
				"level":       ticketLevelFromPriority(r.Priority),
				"resolved_at": tsString(r.ResolvedAt), "closed_at": tsString(r.ClosedAt),
			},
			// Reason 空：AdminTicketPatch 契约里没有 reason 字段。
		}, nil
	})
	if runErr != nil {
		return gen.Ticket{}, runErr
	}

	return gen.Ticket{
		PublicId: row.PublicID,
		Subject:  row.Subject,
		// ⚠️ `AdminUpdateTicket` 的 RETURNING 里没有 category_slug（它 JOIN 不到分类表）。
		//    契约的 Ticket.category 是 required，只能给 account 并记一条 WARN ——
		//    比再跑一次 AdminGetTicketDetail 便宜，且这个响应的用途是「保存成功」的回显。
		Category:    adminTicketCategory(nil, warn),
		Status:      ticketStatusView(row.Status, row.LastAgentReplyAt, row.LastUserReplyAt),
		Level:       ptrOf(ticketLevelFromPriority(row.Priority)),
		CreatedAt:   ttime(row.CreatedAt),
		UpdatedAt:   tptr(row.UpdatedAt),
		LastReplyAt: lastReplyAt(row.LastAgentReplyAt, row.LastUserReplyAt),
	}, nil
}

// ---- CreateAdminTicketMessage ----

// ticketMessageBodyFormat 是 `ticket_messages.body_format` 的取值。
// 0010 的 CHECK 是 ('markdown','plain','html')，默认 'markdown'。
const ticketMessageBodyFormat = "markdown"

// CreateAdminTicketMessage 实现 POST /api/v1/admin/tickets/{id}/messages。
//
// 🔴 **必须用 `AdminBumpTicketOnAgentMessage`，不能用 `BumpTicketMessageCount`。**
// 后者按 `actor_type` 判 SLA 首次响应，而管理面写的消息 actor_type 恒为 'agent' ——
// **包括 `is_internal = true` 的内部备注**。于是客服给自己写一句「这个先放着，
// 等节点商回复」就会把 `first_response_at` 打上，SLA 的首次响应被判为已达成，
// 而**用户那边一个字都没收到**。后果不是少一个告警，是**告警系统开始撒谎**：
// `ListTicketsBreachingFirstResponse` 的谓词是 `first_response_at IS NULL`，
// 被内部备注填上之后这张单再也不会被判违约 —— SLA 数字会系统性变好看，
// 而变好看的方向恰好是没人会去质疑的那个方向。
//
// 🔴 两条写入必须**同一个事务**。这不是「计数不准」那一类可以将就的冗余：
// SLA 时钟错了会让告警系统撒谎。审计写在同一事务里，所以三者一起成功或一起回滚。
func (s *Server) CreateAdminTicketMessage(ctx context.Context, req gen.CreateAdminTicketMessageRequestObject) (gen.CreateAdminTicketMessageResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.CreateAdminTicketMessage500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWriteTicket(admin.Role) {
		return gen.CreateAdminTicketMessage403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能回复工单（admin.ticket.write 由角色决定：owner / admin / support）"),
		}, nil
	}
	if req.Body == nil {
		return gen.CreateAdminTicketMessage422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}

	m, err := createAdminTicketMessage(ctx, s.catalogAudit(), actor, admin.AdminID, req.Id, *req.Body)
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			if oe.kind == catalogErrNotFound {
				return gen.CreateAdminTicketMessage404JSONResponse{
					ErrNotFoundJSONResponse: s.notFound(ctx, oe.msg),
				}, nil
			}
			return gen.CreateAdminTicketMessage422JSONResponse{
				ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
			}, nil
		}
		return gen.CreateAdminTicketMessage500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "回复工单失败", err),
		}, nil
	}
	return gen.CreateAdminTicketMessage201JSONResponse{Data: m, Meta: s.meta(ctx)}, nil
}

func createAdminTicketMessage(
	ctx context.Context,
	run catalogAuditRunner,
	actor audit.Actor,
	adminID int64,
	ticketID int64,
	body gen.AdminCreateTicketMessageRequest,
) (gen.AdminTicketMessage, error) {
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		return gen.AdminTicketMessage{}, catalogUnprocessable("message", "回复内容不能为空")
	}
	if n := len([]rune(msg)); n > ticketMessageMaxRunes {
		return gen.AdminTicketMessage{}, catalogUnprocessable("message",
			fmt.Sprintf("回复最多 %d 个字符（当前 %d）", ticketMessageMaxRunes, n))
	}

	var created dbgen.TicketMessage
	runErr := run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		// 🔴 **先 bump 再写消息**，顺序是刻意的：bump 的 UPDATE 影响 0 行
		//    就是「工单不存在」的干净判据，于是 404 不必依赖 INSERT 的外键错误码
		//    （23503 的报文里没有可靠的字段能区分是哪个外键）。
		//    两条同事务，所以「先加了计数再插消息」在外部不可观察。
		bumped, err := tx.AdminBumpTicketOnAgentMessage(ctx, dbgen.AdminBumpTicketOnAgentMessageParams{
			TicketID:   ticketID,
			IsInternal: body.IsInternal,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, catalogNotFound("工单不存在")
			}
			return audit.Entry{}, err
		}

		row, err := tx.CreateTicketMessage(ctx, dbgen.CreateTicketMessageParams{
			TicketID: ticketID,
			// actor_type 恒为 agent：这个端点只有管理员能进。
			ActorType:   dbgen.TicketActorAgent,
			UserID:      nil,
			AdminUserID: &adminID,
			Body:        msg,
			BodyFormat:  ticketMessageBodyFormat,
			IsInternal:  body.IsInternal,
			// channel = 'admin'：这条消息**来自后台**，不是用户从 web 发的。
			// 用 'web' 会让「这条回复是从哪儿来的」在事后不可分辨，
			// 而 ticket_channel 里存在 'admin' 这个值正是为了它。
			Channel:    dbgen.TicketChannelAdmin,
			ExternalID: nil,
		})
		if err != nil {
			return audit.Entry{}, err
		}
		created = row

		return audit.Entry{
			Action:     "admin.ticket.message",
			TargetType: "ticket",
			TargetID:   strconv.FormatInt(ticketID, 10),
			Before: map[string]any{
				"message_count":       bumped.BeforeMessageCount,
				"first_response_at":   tsString(bumped.BeforeFirstResponseAt),
				"last_agent_reply_at": tsString(bumped.BeforeLastAgentReplyAt),
			},
			After: map[string]any{
				"message_id":    row.ID,
				"message_count": bumped.MessageCount,
				// 🔴 `is_internal` 必须进审计：一条永远不会到达用户的内部备注，
				//    与一条真的发给用户的回复，在事后是两件完全不同的事。
				"is_internal":         row.IsInternal,
				"first_response_at":   tsString(bumped.FirstResponseAt),
				"last_agent_reply_at": tsString(bumped.LastAgentReplyAt),
				"body_chars":          len([]rune(msg)),
			},
			// Reason 空：AdminCreateTicketMessageRequest 契约里没有 reason 字段。
		}, nil
	})
	if runErr != nil {
		return gen.AdminTicketMessage{}, runErr
	}

	return gen.AdminTicketMessage{
		Id:         created.ID,
		Author:     adminTicketMessageAuthor(created.ActorType),
		Body:       created.Body,
		CreatedAt:  ttime(created.CreatedAt),
		IsInternal: created.IsInternal,
	}, nil
}

// ============================================================
// 审计日志（模块 10，**只有 GET**）
// ============================================================
//
// 🔴 **本节只有读。没有 UPDATE，没有 DELETE，将来也不要加。**
// api-contract §6.1 原话：「一个能被清理的审计日志等于没有审计日志」。
// 表本身由 DB 层 REVOKE UPDATE/DELETE/TRUNCATE 兜底（0011 的注释），
// 但那道机制防的是应用写错，不防「有人往这个文件里加了一条 DELETE 然后 CI 过了」。
//
// ⚠️ **三处契约与实表的偏差**（audit.go 的包注释记了其一，这里补全）：
//  1. **`request_id` 没有落库。** openapi 的 AuditLogEntry 把它列进 required，
//     而 `audit_logs` 根本没有这一列。它是「把一条审计接回访问日志 / trace」的唯一钥匙。
//     handler 只能填空串 —— 而空串在前端就是一个点不动的链接。
//     补它需要一支迁移加列 + audit.Actor 加字段（audit.go 已挂 TODO(P2)）。
//  2. **`admin_id` 可空、契约必填。** 列是 ON DELETE SET NULL。软停用之下它永不为 NULL，
//     但历史上若真删过管理员，那些行会是 NULL → 只能填 0。
//     更残酷的一层：能指认到人的 `admin_email_snapshot` 在 AuditLogEntry 上
//     **没有字段可放** —— 那条证据存在于库里、但通过 API 看不到。
//     所以它进服务端日志（见 adminAuditEntryView 的调用点）。
//  3. **`action` 的形态带 D 编号**（'D6.order.mark_paid'），而 openapi 的举例不带。
//     以库为准 —— 带编号的那份能直接按 `action LIKE 'D6.%'` 筛出一整类危险操作。

type adminAuditLister interface {
	ListAdminAuditLogPage(ctx context.Context, arg dbgen.ListAdminAuditLogPageParams) ([]dbgen.AuditLog, error)
	CountAdminAuditLog(ctx context.Context, arg dbgen.CountAdminAuditLogParams) (int64, error)
}

// ListAdminAuditLog 实现 GET /api/v1/admin/audit。
func (s *Server) ListAdminAuditLog(ctx context.Context, req gen.ListAdminAuditLogRequestObject) (gen.ListAdminAuditLogResponseObject, error) {
	if _, ok := middleware.AdminFrom(ctx); !ok {
		return nil, errNoAdminAuth
	}
	data, meta, err := listAdminAuditLog(ctx, s.db, s.meta(ctx), req.Params, s.catalogWarn(ctx))
	if err != nil {
		return gen.ListAdminAuditLog500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取审计日志失败", err),
		}, nil
	}
	return gen.ListAdminAuditLog200JSONResponse{Data: data, Meta: meta}, nil
}

// auditActionFilter 把 `?action=` 翻成 SQL 的 ILIKE 模式。
//
// 🔴 **必须是包含匹配，不能是等值。** 库里的 action 带 D 编号前缀
// （`D6.order.mark_paid`），而契约的参数说明举的例子是 `order.mark_paid`（不带）。
// 等值匹配的现象是**一条都查不到，且不报错** —— 后台显示「审计日志是空的」，
// 而那正是有人会拿来证明「没人动过」的那块屏幕。
//
// 🔴 转义在拼 `%…%` **之前**：一个未转义的 `%` 会让过滤器匹配全部，
// 一个 `_` 会安静地多返回一批不相干的记录。两者都不报错。
func auditActionFilter(action *string) *string {
	if action == nil {
		return nil
	}
	a := strings.TrimSpace(*action)
	if a == "" {
		return nil
	}
	pattern := "%" + catalogEscapeLike(a) + "%"
	return &pattern
}

func listAdminAuditLog(
	ctx context.Context,
	q adminAuditLister,
	meta gen.Meta,
	params gen.ListAdminAuditLogParams,
	warn func(string),
) ([]gen.AuditLogEntry, gen.Meta, error) {
	want, limitPlusOne := pageLimit(params.Limit)

	actionLike := auditActionFilter(params.Action)
	var targetType *string
	if params.TargetType != nil {
		if t := strings.TrimSpace(*params.TargetType); t != "" {
			targetType = &t
		}
	}

	arg := dbgen.ListAdminAuditLogPageParams{
		PageLimit:  limitPlusOne,
		ActionLike: actionLike,
		TargetType: targetType,
	}
	if params.Cursor != nil && *params.Cursor != "" {
		cur, ok := decodePageCursor(*params.Cursor)
		if !ok {
			warn("审计日志游标非法，按第一页处理")
		} else {
			// 🔴 两列游标 `(created_at, id)`：审计是最大的 append-only 表，
			//    而唯一能吃到顺序的索引是 `audit_logs_created_idx (created_at DESC)`。
			//    按 id 排序会让每一次翻页都变成一次全表排序。
			//    id 破平手是必须的：同一个事务里写的多条审计时间戳可以完全相同
			//    （now() 在一个事务里是常量）。
			arg.CursorAt = tstz(cur.At)
			arg.CursorID = &cur.ID
		}
	}

	rows, err := q.ListAdminAuditLogPage(ctx, arg)
	if err != nil {
		return nil, meta, err
	}
	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.AuditLogEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminAuditEntryView(r, warn))
	}
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		meta.NextCursor = ptrOf(encodePageCursor(last.ID, ttime(last.CreatedAt)))
	}
	if params.Count != nil && *params.Count {
		// ⚠️ WHERE 与列表逐字同形。
		// ⚠️ **代价登记**：审计表只增不删，这个 COUNT 会随时间线性变慢。
		//    撤回条件：p95 > 500 ms 时后台改成「不显示总数、只显示有没有下一页」——
		//    审计页要的是能翻到底，不是知道一共几条。
		total, err := q.CountAdminAuditLog(ctx, dbgen.CountAdminAuditLogParams{
			ActionLike: actionLike,
			TargetType: targetType,
		})
		if err != nil {
			return nil, meta, err
		}
		meta.Total = &total
	}
	return out, meta, nil
}

func adminAuditEntryView(r dbgen.AuditLog, warn func(string)) gen.AuditLogEntry {
	e := gen.AuditLogEntry{
		Id:         r.ID,
		Action:     r.Action,
		TargetType: r.TargetType,
		TargetId:   r.TargetID,
		CreatedAt:  ttime(r.CreatedAt),
		Ip:         r.RequestIp.String(),
		Reason:     r.Reason,
		UserAgent:  r.UserAgent,
		Before:     catalogJSONObject(r.BeforeValue),
		After:      catalogJSONObject(r.AfterValue),
		// 🔴 `request_id` 在契约里是 required，而 audit_logs 没有这一列。
		//    空串是唯一诚实的值 —— 编一个（比如复用当前请求的 id）会让人以为
		//    这条审计能接回那次操作的访问日志，而它接的是**读日志这个动作**。
		RequestId: "",
	}
	if r.AdminUserID != nil {
		e.AdminId = *r.AdminUserID
	} else {
		// admin_user_id 是 ON DELETE SET NULL。软停用之下永不为 NULL，
		// 所以出现 NULL 说明历史上真删过管理员 —— 那条记录从此指认不到人。
		warn(fmt.Sprintf("审计记录 %d 的 admin_user_id 为 NULL（管理员被硬删过），"+
			"能指认到人的 admin_email_snapshot=%q 在契约的 AuditLogEntry 上没有字段可放",
			r.ID, r.AdminEmailSnapshot))
	}
	return e
}

// ============================================================
// 系统配置（模块 16）—— GetAdminSettings · UpdateAdminSettings（D13）
// ============================================================
//
// 🔴 **凭据不在这张表里，将来也不许放进来**（AGENTS.md §4：一律走环境变量 / Secret Manager）。
// `ListAdminSettings` 不做任何过滤，所以**任何**写进 settings 的东西都会原样出现在
// 管理面响应体里 —— 这条规则不是靠过滤保证的，是靠**不写进去**保证的。
// 往这张表里塞一个 api_key 的那一刻，它同时出现在管理面响应、浏览器缓存与前端 devtools 里。
//
// ⚠️ 这里还有一层结构性的保护：`UpdateAdminSettingsValues` 是**纯 UPDATE 不是 UPSERT**，
//    所以通过 API **建不出新键** —— 一个新的凭据键必须走迁移，而迁移是要被 review 的。

type adminSettingsReader interface {
	ListAdminSettings(ctx context.Context) ([]dbgen.Setting, error)
}

// GetAdminSettings 实现 GET /api/v1/admin/settings。
func (s *Server) GetAdminSettings(ctx context.Context, _ gen.GetAdminSettingsRequestObject) (gen.GetAdminSettingsResponseObject, error) {
	if _, ok := middleware.AdminFrom(ctx); !ok {
		return nil, errNoAdminAuth
	}
	m, err := readAdminSettings(ctx, s.db, s.catalogWarn(ctx))
	if err != nil {
		return gen.GetAdminSettings500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取系统配置失败", err),
		}, nil
	}
	return gen.GetAdminSettings200JSONResponse{Data: m, Meta: s.meta(ctx)}, nil
}

func readAdminSettings(ctx context.Context, q adminSettingsReader, warn func(string)) (gen.SettingsMap, error) {
	rows, err := q.ListAdminSettings(ctx)
	if err != nil {
		return nil, err
	}
	// 非 nil 的 map：SettingsMap 是 `map[string]interface{}`，nil 序列化成 null，
	// 而契约里 data 是一个对象。前端对 null 与 {} 的处理分支不同。
	out := make(gen.SettingsMap, len(rows))
	for _, r := range rows {
		var v interface{}
		if err := json.Unmarshal(r.Value, &v); err != nil {
			// jsonb 列保证是合法 JSON，走到这里说明取回来的字节被截断了之类。
			// **不丢弃这个键** —— 少一个键会让前端以为这个配置项不存在，
			// 于是有人去「新建」它，而 UPDATE-only 的写侧会把那次新建 422 掉，
			// 现象是「这个配置怎么也保存不上」。原样交出去当字符串更好排查。
			warn(fmt.Sprintf("配置项 %q 的值不是合法 JSON，已按字符串下发", r.Key))
			v = string(r.Value)
		}
		out[r.Key] = v
	}
	return out, nil
}

// ---- UpdateAdminSettings（D13：L2 必填原因）----

// UpdateAdminSettings 实现 PATCH /api/v1/admin/settings。
//
// 🔴 **UPDATE 不是 UPSERT：只改已存在的键，不认识的键一条都不写。**
// `settings.key` 是自由文本主键，没有任何白名单表。写成 `INSERT … ON CONFLICT DO UPDATE`
// 的话，一次手滑的 `{"expire_remid": …}` 会**静默地新建**一个永远不会被读到的键，
// 而真正想改的那个原封不动 —— 页面显示「已保存」，行为没有任何变化。
// 改成纯 UPDATE 之后，那次手滑影响 0 行，靠 **`len(rows) == 传入键数`** 这条断言
// 翻成 422 并列出哪些键不认识。
//
// D13 还要求「展示 diff」（page-inventory §4.4）：审计的 before/after 是**逐键**的
// 新旧值，不是「改了 5 个键」这种摘要 —— 配置回滚时唯一能用的东西就是这份逐键旧值。
func (s *Server) UpdateAdminSettings(ctx context.Context, req gen.UpdateAdminSettingsRequestObject) (gen.UpdateAdminSettingsResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.UpdateAdminSettings500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.UpdateAdminSettings403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能修改系统配置（admin.settings.write 由角色决定：owner / admin）"),
		}, nil
	}
	if req.Body == nil {
		return gen.UpdateAdminSettings422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}

	if err := updateAdminSettings(ctx, s.catalogAudit(), actor, admin.AdminID, *req.Body); err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			return gen.UpdateAdminSettings422JSONResponse{
				ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
			}, nil
		}
		return gen.UpdateAdminSettings500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "修改系统配置失败", err),
		}, nil
	}

	// 事务提交之后**重新读一遍整张表**再返回。
	// 只回改过的那几个键会让 GET 与 PATCH 的 `data` 是同一个 schema 却含义不同
	// （一个是全量、一个是子集），而前端多半会拿它整体替换本地状态 ——
	// 那样一来没改的配置项在界面上会集体消失。多一次读换掉这个坑是值的。
	m, err := readAdminSettings(ctx, s.db, s.catalogWarn(ctx))
	if err != nil {
		return gen.UpdateAdminSettings500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "配置已保存，但回读失败", err),
		}, nil
	}
	return gen.UpdateAdminSettings200JSONResponse{Data: m, Meta: s.meta(ctx)}, nil
}

// settingsMaxKeys 是一次 PATCH 能改的键数上限。
//
// 契约没给上限，而两个 text[] 参数会被整体发进一条语句。取 200 是**设定值**：
// settings 全表也就几十行，一次改 200 个键已经不是「改配置」而是「重置系统」。
const settingsMaxKeys = 200

func updateAdminSettings(
	ctx context.Context,
	run catalogAuditRunner,
	actor audit.Actor,
	adminID int64,
	body gen.SettingsPatchRequest,
) error {
	if err := catalogCheckReason(body.Reason); err != nil {
		return err
	}
	if len(body.Values) == 0 {
		return catalogUnprocessable("values", "没有要修改的配置项")
	}
	if len(body.Values) > settingsMaxKeys {
		return catalogUnprocessable("values",
			fmt.Sprintf("一次最多修改 %d 个配置项（当前 %d）", settingsMaxKeys, len(body.Values)))
	}

	// 🔴 **键必须排序。** 两个数组按下标配对，而 Go 的 map 遍历顺序是随机的：
	//    不排序的话同一个请求两次执行会产生不同的参数顺序，于是
	//    ① 审计快照里的键顺序每次都不一样（diff 没法看），
	//    ② 出问题时「同样的请求为什么这次成功那次失败」无从复现。
	keys := make([]string, 0, len(body.Values))
	for k := range body.Values {
		if strings.TrimSpace(k) == "" {
			return catalogUnprocessable("values", "配置键不能为空")
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	values := make([]string, 0, len(keys))
	for _, k := range keys {
		// 值以 **text** 传入，SQL 里 `::jsonb` 转换。这样非法 JSON 在那一步就抛 22P02
		// 整条回滚，而不是变成一个存进库里、下次启动时才炸的坏值。
		// 这里的值来自已经解析过的 JSON，所以 Marshal 不会失败；真失败就 422。
		b, err := json.Marshal(body.Values[k])
		if err != nil {
			return catalogUnprocessable("values", fmt.Sprintf("配置项 %q 的值无法序列化成 JSON", k))
		}
		values = append(values, string(b))
	}

	return run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		rows, err := tx.UpdateAdminSettingsValues(ctx, dbgen.UpdateAdminSettingsValuesParams{
			SettingKeys:   keys,
			SettingValues: values,
			AdminUserID:   adminID,
		})
		if err != nil {
			return audit.Entry{}, err
		}
		// 🔴 **`len(rows) == len(keys)` 这条断言是本端点的核心。**
		//    少了 = 有键不存在（纯 UPDATE 影响 0 行）。不断言的话那次手滑
		//    会得到一个 200「已保存」，而那个键从来没有被写过。
		if len(rows) != len(keys) {
			written := make(map[string]bool, len(rows))
			for _, r := range rows {
				written[r.Key] = true
			}
			unknown := make([]string, 0, len(keys)-len(rows))
			for _, k := range keys {
				if !written[k] {
					unknown = append(unknown, k)
				}
			}
			return audit.Entry{}, catalogUnprocessable("values", fmt.Sprintf(
				"这些配置项不存在：%s。"+
					"新增配置项必须走数据库迁移（settings.description 是 NOT NULL —— "+
					"一个配置项的说明本来就该跟着它的引入一起写）。", strings.Join(unknown, "、")))
		}

		// D13 要求「展示 diff」：before / after 都是**逐键**的完整值。
		before := make(map[string]any, len(rows))
		after := make(map[string]any, len(rows))
		changed := make([]string, 0, len(rows))
		for _, r := range rows {
			before[r.Key] = catalogRawJSON(r.BeforeValue)
			after[r.Key] = catalogRawJSON(r.AfterValue)
			changed = append(changed, r.Key)
		}
		return audit.Entry{
			Action:     "D13.settings.update",
			TargetType: "settings",
			// target_id 用「被改的键」本身：审计的检索维度之一是「对象」
			// （§6.3：操作者 / 对象 / 时间），而配置的对象就是键。
			// 键多时截断，完整清单在 after 快照里。
			TargetID: truncateForTargetID(strings.Join(changed, ",")),
			Before:   before,
			After:    after,
			Reason:   strings.TrimSpace(body.Reason),
		}, nil
	})
}

// catalogRawJSON 把 jsonb 的原始字节包成审计快照里的一个值。
//
// 用 json.RawMessage 而不是先反序列化再序列化：配置值可能是任意 JSON，
// 往返一趟会丢掉键序与数字的原始写法（1.0 变成 1），而 D13 的审计是**回滚配置的唯一依据** ——
// 回滚时要写回去的应当是当时那个字节串，不是它的一个等价物。
//
// ⚠️ 空字节必须换成 `null` 字面量：`json.RawMessage` 为长度 0 且非 nil 时，
// json.Marshal 会报「unexpected end of JSON input」，而那会让审计写失败 →
// **一次合法的配置修改被整体回滚**。settings.value 是 NOT NULL jsonb，
// 正常不会走到这里；写这一行是因为它的失败模式比它的概率重要。
func catalogRawJSON(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(raw)
}

// targetIDMaxBytes 是 audit_logs.target_id 的实用上限。
//
// 列本身是无上限 text，但 `audit_logs_target_idx (target_type, target_id, created_at DESC)`
// 是一条 B-tree 索引 —— PostgreSQL 的 B-tree 单个条目上限约 2704 字节，
// 超过会**在 INSERT 时报错**（index row size … exceeds maximum）。
// 那意味着一次改了 300 个键的配置操作会因为审计写不进去而整个回滚，
// 而报错信息指向索引、完全看不出与配置有什么关系。
const targetIDMaxBytes = 512

func truncateForTargetID(s string) string {
	if len(s) <= targetIDMaxBytes {
		return s
	}
	// 退到 rune 边界：PostgreSQL 的 text 列**拒收**非法 UTF-8（不是静默截断），
	// 切在多字节字符中间会让整条审计写失败 → 业务操作一起回滚。
	cut := targetIDMaxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// ============================================================
// 流量统计（模块 7）—— GetAdminStats · ExportAdminStats（D14）
// ============================================================
//
// 🔴 **口径：`stat_user_server.stat_date` 是按 `Asia/Shanghai` 切的天**
// （0009 的列注释；写入侧 `BulkUpsertStatUserServer` 用的就是
// `(now() AT TIME ZONE 'Asia/Shanghai')::date`）。入参与出参都必须做同样的换算 ——
// 直接拿 UTC 日期去比会**整体错开一天**，而错开一天的日报看起来完全正常，
// 只是每天的数字都是隔壁那天的。换算只有 `catalogStatDate` / `catalogRecordAt` 两个函数，
// 三种 scope 共用（各写一遍是三份会漂移的时区代码）。
//
// ⚠️ **openapi 给 /admin/stats 的参数里没有任何分页**（只有 scope/from/to），
//    而 scope=user 的结果是「天数 × 用户数」行 —— 一年 × 1000 人 = 36.5 万行。
//    所以 handler 自己钉一个上限，并在命中上限时**明确告知截断**（meta.has_more）。
//    静默截断的报表会被当成完整数据去做决策。

const (
	// statsPageLimit 是 scope=user / server 的行数上限。
	//
	// 5000 是**设定值**：一年 × 13 个节点 ≈ 4700 行（server 维度一年内够用），
	// user 维度则会在几十个用户 × 几个月就触顶 —— 那时 has_more 会是 true，
	// 前端必须显示「结果已截断」。这是端点没有分页参数的直接后果，缺口已登记。
	statsPageLimit = 5000

	// statsDefaultWindowDays 是 from/to 都没传时的默认窗口。
	statsDefaultWindowDays = 30

	// statsMaxWindowDays 是 from/to 都传了时允许的最大跨度。
	//
	// 没有上限的话一次 `from=1970` 会让 scope=user 扫全表再截断到 5000 行 ——
	// 代价付了，结果还是截断的。366 天覆盖「看去年同期」这个真实需求。
	statsMaxWindowDays = 366

	// statsExportWindowDays 是 **exportAdminStats 自钉的窗口**。
	//
	// 🔴 `/admin/stats/export` 的参数**只有 scope，没有 from/to** ——
	//    无界导出 = 一次请求带走全站历史，D14 那层权限位就白设了。
	//    90 天是**设定值**：足够做季度成本核算，又不至于一次交出全部历史。
	statsExportWindowDays = 90

	// 导出限流（契约给 exportAdminStats 声明了 429）。
	// per admin 的 5/h 是**设定值**：真实的导出需求是「一天一两次」，
	// 而这个端点是数据外泄面。
	bucketStatsExportAdmin = "admin_stats_export_1h"
	statsExportPerHour     = 5
)

// statsWindow 是解析并夹紧之后的时间窗。
type statsWindow struct {
	from time.Time
	to   time.Time
}

// parseStatsWindow 解析 from/to 并夹紧。
//
// ⚠️ 只传 from 或只传 to 都要能工作：另一端按默认窗口补齐。
// 契约没说它们是必填，而「只传 from 就报错」会让一个合法请求得到 422。
func parseStatsWindow(from, to *time.Time, now time.Time) (statsWindow, error) {
	w := statsWindow{to: now}
	if to != nil {
		w.to = *to
	}
	if from != nil {
		w.from = *from
	} else {
		w.from = w.to.AddDate(0, 0, -statsDefaultWindowDays)
	}
	if !w.to.After(w.from) {
		return w, catalogUnprocessable("to", "结束时间必须晚于开始时间")
	}
	if w.to.Sub(w.from) > time.Duration(statsMaxWindowDays)*24*time.Hour {
		return w, catalogUnprocessable("from",
			fmt.Sprintf("时间跨度最多 %d 天（这个端点没有分页参数，更长的窗口只会被截断）", statsMaxWindowDays))
	}
	return w, nil
}

type adminStatsQuerier interface {
	GetGlobalDailyTraffic(ctx context.Context, arg dbgen.GetGlobalDailyTrafficParams) ([]dbgen.GetGlobalDailyTrafficRow, error)
	ListAdminStatByUser(ctx context.Context, arg dbgen.ListAdminStatByUserParams) ([]dbgen.ListAdminStatByUserRow, error)
	ListAdminStatByServer(ctx context.Context, arg dbgen.ListAdminStatByServerParams) ([]dbgen.ListAdminStatByServerRow, error)
}

// statScope 归一化 scope 参数（两个端点各有一个枚举类型，值相同）。
func statScope(s *string) string {
	if s == nil || *s == "" {
		return "global" // 契约的 default
	}
	return *s
}

// loadAdminStats 按 scope 取统计。第二个返回值 = 是否命中行数上限（被截断）。
func loadAdminStats(ctx context.Context, q adminStatsQuerier, scope string, w statsWindow) ([]gen.StatBucket, bool, error) {
	switch scope {
	case "global":
		rows, err := q.GetGlobalDailyTraffic(ctx, dbgen.GetGlobalDailyTrafficParams{
			StatDate:   catalogStatDate(w.from),
			StatDate_2: catalogStatDate(w.to),
		})
		if err != nil {
			return nil, false, err
		}
		out := make([]gen.StatBucket, 0, len(rows))
		for _, r := range rows {
			out = append(out, gen.StatBucket{
				RecordAt:      catalogRecordAt(r.StatDate),
				UploadBytes:   r.U,
				DownloadBytes: r.D,
			})
		}
		// global 维度每天一行，最多 366 行，不会触顶。
		return out, false, nil

	case "user":
		rows, err := q.ListAdminStatByUser(ctx, dbgen.ListAdminStatByUserParams{
			FromAt:    tstz(w.from),
			ToAt:      tstz(w.to),
			PageLimit: statsPageLimit,
		})
		if err != nil {
			return nil, false, err
		}
		out := make([]gen.StatBucket, 0, len(rows))
		for _, r := range rows {
			row := r
			out = append(out, gen.StatBucket{
				RecordAt:      catalogRecordAt(row.StatDate),
				UserId:        &row.UserID,
				UploadBytes:   row.UploadBytes,
				DownloadBytes: row.DownloadBytes,
			})
		}
		return out, len(rows) >= statsPageLimit, nil

	case "server":
		rows, err := q.ListAdminStatByServer(ctx, dbgen.ListAdminStatByServerParams{
			FromAt:    tstz(w.from),
			ToAt:      tstz(w.to),
			PageLimit: statsPageLimit,
		})
		if err != nil {
			return nil, false, err
		}
		out := make([]gen.StatBucket, 0, len(rows))
		for _, r := range rows {
			row := r
			out = append(out, gen.StatBucket{
				RecordAt:      catalogRecordAt(row.StatDate),
				ServerId:      &row.ServerID,
				UploadBytes:   row.UploadBytes,
				DownloadBytes: row.DownloadBytes,
			})
		}
		return out, len(rows) >= statsPageLimit, nil

	default:
		return nil, false, catalogUnprocessable("scope", fmt.Sprintf("未知的聚合维度 %q", scope))
	}
}

// GetAdminStats 实现 GET /api/v1/admin/stats。
func (s *Server) GetAdminStats(ctx context.Context, req gen.GetAdminStatsRequestObject) (gen.GetAdminStatsResponseObject, error) {
	if _, ok := middleware.AdminFrom(ctx); !ok {
		return nil, errNoAdminAuth
	}
	var scope *string
	if req.Params.Scope != nil {
		scope = ptrOf(string(*req.Params.Scope))
	}
	w, err := parseStatsWindow(req.Params.From, req.Params.To, time.Now())
	if err != nil {
		// ⚠️ 契约给 getAdminStats 只声明了 403/500，**没有 422**。
		//    参数不合法只能落到 500 —— 用 VALIDATION_FAILED 这个 code 让前端仍能分辨，
		//    message 是人话。缺口已登记（补法：给这个端点加 422 响应）。
		oe, _ := asCatalogOpError(err)
		return gen.GetAdminStats500JSONResponse{
			ErrInternalJSONResponse: s.statsBadParam(ctx, oe.msg),
		}, nil
	}

	data, truncated, err := loadAdminStats(ctx, s.db, statScope(scope), w)
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			return gen.GetAdminStats500JSONResponse{
				ErrInternalJSONResponse: s.statsBadParam(ctx, oe.msg),
			}, nil
		}
		return gen.GetAdminStats500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取流量统计失败", err),
		}, nil
	}

	meta := s.meta(ctx)
	// 🔴 命中上限时 has_more = true。这是这个端点唯一能表达「结果被截断」的字段
	//    （契约没有分页参数，所以也没有 next_cursor 可以给）。
	//    不给这个信号的话，一份被截断到 5000 行的报表看起来和完整报表一模一样。
	meta.HasMore = &truncated
	if truncated {
		s.logger.WarnContext(ctx, "bp_admin_stats_truncated 统计结果命中行数上限，已截断",
			"scope", statScope(scope), "limit", statsPageLimit,
			"request_id", middleware.RequestIDFrom(ctx))
	}
	return gen.GetAdminStats200JSONResponse{Data: data, Meta: meta}, nil
}

// statsBadParam 构造「参数不合法」的响应体。
//
// 🔴 状态码是 500 而正确答案是 422 —— 契约给 getAdminStats 只声明了 403/500。
// code 写 VALIDATION_FAILED 让前端仍能按 code 分支，且**不打 ERROR 日志**
// （这不是我们的故障）。缺口已登记。
func (s *Server) statsBadParam(ctx context.Context, msg string) gen.ErrInternalJSONResponse {
	s.logger.WarnContext(ctx, "统计参数不合法（契约缺 422，只能用 500 承载）",
		"reason", msg, "request_id", middleware.RequestIDFrom(ctx))
	return gen.ErrInternalJSONResponse{
		Body:    s.envelope(ctx, gen.VALIDATIONFAILED, msg),
		Headers: gen.ErrInternalResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}
}

// ---- ExportAdminStats（D14：独立权限位 + 审计）----

// statsCSVHeader 是导出的表头。三种 scope 共用一张表 ——
// 列固定意味着下游脚本不必按 scope 分支解析。
var statsCSVHeader = []string{"record_at", "scope", "user_id", "server_id", "upload_bytes", "download_bytes"}

// ExportAdminStats 实现 GET /api/v1/admin/stats/export。
//
// 🔴 **L4：`perm_export_csv`（契约的 `admin.user.export`）是这个端点唯一真正的闸门。**
// 它是 `admin_users` 上真实存在的一列，默认 false，**即使团队只有一个人也不预授**。
// 这一层不是「角色推出来的」，与本文件其余的写操作不同。
//
// 🔴 **审计写失败 → 导出也失败（500）。** 这是纯读操作，没有业务事务可搭，
// 所以走 `audit.Write`；但 D14 的全部要求就是那条审计（「谁、何时、哪些字段、多少行」），
// 「数据给了、记录没留」正是它要防的东西。先写审计再吐 CSV。
//
// ⚠️ **窗口由服务端自钉 90 天**：端点只有 scope 没有 from/to，无界导出会让 L4 白设。
func (s *Server) ExportAdminStats(ctx context.Context, req gen.ExportAdminStatsRequestObject) (gen.ExportAdminStatsResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.ExportAdminStats500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	// L4：真的权限位。
	if !admin.Can(middleware.PermExportCSV) {
		s.logger.WarnContext(ctx, "导出统计被拒：缺 perm_export_csv",
			"admin_id", admin.AdminID, "request_id", middleware.RequestIDFrom(ctx))
		return gen.ExportAdminStats403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"导出需要 admin.user.export 权限位（默认不授予，需单独开）"),
		}, nil
	}

	// 频率上限：数据外泄面上的一道额外闸。per admin 而不是 per IP ——
	// 权限位已经把范围收到几个人，IP 维度只会误伤同一个办公网出口。
	if retry, limited := s.checkRateRules(ctx, rateRule{
		bucket:  bucketStatsExportAdmin,
		subject: strconv.FormatInt(admin.AdminID, 10),
		limit:   statsExportPerHour,
		window:  time.Hour,
	}); limited {
		return gen.ExportAdminStats429JSONResponse{
			ErrRateLimitedJSONResponse: s.rateLimited(ctx,
				fmt.Sprintf("导出过于频繁，每小时最多 %d 次", statsExportPerHour), retry),
		}, nil
	}

	scope := "global"
	if req.Params.Scope != nil {
		scope = string(*req.Params.Scope)
	}
	now := time.Now()
	w := statsWindow{from: now.AddDate(0, 0, -statsExportWindowDays), to: now}

	rows, truncated, err := loadAdminStats(ctx, s.db, scope, w)
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			return gen.ExportAdminStats500JSONResponse{
				ErrInternalJSONResponse: s.statsBadParam(ctx, oe.msg),
			}, nil
		}
		return gen.ExportAdminStats500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取流量统计失败", err),
		}, nil
	}

	csvBytes, err := statsCSV(rows, scope)
	if err != nil {
		return gen.ExportAdminStats500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "生成 CSV 失败", err),
		}, nil
	}

	// D14 的审计：谁（actor）、何时（created_at）、哪些字段（columns）、多少行（rows）。
	// ⚠️ 走 audit.Write 而不是 InTx —— 纯读没有业务事务可搭（audit.go 明写这是保留
	//    Write 导出的理由）。
	// 🔴 写失败 → 500，**不吐数据**。
	if err := audit.Write(ctx, s.db.Pool, actor, audit.Entry{
		Action:     "D14.stats.export",
		TargetType: "stats",
		TargetID:   scope,
		Before:     nil,
		After: map[string]any{
			"scope":       scope,
			"from":        w.from.UTC().Format(time.RFC3339),
			"to":          w.to.UTC().Format(time.RFC3339),
			"window_days": statsExportWindowDays,
			"row_count":   len(rows),
			"truncated":   truncated,
			"columns":     statsCSVHeader,
			"bytes":       len(csvBytes),
		},
		// Reason 空：exportAdminStats 是 GET，契约上没有任何地方能带 reason。
		// D14 在 §6.2 的 L2 行里也没有，所以这不是缺口，是它本来就不要求。
	}); err != nil {
		return gen.ExportAdminStats500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx,
				"导出审计写入失败，已拒绝下发数据（D14 的要求就是这条审计）", err),
		}, nil
	}

	if truncated {
		s.logger.WarnContext(ctx, "bp_admin_stats_truncated 导出命中行数上限，CSV 是被截断的",
			"scope", scope, "limit", statsPageLimit, "request_id", middleware.RequestIDFrom(ctx))
	}
	return gen.ExportAdminStats200TextcsvResponse{
		Body:          bytes.NewReader(csvBytes),
		ContentLength: int64(len(csvBytes)),
	}, nil
}

// statsCSV 把统计行序列化成 CSV。
//
// ⚠️ **带 UTF-8 BOM**：表头是英文，但这份文件会被 Excel 打开，而 Excel 在
// 中文 Windows 上按 GBK 猜编码。将来加中文列名时没有 BOM 会显示成乱码，
// 而那时没人会想起来是这里的问题。BOM 对 pandas / csv 模块无害（它们认 utf-8-sig）。
//
// ⚠️ **不 JOIN 邮箱进来。** `StatBucket` 的字段就是 user_id / server_id ——
// 统计导出的泄漏面因此比用户导出小一个量级，这不是巧合。
// 为了「CSV 好看」加一列 email 会让这个端点从「流量数字」变成「用户名单」。
func statsCSV(rows []gen.StatBucket, scope string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF")
	w := csv.NewWriter(&buf)
	if err := w.Write(statsCSVHeader); err != nil {
		return nil, err
	}
	for _, r := range rows {
		userID, serverID := "", ""
		if r.UserId != nil {
			userID = strconv.FormatInt(*r.UserId, 10)
		}
		if r.ServerId != nil {
			serverID = strconv.FormatInt(*r.ServerId, 10)
		}
		if err := w.Write([]string{
			// RFC3339 而不是本地格式：这份 CSV 会被脚本读。
			// record_at 已经是「上海当天 00:00」换算过的 UTC 时刻（catalogRecordAt）。
			r.RecordAt.UTC().Format(time.RFC3339),
			scope,
			userID,
			serverID,
			strconv.FormatInt(r.UploadBytes, 10),
			strconv.FormatInt(r.DownloadBytes, 10),
		}); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ============================================================
// 运营看板（模块 1）—— GetAdminDashboard
// ============================================================
//
// 🔴 **五条独立查询，并发度 2，不开事务，任一格失败渲染成「—」。** 三条理由各不相同：
//
//  1. **一个数算不出来不该让整页 500。** 在线数来自 UNLOGGED 表（崩溃后自动 TRUNCATE），
//     收入要扫 orders 且**没有 paid_at 索引**（顺序扫描）—— 这两个的失败模式与
//     「数一下未读工单」完全不同。任一格失败时把那一格留空（契约里 AdminDashboard
//     的字段全是可选的，缺字段 = 前端渲染「—」），其余四格照常显示。
//  2. **并发度必须是 2。** ADR 0005 的硬约束：**每实例连接池 max=2**。
//     开五个 goroutine 只会让三个在池上排队，还多占两个 context ——
//     而排队的那三个如果碰上池被别的请求占满，整块看板会一起超时。
//  3. 🔴 **不开事务。** 五个数字之间**不需要**一致的快照 —— 看板上的「今日收入」
//     和「在线人数」本来就不是同一时刻的事实。开一个事务只会让这五条查询
//     串行地持有同一条连接（连接池只有 2 条），把并发度从 2 变成 1。
//
// ⚠️ `errgroup` 的回调**一律返回 nil**：`Group.Wait` 的语义是「任一失败即整体失败」，
//    而这里要的恰好相反。错误记在各自的格子里，由外面决定怎么渲染。
//    用 errgroup 而不是裸 WaitGroup 是为了 `SetLimit(2)` 那一行 —— 并发度是本节的重点。
//
// ⚠️ `active_nodes` 映射 **alive_nodes**（2 分钟内有 /push 的），不是 enabled_nodes：
//    管理员打开看板是想知道**现在有几个能用的**。两者的差值才是「掉线的节点数」。

// dashboardConcurrency 是看板并发度。见上：等于连接池的 max。
const dashboardConcurrency = 2

type adminDashboardQuerier interface {
	GetAdminDashboardOnlineUsers(ctx context.Context) (dbgen.GetAdminDashboardOnlineUsersRow, error)
	GetAdminDashboardNodes(ctx context.Context) (dbgen.GetAdminDashboardNodesRow, error)
	GetAdminDashboardTrafficToday(ctx context.Context) (dbgen.GetAdminDashboardTrafficTodayRow, error)
	GetAdminDashboardRevenue(ctx context.Context) (dbgen.GetAdminDashboardRevenueRow, error)
	GetAdminDashboardQueues(ctx context.Context) (dbgen.GetAdminDashboardQueuesRow, error)
}

// GetAdminDashboard 实现 GET /api/v1/admin/dashboard。
func (s *Server) GetAdminDashboard(ctx context.Context, _ gen.GetAdminDashboardRequestObject) (gen.GetAdminDashboardResponseObject, error) {
	if _, ok := middleware.AdminFrom(ctx); !ok {
		return nil, errNoAdminAuth
	}
	data := loadAdminDashboard(ctx, s.db, func(cell string, err error) {
		// 每一格的失败都要留痕：前端只会显示一个「—」，
		// 而「哪一格坏了、坏了多久」只能从这里看。
		s.logger.ErrorContext(ctx, "bp_admin_dashboard_cell_failed 看板某一格取数失败，该格渲染为「—」",
			"cell", cell, "err", err, "request_id", middleware.RequestIDFrom(ctx))
	})
	return gen.GetAdminDashboard200JSONResponse{Data: data, Meta: s.meta(ctx)}, nil
}

func loadAdminDashboard(ctx context.Context, q adminDashboardQuerier, onErr func(string, error)) gen.AdminDashboard {
	var (
		online  dbgen.GetAdminDashboardOnlineUsersRow
		nodes   dbgen.GetAdminDashboardNodesRow
		traffic dbgen.GetAdminDashboardTrafficTodayRow
		revenue dbgen.GetAdminDashboardRevenueRow
		queues  dbgen.GetAdminDashboardQueuesRow

		onlineOK, nodesOK, trafficOK, revenueOK, queuesOK bool
	)

	var g errgroup.Group
	// 🔴 并发度 = 连接池 max（ADR 0005）。见本节头第 2 条。
	g.SetLimit(dashboardConcurrency)

	// ⚠️ 五个回调**都返回 nil**：errgroup 的 Wait 在任一非 nil 时会提前返回，
	//    而这里要的是「五格各自成败，互不影响」。用 errgroup 只为 SetLimit。
	g.Go(func() error {
		r, err := q.GetAdminDashboardOnlineUsers(ctx)
		if err != nil {
			onErr("online_users", err)
			return nil
		}
		online, onlineOK = r, true
		return nil
	})
	g.Go(func() error {
		r, err := q.GetAdminDashboardNodes(ctx)
		if err != nil {
			onErr("nodes", err)
			return nil
		}
		nodes, nodesOK = r, true
		return nil
	})
	g.Go(func() error {
		r, err := q.GetAdminDashboardTrafficToday(ctx)
		if err != nil {
			onErr("traffic_today", err)
			return nil
		}
		traffic, trafficOK = r, true
		return nil
	})
	g.Go(func() error {
		r, err := q.GetAdminDashboardRevenue(ctx)
		if err != nil {
			onErr("revenue", err)
			return nil
		}
		revenue, revenueOK = r, true
		return nil
	})
	g.Go(func() error {
		r, err := q.GetAdminDashboardQueues(ctx)
		if err != nil {
			onErr("queues", err)
			return nil
		}
		queues, queuesOK = r, true
		return nil
	})
	// 回调恒返回 nil，所以 Wait 不会有错误。仍然接住它，别让 errcheck 之类的
	// 静态检查因为一个被丢弃的返回值而以为这里有遗漏。
	_ = g.Wait()

	var d gen.AdminDashboard
	if onlineOK {
		d.OnlineUsers = ptrOf(online.OnlineUsers)
	}
	if nodesOK {
		// 🔴 active_nodes ← alive_nodes（2 分钟内有上报），不是 enabled_nodes。
		d.ActiveNodes = ptrOf(int32(nodes.AliveNodes))
		d.TotalNodes = ptrOf(int32(nodes.TotalNodes))
	}
	if trafficOK {
		d.TodayUploadBytes = ptrOf(traffic.TodayUploadBytes)
		d.TodayDownloadBytes = ptrOf(traffic.TodayDownloadBytes)
	}
	if revenueOK {
		d.TodayRevenueAmount = ptrOf(revenue.TodayRevenueAmount)
		d.MonthRevenueAmount = ptrOf(revenue.MonthRevenueAmount)
	}
	if queuesOK {
		d.PendingTickets = ptrOf(int32(queues.PendingTickets))
		d.UnderpaidOrders = ptrOf(int32(queues.UnderpaidOrders))
	}
	return d
}

// ============================================================
// 邮件（模块 15）—— BroadcastAdminMail（D11b）· ListAdminMailLogs
// ============================================================
//
// 🔴 **BroadcastAdminMail 只能实现一半，另一半必须 501。**
// `email_log` **没有正文列**：它有 `template`（模板键）和 `subject`，没有 `body`。
// 而 `MailBroadcastRequest.body` 是一段**临时写的正文**，不是模板键 ——
// 它没有地方可存，`ClaimQueuedMail`（mail-send 任务唯一的取件查询）也取不到正文。
// 于是：
//
//	body 是一个**已登记的模板键** → 走 AdminCountBroadcastAudience + AdminEnqueueBroadcastMails；
//	body 是任何别的东西           → **501**（契约自己留了这个出口：
//	                                「服务端对未实现的组合返回 501 并 // TODO(P1)」）。
//
// 退化成「把正文丢掉、按某个默认模板发」是最坏的选择：管理员写了一封信、
// 系统发出去的是另一封，而两边都显示成功。
// TODO(P1): 加 `email_log.body_md text`，或建一张 `mail_broadcasts` 父表
// （后者更对：一次群发的正文只该存一份，不是每个收件人一份）。
//
// 🔴 **退信过滤是硬要求不是优化。** D11b 的危害栏原文：
// 「AWS SES 退信率 ≥ 5% 进入审查、≥ 10% 可能暂停发信」，而邮件是 ADR 0002 定的
// **唯一失联恢复通道**。一次群发把发信资格打掉，代价不是这封信没发出去，
// 是下一次域名被封时我们没有任何办法通知用户。
// 两条排除（已注销用户 / 已硬退地址）写在 SQL 的 WHERE 里，是判定的一部分。
//
// 🔴 **本系统在数据模型上无法尊重「运营邮件」的退订意愿。**
// `users` 只有 notify_expire / notify_traffic 两个开关，且列注释写明「只管到期与流量两类」——
// schema 上刻意没有总开关，也没有 notify_broadcast。缺口已登记
// （补法：加一列并同时改 AdminCountBroadcastAudience 与 AdminEnqueueBroadcastMails 两处谓词）。
//
// 🔴 **「强制先发测试件」在契约上没有入口。** page-inventory §4.4 D11b 要求
// 「二次确认 + **强制先发测试件** + 确认框显示收件人数 + 频率上限 + 审计」，
// 而 `MailBroadcastRequest` 里没有任何字段能表达「这是一次测试件」或者
// 「我已经发过测试件了」。本文件实现了其中三条（收件人数 → 进审计与日志、
// 频率上限 → 2/h、审计 → InTx），测试件那条只能登记。

const (
	// broadcastTemplateDomain 是**唯一**允许群发的模板键。
	//
	// 0011 的 `email_log.template` 列注释给的三个示例是
	// 'verify_code' / 'domain_broadcast' / 'expire_remind'，其中只有第二个是运营广播。
	// 🔴 另外几个键**必须**被挡住，而且理由各不相同：
	//   · verify_code / password_reset —— 群发它们等于给全站用户各发一封凭据邮件，
	//     而 ClaimQueuedMail 会真的去渲染并投递。这是一次自制的钓鱼活动。
	//   · expire_remind / traffic_remind —— 它们的幂等键是 `(user_id, template, 当天)`
	//     （tasks.sql），手工群发一次会把当天的提醒配额吃掉，
	//     于是真正该收到到期提醒的人当天收不到了。
	// 允许列表因此是**白名单不是黑名单**：将来新增模板键时默认发不出去，
	// 需要有人显式把它加进来 —— 这个方向的失败是「发不出去」，反过来是「发错了」。
	broadcastTemplateDomain = "domain_broadcast"

	// 群发限流：契约的 summary 逐字写着「限流 2/h」。
	bucketMailBroadcastAdmin = "admin_mail_broadcast_1h"
	mailBroadcastPerHour     = 2

	// broadcastExpiringWithinDays 是 audience='expiring_soon' 的窗口。
	// 契约没有给这个参数，只能由服务端钉。7 天与 remindExpireWithinDays（3 天）
	// 刻意不同：那是自动提醒（要精准），这是运营活动（要覆盖面）。
	broadcastExpiringWithinDays = 7

	// 主题长度上限。email_log.subject 是 text（无上限），而主题会进每一行收件记录 ——
	// 一次 1000 人的群发就是 1000 份。200 远超任何真实主题。
	broadcastSubjectMaxRunes = 200
)

// broadcastTemplates 是允许群发的模板键白名单。见 broadcastTemplateDomain 的注释。
var broadcastTemplates = map[string]bool{
	broadcastTemplateDomain: true,
}

// broadcastAudienceToDB 校验 audience 并给出 SQL 要的字符串。
//
// 🔴 SQL 里的 `ELSE false` 不是兜底装饰：audience 传了一个我们不认识的值时，
// 命中人数是 **0**，不是全部。反过来写（ELSE true）意味着一次拼错的枚举 = 一次全站群发。
// 这里再挡一道，是为了让「拼错了」表现为 422 而不是「发给了 0 个人」——
// 后者会让管理员以为系统坏了，然后重试三次。
func broadcastAudienceToDB(a gen.MailBroadcastRequestAudience, planIDs *[]int64) (string, []int64, error) {
	switch a {
	case gen.MailBroadcastRequestAudienceAll,
		gen.MailBroadcastRequestAudienceActive,
		gen.MailBroadcastRequestAudienceExpired,
		gen.MailBroadcastRequestAudienceExpiringSoon:
		return string(a), []int64{}, nil
	case gen.MailBroadcastRequestAudienceByPlan:
		if planIDs == nil || len(*planIDs) == 0 {
			// by_plan 而不给 plan_ids，SQL 里 `= ANY('{}')` 恒为 false → 命中 0 人。
			// 422 说清楚，别让它表现成「一个人都没匹配上」。
			return "", nil, catalogUnprocessable("plan_ids", "audience=by_plan 时必须给至少一个套餐 id")
		}
		return string(a), *planIDs, nil
	default:
		return "", nil, catalogUnprocessable("audience", fmt.Sprintf("未知的收件人范围 %q", string(a)))
	}
}

// BroadcastAdminMail 实现 POST /api/v1/admin/mail/broadcast。
func (s *Server) BroadcastAdminMail(ctx context.Context, req gen.BroadcastAdminMailRequestObject) (gen.BroadcastAdminMailResponseObject, error) {
	actor, admin, err := s.catalogActor(ctx)
	if err != nil {
		if errors.Is(err, errNoAdminAuth) {
			return nil, err
		}
		return gen.BroadcastAdminMail500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "组装审计上下文失败", err),
		}, nil
	}
	if !catalogRoleCanWrite(admin.Role) {
		return gen.BroadcastAdminMail403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED,
				"当前角色不能群发邮件（由角色决定：owner / admin）"),
		}, nil
	}
	if req.Body == nil {
		return gen.BroadcastAdminMail422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体缺失"),
		}, nil
	}

	// 🔴 **自定义正文 → 501**，在限流与任何写入之前判：
	//    一个注定 501 的请求不该消耗那 2 次/小时的配额。
	tmpl := strings.TrimSpace(req.Body.Body)
	if !broadcastTemplates[tmpl] {
		s.logger.WarnContext(ctx,
			"bp_admin_broadcast_custom_body 带自定义正文的群发未实现（email_log 没有正文列），已返回 501",
			"admin_id", admin.AdminID, "audience", string(req.Body.Audience),
			"body_chars", len([]rune(req.Body.Body)), "request_id", middleware.RequestIDFrom(ctx))
		// TODO(P1): 加 email_log.body_md 或建 mail_broadcasts 父表之后，
		// 这里改成「把正文存下来 + 用一个 'custom' 模板键入队」。
		return nil, ErrNotImplemented
	}

	if retry, limited := s.checkRateRules(ctx, rateRule{
		bucket:  bucketMailBroadcastAdmin,
		subject: strconv.FormatInt(admin.AdminID, 10),
		limit:   mailBroadcastPerHour,
		window:  time.Hour,
	}); limited {
		return gen.BroadcastAdminMail429JSONResponse{
			ErrRateLimitedJSONResponse: s.rateLimited(ctx,
				fmt.Sprintf("群发过于频繁，每小时最多 %d 次（退信率是发信资格的生死线）", mailBroadcastPerHour), retry),
		}, nil
	}

	queued, expected, err := broadcastAdminMail(ctx, s.catalogAudit(), actor,
		*req.Body, tmpl, s.mailSender().Name())
	if err != nil {
		if oe, ok := asCatalogOpError(err); ok {
			return gen.BroadcastAdminMail422JSONResponse{
				ErrUnprocessableJSONResponse: s.catalogDetail(ctx, oe),
			}, nil
		}
		return gen.BroadcastAdminMail500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "群发入队失败", err),
		}, nil
	}
	if queued != expected {
		// 不是错误：确认框那个数字与真正入队的数字之间隔着管理员点确认的几秒，
		// 期间可能有人注册、有人到期。记下来是为了让「说好 312 人、实际 315 人」
		// 有据可查 —— 差距大到离谱时它是两处 WHERE 漂移了的信号。
		s.logger.InfoContext(ctx, "群发实际入队数与预估命中数不同（正常，中间隔着确认的几秒）",
			"expected", expected, "queued", queued, "request_id", middleware.RequestIDFrom(ctx))
	}
	return gen.BroadcastAdminMail202JSONResponse{
		Data: gen.MailBroadcastResult{Queued: queued},
		Meta: s.meta(ctx),
	}, nil
}

func broadcastAdminMail(
	ctx context.Context,
	run catalogAuditRunner,
	actor audit.Actor,
	body gen.MailBroadcastRequest,
	template string,
	esp string,
) (queued int64, expected int64, err error) {
	if rErr := catalogCheckReason(body.Reason); rErr != nil {
		return 0, 0, rErr
	}
	subject := strings.TrimSpace(body.Subject)
	if subject == "" {
		return 0, 0, catalogUnprocessable("subject", "邮件主题不能为空")
	}
	if n := len([]rune(subject)); n > broadcastSubjectMaxRunes {
		return 0, 0, catalogUnprocessable("subject",
			fmt.Sprintf("主题最多 %d 个字符（当前 %d）", broadcastSubjectMaxRunes, n))
	}
	audience, planIDs, aErr := broadcastAudienceToDB(body.Audience, body.PlanIds)
	if aErr != nil {
		return 0, 0, aErr
	}
	within := pgtype.Interval{Days: broadcastExpiringWithinDays, Valid: true}

	runErr := run(ctx, actor, func(ctx context.Context, tx dbgen.Querier) (audit.Entry, error) {
		// 先算命中人数（page-inventory §4.4 D11b：「确认框显示收件人数」）。
		// ⚠️ 契约里没有 confirmation 字段，所以这个数字**没法拿去和操作者对质** ——
		//    它只能进审计与日志。缺口已登记。
		n, err := tx.AdminCountBroadcastAudience(ctx, dbgen.AdminCountBroadcastAudienceParams{
			Audience:       audience,
			ExpiringWithin: within,
			PlanIds:        planIDs,
		})
		if err != nil {
			return audit.Entry{}, err
		}
		expected = n
		if n == 0 {
			// 命中 0 人不是「成功发了 0 封」。422 让管理员知道筛选条件没选中任何人 ——
			// 而「命中 0 人」正是这个确认数字要防的那种意外。
			return audit.Entry{}, catalogUnprocessable("audience",
				"这个收件人范围没有命中任何人（已排除注销用户与曾经硬退的地址）")
		}

		rows, err := tx.AdminEnqueueBroadcastMails(ctx, dbgen.AdminEnqueueBroadcastMailsParams{
			Esp:            esp,
			Template:       template,
			Subject:        subject,
			Audience:       audience,
			ExpiringWithin: within,
			PlanIds:        planIDs,
		})
		if err != nil {
			return audit.Entry{}, err
		}
		// 🔴 `queued` 取**入队语句的实际行数**，不是上面那条 count 的结果。
		queued = int64(len(rows))

		domains := make(map[string]int, 8)
		for _, r := range rows {
			domains[r.ToDomain]++
		}
		return audit.Entry{
			Action:     "D11b.mail.broadcast",
			TargetType: "mail_broadcast",
			TargetID:   template + ":" + audience,
			Before:     nil,
			After: map[string]any{
				"audience":            audience,
				"plan_ids":            planIDs,
				"expiring_within_day": broadcastExpiringWithinDays,
				"template":            template,
				"subject":             subject,
				"esp":                 esp,
				"expected_recipients": expected,
				"queued":              queued,
				// 按域名分组的收件数：退信率是按域名看的（ADR 0002 §7 关心的
				// 正是 qq.com / 163.com 这几个），事后追一次群发的影响必须能分域名看。
				"by_domain": domains,
			},
			Reason: strings.TrimSpace(body.Reason),
		}, nil
	})
	if runErr != nil {
		return 0, expected, runErr
	}
	return queued, expected, nil
}

// ---- ListAdminMailLogs ----

type adminMailLogLister interface {
	AdminListMailLogsPage(ctx context.Context, arg dbgen.AdminListMailLogsPageParams) ([]dbgen.EmailLog, error)
	AdminCountMailLogsFiltered(ctx context.Context, recipientDomain *string) (int64, error)
}

// ListAdminMailLogs 实现 GET /api/v1/admin/mail/logs。
//
// 这个端点兼 ADR 0002 §7 的**送达率实测数据源**（0011 把 email_log 与 email_probe
// 合并成一张表，理由是「恰恰是『其他邮件』（域名广播）的送达率才是生死攸关的那一个」）。
//
// ⚠️ 不带域名过滤时**没有可用索引**（这张表上只有 `(to_domain, template, created_at DESC)`），
//
//	是一次顺序扫 + 排序。登记，别当成走了索引。
func (s *Server) ListAdminMailLogs(ctx context.Context, req gen.ListAdminMailLogsRequestObject) (gen.ListAdminMailLogsResponseObject, error) {
	if _, ok := middleware.AdminFrom(ctx); !ok {
		return nil, errNoAdminAuth
	}
	data, meta, err := listAdminMailLogs(ctx, s.db, s.meta(ctx), req.Params, s.catalogWarn(ctx))
	if err != nil {
		return gen.ListAdminMailLogs500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取邮件日志失败", err),
		}, nil
	}
	return gen.ListAdminMailLogs200JSONResponse{Data: data, Meta: meta}, nil
}

// adminMailLogView 映射一条邮件日志。
//
// 🔴 **`sent_at` 必须回落到 `created_at`。** 契约把 sent_at 放在 required 里，
// 而库里这一列可空（'queued' 的信还没发出去就是 NULL，`MarkMailSendFailed` 还会把它清回 NULL）。
// 把 NULL 序列化成零值时间的后果是 `1970-01-01` 出现在送达率报表里，
// 会被当成一封「很久以前就发了但没到」的信 —— 而那份报表正是「选哪家 ESP」的唯一依据。
func adminMailLogView(r dbgen.EmailLog) gen.MailLogEntry {
	sentAt := r.SentAt
	if !sentAt.Valid {
		sentAt = r.CreatedAt
	}
	e := gen.MailLogEntry{
		Id:              r.ID,
		RecipientDomain: r.ToDomain,
		SentAt:          ttime(sentAt),
		DeliveredAt:     tptr(r.DeliveredAt),
		BounceCode:      r.BounceCode,
	}
	if r.Esp != "" {
		e.Esp = ptrOf(r.Esp)
	}
	if r.Template != "" {
		e.TemplateKey = ptrOf(r.Template)
	}
	return e
}

func listAdminMailLogs(
	ctx context.Context,
	q adminMailLogLister,
	meta gen.Meta,
	params gen.ListAdminMailLogsParams,
	warn func(string),
) ([]gen.MailLogEntry, gen.Meta, error) {
	want, limitPlusOne := pageLimit(params.Limit)

	var domain *string
	if params.RecipientDomain != nil {
		// ⚠️ 过滤值在 SQL 里 lower()，to_domain 入库时也 lower 过 ——
		//    两侧同形才不会「按 QQ.com 查不到东西」。这里只做 trim。
		if d := strings.TrimSpace(*params.RecipientDomain); d != "" {
			domain = &d
		}
	}

	arg := dbgen.AdminListMailLogsPageParams{PageLimit: limitPlusOne, RecipientDomain: domain}
	if params.Cursor != nil && *params.Cursor != "" {
		cur, ok := decodePageCursor(*params.Cursor)
		if !ok {
			warn("邮件日志游标非法，按第一页处理")
		} else {
			arg.CursorAt = tstz(cur.At)
			arg.CursorID = &cur.ID
		}
	}

	rows, err := q.AdminListMailLogsPage(ctx, arg)
	if err != nil {
		return nil, meta, err
	}
	hasMore := len(rows) > want
	if hasMore {
		rows = rows[:want]
	}
	out := make([]gen.MailLogEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminMailLogView(r))
	}
	meta.HasMore = &hasMore
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		// 游标用 **created_at**（排序键），不是 sent_at —— 后者可空，
		// 用它编游标会在 queued 的信上产生一个 NULL 分量，行比较求值为 NULL、返回 0 行，
		// 现象是「翻到某一页就没有了」。
		meta.NextCursor = ptrOf(encodePageCursor(last.ID, ttime(last.CreatedAt)))
	}
	if params.Count != nil && *params.Count {
		total, err := q.AdminCountMailLogsFiltered(ctx, domain)
		if err != nil {
			return nil, meta, err
		}
		meta.Total = &total
	}
	return out, meta, nil
}
