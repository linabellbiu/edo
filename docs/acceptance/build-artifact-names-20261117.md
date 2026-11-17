# 构建产物名称静态检查与部署镜像选择

日期：2026-11-17

## 需求

Kubernetes 部署阶段需要管理上游构建产物名称：镜像应从上游构建产物里选择，而不是手填一个可能与构建不一致的名字。为此 EDO 在构建配置阶段静态解析并校验产物名称，并把结果提供给部署阶段。

## 静态解析与检查

后端新增 `internal/pipeline/workflow_images.go`：

- 镜像构建（Dockerfile 方案）：产物名称取任务的镜像名称，留空时与应用名一致；名称必须是合法的仓库名称（小写字母、数字、点、下划线和连字符，不能带 Tag / Digest，最多 255 字符），含运行期变量的名称在执行阶段再校验。
- 自定义构建（脚本方案）：产物名称来自“暂存构建物”的命名输出；名称需符合字母或下划线开头、最多 32 位的变量命名规则。
- 多个构建任务的产物名称不能重复：镜像名称重复或自定义构建产物名称重复都会报错，避免部署时无法区分上游产物。
- 没有镜像仓库的 Dockerfile 任务按本地镜像执行，不视为错误；没有命名输出的脚本构建只保存单个文件制品，由部署任务按上游任务引用。

检查结果通过 `build_image_name_invalid`、`build_image_name_duplicate`、`build_artifact_name_invalid`、`build_artifact_name_duplicate` 进入流水线问题列表：草稿可以保存，保存并启用时拦截。

## 部署阶段

- `WorkflowResult` 增加 `build_images`（任务、产物名称、Tag、镜像仓库）与 `build_artifacts`（自定义构建产物名称）。
- 流水线编辑器的部署阶段与前端工具 `workflowBuildImageNames` 使用同一套解析规则列出上游镜像产物：Kubernetes 部署的“镜像”字段优先列出这些名称（附构建任务名），再补充镜像仓库已有镜像与已保存旧值；选中的镜像不在上游产物里时给出行内提示。
- 选择镜像后容器名称默认跟随镜像名称（用户改过则保持原值）。

## 验证

- `go test ./internal/pipeline -run 'WorkflowBuildProduct|WorkflowValidationSurfacesBuildProduct'`：名称解析、非法名称、跨任务重复、流水线校验上报。
- `go test ./internal/pipeline ./internal/httpapi ./internal/assistant` 通过。
- `node --test web/tests/workflow-build-images.test.mjs`：镜像名称解析、方案库构建识别、重复去重、容器名称规范化。
- 前端全量 2045 项：2025 通过，20 项为日志抽屉既有失败（与本次无关）；`npm --prefix web run build` 通过。
