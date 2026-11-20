import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import * as vue from 'vue'
import { createRouter, createMemoryHistory } from 'vue-router'
import { compileScript, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import ts from 'typescript'
import { loadFrontend } from './acceptanceHarness.mjs'

const api = loadFrontend('../src/api/kubernetes.ts')
const workspace = loadFrontend('../src/utils/kubernetesWorkspace.ts')
const presentation = loadFrontend('../src/utils/kubernetesPresentation.ts', { '@/api/kubernetes': api, '@/utils/kubernetesRoles': loadFrontend('../src/utils/kubernetesRoles.ts') })
const explorerHelpers = loadFrontend('../src/utils/kubernetesWorkloadExplorer.ts')
const podType = api.kubeTypes.find(item => item.resource === 'pods')
const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
function pod(uid = 'pod-a') {
  return { apiVersion: 'v1', kind: 'Pod', metadata: { uid, name: 'web', namespace: 'team' }, spec: { containers: [{ name: 'web' }, { name: 'metrics' }], initContainers: [{ name: 'prepare' }], ephemeralContainers: [{ name: 'debug' }] }, status: { phase: 'Running', containerStatuses: [{ name: 'web', state: { running: {} } }, { name: 'metrics', state: { running: {} } }], initContainerStatuses: [{ name: 'prepare', state: { terminated: { exitCode: 0 } } }], ephemeralContainerStatuses: [{ name: 'debug', state: { running: {} } }] } }
}
async function harness(getResource) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/kubernetes', component: {} }] })
  await router.push('/kubernetes?view=workloads&resource=pods&cluster=cluster-a&namespace=team')
  const route = new Proxy({}, { get: (_, key) => router.currentRoute.value[key] }), blocked = vue.reactive(new Set()), messages = [], requests = []
  const component = loadFrontend('../src/views/KubernetesView.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {} },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'vue-router': { useRoute: () => route, useRouter: () => router, onBeforeRouteLeave() {}, onBeforeRouteUpdate() {} },
    'ant-design-vue': { Modal: { confirm() {} }, message: { warning: value => messages.push(value) } },
    '@/api/kubernetes': { ...api, listKubeResourceScopes: async () => ({ items: [], cursor: {} }), listKubeResources: async () => ({ items: [], continue: '' }) },
    '@/utils/kubernetesWorkspace': workspace, '@/utils/kubernetesPresentation': presentation,
    '@/stores/auth': { useAuthStore: () => ({ canAny: codes => codes.some(code => !blocked.has(code)) }) },
    '@/api/client': { __esModule: true, default: { get: async (url, options) => {
      if (url.includes('/resources/')) requests.push({ url, options })
      if (url.endsWith('/events')) return { data: { items: [] } }
      if (getResource && url.includes('/resources/')) return { data: await getResource(url, options) }
      return { data: url.endsWith('/namespaces') ? { namespaces: ['team'] } : { counts: {}, nodes: [], usage: {}, phases: {} } }
    } } },
  }, { document: { hidden: false }, localStorage: { getItem: () => 'project-a' } }).default
  const scope = vue.effectScope(); let state
  scope.run(() => { state = component.setup({}, { expose() {} }) })
  state.clusters.value = [{ id: 'cluster-a', name: 'Cluster A', is_active: true, default_namespace: 'team' }]
  state.applyScopeQuery();state.selectionReady.value=true; await settle()
  const current = pod()
  state.detailCluster.value='cluster-a';state.detail.value = workspace.withKubeCluster(current,'cluster-a'); state.detailType.value = podType; state.detailOpen.value = true; state.detailLoading.value = false
  return { state, blocked, messages, requests, target: container => ({ cluster: 'cluster-a', namespace: 'team', podUID: 'pod-a', container }), attachExplorer() {
    const explorer = loadFrontend('../src/components/KubeWorkloadExplorer.vue', {
      vue: { ...vue, onMounted() {}, onBeforeUnmount() {} },
      'vue-i18n': { useI18n: () => ({ t: key => key }) },
      '@/api/kubernetes': api, '@/utils/kubernetesPresentation': presentation,
      '@/utils/kubernetesWorkspace': workspace, '@/utils/kubernetesWorkloadExplorer': explorerHelpers,
    }).default
    const props = {
      get cluster() { return state.cluster.value },
      get object() { return state.detail.value },
      get disabled() { return !state.detailOpen.value || Boolean(state.busy.value || state.detailLoading.value || state.detailError.value || state.detailSyncError.value) },
      get selection() { return state.workloadSelection.value },
      type: podType, pods: [], can: code => !blocked.has(code), canFollow: state.canFollowWorkloadRuntime,
    }
    return scope.run(() => explorer.setup(props, { expose() {}, emit(event, ...args) {
      if (event === 'selection') state.workloadSelections.value[state.workloadSelectionKey.value] = args[0]
      if (event === 'logs') state.openWorkloadStream(...args)
      if (event === 'terminal') state.openWorkloadStream(...args, true)
      if (event === 'follow') void state.followReference(...args)
    } }))
  }, stop() { state.cancelListPersist(); scope.stop() } }
}

test('从 Pod 列表直接进入 Deployment 三栏并选中原 Pod，不先渲染 Pod 详情', async () => {
  const selection = pod(), deployment = { kind: 'Deployment', apiVersion: 'apps/v1', metadata: { name: 'workload', namespace: 'team', uid: 'deployment-a' } }
  let respond
  const h = await harness(() => new Promise(resolve => { respond = resolve }))
  const pending = h.state.openDetails(selection, 'basic', podType)
  assert.equal(h.state.detail.value.apiVersion, undefined)
  assert.equal(h.state.detailLoading.value, true)
  assert.deepEqual(h.requests[0].options.params, { namespace: 'team', include_deployment: true })
  respond({ object: selection, deployment: { object: deployment, replica_set_uid: 'rs-a' } })
  await pending
  assert.equal(h.state.detail.value.kind, 'Deployment')
  assert.equal(h.state.detailType.value.resource, 'deployments')
  assert.equal(h.state.detailTab.value, 'basic')
  assert.equal(h.state.lastViewedRow.value, JSON.stringify(['cluster-a','pod-a']))
  assert.deepEqual(h.state.workloadSelections.value['cluster-adeployment-a'], { revisionUID: 'rs-a', podUID: 'pod-a', containerName: '', podTab: 'summary' })
  assert.equal(h.state.canFollowWorkloadRuntime({ resource: 'pods', name: 'web', namespace: 'team', uid: 'pod-a' }), false)
  assert.equal(h.state.canFollowWorkloadRuntime({ resource: 'nodes', name: 'worker' }), true)
  assert.deepEqual(h.requests.map(request => request.url), ['/kubernetes/clusters/cluster-a/resources/pods/web', '/kubernetes/clusters/cluster-a/resources/deployments/workload/events'])
  h.stop()
})

test('仅在已核实 Pod 归属时跳转，迟到响应不能替换后一次选择', async () => {
  const selection = pod(), independent = { ...pod('pod-b'), metadata: { ...pod('pod-b').metadata, name: 'standalone' } }
  let respond
  const h = await harness(url => url.endsWith('/web') ? new Promise(resolve => { respond = resolve }) : { object: independent, deployment: null })
  const old = h.state.openDetails(selection, 'basic', podType)
  await h.state.openDetails(independent, 'basic', podType)
  assert.equal(h.requests[0].options.signal.aborted, true)
  respond({ object: selection, deployment: { object: { kind: 'Deployment', metadata: { uid: 'old-deployment', namespace: 'team', name: 'old' } }, replica_set_uid: 'rs-old' } })
  await old
  assert.equal(h.state.detail.value.metadata.uid, 'pod-b')
  assert.equal(h.state.detailType.value.resource, 'pods')
  assert.deepEqual(h.state.workloadSelections.value, {})
  h.stop()
})

test('从关联资源进入 Pod 所属 Deployment，返回后保留原资源及页签', async () => {
  const source = { kind: 'Service', apiVersion: 'v1', metadata: { name: 'service', namespace: 'team', uid: 'service-a' } }
  const deployment = { kind: 'Deployment', apiVersion: 'apps/v1', metadata: { name: 'workload', namespace: 'team', uid: 'deployment-a' } }
  const h = await harness(url => url.endsWith('/pods/web')
    ? { object: pod(), deployment: { object: deployment, replica_set_uid: 'rs-a' } }
    : { object: source })
  h.state.detail.value = source
  h.state.detailType.value = api.kubeTypes.find(type => type.resource === 'services')
  h.state.detailTab.value = 'pods'
  await h.state.followReference({ resource: 'pods', name: 'web', namespace: 'team', uid: 'pod-a' })
  assert.equal(h.state.detail.value.metadata.uid, 'deployment-a')
  await h.state.backDetail()
  assert.equal(h.state.detail.value.metadata.uid, 'service-a')
  assert.equal(h.state.detailTab.value, 'pods')
  assert.equal(h.state.detailHistory.value.length, 0)
  h.stop()
})

test('初始默认普通容器，显式选择失效后保留名称并停止日志和弹出链接', async () => {
  const h = await harness(), explorer = h.attachExplorer(), current = h.state.detail.value
  assert.equal(explorer.selectedContainer.value.name, 'web')
  explorer.chooseRuntimeContainer(h.target('metrics'))
  assert.equal(explorer.selectedContainer.value.name, 'metrics'); assert.equal(h.state.stream.value.open, false)
  explorer.openRuntimeContainer(h.target('metrics'))
  h.state.stream.value.open = false
  current.spec.containers = current.spec.containers.filter(item => item.name !== 'metrics')
  assert.equal(explorer.selectedContainer.value, undefined)
  assert.equal(explorer.containerName.value, 'metrics'); assert.equal(h.state.missingPodContainer(current), true)
  assert.equal(h.state.podStreamPath(current), ''); assert.equal(h.state.podStreamPath(current, true), '')
  explorer.openRuntimeContainer(h.target('metrics')); assert.equal(h.state.stream.value.open, false)
  explorer.chooseRuntimeContainer(h.target('web')); assert.equal(explorer.selectedContainer.value.name, 'web')
  h.stop()
})

test('Pod 直接点击只切换容器；打开日志与新窗口使用相同 UID 和容器路径', async () => {
  const h = await harness(), explorer = h.attachExplorer()
  explorer.chooseRuntimeContainer(h.target('metrics')); assert.equal(h.state.stream.value.open, false)
  explorer.openRuntimeContainer(h.target('metrics'))
  const path = h.state.podStreamPath(h.state.detail.value)
  assert.equal(h.state.stream.value.open, true); assert.equal(h.state.stream.value.path, path)
  assert.match(path, /\/pods\/web\/containers\/metrics\/logs\/ws\?project_id=project-a&pod_uid=pod-a$/)
  assert.equal(h.state.stream.value.title, 'web / metrics')
  h.state.stream.value.open = false
  explorer.openRuntimeContainer(h.target('debug'), true)
  assert.equal(h.state.stream.value.path, h.state.podStreamPath(h.state.detail.value, true))
  assert.equal(h.state.stream.value.terminal, true)
  h.stop()
})

test('迟到的旧集群、命名空间、Pod UID 和不存在容器不能切换或打开当前目标', async () => {
  const h = await harness(), explorer = h.attachExplorer()
  for (const target of [{ ...h.target('metrics'), cluster: 'old-cluster' }, { ...h.target('metrics'), namespace: 'other' }, { ...h.target('metrics'), podUID: 'pod-old' }, h.target('removed')]) {
    explorer.chooseRuntimeContainer(target); explorer.openRuntimeContainer(target); explorer.openRuntimeContainer(target, true)
    assert.equal(explorer.selectedContainer.value.name, 'web'); assert.equal(h.state.stream.value.open, false)
  }
  h.state.detail.value = pod('replacement')
  explorer.openRuntimeContainer(h.target('web')); assert.equal(h.state.stream.value.open, false)
  h.stop()
})

test('Pod 独立详情不提供自身完整详情跳转，节点与其他 Pod 关联仍按权限开放', async () => {
  const h = await harness(), explorer = h.attachExplorer(), self = { resource: 'pods', name: 'web', namespace: 'team', uid: 'pod-a' }
  assert.equal(h.state.canFollowWorkloadRuntime(self), false)
  assert.equal(h.state.canFollowWorkloadRuntime({ ...self, uid: undefined }), false)
  explorer.followRuntimeReference(self, h.target('web'))
  assert.equal(h.state.detailHistory.value.length, 0); assert.equal(h.state.detail.value.metadata.uid, 'pod-a')
  assert.equal(h.state.canFollowWorkloadRuntime({ resource: 'nodes', name: 'worker-a' }), true)
  assert.equal(h.state.canFollowWorkloadRuntime({ ...self, name: 'other-pod', uid: 'pod-b' }), true)
  h.blocked.add('kubernetes.node.read')
  assert.equal(h.state.canFollowWorkloadRuntime({ resource: 'nodes', name: 'worker-a' }), false)
  h.stop()
})

test('读取中、读取失败、关闭详情和撤销权限均阻止直接容器操作', async () => {
  const h = await harness(), explorer = h.attachExplorer()
  for (const [field, value] of [['busy', 'operation'], ['detailLoading', true], ['detailError', '读取失败'], ['detailSyncError', '同步失败'], ['detailOpen', false]]) {
    const before = h.state[field].value; h.state[field].value = value
    explorer.chooseRuntimeContainer(h.target('metrics')); explorer.openRuntimeContainer(h.target('metrics'))
    h.state.openWorkloadStream(h.state.detail.value, 'metrics')
    assert.equal(explorer.selectedContainer.value.name, 'web', field); assert.equal(h.state.stream.value.open, false, field)
    h.state[field].value = before
  }
  h.blocked.add('kubernetes.workload.read')
  explorer.chooseRuntimeContainer(h.target('metrics')); explorer.openRuntimeContainer(h.target('metrics'))
  assert.equal(h.state.podStreamPath(h.state.detail.value), ''); assert.equal(h.state.stream.value.open, false)
  h.blocked.delete('kubernetes.workload.read'); h.blocked.add('terminal.open')
  explorer.openRuntimeContainer(h.target('web'), true); assert.equal(h.state.stream.value.open, false)
  assert.equal(h.state.podStreamPath(h.state.detail.value, true), '')
  h.stop()
})

test('Init 可查看日志但不能打开终端，停止或删除中的容器不生成终端入口', async () => {
  const h = await harness(), explorer = h.attachExplorer()
  explorer.openRuntimeContainer(h.target('prepare'), true); assert.equal(h.state.stream.value.open, false)
  explorer.openRuntimeContainer(h.target('prepare')); assert.equal(h.state.stream.value.terminal, false); assert.match(h.state.stream.value.path, /containers\/prepare\/logs/)
  h.state.stream.value.open = false
  h.state.detail.value.metadata.deletionTimestamp = '2026-11-17T00:00:00Z'
  explorer.openRuntimeContainer(h.target('web'), true); assert.equal(h.state.stream.value.open, false); assert.equal(h.state.podStreamPath(h.state.detail.value, true), '')
  delete h.state.detail.value.metadata.deletionTimestamp
  h.state.detail.value.status.containerStatuses[0].state = { terminated: { exitCode: 1 } }
  explorer.openRuntimeContainer(h.target('web'), true); assert.equal(h.state.stream.value.open, false)
  h.stop()
})

test('Job 与独立 Pod 走三栏并选中自身，概览和模板共用容器选择', async () => {
  const current = pod(), h = await harness(() => ({ object: current, deployment: null }))
  current.metadata.ownerReferences = [
    { kind: 'ConfigMap', name: 'unrelated', uid: 'config-a', controller: false },
    { kind: 'Job', apiVersion: 'batch/v1', name: 'installer', uid: 'job-a', controller: true },
  ]
  await h.state.openDetails(current, 'basic', podType)
  const explorer = h.attachExplorer()
  assert.equal(explorer.visiblePods.value.length, 1)
  assert.equal(explorer.selectedPod.value.metadata.uid, current.metadata.uid)
  assert.deepEqual(explorer.podControllerLink.value, { resource: 'jobs', name: 'installer', namespace: 'team', uid: 'job-a' })
  assert.equal(h.state.needsRelatedPods.value, false)
  explorer.chooseRuntimeContainer(h.target('metrics'))
  await settle()
  assert.equal(h.state.workloadSelection.value.containerName, 'metrics')
  h.state.rememberTemplatePodContainer('web')
  await settle()
  assert.equal(explorer.containerName.value, 'web')
  h.state.detail.value.metadata.ownerReferences = current.metadata.ownerReferences.slice(0, 1)
  assert.equal(explorer.podController.value, undefined)
  assert.equal(explorer.selectedPod.value.metadata.uid, current.metadata.uid)
  h.stop()
})

test('关联 Pod 直接点选保留行目标，刷新、错误或容器失效不允许操作', async () => {
  const h = await harness(), row = pod()
  h.state.detail.value = { kind: 'Service', metadata: { name: 'web', uid: 'service-a', namespace: 'team' } }
  h.state.relatedPods.value = [row]
  h.state.chooseRelatedPodContainer(row, 'metrics'); assert.equal(h.state.stream.value.open, false)
  assert.equal(h.state.selectedPodContainer(row).name, 'metrics'); assert.equal(h.state.allowedRelatedPodStream(row, true), true)
  h.state.openRelatedPodStream(row); assert.equal(h.state.stream.value.path, h.state.podStreamPath(row))
  h.state.stream.value.open = false
  for (const field of ['relatedLoading', 'relatedError', 'detailSyncError']) {
    const before = h.state[field].value; h.state[field].value = field.endsWith('Loading') ? true : '读取失败'
    h.state.chooseRelatedPodContainer(row, 'web'); h.state.openRelatedPodStream(row)
    assert.equal(h.state.allowedRelatedPodStream(row), false); assert.equal(h.state.selectedPodContainer(row).name, 'metrics'); assert.equal(h.state.stream.value.open, false)
    h.state[field].value = before
  }
  h.state.relatedPods.value = [pod('replacement')]
  h.state.chooseRelatedPodContainer(row, 'web'); h.state.openRelatedPodStream(row)
  assert.equal(h.state.allowedRelatedPodStream(row), false); assert.equal(h.state.stream.value.open, false)
  h.state.relatedPods.value = [row]; row.spec.containers = [{ name: 'web' }]
  assert.equal(h.state.allowedRelatedPodStream(row), false); assert.equal(h.state.missingPodContainer(row), true)
  h.state.chooseRelatedPodContainer(row, 'web'); assert.equal(h.state.allowedRelatedPodStream(row), true)
  h.stop()
})

function renderActions(props) {
  const source = readFileSync(new URL('../src/components/KubeResourceActions.vue', import.meta.url), 'utf8')
  const script = compileScript(parse(source).descriptor, { id: 'pod-actions', inlineTemplate: true }).content
  const output = ts.transpileModule(script, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }, modules = { vue, 'vue-i18n': { useI18n: () => ({ t: key => key }) }, './ResourceRowActions.vue': { __esModule: true, default: { setup: (_, { slots }) => () => vue.h('div', [slots.default?.(), slots.more?.()]) } }, './SessionPopoutLink.vue': { __esModule: true, default: { props: ['path'], setup: props => () => vue.h('a', { href: props.path }, 'popout') } } }
  new Function('require', 'module', 'exports', output)(name => modules[name] ?? {}, module, module.exports)
  const app = vue.createSSRApp(module.exports.default, props)
  app.component('a-button', { props: ['disabled'], setup: (props, { slots }) => () => vue.h('button', { disabled: props.disabled }, slots.default?.()) })
  app.component('a-menu-item', { setup: (_, { slots }) => () => vue.h('div', slots.default?.()) })
  return renderToString(app)
}

test('Pod 页头隐藏日志终端及弹出入口，同时保留删除等资源操作', async () => {
  const props = { object: pod(), type: podType, can: () => true, inDrawer: true, terminalReady: true, logsPath: '/logs', terminalPath: '/terminal' }
  const normal = await renderActions(props)
  assert.match(normal, /k8s.logs/); assert.match(normal, /k8s.terminal/); assert.match(normal, /popout/)
  const hidden = await renderActions({ ...props, hideContainerActions: true })
  assert.doesNotMatch(hidden, /k8s.logs|k8s.terminal|popout/)
  assert.match(hidden, /k8s.remove/)
})

test('Pod 总览与关联表格使用直接容器项，页头不再保留容器下拉', () => {
  const source = readFileSync(new URL('../src/views/KubernetesView.vue', import.meta.url), 'utf8')
  const template = parse(source).descriptor.template.content
  assert.doesNotMatch(template, /containerOptions|container-select/)
  assert.match(template, /<KubeWorkloadExplorer v-if="workloadDetail"/)
  assert.doesNotMatch(template, /<KubeWorkloadPodRuntime/)
  assert.match(template, /@click="chooseRelatedPodContainer\(row,item.name\)"/)
  assert.match(template, /:aria-pressed="selectedPodContainer\(row\)\?\.name===item.name"/)
  assert.match(template, /:disabled="!allowedRelatedPodStream\(row,true\)"/)
})
