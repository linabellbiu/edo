# EDO 部署说明

EDO 的 Web 与 Go API 分别构建和运行，API 一个进程同时处理 HTTP、WebSocket 和后台任务。正式部署推荐 Docker Compose；本地开发仍默认 SQLite，见 [README](../README.md#本地启动)。

## Docker Compose

宿主机准备 Git、Docker 和 Compose v2/更新版本、Buildx，当前账户可运行 Docker 后：

```bash
git clone --branch main https://github.com/linabellbiu/edo.git
cd edo
cp .env.example .env
docker compose up -d
```

复制后的配置无需修改。首次自动构建 Web/API，初始化 PostgreSQL 17、Redis、NATS JetStream 和隔离的 Docker 构建器，迁移成功且 API 健康后启动 Web。首次构建需要能访问官方镜像仓库、Go/npm 依赖源。浏览器访问 `http://服务器IP:8080`，本机使用 `http://localhost:8080`。

空账户库自动创建 `admin`，初始密码 `123456`。登录后可通过头像菜单修改密码，不限制最小长度，不能为空；创建与重置本地账户采用相同规则。已有账户不会被覆盖。

```bash
docker compose ps -a
docker compose logs --tail 100 bootstrap migrate api web
curl --fail http://localhost:8080/healthz
curl --fail http://localhost:8080/api/v1/health/ready
```

`bootstrap` 与 `migrate` 成功后显示 `Exited (0)` 正常，其他服务应保持健康。仍需实际打开登录页；API 正常不能代替 Web 验收。初始化失败不绕过，修复配置或恢复备份后再次执行启动命令。

### 初始化与默认值

- 数据库：官方镜像同步版 `postgres:17.11-trixie`，库及用户为 `edo`，随机密码通过官方 `POSTGRES_PASSWORD_FILE` 传入，端口仅在容器网络内可用。`TZ`、`PGTZ`、数据库 `TimeZone` 和 `log_timezone` 均为 `Asia/Shanghai`。
- 加密密钥：首次随机生成 32 字节 Base64，保存到 `data/bootstrap/secrets-key`；数据库密码及连接分别保存到同目录的 `postgres-password`、`postgres-dsn`。PG 仅挂载单独的 `data/bootstrap/postgres/` 密码子目录，不获得 API 密钥。配置不输出到日志或写入镜像，后续启动固定复用。
- 自带 PostgreSQL 的 DSN：`.env` 中 `EDO_COMPOSE_DATABASE_DSN` 留空，从持久化连接文件读取。可在首次初始化前填写外部数据库 DSN；该值优先于文件，内置 PostgreSQL 仍由当前 Compose 启动。使用其他数据库驱动时必须同时提供 DSN。
- 一次性 `bootstrap` 使用官方 Alpine/BusyBox 和系统随机源准备配置与顶层目录权限，无网络、不安装软件、不递归修改已有子目录。API 与迁移仍使用 `10001:10001`，Web 使用 `101:101`，无需手工创建或修改 data 权限。
- 已有 `.env` 的 `EDO_SECRETS_KEY` 会在首次接管时固定保存；以后必须与保存值一致。已有数据库或初始化记录缺少密钥、密码时拒绝启动，不静默生成替代值。已有 SQLite 不自动迁入 PostgreSQL，应保留原 DRIVER、DSN 和密钥。

PostgreSQL 使用默认名为 `edo_edo-postgres-data` 的 Docker 持久卷（自定义项目名时前缀相应变化），兼容 Docker Desktop 的文件身份映射。`data/bootstrap`、数据库卷和 `.env` 必须一起备份。配置目录权限受限，Linux 备份通常需要 sudo。不要重新复制模板覆盖已有 `.env`，不要删除配置目录解决启动错误。中断遗留的 `data/bootstrap/.init-lock` 会阻断初始化，先核对没有其他初始化进程，再由管理员处理遗留锁。

构建上下文排除真实 `.env`、data 与备份变体，只保留公开 `.env.example`。密钥不得放入 Dockerfile、构建参数或前端配置。Web 不读取业务卷或密钥；API 仅持有隔离构建器的只读 mTLS 客户端证书，不挂载宿主机 Docker Socket。

### 默认镜像源

按用户指定的 [docker.aityp.com](https://docker.aityp.com/) 公开索引使用其华为云同步地址，已将运行镜像和构建阶段镜像一起写入 Compose。同步条目默认 `linux/amd64`，macOS Apple Silicon 使用 Docker Desktop 仿真；原生 arm64 部署请把 `.env` 的 `EDO_DOCKER_PLATFORM` 和各镜像覆盖为实际支持该架构的官方或自有镜像。

| 用途 | 同步的官方镜像版本 |
| --- | --- |
| 初始化 | `alpine:3.22` |
| PostgreSQL | `postgres:17.11-trixie` |
| Redis | `redis:7.4.9` |
| Docker CLI / DinD | `docker:29.8.2-cli` / `docker:29.8.2-dind` |
| Go 编译 | `golang:1.26.10-alpine`，仍为 CGO 关闭的 Go 构建 |
| API 运行层 | `debian:bookworm-slim` |
| Node 编译 | `node:24.16.0-trixie-slim` |
| Web 运行层 | `nginx:1.30.6-alpine3.24` |

指定站暂无原 NATS 精确版本，继续使用 `nats:2.12.12-alpine`，通过 `docker.m.daocloud.io/library/nats:2.12.12-alpine` 下载，不降级 JetStream。原 Docker 28.3.3 未找到，CLI 与 DinD 一起改为已同步的固定 29.8.2。Go、Node、Redis 和 NGINX 保留原软件版本，选择可用的发行版标签。每项地址都可通过 `.env` 的 `EDO_*_IMAGE` 覆盖，不修改宿主机 Docker 镜像加速配置或重启 Docker。同步源是第三方镜像分发服务，不代表其镜像已完成独立安全审计。

公开构建依赖默认使用 `https://goproxy.cn,direct` 与 `https://registry.npmmirror.com`，可分别通过 `EDO_GO_PROXY`、`EDO_NPM_REGISTRY` 覆盖，仅用于构建阶段。

### 访问与配置覆盖

`.env` 支持以下常用配置，修改后执行 `docker compose up -d`：

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `EDO_HTTP_BIND` | `0.0.0.0` | Web 监听地址，仅供本机代理时改为 `127.0.0.1` |
| `EDO_HTTP_PORT` | `8080` | Web 对外端口 |
| `EDO_AUTH_COOKIE_SECURE` | `false` | 配置 HTTPS 代理后改为 `true` |
| `EDO_COMPOSE_DATABASE_DRIVER` | `postgres` | 首次选择数据库类型 |
| `EDO_COMPOSE_DATABASE_DSN` | 留空 | 使用自带 PostgreSQL；外部库或旧 SQLite 填写实际连接 |
| `EDO_SECRETS_KEY` | 留空 | 正式 Compose 首次自动生成；已有部署可提供原密钥 |

云服务器需在安全组放行 `EDO_HTTP_PORT` 对应的 TCP 端口，默认 `8080`。服务器本机访问正常但公网请求无法到达 Web 时，单独核对云入口和网络策略，不通过重建数据库或重启 Docker 解决。

API、数据库、Redis、NATS 和构建器不直接暴露宿主机端口。生产访问可通过 HTTPS 代理转发到 Web，保留 WebSocket Upgrade、上传限制与长连接；`EDO_WEB_PORT` 只影响开发 Compose。默认不信任转发头；需要真实客户端地址时，通过 `EDO_COMPOSE_SERVER_TRUSTED_PROXIES` 仅填写受控代理 IP/CIDR，不能填写 `/0` 或整个 Docker 私网。NGINX 覆盖来路的 `X-Forwarded-For`、`X-Real-IP` 并清除 `Forwarded`，不继承伪造地址。Web 前若还有代理，其信任需单独配置。

按产品约定，HTTP API 和终端 WebSocket 不检查 Origin、Host 或 Sec-Fetch-Site，仍保留会话和权限校验。通过 HTTPS、强密码和网络访问控制保护公开部署。

宿主机/Mage 使用 `EDO_DATABASE_*`、`EDO_REDIS_URL`、`EDO_NATS_URL`；容器使用 `.env` 的 `EDO_COMPOSE_*` 映射，避免把宿主机 `127.0.0.1` 当成容器服务地址。开发 Compose 通过 `EDO_DEV_COMPOSE_DATABASE_*` 独立使用 SQLite，且仍需手填固定 `EDO_SECRETS_KEY`，不执行正式部署的自动初始化。

## 更新发布

先验证、提交并推送 GitHub，再在服务器仓库执行：

```bash
git pull --ff-only origin main
git rev-parse HEAD
docker compose up -d --build
docker compose ps -a
curl --fail http://localhost:8080/api/v1/health/ready
```

更新前备份并确认没有正在执行的发布。远端未提交修改需先核对，不能覆盖。更新后记录分支和 Commit，并检查页面及 Web/API 健康。源码变更使用 `--build` 重建两个独立镜像，普通再次启动只需 `up -d`。PostgreSQL 固定大版本 17，不自动做跨大版本升级。

## 备份与恢复

运行中 PostgreSQL 使用官方工具备份，备份包含业务与凭据，请限制权限：

```bash
umask 077
docker compose exec -T postgres pg_dump -U edo -d edo -Fc > edo-postgres.dump
```

同时备份 `.env` 和整个 `data/`。若选择文件级完整备份，先 `docker compose down` 停止所有写入，再将数据库卷单独归档：

```bash
docker compose run --rm --no-deps --user 0 --entrypoint tar -v "$PWD:/backup" postgres -C /var/lib/postgresql/data -czf /backup/edo-postgres-data.tar.gz .
```

归档包含完整数据库，应限制访问。恢复完整环境时需恢复数据库卷、`.env` 与整个 `data/`，保持 PostgreSQL 大版本一致；由管理员在隔离环境核对，不直接覆盖现有库。外部 PostgreSQL/MySQL 还需独立备份。不要对正在写入的 PGDATA 直接打包并声称可恢复。

`docker compose down` 保留 bind 数据和命名卷；构建器命名卷另有镜像、缓存和本地发布的应用运行环境，其应用数据要独立备份。不要使用 `down -v` 或清空 `data/` 重启系统。

## 数据库 DSN 示例

只在首次初始化选择数据库；不支持运行后的跨库复制、转换或切换。

```text
# 外部 PostgreSQL
postgres://edo:password@postgres.example.com:5432/edo?sslmode=require&TimeZone=Asia%2FShanghai

# 外部 MySQL
edo:password@tcp(mysql.example.com:3306)/edo?charset=utf8mb4&parseTime=True&loc=Asia%2FShanghai&tls=true

# 保留原 Compose SQLite
EDO_COMPOSE_DATABASE_DRIVER=sqlite
EDO_COMPOSE_DATABASE_DSN=/app/data/edo.db
```

外部数据库先准备库及账号。迁移需要建表、索引权限；DSN 可包含认证信息，不得提交真实 `.env`。MySQL 新建和重连会话统一使用 `Asia/Shanghai`，服务器须预装对应时区表，参考 [MySQL 官方说明](https://dev.mysql.com/doc/refman/8.4/en/time-zone-support.html)。EDO 不自动改服务器或退回其他时区。

## 探针与停机

- `/healthz`：Web 存活。
- `/api/v1/health/live`：API 进程存活。
- `/api/v1/health/ready`：数据库、Redis、NATS JetStream 就绪。
- 停机统一关闭 HTTP/WebSocket、停止领取新消息，并在配置上限内等待后台任务。

不要让多个主机通过 NFS 共享 SQLite 文件，不要把 Docker Socket、kubeconfig、数据库密码或密钥写进镜像。

API 运行层安装系统包时默认使用华为云 Debian 镜像，通过 `EDO_DEBIAN_MIRROR` 覆盖；保留 Debian 官方签名验证，仅影响镜像构建，不修改宿主机 apt 配置。首次本地构建在官方 Debian 下载地址遇到单个包 502，切换后重新验证，不把原失败记为成功。
