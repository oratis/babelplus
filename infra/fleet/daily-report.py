#!/usr/bin/env python3
"""daily-report.py · 本机侧的日报：预览 / 手动补发 / Worker 不可用时的兜底。

事实源：docs/04-ops/personal-fleet-runbook.md §3.1（降级路径）、§3.4（卡片格式）
        infra/fleet/worker/src/index.js（正式渲染在 Worker 里；本脚本 --source worker 直接取它渲染好的卡片）

用法（先 set -a; source infra/fleet/.secrets.env; set +a）：
  daily-report.py --preview                    # 从 Worker 取今日卡片，打印 JSON，不发送
  daily-report.py --send                       # 从 Worker 取卡片并经飞书 webhook 发送（补发 / 首条真实消息）
  daily-report.py --source nodes --preview     # Worker 不可用：IAP SSH 逐台拉 /var/lib/fleet/latest.json，本地渲染精简版
  daily-report.py --source nodes --send

需要：FLEET_INGEST_URL（Worker 基址）、ADMIN_TOKEN；发送还需 FEISHU_WEBHOOK_URL（+ FEISHU_WEBHOOK_SECRET 可选）。
🔴 本地渲染是精简版（没有月度用量差分 —— 那份状态只在 KV 里），只保证「每台一行 + 未上报正向可读 + 待办」。
"""
import argparse, base64, datetime, hashlib, hmac, json, os, pathlib, subprocess, sys, time, urllib.request

HERE = pathlib.Path(__file__).resolve().parent
FLEET = json.loads((HERE / "fleet.json").read_text(encoding="utf-8"))


def env(k, required=False):
    v = os.environ.get(k, "")
    if required and not v:
        sys.exit(f"缺少环境变量 {k}（set -a; source infra/fleet/.secrets.env; set +a）")
    return v


def http(url, method="GET", body=None, headers=None):
    req = urllib.request.Request(url, method=method, data=body, headers=headers or {})
    with urllib.request.urlopen(req, timeout=30) as r:
        return r.status, r.read().decode("utf-8", "replace")


def card_from_worker():
    base = env("FLEET_INGEST_URL", True).rstrip("/")
    st, body = http(f"{base}/admin/report", headers={"Authorization": "Bearer " + env("ADMIN_TOKEN", True)})
    if st != 200:
        sys.exit(f"Worker 返回 {st}: {body[:200]}")
    return json.loads(body)["card"]


def latest_from_nodes():
    out = {}
    for n in FLEET["nodes"]:
        if (n.get("status") or "running") == "planned":
            continue
        cmd = ["gcloud", "compute", "ssh", n["host"], f"--project={FLEET['project']}", f"--zone={n['zone']}",
               "--tunnel-through-iap", "--quiet", "--command=sudo cat /var/lib/fleet/latest.json 2>/dev/null || true"]
        try:
            r = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
            txt = r.stdout.strip().splitlines()
            txt = [l for l in txt if l.startswith("{")]
            out[n["host"]] = json.loads(txt[-1]) if txt else None
        except Exception as e:
            out[n["host"]] = None
            print(f"  {n['host']}: 取不到（{e}）", file=sys.stderr)
    return out


def card_local(latest):
    tz = datetime.timezone(datetime.timedelta(hours=8))
    now = datetime.datetime.now(tz)
    svc = FLEET.get("services", {})
    lines, todo, worst = [], [], 0
    for n in FLEET["nodes"]:
        if (n.get("status") or "running") == "planned":
            todo.append(f"fleet.json 里仍标 planned：{n['host']}")
            continue
        hc = latest.get(n["host"])
        if not hc:
            lines.append(f"⚫ **{n['host']}**  —— 未上报（本地兜底路径也拉不到 latest.json）")
            worst = 2
            continue
        expected = sorted({svc[p["kind"]] for p in n["paths"] if p["kind"] in svc})
        active = [s for s in expected if hc.get("services", {}).get(s) == "active"]
        mark = "🟢" if len(active) == len(expected) else "🔴"
        if mark == "🔴":
            worst = 2
        h = hc.get("host", {})
        if h.get("reboot_required"):
            todo.append(f"{n['host']} 有内核更新待重启")
        certs = " · ".join(f"cert {c['days_left']}d" for c in hc.get("cert", {}).values() if isinstance(c, dict) and "days_left" in c)
        lines.append(f"{mark} **{n['host']}**  进程 {len(active)}/{len(expected)} · 443 est {hc.get('est443_public', '?')}"
                     f"{' · ' + certs if certs else ''} · CPU {h.get('cpu_pct', '?')}%  （{hc.get('ts', '?')}）")
    template = "red" if worst == 2 else "green"
    return {
        "config": {"wide_screen_mode": True},
        "header": {"template": template, "title": {"tag": "plain_text", "content": f"🐕 机队日报（本地兜底）· {now:%Y-%m-%d}"}},
        "elements": [
            {"tag": "div", "text": {"tag": "lark_md", "content": "\n".join(lines) or "（无节点）"}},
            {"tag": "hr"},
            {"tag": "div", "text": {"tag": "lark_md", "content": "**本月用量**\n（本地路径无月度差分；看 Worker 恢复后的日报或订阅流量条）"}},
            {"tag": "hr"},
            {"tag": "div", "text": {"tag": "lark_md", "content": "**待办**\n" + ("\n".join("· " + t for t in todo) or "· 无")}},
            {"tag": "note", "elements": [{"tag": "plain_text", "content": f"生成于 {now:%Y-%m-%d %H:%M} · daily-report.py --source nodes"}]},
        ],
    }


def send_feishu(card):
    url = env("FEISHU_WEBHOOK_URL", True)
    payload = {"msg_type": "interactive", "card": card}
    secret = env("FEISHU_WEBHOOK_SECRET")
    if secret:
        ts = str(int(time.time()))
        key = f"{ts}\n{secret}".encode()
        payload["timestamp"] = ts
        payload["sign"] = base64.b64encode(hmac.new(key, b"", hashlib.sha256).digest()).decode()
    st, body = http(url, "POST", json.dumps(payload, ensure_ascii=False).encode(), {"Content-Type": "application/json; charset=utf-8"})
    print(f"飞书返回 HTTP {st}: {body[:200]}")
    try:
        return json.loads(body).get("code") == 0
    except Exception:
        return st == 200


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--source", choices=["worker", "nodes"], default="worker")
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--preview", action="store_true")
    g.add_argument("--send", action="store_true")
    a = ap.parse_args()
    card = card_from_worker() if a.source == "worker" else card_local(latest_from_nodes())
    if a.preview:
        print(json.dumps(card, ensure_ascii=False, indent=2))
        return
    ok = send_feishu(card)
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
