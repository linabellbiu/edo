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

test('部门树按真实上下级组织，搜索下级保留完整路径且不改变折叠状态',async t=>{
  const h=harness(t,'../src/views/OrganizationView.vue',{codes:['department.read']})
  const department=(id,name,parent_id='')=>({id,name,parent_id,description:'',member_count:0,is_default:false})
  h.state.rows.value=[department('backend','后端组','engineering'),department('support','客户支持'),department('engineering','研发部'),department('payments','支付服务组','backend')]
  await Promise.resolve()
  const root=h.state.treeData.value[0]
  assert.deepEqual(root.children.map(node=>node.key),['support','engineering'])
  assert.equal(root.children[1].children[0].children[0].title,'支付服务组')
  h.state.expandNodes(['company','engineering'])
  h.state.query.value=' 支付 '
  assert.equal(h.state.treeData.value[0].children.length,1)
  assert.equal(h.state.treeData.value[0].children[0].children[0].children[0].key,'payments')
  assert.ok(h.state.visibleExpandedKeys.value.includes('backend'))
  h.state.query.value=''
  assert.deepEqual(h.state.visibleExpandedKeys.value,['company','engineering'])
  assert.equal(h.state.rows.value.length,4)
})

test('节点添加下级自动绑定该部门，修改仍指向原部门，公司节点添加直属部门',async t=>{
  const h=harness(t,'../src/views/OrganizationView.vue')
  const parent={id:'parent',name:'研发部',parent_id:'',description:'研发说明',member_count:2,is_default:false}
  h.state.rows.value=[parent]
  h.state.addDepartment(parent.id)
  assert.equal(h.state.form.id,'');assert.equal(h.state.form.parent_id,'parent')
  assert.deepEqual(h.state.parentOptions.value.map(option=>option.value),['','parent'])
  assert.equal(h.state.parentOptions.value.find(option=>option.value==='parent').label,'研发部')
  h.state.form.name='后端组';await h.state.saveDepartment()
  assert.equal(writes(h)[0].path,'/departments');assert.equal(writes(h)[0].body.parent_id,'parent')
  h.state.edit(parent);h.state.form.name='产品研发部';await h.state.saveDepartment()
  assert.equal(writes(h)[1].path,'/departments/parent');assert.equal(writes(h)[1].body.parent_id,'')
  h.state.addDepartment()
  assert.equal(h.state.form.id,'');assert.equal(h.state.form.parent_id,'');assert.equal(h.state.form.name,'')
})

test('默认部门连续编辑与添加下级时，上级名称独立于候选缓存且始终使用字符串 ID 保存',async t=>{
  const id='00000000-0000-0000-0000-000000000001'
  const h=harness(t,'../src/views/OrganizationView.vue')
  const parent={id,name:'默认部门',parent_id:'',description:'',member_count:1,is_default:true}
  h.state.rows.value=[parent,{id:'child',name:'下级部门',parent_id:id,description:'',member_count:0,is_default:false}]
  h.state.edit(parent)
  assert.deepEqual(h.state.parentSelection.value,{value:'',label:'workspace.root'})
  h.state.addDepartment(id)
  assert.deepEqual(h.state.parentSelection.value,{value:id,label:'默认部门'})
  assert.equal(h.state.parentValid.value,true)
  h.state.rows.value=[{...parent,name:'默认部门新名称'},h.state.rows.value[1]]
  assert.equal(h.state.parentSelection.value.label,'默认部门新名称')
  h.state.selectParent({value:'child',label:'下级部门'})
  assert.equal(h.state.form.parent_id,'child')
  h.state.selectParent({value:id,label:'缓存中的旧名'})
  h.state.form.name='新部门'
  await h.state.saveDepartment()
  assert.equal(writes(h)[0].body.parent_id,id)
})

test('不可查看的上级保留原关联，不回显 UUID；失效关联由服务端拒绝，循环上级不可选',async t=>{
  const h=harness(t,'../src/views/OrganizationView.vue',{api:{put:async()=>{throw new Error('上级部门不可用')}}})
  const row={id:'self',name:'本部门',parent_id:'missing',description:'',member_count:0,is_default:false}
  h.state.rows.value=[row,{...row,id:'child',name:'下级',parent_id:'self'}]
  h.state.edit(row)
  assert.deepEqual(h.state.parentSelection.value,{value:'missing',label:'workspace.parentUnavailable'})
  await h.state.saveDepartment()
  assert.equal(writes(h)[0].body.parent_id,'missing')
  assert.equal(h.state.departmentOpen.value,true);assert.equal(h.state.form.parent_id,'missing')
  h.state.selectParent({value:'self',label:'本部门'})
  assert.equal(h.state.form.parent_id,'missing')
  h.state.form.parent_id='self'
  assert.equal(h.state.parentValid.value,false)
  await h.state.saveDepartment();assert.equal(writes(h).length,1)
  h.state.selectParent({value:'child',label:'下级'})
  assert.equal(h.state.form.parent_id,'self')
  h.state.selectParent({value:'',label:'公司直属'})
  assert.equal(h.state.parentValid.value,true);assert.equal(h.state.form.parent_id,'')
})

test('部门树刷新保留收起的分支，新增节点可见，历史异常关系不丢失部门',async t=>{
  const h=harness(t,'../src/views/OrganizationView.vue')
  const rows=[{id:'parent',name:'研发',parent_id:''},{id:'child',name:'后端',parent_id:'parent'}]
  h.state.rows.value=rows;await Promise.resolve()
  h.state.expandNodes(['company'])
  h.state.rows.value=rows.map(row=>({...row}));await Promise.resolve()
  assert.deepEqual(h.state.visibleExpandedKeys.value,['company'])
  h.state.rows.value=[{id:'a',name:'A',parent_id:'b'},{id:'b',name:'B',parent_id:'a'},{id:'orphan',name:'历史部门',parent_id:'missing'}]
  assert.deepEqual(h.state.treeData.value[0].children.map(node=>node.key),['a','b','orphan'])
})

test('默认部门排在首位，切换部门后右侧显示直属成员并支持账号搜索',async t=>{
  const departments=[{id:'team',name:'研发部',parent_id:'',is_default:false,member_count:1},{id:'default',name:'默认部门',parent_id:'',is_default:true,member_count:200}]
  const users=Array.from({length:201},(_,index)=>({id:`user-${index}`,username:`account-${index}`,nickname:index===200?'小林':'成员',department_id:index===200?'team':'default',is_active:index!==200}))
  const h=harness(t,'../src/views/OrganizationView.vue',{codes:['department.read','user.read'],api:{get:async(path,config)=>({data:path==='/departments'?{departments}:{users:users.slice(config.params.offset,config.params.offset+config.params.limit)}})}})
  await h.state.load()
  const root=h.state.treeData.value[0]
  assert.equal(root.children[0].key,'default');assert.equal(root.children[0].children.length,0)
  assert.equal(h.state.scopedMembers.value.length,201)
  h.state.selectOrganizationNode(['team'])
  assert.equal(h.state.currentTitle.value,'研发部')
  assert.equal(h.state.scopedMembers.value[0].nickname,'小林');assert.equal(h.state.scopedMembers.value[0].is_active,false)
  assert.deepEqual(h.requests.filter(row=>row.path==='/users').map(row=>row.body.params),[{view:'members',limit:200,offset:0},{view:'members',limit:200,offset:200}])
  h.state.memberQuery.value='account-200'
  assert.equal(h.state.filteredMembers.value.length,1)
  h.state.selectOrganizationNode(['default'])
  assert.equal(h.state.filteredMembers.value.length,0)
  h.state.selectOrganizationNode(['company'])
  assert.equal(h.state.filteredMembers.value[0].id,'user-200')
  assert.equal(h.state.treeData.value[0].children.length,2)
})

test('成员读取失败不影响部门树，无用户权限时不读取成员且不把缺失当作无人',async t=>{
  const department={id:'team',name:'研发部',parent_id:'',is_default:false,member_count:1}
  const reader=harness(t,'../src/views/OrganizationView.vue',{codes:['department.read'],api:{get:async()=>({data:{departments:[department]}})}})
  await reader.state.load()
  assert.deepEqual(reader.requests.map(row=>row.path),['/departments'])
  assert.equal(reader.state.memberDirectoryAllowed.value,false)
  const failed=harness(t,'../src/views/OrganizationView.vue',{codes:['department.read','user.read'],api:{get:async path=>{if(path==='/users')throw new Error('成员读取失败');return {data:{departments:[department]}}}}})
  await failed.state.load()
  assert.equal(failed.state.treeData.value[0].children[0].key,'team')
  assert.equal(failed.state.membersLoading.value,false);assert.equal(failed.state.membersError.value,'workspace.memberLoadFailed')
})

test('成员读取中身份变化后丢弃旧成员，不继续翻页',async t=>{
  let finish
  const h=harness(t,'../src/views/OrganizationView.vue',{codes:['department.read','user.read'],api:{get:async path=>path==='/users'?new Promise(resolve=>{finish=resolve}):{data:{departments:[]}}}})
  const pending=h.state.load()
  h.auth.user.id='another-user'
  finish({data:{users:Array.from({length:200},(_,index)=>({id:`old-${index}`,username:'旧身份成员',department_id:'old'}))}})
  await pending
  assert.deepEqual(h.state.members.value,[])
  assert.equal(h.requests.filter(row=>row.path==='/users').length,1)
})

test('公司编辑使用独立草稿，取消或保存失败不改变已显示公司资料',async t=>{
  const h=harness(t,'../src/views/OrganizationView.vue',{api:{put:async()=>{throw new Error('公司名称不可用')}}})
  Object.assign(h.state.company,{name:'原公司',description:'原说明'})
  h.state.editCompany();h.state.companyForm.name='未保存名称';h.state.companyOpen.value=false
  assert.equal(h.state.currentTitle.value,'原公司');assert.equal(h.state.company.name,'原公司')
  h.state.editCompany();assert.equal(h.state.companyForm.name,'原公司')
  h.state.companyForm.name='保留草稿';await h.state.saveCompany()
  assert.equal(h.state.companyOpen.value,true);assert.equal(h.state.companyForm.name,'保留草稿')
  assert.equal(h.state.company.name,'原公司')
})

test('新建部门保存后准确选中，刷新保留选择，失效目标回到公司而不替换成另一部门',async t=>{
  const departments=[{id:'parent',name:'研发',description:'',parent_id:'',member_count:0,is_default:false}]
  const h=harness(t,'../src/views/OrganizationView.vue',{codes:['department.create','department.read'],api:{
    get:async()=>({data:{departments:departments.map(row=>({...row}))}}),
    post:async(_path,body)=>{const department={...body,id:'created',member_count:0,is_default:false};departments.push(department);return {data:{department}}},
  }})
  await h.state.load();h.state.addDepartment('parent');h.state.form.name='后端组';await h.state.saveDepartment()
  assert.equal(h.state.selected.value.id,'created');assert.equal(h.state.currentTitle.value,'后端组')
  departments[1].name='服务组';await h.state.load()
  assert.equal(h.state.selected.value.id,'created');assert.equal(h.state.currentTitle.value,'服务组')
  h.state.selectOrganizationNode(['unknown']);assert.equal(h.state.selected.value.id,'created')
  h.state.selectOrganizationNode([]);assert.equal(h.state.selected.value.id,'created')
  departments.pop();await h.state.load();assert.equal(h.state.selected.value,null)
})

test('成员面板只展示直属成员，分页随筛选及部门切换复位，缺少成员不冒充零人',async t=>{
  const h=harness(t,'../src/views/OrganizationView.vue')
  h.state.rows.value=[{id:'parent',name:'研发',parent_id:'',member_count:1},{id:'child',name:'后端',parent_id:'parent',member_count:1}]
  h.state.members.value=[{id:'one',nickname:'小陈',username:'chen',department_id:'parent',is_active:true},{id:'two',nickname:'小李',username:'li',department_id:'child',is_active:false}]
  h.state.membersLoaded.value=true;h.state.selectOrganizationNode(['parent'])
  assert.deepEqual(h.state.scopedMembers.value.map(row=>row.id),['one'])
  h.state.memberPage.value=3;h.state.memberQuery.value='chen';await Promise.resolve()
  assert.equal(h.state.memberPage.value,1)
  h.state.selectOrganizationNode(['child']);await Promise.resolve()
  assert.equal(h.state.filteredMembers.value.length,0);assert.equal(h.state.membersMissing.value,false)
  h.state.members.value=[]
  assert.equal(h.state.membersMissing.value,true);assert.equal(h.state.memberCount.value,1)
})

test('公司和部门保存区分创建及更新，空名称不发送，接口失败保留编辑草稿',async t=>{
  const h=harness(t,'../src/views/OrganizationView.vue')
  await h.state.saveCompany();await h.state.saveDepartment();assert.equal(writes(h).length,0)
  Object.assign(h.state.company,{name:'公司名称',description:'公司说明'});h.state.editCompany()
  await h.state.saveCompany()
  assert.deepEqual(writes(h)[0],{method:'put',path:'/organization',body:{name:'公司名称',description:'公司说明'},options:undefined})
  assert.equal(h.state.companyOpen.value,false)
  h.state.rows.value=[{id:'parent-a',name:'上级部门',description:'',parent_id:'',member_count:0,is_default:false}]
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
  const h=harness(t,'../src/views/ProjectsView.vue',{api:{delete:async()=>{throw new Error('项目仍有关联资源')}},globals:{localStorage:{getItem:key=>stored.get(key)??null,setItem:(key,value)=>stored.set(key,value)}}})
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
