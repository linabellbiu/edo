# 一台主机只属于一个集群

沿用主机 Kubernetes 能力的唯一约束，新增后端归属校验，覆盖前端已禁用的占用主机选择。保留同一集群内控制平面兼任 Worker、一个集群多台主机、显式解除关联及官方卸载成功后的清理。k3d 从仍存在连接的安装记录识别安装宿主，不伪造物理节点关联。

| ID | 前置与角色 | 步骤 | 预期 |
| --- | --- | --- | --- |
| HO-01 | 有安装权限；主机已关联 A | 检测/预览原生 K3s 与 k3d 创建，再尝试覆盖到 B | 中文说明已有归属；不产生任务、锁或远程读取/执行 |
| HO-02 | 有安装权限；已预览空闲主机 | 确认前关联到另一集群，再执行；后台阶段再复查 | 阻止执行；本任务已导入的同一集群允许继续 |
| HO-03 | 有关联权限；主机属于 A | SetHosts、AppendHosts、主机编辑关联到 B；验证期间再制造归属变化 | 后端及事务均拒绝覆盖，A 的状态/版本保留，其他能力及原目标关联不变 |
| HO-04 | 已完成 k3d 安装 | 复用该宿主创建其他集群 | 按安装记录拒绝，不把宿主当作容器集群的物理节点 |
| HO-05 | 接口返回归属冲突 | 集群、主机及 K3s 接口分别处理带内部诊断的错误 | HTTP 409，固定中文，无内部诊断泄漏 |
| HO-06 | 正常安装/加入与卸载 | 回归既有 Worker、k3d、权限和卸载流程 | 同一集群正常使用；官方脚本及成功卸载后的移除规则保持有效 |

本轮不执行真实主机安装/卸载、不修改现有业务数据；只更新本地 API。前端原有禁用已关联主机的行为复用既有用例，不修改界面。

验证状态：HO-01 至 HO-06 的后端定向回归通过，保留 race；前端原有安装主机禁用、同机 Server/Worker 与卸载回归共 109 项通过。本轮未重新执行真实主机安装/卸载。本地 API 版本与容器核验另存更新回执。回归时发现主机套件原有发布目标断言直接比较含切片结构导致编译失败，改为完整结构深比较，保留全部字段断言。

后端命令：`go test -race ./internal/kube ./internal/host ./internal/k3s ./internal/httpapi -run 'Test(HostOwnership|ClusterHost|ClusterCreationWithHosts|AppendHosts|KubernetesHost|HostSaveReports|LocalExecRemoval|Worker|K3dHTTP|K3dWorker|K3dNormalization|K3sHTTP|Uninstall|K3sUninstall)' -count=1 -timeout 180s`。

前端命令：`node --test web/tests/k3s-provision.test.mjs web/tests/k3s-uninstall.test.mjs web/tests/k3s-progress.test.mjs`。
