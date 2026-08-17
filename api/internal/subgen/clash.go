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

	return w.bytes(), nil
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
