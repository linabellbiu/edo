<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Box, CheckCircle2, ChevronRight, FileText, TerminalSquare } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { kubeErrorMessage, kubeTypes, resourceURL, type KubeObject } from '@/api/kubernetes'
import { resourceStatus, type KubeReference } from '@/utils/kubernetesPresentation'
import { podRuntimeContainers } from '@/utils/kubernetesDiagnostics'
import { podContainerChoices } from '@/utils/kubernetesWorkspace'
import { podExplorerEvents, workloadConditionTone, type WorkloadContainerTarget } from '@/utils/kubernetesWorkloadExplorer'
import { formatDateTime } from '@/utils/time'
import KubeResourceDiagnostics from './KubeResourceDiagnostics.vue'
import KubeWorkloadEvents from './KubeWorkloadEvents.vue'

const props = defineProps<{
  cluster: string
  pod: KubeObject
  container: string
  autoRefresh: boolean
  refreshToken: number
  disabled?: boolean
  can: (permission: string) => boolean
  canFollow: (reference: KubeReference) => boolean
}>()
const emit = defineEmits<{
  follow: [reference: KubeReference, target: WorkloadContainerTarget]
  container: [target: WorkloadContainerTarget]
  logs: [target: WorkloadContainerTarget]
  terminal: [target: WorkloadContainerTarget]
}>()
const { t } = useI18n()
const podType = kubeTypes.find(type => type.resource === 'pods')!
const readable = computed(() => props.can('kubernetes.workload.read'))
const context = computed(() => `${props.cluster}/${props.pod.metadata.namespace}/${props.pod.metadata.uid}`)
const containers = computed(() => readable.value ? podRuntimeContainers(props.pod) : [])
const state = computed(() => resourceStatus(props.pod, t))
const events = ref<KubeObject[]>([]), eventsLoading = ref(false), eventsError = ref('')
const missingContainer = computed(() => Boolean(props.container) && !containers.value.some(item => item.name === props.container))
let revision = 0, disposed = false, controller: AbortController | undefined, timer: ReturnType<typeof setInterval> | undefined

const conditionLabel = (type: string) => ({ Initialized: '已初始化', PodScheduled: '已调度', PodReadyToStartContainers: '可启动容器', Ready: '容器组已就绪', ContainersReady: '容器已就绪', DisruptionTarget: '正在中断' } as Record<string, string>)[type] ?? type
function containerState(item: KubeObject) {
  const current = item.runtime?.state
  if (current?.running) return { text: '运行中', tone: 'success', at: current.running.startedAt }
  if (current?.waiting) return { text: current.waiting.reason || '等待中', tone: 'warning', message: current.waiting.message }
  if (current?.terminated) return { text: current.terminated.reason || '已退出', tone: current.terminated.exitCode === 0 ? 'default' : 'error', at: current.terminated.finishedAt, exit: current.terminated.exitCode }
  return { text: '状态未知', tone: 'default' }
}
function allowedContainer(name: string) {
  return !props.disabled && readable.value && !missingContainer.value && containers.value.some(item => item.name === name)
}
function allowedTerminal(name: string) {
  return allowedContainer(name) && props.can('terminal.open') && !props.pod.metadata.deletionTimestamp
    && podContainerChoices(props.pod).find(container => container.name === name)?.running === true
}
function containerTarget(name: string): WorkloadContainerTarget {
  return { cluster: props.cluster, namespace: props.pod.metadata.namespace, podUID: props.pod.metadata.uid, container: name }
}
function chooseContainer(name: string) {
  if (!props.disabled && readable.value && containers.value.some(item => item.name === name)) emit('container', containerTarget(name))
}
function openLogs(name: string, terminal = false) {
  // 操作仍由父层用当前 Pod UID 和容器状态再次核验。
  const item = containers.value.find(container => container.name === name)
  if (!item || !allowedContainer(name) || terminal && !allowedTerminal(name)) return
  const target = containerTarget(name)
  emit('container', target)
  if (terminal) emit('terminal', target)
  else emit('logs', target)
}
function follow(reference: KubeReference) {
  if (!props.disabled && readable.value && props.canFollow(reference)) emit('follow', reference, containerTarget(props.container))
}
async function loadEvents() {
  if (!readable.value || !props.cluster || !props.pod.metadata.uid) return
  controller?.abort()
  const request = new AbortController(), current = ++revision, identity = context.value
  controller = request; eventsLoading.value = true
  try {
    const { data } = await client.get(`${resourceURL(props.cluster, podType, props.pod.metadata.name)}/events`, { params: { namespace: props.pod.metadata.namespace, uid: props.pod.metadata.uid }, signal: request.signal })
    if (disposed || request.signal.aborted || current !== revision || identity !== context.value || !readable.value) return
    const eventTime = (event: KubeObject) => Date.parse(event.series?.lastObservedTime ?? event.lastTimestamp ?? event.eventTime ?? event.metadata?.creationTimestamp ?? '') || 0
    events.value = podExplorerEvents(Array.isArray(data.items) ? data.items : [], props.pod.metadata.uid).sort((left, right) => eventTime(right) - eventTime(left))
    eventsError.value = ''
  } catch (error) {
    if (!disposed && current === revision && !request.signal.aborted && identity === context.value && readable.value) { events.value = []; eventsError.value = kubeErrorMessage(error) }
  } finally { if (!disposed && current === revision) eventsLoading.value = false }
}
watch([context, readable], () => {
  revision++; controller?.abort(); events.value = []; eventsError.value = ''; eventsLoading.value = false
  if (readable.value) void loadEvents()
}, { immediate: true })
watch(() => props.refreshToken, () => { void loadEvents() })
function syncVisible() { if (!document.hidden && props.autoRefresh && !props.disabled && !eventsLoading.value) void loadEvents() }
onMounted(() => { timer = setInterval(syncVisible, 30_000); document.addEventListener('visibilitychange', syncVisible) })
onBeforeUnmount(() => { disposed = true; revision++; controller?.abort(); if (timer) clearInterval(timer); document.removeEventListener('visibilitychange', syncVisible) })
</script>

<template>
  <div class="workload-pod-runtime" :aria-busy="eventsLoading">
    <section class="runtime-events" aria-label="所选 Pod 事件">
      <KubeWorkloadEvents :events="events" :error="Boolean(eventsError)" :error-text="eventsError" :loading="eventsLoading" compact empty-text="该 Pod 暂无事件" />
    </section>
    <KubeResourceDiagnostics v-if="readable" :key="context" :cluster="cluster" :object="pod" :type="podType" :auto-refresh="autoRefresh && !disabled" :refresh-token="refreshToken" :can-follow="canFollow" embedded compact trend @follow="follow" />

    <section class="runtime-pod-card">
      <header class="runtime-pod-title">
        <Box :size="15" /><span>容器组</span><strong :title="pod.metadata.name">{{ pod.metadata.name }}</strong>
        <button v-if="canFollow({resource:'pods',name:pod.metadata.name,namespace:pod.metadata.namespace,uid:pod.metadata.uid})" class="pod-detail-link" :disabled="disabled" @click="follow({resource:'pods',name:pod.metadata.name,namespace:pod.metadata.namespace,uid:pod.metadata.uid})">完整详情 <ChevronRight :size="13" /></button>
      </header>
      <dl class="runtime-pod-facts">
        <div><dt>所在节点</dt><dd><button v-if="pod.spec?.nodeName && canFollow({resource:'nodes',name:pod.spec.nodeName})" class="runtime-link" :disabled="disabled" @click="follow({resource:'nodes',name:pod.spec.nodeName})">{{ pod.spec.nodeName }}</button><span v-else>{{ pod.spec?.nodeName || '未调度' }}</span></dd></div>
        <div><dt>容器组 IP</dt><dd>{{ pod.status?.podIP || '—' }}</dd></div>
        <div><dt>状态</dt><dd><a-tag :color="state.tone">{{ state.text }}</a-tag></dd></div>
        <div><dt>重启策略</dt><dd>{{ pod.spec?.restartPolicy || '—' }}</dd></div>
      </dl>
      <div class="runtime-conditions" aria-label="Pod 健康状态">
        <div v-for="condition in pod.status?.conditions ?? []" :key="condition.type" :class="['runtime-condition',`condition-${workloadConditionTone(condition)}`]" :title="[condition.reason,condition.message].filter(Boolean).join(' · ')">
          <CheckCircle2 :size="13" v-if="workloadConditionTone(condition)==='success'" /><span v-else class="condition-dot" />
          <strong>{{ conditionLabel(condition.type) }}</strong><span>{{ condition.status === 'True' ? '' : condition.status || 'Unknown' }}</span><time>{{ formatDateTime(condition.lastTransitionTime) }}</time>
        </div>
        <p v-if="!pod.status?.conditions?.length" class="runtime-empty">暂无健康状态</p>
      </div>
      <a-alert v-if="missingContainer" type="warning" show-icon message="所选容器已不存在，请重新选择容器；不会自动切换日志或终端目标。" />
      <div class="runtime-containers" aria-label="容器运行状态">
        <article v-for="item in containers" :key="`${item.containerKind}/${item.name}`" class="runtime-container" :class="{selected:item.name===container}">
          <button class="container-heading" :disabled="disabled" :aria-expanded="item.name===container" @click="chooseContainer(item.name)">
            <Box :size="22" /><a-tag>{{ item.containerKind==='app'?'容器':item.containerKind==='init'?'Init 容器':'临时容器' }}</a-tag><strong>{{ item.name }}</strong><a-tag :color="containerState(item).tone">{{ containerState(item).text }}</a-tag>
          </button>
          <p class="container-image" :title="item.image"><span>镜像拉取策略：{{ item.imagePullPolicy || '—' }}</span><span>镜像：<code>{{ item.image || '—' }}</code></span></p>
          <template v-if="item.name===container">
            <div class="container-current-state"><span>当前状态</span><a-tag :color="item.runtime?.started===true?'success':item.runtime?.started===false?'warning':'default'">Started {{ item.runtime?.started===true?'是':item.runtime?.started===false?'否':'—' }}</a-tag><a-tag :color="item.runtime?.ready===true?'success':item.runtime?.ready===false?'warning':'default'">Ready {{ item.runtime?.ready===true?'是':item.runtime?.ready===false?'否':'—' }}</a-tag><a-tag :color="containerState(item).tone">{{ containerState(item).text }}</a-tag><span>重启 {{ item.runtime?.restartCount ?? '—' }} 次</span><time v-if="containerState(item).at">{{ formatDateTime(containerState(item).at) }}</time></div>
            <p v-if="containerState(item).message" class="runtime-message">{{ containerState(item).message }}</p>
            <p v-if="containerState(item).exit!==undefined" class="runtime-message">退出码 {{ containerState(item).exit }}</p>
            <div v-if="item.runtime?.lastState?.terminated" class="previous-state"><span>上次退出</span><strong>{{ item.runtime.lastState.terminated.reason || '—' }}</strong><span>exit {{ item.runtime.lastState.terminated.exitCode ?? '—' }}</span><time>{{ formatDateTime(item.runtime.lastState.terminated.finishedAt) }}</time></div>
            <div class="container-action-bar"><a-button :disabled="!allowedContainer(item.name)" @click="openLogs(item.name)"><FileText :size="14" />追踪日志</a-button><a-button v-if="can('terminal.open')" :disabled="!allowedTerminal(item.name)" :title="allowedTerminal(item.name)?undefined:item.containerKind==='init'?'Init 容器只支持查看日志，不支持终端':'容器未运行或当前不可操作'" @click="openLogs(item.name,true)"><TerminalSquare :size="14" />终端</a-button></div>
          </template>
        </article>
      </div>
    </section>
  </div>
</template>

<style scoped>
.workload-pod-runtime{min-width:0;color:var(--edo-text)}.runtime-events{padding:0 0 8px}.runtime-pod-card{border:1px solid var(--edo-border);background:var(--edo-surface);border-radius:4px;overflow:hidden}.runtime-pod-title{display:flex;align-items:center;gap:8px;background:var(--edo-kube-accent,#415c87);color:#fff;padding:8px 12px;font-size:12px;min-width:0}.runtime-pod-title>strong{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-family:var(--edo-font-mono,monospace)}.pod-detail-link{display:flex;align-items:center;gap:2px;color:inherit;border:1px solid #ffffff55;border-radius:3px;background:#ffffff12;padding:2px 6px;white-space:nowrap;font:inherit;cursor:pointer}.runtime-pod-facts{display:grid;grid-template-columns:minmax(0,1.25fr) minmax(0,1fr) minmax(0,.8fr) minmax(0,.7fr);gap:12px;margin:0;padding:14px 14px 10px}.runtime-pod-facts>div{min-width:0}.runtime-pod-facts dt{font-size:12px;color:var(--edo-muted);margin-bottom:6px}.runtime-pod-facts dd{font-size:12px;margin:0;overflow-wrap:anywhere;font-weight:600}.runtime-pod-facts :deep(.ant-tag){margin:0}.runtime-link{color:var(--edo-primary);background:none;border:0;padding:0;cursor:pointer;text-align:left;font:inherit}.runtime-conditions{padding:4px 14px 10px;display:grid;gap:5px}.runtime-condition{display:flex;align-items:center;gap:7px;padding:5px 8px;background:var(--edo-surface-soft);font-size:11px;min-width:0}.runtime-condition strong{font-weight:500;min-width:108px}.runtime-condition time{margin-left:auto;color:var(--edo-muted)}.condition-dot{width:7px;height:7px;border-radius:50%;background:var(--edo-muted);margin:0 3px}.condition-success>svg{color:var(--edo-success,#23ad8c)}.condition-warning>.condition-dot{background:#d69222}.condition-error>.condition-dot{background:#d74757}.runtime-empty{color:var(--edo-muted);font-size:12px}.runtime-containers{display:grid;gap:10px;padding:0 14px 14px}.runtime-container{border:1px solid var(--edo-border);border-radius:4px;background:var(--edo-surface);padding:12px;min-width:0}.runtime-container.selected{box-shadow:inset 3px 0 var(--edo-kube-accent,#415c87)}.container-heading{display:flex;align-items:center;gap:8px;width:100%;border:0;background:none;padding:0;color:inherit;font:inherit;cursor:pointer;text-align:left}.container-heading>strong{flex:1;min-width:0;overflow-wrap:anywhere;font-weight:600}.container-heading :deep(.ant-tag){margin:0;font-size:11px}.container-heading>svg{color:var(--edo-muted);flex:none}.container-image{display:flex;align-items:center;gap:4px 12px;color:var(--edo-muted);font-size:11px;line-height:1.6;margin:8px 0 10px;min-width:0;flex-wrap:wrap}.container-image>span:last-child{min-width:0;overflow-wrap:anywhere}.container-image code{font:inherit}.container-current-state,.previous-state{display:flex;gap:4px 8px;align-items:center;flex-wrap:wrap;background:var(--edo-surface-soft);padding:5px 7px;font-size:11px;color:var(--edo-muted)}.container-current-state :deep(.ant-tag){font-size:10px;line-height:17px;margin:0}.previous-state{margin-top:6px}.runtime-message{font-size:12px;overflow-wrap:anywhere;margin:8px 0}.container-action-bar{display:flex;gap:0;margin-top:9px}.container-action-bar :deep(.ant-btn){display:flex;align-items:center;gap:5px;border-radius:0;font-size:12px;height:29px}.container-action-bar :deep(.ant-btn:first-child){border-radius:3px 0 0 3px}.container-action-bar :deep(.ant-btn:last-child){border-radius:0 3px 3px 0}.container-action-bar :deep(.ant-btn:not(:disabled)){background:var(--edo-kube-accent,#415c87);color:#fff;border-color:var(--edo-kube-accent,#415c87)}button:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}button:disabled{cursor:not-allowed;opacity:.55}.runtime-pod-card>:deep(.ant-alert){margin:0 14px 12px}@media(max-width:1100px){.runtime-pod-facts{grid-template-columns:repeat(2,minmax(0,1fr))}.runtime-pod-title{flex-wrap:wrap}.runtime-pod-title>strong{flex-basis:70%}}@media(max-width:540px){.runtime-conditions time{width:100%;margin-left:20px}.runtime-condition{flex-wrap:wrap}.runtime-pod-facts{padding:10px}.runtime-containers{padding:0 10px 10px}.runtime-container{padding:10px}}
</style>
