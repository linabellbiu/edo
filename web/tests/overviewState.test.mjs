import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { compileTemplate, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import { RefreshCw, Users } from 'lucide-vue-next'
import ts from 'typescript'
import * as vue from 'vue'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const ready = (patch = {}) => ({ status: 'ok', checks: { database: 'ok', redis: 'ok', nats: 'ok', builder: 'ok' }, ...patch })
function harness(t, getReadyStatus) {
  const unmounts = [], module = loadFrontend('../src/stores/system.ts', { vue, pinia: { defineStore: (_name, setup) => setup }, '@/api/client': { getReadyStatus } })
  const system = vue.reactive(module.useSystemStore())
  const h = componentHarness('../src/views/OverviewView.vue', { modules: { vue: { ...vue, onMounted() {}, onBeforeUnmount: fn => unmounts.push(fn) }, '@/stores/system': { useSystemStore: () => system } } })
  const stop = () => { unmounts.forEach(fn => fn()); h.stop() }; t.after(stop)
  return { ...h, system, stop }
}

async function renderOverview(state) {
  const descriptor = parse(readFileSync(new URL('../src/views/OverviewView.vue', import.meta.url), 'utf8')).descriptor
  const template = compileTemplate({ source: descriptor.template.content, filename: 'OverviewView.vue', id: 'overview-scale-test' })
  assert.deepEqual(template.errors, [])
  const output = ts.transpileModule(template.code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} }
  new Function('require', 'exports', output)(name => { assert.equal(name, 'vue'); return vue }, module.exports)
  const app = vue.createSSRApp({ setup: () => ({ ...state }), render: module.exports.render })
  for (const tag of ['PageToolbar', 'a-button', 'a-alert', 'a-tag']) app.component(tag, { setup: (_props, { slots }) => () => vue.h('div', slots.default?.()) })
  app.component('RefreshCw', RefreshCw); app.component('Users', Users)
  return renderToString(app)
}

function renderedAttributes(html, tag, name) {
  const chart = html.match(/<svg\b[^>]*aria-label="服务可用性趋势"[^>]*>[\s\S]*?<\/svg>/)?.[0]
  assert.ok(chart, '必须检查概览趋势 SVG，而不是页面图标')
  return [...chart.matchAll(new RegExp(`<${tag}\\b[^>]*\\b${name}="([^"]*)"`, 'g'))].map(match => match[1])
}

test('概览0/25/50/75/100%的真实采样点与百分比纵轴采用同一坐标', async t => {
  for (const percent of [0, 25, 50, 75, 100]) await t.test(`${percent}%`, async t => {
    const checks = Object.fromEntries(['database', 'redis', 'nats', 'builder'].map((key, index) => [key, index < percent / 25 ? 'ok' : key === 'builder' ? 'unavailable' : 'failed']))
    const h = harness(t, async () => ready({ status: percent === 100 ? 'ok' : percent === 75 ? 'degraded' : 'failed', checks }))
    await h.state.refresh()
    assert.equal(h.state.healthPercent.value, percent)
    assert.deepEqual(h.state.points.value, [{ x: 0, y: 80 - percent * .8 }])
  })
})

test('概览真实模板保留原有网格位置，单点75%位于60%与80%对应尺度', async t => {
  const h = harness(t, async () => ready())
  h.state.history.value = [75]
  const html = await renderOverview(h.state)
  assert.deepEqual([...html.matchAll(/class="grid-line" style="top:([\d.]+)%;?"><span>(\d+)%<\/span>/g)].map(match => [Number(match[2]), Number(match[1])]), [[80, 16], [60, 32], [40, 48], [20, 64], [0, 80]])
  assert.deepEqual(renderedAttributes(html, 'circle', 'cy'), ['20'])
  assert.deepEqual(renderedAttributes(html, 'polyline', 'points'), [])
  assert.deepEqual(renderedAttributes(html, 'polygon', 'points'), [])
})

test('概览真实模板面积止于0%刻度，折线与圆点使用一致的坐标且不跨断档', async t => {
  const h = harness(t, async () => ready())
  h.state.history.value = [0, 25, 50, 75, 100]
  const continuous = await renderOverview(h.state)
  const polygon = renderedAttributes(continuous, 'polygon', 'points')[0].split(' ')
  assert.equal(polygon[0], '0,80'); assert.equal(polygon.at(-1), '100,80')
  assert.deepEqual(renderedAttributes(continuous, 'polyline', 'points'), ['0,80 25,60 50,40 75,20 100,0'])
  h.state.history.value = [0, null, 100]
  const interrupted = await renderOverview(h.state)
  assert.deepEqual(renderedAttributes(interrupted, 'circle', 'cy'), ['80', '0'])
  assert.deepEqual(renderedAttributes(interrupted, 'polyline', 'points'), [])
  assert.deepEqual(renderedAttributes(interrupted, 'polygon', 'points'), [])
})

test('概览缺测保持空值和中性，不造趋势点；真实四项检查全不正常仍保留0%', async t => {
  const h = harness(t, async () => ready({ status: 'failed', checks: { database: 'failed', redis: 'failed', nats: 'failed', builder: 'unavailable' } }))
  assert.equal(h.state.healthPercent.value, null); assert.equal(h.state.checkCount.value, 0)
  assert.deepEqual(h.state.cards.value.map(card => card.value), ['—', '—', '—', '—'])
  assert.ok(h.state.cards.value.every(card => card.status !== 'failed'))
  assert.deepEqual(h.state.points.value, []); assert.deepEqual(h.state.segments.value, [])
  await h.state.refresh()
  assert.equal(h.state.healthPercent.value, 0); assert.deepEqual(h.state.history.value, [0])
  assert.equal(h.state.points.value.length, 1)
  h.system.ready = ready({ checks: { database: 'ok', redis: 'ok', nats: 'ok' } })
  assert.equal(h.state.healthPercent.value, null, '不把缺少真实 builder 项的三项响应伪装成完整检查')
})

test('网络失败保留真实断档，恢复后不跨断点连线，builder降级不冒充整体失败', async t => {
  let count = 0
  const h = harness(t, async () => { if (++count === 2) throw Error('synthetic offline'); return count === 1 ? ready() : ready({ status: 'degraded', checks: { database: 'ok', redis: 'ok', nats: 'ok', builder: 'unavailable' } }) })
  await h.state.refresh(); await h.state.refresh()
  assert.deepEqual(h.state.history.value, [100, null]); assert.equal(h.state.healthPercent.value, null)
  assert.deepEqual(h.state.cards.value.slice(1).map(card => card.value), ['—', '—', '—'])
  await h.state.refresh()
  assert.deepEqual(h.state.history.value, [100, null, 75])
  assert.deepEqual(h.state.segments.value.map(segment => segment.length), [1, 1])
  assert.notEqual(h.state.cards.value[0].status, 'failed'); assert.equal(h.state.healthStatus.value.label, 'dashboard.degraded')
})

test('不完整响应和未知状态不崩溃或伪造百分比，额外未登记检查不进入统计', t => {
  const h = harness(t, async () => ready())
  for (const response of [{}, { status: 'ok', checks: null }, ready({ checks: { database: 'ok', redis: 'ok', nats: 'ok' } }), ready({ status: 'unknown' })]) {
    h.system.ready = response
    assert.doesNotThrow(() => { void h.state.cards.value; void h.state.healthStatus.value })
    assert.equal(h.state.healthPercent.value, null)
  }
  for (const value of ['ok', 'failed']) {
    h.system.ready = ready({ checks: { ...ready().checks, unregistered: value } })
    assert.equal(h.state.healthyCount.value, 4); assert.equal(h.state.checkCount.value, 4)
    assert.equal(h.state.healthPercent.value, 100)
  }
})

test('最新健康读取获胜，旧成功失败与finally均不覆盖当前值或追加旧采样', async t => {
  for (const oldFails of [false, true]) {
    const first = deferred(), second = deferred(); let count = 0
    const h = harness(t, () => ++count === 1 ? first.promise : second.promise)
    const old = h.state.refresh(), current = h.state.refresh()
    if (oldFails) first.reject(Error('synthetic old failure')); else first.resolve(ready({ status: 'failed', checks: { database: 'failed', redis: 'failed', nats: 'failed', builder: 'unavailable' } }))
    await old
    assert.equal(h.system.loading, true); assert.deepEqual(h.state.history.value, [])
    second.resolve(ready()); await current
    assert.equal(h.system.ready.status, 'ok'); assert.deepEqual(h.state.history.value, [100]); assert.equal(h.system.error, '')
  }
  const first = deferred(), second = deferred(); let count = 0
  const h = harness(t, () => ++count === 1 ? first.promise : second.promise)
  const old = h.state.refresh(), current = h.state.refresh(); second.resolve(ready()); await current
  first.reject(Error('late old failure')); await old
  assert.equal(h.system.ready.status, 'ok'); assert.deepEqual(h.state.history.value, [100]); assert.equal(h.system.error, '')
})

test('离开概览后迟到结果不追加页面历史，也不再启动该页的新读取', async t => {
  const pending = deferred(); let count = 0
  const h = harness(t, () => { count++; return pending.promise })
  const work = h.state.refresh(); h.stop(); pending.resolve(ready()); await work
  assert.deepEqual(h.state.history.value, []); await h.state.refresh(); assert.equal(count, 1)
})

test('概览真实模板逐段绘图，单采样只显示点，不能绕过断档连接整条历史', () => {
  const ast = parse(readFileSync(new URL('../src/views/OverviewView.vue', import.meta.url), 'utf8')).descriptor.template.ast
  const groups = []
  const visit = node => { if (node.type === 1 && node.tag === 'g' && node.props.some(prop => prop.type === 7 && prop.name === 'for' && prop.exp.content === '(segment, index) in segments')) groups.push(node); for (const child of node.children || []) visit(child) }
  visit(ast); assert.equal(groups.length, 1)
  for (const tag of ['polygon', 'polyline']) {
    const element = groups[0].children.find(child => child.tag === tag)
    assert.ok(element.props.some(prop => prop.type === 7 && prop.name === 'if' && prop.exp.content === 'segment.length > 1'))
    assert.ok(element.props.some(prop => prop.type === 7 && prop.name === 'bind' && prop.arg.content === 'points' && prop.exp.content.includes('segment.map(')))
  }
  assert.ok(groups[0].children.find(child => child.tag === 'circle').props.some(prop => prop.type === 7 && prop.name === 'else'))
})
