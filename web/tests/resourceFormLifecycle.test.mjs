import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import axios from 'axios'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)) }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const codes = ['application', 'repository', 'build_plan', 'image_registry'].flatMap(prefix => ['read', 'create', 'update', 'execute'].map(action => `${prefix}.${action}`))

async function harness(t, { section = 'build-plans', pending, transport, permissions = codes } = {}) {
  const requests = [], notifications = [], unmounts = []
  const storage = { project: 'project-a' }, window = new EventTarget()
  const auth = vue.reactive({ authorizationReady: true, user: { id: 'operator', department_id: 'department-a', project_id: 'project-a', permissions: [...permissions] }, canAny(required) { return this.authorizationReady && required.some(code => this.user?.permissions.includes(code)) } })
  const records = {
    applications: [{ id: 'app-a', name: 'app_a', repository_id: 'repo-a', is_active: true, workflows: [] }],
    repositories: [{ id: 'repo-a', name: '仓库甲', provider: 'generic', clone_url: 'https://git.example/test.git', default_branch: 'main', auth_type: 'none', is_active: true }],
    build_plans: [{ id: 'plan-a', name: '构建方案', kind: 'dockerfile', dockerfile_path: 'Dockerfile', context_path: '.', timeout_seconds: 1800, config_version: 1, is_active: true }],
  }
  const client = {
    async get(path, options) { requests.push({ method: 'get', path, options }); return { data: path === '/applications' ? { applications: records.applications } : path === '/repositories' ? { repositories: records.repositories } : path === '/build-plans' ? { build_plans: records.build_plans } : {} } },
    ...Object.fromEntries(['post', 'put'].map(method => [method, async (path, body, options) => { requests.push({ method, path, body: structuredClone(body), options }); return pending ? pending.promise : { data: {} } }])),
  }
  const component = loadFrontend('../src/views/DevOpsView.vue', {
    vue: { ...vue, onMounted() {}, onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({}) },
    'vue-i18n': { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) },
    '@/stores/auth': { useAuthStore: () => auth }, '@/api/client': transport?.module ?? { __esModule: true, default: client },
    '@/api/resources': transport?.resources ?? { apiErrorMessage: () => '操作失败', isMutationAcknowledged: error => error?.code === 'EDO_MUTATION_ACKNOWLEDGED' },
    '@/utils/deploymentInstances': loadFrontend('../src/utils/deploymentInstances.ts'),
    'ant-design-vue': { Modal: { destroyAll() {} }, message: Object.fromEntries(['error', 'success', 'info'].map(kind => [kind, value => notifications.push({ kind, value })])) },
  }, { localStorage: { getItem: () => storage.project }, window, document: new EventTarget() }).default
  const scope = vue.effectScope(), props = vue.reactive({ section })
  const state = scope.run(() => component.setup(props, { expose() {} }))
  await state.refresh()
  let stopped = false
  const stop = () => { if (stopped) return; stopped = true; unmounts.forEach(fn => fn()); scope.stop() }
  t.after(stop)
  const open = (editing = false, fillDraft = true) => {
    if (editing) state.edit(records[props.section === 'build-plans' ? 'build_plans' : props.section][0])
    else state.create()
    if (fillDraft && props.section === 'build-plans') state.buildForm.name = '未保存草稿'
    if (fillDraft && props.section === 'applications') Object.assign(state.appForm, { name: 'draft_app', repository_id: 'repo-a' })
    if (fillDraft && props.section === 'repositories') state.repoForm.name = '未保存仓库'
  }
  return { state, auth, props, storage, requests, notifications, open, stop }
}
const writes = h => h.requests.filter(x => x.method !== 'get')

for (const editing of [false, true]) for (const lost of ['read', editing ? 'update' : 'create']) test(`${editing ? '编辑' : '新建'}构建方案撤${lost}清空已开表单，恢复后不复活`, async t => {
  const h = await harness(t); h.open(editing)
  h.state.buildArgsText.value = 'PUBLIC=old'
  h.auth.user.permissions = codes.filter(code => code !== `build_plan.${lost}`); await settle()
  assert.equal(h.state.formOpen.value, false)
  assert.equal(h.state.buildForm.name, '')
  assert.equal(h.state.buildArgsText.value, '')
  await h.state.save(); assert.equal(writes(h).length, 0)
  h.auth.user.permissions = [...codes]; await settle(); h.open(editing, false)
  assert.notEqual(h.state.buildForm.name, '未保存草稿')
  assert.equal(h.state.buildArgsText.value, '')
})

for (const key of ['id', 'department_id', 'project_id']) test(`构建方案表单${key}改变必须清场`, async t => {
  const h = await harness(t); h.open(true)
  h.auth.user[key] = 'other'; await settle()
  assert.equal(h.state.formOpen.value, false)
  assert.equal(h.state.buildForm.name, '')
  await h.state.save(); assert.equal(writes(h).length, 0)
})

test('无关授权变化保持草稿，确认权限核验开始则清场', async t => {
  const h = await harness(t); h.open(true)
  h.auth.user.permissions.push('monitor.read'); await settle()
  assert.equal(h.state.formOpen.value, true)
  assert.equal(h.state.buildForm.name, '未保存草稿')
  h.auth.authorizationReady = false; await settle()
  assert.equal(h.state.formOpen.value, false)
  assert.equal(h.state.buildForm.name, '')
})

test('所选项目已变但范围事件尚未派发，不得提交旧构建方案', async t => {
  const h = await harness(t); h.open(true); h.storage.project = 'project-b'
  await h.state.save(); assert.equal(writes(h).length, 0)
})

test('正常取消清空隐藏草稿，失效的编辑及新建回调不重开表单', async t => {
  const h = await harness(t); h.open(true); h.state.closeForm(); await settle()
  assert.equal(h.state.buildForm.name, '')
  h.auth.user.permissions = ['build_plan.read']; await settle()
  h.state.create(); h.state.edit({ id: 'plan-a', name: '旧对象', kind: 'dockerfile' }); await settle()
  assert.equal(h.state.formOpen.value, false)
  assert.equal(h.state.buildForm.name, '')
})

for (const change of ['id', 'department_id', 'project_id', 'revoked', 'unmount']) for (const response of ['success', 'failure', 'ack']) test(`构建保存${change}后迟到${response}不得关闭新稿、通知或刷新`, async t => {
  const pending = deferred(), h = await harness(t, { pending }); h.open(true)
  const saving = h.state.save(); await settle(); const request = writes(h)[0]
  if (change === 'unmount') h.stop()
  else if (change === 'revoked') h.auth.user.permissions = ['build_plan.read']
  else h.auth.user[change] = 'other'
  await settle()
  assert.equal(request.options?.headers?.['X-EDO-Project-ID'], 'project-a')
  assert.equal(request.options?.signal?.aborted, true)
  if (change !== 'unmount') {
    Object.assign(h.auth.user, { id: 'operator', department_id: 'department-a', project_id: 'project-a', permissions: [...codes] }); await settle(); h.open(false)
  }
  const count = h.requests.length
  if (response === 'success') pending.resolve({ data: {} })
  else pending.reject(response === 'ack' ? Object.assign(new Error('已受理'), { code: 'EDO_MUTATION_ACKNOWLEDGED' }) : new Error('合成网络失败'))
  await saving
  assert.equal(h.requests.length, count)
  assert.deepEqual(h.notifications, [])
  assert.equal(h.state.saving.value, false)
  if (change !== 'unmount') {
    assert.equal(h.state.formOpen.value, true)
    assert.equal(h.state.buildForm.name, '未保存草稿')
  }
})

test('同范围待保存禁止双提交，当前失败保留草稿并允许正常重试', async t => {
  const pending = deferred(), h = await harness(t, { pending }); h.open(true)
  const saving = h.state.save(); await settle(); await h.state.save(); assert.equal(writes(h).length, 1)
  pending.reject(new Error('合成失败')); await saving
  assert.equal(h.state.formOpen.value, true); assert.equal(h.state.buildForm.name, '未保存草稿'); assert.equal(h.state.saving.value, false)
  assert.deepEqual(h.notifications.map(x => x.kind), ['error'])
})

test('当前已受理回执关闭清稿，不提示普通失败或自动重试', async t => {
  const pending = deferred(), h = await harness(t, { pending }); h.open(false)
  const saving = h.state.save(); await settle()
  pending.reject(Object.assign(new Error('已受理'), { code: 'EDO_MUTATION_ACKNOWLEDGED' })); await saving
  assert.equal(h.state.formOpen.value, false); assert.equal(h.state.buildForm.name, '')
  assert.equal(writes(h).length, 1); assert.deepEqual(h.notifications.map(x => x.kind), ['info'])
})

for (const section of ['applications', 'repositories']) test(`共用${section}表单在身份变化时清场，不让旧保存关闭新表单`, async t => {
  const pending = deferred(), h = await harness(t, { section, pending }); h.open(true)
  const saving = h.state.save(); await settle(); h.auth.user.id = 'other'; await settle()
  assert.equal(h.state.formOpen.value, false)
  h.auth.user.id = 'operator'; await settle(); h.open(false)
  const count = h.requests.length; pending.resolve({ data: {} }); await saving
  assert.equal(h.state.formOpen.value, true); assert.equal(h.requests.length, count); assert.deepEqual(h.notifications, [])
})

test('镜像登录测试的旧结果不能标记新凭据已经测试', async t => {
  const pending = deferred(), h = await harness(t, { section: 'image-registries', pending }); h.state.create()
  Object.assign(h.state.registryForm, { name: '测试镜像仓库', endpoint: 'https://registry.example', username: 'old', credential: 'synthetic-old' })
  const testing = h.state.testRegistry(); await settle()
  h.auth.user.id = 'other'; await settle(); h.auth.user.id = 'operator'; await settle(); h.state.create()
  Object.assign(h.state.registryForm, { username: 'new', credential: 'synthetic-new' })
  pending.resolve({ data: {} }); await testing
  assert.equal(h.state.registryTested.value, false); assert.equal(h.state.registryForm.credential, 'synthetic-new'); assert.deepEqual(h.notifications, [])
})

test('同一镜像草稿测试中修改凭据，旧成功不代表当前凭据通过', async t => {
  const pending = deferred(), h = await harness(t, { section: 'image-registries', pending }); h.state.create()
  Object.assign(h.state.registryForm, { name: '测试仓库', endpoint: 'https://registry.example', credential: 'synthetic-old' })
  const testing = h.state.testRegistry(); await settle(); h.state.registryForm.credential = 'synthetic-new'
  pending.resolve({ data: {} }); await testing
  assert.equal(h.state.registryTested.value, false); assert.equal(h.state.testing.value, false); assert.deepEqual(h.notifications, [])
})

test('同一镜像草稿更换凭据后，旧测试失败也不得提示新稿错误', async t => {
  const pending = deferred(), h = await harness(t, { section: 'image-registries', pending }); h.state.create()
  Object.assign(h.state.registryForm, { endpoint: 'https://registry.example', credential: 'synthetic-old' })
  const testing = h.state.testRegistry(); await settle(); h.state.registryForm.credential = 'synthetic-new'
  pending.reject(new Error('旧凭据失败')); await testing
  assert.equal(h.state.registryTested.value, false); assert.equal(h.state.registryForm.credential, 'synthetic-new'); assert.deepEqual(h.notifications, [])
})

for (const editing of [false, true]) test(`独立仓库${editing ? '编辑' : '新建'}只需仓库权限，不依赖应用查看权限`, async t => {
  const h = await harness(t, { section: 'repositories', permissions: ['repository.read', editing ? 'repository.update' : 'repository.create'] }); h.open(editing)
  assert.equal(h.state.formOpen.value, true)
  h.state.repoForm.clone_url = 'https://git.example/synthetic.git'; await h.state.save()
  assert.equal(writes(h).length, 1); assert.equal(writes(h)[0].path, editing ? '/repositories/repo-a' : '/repositories')
  assert.equal(h.state.formOpen.value, false); assert.equal(h.notifications.some(x => x.kind === 'error'), false)
})

test('独立仓库撤read但保留update必须清场，不把application.read当作替代', async t => {
  const h = await harness(t, { section: 'repositories', permissions: ['repository.read', 'repository.update', 'application.read'] }); h.open(true)
  h.auth.user.permissions = ['repository.update', 'application.read']; await settle()
  assert.equal(h.state.formOpen.value, false); assert.equal(h.state.repoForm.name, '')
  await h.state.save(); assert.equal(writes(h).length, 0)
})

for (const editing of [false, true]) test(`应用${editing ? '编辑' : '新建'}中的新仓库成功回填经过真实同步watch，保留父草稿`, async t => {
  const pending = deferred(), h = await harness(t, { section: 'applications', pending, permissions: ['application.read', editing ? 'application.update' : 'application.create', 'repository.create'] }); h.open(editing)
  const draft = structuredClone(vue.toRaw(h.state.appForm)), id = h.state.editingID.value
  h.state.createApplicationRepository(); Object.assign(h.state.repoForm, { name: 'synthetic_repo', clone_url: 'https://git.example/synthetic.git' })
  const saving = h.state.save(); await settle(); assert.equal(writes(h).length, 1); assert.equal(writes(h)[0].path, '/repositories')
  pending.resolve({ data: { repository: { id: 'new-repo', name: 'synthetic_repo' } } }); await saving
  assert.equal(h.state.formOpen.value, true); assert.equal(h.state.formSection.value, 'applications'); assert.equal(h.state.editingID.value, id)
  assert.deepEqual(structuredClone(vue.toRaw(h.state.appForm)), { ...draft, repository_id: 'new-repo' }); assert.equal(h.state.saving.value, false)
})

for (const section of ['repositories', 'image-registries']) test(`${section}连接测试中撤执行权限，取消旧请求并清场`, async t => {
  const pending = deferred(), h = await harness(t, { section, pending }); h.open(false)
  if (section === 'image-registries') Object.assign(h.state.registryForm, { endpoint: 'https://registry.example', credential: 'synthetic-old' })
  const testing = section === 'repositories' ? h.state.testRepository() : h.state.testRegistry(); await settle()
  const prefix = section === 'repositories' ? 'repository' : 'image_registry'
  h.auth.user.permissions = codes.filter(code => code !== `${prefix}.execute`); await settle()
  assert.equal(writes(h)[0].options.signal.aborted, true); assert.equal(h.state.formOpen.value, false)
  pending.resolve({ data: {} }); await testing
  assert.equal(h.state.registryTested.value, false); assert.equal(h.state.testing.value, false); assert.deepEqual(h.notifications, [])
})

// 以下使用项目真实 Axios 拦截器及内存 adapter，不访问网络或服务。
function memoryTransport() {
  const calls = [], window = new EventTarget()
  const module = loadFrontend('../src/api/client.ts', { axios: { __esModule: true, default: axios } }, { window, localStorage: { getItem: () => 'project-a' } })
  const resources = loadFrontend('../src/api/resources.ts', { axios: { __esModule: true, default: axios }, '@/api/client': module })
  let pending
  module.default.defaults.adapter = config => {
    calls.push({ method: config.method, path: config.url })
    if (config.method !== 'get') return new Promise((resolve, reject) => { pending = { config, resolve, reject } })
    assert.ok(['/applications', '/repositories', '/build-plans', '/image-registries'].includes(config.url))
    return Promise.resolve({ status: 200, statusText: 'OK', headers: {}, config, data: { applications: [], repositories: [], build_plans: [], image_registries: [] } })
  }
  return { module, resources, calls, get pending() { return pending }, respond() { pending.resolve({ status: 200, statusText: 'OK', headers: {}, config: pending.config, data: { build_plan: { id: 'synthetic-old-response' } } }) } }
}

test('真实客户端收到旧epoch的200只按已受理处理，不报保存失败或重试', async t => {
  const transport = memoryTransport(), h = await harness(t, { transport }); h.open(true)
  const saving = h.state.save(); await settle()
  assert.equal(transport.pending.config.headers.get('X-EDO-Project-ID'), 'project-a')
  transport.module.setAuthorizationFreshness(false); transport.respond(); await saving
  assert.equal(h.state.formOpen.value, false); assert.equal(h.state.buildForm.name, '')
  assert.deepEqual(h.notifications.map(x => x.kind), ['info']); assert.equal(transport.calls.filter(x => x.method !== 'get').length, 1)
})

test('真实客户端取消旧保存后，迟到200不影响重新打开的新稿', async t => {
  const transport = memoryTransport(), h = await harness(t, { transport }); h.open(true)
  const saving = h.state.save(); await settle(); h.auth.user.id = 'other'; await settle()
  assert.equal(transport.pending.config.signal.aborted, true)
  h.auth.user.id = 'operator'; await settle(); h.open(false)
  const count = transport.calls.length; transport.respond(); await saving
  assert.equal(h.state.formOpen.value, true); assert.equal(h.state.buildForm.name, '未保存草稿')
  assert.equal(transport.calls.length, count); assert.deepEqual(h.notifications, [])
})

test('真实客户端当前403保留当前草稿并显示业务失败，不冒充已受理', async t => {
  const transport = memoryTransport(), h = await harness(t, { transport }); h.open(true)
  const saving = h.state.save(); await settle()
  transport.pending.reject(new axios.AxiosError('合成拒绝', 'ERR_BAD_REQUEST', transport.pending.config, undefined, { status: 403, data: { message: '合成拒绝' }, config: transport.pending.config }))
  await saving
  assert.equal(h.state.formOpen.value, true); assert.equal(h.state.buildForm.name, '未保存草稿')
  assert.deepEqual(h.notifications, [{ kind: 'error', value: '合成拒绝' }]); assert.equal(transport.calls.filter(x => x.method !== 'get').length, 1)
})
