import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, compileStyle, compileTemplate, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import postcss from 'postcss'
import ts from 'typescript'
import * as vue from 'vue'
import * as metrics from '../src/utils/systemMetrics.ts'
import { createMetricTrendUnitHistory } from './fixtures/metric-trend-unit-data.mjs'

const source = readFileSync(new URL('../src/components/MetricTrend.vue', import.meta.url), 'utf8')
const { descriptor, errors } = parse(source)
const scope = 'metric-unit-layout'
assert.deepEqual(errors, [])
const script = compileScript(descriptor, { id: scope, inlineTemplate: true })
const template = compileTemplate({ source: descriptor.template.content, filename: 'MetricTrend.vue', id: scope, compilerOptions: { bindingMetadata: script.bindings } })
assert.deepEqual(template.errors, [])
const output = ts.transpileModule(script.content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
const modules = {
  vue,
  'vue-i18n': { useI18n: () => ({ locale: vue.ref('zh-CN'), t: (key, values) => key === 'systemMonitor.history.unitHint' ? `单位：${values.unit}` : key === 'systemMonitor.history.count' ? '次数' : key }) },
  '@/utils/systemMetrics': metrics,
  '@/stores/preferences': { usePreferencesStore: () => ({ theme: 'light' }) },
  'echarts/core': { use() {}, init() { throw new Error('内存渲染不能初始化真实图表') } },
}
const componentModule = { exports: {} }
new Function('require', 'module', 'exports', output)(name => modules[name] ?? {}, componentModule, componentModule.exports)
const component = componentModule.exports.default
const history = { node_id: 'unit-layout', from: '2026-10-28T06:00:00Z', to: '2026-10-31T06:00:00Z', interval_seconds: 60, points: [] }
const render = (unit, props = {}) => renderToString(vue.createSSRApp(component, { history, definitions: [{ key: 'unit', name: '单位测试', unit, read: () => null }], showTime: true, ...props }))

test('小图单位编译后的局部CSS不收缩、不逐字换行，不改变时间布局', () => {
  const declarations = new Map()
  for (const style of descriptor.styles) {
    const compiled = compileStyle({ source: style.content, filename: 'MetricTrend.vue', id: scope, scoped: true })
    assert.deepEqual(compiled.errors, [])
    postcss.parse(compiled.code).walkRules(rule => {
      if (!['.metric-trend-unit', '.metric-trend-time'].some(name => rule.selector === `${name}[data-v-${scope}]`)) return
      declarations.set(rule.selector, Object.fromEntries(rule.nodes.filter(node => node.type === 'decl').map(node => [node.prop, node.value])))
    })
  }
  const unit = declarations.get(`.metric-trend-unit[data-v-${scope}]`)
  assert.ok(unit, '单位需要独立的局部样式，不能影响两个时间标签')
  assert.equal(unit['flex-shrink'], '0')
  assert.equal(unit['white-space'], 'nowrap')
  const footer = declarations.get(`.metric-trend-time[data-v-${scope}]`)
  assert.equal(footer.display, 'flex')
  assert.equal(footer['justify-content'], 'space-between')
  assert.equal(footer.gap, 'min(6px, 2%)')
})

test('真实三天模板将字节、百分比、计数单位渲染为完整独立span，保留两端时间', async () => {
  for (const [unit, label] of [['bytes', 'B'], ['percent', '%'], ['count', '次数']]) {
    const html = await render(unit)
    assert.match(html, new RegExp(`<span class="metric-trend-unit">单位：${label}</span>`))
    const times = [...html.matchAll(/<time>([^<]+)<\/time>/g)].map(match => match[1])
    assert.equal(times.length, 2)
    assert.equal(times[0], new Date(history.from).toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }))
    assert.equal(times[1], new Date(history.to).toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }))
  }
})

test('关闭时间或历史不可用时，不新增单位与时间占位', async () => {
  for (const props of [{ showTime: false }, { history: null }]) {
    const html = await render('bytes', props)
    assert.doesNotMatch(html, /metric-trend-unit|<time>/)
  }
})

test('本地视觉夹具使用固定公开三天样本，真实组件入口与模板样式可编译', () => {
  const history = createMetricTrendUnitHistory()
  assert.equal(Date.parse(history.to) - Date.parse(history.from), 3 * 86400000)
  assert.equal(history.points.length, 73)
  assert.deepEqual(createMetricTrendUnitHistory(), history)
  for (const key of ['rss', 'cpu', 'jobsPending']) {
    const metric = metrics.monitorMetrics.find(item => item.key === key)
    const data = metrics.metricsChartData(history, [metric])
    assert.equal(data[0].length, 73)
    assert.ok(data[1].every(Number.isFinite))
  }
  const text = readFileSync(new URL('./fixtures/MetricTrendUnitPreview.vue', import.meta.url), 'utf8')
  const { descriptor, errors } = parse(text)
  assert.deepEqual(errors, [])
  const compiled = compileScript(descriptor, { id: 'metric-unit-preview' })
  assert.deepEqual(compileTemplate({ source: descriptor.template.content, filename: 'MetricTrendUnitPreview.vue', id: 'metric-unit-preview', compilerOptions: { bindingMetadata: compiled.bindings } }).errors, [])
  for (const style of descriptor.styles) assert.deepEqual(compileStyle({ source: style.content, filename: 'MetricTrendUnitPreview.vue', id: 'metric-unit-preview', scoped: true }).errors, [])
  assert.match(text, /import MetricTrend from '\/src\/components\/MetricTrend\.vue'/)
  const entry = readFileSync(new URL('./fixtures/metric-trend-unit.mjs', import.meta.url), 'utf8')
  for (const value of [entry, text]) assert.doesNotMatch(value, /fetch\(|XMLHttpRequest|WebSocket|localStorage|document\.cookie|\/api\//)
})
