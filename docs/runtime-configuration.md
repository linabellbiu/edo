# 运行配置

正式 Compose 可以直接 `docker compose up -d`。需要覆盖端口、镜像或依赖连接时，在仓库根目录的 `.env` 中填写变更项。Apple Silicon 和其他 ARM64 主机首次安装使用 `.env.arm64.example` 取得平台和镜像源覆盖。[部署说明](deployment.md)

## 系统设置

| 入口 | 可保存的配置 | 生效方式 |
| --- | --- | --- |
| 日志设置 | 最低日志级别、HTTP 访问日志、日志文件开关、目录、切分大小、压缩天数 | 保存后当前进程立即使用 |
| 日志设置 | 流水线日志和审计日志的保留时间 | 下次清理使用已保存的设置 |
| 存储设置 | 仓库工作区、构建目录、Git 缓存、本地产物目录、单个文件制品大小上限 | 保存后新任务和上传使用，新目录切换与进行中的制品存储操作互斥 |
| 安全与接入、登录方式、界面设置、助手设置 | 登录锁定、接入方式、界面显示及助手开关等 | 保存后由对应功能读取 |

日志默认输出 `info` 并开启 HTTP 访问日志和文件日志。滚动日志单文件默认 100 MiB，历史文件 3 天后压缩。正式和开发 Compose 的数据卷目录从 `/app/data` 推导，包括 `logs`、`repositories`、`builds`、`cache` 和 `artifacts`。宿主机运行的日志目录默认是 `logs`，其他运行目录位于 `data`。单个文件制品默认 1 GiB，可设 1 字节至 1 TiB。

数据库中保存的运行设置优先于启动默认值。已有 `.env` 的日志和目录覆盖继续作为未保存过相应设置时的启动值。已有文件保持可读，调整上限作用于后续登记或上传的文件制品。

## Compose 配置

在 `.env` 填写 Compose 需要的覆盖项。应用镜像、平台、端口和依赖连接会在 `docker compose up -d` 时按新配置创建或更新受影响的容器。构建参数变更时使用 `docker compose up -d --build`。

| 变量 | 默认值或用途 |
| --- | --- |
| `EDO_HTTP_BIND`、`EDO_HTTP_PORT` | `0.0.0.0`、`8080`。仅本机访问使用 `127.0.0.1` |
| `EDO_WEB_PORT`、`EDO_REDIS_PORT`、`EDO_NATS_PORT`、`EDO_NATS_MONITOR_PORT` | 开发 Compose 的映射端口，默认 `5173`、`6379`、`4222`、`8222` |
| `EDO_DOCKER_PLATFORM`、`EDO_DOCKER_BUILDER_PLATFORM` | 正式部署默认 `linux/amd64`。构建器平台默认继承部署平台 |
| `EDO_IMAGE`、`EDO_WEB_IMAGE`、`EDO_VERSION` | API 和 Web 构建结果的镜像名，以及后端版本，默认 `edo:local`、`edo-web:local`、`dev` |
| `EDO_BOOTSTRAP_IMAGE`、`EDO_POSTGRES_IMAGE`、`EDO_REDIS_IMAGE`、`EDO_NATS_IMAGE`、`EDO_DOCKER_BUILDER_IMAGE` | 部署依赖镜像。内置数据库是 PostgreSQL 18.6。镜像源与版本见部署说明 |
| `EDO_DOCKER_CLI_IMAGE`、`EDO_GO_BUILD_IMAGE`、`EDO_API_BASE_IMAGE`、`EDO_NODE_BUILD_IMAGE`、`EDO_WEB_BASE_IMAGE` | API 和 Web 的构建及运行基础镜像 |
| `EDO_GO_PROXY`、`EDO_DEBIAN_MIRROR`、`EDO_NPM_REGISTRY` | 构建时使用的公开依赖下载源 |
| `EDO_COMPOSE_DATABASE_DRIVER`、`EDO_COMPOSE_DATABASE_DSN` | 正式部署默认 `postgres`，DSN 从持久化连接文件读取。配置外部数据库时填实际驱动和连接 |
| `EDO_DEV_COMPOSE_DATABASE_DRIVER`、`EDO_DEV_COMPOSE_DATABASE_DSN` | 开发 Compose 优先使用的数据库覆盖，默认 SQLite `/app/data/edo.db` |
| `EDO_COMPOSE_REDIS_URL`、`EDO_COMPOSE_NATS_URL` | 默认 `redis://redis:6379/0`、`nats://nats:4222` |
| `EDO_SECRETS_KEY` | 固定加密密钥。正式 Compose 首次自动生成并复用持久化文件，宿主机和开发 Compose 使用用户提供的 32 字节 Base64 值 |
| `EDO_AUTH_COOKIE_SECURE` | 正式 Compose 默认 `false`，供 HTTP 启动使用。配置 HTTPS 后设为 `true` |
| `EDO_COMPOSE_SERVER_TRUSTED_PROXIES` | 默认空。仅填写受控反向代理的 IP 或 CIDR，多个值用逗号分隔 |
| `EDO_DEPLOY_HOST_DIRECTORY` | 非 ARM64 卷模板的宿主机部署文件目录，默认 `~/edo/deply` |

已通过 `.env` 或启动命令设置的 `EDO_LOG_LEVEL`、`EDO_LOG_FILE_ENABLED`、`EDO_LOG_MAX_FILE_SIZE_MB`、`EDO_LOG_COMPRESS_AFTER_DAYS`、`EDO_ARTIFACTS_MAX_BYTES` 继续有效，四个目录的原 `EDO_COMPOSE_GIT_DIRECTORY`、`EDO_COMPOSE_GIT_CACHE_DIRECTORY`、`EDO_COMPOSE_BUILD_DIRECTORY`、`EDO_COMPOSE_ARTIFACTS_DIRECTORY` 也继续读取。可以在相应的系统设置保存当前值，后续在网页管理。

## 后端启动参数

需要调整后端进程初始化参数时，在仓库根目录创建 `.env.runtime`。该文件只注入 API 和迁移服务，优先于上一节的旧应用覆盖项。宿主机运行使用根目录 `.env` 或实际进程环境，进程环境优先。Compose 模板要求 v2.24.0 或更新版本，ARM64 数据子卷挂载要求见[部署说明](deployment.md)。

```dotenv
EDO_WORKER_CONCURRENCY=4
EDO_RUNTIME_TERMINAL_MAX_DURATION=1h
```

修改后运行 `docker compose up -d`。Compose 重建参数有变更的容器，后端在正常启动时读取新环境。

| 变量 | 代码默认值 |
| --- | --- |
| `EDO_ENV`、`EDO_SERVER_ADDRESS`、`EDO_SERVER_TRUSTED_PROXIES` | `development`、`:8080`、空。Compose 通过部署配置约定内部端口和代理来源 |
| `EDO_SERVER_READ_TIMEOUT`、`EDO_SERVER_WRITE_TIMEOUT`、`EDO_SERVER_IDLE_TIMEOUT`、`EDO_SERVER_SHUTDOWN_TIMEOUT` | `15s`、`30s`、`60s`、`15s` |
| `EDO_AUTH_SESSION_TTL`、`EDO_AUTH_COOKIE_NAME`、`EDO_AUTH_COOKIE_SECURE` | `8h`、`edo_session`、宿主机生产模式为 `true` |
| `EDO_AUTH_LOGIN_MAX_FAILURE`、`EDO_AUTH_LOGIN_WINDOW` | `5`、`15m`。启停锁定由系统设置控制 |
| `EDO_DATABASE_DRIVER`、`EDO_DATABASE_DSN` | 宿主机默认 `sqlite`、`data/edo.db`。Compose 用上表中的连接配置 |
| `EDO_DATABASE_MAX_OPEN_CONNS`、`EDO_DATABASE_MAX_IDLE_CONNS`、`EDO_DATABASE_CONN_MAX_LIFETIME` | `25`、`5`、`1h`。SQLite 使用单连接 |
| `EDO_REDIS_URL`、`EDO_REDIS_KEY_PREFIX`、`EDO_REDIS_TIMEOUT` | 宿主机默认 `redis://127.0.0.1:6379/0`、`edo:`、`3s` |
| `EDO_NATS_URL`、`EDO_NATS_TIMEOUT`、`EDO_NATS_MAX_ATTEMPTS` | 宿主机默认 `nats://127.0.0.1:4222`、`5s`、`4` |
| `EDO_NATS_STREAM`、`EDO_NATS_DEAD_STREAM`、`EDO_NATS_SUBJECT_PREFIX`、`EDO_NATS_DEAD_SUBJECT` | `edo_tasks`、`edo_dead`、`edo.task`、`edo.dead.task.v1` |
| `EDO_NATS_MAX_AGE`、`EDO_NATS_MAX_BYTES`、`EDO_NATS_DEAD_MAX_BYTES`、`EDO_NATS_REPLICAS` | `168h`、`536870912`、`268435456`、`1` |
| `EDO_WORKER_CONCURRENCY`、`EDO_WORKER_TASK_TIMEOUT`、`EDO_WORKER_LEASE_DURATION`、`EDO_WORKER_SHUTDOWN_TIMEOUT` | `8`、`30m`、`45s`、`30s` |
| `EDO_GIT_TIMEOUT`、`EDO_GIT_KNOWN_HOSTS_FILE` | `30s`、空。使用 SSH 时提供受信任的 known_hosts 文件 |
| `EDO_RUNTIME_CONNECT_TIMEOUT`、`EDO_RUNTIME_REQUEST_TIMEOUT`、`EDO_RUNTIME_TERMINAL_MAX_DURATION` | `10s`、`30s`、`2h` |
| `EDO_DOCKER_BUILDER_HOST`、`EDO_DOCKER_BUILDER_TLS_CERT_PATH` | 宿主机默认由 Docker 环境决定。Compose 使用隔离构建器及只读 mTLS 客户端证书 |
| `EDO_SCHEDULER_POLL_INTERVAL` | `15s` |
| `EDO_LOG_LEVEL`、`EDO_LOG_FILE_ENABLED`、`EDO_LOG_DIRECTORY`、`EDO_LOG_MAX_FILE_SIZE_MB`、`EDO_LOG_COMPRESS_AFTER_DAYS` | 日志设置的启动默认值，见上文 |
| `EDO_GIT_DIRECTORY`、`EDO_GIT_CACHE_DIRECTORY`、`EDO_BUILD_DIRECTORY`、`EDO_ARTIFACTS_DIRECTORY`、`EDO_ARTIFACTS_MAX_BYTES` | 存储设置的启动默认值，见上文。大小使用字节 |

Docker 内部连接和密钥文件路径由 Compose 提供，优先于 `.env.runtime` 中的同名键。数据库、认证 Cookie 和可信代理的 Compose 覆盖项使用上一节指定的 `.env` 变量。数据库大版本迁移按独立迁移流程处理，保持原加密密钥和数据卷可用。[数据库与部署说明](deployment.md#更新发布)
