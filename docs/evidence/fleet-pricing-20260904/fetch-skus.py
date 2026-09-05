import json, os, subprocess, urllib.request, sys
tok = subprocess.check_output(["gcloud","auth","print-access-token"], text=True).strip()
out, page = [], ""
url0 = "https://cloudbilling.googleapis.com/v1/services/6F81-5844-456A/skus?pageSize=5000"
n = 0
while True:
    url = url0 + (f"&pageToken={page}" if page else "")
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {tok}"})
    with urllib.request.urlopen(req, timeout=90) as r:
        d = json.load(r)
    out.extend(d.get("skus", []))
    page = d.get("nextPageToken", "")
    n += 1
    if not page or n > 20:
        break
print(f"fetched {len(out)} skus in {n} pages", file=sys.stderr)
json.dump(out, open(os.environ["SP"] + "/skus-compute.json", "w"))
