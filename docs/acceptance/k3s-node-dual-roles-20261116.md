# K3s 控制节点的 Worker 角色

用户指出集群列表主机只显示 Master，缺少 Worker。此前前后端均只依据显式的节点角色标签，遗漏 K3s 默认 Server 自带 Agent 的情况。

按 [K3s 架构文档](https://docs.k3s.io/architecture)，Server 与 Agent 均运行 kubelet；[无 Agent Server 文档](https://docs.k3s.io/advanced#running-agentless-servers-experimental)明确 `--disable-agent` 不注册 Node。本次仅当现有控制节点报告合法 K3s kubelet 版本时补充 Worker，不依赖主机名称、Pod 数量或历史安装配置，不修改集群标签或配置。角色表示已注册节点的职责，当前可达性、健康和调度限制仍分别展示。

后端复用现有 Kubernetes 版本解析库；前端采用相同的 K3s 版本识别边界，使集群主机标签、节点列表、详情和搜索保持一致。普通控制节点、缺少 Node/版本、其他发行版及格式错误的版本不自动补 Worker，显式双角色仍保留。

| 用例 | 结果 |
| --- | --- |
| K3s 稳定版、预发布版，缺少 worker 标签 | Master 与 Worker 同时展示 |
| 普通 Kubernetes、未知版本、无 Node | 保留真实已知角色，不按名称或 Pod 容量猜测 |
| 停止调度或带控制平面污点 | 角色保持独立，调度限制不冒充角色变化 |
| 独立接入且无 EDO 安装历史 | 当前 Node 和精确匹配主机的拓扑角色同步；新响应不再报告 K3s 时不沿用旧推断 |
| 前端列表、详情、搜索与异步切换 | 相关 24 条回归通过，缺失、撤权和迟到数据仍不回填角色 |

干净源码副本中的后端 NodeRoles/Topology 回归、前端类型检查和生产构建通过。后台回归使用回环 TLS 服务与临时 SQLite，不读取开发机 kubeconfig 或连接其他集群。
