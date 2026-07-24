# ZRT

ZRT 是面向 Docker 与 Kubernetes 的运维、发布和可观测平台，使用 Go 与 React 构建；运行、构建和部署链路不依赖 Python。

## 主要能力

- Go、Gin、GORM；默认 SQLite，并兼容 PostgreSQL 与 MySQL。
- React、TypeScript、Vite、React Router、Zustand。
- Redis 保存登录会话、限流和短期状态；不承担任务队列职责。
- NATS JetStream 持久化任务、显式确认和死信，默认最多执行 4 次，发布与回滚默认只执行 1 次。
- 通用 Git，以及 GitHub、GitLab、Gitea、Gitee 仓库和 Webhook。
- 应用可同时配置 dev、test、pre、prod 环境，每个环境分别选择分支、Pull、Push、PR、Tag、发布方案和发布目标。
- 公共发布计划使用类似 ComfyUI 的可缩放、可拖动无限画布；环境、分支、代码事件、人工接测、审核、发布方案和发布目标都在节点中配置，并可自由连线。
- 创建应用时直接选择已启用的公共发布计划。ZRT 会复制当时的计划版本和环境配置，之后修改公共计划不会影响正在运行的应用。
- 每个应用单独决定发布计划是否需要审核；开启后，生产部署的所有路径都必须经过审核节点，申请人不能审核自己的计划。
- 构建方案支持脚本和 Dockerfile，发布方案支持脚本、Helm、Docker Compose 和 Docker；代码变化、环境晋级、审核与部署就绪状态会保存为流水线记录。
- Docker API 与 Kubernetes API 发布、健康等待、生产审批和回滚。
- Docker 容器与 Kubernetes Pod 的 WebSocket 交互终端；不提供宿主机 SSH 登录和远程文件管理。
- 配置中心、Webhook 通知、HTTP 监控、安全白名单定时任务、任务中心。
- Argon2id、Redis 不透明会话、RBAC、操作审计、加密凭据与安全错误边界。
- 本地账户、LDAP、通用 OAuth，以及飞书、Google、GitHub、GitLab 登录。

## 本地开发

需要 Go 1.26.5 或更新的安全补丁版本、Node.js 24、Docker 和 Git。

```bash
cp .env.example .env
docker compose -f deploy/compose.dev.yml up --build
```

首次启动服务且账户库为空时会自动创建管理员账户 `admin`，初始密码为 `123456`；该账户登录后不会被强制修改密码。已有任意账户的数据库不会补建或覆盖默认管理员。普通新建账户仍须使用至少 12 位密码。

Compose 会先运行一次性 `migrate`，成功后启动 Go 后端，后端健康后再启动 Vite 前端；Redis 和 NATS JetStream 也由同一命令启动。浏览器访问 `http://127.0.0.1:5173`，前端会通过容器网络把 API 和 WebSocket 请求代理到后端。修改 Go 代码后重新执行带 `--build` 的启动命令即可重建后端；前端源码通过挂载提供给 Vite。停止环境执行 `docker compose -f deploy/compose.dev.yml down`，该命令不会删除业务数据。

登录后可在“平台管理 → 登录方式”中接入 LDAP 或 OAuth。开发 Compose 自带一份仅供本机使用的加密密钥；多人共享、测试和生产环境必须在 `.env` 中设置自己的 `ZRT_SECRETS_KEY`，否则不同环境会错误地共用密钥。

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
