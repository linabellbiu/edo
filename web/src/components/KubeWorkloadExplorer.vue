<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Box, ChevronRight, RefreshCw } from 'lucide-vue-next'
import client from '@/api/client'
import { kubeErrorMessage, resourceURL, type KubeObject, type KubeType } from '@/api/kubernetes'
import { resourceStatus, type KubeReference } from '@/utils/kubernetesPresentation'
import { podContainerChoices } from '@/utils/kubernetesWorkspace'
import { initialWorkloadRevisionUID, revisionOwnedPods, selectedWorkloadPod, selectedWorkloadRevision, sortWorkloadRevisions, type WorkloadContainerTarget, type WorkloadExplorerSelection, type WorkloadRevision } from '@/utils/kubernetesWorkloadExplorer'
import { formatDateTime, formatRelativeTime } from '@/utils/time'

const KubeWorkloadPodRuntime = defineAsyncComponent(() => import('./KubeWorkloadPodRuntime.vue'))
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
const actionDisabled = computed(() => Boolean(props.disabled) || props.podsLoading || Boolean(props.podsError) || deployment.value && Boolean(historyError.value) || !readable.value || !selectedPod.value || !selectedContainer.value)

function count(value: unknown) { return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? String(value) : '—' }
function podState(pod: KubeObject) { return resourceStatus(pod, t) }
function choosePod(pod: KubeObject) {
  if (props.disabled || !readable.value || !selectedWorkloadPod(visiblePods.value, pod.metadata?.uid, namespace.value)) return
  podUID.value = pod.metadata.uid; podChosen = true; containerName.value = ''; podTab.value = 'summary'
}
function chooseRevision(item: WorkloadRevision) {
  if (props.disabled || !revisions.value.some(revision => revision.uid === item.uid)) return
  revisionUID.value = item.uid; revisionChosen = true
  podUID.value = ''; podChosen = false; containerName.value = ''; podTab.value = 'summary'
}
function chooseContainer(name: string) {
  if (!props.disabled && readable.value && choices.value.some(container => container.name === name)) containerName.value = name
}
function openNamedContainer(name: string, terminal = false) {
  chooseContainer(name)
  if (containerName.value === name) openContainer(terminal)
}
function matchesContainerTarget(target: WorkloadContainerTarget) {
  return target.cluster === props.cluster && target.namespace === namespace.value && target.podUID === selectedPod.value?.metadata.uid
}
function chooseRuntimeContainer(target: WorkloadContainerTarget) {
  if (matchesContainerTarget(target)) chooseContainer(target.container)
}
function openRuntimeContainer(target: WorkloadContainerTarget, terminal = false) {
  if (matchesContainerTarget(target)) openNamedContainer(target.container, terminal)
}
function followRuntimeReference(reference: KubeReference, target: WorkloadContainerTarget) {
  if (matchesContainerTarget(target) && !props.disabled && readable.value && props.canFollow(reference)) emit('follow', reference)
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
    <aside v-if="deployment" class="explorer-column revision-column">
      <header><h4>历史版本</h4><span class="history-limit">最多 {{ object.spec?.revisionHistoryLimit ?? '—' }} 个</span><a-button size="small" type="text" :loading="historyLoading" :disabled="disabled || !readable" aria-label="刷新历史版本" @click="loadHistory"><RefreshCw :size="13" /></a-button></header>
      <a-alert v-if="historyError" class="column-alert" type="error" show-icon :message="historyError"><template #action><a-button size="small" :disabled="disabled" @click="loadHistory">重试</a-button></template></a-alert>
      <a-alert v-else-if="historyState==='partial' || historyState==='pods_unavailable'" class="column-alert" type="warning" show-icon :message="historyState==='partial'?'历史版本数据尚未完整加载':'暂时无法读取历史版本关联的 Pod，请核对关联 Pod 列表'" />
      <a-spin :spinning="historyLoading">
        <div v-if="revisions.length" class="revision-list" role="list" aria-label="ReplicaSet 历史版本">
          <div v-for="item in revisions" :key="item.uid" class="revision-entry" :class="{ selected: item.uid === revisionUID }">
            <span class="revision-created" :title="formatDateTime(item.created)">已创建：{{ formatRelativeTime(item.created) }}</span>
            <button class="revision-item" :class="{ selected: item.uid === revisionUID }" :disabled="disabled" :aria-pressed="item.uid === revisionUID" :title="(item.images ?? []).join('\n')" @click="chooseRevision(item)">
              <span class="revision-heading"><strong>{{ item.name }}</strong><span>#{{ item.revision ?? '—' }}</span></span>
              <span class="replica-counts"><span>期望：<b>{{ count(item.desired) }}</b></span><span>当前：<b>{{ count(item.replicas) }}</b></span><span>就绪：<b>{{ count(item.ready) }}</b></span><span v-if="item.current" class="revision-current">当前</span></span>
            </button>
          </div>
        </div>
        <a-empty v-else-if="!historyLoading && !historyError" :description="!readable ? '没有查看工作负载的权限' : historyState==='partial' || historyState==='pods_unavailable' ? '历史数据尚未完整获取' : '暂无历史版本'" :image-style="{ height: '40px' }" />
      </a-spin>
      <p v-if="revisionChosen && revisionUID && !selectedRevision" class="column-note" role="status">所选历史版本已不存在，请重新选择。</p>
    </aside>

    <section class="explorer-column pod-column">
      <header><h4>对应容器组 <span class="heading-count">{{ podsError||podsLoading?'—':`${visiblePods.length}${podsPartial?'+':''}` }}</span></h4><a-button size="small" type="text" :disabled="disabled || !readable" :loading="podsLoading" aria-label="刷新关联 Pod" @click="emit('retryPods')"><RefreshCw :size="13" /></a-button></header>
      <p v-if="selectedRevision" class="selected-revision">副本集 <strong :title="selectedRevision.name">{{ selectedRevision.name }}</strong></p>
      <a-alert v-if="podsError" class="column-alert" type="error" show-icon :message="podsError"><template #action><a-button size="small" :disabled="disabled" @click="emit('retryPods')">重试</a-button></template></a-alert>
      <a-alert v-else-if="podsPartial" class="column-alert" type="warning" show-icon message="关联 Pod 尚未完整加载" />
      <a-spin :spinning="podsLoading">
        <div v-if="visiblePods.length" class="pod-list" role="list" aria-label="关联 Pod">
          <button v-for="pod in visiblePods" :key="pod.metadata.uid" class="pod-item" :class="{ selected: pod.metadata.uid === podUID }" :disabled="disabled || !readable" :aria-pressed="pod.metadata.uid === podUID" @click="choosePod(pod)">
            <span class="pod-heading"><Box :size="14" /><strong :title="pod.metadata.name">{{ pod.metadata.name }}</strong><a-tag :color="podState(pod).tone">{{ podState(pod).text }}</a-tag><ChevronRight :size="12" /></span>
            <span class="pod-created" :title="formatDateTime(pod.metadata.creationTimestamp)">{{ formatRelativeTime(pod.metadata.creationTimestamp) }}</span>
            <span class="pod-placement"><span><i>C</i><code>{{ pod.status?.podIP || '—' }}</code></span><span :title="pod.spec?.nodeName"><i>H</i><span>{{ pod.spec?.nodeName || '未调度' }}</span></span></span>
            <span v-if="podState(pod).reason" class="pod-reason">{{ podState(pod).reason }}</span>
          </button>
        </div>
        <a-empty v-else-if="!podsLoading && !podsError" :description="deployment && !selectedRevision ? '请选择历史版本' : '暂无关联 Pod'" :image-style="{ height: '40px' }" />
      </a-spin>
    </section>

    <section class="explorer-column pod-detail-column" aria-label="所选 Pod 事件与运行状态">
      <a-alert v-if="podMissing" class="column-alert" type="warning" show-icon message="所选 Pod 已删除或不在当前副本组中，请重新选择 Pod。" />
      <KubeWorkloadPodRuntime v-if="selectedPod && readable" :key="`${cluster}/${selectedPod.metadata.uid}`" :cluster="cluster" :pod="selectedPod" :container="containerName" :auto-refresh="autoRefresh" :refresh-token="refreshToken" :can="can" :can-follow="canFollow" :disabled="Boolean(disabled)||Boolean(podsError)||deployment&&Boolean(historyError)" @container="chooseRuntimeContainer" @logs="target=>openRuntimeContainer(target)" @terminal="target=>openRuntimeContainer(target,true)" @follow="followRuntimeReference" />
      <a-empty v-else-if="!podMissing" :description="readable ? '选择 Pod 查看运行状态' : '没有查看工作负载的权限'" :image-style="{ height: '40px' }" />
    </section>
  </section>
</template>

<style scoped>
.workload-explorer{--edo-kube-accent:#415c87;display:grid;grid-template-columns:minmax(230px,1fr) minmax(0,2fr);gap:10px;padding:12px;background:var(--edo-surface);border:1px solid var(--edo-border);border-radius:5px;height:clamp(470px,calc(100dvh - 350px),780px);overflow:hidden}
.workload-explorer.is-deployment{grid-template-columns:minmax(220px,1fr) minmax(230px,1fr) minmax(0,1.9fr)}
.explorer-column{min-width:0;min-height:0;overflow-y:auto;overscroll-behavior:contain;background:var(--edo-surface-soft);scrollbar-gutter:stable}
.explorer-column>header{min-height:32px;padding:3px 9px;display:flex;align-items:center;justify-content:space-between;gap:5px;position:sticky;top:0;background:var(--edo-surface-soft);z-index:1;border-bottom:1px solid var(--edo-border)}
.explorer-column h4{font-size:12px;margin:0;font-weight:600}.heading-count,.history-limit{font-size:11px;color:var(--edo-muted);font-weight:400}.history-limit{margin-left:auto;background:var(--edo-surface);padding:1px 5px;border:1px solid var(--edo-border);border-radius:2px}.revision-list{position:relative;padding:5px 9px 16px 28px}.revision-list::before{content:'';position:absolute;left:14px;top:21px;bottom:28px;width:1px;background:var(--edo-border)}.revision-entry{position:relative;margin:0 0 20px}.revision-entry::before{content:'';position:absolute;left:-19px;top:6px;width:10px;height:10px;border-radius:50%;background:var(--edo-muted);border:1px solid var(--edo-surface-soft)}.revision-entry.selected::before{background:#55b69b}.revision-created{display:block;padding:4px 0 8px;font-size:11px;color:var(--edo-muted)}
.revision-item,.pod-item{position:relative;width:100%;border:1px solid transparent;border-radius:4px;background:color-mix(in srgb,#39b89b 10%,var(--edo-surface));text-align:left;padding:10px;display:flex;flex-direction:column;gap:7px;font:inherit;color:var(--edo-text);cursor:pointer;min-width:0}.revision-item.selected,.pod-item.selected{background:var(--edo-kube-accent);color:#fff}.revision-item.selected::after,.pod-item.selected::after{content:'';position:absolute;right:-7px;top:calc(50% - 7px);border-top:7px solid transparent;border-bottom:7px solid transparent;border-left:7px solid var(--edo-kube-accent)}
.revision-item:hover:not(.selected),.pod-item:hover:not(.selected){border-color:var(--edo-primary)}button:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}button:disabled{cursor:not-allowed;opacity:.65}.revision-heading,.pod-heading{display:flex;align-items:center;gap:7px;min-width:0}.revision-heading strong{font-size:12px;flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-family:var(--edo-font-mono,monospace)}.revision-heading>span{font-size:12px;flex:none}.replica-counts{display:flex;align-items:center;gap:8px;font-size:11px;flex-wrap:wrap}.replica-counts b{font-weight:600}.revision-current{font-size:10px;margin-left:auto;border:1px solid currentColor;border-radius:2px;padding:0 3px;opacity:.8}.selected-revision{font-size:11px;margin:6px 10px 8px;color:var(--edo-muted);overflow-wrap:anywhere}.selected-revision>strong{color:var(--edo-kube-accent);font-weight:600}.pod-list{padding:4px 12px 16px 9px;display:grid;gap:10px}.pod-heading strong{font-size:12px;flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-family:var(--edo-font-mono,monospace)}.pod-heading>svg{flex:none}.pod-heading :deep(.ant-tag){font-size:10px;line-height:16px;padding:0 4px;margin:0}.pod-created{font-size:11px}.pod-placement{display:flex;flex-wrap:wrap;gap:5px 12px;font-size:11px;min-width:0}.pod-placement>span{display:flex;align-items:center;gap:4px;min-width:0}.pod-placement>span>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:180px}.pod-placement i{font-style:normal;background:var(--edo-surface);color:var(--edo-kube-accent);padding:0 2px;font-size:9px;border-radius:2px}.pod-placement code{font:inherit}.pod-reason{font-size:11px;overflow-wrap:anywhere}.pod-detail-column{background:var(--edo-surface);padding-inline:0 2px}.pod-detail-column>header{background:var(--edo-surface);padding-left:0;margin-bottom:8px}.pod-detail-column>header :deep(.ant-btn){font-size:11px;height:25px;padding-inline:4px}.column-alert{margin:8px}.column-note{font-size:12px;color:var(--edo-muted);padding:0 9px;line-height:1.6}.workload-explorer :deep(.ant-empty){margin:24px 9px}.pod-detail-column :deep(.diagnostics){padding:0;border:0;background:transparent}.pod-detail-column :deep(.diagnostic-charts){gap:8px;margin:0 0 9px}.pod-detail-column :deep(.diagnostic-charts article){padding:8px;border-radius:3px}.pod-detail-column :deep(.diagnostic-charts h4){font-size:11px}
.selected-revision>strong{color:var(--edo-primary)}
:global(:root[data-theme='dark']) .workload-explorer{--edo-kube-accent:#365477}
@media(max-width:1100px){.workload-explorer.is-deployment{grid-template-columns:1fr 1fr;height:auto}.is-deployment .revision-column,.is-deployment .pod-column{max-height:300px}.is-deployment .pod-detail-column{grid-column:1/-1;max-height:none;overflow:visible}.pod-detail-column{scrollbar-gutter:auto}}
@media(max-width:680px){.workload-explorer,.workload-explorer.is-deployment{grid-template-columns:minmax(0,1fr);height:auto;padding:8px;gap:8px}.explorer-column{max-height:280px}.pod-detail-column{grid-column:auto;max-height:none;overflow:visible}.replica-counts{gap:12px}.history-limit{margin-left:auto}}
</style>
