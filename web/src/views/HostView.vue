<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import {
  Boxes,
  CheckCircle2,
  CircleOff,
  Container,
  ArrowUpRight,
  Link2,
  Folder,
  FolderOpen,
  MoreHorizontal,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Server,
  Trash2,
} from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter, type LocationQueryRaw } from 'vue-router'

import client from '@/api/client'
import {
  capabilityOf,
  environmentIDsOf,
  listEnvironments,
  listHosts,
  listHostStatuses,
  mergeHostStatuses,
  setClusterHosts,
  type HostCapabilityKind,
  type InfrastructureEnvironment,
  type InfrastructureHost,
  type InfrastructureCluster,
} from '@/api/infrastructure'
import { apiErrorMessage } from '@/api/resources'
import HostDrawer from '@/components/HostDrawer.vue'
import HostResourceSummary from '@/components/HostResourceSummary.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import ResourceRowActions from '@/components/ResourceRowActions.vue'
import { useResourceList } from '@/utils/resourceList'
import KubernetesClusterDrawer from '@/components/KubernetesClusterDrawer.vue'
import ClusterInventory from '@/components/ClusterInventory.vue'
import KubeHostRoleBadge from '@/components/KubeHostRoleBadge.vue'
import K3sProvisionDrawer from '@/components/K3sProvisionDrawer.vue'
import type { K3sInstallation } from '@/api/k3s'
import PageToolbar from '@/components/PageToolbar.vue'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'
import RuntimeResourcePanel from '@/components/RuntimeResourcePanel.vue'
import { useAuthStore } from '@/stores/auth'
import { hostSupportsTerminal, useHostTerminalsStore } from '@/stores/hostTerminals'
import { formatDateTime } from '@/utils/time'
import { clusterForHost, clusterHostIDs } from '@/utils/infrastructureRelations'

type ClusterOption = InfrastructureCluster
type HostGroupKey = string
type HostStatusFilter = 'all' | 'ready' | 'warning' | 'inactive'
type GroupFormMode = 'create' | 'details' | 'hosts'
type DetailTab = 'info' | 'resources'

const auth = useAuthStore()
function openHostTerminal(host: InfrastructureHost) {
  const current = hosts.value.find(item => item.id === host.id)
  if (!current || current !== host || hostLoadError.value || !hostSupportsTerminal(current)) return
  const result = useHostTerminalsStore().open(current)
  if (result.error) message.warning(result.error)
}
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const hosts = ref<InfrastructureHost[]>([])
const environments = ref<InfrastructureEnvironment[]>([])
const clusters = ref<ClusterOption[]>([])
const clusterLoadError = ref('')
const hostLoadError = ref('')
const environmentLoadError = ref('')
const relationOpen = ref(false)
const relationHost = ref<InfrastructureHost>()
const relationClusterID = ref('')
const relationSaving = ref(false)
const relationError = ref('')
watch(relationClusterID, () => { relationError.value = '' })
const selectedID = ref('')
const selectedGroupKey = ref<HostGroupKey>('all')
const searchQuery = ref('')
const statusFilter = ref<HostStatusFilter>('all')
const loading = ref(false)
const resourceRevision = ref(0)
const drawerOpen = ref(false)
const detailOpen = ref(false)
const detailTab = ref<DetailTab>('info')
const resourceNode = ref('overview')
const resourceHostID = ref('')
const clusterCreateOpen = ref(false)
const clusterProvisionOpen = ref(false)
const editingHost = ref<InfrastructureHost>()
const groupDrawerOpen = ref(false)
const groupSaving = ref(false)
const groupMode = ref<GroupFormMode>('create')
const editingGroupID = ref('')
const groupForm = reactive({ name: '', description: '', host_ids: [] as string[] })
let statusTimer: number | undefined
let statusRefreshing = false
let statusErrorShown = false
let lastStatusRefreshAt = 0
let initialized = false
let mounted = true
let inventoryRead = 0
let statusRead = 0
let inventoryController: AbortController | undefined
let statusController: AbortController | undefined

const statusPollInterval = 5000
const statusRefreshDeduplicationWindow = 1000
// 旧版自动环境仅供已有部署兼容，不作为用户创建的主机分组展示。
const legacyLocalEnvironmentID = 'edo-local-environment'
const canReadGroups = computed(() => auth.canAny(['deployment.read']))
const canReadHosts = computed(() => auth.canAny(['cluster.read', 'deployment.read']))
const canReadResources = computed(() => auth.canAny(['cluster.read']))
const canReadClusters = computed(() => auth.canAny(['cluster.read', 'kubernetes.node.read', 'kubernetes.workload.read', 'kubernetes.config.read', 'kubernetes.storage.read', 'kubernetes.network.read']))
const canManageKubernetes = computed(() => auth.canAny(['kubernetes.node.read', 'kubernetes.workload.read', 'kubernetes.config.read', 'kubernetes.storage.read', 'kubernetes.network.read']))
const activeWorkspace = computed(() => route.query.view === 'clusters' ? 'clusters' : 'hosts')
const selected = computed(() => hosts.value.find(host => host.id === selectedID.value))
const selectedCluster = computed(() => selected.value ? clusterForHost(selected.value, clusters.value) : undefined)
const hostGroups = computed(() => environments.value.filter(environment => environment.id !== legacyLocalEnvironmentID))
const selectedGroup = computed(() => hostGroups.value.find(environment => environment.id === selectedGroupKey.value))
const environmentNames = computed(() => new Map(hostGroups.value.map(environment => [environment.id, environment.name])))
const normalizedSearch = computed(() => searchQuery.value.trim().toLocaleLowerCase())
const tableColumns = [
  { title: '主机名称', key: 'name', width: 190 },
  { title: '连接信息', key: 'connection', width: 180 },
  { title: '系统资源', key: 'resources', width: 240 },
  { title: '所属分组', key: 'groups', width: 150 },
  { title: '主机能力', key: 'capabilities', width: 120 },
  { title: '关联集群', key: 'cluster', width: 180 },
  { title: '状态', key: 'status', width: 140 },
  { title: '操作', key: 'actions', fixed: 'right' as const, width: 170 },
]

const selectedEnvironmentSummary = computed(() => {
  if (!selected.value || !canReadGroups.value) return canReadGroups.value ? '' : '无环境查看权限'
  const environmentIDs = environmentIDsOf(selected.value).filter(id => id !== legacyLocalEnvironmentID)
  const names = environmentIDs
    .map(id => environmentNames.value.get(id))
    .filter((name): name is string => Boolean(name))
  if (names.length === environmentIDs.length && names.length) {
    return t('environment.hostAssociation.linked', { names: names.join(t('environment.hostAssociation.separator')) })
  }
  if (environmentIDs.length) return t('environment.hostAssociation.countOnly', { count: environmentIDs.length })
  return t('environment.hostAssociation.none')
})

const searchAndStatusHosts = computed(() => hosts.value.filter(host => {
  const query = normalizedSearch.value
  if (query) {
    const groupNames = canReadGroups.value
      ? environmentIDsOf(host).map(id => environmentNames.value.get(id) || '').join(' ')
      : ''
    const searchable = [host.name, host.address, host.ssh_username, groupNames, clusterForHost(host, clusters.value)?.name].join(' ').toLocaleLowerCase()
    if (!searchable.includes(query)) return false
  }
  const state = hostRuntimeState(host).className
  if (statusFilter.value === 'ready') return state === 'ready'
  if (statusFilter.value === 'inactive') return state === 'inactive'
  if (statusFilter.value === 'warning') return state !== 'ready' && state !== 'inactive'
  return true
}))

const filteredHosts = computed(() => {
  if (!canReadGroups.value || selectedGroupKey.value === 'all') return searchAndStatusHosts.value
  return searchAndStatusHosts.value.filter(host => environmentIDsOf(host).includes(selectedGroupKey.value))
})

const { pagination: hostPagination, changePage: changeHostPage } = useResourceList(() => filteredHosts.value)

const visibleEnvironments = computed(() => hostGroups.value.filter(environment => {
  if (!normalizedSearch.value && statusFilter.value === 'all') return true
  return groupHostCount(environment.id) > 0 || environment.id === selectedGroupKey.value
}))
const groupTitle = computed(() => {
  if (groupMode.value === 'details') return `编辑分组“${groupForm.name}”`
  if (groupMode.value === 'hosts') return `调整“${groupForm.name}”的主机`
  return '新建主机分组'
})
const groupHostOptions = computed(() => hosts.value.map(host => ({
  value: host.id,
  label: host.name,
  location: host.is_builtin ? '本地' : `${host.address}:${host.ssh_port}`,
  capabilities: host.capabilities.map(item => capabilityName(item.kind)).join(' + ') || '未配置能力',
  isActive: host.is_active,
})))

function groupHostCount(environmentID: string) {
  return searchAndStatusHosts.value.filter(host => environmentIDsOf(host).includes(environmentID)).length
}

function groupNamesOf(host: InfrastructureHost) {
  if (!canReadGroups.value) return []
  return environmentIDsOf(host)
    .map(id => environmentNames.value.get(id))
    .filter((name): name is string => Boolean(name))
}

function runtimeCount(host: InfrastructureHost) {
  return host.capabilities.filter(capability =>
    (capability.kind === 'docker' || capability.kind === 'kubernetes') && capability.runtime_id,
  ).length
}

function hostHasRuntimeNode(host: InfrastructureHost, node: string) {
  const separator = node.indexOf(':')
  if (separator < 1) return false
  const kind = node.slice(0, separator)
  const runtimeID = node.slice(separator + 1)
  if ((kind !== 'docker' && kind !== 'kubernetes') || !runtimeID) return false
  return host.capabilities.some(capability => capability.kind === kind && capability.runtime_id === runtimeID)
}

function syncRouteState() {
  if (!initialized) return
  const requested = typeof route.query.host === 'string' ? route.query.host : ''
  const requestedNode = typeof route.query.node === 'string' ? route.query.node : ''
  const resourcesRequested = route.query.view === 'resources' || Boolean(requestedNode)
  if (resourcesRequested) {
    if (!canReadResources.value) {
      const query = { ...route.query }
      delete query.view
      delete query.node
      void router.replace({ path: route.path, query })
      return
    }
    const requestedHost = hosts.value.find(item => item.id === requested)
    const host = requestedHost && (!requestedNode || requestedNode === 'overview' || hostHasRuntimeNode(requestedHost, requestedNode))
      ? requestedHost
      : undefined
    selectedID.value = host?.id || ''
    resourceHostID.value = requested || ''
    resourceNode.value = requestedNode || 'overview'
    if (host) selectedGroupKey.value = 'all'
    detailTab.value = 'resources'
    detailOpen.value = true
    return
  }
  const host = hosts.value.find(item => item.id === requested)
  if (host) {
    selectedID.value = host.id
    resourceHostID.value = host.id
    selectedGroupKey.value = 'all'
    detailTab.value = 'info'
    detailOpen.value = true
    return
  }
  if (requested) {
    selectedID.value = ''
    resourceHostID.value = ''
    detailOpen.value = false
    const query = { ...route.query }
    delete query.host
    void router.replace({ path: route.path, query })
    return
  }
  selectedID.value = ''
  resourceHostID.value = ''
  detailOpen.value = false
}

async function refresh() {
  if (loading.value) return
  const read = ++inventoryRead
  const context = hostReadContext()
  statusRead += 1
  statusController?.abort()
  statusRefreshing = false
  inventoryController?.abort()
  inventoryController = new AbortController()
  const signal = inventoryController.signal
  loading.value = true
  try {
    const [hostResult, environmentResult, clusterResult] = await Promise.allSettled([
      canReadHosts.value ? listHosts(signal) : Promise.resolve([]),
      canReadGroups.value ? listEnvironments(signal) : Promise.resolve([]),
      canReadClusters.value ? client.get<{ clusters: ClusterOption[] }>('/kubernetes/clusters', { signal }).then(response => response.data.clusters ?? []) : Promise.resolve([]),
    ])
    if (!mounted || read !== inventoryRead || context !== hostReadContext()) return
    hosts.value = hostResult.status === 'fulfilled' ? hostResult.value : []
    resourceRevision.value += 1
    hostLoadError.value = hostResult.status === 'rejected' ? apiErrorMessage(hostResult.reason) : ''
    environments.value = environmentResult.status === 'fulfilled' ? environmentResult.value : []
    environmentLoadError.value = environmentResult.status === 'rejected' ? apiErrorMessage(environmentResult.reason) : ''
    clusters.value = clusterResult.status === 'fulfilled' ? clusterResult.value : []
    clusterLoadError.value = clusterResult.status === 'rejected' ? apiErrorMessage(clusterResult.reason) : ''
    if (!canReadGroups.value) selectedGroupKey.value = 'all'
    else if (selectedGroupKey.value !== 'all' && !hostGroups.value.some(item => item.id === selectedGroupKey.value)) {
      selectedGroupKey.value = 'all'
    }
    initialized = true
    if (!hostLoadError.value) syncRouteState()
    else detailOpen.value = false
    const error = hostLoadError.value || clusterLoadError.value || environmentLoadError.value
    if (error) message.error(error)
  } catch (error) {
    if (mounted && read === inventoryRead && context === hostReadContext()) message.error(apiErrorMessage(error))
  } finally {
    if (mounted && read === inventoryRead) loading.value = false
  }
}

async function refreshStatuses() {
  const now = Date.now()
  if (!canReadHosts.value || document.hidden || loading.value || statusRefreshing || now - lastStatusRefreshAt < statusRefreshDeduplicationWindow) return
  statusRefreshing = true
  lastStatusRefreshAt = now
  const read = ++statusRead
  const context = hostReadContext()
  statusController?.abort()
  statusController = new AbortController()
  try {
    const statuses = await listHostStatuses(statusController.signal)
    if (!mounted || read !== statusRead || context !== hostReadContext() || !canReadHosts.value) return
    const merged = mergeHostStatuses(hosts.value, statuses)
    if (!merged) {
      await refresh()
      return
    }
    hosts.value = merged
    syncRouteState()
    statusErrorShown = false
  } catch (error) {
    if (!mounted || read !== statusRead || context !== hostReadContext()) return
    if (!statusErrorShown) message.error(apiErrorMessage(error))
    statusErrorShown = true
  } finally {
    if (mounted && read === statusRead) statusRefreshing = false
  }
}

function refreshVisibleStatuses() {
  if (!document.hidden) void refreshStatuses()
}

function create() {
  selectedGroupKey.value = 'all'
  editingHost.value = undefined
  drawerOpen.value = true
}

async function hostSaved() {
  await refresh()
}

function provisionContext() {
  return JSON.stringify([auth.user?.id??'',auth.user?.project_id??'',typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')??'',auth.user?.is_superuser??false,[...(auth.user?.permissions??[])].sort(),route.path,route.query.view??'',route.query.cluster??''])
}

function hostReadContext() {
  return JSON.stringify([auth.user?.id, auth.user?.project_id, auth.user?.department_id, auth.user?.is_superuser,
    [...(auth.user?.permissions ?? [])].sort(), typeof localStorage === 'undefined' ? '' : localStorage.getItem('edo.project')])
}

async function clusterCreated(cluster: Pick<ClusterOption, 'id'>) {
  const context=provisionContext()
  await refresh()
  if (!mounted || drawerOpen.value || context!==provisionContext()) return
  void router.replace({ path: '/hosts', query: { view: 'clusters', cluster: cluster.id } })
}

async function clusterProvisionCompleted(installation: K3sInstallation) {
  const id = installation.cluster_id || installation.cluster?.id
  if (id) await clusterCreated({ id })
  else await refresh()
}

function manageCluster(cluster: InfrastructureCluster) {
  if (canManageKubernetes.value) void router.push({ path: '/kubernetes', query: { cluster: cluster.id, view: 'overview' } })
  else void router.push({ path: '/hosts', query: { view: 'clusters', cluster: cluster.id } })
}

function editHostCluster(host: InfrastructureHost) {
  if (clusterLoadError.value || hostLoadError.value || !auth.canAny(['cluster.update']) || !canReadHosts.value) return
  relationHost.value = host
  relationClusterID.value = clusterForHost(host, clusters.value)?.id || ''
  relationError.value = ''
  relationOpen.value = true
}

async function saveHostCluster() {
  const host = relationHost.value
  if (!host || !relationOpen.value || relationSaving.value || clusterLoadError.value || hostLoadError.value || !auth.canAny(['cluster.update']) || !canReadHosts.value) return
  const current = clusterForHost(host, clusters.value)
  const target = clusters.value.find(cluster => cluster.id === relationClusterID.value)
  if (relationClusterID.value && !target) {
    relationError.value = '所选集群已不可用，请刷新后重试'
    message.error(relationError.value)
    return
  }
  if (current?.id === target?.id) {
    relationOpen.value = false
    return
  }
  relationSaving.value = true
  relationError.value = ''
  try {
    if (target) await setClusterHosts(target.id, clusterHostIDs(target, hosts.value, host.id, true))
    else if (current) await setClusterHosts(current.id, clusterHostIDs(current, hosts.value, host.id, false))
    message.success(target ? '集群关联已更新' : '已解除集群关联')
    relationOpen.value = false
    await refresh()
  } catch (error) {
    relationError.value = apiErrorMessage(error)
    message.error(relationError.value)
  } finally {
    relationSaving.value = false
  }
}

function edit(host: InfrastructureHost) {
  editingHost.value = host
  drawerOpen.value = true
}

function openDetails(host: InfrastructureHost) {
  selectedID.value = host.id
  resourceHostID.value = host.id
  detailTab.value = 'info'
  detailOpen.value = true
  const query: LocationQueryRaw = { ...route.query, host: host.id }
  delete query.view
  delete query.node
  void router.replace({ path: route.path, query })
}

function closeDetails() {
  detailOpen.value = false
  detailTab.value = 'info'
  resourceNode.value = 'overview'
  resourceHostID.value = ''
  const query = { ...route.query }
  delete query.host
  delete query.view
  delete query.node
  void router.replace({ path: route.path, query })
}

function remove(host: InfrastructureHost) {
  Modal.confirm({
    title: '删除主机',
    content: `确定删除主机“${host.name}”吗？仍被部署配置引用的主机不能删除。`,
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    async onOk() {
      try {
        await client.delete(`/hosts/${host.id}`)
        message.success('主机已删除')
        if (selectedID.value === host.id) closeDetails()
        await refresh()
      } catch (error) {
        message.error(apiErrorMessage(error))
        return Promise.reject(error)
      }
    },
  })
}

async function toggle(host: InfrastructureHost) {
  try {
    await client.patch(`/hosts/${host.id}/status`, { active: !host.is_active })
    message.success(host.is_active ? '主机已停用' : '主机已启用')
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  }
}

function openRuntime(host: InfrastructureHost, kind: 'docker' | 'kubernetes') {
  const current = hosts.value.find(item => item.id === host.id)
  if (hostLoadError.value || !current?.is_active || !host.is_active || !canReadResources.value) return
  const capability = capabilityOf(host, kind)
  if (capability?.runtime_id !== capabilityOf(current, kind)?.runtime_id) return
  if (!capability?.runtime_id) {
    message.info(kind === 'docker' ? '该主机尚未建立 Docker 运行时' : '该主机尚未关联 Kubernetes 集群')
    return
  }
  if (kind === 'kubernetes') {
    const cluster = clusters.value.find(item => item.id === capability.runtime_id)
    if (cluster) manageCluster(cluster)
    return
  }
  selectedID.value = host.id
  resourceHostID.value = host.id
  resourceNode.value = `${kind}:${capability.runtime_id}`
  detailTab.value = 'resources'
  detailOpen.value = true
  void router.replace({
    path: '/hosts',
    query: { ...route.query, host: host.id, view: 'resources', node: resourceNode.value },
  })
}

function openHostResources(host: InfrastructureHost) {
  selectedID.value = host.id
  resourceHostID.value = host.id
  resourceNode.value = 'overview'
  detailTab.value = 'resources'
  detailOpen.value = true
  void router.replace({
    path: '/hosts',
    query: { ...route.query, host: host.id, view: 'resources', node: 'overview' },
  })
}

function openAllResources() {
  selectedID.value = ''
  resourceHostID.value = ''
  resourceNode.value = 'overview'
  detailTab.value = 'resources'
  detailOpen.value = true
  const query: LocationQueryRaw = { ...route.query, view: 'resources', node: 'overview' }
  delete query.host
  void router.replace({ path: '/hosts', query })
}

function openStandaloneRuntime(node: string) {
  selectedID.value = ''
  resourceHostID.value = ''
  resourceNode.value = node
  detailTab.value = 'resources'
  detailOpen.value = true
  const query: LocationQueryRaw = { ...route.query, view: 'resources', node }
  delete query.host
  void router.replace({ path: '/hosts', query })
}

function updateResourceNode(node: string) {
  resourceNode.value = node
  const query: LocationQueryRaw = { ...route.query, view: 'resources', node }
  if (resourceHostID.value) query.host = resourceHostID.value
  else delete query.host
  void router.replace({ path: '/hosts', query })
}

function changeDetailTab(value: string | number) {
  if (!selected.value) return
  if (value === 'resources') openHostResources(selected.value)
  else openDetails(selected.value)
}

function handleHostAction(host: InfrastructureHost, action: string) {
  if (action === 'details') openDetails(host)
  else if (action === 'edit') edit(host)
  else if (action === 'toggle') void toggle(host)
  else if (action === 'delete') remove(host)
  else if (action === 'docker' || action === 'kubernetes') openRuntime(host, action)
}

function selectGroup(key: HostGroupKey) {
  selectedGroupKey.value = key
  if (detailOpen.value) closeDetails()
}

function resetGroupForm() {
  editingGroupID.value = ''
  groupMode.value = 'create'
  Object.assign(groupForm, { name: '', description: '', host_ids: [] })
}

function createGroup() {
  resetGroupForm()
  groupDrawerOpen.value = true
}

function editGroup(environment: InfrastructureEnvironment, mode: Exclude<GroupFormMode, 'create'>) {
  editingGroupID.value = environment.id
  groupMode.value = mode
  Object.assign(groupForm, {
    name: environment.name,
    description: environment.description || '',
    host_ids: environment.hosts.map(host => host.id),
  })
  groupDrawerOpen.value = true
}

async function saveGroup() {
  if (groupMode.value !== 'hosts' && !groupForm.name.trim()) {
    message.error('请输入分组名称')
    return
  }
  groupSaving.value = true
  try {
    let response: { data: { environment: InfrastructureEnvironment } }
    if (!editingGroupID.value) {
      response = await client.post<{ environment: InfrastructureEnvironment }>('/environments', {
        name: groupForm.name.trim(),
        description: groupForm.description.trim(),
        host_ids: groupForm.host_ids,
      })
    } else if (groupMode.value === 'details') {
      response = await client.patch<{ environment: InfrastructureEnvironment }>(`/environments/${editingGroupID.value}`, {
        name: groupForm.name.trim(),
        description: groupForm.description.trim(),
      })
    } else {
      response = await client.put<{ environment: InfrastructureEnvironment }>(`/environments/${editingGroupID.value}/hosts`, {
        host_ids: groupForm.host_ids,
      })
    }
    selectedGroupKey.value = response.data.environment.id
    message.success(editingGroupID.value ? (groupMode.value === 'hosts' ? '分组主机已更新' : '分组信息已更新') : '分组已创建')
    groupDrawerOpen.value = false
    resetGroupForm()
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    groupSaving.value = false
  }
}

async function toggleGroup(environment: InfrastructureEnvironment) {
  try {
    await client.patch(`/environments/${environment.id}/status`, { active: !environment.is_active })
    message.success(environment.is_active ? '分组已停用' : '分组已启用')
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  }
}

function removeGroup(environment: InfrastructureEnvironment) {
  Modal.confirm({
    title: '删除主机分组',
    content: `确定删除分组“${environment.name}”吗？这不会删除组内主机；仍被部署配置引用的分组不能删除。`,
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    async onOk() {
      try {
        await client.delete(`/environments/${environment.id}`)
        if (selectedGroupKey.value === environment.id) selectedGroupKey.value = 'all'
        message.success('分组已删除')
        await refresh()
      } catch (error) {
        message.error(apiErrorMessage(error))
        return Promise.reject(error)
      }
    },
  })
}

function handleGroupAction(environment: InfrastructureEnvironment, action: string) {
  if (action === 'details') editGroup(environment, 'details')
  else if (action === 'hosts') editGroup(environment, 'hosts')
  else if (action === 'toggle') void toggleGroup(environment)
  else if (action === 'delete') removeGroup(environment)
  else if (action === 'environment') void router.push({ path: '/environments', query: { environment: environment.id } })
}

function capabilityName(kind: HostCapabilityKind) {
  if (kind === 'ssh') return 'SSH 命令部署'
  if (kind === 'docker') return 'Docker'
  if (kind === 'local_exec') return '直接终端执行'
  return 'Kubernetes'
}

function capabilityDescription(host: InfrastructureHost, kind: HostCapabilityKind) {
  const capability = capabilityOf(host, kind)
  if (!capability) return ''
  if (kind === 'ssh') return capability.status === 'ready' ? '已验证，可执行部署方案脚本' : 'SSH 连接尚未验证'
  if (kind === 'local_exec') return capability.status === 'ready' ? '可在本地执行已保存的部署脚本' : '当前运行方式不支持终端执行'
  if (capability.status === 'unreachable') return '运行时不可达'
  if (capability.status === 'ready') return capability.version || '运行时已连接'
  if (capability.runtime_id) return '正在检查'
  return kind === 'kubernetes' ? '已识别节点，尚未关联集群' : '尚未建立运行时'
}

function hostRuntimeState(host: InfrastructureHost) {
  if (!host.is_active) return { className: 'inactive', label: '已停用', color: 'default' }
  const runtimes = host.capabilities.filter(capability =>
    (capability.kind === 'docker' || capability.kind === 'kubernetes') && capability.runtime_id,
  )
  if (runtimes.some(capability => capability.status === 'unreachable')) {
    return { className: 'unavailable', label: '运行时不可达', color: 'error' }
  }
  if (runtimes.some(capability => capability.status !== 'ready')) {
    return { className: 'unchecked', label: '正在检查', color: 'warning' }
  }
  if (runtimes.length > 0) return { className: 'ready', label: '运行时已连接', color: 'success' }
  return { className: 'neutral', label: '未关联运行时', color: 'default' }
}

function formatTime(value?: string) {
  return formatDateTime(value, 'zh-CN')
}

watch(() => route.query.create, value => {
  if (!auth.canAny(['cluster.create'])) return
  if (value === '1') create()
  else if (value === 'kubernetes') clusterCreateOpen.value = true
  else return
  const query = { ...route.query }
  delete query.create
  void router.replace({ path: route.path, query })
}, { immediate: true })
watch(() => [route.query.host, route.query.view, route.query.node], syncRouteState)
watch(hostReadContext, () => {
  inventoryRead += 1
  statusRead += 1
  inventoryController?.abort()
  statusController?.abort()
  loading.value = false
  statusRefreshing = false
  initialized = false
  hosts.value = []
  environments.value = []
  clusters.value = []
  selectedID.value = ''
  resourceHostID.value = ''
  detailOpen.value = false
  relationOpen.value = false
  drawerOpen.value = false
  groupDrawerOpen.value = false
  if (mounted) void refresh()
})

onMounted(() => {
  lastStatusRefreshAt = Date.now()
  statusTimer = window.setInterval(refreshVisibleStatuses, statusPollInterval)
  document.addEventListener('visibilitychange', refreshVisibleStatuses)
  window.addEventListener('focus', refreshVisibleStatuses)
  void refresh()
})

onBeforeUnmount(() => {
  mounted = false
  inventoryRead += 1
  statusRead += 1
  inventoryController?.abort()
  statusController?.abort()
  if (statusTimer !== undefined) window.clearInterval(statusTimer)
  document.removeEventListener('visibilitychange', refreshVisibleStatuses)
  window.removeEventListener('focus', refreshVisibleStatuses)
})
</script>

<template>
  <section class="host-page">
    <PageToolbar :title="activeWorkspace === 'clusters' ? '集群' : '主机'" description="">
      <a-button v-if="activeWorkspace === 'hosts' && canReadGroups && auth.canAny(['deployment.create'])" @click="createGroup"><Plus :size="15" />新建主机分组</a-button>
      <a-button v-if="activeWorkspace === 'hosts' && canReadResources" @click="openAllResources"><Container :size="15" />运行资源</a-button>
    </PageToolbar>

    <nav class="infrastructure-tabs" aria-label="基础设施类型">
      <router-link v-if="canReadHosts" to="/hosts" :class="{ active: activeWorkspace === 'hosts' }"><Server :size="16" />主机<span>{{ hosts.length }}</span></router-link>
      <router-link v-if="canReadClusters" to="/hosts?view=clusters" :class="{ active: activeWorkspace === 'clusters' }"><RuntimeBrandIcon kind="kubernetes" />集群<span>{{ clusters.length }}</span></router-link>
    </nav>

    <ClusterInventory v-if="activeWorkspace === 'clusters'" :clusters="clusters" :hosts="hosts" :loading="loading" :error="clusterLoadError" :host-error="hostLoadError" @refresh="refresh" @create="clusterCreateOpen = true" @provision="clusterProvisionOpen = true" @host="openDetails" />

    <div v-else class="host-workspace vben-card">
      <main class="host-table-panel">
        <a-alert v-if="hostLoadError || environmentLoadError || clusterLoadError" class="inventory-error" type="error" show-icon :message="hostLoadError ? '主机列表加载失败' : clusterLoadError ? '集群关联加载失败' : '主机分组加载失败'" :description="hostLoadError || clusterLoadError || environmentLoadError"><template #action><a-button size="small" @click="refresh">重试</a-button></template></a-alert>
        <header class="table-toolbar">
          <div class="table-toolbar-copy">
            <strong>{{ selectedGroup ? selectedGroup.name : '全部主机' }}</strong>
            <small>显示 {{ filteredHosts.length }} / {{ hosts.length }} 台主机</small>
          </div>
          <div class="table-filters">
            <label v-if="canReadGroups" class="host-group-filter"><span class="visually-hidden">主机分组</span><select :value="selectedGroupKey" aria-label="主机分组" @change="selectGroup(($event.target as HTMLSelectElement).value)"><option value="all">全部分组</option><option v-for="group in hostGroups" :key="group.id" :value="group.id">{{ group.name }}</option></select></label>
            <a-dropdown v-if="selectedGroup && auth.canAny(['deployment.update', 'deployment.delete'])" :trigger="['click']"><a-button size="small">分组操作<MoreHorizontal :size="14" /></a-button><template #overlay><a-menu @click="handleGroupAction(selectedGroup!, String($event.key))"><a-menu-item v-if="auth.canAny(['deployment.update'])" key="details">编辑信息</a-menu-item><a-menu-item v-if="auth.canAny(['deployment.update'])" key="hosts">调整主机</a-menu-item><a-menu-item v-if="auth.canAny(['deployment.update'])" key="toggle">{{ selectedGroup.is_active ? '停用分组' : '启用分组' }}</a-menu-item><a-menu-item v-if="auth.canAny(['deployment.delete'])" key="delete" danger>删除分组</a-menu-item></a-menu></template></a-dropdown>
            <a-input v-model:value="searchQuery" allow-clear placeholder="搜索名称、地址、分组或集群" class="host-search" aria-label="搜索主机">
              <template #prefix><Search :size="15" /></template>
            </a-input>
            <a-segmented
              v-model:value="statusFilter"
              :options="[
                { label: '全部', value: 'all' },
                { label: '正常', value: 'ready' },
                { label: '待检查', value: 'warning' },
                { label: '停用', value: 'inactive' },
              ]"
            />
          </div>
          <div class="table-actions">
            <a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />刷新</a-button>
            <a-button v-if="auth.canAny(['cluster.create'])" type="primary" @click="create"><Plus :size="15" />添加主机</a-button>
          </div>
        </header>

        <a-table
          :columns="tableColumns"
          :data-source="filteredHosts"
          :loading="loading"
          :pagination="{ ...hostPagination, showSizeChanger: false }" @change="changeHostPage"
          :scroll="{ x: 1370 }"
          row-key="id"
          size="middle"
          class="host-table resource-list-table"
        >
          <template #bodyCell="{ column, record }: { column: { key: string }, record: InfrastructureHost }">
            <template v-if="column.key === 'name'">
              <button type="button" class="host-name" @click="openDetails(record)">
                <span class="host-icon" :class="{ inactive: !record.is_active }"><Server :size="18" /></span>
                <span><strong>{{ record.name }}</strong><small>{{ record.is_builtin ? '本地主机' : 'SSH 主机' }}<span v-if="record.architecture"> · {{ record.architecture.toUpperCase() }}</span></small></span>
              </button>
            </template>
            <template v-else-if="column.key === 'connection'">
              <div class="two-line-cell">
                <strong>{{ record.is_builtin ? '本地' : `${record.address}:${record.ssh_port}` }}</strong>
                <small>{{ record.is_builtin ? '当前 EDO 运行环境' : `SSH 用户：${record.ssh_username}` }}</small>
              </div>
            </template>
            <template v-else-if="column.key === 'resources'">
              <HostResourceSummary :host="record" :revision="resourceRevision" />
            </template>
            <template v-else-if="column.key === 'groups'">
              <span v-if="!canReadGroups" class="muted-text">无环境查看权限</span>
              <div v-else-if="groupNamesOf(record).length" class="group-tags">
                <a-tag v-for="name in groupNamesOf(record)" :key="name">{{ name }}</a-tag>
              </div>
              <span v-else class="muted-text">—</span>
            </template>
            <template v-else-if="column.key === 'capabilities'">
              <div v-if="record.capabilities.length" class="capability-icons">
                <template v-for="capability in record.capabilities" :key="capability.kind">
                  <button v-if="capability.kind === 'docker'" type="button" class="capability-shortcut" :class="capability.status" :disabled="!canReadResources || !record.is_active || !capability.runtime_id || Boolean(hostLoadError)" :aria-label="`查看 ${record.name} 的 Docker 容器`" :title="`Docker：${capabilityDescription(record, capability.kind)}`" @click="openRuntime(record, 'docker')"><RuntimeBrandIcon kind="docker" /></button>
                  <button v-else-if="capability.kind === (record.mode === 'local' ? 'local_exec' : 'ssh')" type="button" class="capability-shortcut host-terminal-shortcut" :class="capability.status" :disabled="!auth.canAny(['host.terminal.open']) || !hostSupportsTerminal(record) || Boolean(hostLoadError)" :aria-label="`打开 ${record.name} 的主机终端`" :title="auth.canAny(['host.terminal.open']) ? `主机终端：${record.name}` : '没有打开主机终端的权限'" @click="openHostTerminal(record)"><RuntimeBrandIcon :kind="capability.kind" /></button>
                  <span v-else :class="capability.status" :title="`${capabilityName(capability.kind)}：${capabilityDescription(record, capability.kind)}`"><RuntimeBrandIcon :kind="capability.kind" /></span>
                </template>
              </div>
              <span v-else class="muted-text">未配置</span>
            </template>
            <template v-else-if="column.key === 'architecture'">{{ record.architecture ? record.architecture.toUpperCase() : '—' }}</template>
            <template v-else-if="column.key === 'cluster'">
              <span v-if="clusterLoadError" class="muted-text">读取失败</span>
              <div v-else-if="clusterForHost(record, clusters)" class="host-cluster-cell"><button type="button" class="host-cluster-link" @click="manageCluster(clusterForHost(record, clusters)!)"><RuntimeBrandIcon kind="kubernetes" />{{ clusterForHost(record, clusters)?.name }}<ArrowUpRight :size="12" /></button><KubeHostRoleBadge :cluster="clusterForHost(record, clusters)!.id" :host="record.id" /></div>
              <span v-else class="muted-text">—</span>
            </template>
            <template v-else-if="column.key === 'status'">
              <span class="runtime-state" :class="hostRuntimeState(record).className"><i />{{ hostRuntimeState(record).label }}</span>
            </template>
            <template v-else-if="column.key === 'updated_at'">{{ formatTime(record.updated_at) }}</template>
            <template v-else-if="column.key === 'actions'">
              <ResourceRowActions :more="auth.canAny(['cluster.update', 'cluster.delete'])">
                <a-button v-if="canReadResources && runtimeCount(record)" type="link" size="small" @click="openHostResources(record)">资源 {{ runtimeCount(record) }}</a-button>
                <a-button type="link" size="small" @click="openDetails(record)">详情</a-button>
                <template #more>
                      <a-menu-item v-if="auth.canAny(['cluster.update'])" @click="handleHostAction(record, 'edit')">编辑主机</a-menu-item>
                      <a-menu-item v-if="auth.canAny(['cluster.update']) && !record.is_builtin" :disabled="Boolean(clusterLoadError || hostLoadError)" @click="editHostCluster(record)">关联集群</a-menu-item>
                      <a-menu-item v-if="auth.canAny(['cluster.update']) && !record.is_builtin" @click="handleHostAction(record, 'toggle')">{{ record.is_active ? '停用主机' : '启用主机' }}</a-menu-item>
                      <a-menu-divider v-if="auth.canAny(['cluster.delete']) && !record.is_builtin" />
                      <a-menu-item v-if="auth.canAny(['cluster.delete']) && !record.is_builtin" @click="handleHostAction(record, 'delete')" danger>删除主机</a-menu-item>
                </template>
              </ResourceRowActions>
            </template>
          </template>
          <template #emptyText><a-empty :description="hostLoadError ? '主机列表加载失败' : '当前筛选条件下没有主机'" /></template>
        </a-table>
        <label class="host-page-size">每页条数<select :value="hostPagination.pageSize" aria-label="每页条数" @change="changeHostPage({ current: 1, pageSize: Number(($event.target as HTMLSelectElement).value) })"><option v-for="size in [10, 20, 50, 100]" :key="size" :value="size">{{ size }}</option></select></label>
      </main>
    </div>

    <ResourceDetailDrawer v-model:open="detailOpen" class="host-detail-drawer" :title="selected ? selected.name : '运行资源'" :width="detailTab === 'resources' ? 'min(1440px, 96vw)' : 'min(960px, 94vw)'" @close="closeDetails">
      <template v-if="selected && detailTab === 'info'" #actions>
        <a-space>
          <a-button v-if="auth.canAny(['cluster.update'])" @click="edit(selected)"><Pencil :size="14" />编辑</a-button>
          <a-button
            v-if="auth.canAny(['cluster.delete']) && selected.is_builtin"
            danger
            disabled
            title="内置本地主机不能删除，可按需关闭它的主机能力"
          ><Trash2 :size="14" />删除</a-button>
          <a-dropdown v-if="(!selected.is_builtin && auth.canAny(['cluster.update', 'cluster.delete']))" placement="bottomRight">
            <a-button>更多<MoreHorizontal :size="14" /></a-button>
            <template #overlay>
              <a-menu @click="handleHostAction(selected, String($event.key))">
                <a-menu-item v-if="auth.canAny(['cluster.update'])" key="toggle">{{ selected.is_active ? '停用主机' : '启用主机' }}</a-menu-item>
                <a-menu-divider v-if="auth.canAny(['cluster.delete'])" />
                <a-menu-item v-if="auth.canAny(['cluster.delete'])" key="delete" danger>删除主机</a-menu-item>
              </a-menu>
            </template>
          </a-dropdown>
        </a-space>
      </template>

      <div v-if="selected && canReadResources && detailTab === 'info'" class="detail-view-switch">
        <a-segmented
          :value="detailTab"
          :options="[
            { label: '主机信息', value: 'info' },
            { label: `运行资源 (${runtimeCount(selected)})`, value: 'resources' },
          ]"
          @change="changeDetailTab"
        />
      </div>

      <template v-if="detailTab === 'info' && selected">
        <section class="resource-detail-section">
          <h4>{{ t('resourceList.basic') }}</h4>
          <dl class="resource-facts">
            <div class="resource-facts-wide"><dt>系统资源</dt><dd><HostResourceSummary :host="selected" :revision="resourceRevision" /></dd></div>
            <div><dt>连接地址</dt><dd>{{ selected.is_builtin ? '本地' : selected.address + ':' + selected.ssh_port }}</dd></div>
            <div><dt>SSH 用户</dt><dd>{{ selected.is_builtin ? '—' : selected.ssh_username }}</dd></div>
            <div><dt>认证方式</dt><dd>{{ selected.is_builtin ? '本地连接' : selected.ssh_auth_type === 'password' ? 'SSH 密码' : 'SSH 私钥' }}</dd></div>
            <div><dt>主机架构</dt><dd>{{ selected.architecture ? selected.architecture.toUpperCase() : '尚未检测' }}</dd></div>
            <div><dt>{{ t('resourceList.updated') }}</dt><dd>{{ formatTime(selected.updated_at) }}</dd></div>
            <div><dt>所属环境</dt><dd>{{ selectedEnvironmentSummary }}</dd></div>
            <div class="resource-facts-wide"><dt>主机指纹</dt><dd>{{ selected.ssh_host_key_fingerprint || '本地无需 SSH 指纹' }}</dd></div>
          </dl>
        </section>

        <section class="capability-section resource-detail-section">
          <header><h4>{{ selected.is_builtin ? '本地主机能力' : '主机能力' }}</h4></header>
          <div class="capability-cards">
            <article v-for="capability in selected.capabilities.filter(item => item.kind !== 'kubernetes')" :key="capability.kind">
              <span class="runtime-logo" :class="capability.kind"><RuntimeBrandIcon :kind="capability.kind" /></span>
              <div><strong>{{ capabilityName(capability.kind) }}</strong><small>{{ capabilityDescription(selected, capability.kind) }}</small></div>
              <component :is="capability.status === 'ready' ? CheckCircle2 : CircleOff" class="capability-state" :class="[capability.status, { unavailable: capability.status === 'unreachable' }]" />
              <a-button v-if="auth.canAny(['cluster.read']) && (capability.kind === 'docker' || capability.kind === 'kubernetes')" size="small" :disabled="!capability.runtime_id" @click="openRuntime(selected, capability.kind)">查看资源</a-button>
            </article>
            <a-empty v-if="!selected.capabilities.some(item => item.kind !== 'kubernetes')" description="未配置主机能力" />
          </div>
        </section>
        <section v-if="!selected.is_builtin && canReadClusters" class="resource-detail-section host-cluster-section">
          <header><h4>关联集群</h4><a-button v-if="auth.canAny(['cluster.update'])" size="small" :disabled="Boolean(clusterLoadError || hostLoadError)" @click="editHostCluster(selected)"><Link2 :size="14" />{{ selectedCluster ? '更改关联集群' : '关联集群' }}</a-button></header>
          <a-alert v-if="clusterLoadError" type="error" message="集群关联加载失败" :description="clusterLoadError" />
          <button v-else-if="selectedCluster" type="button" class="associated-cluster" @click="manageCluster(selectedCluster)"><span class="runtime-logo kubernetes"><RuntimeBrandIcon kind="kubernetes" /></span><span><strong>{{ selectedCluster.name }}</strong><small>{{ selectedCluster.api_server }}</small><KubeHostRoleBadge :cluster="selectedCluster.id" :host="selected.id" /></span><a-tag :color="selectedCluster.is_active ? 'success' : undefined">{{ selectedCluster.is_active ? '已启用' : '已停用' }}</a-tag><ArrowUpRight :size="16" /></button>
          <p v-else class="muted-text association-empty">未关联集群</p>
        </section>
      </template>
      <RuntimeResourcePanel
        v-else-if="detailTab === 'resources' && canReadResources"
        :hosts="hosts"
        :host-id="resourceHostID"
        :node="resourceNode"
        @update:node="updateResourceNode"
      />
    </ResourceDetailDrawer>

    <a-drawer v-model:open="groupDrawerOpen" :title="groupTitle" width="min(620px, 100vw)">
      <a-form layout="vertical">
        <a-form-item v-if="groupMode !== 'hosts'" label="分组名称" required>
          <a-input v-model:value="groupForm.name" maxlength="128" placeholder="例如：上海测试、海外生产" />
        </a-form-item>
        <a-form-item v-if="groupMode !== 'hosts'" label="分组说明">
          <a-textarea v-model:value="groupForm.description" :rows="3" maxlength="500" placeholder="说明该分组的用途、区域或约束" />
        </a-form-item>
        <a-form-item v-if="groupMode !== 'details'" label="组内主机（可多选）">
          <a-select v-model:value="groupForm.host_ids" mode="multiple" allow-clear placeholder="选择一个或多个主机" :options="groupHostOptions">
            <template #option="{ label, location, capabilities, isActive }">
              <span class="host-option"><strong>{{ label }}<em v-if="!isActive">已停用</em></strong><small>位置：{{ location }}；能力：{{ capabilities }}</small></span>
            </template>
          </a-select>
          <div class="field-hint">一台主机可加入多个分组；移除正被部署引用或正在发布的主机时，服务端会拒绝操作。</div>
        </a-form-item>
      </a-form>
      <template #footer>
        <div class="drawer-actions"><a-button @click="groupDrawerOpen = false">取消</a-button><a-button type="primary" :loading="groupSaving" @click="saveGroup">保存</a-button></div>
      </template>
    </a-drawer>

    <HostDrawer v-model:open="drawerOpen" :host="editingHost" :clusters="clusters" :can-test="auth.canAny(['cluster.execute'])" @create-cluster="clusterCreateOpen = true" @saved="hostSaved" />
    <KubernetesClusterDrawer v-model:open="clusterCreateOpen" :hosts="hosts" :clusters="clusters" :can-associate="auth.canAny(['cluster.update']) && canReadHosts && !hostLoadError && !clusterLoadError" :can-test="auth.canAny(['cluster.execute'])" @created="clusterCreated" />
    <K3sProvisionDrawer v-model:open="clusterProvisionOpen" :hosts="hosts" @completed="clusterProvisionCompleted" />

    <a-drawer :open="relationOpen" :title="relationHost && clusterForHost(relationHost, clusters) ? '更改关联集群' : '关联集群'" width="min(520px, 100vw)" :mask-closable="!relationSaving" @close="!relationSaving && (relationOpen = false)">
      <div v-if="relationHost" class="host-association-summary"><Server :size="20" /><div><strong>{{ relationHost.name }}</strong><small>{{ relationHost.address }}</small></div></div>
      <a-form layout="vertical"><a-form-item label="Kubernetes 集群"><a-select v-model:value="relationClusterID" :disabled="relationSaving" :options="[{ value: '', label: '不关联集群' }, ...clusters.map(cluster => ({ value: cluster.id, label: `${cluster.name}${cluster.is_active ? '' : '（已停用）'}` }))]" /></a-form-item></a-form>
      <p class="field-hint">保存时核对主机与集群节点信息，不匹配无法关联。解除关联不改变集群节点和资源。</p>
      <a-alert v-if="relationHost && clusterForHost(relationHost, clusters) && relationClusterID && relationClusterID !== clusterForHost(relationHost, clusters)?.id" type="warning" show-icon :message="`保存后，EDO 中的关联将从“${clusterForHost(relationHost, clusters)?.name}”更改为所选集群。`" />
      <a-alert v-if="relationError" type="error" show-icon :message="relationError" />
      <template #footer><div class="drawer-actions"><a-button :disabled="relationSaving" @click="relationOpen = false">取消</a-button><a-button type="primary" :loading="relationSaving" @click="saveHostCluster">{{ relationClusterID ? '校验并保存' : '解除关联' }}</a-button></div></template>
    </a-drawer>
  </section>
</template>

<style scoped>
.inventory-error{margin:14px}
.infrastructure-tabs{display:flex;align-items:center;gap:24px;margin:0 0 18px;border-bottom:1px solid var(--edo-border)}.infrastructure-tabs>a{display:flex;align-items:center;gap:8px;position:relative;padding:0 1px 12px;color:var(--edo-muted);font-size:14px;text-decoration:none}.infrastructure-tabs>a.active{color:var(--edo-primary);font-weight:600}.infrastructure-tabs>a.active::after{position:absolute;right:0;bottom:-1px;left:0;height:2px;border-radius:1px;background:var(--edo-primary);content:''}.infrastructure-tabs>a>svg{width:16px;height:16px}.infrastructure-tabs>a>span{min-width:20px;padding:0 5px;border-radius:4px;background:var(--edo-surface-soft);color:var(--edo-muted);font-size:11px;line-height:18px;text-align:center}.host-cluster-link{display:inline-flex;align-items:center;gap:6px;max-width:100%;padding:0;border:0;background:transparent;color:var(--edo-primary);font-size:12px;cursor:pointer;text-align:left}.host-cluster-link :deep(svg){width:15px;height:15px;flex:none}.host-cluster-link:hover{text-decoration:underline}.associated-cluster{display:flex;align-items:center;gap:12px;width:100%;padding:12px 0;border:0;background:transparent;color:var(--edo-text);cursor:pointer;text-align:left}.associated-cluster>span:nth-child(2){flex:1;min-width:0}.associated-cluster strong,.associated-cluster small{display:block}.associated-cluster small{margin-top:4px;color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}.associated-cluster:hover strong{color:var(--edo-primary)}.association-empty{margin:0;padding:10px 0}.host-association-summary{display:flex;align-items:center;gap:12px;margin-bottom:24px;padding-bottom:18px;border-bottom:1px solid var(--edo-border)}.host-association-summary strong,.host-association-summary small{display:block}.host-association-summary small{margin-top:4px;color:var(--edo-muted);font-size:12px}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}
.host-workspace{display:grid;min-height:610px;grid-template-columns:260px minmax(0,1fr);overflow:hidden}.group-panel{display:flex;min-width:0;flex-direction:column;border-right:1px solid var(--edo-border);background:var(--edo-surface-soft)}.group-panel-header{display:flex;height:64px;align-items:center;justify-content:space-between;padding:0 14px;border-bottom:1px solid var(--edo-border)}.group-panel-header strong,.group-panel-header small{display:block}.group-panel-header small{margin-top:2px;color:var(--edo-muted);font-size:12px}.group-list{min-height:0;flex:1;overflow-y:auto;padding:9px}.group-row{position:relative;display:flex;min-height:55px;align-items:center;margin:2px 0;border-radius:9px}.group-row:hover{background:var(--edo-surface)}.group-row.active{background:var(--edo-primary-soft);box-shadow:inset 3px 0 var(--edo-primary)}.group-row.inactive .group-icon,.group-row.inactive .group-copy{opacity:.62}.group-select{display:grid;min-width:0;flex:1;align-items:center;grid-template-columns:32px minmax(0,1fr) auto;gap:8px;padding:8px 8px;border:0;background:transparent;cursor:pointer;text-align:left}.group-icon{display:grid;width:32px;height:32px;place-items:center;border-radius:9px;color:var(--edo-primary);background:var(--edo-surface)}.group-icon.muted{color:var(--edo-muted)}.group-copy{min-width:0}.group-copy strong,.group-copy small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.group-copy small{margin-top:2px;color:var(--edo-muted);font-size:11px}.group-count{min-width:24px;padding:2px 7px;border-radius:999px;color:var(--edo-muted);background:var(--edo-surface);font-size:11px;text-align:center}.group-more{width:28px;flex:0 0 28px;margin-right:4px;padding:0}.host-table-panel{min-width:0;background:var(--edo-surface)}.table-toolbar{display:grid;min-height:64px;align-items:center;grid-template-columns:auto minmax(360px,1fr) auto;gap:14px;padding:10px 14px;border-bottom:1px solid var(--edo-border)}.table-toolbar-copy strong,.table-toolbar-copy small{display:block;white-space:nowrap}.table-toolbar-copy small{margin-top:2px;color:var(--edo-muted);font-size:11px}.table-filters{display:flex;min-width:0;align-items:center;justify-content:center;gap:10px}.host-search{max-width:320px}.table-actions{display:flex;align-items:center;gap:8px}.host-table :deep(.ant-table-thead>tr>th){font-size:12px}.host-table :deep(.ant-table-cell){vertical-align:middle}.host-name{display:flex;min-width:0;align-items:center;gap:10px;padding:0;border:0;color:var(--edo-text);background:transparent;cursor:pointer;text-align:left}.host-name:hover strong{color:var(--edo-primary)}.host-name>span:last-child{min-width:0}.host-name strong,.host-name small,.two-line-cell strong,.two-line-cell small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.host-name small,.two-line-cell small{margin-top:2px;color:var(--edo-muted);font-size:11px}.host-icon{display:grid;width:36px;height:36px;flex:0 0 36px;place-items:center;border-radius:10px;color:var(--edo-primary);background:var(--edo-primary-soft)}.host-icon.inactive{color:var(--edo-muted);background:var(--edo-surface-soft)}.group-tags{display:flex;overflow:hidden;flex-wrap:wrap;gap:4px}.group-tags :deep(.ant-tag){max-width:120px;overflow:hidden;margin:0;text-overflow:ellipsis}.muted-text{color:var(--edo-muted)}.capability-icons{display:flex;align-items:center;gap:6px}.capability-icons>span{position:relative;display:grid;width:27px;height:27px;place-items:center;border-radius:8px;color:var(--edo-muted);background:var(--edo-surface-soft)}.capability-icons>span.ready{color:var(--edo-primary)}.capability-icons>span.unreachable{color:#ef5656}.capability-icons>span::after{position:absolute;right:1px;bottom:1px;width:6px;height:6px;border:1px solid var(--edo-surface);border-radius:50%;background:#d99b25;content:''}.capability-icons>span.ready::after{background:#28b66e}.capability-icons>span.unreachable::after{background:#ef5656}.capability-icons :deep(svg){width:16px;height:16px}.runtime-state{display:inline-flex;align-items:center;gap:7px;white-space:nowrap}.runtime-state i{width:8px;height:8px;border-radius:50%;background:#a8adb7}.runtime-state.ready{color:#1d9459}.runtime-state.ready i{background:#28b66e;box-shadow:0 0 0 4px color-mix(in srgb,#28b66e 12%,transparent)}.runtime-state.unavailable{color:#d74747}.runtime-state.unavailable i{background:#ef5656}.runtime-state.unchecked{color:#b67813}.runtime-state.unchecked i{background:#d99b25}.drawer-title{display:flex;align-items:center;gap:9px}.detail-view-switch{display:flex;justify-content:center;margin:-2px 0 20px;padding-bottom:14px;border-bottom:1px solid var(--edo-border)}.detail-summary{padding:0 2px 16px}.detail-summary span,.detail-summary p{color:var(--edo-muted)}.detail-summary p{margin:4px 0 0}.host-facts{display:grid;grid-template-columns:1fr 1fr;gap:12px}.host-facts>div{min-width:0;padding:14px 16px;border:1px solid var(--edo-border);border-radius:10px;background:var(--edo-surface-soft)}.host-facts small,.host-facts strong{display:block}.host-facts small{margin-bottom:5px;color:var(--edo-muted)}.host-facts .fingerprint{grid-column:1/-1}.host-facts code{overflow-wrap:anywhere;color:var(--edo-muted)}.capability-section{margin-top:28px}.capability-section>header h4,.capability-section>header p{margin:0}.capability-section>header p{margin-top:3px;color:var(--edo-muted)}.capability-cards{display:grid;grid-template-columns:1fr;gap:10px;margin-top:14px}.capability-cards article{display:grid;min-height:80px;align-items:center;grid-template-columns:42px minmax(0,1fr) 20px auto;gap:12px;padding:13px;border:1px solid var(--edo-border);border-radius:11px;background:var(--edo-surface-soft)}.runtime-logo{display:grid;width:42px;height:42px;place-items:center;border-radius:11px;background:var(--edo-surface)}.runtime-logo :deep(svg){width:25px;height:25px}.runtime-logo.ssh,.runtime-logo.local_exec{color:var(--edo-muted)}.runtime-logo.docker{color:#2496ed}.runtime-logo.kubernetes{color:#326ce5}.capability-cards article strong,.capability-cards article small{display:block}.capability-cards article small{margin-top:3px;color:var(--edo-muted)}.capability-state{width:18px}.capability-state.ready{color:#28b66e}.capability-state.unchecked{color:#d99b25}.capability-state.unavailable{color:#ef5656}.host-option strong,.host-option small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.host-option small{color:var(--edo-muted);font-size:11px}.field-hint{margin-top:7px;color:var(--edo-muted);font-size:12px}@media(max-width:1100px){.table-toolbar{grid-template-columns:1fr auto}.table-filters{grid-column:1/-1;grid-row:2;justify-content:flex-start}.host-search{max-width:none;flex:1}}@media(max-width:820px){.host-workspace{grid-template-columns:1fr}.group-panel{max-height:250px;border-right:0;border-bottom:1px solid var(--edo-border)}.group-list{max-height:170px}.table-toolbar{grid-template-columns:1fr}.table-filters{grid-column:auto;grid-row:auto;flex-direction:column;align-items:stretch}.table-actions{justify-content:flex-end}.host-facts{grid-template-columns:1fr}.host-facts .fingerprint{grid-column:auto}}@media(max-width:560px){.table-toolbar{padding:10px}.table-actions{justify-content:stretch}.table-actions .ant-btn{flex:1}.capability-cards article{grid-template-columns:38px minmax(0,1fr) 18px}.capability-cards article>.ant-btn{grid-column:2/-1}.host-detail-drawer :deep(.ant-drawer-content-wrapper){width:100%!important}}
.host-option strong em{margin-left:7px;color:var(--edo-muted);font-size:11px;font-style:normal;font-weight:400}
@media (min-width:821px) and (max-width:1550px){.table-toolbar{grid-template-columns:1fr auto}.table-filters{grid-column:1/-1;grid-row:2;justify-content:flex-start}.host-search{max-width:none;flex:1}}

.host-workspace{display:block;min-height:0}.table-toolbar{grid-template-columns:auto minmax(0,1fr) auto}.table-filters{flex-wrap:wrap;justify-content:flex-start}.host-table :deep(.resource-row-actions .ant-btn-link){border:1px solid var(--edo-border);color:var(--edo-text);padding:0 10px}.detail-view-switch{justify-content:flex-start;margin:0;padding:0 0 12px}.capability-section{margin-top:0}.capability-cards{gap:0;margin-top:12px}.capability-cards article{min-height:58px;grid-template-columns:28px minmax(0,1fr) 18px auto;gap:10px;padding:10px 0;border:0;border-bottom:1px solid var(--edo-border);border-radius:0;background:transparent}.runtime-logo{width:28px;height:28px;border-radius:5px}.runtime-logo :deep(svg){width:19px;height:19px}.capability-cards article small{font-size:12px}.host-name small,.two-line-cell small{font-size:12px}.host-icon{width:28px;height:28px;flex-basis:28px;border-radius:5px}
@media(max-width:1100px){.table-toolbar{grid-template-columns:1fr auto}.table-filters{grid-column:1/-1;grid-row:2}}@media(max-width:640px){.table-toolbar{grid-template-columns:1fr}.table-actions{flex-wrap:wrap}.table-filters{grid-column:auto;grid-row:auto}}
</style>

<style scoped>
.host-cluster-cell{display:flex;align-items:flex-start;flex-direction:column;gap:2px}
.host-page{--host-link:#3859cc;--host-ready:#167645}
:global([data-theme="dark"] .host-page){--host-link:#a5b7ff;--host-ready:#6cdaa5}
.host-page .infrastructure-tabs>a.active,.host-page .host-cluster-link{color:var(--host-link)}
.host-page .runtime-state.ready{color:var(--host-ready)}
.host-page :deep(.host-node-roles .ant-tag-processing){color:var(--host-link)}
.host-workspace :deep(.ant-btn-primary){background:#4057d6;border-color:#4057d6;color:#fff}
.host-group-filter{display:flex;min-width:150px;max-width:100%;align-items:center}
.host-group-filter select,.host-page-size select{min-height:32px;max-width:100%;padding:4px 28px 4px 10px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-surface);color:var(--edo-text);font:inherit;font-size:13px}
.host-group-filter select{width:100%}
.host-group-filter select:focus-visible,.host-page-size select:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
.host-page-size{display:flex;justify-content:flex-end;align-items:center;gap:10px;padding:0 16px 14px;color:var(--edo-muted);font-size:12px}
.host-table :deep(.ant-table-thead>tr>th){background:var(--edo-surface-soft);color:var(--edo-text);border-right:1px solid var(--edo-border)}
.host-table :deep(.ant-table-tbody>tr>td){border-right:1px solid var(--edo-border)}
.host-table :deep(.ant-table-tbody>tr:last-child>td){border-bottom:1px solid var(--edo-border)}
.host-name strong{white-space:normal;overflow-wrap:anywhere}
.visually-hidden{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
.capability-icons>.capability-shortcut{position:relative;display:grid;width:34px;height:34px;place-items:center;padding:0;border:0;border-radius:6px;background:transparent;cursor:pointer;transition:background-color .15s}
.capability-icons>.capability-shortcut:hover:not(:disabled){background:var(--edo-primary-soft)}
.capability-icons>.capability-shortcut:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px}
.capability-icons>.capability-shortcut:disabled{opacity:.48;cursor:not-allowed;background:transparent}
.capability-shortcut :deep(svg){fill:#2496ed}
.host-terminal-shortcut :deep(svg){fill:var(--edo-primary)}
.capability-shortcut::after{position:absolute;right:2px;bottom:2px;width:6px;height:6px;border:1px solid var(--edo-surface);border-radius:50%;background:var(--edo-muted);content:''}
.capability-shortcut.ready::after{background:var(--edo-success,#28b66e)}
.capability-shortcut.unreachable::after{background:var(--edo-error,#ef5656)}
.capability-shortcut.unchecked::after{background:var(--edo-warning,#d99b25)}
.host-detail-drawer :deep(.resource-detail-content){padding:16px 20px}
@media(prefers-reduced-motion:reduce){.capability-icons>.capability-shortcut{transition:none}}
@media(max-width:640px){.host-detail-drawer :deep(.resource-detail-content){padding:12px}}
</style>
