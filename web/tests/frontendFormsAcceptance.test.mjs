import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import {componentHarness} from './acceptanceHarness.mjs'

const writes=h=>h.requests.filter(item=>item.method!=='get')
function harness(t,path,options={}) {const h=componentHarness(path,options);t.after(h.stop);return h}
function settings(t,options={}) {return harness(t,'../src/views/SettingsView.vue',options)}

test('个人设置始终可见，变量、登录方式和配置分别按权限展开',t=>{
  assert.deepEqual(settings(t).state.sections.value.map(item=>item.key),['account'])
  const app=settings(t,{codes:['application.read']})
  assert.deepEqual(app.state.sections.value.map(item=>item.key),['account','variables'])
  const identity=settings(t,{codes:['identity.read']})
  assert.deepEqual(identity.state.sections.value.map(item=>item.key),['account','identity'])
  const administrator=settings(t,{superuser:true})
  assert.ok(!administrator.state.sections.value.some(item=>item.key==='database'))
})

test('修改密码先校验长度和确认值，成功才跳转重新登录，失败保留表单',async t=>{
  const h=settings(t)
  Object.assign(h.state.password,{current:'old-test-password',next:'short',confirm:'short'})
  await h.state.changePassword();assert.equal(writes(h).length,0)
  h.state.password.next='new-test-password';h.state.password.confirm='different-password'
  await h.state.changePassword();assert.equal(writes(h).length,0)
  h.state.password.confirm='new-test-password';await h.state.changePassword()
  assert.deepEqual(writes(h)[0].body,{current_password:'old-test-password',new_password:'new-test-password'})
  assert.deepEqual(h.locations,['/login?reason=password_changed'])
  const denied=settings(t,{api:{put:async()=>{throw new Error('原密码不正确')}}})
  Object.assign(denied.state.password,{current:'wrong-test-password',next:'new-test-password',confirm:'new-test-password'})
  await denied.state.changePassword()
  assert.deepEqual(denied.locations,[]);assert.equal(denied.state.passwordSaving.value,false)
  assert.equal(denied.state.password.next,'new-test-password')
})

test('目录清理需要执行权限与确认，失败拒绝确认Promise，成功不提交任意目录路径',async t=>{
  const reader=settings(t,{codes:['config.read']})
  reader.state.cleanupDirectory('cache');assert.equal(reader.confirmations.length,0)
  const h=settings(t,{codes:['config.execute'],api:{post:async()=>({data:{files_deleted:2,bytes_released:1024}})}})
  h.state.cleanupDirectory('cache');assert.equal(writes(h).length,0)
  await h.confirmations[0].onOk()
  assert.deepEqual(writes(h)[0],{method:'post',path:'/settings/runtime-directories/cleanup-cache',body:undefined,options:undefined})
  assert.equal(h.state.saving.value,'')
  const denied=settings(t,{codes:['config.execute'],api:{post:async()=>{throw new Error('当前目录忙碌')}}})
  denied.state.cleanupDirectory('artifacts')
  await assert.rejects(denied.confirmations[0].onOk(),/当前目录忙碌/)
  assert.equal(denied.state.saving.value,'')
})

test('运行日志及四类目录保存携带版本，日志配置不混入数据库或凭据',async t=>{
  const h=settings(t,{codes:['config.update']})
  h.state.runtimeLogging.value={level:'warn',http_access_enabled:false,file_enabled:true,file_directory:'/tmp/edo-logs',max_file_size_mb:10,compress_after_days:2,version:7}
  await h.state.saveRuntimeLogging()
  assert.deepEqual(writes(h)[0].body,{level:'warn',http_access_enabled:false,file_enabled:true,file_directory:'/tmp/edo-logs',max_file_size_mb:10,compress_after_days:2,expected_version:7})
  h.state.runtimeDirectories.value={workspace_directory:'/tmp/workspaces',build_directory:'/tmp/builds',cache_directory:'/tmp/cache',local_artifact_directory:'/tmp/artifacts',version:4}
  await h.state.saveRuntimeDirectories()
  assert.deepEqual(writes(h)[1].body,{workspace_directory:'/tmp/workspaces',build_directory:'/tmp/builds',cache_directory:'/tmp/cache',local_artifact_directory:'/tmp/artifacts',expected_version:4})
})

test('登录表单按本地、LDAP和邮箱验证码选择正确入口，并限制外部跳转',async t=>{
  const calls=[]
  const auth={login:async(...args)=>calls.push(['local',...args]),loginLDAP:async(...args)=>calls.push(['ldap',...args]),loginEmail:async(...args)=>calls.push(['email',...args])}
  const h=harness(t,'../src/views/LoginView.vue',{query:{redirect:'/applications?application=app-id'},modules:{'@/stores/auth':{useAuthStore:()=>auth},'@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})},axios:{__esModule:true,default:{isAxiosError:()=>false}}}})
  await h.state.submit();assert.equal(calls.length,0)
  h.state.username.value='tester';h.state.password.value='synthetic-login-password';await h.state.submit()
  h.state.providers.value=[{id:'ldap-provider',type:'ldap',display_name:'LDAP'},{id:'email-provider',type:'email',display_name:'邮箱'},{id:'oauth-provider',type:'github',display_name:'GitHub'}]
  h.state.provider.value='ldap-provider';await h.state.submit()
  h.state.provider.value='email-provider';h.state.email.value='test@example.com';h.state.code.value='bad';await h.state.submit();assert.equal(calls.length,2)
  h.state.code.value='123456';await h.state.submit()
  assert.deepEqual(calls.map(item=>item[0]),['local','ldap','email'])
  assert.ok(h.replacements.every(item=>item==='/applications?application=app-id'))
  for(const target of ['https://outside.example','//outside.example','/\\outside.example','javascript:alert(1)',null]) assert.equal(h.state.safeRedirect(target),'/')
  h.state.externalLogin({id:'github/provider'})
  assert.equal(h.locations[0],'/api/v1/auth/oauth/github%2Fprovider/start?return_to=%2Fapplications%3Fapplication%3Dapp-id')
})

test('邮箱验证码请求包含选定提供方且成功设置重发等待，失败清理loading',async t=>{
  const h=harness(t,'../src/views/LoginView.vue',{api:{post:async()=>({data:{retry_after:60}})},modules:{'@/stores/preferences':{usePreferencesStore:()=>({})},axios:{__esModule:true,default:{isAxiosError:()=>false}}}})
  h.state.provider.value='email/provider';await h.state.sendCode();assert.equal(writes(h).length,0)
  h.state.email.value='test@example.com';await h.state.sendCode()
  assert.equal(writes(h)[0].path,'/auth/email/email%2Fprovider/code')
  assert.deepEqual(writes(h)[0].body,{email:'test@example.com'})
  assert.equal(h.state.resendSeconds.value,60);assert.equal(h.state.sendingCode.value,false)
})

test('登录方式编辑不回填密钥，留空保留，邮箱域名和回调字段按类型构造',async t=>{
  const h=harness(t,'../src/views/IdentityProvidersView.vue',{codes:['identity.update']})
  h.state.edit({id:'email-id',type:'email',name:'email',display_name:'邮箱',email:{host:'smtp.example.com',port:465,username:'mailer',from:'edo@example.com',tls_mode:'tls',allowed_domains:['example.com'],has_password:true}})
  assert.equal(h.state.form.email_password,'');assert.equal(h.state.form.client_secret,'');assert.equal(h.state.form.ldap_bind_password,'')
  h.state.form.email_domains='example.com，other.example\nthird.example';await h.state.save()
  assert.equal(writes(h)[0].path,'/identity-providers/email-id')
  assert.equal(writes(h)[0].body.email.password,undefined)
  assert.deepEqual(writes(h)[0].body.email.allowed_domains,['example.com','other.example','third.example'])
  assert.equal(writes(h)[0].body.redirect_url,'')
  assert.equal(writes(h)[0].body.client_secret,undefined)
  assert.equal(h.state.open.value,false)
})

test('登录方式保存失败保留表单，启停只提交状态而不带密钥',async t=>{
  const h=harness(t,'../src/views/IdentityProvidersView.vue',{codes:['identity.update'],api:{put:async()=>{throw new Error('SMTP配置无效')}}})
  h.state.edit({id:'provider-id',type:'ldap',name:'ldap',display_name:'LDAP'})
  h.state.form.ldap_bind_password='synthetic-bind-password';await h.state.save()
  assert.equal(h.state.open.value,true);assert.equal(h.state.saving.value,false)
  assert.equal(h.state.form.ldap_bind_password,'synthetic-bind-password')
  await h.state.toggle({id:'provider-id',is_active:true})
  assert.deepEqual(writes(h)[1],{method:'patch',path:'/identity-providers/provider-id/status',body:{is_active:false},options:undefined})
})

test('系统日志首屏替换与历史追加使用正确游标和筛选，错误保留已有结果',async t=>{
  let calls=0
  const h=harness(t,'../src/views/LogsView.vue',{api:{get:async()=>{calls++;if(calls===3)throw new Error('日志暂不可用');return {data:{items:[{id:3-calls,message:'日志'+calls}],next_before_id:3-calls,has_more:true}}}},modules:{vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}}}})
  h.state.level.value='warn';h.state.query.value='pipeline';await h.state.load(false)
  await h.state.load(true)
  assert.equal(h.requests[0].body.params.limit,100);assert.equal(h.requests[0].body.params.level,'warn');assert.equal(h.requests[0].body.params.query,'pipeline')
  assert.equal(h.requests[1].body.params.before_id,2)
  assert.deepEqual(h.state.items.value.map(item=>item.id),[2,1])
  await h.state.load(false)
  assert.deepEqual(h.state.items.value.map(item=>item.id),[2,1]);assert.equal(h.state.loading.value,false)
})

test('任务取消和幂等重试调用准确任务接口，失败不会显示成功或清空列表',async t=>{
  const requests=[]
  const h=harness(t,'../src/views/TaskCenterView.vue',{codes:['task.read','task.execute'],api:{get:async()=>({data:{tasks:[{id:'pending',status:'pending'}]}})},modules:{'@/api/resources':{apiErrorMessage:error=>error.message,getResources:async()=>[{id:'pending',status:'pending'}],postResource:async path=>{requests.push(path);if(path.includes('denied'))throw new Error('任务不允许重试')}}}})
  await h.state.refresh();await h.state.action({id:'exact-task'},'cancel')
  assert.deepEqual(requests,['/tasks/exact-task/cancel'])
  await h.state.action({id:'denied'},'retry')
  assert.deepEqual(h.state.rows.value,[{id:'pending',status:'pending'}])
  assert.equal(h.notifications.filter(item=>item.kind==='success').length,1)
  assert.equal(h.notifications.at(-1).value,'任务不允许重试')
})

test('DNS账号切换厂商重置字段，编辑留空密钥不要求清除已有值',async t=>{
  const h=harness(t,'../src/views/DomainView.vue',{codes:['dns.update'],api:{get:async path=>({data:path==='/dns/providers'?{providers:[]}:path==='/dns/accounts'?{accounts:[]}:{domains:[]}})}})
  h.state.providers.value=[{code:'one',fields:[{key:'token',secret:true}]},{code:'two',fields:[{key:'region',default:'cn-test'}]}]
  h.state.newAccount();h.state.accountForm.config.token='synthetic-token';h.state.changeProvider('two')
  assert.deepEqual(h.state.accountForm.config,{region:'cn-test'})
  h.state.editAccount({id:'account-id',name:'账号',provider:'one',config:{},configured_secret_fields:['token']})
  assert.equal(h.state.accountForm.config.token,'')
  await h.state.saveAccount()
  assert.equal(writes(h)[0].path,'/dns/accounts/account-id')
  assert.deepEqual(writes(h)[0].body.clear_secret_fields,[])
  assert.equal(h.state.accountOpen.value,false)
})

test('DNS记录请求切换域名后丢弃迟到响应，关闭详情也不恢复旧记录',async t=>{
  const pending=[]
  const h=harness(t,'../src/views/DomainView.vue',{api:{get:()=>new Promise(resolve=>pending.push(resolve))},modules:{vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}}}})
  h.state.selectedID.value='first';h.state.detailOpen.value=true;const first=h.state.loadRecords()
  h.state.selectedID.value='second';const second=h.state.loadRecords()
  pending[1]({data:{records:[{id:'second-record'}]}});await second
  pending[0]({data:{records:[{id:'first-record'}]}});await first
  assert.deepEqual(h.state.records.value.map(item=>item.id),['second-record'])
  const closing=h.state.loadRecords();h.state.detailOpen.value=false
  pending[2]({data:{records:[{id:'late-record'}]}});await closing
  assert.deepEqual(h.state.records.value.map(item=>item.id),['second-record'])
})

test('项目成员变更在确认时固定项目和成员，角色只能委派当前项目已有权限',async t=>{
  const h=harness(t,'../src/views/ProjectsView.vue',{codes:['project.member.update'],api:{get:async()=>({data:{project:{id:'project-a'},members:[],users:[],roles:[],permissions:['project.member.update'],catalog:[]}})}})
  h.state.selected.value={project:{id:'project-a'},permissions:['project.member.update','application.read'],roles:[{id:'reader',display_name:'只读',permissions:['application.read']},{id:'strong',display_name:'高级',permissions:['user.delete']}],members:[]}
  assert.deepEqual(h.state.roleOptions.value.map(item=>[item.value,item.disabled]),[['reader',false],['strong',true]])
  h.state.assign('member-a','reader');assert.equal(writes(h).length,0)
  h.state.selected.value={project:{id:'project-b'},permissions:[],roles:[],members:[]}
  await h.confirmations[0].onOk()
  assert.deepEqual(writes(h)[0],{method:'put',path:'/projects/project-a/members/member-a',body:{role_id:'reader'},options:undefined})
})
