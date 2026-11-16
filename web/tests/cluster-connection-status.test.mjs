import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const settle = () => new Promise(resolve => setImmediate(resolve))
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const cluster = (id, is_active = true) => ({ id, name: id, is_active })
const status = (cluster_id, state, message = '', expires_at = '2030-01-01T00:00:00Z') => ({ cluster_id, status: state, reason: '', message, expires_at })
function harness(t, read) {
  const storage = { project: 'project-a' }, auth = vue.reactive({ authorizationReady: true, user: { id: 'operator', project_id: 'project-a', permissions: ['cluster.read'] }, canAny: codes => codes.includes('cluster.read') })
  const calls = [], timers = new Map(), callbacks = new Map()
  let timerID = 0
  const modules = { vue, '@/stores/auth': { useAuthStore: () => auth }, '@/api/resources': { apiErrorMessage: error => error.message }, '@/api/client': { __esModule: true, default: { get: (path, options) => { calls.push({ path, options }); return read(calls.length, options) } } } }
  const globals = { localStorage: { getItem: () => storage.project }, window: { addEventListener: (event, callback) => callbacks.set(event, callback), removeEventListener: event => callbacks.delete(event) }, setTimeout: (callback, delay) => { timers.set(++timerID, { callback, delay }); return timerID }, clearTimeout: id => timers.delete(id) }
  const api = loadFrontend('../src/utils/clusterConnection.ts', modules, globals)
  const clusters = vue.ref([cluster('fast'), cluster('slow')]), enabled = vue.ref(true), scope = vue.effectScope()
  const state = scope.run(() => api.useClusterConnections(() => clusters.value, () => enabled.value))
  t.after(() => scope.stop())
  return { state, api, auth, clusters, enabled, storage, calls, timers, callbacks }
}

test('慢集群检测不会阻塞快集群回填；使用批量接口并独立保留状态', async t => {
  const h = harness(t, call => Promise.resolve({ data: { statuses: call === 1 ? [status('fast', 'connected', '连接正常'), status('slow', 'checking', '正在检测集群连接')] : [status('fast', 'connected', '连接正常'), status('slow', 'unreachable', 'EDO 无法连接集群，请检查网络')] } }))
  await settle()
  assert.equal(h.calls[0].path, '/kubernetes/clusters/connection-statuses')
  assert.equal(h.calls[0].options.params.ids, 'fast,slow')
  assert.equal(h.calls[0].options.params.refresh, undefined)
  assert.equal(h.state.statuses.value.fast.status, 'connected')
  assert.equal(h.state.statuses.value.slow.status, 'checking')
  assert.equal(h.state.pending.value, false)
  assert.equal([...h.timers.values()][0].delay, 2000)
  await h.state.refresh()
  assert.equal(h.calls[1].options.params.refresh, true)
  assert.equal(h.state.statuses.value.fast.status, 'connected')
  assert.equal(h.state.statuses.value.slow.status, 'unreachable')
})

test('过期结果刷新时不冒充已连接，读取失败显示未知并可单独重试', async t => {
  const next = deferred()
  const h = harness(t, call => call === 1 ? Promise.resolve({ data: { statuses: [status('fast', 'connected', '连接正常', '2000-01-01T00:00:00Z')] } }) : next.promise)
  await settle()
  const pending = h.state.refresh()
  assert.equal(h.state.statuses.value.fast.status, 'checking')
  next.reject(new Error('暂时无法读取连接状态'))
  await pending
  assert.equal(h.state.statuses.value.fast.status, 'unknown')
  assert.equal(h.state.error.value, '暂时无法读取连接状态')
  assert.equal(h.state.pending.value, false)
})

test('切换项目或撤销授权取消旧请求，迟到结果不进入新范围', async t => {
  const old = deferred(), fresh = deferred()
  const h = harness(t, call => call === 1 ? old.promise : fresh.promise)
  h.storage.project = 'project-b'
  h.auth.user.project_id = 'project-b'
  assert.equal(h.calls[0].options.signal.aborted, true)
  fresh.resolve({ data: { statuses: [status('fast', 'restricted', '没有集群访问权限')] } })
  await settle()
  old.resolve({ data: { statuses: [status('fast', 'connected', '旧项目成功')] } })
  await settle()
  assert.equal(h.state.statuses.value.fast.status, 'restricted')
  h.auth.authorizationReady = false
  assert.deepEqual(h.state.statuses.value, {})
  const count = h.calls.length
  await h.state.refresh()
  assert.equal(h.calls.length, count)
})

test('集群选择保留失联值且允许保存配置，失联与权限受限均有明确标签', t => {
  const statuses = vue.ref({ offline: status('offline', 'unreachable', 'EDO 无法连接集群，请检查网络'), restricted: status('restricted', 'restricted', '没有集群访问权限') })
  const helpers = loadFrontend('../src/utils/clusterConnection.ts', { vue })
  const props = vue.reactive({ clusters: [cluster('offline'), cluster('restricted'), cluster('disabled', false)], value: 'offline' })
  const h = componentHarness('../src/components/KubernetesClusterSelect.vue', { props, modules: { '@/utils/clusterConnection': { ...helpers, useClusterConnections: () => ({ statuses, error: vue.ref(''), pending: vue.ref(false), refresh() {} }) } } })
  t.after(h.stop)
  assert.equal(h.state.options.value.find(option => option.value === 'offline').label, 'offline · 未连接')
  assert.equal(h.state.options.value.find(option => option.value === 'offline').disabled, false)
  assert.equal(h.state.options.value.find(option => option.value === 'restricted').label, 'restricted · 访问受限')
  assert.equal(h.state.selectedConnection.value.message, 'EDO 无法连接集群，请检查网络')
  assert.deepEqual(h.events, [], '状态不能修改草稿或上报阻止保存的字段异常')
  props.value = 'disabled'
  assert.equal(h.state.options.value.find(option => option.value === 'disabled').disabled, true)
  props.value = 'deleted'
  assert.equal(h.state.options.value.at(-1).value, 'deleted')
  assert.equal(h.state.options.value.at(-1).disabled, true)
  assert.equal(helpers.clusterConnectionLabel(cluster('unknown')), '未检测')
  assert.equal(helpers.clusterConnectionLabel(cluster('disabled', false), status('disabled', 'connected')), '已停用')
})

test('大目录只探测搜索前100项和当前目标，搜索与异步状态不改变已选目标', t => {
  const helpers = loadFrontend('../src/utils/clusterConnection.ts', { vue })
  const props = vue.reactive({ clusters: Array.from({ length: 600 }, (_, index) => cluster(`cluster-${index}`)), value: 'cluster-599' })
  let requested
  const h = componentHarness('../src/components/KubernetesClusterSelect.vue', { props, modules: { '@/utils/clusterConnection': { ...helpers, useClusterConnections: getter => { requested = getter; return { statuses: vue.ref({}), error: vue.ref(''), pending: vue.ref(false), refresh() {} } } } } })
  t.after(h.stop)
  assert.equal(requested().length, 101)
  assert.equal(requested().at(-1).id, 'cluster-599')
  h.state.search.value = 'cluster-55'
  assert.ok(requested().some(cluster => cluster.id === 'cluster-550'))
  assert.ok(requested().some(cluster => cluster.id === 'cluster-599'))
  assert.equal(props.value, 'cluster-599')
  assert.deepEqual(h.events, [])
})
