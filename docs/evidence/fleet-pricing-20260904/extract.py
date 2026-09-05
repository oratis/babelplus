#!/usr/bin/env python3
"""把 fetch-skus.py 抓下来的 Compute Engine SKU 目录，筛成本目录 README 里的四张表。

用法：
    python3 fetch-skus.py > skus-compute.json     # 需 gcloud auth print-access-token
    python3 extract.py skus-compute.json

只做筛选与算术，不做任何推断；输出的每个数字都能在输入 JSON 里逐字找到。
"""
import json, sys

REGIONS = ["us-west1", "us-central1", "us-east1",
           "asia-northeast1", "asia-southeast1", "asia-east2", "asia-east1", "europe-west4"]

# (vCPU, GiB RAM) —— E2 共享核机型的计费规格，用于把 core/RAM 单价折成整机月价。
SHAPES = {"e2-micro": (0.25, 1), "e2-small": (0.5, 2), "e2-medium": (1, 4),
          "e2-standard-2": (2, 8), "e2-standard-4": (4, 16)}
HOURS = 730  # GCP 月度折算惯例


def tiers(sku):
    pe = sku["pricingInfo"][0]["pricingExpression"]
    return pe["usageUnit"], [
        (t["startUsageAmount"],
         int(t["unitPrice"].get("units", "0")) + t["unitPrice"].get("nanos", 0) / 1e9)
        for t in pe["tieredRates"]
    ]


def main(path):
    skus = json.load(open(path))
    print(f"# 输入 {len(skus)} 个 Compute Engine SKU\n")

    print("## 表 1 · E2 整机月价（OnDemand，730h，不含盘与出口）")
    rate = {}
    for s in skus:
        c = s["category"]
        if c["usageType"] != "OnDemand" or not s["description"].startswith("E2 Instance"):
            continue
        if c["resourceGroup"] not in ("CPU", "RAM"):
            continue
        for r in s["serviceRegions"]:
            if r in REGIONS:
                rate.setdefault(r, {})[c["resourceGroup"]] = tiers(s)[1][0][1]
    hdr = "| 区域 | " + " | ".join(SHAPES) + " |"
    print(hdr); print("|" + "---|" * (len(SHAPES) + 1))
    for r in REGIONS:
        v = rate.get(r)
        if not v:
            continue
        cells = [f"${(v['CPU'] * vc + v['RAM'] * gb) * HOURS:.2f}" for vc, gb in SHAPES.values()]
        print(f"| `{r}` | " + " | ".join(cells) + " |")

    print("\n## 表 2 · Standard 层级出网（按源区域，目的地无关；单位 GiB）")
    print("| 源区域 | 0–200 | 200–10,240 | 10,240–153,600 | >153,600 |")
    print("|---|---|---|---|---|")
    for s in skus:
        if "Standard Data Transfer Out to Internet" not in s["description"]:
            continue
        for r in s["serviceRegions"]:
            if r in REGIONS:
                _, t = tiers(s)
                print(f"| `{r}` | " + " | ".join(f"${p:.3f}" for _, p in t) + " |")

    print("\n## 表 3 · Carrier Peering 出网（Premium 路由下中国方向的实际落点，无免费额度、无阶梯）")
    print("| SKU | 目录区域 | $/GiB |")
    print("|---|---|---|")
    for s in skus:
        d = s["description"]
        if "Carrier Peering" in d and "Data Transfer Out" in d:
            print(f"| {d} | `{','.join(s['serviceRegions'])}` | ${tiers(s)[1][0][1]:.3f} |")

    print("\n## 表 4 · Premium 层级到中国大陆的 Internet DTO（对照用；egress-billing-20260820 窗口内用量为 0）")
    print("| SKU | 0–1 TiB | 1–10 TiB | >10 TiB |")
    print("|---|---|---|---|")
    want = ("from Japan to China", "from Singapore to China", "from Hong Kong to China",
            "from Los Angeles to China", "from Americas to China", "from APAC to China")
    for s in skus:
        d = s["description"]
        if not (d.startswith("Network Internet Data Transfer Out") and d.endswith("to China")):
            continue
        if not any(w in d for w in want):
            continue
        print(f"| {d} | " + " | ".join(f"${p:.2f}" for _, p in tiers(s)[1]) + " |")


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "skus-compute.json")
