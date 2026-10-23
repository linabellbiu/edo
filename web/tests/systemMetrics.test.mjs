import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import * as metrics from '../src/utils/systemMetrics.ts'

const baseTime = Date.parse('2026-10-22T12:00:00Z')
function sample(seconds, changes = {}) {
  return { collected_at: new Date(baseTime + seconds * 1000).toISOString(), host: { cpu_percent: 1, logical_cpus: 4, memory_used_bytes: 1000, memory_total_bytes: 10000, load_1: 0.1, load_5: 0.2 }, process: { uptime_seconds: 100 + seconds, cpu_percent: 0.5, rss_bytes: 500 }, runtime: { goroutines: 20, gc_cycles: 2, sys_bytes: 1000, heap_alloc_bytes: 200 }, worker: { capacity: 8, active: 0, executed: 0, retried: 0, instances: 1 }, jobs: { pending: 0, succeeded: 2, failed: 0 }, outbox: { pending: 0 }, queue: { connected: true, pending_messages: 0, ack_pending: 0, stored_messages: 0, stored_bytes: 0, consumers: 1, redelivered: 0, dead_messages: 0, dead_bytes: 0 }, database: { open_connections: 5, max_open_connections: 25, in_use: 0, idle: 5, wait_duration_ms: 0 }, ...changes }
}
function history(points, changes = {}) {
  return { node_id: 'node-a', from: new Date(baseTime).toISOString(), to: new Date(baseTime + 900000).toISOString(), interval_seconds: 5, points, latest: points.at(-1) || sample(0), ...changes }
}
const definition = key => metrics.monitorMetrics.find(item => item.key === key)

test('历史增量按真实时间去重，分钟桶替换旧点，不混合节点或超出窗口', () => {
  const previous = history([sample(1), sample(6)])
  const next = metrics.mergeMetricsHistory(previous, history([sample(6), sample(11)]))
  assert.deepEqual(next.points.map(item => item.collected_at), [sample(1), sample(6), sample(11)].map(item => item.collected_at))
  const coarse = metrics.mergeMetricsHistory(history([sample(1)], { interval_seconds: 60 }), history([sample(16)], { interval_seconds: 60 }))
  assert.equal(coarse.points.length, 1)
  assert.equal(coarse.points[0].collected_at, sample(16).collected_at)
  assert.equal(metrics.mergeMetricsHistory(previous, history([sample(11)], { node_id: 'node-b' })).points.length, 1)
  assert.equal(metrics.mergeMetricsHistory(previous, history([sample(11)], { from: sample(10).collected_at })).points.length, 1)
})

test('不可用采样、队列断线和采集断档保持空缺，不伪造零；零值仍是有效采样', () => {
  assert.equal(metrics.metricValue(sample(0), definition('worker')), 0)
  assert.equal(metrics.metricValue(sample(0, { unavailable: ['cpu'] }), definition('cpu')), null)
  assert.equal(metrics.metricValue(sample(0, { unavailable: ['process'] }), definition('processCPU')), null)
  assert.equal(metrics.metricValue(sample(0, { queue: { connected: false } }), definition('pending')), null)
  const data = metrics.metricsChartData(history([sample(0), sample(5), sample(40)]), [definition('cpu')])
  assert.equal(data[0].length, 4)
  assert.equal(data[1][2], null)
  assert.equal(data[1][3], 1)
  const reset = metrics.metricsChartData(history([sample(0), sample(5, { process: { uptime_seconds: 1, cpu_percent: 0.1 } })]), [definition('cpu'), definition('processCPU')])
  assert.equal(reset[1][1], null)
  assert.equal(reset[2][1], null)
  assert.equal(metrics.formatMetric(null, 'bytes'), '—')
  assert.equal(metrics.formatMetric(1024, 'bytes'), '1 KiB')
})

const source = readFileSync(new URL('../src/views/SystemMonitorView.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(compileScript(parse(source).descriptor, { id: 'system-metrics-test' }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } })
function harness(t) {
  const requests = [], failures = [], unmount = []
  const modules = {
    vue: { ...vue, onMounted() {}, onBeforeUnmount: fn => unmount.push(fn) },
    'vue-i18n': { useI18n: () => ({ t: (key, values) => values ? key + ':' + JSON.stringify(values) : key, locale: vue.ref('zh-CN') }) },
    'ant-design-vue': { message: { error: value => failures.push(value) }, Modal: {} },
    '@/stores/auth': { useAuthStore: () => ({ canAny: () => false }) },
    '@/api/resources': { apiErrorMessage: () => '历史读取失败' },
    '@/utils/systemMetrics': metrics,
    '@/api/client': { __esModule: true, default: { get(path, options) {
      if (path === '/system/metrics') return Promise.resolve({ data: sample(0) })
      return new Promise((resolve, reject) => requests.push({ path, options, resolve: data => resolve({ data }), reject }))
    } } },
  }
  const module = { exports: {} }, scope = vue.effectScope()
  new Function('require', 'module', 'exports', 'document', 'window', outputText)(name => modules[name] || {}, module, module.exports, { hidden: false, removeEventListener() {} }, { removeEventListener() {} })
  const state = scope.run(() => module.exports.default.setup({}, { expose() {} }))
  t.after(() => { unmount.forEach(fn => fn()); scope.stop() })
  return { state, requests, failures, unmount }
}

test('首屏分开展示服务器内存与 EDO RSS，不把整机用量当成进程用量', t => {
  const { state } = harness(t)
  const base = sample(0)
  state.metrics.value = sample(0, {
    host: { ...base.host, memory_used_bytes: 4 * 1024 ** 3, memory_total_bytes: 32 * 1024 ** 3 },
    process: { ...base.process, rss_bytes: 128 * 1024 ** 2 },
    runtime: { ...base.runtime, heap_alloc_bytes: 32 * 1024 ** 2 },
  })
  const summary = key => state.summaries.value.find(item => item.key === key)
  assert.equal(state.summaries.value.length, 5)
  assert.equal(summary('rss').name, 'systemMonitor.memory.processRSS')
  assert.equal(summary('memory').name, 'systemMonitor.memory.hostUsed')
  assert.equal(state.currentValue(summary('memory')), '4.0 GiB')
  assert.equal(state.currentValue(summary('rss')), '128.0 MiB')
  assert.equal(summary('rss').definitions[0].read(state.metrics.value), 128 * 1024 ** 2)
  assert.equal(state.summaryDescription('rss'), 'systemMonitor.memory.processHeap:{"heap":"32.0 MiB"}')
  assert.equal(state.summaryDescription('memory'), 'systemMonitor.memory.hostTotal:{"total":"32.0 GiB"}')
  assert.equal(state.panels.value.find(panel => panel.subtitle === 'Go Runtime').definitions[0].name, summary('rss').name)

  state.metrics.value = { ...state.metrics.value, unavailable: ['process_memory', 'runtime', 'host_memory'] }
  assert.equal(state.currentValue(summary('rss')), '—')
  assert.equal(state.currentValue(summary('memory')), '—')
  assert.equal(state.summaryDescription('rss'), 'systemMonitor.memory.processHeap:{"heap":"—"}')
  assert.equal(state.summaryDescription('memory'), 'systemMonitor.memory.hostTotal:{"total":"—"}')
})

test('首次读取完整历史，后续使用采样游标；时间范围失败后仍重新读取完整窗口', async t => {
  const { state, requests } = harness(t)
  const first = state.refresh()
  assert.equal(requests[0].options.params.after, 0)
  requests[0].resolve(history([sample(0), sample(5)]))
  await first
  const delta = state.refresh()
  assert.equal(requests[1].options.params.after, baseTime + 5000)
  requests[1].resolve(history([sample(10)]))
  await delta
  assert.equal(state.history.value.points.length, 3)
  state.windowSeconds.value = 21600
  await vue.nextTick()
  assert.equal(requests[2].options.params.after, 0)
  requests[2].reject(new Error('temporarily unavailable'))
  await new Promise(resolve => setImmediate(resolve))
  assert.equal(state.historyError.value, '历史读取失败')
  const retry = state.refresh()
  assert.equal(requests[3].options.params.window, 21600)
  assert.equal(requests[3].options.params.after, 0)
  requests[3].resolve(history([sample(20)], { interval_seconds: 60 }))
  await retry
  assert.equal(state.history.value.interval_seconds, 60)
  assert.equal(state.historyError.value, '')
})

test('切换窗口丢弃迟到响应，离开页面后不写回历史，权限不足不展示清理操作', async t => {
  const { state, requests, unmount } = harness(t)
  const first = state.refresh()
  const second = state.refresh(true, true)
  assert.equal(requests[0].options.signal.aborted, true)
  requests[1].resolve(history([sample(10)]))
  await second
  requests[0].resolve(history([sample(0)]))
  await first
  assert.equal(state.metrics.value.collected_at, sample(10).collected_at)
  assert.equal(state.canExecuteQueueAction.value, false)
  const last = state.refresh()
  unmount.forEach(fn => fn())
  requests[2].resolve(history([sample(30)]))
  await last
  assert.equal(state.metrics.value.collected_at, sample(10).collected_at)
})

test('ECharts 首次绘制与重复刷新保留时间轴、断档和单点，卸载释放实例', t => {
  const mounted = [], unmount = [], plots = []
  class Plot {
    constructor() { plots.push(this) }
    setOption(options) { this.options = options }
    resize() {}
    on() {}
    getZr() { return { on() {} } }
    dispatchAction() {}
    dispose() { this.destroyed = true }
  }
  const modules = {
    vue: { ...vue, onMounted: fn => mounted.push(fn), onBeforeUnmount: fn => unmount.push(fn) },
    'vue-i18n': { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) },
    '@/stores/preferences': { usePreferencesStore: () => ({ theme: 'light' }) },
    '@/utils/systemMetrics': metrics,
    'echarts/core': { init: () => new Plot(), use() {} },
  }
  const text = readFileSync(new URL('../src/components/MetricTrend.vue', import.meta.url), 'utf8')
  const compiled = ts.transpileModule(compileScript(parse(text).descriptor, { id: 'metric-trend-test' }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }, scope = vue.effectScope()
  new Function('require', 'module', 'exports', 'ResizeObserver', compiled)(name => modules[name] || {}, module, module.exports, class { observe() {}; disconnect() {} })
  const props = vue.reactive({ history: history([sample(0)]), definitions: [definition('cpu')], height: 52, showTime: false })
  const state = scope.run(() => module.exports.default.setup(props, { expose() {} }))
  state.canvas.value = { clientWidth: 240 }
  mounted.forEach(fn => fn())
  assert.equal(plots.length, 1)
  assert.equal(plots[0].options.series[0].showSymbol, true)
  assert.equal(plots[0].options.animation, false)
  assert.equal(plots[0].options.series[0].connectNulls, false)
  props.history = history([sample(0), sample(5)])
  state.render()
  assert.equal(plots[0].options.series[0].showSymbol, false)
  assert.equal(plots[0].options.xAxis.min, baseTime)
  assert.equal(plots[0].options.xAxis.max, baseTime + 900000)
  assert.deepEqual(plots[0].options.series[0].data, [[baseTime, 1], [baseTime + 5000, 1]])
  props.history = history([sample(0), sample(40)])
  state.render()
  assert.equal(plots[0].options.series[0].data[1][1], null)
  t.after(() => { unmount.forEach(fn => fn()); scope.stop(); assert.equal(plots[0].destroyed, true) })
})
