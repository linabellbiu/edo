# EDO 本地开发

正式部署见[部署说明](deployment.md)，项目介绍见 [README](../README.md)。

本地开发和编译需要 Go 1.26.10（与 [go.mod](../go.mod) 一致）、Node.js 24、npm，以及上面的 Docker 工具。安装 Mage 并确认命令可用：

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

## 开发模式

```bash
mage start --dev
```

访问 [http://127.0.0.1:5173](http://127.0.0.1:5173)，API 地址为 `http://127.0.0.1:8080`。前后端都在当前终端运行，前端支持热更新；修改 Go 源码后需要重启，按 `Ctrl+C` 停止前后端。

这个命令先停止开发 Compose 中遗留的 API、Web 和构建器，再启动 Redis、NATS，迁移数据库并运行宿主机前后端。Redis、NATS 在 `Ctrl+C` 后继续保留。若 `.env` 不存在，或其中的密钥为空、无效，Mage 会在构建、准备依赖和迁移前报错。请先按上面的步骤复制模板并配置固定密钥；Mage 不会自动创建配置、补填或轮换已有密钥。

## 后台运行

先按上面的方式准备 `.env` 和密钥，再启动 Redis、NATS：

```bash
docker compose --env-file .env -f deploy/compose.dev.yml up -d --wait redis nats
mage start
```

`mage start` 分别构建 Go API 和前端，初始化数据库后在后台启动两个独立进程。访问 [http://127.0.0.1:5173](http://127.0.0.1:5173)，API 为 `http://127.0.0.1:8080`。本地预览服务转发 API 与 WebSocket；正式 Docker 部署使用独立 NGINX Web 容器。

本地 API 的就绪检查地址为 `http://127.0.0.1:8080/api/v1/health/ready`。模板中的宿主机 API 默认监听 `:8080`；只需本机访问时，可把 `.env` 的 `EDO_SERVER_ADDRESS` 改为 `127.0.0.1:8080`。当前 Mage 的本地进程探测固定使用 8080 和 5173；请保留这两个端口，`EDO_HTTP_PORT` / `EDO_WEB_PORT` 只修改 Compose 的端口映射。

## 完整开发容器

希望 Go 和 Node.js 也在容器内运行时，可使用：

```bash
mage start --docker
```

该方式使用 `deploy/compose.dev.yml`，访问 `http://127.0.0.1:5173`，API 映射为本机 8080。它启动的是开发环境，与根目录 Compose 的 NGINX 正式入口不同；Go 镜像与开发 Web 容器分别构建。

## Mage 启动参数

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

## 进程与日志管理

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

## 常见启动问题

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

