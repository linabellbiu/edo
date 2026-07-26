# ZRT

ZRT 是面向 Docker 与 Kubernetes 的运维、发布和可观测平台，使用 Go 与 React 构建；运行、构建和部署链路不依赖 Python。

## 主要能力

- Go、Gin、GORM；默认 SQLite，并兼容 PostgreSQL 与 MySQL。
- React、TypeScript、Vite、React Router、Zustand。
- Redis 保存登录会话、限流和短期状态；不承担任务队列职责。
- NATS JetStream 持久化任务、显式确认和死信，默认最多执行 4 次，发布与回滚默认只执行 1 次。
- 通用 Git，以及 GitHub、GitLab、Gitea、Gitee 仓库和 Webhook。
- 个人 Git 令牌库按用户隔离并加密保存；创建仓库时可选择已有令牌或直接创建并保存新令牌。
- Webhook 签名密钥可按独立权限重复查看，所有查看操作都会写入审计日志。
- 域名解析统一接入 Cloudflare、阿里云 DNS、腾讯云 DNSPod、AWS Route 53、华为云、Azure、Google Cloud、DigitalOcean、Gandi、GoDaddy、Namecheap、Hetzner、PowerDNS 和 RFC 2136；厂商凭据加密保存，解析变更受独立权限与审计控制。
- 应用可同时配置 dev、test、pre、prod 环境，每个环境分别选择分支、Pull、Push、PR、Tag 和部署方案。
- 流水线方案先在列表中统一管理，再进入类似 ComfyUI 的可缩放、可拖动无限画布编辑；环境、分支、代码事件、人工接测、审核和部署方案都在节点中配置，并可自由连线。未被应用使用的方案可以删除，使用中的方案受引用保护。
- 创建应用时选择已启用的流水线方案。ZRT 会复制当时的方案版本成为应用流水线，之后修改公共方案不会影响已有应用。
- 每个应用单独决定发布是否需要审核；每条发布计划关联启动时的应用流水线快照，开启审核后生产部署的所有路径都必须经过审核节点，申请人不能审核自己的计划。
- 构建方案支持脚本和 Dockerfile，部署方案支持脚本、Helm、Docker Compose 和 Docker；手动申请或代码事件会生成发布计划，并记录环境晋级、审核与部署就绪状态。
- 发布计划与发布记录位于同一页面；发布记录只提供查询，不提供目标配置、手工发布、审批或回滚入口。
- Docker API 与 Kubernetes API 发布、健康等待、生产审批和回滚。
- Docker 容器与 Kubernetes Pod 的 WebSocket 交互终端；不提供宿主机 SSH 登录和远程文件管理。
- 配置中心、Webhook 通知、HTTP 监控、安全白名单定时任务、任务中心。
- Argon2id、Redis 不透明会话、Casbin RBAC、操作审计、加密凭据与安全错误边界；角色权限可叠加用户级允许或拒绝规则。
- 本地账户、LDAP、通用 OAuth，以及飞书、Google、GitHub、GitLab 登录。

## 本地开发

需要 Go 1.26.5 或更新的安全补丁版本、Node.js 24 和 Docker。

```bash
cp .env.example .env
go install github.com/magefile/mage@v1.17.2
mage start
```

首次启动服务且账户库为空时会自动创建管理员账户 `admin`，初始密码为 `123456`；该账户登录后不会被强制修改密码。已有任意账户的数据库不会补建或覆盖默认管理员。普通新建账户仍须使用至少 12 位密码。

`mage start` 会读取 `.env`，构建 Web，并把页面资源嵌入 `bin/zrt`（Windows 为 `bin/zrt.exe`），然后迁移数据库并启动这个二进制。运行时不需要 `web/dist`、Node.js 或 Nginx，API 和页面都使用 `http://127.0.0.1:8080`。

构建后的程序可以单独复制到其他机器。新数据库先迁移，再启动服务：

```bash
./zrt migrate
./zrt
```

Windows 使用 `zrt.exe migrate` 和 `zrt.exe`。单文件只包含 ZRT 后端和 Web，Redis、NATS 以及 `.env` 中配置的外部数据库仍需单独提供；使用默认 SQLite 时，数据库文件会写入 `data/zrt.db`。

开发时执行 `mage start --dev`。Mage 会先通过 `deploy/compose.dev.yml` 启动 Redis 和 NATS，等待健康检查通过，再在本机执行数据库迁移、`go run` 和 `npm start`。开发页面地址为 `http://127.0.0.1:5173`，Go 后端不构建镜像，前端继续使用 Vite 热更新。

SQLite 固定保存在仓库的 `data/zrt.db`，Redis 和 NATS 分别保存在 `data/redis`、`data/nats`。`mage start --dev`、`mage start --docker` 和根目录 Compose 使用同一组文件，切换启动方式不会换库。依赖容器在 Mage 退出后继续运行，执行 `docker compose -f deploy/compose.dev.yml stop redis nats` 可以停止它们。首次运行缺少本机 Web 依赖时会自动执行 `npm ci`。不要直接删除 `data` 目录。

只迁移 `.env` 指定的数据库时执行：

```bash
mage migrate
```

该命令不启动 Redis、NATS、后端或 Web。`mage start` 和包含后端的 `mage start --dev` 已经自动执行迁移，不需要再手工运行。

只启动一个组件时增加 `--server` 或 `--web`，例如 `mage start --server`、`mage start --web`、`mage start --dev --server` 和 `mage start --dev --web`。不指定组件或同时指定两个组件时，后端和 Web 会一起启动。开发模式包含后端时会自动启动依赖并迁移数据库；仅运行 Web 时不会启动不需要的后端依赖。

执行 `mage help` 可查看中文命令总览，执行 `mage start --help` 可查看启动参数说明。

需要完全使用容器时执行 `mage start --docker`。该模式会在后台启动 Redis、NATS、迁移任务、后端和 Web；停止环境执行 `docker compose -f deploy/compose.dev.yml down`，该命令不会删除业务数据。`--dev` 和 `--docker` 不能同时使用。

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

详细步骤见 [部署说明](docs/deployment.md)，架构与安全取舍见 [架构说明](docs/refactor.md)，通用能力的复用与保留理由见 [第三方依赖决策](docs/dependencies.md)。

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
