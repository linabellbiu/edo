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
- 实现通用能力前必须先调研 Go 生态中成熟、持续维护且许可证兼容的第三方包，优先复用官方 SDK、事实标准库和经过生产验证的实现，不得在已有可靠方案时无理由重复造轮子；引入前仍须评估维护活跃度、安全记录、依赖体积、可替换性和运维成本，业务规则及安全边界保留在 ZRT 自身代码中。
- Docker Compose、Kubernetes 等运维配置必须用中文注释说明 YAML 锚点、一次性任务、启动依赖、安全限制、健康检查、持久化数据和关键容量或重试参数，不能只给配置而不解释用途与风险。
- 重构过程中发现功能设计不合理、安全风险、可靠性缺陷或运维体验问题时，可以直接调整、替换或删除功能，不限于修复缺陷。
- 决策应以高级运维工程实践为依据，优先保证最小权限、操作审计、幂等执行、并发安全、超时取消、失败重试、灰度发布、健康检查、回滚能力、资源隔离、可观测性和灾难恢复。
- 功能调整必须记录调整原因、行为变化、兼容性影响和数据迁移方式；不得静默改变关键发布语义。
- 涉及删除数据、降低安全边界、不可逆迁移或改变外部系统状态的操作，仍须明确目标并遵守破坏性操作安全规范。

## 代码风格规范

- 新增和修改代码必须贴合所在模块既有的命名、分层、错误处理和测试习惯，避免出现与项目风格割裂的模板化“AI 代码”。
- 只实现需求所需的最小清晰结构，不为展示完整性增加无实际调用的接口、空泛扩展点、重复包装层、过度通用的辅助类型或不必要的设计模式。
- 注释重点说明设计原因、业务约束和容易误用的边界，不逐行复述代码，不添加“初始化”“处理数据”“返回结果”等无信息量注释，也不使用装饰性分区注释堆砌文件结构。
- 命名和控制流应自然、直接、符合语言惯例；能在局部清楚表达的逻辑不随意拆成大量单次调用函数，已有成熟包或项目公共能力可以解决的问题不得重新实现一套近似版本。
- 测试围绕真实行为、失败边界和回归风险编写，不生成机械重复、只验证常量或只为提高数量的展示性用例。
- 对外文案、日志和文档使用简洁自然的中文，避免空泛总结、营销式措辞和明显的机器生成套话。

## 前端交互规范

- 表单中用于选择可创建关联资源的下拉框，必须在控件右侧提供可访问的“＋”创建入口；仅在用户拥有对应创建权限时显示，点击后应跳转并直接展开对应资源的创建界面，或聚焦到不会自动写入数据的明确创建入口，不得要求用户退出当前流程后再从侧栏查找。
- Git 令牌必须按用户隔离并加密保存，任何账户（包括超级管理员）都不得通过用户接口查看其他用户的令牌明文；仓库只能引用当前操作者自己的已保存令牌。
- Webhook 签名密钥允许拥有 `repository.secret.read` 权限的用户重复查看，不再采用只显示一次的交互；每次读取必须经过鉴权并写入审计日志。
- RBAC 同时支持角色权限和用户级权限覆盖；用户级显式拒绝优先于角色授权，用户级允许用于少量例外，前端菜单可见性与后端接口必须使用同一份有效权限结果。
- 新建镜像仓库表单必须提供“测试”按钮；测试应使用尚未保存的配置执行真实 OCI Registry 登录，只有登录成功才显示测试成功，且测试过程不得持久化仓库配置或明文凭据。
- RBAC 授权判定统一使用 Casbin；现有 GORM 角色、权限和用户覆盖表是唯一事务数据源，通过只读策略适配器加载到 Casbin，并使用 Redis Watcher 同步多实例策略，禁止再维护另一套自研权限匹配逻辑或重复的 `casbin_rule` 影子数据。


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
