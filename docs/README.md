# EDO 文档索引

本目录的文档分三类。阅读时先看这里，避免把历史验收记录当成当前行为。

## 当前有效

这些文档描述当前实现，改动相关行为时需要同步更新。

| 文档 | 内容 |
| --- | --- |
| [部署说明](deployment.md) | Compose、端口、数据库、备份与恢复 |
| [本地开发](development.md) | 工具安装、Mage 命令、本地启动排查 |
| [测试说明](testing.md) | 测试分层、真实数据库与容器测试的运行条件 |
| [应用与交付](unified-delivery.md) | 应用、流水线、构建与部署方案 |
| [流水线检查](delivery-checks.md) | 测试报告、变量与密钥、发布保护 |
| [流水线任务配置](pipeline-task-configuration.md) | 各类流水线任务的配置项 |
| [权限说明](access-control.md) | 组织、项目、角色与个人权限 |
| [Kubernetes](kubernetes.md) | 集群接入与资源管理 |
| [Kubernetes RBAC](kubernetes-rbac.md) | Kubernetes 相关权限模型 |
| [终端工作区](terminal-workspace.md) | 主机、容器与 Pod 终端及文件操作 |
| [对话助手](assistant.md) | 默认关闭的功能，由管理员配置启用 |
| [依赖说明](dependencies.md) | 第三方依赖的采用与评估记录 |
| [架构说明](refactor.md) | 运行组件、队列、数据库与安全边界 |

## 专题设计

这些文档说明某个专题的设计思路或迁移过程，用于理解背景。

| 文档 | 内容 |
| --- | --- |
| [迁移说明](migration.md) | 从旧系统迁入数据的步骤 |
| [性能说明](performance.md) | 性能测量与结论 |
| [前端框架迁移](frontend-vben-migration.md) | 前端外壳与基线选择 |

## 历史验收

`acceptance/` 目录和根目录中名称带 `acceptance` 的文件，都是某次验收的流水账，记录的是当时的提交、环境与结果。它们不代表当前行为，也不会随代码更新。

- [验收目录](acceptance/)：约 78 篇按主题与日期命名的验收记录。
- 根目录的 `*-acceptance*.md` 文件同样属于历史记录。

如果某份历史记录与上面的"当前有效"文档冲突，以"当前有效"文档和代码为准。
