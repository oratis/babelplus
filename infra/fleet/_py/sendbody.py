import json, os
print(json.dumps({"receive_id": os.environ["TO"], "msg_type": os.environ["MT"],
                  "content": os.environ["C"]}, ensure_ascii=False))
