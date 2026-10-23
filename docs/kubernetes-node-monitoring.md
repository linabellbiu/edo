# EDO Kubernetes 节点能力核查

2026-10-23：以用户提供的 Kuboard 节点详情为只读对照，集中核查节点列表、详情、监控、关联 Pod、事件、元数据与操作。

| 能力 | 行为与边界 |
| --- | --- |
| 筛选与导航 | 节点只按集群选择，可按名称、地址、状态和标签筛选；总览来源才显示返回总览；关闭抽屉保留列表位置。 |
| 当前用量 | 列表 CPU / 内存 / Pod 用量条保留；详情默认打开资源监控，显示绝对值、可分配量与比例。 |
| 历史监控 | 每 30 秒后台采集，Redis 每分钟保留最新真实采样，最多 4321 点、保留 3 天；支持 15 分钟、1 小时、6 小时、24 小时和 3 天。关闭浏览器仍采集，服务重启保留历史，断档不连线。 |
| 请求与限制 | 汇总全命名空间未结束 Pod，包含 Init、可重启 Sidecar、Pod 级资源与 Overhead。仅显示显式配置的限制，允许合计超配。未结束且正在删除的 Pod 仍计算，已结束记录只计 Pod 数量。 |
| 容量与系统信息 | 总容量与可分配量、临时存储、扩展资源、内核、架构、Kubelet、运行时、Pod CIDR、提供商、地址和调度状态。 |
| 健康状态 | Ready 与压力状态区分健康方向；显示原始原因、消息、心跳和状态变化时间。 |
| 关联资源 | 节点关联所有命名空间 Pod，支持在当前抽屉查看 Pod、日志和容器终端，再返回；节点缓存镜像单独只读分页。 |
| 事件 | 按资源 UID 读取，明确区分无事件与读取失败，事件页随刷新更新。 |
| 安全操作 | 保留标签/污点编辑、停/恢复调度、真实加入凭据与受控移除；写操作确认、权限和 UID/resourceVersion 校验保持不变。 |

实页验收补充：修复 Kubernetes 无事件返回 `null` 导致页面误报不可用；关联 Pod 创建时间使用真实元数据，小镜像使用 KiB / MiB 单位避免舍入成零。

## 指标与权限

- 只使用 Metrics API 提供的 CPU 和内存，不把节点容量冒充磁盘实际使用量；网络速率、磁盘用量及系统负载需要额外指标源，界面明确说明尚不提供这些采样。不自动安装 Prometheus、Grafana、Exporter 或修改集群。
- 无 Metrics Server、集群凭据被拒绝、采样过期、统计分页不完整时显示空缺与原因，不补零、不用过期样本冒充当前值。
- 采样任务按持久化项目/部门范围读取集群；历史查询再次检查当前项目的集群和节点 UID。接口受 `kubernetes.node.read` 控制，Pod 数量及请求/限制另需 `kubernetes.workload.read`；撤销权限后即使历史中存在，也不返回这些字段。
- 节点详情、列表与 YAML 不回传 K3s / RKE2 可能含加入令牌的 node-env 注解，原集群对象不受影响。
- Redis Key 隔离集群 ID 和节点 UID；同名重建不会拼接旧节点历史。Redis 不可用不影响其他功能，接口返回稳定中文提示，诊断写入日志。
- 无 SQL 表结构变更，无业务数据迁移；升级前历史为空，首次采样后自然累积。

## 依赖选择

复用现有 client-go、go-redis、ECharts；新增 Kubernetes 官方 `k8s.io/component-helpers/resource v0.36.2`，与现有 Kubernetes 依赖版本一致，Apache-2.0，持续随 Kubernetes 发布。它只在服务端计算 Pod 资源，不引入常驻组件或平台限制，现有 Kubernetes 依赖覆盖其主要传递依赖。相比自行实现调度资源计算，它覆盖 Init、可重启 Sidecar、Overhead 和原地资源调整；升级需与 client-go 同步并回归资源计算测试，调用封装在节点采集层，便于替换。不引入整个 kubectl 或 Kubernetes 单体依赖。

参考：[Metrics API 能力边界](https://kubernetes.io/docs/tasks/debug/debug-cluster/resource-metrics-pipeline/)、[官方资源计算库](https://pkg.go.dev/k8s.io/component-helpers/resource)。
