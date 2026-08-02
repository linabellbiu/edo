# EDO

EDO（EasyDevOps）是一套给中小公司和小团队使用的发布自动化工具。它解决的是一件很实际的事：少花时间打包和部署，多花时间做产品。

## 为什么做 EDO

小团队的发布流程往往从几条命令开始：拉代码、打包、上传、登录服务器、重启服务。项目少的时候还能应付，项目和环境多起来以后，漏步骤、版本不一致、发布失败、记录找不到都会变成日常问题。

现有工具也不总是合适：

- 有些开源项目已经停更，碰到兼容问题或历史 Bug 只能自己处理。
- 有些云服务的免费版本有时间、次数或资源限制，用量上来后就要付费或迁移。
- 大型平台功能很多，但安装和维护也更复杂，小团队通常没有人专门照看它。
- 有些工具接入过程太长，最后还不如继续用脚本和手工发布。

EDO 想把这件事做得简单一些。接好代码仓库，选好触发条件，再配置测试、构建和部署步骤，后面的发布就交给系统执行。过程有日志，结果能追踪，失败后也知道从哪里查。

## EDO 的特点

- **启动快**：本地开发、单文件运行和 Docker Compose 都有现成命令。默认配置可以直接使用，不需要先搭一套复杂的平台。
- **少做重复工作**：代码检出、测试、构建镜像、登记制品、部署、健康检查和日志保存都放进流水线，不用每次上线重新敲一遍命令。
- **流程容易理解**：流水线就是代码源、阶段和任务。支持 Push、Pull Request、Tag 和手动执行，审核、通知、并行任务和多应用发布按需添加。
- **自己部署，没有试用期限**：代码、配置、凭据、制品和运行记录都留在自己的环境里，不受云服务免费额度和套餐变化影响。
- **日常维护量小**：Go 后端使用单进程，Web 可以打进同一个 `edo` 二进制。默认数据库是 SQLite，规模上来后再切换 PostgreSQL 或 MySQL。
- **Docker 和 Kubernetes 都能用**：小项目可以先用本地或远程 Docker，需要时再接 Kubernetes。构建、部署、日志和容器终端都走标准 API。
- **发布记录完整**：任务通过 NATS JetStream 持久化，支持确认、有限重试和死信。每次运行保留配置快照和执行日志，重新执行不会覆盖原记录。
- **权限不靠共享密码**：系统提供权限控制、加密凭据、SSH 主机指纹校验和操作审计，可以把发布权限分给团队成员。
- **基本监控不用另外搭平台**：CPU、内存、Go GC、Worker、任务、JetStream 和数据库连接池状态可以直接在 EDO 中查看。

## 快速开始

本地开发运行：

```bash
go install github.com/magefile/mage@v1.17.2
mage start --dev
```

启动完成后访问：

- Web：http://127.0.0.1:5173
- API：http://127.0.0.1:8080
- 就绪检查：http://127.0.0.1:8080/api/v1/health/ready

`mage start --dev` 会依次完成以下操作：

1. 读取根目录 `.env`；不存在时根据 `.env.example` 创建，并生成 `EDO_SECRETS_KEY`。
2. 停止同一 Compose 项目中遗留的 API、Web 和 Docker-in-Docker 容器。
3. 使用 Docker Compose 启动 Redis 和 NATS JetStream。
4. 执行数据库迁移。
5. 使用 `go run` 在当前终端启动后端，后端就绪后再启动 Vite 开发服务器。

`mage start --dev` 会保持前台运行，后端和 Vite 日志直接输出到当前终端；后端结构化日志同时写入可滚动的 `logs/edo.log`。按 `Ctrl+C` 可以停止，也可以在另一个终端执行 `mage stop` 或 `mage kill`。

普通 `mage start` 会在服务就绪后返回并保持后台运行；后端日志追加到 `logs/backend.log`，单独使用 `mage start --web` 时 Web 日志追加到 `logs/web.log`。

停止前后端：

```bash
mage stop  # 发送终止信号并等待安全退出
mage kill  # 进程无响应时强制结束
```

这两个命令只处理当前项目由 Mage 记录的本地前后端，以及 `edo-dev` Compose 项目的 `api/web`；不会按进程名误杀其他 Go/Node 进程，也不会停止 Redis、NATS 或删除数据。

需要同时停止开发依赖时执行：

```bash
docker compose --env-file .env -f deploy/compose.dev.yml stop redis nats
```

## 环境要求

EDO 仅支持 Linux 和 macOS。

- Go 1.26.5 或同系列更新的安全补丁版本
- Node.js 24
- Docker CLI、Docker Compose v2 和 Docker Buildx
- Mage 1.17.2

macOS 推荐使用 Docker Desktop。Linux 需要安装 Docker Engine，并确保当前用户可以访问 Docker daemon。

确认环境：

```bash
go version
node --version
npm --version
docker version
docker compose version
docker buildx version
mage -version
```

## 配置 `.env`

所有启动方式都从项目根目录读取 `.env`。通过 Mage 启动时，如果文件不存在，会自动复制 `.env.example` 并生成 32 字节随机密钥。

也可以手动创建：

```bash
cp .env.example .env
openssl rand -base64 32
```

将生成结果填写到：

```dotenv
EDO_SECRETS_KEY=生成的Base64密钥
```

常用配置：

```dotenv
EDO_SERVER_ADDRESS=:8080
EDO_DATABASE_DRIVER=sqlite
EDO_DATABASE_DSN=data/edo.db
EDO_REDIS_URL=redis://127.0.0.1:6379/0
EDO_NATS_URL=nats://127.0.0.1:4222
EDO_HTTP_PORT=8080
EDO_WEB_PORT=5173
```

注意：

- 已导出的同名进程环境变量优先于 `.env`。
- `EDO_SECRETS_KEY` 用于加密凭据。实例开始使用后必须固定保存并备份，不能静默更换。
- 宿主机连接使用 `EDO_DATABASE_*`、`EDO_REDIS_URL` 和 `EDO_NATS_URL`。
- Compose 容器连接使用 `EDO_COMPOSE_*`，不要把宿主机的 `127.0.0.1` 地址直接传给容器。
- `.env` 包含敏感信息，已经被 Git 忽略，不要提交。

完整配置和说明见 [.env.example](.env.example)。

## Mage 启动命令

### 命令速查

| 命令 | 用途 | Web 地址 | API 地址 |
| --- | --- | --- | --- |
| `mage start --dev` | 推荐的本地开发模式；启动 Redis、NATS 和迁移，在前台运行后端与 Vite | `:5173` | `:8080` |
| `mage start` | 构建内嵌 Web 的 `bin/edo`，迁移后在后台启动 | `:8080` | `:8080` |
| `mage start --docker` | 构建并在容器中后台启动完整开发环境 | `:5173` | `:8080` |
| `mage start --dev --server` | 在前台仅启动开发后端，同时启动 Redis、NATS 并迁移 | 无 | `:8080` |
| `mage start --dev --web` | 在前台仅启动 Vite，要求后端已经单独运行 | `:5173` | 不启动 |
| `mage start --server` | 构建并启动不含 Web 的后端，启动前迁移数据库 | 无 | `:8080` |
| `mage start --web` | 构建 Web 后使用 Vite Preview 运行，要求后端已经单独运行 | `:5173` | 不启动 |
| `mage start --docker --server` | 仅启动 Compose 后端及其依赖 | 无 | `:8080` |
| `mage start --docker --web` | 仅启动 Compose Web，不自动启动依赖 | `:5173` | 不启动 |
| `mage migrate` | 只迁移 `.env` 指定的数据库 | 无 | 不启动 |

进程控制命令：

| 命令 | 用途 |
| --- | --- |
| `mage stop` | 向当前项目的本地前后端发送终止信号并等待安全退出；安全停止 Compose `api/web` |
| `mage kill` | 强制结束当前项目的本地前后端进程组；以零秒宽限强制停止 Compose `api/web` |
| `mage status` | 显示本地前后端的运行模式、PID、就绪状态和日志路径，并显示全部 Compose 服务状态 |
| `mage log --tail 100` | 显示并持续监听 `logs/backend.log`、`logs/web.log` 和 `logs/edo.log` |

查看状态或监听后台日志：

```bash
mage status
mage log --tail 100
```

`mage log` 默认从最后 100 行开始；支持 `--tail=100` 和 `-n 100`。使用 `--tail 0` 时不显示历史内容，只监听新增日志。按 `Ctrl+C` 停止监听。开发模式中的后端日志会同时显示在终端并写入可滚动文件；Vite 日志仅显示在终端。

### 运行日志滚动

后端使用开源 `github.com/libtnb/logrotate` 写入结构化日志。活动文件默认是 `logs/edo.log`：

- 每天本地时间零点后的第一条日志会触发切分，历史文件名包含年、月、日和时间。
- 活动文件达到 100 MiB 时立即切分。
- 已切分且满 3 天的 `.log` 会转为 `.log.gz`。
- 在“设置 → 日志设置”中可修改文件开关、目录、单文件大小和压缩天数，保存后立即生效，无需重启。

首次启动值也可通过 `.env` 设置：

```dotenv
EDO_LOG_FILE_ENABLED=true
EDO_LOG_DIRECTORY=logs
EDO_LOG_MAX_FILE_SIZE_MB=100
EDO_LOG_COMPRESS_AFTER_DAYS=3
```

管理员在设置页保存后，数据库中的值优先于上述启动默认值。Compose 模式的日志目录固定为持久化数据卷内的 `/app/data/logs`。

不指定 `--server` 或 `--web` 时，默认同时启动两者；两个参数同时提供时也是同时启动。`--dev` 与 `--docker` 不能一起使用。

不带 `--dev` 或 `--docker` 的后端启动方式不会自动启动 Redis 和 NATS，必须确保 `.env` 中配置的依赖已经可用。

查看帮助：

```bash
mage help
mage start --help
mage log --help
mage -l
```

### 本机开发模式

```bash
mage start --dev
```

此模式适合日常开发：

- Redis、NATS 在容器中运行。
- Go 后端在宿主机通过 `go run ./cmd/edo server` 前台运行。
- Vue 前端在宿主机通过 Vite 前台运行并支持热更新。
- `mage start --dev` 会占用当前终端，后端和 Vite 日志直接显示在终端；按 `Ctrl+C` 停止。
- 也可以在另一个终端执行 `mage stop` 安全停止，或执行 `mage kill` 强制结束。
- Dockerfile 流水线直接使用宿主机 Docker/Buildx，不使用 Docker-in-Docker。

只调试后端：

```bash
mage start --dev --server
```

只调试前端：

```bash
mage start --dev --web
```

仅启动前端时不会启动依赖、迁移数据库或启动 API，需要确保后端已在 `127.0.0.1:8080` 运行。

### 本机单文件模式

```bash
mage start
```

该命令会：

1. 安装或校验前端依赖。
2. 构建 Vue 生产资源。
3. 将 Web 资源嵌入 `bin/edo`。
4. 执行数据库迁移。
5. 在后台启动 `bin/edo server`，确认就绪后返回。

Web 与 API 都通过 `http://127.0.0.1:8080` 提供。Mage 启动器输出追加到 `logs/backend.log`，后端结构化日志写入可滚动的 `logs/edo.log`。此模式不会自动启动 Redis 和 NATS，可以预先启动依赖：

```bash
docker compose --env-file .env -f deploy/compose.dev.yml up -d --wait redis nats
mage start
```

构建生成的 `bin/edo` 可以独立运行，但仍需要 `.env`、数据库、Redis 和 NATS。

### 容器开发模式

```bash
mage start --docker
```

此命令使用 `deploy/compose.dev.yml` 构建并后台启动迁移任务、API、Vite、Redis、NATS 和隔离的 Docker-in-Docker 构建器。

查看状态和日志：

```bash
docker compose --env-file .env -f deploy/compose.dev.yml ps
docker compose --env-file .env -f deploy/compose.dev.yml logs -f api web
```

停止：

```bash
docker compose --env-file .env -f deploy/compose.dev.yml down
```

不要附加 `-v`，否则会删除 Docker-in-Docker 缓存和前端依赖卷。业务数据库、Redis 和 NATS 数据位于仓库的 `data/`，也不要直接删除该目录。

### 单机 Compose 模式

根目录 `docker-compose.yml` 用于单机或试用部署。它构建带内嵌 Web 的 EDO 镜像，Web 与 API 共用 8080 端口：

```bash
docker compose --env-file .env up -d --build
docker compose --env-file .env ps
docker compose --env-file .env logs -f api
```

停止服务但保留数据：

```bash
docker compose --env-file .env down
```

直接执行 Docker Compose 不会自动创建 `.env` 或生成密钥，必须提前准备。

## `edo` 命令

以下示例使用 `mage start` 生成的 `bin/edo`。如果二进制已复制到其他目录，将路径替换为实际位置。

### 启动服务

```bash
./bin/edo server
```

省略子命令时也默认启动服务：

```bash
./bin/edo
```

服务启动前不会自动迁移数据库。首次运行或升级版本后，应先执行 `migrate`。

### 迁移数据库

```bash
./bin/edo migrate
```

该命令只连接并迁移 `.env` 指定的数据库，不启动 HTTP、Redis、NATS 消费者或调度器。

### 查看版本

```bash
./bin/edo version
```

### 创建管理员

```bash
./bin/edo admin create --username admin --nickname 管理员
```

命令会在终端中要求输入并确认密码。非交互环境可以使用临时环境变量：

```bash
read -s EDO_ADMIN_PASSWORD
export EDO_ADMIN_PASSWORD
./bin/edo admin create --username admin --nickname 管理员
unset EDO_ADMIN_PASSWORD
```

`EDO_ADMIN_PASSWORD` 仅用于本次命令，不要写入 `.env`、Shell 历史或日志。

### 重置用户密码

```bash
./bin/edo admin reset-password --username admin
```

密码输入方式与创建管理员相同。重置成功后会启用该账户。

### 导入旧数据

先使用只读来源账户执行预检：

```bash
EDO_LEGACY_DATABASE_DRIVER=mysql \
EDO_LEGACY_DATABASE_DSN='readonly:password@tcp(db:3306)/legacy?charset=utf8mb4&parseTime=True&loc=UTC' \
./bin/edo legacy-import --dry-run
```

确认预检结果后，去掉 `--dry-run` 才会写入 EDO 数据库。详细范围与限制见 [迁移说明](docs/migration.md)。

## 数据与初始账户

默认数据位置：

- SQLite：`data/edo.db`
- Redis：`data/redis/`
- NATS JetStream：`data/nats/`
- 构建制品：`data/artifacts/`
- 流水线仓库缓存与版本工作区：`data/repositories/<仓库地址Hash>/<Tag版本号或Commit>/`

首次启动且账户表为空时，系统会创建管理员：

- 用户名：`admin`
- 初始密码：`123456`

登录后应立即修改初始密码。已有账户的数据库不会重复创建或覆盖管理员。

## 构建与检查

安装前端依赖：

```bash
npm ci --prefix web
```

执行项目检查：

```bash
go test ./...
go vet ./...
npm run build --prefix web
```

构建不含内嵌 Web 的后端二进制：

```bash
go build -trimpath -o bin/edo ./cmd/edo
```

需要内嵌 Web 的单文件程序时使用 `mage start`；该命令会生成 `bin/edo` 并立即启动。

## 常见问题

### 提示 `EDO_SECRETS_KEY` 无效

密钥必须是 32 字节随机数据的 Base64 编码。可以重新生成新实例的密钥：

```bash
openssl rand -base64 32
```

已有加密数据的实例不能直接更换密钥，否则已保存的凭据将无法解密。

### 8080 或 5173 端口被占用

修改 `.env` 中的 `EDO_HTTP_PORT` 或 `EDO_WEB_PORT`。宿主机直接运行后端时，监听地址由 `EDO_SERVER_ADDRESS` 控制。

### Docker 流水线不可用

确认 Docker daemon、Buildx 和 Compose 均可用：

```bash
docker version
docker buildx version
docker compose version
```

本机开发模式使用宿主机 Docker；Compose 模式使用隔离的 Docker-in-Docker。

## 更多文档

- [部署说明](docs/deployment.md)
- [数据库迁移](docs/database-migration.md)
- [旧数据迁移](docs/migration.md)
- [架构与重构说明](docs/refactor.md)
- [第三方依赖决策](docs/dependencies.md)

## 许可

本项目许可条款见 [LICENSE](LICENSE)。
