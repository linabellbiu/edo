<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Box, ChevronRight, FileText, RefreshCw, TerminalSquare } from 'lucide-vue-next'
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
const selectedPodState = computed(() => selectedPod.value ? resourceStatus(selectedPod.value, t) : undefined)
const podType = kubeTypes.find(type => type.resource === 'pods')!
const actionDisabled = computed(() => Boolean(props.disabled) || props.podsLoading || Boolean(props.podsError) || deployment.value && Boolean(historyError.value) || !readable.value || !selectedPod.value || !selectedContainer.value)
const terminalAllowed = computed(() => props.can('terminal.open') && selectedContainer.value?.running === true && !selectedPod.value?.metadata?.deletionTimestamp)

function count(value: unknown) { return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? String(value) : '—' }
function podState(pod: KubeObject) { return resourceStatus(pod, t) }
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
    <aside v-if="deployment" class="explorer-column revision-column">
      <header><h4>历史版本</h4><a-button size="small" type="text" :loading="historyLoading" :disabled="disabled || !readable" aria-label="刷新历史版本" @click="loadHistory"><RefreshCw :size="14" /></a-button></header>
      <a-alert v-if="historyError" class="column-alert" type="error" show-icon :message="historyError"><template #action><a-button size="small" :disabled="disabled" @click="loadHistory">重试</a-button></template></a-alert>
      <a-alert v-else-if="historyState==='partial' || historyState==='pods_unavailable'" class="column-alert" type="warning" show-icon :message="historyState==='partial'?'历史版本数据尚未完整加载':'暂时无法读取历史版本关联的 Pod，请核对关联 Pod 列表'" />
      <a-spin :spinning="historyLoading">
        <div v-if="revisions.length" class="revision-list" role="list" aria-label="ReplicaSet 历史版本">
          <button v-for="item in revisions" :key="item.uid" class="revision-item" :class="{ selected: item.uid === revisionUID }" :disabled="disabled" :aria-pressed="item.uid === revisionUID" @click="chooseRevision(item)">
            <span class="revision-heading"><strong>版本 {{ item.revision ?? '—' }}</strong><a-tag v-if="item.current" color="blue">当前</a-tag></span>
            <span class="secondary">{{ formatDateTime(item.created) }}</span><span class="revision-name">{{ item.name }}</span>
            <span class="replica-counts"><span>期望 <b>{{ count(item.desired) }}</b></span><span>当前 <b>{{ count(item.replicas) }}</b></span><span>就绪 <b>{{ count(item.ready) }}</b></span></span>
          </button>
        </div>
        <a-empty v-else-if="!historyLoading && !historyError" :description="!readable ? '没有查看工作负载的权限' : historyState==='partial' || historyState==='pods_unavailable' ? '历史数据尚未完整获取' : '暂无历史版本'" :image-style="{ height: '40px' }" />
      </a-spin>
      <p v-if="revisionChosen && revisionUID && !selectedRevision" class="column-note" role="status">所选历史版本已不存在，请重新选择。</p>
    </aside>

    <section class="explorer-column pod-column">
      <header><h4>Pod <span class="heading-count">{{ podsError||podsLoading?'—':`${visiblePods.length}${podsPartial?'+':''}` }}</span></h4><a-button size="small" type="text" :disabled="disabled || !readable" :loading="podsLoading" aria-label="刷新关联 Pod" @click="emit('retryPods')"><RefreshCw :size="14" /></a-button></header>
      <a-alert v-if="podsError" class="column-alert" type="error" show-icon :message="podsError"><template #action><a-button size="small" :disabled="disabled" @click="emit('retryPods')">重试</a-button></template></a-alert>
      <a-alert v-else-if="podsPartial" class="column-alert" type="warning" show-icon message="关联 Pod 尚未完整加载" />
      <a-spin :spinning="podsLoading">
        <div v-if="visiblePods.length" class="pod-list" role="list" aria-label="关联 Pod">
          <button v-for="pod in visiblePods" :key="pod.metadata.uid" class="pod-item" :class="{ selected: pod.metadata.uid === podUID }" :disabled="disabled || !readable" :aria-pressed="pod.metadata.uid === podUID" @click="choosePod(pod)">
            <span class="pod-heading"><Box :size="15" /><strong>{{ pod.metadata.name }}</strong><ChevronRight :size="14" /></span>
            <span class="pod-status"><a-tag :color="podState(pod).tone">{{ podState(pod).text }}</a-tag><span v-if="podState(pod).reason" class="secondary">{{ podState(pod).reason }}</span></span>
            <span class="pod-placement"><span>IP</span><code>{{ pod.status?.podIP || '—' }}</code></span><span class="pod-placement"><span>节点</span><span>{{ pod.spec?.nodeName || '未调度' }}</span></span>
          </button>
        </div>
        <a-empty v-else-if="!podsLoading && !podsError" :description="deployment && !selectedRevision ? '请选择历史版本' : '暂无关联 Pod'" :image-style="{ height: '40px' }" />
      </a-spin>
    </section>

    <section class="explorer-column pod-detail-column">
      <header><h4>Pod 详情</h4><a-button v-if="selectedPod && canFollow(reference(selectedPod))" type="link" size="small" :disabled="disabled" @click="followPod">完整详情 <ChevronRight :size="13" /></a-button></header>
      <a-alert v-if="podMissing" class="column-alert" type="warning" show-icon message="所选 Pod 已删除或不在当前副本组中，请重新选择 Pod。" />
      <template v-if="selectedPod && readable">
        <div class="pod-detail-heading"><strong>{{ selectedPod.metadata.name }}</strong><a-tag :color="selectedPodState?.tone">{{ selectedPodState?.text }}</a-tag></div>
        <a-tabs v-model:active-key="podTab" size="small" class="pod-detail-tabs" :animated="false">
          <a-tab-pane key="summary" tab="运行信息">
            <dl class="pod-facts"><div><dt>命名空间</dt><dd>{{ selectedPod.metadata.namespace }}</dd></div><div><dt>Pod IP</dt><dd>{{ selectedPod.status?.podIP || '—' }}</dd></div><div><dt>节点</dt><dd><button v-if="selectedPod.spec?.nodeName && canFollow({resource:'nodes',name:selectedPod.spec.nodeName})" class="inline-link" :disabled="disabled" @click="followNode">{{ selectedPod.spec.nodeName }}</button><span v-else>{{ selectedPod.spec?.nodeName || '未调度' }}</span></dd></div><div><dt>创建时间</dt><dd>{{ formatDateTime(selectedPod.metadata.creationTimestamp) }}</dd></div></dl>
            <div class="container-toolbar"><label :for="`explorer-container-${object.metadata.uid}`">容器</label><a-select :id="`explorer-container-${object.metadata.uid}`" v-model:value="containerName" aria-label="Pod 容器" :disabled="disabled || !choices.length" :options="choices.map(container => ({value:container.name,label:container.kind==='app'?container.name:`${container.name} (${container.kind==='init'?'Init':'临时容器'})`}))" /><a-button :disabled="actionDisabled" @click="openContainer()"><FileText :size="13" />日志</a-button><a-button v-if="can('terminal.open')" :disabled="actionDisabled || !terminalAllowed" @click="openContainer(true)"><TerminalSquare :size="13" />终端</a-button></div>
            <div class="container-images"><div v-for="container in selectedPod.spec?.containers ?? []" :key="container.name"><span class="secondary">{{ container.name }}</span><code>{{ container.image || '—' }}</code></div></div>
            <KubeResourceDiagnostics v-if="podTab==='summary'" :key="`${cluster}/${selectedPod.metadata.uid}/summary`" :cluster="cluster" :object="selectedPod" :type="podType" :auto-refresh="autoRefresh && !disabled" :refresh-token="refreshToken" :can-follow="canFollow" embedded compact @follow="emit('follow',$event)" />
            <h5>健康状态</h5><div class="conditions"><div v-for="condition in selectedPod.status?.conditions ?? []" :key="condition.type"><span>{{ condition.type }}</span><a-tag :color="workloadConditionTone(condition)">{{ ['True','False'].includes(condition.status)?condition.status:'Unknown' }}</a-tag><span v-if="condition.reason || condition.message" class="condition-message">{{ condition.message || condition.reason }}</span></div><p v-if="!selectedPod.status?.conditions?.length" class="secondary">暂无健康状态</p></div>
          </a-tab-pane>
          <a-tab-pane key="diagnostics" tab="运行诊断"><KubeResourceDiagnostics v-if="podTab==='diagnostics'" :key="`${cluster}/${selectedPod.metadata.uid}`" :cluster="cluster" :object="selectedPod" :type="podType" :auto-refresh="autoRefresh && !disabled" :refresh-token="refreshToken" :can-follow="canFollow" embedded @follow="emit('follow',$event)" /></a-tab-pane>
        </a-tabs>
      </template>
      <a-empty v-else-if="!podMissing" :description="readable ? '选择 Pod 查看运行状态' : '没有查看工作负载的权限'" :image-style="{ height: '40px' }" />
    </section>
  </section>
</template>

<style scoped>
.revision-list{position:relative}.revision-list::before{content:'';position:absolute;left:16px;top:24px;bottom:24px;width:1px;background:var(--edo-border)}.revision-list .revision-item{padding-left:29px}.revision-item::before{content:'';position:absolute;left:9px;top:17px;width:9px;height:9px;border-radius:50%;border:2px solid var(--edo-surface);background:var(--edo-border)}.revision-item.selected::before{background:var(--edo-primary)}
.workload-explorer{display:grid;grid-template-columns:minmax(240px,.85fr) minmax(0,1.7fr);border:1px solid var(--edo-border);border-radius:6px;overflow:hidden;background:var(--edo-surface)}.workload-explorer.is-deployment{grid-template-columns:220px minmax(250px,.9fr) minmax(0,1.6fr)}.explorer-column{min-width:0;min-height:300px;border-right:1px solid var(--edo-border)}.pod-detail-column{border-right:0}.explorer-column>header{height:48px;padding:8px 14px;border-bottom:1px solid var(--edo-border);background:var(--edo-surface-soft);display:flex;align-items:center;justify-content:space-between;gap:6px}.explorer-column h4,.workload-events h4{font-size:13px;margin:0;font-weight:600}.heading-count{font-size:12px;color:var(--edo-muted);font-weight:400;margin-left:5px}.revision-list,.pod-list{padding:6px 0;max-height:670px;overflow-y:auto}.revision-item,.pod-item{position:relative;width:100%;border:0;border-left:3px solid transparent;border-bottom:1px solid var(--edo-border);background:none;text-align:left;padding:12px 13px;display:flex;flex-direction:column;gap:7px;font:inherit;color:var(--edo-text);cursor:pointer;overflow-wrap:anywhere}.revision-item:hover,.pod-item:hover{background:var(--edo-surface-soft)}.revision-item.selected,.pod-item.selected{background:color-mix(in srgb,var(--edo-primary) 8%,var(--edo-surface));border-left-color:var(--edo-primary)}button:focus-visible{outline:2px solid var(--edo-primary);outline-offset:-3px}button:disabled{cursor:not-allowed;opacity:.65}.revision-heading,.pod-heading,.pod-status{display:flex;align-items:center;gap:7px}.revision-heading strong{font-size:13px}.revision-name{font-size:11px;color:var(--edo-muted)}.secondary{font-size:11px;color:var(--edo-muted)}.replica-counts{display:flex;gap:11px;font-size:11px;color:var(--edo-muted)}.replica-counts b{font-weight:500;color:var(--edo-text)}.pod-heading strong{font-size:12px;flex:1;min-width:0}.pod-heading>svg{flex:none}.pod-placement{display:grid;grid-template-columns:30px minmax(0,1fr);gap:6px;font-size:11px;color:var(--edo-muted)}.pod-placement code{font:inherit}.column-alert{margin:10px}.column-note{font-size:12px;color:var(--edo-muted);padding:0 12px;line-height:1.6}.pod-detail-heading{padding:14px 14px 0;display:flex;align-items:flex-start;gap:8px;font-size:13px;overflow-wrap:anywhere}.pod-detail-heading strong{flex:1;min-width:0}.pod-detail-tabs{padding:0 14px}.pod-detail-tabs :deep(.ant-tabs-tab){font-size:12px;padding-block:11px}.pod-detail-tabs :deep(.ant-tabs-nav){margin-bottom:8px}.pod-facts{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin:8px 0 18px}.pod-facts>div{min-width:0}.pod-facts dt{font-size:11px;color:var(--edo-muted);margin-bottom:4px}.pod-facts dd{font-size:12px;margin:0;overflow-wrap:anywhere}.inline-link{padding:0;border:0;background:none;color:var(--edo-primary);cursor:pointer;text-align:left;font:inherit}.container-toolbar{display:flex;align-items:center;gap:6px;flex-wrap:wrap;border-top:1px solid var(--edo-border);padding-top:12px;font-size:12px}.container-toolbar :deep(.ant-select){flex:1;min-width:120px}.container-toolbar :deep(.ant-btn){display:flex;align-items:center;gap:4px;padding-inline:8px;font-size:12px}.container-images{display:grid;gap:9px;margin:12px 0 18px}.container-images>div{display:grid;gap:3px;min-width:0}.container-images code{font-size:11px;overflow-wrap:anywhere}h5{font-size:12px;margin:12px 0 8px}.conditions>div{display:flex;align-items:center;gap:8px;padding:8px 0;border-bottom:1px solid var(--edo-border);font-size:11px;flex-wrap:wrap}.condition-message{flex-basis:100%;color:var(--edo-muted);line-height:1.6;overflow-wrap:anywhere}.workload-events{grid-column:1/-1;border-top:1px solid var(--edo-border);padding:12px 14px}.explorer-event{padding:10px 0;display:flex;align-items:flex-start;gap:8px;font-size:12px;border-bottom:1px solid var(--edo-border);overflow-wrap:anywhere;flex-wrap:wrap}.explorer-event p{margin:0;flex:1}.explorer-event>.secondary{margin-left:auto}.workload-events h4{margin-bottom:8px}.workload-explorer :deep(.ant-empty){margin:30px 12px}.workload-explorer :deep(.ant-tag){margin-inline-end:0;font-size:11px}.pod-detail-tabs :deep(.diagnostics){padding:0;border:0;background:transparent}.pod-detail-tabs :deep(.diagnostic-charts){grid-template-columns:repeat(auto-fit,minmax(min(100%,280px),1fr));gap:10px}.pod-detail-tabs :deep(.resource-table){min-width:670px}@media(max-width:1050px){.workload-explorer.is-deployment{grid-template-columns:220px minmax(0,1fr)}.is-deployment .pod-column{border-right:0}.is-deployment .pod-detail-column{grid-column:1/-1;border-top:1px solid var(--edo-border)}.is-deployment .revision-list,.is-deployment .pod-list{max-height:350px}}@media(max-width:680px){.workload-explorer,.workload-explorer.is-deployment{grid-template-columns:minmax(0,1fr)}.explorer-column{border-right:0;border-bottom:1px solid var(--edo-border);min-height:120px}.pod-detail-column{border-bottom:0}.is-deployment .pod-detail-column{border-top:0}.revision-list,.pod-list,.is-deployment .revision-list,.is-deployment .pod-list{max-height:280px}.pod-facts{grid-template-columns:1fr}.replica-counts{gap:20px}}
</style>
