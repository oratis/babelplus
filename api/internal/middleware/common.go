package middleware

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// ---- 请求 ID ----

// RequestID 为每个请求生成或沿用一个 ID，写进响应头与日志。
// Cloud Run 会注入 X-Cloud-Trace-Context，优先沿用它，这样应用日志能和平台日志对上。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			if tc := r.Header.Get("X-Cloud-Trace-Context"); tc != "" {
				id, _, _ = strings.Cut(tc, "/")
			}
		}
		if id == "" {
			b := make([]byte, 12)
			_, _ = rand.Read(b)
			id = base64.RawURLEncoding.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, id)))
	})
}

// RequestIDFrom 取出请求 ID。
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// ---- 结构化日志 ----

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// AccessLog 输出结构化访问日志。
//
// 字段名对齐 Cloud Logging 的约定，方便直接建 log-based metric。
// ⚠️ 按 monitoring.md 的告警设计，**不要**在这里打 per-user / per-IP 标签 ——
// 自定义指标是按基数计费的，加了会爆。
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			if sw.status == 0 {
				sw.status = http.StatusOK
			}

			lvl := slog.LevelInfo
			switch {
			case sw.status >= 500:
				lvl = slog.LevelError
			case sw.status >= 400:
				lvl = slog.LevelWarn
			}

			logger.LogAttrs(r.Context(), lvl, "http",
				slog.String("method", r.Method),
				// ⚠️ 必须走 RedactPath —— 订阅短链 /s/{token} 把凭据放在**路径**里，
				// 直接记 r.URL.Path 等于把可用的订阅 token 抄进日志。见 RedactPath 的注释。
				slog.String("path", RedactPath(r.URL.Path)),
				slog.Int("status", sw.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.Int("bytes", sw.bytes),
				slog.String("request_id", RequestIDFrom(r.Context())),
			)
		})
	}
}

// ---- panic 恢复 ----

// Recover 把 panic 转成 500，避免一个 handler 的 bug 打死整个实例。
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					logger.ErrorContext(r.Context(), "handler panic",
						"panic", v,
						"path", RedactPath(r.URL.Path),
						"request_id", RequestIDFrom(r.Context()),
						"stack", string(debug.Stack()),
					)
					// 走 WriteError 而不是直接写信封：节点面的 500 同样必须是裸 JSON。
					WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ---- 日志脱敏 ----

// subShortLinkPrefix 是订阅短链的路径前缀。与 openapi 的 `/s/{token}` 对应。
const subShortLinkPrefix = "/s/"

// tokenPrefixLen 是允许留在日志里的明文位数。
//
// 8 不是随手取的：`subscription_tokens.token_prefix` 就是「明文前 8 位」，
// DDL 注释写明它的用途是「面板列表与日志定位」。
// 保持一致意味着日志里的这 8 位可以直接 join 回 token_prefix 列定位到具体那条订阅，
// 而这 8 位本身已经被设计为非秘密。
const tokenPrefixLen = 8

// RedactPath 把路径里的凭据换成「前 8 位 + 省略号」。
//
// 🔴 为什么必须有：订阅短链 `/s/{token}` 把**可直接使用的凭据放在路径里**，
// 而访问日志、panic 日志、请求解析失败日志都记录路径。不脱敏的后果是
// Cloud Logging 里躺着一份可用订阅 token 的明文清单 —— 而日志的读取权限
// 比数据库宽得多（值班、排障、日志导出、log sink 到 BigQuery 都会拿到），
// 数据库里那份反而是 sha256 哈希。等于加密存储被日志绕过。
//
// 节点密钥不受影响：v2node 走 query string，而这里记的是 r.URL.Path，
// query 从来没有被写进日志（这一点已在冒烟里实测过）。
//
// 只处理 `/s/{token}`，不做通配的「像 token 的段一律打码」：
// 后者会把 `/api/v1/orders/{trade_no}` 这类需要在日志里被搜索的业务标识
// 一起打掉，排障时反而查不出来。新增把凭据放进路径的端点时，必须回到这里加分支。
func RedactPath(path string) string {
	rest, ok := strings.CutPrefix(path, subShortLinkPrefix)
	if !ok {
		return path
	}
	// 只截第一段：/s/{token}/anything 的后续段落不是凭据，但也没有保留价值。
	token, _, _ := strings.Cut(rest, "/")
	if len(token) <= tokenPrefixLen {
		// 太短，本来就不是有效 token（形态校验会直接 404）。原样保留，
		// 因为这类请求正是扫描探测，路径本身是有用的取证信息。
		return path
	}
	return subShortLinkPrefix + token[:tokenPrefixLen] + "…"
}

// ---- 请求体上限 ----

// MaxBodyBytes 是全局请求体上限。
//
// 定这个数的依据：现有 18 个 operation 里最大的请求体是
// `PATCH /admin/servers/{id}`（节点配置，含 protocol_settings 的一坨 JSON），
// 量级在 KB。1 MiB 给了三个数量级的余量，同时把 Cloud Run 单请求上限
// （HTTP/1 32 MiB）挡在外面。**将来要传附件（工单截图）时不要抬高这个全局值，
// 而是在那条路由上单独放宽** —— 全局抬高等于把下面这个 DoS 面重新打开。
const MaxBodyBytes int64 = 1 << 20

// LimitBody 给每个请求的 Body 套上 http.MaxBytesReader。
//
// 🔴 **必须挂在全局链上，而且要在生成代码之前生效。**
// oapi-codegen 生成的 strictHandler 在调用中间件链**之前**就先解请求体：
//
//	var body LoginJSONRequestBody
//	json.NewDecoder(r.Body).Decode(&body)
//	for _, middleware := range sh.middlewares { ... }
//
// 也就是说 handler 层的任何检查（包括 auth.go 里 argon2 那套 argon2Slots 并发闸、
// validPassword 的 8–128 字长度校验）**全都在解码之后**。
// 在装上这道闸之前，一个不需要任何凭据的 `POST /api/v1/auth/login`
// 带上几十 MB 的 password 字段就能让 512Mi 的实例 OOM ——
// 而 auth.go 文件头那段「64 MiB × 8 并发 = 512 MiB，不限并发的话 80 个并发登录
// 就能把实例 OOM 掉」的内存核算，算的是 argon2 那块，管不到这条路径。
//
// MaxBytesReader 超限时让 Read 返回错误，json 解码随之失败，
// 生成代码回 400 —— 内存分配在超过上限的那一刻就停住了，不会先把整个报文读进来。
func LimitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// ---- 客户端 IP ----

// ClientIP 提取来源 IP。
//
// trustProxy 必须由配置控制：来源 IP 会写进 subscription_fetch_log 用于识别账号共享，
// 在不可信环境下信任 XFF 等于让用户自己决定日志里记什么。
//
// 🔴 **取的是 X-Forwarded-For 的最右一段，不是最左。** 这里曾经取最左，
// 而那行注释「代理追加在最右侧，最左侧是原始客户端」恰恰证明了取最左是错的：
// 既然入口只在**右侧追加**而不剥离调用方自带的值，那么调用方随手写一个
//
//	X-Forwarded-For: 9.9.9.9
//
// 就会原封不动留在最左边，Cloud Run 的前端把它观测到的真实对端追加成
// `9.9.9.9, <真实IP>`。取最左 = 取纯粹的用户输入。
//
// 可信的那一段是**基础设施自己追加的最后一段** —— 与 GCLB 文档「只有最后两段可信」
// 是同一条规则。`bp-api` 目前是 `--ingress=all` 直接暴露在 `*.run.app` 上，
// 前面只有 Google 的前端这一跳，所以取最后一段。
//
// ⚠️ **将来在前面加一层代理（CF 橙云 / GCLB，见 deploy §11.1 与 roadmap B9）时必须回来改这里**：
// 每多一跳可信代理，要跳过的尾部段数就多一段。**不要改回取最左。**
//
// 🔴 2026-08-23 起这件事的代价变了：本函数的返回值现在是 `rate_limit` 表里
// per-IP 维度的 subject（`internal/ratelimit` + auth.go 的 login / email-code /
// forgot / invite-verify）。忘了改这里的现象不再只是「审计日志记错 IP」——
// 多一跳代理之后**所有请求的最后一段都会是那一跳的地址**，于是全站用户共用
// 同一个限流 subject：每分钟前 5 次登录之后，**所有人一起 429**。
// 也就是说这条 TODO 的失败模式从「记错一列」升级成了「全站登录不可用」。
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if ip := rightmostForwardedFor(r.Header.Get("X-Forwarded-For")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rightmostForwardedFor 返回 XFF 里最后一段非空值，取不到返回 ""。
//
// 只跳过尾部的空段（`"1.1.1.1, "` 这种），不跳过解析不了的值 ——
// 一个非法的尾段说明入口行为与预期不符，那时宁可让调用方拿到一个明显错误的字符串，
// 也不要静默回退到更靠左、更不可信的那一段。
func rightmostForwardedFor(xff string) string {
	for xff != "" {
		var last string
		if i := strings.LastIndexByte(xff, ','); i >= 0 {
			last, xff = xff[i+1:], xff[:i]
		} else {
			last, xff = xff, ""
		}
		if last = strings.TrimSpace(last); last != "" {
			return last
		}
	}
	return ""
}

// ---- 错误响应 ----

// errBody 是失败响应的统一形状。
//
// ⚠️ UniProxy 的 200 响应是**裸 JSON 无信封**（v2node 兼容要求），
// 但错误响应仍用信封 —— 这个不对称是刻意的，api-contract.md 有记录。
type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeErrEnvelope(w http.ResponseWriter, status int, code, msg string) {
	var b errBody
	b.Error.Code = code
	b.Error.Message = msg
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(b)
}

// nodeErrBody 是节点面的错误形状：**裸 JSON，不套信封**。
// 与 openapi 的 NodeError schema 一一对应。
type nodeErrBody struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

func writeErrBare(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(nodeErrBody{Message: msg, Code: code})
}

// IsNodeAPIPath 判断一个路径是否属于节点面。
//
// 节点面与用户面的**错误形状不同**，而中间件层写错误响应时拿不到 operationID，
// 只能按路径判断。前缀常量与 CORS 用的是同一个（见 cors.go 的 nodeAPIPathPrefix）——
// 两处必须一致，否则会出现「CORS 认为是节点面、错误响应认为不是」的分裂。
func IsNodeAPIPath(path string) bool {
	return strings.HasPrefix(path, nodeAPIPathPrefix)
}

// WriteError 按请求路径选错误形状写出。
//
// 🔴 **节点面的错误也必须是裸 JSON。** api-contract §2.2 的例外表把整个
// `/api/v1/server/UniProxy/*` 端点族列为「裸 JSON，不套信封」，**没有「错误响应除外」这一条**；
// openapi 的 NodeUnauthorized / NodeForbidden / NodeRateLimited / NodeInternalError
// 四个 response 也全部 `$ref: NodeError`。
//
// 这里曾经无条件写信封，于是同一个端点上出现两种互不兼容的错误形状：
// handler 自己返的 403 NODE_ID_MISMATCH 是裸 NodeError，
// 中间件返的 403 NODE_SCOPE_DENIED 是 `{"error":{...}}`。
// 任何按契约生成结构体解析节点面错误的实现（v2node 的 fork、将来的自研节点）
// 在后者上会拿到 code/message 全空的零值。
//
// 现实影响有限 —— v2node 只看 HTTP 状态码，连响应体都不解析（见 node.go 的注释）——
// 但「契约怎么写就怎么发」本身就是节点面的立身之本，不能靠「反正没人看」维持。
// CI 抓不到这条：这段响应由中间件直接写 http.ResponseWriter，
// 绕过了 oapi-codegen 生成的 response 类型，契约测试断言不到。
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	if r != nil && IsNodeAPIPath(r.URL.Path) {
		writeErrBare(w, status, code, msg)
		return
	}
	writeErrEnvelope(w, status, code, msg)
}
