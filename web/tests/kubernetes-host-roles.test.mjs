import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const roleHelpers = loadFrontend('../src/utils/kubernetesRoles.ts')
const defaultPermissions = ['kubernetes.node.read', 'cluster.read']
function deferred() { let resolve, reject; const promise = new Promise((ok, fail) => { resolve = ok; reject = fail }); return { promise, resolve, reject } }
async function settle() { await Promise.resolve(); await Promise.resolve(); await vue.nextTick() }
function topologyFor(cluster, { state = 'matched', roles = ['control-plane'], hosts = undefined } = {}) {
  return {
    cluster_id: cluster,
    hosts: hosts === undefined ? [{ id: 'host-a', name: 'NAS', address: '10.0.0.1', is_active: true, state, roles, node_uids: ['node-a-uid'] }] : hosts,
    nodes: [{ node: { kind: 'Node', metadata: { uid: 'node-a-uid', name: 'node-a' } }, roles }],
    metrics_state: 'unavailable', collected_at: new Date().toISOString(),
  }
}
function badgeHarness({ cluster = 'cluster-a', host = 'host-a', permissions = defaultPermissions, superuser = false } = {}) {
  const props = vue.reactive({ cluster, host }), reads = [], cleanups = [], scope = vue.effectScope()
  const auth = vue.reactive({
    user: { id: 'user-a', project_id: 'project-a', is_superuser: superuser, permissions: [...permissions] },
    canAny(required) { return this.user.is_superuser || required.some(code => this.user.permissions.includes(code)) },
  })
  const component = loadFrontend('../src/components/KubeHostRoleBadge.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount: callback => cleanups.push(callback) },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/api/kubernetesTopology': { getHostNodeTopology: (cluster, permissionScope) => { const pending = deferred(); reads.push({ cluster, scope: permissionScope, ...pending }); return pending.promise } },
    '@/api/resources': { apiErrorMessage: error => error.message },
    '@/utils/kubernetesRoles': roleHelpers,
  }, { localStorage: { getItem: () => 'project-a' } }).default
  const state = scope.run(() => component.setup(props, { expose() {} }))
  return { state, props, auth, reads, stop() { cleanups.forEach(callback => callback()); scope.stop() } }
}

test('主机角色仅来自已确认匹配的真实节点，双角色及节点名称保持准确', async () => {
  const fixture = badgeHarness()
  fixture.reads[0].resolve(topologyFor('cluster-a', { roles: ['control-plane', 'worker'] }))
  await settle()
  assert.deepEqual([...fixture.state.roles.value], ['control-plane', 'worker'])
  assert.match(fixture.state.description.value, /nodeRoles.controlPlane \/ nodeRoles.worker/)
  assert.match(fixture.state.description.value, /node-a/)
  fixture.stop()
})

test('未匹配、探测失败和没有主机读取权限的响应不会被当成Worker', async () => {
  for (const options of [{ state: 'unmatched', roles: ['worker'] }, { state: 'unavailable', roles: ['worker'] }, { hosts: null }, { hosts: [] }]) {
    const fixture = badgeHarness()
    fixture.reads[0].resolve(topologyFor('cluster-a', options)); await settle()
    assert.deepEqual([...fixture.state.roles.value], [])
    assert.equal(fixture.state.description.value, options.state === 'unmatched' ? 'k8sTopology.hostUnmatched' : 'k8sTopology.rolesUnavailable')
    fixture.stop()
  }
})

test('没有节点查看权限或缺少主机/集群选择时不会发送拓扑请求', () => {
  for (const options of [{ permissions: ['cluster.read'] }, { cluster: '' }, { host: '' }]) {
    const fixture = badgeHarness(options)
    assert.equal(fixture.reads.length, 0)
    assert.equal(fixture.state.loading.value, false)
    assert.equal(fixture.state.data.value, undefined)
    fixture.stop()
  }
})

test('切换集群、主机或卸载后，迟到响应不能覆盖新上下文', async () => {
  const fixture = badgeHarness()
  fixture.props.cluster = 'cluster-b'; await vue.nextTick()
  assert.equal(fixture.reads.length, 2)
  fixture.reads[1].resolve(topologyFor('cluster-b', { roles: ['worker'] })); await settle()
  fixture.reads[0].resolve(topologyFor('cluster-a')); await settle()
  assert.equal(fixture.state.data.value.cluster_id, 'cluster-b')
  assert.deepEqual([...fixture.state.roles.value], ['worker'])
  fixture.props.host = 'host-b'; await vue.nextTick()
  assert.equal(fixture.state.data.value, undefined)
  const late = fixture.reads[2]
  fixture.stop(); late.resolve(topologyFor('cluster-b')); await settle()
  assert.equal(fixture.state.data.value, undefined)
})

test('权限被撤销或集群选择清空会立即结束加载，旧响应不可恢复主机角色', async () => {
  for (const change of ['permission', 'cluster']) {
    const fixture = badgeHarness()
    assert.equal(fixture.state.loading.value, true)
    if (change === 'permission') fixture.auth.user.permissions = ['cluster.read']
    else fixture.props.cluster = ''
    await vue.nextTick()
    assert.equal(fixture.state.loading.value, false)
    assert.equal(fixture.state.error.value, '')
    fixture.reads[0].resolve(topologyFor('cluster-a')); await settle()
    assert.equal(fixture.state.data.value, undefined)
    assert.deepEqual([...fixture.state.roles.value], [])
    fixture.stop()
  }
})

test('API失败清除旧角色并提供失败原因，重试成功后恢复真实角色', async () => {
  const fixture = badgeHarness()
  fixture.reads[0].resolve(topologyFor('cluster-a')); await settle()
  const failed = fixture.state.load()
  fixture.reads[1].reject(new Error('节点信息读取失败')); await failed
  assert.equal(fixture.state.data.value, undefined)
  assert.equal(fixture.state.description.value, '节点信息读取失败')
  const retry = fixture.state.load()
  fixture.reads[2].resolve(topologyFor('cluster-a', { roles: ['worker'] })); await retry
  assert.deepEqual([...fixture.state.roles.value], ['worker'])
  assert.equal(fixture.state.error.value, '')
  fixture.stop()
})

test('权限集和管理员状态改变会使用不同缓存scope，即使节点与主机查看权限未变', async () => {
  const fixture = badgeHarness()
  const initial = fixture.reads[0].scope
  fixture.auth.user.permissions.push('kubernetes.workload.read'); await vue.nextTick()
  assert.equal(fixture.reads.length, 2)
  const withPods = fixture.reads[1].scope
  assert.notEqual(withPods, initial)
  fixture.auth.user.is_superuser = true; await vue.nextTick()
  assert.equal(fixture.reads.length, 3)
  assert.notEqual(fixture.reads[2].scope, withPods)
  fixture.auth.user.project_id = 'project-b'; await vue.nextTick()
  assert.equal(fixture.reads.length, 4)
  assert.notEqual(fixture.reads[3].scope, fixture.reads[2].scope)
  fixture.auth.user.id = 'user-b'; await vue.nextTick()
  assert.equal(fixture.reads.length, 5)
  assert.notEqual(fixture.reads[4].scope, fixture.reads[3].scope)
  fixture.stop()
})

function cacheHarness() {
  const reads = [], clock = { now: 1_000_000 }
  const api = loadFrontend('../src/api/kubernetesTopology.ts', {
    '@/api/client': { __esModule: true, default: { get: (path, options) => { const pending = deferred(); reads.push({ path, options, ...pending }); return pending.promise } } },
  }, { Date: { now: () => clock.now } })
  return { api, reads, clock }
}

test('同集群同scope的并发主机徽标共用一个请求，成功结果只在TTL内复用', async () => {
  const { api, reads, clock } = cacheHarness()
  const first = api.getHostNodeTopology('cluster-a', 'user-a:project-a:node-host-read')
  const concurrent = api.getHostNodeTopology('cluster-a', 'user-a:project-a:node-host-read')
  assert.equal(first, concurrent)
  assert.equal(reads.length, 1)
  const result = topologyFor('cluster-a')
  reads[0].resolve({ data: result })
  assert.equal(await first, result)
  clock.now += 19_999
  assert.equal(await api.getHostNodeTopology('cluster-a', 'user-a:project-a:node-host-read'), result)
  assert.equal(reads.length, 1)
  clock.now += 1
  const refreshed = api.getHostNodeTopology('cluster-a', 'user-a:project-a:node-host-read')
  assert.equal(reads.length, 2)
  reads[1].resolve({ data: result }); await refreshed
})

test('用户、项目、权限scope以及cluster各自隔离，不复用其他上下文的结果', async () => {
  const { api, reads } = cacheHarness()
  const contexts = [['cluster-a', 'user-a:project-a:node-host-read'], ['cluster-a', 'user-b:project-a:node-host-read'], ['cluster-a', 'user-a:project-b:node-host-read'], ['cluster-a', 'user-a:project-a:node-read'], ['cluster-b', 'user-a:project-a:node-host-read']]
  const promises = contexts.map(([cluster, scope]) => api.getHostNodeTopology(cluster, scope))
  assert.equal(reads.length, contexts.length)
  for (let index = 0; index < reads.length; index += 1) reads[index].resolve({ data: topologyFor(contexts[index][0]) })
  await Promise.all(promises)
})

test('请求失败不会留下失败缓存，后续重试会重新请求；独立拓扑读取传递取消信号', async () => {
  const { api, reads } = cacheHarness()
  const first = api.getHostNodeTopology('cluster-a', 'reader')
  reads[0].reject(new Error('temporary failure'))
  await assert.rejects(first, /temporary failure/)
  const retry = api.getHostNodeTopology('cluster-a', 'reader')
  assert.equal(reads.length, 2)
  reads[1].resolve({ data: topologyFor('cluster-a') }); await retry
  const abort = new AbortController()
  const direct = api.getClusterNodeTopology('team/cluster', abort.signal)
  assert.equal(reads[2].path, '/kubernetes/clusters/team%2Fcluster/topology')
  assert.equal(reads[2].options.signal, abort.signal)
  assert.equal(reads[2].options.timeout, 35_000)
  reads[2].resolve({ data: topologyFor('team/cluster') }); await direct
})
