import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const source = readFileSync(new URL('../src/components/ReleasePlanWorkspace.vue', import.meta.url), 'utf8')
const resourceListSource = readFileSync(new URL('../src/utils/resourceList.ts', import.meta.url), 'utf8')
const transpile = value => ts.transpileModule(value, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
const script = compileScript(parse(source).descriptor, { id: 'release-edit-handoff', inlineTemplate: true }).content
const output = transpile(script)
const pass = tag => ({ inheritAttrs: false, setup: (_, { attrs, slots }) => () => vue.h(tag, attrs, slots.default?.()) })
const detail = { props: ['open', 'title'], setup: (props, { slots }) => () => props.open
  ? vue.h('section', { 'data-detail': props.title }, [slots.actions?.(), slots.default?.()]) : null }
const actions = { setup: (_, { slots }) => () => vue.h('div', [slots.default?.(), vue.h('menu', slots.more?.())]) }
const i18n = { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) }
const resourceModule = { exports: {} }
new Function('require', 'module', 'exports', transpile(resourceListSource))(name => {
  assert.ok(['vue', 'vue-i18n'].includes(name), `未登记的列表状态依赖 ${name}`)
  return name === 'vue' ? vue : i18n
}, resourceModule, resourceModule.exports)
let lastListState
const modules = {
  vue, 'vue-i18n': i18n, 'ant-design-vue': { Empty: { PRESENTED_IMAGE_SIMPLE: 'simple' } },
  'lucide-vue-next': Object.fromEntries(['Boxes', 'ChevronRight', 'Clock3', 'GitBranch', 'GitCommit', 'ListChecks', 'Pencil', 'Play', 'Plus', 'Power', 'PowerOff', 'Route', 'TerminalSquare', 'Trash2'].map(name => [name, pass('i')])),
  '@/utils/time': { formatDateTime: value => value || '', formatRelativeTime: value => value || '' },
  '@/utils/resourceList': { useResourceList: (...args) => { lastListState = resourceModule.exports.useResourceList(...args); return lastListState } },
  '@/components/ResourceDetailDrawer.vue': { __esModule: true, default: detail },
  '@/components/ResourceRowActions.vue': { __esModule: true, default: actions },
  '@/styles/plan-list.css': {},
}
const module = { exports: {} }
new Function('require', 'module', 'exports', output)(name => {
  assert.ok(Object.hasOwn(modules, name), `未登记的组件测试依赖 ${name}`)
  return modules[name]
}, module, module.exports)
const component = module.exports.default

function harness() {
  const element = tag => ({ tag, props: {}, children: [], parent: null, text: '' })
  const renderer = vue.createRenderer({
    patchProp: (node, key, _old, value) => node.props[key] = value,
    createElement: element, createText: text => ({ ...element('#text'), text }), createComment: () => element('#comment'),
    setText: (node, text) => node.text = text, setElementText: (node, text) => { node.text = text; node.children = [] },
    parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
    insert(node, parent, anchor) {
      if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
      node.parent = parent
      const index = anchor ? parent.children.indexOf(anchor) : -1
      parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
    },
    remove(node) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); node.parent = null },
  })
  const props = vue.reactive({
    plans: Array.from({ length: 21 }, (_, i) => ({ id: `p${i + 1}`, description: `Plan ${i + 1}`, status: 'draft', is_active: true, created_at: '2026-11-01T00:00:00Z', groups: [{ id: `g${i + 1}`, name: '应用列表', mode: 'parallel', failure_policy: 'stop', applications: [{ id: `m${i + 1}`, application_id: 'app1', manual_deploy: false }] }] })),
    applications: [{ id: 'app1', name: 'App', is_active: true }], pipelineRuns: [], activePlanID: 'p1',
    canUpdate: true, canCreate: false, canExecute: false, canDelete: false, executingPlanIds: [], mutatingPlanID: '',
  })
  const events = [], editor = vue.ref(null), rootNode = element('root')
  let table, listState
  const find = (predicate, node = rootNode) => [...(predicate(node) ? [node] : []), ...node.children.flatMap(child => find(predicate, child))]
  const text = node => node.text + node.children.map(text).join('')
  const app = renderer.createApp({ setup: () => () => vue.h('main', [
    vue.h(component, { ...props, onEdit: id => { events.push({ id, detailOpenOnEmit: listState.detailOpen.value }); editor.value = id } }),
    editor.value ? vue.h('section', { 'data-editor': editor.value }) : null,
  ]) })
  app.component('a-table', { props: ['columns', 'dataSource', 'pagination'], setup: (p, { attrs, slots }) => {
    table = { props: p, attrs }
    return () => vue.h('table', p.dataSource.map(record => vue.h('tr', { 'data-plan': record.id }, p.columns.map(column => vue.h('td', slots.bodyCell({ column, record }))))))
  } })
  for (const [name, tag] of [['a-button', 'button'], ['a-menu-item', 'button'], ['a-tooltip', 'span'], ['a-menu-divider', 'hr'], ['a-popconfirm', 'div'], ['a-empty', 'span'], ['a-skeleton', 'div']]) app.component(name, pass(tag))
  app.mount(rootNode)
  listState = lastListState
  const one = predicate => { const nodes = find(predicate); assert.equal(nodes.length, 1); return nodes[0] }
  const click = async node => { node.props.onClick(); await vue.nextTick() }
  const row = id => one(node => node.props['data-plan'] === id)
  const rowButton = (id, label) => { const nodes = find(node => node.tag === 'button' && text(node) === label, row(id)); assert.equal(nodes.length, 1); return nodes[0] }
  const toolbarEdit = () => one(node => node.tag === 'button' && text(node) === 'releasePlan.actions.edit' && node.parent?.props['data-detail'])
  return { props, events, editor, click, rowButton, toolbarEdit,
    details: () => find(node => Boolean(node.props['data-detail'])).length,
    editors: () => find(node => Boolean(node.props['data-editor'])).length,
    selected: () => listState.detailID.value,
    page: () => table.props.pagination.current,
    changePage: async () => { table.attrs.onChange({ current: 2, pageSize: 10 }); await vue.nextTick() },
    stop: () => app.unmount() }
}

test('真实详情编辑先关闭本地详情，唯一编辑器保留page与selected上下文', async t => {
  const h = harness(); t.after(h.stop)
  await h.changePage(); await h.click(h.rowButton('p11', 'Plan 11'))
  assert.equal(h.details(), 1)
  const selected = h.selected(), active = h.props.activePlanID
  await h.click(h.toolbarEdit())
  assert.equal(h.details(), 0, '编辑器打开后旧详情必须关闭')
  assert.equal(h.editors(), 1); assert.equal(h.editor.value, 'p11')
  assert.equal(h.page(), 2); assert.equal(h.selected(), selected); assert.equal(h.props.activePlanID, active)
  assert.deepEqual(h.events, [{ id: 'p11', detailOpenOnEmit: false }])
})

test('真实行编辑菜单共用交接，旧回调只发送准确UUID并保留当前对象', async t => {
  const h = harness(); t.after(h.stop)
  await h.changePage(); await h.click(h.rowButton('p11', 'Plan 11'))
  const callback = h.rowButton('p11', 'releasePlan.actions.edit').props.onClick
  h.props.plans = h.props.plans.map(plan => plan.id === 'p11' ? { ...plan, description: 'Current plan' } : plan)
  await vue.nextTick(); callback(); await vue.nextTick()
  assert.equal(h.details(), 0); assert.equal(h.editors(), 1); assert.equal(h.editor.value, 'p11')
  assert.equal(h.props.plans.find(plan => plan.id === 'p11').description, 'Current plan')
  assert.equal(h.page(), 2); assert.equal(h.selected(), 'p11')
  assert.deepEqual(h.events, [{ id: 'p11', detailOpenOnEmit: false }])
})

for (const entry of ['toolbar', 'menu']) for (const reason of ['revoked', 'removed', 'pending', 'running', 'executing', 'mutating']) test(`真实${entry}旧编辑回调在${reason}后不得emit`, async t => {
  const h = harness(); t.after(h.stop)
  await h.click(h.rowButton('p11', 'Plan 11'))
  const button = () => entry === 'toolbar' ? h.toolbarEdit() : h.rowButton('p11', 'releasePlan.actions.edit')
  const callback = button().props.onClick
  if (reason === 'revoked') h.props.canUpdate = false
  if (reason === 'removed') h.props.plans = h.props.plans.filter(plan => plan.id !== 'p11')
  if (reason === 'pending' || reason === 'running') h.props.plans = h.props.plans.map(plan => plan.id === 'p11' ? { ...plan, latest_execution: { id: 'execution11', status: reason, created_at: '2026-11-01T00:00:01Z' } } : plan)
  if (reason === 'executing') h.props.executingPlanIds = ['p11']
  if (reason === 'mutating') h.props.mutatingPlanID = 'p11'
  await vue.nextTick()
  if (reason === 'mutating') assert.equal(button().props.disabled, true)
  callback(); await vue.nextTick()
  assert.equal(h.events.length, 0); assert.equal(h.editor.value, null); assert.equal(h.editors(), 0)
})
