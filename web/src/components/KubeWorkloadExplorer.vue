<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Box, ChevronRight, FileText, History, RefreshCw, TerminalSquare } from 'lucide-vue-next'
import client from '@/api/client'
import { kubeErrorMessage, kubeTypes, resourceURL, type KubeObject, type KubeType } from '@/api/kubernetes'
import { resourceStatus, type KubeReference } from '@/utils/kubernetesPresentation'
import { podContainerChoices } from '@/utils/kubernetesWorkspace'
import { initialWorkloadRevisionUID, revisionOwnedPods, selectedWorkloadPod, selectedWorkloadRevision, sortWorkloadRevisions, workloadConditionTone, type WorkloadExplorerSelection, type WorkloadRevision } from '@/utils/kubernetesWorkloadExplorer'
import { formatDateTime } from '@/utils/time'

const KubeResourceDiagnostics = defineAsyncComponent(() => import('./KubeResourceDiagnostics.vue'))
const props = defineProps<{
  cluster: string
  object: KubeObject
  type: KubeType
  pods: KubeObject[]
  podsLoading: boolean
  podsError: string
  podsPartial: boolean
  events: KubeObject[]
  eventsError: boolean
  autoRefresh: boolean
  refreshToken: number
  can: (permission: string) => boolean
  canFollow: (reference: KubeReference) => boolean
  disabled?: boolean
  selection?: WorkloadExplorerSelection
}>()
const emit = defineEmits<{
  follow: [reference: KubeReference]
  logs: [pod: KubeObject, container: string]
  terminal: [pod: KubeObject, container: string]
  retryPods: []
  selection: [snapshot: WorkloadExplorerSelection]
}>()
const { t } = useI18n()
const deployment = computed(() => props.object.kind === 'Deployment')
const readable = computed(() => props.can('kubernetes.workload.read'))
const namespace = computed(() => String(props.object.metadata?.namespace ?? ''))
const revisions = ref<WorkloadRevision[]>([]), historyLoading = ref(false), historyError = ref(''), historyState = ref('')
const revisionUID = ref(''), podUID = ref(''), containerName = ref(''), podTab = ref('summary')
let revisionChosen = false, podChosen = false, disposed = false, revision = 0
let request: AbortController | undefined, timer: ReturnType<typeof setInterval> | undefined
const context = computed(() => `${props.cluster}/${props.type.resource}/${namespace.value}/${props.object.metadata?.uid ?? ''}`)
const selectedRevision = computed(() => selectedWorkloadRevision(revisions.value, revisionUID.value))
const visiblePods = computed(() => !readable.value ? [] : deployment.value
  ? selectedRevision.value ? revisionOwnedPods(props.pods, selectedRevision.value.uid, namespace.value) : []
  : props.pods.filter(pod => pod.kind === 'Pod' && pod.metadata?.uid && pod.metadata.namespace === namespace.value))
const selectedPod = computed(() => selectedWorkloadPod(visiblePods.value, podUID.value, namespace.value))
const podMissing = computed(() => podChosen && Boolean(podUID.value) && !selectedPod.value)
const choices = computed(() => selectedPod.value ? podContainerChoices(selectedPod.value) : [])
const selectedContainer = computed(() => choices.value.find(container => container.name === containerName.value))
const selectedContainerImage = computed(() => {
  const spec = selectedPod.value?.spec
  return [...(spec?.containers ?? []), ...(spec?.initContainers ?? []), ...(spec?.ephemeralContainers ?? [])].find(container => container.name === containerName.value)?.image
})
const selectedPodState = computed(() => selectedPod.value ? resourceStatus(selectedPod.value, t) : undefined)
const podType = kubeTypes.find(type => type.resource === 'pods')!
const actionDisabled = computed(() => Boolean(props.disabled) || props.podsLoading || Boolean(props.podsError) || deployment.value && Boolean(historyError.value) || !readable.value || !selectedPod.value || !selectedContainer.value)
const terminalAllowed = computed(() => props.can('terminal.open') && selectedContainer.value?.running === true && !selectedPod.value?.metadata?.deletionTimestamp)

function count(value: unknown) { return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? String(value) : '—' }
function podState(pod: KubeObject) { return resourceStatus(pod, t) }
function badgeStatus(tone?: string): 'success' | 'warning' | 'error' | 'default' | 'processing' {
  return tone === 'success' || tone === 'warning' || tone === 'error' ? tone : tone === 'processing' || tone === 'blue' ? 'processing' : 'default'
}
function conditionLabel(type: string) {
  return ({ PodReadyToStartContainers: '容器环境', Initialized: '初始化', Ready: '就绪检查', ContainersReady: '容器就绪', PodScheduled: '节点调度' } as Record<string, string>)[type] ?? type
}
function reference(pod: KubeObject): KubeReference { return { resource: 'pods', name: pod.metadata.name, namespace: pod.metadata.namespace, uid: pod.metadata.uid } }
function followPod() { if (selectedPod.value && !props.disabled && props.canFollow(reference(selectedPod.value))) emit('follow', reference(selectedPod.value)) }
function followNode() {
  const name = selectedPod.value?.spec?.nodeName
  if (name && !props.disabled && props.canFollow({ resource: 'nodes', name })) emit('follow', { resource: 'nodes', name })
}
function choosePod(pod: KubeObject) {
  if (props.disabled || !readable.value || !selectedWorkloadPod(visiblePods.value, pod.metadata?.uid, namespace.value)) return
  podUID.value = pod.metadata.uid; podChosen = true; containerName.value = ''; podTab.value = 'summary'
}
function chooseRevision(item: WorkloadRevision) {
  if (props.disabled || !revisions.value.some(revision => revision.uid === item.uid)) return
  revisionUID.value = item.uid; revisionChosen = true
  podUID.value = ''; podChosen = false; containerName.value = ''; podTab.value = 'summary'
}
function openContainer(terminal = false) {
  // 每次点击重新取当前 UID 与容器状态，失效选择不会自动切到另一个 Pod。
  const pod = selectedWorkloadPod(visiblePods.value, podUID.value, namespace.value)
  const container = pod ? podContainerChoices(pod).find(choice => choice.name === containerName.value) : undefined
  if (actionDisabled.value || !pod || !container || terminal && (!props.can('terminal.open') || !container.running || pod.metadata?.deletionTimestamp)) return
  if (terminal) emit('terminal', pod, container.name)
  else emit('logs', pod, container.name)
}
async function loadHistory() {
  if (!deployment.value || !props.cluster || !props.object.metadata?.uid || !readable.value) return
  request?.abort(); const controller = new AbortController(), current = ++revision, key = context.value
  request = controller; historyLoading.value = true
  try {
    const { data } = await client.get(`${resourceURL(props.cluster, props.type, props.object.metadata.name)}/diagnostics`, { params: { namespace: namespace.value, uid: props.object.metadata.uid }, signal: controller.signal })
    if (disposed || current !== revision || key !== context.value || controller.signal.aborted || !readable.value) return
    if (data.uid !== props.object.metadata.uid) throw new Error('工作负载已发生变化，请重新打开详情')
    revisions.value = sortWorkloadRevisions((Array.isArray(data.revisions) ? data.revisions : []).filter((item: WorkloadRevision) => typeof item.uid === 'string' && item.uid))
    historyError.value = ''; historyState.value = String(data.state ?? '')
    if (!revisionChosen) { revisionUID.value = initialWorkloadRevisionUID(revisions.value); revisionChosen = Boolean(revisionUID.value) }
  } catch (error) {
    if (!disposed && current === revision && key === context.value && !controller.signal.aborted) historyError.value = kubeErrorMessage(error)
  } finally { if (!disposed && current === revision) historyLoading.value = false }
}
watch(context, () => {
  revision++; request?.abort(); historyLoading.value = false; historyError.value = ''; historyState.value = ''; revisions.value = []
  const saved = props.selection
  revisionUID.value = saved?.revisionUID ?? ''; podUID.value = saved?.podUID ?? ''; containerName.value = saved?.containerName ?? ''; podTab.value = saved?.podTab === 'diagnostics' ? 'diagnostics' : 'summary'
  revisionChosen = Boolean(revisionUID.value); podChosen = Boolean(podUID.value)
  void loadHistory()
}, { immediate: true })
watch([revisionUID, podUID, containerName, podTab], () => emit('selection', {
  revisionUID: revisionUID.value, podUID: podUID.value, containerName: containerName.value, podTab: podTab.value,
}))
watch([visiblePods, () => props.disabled, () => props.podsLoading], ([pods]) => {
  if (!podChosen && !props.disabled && !props.podsLoading && pods.length) choosePod(pods[0]!)
}, { immediate: true })
watch(choices, containers => {
  if (!containerName.value && containers.length) containerName.value = containers.find(container => container.kind === 'app')?.name ?? containers[0]!.name
}, { immediate: true })
watch(() => props.refreshToken, () => { if (deployment.value) void loadHistory() })
watch(readable, value => {
  if (value) { if (deployment.value) void loadHistory() }
  else { revision++; request?.abort(); historyLoading.value = false; historyState.value = ''; revisions.value = []; revisionUID.value = ''; podUID.value = ''; containerName.value = ''; revisionChosen = false; podChosen = false }
})
function syncVisible() { if (!document.hidden && props.autoRefresh && !props.disabled && !historyLoading.value && deployment.value) void loadHistory() }
onMounted(() => { timer = setInterval(syncVisible, 30_000); document.addEventListener('visibilitychange', syncVisible) })
onBeforeUnmount(() => { disposed = true; revision++; request?.abort(); if (timer) clearInterval(timer); document.removeEventListener('visibilitychange', syncVisible) })
</script>

<template>
  <section class="workload-explorer" :class="{ 'is-deployment': deployment }" aria-label="工作负载运行状态">
    <aside class="explorer-navigation" aria-label="版本与 Pod 导航">
      <a-card v-if="deployment" class="explorer-card revision-column" size="small">
        <template #title><span class="section-title"><History :size="15" />历史版本<a-tag :bordered="false">{{ revisions.length }}</a-tag></span></template>
        <template #extra><a-tooltip title="刷新历史版本"><a-button size="small" type="text" :loading="historyLoading" :disabled="disabled || !readable" aria-label="刷新历史版本" @click="loadHistory"><RefreshCw :size="14" /></a-button></a-tooltip></template>
        <a-alert v-if="historyError" class="column-alert" type="error" show-icon :message="historyError"><template #action><a-button size="small" :disabled="disabled" @click="loadHistory">重试</a-button></template></a-alert>
        <a-alert v-else-if="historyState==='partial' || historyState==='pods_unavailable'" class="column-alert" type="warning" show-icon :message="historyState==='partial'?'历史版本数据尚未完整加载':'暂时无法读取历史版本关联的 Pod，请核对关联 Pod 列表'" />
        <a-spin :spinning="historyLoading">
          <a-list v-if="revisions.length" class="revision-list" :data-source="revisions" :split="false" size="small" aria-label="ReplicaSet 历史版本">
            <template #renderItem="{ item }"><a-list-item>
              <button class="revision-item navigation-item" :class="{ selected: item.uid === revisionUID }" :disabled="disabled" :aria-pressed="item.uid === revisionUID" @click="chooseRevision(item)">
                <span class="revision-heading"><strong>版本 {{ item.revision ?? '—' }}</strong><a-tag v-if="item.current" color="blue" :bordered="false">当前</a-tag><ChevronRight :size="14" class="selection-chevron" /></span>
                <span class="revision-name" :title="item.name">{{ item.name }}</span>
                <span class="replica-counts"><span>期望 <b>{{ count(item.desired) }}</b></span><span>当前 <b>{{ count(item.replicas) }}</b></span><span>就绪 <b>{{ count(item.ready) }}</b></span></span>
                <time class="secondary">{{ formatDateTime(item.created) }}</time>
              </button>
            </a-list-item></template>
          </a-list>
          <a-empty v-else-if="!historyLoading && !historyError" :description="!readable ? '没有查看工作负载的权限' : historyState==='partial' || historyState==='pods_unavailable' ? '历史数据尚未完整获取' : '暂无历史版本'" :image-style="{ height: '36px' }" />
        </a-spin>
        <p v-if="revisionChosen && revisionUID && !selectedRevision" class="column-note" role="status">所选历史版本已不存在，请重新选择。</p>
      </a-card>

      <a-card class="explorer-card pod-column" size="small">
        <template #title><span class="section-title"><Box :size="15" />Pod<a-tag :bordered="false">{{ podsError||podsLoading?'—':`${visiblePods.length}${podsPartial?'+':''}` }}</a-tag></span></template>
        <template #extra><a-tooltip title="刷新关联 Pod"><a-button size="small" type="text" :disabled="disabled || !readable" :loading="podsLoading" aria-label="刷新关联 Pod" @click="emit('retryPods')"><RefreshCw :size="14" /></a-button></a-tooltip></template>
        <a-alert v-if="podsError" class="column-alert" type="error" show-icon :message="podsError"><template #action><a-button size="small" :disabled="disabled" @click="emit('retryPods')">重试</a-button></template></a-alert>
        <a-alert v-else-if="podsPartial" class="column-alert" type="warning" show-icon message="关联 Pod 尚未完整加载" />
        <a-spin :spinning="podsLoading">
          <a-list v-if="visiblePods.length" class="pod-list" :data-source="visiblePods" :split="false" size="small" aria-label="关联 Pod">
            <template #renderItem="{ item: pod }"><a-list-item>
              <button class="pod-item navigation-item" :class="{ selected: pod.metadata.uid === podUID }" :disabled="disabled || !readable" :aria-pressed="pod.metadata.uid === podUID" @click="choosePod(pod)">
                <span class="pod-heading"><strong :title="pod.metadata.name">{{ pod.metadata.name }}</strong><ChevronRight :size="14" class="selection-chevron" /></span>
                <span class="pod-status"><a-badge :status="badgeStatus(podState(pod).tone)" :text="podState(pod).text" /><span v-if="podState(pod).reason" class="secondary">{{ podState(pod).reason }}</span></span>
                <span class="pod-placement"><span>IP</span><code>{{ pod.status?.podIP || '—' }}</code></span>
                <span class="pod-placement"><span>节点</span><span :title="pod.spec?.nodeName">{{ pod.spec?.nodeName || '未调度' }}</span></span>
              </button>
            </a-list-item></template>
          </a-list>
          <a-empty v-else-if="!podsLoading && !podsError" :description="deployment && !selectedRevision ? '请选择历史版本' : '暂无关联 Pod'" :image-style="{ height: '36px' }" />
        </a-spin>
      </a-card>
    </aside>

    <a-card class="explorer-card pod-detail-column" size="small">
      <template #title>
        <div class="pod-detail-identity"><span class="pod-kind-icon"><Box :size="19" /></span><div><span class="kind-label">Pod 详情</span><h3>{{ selectedPod?.metadata.name || '运行状态' }}</h3></div></div>
      </template>
      <template #extra><a-space class="pod-detail-actions" :size="12" wrap>
        <a-badge v-if="selectedPodState" :status="badgeStatus(selectedPodState.tone)" :text="selectedPodState.text" />
        <a-button v-if="selectedPod && canFollow(reference(selectedPod))" type="link" size="small" :disabled="disabled" @click="followPod">完整详情<ChevronRight :size="13" /></a-button>
      </a-space></template>
      <a-alert v-if="podMissing" class="column-alert" type="warning" show-icon message="所选 Pod 已删除或不在当前副本组中，请重新选择 Pod。" />
      <template v-if="selectedPod && readable">
        <a-tabs v-model:active-key="podTab" class="pod-detail-tabs" :animated="false">
          <a-tab-pane key="summary" tab="运行信息">
            <a-descriptions class="pod-facts" size="small" :column="{ xs: 1, sm: 2, xl: 4 }" layout="vertical" :colon="false">
              <a-descriptions-item label="命名空间"><span class="namespace-value">{{ selectedPod.metadata.namespace }}</span></a-descriptions-item>
              <a-descriptions-item label="Pod IP"><code>{{ selectedPod.status?.podIP || '—' }}</code></a-descriptions-item>
              <a-descriptions-item label="节点"><button v-if="selectedPod.spec?.nodeName && canFollow({resource:'nodes',name:selectedPod.spec.nodeName})" class="inline-link" :disabled="disabled" @click="followNode">{{ selectedPod.spec.nodeName }}</button><span v-else>{{ selectedPod.spec?.nodeName || '未调度' }}</span></a-descriptions-item>
              <a-descriptions-item label="创建时间">{{ formatDateTime(selectedPod.metadata.creationTimestamp) }}</a-descriptions-item>
            </a-descriptions>

            <section class="container-section" aria-label="容器运行信息">
              <div class="container-toolbar">
                <label :for="`explorer-container-${object.metadata.uid}`">容器</label>
                <a-select :id="`explorer-container-${object.metadata.uid}`" v-model:value="containerName" aria-label="Pod 容器" :disabled="disabled || !choices.length" :options="choices.map(container => ({value:container.name,label:container.kind==='app'?container.name:`${container.name} (${container.kind==='init'?'Init':'临时容器'})`}))" />
                <a-space :size="8" class="container-actions">
                  <a-button :disabled="actionDisabled" @click="openContainer()"><FileText :size="14" />日志</a-button>
                  <a-button v-if="can('terminal.open')" :disabled="actionDisabled || !terminalAllowed" @click="openContainer(true)"><TerminalSquare :size="14" />终端</a-button>
                </a-space>
              </div>
              <div class="container-image"><span class="secondary">镜像</span><code>{{ selectedContainerImage || '—' }}</code></div>
            </section>

            <KubeResourceDiagnostics v-if="podTab==='summary'" :key="`${cluster}/${selectedPod.metadata.uid}/summary`" :cluster="cluster" :object="selectedPod" :type="podType" :auto-refresh="autoRefresh && !disabled" :refresh-token="refreshToken" :can-follow="canFollow" embedded compact @follow="emit('follow',$event)" />

            <section class="health-section" aria-label="Pod 健康状态">
              <div class="health-heading"><h4>状态检查</h4><span class="secondary">{{ selectedPod.status?.conditions?.length ?? 0 }} 项</span></div>
              <div v-if="selectedPod.status?.conditions?.length" class="health-grid">
                <div v-for="condition in selectedPod.status.conditions" :key="condition.type" class="health-item">
                  <div class="condition-heading"><a-tooltip :title="condition.type"><a-badge :status="badgeStatus(workloadConditionTone(condition))" :text="conditionLabel(condition.type)" /></a-tooltip><span class="condition-value">{{ ['True','False'].includes(condition.status)?condition.status:'Unknown' }}</span></div>
                  <p v-if="condition.reason || condition.message" class="condition-message">{{ condition.message || condition.reason }}</p>
                </div>
              </div>
              <a-empty v-else description="暂无健康状态" :image-style="{ height: '32px' }" />
            </section>
          </a-tab-pane>
          <a-tab-pane key="diagnostics" tab="运行诊断"><KubeResourceDiagnostics v-if="podTab==='diagnostics'" :key="`${cluster}/${selectedPod.metadata.uid}`" :cluster="cluster" :object="selectedPod" :type="podType" :auto-refresh="autoRefresh && !disabled" :refresh-token="refreshToken" :can-follow="canFollow" embedded @follow="emit('follow',$event)" /></a-tab-pane>
        </a-tabs>
      </template>
      <a-empty v-else-if="!podMissing" class="detail-empty" :description="readable ? '选择 Pod 查看运行状态' : '没有查看工作负载的权限'" :image-style="{ height: '48px' }" />
    </a-card>
  </section>
</template>

<style scoped>
.workload-explorer{display:grid;grid-template-columns:280px minmax(0,1fr);gap:16px;align-items:start;color:var(--edo-text)}
.explorer-navigation{display:flex;flex-direction:column;gap:14px;min-width:0}
.explorer-card{min-width:0;border-color:var(--edo-border);border-radius:8px;background:var(--edo-surface);overflow:hidden;box-shadow:none}
.explorer-card :deep(.ant-card-head){padding:0 16px;min-height:48px;border-bottom-color:var(--edo-border)}
.explorer-card :deep(.ant-card-head-title){padding:12px 0;font-size:13px;font-weight:600}
.explorer-card :deep(.ant-card-extra){padding:8px 0}
.explorer-navigation :deep(.ant-card-body){padding:6px}
.section-title{display:flex;align-items:center;gap:8px}.section-title>svg{color:var(--edo-muted)}
.section-title :deep(.ant-tag){margin:0;padding:0 6px;line-height:20px;font-size:12px;color:var(--edo-muted);background:var(--edo-surface-soft)}
.explorer-card :deep(.ant-btn){display:inline-flex;align-items:center;justify-content:center;gap:6px}
.revision-list{max-height:300px;overflow-y:auto}.pod-list{max-height:520px;overflow-y:auto}
.explorer-navigation :deep(.ant-list-item){padding:2px 0;border:0}
.navigation-item{position:relative;width:100%;padding:12px;border:0;border-radius:5px;background:transparent;color:var(--edo-text);text-align:left;display:flex;flex-direction:column;gap:7px;font:inherit;cursor:pointer;overflow-wrap:anywhere;transition:background .15s}
.navigation-item:hover{background:var(--edo-surface-soft)}
.navigation-item.selected{background:var(--edo-primary-soft);box-shadow:inset 2px 0 var(--edo-primary)}
.navigation-item:focus-visible,.inline-link:focus-visible{outline:2px solid var(--edo-primary);outline-offset:-2px}
.navigation-item:disabled,.inline-link:disabled{cursor:not-allowed;opacity:.6}
.revision-heading,.pod-heading,.pod-status{display:flex;align-items:center;gap:8px;min-width:0}
.revision-heading strong,.pod-heading strong{font-size:13px;font-weight:600;line-height:1.5;min-width:0}
.pod-heading strong{flex:1;overflow-wrap:anywhere}.selection-chevron{flex:none;margin-left:auto;color:var(--edo-muted);opacity:0}.selected .selection-chevron{opacity:1;color:var(--edo-primary)}
.revision-heading :deep(.ant-tag){font-size:11px;line-height:20px;margin:0}
.revision-name{color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}
.secondary{font-size:12px;line-height:1.5;color:var(--edo-muted)}
.replica-counts{display:flex;gap:12px;font-size:12px;color:var(--edo-muted)}.replica-counts b{font-weight:500;color:var(--edo-text);font-variant-numeric:tabular-nums}
.pod-status{flex-wrap:wrap}.pod-status :deep(.ant-badge-status-text){font-size:12px}
.pod-placement{display:grid;grid-template-columns:30px minmax(0,1fr);gap:8px;font-size:12px;line-height:1.5;color:var(--edo-muted)}.pod-placement code{font-size:12px;color:var(--edo-text)}.pod-placement>span:last-child{overflow-wrap:anywhere}
.column-alert{margin:8px}.column-note{padding:4px 10px;font-size:12px;line-height:1.6;color:var(--edo-muted)}
.explorer-navigation :deep(.ant-empty){margin:24px 8px}.explorer-navigation :deep(.ant-empty-description){font-size:12px;color:var(--edo-muted)}
.pod-detail-column :deep(.ant-card-head){padding:0 20px}.pod-detail-column :deep(.ant-card-head-wrapper){align-items:center;gap:16px;flex-wrap:wrap}.pod-detail-column :deep(.ant-card-head-title){white-space:normal;min-width:0;padding:16px 0}.pod-detail-column :deep(.ant-card-body){padding:0 20px 20px}
.pod-detail-identity{display:flex;align-items:center;gap:12px;min-width:0}.pod-detail-identity>div{min-width:0}
.pod-kind-icon{display:grid;place-items:center;width:36px;height:36px;border-radius:7px;background:var(--edo-surface-soft);color:var(--edo-muted);flex:none}
.kind-label{font-size:11px;line-height:1.5;font-weight:400;color:var(--edo-muted)}.pod-detail-identity h3{margin:3px 0 0;font-size:16px;line-height:1.5;font-weight:600;overflow-wrap:anywhere}
.pod-detail-actions :deep(.ant-badge-status-text){font-size:12px}.pod-detail-actions :deep(.ant-btn-link){padding-inline:0;font-size:12px}
.pod-detail-tabs :deep(.ant-tabs-nav){margin:0 0 18px}.pod-detail-tabs :deep(.ant-tabs-tab){font-size:13px;padding:14px 0}.pod-detail-tabs :deep(.ant-tabs-nav::before){border-bottom-color:var(--edo-border)}
.pod-facts{margin-bottom:4px}.pod-facts :deep(.ant-descriptions-item){padding-bottom:16px}.pod-facts :deep(.ant-descriptions-item-label){font-size:12px;color:var(--edo-muted);padding-bottom:5px}.pod-facts :deep(.ant-descriptions-item-content){font-size:13px;color:var(--edo-text);overflow-wrap:anywhere}.pod-facts code{font-size:12px}.namespace-value{font-size:12px;background:var(--edo-surface-soft);padding:2px 7px;border-radius:4px}
.inline-link{padding:0;border:0;background:none;color:var(--edo-primary);text-align:left;font:inherit;cursor:pointer;overflow-wrap:anywhere}
.container-section{margin:4px 0 18px;padding:16px 0;border-top:1px solid var(--edo-border);border-bottom:1px solid var(--edo-border)}
.container-toolbar{display:flex;align-items:center;gap:12px;flex-wrap:wrap}.container-toolbar label{font-size:13px;font-weight:500}.container-toolbar :deep(.ant-select){width:260px;max-width:100%;min-width:140px}.container-actions{margin-left:auto}.container-actions :deep(.ant-btn){font-size:12px}
.container-image{display:flex;align-items:baseline;gap:12px;margin-top:12px;min-width:0}.container-image>.secondary{flex-shrink:0}.container-image code{font-size:12px;line-height:1.7;overflow-wrap:anywhere;color:var(--edo-muted)}
.health-section{margin-top:20px}.health-heading{display:flex;align-items:center;gap:8px;margin-bottom:12px}.health-heading h4{margin:0;font-size:13px;font-weight:600}.health-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:10px 24px}.health-item{min-width:0;padding:8px 0}.condition-heading{display:flex;align-items:center;justify-content:space-between;gap:10px}.condition-heading :deep(.ant-badge-status-text){font-size:12px;color:var(--edo-text)}.condition-value{font-size:12px;color:var(--edo-muted);font-variant-numeric:tabular-nums}.condition-message{font-size:12px;line-height:1.6;color:var(--edo-muted);margin:7px 0 0;overflow-wrap:anywhere}
.pod-detail-tabs :deep(.diagnostics){padding:0;border:0;background:transparent}.pod-detail-tabs :deep(.diagnostic-charts){margin:0;gap:14px}.pod-detail-tabs :deep(.resource-table){min-width:670px}.detail-empty{padding:56px 12px}
@media(min-width:1500px){.workload-explorer{grid-template-columns:300px minmax(0,1fr)}}
@media(max-width:1050px){.workload-explorer{grid-template-columns:250px minmax(0,1fr);gap:12px}.pod-detail-column :deep(.ant-card-head){padding:0 16px}.pod-detail-column :deep(.ant-card-body){padding:0 16px 16px}.pod-detail-identity h3{font-size:14px}.pod-kind-icon{display:none}.container-actions{margin-left:0}.pod-detail-actions{gap:6px!important}}
@media(max-width:760px){.workload-explorer{grid-template-columns:minmax(0,1fr)}.is-deployment .explorer-navigation{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1.3fr);gap:12px}.revision-list,.pod-list{max-height:260px}.pod-detail-column :deep(.ant-card-head-wrapper){gap:0}.pod-detail-column :deep(.ant-card-extra){padding-top:0}.pod-facts :deep(.ant-descriptions-item){padding-bottom:12px}.health-grid{grid-template-columns:repeat(auto-fit,minmax(180px,1fr))}}
@media(max-width:500px){.is-deployment .explorer-navigation{grid-template-columns:minmax(0,1fr)}.container-toolbar{gap:10px}.container-toolbar :deep(.ant-select){flex:1;width:auto}.container-actions{flex-basis:100%;margin-left:0}.pod-detail-identity h3{font-size:14px}.pod-detail-column :deep(.ant-card-head-title){padding-bottom:10px}.container-image{align-items:flex-start}.health-grid{grid-template-columns:minmax(0,1fr)}}
</style>
