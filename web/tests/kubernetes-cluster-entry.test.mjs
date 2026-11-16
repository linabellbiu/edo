import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { baseParse } from '@vue/compiler-dom'
import ts from 'typescript'
import * as vue from 'vue'
import { loadFrontend } from './acceptanceHarness.mjs'

const source = readFileSync(new URL('../src/views/KubernetesView.vue', import.meta.url), 'utf8')
const template = parse(source).descriptor.template.content
const entries = []
function inspect(node, parents = []) {
  if (node.type === 1 && node.tag === 'a-button' && node.props.some(prop => prop.type === 7 && prop.name === 'on' && prop.arg?.content === 'click' && prop.exp?.content === 'clusterDrawer=true')) entries.push({node, parents})
  for (const child of node.children ?? []) inspect(child, [...parents, node])
}
inspect(baseParse(template))

test('接入集群入口位于总览内容区，不在公共工具栏中', () => {
  const toolbar = template.match(/<PageToolbar\b[\s\S]*?<\/PageToolbar>/)?.[0]
  if(toolbar)assert.doesNotMatch(toolbar, /k8s\.addCluster|clusterDrawer/)
  const panel = template.match(/<section[^>]*class="resource-panel">[\s\S]*?<\/section>/)?.[0]
  assert.ok(panel)
  assert.match(panel, /@click="clusterDrawer=true"/)
  assert.equal(entries.length, 1)
})

test('只有总览且具备创建权限时才显示接入集群入口', () => {
  const entry = entries[0]
  assert.ok(entry)
  const condition = [...entry.parents, entry.node].flatMap(node => (node.props ?? []).filter(prop => prop.type === 7 && prop.name === 'if').map(prop => `(${prop.exp?.content})`)).join(' && ')
  assert.ok(condition)
  const visible = new Function('group', 'can', `return ${condition}`)
  for (const group of ['overview', 'nodes', 'workloads', 'config', 'storage', 'network']) {
    for (const allowed of [true, false]) {
      assert.equal(visible(group, permission => permission === 'cluster.create' && allowed), group === 'overview' && allowed)
    }
  }
})

function compile(source, modules = {}) {
  const script = compileScript(parse(source).descriptor, { id: 'cluster-entry-render', inlineTemplate: true }).content
  const output = ts.transpileModule(script, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }, empty = { inheritAttrs: false, setup: () => () => null }
  new Function('require', 'module', 'exports', output)(name => name === 'vue' ? vue : modules[name] ?? { __esModule: true, default: empty }, module, module.exports)
  return module.exports.default
}
const node = (tag, text = '') => ({ tag, text, props: {}, children: [], parent: null })
const renderer = vue.createRenderer({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: () => node('#comment'),
  setText: (n, text) => { n.text = text }, setElementText: (n, text) => { n.text = text; n.children = [] }, patchProp: (n, key, _old, value) => { n.props[key] = value },
  parentNode: n => n.parent, nextSibling: n => n.parent?.children[n.parent.children.indexOf(n) + 1] ?? null,
  remove(n) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); n.parent = null },
  insert(n, parent, anchor = null) { if (n.parent) n.parent.children.splice(n.parent.children.indexOf(n), 1); const index = anchor ? parent.children.indexOf(anchor) : -1; parent.children.splice(index < 0 ? parent.children.length : index, 0, n); n.parent = parent },
})
const text = n => n.text + n.children.map(text).join('')
const descendants = n => [n, ...n.children.flatMap(descendants)]
function mount(t, component, props = {}) {
  const root = node('root'), app = renderer.createApp(component, props)
  for (const name of ['a-button', 'a-input', 'a-select', 'a-empty', 'a-space', 'a-checkbox', 'a-radio-group', 'a-radio-button', 'a-alert', 'a-tag', 'a-menu-item']) {
    app.component(name, { inheritAttrs: false, setup: (_p, { attrs, slots }) => () => vue.h(name === 'a-button' ? 'button' : 'div', attrs, slots.default?.()) })
  }
  app.component('a-table', { props: ['dataSource'], setup: (p, { slots }) => () => vue.h('div', p.dataSource.length ? p.dataSource.map(record=>slots.bodyCell?.({column:{key:'name'},record})) : slots.emptyText?.()) })
  for (const name of ['RefreshCw', 'Plus']) app.component(name, () => vue.h('svg'))
  app.mount(root); t.after(() => app.unmount())
  return { buttons: () => descendants(root).filter(n => n.tag === 'button') }
}
const canCreate = ['cluster.create', 'cluster.execute', 'cluster.update', 'cluster.read']
test('详情抽屉保留自定义标题，普通标题仍按属性显示', t => {
  const Drawer = compile(readFileSync(new URL('../src/components/ResourceDetailDrawer.vue', import.meta.url), 'utf8'))
  for (const custom of [false, true]) {
    const root = node('root'), app = renderer.createApp({
      render: () => vue.h(Drawer, { open: true, title: '连接详情' }, custom ? { title: () => vue.h('strong', '目标集群') } : {}),
    })
    app.component('a-drawer', {
      props: ['title'],
      setup: (props, { slots }) => () => vue.h('section', props.title ?? slots.title?.()),
    })
    app.mount(root); t.after(() => app.unmount())
    assert.equal(text(root), custom ? '目标集群' : '连接详情')
  }
})
function inventory(permissions, navigations = []) {
  return compile(readFileSync(new URL('../src/components/ClusterInventory.vue', import.meta.url), 'utf8'), {
    '@/components/ClusterConnectionScope.vue': { __esModule: true, default: compile(readFileSync(new URL('../src/components/ClusterConnectionScope.vue', import.meta.url), 'utf8'), {
      '@/utils/clusterConnection': { useClusterConnections: () => ({ statuses: vue.ref({}), error: vue.ref(''), pending: vue.ref(false), refresh() {} }) },
    }) },
    'lucide-vue-next': Object.fromEntries(['ArrowUpRight', 'Link2', 'Pencil', 'Plus', 'RefreshCw', 'Search', 'Server'].map(name => [name, () => vue.h('svg')])),
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({ replace(route) { navigations.push({kind:'replace',route}) }, push(route) { navigations.push({kind:'push',route}) } }) },
    '@/stores/auth': { useAuthStore: () => ({ canAny: codes => codes.some(code => permissions.has(code)) }) },
    '@/utils/resourceList': loadFrontend('../src/utils/resourceList.ts', { vue, 'vue-i18n': { useI18n: () => ({ t: key => key }) } }),
    '@/utils/infrastructureRelations': loadFrontend('../src/utils/infrastructureRelations.ts'),
  })
}

test('真实集群列表有数据或为空时都不提供接入入口，保留刷新与创建集群', async t => {
  for (const clusters of [[], [{ id: 'cluster-a', name: '测试集群', hosts: [] }]]) {
    await t.test(clusters.length ? '已有集群' : '空列表', t => {
      const events = [], h = mount(t, inventory(new Set(canCreate)), { clusters, hosts: [], loading: false, onRefresh: () => events.push('refresh'), onProvision: () => events.push('provision') })
      assert.equal(h.buttons().some(button => text(button) === '接入集群'), false)
      const refresh = h.buttons().find(button => text(button) === '刷新'), provision = h.buttons().find(button => text(button) === '创建集群')
      assert.ok(refresh); assert.ok(provision)
      assert.equal(h.buttons().filter(button => text(button) === '创建集群').length, 1)
      refresh.props.onClick(); provision.props.onClick()
      assert.deepEqual(events, ['refresh', 'provision'])
    })
  }
})

test('集群名称进入准确总览；无资源权限或停用时保留详情入口', t => {
  for(const [readResources,active] of [[true,true],[false,true],[true,false]]){
    const navigations=[],permissions=new Set(['cluster.read',...(readResources?['kubernetes.node.read']:[])])
    const cluster={id:'cluster-target',name:'目标集群',is_active:active,hosts:[]}
    const h=mount(t,inventory(permissions,navigations),{clusters:[cluster],hosts:[],loading:false})
    const name=h.buttons().find(button=>text(button)==='目标集群');assert.ok(name);name.props.onClick()
    assert.deepEqual(navigations.at(-1),readResources&&active
      ?{kind:'push',route:{path:'/kubernetes',query:{cluster:'cluster-target',view:'overview'}}}
      :{kind:'replace',route:{path:'/hosts',query:{view:'clusters',cluster:'cluster-target'}}})
  }
})

test('真实集群列表没有创建权限时不显示安装入口', t => {
  const h = mount(t, inventory(new Set(['cluster.read'])), { clusters: [], hosts: [], loading: false })
  assert.deepEqual(h.buttons().map(text), ['刷新'])
})

const heading = baseParse(template).children.flatMap(function visit(n) { return n.type === 1 && n.props.some(p => p.type === 6 && p.name === 'class' && p.value?.content === 'resource-panel-heading') ? [n] : (n.children ?? []).flatMap(visit) })[0]
const OverviewHeading = compile(`<script setup lang="ts">
import {ref,toRefs} from 'vue'
const props=defineProps<{state:any}>(),{group,can,clusterDrawer}=toRefs(props.state)
const types=[],activeType=undefined,contextDisabled=false,nodeView='list',loading=false,overviewLoading=false,topologyLoading=false,cluster='',clusterInfo=undefined,autoRefresh=ref(false),clusterRbacOpen=ref(false),joinOpen=ref(false)
const auth={canAny:()=>false},t=(key:string)=>key,refreshAll=()=>{},openProvision=()=>{},setNodeView=()=>{},selectResource=()=>{},resourceTabKey=()=>{},edit=()=>{}
</script><template>${heading.loc.source}</template>`)

test('真实总览内容的接入入口随页面和创建权限变化，点击只打开接入抽屉', async t => {
  const state = vue.reactive({ group: 'overview', can: permission => permission === 'cluster.create', clusterDrawer: false }), h = mount(t, OverviewHeading, { state })
  const entry = () => h.buttons().filter(button => text(button) === '接入集群')
  assert.equal(entry().length, 1); entry()[0].props.onClick(); assert.equal(state.clusterDrawer, true)
  state.can = () => false; await vue.nextTick(); assert.equal(entry().length, 0)
  state.can = permission => permission === 'cluster.create'
  for (const group of ['nodes', 'workloads', 'config', 'storage', 'network']) {
    state.group = group; await vue.nextTick(); assert.equal(entry().length, 0)
  }
})
