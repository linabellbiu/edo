<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { History } from 'lucide-vue-next'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { subscribeDeliveryEvents } from '@/utils/deliveryEvents'
import type { ApplicationWorkflowRunRecord as Run } from '@/types/pipeline'
import ApplicationWorkflowRunRow from './ApplicationWorkflowRunRow.vue'

const props = defineProps<{ applicationId: string; workflowId: string; scopeKey: string; workflowName?: string }>()
const emit = defineEmits<{ logs: [run: Run] }>()
const auth = useAuthStore()
const active = ref<Run[]>([]), history = ref<Run[]>([]), loading = ref(false), loaded = ref(false), error = ref('')
const historyExpanded = ref(false), historyLoaded = ref(false), historyError = ref('')
const canRead = computed(() => auth.canAny(['pipeline_run.read']))
const allowed = computed(() => auth.authorizationState === 'ready' && canRead.value)
const finished = new Set(['succeeded', 'failed', 'canceled', 'skipped'])
const rows = computed(() => {
  const previous = historyExpanded.value ? history.value : []
  const completed = new Set(previous.map(run => run.id))
  return [...active.value.filter(run => !completed.has(run.id)), ...previous]
})
const currentRuns = computed(() => rows.value.filter(run => !finished.has(run.status)))
const historyTrigger = ref<HTMLButtonElement>()
let historyOpenedScope = ''
let controller: AbortController | undefined, disposed = false, mounted = false, connected = false, revision = 0, refreshTimer: ReturnType<typeof setTimeout> | undefined, fallbackTimer: ReturnType<typeof setInterval> | undefined, unsubscribe: (() => void) | undefined
const identity = () => JSON.stringify([props.applicationId, props.workflowId, props.scopeKey])
function openHistory() { if (allowed.value) { historyOpenedScope = identity(); historyExpanded.value = true } }
function historyVisibilityChanged(open: boolean) {
  if (!open && !historyExpanded.value && !disposed && allowed.value && historyOpenedScope === identity()) historyTrigger.value?.focus({ preventScroll: true })
}
function cancel() { revision++; controller?.abort(); controller = undefined; clearTimeout(refreshTimer); refreshTimer = undefined; loading.value = false }
async function load() {
  if (!allowed.value || disposed || document.hidden) return
  controller?.abort()
  const request = ++revision, scope = identity(), pending = new AbortController()
  controller = pending; loading.value = true
  const current = () => !disposed && request === revision && !pending.signal.aborted && scope === identity() && allowed.value
  try {
    const params = { application_id: props.applicationId, workflow_id: props.workflowId, order: 'desc' }
    const belongs = (run: Run) => run.application_id === props.applicationId && run.workflow_id === props.workflowId
    const read = async (group: 'active' | 'finished') => {
      try {
        const response = await client.get<{ pipeline_runs: Run[] }>('/pipeline-runs', { params: { ...params, status_group: group, limit: group === 'active' ? 200 : 50 }, signal: pending.signal })
        if (!current()) return
        const seen = new Set<string>()
        const records = (response.data.pipeline_runs || []).filter(run => {
          if (!belongs(run) || finished.has(run.status) !== (group === 'finished') || seen.has(run.id)) return false
          seen.add(run.id); return true
        })
        if (group === 'active') { active.value = records; loaded.value = true; error.value = '' }
        else { history.value = records.slice(0, 50); historyLoaded.value = true; historyError.value = '' }
      } catch (cause) {
        if (current()) { if (group === 'active') error.value = apiErrorMessage(cause); else historyError.value = apiErrorMessage(cause) }
      }
    }
    await Promise.all([read('active'), ...(historyExpanded.value ? [read('finished')] : [])])
    if (current()) {
      const completed = new Set(history.value.map(run => run.id))
      active.value = active.value.filter(run => !completed.has(run.id))
    }
  }
  finally { if (current()) { loading.value = false; controller = undefined } }
}
function schedule() {
  if (disposed || !allowed.value || document.hidden || refreshTimer) return
  refreshTimer = setTimeout(() => { refreshTimer = undefined; void load() }, 300)
}
function visibility() { if (document.hidden) cancel(); else void load() }
function connect() {
  if (!mounted) return
  unsubscribe?.(); unsubscribe = undefined; connected = false
  if (!allowed.value) return
  unsubscribe = subscribeDeliveryEvents({ event: event => { if (event.type === 'resync' || !event.resources?.length || event.resources.includes('pipeline_runs')) schedule() }, state: value => { connected = value === 'connected' } })
}
watch(() => [props.applicationId, props.workflowId, props.scopeKey, allowed.value, historyExpanded.value], (value, previous) => {
  cancel()
  const scopeChanged = !previous || value.slice(0, 4).some((item, index) => item !== previous[index])
  if (scopeChanged) {
    active.value = []; loaded.value = false; error.value = ''; connect()
    if (historyExpanded.value) {
      history.value = []; historyLoaded.value = false; historyError.value = ''
      historyExpanded.value = false
      return
    }
  }
  if (scopeChanged || !historyExpanded.value) { history.value = []; historyLoaded.value = false; historyError.value = '' }
  void load()
}, { immediate: true })
onMounted(() => {
  mounted = true
  document.addEventListener('visibilitychange', visibility)
  connect()
  fallbackTimer = setInterval(() => { if (!connected) schedule() }, 60_000)
})
onBeforeUnmount(() => { disposed = true; cancel(); clearInterval(fallbackTimer); unsubscribe?.(); document.removeEventListener('visibilitychange', visibility) })
</script>

<template>
  <section class="workflow-runs" aria-label="当前流水线执行">
    <header>
      <span class="workflow-current-label">当前执行<span v-if="currentRuns.length" class="workflow-current-count">{{ currentRuns.length }}</span></span>
      <span v-if="!allowed" class="workflow-runs-empty">{{ canRead?'权限正在核验，暂时无法读取执行记录':'没有查看流水线运行记录的权限' }}</span>
      <span v-else-if="loading&&!loaded" class="workflow-runs-empty">正在读取执行状态…</span>
      <span v-else-if="loaded&&!currentRuns.length&&!error" class="workflow-runs-empty">当前没有正在执行的流水线</span>
      <div class="workflow-runs-actions"><button ref="historyTrigger" type="button" class="workflow-history-button" :disabled="!allowed" aria-haspopup="dialog" :aria-expanded="historyExpanded" @click="openHistory"><History :size="14" aria-hidden="true"/>历史执行</button><a-button size="small" :loading="loading" :disabled="!allowed" @click="load">刷新</a-button></div>
    </header>
    <template v-if="allowed">
      <a-alert v-if="error" type="error" show-icon :message="error"><template #action><a-button size="small" :disabled="loading" @click="load">重试</a-button></template></a-alert>
      <div v-if="currentRuns.length" class="workflow-current-runs" role="region" aria-label="正在执行的流水线" tabindex="0">
        <ApplicationWorkflowRunRow v-for="run in currentRuns" :key="run.id" :run="run" compact @logs="emit('logs',$event)"/>
      </div>
    </template>
    <a-drawer v-model:open="historyExpanded" :title="`${workflowName||'流水线'} · 历史执行`" width="min(960px, 94vw)" class="workflow-history-drawer" :body-style="{padding:0}" destroy-on-close @after-open-change="historyVisibilityChanged">
      <template #extra><a-button size="small" :loading="loading" :disabled="!allowed" @click="load">刷新</a-button></template>
      <section v-if="allowed" aria-label="最近 50 条历史执行">
        <p class="workflow-history-caption">最近 50 条历史执行</p>
        <a-alert v-if="historyError" type="error" show-icon :message="historyError"><template #action><a-button size="small" :disabled="loading" @click="load">重试</a-button></template></a-alert>
        <p v-if="loading&&!historyLoaded" class="workflow-history-empty">正在读取历史执行…</p>
        <p v-else-if="historyLoaded&&!history.length&&!historyError" class="workflow-history-empty">暂无历史执行</p>
        <ApplicationWorkflowRunRow v-for="run in history" :key="run.id" :run="run" @logs="emit('logs',$event)"/>
      </section>
    </a-drawer>
  </section>
</template>

<style scoped>
.workflow-runs{min-width:0;background:var(--edo-surface)}.workflow-runs>header{display:flex;min-height:36px;align-items:center;gap:12px;padding:4px 12px;color:var(--edo-muted);font-size:12px}.workflow-current-label{display:inline-flex;align-items:center;gap:6px;flex-shrink:0}.workflow-current-count{font-variant-numeric:tabular-nums;color:var(--edo-text)}.workflow-runs-empty{font-size:12px;overflow-wrap:anywhere}.workflow-runs-actions{display:flex;align-items:center;gap:10px;margin-left:auto;flex-shrink:0}.workflow-history-button{display:inline-flex;align-items:center;gap:4px;min-height:28px;padding:2px 4px;border:0;border-radius:4px;background:transparent;color:var(--edo-muted);cursor:pointer;font-size:12px}.workflow-history-button:hover{color:var(--edo-primary);background:var(--edo-primary-soft)}.workflow-history-button:focus-visible,.workflow-current-runs:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.workflow-history-button:disabled{cursor:not-allowed;opacity:.5}
.workflow-current-runs{max-height:180px;overflow:auto;border-top:1px solid var(--edo-border)}.workflow-history-caption{margin:0;padding:10px 14px;border-bottom:1px solid var(--edo-border);color:var(--edo-muted);font-size:12px}.workflow-history-empty{margin:0;padding:20px 14px;color:var(--edo-muted);font-size:13px}
@media(max-width:600px){.workflow-runs>header{flex-wrap:wrap;gap:4px 10px}.workflow-runs-empty{flex-basis:100%;order:1;padding-bottom:2px}}
</style>
