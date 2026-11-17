# 部署镜像产物与容器名称简化（2026-11-17）

## 需求

Kubernetes 部署表单：`镜像` 字段改名为 `镜像产物`；删除 `容器名称` 输入，容器名称与镜像产物同名。

## 实现

- `DeploymentConfigurationFields.vue`：Kubernetes 部署镜像字段 label 由「镜像」改为「镜像产物」，仍只允许从上游构建产物中选择，非产物镜像继续行内提示。
- `PipelineDeployFields.vue`：删除「容器名称」输入；`desiredContainerName` 统一派生容器名：镜像产物名（取最后一段并规范化）→ 未配置镜像仓库时用上游构建产物名 → 应用名。镜像或构建产物变化时自动同步，不再保留单独填写值。
- `DeploymentPlanView.vue`：删除「容器名称」输入与必填校验（改为命名空间 + Deployment 名称）；`updateExecution`、`edit`、`save` 统一用 `containerFromExecution()` 从部署镜像产物派生；未配置镜像时回退 Deployment 名称，保证后端 `IsDNS1123Label` 校验与真实容器目标仍成立。Docker 部署的「容器名称（可选）」属于另一种部署方式，未改动。
- 未改后端字段、权限、快照和发布语义；已保存方案打开时优先按镜像产物回显容器名，未配置镜像才保留原值，避免一打开就改写目标。

## 验证

- `node --test tests/kubernetes-deployment-fields.test.mjs`：14 项通过，覆盖「容器名称始终等于镜像产物（显式改镜像后同步）」「命名空间/Deployment 默认值」「表单不再出现容器名称输入」「部署镜像 label 为镜像产物」。
- 相关子集 `kubernetes-*`、`deployment*`、`workflow-*`、编辑器作用域等 704 项全部通过。
- `vue-tsc --noEmit --skipLibCheck` 通过。
- 部署后需在页面确认字段与自动回填（浏览器扩展离线，本次未做点击验收）。
