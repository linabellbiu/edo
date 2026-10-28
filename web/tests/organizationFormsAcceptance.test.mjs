import assert from 'node:assert/strict'
import {test} from 'node:test'
import {componentHarness,loadFrontend} from './acceptanceHarness.mjs'

const writes=h=>h.requests.filter(item=>item.method!=='get')
function harness(t,path,options={}) {const h=componentHarness(path,options);t.after(h.stop);return h}

test('组织读取只请求已授权的公司或部门，筛选不修改原部门目录',async t=>{
  const departments=[{id:'department-a',name:'研发',description:'业务研发',parent_id:'',member_count:1}]
  const h=harness(t,'../src/views/OrganizationView.vue',{codes:['department.read'],api:{get:async()=>({data:{departments}})}})
  await h.state.load()
  assert.deepEqual(h.requests.map(item=>item.path),['/departments'])
  h.state.query.value='业务'
  assert.equal(h.state.filtered.value.length,1)
  h.state.query.value='不存在'
  assert.equal(h.state.filtered.value.length,0);assert.equal(h.state.rows.value.length,1)
  const company=harness(t,'../src/views/OrganizationView.vue',{codes:['organization.read'],api:{get:async()=>({data:{organization:{name:'测试公司',description:'公司说明'}}})}})
  await company.state.load()
  assert.deepEqual(company.requests.map(item=>item.path),['/organization']);assert.equal(company.state.company.name,'测试公司')
})

test('公司和部门保存区分创建及更新，空名称不发送，接口失败保留编辑草稿',async t=>{
  const h=harness(t,'../src/views/OrganizationView.vue')
  await h.state.saveCompany();await h.state.saveDepartment();assert.equal(writes(h).length,0)
  Object.assign(h.state.company,{name:'公司名称',description:'公司说明'});h.state.companyOpen.value=true
  await h.state.saveCompany()
  assert.deepEqual(writes(h)[0],{method:'put',path:'/organization',body:{name:'公司名称',description:'公司说明'},options:undefined})
  assert.equal(h.state.companyOpen.value,false)
  h.state.edit();Object.assign(h.state.form,{name:'研发部',description:'描述',parent_id:'parent-a'})
  await h.state.saveDepartment()
  assert.equal(writes(h)[1].method,'post');assert.equal(writes(h)[1].path,'/departments')
  h.state.edit({id:'department-a',name:'原名',description:'旧描述',parent_id:''});h.state.form.name='新名'
  await h.state.saveDepartment()
  assert.equal(writes(h)[2].method,'put');assert.equal(writes(h)[2].path,'/departments/department-a')
  const failure=harness(t,'../src/views/OrganizationView.vue',{api:{post:async()=>{throw new Error('部门名称重复')}}})
  failure.state.edit();failure.state.form.name='部门草稿'
  await failure.state.saveDepartment()
  assert.equal(failure.state.departmentOpen.value,true);assert.equal(failure.state.form.name,'部门草稿');assert.equal(failure.state.saving.value,false)
})

test('组织页删除先确认，失败向确认层拒绝且保留当前部门',async t=>{
  const h=harness(t,'../src/views/OrganizationView.vue',{api:{delete:async()=>{throw new Error('部门仍有成员')}}})
  const department={id:'department-a',name:'测试部门',member_count:1}
  h.state.selected.value=department;h.state.remove(department)
  assert.equal(writes(h).length,0);assert.equal(h.confirmations[0].okType,'danger')
  await assert.rejects(h.confirmations[0].onOk(),/部门仍有成员/)
  assert.equal(h.state.selected.value.id,'department-a');assert.equal(writes(h)[0].path,'/departments/department-a')
})

test('项目创建采用当前部门，更新绑定准确项目，失败不关闭表单或丢失输入',async t=>{
  const h=harness(t,'../src/views/ProjectsView.vue')
  h.state.edit();assert.equal(h.state.form.department_id,'department-a')
  await h.state.save();assert.equal(writes(h).length,0)
  Object.assign(h.state.form,{name:'测试项目',description:'说明'})
  await h.state.save()
  assert.deepEqual(writes(h)[0],{method:'post',path:'/projects',body:{name:'测试项目',description:'说明',department_id:'department-a',is_active:true},options:undefined})
  assert.equal(h.state.formOpen.value,false)
  h.state.edit({id:'project-b',name:'项目B',description:'B',department_id:'department-b',is_active:false})
  h.state.form.description='新说明';await h.state.save()
  assert.equal(writes(h)[1].method,'put');assert.equal(writes(h)[1].path,'/projects/project-b');assert.equal(writes(h)[1].body.department_id,'department-b')
  const failure=harness(t,'../src/views/ProjectsView.vue',{api:{post:async()=>{throw new Error('项目名称重复')}}})
  failure.state.edit();failure.state.form.name='保留项目草稿';await failure.state.save()
  assert.equal(failure.state.formOpen.value,true);assert.equal(failure.state.form.name,'保留项目草稿');assert.equal(failure.state.saving.value,false)
})

test('项目删除确认绑定原项目，失败不关闭详情，使用项目写入准确范围再导航',async t=>{
  const stored=new Map()
  const h=harness(t,'../src/views/ProjectsView.vue',{api:{delete:async()=>{throw new Error('项目仍有关联资源')}},globals:{localStorage:{setItem:(key,value)=>stored.set(key,value)}}})
  h.state.open.value=true
  h.state.remove({id:'project-a',name:'项目A'})
  assert.equal(writes(h).length,0)
  await assert.rejects(h.confirmations[0].onOk(),/项目仍有关联资源/)
  assert.equal(h.state.open.value,true);assert.equal(writes(h)[0].path,'/projects/project-a')
  h.state.useProject({id:'project-b'})
  assert.equal(stored.get('edo.project'),'project-b');assert.deepEqual(h.locations,['/applications'])
})

test('凭据列表只在明确显示时读取正文，再点击立即隐藏，保存同凭据后清除旧正文',async t=>{
  const h=harness(t,'../src/views/CredentialView.vue',{codes:['credential.secret.read'],api:{get:async path=>path.endsWith('/secret')?{data:{secret:'synthetic-only-secret'}}:{data:{credentials:[]}}}})
  const item={id:'credential-a',name:'测试凭据'}
  await h.state.refresh();assert.equal(h.requests.filter(item=>item.path.endsWith('/secret')).length,0)
  await h.state.reveal(item);assert.equal(h.state.revealed['credential-a'],'synthetic-only-secret')
  await h.state.reveal(item);assert.equal(h.state.revealed['credential-a'],undefined)
  assert.equal(h.requests.filter(item=>item.path.endsWith('/secret')).length,1)
  await h.state.reveal(item);await h.state.saved(item)
  assert.equal(h.state.revealed['credential-a'],undefined)
})

test('凭据普通查看不请求原文，原文请求等待期间撤权不保留结果',async t=>{
  const reader=harness(t,'../src/views/CredentialView.vue',{codes:['credential.read']})
  await reader.state.reveal({id:'credential-a'})
  assert.equal(reader.requests.length,0)
  let finish
  const allowed=harness(t,'../src/views/CredentialView.vue',{codes:['credential.secret.read'],api:{get:()=>new Promise(resolve=>{finish=resolve})}})
  const pending=allowed.state.reveal({id:'credential-a'})
  allowed.auth.permissions.delete('credential.secret.read')
  finish({data:{secret:'synthetic-discarded-secret'}})
  await pending
  assert.equal(allowed.state.revealed['credential-a'],undefined)
})

test('登录方式只读不加载授权选项，编辑仅使用后端可委派的角色候选',async t=>{
  const reader=harness(t,'../src/views/IdentityProvidersView.vue',{codes:['identity.read']})
  await reader.state.refresh()
  assert.deepEqual(reader.requests.map(item=>item.path),['/identity-providers'])
  const roles=[{id:'safe-role',display_name:'可委派角色'}]
  const editor=harness(t,'../src/views/IdentityProvidersView.vue',{codes:['identity.update'],api:{get:async path=>({data:path.endsWith('/role-choices')?{roles}:{providers:[]}})}})
  await editor.state.refresh()
  assert.deepEqual(editor.requests.map(item=>item.path),['/identity-providers','/identity-providers/role-choices'])
  assert.deepEqual(editor.state.roles.value,roles)
  const failed=harness(t,'../src/views/IdentityProvidersView.vue',{codes:['identity.update'],api:{get:async path=>{if(path.endsWith('/role-choices'))throw new Error('当前无权分配角色');return {data:{providers:[]}}}}})
  failed.state.roles.value=roles
  await failed.state.refresh()
  assert.deepEqual(failed.state.roles.value,[])
  assert.equal(failed.requests.some(item=>item.path==='/roles'),false)
})

test('死信清理只在连接正常且非空时确认，只请求死信端点，忙碌时不重复打开',async t=>{
  const metricUtils=loadFrontend('../src/utils/systemMetrics.ts')
  let finish
  const h=harness(t,'../src/views/SystemMonitorView.vue',{codes:['monitor.execute'],modules:{'@/utils/systemMetrics':metricUtils},api:{delete:()=>new Promise(resolve=>finish=resolve),get:async()=>({data:{node_id:'node-a',latest:{queue:{connected:true,dead_messages:0}},points:[],window_seconds:900}})}})
  h.state.metrics.value={queue:{connected:false,dead_messages:2}}
  h.state.clearDeadMessages();assert.equal(h.confirmations.length,0)
  h.state.metrics.value.queue={connected:true,dead_messages:0}
  h.state.clearDeadMessages();assert.equal(h.confirmations.length,0)
  h.state.metrics.value.queue.dead_messages=2
  h.state.clearDeadMessages();assert.equal(writes(h).length,0);assert.equal(h.confirmations.length,1)
  const clearing=h.confirmations[0].onOk()
  h.state.clearDeadMessages();assert.equal(h.confirmations.length,1)
  assert.equal(writes(h)[0].path,'/system/metrics/queue/dead-messages')
  finish({data:{purged_messages:2}});await clearing
  assert.equal(h.state.clearingDeadMessages.value,false)
  assert.equal(h.state.canExecuteQueueAction.value,true)
})
