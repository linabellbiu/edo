# EDO 部署说明

## Docker Compose

复制根目录环境文件模板，生成密钥并填入 `.env` 的 `EDO_SECRETS_KEY`：

```bash
cp .env.example .env
openssl rand -base64 32
docker compose --env-file .env up -d --build
```

`.env` 已被 Git 忽略，但仍应限制文件权限并单独备份密钥。已有数据库必须继续使用原密钥，不能用新生成的值直接覆盖，否则历史凭据将无法解密。

根目录 `.env` 同时保存运行连接。直接运行二进制或 Mage 时使用 `EDO_DATABASE_*`、`EDO_REDIS_URL` 和 `EDO_NATS_URL`；Compose 容器使用 `.env` 中的 `EDO_COMPOSE_DATABASE_*`、`EDO_COMPOSE_REDIS_URL` 和 `EDO_COMPOSE_NATS_URL`，再映射成容器进程实际读取的变量。两组变量必须分开，因为宿主机的 `127.0.0.1`、相对 SQLite 路径不能在容器内原样使用。

默认只监听 `127.0.0.1:8080`，应由启用 HTTPS 和 WebSocket Upgrade 的反向代理对外提供服务。首次启动服务且账户库为空时会自动创建管理员账户 `admin`，初始密码为 `123456`，且登录后不会强制修改密码。已有任意账户的数据库不会补建或覆盖该账户，普通新建账户的 12 位密码要求保持不变。

这一行为用于提供可直接登录的统一初始化环境。升级已有数据库时不会修改现有账户、密码或会话；旧数据导入仍可在首次启动服务前写入空账户库，全新部署无需额外账户迁移步骤。公开或生产部署应通过网络访问控制限制登录入口，并按实际安全要求主动重置默认密码。

Compose 默认使用持久化 SQLite，适合单机。需要横向扩容 EDO 实例时，将 `.env` 中的 `EDO_COMPOSE_DATABASE_DRIVER` 和 `EDO_COMPOSE_DATABASE_DSN` 改为 PostgreSQL 或 MySQL，并把 `EDO_COMPOSE_REDIS_URL`、`EDO_COMPOSE_NATS_URL` 指向高可用 Redis 与 NATS JetStream。连接 URL 可以携带认证信息，但 `.env` 不得提交且应限制为仅部署账户可读。每个实例都是包含 API 和后台任务 Goroutine 的完整单进程服务。

## Kubernetes

1. 将 `deploy/kubernetes/secret.example.yaml` 复制到集群外的密钥管理流程，替换 PostgreSQL、Redis、NATS 和加密密钥。
2. 把 `deployment.yaml` 中的 `ghcr.io/example/edo:latest` 替换为自己的不可变镜像摘要。
3. 先创建 Secret，再应用 Kustomize 资源：

```bash
kubectl apply -f secret.yaml
kubectl apply -k deploy/kubernetes
```

示例只运行一个 EDO 主容器，数据库迁移由 initContainer 完成。主进程同时处理 HTTP/WebSocket 和后台任务，不需要单独的 Worker Deployment。`edo` ServiceAccount 的示例 ClusterRole 只允许读取 Namespace/Pod、执行 Pod Exec，以及读取和更新 Deployment；应按实际目标命名空间进一步缩小权限。

Ingress 示例位于 `deploy/kubernetes/ingress.example.yaml`，为长连接设置了两小时代理超时。生产环境必须配置 TLS，否则 Secure Cookie 无法工作。

EDO 按产品约定不校验 HTTP API 和终端 WebSocket 的 `Origin`、`Host` 或 `Sec-Fetch-Site`，跨来源请求会直接进入登录与权限校验。部署时必须通过 HTTPS、强密码、最小权限和网络访问控制保护服务；反向代理仍建议让前端与 API 使用同一站点，避免扩大浏览器会话的攻击面。

## 数据库 DSN 示例

```text
# PostgreSQL
postgres://edo:password@postgres.example.com:5432/edo?sslmode=require

# MySQL
edo:password@tcp(mysql.example.com:3306)/edo?charset=utf8mb4&parseTime=True&loc=UTC&tls=true
```

生产数据库账户应只拥有 EDO 数据库所需权限。迁移进程需要建表和索引权限；EDO 主服务可以在迁移后使用权限更小的运行账户。

## 探针与停机

- `/api/v1/health/live` 只表示进程存活。
- `/api/v1/health/ready` 检查数据库、Redis、NATS JetStream。
- EDO 收到停止信号后统一关闭 HTTP/WebSocket，停止领取新消息，并在配置上限内等待后台在途任务结束。

不要让多个主机通过 NFS 共享 SQLite 文件，也不要把 Docker Socket、kubeconfig、数据库密码或 `EDO_SECRETS_KEY` 写进镜像。
