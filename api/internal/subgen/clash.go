package subgen

// Clash / mihomo YAML 渲染。事实源：api-contract.md §4.5「Clash / mihomo YAML」。
//
// ⚠️ 本文件里有三处 api-contract 明确标了 **需实测** 的字段名，每处都有 `需实测` 注释：
//   1. vless 的 `smux` 块字段名
//   2. `reality-opts` 的键名（`public-key` / `short-id`）
//   3. hysteria2 的 `obfs-password` 拼写
// 「需实测」不是「大概是对的」：这三处任意一处写错，对应节点会在客户端里
// 静默失效（能导入、能显示、连不上），而不是报一个能被搜索的错误。
// 验证方法是 ADR 0006 §12 的人工步骤，不是这里的单测。

// renderClash 生成 Clash/mihomo 配置。
func renderClash(doc Document) ([]byte, error) {
	proxies := prepare(doc)
	var w yamlWriter

	// 顶层运行参数照抄 api-contract §4.5 的示例。
	// external-controller 绑 127.0.0.1 而不是 0.0.0.0：这是客户端本机的
	// 管理接口，绑到全网等于把用户机器上的代理控制面开给局域网。
	w.line(0, "port: 7890")
	w.line(0, "socks-port: 7891")
	w.line(0, "allow-lan: false")
	w.line(0, "mode: rule")
	w.line(0, "log-level: info")
	w.line(0, "external-controller: 127.0.0.1:9090")
	w.blank()

	w.line(0, "proxies:")
	for _, p := range proxies {
		writeClashProxy(&w, p)
	}
	w.blank()

	defaults, boost := groupMembers(proxies)
	w.line(0, "proxy-groups:")
	// 默认组是 fallback 不是 url-test（system-design §3.1 的实测结论：
	// 各健康节点延迟同在 100–250 ms 噪声带内、吞吐差 4–5 倍，url-test 会稳定选错）。
	w.line(1, "- name: %s", yamlString(GroupDefault))
	w.line(2, "type: fallback")
	w.line(2, "proxies: %s", yamlStringList(defaults))
	w.line(2, "url: %s", healthCheckURL)
	w.line(2, "interval: %d", healthCheckInterval)
	// 加速组永远输出，即使当前没有 Hysteria2 节点 ——
	// 分组名是客户端里用户选择的持久化键，时有时无会让用户的选择丢失。
	w.line(1, "- name: %s", yamlString(GroupBoost))
	w.line(2, "type: select")
	w.line(2, "proxies: %s", yamlStringList(boost))
	w.blank()

	writeClashRules(&w)

	return w.bytes(), nil
}

// writeClashRules 输出分流规则表。
//
// 🔴 **这一段曾经整个不存在，而上面写着 `mode: rule`。**
// mihomo 在 rule 模式下逐条匹配 `rules`，一条都匹配不上时回落到 `DIRECT` ——
// 规则表为空 = 每一条连接都匹配不上 = **全部直连**。
// 用户导入后会看到节点全在、延迟测得出来、订阅流量条正常，
// 但被墙的站点一个都打不开，且他必然把这个报成「节点坏了」。
//
// 现在的表只有两类，都**不依赖任何需要下载的数据**：
//
//  1. 私有网段直连。带 `no-resolve`：这些是本机与局域网地址，走代理没有意义，
//     而 `no-resolve` 避免为了判断一条 IP 规则先去做 DNS 解析。
//  2. `MATCH` 兜底到默认组。
//
// 🔴 **刻意没有 `GEOIP,CN,DIRECT`，尽管产品上想要它。** 这一条是实测改的，不是保守：
//
//	mihomo v1.19.30 · 全新配置目录 · 断网
//	  带 GEOIP,CN → can't download MMDB …… configuration file test **failed**
//	  不带        → Initial configuration complete, 7ms, test **is successful**
//
// 也就是说 `GEOIP,CN` 拿不到 GeoIP 数据库时**不是「这条规则不匹配」，
// 而是整份配置被拒绝加载**。而那个数据库是 8.6 MB、来源
// `github.com/MetaCubeX/meta-rules-dat/releases` —— 需要它的人恰恰是
// 「人在大陆、刚装完客户端、还没有任何可用代理」的那一刻。
// 缓存过一次之后离线没问题（实测 B2），所以风险**特指首次加载**。
//
// 两种失败方向的代价不对称：
//
//	· 没有 GEOIP → 国内流量也走节点，慢一些、出口账单贵一些（**产品能用**）
//	· 有 GEOIP 但下不到 → **整份订阅加载不了，产品完全不能用**
//
// 首次加载必须选前者。
//
// 想拿回国内直连（tutorials-spec 的排障表假定了它的存在，出口成本也在等它），
// 前置条件是先回答「首推客户端是否自带 geoip.metadb」——
// 桌面版 Clash Verge Rev 很可能自带，但**本仓没有任何一手数据**。
// 登记为 roadmap B46。
//
// 实测原始输出：docs/evidence/client-config-validation-20260822/
func writeClashRules(w *yamlWriter) {
	w.line(0, "rules:")
	for _, r := range []string{
		"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
		"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
		"IP-CIDR,169.254.0.0/16,DIRECT,no-resolve",
		"IP-CIDR6,::1/128,DIRECT,no-resolve",
		"IP-CIDR6,fc00::/7,DIRECT,no-resolve",
		"IP-CIDR6,fe80::/10,DIRECT,no-resolve",
	} {
		w.line(1, "- %s", r)
	}
	// MATCH 的目标是默认组的名字，必须与 proxy-groups 里那个 name 逐字一致。
	// 用同一个常量拼出来，避免哪天改组名时这里被漏掉。
	w.line(1, "- MATCH,%s", GroupDefault)
}

func writeClashProxy(w *yamlWriter, p Proxy) {
	w.line(1, "- name: %s", yamlString(p.Name))

	switch p.Kind {
	case KindVLESSReality:
		w.line(2, "type: vless")
		w.line(2, "server: %s", yamlString(p.Server))
		w.line(2, "port: %d", p.Port)
		w.line(2, "uuid: %s", yamlString(p.UUID))
		w.line(2, "network: tcp")
		w.line(2, "tls: true")
		// udp 必须显式写 true：mihomo 对多数协议默认 udp: false，
		// 漏了它的现象是「网页能开但游戏/语音不通」。
		w.line(2, "udp: true")
		w.line(2, "flow: %s", yamlString(p.Flow))
		w.line(2, "servername: %s", yamlString(p.SNI))
		// client-fingerprint 是 **uTLS 指纹**；同名相近的 `fingerprint` 是
		// 证书 SHA-256 pin，是完全不同的东西。写错会导致极难排查的连接失败。
		w.line(2, "client-fingerprint: %s", yamlString(p.Fingerprint))
		// ⚠️ 需实测：reality-opts 的键名（api-contract §4.5）。
		w.line(2, "reality-opts:")
		w.line(3, "public-key: %s", yamlString(p.PublicKey))
		w.line(3, "short-id: %s", yamlString(p.ShortID))
		if p.Mux {
			// ⚠️ 需实测：smux 块的字段名（api-contract §4.5）。
			// TCP 路径开 mux 是 ADR 0004 §裁决 2：抗 TLS-in-TLS 指纹优先于吞吐。
			w.line(2, "smux:")
			w.line(3, "enabled: true")
			w.line(3, "protocol: h2mux")
			w.line(3, "max-connections: 4")
			w.line(3, "min-streams: 4")
		}

	case KindHysteria2:
		w.line(2, "type: hysteria2")
		w.line(2, "server: %s", yamlString(p.Server))
		w.line(2, "port: %d", p.Port)
		w.line(2, "password: %s", yamlString(p.Password))
		if p.ObfsType != "" {
			w.line(2, "obfs: %s", yamlString(p.ObfsType))
			// ⚠️ 需实测：obfs-password 的拼写（api-contract §4.5）。
			w.line(2, "obfs-password: %s", yamlString(p.ObfsPassword))
		}
		// sni 空时整行省略：下发 `sni: ""` 会让 mihomo 把空串当成「显式指定了空 SNI」
		// 而不是「未指定」，握手时送出一个空 SNI —— 那比不写更糟。
		if p.SNI != "" {
			w.line(2, "sni: %s", yamlString(p.SNI))
		}
		w.line(2, "udp: true")
		// 🔴 **刻意不输出 up / down。** 它们是 Brutal 拥塞控制的带宽声明，
		// 填了就等于启用 Brutal —— ADR 0004 §裁决 1 已裁定用 BBR，
		// 理由是 Brutal 在丢包时提高发送速率，特征 100% 可分。
		// 这里少两行的代价是放弃 55% 吞吐，是**有意付出的**，不要「顺手补上」。
		// UDP 路径同样**不开 mux**（ADR 0004 §裁决 2）：QUIC 原生多路复用，
		// 且 Hysteria2 的价值就是单流吞吐。

	case KindShadowsocks2022:
		w.line(2, "type: ss")
		w.line(2, "server: %s", yamlString(p.Server))
		w.line(2, "port: %d", p.Port)
		w.line(2, "cipher: %s", yamlString(p.Method))
		w.line(2, "password: %s", yamlString(p.Password))
		w.line(2, "udp: true")

	case KindNotice:
		// 伪节点：语法合法、必定连不上。它的唯一职责是让名字能显示出来。
		w.line(2, "type: ss")
		w.line(2, "server: %s", yamlString(p.Server))
		w.line(2, "port: %d", p.Port)
		w.line(2, "cipher: %s", yamlString(p.Method))
		w.line(2, "password: %s", yamlString(p.Password))
		w.line(2, "udp: false")
	}
}
