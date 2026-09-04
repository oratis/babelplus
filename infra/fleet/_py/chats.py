import json, sys
d = json.load(sys.stdin)
if d.get("code"):
    sys.exit("code=%s msg=%s" % (d.get("code"), d.get("msg")))
items = d.get("data", {}).get("items", [])
if not items:
    sys.exit("胖狗当前不在任何会话里。先在飞书建一个只有你和它的群，再跑一次。")
print("%d 个会话：" % len(items))
for c in items:
    print("  %-38s [%-7s] %s" % (c["chat_id"], c.get("chat_mode"), c.get("name") or "(无名)"))
print("")
print("把只有你和胖狗的那个群的 chat_id 写进 infra/fleet/.secrets.env：")
print("  FEISHU_RECEIVE_ID=oc_xxxx")
print("  FEISHU_RECEIVE_ID_TYPE=chat_id")
