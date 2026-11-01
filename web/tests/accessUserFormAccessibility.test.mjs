import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileStyle, compileTemplate, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import { Form, FormItem, Input, InputPassword, Select } from 'ant-design-vue'
import postcss from 'postcss'
import ts from 'typescript'
import * as vue from 'vue'
import { componentHarness } from './acceptanceHarness.mjs'

const source = readFileSync(new URL('../src/views/AccessView.vue', import.meta.url), 'utf8')
const descriptor = parse(source).descriptor, modals = []
function visit(node) {
  if (node.tag === 'a-modal' && node.props.some(prop => prop.type === 7 && prop.name === 'model' && prop.exp?.content === 'userFormOpen')) modals.push(node)
  for (const child of node.children || []) visit(child)
}
visit(descriptor.template.ast)
assert.equal(modals.length, 1, '只编译 AccessView 创建用户的真实 modal 分支')
const template = compileTemplate({ source: modals[0].loc.source, filename: 'AccessView.vue', id: 'access-user-a11y', compilerOptions: { expressionPlugins: ['typescript'] } })
assert.deepEqual(template.errors, [])
const rendered = { exports: {} }
new Function('require', 'exports', ts.transpileModule(template.code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)(name => { assert.equal(name, 'vue'); return vue }, rendered.exports)
const fields = ['username', 'nickname', 'department_id', 'password', 'role_ids']
const modal = { props: ['open', 'wrapClassName'], inheritAttrs: false, setup: (props, { slots }) => () => props.open ? vue.h('section', { class: props.wrapClassName }, slots.default?.()) : null }
const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
function harness(t, { superuser = true, api = {} } = {}) {
  const auth = vue.reactive({ user: { id: 'operator', department_id: 'department-a', is_superuser: superuser }, permissions: new Set(['user.read', 'user.create']), authorizationState: 'ready', canAny: codes => auth.user.is_superuser || codes.some(code => auth.permissions.has(code)) })
  const h = componentHarness('../src/views/AccessView.vue', { query: { view: 'users' }, api, modules: { '@/stores/auth': { useAuthStore: () => auth } } })
  t.after(h.stop)
  h.state.departments.value = [{ id: 'department-a', name: '研发', is_active: true }, { id: 'department-b', name: '测试', is_active: true }]
  h.state.roles.value = [{ id: 'reader', name: 'reader', display_name: '只读', permissions: [] }, { id: 'writer', name: 'writer', display_name: '编辑', permissions: [] }]
  h.state.resetUser(); h.state.userFormOpen.value = true
  return { ...h, auth }
}
async function actualForm(h, warnings) {
  const app = vue.createSSRApp({ setup: () => ({ ...h.state }), render: rendered.exports.render })
  if (warnings) app.config.warnHandler = value => warnings.push(value)
  for (const [name, component] of Object.entries({ 'a-modal': modal, 'a-form': Form, 'a-form-item': FormItem, 'a-input': Input, 'a-input-password': InputPassword, 'a-select': Select })) app.component(name, component)
  return renderToString(app)
}
const attributes = tag => Object.fromEntries([...tag.matchAll(/([\w:-]+)(?:="([^"]*)")?/g)].map(([, key, value]) => [key, value ?? '']))
const inputs = html => [...html.matchAll(/<input\b[^>]*>/g)].map(([tag]) => attributes(tag))
test('创建用户真实 Ant 表单五个可见标签关联唯一输入，空值及多选已有值均有名称', async t => {
  for (const roles of [[], ['reader', 'writer']]) await t.test(`${roles.length}个角色`, async t => {
    const h = harness(t); h.state.userForm.role_ids = roles
    const html = await actualForm(h), controls = inputs(html)
    assert.match(html, /<section class="access-user-modal">/)
    const labels = [...html.matchAll(/<label\b([^>]*)>([\s\S]*?)<\/label>/g)].map(([, attrs, text]) => ({ ...attributes(attrs), text: text.replace(/<[^>]+>/g, '').trim() }))
    assert.equal(labels.length, 5)
    assert.deepEqual(labels.map(label => label.for), fields.map(field => `access-user_${field}`))
    assert.equal(new Set(labels.map(label => label.for)).size, 5)
    for (const label of labels) { assert.ok(label.text); assert.equal(controls.filter(control => control.id === label.for).length, 1) }
    assert.equal(controls.find(control => control.id === 'access-user_password').type, 'password')
    assert.equal(controls.find(control => control.id === 'access-user_username').maxlength, '32')
    const selects = controls.filter(control => control.role === 'combobox')
    assert.equal(selects.length, 2)
    assert.ok(selects.every(control => control['aria-expanded'] === 'false'))
    assert.ok(selects.every(control => control['aria-haspopup'] === 'listbox'))
  })
})

test('普通管理员的真实部门输入仍禁用，不能因语义修复扩大部门或角色权限', async t => {
  const h = harness(t, { superuser: false })
  const controls = inputs(await actualForm(h))
  const department = controls.find(control => control.id === 'access-user_department_id')
  assert.ok(department)
  assert.equal(Object.hasOwn(department, 'disabled'), true)
  assert.equal(h.state.userForm.department_id, 'department-a')
  h.state.roles.value = [{ id: 'forbidden', name: 'forbidden', display_name: '不可委派', permissions: ['config.update'] }]
  assert.equal(h.state.delegableRoleOptions.value[0].disabled, true)
})

test('真实 Ant maxlength 数字契约不产生类型告警，用户名昵称密码限制保持原值', async t => {
  const h = harness(t), warnings = [], controls = inputs(await actualForm(h, warnings))
  assert.deepEqual(warnings, [])
  assert.deepEqual(['username', 'nickname', 'password'].map(field => controls.find(control => control.id === `access-user_${field}`).maxlength), ['32', '64', '128'])
  assert.equal(controls.find(control => control.id === 'access-user_password').minlength, '12')
})

test('真实 Select 初始关闭态保留 combobox 语义并采样 IDREF，不冒充 popup/axe 验收', async t => {
  const h = harness(t), html = await actualForm(h)
  const ids = new Set([...html.matchAll(/\bid="([^"]+)"/g)].map(([, id]) => id))
  const controls = inputs(html).filter(control => control.role === 'combobox')
  assert.equal(controls.length, 2)
  assert.ok(controls.every(control => control['aria-expanded'] === 'false'))
  assert.ok(controls.every(control => !control['aria-activedescendant']), '关闭的下拉没有活动选项，不应指向未挂载的选项')
  const metadata = controls.map(control => ({ id: control.id, expanded: control['aria-expanded'], references: ['aria-controls', 'aria-owns', 'aria-activedescendant'].map(attribute => ({ attribute, id: control[attribute], existsInSSR: ids.has(control[attribute]) })) }))
  t.diagnostic(JSON.stringify({ kind: 'closed-select-ssr-only', controls: metadata, browserPopupAndAxe: '未执行' }))
})

function publicSelects(t, h) {
  const bindings = [], remove = node => { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); node.parent = null }
  const renderer = vue.createRenderer({ createElement: () => ({ children: [] }), createText: () => ({}), createComment: () => ({}), setText() {}, setElementText() {}, patchProp() {}, parentNode: node => node.parent, nextSibling: () => null, remove, insert(node, parent) { remove(node); node.parent = parent; parent.children.push(node) } })
  const app = renderer.createApp({ setup: () => ({ ...h.state }), render: rendered.exports.render })
  app.component('a-modal', modal)
  for (const name of ['a-form', 'a-form-item', 'a-input', 'a-input-password']) app.component(name, { inheritAttrs: false, setup: (_props, { slots }) => () => vue.h('div', slots.default?.()) })
  app.component('a-select', { inheritAttrs: false, setup() { const instance = vue.getCurrentInstance(); bindings.push(() => instance.vnode.props); return () => null } })
  app.mount({ children: [] }); t.after(() => app.unmount())
  return () => bindings.map(binding => binding())
}
const escape = () => ({ key: 'Escape', stopped: false, prevented: false, stopPropagation() { this.stopped = true }, preventDefault() { this.prevented = true } })
test('用户表单局部 Select 只用公开 Boolean open 和事件，关闭/禁用/授权失效后不重开旧 popup', async t => {
  const h = harness(t), bindings = publicSelects(t, h)
  assert.deepEqual(bindings().map(binding => binding.open), [false, false])
  for (const index of [0, 1]) {
    bindings()[index].onDropdownVisibleChange(true); await settle()
    assert.equal(bindings()[index].open, true)
    const first = escape(); bindings()[index].onInputKeyDown(first); await settle()
    assert.equal(bindings()[index].open, false); assert.equal(first.stopped, true); assert.equal(first.prevented, true)
    const second = escape(); bindings()[index].onInputKeyDown(second)
    assert.equal(second.stopped, false); assert.equal(second.prevented, false)
    for (const key of ['ArrowDown', 'ArrowUp', 'Enter', 'Tab']) {
      const event = { ...escape(), key }; bindings()[index].onInputKeyDown(event)
      assert.equal(event.stopped, false); assert.equal(event.prevented, false)
    }
  }
  bindings()[0]['onUpdate:value']('department-b'); bindings()[1]['onUpdate:value'](['reader', 'writer']); await settle()
  assert.equal(h.state.userForm.department_id, 'department-b'); assert.deepEqual(h.state.userForm.role_ids, ['reader', 'writer'])
  bindings()[0].onDropdownVisibleChange(true); bindings()[1].onDropdownVisibleChange(true); await settle()
  h.state.userFormOpen.value = false; await settle(); h.state.userFormOpen.value = true; await settle()
  assert.deepEqual(bindings().slice(-2).map(binding => binding.open), [false, false])
  const current = () => bindings().slice(-2)
  current()[0].onDropdownVisibleChange(true); await settle(); h.auth.user.is_superuser = false; await settle()
  assert.equal(h.state.userFormOpen.value, false)
  h.state.userFormOpen.value = true; await settle()
  assert.equal(current()[0].open, false); assert.equal(current()[0].disabled, true)
  current()[0].onDropdownVisibleChange(true); await settle(); assert.equal(current()[0].open, false)
  current()[1].onDropdownVisibleChange(true); await settle(); h.auth.authorizationState = 'checking'; await settle()
  assert.equal(h.state.userFormOpen.value, true)
  assert.equal(current()[1].open, false); assert.equal(current()[1].disabled, true)
  h.auth.authorizationState = 'ready'; await settle()
  assert.deepEqual(current().map(binding => binding.open), [false, false])
})

test('短暂权限核验中的真实 Ant 用户输入全部禁用，恢复后重新可用而不丢草稿', async t => {
  const h = harness(t)
  h.state.userForm.nickname = '保留的输入'
  h.auth.authorizationState = 'checking'; await settle()
  assert.equal(h.state.userFormOpen.value, true)
  const paused = inputs(await actualForm(h))
  assert.equal(paused.length, 5)
  assert.ok(paused.every(control => Object.hasOwn(control, 'disabled')))
  h.auth.authorizationState = 'ready'; await settle()
  const resumed = inputs(await actualForm(h))
  assert.ok(resumed.every(control => !Object.hasOwn(control, 'disabled')))
  assert.equal(h.state.userForm.nickname, '保留的输入')
})

test('语义修复保留创建请求字段、成功关闭、失败保稿及授权未知不提交', async t => {
  const calls = [], h = harness(t, { api: { post: async (path, body) => { calls.push({ path, body: { ...body, role_ids: [...body.role_ids] } }); throw new Error('公开测试失败') } } })
  const draft = { username: 'public_user', nickname: '测试', department_id: 'department-a', password: 'public-test-value', role_ids: ['reader'] }
  Object.assign(h.state.userForm, draft)
  await h.state.createUser()
  assert.deepEqual(calls, [{ path: '/users', body: draft }])
  assert.equal(h.state.userFormOpen.value, true); assert.deepEqual({ ...h.state.userForm }, draft)
  h.auth.authorizationState = 'checking'; await settle(); await h.state.createUser(); assert.equal(calls.length, 1)
  const success = harness(t, { api: { post: async () => ({ data: {} }) } })
  Object.assign(success.state.userForm, draft); await success.state.createUser()
  assert.equal(success.state.userFormOpen.value, false); assert.equal(success.state.userForm.password, '')
})

const rgb = value => { const hex = value.replace('#', ''); return (hex.length === 3 ? [...hex].map(char => char + char).join('') : hex).match(/../g).map(part => parseInt(part, 16)) }
const luminance = color => rgb(color).map(value => value / 255).map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4).reduce((sum, value, index) => sum + value * [.2126, .7152, .0722][index], 0)
const ratio = (one, two) => (Math.max(luminance(one), luminance(two)) + .05) / (Math.min(luminance(one), luminance(two)) + .05)
test('用户 modal 局部编译样式复用主题 tokens，浅深色 placeholder/创建各状态理论对比度达标', () => {
  const compiled = compileStyle({ source: descriptor.styles[0].content, filename: 'AccessView.vue', id: 'data-v-access-user-a11y', scoped: true })
  assert.deepEqual(compiled.errors, [])
  const css = postcss.parse(compiled.code), rules = []
  css.walkRules(rule => { if (rule.selector.includes('.access-user-modal')) rules.push(rule) })
  const declaration = (rule, property) => rule.nodes.find(node => node.type === 'decl' && node.prop === property)?.value
  const placeholder = rules.find(rule => rule.selector === '.access-user-modal .ant-select-selection-placeholder')
  assert.ok(placeholder); assert.equal(declaration(placeholder, 'color'), 'var(--edo-muted)')
  const openedSelection = rules.find(rule => rule.selector === '.access-user-modal .ant-select-single.ant-select-open .ant-select-selection-item')
  assert.ok(openedSelection); assert.equal(declaration(openedSelection, 'color'), 'var(--edo-muted)')
  const button = rules.filter(rule => rule.selector.includes('.ant-modal-footer .ant-btn-primary:not(:disabled)'))
  assert.equal(button.length, 3)
  const base = button.find(rule => !rule.selector.endsWith(':hover') && !rule.selector.endsWith(':active'))
  assert.equal(declaration(base, 'color'), 'var(--edo-surface)')
  assert.equal(declaration(base, 'background'), 'var(--access-user-action-bg)')
  const themes = postcss.parse(readFileSync(new URL('../src/styles/vben.css', import.meta.url), 'utf8')).nodes.filter(node => node.type === 'rule' && [':root', ':root[data-theme="dark"]'].includes(node.selector))
  for (const theme of themes) {
    const token = key => declaration(theme, key)
    assert.ok(ratio(token('--edo-muted'), token('--edo-surface')) >= 4.5)
    for (const rule of button) {
      const weight = /var\(--edo-primary\)\s+(\d+)%/.exec(declaration(rule, '--access-user-action-bg'))
      assert.ok(weight)
      const fraction = Number(weight[1]) / 100
      const mixed = '#' + rgb(token('--edo-primary')).map((value, index) => Math.round(value * fraction + rgb(token('--edo-text'))[index] * (1 - fraction)).toString(16).padStart(2, '0')).join('')
      assert.ok(ratio(mixed, token('--edo-surface')) >= 4.5, `${theme.selector} ${rule.selector}`)
    }
  }
})
