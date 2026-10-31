import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {loadFrontend} from './acceptanceHarness.mjs'

const active = (id = 'project-a') => ({id, name: id, is_active: true})
const profile = (project = 'project-a') => ({id: 'reader', department_id: 'department-a', project_id: project, is_superuser: false, permissions: []})
const settle = async () => { await vue.nextTick(); await Promise.resolve(); await vue.nextTick() }
function harness(t, {authorization = 'ready', project = 'project-a', user = profile(project), refreshProfile = false} = {}) {
  const mounts = [], unmounts = [], requests = [], notifications = [], locations = [], storage = new Map(project ? [['edo.project', project]] : [])
  const listeners = new Map(), auth = vue.reactive({authorizationState: authorization, user}), scope = vue.effectScope()
  let refreshes = 0, stopped = false, state
  auth.refreshPermissions = async () => { refreshes++; if (refreshProfile && auth.user) auth.user = {...auth.user} }
  const window = {location: {assign: path => locations.push(path)}, addEventListener: (name, callback) => listeners.set(name, callback), removeEventListener: (name, callback) => { if (listeners.get(name) === callback) listeners.delete(name) }}
  const component = loadFrontend('../src/components/ProjectSelector.vue', {
    vue: {...vue, useId: () => 'selector-recovery', onMounted: callback => mounts.push(callback), onBeforeUnmount: callback => unmounts.push(callback)},
    'vue-i18n': {useI18n: () => ({t: key => key})},
    'ant-design-vue': {message: {error: value => notifications.push(value)}},
    '@/stores/auth': {useAuthStore: () => auth},
    '@/api/resources': {apiErrorMessage: error => error.message},
    '@/api/client': {__esModule: true, default: {get: (path, config) => new Promise((resolve, reject) => requests.push({path, config, resolve: projects => resolve({data: {projects}}), reject}))}},
  }, {window, localStorage: {getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key)}}).default
  scope.run(() => { state = component.setup({}, {expose() {}}) })
  const stop = () => { if (stopped) return; stopped = true; unmounts.forEach(callback => callback()); scope.stop() }
  t.after(stop)
  return {state, auth, requests, notifications, locations, storage, listeners, stop, mount: () => mounts.forEach(callback => callback()), event: (name, value) => listeners.get(name)?.(value), refreshes: () => refreshes}
}

test('首次checking不发项目请求，ready后只读取一次，同范围profile对象更新不形成循环', async t => {
  const h = harness(t, {authorization: 'checking', refreshProfile: true})
  h.mount(); await settle(); assert.equal(h.requests.length, 0)
  h.event('edo:projects-changed'); await settle(); assert.equal(h.requests.length, 0)
  h.auth.authorizationState = 'ready'; await settle(); assert.equal(h.requests.length, 1)
  h.requests[0].resolve([active()]); await settle()
  assert.equal(h.state.items.value[0].id, 'project-a'); assert.equal(h.refreshes(), 1)
  await settle(); assert.equal(h.requests.length, 1); assert.deepEqual(h.locations, [])
})

for (const authorization of [undefined, 'unknown-state']) test(`${authorization === undefined ? '未提供' : '陌生'}授权状态不读取或选择项目，只有明确ready才恢复`, async t => {
  const h = harness(t)
  h.auth.authorizationState = authorization
  h.mount(); h.event('edo:projects-changed'); await settle()
  assert.equal(h.requests.length, 0)
  h.state.change('project-b'); assert.equal(h.storage.get('edo.project'), 'project-a'); assert.deepEqual(h.locations, [])
  h.auth.authorizationState = 'ready'; await settle(); assert.equal(h.requests.length, 1)
  h.requests[0].resolve([active()]); await settle()
  assert.equal(h.state.items.value[0].id, 'project-a'); assert.equal(h.refreshes(), 1); assert.deepEqual(h.locations, [])
})

test('撤销后空列表在授权恢复时自动重读，恢复项目沿原语义保存选择并重建/projects页面', async t => {
  const h = harness(t, {project: ''})
  h.mount(); h.requests[0].resolve([]); await settle(); assert.equal(h.state.items.value.length, 0)
  h.auth.authorizationState = 'checking'; h.auth.authorizationState = 'ready'; await settle()
  assert.equal(h.requests.length, 2, '不能依赖成员恢复不会发送的projects-changed事件')
  h.requests[1].resolve([active()]); await settle()
  assert.equal(h.state.items.value[0].id, 'project-a'); assert.equal(h.storage.get('edo.project'), 'project-a')
  assert.deepEqual(h.locations, ['/projects']); assert.equal(h.refreshes(), 1)
})

test('授权未知同步清掉旧options和显示选择，不修改持久选择或请求新列表', async t => {
  const h = harness(t)
  h.mount(); h.requests[0].resolve([active()]); await settle()
  h.auth.authorizationState = 'unavailable'
  assert.equal(h.state.items.value.length, 0); assert.equal(h.state.selected.value, '')
  assert.equal(h.storage.get('edo.project'), 'project-a'); assert.equal(h.state.loading.value, false)
  h.state.change('project-b'); await settle()
  assert.deepEqual(h.locations, []); assert.equal(h.storage.get('edo.project'), 'project-a'); assert.equal(h.requests.length, 1)
})

for (const failed of [false, true]) test(`checking→ready同身份期间的旧${failed ? '失败' : '成功'}不能污染恢复后的列表、错误或loading`, async t => {
  const h = harness(t)
  h.mount(); const old = h.requests[0]
  h.auth.authorizationState = 'checking'; assert.equal(old.config.signal.aborted, true)
  h.auth.authorizationState = 'ready'; await settle(); assert.equal(h.requests.length, 2)
  h.requests[1].resolve([active()]); await settle()
  if (failed) old.reject(new Error('旧请求错误')); else old.resolve([active('stale-project')])
  await settle()
  assert.equal(h.state.items.value[0].id, 'project-a'); assert.equal(h.state.selected.value, 'project-a')
  assert.equal(h.state.loading.value, false); assert.equal(h.refreshes(), 1); assert.deepEqual(h.notifications, []); assert.deepEqual(h.locations, [])
})

test('身份部门项目范围替换同步清旧数据，读取准确新范围，迟到旧结果不能回写', async t => {
  const h = harness(t)
  h.mount(); const old = h.requests[0]
  h.storage.set('edo.project', 'project-b'); h.auth.user = {...profile('project-b'), id: 'other-reader', department_id: 'department-b'}
  assert.equal(old.config.signal.aborted, true); assert.equal(h.state.items.value.length, 0)
  await settle(); assert.equal(h.requests.length, 2)
  h.requests[1].resolve([active('project-b')]); await settle(); old.resolve([active()]); await settle()
  assert.equal(h.state.items.value[0].id, 'project-b'); assert.equal(h.state.selected.value, 'project-b')
  assert.equal(h.refreshes(), 1); assert.deepEqual(h.locations, [])
})

test('卸载撤销请求和监听，迟到成功失败及后续授权事件均无副作用', async t => {
  for (const failed of [false, true]) {
    const h = harness(t); h.mount(); const pending = h.requests[0]
    h.stop(); assert.equal(pending.config.signal.aborted, true); assert.equal(h.listeners.size, 0)
    if (failed) pending.reject(new Error('卸载后的旧错误')); else pending.resolve([active('stale-project')])
    h.auth.authorizationState = 'checking'; h.auth.authorizationState = 'ready'; await settle()
    assert.equal(h.requests.length, 1); assert.equal(h.state.items.value.length, 0); assert.equal(h.refreshes(), 0)
    assert.deepEqual(h.notifications, []); assert.deepEqual(h.locations, [])
  }
})

test('正常projects-changed刷新取代旧请求，只应用最新响应且错误不会循环请求', async t => {
  const h = harness(t); h.mount(); const old = h.requests[0]
  h.event('edo:projects-changed'); assert.equal(old.config.signal.aborted, true); assert.equal(h.requests.length, 2)
  h.requests[1].reject(new Error('当前读取失败')); await settle(); old.resolve([active('stale-project')]); await settle()
  assert.deepEqual(h.notifications, ['当前读取失败']); assert.equal(h.requests.length, 2); assert.equal(h.state.loading.value, false)
  h.auth.authorizationState = 'checking'; h.auth.authorizationState = 'ready'; await settle(); assert.equal(h.requests.length, 3)
  h.requests[2].resolve([active()]); await settle(); assert.equal(h.state.items.value[0].id, 'project-a'); assert.equal(h.requests.length, 3)
})

test('正常选择和跨窗口storage仍使用原硬导航，其他key及相同选择不跳转', async t => {
  const h = harness(t); h.mount(); h.requests[0].resolve([active(), active('project-b')]); await settle()
  h.state.change('project-a'); h.event('storage', {key: 'unrelated'}); h.event('storage', {key: null}); assert.deepEqual(h.locations, [])
  h.state.change('project-b'); assert.equal(h.storage.get('edo.project'), 'project-b'); assert.deepEqual(h.locations, ['/projects'])
  const other = harness(t); other.mount(); const old = other.requests[0]
  other.storage.set('edo.project', 'project-b'); other.event('storage', {key: 'edo.project'})
  assert.equal(old.config.signal.aborted, true); assert.deepEqual(other.locations, ['/projects'])
  old.resolve([active()]); await settle(); assert.equal(other.state.items.value.length, 0); assert.equal(other.refreshes(), 0)
})

test('已退出身份不读取项目，ready范围中失效项目仍清存储且只硬导航一次', async t => {
  const out = harness(t, {user: null}); out.mount(); await settle(); assert.equal(out.requests.length, 0)
  const h = harness(t); h.mount(); h.requests[0].resolve([]); await settle()
  assert.equal(h.storage.has('edo.project'), false); assert.equal(h.state.selected.value, '')
  assert.deepEqual(h.locations, ['/projects']); assert.equal(h.refreshes(), 0)
})
