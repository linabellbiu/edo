import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { createRouter, createMemoryHistory } from 'vue-router'
import { loadFrontend } from './acceptanceHarness.mjs'

const api = loadFrontend('../src/api/kubernetes.ts')
const presentation = loadFrontend('../src/utils/kubernetesPresentation.ts', {
  '@/api/kubernetes': api, '@/utils/kubernetesRoles': loadFrontend('../src/utils/kubernetesRoles.ts'),
})
const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const catalog = [
  { id: 'a', name: '集群 A', default_namespace: 'default', is_active: true },
  { id: 'b', name: '集群 B', default_namespace: 'system', is_active: true },
]
function storage() {
  const values = new Map()
  return { values, getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value) }
}
async function harness({ saved = storage(), path = '/kubernetes?view=workloads', user = 'reader', project = 'project-a', clusters = catalog, namespaceRead } = {}) {
  saved.setItem('edo.project', project)
  const helpers = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: saved })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/kubernetes', component: {} }] })
  await router.push(path)
  const route = new Proxy({}, { get: (_, key) => router.currentRoute.value[key] })
  const auth = vue.reactive({ user: { id: user, project_id: project }, canAny: () => true })
  const mounts = [], unmounts = [], listeners = new Map(), reads = [], listReads = [], scope = vue.effectScope()
  const component = loadFrontend('../src/views/KubernetesView.vue', {
    vue: { ...vue, onMounted: fn => mounts.push(fn), onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-router': { useRouter: () => router, useRoute: () => route, onBeforeRouteLeave() {}, onBeforeRouteUpdate() {} },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/kubernetesWorkspace': helpers,
    '@/utils/kubernetesPresentation': presentation,
    '@/api/kubernetes': { ...api,
      listKubeResourceScopes: async (cluster, type, selection) => { listReads.push({ cluster, resource: type.resource, selection }); return { items: [], cursor: {} } },
      streamKubeEvents: () => new Promise(() => {}),
    },
    '@/api/client': { __esModule: true, default: { get: async (path, options) => {
      reads.push({ path, options })
      if (path === '/kubernetes/clusters') return { data: { clusters: structuredClone(clusters) } }
      if (path.endsWith('/namespaces')) return namespaceRead?.(path, options) ?? { data: { namespaces: ['default', 'prod', 'test', 'system'] } }
      return { data: { counts: {}, usage: {}, nodes: [], phases: {} } }
    } } },
  }, {
    localStorage: saved, document: { hidden: false, addEventListener() {}, removeEventListener() {} },
    window: { addEventListener: (event, fn) => listeners.set(event, fn), removeEventListener: event => listeners.delete(event) },
    setInterval: () => 1, clearInterval() {}, setTimeout: () => 2, clearTimeout() {},
  }).default
  const state = scope.run(() => component.setup({}, { expose() {} }))
  mounts.forEach(fn => fn()); await settle()
  return { state, saved, helpers, router, route, auth, listeners, reads, listReads, stop() { unmounts.forEach(fn => fn()); scope.stop() } }
}

test('重新挂载恢复最后集群、各集群范围、自动刷新和每页条数，全部取消也可恢复', async t => {
  const first = await harness(); t.after(first.stop)
  first.state.selectNamespaces(['prod', 'test']); await settle()
  first.state.selectCluster('b', true); await settle()
  first.state.selectNamespaces([])
  first.state.autoRefresh.value = false; first.state.pageSize.value = 50
  await settle()
  const second = await harness({ saved: first.saved }); t.after(second.stop)
  assert.equal(second.state.cluster.value, 'b')
  assert.deepEqual(second.state.selectedNamespaces.value, [])
  assert.equal(second.state.autoRefresh.value, false)
  assert.equal(second.state.pageSize.value, 50)
  assert.equal(second.listReads.length, 0)
  second.state.selectCluster('a'); await settle()
  assert.deepEqual(second.state.selectedNamespaces.value, ['prod', 'test'])
  second.state.selectCluster('b'); await settle()
  assert.deepEqual(second.state.selectedNamespaces.value, [])
})

test('URL明确指定范围与每页条数时覆盖本地，前进后退恢复各自历史范围', async t => {
  const saved = storage(), helpers = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: saved })
  helpers.writeKubeWorkspacePreferences('reader', 'project-a', { cluster: 'b', scopes: { a: ['test'], b: [] }, pageSize: 50 })
  const h = await harness({ saved, path: '/kubernetes?view=workloads&cluster=a&namespace=prod&limit=20' }); t.after(h.stop)
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod'])
  assert.equal(h.state.pageSize.value, 20)
  await h.router.push('/kubernetes?view=network&cluster=b&namespace=*&limit=10'); await settle()
  assert.deepEqual(h.state.selectedNamespaces.value, ['*'])
  assert.equal(h.state.pageSize.value, 10)
  h.router.back(); await settle()
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod'])
  assert.equal(h.state.pageSize.value, 20)
  h.router.forward(); await settle()
  assert.equal(h.state.cluster.value, 'b')
  assert.deepEqual(h.state.selectedNamespaces.value, ['*'])
})

test('集群级页面沿用保存的命名空间范围，返回资源列表不覆盖为全选', async t => {
  const saved = storage(), helpers = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: saved })
  helpers.writeKubeWorkspacePreferences('reader', 'project-a', { cluster: 'b', scopes: { b: ['prod', 'test'] } })
  const h = await harness({ saved, path: '/kubernetes?view=nodes' }); t.after(h.stop)
  assert.equal(h.state.cluster.value, 'b')
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod', 'test'])
  assert.equal(h.reads.some(item => item.path.endsWith('/namespaces')), false)
  await h.router.push({ query: { ...h.route.query, view: 'workloads' } }); await settle()
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod', 'test'])
  assert.deepEqual(h.listReads.at(-1).selection, ['prod', 'test'])
})

test('展开其他集群只读目录，不更改URL、本地勾选、资源列表或当前集群；点击框选后才切换', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.selectNamespaces(['prod', 'test']); await settle()
  const beforeURL = h.route.fullPath, beforeSaved = h.helpers.readKubeWorkspacePreferences('reader', 'project-a')
  const beforeReads = h.listReads.length
  await h.state.browseNamespaces('b'); await settle()
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod', 'test'])
  assert.deepEqual(h.state.namespaceCatalogs.value.b.names, ['default', 'prod', 'test', 'system'])
  assert.equal(h.route.fullPath, beforeURL)
  assert.equal(h.listReads.length, beforeReads)
  assert.deepEqual(h.helpers.readKubeWorkspacePreferences('reader', 'project-a'), beforeSaved)
  h.state.selectNamespaces(['system'], 'b'); await settle()
  assert.equal(h.state.cluster.value, 'b')
  assert.deepEqual(h.state.selectedNamespaces.value, ['system'])
  assert.deepEqual(h.helpers.readKubeWorkspacePreferences('reader', 'project-a').scopes.a, ['prod', 'test'])
})

test('目录收起或权限变化后丢弃迟到的浏览结果，不污染已选范围', async t => {
  const pending = []
  const h = await harness({ namespaceRead: (path, options) => path.includes('/b/') ? new Promise(resolve => pending.push({ options, resolve })) : undefined }); t.after(h.stop)
  const first = h.state.browseNamespaces('b')
  assert.equal(pending[0].options.signal.aborted, false)
  await h.state.browseNamespaces('')
  assert.equal(pending[0].options.signal.aborted, true)
  pending[0].resolve({ data: { namespaces: ['late'] } }); await first
  assert.deepEqual(h.state.namespaceCatalogs.value.b.names, [])
  const second = h.state.browseNamespaces('b')
  h.auth.user.permissions = ['cluster.read']; await settle()
  assert.equal(pending[1].options.signal.aborted, true)
  pending[1].resolve({ data: { namespaces: ['private'] } }); await second
  assert.deepEqual(h.state.namespaceCatalogs.value, {})
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['default'])
})

test('用户与项目隔离；同页切换身份不把旧范围写入新身份或继续复用旧目录', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.selectCluster('b', true); h.state.autoRefresh.value = false; await settle()
  h.helpers.writeKubeWorkspacePreferences('other', 'project-b', { cluster: 'a', scopes: { a: ['test'] }, autoRefresh: true })
  h.saved.setItem('edo.project', 'project-b')
  h.auth.user = { id: 'other', project_id: 'project-b' }; await settle()
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['test'])
  assert.equal(h.state.autoRefresh.value, true)
  const original = h.helpers.readKubeWorkspacePreferences('reader', 'project-a')
  assert.equal(original.cluster, 'b')
  assert.deepEqual(original.scopes.b, ['*'])
  assert.equal(original.autoRefresh, false)
  assert.deepEqual(h.helpers.readKubeWorkspacePreferences('new-reader', 'project-b'), {})
})

test('删除、停用或不在当前可见目录的保存集群不自动成为执行范围', async t => {
  const saved = storage(), helpers = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: saved })
  helpers.writeKubeWorkspacePreferences('reader', 'project-a', { cluster: 'b', scopes: { b: ['prod'] } })
  const h = await harness({ saved, clusters: [catalog[0], { ...catalog[1], is_active: false }] }); t.after(h.stop)
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['default'])
  assert.ok(h.listReads.every(read => read.cluster === 'a'))
})

test('浏览器存储异常或损坏不阻止页面选择；仅保存允许的偏好字段', async t => {
  const saved = storage(), helpers = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: saved })
  const key = helpers.kubeWorkspacePreferenceKey('reader', 'project-a')
  saved.setItem(key, '{broken')
  assert.deepEqual(helpers.readKubeWorkspacePreferences('reader', 'project-a'), {})
  saved.setItem(key, JSON.stringify({ cluster: 'a', scopes: { a: [], b: ['*', 'prod'], c: ['bad/ns'], d: 'prod' }, autoRefresh: 'false', pageSize: 99, token: 'secret' }))
  assert.deepEqual(helpers.readKubeWorkspacePreferences('reader', 'project-a'), { cluster: 'a', scopes: { a: [], b: ['*'] } })
  saved.setItem = () => { throw new Error('QuotaExceededError') }
  const h = await harness({ saved: { ...saved, setItem(key, value) { if (key === 'edo.project') saved.values.set(key, value); else saved.setItem(key, value) } } }); t.after(h.stop)
  h.state.selectNamespaces(['prod']); await settle()
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod'])
  const denied = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: { getItem() { throw new Error('SecurityError') }, setItem() { throw new Error('SecurityError') } } })
  assert.deepEqual(denied.readKubeWorkspacePreferences('reader', 'project-a'), {})
  assert.doesNotThrow(() => denied.writeKubeWorkspacePreferences('reader', 'project-a', { scopes: { a: [] } }))
})
