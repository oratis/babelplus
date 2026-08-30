# `infra/scripts/` · 索引

> 日期：2026-08-30 · 性质：**目录索引**（只做导航，不做事实登记）
> 读者：第一次打开这个目录、想知道「这八个文件分别是什么」的人。

🔴 **这份文件不是事实源。** 脚本清单、幂等性、危险度、**以及「是否已执行过」**
的唯一事实源是 [`../deploy/README.md` §1](../deploy/README.md)。
那张表不在这里复制一遍 —— 复制它就等于制造第二处会漂移的真相，
而这个仓库已经为同一类债付过好几次账（`../deploy/README.md` §6 第 1 条）。

---

## 八个脚本

| 脚本 | 一句话 |
|---|---|
| [`inventory.sh`](inventory.sh) | 把 as-built §7 的资产清点固化成可 diff 的快照。纯只读。 |
| [`verify-isolation.sh`](verify-isolation.sh) | 部署门禁：确认 `vpn-*` / `lisa-*` 等**别人的**资源未受影响，有差异非零退出。纯只读。 |
| [`image-provenance.sh`](image-provenance.sh) | 反查一个 Cloud Run 修订版跑的到底是哪个完整 git sha（roadmap B41）。纯只读。 |
| [`check-cert-issuer.sh`](check-cert-issuer.sh) | 核对证书签发者是否仍是 Let's Encrypt，不符就写 `bp_cert_issuer_bad` 的那条日志（B42 的信号源）。只读 + 写日志，**不建任何资源**。 |
| [`setup-scheduler.sh`](setup-scheduler.sh) | 建定时面：8 条 Cloud Scheduler + 2 个 Cloud Tasks 队列 + B42 的每日证书核对调度。 |
| [`setup-metrics.sh`](setup-metrics.sh) | 建 monitoring §3.2 的 11 条 log-based metric（渲染 LogMetric YAML 走 `--config-from-file`）。 |
| [`setup-alerts.sh`](setup-alerts.sh) | 按 ADR 0014 的 A/B/C 三级建通知渠道与告警策略。🔴 **ADR 0014 是「提案，未批准」——批准前不应 `--apply`。** |
| [`setup-wif.sh`](setup-wif.sh) | 建 GitHub Actions → GCP 的 Workload Identity Federation，并打印 `deploy.yml` 要的两个仓库变量。 |

**四支 `setup-*.sh` 截至 2026-08-30 一次都没有 `--apply` 过。**
「写了脚本」不是「建好了」——每支脚本的输出末尾都有一段「它建不了什么、卡在钱 / 凭据 /
未批准的裁决哪一样」，逐条汇总见 [`../deploy/README.md` §1.1](../deploy/README.md)。

---

## 先跑这一条

八个脚本**全部默认只读或 dry-run**，所以第一步永远是空手跑一次看它打算干什么：

```bash
./infra/scripts/<脚本>.sh --help      # 每支都有
./infra/scripts/<脚本>.sh             # 四支 setup-* 默认 dry-run，不会改任何东西
```

四条共同纪律（`set -euo pipefail` / 断言项目是 `oratis-491316` / 每条 `gcloud` 显式
`--project` / 危险操作手打确认串且默认 dry-run）写在
[`../deploy/README.md` §1](../deploy/README.md) 的表格下方，八个脚本逐条遵守。

CI 对 `infra/**/*.sh` 跑 shellcheck，**不降级**（`.github/workflows/ci.yml` 的 `shellcheck` 作业）。
本地复现：

```bash
docker run --rm -v "$PWD":/w -w /w koalaman/shellcheck:stable -x \
  $(find infra -type f -name '*.sh' | sort)
```
