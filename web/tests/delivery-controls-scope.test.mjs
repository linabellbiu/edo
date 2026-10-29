import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const permissions = ['application_database.read', 'application_database.create', 'application_database.update', 'application.read', 'deployment.read', 'environment_policy.read', 'environment_policy.update']
const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const deferred = () => { let resolve; const promise = new Promise(yes => { resolve = yes }); return { promise, resolve } }

async function harness({ routeName = 'application-databases', get } = {}) {
  const requests = [], unmounts = []
  const auth = vue.reactive({
    user: { id: 'operator-a', department_id: 'department-a', project_id: 'project-a', permissions: [...permissions], is_superuser: false },
    canAny(required) { return !!this.user && (this.user.is_superuser || required.some(code => this.user.permissions.includes(code))) },
  })
  const route = vue.reactive({ name: routeName, query: {} })
  const client = { async get(path) {
    requests.push({ method: 'get', path })
    if (get) { const response = await get(path); if (response) return response }
    return { data: path === '/application-databases' ? { databases: [{ id: 'db-a', name: '数据库 A' }] } : path === '/environment-policies' ? { policies: [] } : path === '/applications' ? { applications: [{ id: 'app-a', name: '应用 A' }] } : path === '/environments' ? { environments: [{ id: 'env-a', name: '环境 A' }] } : path === '/environment-policies/reviewers' ? { reviewers: [] } : { migrations: [] } }
  } }
  const component = loadFrontend('../src/views/DeliveryControlsView.vue', {
    vue: { ...vue, onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-router': { useRoute: () => route, useRouter: () => ({ async replace() {} }) },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/api/client': { __esModule: true, default: client },
    '@/api/resources': { apiErrorMessage: () => '操作失败' },
    'ant-design-vue': { Modal: { confirm() {} }, message: { success() {}, error() {}, warning() {} } },
  }).default
  const scope = vue.effectScope(); let state
  scope.run(() => { state = component.setup({}, { expose() {} }) })
  await settle()
  return { state, auth, route, requests, stop() { unmounts.forEach(fn => fn()); scope.stop() } }
}

test('相同身份权限校准不关闭数据库抽屉、不清密码草稿或触发额外读取', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.editDatabase(); h.state.dbForm.name = '尚未保存的名称'; h.state.dbForm.password = '仅测试草稿'
  await settle()
  h.state.tested.value = true; h.state.historyOpen.value = true
  const count = h.requests.length
  h.auth.user = { ...h.auth.user, permissions: [...h.auth.user.permissions].reverse() }
  await settle()
  assert.equal(h.state.open.value, true)
  assert.equal(h.state.dbForm.name, '尚未保存的名称')
  assert.equal(h.state.dbForm.password, '仅测试草稿')
  assert.equal(h.state.tested.value, true)
  assert.equal(h.state.historyOpen.value, true)
  assert.equal(h.requests.length, count)
})

test('同一环境保护身份校准保留未保存审核规则与抽屉', async t => {
  const h = await harness({ routeName: 'environment-policies' }); t.after(h.stop)
  h.state.editPolicy(); h.state.policyForm.allowed_refs = 'refs/heads/release/*'
  h.auth.user = { ...h.auth.user, permissions: [...h.auth.user.permissions] }
  await settle()
  assert.equal(h.state.open.value, true)
  assert.equal(h.state.policyForm.allowed_refs, 'refs/heads/release/*')
})

test('实际用户、部门、项目、页面或相关权限变化仍关闭抽屉并清密码', async t => {
  for (const update of [
    h => { h.auth.user.id = 'operator-b' },
    h => { h.auth.user.department_id = 'department-b' },
    h => { h.auth.user.is_superuser = true },
    h => { h.auth.user.project_id = 'project-b' },
    h => { h.route.name = 'environment-policies' },
    h => { h.auth.user.permissions = h.auth.user.permissions.filter(code => code !== 'application_database.create') },
    h => { h.auth.user.permissions = h.auth.user.permissions.filter(code => code !== 'application_database.update') },
    h => { h.auth.user.permissions = h.auth.user.permissions.filter(code => code !== 'application_database.read') },
    h => { h.auth.user = null },
  ]) {
    const h = await harness(); t.after(h.stop)
    h.state.editDatabase(); h.state.dbForm.password = '旧范围密码'; h.state.historyOpen.value = true
    update(h); await settle()
    assert.equal(h.state.open.value, false)
    assert.equal(h.state.historyOpen.value, false)
    assert.equal(h.state.dbForm.password, '')
  }
})

test('真实项目切换后旧列表响应失效，不能覆盖新范围', async t => {
  const pending = deferred(); let reads = 0
  const h = await harness({ get: path => path === '/application-databases' && reads++ === 0 ? pending.promise : undefined }); t.after(h.stop)
  h.auth.user.project_id = 'project-b'; await settle()
  pending.resolve({ data: { databases: [{ id: 'old-db', name: '旧项目' }] } }); await settle()
  assert.deepEqual(h.state.databases.value.map(row => row.id), ['db-a'])
})

test('超级管理员降级即使保留相关动作权限也关闭敏感草稿', async t => {
  const h = await harness(); t.after(h.stop)
  h.auth.user.is_superuser = true; await settle()
  h.state.editDatabase(); h.state.dbForm.password = '降级前草稿'
  h.auth.user = { ...h.auth.user, is_superuser: false }; await settle()
  assert.equal(h.state.open.value, false)
  assert.equal(h.state.dbForm.password, '')
})
