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
function setup(path, initialProps, extra = {}, initialPermissions = ['cluster.read', 'cluster.update', 'cluster.execute', 'cluster.create']) {
  const props = vue.reactive(initialProps), events = [], errors = [], successes = [], routes = [], cleanups = []
  const permissions = vue.reactive(new Set(initialPermissions))
  const auth = { canAny: values => values.some(value => permissions.has(value)) }
  const mockedVue = { ...vue, onBeforeUnmount: callback => cleanups.push(callback), onMounted() {} }
  const route = vue.reactive({ path: '/hosts', query: { view: 'clusters' } })
  const list = load('../src/utils/resourceList.ts', { vue: mockedVue, 'vue-i18n': { useI18n: () => ({ t }) } })
  const modules = {
    vue: mockedVue,
    'vue-i18n': { useI18n: () => ({ t }) },
    'vue-router': { useRoute: () => route, useRouter: () => ({ push: value => routes.push(value), replace: value => routes.push(value) }) },
    'ant-design-vue': { message: { success: value => successes.push(value), error: value => errors.push(value), info() {} } },
    '@/api/resources': { apiErrorMessage: error => error?.response?.data?.message || '连接失败' },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/infrastructureRelations': relations,
    '@/utils/resourceList': list,
    ...extra,
  }
  const component = load(path, modules).default
  const scope = vue.effectScope()
  const state = scope.run(() => component.setup(props, { expose() {}, emit: (name, value) => { events.push([name, value]); if (name === 'update:open') props.open = value } }))
  return { state, props, events, errors, successes, routes, permissions, stop() {
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
    assert.equal(fixture.state.form.kubeconfig, '')
  } finally {
    fixture.stop()
    globalThis.FileReader = originalReader
  }
})

const mismatchMessage = '所选主机与集群节点信息不一致，无法关联'
const mismatchError = () => ({ response: { status: 400, data: { code: 'kubernetes_cluster_host_mismatch', message: mismatchMessage } } })

test('主机关联核对失败显示服务端原因并保留草稿，重试通过才关闭和刷新', async () => {
  let mismatch = true
  const calls = []
  const fixture = setup('../src/components/ClusterHostDrawer.vue', { open: true, cluster, clusters: [cluster], hosts: [host] }, {
    '@/api/infrastructure': { setClusterHosts: async (...args) => { calls.push(args); if (mismatch) throw mismatchError(); return cluster } },
  })
  await fixture.state.save()
  assert.equal(fixture.state.saveError.value, mismatchMessage)
  assert.equal(fixture.props.open, true)
  assert.deepEqual([...fixture.state.selectedIDs.value], ['host-1'])
  assert.deepEqual(fixture.events, [])
  assert.deepEqual(fixture.successes, [])
  mismatch = false
  await fixture.state.save()
  assert.equal(fixture.state.saveError.value, '')
  assert.equal(fixture.props.open, false)
  assert.equal(calls.length, 2)
  assert.deepEqual(fixture.events.map(event => event[0]), ['saved', 'update:open'])
  assert.deepEqual(fixture.successes, ['关联主机已更新'])
  fixture.stop()
})

test('创建集群时保存核对失败保留 kubeconfig 和主机选择，不发送创建成功事件', async () => {
  let mismatch = true
  const calls = []
  const fixture = setup('../src/components/KubernetesClusterDrawer.vue', { open: true, canTest: true, canAssociate: true, hosts: [host], clusters: [] }, {
    '@/api/client': { post: async (path, payload) => {
      calls.push({ path, host_ids: [...payload.host_ids] })
      if (path.endsWith('/test')) return { data: { api_server: cluster.api_server, version: 'v1.35.5' } }
      if (mismatch) throw mismatchError()
      return { data: { cluster } }
    } },
  })
  Object.assign(fixture.state.form, { name: 'team', kubeconfig: 'private-kubeconfig', host_ids: ['host-1'] })
  await vue.nextTick()
  await fixture.state.testConnection()
  assert.equal(fixture.state.connectionTested.value, true)
  fixture.successes.length = 0
  await fixture.state.submit()
  assert.equal(fixture.state.requestError.value, mismatchMessage)
  assert.equal(fixture.props.open, true)
  assert.equal(fixture.state.form.kubeconfig, 'private-kubeconfig')
  assert.deepEqual([...fixture.state.form.host_ids], ['host-1'])
  assert.deepEqual(fixture.events, [])
  assert.deepEqual(fixture.successes, [])
  mismatch = false
  await fixture.state.submit()
  assert.equal(fixture.state.requestError.value, '')
  assert.equal(fixture.props.open, false)
  assert.deepEqual(fixture.events.map(event => event[0]), ['created', 'update:open'])
  assert.deepEqual(calls.map(item => item.path), ['/kubernetes/clusters/test', '/kubernetes/clusters', '/kubernetes/clusters'])
  fixture.stop()
})

test('修改新建集群所选主机后原连接测试失效，不能沿用旧测试提交', async () => {
  const calls = []
  const fixture = setup('../src/components/KubernetesClusterDrawer.vue', { open: true, canTest: true, canAssociate: true, hosts: [host], clusters: [] }, {
    '@/api/client': { post: async path => { calls.push(path); return { data: { api_server: cluster.api_server, version: 'v1.35.5' } } } },
  })
  Object.assign(fixture.state.form, { name: 'team', kubeconfig: 'private-kubeconfig', host_ids: ['host-1'] })
  await vue.nextTick()
  await fixture.state.testConnection()
  assert.equal(fixture.state.connectionTested.value, true)
  fixture.state.form.host_ids = ['other-host']
  assert.equal(fixture.state.connectionTested.value, false)
  await fixture.state.submit()
  assert.deepEqual(calls, ['/kubernetes/clusters/test'])
  assert.deepEqual(fixture.events, [])
  fixture.stop()
})

test('主机详情更改集群不匹配时保留原有关联及待选集群，重试成功后刷新', async () => {
  let mismatch = true, saved = false
  const target = { ...cluster, id: 'cluster-2', name: 'other', hosts: [] }
  const fixture = setup('../src/views/HostView.vue', {}, {
    '@/api/infrastructure': {
      listHosts: async () => [host], listEnvironments: async () => [],
      capabilityOf: () => undefined, environmentIDsOf: () => [],
      setClusterHosts: async () => { if (mismatch) throw mismatchError(); saved = true; return target },
    },
    '@/api/client': { get: async () => ({ data: { clusters: saved ? [{ ...cluster, hosts: [] }, { ...target, hosts: [host] }] : [cluster, target] } }) },
  })
  await fixture.state.refresh()
  fixture.state.editHostCluster(host)
  fixture.state.relationClusterID.value = target.id
  await vue.nextTick()
  await fixture.state.saveHostCluster()
  assert.equal(fixture.state.relationError.value, mismatchMessage)
  assert.equal(fixture.state.relationOpen.value, true)
  assert.equal(fixture.state.relationClusterID.value, target.id)
  assert.deepEqual(fixture.state.clusters.value[0].hosts.map(item => item.id), ['host-1'])
  assert.deepEqual(fixture.successes, [])
  mismatch = false
  await fixture.state.saveHostCluster()
  assert.equal(fixture.state.relationError.value, '')
  assert.equal(fixture.state.relationOpen.value, false)
  assert.deepEqual(fixture.state.clusters.value[0].hosts, [])
  assert.deepEqual(fixture.state.clusters.value[1].hosts.map(item => item.id), ['host-1'])
  assert.deepEqual(fixture.successes, ['集群关联已更新'])
  fixture.stop()
})
