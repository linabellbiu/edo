<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { Link2, Plus, RefreshCw, Search, Server } from 'lucide-vue-next'
import { useRoute } from 'vue-router'
import client from '@/api/client'
import type { InfrastructureCluster, InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage } from '@/api/resources'
import ClusterHostDrawer from '@/components/ClusterHostDrawer.vue'
import KubernetesClusterDrawer from '@/components/KubernetesClusterDrawer.vue'
import ClusterRbacDrawer from '@/components/ClusterRbacDrawer.vue'
import KubeHostRoleBadge from '@/components/KubeHostRoleBadge.vue'
import ResourceRowActions from '@/components/ResourceRowActions.vue'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'
import ClusterConnectionScope from '@/components/ClusterConnectionScope.vue'
import ClusterConnectionBadge from '@/components/ClusterConnectionBadge.vue'
import { useAuthStore } from '@/stores/auth'
import { useResourceList } from '@/utils/resourceList'
import { hostsForCluster } from '@/utils/infrastructureRelations'
import { formatDateTime } from '@/utils/time'

const props = defineProps<{ clusters: InfrastructureCluster[]; hosts: InfrastructureHost[]; loading: boolean; error?: string; hostError?: string }>()
const emit = defineEmits<{ refresh: []; provision: [] }>()
const auth = useAuthStore()
const route = useRoute()
const query = ref('')
const association = ref('all')
const associationOpen = ref(false)
const associationCluster = ref<InfrastructureCluster>()
const createOpen = ref(false)
const editOpen = ref(false)
const editingCluster = ref<InfrastructureCluster>()
const rbacCluster=ref(''),rbacOpen=ref(false)
function openRbac(cluster:InfrastructureCluster){if(!auth.canAny(['kubernetes.rbac.read','kubernetes.rbac.update']))return;rbacCluster.value=cluster.id;rbacOpen.value=true}
const checking = reactive(new Set<string>())
const connectionRevision = ref(0)
const updating = ref('')
let mounted = true
const connection = reactive<Record<string, { ok: boolean; version?: string; error?: string }>>({})
const canReadHosts = computed(() => auth.canAny(['cluster.read', 'deployment.read']))
const canEditAssociations = computed(() => auth.canAny(['cluster.update']) && canReadHosts.value && !props.hostError && !props.error)
const canFilterAssociations = computed(() => canReadHosts.value && !props.hostError && props.clusters.every(cluster => Array.isArray(cluster.hosts)))
const canCreateK3s = computed(() => auth.canAny(['cluster.create']) && auth.canAny(['cluster.execute']) && auth.canAny(['cluster.update']) && canReadHosts.value)
const filtered = computed(() => props.clusters.filter(cluster => {
  const linked = hostsForCluster(cluster, props.hosts)
  if (association.value !== 'all' && !associationKnown(cluster)) return false
  if (association.value === 'linked' && !linked.length || association.value === 'standalone' && linked.length) return false
  const search = query.value.trim().toLocaleLowerCase()
  return !search || [cluster.name, cluster.api_server, ...linked.map(host => `${host.name} ${host.address}`)].join(' ').toLocaleLowerCase().includes(search)
}))
const { pagination, changePage } = useResourceList(
  () => filtered.value,
  () => typeof route.query.cluster === 'string' ? route.query.cluster : '',
)
const connectionClusters = computed(() => {
  const { current, pageSize } = pagination.value
  const visible = filtered.value.slice((current - 1) * pageSize, current * pageSize)
  return visible
})
const columns = [
  { title: '集群名称', key: 'name', width: 210 },
  { title: 'API 地址', dataIndex: 'api_server', key: 'api_server', width: 280 },
  { title: '关联主机', key: 'hosts', width: 250 },
  { title: '默认命名空间', key: 'namespace', width: 150 },
  { title: '最近更新', key: 'updated_at', width: 170 },
  { title: '操作', key: 'actions', width: 112, fixed: 'right' as const },
]

function openCreate() {
  if (!auth.canAny(['cluster.create']) || props.error) return
  createOpen.value = true
}
function clusterCreated() {
  emit('refresh')
}
function associate(cluster: InfrastructureCluster) {
  if (!canEditAssociations.value || !associationKnown(cluster)) return
  associationCluster.value = cluster
  associationOpen.value = true
}
function editCluster(cluster: InfrastructureCluster) {
  if (!auth.canAny(['cluster.update']) || props.error) return
  const current = props.clusters.find(item => item.id === cluster.id)
  if (!current) return
  editingCluster.value = current
  editOpen.value = true
}
function clusterSaved(cluster: InfrastructureCluster) {
  if (cluster.id !== editingCluster.value?.id) return
  delete connection[cluster.id]
  emit('refresh')
}
function associationKnown(cluster: InfrastructureCluster) {
  return canReadHosts.value && !props.hostError && Array.isArray(cluster.hosts)
}
function unknownAssociationLabel() {
  return props.hostError ? '主机关联读取失败' : !canReadHosts.value ? '无主机查看权限' : '暂无法查看关联'
}
async function ping(cluster: InfrastructureCluster) {
  if (checking.has(cluster.id) || !cluster.is_active || !auth.canAny(['cluster.execute'])) return
  checking.add(cluster.id)
  try {
    const { data } = await client.post<{ version: string }>(`/kubernetes/clusters/${encodeURIComponent(cluster.id)}/ping`)
    if (!mounted) return
    connection[cluster.id] = { ok: true, version: data.version }
    message.success(`${cluster.name} 连接正常`)
  } catch (error) {
    if (!mounted) return
    const detail = apiErrorMessage(error)
    connection[cluster.id] = { ok: false, error: detail }
    message.error(detail)
  } finally {
    checking.delete(cluster.id)
    connectionRevision.value++
  }
}
async function toggle(cluster: InfrastructureCluster) {
  if (updating.value || !auth.canAny(['cluster.update'])) return
  updating.value = cluster.id
  try {
    await client.patch(`/kubernetes/clusters/${encodeURIComponent(cluster.id)}/status`, { active: !cluster.is_active })
    if (!mounted) return
    delete connection[cluster.id]
    message.success(cluster.is_active ? '集群已停用' : '集群已启用')
    emit('refresh')
  } catch (error) {
    if (mounted) message.error(apiErrorMessage(error))
  } finally {
    updating.value = ''
  }
}
watch(canFilterAssociations, allowed => { if (!allowed) association.value = 'all' })
watch(() => props.error, error => { if (error) { createOpen.value = false; associationOpen.value = false } })
watch(() => props.clusters, clusters => {
  if (!editOpen.value || !editingCluster.value) return
  const current = clusters.find(item => item.id === editingCluster.value?.id)
  if (current) editingCluster.value = current
  else { editOpen.value = false; editingCluster.value = undefined }
})
watch(() => auth.canAny(['cluster.update']) && !props.error, allowed => { if (!allowed) editOpen.value = false })
watch(() => auth.canAny(['cluster.create']) && !props.error, allowed => { if (!allowed) createOpen.value = false })
onBeforeUnmount(() => { mounted = false })
</script>

<template>
  <ClusterConnectionScope :clusters="connectionClusters" :revision="connectionRevision" :enabled="!error" v-slot="{ statuses, error: connectionError, pending: connectionPending, refresh: refreshConnections }">
    <div class="cluster-inventory vben-card">
      <header class="cluster-toolbar">
        <div class="cluster-count"><strong>全部集群</strong><small>{{ filtered.length }} 个集群</small></div>
        <div class="cluster-filters">
          <a-input v-model:value="query" allow-clear placeholder="搜索集群、API 地址或主机" aria-label="搜索集群"><template #prefix><Search :size="15" /></template></a-input>
          <a-select v-model:value="association" :disabled="!canFilterAssociations" aria-label="按主机关联筛选" :options="[{ label: '全部集群', value: 'all' }, { label: '已关联主机', value: 'linked' }, { label: '未关联主机', value: 'standalone' }]" />
        </div>
        <div class="cluster-toolbar-actions">
          <a-button :loading="loading" @click="emit('refresh');refreshConnections()"><RefreshCw :size="15" />刷新</a-button>
          <a-button v-if="canCreateK3s" @click="emit('provision')"><Plus :size="15" />创建集群</a-button>
          <a-button v-if="auth.canAny(['cluster.create'])" type="primary" :disabled="Boolean(error)" @click="openCreate"><Link2 :size="15" />接入集群</a-button>
        </div>
      </header>
      <a-alert v-if="error" class="cluster-load-error" type="error" show-icon message="集群列表加载失败" :description="error"><template #action><a-button size="small" @click="emit('refresh')">重试</a-button></template></a-alert>
      <div v-if="connectionError" class="connection-load-error" role="status"><span>连接状态读取失败：{{ connectionError }}</span><a-button size="small" type="link" :loading="connectionPending" @click="refreshConnections()">重试</a-button></div>
      <a-table :columns="columns" :data-source="filtered" :loading="loading" :pagination="pagination" :scroll="{ x: 1172 }" :row-class-name="(record: InfrastructureCluster) => route.query.cluster === record.id ? 'resource-selected' : ''" table-layout="fixed" class="cluster-table resource-list-table" row-key="id" size="middle" @change="changePage">
        <template #bodyCell="{ column, record }: { column: { key: string }, record: InfrastructureCluster }">
          <template v-if="column.key === 'name'">
            <div class="cluster-identity">
              <span class="cluster-icon" aria-hidden="true"><RuntimeBrandIcon kind="kubernetes" /></span>
              <div class="cluster-identity-copy">
                <div class="cluster-name-line">
                  <a-tooltip :title="record.name" :trigger="['hover','focus','click']">
                    <strong class="cluster-name" tabindex="0">{{ record.name }}</strong>
                  </a-tooltip>
                  <ClusterConnectionBadge compact :cluster="record" :connection="statuses[record.id]" />
                </div>
                <div class="cluster-meta"><span>kubeconfig</span><span v-if="connection[record.id]?.version">{{ connection[record.id]?.version }}</span></div>
              </div>
            </div>
          </template>
          <template v-else-if="column.key === 'api_server'">
            <a-tooltip :title="record.api_server" :trigger="['hover','focus','click']">
              <code class="api-address" tabindex="0" :aria-label="`API 地址：${record.api_server}`">{{ record.api_server }}</code>
            </a-tooltip>
          </template>
          <template v-else-if="column.key === 'hosts'">
            <span v-if="!associationKnown(record)" class="muted">{{ unknownAssociationLabel() }}</span>
            <div v-else-if="hostsForCluster(record, hosts).length" class="linked-host-summary">
              <a-popover :trigger="['hover','focus','click']">
                <template #content>
                  <div class="cluster-host-popover" role="list" tabindex="0" aria-label="关联主机完整信息">
                    <div v-for="host in hostsForCluster(record, hosts)" :key="host.id" class="cluster-host-info" role="listitem"><span><strong>{{ host.name }}<small v-if="!host.is_active">（已停用）</small></strong><code>{{ host.address || '—' }}</code></span><KubeHostRoleBadge :cluster="record.id" :host="host.id" /></div>
                  </div>
                </template>
                <button type="button" class="host-summary-trigger" :aria-label="`查看 ${hostsForCluster(record, hosts).length} 台关联主机的信息`"><Server :size="13" aria-hidden="true" /><span class="host-name-text">{{ hostsForCluster(record, hosts)[0]!.name }}</span><small v-if="!hostsForCluster(record, hosts)[0]!.is_active">停用</small><KubeHostRoleBadge :cluster="record.id" :host="hostsForCluster(record, hosts)[0]!.id" /><span v-if="hostsForCluster(record, hosts).length > 1" class="host-count">+{{ hostsForCluster(record, hosts).length - 1 }}</span></button>
              </a-popover>
              <a-tooltip :title="hostsForCluster(record, hosts)[0]!.address" :trigger="['hover','focus','click']"><code class="linked-host-address" tabindex="0">{{ hostsForCluster(record, hosts)[0]!.address || '—' }}</code></a-tooltip>
            </div>
            <span v-else class="muted">未关联主机</span>
          </template>
          <template v-else-if="column.key === 'namespace'">
            <a-tooltip :title="record.default_namespace" :trigger="['hover','focus','click']"><span class="namespace-label" tabindex="0">{{ record.default_namespace }}</span></a-tooltip>
          </template>
          <template v-else-if="column.key === 'updated_at'"><time class="cluster-updated" :datetime="record.updated_at">{{ record.updated_at ? formatDateTime(record.updated_at, 'zh-CN') : '—' }}</time></template>
          <template v-else-if="column.key === 'actions'">
            <ResourceRowActions :more="auth.canAny(['cluster.update', 'cluster.execute','kubernetes.rbac.read','kubernetes.rbac.update'])">
              <a-button v-if="auth.canAny(['cluster.update'])" size="small" :disabled="Boolean(error)" @click="editCluster(record)">编辑</a-button>
              <template #more>
                <a-menu-item v-if="auth.canAny(['kubernetes.rbac.read','kubernetes.rbac.update'])" :disabled="!record.is_active" @click="openRbac(record)">集群授权</a-menu-item>
                <a-menu-item v-if="canEditAssociations" :disabled="!associationKnown(record)" @click="associate(record)">关联主机</a-menu-item>
                <a-menu-item v-if="auth.canAny(['cluster.execute'])" :disabled="!record.is_active || checking.has(record.id)" @click="ping(record)">测试连接</a-menu-item>
                <a-menu-item v-if="auth.canAny(['cluster.update'])" :disabled="Boolean(updating)" @click="toggle(record)">{{ record.is_active ? '停用集群' : '启用集群' }}</a-menu-item>
              </template>
            </ResourceRowActions>
          </template>
        </template>
        <template #emptyText><a-empty :description="error ? '集群列表加载失败' : query || association !== 'all' ? '没有匹配的集群' : '暂无 Kubernetes 集群'" /></template>
      </a-table>
      <ClusterHostDrawer v-model:open="associationOpen" :cluster="associationCluster" :clusters="clusters" :hosts="hosts" @saved="emit('refresh')" />
      <KubernetesClusterDrawer v-model:open="createOpen" :can-test="auth.canAny(['cluster.execute'])" @created="clusterCreated" />
      <KubernetesClusterDrawer v-model:open="editOpen" :cluster="editingCluster" :can-test="auth.canAny(['cluster.execute'])" @saved="clusterSaved" />
      <ClusterRbacDrawer v-model:open="rbacOpen" :cluster="rbacCluster" />
    </div>
  </ClusterConnectionScope>
</template>

<style scoped>
.cluster-inventory{min-width:0}
.cluster-toolbar{display:flex;align-items:center;gap:18px;padding:10px 14px;border-bottom:1px solid var(--edo-border)}
.cluster-count{flex:none;min-width:78px}.cluster-count strong,.cluster-count small{display:block}.cluster-count small{margin-top:2px;color:var(--edo-muted);font-size:12px}
.cluster-filters{display:flex;min-width:0;flex:1;gap:10px}.cluster-filters>.ant-input-affix-wrapper{max-width:320px}.cluster-filters>.ant-select{min-width:140px}
.cluster-toolbar-actions{display:flex;flex-shrink:0;gap:8px}.cluster-load-error{margin:14px}
.connection-load-error{display:flex;align-items:center;gap:8px;padding:8px 14px;color:var(--edo-danger,#d74747);font-size:12px}
.cluster-table :deep(.ant-table-thead>tr>th){height:40px;padding:9px 12px;border-right:1px solid var(--edo-border);background:var(--edo-surface-soft);color:var(--edo-text)}
.cluster-table :deep(.ant-table-tbody>tr:not(.ant-table-expanded-row)>td){height:64px;padding:8px 12px;border-right:1px solid var(--edo-border);line-height:20px}
.cluster-table :deep(.ant-table-tbody>tr:last-child>td){border-bottom:1px solid var(--edo-border)}
.cluster-identity{display:flex;min-width:0;align-items:center;gap:8px}.cluster-identity-copy{min-width:0;flex:1}.cluster-name-line{display:flex;min-width:0;align-items:center}
.cluster-name{display:block;min-width:0;max-width:calc(100% - 24px);overflow:hidden;color:var(--edo-text);font:inherit;font-weight:600;text-overflow:ellipsis;white-space:nowrap;cursor:text}
.cluster-icon{display:grid;place-items:center;width:28px;height:28px;flex:none;color:#326ce5}.cluster-icon :deep(svg){width:20px;height:20px}
.cluster-meta{display:flex;min-width:0;gap:8px;margin-top:2px;overflow:hidden;color:var(--edo-muted);font-size:12px;line-height:18px;white-space:nowrap}
.api-address{display:block;max-width:100%;overflow:hidden;color:var(--edo-text);font-family:var(--font-mono,monospace);font-size:12px;line-height:20px;text-overflow:ellipsis;white-space:nowrap;cursor:text}
.namespace-label{display:inline-block;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;cursor:text}
.host-summary-trigger{display:flex;min-width:0;max-width:100%;align-items:center;gap:5px;padding:0;border:0;background:transparent;color:var(--edo-text);font:inherit;text-align:left;cursor:help}.host-summary-trigger>svg{flex-shrink:0;color:var(--edo-muted)}
.host-name-text{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.host-summary-trigger small,.host-count{flex-shrink:0;color:var(--edo-muted);font-size:12px}.host-summary-trigger :deep(.host-node-roles){flex-shrink:0;flex-wrap:nowrap;margin:0}.host-summary-trigger :deep(.ant-tag){margin:0;font-size:11px;line-height:16px}
.linked-host-address{display:block;max-width:100%;overflow:hidden;margin-top:2px;color:var(--edo-muted);font-size:12px;line-height:18px;text-overflow:ellipsis;white-space:nowrap;cursor:text}
.cluster-host-popover{display:grid;gap:0;max-width:min(480px,80vw);max-height:280px;overflow:auto}.cluster-host-info{display:flex;align-items:center;gap:10px;min-width:0;padding:8px 0;border-bottom:1px solid var(--edo-border);color:var(--edo-text)}.cluster-host-info:last-child{border-bottom:0}.cluster-host-info>span{min-width:0;flex:1}.cluster-host-info strong,.cluster-host-info code{display:block;overflow-wrap:anywhere}.cluster-host-info code,.cluster-host-info small{color:var(--edo-muted);font-size:12px}
.cluster-updated{color:var(--edo-muted);font-size:12px;white-space:nowrap}.muted{color:var(--edo-muted)}
.cluster-name:focus-visible,.api-address:focus-visible,.namespace-label:focus-visible,.linked-host-address:focus-visible,.host-summary-trigger:focus-visible,.cluster-host-popover:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px;border-radius:3px}
@media(max-width:1200px){.cluster-toolbar{flex-wrap:wrap;gap:10px}.cluster-filters{order:3;flex-basis:100%}.cluster-filters>.ant-input-affix-wrapper{max-width:none}.cluster-toolbar-actions{margin-left:auto}}
@media(max-width:560px){.cluster-toolbar{padding:10px}.cluster-filters{flex-direction:column}.cluster-toolbar-actions{flex-wrap:wrap}.cluster-toolbar-actions .ant-btn{padding-inline:9px}}
</style>
