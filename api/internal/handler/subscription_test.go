package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/subgen"
)

// 订阅下发的单测。分成两组：
//
//  1. **判定组** —— api-contract §4.2 的完整判定。这一组的每一条都对应一个
//     具体的攻击面或产品承诺，改动时不要「顺手放宽」：
//     404 与 403 的区别是防枚举，200 + 伪节点与 404 的区别是通知通道。
//  2. **协商与响应头组** —— §4.3 的 UA 表与 §4.4 的 subscription-userinfo。
//
// 全部不碰数据库：subscriptionStore 是接口，塞假实现即可。

// ---- 假数据库 ----

type fakeSubStore struct {
	mu sync.Mutex

	resolveCalls int
	resolveRow   dbgen.ResolveSubscriptionTokenRow
	resolveErr   error

	usage    dbgen.GetSubscriptionUsageRow
	usageErr error

	servers    []dbgen.Server
	serversErr error

	logs   []dbgen.InsertSubscriptionFetchLogParams
	logErr error

	touched []int64
}

func (f *fakeSubStore) ResolveSubscriptionToken(_ context.Context, _ []byte) (dbgen.ResolveSubscriptionTokenRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolveCalls++
	return f.resolveRow, f.resolveErr
}

func (f *fakeSubStore) GetSubscriptionUsage(_ context.Context, _ int64) (dbgen.GetSubscriptionUsageRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usage, f.usageErr
}

func (f *fakeSubStore) ListVisibleServersForUser(_ context.Context, _ int64) ([]dbgen.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.servers, f.serversErr
}

func (f *fakeSubStore) InsertSubscriptionFetchLog(_ context.Context, arg dbgen.InsertSubscriptionFetchLogParams) (dbgen.InsertSubscriptionFetchLogRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, arg)
	return dbgen.InsertSubscriptionFetchLogRow{ID: 1}, f.logErr
}

func (f *fakeSubStore) TouchSubscriptionToken(_ context.Context, arg dbgen.TouchSubscriptionTokenParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, arg.ID)
	return nil
}

func (f *fakeSubStore) snapshot() (resolveCalls int, logs []dbgen.InsertSubscriptionFetchLogParams) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolveCalls, append([]dbgen.InsertSubscriptionFetchLogParams(nil), f.logs...)
}

// ---- 夹具 ----

const testToken = "abcdefghijklmnop1234" // 20 字符，落在 16–64 的合法区间

func testDeps(db subscriptionStore) subDeps {
	return subDeps{
		db:         db,
		pepper:     "test-pepper",
		trustProxy: false,
		webBaseURL: "https://web.babel.plus",
		// 丢弃日志：这些用例里日志只是噪音，唯一关心的是返回值。
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func testRequest(userAgent string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/s/"+testToken, nil)
	r.Header.Set("User-Agent", userAgent)
	r.RemoteAddr = "198.51.100.7:51234"
	return r
}

func activeToken() dbgen.ResolveSubscriptionTokenRow {
	return dbgen.ResolveSubscriptionTokenRow{
		TokenID:        42,
		UserID:         7,
		Uuid:           pgtype.UUID{Bytes: [16]byte{0x8f, 0x3a, 0x2c, 0x1d}, Valid: true},
		GroupID:        3,
		TransferEnable: 100 << 30, // 100 GiB
		ExpiredAt:      pgtype.Timestamptz{Time: time.Now().Add(30 * 24 * time.Hour), Valid: true},
	}
}

func realityServer() dbgen.Server {
	return dbgen.Server{
		ID: 1, Code: "bp-node-hk1", Name: "HK-1 · REALITY",
		Protocol: dbgen.ServerProtocolVlessReality,
		Host:     "203.0.113.10", Port: 443,
		// 🔴 键名与 node.go 的 nodeProtocolSettings 共用同一列，必须一致。
		ProtocolSettings: []byte(`{"server_name":"www.cloudflare.com","reality_public_key":"7Xk1pub","reality_short_id":"6ba85179e30d4fc2"}`),
	}
}

// TestProtocolSettingsKeysMatchNodeSide 锁住「同一列两个读者」的键名一致性。
//
// servers.protocol_settings 被两处读：node.go 的 nodeProtocolSettings（下发给节点）
// 与本文件的 subProtocolSettings（下发给客户端）。两边对同一个概念各起一个名字，
// 现象是运营填了一遍、另一边读到空值 —— 而两边的配置界面看起来都「没问题」。
func TestProtocolSettingsKeysMatchNodeSide(t *testing.T) {
	raw := []byte(`{
	  "server_name": "www.cloudflare.com",
	  "reality_private_key": "PRIVATE-MUST-NOT-LEAK",
	  "reality_short_id": "6ba85179e30d4fc2",
	  "reality_public_key": "pub",
	  "obfs": "salamander",
	  "obfs_password": "Jc7",
	  "cipher": "2022-blake3-aes-128-gcm",
	  "server_key": "SERVERKEY-MUST-NOT-LEAK"
	}`)

	var sub subProtocolSettings
	if err := json.Unmarshal(raw, &sub); err != nil {
		t.Fatalf("订阅侧解析失败: %v", err)
	}
	var node nodeProtocolSettings
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatalf("节点侧解析失败: %v", err)
	}

	if node.ServerName == nil || sub.ServerName != *node.ServerName {
		t.Errorf("server_name 两侧读到的值不同: %q vs %v", sub.ServerName, node.ServerName)
	}
	if node.RealityShortID == nil || sub.RealityShortID != *node.RealityShortID {
		t.Errorf("reality_short_id 两侧不一致: %q vs %v", sub.RealityShortID, node.RealityShortID)
	}
	if node.ObfsPassword == nil || sub.ObfsPassword != *node.ObfsPassword {
		t.Errorf("obfs_password 两侧不一致: %q vs %v", sub.ObfsPassword, node.ObfsPassword)
	}
	if node.Cipher == nil || sub.Cipher != *node.Cipher {
		t.Errorf("cipher 两侧不一致: %q vs %v", sub.Cipher, node.Cipher)
	}
}

// TestSubscriptionNeverCarriesServerCredentials 是一条结构性断言。
//
// 🔴 REALITY 私钥与 SS-2022 的 server_key 只该下发给节点。订阅是**公开可拉取**的
// 响应（只要有 token 就能拿），私钥一旦进去等于公开发布。
// subProtocolSettings 里根本没有这两个字段，所以它们连被渲染的机会都没有 ——
// 这条测试锁的就是「以后有人为了省事把整个 protocol_settings 透传进来」。
func TestSubscriptionNeverCarriesServerCredentials(t *testing.T) {
	srv := realityServer()
	srv.ProtocolSettings = []byte(`{
	  "server_name":"www.cloudflare.com",
	  "reality_public_key":"7Xk1pub",
	  "reality_short_id":"6ba85179e30d4fc2",
	  "reality_private_key":"PRIVATE-MUST-NOT-LEAK",
	  "server_key":"SERVERKEY-MUST-NOT-LEAK"
	}`)
	db := &fakeSubStore{resolveRow: activeToken(), servers: []dbgen.Server{srv}}

	for _, flag := range []string{"clash", "singbox", "base64"} {
		resp := deliverSubscription(context.Background(), testDeps(db), testRequest("curl/8"), testToken, flag)
		body := string(resp.body)
		if flag == "base64" {
			raw, err := base64.StdEncoding.DecodeString(body)
			if err != nil {
				t.Fatalf("base64 解码: %v", err)
			}
			body = string(raw)
		}
		for _, secret := range []string{"PRIVATE-MUST-NOT-LEAK", "SERVERKEY-MUST-NOT-LEAK"} {
			if strings.Contains(body, secret) {
				t.Errorf("flag=%s 的订阅正文里出现了服务端凭据 %q", flag, secret)
			}
		}
	}
}

// body 渲染响应并取回状态码、头与正文。
func serveAndRecord(t *testing.T, resp *subscriptionResponse) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := resp.VisitGetShortSubscriptionResponse(rec); err != nil {
		t.Fatalf("写响应失败: %v", err)
	}
	return rec
}

// ---- 判定组 ----

func TestInvalidTokenShapeReturns404WithoutQueryingDB(t *testing.T) {
	// api-contract §4.2 步 1：形态不合法 → 直接 404，**不查库**。
	// 不查库既省一次数据库往返，也让针对 token 表的探测拿不到时序差异。
	cases := map[string]string{
		"太短":     "short",
		"太长":     strings.Repeat("a", 65),
		"空":      "",
		"含斜杠":    "abcdefghij/klmnop123",
		"含点":     "abcdefghij.klmnop123",
		"含百分号编码": "abcdefghij%2Fklmnop1",
		"含中文":    "abcdefghijklmnop中文",
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			db := &fakeSubStore{}
			resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash"), tok, "")
			if resp.status != http.StatusNotFound {
				t.Errorf("状态码 = %d，期望 404", resp.status)
			}
			if calls, _ := db.snapshot(); calls != 0 {
				t.Errorf("查库 %d 次，形态不合法时必须为 0", calls)
			}
		})
	}
}

func TestUnknownOrRevokedTokenReturns404Not403(t *testing.T) {
	// 🔴 ADR 0006 §10.2：不存在 / 已吊销 / issued_at < sub_revoked_at 一律 404。
	// 403 会告诉攻击者「这个 token 存在但你不能用」—— 那正是枚举者要的信号。
	//
	// SQL 已经把这四种情形合并成「无行返回」，所以 Go 侧连区分的能力都没有。
	// 这条测试锁住的是：**未来有人加一个 403 分支时会红**。
	db := &fakeSubStore{resolveErr: pgx.ErrNoRows}
	resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash"), testToken, "")

	if resp.status != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404（不是 403）", resp.status)
	}
	rec := serveAndRecord(t, resp)
	// 404 不能带 subscription-userinfo：那会暴露「这个用户存在」。
	if got := lowerHeader(rec, "subscription-userinfo"); got != "" {
		t.Errorf("404 带了 subscription-userinfo=%q —— 泄漏用户存在性", got)
	}
	if body := rec.Body.String(); strings.Contains(body, "revoke") || strings.Contains(body, "吊销") {
		t.Errorf("404 正文泄漏了原因: %q", body)
	}
}

func TestDatabaseFailureAlsoReturns404(t *testing.T) {
	// 查库出错同样 404 而不是 500：对调用方而言两者的区别只有
	// 「这个 token 到底存不存在」，而那正是不能泄漏的东西。
	db := &fakeSubStore{resolveErr: context.DeadlineExceeded}
	resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash"), testToken, "")
	if resp.status != http.StatusNotFound {
		t.Errorf("状态码 = %d，期望 404", resp.status)
	}
}

// TestBannedUserGets200WithNoticeProxy 是本文件最重要的一条。
//
// 被封禁的用户看到「所有节点消失」会开工单；看到伪节点写着原因会去申诉。
// 这与「订阅 URL 本身就是通知通道」（user-journey §11.2）是同一件事 ——
// 它是邮箱收不到、Telegram 连不上、主站被封时**仍然能触达用户**的唯一通道。
func TestBannedUserGets200WithNoticeProxy(t *testing.T) {
	row := activeToken()
	row.Banned = true
	db := &fakeSubStore{resolveRow: row, servers: []dbgen.Server{realityServer()}}

	resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash-verge/2.0"), testToken, "")

	if resp.status != http.StatusOK {
		t.Fatalf("状态码 = %d —— banned 必须是 200，不是 404/403", resp.status)
	}
	body := string(resp.body)
	if !strings.Contains(body, "⚠️ 账号已停用 · 请提交工单 web.babel.plus") {
		t.Errorf("伪节点名不对:\n%s", body)
	}
	// 真节点一个都不能下发。
	if strings.Contains(body, "203.0.113.10") || strings.Contains(body, "type: vless") {
		t.Errorf("banned 用户拿到了真节点:\n%s", body)
	}
	// 伪节点必须是语法合法但连不上的条目 —— 真的下发空 proxies
	// 会让部分客户端拒绝导入整份配置，用户连这句话都看不到。
	if !strings.Contains(body, `server: "127.0.0.1"`) {
		t.Errorf("伪节点没有指向 127.0.0.1:\n%s", body)
	}

	// 审计仍然要写，node_count = 0（data-model §5 的列注释：0 = 伪节点响应）。
	_, logs := db.snapshot()
	if len(logs) != 1 {
		t.Fatalf("审计写了 %d 条，期望 1 条", len(logs))
	}
	if logs[0].NodeCount == nil || *logs[0].NodeCount != 0 {
		t.Errorf("node_count = %v，期望 0", logs[0].NodeCount)
	}
	if logs[0].StatusCode != 200 {
		t.Errorf("审计 status_code = %d，期望 200", logs[0].StatusCode)
	}
}

func TestExpiredAndQuotaExhaustedGet200WithNotice(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*dbgen.ResolveSubscriptionTokenRow, *dbgen.GetSubscriptionUsageRow)
		wantSub string
	}{
		{
			name: "到期",
			mutate: func(r *dbgen.ResolveSubscriptionTokenRow, _ *dbgen.GetSubscriptionUsageRow) {
				r.ExpiredAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
			},
			wantSub: "⚠️ 订阅已到期 · 续费 web.babel.plus",
		},
		{
			name: "配额耗尽",
			mutate: func(r *dbgen.ResolveSubscriptionTokenRow, u *dbgen.GetSubscriptionUsageRow) {
				r.TransferEnable = 1000
				u.U, u.D = 600, 400 // u + d >= transfer_enable，与节点面判定同形
			},
			wantSub: "⚠️ 流量已用尽 · 购买流量包 web.babel.plus",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := activeToken()
			var usage dbgen.GetSubscriptionUsageRow
			tc.mutate(&row, &usage)
			db := &fakeSubStore{resolveRow: row, usage: usage, servers: []dbgen.Server{realityServer()}}

			resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash"), testToken, "")
			if resp.status != http.StatusOK {
				t.Fatalf("状态码 = %d，期望 200", resp.status)
			}
			if !strings.Contains(string(resp.body), tc.wantSub) {
				t.Errorf("缺伪节点名 %q:\n%s", tc.wantSub, resp.body)
			}
		})
	}
}

func TestBannedTakesPrecedenceOverExpired(t *testing.T) {
	// 一个既被封禁又已到期的账号，用户需要看到的是「已停用，去申诉」
	// 而不是「已到期，去续费」—— 后者会让他付了钱之后发现还是不能用。
	row := activeToken()
	row.Banned = true
	row.ExpiredAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	db := &fakeSubStore{resolveRow: row}

	resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash"), testToken, "")
	if !strings.Contains(string(resp.body), "账号已停用") {
		t.Errorf("既封禁又到期时应显示停用:\n%s", resp.body)
	}
}

func TestActiveUserGetsRealNodesAndAudit(t *testing.T) {
	db := &fakeSubStore{
		resolveRow: activeToken(),
		usage:      dbgen.GetSubscriptionUsageRow{U: 1 << 20, D: 10 << 20},
		servers: []dbgen.Server{
			realityServer(),
			{
				ID: 2, Code: "bp-node-hk2", Name: "HK-2 · HY2 加速",
				Protocol: dbgen.ServerProtocolHysteria2,
				Host:     "203.0.113.11", Port: 443,
				ProtocolSettings: []byte(`{"server_name":"hk2.example.invalid","obfs_password":"Jc7"}`),
			},
		},
	}

	resp := deliverSubscription(context.Background(), testDeps(db), testRequest("mihomo/1.19"), testToken, "")
	if resp.status != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.status)
	}
	body := string(resp.body)
	for _, want := range []string{"HK-1 · REALITY", "HK-2 · HY2 加速", "type: vless", "type: hysteria2"} {
		if !strings.Contains(body, want) {
			t.Errorf("缺 %q:\n%s", want, body)
		}
	}
	// uuid 必须来自 users.uuid，不是订阅 token。
	if !strings.Contains(body, "8f3a2c1d-0000-0000-0000-000000000000") {
		t.Errorf("节点里没有用户 uuid:\n%s", body)
	}
	// 订阅 token 明文绝不能出现在正文里。
	if strings.Contains(body, testToken) {
		t.Errorf("订阅正文里出现了 token 明文:\n%s", body)
	}

	_, logs := db.snapshot()
	if len(logs) != 1 {
		t.Fatalf("审计条数 = %d", len(logs))
	}
	lg := logs[0]
	if lg.UserID != 7 || lg.TokenID == nil || *lg.TokenID != 42 {
		t.Errorf("审计的 user_id/token_id 不对: %+v", lg)
	}
	if lg.NodeCount == nil || *lg.NodeCount != 2 {
		t.Errorf("node_count = %v，期望 2", lg.NodeCount)
	}
	if lg.Format == nil || *lg.Format != string(subgen.FormatClash) {
		t.Errorf("format = %v，期望 clash", lg.Format)
	}
	if lg.ClientFlag == nil || *lg.ClientFlag != "mihomo" {
		t.Errorf("client_flag = %v，期望 mihomo", lg.ClientFlag)
	}
	// 🔴 来源 IP 是识别账号共享的唯一依据（system-design §5.2）。
	if lg.RequestIp.String() != "198.51.100.7" {
		t.Errorf("request_ip = %s，期望取自 RemoteAddr（trustProxy=false）", lg.RequestIp)
	}
	if lg.UserAgent != "mihomo/1.19" {
		t.Errorf("user_agent = %q", lg.UserAgent)
	}
}

func TestForgedXFFIgnoredWhenProxyNotTrusted(t *testing.T) {
	// 来源 IP 会写进 subscription_fetch_log 用于识别账号共享 ——
	// 在不可信环境下信任 XFF 等于让白嫖者自己决定日志里记什么。
	db := &fakeSubStore{resolveRow: activeToken()}
	r := testRequest("clash")
	r.Header.Set("X-Forwarded-For", "1.2.3.4")

	deliverSubscription(context.Background(), testDeps(db), r, testToken, "")

	_, logs := db.snapshot()
	if len(logs) != 1 || logs[0].RequestIp.String() != "198.51.100.7" {
		t.Errorf("trustProxy=false 时不该采信 XFF，实际记了 %v", logs)
	}

	d := testDeps(db)
	d.trustProxy = true
	db.logs = nil
	deliverSubscription(context.Background(), d, r, testToken, "")
	_, logs = db.snapshot()
	if len(logs) != 1 || logs[0].RequestIp.String() != "1.2.3.4" {
		t.Errorf("trustProxy=true 时应采信 XFF，实际记了 %v", logs)
	}
}

func TestNodeWithIncompleteSettingsIsSkipped(t *testing.T) {
	// 残缺条目在客户端里是「能导入、能显示、连不上」，用户会把它当成节点故障来报，
	// 而真正的原因在后台的一个空字段里 —— 排查成本最高的一类故障。
	// 跳过 + ERROR 日志，比下发一个坏节点好。
	bad := realityServer()
	bad.ProtocolSettings = []byte(`{"server_name":"www.cloudflare.com"}`) // 缺 reality_public_key
	db := &fakeSubStore{resolveRow: activeToken(), servers: []dbgen.Server{bad}}

	resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash"), testToken, "")
	if resp.status != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.status)
	}
	// 一个节点都没剩 → 必须退化成伪节点，不能下发空 proxies。
	if !strings.Contains(string(resp.body), "暂无可用节点") {
		t.Errorf("应退化为伪节点:\n%s", resp.body)
	}
	_, logs := db.snapshot()
	if len(logs) != 1 || logs[0].NodeCount == nil || *logs[0].NodeCount != 0 {
		t.Errorf("node_count 应为 0: %v", logs)
	}
}

func TestShadowsocks2022IsSkippedUntilPasswordFormVerified(t *testing.T) {
	// 🔴 一个填错的 SS-2022 节点会让 mihomo **整份配置**加载失败
	// （2022-* cipher 的 PSK 必须是定长 base64，而 uuid 里有 `-`），
	// 用户的 REALITY 与 Hysteria2 节点会跟着一起消失。
	// 在密码形态实测确认之前，跳过只损失兜底通路，猜错则损失全部通路。
	db := &fakeSubStore{
		resolveRow: activeToken(),
		servers: []dbgen.Server{
			realityServer(),
			{
				ID: 3, Code: "bp-node-ss1", Name: "TW-1 · SS 兜底",
				Protocol: dbgen.ServerProtocolShadowsocks2022,
				Host:     "203.0.113.30", Port: 8388,
				ProtocolSettings: []byte(`{"cipher":"2022-blake3-aes-128-gcm","server_key":"KEY"}`),
			},
		},
	}

	resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash"), testToken, "")
	body := string(resp.body)
	if !strings.Contains(body, "HK-1 · REALITY") {
		t.Errorf("REALITY 节点应照常下发:\n%s", body)
	}
	if strings.Contains(body, "TW-1 · SS 兜底") || strings.Contains(body, "type: ss\n") {
		t.Errorf("SS-2022 节点不该出现在订阅里:\n%s", body)
	}
	_, logs := db.snapshot()
	if len(logs) != 1 || logs[0].NodeCount == nil || *logs[0].NodeCount != 1 {
		t.Errorf("node_count 应为 1（只有 REALITY）: %v", logs)
	}
}

func TestUnsupportedProtocolsAreSkippedNotRendered(t *testing.T) {
	// vless_xhttp_cdn 的客户端字段名在本仓没有任何事实源。按 ADR 0004 它是
	// 「应急、默认关闭」的通路 —— 宁可不下发，也不猜一组字段名，
	// 猜错的现象是「应急通道在需要它的那天不能用」。
	db := &fakeSubStore{
		resolveRow: activeToken(),
		servers: []dbgen.Server{{
			ID: 4, Code: "bp-node-cdn", Name: "CDN 应急",
			Protocol: dbgen.ServerProtocolVlessXhttpCdn,
			Host:     "cdn.example.invalid", Port: 443,
		}},
	}
	resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash"), testToken, "")
	if strings.Contains(string(resp.body), "CDN 应急") {
		t.Errorf("vless_xhttp_cdn 不该被渲染:\n%s", resp.body)
	}
	if !strings.Contains(string(resp.body), "暂无可用节点") {
		t.Errorf("全部跳过后应退化为伪节点:\n%s", resp.body)
	}
}

// ---- 格式协商 ----

func TestNegotiateFormat(t *testing.T) {
	// api-contract §4.3 的表，匹配不区分大小写、按表内顺序取第一个命中。
	// ⚠️ 这张表本身在契约里标着「需实测」—— 本测试锁的是实现与契约一致，
	// 不是「这些 UA 就是各客户端的真实 UA」。
	cases := []struct {
		ua     string
		format subgen.Format
		client string
	}{
		{"clash-verge/2.0.3", subgen.FormatClash, "clash-verge"},
		{"ClashMetaForAndroid/2.11", subgen.FormatClash, "clash"},
		{"clash.meta/1.0", subgen.FormatClash, "clash-meta"},
		{"mihomo/1.19.0", subgen.FormatClash, "mihomo"},
		{"sing-box 1.13.0", subgen.FormatSingbox, "singbox"},
		{"SFI/1.12.0", subgen.FormatSingbox, "singbox"},
		{"SFA/1.12.0", subgen.FormatSingbox, "singbox"},
		{"SFM/1.12.0", subgen.FormatSingbox, "singbox"},
		{"SFT/1.12.0", subgen.FormatSingbox, "singbox"},
		{"Karing/1.0", subgen.FormatSingbox, "karing"},
		{"Hiddify/2.0", subgen.FormatSingbox, "hiddify"},
		{"v2rayNG/1.9.5", subgen.FormatBase64, "v2rayng"},
		{"v2rayN/6.45", subgen.FormatBase64, "v2rayn"},
		{"Shadowrocket/2.2", subgen.FormatBase64, "shadowrocket"},
		{"", subgen.FormatBase64, "unknown"},
		{"curl/8.4.0", subgen.FormatBase64, "unknown"},
	}
	for _, tc := range cases {
		gotF, gotC := negotiateFormat(tc.ua, "")
		if gotF != tc.format || gotC != tc.client {
			t.Errorf("negotiateFormat(%q) = (%s, %s)，期望 (%s, %s)", tc.ua, gotF, gotC, tc.format, tc.client)
		}
	}
}

func TestFlagOverridesUAButNotClientFlag(t *testing.T) {
	// `?flag=` 照抄 Xboard 语义，强制覆盖格式。
	// client_flag 仍然来自 UA —— 审计表要回答的是「谁在拉」，不是「他要了什么格式」。
	f, c := negotiateFormat("clash-verge/2.0", "singbox")
	if f != subgen.FormatSingbox {
		t.Errorf("flag 没有覆盖格式: %s", f)
	}
	if c != "clash-verge" {
		t.Errorf("client_flag = %q，应仍来自 UA", c)
	}
	// 非法 flag 值不生效（openapi 已限制枚举，这里是纵深防御）。
	if f, _ := negotiateFormat("clash-verge/2.0", "surge"); f != subgen.FormatClash {
		t.Errorf("非法 flag 不该改变格式，得到 %s", f)
	}
}

func TestFlagDeliversRequestedFormat(t *testing.T) {
	db := &fakeSubStore{resolveRow: activeToken(), servers: []dbgen.Server{realityServer()}}

	sb := deliverSubscription(context.Background(), testDeps(db), testRequest("curl/8"), testToken, "singbox")
	if !strings.HasPrefix(sb.contentType, "application/json") {
		t.Errorf("flag=singbox 的 Content-Type = %q", sb.contentType)
	}
	var cfg map[string]any
	if err := json.Unmarshal(sb.body, &cfg); err != nil {
		t.Fatalf("flag=singbox 输出不是合法 JSON: %v", err)
	}

	b64 := deliverSubscription(context.Background(), testDeps(db), testRequest("curl/8"), testToken, "")
	if !strings.HasPrefix(b64.contentType, "text/plain") {
		t.Errorf("兜底格式的 Content-Type = %q", b64.contentType)
	}
	raw, err := base64.StdEncoding.DecodeString(string(b64.body))
	if err != nil {
		t.Fatalf("兜底格式不是标准 base64: %v", err)
	}
	if !strings.HasPrefix(string(raw), "vless://") {
		t.Errorf("解码后不是分享链接: %s", raw)
	}
}

// ---- 响应头 ----

func TestSubscriptionUserInfoHeader(t *testing.T) {
	row := activeToken()
	row.TransferEnable = 107374182400
	expire := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	row.ExpiredAt = pgtype.Timestamptz{Time: expire, Valid: true}
	db := &fakeSubStore{
		resolveRow: row,
		usage:      dbgen.GetSubscriptionUsageRow{U: 1048576, D: 10485760},
		servers:    []dbgen.Server{realityServer()},
	}

	resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash"), testToken, "")
	rec := serveAndRecord(t, resp)

	// 照抄 Xboard app/Protocols/General.php：分隔符是「分号 + 一个空格」，
	// 值全部十进制整数、无引号无单位；expire 是 Unix 秒（§2.5 的例外之一）。
	want := "upload=1048576; download=10485760; total=107374182400; expire=" +
		strconv.FormatInt(expire.Unix(), 10)
	// 直接读 map 而不是 Header().Get()：Get 会把键规范化成
	// Subscription-Userinfo，那样这条断言就**测不出大小写**了 ——
	// 而契约要求的正是全小写（api-contract §4.4）。
	if got := lowerHeader(rec, "subscription-userinfo"); got != want {
		t.Errorf("subscription-userinfo = %q，期望 %q", got, want)
	}
}

func TestUnlimitedPlanExpireUsesFarFutureProposal(t *testing.T) {
	// 🔴 **这是提案不是裁决**（api-contract §4.4 / glossary 都标着未裁决）。
	// Xboard 输出空值，而部分客户端把空值当 0 处理并渲染成「1970-01-01 已过期」——
	// 一个付了不限时套餐的用户看到「已过期」是最糟糕的体验之一。
	// 本实现输出 4102444800（2100-01-01Z）。实测结论若相反，改 subNoExpiryUnix 一处即可。
	row := activeToken()
	row.ExpiredAt = pgtype.Timestamptz{} // NULL = 不限时
	db := &fakeSubStore{resolveRow: row, servers: []dbgen.Server{realityServer()}}

	resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash"), testToken, "")
	rec := serveAndRecord(t, resp)

	got := lowerHeader(rec, "subscription-userinfo")
	if !strings.HasSuffix(got, "expire=4102444800") {
		t.Errorf("不限时套餐的 expire 不对: %q", got)
	}
	if strings.HasSuffix(got, "expire=0") || strings.HasSuffix(got, "expire=") {
		t.Errorf("expire 为空/0 会被部分客户端渲染成 1970 已过期: %q", got)
	}
}

func TestResponseHeadersAndNoETag(t *testing.T) {
	db := &fakeSubStore{resolveRow: activeToken(), servers: []dbgen.Server{realityServer()}}
	resp := deliverSubscription(context.Background(), testDeps(db), testRequest("clash"), testToken, "")
	rec := serveAndRecord(t, resp)

	want := map[string]string{
		"content-type":            "text/yaml; charset=utf-8",
		"profile-update-interval": "24",
		"profile-web-page-url":    "https://web.babel.plus/subscribe",
		"content-disposition":     `attachment; filename*=UTF-8''babel.plus`,
		"cache-control":           "no-store",
	}
	for k, v := range want {
		got := rec.Header().Get(k)
		if k != "content-type" {
			// 🔴 契约要求头名全小写（api-contract §4.4：照抄 Xboard）。
			// Header().Get 会规范化键名，用它断言就测不出大小写 ——
			// 所以这几个非标准头直接读 map。
			got = lowerHeader(rec, k)
		}
		if got != v {
			t.Errorf("%s = %q，期望 %q", k, got, v)
		}
	}
	// 逐条确认键名确实是小写形态存在于 header map 里。
	for _, k := range []string{"subscription-userinfo", "profile-update-interval", "profile-web-page-url", "content-disposition", "cache-control"} {
		if _, ok := rec.Header()[k]; !ok {
			t.Errorf("头名 %q 不是全小写（实际 header map: %v）", k, rec.Header())
		}
	}
	// 🔴 **不设 ETag**（api-contract §4.4）：订阅内容内嵌当前用量数字，
	// 304 会让客户端继续显示旧的流量条，而流量条是用户判断「我还剩多少」的唯一入口。
	if got := rec.Header().Get("ETag"); got != "" {
		t.Errorf("订阅响应带了 ETag=%q —— 会让客户端卡在旧流量条上", got)
	}
}

// ---- 装配 ----

func TestServeSubscriptionFailsLoudlyWithoutRequestBinding(t *testing.T) {
	// 缺了绑定中间件意味着「格式全部退化成 base64 且审计 IP 全是 0.0.0.0」——
	// 一种能正常返回 200 的静默失效。所以必须是一个响亮的错误。
	s := &Server{}
	_, err := s.serveSubscription(context.Background(), testToken, "")
	if err != errNoBoundRequest {
		t.Errorf("err = %v，期望 errNoBoundRequest", err)
	}
}

func TestRequestBindingRoundTrip(t *testing.T) {
	r := testRequest("clash")
	ctx := context.WithValue(context.Background(), ctxKeyBoundRequest{}, r)
	got, ok := boundRequestFrom(ctx)
	if !ok || got != r {
		t.Errorf("boundRequestFrom 没有取回原请求")
	}
	if _, ok := boundRequestFrom(context.Background()); ok {
		t.Error("空 ctx 不该取到请求")
	}
}

// ---- token ----

func TestPlausibleSubscriptionToken(t *testing.T) {
	ok := []string{
		strings.Repeat("a", 16),
		strings.Repeat("a", 64),
		"AbC-123_xyzXYZ0987",
	}
	for _, s := range ok {
		if !plausibleSubscriptionToken(s) {
			t.Errorf("%q 应被接受", s)
		}
	}
	bad := []string{"", strings.Repeat("a", 15), strings.Repeat("a", 65), "abcdefghijklmno=", "abcdefghijklmno/"}
	for _, s := range bad {
		if plausibleSubscriptionToken(s) {
			t.Errorf("%q 应被拒绝", s)
		}
	}
}

func TestTokenHashDependsOnPepper(t *testing.T) {
	// pepper 在 Secret Manager 里、不落库：只拿到数据库的攻击者无法离线爆破。
	a := subscriptionTokenHash("pepper-a", testToken)
	b := subscriptionTokenHash("pepper-b", testToken)
	if string(a) == string(b) {
		t.Error("不同 pepper 产生了相同哈希")
	}
	if len(a) != 32 {
		t.Errorf("哈希长度 = %d，期望 32（sha256）", len(a))
	}
}

// lowerHeader 直接读 header map，不经过 Header().Get 的键名规范化。
//
// 存在的唯一理由：api-contract §4.4 要求 subscription-userinfo / profile-*
// 这几个非标准头**全小写**（照抄 Xboard）。Header().Get 会把键规范化成
// Subscription-Userinfo 再查，于是无论实现写的是大写还是小写它都能查到 ——
// 用它做断言等于这条契约根本没被测。
func lowerHeader(rec *httptest.ResponseRecorder, key string) string {
	v := rec.Header()[key]
	if len(v) == 0 {
		return ""
	}
	return v[0]
}
