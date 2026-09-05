import json, sys
print(json.dumps(json.load(open(sys.argv[1], encoding="utf-8")), ensure_ascii=False))
