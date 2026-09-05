# bp-web / bp-admin —— 两个 SPA 的静态托管镜像（Cloud Run + nginx，过渡形态）
#
# 事实源：web/user/（bp-web）与 web/admin/（bp-admin），Vite 构建；
#         docs/04-ops/first-deploy-20260831.md §4 的欠账：「bp-web / bp-admin 的 Dockerfile 与 nginx.conf
#         不在仓库里（临时目录），下一个人无法照着仓库重发这两个服务」—— 本文件（2026-09-05）关掉这条。
#         docs/04-ops/deploy.md §10（构建产物只有一份）· infra/site/Dockerfile（同一套写法）
#
# 构建参数 APP = user | admin，对应 pnpm 包 @babelplus/user / @babelplus/admin。
# 构建上下文是仓库根（部署脚本会裁成只含 web/ 与 infra/web/ 的临时目录，否则 node_modules 会整个上传）。
#
# ⚠️ 这仍然是 first-deploy §4 记的**过渡形态**（Cloud Run + nginx 静态托管），不是 ADR 0003 的裁决结果；
#    deploy-web.sh 里的 Cloudflare / GitHub / Netlify 三个目标一个都还没用上。把镜像定义进仓库
#    只是让「重发」可复现，不是把形态定下来。
FROM node:24-alpine AS build
ARG APP=user
WORKDIR /src
RUN corepack enable
COPY web/ ./web/
WORKDIR /src/web
# 不加 --ignore-scripts：user / admin 依赖 @tailwindcss/oxide 与 esbuild 的平台二进制，
# 它们由 optionalDependencies 带入、不靠 postinstall，但 esbuild 的 postinstall 会自检二进制，
# 跳过它的收益只是省几秒，代价是构建失败时报错指向别处。
RUN pnpm install --frozen-lockfile --filter "@babelplus/${APP}..." \
 && pnpm --filter "@babelplus/${APP}" build

FROM nginx:1.29-alpine
ARG APP=user
COPY infra/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY infra/web/headers.conf /etc/nginx/bp-headers.conf
COPY --from=build /src/web/${APP}/dist/ /usr/share/nginx/html/
# Cloud Run 注入 PORT，但 nginx 不读环境变量。8080 是 Cloud Run 的默认值，与部署脚本一致。
EXPOSE 8080
CMD ["nginx", "-g", "daemon off;"]
