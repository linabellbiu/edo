import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const codes = ['delivery_variable.read', 'delivery_variable.create', 'delivery_variable.update', 'delivery_variable.delete', 'application.read', 'deployment.read']
const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const variable = (project = 'project-a', patch = {}) => ({ id: `variable-${project}`, project_id: project, name: 'API_URL', description: '仅当前项目使用', kind: 'env', usage: 'build', application_id: '', environment_id: '', is_secret: false, is_active: true, version: 3, protected_refs: [], value: 'https://example.test', ...patch })

async function harness({ permissions = codes, project = 'project-a', selected = project, get, put, post, del } = {}) {
  const requests = [], notifications = [], confirmations = [], mounted = [], unmounts = []
  const auth = vue.reactive({ user: { id: 'operator-a', department_id: 'department-a', project_id: project, is_superuser: false, permissions: [...permissions] }, canAny(required) { return !!this.user && (this.user.is_superuser || required.some(code => this.user.permissions.includes(code))) } })
  const storage = { selected }, localStorage = { getItem: key => key === 'edo.project' ? storage.selected : null }, window = new EventTarget()
  const client = Object.fromEntries(['get', 'put', 'post', 'delete'].map(method => [method, async (path, bodyOrOptions, options) => {
    const request = { method, path, body: ['post', 'put'].includes(method) ? structuredClone(bodyOrOptions) : undefined, options: ['post', 'put'].includes(method) ? options : bodyOrOptions }
    requests.push(request)
    const handler = { get, put, post, delete: del }[method]
    if (handler) { const response = await handler(request); if (response) return response }
    const projectID = request.options.headers['X-EDO-Project-ID']
    return { data: path === '/delivery-variables' ? { variables: [variable(projectID)] } : path === '/projects' ? { projects: [{ id: 'project-a', name: '项目 A' }, { id: 'project-b', name: '项目 B' }] } : path === '/applications' ? { applications: [{ id: `app-${projectID}`, name: '应用' }] } : path === '/environments' ? { environments: [{ id: `env-${projectID}`, name: '环境' }] } : {} }
  }]))
  const component = loadFrontend('../src/views/DeliveryVariablesView.vue', {
    vue: { ...vue, onMounted: fn => mounted.push(fn), onBeforeUnmount: fn => unmounts.push(fn) },
    '@/stores/auth': { useAuthStore: () => auth }, '@/api/client': { __esModule: true, default: client }, '@/api/resources': { apiErrorMessage: () => '当前项目操作失败，请重试' },
    'ant-design-vue': { Modal: { confirm(config) { const item = { ...config, destroyed: false, destroy() { this.destroyed = true; config.afterClose?.() } }; confirmations.push(item); return item } }, message: Object.fromEntries(['success', 'warning', 'error'].map(kind => [kind, text => notifications.push({ kind, text })])) },
  }, { localStorage, window }).default
  const scope = vue.effectScope(); let state, stopped = false
  scope.run(() => { state = component.setup({}, { expose() {} }) }); mounted.forEach(fn => fn()); await settle()
  const stop = () => { if (stopped) return; stopped = true; unmounts.forEach(fn => fn()); scope.stop() }
  return { state, auth, storage, window, requests, notifications, confirmations, stop }
}
const writes = h => h.requests.filter(item => item.method !== 'get')
const changeProject = async (h, id) => { h.storage.selected = id; h.window.dispatchEvent(new Event('edo:projects-changed')); h.auth.user.project_id = id; await settle() }

test('变量页只读取当前项目并显示名称，读请求固定项目头且不额外执行写入', async t => {
  const h = await harness(); t.after(h.stop)
  assert.equal(h.state.projectName.value, '项目 A'); assert.equal(h.state.rows.value[0].project_id, 'project-a')
  assert.ok(h.requests.every(item => item.options.headers['X-EDO-Project-ID'] === 'project-a'))
  assert.deepEqual(writes(h), [])
})

test('未选项目、项目权限尚未对应、没有读取角色权限时不读取也不能修改', async t => {
  for (const options of [{ selected: '' }, { selected: 'project-b' }, { permissions: ['delivery_variable.update', 'delivery_variable.delete'] }]) {
    const h = await harness(options); t.after(h.stop)
    h.state.edit(); h.state.edit(variable()); h.state.remove(variable()); await h.state.save(); await h.state.load()
    assert.deepEqual(h.requests, []); assert.equal(h.state.open.value, false); assert.equal(h.confirmations.length, 0)
  }
})

test('只读角色能查看但不能新增编辑或删除，前端动作再次检查权限', async t => {
  const h = await harness({ permissions: ['delivery_variable.read'] }); t.after(h.stop)
  h.state.edit(); h.state.edit(h.state.rows.value[0]); h.state.remove(h.state.rows.value[0]); await h.state.save()
  assert.equal(h.state.can('create'), false); assert.equal(h.state.can('update'), false); assert.equal(h.state.can('delete'), false)
  assert.equal(h.state.open.value, false); assert.equal(h.confirmations.length, 0); assert.deepEqual(writes(h), [])
})

test('异常返回其他项目变量不会展示或成为编辑删除目标', async t => {
  const h = await harness({ get: request => request.path === '/delivery-variables' ? { data: { variables: [variable(), variable('project-b')] } } : undefined }); t.after(h.stop)
  assert.deepEqual(h.state.rows.value.map(item => item.project_id), ['project-a'])
  h.state.edit(variable('project-b')); h.state.remove(variable('project-b'))
  assert.equal(h.state.open.value, false); assert.equal(h.confirmations.length, 0)
})

test('编辑草稿期间切项目立即清空密钥、旧应用环境和筛选，不在新范围保存', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.edit(h.state.rows.value[0]); h.state.form.is_secret = true; h.state.form.value = '仅旧项目的草稿'; h.state.search.value = '旧搜索'
  await changeProject(h, 'project-b'); await h.state.save()
  assert.equal(h.state.open.value, false); assert.equal(h.state.form.value, ''); assert.equal(h.state.form.name, ''); assert.equal(h.state.search.value, '')
  assert.equal(h.state.projectName.value, '项目 B'); assert.equal(h.state.applications.value[0].id, 'app-project-b'); assert.deepEqual(writes(h), [])
})

test('保存冻结原项目和版本，切换后中止请求且迟到结果不能关闭新草稿', async t => {
  const pending = deferred(), h = await harness({ put: () => pending.promise }); t.after(h.stop)
  h.state.edit(h.state.rows.value[0]); h.state.form.value = '原项目值'; const saving = h.state.save(); await h.state.save()
  assert.equal(writes(h).length, 1); const request = writes(h)[0]
  assert.equal(request.options.headers['X-EDO-Project-ID'], 'project-a'); assert.equal(request.body.expected_version, 3)
  await changeProject(h, 'project-b'); assert.equal(request.options.signal.aborted, true)
  h.state.edit(); h.state.form.name = 'B_ONLY'; h.state.form.value = '新项目草稿'
  pending.resolve({ data: {} }); await saving
  assert.equal(h.state.open.value, true); assert.equal(h.state.form.value, '新项目草稿'); assert.equal(h.notifications.length, 0)
})

test('本地选择已变化但事件尚未派发时也不能按旧权限发出修改', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.edit(h.state.rows.value[0]); h.state.form.value = '旧值'; h.storage.selected = 'project-b'
  await h.state.save(); h.state.remove(h.state.rows.value[0]); assert.deepEqual(writes(h), []); assert.equal(h.confirmations.length, 0)
})

test('角色权限撤销立即关闭编辑并清空内容，已有保存的迟到失败不弹到新范围', async t => {
  const pending = deferred(), h = await harness({ post: () => pending.promise }); t.after(h.stop)
  h.state.edit(); h.state.form.name = 'KEY'; h.state.form.is_secret = true; h.state.form.value = '敏感草稿'; const saving = h.state.save()
  h.auth.user.permissions = ['delivery_variable.read']; await settle()
  assert.equal(h.state.open.value, false); assert.equal(h.state.form.value, ''); assert.equal(writes(h)[0].options.signal.aborted, true)
  pending.reject(new Error('旧请求错误')); await saving
  assert.equal(h.state.editorError.value, ''); assert.equal(h.notifications.length, 0)
})

test('删除确认绑定项目和角色，切项目/撤权销毁确认且旧确认回调不能删除', async t => {
  for (const update of [h => changeProject(h, 'project-b'), async h => { h.auth.user.permissions = ['delivery_variable.read']; await settle() }]) {
    const h = await harness(); t.after(h.stop); h.state.remove(h.state.rows.value[0]); const confirmation = h.confirmations[0]
    assert.match(confirmation.content, /项目 A/); await update(h); assert.equal(confirmation.destroyed, true)
    await confirmation.onOk(); assert.deepEqual(writes(h), [])
  }
})

test('删除确认后才执行且禁止双击，保留项目头和预期版本', async t => {
  const pending = deferred(), h = await harness({ del: () => pending.promise }); t.after(h.stop)
  h.state.remove(h.state.rows.value[0]); assert.deepEqual(writes(h), [])
  const removal = h.confirmations[0].onOk(); await h.confirmations[0].onOk()
  assert.equal(writes(h).length, 1); assert.equal(writes(h)[0].options.headers['X-EDO-Project-ID'], 'project-a'); assert.equal(writes(h)[0].options.params.version, 3)
  pending.resolve({ data: {} }); await removal; assert.equal(h.notifications[0].text, '变量已删除')
})

test('读取晚到不能把旧项目行覆盖新项目，权限撤销后不保留读取结果', async t => {
  const pending = deferred(), h = await harness({ get: request => request.path === '/delivery-variables' && request.options.headers['X-EDO-Project-ID'] === 'project-a' ? pending.promise : undefined }); t.after(h.stop)
  const old = h.requests.find(item => item.path === '/delivery-variables'); await changeProject(h, 'project-b')
  pending.resolve({ data: { variables: [variable()] } }); await settle()
  assert.equal(old.options.signal.aborted, true); assert.equal(h.state.rows.value[0].project_id, 'project-b')
  h.auth.user.permissions = []; await settle(); assert.deepEqual(h.state.rows.value, []); assert.deepEqual(h.state.applications.value, [])
})

test('服务端403清空已有列表、密钥草稿和确认，不把受限内容保留页面', async t => {
  const h = await harness({ put: () => { throw { response: { status: 403 } } } }); t.after(h.stop)
  h.state.edit(h.state.rows.value[0]); h.state.form.value = '不应残留'; await h.state.save()
  assert.equal(h.state.open.value, false); assert.equal(h.state.form.value, ''); assert.deepEqual(h.state.rows.value, []); assert.match(h.state.error.value, /当前项目操作失败/)
})

test('保存保留未替换密钥语义，普通值不丢失；成功只刷新当前项目', async t => {
  const h = await harness({ get: request => request.path === '/delivery-variables' ? { data: { variables: [variable('project-a', { is_secret: true, value: undefined })] } } : undefined }); t.after(h.stop)
  h.state.edit(h.state.rows.value[0]); assert.equal(h.state.form.value, ''); assert.equal(h.state.replaceValue.value, false)
  await h.state.save(); assert.equal(writes(h)[0].body.value, undefined); assert.equal(h.state.open.value, false)
  h.state.edit(); h.state.form.name = 'NORMAL'; h.state.form.value = '可见普通值'; await h.state.save()
  assert.equal(writes(h)[1].body.value, '可见普通值'); assert.ok(h.requests.every(item => item.options.headers['X-EDO-Project-ID'] === 'project-a'))
})

test('卸载取消所有读取，保存迟到响应不提示成功或恢复数据', async t => {
  const pending = deferred(), h = await harness({ post: () => pending.promise }); t.after(h.stop)
  h.state.edit(); h.state.form.name = 'KEY'; h.state.form.value = '草稿'; const saving = h.state.save(); h.stop()
  assert.equal(writes(h)[0].options.signal.aborted, true); assert.equal(h.state.form.value, '')
  pending.resolve({ data: {} }); await saving; assert.deepEqual(h.notifications, []); assert.deepEqual(h.state.rows.value, [])
})
