#!/usr/bin/env bash
#
# build.sh —— 构建并推送 bp-cert-issuer-check 镜像，打印可直接喂给
# `setup-scheduler.sh --only=cert --cert-image=<ref>` 的镜像引用。
#
# 为什么不直接 `gcloud builds submit .`：仓库根目录带着 web/node_modules，
# 上传整个树既慢又没必要。这里把 Dockerfile 与脚本拷进一个干净的临时目录再提交。
# 构建上下文只有两个文件，任何人都能一眼看完镜像里装了什么。

set -euo pipefail

readonly PROJECT_ID="oratis-491316"
readonly AR_HOST="us-central1-docker.pkg.dev"
readonly AR_REPO="bp-images"
readonly IMAGE_NAME="bp-cert-issuer-check"

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
TAG="${1:-$(git -C "$ROOT" rev-parse --short=7 HEAD)}"
REF="${AR_HOST}/${PROJECT_ID}/${AR_REPO}/${IMAGE_NAME}:${TAG}"

CTX="$(mktemp -d "${TMPDIR:-/tmp}/bp-cert-build.XXXXXX")"
trap 'rm -rf "$CTX"' EXIT
cp "$HERE/Dockerfile" "$CTX/Dockerfile"
cp "$ROOT/infra/scripts/check-cert-issuer.sh" "$CTX/check-cert-issuer.sh"

printf '▸ 构建 %s\n' "$REF" >&2
gcloud builds submit "$CTX" --project="$PROJECT_ID" --tag="$REF" --timeout=10m >&2
printf '%s\n' "$REF"
