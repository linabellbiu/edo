import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import { parse, compileScript } from '@vue/compiler-sfc'
import { createRouter, createMemoryHistory } from 'vue-router'

function load(path, modules = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const code = path.endsWith('.vue') ? compileScript(parse(source).descriptor, { id: 'kube-navigation' }).content : source
  const output = ts.transpileModule(code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output)(key => modules[key] ?? (key==='@/utils/kubernetesWorkspace'?load('../src/utils/kubernetesWorkspace.ts',{}):{}), module, module.exports)
  return module.exports
}
const nav = load('../src/router/navigation.ts')
const api = load('../src/api/kubernetes.ts')
const infrastructure = nav.navigation.flatMap(section => section.branches ?? []).find(branch => branch.key === 'infrastructure')
test('配置与存储包含配置中心和存储，三个存储资源不进入主侧栏', () => {
  const kube = infrastructure.items.find(item => item.path === '/kubernetes')
  assert.deepEqual(kube.children.map(item => item.label), ['nav.hosts','k8s.clusters','k8s.overview','k8s.nodes','k8s.workloads','k8s.configStorage','k8s.network'])
  assert.equal(nav.flatNavigation().some(item => item.path === '/kubernetes'), false)
  assert.equal(nav.findNavigationItem('/kubernetes').label, 'k8s.overview')
  assert.equal(nav.findNavigationItem('/kubernetes?view=unknown').label, 'k8s.overview')
  assert.equal(nav.findNavigationItem('/kubernetes?view=nodes&view=network').label, 'k8s.overview')
  for (const child of nav.flatNavigation().filter(item=>item.path.startsWith('/kubernetes?'))) assert.equal(nav.findNavigationItem(child.path+'&cluster=test&namespace=team').label, child.label)
  assert.deepEqual(nav.navigationTrail('/kubernetes?view=nodes').map(item => item.label), ['nav.platform','nav.infrastructure','nav.containers','k8s.nodes'])
  const configStorage=kube.children.find(item=>item.label==='k8s.configStorage')
  assert.deepEqual(configStorage.children.map(item=>item.label),['k8s.config','k8s.storage'])
  assert.ok(configStorage.children.every(item=>!item.children))
  assert.equal(nav.flatNavigation().some(item=>item.label.startsWith('k8s.type_')),false)
  for(const resource of ['', 'bad', 'storageclasses', 'persistentvolumes', 'persistentvolumeclaims']) assert.equal(nav.findNavigationItem('/kubernetes?view=storage&resource='+resource).label,'k8s.storage')
  assert.deepEqual(nav.navigationTrail('/kubernetes?view=storage&resource=storageclasses').map(item=>item.label),['nav.platform','nav.infrastructure','nav.containers','k8s.configStorage','k8s.storage'])
})
test('配置与存储分别检查权限，无权访问的资源和空父菜单不会展示', () => {
  const storageOnly = nav.visibleNavigationItems(infrastructure.items, permissions => permissions.includes('kubernetes.storage.read'))
  assert.equal(storageOnly.length, 1)
  assert.deepEqual(storageOnly[0].children.map(item => item.label), ['k8s.clusters','k8s.overview','k8s.configStorage'])
  assert.deepEqual(storageOnly[0].children[2].children.map(item=>item.label),['k8s.storage'])
  const configOnly=nav.visibleNavigationItems(infrastructure.items,permissions=>permissions.includes('kubernetes.config.read'))
  assert.deepEqual(configOnly[0].children.map(item=>item.label),['k8s.clusters','k8s.overview','k8s.configStorage'])
  assert.deepEqual(configOnly[0].children[2].children.map(item=>item.label),['k8s.config'])
  assert.deepEqual(nav.visibleNavigationItems(infrastructure.items, () => false), [])
  const networkOnly = nav.visibleNavigationItems(infrastructure.items, permissions => permissions.includes('kubernetes.network.read'))
  assert.deepEqual(networkOnly[0].children.map(item => item.label), ['k8s.clusters','k8s.overview','k8s.network'])
})

test('集群目录有独立菜单和面包屑，主机详情仍选中主机入口', () => {
  assert.equal(nav.findNavigationItem('/hosts?view=clusters&cluster=test').label, 'k8s.clusters')
  assert.equal(nav.findNavigationItem('/hosts?view=hosts&host=test').label, 'nav.hosts')
  assert.deepEqual(nav.navigationTrail('/hosts?view=clusters').map(item => item.label), ['nav.platform','nav.infrastructure','nav.containers','k8s.clusters'])
})

test('从集群目录切换侧栏资源类型保留选中集群', () => {
  const { state, route, pushes, stop } = layoutHarness()
  route.path = '/hosts'; route.query = { view: 'clusters', cluster: 'standalone' }
  state.navigate('/kubernetes?view=nodes')
  assert.deepEqual(pushes[0], { path: '/kubernetes', query: { view: 'nodes', cluster: 'standalone' } })
  stop()
})
function layoutHarness() {
  const pushes = [], visits = [], scope = vue.effectScope(), preferences = vue.reactive({sidebarCollapsed:false})
  const route = vue.reactive({ path:'/kubernetes',fullPath:'/kubernetes?view=config&cluster=test',query:{view:'config',cluster:'test',namespace:'team'},meta:{title:'k8s.config'} })
  const component = load('../src/layouts/AppLayout.vue', {
    vue:{...vue,onMounted(){},onBeforeUnmount(){}}, 'vue-i18n':{useI18n:()=>({t:key=>key})},
    'vue-router':{useRoute:()=>route,useRouter:()=>({push:path=>pushes.push(path),resolve:()=>({name:'unknown',meta:{permissions:['no-access']}})})},
    '@/router/navigation':nav, '@/stores/auth':{useAuthStore:()=>({canAny:permissions=>!permissions.length||permissions.some(item=>['kubernetes.config.read','kubernetes.storage.read'].includes(item))})},
    '@/stores/preferences':{usePreferencesStore:()=>preferences}, '@/stores/tabs':{useTabsStore:()=>({tabs:[],recent:()=>[],canGoBack:()=>false,visit:()=>visits.push(route.fullPath),add(){}})},
  }).default
  let state
  scope.run(()=>{state=component.setup({}, {expose(){}})})
  return {state,route,pushes,visits,preferences,stop:()=>scope.stop()}
}
test('详情路径打开时自动展开父菜单，分类切换保留当前集群、命名空间及历史访问', async () => {
  const {state,route,pushes,visits,stop} = layoutHarness()
  assert.deepEqual(state.selectedKeys.value, ['/kubernetes?view=config'])
  assert.deepEqual(state.openKeys.value, ['infrastructure','/kubernetes','/kubernetes?section=config-storage'])
  state.navigate('/kubernetes?view=overview')
  assert.deepEqual(pushes[0], {path:'/kubernetes',query:{view:'overview',cluster:'test',namespace:'team'}})
  route.query.view='overview';route.fullPath='/kubernetes?view=overview&cluster=test';await vue.nextTick()
  assert.deepEqual(state.selectedKeys.value, ['/kubernetes?view=overview'])
  assert.equal(visits.length,2)
  assert.equal(state.allowedHistoryPath('/kubernetes?view=nodes'),false)
  assert.equal(state.allowedHistoryPath('/kubernetes?view=config'),true)
  assert.equal(state.historyContext('/kubernetes?view=config'),'nav.platform > nav.infrastructure > nav.containers > k8s.configStorage')
  stop()
})
test('侧栏折叠时不覆盖已展开分支，恢复后重新展开当前 Kubernetes 分类', async () => {
  const {state,preferences,stop}=layoutHarness()
  preferences.sidebarCollapsed=true;await vue.nextTick();state.updateOpenKeys([])
  assert.deepEqual(state.openKeys.value,['infrastructure','/kubernetes','/kubernetes?section=config-storage'])
  state.openKeys.value=[];preferences.sidebarCollapsed=false;await vue.nextTick()
  assert.deepEqual(state.openKeys.value,['infrastructure','/kubernetes','/kubernetes?section=config-storage'])
  stop()
})
test('存储资源页签共享左侧存储入口，切换配置中心仍保留集群和命名空间', async () => {
  const {state,route,pushes,stop}=layoutHarness()
  state.navigate('/kubernetes?view=storage&resource=storageclasses')
  assert.deepEqual(pushes[0],{path:'/kubernetes',query:{view:'storage',resource:'storageclasses',cluster:'test',namespace:'team'}})
  route.query.view='storage';route.query.resource='storageclasses';route.fullPath='/kubernetes?view=storage&resource=storageclasses&cluster=test';await vue.nextTick()
  assert.deepEqual(state.openKeys.value,['infrastructure','/kubernetes','/kubernetes?section=config-storage'])
  assert.deepEqual(state.selectedKeys.value,['/kubernetes?view=storage'])
  state.navigate('/kubernetes?view=config')
  assert.deepEqual(pushes[1],{path:'/kubernetes',query:{view:'config',cluster:'test',namespace:'team'}})
  route.query.resource='persistentvolumes';route.fullPath='/kubernetes?view=storage&resource=persistentvolumes';await vue.nextTick()
  assert.deepEqual(state.selectedKeys.value,['/kubernetes?view=storage'])
  stop()
})
test('Kubernetes 分类深链接使用自身查看权限，分类名称进入页面标签和标题', async () => {
  let guard, after
  const fakeRouter = { beforeEach:callback=>guard=callback, afterEach:callback=>after=callback }
  load('../src/router/index.ts', {
    'vue-router':{createRouter:()=>fakeRouter,createWebHistory(){}},
    '@/locales':{__esModule:true,default:{global:{t:key=>key}}}, './navigation':nav,
    '@/stores/auth':{useAuthStore:()=>({ensureLoaded:async()=>{},user:{id:'reader'},canAny:permissions=>permissions.includes('kubernetes.storage.read')})},
  })
  const route = view=>({path:'/kubernetes',fullPath:`/kubernetes?view=${view}`,query:{view},meta:{permissions:['kubernetes.node.read','kubernetes.storage.read'],title:'k8s.title'}})
  assert.equal(await guard(route('nodes')), '/projects')
  const config = route('config')
  assert.equal(await guard(config), '/projects')
  const storage=route('storage')
  assert.equal(await guard(storage),true);assert.equal(storage.meta.title,'k8s.storage')
  const overview = route('overview')
  assert.equal(await guard(overview),true);assert.equal(overview.meta.title,'k8s.overview')
  assert.equal(typeof after,'function')
})
test('集群与命名空间使用独立筛选树，内容区不再重复展示分类菜单', () => {
  const source=readFileSync(new URL('../src/views/KubernetesView.vue',import.meta.url),'utf8')
  const template=parse(source).descriptor.template.content
  assert.match(template, /<KubeNamespaceTree /)
  assert.match(template, /:selected="selectedNamespaces"/)
  assert.doesNotMatch(template, /kube-workspace-namespace|kube-workspace-cluster/)
  assert.doesNotMatch(template,/workspace-tabs|v-for="item in groups"|context-scope/)
  assert.match(template,/v-if="types.length>1[^"]*" class="resource-type-tabs"/)
  assert.match(template,/@click="selectResource\(type.resource\)"/)
})
test('存储列表跟随导航和浏览历史，刷新恢复准确类型，无效类型回到存储卷声明', async () => {
  const route=vue.reactive({query:{view:'storage',resource:'storageclasses'}}), pushes=[], scope=vue.effectScope()
  const component=load('../src/views/KubernetesView.vue',{
    vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},
    'vue-router':{useRoute:()=>route,useRouter:()=>({push:value=>pushes.push(value)})},
    '@/api/kubernetes':api,'@/stores/auth':{useAuthStore:()=>({canAny:()=>true})},
  }).default
  let state
  scope.run(()=>{state=component.setup({}, {expose(){}})})
  assert.equal(state.activeType.value.resource,'storageclasses')
  assert.equal(state.activeType.value.namespaced,false)
  route.query.resource='persistentvolumeclaims';await vue.nextTick()
  assert.equal(state.activeType.value.resource,'persistentvolumeclaims')
  assert.equal(state.activeType.value.namespaced,true)
  route.query.resource='persistentvolumes';await vue.nextTick()
  assert.equal(state.activeType.value.resource,'persistentvolumes')
  route.query.resource='unknown';await vue.nextTick()
  assert.equal(state.activeType.value.resource,'persistentvolumeclaims')
  route.query.resource=['storageclasses','persistentvolumes'];await vue.nextTick()
  assert.equal(state.activeType.value.resource,'persistentvolumeclaims')
  route.query.view='config';await vue.nextTick()
  assert.deepEqual(state.types.value.map(item=>item.resource),['configmaps','secrets','namespaces'])
  state.selectResource('storageclasses')
  assert.deepEqual(pushes[0].query,{view:'storage',resource:'storageclasses'})
  scope.stop()
})

test('总览所有资源入口和资源页签进入真实历史，后退、前进及刷新恢复准确资源与范围', async () => {
  const router=createRouter({history:createMemoryHistory(),routes:[{path:'/kubernetes',component:{}}]})
  const origin='/kubernetes?view=overview&cluster=test&namespace=prod&namespace=team'
  await router.push(origin)
  const route=new Proxy({}, {get:(_target,key)=>router.currentRoute.value[key]})
  const component=load('../src/views/KubernetesView.vue',{
    vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},
    'vue-router':{useRoute:()=>route,useRouter:()=>router},
    '@/api/kubernetes':api,'@/stores/auth':{useAuthStore:()=>({canAny:()=>true})},
  }).default
  function state(){const scope=vue.effectScope();let value;scope.run(()=>{value=component.setup({}, {expose(){}})});return {value,stop:()=>scope.stop()}}
  const navigate=action=>new Promise((resolve,reject)=>{const remove=router.afterEach((_to,_from,failure)=>{remove();failure?reject(failure):resolve()});action()})
  const page=state()
  for(const type of api.kubeTypes){
    await navigate(()=>page.value.selectResource(type.resource))
    assert.equal(page.value.activeType.value.resource,type.resource)
    assert.equal(router.currentRoute.value.query.view,type.group)
    assert.equal(router.currentRoute.value.query.resource,type.resource)
    assert.equal(router.currentRoute.value.query.from,'overview')
    assert.equal(page.value.fromOverview.value,true)
    assert.equal(router.currentRoute.value.query.cluster,'test')
    assert.deepEqual(router.currentRoute.value.query.namespace,['prod','team'])
    const restored=state()
    assert.equal(restored.value.activeType.value.resource,type.resource)
    assert.equal(restored.value.fromOverview.value,true)
    restored.stop()
    await navigate(()=>router.back())
    assert.equal(router.currentRoute.value.fullPath,origin)
    assert.equal(page.value.activeType.value,undefined)
    assert.equal(page.value.fromOverview.value,false)
    await navigate(()=>router.forward())
    assert.equal(router.currentRoute.value.query.resource,type.resource)
    assert.equal(page.value.activeType.value.resource,type.resource)
    await navigate(()=>router.back())
  }
  page.stop()
})

test('只有总览来源显示返回入口，直接打开不显示；返回清除来源且保留范围', () => {
  const pushes=[],backs=[],route=vue.reactive({query:{view:'workloads',resource:'pods',cluster:'test',namespace:['prod','team']}})
  const target='/kubernetes?view=overview&cluster=test&namespace=prod&namespace=team'
  const router={options:{history:{state:{back:target}}},resolve:()=>({fullPath:target}),push:value=>pushes.push(value),back:()=>backs.push(true)}
  const component=load('../src/views/KubernetesView.vue',{
    vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},
    'vue-router':{useRoute:()=>route,useRouter:()=>router},'@/api/kubernetes':api,'@/stores/auth':{useAuthStore:()=>({canAny:()=>true})},
  }).default
  const scope=vue.effectScope();let state;scope.run(()=>{state=component.setup({}, {expose(){}})})
  assert.equal(state.fromOverview.value,false)
  state.backToOverview();assert.equal(backs.length,0);assert.equal(pushes.length,0)
  route.query.from='overview'
  assert.equal(state.fromOverview.value,true)
  state.backToOverview();assert.equal(backs.length,1);assert.equal(pushes.length,0)
  router.options.history.state.back='/applications'
  state.backToOverview()
  assert.deepEqual(pushes[0].query,{view:'overview',resource:undefined,from:undefined,cluster:'test',namespace:['prod','team']})
  state.selectResource('deployments');assert.equal(pushes[1].query.from,'overview')
  state.editorOpen.value=true;state.backToOverview();state.selectResource('deployments');assert.equal(pushes.length,2)
  scope.stop()
})

test('侧栏分类清除总览来源，页面标签保留显式来源和资源类型', () => {
  const {state,route,pushes,stop}=layoutHarness()
  route.query.from='overview'
  state.navigate('/kubernetes?view=config&resource=secrets&cluster=test&namespace=team')
  assert.equal(pushes[0].query.resource,'secrets')
  assert.equal(pushes[0].query.from,undefined)
  state.navigate('/kubernetes?view=workloads')
  assert.equal(pushes[1].query.resource,undefined)
  assert.deepEqual(pushes[1].query.namespace,'team')
  state.navigate('/kubernetes?view=workloads&resource=pods&cluster=test&namespace=prod&namespace=team&from=overview')
  assert.deepEqual(pushes[2].query.namespace,['prod','team'])
  assert.equal(pushes[2].query.resource,'pods')
  assert.equal(pushes[2].query.from,'overview')
  stop()
})

test('各分类直接进入不显示返回总览，只读用户没有空操作栏',async()=>{
  const route=vue.reactive({query:{view:'overview'}}),scope=vue.effectScope()
  const component=load('../src/views/KubernetesView.vue',{
    vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},
    'vue-router':{useRoute:()=>route,useRouter:()=>({})},'@/api/kubernetes':api,
    '@/stores/auth':{useAuthStore:()=>({canAny:permissions=>permissions.some(permission=>permission.endsWith('.read'))})},
  }).default
  let state;scope.run(()=>{state=component.setup({}, {expose(){}})})
  for(const type of api.kubeTypes){
    route.query={view:type.group,resource:type.resource};await vue.nextTick()
    assert.equal(state.fromOverview.value,false)
    assert.equal(state.showResourceToolbar.value,false)
    route.query.from='overview';await vue.nextTick()
    assert.equal(state.fromOverview.value,true)
    assert.equal(state.showResourceToolbar.value,true)
  }
  route.query={view:'workloads',from:['overview','overview']};await vue.nextTick()
  assert.equal(state.fromOverview.value,false)
  scope.stop()
})

test('集群和命名空间地址同步保留滚动，分类与资源切换回顶，后退使用保存位置', () => {
  let scroll
  load('../src/router/index.ts',{'vue-router':{createRouter:options=>{scroll=options.scrollBehavior;return {beforeEach(){},afterEach(){}}},createWebHistory(){}},'./navigation':nav})
  const from={path:'/kubernetes',query:{view:'workloads',resource:'pods',cluster:'a'}}
  assert.equal(scroll({...from,query:{...from.query,cluster:'b',namespace:['prod','team']}},from),false)
  assert.deepEqual(scroll({...from,query:{...from.query,resource:'deployments'}},from),{top:0})
  assert.deepEqual(scroll({...from,query:{view:'overview'}},from),{top:0})
  const saved={top:420,left:0};assert.equal(scroll(from,from,saved),saved)
})
