import type { MetricsHistory, SystemMetrics } from '@/types/systemMetrics'

export type MetricUnit = 'percent' | 'bytes' | 'count' | 'number' | 'seconds' | 'ms'
export interface MetricDefinition {
  key: string
  name: string
  unit: MetricUnit
  component: string
  processScoped?: boolean
  read: (sample: SystemMetrics) => number
}

export const monitorMetrics: MetricDefinition[] = [
  { key: 'cpu', name: '节点 CPU', unit: 'percent', component: 'cpu', read: s => s.host.cpu_percent },
  { key: 'memory', name: '节点内存', unit: 'bytes', component: 'host_memory', read: s => s.host.memory_used_bytes },
  { key: 'worker', name: 'Worker 使用', unit: 'count', component: 'worker', processScoped: true, read: s => s.worker.active },
  { key: 'pending', name: '队列待处理', unit: 'count', component: 'queue', read: s => s.queue.pending_messages + s.queue.ack_pending },
  { key: 'processCPU', name: 'EDO CPU', unit: 'percent', component: 'cpu', processScoped: true, read: s => s.process.cpu_percent },
  { key: 'active', name: '正在执行', unit: 'count', component: 'worker', processScoped: true, read: s => s.worker.active },
  { key: 'jobsPending', name: '待执行任务', unit: 'count', component: 'jobs', read: s => s.jobs.pending },
  { key: 'succeeded', name: '任务成功', unit: 'count', component: 'jobs', read: s => s.jobs.succeeded },
  { key: 'failed', name: '任务失败', unit: 'count', component: 'jobs', read: s => s.jobs.failed },
  { key: 'retried', name: '已安排重试', unit: 'count', component: 'worker', processScoped: true, read: s => s.worker.retried },
  { key: 'outbox', name: 'Outbox 待发布', unit: 'count', component: 'outbox', read: s => s.outbox.pending },
  { key: 'stored', name: 'Stream 消息', unit: 'count', component: 'queue', read: s => s.queue.stored_messages },
  { key: 'storedBytes', name: '消息占用', unit: 'bytes', component: 'queue', read: s => s.queue.stored_bytes },
  { key: 'consumers', name: 'Consumer', unit: 'count', component: 'queue', read: s => s.queue.consumers },
  { key: 'redelivered', name: '重投中', unit: 'count', component: 'queue', read: s => s.queue.redelivered },
  { key: 'dead', name: '死信', unit: 'count', component: 'queue', read: s => s.queue.dead_messages },
  { key: 'deadBytes', name: '死信占用', unit: 'bytes', component: 'queue', read: s => s.queue.dead_bytes },
  { key: 'rss', name: '进程 RSS', unit: 'bytes', component: 'process_memory', processScoped: true, read: s => s.process.rss_bytes },
  { key: 'goroutines', name: 'Goroutine', unit: 'count', component: 'runtime', processScoped: true, read: s => s.runtime.goroutines },
  { key: 'heap', name: '堆已分配', unit: 'bytes', component: 'runtime', processScoped: true, read: s => s.runtime.heap_alloc_bytes },
  { key: 'sys', name: 'Go 总保留', unit: 'bytes', component: 'runtime', processScoped: true, read: s => s.runtime.sys_bytes },
  { key: 'gc', name: 'GC 累计次数', unit: 'count', component: 'runtime', processScoped: true, read: s => s.runtime.gc_cycles },
  { key: 'uptime', name: '运行时间', unit: 'seconds', component: 'process', processScoped: true, read: s => s.process.uptime_seconds },
  { key: 'load1', name: '1 分钟负载', unit: 'number', component: 'load', read: s => s.host.load_1 },
  { key: 'load5', name: '5 分钟负载', unit: 'number', component: 'load', read: s => s.host.load_5 },
  { key: 'connections', name: '数据库连接', unit: 'count', component: 'database', read: s => s.database.open_connections },
  { key: 'inUse', name: '连接使用中', unit: 'count', component: 'database', read: s => s.database.in_use },
  { key: 'idle', name: '空闲连接', unit: 'count', component: 'database', read: s => s.database.idle },
  { key: 'wait', name: '累计等待', unit: 'ms', component: 'database', processScoped: true, read: s => s.database.wait_duration_ms },
]

export function metricValue(sample: SystemMetrics, metric: MetricDefinition): number | null {
  const missing = sample.unavailable || []
  if (missing.includes(metric.component) || (metric.processScoped && missing.includes('process')) || (metric.component === 'queue' && !sample.queue.connected)) return null
  const value = metric.read(sample)
  return Number.isFinite(value) ? value : null
}

export function formatMetric(value: number | null, unit: MetricUnit, locale = 'zh-CN') {
  if (value === null || !Number.isFinite(value)) return '—'
  if (unit === 'bytes') {
    if (value <= 0) return '0 B'
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
    const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), 4)
    return `${(value / 1024 ** index).toFixed(index > 1 ? 1 : 0)} ${units[index]}`
  }
  if (unit === 'percent') return `${value.toFixed(1)}%`
  if (unit === 'ms') return `${value.toFixed(1)} ms`
  if (unit === 'seconds') return `${Math.floor(value / 60).toLocaleString(locale)} ${locale.startsWith('zh') ? '分钟' : 'min'}`
  return value.toLocaleString(locale, { maximumFractionDigits: unit === 'number' ? 2 : 0 })
}

export function mergeMetricsHistory(previous: MetricsHistory | null, next: MetricsHistory, replace = false): MetricsHistory {
  const samples = new Map<number, SystemMetrics>()
  const minimum = Date.parse(next.from), maximum = Date.parse(next.to), interval = next.interval_seconds * 1000
  const old = !replace && previous?.node_id === next.node_id && previous.interval_seconds === next.interval_seconds ? previous.points : []
  for (const sample of [...old, ...next.points]) {
    const timestamp = Date.parse(sample.collected_at)
    if (timestamp >= minimum && timestamp <= maximum) samples.set(Math.floor(timestamp / interval), sample)
  }
  return { ...next, points: [...samples.values()].sort((a, b) => Date.parse(a.collected_at) - Date.parse(b.collected_at)).slice(-4321) }
}

export function metricsChartData(history: MetricsHistory, definitions: MetricDefinition[]) {
  const timestamps: number[] = [], values: Array<Array<number | null>> = definitions.map(() => [])
  let previous: SystemMetrics | undefined
  for (const sample of history.points) {
    const timestamp = Date.parse(sample.collected_at) / 1000
    if (!Number.isFinite(timestamp)) continue
    const gap = previous && (timestamp - Date.parse(previous.collected_at) / 1000 > history.interval_seconds * 2.5)
    const restarted = previous && sample.process.uptime_seconds < previous.process.uptime_seconds
    if (gap || (restarted && definitions.some(item => item.processScoped))) {
      timestamps.push((Date.parse(previous!.collected_at) / 1000 + timestamp) / 2)
      definitions.forEach((_item, index) => values[index]!.push(null))
    }
    timestamps.push(timestamp)
    definitions.forEach((item, index) => values[index]!.push(metricValue(sample, item)))
    previous = sample
  }
  return [timestamps, ...values] as [number[], ...Array<Array<number | null>>]
}
