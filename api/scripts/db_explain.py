#!/usr/bin/env python3
"""把 db/gen/*.sql.go 里的每一条 SQL 生成成一份 EXPLAIN 脚本，交给 psql 跑。

为什么需要这一步（ADR 0013 §6.4）：
    sqlc 与 go build **都不校验 SQL 的语义**。`api/sqlc.yaml` 没有 `database:` 段，
    内置引擎只做语法与列名解析；`go build` 看到的只是一个字符串常量。
    于是「往一个 GENERATED ALWAYS 列上写值」这种错误可以一路穿过
    `sqlc generate`（exit 0）与 `go build`（通过），躺在生成物里等着 ——
    第一次暴露点是**生产环境里第一笔付款成功之后的 ApplyUserEntitlement**：
    用户付了钱，订单进 paid，开通权利时 500。

    EXPLAIN 会做完整的 parse analysis + rewrite + plan，这类错误在那里就报出来了。
    实测（postgres:17）：
        EXPLAIN (GENERIC_PLAN) UPDATE users SET transfer_enable = $1 WHERE id = $2;
        ERROR:  column "transfer_enable" can only be updated to DEFAULT
    而同一条语句 sqlc generate 与 go build 全部 exit 0。

为什么读 db/gen/*.sql.go 而不是 db/queries/*.sql：
    queries 里有 `sqlc.narg(x)` / `@server_id` 这类**不是合法 SQL** 的写法，
    直接喂给 psql 会在解析阶段就废掉。生成物里它们已经被替换成 $N，
    而且那才是**真正发到 Postgres 的那份字符串** —— 检查它，检查的就是线上跑的东西。

为什么是 GENERIC_PLAN：
    这些语句带 $N 参数，普通 EXPLAIN 没法绑值。GENERIC_PLAN（PG 16+）就是为这个场景加的：
    不需要参数值，照样走完整的分析与规划。

    ⚠️ EXPLAIN **不执行**语句，所以它抓不到运行期才成立的约束
    （NOT NULL 违反、CHECK 违反、外键）。那一类要靠真的写一行数据来抓 ——
    见 .github/workflows/ci.yml 的「回滚后真的写一行」步骤。

用法：
    python3 scripts/db_explain.py | psql -v ON_ERROR_STOP=1
    make db-explain        # 本地：起库 → 灌 up → 跑本检查
"""

from __future__ import annotations

import pathlib
import re
import sys

GEN_DIR = pathlib.Path(__file__).resolve().parent.parent / "db" / "gen"

# const addUserTransferQuota = `-- name: AddUserTransferQuota :one
CONST_RE = re.compile(r"^const (\w+) = `-- name: (\w+) :(\S+)$")

# 判定「是不是写语句」时先把行注释剥掉，免得注释里的 update 之类的词造成误判。
# 本仓库的 SQL 字面量里不含 `--`，所以这个粗暴的剥法是安全的。
COMMENT_RE = re.compile(r"--[^\n]*")
WRITE_RE = re.compile(r"\b(insert\s+into|update\s|delete\s+from)\b", re.IGNORECASE)


def extract(path: pathlib.Path) -> list[tuple[str, str, str]]:
    """返回 [(query_name, kind, sql), ...]。"""
    out: list[tuple[str, str, str]] = []
    lines = path.read_text(encoding="utf-8").splitlines()
    i = 0
    while i < len(lines):
        m = CONST_RE.match(lines[i])
        if not m:
            i += 1
            continue
        name, kind = m.group(2), m.group(3)
        body: list[str] = []
        i += 1
        while i < len(lines) and lines[i] != "`":
            body.append(lines[i])
            i += 1
        if i >= len(lines):
            sys.exit(f"{path.name}: 常量 {name} 的反引号没有闭合，脚本的解析假设已经不成立")
        out.append((name, kind, "\n".join(body).strip()))
        i += 1
    return out


def main() -> int:
    files = sorted(GEN_DIR.glob("*.sql.go"))
    if not files:
        sys.exit(f"{GEN_DIR} 下一个 *.sql.go 都没有 —— 先跑 make gen-db")

    stmts = [(f.name, *q) for f in files for q in extract(f)]
    if not stmts:
        sys.exit("一条 SQL 都没抽出来。生成物的形状变了，本脚本失效 —— 先修脚本，别把它跳过去")

    writes = [s for s in stmts if WRITE_RE.search(COMMENT_RE.sub("", s[3]))]
    if not writes:
        sys.exit("抽出来的语句里一条写语句都没有 —— 那正是本检查要盯的东西，不可能为 0")

    emit = print
    emit("-- 由 scripts/db_explain.py 生成，不要手改。")
    emit(f"-- 共 {len(stmts)} 条语句，其中写语句 {len(writes)} 条。")
    emit("\\set ON_ERROR_STOP on")
    emit("\\o /dev/null")  # 丢掉执行计划本身；\echo 与错误照常走 stdout/stderr
    for filename, name, kind, sql in stmts:
        mark = "W" if (filename, name, kind, sql) in writes else "r"
        emit(f"\\echo '  [{mark}] {filename}: {name}'")
        emit(f"EXPLAIN (GENERIC_PLAN, COSTS OFF)\n{sql};")
    emit("\\o")
    emit(f"\\echo '✅ {len(stmts)} 条语句全部通过语义校验（其中 {len(writes)} 条是写语句）'")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
