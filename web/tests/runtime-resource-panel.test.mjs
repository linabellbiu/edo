import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import * as vue from 'vue'
import { parse, compileScript, compileStyle, compileTemplate } from '@vue/compiler-sfc'
import { loadFrontend } from './acceptanceHarness.mjs'

const helper = loadFrontend('../src/utils/runtimeResources.ts')
const settle = async () => { await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const fixtureRows = () => [
  { id: 'container-running', names: ['/api'], image_display: 'example/api:v1', state: 'running', status: 'Up 1 hour' },
  { id: 'container-stopped', names: ['/worker'], image_display: 'example/worker:v2', state: 'exited', status: 'Exited (0)' },
  { id: 'container-dead', names: ['/broken'], image_display: 'example/other:v1', state: 'dead', status: 'Dead' },
  { id: 'container-paused', names: ['/paused'], image_display: 'example/other:v1', state: 'paused', status: 'Up (Paused)' },
]
const host = (id = 'host-a', capabilities = [{ kind: 'docker', runtime_id: 'd1', status: 'ready' }]) => ({ id, name: id, is_active: true, capabilities })

function harness(options = {}) {
  const requests = [], notifications = [], events = [], mounts = [], unmounts = [], listeners = new Map()
  let project = 'project-a', stopped = false
  const auth = vue.reactive({ user: { id: 'operator', department_id: 'department-a', project_id: 'project-a', is_superuser: false, permissions: options.codes ?? ['cluster.read', 'terminal.open', 'cluster.execute'] } })
  auth.canAny = codes => Boolean(auth.user?.is_superuser || codes.some(code => auth.user?.permissions.includes(code)))
  const props = vue.reactive({ hosts: options.hosts ?? [host()], hostId: options.hostId ?? 'host-a', node: options.node ?? 'docker:d1' })
  const client = {
    async get(path, config) {
      const request = { method: 'get', path, config }; requests.push(request)
      if (options.get) { const result = options.get(path, config, request); if (result !== undefined) return result }
      if (path === '/docker/endpoints') return { data: { endpoints: options.endpoints ?? [{ id: 'd1', name: 'Docker one', is_active: true }] } }
      if (path === '/kubernetes/clusters') return { data: { clusters: options.clusters ?? [{ id: 'k1', name: 'Cluster', default_namespace: 'team', is_active: true }] } }
      if (path.includes('/containers?')) return { data: { containers: fixtureRows() } }
      if (path.includes('/pods?')) return { data: { pods: [{ id: 'pod-a', name: 'api', namespace: 'team', phase: 'Running', containers: ['api', 'sidecar'] }] } }
      throw new Error('unexpected request')
    },
    async post(path, body, config) { requests.push({ method: 'post', path, config }); return options.post ? options.post(path, body, config) : { data: {} } },
  }
  const component = loadFrontend('../src/components/RuntimeResourcePanel.vue', {
    vue: { ...vue, onMounted: callback => mounts.push(callback), onBeforeUnmount: callback => unmounts.push(callback) },
    'vue-i18n': { useI18n: () => ({ t: (key, values) => key + (values ? JSON.stringify(values) : ''), te: key => /^runtimeResources\.state_(running|exited|created|dead|paused|restarting|removing|pending|succeeded|failed|unknown)$/.test(key) }) },
    'ant-design-vue': { message: Object.fromEntries(['error', 'success', 'warning'].map(kind => [kind, value => notifications.push({ kind, value })])) },
    '@/api/client': { __esModule: true, default: client }, '@/api/resources': { apiErrorMessage: error => error.message },
    '@/stores/auth': { useAuthStore: () => auth }, '@/utils/runtimeResources': helper,
  }, {
    localStorage: { getItem: () => project },
    window: { addEventListener: (event, callback) => listeners.set(event, callback), removeEventListener: event => listeners.delete(event) },
  }).default
  const scope = vue.effectScope(); let state
  scope.run(() => { state = component.setup(props, { expose() {}, emit: (name, value) => { events.push([name, value]); if (options.syncNode !== false && name === 'update:node') props.node = value } }) })
  return { state, props, auth, requests, notifications, events, listeners, async mount() { mounts.forEach(callback => callback()); await settle() }, project(value, notify = true) { project = value; if (notify) listeners.get('edo:projects-changed')?.() }, stop() { if (stopped) return; stopped = true; unmounts.forEach(callback => callback()); scope.stop() } }
}

test('容器筛选只按真实状态分类，暂停/重启和未知不冒充运行或已停止，搜索不改变原记录', () => {
  const rows = [...fixtureRows(), { id: 'created', state: 'created' }, { id: 'restarting', state: 'restarting' }, { id: 'unknown' }]
  assert.deepEqual(helper.filterRuntimeContainers(rows, '', 'running').map(row => row.id), ['container-running'])
  assert.deepEqual(helper.filterRuntimeContainers(rows, '', 'stopped').map(row => row.id), ['container-stopped', 'container-dead'])
  assert.equal(helper.filterRuntimeContainers(rows, '', 'all').length, 7)
  assert.equal(helper.filterRuntimeContainers(rows, ' API:V1 ', 'all')[0], rows[0])
  assert.equal(helper.runtimeMatches({ namespace: 'Team', containers: ['Sidecar'] }, 'sidecar'), true)
  assert.equal(helper.runtimeContainerName(rows[0]), 'api'); assert.equal(helper.runtimeContainerName({ id: 'id' }), 'id')
  assert.equal(helper.runtimeStateTone(undefined), 'default'); assert.equal(helper.runtimeStateTone('Running'), 'success'); assert.equal(helper.runtimeStateTone('dead'), 'error')
})

test('打开单 Docker 运行时按需请求，仅显示运行中；筛选和搜索都不重复调用 API', async t => {
  const h = harness(); t.after(h.stop)
  assert.equal(h.requests.length, 0); assert.equal(h.state.resourceLoaded.value, false)
  await h.mount()
  assert.deepEqual(h.requests.map(request => request.path), ['/docker/endpoints', '/docker/endpoints/d1/containers?all=true'])
  assert.equal(h.state.containerFilter.value, 'running'); assert.equal(h.state.filteredContainers.value.length, 1)
  assert.deepEqual(h.state.containerCounts.value, { running: 1, stopped: 2, all: 4 })
  h.state.containerFilter.value = 'all'; h.state.search.value = 'worker'; await settle()
  assert.equal(h.state.filteredContainers.value[0].id, 'container-stopped'); assert.equal(h.requests.length, 2)
  h.state.search.value = ''; h.state.containerFilter.value = 'stopped'
  assert.equal(h.state.filteredContainers.value.length, 2); assert.equal(h.state.rowActionsDisabled.value, false)
})

test('单运行时 overview 直接显示资源，多运行时 overview 不批量拉取容器或 Pod', async t => {
  const single = harness({ node: 'overview' }); t.after(single.stop); await single.mount()
  assert.equal(single.state.selectedNode.value, 'docker:d1'); assert.equal(single.state.singleRuntime.value, true); assert.equal(single.state.resourceLoaded.value, true)
  const h = harness({ hostId: '', node: 'overview' }); t.after(h.stop); await h.mount()
  assert.deepEqual(h.requests.map(request => request.path), ['/docker/endpoints', '/kubernetes/clusters'])
  assert.equal(h.state.visibleRuntimeOptions.value.length, 2); assert.equal(h.state.resourceLoaded.value, false)
  h.state.choose('kubernetes:k1'); await settle()
  assert.equal(h.state.namespace.value, 'team'); assert.match(h.requests.at(-1).path, /kubernetes\/clusters\/k1\/pods\?namespace=team$/)
  assert.equal(h.requests.filter(request => request.path.includes('/containers?')).length, 0)
})

test('Pod 按命名空间加载，修改输入清理旧列表但不自动发请求；终端绑定当前 Pod 与容器', async t => {
  const h = harness({ hosts: [host('host-a', [{ kind: 'kubernetes', runtime_id: 'k1', status: 'ready' }])], node: 'kubernetes:k1' }); t.after(h.stop); await h.mount()
  const pod = h.state.pods.value[0]
  h.state.openPodTerminal(pod, 'not-present'); assert.equal(h.state.terminal.value.open, false)
  h.state.openPodTerminal(pod, 'sidecar'); assert.match(h.state.terminal.value.path, /\/kubernetes\/k1\/namespaces\/team\/pods\/api\/containers\/sidecar\/ws$/)
  h.state.namespace.value = 'prod'; assert.equal(h.state.pods.value.length, 0); assert.equal(h.state.terminal.value.open, false); assert.equal(h.requests.length, 2)
  h.state.openPodTerminal(pod, 'api'); assert.equal(h.state.terminal.value.open, false)
  await h.state.loadRuntime(); assert.match(h.requests.at(-1).path, /namespace=prod$/)
})

test('运行和停止容器都能看日志，终端只允许当前运行记录；旧行或伪造记录不能打开', async t => {
  const h = harness(); t.after(h.stop); await h.mount()
  const [running, stopped] = h.state.containers.value
  h.state.openContainerLogs(stopped); assert.match(h.state.containerLogs.value.path, /containers\/container-stopped\/logs\/ws$/)
  h.state.openDockerTerminal(stopped); assert.equal(h.state.terminal.value.open, false)
  h.state.openDockerTerminal(running); assert.match(h.state.terminal.value.path, /docker\/d1\/containers\/container-running\/ws$/)
  h.state.closeStreams(); h.state.openDockerTerminal({ ...running }); h.state.openContainerLogs({ ...stopped })
  assert.equal(h.state.terminal.value.open, false); assert.equal(h.state.containerLogs.value.open, false)
  await h.state.loadRuntime(); h.state.openDockerTerminal(running); assert.equal(h.state.terminal.value.open, false)
})

test('切换运行时取消旧请求，迟到响应不覆盖新列表或清理新加载状态', async t => {
  const old = deferred()
  const h = harness({ hosts: [host('host-a', [{ kind: 'docker', runtime_id: 'd1', status: 'ready' }, { kind: 'docker', runtime_id: 'd2', status: 'ready' }])], endpoints: [{ id: 'd1', name: 'one' }, { id: 'd2', name: 'two' }], get: path => path.includes('/d1/containers?') ? old.promise : path.includes('/d2/containers?') ? { data: { containers: [{ id: 'new', state: 'running' }] } } : undefined }); t.after(h.stop)
  await h.mount(); const oldRequest = h.requests.at(-1); assert.equal(h.state.resourceLoading.value, true)
  h.props.node = 'docker:d2'; await settle()
  assert.equal(oldRequest.config.signal.aborted, true); assert.equal(h.state.containers.value[0].id, 'new')
  old.resolve({ data: { containers: [{ id: 'stale', state: 'running' }] } }); await settle()
  assert.equal(h.state.containers.value[0].id, 'new'); assert.equal(h.state.resourceLoading.value, false)
})

test('无查看权限不请求，撤权立即清理列表与控制台，执行/终端权限独立生效', async t => {
  const denied = harness({ codes: [] }); t.after(denied.stop); await denied.mount(); await denied.state.refresh(); assert.deepEqual(denied.requests, [])
  const h = harness({ codes: ['cluster.read'] }); t.after(h.stop); await h.mount()
  h.state.openDockerTerminal(h.state.containers.value[0]); await h.state.ping(); assert.equal(h.state.terminal.value.open, false); assert.equal(h.requests.length, 2)
  h.state.openContainerLogs(h.state.containers.value[0]); assert.equal(h.state.containerLogs.value.open, true)
  h.auth.user.permissions = []; await settle()
  assert.equal(h.state.containers.value.length, 0); assert.equal(h.state.containerLogs.value.open, false); assert.equal(h.requests.length, 2)
})

test('身份/项目切换关闭控制台并取消旧元数据，卸载取消全部请求并释放事件监听', async t => {
  const first = deferred(); let metadataCalls = 0
  const h = harness({ get: path => path === '/docker/endpoints' && metadataCalls++ === 0 ? first.promise : undefined }); t.after(h.stop)
  await h.mount(); const old = h.requests[0]
  h.auth.user.id = 'another-user'; await settle()
  assert.equal(old.config.signal.aborted, true); assert.equal(h.state.containers.value.length, 4)
  first.resolve({ data: { endpoints: [{ id: 'stale', name: 'stale' }] } }); await settle()
  assert.equal(h.state.dockerEndpoints.value[0].id, 'd1')
  h.state.openContainerLogs(h.state.containers.value[0]); h.project('project-b'); await settle()
  assert.equal(h.state.containerLogs.value.open, false); assert.equal(h.listeners.size, 2)
  const pending = deferred(); const unload = harness({ get: path => path === '/docker/endpoints' ? pending.promise : undefined }); t.after(unload.stop)
  await unload.mount(); const request = unload.requests[0]; unload.stop()
  assert.equal(request.config.signal.aborted, true); assert.equal(unload.listeners.size, 0)
  pending.resolve({ data: { endpoints: [{ id: 'd1', name: 'late' }] } }); await settle()
  assert.equal(unload.state.metadataLoaded.value, false); assert.equal(unload.requests.length, 1); assert.deepEqual(unload.notifications, [])
})

test('同一时刻 localStorage 项目变化即拒绝旧行操作，不依赖异步 watcher 已执行', async t => {
  const h = harness(); t.after(h.stop); await h.mount()
  const row = h.state.containers.value[0]; assert.equal(h.state.rowActionsDisabled.value, false)
  h.project('other-project', false); h.state.openContainerLogs(row); h.state.openDockerTerminal(row)
  assert.equal(h.state.containerLogs.value.open, false); assert.equal(h.state.terminal.value.open, false)
})

test('部门身份变化取消旧资源请求并关闭控制台，同用户同项目的旧响应也不能落地', async t => {
  const pending = deferred(); let resourceCalls = 0
  const h = harness({ get: path => path.includes('/containers?') && ++resourceCalls === 2 ? pending.promise : undefined }); t.after(h.stop); await h.mount()
  const oldRow = h.state.containers.value[0]
  h.state.openContainerLogs(oldRow); h.state.openDockerTerminal(oldRow)
  const refresh = h.state.loadRuntime(), oldRequest = h.requests.at(-1)
  h.auth.user.department_id = 'department-b'; await settle()
  assert.equal(oldRequest.config.signal.aborted, true)
  assert.equal(h.state.containerLogs.value.open, false); assert.equal(h.state.terminal.value.open, false)
  h.state.openContainerLogs(oldRow); assert.equal(h.state.containerLogs.value.open, false)
  pending.resolve({ data: { containers: [{ id: 'wrong-department', state: 'running' }] } }); await refresh; await settle()
  assert.equal(h.state.containers.value[0].id, 'container-running'); assert.equal(h.state.resourceLoaded.value, true)
})

test('主机停用立即取消资源和连接检查、关流并拒绝新操作，不借运行时仍启用继续访问', async t => {
  const resourcePending = deferred(), pingPending = deferred(); let resourceCalls = 0
  const h = harness({ get: path => path.includes('/containers?') && ++resourceCalls === 2 ? resourcePending.promise : undefined, post: () => pingPending.promise }); t.after(h.stop); await h.mount()
  const row = h.state.containers.value[0]; h.state.openContainerLogs(row); h.state.openDockerTerminal(row)
  const reading = h.state.loadRuntime(), resourceRequest = h.requests.at(-1)
  const checking = h.state.ping(), pingRequest = h.requests.at(-1)
  h.props.hosts[0].is_active = false
  assert.equal(resourceRequest.config.signal.aborted, true); assert.equal(pingRequest.config.signal.aborted, true)
  assert.equal(h.state.hostEnabled.value, false); assert.equal(h.state.rowActionsDisabled.value, true)
  assert.equal(h.state.containers.value.length, 0); assert.equal(h.state.containerLogs.value.open, false); assert.equal(h.state.terminal.value.open, false)
  const count = h.requests.length
  await h.state.refresh(); await h.state.loadRuntime(); await h.state.ping(); h.state.choose('docker:d1'); h.state.openContainerLogs(row); h.state.openDockerTerminal(row)
  assert.equal(h.requests.length, count); assert.deepEqual(h.events, [])
  resourcePending.resolve({ data: { containers: [{ id: 'stale-after-disable', state: 'running' }] } }); pingPending.resolve({ data: {} }); await Promise.all([reading, checking])
  assert.equal(h.state.containers.value.length, 0); assert.deepEqual(h.notifications, [])
  h.props.hosts[0].is_active = true; await settle()
  assert.equal(h.state.resourceLoaded.value, true); assert.equal(h.state.containers.value[0].id, 'container-running')
  h.props.hosts = []; await settle()
  assert.equal(h.state.hostEnabled.value, false); assert.equal(h.state.containers.value.length, 0)
  const disabled = harness({ hosts: [{ ...host(), is_active: false }] }); t.after(disabled.stop); await disabled.mount()
  assert.deepEqual(disabled.requests, [])
})

test('主机停用取消尚未完成的运行时元数据，迟到元数据不触发资源请求', async t => {
  const pending = deferred(), h = harness({ get: path => path === '/docker/endpoints' ? pending.promise : undefined }); t.after(h.stop); await h.mount()
  const metadataRequest = h.requests[0]; h.props.hosts[0].is_active = false
  assert.equal(metadataRequest.config.signal.aborted, true)
  pending.resolve({ data: { endpoints: [{ id: 'd1', name: 'late', is_active: true }] } }); await settle()
  assert.equal(h.requests.length, 1); assert.equal(h.state.metadataLoaded.value, false); assert.equal(h.state.dockerEndpoints.value.length, 0)
})

test('刷新失败明确保留上次结果并禁用旧行操作，初次失败不伪造零样本', async t => {
  let resourceCalls = 0
  const h = harness({ get: path => path.includes('/containers?') && resourceCalls++ ? Promise.reject(Error('资源暂时不可用')) : undefined }); t.after(h.stop); await h.mount()
  const row = h.state.containers.value[0]; await h.state.loadRuntime()
  assert.equal(h.state.resourceLoaded.value, true); assert.equal(h.state.containers.value.length, 4); assert.equal(h.state.resourceError.value, '资源暂时不可用')
  h.state.openDockerTerminal(row); assert.equal(h.state.terminal.value.open, false)
  const first = harness({ get: path => path.includes('/containers?') ? Promise.reject(Error('未能读取')) : undefined }); t.after(first.stop); await first.mount()
  assert.equal(first.state.resourceLoaded.value, false); assert.equal(first.state.rowActionsDisabled.value, true)
})

test('停用运行时不拉资源、不 ping；失效深链接不静默换成另一个运行时', async t => {
  const h = harness({ endpoints: [{ id: 'd1', name: 'stopped', is_active: false }] }); t.after(h.stop); await h.mount(); await h.state.ping()
  assert.equal(h.requests.length, 1); assert.equal(h.state.resourceError.value, 'runtimeResources.disabled')
  const missing = harness({ node: 'docker:deleted' }); t.after(missing.stop); await missing.mount()
  assert.equal(missing.state.selectedRuntime.value, undefined); assert.equal(missing.requests.length, 1); assert.deepEqual(missing.events, [])
})

test('连接检查禁止双击，切换目标后迟到成功不显示为新目标检查通过', async t => {
  const pending = deferred(), h = harness({ post: () => pending.promise }); t.after(h.stop); await h.mount()
  const task = h.state.ping(); await h.state.ping()
  assert.equal(h.requests.filter(request => request.method === 'post').length, 1)
  h.props.node = 'overview'; pending.resolve({ data: {} }); await task
  assert.deepEqual(h.notifications, [])
})

test('模板/样式可编译：不再重复标题/概览说明，分页使用具名原生控件，保留10条默认', () => {
  const { descriptor } = parse(readFileSync(new URL('../src/components/RuntimeResourcePanel.vue', import.meta.url), 'utf8'))
  const script = compileScript(descriptor, { id: 'runtime-panel' })
  assert.deepEqual(compileTemplate({ source: descriptor.template.content, filename: 'RuntimeResourcePanel.vue', id: 'runtime-panel', compilerOptions: { bindingMetadata: script.bindings } }).errors, [])
  for (const style of descriptor.styles) assert.deepEqual(compileStyle({ source: style.content, filename: 'RuntimeResourcePanel.vue', id: 'runtime-panel', scoped: true }).errors, [])
  assert.doesNotMatch(descriptor.template.content, /<h[1-6]|返回资源概览|不会在后台|<a-select/)
  assert.equal((descriptor.template.content.match(/#pagination=/g) ?? []).length, 2)
  assert.equal((descriptor.template.content.match(/:aria-label="t\('runtimeResources.pageSize'\)"/g) ?? []).length, 2)
  assert.match(descriptor.template.content, /visibleRuntimeOptions.length > 1/)
  assert.match(descriptor.template.content, /:details="false" keep-header/)
  assert.match(descriptor.styles[0].content, /overflow-wrap:anywhere;white-space:normal/)
  assert.match(descriptor.template.content, /<div v-if="selectedDocker" class="runtime-resource-table">\s*<ResourceTable/)
  assert.match(descriptor.template.content, /<div v-else-if="selectedCluster" class="runtime-resource-table pod-resource-table">\s*<ResourceTable/)
  assert.doesNotMatch(descriptor.template.content, /<ResourceTable[^>]+class="runtime-resource-table/)
  const scopedCSS = compileStyle({ source: descriptor.styles[0].content, filename: 'RuntimeResourcePanel.vue', id: 'runtime-panel', scoped: true }).code
  assert.match(scopedCSS, /\.runtime-resource-table\[data-v-runtime-panel\] \.resource-table\{min-width:870px\}/)
  assert.match(scopedCSS, /@media\(max-width:760px\)\{\.runtime-resource-table\[data-v-runtime-panel\] \.table-actions,\.runtime-resource-table\[data-v-runtime-panel\] \.table-action-heading\{position:static\}\}/)
  assert.match(descriptor.styles[0].content, /tbody tr:hover>td\)\{background:color-mix\(in srgb,var\(--edo-primary\) 4%,var\(--edo-surface\)\)/)
  assert.match(readFileSync(new URL('../src/components/ResourceTable.vue', import.meta.url), 'utf8'), /pageSize = ref\(10\)/)
})
