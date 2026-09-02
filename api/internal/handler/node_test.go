package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
)

// 本文件只测纯函数。Server.db 是具体类型 *store.Store，塞不了假实现，
// 所以 handler 方法本身要等集成测试；但节点面最致命的几个 bug 全部在形状层，
// 而形状层是可以在这里被彻底钉住的。

// ============================================================
// 🔴 头号回归：空用户列表必须序列化成 {"users":[]}
// ============================================================

// TestBuildNodeUserList_EmptySerializesAsArray 是本文件存在的首要理由。
//
// v2node 的 JSON 回退路径是**流式解析**：扫描到 "users" 键后，
// 要求下一个 token 就是 `[`（evidence/v2node-contract-20260817 §4 的源码）。
// {"users": null} 会让它直接报 `expected "users" array` 并放弃整次拉取。
//
// 这个 bug 的症状是**节点侧报错、面板侧一切正常**：
// 面板上用户列表看着没问题，节点日志里一句解析错误，中间没有任何关联线索。
// 而它的触发条件平淡到不可能不发生 —— 一个刚建好还没分配用户的新节点。
func TestBuildNodeUserList_EmptySerializesAsArray(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []dbgen.ListAvailableUsersByServerRow
	}{
		{"nil 切片（sqlc 若丢掉 emit_empty_slices 就是这个）", nil},
		{"空切片（emit_empty_slices 的正常输出）", []dbgen.ListAvailableUsersByServerRow{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, skipped := buildNodeUserList(tc.rows)
			if skipped != 0 {
				t.Fatalf("skipped = %d, want 0", skipped)
			}
			if list.Users == nil {
				t.Fatal("Users 是 nil —— 它会序列化成 null 并让 v2node 解析失败")
			}

			got, err := json.Marshal(list)
			if err != nil {
				t.Fatalf("序列化失败: %v", err)
			}
			if string(got) != `{"users":[]}` {
				t.Fatalf("序列化结果 = %s, want {\"users\":[]}", got)
			}
		})
	}
}

// TestNodeUserList_NilUsersIsTheHazard 反向钉住上面那条不变量的前提。
//
// 如果哪天 gen.NodeUserList 换成了自定义 MarshalJSON 并把 nil 也输出成 []，
// 上面那个测试就变成了永真的空转，而我们会以为自己还有保护。
// 这条断言的是「nil 确实是危险的」—— 它一旦失败，说明保护点应该搬家。
func TestNodeUserList_NilUsersIsTheHazard(t *testing.T) {
	got, err := json.Marshal(gen.NodeUserList{Users: nil})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if string(got) != `{"users":null}` {
		t.Fatalf("nil Users 序列化成 %s；"+
			"若已不再是 null，buildNodeUserList 的 make(...) 保护就不再是承重的，请重新评估", got)
	}
}

// TestBuildAliveList_EmptySerializesAsObject 是同一类不变量的另一半。
//
// nil map 序列化成 {"alive":null}，而 v2node 反序列化的目标是 map[int]int。
func TestBuildAliveList_EmptySerializesAsObject(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []dbgen.ListAliveDeviceCountsRow
	}{
		{"nil 切片", nil},
		{"空切片", []dbgen.ListAliveDeviceCountsRow{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := buildAliveList(tc.rows)
			if list.Alive == nil {
				t.Fatal("Alive 是 nil map —— 它会序列化成 null")
			}
			got, err := json.Marshal(list)
			if err != nil {
				t.Fatalf("序列化失败: %v", err)
			}
			if string(got) != `{"alive":{}}` {
				t.Fatalf("序列化结果 = %s, want {\"alive\":{}}", got)
			}
		})
	}
}

func TestBuildAliveList_KeysAreDecimalStrings(t *testing.T) {
	list := buildAliveList([]dbgen.ListAliveDeviceCountsRow{
		{UserID: 1, Alive: 2},
		{UserID: 19, Alive: 3},
	})
	got, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	// 契约冻结形状：{"alive": {"1": 2, "19": 3}}（encoding/json 对 map key 排序）
	if string(got) != `{"alive":{"1":2,"19":3}}` {
		t.Fatalf("序列化结果 = %s", got)
	}
}

// ============================================================
// 用户列表映射
// ============================================================

func TestBuildNodeUserList_Mapping(t *testing.T) {
	uuidA := mustUUID(t, "8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92")
	limit5 := int32(5)
	speed100 := int32(100)

	list, skipped := buildNodeUserList([]dbgen.ListAvailableUsersByServerRow{
		{ID: 1, Uuid: uuidA, SpeedLimitMbps: nil, DeviceLimit: &limit5},
		{ID: 7, Uuid: uuidA, SpeedLimitMbps: &speed100, DeviceLimit: nil},
	})
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(list.Users) != 2 {
		t.Fatalf("len(Users) = %d, want 2", len(list.Users))
	}
	// NULL = 不限速 / 不限设备，对节点端的表达都是 0（照抄 Xboard 口径）。
	if list.Users[0].SpeedLimit != 0 || list.Users[0].DeviceLimit != 5 {
		t.Fatalf("第 1 个用户映射错: %+v", list.Users[0])
	}
	if list.Users[1].SpeedLimit != 100 || list.Users[1].DeviceLimit != 0 {
		t.Fatalf("第 2 个用户映射错: %+v", list.Users[1])
	}
	if list.Users[0].Uuid != "8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92" {
		t.Fatalf("uuid = %q", list.Users[0].Uuid)
	}
}

// 下发空 uuid 等于给节点一个「空密码」的用户条目，必须跳过而不是照发。
func TestBuildNodeUserList_SkipsInvalidUUID(t *testing.T) {
	list, skipped := buildNodeUserList([]dbgen.ListAvailableUsersByServerRow{
		{ID: 1, Uuid: pgtype.UUID{Valid: false}},
		{ID: 2, Uuid: mustUUID(t, "8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92")},
	})
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if len(list.Users) != 1 || list.Users[0].Id != 2 {
		t.Fatalf("Users = %+v", list.Users)
	}
}

// ============================================================
// /config 组装
// ============================================================

func TestBuildNodeConfig_VlessReality(t *testing.T) {
	port := int32(8443)
	cfg, err := buildNodeConfig(dbgen.GetServerConfigRow{
		Code: "bp-node-hk1", Protocol: dbgen.ServerProtocolVlessReality,
		Host: "hk1.example.invalid", Port: 443, ServerPort: &port,
		ProtocolSettings: []byte(`{
			"reality_private_key": "wKq3",
			"reality_short_id": "6ba85179e30d4fc2",
			"reality_dest": "www.cloudflare.com:443"
		}`),
	})
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	assertStr(t, "protocol", cfg.Protocol, "vless")
	assertStr(t, "network", cfg.Network, "tcp")
	assertStr(t, "flow", cfg.Flow, "xtls-rprx-vision")
	assertStr(t, "listen_ip", cfg.ListenIp, "0.0.0.0")
	if cfg.Tls == nil || *cfg.Tls != 2 {
		t.Fatalf("tls = %v, want 2（REALITY）", cfg.Tls)
	}
	// server_port 优先取 servers.server_port（端口跳跃时与客户端连的端口不同）
	if cfg.ServerPort == nil || *cfg.ServerPort != 8443 {
		t.Fatalf("server_port = %v, want 8443", cfg.ServerPort)
	}
	if cfg.TlsSettings == nil {
		t.Fatal("tls_settings 缺失")
	}
	// server_name 未配时取 dest 的主机名
	assertStr(t, "tls_settings.server_name", cfg.TlsSettings.ServerName, "www.cloudflare.com")
	assertStr(t, "tls_settings.xver", cfg.TlsSettings.Xver, "0")
}

func TestBuildNodeConfig_VlessRealityRequiresCredentials(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings string
		wantHint string
	}{
		{"缺私钥", `{"reality_short_id":"a","reality_dest":"x:443"}`, "reality_private_key"},
		{"缺 short_id", `{"reality_private_key":"k","reality_dest":"x:443"}`, "reality_short_id"},
		{"缺 dest", `{"reality_private_key":"k","reality_short_id":"a"}`, "reality_dest"},
		{"空 protocol_settings", `{}`, "reality_private_key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildNodeConfig(dbgen.GetServerConfigRow{
				Code: "n1", Protocol: dbgen.ServerProtocolVlessReality, Port: 443,
				ProtocolSettings: []byte(tc.settings),
			})
			if err == nil {
				t.Fatal("配置不全却组装成功了 —— 那会下发一份让节点起不来（或不设防）的配置")
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("错误里没指出缺哪个字段: %v", err)
			}
		})
	}
}

// 🔴 ADR 0004：Hysteria2 用 BBR 不用 Brutal，而 up/down_mbps 就是 Brutal 的参数。
// 这条断言的不是「字段存在」，而是「值必须是 0 且必须被下发」——
// 省略字段与下发 0 对 v2node 是两件事（省略走它自己的默认值）。
func TestBuildNodeConfig_Hysteria2DisablesBrutal(t *testing.T) {
	cfg, err := buildNodeConfig(dbgen.GetServerConfigRow{
		Code: "bp-node-hk2", Protocol: dbgen.ServerProtocolHysteria2,
		Host: "hk2.example.invalid", Port: 443,
		ProtocolSettings: []byte(`{"obfs_password":"Jc7"}`),
	})
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if cfg.UpMbps == nil || *cfg.UpMbps != 0 {
		t.Fatalf("up_mbps = %v, want 0（下发 0 = 不启用 Brutal）", cfg.UpMbps)
	}
	if cfg.DownMbps == nil || *cfg.DownMbps != 0 {
		t.Fatalf("down_mbps = %v, want 0", cfg.DownMbps)
	}

	// 序列化后必须真的出现这两个键 —— NodeConfig 全字段 omitempty，
	// 一个 *int32(0) 与 nil 在结构体上很像，在 JSON 里天差地别。
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	for _, k := range []string{"up_mbps", "down_mbps"} {
		v, ok := m[k]
		if !ok {
			t.Fatalf("响应体里没有 %s 键：省略字段会让 v2node 用自己的默认值", k)
		}
		if v != float64(0) {
			t.Fatalf("%s = %v, want 0", k, v)
		}
	}
	// 🔴 hysteria2 不是 hysteria：v2node v0.4.3 收到 hysteria 会让整个进程退出（2026-09-02 真机实测）。
	assertStr(t, "protocol", cfg.Protocol, "hysteria2")
	// tls=1 缺失时 v2node 报 "hysteria: tls config is nil" 并整个退出（2026-09-02 真机实测）。
	if cfg.Tls == nil || *cfg.Tls != tlsModeTLS {
		t.Fatalf("tls = %v, want %d（Hysteria2 的 TLS 不是可选项）", cfg.Tls, tlsModeTLS)
	}
	assertStr(t, "obfs", cfg.Obfs, "salamander")
	// server_name 未配时取 servers.host
	assertStr(t, "server_name", cfg.ServerName, "hk2.example.invalid")
	if cfg.Version == nil || *cfg.Version != 2 {
		t.Fatalf("version = %v, want 2", cfg.Version)
	}
}

// 证书路径经 tls_settings 原样下发：v2node 用 cert_mode/cert_file/key_file 构造 Hysteria2 的 TLS。
// 路径必须是 LoadCredential 挂载后的 /run/credentials/… —— 这是库里存什么就发什么，不在这里改写。
func TestBuildNodeConfig_Hysteria2TlsSettingsPassthrough(t *testing.T) {
	cfg, err := buildNodeConfig(dbgen.GetServerConfigRow{
		Code: "n", Protocol: dbgen.ServerProtocolHysteria2, Host: "203.0.113.10", Port: 443,
		ProtocolSettings: []byte(`{"server_name":"hk1.example.invalid","cert_mode":"file",` +
			`"cert_file":"/run/credentials/bp-node.service/fullchain.pem",` +
			`"key_file":"/run/credentials/bp-node.service/privkey.pem"}`),
	})
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if cfg.TlsSettings == nil {
		t.Fatalf("tls_settings 缺失：v2node 拿不到证书路径，Hysteria2 起不来")
	}
	assertStr(t, "tls_settings.server_name", cfg.TlsSettings.ServerName, "hk1.example.invalid")
	assertStr(t, "tls_settings.cert_mode", cfg.TlsSettings.CertMode, "file")
	assertStr(t, "tls_settings.cert_file", cfg.TlsSettings.CertFile, "/run/credentials/bp-node.service/fullchain.pem")
	assertStr(t, "tls_settings.key_file", cfg.TlsSettings.KeyFile, "/run/credentials/bp-node.service/privkey.pem")

	// 没配证书字段时不下发 tls_settings（REALITY 之外的分支不该无端多一个空对象）。
	cfg2, err := buildNodeConfig(dbgen.GetServerConfigRow{
		Code: "n", Protocol: dbgen.ServerProtocolHysteria2, Host: "h", Port: 443,
		ProtocolSettings: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if cfg2.TlsSettings != nil {
		t.Fatalf("未配证书字段却下发了 tls_settings: %+v", cfg2.TlsSettings)
	}
}

// 没有 obfs 密码时不下发 obfs：只发 obfs 不发密码会让节点起一个密码为空的混淆监听，
// 客户端表现为「握手就断」。不配混淆本身是合法的 Hysteria2 配置。
func TestBuildNodeConfig_Hysteria2OmitsObfsWithoutPassword(t *testing.T) {
	cfg, err := buildNodeConfig(dbgen.GetServerConfigRow{
		Code: "n", Protocol: dbgen.ServerProtocolHysteria2, Host: "h", Port: 443,
		ProtocolSettings: []byte(`{"obfs":"salamander"}`),
	})
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if cfg.Obfs != nil || cfg.ObfsPassword != nil {
		t.Fatalf("obfs=%v obfs-password=%v，两者必须成对出现或都不出现", cfg.Obfs, cfg.ObfsPassword)
	}
}

// base_config 的四个值是成本模型与计费口径的输入，改动必须是自觉的。
func TestBuildNodeConfig_BaseConfig(t *testing.T) {
	cfg, err := buildNodeConfig(dbgen.GetServerConfigRow{
		Code: "n", Protocol: dbgen.ServerProtocolHysteria2, Host: "h", Port: 443,
		ProtocolSettings: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	bc := cfg.BaseConfig
	if bc == nil {
		t.Fatal("base_config 缺失")
	}
	if bc.PullInterval == nil || *bc.PullInterval != 60 || bc.PushInterval == nil || *bc.PushInterval != 60 {
		t.Fatalf("pull/push_interval = %v/%v, want 60/60（请求量算术的输入）", bc.PullInterval, bc.PushInterval)
	}
	// 单位 KB（实测），1000 = 1 MB：防止空闲客户端吃掉设备名额。
	if bc.DeviceOnlineMinTraffic == nil || *bc.DeviceOnlineMinTraffic != 1000 {
		t.Fatalf("device_online_min_traffic = %v, want 1000（KB）", bc.DeviceOnlineMinTraffic)
	}
	// 单位未确认，取 0 = 不过滤。取非 0 在「单位是 KB」的读法下是持续的收入漏洞。
	if bc.NodeReportMinTraffic == nil || *bc.NodeReportMinTraffic != 0 {
		t.Fatalf("node_report_min_traffic = %v, want 0（单位未确认，0 是唯一安全值）", bc.NodeReportMinTraffic)
	}
}

func TestBuildNodeConfig_Shadowsocks2022(t *testing.T) {
	cfg, err := buildNodeConfig(dbgen.GetServerConfigRow{
		Code: "n", Protocol: dbgen.ServerProtocolShadowsocks2022, Host: "h", Port: 8388,
		ProtocolSettings: []byte(`{"server_key":"c2VjcmV0"}`),
	})
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if m["cipher"] != "2022-blake3-aes-128-gcm" || m["server_key"] != "c2VjcmV0" {
		t.Fatalf("additionalProperties 未下发: %v", m)
	}
}

// protocol_settings 是白名单不是透传：写进那一列的其他键**不得**出现在下发的配置里。
func TestBuildNodeConfig_DoesNotLeakUnknownSettings(t *testing.T) {
	cfg, err := buildNodeConfig(dbgen.GetServerConfigRow{
		Code: "n", Protocol: dbgen.ServerProtocolHysteria2, Host: "h", Port: 443,
		ProtocolSettings: []byte(`{"internal_note":"给运维看的","ssh_key":"AAAA"}`),
	})
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	for _, k := range []string{"internal_note", "ssh_key"} {
		if strings.Contains(string(raw), k) {
			t.Fatalf("未知键 %q 被透传给了节点: %s", k, raw)
		}
	}
}

func TestBuildNodeConfig_UnknownProtocolFails(t *testing.T) {
	_, err := buildNodeConfig(dbgen.GetServerConfigRow{
		Code: "n", Protocol: dbgen.ServerProtocol("wireguard"), Port: 443,
		ProtocolSettings: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("未知协议必须报错，不能下发一份空配置")
	}
}

// server_port 为空表示「监听端口 = 客户端连的端口」。下发 0 会让节点绑到随机端口。
func TestEffectiveServerPort_FallsBackToPort(t *testing.T) {
	zero := int32(0)
	for _, tc := range []struct {
		name string
		row  dbgen.GetServerConfigRow
		want int32
	}{
		{"server_port 为 NULL", dbgen.GetServerConfigRow{Port: 443}, 443},
		{"server_port 为 0", dbgen.GetServerConfigRow{Port: 443, ServerPort: &zero}, 443},
		{"server_port 有值", dbgen.GetServerConfigRow{Port: 443, ServerPort: ptrTo(int32(8443))}, 8443},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveServerPort(tc.row); got != tc.want {
				t.Fatalf("= %d, want %d", got, tc.want)
			}
		})
	}
}

// ============================================================
// /push 流量上报解析
// ============================================================

func TestParseTrafficBatch_Basic(t *testing.T) {
	b := parseTrafficBatch(gen.NodeTrafficPushRequest{
		"19": {0, 314572800},
		"1":  {10485760, 52428800},
		"7":  {1024, 8192},
	})
	if b.Dropped != 0 || b.Truncated {
		t.Fatalf("dropped=%d truncated=%v, want 0/false", b.Dropped, b.Truncated)
	}
	// 必须按 user_id 升序：Canonical 要与 map 迭代顺序无关（Go 的 map 迭代是随机的）。
	wantIDs := []int64{1, 7, 19}
	if len(b.UserIDs) != 3 {
		t.Fatalf("UserIDs = %v", b.UserIDs)
	}
	for i, want := range wantIDs {
		if b.UserIDs[i] != want {
			t.Fatalf("UserIDs = %v, want %v", b.UserIDs, wantIDs)
		}
	}
	if b.Up[0] != 10485760 || b.Down[0] != 52428800 {
		t.Fatalf("uid=1 的 up/down 配对错: %d/%d", b.Up[0], b.Down[0])
	}
	if len(b.UserIDs) != len(b.Up) || len(b.Up) != len(b.Down) {
		t.Fatal("三个数组不等长 —— WITH ORDINALITY 配对会静默丢尾部")
	}
}

// 幂等键是内容的函数，所以「同一批」必须算出同一个键，与 map 迭代顺序无关。
// 这条挂了的后果是重放挡不住 = 重复计费。
func TestTrafficBatchKey_StableAcrossMapIteration(t *testing.T) {
	raw := gen.NodeTrafficPushRequest{
		"3": {1, 2}, "1": {3, 4}, "2": {5, 6}, "9": {7, 8}, "4": {9, 10},
	}
	first := trafficBatchKey(7, parseTrafficBatch(raw).Canonical)
	for i := 0; i < 32; i++ {
		if got := trafficBatchKey(7, parseTrafficBatch(raw).Canonical); got != first {
			t.Fatalf("第 %d 次算出不同的幂等键: %s != %s", i, got, first)
		}
	}
	// 不同节点的同一批内容必须是不同的键，否则 A 节点的上报会挡掉 B 节点的。
	if trafficBatchKey(8, parseTrafficBatch(raw).Canonical) == first {
		t.Fatal("不同 server_id 算出了相同的幂等键")
	}
	// 内容变了键必须变，否则真实的下一批会被误判为重放丢掉。
	other := parseTrafficBatch(gen.NodeTrafficPushRequest{"3": {1, 2}, "1": {3, 5}})
	if trafficBatchKey(7, other.Canonical) == first {
		t.Fatal("不同内容算出了相同的幂等键")
	}
}

// 幂等键必须落在 httpx.ValidateIdempotencyKey 的长度与字符集约束内，
// 否则 BeginIdempotent 会在校验阶段直接拒绝，整条防重放形同虚设。
func TestTrafficBatchKey_IsAValidIdempotencyKey(t *testing.T) {
	key := trafficBatchKey(1234567890, parseTrafficBatch(gen.NodeTrafficPushRequest{"1": {1, 1}}).Canonical)
	if len(key) < 8 || len(key) > 128 {
		t.Fatalf("键长 %d 越界: %s", len(key), key)
	}
	for i := 0; i < len(key); i++ {
		if c := key[i]; c <= 0x20 || c >= 0x7f {
			t.Fatalf("键含非法字符 %q: %s", c, key)
		}
	}
}

// 契约 §3.5：坏条目静默丢弃，不让整批失败。
// 整批失败对我们是纯损失 —— v2node 不看状态码，不会因此重发。
func TestParseTrafficBatch_DropsMalformedEntries(t *testing.T) {
	b := parseTrafficBatch(gen.NodeTrafficPushRequest{
		"1":     {100, 200}, // 好的
		"2":     {100},      // 长度不是 2
		"3":     {1, 2, 3},  // 长度不是 2
		"4":     {-1, 5},    // 负数
		"5":     {5, -1},    // 负数
		"abc":   {1, 2},     // key 不是数字
		"0":     {1, 2},     // user id 必须 > 0
		"-3":    {1, 2},     // 同上
		"01":    {1, 2},     // 非规范十进制：会与 "1" 撞成同一个 uid，破坏幂等指纹
		"+1":    {1, 2},     // 同上
		" 1":    {1, 2},     // 同上
		"1e3":   {1, 2},     // 同上
		"99999": {0, 0},     // 全零：不算丢弃，只是没有信息
	})
	if len(b.UserIDs) != 1 || b.UserIDs[0] != 1 {
		t.Fatalf("只应留下 uid=1，实际 %v", b.UserIDs)
	}
	// 11 条非法（全零那条不计入 dropped）
	if b.Dropped != 11 {
		t.Fatalf("dropped = %d, want 11", b.Dropped)
	}
}

func TestParseTrafficBatch_EmptyIsSafe(t *testing.T) {
	b := parseTrafficBatch(gen.NodeTrafficPushRequest{})
	if len(b.UserIDs) != 0 || b.UserIDs == nil {
		t.Fatalf("空批必须是长度 0 的非 nil 切片，实际 %v", b.UserIDs)
	}
	if len(b.Canonical) != 0 {
		t.Fatalf("空批的 Canonical 应为空: %q", b.Canonical)
	}
}

// ============================================================
// /alive 在线设备上报解析
// ============================================================

func TestParseAliveBatch_Basic(t *testing.T) {
	b := parseAliveBatch(gen.NodeAlivePushRequest{
		"1": {"203.0.113.7", "198.51.100.42"},
		"7": {"2001:db8::1"},
	})
	if b.Dropped != 0 || b.Truncated {
		t.Fatalf("dropped=%d truncated=%v", b.Dropped, b.Truncated)
	}
	if len(b.UserIDs) != 3 || len(b.DeviceIPs) != 3 {
		t.Fatalf("摊平结果 = %v / %v", b.UserIDs, b.DeviceIPs)
	}
	// 两个数组按下标配对是 BulkUpsertUserDeviceState 的硬要求（WITH ORDINALITY）。
	if len(b.UserIDs) != len(b.DeviceIPs) {
		t.Fatal("两个数组不等长")
	}
	for i, uid := range b.UserIDs {
		if uid != 1 && uid != 7 {
			t.Fatalf("下标 %d 的 uid 异常: %d", i, uid)
		}
	}
}

// ::ffff:203.0.113.7 与 203.0.113.7 是同一台机器。不规范化就会占两个设备名额，
// 而设备数正是我们的定价杠杆（2/5/10 档）。
func TestParseAliveBatch_NormalizesIPv4MappedAndZone(t *testing.T) {
	b := parseAliveBatch(gen.NodeAlivePushRequest{
		"1": {"::ffff:203.0.113.7", "203.0.113.7", "fe80::1%eth0"},
	})
	if b.Dropped != 0 {
		t.Fatalf("dropped = %d, want 0", b.Dropped)
	}
	got := map[string]int{}
	for _, ip := range b.DeviceIPs {
		got[ip]++
	}
	if got["203.0.113.7"] != 2 {
		t.Fatalf("v4-mapped 未归一化: %v", b.DeviceIPs)
	}
	if got["fe80::1"] != 1 {
		t.Fatalf("zone 未剥离（带 %%zone 会让 inet 转换整条 SQL 报错）: %v", b.DeviceIPs)
	}
}

func TestParseAliveBatch_DropsMalformed(t *testing.T) {
	b := parseAliveBatch(gen.NodeAlivePushRequest{
		"1":   {"203.0.113.7", "not-an-ip", "", "203.0.113.7/32"},
		"xyz": {"203.0.113.9"},
		"0":   {"203.0.113.10"},
	})
	if len(b.UserIDs) != 1 || b.UserIDs[0] != 1 || b.DeviceIPs[0] != "203.0.113.7" {
		t.Fatalf("只应留下 (1, 203.0.113.7)，实际 %v / %v", b.UserIDs, b.DeviceIPs)
	}
	// 3 个坏 IP + 2 个坏 uid
	if b.Dropped != 5 {
		t.Fatalf("dropped = %d, want 5", b.Dropped)
	}
}

func TestParseAliveBatch_EmptyIsSafe(t *testing.T) {
	b := parseAliveBatch(gen.NodeAlivePushRequest{"1": {}})
	if b.UserIDs == nil || b.DeviceIPs == nil {
		t.Fatal("空批必须是非 nil 切片")
	}
	if len(b.UserIDs) != 0 {
		t.Fatalf("UserIDs = %v", b.UserIDs)
	}
}

// ============================================================
// 辅助
// ============================================================

func TestHostOf(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"www.cloudflare.com:443", "www.cloudflare.com"},
		{"www.cloudflare.com", "www.cloudflare.com"},
		{"", ""},
	} {
		if got := hostOf(tc.in); got != tc.want {
			t.Fatalf("hostOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("构造 uuid 失败: %v", err)
	}
	return u
}

func assertStr(t *testing.T, name string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s 缺失, want %q", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %q, want %q", name, *got, want)
	}
}

// TestRealityDestIsSplitIntoHostAndPort 钉死 REALITY 回落目标的下发形态。
//
// 🔴 这是一条真机实测出来的回归：v2node 的 core/inbound.go 在 Reality 分支里拼的是
//
//	fmt.Sprintf("%s:%s", TlsSettings.Dest, TlsSettings.ServerPort)
//
// 所以 dest **必须是主机名、不能带端口**。带了的话拼出来是 "www.microsoft.com:443:"，
// xray 报 `please fill in a valid value for "target"` ——
// **报错指向 target 字段，而真正的原因是我们多发了一个冒号**。
// 这种「错误信息指向 A、真因在 B」的缺陷，靠读代码是发现不了的，
// 2026-09-01 第一次把真节点接上来时才撞到。
//
// 库里的 reality_dest 仍然存 host:port（人读着方便），拆分只发生在下发这一步 ——
// 所以本用例同时钉住「库里带端口」与「下发不带端口」两侧。
func TestRealityDestIsSplitIntoHostAndPort(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dest     string
		wantHost string
		wantPort string
	}{
		{"带端口", "www.microsoft.com:443", "www.microsoft.com", "443"},
		{"非 443 端口", "example.com:8443", "example.com", "8443"},
		{"不带端口时默认 443", "example.com", "example.com", "443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := buildNodeConfig(dbgen.GetServerConfigRow{
				Code:     "bp-node-test",
				Protocol: dbgen.ServerProtocolVlessReality,
				Port:     443,
				ProtocolSettings: []byte(`{
					"reality_private_key":"PRIVKEY",
					"reality_short_id":"a1b2c3d4",
					"reality_dest":"` + tc.dest + `"
				}`),
			})
			if err != nil {
				t.Fatalf("buildNodeConfig: %v", err)
			}
			if cfg.TlsSettings == nil {
				t.Fatal("tls_settings 为空")
			}
			if cfg.TlsSettings.Dest == nil || *cfg.TlsSettings.Dest != tc.wantHost {
				t.Errorf("dest = %v，期望 %q（**不得带端口**）", cfg.TlsSettings.Dest, tc.wantHost)
			}
			if strings.Contains(*cfg.TlsSettings.Dest, ":") {
				t.Errorf("dest 里出现了冒号：%q —— v2node 会再拼一次端口，结果是无效的 target",
					*cfg.TlsSettings.Dest)
			}
			if cfg.TlsSettings.ServerPort == nil || *cfg.TlsSettings.ServerPort != tc.wantPort {
				t.Errorf("tls_settings.server_port = %v，期望 %q（**字符串**）",
					cfg.TlsSettings.ServerPort, tc.wantPort)
			}
			// server_name 缺省应取主机名，不是整串。
			if cfg.TlsSettings.ServerName == nil || *cfg.TlsSettings.ServerName != tc.wantHost {
				t.Errorf("server_name = %v，期望 %q", cfg.TlsSettings.ServerName, tc.wantHost)
			}
		})
	}
}
