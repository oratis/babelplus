#!/usr/bin/env bash
# 核对 docs/evidence/ 的两条约定是否还成立。
#
# ── 为什么有这个脚本 ────────────────────────────────────────────────────────
# `docs/evidence/README.md` 头一节写着「每个证据目录**必须有 README.md**」，
# 并且自己维护一张「已完成」表。两条约定都是**手写的，此前没有任何东西在核对**。
# 结果是同一个洞掉了两次：
#   2026-08-29  表里 8 行、目录 9 个 —— 漏了 ipv6-censorship（且它连 README 都没有）
#   2026-09-04  表里 9 行、目录 14 个 —— 漏了 2026-09-01/02 的五个
# 第一次的处置是「写下教训」。六天后又漏五个，说明**写下教训不是机制**。
# 这个脚本就是那个机制。
#
# 判据三条，全部致命：
#   1. 每个 docs/evidence/<dir>/ 都有 README.md
#   2. 每个目录都在 docs/evidence/README.md 里被链接到（形如 `](<dir>/)`）
#   3. 表里链接到的目录都真实存在（防止改名之后表里留下死链）
#
# 刻意**不**检查的：行数、顺序、每行写了什么。那些是判断，不是事实，
# 交给评审的人 —— 一个会误报的检查很快就会被人加 `|| true` 绕过去。
#
# 用法：从仓库根目录跑 `infra/scripts/check-evidence-index.sh`
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
dir="$root/docs/evidence"
index="$dir/README.md"

[ -f "$index" ] || { echo "::error::找不到 $index"; exit 1; }

fail=0
found=0

for d in "$dir"/*/; do
  [ -d "$d" ] || continue
  name="$(basename "$d")"
  found=$((found + 1))

  if [ ! -f "$d/README.md" ]; then
    echo "::error file=docs/evidence/$name::缺 README.md。约定见 docs/evidence/README.md 头一节：每个证据目录必须有 README，且必须写「这些证据证明什么、不证明什么」"
    fail=1
  fi

  if ! grep -qF "]($name/)" "$index"; then
    echo "::error file=docs/evidence/README.md::「已完成」表里没有 $name。补一行：| [$name]($name/) | <解决了什么> |"
    fail=1
  fi
done

# 反向：表里链到的目录必须存在。
while IFS= read -r name; do
  [ -d "$dir/$name" ] || {
    echo "::error file=docs/evidence/README.md::表里链到的 $name/ 不存在（改名或删目录时忘了改表？）"
    fail=1
  }
done < <(grep -oE '\]\([a-z0-9][a-z0-9.-]*/\)' "$index" | sed -E 's/^\]\(//; s|/\)$||' | sort -u)

if [ "$fail" -eq 0 ]; then
  echo "✅ docs/evidence/：$found 个目录，README 齐全且全部在索引表里"
fi
exit "$fail"
