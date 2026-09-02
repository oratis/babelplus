package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	dbgen "github.com/oratis/babelplus/api/db/gen"
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
// ✅ 2026-08-17 前提已验证（evidence/v2node-contract-20260817 §2）：
// v2node 完整实现了条件请求 —— 发送 If-None-Match、收到 304 直接短路不解析响应体、
// 并保存新的 ETag。本文件的 ETag 逻辑**有实际收益**，不是空转。
//
// 🔴 但另一个约束浮现了：v2node 的默认请求超时是 **15 秒，且只重试 1 次**。
// 一次冷启动超时就会让该节点这一轮拿不到用户列表。
// 所以 /user 的 p99 必须远低于 15 秒 —— 这是 ETag 之外，本端点必须做好的第二个理由。
//
// 另：JSON 回退路径下 v2node 是**流式解析**，扫到 "users" 键后要求紧跟 `[`。
// {"users": null} 会直接报错 —— sqlc 的 emit_empty_slices 是承重配置，不要改。
//
// ---- 本文件的三条实现纪律 ----
//
//  1. **所有形状转换都放在纯函数里**（buildNodeConfig / buildNodeUserList /
//     parseTrafficBatch / parseAliveBatch / buildAliveList）。
//     Server.db 是具体类型 *store.Store，塞不了假实现，所以能被单测覆盖的只有纯函数 ——
//     而节点面最致命的几个 bug（users:null、alive:null、uid 解析）全部在形状层。
//  2. **上报端点一律尽最大努力吞下一批**：坏条目丢弃并计数，不让整批失败
//     （api-contract §3.5 的容错要求）。整批失败对我们是纯损失 ——
//     v2node 不看响应状态码，不会因为 4xx/5xx 重发。
//  3. **响应体不套统一信封**。UniProxy 是冻结契约，包一层 {"data":…} 立刻不兼容。
//  4. **每个端点在 node_id 校验通过后都要调 s.noteNodeAlive**。它是 monitoring.md §5
//     第 1 条「节点心跳缺失」告警的唯一数据来源（bp_node_alive），
//     而那条告警是 metric absence —— 从未上报过的节点在监控眼里不存在。
//     新增节点面端点时漏掉它不会有任何报错，只会让心跳的采样点变少。
//     完整理由见 nodealive.go。

// errNoNodeAuth 表示中间件没有把节点身份注入上下文 —— 属于装配错误，不是用户错误。
var errNoNodeAuth = errors.New("节点身份缺失：路由未挂载 RequireNodeScope 中间件")

// errNodeConfigIncomplete 表示 servers.protocol_settings 缺少该协议必需的字段。
//
// 刻意让它变成 500 而不是「下发一份缺字段的配置」：
// 缺字段的配置会让节点起不来或起成一个**不设防**的监听（例如 REALITY 没有私钥），
// 而节点侧的失败没人看。500 会进我们的错误率告警，10 分钟内就有人知道。
var errNodeConfigIncomplete = errors.New("节点 protocol_settings 缺少必需字段")

// ---- base_config 的四个值 ----
//
// 这四个值当前是常量。它们**应当**是每节点可配的（P2 挪进 servers.protocol_settings
// 或 config），但在只有个位数节点的阶段，把它们放在代码里反而更容易论证一致性。
const (
	// 拉取 / 上报间隔固定 60 秒。这就是 §3.1 请求量算术的输入 —— 改它等于改成本模型。
	nodePullIntervalSeconds int32 = 60
	nodePushIntervalSeconds int32 = 60

	// nodeDeviceOnlineMinTrafficKB：本轮流量低于此值的用户**不计入在线设备**。
	//
	// 单位是 **KB** —— 已实测（evidence §6：v2node 里写的是 `devicemin*1000` 转字节）。
	// 取 1000（= 1 MB）的理由：设为 0 会让「开着客户端但没上网」的每个空闲连接
	// 都吃掉一个设备名额，而我们的定价杠杆正是设备数（2/5/10 档）——
	// 那会直接变成客诉。1000 是推断值，**需在真实用量下调参**。
	nodeDeviceOnlineMinTrafficKB int64 = 1000

	// nodeReportMinTraffic：本轮流量低于此值的用户**不上报流量**。
	//
	// 🔴 取 **0（= 不过滤）**，与 api-contract §3.3 示例里的 1000 **不同**，这是刻意的。
	// 理由：这个字段的**单位至今未确认**（evidence §11 第一条：没读到
	// GetUserTrafficSlice 的实现，它在 xray-core 侧）。
	//   - 若单位是字节：1000 字节是个无意义的过滤，取 0 与取 1000 没有区别；
	//   - 若单位是 KB：1000 = 1 MB，意味着每个用户每分钟最多 1 MB 的流量**永不入账**。
	//     单用户理论上限 1 MB/min × 1440 = 1.4 GB/天不计费。
	// 两种读法里，0 都是安全的那个；1000 在其中一种读法下是持续的收入漏洞。
	// 单位确认之后再考虑调高（收益只是少几条 push 条目，很小）。
	nodeReportMinTraffic int64 = 0
)

// ---- 协议参数 ----
const (
	// 🔴 Hysteria2 的 up/down_mbps 必须下发 0。
	// 这两个字段**就是 Brutal 拥塞控制的参数**，ADR 0004 已裁定「Hysteria2 用 BBR 不用 Brutal」，
	// 所以必须下发 0（= 不声明带宽 = 不启用 Brutal）。
	// ⚠️ v2node 对 0 的处理**仍未实测**（evidence 没覆盖这一条）——
	// 若它把 0 当成「无限带宽」而不是「不启用」，行为与预期相反，接入首节点时必须回验。
	hysteriaUpMbps   int32 = 0
	hysteriaDownMbps int32 = 0
	hysteriaVersion  int32 = 2

	defaultListenIP     = "0.0.0.0"
	defaultHysteriaObfs = "salamander"
	vlessRealityFlow    = "xtls-rprx-vision"

	// tls 字段：0 无 / 1 TLS / 2 REALITY（照抄 Xboard buildNodeConfig）。
	tlsModeTLS     int32 = 1
	tlsModeReality int32 = 2
)

// ---- 上报批量的上限 ----
//
// 节点是经过鉴权的，不是任意来源，所以这里不是防攻击而是**防事故**：
// 一个跑飞的节点端可以把单批做到任意大，而 unnest 的数组会整个进内存、
// 再整个进一条 SQL。截断并告警比让 Cloud Run 实例 OOM 好 —— OOM 会连带影响
// 同实例上所有用户面请求。
const (
	maxTrafficEntriesPerPush = 50_000
	maxAliveRowsPerPush      = 50_000
)

// GetUniProxyConfig 下发节点自身配置。
//
// ETag 用 config_rev 生成，**不需要先序列化响应体**。
// nodeConfigResult 是 /config 的路径无关结果。
//
// 抽出来的唯一理由是**两条路径返回同一件事**：
//
//	/api/v1/server/UniProxy/config —— 冻结契约（抄 v2board 1.7.4 / Xboard）
//	/api/v2/server/config          —— v2node ≥ v0.4.0 实际请求的路径
//
// 见 openapi.yaml 里 getNodeConfigV2 的 description。
// 两个 handler 只做响应类型转换，业务分支一份，不允许有第二处。
type nodeConfigResult struct {
	forbidden   *gen.NodeForbiddenJSONResponse
	notModified bool
	etag        string
	body        gen.NodeConfig
}

func (s *Server) nodeConfig(ctx context.Context, nodeID *int64, ifNoneMatch *string) (nodeConfigResult, error) {
	var out nodeConfigResult

	auth, ok := middleware.NodeAuthFrom(ctx)
	if !ok {
		return out, errNoNodeAuth
	}
	if !s.nodeIDMatches(ctx, auth, nodeID) {
		f := gen.NodeForbiddenJSONResponse(errNodeIDMismatch())
		out.forbidden = &f
		return out, nil
	}
	s.noteNodeAlive(ctx, auth)

	rev, err := s.db.GetNodeRev(ctx, auth.ServerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// node_rev 行由 InitNodeRev 在建节点时写入。缺行说明建节点流程没走完，
			// 这是运维错误，要能在日志里看见，不能静默当成「无变更」。
			s.logger.ErrorContext(ctx, "节点缺少 node_rev 行", "server_code", auth.ServerCode)
		}
		return out, err
	}

	out.etag = httpx.RevisionETag(httpx.ScopeConfig, rev.ConfigRev)
	if ifNoneMatch != nil && httpx.MatchesETag(*ifNoneMatch, out.etag) {
		out.notModified = true
		return out, nil
	}

	row, err := s.db.GetServerConfig(ctx, auth.ServerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 鉴权已经过了（AuthenticateServerKey 里 JOIN 了 servers 且过滤 deleted_at），
			// 所以这里查不到只可能是这两次查询之间节点被软删了 —— 竞态，不是常态。
			s.logger.ErrorContext(ctx, "节点鉴权通过但配置行不存在", "server_code", auth.ServerCode)
		}
		return out, err
	}

	cfg, err := buildNodeConfig(row)
	if err != nil {
		s.logger.ErrorContext(ctx, "组装节点配置失败",
			"server_code", auth.ServerCode, "protocol", string(row.Protocol), "err", err)
		return out, err
	}
	out.body = cfg
	return out, nil
}

func (s *Server) GetUniProxyConfig(ctx context.Context, req gen.GetUniProxyConfigRequestObject) (gen.GetUniProxyConfigResponseObject, error) {
	var inm *string
	if req.Params.IfNoneMatch != nil {
		v := string(*req.Params.IfNoneMatch)
		inm = &v
	}
	res, err := s.nodeConfig(ctx, req.Params.NodeId, inm)
	if err != nil {
		return nil, err
	}
	if res.forbidden != nil {
		return gen.GetUniProxyConfig403JSONResponse{NodeForbiddenJSONResponse: *res.forbidden}, nil
	}
	if res.notModified {
		return gen.GetUniProxyConfig304Response{
			Headers: gen.NodeNotModifiedResponseHeaders{
				CacheControl: httpx.CacheControlNoStore,
				ETag:         res.etag,
			},
		}, nil
	}
	return gen.GetUniProxyConfig200JSONResponse{
		Body: res.body,
		Headers: gen.GetUniProxyConfig200ResponseHeaders{
			CacheControl: httpx.CacheControlNoStore,
			ETag:         res.etag,
		},
	}, nil
}

// GetNodeConfigV2 与 GetUniProxyConfig 返回逐字节相同的东西，只是路径不同。
//
// 🔴 它的存在是一条实测结论，不是设计偏好：v2node（本项目选定的 agent）
// 自 v0.4.0 起把**且仅把**配置端点迁到 /api/v2/server/config，
// 其余四个（/user /push /alive /alivelist）仍在 /api/v1/server/UniProxy/*。
// 2026-09-01 第一次真机装机时才发现 —— 因为冻结契约抄的是 v2board/Xboard，
// 而 agent 是 v2node，两者在这一个端点上分了叉。
// 失败形态极具误导性：v2node 拿到 Go 默认的 404 page not found，把它当 JSON 解，
// 报 "invalid character 'p' after top-level value"，日志里完全看不出是路由不匹配。
func (s *Server) GetNodeConfigV2(ctx context.Context, req gen.GetNodeConfigV2RequestObject) (gen.GetNodeConfigV2ResponseObject, error) {
	var inm *string
	if req.Params.IfNoneMatch != nil {
		v := string(*req.Params.IfNoneMatch)
		inm = &v
	}
	res, err := s.nodeConfig(ctx, req.Params.NodeId, inm)
	if err != nil {
		return nil, err
	}
	if res.forbidden != nil {
		return gen.GetNodeConfigV2403JSONResponse{NodeForbiddenJSONResponse: *res.forbidden}, nil
	}
	if res.notModified {
		return gen.GetNodeConfigV2304Response{
			Headers: gen.NodeNotModifiedResponseHeaders{
				CacheControl: httpx.CacheControlNoStore,
				ETag:         res.etag,
			},
		}, nil
	}
	return gen.GetNodeConfigV2200JSONResponse{
		Body: res.body,
		Headers: gen.GetNodeConfigV2200ResponseHeaders{
			CacheControl: httpx.CacheControlNoStore,
			ETag:         res.etag,
		},
	}, nil
}

// GetUniProxyUsers 下发该节点的可用用户列表。
//
// 这是**全系统请求量最大的查询**。ETag 用 user_rev 生成，
// 命中 304 就完全不查 users 表 —— data-model.md 的 node_rev 触发器
// 就是为这条路径设计的（改 users 的关键列会自动 bump user_rev）。
//
// ⚠️ 慢查询风险登记：ListAvailableUsersByServer 的
// `ut.u + ut.d < u.transfer_enable` 是**跨表跨列比较，无法索引**，
// 只能在 users ⨝ user_traffic 的连接结果上逐行过滤。
// 用户数上万之后这条会成为 15 秒超时预算里最大的一项。
// 缓解顺序：① 先靠 ETag 让它极少真的执行；② 再考虑把「是否超额」物化成 users 上的
// 一个布尔列（代价是 push 入账时要维护它，且要进触发器监视列表）。
func (s *Server) GetUniProxyUsers(ctx context.Context, req gen.GetUniProxyUsersRequestObject) (gen.GetUniProxyUsersResponseObject, error) {
	auth, ok := middleware.NodeAuthFrom(ctx)
	if !ok {
		return nil, errNoNodeAuth
	}
	if !s.nodeIDMatches(ctx, auth, req.Params.NodeId) {
		return gen.GetUniProxyUsers403JSONResponse{
			NodeForbiddenJSONResponse: gen.NodeForbiddenJSONResponse(errNodeIDMismatch()),
		}, nil
	}
	s.noteNodeAlive(ctx, auth)

	rev, err := s.db.GetNodeRev(ctx, auth.ServerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.logger.ErrorContext(ctx, "节点缺少 node_rev 行", "server_code", auth.ServerCode)
		}
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

	// 可用性判定已在 SQL 里一条覆盖：u + d < transfer_enable
	// AND coalesce(expired_at,'infinity') > now() AND NOT banned AND 分组匹配。
	rows, err := s.db.ListAvailableUsersByServer(ctx, auth.ServerID)
	if err != nil {
		return nil, err
	}

	list, skipped := buildNodeUserList(rows)
	if skipped > 0 {
		// uuid 是 NOT NULL 列，所以 skipped > 0 只可能是数据被手工污染。
		// 必须能看见：被跳过的用户会在节点侧表现为「订阅正常但连不上」，
		// 而这个现象用户根本无法自诊断。
		s.logger.ErrorContext(ctx, "用户列表中存在 uuid 非法的行，已跳过",
			"server_code", auth.ServerCode, "skipped", skipped, "total", len(rows))
	}

	return gen.GetUniProxyUsers200JSONResponse{
		Body: list,
		Headers: gen.GetUniProxyUsers200ResponseHeaders{
			CacheControl: httpx.CacheControlNoStore,
			ETag:         etag,
		},
	}, nil
}

// PushUniProxyTraffic 接收节点上报的流量。
//
// ⚠️⚠️ **这是临时实现：在 HTTP 请求路径上同步累加。**
//
// system-design §4 与 api-contract §3.5 裁定的目标形态是「只入队立即返回」：
//
//	POST /push → INSERT traffic_batch + enqueue Cloud Tasks → 200（预算 < 50 ms）
//	           → /internal/tasks/traffic-batch 里才真的累加
//
// TODO(P2): 改成上面的形态。**这不是可选优化**，两条硬理由：
//   - 每 60 秒一次的写 × 节点数会打满 db-f1-micro 的连接（ADR 0005 的实例规格）；
//   - /push 越慢，v2node 越可能在 15 秒超时后重发，而重发是我们唯一的重复来源。
//
// 在那之前，本实现至少要把「重复上报」挡住 —— 见 trafficBatchKey 的长注释。
//
// 🔴 v2node **完全忽略响应体，也不看状态码**（evidence §5：`_, err :=`）。
// 两个推论，都必须记住：
//   - 我们返回什么形状都行，2xx 即可 —— 但仍按契约返回 {"data":true}；
//   - 我们返回 5xx **不会触发它重试**，这一批就是永久丢了。
//     所以「宁可丢一批也不要重复计费」这个取舍在本端点上是有真实代价的，
//     它不是免费的保守选择。
func (s *Server) PushUniProxyTraffic(ctx context.Context, req gen.PushUniProxyTrafficRequestObject) (gen.PushUniProxyTrafficResponseObject, error) {
	auth, ok := middleware.NodeAuthFrom(ctx)
	if !ok {
		return nil, errNoNodeAuth
	}
	if !s.nodeIDMatches(ctx, auth, req.Params.NodeId) {
		return gen.PushUniProxyTraffic403JSONResponse{
			NodeForbiddenJSONResponse: gen.NodeForbiddenJSONResponse(errNodeIDMismatch()),
		}, nil
	}
	s.noteNodeAlive(ctx, auth)
	if req.Body == nil {
		return gen.PushUniProxyTraffic400JSONResponse{
			NodeBadRequestJSONResponse: gen.NodeBadRequestJSONResponse(
				nodeError(gen.VALIDATIONMALFORMEDBODY, "请求体缺失")),
		}, nil
	}

	batch := parseTrafficBatch(*req.Body)
	if batch.Dropped > 0 {
		// 契约要求坏条目静默丢弃不让整批失败，但「静默」只对节点静默 ——
		// 对我们必须可见，否则节点端换版本导致的形状变化会表现为「流量凭空少了一半」。
		s.logger.WarnContext(ctx, "流量上报存在非法条目，已丢弃",
			"server_code", auth.ServerCode, "dropped", batch.Dropped, "accepted", len(batch.UserIDs))
	}
	if batch.Truncated {
		s.logger.ErrorContext(ctx, "流量上报条目数超上限，已截断",
			"server_code", auth.ServerCode, "limit", maxTrafficEntriesPerPush)
	}

	if len(batch.UserIDs) == 0 {
		// 空批（全零 / 全非法）也算一次心跳：节点还活着，只是这一分钟没人用它。
		// 不走幂等表 —— 省两次写，且空批本来就没有重复计费的风险。
		s.touchPush(ctx, auth)
		return ackTraffic(), nil
	}

	key := trafficBatchKey(auth.ServerID, batch.Canonical)
	att, err := httpx.BeginIdempotent(ctx, s.db, httpx.IdempotentRequest{
		Key: key,
		// UserID 留空：上报方是节点不是用户。
		Endpoint: "PushUniProxyTraffic",
		Body:     batch.Canonical,
	})
	switch {
	case err == nil && att.Outcome == httpx.OutcomeExecute:
		// 落到下面真的入账。

	case err == nil: // OutcomeReplay
		s.logger.InfoContext(ctx, "流量上报重复，已按幂等丢弃",
			"server_code", auth.ServerCode, "entries", len(batch.UserIDs))
		return ackTraffic(), nil

	case errors.Is(err, httpx.ErrIdempotencyInProgress):
		// 同一批的上一次还在处理中 —— 正是「v2node 15 秒超时后重发，而我们还没写完」。
		// 丢弃是唯一正确的选择：执行就是重复计费。
		s.logger.WarnContext(ctx, "流量上报与上一次同批并发，已丢弃",
			"server_code", auth.ServerCode, "entries", len(batch.UserIDs))
		return ackTraffic(), nil

	case errors.Is(err, httpx.ErrIdempotencyKeyStale):
		// 幂等键过期未清理。丢弃而不是执行：双计需要人工冲正，少记一分钟只是少收一点钱。
		// 出现这条日志说明 CleanupExpiredIdempotencyKeys 没被定时调起来。
		s.logger.ErrorContext(ctx, "幂等键过期未清理，本批流量已丢弃（请检查清理定时任务）",
			"server_code", auth.ServerCode, "key", key)
		return ackTraffic(), nil

	case errors.Is(err, httpx.ErrIdempotencyMismatch):
		// 键就是载荷指纹的函数，同键必然同指纹 —— 走到这里意味着 sha256 撞了，
		// 或者有人往 idempotency_keys 里手工写了行。两者都该炸出来看。
		s.logger.ErrorContext(ctx, "流量上报幂等键指纹不一致（不应发生）",
			"server_code", auth.ServerCode, "key", key)
		return ackTraffic(), nil

	default:
		return nil, err
	}

	// 累加 user_traffic + 按天聚合 stat_user_server，**不落明细流水**
	// （stats.sql 开头：Xboard 写三处，三份数字可能对不上且没有任何机制发现）。
	// 两条必须同事务，否则「计数涨了但日报没涨」会在对账时无从追查。
	var result dbgen.AddNodeTrafficBatchRow
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var txErr error
		result, txErr = q.AddNodeTrafficBatch(ctx, dbgen.AddNodeTrafficBatchParams{
			ServerID:  auth.ServerID,
			UserIds:   batch.UserIDs,
			UpBytes:   batch.Up,
			DownBytes: batch.Down,
		})
		if txErr != nil {
			return txErr
		}
		if txErr = q.BulkUpsertStatUserServer(ctx, dbgen.BulkUpsertStatUserServerParams{
			ServerID:  auth.ServerID,
			UserIds:   batch.UserIDs,
			UpBytes:   batch.Up,
			DownBytes: batch.Down,
		}); txErr != nil {
			return txErr
		}
		return q.TouchServerPush(ctx, auth.ServerID)
	})
	if err != nil {
		// 幂等键会以 in_progress 残留到过期，同批重发在 24 小时内一直被丢弃。
		// 这是 httpx.BeginIdempotent 已登记的代价（抢占刻意不在业务事务里）。
		// 对 /push 而言影响有限：v2node 不看状态码，本来也不会重发这一批。
		s.logger.ErrorContext(ctx, "流量入账失败",
			"server_code", auth.ServerCode, "entries", len(batch.UserIDs), "err", err)
		return nil, err
	}

	if int(result.UpdatedUsers) != len(batch.UserIDs) {
		// 上报里的 user_id 在 user_traffic 里没有行。这条日志是那部分流量
		// **静默蒸发**的唯一信号，不打就永远发现不了。
		//
		// 成因不是「用户被硬删」—— user_traffic 对 users 是 ON DELETE CASCADE，
		// 硬删会连带删掉 user_traffic 行，而本项目的用户删除是 deleted_at 软删。
		// 真实成因只有两类：
		//   1. 开户流程漏建 user_traffic 行（我们的 bug）；
		//   2. **节点报上来一个我们这边根本不存在的 user_id**（节点侧 bug、
		//      节点指错了环境、节点主机被拿下、或 DR 回滚后节点持有更新的用户列表）。
		// 第 2 类以前会让 BulkUpsertStatUserServer 撞外键并回滚整批 ——
		// 现在两条语句都 JOIN user_traffic，未知 id 一致地被丢弃，
		// 于是这条日志成了它唯一的出口。**持续出现要当成节点侧异常查，不是噪声。**
		s.logger.WarnContext(ctx, "流量上报中有 user_id 无对应 user_traffic 行，该部分已丢弃",
			"server_code", auth.ServerCode,
			"reported", len(batch.UserIDs), "updated", result.UpdatedUsers)
	}
	if result.BumpedServers > 0 {
		// 有用户在本批里跨过配额阈值 —— 相关节点的 user_rev 已 bump，下一轮它们会拿 200 而非 304。
		s.logger.InfoContext(ctx, "本批流量导致用户配额耗尽，已 bump 相关节点 user_rev",
			"server_code", auth.ServerCode, "bumped_servers", result.BumpedServers)
	}

	if err := httpx.CompleteIdempotent(ctx, s.db, att.Key, 200, ackTrafficBody); err != nil {
		// 只影响「同批重发会被再执行一次」的窗口，不影响本次入账结果，所以不失败请求。
		s.logger.WarnContext(ctx, "幂等键落盘失败", "server_code", auth.ServerCode, "err", err)
	}
	return ackTraffic(), nil
}

// PushUniProxyAlive 接收在线设备上报，用于设备数限制。
//
// 写 user_device_state（UNLOGGED 表，ADR 0005 裁定不买 Redis）。
// 用 BulkUpsertUserDeviceState 批量写，不逐条 —— 上报本来就是按节点整批来的，
// 200 用户 × 3 IP = 600 行逐条写就是 600 次往返，而连接池每实例只有 2 条连接。
//
// 口径是 **按 IP**，不是按设备（evidence §5 已确认：节点报 IP 明细、面板回计数）。
// 这个口径必须原样承认并写在产品页面上：同一台手机在 Wi-Fi 与蜂窝之间切换会占两个名额。
//
// 本端点不需要幂等：写的是 last_seen_at 的 upsert，重复执行结果相同。
func (s *Server) PushUniProxyAlive(ctx context.Context, req gen.PushUniProxyAliveRequestObject) (gen.PushUniProxyAliveResponseObject, error) {
	auth, ok := middleware.NodeAuthFrom(ctx)
	if !ok {
		return nil, errNoNodeAuth
	}
	if !s.nodeIDMatches(ctx, auth, req.Params.NodeId) {
		return gen.PushUniProxyAlive403JSONResponse{
			NodeForbiddenJSONResponse: gen.NodeForbiddenJSONResponse(errNodeIDMismatch()),
		}, nil
	}
	s.noteNodeAlive(ctx, auth)
	if req.Body == nil {
		return gen.PushUniProxyAlive400JSONResponse{
			NodeBadRequestJSONResponse: gen.NodeBadRequestJSONResponse(
				nodeError(gen.VALIDATIONMALFORMEDBODY, "请求体缺失")),
		}, nil
	}

	batch := parseAliveBatch(*req.Body)
	if batch.Dropped > 0 {
		s.logger.WarnContext(ctx, "在线设备上报存在非法条目，已丢弃",
			"server_code", auth.ServerCode, "dropped", batch.Dropped, "accepted", len(batch.UserIDs))
	}
	if batch.Truncated {
		s.logger.ErrorContext(ctx, "在线设备上报条目数超上限，已截断",
			"server_code", auth.ServerCode, "limit", maxAliveRowsPerPush)
	}
	if len(batch.UserIDs) == 0 {
		return ackAlive(), nil
	}

	// BulkUpsertUserDeviceState 的 SQL 用 WITH ORDINALITY 按下标配对两个数组，
	// 等长是**调用方的义务**（servers.sql 里写明了）。parseAliveBatch 是唯一的构造点，
	// 但这里仍然断言一次：不等长不会报错，会静默丢掉尾部，那种 bug 极难发现。
	if len(batch.UserIDs) != len(batch.DeviceIPs) {
		return nil, fmt.Errorf("在线设备上报数组长度不一致: users=%d ips=%d",
			len(batch.UserIDs), len(batch.DeviceIPs))
	}

	if err := s.db.BulkUpsertUserDeviceState(ctx, dbgen.BulkUpsertUserDeviceStateParams{
		ServerID:  auth.ServerID,
		UserIds:   batch.UserIDs,
		DeviceIps: batch.DeviceIPs,
	}); err != nil {
		s.logger.ErrorContext(ctx, "写入在线设备状态失败",
			"server_code", auth.ServerCode, "rows", len(batch.UserIDs), "err", err)
		return nil, err
	}
	return ackAlive(), nil
}

// GetUniProxyAliveList 返回各用户当前在线设备数，供节点执行设备数限制。
//
// 🔴 **本端点失败时 v2node 静默降级为「所有用户 0 台在线设备」**
// （evidence §5：err 或 status ≥ 399 都会把 AliveMap 清空后照常返回 nil）。
// 也就是说我们挂掉时，节点不会拒绝服务，而是**当作没有设备数限制继续放行**。
//
// 这个行为的两面都必须写进文档：
//   - 对可用性是好事（面板挂了不影响用户上网，符合 system-design §5.3）；
//   - 但它意味着**设备数限制只能是尽力而为的软限制**，
//     不能作为计费或防滥用的强保证 —— product-brief 的能力边界必须这么写。
//
// ⚠️ 计数是**全网**的（ListAliveDeviceCounts 不按 server_id 过滤），这是有意的：
// 设备数是账号级配额，按节点算等于用户连 N 个节点就有 N 倍名额。
//
// ⚠️ 关于 304：契约给了 304 分支，这里也实现了，但**只在客户端主动发 If-None-Match
// 时才可能触发**。v2node 当前不对本端点发这个头（evidence §2 只确认了 /user 与 /config），
// 所以对它是空转。若将来某版 v2node 开始发送而又没处理好 304 的空响应体，
// 表现就是上面那条静默降级 —— 升级 v2node 时必须回验这一条。
func (s *Server) GetUniProxyAliveList(ctx context.Context, req gen.GetUniProxyAliveListRequestObject) (gen.GetUniProxyAliveListResponseObject, error) {
	auth, ok := middleware.NodeAuthFrom(ctx)
	if !ok {
		return nil, errNoNodeAuth
	}
	if !s.nodeIDMatches(ctx, auth, req.Params.NodeId) {
		return gen.GetUniProxyAliveList403JSONResponse{
			NodeForbiddenJSONResponse: gen.NodeForbiddenJSONResponse(errNodeIDMismatch()),
		}, nil
	}
	s.noteNodeAlive(ctx, auth)

	rows, err := s.db.ListAliveDeviceCounts(ctx)
	if err != nil {
		return nil, err
	}
	list := buildAliveList(rows)

	// 这里只能用内容哈希做 ETag：在线设备状态没有版本号，也不该为它建一个
	// （它每 60 秒被全量重写，版本号会每 60 秒 +1，等于没有 ETag 还多一张表）。
	// 序列化一遍算哈希的代价可接受：本响应只有 {uid: count}，比用户列表小一个量级。
	body, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	etag := httpx.StrongETag(body)
	if req.Params.IfNoneMatch != nil && httpx.MatchesETag(string(*req.Params.IfNoneMatch), etag) {
		return gen.GetUniProxyAliveList304Response{
			Headers: gen.NodeNotModifiedResponseHeaders{
				CacheControl: httpx.CacheControlNoStore,
				ETag:         etag,
			},
		}, nil
	}

	return gen.GetUniProxyAliveList200JSONResponse{
		Body: list,
		Headers: gen.GetUniProxyAliveList200ResponseHeaders{
			CacheControl: httpx.CacheControlNoStore,
			ETag:         etag,
		},
	}, nil
}

// ============================================================
// 形状转换（纯函数，全部有单测）
// ============================================================

// nodeProtocolSettings 是 servers.protocol_settings 这一列的 Go 形状。
//
// 🔴 **这里是一份白名单，不是透传。** 刻意不把 protocol_settings 整个塞进
// NodeConfig.AdditionalProperties：那样任何写进这一列的键都会被下发给节点，
// 包括将来某个人顺手存进去的内部字段。加协议字段必须改代码 —— 这是特性不是麻烦。
//
// ⚠️ reality_private_key / server_key / obfs_password 是**凭据**。
// 当前它们存在 servers.protocol_settings（jsonb）里，也就是明文落库。
// TODO(P2): 这三项应当只存 Secret Manager 的资源名，由本函数在下发时解引用 ——
// 现在的形态意味着「拿到只读数据库权限 = 拿到全部节点的连接凭据」。
type nodeProtocolSettings struct {
	ListenIP   *string `json:"listen_ip"`
	ServerName *string `json:"server_name"`

	// VLESS + REALITY
	RealityPrivateKey *string `json:"reality_private_key"`
	RealityShortID    *string `json:"reality_short_id"`
	RealityDest       *string `json:"reality_dest"`
	RealityXver       *string `json:"reality_xver"`

	// Hysteria2
	Obfs         *string `json:"obfs"`
	ObfsPassword *string `json:"obfs_password"`
	// 证书在**节点上**的来源与路径，原样下发到 tls_settings（v2node TlsSettings 的三个同名字段）。
	// 只用 cert_mode=file：签发在装机脚本层做（acme.sh + LE），/config 只告诉节点文件在哪。
	CertMode *string `json:"cert_mode"`
	CertFile *string `json:"cert_file"`
	KeyFile  *string `json:"key_file"`

	// Shadowsocks-2022
	Cipher    *string `json:"cipher"`
	ServerKey *string `json:"server_key"`

	// VLESS + XHTTP over CDN（应急通路）
	NetworkSettings map[string]any `json:"network_settings"`
}

// buildNodeConfig 把一行 servers 展开成 Xboard 形状的裸配置对象。
//
// 字段名全部照抄 Xboard `buildNodeConfig()`，包括 networkSettings 的驼峰
// —— 它与周围的蛇形命名不一致，但**不能改**。
// ⚠️ 例外：obfs_password 是**下划线**。Xboard 发连字符，但 v2node v0.4.3 读的是 obfs_password，
// 照抄 Xboard 的后果是服务端静默不开混淆（2026-09-02 真机实测，见 openapi 该字段的说明）。
func buildNodeConfig(row dbgen.GetServerConfigRow) (gen.NodeConfig, error) {
	var ps nodeProtocolSettings
	if len(row.ProtocolSettings) > 0 {
		if err := json.Unmarshal(row.ProtocolSettings, &ps); err != nil {
			return gen.NodeConfig{}, fmt.Errorf("解析 protocol_settings 失败 (server=%s): %w", row.Code, err)
		}
	}

	cfg := gen.NodeConfig{
		ListenIp:   ptrTo(settingOr(ps.ListenIP, defaultListenIP)),
		ServerPort: ptrTo(effectiveServerPort(row)),
		BaseConfig: &gen.NodeBaseConfig{
			PullInterval:           ptrTo(nodePullIntervalSeconds),
			PushInterval:           ptrTo(nodePushIntervalSeconds),
			DeviceOnlineMinTraffic: ptrTo(nodeDeviceOnlineMinTrafficKB),
			NodeReportMinTraffic:   ptrTo(nodeReportMinTraffic),
		},
	}

	switch row.Protocol {
	case dbgen.ServerProtocolVlessReality:
		var missing []string
		if isBlank(ps.RealityPrivateKey) {
			missing = append(missing, "reality_private_key")
		}
		if isBlank(ps.RealityShortID) {
			missing = append(missing, "reality_short_id")
		}
		if isBlank(ps.RealityDest) {
			missing = append(missing, "reality_dest")
		}
		if len(missing) > 0 {
			return gen.NodeConfig{}, missingSettings(row, missing)
		}
		cfg.Protocol = ptrTo("vless")
		cfg.Network = ptrTo("tcp")
		cfg.Flow = ptrTo(vlessRealityFlow)
		cfg.Tls = ptrTo(tlsModeReality)
		// 🔴 dest 必须拆成「主机名」+「端口」两个字段下发，不能整串发。
		// v2node 的 core/inbound.go 在 Reality 分支里拼的是
		//   fmt.Sprintf("%s:%s", TlsSettings.Dest, TlsSettings.ServerPort)
		// 整串发过去就变成 www.example.com:443:，xray 报
		//   please fill in a valid value for "target"
		// —— 报错指向 target，真正的原因是多了一个冒号。2026-09-01 首次真机接入时实测。
		// 库里的 reality_dest 仍然存 host:port（人读着方便，也是 ADR 的写法），
		// 拆分只发生在下发这一步。
		destHost, destPort := splitHostPortDefault(*ps.RealityDest, "443")
		cfg.TlsSettings = &gen.NodeTlsSettings{
			// server_name 缺省取 dest 的主机名：REALITY 的 SNI 与回落目标本来就该一致，
			// 分开配是给「回落到 A、伪装成 B」那种高级用法留的口子，不是常态。
			ServerName: ptrTo(settingOr(ps.ServerName, destHost)),
			PrivateKey: ps.RealityPrivateKey,
			ShortId:    ps.RealityShortID,
			Dest:       ptrTo(destHost),
			ServerPort: ptrTo(destPort),
			// xver 在 Xboard 里是**字符串**（不是数字），照抄不改类型。
			Xver: ptrTo(settingOr(ps.RealityXver, "0")),
		}

	case dbgen.ServerProtocolHysteria2:
		// 🔴 是 hysteria2 不是 Xboard 的 hysteria：v2node v0.4.3 只认 hysteria2，
		// 收到 hysteria 会报 unsupport protocol 并让**整个进程**退出（退出码 0，同机 REALITY 一起下线）。
		// 2026-09-01 之前这里写 hysteria，2026-09-02 首次启用 HY2 节点时实测撞上。
		cfg.Protocol = ptrTo("hysteria2")
		cfg.Version = ptrTo(hysteriaVersion)
		// 🔴 tls=1 必须显式下发：v2node 只在 Security==Tls 时才用 tls_settings 里的证书构造监听，
		// 缺了它 hysteria2 inbound 报 "transport/internet/hysteria: tls config is nil"，
		// 整个进程退出（退出码 0）。Hysteria2 的 TLS 不是可选项，所以这里无条件写 1。2026-09-02 真机实测。
		cfg.Tls = ptrTo(tlsModeTLS)
		// 🔴 Brutal 关闭：ADR 0004 裁定用 BBR。见 hysteriaUpMbps 的注释。
		cfg.UpMbps = ptrTo(hysteriaUpMbps)
		cfg.DownMbps = ptrTo(hysteriaDownMbps)
		cfg.ServerName = ptrTo(settingOr(ps.ServerName, row.Host))
		// obfs 与 obfs_password 必须**成对出现或都不出现**：
		// 只发 obfs 不发密码会让节点起一个 salamander 混淆但密码为空的监听，
		// 客户端连不上，且现象是「握手就断」这种最难查的形态。
		// 不配混淆是合法的 Hysteria2 配置，所以这里不报错，静默降级为不混淆。
		if !isBlank(ps.ObfsPassword) {
			cfg.Obfs = ptrTo(settingOr(ps.Obfs, defaultHysteriaObfs))
			cfg.ObfsPassword = ps.ObfsPassword
		}
		// 证书路径走 tls_settings。三项里任一有值就下发整组；v2node 对 cert_mode=file 要求
		// cert_file 与 key_file 都非空，缺一个它会拒绝启动 —— 那是正确的失败，这里不替它兜底。
		if !isBlank(ps.CertMode) || !isBlank(ps.CertFile) || !isBlank(ps.KeyFile) {
			cfg.TlsSettings = &gen.NodeTlsSettings{
				ServerName: cfg.ServerName,
				CertMode:   ps.CertMode,
				CertFile:   ps.CertFile,
				KeyFile:    ps.KeyFile,
			}
		}

	case dbgen.ServerProtocolShadowsocks2022:
		if isBlank(ps.ServerKey) {
			return gen.NodeConfig{}, missingSettings(row, []string{"server_key"})
		}
		cfg.Protocol = ptrTo("shadowsocks")
		// cipher / server_key 不在 NodeConfig 的具名字段里（契约只给了 VLESS 与 Hysteria
		// 两个分支的字段），走 additionalProperties。openapi 的 NodeConfig 明确
		// allows additional properties，所以这不是绕过契约。
		cfg.Set("cipher", settingOr(ps.Cipher, "2022-blake3-aes-128-gcm"))
		cfg.Set("server_key", *ps.ServerKey)

	case dbgen.ServerProtocolVlessXhttpCdn:
		if len(ps.NetworkSettings) == 0 {
			return gen.NodeConfig{}, missingSettings(row, []string{"network_settings"})
		}
		if isBlank(ps.ServerName) {
			return gen.NodeConfig{}, missingSettings(row, []string{"server_name"})
		}
		cfg.Protocol = ptrTo("vless")
		cfg.Network = ptrTo("xhttp")
		cfg.NetworkSettings = &ps.NetworkSettings
		cfg.Tls = ptrTo(tlsModeTLS)
		cfg.TlsSettings = &gen.NodeTlsSettings{ServerName: ps.ServerName}
		// 刻意不下发 flow：xtls-rprx-vision 只适用于裸 TCP，
		// 挂在 XHTTP 上会让节点直接拒绝启动。

	default:
		// 新增枚举值时这里会炸，而不是下发一份空配置。
		return gen.NodeConfig{}, fmt.Errorf("未知节点协议 %q (server=%s)", row.Protocol, row.Code)
	}

	return cfg, nil
}

// buildNodeUserList 把查询结果映射成冻结形状 {"users":[…]}。
//
// 🔴 **返回的切片必须非 nil。**
// v2node 的 JSON 回退路径是流式解析：它扫描到 "users" 键之后，
// 要求下一个 token 就是 `[`（evidence §4 的源码）。
// {"users": null} 会让它直接报 `expected "users" array` 并放弃整次拉取 ——
// 症状是**节点侧报错、面板侧一切正常**，最难定位的那一类。
// 这里的 make(…, 0, n) 是这条不变量的实际保证；sqlc 的 emit_empty_slices
// 只保证了 rows 非 nil，保证不到这一层。
//
// 第二个返回值是被跳过的行数（uuid 非法）。调用方必须记录它：
// 被跳过的用户在节点侧表现为「订阅显示正常但连不上」，用户无法自诊断。
func buildNodeUserList(rows []dbgen.ListAvailableUsersByServerRow) (gen.NodeUserList, int) {
	users := make([]gen.NodeUser, 0, len(rows))
	skipped := 0
	for _, r := range rows {
		if !r.Uuid.Valid {
			// pgtype.UUID.String() 在 !Valid 时返回空串。下发空 uuid 比跳过更危险：
			// 那等于给节点一个「空密码」的用户条目。
			skipped++
			continue
		}
		u := gen.NodeUser{Id: r.ID, Uuid: r.Uuid.String()}
		// NULL = 不限速 / 不限设备，对节点端的表达都是 0（照抄 Xboard 口径）。
		if r.SpeedLimitMbps != nil {
			u.SpeedLimit = *r.SpeedLimitMbps
		}
		if r.DeviceLimit != nil {
			u.DeviceLimit = *r.DeviceLimit
		}
		users = append(users, u)
	}
	return gen.NodeUserList{Users: users}, skipped
}

// buildAliveList 把在线计数映射成冻结形状 {"alive": {uid: count}}。
//
// 🔴 与 buildNodeUserList 同一类不变量：**map 必须非 nil**。
// nil map 序列化成 {"alive": null}，而 v2node 期望 map[int]int。
// 用 make 而不是 var 声明，就是为了这一条。
func buildAliveList(rows []dbgen.ListAliveDeviceCountsRow) gen.NodeAliveList {
	alive := make(map[string]int64, len(rows))
	for _, r := range rows {
		// key 是**字符串**形式的 user id：JSON 对象键必然是字符串，
		// 而 v2node 侧反序列化成 map[int]int 时会自己转回去。
		alive[strconv.FormatInt(r.UserID, 10)] = r.Alive
	}
	return gen.NodeAliveList{Alive: alive}
}

// trafficBatch 是解析并规范化之后的一批流量增量。
type trafficBatch struct {
	UserIDs []int64
	Up      []int64
	Down    []int64

	// Canonical 是幂等指纹的输入：按 user_id 升序排列的 "uid:up:down\n" 串。
	// 用规范化形式而不是原始请求体，见 trafficBatchKey 的注释。
	Canonical []byte

	// Dropped 是被丢弃的非法条目数，Truncated 表示超过上限后被截断。
	Dropped   int
	Truncated bool
}

// parseTrafficBatch 解析 {uid: [upload, download]}。
//
// 容错规则照抄 api-contract §3.5：非「长度 2 的非负整数数组」的条目**静默丢弃**，
// 不让整批失败。理由不是宽容，而是 v2node 不看状态码 ——
// 整批拒绝对它没有任何反馈，只是把一分钟的流量变成零。
//
// 额外收紧一条契约没写的：**key 必须是 user id 的规范十进制形式**。
// "01"、"+1"、" 1" 都会被 ParseInt 接受并解析成 1，于是同一个用户可以在一批里
// 出现多次。SQL 侧的 GROUP BY 能吃下重复，但规范化形式不唯一会让幂等指纹失效
// （同一批的两次上报算出两个不同的指纹 → 重放挡不住）。
func parseTrafficBatch(raw gen.NodeTrafficPushRequest) trafficBatch {
	type entry struct{ uid, up, down int64 }

	entries := make([]entry, 0, len(raw))
	b := trafficBatch{}
	for k, v := range raw {
		if len(entries) >= maxTrafficEntriesPerPush {
			b.Truncated = true
			break
		}
		uid, ok := parseUserID(k)
		if !ok || len(v) != 2 {
			b.Dropped++
			continue
		}
		up, down := v[0], v[1]
		if up < 0 || down < 0 {
			b.Dropped++
			continue
		}
		if up == 0 && down == 0 {
			// 全零条目对 user_traffic 是纯无效写（还会白白刷新 online_at），
			// 丢掉不算「丢弃」—— 它没有携带任何信息。
			continue
		}
		entries = append(entries, entry{uid, up, down})
	}

	// 排序有两个作用：① 让 Canonical 与 map 迭代顺序无关（Go 的 map 迭代是随机的，
	// 直接哈希原始请求体或哈希遍历顺序都会让同一批算出不同指纹）；
	// ② 让批内的行按 user_id 升序进 UPDATE，降低并发批次之间死锁的概率。
	slices.SortFunc(entries, func(a, c entry) int {
		switch {
		case a.uid < c.uid:
			return -1
		case a.uid > c.uid:
			return 1
		default:
			return 0
		}
	})

	b.UserIDs = make([]int64, 0, len(entries))
	b.Up = make([]int64, 0, len(entries))
	b.Down = make([]int64, 0, len(entries))
	canonical := make([]byte, 0, len(entries)*24)
	for _, e := range entries {
		b.UserIDs = append(b.UserIDs, e.uid)
		b.Up = append(b.Up, e.up)
		b.Down = append(b.Down, e.down)
		canonical = fmt.Appendf(canonical, "%d:%d:%d\n", e.uid, e.up, e.down)
	}
	b.Canonical = canonical
	return b
}

// trafficBatchKey 从「节点 + 批内容」推出幂等键。
//
// 🔴 **这里刻意偏离了「幂等键 = server_id + 上报窗口」的做法，理由必须写清楚。**
//
// 前提（evidence §5 / §7）：v2node 上报增量字节，**不带任何幂等键**；它不看状态码，
// 因此唯一的重复来源是「15 秒超时后 resty 重发同一批」，而重发的**内容与首发完全相同**。
//
// 用挂钟窗口（floor(now/60s)）当键的问题：重发发生在超时之后 15–30 秒，
// 有相当概率跨过窗口边界 —— 那正是它该被挡住的时候，却挡不住；
// 而把窗口放粗到能覆盖重发，又会把下一个真实批次误判成重放丢掉。
// 两个方向都错，且错的方式相反，没有能同时满足的窗口长度。
//
// 内容寻址没有这个问题：同一批 ⇒ 同一个键，跨多久都挡得住。
//
// **它的代价，明确承认**：同一节点在 24 小时（幂等键 TTL）内出现两个**逐字节相同**
// 的非空增量批时，第二批会被误判为重放丢掉。要撞上需要一批里每个用户的 up/down
// 都恰好相等 —— 多用户批实际上不可能，单用户小批（如某人两次都恰好传了 1024/8192 字节）
// 有极小概率。代价是少记一次几 KB，方向上是少收钱而不是多收钱。
//
// TODO(P2): 改成 Cloud Tasks 之后，batch_id 由我们自己在入队时生成，
// 这一整套推断就不需要了 —— 那才是根治。
func trafficBatchKey(serverID int64, canonical []byte) string {
	sum := sha256.Sum256(canonical)
	// 形如 bpnpush-12-<64 hex>，长度 ~75，落在幂等键的 [8,128] 与 ASCII 可见字符集内。
	return "bpnpush-" + strconv.FormatInt(serverID, 10) + "-" + hex.EncodeToString(sum[:])
}

// aliveBatch 是解析后的在线设备上报，两个数组按下标配对。
type aliveBatch struct {
	UserIDs   []int64
	DeviceIPs []string

	Dropped   int
	Truncated bool
}

// parseAliveBatch 解析 {uid: ["ip1","ip2"]}。
//
// 上报的是 **IP 列表不是计数**（evidence §5）。这里把它摊平成两个等长数组，
// 因为 BulkUpsertUserDeviceState 用 WITH ORDINALITY 按下标配对
// （sqlc v1.31 的 catalog 没有多参数 unnest）。
//
// IP 做两步规范化，都不是洁癖：
//   - Unmap()：::ffff:203.0.113.7 与 203.0.113.7 是同一台机器，
//     不规范化就会占两个设备名额，而设备名额是我们的定价杠杆；
//   - 去掉 zone（%eth0）：链路本地地址带 zone 时 inet 转换会失败，整条 SQL 报错。
//
// 排序理由同 parseTrafficBatch 的第 ② 条：让 upsert 的行序稳定。
func parseAliveBatch(raw gen.NodeAlivePushRequest) aliveBatch {
	type pair struct {
		uid int64
		ip  string
	}

	pairs := make([]pair, 0, len(raw))
	b := aliveBatch{}
	truncated := false
	for k, ips := range raw {
		uid, ok := parseUserID(k)
		if !ok {
			b.Dropped++
			continue
		}
		for _, rawIP := range ips {
			if len(pairs) >= maxAliveRowsPerPush {
				truncated = true
				break
			}
			addr, err := netip.ParseAddr(strings.TrimSpace(rawIP))
			if err != nil || !addr.IsValid() {
				b.Dropped++
				continue
			}
			pairs = append(pairs, pair{uid, addr.Unmap().WithZone("").String()})
		}
		if truncated {
			break
		}
	}
	b.Truncated = truncated

	slices.SortFunc(pairs, func(a, c pair) int {
		switch {
		case a.uid != c.uid:
			if a.uid < c.uid {
				return -1
			}
			return 1
		default:
			return strings.Compare(a.ip, c.ip)
		}
	})

	b.UserIDs = make([]int64, 0, len(pairs))
	b.DeviceIPs = make([]string, 0, len(pairs))
	for _, p := range pairs {
		b.UserIDs = append(b.UserIDs, p.uid)
		b.DeviceIPs = append(b.DeviceIPs, p.ip)
	}
	return b
}

// parseUserID 解析上报里的用户 id 键。
//
// 只接受规范十进制正整数：解析回去必须逐字节等于原串。
// 见 parseTrafficBatch 的注释 —— 非规范形式会破坏幂等指纹的唯一性。
func parseUserID(k string) (int64, bool) {
	uid, err := strconv.ParseInt(k, 10, 64)
	if err != nil || uid <= 0 {
		return 0, false
	}
	if strconv.FormatInt(uid, 10) != k {
		return 0, false
	}
	return uid, true
}

// ============================================================
// 小工具
// ============================================================

// ackTrafficBody 是 /push 成功时落进幂等表的响应体。
//
// 必须与 ackTraffic() 序列化出来的字节一致 —— 重放路径不该有重新序列化的机会。
var ackTrafficBody = []byte(`{"data":true}`)

func ackTraffic() gen.PushUniProxyTraffic200JSONResponse {
	return gen.PushUniProxyTraffic200JSONResponse{NodeAckJSONResponse: gen.NodeAckJSONResponse{Data: true}}
}

func ackAlive() gen.PushUniProxyAlive200JSONResponse {
	return gen.PushUniProxyAlive200JSONResponse{NodeAckJSONResponse: gen.NodeAckJSONResponse{Data: true}}
}

// nodeError 组装节点面的裸错误对象（**不套统一信封**）。
func nodeError(code gen.ErrorCode, msg string) gen.NodeError {
	return gen.NodeError{Code: &code, Message: &msg}
}

func errNodeIDMismatch() gen.NodeError {
	return nodeError(gen.NODEIDMISMATCH, "query 里的 node_id 与密钥绑定的节点不一致")
}

// nodeIDMatches 比对 query 里的 node_id 与密钥绑定的 server_id。
//
// 权威来源永远是**密钥**（server_keys.server_id），query 里的 node_id 只是节点自报。
// 不一致时返回 403 而不是静默忽略 —— 静默忽略会让「装机脚本把配置写到了哪台机器上」
// 这类事故无法被发现，而它的现象是「某台节点一直没有用户」，极难归因。
//
// node_id 缺省（nil）时视为通过：契约把它标成 optional，且它本来就不参与查询。
func (s *Server) nodeIDMatches(ctx context.Context, auth *middleware.NodeAuth, nodeID *gen.NodeIdQuery) bool {
	if nodeID == nil || *nodeID == auth.ServerID {
		return true
	}
	s.logger.ErrorContext(ctx, "节点自报 node_id 与密钥绑定不一致",
		"server_code", auth.ServerCode,
		"key_server_id", auth.ServerID,
		"query_node_id", *nodeID)
	return false
}

// touchPush 记一次 /push 心跳。失败不影响响应 —— 它只是运营视图。
func (s *Server) touchPush(ctx context.Context, auth *middleware.NodeAuth) {
	if err := s.db.TouchServerPush(ctx, auth.ServerID); err != nil {
		s.logger.WarnContext(ctx, "更新节点 last_push_at 失败",
			"server_code", auth.ServerCode, "err", err)
	}
}

// missingSettings 组装「protocol_settings 缺字段」的错误。
func missingSettings(row dbgen.GetServerConfigRow, fields []string) error {
	return fmt.Errorf("%w: server=%s protocol=%s 缺 %s",
		errNodeConfigIncomplete, row.Code, row.Protocol, strings.Join(fields, ", "))
}

// effectiveServerPort 取节点实际监听端口。
//
// server_port 为空表示「监听端口 = 客户端连的端口」；两者不同只出现在端口跳跃 /
// 前置转发的场景。下发 0 会让节点绑到随机端口，所以这里必须有兜底。
func effectiveServerPort(row dbgen.GetServerConfigRow) int32 {
	if row.ServerPort != nil && *row.ServerPort > 0 {
		return *row.ServerPort
	}
	return row.Port
}

// hostOf 取 "host:port" 里的 host；没有端口就原样返回。
// splitHostPortDefault 把 "host:port" 拆成两段；没有端口时用 def。
// 不用 net.SplitHostPort：它对没有端口的输入返回 error，而「只写主机名」在
// protocol_settings 里是合法写法（端口默认 443）。
func splitHostPortDefault(hostPort, def string) (string, string) {
	if i := strings.LastIndex(hostPort, ":"); i > 0 && !strings.Contains(hostPort[i+1:], "]") {
		if p := hostPort[i+1:]; p != "" {
			return hostPort[:i], p
		}
		return hostPort[:i], def
	}
	return hostPort, def
}

func hostOf(hostPort string) string {
	if i := strings.LastIndex(hostPort, ":"); i > 0 {
		return hostPort[:i]
	}
	return hostPort
}

func isBlank(s *string) bool { return s == nil || strings.TrimSpace(*s) == "" }

func settingOr(s *string, def string) string {
	if isBlank(s) {
		return def
	}
	return *s
}

func ptrTo[T any](v T) *T { return &v }
