import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

// 集群列表：删除默认命名空间列，操作列提供“查看集群”进入该集群的 Kubernetes 工作区。
const inventorySource = readFileSync(new URL('../src/components/ClusterInventory.vue', import.meta.url), 'utf8')

function load(path, modules = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const code = path.endsWith('.vue') ? compileScript(parse(source).descriptor, { id: 'cluster-list-actions' }).content : source
  const output = ts.transpileModule(code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output)(name => modules[name] ?? {}, module, module.exports)
  return module.exports
}
const relations = load('../src/utils/infrastructureRelations.ts')
const time = load('../src/utils/time.ts')
const clusters = [
  { id: 'cluster-1', name: 'ci内网集群', api_server: 'https://10.249.0.102:16443', default_namespace: 'default', is_active: true, updated_at: '2026-11-17T09:22:58Z', hosts: [] },
  { id: 'cluster-2', name: '已停用集群', api_server: 'https://10.249.0.161:16443', default_namespace: 'team', is_active: false, updated_at: '2026-11-17T09:20:17Z', hosts: [] },
]
function setup(initialPermissions = ['cluster.read', 'kubernetes.node.read', 'cluster.update']) {
  const routes = [], cleanups = []
  const permissions = vue.reactive(new Set(initialPermissions))
  const auth = { canAny: values => values.some(value => permissions.has(value)) }
  const mockedVue = { ...vue, onBeforeUnmount: callback => cleanups.push(callback), onMounted() {} }
  const route = vue.reactive({ path: '/hosts', query: { view: 'clusters' } })
  const list = load('../src/utils/resourceList.ts', { vue: mockedVue, 'vue-i18n': { useI18n: () => ({ t: key => key }) } })
  const modules = {
    vue: mockedVue,
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'vue-router': { useRoute: () => route, useRouter: () => ({ push: value => routes.push(value), replace: value => routes.push(value) }) },
    'ant-design-vue': { message: { success() {}, error() {}, info() {} } },
    '@/api/client': { __esModule: true, default: { get: async () => ({ data: {} }), post: async () => ({ data: {} }), patch: async () => ({ data: {} }) } },
    '@/api/resources': { apiErrorMessage: () => '连接失败' },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/infrastructureRelations': relations,
    '@/utils/resourceList': list,
    '@/utils/time': time,
  }
  const component = load('../src/components/ClusterInventory.vue', modules).default
  const props = vue.reactive({ clusters, hosts: [], loading: false })
  const scope = vue.effectScope()
  const state = scope.run(() => component.setup(props, { expose() {}, emit() {} }))
  return { state, props, routes, permissions, stop() {
    const previousDocument = globalThis.document, previousWindow = globalThis.window
    globalThis.document ??= { removeEventListener() {} }
    globalThis.window ??= { removeEventListener() {}, clearInterval() {} }
    try { cleanups.forEach(callback => callback()); scope.stop() }
    finally { globalThis.document = previousDocument; globalThis.window = previousWindow }
  } }
}

test('集群列表不再展示默认命名空间列，操作列仍固定在右侧', () => {
  const fixture = setup()
  const keys = fixture.state.columns.map(column => column.key)
  assert.deepEqual(keys, ['name', 'api_server', 'hosts', 'updated_at', 'actions'])
  assert.equal(keys.includes('namespace'), false)
  assert.doesNotMatch(inventorySource, /默认命名空间/)
  assert.doesNotMatch(inventorySource, /column\.key === 'namespace'/)
  const actions = fixture.state.columns.find(column => column.key === 'actions')
  assert.equal(actions.fixed, 'right')
  assert.ok(actions.width >= 180, '操作列需要容纳查看集群与编辑')
  fixture.stop()
})

test('操作列的查看集群按集群打开 Kubernetes 总览，不进入本地主机页', () => {
  const fixture = setup()
  for (const cluster of clusters) {
    fixture.state.viewCluster(cluster)
    assert.deepEqual(fixture.routes.at(-1), { path: '/kubernetes', query: { cluster: cluster.id, view: 'overview' } })
  }
  assert.equal(fixture.routes.length, clusters.length)
  fixture.stop()
})

test('缺少 Kubernetes 读取权限时不显示查看集群，也不能跳转', () => {
  const fixture = setup(['cluster.read', 'cluster.update'])
  assert.equal(fixture.state.canViewCluster.value, false)
  fixture.state.viewCluster(clusters[0])
  assert.deepEqual(fixture.routes, [])
  fixture.permissions.add('kubernetes.workload.read')
  assert.equal(fixture.state.canViewCluster.value, true)
  fixture.state.viewCluster(clusters[0])
  assert.deepEqual(fixture.routes, [{ path: '/kubernetes', query: { cluster: 'cluster-1', view: 'overview' } }])
  fixture.stop()
})

test('操作列按钮声明查看集群并保留编辑与更多操作', () => {
  assert.match(inventorySource, /<a-button v-if="canViewCluster" size="small" @click="viewCluster\(record\)">查看集群<\/a-button>/)
  assert.match(inventorySource, /<a-button v-if="auth\.canAny\(\['cluster\.update'\]\)" size="small" :disabled="Boolean\(error\)" @click="editCluster\(record\)">编辑<\/a-button>/)
})
