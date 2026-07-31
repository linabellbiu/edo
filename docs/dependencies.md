# ZRT 第三方依赖决策

本文件记录通用能力的依赖选型。业务规则、权限边界和审计语义仍由 ZRT 定义；第三方包只负责经过验证的通用机制。

## 已采用

| 能力 | 依赖 | 采用原因 | ZRT 保留的边界 |
| --- | --- | --- | --- |
| 环境变量解析 | `github.com/caarlos0/env/v11` | 零运行时依赖，支持结构体标签、数值、布尔值和 `time.Duration`，替代手写类型转换 | 默认值、跨字段校验、显式空值检查和安全错误文案 |
| Git 远端引用与认证 | `github.com/go-git/go-git/v5` | 纯 Go、持续维护、支持 HTTPS Basic Auth、SSH 私钥和 `known_hosts` | 仓库地址白名单、不安全 HTTP 显式确认、凭据所有权和引用数量上限 |
| Docker 构建与发布 | Moby Client + Docker CLI/Buildx/Compose v2 + `golang.org/x/crypto/ssh` | Moby 官方客户端负责镜像、容器和传输 API；官方 Docker CLI/Buildx 负责 BuildKit 双向 session，Compose v2 负责执行经 ZRT 校验的内联服务定义；Go 官方扩展库负责远程目标认证 | Compose 通过隔离网络连接 DinD，本地二进制直接使用宿主机 Docker；Docker CLI/Buildx/Compose 从固定版本官方镜像复制到 ZRT 运行镜像，Apache-2.0 许可证兼容。每个 Worker 使用独立上下文、认证目录和镜像标签，可按配置并行构建；认证只写入权限为 `0600` 的临时配置并立即清理。本地目标直接部署构建运行时内镜像；远程目标固定主机指纹，有仓库时受控 push/pull，无仓库时传输 `docker save` 流并校验两端镜像，不开放宿主机终端 |
| Docker Compose YAML 解析 | `go.yaml.in/yaml/v3` | 维护活跃，MIT/Apache-2.0 双许可兼容；使用 AST 能在执行前精确验证服务、镜像占位符和禁止的外部文件引用 | ZRT 只允许 `${ZRT_IMAGE}` 插值，拒绝 `build`、`include`、`extends`、`env_file`、外部 config/secret 等越界输入，项目名、超时、镜像摘要校验和执行审计仍由 ZRT 实现 |
| Docker 构建上下文 | `github.com/moby/patternmatcher` | 复用 Docker 官方维护的 `.dockerignore` 语义，避免自行实现忽略规则后出现敏感文件误打包或缓存失效 | 强制排除 `.git`、限制上下文为 1 GiB，并拒绝上下文外 Dockerfile |
| 密码摘要 | `github.com/matthewhartstonge/argon2` | Apache-2.0、持续维护、兼容标准 Argon2id PHC 格式 | 固定 Argon2id v1.3，并在计算前限制内存、迭代、并发、盐和摘要长度 |
| RBAC 判定 | `github.com/casbin/casbin/v3` | 成熟的 RBAC 模型、角色继承和 deny-override 判定 | 权限目录、超级管理员边界、个人密钥所有权和管理接口 |
| RBAC 多实例同步 | `github.com/casbin/redis-watcher/v2` | 使用现有 go-redis/v9 客户端广播策略失效事件 | GORM 表仍是唯一数据源；本实例先重载策略，再通知其他实例 |
| 发布并发锁 | `github.com/bsm/redislock` | 基于现有 go-redis/v9 的令牌锁、原子释放和有限 TTL，避免自行维护易出错的 SET NX/Lua 协议 | 锁粒度固定为发布目标；构建阶段不持锁，不同目标可并行，同一目标部署串行；等待受任务 Context 限制，TTL 由发布超时推导 |
| OCI 镜像仓库认证 | `github.com/regclient/regclient` | 成熟实现 OCI Distribution API、Basic/Bearer 认证挑战和 Registry 兼容处理 | 地址与 HTTP 安全校验、请求超时、权限审计、凭据加密和安全错误文案 |
| 主机与进程指标 | `github.com/shirou/gopsutil/v4` | BSD-3-Clause，在 ZRT 支持的 Linux、macOS 上持续维护；复用成熟的 CPU、内存和进程采集实现，避免维护多套平台专用系统调用 | 只读取本机指标，不发起网络请求；Go 堆与 GC 使用标准库，Worker、任务和队列业务指标由 ZRT 自身定义 |
| 前端运行时、路由与状态 | `vue`、`vue-router`、`pinia` | 与 Vben Admin 5 `web-antd` 使用同一套 Vue 3 技术栈；均为 MIT 许可、维护活跃，路由元数据和组合式 Store 可以直接承载菜单、面包屑、历史标签和权限状态 | ZRT 的有效权限、菜单层级、标签关闭策略和 API 契约仍由项目定义 |
| UI、图标与动效 | `ant-design-vue`、`lucide-vue-next`、`@vueuse/motion` | 对齐 Vben `web-antd` 的成熟组件与主题能力；图标和动效包均可按需打包，避免维护自制表单、弹层和动画基础设施 | ZRT 主题令牌、简体中文文案、业务状态色、减少动效偏好和可访问性规则 |
| Vue 组件按需导入 | `unplugin-vue-components` | MIT 许可、持续维护并提供 Ant Design Vue Resolver；构建时生成显式组件导入，避免全量注册 UI 库造成超大首屏入口包 | 只在 Vite 构建阶段工作，不参与浏览器运行；组件选型和页面行为仍由 ZRT 控制 |
| 国际化 | `vue-i18n` | Vue 官方生态事实标准，支持组合式 API、懒加载词典和运行时切换 | 简体中文为默认语言；语言入口、业务词典和 Ant Design Vue Locale 同步由 ZRT 管理 |
| 流水线阶段与任务排序 | `sortablejs` | MIT 许可、成熟且项目已在使用；能直接实现阶段和阶段内任务链的横向拖动 | 只保存版本化的“唯一代码源 + 串行阶段数组 + 串行任务数组”；跨阶段规则、任务配置、草稿和启用边界由 ZRT 控制 |

`go-git` 同时负责远端引用查询和指定 Commit 检出。运行镜像不再安装 `git` 和 `openssh-client`，SSH 私钥也不再写入临时文件。已有 Argon2id PHC 摘要不需要迁移。

系统指标使用 `gopsutil/v4` 的 CPU、内存、进程、负载和运行时间子包。当前固定在 v4.26.6，依赖体积中等且不需要常驻 Agent 或 CGO；采集被封装在系统指标服务中，后续可以按平台替换而不改变内置 API。即使单项采集失败，页面仍返回其余指标，并只在服务端日志记录原始错误。

发布锁使用 `bsm/redislock` v0.10.0，Apache-2.0 许可证与项目兼容，直接复用现有 go-redis/v9 客户端且依赖体积小。它只封装通用锁协议；目标锁 Key、等待边界、部署状态机、失败文案和审计仍由 ZRT 控制，后续可替换而不改变业务表结构。

前端版本线按 Vben Admin 5.7 的 `web-antd` 工作区核对，当前固定 Vue 3.5、Pinia 3、Ant Design Vue 4.2、Vue I18n 11、VueUse Motion 3 和 Vue Router 5。上述包均为 MIT 许可、持续维护且不包含浏览器端远程代码加载；Vite 按路由拆包，组件和图标只打入实际引用的导出。ZRT 只在布局、主题和页面组件边界依赖这些公开 API，后续可以逐包升级或替换，不会改变 Go API、权限或数据库模型。

## 评估后未采用

| 候选 | 结论 |
| --- | --- |
| `casbin/gorm-adapter` | 当前版本不能与 ZRT 的角色元数据共享同一个外部 GORM 事务。另建 `casbin_rule` 会形成双数据源并产生部分提交风险，因此使用只读适配器把现有事务表投影到 Casbin。 |
| `go-playground/webhooks` | 只覆盖 GitHub、GitLab、Gitea，不能完整覆盖 ZRT 必须支持的 Gitee 和通用 Git；接入后仍需维护两套签名和事件模型，不能降低复杂度。 |
| GitHub、GitLab、Gitea、Gitee 四套完整 Go API SDK | 已评估官方或主流客户端，但当前只需要一个只读 PR/MR 列表接口；同时引入四套客户端会显著增加依赖体积、版本升级和企业版 Base URL 适配成本，且仍不能覆盖通用 Git。保留 `go-git` 处理标准 Git 能力，平台 PR/MR 使用 Go 标准库 HTTP 客户端调用官方只读 API，并通过统一结构、分页上限、超时、响应体上限和契约测试隔离平台差异；后续扩展评论、状态检查等多接口时再切换对应官方 SDK。 |
| 通用 Redis 限流器 | ZRT 的登录锁定默认关闭，开启后只累计失败并在成功后清零，和普通请求频率限制语义不同；继续使用 Redis 原子计数与 TTL。 |
| 第三方 AES 封装 | 当前实现只是标准库 AES-256-GCM 的版本化信封和 AAD 绑定。额外依赖不会改善算法或密钥边界，反而扩大供应链。 |
| Gin 通用中间件合集 | 请求 ID、结构化访问日志和安全响应头都很短，并与 ZRT 的中文错误边界及 `slog` 字段约定绑定；整包替换收益低。 |
| Prometheus `client_golang` 与 Grafana | 两者适合外部抓取、长期存储和集中看板，但不能直接满足 ZRT 内登录后查看基础运行状态的产品要求。本次使用受权限保护的快照 API 和内置页面；未来可增加可选导出，但不能替代内置监控。 |
| 直接复制 `vbenjs/vue-vben-admin` 完整 Monorepo 与内部 `@vben/*` 工作区包 | Vben Admin 5 为 MIT 许可且维护活跃，本次已采用其 `web-antd` 技术栈和交互基线；但完整仓库依赖 pnpm workspace、Turbo 和大量内部包，直接嵌入会破坏 ZRT 当前 npm 单应用、Docker 构建及 Go `embed` 链路。ZRT 因此使用同版本线的 Vue、Pinia、Vue Router、Ant Design Vue、Vue I18n 与 Motion 公开包自行组织单应用，不复制不可独立发布的 `workspace:*` 包。 |
| Vue Flow、AntV X6、LogicFlow 等通用图编辑器 | 三者均有持续维护且许可证兼容的开源实现，适合自由连线、分支、缩放和平移画布；但 ZRT 当前故意限制为类 Gitee 的唯一代码源与串行阶段。引入图引擎会增加依赖体积、可访问性和交互约束成本，且会诱导用户创建后端不支持的拓扑；因此复用现有 SortableJS，只实现领域化阶段/任务视图，不自行重造通用图引擎。若执行器未来真实支持 DAG，再重新评估。 |

## 升级原则

- 依赖升级必须通过 `go test -race ./...`、`go vet ./...` 和前端生产构建。
- Git、认证、授权和加密依赖升级时必须额外验证现有数据兼容性及拒绝路径。
- 不采用预发布主版本；例如 `go-git/v6` 稳定前继续使用 v5 稳定线。
