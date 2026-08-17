package subgen

import (
	"bytes"
	"encoding/json"
)

// sing-box JSON 渲染。事实源：api-contract.md §4.5「sing-box JSON」。
//
// 结构用具名结构体而不是 map[string]any：map 会被 encoding/json 按键名排序，
// 产出与 api-contract 的示例对不上，人工核对（ADR 0006 §12）就没法做。
// 结构体按声明顺序输出，键序稳定。
//
// > ADR 0006 §5.1 记了一个加分做法：Go 侧直接 import sing-box 的配置结构体
// > 反序列化我们的输出，让「生成了 sing-box 不认的 JSON」在测试里就暴露。
// > **本轮没有做**：sing-box 的模块依赖树很大（含 quic-go / gVisor 等），
// > 为一个校验拉进来会显著拖慢构建并把我们绑在它的版本上。
// > TODO(P2)：改成 CI 里跑 `sing-box check` 的容器步骤 —— 同样的收益，不进 go.mod。

type sbConfig struct {
	Log       sbLog `json:"log"`
	Outbounds []any `json:"outbounds"`
}

type sbLog struct {
	Level string `json:"level"`
}

type sbSelector struct {
	Type      string   `json:"type"`
	Tag       string   `json:"tag"`
	Outbounds []string `json:"outbounds"`
	Default   string   `json:"default"`
}

type sbTLS struct {
	Enabled    bool       `json:"enabled"`
	ServerName string     `json:"server_name,omitempty"`
	UTLS       *sbUTLS    `json:"utls,omitempty"`
	Reality    *sbReality `json:"reality,omitempty"`
}

type sbUTLS struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint"`
}

type sbReality struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
}

type sbMultiplex struct {
	Enabled        bool   `json:"enabled"`
	Protocol       string `json:"protocol"`
	MaxConnections int    `json:"max_connections"`
	MinStreams     int    `json:"min_streams"`
}

type sbVLESS struct {
	Type       string       `json:"type"`
	Tag        string       `json:"tag"`
	Server     string       `json:"server"`
	ServerPort int          `json:"server_port"`
	UUID       string       `json:"uuid"`
	Flow       string       `json:"flow,omitempty"`
	TLS        sbTLS        `json:"tls"`
	Multiplex  *sbMultiplex `json:"multiplex,omitempty"`
}

type sbObfs struct {
	Type     string `json:"type"`
	Password string `json:"password"`
}

type sbHysteria2 struct {
	Type       string  `json:"type"`
	Tag        string  `json:"tag"`
	Server     string  `json:"server"`
	ServerPort int     `json:"server_port"`
	Password   string  `json:"password"`
	Obfs       *sbObfs `json:"obfs,omitempty"`
	TLS        sbTLS   `json:"tls"`
	// 🔴 **没有 up_mbps / down_mbps，这是刻意的。**
	// sing-box 文档：留空则改用 BBR，显式填写才启用 Hysteria 自有的 Brutal。
	// ADR 0004 §裁决 1 已裁定用 BBR（Brutal 在丢包时提高发送速率，特征 100% 可分），
	// 所以这两个键**整个省略**。省略与填 0 对 sing-box 等价，
	// 但省略更不容易在后续改动里被「顺手补个真实带宽」。
}

type sbShadowsocks struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Method     string `json:"method"`
	Password   string `json:"password"`
}

func renderSingbox(doc Document) ([]byte, error) {
	proxies := prepare(doc)
	defaults, _ := groupMembers(proxies)

	outbounds := make([]any, 0, len(proxies)+1)
	// 选择器排第一位，与 api-contract §4.5 的示例一致。
	// sing-box 没有 mihomo 的 fallback 类型（urltest 是按延迟选，
	// 而 system-design §3.1 的实测结论正是「按延迟选会稳定选错」），
	// 所以这里用 selector + default 指向第一个节点 —— 语义等价于
	// 「默认用运营排在最前的那个，用户可以自己换」。
	outbounds = append(outbounds, sbSelector{
		Type:      "selector",
		Tag:       GroupDefault,
		Outbounds: defaults,
		Default:   defaults[0],
	})

	for _, p := range proxies {
		switch p.Kind {
		case KindVLESSReality:
			ob := sbVLESS{
				Type:       "vless",
				Tag:        p.Name,
				Server:     p.Server,
				ServerPort: p.Port,
				UUID:       p.UUID,
				Flow:       p.Flow,
				TLS: sbTLS{
					Enabled:    true,
					ServerName: p.SNI,
					UTLS:       &sbUTLS{Enabled: true, Fingerprint: p.Fingerprint},
					Reality:    &sbReality{Enabled: true, PublicKey: p.PublicKey, ShortID: p.ShortID},
				},
			}
			if p.Mux {
				// TCP 路径开 mux（ADR 0004 §裁决 2）。
				ob.Multiplex = &sbMultiplex{
					Enabled: true, Protocol: "h2mux", MaxConnections: 4, MinStreams: 4,
				}
			}
			outbounds = append(outbounds, ob)

		case KindHysteria2:
			ob := sbHysteria2{
				Type:       "hysteria2",
				Tag:        p.Name,
				Server:     p.Server,
				ServerPort: p.Port,
				Password:   p.Password,
				// tls 对 hysteria2 是**必填项**（sing-box v1.13 文档已核实），
				// 漏了它配置直接加载失败。
				TLS: sbTLS{Enabled: true, ServerName: p.SNI},
			}
			if p.ObfsType != "" {
				ob.Obfs = &sbObfs{Type: p.ObfsType, Password: p.ObfsPassword}
			}
			outbounds = append(outbounds, ob)

		case KindShadowsocks2022, KindNotice:
			outbounds = append(outbounds, sbShadowsocks{
				Type:       "shadowsocks",
				Tag:        p.Name,
				Server:     p.Server,
				ServerPort: p.Port,
				Method:     p.Method,
				Password:   p.Password,
			})
		}
	}

	cfg := sbConfig{Log: sbLog{Level: "warn"}, Outbounds: outbounds}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// SetEscapeHTML(false) 是必须的：默认会把 `<` `>` `&` 转成 < 之类。
	// 节点名是运营手填的，出现 `&`（"HK & TW"）完全正常，
	// 转义后客户端显示的是一串 &，用户看到的是乱码。
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
