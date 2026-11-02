import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {createMemoryHistory, createRouter} from 'vue-router'
import {componentHarness, loadFrontend} from './acceptanceHarness.mjs'

const navigation = loadFrontend('../src/router/navigation.ts')
const cluster = {id:'rbac-cluster',name:'授权测试集群',api_server:'https://cluster.invalid',default_namespace:'default',is_active:true,hosts:[]}
const clusterPath = '/hosts?view=clusters'
const permitted = codes => required => !required.length || required.some(code => codes.includes(code))
const leaves = items => items.flatMap(item => item.children ? leaves(item.children) : [item])

function routerFor(codes) {
  const withoutPages = record => ({...record,component:{},children:record.children?.map(withoutPages)})
  return loadFrontend('../src/router/index.ts', {
    'vue-router':{createWebHistory:createMemoryHistory,createRouter:options=>createRouter({...options,routes:options.routes.map(withoutPages)})},
    '@/stores/auth':{useAuthStore:()=>({user:{id:'reader',project_id:'project-a'},ensureLoaded:async()=>{},canAny:permitted(codes)})},
    '@/locales':{__esModule:true,default:{global:{t:key=>key}}},
    './navigation':navigation,
  },{document:{title:''}}).default
}

function hostView(codes) {
  const calls = []
  const get = async path => {
    calls.push(path)
    if (path === '/kubernetes/clusters') return {data:{clusters:[cluster]}}
    if (path === '/hosts') return {data:{hosts:[]}}
    if (path === '/environments') return {data:{environments:[]}}
    assert.fail(`不应请求额外资源：${path}`)
  }
  const infrastructure = loadFrontend('../src/api/infrastructure.ts', {'@/api/client':{__esModule:true,default:{get}}})
  const list = loadFrontend('../src/utils/resourceList.ts', {vue,'vue-i18n':{useI18n:()=>({t:key=>key})}})
  const ctx = componentHarness('../src/views/HostView.vue', {
    codes,query:{view:'clusters'},api:{get},globals:{document:{hidden:false},localStorage:{getItem:()=> 'project-a'}},
    modules:{'@/api/infrastructure':infrastructure,'@/utils/resourceList':list,
      '@/utils/infrastructureRelations':loadFrontend('../src/utils/infrastructureRelations.ts'),'@/utils/time':loadFrontend('../src/utils/time.ts')},
  })
  return {...ctx,calls}
}

function inventory(codes) {
  return componentHarness('../src/components/ClusterInventory.vue', {
    codes,query:{view:'clusters'},props:vue.reactive({clusters:[cluster],hosts:[],loading:false}),
    modules:{'@/utils/resourceList':loadFrontend('../src/utils/resourceList.ts',{vue,'vue-i18n':{useI18n:()=>({t:key=>key})}}),
      '@/utils/infrastructureRelations':loadFrontend('../src/utils/infrastructureRelations.ts'),'@/utils/time':loadFrontend('../src/utils/time.ts')},
  })
}

test('独立RBAC查看和管理权限均显示集群目录菜单', async t => {
  for (const code of ['kubernetes.rbac.read','kubernetes.rbac.update']) await t.test(code, () => {
    const visible = navigation.navigation.flatMap(section => [
      ...navigation.visibleNavigationItems(section.items??[],permitted([code])),
      ...(section.branches??[]).flatMap(branch=>navigation.visibleNavigationItems(branch.items,permitted([code]))),
    ])
    assert.ok(leaves(visible).some(item=>item.path===clusterPath))
    assert.equal(leaves(visible).some(item=>item.path.startsWith('/kubernetes?')),false)
  })
})

test('独立RBAC权限通过真实统一路由守卫且不能进入资源分类', async t => {
  for (const code of ['kubernetes.rbac.read','kubernetes.rbac.update']) await t.test(code, async () => {
    const router = routerFor([code])
    await router.push(clusterPath)
    assert.equal(router.currentRoute.value.fullPath,clusterPath)
    assert.equal(router.currentRoute.value.meta.title,'k8s.clusters')
    await router.push('/kubernetes?view=workloads')
    assert.equal(router.currentRoute.value.path,'/projects')
  })
})

test('RBAC目录加载只取集群元数据并打开准确授权对象，不探测主机或资源', async t => {
  for (const code of ['kubernetes.rbac.read','kubernetes.rbac.update']) await t.test(code, async t => {
    const page = hostView([code]); t.after(page.stop)
    await page.state.refresh()
    assert.deepEqual(page.calls,['/kubernetes/clusters'])
    assert.deepEqual(page.state.clusters.value.map(item=>item.id),[cluster.id])
    assert.equal(page.state.canReadHosts.value,false)
    assert.equal(page.state.canReadResources.value,false)
    assert.equal(page.state.canManageKubernetes.value,false)
    await page.state.refreshVisibleStatuses()
    assert.deepEqual(page.calls,['/kubernetes/clusters'])
    const rows = inventory([code]); t.after(rows.stop)
    assert.equal(rows.state.canManageResources.value,false)
    rows.state.openRbac(page.state.clusters.value[0])
    assert.equal(rows.state.rbacOpen.value,true)
    assert.equal(rows.state.rbacCluster.value,cluster.id)
    assert.equal(rows.requests.length,0)
  })
})

test('已有主机或部署查看权限不会获得RBAC授权入口', async t => {
  for (const code of ['cluster.read','deployment.read']) await t.test(code, async t => {
    const rows = inventory([code]); t.after(rows.stop)
    rows.state.openRbac(cluster)
    assert.equal(rows.state.rbacOpen.value,false)
    assert.equal(rows.state.rbacCluster.value,'')
    assert.equal(rows.requests.length,0)
    const router = routerFor([code])
    await router.push(clusterPath)
    assert.equal(router.currentRoute.value.fullPath,code==='cluster.read'?clusterPath:'/projects')
  })
})
