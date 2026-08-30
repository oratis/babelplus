package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"regexp"
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
)

// 后台「节点与密钥」十个 operation 的测试。
//
// 与 order_test.go / task_test.go 同一条纪律：测**吃窄接口的自由函数**与纯函数，
// 不测那些必须先有 *store.Store 的 handler 方法。能在 handler 方法这一层测的，
// 只有「在碰数据库之前就返回」的那些分支 —— 而四层强制里的 L2/L3/L4 恰好全在那之前，
// 所以本文件的「参数没收齐时不许提交」用例有一半是真的打 handler 方法的。
//
// 带 🔴 的用例都是「不这么做会**静默**出错」的那一类，其中五条是任务书点名的：
//   - TestRevokeAdminNodeKey…NoWitness…：一步吊销必须 409（D5 的全部意义）
//   - TestRevokeAdminNodeKey…Ambiguous…：key_prefix 撞车必须 500 且**不吊销任何一把**
//   - TestUpdateAdminNode…PatchKeeps…：PATCH 未提供的字段不许被抹成零值
//   - TestAdminNodeWrites…AuditFailureRollsBack…：审计写失败 → 业务写入回滚
//   - TestCreateAdminNodeKey…SecretNever…：明文不进审计、不进库

// ============================================================
// 本组 operation 必须真的落在 Server 上
// ============================================================

func TestAdminNodeOperationsAreImplemented(t *testing.T) {
	var s any = &Server{}
	if _, ok := s.(interface {
		ListAdminNodes(context.Context, gen.ListAdminNodesRequestObject) (gen.ListAdminNodesResponseObject, error)
		GetAdminNode(context.Context, gen.GetAdminNodeRequestObject) (gen.GetAdminNodeResponseObject, error)
		CreateAdminNode(context.Context, gen.CreateAdminNodeRequestObject) (gen.CreateAdminNodeResponseObject, error)
		UpdateAdminNode(context.Context, gen.UpdateAdminNodeRequestObject) (gen.UpdateAdminNodeResponseObject, error)
		DeleteAdminNode(context.Context, gen.DeleteAdminNodeRequestObject) (gen.DeleteAdminNodeResponseObject, error)
		EnableAdminNode(context.Context, gen.EnableAdminNodeRequestObject) (gen.EnableAdminNodeResponseObject, error)
		DisableAdminNode(context.Context, gen.DisableAdminNodeRequestObject) (gen.DisableAdminNodeResponseObject, error)
		ListAdminNodeKeys(context.Context, gen.ListAdminNodeKeysRequestObject) (gen.ListAdminNodeKeysResponseObject, error)
		CreateAdminNodeKey(context.Context, gen.CreateAdminNodeKeyRequestObject) (gen.CreateAdminNodeKeyResponseObject, error)
		RevokeAdminNodeKey(context.Context, gen.RevokeAdminNodeKeyRequestObject) (gen.RevokeAdminNodeKeyResponseObject, error)
	}); !ok {
		t.Fatal("节点与密钥这一组里有 operation 没有被 Server 覆盖，仍落在 Unimplemented 的 501 上")
	}
}

// 管理面**一条都不能**进免登录表：它们能删节点、能签发进入整张网的密钥。
func TestAdminNodeOperationsAreNotPublic(t *testing.T) {
	for _, name := range []string{
		"ListAdminNodes", "GetAdminNode", "CreateAdminNode", "UpdateAdminNode",
		"DeleteAdminNode", "EnableAdminNode", "DisableAdminNode",
		"ListAdminNodeKeys", "CreateAdminNodeKey", "RevokeAdminNodeKey",
	} {
		if PublicOperations[name] {
			t.Fatalf("%s 出现在免登录表里", name)
		}
	}
}

// 🔴 列表与详情的投影必须**逐字相同**。
//
// 两条 SQL 的 SELECT 列表是刻意一致的（让「列表说在线、详情说离线」在结构上不可能）。
// 这条断言把那个不变量钉在 Go 侧：改了其中一条查询、忘了改另一条时，
// 生成出来的两个结构体字段集就会分叉 —— 而分叉的现象是后台两个页面显示不同的东西，
// 没有任何报错。
func TestAdminNodeListAndGetProjectionsStayIdentical(t *testing.T) {
	list := reflect.TypeOf(dbgen.AdminListNodesPageRow{})
	get := reflect.TypeOf(dbgen.AdminGetNodeRow{})
	if list.NumField() != get.NumField() {
		t.Fatalf("字段数不同：列表 %d，详情 %d —— 两条查询的投影已经漂移", list.NumField(), get.NumField())
	}
	for i := range list.NumField() {
		lf, gf := list.Field(i), get.Field(i)
		if lf.Name != gf.Name || lf.Type != gf.Type {
			t.Fatalf("第 %d 个字段不同：列表 %s %s，详情 %s %s", i, lf.Name, lf.Type, gf.Name, gf.Type)
		}
	}
}

// ============================================================
// 假实现
// ============================================================

// fakeAdminNodeDB 同时满足 adminNodeReader 与 adminNodeWriter。
//
// 它把「写了什么」记进 applied，把「事务回滚了」记成 rolledBack ——
// 因为本文件最重要的一条断言是「审计写失败时业务写入必须一起消失」，
// 而那条断言只有在假实现能表达「回滚」时才写得出来。
type fakeAdminNodeDB struct {
	nodes    map[int64]dbgen.AdminGetNodeRow
	keys     map[int64][]dbgen.AdminListNodeKeysRow
	byPrefix map[string][]dbgen.AdminGetNodeKeyByPrefixRow

	listRows  []dbgen.AdminListNodesPageRow
	listParam dbgen.AdminListNodesPageParams
	total     int64
	countCall int

	activeKeys dbgen.AdminCountActiveNodeKeysRow

	// 注入的错误
	prefixAlwaysHit bool
	getErr          error
	listErr         error
	countErr        error
	createSrvErr    error
	addGroupErr     error
	revokeNoRows    bool

	// 记录
	applied    []string
	rolledBack bool
	createdKey dbgen.CreateServerKeyParams
	createdSrv dbgen.CreateServerParams
	updated    dbgen.AdminUpdateNodeParams
	enabledSet []dbgen.AdminSetNodeEnabledParams
	bumpedRevs []int64
	bumpedCfg  []int64
	addedGrp   []int64
	removedGrp []int64
	softDelete []int64
	revoked    []dbgen.AdminRevokeNodeKeyTwoStepParams
}

func (f *fakeAdminNodeDB) note(op string) { f.applied = append(f.applied, op) }

func (f *fakeAdminNodeDB) did(op string) bool {
	for _, a := range f.applied {
		if a == op {
			return true
		}
	}
	return false
}

// ---- adminNodeReader ----

func (f *fakeAdminNodeDB) AdminListNodesPage(_ context.Context, arg dbgen.AdminListNodesPageParams) ([]dbgen.AdminListNodesPageRow, error) {
	f.listParam = arg
	if f.listErr != nil {
		return nil, f.listErr
	}
	n := int(arg.PageLimit)
	if n > len(f.listRows) {
		n = len(f.listRows)
	}
	return f.listRows[:n], nil
}

func (f *fakeAdminNodeDB) AdminCountNodesFiltered(context.Context) (int64, error) {
	f.countCall++
	return f.total, f.countErr
}

func (f *fakeAdminNodeDB) AdminGetNode(_ context.Context, id int64) (dbgen.AdminGetNodeRow, error) {
	if f.getErr != nil {
		return dbgen.AdminGetNodeRow{}, f.getErr
	}
	row, ok := f.nodes[id]
	if !ok {
		return dbgen.AdminGetNodeRow{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeAdminNodeDB) AdminListNodeKeys(_ context.Context, id int64) ([]dbgen.AdminListNodeKeysRow, error) {
	return f.keys[id], nil
}

// ---- adminNodeWriter ----

func (f *fakeAdminNodeDB) CreateServer(_ context.Context, arg dbgen.CreateServerParams) (dbgen.Server, error) {
	if f.createSrvErr != nil {
		return dbgen.Server{}, f.createSrvErr
	}
	f.note("create_server")
	f.createdSrv = arg
	return dbgen.Server{
		ID: 77, Code: arg.Code, Name: arg.Name, Protocol: arg.Protocol,
		Host: arg.Host, Port: arg.Port, Region: arg.Region,
		Enabled: arg.Enabled, Visible: arg.Visible,
	}, nil
}

func (f *fakeAdminNodeDB) InitNodeRev(_ context.Context, id int64) (dbgen.NodeRev, error) {
	f.note("init_node_rev")
	return dbgen.NodeRev{ServerID: id}, nil
}

func (f *fakeAdminNodeDB) AdminUpdateNode(_ context.Context, arg dbgen.AdminUpdateNodeParams) (dbgen.AdminUpdateNodeRow, error) {
	cur, ok := f.nodes[arg.ServerID]
	if !ok {
		return dbgen.AdminUpdateNodeRow{}, pgx.ErrNoRows
	}
	f.note("update_node")
	f.updated = arg
	return dbgen.AdminUpdateNodeRow{
		BeforeName: cur.Name, BeforeProtocol: cur.Protocol, BeforeHost: cur.Host,
		BeforePort: cur.Port, BeforeRegion: cur.Region, BeforeEnabled: cur.Enabled,
		ID: arg.ServerID, Code: cur.Code, Name: arg.Name, Protocol: arg.Protocol,
		Host: arg.Host, Port: arg.Port, Region: arg.Region, Enabled: arg.Enabled,
	}, nil
}

func (f *fakeAdminNodeDB) BumpConfigRev(_ context.Context, id int64) (dbgen.BumpConfigRevRow, error) {
	f.note("bump_config_rev")
	f.bumpedCfg = append(f.bumpedCfg, id)
	return dbgen.BumpConfigRevRow{ConfigRev: 2}, nil
}

func (f *fakeAdminNodeDB) AddServerToGroup(_ context.Context, arg dbgen.AddServerToGroupParams) error {
	if f.addGroupErr != nil {
		return f.addGroupErr
	}
	f.note("add_group")
	f.addedGrp = append(f.addedGrp, arg.GroupID)
	return nil
}

func (f *fakeAdminNodeDB) RemoveServerFromGroup(_ context.Context, arg dbgen.RemoveServerFromGroupParams) error {
	f.note("remove_group")
	f.removedGrp = append(f.removedGrp, arg.GroupID)
	return nil
}

func (f *fakeAdminNodeDB) BumpUserRevByGroup(_ context.Context, groupID int64) error {
	f.note("bump_user_rev")
	f.bumpedRevs = append(f.bumpedRevs, groupID)
	return nil
}

func (f *fakeAdminNodeDB) AdminSetNodeEnabled(_ context.Context, arg dbgen.AdminSetNodeEnabledParams) (dbgen.AdminSetNodeEnabledRow, error) {
	cur, ok := f.nodes[arg.ServerID]
	if !ok {
		return dbgen.AdminSetNodeEnabledRow{}, pgx.ErrNoRows
	}
	f.note("set_enabled")
	f.enabledSet = append(f.enabledSet, arg)
	return dbgen.AdminSetNodeEnabledRow{
		BeforeEnabled: cur.Enabled, ID: arg.ServerID, Code: cur.Code, Name: cur.Name,
		AfterEnabled: arg.Enabled,
	}, nil
}

func (f *fakeAdminNodeDB) AdminGetNodeForDangerOp(_ context.Context, id int64) (dbgen.AdminGetNodeForDangerOpRow, error) {
	cur, ok := f.nodes[id]
	if !ok {
		return dbgen.AdminGetNodeForDangerOpRow{}, pgx.ErrNoRows
	}
	return dbgen.AdminGetNodeForDangerOpRow{
		ID: id, Code: cur.Code, Name: cur.Name, Enabled: cur.Enabled, Visible: cur.Visible,
		ReportedOnlineUsers: 0,  // 节点失联时上报值就是这个「让人放心」的 0
		ObservedOnlineUsers: 41, // 我们自己观测到的却是 41
		ActiveKeyCount:      2,
	}, nil
}

func (f *fakeAdminNodeDB) AdminSoftDeleteNode(_ context.Context, id int64) (dbgen.AdminSoftDeleteNodeRow, error) {
	cur, ok := f.nodes[id]
	if !ok {
		return dbgen.AdminSoftDeleteNodeRow{}, pgx.ErrNoRows
	}
	f.note("soft_delete")
	f.softDelete = append(f.softDelete, id)
	return dbgen.AdminSoftDeleteNodeRow{
		BeforeName: cur.Name, BeforeEnabled: cur.Enabled, BeforeVisible: cur.Visible,
		ID: id, Code: cur.Code, Name: cur.Name, DeletedAt: ts(time.Now()),
	}, nil
}

func (f *fakeAdminNodeDB) AdminCountActiveNodeKeys(context.Context, int64) (dbgen.AdminCountActiveNodeKeysRow, error) {
	return f.activeKeys, nil
}

func (f *fakeAdminNodeDB) CreateServerKey(_ context.Context, arg dbgen.CreateServerKeyParams) (dbgen.ServerKey, error) {
	f.note("create_key")
	f.createdKey = arg
	return dbgen.ServerKey{
		ID: 900, ServerID: arg.ServerID, Name: arg.Name, KeyPrefix: arg.KeyPrefix,
		KeyHash: arg.KeyHash, Scopes: arg.Scopes, ExpiresAt: arg.ExpiresAt,
		IssuedAt: ts(time.Now()), CreatedBy: arg.CreatedBy,
	}, nil
}

func (f *fakeAdminNodeDB) AdminGetNodeKeyByPrefix(_ context.Context, prefix string) ([]dbgen.AdminGetNodeKeyByPrefixRow, error) {
	if f.prefixAlwaysHit {
		return []dbgen.AdminGetNodeKeyByPrefixRow{keyRow(1, 3, prefix, false, 1)}, nil
	}
	return f.byPrefix[prefix], nil
}

func (f *fakeAdminNodeDB) AdminRevokeNodeKeyTwoStep(_ context.Context, arg dbgen.AdminRevokeNodeKeyTwoStepParams) (dbgen.AdminRevokeNodeKeyTwoStepRow, error) {
	if f.revokeNoRows {
		// 模拟「读到写之间见证密钥没了」：UPDATE 的 EXISTS 不满足 → 0 行。
		return dbgen.AdminRevokeNodeKeyTwoStepRow{}, pgx.ErrNoRows
	}
	f.note("revoke_key")
	f.revoked = append(f.revoked, arg)
	return dbgen.AdminRevokeNodeKeyTwoStepRow{
		ID: arg.KeyID, ServerID: 3, KeyPrefix: "aaaaaa",
		AfterRevokedAt: ts(time.Now()), AfterRevokedReason: &arg.RevokedReason,
	}, nil
}

// fakeAdminNodeTx 复刻 audit.InTx 的语义：
// fn 报错 → 回滚；fn 成功但审计写失败 → **同样回滚**；都成功才提交。
type fakeAdminNodeTx struct {
	db        *fakeAdminNodeDB
	auditErr  error
	actor     audit.Actor
	entry     audit.Entry
	committed bool
	ran       bool
}

func (f *fakeAdminNodeTx) Run(ctx context.Context, actor audit.Actor,
	fn func(context.Context, adminNodeWriter) (audit.Entry, error)) error {
	f.ran = true
	f.actor = actor
	e, err := fn(ctx, f.db)
	if err != nil {
		f.db.rolledBack = true
		f.db.applied = nil
		return err
	}
	f.entry = e
	if f.auditErr != nil {
		// §6.3 第 1 条：审计写在业务写之后、提交之前，失败则整个操作回滚。
		f.db.rolledBack = true
		f.db.applied = nil
		return f.auditErr
	}
	f.committed = true
	return nil
}

// fakeNodeStepUp 是 L3 的假实现。
type fakeNodeStepUp struct {
	err    *middleware.AuthError
	called int
	gotVal string
}

func (f *fakeNodeStepUp) RequireStepUp(_ context.Context, code string) *middleware.AuthError {
	f.called++
	f.gotVal = code
	return f.err
}

// ---- 公共夹具 ----

func nodeRow(id int64, name string) dbgen.AdminGetNodeRow {
	return dbgen.AdminGetNodeRow{
		ID: id, Code: "bp-node-hk1", Name: name,
		Protocol: dbgen.ServerProtocolVlessReality,
		Host:     "hk1.example.com", Port: 443, Region: "asia-east2",
		Enabled: true, Visible: true, GroupIds: []int64{1, 2},
		CreatedAt: ts(time.Unix(1750000000, 0)),
	}
}

func nodeActor() audit.Actor {
	return audit.Actor{AdminID: 5, Email: "ops@example.com", IP: netip.MustParseAddr("203.0.113.9")}
}

func adminCtx(role string) context.Context {
	return middleware.WithAdmin(context.Background(),
		&middleware.AdminAuth{AdminID: 5, Email: "ops@example.com", Role: role})
}

// adminNodeTestServer 是一个**没有数据库**的 Server：
// 只够跑「在碰数据库之前就返回」的那些分支（L2 / L4 / 请求体校验）。
func adminNodeTestServer() *Server {
	return &Server{logger: testLogger(), cfg: &config.Config{}}
}

// ============================================================
// L2 / L4 / L1 的三个判据
// ============================================================

func TestCheckAdminNodeReason(t *testing.T) {
	// 🔴 按 rune 而不是字节：一条 5 个汉字的原因是 15 个字节，
	//    按字节判会放它过去，而它明显没写清楚为什么。
	if d := checkAdminNodeReason("节点已下线"); d == nil {
		t.Fatal("5 个汉字（15 字节）必须被拒 —— 判据是字符数不是字节数")
	}
	if d := checkAdminNodeReason("机房停电，节点长期离线"); d != nil {
		t.Fatalf("11 个汉字应当通过：%+v", d)
	}
	if d := checkAdminNodeReason("        "); d == nil {
		t.Fatal("纯空白必须被拒（trim 之后是空串）")
	}
	if d := checkAdminNodeReason("decommissioned"); d != nil {
		t.Fatalf("14 个 ASCII 字符应当通过：%+v", d)
	}
}

func TestAdminCanWriteNodes(t *testing.T) {
	if !adminCanWriteNodes(middleware.RoleOwner) || !adminCanWriteNodes(middleware.RoleAdmin) {
		t.Fatal("owner / admin 必须能写节点")
	}
	// 🔴 support 只读：停一台节点会让上面的人在 60 秒内掉线，
	//    这不该是回工单的人手边就有的按钮。
	if adminCanWriteNodes(middleware.RoleSupport) {
		t.Fatal("support 不能写节点")
	}
	// 未知角色的现象必须是「做不了」，不是「谁都能做」。
	if adminCanWriteNodes("") || adminCanWriteNodes("superadmin") {
		t.Fatal("未知角色必须被拒")
	}
}

func TestAdminNodeConfirmMatches(t *testing.T) {
	if !adminNodeConfirmMatches("香港 01", "香港 01") {
		t.Fatal("逐字相等必须通过")
	}
	for _, got := range []string{"香港 02", "香港 0", "香港 01 ", "", "香港 01x"} {
		if adminNodeConfirmMatches("香港 01", got) {
			t.Fatalf("%q 不应当匹配 —— L1 是逐字比对，不做 trim 也不做前缀匹配", got)
		}
	}
}

// 🔴 L4 必须在 L3 之前：反过来的话，一个没有节点写权限的人只要打一次请求，
// 就能让服务端把某个 6 位数记进 used_totp（验对即占用），从而把管理员正要用的
// 那个 code 提前烧掉 —— 一个不需要任何权限的拒绝服务。
func TestAdminNodeKeyGateChecksPermissionBeforeBurningTOTP(t *testing.T) {
	su := &fakeNodeStepUp{}
	admin := &middleware.AdminAuth{AdminID: 5, Email: "cs@example.com", Role: middleware.RoleSupport}

	authErr := adminNodeKeyGate(context.Background(), admin, su, "123456")
	if authErr == nil || authErr.Status != http.StatusForbidden {
		t.Fatalf("support 必须被 403 拒绝，实际 %+v", authErr)
	}
	if authErr.Code != "AUTH_PERMISSION_DENIED" {
		t.Fatalf("错误码应当是 AUTH_PERMISSION_DENIED，实际 %s", authErr.Code)
	}
	if su.called != 0 {
		t.Fatal("权限不足时**绝不能**调 RequireStepUp —— 那会消耗掉一个 TOTP code")
	}
}

// 「缺 TOTP」这条参数没收齐的用例：D5 两个 operation 共用这道闸。
func TestAdminNodeKeyGateRejectsMissingTOTP(t *testing.T) {
	su := &fakeNodeStepUp{err: &middleware.AuthError{
		Status: http.StatusForbidden, Code: "AUTH_TOTP_REQUIRED", Message: "该操作需要二次验证"}}
	admin := &middleware.AdminAuth{AdminID: 5, Email: "ops@example.com", Role: middleware.RoleOwner}

	authErr := adminNodeKeyGate(context.Background(), admin, su, "")
	if authErr == nil || authErr.Code != "AUTH_TOTP_REQUIRED" {
		t.Fatalf("缺 TOTP 必须被拒，实际 %+v", authErr)
	}
	if su.called != 1 || su.gotVal != "" {
		t.Fatalf("必须把请求头原样交给 RequireStepUp：called=%d val=%q", su.called, su.gotVal)
	}

	// 有权限 + TOTP 通过 → 放行。
	ok := &fakeNodeStepUp{}
	if authErr := adminNodeKeyGate(context.Background(), admin, ok, "481920"); authErr != nil {
		t.Fatalf("正常路径不应被拒：%+v", authErr)
	}
	if ok.gotVal != "481920" {
		t.Fatalf("TOTP code 没有透传：%q", ok.gotVal)
	}
}

// 🔴 step-up 的 500 不能被压成 403。
//
// RequireStepUp 在「路由没挂管理面鉴权」和「used_totp 写不进去」时返回的是 500。
// 压成 403 之后，「我们的 TOTP 依赖坏了」在前端长得和「验证码输错了」一模一样，
// 于是管理员会反复重输，而真正的故障没有任何人看得见。
func TestAdminNodeAuthErrIsInternal(t *testing.T) {
	if !adminNodeAuthErrIsInternal(&middleware.AuthError{
		Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "内部错误"}) {
		t.Fatal("500 必须被识别成我们自己的故障")
	}
	for _, code := range []string{"AUTH_TOTP_REQUIRED", "AUTH_TOTP_INVALID", "AUTH_PERMISSION_DENIED"} {
		if adminNodeAuthErrIsInternal(&middleware.AuthError{
			Status: http.StatusForbidden, Code: code, Message: "x"}) {
			t.Fatalf("%s 是调用方的问题，必须仍然是 403", code)
		}
	}
	if adminNodeAuthErrIsInternal(nil) {
		t.Fatal("nil 不是故障")
	}
}

// ============================================================
// 投影
// ============================================================

func TestAdminNodeView(t *testing.T) {
	t.Run("type 输出库里的原值，不折叠成用户面那套粗粒度名", func(t *testing.T) {
		p := adminNodeProjectionFromGet(nodeRow(3, "香港 01"))
		p.Protocol = dbgen.ServerProtocolVlessXhttpCdn
		v := adminNodeView(p)
		// 🔴 用户面把两条 vless 变体折叠成 "vless" 是为了不对外宣告「我们正在被封」；
		//    后台恰恰相反 —— 它是唯一能看见应急通路的地方。
		if v.Type != string(dbgen.ServerProtocolVlessXhttpCdn) {
			t.Fatalf("后台必须看得见 vless_xhttp_cdn，实际 %q", v.Type)
		}
	})

	t.Run("multiplier_e9 恒为 null（库里没有倍率列）", func(t *testing.T) {
		v := adminNodeView(adminNodeProjectionFromGet(nodeRow(3, "香港 01")))
		if v.MultiplierE9 != nil {
			t.Fatal("0004 刻意不建倍率列，绝不能拿别的列凑一个数出来")
		}
	})

	t.Run("node_rev 缺行时 config_rev 保持 null，不补 0", func(t *testing.T) {
		v := adminNodeView(adminNodeProjectionFromGet(nodeRow(3, "香港 01")))
		// 🔴 null 的意思是「建节点时漏了 InitNodeRev，这台机器的 ETag 从此不工作」；
		//    补成 0 会把这个故障伪装成「还没 bump 过」。
		if v.ConfigRev != nil || v.UserRev != nil {
			t.Fatal("LEFT JOIN 缺行必须保持 null")
		}
	})

	t.Run("从未上报过时不给 load_status，而不是给一份全 0 的", func(t *testing.T) {
		v := adminNodeView(adminNodeProjectionFromGet(nodeRow(3, "香港 01")))
		// 🔴 全 0 在后台看起来是「这台机器很空闲」—— 恰恰把「我们不知道它的状态」
		//    渲染成了最让人放心的那个样子。
		if v.LoadStatus != nil {
			t.Fatal("server_online_state 没有这一行时不该有 load_status")
		}
	})

	t.Run("有上报时给出四项资源，缺的分量落 0", func(t *testing.T) {
		p := adminNodeProjectionFromGet(nodeRow(3, "香港 01"))
		p.LastStatusAt = ts(time.Unix(1750000900, 0))
		p.CpuPct = ptrOf(float32(37.5))
		p.MemTotal, p.MemUsed = ptrOf(int64(2<<30)), ptrOf(int64(1<<30))
		v := adminNodeView(p)
		if v.LoadStatus == nil {
			t.Fatal("有上报就该有 load_status")
		}
		if v.LoadStatus.Cpu != 37.5 || v.LoadStatus.Mem.Total != 2<<30 {
			t.Fatalf("资源值不对：%+v", v.LoadStatus)
		}
		if v.LoadStatus.Disk.Total != 0 {
			t.Fatal("节点没报磁盘时应当落 0（契约的 NodeResourceUsage 两个字段都是必填非指针）")
		}
	})

	t.Run("group_ids 永远是数组，不是 null", func(t *testing.T) {
		p := adminNodeProjectionFromGet(nodeRow(3, "香港 01"))
		p.GroupIds = nil
		v := adminNodeView(p)
		if v.GroupIds == nil || len(*v.GroupIds) != 0 {
			t.Fatal("group_ids 必须序列化成 []，契约里它是数组")
		}
	})
}

func TestNodeKeyViewNeverLeaksHash(t *testing.T) {
	row := dbgen.AdminListNodeKeysRow{
		ID: 9, ServerID: 3, Name: "2026-08 轮换", KeyPrefix: "7f3a2c",
		Scopes: []string{"node:config:read", "uniproxy"}, IssuedAt: ts(time.Unix(1750000000, 0)),
	}
	v := nodeKeyView(row)
	if v.KeyId != "7f3a2c" {
		t.Fatalf("key_id ← key_prefix：%q", v.KeyId)
	}
	if v.CreatedAt.Unix() != 1750000000 {
		t.Fatal("created_at ← issued_at（库里没有 created_at 这一列）")
	}
	// 🔴 scopes 原样透传，**不过滤未知值**：库里若躺着 0004 那个 DEFAULT
	//    `'{uniproxy}'`，后台必须看得见它。过滤掉的话它看起来是「scopes: []」，
	//    而空数组像是「还没配」，不像「配错了」。
	if len(v.Scopes) != 2 || v.Scopes[1] != gen.NodeScope("uniproxy") {
		t.Fatalf("scopes 应当原样透传：%v", v.Scopes)
	}
	// 序列化出来一个字节的哈希都不能有。
	b, _ := json.Marshal(v)
	for _, bad := range []string{"hash", "secret", "key_hash"} {
		if strings.Contains(strings.ToLower(string(b)), bad) {
			t.Fatalf("NodeKey 序列化里出现了 %q：%s", bad, b)
		}
	}
}

// ============================================================
// 1 · ListAdminNodes
// ============================================================

func TestListAdminNodesPage(t *testing.T) {
	rows := make([]dbgen.AdminListNodesPageRow, 4)
	for i := range rows {
		rows[i] = dbgen.AdminListNodesPageRow{
			ID: int64(10 - i), Name: "n", Protocol: dbgen.ServerProtocolHysteria2,
			CreatedAt: ts(time.Unix(1750000000, 0)), GroupIds: []int64{},
		}
	}

	t.Run("多取一行判 has_more，多的那行不进正文", func(t *testing.T) {
		f := &fakeAdminNodeDB{listRows: rows}
		page, err := listAdminNodesPage(context.Background(), f, nil, 3, false)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if f.listParam.PageLimit != 4 {
			t.Fatalf("必须取 limit+1 行，实际 %d", f.listParam.PageLimit)
		}
		if len(page.Data) != 3 || !page.HasMore || page.NextCursor == nil {
			t.Fatalf("分页不对：len=%d hasMore=%v cursor=%v", len(page.Data), page.HasMore, page.NextCursor)
		}
	})

	t.Run("正好整除时 has_more=false，不发一个指向空页的游标", func(t *testing.T) {
		// 🔴 用「返回行数 == limit」判 has_more 的写法会在这里判成 true，
		//    用户点「加载更多」看到一页空数据 —— 而空页在前端通常长得像加载失败。
		f := &fakeAdminNodeDB{listRows: rows[:3]}
		page, err := listAdminNodesPage(context.Background(), f, nil, 3, false)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if page.HasMore || page.NextCursor != nil {
			t.Fatal("没有下一页时 has_more 必须是 false 且 next_cursor 必须是 null")
		}
	})

	t.Run("游标只透传 id：排序键就是 id DESC", func(t *testing.T) {
		f := &fakeAdminNodeDB{listRows: rows}
		if _, err := listAdminNodesPage(context.Background(), f, ptrOf(int64(8)), 2, false); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if f.listParam.CursorID == nil || *f.listParam.CursorID != 8 {
			t.Fatalf("游标 id 没有透传：%v", f.listParam.CursorID)
		}
	})

	t.Run("只有 count=true 才跑 COUNT(*)", func(t *testing.T) {
		f := &fakeAdminNodeDB{listRows: rows[:1], total: 87}
		if _, err := listAdminNodesPage(context.Background(), f, nil, 20, false); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if f.countCall != 0 {
			t.Fatal("没传 count=true 时不该跑 COUNT —— db-f1-micro 上那是实打实的开销")
		}
		page, err := listAdminNodesPage(context.Background(), f, nil, 20, true)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if page.Total == nil || *page.Total != 87 {
			t.Fatalf("total 不对：%v", page.Total)
		}
	})

	t.Run("查询失败要上报，不能静默返回空列表", func(t *testing.T) {
		f := &fakeAdminNodeDB{listErr: errors.New("boom")}
		if _, err := listAdminNodesPage(context.Background(), f, nil, 20, false); err == nil {
			t.Fatal("必须上报错误：一个空的节点列表看起来像「一台机器都没有」")
		}
	})
}

// ============================================================
// 2 · GetAdminNode / 8 · ListAdminNodeKeys
// ============================================================

func TestGetAdminNodeView(t *testing.T) {
	f := &fakeAdminNodeDB{nodes: map[int64]dbgen.AdminGetNodeRow{3: nodeRow(3, "香港 01")}}
	v, err := getAdminNodeView(context.Background(), f, 3)
	if err != nil || v.Name != "香港 01" {
		t.Fatalf("正常路径失败：%v %+v", err, v)
	}
	if _, err := getAdminNodeView(context.Background(), f, 999); !errors.Is(err, errAdminNodeNotFound) {
		t.Fatalf("不存在的节点必须 404：%v", err)
	}
}

func TestListAdminNodeKeysView(t *testing.T) {
	f := &fakeAdminNodeDB{
		nodes: map[int64]dbgen.AdminGetNodeRow{3: nodeRow(3, "香港 01")},
		keys: map[int64][]dbgen.AdminListNodeKeysRow{3: {
			{ID: 1, KeyPrefix: "aaaaaa", Name: "旧", IssuedAt: ts(time.Unix(1740000000, 0)),
				RevokedAt: ts(time.Unix(1745000000, 0)), RevokedReason: ptrOf("轮换")},
			{ID: 2, KeyPrefix: "bbbbbb", Name: "新", IssuedAt: ts(time.Unix(1750000000, 0))},
		}},
	}

	keys, err := listAdminNodeKeysView(context.Background(), f, 3)
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	// 🔴 已吊销的密钥必须在列表里：后台要能回答「上个月那把是谁吊的」，
	//    而 revoked_reason 只在这张表里。
	if len(keys) != 2 || keys[0].RevokedAt == nil {
		t.Fatalf("已吊销的密钥必须一并列出：%+v", keys)
	}

	// 🔴 节点不存在时是 404，不是 200 + []。
	//    后者会让「节点 id 打错了」看起来像「这台机器没有密钥」，
	//    而后者会诱导运维去给一个不存在的节点签发密钥。
	if _, err := listAdminNodeKeysView(context.Background(), f, 999); !errors.Is(err, errAdminNodeNotFound) {
		t.Fatalf("节点不存在必须 404，实际 %v", err)
	}
}

// ============================================================
// 3 / 4 · 建 / 改节点（D9）
// ============================================================

func TestValidateNodeUpsert(t *testing.T) {
	cur := adminNodeProjectionFromGet(nodeRow(3, "香港 01"))

	// 🔴🔴 本文件最重要的一条。
	//
	// `AdminUpdateNode` 的 SQL **无条件**写 name/protocol/host/port/region/enabled 六列，
	// 而 AdminNodeUpsert 里除 name/type 之外全是可选。照请求体的零值写下去，
	// 一次「改个显示名」会把 host 抹成空串、把 enabled 抹成 false ——
	// 前者不报错（NOT NULL 但空串合法），后者是「改完名字节点就没人能连了」。
	t.Run("🔴 PATCH 未提供的字段必须回填当前值，不能被抹成零值", func(t *testing.T) {
		in, details := validateNodeUpsert(&gen.AdminNodeUpsert{
			Name: "香港 01（新）", Type: string(dbgen.ServerProtocolVlessReality),
			Reason: "统一命名规范，改显示名",
		}, &cur)
		if details != nil {
			t.Fatalf("不应有校验错误：%+v", details)
		}
		if in.Host != "hk1.example.com" {
			t.Fatalf("host 被抹掉了：%q —— 这台节点从此不可连接，且不报错", in.Host)
		}
		if in.Port != 443 {
			t.Fatalf("port 被抹成 %d", in.Port)
		}
		if in.Region != "asia-east2" {
			t.Fatalf("region 被抹成 %q", in.Region)
		}
		if !in.Enabled {
			t.Fatal("enabled 被抹成 false —— 一次改名把这台机器上的人全踢下线了")
		}
		if in.GroupIDs != nil {
			t.Fatal("没提 group_ids 就不该动分组（nil = 不动，空切片 = 清空）")
		}
	})

	t.Run("显式传 enabled=false 时照传（这是 PATCH 的合法用法）", func(t *testing.T) {
		in, _ := validateNodeUpsert(&gen.AdminNodeUpsert{
			Name: "香港 01", Type: string(dbgen.ServerProtocolVlessReality),
			Enabled: ptrOf(false), Reason: "临时停机维护，先停用",
		}, &cur)
		if in.Enabled {
			t.Fatal("显式的 false 必须被尊重")
		}
	})

	t.Run("L2：reason 少于 8 字符必须被拒", func(t *testing.T) {
		_, details := validateNodeUpsert(&gen.AdminNodeUpsert{
			Name: "香港 01", Type: string(dbgen.ServerProtocolVlessReality), Reason: "改名",
		}, &cur)
		if !hasDetailField(details, "reason") {
			t.Fatalf("reason 太短必须被拒：%+v", details)
		}
	})

	t.Run("🔴 type 不接受 vless 这类折叠名", func(t *testing.T) {
		// 「vless」在库里对应两个值。替调用方猜一个的后果是：
		// 本想建 REALITY 节点，建出来的是应急 CDN 通路，两者 protocol_settings 完全不同，
		// 节点拉到配置后起不来，报的是协议层的错，没人会往「后台把类型猜错了」上想。
		for _, bad := range []string{"vless", "hysteria", "shadowsocks", "", "VLESS_REALITY"} {
			_, details := validateNodeUpsert(&gen.AdminNodeUpsert{
				Name: "香港 01", Type: bad, Reason: "补齐节点协议类型字段",
			}, &cur)
			if !hasDetailField(details, "type") {
				t.Fatalf("type=%q 必须被拒", bad)
			}
		}
		for _, ok := range []string{"vless_reality", "hysteria2", "shadowsocks2022", "vless_xhttp_cdn"} {
			if _, details := validateNodeUpsert(&gen.AdminNodeUpsert{
				Name: "香港 01", Type: ok, Reason: "补齐节点协议类型字段",
			}, &cur); hasDetailField(details, "type") {
				t.Fatalf("type=%q 应当通过", ok)
			}
		}
	})

	t.Run("端口范围在 handler 里判，不留给 DB 的 CHECK", func(t *testing.T) {
		// CHECK 违反在 pgx 侧是 23514，落到 handler 只能是 500 ——
		// 把一次「填错端口」谎报成服务端故障。
		for _, bad := range []int32{0, -1, 65536} {
			_, details := validateNodeUpsert(&gen.AdminNodeUpsert{
				Name: "香港 01", Type: string(dbgen.ServerProtocolVlessReality),
				Port: ptrOf(bad), Reason: "调整节点的监听端口",
			}, &cur)
			if !hasDetailField(details, "port") {
				t.Fatalf("port=%d 必须被拒", bad)
			}
		}
	})

	t.Run("新建时 host / port 必填（没有可回填的当前值）", func(t *testing.T) {
		in, details := validateNodeUpsert(&gen.AdminNodeUpsert{
			Name: "hk2", Type: string(dbgen.ServerProtocolVlessReality), Reason: "新增香港第二台节点",
		}, nil)
		if !hasDetailField(details, "host") || !hasDetailField(details, "port") {
			t.Fatalf("新建缺 host/port 必须被拒：%+v", details)
		}
		_ = in
	})

	t.Run("新建默认停用：还没签发密钥的节点不该立刻进用户订阅", func(t *testing.T) {
		in, details := validateNodeUpsert(&gen.AdminNodeUpsert{
			Name: "hk2", Type: string(dbgen.ServerProtocolVlessReality),
			Host: ptrOf("hk2.example.com"), Port: ptrOf(int32(443)), Reason: "新增香港第二台节点",
		}, nil)
		if details != nil {
			t.Fatalf("不应有校验错误：%+v", details)
		}
		if in.Enabled {
			t.Fatal("新建节点默认必须是停用：它还没有密钥，GCE 实例大概率也没起")
		}
	})

	t.Run("空请求体不能穿过去", func(t *testing.T) {
		if _, details := validateNodeUpsert(nil, nil); len(details) == 0 {
			t.Fatal("nil body 必须被拒")
		}
	})
}

func hasDetailField(details []gen.ErrorDetail, field string) bool {
	for _, d := range details {
		if d.Field == field {
			return true
		}
	}
	return false
}

func TestGenerateNodeCode(t *testing.T) {
	// 🔴 必须是**确定的**：掺随机后缀的话，运维就没有任何办法让 code 与他刚创建的
	//    那台 GCE 实例对上，而 code 一旦写下就再也改不了（AdminUpdateNode 不碰它）。
	a, ok1 := generateNodeCode("HK1", "asia-east2", "hk1.example.com")
	b, ok2 := generateNodeCode("HK1", "asia-east2", "hk1.example.com")
	if !ok1 || !ok2 || a != b || a != "bp-node-hk1" {
		t.Fatalf("同样的输入必须得到同样的 code：%q %q", a, b)
	}
	// 名字全是中文时退到 region（GCE 实例名本来就只能是 ASCII）。
	if c, ok := generateNodeCode("香港一号", "asia-east2", ""); !ok || c != "bp-node-asia-east2" {
		t.Fatalf("应当退到 region：%q %v", c, ok)
	}
	// 三个来源都推不出 slug 时如实失败，而不是编一个出来。
	if _, ok := generateNodeCode("香港一号", "", ""); ok {
		t.Fatal("推不出 slug 时必须失败，让人改名，而不是生成一个与任何实例都不对应的 code")
	}
	if got := nodeCodeSlug("  HK--1 节点 A  "); got != "hk-1-a" {
		t.Fatalf("slug 归一化不对：%q", got)
	}
}

func TestCreateAdminNode(t *testing.T) {
	base := func() (*fakeAdminNodeDB, *fakeAdminNodeTx) {
		db := &fakeAdminNodeDB{nodes: map[int64]dbgen.AdminGetNodeRow{77: nodeRow(77, "hk2")}}
		return db, &fakeAdminNodeTx{db: db}
	}
	in := adminNodeUpsertInput{
		Name: "hk2", Protocol: dbgen.ServerProtocolVlessReality,
		Host: "hk2.example.com", Port: 443, Region: "asia-east2",
		GroupIDs: []int64{1, 2}, Reason: "新增香港第二台节点",
	}

	t.Run("正常路径：建表 + InitNodeRev + 挂分组 + bump user_rev，全在一个事务里", func(t *testing.T) {
		db, tx := base()
		node, err := createAdminNode(context.Background(), tx, nodeActor(), in, "bp-node-hk2")
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if node.Id != 77 {
			t.Fatalf("回读的节点不对：%+v", node)
		}
		// 🔴 漏 InitNodeRev 的现象是「这台机器的 ETag 从此不工作」——
		//    每次 /config 与 /user 都是全量 200，且没有任何报错。
		if !db.did("init_node_rev") {
			t.Fatal("必须在同一事务里 InitNodeRev")
		}
		// 🔴 server_group_map 上没有触发器（0012 的触发器只挂在 users 上），
		//    漏 bump 的现象是同组节点继续 304 返回旧用户列表。
		if len(db.bumpedRevs) != 2 {
			t.Fatalf("每个分组都要 bump user_rev，实际 %v", db.bumpedRevs)
		}
		if !tx.committed {
			t.Fatal("事务应当提交")
		}
	})

	t.Run("visible 恒为 true：否则建出来的节点永远进不了任何人的订阅", func(t *testing.T) {
		db, tx := base()
		if _, err := createAdminNode(context.Background(), tx, nodeActor(), in, "bp-node-hk2"); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		// servers_visible_idx 要求 visible AND enabled，而契约里能改的只有 enabled。
		// visible 建成 false 就再也没有 API 能把它改回来。
		if !db.createdSrv.Visible {
			t.Fatal("visible 必须建成 true —— 契约里没有任何端点能把它改回来")
		}
	})

	// 🔴 protocol_settings 与 tags 都是 NOT NULL DEFAULT，但 CreateServer 的 INSERT
	//    把它们写成了占位参数 —— DEFAULT 不生效，nil 切片会被 pgx 编码成 SQL NULL，
	//    于是**每一次**建节点都撞 NOT NULL 约束、以 500 收场。
	t.Run("🔴 protocol_settings / tags 必须显式给空值，nil 会撞 NOT NULL", func(t *testing.T) {
		db, tx := base()
		if _, err := createAdminNode(context.Background(), tx, nodeActor(), in, "bp-node-hk2"); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if db.createdSrv.ProtocolSettings == nil {
			t.Fatal("protocol_settings 传了 nil —— pgx 会编成 SQL NULL，撞 NOT NULL")
		}
		if string(db.createdSrv.ProtocolSettings) != "{}" {
			t.Fatalf("protocol_settings 应当是空对象：%q", db.createdSrv.ProtocolSettings)
		}
		if db.createdSrv.Tags == nil {
			t.Fatal("tags 传了 nil —— 同上")
		}
	})

	t.Run("code 撞车映射成 422 语义，不是 500", func(t *testing.T) {
		db, tx := base()
		db.createSrvErr = &pgconn.PgError{Code: "23505"}
		_, err := createAdminNode(context.Background(), tx, nodeActor(), in, "bp-node-hk2")
		if !errors.Is(err, errAdminNodeCodeTaken) {
			t.Fatalf("唯一约束冲突必须落到「改个名字」，实际 %v", err)
		}
	})

	t.Run("分组不存在映射成 422 语义，不是 500", func(t *testing.T) {
		db, tx := base()
		db.addGroupErr = &pgconn.PgError{Code: "23503"}
		_, err := createAdminNode(context.Background(), tx, nodeActor(), in, "bp-node-hk2")
		if !errors.Is(err, errAdminNodeBadGroup) {
			t.Fatalf("外键违反必须落到「分组不存在」，实际 %v", err)
		}
	})

	t.Run("审计条目：action 与 target 对得上，且没有 before", func(t *testing.T) {
		db, tx := base()
		if _, err := createAdminNode(context.Background(), tx, nodeActor(), in, "bp-node-hk2"); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if tx.entry.Action != "D9.node.create" || tx.entry.TargetType != "node" || tx.entry.TargetID != "77" {
			t.Fatalf("审计条目不对：%+v", tx.entry)
		}
		if tx.entry.Before != nil {
			t.Fatal("创建操作没有 before（nil → SQL NULL）")
		}
		if tx.entry.Reason != in.Reason {
			t.Fatalf("reason 必须原样进审计：%q", tx.entry.Reason)
		}
		_ = db
	})
}

func TestUpdateAdminNode(t *testing.T) {
	base := func() (*fakeAdminNodeDB, *fakeAdminNodeTx) {
		db := &fakeAdminNodeDB{nodes: map[int64]dbgen.AdminGetNodeRow{3: nodeRow(3, "香港 01")}}
		return db, &fakeAdminNodeTx{db: db}
	}

	// 🔴 与 TestValidateNodeUpsert 的同名用例是两件事：那条测校验层，
	//    这条测**真的交给 SQL 的那组参数**。中间任何一层写漏都会在这里被抓住。
	t.Run("🔴 PATCH 只改名字时，交给 AdminUpdateNode 的六列不能有被抹掉的", func(t *testing.T) {
		db, tx := base()
		_, details, err := updateAdminNode(context.Background(), tx, nodeActor(), 3, &gen.AdminNodeUpsert{
			Name: "香港 01（改）", Type: string(dbgen.ServerProtocolVlessReality),
			Reason: "统一命名规范，改显示名",
		})
		if err != nil || details != nil {
			t.Fatalf("不应报错：%v %+v", err, details)
		}
		got := db.updated
		if got.Host != "hk1.example.com" || got.Port != 443 || got.Region != "asia-east2" || !got.Enabled {
			t.Fatalf("有字段被抹成零值了：%+v", got)
		}
	})

	t.Run("🔴 必须在同一事务里 bump config_rev", func(t *testing.T) {
		db, tx := base()
		if _, _, err := updateAdminNode(context.Background(), tx, nodeActor(), 3, &gen.AdminNodeUpsert{
			Name: "香港 01", Type: string(dbgen.ServerProtocolVlessReality),
			Host: ptrOf("hk1-new.example.com"), Reason: "机房迁移，换接入域名",
		}); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		// 不 bump 的话节点会一直拿旧配置的 304 —— 改了等于没改，
		// 而后台显示的是新值，两边都不报错。
		if len(db.bumpedCfg) != 1 || db.bumpedCfg[0] != 3 {
			t.Fatalf("必须 bump config_rev，实际 %v", db.bumpedCfg)
		}
	})

	t.Run("分组增删：加进来的和摘出去的都要 bump user_rev", func(t *testing.T) {
		db, tx := base() // 当前分组 {1,2}
		if _, _, err := updateAdminNode(context.Background(), tx, nodeActor(), 3, &gen.AdminNodeUpsert{
			Name: "香港 01", Type: string(dbgen.ServerProtocolVlessReality),
			GroupIds: ptrOf([]int64{2, 3}), Reason: "把这台机器挪到高级分组",
		}); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if len(db.addedGrp) != 1 || db.addedGrp[0] != 3 {
			t.Fatalf("应当只加 3：%v", db.addedGrp)
		}
		if len(db.removedGrp) != 1 || db.removedGrp[0] != 1 {
			t.Fatalf("应当只摘 1：%v", db.removedGrp)
		}
		// 🔴 摘出去的那个分组里的其它节点，可见用户集合同样变了，不 bump 会继续 304。
		if len(db.bumpedRevs) != 2 {
			t.Fatalf("加和摘的分组都要 bump，实际 %v", db.bumpedRevs)
		}
	})

	t.Run("不提 group_ids 就完全不动分组", func(t *testing.T) {
		db, tx := base()
		if _, _, err := updateAdminNode(context.Background(), tx, nodeActor(), 3, &gen.AdminNodeUpsert{
			Name: "香港 01", Type: string(dbgen.ServerProtocolVlessReality), Reason: "只改一下节点显示名称",
		}); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if len(db.addedGrp)+len(db.removedGrp) != 0 {
			t.Fatal("没提 group_ids 时不该动分组")
		}
	})

	t.Run("参数没收齐时不许提交：reason 太短 → 一条写入都没有", func(t *testing.T) {
		db, tx := base()
		_, details, err := updateAdminNode(context.Background(), tx, nodeActor(), 3, &gen.AdminNodeUpsert{
			Name: "香港 01", Type: string(dbgen.ServerProtocolVlessReality), Reason: "改名",
		})
		if !errors.Is(err, errAdminNodeInvalidUpsert) || !hasDetailField(details, "reason") {
			t.Fatalf("reason 太短必须被拒：%v %+v", err, details)
		}
		if db.did("update_node") || tx.committed {
			t.Fatal("校验没过却写了库")
		}
	})

	t.Run("节点不存在 → 404 语义", func(t *testing.T) {
		_, tx := base()
		if _, _, err := updateAdminNode(context.Background(), tx, nodeActor(), 999, &gen.AdminNodeUpsert{
			Name: "x", Type: string(dbgen.ServerProtocolVlessReality), Reason: "补齐节点的基础信息",
		}); !errors.Is(err, errAdminNodeNotFound) {
			t.Fatalf("应当是 404：%v", err)
		}
	})

	t.Run("审计前后像来自同一条语句的 prev.*，不是我们自己先读的那一份", func(t *testing.T) {
		db, tx := base()
		if _, _, err := updateAdminNode(context.Background(), tx, nodeActor(), 3, &gen.AdminNodeUpsert{
			Name: "香港 01（改）", Type: string(dbgen.ServerProtocolVlessReality), Reason: "统一命名规范，改显示名",
		}); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		before, _ := tx.entry.Before.(map[string]any)
		after, _ := tx.entry.After.(map[string]any)
		if before["name"] != "香港 01" || after["name"] != "香港 01（改）" {
			t.Fatalf("前后像不对：%v → %v", before["name"], after["name"])
		}
		_ = db
	})
}

// ============================================================
// 5 · 删节点（D4）
// ============================================================

func TestDeleteAdminNode(t *testing.T) {
	base := func() (*fakeAdminNodeDB, *fakeAdminNodeTx) {
		db := &fakeAdminNodeDB{nodes: map[int64]dbgen.AdminGetNodeRow{3: nodeRow(3, "香港 01")}}
		return db, &fakeAdminNodeTx{db: db}
	}

	// 🔴 「参数没收齐时不许提交」的核心用例：确认串不匹配时**一行都不能删**。
	t.Run("🔴 L1 确认串不匹配 → 拒绝，且没有发生软删", func(t *testing.T) {
		db, tx := base()
		facts, err := deleteAdminNode(context.Background(), tx, nodeActor(), 3, "香港 02", "机房退租，节点下线")
		if !errors.Is(err, errAdminNodeConfirmMismatch) {
			t.Fatalf("确认串不匹配必须被拒：%v", err)
		}
		if db.did("soft_delete") || tx.committed {
			t.Fatal("确认串没对上却把节点删了")
		}
		// 🔴 两个在线人数必须分开带回来给确认框：上报值在节点失联时是旧值、
		//    数据库重启后是 0，而「0 人在线」恰恰是让人放心点删除的那个数字。
		if facts.ReportedOnlineUsers != 0 || facts.ObservedOnlineUsers != 41 {
			t.Fatalf("两个在线人数必须分别带回，实际 %+v", facts)
		}
		if facts.ActiveKeyCount != 2 {
			t.Fatal("密钥数也要带回：删节点会 CASCADE 掉它的 server_keys")
		}
	})

	t.Run("确认串对上 → 软删 + 审计", func(t *testing.T) {
		db, tx := base()
		if _, err := deleteAdminNode(context.Background(), tx, nodeActor(), 3, "香港 01", "机房退租，节点下线"); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !db.did("soft_delete") || !tx.committed {
			t.Fatal("应当软删并提交")
		}
		if tx.entry.Action != "D4.node.delete" || tx.entry.TargetID != "3" {
			t.Fatalf("审计条目不对：%+v", tx.entry)
		}
		// 🔴 两个在线人数进快照：server_online_state 是 UNLOGGED 表，重启即失，
		//    事后追责「删的时候上面有没有人」只有这一处记录。
		before, _ := tx.entry.Before.(map[string]any)
		if _, ok := before["reported_online_users"]; !ok {
			t.Fatal("审计快照里缺 reported_online_users")
		}
		if _, ok := before["observed_online_users"]; !ok {
			t.Fatal("审计快照里缺 observed_online_users")
		}
	})

	t.Run("节点不存在 → 404 语义", func(t *testing.T) {
		_, tx := base()
		if _, err := deleteAdminNode(context.Background(), tx, nodeActor(), 999, "x", "机房退租，节点下线"); !errors.Is(err, errAdminNodeNotFound) {
			t.Fatalf("应当是 404：%v", err)
		}
	})
}

// 「参数没收齐时不许提交」在 handler 层的两条（这两条在碰数据库之前就返回，
// 所以可以用一个没有 db 的 Server 打）。
func TestDeleteAdminNodeHandlerRejectsIncompleteRequests(t *testing.T) {
	s := adminNodeTestServer()
	ctx := adminCtx(middleware.RoleOwner)

	t.Run("L2：reason 太短 → 422，且根本没去动数据库", func(t *testing.T) {
		resp, err := s.DeleteAdminNode(ctx, gen.DeleteAdminNodeRequestObject{
			Id: 3, Body: &gen.ConfirmedReasonRequest{Confirmation: "香港 01", Reason: "退租"},
		})
		if err != nil {
			t.Fatalf("不应返回 error：%v", err)
		}
		if _, ok := resp.(gen.DeleteAdminNode422JSONResponse); !ok {
			t.Fatalf("应当是 422，实际 %T", resp)
		}
	})

	t.Run("空请求体 → 422", func(t *testing.T) {
		resp, err := s.DeleteAdminNode(ctx, gen.DeleteAdminNodeRequestObject{Id: 3})
		if err != nil {
			t.Fatalf("不应返回 error：%v", err)
		}
		if _, ok := resp.(gen.DeleteAdminNode422JSONResponse); !ok {
			t.Fatalf("应当是 422，实际 %T", resp)
		}
	})

	t.Run("L4：support 角色 → 403，且不会碰数据库", func(t *testing.T) {
		resp, err := s.DeleteAdminNode(adminCtx(middleware.RoleSupport), gen.DeleteAdminNodeRequestObject{
			Id: 3, Body: &gen.ConfirmedReasonRequest{Confirmation: "香港 01", Reason: "机房退租，节点下线"},
		})
		if err != nil {
			t.Fatalf("不应返回 error：%v", err)
		}
		if _, ok := resp.(gen.DeleteAdminNode403JSONResponse); !ok {
			t.Fatalf("应当是 403，实际 %T", resp)
		}
	})
}

// D9 的两个写 operation 在 handler 层同样要挡住 support 与短 reason。
func TestNodeWriteHandlersEnforceRoleAndReason(t *testing.T) {
	s := adminNodeTestServer()
	support := adminCtx(middleware.RoleSupport)
	owner := adminCtx(middleware.RoleOwner)
	body := &gen.AdminNodeUpsert{
		Name: "hk2", Type: string(dbgen.ServerProtocolVlessReality),
		Host: ptrOf("hk2.example.com"), Port: ptrOf(int32(443)), Reason: "新增香港第二台节点",
	}

	if resp, _ := s.CreateAdminNode(support, gen.CreateAdminNodeRequestObject{Body: body}); resp == nil {
		t.Fatal("不应返回 nil")
	} else if _, ok := resp.(gen.CreateAdminNode403JSONResponse); !ok {
		t.Fatalf("CreateAdminNode 对 support 应当 403，实际 %T", resp)
	}
	if resp, _ := s.UpdateAdminNode(support, gen.UpdateAdminNodeRequestObject{Id: 3, Body: body}); resp == nil {
		t.Fatal("不应返回 nil")
	} else if _, ok := resp.(gen.UpdateAdminNode403JSONResponse); !ok {
		t.Fatalf("UpdateAdminNode 对 support 应当 403，实际 %T", resp)
	}
	// 停用会让人掉线，同样只能是 owner/admin。
	if resp, _ := s.DisableAdminNode(support, gen.DisableAdminNodeRequestObject{Id: 3}); resp == nil {
		t.Fatal("不应返回 nil")
	} else if _, ok := resp.(gen.DisableAdminNode403JSONResponse); !ok {
		t.Fatalf("DisableAdminNode 对 support 应当 403，实际 %T", resp)
	}
	if resp, _ := s.EnableAdminNode(support, gen.EnableAdminNodeRequestObject{Id: 3}); resp == nil {
		t.Fatal("不应返回 nil")
	} else if _, ok := resp.(gen.EnableAdminNode403JSONResponse); !ok {
		t.Fatalf("EnableAdminNode 对 support 应当 403，实际 %T", resp)
	}

	// L2：owner 也不能用一句「改名」就建节点。
	short := *body
	short.Reason = "建节点"
	if resp, _ := s.CreateAdminNode(owner, gen.CreateAdminNodeRequestObject{Body: &short}); resp == nil {
		t.Fatal("不应返回 nil")
	} else if _, ok := resp.(gen.CreateAdminNode422JSONResponse); !ok {
		t.Fatalf("reason 太短应当 422，实际 %T", resp)
	}
}

// 路由没挂管理面鉴权时必须是 500，不是 403。
//
// 403 是「你没权限」，而这里的真相是「我们把路由配错了」——
// 用 403 会让一次配置事故看起来像一次正常的权限拒绝，没有人会去查。
func TestAdminNodeHandlersFailLoudWithoutAdminContext(t *testing.T) {
	s := adminNodeTestServer()
	resp, err := s.ListAdminNodes(context.Background(), gen.ListAdminNodesRequestObject{})
	if err != nil {
		t.Fatalf("不应返回 error：%v", err)
	}
	if _, ok := resp.(gen.ListAdminNodes500JSONResponse); !ok {
		t.Fatalf("缺管理员身份应当 500（装配错误），实际 %T", resp)
	}
}

// ============================================================
// 6 / 7 · 启停
// ============================================================

func TestSetAdminNodeEnabled(t *testing.T) {
	base := func(enabled bool) (*fakeAdminNodeDB, *fakeAdminNodeTx) {
		row := nodeRow(3, "香港 01")
		row.Enabled = enabled
		db := &fakeAdminNodeDB{nodes: map[int64]dbgen.AdminGetNodeRow{3: row}}
		return db, &fakeAdminNodeTx{db: db}
	}

	t.Run("停用的 action 带 D4 前缀，启用不带", func(t *testing.T) {
		db, tx := base(true)
		if _, _, err := setAdminNodeEnabled(context.Background(), tx, nodeActor(), 3, false); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if tx.entry.Action != "D4.node.disable" {
			t.Fatalf("停用应当带 D4 前缀（让「查所有 D4」能命中它）：%q", tx.entry.Action)
		}
		if len(db.enabledSet) != 1 || db.enabledSet[0].Enabled {
			t.Fatalf("参数不对：%+v", db.enabledSet)
		}

		_, tx2 := base(false)
		if _, _, err := setAdminNodeEnabled(context.Background(), tx2, nodeActor(), 3, true); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if tx2.entry.Action != "node.enable" {
			t.Fatalf("启用不该带 D4 前缀：%q", tx2.entry.Action)
		}
	})

	// 🔴 「停用一台本来就停着的节点」与「停用一台在跑的节点」是两件事
	//    （后者会让人掉线），事后只有 before_enabled 这一列能分辨。
	t.Run("审计记 before_enabled，用来分辨这次停用有没有让人掉线", func(t *testing.T) {
		_, tx := base(true)
		_, wasEnabled, err := setAdminNodeEnabled(context.Background(), tx, nodeActor(), 3, false)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !wasEnabled {
			t.Fatal("改前值应当是 true")
		}
		before, _ := tx.entry.Before.(map[string]any)
		if before["enabled"] != true {
			t.Fatalf("审计前像不对：%+v", before)
		}
	})

	// 🔴 契约给 enable/disable 没有请求体，L2 无从取值。
	//    编一句「管理员操作」塞进去比留空更坏：它会让事后读审计的人
	//    以为当时真的有人给过理由。
	t.Run("reason 保持空，不编造", func(t *testing.T) {
		_, tx := base(true)
		if _, _, err := setAdminNodeEnabled(context.Background(), tx, nodeActor(), 3, false); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if tx.entry.Reason != "" {
			t.Fatalf("契约没有 reason 字段，绝不能编一个：%q", tx.entry.Reason)
		}
	})

	t.Run("节点不存在 → 404 语义", func(t *testing.T) {
		_, tx := base(true)
		if _, _, err := setAdminNodeEnabled(context.Background(), tx, nodeActor(), 999, false); !errors.Is(err, errAdminNodeNotFound) {
			t.Fatalf("应当是 404：%v", err)
		}
	})
}

// ============================================================
// 9 · 签发密钥（D5 第 1 步）
// ============================================================

var nodeKeyTokenShape = regexp.MustCompile(`^bpn_[a-z2-7]{6}_[A-Za-z0-9_-]{43}$`)

func TestGenerateNodeKey(t *testing.T) {
	const pepper = "test-pepper"

	k, err := generateNodeKey(pepper)
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if !nodeKeyTokenShape.MatchString(k.Secret) {
		t.Fatalf("密钥串形态不对：%q（契约 §3.2.1：bpn_<key_id>_<secret>）", k.Secret)
	}
	if k.KeyID != k.Secret[4:10] {
		t.Fatalf("key_id 必须是密钥串里的那一段：%q vs %q", k.KeyID, k.Secret)
	}

	// 🔴 哈希口径必须与 mw.AuthenticateNode 里那一行**逐字一致**：
	//    sha256(pepper + 整串)。少拼 pepper、或者只哈希 secret 段，
	//    签出来的密钥在鉴权时永远查不到 —— 现象是「新签的密钥一用就 401」，
	//    而人的第一反应会去查节点配置，不是查签发口径。
	want := sha256.Sum256([]byte(pepper + k.Secret))
	if string(k.Hash) != string(want[:]) {
		t.Fatal("key_hash 必须等于 sha256(pepper + 完整密钥串)")
	}

	// 🔴 节点鉴权侧还有一道 plausibleToken 形态闸（24–128 位 [A-Za-z0-9_-]）。
	//    签出来的串过不了它的话，密钥在**查库之前**就被拒了。
	if len(k.Secret) < 24 || len(k.Secret) > 128 {
		t.Fatalf("长度 %d 落在 mw.plausibleToken 的 24–128 之外", len(k.Secret))
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(k.Secret) {
		t.Fatalf("字符集过不了 mw.plausibleToken：%q", k.Secret)
	}

	// 每次都不一样（CSPRNG）。
	seen := map[string]bool{}
	for range 64 {
		k2, err := generateNodeKey(pepper)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if seen[k2.Secret] {
			t.Fatal("签出了两把一样的密钥")
		}
		seen[k2.Secret] = true
	}
}

func TestResolveNodeKeyScopes(t *testing.T) {
	t.Run("缺省是五个，且不含 node:status:write", func(t *testing.T) {
		got, d := resolveNodeKeyScopes(nil)
		if d != nil {
			t.Fatalf("不应报错：%+v", d)
		}
		if len(got) != 5 {
			t.Fatalf("缺省应当是五个：%v", got)
		}
		for _, s := range got {
			if s == string(gen.NodeStatusWrite) {
				t.Fatal("node:status:write 不在缺省里：默认给出去等于让每把密钥都能改在线态展示")
			}
		}
		// 🔴 必须显式传：0004 给这一列的 DEFAULT 是 '{uniproxy}'，
		//    靠默认值会签发出一把 scope 谁也不认识的密钥。
		if len(got) == 0 {
			t.Fatal("绝不能把空 scopes 交给 CreateServerKey")
		}
	})

	t.Run("未知 scope 被拒（精确匹配，非前缀）", func(t *testing.T) {
		for _, bad := range []gen.NodeScope{"node:alive", "node:config:read:extra", "uniproxy", ""} {
			if _, d := resolveNodeKeyScopes(&[]gen.NodeScope{bad}); d == nil {
				t.Fatalf("scope %q 必须被拒", bad)
			}
		}
	})

	t.Run("显式的空数组被拒，不当作「用默认值」", func(t *testing.T) {
		// 一把零 scope 的密钥能通过鉴权，但每个 HasScope 都是 false ——
		// 节点在所有端点上拿到 403，看起来像被封禁而不是像配错了。
		if _, d := resolveNodeKeyScopes(&[]gen.NodeScope{}); d == nil {
			t.Fatal("空数组必须被拒")
		}
	})

	t.Run("重复的 scope 去重", func(t *testing.T) {
		got, d := resolveNodeKeyScopes(&[]gen.NodeScope{gen.NodeConfigRead, gen.NodeConfigRead})
		if d != nil || len(got) != 1 {
			t.Fatalf("应当去重成一个：%v %+v", got, d)
		}
	})
}

func TestCreateAdminNodeKey(t *testing.T) {
	const pepper = "test-pepper"
	base := func(active int64) (*fakeAdminNodeDB, *fakeAdminNodeTx) {
		db := &fakeAdminNodeDB{
			nodes:      map[int64]dbgen.AdminGetNodeRow{3: nodeRow(3, "香港 01")},
			byPrefix:   map[string][]dbgen.AdminGetNodeKeyByPrefixRow{},
			activeKeys: dbgen.AdminCountActiveNodeKeysRow{ActiveKeys: active},
		}
		return db, &fakeAdminNodeTx{db: db}
	}
	scopes := []string{string(gen.NodeConfigRead), string(gen.NodeUsersRead)}

	t.Run("正常路径：落库的是哈希，返回体里才是明文", func(t *testing.T) {
		db, tx := base(1)
		out, err := createAdminNodeKey(context.Background(), tx, nodeActor(), 3, "2026-08 轮换",
			scopes, pgtype.Timestamptz{}, pepper)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !nodeKeyTokenShape.MatchString(out.Secret) {
			t.Fatalf("明文形态不对：%q", out.Secret)
		}
		// key_prefix 存的就是路径参数将来会带来的那六个字符 ——
		// 否则 D5 第 2 步永远拼不出要查的串。
		if db.createdKey.KeyPrefix != out.Key.KeyId || len(db.createdKey.KeyPrefix) != 6 {
			t.Fatalf("key_prefix 必须等于 key_id：%q vs %q", db.createdKey.KeyPrefix, out.Key.KeyId)
		}
		want := sha256.Sum256([]byte(pepper + out.Secret))
		if string(db.createdKey.KeyHash) != string(want[:]) {
			t.Fatal("落库的必须是 sha256(pepper + 明文)")
		}
		if db.createdKey.CreatedBy == nil || *db.createdKey.CreatedBy != 5 {
			t.Fatalf("created_by 应当是操作人：%v", db.createdKey.CreatedBy)
		}
		if len(db.createdKey.Scopes) != 2 {
			t.Fatalf("scopes 必须显式传：%v", db.createdKey.Scopes)
		}
	})

	// 🔴 任务书点名：明文不落库、不进日志、不进审计。
	//    audit_logs 是 append-only 且永不删除的 —— 明文进去就永远在里面，
	//    而且会被每一个有 GET /admin/audit 权限的人看到。
	t.Run("🔴 明文绝不进审计快照，也绝不进任何落库参数", func(t *testing.T) {
		db, tx := base(0)
		out, err := createAdminNodeKey(context.Background(), tx, nodeActor(), 3, "2026-08 轮换",
			scopes, pgtype.Timestamptz{}, pepper)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		blob, err := json.Marshal(map[string]any{"before": tx.entry.Before, "after": tx.entry.After})
		if err != nil {
			t.Fatalf("审计快照必须可序列化：%v", err)
		}
		if strings.Contains(string(blob), out.Secret) {
			t.Fatalf("审计快照里出现了明文密钥：%s", blob)
		}
		// 连 secret 的随机段都不能出现（防「只截一半」这种写法）。
		if strings.Contains(string(blob), out.Secret[11:]) {
			t.Fatalf("审计快照里出现了密钥的随机段：%s", blob)
		}
		// 哈希也不该进审计：它虽然不可逆，但离线爆破 32 字节随机值不成立不代表
		// 要把它多复制一份到一张永不删除的表里。
		if strings.Contains(string(blob), "key_hash") {
			t.Fatalf("审计快照里出现了 key_hash：%s", blob)
		}
		if db.createdKey.KeyPrefix == "" {
			t.Fatal("落库参数缺 key_prefix")
		}
	})

	// 🔴 «同时有效 ≤ 2» 闸：轮换期两把是正常的，第三把说明上一次轮换没做完。
	t.Run("已有两把有效密钥时拒绝签发第三把", func(t *testing.T) {
		db, tx := base(2)
		_, err := createAdminNodeKey(context.Background(), tx, nodeActor(), 3, "又一把",
			scopes, pgtype.Timestamptz{}, pepper)
		if !errors.Is(err, errAdminNodeKeyTooMany) {
			t.Fatalf("应当被闸住：%v", err)
		}
		if db.did("create_key") || tx.committed {
			t.Fatal("被闸住了却还是签发了")
		}
	})

	t.Run("节点不存在 → 404 语义，且不会签给一个不存在的节点", func(t *testing.T) {
		db, tx := base(0)
		if _, err := createAdminNodeKey(context.Background(), tx, nodeActor(), 999, "x",
			scopes, pgtype.Timestamptz{}, pepper); !errors.Is(err, errAdminNodeNotFound) {
			t.Fatalf("应当是 404：%v", err)
		}
		if db.did("create_key") {
			t.Fatal("给一个不存在的节点签发了密钥")
		}
	})

	// 🔴 撞前缀的防守在签发这一侧：撞上之后 D5 第 2 步会拿到 2 行，
	//    那时唯一安全的回应是 500 —— 也就是那把密钥再也吊销不了。
	t.Run("生成的 key_id 撞车时拒绝签发", func(t *testing.T) {
		db, tx := base(0)
		// 让任何前缀都「已存在」：key_id 是随机的，塞一张 map 命不中，
		// 所以在假实现上开一个开关。
		db.prefixAlwaysHit = true
		_, err := createAdminNodeKey(context.Background(), tx, nodeActor(), 3, "x",
			scopes, pgtype.Timestamptz{}, pepper)
		if err == nil {
			t.Fatal("撞车必须失败，而不是签出一把将来吊销不掉的密钥")
		}
		if db.did("create_key") {
			t.Fatal("撞车了却还是签发了")
		}
	})
}

// ============================================================
// 10 · 吊销密钥（D5 第 2 步）—— 本文件最重要的一组
// ============================================================

func keyRow(id, serverID int64, prefix string, revoked bool, witness int64) dbgen.AdminGetNodeKeyByPrefixRow {
	r := dbgen.AdminGetNodeKeyByPrefixRow{
		ID: id, ServerID: serverID, KeyPrefix: prefix, Name: "旧密钥",
		IssuedAt: ts(time.Unix(1740000000, 0)), WitnessCount: witness, Active: !revoked,
	}
	if revoked {
		r.RevokedAt = ts(time.Unix(1745000000, 0))
	}
	return r
}

func TestRevokeAdminNodeKey(t *testing.T) {
	base := func(rows []dbgen.AdminGetNodeKeyByPrefixRow) (*fakeAdminNodeDB, *fakeAdminNodeTx) {
		db := &fakeAdminNodeDB{byPrefix: map[string][]dbgen.AdminGetNodeKeyByPrefixRow{"aaaaaa": rows}}
		return db, &fakeAdminNodeTx{db: db}
	}

	// 🔴🔴 任务书点名的那一条：「一步吊销」必须 409。
	//
	// 这是 D5 存在的全部意义 —— 没有见证密钥就吊销旧的，
	// 节点会在下一次 60 秒轮询时失联，而失联之后没有任何一条路能把新密钥送进去。
	t.Run("🔴 没有见证密钥（一步吊销）→ 409，且一行都没改", func(t *testing.T) {
		db, tx := base([]dbgen.AdminGetNodeKeyByPrefixRow{keyRow(9, 3, "aaaaaa", false, 0)})
		_, err := revokeAdminNodeKey(context.Background(), tx, nodeActor(), "aaaaaa", "D5 轮换")
		if !errors.Is(err, errAdminNodeKeyNoWitness) {
			t.Fatalf("一步吊销必须被拒：%v", err)
		}
		if db.did("revoke_key") || tx.committed {
			t.Fatal("没有见证密钥却把旧密钥吊销了 —— 节点将在 60 秒内失联")
		}
	})

	// 🔴 读到写之间见证没了（轮换期节点每 60 秒改一次 last_used_at，
	//    另一个管理员可能同时在吊销另一把）。UPDATE 的 EXISTS 正确地拒绝了，
	//    这时**仍然是 409**，不是「读的时候明明可以」的 500 ——
	//    数据库刚刚挡住了一次会让节点失联的操作，那是它该做的事。
	t.Run("🔴 读到写之间见证消失 → 仍然 409（真正的拒绝点在 UPDATE 的 EXISTS 里）", func(t *testing.T) {
		db, tx := base([]dbgen.AdminGetNodeKeyByPrefixRow{keyRow(9, 3, "aaaaaa", false, 1)})
		db.revokeNoRows = true
		_, err := revokeAdminNodeKey(context.Background(), tx, nodeActor(), "aaaaaa", "D5 轮换")
		if !errors.Is(err, errAdminNodeKeyNoWitness) {
			t.Fatalf("UPDATE 0 行必须落到 409：%v", err)
		}
	})

	// 🔴🔴 任务书点名的那一条：AdminGetNodeKeyByPrefix 是 :many，
	//    命中多行时必须**拒绝**，而不是取第一行。
	//    server_keys.key_prefix 上没有唯一索引，取第一行有一半概率吊销掉另一把密钥
	//    = 节点失联，且事后从日志里看不出任何异常。
	t.Run("🔴 同一 key_id 命中两行 → 500（不是 409），且一把都不吊销", func(t *testing.T) {
		db, tx := base([]dbgen.AdminGetNodeKeyByPrefixRow{
			keyRow(9, 3, "aaaaaa", false, 1),
			keyRow(10, 4, "aaaaaa", false, 1),
		})
		_, err := revokeAdminNodeKey(context.Background(), tx, nodeActor(), "aaaaaa", "D5 轮换")
		if !errors.Is(err, errAdminNodeKeyAmbiguous) {
			t.Fatalf("撞前缀必须是「我们的数据坏了」而不是状态冲突：%v", err)
		}
		if db.did("revoke_key") {
			t.Fatal("命中多行却还是吊销了 —— 有一半概率吊掉的是另一把密钥")
		}
		// 明确不是 409：409 说的是「你要做的事与当前状态冲突」，
		// 而这里调用方什么也没做错。
		if errors.Is(err, errAdminNodeKeyNoWitness) || errors.Is(err, errAdminNodeKeyRevoked) {
			t.Fatal("不能落成 409")
		}
	})

	t.Run("密钥不存在 → 404", func(t *testing.T) {
		_, tx := base(nil)
		if _, err := revokeAdminNodeKey(context.Background(), tx, nodeActor(), "zzzzzz", "D5 轮换"); !errors.Is(err, errAdminNodeKeyNotFound) {
			t.Fatalf("应当是 404：%v", err)
		}
	})

	t.Run("已经吊销过 → 409，且不重复写", func(t *testing.T) {
		db, tx := base([]dbgen.AdminGetNodeKeyByPrefixRow{keyRow(9, 3, "aaaaaa", true, 1)})
		if _, err := revokeAdminNodeKey(context.Background(), tx, nodeActor(), "aaaaaa", "D5 轮换"); !errors.Is(err, errAdminNodeKeyRevoked) {
			t.Fatalf("应当是 409：%v", err)
		}
		if db.did("revoke_key") {
			t.Fatal("已吊销的密钥不该再写一次（revoked_at 会被刷新，改吊人也会被覆盖）")
		}
	})

	t.Run("有见证 → 吊销成功 + 审计", func(t *testing.T) {
		db, tx := base([]dbgen.AdminGetNodeKeyByPrefixRow{keyRow(9, 3, "aaaaaa", false, 1)})
		serverID, err := revokeAdminNodeKey(context.Background(), tx, nodeActor(), "aaaaaa", "D5 轮换第 2 步")
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if serverID != 3 {
			t.Fatalf("应当带回节点 id：%d", serverID)
		}
		if len(db.revoked) != 1 || db.revoked[0].KeyID != 9 {
			t.Fatalf("必须按主键吊销（不是按 prefix）：%+v", db.revoked)
		}
		if tx.entry.Action != "D5.node_key.revoke" {
			t.Fatalf("审计 action 不对：%q", tx.entry.Action)
		}
		// 见证数进快照：事后要能回答「当时凭什么允许吊销」。
		before, _ := tx.entry.Before.(map[string]any)
		if before["witness_count"] != int64(1) {
			t.Fatalf("审计前像里缺 witness_count：%+v", before)
		}
	})
}

func TestPlausibleNodeKeyID(t *testing.T) {
	if !plausibleNodeKeyID("7f3a2c") {
		t.Fatal("契约形状必须通过")
	}
	// 历史上手工灌进去的密钥（bpk_ 形状）不能被形态闸挡死，
	// 否则它们永远吊销不掉 —— 库里 key_prefix 是 text，不是 char(6)。
	if !plausibleNodeKeyID("bpk_smoke") {
		t.Fatal("库里的历史形状也要能查（否则那些密钥永远吊销不掉）")
	}
	if plausibleNodeKeyID("") || plausibleNodeKeyID(strings.Repeat("a", 65)) {
		t.Fatal("空串与超长串必须被挡在数据库之外")
	}
}

// ============================================================
// 🔴 审计写失败 → 业务写入回滚（§6.3 第 1 条）
// ============================================================

// 这是本文件的压轴用例，也是任务书的硬要求。
//
// 「异步写审计等于承认审计可能缺失」——§6.3 的原话。六条写路径全部走同一个
// adminNodeTxRunner，所以这条性质只需要在每条路径上确认一次「审计失败时业务没留下」。
// 表驱动是刻意的：将来加第七条写路径而忘了走事务时，
// 正确的现象是这张表少一行（有人得来加），而不是那条路径静默地绕开审计。
func TestAdminNodeWritesRollBackWhenAuditFails(t *testing.T) {
	auditBoom := errors.New("写审计日志失败: 磁盘满")

	newDB := func() *fakeAdminNodeDB {
		return &fakeAdminNodeDB{
			nodes:      map[int64]dbgen.AdminGetNodeRow{3: nodeRow(3, "香港 01"), 77: nodeRow(77, "hk2")},
			byPrefix:   map[string][]dbgen.AdminGetNodeKeyByPrefixRow{"aaaaaa": {keyRow(9, 3, "aaaaaa", false, 1)}},
			activeKeys: dbgen.AdminCountActiveNodeKeysRow{ActiveKeys: 1},
		}
	}

	cases := []struct {
		name string
		// wantWrite 是这条路径在事务里应当发生过的那次业务写入的标记。
		wantWrite string
		run       func(tx adminNodeTxRunner) error
	}{
		{"CreateAdminNode", "create_server", func(tx adminNodeTxRunner) error {
			_, err := createAdminNode(context.Background(), tx, nodeActor(), adminNodeUpsertInput{
				Name: "hk2", Protocol: dbgen.ServerProtocolVlessReality,
				Host: "hk2.example.com", Port: 443, Reason: "新增香港第二台节点",
			}, "bp-node-hk2")
			return err
		}},
		{"UpdateAdminNode", "update_node", func(tx adminNodeTxRunner) error {
			_, _, err := updateAdminNode(context.Background(), tx, nodeActor(), 3, &gen.AdminNodeUpsert{
				Name: "香港 01（改）", Type: string(dbgen.ServerProtocolVlessReality),
				Reason: "统一命名规范，改显示名",
			})
			return err
		}},
		{"DeleteAdminNode", "soft_delete", func(tx adminNodeTxRunner) error {
			_, err := deleteAdminNode(context.Background(), tx, nodeActor(), 3, "香港 01", "机房退租，节点下线")
			return err
		}},
		{"DisableAdminNode", "set_enabled", func(tx adminNodeTxRunner) error {
			_, _, err := setAdminNodeEnabled(context.Background(), tx, nodeActor(), 3, false)
			return err
		}},
		{"CreateAdminNodeKey", "create_key", func(tx adminNodeTxRunner) error {
			_, err := createAdminNodeKey(context.Background(), tx, nodeActor(), 3, "2026-08 轮换",
				[]string{string(gen.NodeConfigRead)}, pgtype.Timestamptz{}, "test-pepper")
			return err
		}},
		{"RevokeAdminNodeKey", "revoke_key", func(tx adminNodeTxRunner) error {
			_, err := revokeAdminNodeKey(context.Background(), tx, nodeActor(), "aaaaaa", "D5 轮换第 2 步")
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name+"：审计写成功时业务写入确实发生了", func(t *testing.T) {
			db := newDB()
			tx := &fakeAdminNodeTx{db: db}
			if err := tc.run(tx); err != nil {
				t.Fatalf("正常路径不应报错：%v", err)
			}
			if !db.did(tc.wantWrite) {
				t.Fatalf("这条路径应当写过 %q，实际 %v", tc.wantWrite, db.applied)
			}
			if !tx.committed {
				t.Fatal("应当提交")
			}
			// 审计条目的三个必填字段一个都不能空 —— audit.validate 会因此让整个事务回滚。
			if tx.entry.Action == "" || tx.entry.TargetType == "" || tx.entry.TargetID == "" {
				t.Fatalf("审计条目缺必填字段，audit.Write 会拒绝并回滚整个操作：%+v", tx.entry)
			}
			// 审计主体必须带上操作人与来源 IP：audit.validateActor 缺一个就整体失败。
			if tx.actor.AdminID == 0 || tx.actor.Email == "" || !tx.actor.IP.IsValid() {
				t.Fatalf("审计主体不完整：%+v", tx.actor)
			}
		})

		t.Run(tc.name+"：🔴 审计写失败 → 业务写入必须一起回滚", func(t *testing.T) {
			db := newDB()
			tx := &fakeAdminNodeTx{db: db, auditErr: auditBoom}
			err := tc.run(tx)
			if !errors.Is(err, auditBoom) {
				t.Fatalf("审计失败必须让整个操作失败，实际 %v", err)
			}
			if tx.committed {
				t.Fatal("审计失败却提交了 —— 这正是 §6.3 第 1 条禁止的形状")
			}
			if !db.rolledBack || len(db.applied) != 0 {
				t.Fatalf("业务写入没有回滚：%v", db.applied)
			}
		})
	}
}

// ============================================================
// 小工具
// ============================================================

// 请求上下文里带 IP 时，审计主体必须拿到它（否则 audit.validateActor 会让整个操作失败）。
func TestNodeAdminActorCarriesRequestIP(t *testing.T) {
	s := adminNodeTestServer()
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/nodes/3", nil)
	r.RemoteAddr = "198.51.100.7:5555"
	r.Header.Set("User-Agent", "curl/8.6.0")

	ctx := context.WithValue(adminCtx(middleware.RoleOwner), ctxKeyBoundRequest{}, r)
	actor, admin, ok := s.nodeAdminActor(ctx)
	if !ok || admin == nil {
		t.Fatal("应当取到管理员身份")
	}
	if !actor.IP.IsValid() {
		t.Fatal("审计主体缺来源 IP —— audit.validateActor 会让整个操作失败（刻意不回退到 0.0.0.0）")
	}
	if actor.UserAgent != "curl/8.6.0" {
		t.Fatalf("UA 没带上：%q", actor.UserAgent)
	}
	// 🔴 email 取的是 admin_users 那一份（mw.AdminAuth.Email），不是 IAP 断言里那一份：
	//    审计要留的是「本系统认为他是谁」。
	if actor.Email != "ops@example.com" {
		t.Fatalf("审计主体的 email 不对：%q", actor.Email)
	}
}
