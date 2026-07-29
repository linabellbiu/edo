<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { Cpu, Database, Gauge, MemoryStick, RadioTower, RefreshCw, TimerReset, Trash2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import { useAuthStore } from '@/stores/auth'

interface SystemMetrics {
  collected_at: string
  host: { cpu_percent: number; logical_cpus: number; memory_total_bytes: number; memory_used_bytes: number; memory_available_bytes: number; memory_used_percent: number; load_1: number; load_5: number; load_15: number; uptime_seconds: number }
  process: { cpu_percent: number; rss_bytes: number; vms_bytes: number; uptime_seconds: number }
  runtime: { goroutines: number; gomaxprocs: number; heap_alloc_bytes: number; heap_inuse_bytes: number; stack_inuse_bytes: number; sys_bytes: number; next_gc_bytes: number; gc_cycles: number; gc_pause_total_ms: number; last_gc_at?: string }
  worker: { instances: number; consumer: string; capacity: number; active: number; executed: number; succeeded: number; failed: number; retried: number }
  jobs: { total: number; pending: number; running: number; succeeded: number; failed: number; canceled: number }
  outbox: { pending: number; failed: number }
  queue: { connected: boolean; stored_messages: number; stored_bytes: number; consumers: number; pending_messages: number; ack_pending: number; redelivered: number; waiting_pulls: number; dead_messages: number; dead_bytes: number }
  database: { max_open_connections: number; open_connections: number; in_use: number; idle: number; wait_count: number; wait_duration_ms: number }
  unavailable?: string[]
}

const metrics = ref<SystemMetrics | null>(null)
const loading = ref(false)
const clearingDeadMessages = ref(false)
const auth = useAuthStore()
const { t } = useI18n()
let timer = 0

function bytes(value = 0) { if (value <= 0) return '0 B'; const units = ['B','KiB','MiB','GiB','TiB']; const index = Math.min(Math.floor(Math.log(value)/Math.log(1024)),4); return `${(value/1024**index).toFixed(index > 1 ? 1 : 0)} ${units[index]}` }
function duration(seconds = 0) { const days=Math.floor(seconds/86400); const hours=Math.floor(seconds%86400/3600); const minutes=Math.floor(seconds%3600/60); return days ? `${days} 天 ${hours} 小时` : hours ? `${hours} 小时 ${minutes} 分钟` : `${minutes} 分钟` }
const workerPercent = computed(() => metrics.value?.worker.capacity ? Math.round(metrics.value.worker.active / metrics.value.worker.capacity * 100) : 0)
const queuePending = computed(() => (metrics.value?.queue.pending_messages ?? 0) + (metrics.value?.queue.ack_pending ?? 0))
const canManageQueue = computed(() => auth.canAny(['monitor.manage']))

async function refresh() { loading.value = true; try { metrics.value = (await client.get<SystemMetrics>('/system/metrics')).data } catch (error) { message.error(apiErrorMessage(error)) } finally { loading.value = false } }
function clearDeadMessages() {
  const count = metrics.value?.queue.dead_messages ?? 0
  if (!metrics.value?.queue.connected || !count || clearingDeadMessages.value) return
  Modal.confirm({
    title: t('systemMonitor.deadLetters.confirmTitle'),
    content: t('systemMonitor.deadLetters.confirmDescription', { count }),
    okText: t('systemMonitor.deadLetters.clear'),
    okType: 'danger',
    cancelText: t('systemMonitor.deadLetters.cancel'),
    async onOk() {
      clearingDeadMessages.value = true
      try {
        const response = await client.delete<{ purged_messages: number }>('/system/metrics/queue/dead-messages')
        message.success(t('systemMonitor.deadLetters.cleared', { count: response.data.purged_messages }))
        await refresh()
      } catch (error) {
        message.error(apiErrorMessage(error))
      } finally {
        clearingDeadMessages.value = false
      }
    },
  })
}
onMounted(() => { void refresh(); timer=window.setInterval(() => { if (!document.hidden) void refresh() },5000) })
onBeforeUnmount(() => clearInterval(timer))
</script>

<template>
  <section>
    <PageToolbar description="直接查看 ZRT 节点、进程、Worker、任务队列与数据库连接池，页面每 5 秒自动刷新。"><span v-if="metrics" class="metrics-time">{{ new Date(metrics.collected_at).toLocaleTimeString('zh-CN',{hour12:false}) }}</span><a-button :loading="loading" @click="refresh"><RefreshCw :size="15" />立即刷新</a-button></PageToolbar>
    <a-alert v-if="metrics?.unavailable?.length" type="warning" show-icon :message="`部分指标暂时不可用：${metrics.unavailable.join('、')}`" class="metric-alert" />
    <a-spin :spinning="loading && !metrics"><template v-if="metrics">
      <div class="metric-cards">
        <article class="metric-card vben-card"><div><Cpu /><span>节点 CPU</span></div><strong>{{ metrics.host.cpu_percent.toFixed(1) }}%</strong><p>{{ metrics.host.logical_cpus }} 个逻辑核心 · ZRT {{ metrics.process.cpu_percent.toFixed(1) }}%</p><a-progress :percent="metrics.host.cpu_percent" :show-info="false" /></article>
        <article class="metric-card vben-card"><div><MemoryStick /><span>节点内存</span></div><strong>{{ bytes(metrics.host.memory_used_bytes) }}</strong><p>总计 {{ bytes(metrics.host.memory_total_bytes) }}</p><a-progress :percent="metrics.host.memory_used_percent" :show-info="false" status="active" /></article>
        <article class="metric-card vben-card"><div><Gauge /><span>Worker 使用</span></div><strong>{{ metrics.worker.active }} / {{ metrics.worker.capacity }}</strong><p>{{ metrics.worker.instances }} 个实例 · 累计 {{ metrics.worker.executed }} 次</p><a-progress :percent="workerPercent" :show-info="false" stroke-color="#7b61ff" /></article>
        <article class="metric-card vben-card"><div><RadioTower /><span>队列待处理</span></div><strong>{{ queuePending }}</strong><p>{{ metrics.queue.pending_messages }} 待投递 · {{ metrics.queue.ack_pending }} 待确认</p><a-progress :percent="Math.min(100,queuePending)" :show-info="false" stroke-color="#e9a13d" /></article>
      </div>
      <div class="monitor-grid">
        <article class="monitor-panel vben-card"><header><div><span>任务执行</span><h3>Worker 与任务</h3></div><a-tag color="blue">{{ metrics.worker.consumer || '未就绪' }}</a-tag></header><dl><div><dt>正在执行</dt><dd>{{ metrics.worker.active }}</dd></div><div><dt>待执行任务</dt><dd>{{ metrics.jobs.pending }}</dd></div><div><dt>任务成功</dt><dd>{{ metrics.jobs.succeeded }}</dd></div><div><dt>任务失败</dt><dd class="danger">{{ metrics.jobs.failed }}</dd></div><div><dt>已安排重试</dt><dd>{{ metrics.worker.retried }}</dd></div><div><dt>Outbox 待发布</dt><dd>{{ metrics.outbox.pending }}</dd></div></dl></article>
        <article class="monitor-panel vben-card"><header><div><span>JetStream</span><h3>消息队列</h3></div><div class="queue-actions"><a-button v-if="canManageQueue" size="small" danger :disabled="!metrics.queue.connected || metrics.queue.dead_messages === 0" :loading="clearingDeadMessages" @click="clearDeadMessages"><Trash2 :size="14" />{{ t('systemMonitor.deadLetters.clear') }}</a-button><a-tag :color="metrics.queue.connected ? 'success' : 'error'">{{ metrics.queue.connected ? '已连接' : '不可用' }}</a-tag></div></header><dl><div><dt>Stream 消息</dt><dd>{{ metrics.queue.stored_messages }}</dd></div><div><dt>消息占用</dt><dd>{{ bytes(metrics.queue.stored_bytes) }}</dd></div><div><dt>Consumer</dt><dd>{{ metrics.queue.consumers }}</dd></div><div><dt>重投中</dt><dd>{{ metrics.queue.redelivered }}</dd></div><div><dt>死信</dt><dd class="danger">{{ metrics.queue.dead_messages }}</dd></div><div><dt>死信占用</dt><dd>{{ bytes(metrics.queue.dead_bytes) }}</dd></div></dl></article>
        <article class="monitor-panel vben-card"><header><div><span>Go Runtime</span><h3>ZRT 进程与 GC</h3></div><TimerReset /></header><dl><div><dt>进程 RSS</dt><dd>{{ bytes(metrics.process.rss_bytes) }}</dd></div><div><dt>Goroutine</dt><dd>{{ metrics.runtime.goroutines }}</dd></div><div><dt>堆已分配</dt><dd>{{ bytes(metrics.runtime.heap_alloc_bytes) }}</dd></div><div><dt>Go 总保留</dt><dd>{{ bytes(metrics.runtime.sys_bytes) }}</dd></div><div><dt>GC 次数</dt><dd>{{ metrics.runtime.gc_cycles }}</dd></div><div><dt>运行时间</dt><dd>{{ duration(metrics.process.uptime_seconds) }}</dd></div></dl></article>
        <article class="monitor-panel vben-card"><header><div><span>基础资源</span><h3>节点与数据库</h3></div><Database /></header><dl><div><dt>1 分钟负载</dt><dd>{{ metrics.host.load_1.toFixed(2) }}</dd></div><div><dt>5 分钟负载</dt><dd>{{ metrics.host.load_5.toFixed(2) }}</dd></div><div><dt>数据库连接</dt><dd>{{ metrics.database.open_connections }} / {{ metrics.database.max_open_connections }}</dd></div><div><dt>连接使用中</dt><dd>{{ metrics.database.in_use }}</dd></div><div><dt>空闲连接</dt><dd>{{ metrics.database.idle }}</dd></div><div><dt>累计等待</dt><dd>{{ metrics.database.wait_duration_ms.toFixed(1) }} ms</dd></div></dl></article>
      </div>
    </template><div v-else class="empty-panel vben-card">正在读取系统指标…</div></a-spin>
  </section>
</template>

<style scoped>
.metrics-time{color:var(--zrt-muted);font-size:12px}.metric-alert{margin-bottom:14px}.metric-cards{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:14px}.metric-card{padding:18px}.metric-card>div{display:flex;align-items:center;gap:8px;color:var(--zrt-muted)}.metric-card svg{width:19px;color:var(--zrt-primary)}.metric-card strong{display:block;margin:18px 0 5px;font-size:25px}.metric-card p{height:20px;margin:0 0 15px;color:var(--zrt-muted);font-size:12px}.monitor-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px;margin-top:14px}.monitor-panel{padding:19px}.monitor-panel header{display:flex;align-items:center;justify-content:space-between}.monitor-panel header span{color:var(--zrt-muted);font-size:11px}.monitor-panel h3{margin:2px 0 0}.monitor-panel header>svg{width:21px;color:var(--zrt-primary)}.queue-actions{display:flex;align-items:center;gap:8px}.queue-actions .ant-tag{margin-inline-end:0}dl{display:grid;grid-template-columns:repeat(3,1fr);gap:1px;margin:17px 0 0;background:var(--zrt-border)}dl>div{padding:13px;background:var(--zrt-surface)}dt{color:var(--zrt-muted);font-size:11px}dd{margin:5px 0 0;font-weight:650}.danger{color:#e94f5f}@media(max-width:1150px){.metric-cards{grid-template-columns:repeat(2,1fr)}}@media(max-width:760px){.metric-cards,.monitor-grid{grid-template-columns:1fr}dl{grid-template-columns:repeat(2,1fr)}}
</style>
