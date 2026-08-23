// Package ratelimit 实现 api-contract.md §10.2 的「精确档」限流。
//
// 计数落在 Postgres 的 UNLOGGED 表 rate_limit 上（migration 0013），
// 递增靠一条 INSERT … ON CONFLICT DO UPDATE 完成。
//
// # 为什么不是进程内令牌桶
//
// Cloud Run `--max-instances=8`。进程内计数的实际上限 = 配置值 × 实例数，
// 最坏 8 倍。api-contract §10.2 显式接受了这 8 倍放大 —— 但只对「防雪崩」类端点。
// 本包服务的是另外两类：
//
//   - **凭据爆破**（login）：5/min 变成 40/min 是真实的安全损失；
//   - **邮件配额消耗**（email-code / forgot）：AWS SES 退信率 ≥ 5% 进审查、
//     ≥ 10% 可能暂停发信，而邮件是 ADR 0002 裁定的唯一失联恢复通道。
//
// 这两类不能用近似档。而买 Redis（Memorystore $35.77/月，比整个数据库贵 3.7 倍）
// 已被 ADR 0005 §8 否决，于是只剩这张表。
//
// # 🔴 失败模式：**失败开放**（fail-open）
//
// 数据库不可用时 Allow 返回 allowed=true。这是本包最重要的一个决定，理由三条：
//
//  1. **限流器故障导致全站不可登录，比短暂失去限流更糟。** 前者是 100% 的用户
//     立刻受影响且我们自己造成；后者只在同一时刻恰好有人在爆破时才有代价。
//
//  2. **在「数据库整体不可用」这个主场景里，失败关闭一分钱也买不到。**
//     login / forgot / email-code 每一条都要读写业务表，DB 挂了它们本来就会 500。
//     失败关闭真正改变行为的只有「唯独 rate_limit 这张表出问题」的偏门场景
//     （语句超时、UNLOGGED 表崩溃后被 TRUNCATE 的瞬间、将来误把查询路由到只读副本）——
//     而这些恰恰是最可能误伤而不是最可能有攻击的时刻。
//
//  3. 失败**必须可见**：每一次降级都写一条固定文案 `bp_ratelimit_degraded` 的
//     ERROR 日志。指标缺席型告警看不见「本该限流却没限」，只有这条日志能。
//     ⚠️ 这条日志对应的 log-based metric **尚未在 GCP 上创建**
//     （monitoring.md §3.2 已登记为未建）。在它被创建之前，失败开放是**静默**的。
//
// 反方意见记录在案：对 forgot / email-code 而言，失败开放意味着 SES 退信率可能被打上去，
// 而那是外部机构对我们的判罚，不是我们自己能撤销的。之所以仍然选失败开放，
// 是因为这两个端点还各自有一层**不依赖本表**的计数（auth.go 里基于
// email_verifications 历史行的 per email 3/h、per IP 10/h）—— 那一层跑在普通表上，
// 不会随 UNLOGGED 表一起丢，也不会因为本包失败而失效。两层的失败模式是独立的。
package ratelimit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	dbgen "github.com/oratis/babelplus/api/db/gen"
)

// MaxWindow 是任何 bucket 允许的最长窗口。
//
// 🔴 这个值与 migration 0013 的 `CHECK (window_seconds BETWEEN 1 AND 3600)`
// 是**同一条不变量的两处表述**，改一处必须改另一处。
// 它同时是清理语句的正确性前提：清理删的是「window_start 比 MaxWindow 还老」的行，
// 只有在没有任何桶的窗口超过 MaxWindow 时，这个粗筛才不会误删活跃窗口。
const MaxWindow = time.Hour

const (
	// sweepBatch 单次清理的行数上限。清理跑在用户请求路径上，必须封顶。
	sweepBatch = 500

	// sweepOneIn 抽样分母：平均每 sweepOneIn 次 Allow 触发一次清理。
	//
	// 为什么是抽样而不是定时任务：Cloud Run 会缩到 0，进程内不能加 ticker
	// （加了就必须常开 min-instances，成本模型立刻不成立 —— 与
	// idempotency_keys 清理面对的是同一堵墙）。抽样的好处是**自适应**：
	// 清理频率正比于请求量，而死行的产生速率也正比于请求量。
	// 没有流量时不清理，也不需要清理。
	sweepOneIn = 64

	// minRetryAfter 是 Retry-After 的下限。窗口只剩几毫秒时返回 0
	// 会让客户端立刻重试并再吃一个 429，白白多一轮往返。
	minRetryAfter = time.Second
)

// ErrWindowOutOfRange 表示调用方给了一个 0013 的 CHECK 不接受的窗口。
var ErrWindowOutOfRange = errors.New("限流窗口超出 [1s, MaxWindow] 范围")

// Counter 是本包需要的最小数据库能力。
//
// 收窄成接口而不是直接吃 *store.Store：单测要能塞假实现，
// 而窗口边界与并发递增这两件事必须有测试（它们错了不会报错，只会静默漏限流）。
type Counter interface {
	BumpRateLimit(ctx context.Context, arg dbgen.BumpRateLimitParams) (dbgen.BumpRateLimitRow, error)
	SweepExpiredRateLimits(ctx context.Context, arg dbgen.SweepExpiredRateLimitsParams) (int64, error)
}

// Limiter 是限流器。零值不可用，必须走 New。
type Limiter struct {
	db     Counter
	key    []byte
	logger *slog.Logger

	// sweepDue 决定这一次 Allow 要不要顺带清理。抽出来是为了让测试确定化 ——
	// 用真随机的话，「清理失败不影响放行」这条断言会有 63/64 的概率什么都没测到。
	sweepDue func() bool
}

// New 构造 Limiter。
//
// pepper 用于把明文 subject（IP / 邮箱）变成入库的摘要，见 digest。
func New(db Counter, pepper string, logger *slog.Logger) *Limiter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Limiter{
		db:       db,
		key:      []byte(pepper),
		logger:   logger,
		sweepDue: func() bool { return rand.IntN(sweepOneIn) == 0 },
	}
}

// Allow 记一次命中并判断是否放行。
//
// subject 传**明文**（IP 字符串或归一化后的邮箱），哈希由本函数负责 ——
// 这个分工是刻意的：把哈希留给调用方，迟早有一个调用点直接把邮箱传进来，
// 而现象是「数据库里多了一份可枚举的邮箱名单」，没有任何报错。
//
// 返回 (allowed, retryAfter, err)：
//   - allowed=false 时 retryAfter 是当前窗口的剩余时间（≥ 1s），调用方必须把它
//     写进 `Retry-After` 头；
//   - err != nil 时 **allowed 恒为 true**（失败开放，理由见包注释）。
//     调用方可以忽略 err —— 本函数已经记过日志了；返回它只是为了让调用方
//     有机会补充自己的上下文。**调用方绝不能在 err != nil 时拒绝请求。**
func (l *Limiter) Allow(ctx context.Context, bucket, subject string, limit int, window time.Duration) (bool, time.Duration, error) {
	secs := int32(window / time.Second)
	if secs < 1 || time.Duration(secs)*time.Second > MaxWindow {
		// 编程错误，不是运行时故障。提前拦住是为了让现场说得清楚：
		// 交给 0013 的 CHECK 去拒，报出来的只是一句 23514 约束冲突，
		// 谁也看不出是「哪个桶配了多长的窗口」。
		err := fmt.Errorf("%w: bucket=%s window=%s", ErrWindowOutOfRange, bucket, window)
		l.degraded(ctx, bucket, err)
		return true, 0, err
	}

	row, err := l.db.BumpRateLimit(ctx, dbgen.BumpRateLimitParams{
		Bucket:        bucket,
		Subject:       l.digest(bucket, subject),
		WindowSeconds: secs,
	})
	if err != nil {
		l.degraded(ctx, bucket, err)
		return true, 0, err
	}

	l.maybeSweep(ctx)

	if int(row.Hits) <= limit {
		return true, 0, nil
	}
	return false, retryAfter(row, window), nil
}

// retryAfter 用**数据库的时钟**算窗口剩余时间。
//
// 不用 time.Now()：应用进程与 Cloud SQL 之间的时钟差会原样变成 Retry-After 的误差，
// 而客户端是照着这个头退避的 —— 偏小则它提前重试再吃一个 429，
// 偏大则它比必要更久地退避。两个方向都只有坏处。
func retryAfter(row dbgen.BumpRateLimitRow, window time.Duration) time.Duration {
	if !row.ResetAt.Valid || !row.ServerNow.Valid {
		// 理论上不会发生（两列都是 NOT NULL 表达式）。真发生了就退回窗口长度，
		// 这是一个**偏保守但绝不会把客户端引向立刻重试**的兜底。
		return window
	}
	d := row.ResetAt.Time.Sub(row.ServerNow.Time)
	if d < minRetryAfter {
		return minRetryAfter
	}
	if d > window {
		// 窗口刚被重置时理论上恰好等于 window；再大只能是时钟异常。
		return window
	}
	return d
}

// digest 把明文 subject 变成入库的摘要。
//
// 三个决定各有理由：
//
//  1. **哈希而不是明文。** 邮箱明文落库等于凭空多出一份「谁在什么时候试过登录」
//     的可枚举名单，而 rate_limit 是一张运维排障时谁都可能 select 的易失表，
//     它的实际访问约束比 users 弱。IP 一并哈希是顺带的（也让本表不必进
//     「哪些表含个人数据」的清单）。
//
//  2. **HMAC 而不是裸 sha256(pepper || subject)。** 邮箱与 IP 都是低熵、可枚举的输入，
//     哈希本身挡不住字典攻击 —— 真正起作用的是 pepper 不落库。
//     用 HMAC 是为了不给「长度扩展」之类的构造留任何余地，成本为零。
//
//  3. **pepper 复用 SessionSigningKey，但做了域分隔**（前缀 bp-ratelimit\x00）。
//     本可以新增一个 BP_RATE_LIMIT_PEPPER，但那意味着新 secret、改部署脚本、
//     改 config 的必填项 —— 而 config.Load 是 fail-closed 的，漏配一个环境变量
//     直接起不来。域分隔已经让两处摘要无法互相对照，收益与新 secret 相同。
//
// bucket 也进摘要：同一个 IP 在不同桶里是不同的 subject，
// 拿到库的人无法把「登录被限的那个人」与「发码被限的那个人」对上。
func (l *Limiter) digest(bucket, subject string) []byte {
	m := hmac.New(sha256.New, l.key)
	m.Write([]byte("bp-ratelimit\x00"))
	m.Write([]byte(bucket))
	m.Write([]byte{0})
	m.Write([]byte(subject))
	return m.Sum(nil)
}

// maybeSweep 抽样触发一次过期行清理。
//
// **同步**执行，不起 goroutine。中间件里那两处 `go func()`（TouchKeyLastUsed、
// touchToken）写的是运营字段，丢了不影响任何判断；清理不一样 —— 它失败会让表
// 无界增长，必须被同步地看见（错误日志 + 这一次请求真的变慢）。
// 代价是 1/sweepOneIn 的请求多一次 DELETE 的往返，而 DELETE 有 LIMIT 封顶。
func (l *Limiter) maybeSweep(ctx context.Context) {
	if !l.sweepDue() {
		return
	}
	n, err := l.db.SweepExpiredRateLimits(ctx, dbgen.SweepExpiredRateLimitsParams{
		MaxWindowSeconds: int32(MaxWindow / time.Second),
		Batch:            sweepBatch,
	})
	if err != nil {
		// 不升级成请求失败：清理是后台性质的，删不掉只是表变大。
		l.logger.WarnContext(ctx, "清理过期限流行失败（rate_limit 表会继续增长）", "err", err)
		return
	}
	if n == sweepBatch {
		// 一次删满说明积压超过单批上限。抽样频率跟得上的话这条应当罕见；
		// 长期出现意味着 sweepOneIn / sweepBatch 需要重新算账。
		l.logger.InfoContext(ctx, "限流表清理达到单批上限", "deleted", n, "batch", sweepBatch)
	}
}

// degraded 记录一次「限流器自身失效」。
//
// 文案固定为 bp_ratelimit_degraded 且**就是指标名**：日志过滤器写
// `jsonPayload.message="bp_ratelimit_degraded"` 就够，不会因为谁改了一句中文措辞
// 而静默失配。同样的做法见 handler 里的 bp_node_alive。
func (l *Limiter) degraded(ctx context.Context, bucket string, err error) {
	l.logger.ErrorContext(ctx, "bp_ratelimit_degraded", "bucket", bucket, "err", err)
}
