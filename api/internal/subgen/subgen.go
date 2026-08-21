// Package subgen 把节点列表渲染成客户端能吃的三种订阅格式：
// Clash/mihomo YAML、sing-box JSON、base64 分享链接。
//
// 为什么单独成包：这是**与客户端生态的硬接口**（api-contract.md §4.5），
// 字段名不能自创、渲染结果必须能被真实客户端加载。把它从 handler 里拆出来，
// 是为了让「渲染」可以在没有数据库、没有 HTTP 的前提下被逐字节断言 ——
// ADR 0006 §12 已把「每次改订阅格式，人工用 Clash Verge Rev / sing-box 各加载一次」
// 定为不可自动化的人工步骤，单测能做的是把人工验证的输入固定下来。
//
// # 三条来自 ADR 0004 的协议裁决，本包必须落实
//
//  1. **Hysteria2 用 BBR 不用 Brutal。** 因此**不下发** up/down 带宽声明 ——
//     声明带宽就是启用 Brutal，而 Brutal 有 100% 可分的特征（ADR 0004 §裁决 1）。
//     sing-box 的 up_mbps/down_mbps 与 mihomo 的 up/down 都**整个键省略**：
//     对 sing-box 而言「缺省」与「0」在 Go 反序列化后完全等价，省略更不容易被误改成非 0。
//     （节点面 /config 那侧是另一回事：它必须**显式**下发 0，见 api-contract §3.3。）
//  2. **TCP 路径（REALITY）启用 mux，UDP 路径（Hysteria2）不启用。**
//     抗 TLS-in-TLS 指纹 > 吞吐（ADR 0004 §裁决 2）。
//  3. **REALITY 是默认主力**，Hysteria2 是加速通路，SS-2022 是兜底。
//
// # 需实测（不要当成已核实）
//
//   - mihomo 的 `smux` 块字段名、`reality-opts` 的键名、hysteria2 的 `obfs-password`
//     拼写：api-contract.md §4.5 明确标了「需实测」。本包按文档实现，
//     每个这样的字段在渲染处都有 `⚠️ 需实测` 注释。
//   - 各客户端对节点名里 emoji / 空格 / `·` 的渲染差异（api-contract §4.6）。
package subgen

import (
	"fmt"
	"sort"
	"strings"
)

// Format 是三种下发格式。取值与 subscription_fetch_log.format 列、
// 以及 openapi 的 `?flag=` 枚举**逐字一致**，不要在这里另起名字。
type Format string

const (
	FormatClash   Format = "clash"
	FormatSingbox Format = "singbox"
	FormatBase64  Format = "base64"
)

// ContentType 返回该格式的 Content-Type（含 charset）。
//
// charset 是必须的：节点名里有中文与 emoji，少了 charset 会让部分客户端
// 按 latin-1 解码，表现为节点名乱码而不是报错 —— 属于最难被用户描述清楚的一类故障。
func (f Format) ContentType() string {
	switch f {
	case FormatClash:
		return "text/yaml; charset=utf-8"
	case FormatSingbox:
		return "application/json; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}

// Kind 是节点的协议种类。与 db 的 server_protocol 枚举对应，
// 但**刻意不复用**那个类型：subgen 不该依赖数据库生成代码，
// 否则这个包就没法在没有 sqlc 的前提下被测试。
type Kind string

const (
	// KindVLESSReality 是 VLESS + XTLS-Vision + REALITY，TCP:443，默认主力。
	KindVLESSReality Kind = "vless_reality"
	// KindHysteria2 是 Hysteria2 + salamander obfs，UDP:443，加速通路。
	KindHysteria2 Kind = "hysteria2"
	// KindShadowsocks2022 是 2022-blake3-aes-128-gcm，兜底。
	KindShadowsocks2022 Kind = "shadowsocks2022"
	// KindNotice 是**伪节点**：语法合法但连不上的条目，用节点名向用户传达状态。
	//
	// 为什么必须存在（api-contract §4.6 / user-journey §11.2）：订阅 URL 本身
	// 是「邮箱收不到、Telegram 连不上、主站被封时仍然能触达用户」的唯一通道。
	// 而且**不能只是把 proxies 留空** —— 部分客户端会因为「proxies 为空」
	// 拒绝导入整份配置，用户连这句话都看不到。
	KindNotice Kind = "notice"
)

// Proxy 是协议无关的下发节点模型。
//
// 用一个带 Kind 判别式的扁平结构体而不是 interface：三种格式的渲染器都要对
// 全部字段做一次遍历，接口化只会让「某个协议在某个格式里漏了字段」变得看不见。
type Proxy struct {
	// Name 是客户端里显示的节点名。可含中文 / emoji / `·`。
	Name string
	Kind Kind

	Server string
	Port   int

	// ---- VLESS / REALITY ----
	UUID        string // 用户 uuid
	Flow        string // 固定 xtls-rprx-vision
	SNI         string // REALITY 的伪装域名（servername / server_name）
	Fingerprint string // uTLS 指纹：chrome / firefox / …（**不是**证书 pin）
	PublicKey   string // REALITY x25519 公钥（私钥只下发给节点，绝不进订阅）
	ShortID     string // REALITY short id，偶数长度 hex
	Mux         bool   // TCP 路径 true（ADR 0004 §裁决 2）

	// ---- Hysteria2 ----
	// Password 对 Hysteria2 是用户 uuid（XrayR / v2node 系的约定，
	// api-contract §3.4 的字段表：「SS-2022 侧 XrayR 系直接把它当 password 用」，
	// Hysteria2 同源同理）。
	Password     string
	ObfsType     string // salamander（1.13 稳定版只认这一个）
	ObfsPassword string

	// ---- Shadowsocks-2022 ----
	Method string // 2022-blake3-aes-128-gcm
}

// Document 是一次订阅下发的全部内容。
type Document struct {
	// Proxies 按下发顺序排列。**第一位可能是域名广播位**
	// （user-journey：第一个节点名保留给域名广播），本包不做特殊处理 ——
	// 广播位就是一个普通节点，顺序由 servers.sort_order 决定。
	Proxies []Proxy
}

// 分组名。两个组的语义来自 system-design §3.1 的实测结论：
// 各健康节点的延迟同在 100–250 ms 噪声带内，吞吐却差 4–5 倍，
// 所以默认组用 `fallback`（按顺序取第一个活的）而不是 `url-test`（按延迟选，会稳定选错）。
// 另给一个 select 组「加速」，对应教程里「慢的时候切到 HY2 试试」。
const (
	GroupDefault = "默认"
	GroupBoost   = "加速"
)

// healthCheckURL 是 fallback 组的探活地址。用 gstatic 的 204 端点：
// 响应体为空、被墙的概率低、且它是 Clash 生态的事实默认值。
const healthCheckURL = "http://www.gstatic.com/generate_204"

// healthCheckInterval 是探活间隔（秒）。
const healthCheckInterval = 300

// ErrEmptyDocument 表示调用方传了一份没有任何节点的文档。
//
// 这**不是**「用户到期/超配额」的情形 —— 那种情况调用方必须放一个 KindNotice 伪节点进来
// （见 NoticeProxy）。真的渲染出空 proxies 会让部分客户端拒绝导入整份配置，
// 所以在这里硬拦一道，把「忘了放伪节点」变成一个响亮的错误而不是一份静默失效的订阅。
var ErrEmptyDocument = fmt.Errorf("subgen: 节点列表为空（到期/超配额/封禁必须放一个伪节点，不能真的下发空列表）")

// Render 按格式渲染整份订阅。
func Render(f Format, doc Document) ([]byte, error) {
	if len(doc.Proxies) == 0 {
		return nil, ErrEmptyDocument
	}
	switch f {
	case FormatClash:
		return renderClash(doc)
	case FormatSingbox:
		return renderSingbox(doc)
	case FormatBase64:
		return renderBase64(doc)
	default:
		return nil, fmt.Errorf("subgen: 未知格式 %q", f)
	}
}

// NoticeProxy 构造一个伪节点。
//
// 指向 127.0.0.1:1 —— 语法合法、连接必定失败、且**不会**把用户的流量
// 打到任何真实主机上（用一个真实地址当占位符是这一类实现最常见的错误）。
//
// 协议选 Shadowsocks（旧版 AEAD，不是 2022）：它是三种格式里兼容面最宽的一个，
// mihomo / sing-box / v2rayN / Shadowrocket 全部认识。伪节点的唯一职责是
// **能被解析并显示出名字**，所以这里要的是兼容性，不是安全性。
func NoticeProxy(name string) Proxy {
	return Proxy{
		Name:     name,
		Kind:     KindNotice,
		Server:   "127.0.0.1",
		Port:     1,
		Method:   "aes-128-gcm",
		Password: "babelplus",
	}
}

// groupMembers 返回两个分组各自的成员顺序。
//
// 默认组：原顺序（= sort_order，运营可控）。
// 加速组：Hysteria2 优先，其余保持相对顺序 —— 对应 api-contract §4.5 的示例
// `proxies: ["HK-1 · HY2 加速", "HK-1 · REALITY"]`。
//
// 用 sort.SliceStable 而不是「先挑出 HY2 再拼」：稳定排序保证同类节点之间
// 仍然是 sort_order 的顺序，运营调 sort_order 时两个组会一起动。
func groupMembers(proxies []Proxy) (defaults []string, boost []string) {
	defaults = make([]string, 0, len(proxies))
	for _, p := range proxies {
		defaults = append(defaults, p.Name)
	}

	idx := make([]int, len(proxies))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return proxies[idx[a]].Kind == KindHysteria2 && proxies[idx[b]].Kind != KindHysteria2
	})
	boost = make([]string, 0, len(proxies))
	for _, i := range idx {
		boost = append(boost, proxies[i].Name)
	}
	return defaults, boost
}

// dedupeNames 保证节点名在一份订阅里唯一。
//
// 必须做：mihomo 与 sing-box 都用名字/tag 当**主键**（分组引用、策略引用全靠它），
// 重名会让配置加载直接失败或静默丢节点。而节点名是运营在后台手填的，
// 重名是迟早的事 —— 与其等到用户导入失败来开工单，不如在这里补后缀。
//
// 后缀形如 ` #2`：放在末尾、不含会被 URL 编码放大的字符，
// 而且用户一眼能看出「这是同名的第二个」。
func dedupeNames(proxies []Proxy) []Proxy {
	seen := make(map[string]int, len(proxies))
	out := make([]Proxy, len(proxies))
	copy(out, proxies)
	for i := range out {
		name := out[i].Name
		if name == "" {
			name = "node"
		}
		n := seen[name]
		seen[name] = n + 1
		if n > 0 {
			uniq := fmt.Sprintf("%s #%d", name, n+1)
			// 补了后缀的名字自己也可能撞上别的名字，继续往后找。
			for seen[uniq] > 0 {
				n++
				seen[name] = n + 1
				uniq = fmt.Sprintf("%s #%d", name, n+1)
			}
			seen[uniq] = 1
			name = uniq
		}
		out[i].Name = name
	}
	return out
}

// sanitizeName 去掉节点名里会破坏所有三种格式的控制字符。
//
// 换行是唯一真正致命的一个：它会把 YAML 的一行拆成两行、把 base64 解码后的
// 一条分享链接拆成两条。运营从别处粘贴节点名时带进换行完全可能。
func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// prepare 是三个渲染器共用的前处理：清洗名字 → 去重。
func prepare(doc Document) []Proxy {
	out := make([]Proxy, len(doc.Proxies))
	copy(out, doc.Proxies)
	for i := range out {
		out[i].Name = sanitizeName(out[i].Name)
	}
	return dedupeNames(out)
}
