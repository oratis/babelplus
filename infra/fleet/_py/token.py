import json, sys
d = json.load(sys.stdin)
if d.get("code"):
    sys.exit("飞书鉴权失败 code=%s msg=%s" % (d.get("code"), d.get("msg")))
print(d["tenant_access_token"])
