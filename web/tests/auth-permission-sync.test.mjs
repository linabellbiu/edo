import assert from 'node:assert/strict'
import { test } from 'node:test'
import axios from 'axios'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const profile = (permissions = ['cluster.read'], extra = {}) => ({ id: 'viewer', department_id: 'department-a', project_id: 'project-a', is_superuser: false, permissions, ...extra })
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const tick = () => new Promise(resolve => setImmediate(resolve))

function harness() {
  let project = 'project-a', now = 10_000
  const requests = [], intervals = new Map(), timeouts = new Map(), events = []
  const window = new EventTarget(), document = new EventTarget()
  document.hidden = false
  window.setInterval = (callback, delay) => { intervals.set(1, { callback, delay }); return 1 }
  window.clearInterval = id => intervals.delete(id)
  window.setTimeout = (callback, delay) => { timeouts.set(1, { callback: () => { timeouts.delete(1); callback() }, delay }); return 1 }
  window.clearTimeout = id => timeouts.delete(id)
  window.addEventListener('edo:projects-changed', () => events.push('projects-changed'))
  const api = {
    __esModule: true,
    default: { get: async () => ({ data: { projects: [{ id: project, is_active: true }] } }), post: async () => {} },
    getCurrentUser: () => { const request = deferred(); requests.push(request); return request.promise },
    login: async () => {}, loginLDAP: async () => {}, logout: async () => {},
  }
  const module = loadFrontend('../src/stores/auth.ts', {
    vue, pinia: { defineStore: (_, factory) => factory }, axios: { __esModule: true, default: axios }, '@/api/client': api,
  }, { window, document, Date: { now: () => now }, localStorage: { getItem: () => project, setItem: (_, value) => { project = value }, removeItem: () => { project = '' } } })
  const store = module.useAuthStore()
  store.user.value = profile(); store.loaded.value = true
  return { store, requests, intervals, timeouts, window, document, events, select: value => { project = value }, advance: value => { now += value }, denied: value => window.dispatchEvent(new CustomEvent('edo:permissions-stale', { detail: { project: value ?? project } })) }
}

test('无业务通知权限的普通用户，403触发去重刷新并移除旧权限', async () => {
  const h = harness(), stop = h.store.startPermissionSync()
  try {
    h.denied(); h.denied(); h.window.dispatchEvent(new Event('focus'))
    assert.equal(h.requests.length, 1)
    assert.equal(h.intervals.get(1).delay, 60_000)
    h.requests[0].resolve(profile(['project.read'])); await tick()
    assert.equal(h.requests.length, 2)
    h.requests[1].resolve(profile(['project.read'])); await tick()
    assert.equal(h.store.canAny(['cluster.read']), false)
    assert.equal(h.store.canAny(['project.read']), true)
    assert.equal(h.store.user.value.id, 'viewer')
  } finally { stop() }
})

test('同一范围多个刷新入口共用一次请求，成功响应和权限集合正常更新', async () => {
  const h = harness()
  const one = h.store.refreshPermissions(), two = h.store.refreshPermissions()
  assert.equal(h.requests.length, 1)
  h.requests[0].resolve(profile(['project.read', 'kubernetes.workload.read']))
  await Promise.all([one, two])
  assert.deepEqual([...h.store.permissions.value], ['project.read', 'kubernetes.workload.read'])
})

test('短期重复403被有界延后核验而不是静默丢弃，停止时取消待处理核验', async () => {
  const h = harness(), stop = h.store.startPermissionSync()
  h.denied(); h.requests[0].resolve(profile()); await tick()
  h.denied(); h.denied()
  assert.equal(h.requests.length, 1); assert.equal(h.timeouts.size, 1)
  h.advance(1000); h.timeouts.get(1).callback()
  assert.equal(h.requests.length, 2)
  h.requests[1].resolve(profile(['project.read'])); await tick()
  h.denied(); stop(); assert.equal(h.timeouts.size, 0)
})

test('慢身份响应期间发生403，旧权限快照丢弃并串行尾随核验，不等60秒校准', async () => {
  const h = harness(), stop = h.store.startPermissionSync()
  try {
    h.window.dispatchEvent(new Event('focus'))
    h.advance(2000); h.denied(); h.denied()
    assert.equal(h.requests.length, 1)
    // 旧请求的权限快照在撤权前生成，不能重新加回已经失效的终端权限。
    h.requests[0].resolve(profile(['cluster.read', 'host.terminal.open'])); await tick()
    assert.equal(h.store.canAny(['host.terminal.open']), false)
    assert.equal(h.requests.length, 2)
    h.requests[1].resolve(profile(['project.read'])); await tick()
    assert.equal(h.store.canAny(['cluster.read']), false)
    assert.equal(h.store.canAny(['project.read']), true)
  } finally { stop() }
})

test('一秒内发生的拒绝也使在途快照失效，多次突发仅合并一次尾随请求', async () => {
  const h = harness(), stop = h.store.startPermissionSync()
  try {
    h.window.dispatchEvent(new Event('focus')); h.advance(200)
    for (let index = 0; index < 30; index++) h.denied()
    assert.equal(h.requests.length, 1); assert.equal(h.timeouts.size, 0)
    h.requests[0].resolve(profile(['host.terminal.open'])); await tick()
    assert.equal(h.requests.length, 2); assert.equal(h.store.canAny(['host.terminal.open']), false)
    h.requests[1].resolve(profile(['project.read'])); await tick()
    assert.equal(h.requests.length, 2); assert.equal(h.store.canAny(['cluster.read']), false)
  } finally { stop() }
})

test('卸载、切项目或换用户后在途dirty响应不落盘，也不发起尾随请求', async () => {
  for (const action of ['stop', 'project', 'user']) {
    const h = harness(), stop = h.store.startPermissionSync()
    h.window.dispatchEvent(new Event('focus')); h.advance(2000); h.denied()
    if (action === 'stop') stop()
    if (action === 'project') { h.select('project-b'); h.store.user.value = profile(['project.read'], { project_id: 'project-b' }) }
    if (action === 'user') h.store.user.value = profile(['project.read'], { id: 'other-user' })
    h.requests[0].resolve(profile(['host.terminal.open'])); await tick()
    assert.equal(h.requests.length, 1); assert.equal(h.store.canAny(['host.terminal.open']), false)
    stop()
  }
})

test('持续403每轮最多原请求加一次尾随，仍dirty时只延后一次，稳定后恢复同步', async () => {
  const h = harness(), stop = h.store.startPermissionSync()
  try {
    h.window.dispatchEvent(new Event('focus'))
    for (let attempt = 0; attempt < 2; attempt++) {
      h.advance(2000)
      for (let burst = 0; burst < 100; burst++) h.denied()
      h.requests[attempt].resolve(profile(['host.terminal.open'])); await tick()
    }
    assert.equal(h.requests.length, 2)
    assert.equal(h.store.canAny(['host.terminal.open']), false)
    assert.equal(h.timeouts.size, 1); assert.equal(h.timeouts.get(1).delay, 1000)
    h.denied(); h.window.dispatchEvent(new Event('focus'))
    assert.equal(h.requests.length, 2); assert.equal(h.timeouts.size, 1)
    h.advance(1000); h.timeouts.get(1).callback()
    assert.equal(h.requests.length, 3)
    h.requests[2].resolve(profile(['project.read'])); await tick()
    assert.equal(h.requests.length, 3); assert.equal(h.timeouts.size, 0)
    assert.equal(h.store.canAny(['cluster.read']), false)
  } finally { stop() }
})

test('项目切换后旧身份响应不能恢复旧权限，也不能清理新项目身份', async () => {
  for (const failed of [false, true]) {
    const h = harness(), old = h.store.refreshPermissions()
    h.select('project-b'); h.store.user.value = profile(['project.read'], { project_id: 'project-b' })
    if (failed) h.requests[0].reject(new axios.AxiosError('denied', undefined, undefined, undefined, { status: 401 }))
    else h.requests[0].resolve(profile(['cluster.read', 'host.terminal.open']))
    await old
    assert.equal(h.store.user.value.project_id, 'project-b')
    assert.deepEqual([...h.store.permissions.value], ['project.read'])
  }
})

test('用户或部门切换后旧权限请求不回写', async () => {
  for (const identity of [{ id: 'other' }, { department_id: 'department-b' }]) {
    const h = harness(), old = h.store.refreshPermissions()
    h.store.user.value = profile(['project.read'], identity)
    h.requests[0].resolve(profile(['host.terminal.open'])); await old
    assert.deepEqual([...h.store.permissions.value], ['project.read'])
  }
})

test('服务端身份发生变化时清理旧页面会话，不把不同用户或部门权限混在一起', async () => {
  const h = harness(), pending = h.store.refreshPermissions()
  h.requests[0].resolve(profile(['host.terminal.open'], { id: 'other-user' })); await pending
  assert.equal(h.store.user.value, null)
  assert.equal(h.store.loaded.value, true)
})

test('退出和新登录使旧加载、旧刷新失效，不复活旧会话', async () => {
  const h = harness(), old = h.store.refreshPermissions()
  await h.store.logout()
  assert.equal(h.store.user.value, null)
  h.requests[0].resolve(profile(['host.terminal.open'])); await old
  assert.equal(h.store.user.value, null)
  const login = h.store.login('new-user', 'public-test-value'); await tick()
  h.requests[1].resolve(profile(['project.read'], { id: 'new-user' })); await login
  assert.equal(h.store.user.value.id, 'new-user')
  assert.deepEqual([...h.store.permissions.value], ['project.read'])
})

test('刷新401退出当前身份；403只清当前权限并保留其他项目的登录会话', async () => {
  for (const status of [401, 403]) {
    const h = harness(), pending = h.store.refreshPermissions()
    h.requests[0].reject(new axios.AxiosError('denied', undefined, undefined, undefined, { status }))
    await assert.rejects(pending)
    if (status === 401) assert.equal(h.store.user.value, null)
    else {
      assert.equal(h.store.user.value.id, 'viewer')
      assert.deepEqual(h.store.user.value.permissions, [])
      assert.deepEqual(h.events, ['projects-changed'])
    }
  }
})

test('网络失败保留有效身份，不伪造撤权或登出，后续可重新校准', async () => {
  const h = harness(), pending = h.store.refreshPermissions()
  h.requests[0].reject(new axios.AxiosError('offline'))
  await assert.rejects(pending)
  assert.equal(h.store.canAny(['cluster.read']), true)
  const retry = h.store.refreshPermissions()
  h.requests[1].resolve(profile(['project.read'])); await retry
  assert.equal(h.store.canAny(['cluster.read']), false)
})

test('当前项目持续403不会递归触发项目选择器刷新或反复发出范围变更事件', async () => {
  const h = harness()
  for (let index = 0; index < 2; index++) {
    const pending = h.store.refreshPermissions()
    h.requests[index].reject(new axios.AxiosError('denied', undefined, undefined, undefined, { status: 403 }))
    await assert.rejects(pending)
  }
  assert.deepEqual(h.events, ['projects-changed'])
  assert.equal(h.store.user.value.id, 'viewer')
})

test('可见性与低频校准独立于业务列表，旧项目通知、隐藏页及卸载后不刷新', async () => {
  const h = harness(), stop = h.store.startPermissionSync()
  h.denied('project-b'); assert.equal(h.requests.length, 0)
  h.document.hidden = true; h.intervals.get(1).callback(); assert.equal(h.requests.length, 0)
  h.document.hidden = false; h.document.dispatchEvent(new Event('visibilitychange'))
  h.requests[0].resolve(profile()); await tick()
  h.advance(60_000); h.intervals.get(1).callback()
  assert.equal(h.requests.length, 2)
  h.requests[1].resolve(profile()); await tick()
  stop(); h.advance(60_000); h.denied(); h.window.dispatchEvent(new Event('focus'))
  assert.equal(h.requests.length, 2); assert.equal(h.intervals.size, 0)
})

test('初始化加载在退出后返回不能覆盖退出状态或恢复项目', async () => {
  const h = harness()
  h.store.loaded.value = false; h.store.user.value = null
  const load = h.store.ensureLoaded(); await tick()
  await h.store.logout(); h.requests[0].resolve(profile()); await load
  assert.equal(h.store.user.value, null)
  assert.equal(h.store.loaded.value, true)
})
