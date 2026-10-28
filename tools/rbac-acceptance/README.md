# EDO RBAC 真实 HTTP 验收

本目录对应 [RBAC 验收计划](../../docs/rbac-devops-acceptance-plan.md)，不安装依赖，不启动或重建 EDO。`run.mjs` 与 `sql-workflow.mjs` 使用 Node.js 20+ 的内置 `fetch`，本轮验证版本为 Node.js 24.16.0。`revocation.mjs` 的带请求头 WebSocket 已验证 Node.js 24.16.0，也可使用 Bun；不能把它视为 Node.js 20 通用接口。`terminal-workflow.mjs` 使用已验证的 Bun 1.3.13，并在读取文件或修改资源前拒绝其他运行时。

只允许 `127.0.0.1` 和 `[::1]` IP，默认 `http://127.0.0.1:18180`；拒绝重定向和携带凭据的 URL。管理员必须由维护本地验收环境的会话通过官方管理命令另建，不读取、改用其他账号的密码。

## 准备与运行

在源码目录之外创建权限为 `700` 的私有目录，管理员 JSON 文件为 `600`，格式为 `{"username":"本轮管理员","password":"本轮管理员密码"}`。不要把真实内容粘贴进命令行、聊天、日志或仓库。

```sh
node tools/rbac-acceptance/run.mjs --admin-file /private/tmp/edo-rbac-private/admin.json --out /private/tmp/edo-rbac-private/run-01 --phase seed
node tools/rbac-acceptance/run.mjs --admin-file /private/tmp/edo-rbac-private/admin.json --out /private/tmp/edo-rbac-private/run-01 --phase matrix
```

`--phase all` 顺序执行两阶段。`seed` 对同一目录只运行一次；重新跑整轮请使用新输出目录。`matrix` 可复测已建种子，变量和额外配置使用新随机后缀，不自动删除既有测试数据。遇到超时不会自动重试写请求，以免重复创建。

输出中的 `manifest.json` 权限为 `600`，包含本轮 UUID、角色动作和随机测试账号凭据，供同轮浏览器验收使用；不包含管理员凭据、Cookie、密钥值或响应正文。`report-*.json` 仅保留断言、状态码、安全错误码、权限代码和时间。终端只打印用例标识及结果。报告、清单不得提交或公开。

## 首版覆盖

- 从真实 `/permissions` 读取并验证 platform/project 范围，创建完整职责模板、两个部门和三个项目；开发者在 A 开发、B 只读，另一部门 C 独立。
- 共 17 个职责模板：平台组织管理员、项目管理员、开发、测试、构建、发布申请、发布审批、项目运维、DBA 审核、SQL 申请、DBA 执行、项目只读审计、平台安全审计、个人 Git 凭据、变量管理、密钥使用与独立终端；另有非成员、停用用户与内置开发角色身份。
- 真实登录和 `/auth/me` 有效动作核对；按身份检查项目与平台列表，直接 HTTP 请求验证拒绝，不以隐藏菜单代替后端安全。
- 矩阵开始先由管理员逐条读取相同项目/平台路径，要求 HTTP 200 和真实 JSON，证明路由及参数有效；正常角色访问不接受 404/400 作为通过。
- 有效仓库/环境/变量请求、跨项目对象和环境关联、项目头与查询参数优先关系；同部门 B 的只读权限不能沿用 A 的开发权限。
- 允许委派较小角色，以及自改角色、授予平台角色、超过自身能力、跨部门成员、跨部门调动与平台角色授予项目执行等负例。
- 变量 CRUD、版本冲突、跨项目关联、密钥元数据和 `no-store`，不保存或输出密钥值。
- 两个普通账号各自拥有个人 Git 凭据权限；只创建随机的本轮虚拟令牌，核验所有者列表/查看与另一账号读取、修改、删除、仓库关联均被拒绝，最后删除本轮令牌。不向 Git 托管平台发送这些令牌。
- 内置开发角色是否携带发布保护/集群写权限；没有 `deployment.review` 的成员是否出现在审核人选项并被允许保存为必需审核人；合法双审批策略保存及开发者不能关闭保护。
- 尝试创建完整项目动作角色；超过 API 动作上限时明确失败并保留原数量，绝不截断权限列表冒充成功。

种子仓库指向 `example.invalid`，关闭 Webhook，不创建关联应用。当前 `CreateApplication` 总是启用新应用，后台 `scanDueApplications` 扫描全部启用应用；`SyncApplication` 在判断空工作流前也会轮询仓库。创建后立即停用存在竞态，不能保证没有外部 Git 请求。因此脚本显式禁止 `POST /applications`，将应用创建/修改正向及相应关联场景记为“前置未满足”，等待原子创建停用应用的正常入口后再测试。不要为了继续验收而停止或修改共享后台调度。

环境无主机，构建方案只保存不执行。脚本不连接外部代码源、Docker、Kubernetes、SSH 或数据库目标，也不读取本机集群配置。

首版不声称完成流水线、真实部署、双人审核运行、排队撤权、WebSocket、终端、文件或 SQL 外部执行验收；这些应按计划后续接入独立测试目标。预期拒绝收到意外成功会标记失败，保留本轮目标便于排查，不删除或改动既有用户、角色、项目和资源。

## 在线撤权专项

`revocation.mjs` 另建自己的 `rbac_revoke_` 项目、角色和账号，只读取基础种子中的两个部门 ID，不改变原矩阵身份。它验证同一 Cookie 的角色减权、个人拒绝、移出项目、部门变更、账号/项目停用，以及共享模板跨项目的实际作用；状态 WebSocket 必须先成功握手并收到 `resync`，才断言撤权关流。

```sh
node tools/rbac-acceptance/revocation.mjs --admin-file /private/tmp/edo-rbac-private/admin.json --source-manifest /private/tmp/edo-rbac-private/run-01/manifest.json --out /private/tmp/edo-rbac-private/revoke-01 --phase all
node tools/rbac-acceptance/revocation.mjs --admin-file /private/tmp/edo-rbac-private/admin.json --source-manifest /private/tmp/edo-rbac-private/run-01/manifest.json --out /private/tmp/edo-rbac-private/revoke-01 --phase verify
```

账号停用测试保留两个已登录会话：一个在停用期发请求，另一个始终休眠。重新启用后两个旧会话都应失效，新登录应成功，不能只证明活跃会话被删除。所有状态变更和最终恢复均限制在专项白名单 UUID，报告包含最终恢复状态；不创建应用、执行任务或直接写数据库。凭据单独存于 `600` 的 `credentials.json`，Cookie 不落盘。

## SQL 岗位流程专项

`sql-workflow.mjs` 默认 `prepare` 只生成私有计划，不发 HTTP 请求或创建容器。后续阶段会创建独立 MySQL 8.4 目标，并通过正常 EDO 接口执行“保存并检查 → 独立审核 → 执行”。这不是只读测试：仅在独立验收环境与任务窗口已确认后使用。

先通过正常 EDO 流程取得 A 项目的真实应用、分支、流水线运行与环境 UUID；不要生成替代 UUID。把这些值填入本轮 `EDO_RBAC_APPLICATION_ID`、`EDO_RBAC_PIPELINE_RUN_ID`、`EDO_RBAC_ENVIRONMENT_ID` 环境变量，再生成计划。默认分支为 `refs/heads/main`，其他分支在 `prepare` 时使用 `--branch` 指定。

```sh
node tools/rbac-acceptance/sql-workflow.mjs --admin-file /private/tmp/edo-rbac-private/admin.json --source-manifest /private/tmp/edo-rbac-private/run-01/manifest.json --out /private/tmp/edo-rbac-private/sql-01 --phase prepare --application-id "$EDO_RBAC_APPLICATION_ID" --pipeline-run-id "$EDO_RBAC_PIPELINE_RUN_ID" --environment-id "$EDO_RBAC_ENVIRONMENT_ID"
node tools/rbac-acceptance/sql-workflow.mjs --admin-file /private/tmp/edo-rbac-private/admin.json --source-manifest /private/tmp/edo-rbac-private/run-01/manifest.json --out /private/tmp/edo-rbac-private/sql-01 --phase target
node tools/rbac-acceptance/sql-workflow.mjs --admin-file /private/tmp/edo-rbac-private/admin.json --source-manifest /private/tmp/edo-rbac-private/run-01/manifest.json --out /private/tmp/edo-rbac-private/sql-01 --phase submit
```

后续沿用完全相同的管理员、来源清单和输出目录，依次使用 `--phase review`、`--phase execute`、`--phase verify`，最后显式执行 `--phase cleanup`。分阶段运行可在待审核、已批准、执行成功时分别检查三岗位页面；`review` 也可核对已由指定 DBA 在页面完成的唯一批准记录。`all` 会连续创建目标、检查、审核和执行，不包含 `cleanup`，不替代页面验收。

只有本轮准确内容仍为 `check_failed`、没有审核或执行记录时，才可明确选择 `--phase recheck`。它通过正常检查接口生成新修订并保留原失败记录，不会直接改状态，也不会自动重试外部写入。检查镜像由 API 的构建运行时使用；宿主 Docker 已有镜像不代表该运行时已准备好。冷启动超时应保留为失败，后续重新检查成功不能冒充原失败已修复。

目标固定为官方 `mysql:8.4`，使用独立名称、网络、回环端口与 tmpfs，限制 1 CPU、1 GiB 内存及进程数。API 容器须能通过 `host.docker.internal` 访问该回环映射；该地址不能冒充 API 容器自己的 `127.0.0.1`。脚本每次运行都复核保存的 Docker context 仍指向本地 Unix endpoint，不切换全局 context。bootstrap 只创建专库、运行用户与只读验收用户；业务建表和写入只来自经过审核的 SQL 工单。

`cleanup` 先核对项目、应用、环境、数据库身份与当前版本，通过正常 PUT 停用本轮数据库配置，然后按保存的容器和网络 ID 清理，拒绝同名替换。未完成启动或已停止的自有资源，仅在尚未登记数据库配置、或配置已经停用时可清理。已登记配置仍启用而目标停止或缺失时，脚本明确失败并保留现场：现有 PUT 停用仍要求可连接，不能借清理直接写 EDO 数据库、删除配置或擅自重启环境。

报告、工单、角色和账号历史保留；`cleanup` 不删除这些记录、宿主镜像或私有证据文件。`target-credentials.json` 包含随机目标及影子账号凭据，`mysql-secrets/` 包含目标连接文件，均须继续保持目录 `700`、文件 `600`，由验收维护者按留存要求处置。本工具不自动停用影子账号，不把基础设施清理称为全部身份清理。

## 主机终端与文件专项

`terminal-workflow.mjs` 只用于独立 E2E API 容器内的本地主机。`enable` 会把内置本地主机从仅 `docker` 临时改为 `docker` 加 `local_exec`；不能把这项操作用于开发机原生 EDO 或共享生产环境。先约定该 E2E 的测试窗口，再按一次性的 `enable → verify → restore` 顺序运行：

```sh
bun run tools/rbac-acceptance/terminal-workflow.mjs --admin-file /private/tmp/edo-rbac-private/admin.json --manifest /private/tmp/edo-rbac-private/release-identities/manifest.json --out /private/tmp/edo-rbac-private/terminal-01 --phase enable
bun run tools/rbac-acceptance/terminal-workflow.mjs --admin-file /private/tmp/edo-rbac-private/admin.json --manifest /private/tmp/edo-rbac-private/release-identities/manifest.json --out /private/tmp/edo-rbac-private/terminal-01 --phase verify
bun run tools/rbac-acceptance/terminal-workflow.mjs --admin-file /private/tmp/edo-rbac-private/admin.json --manifest /private/tmp/edo-rbac-private/release-identities/manifest.json --out /private/tmp/edo-rbac-private/terminal-01 --phase restore
```

这里的清单来自本轮独立岗位身份准备，必须包含 `prefix`、`fixturePrefix`、`projects.DEFAULT`、`users.terminal_operator` 及 `created` 用户 UUID 白名单。终端账号须为本轮新建普通用户，服务端昵称精确为 `fixturePrefix + '-terminal_operator'`；随机用户名不要求带前缀。脚本每阶段核对真实账号 UUID、用户名、昵称和非超级管理员身份，管理员登录必须返回超级管理员，普通用户登录 ID 必须与清单一致。

文件目标只允许状态中随机生成的 `/tmp/rbac-terminal-<20 位十六进制>.txt` 及其 `.second` 文件，不能把私有状态改成其他真实文件路径后继续测试。`verify` 验证读、上传、编辑、覆盖所需的独立权限、旧摘要冲突及无副作用，并在同一已成功工作的主机终端连接上验证撤权关流。终端只执行固定的验收标记命令，输入输出不写入状态或报告。

`verify` 结束会尝试恢复专属账号的个人权限，主机能力仍必须显式执行 `restore`；失败不会伪装恢复成功。恢复前核对本轮最后应用的个人权限及主机名称、能力、环境关联，发现其他修改时保留现场，不覆盖外部变更。查看 `state.json` 的 `restored` 与失败标记确认结果；已完成的阶段不要当成幂等接口重复提交。两个无敏感内容的临时文件不通过任意 Shell 删除，留待独立 E2E 容器的维护者收尾处理。状态文件仍为 `600`，不包含凭据、Cookie 或终端内容。
