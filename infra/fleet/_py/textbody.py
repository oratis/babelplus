import json, os
print(json.dumps({"text": os.environ["C"]}, ensure_ascii=False))
