FROM docker:28.3.3-cli AS docker-cli

FROM node:24.16.0-bookworm-slim AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26.5-bookworm AS go-source
WORKDIR /src
# Buildx 负责与 Docker-in-Docker 中的 BuildKit 建立双向 session；仅调用 HTTP build API 无法传输上下文和认证。
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-compose
COPY go.mod go.sum ./
# Go 模块代理的暂态断连只在镜像构建阶段有限重试，避免一次 EOF 让整条流水线直接失败。
RUN set -eu; \
    attempt=1; \
    until go mod download; do \
      if [ "$attempt" -ge 3 ]; then \
        exit 1; \
      fi; \
      wait_seconds=$((attempt * 2)); \
      echo "Go 模块下载失败，${wait_seconds} 秒后进行第 $((attempt + 1)) 次尝试" >&2; \
      sleep "$wait_seconds"; \
      attempt=$((attempt + 1)); \
    done
COPY . .

# 开发 Compose 只构建 Go，不等待生产 Web 打包。
FROM go-source AS go-builder
ARG EDO_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${EDO_VERSION}" -o /out/edo ./cmd/edo

# 正式镜像把 Web 产物编进二进制，运行层不再保存另一份静态文件。
FROM go-source AS go-release-builder
COPY --from=web-builder /src/web/dist/ ./internal/webui/dist/
ARG EDO_VERSION=dev
RUN CGO_ENABLED=0 go build -tags=edo_web -trimpath -ldflags="-s -w -X main.version=${EDO_VERSION}" -o /out/edo ./cmd/edo

FROM debian:bookworm-slim
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-compose
RUN apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install -y --no-install-recommends ca-certificates curl tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 edo \
    && useradd --uid 10001 --gid edo --no-create-home --home-dir /app --shell /usr/sbin/nologin edo \
    && mkdir -p /app/data \
    && chown -R edo:edo /app
WORKDIR /app
COPY --from=go-release-builder --chown=edo:edo /out/edo /app/edo
ENV EDO_DATABASE_DRIVER=sqlite \
    EDO_DATABASE_DSN=/app/data/edo.db
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/app/edo"]
CMD ["server"]
