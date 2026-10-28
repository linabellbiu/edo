import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { parse, compileScript } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import * as vue from 'vue'
import ts from 'typescript'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const read = file => readFileSync(new URL(file, import.meta.url), 'utf8')
function compile(descriptor, modules = {}) {
  const script = compileScript(descriptor, { id: 'registry-credential-render', inlineTemplate: true }).content
  const output = ts.transpileModule(script, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output)(name => {
    if (name === 'vue') return vue
    if (!Object.hasOwn(modules, name)) throw new Error(`未登记的渲染依赖 ${name}`)
    return modules[name]
  }, module, module.exports)
  return module.exports.default
}
const emptyDrawer = { setup: () => () => null }
const ResourceTable = compile(parse(read('../src/components/ResourceTable.vue')).descriptor, {
  'vue-i18n': { useI18n: () => ({ t: key => key }) },
  '@/components/ResourceDetailDrawer.vue': { __esModule: true, default: emptyDrawer },
})
const view = parse(read('../src/views/DevOpsView.vue')).descriptor
const tables = []
function visit(node) {
  if (node.type === 1 && node.tag === 'ResourceTable') tables.push(node)
  for (const child of node.children || []) visit(child)
}
visit(view.template.ast)
const table = tables.find(node => node.props.some(prop => prop.type === 7 && prop.name === 'bind' && prop.arg?.content === 'columns' && prop.exp?.content === 'activeColumns'))
assert.ok(table, '需要渲染 DevOpsView 的真实通用资源表格及 scoped slots')
const Host = compile(parse(`<script setup lang="ts">
import { computed } from 'vue'
import ResourceTable from './ResourceTable.vue'
const props = defineProps<{section: string; rows: Record<string, unknown>[]; columns: {key: string; label: string}[]}>()
const activeRows = computed(() => props.rows), activeColumns = computed(() => props.columns)
const loading = false, activeResourceID = '', canUpdate = false, canExecute = false, repositoryTestingID = ''
const auth = { canAny: () => false }
const registryProviderLabel = (value: string) => value
const registryEndpointLabel = (row: Record<string, unknown>) => row.endpoint
const edit = () => {}, testStoredRepository = () => {}, showWebhook = () => {}
</script><template>${table.loc.source}</template>`).descriptor, {
  './ResourceTable.vue': { __esModule: true, default: ResourceTable },
})
async function render(component, props) {
  const app = vue.createSSRApp(component, props)
  for (const tag of ['a-spin', 'a-tag', 'a-tooltip', 'a-button', 'a-pagination', 'a-empty']) {
    app.component(tag, { inheritAttrs: false, props: ['color'], setup: (props, { slots }) => () => vue.h('span', tag === 'a-tag' ? { 'data-tag-color': props.color } : {}, slots.default?.()) })
  }
  return renderToString(app)
}
function registryColumns(t) {
  const h = componentHarness('../src/views/DevOpsView.vue', { props: { section: 'image-registries' }, codes: ['image_registry.read'], modules: { '@/utils/deploymentInstances': loadFrontend('../src/utils/deploymentInstances.ts') }, globals: { document: { hidden: true } } })
  t.after(h.stop)
  assert.ok(h.state.activeColumns.value.some(column => column.key === 'has_credential'))
  return h.state.activeColumns.value
}

test('正常启用的匿名镜像仓库显示匿名，不把无凭据误报成停用', async t => {
  const html = await render(Host, { section: 'image-registries', columns: registryColumns(t), rows: [{ id: 'anonymous', name: '本地匿名 OCI', provider: 'generic', endpoint: 'http://registry.test:5000', namespace: '', has_credential: false, is_active: true }] })
  assert.match(html, />匿名</)
  assert.doesNotMatch(html, /启用|停用/)
})

test('已保存凭据显示已配置，与镜像仓库启停状态分别表达', async t => {
  const html = await render(Host, { section: 'image-registries', columns: registryColumns(t), rows: [{ id: 'configured', name: '私有仓库', provider: 'generic', endpoint: 'https://registry.test', namespace: 'team', has_credential: true, is_active: false }] })
  assert.match(html, />已配置</)
  assert.doesNotMatch(html, /启用|停用/)
})

test('缺失凭据状态保持空值，不伪造匿名或已配置', async () => {
  const html = await render(Host, { section: 'image-registries', columns: [{ key: 'has_credential', label: '凭据' }], rows: [{ id: 'unknown' }] })
  assert.match(html, />—</)
  assert.doesNotMatch(html, /匿名|已配置|启用|停用/)
})

test('其他资源无凭据只显示未配置，不冒充支持匿名访问', async () => {
  const html = await render(Host, { section: 'repositories', columns: [{ key: 'has_credential', label: '凭据' }], rows: [{ id: 'repository', has_credential: false }] })
  assert.match(html, />未配置</)
  assert.doesNotMatch(html, /匿名|停用/)
})

test('共享表格的启停布尔列保持原有显示，不因凭据修复改变其他资源', async () => {
  const html = await render(ResourceTable, { columns: [{ key: 'is_active', label: '状态' }], rows: [{ id: 'enabled', is_active: true }, { id: 'disabled', is_active: false }], details: false, pagination: false })
  assert.match(html, />启用</)
  assert.match(html, />停用</)
})
