import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { loadFrontend } from './acceptanceHarness.mjs'

const roles = loadFrontend('../src/utils/kubernetesRoles.ts')
const api = loadFrontend('../src/api/kubernetes.ts', { './resources': { apiErrorMessage: reason => reason.message } })
const workspace = loadFrontend('../src/utils/kubernetesWorkspace.ts')
const presentation = loadFrontend('../src/utils/kubernetesPresentation.ts', { '@/api/kubernetes': api, '@/utils/kubernetesRoles': roles })
const node = cluster => ({ kind: 'Node', metadata: { uid: `${cluster}-node-uid`, name: `${cluster}-node` }, status: { conditions: [{ type: 'Ready', status: 'True' }] } })
const snapshot = cluster => ({ cluster_id: cluster, nodes: [{ node: node(cluster), roles: ['worker'] }], hosts: [], hosts_state: 'ok', metrics_state: 'unavailable', collected_at: new Date().toISOString() })
function deferred() { let resolve, reject; const promise = new Promise((ok, fail) => { resolve = ok; reject = fail }); return { promise, resolve, reject } }
async function settle() { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }

async function harness(path = '/kubernetes?view=nodes&resource=nodes&cluster=cluster-a&node_view=topology') {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/kubernetes', component: {} }] })
  await router.push(path)
  const route = new Proxy({}, { get: (_, key) => router.currentRoute.value[key] })
  const scope = vue.effectScope(), mounts = [], unmounts = [], intervals = new Map(), timeouts = new Map()
  const topologyReads = [], listReads = [], httpReads = [], sessions = [], clock = { now: 1_000_000 }
  const document = { hidden: false, addEventListener() {}, removeEventListener() {} }
  let timerID = 0
  const clusters = ['cluster-a', 'cluster-b'].map(id => ({ id, name: id, default_namespace: 'default', is_active: true, hosts: [] }))
  const client = { get: async (url, options = {}) => {
    httpReads.push({ url, options })
    if (url === '/kubernetes/clusters') return { data: { clusters } }
    if (url.endsWith('/namespaces')) return { data: { namespaces: ['default'] } }
    if (url.endsWith('/overview')) return { data: { counts: {}, nodes: [], usage: {}, phases: {}, collected_at: new Date().toISOString() } }
    if (url.endsWith('/events')) return { data: { items: [] } }
    throw new Error(`Unexpected request: ${url}`)
  } }
  const component = loadFrontend('../src/views/KubernetesView.vue', {
    vue: { ...vue, onMounted: callback => mounts.push(callback), onBeforeUnmount: callback => unmounts.push(callback) },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'vue-router': { useRoute: () => route, useRouter: () => router, onBeforeRouteLeave() {}, onBeforeRouteUpdate() {} },
    '@/api/client': { __esModule: true, default: client },
    '@/api/kubernetes': { ...api,
      listKubeResourceScopes: async (cluster, type, selection, cursor, signal, fresh) => { listReads.push({ cluster, type, selection, cursor, signal, fresh }); return { items: [node(cluster)], cursor: {} } },
      streamKubeEvents: (cluster, namespaces, project, signal, onEvent) => { const pending = deferred(); sessions.push({ cluster, signal, onEvent, ...pending }); return pending.promise },
    },
    '@/api/kubernetesTopology': { getClusterNodeTopology: (cluster, signal) => { const pending = deferred(); topologyReads.push({ cluster, signal, ...pending }); return pending.promise } },
    '@/utils/kubernetesWorkspace': workspace,
    '@/utils/kubernetesPresentation': presentation,
    '@/stores/auth': { useAuthStore: () => ({ user: { id: 'reader', is_superuser: false }, canAny: values => values.some(value => ['cluster.read', 'kubernetes.node.read'].includes(value)) }) },
  }, {
    document, window: { addEventListener() {}, removeEventListener() {} }, localStorage: { getItem: () => 'project-a' },
    setInterval: (callback, delay) => { const id = ++timerID; intervals.set(id, { callback, delay }); return id },
    clearInterval: id => intervals.delete(id),
    setTimeout: (callback, delay) => { const id = ++timerID; timeouts.set(id, { callback, delay }); return id },
    clearTimeout: id => timeouts.delete(id),
    Date: class extends Date { static now() { return clock.now } },
  }).default
  const state = scope.run(() => component.setup({}, { expose() {} }))
  mounts.forEach(callback => callback())
  await settle()
  let stopped = false
  return { state, router, route, document, topologyReads, listReads, httpReads, sessions, intervals, clock,
    async runInterval(delay) { for (const item of [...intervals.values()]) if (item.delay === delay) item.callback(); await settle() },
    stop() { if (stopped) return; stopped = true; unmounts.forEach(callback => callback()); scope.stop() },
  }
}

test('节点拓扑深链接和视图切换由query驱动，列表筛选保留，繁忙时不切换视图', async t => {
  const h = await harness('/kubernetes?view=nodes&resource=nodes&cluster=cluster-a&node_view=topology&q=nas&page=2&limit=20')
  t.after(h.stop)
  assert.equal(h.state.showNodeTopology.value, true)
  assert.equal(h.topologyReads.length, 1)
  assert.equal(h.listReads.length, 0)
  assert.equal(h.httpReads.filter(call => call.url.endsWith('/overview')).length, 0)
  h.topologyReads[0].resolve(snapshot('cluster-a')); await settle()
  h.state.setNodeView('list'); await settle()
  assert.equal(h.route.query.node_view, undefined)
  assert.equal(h.state.showNodeTopology.value, false)
  assert.equal(h.state.query.value, 'nas')
  assert.equal(h.state.currentPage.value, 2)
  assert.equal(h.route.query.limit, '20')
  assert.equal(h.listReads.length, 1)
  h.state.setNodeView('topology'); await settle()
  assert.equal(h.route.query.node_view, 'topology')
  assert.equal(h.state.showNodeTopology.value, true)
  assert.equal(h.topologyReads.length, 2)
  assert.equal(h.route.query.q, 'nas')
  h.state.busy.value = 'saving'
  h.state.setNodeView('list'); await settle()
  assert.equal(h.route.query.node_view, 'topology')
  h.state.busy.value = ''
  await h.router.push({ query: { ...h.route.query, node_view: 'unexpected' } }); await settle()
  assert.equal(h.state.nodeView.value, 'list')
  assert.equal(h.state.showNodeTopology.value, false)
})

test('切换集群立即取消并清空旧拓扑，旧请求成功或失败均不能混入当前集群', async t => {
  const h = await harness(); t.after(h.stop)
  const old = h.topologyReads[0]
  h.state.selectCluster('cluster-b'); await settle()
  assert.equal(old.signal.aborted, true)
  assert.equal(h.state.topology.value, null)
  assert.equal(h.topologyReads.length, 2)
  assert.equal(h.topologyReads[1].cluster, 'cluster-b')
  h.topologyReads[1].resolve(snapshot('cluster-b')); await settle()
  old.resolve(snapshot('cluster-a')); await settle()
  assert.equal(h.state.topology.value.cluster_id, 'cluster-b')
  assert.equal(h.state.topologyLoading.value, false)
  const staleFailure = h.state.loadTopology()
  const failed = h.topologyReads.at(-1)
  h.state.selectCluster('cluster-a'); await settle()
  failed.reject(new Error('old cluster offline')); await staleFailure; await settle()
  assert.equal(failed.signal.aborted, true)
  assert.equal(h.state.topologyError.value, '')
  assert.equal(h.state.topology.value, null)
  h.topologyReads.at(-1).resolve(snapshot('cluster-a')); await settle()
  assert.equal(h.state.topology.value.cluster_id, 'cluster-a')
})

test('从拓扑回到列表立即刷新节点和总览，之后自动刷新与手动刷新正常恢复', async t => {
  const h = await harness(); t.after(h.stop)
  const pending = h.topologyReads[0]
  h.state.setNodeView('list'); await settle()
  assert.equal(pending.signal.aborted, true)
  assert.equal(h.state.topologyLoading.value, false)
  assert.equal(h.state.topology.value, null)
  assert.equal(h.listReads.length, 1)
  assert.equal(h.state.rows.value[0].metadata.name, 'cluster-a-node')
  assert.equal(h.httpReads.filter(call => call.url.endsWith('/overview')).length, 1)
  pending.resolve(snapshot('cluster-a')); await settle()
  assert.equal(h.state.topology.value, null)
  h.clock.now += 5001; await h.runInterval(5000)
  assert.equal(h.listReads.length, 2)
  assert.equal(h.topologyReads.length, 1)
  h.state.refreshAll(); await settle()
  assert.equal(h.listReads.length, 3)
})

test('拓扑刷新去重、失败可重试，头部刷新与拓扑刷新均读取当前集群', async t => {
  const h = await harness(); t.after(h.stop)
  h.topologyReads[0].resolve(snapshot('cluster-a')); await settle()
  const refresh = h.state.loadTopology()
  await h.state.loadTopology()
  assert.equal(h.topologyReads.length, 2)
  assert.equal(h.state.topologyLoading.value, true)
  assert.equal(h.state.topology.value.cluster_id, 'cluster-a')
  h.topologyReads[1].reject(new Error('metrics unavailable')); await refresh
  assert.equal(h.state.topologyLoading.value, false)
  assert.equal(h.state.topologyError.value, 'metrics unavailable')
  h.state.refreshAll(); await settle()
  assert.equal(h.topologyReads.length, 3)
  assert.equal(h.state.topologyError.value, '')
  assert.equal(h.listReads.length, 0)
  assert.equal(h.httpReads.filter(call => call.url.endsWith('/overview')).length, 0)
  h.topologyReads[2].resolve(snapshot('cluster-a')); await settle()
  assert.equal(h.state.topologyLoading.value, false)
  assert.equal(h.state.topologyError.value, '')
})

test('5秒列表校准不读取拓扑，30秒拓扑刷新遵守暂停、后台、详情和编辑状态，卸载清理请求与定时器', async t => {
  const h = await harness(); t.after(h.stop)
  h.topologyReads[0].resolve(snapshot('cluster-a')); await settle()
  const calibrate = async () => { h.clock.now += 5001; await h.runInterval(5000) }
  await calibrate()
  assert.equal(h.topologyReads.length, 1)
  h.state.autoRefresh.value = false; await calibrate(); await h.runInterval(30000)
  assert.equal(h.topologyReads.length, 1)
  h.state.autoRefresh.value = true; h.document.hidden = true; await calibrate(); await h.runInterval(30000)
  assert.equal(h.topologyReads.length, 1)
  h.document.hidden = false; h.state.detailOpen.value = true; await calibrate(); await h.runInterval(30000)
  assert.equal(h.topologyReads.length, 1)
  h.state.detailOpen.value = false; h.state.editorOpen.value = true; await calibrate(); await h.runInterval(30000)
  assert.equal(h.topologyReads.length, 1)
  h.state.editorOpen.value = false; await calibrate()
  assert.equal(h.topologyReads.length, 1)
  assert.equal(h.listReads.length, 0)
  assert.equal(h.httpReads.filter(call => call.url.endsWith('/overview')).length, 0)
  await h.runInterval(30000)
  assert.equal(h.topologyReads.length, 2)
  const pending = h.topologyReads[1], previous = h.state.topology.value
  h.stop()
  assert.equal(pending.signal.aborted, true)
  assert.equal(h.intervals.size, 0)
  pending.resolve(snapshot('cluster-b')); await settle()
  assert.equal(h.state.topology.value, previous)
})
