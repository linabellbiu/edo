import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const permissions = ['workflow.read', 'workflow.create', 'workflow.execute']
const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const presets = [{ key: 'empty', name: '空白流水线', category: 'quickstart', steps: [] }, { key: 'go', name: 'Go 流水线', category: 'go', steps: [{ name: '构建', type: 'build' }] }, { key: 'nodejs', name: 'Node 流水线', category: 'nodejs', steps: [] }]
const runtime = (language = 'go', patch = {}) => ({ language, version: '1.26', image: 'golang:1.26-alpine', recommended: true, installed: false, ...patch })

async function harness({ codes = permissions, project = 'project-a', selected = project, canCreate = true, canExecute = true, get, post } = {}) {
  const requests = [], notifications = [], events = [], mounted = [], unmounts = []
  const auth = vue.reactive({ user: { id: 'operator', project_id: project, permissions: [...codes], is_superuser: false }, canAny(codes) { return this.user?.is_superuser || codes.some(code => this.user?.permissions.includes(code)) } })
  const props = vue.reactive({ open: true, canCreate, canExecute })
  const storage = { selected }, localStorage = { getItem: key => key === 'edo.project' ? storage.selected : null }
  const timers = new Map(); let timerID = 0
  const window = new EventTarget()
  window.setTimeout = callback => { const id = ++timerID; timers.set(id, callback); return id }
  window.clearTimeout = id => timers.delete(id)
  const client = Object.fromEntries(['get', 'post'].map(method => [method, async (path, bodyOrOptions, options) => {
    const request = { method, path, body: method === 'post' ? structuredClone(bodyOrOptions) : undefined, options: method === 'post' ? options : bodyOrOptions }
    requests.push(request)
    const handler = { get, post }[method]
    if (handler) { const response = await handler(request); if (response) return response }
    return { data: path === '/workflow-presets' ? { workflow_presets: structuredClone(presets) } : path === '/workflow-runtime-versions' ? { runtime_versions: [runtime(request.options.params.language)] } : path === '/workflow-runtime-versions/prepare' ? runtime(request.body.language, { preparation_status: 'preparing' }) : { workflow_template: { id: 'template-new', project_id: request.options.headers['X-EDO-Project-ID'] }, valid: true, issues: [] } }
  }]))
  const component = loadFrontend('../src/components/WorkflowPresetModal.vue', {
    vue: { ...vue, onMounted: fn => mounted.push(fn), onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-i18n': { useI18n: () => ({ t: (key, args) => args ? `${key}:${JSON.stringify(args)}` : key }) },
    'ant-design-vue': { message: Object.fromEntries(['success', 'error'].map(kind => [kind, text => notifications.push({ kind, text })])) },
    '@/stores/auth': { useAuthStore: () => auth }, '@/api/client': { __esModule: true, default: client }, '@/api/resources': { apiErrorMessage: () => '当前项目操作失败' },
  }, { localStorage, window }).default
  const scope = vue.effectScope(); let state, stopped = false
  scope.run(() => { state = component.setup(props, { expose() {}, emit: (name, ...args) => { events.push({ name, args }); if (name === 'update:open') props.open = args[0] } }) })
  mounted.forEach(fn => fn()); await settle()
  const stop = () => { if (stopped) return; stopped = true; unmounts.forEach(fn => fn()); scope.stop() }
  const tickTimer = async () => { const next = timers.entries().next().value; assert.ok(next, '应有待执行的运行时轮询'); timers.delete(next[0]); next[1](); await settle() }
  return { state, props, auth, storage, window, timers, requests, notifications, events, stop, tickTimer }
}

const writes = h => h.requests.filter(request => request.method === 'post')
const changeProject = async (h, id) => { h.storage.selected = id; h.window.dispatchEvent(new Event('edo:projects-changed')); h.auth.user.project_id = id; await settle() }

test('预设创建冻结项目和所选配置，成功只发送当前项目结果，双击不会重复创建', async t => {
  const pending = deferred(), h = await harness({ post: request => request.path === '/workflow-templates/from-preset' ? pending.promise : undefined }); t.after(h.stop)
  assert.equal(h.requests[0].options.headers['X-EDO-Project-ID'], 'project-a')
  h.state.selectedKey.value = 'empty'; assert.equal(h.state.canConfirm.value, true)
  const creation = h.state.confirm(); await h.state.confirm()
  assert.equal(writes(h).length, 1); assert.deepEqual(writes(h)[0].body, { preset_key: 'empty', runtime_version: undefined })
  assert.equal(writes(h)[0].options.headers['X-EDO-Project-ID'], 'project-a')
  pending.resolve({ data: { workflow_template: { id: 'created-a', project_id: 'project-a' }, valid: true, issues: [] } }); await creation
  assert.equal(h.events.find(item => item.name === 'created').args[0].workflow_template.id, 'created-a')
  assert.equal(h.props.open, false); assert.equal(h.notifications[0].kind, 'success')
})

test('未选择项目、项目权限未对应或缺少读取权限时不加载，无法靠过期按钮创建', async t => {
  for (const options of [{ selected: '' }, { selected: 'project-b' }, { codes: ['workflow.create'] }]) {
    const h = await harness(options); t.after(h.stop)
    h.state.selectedKey.value = 'empty'; await h.state.confirm(); await h.state.prepareSelectedRuntime()
    assert.deepEqual(h.requests, []); assert.equal(h.state.canConfirm.value, false)
  }
  const h = await harness({ codes: ['workflow.read'], canCreate: true, canExecute: true }); t.after(h.stop)
  h.state.selectedKey.value = 'empty'; await h.state.confirm(); await h.state.prepareSelectedRuntime()
  assert.equal(h.state.canConfirm.value, false); assert.deepEqual(writes(h), [])
})

test('创建等待期间切项目取消旧请求并关闭，迟到成功不影响新项目弹窗', async t => {
  const pending = deferred(), h = await harness({ post: request => request.path === '/workflow-templates/from-preset' ? pending.promise : undefined }); t.after(h.stop)
  h.state.selectedKey.value = 'empty'; const creation = h.state.confirm(); const old = writes(h)[0]
  await changeProject(h, 'project-b'); assert.equal(old.options.signal.aborted, true); assert.equal(h.props.open, false)
  h.props.open = true; await settle(); h.state.selectedKey.value = 'go'
  pending.resolve({ data: { workflow_template: { id: 'created-a', project_id: 'project-a' }, valid: true, issues: [] } }); await creation
  assert.equal(h.props.open, true); assert.equal(h.state.selectedKey.value, 'go'); assert.equal(h.events.filter(item => item.name === 'created').length, 0)
  assert.deepEqual(h.notifications, []); assert.equal(h.requests.at(-1).options.headers['X-EDO-Project-ID'], 'project-b')
})

test('角色撤权时清空选择并取消创建，旧失败不弹到新权限范围', async t => {
  const pending = deferred(), h = await harness({ post: () => pending.promise }); t.after(h.stop)
  h.state.selectedKey.value = 'empty'; const creation = h.state.confirm(); h.auth.user.permissions = ['workflow.read']; await settle()
  assert.equal(writes(h)[0].options.signal.aborted, true); assert.equal(h.props.open, false); assert.equal(h.state.selectedKey.value, '')
  pending.reject(new Error('旧项目失败')); await creation
  assert.deepEqual(h.notifications, []); assert.equal(h.events.some(item => item.name === 'created'), false)
})

test('本地选择已变更而事件尚未触发时，旧弹窗不会向新项目发请求', async t => {
  const h = await harness(); t.after(h.stop); h.state.selectedKey.value = 'empty'; h.storage.selected = 'project-b'
  await h.state.confirm(); h.state.selectCategory('go'); await h.state.prepareSelectedRuntime()
  assert.equal(h.requests.length, 1); assert.deepEqual(writes(h), [])
})

test('关闭重开同一项目后旧预设响应不能覆盖新一轮读取', async t => {
  const first = deferred(), second = deferred(); let reads = 0
  const h = await harness({ get: request => request.path === '/workflow-presets' ? (++reads === 1 ? first.promise : second.promise) : undefined }); t.after(h.stop)
  h.props.open = false; h.props.open = true; await settle(); assert.equal(h.requests[0].options.signal.aborted, true)
  second.resolve({ data: { workflow_presets: [{ ...presets[0], name: '第二次读取' }] } }); await settle()
  first.resolve({ data: { workflow_presets: [{ ...presets[0], name: '第一次读取' }] } }); await settle()
  assert.equal(h.state.presets.value[0].name, '第二次读取'); assert.equal(h.state.loading.value, false)
})

test('语言切换再切回时，同语言的旧响应不会覆盖最新运行时', async t => {
  const first = deferred(); let reads = 0
  const h = await harness({ get: request => request.path === '/workflow-runtime-versions' && request.options.params.language === 'go' && ++reads === 1 ? first.promise : undefined }); t.after(h.stop)
  h.state.selectCategory('go'); await settle(); const old = h.requests.at(-1)
  h.state.selectCategory('nodejs'); await settle(); h.state.selectCategory('go'); await settle()
  assert.equal(old.options.signal.aborted, true); assert.equal(h.state.runtimeVersions.value[0].language, 'go')
  first.resolve({ data: { runtime_versions: [runtime('go', { version: '1.24', installed: true })] } }); await settle()
  assert.equal(h.state.runtimeVersions.value[0].version, '1.26'); assert.equal(h.state.loadingRuntimes.value, false)
})

test('运行时下载禁止重复提交且固定项目，切换后不更新结果或继续轮询', async t => {
  const pending = deferred(), h = await harness({ post: request => request.path === '/workflow-runtime-versions/prepare' ? pending.promise : undefined }); t.after(h.stop)
  h.state.selectCategory('go'); await settle(); const preparation = h.state.prepareSelectedRuntime(); await h.state.prepareSelectedRuntime()
  assert.equal(writes(h).length, 1); assert.equal(writes(h)[0].options.headers['X-EDO-Project-ID'], 'project-a')
  await changeProject(h, 'project-b'); assert.equal(writes(h)[0].options.signal.aborted, true)
  pending.resolve({ data: runtime('go', { installed: true }) }); await preparation
  assert.deepEqual(h.state.runtimeVersions.value, []); assert.equal(h.timers.size, 0); assert.deepEqual(h.notifications, [])
})

test('轮询响应返回后再次核对项目，旧项目采样不进入新范围或显示下载成功', async t => {
  const poll = deferred(); let runtimeReads = 0
  const h = await harness({ get: request => request.path === '/workflow-runtime-versions' && ++runtimeReads > 1 ? poll.promise : undefined }); t.after(h.stop)
  h.state.selectCategory('go'); await settle(); await h.state.prepareSelectedRuntime(); await h.tickTimer()
  const request = h.requests.at(-1); assert.equal(request.path, '/workflow-runtime-versions'); assert.equal(request.options.headers['X-EDO-Project-ID'], 'project-a')
  await changeProject(h, 'project-b'); assert.equal(request.options.signal.aborted, true)
  poll.resolve({ data: { runtime_versions: [runtime('go', { installed: true })] } }); await settle()
  assert.deepEqual(h.state.runtimeVersions.value, []); assert.deepEqual(h.notifications, []); assert.equal(h.timers.size, 0)
})

test('切换版本立即取消上一轮等待与请求，只为所选版本保留轮询', async t => {
  const h = await harness({ get: request => request.path === '/workflow-runtime-versions' ? { data: { runtime_versions: [runtime('go', { preparation_status: 'preparing' }), runtime('go', { version: '1.25', recommended: false, preparation_status: 'preparing' })] } } : undefined }); t.after(h.stop)
  h.state.selectCategory('go'); await settle(); assert.equal(h.timers.size, 1)
  const firstTimer = [...h.timers.keys()][0]; h.state.selectedRuntimeVersion.value = '1.25'; await settle()
  assert.equal(h.timers.has(firstTimer), false); assert.equal(h.timers.size, 1)
  h.state.selectCategory('quickstart'); await settle(); assert.equal(h.timers.size, 0)
})

test('卸载清理轮询和未完成请求，迟到成功不能创建导航或提示', async t => {
  const pending = deferred(), h = await harness({ post: request => request.path === '/workflow-templates/from-preset' ? pending.promise : undefined }); t.after(h.stop)
  h.state.selectCategory('go'); await settle(); await h.state.prepareSelectedRuntime(); assert.equal(h.timers.size, 1)
  h.state.selectCategory('quickstart'); h.state.selectedKey.value = 'empty'; const creation = h.state.confirm(); h.stop()
  assert.equal(h.timers.size, 0); assert.equal(writes(h).find(request => request.path === '/workflow-templates/from-preset').options.signal.aborted, true)
  pending.resolve({ data: { workflow_template: { id: 'old', project_id: 'project-a' } } }); await creation
  assert.deepEqual(h.notifications, []); assert.equal(h.events.some(item => item.name === 'created'), false)
})

test('服务端返回其他项目模板时拒绝应用，不发送 created 导航', async t => {
  const h = await harness({ post: () => ({ data: { workflow_template: { id: 'foreign', project_id: 'project-b' } } }) }); t.after(h.stop)
  h.state.selectedKey.value = 'empty'; await h.state.confirm()
  assert.equal(h.events.some(item => item.name === 'created'), false); assert.equal(h.props.open, true); assert.equal(h.state.creating.value, false)
  assert.match(h.notifications[0].text, /不属于当前项目/)
})
