# EDO

EDO（EasyDevOps）是一个简单、轻量的自动发布工具，可以装在自己的电脑或公司内网服务器上，适合中小团队自己部署和使用。

把代码仓库接进来，选好分支，配好测试、打包和部署步骤。以后提交代码，EDO 就会按这些步骤自动执行，每一步的进度、日志和结果都能在页面上看。流水线可以从已有方案开始，再按自己的需要调整。

默认采用 Pull 模式：EDO 按设置的间隔主动检查仓库里的分支、Tag 和 PR/MR，发现符合流水线规则的变化就自动执行。仓库不需要反过来连接 EDO，适合放在内网运行；EDO 需要能访问代码仓库和发布目标。也可以接入 Webhook，更快收到仓库变化。

EDO 的前端和后端分别构建、运行：Web 提供网页，Go API 处理接口、WebSocket 和后台任务。默认用 SQLite 保存数据，数据库就是本地的一个文件。Docker Compose 会一起管理 Web、API、Redis、NATS 和隔离的 Docker 构建器，不需要分别手工启动。

应用可以直接发布到 EDO 的本地 Docker 环境，也可以发布到远程主机或 Kubernetes 集群。Docker、Docker Compose 和 Kubernetes 都从应用开始，选择流水线和部署方案，再填好具体位置，统一按「应用 → 流水线 → 部署方案 → 运行资源」发布。

在 Kubernetes 集群页面创建集群时，可选择“原生 K3s”或“k3d”。默认原生 K3s 使用官方安装器，在 Linux 主机上运行系统服务；k3d 在一台已有 Docker 的主机中运行隔离的 K3s 容器，适合测试，不代表多台物理机的高可用集群。原生模式支持默认或自定义参数，也可以为已有 K3s 集群添加 Master 或 Worker；Master 逐台加入经过验证的嵌入式 etcd 集群，需显式提供 Server Token，不会自动转换 SQLite。操作前先做独立只读“安装前检测”，不满足时不能生成计划；核对报告后再生成计划并确认安装。任务在后台执行，进度可再次打开查看，详见[创建 K3s 集群与添加节点](docs/k3s-provisioning.md)。

项目角色可按集群、命名空间和操作范围映射到 Kubernetes 原生 RBAC。映射默认关闭，显式配置并同步成功后生效；用户请求使用受控身份，授权变更同时影响缓存和长连接。详见[集群 RBAC 授权](docs/kubernetes-rbac.md)。

## 安装前准备

EDO 支持 Linux 和 macOS，可运行在本地电脑或内网服务器上。先选择一种启动方式，不要在同一个工作目录和数据目录中同时启动下面几种环境。

| 方式 | 适用场景 | 需要安装 | 默认网页入口 |
| --- | --- | --- | --- |
| Docker Compose | 安装使用、服务器部署 | Git、Docker、Compose v2、Buildx、OpenSSL | `http://127.0.0.1:8080` |
| `mage start --dev` | 修改源码、前端热更新 | 上述工具，以及 Go、Node.js、npm、Mage | `http://127.0.0.1:5173` |
| `mage start` | 源码构建后在宿主机后台运行 | 与开发模式相同，Redis/NATS 需先启动 | `http://127.0.0.1:5173` |

普通安装使用第一种即可，不需要在宿主机安装 Go、Node.js 或独立 Worker。Compose 启动两个应用容器：Web 提供页面，Go API 同时处理 HTTP、WebSocket 和后台任务；另有 Redis、NATS JetStream 和隔离的 Docker 构建器。默认数据库是 SQLite 文件，无需额外安装数据库服务。构建器使用需要 `privileged` 的 Docker-in-Docker 容器，API 不挂载宿主机 Docker Socket。

确认 Docker 已启动，当前用户可以执行以下命令：

```bash
git --version
docker version
docker compose version
docker buildx version
openssl version
```

首次构建需要访问 Go/npm 依赖源和容器镜像仓库；运行流水线时，EDO 还需能访问所用代码仓库、镜像仓库和发布目标。

### 获取源码与配置

```bash
git clone --branch main https://github.com/linabellbiu/edo.git
cd edo
cp .env.example .env
chmod 600 .env
openssl rand -base64 32
```

用编辑器打开 `.env`，把最后一条命令生成的完整内容填入 `EDO_SECRETS_KEY=` 后面。这个值用于加密仓库令牌、SSH 凭据和集群配置，必须固定保存。已有 `.env` 或数据库时继续使用原配置与密钥，不要重新复制模板覆盖。

默认配置即可使用 SQLite。需要 PostgreSQL 或 MySQL 时，在首次启动前按下文[配置与数据](#配置与数据)修改数据库连接；不提供运行后的跨数据库切换功能。

## Docker Compose 部署

以下命令均在仓库根目录执行，使用根目录的 `docker-compose.yml`。

### 1. 准备数据目录

```bash
mkdir -p data
```

Linux 首次安装时，API 和迁移容器以 `10001:10001` 运行，新建的空 `data/` 目录需要允许该用户写入：

```bash
sudo chown 10001:10001 data
```

已有数据目录先核对原归属和备份，不要递归修改 Redis、NATS 等子目录的权限。macOS 使用 Docker Desktop 时，确保仓库目录已允许 Docker 文件共享。

### 2. 校验并启动

```bash
docker compose --env-file .env config --quiet
docker compose --env-file .env up -d --build
docker compose --env-file .env ps -a
```

首次构建需要一些时间。`migrate` 是一次性数据库迁移，成功后显示 `Exited (0)` 属于正常情况；等待 `api`、`web` 及依赖服务健康后，打开 [http://127.0.0.1:8080](http://127.0.0.1:8080)。

确认页面与后端均已就绪：

```bash
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/api/v1/health/ready
```

前一个检查 Web，后一个检查 API 与数据库、Redis、NATS JetStream。还应在浏览器实际打开登录页；仅 API 返回正常不能代替页面检查。

### 3. 首次登录

账户库为空时，服务自动创建管理员 `admin`，初始密码为 `123456`。登录后通过右上角头像菜单的“修改密码”设置新密码，至少 12 位。已有账户的数据库不会补建管理员或重置现有密码。

需要在终端重置管理员密码时，可执行：

```bash
docker compose --env-file .env exec api /app/edo admin reset-password --username admin
```

命令会隐藏输入并要求确认新密码，无需把密码写进命令行。

### 4. 服务器访问与端口

正式 Compose 只将 Web 映射到宿主机 `127.0.0.1:8080`。NGINX 在容器网络内转发 `/api/`、WebSocket 和流式输出；API、Redis、NATS 和构建器不直接映射宿主机端口。因此，远程电脑不能直接访问服务器 IP 的 8080 端口。

临时从自己的电脑访问服务器，可在自己的电脑执行，替换登录用户和服务器地址：

```bash
ssh -L 18080:127.0.0.1:8080 deploy@your-server
```

保持该 SSH 连接，访问自己电脑的 `http://127.0.0.1:18080`。长期访问使用 HTTPS 反向代理，代理到服务器本地 Web 入口，并支持 WebSocket Upgrade 和长连接。HTTPS 部署还需将 Compose 的 `EDO_AUTH_COOKIE_SECURE` 环境项改为 `"true"`；当前 Compose 文件显式设置该值，仅修改 `.env` 同名项不会覆盖它。代理信任配置见[部署说明](docs/deployment.md)。

端口占用时，修改 `.env` 的 `EDO_HTTP_PORT` 后重新执行启动命令。例如设为 `18080`，正式网页入口变为 `http://127.0.0.1:18080`。`EDO_WEB_PORT` 只调整开发 Compose 的 Vite 端口，不改变正式 Web 入口。

### 日常管理与更新

```bash
# 状态与最近日志；Ctrl+C 只退出日志查看
docker compose --env-file .env ps -a
docker compose --env-file .env logs --tail 100 -f api web migrate

# 停止容器，保留 data/ 和命名卷
docker compose --env-file .env down

# 再次启动
docker compose --env-file .env up -d
```

更新前备份数据库和 `.env`，确认当前没有正在执行的发布任务。在服务器拉取已推送的 `main`，同时重建 Web 和 API：

```bash
git pull --ff-only origin main
git rev-parse --short HEAD
docker compose --env-file .env up -d --build
docker compose --env-file .env ps -a
curl --fail http://127.0.0.1:8080/api/v1/health/ready
```

使用自定义端口时同步替换检查地址，最后在浏览器复查页面。不要用重新复制 `.env.example` 或删除 `data/` 解决升级问题。

自行使用 systemd 部署时，Web 与 API 仍需分别发布。[EDO 保护发布](tools/release-activate/README.md) 只负责 systemd Go API 的备份、切换及回滚，不能把 API 激活视为前后端全部上线。

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

先按[获取源码与配置](#获取源码与配置)准备 `.env`。Mage 会自动准备缺失的前端依赖；首次安装需要能访问 Go/npm 依赖源。

如果已经通过正式 Compose 启动同目录的 EDO，先执行根目录的 `docker compose --env-file .env down`，再选择下面一种本地方式。它与开发 Compose 是两个不同项目，`mage stop` 不会停止正式 Compose。Linux 切换运行用户时也需核对数据目录的写入权限。

### 开发模式

```bash
mage start --dev
```

访问 [http://127.0.0.1:5173](http://127.0.0.1:5173)，API 地址为 `http://127.0.0.1:8080`。前后端都在当前终端运行，前端支持热更新；修改 Go 源码后需要重启，按 `Ctrl+C` 停止前后端。

这个命令先停止开发 Compose 中遗留的 API、Web 和构建器，再启动 Redis、NATS，迁移数据库并运行宿主机前后端。Redis、NATS 在 `Ctrl+C` 后继续保留。若 `.env` 不存在，Mage 会按模板创建权限为 `600` 的文件并生成固定密钥；已有文件的密钥为空或无效时会报错，不会自动补填或轮换。

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
| 提示 `EDO_SECRETS_KEY` 缺失或无效 | 检查仓库根目录 `.env`，需填写 32 字节随机值的 Base64；已有数据应恢复原密钥 |
| 8080、5173、6379 或 4222 被占用 | 先确认现有进程；Compose 改对应映射端口，宿主机 Redis/NATS 的连接 URL 也需同步修改 |
| 数据库或日志目录 `permission denied` | Linux 检查 API 用户 `10001:10001` 对挂载目录的写权限；不要改为全目录 `777` |
| `migrate` 失败，API/Web 未启动 | 查看 `docker compose --env-file .env logs migrate`，修正数据库连接或权限后再启动 |
| 页面能打开但登录失败、API 未就绪 | 查看 API 日志及 `/api/v1/health/ready`，检查数据库、Redis 和启用 JetStream 的 NATS |
| Docker 构建器不健康 | 查看对应 Compose 的 `docker-builder` 日志；容器部署使用隔离 DinD，宿主机运行使用当前 Docker daemon |
| 本地提示 Redis/NATS 连接失败 | `mage start` 不会启动依赖，先执行“后台运行”中的依赖启动命令；容器内不要使用宿主机 `127.0.0.1` 连接 URL |
| 服务器 IP 无法直接打开网页 | 正式入口默认仅监听服务器本机，使用 SSH 转发或 HTTPS 反向代理 |

## 开始发布

1. 创建应用，关联代码仓库和 Git 凭据。
2. 创建流水线，选择要监听的分支、Tag 或 Pull Request。
3. 配置测试、构建和部署任务，选择部署方案并填写发布位置。
4. 启用流水线，提交代码或手动执行，在运行记录中查看进度和日志。

需要时可以添加变量与密钥、并行检查、测试和覆盖率报告、SQL 检查、安全扫描、数据库变更和环境审核。检查失败会拦住后续任务，结果留在运行记录中。配置方法见[流水线检查与发布保护](docs/delivery-checks.md)。

独立的手动 SQL 变更在“基础设施 → SQL 审计”：提交后自动静态检查，由指定审核人审批，再人工执行或跳过。每项绑定本次发布与目标数据库，成功后不能修改、删除；目前不自动联动流水线。使用方法与权限说明见[手动 SQL 审计](docs/sql-audit.md)。

## 配置与数据

首次启动前选择数据库，默认 SQLite，也可以选择 PostgreSQL 或 MySQL。连接地址、端口和目录在 [.env.example](.env.example) 中都有说明，实际配置保存在 `.env`。宿主机与容器使用的地址分开填写：

| 配置 | 宿主机 / Mage | Compose 容器 |
| --- | --- | --- |
| 数据库类型 | `EDO_DATABASE_DRIVER` | `EDO_COMPOSE_DATABASE_DRIVER` |
| 数据库连接 | `EDO_DATABASE_DSN` | `EDO_COMPOSE_DATABASE_DSN` |
| Redis | `EDO_REDIS_URL` | `EDO_COMPOSE_REDIS_URL` |
| NATS JetStream | `EDO_NATS_URL` | `EDO_COMPOSE_NATS_URL` |

SQLite 默认分别为 `data/edo.db` 和容器内 `/app/data/edo.db`，通过挂载对应同一个文件。外部 PostgreSQL/MySQL 需先创建 EDO 数据库及账户，再填写容器实际可访问的地址；容器中的 `127.0.0.1` 指容器自身。DSN 示例见[部署说明](docs/deployment.md#数据库-dsn-示例)。

默认 EDO 数据保存在 `data/`，包括 SQLite、Redis、NATS、仓库文件和构建制品；本地启动器日志在 `logs/backend.log`、`logs/web.log`，后端结构化日志为 `logs/edo.log`。Compose 中的后端日志保存在 `data/logs/`，Web 日志通过 Compose 查看。

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
