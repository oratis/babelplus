package subgen

import (
	"fmt"
	"strings"
)

// 为什么手写 YAML 而不用 yaml 库
//
// 1. **字节级可预期。** ADR 0006 §12 要求每次改订阅格式都人工用 Clash Verge Rev
//    加载一次并与 api-contract.md §4.5 的示例逐行对照。序列化库会按自己的规则
//    决定引号、缩进与键序（map 还会排序），示例与产出对不上时无法判断
//    「是格式变了还是库的风格变了」。
// 2. **本仓当前没有直接依赖任何 yaml 库。** go.mod 里的 oasdiff/yaml3 是
//    oapi-codegen 的**间接**依赖，拿它当运行时依赖等于把生成工具链的版本
//    绑进下发路径 —— 它升一次版，用户的订阅就可能换一种引号风格。
// 3. 我们要输出的结构是**固定的**：六个顶层标量 + proxies + proxy-groups。
//    没有任意嵌套，手写的表达力完全够用。
//
// 代价（明确承认）：转义规则要自己保证正确。所以**所有数据来源的字符串一律
// 双引号 + 转义**，只有代码里写死的关键字（`vless` / `tcp` / `fallback`）才裸写。
// 双引号风格的 YAML 转义规则与 JSON 基本一致，是三种引号风格里最不容易出错的。

// yamlString 把任意字符串编码成 YAML 双引号标量。
//
// 双引号风格允许 \ 转义序列，规则与 JSON 同源：必须转义反斜杠与双引号，
// 控制字符用 \xNN。非 ASCII（中文、emoji、`·`）**不转义**，直接以 UTF-8 输出 ——
// YAML 1.2 的字符集就是 UTF-8，转成 \uXXXX 只会让人工核对变得不可能。
func yamlString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\x%02x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// yamlStringList 编码成流式序列 ["a", "b"]。
// 分组成员用流式而不是块式，是为了让一个组占一行，人工核对时一眼看全。
func yamlStringList(items []string) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, yamlString(it))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// yamlWriter 是一个只管缩进的极薄辅助器。
type yamlWriter struct {
	b strings.Builder
}

// line 写一行，indent 为缩进层级（每级两个空格）。
func (w *yamlWriter) line(indent int, format string, args ...any) {
	w.b.WriteString(strings.Repeat("  ", indent))
	if len(args) == 0 {
		w.b.WriteString(format)
	} else {
		fmt.Fprintf(&w.b, format, args...)
	}
	w.b.WriteByte('\n')
}

// blank 写一个空行。
func (w *yamlWriter) blank() { w.b.WriteByte('\n') }

func (w *yamlWriter) bytes() []byte { return []byte(w.b.String()) }
