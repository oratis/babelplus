# 输入 32771 个 Compute Engine SKU

## 表 1 · E2 整机月价（OnDemand，730h，不含盘与出口）
| 区域 | e2-micro | e2-small | e2-medium | e2-standard-2 | e2-standard-4 |
|---|---|---|---|---|---|
| `us-west1` | $6.11 | $12.23 | $24.46 | $48.92 | $97.84 |
| `us-central1` | $6.11 | $12.23 | $24.46 | $48.92 | $97.84 |
| `us-east1` | $6.11 | $12.23 | $24.46 | $48.92 | $97.84 |
| `asia-northeast1` | $7.84 | $15.69 | $31.38 | $62.75 | $125.51 |
| `asia-southeast1` | $7.54 | $15.09 | $30.17 | $60.35 | $120.69 |
| `asia-east2` | $8.56 | $17.11 | $34.22 | $68.45 | $136.89 |
| `asia-east1` | $7.08 | $14.16 | $28.32 | $56.64 | $113.28 |
| `europe-west4` | $6.73 | $13.46 | $26.93 | $53.85 | $107.71 |

## 表 2 · Standard 层级出网（按源区域，目的地无关；单位 GiB）
| 源区域 | 0–200 | 200–10,240 | 10,240–153,600 | >153,600 |
|---|---|---|---|---|
| `europe-west4` | $0.000 | $0.085 | $0.065 | $0.045 |
| `us-central1` | $0.000 | $0.085 | $0.065 | $0.045 |
| `us-west1` | $0.000 | $0.085 | $0.065 | $0.045 |
| `asia-east2` | $0.000 | $0.110 | $0.075 | $0.070 |
| `us-east1` | $0.000 | $0.085 | $0.065 | $0.045 |
| `asia-east1` | $0.000 | $0.110 | $0.075 | $0.070 |
| `asia-northeast1` | $0.000 | $0.110 | $0.075 | $0.070 |
| `asia-southeast1` | $0.000 | $0.110 | $0.075 | $0.070 |

## 表 3 · Carrier Peering 出网（Premium 路由下中国方向的实际落点，无免费额度、无阶梯）
| SKU | 目录区域 | $/GiB |
|---|---|---|
| Network Data Transfer Out via Carrier Peering Network - APAC Based | `asia-east1` | $0.085 |
| Network Data Transfer Out via Carrier Peering Network - Americas Based | `us-central1,us-east1,us-west1` | $0.080 |
| Network Data Transfer Out via Carrier Peering Network - EMEA Based | `europe-west1` | $0.080 |

## 表 4 · Premium 层级到中国大陆的 Internet DTO（对照用；egress-billing-20260820 窗口内用量为 0）
| SKU | 0–1 TiB | 1–10 TiB | >10 TiB |
|---|---|---|---|
| Network Internet Data Transfer Out from APAC to China | $0.23 | $0.22 | $0.20 |
| Network Internet Data Transfer Out from Japan to China | $0.23 | $0.22 | $0.20 |
| Network Internet Data Transfer Out from Hong Kong to China | $0.23 | $0.22 | $0.20 |
| Network Internet Data Transfer Out from Americas to China | $0.23 | $0.22 | $0.20 |
| Network Internet Data Transfer Out from Los Angeles to China | $0.23 | $0.22 | $0.20 |
| Network Internet Data Transfer Out from Singapore to China | $0.23 | $0.22 | $0.20 |
