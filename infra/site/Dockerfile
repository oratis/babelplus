# bp-site —— 官网（babel.plus）的静态托管镜像
#
# 事实源：web/site/（Vite 构建，产出是**零 JavaScript 的静态页**）
#         docs/04-ops/deploy.md §10/§11（域名与证书）
#
# 🔴 与 bp-web / bp-admin 的一处**刻意不同**：那两个服务的 Dockerfile 与 nginx.conf
#    当初留在临时目录、没有进仓库（first-deploy §「这次没有解决的」明写「下一个人无法照着仓库重发」）。
#    本服务不重复那个欠账 —— 镜像、配置、部署脚本三样都在仓库里。
#
# 构建上下文是**仓库根**（部署脚本会把它裁成一个只含 web/ 的临时目录，
# 否则 node_modules 会被整个上传给 Cloud Build）。
FROM node:24-alpine AS build
WORKDIR /src
RUN corepack enable
COPY web/ ./web/
WORKDIR /src/web
# 🔴 --ignore-scripts：站点一行 postinstall 都不需要，而工作区里有 Electron 之外的
#    平台二进制（tailwind oxide）会在这里白下载。站点只依赖 vite + typescript。
RUN pnpm install --frozen-lockfile --ignore-scripts --filter @babelplus/site... \
 && pnpm --filter @babelplus/site build

FROM nginx:1.29-alpine
# 非 root：Cloud Run 不要求，但一个只发静态文件的容器没有理由用 root。
RUN sed -i 's/^user  nginx;/user  nginx;/' /etc/nginx/nginx.conf
COPY infra/site/nginx.conf /etc/nginx/conf.d/default.conf
COPY infra/site/headers.conf /etc/nginx/bp-headers.conf
COPY --from=build /src/web/site/dist/ /usr/share/nginx/html/
# Cloud Run 注入 PORT，但 nginx 不读环境变量。8080 是 Cloud Run 的默认值，与部署脚本一致。
EXPOSE 8080
CMD ["nginx", "-g", "daemon off;"]
