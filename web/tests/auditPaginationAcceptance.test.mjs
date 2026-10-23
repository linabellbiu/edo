import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {parse} from '@vue/compiler-sfc'
import * as vue from 'vue'
import {AxiosError,isAxiosError} from 'axios'
import {componentHarness} from './acceptanceHarness.mjs'

const records=(prefix,count)=>Array.from({length:count},(_,i)=>({id:`${prefix}-${i}`,action:prefix,resource_type:'test',result:'succeeded'}))
const page=(prefix,count,next='')=>({audit_logs:records(prefix,count),has_more:Boolean(next),next_cursor:next})
const settle=()=>new Promise(resolve=>setImmediate(resolve))

function audit(t,{codes=['audit.read'],get}={}) {
  const identity=vue.reactive({id:'operator-a',department_id:'department-a',project_id:'project-a',is_superuser:false,permissions:[...codes]})
  const auth={user:identity,get permissions(){return new Set(identity.permissions)},canAny:required=>identity.is_superuser||!required.length||required.some(code=>identity.permissions.includes(code))}
  const unmount=[]
  const h=componentHarness('../src/views/AccessView.vue',{
    query:{view:'audit'},
    modules:{axios:{isAxiosError},'@/stores/auth':{useAuthStore:()=>auth},vue:{...vue,onMounted(){},onBeforeUnmount:callback=>unmount.push(callback)}},
    api:{get:async(path,options)=>{
      const url=new URL(path,'https://edo.example')
      if(url.pathname==='/audit-logs')return {data:await get(url,options)}
      return {data:{users:[],departments:[],roles:[],permissions:[]}}
    }},
  })
  t.after(h.stop)
  return {...h,identity,unmount,auditRequests:()=>h.requests.filter(item=>item.path.startsWith('/audit-logs'))}
}

function pendingAudit(t,options={}) {
  const pending=[]
  const h=audit(t,{...options,get:(url,request)=>new Promise((resolve,reject)=>pending.push({url,request,resolve,reject}))})
  return {...h,pending}
}

test('审计首页只拉固定100条，不自动遍历历史，并保留服务端字符串游标',async t=>{
  const cursor='9223372036854775806'
  const h=audit(t,{get:async()=>page('first',100,cursor)})
  await h.state.refresh()
  assert.deepEqual(h.auditRequests().map(item=>item.path),['/audit-logs?limit=100'])
  assert.equal(h.state.audits.value.length,100)
  assert.deepEqual(h.state.auditCursorStack.value,[''])
  assert.equal(h.state.auditPage.value,1)
  assert.equal(h.state.auditNextCursor.value,cursor)
  assert.equal(h.state.auditHasMore.value,true)
})

test('超过100条审计可逐页前后读取，只保留当前页且末页停止请求',async t=>{
  const pages=new Map([['',page('first',100,'201')],['201',page('second',100,'101')],['101',page('last',51)]])
  const h=audit(t,{get:async url=>pages.get(url.searchParams.get('cursor')||'')})
  await h.state.refresh()
  await h.state.previousAuditPage();assert.equal(h.auditRequests().length,1)
  await h.state.nextAuditPage()
  assert.equal(h.state.auditPage.value,2);assert.equal(h.state.audits.value.length,100)
  assert.equal(h.state.audits.value[0].action,'second')
  await h.state.nextAuditPage()
  assert.equal(h.state.auditPage.value,3);assert.equal(h.state.audits.value.length,51)
  assert.equal(h.state.auditHasMore.value,false)
  await h.state.nextAuditPage();assert.equal(h.auditRequests().length,3)
  await h.state.previousAuditPage()
  assert.equal(h.state.auditPage.value,2);assert.equal(h.state.audits.value[0].action,'second')
  await h.state.previousAuditPage()
  assert.equal(h.state.auditPage.value,1);assert.equal(h.state.audits.value[0].action,'first')
  assert.deepEqual(h.auditRequests().map(item=>item.path),['/audit-logs?limit=100','/audit-logs?limit=100&cursor=201','/audit-logs?limit=100&cursor=101','/audit-logs?limit=100&cursor=201','/audit-logs?limit=100'])
})

test('审计游标不转Number或使用资源UUID推导，大整数游标原样发送',async t=>{
  const cursor='9223372036854775806'
  const h=audit(t,{get:async url=>page(url.searchParams.has('cursor')?'second':'first',1,url.searchParams.has('cursor')?'':cursor)})
  await h.state.refresh();await h.state.nextAuditPage()
  assert.equal(h.auditRequests()[1].path,`/audit-logs?limit=100&cursor=${cursor}`)
  assert.deepEqual(h.state.auditCursorStack.value,['',cursor])
})

test('审计刷新从首页重新请求，成功后清空旧栈并使用新的下一页游标',async t=>{
  let refreshed=false
  const h=audit(t,{get:async url=>url.searchParams.has('cursor')?page('second',10):page(refreshed?'fresh':'first',100,refreshed?'900':'200')})
  await h.state.refresh();await h.state.nextAuditPage()
  assert.equal(h.state.auditPage.value,2)
  refreshed=true;await h.state.refresh()
  assert.equal(h.state.auditPage.value,1);assert.equal(h.state.audits.value[0].action,'fresh')
  assert.deepEqual(h.state.auditCursorStack.value,['']);assert.equal(h.state.auditNextCursor.value,'900')
  assert.equal(h.auditRequests().length,3)
})

test('审计下一页失败保留页号、记录和游标，重试不跳过失败页',async t=>{
  let failures=0
  const h=audit(t,{get:async url=>{
    const cursor=url.searchParams.get('cursor')||''
    if(cursor==='100'&&failures++===0)throw new Error('审计暂不可用')
    return cursor==='100'?page('last',1):cursor==='200'?page('second',100,'100'):page('first',100,'200')
  }})
  await h.state.refresh();await h.state.nextAuditPage();await h.state.nextAuditPage()
  assert.equal(h.state.auditPage.value,2);assert.deepEqual(h.state.auditCursorStack.value,['','200'])
  assert.equal(h.state.audits.value[0].action,'second');assert.equal(h.state.auditNextCursor.value,'100')
  assert.equal(h.state.auditLoading.value,false);assert.equal(h.notifications.at(-1).value,'审计暂不可用')
  await h.state.nextAuditPage()
  assert.equal(h.state.auditPage.value,3);assert.equal(h.state.audits.value[0].action,'last')
  assert.equal(h.auditRequests().at(-1).path,'/audit-logs?limit=100&cursor=100')
})

test('审计刷新失败保留原页上下文，不把第二页旧记录冒充第一页',async t=>{
  let fail=false
  const h=audit(t,{get:async url=>{
    if(fail)throw new Error('刷新失败')
    return url.searchParams.has('cursor')?page('second',12):page('first',100,'200')
  }})
  await h.state.refresh();await h.state.nextAuditPage()
  fail=true;await h.state.refresh()
  assert.equal(h.state.auditPage.value,2);assert.equal(h.state.audits.value[0].action,'second')
  assert.deepEqual(h.state.auditCursorStack.value,['','200'])
  assert.equal(h.notifications.at(-1).value,'刷新失败')
})

test('刷新接管正在读取的旧页，迟到旧成功不得覆盖首页和新游标',async t=>{
  const h=pendingAudit(t)
  const initial=h.state.refresh();h.pending[0].resolve(page('first',100,'200'));await initial
  const next=h.state.nextAuditPage(),refresh=h.state.refresh()
  h.pending[2].resolve(page('fresh',100,'900'));await refresh
  h.pending[1].resolve(page('old-second',100,'100'));await next
  assert.equal(h.pending[1].request.signal.aborted,true)
  assert.equal(h.state.auditPage.value,1);assert.equal(h.state.audits.value[0].action,'fresh')
  assert.equal(h.state.auditNextCursor.value,'900');assert.equal(h.state.auditLoading.value,false)
})

test('旧审计错误不弹出且finally不能结束仍在读取的新页loading',async t=>{
  const h=pendingAudit(t)
  const old=h.state.refresh(),current=h.state.refresh()
  h.pending[0].reject(new Error('过时请求失败'));await old
  const loadingBeforeCurrent=h.state.auditLoading.value,notificationsBeforeCurrent=[...h.notifications]
  h.pending[1].resolve(page('current',10));await current
  assert.equal(loadingBeforeCurrent,true);assert.deepEqual(notificationsBeforeCurrent,[])
  assert.equal(h.state.audits.value[0].action,'current');assert.equal(h.state.auditLoading.value,false)
})

test('审计分页忙碌时不重复下一页或上一页，未给有效游标时不能继续',async t=>{
  const h=pendingAudit(t)
  const initial=h.state.refresh();h.pending[0].resolve(page('first',100,'200'));await initial
  const next=h.state.nextAuditPage()
  await h.state.nextAuditPage();await h.state.previousAuditPage()
  assert.equal(h.pending.length,2)
  h.pending[1].resolve({...page('last',10),has_more:true,next_cursor:''});await next
  await h.state.nextAuditPage();assert.equal(h.pending.length,2)
})

test('离开审计视图立即清空并取消旧页，再进入只请求首页一次',async t=>{
  const h=pendingAudit(t,{codes:['audit.read','role.read']})
  const initial=h.state.refresh();h.pending[0].resolve(page('first',100,'200'));await initial
  const next=h.state.nextAuditPage()
  h.route.query.view='roles';await vue.nextTick()
  assert.equal(h.state.audits.value.length,0);assert.deepEqual(h.state.auditCursorStack.value,[''])
  h.pending[1].resolve(page('old-second',100));await next
  assert.equal(h.state.audits.value.length,0)
  h.route.query.view='audit';await vue.nextTick()
  assert.equal(h.pending.length,3);assert.equal(h.pending[2].url.searchParams.get('cursor'),null)
  h.pending[2].resolve(page('fresh',2));await settle()
  assert.equal(h.state.auditPage.value,1);assert.equal(h.state.audits.value[0].action,'fresh')
})

for(const field of ['id','department_id','project_id']) {
  test(`审计访问上下文${field}变化立即丢弃旧页，并从新范围首页读取`,async t=>{
    const h=pendingAudit(t)
    const initial=h.state.refresh();h.pending[0].resolve(page('first',100,'200'));await initial
    const next=h.state.nextAuditPage()
    h.identity[field]='changed';await vue.nextTick()
    assert.equal(h.state.audits.value.length,0);assert.equal(h.pending[2].url.searchParams.get('cursor'),null)
    h.pending[1].resolve(page('old-scope',100));await next
    assert.equal(h.state.audits.value.length,0)
    h.pending[2].resolve(page('new-scope',1));await settle()
    assert.equal(h.state.audits.value[0].action,'new-scope');assert.equal(h.state.auditPage.value,1)
  })
}

test('撤销审计权限立即取消读取清空记录；恢复后只读取新首页',async t=>{
  const h=pendingAudit(t)
  const initial=h.state.refresh();h.pending[0].resolve(page('first',100,'200'));await initial
  const next=h.state.nextAuditPage()
  h.identity.permissions=[];await vue.nextTick()
  assert.equal(h.state.audits.value.length,0);assert.equal(h.state.active.value,'')
  h.pending[1].reject(new Error('已撤销的旧请求失败'));await next
  assert.deepEqual(h.notifications,[]);assert.equal(h.pending.length,2)
  h.identity.permissions=['audit.read'];await vue.nextTick()
  assert.equal(h.pending.length,3);assert.equal(h.pending[2].url.searchParams.get('cursor'),null)
  h.pending[2].resolve(page('restored',1));await settle()
  assert.equal(h.state.audits.value[0].action,'restored')
})

test('没有审计权限不发请求，组件卸载后旧响应和旧错误均失效',async t=>{
  const denied=audit(t,{codes:[],get:async()=>{assert.fail('无权限不得读取审计')}})
  await denied.state.refresh();assert.equal(denied.auditRequests().length,0)
  for(const reject of [false,true]) {
    const h=pendingAudit(t)
    const pending=h.state.refresh()
    h.unmount.forEach(callback=>callback())
    if(reject)h.pending[0].reject(new Error('卸载后的旧错误'))
    else h.pending[0].resolve(page('old',100,'200'))
    await pending
    assert.equal(h.unmount.length,1);assert.equal(h.pending[0].request.signal.aborted,true)
    assert.equal(h.state.audits.value.length,0);assert.deepEqual(h.notifications,[])
  }
})

for(const status of [401,403]) {
  test(`审计接口返回${status}时即使身份缓存未刷新也清空旧页和游标，保留明确错误`,async t=>{
    const h=audit(t,{get:async url=>{
      const cursor=url.searchParams.get('cursor')||''
      if(cursor==='100')throw new AxiosError('当前身份无权继续读取审计','ERR_BAD_REQUEST',undefined,undefined,{status})
      return cursor==='200'?page('second',100,'100'):page('first',100,'200')
    }})
    await h.state.refresh();await h.state.nextAuditPage();await h.state.nextAuditPage()
    assert.equal(h.identity.permissions.includes('audit.read'),true)
    assert.equal(h.state.audits.value.length,0);assert.equal(h.state.auditPage.value,1)
    assert.deepEqual(h.state.auditCursorStack.value,['']);assert.equal(h.state.auditNextCursor.value,'')
    assert.equal(h.state.auditHasMore.value,false);assert.equal(h.state.auditLoading.value,false)
    assert.equal(h.state.auditError.value,'当前身份无权继续读取审计')
    assert.equal(h.notifications.at(-1).value,'当前身份无权继续读取审计')
  })
}

test('审计接口500仍保留原页上下文和可重试游标',async t=>{
  const h=audit(t,{get:async url=>{
    if(url.searchParams.has('cursor'))throw new AxiosError('审计暂不可用','ERR_BAD_RESPONSE',undefined,undefined,{status:500})
    return page('first',100,'200')
  }})
  await h.state.refresh();await h.state.nextAuditPage()
  assert.equal(h.state.audits.value.length,100);assert.equal(h.state.audits.value[0].action,'first')
  assert.deepEqual(h.state.auditCursorStack.value,['']);assert.equal(h.state.auditNextCursor.value,'200')
  assert.equal(h.state.auditHasMore.value,true);assert.equal(h.state.auditError.value,'审计暂不可用')
})

test('迟到旧403不得清空刷新后的合法页或替换新游标',async t=>{
  const h=pendingAudit(t)
  const initial=h.state.refresh();h.pending[0].resolve(page('first',100,'200'));await initial
  const old=h.state.nextAuditPage(),current=h.state.refresh()
  h.pending[2].resolve(page('current',100,'900'));await current
  h.pending[1].reject(new AxiosError('已过期范围拒绝','ERR_BAD_REQUEST',undefined,undefined,{status:403}));await old
  assert.equal(h.state.audits.value.length,100);assert.equal(h.state.audits.value[0].action,'current')
  assert.equal(h.state.auditNextCursor.value,'900');assert.equal(h.state.auditHasMore.value,true)
  assert.equal(h.state.auditError.value,'');assert.deepEqual(h.notifications,[])
})

test('审计结果按真实枚举展示中文与状态色，未知值不冒充成功',t=>{
  const h=audit(t,{get:async()=>page('first',0)})
  assert.deepEqual(h.state.auditResult('succeeded'),{label:'成功',color:'success'})
  assert.deepEqual(h.state.auditResult('failed'),{label:'失败',color:'error'})
  assert.deepEqual(h.state.auditResult('denied'),{label:'拒绝',color:'warning'})
  for(const value of ['success','unexpected','',null,undefined])assert.deepEqual(h.state.auditResult(value),{label:'未知',color:'default'})
})

test('模板绑定：仅审计ResourceTable关闭本地分页，其他列表继续使用默认分页',()=>{
  const source=readFileSync(new URL('../src/views/AccessView.vue',import.meta.url),'utf8')
  const ast=parse(source).descriptor.template.ast
  const elements=[]
  function visit(node){if(node.type===1)elements.push(node);for(const child of node.children||[])visit(child)}
  visit(ast)
  const tables=elements.filter(node=>node.tag==='ResourceTable')
  const binding=(node,name)=>node.props.find(prop=>prop.type===7&&prop.name==='bind'&&prop.arg?.content===name)?.exp?.content
  const audits=tables.filter(node=>binding(node,'rows')==='audits')
  assert.equal(audits.length,1);assert.equal(binding(audits[0],'pagination'),'false')
  for(const table of tables.filter(node=>binding(node,'rows')!=='audits'))assert.equal(binding(table,'pagination'),undefined)
})
