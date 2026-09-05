import json, os
print(json.dumps({"app_id": os.environ["APP_ID"], "app_secret": os.environ["APP_SECRET"]}))
