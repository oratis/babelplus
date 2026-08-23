package handler

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// truncateUA 的返回值会作为 user_agent（text 列）写进 subscription_fetch_log。
// Postgres 对 text 做 UTF-8 编码校验，非法字节序列会让整条 INSERT 报 22021 失败 ——
// 而失败路径只打一条 ERROR、订阅照常 200 下发，于是这次拉取**在审计表里没有留痕**。
// UA 完全由调用方控制，所以「能不能构造出非法序列」等价于
// 「被审计者能不能自己关掉审计」。这张表是识别账号共享的唯一数据来源。
func TestTruncateUAAlwaysValidUTF8(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
	}{
		{
			// 曾经的实现 ua[:512] 会把这个 é 从中间切开，留下孤立的 0xC3。
			name: "多字节字符正好跨在 512 字节边界上",
			in:   strings.Repeat("a", 511) + "é",
		},
		{
			name: "边界处是 3 字节字符",
			in:   strings.Repeat("a", 510) + "中" + strings.Repeat("b", 100),
		},
		{
			name: "边界处是 4 字节 emoji",
			in:   strings.Repeat("a", 509) + "🚀" + strings.Repeat("b", 100),
		},
		{
			// HTTP 头的 value 允许 obs-text（≥0x80 的裸字节），
			// 调用方可以直接送一段本来就非法的 UTF-8，与截断无关。
			name: "输入本身就是非法 UTF-8（未超长）",
			in:   "Mozilla/5.0 \xff\xfe bad",
		},
		{
			name: "输入本身非法且超长",
			in:   strings.Repeat("\xff", 600),
		},
		{
			name: "正常 UA",
			in:   "ClashforWindows/0.20.39",
		},
		{
			name: "空",
			in:   "",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := truncateUA(c.in)
			if !utf8.ValidString(got) {
				t.Errorf("结果不是合法 UTF-8（Postgres 会拒绝这条 INSERT）：%q", got)
			}
			if len(got) > 512 {
				t.Errorf("结果 %d 字节，超过 512 上限", len(got))
			}
		})
	}
}

// 没超长的合法 UA 必须原样返回 —— 审计表里记的应当是客户端真正发来的那一串。
func TestTruncateUAKeepsShortInputIntact(t *testing.T) {
	const ua = "ClashforWindows/0.20.39 中文也要原样保留"
	if got := truncateUA(ua); got != ua {
		t.Errorf("truncateUA(%q) = %q，期望原样返回", ua, got)
	}
}

// 截断只应发生在超长时，且要尽量贴近上限（不能因为对齐 rune 边界就砍掉一大截）。
func TestTruncateUACutsCloseToLimit(t *testing.T) {
	got := truncateUA(strings.Repeat("中", 400)) // 1200 字节
	if len(got) > 512 {
		t.Fatalf("结果 %d 字节，超过上限", len(got))
	}
	// 3 字节字符对齐后最多少 2 字节。
	if len(got) < 512-3 {
		t.Errorf("结果只有 %d 字节，离上限 512 太远", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("结果不是合法 UTF-8：%q", got)
	}
}
