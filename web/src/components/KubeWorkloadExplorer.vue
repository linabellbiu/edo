<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Box, ChevronRight, Network, RefreshCw, Server } from 'lucide-vue-next'
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
  visible?: boolean
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
const root = ref<HTMLElement>()
let restoreRevision = false, restorePod = false
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
function revealInColumn(column: HTMLElement, item: HTMLElement) {
  const bounds = column.getBoundingClientRect(), target = item.getBoundingClientRect()
  const top = bounds.top + (column.querySelector('header')?.getBoundingClientRect().height ?? 0) + 6
  if (target.top < top) column.scrollTop += target.top - top
  else if (target.bottom > bounds.bottom - 6) column.scrollTop += target.bottom - bounds.bottom + 6
}
async function restoreVisibleSelection() {
  const identity = context.value
  await nextTick()
  if (disposed || identity !== context.value || props.disabled || props.visible === false || !readable.value || !root.value || root.value.clientHeight === 0) return
  for (const [pending, selector] of [[restoreRevision, '.revision-column'], [restorePod, '.pod-column']] as const) {
    if (!pending) continue
    const column = root.value.querySelector<HTMLElement>(selector), selected = column?.querySelector<HTMLElement>('button.selected')
    if (!column || !selected) continue
    revealInColumn(column, selected)
    if (selector === '.revision-column') restoreRevision = false
    else restorePod = false
  }
}
function revealFocused(event: FocusEvent) {
  const target = event.target as HTMLElement | null, column = target?.closest<HTMLElement>('.explorer-column')
  if (column && target) revealInColumn(column, target)
}
function podState(pod: KubeObject) { return resourceStatus(pod, t) }
function choosePod(pod: KubeObject) {
  if (props.disabled || !readable.value || !selectedWorkloadPod(visiblePods.value, pod.metadata?.uid, namespace.value)) return
  restorePod = false
  podUID.value = pod.metadata.uid; podChosen = true; containerName.value = ''; podTab.value = 'summary'
}
function chooseRevision(item: WorkloadRevision) {
  if (props.disabled || !revisions.value.some(revision => revision.uid === item.uid)) return
  restoreRevision = false; restorePod = false
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
  restoreRevision = revisionChosen; restorePod = podChosen
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
watch([revisionUID, podUID, historyLoading, () => props.podsLoading, readable, () => props.disabled, () => props.visible], () => { void restoreVisibleSelection() }, { flush: 'post' })
watch(() => props.refreshToken, () => { if (deployment.value) void loadHistory() })
watch(readable, value => {
  if (value) { if (deployment.value) void loadHistory() }
  else { revision++; request?.abort(); historyLoading.value = false; historyState.value = ''; revisions.value = []; revisionUID.value = ''; podUID.value = ''; containerName.value = ''; revisionChosen = false; podChosen = false }
})
function syncVisible() { if (!document.hidden && props.autoRefresh && !props.disabled && !historyLoading.value && deployment.value) void loadHistory() }
onMounted(() => { void restoreVisibleSelection(); timer = setInterval(syncVisible, 30_000); document.addEventListener('visibilitychange', syncVisible) })
onBeforeUnmount(() => { disposed = true; revision++; request?.abort(); if (timer) clearInterval(timer); document.removeEventListener('visibilitychange', syncVisible) })
</script>

<template>
  <section ref="root" class="workload-explorer" :class="{ 'is-deployment': deployment }" aria-label="工作负载运行状态" @focusin="revealFocused">
    <aside v-if="deployment" class="explorer-column revision-column">
      <header><h4>历史版本</h4><span class="history-limit">最多 {{ object.spec?.revisionHistoryLimit ?? '—' }} 个</span><a-button size="small" type="text" :loading="historyLoading" :disabled="disabled || !readable" aria-label="刷新历史版本" @click="loadHistory"><RefreshCw :size="13" /></a-button></header>
      <a-alert v-if="historyError" class="column-alert" type="error" show-icon :message="historyError"><template #action><a-button size="small" :disabled="disabled" @click="loadHistory">重试</a-button></template></a-alert>
      <a-alert v-else-if="historyState==='partial' || historyState==='pods_unavailable'" class="column-alert" type="warning" show-icon :message="historyState==='partial'?'历史版本数据尚未完整加载':'暂时无法读取历史版本关联的 Pod，请核对关联 Pod 列表'" />
      <a-spin :spinning="historyLoading">
        <div v-if="revisions.length" class="revision-list" role="list" aria-label="ReplicaSet 历史版本">
          <div v-for="item in revisions" :key="item.uid" class="revision-entry" role="listitem" :class="{ selected: item.uid === revisionUID }">
            <span class="revision-created" :title="formatDateTime(item.created)">已创建：{{ formatRelativeTime(item.created) }}</span>
            <button class="revision-item" :class="{ selected: item.uid === revisionUID }" :disabled="disabled" :aria-pressed="item.uid === revisionUID" :title="(item.images ?? []).join('\n')" @click="chooseRevision(item)">
              <span class="revision-heading"><strong>{{ item.name }}</strong><span>#{{ item.revision ?? '—' }}</span><span v-if="item.current" class="revision-current">当前</span></span>
              <span class="replica-counts"><span>期望<b>{{ count(item.desired) }}</b></span><span>当前<b>{{ count(item.replicas) }}</b></span><span>就绪<b>{{ count(item.ready) }}</b></span></span>
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
          <div v-for="pod in visiblePods" :key="pod.metadata.uid" role="listitem"><button class="pod-item" :class="{ selected: pod.metadata.uid === podUID }" :disabled="disabled || !readable" :aria-pressed="pod.metadata.uid === podUID" @click="choosePod(pod)">
            <span class="pod-heading"><Box :size="14" /><strong :title="pod.metadata.name">{{ pod.metadata.name }}</strong><a-tag :color="podState(pod).tone">{{ podState(pod).text }}</a-tag><ChevronRight :size="12" /></span>
            <span class="pod-placement"><span title="Pod IP"><Network :size="12" aria-hidden="true" /><code>{{ pod.status?.podIP || '—' }}</code></span><span :title="pod.spec?.nodeName"><Server :size="12" aria-hidden="true" /><span>{{ pod.spec?.nodeName || '未调度' }}</span></span><time class="pod-created" :title="formatDateTime(pod.metadata.creationTimestamp)">{{ formatRelativeTime(pod.metadata.creationTimestamp) }}</time></span>
            <span v-if="podState(pod).reason" class="pod-reason">{{ podState(pod).reason }}</span>
          </button></div>
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
.workload-explorer {
  display: grid;
  grid-template-columns: minmax(230px, 1fr) minmax(0, 2fr);
  gap: 12px;
  height: clamp(470px, calc(100dvh - 350px), 780px);
  padding: 10px;
  overflow: hidden;
  border: 1px solid var(--workload-border);
  border-radius: 4px;
  background: var(--workload-column);
  color: var(--workload-text);
}
.workload-explorer.is-deployment { grid-template-columns: minmax(210px, .94fr) minmax(240px, 1.04fr) minmax(0, 1.8fr); }
.explorer-column { min-width: 0; min-height: 0; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; scroll-padding-top: 48px; background: var(--workload-column); }
.explorer-column>header { display: flex; position: sticky; top: 0; z-index: 1; align-items: center; justify-content: space-between; gap: 6px; min-height: 36px; padding: 5px 10px; border-bottom: 1px solid var(--workload-border); background: var(--workload-heading); color: var(--workload-title); }
.explorer-column h4 { margin: 0; font-size: 13px; font-weight: 650; line-height: 22px; }
.heading-count { margin-left: 4px; color: var(--workload-muted); font-size: 12px; font-weight: 500; font-variant-numeric: tabular-nums; }
.history-limit { margin-left: auto; color: var(--workload-muted); font-size: 12px; white-space: nowrap; }
.explorer-column>header :deep(.ant-btn) { display: grid; width: 26px; height: 26px; place-items: center; padding: 0; color: var(--workload-muted); }
.explorer-column>header :deep(.ant-btn:not(:disabled):hover) { color: var(--workload-link); background: var(--workload-column); }
.revision-list { position: relative; padding: 8px 8px 12px 22px; }
.revision-list::before { position: absolute; top: 23px; bottom: 30px; left: 9px; width: 1px; background: var(--workload-border); content: ''; }
.revision-entry { position: relative; margin: 0 0 12px; }
.revision-entry::before { position: absolute; top: 9px; left: -16px; width: 7px; height: 7px; border: 1px solid var(--workload-column); border-radius: 50%; background: var(--workload-muted); content: ''; }
.revision-entry.selected::before { background: var(--workload-accent); }
.revision-created { display: block; padding: 3px 0 6px; color: var(--workload-muted); font-size: 12px; line-height: 18px; }
.revision-item,.pod-item { position: relative; display: flex; flex-direction: column; gap: 6px; width: 100%; min-width: 0; padding: 10px 12px; border: 1px solid var(--workload-idle-border); border-radius: 3px; background: var(--workload-idle); color: var(--workload-text); font: inherit; text-align: left; cursor: pointer; }
.revision-item.selected,.pod-item.selected { border-color: var(--workload-selected-border); background: var(--workload-selected); color: var(--workload-text); box-shadow: inset 2px 0 var(--workload-accent); }
.revision-item.selected .revision-heading strong,.pod-item.selected .pod-heading strong { color: var(--workload-on-selected); }
.revision-item:hover:not(.selected):not(:disabled),.pod-item:hover:not(.selected):not(:disabled) { border-color: var(--workload-hover-border); background: var(--workload-hover); color: var(--workload-text); }
button:focus-visible { outline: 2px solid var(--workload-focus); outline-offset: 2px; }
button:disabled { opacity: .6; cursor: not-allowed; }
.revision-heading,.pod-heading { display: flex; align-items: center; gap: 6px; min-width: 0; line-height: 20px; }
.revision-heading strong,.pod-heading strong { flex: 1; min-width: 0; color: var(--workload-title); font-size: 13px; font-weight: 600; white-space: normal; overflow-wrap: anywhere; font-family: var(--edo-font-mono, ui-monospace, monospace); }
.revision-heading>span { flex: none; color: var(--workload-muted); font-size: 12px; font-variant-numeric: tabular-nums; }
.replica-counts { display: grid; grid-template-columns: repeat(3,minmax(0,1fr)); gap: 8px; color: var(--workload-muted); font-size: 12px; line-height: 20px; }
.replica-counts>span { display: flex; align-items: baseline; gap: 6px; min-width: 0; white-space: nowrap; }
.replica-counts b { color: var(--workload-text); font-size: 12px; font-weight: 600; font-variant-numeric: tabular-nums; }
.revision-heading .revision-current { padding: 0 5px; border-radius: 2px; background: var(--workload-column); color: var(--workload-link); font-size: 12px; line-height: 18px; }
.selected-revision { margin: 0 0 8px; padding: 7px 10px; border-bottom: 1px solid var(--workload-border); color: var(--workload-muted); font-size: 12px; line-height: 18px; overflow-wrap: anywhere; }
.selected-revision>strong { color: var(--workload-link); font-weight: 500; }
.pod-list { display: grid; gap: 8px; padding: 0 8px 12px; }
.pod-heading>svg { flex: none; color: var(--workload-muted); }
.pod-heading :deep(.ant-tag) { margin: 0; padding: 0 5px; font-size: 12px; line-height: 18px; }
.pod-placement { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 12px; min-width: 0; color: var(--workload-muted); font-size: 12px; line-height: 18px; }
.pod-placement>span { display: inline-flex; align-items: center; gap: 4px; min-width: 0; }
.pod-placement svg { flex: none; color: var(--workload-muted); }
.pod-placement code { color: var(--workload-text); font: inherit; font-variant-numeric: tabular-nums; }
.pod-placement>span>span { max-width: 100%; overflow-wrap: anywhere; }
.pod-created { margin-left: auto; color: var(--workload-muted); font-size: 12px; white-space: nowrap; }
.pod-reason { color: var(--workload-muted); font-size: 12px; overflow-wrap: anywhere; }
.pod-column,.pod-detail-column { padding-left: 12px; border-left: 1px solid var(--workload-divider); }
.pod-detail-column { padding-right: 2px; background: var(--workload-column); }
.column-alert { margin: 8px; }
.column-note { padding: 0 9px; color: var(--workload-muted); font-size: 12px; line-height: 1.6; }
.workload-explorer :deep(.ant-empty) { margin: 24px 9px; }
.pod-detail-column :deep(.diagnostics) { padding: 0; border: 0; background: transparent; }
.pod-detail-column :deep(.diagnostic-charts) { gap: 8px; margin: 0 0 10px; }
.pod-detail-column :deep(.diagnostic-charts article) { padding: 8px; border-radius: 3px; }
.pod-detail-column :deep(.diagnostic-charts h4) { font-size: 12px; }
@media(max-width:1100px) {
  .workload-explorer.is-deployment { grid-template-columns: 1fr 1fr; height: auto; }
  .is-deployment .revision-column,.is-deployment .pod-column { max-height: 320px; }
  .is-deployment .pod-detail-column { grid-column: 1/-1; max-height: none; overflow: visible; }
  .pod-detail-column { scrollbar-gutter: auto; }
}
@media(max-width:680px) {
  .workload-explorer,.workload-explorer.is-deployment { grid-template-columns: minmax(0,1fr); height: auto; padding: 8px; gap: 12px; }
  .explorer-column,.is-deployment .revision-column,.is-deployment .pod-column { max-height: none; overflow: visible; }
  .pod-column,.pod-detail-column { border-top: 1px solid var(--workload-divider); border-left: 0; padding-top: 8px; padding-left: 0; }
  .pod-detail-column { grid-column: auto; max-height: none; overflow: visible; }
}
</style>
