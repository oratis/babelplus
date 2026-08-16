package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/httpx"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 节点面（UniProxy v1 兼容）。
//
// 这一组是全系统唯一的高频路径：节点每 60 秒轮询 /config 与 /user。
// 因此 ETag 协商是**真实实现**，不是 TODO —— 它决定了请求量能不能算平：
// 10 节点 × 2 端点 × 1440 次/天 × 30 天 = 1,728,000 请求/月，
// 占 Cloud Run 免费额度 200 万/月的 86%。
//
// 🔴 前提未验证：v2node 是否发送 If-None-Match。不发则 304 一次都不会命中，
// 本文件的 ETag 逻辑全部空转（ADR 0006 §11.4 最高优先级实测项）。
// 观测方法：monitoring.md 里对 304 命中率建了 log-based metric，
// 上线后若命中率长期为 0，说明前提不成立，需要改用别的降载手段。

// errNoNodeAuth 表示中间件没有把节点身份注入上下文 —— 属于装配错误，不是用户错误。
var errNoNodeAuth = errors.New("节点身份缺失：路由未挂载 RequireNodeScope 中间件")

// GetUniProxyConfig 下发节点自身配置。
//
// ETag 用 config_rev 生成，**不需要先序列化响应体**。
func (s *Server) GetUniProxyConfig(ctx context.Context, req gen.GetUniProxyConfigRequestObject) (gen.GetUniProxyConfigResponseObject, error) {
	auth, ok := middleware.NodeAuthFrom(ctx)
	if !ok {
		return nil, errNoNodeAuth
	}

	rev, err := s.db.GetNodeRev(ctx, auth.ServerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// node_rev 行由 InitNodeRev 在建节点时写入。缺行说明建节点流程没走完，
			// 这是运维错误，要能在日志里看见，不能静默当成「无变更」。
			s.logger.ErrorContext(ctx, "节点缺少 node_rev 行", "server_code", auth.ServerCode)
		}
		return nil, err
	}

	etag := httpx.RevisionETag(httpx.ScopeConfig, rev.ConfigRev)
	if req.Params.IfNoneMatch != nil && httpx.MatchesETag(string(*req.Params.IfNoneMatch), etag) {
		return gen.GetUniProxyConfig304Response{
			Headers: gen.NodeNotModifiedResponseHeaders{
				CacheControl: httpx.CacheControlNoStore,
				ETag:         etag,
			},
		}, nil
	}

	// TODO(P1): 从 servers 表组装 NodeConfig（协议参数、REALITY 密钥、Hysteria2 配置）。
	// 注意 ADR 0004 的两条硬约束：Hysteria2 用 BBR 不用 Brutal；TCP 路径开 mux。
	// 另注意 up_mbps/down_mbps 必须下发 0（见 gen.NodeConfig 的字段注释）。
	_ = etag
	return nil, ErrNotImplemented
}

// GetUniProxyUsers 下发该节点的可用用户列表。
//
// 这是**全系统请求量最大的查询**。ETag 用 user_rev 生成，
// 命中 304 就完全不查 users 表 —— data-model.md 的 node_rev 触发器
// 就是为这条路径设计的（改 users 的关键列会自动 bump user_rev）。
func (s *Server) GetUniProxyUsers(ctx context.Context, req gen.GetUniProxyUsersRequestObject) (gen.GetUniProxyUsersResponseObject, error) {
	auth, ok := middleware.NodeAuthFrom(ctx)
	if !ok {
		return nil, errNoNodeAuth
	}

	rev, err := s.db.GetNodeRev(ctx, auth.ServerID)
	if err != nil {
		return nil, err
	}

	etag := httpx.RevisionETag(httpx.ScopeUserList, rev.UserRev)
	if req.Params.IfNoneMatch != nil && httpx.MatchesETag(string(*req.Params.IfNoneMatch), etag) {
		// 命中即返回，**不查 users 表**。这是整套 ETag 设计的全部意义。
		return gen.GetUniProxyUsers304Response{
			Headers: gen.NodeNotModifiedResponseHeaders{
				CacheControl: httpx.CacheControlNoStore,
				ETag:         etag,
			},
		}, nil
	}

	// TODO(P1): 调 ListAvailableUsersByServer 并映射成 gen.NodeUser。
	// 可用性判定已在 SQL 里一条覆盖：u + d < transfer_enable
	// AND (expired_at IS NULL OR 未过期) AND NOT banned。
	// ⚠️ 空列表必须序列化成 {"users":[]} 而不是 {"users":null} ——
	// sqlc 的 emit_empty_slices 已经保证了这一点，改配置时别弄丢。
	return nil, ErrNotImplemented
}

// PushUniProxyTraffic 接收节点上报的流量。
//
// TODO(P1): 按 system-design §4 的裁定，这里**只做入队**（Cloud Tasks / Pub-Sub push），
// 立即返回，不在请求路径上累加 —— 否则每 60 秒一次的写会打满 db-f1-micro 的连接。
// 真正的累加在 push 回调的内部路由里做，且必须幂等（幂等键：server_id + 上报窗口）。
func (s *Server) PushUniProxyTraffic(ctx context.Context, _ gen.PushUniProxyTrafficRequestObject) (gen.PushUniProxyTrafficResponseObject, error) {
	if _, ok := middleware.NodeAuthFrom(ctx); !ok {
		return nil, errNoNodeAuth
	}
	return nil, ErrNotImplemented
}

// PushUniProxyAlive 接收在线设备上报，用于设备数限制。
//
// TODO(P1): 写 user_device_state（UNLOGGED 表，ADR 0005 裁定不买 Redis）。
// 用 BulkUpsertUserDeviceState 批量写，不要逐条 —— 上报是按节点批量来的。
func (s *Server) PushUniProxyAlive(ctx context.Context, _ gen.PushUniProxyAliveRequestObject) (gen.PushUniProxyAliveResponseObject, error) {
	if _, ok := middleware.NodeAuthFrom(ctx); !ok {
		return nil, errNoNodeAuth
	}
	return nil, ErrNotImplemented
}

// GetUniProxyAliveList 返回各用户当前在线设备数，供节点执行设备数限制。
//
// TODO(P1): 调 ListAliveDeviceCounts。
func (s *Server) GetUniProxyAliveList(ctx context.Context, _ gen.GetUniProxyAliveListRequestObject) (gen.GetUniProxyAliveListResponseObject, error) {
	if _, ok := middleware.NodeAuthFrom(ctx); !ok {
		return nil, errNoNodeAuth
	}
	return nil, ErrNotImplemented
}

var _ = context.Background // 保留 context 导入，供后续实现使用
