<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { CheckCircle2, CircleOff, Pencil, Plus, RefreshCw, Server, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import client from '@/api/client'
import { capabilityOf, environmentIDsOf, listEnvironments, listHosts, type HostCapabilityKind, type InfrastructureEnvironment, type InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage } from '@/api/resources'
import HostDrawer from '@/components/HostDrawer.vue'
import KubernetesClusterDrawer from '@/components/KubernetesClusterDrawer.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import RuntimeBrandIcon from '@/components/RuntimeBrandIcon.vue'
import { useAuthStore } from '@/stores/auth'

interface ClusterOption { id: string; name: string; is_active: boolean }

const auth = useAuthStore()
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const hosts = ref<InfrastructureHost[]>([])
const environments = ref<InfrastructureEnvironment[]>([])
const clusters = ref<ClusterOption[]>([])
const selectedID = ref('')
const loading = ref(false)
const drawerOpen = ref(false)
const clusterCreateOpen = ref(false)
const editingHost = ref<InfrastructureHost>()
let statusTimer: number | undefined
let statusRefreshing = false
let statusErrorShown = false

const selected = computed(() => hosts.value.find(host => host.id === selectedID.value))
const environmentNames = computed(() => new Map(environments.value.map(environment => [environment.id, environment.name])))
const selectedEnvironmentSummary = computed(() => {
  if (!selected.value) return ''
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

async function refresh() {
  loading.value = true
  try {
    const [hostItems, environmentItems, clusterResponse] = await Promise.all([
      listHosts(),
      auth.canAny(['deployment.read']) ? listEnvironments() : Promise.resolve([]),
      auth.canAny(['cluster.read'])
        ? client.get<{ clusters: ClusterOption[] }>('/kubernetes/clusters')
        : Promise.resolve({ data: { clusters: [] as ClusterOption[] } }),
    ])
    hosts.value = hostItems
    environments.value = environmentItems
    clusters.value = clusterResponse.data.clusters ?? []
    if (!hosts.value.some(host => host.id === selectedID.value)) selectedID.value = hosts.value[0]?.id ?? ''
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
    hosts.value = await listHosts()
    if (!hosts.value.some(host => host.id === selectedID.value)) selectedID.value = hosts.value[0]?.id ?? ''
    statusErrorShown = false
  } catch (error) {
    if (!statusErrorShown) message.error(apiErrorMessage(error))
    statusErrorShown = true
  } finally {
    statusRefreshing = false
  }
}

function create() {
  editingHost.value = undefined
  drawerOpen.value = true
}

async function clusterCreated(cluster: ClusterOption) {
  await refresh()
  if (auth.canAny(['cluster.read'])) {
    await router.push({ path: '/hosts', query: { view: 'resources', node: `kubernetes:${cluster.id}` } })
  }
}

function edit(host: InfrastructureHost) {
  editingHost.value = host
  drawerOpen.value = true
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
        if (selectedID.value === host.id) selectedID.value = ''
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
    await refresh()
  } catch (error) {
    message.error(apiErrorMessage(error))
  }
}

function openRuntime(kind: 'docker' | 'kubernetes') {
  const capability = selected.value && capabilityOf(selected.value, kind)
  if (!capability?.runtime_id) {
    message.info(kind === 'docker' ? '该主机尚未建立 Docker 运行时' : '该主机尚未关联 Kubernetes 集群')
    return
  }
  void router.push({ path: '/hosts', query: { view: 'resources', node: `${kind}:${capability.runtime_id}` } })
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
  if (!host.is_active) return { className: 'inactive', label: '已停用' }
  const runtimes = host.capabilities.filter(capability =>
    (capability.kind === 'docker' || capability.kind === 'kubernetes') && capability.runtime_id,
  )
  if (runtimes.some(capability => capability.status === 'unreachable')) {
    return { className: 'unavailable', label: '运行时不可达' }
  }
  if (runtimes.some(capability => capability.status !== 'ready')) {
    return { className: 'unchecked', label: '正在检查' }
  }
  if (runtimes.length > 0) return { className: 'ready', label: '运行时已连接' }
  return { className: 'neutral', label: '未关联运行时' }
}

watch(() => route.query.create, value => {
  if (!auth.canAny(['cluster.manage'])) return
  if (value === '1') create()
  else if (value === 'kubernetes') clusterCreateOpen.value = true
  else return
  const query = { ...route.query }
  delete query.create
  void router.replace({ path: route.path, query })
}, { immediate: true })

onMounted(() => {
  statusTimer = window.setInterval(() => void refreshStatuses(), 1000)
  void refresh()
})

onBeforeUnmount(() => {
  if (statusTimer !== undefined) window.clearInterval(statusTimer)
})
</script>

<template>
  <section>
    <PageToolbar description="维护主机接入、环境归属和运行时能力；容器、Pod、日志和终端统一在“运行资源”中查看。">
      <a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />刷新</a-button>
      <a-button v-if="auth.canAny(['cluster.manage'])" @click="clusterCreateOpen = true"><Plus :size="15" />{{ t('kubernetesCluster.action.add') }}</a-button>
      <a-button v-if="auth.canAny(['cluster.manage'])" type="primary" @click="create"><Plus :size="15" />添加主机</a-button>
    </PageToolbar>

    <div class="host-layout vben-card">
      <aside class="host-list">
        <header>
          <div><strong>主机</strong><small>本地与远程能力</small></div>
          <span>{{ hosts.length }}</span>
        </header>
        <div class="host-list-scroll">
          <button
            v-for="host in hosts"
            :key="host.id"
            :class="{ active: selectedID === host.id }"
            @click="selectedID = host.id"
          >
            <span class="host-icon" :class="{ inactive: !host.is_active }"><Server /></span>
            <span class="host-copy">
              <strong>{{ host.name }}</strong>
              <small>{{ host.is_builtin ? '本地' : `${host.address}:${host.ssh_port}` }}</small>
              <span class="mini-capabilities">
                <i v-for="capability in host.capabilities" :key="capability.kind" :title="capability.kind">
                  <RuntimeBrandIcon :kind="capability.kind" />
                </i>
              </span>
            </span>
            <span class="host-state" :class="hostRuntimeState(host).className" :title="hostRuntimeState(host).label" :aria-label="hostRuntimeState(host).label" />
          </button>
          <a-empty v-if="!hosts.length && !loading" description="还没有主机" />
        </div>
      </aside>

      <main v-if="selected" class="host-detail">
        <header class="detail-header">
          <div class="detail-title">
            <span>{{ selected.is_builtin ? '本地主机' : 'SSH 主机' }}</span>
            <h3>{{ selected.name }}</h3>
            <p>{{ selectedEnvironmentSummary }}</p>
          </div>
          <div class="detail-actions">
            <a-tag :color="selected.is_active ? 'success' : 'default'">{{ selected.is_active ? '已启用' : '已停用' }}</a-tag>
            <a-button v-if="auth.canAny(['cluster.manage'])" @click="edit(selected)"><Pencil :size="14" />编辑</a-button>
            <a-button v-if="auth.canAny(['cluster.manage']) && !selected.is_builtin" @click="toggle(selected)">{{ selected.is_active ? '停用' : '启用' }}</a-button>
            <a-button
              v-if="auth.canAny(['cluster.manage'])"
              danger
              :disabled="selected.is_builtin"
              :title="selected.is_builtin ? '内置本地主机不能删除，可按需关闭它的主机能力' : '删除主机'"
              @click="!selected.is_builtin && remove(selected)"
            ><Trash2 :size="14" />删除</a-button>
          </div>
        </header>

        <div class="host-facts">
          <div><small>连接地址</small><strong>{{ selected.is_builtin ? '本地' : `${selected.ssh_username}@${selected.address}:${selected.ssh_port}` }}</strong></div>
          <div><small>认证方式</small><strong>{{ selected.is_builtin ? '本地连接' : selected.ssh_auth_type === 'password' ? 'SSH 密码' : selected.ssh_auth_type === 'legacy' ? '历史凭据' : 'SSH 私钥' }}</strong></div>
          <div class="fingerprint"><small>主机指纹</small><code>{{ selected.ssh_host_key_fingerprint || '本地无需 SSH 指纹' }}</code></div>
        </div>

        <section class="capability-section">
          <header><div><h4>{{ selected.is_builtin ? '本地的主机能力' : '主机能力' }}</h4><p>{{ selected.is_builtin ? '本地 Docker 与直接终端执行能力由当前 ZRT 运行环境决定。' : 'SSH 用于执行部署方案脚本；Docker 与 Kubernetes 的运行资源在“运行资源”中操作。' }}</p></div></header>
          <div class="capability-cards">
            <article v-for="capability in selected.capabilities" :key="capability.kind">
              <span class="runtime-logo" :class="capability.kind"><RuntimeBrandIcon :kind="capability.kind" /></span>
              <div>
                <strong>{{ capabilityName(capability.kind) }}</strong>
                <small>{{ capabilityDescription(selected, capability.kind) }}</small>
              </div>
              <component :is="capability.status === 'ready' ? CheckCircle2 : CircleOff" class="capability-state" :class="[capability.status, { unavailable: capability.status === 'unreachable' }]" />
              <a-button v-if="capability.kind === 'docker' || capability.kind === 'kubernetes'" size="small" :disabled="!capability.runtime_id" @click="openRuntime(capability.kind)">查看资源</a-button>
            </article>
            <a-empty v-if="!selected.capabilities.length" description="未配置主机能力" />
          </div>
        </section>
      </main>
      <div v-else class="empty-panel"><a-empty description="选择或添加主机" /></div>
    </div>

    <HostDrawer v-model:open="drawerOpen" :host="editingHost" :clusters="clusters" @create-cluster="clusterCreateOpen = true" @saved="refresh" />
    <KubernetesClusterDrawer v-model:open="clusterCreateOpen" @created="clusterCreated" />
  </section>
</template>

<style scoped>
.host-layout{display:grid;min-height:590px;grid-template-columns:290px minmax(0,1fr);overflow:hidden}.host-list{min-width:0;border-right:1px solid var(--zrt-border);background:var(--zrt-surface-soft)}.host-list>header{display:flex;height:64px;align-items:center;justify-content:space-between;padding:0 16px}.host-list>header div strong,.host-list>header div small{display:block}.host-list>header small{margin-top:2px;color:var(--zrt-muted);font-size:12px}.host-list>header>span{min-width:27px;padding:3px 8px;border-radius:999px;color:var(--zrt-muted);background:var(--zrt-surface);text-align:center}.host-list-scroll{max-height:calc(100vh - 230px);overflow-y:auto;padding:0 8px 10px}.host-list-scroll>button{display:grid;width:100%;min-height:72px;align-items:center;grid-template-columns:38px minmax(0,1fr) 8px;gap:10px;margin:3px 0;padding:9px 10px;border:0;border-radius:10px;background:transparent;cursor:pointer;text-align:left}.host-list-scroll>button:hover{background:var(--zrt-surface)}.host-list-scroll>button.active{background:var(--zrt-primary-soft);box-shadow:inset 3px 0 var(--zrt-primary)}.host-icon{display:grid;width:38px;height:38px;place-items:center;border-radius:11px;color:var(--zrt-primary);background:var(--zrt-surface)}.host-icon.inactive{color:var(--zrt-muted)}.host-icon svg{width:19px}.host-copy{min-width:0}.host-copy>strong,.host-copy>small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.host-copy>small{margin-top:2px;color:var(--zrt-muted);font-size:12px}.mini-capabilities{display:flex;gap:5px;margin-top:5px}.mini-capabilities i{display:grid;width:17px;height:17px;place-items:center;color:var(--zrt-muted)}.mini-capabilities :deep(svg){width:14px;height:14px}.host-state{width:8px;height:8px;border-radius:50%;background:#a8adb7}.host-state.ready{background:#28b66e;box-shadow:0 0 0 4px color-mix(in srgb,#28b66e 12%,transparent)}.host-state.unavailable{background:#ef5656;box-shadow:0 0 0 4px color-mix(in srgb,#ef5656 11%,transparent)}.host-state.unchecked{background:#d99b25;box-shadow:0 0 0 4px color-mix(in srgb,#d99b25 11%,transparent)}.host-state.inactive,.host-state.neutral{background:#a8adb7;box-shadow:none}.host-detail{min-width:0;padding:24px}.detail-header{display:flex;align-items:flex-start;justify-content:space-between;gap:20px}.detail-title>span,.detail-title p{color:var(--zrt-muted)}.detail-title h3{margin:3px 0 1px;font-size:22px}.detail-title p{margin:0}.detail-actions{display:flex;align-items:center;gap:8px}.host-facts{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:24px}.host-facts>div{min-width:0;padding:14px 16px;border:1px solid var(--zrt-border);border-radius:10px;background:var(--zrt-surface-soft)}.host-facts small,.host-facts strong{display:block}.host-facts small{margin-bottom:5px;color:var(--zrt-muted)}.host-facts .fingerprint{grid-column:1/-1}.host-facts code{overflow-wrap:anywhere;color:var(--zrt-muted)}.capability-section{margin-top:28px}.capability-section>header h4,.capability-section>header p{margin:0}.capability-section>header p{margin-top:3px;color:var(--zrt-muted)}.capability-cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:12px;margin-top:14px}.capability-cards article{display:grid;min-height:84px;align-items:center;grid-template-columns:44px minmax(0,1fr) 20px auto;gap:12px;padding:14px;border:1px solid var(--zrt-border);border-radius:12px;background:var(--zrt-surface-soft)}.runtime-logo{display:grid;width:44px;height:44px;place-items:center;border-radius:12px;background:var(--zrt-surface)}.runtime-logo :deep(svg){width:27px;height:27px}.runtime-logo.ssh{color:var(--zrt-muted)}.runtime-logo.docker{color:#2496ed}.runtime-logo.kubernetes{color:#326ce5}.capability-cards article strong,.capability-cards article small{display:block}.capability-cards article small{margin-top:3px;color:var(--zrt-muted)}.capability-state{width:18px}.capability-state.ready{color:#28b66e}.capability-state.unchecked{color:#d99b25}.capability-state.unavailable{color:#ef5656}@media(max-width:900px){.host-layout{grid-template-columns:1fr}.host-list{max-height:270px;border-right:0;border-bottom:1px solid var(--zrt-border)}.host-list-scroll{max-height:200px}.capability-cards{grid-template-columns:1fr}}@media(max-width:640px){.host-detail{padding:16px}.detail-header{flex-direction:column}.host-facts{grid-template-columns:1fr}.host-facts .fingerprint{grid-column:auto}}
.runtime-logo.local_exec{color:var(--zrt-muted)}
</style>
