# EDO Kubernetes 工作区

集群与命名空间通过左侧可搜索的树选择。一个集群内可以同时勾选多个命名空间；勾选集群表示查看全部命名空间，取消全部选择后，命名空间资源列表提示先选择范围。搜索树节点只过滤展示，不会修改已勾选的范围。切换资源分类、浏览器返回及刷新通过 URL 保留当前集群和命名空间。

多选资源按各命名空间独立请求和分页，最多同时发送四个请求。加载更多只继续尚有下一页的命名空间，不重复请求已完成的范围；切换范围会取消旧请求并丢弃旧响应。统计仅累加命名空间资源，节点、命名空间、PV 和 StorageClass 等集群级资源不重复累加，节点 CPU、内存及全部已分配 Pod 用量不随筛选缩减。任一范围统计缺失时显示不可用或部分结果，不伪装为完整的零值。

整个 Kubernetes 工作区及关联 Pod 抽屉共用带竖线的表格和有底色的表头。就绪数量显示为紧凑徽标：绿色表示就绪且控制器已确认当前版本，橙色表示等待或更新，红色表示故障，灰色表示未知或已缩至零；更新数量仍可在悬浮或键盘聚焦提示中查看。操作按查看、修改、运行及删除区分颜色，原有权限、禁用状态、确认与审计保持一致。

树和表格使用主题令牌适配浅色及深色模式。窄屏下筛选树移到表格上方并限制高度，表格在自身区域内横向滚动，不撑宽整个页面。

## 资源表单与诊断

工作负载共用容器表单，支持 CPU/内存 requests 与 limits、镜像拉取策略、启动命令与参数、三种健康探针、Init/原生 Sidecar、镜像拉取 Secret 名称、emptyDir/ConfigMap/Secret/PVC 卷及挂载。Deployment 支持 RollingUpdate/Recreate、更新批次、最短就绪时间、版本保留数和进度超时。编辑容器时保留原有扩展资源、Secret 引用、探针附加项及未展开字段；未知卷类型保留原配置。输入数量沿用 Kubernetes Quantity（如 `128Mi`、`1Gi`），只读展示统一使用 MiB/GiB。表单先检查数量、请求与限制、探针、挂载和更新参数，最终仍由 Kubernetes API 校验；原有 UID/resourceVersion 校验、权限、确认与审计不变。

资源详情增加三个按需加载的诊断页签，切换关联 Pod 继续使用当前抽屉和返回栈：

- Service 的“真实端点”读取 `discovery.k8s.io/v1` EndpointSlice，展示实际地址、端口、Ready/Serving/Terminating、节点与目标 Pod；未知条件显示未知，手动维护的端点同样可见。不会把标签匹配结果当成已就绪端点。
- Deployment 的“版本历史”读取同命名空间 ReplicaSet，按 controller owner UID 验证归属，按模板（忽略控制器的 hash 标签）识别当前版本，并展示每个 ReplicaSet 的副本、镜像及实际归属 Pod。该入口只读，不提供绕过应用流水线的回滚发布。
- Pod 的“资源与诊断”使用 Metrics API 的真实容器采样绘制 CPU/内存用量、申请及限制对比图，并展示普通、Init、临时容器的状态、重启、上次退出与按真实时间排列的生命周期。当前用量每 30 秒刷新；不制造浏览器历史。缺失、过期、替换 UID 和旧容器实例的指标不显示为零，未配置限制显示空缺。

新增只读接口：`GET /kubernetes/clusters/:id/resources/:resource/:name/diagnostics?namespace=...&uid=...`，只支持 services/deployments/pods，继承对应的 network/workload 读取权限和项目范围，查询后再次验证 UID，禁止任意资源透传。集群凭据还需有读取 EndpointSlice、ReplicaSet、Pod 及 Pod metrics 的 Kubernetes RBAC 权限；拒绝读取时展示对应失败或部分结果，不自动扩大权限。分页查询上限 10000 条，超出明确提示部分结果。无数据库结构变更或历史业务数据迁移。

实现复用项目已使用的官方 client-go、Kubernetes API 类型/语义比较及 ECharts；未引入新运行依赖或额外监控组件。EndpointSlice 条件和 Deployment 模板识别参考 [Kubernetes EndpointSlice 文档](https://kubernetes.io/docs/concepts/services-networking/endpoint-slices/) 与 [Deployment 文档](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/)。
