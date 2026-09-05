import json, os
v = os.environ["V"]
key = "emails" if "@" in v else "mobiles"
print(json.dumps({key: [v], "include_resigned": False}))
