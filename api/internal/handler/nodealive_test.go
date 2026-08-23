package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oratis/babelplus/api/internal/middleware"
)

// bp_node_alive 的单测。
//
// 这条日志的特殊之处：**它是一条告警的唯一数据来源，而那条告警是 metric absence**。
// 也就是说这里出任何问题（文案改了、字段名改了、降频把它降没了、
// 新端点忘了调），现象都是「告警不响」—— 一个在故障发生之前完全静默的故障。
// 所以它的测试比大多数日志都值得写。

func newAliveTestServer() (*Server, *bytes.Buffer) {
	var buf bytes.Buffer
	return &Server{
		logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, &buf
}

func aliveRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("日志不是 JSON：%s", line)
		}
		if m["msg"] == nodeAliveMessage {
			out = append(out, m)
		}
	}
	return out
}

// ============================================================
// 日志形状
// ============================================================

// TestNoteNodeAlive_LogShape 钉住三件与 GCP 侧配置强耦合的事：
//
//  1. 文案**恰好**是 bp_node_alive —— 它就是指标名，过滤器直接匹配它；
//  2. 带 node_id 字段 —— monitoring.md §5.1 的 groupByFields 是 metric.label.node_id；
//  3. node_id 的值是**字符串** —— label 本身是字符串类型，写成数字要多依赖一次
//     隐式转换，而那一步出错的现象是「指标建出来了但没有标签」，
//     于是按 node_id 分组的缺失告警退化成「全部节点都挂了才报」。
func TestNoteNodeAlive_LogShape(t *testing.T) {
	s, buf := newAliveTestServer()
	s.noteNodeAlive(context.Background(), &middleware.NodeAuth{ServerID: 7, ServerCode: "jp-tokyo-01"})

	recs := aliveRecords(t, buf)
	if len(recs) != 1 {
		t.Fatalf("期望恰好 1 条 %s 日志，得到 %d 条（全部日志：%s）", nodeAliveMessage, len(recs), buf.String())
	}
	rec := recs[0]

	nodeID, ok := rec["node_id"]
	if !ok {
		t.Fatalf("日志里没有 node_id 字段 —— 缺失告警无法按节点分组，退化成「全挂才报」")
	}
	if s, isString := nodeID.(string); !isString || s != "7" {
		t.Fatalf("node_id = %#v，期望字符串 \"7\"（log-based metric 的 label 是字符串类型）", nodeID)
	}
	if rec["server_code"] != "jp-tokyo-01" {
		t.Fatalf("server_code = %#v，期望 jp-tokyo-01（值班要靠它把 node_id 对回具体那台）", rec["server_code"])
	}
}

// ============================================================
// 降频
// ============================================================

// TestNodeAliveThrottle 钉住降频窗口的三个边界。
//
// 降频错在两个方向都很贵：
//   - 降得不够 → 10 节点 × 4 次/分钟 = 7.2 万条/天的心跳日志，纯烧钱；
//   - 降得太狠 → 5 分钟的告警窗口里采样点不足，一次抖动就误报「节点挂了」。
func TestNodeAliveThrottle(t *testing.T) {
	base := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	var th nodeAliveThrottle // 零值必须可用

	if !th.due(1, base, nodeAliveInterval) {
		t.Fatalf("某个节点的第一次上报必须记录 —— metric absence 需要 time series 先存在过")
	}
	if th.due(1, base.Add(nodeAliveInterval-time.Nanosecond), nodeAliveInterval) {
		t.Fatalf("间隔未满就又记了一条，降频没生效")
	}
	if !th.due(1, base.Add(nodeAliveInterval), nodeAliveInterval) {
		t.Fatalf("间隔满了必须再记一条，否则告警窗口里会没有采样点")
	}
	if !th.due(2, base, nodeAliveInterval) {
		t.Fatalf("节点之间必须互相独立 —— 否则一个高频节点会把其他节点的心跳全压掉，"+
			"而那正好是「8 个里挂了 1 个」检测不出来的场景（node_id=%d）", 2)
	}
}

// TestNodeAliveThrottle_Concurrent 钉住「判断与记录在同一次加锁里完成」。
//
// 写成「先问再记」两步的话，同一节点的并发请求会全部拿到 true，降频等于没有 ——
// 而节点面恰恰是全系统唯一的高频路径，并发是常态不是边角。
func TestNodeAliveThrottle_Concurrent(t *testing.T) {
	var th nodeAliveThrottle
	fixed := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	const goroutines = 100
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		trues int
	)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if th.due(42, fixed, nodeAliveInterval) {
				mu.Lock()
				trues++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if trues != 1 {
		t.Fatalf("同一时刻 %d 个并发请求里应当只有 1 个记录心跳，实际 %d 个", goroutines, trues)
	}
}

// TestNoteNodeAlive_Throttled 从 noteNodeAlive 这一层再确认一次降频真的接上了。
func TestNoteNodeAlive_Throttled(t *testing.T) {
	s, buf := newAliveTestServer()
	auth := &middleware.NodeAuth{ServerID: 3, ServerCode: "us-lax-01"}
	for i := 0; i < 10; i++ {
		s.noteNodeAlive(context.Background(), auth)
	}
	if n := len(aliveRecords(t, buf)); n != 1 {
		t.Fatalf("连续 10 次上报应只写 1 条心跳日志，实际 %d 条", n)
	}
}

// ============================================================
// 覆盖面（漂移防护）
// ============================================================

// TestEveryUniProxyHandlerRecordsHeartbeat 扫 node.go 的语法树，
// 确认**每一个** UniProxy handler 都调了 noteNodeAlive。
//
// 为什么需要一个源码级的测试：漏调不会有任何编译错误、任何运行时报错，
// 只会让那个端点不再贡献心跳采样点。如果漏的恰好是最后一个还在贡献采样点的端点，
// 结果就是一个活得好好的节点被 metric absence 判成离线，
// 值班在半夜被叫起来去查一台没问题的机器。
//
// 用例数写死成 5 是**故意**的：将来实现 PushUniProxyStatus（当前是 501 stub）时
// 本用例会失败，失败信息会告诉你「新端点也要调 noteNodeAlive，然后把这里改成 6」。
// 那正是我们希望有人停下来想一秒的时刻。
func TestEveryUniProxyHandlerRecordsHeartbeat(t *testing.T) {
	const wantHandlers = 5

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "node.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 node.go 失败: %v", err)
	}

	found := 0
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Body == nil {
			continue
		}
		if !strings.HasPrefix(fn.Name.Name, "GetUniProxy") && !strings.HasPrefix(fn.Name.Name, "PushUniProxy") {
			continue
		}
		found++
		if !callsNoteNodeAlive(fn.Body) {
			t.Errorf("%s 没有调用 s.noteNodeAlive —— 该端点不再贡献 bp_node_alive 采样点", fn.Name.Name)
		}
	}

	if found != wantHandlers {
		t.Fatalf("node.go 里有 %d 个 UniProxy handler，用例按 %d 个写的。\n"+
			"如果你刚加了一个端点：先在它里面调 s.noteNodeAlive（node_id 校验通过之后），再把 wantHandlers 改成 %d",
			found, wantHandlers, found)
	}
}

func callsNoteNodeAlive(body *ast.BlockStmt) bool {
	hit := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "noteNodeAlive" {
			hit = true
			return false
		}
		return true
	})
	return hit
}
