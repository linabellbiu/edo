<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import {
  Boxes,
  CheckCircle2,
  CircleOff,
  Container,
  Folder,
  FolderOpen,
  MoreHorizontal,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Server,
  Trash2,
  UsersRound,
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
  type HostCapabilityKind,
  type InfrastructureEnvironment,
  type InfrastructureHost,
} from '@/api/infrastructure'
import { apiErrorMessage } from '@/api/resources'
import HostDrawer from '@/components/HostDrawer.vue'
import KubernetesClusterDrawer from '@/components/KubernetesClusterDrawer.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'
import RuntimeResourcePanel from '@/components/RuntimeResourcePanel.vue'
import { useAuthStore } from '@/stores/auth'
import { formatDateTime } from '@/utils/time'

interface ClusterOption { id: string; name: string; is_active: boolean }
type HostGroupKey = 'all' | 'ungrouped' | string
type HostStatusFilter = 'all' | 'ready' | 'warning' | 'inactive'
type GroupFormMode = 'create' | 'details' | 'hosts'
type DetailTab = 'info' | 'resources'

const auth = useAuthStore()
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const hosts = ref<InfrastructureHost[]>([])
const environments = ref<InfrastructureEnvironment[]>([])
const clusters = ref<ClusterOption[]>([])
const selectedID = ref('')
const selectedGroupKey = ref<HostGroupKey>('all')
const searchQuery = ref('')
const statusFilter = ref<HostStatusFilter>('all')
const loading = ref(false)
const drawerOpen = ref(false)
const detailOpen = ref(false)
const detailTab = ref<DetailTab>('info')
const resourceNode = ref('overview')
const resourceHostID = ref('')
const clusterCreateOpen = ref(false)
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

const statusPollInterval = 5000
const statusRefreshDeduplicationWindow = 1000
const canReadGroups = computed(() => auth.canAny(['deployment.read']))
const canReadResources = computed(() => auth.canAny(['cluster.read']))
const selected = computed(() => hosts.value.find(host => host.id === selectedID.value))
const selectedGroup = computed(() => environments.value.find(environment => environment.id === selectedGroupKey.value))
const environmentNames = computed(() => new Map(environments.value.map(environment => [environment.id, environment.name])))
const normalizedSearch = computed(() => searchQuery.value.trim().toLocaleLowerCase())
const tableColumns = [
  { title: '主机名称', key: 'name', width: 210 },
  { title: '连接信息', key: 'connection', width: 220 },
  { title: '所属分组', key: 'groups', width: 220 },
  { title: '主机能力', key: 'capabilities', width: 180 },
  { title: '架构', key: 'architecture', width: 86 },
  { title: '状态', key: 'status', width: 126 },
  { title: '更新时间', key: 'updated_at', width: 170 },
  { title: '操作', key: 'actions', fixed: 'right' as const, width: 190 },
]

const selectedEnvironmentSummary = computed(() => {
  if (!selected.value || !canReadGroups.value) return canReadGroups.value ? '' : '无环境查看权限'
  const environmentIDs = environmentIDsOf(selected.value)
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
    const searchable = [host.name, host.address, host.ssh_username, groupNames].join(' ').toLocaleLowerCase()
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
  if (selectedGroupKey.value === 'ungrouped') {
    return searchAndStatusHosts.value.filter(host => environmentIDsOf(host).length === 0)
  }
  return searchAndStatusHosts.value.filter(host => environmentIDsOf(host).includes(selectedGroupKey.value))
})

const ungroupedCount = computed(() => searchAndStatusHosts.value.filter(host => environmentIDsOf(host).length === 0).length)
const visibleEnvironments = computed(() => environments.value.filter(environment => {
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
    resourceHostID.value = host?.id || ''
    resourceNode.value = requestedNode || 'overview'
    if (host) selectedGroupKey.value = 'all'
    detailTab.value = 'resources'
    detailOpen.value = true
    if (requested && requested !== host?.id) {
      const query = { ...route.query }
      delete query.host
      void router.replace({ path: route.path, query })
    }
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
  loading.value = true
  try {
    const [hostItems, environmentItems] = await Promise.all([
      listHosts(),
      canReadGroups.value ? listEnvironments() : Promise.resolve([]),
    ])
    hosts.value = hostItems
    environments.value = environmentItems
    if (!canReadGroups.value) selectedGroupKey.value = 'all'
    else if (selectedGroupKey.value !== 'all' && selectedGroupKey.value !== 'ungrouped' && !environmentItems.some(item => item.id === selectedGroupKey.value)) {
      selectedGroupKey.value = 'all'
    }
    if (canReadResources.value) {
      try {
        const clusterResponse = await client.get<{ clusters: ClusterOption[] }>('/kubernetes/clusters')
        clusters.value = clusterResponse.data.clusters ?? []
      } catch (error) {
        clusters.value = []
        message.error(apiErrorMessage(error))
      }
    } else {
      clusters.value = []
    }
    initialized = true
    syncRouteState()
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    loading.value = false
  }
}

async function refreshStatuses() {
  const now = Date.now()
  if (document.hidden || loading.value || statusRefreshing || now - lastStatusRefreshAt < statusRefreshDeduplicationWindow) return
  statusRefreshing = true
  lastStatusRefreshAt = now
  try {
    const merged = mergeHostStatuses(hosts.value, await listHostStatuses())
    if (!merged) {
      await refresh()
      return
    }
    hosts.value = merged
    syncRouteState()
    statusErrorShown = false
  } catch (error) {
    if (!statusErrorShown) message.error(apiErrorMessage(error))
    statusErrorShown = true
  } finally {
    statusRefreshing = false
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

async function clusterCreated(cluster: ClusterOption) {
  await refresh()
  if (canReadResources.value) openStandaloneRuntime(`kubernetes:${cluster.id}`)
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
  const capability = capabilityOf(host, kind)
  if (!capability?.runtime_id) {
    message.info(kind === 'docker' ? '该主机尚未建立 Docker 运行时' : '该主机尚未关联 Kubernetes 集群')
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
  if (kind === 'local_exec') return capability.status === 'ready' ? '可在本地直接执行受控部署脚本' : '当前运行方式不支持终端执行'
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

onMounted(() => {
  lastStatusRefreshAt = Date.now()
  statusTimer = window.setInterval(refreshVisibleStatuses, statusPollInterval)
  document.addEventListener('visibilitychange', refreshVisibleStatuses)
  window.addEventListener('focus', refreshVisibleStatuses)
  void refresh()
})

onBeforeUnmount(() => {
  if (statusTimer !== undefined) window.clearInterval(statusTimer)
  document.removeEventListener('visibilitychange', refreshVisibleStatuses)
  window.removeEventListener('focus', refreshVisibleStatuses)
})
</script>

<template>
  <section>
    <PageToolbar description="按分组管理主机接入和运行时能力；容器、Pod、日志和终端可直接在对应主机中查看。">
      <a-button v-if="canReadResources" @click="openAllResources"><Container :size="15" />全部运行资源</a-button>
    </PageToolbar>

    <div class="host-workspace vben-card">
      <aside class="group-panel">
        <header class="group-panel-header">
          <div><strong>主机分组</strong><small>{{ canReadGroups ? '按环境组织主机' : '仅显示可访问主机' }}</small></div>
          <a-button v-if="canReadGroups && auth.canAny(['deployment.create'])" type="text" size="small" aria-label="新建主机分组" title="新建主机分组" @click="createGroup"><Plus :size="17" /></a-button>
        </header>

        <div class="group-list">
          <div class="group-row" :class="{ active: selectedGroupKey === 'all' }">
            <button type="button" class="group-select" @click="selectGroup('all')">
              <span class="group-icon"><Boxes :size="17" /></span>
              <span class="group-copy"><strong>全部主机</strong><small>当前可访问的主机</small></span>
              <span class="group-count">{{ searchAndStatusHosts.length }}</span>
            </button>
          </div>
          <div v-if="canReadGroups" class="group-row" :class="{ active: selectedGroupKey === 'ungrouped' }">
            <button type="button" class="group-select" @click="selectGroup('ungrouped')">
              <span class="group-icon muted"><Folder :size="17" /></span>
              <span class="group-copy"><strong>未分组</strong><small>尚未关联环境</small></span>
              <span class="group-count">{{ ungroupedCount }}</span>
            </button>
          </div>
          <div
            v-for="environment in visibleEnvironments"
            :key="environment.id"
            class="group-row"
            :class="{ active: selectedGroupKey === environment.id, inactive: !environment.is_active }"
          >
            <button type="button" class="group-select" @click="selectGroup(environment.id)">
              <span class="group-icon"><component :is="selectedGroupKey === environment.id ? FolderOpen : Folder" :size="17" /></span>
              <span class="group-copy"><strong>{{ environment.name }}</strong><small>{{ environment.description || (environment.is_active ? '已启用' : '已停用') }}</small></span>
              <span class="group-count">{{ groupHostCount(environment.id) }}</span>
            </button>
            <a-dropdown v-if="auth.canAny(['deployment.update', 'deployment.delete'])" :trigger="['click']" placement="bottomRight">
              <a-button type="text" size="small" class="group-more" aria-label="分组操作" @click.stop><MoreHorizontal :size="16" /></a-button>
              <template #overlay>
                <a-menu @click="handleGroupAction(environment, String($event.key))">
                  <a-menu-item v-if="auth.canAny(['deployment.update'])" key="details">编辑信息</a-menu-item>
                  <a-menu-item v-if="auth.canAny(['deployment.update'])" key="hosts">调整主机</a-menu-item>
                  <a-menu-item v-if="auth.canAny(['deployment.update'])" key="toggle">{{ environment.is_active ? '停用分组' : '启用分组' }}</a-menu-item>
                  <a-menu-item key="environment">打开环境管理</a-menu-item>
                  <a-menu-divider v-if="auth.canAny(['deployment.delete'])" />
                  <a-menu-item v-if="auth.canAny(['deployment.delete'])" key="delete" danger>删除分组</a-menu-item>
                </a-menu>
              </template>
            </a-dropdown>
          </div>
          <a-empty v-if="canReadGroups && !visibleEnvironments.length && !loading" :image="undefined" description="还没有自定义分组" />
        </div>

        <button v-if="canReadGroups" type="button" class="environment-link" @click="router.push('/environments')">
          <UsersRound :size="15" />完整环境管理
        </button>
      </aside>

      <main class="host-table-panel">
        <header class="table-toolbar">
          <div class="table-toolbar-copy">
            <strong>{{ selectedGroup ? selectedGroup.name : selectedGroupKey === 'ungrouped' ? '未分组' : '全部主机' }}</strong>
            <small>显示 {{ filteredHosts.length }} / {{ hosts.length }} 台主机</small>
          </div>
          <div class="table-filters">
            <a-input v-model:value="searchQuery" allow-clear placeholder="搜索名称、地址、用户或分组" class="host-search">
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
            <a-button v-if="auth.canAny(['cluster.create'])" @click="clusterCreateOpen = true"><Plus :size="15" />{{ t('kubernetesCluster.action.add') }}</a-button>
            <a-button v-if="auth.canAny(['cluster.create'])" type="primary" @click="create"><Plus :size="15" />添加主机</a-button>
          </div>
        </header>

        <a-table
          :columns="tableColumns"
          :data-source="filteredHosts"
          :loading="loading"
          :pagination="{ pageSize: 12, hideOnSinglePage: true, showSizeChanger: false }"
          :scroll="{ x: 1360 }"
          row-key="id"
          size="middle"
          class="host-table"
        >
          <template #bodyCell="{ column, record }: { column: { key: string }, record: InfrastructureHost }">
            <template v-if="column.key === 'name'">
              <button type="button" class="host-name" @click="openDetails(record)">
                <span class="host-icon" :class="{ inactive: !record.is_active }"><Server :size="18" /></span>
                <span><strong>{{ record.name }}</strong><small>{{ record.is_builtin ? '本地主机' : 'SSH 主机' }}</small></span>
              </button>
            </template>
            <template v-else-if="column.key === 'connection'">
              <div class="two-line-cell">
                <strong>{{ record.is_builtin ? '本地' : `${record.address}:${record.ssh_port}` }}</strong>
                <small>{{ record.is_builtin ? '当前 EDO 运行环境' : `SSH 用户：${record.ssh_username}` }}</small>
              </div>
            </template>
            <template v-else-if="column.key === 'groups'">
              <span v-if="!canReadGroups" class="muted-text">无环境查看权限</span>
              <div v-else-if="groupNamesOf(record).length" class="group-tags">
                <a-tag v-for="name in groupNamesOf(record)" :key="name">{{ name }}</a-tag>
              </div>
              <span v-else class="muted-text">未分组</span>
            </template>
            <template v-else-if="column.key === 'capabilities'">
              <div v-if="record.capabilities.length" class="capability-icons">
                <span v-for="capability in record.capabilities" :key="capability.kind" :class="capability.status" :title="`${capabilityName(capability.kind)}：${capabilityDescription(record, capability.kind)}`">
                  <RuntimeBrandIcon :kind="capability.kind" />
                </span>
              </div>
              <span v-else class="muted-text">未配置</span>
            </template>
            <template v-else-if="column.key === 'architecture'">{{ record.architecture ? record.architecture.toUpperCase() : '—' }}</template>
            <template v-else-if="column.key === 'status'">
              <span class="runtime-state" :class="hostRuntimeState(record).className"><i />{{ hostRuntimeState(record).label }}</span>
            </template>
            <template v-else-if="column.key === 'updated_at'">{{ formatTime(record.updated_at) }}</template>
            <template v-else-if="column.key === 'actions'">
              <a-space :size="0">
                <a-button v-if="canReadResources && runtimeCount(record)" type="link" size="small" @click="openHostResources(record)">资源 {{ runtimeCount(record) }}</a-button>
                <a-button type="link" size="small" @click="openDetails(record)">详情</a-button>
                <a-dropdown placement="bottomRight">
                  <a-button type="link" size="small">更多<MoreHorizontal :size="14" /></a-button>
                  <template #overlay>
                    <a-menu @click="handleHostAction(record, String($event.key))">
                      <a-menu-item key="details">查看详情</a-menu-item>
                      <a-menu-item v-if="auth.canAny(['cluster.update'])" key="edit">编辑主机</a-menu-item>
                      <a-menu-item v-if="auth.canAny(['cluster.read']) && capabilityOf(record, 'docker')?.runtime_id" key="docker">Docker 资源</a-menu-item>
                      <a-menu-item v-if="auth.canAny(['cluster.read']) && capabilityOf(record, 'kubernetes')?.runtime_id" key="kubernetes">Kubernetes 资源</a-menu-item>
                      <a-menu-item v-if="auth.canAny(['cluster.update']) && !record.is_builtin" key="toggle">{{ record.is_active ? '停用主机' : '启用主机' }}</a-menu-item>
                      <a-menu-divider v-if="auth.canAny(['cluster.delete']) && !record.is_builtin" />
                      <a-menu-item v-if="auth.canAny(['cluster.delete']) && !record.is_builtin" key="delete" danger>删除主机</a-menu-item>
                    </a-menu>
                  </template>
                </a-dropdown>
              </a-space>
            </template>
          </template>
          <template #emptyText><a-empty description="当前筛选条件下没有主机" /></template>
        </a-table>
      </main>
    </div>

    <a-drawer v-model:open="detailOpen" width="min(980px, 100vw)" class="host-detail-drawer" @close="closeDetails">
      <template #title>
        <div class="drawer-title">
          <component :is="selected ? Server : Container" :size="19" />
          <span>{{ selected ? selected.name : '全部运行资源' }}</span>
          <a-tag v-if="selected" :color="selected.is_active ? 'success' : 'default'">{{ selected.is_active ? '已启用' : '已停用' }}</a-tag>
        </div>
      </template>
      <template v-if="selected && detailTab === 'info'" #extra>
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

      <div v-if="selected && canReadResources" class="detail-view-switch">
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
        <div class="detail-summary">
          <span>{{ selected.is_builtin ? '本地主机' : 'SSH 主机' }}</span>
          <p>{{ selectedEnvironmentSummary }}</p>
        </div>
        <div class="host-facts">
          <div><small>连接地址</small><strong>{{ selected.is_builtin ? '本地' : `${selected.ssh_username}@${selected.address}:${selected.ssh_port}` }}</strong></div>
          <div><small>认证方式</small><strong>{{ selected.is_builtin ? '本地连接' : selected.ssh_auth_type === 'password' ? 'SSH 密码' : selected.ssh_auth_type === 'legacy' ? '历史凭据' : 'SSH 私钥' }}</strong></div>
          <div><small>主机架构</small><strong>{{ selected.architecture ? selected.architecture.toUpperCase() : '尚未检测，请重新测试连接' }}</strong></div>
          <div><small>更新时间</small><strong>{{ formatTime(selected.updated_at) }}</strong></div>
          <div class="fingerprint"><small>主机指纹</small><code>{{ selected.ssh_host_key_fingerprint || '本地无需 SSH 指纹' }}</code></div>
        </div>

        <section class="capability-section">
          <header><div><h4>{{ selected.is_builtin ? '本地的主机能力' : '主机能力' }}</h4><p>{{ selected.is_builtin ? '本地 Docker 与直接终端执行能力由当前 EDO 运行环境决定。' : 'SSH 用于执行部署方案脚本；Docker 与 Kubernetes 资源可直接在当前主机中查看。' }}</p></div></header>
          <div class="capability-cards">
            <article v-for="capability in selected.capabilities" :key="capability.kind">
              <span class="runtime-logo" :class="capability.kind"><RuntimeBrandIcon :kind="capability.kind" /></span>
              <div><strong>{{ capabilityName(capability.kind) }}</strong><small>{{ capabilityDescription(selected, capability.kind) }}</small></div>
              <component :is="capability.status === 'ready' ? CheckCircle2 : CircleOff" class="capability-state" :class="[capability.status, { unavailable: capability.status === 'unreachable' }]" />
              <a-button v-if="auth.canAny(['cluster.read']) && (capability.kind === 'docker' || capability.kind === 'kubernetes')" size="small" :disabled="!capability.runtime_id" @click="openRuntime(selected, capability.kind)">查看资源</a-button>
            </article>
            <a-empty v-if="!selected.capabilities.length" description="未配置主机能力" />
          </div>
        </section>
      </template>
      <RuntimeResourcePanel
        v-else-if="detailTab === 'resources' && canReadResources"
        :hosts="hosts"
        :host-id="resourceHostID"
        :node="resourceNode"
        @update:node="updateResourceNode"
      />
    </a-drawer>

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
    <KubernetesClusterDrawer v-model:open="clusterCreateOpen" :can-test="auth.canAny(['cluster.execute'])" @created="clusterCreated" />
  </section>
</template>

<style scoped>
.host-workspace{display:grid;min-height:610px;grid-template-columns:260px minmax(0,1fr);overflow:hidden}.group-panel{display:flex;min-width:0;flex-direction:column;border-right:1px solid var(--edo-border);background:var(--edo-surface-soft)}.group-panel-header{display:flex;height:64px;align-items:center;justify-content:space-between;padding:0 14px;border-bottom:1px solid var(--edo-border)}.group-panel-header strong,.group-panel-header small{display:block}.group-panel-header small{margin-top:2px;color:var(--edo-muted);font-size:12px}.group-list{min-height:0;flex:1;overflow-y:auto;padding:9px}.group-row{position:relative;display:flex;min-height:55px;align-items:center;margin:2px 0;border-radius:9px}.group-row:hover{background:var(--edo-surface)}.group-row.active{background:var(--edo-primary-soft);box-shadow:inset 3px 0 var(--edo-primary)}.group-row.inactive .group-icon,.group-row.inactive .group-copy{opacity:.62}.group-select{display:grid;min-width:0;flex:1;align-items:center;grid-template-columns:32px minmax(0,1fr) auto;gap:8px;padding:8px 8px;border:0;background:transparent;cursor:pointer;text-align:left}.group-icon{display:grid;width:32px;height:32px;place-items:center;border-radius:9px;color:var(--edo-primary);background:var(--edo-surface)}.group-icon.muted{color:var(--edo-muted)}.group-copy{min-width:0}.group-copy strong,.group-copy small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.group-copy small{margin-top:2px;color:var(--edo-muted);font-size:11px}.group-count{min-width:24px;padding:2px 7px;border-radius:999px;color:var(--edo-muted);background:var(--edo-surface);font-size:11px;text-align:center}.group-more{width:28px;flex:0 0 28px;margin-right:4px;padding:0}.environment-link{display:flex;align-items:center;justify-content:center;gap:7px;margin:8px;padding:10px;border:1px solid var(--edo-border);border-radius:8px;color:var(--edo-muted);background:var(--edo-surface);cursor:pointer}.environment-link:hover{color:var(--edo-primary);border-color:color-mix(in srgb,var(--edo-primary) 40%,var(--edo-border))}.host-table-panel{min-width:0;background:var(--edo-surface)}.table-toolbar{display:grid;min-height:64px;align-items:center;grid-template-columns:auto minmax(360px,1fr) auto;gap:14px;padding:10px 14px;border-bottom:1px solid var(--edo-border)}.table-toolbar-copy strong,.table-toolbar-copy small{display:block;white-space:nowrap}.table-toolbar-copy small{margin-top:2px;color:var(--edo-muted);font-size:11px}.table-filters{display:flex;min-width:0;align-items:center;justify-content:center;gap:10px}.host-search{max-width:320px}.table-actions{display:flex;align-items:center;gap:8px}.host-table :deep(.ant-table-thead>tr>th){font-size:12px}.host-table :deep(.ant-table-cell){vertical-align:middle}.host-name{display:flex;min-width:0;align-items:center;gap:10px;padding:0;border:0;color:var(--edo-text);background:transparent;cursor:pointer;text-align:left}.host-name:hover strong{color:var(--edo-primary)}.host-name>span:last-child{min-width:0}.host-name strong,.host-name small,.two-line-cell strong,.two-line-cell small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.host-name small,.two-line-cell small{margin-top:2px;color:var(--edo-muted);font-size:11px}.host-icon{display:grid;width:36px;height:36px;flex:0 0 36px;place-items:center;border-radius:10px;color:var(--edo-primary);background:var(--edo-primary-soft)}.host-icon.inactive{color:var(--edo-muted);background:var(--edo-surface-soft)}.group-tags{display:flex;overflow:hidden;flex-wrap:wrap;gap:4px}.group-tags :deep(.ant-tag){max-width:120px;overflow:hidden;margin:0;text-overflow:ellipsis}.muted-text{color:var(--edo-muted)}.capability-icons{display:flex;align-items:center;gap:6px}.capability-icons>span{position:relative;display:grid;width:27px;height:27px;place-items:center;border-radius:8px;color:var(--edo-muted);background:var(--edo-surface-soft)}.capability-icons>span.ready{color:var(--edo-primary)}.capability-icons>span.unreachable{color:#ef5656}.capability-icons>span::after{position:absolute;right:1px;bottom:1px;width:6px;height:6px;border:1px solid var(--edo-surface);border-radius:50%;background:#d99b25;content:''}.capability-icons>span.ready::after{background:#28b66e}.capability-icons>span.unreachable::after{background:#ef5656}.capability-icons :deep(svg){width:16px;height:16px}.runtime-state{display:inline-flex;align-items:center;gap:7px;white-space:nowrap}.runtime-state i{width:8px;height:8px;border-radius:50%;background:#a8adb7}.runtime-state.ready{color:#1d9459}.runtime-state.ready i{background:#28b66e;box-shadow:0 0 0 4px color-mix(in srgb,#28b66e 12%,transparent)}.runtime-state.unavailable{color:#d74747}.runtime-state.unavailable i{background:#ef5656}.runtime-state.unchecked{color:#b67813}.runtime-state.unchecked i{background:#d99b25}.drawer-title{display:flex;align-items:center;gap:9px}.detail-view-switch{display:flex;justify-content:center;margin:-2px 0 20px;padding-bottom:14px;border-bottom:1px solid var(--edo-border)}.detail-summary{padding:0 2px 16px}.detail-summary span,.detail-summary p{color:var(--edo-muted)}.detail-summary p{margin:4px 0 0}.host-facts{display:grid;grid-template-columns:1fr 1fr;gap:12px}.host-facts>div{min-width:0;padding:14px 16px;border:1px solid var(--edo-border);border-radius:10px;background:var(--edo-surface-soft)}.host-facts small,.host-facts strong{display:block}.host-facts small{margin-bottom:5px;color:var(--edo-muted)}.host-facts .fingerprint{grid-column:1/-1}.host-facts code{overflow-wrap:anywhere;color:var(--edo-muted)}.capability-section{margin-top:28px}.capability-section>header h4,.capability-section>header p{margin:0}.capability-section>header p{margin-top:3px;color:var(--edo-muted)}.capability-cards{display:grid;grid-template-columns:1fr;gap:10px;margin-top:14px}.capability-cards article{display:grid;min-height:80px;align-items:center;grid-template-columns:42px minmax(0,1fr) 20px auto;gap:12px;padding:13px;border:1px solid var(--edo-border);border-radius:11px;background:var(--edo-surface-soft)}.runtime-logo{display:grid;width:42px;height:42px;place-items:center;border-radius:11px;background:var(--edo-surface)}.runtime-logo :deep(svg){width:25px;height:25px}.runtime-logo.ssh,.runtime-logo.local_exec{color:var(--edo-muted)}.runtime-logo.docker{color:#2496ed}.runtime-logo.kubernetes{color:#326ce5}.capability-cards article strong,.capability-cards article small{display:block}.capability-cards article small{margin-top:3px;color:var(--edo-muted)}.capability-state{width:18px}.capability-state.ready{color:#28b66e}.capability-state.unchecked{color:#d99b25}.capability-state.unavailable{color:#ef5656}.host-option strong,.host-option small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.host-option small{color:var(--edo-muted);font-size:11px}.field-hint{margin-top:7px;color:var(--edo-muted);font-size:12px}@media(max-width:1100px){.table-toolbar{grid-template-columns:1fr auto}.table-filters{grid-column:1/-1;grid-row:2;justify-content:flex-start}.host-search{max-width:none;flex:1}}@media(max-width:820px){.host-workspace{grid-template-columns:1fr}.group-panel{max-height:250px;border-right:0;border-bottom:1px solid var(--edo-border)}.group-list{max-height:170px}.table-toolbar{grid-template-columns:1fr}.table-filters{grid-column:auto;grid-row:auto;flex-direction:column;align-items:stretch}.table-actions{justify-content:flex-end}.host-facts{grid-template-columns:1fr}.host-facts .fingerprint{grid-column:auto}}@media(max-width:560px){.table-toolbar{padding:10px}.table-actions{justify-content:stretch}.table-actions .ant-btn{flex:1}.capability-cards article{grid-template-columns:38px minmax(0,1fr) 18px}.capability-cards article>.ant-btn{grid-column:2/-1}.host-detail-drawer :deep(.ant-drawer-content-wrapper){width:100%!important}}
.host-option strong em{margin-left:7px;color:var(--edo-muted);font-size:11px;font-style:normal;font-weight:400}
@media (min-width:821px) and (max-width:1550px){.table-toolbar{grid-template-columns:1fr auto}.table-filters{grid-column:1/-1;grid-row:2;justify-content:flex-start}.host-search{max-width:none;flex:1}}
</style>
