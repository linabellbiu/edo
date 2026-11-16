import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { baseParse } from '@vue/compiler-dom'
import ts from 'typescript'
import * as vue from 'vue'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const source = readFileSync(new URL('../src/views/KubernetesView.vue', import.meta.url), 'utf8')
const template = parse(source).descriptor.template.content
const inventorySource = readFileSync(new URL('../src/components/ClusterInventory.vue', import.meta.url), 'utf8')
const inventoryTemplate = parse(inventorySource).descriptor.template.content
const entries = []
function inspect(node, parents = []) {
  if (node.type === 1 && node.tag === 'a-button' && node.children.some(child => child.type === 2 && child.content.trim() === '接入集群')) entries.push({node, parents})
  for (const child of node.children ?? []) inspect(child, [...parents, node])
}
inspect(baseParse(inventoryTemplate))

test('接入集群只在集群列表工具栏保留一个入口，总览不再挂载接入抽屉', () => {
  assert.equal(entries.length, 1)
  assert.ok(entries[0].parents.some(node => node.type === 1 && node.tag === 'header' && node.props.some(prop => prop.type === 6 && prop.name === 'class' && prop.value?.content === 'cluster-toolbar')))
  assert.doesNotMatch(source, /KubernetesClusterDrawer|clusterDrawer|接入集群/)
  assert.match(inventoryTemplate, /<KubernetesClusterDrawer[^>]*v-model:open="createOpen"[^>]*:can-test="auth\.canAny\(\['cluster.execute'\]\)"[^>]*@created="clusterCreated"/)
  assert.match(inventoryTemplate, /<KubernetesClusterDrawer[^>]*v-model:open="editOpen"[^>]*:cluster="editingCluster"[^>]*@saved="clusterSaved"/)
})

test('接入入口依据创建权限显示，不要求预先添加主机或具备安装权限', () => {
  const entry = entries[0]
  assert.ok(entry)
  const condition = [...entry.parents, entry.node].flatMap(node => (node.props ?? []).filter(prop => prop.type === 7 && prop.name === 'if').map(prop => `(${prop.exp?.content})`)).join(' && ')
  const visible = new Function('auth', `return ${condition}`)
  for (const permissions of [[], ['cluster.read'], ['cluster.execute'], ['cluster.create'], ['cluster.create', 'cluster.execute']]) {
    assert.equal(visible({canAny: codes => codes.some(code => permissions.includes(code))}), permissions.includes('cluster.create'))
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
function mount(t, component, props = {}, { expandPopovers = false } = {}) {
  const root = node('root'), app = renderer.createApp(component, props)
  for (const name of ['a-button', 'a-input', 'a-select', 'a-empty', 'a-space', 'a-checkbox', 'a-radio-group', 'a-radio-button', 'a-alert', 'a-tag', 'a-menu-item', 'a-tooltip', 'a-popover']) {
    app.component(name, { inheritAttrs: false, setup: (_p, { attrs, slots }) => () => vue.h(name === 'a-button' ? 'button' : 'div', attrs, expandPopovers && name === 'a-popover' ? [slots.default?.(), slots.content?.()] : slots.default?.()) })
  }
  app.component('a-table', { props: ['dataSource', 'columns'], setup: (p, { slots }) => () => vue.h('div', p.dataSource.length ? p.dataSource.flatMap(record=>p.columns.map(column=>slots.bodyCell?.({column,record}))) : slots.emptyText?.()) })
  for (const name of ['RefreshCw', 'Plus']) app.component(name, () => vue.h('svg'))
  app.mount(root); t.after(() => app.unmount())
  return { buttons: () => descendants(root).filter(n => n.tag === 'button'), nodes: () => descendants(root), text: () => text(root) }
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

test('真实集群列表有数据或为空时都有唯一接入入口，保留刷新与创建集群', async t => {
  for (const clusters of [[], [{ id: 'cluster-a', name: '测试集群', hosts: [] }]]) {
    await t.test(clusters.length ? '已有集群' : '空列表', t => {
      const events = [], h = mount(t, inventory(new Set(canCreate)), { clusters, hosts: [], loading: false, onRefresh: () => events.push('refresh'), onProvision: () => events.push('provision') })
      assert.equal(h.buttons().filter(button => text(button) === '接入集群').length, 1)
      const refresh = h.buttons().find(button => text(button) === '刷新'), provision = h.buttons().find(button => text(button) === '创建集群')
      assert.ok(refresh); assert.ok(provision)
      assert.equal(h.buttons().filter(button => text(button) === '创建集群').length, 1)
      refresh.props.onClick(); provision.props.onClick()
      assert.deepEqual(events, ['refresh', 'provision'])
    })
  }
})

test('集群名称始终原地只读，启用与权限变化均不导航且完整名称可聚焦查看', t => {
  assert.doesNotMatch(inventorySource,/useRouter|function openCluster|canManageResources/)
  const fullName='目标集群-用于验证长名称始终原地查看完整信息'
  for(const [readResources,active] of [[true,true],[false,true],[true,false],[false,false]]){
    const navigations=[],permissions=new Set(['cluster.read',...(readResources?['kubernetes.node.read']:[])])
    const cluster={id:'cluster-target',name:fullName,is_active:active,hosts:[]}
    const h=mount(t,inventory(permissions,navigations),{clusters:[cluster],hosts:[],loading:false})
    assert.equal(h.buttons().some(button=>text(button)===fullName),false)
    const name=h.nodes().find(n=>n.tag==='strong'&&text(n)===fullName)
    assert.ok(name)
    assert.match(name.props.class,/\bcluster-name\b/)
    assert.equal(String(name.props.tabindex),'0')
    assert.equal(name.props.onClick,undefined)
    assert.equal(name.parent.props.title,fullName)
    assert.deepEqual(name.parent.props.trigger,['hover','focus','click'])
    assert.deepEqual(navigations,[])
    assert.equal(h.buttons().some(button=>text(button)==='详情'),false)
  }
})

test('真实集群列表没有创建权限时不显示安装或接入入口', t => {
  const h = mount(t, inventory(new Set(['cluster.read'])), { clusters: [], hosts: [], loading: false })
  assert.deepEqual(h.buttons().map(text), ['刷新'])
})

const heading = baseParse(template).children.flatMap(function visit(n) { return n.type === 1 && n.props.some(p => p.type === 6 && p.name === 'class' && p.value?.content === 'resource-panel-heading') ? [n] : (n.children ?? []).flatMap(visit) })[0]
const OverviewHeading = compile(`<script setup lang="ts">
import {ref,toRefs} from 'vue'
const props=defineProps<{state:any}>(),{group,can}=toRefs(props.state)
const types=[],activeType=undefined,contextDisabled=false,nodeView='list',loading=false,overviewLoading=false,topologyLoading=false,cluster='',clusterInfo=undefined,autoRefresh=ref(false),clusterRbacOpen=ref(false),joinOpen=ref(false)
const auth={canAny:()=>false},t=(key:string)=>key,refreshAll=()=>{},openProvision=()=>{},setNodeView=()=>{},selectResource=()=>{},resourceTabKey=()=>{},edit=()=>{}
</script><template>${heading.loc.source}</template>`)

test('真实总览及其他资源页面均没有接入入口', async t => {
  const state = vue.reactive({ group: 'overview', can: () => true }), h = mount(t, OverviewHeading, { state })
  for (const group of ['overview', 'nodes', 'workloads', 'config', 'storage', 'network']) {
    state.group = group; await vue.nextTick()
    assert.equal(h.buttons().some(button => text(button) === '接入集群'), false)
  }
})

test('集群列表直接呈现连接信息及主机关联，不再保留详情抽屉或详情动作', t => {
  assert.doesNotMatch(inventorySource, /ResourceDetailDrawer|function openDetails|function closeDetails|@click="openDetails\(/)
  assert.doesNotMatch(inventoryTemplate, />详情<|集群详情/)
  const cluster={id:'standalone',name:'独立集群',api_server:'https://cluster.example:6443',default_namespace:'default',is_active:true,hosts:[]}
  const h=mount(t,inventory(new Set(canCreate)),{clusters:[cluster],hosts:[],loading:false})
  assert.match(h.text(), /独立集群/)
  assert.match(h.text(), /https:\/\/cluster\.example:6443/)
  assert.match(h.text(), /未关联主机/)
  assert.equal(h.buttons().some(button=>text(button)==='详情'), false)
})

test('关联主机在原地弹层呈现完整名称和地址，不打开主机或导航', t => {
  assert.doesNotMatch(inventorySource,/function openHost|function canOpenHost|emit\(['"]host['"]|@click="openHost\(/)
  const navigations=[]
  const hosts=[
    {id:'host-a',name:'第一台很长的关联主机完整名称',address:'192.0.2.10',is_active:true,capabilities:[]},
    {id:'host-b',name:'第二台很长的关联主机完整名称',address:'192.0.2.11',is_active:false,capabilities:[]},
  ]
  const cluster={id:'multi-host',name:'多主机集群',api_server:'https://cluster.example:6443',default_namespace:'default',is_active:true,hosts}
  const Inventory=inventory(new Set([...canCreate,'kubernetes.node.read']),navigations)
  assert.equal(Inventory.emits.includes('host'),false)
  const h=mount(t,Inventory,{clusters:[cluster],hosts,loading:false},{expandPopovers:true})
  const summary=h.buttons().find(button=>button.props.class==='host-summary-trigger')
  assert.ok(summary)
  assert.equal(summary.props['aria-label'],'查看 2 台关联主机的信息')
  assert.equal(summary.props.onClick,undefined)
  assert.deepEqual(summary.parent.props.trigger,['hover','focus','click'])
  const popup=h.nodes().find(n=>n.props.class==='cluster-host-popover')
  assert.ok(popup)
  assert.equal(popup.props.role,'list')
  assert.equal(String(popup.props.tabindex),'0')
  assert.equal(descendants(popup).filter(n=>n.props.role==='listitem').length,hosts.length)
  for(const host of hosts){
    assert.ok(text(popup).includes(host.name))
    assert.ok(text(popup).includes(host.address))
  }
  assert.equal(descendants(popup).some(n=>n.tag==='button'),false)
  assert.ok(descendants(popup).every(n=>n.props.onClick===undefined))
  assert.deepEqual(navigations,[])
})

test('接入表单独立于编辑抽屉，接入成功刷新列表且不导航到详情', async t => {
  const permissions=['cluster.create','cluster.execute']
  const h=componentHarness('../src/components/ClusterInventory.vue', {
    codes:permissions, props:vue.reactive({clusters:[],hosts:[],loading:false}),
    modules:{
      '@/utils/resourceList':loadFrontend('../src/utils/resourceList.ts',{vue,'vue-i18n':{useI18n:()=>({t:key=>key})}}),
      '@/utils/infrastructureRelations':loadFrontend('../src/utils/infrastructureRelations.ts'),
    },
  })
  t.after(h.stop)
  assert.equal(h.state.createOpen.value,false)
  assert.equal(h.state.editOpen.value,false)
  const entry=entries[0].node.props.find(prop=>prop.type===7&&prop.name==='on'&&prop.arg?.content==='click')?.exp?.content
  if(entry==='createOpen=true')h.state.createOpen.value=true
  else { assert.equal(entry,'openCreate');h.state.openCreate() }
  assert.equal(h.state.createOpen.value,true)
  assert.equal(h.state.editOpen.value,false)
  h.state.clusterCreated({id:'new-independent',name:'独立接入'})
  await vue.nextTick()
  assert.deepEqual(h.events.map(event=>event.name),['refresh'])
  assert.deepEqual(h.replacements,[])
})

test('零主机接入仍先测试当前草稿，通过后创建且不提交主机关联', async t => {
  const cluster={id:'new-independent',name:'直接接入',api_server:'https://isolated.example:6443',default_namespace:'default',is_active:true}
  const h=componentHarness('../src/components/KubernetesClusterDrawer.vue',{
    codes:['cluster.create','cluster.execute'],
    props:vue.reactive({open:true,canTest:true,canAssociate:false,hosts:[],clusters:[]}),
    globals:{localStorage:{getItem:()=>''}},
    api:{post:async(path)=>path==='/kubernetes/clusters/test'?{data:{api_server:cluster.api_server,version:'v1.35.5'}}:{data:{cluster}}},
  })
  t.after(h.stop)
  Object.assign(h.state.form,{name:cluster.name,kubeconfig:'隔离验收专用配置'})
  await h.state.submit()
  assert.equal(h.requests.length,0)
  await h.state.testConnection()
  assert.equal(h.state.connectionTested.value,true)
  await h.state.submit()
  assert.deepEqual(h.requests.map(({method,path})=>({method,path})),[
    {method:'post',path:'/kubernetes/clusters/test'},
    {method:'post',path:'/kubernetes/clusters'},
  ])
  for(const request of h.requests){
    assert.deepEqual(request.body,{name:cluster.name,mode:'kubeconfig',default_namespace:'default',kubeconfig:'隔离验收专用配置'})
    assert.equal('host_ids' in request.body,false)
  }
  assert.deepEqual(h.events.map(event=>event.name),['created','update:open'])
  assert.equal(h.events[0].args[0].id,cluster.id)
  assert.equal(h.events[1].args[0],false)
})
