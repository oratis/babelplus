package handler

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/oratis/babelplus/api/internal/middleware"
)

// bp_node_alive —— 节点心跳的结构化日志（roadmap B42）。
//
// # 它为什么必须存在，而且必须在节点上线**之前**存在
//
// monitoring.md §5 第 1 条告警是 **metric absence**：某个 node_id 的 time series
// 连续 5 分钟没有新数据就报警。而 metric absence 有一条致命前提 ——
// **它需要那条 time series 曾经有过数据**。一个从未上报过的节点在监控眼里
// 根本不存在，于是它挂了也不会有任何告警。
// 所以这条日志不是「上线后补上的观测项」，它是建节点流程的一步
// （ADR 0007 要求首次上报后人工确认 time series 已出现，告警才算 armed）。
//
// # 为什么不能靠 access log
//
// monitoring.md §3.2 写明了：access log 里 `POST /alive` 的 200 **区分不出哪个节点**，
// 而我们要的恰恰是逐节点的缺失告警（不按 node_id 分组的话，
// 「8 个节点挂了 1 个」根本不会触发缺失，总数仍然 > 0）。
// 节点凭据走 query string，也不该为了识别节点去解析 URL 里的凭据。
//
// # 字段与文案的约定
//
//   - 日志文案**就是指标名** `bp_node_alive`。过滤器写
//     `jsonPayload.message="bp_node_alive"` 即可，不会因为谁改了一句中文措辞而静默失配。
//     （monitoring.md 里 bp_subscribe_404 那条就是靠匹配中文日志行，
//     它的脆弱性正是这里不重蹈的原因。）
//   - 标签字段名是 `node_id`，对齐 monitoring.md §5.1 告警 JSON 里的
//     `groupByFields: ["metric.label.node_id"]`。
//   - 值写成**字符串**而不是数字：log-based metric 的 label 本身就是字符串类型，
//     写成数字要多依赖一次隐式转换，而这一步出错的现象是「指标建出来了但没有标签」。
//
// 对应的 log-based metric **尚未在 GCP 上创建**（monitoring.md §3.2 登记为
// 「🔴 建不了 —— 需应用主动写日志」）。本次改动只解掉「建不了」这一半，
// 创建指标与告警策略仍然欠着。

// nodeAliveMessage 同时是日志文案与 log-based metric 名。改它 = 改指标名。
const nodeAliveMessage = "bp_node_alive"

// nodeAliveInterval 是**每个节点**两条心跳日志之间的最小间隔。
//
// 降频的账：v2node 每 60 秒轮询 4 个端点（config / user / alive / alivelist）
// 再加 push，10 节点就是约 50 条/分钟 = 7.2 万条/天。这些日志除了心跳没有别的用途，
// 而 Cloud Logging 按量计费、告警只需要「这 5 分钟里有没有」。
//
// 取 60 秒的依据是告警窗口：metric absence 的判定窗口是 **5 分钟**（monitoring.md §5），
// 60 秒的间隔在窗口里留下约 5 个采样点。再稀就没有余量了 ——
// 一次冷启动或一次网络抖动丢掉两三个点就会误报。
//
// ⚠️ 降频是**每实例**的。Cloud Run 最多 8 个实例，最坏情况下同一节点每分钟
// 仍可能出现 8 条（每个实例各自记一次）。这不影响告警正确性（absence 只看有没有），
// 只影响日志量的上界：10 节点 × 8 实例 × 1440 = 11.5 万条/天的最坏值，
// 而实际上节点的连接会长时间落在同一个实例上，远达不到。
// 要做到真正的全局降频就得跨实例共享状态，那又是一次数据库写 —— 为一条心跳日志
// 去写库，代价和收益完全不成比例。
const nodeAliveInterval = 60 * time.Second

// nodeAliveThrottle 是每节点的降频闸。零值可用。
//
// 刻意用普通 map + mutex 而不是 sync.Map：键的数量等于节点数（ADR 0006 §3.3 按 10 算），
// 争用几乎不存在，而普通 map 的行为更容易论证。
//
// 不做过期清理：一个被删除的节点会在这里留下一个 int64→time.Time 的条目，
// 而实例本身活不过一次缩容。为几十字节加一套清理逻辑是把复杂度花错了地方。
type nodeAliveThrottle struct {
	mu   sync.Mutex
	last map[int64]time.Time
}

// due 判断这个节点现在该不该再写一条心跳，并在返回 true 时记下时刻。
//
// 判断与记录必须在**同一次加锁**里完成：分成「先问再记」两步的话，
// 同一节点的并发请求会全部拿到 true，降频等于没有。
func (t *nodeAliveThrottle) due(nodeID int64, now time.Time, interval time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last = make(map[int64]time.Time)
	}
	if prev, ok := t.last[nodeID]; ok && now.Sub(prev) < interval {
		return false
	}
	t.last[nodeID] = now
	return true
}

// noteNodeAlive 记一次节点心跳（降频后）。
//
// # 调用位置：鉴权 + node_id 校验通过之后，业务逻辑之前
//
// 这个位置是刻意的，它决定了指标回答的是哪个问题。
// 放在这里，`bp_node_alive` 回答的是「**这个节点还能连上我们并且凭据有效吗**」，
// 而不是「这次请求成功了吗」。区别在故障时才显形：
//
//	数据库挂了 → 五个端点全部 500。
//	  · 按当前位置：心跳照常，只有 bp_api_5xx 告警响 → 值班去看 API/DB。✅
//	  · 若放在响应成功之后：8 个节点的心跳同时消失，
//	    「全部节点离线」+「5xx 飙升」两条告警一起响 → 值班先跑去查节点。❌
//
// 节点侧真正掉线时两种位置的表现是一样的（请求根本到不了我们）。
// 也就是说把它放在前面**只会**减少误导，不会漏报。
func (s *Server) noteNodeAlive(ctx context.Context, auth *middleware.NodeAuth) {
	if !s.nodeAlive.due(auth.ServerID, time.Now(), nodeAliveInterval) {
		return
	}
	s.logger.InfoContext(ctx, nodeAliveMessage,
		"node_id", strconv.FormatInt(auth.ServerID, 10),
		// server_code 只给人看（值班从告警里看到 node_id=7 时需要知道那是哪台）。
		// 它**不该**被做成指标标签 —— monitoring.md §3.2 只批准了 node_id 一个实体标签。
		"server_code", auth.ServerCode,
	)
}
