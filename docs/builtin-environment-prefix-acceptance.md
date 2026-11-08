# 内置环境变量前缀验收

EDO 内置 Shell 环境变量统一使用 `EDO_` 前缀，页面、脚本示例及 Compose 默认引用使用 `${EDO_IMAGE_TAG}`。本次镜像 Tag 仍来自当前流水线的实际构建产物，不能由用户变量或外部镜像来源覆盖。

| 原名称 | 内置变量名 |
| --- | --- |
| `IMAGE_TAG` | `EDO_IMAGE_TAG` |
| `CI` | `EDO_CI` |
| `HOME` | `EDO_HOME` |
| `TMPDIR` | `EDO_TMPDIR` |
| `GOOS` | `EDO_GOOS` |
| `GOARCH` | `EDO_GOARCH` |

系统及工具链仍收到其要求的原生变量；已保存的 `${IMAGE_TAG}`、自定义 Tag 引用和运行快照保持有效，不改写业务仓库、配置正文或脚本。通知模板及 Dockerfile 的语言版本构建参数保持原有语法，它们不属于 Shell 环境变量目录。

| ID | 角色及前置 | 操作 | 预期 | 结果 |
| --- | --- | --- | --- | --- |
| ENV-PREFIX-01 | 可读取设置的用户 | 读取内置变量目录 | 所有 environment 项以 `EDO_` 开头，无运行值或密钥 | 通过：HTTP 隔离回归 |
| ENV-PREFIX-02 | 流水线编辑者 | 新建 Compose 部署并填写镜像 Tag 引用 | 默认 `${EDO_IMAGE_TAG}`；裸变量名和其他 EDO 内置变量不能作为 Tag 别名 | 通过：前端组件及正常保存接口 |
| ENV-PREFIX-03 | 当前流水线已有镜像产物 | 用新旧 Tag 引用匹配镜像，传入错误的同名用户变量 | 使用真实产物 Tag，固定 Digest；缺少产物或 Tag 歧义时拒绝 | 通过：Go 渲染及环境解析回归 |
| ENV-PREFIX-04 | Go 构建任务，单一 Linux 目标 | 生成构建执行环境 | EDO_GOOS/EDO_GOARCH 与 Go 原生变量相同，多架构时不伪造单一架构 | 通过：执行环境及保留变量回归 |
| ENV-PREFIX-05 | 自定义构建任务 | 在隔离临时工作区真实运行脚本 | EDO_CI、EDO_HOME、EDO_TMPDIR 可读，目录与原生变量相同，受控变量不可覆盖 | 通过：本地真实 Shell 执行及容器参数回归 |
| ENV-PREFIX-06 | 有重启、重建权限的用户，已绑定应用服务 | 执行冻结的重启及重建动作 | 注入 EDO_IMAGE_TAG，保留旧别名，并继续核验容器身份 | 通过：隔离数据库及模拟 Docker/SSH 回归 |

验证在本地隔离环境完成。前端在项目使用的 Node.js 24 下全部 1764 项测试通过，类型检查与生产构建通过；相关 Go 包及 HTTP 接口回归启用 `-race`。首次误用本机默认 Node.js 25 导致实验性全局 localStorage 与测试桩冲突，改用项目 Node.js 24 后全部通过。
