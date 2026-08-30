package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/audit"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 后台「节点与密钥」十个 operation（api-contract §6.1 模块 5 与模块 6）。
//
// 这一组的危险性与订单面不同：订单面弄错是钱，这一组弄错是**所有人同时断线**，
// 而且几乎每一种弄错法都**不报错**。下面五条纪律贯穿全文件，每一条都对应一次
// 「不这么写会静默出事」：
//
//  1. 🔴 **PATCH 未提供的字段必须回填当前值。** `AdminUpdateNode` 的 SQL 无条件写
//     name/protocol/host/port/region/enabled 六列，而 `AdminNodeUpsert` 里除 name/type
//     之外全是可选。照着请求体的零值写下去，一次「改个显示名」会把 host 抹成空串、
//     把 enabled 抹成 false —— 前者不报错（host 是 NOT NULL 但空串合法），
//     后者是「改完名字节点就没人能连了」。所以改前必须在**同一事务里**先读一份当前值。
//
//  2. 🔴 **D5 吊销的前置条件由数据库拒绝，不由这里的 if 拒绝。**
//     `AdminGetNodeKeyByPrefix` 只负责分辨 404 / 409 与取审计前像；真正的闸是
//     `AdminRevokeNodeKeyTwoStep` 里的 EXISTS。两条语句之间有窗口，而轮换期节点每
//     60 秒改一次 `last_used_at`、另一个管理员可能同时在吊销另一把 —— 判断与写分开，
//     就允许「判断时有见证、写的时候见证已经没了」发生，其结果正是这条规则要防的
//     那件事：节点在下一次轮询时失联。
//
//  3. 🔴 **`AdminGetNodeKeyByPrefix` 是 `:many`，必须自己断言恰好一行。**
//     `server_keys.key_prefix` 上没有唯一索引（0004 只在 key_hash 与 server_id 上建），
//     两行同前缀是能插进去的。取第一行 = 吊销掉另一把密钥 = 节点失联，且事后
//     从日志里看不出任何异常。≥2 行一律 500 + 告警（不是 409：那是我们自己的数据错误，
//     不是调用者的状态冲突）。
//
//  4. 🔴 **密钥明文只进 201 响应体一次。** 不落库（库里只有 sha256）、不进日志、
//     不进审计快照 —— audit_logs 是 append-only 且永不删除的，一条明文进去就永远在里面。
//
//  5. 🔴 **审计与业务写入同一事务**（§6.3 第 1 条）。本文件所有写路径一律走
//     `adminNodeTxRunner`，它在生产上就是 `audit.InTx`；审计写失败 → 业务写入回滚。
//     不给「不写审计」留任何出口。
//
// # 四层强制（§6.2）在这一组的实际覆盖面
//
// 契约冻结，能强制的层只有契约给了入口的那些。下表是逐个 operation 核过 openapi 之后
// 的实际情况，**缺的那些是契约缺口，不是本文件偷懒**（逐条登记在文件末尾的 gap 清单）：
//
//	operation              L1 确认串        L2 reason≥8   L3 TOTP    L4 权限
//	createAdminNode  (D9)  —（契约无字段）  ✅            —          ✅ 角色
//	updateAdminNode  (D9)  —（契约无字段）  ✅            —          ✅ 角色
//	deleteAdminNode  (D4)  ✅ = 节点名      ✅            —（契约无） ✅ 角色
//	enable/disable   (D4)  —（无请求体）    —（无请求体） —（无）     ✅ 角色
//	createAdminNodeKey(D5) —（无字段）      —（无字段）   ✅         ✅ 角色
//	revokeAdminNodeKey(D5) —（无请求体）    —（无请求体） ✅         ✅ 角色
//
// L4 用**角色**而不是权限位：`admin_users` 上只有四个权限位列
// （perm_mark_order_paid / perm_refund / perm_adjust_balance / perm_export_csv，0002），
// **没有 perm_node_write**，`mw.AdminPermission` 的枚举里同样没有。
// openapi 的 `AdminPermission` 枚举里那个 `admin.node.write` 在库里没有落点。
// 于是唯一诚实的做法是按角色判：owner / admin 可写，support 只读。
// **绝不假装成功** —— 也绝不为了「看起来有 L4」去读一个不存在的列。

// ============================================================
// 节点密钥的形态（api-contract §3.2.1）
// ============================================================

// 密钥串形如 `bpn_<key_id>_<secret>`：
//
//	bpn_      固定前缀，便于在日志/代码扫描里正则识别泄漏
//	key_id    base32 六字符（`^[a-z2-7]{6}$`），存进 server_keys.key_prefix
//	secret    32 字节 CSPRNG，base64url 无填充（43 字符）
//
// 🔴 **key_prefix 存的是 key_id 本身，不是 0004 列注释里那个 `'bpk_a1b2c3d4'` 样例。**
// 这不是随手选的：`DELETE /admin/node-keys/{key_id}` 的路径参数**只有**那六个字符，
// 它是服务端定位这一行的唯一句柄。如果签发时存 `bpk_` + 8 字符，吊销时就永远
// 拼不出要查的串 —— D5 第 2 步在契约层面直接不可实现。
// 交接说明 §二 也是这个结论：「签发时生成什么形状，吊销时就按什么形状查」。
// 存量用 `bpk_` 形状手工灌进去的密钥（docs/04-ops/local-development.md 的冒烟种子）
// 因此无法通过本端点吊销，只能在库里改 —— 这是可接受的：那不是签发出来的密钥。
const (
	nodeKeyTokenPrefix = "bpn_"
	nodeKeyIDLen       = 6
	nodeKeySecretBytes = 32

	// nodeKeyIDAlphabet 是 RFC 4648 base32 字母表的小写形式，恰好 32 个字符。
	// 32 整除 256，所以 `b & 31` 是**均匀**的，不需要拒绝采样 ——
	// 用 `b % len(alphabet)` 配一个非 2 的幂长度才需要，那时不做拒绝采样会有偏。
	nodeKeyIDAlphabet = "abcdefghijklmnopqrstuvwxyz234567"
)

// nodeKeyMaxActivePerServer 是「同时有效的密钥数」上限（data-model §8.3 的应用层规则）。
//
// 2 不是保守取值，是 D5 的定义：轮换期同时持有新旧两把是正常的，出现第三把说明
// 上一次轮换没做完（旧的忘了吊销）—— 而那是一个正在积累的失控面：
// 每多一把没人记得的有效密钥，就多一条谁也不会去查的进入通道。
const nodeKeyMaxActivePerServer = 2

// nodeKeyDefaultScopes 是签发缺省授予的五个 scope（契约 createAdminNodeKey 的描述）。
//
// 🔴 `node:status:write` **不在**缺省里：它是节点自报负载的写口，
// 只有真的会推 /status 的节点才需要，默认给出去等于让每一把密钥都能改在线态展示。
//
// ⚠️ 0004 给 `server_keys.scopes` 的 DEFAULT 是 `'{uniproxy}'` —— 一个与契约枚举
// 完全不同的旧值。所以这一列**必须显式传**，靠默认值会签发出一把 scope 谁也不认识的密钥
// （鉴权时每个 HasScope 都为 false，现象是节点 403 而不是 401，很难往密钥上想）。
var nodeKeyDefaultScopes = []gen.NodeScope{
	gen.NodeConfigRead,
	gen.NodeUsersRead,
	gen.NodeTrafficWrite,
	gen.NodeAliveWrite,
	gen.NodeAliveRead,
}

// nodeKeyAllowedScopes 是 scope 白名单。
//
// **精确匹配，非前缀**（契约 `NodeScope` 的原话），而且是 Go 里的常量 map、不从 DB 读：
// 前缀匹配会让 `node:alive` 这样一个不存在的 scope 意外覆盖两个真 scope；
// 从 DB 读会让「能签发什么权限」变成一张可被后台改的表。
var nodeKeyAllowedScopes = map[gen.NodeScope]bool{
	gen.NodeConfigRead:   true,
	gen.NodeUsersRead:    true,
	gen.NodeTrafficWrite: true,
	gen.NodeAliveWrite:   true,
	gen.NodeAliveRead:    true,
	gen.NodeStatusWrite:  true,
}

// ============================================================
// 业务错误（handler 侧映射成状态码）
// ============================================================
//
// 用哨兵错误而不是在事务闭包里直接构造响应：闭包必须能靠 `return err` 触发回滚
// （audit.InTx 的语义），而响应对象不是 error。两者混在一起写，
// 迟早会出现「构造了 422 响应但忘了 return error」= 业务写入被提交的那一版。
var (
	errAdminNodeNotFound        = errors.New("节点不存在或已删除")
	errAdminNodeKeyNotFound     = errors.New("节点密钥不存在")
	errAdminNodeConfirmMismatch = errors.New("确认串与节点名不一致")
	errAdminNodeKeyRevoked      = errors.New("该密钥已经吊销过")
	errAdminNodeKeyNoWitness    = errors.New("新密钥尚未被节点使用过")
	errAdminNodeKeyAmbiguous    = errors.New("同一 key_id 命中多行")
	errAdminNodeKeyTooMany      = errors.New("同时有效的密钥已达上限")
	errAdminNodeCodeTaken       = errors.New("节点 code 已被占用")
	errAdminNodeBadGroup        = errors.New("分组不存在")
)

// ============================================================
// 收窄的数据库能力
// ============================================================

// adminNodeReader 是三个只读 operation 用到的全部数据库能力。
//
// 收窄成接口而不是直接吃 *store.Store：后者是具体类型，单测塞不了假实现
// （与 task.go / node.go 同一条纪律）。
type adminNodeReader interface {
	AdminListNodesPage(ctx context.Context, arg dbgen.AdminListNodesPageParams) ([]dbgen.AdminListNodesPageRow, error)
	AdminCountNodesFiltered(ctx context.Context) (int64, error)
	AdminGetNode(ctx context.Context, serverID int64) (dbgen.AdminGetNodeRow, error)
	AdminListNodeKeys(ctx context.Context, serverID int64) ([]dbgen.AdminListNodeKeysRow, error)
}

// adminNodeWriter 是**事务内**用到的全部数据库能力。
//
// 含 AdminGetNode 是刻意的：PATCH 的「未提供字段沿用当前值」与响应投影都必须读，
// 而这两次读**必须与写在同一个事务里** —— 在事务外先读一份再进事务写，
// 中间的窗口足够另一个管理员改掉同一个节点，于是我们会拿他的值去回填我们的 PATCH。
type adminNodeWriter interface {
	AdminGetNode(ctx context.Context, serverID int64) (dbgen.AdminGetNodeRow, error)

	CreateServer(ctx context.Context, arg dbgen.CreateServerParams) (dbgen.Server, error)
	InitNodeRev(ctx context.Context, serverID int64) (dbgen.NodeRev, error)

	AdminUpdateNode(ctx context.Context, arg dbgen.AdminUpdateNodeParams) (dbgen.AdminUpdateNodeRow, error)
	BumpConfigRev(ctx context.Context, serverID int64) (dbgen.BumpConfigRevRow, error)

	AddServerToGroup(ctx context.Context, arg dbgen.AddServerToGroupParams) error
	RemoveServerFromGroup(ctx context.Context, arg dbgen.RemoveServerFromGroupParams) error
	BumpUserRevByGroup(ctx context.Context, groupID int64) error

	AdminSetNodeEnabled(ctx context.Context, arg dbgen.AdminSetNodeEnabledParams) (dbgen.AdminSetNodeEnabledRow, error)
	AdminGetNodeForDangerOp(ctx context.Context, serverID int64) (dbgen.AdminGetNodeForDangerOpRow, error)
	AdminSoftDeleteNode(ctx context.Context, serverID int64) (dbgen.AdminSoftDeleteNodeRow, error)

	AdminCountActiveNodeKeys(ctx context.Context, serverID int64) (dbgen.AdminCountActiveNodeKeysRow, error)
	CreateServerKey(ctx context.Context, arg dbgen.CreateServerKeyParams) (dbgen.ServerKey, error)
	AdminGetNodeKeyByPrefix(ctx context.Context, keyPrefix string) ([]dbgen.AdminGetNodeKeyByPrefixRow, error)
	AdminRevokeNodeKeyTwoStep(ctx context.Context, arg dbgen.AdminRevokeNodeKeyTwoStepParams) (dbgen.AdminRevokeNodeKeyTwoStepRow, error)
}

// adminNodeTxRunner 是「业务写入 + 审计写入同事务」这件事本身。
//
// 🔴 为什么要多这一层接口，而不是让业务函数直接调 `audit.InTx`：
// `audit.InTx` 的回调签名收的是 `*dbgen.Queries`（具体类型），单测塞不进假实现，
// 于是「审计写失败 → 业务回滚」这条**最重要的**性质就没有办法在本包里被测到。
// 多一个接口换来的是：那条性质有一个会失败的测试盯着它。
//
// 生产实现 pgAdminNodeTx 是 audit.InTx 的一层薄封装，没有别的逻辑 ——
// 也就是说这一层不可能与 audit 包的语义漂移。
type adminNodeTxRunner interface {
	Run(ctx context.Context, actor audit.Actor, fn func(context.Context, adminNodeWriter) (audit.Entry, error)) error
}

// pgAdminNodeTx 把 audit.InTx 适配成 adminNodeTxRunner。
type pgAdminNodeTx struct{ pool audit.Beginner }

func (t pgAdminNodeTx) Run(ctx context.Context, actor audit.Actor,
	fn func(context.Context, adminNodeWriter) (audit.Entry, error)) error {
	return audit.InTx(ctx, t.pool, actor, func(ctx context.Context, q *dbgen.Queries) (audit.Entry, error) {
		return fn(ctx, q)
	})
}

// 编译期断言：生成的 Queries 必须覆盖上面两张能力表。
// 少一个方法的现象是编译失败，而不是运行时「某个后台按钮点了没反应」。
var (
	_ adminNodeReader   = (*dbgen.Queries)(nil)
	_ adminNodeWriter   = (*dbgen.Queries)(nil)
	_ adminNodeTxRunner = pgAdminNodeTx{}
)

// L3 的最小能力面 adminStepUpVerifier 在 admin_common.go（`mw.AdminAuthConfig` 满足它）。

// nodeTx 构造生产用的事务执行器。
func (s *Server) nodeTx() adminNodeTxRunner { return pgAdminNodeTx{pool: s.db.Pool} }

// nodeStepUp 构造 L3 校验器。配置的组装在 admin_common.go 的 adminAuthConfig()——
// 管理面四个文件曾各自重建一份 mw.AdminAuthConfig，现在只有一处；
// 那里也记着残余的漂移风险（main.go 仍有独立的一份）与 TODO(P1)。
func (s *Server) nodeStepUp() adminStepUpVerifier { return s.adminAuthConfig() }

// ============================================================
// L2 / L4 与审计主体
// ============================================================

// L2 的下限 adminReasonMinRunes 在 admin_common.go（管理面四个文件共用一份）：reason ≥ 8 字符。
//
// 按 **rune** 数而不是字节数：中文原因「节点已下线」是 5 个字 15 个字节，
// 按字节判会让一条明显合格的中文原因被拒、而一条 8 个字节的英文乱码通过 ——
// 判据与它想表达的「写清楚为什么」完全脱钩。

// checkAdminNodeReason 校验 L2。返回 nil 表示通过。
func checkAdminNodeReason(reason string) *gen.ErrorDetail {
	r := strings.TrimSpace(reason)
	if len([]rune(r)) < adminReasonMinRunes {
		return &gen.ErrorDetail{
			Field:  "reason",
			Reason: fmt.Sprintf("必填，且至少 %d 个字符（会原样进审计日志）", adminReasonMinRunes),
		}
	}
	return nil
}

// adminCanWriteNodes 是 L4。
//
// 🔴 判的是**角色**不是权限位，因为库里没有 perm_node_write 这一列（见文件头）。
// support 只读：客服需要看节点状态来回答「是不是节点挂了」，但停一台节点会让
// 那台机器上的所有人在 ≤ 60 秒内掉线 —— 这不该是回工单的人手边就有的按钮。
//
// 未知角色一律拒绝：将来加了新角色而忘了在这里加分支时，
// 现象必须是「这个角色做不了节点写操作」，不能是「谁都能做」。
func adminCanWriteNodes(role string) bool {
	switch role {
	case middleware.RoleOwner, middleware.RoleAdmin:
		return true
	default:
		return false
	}
}

// adminNodeKeyGate 跑密钥两个 operation 的 L4 + L3。返回 nil 表示放行。
//
// 🔴 **顺序不能反：先判权限位，再要 TOTP。**
// 反过来的话，一个没有节点写权限的人只要打一次请求，就能让服务端把他随手填的
// 那个 6 位数记进 used_totp（RequireStepUp 验对之后会占用它）—— 而占用是不可逆的。
// 于是他可以在管理员真正要用某个 code 的那 30 秒里把它提前烧掉：
// 一个不需要任何权限、只需要猜中 6 位数的拒绝服务。先判权限就没有这条路径。
//
// 抽成自由函数而不是写在两个 handler 里：这两层的**顺序**本身就是安全属性，
// 写两遍就有两个地方可以写反，而写反了两个 handler 都还是「能用的」。
func adminNodeKeyGate(ctx context.Context, admin *middleware.AdminAuth,
	su adminStepUpVerifier, totpCode string) *middleware.AuthError {
	if admin == nil || !adminCanWriteNodes(admin.Role) {
		return &middleware.AuthError{
			Status:  403,
			Code:    "AUTH_PERMISSION_DENIED",
			Message: "当前角色不能签发或吊销节点密钥",
		}
	}
	return su.RequireStepUp(ctx, totpCode)
}

// adminNodeAuthErrIsInternal 判断 step-up 的失败是不是**我们自己的**故障。
//
// mw.RequireStepUp 的返回值里混着两类：
//   - 403 AUTH_TOTP_REQUIRED / AUTH_TOTP_INVALID —— 调用方的问题；
//   - 500 INTERNAL_ERROR —— 路由没挂管理面鉴权、或 used_totp 写不进去。
//
// 后者压成 403 的后果是「TOTP 依赖坏了」在前端长得和「验证码输错了」一模一样，
// 于是管理员会反复重输，而真正的故障没有任何人看得见。
func adminNodeAuthErrIsInternal(e *middleware.AuthError) bool {
	return e != nil && e.Status >= 500
}

// adminNodeConfirmMatches 是 L1：常数时间比对确认串。
//
// 期望值由服务端自己查出来（这里是 servers.name），**不接受请求里带来的期望值** ——
// §6.2 的原话：「前端的确认弹窗对一个直接 curl 的人是零」。
//
// 用 subtle.ConstantTimeCompare 而不是 `==`：节点名本身不是秘密（列表端点就返回它），
// 所以这里防的不是信息泄漏，而是纪律 —— 四条 L1 校验里只要有一条写成 `==`，
// 将来把同一段代码套到「确认串是用户邮箱」的 D3/D6 上就变成了一台邮箱枚举机。
// 让这一组里的每一次比对都长成同一个安全的形状，比逐个论证便宜。
func adminNodeConfirmMatches(want, got string) bool {
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// adminActor 组装审计主体。
//
// 第二个返回值为 false 表示**装配错误**（这条路由没挂管理面鉴权），调用方回 500 ——
// 不是 403：403 是「你没权限」，而这里的真相是「我们把路由配错了」，
// 用 403 会让一次配置事故看起来像一次正常的权限拒绝，没有人会去查。
func (s *Server) nodeAdminActor(ctx context.Context) (audit.Actor, *middleware.AdminAuth, bool) {
	a, ok := middleware.AdminFrom(ctx)
	if !ok || a == nil {
		s.logger.ErrorContext(ctx, "管理面 handler 在没有管理员身份的上下文里被调用",
			"request_id", middleware.RequestIDFrom(ctx))
		return audit.Actor{}, nil, false
	}
	meta := s.requestMetadata(ctx)
	actor := audit.Actor{
		// Email 取 admin_users 那一份（mw.AdminAuth.Email 已经是这一份），
		// 不是 IAP 断言里那一份：审计要留的是「本系统认为他是谁」。
		AdminID: a.AdminID,
		Email:   a.Email,
	}
	if meta.IP != nil {
		actor.IP = *meta.IP
	}
	if meta.UserAgent != nil {
		actor.UserAgent = *meta.UserAgent
	}
	return actor, a, true
}

// ============================================================
// 投影：库行 → 契约对象
// ============================================================

// adminNodeProjection 是 AdminListNodesPage 与 AdminGetNode **逐字相同**的那份投影。
//
// 两条查询的 SELECT 列表在 SQL 里是刻意逐字一致的（「让列表说在线、详情说离线」
// 这类不一致在结构上不可能出现）。Go 这一侧用同一个中间结构体承接，
// 是把那个不变量继续往上带一层：只要有人改了其中一条查询的投影，
// 下面两个 From 函数里必然有一个编译不过 —— 而不是安静地给出两种展示。
// admin_nodes_test.go 里还有一条反射断言，直接比对两个生成结构体的字段集。
type adminNodeProjection struct {
	ID           int64
	Code         string
	Name         string
	Protocol     dbgen.ServerProtocol
	Host         string
	Port         int32
	Region       string
	Enabled      bool
	Visible      bool
	GroupIds     []int64
	ConfigRev    *int64
	UserRev      *int64
	LastPushAt   pgtype.Timestamptz
	LastStatusAt pgtype.Timestamptz
	CpuPct       *float32
	MemTotal     *int64
	MemUsed      *int64
	SwapTotal    *int64
	SwapUsed     *int64
	DiskTotal    *int64
	DiskUsed     *int64
}

func adminNodeProjectionFromList(r dbgen.AdminListNodesPageRow) adminNodeProjection {
	return adminNodeProjection{
		ID: r.ID, Code: r.Code, Name: r.Name, Protocol: r.Protocol,
		Host: r.Host, Port: r.Port, Region: r.Region,
		Enabled: r.Enabled, Visible: r.Visible, GroupIds: r.GroupIds,
		ConfigRev: r.ConfigRev, UserRev: r.UserRev,
		LastPushAt: r.LastPushAt, LastStatusAt: r.LastStatusAt,
		CpuPct:   r.CpuPct,
		MemTotal: r.MemTotal, MemUsed: r.MemUsed,
		SwapTotal: r.SwapTotal, SwapUsed: r.SwapUsed,
		DiskTotal: r.DiskTotal, DiskUsed: r.DiskUsed,
	}
}

func adminNodeProjectionFromGet(r dbgen.AdminGetNodeRow) adminNodeProjection {
	return adminNodeProjection{
		ID: r.ID, Code: r.Code, Name: r.Name, Protocol: r.Protocol,
		Host: r.Host, Port: r.Port, Region: r.Region,
		Enabled: r.Enabled, Visible: r.Visible, GroupIds: r.GroupIds,
		ConfigRev: r.ConfigRev, UserRev: r.UserRev,
		LastPushAt: r.LastPushAt, LastStatusAt: r.LastStatusAt,
		CpuPct:   r.CpuPct,
		MemTotal: r.MemTotal, MemUsed: r.MemUsed,
		SwapTotal: r.SwapTotal, SwapUsed: r.SwapUsed,
		DiskTotal: r.DiskTotal, DiskUsed: r.DiskUsed,
	}
}

// adminNodeView 把库行渲染成契约的 AdminNode。
//
// 三处刻意的取值，每一处都有一个「换个写法会怎样」：
//
//   - `type` 输出 **servers.protocol 的原值**（vless_reality / vless_xhttp_cdn / …），
//     不折叠成用户面那个 `nodeDisplayType` 的粗粒度名。用户面折叠是因为
//     `vless_xhttp_cdn` 出现在任何登录用户能拉的列表里等于对外宣告「我们正在被封」；
//     后台恰恰相反 —— 它是唯一能看见「这台机器现在跑的是不是应急通路」的地方，
//     压扁成 "vless" 会把这条信息扔掉。契约也没给 type 定 enum。
//
//   - `multiplier_e9` **一律 null**。0004 刻意不建倍率列（product-brief §6 裁定第一阶段
//     不引入），拿别的列凑一个数出来只会让后台显示一个没有任何来源的倍率。
//
//   - `config_rev` / `user_rev` 取 **node_rev 表**（契约 schema 描述里写的
//     `servers.config_rev` 在库里不存在）。LEFT JOIN 缺行时保持 null 而不是补 0：
//     null 的意思是「这台机器建的时候漏了 InitNodeRev，它的 ETag 从此不工作」，
//     补成 0 会把这个故障伪装成「还没 bump 过」。
func adminNodeView(p adminNodeProjection) gen.AdminNode {
	groups := p.GroupIds
	if groups == nil {
		// coalesce 已经保证是 '{}'，这里只防「将来有人改了查询」：
		// nil 序列化成 JSON 的 null，而契约说 group_ids 是数组。
		groups = []int64{}
	}
	n := gen.AdminNode{
		Id:      p.ID,
		Name:    p.Name,
		Type:    string(p.Protocol),
		Enabled: p.Enabled,

		Host:     ptrOf(p.Host),
		Port:     ptrOf(p.Port),
		Region:   ptrOf(p.Region),
		GroupIds: &groups,

		ConfigRev: p.ConfigRev,
		UserRev:   p.UserRev,

		LastPushAt:   tsPtr(p.LastPushAt),
		LastStatusAt: tsPtr(p.LastStatusAt),

		// MultiplierE9 保持 nil，见上面第二条。
	}
	// load_status 只在 server_online_state 真的有那一行时给出。
	// reported_at 是那张表的 NOT NULL 列，所以它的 Valid 就是「有没有这一行」。
	//
	// ⚠️ 没有上报过（或数据库重启后 UNLOGGED 表被 TRUNCATE）时**不给** load_status，
	// 而不是给一份全 0 的。全 0 在后台看起来是「这台机器很空闲」——
	// 恰恰是把「我们不知道它的状态」渲染成了最让人放心的那个样子。
	if p.LastStatusAt.Valid {
		n.LoadStatus = &gen.NodeStatusReport{
			Cpu:  float64(nodeStatOrZero(p.CpuPct, 0)),
			Mem:  gen.NodeResourceUsage{Total: nodeStatOrZero(p.MemTotal, 0), Used: nodeStatOrZero(p.MemUsed, 0)},
			Swap: gen.NodeResourceUsage{Total: nodeStatOrZero(p.SwapTotal, 0), Used: nodeStatOrZero(p.SwapUsed, 0)},
			Disk: gen.NodeResourceUsage{Total: nodeStatOrZero(p.DiskTotal, 0), Used: nodeStatOrZero(p.DiskUsed, 0)},
		}
	}
	return n
}

// nodeStatOrZero 解引用可空列，nil 时给零值。
// 六个资源列全部可空（节点可以只报 cpu 不报磁盘），而契约的 NodeResourceUsage
// 两个字段都是必填非指针 —— 这是契约与库的一处形状差，只能在这里吃掉。
func nodeStatOrZero[T any](p *T, zero T) T {
	if p == nil {
		return zero
	}
	return *p
}

// nodeKeyView 把 server_keys 的一行渲染成契约的 NodeKey。
//
// 🔴 投影里**没有** key_hash，也没有任何能推回明文的东西。这不是"记得别写"，
// 而是 SQL 侧的投影本身就不含那一列 —— 让「不小心把哈希返回出去」需要先改一条查询。
func nodeKeyView(r dbgen.AdminListNodeKeysRow) gen.NodeKey {
	k := gen.NodeKey{
		Id:    r.ID,
		KeyId: r.KeyPrefix,
		Name:  r.Name,
		// created_at ← issued_at（库里没有 created_at 这一列）
		Scopes:     nodeScopesView(r.Scopes),
		LastUsedAt: tsPtr(r.LastUsedAt),
		ExpiresAt:  tsPtr(r.ExpiresAt),
		RevokedAt:  tsPtr(r.RevokedAt),
	}
	if r.IssuedAt.Valid {
		k.CreatedAt = r.IssuedAt.Time.UTC()
	}
	return k
}

// nodeScopesView 把 text[] 转成契约枚举。
//
// **原样透传，不过滤未知值。** 库里如果躺着 0004 那个 DEFAULT `'{uniproxy}'`，
// 后台必须看得见它 —— 过滤掉的话，一把 scope 全错的密钥在后台看起来是「scopes: []」，
// 而空数组像是「还没配」，不像「配错了」。
func nodeScopesView(scopes []string) []gen.NodeScope {
	out := make([]gen.NodeScope, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, gen.NodeScope(s))
	}
	return out
}

// ============================================================
// 1 · ListAdminNodes
// ============================================================

// adminNodePage 是一页节点。
type adminNodePage struct {
	Data       []gen.AdminNode
	NextCursor *string
	HasMore    bool
	Total      *int64
}

// listAdminNodesPage 取一页节点，可选带总数。
//
// 游标键是 **id DESC**，不是 sort_order —— 后者既不唯一又可被后台随手改，
// 拿一个会变、会重复的键做游标，翻页会**静默地跳行或重复行**
// （改完某个节点的 sort_order 之后，正在翻页的人看到的结果集自相矛盾，且没有报错）。
// 代价是后台的节点顺序与用户面不同，这是可接受的：后台看的是「有哪些节点」。
//
// `?count=true` 才跑 COUNT(*)：db-f1-micro 上一次 COUNT 是实打实的开销，
// 不能让每次翻页都付（§2.4 的管理面口径）。
func listAdminNodesPage(ctx context.Context, q adminNodeReader,
	cur *int64, want int, wantCount bool) (adminNodePage, error) {
	var page adminNodePage

	rows, err := q.AdminListNodesPage(ctx, dbgen.AdminListNodesPageParams{
		CursorID:  cur,
		PageLimit: int32(want + 1), // 多取一行判 has_more（§2.4）
	})
	if err != nil {
		return page, fmt.Errorf("查询节点列表失败: %w", err)
	}

	page.HasMore = len(rows) > want
	if page.HasMore {
		rows = rows[:want]
	}
	page.Data = make([]gen.AdminNode, 0, len(rows))
	for i := range rows {
		page.Data = append(page.Data, adminNodeView(adminNodeProjectionFromList(rows[i])))
	}

	if page.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		// 🔴 游标里带 `at` 只是为了与 §2.4 的线格式 `{"id":…,"at":"…"}` 一致
		//    （编解码复用 catalog.go 那一套，一个包里两套游标编解码就是两份真相）。
		//    **排序键只有 id**，所以 SQL 侧只吃 cursor_id —— 别看见 at 就以为可以按时间翻页，
		//    那会在同一秒建的两台节点上跳行。
		c := keysetCursor{ID: ptrOf(last.ID)}
		if last.CreatedAt.Valid {
			c.At = ptrOf(last.CreatedAt.Time.UTC())
		}
		if enc := encodeKeysetCursor(c); enc != "" {
			page.NextCursor = &enc
		} else {
			page.HasMore = false
		}
	}

	if wantCount {
		total, err := q.AdminCountNodesFiltered(ctx)
		if err != nil {
			return page, fmt.Errorf("统计节点总数失败: %w", err)
		}
		page.Total = &total
	}
	return page, nil
}

// ListAdminNodes 实现 GET /api/v1/admin/nodes。
func (s *Server) ListAdminNodes(ctx context.Context, req gen.ListAdminNodesRequestObject) (gen.ListAdminNodesResponseObject, error) {
	if _, _, ok := s.nodeAdminActor(ctx); !ok {
		return gen.ListAdminNodes500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "管理面路由未挂鉴权", errAdminNodeNoAuth),
		}, nil
	}

	want, _ := pageLimit(req.Params.Limit)

	var cur *int64
	if req.Params.Cursor != nil && *req.Params.Cursor != "" {
		c, err := decodeKeysetCursor(*req.Params.Cursor)
		if err != nil || c.ID == nil {
			// 契约给 listAdminNodes 只声明了 403 与 500，没有 400 可用。
			// 退回第一页 + 一条 Warn：500 会把一个客户端问题谎报成服务端故障，
			// 而「翻页按钮好像没反应」这类反馈只能靠这条日志回答（同 catalog.go 的取舍）。
			s.logger.WarnContext(ctx, "后台节点游标非法，按第一页处理",
				"request_id", middleware.RequestIDFrom(ctx))
		} else {
			cur = c.ID
		}
	}

	page, err := listAdminNodesPage(ctx, s.db, cur, want,
		req.Params.Count != nil && bool(*req.Params.Count))
	if err != nil {
		return gen.ListAdminNodes500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取节点列表失败", err),
		}, nil
	}

	meta := s.meta(ctx)
	meta.HasMore = &page.HasMore
	meta.NextCursor = page.NextCursor
	meta.Total = page.Total
	return gen.ListAdminNodes200JSONResponse{Data: page.Data, Meta: meta}, nil
}

// errAdminNodeNoAuth 是装配错误的哨兵，只进日志不进响应体。
var errAdminNodeNoAuth = errors.New("上下文里没有管理员身份（路由未挂 RequireAdmin）")

// ============================================================
// 2 · GetAdminNode
// ============================================================

func getAdminNodeView(ctx context.Context, q adminNodeReader, id int64) (gen.AdminNode, error) {
	row, err := q.AdminGetNode(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.AdminNode{}, errAdminNodeNotFound
		}
		return gen.AdminNode{}, fmt.Errorf("查询节点失败: %w", err)
	}
	return adminNodeView(adminNodeProjectionFromGet(row)), nil
}

// GetAdminNode 实现 GET /api/v1/admin/nodes/{id}。
func (s *Server) GetAdminNode(ctx context.Context, req gen.GetAdminNodeRequestObject) (gen.GetAdminNodeResponseObject, error) {
	if _, _, ok := s.nodeAdminActor(ctx); !ok {
		return gen.GetAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "管理面路由未挂鉴权", errAdminNodeNoAuth),
		}, nil
	}
	node, err := getAdminNodeView(ctx, s.db, req.Id)
	switch {
	case errors.Is(err, errAdminNodeNotFound):
		return gen.GetAdminNode404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "节点不存在")}, nil
	case err != nil:
		return gen.GetAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取节点详情失败", err),
		}, nil
	}
	return gen.GetAdminNode200JSONResponse{Data: node, Meta: s.meta(ctx)}, nil
}

// ============================================================
// 3 · CreateAdminNode（D9）
// ============================================================

// adminNodeUpsertInput 是校验通过之后的 upsert 入参。
//
// 每个字段都是**已经决定好的值**（可选字段已经在校验阶段回填完毕），
// 目的是让事务闭包里不再有任何「这个字段调用方到底给没给」的分支 ——
// 那种分支正是 PATCH 抹字段的来源。
type adminNodeUpsertInput struct {
	Name     string
	Protocol dbgen.ServerProtocol
	Host     string
	Port     int32
	Region   string
	Enabled  bool
	// GroupIDs 为 nil 表示「调用方没提分组」= 不动分组（PATCH 语义）。
	// 空切片表示「清空分组」，与 nil 是两件事。
	GroupIDs []int64
	Reason   string
}

// parseNodeProtocol 把契约的自由字符串 `type` 映射成 servers.protocol 枚举。
//
// 🔴 只接受库里那四个枚举值的**逐字**写法，不接受用户面那套折叠名（vless / hysteria）。
// 「vless」在库里对应两个值（vless_reality 与 vless_xhttp_cdn），替调用方猜一个
// 的后果是：本想建一台 REALITY 节点，建出来的是应急 CDN 通路，而两者的
// protocol_settings 完全不同 —— 节点拉到配置后起不来，报的是协议层的错，
// 没有人会往「后台把类型猜错了」上想。
func parseNodeProtocol(v string) (dbgen.ServerProtocol, bool) {
	switch dbgen.ServerProtocol(strings.TrimSpace(v)) {
	case dbgen.ServerProtocolVlessReality:
		return dbgen.ServerProtocolVlessReality, true
	case dbgen.ServerProtocolHysteria2:
		return dbgen.ServerProtocolHysteria2, true
	case dbgen.ServerProtocolShadowsocks2022:
		return dbgen.ServerProtocolShadowsocks2022, true
	case dbgen.ServerProtocolVlessXhttpCdn:
		return dbgen.ServerProtocolVlessXhttpCdn, true
	default:
		return "", false
	}
}

// validateNodeUpsert 校验 AdminNodeUpsert 并落成 adminNodeUpsertInput。
//
// `cur` 为 nil 表示新建（所有可选字段必须自带值），非 nil 表示 PATCH ——
// 🔴 **未提供的字段一律回填 cur 的当前值**。理由见文件头第 1 条：
// `AdminUpdateNode` 无条件写六列，照零值写下去，一次改名会把 host 抹空、把节点停掉。
func validateNodeUpsert(body *gen.AdminNodeUpsert, cur *adminNodeProjection) (adminNodeUpsertInput, []gen.ErrorDetail) {
	var in adminNodeUpsertInput
	var details []gen.ErrorDetail

	if body == nil {
		return in, []gen.ErrorDetail{detail("body", "请求体不能为空")}
	}

	// L2：reason ≥ 8 字符，进审计日志。
	if d := checkAdminNodeReason(body.Reason); d != nil {
		details = append(details, *d)
	}
	in.Reason = strings.TrimSpace(body.Reason)

	in.Name = strings.TrimSpace(body.Name)
	if in.Name == "" {
		details = append(details, detail("name", "必填"))
	}

	if p, ok := parseNodeProtocol(body.Type); ok {
		in.Protocol = p
	} else {
		details = append(details, detail("type",
			"必须是 vless_reality / hysteria2 / shadowsocks2022 / vless_xhttp_cdn 之一（不接受 vless 这类折叠名：它对应两个不同的协议）"))
	}

	switch {
	case body.Host != nil:
		in.Host = strings.TrimSpace(*body.Host)
		if in.Host == "" {
			details = append(details, detail("host", "不能为空串（servers.host 是 NOT NULL，空串会让节点无法被连接且不报错）"))
		}
	case cur != nil:
		in.Host = cur.Host
	default:
		details = append(details, detail("host", "新建节点必填"))
	}

	switch {
	case body.Port != nil:
		in.Port = *body.Port
	case cur != nil:
		in.Port = cur.Port
	default:
		details = append(details, detail("port", "新建节点必填"))
	}
	// 端口范围在这里判而不是留给 DB 的 CHECK：CHECK 违反在 pgx 侧是一个
	// 23514 错误，落到 handler 只能是 500 —— 把一次「填错端口」谎报成服务端故障。
	if in.Port < 1 || in.Port > 65535 {
		details = append(details, detail("port", "必须在 1–65535 之间"))
	}

	switch {
	case body.Region != nil:
		in.Region = strings.TrimSpace(*body.Region)
	case cur != nil:
		in.Region = cur.Region
	default:
		// region 是 NOT NULL 但没有 CHECK，空串合法。新建时不强制：
		// 它只影响后台展示，为它挡住一次建节点是把代价放错了地方。
		in.Region = ""
	}

	switch {
	case body.Enabled != nil:
		in.Enabled = *body.Enabled
	case cur != nil:
		// 🔴 这一支是本函数存在的主要理由。PATCH 不带 enabled 时保持原值 ——
		//    照零值写 false 会让「改一下节点名」变成「把这台机器上的人全踢下线」。
		in.Enabled = cur.Enabled
	default:
		// 新建默认**停用**：一台刚建好的节点还没有密钥、GCE 实例大概率也还没起，
		// 直接 enabled=true 会让它立刻进用户订阅（servers_visible_idx 要求
		// visible AND enabled），用户连过去只能连失败。
		in.Enabled = false
	}

	if body.GroupIds != nil {
		in.GroupIDs = dedupeNodeGroupIDs(*body.GroupIds)
		for _, g := range in.GroupIDs {
			if g <= 0 {
				details = append(details, detail("group_ids", "分组 id 必须是正整数"))
				break
			}
		}
	} else if cur == nil {
		// 新建时不给分组 = 不属于任何分组。合法（server_group_map 允许），
		// 但这台节点对**所有**用户不可见，直到有人给它分组。
		in.GroupIDs = []int64{}
	}

	// multiplier_e9 收到了也一律忽略：库里没有倍率列（0004 刻意不建）。
	// 不报 422 是因为契约里它是合法字段，拒绝一个合法字段会让前端无从下手；
	// 但也绝不假装写进去了 —— 响应里它恒为 null，前端能看出没生效。

	if len(details) > 0 {
		return in, details
	}
	return in, nil
}

// dedupeNodeGroupIDs 去重并排序。
// 分组 id 重复会让 AddServerToGroup 白跑一次（ON CONFLICT DO NOTHING 吃掉），
// 但也会让 BumpUserRevByGroup 对同一个分组 bump 两次 —— 那不是错误，只是浪费；
// 真正的收益是让审计快照里的 group_ids 有稳定顺序，便于前后像对比。
func dedupeNodeGroupIDs(in []int64) []int64 {
	seen := make(map[int64]bool, len(in))
	out := make([]int64, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// generateNodeCode 由节点名（退而求其次：region / host）推出 servers.code。
//
// 🔴 **这是一处契约缺口的兜底，不是一个好设计。**
// `servers.code` 是 NOT NULL UNIQUE，0004 的列注释写明「与 GCE 实例名一致」——
// 它是节点记录与那台虚拟机之间**唯一**的对应关系。而 `AdminNodeUpsert` 里没有 code 字段，
// AdminUpdateNode 也刻意不改它，也就是说这里生成什么，就永远是什么。
//
// 所以规则必须是**确定的**（同样的名字永远得到同样的 code），不能掺随机后缀：
// 掺了随机数，运维就没有任何办法让 code 与他刚创建的那台 `bp-node-hk1` 对上。
// 撞车时返回 422 让人改名，而不是自动加后缀 —— 一个自动生成的 `bp-node-hk1-x7f2`
// 看起来成功了，但它与任何一台 GCE 实例都不对应，且再也改不回来。
//
// 生成不出可用 slug（名字全是中文、region 与 host 也为空）时返回 false，
// 调用方回 422 说明「节点名需要含 ASCII 字母或数字」。这看着苛刻，但方向是对的：
// GCE 实例名本来就只能是 ASCII，一个纯中文名注定与实例名对不上。
func generateNodeCode(name, region, host string) (string, bool) {
	for _, src := range []string{name, region, host} {
		if slug := nodeCodeSlug(src); slug != "" {
			return "bp-node-" + slug, true
		}
	}
	return "", false
}

// nodeCodeSlug 把任意串压成 `[a-z0-9-]`，折叠连续分隔符，长度上限 40。
func nodeCodeSlug(s string) string {
	var b strings.Builder
	prevDash := true // 前导 '-' 也要吃掉
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		default:
			// 非 ASCII（中文等）直接丢弃：转写成拼音会让 code 依赖一张
			// 会变的转写表，而 code 一旦定下就不能改。
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// createAdminNode 在一个事务里建节点、初始化版本号、挂分组，并写审计。
//
// ⚠️ 三件事缺一件都是静默故障：
//   - 漏 `InitNodeRev`：node_rev 里没有这台机器的行，它的 ETag 从此不工作
//     （每次 /config 与 /user 都是全量 200，且没有任何报错）；
//   - 漏 `BumpUserRevByGroup`：server_group_map 上**没有触发器**（0012 的触发器只挂在
//     users 上），分组变了但同组节点的 user_rev 没动，它们会继续 304 返回旧用户列表；
//   - 漏审计：audit.InTx 的签名不给这个出口。
func createAdminNode(ctx context.Context, tx adminNodeTxRunner, actor audit.Actor,
	in adminNodeUpsertInput, code string) (gen.AdminNode, error) {
	var out gen.AdminNode

	err := tx.Run(ctx, actor, func(ctx context.Context, q adminNodeWriter) (audit.Entry, error) {
		srv, err := q.CreateServer(ctx, dbgen.CreateServerParams{
			Code:     code,
			Name:     in.Name,
			Protocol: in.Protocol,
			Host:     in.Host,
			Port:     in.Port,
			Region:   in.Region,
			Enabled:  in.Enabled,
			// visible 恒为 true，enabled 才是开关。
			//
			// 🔴 理由是「哪个开关有 API」：`servers_visible_idx` 要求 visible AND enabled，
			//    而契约里能改的只有 enabled（enable/disable 两个端点）——
			//    visible 建成 false 就再也没有任何 API 能把它改回来，
			//    于是通过后台建出来的节点**永远不会出现在任何人的订阅里**，且不报错。
			Visible: true,

			// 🔴 `protocol_settings` 与 `tags` 必须显式给**空值而不是 nil**。
			//    两列都是 `NOT NULL DEFAULT`（0004），但 `CreateServer` 的 INSERT
			//    把它们写成了占位参数 —— 也就是说 DEFAULT **不会生效**，
			//    pgx 会把 Go 的 nil 切片编码成 SQL NULL，于是每一次建节点都撞
			//    NOT NULL 约束、以 500 收场。给 `{}` 是在补 DEFAULT 那一层。
			ProtocolSettings: []byte("{}"),
			Tags:             []string{},

			// server_port / parent_id / sort_order 留零值（前两者是可空列）：
			// 契约里没有这些字段，建完之后由运维在库里补
			// （AdminUpdateNode 同样刻意不碰它们，见那条查询的注释）。
			SortOrder: 0,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return audit.Entry{}, fmt.Errorf("%w: %s", errAdminNodeCodeTaken, code)
			}
			return audit.Entry{}, fmt.Errorf("创建节点失败: %w", err)
		}

		if _, err := q.InitNodeRev(ctx, srv.ID); err != nil {
			return audit.Entry{}, fmt.Errorf("初始化节点版本号失败: %w", err)
		}

		for _, g := range in.GroupIDs {
			if err := q.AddServerToGroup(ctx, dbgen.AddServerToGroupParams{ServerID: srv.ID, GroupID: g}); err != nil {
				if isNodeGroupFKViolation(err) {
					return audit.Entry{}, fmt.Errorf("%w: group_id=%d", errAdminNodeBadGroup, g)
				}
				return audit.Entry{}, fmt.Errorf("挂载节点分组失败: %w", err)
			}
			if err := q.BumpUserRevByGroup(ctx, g); err != nil {
				return audit.Entry{}, fmt.Errorf("bump 分组用户版本号失败: %w", err)
			}
		}

		// 用与 GetAdminNode 逐字相同的投影读回来，而不是拿 CreateServer 的返回值拼 ——
		// 后者没有 group_ids / config_rev / 在线态，拼出来的 201 与随后一次 GET
		// 会长得不一样，而「创建后立刻刷新页面，字段变了」是最难查的一类 bug。
		row, err := q.AdminGetNode(ctx, srv.ID)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("回读新建节点失败: %w", err)
		}
		out = adminNodeView(adminNodeProjectionFromGet(row))

		return audit.Entry{
			Action:     "D9.node.create",
			TargetType: "node",
			TargetID:   strconv.FormatInt(srv.ID, 10),
			// 创建操作没有 before（nil → SQL NULL，不是 JSON 的 null 字面量）。
			After: map[string]any{
				"id": srv.ID, "code": srv.Code, "name": srv.Name,
				"protocol": string(srv.Protocol), "host": srv.Host, "port": srv.Port,
				"region": srv.Region, "enabled": srv.Enabled, "visible": srv.Visible,
				"group_ids": in.GroupIDs,
			},
			Reason: in.Reason,
		}, nil
	})
	if err != nil {
		return gen.AdminNode{}, err
	}
	return out, nil
}

// CreateAdminNode 实现 POST /api/v1/admin/nodes。
func (s *Server) CreateAdminNode(ctx context.Context, req gen.CreateAdminNodeRequestObject) (gen.CreateAdminNodeResponseObject, error) {
	actor, admin, ok := s.nodeAdminActor(ctx)
	if !ok {
		return gen.CreateAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "管理面路由未挂鉴权", errAdminNodeNoAuth),
		}, nil
	}
	// L4
	if !adminCanWriteNodes(admin.Role) {
		return gen.CreateAdminNode403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED, "当前角色不能写节点"),
		}, nil
	}

	// L2 + 字段校验（新建：cur 为 nil，可选字段没有回填来源）
	in, details := validateNodeUpsert(req.Body, nil)
	if len(details) > 0 {
		return gen.CreateAdminNode422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求参数不合法", details...),
		}, nil
	}

	code, ok := generateNodeCode(in.Name, in.Region, in.Host)
	if !ok {
		return gen.CreateAdminNode422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "无法为该节点生成 code", detail("name",
				"需含 ASCII 字母或数字：servers.code（与 GCE 实例名一致）只能由 name / region / host 生成，而契约的 AdminNodeUpsert 没有 code 字段")),
		}, nil
	}

	node, err := createAdminNode(ctx, s.nodeTx(), actor, in, code)
	switch {
	case errors.Is(err, errAdminNodeCodeTaken):
		return gen.CreateAdminNode422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("节点 code %q 已被占用（含已软删的节点：code 的唯一约束没有排除 deleted_at）", code),
				detail("name", "换一个名字")),
		}, nil
	case errors.Is(err, errAdminNodeBadGroup):
		return gen.CreateAdminNode422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "分组不存在", detail("group_ids", err.Error())),
		}, nil
	case err != nil:
		return gen.CreateAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "创建节点失败", err),
		}, nil
	}

	s.logger.InfoContext(ctx, "后台新建节点",
		"node_id", node.Id, "code", code, "admin_id", admin.AdminID,
		"request_id", middleware.RequestIDFrom(ctx))

	resp := gen.CreateAdminNode201JSONResponse{
		Headers: gen.CreateAdminNode201ResponseHeaders{
			Location: fmt.Sprintf("/api/v1/admin/nodes/%d", node.Id),
		},
	}
	resp.Body.Data = node
	resp.Body.Meta = s.meta(ctx)
	return resp, nil
}

// ============================================================
// 4 · UpdateAdminNode（D9）
// ============================================================

// updateAdminNode 改节点：回填未提供字段 → 写 → bump config_rev → 调分组 → 审计。
//
// 🔴 `BumpConfigRev` 与业务写入必须在同一事务：不 bump 的话节点会一直拿到旧配置的 304，
// **改了等于没改**，而后台显示的是新值 —— 一个「明明改了但不生效」的故障，
// 且两边都不报错。
func updateAdminNode(ctx context.Context, tx adminNodeTxRunner, actor audit.Actor,
	id int64, body *gen.AdminNodeUpsert) (gen.AdminNode, []gen.ErrorDetail, error) {
	var out gen.AdminNode
	var details []gen.ErrorDetail

	err := tx.Run(ctx, actor, func(ctx context.Context, q adminNodeWriter) (audit.Entry, error) {
		// 🔴 先在**事务内**读当前值。放在事务外读会有窗口：
		//    另一个管理员在窗口里改了 host，我们随后用他改之前的值回填自己的 PATCH，
		//    结果是把他的修改静默地回滚掉。
		before, err := q.AdminGetNode(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, errAdminNodeNotFound
			}
			return audit.Entry{}, fmt.Errorf("读取节点当前值失败: %w", err)
		}
		cur := adminNodeProjectionFromGet(before)

		in, ds := validateNodeUpsert(body, &cur)
		if len(ds) > 0 {
			details = ds
			// 返回 error 让事务回滚。此刻还没有任何写入，回滚是空操作 ——
			// 但让"校验失败"也走 return err，是为了不给「校验没过却继续往下写」留形状。
			return audit.Entry{}, errAdminNodeInvalidUpsert
		}

		row, err := q.AdminUpdateNode(ctx, dbgen.AdminUpdateNodeParams{
			ServerID: id,
			Name:     in.Name,
			Protocol: in.Protocol,
			Host:     in.Host,
			Port:     in.Port,
			Region:   in.Region,
			Enabled:  in.Enabled,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, errAdminNodeNotFound
			}
			return audit.Entry{}, fmt.Errorf("更新节点失败: %w", err)
		}

		if _, err := q.BumpConfigRev(ctx, id); err != nil {
			return audit.Entry{}, fmt.Errorf("bump 节点配置版本号失败: %w", err)
		}

		// 分组差异。body.GroupIds 为 nil 时 in.GroupIDs 也是 nil = 不动分组。
		afterGroups := cur.GroupIds
		if in.GroupIDs != nil {
			added, removed := diffNodeGroupIDs(cur.GroupIds, in.GroupIDs)
			for _, g := range added {
				if err := q.AddServerToGroup(ctx, dbgen.AddServerToGroupParams{ServerID: id, GroupID: g}); err != nil {
					if isNodeGroupFKViolation(err) {
						return audit.Entry{}, fmt.Errorf("%w: group_id=%d", errAdminNodeBadGroup, g)
					}
					return audit.Entry{}, fmt.Errorf("挂载节点分组失败: %w", err)
				}
			}
			for _, g := range removed {
				if err := q.RemoveServerFromGroup(ctx, dbgen.RemoveServerFromGroupParams{ServerID: id, GroupID: g}); err != nil {
					return audit.Entry{}, fmt.Errorf("摘除节点分组失败: %w", err)
				}
			}
			// 🔴 **加进来的和摘出去的分组都要 bump**：摘出去的那个分组里的其它节点，
			//    可见用户集合同样变了（少了这台机器上的用户视角），不 bump 它们会继续 304。
			for _, g := range append(append([]int64{}, added...), removed...) {
				if err := q.BumpUserRevByGroup(ctx, g); err != nil {
					return audit.Entry{}, fmt.Errorf("bump 分组用户版本号失败: %w", err)
				}
			}
			afterGroups = in.GroupIDs
		}

		// 与 GetAdminNode 同一份投影回读（同 createAdminNode 的理由）。
		fresh, err := q.AdminGetNode(ctx, id)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("回读节点失败: %w", err)
		}
		out = adminNodeView(adminNodeProjectionFromGet(fresh))

		// 前后像走"写法甲"：`AdminUpdateNode` 一条语句同时给出 prev.* 与新值，
		// 两者之间没有窗口（FROM 侧读的是本语句开始时的快照）。
		return audit.Entry{
			Action:     "D9.node.update",
			TargetType: "node",
			TargetID:   strconv.FormatInt(id, 10),
			Before: map[string]any{
				"name": row.BeforeName, "protocol": string(row.BeforeProtocol),
				"host": row.BeforeHost, "port": row.BeforePort,
				"region": row.BeforeRegion, "enabled": row.BeforeEnabled,
				"group_ids": cur.GroupIds,
			},
			After: map[string]any{
				"name": row.Name, "protocol": string(row.Protocol),
				"host": row.Host, "port": row.Port,
				"region": row.Region, "enabled": row.Enabled,
				"group_ids": afterGroups,
			},
			Reason: in.Reason,
		}, nil
	})
	if err != nil {
		return gen.AdminNode{}, details, err
	}
	return out, nil, nil
}

// errAdminNodeInvalidUpsert 只在 updateAdminNode 内部用：把「校验没过」变成一个
// 能触发回滚的 error，真正给调用方的信息在 details 里。
var errAdminNodeInvalidUpsert = errors.New("节点参数不合法")

// diffNodeGroupIDs 求 want 相对 have 的增删。两侧都已去重。
func diffNodeGroupIDs(have, want []int64) (added, removed []int64) {
	inHave := make(map[int64]bool, len(have))
	for _, v := range have {
		inHave[v] = true
	}
	inWant := make(map[int64]bool, len(want))
	for _, v := range want {
		inWant[v] = true
	}
	for _, v := range want {
		if !inHave[v] {
			added = append(added, v)
		}
	}
	for _, v := range have {
		if !inWant[v] {
			removed = append(removed, v)
		}
	}
	return added, removed
}

// UpdateAdminNode 实现 PATCH /api/v1/admin/nodes/{id}。
func (s *Server) UpdateAdminNode(ctx context.Context, req gen.UpdateAdminNodeRequestObject) (gen.UpdateAdminNodeResponseObject, error) {
	actor, admin, ok := s.nodeAdminActor(ctx)
	if !ok {
		return gen.UpdateAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "管理面路由未挂鉴权", errAdminNodeNoAuth),
		}, nil
	}
	if !adminCanWriteNodes(admin.Role) {
		return gen.UpdateAdminNode403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED, "当前角色不能写节点"),
		}, nil
	}
	if req.Body == nil {
		return gen.UpdateAdminNode422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空"),
		}, nil
	}

	node, details, err := updateAdminNode(ctx, s.nodeTx(), actor, req.Id, req.Body)
	switch {
	case errors.Is(err, errAdminNodeInvalidUpsert):
		return gen.UpdateAdminNode422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求参数不合法", details...),
		}, nil
	case errors.Is(err, errAdminNodeNotFound):
		return gen.UpdateAdminNode404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "节点不存在")}, nil
	case errors.Is(err, errAdminNodeBadGroup):
		return gen.UpdateAdminNode422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "分组不存在", detail("group_ids", err.Error())),
		}, nil
	case err != nil:
		return gen.UpdateAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "更新节点失败", err),
		}, nil
	}
	return gen.UpdateAdminNode200JSONResponse{Data: node, Meta: s.meta(ctx)}, nil
}

// ============================================================
// 5 · DeleteAdminNode（D4：L1 确认串 + L2 原因）
// ============================================================

// adminNodeDangerFacts 是删节点确认框必须显示的事实。
//
// 🔴 **两个在线人数刻意都给，而且刻意不合并**（page-inventory §4.4 D4）：
//   - reported 是节点自己上报的。节点已经失联时它是一个不再变化的旧值，
//     而 server_online_state 是 UNLOGGED 表，数据库重启后它是 **0** ——
//     「0 人在线」恰恰是让运维放心点下删除的那个数字。
//   - observed 是我们自己观测到的（user_device_state 近 2 分钟去重用户数）。
//     它同样会偏小（alivelist 拉取失败时 v2node 静默降级为「零在线设备」，B16 实证）。
//
// 两个数的**偏差本身**才是信号：差很多 = 这台机器的状态不可信，别删。
// 合并成一个「在线人数」等于把这条信息扔掉。
//
// active_key_count 一并给：删节点会 CASCADE 掉它的 server_keys（0004 的外键），
// 也就是说这些密钥会无声消失。
type adminNodeDangerFacts struct {
	Name                string
	ReportedOnlineUsers int32
	ObservedOnlineUsers int64
	ActiveKeyCount      int64
}

// deleteAdminNode 软删节点。
//
// L1 的比对**在事务里、在 FOR UPDATE 之后**做：`AdminGetNodeForDangerOp` 锁住 servers
// 那一行，所以我们比对的名字与随后被删的行是同一个版本。
// 先在事务外查一次名字再进事务删，中间的窗口足够别人改名 —— 于是确认串校验的是
// 一个已经不存在的名字，而删掉的是另一台机器。
func deleteAdminNode(ctx context.Context, tx adminNodeTxRunner, actor audit.Actor,
	id int64, confirmation, reason string) (adminNodeDangerFacts, error) {
	var facts adminNodeDangerFacts

	err := tx.Run(ctx, actor, func(ctx context.Context, q adminNodeWriter) (audit.Entry, error) {
		pre, err := q.AdminGetNodeForDangerOp(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, errAdminNodeNotFound
			}
			return audit.Entry{}, fmt.Errorf("读取节点危险操作前置事实失败: %w", err)
		}
		facts = adminNodeDangerFacts{
			Name:                pre.Name,
			ReportedOnlineUsers: pre.ReportedOnlineUsers,
			ObservedOnlineUsers: pre.ObservedOnlineUsers,
			ActiveKeyCount:      pre.ActiveKeyCount,
		}

		// L1：期望值是**服务端查出来的**节点名。
		if !adminNodeConfirmMatches(pre.Name, confirmation) {
			return audit.Entry{}, errAdminNodeConfirmMismatch
		}

		row, err := q.AdminSoftDeleteNode(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// FOR UPDATE 之后仍然 0 行：另一个事务在我们拿锁之前已经删过了。
				return audit.Entry{}, errAdminNodeNotFound
			}
			return audit.Entry{}, fmt.Errorf("删除节点失败: %w", err)
		}

		return audit.Entry{
			Action:     "D4.node.delete",
			TargetType: "node",
			TargetID:   strconv.FormatInt(id, 10),
			Before: map[string]any{
				"name": row.BeforeName, "enabled": row.BeforeEnabled, "visible": row.BeforeVisible,
				// 两个在线人数与密钥数进快照：事后追责时「删的时候上面有没有人」
				// 只有这一处记录（server_online_state 是 UNLOGGED，重启即失）。
				"reported_online_users": pre.ReportedOnlineUsers,
				"observed_online_users": pre.ObservedOnlineUsers,
				"active_key_count":      pre.ActiveKeyCount,
			},
			After: map[string]any{
				"name": row.Name, "enabled": row.Enabled, "visible": row.Visible,
				"deleted_at": tsPtr(row.DeletedAt),
			},
			Reason: reason,
		}, nil
	})
	return facts, err
}

// DeleteAdminNode 实现 DELETE /api/v1/admin/nodes/{id}。
func (s *Server) DeleteAdminNode(ctx context.Context, req gen.DeleteAdminNodeRequestObject) (gen.DeleteAdminNodeResponseObject, error) {
	actor, admin, ok := s.nodeAdminActor(ctx)
	if !ok {
		return gen.DeleteAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "管理面路由未挂鉴权", errAdminNodeNoAuth),
		}, nil
	}
	if !adminCanWriteNodes(admin.Role) {
		return gen.DeleteAdminNode403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED, "当前角色不能写节点"),
		}, nil
	}
	if req.Body == nil {
		return gen.DeleteAdminNode422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空",
				detail("confirmation", "必填，且必须等于该节点的 name"),
				detail("reason", "必填，且至少 8 个字符")),
		}, nil
	}
	// L2 先判：reason 不合格就没必要去动数据库，也不必让人先猜对确认串。
	if d := checkAdminNodeReason(req.Body.Reason); d != nil {
		return gen.DeleteAdminNode422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求参数不合法", *d),
		}, nil
	}

	facts, err := deleteAdminNode(ctx, s.nodeTx(), actor, req.Id,
		req.Body.Confirmation, strings.TrimSpace(req.Body.Reason))
	switch {
	case errors.Is(err, errAdminNodeNotFound):
		return gen.DeleteAdminNode404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "节点不存在")}, nil
	case errors.Is(err, errAdminNodeConfirmMismatch):
		// 🔴 422 的 message 必须把危害说清楚：删除会让这台机器上的用户在 ≤ 60 秒内
		//    全部断线，并 CASCADE 掉它的全部密钥。两个在线人数分开报，理由见
		//    adminNodeDangerFacts 的注释。
		return gen.DeleteAdminNode422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("确认串不匹配。删除节点会让该节点上的用户在 60 秒内全部断线，"+
					"并连带删除它的 %d 把有效密钥；当前节点上报在线 %d 人、我们观测到 %d 人"+
					"（两个数差得多说明这台机器状态不可信，不要删）",
					facts.ActiveKeyCount, facts.ReportedOnlineUsers, facts.ObservedOnlineUsers),
				detail("confirmation", "必须逐字等于该节点的 name")),
		}, nil
	case err != nil:
		return gen.DeleteAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "删除节点失败", err),
		}, nil
	}

	s.logger.WarnContext(ctx, "后台删除节点",
		"node_id", req.Id, "admin_id", admin.AdminID,
		"reported_online_users", facts.ReportedOnlineUsers,
		"observed_online_users", facts.ObservedOnlineUsers,
		"active_key_count", facts.ActiveKeyCount,
		"request_id", middleware.RequestIDFrom(ctx))

	return gen.DeleteAdminNode204Response{
		Headers: gen.DeleteAdminNode204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}, nil
}

// ============================================================
// 6 / 7 · EnableAdminNode / DisableAdminNode
// ============================================================

// setAdminNodeEnabled 启用或停用节点（一条查询服务两个 operation）。
//
// 审计记 before_enabled：「停用一台本来就停着的节点」与「停用一台在跑的节点」
// 是两件事（后者会让人掉线，前者不会），事后只有这一列能分辨。
func setAdminNodeEnabled(ctx context.Context, tx adminNodeTxRunner, actor audit.Actor,
	id int64, enabled bool) (gen.AdminNode, bool, error) {
	var out gen.AdminNode
	var beforeEnabled bool

	err := tx.Run(ctx, actor, func(ctx context.Context, q adminNodeWriter) (audit.Entry, error) {
		row, err := q.AdminSetNodeEnabled(ctx, dbgen.AdminSetNodeEnabledParams{ServerID: id, Enabled: enabled})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, errAdminNodeNotFound
			}
			return audit.Entry{}, fmt.Errorf("切换节点启用状态失败: %w", err)
		}
		beforeEnabled = row.BeforeEnabled

		fresh, err := q.AdminGetNode(ctx, id)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("回读节点失败: %w", err)
		}
		out = adminNodeView(adminNodeProjectionFromGet(fresh))

		action := "node.enable"
		if !enabled {
			// 停用是 D4 那一类（会让人掉线），启用不是 —— action 前缀把这个区别
			// 带进审计表，让「查所有 D4」这类检索能命中它。
			action = "D4.node.disable"
		}
		return audit.Entry{
			Action:     action,
			TargetType: "node",
			TargetID:   strconv.FormatInt(id, 10),
			Before:     map[string]any{"enabled": row.BeforeEnabled},
			After:      map[string]any{"enabled": row.AfterEnabled},
			// 🔴 Reason 为空：契约给 enable/disable **没有请求体**，L2 无从取值。
			//    绝不编一句「管理员操作」塞进去 —— 一条编造的原因比没有原因更坏，
			//    它会让事后读审计的人以为当时真的有人给过理由。缺口已登记。
		}, nil
	})
	if err != nil {
		return gen.AdminNode{}, false, err
	}
	return out, beforeEnabled, nil
}

// EnableAdminNode 实现 POST /api/v1/admin/nodes/{id}/enable。
func (s *Server) EnableAdminNode(ctx context.Context, req gen.EnableAdminNodeRequestObject) (gen.EnableAdminNodeResponseObject, error) {
	actor, admin, ok := s.nodeAdminActor(ctx)
	if !ok {
		return gen.EnableAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "管理面路由未挂鉴权", errAdminNodeNoAuth),
		}, nil
	}
	if !adminCanWriteNodes(admin.Role) {
		return gen.EnableAdminNode403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED, "当前角色不能写节点"),
		}, nil
	}

	node, _, err := setAdminNodeEnabled(ctx, s.nodeTx(), actor, req.Id, true)
	switch {
	case errors.Is(err, errAdminNodeNotFound):
		return gen.EnableAdminNode404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "节点不存在")}, nil
	case err != nil:
		return gen.EnableAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "启用节点失败", err),
		}, nil
	}
	return gen.EnableAdminNode200JSONResponse{Data: node, Meta: s.meta(ctx)}, nil
}

// DisableAdminNode 实现 POST /api/v1/admin/nodes/{id}/disable。
//
// ⚠️ 停用会让该节点上的在线用户在 **≤ 60 秒内掉线**（节点每 60 秒拉一次 /config，
// 拿到 enabled=false 就停止服务）。page-inventory 把它归在 D4，
// 但契约给这个 operation **没有请求体**，也没有 422 出口 —— 于是 L1（确认串）与
// L2（原因）在这里无法实现。本 handler 只能做 L4 + 审计，并把这条缺口如实登记。
func (s *Server) DisableAdminNode(ctx context.Context, req gen.DisableAdminNodeRequestObject) (gen.DisableAdminNodeResponseObject, error) {
	actor, admin, ok := s.nodeAdminActor(ctx)
	if !ok {
		return gen.DisableAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "管理面路由未挂鉴权", errAdminNodeNoAuth),
		}, nil
	}
	if !adminCanWriteNodes(admin.Role) {
		return gen.DisableAdminNode403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.AUTHPERMISSIONDENIED, "当前角色不能写节点"),
		}, nil
	}

	node, wasEnabled, err := setAdminNodeEnabled(ctx, s.nodeTx(), actor, req.Id, false)
	switch {
	case errors.Is(err, errAdminNodeNotFound):
		return gen.DisableAdminNode404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "节点不存在")}, nil
	case err != nil:
		return gen.DisableAdminNode500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "停用节点失败", err),
		}, nil
	}
	if wasEnabled {
		// 只在**真的从启用变成停用**时告警：重复停用一台已经停着的节点不会让任何人掉线，
		// 两者用同一条日志会让告警里全是噪声。
		s.logger.WarnContext(ctx, "后台停用节点，该节点上的用户将在 60 秒内断线",
			"node_id", req.Id, "admin_id", admin.AdminID,
			"request_id", middleware.RequestIDFrom(ctx))
	}
	return gen.DisableAdminNode200JSONResponse{Data: node, Meta: s.meta(ctx)}, nil
}

// ============================================================
// 8 · ListAdminNodeKeys
// ============================================================

// listAdminNodeKeysView 列出一个节点的全部密钥（含已吊销的）。
//
// 含已吊销是刻意的（查询里没有 `revoked_at IS NULL`）：后台要能回答
// 「上个月那把是谁吊的、什么时候吊的」，而 revoked_reason 只在这张表里。
func listAdminNodeKeysView(ctx context.Context, q adminNodeReader, nodeID int64) ([]gen.NodeKey, error) {
	// 先确认节点存在：契约给这个 operation 声明了 404，而
	// AdminListNodeKeys 对一个不存在的节点返回的是空列表 ——
	// 直接返回 200 + [] 会让「节点 id 打错了」看起来像「这台机器没有密钥」，
	// 而后者会诱导运维去签发一把新密钥（签给一个不存在的节点，还会 FK 失败）。
	if _, err := q.AdminGetNode(ctx, nodeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errAdminNodeNotFound
		}
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	rows, err := q.AdminListNodeKeys(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("查询节点密钥失败: %w", err)
	}
	out := make([]gen.NodeKey, 0, len(rows))
	for i := range rows {
		out = append(out, nodeKeyView(rows[i]))
	}
	return out, nil
}

// ListAdminNodeKeys 实现 GET /api/v1/admin/nodes/{id}/keys。
func (s *Server) ListAdminNodeKeys(ctx context.Context, req gen.ListAdminNodeKeysRequestObject) (gen.ListAdminNodeKeysResponseObject, error) {
	if _, _, ok := s.nodeAdminActor(ctx); !ok {
		return gen.ListAdminNodeKeys500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "管理面路由未挂鉴权", errAdminNodeNoAuth),
		}, nil
	}
	keys, err := listAdminNodeKeysView(ctx, s.db, req.Id)
	switch {
	case errors.Is(err, errAdminNodeNotFound):
		return gen.ListAdminNodeKeys404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "节点不存在")}, nil
	case err != nil:
		return gen.ListAdminNodeKeys500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "读取节点密钥失败", err),
		}, nil
	}
	return gen.ListAdminNodeKeys200JSONResponse{Data: keys, Meta: s.meta(ctx)}, nil
}

// ============================================================
// 9 · CreateAdminNodeKey（D5 第 1 步）
// ============================================================

// issuedNodeKey 是一次签发的产物。
//
// Secret 是**明文**，只能走到 201 响应体，绝不进日志、库、审计。
type issuedNodeKey struct {
	KeyID  string // 六字符 base32，写进 server_keys.key_prefix
	Secret string // 完整密钥串 bpn_<key_id>_<secret>
	Hash   []byte // sha256(pepper + Secret)
}

// generateNodeKey 生成一把密钥。
//
// 🔴 哈希口径必须与 `mw.AuthenticateNode` 里那一行**逐字一致**：
// `sha256(cfg.Pepper + raw)`，其中 raw 是节点原样发过来的整串
// （`bpn_<key_id>_<secret>`，不是只有 secret 段）。
// 少拼一段 pepper、或者只哈希 secret 段，签出来的密钥在鉴权时永远查不到 ——
// 现象是「新签的密钥一用就 401」，而人的第一反应会是去查节点配置，不是查签发口径。
//
// SHA-256 而不是 argon2id 是**有意的取舍**（api-contract §3.2.1）：慢哈希抵抗的是
// 对低熵人类密码的离线爆破，这里是 256 位 CSPRNG，离线爆破物理上不成立，
// 而每 60 秒 × 节点数 × 5 端点都付一次 argon2 是纯损失。
// ⚠️ 这条取舍在密钥改为人工可设时立刻失效 —— 所以请求体里没有任何写 secret 的字段。
func generateNodeKey(pepper string) (issuedNodeKey, error) {
	keyID, err := randomNodeKeyID()
	if err != nil {
		return issuedNodeKey{}, err
	}
	raw := make([]byte, nodeKeySecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return issuedNodeKey{}, fmt.Errorf("生成密钥随机数失败: %w", err)
	}
	token := nodeKeyTokenPrefix + keyID + "_" + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(pepper + token))
	return issuedNodeKey{KeyID: keyID, Secret: token, Hash: sum[:]}, nil
}

// randomNodeKeyID 生成六字符 base32 短标识。
//
// `b & 31` 是均匀的（32 整除 256），不需要拒绝采样。
// 用 `b % 32` 也一样，但写成掩码是为了让「换一个非 2 的幂的字母表就必须改这里」
// 在读代码时是显式的 —— 那种改动会引入一个不报错的分布偏斜。
func randomNodeKeyID() (string, error) {
	b := make([]byte, nodeKeyIDLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成 key_id 随机数失败: %w", err)
	}
	out := make([]byte, nodeKeyIDLen)
	for i, v := range b {
		out[i] = nodeKeyIDAlphabet[v&31]
	}
	return string(out), nil
}

// resolveNodeKeyScopes 校验并归一化 scopes。
func resolveNodeKeyScopes(in *[]gen.NodeScope) ([]string, *gen.ErrorDetail) {
	scopes := nodeKeyDefaultScopes
	if in != nil {
		if len(*in) == 0 {
			// 🔴 显式的空数组不当作「用默认值」。一把零 scope 的密钥能通过鉴权、
			//    但每个 HasScope 都是 false —— 节点会在所有端点上拿到 403，
			//    看起来像被封禁而不是像配置错误。这是客户端的 bug，让它响亮。
			return nil, &gen.ErrorDetail{Field: "scopes",
				Reason: "不能是空数组（不传该字段即使用默认的五个 scope；一把零 scope 的密钥在所有节点端点上都会 403）"}
		}
		scopes = *in
	}
	out := make([]string, 0, len(scopes))
	seen := make(map[gen.NodeScope]bool, len(scopes))
	for _, sc := range scopes {
		// 精确匹配，非前缀（契约 NodeScope 的原话）。
		if !nodeKeyAllowedScopes[sc] {
			return nil, &gen.ErrorDetail{Field: "scopes", Reason: fmt.Sprintf("未知 scope %q", string(sc))}
		}
		if seen[sc] {
			continue
		}
		seen[sc] = true
		out = append(out, string(sc))
	}
	return out, nil
}

// createAdminNodeKey 签发一把密钥（D5 第 1 步）。
//
// 返回的 gen.NodeKeyCreated 里含明文，调用方必须**只**把它放进 201 响应体。
func createAdminNodeKey(ctx context.Context, tx adminNodeTxRunner, actor audit.Actor,
	nodeID int64, name string, scopes []string, expiresAt pgtype.Timestamptz,
	pepper string) (gen.NodeKeyCreated, error) {
	var out gen.NodeKeyCreated

	err := tx.Run(ctx, actor, func(ctx context.Context, q adminNodeWriter) (audit.Entry, error) {
		if _, err := q.AdminGetNode(ctx, nodeID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return audit.Entry{}, errAdminNodeNotFound
			}
			return audit.Entry{}, fmt.Errorf("查询节点失败: %w", err)
		}

		// «同时有效 ≤ 2» 闸（data-model §8.3）。
		cnt, err := q.AdminCountActiveNodeKeys(ctx, nodeID)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("统计有效密钥失败: %w", err)
		}
		if cnt.ActiveKeys >= nodeKeyMaxActivePerServer {
			return audit.Entry{}, fmt.Errorf("%w: 当前 %d 把", errAdminNodeKeyTooMany, cnt.ActiveKeys)
		}

		key, err := generateNodeKey(pepper)
		if err != nil {
			return audit.Entry{}, err
		}
		// 🔴 撞前缀的防守放在**签发**这一侧。
		//    server_keys.key_prefix 上没有唯一索引，两行同前缀能插进去；
		//    而撞上之后 D5 第 2 步的 AdminGetNodeKeyByPrefix 会返回 2 行，
		//    那时唯一安全的回应是 500 —— 也就是那把密钥再也吊销不了。
		//    32^6 ≈ 10.7 亿，实际节点数下撞车概率极低，但「极低」不等于零，
		//    而代价是一把永久吊销不掉的密钥。这里查一次挡住它。
		//    根治仍然是一条 migration：CREATE UNIQUE INDEX server_keys_prefix_uk ON server_keys (key_prefix)。
		dup, err := q.AdminGetNodeKeyByPrefix(ctx, key.KeyID)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("检查 key_id 是否撞车失败: %w", err)
		}
		if len(dup) > 0 {
			// 不在事务里重试：PG 的事务在任何一条语句出错后就整体作废，
			// 而这里虽然没出错，重试也只会让一次本该罕见的碰撞变成一段不好论证的循环。
			// 直接失败，调用方重试一次请求即可（签发不是高频操作）。
			return audit.Entry{}, fmt.Errorf("生成的 key_id %q 与已有密钥撞车，请重试本次请求", key.KeyID)
		}

		row, err := q.CreateServerKey(ctx, dbgen.CreateServerKeyParams{
			ServerID:  nodeID,
			Name:      name,
			KeyPrefix: key.KeyID,
			KeyHash:   key.Hash,
			Scopes:    scopes,
			ExpiresAt: expiresAt,
			CreatedBy: &actor.AdminID,
		})
		if err != nil {
			return audit.Entry{}, fmt.Errorf("签发节点密钥失败: %w", err)
		}

		out = gen.NodeKeyCreated{
			Key: gen.NodeKey{
				Id:        row.ID,
				KeyId:     row.KeyPrefix,
				Name:      row.Name,
				Scopes:    nodeScopesView(row.Scopes),
				ExpiresAt: tsPtr(row.ExpiresAt),
			},
			Secret: key.Secret,
		}
		if row.IssuedAt.Valid {
			out.Key.CreatedAt = row.IssuedAt.Time.UTC()
		}

		return audit.Entry{
			Action:     "D5.node_key.create",
			TargetType: "node_key",
			TargetID:   row.KeyPrefix,
			// 🔴 After 里只有 key_prefix / scopes / server_id ——
			//    **没有 secret，也没有 key_hash**。audit_logs 是 append-only 且永不删除的，
			//    明文进去就永远在里面，而且会被每一个有 GET /admin/audit 权限的人看到。
			After: map[string]any{
				"id": row.ID, "server_id": nodeID, "key_prefix": row.KeyPrefix,
				"name": row.Name, "scopes": row.Scopes, "expires_at": tsPtr(row.ExpiresAt),
			},
			// Reason 为空：契约的 NodeKeyCreateRequest 是 additionalProperties:false
			// 且没有 reason 字段，L2 无从取值。不编造。
		}, nil
	})
	if err != nil {
		return gen.NodeKeyCreated{}, err
	}
	return out, nil
}

// CreateAdminNodeKey 实现 POST /api/v1/admin/nodes/{id}/keys。
func (s *Server) CreateAdminNodeKey(ctx context.Context, req gen.CreateAdminNodeKeyRequestObject) (gen.CreateAdminNodeKeyResponseObject, error) {
	actor, admin, ok := s.nodeAdminActor(ctx)
	if !ok {
		return gen.CreateAdminNodeKey500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "管理面路由未挂鉴权", errAdminNodeNoAuth),
		}, nil
	}
	// L4 + L3（顺序与理由见 adminNodeKeyGate）
	if authErr := adminNodeKeyGate(ctx, admin, s.nodeStepUp(), req.Params.XTOTPCode); authErr != nil {
		// 🔴 按 Status 分派，不要一律当 403。RequireStepUp 在装配错误、
		//    或 used_totp 写不进去时返回的是 **500** —— 把它压成 403
		//    会把「我们的 TOTP 依赖坏了」谎报成「你的验证码不对」，
		//    于是管理员会一直重输验证码，而没有人去查真正的原因。
		if adminNodeAuthErrIsInternal(authErr) {
			return gen.CreateAdminNodeKey500JSONResponse{
				ErrInternalJSONResponse: s.internalErr(ctx, "TOTP step-up 依赖不可用", authErr),
			}, nil
		}
		return gen.CreateAdminNodeKey403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.ErrorCode(authErr.Code), authErr.Message),
		}, nil
	}

	if req.Body == nil {
		return gen.CreateAdminNodeKey422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求体不能为空", detail("name", "必填")),
		}, nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" || len([]rune(name)) > 64 {
		return gen.CreateAdminNodeKey422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求参数不合法",
				detail("name", "必填，1–64 字符（如「2026-08 轮换」）")),
		}, nil
	}
	scopes, sd := resolveNodeKeyScopes(req.Body.Scopes)
	if sd != nil {
		return gen.CreateAdminNodeKey422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求参数不合法", *sd),
		}, nil
	}
	var expires pgtype.Timestamptz
	if req.Body.ExpiresAt != nil {
		if !req.Body.ExpiresAt.After(time.Now()) {
			// 签一把已经过期的密钥是「签了个寂寞」：节点用它鉴权直接失败，
			// 而 D5 第 2 步会因为「新密钥从没被用过」永远拒绝吊销旧的 —— 轮换卡死。
			return gen.CreateAdminNodeKey422JSONResponse{
				ErrUnprocessableJSONResponse: s.unprocessable(ctx, "请求参数不合法",
					detail("expires_at", "必须晚于当前时间")),
			}, nil
		}
		expires = tstz(*req.Body.ExpiresAt)
	}

	// 🔴 pepper 缺失时不能签发：签出来的哈希与鉴权侧算的不一致，
	//    现象是「新密钥一用就 401」，而密钥已经发到运维手里了（明文不可再取）。
	if s.cfg.NodeKeyPepper == "" {
		return gen.CreateAdminNodeKey500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "BP_NODE_KEY_PEPPER 未配置，拒绝签发节点密钥",
				errors.New("node key pepper 为空")),
		}, nil
	}

	created, err := createAdminNodeKey(ctx, s.nodeTx(), actor, req.Id, name, scopes, expires, s.cfg.NodeKeyPepper)
	switch {
	case errors.Is(err, errAdminNodeNotFound):
		return gen.CreateAdminNodeKey404JSONResponse{ErrNotFoundJSONResponse: s.notFound(ctx, "节点不存在")}, nil
	case errors.Is(err, errAdminNodeKeyTooMany):
		// 契约给 createAdminNodeKey 没有 409，只能用 422。
		return gen.CreateAdminNodeKey422JSONResponse{
			ErrUnprocessableJSONResponse: s.unprocessable(ctx,
				fmt.Sprintf("该节点同时有效的密钥已达上限 %d 把：轮换期有两把是正常的，"+
					"出现第三把说明上一次轮换没做完（旧密钥忘了吊销）。请先吊销旧密钥", nodeKeyMaxActivePerServer),
				detail("node", err.Error())),
		}, nil
	case err != nil:
		return gen.CreateAdminNodeKey500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "签发节点密钥失败", err),
		}, nil
	}

	// 🔴 日志里只有 key_prefix，没有 Secret。Cloud Logging 是长期保留的，
	//    一条带明文的 INFO 等于把密钥永久写进日志系统。
	s.logger.InfoContext(ctx, "后台签发节点密钥（D5 第 1 步）",
		"node_id", req.Id, "key_prefix", created.Key.KeyId, "admin_id", admin.AdminID,
		"request_id", middleware.RequestIDFrom(ctx))

	resp := gen.CreateAdminNodeKey201JSONResponse{
		Headers: gen.CreateAdminNodeKey201ResponseHeaders{
			Location: "/api/v1/admin/node-keys/" + created.Key.KeyId,
		},
	}
	resp.Body.Data = created
	resp.Body.Meta = s.meta(ctx)
	return resp, nil
}

// ============================================================
// 10 · RevokeAdminNodeKey（D5 第 2 步）
// ============================================================

// plausibleNodeKeyID 做一次廉价的形态校验。
//
// 契约的路径参数是 `^[a-z2-7]{6}$`，但生成的路由包装器**不校验 pattern**
// （oapi-codegen 只做类型绑定），所以这里必须自己挡一道。
// 只挡明显不可能的输入（空、超长），不严格要求六字符 base32：
// 库里 key_prefix 是 text，历史上手工灌进去的密钥（`bpk_smoke` 之类）形状不同，
// 严格拒绝会让它们**永远吊销不掉**。形状不合契约时按普通查询走，查不到就是 404。
func plausibleNodeKeyID(s string) bool {
	return s != "" && len(s) <= 64
}

// revokeAdminNodeKey 吊销一把密钥（D5 第 2 步）。
//
// 🔴 三个错误码的分辨必须靠**先读一次**：`AdminRevokeNodeKeyTwoStep` 影响 0 行
// 有三种互不相同的原因（不存在 / 已吊销 / 没有见证密钥），只有 UPDATE 的话
// 三种全塌成「0 行」，错误码只能瞎猜 —— 而契约逐字要求 409 的那句 message。
//
// 🔴 但**拒绝本身仍由 UPDATE 里的 EXISTS 做**：两条语句之间有窗口，
// 轮换期节点每 60 秒改一次 last_used_at、另一个管理员可能同时在吊销另一把。
// 「读的时候有见证、写的时候没有了」这个顺序是真实可发生的，
// 而它的结果正是这条规则要防的那件事。所以 UPDATE 返回 0 行时**也**当作 409，
// 不是当作「读的时候明明可以」的内部错误。
func revokeAdminNodeKey(ctx context.Context, tx adminNodeTxRunner, actor audit.Actor,
	keyID, revokedReason string) (int64, error) {
	var serverID int64

	err := tx.Run(ctx, actor, func(ctx context.Context, q adminNodeWriter) (audit.Entry, error) {
		rows, err := q.AdminGetNodeKeyByPrefix(ctx, keyID)
		if err != nil {
			return audit.Entry{}, fmt.Errorf("查询节点密钥失败: %w", err)
		}
		switch {
		case len(rows) == 0:
			return audit.Entry{}, errAdminNodeKeyNotFound
		case len(rows) > 1:
			// 🔴 **不是 409。** 409 说的是「你要做的事与当前状态冲突」，
			//    而这里的真相是「我们的数据坏了」：key_prefix 上没有唯一索引，
			//    同前缀两行都插进来了。挑一行去吊销 = 有一半概率吊销掉另一把密钥，
			//    而那正是「节点失联」的另一条路径，事后从日志里看不出任何异常。
			return audit.Entry{}, fmt.Errorf("%w: key_id=%s 命中 %d 行", errAdminNodeKeyAmbiguous, keyID, len(rows))
		}
		pre := rows[0]
		serverID = pre.ServerID

		if pre.RevokedAt.Valid {
			return audit.Entry{}, errAdminNodeKeyRevoked
		}
		if pre.WitnessCount == 0 {
			return audit.Entry{}, errAdminNodeKeyNoWitness
		}

		row, err := q.AdminRevokeNodeKeyTwoStep(ctx, dbgen.AdminRevokeNodeKeyTwoStepParams{
			KeyID:         pre.ID,
			RevokedReason: revokedReason,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// 读到写之间见证没了（或者别人先吊销了这一把）。
				// 仍然是 409：数据库刚刚**正确地**拒绝了一次会让节点失联的操作。
				return audit.Entry{}, errAdminNodeKeyNoWitness
			}
			return audit.Entry{}, fmt.Errorf("吊销节点密钥失败: %w", err)
		}

		return audit.Entry{
			Action:     "D5.node_key.revoke",
			TargetType: "node_key",
			TargetID:   row.KeyPrefix,
			Before: map[string]any{
				"server_id": row.ServerID, "key_prefix": row.KeyPrefix,
				"revoked_at": tsPtr(row.BeforeRevokedAt), "revoked_reason": row.BeforeRevokedReason,
				"last_used_at": tsPtr(row.BeforeLastUsedAt),
				// 见证数进快照：事后要能回答「当时凭什么允许吊销」。
				"witness_count": pre.WitnessCount,
			},
			After: map[string]any{
				"server_id": row.ServerID, "key_prefix": row.KeyPrefix,
				"revoked_at": tsPtr(row.AfterRevokedAt), "revoked_reason": row.AfterRevokedReason,
			},
			// Reason 与 revoked_reason 同源，见 handler 侧的构造。
			Reason: revokedReason,
		}, nil
	})
	return serverID, err
}

// RevokeAdminNodeKey 实现 DELETE /api/v1/admin/node-keys/{key_id}。
func (s *Server) RevokeAdminNodeKey(ctx context.Context, req gen.RevokeAdminNodeKeyRequestObject) (gen.RevokeAdminNodeKeyResponseObject, error) {
	actor, admin, ok := s.nodeAdminActor(ctx)
	if !ok {
		return gen.RevokeAdminNodeKey500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "管理面路由未挂鉴权", errAdminNodeNoAuth),
		}, nil
	}
	// L4 + L3（顺序与理由见 adminNodeKeyGate）
	if authErr := adminNodeKeyGate(ctx, admin, s.nodeStepUp(), req.Params.XTOTPCode); authErr != nil {
		// 按 Status 分派，理由同 CreateAdminNodeKey。
		if adminNodeAuthErrIsInternal(authErr) {
			return gen.RevokeAdminNodeKey500JSONResponse{
				ErrInternalJSONResponse: s.internalErr(ctx, "TOTP step-up 依赖不可用", authErr),
			}, nil
		}
		return gen.RevokeAdminNodeKey403JSONResponse{
			ErrForbiddenJSONResponse: s.forbidden(ctx, gen.ErrorCode(authErr.Code), authErr.Message),
		}, nil
	}

	keyID := strings.TrimSpace(req.KeyId)
	if !plausibleNodeKeyID(keyID) {
		// 契约给这个 operation 没有 422，所以形态不合法只能落 404。
		// 这不是遮掩：一个不可能存在的 key_id 与一个不存在的 key_id，
		// 对调用方来说答案确实一样。
		return gen.RevokeAdminNodeKey404JSONResponse{
			ErrNotFoundJSONResponse: s.notFound(ctx, "节点密钥不存在"),
		}, nil
	}

	// revoked_reason 是 NOT NULL 列，而契约给这个 operation **没有请求体**，
	// L2 无从取值。这里由服务端拼一条**如实的**说明（是谁、哪次请求），
	// 而不是编一句业务理由 —— 编出来的理由会让事后读审计的人以为当时有人给过原因。
	reason := fmt.Sprintf("D5 两步轮换第 2 步：由 %s(admin_id=%d) 吊销，request_id=%s",
		admin.Email, admin.AdminID, middleware.RequestIDFrom(ctx))

	serverID, err := revokeAdminNodeKey(ctx, s.nodeTx(), actor, keyID, reason)
	switch {
	case errors.Is(err, errAdminNodeKeyNotFound):
		return gen.RevokeAdminNodeKey404JSONResponse{
			ErrNotFoundJSONResponse: s.notFound(ctx, "节点密钥不存在"),
		}, nil
	case errors.Is(err, errAdminNodeKeyRevoked):
		return gen.RevokeAdminNodeKey409JSONResponse{
			ErrConflictJSONResponse: s.conflict(ctx, "该密钥已经吊销过"),
		}, nil
	case errors.Is(err, errAdminNodeKeyNoWitness):
		// 契约逐字要求的这句话。
		return gen.RevokeAdminNodeKey409JSONResponse{
			ErrConflictJSONResponse: s.conflict(ctx,
				"新密钥尚未被节点使用过，现在吊销旧密钥会导致节点失联"),
		}, nil
	case errors.Is(err, errAdminNodeKeyAmbiguous):
		// 告警标记 bp_node_key_prefix_collision：monitoring 侧按它建 log-based metric。
		// 这条必须响亮 —— 它意味着有一把密钥暂时吊销不掉，且库里有两行同前缀。
		s.logger.ErrorContext(ctx, "bp_node_key_prefix_collision key_id 命中多行，拒绝吊销以免误吊另一把密钥",
			"key_id", keyID, "admin_id", admin.AdminID, "err", err,
			"request_id", middleware.RequestIDFrom(ctx))
		return gen.RevokeAdminNodeKey500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "节点密钥标识不唯一", err),
		}, nil
	case err != nil:
		return gen.RevokeAdminNodeKey500JSONResponse{
			ErrInternalJSONResponse: s.internalErr(ctx, "吊销节点密钥失败", err),
		}, nil
	}

	s.logger.InfoContext(ctx, "后台吊销节点密钥（D5 第 2 步）",
		"node_id", serverID, "key_id", keyID, "admin_id", admin.AdminID,
		"request_id", middleware.RequestIDFrom(ctx))

	return gen.RevokeAdminNodeKey204Response{
		Headers: gen.RevokeAdminNodeKey204ResponseHeaders{XRequestId: middleware.RequestIDFrom(ctx)},
	}, nil
}

// ============================================================
// PG 错误识别
// ============================================================

// isNodeGroupFKViolation 识别外键违反（23503）。
//
// 用它把「分组 id 不存在」映射成 422 而不是 500：调用方填错一个 group_id
// 是他能自己纠正的事，回 500 会让它进错误率告警，并且什么也没告诉他。
func isNodeGroupFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
