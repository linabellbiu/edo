# Docker 与 compose 部署镜像仓库选择验收

范围：镜像仓库是已保存部署配置的一部分。唯一上游仓库仅预填编辑草稿，不自动保存；Docker、compose、compose_stack 新运行必须使用已保存的选择。配置保存在现有 execution JSON，无数据库结构迁移。

| ID | 角色与前提 | 操作与预期 | 结果与证据 |
| --- | --- | --- | --- |
| REG-01 | 管理员；上游有唯一可用仓库，部署尚未保存仓库 | 打开编辑显示上游建议；取消不保存；创建运行被拒绝 | 通过。真实组件浏览器检查取消后仍显示“已保存仓库：无”；前端保存校验及后端工作流/运行回归覆盖三个 Docker/compose 类型 |
| REG-02 | 管理员；有上游仓库 | 手动保存并重新打开；变更上游不覆盖选择，修改方案不污染历史运行 | 通过。浏览器保存 A 后切换上游到 B，重新打开仍显示 A；正常 HTTP 创建/修改/省略字段回归验证仓库 JSON 往返，后端验证快照固定 |
| REG-03 | 管理员；没有上游构建 | 选择仓库、镜像与实际 Tag 并保存 | 通过。前端状态测试及正常方案保存测试验证显式 ID；本次未执行外部镜像推拉 |
| REG-04 | 只读、跨项目、仓库停用或撤权 | 只读不补写；不可用或无权限时拒绝新运行，已入队运行执行前复查 | 通过。浏览器只读空值与修改保存提示；Go 回归包含跨项目、停用及创建后撤权；HTTP 稳定中文错误不回显内部诊断 |
| REG-05 | 已存在的历史运行 | 新快照缺少保存仓库时拒绝执行；无新标志的历史快照维持原有加载语义 | 通过。同一个隔离运行分别检查新/旧快照；实际脚本部署及配置重启回归通过 |

验证命令：

- `go test ./internal/pipeline ./internal/model -race -count=1 -timeout=240s`：通过。
- `go test ./internal/httpapi -race -count=1 -timeout=240s -run 'TestDeploymentRegistryErrorsHaveSafeClientResponses|TestDeploymentPlanExecutionConfigurationHTTP|TestWorkflowInlineDeploymentHTTPPersistsWithoutCreatingLibraryResources|TestReleasePlanExecution|TestCreateReleasePlanExecution'`：通过。
- Node 24 执行 `node --test web/tests/*.test.mjs`：1776 通过，0 失败。
- `npm run build`：通过。

浏览器验证使用独立会话和本地真实 Vue 组件，仓库接口与保存按钮采用隔离夹具，不代表生产页面或生产镜像推拉验收。临时页面、Vite 与浏览器会话已清理。本批不推送、不部署；GitHub CI 未运行。
