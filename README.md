# EDO

EDO（EasyDevOps）是一个简单、轻量的自动发布工具，可以装在自己的电脑或公司内网服务器上，适合中小团队自己部署和使用。

把代码仓库接进来，选好分支，配好测试、打包和部署步骤。以后提交代码，EDO 就会按这些步骤自动执行，每一步的进度、日志和结果都能在页面上看。流水线可以从已有方案开始，再按自己的需要调整。

默认采用 Pull 模式：EDO 按设置的间隔主动检查仓库里的分支、Tag 和 PR/MR，发现符合流水线规则的变化就自动执行。仓库不需要反过来连接 EDO，适合放在内网运行；EDO 需要能访问代码仓库和发布目标。也可以接入 Webhook，更快收到仓库变化。

EDO 的前端和后端分别构建、运行：Web 提供网页，Go API 处理接口、WebSocket 和后台任务。Docker Compose 默认使用 PostgreSQL，并一起管理 Web、API、数据库、Redis、NATS 和隔离的 Docker 构建器，不需要分别手工安装这些服务。宿主机开发和二进制运行仍默认使用 SQLite。

应用可以直接发布到 EDO 的本地 Docker 环境，也可以发布到远程主机或 Kubernetes 集群。Docker、Docker Compose 和 Kubernetes 都从应用开始，选择流水线和部署方案，再填好具体位置，统一按「应用 → 流水线 → 部署方案 → 运行资源」发布。

在 Kubernetes 集群页面创建集群时，可选择“原生 K3s”或“k3d”。默认原生 K3s 使用官方安装器，在 Linux 主机上运行系统服务；k3d 在一台已有 Docker 的主机中运行隔离的 K3s 容器，适合测试，不代表多台物理机的高可用集群。原生模式支持默认或自定义参数，也可以为已有 K3s 集群添加 Master 或 Worker；Master 逐台加入经过验证的嵌入式 etcd 集群，需显式提供 Server Token，不会自动转换 SQLite。操作前先做独立只读“安装前检测”，不满足时不能生成计划；核对报告后再生成计划并确认安装。任务在后台执行，进度可再次打开查看，详见[创建 K3s 集群与添加节点](docs/k3s-provisioning.md)。

项目角色可按集群、命名空间和操作范围映射到 Kubernetes 原生 RBAC。映射默认关闭，显式配置并同步成功后生效；用户请求使用受控身份，授权变更同时影响缓存和长连接。详见[集群 RBAC 授权](docs/kubernetes-rbac.md)。

## Docker Compose 一键启动

支持 Linux 和 macOS。电脑或服务器上只需准备 Git、Docker Engine（macOS 使用 Docker Desktop）及 Compose v2/更新版本、Buildx 插件，确认当前用户可以运行 Docker。无需安装 Go、Node.js、PostgreSQL 或 OpenSSL。

```bash
git clone --branch main https://github.com/linabellbiu/edo.git
cd edo
cp .env.example .env
docker compose up -d
```

无需修改复制后的配置。第一次自动构建 Web/API 镜像，生成并保存随机加密密钥与数据库密码，初始化 PostgreSQL，完成迁移后启动服务。默认已配置国内镜像同步源及 Go/npm 源，首次需联网下载，请等待构建完成；对应版本、架构和覆盖方式见[镜像说明](docs/deployment.md#默认镜像源)。再次运行同一命令会复用已有镜像和数据；更新源码后使用 `docker compose up -d --build`。

浏览器打开 **`http://服务器IP:8080`**，本机安装打开 [http://localhost:8080](http://localhost:8080)。默认账户 **`admin`**，初始密码 **`123456`**；登录后可通过头像菜单“修改密码”设置新密码，不限制最小长度，不能为空。已有账户不会被覆盖。

### 检查启动结果

```bash
docker compose ps -a
curl --fail http://localhost:8080/healthz
curl --fail http://localhost:8080/api/v1/health/ready
```

`bootstrap` 和 `migrate` 是一次性任务，`Exited (0)` 属于正常结果。其余服务应保持健康；Web 检查与 API 检查均通过后，在浏览器实际打开登录页。初始化或迁移失败会阻止后续启动，使用 `docker compose logs bootstrap migrate api web` 查看安全日志。

### 默认启动了什么

Web 单独提供网页，Go API 一个进程同时处理接口、WebSocket 和后台任务；另有 PostgreSQL 17、Redis、NATS JetStream 和隔离的 Docker 构建器。数据库不映射宿主机端口。构建器需要 `privileged`，不挂载宿主机 Docker Socket，并通过 mTLS 与 API 通信。

默认只有 Web 对外监听 `8080`。云服务器需在安全组放行对应 TCP 端口。修改 `.env` 的 `EDO_HTTP_PORT` 可换端口；仅通过本机反向代理访问时，将 `EDO_HTTP_BIND` 改为 `127.0.0.1`。使用 HTTPS 反向代理时，将 `EDO_AUTH_COOKIE_SECURE` 改为 `true`，并保留 WebSocket Upgrade 和长连接。实际客户端地址的代理信任配置见[部署说明](docs/deployment.md)。

### 数据、密钥与备份

PostgreSQL 数据保存在 Docker 持久卷 `edo_edo-postgres-data`（默认项目名）中，重建容器不会丢失。仓库 `data/` 保存随机密钥和数据库连接文件（`data/bootstrap/`），以及 Redis、NATS、仓库工作区、文件制品和日志。初始化会自动处理顶层目录权限，无需手工 `mkdir` 或 `chown`；不会递归修改已有数据目录。

**保留并备份 PostgreSQL、整个 `data/` 和 `.env`。** 加密密钥不是每次启动重新生成的，丢失后无法解密原凭据。已初始化环境缺少密钥或数据库密码时会拒绝启动；恢复备份，不要删除配置目录让它重新生成。已有 `.env` 和数据时不要重新复制模板覆盖，也不会将已有 SQLite 自动切换到 PostgreSQL。运行中的 PostgreSQL 使用 `pg_dump` 备份，复制数据库卷及文件目录前应先停止全部写入，见[备份说明](docs/deployment.md#备份与恢复)。

### 日常管理与更新

```bash
# 状态与日志；Ctrl+C 只退出日志查看
docker compose ps -a
docker compose logs --tail 100 -f api web

# 停止容器，保留 data/ 和命名卷
docker compose down

# 再次启动
docker compose up -d

# 更新前先备份并确认没有正在执行的发布任务
git pull --ff-only origin main
git rev-parse --short HEAD
docker compose up -d --build
```

更新后再次检查健康接口和页面。不要添加 `down -v` 或直接清空 `data/`。构建器的命名卷还保存本地发布环境与镜像缓存，其应用数据需单独备份。

需要在终端重置管理员密码时执行以下命令，按隐藏输入提示操作：

```bash
docker compose exec api /app/docker-entrypoint.sh admin reset-password --username admin
```

自行使用 systemd 部署时，Web 与 API 仍需分别发布。[EDO 保护发布](tools/release-activate/README.md) 只负责 systemd Go API 的备份、切换及回滚。

## 本地启动

本地开发和编译需要 Go 1.26.10（与 [go.mod](go.mod) 一致）、Node.js 24、npm，以及上面的 Docker 工具。安装 Mage 并确认命令可用：

```bash
go install github.com/magefile/mage@v1.17.2
export PATH="$(go env GOPATH)/bin:$PATH"
go version
node --version
npm --version
mage -l
```

本地二进制、Mage 与开发 Compose 仍默认 SQLite，需手工配置固定加密密钥：

```bash
cp .env.example .env
chmod 600 .env
openssl rand -base64 32
```

把生成的完整值填入 `.env` 的 `EDO_SECRETS_KEY=`。已有配置时保留原密钥，不要覆盖；从正式 Compose 环境转为本地开发时请使用独立工作目录与数据库。Mage 会自动准备缺失的前端依赖。

如果已经通过正式 Compose 启动同目录的 EDO，先执行根目录的 `docker compose --env-file .env down`，再选择下面一种本地方式。它与开发 Compose 是两个不同项目，`mage stop` 不会停止正式 Compose。Linux 切换运行用户时也需核对数据目录的写入权限。

### 开发模式

```bash
mage start --dev
```

访问 [http://127.0.0.1:5173](http://127.0.0.1:5173)，API 地址为 `http://127.0.0.1:8080`。前后端都在当前终端运行，前端支持热更新；修改 Go 源码后需要重启，按 `Ctrl+C` 停止前后端。

这个命令先停止开发 Compose 中遗留的 API、Web 和构建器，再启动 Redis、NATS，迁移数据库并运行宿主机前后端。Redis、NATS 在 `Ctrl+C` 后继续保留。若 `.env` 不存在，或其中的密钥为空、无效，Mage 会在构建、准备依赖和迁移前报错。请先按上面的步骤复制模板并配置固定密钥；Mage 不会自动创建配置、补填或轮换已有密钥。

### 后台运行

先按上面的方式准备 `.env` 和密钥，再启动 Redis、NATS：

```bash
docker compose --env-file .env -f deploy/compose.dev.yml up -d --wait redis nats
mage start
```

`mage start` 分别构建 Go API 和前端，初始化数据库后在后台启动两个独立进程。访问 [http://127.0.0.1:5173](http://127.0.0.1:5173)，API 为 `http://127.0.0.1:8080`。本地预览服务转发 API 与 WebSocket；正式 Docker 部署使用独立 NGINX Web 容器。

本地 API 的就绪检查地址为 `http://127.0.0.1:8080/api/v1/health/ready`。模板中的宿主机 API 默认监听 `:8080`；只需本机访问时，可把 `.env` 的 `EDO_SERVER_ADDRESS` 改为 `127.0.0.1:8080`。当前 Mage 的本地进程探测固定使用 8080 和 5173；请保留这两个端口，`EDO_HTTP_PORT` / `EDO_WEB_PORT` 只修改 Compose 的端口映射。

### 完整开发容器

希望 Go 和 Node.js 也在容器内运行时，可使用：

```bash
mage start --docker
```

该方式使用 `deploy/compose.dev.yml`，访问 `http://127.0.0.1:5173`，API 映射为本机 8080。它启动的是开发环境，与根目录 Compose 的 NGINX 正式入口不同；Go 镜像与开发 Web 容器分别构建。

### Mage 启动参数

在项目根目录执行：

```text
mage start [--dev | --docker] [--server | --web]
```

| 参数 | 用途 |
| --- | --- |
| 不带参数 | 分别构建前后端，迁移数据库，然后在后台启动两个独立进程 |
| `--dev` | 开发模式，在当前终端运行；启动后端时会先启动 Redis、NATS 并迁移数据库 |
| `--docker` | 使用 `deploy/compose.dev.yml` 构建并在后台启动容器 |
| `--server` | 只启动后端；Docker 模式会同时启动后端依赖 |
| `--web` | 只启动前端，不启动后端及其依赖 |
| `--help` / `-h` | 查看启动参数说明 |

`--dev` 和 `--docker` 不能同时使用。不指定 `--server` 或 `--web` 时默认启动前后端，同时指定两者也会启动前后端。

常见组合：

| 命令 | 用途 |
| --- | --- |
| `mage start --server` | 只构建后端、迁移数据库并在后台启动，不构建前端 |
| `mage start --web` | 构建前端并在后台启动预览服务，默认端口为 5173 |
| `mage start --dev` | 在当前终端运行后端和 Vite，前端支持热更新 |
| `mage start --dev --server` | 开发模式，只在当前终端运行后端 |
| `mage start --dev --web` | 开发模式，只在当前终端运行 Vite |
| `mage start --docker` | 在容器中启动完整开发环境，网页默认端口为 5173 |
| `mage start --docker --server` | 在容器中启动后端、数据库迁移和后端依赖 |
| `mage start --docker --web` | 只启动前端容器，不自动启动后端及其依赖 |

`mage start` 和 `mage start --server` 需要事先启动 Redis、NATS，可使用“后台运行”中的依赖启动命令。只启动前端时，需要另行启动后端才能使用登录等 API 功能。

### 进程与日志管理

| 命令 | 用途 |
| --- | --- |
| `mage status` | 查看本地服务的 PID、前后台模式、就绪状态及 Compose 服务状态 |
| `mage log --tail 100` | 从本地服务日志最后 100 行开始持续监听，默认行数为 100 |
| `mage stop` | 安全停止 Mage 管理的本地前后端和开发 Compose 的 API/Web，保留依赖容器和数据 |
| `mage kill` | 强制结束上述前后端，用于安全停止超时等情况 |
| `mage migrate` | 迁移 `.env` 指定的数据库，不启动其他服务 |
| `mage help` | 查看常用命令 |
| `mage -l` | 查看全部 Mage 任务 |

本地进程记录保存在 `data/run/`，后台日志保存在 `logs/`。重复启动已运行的本地服务会报错，需要先执行 `mage stop`，再启动。开发模式也可以按 `Ctrl+C` 停止，或在另一终端执行 `mage stop`。

`mage log` 监听本地日志文件，需要系统安装 `tail`。通过 `mage start --docker` 启动的容器请使用 Compose 查看日志：

```bash
docker compose --env-file .env -f deploy/compose.dev.yml logs --tail 100 -f api web
```

需要停止全部开发容器时执行下列命令，仍保留数据和命名卷：

```bash
docker compose --env-file .env -f deploy/compose.dev.yml down
```

两个 Compose 文件默认分别使用 `edo`、`edo-dev` 项目名。管理时始终使用与启动时相同的文件、环境文件及项目名；使用自定义 `-p` 时，后续命令也要带上相同的 `-p`。

### 常见启动问题

| 现象 | 检查方式 |
| --- | --- |
| 提示密钥缺失或无效 | 正式 Compose 检查 `bootstrap` 日志及 `data/bootstrap/`；本地开发检查 `.env` 的密钥；已有数据恢复原密钥 |
| 8080、5173、6379 或 4222 被占用 | 先确认现有进程；Compose 改对应映射端口，宿主机 Redis/NATS 的连接 URL 也需同步修改 |
| 数据库或日志目录 `permission denied` | Linux 检查 API 用户 `10001:10001` 对挂载目录的写权限；不要改为全目录 `777` |
| `migrate` 失败，API/Web 未启动 | 查看 `docker compose --env-file .env logs migrate`，修正数据库连接或权限后再启动 |
| 页面能打开但登录失败、API 未就绪 | 查看 API 日志及 `/api/v1/health/ready`，检查数据库、Redis 和启用 JetStream 的 NATS |
| Docker 构建器不健康 | 查看对应 Compose 的 `docker-builder` 日志；容器部署使用隔离 DinD，宿主机运行使用当前 Docker daemon |
| 本地提示 Redis/NATS 连接失败 | `mage start` 不会启动依赖，先执行“后台运行”中的依赖启动命令；容器内不要使用宿主机 `127.0.0.1` 连接 URL |
| 服务器 IP 无法直接打开网页 | 检查 Web 健康、`EDO_HTTP_BIND`、`EDO_HTTP_PORT` 及服务器网络入口是否允许该端口 |

## 开始发布

1. 创建应用，关联代码仓库和 Git 凭据。
2. 创建流水线，选择要监听的分支、Tag 或 Pull Request。
3. 配置测试、构建和部署任务，选择部署方案并填写发布位置。
4. 启用流水线，提交代码或手动执行，在运行记录中查看进度和日志。

需要时可以添加变量与密钥、并行检查、测试和覆盖率报告、SQL 检查、安全扫描、数据库变更和环境审核。检查失败会拦住后续任务，结果留在运行记录中。配置方法见[流水线检查与发布保护](docs/delivery-checks.md)。

独立的手动 SQL 变更在“基础设施 → SQL 审计”：提交后自动静态检查，由指定审核人审批，再人工执行或跳过。每项绑定本次发布与目标数据库，成功后不能修改、删除；目前不自动联动流水线。使用方法与权限说明见[手动 SQL 审计](docs/sql-audit.md)。

## Docker 交互终端的可选前置条件

容器列表、日志、文件与发布不需要额外终端工具。要在 EDO 中打开 Docker 交互终端，目标 Linux amd64/arm64 镜像需显式预装本项目的 `edo-terminal-session` v1 工具到 `/usr/local/libexec/edo-terminal-session`（只读、可执行），创建容器时使用 Docker 官方 `--init` 或 Compose 的 `init: true`，保留默认独立 PID 命名空间。EDO 不会自动复制工具、重建已有容器、改启动参数、提权或退回旧的直接 TTY 模式。

按目标架构编译工具，例如：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o edo-terminal-session ./cmd/edo-terminal-session
```

arm64 镜像将 `GOARCH` 改为 `arm64`，再在自己的应用 Dockerfile 中显式加入：

```dockerfile
COPY --chmod=0555 edo-terminal-session /usr/local/libexec/edo-terminal-session
```

工具只在会话期间运行，内部是真实 PTY；关闭、超时或权限失效只回收该会话创建的进程，不向终端灌入 `exit`，不按任意 PID 杀进程，也不停止业务容器。原容器用户、工作目录及环境保留；只有未设置 `TERM` 时为 Shell 补 `xterm-256color`。该模式启用 `no_new_privs` 并跟踪自身子代，不能通过 `sudo`/setuid 提权，不支持 `gdb`/`strace` 等调试类会话和兼容 ABI；目标内核及 LSM/seccomp 不支持时会明确拒绝，不自动降低安全策略。

这不是恶意程序沙箱。同 UID 恶意进程冻结监控进程、宿主机冻结或不可中断内核状态，都可能阻止及时回收；未收到完整回收确认或 Docker Exec 仍未停止时，EDO 会记录失败，不将断线当成释放成功。工具内部 Go 预算不是 RSS 上限，不作未经实测的内存承诺。实现和权限边界见[终端工作区](docs/terminal-workspace.md#docker-受控会话)。

## 配置与数据

首次启动前选择数据库：正式 Docker Compose 默认自带 PostgreSQL，宿主机及开发 Compose 默认 SQLite，也支持外部 PostgreSQL 或 MySQL。连接地址、端口和目录在 [.env.example](.env.example) 中都有说明，实际配置保存在 `.env`。宿主机与容器使用的地址分开填写：

| 配置 | 宿主机 / Mage | Compose 容器 |
| --- | --- | --- |
| 数据库类型 | `EDO_DATABASE_DRIVER` | `EDO_COMPOSE_DATABASE_DRIVER` |
| 数据库连接 | `EDO_DATABASE_DSN` | `EDO_COMPOSE_DATABASE_DSN` |
| Redis | `EDO_REDIS_URL` | `EDO_COMPOSE_REDIS_URL` |
| NATS JetStream | `EDO_NATS_URL` | `EDO_COMPOSE_NATS_URL` |

正式 Compose 的 `EDO_COMPOSE_DATABASE_DSN` 留空时，从 `data/bootstrap/postgres-dsn` 读取自动生成的 PostgreSQL 连接。开发 Compose 使用 `EDO_DEV_COMPOSE_DATABASE_DRIVER` 与 `EDO_DEV_COMPOSE_DATABASE_DSN` 独立覆盖，默认 `/app/data/edo.db`；宿主机 SQLite 默认为 `data/edo.db`。外部 PostgreSQL/MySQL 需先创建 EDO 数据库及账户，再填写容器实际可访问的地址；容器中的 `127.0.0.1` 指容器自身。DSN 示例见[部署说明](docs/deployment.md#数据库-dsn-示例)。

默认 PostgreSQL 保存在 Docker 命名卷中；`data/` 保存选择的 SQLite、初始化密钥、Redis、NATS、仓库文件和构建制品。本地启动器日志在 `logs/backend.log`、`logs/web.log`，后端结构化日志为 `logs/edo.log`。Compose 中的后端日志保存在 `data/logs/`，Web 日志通过 Compose 查看。

SQLite 部署在停止写入后一起备份 `data/` 和 `.env`；使用外部数据库时，还需使用对应数据库的备份工具保存 EDO 数据库。`EDO_SECRETS_KEY` 用于解密凭据，请固定保存；更换后将无法读取此前保存的凭据。`.env` 含敏感信息，请勿提交到仓库。

Compose 的 Docker 构建器另有命名卷，保存其镜像、构建缓存及本地发布运行环境；通过它部署的应用数据还需单独备份。`docker compose down` 不会删除这些卷，不要为重启 EDO 添加 `-v` 或直接清空数据目录。

## 界面预览

**登录：使用账号登录，或选择管理员已启用的方式。**

![登录页](docs/images/login.png)

**登录方式：在系统设置中配置并启用飞书、钉钉、邮箱验证码等方式。**

![登录方式选择](docs/images/login-methods.jpg)

**应用：查看代码更新、最近运行和部署实例。**

![应用与部署状态](docs/images/applications.jpg)

**流水线：把构建和部署步骤连起来。**

![流水线编排](docs/images/pipeline.jpg)

**Kubernetes：按集群和命名空间查看工作负载。**

![Kubernetes 工作负载](docs/images/kubernetes.jpg)

**监控：查看资源用量、任务和消息队列。**

![系统监控](docs/images/monitoring.jpg)

## 许可

[AGPL-3.0](LICENSE)
