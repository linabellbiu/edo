FROM node:24.16.0-bookworm-slim AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 使用 NGINX 官方稳定版，运行时不携带 Node、源码或后端二进制。
FROM nginx:1.30.6-alpine AS web-runtime
COPY web/nginx.conf /etc/nginx/nginx.conf
COPY --from=web-builder /src/web/dist/ /usr/share/nginx/html/
# 官方镜像的 nginx 用户为 101；配置中的 PID 和临时目录统一写入 /tmp。
USER 101:101
EXPOSE 8080
# 配置已在构建时固定，跳过需要修改 /etc 的官方模板入口脚本。
ENTRYPOINT ["nginx"]
CMD ["-g", "daemon off;"]
