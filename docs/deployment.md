# EDO 部署说明

EDO 使用前后端分离的部署方式，Web 与 Go API 分别构建和运行，不把前端嵌入 Go 二进制。正式部署推荐 Docker Compose，本地进程启动见 [README](../README.md#后台运行)。

## 更新发布

发布顺序为：本地验证 → 提交并推送 GitHub → 远端拉取 → 远端构建 → 重启服务并验证。

先在开发机提交本次修改并推送到 GitHub 的 `main` 分支，再在服务器的项目目录执行 `git pull --ff-only origin main`。远端有未提交改动时，先备份并整理，再同步代码。

拉取完成后，在远端分别构建前端和后端，再按当前部署方式重新运行。Docker Compose 使用下面的构建启动命令；自行使用 systemd 管理 Go API 时，前端构建产物应单独交给 NGINX 等静态服务，并配置 API 与 WebSocket 转发，Go API 本身不提供网页。发布后检查 `/api/v1/health/ready` 和实际页面，并记录本次 Git 提交版本。

## Docker Compose

复制根目录环境文件模板，生成密钥并填入 `.env` 的 `EDO_SECRETS_KEY`：

```bash
cp .env.example .env
openssl rand -base64 32
docker compose --env-file .env up -d --build
```

`.env` 已被 Git 忽略，但仍应限制文件权限并单独备份密钥。已有数据库必须继续使用原密钥，不能用新生成的值直接覆盖，否则历史凭据将无法解密。

构建上下文会排除真实 `.env` 及其本地、生产和备份变体，只保留公开 `.env.example`。不要把密钥改放到 Dockerfile、构建参数或前端配置中。

根目录 `.env` 同时保存运行连接。直接运行二进制或 Mage 时使用 `EDO_DATABASE_*`、`EDO_REDIS_URL` 和 `EDO_NATS_URL`；Compose 容器使用 `.env` 中的 `EDO_COMPOSE_DATABASE_*`、`EDO_COMPOSE_REDIS_URL` 和 `EDO_COMPOSE_NATS_URL`，再映射成容器进程实际读取的变量。两组变量必须分开，因为宿主机的 `127.0.0.1`、相对 SQLite 路径不能在容器内原样使用。

默认仅 Web 容器监听宿主机 `127.0.0.1:8080`（由 `EDO_HTTP_PORT` 调整），NGINX 提供编译后的前端文件，并把 `/api/` 和 WebSocket 转发到容器网络内的 `api:8080`。API、Redis、NATS 和构建器不直接暴露宿主机端口。生产环境应再由启用 HTTPS 和 WebSocket Upgrade 的反向代理对外提供服务；`EDO_WEB_PORT` 只用于 Vite 开发入口，不改变正式 Web 入口。

Web 镜像不读取 `.env`、业务数据卷或 Docker 客户端证书；配置与凭据只由 API 使用。Web 与 API 均以非 root 用户运行，Web/API 镜像可分别重建。排查页面问题时同时检查 `docker compose --env-file .env logs api web`，不能仅凭 API 健康通过判断页面可用。

默认不信任客户端提供的转发头，经过代理的请求在审计及登录限流中记录代理地址。需要记录客户端地址时，宿主机部署使用 `EDO_SERVER_TRUSTED_PROXIES`，Compose 使用 `EDO_COMPOSE_SERVER_TRUSTED_PROXIES`，只填写实际受控代理的 IP/CIDR，以逗号分隔；不能填写 `/0`，也不要将整个 Docker 私网或构建器来源加入信任。NGINX 会覆盖来路的 `X-Forwarded-For`、`X-Real-IP` 并清除 `Forwarded`，不会继承用户伪造的地址。API 必须保持内部访问；若 Web 前面还有代理，其地址信任还需单独配置，不能直接相信传入的地址头。

首次启动服务且账户库为空时会自动创建管理员账户 `admin`，初始密码为 `123456`，且登录后不会强制修改密码。已有任意账户的数据库不会补建或覆盖该账户，普通新建账户的 12 位密码要求保持不变。

这一行为用于提供可直接登录的统一初始化环境。升级已有数据库时不会修改现有账户、密码或会话；旧数据导入仍可在首次启动服务前写入空账户库，全新部署无需额外账户迁移步骤。公开或生产部署应通过网络访问控制限制登录入口，并按实际安全要求主动重置默认密码。

Compose 默认使用持久化 SQLite，适合单机。需要运行多个 EDO 实例时，在首次初始化前将 `.env` 中的 `EDO_COMPOSE_DATABASE_DRIVER` 和 `EDO_COMPOSE_DATABASE_DSN` 改为 PostgreSQL 或 MySQL，并把 `EDO_COMPOSE_REDIS_URL`、`EDO_COMPOSE_NATS_URL` 指向高可用 Redis 与 NATS JetStream。连接 URL 可以携带认证信息，但 `.env` 不得提交且应限制为仅部署账户可读。每个实例都是包含 API 和后台任务 Goroutine 的完整单进程服务。

EDO 按产品约定不校验 HTTP API 和终端 WebSocket 的 `Origin`、`Host` 或 `Sec-Fetch-Site`，跨来源请求会直接进入登录与权限校验。部署时必须通过 HTTPS、强密码、最小权限和网络访问控制保护服务；反向代理仍建议让前端与 API 使用同一站点，避免扩大浏览器会话的攻击面。

## 数据库 DSN 示例

```text
# PostgreSQL
postgres://edo:password@postgres.example.com:5432/edo?sslmode=require&TimeZone=Asia%2FShanghai

# MySQL
edo:password@tcp(mysql.example.com:3306)/edo?charset=utf8mb4&parseTime=True&loc=Asia%2FShanghai&tls=true
```

生产数据库账户应只拥有 EDO 数据库所需权限。迁移进程需要建表和索引权限；EDO 主服务可以在迁移后使用权限更小的运行账户。

## 探针与停机

- `/api/v1/health/live` 只表示进程存活。
- `/api/v1/health/ready` 检查数据库、Redis、NATS JetStream。
- EDO 收到停止信号后统一关闭 HTTP/WebSocket，停止领取新消息，并在配置上限内等待后台在途任务结束。

不要让多个主机通过 NFS 共享 SQLite 文件，也不要把 Docker Socket、kubeconfig、数据库密码或 `EDO_SECRETS_KEY` 写进镜像。
