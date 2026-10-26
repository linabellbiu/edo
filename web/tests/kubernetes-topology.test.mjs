import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const api = loadFrontend('../src/api/kubernetes.ts')
const roles = loadFrontend('../src/utils/kubernetesRoles.ts')
const topology = loadFrontend('../src/utils/kubernetesTopology.ts', { '@/api/kubernetes': api, '@/utils/kubernetesRoles': roles })
const at = Date.parse('2026-10-26T12:00:00Z')
const timestamp = new Date(at).toISOString()
const node = (name, options = {}) => ({
  kind: 'Node', apiVersion: 'v1', metadata: { uid: `${name}-uid`, name, labels: {}, creationTimestamp: '2026-01-01T00:00:00Z' },
  spec: {}, status: { conditions: [{ type: 'Ready', status: 'True' }], addresses: [{ type: 'InternalIP', address: '10.0.0.1' }], nodeInfo: { kubeletVersion: 'v1.35.5' } }, ...options,
})
const entry = (name, extra = {}) => ({
  node: node(name), roles: ['worker'], cpu: { capacity: 4, allocatable: 3.5, used: 0.7 }, memory: { capacity: 8 * 1024 ** 3, allocatable: 7 * 1024 ** 3, used: 2 * 1024 ** 3 },
  pods: { count: 5, capacity: 110, state: 'ok' }, hosts: [], ...extra,
})
const data = nodes => ({ cluster_id: 'cluster-a', nodes, hosts: [], metrics_state: 'ok', collected_at: timestamp })

function harness(initial) {
  const props = vue.reactive({ data: initial, loading: false, error: '', clusterName: 'NAS K3s', endpoint: '', disabled: false, selected: '' })
  const events = [], scope = vue.effectScope()
  const component = loadFrontend('../src/components/KubeNodeTopology.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {} },
    'vue-i18n': { useI18n: () => ({ t: (key, values = {}) => `${key} ${JSON.stringify(values)}` }) },
    '@/utils/kubernetesTopology': topology,
    '@/utils/time': { formatDateTime: value => value },
  }).default
  const state = scope.run(() => component.setup(props, { expose() {}, emit: (name, ...args) => events.push({ name, args }) }))
  state.now.value = at
  return { state, props, events, stop: () => scope.stop() }
}

test('节点资源使用真实用量和可分配容量，真实零值保留，超过100%的用量不被文本截断', () => {
  const current = entry('worker', { cpu: { capacity: 4, allocatable: 2, used: 3 }, memory: { capacity: 8, allocatable: 7, used: 0 } })
  const [cpu, memory, pods] = topology.topologyMeters(current, timestamp, false, at)
  assert.equal(cpu.percent, 150)
  assert.equal(cpu.width, 100)
  assert.equal(topology.topologyPercent(cpu), '150.0%')
  assert.equal(topology.topologyTone(cpu), 'danger')
  assert.equal(memory.used, 0)
  assert.equal(memory.percent, 0)
  assert.equal(topology.topologyAmount(memory.used, 'memory'), '0 MiB')
  assert.equal(pods.used, 5)
  assert.equal(pods.total, 110)
})

test('指标缺失、过期、未来时间、错误响应和无效数值均不伪造0', () => {
  const current = entry('worker', { cpu: { capacity: 4, allocatable: 3, used: null }, memory: { capacity: 8, allocatable: 7, used: -1 } })
  assert.equal(topology.topologyMeters(current, timestamp, false, at)[0].used, undefined)
  assert.equal(topology.topologyMeters(current, timestamp, false, at)[1].used, undefined)
  for (const [time, failed] of [[new Date(at - 120001).toISOString(), false], [new Date(at + 60001).toISOString(), false], ['invalid', false], [timestamp, true]]) {
    for (const meter of topology.topologyMeters(entry('worker'), time, failed, at)) {
      assert.equal(meter.used, undefined)
      assert.equal(meter.percent, undefined)
      assert.equal(topology.topologyAmount(meter.used, meter.key), '—')
      assert.equal(topology.topologyTone(meter), 'unknown')
    }
  }
})

test('CPU和内存按实际采样时间过期，空采样时间不沿用请求时间，Pod数量按快照时间保留', () => {
  for (const sampledAt of [null, 'invalid', new Date(at - 120001).toISOString(), new Date(at + 60001).toISOString()]) {
    const [cpu, memory, pods] = topology.topologyMeters(entry('worker', { metrics_timestamp: sampledAt }), timestamp, false, at)
    assert.equal(cpu.used, undefined)
    assert.equal(memory.used, undefined)
    assert.equal(cpu.total, 3.5)
    assert.equal(pods.used, 5)
  }
  for (const sampledAt of [timestamp, new Date(at - 120000).toISOString(), new Date(at + 60000).toISOString()]) {
    const [cpu, memory] = topology.topologyMeters(entry('worker', { metrics_timestamp: sampledAt, cpu: { capacity: 4, allocatable: 3.5, used: 0 } }), timestamp, false, at)
    assert.equal(cpu.used, 0)
    assert.equal(memory.used, 2 * 1024 ** 3)
  }
  const current = entry('worker', { metrics_timestamp: timestamp })
  assert.equal(topology.topologyMeters(current, new Date(at - 120001).toISOString(), false, at)[0].used, undefined)
  assert.equal(topology.topologyMeters(current, timestamp, true, at)[0].used, undefined)
  assert.equal(topology.topologyMeters(entry('legacy'), timestamp, false, at)[0].used, 0.7)
})

test('无Pod权限与没有Pod区分，缺少分母时保留实际用量但不展示百分比', () => {
  const hidden = entry('worker', { pods: { count: 0, capacity: 110, state: 'no_permission' }, cpu: { capacity: 4, allocatable: null, used: 1 } })
  const [cpu, , pods] = topology.topologyMeters(hidden, timestamp, false, at)
  assert.equal(cpu.used, 1)
  assert.equal(cpu.percent, undefined)
  assert.equal(cpu.state, 'missing_capacity')
  assert.equal(pods.used, undefined)
  assert.equal(pods.state, 'no_permission')
  const empty = topology.topologyMeters(entry('worker', { pods: { count: 0, capacity: 110, state: 'ok' } }), timestamp, false, at)[2]
  assert.equal(empty.used, 0)
  assert.equal(empty.percent, 0)
})

test('主机、IP、角色和节点名称可搜索，筛选不修改节点或资源数据', () => {
  const control = entry('server-a', { roles: ['control-plane'], hosts: [{ id: 'nas', name: 'NAS-24', address: '192.168.3.241', is_active: true }] })
  const worker = entry('agent-b', { node: node('agent-b', { status: { addresses: [{ type: 'InternalIP', address: '10.0.0.2' }] } }) })
  const fixture = harness(data([worker, control]))
  fixture.state.query.value = 'nas-24'
  assert.equal(fixture.state.visibleNodes.value[0].node.metadata.name, 'server-a')
  fixture.state.query.value = '10.0.0.2'
  assert.equal(fixture.state.visibleNodes.value[0].node.metadata.name, 'agent-b')
  fixture.state.query.value = 'master'
  assert.equal(fixture.state.visibleNodes.value[0].node.metadata.name, 'server-a')
  fixture.state.role.value = 'worker'
  assert.equal(fixture.state.visibleNodes.value.length, 0)
  fixture.state.clearFilters()
  assert.deepEqual(fixture.state.visibleNodes.value.map(item => item.node.metadata.name), ['agent-b', 'server-a'])
  assert.equal(control.cpu.used, 0.7)
  fixture.stop()
})

test('双角色节点只画一次，不把同名重复UID计成多个节点，状态未知不显示Ready', () => {
  const dual = entry('dual', { roles: ['control-plane', 'worker'] })
  const missing = entry('unknown', { node: node('unknown', { status: {} }) })
  const fixture = harness(data([dual, { ...dual }, missing]))
  assert.equal(fixture.state.nodes.value.length, 2)
  assert.equal(fixture.state.groups.value.reduce((total, group) => total + group.entries.length, 0), 2)
  assert.equal(fixture.state.ready.value, 1)
  fixture.state.role.value = 'worker'
  assert.equal(fixture.state.groups.value.flatMap(group => group.entries).filter(item => item.node.metadata.name === 'dual').length, 1)
  assert.equal(topology.topologyReady(missing), 'unknown')
  fixture.stop()
})

test('点击只传递真实节点详情对象，页面禁用或刷新中不触发重复操作', () => {
  const current = entry('worker'), fixture = harness(data([current]))
  fixture.state.select(current)
  assert.equal(fixture.events[0].name, 'select')
  assert.equal(fixture.events[0].args[0], current.node)
  fixture.props.disabled = true
  fixture.state.select(current)
  fixture.state.refresh()
  assert.equal(fixture.events.length, 1)
  fixture.props.disabled = false
  fixture.props.loading = true
  fixture.state.refresh()
  assert.equal(fixture.events.length, 1)
  fixture.props.loading = false
  fixture.state.refresh()
  assert.equal(fixture.events[1].name, 'refresh')
  fixture.stop()
})

test('切换集群清除旧筛选，旧或失败的指标仍显示节点但不沿用为实时用量', async () => {
  const fixture = harness(data([entry('worker')]))
  fixture.state.query.value = 'other'
  fixture.state.role.value = 'control-plane'
  fixture.props.data = { ...data([entry('worker-b')]), cluster_id: 'cluster-b' }
  await vue.nextTick()
  assert.equal(fixture.state.query.value, '')
  assert.equal(fixture.state.role.value, 'all')
  fixture.props.error = 'Node API unavailable'
  assert.equal(fixture.state.nodes.value.length, 1)
  assert.equal(fixture.state.meters(fixture.state.nodes.value[0])[0].used, undefined)
  fixture.props.error = ''
  fixture.state.now.value = at + 120001
  assert.equal(fixture.state.metricsStale.value, true)
  assert.equal(fixture.state.meters(fixture.state.nodes.value[0])[0].used, undefined)
  fixture.stop()
})

test('页面停留时各节点按自己的采样时间隐藏过期CPU和内存，不影响新采样节点及Pod数量', () => {
  const oldNode = entry('older', { metrics_timestamp: new Date(at - 110000).toISOString() })
  const newNode = entry('newer', { metrics_timestamp: timestamp })
  const fixture = harness(data([oldNode, newNode]))
  assert.equal(fixture.state.meters(oldNode)[0].used, 0.7)
  fixture.state.now.value = at + 10001
  assert.equal(fixture.state.meters(oldNode)[0].used, undefined)
  assert.equal(fixture.state.meters(oldNode)[1].used, undefined)
  assert.equal(fixture.state.meters(oldNode)[2].used, 5)
  assert.equal(fixture.state.meters(newNode)[0].used, 0.7)
  assert.equal(fixture.state.hasMissingMetrics.value, true)
  fixture.stop()
})
