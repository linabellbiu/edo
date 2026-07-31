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
- 一个应用只关联一个代码仓库和一条流水线；代码仓库只保存 Git 来源、凭据和可选 Webhook，不绑定构建方案、部署方案或镜像仓库。多应用的并行、串行和依赖由发布组编排。
- 每条流水线只有一个代码源，可选择任意分支或通配规则以及 Push、Pull Request、Tag 和手动启动。ZRT 默认主动读取远程分支、托管平台 PR/MR 和 Tag，自动流水线不依赖 Webhook。
- 流水线编辑器采用类似 Gitee 的固定图形结构：代码源固定在左侧，阶段按列横向展开，阶段内使用紧凑任务链；阶段边界和任务之间可就地插入，点击后在右侧抽屉配置。定义直接保存为版本化的 `source + stages[].tasks[]`，不再使用自由连线、节点坐标或无限画布。
- 应用可以引用已启用的流水线方案并跟随其最新已启用版本；应用直接编辑流水线时会转为自定义配置。每次运行保存启动时的代码、任务、构建方案、制品、部署方案和内部目标快照。
- 审核只由流水线中的审核任务决定，应用和环境不提供隐式审核开关；申请人不能审核自己的运行。
- 构建方案支持 Dockerfile 和受控 Shell。Dockerfile 构建登记 OCI 镜像制品，可选推送镜像仓库；Shell 构建声明产物路径，由 ZRT 打包、计算摘要并登记文件制品。手工上传和制品列表位于构建方案入口。
- 部署方案把“怎么执行”和“部署到哪里”一次配置完整，支持主机脚本、Docker 容器、内联 Docker Compose 和 Kubernetes Deployment。流水线部署任务只选择部署方案并消费上游不可变制品，不再填写 Dockerfile、构建上下文、独立环境或发布目标。
- 发布计划表示一次迭代、版本或发布列车，例如 `2026.08`；计划内创建发布组，配置多应用的并行、串行、组间依赖和失败策略。
- 发布计划、流水线运行和发布记录位于同一页面的独立标签；发布记录只提供查询，不提供目标配置、手工发布、审批或回滚入口。
- 流水线运行提供实时日志弹窗，按阶段展示代码检出、Docker/BuildKit 构建、镜像传输、部署与最终结果；页面关闭或服务重启后仍可查看已经保存的历史日志。
- Docker API 与 Kubernetes API 发布、健康等待、生产审批和回滚。
- Dockerfile 构建方案可以选择镜像仓库：Compose 使用隔离的 Docker-in-Docker，二进制和 `mage start --dev` 直接调用宿主机 Docker/Buildx；本地 Docker 环境直接部署同一构建运行时内的镜像并跳过 SSH。远程 Docker SSH 环境未绑定仓库时通过 `docker save | ssh docker load` 流式传输，绑定仓库时推送并部署不可变 Digest。Kubernetes 发布必须使用集群可拉取的镜像仓库。
- Docker 容器与 Kubernetes Pod 的 WebSocket 交互终端；不提供宿主机 SSH 登录和远程文件管理。
- 独立环境管理与流水线部署节点绑定；Kubernetes 使用集群 API，Docker 可选择内置本地目标，或使用测试通过且固定主机指纹的远程 SSH 地址。
- 配置中心、Webhook 通知、HTTP 监控、安全白名单定时任务、任务中心。
- 内置系统监控页面，实时展示节点与进程 CPU/内存、Go GC、Worker 并发、任务状态、JetStream 积压与死信、Outbox 和数据库连接池，不依赖 Grafana。
- 独立“日志”页面只展示当前 ZRT 系统进程及内部模块输出；已部署容器的 stdout/stderr 不会被采集到这里，只能从具体容器的只读日志入口按需查看。
- Argon2id、Redis 不透明会话、Casbin RBAC、操作审计、加密凭据与安全错误边界；角色权限可叠加用户级允许或拒绝规则。
- 本地账户、LDAP、通用 OAuth，以及飞书、Google、GitHub、GitLab 登录。

## 本地开发

需要 Go 1.26.5 或更新的安全补丁版本、Node.js 24，以及带 Buildx 和 Compose v2 插件的 Docker CLI。Docker Desktop（Windows/macOS）默认包含这两个插件；Linux 使用 Docker Engine 时需安装官方插件。ZRT 的正式运行镜像已经内置固定版本的 Docker CLI、Buildx 和 Compose v2。

```bash
go install github.com/magefile/mage@v1.17.2
mage start
```

首次运行 Mage 时，如果根目录没有 `.env`，会复制 `.env.example` 的本地默认配置，生成 32 字节随机 `ZRT_SECRETS_KEY` 并持久化到新文件；如果进程环境已经提供有效密钥，则固定保存该密钥，避免本次运行与后续重启使用不同值。创建采用独占写入，已有 `.env` 不会被覆盖，密钥也不会被自动轮换。密钥生成后必须固定保存并备份；已有数据库必须继续使用原密钥，不能删除 `.env` 后重新生成。

需要在首次启动前修改数据库或服务连接时，可以先手动复制 `.env.example` 为 `.env`，再填写配置并使用 `openssl rand -base64 32` 生成密钥。直接使用 Docker Compose 不经过 Mage，仍须手动完成这一步。

`.env` 不是只保存密钥：`ZRT_DATABASE_DRIVER`、`ZRT_DATABASE_DSN`、`ZRT_REDIS_URL` 和 `ZRT_NATS_URL` 是宿主机运行连接。默认分别使用 `data/zrt.db`、`redis://127.0.0.1:6379/0` 和 `nats://127.0.0.1:4222`。Redis 与 NATS 的用户名、密码可直接写入各自 URL，但真实凭据不得提交。启动 Mage 前已经导出的同名进程环境变量优先于 `.env`。

首次启动服务且账户库为空时会自动创建管理员账户 `admin`，初始密码为 `123456`；该账户登录后不会被强制修改密码。已有任意账户的数据库不会补建或覆盖默认管理员。普通新建账户仍须使用至少 12 位密码。

全新实例还会创建默认 Dockerfile 构建方案和已启用的快速开始流水线；本地 Docker 探测可用时，流水线同时包含本地 Docker 部署。首次使用通常只需添加代码仓库，再创建应用并确认系统预选的仓库和流水线方案，随后选择分支或 Tag 执行。自动 Push 触发默认不开启，避免未知代码变化直接部署。默认部署使用容器名 `zrt-quickstart` 且不主动发布端口，第二个应用应先复制部署方案并设置独立容器名和实际端口。已有或已删除过交付资源的实例不会在重启时重新写入这些默认项。

`mage start` 会读取 `.env`，构建 Web，并把页面资源嵌入 `bin/zrt`（Windows 为 `bin/zrt.exe`），然后迁移数据库并启动这个二进制。运行时不需要 `web/dist`、Node.js 或 Nginx，API 和页面都使用 `http://127.0.0.1:8080`。如果要执行 Dockerfile 流水线，宿主机仍需提供 Docker CLI/Buildx；使用 ZRT 官方容器镜像时已经内置，无需另行安装。

构建后的程序可以单独复制到其他机器。新数据库先迁移，再启动服务：

```bash
./zrt migrate
./zrt
```

Windows 使用 `zrt.exe migrate` 和 `zrt.exe`。单文件只包含 ZRT 后端和 Web，Redis、NATS 以及 `.env` 中配置的外部数据库仍需单独提供；使用默认 SQLite 时，数据库文件会写入 `data/zrt.db`。

开发时执行 `mage start --dev`。Mage 会读取根目录 `.env`，先通过 `deploy/compose.dev.yml` 启动 Redis 和 NATS，等待健康检查通过，再在本机使用 `.env` 中的宿主机连接执行数据库迁移、`go run` 和 `npm start`。开发页面地址为 `http://127.0.0.1:5173`，流水线直接使用宿主机 Docker，不启动 DinD；前端继续使用 Vite 热更新。

SQLite 固定保存在仓库的 `data/zrt.db`，Redis 和 NATS 分别保存在 `data/redis`、`data/nats`。`mage start`、`mage start --dev`、`mage start --docker` 和 Compose 都读取根目录 `.env` 并使用同一组数据文件和密钥。由于容器内的 `127.0.0.1` 指向容器自身，Compose 会把 `.env` 的 `ZRT_COMPOSE_DATABASE_*`、`ZRT_COMPOSE_REDIS_URL` 和 `ZRT_COMPOSE_NATS_URL` 映射成容器进程实际配置；默认使用 `/app/data/zrt.db`、`redis://redis:6379/0` 和 `nats://nats:4222`。依赖容器在 Mage 退出后继续运行，执行 `docker compose --env-file .env -f deploy/compose.dev.yml stop redis nats` 可以停止它们。Compose 的 DinD 只服务容器后端且不映射 2375 到宿主机。首次运行、`package-lock.json` 变化或关键 Vue 包缺失时会自动执行 `npm ci --include=dev`，并清理其他包管理器留下的不一致依赖。不要直接删除 `data` 目录。

只迁移 `.env` 指定的数据库时执行：

```bash
mage migrate
```

该命令不启动 Redis、NATS、后端或 Web。`mage start` 和包含后端的 `mage start --dev` 已经自动执行迁移，不需要再手工运行。

只启动一个组件时增加 `--server` 或 `--web`，例如 `mage start --server`、`mage start --web`、`mage start --dev --server` 和 `mage start --dev --web`。不指定组件或同时指定两个组件时，后端和 Web 会一起启动。开发模式包含后端时会自动启动依赖并迁移数据库；仅运行 Web 时不会启动不需要的后端依赖。

执行 `mage help` 可查看中文命令总览，执行 `mage start --help` 可查看启动参数说明。

需要完全使用容器时执行 `mage start --docker`。该模式会读取 `.env` 中的 `ZRT_COMPOSE_*` 容器连接配置，并在后台启动 Redis、NATS、迁移任务、后端和 Web；停止环境执行 `docker compose --env-file .env -f deploy/compose.dev.yml down`，该命令不会删除业务数据。`--dev` 和 `--docker` 不能同时使用。

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
