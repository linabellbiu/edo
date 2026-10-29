import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import * as vue from 'vue'
import { PostgreSQL, MySQL } from '@codemirror/lang-sql'
import { loadFrontend } from './acceptanceHarness.mjs'

const settle = async () => { await vue.nextTick(); await new Promise(resolve => setImmediate(resolve)); await vue.nextTick() }
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const clone = value => JSON.parse(JSON.stringify(value))
const allCodes = ['read','create','update','delete','review','execute','skip','check'].map(code => `sql_change.${code}`).concat('application_database.execute')
const choices = {
  applications: [{ id:'app-a', name:'应用 A', repository_id:'repo-a', repository_name:'代码 A' }, { id:'app-b', name:'应用 B', repository_id:'repo-b' }],
  pipeline_runs: [{ id:'run-a', application_id:'app-a', ref:'refs/heads/main', commit_sha:'a'.repeat(40), message:'本次发布 A', status:'running', created_at:'2026-10-24T00:00:00Z' }, { id:'run-b', application_id:'app-b', ref:'refs/heads/test', commit_sha:'b'.repeat(40), message:'本次发布 B', status:'running', created_at:'2026-10-24T00:00:00Z' }],
  databases: [{ id:'db-a', name:'数据库 A', application_id:'app-a', environment_id:'env-a', environment_name:'环境 A', driver:'postgres', host:'db-a.example',port:5432,database:'orders',version:1 }, { id:'db-b', name:'数据库 B', application_id:'app-b', environment_id:'env-b', environment_name:'环境 B', driver:'mysql',host:'db-b.example',port:3306,database:'payments',version:1 }],
  reviewers: [{ id:'reviewer', name:'审核员' }],
}
function value(status='pending_review', overrides={}) {
  const change = { id:'change-a', name:'安全变更', application_id:'app-a', repository_id:'repo-a', branch:'main', pipeline_run_id:'run-a', database_id:'db-a',database_version:1, environment_id:'env-a', phase:'before', reviewer_id:'reviewer', version:7, revision_id:'revision-a', status, digest:'sha256-fixed', created_by:'applicant', author_id:'applicant', created_at:'2026-10-24T00:00:00Z', updated_at:'2026-10-24T00:00:00Z', ...overrides }
  return { change, sql:'UPDATE orders SET state = 1 WHERE id = 42;', revision:{ id:'revision-a', version:2, digest:change.digest, check_status:'passed', check_summary:'静态检查完成', check_report:{ findings:[{line:2,column:3,rule:'rule',message:'需人工核对'}] }, created_at:change.created_at }, reviews:[], executions:[] }
}
async function harness({ codes=allCodes, user='applicant', query={}, get, post, put, del }={}) {
  const requests=[], messages=[], confirmations=[], unmounts=[], mounted=[], routeLeaves=[], documentListeners=new Map()
  const auth = vue.reactive({ user:{id:user, department_id:'department-a', project_id:'project-a', is_superuser:false}, permissions:new Set(codes), canAny(required){return !!this.user && (this.user.is_superuser || !required.length || required.some(code => this.permissions.has(code)))} })
  const route=vue.reactive({path:'/sql-changes',query:{...query}})
  const router={async push({query}){route.query={...query}}}
  const client={async get(path,options={}){requests.push({method:'get',path,options});if(get){const response=await get(path,options);if(response)return response};return {data:path==='/sql-changes/options'?clone(choices):path==='/sql-changes'?{items:[],total:0}:value()}},async post(path,body,options={}){requests.push({method:'post',path,body:clone(body),options});return post?post(path,body,options):{data:value(path.endsWith('/execute')?'queued':'checking')}},async put(path,body,options={}){requests.push({method:'put',path,body:clone(body),options});return put?put(path,body,options):{data:value('checking')}},async delete(path,options={}){requests.push({method:'delete',path,options});return del?del(path,options):{data:{}}}}
  const api=loadFrontend('../src/api/sqlChanges.ts',{'@/api/client':{__esModule:true,default:client}})
  const document={hidden:false,addEventListener:(event,handler)=>documentListeners.set(event,handler),removeEventListener:event=>documentListeners.delete(event)}
  const component=loadFrontend('../src/views/SQLChangesView.vue',{
    vue:{...vue,onMounted:fn=>mounted.push(fn),onBeforeUnmount:fn=>unmounts.push(fn)},
    'vue-router':{useRoute:()=>route,useRouter:()=>router,onBeforeRouteLeave:fn=>routeLeaves.push(fn)},
    'vue-i18n':{useI18n:()=>({t:key=>key})},'@/stores/auth':{useAuthStore:()=>auth},
    '@/api/client':{__esModule:true,default:client},'@/api/sqlChanges':api,
    '@/api/resources':{apiErrorMessage:()=> '安全错误提示'},
    'ant-design-vue':{message:Object.fromEntries(['success','error','warning'].map(kind=>[kind,text=>messages.push({kind,text})])),Modal:{confirm:config=>{confirmations.push(config);return {destroy(){config.afterClose?.()}}}}},
  },{document,window:{addEventListener(){},removeEventListener(){}},TextEncoder}).default
  const scope=vue.effectScope();let state,stopped=false
  scope.run(()=>{state=component.setup({}, {expose(){}})})
  mounted.forEach(fn=>fn());await settle()
  const stop=()=>{if(stopped)return;stopped=true;unmounts.forEach(fn=>fn());scope.stop()}
  return {state,auth,route,router,client,api,requests,messages,confirmations,routeLeaves,document,documentListeners,stop}
}
function fill(h) { h.state.form.name='变更';h.state.form.application_id='app-a';h.state.form.pipeline_run_id='run-a';h.state.form.database_id='db-a';h.state.form.reviewer_id='reviewer';h.state.form.sql='UPDATE orders SET state=1 WHERE id=1;' }

test('同身份同有效权限刷新不丢SQL草稿与详情，也不重复读取',async t=>{
  const h=await harness();t.after(h.stop);await h.state.edit();fill(h)
  h.state.detail.value=value();h.state.detailOpen.value=true
  const count=h.requests.length
  h.auth.user={...h.auth.user};h.auth.permissions=new Set([...h.auth.permissions].reverse());await settle()
  assert.equal(h.state.editorOpen.value,true);assert.equal(h.state.form.sql,'UPDATE orders SET state=1 WHERE id=1;')
  assert.equal(h.state.detailOpen.value,true);assert.equal(h.state.detail.value.change.id,'change-a');assert.equal(h.requests.length,count)
})
test('真实用户、部门、项目或超级管理员身份变化仍清理SQL编辑草稿与详情',async t=>{
  for(const update of [h=>{h.auth.user.id='another-user'},h=>{h.auth.user.department_id='department-b'},h=>{h.auth.user.project_id='project-b'},h=>{h.auth.user.is_superuser=true},h=>{h.auth.user=null}]){
    const h=await harness();t.after(h.stop);await h.state.edit();fill(h);h.state.detail.value=value();h.state.detailOpen.value=true
    update(h);await settle();assert.equal(h.state.editorOpen.value,false);assert.equal(h.state.form.sql,'');assert.equal(h.state.detailOpen.value,false);assert.equal(h.state.detail.value,undefined)
  }
})
test('同身份权限校准保留审核确认，超级管理员降级仍清理SQL草稿',async t=>{
  const reviewer=await harness({user:'reviewer'});t.after(reviewer.stop)
  await reviewer.state.prepareAction('review',value().change);assert.equal(reviewer.state.actionDialog.open,true)
  reviewer.auth.user={...reviewer.auth.user};reviewer.auth.permissions=new Set([...reviewer.auth.permissions].reverse());await settle()
  assert.equal(reviewer.state.actionDialog.open,true)
  const h=await harness();t.after(h.stop);h.auth.user.is_superuser=true;await settle();await h.state.edit();fill(h)
  h.auth.user={...h.auth.user,is_superuser:false};await settle();assert.equal(h.state.editorOpen.value,false);assert.equal(h.state.form.sql,'')
})

test('SQL独立页初始化只读一次列表和选项，不自动创建、执行或推进流水线',async t=>{
  const h=await harness();t.after(h.stop)
  assert.equal(h.requests.filter(r=>r.path==='/sql-changes').length,1);assert.equal(h.requests.filter(r=>r.path==='/sql-changes/options').length,1)
  assert.ok(h.requests.every(r=>r.method==='get'));assert.equal(h.state.form.phase,'before')
  const denied=await harness({codes:[]});t.after(denied.stop);assert.deepEqual(denied.requests,[])
})
test('应用隔离选择清空旧发布/DB/分支，环境仓库派生，SQL按UTF8限制16KiB',async t=>{
  const h=await harness();t.after(h.stop);await h.state.edit();fill(h)
  assert.equal(h.state.form.branch,'main');assert.equal(h.state.database.value.environment_name,'环境 A');assert.equal(h.state.application.value.repository_id,'repo-a');assert.equal(h.state.validForm(),true)
  h.state.form.sql='中'.repeat(6000);assert.equal(h.state.validForm(),false);h.state.form.sql='SELECT 1;'
  h.state.form.application_id='app-b';assert.equal(h.state.form.database_id,'');assert.equal(h.state.form.pipeline_run_id,'');assert.equal(h.state.form.branch,'');assert.deepEqual(h.state.runs.value.map(v=>v.id),['run-b'])
  h.state.form.pipeline_run_id='run-a';h.state.form.database_id='db-a';assert.equal(h.state.validForm(),false)
})
test('保存冻结输入并自动检查只由后端发起，双击不重复、成功关闭且不跳页',async t=>{
  const pending=deferred(),h=await harness({post:()=>pending.promise});t.after(h.stop);await h.state.edit();fill(h)
  const saving=h.state.save();await h.state.save();assert.equal(h.state.saving.value,true)
  const writes=h.requests.filter(r=>r.method==='post');assert.equal(writes.length,1);assert.equal(writes[0].path,'/sql-changes');assert.equal(writes[0].body.phase,'before');assert.equal(writes[0].body.expected_version,0)
  h.state.form.sql='SELECT 2';assert.notEqual(writes[0].body.sql,h.state.form.sql)
  pending.resolve({data:value('checking')});await saving;assert.equal(h.state.editorOpen.value,false);assert.equal(h.state.form.sql,'');assert.equal(h.route.query.change_id,undefined)
  assert.ok(!h.requests.some(r=>/execute|advance|release-plans/.test(r.path)))
})
test('切项目中止保存和旧读取，迟到响应不关闭新草稿或显示成功',async t=>{
  const pending=deferred(),h=await harness({post:()=>pending.promise});t.after(h.stop);await h.state.edit();fill(h);const saving=h.state.save(),request=h.requests.find(r=>r.method==='post')
  h.auth.user.project_id='project-b';await settle();assert.equal(request.options.signal.aborted,true);assert.equal(h.state.editorOpen.value,false);assert.equal(h.state.form.sql,'')
  await h.state.edit();h.state.form.sql='新项目草稿';pending.resolve({data:value('checking')});await saving
  assert.equal(h.state.editorOpen.value,true);assert.equal(h.state.form.sql,'新项目草稿');assert.ok(!h.messages.some(m=>m.kind==='success'))
})
test('编辑读取最新详情与版本，不信任旧行，queued/running/succeeded禁止改删',async t=>{
  const h=await harness({get:path=>path==='/sql-changes/change-a'?{data:value('approved',{version:12})}:undefined});t.after(h.stop)
  await h.state.edit(value('pending_review').change);assert.equal(h.state.form.expected_version,12);assert.equal(h.state.form.sql,value().sql)
  for(const status of ['queued','running','succeeded']){assert.equal(h.state.permitted('update',value(status)),false);assert.equal(h.state.permitted('delete',value(status)),false)}
})
test('执行同时要求工单与数据库执行权限、批准状态、无已有执行，冻结digest/version',async t=>{
  const pending=deferred(),h=await harness({get:path=>path==='/sql-changes/change-a'?{data:value('approved')}:undefined,post:()=>pending.promise});t.after(h.stop)
  const approved=value('approved');assert.equal(h.state.permitted('execute',approved),true)
  h.auth.permissions.delete('application_database.execute');assert.equal(h.state.permitted('execute',approved),false);h.auth.permissions.add('application_database.execute')
  for(const status of ['pending_review','rejected','checking','failed','uncertain','skipped','succeeded'])assert.equal(h.state.permitted('execute',value(status)),false,status)
  assert.equal(h.state.permitted('execute',{...approved,executions:[{id:'old',status:'failed'}]}),false)
  await h.state.prepareAction('execute',approved.change);assert.equal(h.requests.filter(r=>r.method==='post').length,0)
  const submit=h.state.submitAction();await h.state.submitAction();assert.equal(h.state.actionDialog.pending,true)
  const writes=h.requests.filter(r=>r.method==='post');assert.equal(writes.length,1);assert.equal(writes[0].path,'/sql-changes/change-a/execute');assert.deepEqual(writes[0].body,{expected_version:7,confirm_digest:'sha256-fixed'})
  pending.resolve({data:value('queued')});await submit;assert.equal(h.state.actionDialog.open,false);assert.ok(h.messages.some(m=>m.text==='sqlChanges.queued'))
})
test('人工审核仅指定非申请人，驳回/skip必须理由，skip无SQL执行副作用',async t=>{
  const h=await harness({user:'reviewer'});t.after(h.stop);const item=value()
  assert.equal(h.state.permitted('review',item),true);assert.equal(h.state.permitted('review',value('pending_review',{created_by:'reviewer'})),false)
  h.auth.user.id='other';assert.equal(h.state.permitted('review',item),false);assert.equal(h.state.permitted('skip',item),false);h.auth.user.id='reviewer';await settle()
  await h.state.prepareAction('review',item.change);h.state.actionDialog.decision='reject';await h.state.submitAction();assert.equal(h.state.actionDialog.error,'sqlChanges.reasonRequired');assert.ok(!h.requests.some(r=>r.method==='post'))
  h.state.actionDialog.reason='需补充索引说明';await h.state.submitAction();let write=h.requests.find(r=>r.method==='post');assert.equal(write.path,'/sql-changes/change-a/reviews');assert.equal(write.body.decision,'reject')
  await h.state.prepareAction('skip',item.change);await h.state.submitAction();assert.equal(h.state.actionDialog.error,'sqlChanges.reasonRequired');h.state.actionDialog.reason='本次无需变更';await h.state.submitAction()
  write=h.requests.filter(r=>r.method==='post').at(-1);assert.equal(write.path,'/sql-changes/change-a/skip');assert.equal(write.body.reason,'本次无需变更');assert.ok(!h.requests.some(r=>r.path.endsWith('/execute')))
})
test('动作失败保留弹窗上下文和安全提示，取消后迟到准备响应不能再打开',async t=>{
  const h=await harness({user:'reviewer',post:async()=>{throw new Error('private SQL/DSN/password')}});t.after(h.stop)
  await h.state.prepareAction('review',value().change);await h.state.submitAction();assert.equal(h.state.actionDialog.open,true);assert.equal(h.state.actionDialog.pending,false);assert.equal(h.state.actionDialog.error,'安全错误提示')
  h.state.closeAction();const pending=deferred();h.client.get=()=>pending.promise
  const preparing=h.state.prepareAction('review',value().change);h.state.closeAction();pending.resolve({data:value()});await preparing;assert.equal(h.state.actionDialog.open,false)
})
test('URL页码筛选与详情深链独立，关闭抽屉保留过滤及页码',async t=>{
  const h=await harness({query:{page:'3',page_size:'20',search:'订单',status:'approved',change_id:'change-a'}});t.after(h.stop)
  assert.equal(h.state.page.value,3);assert.equal(h.state.pageSize.value,20);assert.equal(h.state.search.value,'订单');assert.equal(h.state.detailOpen.value,true);assert.equal(h.state.detail.value.change.id,'change-a')
  h.state.closeDetail();await settle();assert.equal(h.route.query.page,'3');assert.equal(h.route.query.search,'订单');assert.equal(h.route.query.change_id,undefined)
  h.state.search.value='新的';h.state.filter();await settle();assert.equal(h.route.query.page,'1');assert.equal(h.route.query.search,'新的')
})
test('草稿离开需明确放弃；保存中禁止离开，取消保留原SQL',async t=>{
  const h=await harness();t.after(h.stop);await h.state.edit();fill(h)
  let leave=h.routeLeaves[0]();assert.equal(h.confirmations.length,1);h.confirmations[0].onCancel();assert.equal(await leave,false);assert.equal(h.state.editorOpen.value,true)
  h.state.saving.value=true;assert.equal(await h.routeLeaves[0](),false);h.state.saving.value=false
  leave=h.routeLeaves[0]();h.confirmations[1].onOk();assert.equal(await leave,true);assert.equal(h.state.editorOpen.value,false);assert.equal(h.state.form.sql,'')
})
test('读取权限撤销及HTTP403清空列表SQL与选项，不把旧内容留在抽屉',async t=>{
  const h=await harness({query:{change_id:'change-a'}});t.after(h.stop);assert.ok(h.state.detail.value.sql)
  h.auth.permissions.delete('sql_change.read');await settle();assert.equal(h.state.detail.value,undefined);assert.equal(h.state.detailOpen.value,false);assert.deepEqual(h.state.options.value.applications,[])
  const deny=await harness({query:{change_id:'change-a'},get:path=>{if(path==='/sql-changes/change-a')throw {response:{status:403}}}});t.after(deny.stop)
  assert.equal(deny.state.detail.value,undefined);assert.equal(deny.state.detailOpen.value,false);assert.deepEqual(deny.state.rows.value,[]);assert.equal(deny.state.error.value,'安全错误提示')
})
test('执行权限撤销销毁确认，卸载中止请求且无迟到弹窗',async t=>{
  const h=await harness({get:path=>path==='/sql-changes/change-a'?{data:value('approved')}:undefined});t.after(h.stop)
  await h.state.prepareAction('execute',value('approved').change);h.auth.permissions.delete('sql_change.execute');await settle();assert.equal(h.state.actionDialog.open,false);await h.state.submitAction();assert.ok(!h.requests.some(r=>r.method==='post'))
  const pending=deferred();h.client.get=()=>pending.promise;h.auth.permissions.add('sql_change.execute');const prepare=h.state.prepareAction('execute',value('approved').change);h.stop();pending.resolve({data:value('approved')});await prepare;assert.equal(h.state.actionDialog.open,false)
})
test('真实CodeMirror SQL方言解析保留关键字、PG dollar字符串及MySQL标识符，而不是YAML着色',()=>{
  assert.match(PostgreSQL.language.parser.parse('SELECT id, $$hello$$ FROM t; -- note').toString(),/Keyword.*String.*LineComment/)
  assert.match(MySQL.language.parser.parse('SELECT `id` FROM `t` # comment').toString(),/QuotedIdentifier.*LineComment/)
  const source=readFileSync(new URL('../src/components/SQLCodeEditor.vue',import.meta.url),'utf8');assert.match(source,/@codemirror\/lang-sql/);assert.doesNotMatch(source,/lang-yaml|readKubeYaml/);assert.match(source,/lineNumbers\(\)/);assert.match(source,/EditorView\.editable/)
})
test('后端真实检查报告line_no/line_pos映射到编辑器行列，不把工具位置重置为第一行',async t=>{
  const h=await harness();t.after(h.stop)
  assert.deepEqual(h.api.sqlCheckFindings([{code:'LT01',line_no:7,line_pos:12,description:'检查问题'}]),[{code:'LT01',line_no:7,line_pos:12,description:'检查问题',line:7,column:12}])
  assert.deepEqual(h.api.sqlCheckFindings('invalid'),[])
})
test('全局风险保留warning和零行列，不伪造第1行也不触发编辑器定位',async t=>{
  const h=await harness();t.after(h.stop)
  const [risk]=h.api.sqlCheckFindings([{code:'risk.drop',line_no:0,line_pos:0,severity:'warning',description:'核对删除影响'}])
  assert.equal(risk.line,0);assert.equal(risk.column,0);assert.equal(risk.severity,'warning');assert.equal(h.state.hasLocation(risk),false)
  h.state.detailTab.value='check';await h.state.focusIssue(risk);assert.equal(h.state.detailTab.value,'check')
  assert.equal(h.state.hasLocation({line:3,column:7}),true)
})
test('当前修订作者不能自审/自跳过，skip同时要求review权限且准备时复核详情',async t=>{
  const h=await harness({user:'reviewer',get:path=>path==='/sql-changes/change-a'?{data:value('pending_review',{author_id:'reviewer'})}:undefined});t.after(h.stop)
  const old=value(),authored=value('pending_review',{author_id:'reviewer'})
  for(const action of ['review','skip']){assert.equal(h.state.permitted(action,old),true);assert.equal(h.state.permitted(action,authored),false);assert.equal(h.state.permitted(action,value('pending_review',{author_id:''})),false)}
  h.auth.permissions.delete('sql_change.review');assert.equal(h.state.permitted('skip',old),false);h.auth.permissions.add('sql_change.review')
  await h.state.prepareAction('review',old.change);assert.equal(h.state.actionDialog.open,false)
  await h.state.prepareAction('skip',old.change);assert.equal(h.state.actionDialog.open,false);assert.ok(!h.requests.some(r=>r.method==='post'))
})
test('原因按1000 UTF8字节限制、skip审核记录不是驳回',async t=>{
  const h=await harness({user:'reviewer'});t.after(h.stop)
  assert.equal(h.state.decisionText('skip'),'sqlChanges.skip');assert.equal(h.state.decisionText('reject'),'sqlChanges.reject');assert.equal(h.state.decisionText('approve'),'sqlChanges.approve');assert.equal(h.state.decisionText('unknown'),'—')
  await h.state.prepareAction('skip',value().change);h.state.actionDialog.reason='中'.repeat(334);await h.state.submitAction()
  assert.equal(h.state.actionDialog.error,'sqlChanges.reasonTooLong');assert.ok(!h.requests.some(r=>r.method==='post'))
  h.state.actionDialog.reason='中'.repeat(333);await h.state.submitAction();assert.equal(h.requests.filter(r=>r.method==='post').length,1)
})
test('执行必须可核对目标环境/驱动/host端口/实际库名/配置版本，缺失或跨范围拒绝',async t=>{
  const h=await harness();t.after(h.stop);const approved=value('approved')
  assert.equal(h.state.verifiedTarget(approved.change),true);assert.equal(h.state.address(approved.change),'db-a.example:5432')
  for(const patch of [{host:''},{port:0},{database:''},{version:2},{driver:'unknown'},{application_id:'app-b'},{environment_id:'env-b'}]){
    h.state.options.value.databases=[{...choices.databases[0],...patch}];assert.equal(h.state.permitted('execute',approved),false,JSON.stringify(patch))
  }
  h.state.options.value.databases=[];assert.equal(h.state.permitted('execute',approved),false)
})
