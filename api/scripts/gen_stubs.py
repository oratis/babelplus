#!/usr/bin/env python3
"""从 oapi-codegen 生成的 StrictServerInterface 重建 501 stub 实现。

为什么需要这一步：
    strict-server 模式下 StrictServerInterface 有 128 个方法，Server 必须全部实现
    才能编译。如果 handler 包里只写已实现的那几个，整个包编译不过 —— 那样每加一个
    OpenAPI 端点就得手写一个 stub，纯机械劳动且容易漏。

    这个脚本把「全量 stub」变成生成物：Server 嵌入 Unimplemented，只覆盖已实现的方法。
    新增端点后重新生成，Server 依然编译通过，新端点自动落到 501。

代价（unimplemented.gen.go 顶部也记了一遍）：
    **漏实现不会在编译期暴露。** 兜底靠 operations.txt 的实现进度清单与集成测试。

用法：
    make gen-stubs        # 或 python3 scripts/gen_stubs.py
"""

from __future__ import annotations

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
GEN = ROOT / "internal" / "gen" / "api.gen.go"
OUT = ROOT / "internal" / "handler" / "unimplemented.gen.go"
OPS = ROOT / "internal" / "handler" / "operations.txt"

HEADER = '''// Package handler 实现 OpenAPI 生成的 StrictServerInterface。
//
// 本文件由 `make gen-stubs` 从 internal/gen/api.gen.go 自动生成，**不要手改**。
//
// 设计：Unimplemented 提供全部 operation 的默认实现（一律返回 ErrNotImplemented）。
// 真实实现放在同包的其他文件里，通过 Server 结构体嵌入 Unimplemented 并覆盖对应方法。
//
// 这样做的好处：改 openapi.yaml 新增 operation 后重新生成本文件，Server 依然能编译
// 通过（新 operation 自动落到 501），而不是整个包编译失败。
// 代价是**漏实现不会在编译期暴露** —— 靠 operations.txt 与集成测试兜底。
package handler

import (
	"context"
	"errors"

	"github.com/oratis/babelplus/api/internal/gen"
)

// ErrNotImplemented 由尚未实现的 operation 返回，错误映射会把它转成 501。
var ErrNotImplemented = errors.New("not implemented")

// Unimplemented 是 StrictServerInterface 的全量默认实现。
type Unimplemented struct{}

var _ gen.StrictServerInterface = (*Unimplemented)(nil)
'''

METHOD_RE = re.compile(
    r"^\t([A-Z][A-Za-z0-9]*)\(ctx context\.Context, request (\w+)\) \((\w+), error\)",
    re.M,
)


def main() -> int:
    if not GEN.exists():
        print(f"找不到生成文件 {GEN}，先跑 `make gen-api`", file=sys.stderr)
        return 1

    src = GEN.read_text(encoding="utf-8")
    m = re.search(r"^type StrictServerInterface interface \{(.*?)^\}", src, re.S | re.M)
    if not m:
        print("在 api.gen.go 里找不到 StrictServerInterface —— 生成配置是否改过？", file=sys.stderr)
        return 1

    methods = METHOD_RE.findall(m.group(1))
    if not methods:
        print("StrictServerInterface 里没解析出任何方法", file=sys.stderr)
        return 1

    parts = [HEADER]
    for name, req, resp in methods:
        parts.append(
            f"\n// {name} 尚未实现。\n"
            f"func (Unimplemented) {name}(_ context.Context, _ gen.{req}) (gen.{resp}, error) {{\n"
            f"\treturn nil, ErrNotImplemented\n"
            f"}}"
        )

    OUT.write_text("\n".join(parts) + "\n", encoding="utf-8")
    OPS.write_text("\n".join(sorted(n for n, _, _ in methods)) + "\n", encoding="utf-8")

    print(f"已生成 {len(methods)} 个 stub → {OUT.relative_to(ROOT)}")
    print(f"operation 清单 → {OPS.relative_to(ROOT)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
