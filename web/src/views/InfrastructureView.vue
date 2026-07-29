<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from 'ant-design-vue'
import { ChevronDown, ChevronRight, FileText, LayoutGrid, RefreshCw, Server, TerminalSquare } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

import client from '@/api/client'
import { listHosts, type HostCapability, type InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage, getResources, type ResourceRecord } from '@/api/resources'
import ContainerLogDrawer from '@/components/ContainerLogDrawer.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'
import TerminalDrawer from '@/components/TerminalDrawer.vue'
import { useAuthStore } from '@/stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const { t } = useI18n()
const hosts = ref<InfrastructureHost[]>([])
const docker = ref<ResourceRecord[]>([])
const clusters = ref<ResourceRecord[]>([])
const containers = ref<ResourceRecord[]>([])
const pods = ref<ResourceRecord[]>([])
const loading = ref(false)
const resourceLoading = ref(false)
const namespace = ref('default')
const terminal = ref({ open: false, title: '', path: '' })
const containerLogs = ref({ open: false, title: '', path: '' })
const expandedHosts = ref<Record<string, boolean>>({})
const legacyExpanded = ref(true)
let statusTimer: number | undefined
let statusRefreshing = false
let statusErrorShown = false

type RuntimeCapabilityKind = 'docker' | 'kubernetes'
type RuntimeCapability = HostCapability & { kind: RuntimeCapabilityKind }

const selectedNode = computed(() => String(route.query.node || 'overview'))
const selectedDockerID = computed(() => selectedNode.value.startsWith('docker:') ? selectedNode.value.slice(7) : '')
const selectedClusterID = computed(() => selectedNode.value.startsWith('kubernetes:') ? selectedNode.value.slice(11) : '')
const selectedDocker = computed(() => docker.value.find(item => String(item.id) === selectedDockerID.value))
const selectedCluster = computed(() => clusters.value.find(item => String(item.id) === selectedClusterID.value))
const runtimeHosts = computed(() => hosts.value.filter(host => runtimeCapabilities(host).length > 0))
const linkedRuntimeIDs = computed(() => new Set(runtimeHosts.value.flatMap(host => runtimeCapabilities(host).map(capability => capability.runtime_id).filter(Boolean))))
const unboundDocker = computed(() => docker.value.filter(item => !linkedRuntimeIDs.value.has(String(item.id))))
const unboundClusters = computed(() => clusters.value.filter(item => !linkedRuntimeIDs.value.has(String(item.id))))
const hasUnboundRuntimes = computed(() => unboundDocker.value.length > 0 || unboundClusters.value.length > 0)
const totalRuntimes = computed(() => runtimeHosts.value.reduce((total, host) => total + runtimeCapabilities(host).filter(item => item.runtime_id).length, 0) + unboundDocker.value.length + unboundClusters.value.length)
const hostForSelectedRuntime = computed(() => runtimeHosts.value.find(host => runtimeCapabilities(host).some(capability => capability.runtime_id === (selectedDockerID.value || selectedClusterID.value))))

function runtimeCapabilities(host: InfrastructureHost): RuntimeCapability[] {
  return host.capabilities.filter(
    (capability): capability is RuntimeCapability => capability.kind === 'docker' || capability.kind === 'kubernetes',
  )
}

function runtimeState(capability: RuntimeCapability) {
  if (!capability.runtime_id) return { className: 'unchecked', label: '未关联运行时' }
  if (capability.status === 'ready') return { className: 'ready', label: '运行时已连接' }
  if (capability.status === 'unreachable') return { className: 'unavailable', label: '运行时不可达' }
  return { className: 'unchecked', label: '正在检查' }
}

function setHosts(items: InfrastructureHost[]) {
  hosts.value = items
  for (const host of runtimeHosts.value) {
    if (!(host.id in expandedHosts.value)) expandedHosts.value[host.id] = true
  }
}

async function refresh() {
  loading.value = true
  try {
    const [hostItems, dockerItems, clusterItems] = await Promise.all([
      listHosts(),
      getResources('/docker/endpoints', 'endpoints'),
      getResources('/kubernetes/clusters', 'clusters'),
    ])
    setHosts(hostItems)
    docker.value = dockerItems
    clusters.value = clusterItems
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    loading.value = false
  }
}

async function refreshStatuses() {
  if (statusRefreshing) return
  statusRefreshing = true
  try {
    setHosts(await listHosts())
    statusErrorShown = false
  } catch (error) {
    if (!statusErrorShown) message.error(apiErrorMessage(error))
    statusErrorShown = true
  } finally {
    statusRefreshing = false
  }
}

function choose(node: string) {
  void router.replace({ path: '/hosts', query: { view: 'resources', node } })
}

function chooseCapability(kind: RuntimeCapabilityKind, runtimeID: string) {
  if (!runtimeID) {
    message.info(kind === 'kubernetes' ? '该主机已标记 Kubernetes 能力，但尚未关联集群' : '该主机尚未建立 Docker 运行时')
    return
  }
  choose(`${kind}:${runtimeID}`)
}

async function loadRuntime() {
  resourceLoading.value = true
  try {
    if (selectedDockerID.value) {
      const response = await client.get<{ containers: ResourceRecord[] }>(
        `/docker/endpoints/${selectedDockerID.value}/containers?all=true`,
        { timeout: 35_000 },
      )
      containers.value = response.data.containers ?? []
    }
    if (selectedClusterID.value) {
      pods.value = await getResources(`/kubernetes/clusters/${selectedClusterID.value}/pods?namespace=${encodeURIComponent(namespace.value)}`, 'pods')
    }
  } catch (error) {
    message.error(apiErrorMessage(error))
  } finally {
    resourceLoading.value = false
  }
}

async function ping(kind: RuntimeCapabilityKind, id: string) {
  try {
    await client.post(
      kind === 'docker' ? `/docker/endpoints/${id}/ping` : `/kubernetes/clusters/${id}/ping`,
      undefined,
      { timeout: kind === 'docker' ? 35_000 : 10_000 },
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
  const name = containerName(row)
  containerLogs.value = {
    open: true,
    title: t('containerLogs.title', { name }),
    path: `/api/v1/docker/endpoints/${encodeURIComponent(selectedDockerID.value)}/containers/${encodeURIComponent(String(row.id))}/logs/ws`,
  }
}

watch([selectedDockerID, selectedClusterID], () => {
  if (selectedCluster.value) namespace.value = String(selectedCluster.value.default_namespace || 'default')
  if (selectedDockerID.value || selectedClusterID.value) void loadRuntime()
})

watch(selectedNode, () => {
  if (hostForSelectedRuntime.value) expandedHosts.value[hostForSelectedRuntime.value.id] = true
})

onMounted(() => {
  statusTimer = window.setInterval(() => void refreshStatuses(), 1000)
  void (async () => {
    await refresh()
    if (selectedDockerID.value || selectedClusterID.value) await loadRuntime()
  })()
})

onBeforeUnmount(() => {
  if (statusTimer !== undefined) window.clearInterval(statusTimer)
})
</script>

<template>
  <section>
    <PageToolbar description="按主机浏览 Docker 容器和 Kubernetes Pod，并查看日志或打开容器终端。">
      <a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />刷新</a-button>
    </PageToolbar>

    <div class="infra-workspace vben-card">
      <aside class="resource-tree-panel">
        <header class="resource-tree-header">
          <div><strong>资源导航</strong><small>主机与运行时</small></div>
          <span>{{ totalRuntimes }}</span>
        </header>
        <nav class="resource-tree" role="tree" aria-label="主机与运行资源">
          <button class="overview-node" :class="{ active: selectedNode === 'overview' }" role="treeitem" :aria-selected="selectedNode === 'overview'" @click="choose('overview')">
            <span class="overview-icon"><LayoutGrid /></span>
            <span><strong>资源概览</strong><small>全部主机与运行时</small></span>
            <ChevronRight />
          </button>

          <section v-for="host in runtimeHosts" :key="host.id" class="host-branch" :class="{ active: hostForSelectedRuntime?.id === host.id }" role="none">
            <button class="host-toggle" role="treeitem" :aria-expanded="expandedHosts[host.id]" @click="expandedHosts[host.id] = !expandedHosts[host.id]">
              <span class="host-icon" :class="{ inactive: !host.is_active }"><Server /></span>
              <span class="host-copy"><strong :title="host.name">{{ host.name }}</strong><small>{{ host.is_builtin ? '本地' : host.address }}</small></span>
              <ChevronDown :class="{ collapsed: !expandedHosts[host.id] }" />
            </button>
            <Transition name="tree-collapse">
              <div v-show="expandedHosts[host.id]" class="host-capabilities" role="group">
                <button
                  v-for="capability in runtimeCapabilities(host)"
                  :key="capability.kind"
                  :class="{ active: selectedNode === `${capability.kind}:${capability.runtime_id}`, unavailable: !capability.runtime_id }"
                  role="treeitem"
                  :aria-selected="selectedNode === `${capability.kind}:${capability.runtime_id}`"
                  @click="chooseCapability(capability.kind, capability.runtime_id)"
                >
                  <span class="runtime-icon" :class="capability.kind"><RuntimeBrandIcon :kind="capability.kind" /></span>
                  <span><strong>{{ capability.kind === 'docker' ? 'Docker' : 'Kubernetes' }}</strong><small>{{ runtimeState(capability).label }}</small></span>
                  <span class="runtime-state" :class="runtimeState(capability).className" :title="runtimeState(capability).label" />
                </button>
              </div>
            </Transition>
          </section>

          <section v-if="hasUnboundRuntimes" class="host-branch legacy-branch" role="none">
            <button class="host-toggle" role="treeitem" :aria-expanded="legacyExpanded" @click="legacyExpanded = !legacyExpanded">
              <span class="host-icon legacy"><Server /></span>
              <span class="host-copy"><strong>未关联主机</strong><small>历史或独立运行时</small></span>
              <ChevronDown :class="{ collapsed: !legacyExpanded }" />
            </button>
            <Transition name="tree-collapse">
              <div v-show="legacyExpanded" class="host-capabilities" role="group">
                <button v-for="item in unboundDocker" :key="`docker-${item.id}`" :class="{ active: selectedNode === `docker:${item.id}` }" role="treeitem" :aria-selected="selectedNode === `docker:${item.id}`" @click="choose(`docker:${item.id}`)">
                  <span class="runtime-icon docker"><RuntimeBrandIcon kind="docker" /></span><span><strong>{{ item.name }}</strong><small>{{ item.local ? '本地兼容运行时' : '未关联主机' }}</small></span><ChevronRight />
                </button>
                <button v-for="item in unboundClusters" :key="`kubernetes-${item.id}`" :class="{ active: selectedNode === `kubernetes:${item.id}` }" role="treeitem" :aria-selected="selectedNode === `kubernetes:${item.id}`" @click="choose(`kubernetes:${item.id}`)">
                  <span class="runtime-icon kubernetes"><RuntimeBrandIcon kind="kubernetes" /></span><span><strong>{{ item.name }}</strong><small>独立集群接入</small></span><ChevronRight />
                </button>
              </div>
            </Transition>
          </section>
        </nav>
      </aside>

      <main>
        <div v-if="selectedNode === 'overview'" class="runtime-overview">
          <h3>运行资源概览</h3>
          <p>主机决定接入与归属；这里仅展示 Docker 容器、Kubernetes Pod、日志和终端。</p>
          <div class="overview-grid">
            <article><span><Server /></span><div><small>运行时主机</small><strong>{{ runtimeHosts.length }}</strong></div></article>
            <article><span class="docker"><RuntimeBrandIcon kind="docker" /></span><div><small>Docker 运行时</small><strong>{{ docker.length }}</strong></div></article>
            <article><span class="kubernetes"><RuntimeBrandIcon kind="kubernetes" /></span><div><small>Kubernetes 集群</small><strong>{{ clusters.length }}</strong></div></article>
          </div>
          <a-alert v-if="hasUnboundRuntimes" class="unbound-alert" type="warning" show-icon message="存在未关联主机的历史或独立运行时" description="可继续使用；远程 Docker 主机可在“主机与接入”中补全归属，托管 Kubernetes 集群可以保持独立。" />
        </div>

        <template v-else-if="selectedDocker">
          <header class="runtime-heading">
            <div><span>{{ hostForSelectedRuntime?.name || '未关联主机' }} · Docker</span><h3>{{ selectedDocker.name }}</h3><p>{{ selectedDocker.local ? 'ZRT 本地运行时' : selectedDocker.host }}</p></div>
            <a-button v-if="auth.canAny(['cluster.manage'])" @click="ping('docker', selectedDockerID)">检查连接</a-button>
          </header>
          <ResourceTable :rows="containers" :columns="[{ key: 'names', label: '名称' }, { key: 'image', label: '镜像' }, { key: 'state', label: '状态' }, { key: 'status', label: '详情' }]" :loading="resourceLoading">
            <template #actions="{ row }">
              <a-button type="link" @click="openContainerLogs(row)"><FileText :size="15" />{{ t('containerLogs.button') }}</a-button>
              <a-button v-if="auth.canAny(['terminal.open']) && row.state === 'running'" type="link" @click="openTerminal(`Docker · ${containerName(row)}`, `/api/v1/terminals/docker/${encodeURIComponent(selectedDockerID)}/containers/${encodeURIComponent(String(row.id))}/ws`)"><TerminalSquare :size="15" />终端</a-button>
            </template>
          </ResourceTable>
        </template>

        <template v-else-if="selectedCluster">
          <header class="runtime-heading">
            <div><span>{{ hostForSelectedRuntime?.name || '独立集群' }} · Kubernetes</span><h3>{{ selectedCluster.name }}</h3><p>{{ selectedCluster.api_server || '集群内连接' }}</p></div>
            <div><a-input v-model:value="namespace" style="width:160px" /><a-button :loading="resourceLoading" @click="loadRuntime">加载命名空间</a-button></div>
          </header>
          <ResourceTable :rows="pods" :columns="[{ key: 'name', label: 'Pod' }, { key: 'namespace', label: '命名空间' }, { key: 'phase', label: '阶段' }, { key: 'containers', label: '容器' }]" :loading="resourceLoading">
            <template v-if="auth.canAny(['terminal.open'])" #actions="{ row }">
              <a-button v-for="podContainer in (row.phase === 'Running' && Array.isArray(row.containers) ? row.containers : [])" :key="String(podContainer)" type="link" @click="openTerminal(`Pod · ${row.name} / ${podContainer}`, `/api/v1/terminals/kubernetes/${encodeURIComponent(selectedClusterID)}/namespaces/${encodeURIComponent(String(row.namespace))}/pods/${encodeURIComponent(String(row.name))}/containers/${encodeURIComponent(String(podContainer))}/ws`)">{{ podContainer }}</a-button>
            </template>
          </ResourceTable>
        </template>

        <div v-else class="empty-panel"><a-empty description="选择主机下的运行时" /></div>
      </main>
    </div>

    <TerminalDrawer v-model:open="terminal.open" :title="terminal.title" :path="terminal.path" />
    <ContainerLogDrawer v-model:open="containerLogs.open" :title="containerLogs.title" :path="containerLogs.path" />
  </section>
</template>

<style scoped>
.infra-workspace{display:grid;min-height:610px;grid-template-columns:292px minmax(0,1fr);overflow:hidden}.resource-tree-panel{min-width:0;border-right:1px solid var(--zrt-border);background:color-mix(in srgb,var(--zrt-surface-soft) 82%,var(--zrt-surface))}.resource-tree-header{display:flex;min-height:64px;align-items:center;justify-content:space-between;padding:0 16px}.resource-tree-header strong,.resource-tree-header small{display:block}.resource-tree-header small{margin-top:1px;color:var(--zrt-muted);font-size:12px}.resource-tree-header>span{min-width:28px;padding:3px 8px;border:1px solid var(--zrt-border);border-radius:999px;color:var(--zrt-muted);background:var(--zrt-surface);text-align:center;font-size:12px}.resource-tree{padding:8px}.resource-tree button{outline:0}.resource-tree button:focus-visible{box-shadow:inset 0 0 0 2px color-mix(in srgb,var(--zrt-primary) 44%,transparent)}.overview-node,.host-toggle,.host-capabilities button{display:grid;width:100%;align-items:center;border:0;background:transparent;cursor:pointer;text-align:left}.overview-node{min-height:52px;grid-template-columns:36px minmax(0,1fr) 16px;gap:10px;padding:7px 9px;border-radius:10px}.overview-node:hover,.overview-node.active{background:var(--zrt-surface)}.overview-node.active{box-shadow:0 2px 8px rgb(30 45 75 / 5%)}.overview-icon,.host-icon,.runtime-icon{display:grid;place-items:center;border-radius:9px;background:var(--zrt-surface)}.overview-icon{width:34px;height:34px;color:var(--zrt-primary);background:var(--zrt-primary-soft)}.overview-icon svg{width:17px}.overview-node>span:nth-child(2),.host-copy,.host-capabilities button>span:nth-child(2){min-width:0}.overview-node strong,.overview-node small,.host-copy strong,.host-copy small,.host-capabilities strong,.host-capabilities small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.overview-node strong,.host-copy strong,.host-capabilities strong{font-size:13px;font-weight:600}.overview-node small,.host-copy small,.host-capabilities small{margin-top:1px;color:var(--zrt-muted);font-size:11.5px}.overview-node>svg,.host-toggle>svg,.host-capabilities button>svg{width:14px;color:var(--zrt-muted)}.host-branch{margin-top:7px;padding:5px;border:1px solid var(--zrt-border);border-radius:11px;background:color-mix(in srgb,var(--zrt-surface) 76%,transparent)}.host-branch.active{border-color:color-mix(in srgb,var(--zrt-primary) 28%,var(--zrt-border));background:var(--zrt-surface)}.host-toggle{min-height:50px;grid-template-columns:36px minmax(0,1fr) 16px;gap:10px;padding:7px 8px;border-radius:8px}.host-toggle:hover{background:var(--zrt-surface-soft)}.host-icon{width:34px;height:34px;color:var(--zrt-primary)}.host-icon.inactive,.host-icon.legacy{color:var(--zrt-muted)}.host-icon svg{width:17px}.host-toggle>svg{transition:transform 160ms ease}.host-toggle>svg.collapsed{transform:rotate(-90deg)}.host-capabilities{display:grid;gap:2px;padding:3px 0 1px 44px}.host-capabilities button{position:relative;min-height:47px;grid-template-columns:30px minmax(0,1fr) 14px;gap:8px;padding:5px 7px;border-radius:8px}.host-capabilities button:hover,.host-capabilities button.active{background:var(--zrt-primary-soft)}.host-capabilities button.active::before{position:absolute;top:9px;bottom:9px;left:0;width:2px;border-radius:2px;background:var(--zrt-primary);content:""}.host-capabilities button.active strong{color:var(--zrt-primary)}.host-capabilities button.unavailable{opacity:.72}.runtime-icon{width:28px;height:28px}.runtime-icon :deep(svg){width:18px;height:18px}.runtime-icon.docker{color:#2496ed;background:color-mix(in srgb,#2496ed 10%,var(--zrt-surface))}.runtime-icon.kubernetes{color:#326ce5;background:color-mix(in srgb,#326ce5 10%,var(--zrt-surface))}.runtime-state{width:7px;height:7px;justify-self:center;border-radius:50%;background:#a8adb7}.runtime-state.ready{background:#25b66f;box-shadow:0 0 0 3px color-mix(in srgb,#25b66f 12%,transparent)}.runtime-state.unavailable{background:#ef5656}.runtime-state.unchecked{background:#d99b25}.legacy-branch{border-style:dashed}.tree-collapse-enter-active,.tree-collapse-leave-active{transition:opacity 140ms ease,transform 160ms ease}.tree-collapse-enter-from,.tree-collapse-leave-to{opacity:0;transform:translateY(-4px)}.infra-workspace>main{min-width:0;padding:22px}.runtime-overview h3,.runtime-overview p{margin:0}.runtime-overview p{margin-top:3px;color:var(--zrt-muted)}.overview-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:18px}.overview-grid article{display:flex;min-height:88px;align-items:center;gap:13px;padding:15px;border:1px solid var(--zrt-border);border-radius:12px;background:var(--zrt-surface-soft)}.overview-grid article>span{display:grid;width:44px;height:44px;place-items:center;border-radius:12px;color:var(--zrt-primary);background:var(--zrt-surface)}.overview-grid article>span :deep(svg){width:24px;height:24px}.overview-grid article>span.docker{color:#2496ed}.overview-grid article>span.kubernetes{color:#326ce5}.overview-grid small,.overview-grid strong{display:block}.overview-grid small{color:var(--zrt-muted)}.overview-grid strong{font-size:23px}.unbound-alert{margin-top:16px}.runtime-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;margin-bottom:18px}.runtime-heading h3,.runtime-heading p{margin:0}.runtime-heading h3{margin-top:2px}.runtime-heading p,.runtime-heading span{color:var(--zrt-muted)}.runtime-heading>div:last-child{display:flex;gap:8px}@media(max-width:900px){.infra-workspace{grid-template-columns:1fr}.resource-tree-panel{max-height:390px;overflow:auto;border-right:0;border-bottom:1px solid var(--zrt-border)}.overview-grid{grid-template-columns:1fr}}@media(max-width:620px){.infra-workspace>main{padding:16px}.runtime-heading{flex-direction:column}}
</style>
