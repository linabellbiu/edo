# 集群列表：移除默认命名空间列并新增查看集群操作（2026-11-17）

## 需求

集群列表删除「默认命名空间」列，并在操作列增加「查看集群」按钮。

## 实现

- `web/src/components/ClusterInventory.vue`：列定义移除 `默认命名空间`（含对应单元格、提示与样式），表格横向总宽同步由 1172 调整为 1110；集群名称仍为原地只读（可聚焦、点击/悬停查看完整名称），不因启用状态或权限变化导航。
- 操作列保留原有的编辑与更多菜单（集群授权、关联主机、测试连接、启用/停用、k3d 手动卸载指引），在最前面增加「查看集群」：跳转 `/kubernetes?cluster=<UUID>&view=overview`，进入该集群的 Kubernetes 总览工作区。
- 权限与导航菜单中的集群总览一致：具备 `kubernetes.node.read`、`kubernetes.workload.read`、`kubernetes.config.read`、`kubernetes.storage.read`、`kubernetes.network.read` 任一权限才显示；无权限时不渲染按钮也不发起导航。停用集群仍可进入，由工作区显示真实连接状态，不在列表层伪造可用性。
- 未改动后端接口、权限码或数据；默认命名空间仍保留在集群数据与接入/编辑抽屉中，仅列表不再重复展示。

## 验证

- `cd web && node --test tests/kubernetes-cluster-entry.test.mjs tests/kubernetes-cluster-edit.test.mjs tests/kubernetes-cluster-list-actions.test.mjs tests/clusterHostInteraction.test.mjs tests/kubernetes-rbac-entry.test.mjs tests/kubernetes-rbac.test.mjs tests/cluster-connection-status.test.mjs tests/clusterHostRelations.test.mjs tests/hosts-workspace-preview.test.mjs`：88 项通过。
- 新增用例：列序列为 `name/api_server/hosts/updated_at/actions`（无 namespace）、操作列固定在右侧；查看集群按 UUID 打开 Kubernetes 总览；缺少 Kubernetes 读取权限时按钮不显示且不能导航；集群名称本身仍无点击导航、无详情抽屉。
- 原有用例 `集群名称始终原地只读` 调整为允许明确的查看集群按钮：保留“名称不导航、无详情动作”的断言，并新增按钮权限与跳转断言，宿主渲染夹具改用真实 `ResourceRowActions`。
- `vue-tsc --noEmit --skipLibCheck` 通过。
- 真实页面点击尚未执行；部署后需在「基础设施 → 集群」实际点开核对按钮位置与跳转。
