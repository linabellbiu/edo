import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const source = readFileSync(new URL('../src/components/BuildPlanWorkspace.vue', import.meta.url), 'utf8')
const script = compileScript(parse(source).descriptor, { id: 'build-edit-handoff', inlineTemplate: true }).content
const output = ts.transpileModule(script, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
const pass = tag => ({ inheritAttrs: false, setup: (_, { attrs, slots }) => () => vue.h(tag, attrs, slots.default?.()) })
const detail = { props: ['open', 'title'], setup: (props, { slots }) => () => props.open
  ? vue.h('section', { 'data-detail': props.title }, [slots.actions?.(), slots.default?.()]) : null }
const actions = { setup: (_, { slots }) => () => vue.h('div', [slots.default?.(), vue.h('menu', slots.more?.())]) }
const modules = {
  vue, 'vue-i18n': { useI18n: () => ({ t: key => key }) },
  'lucide-vue-next': Object.fromEntries(['Download', 'FileCode2', 'Package', 'RefreshCw', 'Upload'].map(name => [name, pass('i')])),
  '@/components/ResourceDetailDrawer.vue': { __esModule: true, default: detail },
  '@/components/ResourceRowActions.vue': { __esModule: true, default: actions },
  '@/components/ResourceTable.vue': { __esModule: true, default: pass('div') },
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
    plans: Array.from({ length: 21 }, (_, i) => ({ id: `p${i + 1}`, name: `Plan ${i + 1}`, kind: 'dockerfile', is_active: true, timeout_seconds: 1800 })),
    applications: [{ id: 'app1', name: 'App', is_active: true }], artifacts: [], registries: [],
    selectedID: 'p1', activeView: 'overview', selectedApplicationID: 'app1',
    canUpdate: true, canReadArtifacts: true, artifactUploading: false,
  })
  const events = [], editor = vue.ref(null), root = element('root')
  let table
  const find = (predicate, node = root) => [...(predicate(node) ? [node] : []), ...node.children.flatMap(child => find(predicate, child))]
  const text = node => node.text + node.children.map(text).join('')
  const app = renderer.createApp({ setup: () => () => vue.h('main', [
    vue.h(component, { ...props,
      onEdit: plan => { events.push(['edit', plan]); editor.value = plan },
      'onSelect-plan': id => { events.push(['select-plan', id]); props.selectedID = id },
      'onSelect-view': view => { events.push(['select-view', view]); props.activeView = view },
      'onUpdate:selectedApplicationID': id => props.selectedApplicationID = id,
    }),
    editor.value ? vue.h('section', { 'data-editor': editor.value.id }) : null,
  ]) })
  app.component('a-table', { props: ['columns', 'dataSource', 'pagination'], setup: (p, { attrs, slots }) => {
    table = { props: p, attrs }
    return () => vue.h('table', p.dataSource.map(record => vue.h('tr', { 'data-plan': record.id },
      p.columns.map(column => vue.h('td', slots.bodyCell({ column, record }))))))
  } })
  for (const [name, tag] of [['a-button', 'button'], ['a-menu-item', 'button'], ['a-tooltip', 'span'], ['a-tag', 'span'], ['a-menu-divider', 'hr'], ['a-upload', 'div'], ['a-select', 'select']]) app.component(name, pass(tag))
  app.mount(root)
  const one = predicate => { const nodes = find(predicate); assert.equal(nodes.length, 1); return nodes[0] }
  const click = async node => { node.props.onClick(); await vue.nextTick() }
  const row = id => one(node => node.props['data-plan'] === id)
  const rowButton = (id, label) => {
    const nodes = find(node => node.tag === 'button' && text(node) === label, row(id))
    assert.equal(nodes.length, 1)
    return nodes[0]
  }
  const toolbarEdit = () => one(node => node.tag === 'button' && text(node) === 'planList.edit' && node.parent?.props['data-detail'])
  return { props, events, editor, find, text, click, rowButton, toolbarEdit,
    detailCount: () => find(node => Boolean(node.props['data-detail'])).length,
    changePage: async () => { table.attrs.onChange({ current: 2, pageSize: 10 }); await vue.nextTick() },
    page: () => table.props.pagination.current, stop: () => app.unmount() }
}

for (const view of ['overview', 'artifacts']) test(`真实${view}详情模板编辑回调先关闭本地抽屉，保留列表及选择上下文`, async t => {
  const h = harness(); t.after(h.stop)
  await h.changePage()
  await h.click(h.rowButton('p11', view === 'overview' ? 'Plan 11' : 'buildPlanWorkspace.artifacts'))
  h.props.selectedApplicationID = 'app1'
  await vue.nextTick()
  assert.equal(h.detailCount(), 1)
  const selected = h.props.selectedID
  await h.click(h.toolbarEdit())
  assert.equal(h.detailCount(), 0, '打开编辑器后原详情/制品抽屉必须关闭')
  assert.equal(h.editor.value.id, 'p11')
  assert.equal(h.props.activeView, 'overview')
  assert.equal(h.props.selectedID, selected)
  assert.equal(h.props.selectedApplicationID, 'app1')
  assert.equal(h.page(), 2)
  assert.deepEqual(h.events.slice(-2).map(([event, value]) => [event, typeof value === 'string' ? value : value.id]), [['select-view', 'overview'], ['edit', 'p11']])
})

test('表格实际编辑菜单也关闭详情，并只发送当前列表中的准确对象', async t => {
  const h = harness(); t.after(h.stop)
  await h.click(h.rowButton('p11', 'Plan 11'))
  const oldClick = h.rowButton('p11', 'planList.edit').props.onClick
  h.props.plans = h.props.plans.map(plan => plan.id === 'p11' ? { ...plan, name: 'Current plan' } : plan)
  await vue.nextTick()
  oldClick()
  await vue.nextTick()
  assert.equal(h.detailCount(), 0)
  assert.equal(h.editor.value, h.props.plans.find(plan => plan.id === 'p11'))
})

for (const entry of ['toolbar', 'menu']) for (const reason of ['uploading', 'revoked', 'removed']) test(`真实${entry}旧回调在${reason}后不得发出编辑`, async t => {
  const h = harness(); t.after(h.stop)
  await h.click(h.rowButton('p11', 'Plan 11'))
  const editButton = () => entry === 'toolbar' ? h.toolbarEdit() : h.rowButton('p11', 'planList.edit')
  const callback = editButton().props.onClick
  if (reason === 'uploading') h.props.artifactUploading = true
  if (reason === 'revoked') h.props.canUpdate = false
  if (reason === 'removed') h.props.plans = h.props.plans.filter(plan => plan.id !== 'p11')
  await vue.nextTick()
  if (reason === 'uploading') assert.equal(editButton().props.disabled, true)
  callback()
  await vue.nextTick()
  assert.equal(h.events.filter(([event]) => event === 'edit').length, 0)
  assert.equal(h.editor.value, null)
})
