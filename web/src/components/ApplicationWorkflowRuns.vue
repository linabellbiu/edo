<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { subscribeDeliveryEvents } from '@/utils/deliveryEvents'
import { formatGitReference } from '@/utils/gitReference'
import { formatDateTime } from '@/utils/time'
import type { ApplicationWorkflowRunRecord as Run, PipelineRunGraphNode } from '@/types/pipeline'
import ApplicationWorkflowRunRow from './ApplicationWorkflowRunRow.vue'
import PipelineTaskHistory from './PipelineTaskHistory.vue'

const props = defineProps<{ applicationId: string; workflowId: string; scopeKey: string; workflowName?: string }>()
const emit = defineEmits<{ logs: [run: Run]; 'task-logs': [run: Run, node: Pick<PipelineRunGraphNode, 'id' | 'name'>] }>()
const auth = useAuthStore()
const allowed = computed(() => auth.authorizationState === 'ready' && auth.canAny(['pipeline_run.read']))
const historyExpanded = ref(false)
const selectedRun = ref<Run>()
const detailHeading = ref<HTMLElement>()
let historyTrigger: HTMLElement | undefined, detailTrigger: HTMLElement | undefined
let historyOpenedScope = ''
const identity = () => JSON.stringify([props.applicationId, props.workflowId, props.scopeKey])

function openHistory(event: MouseEvent) {
  if (!allowed.value || historyExpanded.value) return
  historyTrigger = event.currentTarget as HTMLElement
  historyOpenedScope = identity()
  historyExpanded.value = true
}
defineExpose({ openHistory })

function historyVisibilityChanged(open: boolean) {
  if (open || historyExpanded.value || disposed) return
  if (allowed.value && historyOpenedScope === identity() && historyTrigger?.isConnected) {
    historyTrigger.focus({ preventScroll: true })
  }
  historyTrigger = undefined
}

async function openDetails(run: Run, event: MouseEvent) {
  if (!allowed.value || !historyExpanded.value) return
  const record = history.value.find(record => record.id === run.id)
  if (!record) return
  detailTrigger = event.currentTarget as HTMLElement
  selectedRun.value = record
  await nextTick()
  detailHeading.value?.focus()
}

async function backToHistory() {
  const trigger = detailTrigger
  selectedRun.value = undefined
  detailTrigger = undefined
  await nextTick()
  if (allowed.value && historyExpanded.value && !selectedRun.value && trigger?.isConnected) {
    trigger.focus({ preventScroll: true })
  }
}

type RunGroup = 'active' | 'finished'
const active = ref<Run[]>([]), history = ref<Run[]>([])
const loading = ref(false), historyLoading = ref(false), historyLoaded = ref(false)
const error = ref(''), historyError = ref('')
const finished = new Set(['succeeded', 'failed', 'canceled', 'skipped'])
const historyLimit = 50
const pending = new Map<RunGroup, AbortController>()
let completedIDs = new Set<string>()
let disposed = false, mounted = false, connected = false
let refreshTimer: ReturnType<typeof setTimeout> | undefined, fallbackTimer: ReturnType<typeof setInterval> | undefined
let unsubscribe: (() => void) | undefined

function cancel(group: RunGroup) {
  pending.get(group)?.abort()
  pending.delete(group)
  if (group === 'active') loading.value = false
  else historyLoading.value = false
}

async function load(group: RunGroup) {
  if (!allowed.value || disposed || document.hidden || (group === 'finished' && !historyExpanded.value)) return
  cancel(group)
  const scope = identity()
  const controller = new AbortController()
  const requestLoading = group === 'active' ? loading : historyLoading
  const requestError = group === 'active' ? error : historyError
  pending.set(group, controller)
  requestLoading.value = true
  const current = () => !disposed && pending.get(group) === controller && scope === identity() &&
    allowed.value && (group === 'active' || historyExpanded.value)
  try {
    const params = { application_id: props.applicationId, workflow_id: props.workflowId, order: 'desc', status_group: group, limit: group === 'active' ? 200 : historyLimit }
    const { data } = await client.get<{ pipeline_runs: Run[] }>('/pipeline-runs', { params, signal: controller.signal })
    if (!current()) return
    const seen = new Set<string>()
    const records = (data.pipeline_runs || []).filter(run => {
      if (run.application_id !== props.applicationId || run.workflow_id !== props.workflowId ||
        finished.has(run.status) !== (group === 'finished') || seen.has(run.id)) return false
      seen.add(run.id)
      return true
    })
    if (group === 'active') active.value = records.filter(run => !completedIDs.has(run.id))
    else {
      history.value = records.slice(0, historyLimit)
      historyLoaded.value = true
      completedIDs = new Set(history.value.map(run => run.id))
      active.value = active.value.filter(run => !completedIDs.has(run.id))
    }
    requestError.value = ''
  } catch (cause) {
    if (current()) requestError.value = apiErrorMessage(cause)
  } finally {
    if (pending.get(group) === controller) {
      pending.delete(group)
      requestLoading.value = false
    }
  }
}

function refresh() {
  void load('active')
  void load('finished')
}
function schedule() {
  if (disposed || !allowed.value || document.hidden || refreshTimer) return
  refreshTimer = setTimeout(() => { refreshTimer = undefined; refresh() }, 300)
}
function cancelReads() {
  cancel('active')
  cancel('finished')
  clearTimeout(refreshTimer)
  refreshTimer = undefined
}
function visibility() {
  if (document.hidden) cancelReads()
  else refresh()
}
function connect() {
  if (!mounted) return
  unsubscribe?.()
  unsubscribe = undefined
  connected = false
  if (!allowed.value) return
  unsubscribe = subscribeDeliveryEvents({
    event: event => { if (event.type === 'resync' || !event.resources?.length || event.resources.includes('pipeline_runs')) schedule() },
    state: value => { connected = value === 'connected' },
  })
}

watch([identity, allowed], () => {
  cancelReads()
  historyExpanded.value = false
  completedIDs.clear()
  active.value = []
  error.value = ''
  connect()
  void load('active')
}, { immediate: true })
watch(historyExpanded, open => {
  if (open) void load('finished')
  else {
    cancel('finished')
    history.value = []
    historyLoaded.value = false
    historyError.value = ''
    selectedRun.value = undefined
    detailTrigger = undefined
  }
})
onMounted(() => {
  mounted = true
  document.addEventListener('visibilitychange', visibility)
  connect()
  fallbackTimer = setInterval(() => { if (!connected) schedule() }, 60_000)
})
onBeforeUnmount(() => {
  disposed = true
  cancelReads()
  clearInterval(fallbackTimer)
  unsubscribe?.()
  document.removeEventListener('visibilitychange', visibility)
})
</script>

<template>
  <section v-if="allowed && (active.length || error)" class="workflow-runs" aria-label="当前流水线执行">
    <a-alert v-if="error" type="error" show-icon :message="error">
      <template #action><a-button size="small" :loading="loading" @click="load('active')">重试</a-button></template>
    </a-alert>
    <div v-if="active.length" class="workflow-current-runs" role="region" aria-label="正在执行的流水线" tabindex="0">
      <ApplicationWorkflowRunRow v-for="run in active" :key="run.id" :run="run" compact @logs="emit('logs', $event)"/>
    </div>
  </section>

  <a-drawer v-model:open="historyExpanded" :title="`${workflowName || '流水线'} · ${selectedRun ? '执行详情' : '历史记录'}`"
    width="min(960px, 94vw)" class="workflow-history-drawer" :body-style="{ padding: 0, overflow: 'hidden' }"
    destroy-on-close @after-open-change="historyVisibilityChanged">
    <template #extra>
      <a-button v-if="selectedRun" size="small" @click="backToHistory">返回历史记录</a-button>
      <a-button v-else size="small" :loading="historyLoading" :disabled="!allowed" @click="load('finished')">刷新</a-button>
    </template>
    <template v-if="allowed && historyExpanded">
      <section v-show="!selectedRun" class="workflow-history-content" aria-label="最近 50 条历史执行">
        <p class="workflow-history-caption">最近 50 条历史执行</p>
        <a-alert v-if="historyError" type="error" show-icon :message="historyError">
          <template #action><a-button size="small" :loading="historyLoading" @click="load('finished')">重试</a-button></template>
        </a-alert>
        <p v-if="historyLoading && !historyLoaded" class="workflow-history-empty" role="status">正在读取历史执行…</p>
        <p v-else-if="historyLoaded && !history.length && !historyError" class="workflow-history-empty">暂无历史执行</p>
        <ApplicationWorkflowRunRow v-for="run in history" :key="run.id" :run="run" show-details
          @logs="emit('logs', $event)" @details="openDetails"/>
      </section>
      <section v-if="selectedRun" :key="selectedRun.id" class="workflow-history-content workflow-run-detail" aria-label="执行详情">
        <header class="workflow-run-detail-heading">
          <h3 ref="detailHeading" tabindex="-1"><span v-if="selectedRun.row_id">#{{ selectedRun.row_id }}</span>{{ selectedRun.commit_message || '未记录提交说明' }}</h3>
          <div class="workflow-run-detail-meta">
            <time :datetime="selectedRun.created_at">{{ formatDateTime(selectedRun.created_at) }}</time>
            <span>{{ formatGitReference({ ref: selectedRun.ref, sha: selectedRun.commit_sha, trigger: selectedRun.trigger }) }}</span>
          </div>
        </header>
        <p v-if="selectedRun.message" class="workflow-run-detail-message">{{ selectedRun.message }}</p>
        <PipelineTaskHistory
          :run-id="selectedRun.id" :updated-at="selectedRun.updated_at" :graph="selectedRun.execution_graph"
          :current-node-id="selectedRun.current_node_id" :status="selectedRun.status" :stage="selectedRun.stage"
          @task-logs="node => selectedRun && emit('task-logs', selectedRun, node)"/>
      </section>
    </template>
  </a-drawer>
</template>

<style scoped>
.workflow-runs { min-width: 0; background: var(--edo-surface); }
.workflow-current-runs { max-height: 180px; overflow: auto; }
.workflow-history-content { height: 100%; overflow: auto; }
.workflow-history-caption { margin: 0; padding: 10px 14px; border-bottom: 1px solid var(--edo-border); color: var(--edo-muted); font-size: 12px; }
.workflow-history-empty { margin: 0; padding: 20px 14px; color: var(--edo-muted); font-size: 13px; }
.workflow-run-detail { padding: 16px; }
.workflow-run-detail-heading { padding-bottom: 12px; margin-bottom: 12px; border-bottom: 1px solid var(--edo-border); }
.workflow-run-detail-heading h3 { margin: 0 0 4px; font-size: 15px; font-weight: 600; overflow-wrap: anywhere; }
.workflow-run-detail-heading h3 > span { margin-inline-end: 6px; color: var(--edo-muted); font-weight: 500; }
.workflow-run-detail-meta { display: flex; flex-wrap: wrap; gap: 4px 12px; color: var(--edo-muted); font-size: 12px; overflow-wrap: anywhere; }
.workflow-run-detail-message { margin: 0 0 12px; white-space: pre-wrap; overflow-wrap: anywhere; }
.workflow-current-runs:focus-visible, .workflow-run-detail-heading h3:focus-visible { outline: 2px solid var(--edo-primary); outline-offset: -2px; }
</style>
