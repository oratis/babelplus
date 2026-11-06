#!/usr/bin/env python3
"""把 gen-subscription.py 的产物转成安卓 Clash Meta（CMFA）可直接导入的**自包含**配置。

为什么需要转换：仓库产出的 clash.yaml 靠 proxy-providers 去 sub.telloria.com 拉节点，
URL 里的 __SUB_BASE__/__TOKEN__ 由 Worker 在下发时替换。当成文件直接导入时那两个占位符
不会被替换 -> provider 拉取失败 -> 所有 fallback 分组为空 -> 一条线路都用不了。
所以这里把节点内联进配置，去掉 provider 依赖。

三处安卓专属改动：
  1. find-process-mode: strict -> off（CMFA 匹配的是包名，本份 rules 里 PROCESS-NAME 规则数为 0，关掉零代价）
  2. 分组由 use:[fleet]+filter 改成显式 proxies: 列表（没有 provider 了，filter 会匹配到空）
  3. 头部注释写明这份**不自动更新**：换 IP 后必须重新导入
"""
import pathlib, re, sys

src = pathlib.Path(sys.argv[1])          # 产物目录
dst = src / "clash-android.yaml"

provider = (src / "mihomo-provider.yaml").read_text(encoding="utf-8")
clash    = (src / "clash.yaml").read_text(encoding="utf-8")

# ── 1. 取 provider 的 proxies 块 ──
m = re.search(r"^proxies:\n(.*)\Z", provider, re.S | re.M)
if not m:
    sys.exit("provider 里找不到 proxies:")
proxies_block = m.group(1).rstrip()

names = re.findall(r'^  - name: "([^"]+)"', proxies_block, re.M)
by_prefix = {p: [n for n in names if n.startswith(p + "-")] for p in ("JP", "US", "SG")}
real = [n for n in names if re.match(r"^(JP|US|SG)-", n)]
if not real:
    sys.exit("没有解析到任何真实节点")

# ── 2. 切掉 proxy-providers 段，换成内联 proxies ──
head, sep, rest = clash.partition("proxy-providers:")
if not sep:
    sys.exit("clash.yaml 里找不到 proxy-providers:")
rest = rest.partition("proxy-groups:")[2]        # 丢掉整个 provider 块

head = head.replace("find-process-mode: strict", "find-process-mode: off")
head = re.sub(r"\A#[^\n]*\n#[^\n]*\n#[^\n]*\n", "", head)   # 换掉原三行头注释

# ── 3. 分组：use:[fleet]+filter -> 显式 proxies ──
def expand(block: str) -> str:
    def one(mm):
        indent, filt = mm.group(1), mm.group(2)
        pref = re.findall(r"[A-Z]{2}", filt)
        picked = real if len(pref) > 1 else by_prefix.get(pref[0], [])
        return f"{indent}proxies:\n" + "\n".join(f'{indent}  - "{n}"' for n in picked)
    return re.sub(r'( +)use: \[fleet\]\n +filter: "\^\(?([A-Z|]+)\)?-"', one, block)

groups = expand(rest)

out = f"""# Clash Meta for Android（CMFA）· 自用机队 · 自包含配置
# 由 infra/fleet/gen-subscription.py + make-android.py 生成 · {__import__('datetime').date.today()}
#
# 🔴 这份是**离线兜底**：节点已内联，不依赖 sub.telloria.com。
#    代价是**不会自动更新** —— 换 IP / 加节点后必须重新导入。
#    日常请优先用订阅 URL（Profile 里填 https://sub.telloria.com/p/<token>/clash.yaml），
#    那条走 proxy-providers，15 分钟自动拉一次，换 IP 无感。
#
# 分组默认指向 🇯🇵 Japan 而不是 ⚡ Auto：本链路健康节点延迟同在 95–325 ms 噪声带，
# 吞吐却差 4–5 倍，url-test 按延迟排会稳定挑中慢的那条（as-built-personal-fleet §4.1）。
{head}proxies:
{proxies_block}

proxy-groups:{groups}"""

dst.write_text(out, encoding="utf-8")
dst.chmod(0o600)
print(f"→ {dst}")
print(f"   内联节点 {len(names)} 个（含公告伪节点）：JP={len(by_prefix['JP'])} US={len(by_prefix['US'])} SG={len(by_prefix['SG'])}")
print(f"   {len(out.encode())} B")
