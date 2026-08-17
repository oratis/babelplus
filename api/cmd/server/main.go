// Command server 是 bp-api 的入口。
//
// 部署形态：Cloud Run（无状态、可缩到 0）。
// 因此这里刻意**没有**任何常驻后台 goroutine —— 定时任务走 Cloud Scheduler，
// 流量入账走 Cloud Tasks / Pub-Sub push（system-design §4 的裁定）。
// 加一个 ticker 就等于必须常开 min-instances，成本模型立刻不成立。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/oratis/babelplus/api/internal/config"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/handler"
	mw "github.com/oratis/babelplus/api/internal/middleware"
	"github.com/oratis/babelplus/api/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("启动失败", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		// fail-closed：配置不全直接退出，不带着半截配置跑。
		return err
	}

	logger := newLogger(cfg.LogLevel)
	logger.Info("bp-api 启动", "config", cfg.Redacted())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer db.Close()

	srv := handler.New(cfg, db, logger)
	r := buildRouter(cfg, db, logger, srv)

	httpSrv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
		// Cloud Run 最长请求 60 分钟，但我们没有长连接端点（配置下发是轮询不是 WS），
		// 所以给一个短得多的上限，避免慢请求占住连接。
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("监听中", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Cloud Run 在缩容前发 SIGTERM，给在途请求留出收尾时间。
		logger.Info("收到终止信号，开始优雅关闭")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

// buildRouter 装配路由。
//
// 三套 API 面的中间件链**刻意分开挂载** —— 节点面不该经过用户会话解析，
// 用户面不该有机会拿到节点 scope。共用一条链是权限混淆的常见来源。
func buildRouter(cfg *config.Config, db *store.Store, logger *slog.Logger, srv gen.StrictServerInterface) http.Handler {
	r := chi.NewRouter()

	// 全局链：只放对三套面都成立的东西。
	r.Use(mw.RequestID)
	r.Use(mw.Recover(logger))
	r.Use(mw.AccessLog(logger))

	nodeAuth := mw.NodeAuthConfig{
		DB:     db,
		Pepper: cfg.NodeKeyPepper,
		Logger: logger,
		// v2node **只发 query token**，不发 Authorization 头（2026-08-17 读源码核实，
		// evidence/v2node-contract-20260817 §3）。这个 true 是强制的，不是保守默认。
		AllowQueryToken: true,
	}

	// 鉴权走 StrictMiddlewareFunc 而不是 chi 的按路由挂载。
	//
	// 理由：strict 中间件能拿到 **operationID**，于是「哪个 operation 需要哪个 scope」
	// 可以集中成一张表（nodeOperationScopes）。按 chi 路由挂载则要在路由注册处
	// 重复一遍路径字符串，规格改了路径就会静默失配。
	//
	// ⚠️ 但**不要**以为写错 operationID 会报错：查不到的 key 只是让 isNodeOp=false，
	// 中间件原样放行，没有任何提示。兜底只有 handler 里的 NodeAuthFrom（见下面那段
	// 注释），而 501 stub 不调它 —— 所以拼错的 key 会一路静默到「实现该端点的那天」。
	// 这里曾经就把 PushUniProxyStatus 写成了 GetUniProxyStatus。
	// TODO(P1): 加启动期断言，用生成的 operationID 全集交叉校验本表的每个 key。
	strict := gen.NewStrictHandlerWithOptions(srv,
		[]gen.StrictMiddlewareFunc{nodeScopeMiddleware(nodeAuth)},
		gen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  requestErrorHandler(logger),
			ResponseErrorHandlerFunc: responseErrorHandler(logger),
		},
	)

	gen.HandlerFromMux(strict, r)

	return r
}

// nodeOperationScopes 是节点面 operation 到所需 scope 的映射。
//
// ⚠️ 这是权限控制的**唯一事实源**。不在这张表里的 operation 不会被当作节点面处理。
// 新增节点端点时必须同步加进来，否则它会以「非节点面」身份放行到 handler ——
// handler 里的 NodeAuthFrom 会失败并返回 errNoNodeAuth，属于装配错误而非鉴权绕过。
var nodeOperationScopes = map[string]string{
	"GetUniProxyConfig":    "node:config:read",
	"GetUniProxyUsers":     "node:users:read",
	"PushUniProxyTraffic":  "node:traffic:write",
	"PushUniProxyAlive":    "node:alive:write",
	"GetUniProxyAliveList": "node:alive:read",
	"PushUniProxyStatus":   "node:status:write",
}

// nodeScopeMiddleware 对节点面 operation 做鉴权与 scope 校验，其余 operation 原样放行。
//
// TODO(P1): 用户面与管理面的鉴权中间件尚未实现。在此之前它们全部返回 501
// （Unimplemented），因此**没有鉴权缺口** —— 但实现任何一个用户面 operation
// 之前必须先补上对应的鉴权中间件。
func nodeScopeMiddleware(cfg mw.NodeAuthConfig) gen.StrictMiddlewareFunc {
	return func(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
		scope, isNodeOp := nodeOperationScopes[operationID]
		if !isNodeOp {
			return f
		}
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			auth, authErr := mw.AuthenticateNode(ctx, cfg, r, scope)
			if authErr != nil {
				mw.WriteAuthError(w, authErr)
				// 返回 nil,nil：响应已经写完，生成代码不应再写一次。
				return nil, nil
			}
			return f(mw.WithNodeAuth(ctx, auth), w, r, request)
		}
	}
}

// responseErrorHandler 把 handler 返回的 error 映射成 HTTP 响应。
//
// 关键映射：ErrNotImplemented → **501**，不是 500。
// 这个区分不是洁癖 —— 501 表示「这个端点还没写」，500 表示「写了但炸了」。
// 混在一起会让监控上的错误率告警完全失去意义：脚手架阶段有 120+ 个端点未实现，
// 如果它们都算 500，5xx 告警会长期为红，真正的故障反而被淹没。
// monitoring.md 的告警规则正是按「5xx 排除 501」建的。
func responseErrorHandler(logger *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, handler.ErrNotImplemented) {
			writeErr(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "该端点尚未实现")
			return
		}
		logger.ErrorContext(r.Context(), "handler 返回错误",
			"err", err, "path", r.URL.Path, "request_id", mw.RequestIDFrom(r.Context()))
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "内部错误")
	}
}

// requestErrorHandler 处理请求解包失败（参数缺失、类型不符、JSON 解析失败）。
func requestErrorHandler(logger *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		logger.WarnContext(r.Context(), "请求解析失败",
			"err", err, "path", r.URL.Path, "request_id", mw.RequestIDFrom(r.Context()))
		writeErr(w, http.StatusBadRequest, "VALIDATION_MALFORMED_BODY", err.Error())
	}
}

// writeErr 输出统一的错误信封。
//
// ⚠️ UniProxy 的 200 响应是**裸 JSON 无信封**（v2node 兼容要求），
// 但错误响应仍用信封 —— 这个不对称是刻意的，api-contract.md 有记录。
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

// newLogger 输出 JSON 结构化日志，字段名对齐 Cloud Logging。
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// Cloud Logging 用 severity 而不是 level 做日志级别。
			if a.Key == slog.LevelKey {
				a.Key = "severity"
			}
			if a.Key == slog.MessageKey {
				a.Key = "message"
			}
			return a
		},
	}))
}
