import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {loadFrontend} from './acceptanceHarness.mjs'

function routeHarness(codes=[],{superuser=false,anonymous=false,unavailable=false}={}) {
  const authStore=loadFrontend('../src/stores/auth.ts',{
    vue,pinia:{defineStore:(_name,factory)=>factory},axios:{__esModule:true,default:{isAxiosError:()=>false}},
    '@/api/client':{setAuthorizationFreshness(){}},
  },{localStorage:{getItem(){return null}}}).useAuthStore()
  authStore.user.value=anonymous?null:{id:'operator',permissions:codes,is_superuser:superuser}
  authStore.loaded.value=true;authStore.loadFailed.value=unavailable
  authStore.authorizationState.value='ready'
  const auth=vue.proxyRefs(authStore)
  const navigation=loadFrontend('../src/router/navigation.ts')
  let guard,after,options
  const router={beforeEach:value=>guard=value,afterEach:value=>after=value}
  loadFrontend('../src/router/index.ts',{
    'vue-router':{createRouter:value=>{options=value;return router},createWebHistory(){}},
    '@/stores/auth':{useAuthStore:()=>auth},'./navigation':navigation,
    '@/locales':{__esModule:true,default:{global:{t:key=>key}}},
  },{document:{title:''}})
  const records=options.routes.flatMap(record=>record.children?record.children.map(child=>({...child,path:'/'+child.path})):record)
  function target(path) {
    const url=new URL(path,'https://edo.example'),record=records.find(item=>item.path===url.pathname)
    return {path:url.pathname,fullPath:url.pathname+url.search,query:Object.fromEntries(url.searchParams),meta:{...record?.meta}}
  }
  return {auth,navigation,records,guard:async path=>guard(target(path)),after,options}
}

test('全部受保护路由没有授权时回到项目入口，仅个人设置和项目目录可进入，日志/终端新标签页自行鉴权',async()=>{
  const h=routeHarness()
  // /sessions/view 是新标签页的日志与终端入口，一个页面承载多种目标动作（流水线日志、Pod 日志与终端、
  // 主机终端、系统日志），无法用单一路由权限表达；页面按 sessionPermissions 对目标动作和项目范围独立
  // 鉴权，不允许时直接展示“没有查看此日志或打开终端的权限”。匿名访问仍然只能回到登录页。
  const selfAuthorized=new Set(['/sessions/view'])
  const routes=h.records.filter(record=>record.name&&record.path!=='/login')
  assert.ok(routes.length>=20)
  for(const route of routes) assert.equal(await h.guard(route.path),['/settings','/projects'].includes(route.path)||selfAuthorized.has(route.path)?true:'/projects',route.path)
  assert.equal(await routeHarness([],{anonymous:true}).guard('/sessions/view'),'/login?redirect='+encodeURIComponent('/sessions/view'))
})

test('每条普通路由仅接受声明的有效权限，读权限不被其他资源权限或通配字符串替代',async()=>{
  const all=routeHarness().records.filter(record=>record.name&&record.meta?.permissions?.length)
  for(const route of all) {
    const path=route.path==='/kubernetes'?'/kubernetes?view=overview':route.path
    for(const code of route.meta.permissions) assert.equal(await routeHarness([code]).guard(path),true,`${path}: ${code}`)
    assert.equal(await routeHarness(['unrelated.read','application.*']).guard(path),'/projects',path)
  }
  assert.equal(await routeHarness(['application.read']).guard('/repositories'),'/projects')
  assert.equal(await routeHarness(['role.update']).guard('/access?view=roles'),true)
})

test('同一路径的发布计划和流水线运行分别检查各自查看权限',async()=>{
  const plans=routeHarness(['release_plan.read']),runs=routeHarness(['pipeline_run.read'])
  assert.equal(await plans.guard('/release-plans?view=plans'),true)
  assert.equal(await plans.guard('/release-plans?view=runs'),'/projects')
  assert.equal(await runs.guard('/release-plans?view=runs&run_id=exact-run'),true)
  assert.equal(await runs.guard('/release-plans?view=plans'),'/projects')
})

test('匿名和身份加载失败保留完整深链接，超级管理员可进入全部受保护路由',async()=>{
  const path='/applications?application=exact-id'
  assert.equal(await routeHarness([],{anonymous:true}).guard(path),'/login?redirect='+encodeURIComponent(path))
  assert.equal(await routeHarness([],{anonymous:true,unavailable:true}).guard(path),'/login?redirect='+encodeURIComponent(path)+'&reason=unavailable')
  assert.equal(await routeHarness([],{anonymous:true}).guard('/login'),true)
  assert.equal(await routeHarness().guard('/login'),'/')
  const admin=routeHarness([],{superuser:true})
  for(const record of admin.records.filter(record=>record.name&&record.path!=='/login')) assert.equal(await admin.guard(record.path),true,record.path)
})

test('仅有 K8s 资源查看权限也能进入独立集群目录，不可进入主机目录',async()=>{
  for (const permission of ['kubernetes.node.read','kubernetes.workload.read','kubernetes.config.read','kubernetes.storage.read','kubernetes.network.read']) {
    const reader = routeHarness([permission])
    assert.equal(await reader.guard('/hosts?view=clusters&cluster=standalone'), true)
    assert.equal(await reader.guard('/hosts?view=hosts'), '/projects')
  }
  assert.equal(await routeHarness(['cluster.read']).guard('/hosts?view=clusters'), true)
  assert.equal(await routeHarness(['deployment.read']).guard('/hosts?view=clusters'), '/projects')
})

test('侧栏全部叶子按精确有效权限过滤，无权限父级不会残留，平台与项目入口不混淆',async()=>{
  const baseline=routeHarness(),leaves=baseline.navigation.flatNavigation()
  const flatten=items=>items.flatMap(item=>item.children?flatten(item.children):[item])
  const sections=baseline.navigation.navigation
  const visible=h=>sections.flatMap(section=>[...flatten(h.navigation.visibleNavigationItems(section.items||[],h.auth.canAny)),...(section.branches||[]).flatMap(branch=>flatten(h.navigation.visibleNavigationItems(branch.items,h.auth.canAny)))])
  assert.deepEqual(visible(baseline).map(item=>item.path).sort(),['/projects','/settings'])
  // 隐藏入口（应用数据库、主机组保护）不进侧栏，但路由与权限保留。
  const hidden=leaves.filter(item=>item.hidden)
  assert.deepEqual(hidden.map(item=>item.path).sort(),['/application-databases','/environment-policies'])
  const menuLeaves=leaves.filter(item=>!item.hidden)
  for(const code of new Set(menuLeaves.flatMap(item=>item.permissions))) {
    const h=routeHarness([code]),actual=visible(h).map(item=>item.path).sort()
    const expected=menuLeaves.filter(item=>!item.permissions.length||item.permissions.includes(code)).map(item=>item.path).sort()
    assert.deepEqual(actual,expected,code)
  }
  for(const item of hidden) {
    const h=routeHarness([...item.permissions])
    assert.ok(!visible(h).map(entry=>entry.path).includes(item.path),'隐藏入口不能出现在侧栏')
    assert.equal(await h.guard(item.path),true,'隐藏入口仍保留路由与权限')
  }
  const roleOnly=visible(routeHarness(['role.read'])).map(item=>item.path)
  assert.ok(roleOnly.includes('/access?view=roles'))
  assert.ok(!roleOnly.includes('/access?view=users'))
  assert.ok(!roleOnly.includes('/access?view=audit'))
})

test('旧入口只重定向到现有界面，主机运行资源保留原查询上下文',()=>{
  const h=routeHarness(),byPath=new Map(h.records.map(record=>[record.path,record]))
  assert.equal(byPath.get('/pipelines').redirect,'/release-plans?view=runs')
  assert.equal(byPath.get('/deployments').redirect,'/applications')
  assert.equal(byPath.get('/identity-providers').redirect,'/settings?section=identity')
  assert.deepEqual(byPath.get('/infrastructure').redirect({query:{node:'host-id',runtime:'exact-runtime'}}),{path:'/hosts',query:{node:'host-id',runtime:'exact-runtime',view:'resources'}})
})
