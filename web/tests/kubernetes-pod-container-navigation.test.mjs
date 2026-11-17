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
const podType = api.kubeTypes.find(item => item.resource === 'pods')
const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
function pod(uid = 'pod-a') {
  return { apiVersion: 'v1', kind: 'Pod', metadata: { uid, name: 'web', namespace: 'team' }, spec: { containers: [{ name: 'web' }, { name: 'metrics' }], initContainers: [{ name: 'prepare' }], ephemeralContainers: [{ name: 'debug' }] }, status: { phase: 'Running', containerStatuses: [{ name: 'web', state: { running: {} } }, { name: 'metrics', state: { running: {} } }], initContainerStatuses: [{ name: 'prepare', state: { terminated: { exitCode: 0 } } }], ephemeralContainerStatuses: [{ name: 'debug', state: { running: {} } }] } }
}
async function harness() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/kubernetes', component: {} }] })
  await router.push('/kubernetes?view=workloads&resource=pods&cluster=cluster-a&namespace=team')
  const route = new Proxy({}, { get: (_, key) => router.currentRoute.value[key] }), blocked = vue.reactive(new Set()), messages = []
  const component = loadFrontend('../src/views/KubernetesView.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {} },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'vue-router': { useRoute: () => route, useRouter: () => router, onBeforeRouteLeave() {}, onBeforeRouteUpdate() {} },
    'ant-design-vue': { Modal: { confirm() {} }, message: { warning: value => messages.push(value) } },
    '@/api/kubernetes': { ...api, listKubeResourceScopes: async () => ({ items: [], cursor: {} }), listKubeResources: async () => ({ items: [], continue: '' }) },
    '@/utils/kubernetesWorkspace': workspace, '@/utils/kubernetesPresentation': presentation,
    '@/stores/auth': { useAuthStore: () => ({ canAny: codes => codes.some(code => !blocked.has(code)) }) },
    '@/api/client': { __esModule: true, default: { get: async url => ({ data: url.endsWith('/namespaces') ? { namespaces: ['team'] } : { counts: {}, nodes: [], usage: {}, phases: {} } }) } },
  }, { document: { hidden: false }, localStorage: { getItem: () => 'project-a' } }).default
  const scope = vue.effectScope(); let state
  scope.run(() => { state = component.setup({}, { expose() {} }) })
  state.clusters.value = [{ id: 'cluster-a', name: 'Cluster A', is_active: true, default_namespace: 'team' }]
  state.cluster.value = 'cluster-a'; await settle()
  const current = pod()
  state.detail.value = current; state.detailType.value = podType; state.detailOpen.value = true; state.detailLoading.value = false
  return { state, blocked, messages, target: container => ({ cluster: 'cluster-a', namespace: 'team', podUID: 'pod-a', container }), stop() { state.cancelListPersist(); scope.stop() } }
}

test('初始默认普通容器，显式选择失效后保留名称并停止日志和弹出链接', async () => {
  const h = await harness(), current = h.state.detail.value
  assert.equal(h.state.selectedPodContainer(current).name, 'web')
  h.state.chooseDetailPodContainer(h.target('metrics'))
  assert.equal(h.state.selectedPodContainer(current).name, 'metrics'); assert.equal(h.state.stream.value.open, false)
  current.spec.containers = current.spec.containers.filter(item => item.name !== 'metrics')
  assert.equal(h.state.selectedPodContainer(current), undefined)
  assert.equal(h.state.selectedPodContainerName(current), 'metrics'); assert.equal(h.state.missingPodContainer(current), true)
  assert.equal(h.state.podStreamPath(current), ''); assert.equal(h.state.podStreamPath(current, true), '')
  h.state.openStream(current); assert.equal(h.state.stream.value.open, false)
  h.state.chooseDetailPodContainer(h.target('web')); assert.equal(h.state.missingPodContainer(current), false)
  h.stop()
})

test('Pod 直接点击只切换容器；打开日志与新窗口使用相同 UID 和容器路径', async () => {
  const h = await harness()
  h.state.chooseDetailPodContainer(h.target('metrics')); assert.equal(h.state.stream.value.open, false)
  const path = h.state.podStreamPath(h.state.detail.value)
  h.state.openDetailPodContainer(h.target('metrics'))
  assert.equal(h.state.stream.value.open, true); assert.equal(h.state.stream.value.path, path)
  assert.match(path, /\/pods\/web\/containers\/metrics\/logs\/ws\?project_id=project-a&pod_uid=pod-a$/)
  assert.equal(h.state.stream.value.title, 'web / metrics')
  h.state.stream.value.open = false
  h.state.openDetailPodContainer(h.target('debug'), true)
  assert.equal(h.state.stream.value.path, h.state.podStreamPath(h.state.detail.value, true))
  assert.equal(h.state.stream.value.terminal, true)
  h.stop()
})

test('迟到的旧集群、命名空间、Pod UID 和不存在容器不能切换或打开当前目标', async () => {
  const h = await harness()
  for (const target of [{ ...h.target('metrics'), cluster: 'old-cluster' }, { ...h.target('metrics'), namespace: 'other' }, { ...h.target('metrics'), podUID: 'pod-old' }, h.target('removed')]) {
    h.state.chooseDetailPodContainer(target); h.state.openDetailPodContainer(target); h.state.openDetailPodContainer(target, true)
    assert.equal(h.state.selectedPodContainer(h.state.detail.value).name, 'web'); assert.equal(h.state.stream.value.open, false)
  }
  h.state.detail.value = pod('replacement')
  h.state.openDetailPodContainer(h.target('web')); assert.equal(h.state.stream.value.open, false)
  h.stop()
})

test('Pod 独立详情不提供自身完整详情跳转，节点与其他 Pod 关联仍按权限开放', async () => {
  const h = await harness(), self = { resource: 'pods', name: 'web', namespace: 'team', uid: 'pod-a' }
  assert.equal(h.state.canFollowDetailPodReference(self), false)
  assert.equal(h.state.canFollowDetailPodReference({ ...self, uid: undefined }), false)
  h.state.followDetailPodReference(self, h.target('web'))
  assert.equal(h.state.detailHistory.value.length, 0); assert.equal(h.state.detail.value.metadata.uid, 'pod-a')
  assert.equal(h.state.canFollowDetailPodReference({ resource: 'nodes', name: 'worker-a' }), true)
  assert.equal(h.state.canFollowDetailPodReference({ ...self, name: 'other-pod', uid: 'pod-b' }), true)
  h.blocked.add('kubernetes.node.read')
  assert.equal(h.state.canFollowDetailPodReference({ resource: 'nodes', name: 'worker-a' }), false)
  h.stop()
})

test('读取中、读取失败、关闭详情和撤销权限均阻止直接容器操作', async () => {
  const h = await harness()
  for (const [field, value] of [['busy', 'operation'], ['detailLoading', true], ['detailError', '读取失败'], ['detailSyncError', '同步失败'], ['detailOpen', false]]) {
    const before = h.state[field].value; h.state[field].value = value
    h.state.chooseDetailPodContainer(h.target('metrics')); h.state.openDetailPodContainer(h.target('metrics'))
    assert.equal(h.state.selectedPodContainer(h.state.detail.value).name, 'web', field); assert.equal(h.state.stream.value.open, false, field)
    h.state[field].value = before
  }
  h.blocked.add('kubernetes.workload.read')
  h.state.chooseDetailPodContainer(h.target('metrics')); h.state.openDetailPodContainer(h.target('metrics'))
  assert.equal(h.state.podStreamPath(h.state.detail.value), ''); assert.equal(h.state.stream.value.open, false)
  h.blocked.delete('kubernetes.workload.read'); h.blocked.add('terminal.open')
  h.state.openDetailPodContainer(h.target('web'), true); assert.equal(h.state.stream.value.open, false)
  assert.equal(h.state.podStreamPath(h.state.detail.value, true), '')
  h.stop()
})

test('Init 可查看日志但不能打开终端，停止或删除中的容器不生成终端入口', async () => {
  const h = await harness()
  h.state.openDetailPodContainer(h.target('prepare'), true); assert.equal(h.state.stream.value.open, false)
  h.state.openDetailPodContainer(h.target('prepare')); assert.equal(h.state.stream.value.terminal, false); assert.match(h.state.stream.value.path, /containers\/prepare\/logs/)
  h.state.stream.value.open = false; h.state.chooseDetailPodContainer(h.target('web'))
  h.state.detail.value.metadata.deletionTimestamp = '2026-11-17T00:00:00Z'
  h.state.openDetailPodContainer(h.target('web'), true); assert.equal(h.state.stream.value.open, false); assert.equal(h.state.podStreamPath(h.state.detail.value, true), '')
  delete h.state.detail.value.metadata.deletionTimestamp
  h.state.detail.value.status.containerStatuses[0].state = { terminated: { exitCode: 1 } }
  h.state.openDetailPodContainer(h.target('web'), true); assert.equal(h.state.stream.value.open, false)
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
  assert.match(template, /<KubeWorkloadPodRuntime[^>]+:can-follow="canFollowDetailPodReference"[^>]+@container="chooseDetailPodContainer"/)
  assert.match(template, /@click="chooseRelatedPodContainer\(row,item.name\)"/)
  assert.match(template, /:aria-pressed="selectedPodContainer\(row\)\?\.name===item.name"/)
  assert.match(template, /:disabled="!allowedRelatedPodStream\(row,true\)"/)
})
