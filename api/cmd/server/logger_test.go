package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

// 日志字段名的回归测试。
//
// # 为什么这一条比它看起来重要得多
//
// monitoring.md §3.2 的每一个 log-based metric 都是按 `jsonPayload.message` 过滤的：
//
//	bp_node_alive         → jsonPayload.message="bp_node_alive"
//	bp_ratelimit_degraded → 同上
//	bp_subscribe_404      → 同上
//
// 而 slog 的 JSONHandler 默认把消息写成 **`msg`**，不是 `message`。
// 两者之间只隔着 newLoggerTo 里那三行 ReplaceAttr。删掉它们：
//
//   - 编译通过；
//   - 所有单测通过（handler 层的测试自己建 logger，用的是默认键名）；
//   - 服务照常运行、日志照常输出；
//   - **每一条 log-based metric 同时停止匹配，于是每一条依赖它们的告警一起静默失灵。**
//
// 也就是说，这是一个「只在故障发生时才显形的故障」—— 恰恰是 bp_node_alive
// 与 bp_ratelimit_degraded 这两条日志本身要防的那一类。它必须被钉住。
func TestLoggerFieldNamesMatchCloudLoggingFilters(t *testing.T) {
	var buf bytes.Buffer
	l := newLoggerTo(&buf, "info")

	// 用真实的指标名做载荷，这样断言失败时的现场直接指向受影响的那条告警。
	l.Error("bp_ratelimit_degraded", "bucket", "login_ip_1m")

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("日志不是 JSON：%v（原文：%s）", err, buf.String())
	}

	if _, ok := rec[slog.MessageKey]; ok {
		t.Errorf("日志里出现了 slog 默认的 %q 键 —— Cloud Logging 的过滤器写的是 jsonPayload.message，"+
			"留着它意味着全部 log-based metric 都匹配不上", slog.MessageKey)
	}
	if got := rec["message"]; got != "bp_ratelimit_degraded" {
		t.Errorf("message = %#v，期望 %q（monitoring.md §3.2 的过滤器按 jsonPayload.message 匹配）",
			got, "bp_ratelimit_degraded")
	}

	if _, ok := rec[slog.LevelKey]; ok {
		t.Errorf("日志里出现了 slog 默认的 %q 键 —— Cloud Logging 用 severity 判级别，"+
			"留着它会让 ERROR 被当成 DEFAULT，按 severity 过滤的告警全部漏报", slog.LevelKey)
	}
	if got := rec["severity"]; got != "ERROR" {
		t.Errorf("severity = %#v，期望 \"ERROR\"", got)
	}

	// 结构化字段必须留在顶层：label-extractors 写的是 EXTRACT(jsonPayload.node_id) 这种
	// **一级路径**，字段被套进子对象就取不到了（现象是「指标建出来了但没有标签」）。
	if got := rec["bucket"]; got != "login_ip_1m" {
		t.Errorf("bucket = %#v，期望 %q（label-extractors 取的是 jsonPayload 的一级字段）",
			got, "login_ip_1m")
	}
}

// TestLoggerNodeAliveLabelIsExtractable 走一遍 bp_node_alive 那条指标真正要的形状。
//
// monitoring.md §3.2 的建指标命令是：
//
//	--log-filter='… AND jsonPayload.message="bp_node_alive"'
//	--label-extractors='node_id=EXTRACT(jsonPayload.node_id)'
//
// 这里断言的就是这两行各自要读的东西确实在它们要读的位置上，
// 且 node_id 是**字符串**（log-based metric 的 label 本身是字符串类型）。
func TestLoggerNodeAliveLabelIsExtractable(t *testing.T) {
	var buf bytes.Buffer
	newLoggerTo(&buf, "info").Info("bp_node_alive", "node_id", "7", "server_code", "jp-tokyo-01")

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("日志不是 JSON：%v（原文：%s）", err, buf.String())
	}
	if rec["message"] != "bp_node_alive" {
		t.Fatalf("message = %#v，过滤器匹配不上", rec["message"])
	}
	nodeID, ok := rec["node_id"].(string)
	if !ok || nodeID != "7" {
		t.Fatalf("node_id = %#v，期望字符串 \"7\" —— 缺失告警要按 metric.label.node_id 分组", rec["node_id"])
	}
}
