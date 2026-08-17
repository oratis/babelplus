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
					writeErrEnvelope(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误")
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

// ---- 客户端 IP ----

// ClientIP 提取来源 IP。
//
// trustProxy 必须由配置控制：来源 IP 会写进 subscription_fetch_log 用于识别账号共享，
// 在不可信环境下信任 XFF 等于让用户自己决定日志里记什么。
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Cloud Run 追加在最右侧，最左侧是原始客户端。
			if first, _, found := strings.Cut(xff, ","); found {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
