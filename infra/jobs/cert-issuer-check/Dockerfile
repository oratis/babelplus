# bp-cert-issuer-check —— 每日证书签发者核对的 Cloud Run Job 镜像
#
# 事实源：infra/scripts/setup-scheduler.sh step_cert（形态 A：Cloud Run Job + Scheduler）
#         infra/scripts/check-cert-issuer.sh（真正干活的脚本，本镜像只是把它装进一个能跑的环境）
#         docs/04-ops/monitoring.md §8（判定规则与日志契约）
#
# 镜像只需要三样：bash、openssl、gcloud（写 Cloud Logging 用）。
# google/cloud-sdk:alpine 自带 gcloud，其余两样 apk 装。
# 🔴 tag 钉死：`latest` 会让「今天能跑」与「明天能跑」变成两件事。
FROM google/cloud-sdk:558.0.0-alpine

RUN apk add --no-cache bash openssl coreutils

# 与 setup-scheduler.sh 的 --args 路径逐字一致：/opt/bp/infra/scripts/check-cert-issuer.sh
COPY check-cert-issuer.sh /opt/bp/infra/scripts/check-cert-issuer.sh
RUN chmod 755 /opt/bp/infra/scripts/check-cert-issuer.sh

# 不设 ENTRYPOINT：Job 的 --command=/bin/bash --args=<脚本>,--require-targets 决定怎么跑。
