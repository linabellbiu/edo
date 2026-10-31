import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import { componentHarness } from './acceptanceHarness.mjs'

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')
function compile(descriptor, modules = {}) {
  const script = compileScript(descriptor, { id: 'role-detail-render', inlineTemplate: true }).content
  const output = ts.transpileModule(script, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output)(name => {
    if (name === 'vue') return vue
    assert.ok(Object.hasOwn(modules, name), `未登记的角色详情测试依赖 ${name}`)
    return modules[name]
  }, module, module.exports)
  return module.exports.default
}
const ResourceDetailDrawer = compile(parse(read('../src/components/ResourceDetailDrawer.vue')).descriptor)
const ResourceTable = compile(parse(read('../src/components/ResourceTable.vue')).descriptor, {
  'vue-i18n': { useI18n: () => ({ t: key => key }) },
  '@/components/ResourceDetailDrawer.vue': { __esModule: true, default: ResourceDetailDrawer },
})
const cards = []
function visit(node) {
  if (node.tag === 'div' && node.children?.some(child => child.tag === 'ResourceTable' && child.props.some(prop => prop.type === 7 && prop.name === 'bind' && prop.arg?.content === 'columns' && prop.exp?.content === 'roleColumns'))) cards.push(node)
  for (const child of node.children || []) visit(child)
}
visit(parse(read('../src/views/AccessView.vue')).descriptor.template.ast)
assert.equal(cards.length, 1, '必须使用 AccessView 包含角色、用户和审计表的真实模板分支')
const template = compileTemplate({ source: cards[0].loc.source, filename: 'AccessView.vue', id: 'role-detail-render', compilerOptions: { expressionPlugins: ['typescript'] } })
assert.deepEqual(template.errors, [])
const rendered = { exports: {} }
new Function('require', 'exports', ts.transpileModule(template.code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)(name => { assert.equal(name, 'vue'); return vue }, rendered.exports)

const node = (tag, text = '') => ({ tag, text, props: {}, children: [], parent: null })
const renderer = vue.createRenderer({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: () => node('#comment'),
  setText: (target, text) => { target.text = text }, setElementText: (target, text) => { target.text = text; target.children = [] },
  patchProp: (target, key, _previous, value) => { target.props[key] = value },
  parentNode: target => target.parent, nextSibling: target => target.parent?.children[target.parent.children.indexOf(target) + 1] ?? null,
  remove(target) { if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1); target.parent = null },
  insert(target, parent, anchor = null) { if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1); const index = anchor ? parent.children.indexOf(anchor) : -1; parent.children.splice(index < 0 ? parent.children.length : index, 0, target); target.parent = parent },
})
const walk = root => [root, ...root.children.flatMap(walk)]
const text = root => root.tag === '#comment' ? '' : root.text + root.children.map(text).join('')
const permissionCodes = Array.from({ length: 27 }, (_, index) => `test.permission.${index}`)
const role = permissions => ({ id: 'role-a', name: 'test_reader', display_name: '测试角色', description: '只读角色', permissions, in_use: false })

function mount(t, view, row, options = {}) {
  const h = componentHarness('../src/views/AccessView.vue', { query: { view }, codes: ['role.read', 'user.read', 'audit.read'], ...options })
  t.after(h.stop)
  if (view === 'roles') h.state.roles.value = [row]
  else if (view === 'users') h.state.users.value = [row]
  else h.state.audits.value = [row]
  const root = node('root'), app = renderer.createApp({ setup: () => ({ ...h.state }), render: rendered.exports.render })
  app.component('ResourceTable', ResourceTable)
  app.component('MoreHorizontal', () => vue.h('svg'))
  for (const name of ['a-spin', 'a-tag', 'a-tooltip', 'a-button', 'a-pagination', 'a-empty', 'a-alert', 'a-space', 'a-dropdown', 'a-menu', 'a-menu-item', 'a-menu-divider']) app.component(name, { inheritAttrs: false, setup: (_props, { attrs, slots }) => () => vue.h(name === 'a-button' ? 'button' : 'span', attrs, slots.default?.()) })
  app.component('a-drawer', { props: ['open', 'title'], emits: ['update:open'], setup: (props, { slots, emit }) => () => props.open ? vue.h('aside', [vue.h('button', { onClick: () => emit('update:open', false) }, '关闭详情'), props.title, slots.default?.()]) : null })
  app.mount(root)
  t.after(() => app.unmount())
  const buttons = () => walk(root).filter(item => item.tag === 'button')
  return {
    ...h, buttons,
    listText: () => text(walk(root).find(item => item.tag === 'table')),
    async open() {
      assert.equal(walk(root).some(item => item.tag === 'aside'), false)
      const button = buttons().find(item => text(item) === 'resourceList.details')
      assert.ok(button?.props.onClick, '必须从真实 ResourceTable 详情按钮打开')
      button.props.onClick(); await vue.nextTick()
      const drawer = walk(root).find(item => item.tag === 'aside')
      assert.ok(drawer, '真实 ResourceDetailDrawer 必须打开')
      assert.equal(h.requests.length, 0, '查看计数不能修改权限或提交请求')
      return Object.fromEntries(walk(drawer).filter(item => item.tag === 'dt').map(term => [text(term), text(term.parent.children.find(item => item.tag === 'dd')).trim()]))
    },
    async close() { buttons().find(item => text(item) === '关闭详情').props.onClick(); await vue.nextTick() },
  }
}

test('角色真实列表与详情权限数量一致，27项不会展开权限码且关闭重开仍正确', async t => {
  const page = mount(t, 'roles', role(permissionCodes))
  assert.match(page.listText(), /27 项/)
  assert.equal((await page.open())['权限数量'], '27 项')
  await page.close()
  const reopened = await page.open()
  assert.equal(reopened['权限数量'], '27 项')
  assert.doesNotMatch(Object.values(reopened).join(' '), /test\.permission\./)
})

test('角色空权限和缺失权限在真实详情中与列表使用相同的0项回退', async t => {
  for (const [label, permissions] of [['空数组', []], ['undefined', undefined], ['null', null]]) await t.test(label, async t => {
    const page = mount(t, 'roles', role(permissions))
    assert.match(page.listText(), /0 项/)
    assert.equal((await page.open())['权限数量'], '0 项')
  })
})

test('局部角色计数插槽不改变用户和审计详情的通用数组展示', async t => {
  const user = mount(t, 'users', { id: 'user-a', username: 'reader', nickname: '成员', is_superuser: false, is_active: true, role_ids: ['role-a', 'role-b'], effective_permissions: ['application.read', 'workflow.read'] })
  const userFacts = await user.open()
  assert.equal(userFacts['角色'], 'role-a、role-b')
  assert.equal(userFacts['有效权限'], 'application.read、workflow.read')
  assert.equal(Object.hasOwn(userFacts, '权限数量'), false)
  const audit = mount(t, 'audit', { id: 'audit-a', action: 'read', resource_type: 'test', resource_id: ['target-a', 'target-b'], result: 'success' })
  const auditFacts = await audit.open()
  assert.equal(auditFacts['资源 ID'], 'target-a、target-b')
  assert.equal(Object.hasOwn(auditFacts, '权限数量'), false)
})

test('角色权限配置按钮保留原有草稿入口，不因计数展示发起保存', async t => {
  const page = mount(t, 'roles', role(permissionCodes), { codes: ['role.read', 'role.update'], superuser: true })
  const button = page.buttons().find(item => text(item) === '权限配置')
  assert.ok(button?.props.onClick)
  assert.equal(button.props.disabled, false)
  button.props.onClick(); await vue.nextTick()
  assert.equal(page.state.roleFormOpen.value, true)
  assert.equal(page.state.roleEditorMode.value, 'permissions')
  assert.deepEqual(page.state.roleForm.permissions, permissionCodes)
  assert.equal(page.requests.length, 0)
})
