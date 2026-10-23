import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
import * as vue from 'vue'
import { parse, compileScript } from '@vue/compiler-sfc'

function load(path, modules = {}) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const code = path.endsWith('.vue') ? compileScript(parse(source).descriptor, { id: 'kube-navigation' }).content : source
  const output = ts.transpileModule(code, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } }).outputText
  const module = { exports: {} }
  new Function('require', 'module', 'exports', output)(key => modules[key] ?? {}, module, module.exports)
  return module.exports
}
const nav = load('../src/router/navigation.ts')
const api = load('../src/api/kubernetes.ts')
const infrastructure = nav.navigation.flatMap(section => section.branches ?? []).find(branch => branch.key === 'infrastructure')
test('配置中心与存储分开，存储资源深链接选中叶子并展开完整导航路径', () => {
  const kube = infrastructure.items.find(item => item.path === '/kubernetes')
  assert.deepEqual(kube.children.map(item => item.label), ['k8s.overview','k8s.nodes','k8s.workloads','k8s.config','k8s.storage','k8s.network'])
  assert.equal(nav.flatNavigation().some(item => item.path === '/kubernetes'), false)
  assert.equal(nav.findNavigationItem('/kubernetes').label, 'k8s.overview')
  assert.equal(nav.findNavigationItem('/kubernetes?view=unknown').label, 'k8s.overview')
  assert.equal(nav.findNavigationItem('/kubernetes?view=nodes&view=network').label, 'k8s.overview')
  for (const child of nav.flatNavigation().filter(item=>item.path.startsWith('/kubernetes?'))) assert.equal(nav.findNavigationItem(child.path+'&cluster=test&namespace=team').label, child.label)
  assert.deepEqual(nav.navigationTrail('/kubernetes?view=nodes').map(item => item.label), ['nav.platform','nav.infrastructure','k8s.title','k8s.nodes'])
  const storage=kube.children.find(item=>item.label==='k8s.storage')
  assert.deepEqual(storage.children.map(item=>new URL(item.path,'http://edo.local').searchParams.get('resource')),['storageclasses','persistentvolumes','persistentvolumeclaims'])
  for(const path of ['/kubernetes?view=storage','/kubernetes?view=storage&resource=bad','/kubernetes?view=storage&resource=storageclasses&resource=persistentvolumes']) assert.equal(nav.findNavigationItem(path).label,'k8s.type_persistentvolumeclaims')
  assert.deepEqual(nav.navigationTrail('/kubernetes?view=storage&resource=storageclasses').map(item=>item.label),['nav.platform','nav.infrastructure','k8s.title','k8s.storage','k8s.type_storageclasses'])
})
test('配置与存储分别检查权限，无权访问的资源和空父菜单不会展示', () => {
  const storageOnly = nav.visibleNavigationItems(infrastructure.items, permissions => permissions.includes('kubernetes.storage.read'))
  assert.equal(storageOnly.length, 1)
  assert.deepEqual(storageOnly[0].children.map(item => item.label), ['k8s.overview','k8s.storage'])
  assert.equal(storageOnly[0].children[1].children.length,3)
  const configOnly=nav.visibleNavigationItems(infrastructure.items,permissions=>permissions.includes('kubernetes.config.read'))
  assert.deepEqual(configOnly[0].children.map(item=>item.label),['k8s.overview','k8s.config'])
  assert.deepEqual(nav.visibleNavigationItems(infrastructure.items, () => false), [])
  const networkOnly = nav.visibleNavigationItems(infrastructure.items, permissions => permissions.includes('kubernetes.network.read'))
  assert.deepEqual(networkOnly[0].children.map(item => item.label), ['k8s.overview','k8s.network'])
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
test('详情路径打开时自动展开两层父菜单，分类切换保留当前集群、命名空间及历史访问', async () => {
  const {state,route,pushes,visits,stop} = layoutHarness()
  assert.deepEqual(state.selectedKeys.value, ['/kubernetes?view=config'])
  assert.deepEqual(state.openKeys.value, ['infrastructure','/kubernetes'])
  state.navigate('/kubernetes?view=overview')
  assert.deepEqual(pushes[0], {path:'/kubernetes',query:{view:'overview',cluster:'test',namespace:'team'}})
  route.query.view='overview';route.fullPath='/kubernetes?view=overview&cluster=test';await vue.nextTick()
  assert.deepEqual(state.selectedKeys.value, ['/kubernetes?view=overview'])
  assert.equal(visits.length,2)
  assert.equal(state.allowedHistoryPath('/kubernetes?view=nodes'),false)
  assert.equal(state.allowedHistoryPath('/kubernetes?view=config'),true)
  assert.equal(state.historyContext('/kubernetes?view=config'),'nav.platform > nav.infrastructure > k8s.title')
  stop()
})
test('侧栏折叠时不覆盖已展开分支，恢复后重新展开当前 Kubernetes 分类', async () => {
  const {state,preferences,stop}=layoutHarness()
  preferences.sidebarCollapsed=true;await vue.nextTick();state.updateOpenKeys([])
  assert.deepEqual(state.openKeys.value,['infrastructure','/kubernetes'])
  state.openKeys.value=[];preferences.sidebarCollapsed=false;await vue.nextTick()
  assert.deepEqual(state.openKeys.value,['infrastructure','/kubernetes'])
  stop()
})
test('存储叶子切换保留集群和命名空间，离开存储移除资源参数，返回时恢复展开与选中', async () => {
  const {state,route,pushes,stop}=layoutHarness()
  state.navigate('/kubernetes?view=storage&resource=storageclasses')
  assert.deepEqual(pushes[0],{path:'/kubernetes',query:{view:'storage',resource:'storageclasses',cluster:'test',namespace:'team'}})
  route.query.view='storage';route.query.resource='storageclasses';route.fullPath='/kubernetes?view=storage&resource=storageclasses&cluster=test';await vue.nextTick()
  assert.deepEqual(state.openKeys.value,['infrastructure','/kubernetes','/kubernetes?view=storage'])
  assert.deepEqual(state.selectedKeys.value,['/kubernetes?view=storage&resource=storageclasses'])
  state.navigate('/kubernetes?view=config')
  assert.deepEqual(pushes[1],{path:'/kubernetes',query:{view:'config',cluster:'test',namespace:'team'}})
  route.query.resource='persistentvolumes';route.fullPath='/kubernetes?view=storage&resource=persistentvolumes';await vue.nextTick()
  assert.deepEqual(state.selectedKeys.value,['/kubernetes?view=storage&resource=persistentvolumes'])
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
  assert.equal(await guard(storage),true);assert.equal(storage.meta.title,'k8s.type_persistentvolumeclaims')
  const overview = route('overview')
  assert.equal(await guard(overview),true);assert.equal(overview.meta.title,'k8s.overview')
  assert.equal(typeof after,'function')
})
test('集群与命名空间使用可见标签，内容区不再重复展示分类菜单', () => {
  const source=readFileSync(new URL('../src/views/KubernetesView.vue',import.meta.url),'utf8')
  const template=parse(source).descriptor.template.content
  assert.match(template, /<label for="kube-workspace-cluster">/)
  assert.match(template, /<label for="kube-workspace-namespace">/)
  assert.doesNotMatch(template,/workspace-tabs|v-for="item in groups"|context-scope/)
  assert.match(template,/v-if="group!=='storage'&&types.length>1"/)
})
test('存储列表跟随导航和浏览历史，刷新恢复准确类型，无效类型回到存储卷声明', async () => {
  const route=vue.reactive({query:{view:'storage',resource:'storageclasses'}}), replacements=[], scope=vue.effectScope()
  const component=load('../src/views/KubernetesView.vue',{
    vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key})},
    'vue-router':{useRoute:()=>route,useRouter:()=>({replace:value=>replacements.push(value)})},
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
  assert.deepEqual(replacements[0].query,{view:'storage',resource:'storageclasses'})
  scope.stop()
})
