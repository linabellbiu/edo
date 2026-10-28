FROM docker:28.3.3-cli AS docker-cli

FROM golang:1.26.10-bookworm AS go-source
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
# 只复制后端编译所需源码，前端或文档变化不使 Go 编译层失效。
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# 开发 Compose 只构建 Go，不等待生产 Web 打包。
FROM go-source AS go-builder
ARG EDO_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${EDO_VERSION}" -o /out/edo ./cmd/edo

# API 镜像只编译 Go；生产与开发 Web 均由 web/Dockerfile 独立构建。
FROM debian:bookworm-slim AS api-runtime
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
COPY --from=go-builder --chown=edo:edo /out/edo /app/edo
ENV EDO_DATABASE_DRIVER=sqlite \
    EDO_DATABASE_DSN=/app/data/edo.db
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/app/edo"]
CMD ["server"]
