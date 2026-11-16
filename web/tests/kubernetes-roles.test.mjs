import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const roles = loadFrontend('../src/utils/kubernetesRoles.ts')
const api = loadFrontend('../src/api/kubernetes.ts')
const presentation = loadFrontend('../src/utils/kubernetesPresentation.ts', {
  '@/api/kubernetes': api,
  '@/utils/kubernetesRoles': roles,
})
const locale = loadFrontend('../src/locales/kubernetesRoles.ts')
const t = key => locale.kubernetesRolesZh[key.replace('nodeRoles.', '')] ?? key
const node = (labels = {}, name = 'any-node') => ({ kind: 'Node', metadata: { name, labels }, status: {} })

test('节点角色根据标签键识别，兼容 master，显式工作节点支持双角色', () => {
  assert.deepEqual(roles.kubernetesNodeRoles(node({ 'node-role.kubernetes.io/control-plane': '' })), ['control-plane'])
  assert.deepEqual(roles.kubernetesNodeRoles(node({ 'node-role.kubernetes.io/master': 'true' })), ['control-plane'])
  assert.deepEqual(roles.kubernetesNodeRoles(node({ 'node-role.kubernetes.io/control-plane': '', 'node-role.kubernetes.io/master': '', 'node-role.kubernetes.io/worker': '' })), ['control-plane', 'worker'])
  assert.deepEqual(roles.kubernetesNodeRoles(node({ 'node-role.kubernetes.io/worker': '' })), ['worker'])
  assert.deepEqual(roles.kubernetesNodeRoles(node()), ['worker'])
})

test('名称、Pod 数量、架构和继承标签不参与角色推断', () => {
  const worker = node({}, 'master-01')
  worker.status = { nodeInfo: { architecture: 'arm64' }, allocatable: { pods: '110' } }
  assert.deepEqual(roles.kubernetesNodeRoles(worker), ['worker'])
  assert.deepEqual(roles.kubernetesNodeRoles(node(Object.create({ 'node-role.kubernetes.io/control-plane': '' }))), ['worker'])
  assert.deepEqual(roles.kubernetesNodeRoles(null), [])
  assert.deepEqual(roles.kubernetesNodeRoles({ kind: 'Pod', metadata: { labels: { 'node-role.kubernetes.io/master': '' } } }), [])
})

test('已注册的 K3s 控制节点同时显示工作角色，普通或缺少证据的控制节点不补角色', () => {
  for (const version of ['v1.37.1+k3s1', '1.37.1+k3s2', 'v1.38.0-rc1+k3s1', 'v1.38.0-rc.1+k3s1']) {
    const object = node({ 'node-role.kubernetes.io/control-plane': 'true' })
    object.status = { nodeInfo: { kubeletVersion: version } }
    assert.deepEqual(roles.kubernetesNodeRoles(object), ['control-plane', 'worker'])
    object.spec = { unschedulable: true, taints: [{ key: 'node-role.kubernetes.io/control-plane', effect: 'NoSchedule' }] }
    assert.deepEqual(roles.kubernetesNodeRoles(object), ['control-plane', 'worker'], '角色与当前调度限制分别展示')
    const cells = presentation.resourceCells(object, t)
    assert.equal(cells.role.text, '控制平面 (Master)')
    assert.equal(cells.role.secondary, '工作节点 (Worker)')
    assert.ok(presentation.resourceSearchText(object, t).includes('worker'))
  }
  for (const version of [undefined, '', 'v1.37.1', 'v1.37.1+rke2r1', 'k3s', 'v1.37.1+k3s1-extra', 'v1.02.3+k3s1', 'v1.38.0-rc..1+k3s1', 'v1.38.0-rc.01+k3s1', 'v1.37.1+k3s0', 'v1.37.1+k3s01', ' v1.37.1+k3s1', 'v1.37.1+k3s1\n', 123]) {
    const object = node({ 'node-role.kubernetes.io/control-plane': 'true' }, 'k3s-worker')
    object.status = { nodeInfo: { kubeletVersion: version }, allocatable: { pods: '110' } }
    assert.deepEqual(roles.kubernetesNodeRoles(object), ['control-plane'])
  }
})

test('列表角色列使用真实角色文案，并纳入现有搜索', () => {
  const type = api.kubeTypes.find(item => item.resource === 'nodes')
  const columns = presentation.resourceColumns(type, t)
  assert.equal(columns[1].key, 'role')
  assert.equal(columns[1].label, '节点角色')
  const control = node({ 'node-role.kubernetes.io/master': '', 'node-role.kubernetes.io/worker': '' })
  const cells = presentation.resourceCells(control, t)
  assert.equal(cells.role.text, '控制平面 (Master)')
  assert.equal(cells.role.secondary, '工作节点 (Worker)')
  assert.ok(presentation.resourceSearchText(control, t).includes('master'))
  assert.ok(presentation.resourceSearchText(control, t).includes('worker'))
  assert.equal(roles.kubernetesRolesText(['control-plane', 'worker', 'control-plane'], t), '控制平面 (Master) / 工作节点 (Worker)')
  assert.equal(roles.kubernetesRolesText(['worker', 'control-plane'], t), '控制平面 (Master) / 工作节点 (Worker)')
  assert.equal(roles.kubernetesRolesText([], t), '—')
})

test('节点详情随当前节点响应更新角色，不把上一节点角色保留给新节点', async () => {
  const component = loadFrontend('../src/components/KubeNodeInfo.vue', {
    vue,
    'vue-i18n': { useI18n: () => ({ t }) },
    '@/api/kubernetes': api,
    '@/utils/kubernetesRoles': roles,
  }).default
  const props = vue.reactive({ node: node({ 'node-role.kubernetes.io/control-plane': '' }) })
  const scope = vue.effectScope()
  const state = scope.run(() => component.setup(props, { expose() {} }))
  assert.deepEqual(state.roles.value, ['control-plane'])
  props.node = { ...node({ 'node-role.kubernetes.io/control-plane': '' }), status: { nodeInfo: { kubeletVersion: 'v1.37.1+k3s1' } } }
  await vue.nextTick()
  assert.deepEqual(state.roles.value, ['control-plane', 'worker'])
  props.node = node({ 'node-role.kubernetes.io/worker': '' })
  await vue.nextTick()
  assert.deepEqual(state.roles.value, ['worker'])
  scope.stop()
})
