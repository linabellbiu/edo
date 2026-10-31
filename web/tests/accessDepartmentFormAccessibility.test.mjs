import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileTemplate, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import { Form, FormItem, Input, Textarea } from 'ant-design-vue'
import ts from 'typescript'
import * as vue from 'vue'
import { componentHarness } from './acceptanceHarness.mjs'

const descriptor = parse(readFileSync(new URL('../src/views/AccessView.vue', import.meta.url), 'utf8')).descriptor
const modals = []
function visit(node) {
  if (node.tag === 'a-modal' && node.props.some(prop => prop.type === 7 && prop.name === 'model' && prop.exp?.content === 'departmentFormOpen')) modals.push(node)
  for (const child of node.children || []) visit(child)
}
visit(descriptor.template.ast)
assert.equal(modals.length, 1, '只编译部门创建和编辑共用的真实 modal 分支')
const template = compileTemplate({ source: modals[0].loc.source, filename: 'AccessView.vue', id: 'access-department-a11y', compilerOptions: { expressionPlugins: ['typescript'] } })
assert.deepEqual(template.errors, [])
const rendered = { exports: {} }
new Function('require', 'exports', ts.transpileModule(template.code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)(name => { assert.equal(name, 'vue'); return vue }, rendered.exports)
const modal = { props: ['open'], inheritAttrs: false, setup: (props, { slots }) => () => props.open ? vue.h('section', slots.default?.()) : null }
function harness(t, api = {}) {
  const auth = vue.reactive({ user: { id: 'operator', department_id: 'department-a', is_superuser: true }, permissions: new Set(['department.read', 'department.create', 'department.update']), authorizationState: 'ready', canAny: () => true })
  const h = componentHarness('../src/views/AccessView.vue', { query: { view: 'departments' }, api, modules: { '@/stores/auth': { useAuthStore: () => auth } } })
  t.after(h.stop)
  h.state.resetDepartment(); h.state.departmentFormOpen.value = true
  return { ...h, auth }
}
async function actualForm(h, warnings = [], textareaProps) {
  const app = vue.createSSRApp({ setup: () => ({ ...h.state }), render: rendered.exports.render })
  app.config.warnHandler = value => warnings.push(value)
  // Ant Textarea 自行限制长度，不输出原生 maxlength；只采样公开输入 props，仍渲染真实组件。
  const textarea = textareaProps ? { inheritAttrs: false, setup: (_props, { attrs }) => () => { textareaProps.push({ maxlength: attrs.maxlength, rows: attrs.rows }); return vue.h(Textarea, attrs) } } : Textarea
  for (const [name, component] of Object.entries({ 'a-modal': modal, 'a-form': Form, 'a-form-item': FormItem, 'a-input': Input, 'a-textarea': textarea })) app.component(name, component)
  return renderToString(app)
}
const attributes = tag => Object.fromEntries([...tag.matchAll(/([\w:-]+)(?:="([^"]*)")?/g)].map(([, key, value]) => [key, value ?? '']))
const controls = html => [...html.matchAll(/<(?:input|textarea)\b[^>]*>/g)].map(([tag]) => attributes(tag))
const draft = { name: '公开测试部门', description: '公开测试说明', parent_id: 'parent-public' }

for (const editing of [false, true]) test(`部门${editing ? '编辑' : '创建'}的真实 Ant 标签各自关联唯一输入，保留已有草稿`, async t => {
  const h = harness(t)
  if (editing) h.state.editDepartment({ id: 'department-public', ...draft })
  else Object.assign(h.state.departmentForm, draft)
  const html = await actualForm(h), fields = controls(html)
  const labels = [...html.matchAll(/<label\b([^>]*)>([\s\S]*?)<\/label>/g)].map(([, attrs, text]) => ({ ...attributes(attrs), text: text.replace(/<[^>]+>/g, '').trim() }))
  assert.deepEqual(labels.map(label => ({ text: label.text, for: label.for })), [{ text: '部门名称', for: 'access-department_name' }, { text: '说明', for: 'access-department_description' }])
  assert.equal(fields.length, 2)
  assert.equal(new Set(fields.map(field => field.id)).size, 2)
  for (const label of labels) assert.equal(fields.filter(field => field.id === label.for).length, 1)
  assert.deepEqual({ ...h.state.departmentForm }, draft)
  assert.equal(h.state.editingDepartmentID.value, editing ? 'department-public' : '')
})

test('部门真实 Ant 输入使用数字 maxlength，名称64和说明255保持不变且不发类型告警', async t => {
  const h = harness(t), warnings = [], textareaProps = [], fields = controls(await actualForm(h, warnings, textareaProps))
  assert.deepEqual(warnings, [])
  assert.equal(fields[0].maxlength, '64')
  assert.deepEqual(textareaProps, [{ maxlength: 255, rows: 3 }])
  assert.equal(fields[1].rows, '3')
})

for (const editing of [false, true]) test(`部门${editing ? '编辑' : '创建'}语义修复保留请求字段及 parent_id，失败保稿、成功关闭`, async t => {
  for (const failed of [true, false]) {
    const method = editing ? 'put' : 'post', calls = []
    const h = harness(t, { [method]: async (path, body) => { calls.push({ path, body: { ...body } }); if (failed) throw new Error('公开测试失败'); return { data: {} } } })
    if (editing) h.state.editDepartment({ id: 'department-public', ...draft })
    else Object.assign(h.state.departmentForm, draft)
    await h.state.saveDepartment()
    assert.deepEqual(calls, [{ path: editing ? '/departments/department-public' : '/departments', body: draft }])
    assert.equal(h.state.departmentFormOpen.value, failed)
    assert.deepEqual({ ...h.state.departmentForm }, failed ? draft : { name: '', description: '', parent_id: '' })
    assert.equal(h.state.editingDepartmentID.value, failed && editing ? 'department-public' : '')
    assert.equal(h.state.saving.value, false)
  }
})

test('空名称及授权状态未知时不提交部门请求，不因标签修复放宽原守卫', async t => {
  const calls = [], h = harness(t, { post: async (path, body) => { calls.push({ path, body }); return { data: {} } } })
  h.state.departmentForm.name = '   '
  await h.state.saveDepartment()
  assert.equal(calls.length, 0)
  assert.ok(h.notifications.some(value => value.kind === 'warning' && value.value === '请输入部门名称'))
  Object.assign(h.state.departmentForm, draft)
  h.auth.authorizationState = 'checking'
  await vue.nextTick()
  await h.state.saveDepartment()
  assert.equal(calls.length, 0)
})
