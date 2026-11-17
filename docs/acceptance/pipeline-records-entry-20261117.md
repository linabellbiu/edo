# 流水线记录入口验收（2026-11-17）

## 需求

流水线编辑器底部缺少查看该流水线运行记录的入口；需要在不离开编辑器、不丢失未保存草稿的前提下查看记录。

## 实现

- 流水线编辑器（`web/src/views/PipelineEditorView.vue`）底部状态栏增加「流水线记录」链接，`target="_blank" rel="noopener noreferrer"`，地址由 `router.resolve` 生成：`/release-plans?view=runs&application=<应用>&workflow=<流水线>`。
- 入口仅在已加载应用流水线（非项目共用流水线模板）且拥有 `pipeline_run.read` 时出现；不复制名称、运行数据或凭据到 URL。
- 运行记录页（`web/src/views/DevOpsView.vue` + `web/src/utils/pipelineRunFilters.ts`）支持 `application` / `workflow` 作用域：请求携带 `application_id` / `workflow_id`（服务端 `GET /pipeline-runs` 已支持），筛选与分页保留作用域；页面顶部显示「仅显示：应用 · 流水线」和「查看全部记录」，空态区分「这条流水线还没有运行记录」。作用域参数只接受 1–64 位安全字符，非法值丢弃。
- 路由守卫对 `/release-plans?view=runs` 的权限为 `pipeline_run.read`，新增参数不改变侧栏高亮、面包屑和页面标题。

## 未改动

- 运行记录的列表列、详情抽屉、日志、重新执行、发布计划分组与分页逻辑不变。
- 未改动后端接口、数据库、执行与快照语义；新标签页沿用当前项目作用域（`localStorage` 中的项目）与原鉴权。

## 验证

- `cd web && node --test tests/pipelineRunList.test.mjs tests/pipeline-records-link.test.mjs`：9 项通过（作用域解析、请求参数、URL 保留与清除、编辑器链接属性、权限与身份固定）。
- `cd web && npx vue-tsc --noEmit --skipLibCheck` 通过；`npm run build`（含 vite 构建）通过。
- 全量 `node --test web/tests/*.test.mjs`：2038 项中 2018 项通过、20 项失败。失败集中在 `pipeline-log-focus.test.mjs`（10）、`pipeline-log-layout.test.mjs`（2）、`frontendConcurrencyRegression.test.mjs`（7）、`frontendFormsAcceptance.test.mjs`（1），均与日志视图、日志抽屉相关（`PipelineLogDrawer.vue` 已改用 `SessionPanel`，`LogsView` 读取逻辑也调整过，用例仍在按旧结构读取）；这些源文件与用例本次未修改，失败在本次改动前即存在，应由对应改动方补齐，不能当作本次功能已通过。
- 真实页面验收（构建镜像并更新本地 edo-local 后点击底部入口）尚未执行，本记录不将其记为已通过。
