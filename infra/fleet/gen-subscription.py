#!/usr/bin/env python3
"""gen-subscription.py · 从 fleet.json + .secrets.env 渲染自用机队的四种订阅产物（本机执行，不上传）。

事实源：docs/04-ops/personal-fleet-runbook.md §2.2（产物与三条硬约束）、§2.5（客户端配置）
        api/internal/subgen（商用侧同一批协议的渲染；本脚本**不 import 它**，ADR 0017 §3）
        wiki.metacubex.one proxy-providers / proxy-groups（2026-09-05 抓取：health-check.lazy 默认 true；
        分组的 filter 只作用于 use: 导入；分组级 url/interval 不测 provider 节点）

产物（写到 --out，默认 infra/fleet/out/）：
  mihomo-provider.yaml   只有 proxies: 一个顶层键 —— provider 文件的格式（Clash Verge Rev 真热更新）
  clash.yaml             完整配置：dns / proxy-providers / proxy-groups(use:) / rule-providers / rules
  singbox.json           sing-box 完整 profile：含 inbounds(tun) 与 route.rules（roadmap B45 的坑不踩第二次）
  base64.txt             分享链接逐行 base64（Shadowrocket / 兜底）

三条从商用侧照抄的硬约束：
  1. ~~Hysteria2 默认不下发 up/down~~（ADR 0004 裁决 1 是商用侧的）。2026-11-19 起自用队**默认下发 20/60**：
     联通客户端回程丢包 ~50%，BBR 塌到与 TCP 同级，Brutal 是唯一实测能拉起单流的选项；
     代价是 Brutal 的可识别特征与更多重传出网费。--brutal off 回到 BBR（Proxy_Skill ops/README 2026-11-18 一节）。
  2. 自用队默认关 mux（实测优先于 ADR 0004 裁决 2：as-built-personal-fleet §4.2 证明 mux 对本链路有害）。
  3. 不下发 GEOIP,CN（roadmap B46：拿不到 MMDB 时整份配置拒绝加载）。国内直连改用 RULE-SET cn-cidr，
     列表由同一个 Worker 自托管（publish-subscription.sh --refresh-cn-cidr），不指 GitHub。

🔴 clash.yaml / singbox.json 里的订阅 URL 写成 __SUB_BASE__/p/__TOKEN__/…，由 Worker 在下发时按请求替换。
   这样四种产物对所有设备**内容相同**，KV 里只存一份；吊销只删 token，不重发产物。
🔴 本脚本只把凭据写进产物文件（chmod 600），终端只回显节点数与摘要，不回显任何凭据。
"""
from __future__ import annotations  # macOS 自带 python3 3.9 也能跑（str | None 注解不求值）
import argparse, base64, datetime, hashlib, json, os, pathlib, re, sys, urllib.parse

HERE = pathlib.Path(__file__).resolve().parent
DEFAULT_RULES_FROM = pathlib.Path("/Users/oratis/Documents/Codex/VPN/Proxy_Skill/clash-configs/mac.yaml")

# 区域组：名字前缀（fleet.json paths[].name 的前缀）→ 组名。顺序即 🚀 Proxy 里的列出顺序。
REGIONS = [("HK", "🇭🇰 Hong Kong"), ("JP", "🇯🇵 Japan"), ("SG", "🇸🇬 Singapore"), ("US", "🇺🇸 United States")]
# AI 组可用的区域，第一个是默认。不含 HK：OpenAI / Anthropic / Gemini 不对香港提供服务（2026-11-19）。
AI_REGIONS = ["🇺🇸 United States", "🇯🇵 Japan", "🇸🇬 Singapore"]
# sing-box 没有 clash 的 rules 段，AI 分流在这里列（clash.yaml 的 AI 规则来自 --rules-from）。
AI_DOMAINS = ["openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com", "sora.com", "anthropic.com",
              "claude.ai", "claude.com", "claudeusercontent.com", "gemini.google.com", "aistudio.google.com",
              "generativelanguage.googleapis.com", "notebooklm.google.com", "perplexity.ai", "x.ai", "grok.com",
              "copilot.microsoft.com"]
DEFAULT_BRUTAL = "20,60"   # 2026-11-19 用户决定：自用队 HY2 默认开 Brutal（联通线路 ~50% 丢包，BBR 塌）；--brutal off 关闭

# ───────────────────────── 输入 ─────────────────────────

def load_env(path: pathlib.Path) -> dict:
    env = {}
    if path.exists():
        for line in path.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            k, v = line.split("=", 1)
            env[k.strip()] = v.strip().strip('"').strip("'")
    for k, v in os.environ.items():
        if k in env or re.match(r"^(JP_|SG_|US_)?(STATIC_IP|SS_|REALITY_|CDN_|HY2_)", k):
            env[k] = v
    return env


def build_proxies(fleet: dict, env: dict, notice_name: str, brutal: str | None) -> list[dict]:
    """把 fleet.json 的 paths 变成与协议无关的中间表示；渲染器各自翻译。"""
    out = [{"kind": "notice", "name": notice_name}]
    missing = []
    for node in fleet["nodes"]:
        if (node.get("status") or "running") == "planned" or not node.get("ip"):
            continue
        if node.get("subscription_enabled") is False:
            continue  # 只进巡检 / 日报、不进订阅的节点（如 vpn-fengrongcheng-sg 独享专线）
        p = node.get("secrets_prefix", "")
        def sec(key, required=True):
            v = env.get(p + key, "")
            if required and not v:
                missing.append(p + key)
            return v
        for path in sorted(node["paths"], key=lambda x: x.get("sort", 0)):
            kind, name, port = path["kind"], path["name"], path["port"]
            if kind == "vless_reality":
                out.append({"kind": kind, "name": name, "server": node["ip"], "port": port,
                            "uuid": sec("REALITY_UUID"), "sni": sec("REALITY_SNI"),
                            "public_key": sec("REALITY_PUBLIC"), "short_id": sec("REALITY_SHORTID")})
            elif kind == "hysteria2":
                item = {"kind": kind, "name": name, "server": node["ip"], "port": port,
                        "password": sec("HY2_PASSWORD"), "obfs_password": sec("HY2_OBFS_PASSWORD"),
                        "sni": sec("HY2_SNI")}
                if brutal:
                    up, down = brutal.split(",")
                    item["up"], item["down"] = f"{up.strip()} Mbps", f"{down.strip()} Mbps"
                out.append(item)
            elif kind == "shadowsocks2022":
                out.append({"kind": kind, "name": name, "server": node["ip"], "port": port,
                            "cipher": "2022-blake3-aes-128-gcm", "password": sec("SS_PASSWORD")})
            elif kind == "vless_ws_tls":
                out.append({"kind": kind, "name": name, "server": sec("CDN_SERVER"), "port": port,
                            "host": sec("CDN_HOST"), "uuid": sec("CDN_UUID"), "path": sec("CDN_WS_PATH")})
            else:
                sys.exit(f"未知 kind: {kind}（{name}）")
    if missing:
        sys.exit("ERROR: .secrets.env 缺少: " + ", ".join(sorted(set(missing))))
    return out

# ───────────────────────── mihomo ─────────────────────────

def ystr(v) -> str:
    """YAML 标量：一律用 JSON 风格双引号，避免密码里的 :#{ 等字符改变语义。"""
    return json.dumps(str(v), ensure_ascii=False)


def mihomo_proxy_lines(p: dict) -> list[str]:
    k = p["kind"]
    L = [f"  - name: {ystr(p['name'])}"]
    if k == "notice":
        # 语法合法但连不上：与商用侧 KindNotice 同款设计，用节点名传达状态。
        L += ["    type: ss", "    server: 127.0.0.1", "    port: 1", "    cipher: aes-128-gcm", "    password: notice"]
    elif k == "vless_reality":
        L += ["    type: vless", f"    server: {p['server']}", f"    port: {p['port']}", f"    uuid: {p['uuid']}",
              "    network: tcp", "    tls: true", "    udp: true", "    flow: xtls-rprx-vision",
              f"    servername: {p['sni']}", "    client-fingerprint: chrome", "    reality-opts:",
              f"      public-key: {p['public_key']}", f"      short-id: {ystr(p['short_id'])}"]
    elif k == "hysteria2":
        L += ["    type: hysteria2", f"    server: {p['server']}", f"    port: {p['port']}",
              f"    password: {ystr(p['password'])}", "    obfs: salamander",
              f"    obfs-password: {ystr(p['obfs_password'])}", f"    sni: {p['sni']}",
              "    skip-cert-verify: true", "    udp: true"]
        if "up" in p:
            L += [f"    up: {ystr(p['up'])}", f"    down: {ystr(p['down'])}"]
    elif k == "shadowsocks2022":
        L += ["    type: ss", f"    server: {p['server']}", f"    port: {p['port']}",
              f"    cipher: {p['cipher']}", f"    password: {ystr(p['password'])}", "    udp: true", "    udp-over-tcp: false"]
    elif k == "vless_ws_tls":
        L += ["    type: vless", f"    server: {p['server']}", f"    port: {p['port']}", f"    uuid: {p['uuid']}",
              "    network: ws", "    tls: true", "    udp: true", f"    servername: {p['host']}",
              "    client-fingerprint: chrome", "    ws-opts:", f"      path: {ystr(p['path'])}",
              "      headers:", f"        Host: {p['host']}"]
    return L


def render_provider(proxies: list[dict]) -> str:
    L = ["# mihomo proxy-provider · 自用机队 · gen-subscription.py", "# 只有 proxies: 一个顶层键。第一位是公告伪节点。", "proxies:"]
    for p in proxies:
        L += mihomo_proxy_lines(p) + [""]
    return "\n".join(L).rstrip() + "\n"


def extract_rules(path: pathlib.Path | None, geoip_mode: str) -> list[str]:
    """取现有 clash 配置的 rules: 段（用户的路由策略，非机密）；找不到就用最小规则集。"""
    rules = []
    if path and path.exists():
        txt = path.read_text(encoding="utf-8")
        m = re.search(r"^rules:\n(.*)\Z", txt, re.S | re.M)
        if m:
            for line in m.group(1).splitlines():
                s = line.rstrip()
                if not s.strip() or s.strip().startswith("#"):
                    rules.append(s)
                    continue
                if "GEOIP,CN," in s and geoip_mode == "ruleset":
                    rules.append("  # GEOIP,CN 按 roadmap B46 改为自托管 RULE-SET（gen-subscription.py）")
                    rules.append(s.replace("GEOIP,CN,", "RULE-SET,cn-cidr,"))
                    continue
                rules.append(s)
    if not rules:
        rules = [
            "  - DOMAIN-SUFFIX,lan,DIRECT", "  - DOMAIN-SUFFIX,local,DIRECT",
            "  - IP-CIDR,127.0.0.0/8,DIRECT,no-resolve", "  - IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
            "  - IP-CIDR,172.16.0.0/12,DIRECT,no-resolve", "  - IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
            "  - DOMAIN-SUFFIX,cn,DIRECT",
            "  - RULE-SET,cn-cidr,DIRECT" if geoip_mode == "ruleset" else "  - GEOIP,CN,DIRECT",
            "  - GEOIP,PRIVATE,DIRECT,no-resolve", "  - MATCH,🎯 Final",
        ]
    return rules


def render_clash(fleet: dict, proxies: list[dict], rules: list[str], geoip_mode: str, provider_interval: int) -> str:
    regions = [(r, flag) for r, flag in REGIONS if any(p["name"].startswith(r + "-") for p in proxies)]
    region_names = [f for _, f in regions]
    fallback_geoip = "false" if geoip_mode == "ruleset" else "true"
    L = [
        f"# Clash Verge Rev / mihomo · 自用机队 · 由 gen-subscription.py 生成 · {datetime.date.today()}",
        "# 节点集合来自 proxy-providers（fleet），换 IP / 加节点时本文件不需要重新导入。",
        "# __SUB_BASE__ / __TOKEN__ 由 Worker 在下发时替换成你的订阅地址与设备 token。",
        "",
        "mixed-port: 7890", "allow-lan: false", "mode: rule", "log-level: info", "ipv6: false",
        "find-process-mode: strict", "unified-delay: true", "tcp-concurrent: true",
        "",
        "dns:", "  enable: true", "  listen: 127.0.0.1:1053", "  enhanced-mode: fake-ip",
        "  fake-ip-range: 198.18.0.1/16", "  fake-ip-filter:", '    - "*.lan"', '    - "*.local"',
        '    - "localhost.ptlogin2.qq.com"',
        "  nameserver:", "    - https://223.5.5.5/dns-query", "    - https://1.12.12.12/dns-query",
        "  fallback:", "    - https://1.1.1.1/dns-query", "    - https://8.8.8.8/dns-query",
        "  fallback-filter:",
        f"    geoip: {fallback_geoip}   # ruleset 模式下不依赖 MMDB（B46）",
        "    geoip-code: CN", "    ipcidr:", "      - 169.254.0.0/16", "      - 240.0.0.0/4",
        "",
        "proxy-providers:", "  fleet:", "    type: http",
        "    url: \"__SUB_BASE__/p/__TOKEN__/mihomo-provider.yaml\"",
        "    path: ./providers/fleet.yaml",
        f"    interval: {provider_interval}",
        "    health-check:", "      enable: true", "      url: https://www.gstatic.com/generate_204",
        "      interval: 60   # 2026-11-19：300 → 60，坏路径 1 分钟内被 fallback 跳过",
        "      lazy: false   # fallback 组的健康信息只来自这里（分组级 url/interval 不测 provider 节点）",
        "      expected-status: 204",
        "",
    ]
    if geoip_mode == "ruleset":
        L += ["rule-providers:", "  cn-cidr:", "    type: http", "    behavior: ipcidr", "    format: text",
              "    url: \"__SUB_BASE__/p/__TOKEN__/cn-cidr.txt\"", "    path: ./ruleset/cn-cidr.txt", "    interval: 86400", ""]
    L += ["proxy-groups:",
          "  # 2026-11-19：🚀 Proxy 默认 ⚡ Auto = 在「区域组」之间按延迟选（Proxy_Skill ops/README 2026-11-18 一节）。",
          "  # 联通（AS4837）从 GCP 只有香港 Premium 是直交（50 ms），东京 / 美国都绕美国过境；电信 / 移动东京最快。",
          "  # 按区域组而不是按单个节点比延迟：区域内仍由 fallback 先用 HY2（Brutal 吞吐），延迟只用来选区域。",
          '  - name: "🚀 Proxy"', "    type: select", "    proxies:", '      - "⚡ Auto"']
    L += [f"      - {ystr(f)}" for f in region_names] + ["      - DIRECT", ""]
    L += ['  - name: "⚡ Auto"', "    type: url-test", "    proxies:"] + [f"      - {ystr(f)}" for f in region_names]
    L += ["    url: https://www.gstatic.com/generate_204", "    interval: 120", "    tolerance: 60",
          "    lazy: false", "    expected-status: 204", ""]
    for r, flag in regions:
        L += [f"  - name: {ystr(flag)}", "    type: fallback", "    use: [fleet]", f'    filter: "^{r}-"', ""]
    # OpenAI / Anthropic / Gemini 不对香港提供服务：AI 组不含 HK，也不含可能落到 HK 的 ⚡ Auto / 🚀 Proxy。
    ai = [f for f in AI_REGIONS if f in region_names]
    L += ['  - name: "🤖 AI"', "    type: select", "    proxies:"] + [f"      - {ystr(f)}" for f in ai] + ["      - DIRECT", ""]
    L += ['  - name: "🌎 Global"', "    type: select", "    proxies:", '      - "🚀 Proxy"', "      - DIRECT", ""]
    L += ['  - name: "🇨🇳 Direct-CN"', "    type: select", "    proxies:", "      - DIRECT", '      - "🚀 Proxy"', ""]
    L += ['  - name: "🎯 Final"', "    type: select", "    proxies:", '      - "🚀 Proxy"', "      - DIRECT", ""]
    L += ["rules:"] + rules
    return "\n".join(L).rstrip() + "\n"

# ───────────────────────── sing-box ─────────────────────────

def render_singbox(proxies: list[dict], geoip_mode: str) -> str:
    outbounds = []
    names = []
    for p in proxies:
        k = p["kind"]
        if k == "notice":
            outbounds.append({"type": "shadowsocks", "tag": p["name"], "server": "127.0.0.1", "server_port": 1,
                              "method": "aes-128-gcm", "password": "notice"})
        elif k == "vless_reality":
            outbounds.append({"type": "vless", "tag": p["name"], "server": p["server"], "server_port": p["port"],
                              "uuid": p["uuid"], "flow": "xtls-rprx-vision",
                              "tls": {"enabled": True, "server_name": p["sni"], "utls": {"enabled": True, "fingerprint": "chrome"},
                                      "reality": {"enabled": True, "public_key": p["public_key"], "short_id": p["short_id"]}}})
        elif k == "hysteria2":
            o = {"type": "hysteria2", "tag": p["name"], "server": p["server"], "server_port": p["port"], "password": p["password"],
                 "obfs": {"type": "salamander", "password": p["obfs_password"]},
                 "tls": {"enabled": True, "server_name": p["sni"], "insecure": True}}
            if "up" in p:
                o["up_mbps"], o["down_mbps"] = int(p["up"].split()[0]), int(p["down"].split()[0])
            outbounds.append(o)
        elif k == "shadowsocks2022":
            outbounds.append({"type": "shadowsocks", "tag": p["name"], "server": p["server"], "server_port": p["port"],
                              "method": p["cipher"], "password": p["password"]})
        elif k == "vless_ws_tls":
            outbounds.append({"type": "vless", "tag": p["name"], "server": p["server"], "server_port": p["port"], "uuid": p["uuid"],
                              "tls": {"enabled": True, "server_name": p["host"], "utls": {"enabled": True, "fingerprint": "chrome"}},
                              "transport": {"type": "ws", "path": p["path"], "headers": {"Host": p["host"]}}})
        names.append(p["name"])
    prefixes = "|".join(r for r, _ in REGIONS)
    real = [n for n in names if re.match(rf"^({prefixes})-", n)]
    by = lambda r: [n for n in real if n.startswith(r + "-")]
    test = {"url": "https://www.gstatic.com/generate_204", "interval": "2m"}
    groups = [{"type": "urltest", "tag": tag, "outbounds": by(r), **test, "tolerance": 150} for r, tag in REGIONS if by(r)]
    region_tags = [g["tag"] for g in groups]
    # 与 clash.yaml 同一设计：⚡ Auto 在区域组之间按延迟选；AI 不进 HK（OpenAI / Anthropic / Gemini 不服务香港）
    auto = {"type": "urltest", "tag": "⚡ Auto", "outbounds": region_tags, **test, "tolerance": 60}
    ai_out = [t for t in AI_REGIONS if t in region_tags]
    ai_sel = {"type": "selector", "tag": "🤖 AI", "outbounds": ai_out or ["direct"], "default": (ai_out or ["direct"])[0]}
    proxy_sel = {"type": "selector", "tag": "🚀 Proxy", "outbounds": ["⚡ Auto"] + region_tags + ["direct"], "default": "⚡ Auto"}
    route_rules = [
        {"protocol": "dns", "action": "hijack-dns"},
        {"ip_is_private": True, "outbound": "direct"},
        {"domain_suffix": AI_DOMAINS, "outbound": "🤖 AI"},
        {"domain_suffix": [".cn"], "outbound": "direct"},
    ]
    rule_set = []
    if geoip_mode == "ruleset":
        rule_set.append({"tag": "cn-cidr", "type": "remote", "format": "source", "url": "__SUB_BASE__/p/__TOKEN__/cn-cidr.json",
                         "download_detour": "🚀 Proxy", "update_interval": "24h"})
        route_rules.append({"rule_set": ["cn-cidr"], "outbound": "direct"})
    cfg = {
        "_comment": "sing-box · 自用机队 · gen-subscription.py。含 inbounds(tun) 与 route.rules（B45）。⚠️ 需真机：SFI/SFA 加载一次才算数。",
        "log": {"level": "warn"},
        "dns": {"servers": [{"tag": "cn", "address": "https://223.5.5.5/dns-query", "detour": "direct"},
                            {"tag": "remote", "address": "https://1.1.1.1/dns-query", "detour": "🚀 Proxy"}],
                "rules": [{"domain_suffix": [".cn"], "server": "cn"}], "final": "remote", "strategy": "ipv4_only"},
        "inbounds": [{"type": "tun", "tag": "tun-in", "address": ["172.19.0.1/30"], "auto_route": True, "strict_route": False, "stack": "mixed"}],
        "outbounds": [proxy_sel, auto, ai_sel] + groups + outbounds + [{"type": "direct", "tag": "direct"}],
        "route": {"rules": route_rules, "rule_set": rule_set, "final": "🚀 Proxy", "auto_detect_interface": True},
    }
    return json.dumps(cfg, ensure_ascii=False, indent=2) + "\n"

# ───────────────────────── 分享链接 / base64 ─────────────────────────

def share_links(proxies: list[dict]) -> list[str]:
    links = []
    for p in proxies:
        n = urllib.parse.quote(p["name"])
        k = p["kind"]
        if k == "notice":
            ui = base64.urlsafe_b64encode(b"aes-128-gcm:notice").decode().rstrip("=")
            links.append(f"ss://{ui}@127.0.0.1:1#{n}")
        elif k == "vless_reality":
            q = {"encryption": "none", "type": "tcp", "security": "reality", "sni": p["sni"], "fp": "chrome",
                 "pbk": p["public_key"], "sid": p["short_id"], "flow": "xtls-rprx-vision"}
            links.append(f"vless://{p['uuid']}@{p['server']}:{p['port']}?{urllib.parse.urlencode(q)}#{n}")
        elif k == "hysteria2":
            q = {"sni": p["sni"], "insecure": "1", "obfs": "salamander", "obfs-password": p["obfs_password"]}
            links.append(f"hysteria2://{urllib.parse.quote(p['password'], safe='')}@{p['server']}:{p['port']}/?{urllib.parse.urlencode(q)}#{n}")
        elif k == "shadowsocks2022":
            ui = base64.urlsafe_b64encode(f"{p['cipher']}:{p['password']}".encode()).decode().rstrip("=")
            links.append(f"ss://{ui}@{p['server']}:{p['port']}#{n}")
        elif k == "vless_ws_tls":
            q = {"encryption": "none", "type": "ws", "security": "tls", "sni": p["host"], "host": p["host"], "path": p["path"], "fp": "chrome"}
            links.append(f"vless://{p['uuid']}@{p['server']}:{p['port']}?{urllib.parse.urlencode(q)}#{n}")
    return links

# ───────────────────────── main ─────────────────────────

def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--fleet", default=str(HERE / "fleet.json"))
    ap.add_argument("--secrets", default=str(HERE / ".secrets.env"))
    ap.add_argument("--out", default=str(HERE / "out"))
    ap.add_argument("--sub-domain", default=None, help="写进公告伪节点名的订阅域名（默认取 fleet.json .subscription.hostname）")
    ap.add_argument("--geoip", choices=["ruleset", "geoip"], default="ruleset", help="国内直连用自托管 RULE-SET（默认，B46）还是 GEOIP,CN")
    ap.add_argument("--brutal", default=DEFAULT_BRUTAL, metavar="UP,DOWN|off",
                    help=f"给 Hysteria2 下发 up/down（启用 Brutal，有可分特征）。默认 {DEFAULT_BRUTAL}；off = 不下发，回到 BBR")
    ap.add_argument("--rules-from", default=str(DEFAULT_RULES_FROM), help="从这份 clash 配置抽 rules: 段（用户的路由策略）")
    a = ap.parse_args()

    fleet = json.loads(pathlib.Path(a.fleet).read_text(encoding="utf-8"))
    env = load_env(pathlib.Path(a.secrets))
    sub_domain = a.sub_domain or (fleet.get("subscription") or {}).get("hostname") or "（域名待定）"
    notice = fleet["notice"]["name_template"].format(sub_domain=sub_domain, date=datetime.date.today().isoformat())
    brutal = None if str(a.brutal).lower() in ("off", "none", "0", "") else a.brutal
    proxies = build_proxies(fleet, env, notice, brutal)
    rules = extract_rules(pathlib.Path(a.rules_from) if a.rules_from else None, a.geoip)
    interval = int((fleet.get("subscription") or {}).get("provider_interval_s", 900))

    out = pathlib.Path(a.out); out.mkdir(parents=True, exist_ok=True)
    products = {
        "mihomo-provider.yaml": render_provider(proxies),
        # 线上 Worker（11-13 部署）对 iphone 设备的 base64.txt 请求下发这份；内容与 provider 相同（不再套 dialer-proxy）
        "iphone-shadowrocket.yaml": render_provider(proxies),
        "clash.yaml": render_clash(fleet, proxies, rules, a.geoip, interval),
        "singbox.json": render_singbox(proxies, a.geoip),
        "base64.txt": base64.b64encode(("\n".join(share_links(proxies)) + "\n").encode()).decode() + "\n",
    }
    manifest = {"generated": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
                "sub_domain": sub_domain, "geoip": a.geoip, "brutal": brutal, "nodes": [], "files": {}}
    for f, body in products.items():
        p = out / f
        p.write_text(body, encoding="utf-8"); p.chmod(0o600)
        manifest["files"][f] = {"bytes": len(body.encode()), "sha256": hashlib.sha256(body.encode()).hexdigest()[:16]}
    manifest["nodes"] = [p["name"] for p in proxies]
    (out / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"渲染 {len(proxies)} 个条目（含公告伪节点）→ {out}/")
    for n in manifest["nodes"]:
        print("  ·", n)
    for f, m in manifest["files"].items():
        print(f"  {f:22} {m['bytes']:7d} B  sha256:{m['sha256']}")
    print("（凭据只在文件里；rules 来自 %s）" % (a.rules_from if pathlib.Path(a.rules_from).exists() else "内置最小规则集"))


if __name__ == "__main__":
    main()
