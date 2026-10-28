import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript, compileTemplate, compileStyle } from '@vue/compiler-sfc'
import * as vue from 'vue'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'
import { createHostsPreviewData, createHostsPreviewAdapter, hostPreviewScenarios } from './fixtures/hosts-preview-data.mjs'

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')
const request = (adapter, url, extra = {}) => adapter({ method: 'get', url, ...extra })

function hostView(scenario = 'ready', query = {}) {
  const data = createHostsPreviewData(scenario), calls = [], adapter = createHostsPreviewAdapter(data, calls)
  const get = (url, config = {}) => request(adapter, url, config)
  const infrastructure = loadFrontend('../src/api/infrastructure.ts', { '@/api/client': { __esModule: true, default: { get } } })
  const list = loadFrontend('../src/utils/resourceList.ts', { vue, 'vue-i18n': { useI18n: () => ({ t: key => key }) } })
  const h = componentHarness('../src/views/HostView.vue', { codes: data.permissions, query, api: { get }, modules: { '@/api/infrastructure': infrastructure, '@/utils/resourceList': list, '@/utils/infrastructureRelations': loadFrontend('../src/utils/infrastructureRelations.ts'), '@/utils/time': loadFrontend('../src/utils/time.ts') } })
  return { ...h, data, calls }
}

test('主机夹具独立使用测试地址和关联ID，容量保留二进制字节及未知值', async () => {
  const data = createHostsPreviewData('ready', Date.parse('2026-10-28T03:00:00Z'))
  assert.equal(data.hosts.length, 3)
  assert.ok(data.hosts.filter(host => host.mode === 'ssh').every(host => host.address.startsWith('192.0.2.')))
  assert.equal(data.resources['preview-local'].memory_total_bytes, 16 * 1024 ** 3)
  assert.equal(data.resources['preview-edge'].disk_total_bytes, null)
  assert.equal(data.resources['preview-offline'].state, 'unavailable')
  assert.equal(data.resources['preview-offline'].cpu_cores, null)
  const adapter = createHostsPreviewAdapter(data)
  const { data: response } = await request(adapter, '/hosts/preview-edge/resources')
  assert.deepEqual(response.resources, data.resources['preview-edge'])
  assert.equal(data.hosts[1].capabilities.find(capability => capability.kind === 'kubernetes').runtime_id, data.clusters[0].id)
  assert.equal(data.clusters[0].hosts[0].id, data.hosts[1].id)
  assert.deepEqual(data.topology.hosts[0].node_uids, [data.topology.nodes[0].node.metadata.uid])
  assert.equal('password' in data.hosts[1], false)
})

test('预览适配器只读且不透传未知路径、外部接口或取消的请求；返回副本不污染其他场景', async () => {
  const data = createHostsPreviewData(), requests = [], adapter = createHostsPreviewAdapter(data, requests)
  for (const method of ['post', 'put', 'patch', 'delete']) await assert.rejects(request(adapter, '/hosts', { method }), /不提交主机或运行资源变更/)
  await assert.rejects(request(adapter, '/unknown'), /未登记此只读接口/)
  await assert.rejects(request(adapter, 'https://192.0.2.41/hosts'), /不连接外部接口/)
  await assert.rejects(request(adapter, '/hosts/unknown/resources'), /预览主机不存在/)
  await assert.rejects(request(adapter, '/docker/endpoints/unknown/containers'), /运行时不存在/)
  const controller = new AbortController(); controller.abort()
  const count = requests.length
  await assert.rejects(request(adapter, '/hosts', { signal: controller.signal }), { name: 'AbortError' })
  assert.equal(requests.length, count)
  const response = await request(adapter, '/hosts')
  response.data.hosts[0].name = '不得污染原始夹具'
  assert.equal(data.hosts[0].name, '本地')
  assert.equal(createHostsPreviewData().hosts[0].name, '本地')
})

test('Docker 默认只返回运行中，全部模式包含已停止和重启中；空数据与失败保持不同', async () => {
  const adapter = createHostsPreviewAdapter()
  const running = await request(adapter, '/docker/endpoints/preview-docker-local/containers')
  assert.equal(running.data.containers.length, 2)
  assert.ok(running.data.containers.every(container => container.state === 'running'))
  const all = await request(adapter, '/docker/endpoints/preview-docker-local/containers?all=true')
  assert.deepEqual(all.data.containers.map(container => container.state), ['running', 'running', 'exited', 'restarting'])
  const query = await request(adapter, '/docker/endpoints/preview-docker-local/containers', { params: { all: true } })
  assert.equal(query.data.containers.length, 4)
  const empty = await request(createHostsPreviewAdapter(createHostsPreviewData('docker-empty')), '/docker/endpoints/preview-docker-local/containers')
  assert.deepEqual(empty.data.containers, [])
  await assert.rejects(request(createHostsPreviewAdapter(createHostsPreviewData('docker-error')), '/docker/endpoints/preview-docker-local/containers'), /容器加载失败/)
})

test('空、失败、只读与无资源权限是独立验收场景，不用管理员模拟权限', async () => {
  assert.ok(hostPreviewScenarios.includes('metadata-error'))
  const empty = createHostsPreviewData('empty')
  for (const key of ['hosts', 'environments', 'clusters', 'endpoints', 'containers']) assert.deepEqual(empty[key], [])
  const failed = createHostsPreviewAdapter(createHostsPreviewData('error'))
  await assert.rejects(request(failed, '/hosts'), /主机列表暂不可用/)
  assert.equal((await request(failed, '/kubernetes/clusters')).data.clusters.length, 1)
  const metadata = createHostsPreviewAdapter(createHostsPreviewData('metadata-error'))
  await assert.rejects(request(metadata, '/docker/endpoints'), /运行时列表暂不可用/)
  const readonly = createHostsPreviewData('readonly')
  assert.ok(readonly.permissions.includes('cluster.read'))
  assert.ok(readonly.permissions.every(code => !/\.(create|update|delete|execute|open)$/.test(code)))
  assert.equal(createHostsPreviewData('no-resources').permissions.includes('cluster.read'), false)
  assert.equal(createHostsPreviewData('unknown').scenario, 'ready')
})

test('夹具使用真实HostView而不是复制页面，不注册生产路由且所有原生网络通道受限', () => {
  const entry = read('./fixtures/hosts-workspace.mjs')
  assert.match(entry, /import HostView from '\/src\/views\/HostView.vue'/)
  assert.match(entry, /component: HostView/)
  assert.match(entry, /client.defaults.adapter = createHostsPreviewAdapter\(data\)/)
  assert.match(entry, /url.origin !== location.origin \|\| url.pathname.startsWith\('\/api\/'\)/)
  assert.match(entry, /window.WebSocket = class PreviewWebSocket/)
  assert.match(entry, /不连接真实日志或终端/)
  assert.match(entry, /is_superuser: false/)
  assert.doesNotMatch(read('../src/router/index.ts'), /hosts-workspace|HostWorkspacePreview|hosts-preview-data/)
  assert.doesNotMatch(read('../vite.config.ts'), /hosts-workspace|HostWorkspacePreview/)
  const html = read('./fixtures/hosts-workspace.html')
  assert.match(html, /width=device-width, initial-scale=1/)
  assert.match(html, /本地预览/)
})

test('主机预览外壳可独立编译，提供浅深主题、场景选择及375px可收缩布局', () => {
  const { descriptor, errors } = parse(read('./fixtures/HostWorkspacePreview.vue'))
  assert.deepEqual(errors, [])
  compileScript(descriptor, { id: 'hosts-preview-test' })
  assert.deepEqual(compileTemplate({ id: 'hosts-preview-test', source: descriptor.template.content }).errors, [])
  assert.deepEqual(compileStyle({ id: 'hosts-preview-test', source: descriptor.styles[0].content, scoped: true }).errors, [])
  assert.match(descriptor.template.content, /theme.darkAlgorithm : theme.defaultAlgorithm/)
  assert.match(descriptor.template.content, /验收场景<select/)
  assert.match(descriptor.styles[0].content, /@media\(max-width:640px\)/)
  assert.match(descriptor.styles[0].content, /min-width:0/)
})

test('真实HostView的Docker入口直接选择该主机运行时，不再要求先查看详情；离线目标不触发', async t => {
  const h = hostView(); t.after(h.stop)
  await h.state.refresh()
  const local = h.state.hosts.value.find(host => host.id === 'preview-local')
  h.state.openRuntime(local, 'docker')
  assert.equal(h.state.detailOpen.value, true)
  assert.equal(h.state.detailTab.value, 'resources')
  assert.equal(h.state.resourceHostID.value, local.id)
  assert.equal(h.state.resourceNode.value, 'docker:preview-docker-local')
  assert.deepEqual(h.replacements.at(-1), { path: '/hosts', query: { host: local.id, view: 'resources', node: 'docker:preview-docker-local' } })
  const count = h.replacements.length
  h.state.openRuntime(h.state.hosts.value.find(host => host.id === 'preview-offline'), 'docker')
  h.state.openRuntime({ ...local, id: 'missing-host' }, 'docker')
  assert.equal(h.replacements.length, count)
  assert.ok(h.calls.every(call => call.method === 'get'))
})

test('真实HostView支持资源深链，失效运行时不换目标，无资源权限不开放Docker入口', async t => {
  const direct = hostView('ready', { host: 'preview-local', view: 'resources', node: 'docker:preview-docker-local' }); t.after(direct.stop)
  await direct.state.refresh()
  assert.equal(direct.state.selectedID.value, 'preview-local')
  assert.equal(direct.state.resourceNode.value, 'docker:preview-docker-local')
  assert.equal(direct.state.detailTab.value, 'resources')
  const invalid = hostView('ready', { host: 'preview-local', view: 'resources', node: 'docker:missing-runtime' }); t.after(invalid.stop)
  await invalid.state.refresh()
  assert.equal(invalid.state.selectedID.value, '')
  assert.equal(invalid.state.resourceHostID.value, 'preview-local')
  assert.equal(invalid.state.resourceNode.value, 'docker:missing-runtime')
  assert.equal(invalid.replacements.length, 0)
  const restricted = hostView('no-resources'); t.after(restricted.stop)
  await restricted.state.refresh()
  restricted.state.openRuntime(restricted.state.hosts.value[0], 'docker')
  assert.equal(restricted.state.detailOpen.value, false)
  assert.equal(restricted.replacements.length, 0)
  assert.equal(restricted.calls.some(call => call.path === '/docker/endpoints'), false)
})

test('指定主机的深链不因错绑或主机缺失退化为全部运行时，其他主机真实存在的endpoint也不能打开', async t => {
  for (const query of [
    { host: 'preview-local', view: 'resources', node: 'docker:preview-docker-edge' },
    { host: 'preview-missing-host', view: 'resources', node: 'docker:preview-docker-local' },
  ]) {
    const h = hostView('ready', query); t.after(h.stop)
    await h.state.refresh()
    assert.equal(h.state.selectedID.value, '')
    assert.equal(h.state.resourceHostID.value, query.host)
    assert.equal(h.state.resourceNode.value, query.node)
    assert.equal(h.replacements.length, 0)
    assert.equal(h.route.query.host, query.host)
    const panel = componentHarness('../src/components/RuntimeResourcePanel.vue', { codes: ['cluster.read'], props: { hosts: h.state.hosts.value, hostId: h.state.resourceHostID.value, node: h.state.resourceNode.value }, modules: { '@/utils/runtimeResources': loadFrontend('../src/utils/runtimeResources.ts') } }); t.after(panel.stop)
    panel.state.dockerEndpoints.value = h.data.endpoints
    panel.state.kubernetesClusters.value = h.data.clusters
    assert.ok(panel.state.allRuntimeOptions.value.some(runtime => runtime.key === query.node))
    assert.equal(panel.state.selectedRuntime.value, undefined)
    assert.equal(panel.state.visibleRuntimeOptions.value.some(runtime => runtime.key === query.node), false)
    await panel.state.loadRuntime()
    assert.equal(panel.requests.length, 0)
    assert.deepEqual(panel.state.containers.value, [])
    assert.equal(panel.events.length, 0)
  }
})

test('真实HostView读取失败清空列表并保留可重试错误，不把失败状态展示为空数据成功', async t => {
  const failed = hostView('error'); t.after(failed.stop)
  await failed.state.refresh()
  assert.deepEqual(failed.state.hosts.value, [])
  assert.match(failed.state.hostLoadError.value, /主机列表暂不可用/)
  assert.equal(failed.state.clusters.value.length, 1)
  const empty = hostView('empty'); t.after(empty.stop)
  await empty.state.refresh()
  assert.deepEqual(empty.state.hosts.value, [])
  assert.equal(empty.state.hostLoadError.value, '')
})
