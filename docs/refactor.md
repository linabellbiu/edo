# ZRT 架构说明

## 运行组件

ZRT 由同一个 Go 二进制和一个 React 静态站点组成：

- `zrt migrate`：显式执行数据库结构迁移。
- `zrt server`：提供 Gin API、React 静态资源与 WebSocket 终端。
- `zrt worker`：消费 NATS JetStream 任务，运行监控与定时调度扫描器。
- `web/`：React 19、TypeScript 和 Vite 前端。

API 与 Worker 共享数据库、Redis、NATS 和同一份 `ZRT_SECRETS_KEY`。服务启动只验证迁移版本，不会静默修改生产表结构。

## 数据库

默认 SQLite 适合单机和试用；多个 API/Worker 实例应使用 PostgreSQL 或 MySQL。SQLite 固定单连接并启用 WAL、外键和 busy timeout，不应通过网络文件系统在多主机间共享数据库文件。

所有模型通过同一组版本化 GORM 迁移在 SQLite、PostgreSQL、MySQL 上创建。CI 使用实际 PostgreSQL 和 MySQL 服务重复执行迁移，以验证幂等性。

## Redis 的职责

Redis 保留用于：

- 不透明登录会话；Redis 只保存会话 Token 的 SHA-256 摘要。
- 登录失败限流和有过期时间的短期状态。
- 后续横向扩容时的分布式锁、发布并发协调与实时状态。

Redis 不再用 List 充当任务队列。项目使用 go-redis/v9；这里的 v9 是 Go 客户端主版本，不代表 Redis Server 9。客户端固定 RESP2，当前部署基线为 Redis Server 7.x。

## NATS 与有限重试

任务记录和 Outbox 事件在一个数据库事务中创建。Publisher 使用 JetStream 消息 ID 去重后投递，Worker 显式 Ack、Nak 或 Term。

- `max_attempts` 包含首次执行，默认值为 4，即最多重试 3 次。
- 临时网络故障、超时和限流按退避策略重试。
- 参数、权限、签名和配置错误立即终止。
- 发布和回滚存在外部副作用，默认最大执行次数为 1。
- 执行次数耗尽后写入死信 Stream，并保存稳定的中文失败提示。
- Outbox 自身也使用有限投递次数，不能无限重试。

任务 Stream 默认上限为 512 MiB，死信 Stream 为 256 MiB，可通过 `ZRT_NATS_MAX_BYTES` 和 `ZRT_NATS_DEAD_MAX_BYTES` 按磁盘容量调整。ZRT 不再写死会导致小磁盘 JetStream 拒绝启动的 10GB 预留值。

## WebSocket 的用途

交互式终端需要双向、低延迟、持续传输二进制输出和终端尺寸变更，因此保留 WebSocket。SSE 只有服务端到浏览器的单向流，不能自然承载键盘输入和 resize 控制，不能替代交互终端。

WebSocket 仅用于 Docker 容器和 Kubernetes Pod Exec。宿主机 SSH、宿主机文件管理和浏览器直连服务器已删除。握手要求同源、登录会话、`terminal.open` 权限和 `zrt-terminal-v1` 子协议，并限制帧大小、写入时间及最长会话时长；打开和关闭都会写审计日志。

## 发布边界

- Docker 通过受控 Unix Socket 或双向 TLS Docker API 连接，拒绝明文远程 TCP API。
- Kubernetes 使用 in-cluster 身份或加密保存的 kubeconfig，拒绝 exec 插件和外部文件引用。
- 生产发布必须使用镜像摘要，并由非申请人审批。
- 发布任务保存目标快照；之后修改目标不会改变已经排队的任务。
- 新实例必须通过 Docker 健康状态或 Kubernetes Rollout 检查，失败时记录需人工确认的安全提示。
- Docker/Pod 终端不提供宿主机逃逸能力；实际权限仍由 Docker Socket 边界和 Kubernetes RBAC 决定。

## Git 与凭据

仓库类型支持 generic、GitHub、GitLab、Gitea、Gitee。HTTPS Token 和 SSH 私钥使用 AES-256-GCM 加密；SSH 强制使用可信 `known_hosts`，不会关闭主机指纹校验。Webhook 按平台校验签名并经过 Transactional Outbox 进入任务队列。

## 错误与审计

数据库、Redis、NATS、网络地址、调用栈和原始依赖错误只进入结构化日志。HTTP 客户端只收到稳定、可展示的中文错误和请求 ID。身份、角色、仓库、集群、发布、配置、通知、监控、调度、任务与终端操作均受 RBAC 控制并写入审计记录。
