package main

import (
	"reflect"
	"testing"

	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/handler"
)

// allOperationIDs 用反射列出 StrictServerInterface 的全部方法名。
//
// 方法名与 openapi 的 operationId 一一对应（oapi-codegen 只做首字母大写），
// 所以它是「契约里到底有哪些 operation」在 Go 侧唯一不会过期的事实源 ——
// 比对 operations.txt 那种文本清单会在有人忘了跑 make gen-stubs 时一起过期。
func allOperationIDs() []string {
	t := reflect.TypeOf((*gen.StrictServerInterface)(nil)).Elem()
	names := make([]string, 0, t.NumMethod())
	for i := range t.NumMethod() {
		names = append(names, t.Method(i).Name)
	}
	return names
}

// TestOperationAuthCoverage 是本仓最重要的一条装配测试。
//
// 它同时挡住两类**运行时完全静默**的错误：
//  1. 表里写了一个不存在的 operationID（拼写错误、契约改名）——
//     那一行永远不会被命中，对应的真 operation 落到 default 分支；
//  2. 契约新增了一个 operation 而忘了分类 —— 它会以「未分类」身份存在，
//     而未分类在旧版本里意味着**原样放行、不做任何鉴权**。
//
// 历史事故：`PushUniProxyStatus` 曾被写成 `GetUniProxyStatus`（见 authmap.go 顶部）。
func TestOperationAuthCoverage(t *testing.T) {
	ops := allOperationIDs()
	if len(ops) == 0 {
		t.Fatal("反射没拿到任何 operation，说明 gen 包被改坏了")
	}

	// operation → 命中的表名列表。长度必须恰好为 1。
	hits := make(map[string][]string, len(ops))
	known := make(map[string]bool, len(ops))
	for _, op := range ops {
		known[op] = true
		hits[op] = nil
	}

	mark := func(table string, keys []string) {
		for _, k := range keys {
			if !known[k] {
				t.Errorf("%s 里的 %q 不是任何 operation 的名字（拼写错误？契约改名？）"+
					"—— 这一行永远不会被命中，对应的真 operation 会落到 default 分支", table, k)
				continue
			}
			hits[k] = append(hits[k], table)
		}
	}

	mark("handler.PublicOperations", keysOfBool(handler.PublicOperations))
	mark("nodeOperationScopes", keysOfString(nodeOperationScopes))
	mark("userSessionOperations", keysOfBool(userSessionOperations))
	mark("adminOperations", keysOfBool(adminOperations))
	mark("internalTaskOperations", keysOfBool(internalTaskOperations))

	for _, op := range ops {
		switch n := len(hits[op]); {
		case n == 0:
			t.Errorf("operation %q 没有被任何一张鉴权表分类 —— "+
				"新增端点必须同时在 authmap.go 里声明它需要哪种凭据", op)
		case n > 1:
			t.Errorf("operation %q 同时出现在 %v —— 分类必须互斥，"+
				"否则实际生效的是 authMiddleware 里 switch 的先后顺序，而那不是任何人写下来的意图", op, hits[op])
		}
	}
}

// TestOperationAuthCounts 钉住每一类的数量。
//
// 覆盖性测试保证「不重不漏」，但保证不了「没有被整类挪错桌」：
// 把 41 个用户面 operation 整体搬进 PublicOperations 依然满足互斥且全覆盖。
// 这里的数字来自 openapi.yaml 的 security 段，改契约时必须一起改。
func TestOperationAuthCounts(t *testing.T) {
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"handler.PublicOperations", len(handler.PublicOperations), 11},
		{"nodeOperationScopes", len(nodeOperationScopes), 7},
		{"userSessionOperations", len(userSessionOperations), 42},
		{"adminOperations", len(adminOperations), 61},
		{"internalTaskOperations", len(internalTaskOperations), 9},
		{"StrictServerInterface 方法数", len(allOperationIDs()), 130},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d，期望 %d", c.name, c.got, c.want)
		}
	}
}

// TestNodeScopesNonEmpty 确认每个节点面 operation 都声明了非空 scope。
//
// authMiddleware 用 `nodeOperationScopes[op] != ""` 判定「是不是节点面」，
// 所以空字符串的 scope 不是「不限 scope」而是「这个 operation 不受节点鉴权保护」。
func TestNodeScopesNonEmpty(t *testing.T) {
	for op, scope := range nodeOperationScopes {
		if scope == "" {
			t.Errorf("节点面 operation %q 的 scope 为空 —— 它会被当作非节点面而绕过鉴权", op)
		}
	}
}

func keysOfBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	return out
}

func keysOfString(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
