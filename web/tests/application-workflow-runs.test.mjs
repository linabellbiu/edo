import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { parse } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const flush = () => new Promise(resolve => setImmediate(resolve))
const run = (id, changes = {}) => ({ id, application_id: 'app', workflow_id: 'flow', status: 'succeeded', ref: 'refs/tags/v1', trigger: 'poll_tag', created_at: '2026-11-13T01:00:00Z', ...changes })
function harness(readable = true) {
  const requests = [], mounts = [], unmounts = [], listeners = new Map(), timers = new Map()
  const doc = { hidden: false, addEventListener: (name, fn) => listeners.set(name, fn), removeEventListener: name => listeners.delete(name) }
  const props = vue.reactive({ applicationId: 'app', workflowId: 'flow', scopeKey: 'project-a' })
  const auth = vue.reactive({ readable, authorizationState: 'ready', canAny() { return this.readable } })
  let subscriber, unsubscribed = 0, id = 0
  const component = loadFrontend('../src/components/ApplicationWorkflowRuns.vue', {
    vue: { ...vue, onMounted: fn => mounts.push(fn), onBeforeUnmount: fn => unmounts.push(fn) },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/api/client': { __esModule: true, default: { get: (path, options) => new Promise((resolve, reject) => requests.push({ path, options, resolve, reject })) } },
    '@/api/resources': { apiErrorMessage: error => error.message },
    '@/utils/deliveryEvents': { subscribeDeliveryEvents: value => { subscriber = value; return () => { unsubscribed++ } } },
  }, { document: doc, setTimeout: fn => { timers.set(++id, fn); return id }, clearTimeout: id => timers.delete(id), setInterval: () => ++id, clearInterval() {} }).default
  const scope = vue.effectScope()
  const state = scope.run(() => component.setup(props, { expose() {}, emit() {} }))
  mounts.forEach(fn => fn())
  const resolve = (offset, records = []) => requests[offset].resolve({ data: { pipeline_runs: records } })
  return { state, props, auth, requests, resolve, doc, listeners, timers, get subscriber() { return subscriber }, get unsubscribed() { return unsubscribed }, stop() { unmounts.forEach(fn => fn()); scope.stop() } }
}

test('当前默认直接显示，仅展开历史时才读取最近50条，收起不隐藏当前', async () => {
  const h = harness()
  assert.equal(h.state.historyExpanded.value, false)
  assert.equal(h.requests.length, 1)
  assert.deepEqual(h.requests[0].options.params, { application_id: 'app', workflow_id: 'flow', order: 'desc', status_group: 'active', limit: 200 })
  const active = run('current', { status: 'running', created_at: '2026-11-01T00:00:00Z' })
  h.resolve(0, [active, active, run('wrong', { workflow_id: 'another', status: 'running' })]); await flush()
  assert.deepEqual(h.state.rows.value.map(item => item.id), ['current'])
  h.state.historyExpanded.value = true; await vue.nextTick()
  assert.equal(h.requests.length, 3)
  assert.equal(h.requests[2].options.params.status_group, 'finished')
  assert.equal(h.requests[2].options.params.limit, 50)
  h.resolve(1, [active]); h.resolve(2, [...Array.from({ length: 55 }, (_, i) => run(`history-${i}`)), run('other', { workflow_id: 'another' })]); await flush()
  assert.equal(h.state.rows.value.length, 51)
  assert.equal(h.state.rows.value[0].id, 'current')
  assert.equal(h.state.history.value.length, 50)
  assert.equal(h.state.loading.value, false)
  h.state.historyExpanded.value = false; await vue.nextTick()
  assert.deepEqual(h.state.rows.value.map(item => item.id), ['current'])
  assert.equal(h.state.history.value.length, 0)
  assert.equal(h.requests.length, 4)
  assert.equal(h.requests[3].options.params.status_group, 'active')
  h.resolve(3, [active]); await flush()
  h.stop()
})

test('历史收起时新执行自动出现，完成后移入历史且不重复', async () => {
  const h = harness()
  h.resolve(0); await flush()
  h.subscriber.event({ type: 'changed', resources: ['pipeline_runs'] })
  h.timers.values().next().value()
  h.resolve(1, [run('new', { status: 'running' })]); await flush()
  assert.deepEqual(h.state.rows.value.map(item => item.id), ['new'])
  assert.equal(h.state.historyExpanded.value, false)
  h.state.historyExpanded.value = true; await vue.nextTick()
  h.resolve(3, [run('new')]); await flush()
  assert.deepEqual(h.state.rows.value.map(item => item.id), ['new'], '历史先返回也不重复显示同一执行')
  h.resolve(2); await flush()
  assert.equal(h.state.active.value.length, 0)
  assert.deepEqual(h.state.rows.value.map(item => item.id), ['new'])
  h.stop()
})

test('收起历史取消旧读取，迟到历史不能隐藏或改写当前进度', async () => {
  const h = harness(), active = run('current', { status: 'running' })
  h.resolve(0, [active]); await flush()
  h.state.historyExpanded.value = true; await vue.nextTick()
  h.state.historyExpanded.value = false; await vue.nextTick()
  assert.equal(h.requests[2].options.signal.aborted, true)
  h.resolve(1); h.resolve(2, [run('current')]); h.resolve(3, [active]); await flush()
  assert.deepEqual(h.state.rows.value.map(item => item.id), ['current'])
  assert.deepEqual(h.state.history.value, [])
  assert.equal(h.unsubscribed, 0, '折叠历史保留当前订阅')
  h.stop()
})

test('切换流水线、项目和权限后取消旧读取，迟到响应不能串入新范围', async () => {
  const h = harness()
  h.props.workflowId = 'other-flow'; h.props.scopeKey = 'project-b'
  await vue.nextTick()
  assert.equal(h.requests[0].options.signal.aborted, true)
  h.resolve(0, [run('old', { status: 'running' })])
  h.resolve(1, [run('new', { workflow_id: 'other-flow', status: 'running' }), run('wrong-app', { application_id: 'another', workflow_id: 'other-flow', status: 'running' })])
  await flush()
  assert.deepEqual(h.state.rows.value.map(item => item.id), ['new'])
  const pending = h.state.load()
  h.auth.readable = false
  await vue.nextTick()
  assert.equal(h.requests[2].options.signal.aborted, true)
  h.resolve(2, [run('late', { workflow_id: 'other-flow', status: 'running' })])
  await pending
  assert.deepEqual(h.state.rows.value, [])
  assert.equal(h.state.allowed.value, false)
  assert.ok(h.unsubscribed > 0)
  h.stop()
})

test('历史失败不阻断当前同步，当前读取失败保留数据并明确提示', async () => {
  const h = harness()
  h.resolve(0, [run('saved', { status: 'running' })]); await flush()
  h.state.historyExpanded.value = true; await vue.nextTick()
  h.requests[2].reject(new Error('读取历史执行失败'))
  h.resolve(1, [run('updated', { status: 'running' })]); await flush()
  assert.deepEqual(h.state.rows.value.map(item => item.id), ['updated'])
  assert.equal(h.state.historyError.value, '读取历史执行失败')
  assert.equal(h.state.error.value, '')
  const pending = h.state.load()
  h.requests[3].reject(new Error('读取执行记录失败')); h.resolve(4, [run('history')])
  await pending
  assert.deepEqual(h.state.rows.value.map(item => item.id), ['updated', 'history'])
  assert.equal(h.state.error.value, '读取执行记录失败')
  assert.equal(h.state.historyError.value, '')
  const retry = h.state.load(); h.resolve(5); h.resolve(6, [run('history')]); await retry
  assert.equal(h.state.error.value, '')
  assert.ok(h.requests.every(request => request.path === '/pipeline-runs'))
  h.stop()
})

test('运行事件合并刷新，页面隐藏和关闭时取消请求，无权限不读取', async () => {
  const denied = harness(false)
  assert.equal(denied.requests.length, 0); assert.equal(denied.subscriber, undefined); denied.stop()
  const h = harness()
  h.resolve(0); await flush()
  h.subscriber.event({ type: 'changed', resources: ['application_sync'] })
  assert.equal(h.timers.size, 0)
  for (let i = 0; i < 3; i++) h.subscriber.event({ type: 'changed', resources: ['pipeline_runs'] })
  assert.equal(h.timers.size, 1)
  h.timers.values().next().value()
  assert.equal(h.requests.length, 2)
  h.doc.hidden = true; h.listeners.get('visibilitychange')()
  assert.equal(h.requests[1].options.signal.aborted, true)
  h.doc.hidden = false; h.listeners.get('visibilitychange')()
  assert.equal(h.requests.length, 3)
  h.stop()
  assert.equal(h.requests[2].options.signal.aborted, true)
  assert.equal(h.listeners.size, 0)
})

test('应用关联流水线直接挂载当前执行区域，不以展开按钮控制可见性', () => {
  const source = readFileSync(new URL('../src/views/DevOpsView.vue', import.meta.url), 'utf8'), elements = []
  function walk(node) { if (node.type === 1) elements.push(node); for (const child of node.children || []) walk(child) }
  walk(parse(source).descriptor.template.ast)
  const table = elements.find(node => node.tag === 'a-table' && node.props.some(prop => prop.name === 'class' && prop.value?.content === 'apps-workflow-table'))
  const prop = name => table.props.find(prop => prop.type === 7 && prop.arg?.content === name)?.exp?.content
  assert.equal(prop('show-expand-column'), 'false')
  assert.equal(prop('expanded-row-keys'), 'card.workflowSummaries.map(item=>item.workflow.id)')
  const expanded = table.children.find(node => node.type === 1 && node.props.some(prop => prop.name === 'slot' && prop.arg?.content === 'expandedRowRender'))
  assert.equal(expanded.children[0].tag, 'ApplicationWorkflowRuns')
  assert.equal(expanded.children[0].props.some(prop => prop.name === 'if'), false)
})

test('进度只展示运行快照的阶段，保留未知与失败，不用任务名或总状态补造阶段成功', () => {
  const component = loadFrontend('../src/components/PipelineRunStages.vue', { vue }).default
  const props = vue.reactive({ status: 'running', stages: [
    { id: 'build', name: '构建', tasks: [{ id: 'a', name: 'api' }, { id: 'b', name: 'web' }], timing: { state: 'running', running: true, duration_ms: 1000 } },
    { id: 'deploy', name: '发布', tasks: [{ id: 'c', name: '主机' }] },
  ] })
  const state = component.setup(props, { expose() {} })
  assert.equal(state.rows.value.length, 2)
  assert.equal(state.rows.value[0].live, true)
  assert.equal(state.rows.value[1].label, '状态未知')
  props.status = 'failed'
  assert.equal(state.rows.value[0].live, false)
  assert.equal(state.rows.value[0].label, '状态未知')
  props.stages[0].timing = { state: 'failed', running: false, duration_ms: 1500 }
  assert.equal(state.rows.value[0].label, '失败')
})
