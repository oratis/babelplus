package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---- CORS ----
//
// 为什么必须有这个中间件（docs/04-ops/local-development.md §3.6）：
// 生产形态是「API 与 Web 各用**独立主域名池**」（api-contract.md §2.1），
// 浏览器发出的每一个用户面请求**本来就是跨源的**。没有 CORS 头 = 前端一个接口都调不通。
//
// 三条不能妥协的：
//
//  1. **不硬编码 Origin 列表。** 域名池会被封、会轮换（ADR 0003 §5「一键新增镜像域名」），
//     写死在代码里意味着换个域名要发一次版。列表从配置来（BP_ALLOWED_ORIGINS）。
//
//  2. **精确匹配，不做通配符/前缀匹配。** `*.babel.plus` 这类匹配是 CORS 最常见的漏洞来源：
//     实现上通常退化成 `strings.HasSuffix(origin, ".babel.plus")`，
//     于是 `https://evil-babel.plus` 与 `https://babel.plus.attacker.com` 都能命中。
//     这里只做集合相等判断，**没有任何模式匹配的代码路径可以出错**。
//
//  3. **绝不 `Access-Control-Allow-Origin: *` 配 credentials。** 浏览器会直接拒绝这个组合，
//     而且真让它生效等于任意站点都能带着用户 cookie 调我们的 API。
//     本实现只回显命中白名单的那一个 Origin，永远不会输出 `*`。
//
// 另有一条容易漏的：**必须回 `Vary: Origin`**。响应会经过 CDN / 浏览器缓存 / 反代，
// 不声明 Vary 的话，A 站请求产生的 `Allow-Origin: A` 会被缓存起来喂给 B 站，
// 或者反过来 —— 一个没有 Origin 头的请求的响应（不带 CORS 头）被缓存后，
// 后续真正的跨源请求会拿到没有 CORS 头的副本，表现为「时好时坏的跨域失败」。
// 所以 Vary 是**无条件**加的，不是「命中才加」。

// nodeAPIPathPrefix 是节点面的路径前缀。
//
// 🔴 节点面（/api/v1/server/UniProxy/*）**不经过 CORS**，这是刻意的：
// 那是 v2node 到 API 的服务端到服务端调用，没有浏览器参与，也就没有同源策略可言。
// 给它加 CORS 头不解决任何问题，只是白白告诉扫描器「这个端点欢迎浏览器来试」。
// 更重要的是：节点面凭据走 query token（v2node 唯一形态），
// 一旦它对某个 Origin 可跨源访问，恶意页面就能借用户的网络位置去打节点面。
const nodeAPIPathPrefix = "/api/v1/server/"

// 默认值。三个列表都可被 CORSConfig 覆盖，但默认值就是 P1 需要的全集。
var (
	// 没有 HEAD：chi 对 GET 路由自动处理 HEAD，而 HEAD 本身是 CORS 安全方法，
	// 浏览器不会为它发预检。列出来是无害的冗余，不列更诚实。
	defaultCORSMethods = []string{
		http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete,
	}
	// 逐个对应 api-contract.md §2.7 的通用请求头。
	// Content-Type 虽然是 CORS 安全头，但只在值为三种简单类型时才是 ——
	// `application/json` **不在**其列，所以必须显式允许，否则每个 POST 都预检失败。
	defaultCORSHeaders = []string{
		"Authorization",
		"Content-Type",
		"Idempotency-Key",
		"X-Request-Id",
		"X-TOTP-Code",
		"If-None-Match",
	}
	// 不在 CORS 安全响应头之列的，前端读不到 —— 安全响应头只有
	// Cache-Control / Content-Language / Content-Length / Content-Type / Expires /
	// Last-Modified / Pragma 七个。X-Request-Id 是排障的唯一线索（错误信封只给它），
	// 不暴露的话用户报障时前端拿不出任何可查的东西。
	defaultCORSExposed = []string{
		"X-Request-Id",
		"Retry-After",
		"ETag",
	}
)

// defaultCORSMaxAge 是预检结果的缓存时长。
//
// 10 分钟：Chrome 的上限是 7200 秒，Safari 更短，给大了也会被截断；
// 给小了则每个 JSON POST 都要多一次往返。10 分钟在「域名轮换后旧预检多久失效」
// 与「往返次数」之间取中 —— 域名池切换时我们**希望**旧的预检快点过期。
const defaultCORSMaxAge = 10 * time.Minute

// CORSConfig 是跨源策略。零值不可用：AllowedOrigins 为空 = 拒绝所有跨源请求（fail-closed）。
type CORSConfig struct {
	// AllowedOrigins 是**精确匹配**的 Origin 白名单，形如 https://web.babel.plus。
	// 由 config.Load 从 BP_ALLOWED_ORIGINS 解析并校验过形态，这里只做集合查找。
	AllowedOrigins []string

	// 以下三项留空则用默认值。
	AllowedMethods []string
	AllowedHeaders []string
	ExposedHeaders []string

	// MaxAge <= 0 时用 defaultCORSMaxAge。
	MaxAge time.Duration

	// AllowCredentials 决定是否回 Access-Control-Allow-Credentials: true。
	// 用户面会话走 Authorization 头或 cookie，两者都需要它为 true。
	//
	// ⚠️ 它为 true 时，白名单里的**任何一个** Origin 都能代表已登录用户发请求。
	// 所以 BP_ALLOWED_ORIGINS 里不该出现任何我们不完全控制的域名。
	AllowCredentials bool
}

// CORS 返回跨源中间件。
//
// 挂在全局链上即可 —— 节点面由 nodeAPIPathPrefix 在内部跳过，
// 不需要调用方记得「别把它挂到节点路由上」。少一个必须记住的装配约定，少一个漏洞。
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	// 白名单在构造时定型，请求路径上只有一次 map 查找。
	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		o = strings.ToLower(strings.TrimSpace(o))
		// 防御性丢弃：config 侧已经拒绝这两个值，这里再挡一次 ——
		// 因为 CORSConfig 也可能被测试或将来的调用方直接构造。
		if o == "" || o == "*" || o == "null" {
			continue
		}
		allowed[o] = struct{}{}
	}

	methods := strings.Join(orDefault(cfg.AllowedMethods, defaultCORSMethods), ", ")
	headers := strings.Join(orDefault(cfg.AllowedHeaders, defaultCORSHeaders), ", ")
	exposed := strings.Join(orDefault(cfg.ExposedHeaders, defaultCORSExposed), ", ")

	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = defaultCORSMaxAge
	}
	maxAgeSeconds := strconv.FormatInt(int64(maxAge.Seconds()), 10)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 节点面原样放行，一个 CORS 头都不加。
			if strings.HasPrefix(r.URL.Path, nodeAPIPathPrefix) {
				next.ServeHTTP(w, r)
				return
			}

			h := w.Header()

			// 🔴 无条件加 Vary，与是否命中白名单无关。见文件顶部第 3 段。
			h.Add("Vary", "Origin")

			// 预检的判定条件是两个都成立：方法为 OPTIONS **且**带 Access-Control-Request-Method。
			// 只看 OPTIONS 会把普通的 OPTIONS 探测（如某些健康检查）也短路掉。
			isPreflight := r.Method == http.MethodOptions &&
				r.Header.Get("Access-Control-Request-Method") != ""
			if isPreflight {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
			}

			origin := r.Header.Get("Origin")
			hit := origin != "" && originAllowed(allowed, origin)

			if hit {
				// 回显请求原样的 Origin，不是白名单里那份 ——
				// 浏览器按字节比对 Allow-Origin 与自己的 origin 序列化结果。
				// 这里不存在头注入：只有精确命中白名单（忽略大小写）的值才会被回显，
				// 而白名单项在 config 侧已校验为合法 origin。
				h.Set("Access-Control-Allow-Origin", origin)
				if cfg.AllowCredentials {
					h.Set("Access-Control-Allow-Credentials", "true")
				}
				// 预检响应没有业务响应头可暴露，Expose-Headers 只对实际请求有意义。
				if !isPreflight && exposed != "" {
					h.Set("Access-Control-Expose-Headers", exposed)
				}
			}

			if isPreflight {
				if hit {
					h.Set("Access-Control-Allow-Methods", methods)
					h.Set("Access-Control-Allow-Headers", headers)
					h.Set("Access-Control-Max-Age", maxAgeSeconds)
				}
				// 🔴 无论命中与否都**短路**，不进 handler。
				// 未命中时返回的 204 不带任何 CORS 头，浏览器据此判定预检失败 ——
				// 这正是我们要的结果，而且业务代码永远看不到预检请求。
				// 让预检落到 handler 上则意味着每个 handler 都要自己认得 OPTIONS。
				//
				// 不设 Content-Length：204 按 RFC 7230 不该带它，net/http 自己会处理。
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// originAllowed 做精确（大小写不敏感）匹配。
//
// 大小写不敏感是因为 origin 的 scheme 与 host 本身大小写不敏感（RFC 3986 §3.1/§3.2.2），
// 但**只**在这两处 —— 由于 origin 没有 path，整串小写化是安全的等价变换。
func originAllowed(allowed map[string]struct{}, origin string) bool {
	// 长度上限挡住畸形输入，顺带避免为超长字符串做小写化分配。
	// 253（域名上限）+ scheme + 端口，256 足够宽松。
	if len(origin) > 256 {
		return false
	}
	_, ok := allowed[strings.ToLower(origin)]
	return ok
}

func orDefault(v, def []string) []string {
	if len(v) == 0 {
		return def
	}
	return v
}
