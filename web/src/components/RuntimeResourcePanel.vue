<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import {
  ArrowLeft,
  FileText,
  RefreshCw,
  Server,
  TerminalSquare,
} from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

import client from '@/api/client'
import { type HostCapabilityStatus, type InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import ContainerLogDrawer from '@/components/ContainerLogDrawer.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'
import TerminalDrawer from '@/components/TerminalDrawer.vue'
import { useAuthStore } from '@/stores/auth'

type RuntimeKind = 'docker' | 'kubernetes'

interface DockerEndpoint extends ResourceRecord {
  id: string
  name: string
  host_id?: string
  host?: string
  local?: boolean
  is_active?: boolean
}

interface KubernetesCluster extends ResourceRecord {
  id: string
  name: string
  api_server?: string
  default_namespace?: string
  is_active?: boolean
}

interface RuntimeOption {
  key: string
  id: string
  kind: RuntimeKind
  name: string
  description: string
  hosts: InfrastructureHost[]
  enabled: boolean
}

const props = withDefaults(defineProps<{
  hosts: InfrastructureHost[]
  hostId?: string
  node?: string
}>(), { hostId: '', node: 'overview' })

const emit = defineEmits<{
  'update:node': [value: string]
}>()

const auth = useAuthStore()
const { t } = useI18n()
const dockerEndpoints = ref<DockerEndpoint[]>([])
const kubernetesClusters = ref<KubernetesCluster[]>([])
const containers = ref<ResourceRecord[]>([])
const pods = ref<ResourceRecord[]>([])
const metadataLoading = ref(false)
const resourceLoading = ref(false)
const metadataLoaded = ref(false)
const metadataErrors = ref({ docker: '', kubernetes: '' })
const namespace = ref('default')
const terminal = ref({ open: false, title: '', path: '' })
const containerLogs = ref({ open: false, title: '', path: '' })
let metadataRequest = 0
let resourceRequest = 0
let namespaceRuntimeID = ''

const selectedNode = computed(() => props.node || 'overview')
const selectedHost = computed(() => props.hosts.find(host => host.id === props.hostId))
const linkedDockerIDs = computed(() => new Set(props.hosts.flatMap(host => host.capabilities
  .filter(capability => capability.kind === 'docker' && capability.runtime_id)
  .map(capability => capability.runtime_id))))
const linkedKubernetesIDs = computed(() => new Set(props.hosts.flatMap(host => host.capabilities
  .filter(capability => capability.kind === 'kubernetes' && capability.runtime_id)
  .map(capability => capability.runtime_id))))

function hostsForRuntime(kind: RuntimeKind, runtimeID: string) {
  return props.hosts.filter(host => host.capabilities.some(capability => capability.kind === kind && capability.runtime_id === runtimeID))
}

const allRuntimeOptions = computed<RuntimeOption[]>(() => [
  ...dockerEndpoints.value.map(endpoint => {
    const hosts = hostsForRuntime('docker', endpoint.id)
    return {
      key: `docker:${endpoint.id}`,
      id: endpoint.id,
      kind: 'docker' as const,
      name: endpoint.name,
      description: endpoint.local ? '本地 Docker' : endpoint.host || 'Docker 运行时',
      hosts,
      enabled: endpoint.is_active !== false,
    }
  }),
  ...kubernetesClusters.value.map(cluster => {
    const hosts = hostsForRuntime('kubernetes', cluster.id)
    return {
      key: `kubernetes:${cluster.id}`,
      id: cluster.id,
      kind: 'kubernetes' as const,
      name: cluster.name,
      description: cluster.api_server || 'Kubernetes 集群',
      hosts,
      enabled: cluster.is_active !== false,
    }
  }),
])

const visibleRuntimeOptions = computed(() => {
  if (!props.hostId) return allRuntimeOptions.value
  const host = selectedHost.value
  if (!host) return []
  const keys = new Set(host.capabilities
    .filter(capability => (capability.kind === 'docker' || capability.kind === 'kubernetes') && capability.runtime_id)
    .map(capability => `${capability.kind}:${capability.runtime_id}`))
  return allRuntimeOptions.value.filter(option => keys.has(option.key))
})

const selectedRuntime = computed(() => visibleRuntimeOptions.value.find(option => option.key === selectedNode.value))
const selectedDocker = computed(() => selectedRuntime.value?.kind === 'docker'
  ? dockerEndpoints.value.find(endpoint => endpoint.id === selectedRuntime.value?.id)
  : undefined)
const selectedCluster = computed(() => selectedRuntime.value?.kind === 'kubernetes'
  ? kubernetesClusters.value.find(cluster => cluster.id === selectedRuntime.value?.id)
  : undefined)
const unboundDockerCount = computed(() => dockerEndpoints.value.filter(endpoint => !linkedDockerIDs.value.has(endpoint.id)).length)
const unboundKubernetesCount = computed(() => kubernetesClusters.value.filter(cluster => !linkedKubernetesIDs.value.has(cluster.id)).length)
const unboundCount = computed(() => unboundDockerCount.value + unboundKubernetesCount.value)

function capabilityStatus(runtime: RuntimeOption): HostCapabilityStatus | 'unbound' {
  const scopedHosts = selectedHost.value ? [selectedHost.value] : runtime.hosts
  const statuses = scopedHosts.flatMap(host => host.capabilities
    .filter(capability => capability.kind === runtime.kind && capability.runtime_id === runtime.id)
    .map(capability => capability.status))
  if (statuses.includes('unreachable')) return 'unreachable'
  if (statuses.includes('unchecked')) return 'unchecked'
  if (statuses.includes('ready')) return 'ready'
  return 'unbound'
}

function connectionState(runtime: RuntimeOption) {
  const status = capabilityStatus(runtime)
  if (status === 'ready') return { label: '已连接', color: 'success' }
  if (status === 'unreachable') return { label: '不可达', color: 'error' }
  if (status === 'unchecked') return { label: '检查中', color: 'warning' }
  return { label: '未关联主机', color: 'default' }
}

function runtimeHostSummary(runtime: RuntimeOption) {
  if (selectedHost.value) return `主机：${selectedHost.value.name}`
  if (runtime.hosts.length === 0) return '未关联主机'
  if (runtime.hosts.length === 1) return `主机：${runtime.hosts[0]?.name}`
  return `关联 ${runtime.hosts.length} 台主机：${runtime.hosts.map(host => host.name).join('、')}`
}

function choose(node: string) {
  emit('update:node', node)
}

async function refreshMetadata() {
  const request = ++metadataRequest
  metadataLoading.value = true
  try {
    const [dockerResult, kubernetesResult] = await Promise.allSettled([
      client.get<{ endpoints: DockerEndpoint[] }>('/docker/endpoints'),
      client.get<{ clusters: KubernetesCluster[] }>('/kubernetes/clusters'),
    ])
    if (request !== metadataRequest) return false
    const errors = { docker: '', kubernetes: '' }
    if (dockerResult.status === 'fulfilled') {
      dockerEndpoints.value = dockerResult.value.data.endpoints ?? []
    } else {
      errors.docker = apiErrorMessage(dockerResult.reason)
      message.error(`Docker 运行时加载失败：${errors.docker}`)
    }
    if (kubernetesResult.status === 'fulfilled') {
      kubernetesClusters.value = kubernetesResult.value.data.clusters ?? []
    } else {
      errors.kubernetes = apiErrorMessage(kubernetesResult.reason)
      message.error(`Kubernetes 集群加载失败：${errors.kubernetes}`)
    }
    metadataErrors.value = errors
    metadataLoaded.value = true
    return dockerResult.status === 'fulfilled' || kubernetesResult.status === 'fulfilled'
  } finally {
    if (request === metadataRequest) metadataLoading.value = false
  }
}

function selectedMetadataError() {
  if (selectedNode.value.startsWith('docker:')) return metadataErrors.value.docker
  if (selectedNode.value.startsWith('kubernetes:')) return metadataErrors.value.kubernetes
  return ''
}

async function loadRuntime() {
  const request = ++resourceRequest
  containers.value = []
  pods.value = []
  if (selectedNode.value === 'overview') {
    resourceLoading.value = false
    return
  }
  if (!selectedRuntime.value) {
    if (metadataLoaded.value && !selectedMetadataError()) {
      message.warning('运行时不存在或已被移除')
      choose('overview')
    }
    return
  }
  resourceLoading.value = true
  try {
    if (selectedRuntime.value.kind === 'docker') {
      const response = await client.get<{ containers: ResourceRecord[] }>(
        `/docker/endpoints/${encodeURIComponent(selectedRuntime.value.id)}/containers?all=true`,
        { timeout: 35_000 },
      )
      if (request === resourceRequest) containers.value = response.data.containers ?? []
      return
    }
    const response = await client.get<{ pods: ResourceRecord[] }>(
      `/kubernetes/clusters/${encodeURIComponent(selectedRuntime.value.id)}/pods?namespace=${encodeURIComponent(namespace.value)}`,
    )
    if (request === resourceRequest) pods.value = response.data.pods ?? []
  } catch (error) {
    if (request === resourceRequest) message.error(apiErrorMessage(error))
  } finally {
    if (request === resourceRequest) resourceLoading.value = false
  }
}

function ensureNamespaceForSelection() {
  const cluster = selectedCluster.value
  if (!cluster || namespaceRuntimeID === cluster.id) return
  namespaceRuntimeID = cluster.id
  namespace.value = cluster.default_namespace || 'default'
}

async function refresh() {
  if (!await refreshMetadata()) return
  if (selectedNode.value !== 'overview' && !selectedRuntime.value) {
    if (selectedMetadataError()) return
    message.warning('运行时不存在或已被移除')
    choose('overview')
    return
  }
  ensureNamespaceForSelection()
  await loadRuntime()
}

async function ping() {
  const runtime = selectedRuntime.value
  if (!runtime) return
  try {
    await client.post(
      runtime.kind === 'docker'
        ? `/docker/endpoints/${encodeURIComponent(runtime.id)}/ping`
        : `/kubernetes/clusters/${encodeURIComponent(runtime.id)}/ping`,
      undefined,
      { timeout: runtime.kind === 'docker' ? 35_000 : 10_000 },
    )
    message.success('连接检查通过')
  } catch (error) {
    message.error(apiErrorMessage(error))
  }
}

function openTerminal(title: string, path: string) {
  terminal.value = { open: true, title, path }
}

function containerName(row: ResourceRecord) {
  if (Array.isArray(row.names)) return String(row.names[0] || row.id)
  return String(row.names || row.id)
}

function openContainerLogs(row: ResourceRecord) {
  const runtime = selectedRuntime.value
  if (!runtime || runtime.kind !== 'docker') return
  const name = containerName(row)
  containerLogs.value = {
    open: true,
    title: `容器日志：${name}`,
    path: `/api/v1/docker/endpoints/${encodeURIComponent(runtime.id)}/containers/${encodeURIComponent(String(row.id))}/logs/ws`,
  }
}

watch(() => props.node, async () => {
  if (!metadataLoaded.value) return
  ensureNamespaceForSelection()
  await loadRuntime()
})

onMounted(() => void refresh())
</script>

<template>
  <section class="runtime-resource-panel">
    <header class="runtime-panel-header">
      <div>
        <small>{{ selectedHost ? `所属主机：${selectedHost.name}` : '全部运行位置' }}</small>
        <h3>{{ selectedHost ? '主机运行资源' : '运行资源概览' }}</h3>
        <p>按需查看 Docker 容器、Kubernetes Pod、日志和终端，不会在后台批量拉取资源。</p>
      </div>
      <a-button :loading="metadataLoading || resourceLoading" @click="refresh"><RefreshCw :size="15" />刷新</a-button>
    </header>

    <nav v-if="visibleRuntimeOptions.length" class="runtime-selector" aria-label="选择运行时">
      <button type="button" :class="{ active: selectedNode === 'overview' }" @click="choose('overview')">
        <span class="overview-runtime"><Server :size="18" /></span>
        <span><strong>概览</strong><small>{{ visibleRuntimeOptions.length }} 个运行时</small></span>
      </button>
      <button
        v-for="runtime in visibleRuntimeOptions"
        :key="runtime.key"
        type="button"
        :class="{ active: selectedNode === runtime.key, inactive: !runtime.enabled }"
        @click="choose(runtime.key)"
      >
        <span class="runtime-brand" :class="runtime.kind"><RuntimeBrandIcon :kind="runtime.kind" /></span>
        <span><strong>{{ runtime.name }}</strong><small>{{ runtime.kind === 'docker' ? 'Docker' : 'Kubernetes' }} · {{ connectionState(runtime).label }}</small></span>
      </button>
    </nav>

    <div v-if="metadataErrors.docker || metadataErrors.kubernetes" class="metadata-alerts">
      <a-alert v-if="metadataErrors.docker" type="error" show-icon :message="`Docker 运行时加载失败：${metadataErrors.docker}`" />
      <a-alert v-if="metadataErrors.kubernetes" type="error" show-icon :message="`Kubernetes 集群加载失败：${metadataErrors.kubernetes}`" />
    </div>

    <a-spin :spinning="metadataLoading">
      <div v-if="selectedNode === 'overview'" class="runtime-overview">
        <div class="overview-stats">
          <article><small>Docker 运行时</small><strong>{{ visibleRuntimeOptions.filter(item => item.kind === 'docker').length }}</strong></article>
          <article><small>Kubernetes 集群</small><strong>{{ visibleRuntimeOptions.filter(item => item.kind === 'kubernetes').length }}</strong></article>
          <article v-if="!selectedHost"><small>未关联主机</small><strong>{{ unboundCount }}</strong></article>
        </div>

        <a-alert
          v-if="!selectedHost && unboundCount"
          type="warning"
          show-icon
          message="存在未关联主机的历史或独立运行时"
          description="这些运行时仍可查看和使用，不会因页面合并而丢失。"
        />

        <div v-if="visibleRuntimeOptions.length" class="runtime-card-list">
          <button v-for="runtime in visibleRuntimeOptions" :key="runtime.key" type="button" @click="choose(runtime.key)">
            <span class="runtime-brand large" :class="runtime.kind"><RuntimeBrandIcon :kind="runtime.kind" /></span>
            <span class="runtime-card-copy">
              <strong>{{ runtime.name }}</strong>
              <small>{{ runtimeHostSummary(runtime) }}</small>
              <em>{{ runtime.description }}</em>
            </span>
            <span class="runtime-card-tags">
              <a-tag :color="runtime.enabled ? 'success' : 'default'">{{ runtime.enabled ? '已启用' : '已停用' }}</a-tag>
              <a-tag :color="connectionState(runtime).color">{{ connectionState(runtime).label }}</a-tag>
            </span>
          </button>
        </div>
        <a-empty v-else-if="metadataLoaded" description="当前没有可查看的运行资源" />
      </div>

      <div v-else-if="selectedRuntime" class="runtime-detail">
        <header class="runtime-heading">
          <div>
            <button type="button" class="back-link" @click="choose('overview')"><ArrowLeft :size="14" />返回资源概览</button>
            <span>{{ runtimeHostSummary(selectedRuntime) }}</span>
            <h3>{{ selectedRuntime.name }}</h3>
            <p>{{ selectedRuntime.description }}</p>
          </div>
          <div class="runtime-heading-actions">
            <template v-if="selectedCluster">
              <a-input v-model:value="namespace" aria-label="Kubernetes 命名空间" placeholder="命名空间" />
              <a-button :loading="resourceLoading" @click="loadRuntime">加载</a-button>
            </template>
            <a-button v-if="auth.canAny(['cluster.execute'])" @click="ping">检查连接</a-button>
          </div>
        </header>

        <ResourceTable
          v-if="selectedDocker"
          :rows="containers"
          :columns="[
            { key: 'names', label: '名称' },
            { key: 'image_display', label: '镜像版本' },
            { key: 'state', label: '状态' },
            { key: 'status', label: '详情' },
          ]"
          :loading="resourceLoading"
          empty-text="该 Docker 运行时暂无容器"
        >
          <template #actions="{ row }">
            <a-button type="link" @click="openContainerLogs(row)"><FileText :size="15" />{{ t('containerLogs.button') }}</a-button>
            <a-button
              v-if="auth.canAny(['terminal.open']) && row.state === 'running'"
              type="link"
              @click="openTerminal(`容器终端：${containerName(row)}`, `/api/v1/terminals/docker/${encodeURIComponent(selectedRuntime.id)}/containers/${encodeURIComponent(String(row.id))}/ws`)"
            ><TerminalSquare :size="15" />终端</a-button>
          </template>
        </ResourceTable>

        <ResourceTable
          v-else-if="selectedCluster"
          :rows="pods"
          :columns="[
            { key: 'name', label: 'Pod' },
            { key: 'namespace', label: '命名空间' },
            { key: 'phase', label: '阶段' },
            { key: 'containers', label: '容器' },
          ]"
          :loading="resourceLoading"
          empty-text="当前命名空间暂无 Pod"
        >
          <template v-if="auth.canAny(['terminal.open'])" #actions="{ row }">
            <a-button
              v-for="podContainer in (row.phase === 'Running' && Array.isArray(row.containers) ? row.containers : [])"
              :key="String(podContainer)"
              type="link"
              @click="openTerminal(`Pod 终端：${row.name}（容器：${podContainer}）`, `/api/v1/terminals/kubernetes/${encodeURIComponent(selectedRuntime.id)}/namespaces/${encodeURIComponent(String(row.namespace))}/pods/${encodeURIComponent(String(row.name))}/containers/${encodeURIComponent(String(podContainer))}/ws`)"
            >{{ podContainer }}</a-button>
          </template>
        </ResourceTable>
      </div>
    </a-spin>

    <TerminalDrawer v-model:open="terminal.open" :title="terminal.title" :path="terminal.path" />
    <ContainerLogDrawer v-model:open="containerLogs.open" :title="containerLogs.title" :path="containerLogs.path" />
  </section>
</template>

<style scoped>
.runtime-resource-panel{min-width:0}.runtime-panel-header{display:flex;align-items:flex-start;justify-content:space-between;gap:16px}.runtime-panel-header h3,.runtime-panel-header p{margin:0}.runtime-panel-header h3{margin-top:2px;font-size:19px}.runtime-panel-header small,.runtime-panel-header p{color:var(--edo-muted)}.runtime-panel-header p{margin-top:3px}.runtime-selector{display:flex;overflow-x:auto;gap:8px;margin:18px 0;padding-bottom:4px}.runtime-selector button{display:grid;min-width:150px;align-items:center;grid-template-columns:34px minmax(0,1fr);gap:9px;padding:8px 11px;border:1px solid var(--edo-border);border-radius:10px;color:var(--edo-text);background:var(--edo-surface);cursor:pointer;text-align:left}.runtime-selector button:hover,.runtime-selector button.active{border-color:color-mix(in srgb,var(--edo-primary) 42%,var(--edo-border));background:var(--edo-primary-soft)}.runtime-selector button.active{box-shadow:inset 0 0 0 1px color-mix(in srgb,var(--edo-primary) 35%,transparent)}.runtime-selector button.inactive{opacity:.65}.runtime-selector strong,.runtime-selector small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.runtime-selector small{margin-top:1px;color:var(--edo-muted);font-size:11px}.overview-runtime,.runtime-brand{display:grid;width:32px;height:32px;place-items:center;border-radius:9px}.overview-runtime{color:var(--edo-primary);background:var(--edo-surface)}.runtime-brand :deep(svg){width:20px;height:20px}.runtime-brand.docker{color:#2496ed;background:color-mix(in srgb,#2496ed 10%,var(--edo-surface))}.runtime-brand.kubernetes{color:#326ce5;background:color-mix(in srgb,#326ce5 10%,var(--edo-surface))}.runtime-brand.large{width:43px;height:43px;border-radius:11px}.runtime-brand.large :deep(svg){width:25px;height:25px}.metadata-alerts{display:grid;gap:8px;margin-bottom:14px}.runtime-overview{display:grid;gap:14px}.overview-stats{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px}.overview-stats article{padding:14px 16px;border:1px solid var(--edo-border);border-radius:11px;background:var(--edo-surface-soft)}.overview-stats small,.overview-stats strong{display:block}.overview-stats small{color:var(--edo-muted)}.overview-stats strong{margin-top:2px;font-size:23px}.runtime-card-list{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.runtime-card-list>button{display:grid;min-width:0;align-items:center;grid-template-columns:43px minmax(0,1fr) auto;gap:12px;padding:13px;border:1px solid var(--edo-border);border-radius:11px;color:var(--edo-text);background:var(--edo-surface);cursor:pointer;text-align:left}.runtime-card-list>button:hover{border-color:color-mix(in srgb,var(--edo-primary) 38%,var(--edo-border));background:var(--edo-primary-soft)}.runtime-card-copy{min-width:0}.runtime-card-copy strong,.runtime-card-copy small,.runtime-card-copy em{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.runtime-card-copy small,.runtime-card-copy em{margin-top:2px;color:var(--edo-muted);font-size:11px}.runtime-card-copy em{font-style:normal}.runtime-card-tags{display:grid;justify-items:end;gap:4px}.runtime-card-tags :deep(.ant-tag){margin:0}.runtime-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;margin-bottom:17px;padding-top:3px}.runtime-heading h3,.runtime-heading p{margin:0}.runtime-heading h3{margin-top:2px}.runtime-heading p,.runtime-heading>div>span{color:var(--edo-muted)}.back-link{display:flex;align-items:center;gap:5px;margin:0 0 9px;padding:0;border:0;color:var(--edo-primary);background:transparent;cursor:pointer}.runtime-heading-actions{display:flex;align-items:center;gap:8px}.runtime-heading-actions :deep(.ant-input){width:160px}@media(max-width:760px){.runtime-panel-header,.runtime-heading{flex-direction:column}.runtime-card-list{grid-template-columns:1fr}.overview-stats{grid-template-columns:repeat(2,minmax(0,1fr))}.runtime-heading-actions{width:100%;flex-wrap:wrap}.runtime-heading-actions :deep(.ant-input){min-width:0;flex:1}}@media(max-width:460px){.overview-stats{grid-template-columns:1fr}.runtime-selector button{min-width:135px}.runtime-card-list>button{grid-template-columns:43px minmax(0,1fr)}.runtime-card-tags{grid-column:2;grid-auto-flow:column;justify-content:start;justify-items:start}}
</style>
