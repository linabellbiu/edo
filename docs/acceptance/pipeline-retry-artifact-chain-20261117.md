# 重新执行沿用原运行制品的链路修复（PRA-08）

日期：2026-11-17

## 现象

本机 EDO 的 `edo-demo` 应用在 2026-11-17 17:25 有一条成功的流水线运行（Tag `v0.0.8`，提交 `18454b0d`），运行详情显示“已有制品：`edo-demo-web:v0.0.8`”。用户修改部署任务使用的集群后再点“重新执行”，弹窗显示“该次运行没有可复用的可用制品，将使用原 Commit 重新构建”，只能重新构建。用户据此怀疑改集群把制品弄丢了。

## 现场证据

真实数据库（`edo-local-postgres-1`，库 `edo`）只读核对：

- 运行链：第 20 次（`980405ee`，11-16 17:21，失败）→ 第 21 次（`3085baa0`，失败）→ 第 22 次（`770ee4ae`，失败）→ 第 23 次（`7046887c`，11-16 23:08，失败）→ 第 24 次（`b48f188c`，11-17 17:25，成功）；每条都是 `retry_of_id` 指向原失败运行，代码版本固定为 `refs/tags/v0.0.8` / `18454b0d14`。
- 第 23 次运行真实构建了两个制品并均可用：`c38e13d1`（api，构建节点 `build-image`）、`890ccc5c`（web，构建节点 `build-34680c1e…`），两者的 `pipeline_run_id` 都是第 23 次运行。
- 第 24 次运行是“使用原运行制品”的重试：`pipeline_node_runs` 中两个构建节点都是 `skipped`（“本次使用已选择的制品，跳过重新构建”），web 构建节点登记了制品 `890ccc5c`，运行记录 `artifact_id` 也是 `890ccc5c`；它自己没有新建任何制品。
- 制品 `890ccc5c` 状态仍为 `available`，生产构建 `5608fb7b` 为 `succeeded`，其构建方案是流水线内置构建，身份 `84ec3943…` 与当前流水线 `build-34680c1e…` 节点重算的内置方案身份完全一致（`uuid` v5，命名空间 `edo.workflow.build:<workflow>:<node>`）。
- 用户“换集群”的改动落在部署节点：当前流水线只剩 `docker-compose 部署` 与 `Kubernetes 部署` 两个部署节点，Kubernetes 部署改指向新集群；构建节点、构建方案身份和制品记录都没有变化。

## 根因

`ListRetryRunOptions` 通过 `listWorkflowArtifacts(ctx, application, workflow, runID)` 只收集 `artifacts.pipeline_run_id = 本次运行` 且对应 `build_runs.pipeline_run_id = 本次运行` 的制品。第 24 次运行自己没构建，制品属于第 23 次运行，因此选项为空，弹窗退化为只能重建；而运行详情用 `runs.artifact_id` 展示“已有制品”，两者矛盾。改集群本身没有删除制品，也不会让原制品失效。

重新执行时的写入校验（`retryRunSelection`）同样要求制品的 `pipeline_run_id` 与生产构建的 `pipeline_run_id` 都等于本次运行，所以即使页面强行提交也会被 `ErrRetryArtifactInvalid` 拒绝。

## 修复

- `internal/pipeline/manual_run.go`：新增 `pipelineRunReferencedArtifactIDs`，按“运行记录 `artifact_id` + 构建节点在成功或跳过时登记的 `artifact_id`”得到该运行固定使用过的制品（与 `pipelineimage.Load` 判断构建节点制品的口径一致）。`listWorkflowArtifacts` 在有运行上下文时同时接受这些制品，仍要求制品 `available`、生产构建成功、构建方案处于当前流水线启用范围内。
- `internal/pipeline/workflow.go`：`retryRunSelection` 接受“本次运行构建的制品”或“本次运行已固定使用的制品”，继续要求生产构建的 Ref/Commit 与本次运行固定版本一致；未固定使用的其他运行制品仍返回 `ErrRetryArtifactInvalid`（错误文案改为“已构建或已使用”）。
- `web/src/views/DevOpsView.vue`：重新执行弹窗的制品标签改为“该次运行使用的制品”，占位与空状态文案同步为“已构建或已使用 / 可复用”。

## 验证

- 新增回归用例 `TestChainedRetryKeepsArtifactPinnedByPreviousRetry`：第一次重试沿用原运行制品后部署失败，第二次重试仍必须提供同一制品、跳过构建、保留固定版本且不产生构建任务；未固定使用的其他运行制品仍被拒绝。该用例在修复前失败（`Artifacts:[]`），修复后通过。
- `go test ./internal/pipeline/` 全包通过；`go test -race ./internal/pipeline/ -run 'Retry|Manual'` 通过；`go test ./internal/httpapi/ ./internal/artifact/ ./internal/deployment/` 通过；`go vet ./internal/pipeline/` 与 `git diff --check` 通过。
- 前端：`node --test web/tests/pipelineRetry.test.mjs` 5 项通过（顺带修复该文件既有的 `applicationName` 缺失导致的渲染失败，并登记 `SessionPopoutLink` 桩组件）；`cd web && npm run build`（含 `vue-tsc --noEmit`）通过。
- 真实数据核对：按修复后的条件，第 24 次运行的制品集合为 `{890ccc5c}`，内置构建身份重算与 `build_plan_id` 一致，`manualArtifactBuildNodes` 可定位到 `build-34680c1e…`，因此该运行的重新执行会重新提供 `edo-demo-web:v0.0.8`。

## 边界

- 本次未重启、未重建、未部署本机运行中的 `edo-local-api-1`/`edo-local-web-1`，它们仍是修复前的构建；要在真实页面看到修复后的选项，需要重新构建镜像并更新容器。
- 未执行用户的任何真实流水线运行，未修改应用、流水线、部署方案、集群或制品记录，也未删除历史运行。
- “使用原运行制品”仍然只固定一次选择中的一个制品：多构建节点时可选的是紧跟部署之前的那个构建节点，其余前置构建节点按原行为跳过（本应用为 api 构建被跳过、web 制品被固定）。本轮没有把该选择扩展为多制品，也没有改变跳过范围。
