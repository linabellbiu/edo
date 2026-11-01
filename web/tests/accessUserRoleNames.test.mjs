import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileTemplate, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import { Tag } from 'ant-design-vue'
import ts from 'typescript'
import * as vue from 'vue'
import { componentHarness } from './acceptanceHarness.mjs'

const descriptor = parse(readFileSync(new URL('../src/views/AccessView.vue', import.meta.url), 'utf8')).descriptor
const cells = []
function visit(node) {
  if (node.tag === 'template' && node.props.some(prop => prop.type === 7 && prop.name === 'slot' && prop.arg?.content === 'cell-role_ids')) cells.push(node)
  for (const child of node.children || []) visit(child)
}
visit(descriptor.template.ast)
assert.equal(cells.length, 1)
const template = compileTemplate({ source: `<ResourceTable :rows="filteredUsers">${cells[0].loc.source}</ResourceTable>`, filename: 'AccessView.vue', id: 'access-role-names', compilerOptions: { expressionPlugins: ['typescript'] } })
assert.deepEqual(template.errors, [])
const rendered = { exports: {} }
new Function('require', 'exports', ts.transpileModule(template.code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)(name => { assert.equal(name, 'vue'); return vue }, rendered.exports)
const table = { props: ['rows'], setup: (props, { slots }) => () => vue.h('div', props.rows.map(row => vue.h('section', slots['cell-role_ids']({ row })))) }
const user = (id, roleIDs, roleNames = {}) => ({ id, username: id, nickname: '成员', department_id: 'department-a', department_name: '研发', is_superuser: false, is_active: true, role_ids: roleIDs, role_names: roleNames, permission_overrides: { allow: [], deny: [] }, effective_permissions: [] })
function harness(t, items) {
  const h = componentHarness('../src/views/AccessView.vue', { codes: ['user.read'], query: { view: 'users' }, api: { get: async path => { assert.ok(path.startsWith('/users?')); return { data: { users: items } } } } })
  t.after(h.stop)
  return h
}
async function actualRoles(h) {
  const app = vue.createSSRApp({ setup: () => ({ ...h.state }), render: rendered.exports.render })
  app.component('ResourceTable', table)
  app.component('a-tag', Tag)
  return renderToString(app)
}

test('只读用户使用绑定名称渲染真实角色标签，不读取完整角色及权限目录', async t => {
  const id = 'ef6f94cd-67f3-4a54-b508-07a34b4833e6'
  const h = harness(t, [user('member', [id], { [id]: '发布审核员' })])
  await h.state.refresh()
  assert.deepEqual(h.requests.map(item => item.path), ['/users?limit=200&offset=0'])
  assert.deepEqual(h.state.roles.value, [])
  const html = await actualRoles(h)
  assert.match(html, /发布审核员/)
  assert.doesNotMatch(html, new RegExp(id))
  assert.equal(h.state.canAny(['user.create', 'user.update', 'role.read']), false)
  assert.equal(h.state.canManageUserAccess(h.state.users.value[0]), false)
  assert.deepEqual(h.state.delegableRoleOptions.value, [])
})

test('角色搜索和筛选只使用当前可见用户的绑定名称，缺失名称不回显 UUID', async t => {
  const h = harness(t, [user('first', ['r1'], { r1: '发布审核员', unbound: '不能展示' }), user('second', ['r2'], { r2: '研发成员' }), user('missing', ['r3'])])
  await h.state.refresh()
  assert.deepEqual(h.state.userRoleOptions.value, [{ value: 'r1', label: '发布审核员' }, { value: 'r2', label: '研发成员' }, { value: 'r3', label: '未知角色' }])
  h.state.userQuery.value = '审核'
  assert.deepEqual(h.state.filteredUsers.value.map(item => item.id), ['first'])
  h.state.userRoleID.value = 'r2'
  assert.equal(h.state.filteredUsers.value.length, 0)
  h.state.userQuery.value = ''
  assert.deepEqual(h.state.filteredUsers.value.map(item => item.id), ['second'])
  h.state.userRoleID.value = 'r3'
  const html = await actualRoles(h)
  assert.match(html, /未知角色/)
  assert.doesNotMatch(html, /r3|不能展示/)
})

test('列表刷新同步更新名称和筛选选项，名称摘要不作为可委派权限来源', async t => {
  const h = harness(t, [user('first', ['r1'], { r1: '旧名称' }), user('second', ['r1'], { r1: '旧名称' })])
  await h.state.refresh()
  assert.deepEqual(h.state.userRoleOptions.value, [{ value: 'r1', label: '旧名称' }])
  assert.match(await actualRoles(h), /旧名称/)
  h.state.users.value = [user('first', ['r1'], { r1: '新名称' })]
  h.state.userQuery.value = '新名称'
  assert.equal(h.state.filteredUsers.value.length, 1)
  assert.deepEqual(h.state.userRoleOptions.value, [{ value: 'r1', label: '新名称' }])
  h.auth.permissions.add('user.update')
  assert.equal(h.state.canManageUserAccess(h.state.users.value[0]), false)
  assert.deepEqual(h.state.delegableRoleOptions.value, [])
  h.state.users.value = []
  assert.deepEqual(h.state.userRoleOptions.value, [])
})
