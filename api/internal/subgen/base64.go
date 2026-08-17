package subgen

import (
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
)

// base64 分享链接渲染。事实源：api-contract.md §4.5「base64 分享链接」。
//
// 正文是若干行分享链接的**整体** base64（标准 base64、带填充、无换行）。
// 这是 v2rayN / v2rayNG / Shadowrocket 生态的事实标准，也是所有未识别 UA 的兜底格式。
//
// 三处刻意的实现选择：
//
//  1. **query 手工按契约顺序拼，不用 url.Values.Encode()。**
//     后者按键名字典序输出，产出与 api-contract §4.5 的示例逐字不同。
//     顺序对客户端无所谓，但对「人工把产出和文档对一遍」这个已被 ADR 0006 §12
//     定为强制步骤的动作，顺序不同就等于每次都要重新比对一遍。
//  2. **片段（`#` 之后的节点名）用 url.PathEscape。**
//     它把空格编码成 %20 而不是 `+`，与契约示例 `#HK-1%20%C2%B7%20REALITY` 一致。
//     用 QueryEscape 会输出 `+`，部分客户端会把它原样显示成加号。
//  3. **不下发 Hysteria2 的带宽参数。** 分享链接里根本没有 up/down 的位置，
//     这一点与 ADR 0004 §裁决 1（用 BBR 不用 Brutal）天然一致。
//
// ⚠️ 分享链接格式没有权威规范，各客户端实现有出入 —— 与 UA 表同属 **需实测** 范畴。
// 本文件按 api-contract §4.5 的示例逐字段实现，没有自创任何参数。

func renderBase64(doc Document) ([]byte, error) {
	proxies := prepare(doc)
	lines := make([]string, 0, len(proxies))
	for _, p := range proxies {
		if link := shareLink(p); link != "" {
			lines = append(lines, link)
		}
	}
	body := strings.Join(lines, "\n")
	// 标准 base64、带填充（StdEncoding），一整块无换行 —— 契约明写。
	return []byte(base64.StdEncoding.EncodeToString([]byte(body))), nil
}

// shareLink 生成单条分享链接。未知 Kind 返回空串（调用方跳过）。
func shareLink(p Proxy) string {
	host := hostPort(p.Server, p.Port)

	switch p.Kind {
	case KindVLESSReality:
		// vless://{uuid}@{host}:{port}?…#{name}
		q := newQuery()
		q.add("encryption", "none")
		q.add("security", "reality")
		q.add("sni", p.SNI)
		q.add("fp", p.Fingerprint)
		q.add("pbk", p.PublicKey)
		q.add("sid", p.ShortID)
		q.add("type", "tcp")
		q.add("flow", p.Flow)
		// 分享链接里没有 mux 的标准位置：v2rayN / Shadowrocket 的 mux 是
		// **客户端全局设置**，不是 per-node 参数。所以 ADR 0004 的「TCP 路径开 mux」
		// 在这条路径上下发不了 —— 只有 Clash 与 sing-box 两种格式能表达它。
		// 这是 base64 格式的固有损失，教程里要写清楚（TODO(P2)：tutorials-spec 补一句）。
		return "vless://" + url.PathEscape(p.UUID) + "@" + host + "?" + q.encode() + "#" + url.PathEscape(p.Name)

	case KindHysteria2:
		q := newQuery()
		q.add("sni", p.SNI)
		if p.ObfsType != "" {
			q.add("obfs", p.ObfsType)
			q.add("obfs-password", p.ObfsPassword)
		}
		return "hysteria2://" + url.PathEscape(p.Password) + "@" + host + "?" + q.encode() + "#" + url.PathEscape(p.Name)

	case KindShadowsocks2022, KindNotice:
		// SIP002：ss://{base64url(method:password)}@{host}:{port}#{name}
		// userinfo 用 **RawURLEncoding**（无填充）：SIP002 明写不带 `=`，
		// 带填充的话部分客户端会把 `=` 当成 userinfo 分隔符而解析失败。
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(p.Method + ":" + p.Password))
		return "ss://" + userinfo + "@" + host + "#" + url.PathEscape(p.Name)
	}
	return ""
}

// hostPort 拼 host:port，IPv6 字面量补方括号。
// 少了方括号的 IPv6 链接在每个客户端里都是解析失败，而节点用 IPv6 地址
// 在 ADR 0008 改用 Standard 网络层级之后是完全可能的。
func hostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host + ":" + strconv.Itoa(port)
}

// query 是一个保序的 query 拼接器。
type query struct {
	parts []string
}

func newQuery() *query { return &query{} }

// add 追加一个键值对。值为空则整个跳过 —— 下发 `sni=` 这种空参数，
// 部分客户端会把空串当成「显式指定了空 SNI」而不是「未指定」。
func (q *query) add(k, v string) {
	if v == "" {
		return
	}
	q.parts = append(q.parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
}

func (q *query) encode() string { return strings.Join(q.parts, "&") }
