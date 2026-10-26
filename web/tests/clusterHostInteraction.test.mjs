import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

function load(path, modules = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const code = path.endsWith('.vue') ? compileScript(parse(source).descriptor, { id: 'cluster-host-test' }).content : source
  const output = ts.transpileModule(code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output)(name => modules[name] ?? {}, module, module.exports)
  return module.exports
}
const relations = load('../src/utils/infrastructureRelations.ts')
const t = key => key
const host = { id: 'host-1', name: 'node-1', address: '10.0.0.1', is_active: true, is_builtin: false, capabilities: [] }
const cluster = { id: 'cluster-1', name: 'team', api_server: 'https://10.0.0.1:6443', default_namespace: 'default', is_active: true, hosts: [host] }
function deferred() { let resolve, reject; const promise = new Promise((ok, fail) => { resolve = ok; reject = fail }); return { promise, resolve, reject } }
function setup(path, initialProps, extra = {}, initialPermissions = ['cluster.read', 'cluster.update', 'cluster.execute']) {
  const props = vue.reactive(initialProps), events = [], errors = [], routes = [], cleanups = []
  const permissions = vue.reactive(new Set(initialPermissions))
  const auth = { canAny: values => values.some(value => permissions.has(value)) }
  const mockedVue = { ...vue, onBeforeUnmount: callback => cleanups.push(callback), onMounted() {} }
  const route = vue.reactive({ path: '/hosts', query: { view: 'clusters' } })
  const list = load('../src/utils/resourceList.ts', { vue: mockedVue, 'vue-i18n': { useI18n: () => ({ t }) } })
  const modules = {
    vue: mockedVue,
    'vue-i18n': { useI18n: () => ({ t }) },
    'vue-router': { useRoute: () => route, useRouter: () => ({ push: value => routes.push(value), replace: value => routes.push(value) }) },
    'ant-design-vue': { message: { success() {}, error: value => errors.push(value), info() {} } },
    '@/api/resources': { apiErrorMessage: () => '连接失败' },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/infrastructureRelations': relations,
    '@/utils/resourceList': list,
    ...extra,
  }
  const component = load(path, modules).default
  const scope = vue.effectScope()
  const state = scope.run(() => component.setup(props, { expose() {}, emit: (name, value) => { events.push([name, value]); if (name === 'update:open') props.open = value } }))
  return { state, props, events, errors, routes, permissions, stop() {
    const previousDocument = globalThis.document, previousWindow = globalThis.window
    globalThis.document ??= { removeEventListener() {} }
    globalThis.window ??= { removeEventListener() {}, clearInterval() {} }
    try { cleanups.forEach(callback => callback()); scope.stop() }
    finally { globalThis.document = previousDocument; globalThis.window = previousWindow }
  } }
}

test('关联主机保存防重复提交，提交期间不能关闭，成功后只关闭一次', async () => {
  const request = deferred(), calls = []
  const fixture = setup('../src/components/ClusterHostDrawer.vue', { open: true, cluster, clusters: [cluster], hosts: [host] }, { '@/api/infrastructure': { setClusterHosts: (...args) => { calls.push(args); return request.promise } } })
  const pending = fixture.state.save()
  await fixture.state.save()
  fixture.state.close()
  assert.deepEqual(calls, [['cluster-1', ['host-1']]])
  assert.equal(fixture.events.length, 0)
  request.resolve(cluster)
  await pending
  assert.deepEqual(fixture.events.map(event => event[0]), ['saved', 'update:open'])
  assert.equal(fixture.props.open, false)
  fixture.stop()
})

test('关联失败保留选择和抽屉，未知关联及无权限时不能写空数组', async () => {
  let calls = 0
  const fixture = setup('../src/components/ClusterHostDrawer.vue', { open: true, cluster, clusters: [cluster], hosts: [host] }, { '@/api/infrastructure': { setClusterHosts: async () => { calls++; throw Error('private detail') } } })
  await fixture.state.save()
  assert.equal(fixture.props.open, true)
  assert.deepEqual([...fixture.state.selectedIDs.value], ['host-1'])
  assert.deepEqual(fixture.errors, ['连接失败'])
  fixture.props.cluster = { ...cluster, hosts: null }
  await vue.nextTick()
  await fixture.state.save()
  fixture.props.cluster = cluster
  fixture.permissions.delete('cluster.update')
  await fixture.state.save()
  assert.equal(calls, 1)
  fixture.stop()
})

test('集群只读用户无法借旧主机数据跳转，未知关联不被筛为独立集群', async () => {
  const hidden = { ...cluster, hosts: null }
  const fixture = setup('../src/components/ClusterInventory.vue', { clusters: [hidden], hosts: [host], loading: false }, {}, ['kubernetes.workload.read'])
  assert.equal(fixture.state.canOpenHost(host), false)
  fixture.state.openHost(host)
  assert.equal(fixture.events.length, 0)
  assert.equal(fixture.state.associationKnown(hidden), false)
  assert.equal(fixture.state.unknownAssociationLabel(), '无主机查看权限')
  fixture.state.association.value = 'standalone'
  assert.equal(fixture.state.filtered.value.length, 0)
  fixture.permissions.add('cluster.read')
  assert.equal(fixture.state.associationKnown(hidden), false)
  fixture.props.clusters = [{ ...hidden, hosts: [] }]
  assert.equal(fixture.state.associationKnown(fixture.props.clusters[0]), true)
  fixture.stop()
})

test('主机列表请求失败不阻止独立集群加载，失败数据不会残留为成功列表', async () => {
  let failHosts = false, failClusters = false
  const fixture = setup('../src/views/HostView.vue', {}, {
    '@/api/infrastructure': {
      listHosts: async () => { if (failHosts) throw Error('hosts unavailable'); return [host] },
      listEnvironments: async () => [],
      capabilityOf: () => undefined, environmentIDsOf: () => [],
    },
    '@/api/client': { get: async () => { if (failClusters) throw Error('clusters unavailable'); return { data: { clusters: [cluster] } } } },
  }, ['cluster.read'])
  await fixture.state.refresh()
  assert.equal(fixture.state.hosts.value.length, 1)
  assert.equal(fixture.state.clusters.value.length, 1)
  failHosts = true
  await fixture.state.refresh()
  assert.equal(fixture.state.hosts.value.length, 0)
  assert.equal(fixture.state.hostLoadError.value, '连接失败')
  assert.equal(fixture.state.clusters.value.length, 1)
  failClusters = true
  await fixture.state.refresh()
  assert.equal(fixture.state.clusters.value.length, 0)
  assert.equal(fixture.state.clusterLoadError.value, '连接失败')
  fixture.stop()
})

test('导入 kubeconfig 只填本地草稿，限制文件类型与大小，关闭后丢弃迟到读取', async () => {
  const readers = [], originalReader = globalThis.FileReader
  globalThis.FileReader = class {
    constructor() { readers.push(this) }
    readAsText() {}
  }
  const fixture = setup('../src/components/KubernetesClusterDrawer.vue', { open: true, canTest: true, canAssociate: false, hosts: [], clusters: [] })
  try {
    await fixture.state.readKubeconfigFile({ name: 'large.yaml', size: 1024 * 1024 + 1 })
    await fixture.state.readKubeconfigFile({ name: 'config.exe', size: 1 })
    assert.equal(readers.length, 0)
    const reading = fixture.state.readKubeconfigFile({ name: 'cluster.yaml', size: 20 })
    readers[0].result = 'apiVersion: v1\nclusters: []'
    readers[0].onload()
    await reading
    assert.equal(fixture.state.form.kubeconfig, 'apiVersion: v1\nclusters: []')
    assert.deepEqual(fixture.events, [])
    assert.equal(fixture.state.connectionTested.value, false)
    const late = fixture.state.readKubeconfigFile({ name: 'cluster.conf', size: 20 })
    fixture.props.open = false
    await vue.nextTick()
    readers[1].result = 'must-not-replace'
    readers[1].onload()
    await late
    assert.equal(fixture.state.form.kubeconfig, 'apiVersion: v1\nclusters: []')
  } finally {
    fixture.stop()
    globalThis.FileReader = originalReader
  }
})
