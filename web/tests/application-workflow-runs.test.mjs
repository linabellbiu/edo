import assert from 'node:assert/strict'
import { test } from 'node:test'
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
  const resolve = (offset, active = [], history = []) => {
    requests[offset].resolve({ data: { pipeline_runs: active } }); requests[offset + 1].resolve({ data: { pipeline_runs: history } })
  }
  return { state, props, auth, requests, resolve, doc, listeners, timers, get subscriber() { return subscriber }, get unsubscribed() { return unsubscribed }, stop() { unmounts.forEach(fn => fn()); scope.stop() } }
}

test('展开按准确应用、流水线分别读取当前和最近50条历史，不依赖全局窗口', async () => {
  const h = harness()
  assert.equal(h.requests.length, 2)
  assert.deepEqual(h.requests[0].options.params, { application_id: 'app', workflow_id: 'flow', order: 'desc', status_group: 'active', limit: 200 })
  assert.equal(h.requests[1].options.params.status_group, 'finished')
  assert.equal(h.requests[1].options.params.limit, 50)
  const active = run('current', { status: 'running', created_at: '2026-11-01T00:00:00Z' })
  h.resolve(0, [active], [...Array.from({ length: 55 }, (_, i) => run(`history-${i}`)), run('other', { workflow_id: 'another' })])
  await flush()
  assert.equal(h.state.rows.value.length, 51)
  assert.equal(h.state.rows.value[0].id, 'current')
  assert.equal(h.state.history.value.length, 50)
  assert.equal(h.state.loading.value, false)
  h.stop()
})

test('切换流水线、项目和权限后取消旧读取，迟到响应不能串入新范围', async () => {
  const h = harness()
  h.props.workflowId = 'other-flow'; h.props.scopeKey = 'project-b'
  await vue.nextTick()
  assert.equal(h.requests[0].options.signal.aborted, true)
  h.resolve(0, [], [run('old')])
  h.resolve(2, [], [run('new', { workflow_id: 'other-flow' }), run('wrong-app', { application_id: 'another', workflow_id: 'other-flow' })])
  await flush()
  assert.deepEqual(h.state.rows.value.map(item => item.id), ['new'])
  const pending = h.state.load()
  h.auth.readable = false
  await vue.nextTick()
  assert.equal(h.requests[4].options.signal.aborted, true)
  h.resolve(4, [], [run('late', { workflow_id: 'other-flow' })])
  await pending
  assert.deepEqual(h.state.rows.value, [])
  assert.equal(h.state.allowed.value, false)
  assert.ok(h.unsubscribed > 0)
  h.stop()
})

test('只读展开不提交执行，失败保留已有记录并显示错误，重试可恢复', async () => {
  const h = harness()
  h.resolve(0, [], [run('saved')]); await flush()
  const pending = h.state.load()
  h.requests[2].reject(new Error('读取执行记录失败'))
  h.requests[3].resolve({ data: { pipeline_runs: [] } })
  await pending
  assert.deepEqual(h.state.rows.value.map(item => item.id), ['saved'])
  assert.equal(h.state.error.value, '读取执行记录失败')
  const retry = h.state.load(); h.resolve(4, [], [run('saved')]); await retry
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
  assert.equal(h.requests.length, 4)
  h.doc.hidden = true; h.listeners.get('visibilitychange')()
  assert.equal(h.requests[2].options.signal.aborted, true)
  h.doc.hidden = false; h.listeners.get('visibilitychange')()
  assert.equal(h.requests.length, 6)
  h.stop()
  assert.equal(h.requests[4].options.signal.aborted, true)
  assert.equal(h.listeners.size, 0)
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
