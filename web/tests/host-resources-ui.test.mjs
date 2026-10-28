import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import * as vue from 'vue'
import ts from 'typescript'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'
import { createHostsPreviewAdapter, createHostsPreviewData } from './fixtures/hosts-preview-data.mjs'

const flush = async () => { await vue.nextTick(); await Promise.resolve(); await vue.nextTick() }
const formatting = loadFrontend('../src/utils/hostResources.ts')
const record = (patch = {}) => ({ state: 'ok', source: 'ssh', scope: 'current_environment', cpu_cores: 4, memory_total_bytes: 8 * 1024 ** 3, disk_total_bytes: 256 * 1024 ** 3, disk_mount: '/', os: 'Debian GNU/Linux 12', collected_at: '2026-10-28T03:00:00Z', ...patch })

function summary({ host = createHostsPreviewData().hosts[1], codes = ['cluster.read'], user = {} } = {}) {
  const requests = [], cleanups = []
  const props = vue.reactive({ host: structuredClone(host), revision: 0 })
  const auth = vue.reactive({ user: { id: 'reader', project_id: 'project-a', is_superuser: false, permissions: [...codes], ...user }, canAny(required) { return this.user.is_superuser || required.some(code => this.user.permissions.includes(code)) } })
  const component = loadFrontend('../src/components/HostResourceSummary.vue', {
    vue: { ...vue, onBeforeUnmount: callback => cleanups.push(callback) },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/hostResources': formatting,
    '@/api/hostResources': { getHostResources: (id, signal) => new Promise((resolve, reject) => requests.push({ id, signal, resolve, reject })) },
  }).default
  const scope = vue.effectScope()
  let state, stopped = false
  scope.run(() => { state = component.setup(props, { expose() {} }) })
  return { state, props, auth, requests, async resolve(index, value = record()) { requests[index].resolve(value); await flush() }, async reject(index) { requests[index].reject(new Error('内部连接详情不得进入UI')); await flush() }, stop() { if (stopped) return; stopped = true; cleanups.forEach(callback => callback()); scope.stop() } }
}

function hostView(codes, query = {}) {
  const data = createHostsPreviewData(), adapter = createHostsPreviewAdapter(data)
  const get = (url, config = {}) => adapter({ method: 'get', url, ...config })
  const infrastructure = loadFrontend('../src/api/infrastructure.ts', { '@/api/client': { __esModule: true, default: { get } } })
  const list = loadFrontend('../src/utils/resourceList.ts', { vue, 'vue-i18n': { useI18n: () => ({ t: key => key }) } })
  return componentHarness('../src/views/HostView.vue', { codes, query, api: { get }, modules: { '@/api/infrastructure': infrastructure, '@/utils/resourceList': list, '@/utils/infrastructureRelations': loadFrontend('../src/utils/infrastructureRelations.ts'), '@/utils/time': loadFrontend('../src/utils/time.ts') } })
}

async function renderSummary(resource, { allowed = true, active = true } = {}) {
  const source = readFileSync(new URL('../src/components/HostResourceSummary.vue', import.meta.url), 'utf8')
  const code = compileScript(parse(source).descriptor, { id: 'host-summary-render', inlineTemplate: true }).content
  const output = ts.transpileModule(code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }, instance = { user: { id: 'reader', permissions: allowed ? ['cluster.read'] : [] }, canAny: () => allowed }
  const icon = { setup: (_props, { attrs }) => () => vue.h('svg', attrs) }
  // SSR 本身不等待 watch 的异步返回；预取桥仅等待真实组件这次已发起的采集，不更改模板或状态。
  const runtime = { ...vue, watch(source, callback, options) { return vue.watch(source, (...args) => { const pending = callback(...args); if (pending?.then) vue.onServerPrefetch(() => pending); return pending }, options) } }
  new Function('require', 'module', 'exports', output)(name => ({ vue: runtime, 'lucide-vue-next': { Cpu: icon, HardDrive: icon, MemoryStick: icon }, '@/stores/auth': { useAuthStore: () => instance }, '@/utils/hostResources': formatting, '@/api/hostResources': { async getHostResources() { if (!allowed) throw new Error('无权限不能请求'); return resource } } }[name] || {}), module, module.exports)
  return renderToString(vue.createSSRApp(module.exports.default, { host: { ...createHostsPreviewData().hosts[0], is_active: active } }))
}

function inventoryReads() {
  const requests = [], notifications = [], cleanups = [], replacements = []
  const auth = vue.reactive({ user: { id: 'reader', project_id: 'project-a', department_id: 'department-a', is_superuser: false, permissions: ['cluster.read', 'deployment.read'] }, canAny(required) { return this.user.is_superuser || required.some(code => this.user.permissions.includes(code)) } })
  let clock = 0, stopped = false
  class ControlledDate extends Date { static now() { return clock } }
  const get = (path, options = {}) => new Promise((resolve, reject) => requests.push({ path, options, resolve, reject }))
  const infrastructure = loadFrontend('../src/api/infrastructure.ts', { '@/api/client': { __esModule: true, default: { get } } })
  const list = loadFrontend('../src/utils/resourceList.ts', { vue, 'vue-i18n': { useI18n: () => ({ t: key => key }) } })
  const component = loadFrontend('../src/views/HostView.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount: callback => cleanups.push(callback) },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    'vue-router': { useRoute: () => ({ path: '/hosts', query: {} }), useRouter: () => ({ replace: route => replacements.push(route) }) },
    'ant-design-vue': { Modal: { confirm() {} }, message: Object.fromEntries(['error', 'success', 'warning', 'info'].map(kind => [kind, value => notifications.push({ kind, value })])) },
    '@/stores/auth': { useAuthStore: () => auth }, '@/api/client': { __esModule: true, default: { get } },
    '@/api/infrastructure': infrastructure, '@/utils/resourceList': list,
    '@/api/resources': { apiErrorMessage: () => '读取失败，请重试' },
    '@/utils/infrastructureRelations': loadFrontend('../src/utils/infrastructureRelations.ts'), '@/utils/time': loadFrontend('../src/utils/time.ts'),
  }, { Date: ControlledDate, document: { hidden: false, removeEventListener() {} }, window: { removeEventListener() {}, clearInterval() {} }, localStorage: { getItem: () => 'project-a' } }).default
  const scope = vue.effectScope(); let state
  scope.run(() => { state = component.setup({}, { expose() {} }) })
  return { state, auth, requests, notifications, replacements, tick: value => { clock = value }, async inventory(batch, data = createHostsPreviewData()) { for (const item of batch) item.resolve({ data: item.path === '/hosts' ? { hosts: data.hosts } : item.path === '/environments' ? { environments: data.environments } : { clusters: data.clusters } }); await flush() }, stop() { if (stopped) return; stopped = true; cleanups.forEach(callback => callback()); scope.stop() } }
}

test('主机资源API编码精确主机ID、传递取消信号，并读取resources包装而不是其他主机数据', async () => {
  const calls = [], controller = new AbortController()
  const api = loadFrontend('../src/api/hostResources.ts', { '@/api/client': { __esModule: true, default: { async get(...args) { calls.push(args); return { data: { resources: record() } } } } } })
  assert.deepEqual(await api.getHostResources('host/a b', controller.signal), record())
  assert.deepEqual(calls, [['/hosts/host%2Fa%20b/resources', { signal: controller.signal, timeout: 15_000 }]])
})

test('摘要只允许主机查看权限读取资源，空ID或停用主机不发请求', async t => {
  for (const codes of [[], ['terminal.open'], ['kubernetes.node.read']]) {
    const h = summary({ codes }); t.after(h.stop)
    assert.equal(h.requests.length, 0); assert.equal(h.state.status.value, '无查看权限')
  }
  for (const codes of [['cluster.read'], ['deployment.read']]) {
    const h = summary({ codes }); t.after(h.stop)
    assert.equal(h.requests.length, 1); assert.equal(h.state.status.value, '读取中')
  }
  const inactive = summary({ host: { ...createHostsPreviewData().hosts[1], is_active: false } }); t.after(inactive.stop)
  assert.equal(inactive.requests.length, 0); assert.equal(inactive.state.status.value, '已停用')
  const missing = summary({ host: { ...createHostsPreviewData().hosts[1], id: '' } }); t.after(missing.stop)
  assert.equal(missing.requests.length, 0)
})

test('权限撤销立即清理并取消，旧成功不能回填；恢复后只接受新请求', async t => {
  const h = summary(); t.after(h.stop)
  await h.resolve(0)
  assert.equal(h.state.available.value.cpu_cores, 4)
  const pending = h.state.load()
  h.auth.user.permissions = []
  await flush()
  assert.equal(h.requests[1].signal.aborted, true)
  assert.equal(h.state.data.value, undefined)
  assert.equal(h.state.status.value, '无查看权限')
  h.auth.user.permissions = ['cluster.read']
  await flush()
  assert.equal(h.requests.length, 3)
  await h.resolve(1, record({ cpu_cores: 64 })); await pending
  assert.equal(h.state.data.value, undefined); assert.equal(h.state.loading.value, true)
  await h.resolve(2, record({ cpu_cores: 8 }))
  assert.equal(h.state.available.value.cpu_cores, 8)
})

test('同名主机身份切换取消旧请求，迟到成功或失败不污染新主机或加载状态', async t => {
  const h = summary(); t.after(h.stop)
  h.props.host = { ...h.props.host, id: 'other-host', address: '192.0.2.99' }
  await flush()
  assert.equal(h.requests[0].signal.aborted, true)
  assert.equal(h.requests[1].id, 'other-host')
  await h.resolve(0, record({ cpu_cores: 64 }))
  assert.equal(h.state.data.value, undefined); assert.equal(h.state.loading.value, true)
  await h.resolve(1, record({ cpu_cores: 8 }))
  const pending = h.state.load()
  h.props.host.ssh_host_key_fingerprint = 'SHA256:changed-host-identity'
  await flush()
  assert.equal(h.requests[2].signal.aborted, true); assert.equal(h.requests.length, 4)
  await h.reject(2); await pending
  assert.equal(h.state.failed.value, false); assert.equal(h.state.loading.value, true)
  await h.resolve(3, record({ cpu_cores: 12 }))
  assert.equal(h.state.available.value.cpu_cores, 12)
})

test('用户、项目或部门切换冻结新的权限范围，原请求即使同主机ID也不能回填', async t => {
  const h = summary(); t.after(h.stop)
  h.auth.user.project_id = 'project-b'; await flush()
  assert.equal(h.requests[0].signal.aborted, true); assert.equal(h.requests.length, 2)
  h.auth.user.id = 'other-reader'; await flush()
  assert.equal(h.requests[1].signal.aborted, true); assert.equal(h.requests.length, 3)
  h.auth.user.department_id = 'other-department'; await flush()
  assert.equal(h.requests[2].signal.aborted, true); assert.equal(h.requests.length, 4)
  await h.resolve(0); await h.reject(1); await h.resolve(2, record({ cpu_cores: 64 }))
  assert.equal(h.state.data.value, undefined); assert.equal(h.state.failed.value, false)
  assert.equal(h.state.loading.value, true)
  await h.resolve(3, record({ cpu_cores: 16 }))
  assert.equal(h.state.available.value.cpu_cores, 16)
})

test('每五秒状态clone不重采集，手动revision刷新才重读并拒绝被替换请求', async t => {
  const h = summary(); t.after(h.stop)
  await h.resolve(0)
  for (let index = 0; index < 5; index++) {
    h.props.host = { ...h.props.host, capabilities: h.props.host.capabilities.map(capability => ({ ...capability, status: index % 2 ? 'ready' : 'unchecked' })) }
    await flush()
  }
  assert.equal(h.requests.length, 1)
  h.props.revision++; await flush()
  assert.equal(h.requests.length, 2); assert.equal(h.state.data.value, undefined)
  h.props.revision++; await flush()
  assert.equal(h.requests[1].signal.aborted, true)
  await h.resolve(1, record({ cpu_cores: 64 }))
  assert.equal(h.state.data.value, undefined); assert.equal(h.state.loading.value, true)
  await h.resolve(2, record({ cpu_cores: 8 }))
  assert.equal(h.state.available.value.cpu_cores, 8)
})

test('卸载或停用取消采集，卸载后迟到响应不写回，重新启用重新采集', async t => {
  const h = summary(); t.after(h.stop)
  h.props.host.is_active = false; await flush()
  assert.equal(h.requests[0].signal.aborted, true)
  await h.resolve(0); assert.equal(h.state.data.value, undefined)
  h.props.host.is_active = true; await flush(); assert.equal(h.requests.length, 2)
  h.stop(); assert.equal(h.requests[1].signal.aborted, true)
  await h.resolve(1); assert.equal(h.state.data.value, undefined)
})

test('停用主机重新启用仍重核权限，撤权时不因is_active变化恢复采集', async t => {
  const h = summary({ host: { ...createHostsPreviewData().hosts[1], is_active: false } }); t.after(h.stop)
  h.auth.user.permissions = []; await flush()
  h.props.host.is_active = true; await flush()
  assert.equal(h.requests.length, 0); assert.equal(h.state.status.value, '无查看权限')
  h.auth.user.permissions = ['deployment.read']; await flush()
  assert.equal(h.requests.length, 1)
  await h.resolve(0)
  assert.equal(h.state.available.value.cpu_cores, 4)
})

test('失败为稳定可重试提示，不泄露内部错误，未知和unavailable不冒充零值', async t => {
  const h = summary(); t.after(h.stop)
  await h.reject(0)
  assert.equal(h.state.failed.value, true); assert.equal(h.state.status.value, '未取得资源')
  const retry = h.state.load(); assert.equal(h.state.status.value, '读取中')
  await h.resolve(1, record({ state: 'unavailable', cpu_cores: null, memory_total_bytes: null, disk_total_bytes: null })); await retry
  assert.equal(h.state.available.value, undefined); assert.equal(h.state.status.value, '未取得资源')
  assert.equal(formatting.formatHostCPU(null), '—')
  assert.equal(formatting.formatHostCapacity(null), '—')
  assert.equal(formatting.formatHostCPU(Number.NaN), '—')
  assert.equal(formatting.formatHostCapacity(-1), '—')
  assert.equal(formatting.formatHostCapacity(16 * 1024 ** 3), '16 GiB')
  assert.equal(formatting.formatHostCapacity(256 * 1024 ** 2), '256 MiB')
  assert.notEqual(formatting.formatHostCapacity(1), '0 MiB')
})

test('部分可用的主机资源保留NULL，不丢弃已知CPU，也不把缺失容量转成零', async t => {
  const h = summary(); t.after(h.stop)
  await h.resolve(0, record({ cpu_cores: 4, memory_total_bytes: null, disk_total_bytes: null }))
  assert.equal(h.state.status.value, '')
  assert.equal(h.state.available.value.cpu_cores, 4)
  assert.equal(h.state.available.value.memory_total_bytes, null)
  assert.equal(h.state.available.value.disk_total_bytes, null)
  assert.equal(formatting.formatHostCPU(h.state.available.value.cpu_cores), '4 核')
  assert.equal(formatting.formatHostCapacity(h.state.available.value.memory_total_bytes), '—')
})

test('真实摘要渲染容量标签、二进制单位与缺失破折号，装饰图标不重复朗读', async () => {
  const html = await renderSummary(undefined, { allowed: false })
  assert.match(html, /CPU/); assert.match(html, /内存/); assert.match(html, /根目录/)
  assert.equal((html.match(/<strong>—<\/strong>/g) || []).length, 3)
  assert.equal((html.match(/aria-hidden="true"/g) || []).length, 3)
  assert.match(html, /无查看权限/)
  assert.doesNotMatch(html, /0 GiB|0 MiB|内部连接详情/)
  const known = await renderSummary(record({ memory_total_bytes: 8 * 1024 ** 3, disk_total_bytes: null }))
  assert.match(known, /4 核/); assert.match(known, /8 GiB/)
  assert.equal((known.match(/<strong>—<\/strong>/g) || []).length, 1)
})

test('读取成功但state不可用也渲染重试入口；无权限或停用不提供采集按钮', async () => {
  const unavailable = record({ state: 'unavailable', cpu_cores: null, memory_total_bytes: null, disk_total_bytes: null })
  const html = await renderSummary(unavailable)
  assert.match(html, /未取得资源/)
  assert.match(html, /<button type="button">重试<\/button>/)
  assert.equal((html.match(/<strong>—<\/strong>/g) || []).length, 3)
  assert.doesNotMatch(await renderSummary(unavailable, { allowed: false }), />重试<\/button>/)
  assert.doesNotMatch(await renderSummary(unavailable, { active: false }), />重试<\/button>/)
})

test('HostView Docker图标精确打开所属运行时，不跳到集群且保留现有查询', async t => {
  const h = hostView(['cluster.read', 'deployment.read'], { retained: 'filter-state' }); t.after(h.stop)
  await h.state.refresh()
  h.state.openRuntime(h.state.hosts.value.find(host => host.id === 'preview-edge'), 'docker')
  assert.deepEqual(h.replacements.at(-1), { path: '/hosts', query: { retained: 'filter-state', host: 'preview-edge', view: 'resources', node: 'docker:preview-docker-edge' } })
  assert.equal(h.state.resourceHostID.value, 'preview-edge')
  assert.equal(h.state.resourceNode.value, 'docker:preview-docker-edge')
  assert.equal(h.state.detailTab.value, 'resources')
  const template = parse(readFileSync(new URL('../src/views/HostView.vue', import.meta.url), 'utf8')).descriptor.template.content
  assert.match(template, /<ResourceDetailDrawer[^>]*:title="selected \? selected.name : '运行资源'"/)
})

test('Docker快捷入口无白色底框，保留原生按钮、可访问名称及键盘焦点', () => {
  const { descriptor } = parse(readFileSync(new URL('../src/views/HostView.vue', import.meta.url), 'utf8'))
  assert.match(descriptor.template.content, /<button[^>]*class="capability-shortcut"[^>]*:aria-label=/s)
  const css = descriptor.styles.map(style => style.content).join('\n')
  assert.match(css, /\.capability-icons>\.capability-shortcut\{[^}]*border:0;[^}]*background:transparent;/)
  assert.match(css, /\.capability-icons>\.capability-shortcut:focus-visible\{[^}]*outline:2px solid/)
})

test('HostView Docker入口拒绝权限不足、已停用、列表错误、无绑定及已移除主机', async t => {
  const restricted = hostView(['deployment.read']); t.after(restricted.stop)
  await restricted.state.refresh(); restricted.state.openRuntime(restricted.state.hosts.value[0], 'docker')
  assert.equal(restricted.replacements.length, 0)
  const h = hostView(['cluster.read']); t.after(h.stop); await h.state.refresh()
  const valid = h.state.hosts.value[0]
  h.state.openRuntime(h.state.hosts.value.find(host => !host.is_active), 'docker')
  h.state.openRuntime({ ...valid, id: 'removed-host' }, 'docker')
  h.state.openRuntime({ ...valid, capabilities: [] }, 'docker')
  h.state.hostLoadError.value = '读取失败'; h.state.openRuntime(valid, 'docker')
  assert.equal(h.replacements.length, 0)
  assert.equal(h.state.detailOpen.value, false)
})

test('主机同ID换绑运行时或被停用后，旧Docker点击快照不能打开旧目标或自动换新目标', async t => {
  const h = hostView(['cluster.read']); t.after(h.stop); await h.state.refresh()
  const previous = structuredClone(vue.toRaw(h.state.hosts.value[0]))
  h.state.hosts.value = h.state.hosts.value.map(host => host.id === previous.id ? { ...host, capabilities: host.capabilities.map(capability => capability.kind === 'docker' ? { ...capability, runtime_id: 'replacement-runtime' } : capability) } : host)
  h.state.openRuntime(previous, 'docker')
  assert.equal(h.replacements.length, 0); assert.equal(h.state.detailOpen.value, false)
  const current = JSON.parse(JSON.stringify(h.state.hosts.value[0]))
  h.state.hosts.value = h.state.hosts.value.map(host => host.id === current.id ? { ...host, is_active: false } : host)
  h.state.openRuntime(current, 'docker')
  assert.equal(h.replacements.length, 0); assert.equal(h.state.detailOpen.value, false)
})

test('HostView列表撤权取消在途请求并清理抽屉，迟到成功不能恢复旧主机数据', async t => {
  const h = inventoryReads(); t.after(h.stop)
  const pending = h.state.refresh(), old = h.requests.slice()
  assert.equal(old.length, 3)
  h.state.detailOpen.value = true; h.state.relationOpen.value = true; h.state.drawerOpen.value = true; h.state.groupDrawerOpen.value = true
  h.auth.user.permissions = []; await flush()
  assert.ok(old.every(request => request.options.signal.aborted))
  assert.deepEqual(h.state.hosts.value, []); assert.deepEqual(h.state.environments.value, []); assert.deepEqual(h.state.clusters.value, [])
  for (const name of ['detailOpen', 'relationOpen', 'drawerOpen', 'groupDrawerOpen']) assert.equal(h.state[name].value, false)
  await h.inventory(old); await pending
  assert.deepEqual(h.state.hosts.value, []); assert.equal(h.state.loading.value, false)
  assert.equal(h.notifications.length, 0)
  h.auth.user.permissions = ['cluster.read']; await flush()
  const current = h.requests.slice(old.length)
  assert.equal(current.length, 2)
  await h.inventory(current)
  assert.equal(h.state.hosts.value.length, 3)
})

test('HostView用户、项目与部门变化分别冻结新读范围，旧成功失败都不能污染最新列表或提示', async t => {
  const h = inventoryReads(); t.after(h.stop)
  const first = h.state.refresh(), batches = [h.requests.slice()]
  for (const [key, value] of [['id', 'other-user'], ['project_id', 'project-b'], ['department_id', 'department-b']]) {
    const start = h.requests.length
    h.auth.user[key] = value; await flush()
    assert.ok(batches.at(-1).every(request => request.options.signal.aborted))
    assert.deepEqual(h.state.hosts.value, [])
    batches.push(h.requests.slice(start))
  }
  const current = createHostsPreviewData()
  current.hosts = [{ ...current.hosts[0], name: '最新范围的主机' }]
  await h.inventory(batches[3], current)
  for (const request of batches[0]) request.reject(new Error('旧范围失败'))
  await first
  await h.inventory(batches[1])
  for (const request of batches[2]) request.reject(new Error('旧项目失败'))
  await flush()
  assert.deepEqual(h.state.hosts.value.map(host => host.name), ['最新范围的主机'])
  assert.equal(h.state.loading.value, false); assert.equal(h.state.hostLoadError.value, '')
  assert.equal(h.notifications.length, 0)
})

test('手动完整刷新使旧状态请求失效，同ID旧capabilities不能回滚新Docker绑定', async t => {
  const h = inventoryReads(); t.after(h.stop)
  const initial = h.state.refresh(); await h.inventory(h.requests.slice()); await initial
  h.tick(2000)
  const pendingStatus = h.state.refreshStatuses(), stale = h.requests.at(-1)
  assert.equal(stale.path, '/hosts/statuses')
  const start = h.requests.length, manual = h.state.refresh(), fresh = createHostsPreviewData()
  fresh.hosts[0].capabilities[0].runtime_id = 'new-docker-runtime'
  assert.equal(stale.options.signal.aborted, true)
  await h.inventory(h.requests.slice(start), fresh); await manual
  stale.resolve({ data: { hosts: createHostsPreviewData().hosts.map(({ id, is_active, capabilities }) => ({ id, is_active, capabilities })) } })
  await pendingStatus
  assert.equal(h.state.hosts.value[0].capabilities[0].runtime_id, 'new-docker-runtime')
  assert.equal(h.state.loading.value, false)
  assert.equal(h.notifications.length, 0)
})

test('状态请求撤权或卸载后取消，迟到失败不提示、成功不回写；完整刷新期间不重复发起', async t => {
  const h = inventoryReads(); t.after(h.stop)
  const first = h.state.refresh(), batch = h.requests.slice()
  await h.state.refresh(); assert.equal(h.requests.length, 3)
  await h.inventory(batch); await first
  h.tick(2000)
  const pending = h.state.refreshStatuses(), stale = h.requests.at(-1)
  h.auth.user.permissions = []; await flush()
  assert.equal(stale.options.signal.aborted, true)
  stale.reject(new Error('已撤权的旧状态失败')); await pending
  assert.deepEqual(h.state.hosts.value, []); assert.equal(h.notifications.length, 0)
  const removed = inventoryReads(); t.after(removed.stop)
  const loading = removed.state.refresh(), old = removed.requests.slice()
  removed.stop(); assert.ok(old.every(request => request.options.signal.aborted))
  await removed.inventory(old); await loading
  assert.deepEqual(removed.state.hosts.value, []); assert.equal(removed.notifications.length, 0)
  const statusRemoved = inventoryReads(); t.after(statusRemoved.stop)
  const initial = statusRemoved.state.refresh(); await statusRemoved.inventory(statusRemoved.requests.slice()); await initial
  statusRemoved.tick(2000)
  const statusPending = statusRemoved.state.refreshStatuses(), removedStatus = statusRemoved.requests.at(-1)
  statusRemoved.stop(); assert.equal(removedStatus.options.signal.aborted, true)
  removedStatus.resolve({ data: { hosts: createHostsPreviewData().hosts.map(({ id, capabilities }) => ({ id, is_active: false, capabilities: [] })) } })
  await statusPending
  assert.equal(statusRemoved.state.hosts.value[0].is_active, true)
  assert.ok(statusRemoved.state.hosts.value[0].capabilities.length > 0)
  assert.equal(statusRemoved.notifications.length, 0)
})
