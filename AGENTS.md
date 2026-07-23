## Go 重写目标

- 项目品牌名称为 `ZRT`，所有面向用户的界面标题、产品名称、文档标题和对外文案必须使用大写 `ZRT`。
- 机器可读标识使用小写 `zrt`，包括 Go module/package、前端包名、二进制、目录、容器镜像、服务名、数据库文件、Redis Key 前缀和 NATS Subject；环境变量使用大写 `ZRT_` 前缀。除原始外部标识外不得使用 `Zrt` 等混合写法。
- 项目源码、文档、配置示例、测试数据和对外文案不得再出现旧项目名称或标识；产品名称统一使用 `ZRT`，机器标识统一使用 `zrt`。
- 后端使用 Go、Gin 和 GORM 重写，不保留任何 Python 运行时、构建或运维依赖。
- ZRT 使用单进程运行模式；`zrt server` 必须通过 Go Goroutine 同时运行 HTTP/WebSocket、NATS 任务消费、Outbox、监控和调度，不再提供独立 `zrt worker` 命令、进程或部署容器。
- 数据库默认使用 SQLite，同时兼容 PostgreSQL 和 MySQL。
- 取消通过浏览器 SSH 登录宿主机的功能，不保留宿主机 Web SSH、远程文件管理或交互式服务器终端。
- 保留 Kubernetes Pod 和 Docker 容器内的交互式终端；浏览器通过 WebSocket 连接 Go 后端，由后端使用 Kubernetes Exec 或 Docker Exec API 桥接输入、输出和终端尺寸变更。
- 保留 Docker 和 Kubernetes 部署能力，优先通过 Docker API、BuildKit 和 Kubernetes API 实现，不依赖交互式服务器登录。
- 保留 WebSocket，用于 Pod/容器交互式终端、实时发布日志、构建日志、任务输出和站内通知；仅删除宿主机 Web SSH 通道。
- HTTP API 和终端 WebSocket 不执行 Origin、Host 或 `Sec-Fetch-Site` 同源限制，允许跨来源接入；仍必须保留登录会话、权限校验、终端子协议校验和操作审计。
- 使用 NATS JetStream 作为消息队列，用于任务投递、持久化、消费确认、失败重投和消费者组；不得再使用 Redis List 充当任务队列。
- 所有 JetStream Consumer 必须配置有限的最大投递次数，禁止无限重试；配置中的 `max_attempts` 包含首次执行，默认最多执行 4 次（首次执行加 3 次重试），耗尽后写入死信主题并将数据库任务标记为失败。
- 重试必须区分可重试错误和永久错误：临时网络故障、超时、限流等可以按退避策略重试；参数错误、权限拒绝、配置缺失、签名失败等必须立即终止，不得消耗后续重试次数。
- 发布与回滚等可能产生外部副作用的任务默认不自动重试，除非对应步骤已实现幂等保护；不得因消息重投而重复发布或重复执行不可逆操作。
- 保留 Redis，用于短期缓存、分布式锁、临时状态、发布并发协调和必要的实时数据；不得将其改为仅进程内实现。
- Git 仓库支持通用 Git，以及 GitLab、Gitea、GitHub、Gitee 的仓库和 Webhook 接入。
- 前端使用 React、TypeScript、Vite、React Router 和 Zustand；旧前端已经删除，不得重新引入 Create React App 或 Vue 运行依赖。
- 旧 Python 后端与安装脚本已经删除；兼容旧数据只能通过 Go 实现的只读迁移工具完成，不得恢复 Python 运行时。

## 重构决策原则

- Go 重构不是对旧 Python 实现的逐行翻译，也不要求无条件保留旧功能和旧交互。
- Docker Compose、Kubernetes 等运维配置必须用中文注释说明 YAML 锚点、一次性任务、启动依赖、安全限制、健康检查、持久化数据和关键容量或重试参数，不能只给配置而不解释用途与风险。
- 重构过程中发现功能设计不合理、安全风险、可靠性缺陷或运维体验问题时，可以直接调整、替换或删除功能，不限于修复缺陷。
- 决策应以高级运维工程实践为依据，优先保证最小权限、操作审计、幂等执行、并发安全、超时取消、失败重试、灰度发布、健康检查、回滚能力、资源隔离、可观测性和灾难恢复。
- 功能调整必须记录调整原因、行为变化、兼容性影响和数据迁移方式；不得静默改变关键发布语义。
- 涉及删除数据、降低安全边界、不可逆迁移或改变外部系统状态的操作，仍须明确目标并遵守破坏性操作安全规范。


<claude-mem-context>
# Memory Context

# claude-mem status

This project has no memory yet. The current session will seed it; subsequent sessions will receive auto-injected context for relevant past work.

Memory injection starts on your second session in a project.

`/learn-codebase` is available if the user wants to front-load the entire repo into memory in a single pass (~5 minutes on a typical repo, optional). Otherwise memory builds passively as work happens.

Live activity: http://localhost:37701
How it works: `/how-it-works`

This message disappears once the first observation lands.
</claude-mem-context>
