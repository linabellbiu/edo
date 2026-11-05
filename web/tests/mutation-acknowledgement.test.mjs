// 仅真实前端源码与内存 Axios adapter；没有 HTTP、浏览器、Docker 或服务动作。
import assert from 'node:assert/strict'
import {test} from 'node:test'
import {createRequire} from 'node:module'
import {loadFrontend,componentHarness} from './acceptanceHarness.mjs'
const require=createRequire(import.meta.url),axios=require('axios'),vue=require('vue')
const tick=()=>new Promise(resolve=>setImmediate(resolve))
function transport(){
 const window=new EventTarget(),calls=[]
 const module=loadFrontend('../src/api/client.ts',{axios:{__esModule:true,default:axios}},{window,localStorage:{getItem:()=> 'project-a'}})
 const resources=loadFrontend('../src/api/resources.ts',{axios:{__esModule:true,default:axios},'@/api/client':module})
 let pending
 module.default.defaults.adapter=config=>{
  calls.push({method:config.method,path:config.url})
  if(config.method!=='get')return new Promise((resolve,reject)=>{pending={config,resolve,reject}})
  assert.ok(['/roles','/permissions','/projects','/users?limit=200&offset=0','/users','/departments','/organization'].includes(config.url),'只允许固定的内存读取')
  return Promise.resolve({status:200,statusText:'OK',headers:{},config,data:{roles:[],permissions:[],projects:[],users:[],departments:[],organization:{name:'',description:''}}})
 }
 const respond=(status=200)=>pending.resolve({status,statusText:'OK',headers:{},config:pending.config,data:{marker:'synthetic-old-response-not-for-ui'}})
 return{module,resources,calls,respond,get pending(){return pending}}
}
function view(t,path,globals={}){
 const h=transport(),identity=vue.reactive({authorizationState:'ready',user:{id:'actor-a',department_id:'department-a',project_id:'project-a',is_superuser:true,permissions:['role.update','role.delete','project.update']}})
 const unmount=[]
 const auth={get user(){return identity.user},get authorizationState(){return identity.authorizationState},get permissions(){return new Set(identity.user.permissions)},canAny:codes=>!codes.length||identity.authorizationState==='ready'}
 const component=componentHarness(path,{query:{view:'roles'},globals,modules:{vue:{...vue,onMounted(){},onBeforeUnmount:callback=>unmount.push(callback),onUnmounted(){}},'@/api/client':h.module,'@/api/resources':h.resources,'@/stores/auth':{useAuthStore:()=>auth}}})
 let disposed=false
 const dispose=()=>{if(disposed)return;disposed=true;unmount.forEach(callback=>callback());component.stop()}
 t.after(dispose)
 return{...h,...component,identity,dispose,get pending(){return h.pending},stale(){identity.authorizationState='checking';h.module.setAuthorizationFreshness(false)}}
}
test('同身份 PUT 确认200后遇到旧授权epoch，应分类为无正文受理回执而非未确认失败',async()=>{
 const h=transport(),request=h.module.default.put('/roles/role-a/permissions',{permissions:[]});await tick()
 h.module.setAuthorizationFreshness(false);h.respond(200)
 let caught;try{await request}catch(error){caught=error}
 assert.equal(caught?.code,'EDO_MUTATION_ACKNOWLEDGED')
 assert.equal(caught.response,undefined)
 assert.equal(caught.config,undefined)
 assert.equal(caught.request,undefined)
 assert.equal(JSON.stringify(caught).includes('synthetic-old-response-not-for-ui'),false)
 assert.equal(h.calls.length,1)
})
test('真实 AccessView 保存遇到 changed→PUT200，不得显示普通失败或连接失败',async t=>{
 const h=view(t,'../src/views/AccessView.vue')
 h.state.editRolePermissions({id:'role-a',name:'role-a',display_name:'测试角色',description:'',permissions:[]})
 const saving=h.state.saveRole();await tick();h.stale();h.respond(200);await saving
 assert.equal(h.notifications.filter(item=>item.kind==='error').length,0)
 assert.equal(h.calls.filter(item=>item.method==='put').length,1)
 assert.equal(h.state.roles.value.length,0)
})
test('创建用户201遇到同身份权限核验，关闭已受理表单并清空密码，不重复提交',async t=>{
 const h=view(t,'../src/views/AccessView.vue',{localStorage:{getItem:()=> 'project-a'}})
 h.state.userFormOpen.value=true
 Object.assign(h.state.userForm,{username:'synthetic-user',nickname:'合成用户',department_id:'department-a',password:'synthetic-password-only',role_ids:[]})
 const saving=h.state.createUser();await tick();h.stale();h.respond(201);await saving
 assert.equal(h.state.userFormOpen.value,false)
 assert.equal(h.state.userForm.password,'')
 assert.equal(h.state.userForm.username,'')
 assert.equal(h.state.saving.value,false)
 assert.deepEqual(h.notifications.map(x=>x.kind),['info'])
 assert.equal(h.calls.filter(x=>x.method==='post').length,1)
})
for(const status of [201,'ack'])test(`创建用户旧${status}回执不关闭同身份重新打开的新表单`,async t=>{
 const h=view(t,'../src/views/AccessView.vue',{localStorage:{getItem:()=> 'project-a'}})
 h.state.userFormOpen.value=true
 Object.assign(h.state.userForm,{username:'old-user',department_id:'department-a',password:'synthetic-old-password'})
 const saving=h.state.createUser();await tick()
 h.state.userFormOpen.value=false;h.state.resetUser();h.state.userFormOpen.value=true
 Object.assign(h.state.userForm,{username:'new-draft',department_id:'department-a',password:'synthetic-new-password'})
 if(status==='ack')h.stale()
 h.respond(201);await saving
 assert.equal(h.state.userFormOpen.value,true)
 assert.equal(h.state.userForm.username,'new-draft')
 assert.equal(h.state.userForm.password,'synthetic-new-password')
 assert.deepEqual(h.notifications,[])
 assert.equal(h.calls.filter(x=>x.method==='post').length,1)
})
for(const failure of ['forbidden','network'])test(`创建用户${failure}未确认受理时保留原稿且不重试`,async t=>{
 const h=view(t,'../src/views/AccessView.vue',{localStorage:{getItem:()=> 'project-a'}})
 h.state.userFormOpen.value=true
 Object.assign(h.state.userForm,{username:'synthetic-user',department_id:'department-a',password:'synthetic-password-only'})
 const saving=h.state.createUser();await tick()
 h.pending.reject(new axios.AxiosError('合成失败',failure==='network'?'ERR_NETWORK':'ERR_BAD_REQUEST',h.pending.config,undefined,failure==='forbidden'?{status:403,data:{message:'合成拒绝'},config:h.pending.config}:undefined))
 await saving
 assert.equal(h.state.userFormOpen.value,true)
 assert.equal(h.state.userForm.password,'synthetic-password-only')
 assert.equal(h.state.saving.value,false)
 assert.deepEqual(h.notifications.map(x=>x.kind),['error'])
 assert.equal(h.calls.filter(x=>x.method==='post').length,1)
})
test('创建用户进行中重复确认只发送一次，普通201成功正常关闭并清空',async t=>{
 const h=view(t,'../src/views/AccessView.vue',{localStorage:{getItem:()=> 'project-a'}})
 h.state.userFormOpen.value=true
 Object.assign(h.state.userForm,{username:'synthetic-user',department_id:'department-a',password:'synthetic-password-only'})
 const saving=h.state.createUser();await tick();await h.state.createUser()
 assert.equal(h.calls.filter(x=>x.method==='post').length,1)
 h.respond(201);await saving
 assert.equal(h.state.userFormOpen.value,false)
 assert.equal(h.state.userForm.password,'')
 assert.deepEqual(h.notifications.map(x=>x.kind),['success'])
})
for(const changed of ['id','department_id','project_id','identity-return','unmount'])test(`创建用户${changed}后旧ACK不清理新稿或通知`,async t=>{
 const h=view(t,'../src/views/AccessView.vue',{localStorage:{getItem:()=> 'project-a'}})
 h.state.userFormOpen.value=true
 Object.assign(h.state.userForm,{username:'synthetic-user',department_id:'department-a',password:'synthetic-password-only'})
 const saving=h.state.createUser();await tick();h.stale()
 if(changed==='unmount')h.dispose()
 else if(changed==='identity-return'){h.identity.user.id='other';h.identity.user.id='actor-a'}
 else h.identity.user[changed]='other'
 h.state.userFormOpen.value=true
 Object.assign(h.state.userForm,{username:'new-draft',password:'synthetic-new-password'})
 h.respond(201);await saving
 assert.equal(h.state.userFormOpen.value,true)
 assert.equal(h.state.userForm.username,'new-draft')
 assert.equal(h.state.userForm.password,'synthetic-new-password')
 assert.deepEqual(h.notifications,[])
 assert.equal(h.calls.filter(x=>x.method==='post').length,1)
})
test('真实 ProjectsView 保存遇到 changed→PUT200，不得显示普通失败',async t=>{
 const h=view(t,'../src/views/ProjectsView.vue')
 h.state.edit({id:'project-a',name:'本轮合成项目',description:'',department_id:'department-a',is_active:true})
 const saving=h.state.save();await tick();h.stale();h.respond(200);await saving
 assert.equal(h.notifications.filter(item=>item.kind==='error').length,0)
 assert.equal(h.calls.filter(item=>item.method==='put').length,1)
 assert.equal(h.state.selected.value,null)
})
test('确认删除已经204受理，changed不能让原确认回调冒充提交失败或重放',async t=>{
 const h=view(t,'../src/views/AccessView.vue')
 h.state.removeRole({id:'role-a',display_name:'测试角色',permissions:[],in_use:false})
 const confirm=h.confirmations[0],saving=confirm.onOk();await tick();h.stale();h.respond(204)
 await assert.doesNotReject(saving)
 assert.equal(h.notifications.filter(item=>item.kind==='error').length,0)
 assert.equal(h.calls.filter(item=>item.method==='delete').length,1)
})
test('对照: PUT200完整处理后再变更权限，正常保存仍成功且没有第二次写',async t=>{
 const h=view(t,'../src/views/AccessView.vue')
 h.state.editRolePermissions({id:'role-a',name:'role-a',display_name:'测试角色',description:'',permissions:[]})
 const saving=h.state.saveRole();await tick();h.respond(200);await saving;h.stale()
 assert.equal(h.notifications.filter(item=>item.kind==='success').length,1)
 assert.equal(h.notifications.filter(item=>item.kind==='error').length,0)
 assert.equal(h.calls.filter(item=>item.method==='put').length,1)
})
test('对照: 旧GET200正文仍须拒绝，不可因受理语义放行敏感数据',async()=>{
 const h=transport();let finish
 h.module.default.defaults.adapter=config=>new Promise(resolve=>{finish=()=>resolve({status:200,statusText:'OK',headers:{},config,data:{marker:'synthetic-sensitive-read'}})})
 const request=h.module.default.get('/audit-logs');await tick();h.module.setAuthorizationFreshness(false);finish()
 await assert.rejects(request,error=>error.code==='EDO_AUTHORIZATION_CHANGED'&&error.response===undefined)
})
test('对照: 网络结果未知仍不能当已受理，也不能重放',async()=>{
 const h=transport(),request=h.module.default.put('/roles/role-a/permissions',{permissions:[]});await tick()
 h.pending.reject(new axios.AxiosError('合成网络失败','ERR_NETWORK',h.pending.config))
 await assert.rejects(request,error=>error.code==='ERR_NETWORK')
 assert.equal(h.calls.length,1)
})
test('权限核验错误不能经共享文案被误报成API断网',()=>{
 const h=transport()
 for(const code of ['EDO_AUTHORIZATION_PENDING','EDO_AUTHORIZATION_CHANGED'])assert.notEqual(h.resources.apiErrorMessage(new axios.AxiosError('合成授权状态',code)),'无法连接 EDO API')
})

test('真实Organization保存成功200遇到授权通知，不得误报普通失败',async t=>{
 const h=view(t,'../src/views/OrganizationView.vue')
 h.state.edit({id:'department-a',name:'合成部门',description:'',parent_id:''})
 const saving=h.state.saveDepartment();await tick();h.stale();h.respond(200);await saving
 assert.equal(h.notifications.filter(item=>item.kind==='error').length,0)
 assert.equal(h.calls.filter(item=>item.method==='put').length,1)
})
test('Organization确认删除204遇到changed，不得reject为未知写',async t=>{
 const h=view(t,'../src/views/OrganizationView.vue')
 h.state.remove({id:'department-a',name:'合成部门',member_count:0,is_default:false})
 const confirm=h.confirmations[0],saving=confirm.onOk();await tick();h.stale();h.respond(204)
 await assert.doesNotReject(saving)
 assert.equal(h.calls.filter(item=>item.method==='delete').length,1)
})
test('已变化授权的自有code不能被共享文案映射成断网',()=>{
 const h=transport()
 assert.notEqual(h.resources.apiErrorMessage(new axios.AxiosError('合成授权状态','EDO_AUTHORIZATION_CHANGED')),'无法连接 EDO API')
})
test('对照: stale后的真实网络未知仍不能记为已受理或重放',async()=>{
 const h=transport(),request=h.module.default.put('/roles/role-a/permissions',{permissions:[]});await tick()
 h.module.setAuthorizationFreshness(false)
 h.pending.reject(new axios.AxiosError('合成网络未知','ERR_NETWORK',h.pending.config))
 await assert.rejects(request,error=>error.code!=='EDO_MUTATION_ACKNOWLEDGED')
 assert.equal(h.calls.length,1)
})
test('对照: 确定403拒绝仍保持拒绝，不能转换成已受理',async()=>{
 const h=transport(),request=h.module.default.put('/roles/role-a/permissions',{permissions:[]});await tick()
 h.pending.reject(new axios.AxiosError('合成拒绝','ERR_BAD_REQUEST',h.pending.config,undefined,{status:403,data:{code:'permission_denied',message:'合成拒绝'},config:h.pending.config}))
 await assert.rejects(request,error=>error.response?.status===403&&error.code!=='EDO_MUTATION_ACKNOWLEDGED')
 assert.equal(h.calls.length,1)
})

for(const method of ['post','put','patch','delete'])for(const status of [200,201,202,204])test(`${method.toUpperCase()} ${status}旧epoch只返回无payload受理信号`,async()=>{
 const h=transport(),request=h.module.default.request({method,url:'/roles/role-a/permissions',data:{marker:'synthetic-request-not-for-ui'}});await tick()
 h.module.setAuthorizationFreshness(false);h.respond(status)
 await assert.rejects(request,error=>{
  assert.equal(error.code,'EDO_MUTATION_ACKNOWLEDGED')
  for(const key of ['config','request','response'])assert.equal(error[key],undefined)
  assert.equal(JSON.stringify(error).includes('synthetic-'),false)
  return true
 })
 assert.equal(h.calls.length,1)
})

for(const method of ['get','head','options'])test(`${method.toUpperCase()}旧epoch绝不套用写受理语义`,async()=>{
 const h=transport()
 h.module.default.defaults.adapter=config=>new Promise(resolve=>{h.resolve=()=>resolve({status:200,statusText:'OK',headers:{},config,data:{marker:'synthetic-read-not-for-ui'}})})
 const request=h.module.default.request({method,url:'/audit-logs'});await tick()
 h.module.setAuthorizationFreshness(false);h.resolve()
 await assert.rejects(request,error=>error.code==='EDO_AUTHORIZATION_CHANGED'&&error.response===undefined)
})

for(const status of [400,403,409,500])test(`fulfilled异常${status}不是成功回执`,async()=>{
 const h=transport(),request=h.module.default.put('/roles/role-a/permissions',{});await tick()
 h.module.setAuthorizationFreshness(false);h.respond(status)
 await assert.rejects(request,error=>error.code==='EDO_AUTHORIZATION_CHANGED'&&error.response===undefined)
})

for(const status of [undefined,NaN,'200',200.5])test(`非真实整数状态${String(status)}不转换成受理`,async()=>{
 const h=transport(),request=h.module.default.put('/roles/role-a/permissions',{});await tick()
 h.module.setAuthorizationFreshness(false)
 h.pending.resolve({status,statusText:'synthetic',headers:{},config:h.pending.config,data:{marker:'synthetic-invalid-status'}})
 await assert.rejects(request,error=>error.code!=='EDO_MUTATION_ACKNOWLEDGED'&&error.response===undefined)
})

test('rejected分支即使带200也不能推定完整响应已受理',async()=>{
 const h=transport(),request=h.module.default.put('/roles/role-a/permissions',{});await tick()
 h.module.setAuthorizationFreshness(false)
 h.pending.reject(new axios.AxiosError('合成未完整响应','ERR_BAD_RESPONSE',h.pending.config,undefined,{status:200,data:{marker:'synthetic-partial'},config:h.pending.config}))
 await assert.rejects(request,error=>error.code!=='EDO_MUTATION_ACKNOWLEDGED'&&error.response===undefined)
})

test('pending阶段没有发送写不能称为已受理，认证请求仍沿原认证流程',async()=>{
 const h=transport();h.module.setAuthorizationFreshness(false)
 await assert.rejects(h.module.default.put('/roles/role-a/permissions',{}),error=>error.code==='EDO_AUTHORIZATION_PENDING')
 assert.equal(h.calls.length,0)
 const login=h.module.default.post('/auth/login',{username:'unit-only',password:'not-a-real-secret'});await tick();h.respond(200)
 assert.equal((await login).data.marker,'synthetic-old-response-not-for-ui')
})

const pages=[['AccessView','../src/views/AccessView.vue'],['ProjectsView','../src/views/ProjectsView.vue'],['OrganizationView','../src/views/OrganizationView.vue']]
function startSave(h,page){
 if(page==='AccessView'){h.state.editRolePermissions({id:'role-a',name:'role-a',display_name:'合成角色',description:'',permissions:[]});return h.state.saveRole()}
 if(page==='ProjectsView'){h.state.edit({id:'project-a',name:'合成项目',description:'',department_id:'department-a',is_active:true});return h.state.save()}
 h.state.edit({id:'department-a',name:'合成部门',description:'',parent_id:''});return h.state.saveDepartment()
}
for(const [page,path]of pages)for(const changed of ['id','department_id','project_id','unmount','identity-return'])test(`${page} ${changed}后旧受理不弹toast、不恢复旧上下文`,async t=>{
 const h=view(t,path),saving=startSave(h,page);await tick();h.stale()
 if(changed==='unmount')h.dispose()
 else if(changed==='identity-return'){h.identity.user.id='other';h.identity.user.id='actor-a'}
 else h.identity.user[changed]='other'
 h.respond(200);await saving
 assert.deepEqual(h.notifications,[])
 assert.equal(h.calls.filter(item=>item.method!=='get').length,1)
})
for(const [page,path]of pages)test(`${page}同用户权限数组改变仍准确显示受理而非失败`,async t=>{
 const h=view(t,path),saving=startSave(h,page);await tick();h.stale()
 h.identity.user.permissions=['audit.read']
 h.respond(200);await saving
 assert.deepEqual(h.notifications.map(item=>item.kind),['info'])
 assert.equal(h.calls.filter(item=>item.method!=='get').length,1)
 assert.equal(h.state.saving.value,false)
})

for(const [page,path]of pages)test(`${page}确认的真实403继续reject，不冒充受理`,async t=>{
 const h=view(t,path),row={id:'role-a',name:'合成对象',display_name:'合成对象',permissions:[],in_use:false,member_count:0,is_default:false}
 if(page==='AccessView')h.state.removeRole(row);else h.state.remove(row)
 const saving=h.confirmations[0].onOk();await tick()
 h.pending.reject(new axios.AxiosError('合成拒绝','ERR_BAD_REQUEST',h.pending.config,undefined,{status:403,data:{message:'合成拒绝'},config:h.pending.config}))
 await assert.rejects(saving)
 assert.deepEqual(h.notifications.map(item=>item.kind),['error'])
 assert.equal(h.calls.filter(item=>item.method!=='get').length,1)
})

test('项目成员权限确认收到ACK后resolve且不恢复旧详情',async t=>{
 const h=view(t,'../src/views/ProjectsView.vue')
 h.state.selected.value={project:{id:'project-a',name:'合成项目'},members:[{user_id:'target',username:'合成成员',role_id:'role-a',role_name:'合成角色'}],users:[],roles:[{id:'role-a',display_name:'合成角色',permissions:[]}],permissions:[],catalog:[]}
 h.state.assign('target','role-a')
 const saving=h.confirmations[0].onOk();await tick();h.stale();h.respond(200)
 await assert.doesNotReject(saving)
 assert.equal(h.state.selected.value,null)
 assert.deepEqual(h.notifications.map(item=>item.kind),['info'])
 assert.equal(h.calls.filter(item=>item.method!=='get').length,1)
})

test('Organization ACK后只在ready恢复有界只读目录，不留下已删除旧行',async t=>{
 const h=view(t,'../src/views/OrganizationView.vue')
 h.state.rows.value=[{id:'deleted',name:'旧行',parent_id:''}]
 h.state.remove({id:'deleted',name:'旧行',member_count:0,is_default:false})
 const saving=h.confirmations[0].onOk();await tick();h.stale();h.respond(204);await saving
 assert.deepEqual(h.state.rows.value,[])
 assert.equal(h.calls.filter(item=>item.method==='get').length,0)
 h.module.setAuthorizationFreshness(true);h.identity.authorizationState='ready';await tick()
 assert.deepEqual(h.state.rows.value,[])
 assert.deepEqual(h.calls.filter(item=>item.method==='get').map(item=>item.path).sort(),['/departments','/organization','/users'])
 assert.equal(h.calls.filter(item=>item.method!=='get').length,1)
})

test('Organization旧在途目录不能覆盖恢复后的新目录，卸载后不继续读取',async t=>{
 const h=view(t,'../src/views/OrganizationView.vue'),reads=[]
 h.module.default.defaults.adapter=config=>new Promise(resolve=>reads.push({config,resolve}))
 const old=h.state.load();await tick();assert.equal(reads.length,3)
 h.stale();h.module.setAuthorizationFreshness(true);h.identity.authorizationState='ready';await tick()
 assert.equal(reads.length,6)
 const respond=(index,name)=>reads[index].resolve({status:200,statusText:'OK',headers:{},config:reads[index].config,data:{organization:{name},departments:[{id:name,name,parent_id:''}],users:[{id:`${name}-member`,username:name,department_id:name}]}})
 respond(0,'old');respond(1,'old');respond(2,'old');await old
 assert.deepEqual(h.state.rows.value,[])
 assert.deepEqual(h.state.members.value,[])
 respond(3,'fresh');respond(4,'fresh');respond(5,'fresh');await tick()
 assert.deepEqual(h.state.rows.value.map(row=>row.id),['fresh'])
 assert.equal(h.state.company.name,'fresh')
 assert.deepEqual(h.state.members.value.map(row=>row.id),['fresh-member'])
 h.dispose();await h.state.load();assert.equal(reads.length,6)
})
