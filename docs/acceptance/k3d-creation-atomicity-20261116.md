# k3d 创建原子性与失败补偿

日期：2026-11-16

## 现场原因

本地失败安装 `8cca6f2a-4171-4ede-ba66-eeba45cc1d74`（测试23222）停止在 `k3d_create`，EDO 记录为 `failed`，`cluster_id` 为空，未接入集群列表。隔离 Docker 中仍有该任务的 Server、Agent 两个容器，状态均为 `Created`，启动时间为零。容器创建时间与安装记录吻合，`edo.dev/installation` 与完整任务 UUID 一致。

原流程使用官方 `--no-rollback`，后续失败也没有补偿删除；因此再次预检能发现 Docker 中的残留，却没有可供用户在 EDO 中选择的成功集群。另有一致性缺口：旧实现先注册集群，随后等待节点就绪，集群插入、任务状态与锁释放不在同一事务。

用户本次明确要求失败删除本次 k3d，取代此前失败保留资源的规则；原生 K3s 的安装与卸载流程不在本次修改范围内。

## 当前残留处理

仅对经过现场核对的上述失败任务执行已校验 SHA-256 的官方 k3d v5.9.0：

```text
k3d cluster delete edo-rtfg6ksbofhn5otg525elta5oq
```

执行使用 EDO 当前隔离 Docker 连接，独立临时 `KUBECONFIG`，未接触开发机已有集群配置，也未使用批量删除或自行编写容器、网络、卷删除命令。

结果核对：

- 两个从未启动的节点、一个独立网络和九个关联卷（镜像卷及八个节点匿名卷）全部消失。
- EDO API/Web、PostgreSQL、Redis、NATS、隔离构建器的容器身份和启动时间不变。
- 两个原有 EDO 集群记录不变；原失败任务及日志不变，没有重跑或改为成功。
- 执行前没有活动安装或 Job。

回执：`/tmp/edo-k3d-failed-cleanup-receipt.json`；官方输出：`/tmp/edo-k3d-failed-cleanup-command.log`。

## 官方能力与实现边界

使用官方 k3d CLI 及现有 Docker 连接能力，不引入另一套卸载器。依据 v5.9.0 的 `pkg/client/cluster.go`、`tools.go`、`cmd/cluster/clusterDelete.go` 和 `kubeconfig.go`：

- 没有节点容器时，官方 delete 可能退出成功但不删除仅剩的网络或卷；只有 tools 容器时也可能缺少网络信息。
- 容器、网络或卷删除失败可能仅输出警告。因此必须核验残留，不能只看退出码。
- 官方删除会按集群标签选择管理卷；执行前核对完整资源集合、创建前不存在的证明、安装身份及 Docker 身份，不能仅按名称删除。
- 不启用官方 debug/verbose 删除输出，避免其打印集群令牌；终端输出继续走脱敏与容量限制。
- 官方会清理 kubeconfig，需使用独立临时配置文件，不能使用开发机默认配置或 `/dev/null`。

## 实现

- 创建前将任务、主机、准确 Docker 身份、官方工具摘要及同名资源不存在的证明冻结至加密快照。旧任务不能仅凭现有名称或标签补造删除依据。
- 使用临时 kubeconfig 验证 API 和真实 Ready 节点，不提前插入可见集群；最终集群记录、任务成功状态与主机解锁在同一数据库事务提交。提交响应不明时先回读，成功记录不会被迟到失败回滚。
- 失败只调用官方 `k3d cluster delete`，清理后复核节点、网络、镜像卷和冻结的匿名卷；租约、目标身份变化、外部资源引用或任何项目已接入该集群时阻止删除。
- 首次补偿期间保持运行状态，用户继续看到阶段和真实日志；清理结果归档后才结束任务。失败保留原原因，清理未完成则保留主机锁并有界重试，不能冒称成功或直接重新创建。
- 失联进程尚可能创建资源时等待其原执行窗口结束再接管。快照损坏只用于阻止操作，不会降级成原生清锁路径。恢复使用本地已核验官方工具，不重新依赖 GitHub 联网。
- 复用已有 worker 并发配置，分别限制安装和恢复并发；逐条取得执行槽后重新计算租约，慢清理不阻塞其他独立主机。远程创建和清理共用 flock，安装前只读检测其能力，不自动安装依赖。

未增加第三方包或数据库表。资源归属与补偿边界属于 EDO 业务规则；安装和删除继续使用官方 k3d，连接使用现有 Docker CLI 环境，临时 API 验收复用官方 client-go。

## 验证

- `k3s`、`kube`、`cmd/edo` 全包通过（72.540 / 17.577 / 0.721 秒）：`/tmp/edo-k3d-atomic-all-final.log`。
- HTTP API 全包通过（125.484 秒）：`/tmp/edo-k3d-atomic-httpapi.log`；创建、接入、安装、卸载及权限边界保留。
- 原子性、并发恢复和失败补偿专项竞态测试通过（37.352 秒）：`/tmp/edo-k3d-atomic-race-final.log`；清理所有权专项竞态测试另通过（9.293 秒）：`/tmp/edo-k3d-cleanup-race.log`。
- 最后 flock 预检、shell 合同及原生预检相关测试通过（9.375 秒）：`/tmp/edo-k3d-flock-preflight.log`。
- 官方 k3d 创建和删除契约测试通过（1.245 秒）：`/tmp/edo-k3d-atomic-official.log`。使用官方 v5.9.0 Darwin ARM64 二进制，SHA-256 为 `fe106541d5d0a3f18debcd4d432a16f8c0ce3e6ddc06f8fbb6f696a122313e00`，与官方发布清单一致。全部 Docker 请求指向隔离内存服务，覆盖正常 Created 节点、匿名卷、退出 0 但容器/网络/卷残留、无节点、tools-only 和 kubeconfig 隔离。
- 数据库故障注入覆盖最终事务三个写点的回滚、迟到失败、失去租约、跨项目接入保护、快照损坏保锁和清理失败重试。

本次未通过重新安装真实集群来冒充端到端验收；当前历史残留的官方删除及后验是单独记录的实际操作。

源码提交：`9fa2e570f3ef897b5ce1bc157f9a10ed002bce8e`（`fix(k3d): make registration atomic and clean failed installations`）。

## 本地更新

- 从该冻结提交构建并更新 API：`edo-api:local-9fa2e57`，容器 `3ddcd11847d2`，实际程序版本 `EDO 9fa2e57`。
- 当前 Web `edo-web:local-857e624` 未重建。数据库、Redis、NATS、构建器的身份及启动时间不变；原数据卷和配置保留。
- 更新前无活动任务；更新前后流水线状态、日志和安装记录摘要一致，未重跑任何安装。
- 本地 Web 入口的 `/healthz`、`/api/v1/health/ready` 均返回 HTTP 200，数据库、Redis、NATS、构建器均就绪。
- 回执：`/tmp/edo-k3d-atomic-local-receipt.json`；构建及更新日志：`/tmp/edo-k3d-atomic-api-build.log`、`/tmp/edo-k3d-atomic-local-update.log`。
- 仅提交及更新本地，未推送远端；其他会话未提交的拓扑与前端修改未纳入本批提交或构建。
