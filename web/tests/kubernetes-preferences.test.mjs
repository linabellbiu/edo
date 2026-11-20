import assert from 'node:assert/strict'
import { test } from 'node:test'
import * as vue from 'vue'
import axios from 'axios'
import { createRouter, createMemoryHistory } from 'vue-router'
import { loadFrontend } from './acceptanceHarness.mjs'

const api = loadFrontend('../src/api/kubernetes.ts',{'./resources':{apiErrorMessage:error=>error?.response?.data?.message??error.message}})
const presentation = loadFrontend('../src/utils/kubernetesPresentation.ts', {
  '@/api/kubernetes': api, '@/utils/kubernetesRoles': loadFrontend('../src/utils/kubernetesRoles.ts'),
})
const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const catalog = [
  { id: 'a', name: '集群 A', default_namespace: 'default', is_active: true },
  { id: 'b', name: '集群 B', default_namespace: 'system', is_active: true },
]
function storage() {
  const values = new Map()
  return { values, getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value) }
}
async function harness({ saved = storage(), path = '/kubernetes?view=workloads', user = 'reader', project = 'project-a', clusters = catalog, namespaceRead, listRead, httpRead } = {}) {
  saved.setItem('edo.project', project)
  const helpers = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: saved })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/kubernetes', component: {} }] })
  await router.push(path)
  const route = new Proxy({}, { get: (_, key) => router.currentRoute.value[key] })
  const auth = vue.reactive({ user: { id: user, project_id: project }, canAny: () => true })
  const mounts = [], unmounts = [], listeners = new Map(), reads = [], listReads = [], writes=[], confirmations=[],sessions=[],notifications=[],scope = vue.effectScope()
  const component = loadFrontend('../src/views/KubernetesView.vue', {
    axios,
    'ant-design-vue':{Modal:{confirm:options=>confirmations.push(options)},message:Object.fromEntries(['success','warning','error'].map(key=>[key,value=>notifications.push({key,value})]))},
    vue: { ...vue, onMounted: fn => mounts.push(fn), onBeforeUnmount: fn => unmounts.push(fn) },
    'vue-router': { useRouter: () => router, useRoute: () => route, onBeforeRouteLeave() {}, onBeforeRouteUpdate() {} },
    'vue-i18n': { useI18n: () => ({ t: key => key }) },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/utils/kubernetesWorkspace': helpers,
    '@/utils/kubernetesPresentation': presentation,
    '@/api/kubernetes': { ...api,
      listKubeResourceScopes: async (cluster, type, selection,cursor,signal) => { listReads.push({ cluster, resource: type.resource, selection:[...selection],cursor,signal }); return listRead?.(cluster,type,selection,cursor,signal)??{ items: [], cursor: {} } },
      withFreshKubeResource:async(cluster,type,object,_conflict,mutate)=>{writes.push({fresh:true,cluster,type,object});return mutate(object)},
      streamKubeEvents: (cluster,namespaces,project,signal,onEvent) => {sessions.push({cluster,namespaces,project,signal,onEvent});return new Promise(() => {})},
    },
    '@/api/client': { __esModule: true, default: { get: async (path, options) => {
      reads.push({ path, options })
      const custom=httpRead?.(path,options);if(custom!==undefined)return custom
      if (path === '/kubernetes/clusters') return { data: { clusters: structuredClone(clusters) } }
      if (path.endsWith('/namespaces')) return namespaceRead?.(path, options) ?? { data: { namespaces: ['default', 'prod', 'test', 'system'] } }
      return { data: { counts: {}, usage: {}, nodes: [], phases: {} } }
    },...Object.fromEntries(['post','delete'].map(method=>[method,async(...args)=>{writes.push({method,args});return {data:{}}}]))} },
  }, {
    localStorage: saved, document: { hidden: false, addEventListener() {}, removeEventListener() {} },
    window: { addEventListener: (event, fn) => listeners.set(event, fn), removeEventListener: event => listeners.delete(event) },
    setInterval: () => 1, clearInterval() {}, setTimeout: () => 2, clearTimeout() {},
  }).default
  const state = scope.run(() => component.setup({}, { expose() {} }))
  mounts.forEach(fn => fn()); await settle()
  return { state, saved, helpers, router, route, auth, listeners, reads, listReads, writes, confirmations,sessions,notifications,stop() { unmounts.forEach(fn => fn()); scope.stop() } }
}

test('重新挂载恢复多集群、独立范围及浏览偏好，全部取消也可恢复', async t => {
  const first = await harness(); t.after(first.stop)
  first.state.selectNamespaces(['prod', 'test'], 'a'); await settle()
  first.state.selectNamespaces(['system'], 'b'); await settle()
  first.state.autoRefresh.value=false;first.state.pageSize.value=50;await settle()
  const second=await harness({saved:first.saved});t.after(second.stop)
  assert.deepEqual(second.state.selectedClusters.value,['a','b'])
  assert.deepEqual(second.state.scopeSelections.value,{a:['prod','test'],b:['system']})
  assert.equal(second.state.autoRefresh.value,false);assert.equal(second.state.pageSize.value,50)
  assert.ok(second.listReads.some(read=>read.cluster==='a'&&read.selection.join(',')==='prod,test'))
  assert.ok(second.listReads.some(read=>read.cluster==='b'&&read.selection.join(',')==='system'))
  second.state.selectCluster('a',false);second.state.selectCluster('b',false);await settle()
  const empty=await harness({saved:second.saved});t.after(empty.stop)
  assert.deepEqual(empty.state.selectedClusters.value,[]);assert.equal(empty.listReads.length,0)
  assert.deepEqual(empty.state.scopeSelections.value.a,['prod','test'])
})

test('URL明确指定范围与每页条数时覆盖本地，前进后退恢复各自历史范围', async t => {
  const saved = storage(), helpers = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: saved })
  helpers.writeKubeWorkspacePreferences('reader', 'project-a', { cluster: 'b', scopes: { a: ['test'], b: [] }, pageSize: 50 })
  const h = await harness({ saved, path: '/kubernetes?view=workloads&cluster=a&namespace=prod&limit=20' }); t.after(h.stop)
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod'])
  assert.equal(h.state.pageSize.value, 20)
  await h.router.push('/kubernetes?view=network&cluster=b&namespace=*&limit=10'); await settle()
  assert.deepEqual(h.state.selectedNamespaces.value, ['*'])
  assert.equal(h.state.pageSize.value, 10)
  h.router.back(); await settle()
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod'])
  assert.equal(h.state.pageSize.value, 20)
  h.router.forward(); await settle()
  assert.equal(h.state.cluster.value, 'b')
  assert.deepEqual(h.state.selectedNamespaces.value, ['*'])
})

test('集群级页面沿用保存的命名空间范围，返回资源列表不覆盖为全选', async t => {
  const saved = storage(), helpers = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: saved })
  helpers.writeKubeWorkspacePreferences('reader', 'project-a', { cluster: 'b', scopes: { b: ['prod', 'test'] } })
  const h = await harness({ saved, path: '/kubernetes?view=nodes' }); t.after(h.stop)
  assert.equal(h.state.cluster.value, 'b')
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod', 'test'])
  assert.equal(h.reads.some(item => item.path.endsWith('/namespaces')), false)
  await h.router.push({ query: { ...h.route.query, view: 'workloads' } }); await settle()
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod', 'test'])
  assert.deepEqual(h.listReads.at(-1).selection, ['prod', 'test'])
})

test('展开其他集群只读目录，不更改URL、本地勾选、资源列表或当前集群；点击框选后才加入多选范围', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.selectNamespaces(['prod', 'test']); await settle()
  const beforeURL = h.route.fullPath, beforeSaved = h.helpers.readKubeWorkspacePreferences('reader', 'project-a')
  const beforeReads = h.listReads.length
  await h.state.browseNamespaces('b'); await settle()
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod', 'test'])
  assert.deepEqual(h.state.namespaceCatalogs.value.b.names, ['default', 'prod', 'test', 'system'])
  assert.equal(h.route.fullPath, beforeURL)
  assert.equal(h.listReads.length, beforeReads)
  assert.deepEqual(h.helpers.readKubeWorkspacePreferences('reader', 'project-a'), beforeSaved)
  h.state.selectNamespaces(['system'], 'b'); await settle()
  assert.deepEqual(h.state.selectedClusters.value,['a','b'])
  assert.deepEqual(h.state.scopeSelections.value.b,['system'])
  assert.deepEqual(h.state.scopeSelections.value.a,['prod','test'])
  assert.deepEqual(h.helpers.readKubeWorkspacePreferences('reader', 'project-a').scopes.a, ['prod', 'test'])
})

test('目录收起或权限变化后丢弃迟到的浏览结果，不污染已选范围', async t => {
  const pending = []
  const h = await harness({ namespaceRead: (path, options) => path.includes('/b/') ? new Promise(resolve => pending.push({ options, resolve })) : undefined }); t.after(h.stop)
  const first = h.state.browseNamespaces('b')
  assert.equal(pending[0].options.signal.aborted, false)
  await h.state.browseNamespaces('')
  assert.equal(pending[0].options.signal.aborted, true)
  pending[0].resolve({ data: { namespaces: ['late'] } }); await first
  assert.deepEqual(h.state.namespaceCatalogs.value.b.names, [])
  const second = h.state.browseNamespaces('b')
  h.auth.user.permissions = ['cluster.read']; await settle()
  assert.equal(pending[1].options.signal.aborted, true)
  pending[1].resolve({ data: { namespaces: ['private'] } }); await second
  assert.equal(h.state.namespaceCatalogs.value.b,undefined)
  assert.doesNotMatch(JSON.stringify(h.state.namespaceCatalogs.value),/private|late/)
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['default'])
})

test('用户与项目隔离；同页切换身份不把旧范围写入新身份或继续复用旧目录', async t => {
  const h = await harness(); t.after(h.stop)
  h.state.selectCluster('b', true); h.state.autoRefresh.value = false; await settle()
  h.helpers.writeKubeWorkspacePreferences('other', 'project-b', { cluster: 'a', scopes: { a: ['test'] }, autoRefresh: true })
  h.saved.setItem('edo.project', 'project-b')
  h.auth.user = { id: 'other', project_id: 'project-b' }; await settle()
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['test'])
  assert.equal(h.state.autoRefresh.value, true)
  const original = h.helpers.readKubeWorkspacePreferences('reader', 'project-a')
  assert.deepEqual(original.clusters,['a','b'])
  assert.deepEqual(original.scopes.b, ['*'])
  assert.equal(original.autoRefresh, false)
  assert.deepEqual(h.helpers.readKubeWorkspacePreferences('new-reader', 'project-b'), {})
})

test('删除、停用或不在当前可见目录的保存集群不自动成为执行范围', async t => {
  const saved = storage(), helpers = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: saved })
  helpers.writeKubeWorkspacePreferences('reader', 'project-a', { cluster: 'b', scopes: { b: ['prod'] } })
  const h = await harness({ saved, clusters: [catalog[0], { ...catalog[1], is_active: false }] }); t.after(h.stop)
  assert.equal(h.state.cluster.value, 'a')
  assert.deepEqual(h.state.selectedNamespaces.value, ['default'])
  assert.ok(h.listReads.every(read => read.cluster === 'a'))
})

test('浏览器存储异常或损坏不阻止页面选择；仅保存允许的偏好字段', async t => {
  const saved = storage(), helpers = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: saved })
  const key = helpers.kubeWorkspacePreferenceKey('reader', 'project-a')
  saved.setItem(key, '{broken')
  assert.deepEqual(helpers.readKubeWorkspacePreferences('reader', 'project-a'), {})
  saved.setItem(key, JSON.stringify({ cluster: 'a', scopes: { a: [], b: ['*', 'prod'], c: ['bad/ns'], d: 'prod' }, autoRefresh: 'false', pageSize: 99, token: 'secret' }))
  assert.deepEqual(helpers.readKubeWorkspacePreferences('reader', 'project-a'), { cluster: 'a', scopes: { a: [], b: ['*'] } })
  saved.setItem = () => { throw new Error('QuotaExceededError') }
  const h = await harness({ saved: { ...saved, setItem(key, value) { if (key === 'edo.project') saved.values.set(key, value); else saved.setItem(key, value) } } }); t.after(h.stop)
  h.state.selectNamespaces(['prod']); await settle()
  assert.deepEqual(h.state.selectedNamespaces.value, ['prod'])
  const denied = loadFrontend('../src/utils/kubernetesWorkspace.ts', {}, { localStorage: { getItem() { throw new Error('SecurityError') }, setItem() { throw new Error('SecurityError') } } })
  assert.deepEqual(denied.readKubeWorkspacePreferences('reader', 'project-a'), {})
  assert.doesNotThrow(() => denied.writeKubeWorkspacePreferences('reader', 'project-a', { scopes: { a: [] } }))
})

const multiSaved=()=>{const saved=storage(),helpers=loadFrontend('../src/utils/kubernetesWorkspace.ts',{}, {localStorage:saved});helpers.writeKubeWorkspacePreferences('reader','project-a',{clusters:['a','b'],scopes:{a:['prod'],b:['test']}});return saved}
const publicObject=(kind='Deployment',uid='same-uid',namespace='prod')=>({apiVersion:kind==='Deployment'?'apps/v1':'v1',kind,metadata:{uid,name:'shared-web',namespace,resourceVersion:'1'},spec:kind==='Pod'?{containers:[{name:'app',image:'public:test'}]}:{replicas:1,template:{spec:{containers:[{name:'app',image:'public:test'}]}}},status:{phase:'Running',containerStatuses:[{name:'app',state:{running:{}},ready:true}]}})

test('多集群 URL 精确保存各自范围，显式链接与前进后退覆盖本地，空范围不扩大',async t=>{
  const h=await harness({saved:multiSaved()});t.after(h.stop)
  const encoded=h.helpers.kubeScopeQuery({clusters:['a','b'],scopes:{a:['prod'],b:['test']}})
  assert.deepEqual(h.helpers.readKubeScopeQuery(h.route.query),{clusters:['a','b'],scopes:{a:['prod'],b:['test']}})
  await h.router.push({query:{view:'workloads',cluster:'b',namespace:'system'}});await settle()
  assert.deepEqual(h.state.selectedClusters.value,['b']);assert.deepEqual(h.state.scopeSelections.value.b,['system'])
  h.router.back();await settle()
  assert.deepEqual(h.state.selectedClusters.value,['a','b']);assert.deepEqual(h.state.scopeSelections.value.b,['test'])
  h.router.forward();await settle();assert.deepEqual(h.state.selectedClusters.value,['b'])
  const reopened=await harness({saved:h.saved,path:'/kubernetes?view=workloads&'+new URLSearchParams(Object.entries(encoded).filter(([,value])=>value!==undefined).flatMap(([key,value])=>(Array.isArray(value)?value:[value]).map(item=>[key,item])))});t.after(reopened.stop)
  assert.deepEqual(reopened.state.selectedClusters.value,['a','b']);assert.deepEqual(reopened.state.scopeSelections.value.a,['prod'])
  assert.deepEqual(h.helpers.readKubeScopeQuery({cluster:[''],scope:'["__proto__",["*"]]'}),{clusters:[],scopes:{}})
})

test('多个集群同名同 UID 的资源独立保留，详情、编辑、日志及确认操作固定来源',async t=>{
  const h=await harness({saved:multiSaved(),listRead:async()=>({items:[publicObject()],cursor:{}}),httpRead:(path)=>path.endsWith('/shared-web')?{data:{object:publicObject()}}:undefined});t.after(h.stop)
  assert.equal(h.state.rows.value.length,2)
  assert.equal(new Set(h.state.presented.value.map(row=>row.id)).size,2)
  assert.ok(h.state.columns.value.some(column=>column.key==='cluster'))
  const row=h.state.rows.value.find(object=>h.helpers.kubeObjectCluster(object)==='b'),type=api.kubeTypes.find(type=>type.resource==='deployments')
  await h.state.openDetails(row,'basic',type,false,false);await settle()
  assert.equal(h.state.detailCluster.value,'b')
  assert.ok(h.reads.some(read=>read.path==='/kubernetes/clusters/b/resources/deployments/shared-web'))
  h.state.editYaml();assert.equal(h.state.yamlContext.value.cluster,'b');h.state.yamlOpen.value=false
  await h.state.edit(row,type);assert.equal(h.state.editorContext.value.cluster,'b');h.state.editorOpen.value=false
  const pod=h.helpers.withKubeCluster(publicObject('Pod'),'b')
  assert.match(h.state.podStreamPath(pod),/clusters\/b\/namespaces\/prod\/pods\/shared-web/)
  assert.match(h.state.podStreamPath(pod,true),/kubernetes\/b\/namespaces\/prod/)
  const service=h.helpers.withKubeCluster({...publicObject('Service'),kind:'Service'},'b')
  assert.equal(h.state.servicePortSource(service).cluster,'b')
  h.state.showScale(row,type);assert.equal(h.state.scaleTarget.value.cluster,'b');h.state.scaleValue.value=2;await h.state.scale()
  assert.ok(h.writes.some(write=>write.method==='post'&&write.args[0]==='/kubernetes/clusters/b/resources/deployments/shared-web/actions'))
  h.state.remove(row,type);await h.confirmations.at(-1).onOk()
  assert.ok(h.writes.some(write=>write.method==='delete'&&write.args[0]==='/kubernetes/clusters/b/resources/deployments/shared-web'))
  assert.ok(h.writes.filter(write=>write.fresh).every(write=>write.cluster==='b'))
  assert.deepEqual(JSON.parse(JSON.stringify(row)),publicObject())
  assert.deepEqual(h.state.selectedClusters.value,['a','b'])
})

test('慢集群不遮住已返回资源，取消选择立即中止并丢弃迟到数据',async t=>{
  let release,pendingSignal
  const h=await harness({saved:multiSaved(),listRead:(id,_type,_names,_cursor,signal)=>id==='b'?new Promise(resolve=>{release=resolve;pendingSignal=signal}):{items:[publicObject()],cursor:{}}});t.after(h.stop)
  assert.equal(h.state.rows.value.length,1);assert.equal(h.state.loading.value,false)
  assert.deepEqual(h.state.listLoadingClusters.value,['b'])
  h.state.selectCluster('b',false);await settle()
  assert.equal(pendingSignal.aborted,true)
  release({items:[publicObject('Deployment','late-private')],cursor:{}});await settle()
  assert.equal(h.state.rows.value.length,1);assert.equal(h.helpers.kubeObjectCluster(h.state.rows.value[0]),'a')
  assert.equal(h.state.error.value,'')
})

test('失败集群独立提示并保留成功数据，恢复后重新加入；撤权清除该来源旧数据',async t=>{
  let failure=new Error('读取失败')
  const h=await harness({saved:multiSaved(),listRead:async id=>{if(id==='b'&&failure)throw failure;return {items:[publicObject()],cursor:{}}}});t.after(h.stop)
  assert.equal(h.state.rows.value.length,1);assert.match(h.state.error.value,/集群 B：读取失败/)
  failure=undefined;await h.state.refresh();assert.equal(h.state.rows.value.length,2)
  failure=new axios.AxiosError('Forbidden',undefined,undefined,undefined,{status:403,data:{message:'权限已撤销'}})
  await h.state.refresh()
  assert.equal(h.state.rows.value.length,1)
  assert.ok(h.state.rows.value.every(object=>h.helpers.kubeObjectCluster(object)==='a'))
  assert.match(h.state.error.value,/权限已撤销/)
})

test('加载更多保留各集群独立游标，已读完来源不重复请求，刷新恢复已加载窗口',async t=>{
  const h=await harness({saved:multiSaved(),listRead:async(id,_type,_names,cursor)=>({items:Array.from({length:cursor?1:10},(_,index)=>publicObject('Deployment',id+(cursor?'next':'first')+index)),cursor:cursor||id==='a'?{}:{test:'b-next'}})});t.after(h.stop)
  assert.equal(h.state.rows.value.length,20);assert.equal(h.state.hasContinuation.value,true)
  const before=h.listReads.length;await h.state.refresh(false,true)
  assert.equal(h.listReads.length,before+1);assert.equal(h.listReads.at(-1).cluster,'b');assert.deepEqual(h.listReads.at(-1).cursor,{test:'b-next'})
  assert.equal(h.state.rows.value.length,21);assert.equal(h.state.hasContinuation.value,false)
  await h.state.refresh();assert.equal(h.state.rows.value.length,21)
})

test('跨集群统计累加集群级数量且隔离同名节点指标，缺失来源保留 partial',async()=>{
  const helpers=loadFrontend('../src/utils/kubernetesWorkspace.ts'),snapshot=(cpu,count)=>({counts:{nodes:{count:1,state:'ok'},pods:{count,state:'ok'}},nodes:[{name:'same-node',uid:'same-node-uid',cpu:4,memory:1024,ready:true}],phases:{Running:count},usage:{nodes:{'same-node':{cpu,memory:100,timestamp:new Date().toISOString()}}},metrics_state:{nodes:'ok'},collected_at:'2026-11-20T01:00:00Z'})
  const entries=[{cluster:'a',data:snapshot(1,2)},{cluster:'b',data:snapshot(3,5)}],merged=helpers.mergeClusterOverviews(entries,2)
  assert.equal(merged.counts.nodes.count,2);assert.equal(merged.counts.pods.count,7);assert.equal(merged.phases.Running,7)
  assert.equal(new Set(merged.nodes.map(node=>node.name)).size,2)
  assert.deepEqual(merged.nodes.map(node=>merged.usage.nodes[node.name].cpu),[1,3])
  const partial=helpers.mergeClusterOverviews(entries.slice(0,1),2)
  assert.equal(partial.counts.nodes.state,'partial');assert.equal(partial.nodes,null);assert.equal(partial.phases,null)
})
