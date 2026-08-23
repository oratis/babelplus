package ratelimit

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
)

// 本文件测的是「限流器在什么条件下放行」。它必须被测的理由与别处不同：
// 限流逻辑错了**不会报错**，只会静默地少限或多限 ——
// 少限的现象是「没有现象」（直到有人爆破成功），多限的现象是用户登录不了但日志一片正常。
//
// fakeCounter 逐条复刻 db/queries/ratelimit.sql 里那条 upsert 的语义
// （含「窗口过期就地重置」的 CASE 分支）。它当然不能证明那条 SQL 本身是对的 ——
// 那需要真库，见文件末尾 TestMaxWindowMatchesMigrationCheck 的说明。

// ============================================================
// 假实现
// ============================================================

type fakeKey struct {
	bucket  string
	subject string
}

type fakeRow struct {
	windowStart time.Time
	hits        int32
}

type fakeCounter struct {
	mu   sync.Mutex
	rows map[fakeKey]fakeRow

	// now 是「数据库的时钟」。测试用它推进窗口，而不是 sleep。
	now func() time.Time

	bumpErr  error
	sweepErr error

	bumps       int
	sweeps      int
	lastSweepIn dbgen.SweepExpiredRateLimitsParams
}

func newFakeCounter(now func() time.Time) *fakeCounter {
	return &fakeCounter{rows: map[fakeKey]fakeRow{}, now: now}
}

// BumpRateLimit 复刻 SQL：
//
//	hits = CASE WHEN window_start + window <= now() THEN 1 ELSE hits + 1 END
//
// 整个操作在锁里完成，对应 PostgreSQL 在 ON CONFLICT DO UPDATE 上的行锁 ——
// 这正是「并发递增不丢计数」的来源，所以假实现必须保留它。
func (f *fakeCounter) BumpRateLimit(_ context.Context, arg dbgen.BumpRateLimitParams) (dbgen.BumpRateLimitRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bumps++
	if f.bumpErr != nil {
		return dbgen.BumpRateLimitRow{}, f.bumpErr
	}

	now := f.now()
	win := time.Duration(arg.WindowSeconds) * time.Second
	k := fakeKey{arg.Bucket, string(arg.Subject)}

	row, ok := f.rows[k]
	switch {
	case !ok:
		row = fakeRow{windowStart: now, hits: 1}
	case !row.windowStart.Add(win).After(now): // window_start + window <= now()
		row = fakeRow{windowStart: now, hits: 1}
	default:
		row.hits++
	}
	f.rows[k] = row

	return dbgen.BumpRateLimitRow{
		Hits:      row.hits,
		ResetAt:   pgtype.Timestamptz{Time: row.windowStart.Add(win), Valid: true},
		ServerNow: pgtype.Timestamptz{Time: now, Valid: true},
	}, nil
}

func (f *fakeCounter) SweepExpiredRateLimits(_ context.Context, arg dbgen.SweepExpiredRateLimitsParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweeps++
	f.lastSweepIn = arg
	if f.sweepErr != nil {
		return 0, f.sweepErr
	}
	return 0, nil
}

// newTestLimiter 构造一个**不会自发清理**的 limiter：清理有自己的用例，
// 混进别的用例里只会让断言随机地多一次假调用。
func newTestLimiter(t *testing.T, db Counter) (*Limiter, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	l := New(db, "test-pepper", slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	l.sweepDue = func() bool { return false }
	return l, &buf
}

// ============================================================
// 窗口边界
// ============================================================

// TestAllow_WindowBoundary 钉住三件事，每一件错了都是静默的：
//
//  1. 第 limit 次仍然放行、第 limit+1 次才拒（差一错误会让限额比配置少一次）；
//  2. 窗口**未到**边界时不重置 —— 差一纳秒也不行；
//  3. 窗口到达边界时重置成 1，而不是继续累加（累加 = 一旦触顶永远触顶）。
func TestAllow_WindowBoundary(t *testing.T) {
	base := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	now := base
	db := newFakeCounter(func() time.Time { return now })
	l, _ := newTestLimiter(t, db)

	const limit = 3
	const window = time.Minute
	ctx := context.Background()

	for i := 1; i <= limit; i++ {
		ok, retry, err := l.Allow(ctx, "login_ip_1m", "1.2.3.4", limit, window)
		if err != nil {
			t.Fatalf("第 %d 次 Allow 返回错误: %v", i, err)
		}
		if !ok {
			t.Fatalf("第 %d 次（≤ limit=%d）应当放行，却被拒", i, limit)
		}
		if retry != 0 {
			t.Fatalf("第 %d 次放行时 retryAfter 应为 0，得到 %v", i, retry)
		}
	}

	ok, retry, err := l.Allow(ctx, "login_ip_1m", "1.2.3.4", limit, window)
	if err != nil {
		t.Fatalf("第 %d 次 Allow 返回错误: %v", limit+1, err)
	}
	if ok {
		t.Fatalf("第 %d 次（> limit=%d）必须被拒", limit+1, limit)
	}
	if retry != window {
		// 四次调用都发生在同一时刻，窗口一秒没走，剩余就是整个窗口。
		t.Fatalf("retryAfter 应为整个窗口 %v，得到 %v", window, retry)
	}

	// 差一纳秒不重置。
	now = base.Add(window - time.Nanosecond)
	if ok, _, _ := l.Allow(ctx, "login_ip_1m", "1.2.3.4", limit, window); ok {
		t.Fatalf("窗口还差 1ns 到期就重置了 —— 限额可以靠掐点绕开")
	}

	// 到达边界（SQL 的条件是 window_start + window <= now()）就重置。
	now = base.Add(window)
	ok, retry, err = l.Allow(ctx, "login_ip_1m", "1.2.3.4", limit, window)
	if err != nil {
		t.Fatalf("窗口边界处 Allow 返回错误: %v", err)
	}
	if !ok {
		t.Fatalf("窗口到期后第一次请求必须放行（否则触顶即永久封锁）")
	}
	if retry != 0 {
		t.Fatalf("放行时 retryAfter 应为 0，得到 %v", retry)
	}
}

// TestAllow_RetryAfterUsesServerClock 钉住「Retry-After 用数据库时钟算」。
//
// 假实现的时钟被故意设成与 time.Now() 相差一年。如果哪天有人把 retryAfter 改成
// `resetAt.Sub(time.Now())`，本用例会立刻炸 —— 而线上的现象只是
// Retry-After 大得离谱或直接被钳到下限，没有任何报错。
func TestAllow_RetryAfterUsesServerClock(t *testing.T) {
	skewed := time.Now().Add(365 * 24 * time.Hour)
	db := newFakeCounter(func() time.Time { return skewed })
	l, _ := newTestLimiter(t, db)
	ctx := context.Background()

	const window = 90 * time.Second
	if _, _, err := l.Allow(ctx, "b", "s", 1, window); err != nil {
		t.Fatalf("Allow 返回错误: %v", err)
	}
	ok, retry, err := l.Allow(ctx, "b", "s", 1, window)
	if err != nil {
		t.Fatalf("Allow 返回错误: %v", err)
	}
	if ok {
		t.Fatalf("第 2 次（limit=1）必须被拒")
	}
	if retry != window {
		t.Fatalf("retryAfter 应当只由 reset_at - server_now 决定（%v），得到 %v", window, retry)
	}
}

// TestRetryAfter_Clamps 钉住两个夹逼：下限 1 秒、上限一个窗口。
func TestRetryAfter_Clamps(t *testing.T) {
	base := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		row    dbgen.BumpRateLimitRow
		window time.Duration
		want   time.Duration
	}{
		{
			name: "只剩 1ms 也报 1s —— 报 0 会让客户端立刻重试再吃一个 429",
			row: dbgen.BumpRateLimitRow{
				ResetAt:   pgtype.Timestamptz{Time: base.Add(time.Millisecond), Valid: true},
				ServerNow: pgtype.Timestamptz{Time: base, Valid: true},
			},
			window: time.Minute,
			want:   time.Second,
		},
		{
			name: "窗口已过（时钟异常）也不返回负数",
			row: dbgen.BumpRateLimitRow{
				ResetAt:   pgtype.Timestamptz{Time: base.Add(-time.Hour), Valid: true},
				ServerNow: pgtype.Timestamptz{Time: base, Valid: true},
			},
			window: time.Minute,
			want:   time.Second,
		},
		{
			name: "剩余超过一个窗口（时钟异常）钳到窗口",
			row: dbgen.BumpRateLimitRow{
				ResetAt:   pgtype.Timestamptz{Time: base.Add(time.Hour), Valid: true},
				ServerNow: pgtype.Timestamptz{Time: base, Valid: true},
			},
			window: time.Minute,
			want:   time.Minute,
		},
		{
			name:   "两列都是 NULL（理论上不可能）退回整个窗口，绝不引导立刻重试",
			row:    dbgen.BumpRateLimitRow{},
			window: time.Minute,
			want:   time.Minute,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryAfter(tc.row, tc.window); got != tc.want {
				t.Fatalf("retryAfter = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// ============================================================
// 并发递增
// ============================================================

// TestAllow_ConcurrentIncrement 是本文件的头号回归。
//
// 「先 SELECT 再 UPDATE」写法在并发下必然丢计数：两个请求都读到 4、都写回 5，
// 而实际发生了 6 次。它的现象是**限额在有并发时才失效** ——
// 也就是只在被攻击时失效，平时测不出来。
//
// 这里断言的是：无论多少并发，被放行的次数**恰好**等于 limit。
func TestAllow_ConcurrentIncrement(t *testing.T) {
	fixed := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	db := newFakeCounter(func() time.Time { return fixed })
	l, _ := newTestLimiter(t, db)
	ctx := context.Background()

	const (
		limit    = 10
		requests = 200
	)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	start := make(chan struct{})
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, _, err := l.Allow(ctx, "login_email_1m", "a@example.com", limit, time.Minute)
			if err != nil {
				t.Errorf("Allow 返回错误: %v", err)
				return
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if allowed != limit {
		t.Fatalf("%d 个并发请求里放行了 %d 个，必须恰好是 limit=%d", requests, allowed, limit)
	}
	if db.bumps != requests {
		t.Fatalf("每次 Allow 必须恰好一次 upsert：%d 次调用产生了 %d 次", requests, db.bumps)
	}
}

// TestAllow_SubjectsAreIndependent 钉住「不同 subject 各算各的」。
// 错了的现象是所有人共用一个计数器 —— 一个人触顶，全站被拒。
func TestAllow_SubjectsAreIndependent(t *testing.T) {
	fixed := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	db := newFakeCounter(func() time.Time { return fixed })
	l, _ := newTestLimiter(t, db)
	ctx := context.Background()

	if ok, _, _ := l.Allow(ctx, "login_ip_1m", "1.1.1.1", 1, time.Minute); !ok {
		t.Fatalf("1.1.1.1 的第 1 次必须放行")
	}
	if ok, _, _ := l.Allow(ctx, "login_ip_1m", "1.1.1.1", 1, time.Minute); ok {
		t.Fatalf("1.1.1.1 的第 2 次必须被拒")
	}
	if ok, _, _ := l.Allow(ctx, "login_ip_1m", "2.2.2.2", 1, time.Minute); !ok {
		t.Fatalf("2.2.2.2 不该受 1.1.1.1 的计数影响")
	}
	// 同一个 subject、不同 bucket 也必须互不影响（否则 1m 与 1h 两条规则会互相覆盖）。
	if ok, _, _ := l.Allow(ctx, "login_ip_1h", "1.1.1.1", 1, time.Hour); !ok {
		t.Fatalf("同一 IP 在另一个 bucket 里不该受影响")
	}
}

// ============================================================
// 失败模式
// ============================================================

// TestAllow_FailsOpen 钉住包注释里那条决定：数据库出错时**放行**。
//
// 反向的实现（失败关闭）在代码里同样只有一行差别，而它的后果是
// 「限流表一出问题，全站没人能登录」。这条断言就是防那一行被改掉。
func TestAllow_FailsOpen(t *testing.T) {
	db := newFakeCounter(time.Now)
	db.bumpErr = errors.New("connection refused")
	l, logs := newTestLimiter(t, db)

	ok, retry, err := l.Allow(context.Background(), "login_ip_1m", "1.2.3.4", 1, time.Minute)
	if !ok {
		t.Fatalf("数据库失败时必须失败开放（放行），却拒绝了请求")
	}
	if err == nil {
		t.Fatalf("失败开放不等于吞掉错误：err 必须回传给调用方")
	}
	if retry != 0 {
		t.Fatalf("放行时 retryAfter 应为 0，得到 %v", retry)
	}
	// 降级必须留下可建指标的痕迹，否则「本该限流却没限」在监控上完全不可见。
	if !strings.Contains(logs.String(), "bp_ratelimit_degraded") {
		t.Fatalf("降级时必须写一条 bp_ratelimit_degraded 日志，实际日志：%s", logs.String())
	}
}

// TestAllow_RejectsBadWindow 钉住「窗口必须落在 0013 的 CHECK 范围内」，
// 且这种编程错误同样**失败开放** —— 配错一个桶不该让端点不可用。
func TestAllow_RejectsBadWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window time.Duration
	}{
		{"零窗口", 0},
		{"不足一秒（取整后是 0，会撞 CHECK）", 500 * time.Millisecond},
		{"超过 MaxWindow", MaxWindow + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newFakeCounter(time.Now)
			l, _ := newTestLimiter(t, db)
			ok, _, err := l.Allow(context.Background(), "b", "s", 1, tc.window)
			if !errors.Is(err, ErrWindowOutOfRange) {
				t.Fatalf("期望 ErrWindowOutOfRange，得到 %v", err)
			}
			if !ok {
				t.Fatalf("配置错误也必须失败开放")
			}
			if db.bumps != 0 {
				t.Fatalf("窗口非法时不该发出 upsert（会撞 CHECK 约束）")
			}
		})
	}
}

// ============================================================
// 清理
// ============================================================

// TestMaybeSweep 钉住三件事：抽样开关生效、参数与迁移里的上限一致、
// 以及清理失败**不影响放行判定**（它是后台性质的）。
func TestMaybeSweep(t *testing.T) {
	fixed := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	t.Run("抽样未命中时不清理", func(t *testing.T) {
		db := newFakeCounter(func() time.Time { return fixed })
		l, _ := newTestLimiter(t, db) // sweepDue 恒 false
		if _, _, err := l.Allow(context.Background(), "b", "s", 10, time.Minute); err != nil {
			t.Fatal(err)
		}
		if db.sweeps != 0 {
			t.Fatalf("抽样未命中却清理了 %d 次", db.sweeps)
		}
	})

	t.Run("抽样命中时按 MaxWindow 与 sweepBatch 清理", func(t *testing.T) {
		db := newFakeCounter(func() time.Time { return fixed })
		l, _ := newTestLimiter(t, db)
		l.sweepDue = func() bool { return true }
		if _, _, err := l.Allow(context.Background(), "b", "s", 10, time.Minute); err != nil {
			t.Fatal(err)
		}
		if db.sweeps != 1 {
			t.Fatalf("期望清理 1 次，实际 %d 次", db.sweeps)
		}
		if got := db.lastSweepIn.MaxWindowSeconds; got != int32(MaxWindow/time.Second) {
			t.Fatalf("清理阈值必须是 MaxWindow=%v，得到 %d 秒", MaxWindow, got)
		}
		if db.lastSweepIn.Batch != sweepBatch {
			t.Fatalf("清理必须封顶在 sweepBatch=%d，得到 %d", sweepBatch, db.lastSweepIn.Batch)
		}
	})

	t.Run("清理失败不影响放行判定", func(t *testing.T) {
		db := newFakeCounter(func() time.Time { return fixed })
		db.sweepErr = errors.New("statement timeout")
		l, _ := newTestLimiter(t, db)
		l.sweepDue = func() bool { return true }
		ok, _, err := l.Allow(context.Background(), "b", "s", 1, time.Minute)
		if err != nil {
			t.Fatalf("清理失败不该变成 Allow 的错误: %v", err)
		}
		if !ok {
			t.Fatalf("清理失败不该影响放行")
		}
	})
}

// ============================================================
// subject 摘要
// ============================================================

// TestDigest 钉住「明文不进库」这条硬要求，以及桶间不可关联。
func TestDigest(t *testing.T) {
	l := New(newFakeCounter(time.Now), "pepper-A", slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	const email = "victim@example.com"

	d := l.digest("login_email_1m", email)
	if len(d) != 32 {
		t.Fatalf("摘要应为 32 字节 sha256，得到 %d", len(d))
	}
	if bytes.Contains(d, []byte(email)) || bytes.Contains(d, []byte("example.com")) {
		t.Fatalf("摘要里出现了明文片段")
	}
	if !bytes.Equal(d, l.digest("login_email_1m", email)) {
		t.Fatalf("同样的输入必须得到同样的摘要，否则计数根本对不上")
	}
	if bytes.Equal(d, l.digest("login_email_1h", email)) {
		t.Fatalf("bucket 必须参与摘要 —— 否则拿到库的人能把不同桶里的同一个人对上")
	}

	other := New(newFakeCounter(time.Now), "pepper-B", slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if bytes.Equal(d, other.digest("login_email_1m", email)) {
		t.Fatalf("pepper 必须参与摘要，否则它对离线字典攻击毫无作用")
	}
}

// ============================================================
// 与迁移的一致性
// ============================================================

// TestMaxWindowMatchesMigrationCheck 把 Go 常量与 SQL 的 CHECK 钉在一起。
//
// 这两处是同一条不变量的两处表述，而它们分居两个文件、用两种语言写。
// 改了 Go 常量却忘了改 CHECK 的现象是：新桶的 upsert 撞 23514 → 限流器降级 →
// 失败开放 → **该端点静默地完全没有限流**。本用例是唯一能提前发现它的地方。
//
// ⚠️ 本用例证明不了那条 SQL 本身的行为（窗口重置、原子递增），
// 那需要真 Postgres。仓库当前没有集成测试装置，`make check` 也不起库 ——
// 已在报告里登记为「未做到」。
func TestMaxWindowMatchesMigrationCheck(t *testing.T) {
	const path = "../../db/migrations/0013_rate_limit.up.sql"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到迁移文件 %s: %v", path, err)
	}
	want := "CHECK (window_seconds BETWEEN 1 AND " + strconv.Itoa(int(MaxWindow/time.Second)) + ")"
	if !strings.Contains(string(b), want) {
		t.Fatalf("0013 里没有找到与 MaxWindow=%v 对应的 %q", MaxWindow, want)
	}
}
