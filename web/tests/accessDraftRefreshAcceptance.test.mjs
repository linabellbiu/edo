import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import { componentHarness } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const profile = () => ({ id: 'operator', department_id: 'department-a', project_id: 'project-a', is_superuser: false, permissions: ['user.read', 'user.create', 'role.read', 'role.update', 'department.read'] })
const editors = ['userFormOpen', 'departmentFormOpen', 'roleFormOpen', 'userDepartmentOpen', 'accessOpen']
function harness(t, api = {}) {
  let selectedProject = 'project-a'
  const identity = vue.reactive({ user: profile(), authorizationState: 'ready' })
  const auth = {
    get user() { return identity.user },
    get authorizationState() { return identity.authorizationState },
    get permissions() { return new Set(identity.user?.permissions ?? []) },
    canAny: codes => identity.authorizationState === 'ready' && codes.some(code => identity.user?.permissions.includes(code)),
  }
  const h = componentHarness('../src/views/AccessView.vue', {
    query: { view: 'users' }, api, modules: { '@/stores/auth': { useAuthStore: () => auth } },
    globals: { localStorage: { getItem: () => selectedProject } },
  })
  t.after(h.stop)
  for (const key of editors) h.state[key].value = true
  Object.assign(h.state.userForm, { username: 'draft', nickname: '未提交', password: 'draft-only-value', department_id: 'department-a', role_ids: ['reader'] })
  h.state.roleForm.display_name = '待保存角色'
  h.state.departmentForm.name = '待保存部门'
  h.state.editingUser.value = { id: 'target-user' }
  h.state.accessForm.effects['user.read'] = 'deny'
  return { ...h, identity, select: project => { selectedProject = project } }
}

test('同一身份权限短暂核验保留真实表单草稿，但同步阻断提交并关闭旧下拉及确认框', async t => {
  const h = harness(t)
  const draft = structuredClone({ ...h.state.userForm, role_ids: [...h.state.userForm.role_ids] })
  h.state.userSelectOpen.roles = true
  h.confirmations.push({ title: '旧操作确认' })
  h.identity.authorizationState = 'checking'
  assert.ok(editors.every(key => h.state[key].value))
  assert.deepEqual({ ...h.state.userForm }, draft)
  assert.equal(h.state.userSelectOpen.roles, false)
  assert.equal(h.confirmations.length, 0)
  assert.equal(h.state.canAny(['user.create']), false)
  await h.state.createUser()
  assert.equal(h.requests.filter(request => request.method !== 'get').length, 0)
  h.identity.user = { ...profile(), permissions: [...profile().permissions].reverse() }
  h.identity.authorizationState = 'ready'
  await settle()
  assert.ok(editors.every(key => h.state[key].value))
  assert.deepEqual({ ...h.state.userForm }, draft)
  assert.equal(h.state.roleForm.display_name, '待保存角色')
  assert.equal(h.state.departmentForm.name, '待保存部门')
  assert.equal(h.state.editingUser.value.id, 'target-user')
  assert.equal(h.state.accessForm.effects['user.read'], 'deny')
  assert.ok(h.requests.some(request => request.path.startsWith('/users?')))
})

test('checking 转为 unavailable 即使布尔就绪值没变，也必须关闭原表单', async t => {
  const h = harness(t)
  h.identity.authorizationState = 'checking'
  assert.equal(h.state.userFormOpen.value, true)
  h.identity.authorizationState = 'unavailable'
  assert.ok(editors.every(key => !h.state[key].value))
  assert.equal(h.state.editingUser.value, null)
  h.identity.authorizationState = 'ready'
  await settle()
  assert.ok(editors.every(key => !h.state[key].value), '恢复连接不能自动重开已失效的表单')
})

for (const refreshing of [false, true]) test(`${refreshing ? '短暂核验中' : '周期刷新时'}实际身份或有效权限改变，旧编辑上下文立即关闭`, async t => {
  for (const field of ['id', 'department_id', 'project_id', 'is_superuser', 'permissions', 'session', 'selected-project']) await t.test(field, async t => {
    const h = harness(t)
    if (refreshing) h.identity.authorizationState = 'checking'
    if (field === 'session') h.identity.user = null
    else if (field === 'selected-project') { h.select('project-b'); h.identity.authorizationState = 'checking'; h.identity.user = { ...h.identity.user } }
    else h.identity.user = { ...h.identity.user, [field]: field === 'permissions' ? ['user.read'] : field === 'is_superuser' ? true : 'other' }
    assert.ok(editors.every(key => !h.state[key].value), field)
    assert.equal(h.state.editingUser.value, null)
    h.identity.authorizationState = 'ready'
    await settle()
    assert.ok(editors.every(key => !h.state[key].value))
  })
})
