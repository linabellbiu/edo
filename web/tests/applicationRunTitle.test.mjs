import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'
import * as i18n from 'vue-i18n'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')
const transpile = source => ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
const localeCache = new Map()
function loadLocale(filename) {
  if (localeCache.has(filename)) return localeCache.get(filename)
  const module = { exports: {} }
  localeCache.set(filename, module.exports)
  new Function('require', 'module', 'exports', transpile(readFileSync(filename, 'utf8')))(name => {
    if (name === 'vue-i18n') return i18n
    assert.ok(name.startsWith('./'), `未登记的翻译依赖 ${name}`)
    return loadLocale(resolve(dirname(filename), name + '.ts'))
  }, module, module.exports)
  localeCache.set(filename, module.exports)
  return module.exports
}
const { messages } = loadLocale(fileURLToPath(new URL('../src/locales/index.ts', import.meta.url)))
const source = read('../src/views/DevOpsView.vue')
const tables = []
function visit(node) {
  if (node.type === 1 && node.tag === 'a-table' && node.props.some(prop => prop.type === 6 && prop.name === 'class' && prop.value?.content.split(' ').includes('apps-table'))) tables.push(node)
  for (const child of node.children || []) visit(child)
}
visit(parse(source).descriptor.template.ast)
assert.equal(tables.length, 1, '必须渲染真实应用表格 bodyCell 和标题回调')
const hostSource = `<script setup lang="ts">
import {computed} from 'vue'
import {ArrowUpRight,Workflow} from 'lucide-vue-next'
const props=defineProps<{state:any}>()
const visibleApplicationCards=computed(()=>props.state.applicationCards.value)
const applicationColumns=[{key:'run'}], applicationPagination={current:1,pageSize:10}
const applicationTableRow=(card:any)=>({id:'application-'+card.application.id})
const applicationTableRowClass=()=>'', changeApplicationPage=()=>{}
const t=props.state.t,openApplicationLatestRun=props.state.openApplicationLatestRun
const applicationLatestWorkflowName=props.state.applicationLatestWorkflowName
const formatApplicationTime=props.state.formatApplicationTime,runReferenceLabel=props.state.runReferenceLabel
</script><template>${tables[0].loc.source}</template>`
const compiled = transpile(compileScript(parse(hostSource).descriptor, { id: 'application-run-title', inlineTemplate: true }).content)
const pass = tag => ({ inheritAttrs: false, setup: (_, { attrs, slots }) => () => vue.h(tag, attrs, slots.default?.()) })
const module = { exports: {} }
new Function('require', 'module', 'exports', compiled)(name => {
  if (name === 'vue') return vue
  assert.equal(name, 'lucide-vue-next')
  return { ArrowUpRight: pass('i'), Workflow: pass('i') }
}, module, module.exports)
const Host = module.exports.default
const element = (tag, text = '') => ({ tag, text, props: {}, children: [], parent: null })
const renderer = vue.createRenderer({
  createElement: tag => element(tag), createText: text => element('#text', text), createComment: () => element('#comment'),
  setText: (node, text) => { node.text = text }, setElementText: (node, text) => { node.text = text; node.children = [] },
  patchProp: (node, key, _old, value) => { node.props[key] = value }, parentNode: node => node.parent,
  nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
  insert(node, parent, anchor) {
    if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1)
    node.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, node)
  },
  remove(node) { if (node.parent) node.parent.children.splice(node.parent.children.indexOf(node), 1); node.parent = null },
})
const walk = node => [node, ...node.children.flatMap(walk)]
const text = node => node.tag === '#comment' ? '' : node.text + node.children.map(text).join('')
function mount(t, locale, run) {
  const translations = i18n.createI18n({ legacy: false, locale, fallbackLocale: 'zh-CN', messages })
  const h = componentHarness('../src/views/DevOpsView.vue', {
    props: { section: 'applications' }, codes: ['application.read', 'pipeline_run.read'],
    modules: {
      'vue-i18n': { useI18n: () => translations.global },
      '@/utils/deploymentInstances': loadFrontend('../src/utils/deploymentInstances.ts'),
      '@/utils/gitReference': loadFrontend('../src/utils/gitReference.ts'),
    }, globals: { document: { hidden: true } },
  })
  t.after(h.stop)
  h.state.applications.value = [{ id: 'app-current', name: '应用', workflows: [{ id: 'flow-current', name: '准确流水线', source: { type: 'trigger', config: { branch: 'main', events: ['manual'] } }, stages: [], is_active: true }], is_active: true, sync_status: 'synced' }]
  h.state.runs.value = run ? [{ id: 'older', application_id: 'app-current', created_at: '2026-10-01T00:00:00Z', status: 'failed', commit_message: '旧运行不能被选中' }, run] : []
  const root = element('root'), app = renderer.createApp(Host, { state: h.state })
  app.component('a-table', { props: ['columns', 'dataSource'], setup: (p, { slots }) => () => vue.h('table', p.dataSource.map(record => vue.h('tr', { 'data-application': record.application.id }, p.columns.map(column => vue.h('td', slots.bodyCell({ column, record })))))) })
  for (const name of ['a-tooltip', 'a-button', 'a-menu-item', 'Boxes', 'GitBranch', 'Power', 'ResourceRowActions']) app.component(name, pass(name === 'a-button' || name === 'a-menu-item' ? 'button' : 'span'))
  app.mount(root); t.after(() => app.unmount())
  const buttons = walk(root).filter(node => node.tag === 'button' && node.props.class === 'apps-run-title')
  assert.equal(buttons.length, 1)
  return { button: buttons[0], root, navigation: h.replacements, title: () => text(buttons[0]), cell: () => text(root) }
}
const run = extra => ({ id: 'run-exact', application_id: 'app-current', workflow_id: 'flow-current', created_at: '2026-11-01T00:00:00Z', status: 'succeeded', trigger: 'manual', ref: 'refs/heads/main', commit_sha: '1234567890abcdef', ...extra })

for (const [locale, empty, missing] of [['zh-CN', '还没有流水线运行', '未记录提交说明'], ['en-US', 'No pipeline runs yet', 'No commit message recorded']]) {
  test(`${locale} 真实模板仅在无运行时显示空态并禁用`, t => {
    const h = mount(t, locale)
    assert.equal(h.title(), empty); assert.equal(h.button.props.disabled, true); assert.deepEqual(h.navigation, [])
  })
  for (const [name, value] of [['缺失', {}], ['空', { commit_message: '' }], ['空白', { commit_message: ' \t\n ' }]]) test(`${locale} 有运行但${name}说明仍渲染准确可点击标题`, t => {
    const h = mount(t, locale, run(value))
    assert.equal(h.title(), missing); assert.equal(h.button.props.disabled, false)
    h.button.props.onClick()
    assert.deepEqual(h.navigation, [{ path: '/release-plans', query: { view: 'runs', run: 'run-exact' } }])
    assert.match(h.cell(), /准确流水线/)
  })
  test(`${locale} 正常说明保留文本并跳到准确最新运行`, t => {
    const h = mount(t, locale, run({ commit_message: '修复功能' }))
    assert.equal(h.title(), '修复功能'); assert.equal(h.button.props.disabled, false)
    h.button.props.onClick(); assert.equal(h.navigation[0].query.run, 'run-exact')
  })
  test(`${locale} 制品运行无说明不冒充无运行且不混淆来源ID`, t => {
    const h = mount(t, locale, run({ artifact_id: 'artifact-selected', selected_artifact: { id: 'artifact-selected', name: 'public:artifact', kind: 'oci_image', digest: 'sha256:public' } }))
    assert.equal(h.title(), missing); assert.equal(h.button.props.disabled, false)
    assert.match(h.cell(), /public:artifact/)
    h.button.props.onClick(); assert.equal(h.navigation[0].query.run, 'run-exact')
  })
}
