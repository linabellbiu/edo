import assert from 'node:assert/strict'
import {test} from 'node:test'
import {webcrypto} from 'node:crypto'
import * as vue from 'vue'
import {createRouter,createMemoryHistory} from 'vue-router'
import axios,{AxiosError,isAxiosError} from 'axios'
import {componentHarness,loadFrontend} from './acceptanceHarness.mjs'

const records=(prefix,count)=>Array.from({length:count},(_,i)=>({id:`${prefix}-${i}`,action:prefix,result:'succeeded'}))
const page=(prefix,count,next='')=>({audit_logs:records(prefix,count),has_more:Boolean(next),next_cursor:next})
const pages=new Map([['',page('first',100,'201')],['201',page('second',100,'101')],['101',page('last',43)]])
const storage=()=>{const values=new Map();return {values,getItem:key=>values.get(key)??null,setItem:(key,value)=>values.set(key,value),removeItem:key=>values.delete(key)}}
async function settle(){for(let i=0;i<5;i++){await vue.nextTick();await new Promise(resolve=>setImmediate(resolve))}}
function identity(){return vue.reactive({id:'operator-a',department_id:'department-a',project_id:'project-a',is_superuser:false,permissions:['audit.read','user.read']})}

async function navigation(t,{start='/access?view=audit',saved=storage(),actor=identity(),get,selectedProject}={}){
  const router=createRouter({history:createMemoryHistory(),routes:[{path:'/access',component:{}},{path:'/projects',component:{}}]})
  await router.push(start)
  const route=new Proxy({}, {get:(_target,key)=>router.currentRoute.value[key]})
  const mounted=[],unmounted=[]
  const profile=vue.ref(actor)
  const freshness=vue.ref('ready')
  const auth={get authorizationState(){return freshness.value},get user(){return profile.value},get permissions(){return new Set(profile.value.permissions)},canAny:codes=>freshness.value==='ready'&&(profile.value.is_superuser||!codes.length||codes.some(code=>profile.value.permissions.includes(code)))}
  const h=componentHarness('../src/views/AccessView.vue',{
    modules:{axios:{isAxiosError},'@/stores/auth':{useAuthStore:()=>auth},vue:{...vue,onMounted:callback=>mounted.push(callback),onBeforeUnmount:callback=>unmounted.push(callback)},'vue-router':{useRoute:()=>route,useRouter:()=>router}},
    globals:{window:{sessionStorage:saved,crypto:webcrypto,dispatchEvent(){},setInterval(){return 1},clearInterval(){}},...(selectedProject===undefined?{}:{localStorage:{getItem:()=>selectedProject}})},
    api:{get:async(path,options)=>{const url=new URL(path,'https://edo.example');return url.pathname==='/audit-logs'?{data:await(get?get(url,options):pages.get(url.searchParams.get('cursor')||''))}:{data:{users:[],roles:[],departments:[],permissions:[]}}}},
  })
  let stopped=false
  const stop=()=>{if(stopped)return;stopped=true;unmounted.forEach(callback=>callback());h.stop()}
  t.after(stop)
  await Promise.all(mounted.map(callback=>callback()));await settle()
  const navigate=async action=>{await new Promise((resolve,reject)=>{const remove=router.afterEach((_to,_from,failure)=>{remove();failure?reject(failure):resolve()});action()});await settle()}
  return {...h,router,route,actor,profile,freshness,saved,stop,navigate,auditRequests:()=>h.requests.filter(x=>x.path.startsWith('/audit-logs'))}
}

test('同范围授权核验清空审计正文，但在新页返回前保留表格尺寸，避免滚动归零',async t=>{
  let pending
  const h=await navigation(t,{selectedProject:'project-a',get:url=>pending?pending.promise:pages.get(url.searchParams.get('cursor')||'')})
  await h.state.nextAuditPage();await settle()
  h.state.auditTable.value={querySelector:()=>({getBoundingClientRect:()=>({width:1380,height:6842})})}
  const path=h.route.fullPath
  h.freshness.value='checking'
  assert.equal(h.state.audits.value.length,0,'核验期间不保留敏感正文')
  assert.deepEqual(h.state.auditLayout.value,{'--audit-table-width':'1380px','--audit-table-height':'6842px'})
  pending=deferred()
  h.freshness.value='ready';await settle()
  assert.deepEqual(h.state.auditLayout.value,{'--audit-table-width':'1380px','--audit-table-height':'6842px'})
  assert.equal(h.state.audits.value.length,0)
  pending.resolve(page('second',100,'101'));await settle()
  assert.equal(h.state.audits.value.length,100)
  assert.equal(h.state.auditLayout.value,undefined)
  assert.equal(h.route.fullPath,path)
  assert.equal(h.state.auditPage.value,2)
})

for(const change of ['unavailable','scope','denied-response','failed-response','unmount'])test(`审计尺寸占位在${change}后立即释放，不妨碍清场`,async t=>{
  let pending
  const h=await navigation(t,{selectedProject:'project-a',get:url=>pending?pending.promise:pages.get(url.searchParams.get('cursor')||'')})
  h.state.auditTable.value={querySelector:()=>({getBoundingClientRect:()=>({width:1380,height:6842})})}
  h.freshness.value='checking'
  assert.ok(h.state.auditLayout.value)
  if(change==='unavailable')h.freshness.value='unavailable'
  else if(change==='scope')h.profile.value={...h.profile.value,permissions:['user.read']}
  else if(change==='unmount')h.stop()
  else{
    pending=deferred();h.freshness.value='ready';await settle()
    pending.reject(change==='denied-response'?denied(403):new Error('暂不可用'));await settle()
  }
  assert.equal(h.state.auditLayout.value,undefined)
  assert.equal(h.state.audits.value.length,0)
})

test('授权checking同步清正文并取消旧响应，同权限恢复保持原审计页和位置',async t=>{
  const pending=[]
  let blocked=false
  const h=await navigation(t,{get:(url,options)=>blocked?new Promise(resolve=>pending.push({url,options,resolve})):pages.get(url.searchParams.get('cursor')||'')})
  await h.state.nextAuditPage();await settle()
  const path=h.router.currentRoute.value.fullPath,saved=[...h.saved.values.entries()]
  assert.equal(h.state.auditPage.value,2)
  blocked=true
  const old=h.state.nextAuditPage()
  h.freshness.value='checking'
  assert.equal(h.state.audits.value.length,0)
  assert.equal(pending[0].options.signal.aborted,true)
  assert.equal(h.state.auditPage.value,2)
  assert.equal(h.state.active.value,'audit')
  assert.deepEqual([...h.saved.values.entries()],saved)
  pending[0].resolve(page('stale-sensitive',10));await old
  assert.equal(h.state.audits.value.length,0)
  h.freshness.value='unavailable';await settle()
  assert.equal(h.router.currentRoute.value.fullPath,path)
  assert.equal(h.auditRequests().length,3)
  h.profile.value={...h.profile.value,permissions:[...h.profile.value.permissions].reverse()}
  h.freshness.value='ready';await settle()
  assert.equal(pending.length,2)
  assert.equal(pending[1].url.searchParams.get('cursor'),'201')
  pending[1].resolve(page('fresh-second',100,'101'));await settle()
  assert.equal(h.state.auditPage.value,2)
  assert.equal(h.state.audits.value[0].action,'fresh-second')
})

test('检查期间真正撤权清理位置与编辑层，不以旧超级管理员绕过授权状态',async t=>{
  const h=await navigation(t,{actor:vue.reactive({...identity(),is_superuser:true})})
  await h.state.nextAuditPage();await settle()
  h.state.userFormOpen.value=h.state.roleFormOpen.value=h.state.accessOpen.value=true
  h.state.removeRole({id:'role-a',display_name:'测试角色',permissions:[],in_use:false})
  assert.equal(h.confirmations.length,1)
  h.freshness.value='checking'
  assert.equal(h.state.canDelegatePermission('user.delete'),false)
  assert.equal(h.state.canManageRolePermissions({permissions:[]}),false)
  assert.equal(h.state.canManageUserAccess({id:'other',role_ids:[],permission_overrides:{allow:[]}}),false)
  assert.equal(h.state.userFormOpen.value,false)
  assert.equal(h.state.roleFormOpen.value,false)
  assert.equal(h.state.accessOpen.value,false)
  assert.equal(h.confirmations.length,0)
  assert.ok(h.saved.values.has('edo.audit.positions'),'未知状态本身不能清位置')
  h.profile.value={...h.profile.value,is_superuser:false,permissions:[]}
  assert.equal(h.saved.values.has('edo.audit.positions'),false)
  h.freshness.value='ready';await settle()
  assert.equal(h.state.active.value,'')
  assert.equal(h.state.audits.value.length,0)
  assert.equal(h.state.auditPage.value,1)
})

const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
const denied=status=>new AxiosError('访问已失效',undefined,undefined,undefined,{status,data:{},headers:{},config:{}})
function navigationStore(saved){
  const browser={history:{state:{position:1,forward:null}}}
  const store=loadFrontend('../src/stores/tabs.ts',{vue,pinia:{defineStore:(_id,factory)=>factory}},{window:browser,sessionStorage:saved,localStorage:{getItem:()=>null}}).useTabsStore()
  const {findNavigationItem}=loadFrontend('../src/router/navigation.ts')
  const visit=(route,position,back,forward=null)=>{browser.history.state={position,back,forward};const current={...route,meta:{title:findNavigationItem(route.fullPath)?.label}};store.visit(current);store.add(current)}
  return {store,browser,visit,findNavigationItem}
}

function authLifecycle(saved,initial){
  let response=structuredClone(initial),failure
  const api={__esModule:true,setAuthorizationFreshness(){},default:{get:async()=>({data:{projects:[{id:initial.project_id,is_active:true}]}}),post:async()=>{}},getCurrentUser:async()=>{if(failure)throw failure;return structuredClone(response)},login:async()=>{},loginLDAP:async()=>{},logout:async()=>{}}
  const module=loadFrontend('../src/stores/auth.ts',{vue,pinia:{defineStore:(_id,factory)=>factory},axios:{__esModule:true,default:axios},'@/api/client':api},{window:{sessionStorage:saved,dispatchEvent(){}},localStorage:{getItem:()=>initial.project_id,setItem(){},removeItem(){}}})
  const store=module.useAuthStore();store.user.value=structuredClone(initial);store.loaded.value=true;store.authorizationState.value='ready'
  return {store,respond:value=>{response=structuredClone(value);failure=undefined},fail:value=>{failure=value}}
}

test('实际审计SFC与官方Router：末页离开用户管理再正常后退保留页码、游标和按钮状态',async t=>{
  const h=await navigation(t)
  await h.state.nextAuditPage();await settle();await h.state.nextAuditPage();await settle()
  assert.equal(h.state.auditPage.value,3);assert.equal(h.state.audits.value.length,43);assert.equal(h.state.auditHasMore.value,false)
  await h.router.push('/access?view=users');await settle()
  assert.equal(h.state.audits.value.length,0);assert.deepEqual(h.state.auditCursorStack.value,[''])
  await h.navigate(()=>h.router.back())
  assert.equal(h.state.auditPage.value,3);assert.equal(h.state.audits.value.length,43)
  assert.deepEqual(h.state.auditCursorStack.value,['','201','101']);assert.equal(h.state.auditHasMore.value,false)
  await h.state.previousAuditPage();await settle()
  assert.equal(h.state.auditPage.value,2);assert.equal(h.state.audits.value[0].action,'second')
})

test('审计位置只保存游标元数据，同tab刷新正常重取原页而不保存审计行',async t=>{
  const saved=storage(),h=await navigation(t,{saved})
  await h.state.nextAuditPage();await settle();await h.state.nextAuditPage();await settle()
  const path=h.router.currentRoute.value.fullPath
  assert.equal(typeof h.router.currentRoute.value.query.audit_position,'string')
  assert.ok(saved.values.size>0);assert.ok([...saved.values.values()].every(value=>!value.includes('"action"')&&!value.includes('last-')))
  h.stop()
  const restored=await navigation(t,{saved,start:path})
  assert.equal(restored.state.auditPage.value,3);assert.equal(restored.state.audits.value.length,43)
  assert.deepEqual(restored.auditRequests().map(x=>x.path),['/audit-logs?limit=100&cursor=101'])
})

test('离开并卸载Access后退出再同用户登录，旧位置不得复活',async t=>{
  const h=await navigation(t),saved=h.saved
  await h.state.nextAuditPage();await settle()
  const path=h.router.currentRoute.value.fullPath,original=JSON.parse(JSON.stringify(h.profile.value))
  await h.router.push('/projects');await settle();h.stop()
  const auth=authLifecycle(saved,original)
  await auth.store.logout()
  assert.equal(saved.values.has('edo.audit.positions'),false)
  await auth.store.login('operator-a','only-local-unit-placeholder')
  const restored=await navigation(t,{saved,start:path,actor:vue.reactive({...auth.store.user.value})})
  assert.equal(restored.state.auditPage.value,1);assert.equal(restored.router.currentRoute.value.query.audit_position,undefined)
  assert.deepEqual(restored.auditRequests().map(x=>x.path),['/audit-logs?limit=100'])
})

test('卸载期间等价auth/me新对象及权限顺序变化不误删位置；可同tab刷新恢复',async t=>{
  const h=await navigation(t),saved=h.saved
  await h.state.nextAuditPage();await settle()
  const path=h.router.currentRoute.value.fullPath,original=JSON.parse(JSON.stringify(h.profile.value));h.stop()
  const auth=authLifecycle(saved,original)
  auth.respond({...original,permissions:[...original.permissions].reverse()})
  await auth.store.refreshPermissions()
  assert.equal(saved.values.has('edo.audit.positions'),true)
  const restored=await navigation(t,{saved,start:path})
  assert.equal(restored.state.auditPage.value,2);assert.equal(restored.state.audits.value[0].action,'second')
})

test('卸载期间已观察到撤权再恢复同权限集合，旧位置仍不得复活',async t=>{
  const h=await navigation(t),saved=h.saved
  await h.state.nextAuditPage();await settle()
  const path=h.router.currentRoute.value.fullPath,original=JSON.parse(JSON.stringify(h.profile.value));h.stop()
  const auth=authLifecycle(saved,original)
  auth.respond({...original,permissions:['user.read']});await auth.store.refreshPermissions()
  assert.equal(saved.values.has('edo.audit.positions'),false)
  auth.respond(original);await auth.store.refreshPermissions()
  const restored=await navigation(t,{saved,start:path})
  assert.equal(restored.state.auditPage.value,1)
})

test('真实Tabs历史与Router：position只replace当前步骤，分页不增加标签或虚假返回，顶部返回及前进恢复',async t=>{
  const h=await navigation(t,{start:'/access?view=audit&keep=yes#audit-table'}),nav=navigationStore(storage())
  nav.visit(h.router.currentRoute.value,1)
  await h.state.nextAuditPage();await settle()
  assert.equal(h.route.hash,'#audit-table')
  const position=h.route.query.audit_position
  nav.visit(h.router.currentRoute.value,1)
  await h.state.nextAuditPage();await settle()
  assert.equal(h.route.query.audit_position,position)
  nav.visit(h.router.currentRoute.value,1)
  const auditPath=h.route.fullPath
  assert.equal(nav.store.history.value.length,1)
  assert.equal(nav.store.tabs.value.filter(tab=>tab.key==='/access').length,1)
  assert.equal(nav.findNavigationItem(auditPath).label,'nav.audit')
  assert.equal(nav.store.tabs.value.at(-1).path,auditPath)
  await h.router.push('/access?view=users');await settle()
  nav.visit(h.router.currentRoute.value,2,auditPath)
  assert.equal(h.route.query.audit_position,undefined)
  assert.equal(nav.store.history.value.length,2)
  await h.navigate(()=>nav.store.back(h.router,h.route.fullPath))
  nav.visit(h.router.currentRoute.value,1,undefined,'/access?view=users')
  assert.equal(h.state.auditPage.value,3);assert.equal(h.state.audits.value.length,43)
  assert.equal(h.route.query.keep,'yes');assert.equal(nav.store.canGoBack(h.route.fullPath),false)
  await h.navigate(()=>h.router.forward())
  assert.equal(h.route.query.view,'users');assert.equal(h.state.audits.value.length,0)
  await h.navigate(()=>h.router.back())
  assert.equal(h.state.auditPage.value,3)
  await h.state.refresh();await settle()
  assert.equal(h.state.auditPage.value,1);assert.equal(h.route.query.audit_position,undefined);assert.equal(h.route.query.keep,'yes');assert.equal(h.route.hash,'#audit-table')
  nav.visit(h.router.currentRoute.value,1,undefined,'/access?view=users')
  assert.equal(nav.store.history.value.length,2)
})

test('用户/角色页不继承审计位置，包括深链进入与同组件切换',async t=>{
  const h=await navigation(t)
  await h.state.nextAuditPage();await settle()
  h.state.selectTab('users');await settle()
  assert.equal(h.route.query.view,'users');assert.equal(h.route.query.audit_position,undefined)
  const roleActor=identity();roleActor.permissions.push('role.read')
  const roles=await navigation(t,{start:'/access?view=roles&audit_position=0123456789abcdef01234567&keep=yes',actor:roleActor})
  assert.equal(roles.route.query.view,'roles');assert.equal(roles.route.query.keep,'yes');assert.equal(roles.route.query.audit_position,undefined)
  assert.equal(roles.auditRequests().length,0)
})

for(const field of ['id','department_id','project_id','permissions','is_superuser'])test(`审计位置绑定稳定身份范围：${field}变化立即清场，迟到成功不能重建位置`,async t=>{
  let pending
  const h=await navigation(t,{get:url=>pending&&url.searchParams.get('cursor')==='101'?pending.promise:pages.get(url.searchParams.get('cursor')||'')})
  await h.state.nextAuditPage();await settle()
  const old=h.route.fullPath;pending=deferred()
  const loading=h.state.nextAuditPage();await settle()
  if(field==='permissions')h.profile.value.permissions=['user.read']
  else if(field==='is_superuser')h.profile.value.is_superuser=true
  else h.profile.value[field]='changed'
  assert.equal(h.state.audits.value.length,0)
  assert.equal(h.saved.values.has('edo.audit.positions'),false)
  pending.resolve(page('late-old',43));await loading;await settle()
  assert.ok(h.state.audits.value.every(row=>row.action!=='late-old'))
  assert.equal(h.route.query.audit_position,undefined)
  assert.equal(h.saved.values.has('edo.audit.positions'),false)
  assert.notEqual(old,h.route.fullPath)
})

for(const status of [401,403])test(`当前审计${status}同步清除游标、位置及正文且不自动循环重试`,async t=>{
  let failure
  const h=await navigation(t,{get:url=>{if(failure)throw failure;return pages.get(url.searchParams.get('cursor')||'')}})
  await h.state.nextAuditPage();await settle()
  failure=denied(status);const before=h.auditRequests().length
  await h.state.nextAuditPage();await settle()
  assert.equal(h.auditRequests().length,before+1)
  assert.equal(h.state.audits.value.length,0);assert.deepEqual(h.state.auditCursorStack.value,[''])
  assert.equal(h.route.query.audit_position,undefined);assert.equal(h.saved.values.has('edo.audit.positions'),false)
})

test('等价profile新引用不清页，旧失败不能删除新位置，普通失败不破坏当前位置',async t=>{
  let pending,failure
  const h=await navigation(t,{get:url=>{if(failure)throw failure;return pending?pending.promise:pages.get(url.searchParams.get('cursor')||'')}})
  await h.state.nextAuditPage();await settle()
  const position=h.route.query.audit_position
  h.profile.value={...h.profile.value,permissions:[...h.profile.value.permissions].reverse()};await settle()
  assert.equal(h.state.auditPage.value,2);assert.equal(h.route.query.audit_position,position)
  pending=deferred();const oldResponse=pending,old=h.state.nextAuditPage();await settle()
  await h.router.push('/access?view=users');await settle();pending=undefined
  await h.navigate(()=>h.router.back())
  assert.equal(h.state.auditPage.value,2)
  // 已中止请求的迟到拒绝不得清掉重新鉴权获取的位置。
  failure=denied(500)
  await h.state.refresh();await settle()
  assert.equal(h.state.auditPage.value,2);assert.equal(h.route.query.audit_position,position)
  assert.equal(h.saved.values.has('edo.audit.positions'),true)
  oldResponse.reject(denied(401));await old;await settle()
  assert.equal(h.state.auditPage.value,2);assert.equal(h.route.query.audit_position,position)
  assert.equal(h.saved.values.has('edo.audit.positions'),true)
})

test('其它路由卸载后通过真实顶部返回fallback重建审计39页；深链仅在本tab有界位置中恢复',async t=>{
  const saved=storage(),h=await navigation(t,{saved,get:url=>page(url.searchParams.get('cursor')?'page39':'first',43,'1')}),nav=navigationStore(storage())
  const cursors=['',...Array.from({length:38},(_,i)=>String(1000-i))]
  await h.state.loadAuditPage(cursors);await settle()
  assert.equal(h.state.auditPage.value,39)
  const auditPath=h.route.fullPath;nav.visit(h.router.currentRoute.value,1)
  await h.router.push('/projects');await settle();h.stop()
  nav.visit(h.router.currentRoute.value,2)
  await nav.store.back(h.router,h.route.fullPath)
  assert.equal(h.route.fullPath,auditPath)
  const returned=await navigation(t,{saved,start:h.route.fullPath,get:url=>page(url.searchParams.get('cursor')?'page39':'first',43)})
  assert.equal(returned.state.auditPage.value,39);assert.equal(returned.state.audits.value.length,43)
  assert.equal(returned.auditRequests().at(-1).path,'/audit-logs?limit=100&cursor=963')
  const foreignTab=await navigation(t,{start:auditPath})
  assert.equal(foreignTab.state.auditPage.value,1)
  assert.equal(foreignTab.route.query.audit_position,undefined)
})

test('禁用或抛异常的会话存储不崩页，同组件RAM返回保留，重新挂载安全回首页',async t=>{
  const saved={getItem(){throw Error('disabled')},setItem(){throw Error('quota')},removeItem(){throw Error('disabled')}}
  const h=await navigation(t,{saved})
  await h.state.nextAuditPage();await settle()
  const path=h.route.fullPath
  await h.router.push('/access?view=users');await settle();await h.navigate(()=>h.router.back())
  assert.equal(h.state.auditPage.value,2)
  h.stop()
  const reloaded=await navigation(t,{saved,start:path})
  assert.equal(reloaded.state.auditPage.value,1);assert.equal(reloaded.route.query.audit_position,undefined)
})

test('19位游标保持原字符串；缓存边界40条、1000游标、256KiB不阻止正常API翻页',async t=>{
  const h=await navigation(t,{get:url=>page('data',1,'9223372036854774807')})
  const stack=['',...Array.from({length:999},(_,i)=>(9223372036854775807n-BigInt(i)).toString())]
  assert.equal(h.state.validAuditCursors(stack),true)
  await h.state.loadAuditPage(stack);await settle()
  const cached=JSON.parse(h.saved.values.get('edo.audit.positions'))
  assert.equal(h.state.auditPage.value,1000);assert.deepEqual(cached.positions[0].cursors,stack)
  assert.ok(h.auditRequests().at(-1).path.endsWith('cursor=9223372036854774809'))
  await h.state.nextAuditPage();await settle()
  assert.equal(h.state.auditPage.value,1001);assert.equal(h.state.audits.value.length,1)
  assert.equal(h.route.query.audit_position,undefined)
  for(const invalid of [['','01'],['','0'],['','-1'],['','9223372036854775808'],['','10000000000000000000'],['','9','9'],['','8','9'],['','uuid'],[0,'9'],['',9]])assert.equal(h.state.validAuditCursors(invalid),false)
  // 直接调用实际SFC的缓存函数覆盖容量边界，不冒充真实接口或浏览器验收。
  for(let i=0;i<45;i++){
    await h.router.replace('/access?view=audit');await settle()
    h.state.saveAuditPosition(['',String(10_000+i)]);await settle()
  }
  let raw=h.saved.values.get('edo.audit.positions')
  assert.equal(JSON.parse(raw).positions.length,40)
  for(let i=0;i<15;i++){
    await h.router.replace('/access?view=audit');await settle()
    h.state.saveAuditPosition(['',...Array.from({length:999},(_,j)=>(9223372036854775807n-BigInt(i*2000+j)).toString())]);await settle()
  }
  raw=h.saved.values.get('edo.audit.positions')
  assert.ok(new TextEncoder().encode(raw).byteLength<=256*1024)
  assert.ok(JSON.parse(raw).positions.length<40)
})

test('损坏、过期、外来scope、未知或非法位置只重取首页，绝不携带外来游标',async t=>{
  const id='0123456789abcdef01234567',scope=JSON.stringify(['operator-a','department-a','project-a',false,['audit.read','user.read']])
  const valid={scope,positions:[{position:id,cursors:['','201'],savedAt:Date.now()}]}
  const cases=[
    {raw:'not-json'},
    {raw:JSON.stringify({...valid,scope:'foreign'})},
    {raw:JSON.stringify({...valid,positions:[{...valid.positions[0],savedAt:Date.now()-30*60_000}]})},
    {raw:JSON.stringify({...valid,positions:[{...valid.positions[0],cursors:['','9223372036854775808']}]})},
    {raw:JSON.stringify({...valid,rows:[{action:'must-not-restore'}]})},
    {raw:JSON.stringify({...valid,positions:Array.from({length:41},(_,i)=>({...valid.positions[0],position:i.toString(16).padStart(24,'0')}))})},
    {raw:JSON.stringify(valid),query:'audit_position=ffffffffffffffffffffffff'},
    {raw:JSON.stringify(valid),query:'audit_position=invalid'},
    {raw:JSON.stringify(valid),query:`audit_position=${id}&audit_position=${id}`},
    {raw:'x'.repeat(256*1024+1)},
  ]
  for(const item of cases){
    const saved=storage();saved.setItem('edo.audit.positions',item.raw)
    const h=await navigation(t,{saved,start:`/access?view=audit&keep=yes&${item.query||`audit_position=${id}`}`})
    assert.equal(h.state.auditPage.value,1);assert.equal(h.route.query.audit_position,undefined);assert.equal(h.route.query.keep,'yes')
    assert.deepEqual(h.auditRequests().map(request=>request.path),['/audit-logs?limit=100'])
    if(!item.query)assert.equal(saved.values.has('edo.audit.positions'),false)
    h.stop()
  }
})

for(const status of [401,403])test(`卸载期间auth/me ${status}也清除本功能缓存`,async t=>{
  const h=await navigation(t);await h.state.nextAuditPage();await settle()
  const original=JSON.parse(JSON.stringify(h.profile.value));h.stop()
  const auth=authLifecycle(h.saved,original);auth.fail(denied(status))
  await auth.store.refreshPermissions().catch(()=>{})
  assert.equal(h.saved.values.has('edo.audit.positions'),false)
})
