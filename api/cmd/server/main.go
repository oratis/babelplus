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

	// CORS 挂在 AccessLog **之后**，两个理由：
	//  1. 预检（OPTIONS）会被 CORS 短路掉，挂在前面就永远不会出现在访问日志里 ——
	//     而「预检失败」正是跨域故障的典型现象，日志里看不见它等于失去唯一线索。
	//  2. CORS 在调 next 之前就把响应头写进 header map，所以 Recover 兜底的 500
	//     也会带上 CORS 头 —— 否则浏览器连错误响应都读不到，前端只看到一个 network error。
	//
	// 节点面（/api/v1/server/*）由中间件内部跳过，不需要在这里分路由挂载。
	r.Use(mw.CORS(mw.CORSConfig{
		AllowedOrigins: cfg.AllowedOrigins,
		// 用户面会话走 Authorization 头（将来可能加 cookie），两者都需要它。
		AllowCredentials: true,
	}))

	nodeAuth := mw.NodeAuthConfig{
		DB:     db,
		Pepper: cfg.NodeKeyPepper,
		Logger: logger,
		// v2node **只发 query token**，不发 Authorization 头（2026-08-17 读源码核实，
		// evidence/v2node-contract-20260817 §3）。这个 true 是强制的，不是保守默认。
		AllowQueryToken: true,
	}

	userAuth := mw.UserAuthConfig{
		DB:     db,
		Pepper: cfg.SessionSigningKey,
		Logger: logger,
		// AllowCookie 保持 false（零值）。打开它之前必须先有 CSRF 防护：
		// 独立主域名池意味着 web 与 api 是**跨站**，SameSite=Lax 下 cookie 根本不会发出，
		// 真要用就得 SameSite=None —— 而 None 完全没有 CSRF 防护。
		// 当前 web 客户端只用 Authorization: Bearer，没有任何东西依赖 cookie 形态。
	}

	// 鉴权走 StrictMiddlewareFunc 而不是 chi 的按路由挂载。
	//
	// 理由：strict 中间件能拿到 **operationID**，于是「哪个 operation 需要哪种凭据」
	// 可以集中成几张表（operationAuth）。按 chi 路由挂载则要在路由注册处
	// 重复一遍路径字符串，规格改了路径就会静默失配 —— 而这里改了 operationID
	// 会被 TestOperationAuthCoverage **在编译测试阶段**抓住。
	//
	// 那个测试不是防御性洁癖，是一次真实事故的产物：本表曾把 PushUniProxyStatus
	// 写成 GetUniProxyStatus，而查不到的 key 只会让中间件原样放行，没有任何提示，
	// 于是该节点端点**完全无鉴权**（bc06ed028）。当时留的 TODO 是「加启动期断言」，
	// 这里用测试期反射兑现得更早一步：启动期断言炸的是生产冷启动，测试炸的是 CI。
	//
	// handler.RequestBinding 把原始 *http.Request 放进 ctx。
	//
	// 为什么必须有：strict 接口只把 ctx 与解包后的参数交给 handler，**拿不到请求头**。
	// 订阅下发要读 User-Agent（决定下发 YAML / JSON / base64）与来源 IP
	// （写 subscription_fetch_log，账号共享检测的唯一数据来源），两者只在原始请求里。
	// 缺了它订阅端点会返回 500 而不是静默降级 —— 见 handler.errNoBoundRequest。
	//
	// ⚠️ 生成代码的包裹顺序是 `for _, m := range middlewares { h = m(h, opID) }`，
	// 即**切片里越靠后越靠外**。RequestBinding 放在后面 → 它最先执行，
	// 于是鉴权中间件与 handler 都能从 ctx 里取到原始请求。
	strict := gen.NewStrictHandlerWithOptions(srv,
		[]gen.StrictMiddlewareFunc{authMiddleware(nodeAuth, userAuth), handler.RequestBinding()},
		gen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  requestErrorHandler(logger),
			ResponseErrorHandlerFunc: responseErrorHandler(logger),
		},
	)

	gen.HandlerFromMux(strict, r)

	return r
}

// 鉴权装配（operation → 凭据类型的五张表 + authMiddleware）在 authmap.go。

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
			"err", err, "path", mw.RedactPath(r.URL.Path), "request_id", mw.RequestIDFrom(r.Context()))
		// 码必须是 openapi 的 ErrorCode enum 成员：前端按 code 做分支，
		// enum 外的值会全部落到兜底分支，用户看到的是「未知错误」而不是可操作的提示。
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误")
	}
}

// requestErrorHandler 处理请求解包失败（参数缺失、类型不符、JSON 解析失败）。
func requestErrorHandler(logger *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		logger.WarnContext(r.Context(), "请求解析失败",
			"err", err, "path", mw.RedactPath(r.URL.Path), "request_id", mw.RequestIDFrom(r.Context()))
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
