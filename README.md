# ZRT

ZRT 是面向 Docker 与 Kubernetes 的运维、发布和可观测平台，使用 Go 与 React 构建；运行、构建和部署链路不依赖 Python。

## 主要能力

- Go、Gin、GORM；默认 SQLite，并兼容 PostgreSQL 与 MySQL。
- React、TypeScript、Vite、React Router、Zustand。
- Redis 保存登录会话、限流和短期状态；不承担任务队列职责。
- NATS JetStream 持久化任务、显式确认和死信，默认最多执行 4 次，发布与回滚默认只执行 1 次。
- 通用 Git，以及 GitHub、GitLab、Gitea、Gitee 仓库和 Webhook。
- Docker API 与 Kubernetes API 发布、健康等待、生产审批和回滚。
- Docker 容器与 Kubernetes Pod 的 WebSocket 交互终端；不提供宿主机 SSH 登录和远程文件管理。
- 配置中心、Webhook 通知、HTTP 监控、安全白名单定时任务、任务中心。
- Argon2id、Redis 不透明会话、RBAC、操作审计、加密凭据与安全错误边界。

## 本地开发

需要 Go 1.26.5 或更新的安全补丁版本、Node.js 24、Docker 和 Git。

```bash
cp .env.example .env
docker compose -f deploy/compose.dev.yml up -d
go run ./cmd/zrt migrate
go run ./cmd/zrt admin create --username admin --nickname 管理员
npm ci --prefix web
```

分别启动 ZRT 服务和前端开发服务器。`server` 会使用 Go Goroutine 同时运行 API 与后台任务：

```bash
go run ./cmd/zrt server
npm run dev --prefix web
```

浏览器访问 `http://127.0.0.1:5173`。Vite 会把 API 和 WebSocket 请求代理到 `http://127.0.0.1:8080`。

## 构建与测试

```bash
make test
make build
```

`make test` 会执行 Go 测试、Go 静态检查和 React 生产构建。PostgreSQL/MySQL 实库迁移测试可通过 `ZRT_TEST_POSTGRES_DSN`、`ZRT_TEST_MYSQL_DSN` 启用，CI 默认执行。

## 部署

- 单机或试用：根目录 `docker-compose.yml`，默认 SQLite。
- Kubernetes：`deploy/kubernetes/`，默认连接外部 PostgreSQL、Redis 与 NATS。
- 生产环境必须设置 `ZRT_SECRETS_KEY`；生成方式：`openssl rand -base64 32`。
- 生产入口应使用 HTTPS，并设置 `ZRT_AUTH_COOKIE_SECURE=true`。

详细步骤见 [部署说明](docs/deployment.md)，架构与安全取舍见 [架构说明](docs/refactor.md)。

## 从旧版迁移

先备份旧数据库，再使用只读数据库账户执行预检：

```bash
ZRT_LEGACY_DATABASE_DRIVER=mysql \
ZRT_LEGACY_DATABASE_DSN='readonly:password@tcp(db:3306)/zrt_legacy?charset=utf8mb4&parseTime=True&loc=UTC' \
go run ./cmd/zrt legacy-import --dry-run
```

去掉 `--dry-run` 才会写入 ZRT 数据库。迁入账户默认停用，必须执行以下命令重置密码后才会启用：

```bash
go run ./cmd/zrt admin reset-password --username admin
```

迁移范围和明确不迁移的高风险功能见 [迁移说明](docs/migration.md)。

## 命名约定

面向用户的产品名使用 `ZRT`；二进制、Go module、容器、服务、Redis Key 和 NATS Subject 使用小写 `zrt`；环境变量使用 `ZRT_` 前缀。

## 来源与许可

本项目许可条款见 [LICENSE](LICENSE)。
