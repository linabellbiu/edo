# ZRT 部署说明

## Docker Compose

生成密钥并仅保存在密钥管理系统或受限环境文件中：

```bash
export ZRT_SECRETS_KEY="$(openssl rand -base64 32)"
docker compose up -d --build
```

默认只监听 `127.0.0.1:8080`，应由启用 HTTPS 和 WebSocket Upgrade 的反向代理对外提供服务。首次启动后创建管理员：

```bash
docker compose run --rm api admin create --username admin --nickname 管理员
```

Compose 默认使用持久化 SQLite Volume，适合单机。需要 API/Worker 横向扩容时，将 `ZRT_DATABASE_DRIVER` 和 `ZRT_DATABASE_DSN` 改为 PostgreSQL 或 MySQL，并使用高可用 Redis 与 NATS JetStream。

## Kubernetes

1. 将 `deploy/kubernetes/secret.example.yaml` 复制到集群外的密钥管理流程，替换 PostgreSQL、Redis、NATS 和加密密钥。
2. 把 `deployment.yaml` 中的 `ghcr.io/example/zrt:latest` 替换为自己的不可变镜像摘要。
3. 先创建 Secret，再应用 Kustomize 资源：

```bash
kubectl apply -f secret.yaml
kubectl apply -k deploy/kubernetes
```

示例把 API 与 Worker 放在同一个 Pod，迁移由 initContainer 完成。生产环境可拆分为两个 Deployment，但迁移必须先成功。`zrt` ServiceAccount 的示例 ClusterRole 只允许读取 Namespace/Pod、执行 Pod Exec，以及读取和更新 Deployment；应按实际目标命名空间进一步缩小权限。

Ingress 示例位于 `deploy/kubernetes/ingress.example.yaml`，为长连接设置了两小时代理超时。生产环境必须配置 TLS，否则 Secure Cookie 无法工作。

## 数据库 DSN 示例

```text
# PostgreSQL
postgres://zrt:password@postgres.example.com:5432/zrt?sslmode=require

# MySQL
zrt:password@tcp(mysql.example.com:3306)/zrt?charset=utf8mb4&parseTime=True&loc=UTC&tls=true
```

生产数据库账户应只拥有 ZRT 数据库所需权限。迁移进程需要建表和索引权限；API/Worker 可以在迁移后使用权限更小的运行账户。

## 探针与停机

- `/api/v1/health/live` 只表示进程存活。
- `/api/v1/health/ready` 检查数据库、Redis、NATS JetStream。
- API 使用 HTTP 优雅停机；Worker 停止领取消息并等待在途任务，超过上限后退出。

不要让多个主机通过 NFS 共享 SQLite 文件，也不要把 Docker Socket、kubeconfig、数据库密码或 `ZRT_SECRETS_KEY` 写进镜像。
