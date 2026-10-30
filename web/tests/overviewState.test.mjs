import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { parse } from '@vue/compiler-sfc'
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
