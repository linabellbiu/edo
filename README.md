# ZRT

ZRT 是面向 Docker 与 Kubernetes 的运维、发布和可观测平台，使用 Go 与 Vue 3 构建；运行、构建和部署链路不依赖 Python。

## 主要能力

- Go、Gin、GORM；默认 SQLite，并兼容 PostgreSQL 与 MySQL。
- Vue 3、TypeScript、Vite、Vue Router、Pinia、Ant Design Vue，整体交互以 Vben Admin 5 `web-antd` 为基线。
- Redis 保存登录会话、限流和短期状态；不承担任务队列职责。
- 登录失败锁定默认关闭，可由管理员在系统设置中开启；开启后使用 Redis 记录有限窗口内的失败次数。
- NATS JetStream 持久化任务、显式确认和死信，默认最多执行 4 次，发布与回滚默认只执行 1 次。
- 通用 Git，以及 GitHub、GitLab、Gitea、Gitee 仓库和 Webhook；外部 Webhook API 默认关闭，由管理员在系统设置中显式开启。
- 个人 Git 令牌库按用户隔离并加密保存；创建仓库时可选择已有令牌或直接创建并保存新令牌。
- Webhook 签名密钥可按独立权限重复查看，所有查看操作都会写入审计日志。
- 域名解析统一接入 Cloudflare、阿里云 DNS、腾讯云 DNSPod、AWS Route 53、华为云、Azure、Google Cloud、DigitalOcean、Gandi、GoDaddy、Namecheap、Hetzner、PowerDNS 和 RFC 2136；厂商凭据加密保存，解析变更受独立权限与审计控制。
- 一个应用对应一个代码仓库；代码仓库只保存 Git 来源、凭据和可选 Webhook，构建与部署方案绑定在应用上。多应用的并行、串行和依赖由发布组编排。
- 应用可同时配置自定义流程阶段，每个触发节点选择任意分支或通配规则以及分支变更、PR、Tag 规则。ZRT 默认主动定时读取远程分支、托管平台 PR/MR 和 Tag，自动流水线不依赖 Webhook。
- 流水线方案先在列表中统一管理，再进入类似 ComfyUI 的可缩放、可拖动无限画布编辑；环境、分支、代码事件、人工接测、审核和部署方案都在节点中配置，并可自由连线。未被应用使用的方案可以删除，使用中的方案受引用保护。
- 创建应用时选择已启用的流水线方案。ZRT 会复制当时的方案版本成为应用流水线，之后修改公共方案不会影响已有应用。
- 每个应用单独决定发布是否需要审核；每次流水线运行关联启动时的应用流水线快照，开启审核后生产部署的所有路径都必须经过审核节点，申请人不能审核自己的运行。
- 构建方案支持脚本和 Dockerfile，部署方案支持脚本、Helm、Docker Compose 和 Docker；手动操作或代码事件会生成流水线运行，并记录环境晋级、审核与部署就绪状态。
- 发布计划表示一次迭代、版本或发布列车，例如 `2026.08`；计划内创建发布组，配置多应用的并行、串行、组间依赖和失败策略。
- 发布计划、流水线运行和发布记录位于同一页面的独立标签；发布记录只提供查询，不提供目标配置、手工发布、审批或回滚入口。
- 流水线运行提供实时日志弹窗，按阶段展示代码检出、Docker/BuildKit 构建、镜像传输、部署与最终结果；页面关闭或服务重启后仍可查看已经保存的历史日志。
- Docker API 与 Kubernetes API 发布、健康等待、生产审批和回滚。
- 镜像仓库为可选项：Compose 部署使用隔离的 Docker-in-Docker，二进制和 `mage start --dev` 直接调用宿主机 Docker/Buildx；本地 Docker 环境直接部署同一构建运行时内的镜像并跳过 SSH。远程 Docker SSH 环境未绑定仓库时通过 `docker save | ssh docker load` 流式传输，导出前固定源镜像 ID，加载后读取目标镜像 ID，并用于发布前校验；绑定仓库时推送不可变 Digest。Kubernetes 发布仍需镜像仓库。
- Docker 容器与 Kubernetes Pod 的 WebSocket 交互终端；不提供宿主机 SSH 登录和远程文件管理。
- 独立环境管理与流水线部署节点绑定；Kubernetes 使用集群 API，Docker 可选择内置本地目标，或使用测试通过且固定主机指纹的远程 SSH 地址。
- 配置中心、Webhook 通知、HTTP 监控、安全白名单定时任务、任务中心。
- 内置系统监控页面，实时展示节点与进程 CPU/内存、Go GC、Worker 并发、任务状态、JetStream 积压与死信、Outbox 和数据库连接池，不依赖 Grafana。
- Argon2id、Redis 不透明会话、Casbin RBAC、操作审计、加密凭据与安全错误边界；角色权限可叠加用户级允许或拒绝规则。
- 本地账户、LDAP、通用 OAuth，以及飞书、Google、GitHub、GitLab 登录。

## 本地开发

需要 Go 1.26.5 或更新的安全补丁版本、Node.js 24，以及带 Buildx 的 Docker CLI。Docker Desktop（Windows/macOS）默认包含 Buildx；Linux 使用 Docker Engine 时需安装官方 Buildx 插件。ZRT 的正式运行镜像已经内置固定版本的 Docker CLI/Buildx。

```bash
cp .env.example .env
openssl rand -base64 32
go install github.com/magefile/mage@v1.17.2
mage start
```

把 `openssl` 输出填入根目录 `.env` 的 `ZRT_SECRETS_KEY`。密钥生成后必须固定保存并备份；已有数据库必须继续使用原密钥，不能重新生成后直接替换。

首次启动服务且账户库为空时会自动创建管理员账户 `admin`，初始密码为 `123456`；该账户登录后不会被强制修改密码。已有任意账户的数据库不会补建或覆盖默认管理员。普通新建账户仍须使用至少 12 位密码。

`mage start` 会读取 `.env`，构建 Web，并把页面资源嵌入 `bin/zrt`（Windows 为 `bin/zrt.exe`），然后迁移数据库并启动这个二进制。运行时不需要 `web/dist`、Node.js 或 Nginx，API 和页面都使用 `http://127.0.0.1:8080`。如果要执行 Dockerfile 流水线，宿主机仍需提供 Docker CLI/Buildx；使用 ZRT 官方容器镜像时已经内置，无需另行安装。

构建后的程序可以单独复制到其他机器。新数据库先迁移，再启动服务：

```bash
./zrt migrate
./zrt
```

Windows 使用 `zrt.exe migrate` 和 `zrt.exe`。单文件只包含 ZRT 后端和 Web，Redis、NATS 以及 `.env` 中配置的外部数据库仍需单独提供；使用默认 SQLite 时，数据库文件会写入 `data/zrt.db`。

开发时执行 `mage start --dev`。Mage 会先通过 `deploy/compose.dev.yml` 启动 Redis 和 NATS，等待健康检查通过，再在本机执行数据库迁移、`go run` 和 `npm start`。开发页面地址为 `http://127.0.0.1:5173`，流水线直接使用宿主机 Docker，不启动 DinD；前端继续使用 Vite 热更新。

SQLite 固定保存在仓库的 `data/zrt.db`，Redis 和 NATS 分别保存在 `data/redis`、`data/nats`。`mage start`、`mage start --dev`、`mage start --docker` 和 Compose 都读取根目录 `.env` 并使用同一组数据文件，切换启动方式不会换库或密钥。依赖容器在 Mage 退出后继续运行，执行 `docker compose --env-file .env -f deploy/compose.dev.yml stop redis nats` 可以停止它们。Compose 的 DinD 只服务容器后端且不映射 2375 到宿主机。首次运行缺少本机 Web 依赖时会自动执行 `npm ci`。不要直接删除 `data` 目录。

只迁移 `.env` 指定的数据库时执行：

```bash
mage migrate
```

该命令不启动 Redis、NATS、后端或 Web。`mage start` 和包含后端的 `mage start --dev` 已经自动执行迁移，不需要再手工运行。

只启动一个组件时增加 `--server` 或 `--web`，例如 `mage start --server`、`mage start --web`、`mage start --dev --server` 和 `mage start --dev --web`。不指定组件或同时指定两个组件时，后端和 Web 会一起启动。开发模式包含后端时会自动启动依赖并迁移数据库；仅运行 Web 时不会启动不需要的后端依赖。

执行 `mage help` 可查看中文命令总览，执行 `mage start --help` 可查看启动参数说明。

需要完全使用容器时执行 `mage start --docker`。该模式会在后台启动 Redis、NATS、迁移任务、后端和 Web；停止环境执行 `docker compose --env-file .env -f deploy/compose.dev.yml down`，该命令不会删除业务数据。`--dev` 和 `--docker` 不能同时使用。

登录后可在“平台管理 → 系统设置 → 登录方式”中接入 LDAP 或 OAuth。Mage 和 Compose 不会生成、回退或轮换加密密钥；根目录 `.env` 缺失、密钥为空或格式错误时，包含后端的启动会在迁移和启动前直接失败。

## 构建与测试

```bash
make test
make build
```

`make test` 会执行 Go 测试、Go 静态检查和 Vue 生产构建。PostgreSQL/MySQL 实库迁移测试可通过 `ZRT_TEST_POSTGRES_DSN`、`ZRT_TEST_MYSQL_DSN` 启用，CI 默认执行。

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
