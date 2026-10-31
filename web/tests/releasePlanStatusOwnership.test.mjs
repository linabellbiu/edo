import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')
const transpile = value => ts.transpileModule(value, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
function compile(source, modules) {
  const module = { exports: {} }
  new Function('require', 'module', 'exports', transpile(source))(name => {
    assert.ok(Object.hasOwn(modules, name), `未登记的测试依赖 ${name}`)
    return modules[name]
  }, module, module.exports)
  return module.exports
}
const i18n = { useI18n: () => ({ t: key => key, locale: vue.ref('zh-CN') }) }
const list = compile(read('../src/utils/resourceList.ts'), { vue, 'vue-i18n': i18n })
const pass = tag => ({ inheritAttrs: false, setup: (_, { attrs, slots }) => () => vue.h(tag, attrs, slots.default?.()) })
const detail = { props: ['open', 'title'], setup: (props, { slots }) => () => props.open ? vue.h('section', { 'data-detail': props.title }, [slots.actions?.(), slots.default?.()]) : null }
const actions = { setup: (_, { slots }) => () => vue.h('div', [slots.default?.(), slots.more?.()]) }
const source = read('../src/components/ReleasePlanWorkspace.vue')
const component = compile(compileScript(parse(source).descriptor, { id: 'release-plan-status-owner', inlineTemplate: true }).content, {
  vue, 'vue-i18n': i18n, 'ant-design-vue': { Empty: { PRESENTED_IMAGE_SIMPLE: 'simple' } },
  'lucide-vue-next': Object.fromEntries(['Boxes', 'ChevronRight', 'Clock3', 'GitBranch', 'GitCommit', 'ListChecks', 'Pencil', 'Play', 'Plus', 'Power', 'PowerOff', 'Route', 'TerminalSquare', 'Trash2'].map(name => [name, pass('i')])),
  '@/utils/time': { formatDateTime: value => value || '', formatRelativeTime: value => value || '' },
  '@/utils/resourceList': list,
  '@/components/ResourceDetailDrawer.vue': { __esModule: true, default: detail },
  '@/components/ResourceRowActions.vue': { __esModule: true, default: actions },
  '@/styles/plan-list.css': {},
}).default
const created = '2026-11-01T05:33:15Z', oldTime = '2026-11-01T04:49:11Z', currentTime = '2026-11-01T05:40:00Z', otherTime = '2026-11-01T06:00:00Z'
const member = { id: 'member-current', application_id: 'app-shared', manual_deploy: true }
const plan = extra => ({ id: 'plan-current', description: '新计划', status: 'draft', is_active: true, created_at: created, updated_at: created, groups: [{ id: 'group-current', applications: [member] }], ...extra })
const run = extra => ({ id: 'run-app-latest', application_id: 'app-shared', status: 'succeeded', created_at: oldTime, updated_at: oldTime, ...extra })
const item = extra => ({ id: 'item-current', release_plan_execution_id: 'execution-current', release_group_application_id: member.id, application_id: member.application_id, pipeline_run_id: 'run-plan-current', status: 'failed', ...extra })
const execution = extra => ({ id: 'execution-current', status: 'failed', created_at: currentTime, updated_at: currentTime, items: [item()], ...extra })
function mount(t, value, pipelineRuns, extra = {}) {
  const element = (tag, text = '') => ({ tag, text, props: {}, children: [], parent: null })
  const renderer = vue.createRenderer({
    createElement: tag => element(tag), createText: text => element('#text', text), createComment: () => element('#comment'),
    setText: (node, text) => { node.text = text }, setElementText: (node, text) => { node.text = text; node.children = [] },
    patchProp: (node, key, _old, value) => { node.props[key] = value }, parentNode: node => node.parent,
    nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
    insert(node, parent, anchor) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); node.parent = parent; const i = anchor ? parent.children.indexOf(anchor) : -1; parent.children.splice(i < 0 ? parent.children.length : i, 0, node) },
    remove(node) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); node.parent = null },
  })
  const root = element('root'), props = vue.reactive({ plans: [value], applications: [{ id: member.application_id, name: '共享应用', is_active: true }], pipelineRuns, canUpdate: false, canExecute: true, canReadDeployments: true, executingPlanIds: [], ...extra }), events = []
  const app = renderer.createApp({ setup: () => () => vue.h(component, { ...props, onOpenApplicationRun: (...args) => events.push(['run', ...args]), onOpenApplicationResources: (...args) => events.push(['resources', ...args]) }) })
  app.component('a-table', { props: ['columns', 'dataSource'], setup: (p, { slots }) => () => vue.h('table', p.dataSource.map(record => vue.h('tr', { 'data-plan': record.id }, p.columns.map(column => vue.h('td', { 'data-column': column.key }, slots.bodyCell({ column, record })))))) })
  for (const [name, tag] of [['a-button', 'button'], ['a-menu-item', 'button'], ['a-tooltip', 'span'], ['a-menu-divider', 'hr'], ['a-popconfirm', 'div'], ['a-empty', 'span'], ['a-skeleton', 'div']]) app.component(name, pass(tag))
  app.mount(root); t.after(() => app.unmount())
  const walk = node => [node, ...node.children.flatMap(walk)]
  const text = node => node.tag === '#comment' ? '' : node.text + node.children.map(text).join('')
  const one = (predicate, from = root) => { const result = walk(from).filter(predicate); assert.equal(result.length, 1); return result[0] }
  const row = () => one(node => node.props['data-plan'] === value.id)
  const cell = name => one(node => node.props['data-column'] === name, row())
  const status = () => one(node => String(node.props.class || '').split(' ').includes('plan-status'), row())
  const click = async node => { node.props.onClick(); await vue.nextTick() }
  const open = async () => { await click(one(node => node.tag === 'button' && text(node) === '新计划', row())); return one(node => node.props['data-detail'] === '新计划') }
  const findButton = (label, from) => one(node => node.tag === 'button' && text(node).trim() === label, from)
  return { props, events, text, status, cell, open, click, findButton, walk }
}

for (const relation of [undefined, 'plan-other']) test(`真实列表不把同App的${relation ? '其他计划' : '普通'}成功算成新计划成功或创建前更新`, async t => {
  const h = mount(t, plan(), [run({ release_plan_id: relation })])
  assert.equal(h.text(h.status()), 'releasePlan.status.draft')
  assert.equal(h.status().props.class.includes('live'), false)
  assert.equal(h.cell('updated').children[0].children.find(node => node.tag === 'time').props.datetime, created)
  const drawer = await h.open()
  assert.match(h.text(drawer), /最近执行成功/, '应用自身历史仍是应用级信息')
  await h.click(h.findButton('流水线记录', drawer))
  assert.deepEqual(h.events, [['run', 'plan-current', 'app-shared', 'run-app-latest']])
})

for (const status of ['running', 'failed', 'succeeded']) test(`真实列表仅精确关联当前计划run可回填${status}`, t => {
  const h = mount(t, plan(), [run({ id: 'run-plan-current', release_plan_id: 'plan-current', status, created_at: currentTime, updated_at: currentTime }), run({ id: 'run-other', release_plan_id: 'plan-other', created_at: otherTime, updated_at: otherTime })])
  assert.equal(h.text(h.status()), `releasePlan.status.${status}`)
  assert.equal(h.cell('updated').children[0].children.find(node => node.tag === 'time').props.datetime, currentTime)
})

for (const status of ['pending', 'running', 'failed', 'succeeded']) test(`真实latest_execution ${status}优先且呼吸灯只用于本次忙碌`, t => {
  const h = mount(t, plan({ latest_execution: execution({ status }) }), [run({ id: 'run-plan-old', release_plan_id: 'plan-current', status: 'succeeded', created_at: otherTime, updated_at: otherTime })])
  assert.equal(h.text(h.status()), `releasePlan.status.${status}`)
  assert.equal(h.status().props.class.includes('live'), status === 'pending' || status === 'running')
  assert.equal(h.cell('updated').children[0].children.find(node => node.tag === 'time').props.datetime, currentTime)
})

test('计划更新时间有创建时间下界，忽略关联记录里创建前的旧时间', t => {
  const h = mount(t, plan(), [run({ release_plan_id: 'plan-current' })])
  assert.equal(h.cell('updated').children[0].children.find(node => node.tag === 'time').props.datetime, created)
})

test('本次准确执行项优先于App别次运行，卡片状态时间和两个入口保持同一run', async t => {
  const h = mount(t, plan({ latest_execution: execution() }), [run({ id: 'run-plan-current', release_plan_id: 'plan-current', release_plan_execution_id: 'execution-current', status: 'failed', created_at: currentTime }), run({ id: 'run-unrelated', release_plan_id: 'plan-other', created_at: otherTime })])
  const drawer = await h.open()
  assert.match(h.text(drawer), /最近执行失败/)
  assert.equal(h.walk(drawer).find(node => String(node.props.class || '').includes('plan-app-time')).props.datetime, currentTime)
  await h.click(h.findButton('流水线记录', drawer)); await h.click(h.findButton('部署资源', drawer))
  assert.deepEqual(h.events, [['run', 'plan-current', 'app-shared', 'run-plan-current'], ['resources', 'plan-current', 'app-shared', 'run-plan-current']])
})

test('不把错误成员或别次执行项认成本次，缺准确项时保留App自身历史目标', async t => {
  const h = mount(t, plan({ latest_execution: execution({ items: [item({ release_plan_execution_id: 'other-execution' }), item({ release_group_application_id: 'other-member' })] }) }), [run()])
  const drawer = await h.open(); await h.click(h.findButton('流水线记录', drawer))
  assert.deepEqual(h.events, [['run', 'plan-current', 'app-shared', 'run-app-latest']])
})

test('本次item未出现在有界run列表时不借App别次时间，入口仍绑定本次准确ID', async t => {
  const h = mount(t, plan({ latest_execution: execution() }), [run({ id: 'run-unrelated', created_at: otherTime })])
  const drawer = await h.open()
  assert.match(h.text(drawer), /最近执行失败/)
  assert.equal(h.walk(drawer).filter(node => String(node.props.class || '').includes('plan-app-time')).length, 0)
  await h.click(h.findButton('流水线记录', drawer)); assert.equal(h.events[0][3], 'run-plan-current')
})

test('停用和本地提交中保留既有优先级，未授权不显示执行或部署资源入口', async t => {
  const h = mount(t, plan({ is_active: false }), [run()], { canExecute: false, canReadDeployments: false })
  assert.equal(h.text(h.status()), 'releasePlan.status.disabled')
  h.props.plans[0].is_active = true; h.props.executingPlanIds = ['plan-current']; await vue.nextTick()
  assert.equal(h.text(h.status()), 'releasePlan.status.running'); assert.equal(h.status().props.class.includes('live'), true)
  const drawer = await h.open()
  assert.equal(h.walk(drawer).filter(node => node.tag === 'button' && ['releasePlan.execute', 'releasePlan.executeApplication', '部署资源'].includes(h.text(node))).length, 0)
})

for (const requested of ['run-plan-current', 'run-unrelated']) test(`父正常跳转校验对本次item ${requested === 'run-plan-current' ? '允许' : '拒绝App别次目标'}`, t => {
  const h = componentHarness('../src/views/DevOpsView.vue', { props: { section: 'release-plans' }, codes: ['release_plan.read', 'pipeline_run.read'], globals: { document: { hidden: true } } })
  t.after(h.stop)
  h.state.releasePlans.value = [plan({ latest_execution: execution() })]
  h.state.runs.value = [run({ id: 'run-plan-current', created_at: currentTime }), run({ id: 'run-unrelated', created_at: otherTime })]
  h.state.openReleasePlanApplicationRun('plan-current', 'app-shared', requested)
  assert.deepEqual(h.replacements, requested === 'run-plan-current' ? [{ path: '/release-plans', query: { view: 'runs', run: requested } }] : [])
})

test('父入口没有本次执行时保留App最新跳转，跨计划成员或旧App run不允许', t => {
  const h = componentHarness('../src/views/DevOpsView.vue', { props: { section: 'release-plans' }, codes: ['release_plan.read', 'pipeline_run.read'], globals: { document: { hidden: true } } })
  t.after(h.stop); h.state.releasePlans.value = [plan()]; h.state.runs.value = [run({ id: 'older' }), run({ created_at: currentTime })]
  h.state.openReleasePlanApplicationRun('plan-missing', 'app-shared', 'run-app-latest')
  h.state.openReleasePlanApplicationRun('plan-current', 'app-other', 'run-app-latest')
  h.state.openReleasePlanApplicationRun('plan-current', 'app-shared', 'older')
  assert.deepEqual(h.replacements, [])
  h.state.openReleasePlanApplicationRun('plan-current', 'app-shared', 'run-app-latest')
  assert.equal(h.replacements[0].query.run, 'run-app-latest')
})

test('本次pending项未分配run时不回退App成功，两个入口可见但禁用且不借历史时间', async t => {
  for (const id of ['', undefined, '  ']) {
    const h = mount(t, plan({ latest_execution: execution({ status: 'pending', items: [item({ status: 'pending', pipeline_run_id: id })] }) }), [run()])
    assert.equal(h.text(h.status()), 'releasePlan.status.pending'); assert.equal(h.status().props.class.includes('live'), true)
    const drawer = await h.open()
    assert.match(h.text(drawer), /等待执行/); assert.doesNotMatch(h.text(drawer), /最近执行成功/)
    assert.equal(h.findButton('流水线记录', drawer).props.disabled, true)
    assert.equal(h.findButton('部署资源', drawer).props.disabled, true)
    assert.equal(h.walk(drawer).filter(node => String(node.props.class || '').includes('plan-app-time')).length, 0)
    assert.deepEqual(h.events, [])
  }
})

test('本次failed项缺run仍显示自身失败，不能换成App成功或开放错误入口', async t => {
  const h = mount(t, plan({ latest_execution: execution({ items: [item({ pipeline_run_id: '' })] }) }), [run()])
  const drawer = await h.open()
  assert.match(h.text(drawer), /最近执行失败/); assert.doesNotMatch(h.text(drawer), /最近执行成功/)
  assert.equal(h.findButton('流水线记录', drawer).props.disabled, true)
  assert.equal(h.findButton('部署资源', drawer).props.disabled, true)
})

test('父入口识别本次空run项，空请求与App别次目标均不得回退放行', t => {
  for (const id of ['', undefined, '  ']) {
    const h = componentHarness('../src/views/DevOpsView.vue', { props: { section: 'release-plans' }, codes: ['release_plan.read', 'pipeline_run.read'], globals: { document: { hidden: true } } })
    t.after(h.stop)
    h.state.releasePlans.value = [plan({ latest_execution: execution({ status: 'pending', items: [item({ status: 'pending', pipeline_run_id: id })] }) })]
    h.state.runs.value = [run()]
    h.state.openReleasePlanApplicationRun('plan-current', 'app-shared', '')
    h.state.openReleasePlanApplicationRun('plan-current', 'app-shared', 'run-app-latest')
    assert.deepEqual(h.replacements, [])
  }
})
