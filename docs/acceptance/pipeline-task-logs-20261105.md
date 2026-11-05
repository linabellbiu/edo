# EDO 流水线任务日志验收（2026-11-05）

范围：运行详情中每个任务的日志入口；复用既有运行查看权限，不执行新的业务构建、推送或部署。

| 用例 ID | 角色与前置 | 操作 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| PL-LOG-01 | 有运行查看权限，含并行构建及部署任务 | 逐个点击拓扑任务和任务表名称；用 Enter、空格打开任务 | 标题显示所选任务，HTTP 和 WebSocket 均按运行 ID、任务 ID 查询，关闭回到原入口 | 本机隔离页面通过：两个并行构建、一个部署任务；键盘打开及关闭通过 |
| PL-LOG-02 | 同一次运行的同名并行任务、超过一批的历史日志 | 分页读取并快速切换任务，模拟旧请求及旧连接迟到；再打开整条运行日志 | 不混入其他任务、其他运行或全局日志；切换重置游标；整条日志仍完整 | 自动回归通过：同名 ID 隔离、跨运行隔离、游标、205 条 WebSocket 历史、旧请求取消及迟到数据丢弃 |
| PL-LOG-03 | 未执行任务、未知节点及无权查看的项目 | 查询空日志和不存在节点，跨项目查询及撤权后继续推送 | 空任务明确暂无输出，未知节点 404 且不升级连接，项目及会话权限持续生效 | 后端回归通过：空日志、未知节点、跨项目及既有日志 WebSocket 授权边界 |

验证命令：

```sh
go test -race ./internal/pipeline ./internal/httpapi -run 'Test(NodeRunLogs|PipelineTaskLogs|PipelineRunLogs|BuildLogRedaction|PipelineLogs|ExecutionLogListRespectsDepartmentScope)' -timeout=180s
node --no-experimental-webstorage --test web/tests/pipeline-task-logs.test.mjs web/tests/pipeline-log-focus.test.mjs web/tests/pipeline-log-layout.test.mjs
cd web
npm run build
```

页面夹具：`web/tests/fixtures/pipeline-task-logs.html`，仅使用合成数据，不连接服务器。线上既有六任务运行的逐任务日志验收待发布后执行；本机证据不代表已发布。

实现使用现有 Vue 原生按钮、Gin/GORM 和日志 WebSocket，不引入依赖或数据库迁移。无任务 ID 的历史全局日志保留在整条运行入口，不按任务名猜测归属。
