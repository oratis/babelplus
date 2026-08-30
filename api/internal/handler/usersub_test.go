package handler

import (
	"context"
	"encoding/base64"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/config"
	"github.com/oratis/babelplus/api/internal/gen"
)

// 订阅面板 + 设备 / 用量 / 自检 的单测。
//
// Server.db 是具体类型 *store.Store（塞不进假实现），所以这里覆盖的是
// **形状层与判定层**：映射、三态推导、游标、日期轴、校验。
// 那也正是这一组最容易出错的地方 —— 节点面几个致命 bug 全在形状层（node.go 纪律 1）。
//
// 另有一条不测 Go 而测 **SQL 文本** 的守卫（TestDeviceIDExpressionIsShared），
// 理由见那个函数的注释。

// ============================================================
// 游标
// ============================================================

func TestPageCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 30, 12, 34, 56, 123456789, time.UTC)
	raw := encodePageCursor(4242, at)
	if raw == "" {
		t.Fatal("encodePageCursor 返回空串")
	}
	got, ok := decodePageCursor(raw)
	if !ok {
		t.Fatalf("decodePageCursor(%q) 失败", raw)
	}
	if got.ID != 4242 {
		t.Errorf("id = %d, want 4242", got.ID)
	}
	// 纳秒必须保住：游标里的 at 要参与 (created_at, id) 的行比较，
	// 截到秒会让同一秒内的多行被整批跳过或整批重复。
	if !got.At.Equal(at) {
		t.Errorf("at = %v, want %v", got.At, at)
	}
}

// 🔴 这是本组最重要的一条边界：**类型不对的游标必须被判为不可用**。
//
// 契约要求「服务端必须校验解出的字段类型」。不校验的话 `{"id":"abc"}` 会解成 id = 0，
// 而 `WHERE id < 0` 返回空列表 —— 用户看到的是「你一条记录都没有」，
// 而不是「翻页坏了」。前者会让他以为记录被删了。
func TestDecodePageCursorRejectsBadShapes(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	cases := []struct {
		name string
		raw  string
	}{
		{"空串", ""},
		{"不是 base64", "!!!!not base64!!!!"},
		{"不是 json", enc("hello")},
		{"id 是字符串", enc(`{"id":"abc","at":"2026-08-30T00:00:00Z"}`)},
		{"id 是浮点串", enc(`{"id":"12.5","at":"2026-08-30T00:00:00Z"}`)},
		{"at 是数字", enc(`{"id":1,"at":12345}`)},
		{"at 不是 RFC3339", enc(`{"id":1,"at":"2026/08/30"}`)},
		{"缺 id", enc(`{"at":"2026-08-30T00:00:00Z"}`)},
		{"缺 at", enc(`{"id":1}`)},
		{"多了未知字段", enc(`{"id":1,"at":"2026-08-30T00:00:00Z","evil":true}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, ok := decodePageCursor(c.raw); ok {
				t.Fatalf("坏游标被接受了：%+v", got)
			}
		})
	}
}

// 复用 catalog.go 的 decodeKeysetCursor 之后，只接受**无填充**的 base64url。
// 这条断言把那个约定钉住：我们自己发出去的就是无填充形态
// （encodeKeysetCursor 用 RawURLEncoding），两端一致比「两端各自宽容」更好查。
func TestPageCursorIsUnpaddedBase64URL(t *testing.T) {
	raw := encodePageCursor(7, time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC))
	if strings.ContainsAny(raw, "=+/") {
		t.Fatalf("游标 %q 含需要百分号转义的字符 —— 它会出现在查询串里", raw)
	}
	if _, ok := decodePageCursor(raw); !ok {
		t.Fatalf("自己发出去的游标解不回来：%q", raw)
	}
}

// listPageLimit 只在 pageLimit 之上加一个「端点自带默认值」，
// 但上限与「多取一行」的判据必须原样继承 —— 否则 `?limit=100000`
// 就是一条「用一个 GET 让数据库扫十万行」的免费放大器。
func TestListPageLimit(t *testing.T) {
	cases := []struct {
		name     string
		in       *int32
		def      int32
		wantWant int
		wantPage int32
	}{
		{"缺省取端点自述值", nil, fetchLogLimitDefault, 10, 11},
		{"缺省取共享默认值", nil, defaultPageLimit, 20, 21},
		{"正常", ptrOf(int32(5)), defaultPageLimit, 5, 6},
		{"零被夹到 1", ptrOf(int32(0)), defaultPageLimit, 1, 2},
		{"负数被夹到 1", ptrOf(int32(-3)), defaultPageLimit, 1, 2},
		{"超上限被夹住", ptrOf(int32(100000)), defaultPageLimit, maxPageLimit, maxPageLimit + 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want, page := listPageLimit(c.in, c.def)
			if want != c.wantWant || page != c.wantPage {
				t.Errorf("listPageLimit = (%d,%d), want (%d,%d)", want, page, c.wantWant, c.wantPage)
			}
			// has_more 的判据是「多取一行」，两者必须差 1。
			if int(page) != want+1 {
				t.Errorf("page_limit(%d) 不等于 want(%d)+1", page, want)
			}
		})
	}
}

// ⚠️ 契约自身冲突：listSubscriptionFetchLog 的 description 写「默认 10 条」，
// 共享参数 LimitQuery 写 default 20。页面取 10。
func TestFetchLogDefaultIsTen(t *testing.T) {
	if fetchLogLimitDefault != 10 {
		t.Fatalf("fetchLogLimitDefault = %d, want 10（端点自述优先于共享参数）", fetchLogLimitDefault)
	}
}

// ============================================================
// 订阅 token
// ============================================================

// 🔴 **一键全撤之后，列表里那些 token 必须看起来是失效的。**
//
// 这是 subscription_user.sql §1 全篇的理由：一键全撤只写 users.sub_revoked_at，
// **不动 subscription_tokens 的行**，所以 t.revoked_at 仍然是 NULL。
// 只回填 t.revoked_at 的实现会让用户点完「全部重置」之后，
// 列表里每一条仍然显示有效，而 /s/{token} 对它们全部 404 ——
// 没有报错、没有日志，用户只会得出「重置没生效」。
func TestSubscriptionTokenViewSurfacesRevokeAll(t *testing.T) {
	revokedAt := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	v := subscriptionTokenView(dbgen.ListUserSubscriptionTokensRow{
		ID:           1,
		Name:         "iPhone",
		TokenPrefix:  "a1b2c3d4",
		IssuedAt:     ts(revokedAt.Add(-time.Hour)),
		SubRevokedAt: ts(revokedAt),
		IsActive:     false, // 一键全撤的结果：t.revoked_at 仍是 NULL
	})
	if v.RevokedAt == nil {
		t.Fatal("被一键全撤的 token 在响应里仍然没有 revoked_at —— 前端会把它渲染成有效")
	}
	if !v.RevokedAt.Equal(revokedAt) {
		t.Errorf("revoked_at = %v, want %v（应回退到 sub_revoked_at）", *v.RevokedAt, revokedAt)
	}
}

// 自身过期的 token 同理：expires_at 到了但 revoked_at 是 NULL。
func TestSubscriptionTokenViewSurfacesExpiry(t *testing.T) {
	exp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v := subscriptionTokenView(dbgen.ListUserSubscriptionTokensRow{
		ID: 2, ExpiresAt: ts(exp), IsActive: false,
	})
	if v.RevokedAt == nil || !v.RevokedAt.Equal(exp) {
		t.Fatalf("过期 token 的 revoked_at = %v，want %v", v.RevokedAt, exp)
	}
}

// 有效的 token 不该被凭空标成已吊销。
func TestSubscriptionTokenViewKeepsActiveClean(t *testing.T) {
	v := subscriptionTokenView(dbgen.ListUserSubscriptionTokensRow{
		ID: 3, TokenPrefix: "zzzz1111", IsActive: true,
		SubRevokedAt: ts(time.Now().Add(-time.Hour)), // 撤过一次，但本 token 是之后签发的
	})
	if v.RevokedAt != nil {
		t.Fatalf("有效 token 被标成已吊销：%v", *v.RevokedAt)
	}
}

func TestMaskSubToken(t *testing.T) {
	if got := maskSubToken("a1b2c3d4"); got != "a1b2c3d4…" {
		t.Errorf("mask = %q", got)
	}
	// 前缀比约定长时截断，不要把整条 token 泄出去。
	if got := maskSubToken("0123456789abcdef"); got != "01234567…" {
		t.Errorf("mask(超长前缀) = %q", got)
	}
	if got := maskSubToken(""); got != "…" {
		t.Errorf("mask(空) = %q", got)
	}
}

// 订阅 token 的密文必须能被同一把派生密钥还原 —— 这是 token_enc 这一列
// 存在的唯一理由（ADR 0002 的失联恢复要用户自己拼 https://{新域名}/s/{token}）。
func TestSubTokenEncryptRoundTrip(t *testing.T) {
	const pepper = "unit-test-pepper"
	const plain = "abcdefghijklmnopqrstuvwxyz012345"

	enc, err := encryptSubToken(pepper, plain)
	if err != nil {
		t.Fatalf("encryptSubToken: %v", err)
	}
	if strings.Contains(string(enc), plain) {
		t.Fatal("密文里出现了明文 token")
	}
	got, err := decryptSubToken(pepper, enc)
	if err != nil {
		t.Fatalf("decryptSubToken: %v", err)
	}
	if got != plain {
		t.Fatalf("还原出 %q，want %q", got, plain)
	}
}

func TestSubTokenDecryptRejectsWrongPepper(t *testing.T) {
	enc, err := encryptSubToken("pepper-A", "token-value-token-value-1234")
	if err != nil {
		t.Fatalf("encryptSubToken: %v", err)
	}
	if _, err := decryptSubToken("pepper-B", enc); err == nil {
		t.Fatal("用另一把 pepper 竟然解开了密文")
	}
}

// pepper 为空时派生出来的密钥是一个常量 —— 等于没加密。必须拒绝签发。
func TestSubTokenRefusesEmptyPepper(t *testing.T) {
	if _, err := encryptSubToken("", "whatever"); err == nil {
		t.Fatal("空 pepper 竟然通过了")
	}
	if _, err := decryptSubToken("", []byte("whatever")); err == nil {
		t.Fatal("空 pepper 竟然通过了")
	}
}

func TestFetchLogEntryView(t *testing.T) {
	at := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	ip := netip.MustParseAddr("203.0.113.9")

	t.Run("认得的格式下发", func(t *testing.T) {
		e := fetchLogEntryView(dbgen.ListUserSubscriptionFetchLogRow{
			ID: 1, RequestAt: ts(at), RequestIp: ip,
			UserAgent: "clash-verge/1.0", Format: ptrOf("clash"),
			TokenID: ptrOf(int64(9)), TokenName: ptrOf("公司电脑"),
		})
		if e.Format == nil || *e.Format != gen.SubscriptionFetchLogEntryFormatClash {
			t.Fatalf("format = %v", e.Format)
		}
		if e.SubTokenName == nil || *e.SubTokenName != "公司电脑" {
			t.Fatalf("sub_token_name = %v —— 没有它用户只能「全部重置」", e.SubTokenName)
		}
		// IP 必须是裸地址，不能带掩码。
		if e.RequestIp != "203.0.113.9" {
			t.Errorf("request_ip = %q", e.RequestIp)
		}
	})

	t.Run("认不出的格式不下发", func(t *testing.T) {
		e := fetchLogEntryView(dbgen.ListUserSubscriptionFetchLogRow{
			ID: 2, RequestAt: ts(at), RequestIp: ip, Format: ptrOf("surge"),
		})
		if e.Format != nil {
			t.Fatalf("format = %v，认不出的取值应当留空而不是原样透出（前端按枚举 switch）", *e.Format)
		}
	})

	t.Run("404 那类拉取没有 token_id", func(t *testing.T) {
		e := fetchLogEntryView(dbgen.ListUserSubscriptionFetchLogRow{
			ID: 3, RequestAt: ts(at), RequestIp: ip,
		})
		if e.SubTokenId != nil || e.SubTokenName != nil {
			t.Fatal("没有 token 的拉取记录不该凭空长出 token 信息")
		}
	})
}

// ============================================================
// 节点列表
// ============================================================

// 🔴 **应急通路必须在这个接口上不可观测。**
//
// vless_xhttp_cdn 是 ADR 0004 的应急通路。它出现在一个任何登录用户都能拉的列表里，
// 等于对外宣告「我们正在被封」。两条 vless 变体必须折叠成同一个展示名。
func TestNodeDisplayTypeHidesEmergencyPath(t *testing.T) {
	reality := nodeDisplayType(dbgen.ServerProtocolVlessReality)
	xhttp := nodeDisplayType(dbgen.ServerProtocolVlessXhttpCdn)
	if reality != xhttp {
		t.Fatalf("REALITY 与应急 XHTTP 的展示名不同（%q vs %q）—— 切到应急通路会被外部观测到", reality, xhttp)
	}
	if strings.Contains(xhttp, "xhttp") || strings.Contains(xhttp, "cdn") || strings.Contains(xhttp, "reality") {
		t.Fatalf("展示名 %q 泄漏了内部协议细节", xhttp)
	}
	if got := nodeDisplayType(dbgen.ServerProtocolHysteria2); got != "hysteria" {
		t.Errorf("hysteria2 → %q", got)
	}
	if got := nodeDisplayType(dbgen.ServerProtocolShadowsocks2022); got != "shadowsocks" {
		t.Errorf("shadowsocks2022 → %q", got)
	}
	if got := nodeDisplayType(dbgen.ServerProtocol("brand_new_protocol")); got != "unknown" {
		t.Errorf("未知协议 → %q，应当是 unknown（猜一个会让客户端按错误协议提示用户）", got)
	}
}

func TestNodeStatusOf(t *testing.T) {
	now := ts(time.Now())
	cases := []struct {
		name    string
		enabled bool
		rep     pgtype.Timestamptz
		secs    int64
		want    gen.UserNodeStatus
	}{
		// 🔴 从未上报（server_online_state 是 UNLOGGED 表，崩溃后自动 TRUNCATE）
		// 必须是 offline。把「没有数据」渲染成 online，数据库重启一次
		// 整张节点列表就会集体撒谎。
		{"从未上报", true, pgtype.Timestamptz{}, 0, gen.Offline},
		{"已停用但刚上报", false, now, 1, gen.Offline},
		{"1 分钟前", true, now, 60, gen.Online},
		{"恰好 online 边界", true, now, int64(nodeOnlineWithin / time.Second), gen.Online},
		{"越过 online 边界 1 秒", true, now, int64(nodeOnlineWithin/time.Second) + 1, gen.Degraded},
		{"恰好 degraded 边界", true, now, int64(nodeDegradedWithin / time.Second), gen.Degraded},
		{"越过 degraded 边界 1 秒", true, now, int64(nodeDegradedWithin/time.Second) + 1, gen.Offline},
		// 时钟回拨会让 seconds_since_report 变成负数：那说明节点的上报比我们的 now 还新，
		// 判成 online 是唯一合理的选择。
		{"负的时间差", true, now, -5, gen.Online},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nodeStatusOf(c.enabled, c.rep, c.secs); got != c.want {
				t.Errorf("nodeStatusOf = %q, want %q", got, c.want)
			}
		})
	}
}

func TestUserNodeViewFillsMultiplier(t *testing.T) {
	v := userNodeView(dbgen.ListUserNodesWithStateRow{
		ID: 7, Name: "香港 01", Region: "hk",
		Protocol: dbgen.ServerProtocolVlessReality,
		Enabled:  true, ReportedAt: ts(time.Now()), SecondsSinceReport: 10,
	})
	// 第一阶段不引入倍率，但字段必须存在且是 1.0（servers 表刻意没有 rate 列）。
	if v.MultiplierE9 == nil || *v.MultiplierE9 != 1_000_000_000 {
		t.Fatalf("multiplier_e9 = %v, want 1e9", v.MultiplierE9)
	}
	if v.Region == nil || *v.Region != "hk" {
		t.Fatalf("region = %v", v.Region)
	}
}

// ============================================================
// 设备：列表与踢下线必须用同一个表达式
// ============================================================

// 🔴 **本测试不测 Go，测的是 devices.sql 的文本。**
//
// UserDevice.id 在数据库里不存在，是 `md5(device_ip::text)` 前 **52 bit** + 1 合成的。
//
// 🔴 52 不是随手取的：契约里 UserDevice.id 是 integer，前端拿到的是 JavaScript number，
//
//	而 JS 的安全整数上限是 2^53−1。从前这里取 60 bit，合成出来的 id 有 2^60 量级 ——
//	`JSON.parse` 会把它舍成一个**邻近但不相等**的数，前端拿这个数去 DELETE，
//	`WHERE 合成id = $1` 必然不匹配，于是「踢下线」稳定 404。
//	2^52 < 2^53−1，落在安全区内，往返不丢精度。
//
// devices.sql §1 写明：列表与删除两条语句必须用**同一个表达式**，
// 「改一处不改另一处，所有旧 id 会在一次发布后集体失效」——
// 而那个现象是「点了踢下线没反应」，且只对某些 IP 出现。
//
// 这条不变量跨两条 SQL 语句，Go 侧一行代码都碰不到它（handler 只做 id 透传），
// 所以唯一能守住它的测试就是直接读那个文件。
// ⚠️ 归一化掉表别名（a. / d.）之后再比：两条语句的别名本来就不同，那不是漂移。
func TestDeviceIDExpressionIsShared(t *testing.T) {
	const path = "../../db/queries/devices.sql"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}

	// 去掉全部空白与表别名，让两条语句可以逐字比较。
	norm := regexp.MustCompile(`\s+`).ReplaceAllString(string(raw), "")
	norm = regexp.MustCompile(`\b[a-z]\.device_ip`).ReplaceAllString(norm, "device_ip")

	canonical := `(('x'||substr(md5(device_ip::text),1,13))::bit(52)::bigint+1)`
	if n := strings.Count(norm, canonical); n != 2 {
		t.Fatalf("devices.sql 里合成 id 的表达式出现 %d 次，期望恰好 2 次"+
			"（ListUserDevicesByIP 一次、KickUserDeviceByID 一次）。\n"+
			"出现 1 次 = 两条语句已经漂移，所有旧设备 id 会在下次发布后集体失效，"+
			"现象是「点了踢下线没反应」。\n期望的规范形式：%s", n, canonical)
	}

	// 再确认它确实分布在那两条语句里，而不是同一条里写了两遍。
	for _, name := range []string{"ListUserDevicesByIP", "KickUserDeviceByID"} {
		sec := sqlSection(t, string(raw), name)
		secNorm := regexp.MustCompile(`\s+`).ReplaceAllString(sec, "")
		secNorm = regexp.MustCompile(`\b[a-z]\.device_ip`).ReplaceAllString(secNorm, "device_ip")
		if !strings.Contains(secNorm, canonical) {
			t.Errorf("查询 %s 里没有那个合成 id 表达式", name)
		}
	}
}

// sqlSection 取出 `-- name: <name>` 到下一个 `-- name:` 之间的正文。
func sqlSection(t *testing.T, src, name string) string {
	t.Helper()
	marker := "-- name: " + name + " "
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("在 devices.sql 里找不到查询 %s", name)
	}
	rest := src[i+len(marker):]
	if j := strings.Index(rest, "-- name: "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// handler 侧必须**原样透传** id，一个字都不算。
// 在 Go 里重算就是第三份拷贝，而三份里改漏任何一份现象都一样。
func TestUserDeviceViewPassesThroughSyntheticID(t *testing.T) {
	const synthetic int64 = 1152921504606846977 // 随便一个 60bit+1 量级的值
	seen := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	v := userDeviceView(dbgen.ListUserDevicesByIPRow{
		ID:             synthetic,
		DeviceIp:       netip.MustParseAddr("198.51.100.7"),
		LastSeenAt:     seen,
		NodeCount:      3,
		LastServerID:   5,
		LastServerName: ptrOf("东京 02"),
	})
	if v.Id != synthetic {
		t.Fatalf("id = %d, want %d（列表与踢下线必须用同一个 id）", v.Id, synthetic)
	}
	if !v.LastSeenAt.Equal(seen) {
		t.Errorf("last_seen_at = %v, want %v", v.LastSeenAt, seen)
	}
	// first_seen_at 无法提供（表里只有 last_seen_at，每次 push 都覆盖）。
	// 绝不能用 last_seen_at 冒充 —— 那会让「第一次出现」变成每分钟都在变的数字。
	if v.FirstSeenAt != nil {
		t.Errorf("first_seen_at 被填了 %v，而数据库里没有这个事实", *v.FirstSeenAt)
	}
	if v.Ip != "198.51.100.7" {
		t.Errorf("ip = %q", v.Ip)
	}
}

// 🔴 踢下线的响应**必须**带生效延迟。配置下发是 60 秒轮询，删行不会立刻断开连接；
// user-journey §12.2 的原话是不告知的话「用户会连点五次然后开工单」。
func TestKickResultCarriesEffectiveDelay(t *testing.T) {
	r := kickResult(2)
	if r.Removed != 2 {
		t.Errorf("removed = %d", r.Removed)
	}
	if r.EffectiveWithinSeconds != 60 {
		t.Fatalf("effective_within_seconds = %d, want 60（节点轮询周期）", r.EffectiveWithinSeconds)
	}
	// 0 台也要带上：「全部下线」在没人在线时也是 200。
	if kickResult(0).EffectiveWithinSeconds != 60 {
		t.Fatal("removed = 0 时丢了生效延迟提示")
	}
}

// ============================================================
// 用量曲线
// ============================================================

func TestBuildUsageSeriesFillsAxisAndTotals(t *testing.T) {
	// 上海时间 2026-08-30 10:00 → UTC 2026-08-30 02:00
	now := time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC)
	rows := []dbgen.ListUserUsageSeriesRow{
		{StatDate: pgDate(2026, 8, 28), UploadBytes: 100, DownloadBytes: 200},
		{StatDate: pgDate(2026, 8, 30), UploadBytes: 1, DownloadBytes: 2},
	}
	s := buildUsageSeries(gen.UsageSeriesRangeN7d, 7, rows, now)

	if len(s.Points) != 7 {
		t.Fatalf("points = %d, want 7（没有流量的那天在库里没有行，补零是 handler 的活）", len(s.Points))
	}
	if got := s.Points[0].Date.Format("2006-01-02"); got != "2026-08-24" {
		t.Errorf("轴左端 = %s, want 2026-08-24", got)
	}
	if got := s.Points[6].Date.Format("2006-01-02"); got != "2026-08-30" {
		t.Errorf("轴右端 = %s, want 2026-08-30", got)
	}
	// 🔴 总计必须等于 points 的和 —— 另查一条 sum 会让柱状图加起来不等于旁边的总计。
	var up, down int64
	for _, p := range s.Points {
		up += p.UploadBytes
		down += p.DownloadBytes
	}
	if s.TotalUploadBytes != up || s.TotalDownloadBytes != down {
		t.Fatalf("总计 (%d,%d) 与 points 之和 (%d,%d) 不等", s.TotalUploadBytes, s.TotalDownloadBytes, up, down)
	}
	if s.TotalUploadBytes != 101 || s.TotalDownloadBytes != 202 {
		t.Errorf("总计 = (%d,%d), want (101,202)", s.TotalUploadBytes, s.TotalDownloadBytes)
	}
	// 单位是整数字节，不做任何换算（KB 不是 KiB，而且换算是展示层的事）。
	if s.Points[6].UploadBytes != 1 || s.Points[6].DownloadBytes != 2 {
		t.Errorf("最后一天 = (%d,%d)，SQL 给什么就发什么", s.Points[6].UploadBytes, s.Points[6].DownloadBytes)
	}
}

// 🔴 这是本组的静默边界：**日期轴必须按 Asia/Shanghai 切天**（stat_date 的口径，0009）。
//
// 用进程时区（Cloud Run 容器是 UTC）算的话，北京时间 00:00–08:00 这 8 小时里
// 算出来的「今天」比数据库少一天：用户早上看不到昨天的柱子，中午它又自己出现。
// 没有报错、不可复现，而且只有中国时区的用户会遇到。
func TestBuildUsageSeriesUsesShanghaiDay(t *testing.T) {
	// UTC 2026-08-30 23:30 == 上海 2026-08-31 07:30
	now := time.Date(2026, 8, 30, 23, 30, 0, 0, time.UTC)
	s := buildUsageSeries(gen.UsageSeriesRangeN7d, 7, nil, now)
	last := s.Points[len(s.Points)-1].Date.Format("2006-01-02")
	if last != "2026-08-31" {
		t.Fatalf("轴右端 = %s, want 2026-08-31（UTC 已是 30 日深夜，上海已经是 31 日）", last)
	}
	// 同一时刻按 UTC 算会得到 08-30 —— 确认我们没有退化成那个。
	if last == now.UTC().Format("2006-01-02") {
		t.Fatal("日期轴退化成了 UTC 口径")
	}
}

// 时区必须是固定 +08:00 而不是 LoadLocation：运行镜像是 FROM scratch，里面没有 tzdata。
func TestShanghaiOffsetIsFixedEightHours(t *testing.T) {
	_, off := time.Now().In(shanghaiOffset).Zone()
	if off != 8*60*60 {
		t.Fatalf("shanghaiOffset 偏移 = %d 秒, want 28800", off)
	}
	if _, err := time.LoadLocation("Asia/Shanghai"); err != nil {
		// 这台机器上就没有 tzdata —— 正好证明为什么不能用 LoadLocation。
		t.Logf("本机 LoadLocation 失败（%v），与 scratch 镜像同形", err)
	}
}

func TestUsageRangeDays(t *testing.T) {
	cases := []struct {
		in    *gen.GetUserUsageParamsRange
		days  int32
		label gen.UsageSeriesRange
	}{
		{nil, 30, gen.UsageSeriesRangeN30d},
		{ptrOf(gen.GetUserUsageParamsRangeN7d), 7, gen.UsageSeriesRangeN7d},
		{ptrOf(gen.GetUserUsageParamsRangeN30d), 30, gen.UsageSeriesRangeN30d},
		{ptrOf(gen.GetUserUsageParamsRangeN90d), 90, gen.UsageSeriesRangeN90d},
		// 本端点在契约上没有 4xx 出口，认不出的取值只能落到默认值。
		{ptrOf(gen.GetUserUsageParamsRange("1y")), 30, gen.UsageSeriesRangeN30d},
	}
	for _, c := range cases {
		days, label := usageRangeDays(c.in)
		if days != c.days || label != c.label {
			t.Errorf("usageRangeDays(%v) = (%d,%q), want (%d,%q)", c.in, days, label, c.days, c.label)
		}
	}
}

func pgDate(y int, m time.Month, d int) pgtype.Date {
	return pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
}

// ============================================================
// 账号自检
// ============================================================

// 🔴 **traffic_left 的表达式必须与节点侧逐字相同**：`u + d < transfer_enable`。
//
// 写成 `<=` 或把等号挪到另一边，就会出现「面板说还有流量，节点不给连」——
// 而用户看不到节点侧的判定，只能开工单。
func TestDiagnoseTrafficLeftMatchesNodeExpression(t *testing.T) {
	cases := []struct {
		name           string
		u, d, transfer int64
		wantOK         bool
	}{
		{"还剩 1 字节", 50, 49, 100, true},
		{"恰好用完", 50, 50, 100, false},
		{"超了", 60, 50, 100, false},
		{"一点没用", 0, 0, 100, true},
		{"配额为 0", 0, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := buildDiagnose(dbgen.GetUserDiagnoseFactsRow{
				U: c.u, D: c.d, TransferEnable: c.transfer,
			}, time.Now())
			got := diagnoseCheck(t, r, gen.TrafficLeft)
			if got.Ok != c.wantOK {
				t.Fatalf("traffic_left = %v, want %v（节点侧判的是 u+d < transfer_enable）", got.Ok, c.wantOK)
			}
		})
	}
}

// device_limit 为 NULL = 不限设备。当 0 的话所有不限设备的用户都会看到红色的「设备超限」。
func TestDiagnoseDeviceLimitNullMeansUnlimited(t *testing.T) {
	r := buildDiagnose(dbgen.GetUserDiagnoseFactsRow{
		DeviceCount: 9, DeviceLimit: nil, TransferEnable: 1,
	}, time.Now())
	c := diagnoseCheck(t, r, gen.DeviceUnderLimit)
	if !c.Ok {
		t.Fatal("device_limit 为 NULL（不限设备）被判成超限")
	}
	if c.Detail == nil || (*c.Detail)["unlimited"] != true {
		t.Errorf("detail 里没有标出「不限」：%v", c.Detail)
	}
}

func TestDiagnoseDeviceLimitBoundary(t *testing.T) {
	mk := func(count int32, limit int32) gen.DiagnoseCheck {
		r := buildDiagnose(dbgen.GetUserDiagnoseFactsRow{
			DeviceCount: count, DeviceLimit: &limit, TransferEnable: 1,
		}, time.Now())
		return diagnoseCheck(t, r, gen.DeviceUnderLimit)
	}
	if !mk(2, 2).Ok {
		t.Error("恰好等于上限被判成超限")
	}
	if mk(3, 2).Ok {
		t.Error("超过上限没被判出来")
	}
	// 设备数是软限制且系统性偏小，detail 必须把口径说清楚，
	// 否则用户会拿它跟手上的设备台数对质。
	c := mk(2, 2)
	if c.Detail == nil || (*c.Detail)["counted_by"] != "ip" || (*c.Detail)["approximate"] != true {
		t.Errorf("detail 没有标明口径与近似性：%v", c.Detail)
	}
}

// expired_at IS NULL 是**不限时套餐**，不是「已过期」。
func TestDiagnoseNullExpiryIsUnlimited(t *testing.T) {
	r := buildDiagnose(dbgen.GetUserDiagnoseFactsRow{TransferEnable: 1}, time.Now())
	if !diagnoseCheck(t, r, gen.NotExpired).Ok {
		t.Fatal("expired_at 为 NULL 的不限时套餐被判成已过期")
	}
}

func TestDiagnoseExpiryBoundary(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	past := buildDiagnose(dbgen.GetUserDiagnoseFactsRow{ExpiredAt: ts(now.Add(-time.Second)), TransferEnable: 1}, now)
	if diagnoseCheck(t, past, gen.NotExpired).Ok {
		t.Error("已过期被判成未过期")
	}
	future := buildDiagnose(dbgen.GetUserDiagnoseFactsRow{ExpiredAt: ts(now.Add(time.Second)), TransferEnable: 1}, now)
	if !diagnoseCheck(t, future, gen.NotExpired).Ok {
		t.Error("未过期被判成已过期")
	}
}

// 「他压根没拉」与「他一直在拉、一直被拒」必须能分开 —— 两者的用户动作完全不同。
// 契约的 DiagnoseResult 只有 subscription_last_fetched_at 一个字段，
// 所以 last_ok 落在 account_active 的 detail 里。
func TestDiagnoseSeparatesFetchedAndOkFetched(t *testing.T) {
	last := time.Date(2026, 8, 30, 11, 0, 0, 0, time.UTC)
	lastOK := time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC)
	r := buildDiagnose(dbgen.GetUserDiagnoseFactsRow{
		TransferEnable:              1,
		SubscriptionLastFetchedAt:   last,
		SubscriptionLastOkFetchedAt: lastOK,
	}, time.Now())

	if r.SubscriptionLastFetchedAt == nil || !r.SubscriptionLastFetchedAt.Equal(last) {
		t.Fatalf("subscription_last_fetched_at = %v", r.SubscriptionLastFetchedAt)
	}
	c := diagnoseCheck(t, r, gen.AccountActive)
	if c.Detail == nil {
		t.Fatal("account_active 没有 detail")
	}
	got, ok := (*c.Detail)["subscription_last_ok_fetched_at"].(time.Time)
	if !ok || !got.Equal(lastOK) {
		t.Fatalf("detail 里的 last_ok = %v, want %v —— 少了它就分不出「没拉」与「一直 404」", (*c.Detail)["subscription_last_ok_fetched_at"], lastOK)
	}
}

// data_delay_note「不是装饰」：三个流量口径天然不一致，不写这一句必然有用户
// 拿客户端的数字质问面板的数字。
func TestDiagnoseAlwaysCarriesDelayNote(t *testing.T) {
	r := buildDiagnose(dbgen.GetUserDiagnoseFactsRow{TransferEnable: 1}, time.Now())
	if strings.TrimSpace(r.DataDelayNote) == "" {
		t.Fatal("data_delay_note 为空")
	}
	if len(r.Checks) != 4 {
		t.Fatalf("checks = %d 项, want 4（契约的枚举写死了这四项）", len(r.Checks))
	}
}

func TestDiagnoseBannedAccount(t *testing.T) {
	r := buildDiagnose(dbgen.GetUserDiagnoseFactsRow{
		Banned: true, BannedReason: ptrOf("滥用"), TransferEnable: 1,
	}, time.Now())
	c := diagnoseCheck(t, r, gen.AccountActive)
	if c.Ok {
		t.Fatal("被封禁的账号 account_active 仍为 true")
	}
	if c.Detail == nil || (*c.Detail)["banned_reason"] != "滥用" {
		t.Errorf("detail 缺 banned_reason：%v", c.Detail)
	}
}

func diagnoseCheck(t *testing.T, r gen.DiagnoseResult, key gen.DiagnoseCheckKey) gen.DiagnoseCheck {
	t.Helper()
	for _, c := range r.Checks {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("自检结果里没有 %q", key)
	return gen.DiagnoseCheck{}
}

// ============================================================
// 节点负载上报
// ============================================================

func TestStatusReportParamsValidation(t *testing.T) {
	ok := gen.NodeStatusReport{
		Cpu:  12.5,
		Mem:  gen.NodeResourceUsage{Total: 1000, Used: 400},
		Swap: gen.NodeResourceUsage{Total: 0, Used: 0},
		Disk: gen.NodeResourceUsage{Total: 2000, Used: 1000},
	}
	if p, reason := statusReportParams(7, ok); reason != "" {
		t.Fatalf("合法上报被拒：%s", reason)
	} else {
		if p.ServerID != 7 {
			t.Errorf("server_id = %d", p.ServerID)
		}
		if p.CpuPct < 12.4 || p.CpuPct > 12.6 {
			t.Errorf("cpu_pct = %v", p.CpuPct)
		}
		if p.SwapTotal == nil || *p.SwapTotal != 0 {
			t.Errorf("没有 swap 分区（total = 0）是常见配置，必须原样落库：%v", p.SwapTotal)
		}
	}

	bad := []struct {
		name string
		rep  gen.NodeStatusReport
	}{
		{"cpu 超 100", gen.NodeStatusReport{Cpu: 100.1}},
		{"cpu 为负", gen.NodeStatusReport{Cpu: -0.1}},
		// NaN 与任何值比较都是 false，所以它能穿过 `<0 || >100` 那一对判断。必须单独挡。
		{"cpu 是 NaN", gen.NodeStatusReport{Cpu: math.NaN()}},
		{"mem used > total", gen.NodeStatusReport{Cpu: 1, Mem: gen.NodeResourceUsage{Total: 10, Used: 11}}},
		{"disk 为负", gen.NodeStatusReport{Cpu: 1, Disk: gen.NodeResourceUsage{Total: -1}}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, reason := statusReportParams(1, c.rep); reason == "" {
				t.Fatal("非法上报被接受了 —— 一个恒为「正常」的负载图比没有图更糟")
			}
		})
	}
}

// UpsertServerLoadSnapshotParams 里**不能有** online_users：
// 冻结契约的 NodeStatusReport 根本没有这个字段，handler 只能传 0，
// 于是每 60 秒一次的负载上报都会把在线人数清零 —— 而一个恒为 0 的运营指标
// 看起来就像「今晚没人用」。这条用编译期存在性来守：
// 参数结构体上有这个字段的话，下面这行赋值就编译不过。
func TestLoadSnapshotParamsHasNoOnlineUsers(t *testing.T) {
	var p dbgen.UpsertServerLoadSnapshotParams
	// 字段清单固定为负载七列 + server_id。
	p = dbgen.UpsertServerLoadSnapshotParams{
		ServerID: 1, CpuPct: 1,
		MemTotal: ptrOf(int64(1)), MemUsed: ptrOf(int64(1)),
		SwapTotal: ptrOf(int64(1)), SwapUsed: ptrOf(int64(1)),
		DiskTotal: ptrOf(int64(1)), DiskUsed: ptrOf(int64(1)),
	}
	if p.ServerID != 1 {
		t.Fatal("unreachable")
	}
}

// ============================================================
// 订阅链接
// ============================================================

func TestAPIBaseURLFromRequest(t *testing.T) {
	srv := &Server{
		cfg:    &config.Config{Env: "test", TrustProxyHeaders: true},
		logger: testLogger(),
	}
	r := httptest.NewRequest(http.MethodGet, "http://example.invalid/api/v1/user/subscription", nil)
	r.Host = "api.babel.example"
	r.Header.Set("X-Forwarded-Proto", "https, http")
	ctx := context.WithValue(context.Background(), ctxKeyBoundRequest{}, r)

	if got := srv.apiBaseURL(ctx); got != "https://api.babel.example" {
		t.Fatalf("apiBaseURL = %q（多层代理拼出的 \"https, http\" 只取第一段）", got)
	}
	if got := shortSubscribeURL(srv.apiBaseURL(ctx), "TOKEN"); got != "https://api.babel.example/s/TOKEN" {
		t.Errorf("short = %q", got)
	}
}

func TestAPIBaseURLWithoutBoundRequest(t *testing.T) {
	srv := &Server{cfg: &config.Config{Env: "test"}, logger: testLogger()}
	if got := srv.apiBaseURL(context.Background()); got != "" {
		t.Fatalf("拿不到请求时应退化成空串（相对路径仍可用），got %q", got)
	}
}

func TestAPIBaseURLPlainHTTPWhenNotTrustingProxy(t *testing.T) {
	srv := &Server{cfg: &config.Config{Env: "dev"}, logger: testLogger()}
	r := httptest.NewRequest(http.MethodGet, "http://localhost:8080/x", nil)
	ctx := context.WithValue(context.Background(), ctxKeyBoundRequest{}, r)
	if got := srv.apiBaseURL(ctx); !strings.HasPrefix(got, "http://") {
		t.Fatalf("本地开发是明文 http，写死 https 会给出一条打不开的链接：%q", got)
	}
}
