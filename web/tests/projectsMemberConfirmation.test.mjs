import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import { Button } from 'ant-design-vue'
import ts from 'typescript'
import * as vue from 'vue'

const transpile = source => ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
const loadModule = (source, modules, globals = {}) => {
  const module = { exports: {} }
  new Function('require', 'module', 'exports', ...Object.keys(globals), transpile(source))(name => modules[name] || {}, module, module.exports, ...Object.values(globals))
  return module.exports
}
const { workspaceZh } = loadModule(readFileSync(new URL('../src/locales/workspace.ts', import.meta.url), 'utf8'), {})
const translate = key => workspaceZh[key.replace('workspace.', '')] || key
const detail = (permissions = ['project.member.update', 'application.read']) => ({
  project: { id: 'project-a', name: '交付项目 A', description: '', is_active: true }, permissions,
  members: [{ user_id: 'member-b', username: 'member_b', nickname: '现有成员', role_id: 'reader', role_name: '只读角色' }],
  users: [{ id: 'member-a', username: 'member_a', nickname: '新增成员' }, { id: 'member-b', username: 'member_b', nickname: '现有成员' }],
  roles: [{ id: 'reader', display_name: '只读角色', permissions: ['application.read'] }, { id: 'manager', display_name: '项目管理角色', permissions: ['project.member.update'] }], catalog: [],
})

async function harness(t, { fail = false, permissions } = {}) {
  const requests = [], confirmations = [], buttons = [], errors = []
  const value = detail(permissions), blank = { render: () => null }, icon = () => vue.h('span')
  const table = { setup(props, { slots }) { return () => vue.h('div', (props.rows || []).map(row => vue.h('div', [slots['cell-name']?.({ row }), slots['cell-role_name']?.({ row }), slots.actions?.({ row })]))) }, props: ['rows'] }
  const wrapper = { setup(_props, { slots }) { return () => vue.h('section', [slots.actions?.(), slots.default?.()]) } }
  const client = { put: async (path, body) => { requests.push({ path, body: { ...body } }); if (fail) throw new Error('当前无法更新成员'); return { data: {} } }, get: async () => ({ data: value }) }
  const modules = {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {} }, 'vue-i18n': { useI18n: () => ({ t: translate }) },
    'ant-design-vue': { Modal: { confirm: options => confirmations.push(options) }, message: { error: error => errors.push(error), success() {} } },
    'lucide-vue-next': { Plus: icon, RefreshCw: icon },
    '@/api/client': { __esModule: true, default: client }, '@/api/resources': { apiErrorMessage: error => error.message },
    '@/stores/auth': { useAuthStore: () => ({ user: { id: 'operator', department_id: 'department-a' }, canAny: () => false }) },
    '@/components/PageToolbar.vue': { __esModule: true, default: wrapper }, '@/components/ResourceTable.vue': { __esModule: true, default: table },
    '@/components/ResourceDetailDrawer.vue': { __esModule: true, default: wrapper }, '@/components/PermissionPreview.vue': { __esModule: true, default: blank },
  }
  const descriptor = parse(readFileSync(new URL('../src/views/ProjectsView.vue', import.meta.url), 'utf8')).descriptor
  const script = compileScript(descriptor, { id: 'member-confirmation' })
  const component = loadModule(script.content, modules, { window: { dispatchEvent() {} } }).default
  const template = compileTemplate({ source: descriptor.template.content, filename: 'ProjectsView.vue', id: 'member-confirmation', compilerOptions: { bindingMetadata: script.bindings } })
  component.render = loadModule(template.code, { vue }).render
  const scope = vue.effectScope()
  const state = scope.run(() => component.setup({}, { expose() {} }))
  t.after(() => scope.stop())
  state.rows.value = [value.project]; state.selected.value = value; state.open.value = true
  Object.assign(state.roleDrafts, { 'member-b': 'reader' })
  // 观察锁定 Ant Button 真正渲染到原生按钮的事件，不直接调用 assign 业务方法。
  const nativeButton = node => {
    if (node?.type === 'button') return node
    const children = Array.isArray(node) ? node : Array.isArray(node?.children) ? node.children : node?.children?.default?.() || []
    return children.map(nativeButton).find(Boolean)
  }
  const observedButton = { ...Button, setup(props, context) {
    const render = Button.setup(props, context)
    return () => { const node = render(); buttons.push({ node: nativeButton(node), label: context.slots.default?.().map(item => item.children || '').join('') }); return node }
  } }
  async function render() {
    buttons.length = 0
    const app = vue.createSSRApp({ ...component, setup: () => state })
    app.component('a-button', observedButton)
    for (const name of ['input-search', 'input', 'tag', 'alert', 'select', 'drawer', 'form', 'form-item', 'textarea', 'switch']) app.component(`a-${name}`, blank)
    await renderToString(app)
  }
  async function click(label) {
    await render()
    const button = buttons.find(item => item.label === label)
    assert.ok(button, `缺少真实按钮 ${label}`)
    assert.notEqual(button.node.props.disabled, true)
    const event = new Event('click', { cancelable: true })
    button.node.props.onClick(event)
    await vue.nextTick()
  }
  const contentText = async confirmation => renderToString(vue.createSSRApp({ render: () => vue.h('div', [confirmation.content]) }))
  return { state, requests, confirmations, errors, click, render, buttons, contentText }
}

test('真实添加按钮先确认准确新增动作、项目、用户和目标角色，取消前没有请求', async t => {
  const h = await harness(t)
  Object.assign(h.state.member, { user_id: 'member-a', role_id: 'reader' })
  await h.click('添加成员')
  assert.equal(h.requests.length, 0); assert.equal(h.confirmations.length, 1)
  assert.equal(h.confirmations[0].title, '确认添加成员？')
  const content = await h.contentText(h.confirmations[0])
  for (const expected of ['交付项目 A', '新增成员', 'member_a', '只读角色']) assert.ok(content.includes(expected), expected)
  assert.equal(h.confirmations[0].okText, '添加成员')
})

test('原生成员保存按钮区分修改角色，确认后仍使用当时冻结项目、用户和角色', async t => {
  const h = await harness(t)
  h.state.roleDrafts['member-b'] = 'manager'
  await h.click('保存')
  const confirmation = h.confirmations[0]
  assert.equal(confirmation.title, '确认修改成员角色？')
  const content = await h.contentText(confirmation)
  for (const expected of ['交付项目 A', '现有成员', 'member_b', '项目管理角色']) assert.ok(content.includes(expected), expected)
  h.state.selected.value = { ...detail(), project: { id: 'project-z', name: '其他项目' } }
  h.state.roleDrafts['member-b'] = 'reader'
  await confirmation.onOk()
  assert.deepEqual(h.requests, [{ path: '/projects/project-a/members/member-b', body: { role_id: 'manager' } }])
  assert.equal(h.state.saving.value, false)
})

test('原生移除按钮显示准确当前角色并采用危险确认，失败不吞错或清空当前详情', async t => {
  const h = await harness(t, { fail: true })
  h.state.roleDrafts['member-b'] = 'manager'
  await h.click('移除成员')
  const confirmation = h.confirmations[0]
  assert.equal(confirmation.title, '确认移除成员？'); assert.equal(confirmation.okType, 'danger')
  const content = await h.contentText(confirmation)
  for (const expected of ['交付项目 A', '现有成员', 'member_b', '只读角色']) assert.ok(content.includes(expected), expected)
  assert.ok(!content.includes('项目管理角色'), '移除目标是已保存角色，不是尚未保存的选择')
  assert.equal(h.requests.length, 0)
  await assert.rejects(confirmation.onOk(), /当前无法更新成员/)
  assert.deepEqual(h.requests, [{ path: '/projects/project-a/members/member-b', body: { role_id: '' } }])
  assert.equal(h.state.open.value, true); assert.equal(h.state.selected.value.project.id, 'project-a')
  assert.equal(h.state.saving.value, false); assert.deepEqual(h.errors, ['当前无法更新成员'])
})

test('没有成员管理权限时真实模板不呈现授权按钮，也不放宽项目角色候选', async t => {
  const h = await harness(t, { permissions: ['application.read'] })
  await h.render()
  assert.equal(h.buttons.some(item => ['添加成员', '移除成员', '保存'].includes(item.label)), false)
  assert.deepEqual(h.state.roleOptions.value.map(item => [item.value, item.disabled]), [['reader', false], ['manager', true]])
})

test('成员或角色候选已失效时不生成缺少准确目标的确认，也不发送请求', async t => {
  const h = await harness(t)
  Object.assign(h.state.member, { user_id: 'removed-user', role_id: 'reader' })
  await h.click('添加成员')
  Object.assign(h.state.member, { user_id: 'member-a', role_id: 'removed-role' })
  await h.click('添加成员')
  assert.equal(h.confirmations.length, 0); assert.equal(h.requests.length, 0)
})
