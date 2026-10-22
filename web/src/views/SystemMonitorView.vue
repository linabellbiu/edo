<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { Cpu, Database, Gauge, MemoryStick, RadioTower, RefreshCw, TimerReset, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import MetricTrend from '@/components/MetricTrend.vue'
import { useAuthStore } from '@/stores/auth'
import type { MetricsHistory, SystemMetrics } from '@/types/systemMetrics'
import { formatMetric, mergeMetricsHistory, metricValue, monitorMetrics, type MetricDefinition } from '@/utils/systemMetrics'

const metrics = ref<SystemMetrics | null>(null), history = ref<MetricsHistory | null>(null)
const windowSeconds = ref(900), loading = ref(false), manualLoading = ref(false), historyError = ref('')
const clearingDeadMessages = ref(false)
const auth = useAuthStore()
const { t, locale } = useI18n()
let timer = 0, revision = 0, loadedWindow = 0, disposed = false, controller: AbortController | undefined
const canExecuteQueueAction = computed(() => auth.canAny(['monitor.execute']))
const ranges = computed(() => [900, 3600, 21600, 86400, 259200].map((value, i) => ({ value, label: t('systemMonitor.history.' + ['minutes15', 'hour1', 'hours6', 'hours24', 'days3'][i]) })))
const definition = (key: string) => monitorMetrics.find(item => item.key === key)!
const summaries = [
  { ...definition('cpu'), icon: Cpu, definitions: [definition('cpu'), definition('processCPU')] },
  { ...definition('memory'), icon: MemoryStick, definitions: [definition('memory')] },
  { ...definition('worker'), icon: Gauge, definitions: [definition('worker')] },
  { ...definition('pending'), icon: RadioTower, definitions: [definition('pending')] },
]
const panels = [
  { name: 'Worker 与任务', subtitle: '任务执行', icon: Gauge, keys: ['active', 'jobsPending', 'succeeded', 'failed', 'retried', 'outbox'] },
  { name: '消息队列', subtitle: 'JetStream', icon: RadioTower, keys: ['stored', 'storedBytes', 'consumers', 'redelivered', 'dead', 'deadBytes'] },
  { name: 'EDO 进程与 GC', subtitle: 'Go Runtime', icon: TimerReset, keys: ['rss', 'goroutines', 'heap', 'sys', 'gc', 'uptime'] },
  { name: '节点与数据库', subtitle: '基础资源', icon: Database, keys: ['load1', 'load5', 'connections', 'inUse', 'idle', 'wait'] },
].map(panel => ({ ...panel, definitions: panel.keys.map(definition) }))

function currentValue(item: MetricDefinition) {
  if (!metrics.value) return '—'
  const value = metricValue(metrics.value, item)
  if (value === null) return '—'
  if (item.key === 'worker') return value + ' / ' + metrics.value.worker.capacity
  if (item.key === 'connections') return value + ' / ' + metrics.value.database.max_open_connections
  return formatMetric(value, item.unit, locale.value)
}
function summaryDescription(key: string) {
  const data = metrics.value
  if (!data) return ''
  if (key === 'cpu') return data.host.logical_cpus + ' 个逻辑核心 · EDO ' + formatMetric(metricValue(data, definition('processCPU')), 'percent')
  if (key === 'memory') return '总计 ' + formatMetric(data.host.memory_total_bytes, 'bytes')
  if (key === 'worker') return data.worker.instances + ' 个实例 · 累计 ' + data.worker.executed + ' 次'
  return data.queue.pending_messages + ' 待投递 · ' + data.queue.ack_pending + ' 待确认'
}

async function refresh(manual = false, reset = false) {
  if (disposed || (loading.value && !reset)) return
  controller?.abort()
  const currentController = new AbortController()
  controller = currentController
  const request = ++revision, range = windowSeconds.value
  loading.value = true
  manualLoading.value = manual
  const replace = reset || loadedWindow !== range
  const after = replace ? 0 : Date.parse(history.value?.points.at(-1)?.collected_at || '') || 0
  try {
    let result = await client.get<MetricsHistory>('/system/metrics/history', { params: { window: range, after }, signal: currentController.signal })
    if (after && history.value?.node_id !== result.data.node_id) result = await client.get<MetricsHistory>('/system/metrics/history', { params: { window: range, after: 0 }, signal: currentController.signal })
    if (disposed || request !== revision) return
    history.value = mergeMetricsHistory(history.value, result.data, replace)
    loadedWindow = range
    metrics.value = result.data.latest
    historyError.value = ''
  } catch (error) {
    if (disposed || request !== revision || currentController.signal.aborted) return
    historyError.value = apiErrorMessage(error) || t('systemMonitor.history.error')
    try {
      const result = await client.get<SystemMetrics>('/system/metrics', { signal: currentController.signal })
      if (!disposed && request === revision) metrics.value = result.data
    } catch { /* 保留最后一次实时快照，历史失败已在页面明确提示。 */ }
  } finally {
    if (request === revision) { loading.value = false; manualLoading.value = false }
  }
}
function syncVisible() { if (!document.hidden) void refresh() }
function clearDeadMessages() {
  const count = metrics.value?.queue.dead_messages ?? 0
  if (!metrics.value?.queue.connected || !count || clearingDeadMessages.value) return
  Modal.confirm({
    title: t('systemMonitor.deadLetters.confirmTitle'),
    content: t('systemMonitor.deadLetters.confirmDescription', { count }),
    okText: t('systemMonitor.deadLetters.clear'), okType: 'danger', cancelText: t('systemMonitor.deadLetters.cancel'),
    async onOk() {
      clearingDeadMessages.value = true
      try {
        const response = await client.delete<{ purged_messages: number }>('/system/metrics/queue/dead-messages')
        message.success(t('systemMonitor.deadLetters.cleared', { count: response.data.purged_messages }))
        await refresh(true, true)
      } catch (error) { message.error(apiErrorMessage(error)) }
      finally { clearingDeadMessages.value = false }
    },
  })
}
watch(windowSeconds, () => { void refresh(true, true) })
onMounted(() => {
  void refresh()
  timer = window.setInterval(syncVisible, 5000)
  document.addEventListener('visibilitychange', syncVisible)
  window.addEventListener('focus', syncVisible)
})
onBeforeUnmount(() => {
  disposed = true; revision++; controller?.abort(); clearInterval(timer)
  document.removeEventListener('visibilitychange', syncVisible)
  window.removeEventListener('focus', syncVisible)
})
</script>

<template>
  <section>
    <PageToolbar :description="t('systemMonitor.history.description')">
      <span v-if="metrics" class="metrics-time">{{ new Date(metrics.collected_at).toLocaleTimeString(locale, { hour12: false }) }}</span>
      <a-button :loading="manualLoading" @click="refresh(true)"><RefreshCw :size="15" />{{ t('systemMonitor.history.refresh') }}</a-button>
    </PageToolbar>
    <div class="monitor-history-toolbar">
      <a-segmented v-model:value="windowSeconds" :options="ranges" :aria-label="t('systemMonitor.history.range')" />
      <span v-if="history" class="sampling-label">{{ t('systemMonitor.history.sampling', { seconds: history.interval_seconds }) }}</span>
      <a-tooltip :title="t('systemMonitor.history.note')" :mouse-enter-delay="0.1"><span class="history-note">{{ t('systemMonitor.history.note') }}</span></a-tooltip>
    </div>
    <a-alert v-if="historyError" type="warning" show-icon :message="historyError" class="metric-alert"><template #action><a-button size="small" @click="refresh(true, true)">{{ t('systemMonitor.history.retry') }}</a-button></template></a-alert>
    <a-alert v-if="metrics?.unavailable?.length" type="warning" show-icon :message="t('systemMonitor.history.unavailable', { items: metrics.unavailable.join('、') })" class="metric-alert" />
    <a-spin :spinning="loading && !metrics">
      <template v-if="metrics">
        <div class="metric-cards">
          <article v-for="item in summaries" :key="item.key" class="metric-card vben-card">
            <header><component :is="item.icon" /><span>{{ item.name }}</span></header>
            <strong>{{ currentValue(item) }}</strong><p>{{ summaryDescription(item.key) }}</p>
            <MetricTrend :history="history" :definitions="item.definitions" :height="78" show-time />
          </article>
        </div>
        <div class="monitor-grid">
          <article v-for="panel in panels" :key="panel.subtitle" class="monitor-panel vben-card">
            <header><div><span>{{ panel.subtitle }}</span><h3>{{ panel.name }}</h3></div>
              <a-tag v-if="panel.subtitle === '任务执行'" color="blue">{{ metrics.worker.consumer || '未就绪' }}</a-tag>
              <div v-else-if="panel.subtitle === 'JetStream'" class="queue-actions">
                <a-button v-if="canExecuteQueueAction" size="small" danger :disabled="!metrics.queue.connected || metrics.queue.dead_messages === 0" :loading="clearingDeadMessages" @click="clearDeadMessages"><Trash2 :size="14" />{{ t('systemMonitor.deadLetters.clear') }}</a-button>
                <a-tag :color="metrics.queue.connected ? 'success' : 'error'">{{ metrics.queue.connected ? '已连接' : '不可用' }}</a-tag>
              </div>
              <component v-else :is="panel.icon" />
            </header>
            <dl><div v-for="item in panel.definitions" :key="item.key">
              <dt>{{ item.name }}</dt><dd :class="{ danger: ['failed', 'dead'].includes(item.key) }">{{ currentValue(item) }}</dd>
              <MetricTrend :history="history" :definitions="[item]" />
            </div></dl>
          </article>
        </div>
      </template>
      <div v-else class="empty-panel vben-card">{{ t('systemMonitor.history.noData') }}</div>
    </a-spin>
  </section>
</template>

<style scoped>
.metrics-time{color:var(--edo-muted);font-size:12px}.metric-alert{margin-bottom:14px}.monitor-history-toolbar{display:flex;align-items:center;flex-wrap:wrap;gap:12px;margin-bottom:14px}.sampling-label{color:var(--edo-muted);font-size:12px;white-space:nowrap}.history-note{min-width:0;flex:1;color:var(--edo-muted);font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.metric-cards{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:14px}.metric-card{min-width:0;padding:16px 18px 12px}.metric-card>header{display:flex;align-items:center;gap:8px;color:var(--edo-muted);font-size:13px}.metric-card header svg{width:18px;height:18px;color:var(--edo-primary)}.metric-card strong{display:block;margin:14px 0 4px;font-size:25px;line-height:32px}.metric-card p{min-height:18px;margin:0 0 4px;color:var(--edo-muted);font-size:12px}.monitor-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px;margin-top:14px}.monitor-panel{min-width:0;padding:16px 18px}.monitor-panel>header{display:flex;align-items:center;justify-content:space-between;gap:10px}.monitor-panel header span{color:var(--edo-muted);font-size:12px}.monitor-panel h3{margin:2px 0 0;font-size:15px}.monitor-panel header>svg{width:20px;color:var(--edo-primary)}.queue-actions{display:flex;align-items:center;gap:8px}.queue-actions .ant-tag{margin-inline-end:0}dl{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));margin:14px 0 0}dl>div{min-width:0;padding:10px 12px 4px;border-top:1px solid var(--edo-border);border-right:1px solid var(--edo-border)}dl>div:nth-child(3n){border-right:0}dt{color:var(--edo-muted);font-size:12px}dd{margin:4px 0 2px;font-weight:650;font-size:15px;line-height:21px}.danger{color:var(--edo-danger,#e94f5f)}@media(max-width:1150px){.metric-cards{grid-template-columns:repeat(2,minmax(0,1fr))}}@media(max-width:760px){.metric-cards,.monitor-grid{grid-template-columns:1fr}.history-note{flex-basis:100%}}@media(max-width:460px){dl{grid-template-columns:repeat(2,minmax(0,1fr))}dl>div:nth-child(3n){border-right:1px solid var(--edo-border)}dl>div:nth-child(2n){border-right:0}.metric-card{padding:14px}}
</style>
