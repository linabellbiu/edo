# 交接说明

本文件记录一轮改进的进展，供换到另一台机器（macOS）后继续。内容按"已完成 / 未验证 / 未完成"分开，避免把没跑过的东西当成通过。

## 已推送的提交（origin/main）

| 提交 | 内容 |
| --- | --- |
| `414284e` | Outbox 投递耗尽后收敛关联的流水线运行或发布记录；新增任务回收器；流水线日志改为原子配额预占 |
| `05bf2fb` | 默认管理员首次登录必须改密（迁移 `202611230082`）；本地主机终端文件接口禁止读写 EDO 密钥与数据库配置目录；监控规则与通知 Webhook 拒绝环回、私有、链路本地地址 |
| `bfcea59` | 发布执行期间周期性续期部署目标锁 |
| `d96ddb8` | 删除前端 SQL 变更审阅死代码（后端 `/sql-changes` 保留）；WebSocket 断线不再把授权判为不可用；初始管理员未改密时路由守卫引导至设置页 |
| `e56bb74` | `go.mod` 直接依赖整理（Go 工具链自动产生，版本未变） |
| `0293dc8` | 新增 `docs/README.md` 文档索引、`CONTRIBUTING.md`、`SECURITY.md`、`CHANGELOG.md` |
| `efea56e` | 修正 Cookie Secure 注释，使其与 compose 实际默认值一致 |

## 在 Windows 上已验证的内容

- Linux 目标交叉编译通过；`go build ./...` 在 `-mod=readonly` 下通过（与 CI 模式一致）。
- 以下包的单元测试在 Linux（WSL Ubuntu 22.04，Go 1.26.10）通过：`netguard`、`worker`、`outbox`、`account`、`monitor`、`notification`、`host`、`httpapi`、`deployment`、`database`。
- 以上测试没有覆盖：DevOpsView 相关的前端逻辑、真实 Docker 构建与部署、真实数据库迁移到 PostgreSQL 18。

## 已知失败（基线上即存在，不是本轮引入）

`internal/pipeline` 有 3 个 compose 测试失败，在未修改的 `038baf5` 上同样失败：

- `TestComposeInlineScriptFailurePreservesOrRestoresImages`
- `TestComposeFailureDetailsReachTaskLogsWithoutChangingFailureFlow`
- `TestComposeConfigurationUsesStablePathsAndOpaqueTransport`

报错为 `sh: 2: set: Illegal option -`。`/bin/sh` 在 Ubuntu 上是 dash。原因尚未定位到具体的那一行脚本，建议在 Mac（`/bin/sh` 为 bash 或 zsh 兼容 sh）上重新确认，并在确认后修复。

## 在 Windows 上被跳过的测试

- `TestWorkflowTaskNotificationDispatchesRealWebhookOnce`：保存指向 `127.0.0.1` 的 Webhook，会被 SSRF 校验拒绝。已加开关，默认跳过。需要端到端验证时设置 `EDO_TEST_ALLOW_LOOPBACK_WEBHOOK=1`。

## 未完成，需要在 macOS 上继续

1. **真实部署验证**：Windows 上 Docker Desktop 的 `dockerd` 在 WSL 中起不来（缺 `/run/desktop`），没有跑过 `docker compose up`。Mac 上的 Docker Desktop 应当能正常启动，请先执行：
   ```bash
   docker compose config
   docker compose up -d --build
   docker compose ps -a
   curl --fail http://localhost:8080/api/v1/health/ready
   ```
2. **确认 `sh -se` 与 dash 问题**：见上文"已知失败"。
3. **PostgreSQL 版本**：交付镜像仍为 `postgres:17.11-trixie`，未切换。PG 17 与 PG 18 的数据目录布局不同（PG 18 起 `VOLUME` 与 `PGDATA` 变更），直接切换会让已有安装看起来丢数据。
   - 已确认的上游事实：PG 18 最新补丁为 18.7（[发布说明](https://www.postgresql.org/docs/release/18.7/)）。
   - 未核实：`postgres:18.7-trixie` 标签是否存在。请在 Mac 上执行 `docker pull postgres:18.7-trixie` 确认。
   - 已选择的方案："新安装用 18，已有安装保留 17"。尚未实现。实现时需要在启动前检测数据目录版本，并在真实环境验证。
4. **卸载未使用依赖**：`@codemirror/lang-sql` 已无引用，但仍在 `web/package.json`。在 Mac 上执行：
   ```bash
   cd web && npm uninstall @codemirror/lang-sql && npm run build
   ```
5. **前端构建与类型检查**：`npm run build` 会先跑 `vue-tsc --noEmit`。本轮改动未在本机运行过它，需要在 Mac 上确认没有类型错误（尤其是 `web/src/stores/auth.ts` 与 `web/src/router/index.ts`）。
6. **前端测试**：`node --test web/tests/*.test.mjs`。删除了两个 SQL 审阅测试文件，并修改了 `pageCopy.test.mjs`，需要确认通过。
7. **DevOpsView 拆分**：`web/src/views/DevOpsView.vue` 仍为 2383 行 script，承载 5 个页面。未拆分，因为无法在本机运行 `vue-tsc` 和做可视化验证。
8. **长列表虚拟滚动**：未做。需要先确认生产环境最大的 Pod 数量与主机数量，再决定启用范围。
9. **`SECURITY.md` 占位符**：报告邮箱仍是 `<!-- TODO -->`，需要替换为真实地址。
10. **Windows 不可编译的包**：`internal/artifact`、`internal/sshdeploy`、`internal/host/terminal.go` 依赖 Unix 专用 API，在 Windows 上无法编译。这是项目平台限制，在 Mac 上不受影响。

## 决策记录

- SSRF 拦截：应用数据库测试连接与 Git 仓库地址保留内网访问（用户选择）。监控规则与通知 Webhook 拦截私网。
- SQL 变更审阅：只删前端，后端保留（用户选择）。
- Cookie Secure：默认仍为 `false`，以保证纯 HTTP 部署可以登录。HTTPS 反代部署需要手动改为 `true`。
- 默认管理员口令：保留 `123456`，但强制首次改密（用户选择）。
- 通知测试：默认跳过依赖环回 Webhook 的测试（用户选择）。
