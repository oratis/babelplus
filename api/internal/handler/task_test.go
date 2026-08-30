package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/httpx"
)

// 九个内部定时任务的测试。
//
// 每个任务测三件事（本轮任务书的硬要求）：
//  1. 正常路径；
//  2. 幂等重入 —— Cloud Tasks 是 at-least-once，重复投递是常态不是异常；
//  3. 那条「不这么做会**静默**出错」的边界。第 3 条是这个文件存在的主要理由：
//     前两条错了会有人发现，第 3 条错了不会。
//
// 测试打的是吃窄接口的自由函数（runXxx），不是 handler 方法 ——
// Server.db 是具体类型 *store.Store，塞不了假实现（node.go 的第 1 条纪律）。
// handler 方法这一层能测、也只测**响应形状**：内部面必须回裸 `{"ok":true}`，
// 503 必须带 Retry-After。

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// ============================================================
// 1 · alive-gc
// ============================================================

type fakeAliveGc struct {
	deviceRows, totpRows, idemRows int64
	deviceErr, totpErr, idemErr    error
	calls                          []string
}

func (f *fakeAliveGc) CleanupStaleDeviceState(context.Context) (int64, error) {
	f.calls = append(f.calls, "device")
	return f.deviceRows, f.deviceErr
}

func (f *fakeAliveGc) CleanupUsedTotp(context.Context) (int64, error) {
	f.calls = append(f.calls, "totp")
	return f.totpRows, f.totpErr
}

func (f *fakeAliveGc) CleanupExpiredIdempotencyKeys(context.Context) (int64, error) {
	f.calls = append(f.calls, "idempotency")
	return f.idemRows, f.idemErr
}

func TestRunAliveGc(t *testing.T) {
	t.Run("正常路径：三条清理都跑，行数如实汇总", func(t *testing.T) {
		f := &fakeAliveGc{deviceRows: 12, totpRows: 3, idemRows: 7}
		res, err := runAliveGc(context.Background(), f, testLogger())
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if res.DeviceRows != 12 || res.TotpRows != 3 || res.IdempotencyRows != 7 {
			t.Fatalf("行数不对：%+v", res)
		}
	})

	t.Run("幂等重入：第二次跑没有副作用，行数为 0 也不是错误", func(t *testing.T) {
		f := &fakeAliveGc{}
		for i := range 3 {
			if _, err := runAliveGc(context.Background(), f, testLogger()); err != nil {
				t.Fatalf("第 %d 次不应报错：%v", i+1, err)
			}
		}
		if len(f.calls) != 9 {
			t.Fatalf("三次调用应当各跑三条清理，实际 %d 次", len(f.calls))
		}
	})

	// 🔴 静默边界 A：幂等键清理必须真的被调起来。
	//
	// httpx/idempotency.go 的 ErrIdempotencyKeyStale 注释逐字写着：
	// 「这不是理论边界：CleanupExpiredIdempotencyKeys 必须真的被定时调起来，
	//   否则 24 小时后开始出现无法解释的下单失败」。
	// 在这个任务接上它之前，仓库里没有任何代码调它 —— 而漏掉的表现是
	// 「某个用户的下单突然 409，重启也不好，第二天自己好了」。
	t.Run("静默边界：必须调 CleanupExpiredIdempotencyKeys（漏了会在 24 小时后变成无法解释的下单失败）", func(t *testing.T) {
		f := &fakeAliveGc{}
		if _, err := runAliveGc(context.Background(), f, testLogger()); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !slices.Contains(f.calls, "idempotency") {
			t.Fatal("没有调用 CleanupExpiredIdempotencyKeys")
		}
		if !slices.Contains(f.calls, "totp") {
			t.Fatal("没有调用 CleanupUsedTotp（0015 的注释点名要求由 /internal/tasks/* 清理）")
		}
	})

	// 🔴 静默边界 B：一条失败不能吃掉另外两条。
	// 「因为 TOTP 表清不掉所以在线态也不清」会把一张只增不减的小表，
	// 放大成设备数限制失效（user_device_state 不清 → 用户永远被判定为在线设备超限）。
	t.Run("静默边界：一条清理失败，其余两条照跑，错误合并上报", func(t *testing.T) {
		f := &fakeAliveGc{deviceRows: 5, totpErr: errors.New("boom"), idemRows: 9}
		res, err := runAliveGc(context.Background(), f, testLogger())
		if err == nil {
			t.Fatal("失败必须上报，否则 Scheduler 以为一切正常")
		}
		if res.DeviceRows != 5 || res.IdempotencyRows != 9 {
			t.Fatalf("其余两条没有照跑：%+v", res)
		}
	})
}

// ============================================================
// 2 · expire-check
// ============================================================

type fakeExpireCheck struct {
	expiring   []dbgen.SweepExpiredUsersRow
	packs      []dbgen.ExpireTrafficPacksRow
	bumpErr    error
	bumpedWith [][]int64
	// userRev 模拟 node_rev：group → 版本号，用来断言「节点真的会重新拉列表」。
	userRev map[int64]int64
}

func (f *fakeExpireCheck) SweepExpiredUsers(context.Context) ([]dbgen.SweepExpiredUsersRow, error) {
	out := f.expiring
	f.expiring = nil // 语句自带收敛：expiry_applied_at 已非 NULL 的行不会被再选中
	return out, nil
}

func (f *fakeExpireCheck) ExpireTrafficPacks(context.Context) ([]dbgen.ExpireTrafficPacksRow, error) {
	out := f.packs
	f.packs = nil // 同上：pack_expire_at 已置 NULL
	return out, nil
}

func (f *fakeExpireCheck) BumpUserRevByGroups(_ context.Context, groupIDs []int64) (int64, error) {
	f.bumpedWith = append(f.bumpedWith, slices.Clone(groupIDs))
	if f.bumpErr != nil {
		return 0, f.bumpErr
	}
	if f.userRev == nil {
		f.userRev = map[int64]int64{}
	}
	for _, g := range groupIDs {
		f.userRev[g]++
	}
	return int64(len(groupIDs)), nil
}

func TestRunExpireCheck(t *testing.T) {
	newFake := func() *fakeExpireCheck {
		return &fakeExpireCheck{
			expiring: []dbgen.SweepExpiredUsersRow{
				{ID: 1, GroupID: 10, Email: "a@example.com", ExpiredAt: ts(time.Now().Add(-time.Minute))},
				{ID: 2, GroupID: 10, Email: "b@example.com", ExpiredAt: ts(time.Now().Add(-time.Minute))},
				{ID: 3, GroupID: 20, Email: "c@example.com", ExpiredAt: ts(time.Now().Add(-time.Minute))},
			},
			packs:   []dbgen.ExpireTrafficPacksRow{{ID: 4, GroupID: 30, TransferEnable: 1 << 30}},
			userRev: map[int64]int64{10: 1, 20: 1, 30: 1},
		}
	}

	t.Run("正常路径：到期用户与到期加油包合并去重后一次 bump", func(t *testing.T) {
		f := newFake()
		res, err := runExpireCheck(context.Background(), f, testLogger())
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if res.ExpiredUsers != 3 || res.ExpiredPacks != 1 {
			t.Fatalf("扫描数量不对：%+v", res)
		}
		if len(f.bumpedWith) != 1 {
			t.Fatalf("应当只 bump 一次（分组去重后一条语句），实际 %d 次", len(f.bumpedWith))
		}
		got := slices.Clone(f.bumpedWith[0])
		slices.Sort(got)
		if !slices.Equal(got, []int64{10, 20, 30}) {
			t.Fatalf("bump 的分组不对：%v（期望 10/20/30，且分组 10 只出现一次）", got)
		}
	})

	// 🔴 这是本轮任务书点名的那一条：
	// 「到期不是写操作，没有任何业务写会触发它 —— 所以必须显式 bump user_rev」。
	// 不 bump 的现象：数据库里 expiry_applied_at 已经写上了，节点却因为 user_rev 没变
	// 一直收 304，于是**永远**不知道该把这个人踢掉。没有报错，没有告警。
	t.Run("静默边界：到期用户所在分组的 user_rev 必须真的前进（不前进 = 到期用户永远不从节点列表消失）", func(t *testing.T) {
		f := newFake()
		before := maps2(f.userRev)
		if _, err := runExpireCheck(context.Background(), f, testLogger()); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		for _, g := range []int64{10, 20, 30} {
			if f.userRev[g] <= before[g] {
				t.Fatalf("分组 %d 的 user_rev 没有前进（%d → %d）：节点不会重新拉用户列表",
					g, before[g], f.userRev[g])
			}
		}
	})

	t.Run("幂等重入：第二次跑命中 0 行，不再 bump", func(t *testing.T) {
		f := newFake()
		if _, err := runExpireCheck(context.Background(), f, testLogger()); err != nil {
			t.Fatalf("第一次不应报错：%v", err)
		}
		res, err := runExpireCheck(context.Background(), f, testLogger())
		if err != nil {
			t.Fatalf("第二次不应报错：%v", err)
		}
		if res.ExpiredUsers != 0 || res.ExpiredPacks != 0 || res.Groups != 0 {
			t.Fatalf("第二次不该有任何命中：%+v", res)
		}
		if len(f.bumpedWith) != 1 {
			t.Fatalf("第二次不该再 bump，实际累计 %d 次", len(f.bumpedWith))
		}
	})

	// bump 失败必须变成错误往上抛。吞掉它的后果与「压根没写 bump」完全一样，
	// 而且更隐蔽 —— 代码里明明有那一行。
	t.Run("静默边界：bump 失败必须上报，不能只记日志", func(t *testing.T) {
		f := newFake()
		f.bumpErr = errors.New("db down")
		if _, err := runExpireCheck(context.Background(), f, testLogger()); err == nil {
			t.Fatal("bump 失败被吞掉了：到期已标记但节点永远收不到")
		}
	})

	t.Run("没有到期用户时不调 bump（避免每 5 分钟白白让全站节点重拉一次）", func(t *testing.T) {
		f := &fakeExpireCheck{}
		if _, err := runExpireCheck(context.Background(), f, testLogger()); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if len(f.bumpedWith) != 0 {
			t.Fatalf("空扫描不该 bump，实际 %d 次", len(f.bumpedWith))
		}
	})
}

func maps2(m map[int64]int64) map[int64]int64 {
	out := make(map[int64]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ============================================================
// 3 · order-timeout
// ============================================================

type fakeOrderTimeout struct {
	// batches 是每一趟返回的行；模拟「积压」用多趟满批。
	batches [][]dbgen.ExpireTimedOutOrdersRow
	calls   int
	err     error
}

func (f *fakeOrderTimeout) ExpireTimedOutOrders(_ context.Context, _ int32) ([]dbgen.ExpireTimedOutOrdersRow, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if len(f.batches) == 0 {
		return nil, nil
	}
	out := f.batches[0]
	f.batches = f.batches[1:]
	return out, nil
}

func fullOrderBatch(n int, withAddress bool) []dbgen.ExpireTimedOutOrdersRow {
	rows := make([]dbgen.ExpireTimedOutOrdersRow, n)
	for i := range rows {
		addr := "TXxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
		rows[i] = dbgen.ExpireTimedOutOrdersRow{
			ID:                int64(i + 1),
			TradeNo:           "T" + string(rune('A'+i%26)),
			UserID:            int64(i + 1),
			FromStatus:        dbgen.OrderStatusPending,
			AddressWatchUntil: ts(time.Now().Add(25 * time.Hour)),
		}
		if withAddress {
			rows[i].PayAddress = &addr
		}
	}
	return rows
}

func TestRunOrderTimeout(t *testing.T) {
	t.Run("正常路径：关闭一批订单，带收款地址的计入监听窗口延长", func(t *testing.T) {
		f := &fakeOrderTimeout{batches: [][]dbgen.ExpireTimedOutOrdersRow{fullOrderBatch(3, true)}}
		res, err := runOrderTimeout(context.Background(), f, testLogger())
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if res.Orders != 3 || res.WatchExtended != 3 || res.Passes != 1 {
			t.Fatalf("结果不对：%+v", res)
		}
	})

	t.Run("幂等重入：第二次跑命中 0 行（订单已是 expired，语句 WHERE 挡住）", func(t *testing.T) {
		f := &fakeOrderTimeout{batches: [][]dbgen.ExpireTimedOutOrdersRow{fullOrderBatch(2, true)}}
		if _, err := runOrderTimeout(context.Background(), f, testLogger()); err != nil {
			t.Fatalf("第一次不应报错：%v", err)
		}
		res, err := runOrderTimeout(context.Background(), f, testLogger())
		if err != nil {
			t.Fatalf("第二次不应报错：%v", err)
		}
		if res.Orders != 0 {
			t.Fatalf("第二次不该再关任何订单：%+v", res)
		}
	})

	// 🔴 静默边界：满批必须继续下一趟。
	//
	// 只跑一趟就返回的话，积压永远清不完：那些「已到期但还没被关掉」的订单，
	// 它们的 address_watch_until 也就没有被顶到 ≥ 24 小时 ——
	// 而 chain-scan 的扫描范围正是靠这一列。结果是用户在倒计时结束前一秒付的钱
	// 没有任何人在监听那个地址（user-journey §7 判定为最不可挽回的失败模式）。
	t.Run("静默边界：满批要继续下一趟，否则积压订单的收款地址不会进入监听窗口", func(t *testing.T) {
		f := &fakeOrderTimeout{batches: [][]dbgen.ExpireTimedOutOrdersRow{
			fullOrderBatch(orderTimeoutBatchSize, true),
			fullOrderBatch(orderTimeoutBatchSize, true),
			fullOrderBatch(5, true),
		}}
		res, err := runOrderTimeout(context.Background(), f, testLogger())
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if res.Passes != 3 {
			t.Fatalf("应当跑满 3 趟（前两趟是满批），实际 %d 趟", res.Passes)
		}
		if want := orderTimeoutBatchSize*2 + 5; res.Orders != want {
			t.Fatalf("处理订单数 %d，期望 %d", res.Orders, want)
		}
	})

	t.Run("单次请求的时长可预测：趟数封顶，剩下的留给下一分钟", func(t *testing.T) {
		batches := make([][]dbgen.ExpireTimedOutOrdersRow, 0, orderTimeoutMaxPasses+3)
		for range orderTimeoutMaxPasses + 3 {
			batches = append(batches, fullOrderBatch(orderTimeoutBatchSize, false))
		}
		f := &fakeOrderTimeout{batches: batches}
		res, err := runOrderTimeout(context.Background(), f, testLogger())
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if res.Passes != orderTimeoutMaxPasses {
			t.Fatalf("趟数没有封顶：%d", res.Passes)
		}
	})
}

// ============================================================
// 4 · traffic-reset
// ============================================================

type fakeTrafficReset struct {
	due       []dbgen.ListUsersDueForResetRow
	planless  map[int64]bool // plan_id 为 NULL 的用户：AdvanceUserResetCycle 会 0 行
	calls     []string
	advanced  []int64
	auditRows []dbgen.InsertTrafficResetLogParams
}

func (f *fakeTrafficReset) SuspendResetForPlanlessUsers(context.Context) (int64, error) {
	f.calls = append(f.calls, "suspend")
	var n int64
	f.due = slices.DeleteFunc(f.due, func(r dbgen.ListUsersDueForResetRow) bool {
		if f.planless[r.ID] {
			n++
			return true
		}
		return false
	})
	return n, nil
}

func (f *fakeTrafficReset) ListUsersDueForReset(_ context.Context, _ int32) ([]dbgen.ListUsersDueForResetRow, error) {
	f.calls = append(f.calls, "list")
	return slices.Clone(f.due), nil
}

func (f *fakeTrafficReset) AdvanceUserResetCycle(_ context.Context, userID int64) (dbgen.AdvanceUserResetCycleRow, error) {
	f.calls = append(f.calls, "advance")
	if f.planless[userID] {
		// 真实语句是 `FROM plans p, cur WHERE p.id = u.plan_id` 的交叉连接，
		// plan_id 为 NULL 时匹配 0 行 → :one 报 ErrNoRows。
		return dbgen.AdvanceUserResetCycleRow{}, pgx.ErrNoRows
	}
	f.advanced = append(f.advanced, userID)
	// 推进之后 reset_at 落到未来，下一轮不会再被选中 —— 幂等就是这么来的。
	f.due = slices.DeleteFunc(f.due, func(r dbgen.ListUsersDueForResetRow) bool { return r.ID == userID })
	return dbgen.AdvanceUserResetCycleRow{
		ID: userID, ResetSeq: 2, ResetAt: ts(time.Now().Add(30 * 24 * time.Hour)),
		TransferEnablePlan: 100 << 30, TransferEnablePack: 7 << 30, TransferEnable: 107 << 30,
		OldU: 3 << 30, OldD: 90 << 30,
	}, nil
}

func (f *fakeTrafficReset) InsertTrafficResetLog(_ context.Context, arg dbgen.InsertTrafficResetLogParams) (dbgen.TrafficResetLog, error) {
	f.calls = append(f.calls, "audit")
	f.auditRows = append(f.auditRows, arg)
	return dbgen.TrafficResetLog{ID: int64(len(f.auditRows))}, nil
}

func TestRunTrafficReset(t *testing.T) {
	method := dbgen.ResetMethodMonthlyOnOrderDay

	t.Run("正常路径：逐个推进周期并写审计", func(t *testing.T) {
		f := &fakeTrafficReset{due: []dbgen.ListUsersDueForResetRow{
			{ID: 1, ResetTrafficMethod: &method},
			{ID: 2, ResetTrafficMethod: &method},
		}}
		res, err := runTrafficReset(context.Background(), f, testLogger())
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if res.Reset != 2 || res.Skipped != 0 {
			t.Fatalf("结果不对：%+v", res)
		}
		if len(f.auditRows) != 2 {
			t.Fatalf("审计行数 %d，期望 2", len(f.auditRows))
		}
	})

	// 🔴 静默边界 A（ADR 0013 ③）：审计必须同时记下总额与加油包分量。
	// 只留总额的话，「加油包被吃掉了还是结转了」正好落在总额里看不见 ——
	// 而 §5.3 那个静默失败（调用顺序错 → 加油包只增不减）事后只能靠这两个数反推。
	t.Run("静默边界：审计要同时落 new_transfer_enable 与 new_transfer_enable_pack", func(t *testing.T) {
		f := &fakeTrafficReset{due: []dbgen.ListUsersDueForResetRow{{ID: 1, ResetTrafficMethod: &method}}}
		if _, err := runTrafficReset(context.Background(), f, testLogger()); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		a := f.auditRows[0]
		if a.NewTransferEnable != 107<<30 {
			t.Fatalf("审计的总额取错了：%d", a.NewTransferEnable)
		}
		if a.NewTransferEnablePack != 7<<30 {
			t.Fatalf("审计漏了加油包结转分量：%d（事后无法判断结转算没算对）", a.NewTransferEnablePack)
		}
		if a.OldU != 3<<30 || a.OldD != 90<<30 {
			t.Fatalf("审计的旧用量取错了：u=%d d=%d", a.OldU, a.OldD)
		}
		if a.TriggerSource != "scheduler" {
			t.Fatalf("trigger_source 应为 scheduler，实际 %q", a.TriggerSource)
		}
	})

	t.Run("幂等重入：推进后 reset_at 落到未来，第二次跑不再命中", func(t *testing.T) {
		f := &fakeTrafficReset{due: []dbgen.ListUsersDueForResetRow{{ID: 1, ResetTrafficMethod: &method}}}
		if _, err := runTrafficReset(context.Background(), f, testLogger()); err != nil {
			t.Fatalf("第一次不应报错：%v", err)
		}
		res, err := runTrafficReset(context.Background(), f, testLogger())
		if err != nil {
			t.Fatalf("第二次不应报错：%v", err)
		}
		if res.Due != 0 || res.Reset != 0 {
			t.Fatalf("第二次不该再重置：%+v", res)
		}
		if len(f.advanced) != 1 {
			t.Fatalf("同一个用户被推进了 %d 次", len(f.advanced))
		}
	})

	// 🔴 静默边界 B：没套餐的用户必须**先**被摘掉排期。
	//
	// 顺序反了（先 list 后 suspend）或者干脆不 suspend，现象是：
	// 每小时选中、每小时 ErrNoRows、每小时刷一条错误日志，**永远不收敛**。
	// 真正的问题（这个人没套餐了）被淹在噪声里，而噪声本身会训练所有人忽略这个任务的日志。
	t.Run("静默边界：无套餐用户先被摘除排期，且顺序必须是 suspend → list", func(t *testing.T) {
		f := &fakeTrafficReset{
			due: []dbgen.ListUsersDueForResetRow{
				{ID: 1, ResetTrafficMethod: &method},
				{ID: 99, ResetTrafficMethod: nil}, // 套餐被删
			},
			planless: map[int64]bool{99: true},
		}
		res, err := runTrafficReset(context.Background(), f, testLogger())
		if err != nil {
			t.Fatalf("无套餐用户不该让整批失败：%v", err)
		}
		if res.Suspended != 1 {
			t.Fatalf("应当摘掉 1 个无套餐用户，实际 %d", res.Suspended)
		}
		if res.Reset != 1 {
			t.Fatalf("正常用户仍应被重置，实际 %d", res.Reset)
		}
		if len(f.calls) < 2 || f.calls[0] != "suspend" || f.calls[1] != "list" {
			t.Fatalf("调用顺序必须是 suspend → list，实际 %v", f.calls)
		}
	})

	// 摘除之后仍然 ErrNoRows（用户在两条语句之间被改）时跳过而不是中断整批：
	// 一个人的异常不该让另外 199 个人的配额发不下去。
	// suspend 与 advance 之间还有一个窗口：用户恰好在这两条语句之间失去套餐。
	// 那时 advance 仍会报 ErrNoRows —— 必须跳过而不是中断整批，
	// 否则一个人的并发变更会让另外 199 个人的配额发不下去。
	t.Run("并发变更导致的 ErrNoRows 跳过而不中断整批", func(t *testing.T) {
		f := &fakeTrafficResetAdvanceOnlyFails{
			due: []dbgen.ListUsersDueForResetRow{
				{ID: 1, ResetTrafficMethod: &method},
				{ID: 2, ResetTrafficMethod: &method},
			},
			failFor: 2,
		}
		res, err := runTrafficReset(context.Background(), f, testLogger())
		if err != nil {
			t.Fatalf("单用户失败不该中断整批：%v", err)
		}
		if res.Reset != 1 || res.Skipped != 1 {
			t.Fatalf("结果不对：%+v", res)
		}
	})
}

// fakeTrafficResetAdvanceOnlyFails 模拟「suspend 摘不掉、但 advance 命中 0 行」
// 的并发窗口：用户在两条语句之间失去了套餐。
type fakeTrafficResetAdvanceOnlyFails struct {
	due     []dbgen.ListUsersDueForResetRow
	failFor int64
}

func (f *fakeTrafficResetAdvanceOnlyFails) SuspendResetForPlanlessUsers(context.Context) (int64, error) {
	return 0, nil
}

func (f *fakeTrafficResetAdvanceOnlyFails) ListUsersDueForReset(_ context.Context, _ int32) ([]dbgen.ListUsersDueForResetRow, error) {
	return slices.Clone(f.due), nil
}

func (f *fakeTrafficResetAdvanceOnlyFails) AdvanceUserResetCycle(_ context.Context, userID int64) (dbgen.AdvanceUserResetCycleRow, error) {
	if userID == f.failFor {
		return dbgen.AdvanceUserResetCycleRow{}, pgx.ErrNoRows
	}
	return dbgen.AdvanceUserResetCycleRow{ID: userID}, nil
}

func (f *fakeTrafficResetAdvanceOnlyFails) InsertTrafficResetLog(context.Context, dbgen.InsertTrafficResetLogParams) (dbgen.TrafficResetLog, error) {
	return dbgen.TrafficResetLog{}, nil
}

// ============================================================
// 5 · traffic-batch
// ============================================================

// fakeTrafficBatch 用内存实现 idempotency_keys 的三条语句，
// 从而让「幂等重入」是**真的走了一遍抢占语义**，而不是断言一个计数器。
type fakeTrafficBatch struct {
	keys      map[string]*dbgen.IdempotencyKey
	bumpCalls int
	row       dbgen.BumpUserRevForExhaustedUsersRow
	bumpErr   error
}

func newFakeTrafficBatch() *fakeTrafficBatch {
	return &fakeTrafficBatch{keys: map[string]*dbgen.IdempotencyKey{}}
}

func (f *fakeTrafficBatch) ClaimIdempotencyKey(_ context.Context, arg dbgen.ClaimIdempotencyKeyParams) (dbgen.IdempotencyKey, error) {
	if _, ok := f.keys[arg.Key]; ok {
		// ON CONFLICT DO NOTHING + RETURNING → 0 行 → pgx 报 ErrNoRows
		return dbgen.IdempotencyKey{}, pgx.ErrNoRows
	}
	row := &dbgen.IdempotencyKey{
		Key: arg.Key, UserID: arg.UserID, Endpoint: arg.Endpoint,
		RequestHash: arg.RequestHash, Status: "in_progress",
		ExpiresAt: ts(time.Now().Add(24 * time.Hour)),
	}
	f.keys[arg.Key] = row
	return *row, nil
}

func (f *fakeTrafficBatch) GetIdempotencyKey(_ context.Context, key string) (dbgen.IdempotencyKey, error) {
	row, ok := f.keys[key]
	if !ok {
		return dbgen.IdempotencyKey{}, pgx.ErrNoRows
	}
	return *row, nil
}

func (f *fakeTrafficBatch) CompleteIdempotencyKey(_ context.Context, arg dbgen.CompleteIdempotencyKeyParams) error {
	row, ok := f.keys[arg.Key]
	if !ok {
		return pgx.ErrNoRows
	}
	row.Status = "completed"
	row.ResponseCode = arg.ResponseCode
	row.ResponseBody = arg.ResponseBody
	return nil
}

func (f *fakeTrafficBatch) BumpUserRevForExhaustedUsers(context.Context) (dbgen.BumpUserRevForExhaustedUsersRow, error) {
	f.bumpCalls++
	return f.row, f.bumpErr
}

func TestRunTrafficBatch(t *testing.T) {
	const batchID = "01J8ZQK7X0000000000000000A" // ULID 形态，26 字符

	t.Run("正常路径：抢占成功，跑一次配额耗尽对账", func(t *testing.T) {
		f := newFakeTrafficBatch()
		f.row = dbgen.BumpUserRevForExhaustedUsersRow{ExhaustedUsers: 3, BumpedServers: 0}
		res, err := runTrafficBatch(context.Background(), f, testLogger(), batchID)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if res.Skipped {
			t.Fatal("首次投递不该被判定为重复")
		}
		if f.bumpCalls != 1 {
			t.Fatalf("对账应当跑 1 次，实际 %d", f.bumpCalls)
		}
		if f.keys[batchID].Status != "completed" {
			t.Fatalf("幂等键没有落盘为 completed：%q", f.keys[batchID].Status)
		}
	})

	// 🔴 幂等重入：Cloud Tasks 是 at-least-once，同一个 batch_id 会被投多次。
	// 契约 §9.1 的原文是「claimed_at 抢占失败 → 200 丢弃」；
	// 这里的抢占落在 idempotency_keys 上（那张表不存在，理由见 task.go 的长注释），
	// 但语义必须一致：**业务体只跑一次**。
	t.Run("幂等重入：同一个 batch_id 投三次，对账只跑一次，后两次 idempotent_skip", func(t *testing.T) {
		f := newFakeTrafficBatch()
		for i := range 3 {
			res, err := runTrafficBatch(context.Background(), f, testLogger(), batchID)
			if err != nil {
				t.Fatalf("第 %d 次不应报错：%v", i+1, err)
			}
			if i == 0 && res.Skipped {
				t.Fatal("首次不该 skip")
			}
			if i > 0 && !res.Skipped {
				t.Fatalf("第 %d 次应当 skip", i+1)
			}
		}
		if f.bumpCalls != 1 {
			t.Fatalf("重复投递让业务体跑了 %d 次（应当 1 次）", f.bumpCalls)
		}
	})

	t.Run("并发同批：上一次仍 in_progress 时丢弃，不重复执行", func(t *testing.T) {
		f := newFakeTrafficBatch()
		// 手工造出「抢占成功但还没 Complete」的中间态
		if _, err := f.ClaimIdempotencyKey(context.Background(), dbgen.ClaimIdempotencyKeyParams{
			Key:      batchID,
			Endpoint: "RunTrafficBatchTask",
			RequestHash: httpx.IdempotentRequest{
				Key: batchID, Endpoint: "RunTrafficBatchTask", Body: []byte(batchID),
			}.Fingerprint(),
		}); err != nil {
			t.Fatalf("准备中间态失败：%v", err)
		}
		res, err := runTrafficBatch(context.Background(), f, testLogger(), batchID)
		if err != nil {
			t.Fatalf("并发同批应当优雅丢弃而不是报错：%v", err)
		}
		if !res.Skipped || f.bumpCalls != 0 {
			t.Fatalf("并发同批被执行了：%+v bumpCalls=%d", res, f.bumpCalls)
		}
	})

	// 🔴 静默边界：这个任务存在的全部理由。
	//
	// 「跨过 transfer_enable 阈值那一次必须 bump user_rev」漏掉的后果是
	// 配额耗尽的用户永远不会从节点列表消失 = 免费无限上网，
	// 而且没有报错、没有告警（api-contract §3.8 bump 规则第 3 条）。
	// 对账补 bump 的条数在正常路径下应当恒为 0；非 0 就是有一条入账路径漏了 bump。
	t.Run("静默边界：对账必须真的被调，且补 bump 的条数要能被观察到", func(t *testing.T) {
		f := newFakeTrafficBatch()
		f.row = dbgen.BumpUserRevForExhaustedUsersRow{ExhaustedUsers: 5, BumpedServers: 2}
		res, err := runTrafficBatch(context.Background(), f, testLogger(), batchID)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if f.bumpCalls != 1 {
			t.Fatal("配额耗尽对账没有被调用：跨阈值漏 bump 时没有任何东西会兜住")
		}
		if res.BumpedServers != 2 || res.ExhaustedUsers != 5 {
			t.Fatalf("对账结果没有透出，无法观测：%+v", res)
		}
	})

	t.Run("对账失败要上报（Cloud Tasks 会重投，而对账是幂等的）", func(t *testing.T) {
		f := newFakeTrafficBatch()
		f.bumpErr = errors.New("db down")
		if _, err := runTrafficBatch(context.Background(), f, testLogger(), batchID); err == nil {
			t.Fatal("对账失败被吞掉了")
		}
	})
}

// TestRunTrafficBatchTaskDropsPoisonPayload 钉住毒消息的处理方式。
//
// batch_id 缺失或形态非法时**回 200 丢弃**，不回 5xx：
// Cloud Tasks 对 5xx 会一直重投，而一个非法载荷重投一万次仍然非法 ——
// 那条消息会把队列堵死，后面合法的任务全部被拖慢。
func TestRunTrafficBatchTaskDropsPoisonPayload(t *testing.T) {
	srv := &Server{logger: testLogger()}
	for _, tc := range []struct {
		name string
		req  gen.RunTrafficBatchTaskRequestObject
	}{
		{"请求体缺失", gen.RunTrafficBatchTaskRequestObject{}},
		{"batch_id 为空", gen.RunTrafficBatchTaskRequestObject{Body: &gen.TrafficBatchTaskRequest{BatchId: ""}}},
		{"batch_id 过短", gen.RunTrafficBatchTaskRequestObject{Body: &gen.TrafficBatchTaskRequest{BatchId: "abc"}}},
		{"batch_id 含控制字符", gen.RunTrafficBatchTaskRequestObject{Body: &gen.TrafficBatchTaskRequest{BatchId: "abcdefgh\n"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := srv.RunTrafficBatchTask(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("不应返回 error：%v", err)
			}
			w := httptest.NewRecorder()
			if err := resp.VisitRunTrafficBatchTaskResponse(w); err != nil {
				t.Fatalf("写响应失败：%v", err)
			}
			if w.Code != http.StatusOK {
				t.Fatalf("毒消息必须回 200 丢弃（否则堵死队列），实际 %d", w.Code)
			}
			var body gen.InternalTaskAck
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("响应体不是 InternalTaskAck：%v（%s）", err, w.Body.String())
			}
			if body.IdempotentSkip == nil || !*body.IdempotentSkip {
				t.Fatalf("丢弃应当标记 idempotent_skip：%s", w.Body.String())
			}
		})
	}
}

// ============================================================
// 6 · stat-rollup
// ============================================================

type fakeStatRollup struct {
	wallet      []dbgen.ReconcileWalletBalancesRow
	unbalanced  []dbgen.FindUnbalancedLedgerEntriesRow
	keys        []dbgen.CountActiveServerKeysPerServerRow
	walletErr   error
	calls       []string
	statCutoff  pgtype.Date
	webhookDays pgtype.Interval
	fetchDays   pgtype.Interval
}

func (f *fakeStatRollup) ReconcileWalletBalances(context.Context) ([]dbgen.ReconcileWalletBalancesRow, error) {
	f.calls = append(f.calls, "wallet")
	return f.wallet, f.walletErr
}

func (f *fakeStatRollup) FindUnbalancedLedgerEntries(context.Context) ([]dbgen.FindUnbalancedLedgerEntriesRow, error) {
	f.calls = append(f.calls, "ledger")
	return f.unbalanced, nil
}

func (f *fakeStatRollup) CountActiveServerKeysPerServer(context.Context) ([]dbgen.CountActiveServerKeysPerServerRow, error) {
	f.calls = append(f.calls, "keys")
	return f.keys, nil
}

func (f *fakeStatRollup) CleanupOldStats(_ context.Context, d pgtype.Date) (int64, error) {
	f.calls = append(f.calls, "stats")
	f.statCutoff = d
	return 1, nil
}

func (f *fakeStatRollup) CleanupExpiredSessions(context.Context) (int64, error) {
	f.calls = append(f.calls, "sessions")
	return 2, nil
}

func (f *fakeStatRollup) CleanupOldEmailVerifications(context.Context) (int64, error) {
	f.calls = append(f.calls, "verifications")
	return 3, nil
}

func (f *fakeStatRollup) CleanupOldWebhookEvents(_ context.Context, d pgtype.Interval) (int64, error) {
	f.calls = append(f.calls, "webhooks")
	f.webhookDays = d
	return 4, nil
}

func (f *fakeStatRollup) CleanupOldSubscriptionFetchLog(_ context.Context, d pgtype.Interval) (int64, error) {
	f.calls = append(f.calls, "fetchlog")
	f.fetchDays = d
	return 5, nil
}

func TestRunStatRollup(t *testing.T) {
	now := time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC)

	t.Run("正常路径：三条巡检 + 五条保留期清理都跑", func(t *testing.T) {
		f := &fakeStatRollup{}
		res, err := runStatRollup(context.Background(), f, testLogger(), now)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		for _, want := range []string{"wallet", "ledger", "keys", "stats", "sessions", "verifications", "webhooks", "fetchlog"} {
			if !slices.Contains(f.calls, want) {
				t.Fatalf("没有跑 %q（data-model §13 的保留期与 §7.1 的对账都是硬要求）", want)
			}
		}
		if res.PurgedRows != 1+2+3+4+5 {
			t.Fatalf("清理行数汇总不对：%d", res.PurgedRows)
		}
	})

	t.Run("幂等重入：都是按时间条件的读与删，重复跑没有副作用", func(t *testing.T) {
		f := &fakeStatRollup{}
		if _, err := runStatRollup(context.Background(), f, testLogger(), now); err != nil {
			t.Fatalf("第一次：%v", err)
		}
		if _, err := runStatRollup(context.Background(), f, testLogger(), now); err != nil {
			t.Fatalf("第二次：%v", err)
		}
		if len(f.calls) != 16 {
			t.Fatalf("两次调用共 %d 步，期望 16", len(f.calls))
		}
	})

	// 保留期是合规承诺，不是清理洁癖：数字写错了没有任何报错，
	// 只有某天有人要调证据时发现已经被删了（或者反过来，该删的还在）。
	t.Run("静默边界：保留期数字必须与 data-model §13 一致", func(t *testing.T) {
		f := &fakeStatRollup{}
		if _, err := runStatRollup(context.Background(), f, testLogger(), now); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		wantStat := now.AddDate(0, 0, -3*365)
		if !f.statCutoff.Valid || !f.statCutoff.Time.Equal(wantStat) {
			t.Fatalf("统计保留期不是 3 年：cutoff=%v 期望 %v", f.statCutoff.Time, wantStat)
		}
		if f.webhookDays.Days != 2*365 {
			t.Fatalf("回调事件保留期不是 2 年：%d 天", f.webhookDays.Days)
		}
		if f.fetchDays.Days != 90 {
			t.Fatalf("订阅拉取日志保留期不是 90 天：%d 天", f.fetchDays.Days)
		}
	})

	// 🔴 静默边界：钱包缓存与分录不一致必须被看见。
	// 0007 的表注释：「这是缓存不是真相。每日必须跑一次比对，返回非空行 = 立即告警」。
	// 不一致本身不该让任务失败（那会让后面的清理也不跑），但结果必须透出去。
	t.Run("静默边界：钱包对账不一致要透出，且不因此判定任务失败", func(t *testing.T) {
		f := &fakeStatRollup{wallet: []dbgen.ReconcileWalletBalancesRow{
			{UserID: 7, Currency: "CNY", Cached: 1000, Ledger: 800},
		}}
		res, err := runStatRollup(context.Background(), f, testLogger(), now)
		if err != nil {
			t.Fatalf("对账不一致是告警不是失败：%v", err)
		}
		if res.WalletMismatches != 1 {
			t.Fatalf("不一致没有被透出：%+v", res)
		}
		if !slices.Contains(f.calls, "fetchlog") {
			t.Fatal("对账发现问题之后后续清理停了")
		}
	})

	t.Run("静默边界：一条巡检失败不能吃掉后面的清理", func(t *testing.T) {
		f := &fakeStatRollup{walletErr: errors.New("boom")}
		res, err := runStatRollup(context.Background(), f, testLogger(), now)
		if err == nil {
			t.Fatal("失败必须上报")
		}
		if res.PurgedRows == 0 {
			t.Fatal("巡检失败把后面的保留期清理也吃掉了")
		}
	})
}

// ============================================================
// 7 · remind-sweep
// ============================================================

type fakeRemindSweep struct {
	// sentToday 模拟 email_log 的当天去重（真实实现是查询里的 NOT EXISTS）。
	sentToday map[string]map[int64]bool
	expiring  []dbgen.ListRemindableExpiringUsersRow
	traffic   []dbgen.ListRemindableTrafficUsersRow
	enqueued  []dbgen.EnqueueReminderMailsParams
}

func newFakeRemindSweep() *fakeRemindSweep {
	return &fakeRemindSweep{sentToday: map[string]map[int64]bool{}}
}

func (f *fakeRemindSweep) alreadySent(template string, id int64) bool {
	m := f.sentToday[template]
	return m != nil && m[id]
}

func (f *fakeRemindSweep) ListRemindableExpiringUsers(_ context.Context, arg dbgen.ListRemindableExpiringUsersParams) ([]dbgen.ListRemindableExpiringUsersRow, error) {
	var out []dbgen.ListRemindableExpiringUsersRow
	for _, u := range f.expiring {
		if !f.alreadySent(arg.Template, u.ID) {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeRemindSweep) ListRemindableTrafficUsers(_ context.Context, arg dbgen.ListRemindableTrafficUsersParams) ([]dbgen.ListRemindableTrafficUsersRow, error) {
	var out []dbgen.ListRemindableTrafficUsersRow
	for _, u := range f.traffic {
		if !f.alreadySent(arg.Template, u.ID) {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeRemindSweep) EnqueueReminderMails(_ context.Context, arg dbgen.EnqueueReminderMailsParams) ([]dbgen.EnqueueReminderMailsRow, error) {
	f.enqueued = append(f.enqueued, arg)
	if f.sentToday[arg.Template] == nil {
		f.sentToday[arg.Template] = map[int64]bool{}
	}
	out := make([]dbgen.EnqueueReminderMailsRow, 0, len(arg.UserIds))
	for i, id := range arg.UserIds {
		f.sentToday[arg.Template][id] = true
		uid := id
		out = append(out, dbgen.EnqueueReminderMailsRow{ID: int64(i + 1), UserID: &uid, ToEmail: arg.Emails[i]})
	}
	return out, nil
}

func TestRunRemindSweep(t *testing.T) {
	newFake := func() *fakeRemindSweep {
		f := newFakeRemindSweep()
		f.expiring = []dbgen.ListRemindableExpiringUsersRow{
			{ID: 1, Email: "a@qq.com", ExpiredAt: ts(time.Now().Add(48 * time.Hour))},
		}
		f.traffic = []dbgen.ListRemindableTrafficUsersRow{
			{ID: 2, Email: "b@163.com", U: 85, D: 0, TransferEnable: 100},
		}
		return f
	}

	t.Run("正常路径：两类提醒各入队一批", func(t *testing.T) {
		f := newFake()
		res, err := runRemindSweep(context.Background(), f, unconfiguredMailSender{}, testLogger())
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if res.ExpireQueued != 1 || res.TrafficQueued != 1 {
			t.Fatalf("入队数不对：%+v", res)
		}
		if len(f.enqueued) != 2 {
			t.Fatalf("应当两次批量入队（每类一次），实际 %d", len(f.enqueued))
		}
	})

	// 🔴 幂等重入：契约给的键是 (user_id, remind_kind, day)。
	// 键失效的现象是「同一天收到 N 封一模一样的提醒」——
	// 用户会退订，而退订之后我们连域名封锁广播都发不出去了（ADR 0002）。
	t.Run("幂等重入：同一天再跑一次不再入队", func(t *testing.T) {
		f := newFake()
		if _, err := runRemindSweep(context.Background(), f, unconfiguredMailSender{}, testLogger()); err != nil {
			t.Fatalf("第一次：%v", err)
		}
		res, err := runRemindSweep(context.Background(), f, unconfiguredMailSender{}, testLogger())
		if err != nil {
			t.Fatalf("第二次：%v", err)
		}
		if res.ExpireQueued != 0 || res.TrafficQueued != 0 {
			t.Fatalf("同一天重复入队了：%+v", res)
		}
	})

	// 🔴 静默边界：两类提醒的 template 必须不同，且不能随手改。
	// template 是幂等键的一部分：改字符串 = 当天所有人再收一遍；
	// 两类共用同一个字符串 = 收到到期提醒的人当天收不到流量提醒。
	t.Run("静默边界：两类提醒的 template 必须互不相同（它是幂等键的一部分）", func(t *testing.T) {
		if templateExpireRemind == templateTrafficRemind {
			t.Fatal("两类提醒共用了同一个 template：一类会把另一类的当天名额吃掉")
		}
		f := newFake()
		if _, err := runRemindSweep(context.Background(), f, unconfiguredMailSender{}, testLogger()); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if f.enqueued[0].Template != templateExpireRemind || f.enqueued[1].Template != templateTrafficRemind {
			t.Fatalf("入队用的 template 不对：%q / %q", f.enqueued[0].Template, f.enqueued[1].Template)
		}
		// 0011 的列注释给的示例形态就是 'expire_remind'，不要改成别的拼法。
		if templateExpireRemind != "expire_remind" {
			t.Fatalf("到期提醒的 template 被改了：%q（改它 = 当天所有人再收一遍）", templateExpireRemind)
		}
	})

	// 数组按下标配对（WITH ORDINALITY），两个数组必须等长 ——
	// 不等长在 SQL 里表现为静默丢数（JOIN 条件不成立的那些行直接消失）。
	t.Run("静默边界：user_ids 与 emails 必须等长且一一对应", func(t *testing.T) {
		f := newFake()
		f.expiring = append(f.expiring, dbgen.ListRemindableExpiringUsersRow{ID: 9, Email: "c@gmail.com"})
		if _, err := runRemindSweep(context.Background(), f, unconfiguredMailSender{}, testLogger()); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		arg := f.enqueued[0]
		if len(arg.UserIds) != len(arg.Emails) {
			t.Fatalf("两个数组不等长：%d vs %d（SQL 侧会静默丢数）", len(arg.UserIds), len(arg.Emails))
		}
		if arg.UserIds[1] != 9 || arg.Emails[1] != "c@gmail.com" {
			t.Fatalf("下标配对错位：%v / %v", arg.UserIds, arg.Emails)
		}
	})

	t.Run("ESP 未配置时照样入队（队列是持久的，接线后补发）", func(t *testing.T) {
		f := newFake()
		res, err := runRemindSweep(context.Background(), f, unconfiguredMailSender{}, testLogger())
		if err != nil {
			t.Fatalf("ESP 未配置不该让扫描失败：%v", err)
		}
		if res.ExpireQueued == 0 {
			t.Fatal("ESP 未配置时把入队也跳过了：接线之后这些提醒永远补不回来")
		}
		if f.enqueued[0].Esp != "unconfigured" {
			t.Fatalf("email_log.esp 应当记下当时准备用谁发：%q", f.enqueued[0].Esp)
		}
	})
}

// ============================================================
// 8 · mail-send
// ============================================================

type fakeMailSend struct {
	// queued 模拟 email_log 里 status='queued' 的行。
	queued   map[int64]dbgen.ClaimQueuedMailRow
	status   map[int64]string
	msgIDs   map[int64]string
	bounces  map[int64]string
	claimErr error
}

func newFakeMailSend(ids ...int64) *fakeMailSend {
	f := &fakeMailSend{
		queued: map[int64]dbgen.ClaimQueuedMailRow{},
		status: map[int64]string{}, msgIDs: map[int64]string{}, bounces: map[int64]string{},
	}
	for _, id := range ids {
		f.queued[id] = dbgen.ClaimQueuedMailRow{
			ID: id, ToEmail: "u@qq.com", ToDomain: "qq.com",
			Template: templateExpireRemind, Subject: subjectExpireRemind,
		}
		f.status[id] = "queued"
	}
	return f
}

func (f *fakeMailSend) ClaimQueuedMail(_ context.Context, arg dbgen.ClaimQueuedMailParams) (dbgen.ClaimQueuedMailRow, error) {
	if f.claimErr != nil {
		return dbgen.ClaimQueuedMailRow{}, f.claimErr
	}
	row, ok := f.queued[arg.ID]
	if !ok || f.status[arg.ID] != "queued" {
		// `WHERE id = $1 AND status = 'queued'` 命中 0 行 → :one 报 ErrNoRows
		return dbgen.ClaimQueuedMailRow{}, pgx.ErrNoRows
	}
	f.status[arg.ID] = "sent"
	return row, nil
}

func (f *fakeMailSend) MarkMailSent(_ context.Context, arg dbgen.MarkMailSentParams) error {
	if f.status[arg.ID] != "sent" {
		return nil
	}
	f.msgIDs[arg.ID] = arg.ProviderMsgID
	return nil
}

func (f *fakeMailSend) MarkMailSendFailed(_ context.Context, arg dbgen.MarkMailSendFailedParams) error {
	if f.status[arg.ID] != "sent" {
		return nil
	}
	f.status[arg.ID] = "failed"
	f.bounces[arg.ID] = arg.BounceCode
	return nil
}

type stubMailSender struct {
	name  string
	ready bool
	err   error
	sends []MailMessage
}

func (s *stubMailSender) Name() string     { return s.name }
func (s *stubMailSender) Configured() bool { return s.ready }
func (s *stubMailSender) Send(_ context.Context, m MailMessage) (string, error) {
	s.sends = append(s.sends, m)
	if s.err != nil {
		return "", s.err
	}
	return "msg-" + m.To, nil
}

func TestRunMailSend(t *testing.T) {
	t.Run("正常路径：抢占 → 发信 → 回写 provider_msg_id", func(t *testing.T) {
		db := newFakeMailSend(42)
		sender := &stubMailSender{name: "ses", ready: true}
		res, err := runMailSend(context.Background(), db, sender, testLogger(), 42)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !res.Sent {
			t.Fatalf("没发出去：%+v", res)
		}
		if db.status[42] != "sent" || db.msgIDs[42] == "" {
			t.Fatalf("状态或消息 ID 没落库：status=%q msg=%q", db.status[42], db.msgIDs[42])
		}
		if len(sender.sends) != 1 || sender.sends[0].Template != templateExpireRemind {
			t.Fatalf("发信参数不对：%+v", sender.sends)
		}
	})

	// 🔴 幂等重入：契约的幂等键是 mail_queue.id（实现落在 email_log.id）。
	// 抢占失败 = 已被上一次投递领走 → 200 幂等丢弃。
	// 不做抢占的话，Cloud Tasks 的一次重投就是给用户发第二封验证码 ——
	// 而那意味着两个都有效的 code 同时在飞。
	t.Run("幂等重入：同一封信投三次，ESP 只被调一次", func(t *testing.T) {
		db := newFakeMailSend(42)
		sender := &stubMailSender{name: "ses", ready: true}
		for i := range 3 {
			res, err := runMailSend(context.Background(), db, sender, testLogger(), 42)
			if err != nil {
				t.Fatalf("第 %d 次：%v", i+1, err)
			}
			if i > 0 && !res.Skipped {
				t.Fatalf("第 %d 次应当幂等丢弃", i+1)
			}
		}
		if len(sender.sends) != 1 {
			t.Fatalf("ESP 被调了 %d 次（应当 1 次）", len(sender.sends))
		}
	})

	// 🔴 静默边界 A：ESP 未配置时**不能抢占**。
	//
	// 先抢占再判断的写法会把这封信的状态改成 'sent'，
	// 然后发现没有 ESP —— 于是这封信永远发不出去，而库里显示它已发送。
	// 用户收不到验证码，我们的送达率统计却是 100%。
	t.Run("静默边界：ESP 未配置时不抢占、不报错，信留在队列里", func(t *testing.T) {
		db := newFakeMailSend(42)
		sender := &stubMailSender{name: "unconfigured", ready: false}
		res, err := runMailSend(context.Background(), db, sender, testLogger(), 42)
		if err != nil {
			t.Fatalf("ESP 未接通是计划内状态，不该报错刷告警：%v", err)
		}
		if !res.NotConfigured || res.Sent {
			t.Fatalf("结果不对：%+v", res)
		}
		if db.status[42] != "queued" {
			t.Fatalf("信被抢占掉了（status=%q）：接线之后它永远发不出去", db.status[42])
		}
		if len(sender.sends) != 0 {
			t.Fatal("未配置却调了 Send")
		}

		// 接上 ESP 之后，同一封信必须还能发出去。
		ready := &stubMailSender{name: "ses", ready: true}
		if _, err := runMailSend(context.Background(), db, ready, testLogger(), 42); err != nil {
			t.Fatalf("接线后补发失败：%v", err)
		}
		if db.status[42] != "sent" {
			t.Fatalf("接线后没能补发：status=%q", db.status[42])
		}
	})

	// 🔴 静默边界 B：发信失败必须把状态改回 failed。
	// 停在 'sent' 的话，ADR 0002 §7 的送达率统计会把一封没发出去的信算成成功 ——
	// 而那份统计正是「选哪家 ESP」这个决定的唯一依据。
	t.Run("静默边界：发信失败要落 failed，并按依赖不可达上报", func(t *testing.T) {
		db := newFakeMailSend(42)
		sender := &stubMailSender{name: "ses", ready: true, err: errors.New("554 HL:IPB")}
		res, err := runMailSend(context.Background(), db, sender, testLogger(), 42)
		if err == nil {
			t.Fatal("发信失败必须上报")
		}
		if !res.DependencyDown {
			t.Fatal("应当标记为依赖不可达（→ 503 + Retry-After），而不是普通 500")
		}
		if db.status[42] != "failed" {
			t.Fatalf("状态没改回 failed（%q）：送达率统计会偏高", db.status[42])
		}
		if db.bounces[42] == "" {
			t.Fatal("没有记下失败原因")
		}
	})

	t.Run("未知 id 幂等丢弃，不报错", func(t *testing.T) {
		db := newFakeMailSend()
		sender := &stubMailSender{name: "ses", ready: true}
		res, err := runMailSend(context.Background(), db, sender, testLogger(), 777)
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if !res.Skipped {
			t.Fatalf("应当幂等丢弃：%+v", res)
		}
	})
}

func TestTruncateBounceCode(t *testing.T) {
	if got := truncateBounceCode("554 HL:IPB"); got != "554 HL:IPB" {
		t.Fatalf("短串不该被改：%q", got)
	}
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	if got := truncateBounceCode(string(long)); len(got) != 128 {
		t.Fatalf("长串没截到 128：%d", len(got))
	}

	// 🔴 静默边界：切在多字节字符中间会产生非法 UTF-8，
	// 而 PostgreSQL 的 text 列**拒收**（22021），不是静默截断。
	// 那会让 MarkMailSendFailed 整条语句失败 → 这封信停留在 'sent'
	// → 送达率统计把一封没发出去的信算成成功。
	// 只在「ESP 返回中文错误」时才会触发，是最难被发现的那一类 bug。
	t.Run("中文错误文本截断后仍是合法 UTF-8", func(t *testing.T) {
		var b []byte
		for range 200 {
			b = append(b, []byte("退")...) // 3 字节/字，128 不落在边界上
		}
		got := truncateBounceCode(string(b))
		if len(got) > 128 {
			t.Fatalf("超长：%d", len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("截出了非法 UTF-8（写库会报 22021）：%q", got)
		}
	})
}

// ============================================================
// 9 · chain-scan
// ============================================================

type fakeChainScan struct {
	addrs      []dbgen.ListScannableChainAddressesRow
	recorded   map[string]bool // external_id → 已记
	recordErr  error
	failFor    map[string]bool // external_id → 这一笔落库失败
	touched    []dbgen.TouchPayAddressScanParams
	payFromSet []dbgen.SetOrderPayFromAddressParams
	listErr    error
}

func (f *fakeChainScan) ListScannableChainAddresses(_ context.Context, _ int32) ([]dbgen.ListScannableChainAddressesRow, error) {
	return f.addrs, f.listErr
}

func (f *fakeChainScan) TouchPayAddressScan(_ context.Context, arg dbgen.TouchPayAddressScanParams) error {
	f.touched = append(f.touched, arg)
	return nil
}

func (f *fakeChainScan) RecordChainPayment(_ context.Context, arg dbgen.RecordChainPaymentParams) (dbgen.RecordChainPaymentRow, error) {
	if f.recordErr != nil {
		return dbgen.RecordChainPaymentRow{}, f.recordErr
	}
	if f.failFor[arg.ExternalID] {
		return dbgen.RecordChainPaymentRow{}, errors.New("落库失败")
	}
	if f.recorded == nil {
		f.recorded = map[string]bool{}
	}
	key := arg.Provider + "|" + arg.ExternalID
	if f.recorded[key] {
		// UNIQUE (provider, external_id) + ON CONFLICT DO NOTHING → 0 行
		return dbgen.RecordChainPaymentRow{}, pgx.ErrNoRows
	}
	f.recorded[key] = true
	return dbgen.RecordChainPaymentRow{ID: int64(len(f.recorded)), State: dbgen.PaymentStatePaid, OrderID: &arg.OrderID}, nil
}

func (f *fakeChainScan) SetOrderPayFromAddress(_ context.Context, arg dbgen.SetOrderPayFromAddressParams) error {
	f.payFromSet = append(f.payFromSet, arg)
	return nil
}

type stubChainScanner struct {
	ready     bool
	transfers []ChainTransfer
	err       error
	cursors   []*int64
}

func (s *stubChainScanner) Name() string     { return "stub" }
func (s *stubChainScanner) Configured() bool { return s.ready }
func (s *stubChainScanner) Scan(_ context.Context, _, _ string, cursorMS *int64) ([]ChainTransfer, error) {
	s.cursors = append(s.cursors, cursorMS)
	if s.err != nil {
		return nil, s.err
	}
	return s.transfers, nil
}

func chainAddr(expected *int64) dbgen.ListScannableChainAddressesRow {
	cursor := int64(1_700_000_000_000)
	return dbgen.ListScannableChainAddressesRow{
		PayAddressID: 1, Chain: "tron", Address: "TReceive", CursorTs: &cursor,
		OrderID: 55, TradeNo: "20260830T1", UserID: 7,
		OrderStatus: dbgen.OrderStatusPaying, PayAmountUsdt6: expected,
	}
}

func TestRunChainScan(t *testing.T) {
	expected := int64(10_000_000) // 10 USDT
	transfer := ChainTransfer{
		TxID: "abc123", LogIndex: 0, FromAddress: "TPayer", ToAddress: "TReceive",
		AmountUSDT6: 10_000_000, Confirmations: 25, Solidified: true,
		BlockTimeMS: 1_700_000_600_000,
	}

	// ⚠️ ADR 0012 是「提案，未批准」：默认实现必须什么都不做，且不报错。
	// 每分钟一次的 503 会训练所有人忽略这个任务的告警。
	t.Run("未配置：优雅退出，不查库、不报错、不回 503", func(t *testing.T) {
		f := &fakeChainScan{listErr: errors.New("不该被调用")}
		res, err := runChainScan(context.Background(), f, unconfiguredChainScanner{}, testLogger())
		if err != nil {
			t.Fatalf("未配置不该报错：%v", err)
		}
		if !res.NotConfigured || res.Addresses != 0 {
			t.Fatalf("结果不对：%+v", res)
		}
	})

	t.Run("正常路径：落一条 payments，external_id 是 txid:log_index", func(t *testing.T) {
		f := &fakeChainScan{addrs: []dbgen.ListScannableChainAddressesRow{chainAddr(&expected)}}
		sc := &stubChainScanner{ready: true, transfers: []ChainTransfer{transfer}}
		res, err := runChainScan(context.Background(), f, sc, testLogger())
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if res.Recorded != 1 || res.Duplicates != 0 {
			t.Fatalf("结果不对：%+v", res)
		}
		if !f.recorded["chain_tron|abc123:0"] {
			t.Fatalf("幂等键形态不对：%v", f.recorded)
		}
		if len(f.payFromSet) != 1 || f.payFromSet[0].FromAddress != "TPayer" {
			t.Fatalf("没有回填付款方地址（ADR 0013 §9 之后再也拿不回来）：%+v", f.payFromSet)
		}
	})

	// 🔴 幂等重入：§10.5 的「游标回退 10 分钟重扫」让重复成为**设计内的常态**，
	// 靠 UNIQUE (provider, external_id) 兜底。重复必须是「丢弃」不是「报错」。
	t.Run("幂等重入：同一笔转账扫两次只落一条流水", func(t *testing.T) {
		f := &fakeChainScan{addrs: []dbgen.ListScannableChainAddressesRow{chainAddr(&expected)}}
		sc := &stubChainScanner{ready: true, transfers: []ChainTransfer{transfer}}
		if _, err := runChainScan(context.Background(), f, sc, testLogger()); err != nil {
			t.Fatalf("第一次：%v", err)
		}
		res, err := runChainScan(context.Background(), f, sc, testLogger())
		if err != nil {
			t.Fatalf("第二次：%v", err)
		}
		if res.Recorded != 0 || res.Duplicates != 1 {
			t.Fatalf("重复没有被幂等丢弃：%+v", res)
		}
		if len(f.recorded) != 1 {
			t.Fatalf("同一笔钱落了 %d 条流水", len(f.recorded))
		}
	})

	// 🔴 静默边界：落库失败时**绝不能推进游标**。
	// 推进了就等于把那笔钱跳过去 —— 链上有记录、我们库里没有，
	// 只有用户投诉时才会被发现，而那时我们连「有没有收到」都答不出来。
	t.Run("静默边界：落库失败时游标不推进（推进 = 那笔钱被永久跳过）", func(t *testing.T) {
		f := &fakeChainScan{
			addrs:     []dbgen.ListScannableChainAddressesRow{chainAddr(&expected)},
			recordErr: errors.New("db down"),
		}
		sc := &stubChainScanner{ready: true, transfers: []ChainTransfer{transfer}}
		if _, err := runChainScan(context.Background(), f, sc, testLogger()); err == nil {
			t.Fatal("全部失败应当上报（→ 503）")
		}
		if len(f.touched) != 1 {
			t.Fatalf("应当仍然记一次 last_scanned_at：%d", len(f.touched))
		}
		if f.touched[0].CursorTs != nil {
			t.Fatalf("游标被推进了：%d", *f.touched[0].CursorTs)
		}
	})

	// 🔴 静默边界（同一个地址、同一批里先失败后成功）：
	// 只把 newestMS 归零是不够的 —— 后面那笔成功的转账会把它重新抬上去，
	// 于是游标越过了前面那笔**没记成功**的钱。表现同上：链上有、库里没有。
	t.Run("静默边界：同批里先失败后成功时，游标仍然不能推进", func(t *testing.T) {
		early := transfer
		early.TxID = "early"
		early.BlockTimeMS = 1_700_000_500_000
		late := transfer
		late.TxID = "late"
		late.BlockTimeMS = 1_700_000_900_000

		f := &fakeChainScan{
			addrs:   []dbgen.ListScannableChainAddressesRow{chainAddr(&expected)},
			failFor: map[string]bool{"early:0": true},
		}
		sc := &stubChainScanner{ready: true, transfers: []ChainTransfer{early, late}}
		if _, err := runChainScan(context.Background(), f, sc, testLogger()); err == nil {
			t.Fatal("这个地址全军覆没之外的语义先不管，但失败必须上报")
		}
		if len(f.touched) != 1 {
			t.Fatalf("应当仍然记一次 last_scanned_at：%d", len(f.touched))
		}
		if f.touched[0].CursorTs != nil {
			t.Fatalf("游标被后面那笔成功的转账抬过去了：%d（early 那笔钱会被永久跳过）",
				*f.touched[0].CursorTs)
		}
		if !f.recorded["chain_tron|late:0"] {
			t.Fatal("同一地址里后续转账不该被前一笔的失败带停")
		}
	})

	t.Run("成功时推进游标到本批最新的区块时间", func(t *testing.T) {
		f := &fakeChainScan{addrs: []dbgen.ListScannableChainAddressesRow{chainAddr(&expected)}}
		sc := &stubChainScanner{ready: true, transfers: []ChainTransfer{transfer}}
		if _, err := runChainScan(context.Background(), f, sc, testLogger()); err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if len(f.touched) != 1 || f.touched[0].CursorTs == nil {
			t.Fatalf("游标没有推进：%+v", f.touched)
		}
		if *f.touched[0].CursorTs != transfer.BlockTimeMS {
			t.Fatalf("游标值不对：%d", *f.touched[0].CursorTs)
		}
	})

	// 归属只看地址不看金额（ADR 0012 §5.4）：少付也要落库，
	// 判定交给 SQL 的累计比较，不在 Go 侧做任何金额匹配。
	t.Run("少付照样落库（归属只看地址，金额只决定 state）", func(t *testing.T) {
		f := &fakeChainScan{addrs: []dbgen.ListScannableChainAddressesRow{chainAddr(&expected)}}
		short := transfer
		short.AmountUSDT6 = 1
		sc := &stubChainScanner{ready: true, transfers: []ChainTransfer{short}}
		res, err := runChainScan(context.Background(), f, sc, testLogger())
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if res.Recorded != 1 {
			t.Fatalf("少付被丢掉了：%+v", res)
		}
	})

	t.Run("应收金额缺失时仍然落库（钱不能因为我们的字段缺失而丢）", func(t *testing.T) {
		f := &fakeChainScan{addrs: []dbgen.ListScannableChainAddressesRow{chainAddr(nil)}}
		sc := &stubChainScanner{ready: true, transfers: []ChainTransfer{transfer}}
		res, err := runChainScan(context.Background(), f, sc, testLogger())
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if res.Recorded != 1 {
			t.Fatalf("到账被丢掉了：%+v", res)
		}
	})

	t.Run("地址被 Tether 拉黑时跳过扫描，不影响其余地址", func(t *testing.T) {
		blacklisted := chainAddr(&expected)
		blacklisted.IsBlacklisted = true
		blacklisted.PayAddressID = 2
		f := &fakeChainScan{addrs: []dbgen.ListScannableChainAddressesRow{blacklisted, chainAddr(&expected)}}
		sc := &stubChainScanner{ready: true, transfers: []ChainTransfer{transfer}}
		res, err := runChainScan(context.Background(), f, sc, testLogger())
		if err != nil {
			t.Fatalf("不应报错：%v", err)
		}
		if len(sc.cursors) != 1 {
			t.Fatalf("拉黑的地址仍被扫描了：%d 次", len(sc.cursors))
		}
		if res.Recorded != 1 {
			t.Fatalf("正常地址没被处理：%+v", res)
		}
		// 被拉黑而跳过的地址不算「尝试扫描过」——
		// 混进分母会让「全部失败 → 503」的判据被一个运营问题稀释掉。
		if res.Addresses != 1 {
			t.Fatalf("拉黑地址被算进了尝试扫描的分母：%+v", res)
		}
	})

	t.Run("全部地址拉取失败 → 上报（调用方回 503 + Retry-After）", func(t *testing.T) {
		f := &fakeChainScan{addrs: []dbgen.ListScannableChainAddressesRow{chainAddr(&expected)}}
		sc := &stubChainScanner{ready: true, err: errors.New("upstream 502")}
		res, err := runChainScan(context.Background(), f, sc, testLogger())
		if err == nil {
			t.Fatal("上游整体不可达必须上报")
		}
		if !res.AllFailed || res.Failures != res.Addresses {
			t.Fatalf("失败计数不对：%+v", res)
		}
	})

	// Failures 按**地址**计而不是按事件计：一个地址上 3 笔落库失败
	// 不能让 Failures 超过 Addresses，否则「全部失败 → 503」的判据在
	// 「一个地址坏、其余都好」时会碰巧成立，把好的那部分也退避掉。
	t.Run("静默边界：Failures 按地址计，部分失败不误判为整体不可达", func(t *testing.T) {
		a1 := chainAddr(&expected)
		a2 := chainAddr(&expected)
		a2.PayAddressID = 2
		a2.Address = "TReceive2"
		t1, t2, t3 := transfer, transfer, transfer
		t1.TxID, t2.TxID, t3.TxID = "x1", "x2", "x3"
		ok1 := transfer
		ok1.TxID = "ok1"

		f := &fakeChainScan{
			addrs:   []dbgen.ListScannableChainAddressesRow{a1, a2},
			failFor: map[string]bool{"x1:0": true, "x2:0": true, "x3:0": true},
		}
		sc := &stubChainScannerPerAddress{byAddress: map[string][]ChainTransfer{
			"TReceive":  {t1, t2, t3}, // 三笔全部落库失败 → 这个地址算 1 次失败
			"TReceive2": {ok1},        // 正常
		}}
		res, err := runChainScan(context.Background(), f, sc, testLogger())
		if err != nil {
			t.Fatalf("一个地址坏不该整体上报：%v", err)
		}
		if res.Failures != 1 {
			t.Fatalf("Failures 应当按地址计（1），实际 %d", res.Failures)
		}
		if res.AllFailed {
			t.Fatal("被误判为整体不可达：好的地址会被一起退避掉")
		}
	})

	t.Run("部分地址失败不上报（退避会让好的那部分也停下来）", func(t *testing.T) {
		a1 := chainAddr(&expected)
		a2 := chainAddr(&expected)
		a2.PayAddressID = 2
		a2.Address = "TReceive2"
		f := &fakeChainScan{addrs: []dbgen.ListScannableChainAddressesRow{a1, a2}}
		sc := &stubChainScannerFlaky{transfers: []ChainTransfer{transfer}, failOn: "TReceive2"}
		res, err := runChainScan(context.Background(), f, sc, testLogger())
		if err != nil {
			t.Fatalf("部分失败不该整体上报：%v", err)
		}
		if res.Failures != 1 || res.Recorded != 1 {
			t.Fatalf("结果不对：%+v", res)
		}
	})
}

// stubChainScannerPerAddress 按地址出不同的转账，用来构造
// 「一个地址全坏、另一个地址全好」这种混合场景。
type stubChainScannerPerAddress struct {
	byAddress map[string][]ChainTransfer
}

func (s *stubChainScannerPerAddress) Name() string     { return "per-address" }
func (s *stubChainScannerPerAddress) Configured() bool { return true }
func (s *stubChainScannerPerAddress) Scan(_ context.Context, _, address string, _ *int64) ([]ChainTransfer, error) {
	return s.byAddress[address], nil
}

type stubChainScannerFlaky struct {
	transfers []ChainTransfer
	failOn    string
}

func (s *stubChainScannerFlaky) Name() string     { return "flaky" }
func (s *stubChainScannerFlaky) Configured() bool { return true }
func (s *stubChainScannerFlaky) Scan(_ context.Context, _, address string, _ *int64) ([]ChainTransfer, error) {
	if address == s.failOn {
		return nil, errors.New("timeout")
	}
	return s.transfers, nil
}

// TestLookbackCursor 钉住 §10.5 的「游标往回退 10 分钟重扫」。
//
// 回退量写错的后果是分级的：太小 → 边界上漏读一笔到账（钱进黑洞）；
// 太大 → 每轮多扫一批已经记过的事件（只是浪费额度）。所以宁可大不可小，
// 但这个数字必须与 ADR 一致，否则下一个人无从判断它是不是被谁随手改过。
func TestLookbackCursor(t *testing.T) {
	if got := lookbackCursor(nil); got != nil {
		t.Fatal("从没扫过的地址应当从头扫，不是从 -10 分钟扫")
	}
	base := int64(1_700_000_000_000)
	got := lookbackCursor(&base)
	if got == nil {
		t.Fatal("游标丢了")
	}
	if want := base - 10*60*1000; *got != want {
		t.Fatalf("回看量不是 10 分钟：%d，期望 %d", *got, want)
	}
	small := int64(1000)
	if got := lookbackCursor(&small); got == nil || *got != 0 {
		t.Fatalf("回退不能穿到负数（毫秒时间戳没有负值语义）：%v", got)
	}
}

func TestFallbackRawIsValidJSON(t *testing.T) {
	// payments.raw 是 NOT NULL jsonb：兜底对象必须是合法 JSON，
	// 否则「扫描器没给原文」这条本来无害的路径会变成整笔到账落不了库。
	var v map[string]any
	if err := json.Unmarshal(mustMarshalFallbackRaw(ChainTransfer{TxID: "x"}), &v); err != nil {
		t.Fatalf("兜底 raw 不是合法 JSON：%v", err)
	}
	if v["txid"] != "x" {
		t.Fatalf("兜底 raw 丢了 txid：%v", v)
	}
}

// ============================================================
// 响应形状（内部面契约）
// ============================================================

// TestInternalTaskAckShape 钉住 api-contract §7 表格最后一列：
// 内部面的成功响应是**裸 JSON `{"ok":true}`，不套统一信封**。
// 套上信封不会有任何报错 —— Cloud Tasks 只看状态码 ——
// 但它会让内部面与公网面的响应形状分叉，而契约是冻结的。
func TestInternalTaskAckShape(t *testing.T) {
	for _, tc := range []struct {
		name  string
		visit func(w http.ResponseWriter) error
	}{
		{"alive-gc", func(w http.ResponseWriter) error {
			return gen.RunAliveGcTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}.VisitRunAliveGcTaskResponse(w)
		}},
		{"expire-check", func(w http.ResponseWriter) error {
			return gen.RunExpireCheckTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}.VisitRunExpireCheckTaskResponse(w)
		}},
		{"order-timeout", func(w http.ResponseWriter) error {
			return gen.RunOrderTimeoutTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}.VisitRunOrderTimeoutTaskResponse(w)
		}},
		{"traffic-reset", func(w http.ResponseWriter) error {
			return gen.RunTrafficResetTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}.VisitRunTrafficResetTaskResponse(w)
		}},
		{"traffic-batch", func(w http.ResponseWriter) error {
			return gen.RunTrafficBatchTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}.VisitRunTrafficBatchTaskResponse(w)
		}},
		{"stat-rollup", func(w http.ResponseWriter) error {
			return gen.RunStatRollupTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}.VisitRunStatRollupTaskResponse(w)
		}},
		{"chain-scan", func(w http.ResponseWriter) error {
			return gen.RunChainScanTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}.VisitRunChainScanTaskResponse(w)
		}},
		{"mail-send", func(w http.ResponseWriter) error {
			return gen.RunMailSendTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}.VisitRunMailSendTaskResponse(w)
		}},
		{"remind-sweep", func(w http.ResponseWriter) error {
			return gen.RunRemindSweepTask200JSONResponse{InternalTaskAckJSONResponse: taskAck()}.VisitRunRemindSweepTaskResponse(w)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if err := tc.visit(w); err != nil {
				t.Fatalf("写响应失败：%v", err)
			}
			if w.Code != http.StatusOK {
				t.Fatalf("状态码 %d", w.Code)
			}
			var raw map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
				t.Fatalf("响应体不是 JSON 对象：%v", err)
			}
			if _, hasEnvelope := raw["data"]; hasEnvelope {
				t.Fatalf("内部面不该套信封：%s", w.Body.String())
			}
			if ok, _ := raw["ok"].(bool); !ok {
				t.Fatalf("缺少 ok:true：%s", w.Body.String())
			}
			// idempotent_skip 是可选字段，正常成功时不该出现（omitempty）。
			if _, present := raw["idempotent_skip"]; present {
				t.Fatalf("正常成功不该带 idempotent_skip：%s", w.Body.String())
			}
		})
	}
}

// TestTaskAckSkippedShape 钉住幂等丢弃的形状：仍然 200，但带 idempotent_skip=true。
// 回非 2xx 只会招来更多重投；不带这个字段则重投率不可观测。
func TestTaskAckSkippedShape(t *testing.T) {
	w := httptest.NewRecorder()
	if err := (gen.RunTrafficBatchTask200JSONResponse{InternalTaskAckJSONResponse: taskAckSkipped()}).
		VisitRunTrafficBatchTaskResponse(w); err != nil {
		t.Fatalf("写响应失败：%v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("幂等丢弃必须回 200，实际 %d", w.Code)
	}
	var ack gen.InternalTaskAck
	if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil {
		t.Fatalf("响应体解析失败：%v", err)
	}
	if !ack.Ok || ack.IdempotentSkip == nil || !*ack.IdempotentSkip {
		t.Fatalf("形状不对：%s", w.Body.String())
	}
}

// TestDependencyDownCarriesRetryAfter 钉住 503 必带 Retry-After。
//
// openapi 对 ErrDependencyDown 的原文是「**必带 Retry-After**」。
// 缺了它不会有报错 —— 调用方只是按自己的默认退避重试，
// 而那个默认值与我们的恢复节奏无关。
func TestDependencyDownCarriesRetryAfter(t *testing.T) {
	srv := &Server{logger: testLogger()}
	ctx := context.Background()
	body := srv.dependencyDown(ctx, "链上数据源暂时不可达", errors.New("upstream 502"))

	for _, tc := range []struct {
		name  string
		visit func(w http.ResponseWriter) error
	}{
		{"chain-scan", func(w http.ResponseWriter) error {
			return gen.RunChainScanTask503JSONResponse{ErrDependencyDownJSONResponse: body}.VisitRunChainScanTaskResponse(w)
		}},
		{"mail-send", func(w http.ResponseWriter) error {
			return gen.RunMailSendTask503JSONResponse{ErrDependencyDownJSONResponse: body}.VisitRunMailSendTaskResponse(w)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if err := tc.visit(w); err != nil {
				t.Fatalf("写响应失败：%v", err)
			}
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("状态码 %d", w.Code)
			}
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("503 缺少 Retry-After")
			}
			var env gen.ErrorEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("响应体不是信封：%v", err)
			}
			// 错误码必须落在 openapi 的 ErrorCode enum 里 ——
			// 这个仓库出过「六个错误码不在 enum 里」的事故。
			if env.Error.Code != gen.INTERNALDEPENDENCYDOWN {
				t.Fatalf("错误码不对：%q", env.Error.Code)
			}
		})
	}
}

// TestDefaultDependenciesAreUnconfigured 钉住「默认不接任何第三方」。
//
// 这不是形式主义：ADR 0012 现在是「提案，未批准」，而 ESP 也没选型（ADR 0002 §7
// 要求先拿真实送达率数据）。哪天有人给默认实现塞了一个真的 endpoint，
// 这个测试是唯一会红的东西 —— 否则第一次发现是账单或者一封发错的信。
func TestDefaultDependenciesAreUnconfigured(t *testing.T) {
	if defaultChainScanner.Configured() {
		t.Fatal("默认链上扫描器不该是已配置状态")
	}
	if _, err := defaultChainScanner.Scan(context.Background(), "tron", "T", nil); !errors.Is(err, ErrChainScannerNotConfigured) {
		t.Fatalf("默认实现应当返回未配置错误：%v", err)
	}
	if defaultMailSender.Configured() {
		t.Fatal("默认发信实现不该是已配置状态")
	}
	if _, err := defaultMailSender.Send(context.Background(), MailMessage{}); !errors.Is(err, ErrMailSenderNotConfigured) {
		t.Fatalf("默认实现应当返回未配置错误：%v", err)
	}
}

// TestTimestamptzString 保证日志里打的是时刻而不是结构体字面量。
func TestTimestamptzString(t *testing.T) {
	if got := timestamptzString(pgtype.Timestamptz{}); got != "<null>" {
		t.Fatalf("空值应当打 <null>，实际 %q", got)
	}
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	if got := timestamptzString(ts(at)); got != "2026-08-30T12:00:00Z" {
		t.Fatalf("时刻格式不对：%q", got)
	}
}
