package handler

import (
	"context"
	"log/slog"

	"github.com/oratis/babelplus/api/internal/config"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/store"
)

// Server 是 StrictServerInterface 的实际实现。
//
// 它嵌入 Unimplemented（自动生成的 128 个 501 stub），只覆盖已实现的 operation。
// 这样改 openapi.yaml 新增端点后重新生成 stub，Server 依然能编译 ——
// 新端点自动落到 501 而不是让整个包编译失败。
//
// 代价（已在 unimplemented.gen.go 顶部登记）：**漏实现不会在编译期暴露**。
// 兜底靠 operations.txt 的实现进度清单与集成测试。
type Server struct {
	Unimplemented

	cfg    *config.Config
	db     *store.Store
	logger *slog.Logger
}

// 编译期断言：Server 必须满足完整接口。
var _ gen.StrictServerInterface = (*Server)(nil)

// New 构造 Server。
func New(cfg *config.Config, db *store.Store, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, db: db, logger: logger}
}

// GetHealthz 是探活端点。
//
// 只探数据库连接，不查业务表 —— 健康检查不该被业务数据的状态影响。
// Cloud Run 的启动探针会打这个端点，返回非 200 会导致修订版被判定为不健康。
func (s *Server) GetHealthz(ctx context.Context, _ gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	if err := s.db.Health(ctx); err != nil {
		s.logger.ErrorContext(ctx, "健康检查失败：数据库不可达", "err", err)
		return gen.GetHealthz503JSONResponse{}, nil
	}
	return gen.GetHealthz200TextResponse("ok"), nil
}
