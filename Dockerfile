FROM node:24.16.0-bookworm-slim AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26.5-bookworm AS go-source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# 开发 Compose 只构建 Go，不等待生产 Web 打包。
FROM go-source AS go-builder
ARG ZRT_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${ZRT_VERSION}" -o /out/zrt ./cmd/zrt

# 正式镜像把 Web 产物编进二进制，运行层不再保存另一份静态文件。
FROM go-source AS go-release-builder
COPY --from=web-builder /src/web/dist/ ./internal/webui/dist/
ARG ZRT_VERSION=dev
RUN CGO_ENABLED=0 go build -tags=zrt_web -trimpath -ldflags="-s -w -X main.version=${ZRT_VERSION}" -o /out/zrt ./cmd/zrt

FROM debian:bookworm-slim
RUN apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install -y --no-install-recommends ca-certificates curl tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 zrt \
    && useradd --uid 10001 --gid zrt --no-create-home --home-dir /app --shell /usr/sbin/nologin zrt \
    && mkdir -p /app/data \
    && chown -R zrt:zrt /app
WORKDIR /app
COPY --from=go-release-builder --chown=zrt:zrt /out/zrt /app/zrt
ENV ZRT_DATABASE_DRIVER=sqlite \
    ZRT_DATABASE_DSN=/app/data/zrt.db
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/app/zrt"]
CMD ["server"]
