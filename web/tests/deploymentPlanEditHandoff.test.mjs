import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

const compile = content => ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
function load(content, modules, globals = {}) {
  const module = { exports: {} }
  new Function('require', 'module', 'exports', ...Object.keys(globals), compile(content))(name => {
    assert.ok(Object.hasOwn(modules, name), `未登记的组件测试依赖 ${name}`)
    return modules[name]
  }, module, module.exports, ...Object.values(globals))
  return module.exports
}
const source = readFileSync(new URL('../src/views/DeploymentPlanView.vue', import.meta.url), 'utf8')
const script = compileScript(parse(source).descriptor, { id: 'deployment-edit-handoff', inlineTemplate: true }).content
const i18n = { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) }
const resourceList = load(readFileSync(new URL('../src/utils/resourceList.ts', import.meta.url), 'utf8'), { vue, 'vue-i18n': i18n })
const pass = tag => ({ inheritAttrs: false, setup: (_, { attrs, slots }) => () => vue.h(tag, attrs, slots.default?.()) })

async function harness() {
  const records = vue.ref(Array.from({ length: 21 }, (_, index) => ({ id: `p${index + 1}`, name: `Plan ${index + 1}`, kind: 'docker', timeout_seconds: 600, is_active: true })))
  const permissions = vue.reactive(new Set(['deployment_plan.read', 'deployment_plan.update']))
  const auth = { loaded: true, user: { id: 'user', department_id: 'department', project_id: 'project', permissions: ['deployment_plan.read', 'deployment_plan.update'] }, canAny: codes => codes.some(code => permissions.has(code)) }
  const trace = [], writes = [], errors = []
  const element = tag => ({ tag, props: {}, text: '', children: [], parent: null })
  const renderer = vue.createRenderer({
    patchProp: (node, key, _old, value) => node.props[key] = value,
    createElement: element, createText: text => ({ ...element('#text'), text }), createComment: () => element('#comment'),
    setText: (node, text) => node.text = text, setElementText: (node, text) => { node.text = text; node.children = [] },
    parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
    insert(node, parent, anchor) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); node.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; parent.children.splice(index < 0 ? parent.children.length : index, 0, node) },
    remove(node) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); node.parent = null },
  })
  const detail = { props: ['open', 'title'], setup: (p, { slots }) => () => p.open ? vue.h('section', { 'data-detail': p.title }, [slots.actions?.(), slots.default?.()]) : null }
  const editor = { props: ['open', 'title'], setup: (p, { slots }) => {
    vue.watch(() => p.open, value => { if (value) trace.push('editor-open') }, { flush: 'sync' })
    return () => p.open ? vue.h('aside', { 'data-editor': p.title }, [slots.default?.(), slots.footer?.()]) : null
  } }
  const actions = { setup: (_, { slots }) => () => vue.h('div', [slots.default?.(), slots.more?.()]) }
  const noNetwork = method => async path => { writes.push({ method, path }); throw Error('此交接测试不得执行写请求') }
  const modules = {
    vue, 'vue-i18n': i18n,
    'vue-router': { useRoute: () => vue.reactive({ query: {} }), useRouter: () => ({ replace: () => Promise.resolve(), resolve: () => ({ href: '/resource' }) }) },
    'ant-design-vue': { message: { error: value => errors.push(value), success() {} } },
    'lucide-vue-next': Object.fromEntries(['FileUp', 'Plus', 'RefreshCw', 'Trash2'].map(name => [name, pass('i')])),
    '@/stores/auth': { useAuthStore: () => auth },
    '@/api/client': { __esModule: true, default: { get: async path => { assert.equal(path, '/deployment-plans'); return { data: { deployment_plans: records.value } } }, post: noNetwork('post'), put: noNetwork('put'), delete: noNetwork('delete') } },
    '@/api/resources': { apiErrorMessage: error => error.message },
    '@/api/infrastructure': { environmentIDsOf: host => host.environment_ids || [], listEnvironments: async () => [], listHosts: async () => [] },
    '@/utils/resourceList': { useResourceList: (...args) => { const state = resourceList.useResourceList(...args); vue.watch(state.detailOpen, value => { if (!value) trace.push('detail-close') }, { flush: 'sync' }); return state } },
    '@/utils/time': { formatDateTime: value => value || '—' },
    '@/utils/asyncConfirm': { asyncConfirm: () => assert.fail('交接不能打开写操作确认框') },
    '@/locales/deploymentTarget': { deploymentTargetEn: {}, deploymentTargetZh: {} },
    '@/components/ResourceDetailDrawer.vue': { __esModule: true, default: detail },
    '@/components/ResourceRowActions.vue': { __esModule: true, default: actions },
    '@/components/PageToolbar.vue': { __esModule: true, default: pass('header') },
    '@/components/RuntimeBrandIcon.vue': { __esModule: true, default: pass('i') },
    '@/components/DeliveryDefaultsDrawer.vue': { __esModule: true, default: { setup: () => () => null } },
    '@/styles/plan-list.css': {},
  }
  const component = load(script, modules, { localStorage: { getItem: () => 'project' }, window: { addEventListener() {}, removeEventListener() {} } }).default
  const root = element('root'), app = renderer.createApp(component)
  let table
  app.component('a-table', { props: ['columns', 'dataSource', 'pagination', 'rowClassName'], setup: (p, { attrs, slots }) => {
    table = { props: p, attrs }
    return () => vue.h('table', p.dataSource.map(record => vue.h('tr', { 'data-plan': record.id, class: p.rowClassName(record) }, p.columns.map(column => vue.h('td', slots.bodyCell({ column, record }))))))
  } })
  app.component('a-drawer', editor)
  const tags = new Set()
  function walk(node) { if (node.type === 1 && node.tag.startsWith('a-')) tags.add(node.tag); for (const child of node.children || []) walk(child); for (const branch of node.branches || []) walk(branch) }
  walk(parse(source).descriptor.template.ast)
  for (const tag of tags) if (!['a-table', 'a-drawer'].includes(tag)) app.component(tag, pass(tag === 'a-button' || tag === 'a-menu-item' ? 'button' : tag === 'a-input' ? 'input' : 'div'))
  app.mount(root)
  const flush = async () => { for (let i = 0; i < 4; i++) await vue.nextTick() }
  await flush()
  const find = (predicate, node = root) => [...(predicate(node) ? [node] : []), ...node.children.flatMap(child => find(predicate, child))]
  const text = node => node.text + node.children.map(text).join('')
  const one = predicate => { const nodes = find(predicate); assert.equal(nodes.length, 1); return nodes[0] }
  const click = async node => { node.props.onClick(); await flush() }
  const row = id => one(node => node.props['data-plan'] === id)
  const rowButton = (id, label) => { const nodes = find(node => node.tag === 'button' && text(node) === label, row(id)); assert.equal(nodes.length, 1); return nodes[0] }
  const toolbarEdit = () => one(node => node.tag === 'button' && text(node) === 'planList.edit' && Boolean(node.parent?.props['data-detail']))
  return { records, permissions, trace, writes, errors, find, click, rowButton, toolbarEdit,
    countDetails: () => find(node => Boolean(node.props['data-detail'])).length,
    countEditors: () => find(node => Boolean(node.props['data-editor'])).length,
    name: () => one(node => node.tag === 'input' && node.props.placeholder === '例如：生产后端部署').props.value,
    page: () => table.props.pagination.current,
    selected: () => find(node => node.tag === 'tr' && node.props.class.includes('is-current')).map(node => node.props['data-plan']),
    changePage: async () => { table.attrs.onChange({ current: 2, pageSize: 10 }); await flush() },
    refresh: async () => { await click(one(node => node.tag === 'button' && text(node) === '刷新')) },
    stop: () => app.unmount(),
  }
}

for (const entry of ['toolbar', 'row']) test(`真实${entry}编辑入口先关闭详情再开表单，保留页码/选择`, async t => {
  const h = await harness(); t.after(h.stop)
  await h.changePage()
  await h.click(h.rowButton('p11', 'Plan 11'))
  assert.equal(h.countDetails(), 1)
  const selected = h.selected()
  const button = entry === 'toolbar' ? h.toolbarEdit() : h.rowButton('p11', 'planList.edit')
  h.trace.length = 0
  await h.click(button)
  assert.equal(h.countDetails(), 0, '编辑器打开时详情必须关闭')
  assert.equal(h.countEditors(), 1)
  assert.equal(h.name(), 'Plan 11')
  assert.deepEqual(h.trace, ['detail-close', 'editor-open'])
  assert.equal(h.page(), 2)
  assert.deepEqual(h.selected(), selected)
  assert.deepEqual(h.writes, [])
  assert.deepEqual(h.errors, [])
})

test('真实表格旧回调只使用刷新后相同UUID的当前方案，不填旧对象', async t => {
  const h = await harness(); t.after(h.stop)
  const callback = h.rowButton('p11', 'planList.edit').props.onClick
  h.records.value = h.records.value.map(plan => plan.id === 'p11' ? { ...plan, name: 'Current Plan' } : plan)
  await h.refresh()
  callback()
  await vue.nextTick()
  assert.equal(h.countEditors(), 1)
  assert.equal(h.name(), 'Current Plan')
  assert.deepEqual(h.writes, [])
})

for (const entry of ['toolbar', 'row']) for (const reason of ['revoked', 'removed']) test(`真实${entry}旧回调在${reason}后不改变表单/详情状态`, async t => {
  const h = await harness(); t.after(h.stop)
  await h.click(h.rowButton('p11', 'Plan 11'))
  const callback = (entry === 'toolbar' ? h.toolbarEdit() : h.rowButton('p11', 'planList.edit')).props.onClick
  if (reason === 'revoked') h.permissions.delete('deployment_plan.update')
  else { h.records.value = h.records.value.filter(plan => plan.id !== 'p11'); await h.refresh() }
  await vue.nextTick()
  const before = h.countDetails()
  h.trace.length = 0
  callback()
  await vue.nextTick()
  assert.equal(h.countEditors(), 0)
  assert.equal(h.countDetails(), before)
  assert.deepEqual(h.trace, [])
  assert.deepEqual(h.writes, [])
})
