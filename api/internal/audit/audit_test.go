// audit 包的测试。
//
// # 这个包为什么存在，本文件就测什么
//
// api-contract.md §6.3 第 1 条：
//
//	「审计写入与业务写入在同一个事务里。审计写失败 → 整个操作回滚。
//	  这是『审计不可绕过』唯一可靠的实现方式；异步写审计等于承认审计可能缺失。」
//
// # 每个用例为什么必须存在
//
//	TestInTxRollsBackWhenAuditWriteFails
//	  🔴 **这一条是审计包存在的全部理由。** 业务已经写完、审计写失败时，
//	  必须 Commit 一次都不发生。少了它，「业务成功、审计缺失」就是一个静默的可能，
//	  而一条查不到的管理操作在事后与「没发生过」不可区分 —— D6（手工标记订单已支付）
//	  是 api-contract 自己称为「全系统最大的内部欺诈面」的那个操作。
//
//	TestInTxRollsBackWhenAuditEntryIncomplete
//	  「忘了写审计」的现象必须是**业务操作失败**，不是「操作成功但没留痕」。
//	  Entry 的必填字段为空时整个事务回滚 —— 这是 InTx 签名不给「不写审计」
//	  留出口的运行时那一半。
//
//	TestInTxWritesAuditOnTheSameTx
//	  审计与业务落在**同一个 tx 句柄**上。如果哪天有人把 Write 的第一个参数
//	  换成连接池，上面两条仍然会过（那次写会成功），而 §6.3 第 1 条已经失效。
//
//	TestInTxRejectsBadActorBeforeOpeningTx
//	  缺 IP / 缺 admin_id / 缺 email 的调用连事务都不该开。
//	  request_ip 这里**不回退到 0.0.0.0**：这张表是证据，一条写着 0.0.0.0 的
//	  审计记录会在事后被当成真实来源读，而它其实什么都没说。
//
//	TestWriteMapsNilSnapshotsToSQLNull / TestTruncateUTF8
//	  jsonb 里 SQL NULL 与 JSON `null` 是不同的值，`WHERE before_value IS NULL`
//	  只命中前者；UA 从中间切断会产生非法 UTF-8，而 Postgres 的 text 列会**拒绝**它 ——
//	  于是一次带中文 UA 的管理操作会因为审计写失败而整个回滚。
package audit

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dbgen "github.com/oratis/babelplus/api/db/gen"
)

// ---- 假事务 ----

// fakeTx 内嵌 pgx.Tx 接口（值为 nil）：只实现本包用得到的四个方法，
// 其余方法一旦被调用会 panic —— 那正是我们想要的信号，
// 说明 audit 包开始依赖一条这里没有覆盖到的数据库能力。
type fakeTx struct {
	pgx.Tx

	execs     []string
	execArgs  [][]any
	failAudit error
	commits   int
	rollbacks int
	commitErr error
	closed    bool
}

func (f *fakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	f.execArgs = append(f.execArgs, args)
	if f.failAudit != nil && strings.Contains(sql, "INSERT INTO audit_logs") {
		return pgconn.CommandTag{}, f.failAudit
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (f *fakeTx) Commit(context.Context) error {
	if f.closed {
		return pgx.ErrTxClosed
	}
	f.commits++
	if f.commitErr != nil {
		return f.commitErr
	}
	f.closed = true
	return nil
}

func (f *fakeTx) Rollback(context.Context) error {
	f.rollbacks++
	if f.closed {
		// 已提交后 Rollback 返回 ErrTxClosed，InTx 的 defer 会忽略它。
		return pgx.ErrTxClosed
	}
	f.closed = true
	return nil
}

// auditSQLCount 数一数落在这条事务上的审计写入次数。
func (f *fakeTx) auditWrites() int {
	n := 0
	for _, s := range f.execs {
		if strings.Contains(s, "INSERT INTO audit_logs") {
			n++
		}
	}
	return n
}

// fakeBeginner 是 Beginner 的假实现，只发一条固定的假事务。
type fakeBeginner struct {
	tx     *fakeTx
	err    error
	begins int
}

func (b *fakeBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	b.begins++
	if b.err != nil {
		return nil, b.err
	}
	return b.tx, nil
}

// ---- 夹具 ----

func testActor() Actor {
	return Actor{
		AdminID:   7,
		Email:     "ops@babel.plus",
		IP:        netip.MustParseAddr("203.0.113.9"),
		UserAgent: "Mozilla/5.0 (Macintosh)",
	}
}

func testEntry() Entry {
	return Entry{
		Action:     "D6.order.mark_paid",
		TargetType: "order",
		TargetID:   "20260816T7K2M9Q4",
		Before:     map[string]any{"status": "pending"},
		After:      map[string]any{"status": "paid"},
		Reason:     "客户已线下转账，凭证 #4471",
	}
}

func newBeginner() *fakeBeginner { return &fakeBeginner{tx: &fakeTx{}} }

// businessWrite 是一次真实形态的业务写入：走生成代码，落在传进来的那个句柄上。
func businessWrite(ctx context.Context, q *dbgen.Queries) error {
	return q.ArchivePlan(ctx, 42)
}

// ---- 🔴 审计写失败 → 整个业务操作回滚 ----

// 这条用例是审计包存在的全部理由（api-contract §6.3 第 1 条）。
//
// 业务写入已经发生（execs 里有它），审计写入失败 —— 此时**必须**：
//   - InTx 返回错误
//   - Commit 一次都没有被调用
//   - Rollback 被调用
//
// 少了这一条，「业务成功、审计缺失」就是一个静默的可能。
func TestInTxRollsBackWhenAuditWriteFails(t *testing.T) {
	b := newBeginner()
	b.tx.failAudit = errors.New("audit_logs 写入被拒（磁盘满 / 约束冲突 / 权限）")

	err := InTx(context.Background(), b, testActor(),
		func(ctx context.Context, q *dbgen.Queries) (Entry, error) {
			if err := businessWrite(ctx, q); err != nil {
				return Entry{}, err
			}
			return testEntry(), nil
		})

	if err == nil {
		t.Fatal("审计写失败时 InTx 必须返回错误")
	}
	// 先确认业务写入确实发生过 —— 否则这个用例可能只是在测「fn 没跑」。
	if len(b.tx.execs) < 2 {
		t.Fatalf("业务写入应当已经发生过，实际 execs=%v", b.tx.execs)
	}
	if !strings.Contains(b.tx.execs[0], "UPDATE plans") {
		t.Fatalf("第一条应是业务写入，实得 %q", b.tx.execs[0])
	}
	if b.tx.commits != 0 {
		t.Fatalf("🔴 审计写失败却提交了 %d 次事务 —— 业务写入会留在库里而审计缺失，"+
			"这正是 api-contract §6.3 第 1 条禁止的", b.tx.commits)
	}
	if b.tx.rollbacks == 0 {
		t.Fatal("必须回滚")
	}
}

// 「忘了写审计」的现象必须是业务操作失败。
// Entry 的三个必填字段任一为空 → validate 拒绝 → 事务不提交。
func TestInTxRollsBackWhenAuditEntryIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry Entry
	}{
		{"整个 Entry 是零值（等于没写审计）", Entry{}},
		{"缺 action", Entry{TargetType: "order", TargetID: "T1"}},
		{"缺 target_type", Entry{Action: "D6.order.mark_paid", TargetID: "T1"}},
		{"缺 target_id", Entry{Action: "D6.order.mark_paid", TargetType: "order"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBeginner()
			err := InTx(context.Background(), b, testActor(),
				func(ctx context.Context, q *dbgen.Queries) (Entry, error) {
					if err := businessWrite(ctx, q); err != nil {
						return Entry{}, err
					}
					return tc.entry, nil
				})

			if err == nil {
				t.Fatal("审计条目不完整时整个操作必须失败")
			}
			if b.tx.commits != 0 {
				t.Fatal("🔴 提交了 —— 于是「忘了写审计」变成「操作成功但没留痕」")
			}
			if b.tx.auditWrites() != 0 {
				t.Fatal("不完整的条目不该被写进库")
			}
			if b.tx.rollbacks == 0 {
				t.Fatal("必须回滚")
			}
		})
	}
}

// 审计与业务必须落在**同一个 tx 句柄**上。
//
// 如果哪天有人把 Write 的第一个参数换成连接池，上面两条用例仍然会过
// （那次写会成功），而 §6.3 第 1 条已经悄悄失效。这一条钉住的是「同事务」本身。
func TestInTxWritesAuditOnTheSameTx(t *testing.T) {
	b := newBeginner()

	err := InTx(context.Background(), b, testActor(),
		func(ctx context.Context, q *dbgen.Queries) (Entry, error) {
			if err := businessWrite(ctx, q); err != nil {
				return Entry{}, err
			}
			return testEntry(), nil
		})
	if err != nil {
		t.Fatalf("happy path 应成功，实得 %v", err)
	}
	if b.begins != 1 {
		t.Fatalf("应只开一个事务，实得 %d", b.begins)
	}
	if len(b.tx.execs) != 2 {
		t.Fatalf("业务写与审计写都应落在这条事务上，实得 %v", b.tx.execs)
	}
	if !strings.Contains(b.tx.execs[0], "UPDATE plans") {
		t.Fatalf("第一条应是业务写入，实得 %q", b.tx.execs[0])
	}
	if b.tx.auditWrites() != 1 {
		t.Fatalf("审计写入应恰好一次且在同一条事务上，实得 %d", b.tx.auditWrites())
	}
	// 审计写在业务写**之后**：after 快照往往要等业务写完才拿得到。
	if !strings.Contains(b.tx.execs[1], "INSERT INTO audit_logs") {
		t.Fatalf("审计应写在业务之后，实得 %q", b.tx.execs[1])
	}
	if b.tx.commits != 1 {
		t.Fatalf("应提交一次，实得 %d", b.tx.commits)
	}
}

// 业务本身失败时：不写审计、不提交。
func TestInTxBusinessErrorSkipsAuditAndRollsBack(t *testing.T) {
	b := newBeginner()
	want := errors.New("订单状态机拒绝这次转移")

	err := InTx(context.Background(), b, testActor(),
		func(context.Context, *dbgen.Queries) (Entry, error) { return Entry{}, want })

	if !errors.Is(err, want) {
		t.Fatalf("业务错误应原样返回（调用方要按它分支），实得 %v", err)
	}
	if b.tx.auditWrites() != 0 {
		t.Fatal("业务没成功就不该留下审计记录")
	}
	if b.tx.commits != 0 {
		t.Fatal("不该提交")
	}
}

// 提交失败也必须让整个 InTx 失败 —— 否则调用方会以为操作成功了。
func TestInTxCommitFailureIsReported(t *testing.T) {
	b := newBeginner()
	b.tx.commitErr = errors.New("连接在提交时断了")

	err := InTx(context.Background(), b, testActor(),
		func(ctx context.Context, q *dbgen.Queries) (Entry, error) {
			if err := businessWrite(ctx, q); err != nil {
				return Entry{}, err
			}
			return testEntry(), nil
		})
	if err == nil {
		t.Fatal("提交失败必须报出来")
	}
}

// 缺 IP / 缺 admin_id / 缺 email 的调用连事务都不该开：
// 否则业务写入白跑一趟（虽然最终会回滚）。
func TestInTxRejectsBadActorBeforeOpeningTx(t *testing.T) {
	for _, tc := range []struct {
		name  string
		actor Actor
	}{
		{"缺 admin_user_id", Actor{Email: "a@b.c", IP: netip.MustParseAddr("1.2.3.4")}},
		{"admin_user_id 为负", Actor{AdminID: -1, Email: "a@b.c", IP: netip.MustParseAddr("1.2.3.4")}},
		{"缺 admin_email_snapshot", Actor{AdminID: 7, IP: netip.MustParseAddr("1.2.3.4")}},
		{"缺来源 IP", Actor{AdminID: 7, Email: "a@b.c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBeginner()
			ran := false
			err := InTx(context.Background(), b, tc.actor,
				func(context.Context, *dbgen.Queries) (Entry, error) {
					ran = true
					return testEntry(), nil
				})

			if err == nil {
				t.Fatal("Actor 不完整必须失败")
			}
			if b.begins != 0 {
				t.Fatal("不完整的 Actor 连事务都不该开")
			}
			if ran {
				t.Fatal("业务函数不该被执行")
			}
		})
	}
}

// 来源 IP 缺失时**不能**回退到 0.0.0.0：这张表是证据，
// 一条写着 0.0.0.0 的审计记录会在事后被当成真实来源读，而它什么都没说。
func TestWriteRefusesInvalidIPRatherThanFallingBack(t *testing.T) {
	tx := &fakeTx{}
	a := testActor()
	a.IP = netip.Addr{} // 零值 = 非法

	if err := Write(context.Background(), tx, a, testEntry()); err == nil {
		t.Fatal("非法来源 IP 必须让审计写失败 —— 宁可响亮地失败，也不要写一条说谎的证据")
	}
	if len(tx.execs) != 0 {
		t.Fatal("校验没过就不该发 SQL")
	}
}

// 开事务本身失败要原样报出来，不能被吞掉变成「操作成功」。
func TestInTxBeginFailureIsReported(t *testing.T) {
	b := newBeginner()
	b.err = errors.New("连接池耗尽")

	if err := InTx(context.Background(), b, testActor(),
		func(context.Context, *dbgen.Queries) (Entry, error) { return testEntry(), nil }); err == nil {
		t.Fatal("开事务失败必须报出来")
	}
}

// ---- 参数映射 ----

// nil 快照 → SQL NULL，而不是 JSON 的 `null` 字面量：
// 两者在 jsonb 里是不同的值，`WHERE before_value IS NULL` 只命中前者。
// 空 reason / 空 UA 同理 —— 存空串会让「没填」与「填了个空」不可区分。
func TestWriteMapsNilSnapshotsToSQLNull(t *testing.T) {
	tx := &fakeTx{}
	e := testEntry()
	e.Before = nil // 创建操作没有 before
	e.After = nil
	e.Reason = ""
	a := testActor()
	a.UserAgent = ""

	if err := Write(context.Background(), tx, a, e); err != nil {
		t.Fatalf("应写入成功，实得 %v", err)
	}
	if len(tx.execArgs) != 1 {
		t.Fatalf("应发一条 SQL，实得 %d", len(tx.execArgs))
	}
	args := tx.execArgs[0]
	if len(args) != 10 {
		t.Fatalf("insertSQL 有 10 个占位符，实得 %d 个参数", len(args))
	}
	// $6 before_value / $7 after_value：必须是 nil（SQL NULL），不是 []byte("null")。
	for i, name := range map[int]string{5: "before_value", 6: "after_value"} {
		b, ok := args[i].([]byte)
		if !ok {
			t.Fatalf("%s 参数类型 = %T", name, args[i])
		}
		if b != nil {
			t.Fatalf("%s 应为 SQL NULL，实得 %q —— JSON null 与 SQL NULL 在 jsonb 里不是同一个值", name, b)
		}
	}
	// $8 reason / $10 user_agent：空串必须变成 nil。
	for i, name := range map[int]string{7: "reason", 9: "user_agent"} {
		p, ok := args[i].(*string)
		if !ok {
			t.Fatalf("%s 参数类型 = %T", name, args[i])
		}
		if p != nil {
			t.Fatalf("%s 空值应为 SQL NULL，实得 %q", name, *p)
		}
	}
}

// 快照序列化失败（比如塞了一个 chan）必须让整条审计写失败，
// 从而让整个业务操作回滚 —— 不能悄悄写一条 before/after 为空的记录。
func TestWriteFailsOnUnserializableSnapshot(t *testing.T) {
	tx := &fakeTx{}
	e := testEntry()
	e.After = make(chan int)

	if err := Write(context.Background(), tx, testActor(), e); err == nil {
		t.Fatal("快照序列化失败必须报错")
	}
	if len(tx.execs) != 0 {
		t.Fatal("序列化失败就不该发 SQL")
	}
}

// UA 是完全由调用方控制的字符串，而 audit_logs 是 append-only、永不删除的表。
// 不截断的话，一次带 8 MB UA 头的管理操作会往这张表里塞 8 MB。
func TestWriteTruncatesUserAgent(t *testing.T) {
	tx := &fakeTx{}
	a := testActor()
	a.UserAgent = strings.Repeat("A", maxUserAgentLen*3)

	if err := Write(context.Background(), tx, a, testEntry()); err != nil {
		t.Fatalf("应写入成功，实得 %v", err)
	}
	p, ok := tx.execArgs[0][9].(*string)
	if !ok || p == nil {
		t.Fatalf("user_agent 参数 = %#v", tx.execArgs[0][9])
	}
	if len(*p) > maxUserAgentLen {
		t.Fatalf("UA 应被截到 %d 字节，实得 %d", maxUserAgentLen, len(*p))
	}
}

// 直接 s[:n] 会在多字节字符中间切开，产生非法 UTF-8 —— 而 Postgres 的 text 列
// 会**拒绝**它（不是静默替换），于是一次带中文 UA 的管理操作会因为审计写失败而整个回滚。
func TestTruncateUTF8NeverSplitsARune(t *testing.T) {
	// 每个汉字 3 字节：截断点会落在字符中间。
	s := strings.Repeat("中", 300)
	for _, max := range []int{1, 2, 3, 4, 5, 100, 512, 899, 900} {
		got := truncateUTF8(s, max)
		if len(got) > max {
			t.Fatalf("max=%d 时长度 %d 超限", max, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d 时切出了非法 UTF-8 —— Postgres 的 text 列会拒绝它", max)
		}
	}
	if truncateUTF8("short", 512) != "short" {
		t.Fatal("未超限的串不该被动")
	}
}

// ---- 只有 INSERT ----

// 表本身由 DB 层 REVOKE UPDATE/DELETE 强制，本包也只提供 INSERT。
// §6.1：「一个能被清理的审计日志等于没有审计日志」。
func TestInsertSQLIsAppendOnly(t *testing.T) {
	up := strings.ToUpper(insertSQL)
	if !strings.Contains(up, "INSERT INTO AUDIT_LOGS") {
		t.Fatal("insertSQL 不是往 audit_logs 插入")
	}
	for _, forbidden := range []string{"UPDATE ", "DELETE ", "ON CONFLICT", "TRUNCATE"} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("审计写入语句里出现了 %q —— 审计表是 append-only", forbidden)
		}
	}
}
