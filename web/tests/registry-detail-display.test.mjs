import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {compileScript, parse} from '@vue/compiler-sfc'
import * as vue from 'vue'
import ts from 'typescript'
import {componentHarness, loadFrontend} from './acceptanceHarness.mjs'

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')
function compile(descriptor, modules = {}) {
  const script = compileScript(descriptor, {id: 'registry-detail-render', inlineTemplate: true}).content
  const code = ts.transpileModule(script, {compilerOptions: {module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true}}).outputText
  const module = {exports: {}}
  new Function('require', 'module', 'exports', code)(name => {
    if (name === 'vue') return vue
    if (!Object.hasOwn(modules, name)) throw new Error(`未登记的详情渲染依赖 ${name}`)
    return modules[name]
  }, module, module.exports)
  return module.exports.default
}

const ResourceDetailDrawer = compile(parse(read('../src/components/ResourceDetailDrawer.vue')).descriptor)
const ResourceTable = compile(parse(read('../src/components/ResourceTable.vue')).descriptor, {
  'vue-i18n': {useI18n: () => ({t: key => key})},
  '@/components/ResourceDetailDrawer.vue': {__esModule: true, default: ResourceDetailDrawer},
})
const tables = []
function visit(node) {
  if (node.type === 1 && node.tag === 'ResourceTable') tables.push(node)
  for (const child of node.children || []) visit(child)
}
visit(parse(read('../src/views/DevOpsView.vue')).descriptor.template.ast)
const table = tables.find(node => node.props.some(prop => prop.type === 7 && prop.name === 'bind' && prop.arg?.content === 'columns' && prop.exp?.content === 'activeColumns'))
assert.ok(table, '必须使用 DevOpsView 真实资源表格及详情 slot')
const Host = compile(parse(`<script setup lang="ts">
import {computed} from 'vue'
import ResourceTable from './ResourceTable.vue'
const props = defineProps<{section: string; rows: Record<string, unknown>[]; columns: {key: string; label: string}[]; providerLabel: (value: string) => string}>()
const activeRows = computed(() => props.rows), activeColumns = computed(() => props.columns)
const loading = false, activeResourceID = '', canUpdate = false, canExecute = false, repositoryTestingID = ''
const auth = {canAny: () => false}
const registryProviderLabel = props.providerLabel
const registryEndpointLabel = (row: Record<string, unknown>) => row.endpoint
const edit = () => {}, testStoredRepository = () => {}, showWebhook = () => {}
</script><template>${table.loc.source}</template>`).descriptor, {
  './ResourceTable.vue': {__esModule: true, default: ResourceTable},
})

function node(tag, text = '') { return {tag, text, props: {}, children: [], parent: null} }
const renderer = vue.createRenderer({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: () => node('#comment'),
  setText: (target, text) => { target.text = text },
  setElementText: (target, text) => { target.text = text; target.children = [] },
  patchProp: (target, key, _previous, value) => { target.props[key] = value },
  parentNode: target => target.parent,
  nextSibling: target => target.parent?.children[target.parent.children.indexOf(target) + 1] ?? null,
  remove(target) {
    if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1)
    target.parent = null
  },
  insert(target, parent, anchor = null) {
    if (target.parent) target.parent.children.splice(target.parent.children.indexOf(target), 1)
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, target)
    target.parent = parent
  },
})
const walk = root => [root, ...root.children.flatMap(walk)]
const text = root => root.tag === '#comment' ? '' : root.text + root.children.map(text).join('')

function mount(t, section, row) {
  const h = componentHarness('../src/views/DevOpsView.vue', {
    props: {section}, codes: ['image_registry.read', 'repository.read'],
    modules: {'@/utils/deploymentInstances': loadFrontend('../src/utils/deploymentInstances.ts')},
    globals: {document: {hidden: true}},
  })
  t.after(h.stop)
  const root = node('root')
  const app = renderer.createApp(Host, {section, rows: [row], columns: h.state.activeColumns.value, providerLabel: h.state.registryProviderLabel})
  for (const name of ['a-spin', 'a-tag', 'a-tooltip', 'a-button', 'a-pagination', 'a-empty']) {
    app.component(name, {inheritAttrs: false, setup: (_props, {attrs, slots}) => () => vue.h(name === 'a-button' ? 'button' : 'span', attrs, slots.default?.())})
  }
  app.component('a-drawer', {props: ['open', 'title'], setup: (props, {slots}) => () => props.open ? vue.h('aside', [props.title, slots.default?.()]) : null})
  app.mount(root)
  t.after(() => app.unmount())
  return {
    listText: () => text(walk(root).find(item => item.tag === 'table')),
    async open() {
      assert.equal(walk(root).some(item => item.tag === 'aside'), false, '点击之前详情必须关闭')
      const button = walk(root).find(item => item.tag === 'button' && text(item) === row.name)
      assert.ok(button?.props.onClick, '必须从真实表格名称按钮打开详情')
      button.props.onClick()
      await vue.nextTick()
      const drawer = walk(root).find(item => item.tag === 'aside')
      assert.ok(drawer, '真实 ResourceDetailDrawer 必须打开')
      return Object.fromEntries(walk(drawer).filter(item => item.tag === 'dt').map(term => [text(term), text(term.parent.children.find(item => item.tag === 'dd'))]))
    },
  }
}

test('真实镜像仓库详情点击打开后显示匿名和类型名称，不泄漏布尔或内部类型', async t => {
  const page = mount(t, 'image-registries', {id: 'anonymous', name: '匿名镜像仓库', provider: 'generic', has_credential: false, endpoint: 'https://registry.test', namespace: 'team'})
  assert.match(page.listText(), /匿名/)
  const facts = await page.open()
  assert.equal(facts['类型'], '通用 Registry')
  assert.equal(facts['凭据'], '匿名')
  assert.doesNotMatch(Object.values(facts).join(' '), /\b(?:generic|false)\b/)
})

test('镜像仓库详情已配置凭据与匿名分别表达，复用 Docker Hub 和 Harbor 类型文案', async t => {
  for (const [provider, expected] of [['docker_hub', 'Docker Hub'], ['harbor', 'Harbor']]) {
    const page = mount(t, 'image-registries', {id: provider, name: expected, provider, has_credential: true})
    assert.match(page.listText(), /已配置/)
    const facts = await page.open()
    assert.equal(facts['类型'], expected)
    assert.equal(facts['凭据'], '已配置')
    assert.doesNotMatch(Object.values(facts).join(' '), /\btrue\b|启用|停用/)
  }
})

test('详情缺失或空类型、凭据状态显示空值，不伪造匿名和已配置', async t => {
  for (const value of [undefined, null, '']) {
    const facts = await mount(t, 'image-registries', {id: 'missing', name: '未知仓库', provider: value, has_credential: value}).open()
    assert.equal(facts['类型'], '—')
    assert.equal(facts['凭据'], '—')
  }
})

test('仓库详情格式仅限定镜像仓库，其他资源平台和启停布尔列维持原样', async t => {
  const page = mount(t, 'repositories', {id: 'repository', name: '代码仓库', provider: 'gitee', clone_url: 'https://git.test/team/demo', default_branch: 'main', webhook_enabled: false, is_active: true})
  assert.match(page.listText(), /启用/)
  assert.match(page.listText(), /停用/)
  const facts = await page.open()
  assert.equal(facts['平台'], 'gitee')
  assert.equal(facts['Webhook'], 'false')
  assert.equal(facts['状态'], 'true')
  assert.doesNotMatch(Object.values(facts).join(' '), /匿名|已配置/)
})
