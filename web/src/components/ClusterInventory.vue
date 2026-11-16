<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { ArrowUpRight, Link2, Pencil, Plus, RefreshCw, Search, Server } from 'lucide-vue-next'
import { useRoute, useRouter } from 'vue-router'
import client from '@/api/client'
import type { ClusterHost, InfrastructureCluster, InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage } from '@/api/resources'
import ClusterHostDrawer from '@/components/ClusterHostDrawer.vue'
import KubernetesClusterDrawer from '@/components/KubernetesClusterDrawer.vue'
import ClusterRbacDrawer from '@/components/ClusterRbacDrawer.vue'
import KubeHostRoleBadge from '@/components/KubeHostRoleBadge.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import ResourceRowActions from '@/components/ResourceRowActions.vue'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'
import ClusterConnectionScope from '@/components/ClusterConnectionScope.vue'
import ClusterConnectionBadge from '@/components/ClusterConnectionBadge.vue'
import { useAuthStore } from '@/stores/auth'
import { useResourceList } from '@/utils/resourceList'
import { hostsForCluster } from '@/utils/infrastructureRelations'
import { formatDateTime } from '@/utils/time'

const props = defineProps<{ clusters: InfrastructureCluster[]; hosts: InfrastructureHost[]; loading: boolean; error?: string; hostError?: string }>()
const emit = defineEmits<{ refresh: []; provision: []; host: [host: InfrastructureHost] }>()
const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const query = ref('')
const association = ref('all')
const associationOpen = ref(false)
const associationCluster = ref<InfrastructureCluster>()
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
const canManageResources = computed(() => auth.canAny(['kubernetes.node.read', 'kubernetes.workload.read', 'kubernetes.config.read', 'kubernetes.storage.read', 'kubernetes.network.read']))
const canCreateK3s = computed(() => auth.canAny(['cluster.create']) && auth.canAny(['cluster.execute']) && auth.canAny(['cluster.update']) && canReadHosts.value)
const filtered = computed(() => props.clusters.filter(cluster => {
  const linked = hostsForCluster(cluster, props.hosts)
  if (association.value !== 'all' && !associationKnown(cluster)) return false
  if (association.value === 'linked' && !linked.length || association.value === 'standalone' && linked.length) return false
  const search = query.value.trim().toLocaleLowerCase()
  return !search || [cluster.name, cluster.api_server, ...linked.map(host => `${host.name} ${host.address}`)].join(' ').toLocaleLowerCase().includes(search)
}))
const { selected, detailOpen, pagination, changePage, openDetails: reveal } = useResourceList(
  () => filtered.value,
  () => typeof route.query.cluster === 'string' ? route.query.cluster : '',
)
const connectionClusters = computed(() => {
  const { current, pageSize } = pagination.value
  const visible = filtered.value.slice((current - 1) * pageSize, current * pageSize)
  if (detailOpen.value && selected.value && !visible.some(cluster => cluster.id === selected.value?.id)) visible.push(selected.value)
  return visible
})
const columns = [
  { title: '集群名称', key: 'name', width: 220 },
  { title: 'API 地址', dataIndex: 'api_server', key: 'api_server', width: 270 },
  { title: '关联主机', key: 'hosts', width: 250 },
  { title: '默认命名空间', dataIndex: 'default_namespace', key: 'namespace', width: 150 },
  { title: '连接状态', key: 'status', width: 190 },
  { title: '操作', key: 'actions', width: 160, fixed: 'right' as const },
]

function openDetails(cluster: InfrastructureCluster) {
  reveal(cluster)
  void router.replace({ path: '/hosts', query: { view: 'clusters', cluster: cluster.id } })
}
function closeDetails() {
  detailOpen.value = false
  void router.replace({ path: '/hosts', query: { view: 'clusters' } })
}
function manage(cluster: InfrastructureCluster) {
  if (!canManageResources.value || !cluster.is_active) return
  void router.push({ path: '/kubernetes', query: { cluster: cluster.id, view: 'overview' } })
}
function openCluster(cluster: InfrastructureCluster) {
  if (canManageResources.value && cluster.is_active) manage(cluster)
  else openDetails(cluster)
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
function openHost(host: ClusterHost) {
  if (!canOpenHost(host)) return
  const full = props.hosts.find(item => item.id === host.id)
  if (full) emit('host', full)
}
function canOpenHost(host: ClusterHost) {
  return canReadHosts.value && !props.hostError && props.hosts.some(item => item.id === host.id)
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
watch(() => props.error, error => { if (error) { detailOpen.value = false; associationOpen.value = false } })
watch(() => props.clusters, clusters => {
  if (!editOpen.value || !editingCluster.value) return
  const current = clusters.find(item => item.id === editingCluster.value?.id)
  if (current) editingCluster.value = current
  else { editOpen.value = false; editingCluster.value = undefined }
})
watch(() => auth.canAny(['cluster.update']) && !props.error, allowed => { if (!allowed) editOpen.value = false })
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
      <div class="cluster-toolbar-actions"><a-button :loading="loading" @click="emit('refresh');refreshConnections()"><RefreshCw :size="15" />刷新</a-button><a-button v-if="canCreateK3s" type="primary" @click="emit('provision')"><Plus :size="15" />创建集群</a-button></div>
    </header>
    <a-alert v-if="error" class="cluster-load-error" type="error" show-icon message="集群列表加载失败" :description="error"><template #action><a-button size="small" @click="emit('refresh')">重试</a-button></template></a-alert>
    <div v-if="connectionError" class="connection-load-error" role="status"><span>连接状态读取失败：{{ connectionError }}</span><a-button size="small" type="link" :loading="connectionPending" @click="refreshConnections()">重试</a-button></div>
    <a-table :columns="columns" :data-source="filtered" :loading="loading" :pagination="pagination" :scroll="{ x: 1220 }" class="resource-list-table" row-key="id" size="middle" @change="changePage">
      <template #bodyCell="{ column, record }: { column: { key: string }, record: InfrastructureCluster }">
        <template v-if="column.key === 'name'"><button type="button" class="cluster-name" :aria-label="`${record.name} · ${canManageResources&&record.is_active?'集群总览':'集群详情'}`" @click="openCluster(record)"><span class="cluster-icon"><RuntimeBrandIcon kind="kubernetes" /></span><strong>{{ record.name }}</strong></button></template>
        <template v-else-if="column.key === 'api_server'"><code class="api-address">{{ record.api_server }}</code></template>
        <template v-else-if="column.key === 'hosts'"><span v-if="!associationKnown(record)" class="muted">{{ unknownAssociationLabel() }}</span><div v-else-if="hostsForCluster(record, hosts).length" class="linked-hosts"><button v-for="host in hostsForCluster(record, hosts)" :key="host.id" type="button" class="host-link" :disabled="!canOpenHost(host)" @click="openHost(host)"><Server :size="13" />{{ host.name }}<KubeHostRoleBadge :cluster="record.id" :host="host.id" /></button></div><span v-else class="muted">未关联主机</span></template>
        <template v-else-if="column.key === 'status'"><div class="cluster-status"><ClusterConnectionBadge :cluster="record" :connection="statuses[record.id]" /><span v-if="record.is_active && ['unreachable','restricted'].includes(statuses[record.id]?.status)" class="connection-reason">{{ statuses[record.id]?.message }}</span></div></template>
        <template v-else-if="column.key === 'actions'"><ResourceRowActions :more="auth.canAny(['cluster.update', 'cluster.execute','kubernetes.rbac.read','kubernetes.rbac.update'])"><a-button size="small" @click="openDetails(record)">详情</a-button><template #more><a-menu-item v-if="auth.canAny(['kubernetes.rbac.read','kubernetes.rbac.update'])" :disabled="!record.is_active" @click="openRbac(record)">集群授权</a-menu-item><a-menu-item v-if="auth.canAny(['cluster.update'])" @click="editCluster(record)"><Pencil :size="13" />编辑集群</a-menu-item><a-menu-item v-if="canEditAssociations" :disabled="!associationKnown(record)" @click="associate(record)">关联主机</a-menu-item><a-menu-item v-if="auth.canAny(['cluster.execute'])" :disabled="!record.is_active || checking.has(record.id)" @click="ping(record)">测试连接</a-menu-item><a-menu-item v-if="auth.canAny(['cluster.update'])" :disabled="Boolean(updating)" @click="toggle(record)">{{ record.is_active ? '停用集群' : '启用集群' }}</a-menu-item></template></ResourceRowActions></template>
      </template>
      <template #emptyText><a-empty :description="error ? '集群列表加载失败' : query || association !== 'all' ? '没有匹配的集群' : '暂无 Kubernetes 集群'"></a-empty></template>
    </a-table>

    <ResourceDetailDrawer :open="detailOpen" @close="closeDetails" @update:open="detailOpen = $event">
      <template #title><div v-if="selected" class="cluster-detail-title"><span class="cluster-icon"><RuntimeBrandIcon kind="kubernetes" /></span>{{ selected.name }}<a-tag>{{ selected.is_active ? '已启用' : '已停用' }}</a-tag><ClusterConnectionBadge v-if="selected.is_active" :cluster="selected" :connection="statuses[selected.id]" /></div></template>
      <template v-if="selected" #actions><a-space><a-button v-if="canManageResources" type="primary" :disabled="!selected.is_active" @click="manage(selected)">查看总览<ArrowUpRight :size="14" /></a-button><a-button v-if="auth.canAny(['cluster.update'])" @click="editCluster(selected)"><Pencil :size="14" />编辑</a-button><a-button v-if="auth.canAny(['cluster.execute'])" :loading="checking.has(selected.id)" :disabled="!selected.is_active" @click="ping(selected)">测试连接</a-button></a-space></template>
      <template v-if="selected">
        <section class="resource-detail-section"><h4>集群信息</h4><dl class="resource-facts"><div class="resource-facts-wide"><dt>API 地址</dt><dd><code>{{ selected.api_server }}</code></dd></div><div><dt>默认命名空间</dt><dd>{{ selected.default_namespace }}</dd></div><div><dt>接入方式</dt><dd>kubeconfig</dd></div><div v-if="selected.is_active && statuses[selected.id]?.message" class="resource-facts-wide"><dt>连接状态</dt><dd>{{ statuses[selected.id]?.message }}</dd></div><div><dt>更新时间</dt><dd>{{ formatDateTime(selected.updated_at, 'zh-CN') }}</dd></div><div v-if="connection[selected.id]?.version"><dt>Kubernetes</dt><dd>{{ connection[selected.id]?.version }}</dd></div></dl></section>
        <section class="resource-detail-section"><header><h4>关联主机 <span v-if="associationKnown(selected)" class="muted">{{ hostsForCluster(selected, hosts).length }}</span></h4><a-button v-if="canEditAssociations && associationKnown(selected)" size="small" @click="associate(selected)"><Link2 :size="14" />关联主机</a-button></header><p v-if="!associationKnown(selected)" class="muted">{{ unknownAssociationLabel() }}</p><div v-else-if="hostsForCluster(selected, hosts).length" class="detail-hosts"><button v-for="host in hostsForCluster(selected, hosts)" :key="host.id" type="button" :disabled="!canOpenHost(host)" @click="openHost(host)"><Server :size="17" /><span><strong>{{ host.name }}</strong><small>{{ host.address }}</small><KubeHostRoleBadge :cluster="selected.id" :host="host.id" /></span><a-tag v-if="!host.is_active">已停用</a-tag><ArrowUpRight v-if="canOpenHost(host)" :size="15" /></button></div><div v-else class="standalone-state"><span class="standalone-icon"><RuntimeBrandIcon kind="kubernetes" /></span><strong>未关联主机</strong><p>通过 kubeconfig 访问集群，可直接管理资源。</p></div></section>
      </template>
    </ResourceDetailDrawer>
    <ClusterHostDrawer v-model:open="associationOpen" :cluster="associationCluster" :clusters="clusters" :hosts="hosts" @saved="emit('refresh')" />
    <KubernetesClusterDrawer v-model:open="editOpen" :cluster="editingCluster" :can-test="auth.canAny(['cluster.execute'])" @saved="clusterSaved" />
    <ClusterRbacDrawer v-model:open="rbacOpen" :cluster="rbacCluster" />
  </div>
  </ClusterConnectionScope>
</template>

<style scoped>
.connection-load-error{display:flex;align-items:center;gap:8px;padding:8px 16px;color:var(--edo-danger,#d74747);font-size:12px}.cluster-status{display:flex;flex-direction:column;justify-content:center;gap:4px;height:42px;max-width:174px}.connection-reason{color:var(--edo-muted);font-size:12px;line-height:18px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.host-link:disabled{color:var(--edo-text);cursor:default}.host-link:disabled:hover{text-decoration:none}.detail-hosts>button:disabled{cursor:default}.detail-hosts>button:disabled:hover{color:var(--edo-text)}
.cluster-inventory{min-width:0}.cluster-toolbar{display:flex;align-items:center;gap:22px;padding:14px 16px;border-bottom:1px solid var(--edo-border)}.cluster-count{flex:none;min-width:88px}.cluster-count strong,.cluster-count small{display:block}.cluster-count small{margin-top:3px;color:var(--edo-muted);font-size:12px}.cluster-filters{display:flex;flex:1;gap:10px}.cluster-filters>.ant-input-affix-wrapper{max-width:340px}.cluster-filters>.ant-select{min-width:145px}.cluster-toolbar-actions{display:flex;gap:8px}.cluster-name{display:flex;align-items:center;gap:10px;padding:0;border:0;background:transparent;color:var(--edo-text);cursor:pointer;text-align:left}.cluster-name:hover strong{color:var(--edo-primary)}.cluster-icon{display:grid;place-items:center;width:28px;height:28px;flex:none;border-radius:5px;color:#326ce5;background:color-mix(in srgb,#326ce5 8%,var(--edo-surface))}.cluster-icon :deep(svg){width:20px;height:20px}.api-address{font-size:12px;color:var(--edo-muted);overflow-wrap:anywhere}.linked-hosts{display:flex;flex-wrap:wrap;gap:5px 12px}.host-link{display:inline-flex;align-items:center;gap:5px;padding:0;border:0;background:transparent;color:var(--edo-primary);font-size:12px;cursor:pointer}.host-link:hover{text-decoration:underline}.muted{color:var(--edo-muted);font-weight:400}.cluster-detail-title{display:flex;align-items:center;gap:10px}.cluster-load-error{margin:14px}.detail-hosts{display:grid;gap:0}.detail-hosts>button{display:flex;align-items:center;gap:12px;width:100%;padding:14px 0;border:0;border-bottom:1px solid var(--edo-border);color:var(--edo-text);background:transparent;text-align:left;cursor:pointer}.detail-hosts>button:hover{color:var(--edo-primary)}.detail-hosts>button>span{flex:1;min-width:0}.detail-hosts strong,.detail-hosts small{display:block}.detail-hosts small{margin-top:3px;color:var(--edo-muted);font-size:12px}.standalone-state{display:flex;align-items:center;flex-direction:column;padding:30px 16px;gap:8px}.standalone-icon{width:36px;height:36px;color:#326ce5}.standalone-icon :deep(svg){width:36px;height:36px}.standalone-state strong{font-size:14px}.standalone-state p{margin:0 0 8px;color:var(--edo-muted);font-size:12px}@media(max-width:1150px){.cluster-toolbar{flex-wrap:wrap;gap:12px}.cluster-filters{order:3;flex-basis:100%}.cluster-filters>.ant-input-affix-wrapper{max-width:none}.cluster-toolbar-actions{margin-left:auto}}@media(max-width:560px){.cluster-filters{flex-direction:column}.cluster-toolbar-actions .ant-btn{padding-inline:9px}.resource-facts{grid-template-columns:1fr}}
</style>
