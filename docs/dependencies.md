# EDO 第三方依赖决策

这里记录第三方库的用途和选型原因。组织规则、权限和审计由 EDO 自身处理。

## 已采用

| 能力 | 依赖 | 采用原因 | EDO 负责的部分 |
| --- | --- | --- | --- |
| 环境变量解析 | `github.com/caarlos0/env/v11` | 零运行时依赖，支持结构体标签、数值、布尔值和 `time.Duration`，替代手写类型转换 | 默认值、跨字段校验、显式空值检查和安全错误文案 |
| Git 远端引用与认证 | `github.com/go-git/go-git/v5` | 纯 Go、持续维护、支持 HTTPS Basic Auth、SSH 私钥和 `known_hosts` | 仓库地址白名单、不安全 HTTP 显式确认、凭据所有权和引用数量上限 |
| Docker 构建与发布 | Moby Client + Docker CLI/Buildx/Compose v2 + `golang.org/x/crypto/ssh` | Moby 官方客户端负责镜像、容器和传输 API；官方 Docker CLI/Buildx 负责 BuildKit 双向 session，Compose v2 负责执行经 EDO 校验的内联服务定义；Go 官方扩展库负责远程目标认证 | Compose 通过隔离网络连接 DinD，本地二进制直接使用宿主机 Docker；Docker CLI/Buildx/Compose 从固定版本官方镜像复制到 EDO 运行镜像，Apache-2.0 许可证兼容。每个 Worker 使用独立上下文、认证目录和镜像标签，可按配置并行构建；认证只写入权限为 `0600` 的临时配置并立即清理。本地目标直接部署构建运行时内镜像；远程目标固定主机指纹，有仓库时受控 push/pull，无仓库时传输 `docker save` 流并校验两端镜像，不开放宿主机终端 |
| Docker Compose YAML 解析 | `go.yaml.in/yaml/v3` | 维护活跃，MIT/Apache-2.0 双许可兼容；使用 AST 能在执行前精确验证服务、镜像占位符和禁止的外部文件引用 | EDO 只允许 `${EDO_IMAGE}` 插值，拒绝 `build`、`include`、`extends`、`env_file`、外部 config/secret 等越界输入，项目名、超时、镜像摘要校验和执行审计仍由 EDO 实现 |
| Docker 构建上下文 | `github.com/moby/patternmatcher` | 复用 Docker 官方维护的 `.dockerignore` 语义，避免自行实现忽略规则后出现敏感文件误打包或缓存失效 | 强制排除 `.git`、限制上下文为 1 GiB，并拒绝上下文外 Dockerfile |
| 密码摘要 | `github.com/matthewhartstonge/argon2` | Apache-2.0、持续维护、兼容标准 Argon2id PHC 格式 | 固定 Argon2id v1.3，并在计算前限制内存、迭代、并发、盐和摘要长度 |
| RBAC 判定 | `github.com/casbin/casbin/v3` | 成熟的 RBAC 模型、角色继承和 deny-override 判定 | 权限目录、超级管理员边界、个人密钥所有权和管理接口 |
| RBAC 多实例同步 | `github.com/casbin/redis-watcher/v2` | 使用现有 go-redis/v9 客户端广播策略失效事件 | GORM 表仍是唯一数据源；本实例先重载策略，再通知其他实例 |
| 发布并发锁 | `github.com/bsm/redislock` | 基于现有 go-redis/v9 的令牌锁、原子释放和有限 TTL，避免自行维护易出错的 SET NX/Lua 协议 | 锁粒度固定为发布目标；构建阶段不持锁，不同目标可并行，同一目标部署串行；等待受任务 Context 限制，TTL 由发布超时推导 |
| OCI 镜像仓库认证 | `github.com/regclient/regclient` | 成熟实现 OCI Distribution API、Basic/Bearer 认证挑战和 Registry 兼容处理 | 地址与 HTTP 安全校验、请求超时、权限审计、凭据加密和安全错误文案 |
| 主机与进程指标 | `github.com/shirou/gopsutil/v4` | BSD-3-Clause，在 EDO 支持的 Linux、macOS 上持续维护；复用成熟的 CPU、内存和进程采集实现，避免维护多套平台专用系统调用 | 只读取本机指标，不发起网络请求；Go 堆与 GC 使用标准库，Worker、任务和队列业务指标由 EDO 自身定义 |
| 监控历史折线图 | `echarts` 6.1.0 | Apache-2.0、成熟维护，按需加载折线、坐标系、提示和 Canvas 渲染模块；仅在监控路由加载 | 采样、三天保留期限、权限、缺失值、增量合并和进程重启边界仍由 EDO 定义，图表封装在 `MetricTrend.vue`，可独立替换 |
| 前端运行时、路由与状态 | `vue`、`vue-router`、`pinia` | 与 Vben Admin 5 `web-antd` 使用同一套 Vue 3 技术栈；均为 MIT 许可、维护活跃，路由元数据和组合式 Store 可以直接承载菜单、面包屑、历史标签和权限状态 | EDO 的有效权限、菜单层级、标签关闭策略和 API 契约仍由项目定义 |
| UI、图标与动效 | `ant-design-vue`、`lucide-vue-next`、`@vueuse/motion` | 对齐 Vben `web-antd` 的成熟组件与主题能力；图标和动效包均可按需打包，避免维护自制表单、弹层和动画基础设施 | EDO 主题令牌、简体中文文案、业务状态色、减少动效偏好和可访问性规则 |
| Vue 组件按需导入 | `unplugin-vue-components` | MIT 许可、持续维护并提供 Ant Design Vue Resolver；构建时生成显式组件导入，避免全量注册 UI 库造成超大首屏入口包 | 只在 Vite 构建阶段工作，不参与浏览器运行；组件选型和页面行为仍由 EDO 控制 |
| 国际化 | `vue-i18n` | Vue 官方生态事实标准，支持组合式 API、懒加载词典和运行时切换 | 简体中文为默认语言；语言入口、业务词典和 Ant Design Vue Locale 同步由 EDO 管理 |
| 流水线阶段与任务排序 | `sortablejs` | MIT 许可、成熟且项目已在使用；能直接实现阶段和阶段内任务链的横向拖动 | 只保存版本化的“唯一代码源 + 串行阶段数组 + 串行任务数组”；跨阶段规则、任务配置、草稿和启用边界由 EDO 控制 |

`go-git` 同时负责远端引用查询和指定 Commit 检出。运行镜像不再安装 `git` 和 `openssh-client`，SSH 私钥也不再写入临时文件。已有 Argon2id PHC 摘要不需要迁移。

系统指标使用 `gopsutil/v4` 的 CPU、内存、进程、负载和运行时间子包。当前固定在 v4.26.6，依赖体积中等且不需要常驻 Agent 或 CGO；采集被封装在系统指标服务中，后续可以按平台替换而不改变内置 API。即使单项采集失败，页面仍返回其余指标，并只在服务端日志记录原始错误。

历史图表使用 [Apache ECharts](https://github.com/apache/echarts)，采用官方 [按需导入方案](https://echarts.apache.org/handbook/en/basics/import/)，只注册 LineChart、GridComponent、TooltipComponent 和 CanvasRenderer，不导入地图、编辑器等无关模块。6.1.0 为稳定版本，Apache-2.0 许可兼容，依赖 zrender 6.1.0 与 tslib，支持当前 Linux/macOS 开发及现代浏览器，运行时不下载远程脚本。也评估了更小的 uPlot，最终使用 ECharts。图表包较大，仅在打开监控页时加载。维护风险是大量小图的重绘成本、提示交互和 API 升级，由三天窗口上限、关闭装饰动画、复用实例、卸载释放、回归测试和浏览器检查约束。图表不改变 API 或存储格式，后续可独立替换。现有 Vue、Axios、Vite 等依赖仍有安全告警，本次没有顺带升级既有技术栈，构建成功不表示这些安全告警已修复。

历史存储复用现有 `go-redis/v9` 的 [Sorted Set](https://redis.io/docs/latest/develop/data-types/sorted-sets/) 与事务管道，按时间范围读取、原子替换分钟采样、限制点数并设置过期时间；不自建时序数据库、图表引擎或外部采集服务。Redis 是短期运行趋势缓存，不替代长期监控归档。

2026-10-23 的生产构建中，ECharts 监控路由分包约 512 kB（gzip 174 kB）；这是按需加载的监控页成本，不进入首页入口包。npm 官方源的生产依赖审计未列出 echarts、zrender、tslib 告警；全项目仍有 8 项既有告警（7 项高危、1 项中危），未在本次图表修改中升级其他依赖。

发布锁使用 `bsm/redislock` v0.10.0，Apache-2.0 许可证与项目兼容，直接复用现有 go-redis/v9 客户端且依赖体积小。它只封装通用锁协议；目标锁 Key、等待边界、部署状态机、失败文案和审计仍由 EDO 控制，后续可替换而不改变业务表结构。

前端版本线按 Vben Admin 5.7 的 `web-antd` 工作区核对，当前固定 Vue 3.5、Pinia 3、Ant Design Vue 4.2、Vue I18n 11、VueUse Motion 3 和 Vue Router 5。上述包均为 MIT 许可、持续维护且不包含浏览器端远程代码加载；Vite 按路由拆包，组件和图标只打入实际引用的导出。EDO 只在布局、主题和页面组件边界依赖这些公开 API，后续可以逐包升级或替换，不会改变 Go API、权限或数据库模型。

## 实时通知与连接

流水线停止复用 Go 标准库 [Context 取消](https://pkg.go.dev/context#WithCancel)、[exec.Cmd.Cancel 与 WaitDelay](https://pkg.go.dev/os/exec#Cmd)、现有 Moby API 和 [SSH Session.Signal](https://pkg.go.dev/golang.org/x/crypto/ssh#Session.Signal)。这些能力由 Go 官方及既有 SDK 维护，不增加依赖、常驻服务或平台兼容面；仅支持既定 Linux/macOS，Shell 使用独立进程组控制子进程。持久化停止状态、权限、幂等和审计属于 EDO 业务规则，不引入另一套任务编排框架。维护风险是已提交远程操作无法撤回，因此保留人工确认提示，并用真实隔离 Docker 容器及本地 Shell 取消测试覆盖正常退出路径。

实时状态与流水线日志继续复用已有 `gorilla/websocket` 和官方 `go-redis/v9`，不增加 Redis/NATS 之外的服务或新的依赖。关闭握手、控制帧和并发约束按 [Gorilla 文档](https://pkg.go.dev/github.com/gorilla/websocket#hdr-Control_Messages) 实现；通知通过 Redis Pub/Sub 传递资源失效信号，业务数据仍以数据库和原有权限接口为准。Pub/Sub 的 [至多一次语义](https://redis.io/docs/latest/develop/pubsub/#delivery-semantics) 由重连补同步、日志游标和低频校准兜底。有限缓冲只用于合并突发通知，不代替 Redis；过载或断连不会阻塞业务任务。沿用已有固定版本和兼容许可证，不增加下载体积或平台兼容负担；通道适配封装在 `internal/realtime`，可独立替换。

前端评估了 VueUse `useWebSocket`（MIT、持续维护、支持 Vue 3 的重连与心跳）。本次使用浏览器原生 WebSocket，仅封装 EDO 的结束事件、权限失效停止、动态日志游标和共享订阅计数，未新增直接依赖。连接生命周期由项目维护，以假时钟回归及真实 Vite 代理测试覆盖；如果后续需要支持更多协议，再评估是否改用该库。

## 评估后未采用

| 候选 | 结论 |
| --- | --- |
| `casbin/gorm-adapter` | 当前版本不能与 EDO 的角色元数据共享同一个外部 GORM 事务。另建 `casbin_rule` 会形成双数据源并产生部分提交风险，因此使用只读适配器把现有事务表投影到 Casbin。 |
| `go-playground/webhooks` | 只覆盖 GitHub、GitLab、Gitea，不能完整覆盖 EDO 必须支持的 Gitee 和通用 Git；接入后仍需维护两套签名和事件模型，不能降低复杂度。 |
| GitHub、GitLab、Gitea、Gitee 四套完整 Go API SDK | 已评估官方或主流客户端，但当前只需要一个只读 PR/MR 列表接口；同时引入四套客户端会显著增加依赖体积、版本升级和企业版 Base URL 适配成本，且仍不能覆盖通用 Git。保留 `go-git` 处理标准 Git 能力，平台 PR/MR 使用 Go 标准库 HTTP 客户端调用官方只读 API，并通过统一结构、分页上限、超时、响应体上限和契约测试隔离平台差异；后续扩展评论、状态检查等多接口时再切换对应官方 SDK。 |
| 通用 Redis 限流器 | EDO 的登录锁定默认关闭，开启后只累计失败并在成功后清零，和普通请求频率限制语义不同；继续使用 Redis 原子计数与 TTL。 |
| 第三方 AES 封装 | 当前实现只是标准库 AES-256-GCM 的版本化信封和 AAD 绑定。额外依赖不会改善算法或密钥边界，反而扩大供应链。 |
| Gin 通用中间件合集 | 请求 ID、结构化访问日志和安全响应头都很短，并与 EDO 的中文错误边界及 `slog` 字段约定绑定；整包替换收益低。 |
| Prometheus `client_golang` 与 Grafana | 两者适合外部抓取、长期存储和集中看板，但不能直接满足 EDO 内登录后查看基础运行状态的产品要求。本次使用受权限保护的快照 API 和内置页面；未来可增加可选导出，但不能替代内置监控。 |
| 直接复制 `vbenjs/vue-vben-admin` 完整 Monorepo 与内部 `@vben/*` 工作区包 | Vben Admin 5 为 MIT 许可且维护活跃，本次已采用其 `web-antd` 技术栈和交互基线；但完整仓库依赖 pnpm workspace、Turbo 和大量内部包，直接嵌入会破坏 EDO 当前 npm 单应用、Docker 构建及 Go `embed` 链路。EDO 因此使用同版本线的 Vue、Pinia、Vue Router、Ant Design Vue、Vue I18n 与 Motion 公开包自行组织单应用，不复制不可独立发布的 `workspace:*` 包。 |
| Vue Flow、AntV X6、LogicFlow 等通用图编辑器 | 三者均有持续维护且许可证兼容的开源实现，适合自由连线、分支、缩放和平移画布；但 EDO 当前故意限制为类 Gitee 的唯一代码源与串行阶段。引入图引擎会增加依赖体积、可访问性和交互约束成本，且会诱导用户创建后端不支持的拓扑；因此复用现有 SortableJS，只实现项目需要的阶段和任务视图。执行器支持 DAG 后再评估图编辑器。 |
| [`github.com/otiai10/copy`](https://github.com/otiai10/copy) | 该项目持续维护、采用 MIT 许可，能处理通用目录递归复制、符号链接、权限和时间戳。EDO 的目录热切换只复制内容寻址存储中的不可变普通文件，还必须拒绝符号链接、响应 Context 取消、校验目标摘要，并在配置提交前持有制品写锁；通用复制器的大部分语义用不上，接入后仍需保留这些业务边界。因此使用 Go 标准库 `filepath.WalkDir`、`io.CopyBuffer` 和 `os.Root` 实现受限复制与清理，不新增运行依赖。维护风险是跨文件系统复制和异常中断恢复需由 EDO 自己保证，当前通过临时文件原子替换、目录用途标记、旧目录延后清理以及迁移后下载测试覆盖。 |

## Kubernetes YAML 编辑

本次 npm 官方源审计未列出 `yaml`、CodeMirror 或 Lezer 的告警。全项目仍有 8 项既有告警（7 项高危、1 项中危），涉及 Vue、Axios 和 Vite 等原有依赖，本次没有自动升级它们。

解析和格式化采用 [`yaml`](https://eemeli.org/yaml/) 2.9.1（ISC），代码编辑采用 [CodeMirror 6](https://codemirror.net/)（MIT）的 YAML、行号、折叠、历史和诊断模块。两者持续维护，支持现代浏览器与当前 Node 构建，不引入服务端进程，也不加载远程脚本。`yaml` 相比 `js-yaml` 能保留注释和节点位置；CodeMirror 相比 Monaco 更适合按需加载的小型配置编辑器。所有版本固定，锁文件可复现；语法库和界面组件分开封装，可独立替换。解析限制单文档、2 MiB、引用数量、嵌套深度和节点总数，拒绝重复键、循环引用、自定义标签和非 JSON 数值；诊断不回显提交的原文。依赖审计结果以本次锁文件检查为准，不把构建成功当作无漏洞证明。

服务端校验复用官方 client-go 的 `DryRunAll` 与 `FieldValidation: Strict`，不再引入 Go YAML 解析器。编辑投影、资源身份、字段白名单、并发保护和审计仍由 EDO 控制。对比过 RFC 7396 通用 `json-merge-patch` 方案；这里只比较可编辑资源投影中的 JSON 字段，用小型差异函数将明确移除的键转成 null，避免通用全资源补丁越过状态和节点身份边界。数组按整体替换，新增键、删除键、状态版本变化由测试覆盖；这部分维护风险是 Kubernetes 可变字段变化，最终由集群 dry-run 和真实保存共同检查。

## SQL 交互 Mock（仅测试）

采用 [DATA-DOG/go-sqlmock](https://github.com/DATA-DOG/go-sqlmock) v1.5.2（BSD-3-Clause），通过 `database/sql` 接入现有 GORM 驱动，不增加生产服务或运行时依赖。该库成熟稳定，但发布频率较低且上游征集维护者，不应宣称维护活跃；本次仅用于测试调用边界，封装于 `internal/testutil`，便于替换。纯 Go 实现适用于 Linux/macOS，库运行代码仅依赖标准库，体积小；固定版本与校验和，构建成功不代表完成漏洞审计，后续升级需检查上游安全公告及 Go 漏洞库。

评估过 pgxmock 和 go-txdb：前者主要针对 pgx 接口，不直接匹配当前多驱动 GORM 使用方式；后者仍需真实数据库，不能解决业务测试启动外部数据库的成本。这里不自行实现 SQL 驱动，也不让 Mock 模拟数据库约束、方言、权限回调或真实事务隔离；这些继续由独立 SQLite 集成与三种真实数据库迁移测试覆盖。维护风险是 SQL 文本随 GORM 升级改变，预期仅限定当前测试关心的操作，避免复制整套 ORM 实现。详见 [测试说明](testing.md)。

## 升级原则

- 依赖升级必须通过 `go test -race ./...`、`go vet ./...` 和前端生产构建。
- Git、认证、授权和加密依赖升级时必须额外验证现有数据兼容性及拒绝路径。
- 不采用预发布主版本；例如 `go-git/v6` 稳定前继续使用 v5 稳定线。

## 企业身份与邮箱验证码登录（2026-10-23）

邮件发送采用 [wneessen/go-mail v0.8.1](https://github.com/wneessen/go-mail/releases/tag/v0.8.1)，MIT 许可，仍有发布和安全维护；已核对其 [地址编码安全公告](https://github.com/wneessen/go-mail/security/advisories/GHSA-wpwj-69cm-q9c5)，修复从 v0.7.1 提供。只增加此直接依赖，复用项目已有的 x/crypto 和 x/text，不新增常驻服务；支持 Linux/macOS、Context 超时、强制 STARTTLS 和隐式 TLS，可在单一发件适配函数中替换。邮件地址、验证码生成、Redis 原子消费、发送频率、身份绑定和权限继续由 EDO 负责。评估了冻结维护的标准库 net/smtp 和旧 gomail：前者仍需自行处理 MIME、超时与 TLS 协商，后者维护状态不适合新接入，因此不自行实现 SMTP 协议。后续升级需持续核对 SMTP 认证行为、邮件地址编码及安全公告。

飞书复用已有 Go 官方 x/oauth2 与项目现有 JSON 令牌适配；钉钉新增薄适配器处理官方接口的 camelCase 字段、authCode 回调和专用令牌请求头。已评估 [钉钉官方 Go SDK](https://github.com/alibabacloud-go/dingtalk)（Apache-2.0）及飞书完整 SDK：本次只使用换令牌与用户信息两个接口，全量 SDK 会扩大生成代码与通用 RPC 依赖，因此继续复用现有 OAuth 库与 net/http；维护风险是平台协议变化，使用两套平台响应的 HTTP 契约测试约束，并以[钉钉官方授权流程](https://opensource.dingtalk.com/developerpedia/docs/develop/permission/token/browser/get_user_app_token_browser/)和[飞书授权码文档](https://open.feishu.cn/document/authentication-management/access-token/obtain-oauth-code)为更新依据。通用 OAuth 继续使用 PKCE；钉钉采用其公开的机密客户端授权流程与一次性 state，不假定支持 PKCE。
