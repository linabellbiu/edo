<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { GitBranch, Tag, UserRound } from 'lucide-vue-next'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { subscribeDeliveryEvents } from '@/utils/deliveryEvents'
import { pipelineRunProgress, type PipelineRunProgressRecord } from '@/utils/pipelineRunProgress'
import PipelineRunStages from './PipelineRunStages.vue'
import PipelineRunProgress from './PipelineRunProgress.vue'

interface Run extends PipelineRunProgressRecord { row_id?: number; application_id: string; workflow_id?: string; created_at: string; ref: string; commit_sha: string; commit_message?: string; commit_author?: string; trigger: string }
const props = defineProps<{ applicationId: string; workflowId: string; scopeKey: string }>()
const emit = defineEmits<{ logs: [run: Run] }>()
const auth = useAuthStore()
const active = ref<Run[]>([]), history = ref<Run[]>([]), loading = ref(false), loaded = ref(false), error = ref('')
const canRead = computed(() => auth.canAny(['pipeline_run.read']))
const allowed = computed(() => auth.authorizationState === 'ready' && canRead.value)
const finished = new Set(['succeeded', 'failed', 'canceled', 'skipped'])
const rows = computed(() => [...active.value, ...history.value])
let controller: AbortController | undefined, disposed = false, mounted = false, connected = false, revision = 0, refreshTimer: ReturnType<typeof setTimeout> | undefined, fallbackTimer: ReturnType<typeof setInterval> | undefined, unsubscribe: (() => void) | undefined
const identity = () => JSON.stringify([props.applicationId, props.workflowId, props.scopeKey])
function cancel() { revision++; controller?.abort(); controller = undefined; clearTimeout(refreshTimer); refreshTimer = undefined; loading.value = false }
async function load() {
  if (!allowed.value || disposed || document.hidden) return
  controller?.abort()
  const request = ++revision, scope = identity(), pending = new AbortController()
  controller = pending; loading.value = true
  const current = () => !disposed && request === revision && !pending.signal.aborted && scope === identity() && allowed.value
  try {
    const params = { application_id: props.applicationId, workflow_id: props.workflowId, order: 'desc' }
    const [a, h] = await Promise.all([
      client.get<{ pipeline_runs: Run[] }>('/pipeline-runs', { params: { ...params, status_group: 'active', limit: 200 }, signal: pending.signal }),
      client.get<{ pipeline_runs: Run[] }>('/pipeline-runs', { params: { ...params, status_group: 'finished', limit: 50 }, signal: pending.signal }),
    ])
    if (!current()) return
    const belongs = (run: Run) => run.application_id === props.applicationId && run.workflow_id === props.workflowId
    active.value = (a.data.pipeline_runs || []).filter(run => belongs(run) && !finished.has(run.status))
    const seen = new Set(active.value.map(run => run.id))
    history.value = (h.data.pipeline_runs || []).filter(run => {
      if (!belongs(run) || !finished.has(run.status) || seen.has(run.id)) return false
      seen.add(run.id); return true
    }).slice(0, 50)
    loaded.value = true; error.value = ''
  } catch (cause) { if (current()) error.value = apiErrorMessage(cause) }
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
watch(() => [props.applicationId, props.workflowId, props.scopeKey, allowed.value], () => {
  cancel(); active.value = []; history.value = []; loaded.value = false; error.value = ''
  void load(); connect()
}, { immediate: true })
onMounted(() => {
  mounted = true
  document.addEventListener('visibilitychange', visibility)
  connect()
  fallbackTimer = setInterval(() => { if (!connected) schedule() }, 60_000)
})
onBeforeUnmount(() => { disposed = true; cancel(); clearInterval(fallbackTimer); unsubscribe?.(); document.removeEventListener('visibilitychange', visibility) })
function reference(run: Run) { return run.ref.replace(/^refs\/(heads|tags)\//, '') || '未记录版本' }
function time(value: string) { const date = new Date(value); return Number.isFinite(date.getTime()) ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' }).format(date) : '—' }
function trigger(run: Run) { return ({ manual: '手动触发', retry: '重新执行', poll_tag: 'Tag 触发', tag_push: 'Tag 触发', poll_push: '代码提交', branch_push: '代码提交', poll_pr: '合并请求', pull_request: '合并请求' } as Record<string, string>)[run.trigger] || '代码事件' }
</script>

<template>
  <section class="workflow-runs" aria-label="当前与历史流水线执行">
    <header><span>当前运行与最近 50 条历史执行</span><a-button size="small" :loading="loading" :disabled="!allowed" @click="load">刷新</a-button></header>
    <p v-if="!allowed" class="workflow-runs-empty">{{ canRead?'权限正在核验，暂时无法读取执行记录':'没有查看流水线运行记录的权限' }}</p>
    <template v-else>
      <a-alert v-if="error" type="error" show-icon :message="error"><template #action><a-button size="small" :disabled="loading" @click="load">重试</a-button></template></a-alert>
      <a-spin v-if="loading&&!loaded"/>
      <p v-else-if="loaded&&!rows.length&&!error" class="workflow-runs-empty">暂无运行记录</p>
      <article v-for="run in rows" :key="run.id" class="workflow-run" :aria-label="`执行记录：${run.commit_message||run.id}`">
        <div class="workflow-run-copy">
          <div class="workflow-run-title"><span class="workflow-run-status" :data-state="pipelineRunProgress(run).state"><i aria-hidden="true"/>{{ pipelineRunProgress(run).label }}</span><span v-if="run.row_id" class="workflow-run-number">#{{ run.row_id }}</span><strong>{{ run.commit_message||'未记录提交说明' }}</strong></div>
          <div class="workflow-run-meta"><span class="workflow-run-ref" :title="run.ref"><Tag v-if="run.ref.startsWith('refs/tags/')" :size="13" aria-hidden="true"/><GitBranch v-else :size="13" aria-hidden="true"/>{{ reference(run) }}</span><span>{{ trigger(run) }}</span><span class="workflow-run-author" :aria-label="`提交人：${run.commit_author||'未记录'}`"><UserRound :size="13" aria-hidden="true"/>{{ run.commit_author||'未记录提交人' }}</span><time :datetime="run.created_at">{{ time(run.created_at) }}</time><PipelineRunProgress :run="run" duration-only/></div>
        </div>
        <PipelineRunStages :stages="run.execution_graph?.stages" :status="run.status"/>
        <a-button size="small" @click="emit('logs',run)">日志</a-button>
      </article>
    </template>
  </section>
</template>

<style scoped>
.workflow-runs{min-width:0;background:var(--edo-surface)}.workflow-runs>header{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:10px 14px;border-bottom:1px solid var(--edo-border);color:var(--edo-muted);font-size:12px}.workflow-runs-empty{margin:0;padding:22px 14px;color:var(--edo-muted);font-size:13px}
.workflow-run{display:grid;grid-template-columns:minmax(260px,1fr) minmax(260px,1fr) auto;align-items:center;gap:18px;padding:14px;border-bottom:1px solid var(--edo-border)}.workflow-run:last-child{border-bottom:0}.workflow-run-copy{min-width:0}.workflow-run-title{display:flex;flex-wrap:wrap;align-items:baseline;gap:8px;line-height:1.6}.workflow-run-title>strong{font-size:13px;font-weight:500;overflow-wrap:anywhere}.workflow-run-number{font-size:12px;color:var(--edo-muted)}
.workflow-run-status{display:inline-flex;align-items:center;gap:5px;white-space:nowrap;color:var(--edo-muted);font-size:12px}.workflow-run-status>i{width:7px;height:7px;border-radius:50%;background:currentColor}.workflow-run-status[data-state='succeeded']{color:var(--edo-success,#16845b)}.workflow-run-status[data-state='failed']{color:var(--edo-danger,#d74354)}.workflow-run-status[data-state='running'],.workflow-run-status[data-state='waiting']{color:var(--edo-warning,#b68100)}
:global([data-theme='dark'] .workflow-run-status[data-state='succeeded']){color:#49c89c}:global([data-theme='dark'] .workflow-run-status[data-state='failed']){color:#ff8490}:global([data-theme='dark'] .workflow-run-status[data-state='running']),:global([data-theme='dark'] .workflow-run-status[data-state='waiting']){color:#f1c359}
.workflow-run-meta{display:flex;flex-wrap:wrap;align-items:center;gap:4px 12px;margin-top:5px;color:var(--edo-muted);font-size:12px}.workflow-run-ref{display:inline-flex;align-items:center;gap:5px;max-width:100%;overflow-wrap:anywhere}.workflow-run-ref svg{flex-shrink:0}.workflow-run-meta :deep(.run-progress-duration){font-size:12px;color:var(--edo-muted)}.workflow-run>button{justify-self:end}.workflow-runs :deep(.ant-spin){display:block;padding:20px}
.workflow-run-author{display:inline-flex;align-items:center;gap:5px;max-width:100%;overflow-wrap:anywhere}.workflow-run-author svg{flex-shrink:0}
@media(max-width:760px){.workflow-run{grid-template-columns:minmax(0,1fr) auto;gap:12px}.workflow-run :deep(.pipeline-run-stages),.workflow-run :deep(.pipeline-stages-empty){grid-row:2;grid-column:1/-1}.workflow-run-copy{grid-row:1}.workflow-run>button{grid-row:1;grid-column:2}}
</style>
