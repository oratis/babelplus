package subgen

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// 这批单测护的是「渲染出来的东西客户端认不认」。
//
// 它**不能替代** ADR 0006 §12 的人工验证（每次改格式用 Clash Verge Rev /
// sing-box 各加载一次）—— 单测只能保证结构与字段名没有在重构里被改掉，
// 保证不了「mihomo 这个版本认不认这个键名」。所以断言写在字段名与关键取值上，
// 而不是整份 golden file：golden file 会把「需实测的键名」伪装成「已验证的事实」。

// sampleProxies 是三种真实协议各一个，覆盖所有渲染分支。
func sampleProxies() []Proxy {
	return []Proxy{
		{
			Name: "HK-1 · REALITY", Kind: KindVLESSReality,
			Server: "203.0.113.10", Port: 443,
			UUID: "8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92",
			Flow: "xtls-rprx-vision", SNI: "www.cloudflare.com",
			Fingerprint: "chrome", PublicKey: "7Xk1publickey", ShortID: "6ba85179e30d4fc2",
			Mux: true,
		},
		{
			Name: "HK-1 · HY2 加速", Kind: KindHysteria2,
			Server: "203.0.113.10", Port: 443,
			Password: "8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92",
			ObfsType: "salamander", ObfsPassword: "Jc7obfs", SNI: "hk1.example.invalid",
		},
		{
			Name: "TW-1 · SS", Kind: KindShadowsocks2022,
			Server: "203.0.113.20", Port: 8388,
			Method: "2022-blake3-aes-128-gcm", Password: "8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92",
		},
	}
}

func TestRenderRejectsEmptyDocument(t *testing.T) {
	// 真的下发一份空 proxies 会让部分客户端拒绝导入整份配置 ——
	// 用户连伪节点上那句话都看不到。所以这里必须是错误而不是「渲染出空列表」。
	for _, f := range []Format{FormatClash, FormatSingbox, FormatBase64} {
		if _, err := Render(f, Document{}); err != ErrEmptyDocument {
			t.Errorf("Render(%s, 空文档) = %v，期望 ErrEmptyDocument", f, err)
		}
	}
}

func TestFormatContentTypeAlwaysCarriesCharset(t *testing.T) {
	// 节点名里有中文与 emoji，缺 charset 会让部分客户端按 latin-1 解码，
	// 现象是节点名乱码而不是一个能被搜索的错误。
	for _, f := range []Format{FormatClash, FormatSingbox, FormatBase64} {
		if !strings.Contains(f.ContentType(), "charset=utf-8") {
			t.Errorf("%s 的 Content-Type %q 缺 charset", f, f.ContentType())
		}
	}
}

// ---- Clash / mihomo YAML ----

func TestRenderClashFields(t *testing.T) {
	out, err := Render(FormatClash, Document{Proxies: sampleProxies()})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := string(out)

	// api-contract §4.5 逐条列出的字段。任意一条消失都会让对应节点在客户端里
	// 「能导入、能显示、连不上」。
	want := []string{
		`- name: "HK-1 · REALITY"`,
		"type: vless",
		"flow: \"xtls-rprx-vision\"",
		`servername: "www.cloudflare.com"`,
		`client-fingerprint: "chrome"`,
		"reality-opts:",
		`public-key: "7Xk1publickey"`,
		`short-id: "6ba85179e30d4fc2"`,
		"smux:",
		"protocol: h2mux",
		"type: hysteria2",
		`obfs: "salamander"`,
		`obfs-password: "Jc7obfs"`,
		"proxy-groups:",
		`- name: "默认"`,
		"type: fallback",
		`- name: "加速"`,
		"type: select",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("Clash 输出缺少 %q\n---\n%s", w, got)
		}
	}
}

func TestRenderClashOmitsBrutalBandwidth(t *testing.T) {
	// 🔴 ADR 0004 §裁决 1：Hysteria2 用 BBR 不用 Brutal。
	// mihomo 的 up / down 就是 Brutal 的带宽声明 —— 填了就等于启用 Brutal，
	// 而 Brutal 在丢包时提高发送速率，特征 100% 可分。
	// 这条断言存在的意义是：将来有人「顺手补上带宽让它更快」时测试会红。
	out, _ := Render(FormatClash, Document{Proxies: sampleProxies()})
	for _, forbidden := range []string{"\n    up:", "\n    down:", "up-mbps", "down-mbps"} {
		if strings.Contains(string(out), forbidden) {
			t.Errorf("Clash 输出出现带宽声明 %q —— 那会启用 Brutal，违反 ADR 0004 §裁决 1", forbidden)
		}
	}
}

func TestRenderClashMuxOnlyOnTCPPath(t *testing.T) {
	// ADR 0004 §裁决 2：TCP 路径（REALITY）开 mux，UDP 路径（Hysteria2）不开。
	out, _ := Render(FormatClash, Document{Proxies: sampleProxies()})
	blocks := strings.Split(string(out), "  - name: ")
	for _, b := range blocks {
		switch {
		case strings.Contains(b, "type: vless"):
			if !strings.Contains(b, "smux:") {
				t.Error("REALITY 节点缺少 smux —— 违反 ADR 0004 §裁决 2")
			}
		case strings.Contains(b, "type: hysteria2"):
			if strings.Contains(b, "smux:") {
				t.Error("Hysteria2 节点带了 smux —— UDP 路径不该开 mux（ADR 0004 §裁决 2）")
			}
		}
	}
}

func TestYAMLStringEscapes(t *testing.T) {
	cases := map[string]string{
		`plain`:      `"plain"`,
		`say "hi"`:   `"say \"hi\""`,
		`back\slash`: `"back\\slash"`,
		"line\nbrk":  `"line\nbrk"`,
		// 非 ASCII 不转义：转成 \uXXXX 会让人工核对变得不可能，
		// 而 YAML 1.2 的字符集本来就是 UTF-8。
		"港区 · 主力": `"港区 · 主力"`,
	}
	for in, want := range cases {
		if got := yamlString(in); got != want {
			t.Errorf("yamlString(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// ---- sing-box JSON ----

func TestRenderSingboxStructure(t *testing.T) {
	out, err := Render(FormatSingbox, Document{Proxies: sampleProxies()})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var cfg struct {
		Log       map[string]any   `json:"log"`
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("产出不是合法 JSON: %v\n%s", err, out)
	}
	if len(cfg.Outbounds) != 4 {
		t.Fatalf("outbounds 数量 = %d，期望 1 个 selector + 3 个节点", len(cfg.Outbounds))
	}

	sel := cfg.Outbounds[0]
	if sel["type"] != "selector" || sel["tag"] != GroupDefault {
		t.Errorf("第一个 outbound 应是 tag=%q 的 selector，实际 %v", GroupDefault, sel)
	}
	if sel["default"] != "HK-1 · REALITY" {
		t.Errorf("selector.default = %v，期望排在最前的节点", sel["default"])
	}

	vless := cfg.Outbounds[1]
	if vless["flow"] != "xtls-rprx-vision" {
		t.Errorf("vless.flow = %v", vless["flow"])
	}
	tls, _ := vless["tls"].(map[string]any)
	if tls["server_name"] != "www.cloudflare.com" {
		t.Errorf("tls.server_name = %v", tls["server_name"])
	}
	reality, _ := tls["reality"].(map[string]any)
	if reality["enabled"] != true || reality["public_key"] != "7Xk1publickey" {
		t.Errorf("tls.reality = %v", reality)
	}
	utls, _ := tls["utls"].(map[string]any)
	if utls["fingerprint"] != "chrome" {
		t.Errorf("tls.utls = %v", utls)
	}
	mux, _ := vless["multiplex"].(map[string]any)
	if mux["enabled"] != true || mux["protocol"] != "h2mux" {
		t.Errorf("vless.multiplex = %v —— TCP 路径必须开 mux（ADR 0004 §裁决 2）", mux)
	}

	hy2 := cfg.Outbounds[2]
	// 🔴 up_mbps / down_mbps 必须**不存在**：sing-box 文档明写留空则用 BBR，
	// 显式填写才启用 Brutal。ADR 0004 §裁决 1 选的是 BBR。
	if _, ok := hy2["up_mbps"]; ok {
		t.Error("hysteria2 出现 up_mbps —— 那会启用 Brutal，违反 ADR 0004 §裁决 1")
	}
	if _, ok := hy2["down_mbps"]; ok {
		t.Error("hysteria2 出现 down_mbps —— 违反 ADR 0004 §裁决 1")
	}
	if _, ok := hy2["multiplex"]; ok {
		t.Error("hysteria2 出现 multiplex —— UDP 路径不开 mux（ADR 0004 §裁决 2）")
	}
	// tls 对 hysteria2 是必填项（sing-box v1.13 文档已核实），漏了配置直接加载失败。
	hy2tls, _ := hy2["tls"].(map[string]any)
	if hy2tls["enabled"] != true || hy2tls["server_name"] != "hk1.example.invalid" {
		t.Errorf("hysteria2.tls = %v", hy2tls)
	}
}

func TestRenderSingboxDoesNotHTMLEscapeNames(t *testing.T) {
	// 节点名出现 & 完全正常（"HK & TW"）。encoding/json 默认会把它转成 &，
	// 客户端显示出来就是一串转义序列。
	out, err := Render(FormatSingbox, Document{Proxies: []Proxy{{
		Name: "HK & TW <主力>", Kind: KindVLESSReality, Server: "1.2.3.4", Port: 443,
		UUID: "u", SNI: "s", PublicKey: "p",
	}}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// encoding/json 默认会把 & < > 转成 & < >。
	// 断言写成「原始字节里必须出现节点名本身」—— 只要发生了转义这条就不成立，
	// 而且不用在测试里手写转义序列（那是最容易被下一个人写错的东西）。
	if !strings.Contains(string(out), "HK & TW <主力>") {
		t.Errorf("节点名被转义了（应 SetEscapeHTML(false)）：\n%s", out)
	}
	if strings.Contains(string(out), "\\u00") {
		t.Errorf("sing-box 输出含 \\u00XX 转义：\n%s", out)
	}
}

// ---- base64 分享链接 ----

func TestRenderBase64Links(t *testing.T) {
	out, err := Render(FormatBase64, Document{Proxies: sampleProxies()})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// 契约要求：整体标准 base64、带填充、无换行。
	if strings.ContainsAny(string(out), "\n\r") {
		t.Error("base64 正文含换行 —— 契约要求整块无换行")
	}
	raw, err := base64.StdEncoding.DecodeString(string(out))
	if err != nil {
		t.Fatalf("不是标准 base64: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) != 3 {
		t.Fatalf("解码后行数 = %d，期望 3\n%s", len(lines), raw)
	}

	if !strings.HasPrefix(lines[0], "vless://8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92@203.0.113.10:443?") {
		t.Errorf("vless 链接前缀不对: %s", lines[0])
	}
	for _, want := range []string{
		"encryption=none", "security=reality", "sni=www.cloudflare.com",
		"fp=chrome", "pbk=7Xk1publickey", "sid=6ba85179e30d4fc2",
		"type=tcp", "flow=xtls-rprx-vision",
		// 契约示例里的名字编码：空格是 %20（不是 +），`·` 是 %C2%B7。
		"#HK-1%20%C2%B7%20REALITY",
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("vless 链接缺 %q: %s", want, lines[0])
		}
	}

	if !strings.HasPrefix(lines[1], "hysteria2://") {
		t.Errorf("hysteria2 链接前缀不对: %s", lines[1])
	}
	for _, want := range []string{"sni=hk1.example.invalid", "obfs=salamander", "obfs-password=Jc7obfs"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("hysteria2 链接缺 %q: %s", want, lines[1])
		}
	}

	// SIP002：userinfo 是 base64url **无填充**，带 `=` 会让部分客户端解析失败。
	if !strings.HasPrefix(lines[2], "ss://") {
		t.Errorf("ss 链接前缀不对: %s", lines[2])
	}
	userinfo := strings.TrimPrefix(strings.SplitN(lines[2], "@", 2)[0], "ss://")
	if strings.Contains(userinfo, "=") {
		t.Errorf("ss:// 的 userinfo 带了填充: %s", userinfo)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(userinfo)
	if err != nil {
		t.Fatalf("ss userinfo 不是 base64url: %v", err)
	}
	if string(decoded) != "2022-blake3-aes-128-gcm:8f3a2c1d-4b5e-4c7a-9d21-6e0f3a7b1c92" {
		t.Errorf("ss userinfo 解码 = %q", decoded)
	}
}

func TestHostPortBracketsIPv6(t *testing.T) {
	// 少了方括号的 IPv6 链接在每个客户端里都是解析失败。
	if got := hostPort("2001:db8::1", 443); got != "[2001:db8::1]:443" {
		t.Errorf("hostPort IPv6 = %q", got)
	}
	if got := hostPort("[2001:db8::1]", 443); got != "[2001:db8::1]:443" {
		t.Errorf("hostPort 已带方括号 = %q", got)
	}
	if got := hostPort("203.0.113.1", 443); got != "203.0.113.1:443" {
		t.Errorf("hostPort IPv4 = %q", got)
	}
}

func TestQuerySkipsEmptyValues(t *testing.T) {
	// 下发 `sni=` 这种空参数，部分客户端会把空串当成「显式指定了空 SNI」。
	q := newQuery()
	q.add("sni", "")
	q.add("fp", "chrome")
	if got := q.encode(); got != "fp=chrome" {
		t.Errorf("query.encode() = %q", got)
	}
}

// ---- 伪节点 ----

func TestNoticeProxyRendersInAllThreeFormats(t *testing.T) {
	// 这是「订阅 URL 本身就是通知通道」的落地点：三种格式都必须能把这句话
	// 显示出来，而且节点必须**连不上**（127.0.0.1:1）。
	const name = "⚠️ 账号已停用 · 请提交工单 web.babel.plus"
	doc := Document{Proxies: []Proxy{NoticeProxy(name)}}

	clash, err := Render(FormatClash, doc)
	if err != nil {
		t.Fatalf("clash: %v", err)
	}
	if !strings.Contains(string(clash), name) || !strings.Contains(string(clash), `server: "127.0.0.1"`) {
		t.Errorf("Clash 伪节点不对:\n%s", clash)
	}
	// 伪节点必须同时进两个分组，否则 fallback 组会是空数组 —— 部分客户端同样拒绝加载。
	if strings.Count(string(clash), name) < 3 {
		t.Errorf("伪节点名应出现在 proxies + 两个分组里，实际出现 %d 次", strings.Count(string(clash), name))
	}

	sb, err := Render(FormatSingbox, doc)
	if err != nil {
		t.Fatalf("singbox: %v", err)
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(sb, &cfg); err != nil {
		t.Fatalf("singbox 产出不是合法 JSON: %v", err)
	}
	if len(cfg.Outbounds) != 2 || cfg.Outbounds[1]["server"] != "127.0.0.1" {
		t.Errorf("sing-box 伪节点不对: %v", cfg.Outbounds)
	}

	b64, err := Render(FormatBase64, doc)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(string(b64))
	if err != nil {
		t.Fatalf("base64 解码: %v", err)
	}
	if !strings.HasPrefix(string(raw), "ss://") || !strings.Contains(string(raw), "@127.0.0.1:1#") {
		t.Errorf("base64 伪节点不对: %s", raw)
	}
}

// ---- 名字处理 ----

func TestDedupeNames(t *testing.T) {
	// mihomo 与 sing-box 都用名字当主键（分组引用全靠它），重名会让配置
	// 加载失败或静默丢节点，而节点名是运营手填的。
	in := []Proxy{{Name: "HK"}, {Name: "HK"}, {Name: "TW"}, {Name: "HK"}}
	out := dedupeNames(in)
	got := []string{out[0].Name, out[1].Name, out[2].Name, out[3].Name}
	want := []string{"HK", "HK #2", "TW", "HK #3"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dedupeNames[%d] = %q，期望 %q", i, got[i], want[i])
		}
	}
}

func TestDedupeNamesHandlesCollidingSuffix(t *testing.T) {
	// 补后缀本身也可能撞名：["HK", "HK #2", "HK"] 里第三个不能也叫 "HK #2"。
	in := []Proxy{{Name: "HK"}, {Name: "HK #2"}, {Name: "HK"}}
	out := dedupeNames(in)
	seen := map[string]bool{}
	for _, p := range out {
		if seen[p.Name] {
			t.Fatalf("去重后仍有重名 %q: %v", p.Name, out)
		}
		seen[p.Name] = true
	}
}

func TestSanitizeNameStripsControlChars(t *testing.T) {
	// 换行会把 YAML 的一行拆成两行、把 base64 解码后的一条链接拆成两条。
	if got := sanitizeName("HK\n1\tTW\x00"); got != "HK 1 TW" {
		t.Errorf("sanitizeName = %q", got)
	}
}

func TestGroupMembersPutsHysteria2FirstInBoost(t *testing.T) {
	// 「加速」组对应教程里「慢的时候切到 HY2 试试」，所以 HY2 要排在最前。
	// 同类之间必须保持 sort_order 的相对顺序（稳定排序）。
	proxies := []Proxy{
		{Name: "A", Kind: KindVLESSReality},
		{Name: "B", Kind: KindHysteria2},
		{Name: "C", Kind: KindVLESSReality},
		{Name: "D", Kind: KindHysteria2},
	}
	def, boost := groupMembers(proxies)
	if strings.Join(def, ",") != "A,B,C,D" {
		t.Errorf("默认组 = %v，应保持原顺序", def)
	}
	if strings.Join(boost, ",") != "B,D,A,C" {
		t.Errorf("加速组 = %v，期望 HY2 优先且同类保序", boost)
	}
}
