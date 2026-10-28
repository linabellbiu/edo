<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { ChevronRight, FileText, RefreshCw, Search, TerminalSquare } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { type HostCapabilityStatus, type InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import ContainerLogDrawer from '@/components/ContainerLogDrawer.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'
import TerminalDrawer from '@/components/TerminalDrawer.vue'
import { useAuthStore } from '@/stores/auth'
import { containerState, filterRuntimeContainers, runtimeContainerName, runtimeMatches, runtimeStateTone, stoppedContainer, type ContainerFilter } from '@/utils/runtimeResources'

type RuntimeKind = 'docker' | 'kubernetes'
interface DockerEndpoint extends ResourceRecord { id: string; name: string; host?: string; local?: boolean; is_active?: boolean }
interface KubernetesCluster extends ResourceRecord { id: string; name: string; api_server?: string; default_namespace?: string; is_active?: boolean }
interface RuntimeOption { key: string; id: string; kind: RuntimeKind; name: string; description: string; hosts: InfrastructureHost[]; enabled: boolean }
const props = withDefaults(defineProps<{ hosts: InfrastructureHost[]; hostId?: string; node?: string }>(), { hostId: '', node: 'overview' })
const emit = defineEmits<{ 'update:node': [value: string] }>()
const auth = useAuthStore()
const { t, te } = useI18n({ messages: {
  'zh-CN': { runtimeResources: {
    runtime: '运行时', allRuntimes: '全部运行时', docker: 'Docker 容器', kubernetes: 'Kubernetes Pod', refresh: '刷新', ping: '检查连接', pingOK: '连接检查通过', namespace: '命名空间', allNamespaces: '全部命名空间', load: '加载',
    search: '搜索', searchDocker: '名称、镜像或容器 ID', searchPods: 'Pod、命名空间或容器', status: '状态', running: '运行中', all: '全部', stopped: '已停止', count: '{visible} / {total} 个{kind}', containers: '容器', pods: 'Pod',
    name: '名称', image: '镜像版本', details: '运行信息', phase: '阶段', logs: '日志', terminal: '终端', previousPage: '上一页', nextPage: '下一页', page: '第 {page} / {total} 页', pageSize: '每页条数', noRead: '没有运行资源的查看权限', none: '当前没有可查看的运行时', missing: '运行时不存在或已被移除，请重新选择。', disabled: '运行时已停用', hostDisabled: '主机已停用或不再可用',
    emptyDocker: '暂无容器', emptyRunning: '暂无运行中的容器', emptyStopped: '暂无已停止的容器', emptyPods: '当前命名空间暂无 Pod', noResults: '没有符合条件的资源', unavailable: '资源读取失败', previous: '读取失败，当前保留上次结果',
    metadataFailed: '{kind} 运行时读取失败：{error}', connected: '已连接', unreachable: '不可达', unchecked: '待检查', unbound: '未关联主机', inactive: '已停用', hostCount: '{count} 台主机', dockerLogs: '容器日志：{name}', dockerTerminal: '容器终端：{name}', podTerminal: 'Pod 终端：{name}（{container}）',
    state_running: '运行中', state_exited: '已停止', state_created: '未启动', state_dead: '异常', state_paused: '已暂停', state_restarting: '重启中', state_removing: '删除中', state_pending: '等待中', state_succeeded: '已完成', state_failed: '失败', state_unknown: '未知',
  } },
  'en-US': { runtimeResources: {
    runtime: 'Runtime', allRuntimes: 'All runtimes', docker: 'Docker containers', kubernetes: 'Kubernetes Pods', refresh: 'Refresh', ping: 'Check connection', pingOK: 'Connection check passed', namespace: 'Namespace', allNamespaces: 'All namespaces', load: 'Load',
    search: 'Search', searchDocker: 'Name, image or container ID', searchPods: 'Pod, namespace or container', status: 'Status', running: 'Running', all: 'All', stopped: 'Stopped', count: '{visible} / {total} {kind}', containers: 'containers', pods: 'Pods',
    name: 'Name', image: 'Image', details: 'Runtime details', phase: 'Phase', logs: 'Logs', terminal: 'Terminal', previousPage: 'Previous', nextPage: 'Next', page: 'Page {page} / {total}', pageSize: 'Rows per page', noRead: 'You do not have permission to view runtime resources', none: 'No runtimes available', missing: 'This runtime no longer exists. Select a runtime again.', disabled: 'Runtime disabled', hostDisabled: 'The host is disabled or no longer available',
    emptyDocker: 'No containers', emptyRunning: 'No running containers', emptyStopped: 'No stopped containers', emptyPods: 'No Pods in this namespace', noResults: 'No matching resources', unavailable: 'Resources could not be loaded', previous: 'Refresh failed. Previous results are shown.',
    metadataFailed: 'Could not load {kind} runtimes: {error}', connected: 'Connected', unreachable: 'Unreachable', unchecked: 'Not checked', unbound: 'No linked host', inactive: 'Disabled', hostCount: '{count} hosts', dockerLogs: 'Container logs: {name}', dockerTerminal: 'Container terminal: {name}', podTerminal: 'Pod terminal: {name} ({container})',
    state_running: 'Running', state_exited: 'Stopped', state_created: 'Created', state_dead: 'Dead', state_paused: 'Paused', state_restarting: 'Restarting', state_removing: 'Removing', state_pending: 'Pending', state_succeeded: 'Completed', state_failed: 'Failed', state_unknown: 'Unknown',
  } },
} })
const canRead = computed(() => Boolean(auth.user) && auth.canAny(['cluster.read']))
const dockerEndpoints = ref<DockerEndpoint[]>([]), kubernetesClusters = ref<KubernetesCluster[]>([])
const containers = ref<ResourceRecord[]>([]), pods = ref<ResourceRecord[]>([])
const metadataLoading = ref(false), resourceLoading = ref(false), metadataLoaded = ref(false), resourceLoaded = ref(false), pingLoading = ref(false)
const metadataErrors = ref({ docker: '', kubernetes: '' }), resourceError = ref('')
const namespace = ref('default'), search = ref(''), containerFilter = ref<ContainerFilter>('running')
const terminal = ref({ open: false, title: '', path: '' }), containerLogs = ref({ open: false, title: '', path: '' })
let metadataRequest = 0, resourceRequest = 0, pingRequest = 0, namespaceRuntimeID = '', loadedContext = '', mounted = false, disposed = false, activeMetadataContext = ''
let metadataController: AbortController | undefined, resourceController: AbortController | undefined, pingController: AbortController | undefined
const selectedHost = computed(() => props.hosts.find(host => host.id === props.hostId))
const hostEnabled = computed(() => !props.hostId || Boolean(selectedHost.value && selectedHost.value.is_active !== false))
const hostRuntimeKeys = computed(() => selectedHost.value?.capabilities.filter(item => ['docker', 'kubernetes'].includes(item.kind) && item.runtime_id).map(item => `${item.kind}:${item.runtime_id}`).sort() ?? [])
function identity() { return JSON.stringify([auth.user?.id, auth.user?.department_id, auth.user?.project_id, auth.user?.is_superuser, [...(auth.user?.permissions ?? [])].sort(), typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project')]) }
function metadataContext() { return JSON.stringify([identity(), props.hostId, hostEnabled.value, hostRuntimeKeys.value]) }
function hostsForRuntime(kind: RuntimeKind, id: string) { return props.hosts.filter(host => host.capabilities.some(item => item.kind === kind && item.runtime_id === id)) }
const allRuntimeOptions = computed<RuntimeOption[]>(() => [
  ...dockerEndpoints.value.map(endpoint => ({ key: `docker:${endpoint.id}`, id: endpoint.id, kind: 'docker' as const, name: endpoint.name, description: endpoint.local ? 'Docker' : endpoint.host || 'Docker', hosts: hostsForRuntime('docker', endpoint.id), enabled: endpoint.is_active !== false })),
  ...kubernetesClusters.value.map(cluster => ({ key: `kubernetes:${cluster.id}`, id: cluster.id, kind: 'kubernetes' as const, name: cluster.name, description: cluster.api_server || 'Kubernetes', hosts: hostsForRuntime('kubernetes', cluster.id), enabled: cluster.is_active !== false })),
])
const visibleRuntimeOptions = computed(() => props.hostId ? allRuntimeOptions.value.filter(option => hostRuntimeKeys.value.includes(option.key)) : allRuntimeOptions.value)
const singleRuntime = computed(() => visibleRuntimeOptions.value.length === 1)
const selectedNode = computed(() => (props.node || 'overview') === 'overview' && singleRuntime.value ? visibleRuntimeOptions.value[0]!.key : props.node || 'overview')
const selectedRuntime = computed(() => visibleRuntimeOptions.value.find(option => option.key === selectedNode.value))
const selectedDocker = computed(() => selectedRuntime.value?.kind === 'docker')
const selectedCluster = computed(() => selectedRuntime.value?.kind === 'kubernetes' ? kubernetesClusters.value.find(item => item.id === selectedRuntime.value!.id) : undefined)
const runtimeOptions = computed(() => [{ value: 'overview', label: t('runtimeResources.allRuntimes') }, ...visibleRuntimeOptions.value.map(item => ({ value: item.key, label: `${item.name} · ${item.kind === 'docker' ? 'Docker' : 'Kubernetes'}` }))])
function resourceContext() { return JSON.stringify([identity(), props.hostId, hostEnabled.value, selectedRuntime.value?.key, selectedCluster.value ? namespace.value.trim() : '']) }
const filteredContainers = computed(() => filterRuntimeContainers(containers.value, search.value, containerFilter.value))
const filteredPods = computed(() => pods.value.filter(row => runtimeMatches(row, search.value)))
const containerCounts = computed(() => ({ running: containers.value.filter(row => containerState(row) === 'running').length, all: containers.value.length, stopped: containers.value.filter(stoppedContainer).length }))
const rowActionsDisabled = computed(() => !canRead.value || !hostEnabled.value || metadataLoading.value || resourceLoading.value || !resourceLoaded.value || Boolean(resourceError.value) || !selectedRuntime.value?.enabled || Boolean(selectedRuntime.value && metadataErrors.value[selectedRuntime.value.kind]) || loadedContext !== resourceContext())
const dockerColumns = computed(() => [ { key: 'names', label: t('runtimeResources.name'), width: '230px' }, { key: 'image_display', label: t('runtimeResources.image'), width: '260px' }, { key: 'state', label: t('runtimeResources.status'), width: '110px' }, { key: 'status', label: t('runtimeResources.details'), width: '190px' } ])
const podColumns = computed(() => [ { key: 'name', label: 'Pod', width: '240px' }, { key: 'namespace', label: t('runtimeResources.namespace'), width: '150px' }, { key: 'phase', label: t('runtimeResources.phase'), width: '110px' }, { key: 'containers', label: t('runtimeResources.containers'), width: '220px' } ])
const emptyText = computed(() => search.value.trim() ? t('runtimeResources.noResults') : t(selectedDocker.value ? containerFilter.value === 'running' ? 'runtimeResources.emptyRunning' : containerFilter.value === 'stopped' ? 'runtimeResources.emptyStopped' : 'runtimeResources.emptyDocker' : 'runtimeResources.emptyPods'))
function stateLabel(value: unknown) { const key = `runtimeResources.state_${String(value ?? '').toLowerCase()}`; return te(key) ? t(key) : String(value || t('runtimeResources.state_unknown')) }
function connectionState(runtime: RuntimeOption) {
  if (!runtime.enabled) return { label: t('runtimeResources.inactive'), color: 'default' }
  const scopedHosts = selectedHost.value ? [selectedHost.value] : runtime.hosts
  const statuses = scopedHosts.flatMap(host => host.capabilities.filter(item => item.kind === runtime.kind && item.runtime_id === runtime.id).map(item => item.status)) as HostCapabilityStatus[]
  if (statuses.includes('unreachable')) return { label: t('runtimeResources.unreachable'), color: 'error' }
  if (statuses.includes('unchecked')) return { label: t('runtimeResources.unchecked'), color: 'warning' }
  if (statuses.includes('ready')) return { label: t('runtimeResources.connected'), color: 'success' }
  return { label: t('runtimeResources.unbound'), color: 'default' }
}
function choose(node: string) { if (canRead.value && hostEnabled.value && (node === 'overview' || visibleRuntimeOptions.value.some(item => item.key === node))) emit('update:node', node) }
function closeStreams() { terminal.value = { open: false, title: '', path: '' }; containerLogs.value = { open: false, title: '', path: '' } }
function clearResources() { resourceRequest++; resourceController?.abort(); resourceLoading.value = false; resourceLoaded.value = false; loadedContext = ''; containers.value = []; pods.value = []; resourceError.value = ''; closeStreams() }
function ensureNamespaceForSelection() { const cluster = selectedCluster.value; if (cluster && namespaceRuntimeID !== cluster.id) { namespaceRuntimeID = cluster.id; namespace.value = cluster.default_namespace || 'default' } }

async function refreshMetadata() {
  if (!canRead.value || !hostEnabled.value || disposed) return false
  metadataController?.abort(); const controller = new AbortController(), request = ++metadataRequest, context = metadataContext(); metadataController = controller
  metadataLoading.value = true
  const kinds = (['docker', 'kubernetes'] as const).filter(kind => !props.hostId || hostRuntimeKeys.value.some(key => key.startsWith(`${kind}:`)))
  try {
    const results = await Promise.allSettled(kinds.map(kind => client.get(kind === 'docker' ? '/docker/endpoints' : '/kubernetes/clusters', { signal: controller.signal })))
    if (disposed || controller.signal.aborted || request !== metadataRequest || context !== metadataContext() || !canRead.value) return false
    const errors = { docker: '', kubernetes: '' }
    results.forEach((result, index) => {
      const kind = kinds[index]!
      if (result.status === 'rejected') errors[kind] = apiErrorMessage(result.reason)
      else if (kind === 'docker') dockerEndpoints.value = result.value.data.endpoints ?? []
      else kubernetesClusters.value = result.value.data.clusters ?? []
    })
    metadataErrors.value = errors; metadataLoaded.value = true
    return true
  } finally { if (request === metadataRequest) metadataLoading.value = false }
}
async function loadRuntime() {
  const runtime = selectedRuntime.value
  resourceController?.abort(); const request = ++resourceRequest
  if (!canRead.value || !hostEnabled.value || disposed || !runtime || !runtime.enabled || metadataErrors.value[runtime.kind]) {
    resourceLoading.value = false
    if (runtime && !runtime.enabled) { clearResources(); resourceError.value = t('runtimeResources.disabled') }
    return
  }
  const context = resourceContext(), controller = new AbortController(); resourceController = controller
  if (loadedContext !== context) { containers.value = []; pods.value = []; resourceLoaded.value = false }
  resourceLoading.value = true; resourceError.value = ''
  try {
    const path = runtime.kind === 'docker' ? `/docker/endpoints/${encodeURIComponent(runtime.id)}/containers?all=true` : `/kubernetes/clusters/${encodeURIComponent(runtime.id)}/pods?namespace=${encodeURIComponent(namespace.value.trim())}`
    const { data } = await client.get(path, { timeout: 35_000, signal: controller.signal })
    if (disposed || controller.signal.aborted || request !== resourceRequest || context !== resourceContext() || !canRead.value) return
    if (runtime.kind === 'docker') containers.value = data.containers ?? []
    else pods.value = data.pods ?? []
    loadedContext = context; resourceLoaded.value = true
  } catch (error) {
    if (!disposed && !controller.signal.aborted && request === resourceRequest && context === resourceContext()) resourceError.value = apiErrorMessage(error)
  } finally { if (request === resourceRequest) resourceLoading.value = false }
}
async function refresh() {
  if (!await refreshMetadata()) return
  ensureNamespaceForSelection()
  await loadRuntime()
}
async function ping() {
  const runtime = selectedRuntime.value
  if (!runtime || !runtime.enabled || !hostEnabled.value || !auth.canAny(['cluster.execute']) || !canRead.value || pingLoading.value) return
  pingController?.abort(); const controller = new AbortController(), request = ++pingRequest, context = resourceContext(); pingController = controller; pingLoading.value = true
  try { await client.post(`/${runtime.kind === 'docker' ? 'docker/endpoints' : 'kubernetes/clusters'}/${encodeURIComponent(runtime.id)}/ping`, undefined, { timeout: 35_000, signal: controller.signal }); if (!disposed && request === pingRequest && !controller.signal.aborted && context === resourceContext()) message.success(t('runtimeResources.pingOK')) }
  catch (error) { if (!disposed && request === pingRequest && !controller.signal.aborted && context === resourceContext()) message.error(apiErrorMessage(error)) }
  finally { if (request === pingRequest) pingLoading.value = false }
}
function currentRow(row: ResourceRecord, kind: RuntimeKind) { return !rowActionsDisabled.value && loadedContext === resourceContext() && selectedRuntime.value?.kind === kind && (kind === 'docker' ? containers.value : pods.value).includes(row) }
function openContainerLogs(row: ResourceRecord) {
  if (!currentRow(row, 'docker') || !row.id) return
  const runtime = selectedRuntime.value!
  containerLogs.value = { open: true, title: t('runtimeResources.dockerLogs', { name: runtimeContainerName(row) }), path: `/api/v1/docker/endpoints/${encodeURIComponent(runtime.id)}/containers/${encodeURIComponent(String(row.id))}/logs/ws` }
}
function openDockerTerminal(row: ResourceRecord) {
  if (!currentRow(row, 'docker') || !row.id || !auth.canAny(['terminal.open']) || containerState(row) !== 'running') return
  terminal.value = { open: true, title: t('runtimeResources.dockerTerminal', { name: runtimeContainerName(row) }), path: `/api/v1/terminals/docker/${encodeURIComponent(selectedRuntime.value!.id)}/containers/${encodeURIComponent(String(row.id))}/ws` }
}
function openPodTerminal(row: ResourceRecord, container: string) {
  if (!currentRow(row, 'kubernetes') || !auth.canAny(['terminal.open']) || row.phase !== 'Running' || !Array.isArray(row.containers) || !row.containers.includes(container)) return
  terminal.value = { open: true, title: t('runtimeResources.podTerminal', { name: row.name, container }), path: `/api/v1/terminals/kubernetes/${encodeURIComponent(selectedRuntime.value!.id)}/namespaces/${encodeURIComponent(String(row.namespace))}/pods/${encodeURIComponent(String(row.name))}/containers/${encodeURIComponent(container)}/ws` }
}
function resetContext() {
  activeMetadataContext = metadataContext(); metadataRequest++; metadataController?.abort(); pingRequest++; pingController?.abort(); pingLoading.value = false
  clearResources(); dockerEndpoints.value = []; kubernetesClusters.value = []; metadataErrors.value = { docker: '', kubernetes: '' }; metadataLoaded.value = false; metadataLoading.value = false; namespaceRuntimeID = ''; search.value = ''; containerFilter.value = 'running'
  if (mounted && canRead.value && hostEnabled.value) void refresh()
}
function externalContextChanged() { if (mounted && activeMetadataContext !== metadataContext()) resetContext() }
watch(() => props.node, () => { clearResources(); search.value = ''; containerFilter.value = 'running'; ensureNamespaceForSelection(); pingRequest++; pingController?.abort(); pingLoading.value = false; if (mounted && metadataLoaded.value) void loadRuntime() }, { flush: 'sync' })
watch(() => selectedRuntime.value ? `${selectedRuntime.value.key}/${selectedRuntime.value.enabled}` : '', () => { closeStreams(); if (!selectedRuntime.value || !selectedRuntime.value.enabled) clearResources() }, { flush: 'sync' })
watch(namespace, () => { if (selectedCluster.value) clearResources() }, { flush: 'sync' })
watch(metadataContext, resetContext, { flush: 'sync' })
onMounted(() => { mounted = true; activeMetadataContext = metadataContext(); window.addEventListener('storage', externalContextChanged); window.addEventListener('edo:projects-changed', externalContextChanged); void refresh() })
onBeforeUnmount(() => { disposed = true; mounted = false; metadataRequest++; metadataController?.abort(); pingRequest++; pingController?.abort(); clearResources(); window.removeEventListener('storage', externalContextChanged); window.removeEventListener('edo:projects-changed', externalContextChanged) })
</script>

<template>
  <section class="runtime-resource-panel" :aria-busy="metadataLoading || resourceLoading">
    <a-alert v-if="!canRead" type="warning" show-icon :message="t('runtimeResources.noRead')" />
    <a-alert v-else-if="!hostEnabled" type="warning" show-icon :message="t('runtimeResources.hostDisabled')" />
    <template v-else>
      <div class="runtime-toolbar">
        <label v-if="visibleRuntimeOptions.length > 1" class="runtime-choice"><span>{{ t('runtimeResources.runtime') }}</span><select :value="selectedNode" :aria-label="t('runtimeResources.runtime')" @change="choose(($event.target as HTMLSelectElement).value)"><option v-for="option in runtimeOptions" :key="option.value" :value="option.value">{{ option.label }}</option></select></label>
        <div v-else-if="selectedRuntime" class="runtime-kind"><RuntimeBrandIcon :kind="selectedRuntime.kind" /><span>{{ t(`runtimeResources.${selectedRuntime.kind}`) }}</span></div>
        <a-tag v-if="selectedRuntime" :color="connectionState(selectedRuntime).color">{{ connectionState(selectedRuntime).label }}</a-tag>
        <span class="toolbar-spacer" />
        <a-button v-if="selectedRuntime && auth.canAny(['cluster.execute'])" :disabled="!selectedRuntime.enabled || metadataLoading || resourceLoading" :loading="pingLoading" @click="ping">{{ t('runtimeResources.ping') }}</a-button>
        <a-button :loading="metadataLoading || resourceLoading" @click="refresh"><RefreshCw :size="14" aria-hidden="true" />{{ t('runtimeResources.refresh') }}</a-button>
      </div>
      <div v-if="metadataErrors.docker || metadataErrors.kubernetes" class="runtime-alerts"><a-alert v-for="kind in (['docker', 'kubernetes'] as const).filter(kind => metadataErrors[kind])" :key="kind" type="error" show-icon :message="t('runtimeResources.metadataFailed', { kind: kind === 'docker' ? 'Docker' : 'Kubernetes', error: metadataErrors[kind] })" /></div>
      <a-skeleton v-if="metadataLoading && !metadataLoaded" active :paragraph="{ rows: 3 }" />
      <div v-else-if="selectedNode === 'overview' && visibleRuntimeOptions.length > 1" class="runtime-directory">
        <button v-for="runtime in visibleRuntimeOptions" :key="runtime.key" type="button" @click="choose(runtime.key)"><RuntimeBrandIcon :kind="runtime.kind" /><span class="directory-name"><strong>{{ runtime.name }}</strong><span>{{ runtime.description }}</span></span><span class="directory-hosts">{{ runtime.hosts.length === 1 ? runtime.hosts[0]?.name : runtime.hosts.length ? t('runtimeResources.hostCount', { count: runtime.hosts.length }) : t('runtimeResources.unbound') }}</span><a-tag :color="connectionState(runtime).color">{{ connectionState(runtime).label }}</a-tag><ChevronRight :size="15" aria-hidden="true" /></button>
      </div>
      <div v-else-if="selectedRuntime" class="runtime-detail">
        <div class="resource-filters">
          <a-radio-group v-if="selectedDocker" v-model:value="containerFilter" role="radiogroup" :aria-label="t('runtimeResources.status')" size="small" button-style="solid"><a-radio-button v-for="filter in (['running', 'all', 'stopped'] as const)" :key="filter" :value="filter">{{ t(`runtimeResources.${filter}`) }}<span class="filter-count">{{ resourceLoaded ? containerCounts[filter] : '—' }}</span></a-radio-button></a-radio-group>
          <label v-if="selectedCluster" class="namespace-field"><span>{{ t('runtimeResources.namespace') }}</span><a-input v-model:value="namespace" :aria-label="t('runtimeResources.namespace')" :placeholder="t('runtimeResources.allNamespaces')" @press-enter="loadRuntime" /><a-button :loading="resourceLoading" @click="loadRuntime">{{ t('runtimeResources.load') }}</a-button></label>
          <label class="runtime-search"><span>{{ t('runtimeResources.search') }}</span><a-input v-model:value="search" allow-clear :aria-label="t('runtimeResources.search')" :placeholder="t(selectedDocker ? 'runtimeResources.searchDocker' : 'runtimeResources.searchPods')"><template #prefix><Search :size="14" aria-hidden="true" /></template></a-input></label>
        </div>
        <a-alert v-if="resourceError" class="resource-error" type="warning" show-icon :message="resourceLoaded ? t('runtimeResources.previous') : resourceError" :description="resourceLoaded ? resourceError : undefined" />
        <div class="runtime-list-meta" role="status">{{ t('runtimeResources.count', { visible: resourceLoaded ? selectedDocker ? filteredContainers.length : filteredPods.length : '—', total: resourceLoaded ? selectedDocker ? containers.length : pods.length : '—', kind: t(selectedDocker ? 'runtimeResources.containers' : 'runtimeResources.pods') }) }}</div>
        <div v-if="selectedDocker" class="runtime-resource-table">
        <ResourceTable :key="selectedNode" :rows="filteredContainers" :columns="dockerColumns" :loading="resourceLoading" :details="false" keep-header :empty-text="resourceError ? t('runtimeResources.unavailable') : emptyText">
          <template #cell-names="{ row }"><div class="container-identity"><strong>{{ runtimeContainerName(row) }}</strong><code>{{ String(row.id ?? '').slice(0, 12) }}</code></div></template>
          <template #cell-image_display="{ row }"><span class="runtime-cell" tabindex="0">{{ row.image_display || row.image || '—' }}</span></template>
          <template #cell-state="{ row }"><a-tag :color="runtimeStateTone(row.state)">{{ stateLabel(row.state) }}</a-tag></template>
          <template #cell-status="{ row }"><span class="runtime-cell runtime-status" tabindex="0">{{ row.status || '—' }}</span></template>
          <template #actions="{ row }"><a-button size="small" :disabled="rowActionsDisabled" @click="openContainerLogs(row)"><FileText :size="14" aria-hidden="true" />{{ t('runtimeResources.logs') }}</a-button><a-button v-if="auth.canAny(['terminal.open'])" size="small" :disabled="rowActionsDisabled || containerState(row) !== 'running'" @click="openDockerTerminal(row)"><TerminalSquare :size="14" aria-hidden="true" />{{ t('runtimeResources.terminal') }}</a-button></template>
          <template #pagination="{ page, limit, total, changePage, changeLimit }"><div class="runtime-pagination"><span>{{ t('runtimeResources.page', { page, total: Math.max(1, Math.ceil(total / limit)) }) }}</span><a-button size="small" :disabled="page <= 1" @click="changePage(page - 1)">{{ t('runtimeResources.previousPage') }}</a-button><a-button size="small" :disabled="page * limit >= total" @click="changePage(page + 1)">{{ t('runtimeResources.nextPage') }}</a-button><label>{{ t('runtimeResources.pageSize') }}<select :value="limit" :aria-label="t('runtimeResources.pageSize')" @change="changeLimit(Number(($event.target as HTMLSelectElement).value))"><option v-for="size in [10, 20, 50, 100]" :key="size" :value="size">{{ size }}</option></select></label></div></template>
        </ResourceTable>
        </div>
        <div v-else-if="selectedCluster" class="runtime-resource-table pod-resource-table">
        <ResourceTable :key="selectedNode + namespace" :rows="filteredPods" :columns="podColumns" :loading="resourceLoading" :details="false" keep-header :empty-text="resourceError ? t('runtimeResources.unavailable') : emptyText">
          <template #cell-name="{ row }"><strong class="runtime-cell">{{ row.name }}</strong></template><template #cell-phase="{ row }"><a-tag :color="runtimeStateTone(row.phase)">{{ stateLabel(row.phase) }}</a-tag></template>
          <template #cell-containers="{ row }"><span class="runtime-cell">{{ Array.isArray(row.containers) ? row.containers.join('、') || '—' : '—' }}</span></template>
          <template v-if="auth.canAny(['terminal.open'])" #actions="{ row }"><a-button v-for="podContainer in (Array.isArray(row.containers) ? row.containers : [])" :key="String(podContainer)" size="small" :disabled="rowActionsDisabled || row.phase !== 'Running'" @click="openPodTerminal(row, String(podContainer))"><TerminalSquare :size="14" aria-hidden="true" />{{ podContainer }}</a-button></template>
          <template #pagination="{ page, limit, total, changePage, changeLimit }"><div class="runtime-pagination"><span>{{ t('runtimeResources.page', { page, total: Math.max(1, Math.ceil(total / limit)) }) }}</span><a-button size="small" :disabled="page <= 1" @click="changePage(page - 1)">{{ t('runtimeResources.previousPage') }}</a-button><a-button size="small" :disabled="page * limit >= total" @click="changePage(page + 1)">{{ t('runtimeResources.nextPage') }}</a-button><label>{{ t('runtimeResources.pageSize') }}<select :value="limit" :aria-label="t('runtimeResources.pageSize')" @change="changeLimit(Number(($event.target as HTMLSelectElement).value))"><option v-for="size in [10, 20, 50, 100]" :key="size" :value="size">{{ size }}</option></select></label></div></template>
        </ResourceTable>
        </div>
      </div>
      <a-empty v-else-if="metadataLoaded" :description="t(selectedNode === 'overview' ? 'runtimeResources.none' : 'runtimeResources.missing')" />
    </template>
    <TerminalDrawer v-model:open="terminal.open" :title="terminal.title" :path="terminal.path" />
    <ContainerLogDrawer v-model:open="containerLogs.open" :title="containerLogs.title" :path="containerLogs.path" />
  </section>
</template>

<style scoped>
.runtime-kind :deep(svg),.runtime-directory>button :deep(svg:first-child){fill:currentColor;color:var(--edo-primary)}
.runtime-resource-panel{min-width:0;color:var(--edo-text)}.runtime-toolbar{display:flex;align-items:center;gap:8px;min-height:34px;margin-bottom:16px;flex-wrap:wrap}.toolbar-spacer{flex:1}.runtime-choice,.namespace-field,.runtime-search{display:flex;align-items:center;gap:8px;min-width:0;font-size:12px;color:var(--edo-muted)}.runtime-choice>span,.namespace-field>span,.runtime-search>span{flex-shrink:0}.runtime-choice :deep(.ant-select){width:290px}.runtime-kind{display:flex;align-items:center;gap:8px;font-size:13px;font-weight:600}.runtime-kind :deep(svg){width:22px;height:22px}.runtime-toolbar :deep(.ant-tag){margin:0}.runtime-toolbar :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}.runtime-alerts{display:grid;gap:8px;margin-bottom:12px}.runtime-directory{border:1px solid var(--edo-border);border-radius:6px;overflow:hidden}.runtime-directory>button{display:flex;align-items:center;gap:14px;width:100%;padding:12px 14px;border:0;border-bottom:1px solid var(--edo-border);background:var(--edo-surface);color:var(--edo-text);text-align:left;cursor:pointer}.runtime-directory>button:last-child{border-bottom:0}.runtime-directory>button:hover{background:var(--edo-primary-soft)}.runtime-directory>button:focus-visible,.runtime-cell:focus-visible{outline:2px solid var(--edo-primary);outline-offset:-2px}.runtime-directory>button>:deep(svg){width:25px;height:25px;flex-shrink:0}.directory-name{display:grid;gap:3px;flex:1;min-width:0}.directory-name strong{font-size:13px;font-weight:600}.directory-name>span{font-size:12px;color:var(--edo-muted);overflow-wrap:anywhere}.directory-hosts{max-width:180px;font-size:12px;color:var(--edo-muted);overflow-wrap:anywhere}.runtime-directory :deep(.ant-tag){margin:0}.runtime-detail{min-width:0}.resource-filters{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin-bottom:12px}.filter-count{margin-left:6px;font-size:12px;font-variant-numeric:tabular-nums;opacity:.8}.runtime-search{margin-left:auto;flex:1;max-width:360px;min-width:240px}.runtime-search :deep(.ant-input-affix-wrapper){min-width:0;flex:1}.namespace-field :deep(.ant-input){width:160px}.runtime-list-meta{margin-bottom:8px;font-size:12px;color:var(--edo-muted);font-variant-numeric:tabular-nums}.resource-error{margin-bottom:12px}.runtime-resource-table{border:1px solid var(--edo-border);border-radius:6px;overflow:hidden}.runtime-resource-table :deep(.resource-table){min-width:870px}.runtime-resource-table :deep(th){height:36px;padding:8px 12px;background:var(--edo-surface-soft);color:var(--edo-text);font-weight:600;border-bottom:1px solid var(--edo-border)}.runtime-resource-table :deep(td){height:52px;padding:8px 12px;font-size:13px}.runtime-resource-table :deep(th:not(:last-child)),.runtime-resource-table :deep(td:not(:last-child)){border-right:1px solid var(--edo-border)}.runtime-resource-table :deep(tbody tr:last-child td){border-bottom:1px solid var(--edo-border)}.runtime-resource-table :deep(.table-actions){min-width:156px;box-shadow:none;border-left:1px solid var(--edo-border)}.runtime-resource-table :deep(.resource-row-actions){gap:6px}.runtime-resource-table :deep(.resource-row-actions .ant-btn){display:inline-flex;align-items:center;gap:4px;height:28px;padding-inline:7px;font-size:12px}.runtime-resource-table :deep(.resource-pagination){margin:10px 12px}.runtime-resource-table :deep(.ant-tag){margin:0;font-size:12px}.container-identity{display:grid;gap:3px;min-width:0;max-width:260px}.container-identity strong,.runtime-cell{overflow-wrap:anywhere;white-space:normal;font-size:13px;line-height:1.6}.container-identity strong{font-weight:600}.container-identity code{font-size:11px;color:var(--edo-muted);font-variant-numeric:tabular-nums}.runtime-cell{display:block;max-width:320px;outline-offset:2px}.runtime-status{color:var(--edo-muted);font-size:12px}.pod-resource-table :deep(.resource-table){min-width:760px}
@media(max-width:760px){.runtime-choice{flex:1;min-width:100%}.runtime-choice :deep(.ant-select){flex:1;min-width:0;width:0}.runtime-toolbar{gap:8px;margin-bottom:12px}.runtime-search{max-width:none;min-width:100%;margin-left:0}.namespace-field{flex:1;flex-wrap:wrap}.namespace-field :deep(.ant-input){flex:1;min-width:100px;width:0}.resource-filters{gap:10px}.directory-hosts{display:none}.runtime-directory>button{gap:10px;padding:12px}.directory-name>span{font-size:11px}.runtime-toolbar :deep(.ant-btn){height:30px;padding-inline:8px;font-size:12px}}
.runtime-choice select,.runtime-pagination select{min-height:30px;padding:4px 26px 4px 9px;border:1px solid var(--edo-border);border-radius:5px;background:var(--edo-surface);color:var(--edo-text);font:inherit;font-size:12px;max-width:100%}.runtime-choice select{width:290px}.runtime-choice select:focus-visible,.runtime-pagination select:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.runtime-pagination{display:flex;align-items:center;justify-content:flex-end;gap:8px;padding:10px 12px;flex-wrap:wrap;font-size:12px;color:var(--edo-muted)}.runtime-pagination>span{margin-right:auto}.runtime-pagination label{display:flex;align-items:center;gap:8px;margin-left:4px}.filter-count{opacity:1}.resource-filters :deep(.ant-radio-button-wrapper){color:var(--edo-text)}.resource-filters :deep(.ant-radio-button-wrapper-checked){background:var(--edo-primary-soft);color:var(--edo-text);font-weight:600}.runtime-resource-panel :deep(.ant-tag){--runtime-tone:var(--edo-muted);color:var(--edo-text);background:color-mix(in srgb,var(--runtime-tone) 8%,var(--edo-surface));border-color:color-mix(in srgb,var(--runtime-tone) 32%,var(--edo-border))}.runtime-resource-panel :deep(.ant-tag-success){--runtime-tone:var(--kube-success,#008566)}.runtime-resource-panel :deep(.ant-tag-error){--runtime-tone:var(--kube-danger,#c83c52)}.runtime-resource-panel :deep(.ant-tag-warning){--runtime-tone:var(--kube-warning,#a96008)}
@media(max-width:760px){.runtime-choice select{flex:1;min-width:0;width:0}.runtime-pagination{gap:6px}.runtime-pagination>span{flex-basis:100%}}
.runtime-resource-table :deep(tbody tr>td){background:var(--edo-surface)}.runtime-resource-table :deep(tbody tr:nth-child(even)>td){background:color-mix(in srgb,var(--edo-primary) 2%,var(--edo-surface))}.runtime-resource-table :deep(tbody tr:hover>td){background:color-mix(in srgb,var(--edo-primary) 4%,var(--edo-surface))}
@media(max-width:760px){.runtime-resource-table :deep(.table-actions),.runtime-resource-table :deep(.table-action-heading){position:static}}
</style>
