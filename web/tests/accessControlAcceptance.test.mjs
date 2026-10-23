import assert from 'node:assert/strict'
import {test} from 'node:test'
import {componentHarness} from './acceptanceHarness.mjs'

const catalog=[
  {code:'application.read',scope:'project',group:'发布',resource:'application',resource_name:'应用',action:'read',action_name:'查看',name:'查看应用',description:'查看项目应用'},
  {code:'application.create',scope:'project',group:'发布',resource:'application',resource_name:'应用',action:'create',action_name:'创建',name:'创建应用',description:'创建项目应用'},
  {code:'user.read',scope:'platform',group:'安全',resource:'user',resource_name:'用户',action:'read',action_name:'查看',name:'查看用户',description:'查看用户'},
  {code:'user.delete',scope:'platform',group:'安全',resource:'user',resource_name:'用户',action:'delete',action_name:'删除',name:'删除用户',description:'删除用户',dangerous:true},
  {code:'role.update',scope:'platform',group:'安全',resource:'role',resource_name:'角色',action:'update',action_name:'修改',name:'修改角色',description:'修改角色'},
]
const role=(id,permissions=[])=>({id,name:id,display_name:'角色 '+id,description:'说明',permissions,in_use:false})
const user=(id='member')=>({id,username:id,nickname:'成员',department_id:'department-a',is_superuser:false,is_active:true,role_ids:[],permission_overrides:{allow:[],deny:[]},effective_permissions:[]})
function access(t,options={}) {
  const h=componentHarness('../src/views/AccessView.vue',{codes:['role.read','role.create','role.update','role.delete','user.read','user.update',...catalog.map(item=>item.code)],query:{view:'roles'},...options})
  h.state.permissions.value=structuredClone(catalog)
  t.after(h.stop)
  return h
}
const writes=h=>h.requests.filter(item=>item.method!=='get')

test('访问控制页签与数据读取逐项按权限决定，审计只读不会加载用户角色和凭据',async t=>{
  const h=access(t,{codes:['audit.read'],query:{view:'roles'}})
  assert.deepEqual(h.state.tabs.value.map(item=>item.key),['audit'])
  assert.equal(h.state.active.value,'audit')
  await h.state.refresh()
  assert.deepEqual(h.requests.map(item=>item.path),['/audit-logs?limit=100'])
  assert.deepEqual(h.state.users.value,[])
  assert.deepEqual(h.state.roles.value,[])
  assert.equal(h.state.canDelegatePermission('user.delete'),false)
})

test('创建角色提交原子权限集，成功后关闭并清空表单且刷新目录',async t=>{
  const h=access(t)
  h.state.editingRoleID.value='old';h.state.roleForm.permissions=['old.permission']
  h.state.createRole()
  assert.equal(h.state.roleFormOpen.value,true)
  assert.deepEqual(h.state.roleForm.permissions,[])
  Object.assign(h.state.roleForm,{name:'reviewer',display_name:'审核员',description:'只读审核',permissions:['user.read']})
  await h.state.saveRole()
  assert.deepEqual(writes(h),[{method:'post',path:'/roles',body:{name:'reviewer',display_name:'审核员',description:'只读审核',permissions:['user.read']},options:undefined}])
  assert.equal(h.state.roleFormOpen.value,false)
  assert.equal(h.state.saving.value,false)
  assert.deepEqual(h.state.roleForm.permissions,[])
  assert.ok(h.requests.some(item=>item.path==='/permissions'))
})

test('角色基本信息和权限保存使用独立接口，不互相覆盖字段',async t=>{
  const h=access(t),item=role('role-a',['user.read'])
  h.state.editRoleBasic(item)
  h.state.roleForm.display_name='新名称'
  h.state.roleForm.permissions.push('user.delete')
  assert.deepEqual(item.permissions,['user.read'])
  await h.state.saveRole()
  assert.deepEqual(writes(h)[0],{method:'patch',path:'/roles/role-a/basic',body:{name:'role-a',display_name:'新名称',description:'说明'},options:undefined})
  h.state.editRolePermissions(item)
  h.state.roleForm.permissions=['user.read','user.delete']
  await h.state.saveRole()
  assert.deepEqual(writes(h)[1],{method:'put',path:'/roles/role-a/permissions',body:{permissions:['user.read','user.delete']},options:undefined})
})

test('角色保存被拒绝时保留抽屉及当前输入，不显示成功消息',async t=>{
  const h=access(t,{api:{put:async()=>{throw new Error('权限已变化')}}})
  h.state.editRolePermissions(role('role-a',['user.read']))
  h.state.roleForm.permissions=['user.read','user.delete']
  await h.state.saveRole()
  assert.equal(h.state.roleFormOpen.value,true)
  assert.deepEqual(h.state.roleForm.permissions,['user.read','user.delete'])
  assert.equal(h.state.saving.value,false)
  assert.deepEqual(h.notifications,[{kind:'error',value:'权限已变化'}])
})

test('权限分组批量勾选只改变可委派项，半选状态正确且不修改原角色',t=>{
  const h=access(t,{codes:['role.update','user.read']})
  const resource={key:'user',name:'用户',items:catalog.filter(item=>item.resource==='user')}
  h.state.roleForm.permissions=[]
  assert.equal(h.state.roleResourceChecked(resource),false)
  h.state.toggleRoleResource(resource,true)
  assert.deepEqual(h.state.roleForm.permissions,['user.read'])
  assert.equal(h.state.roleResourceChecked(resource),true)
  assert.equal(h.state.roleResourceIndeterminate(resource),false)
  h.state.toggleRoleResource(resource,false)
  assert.deepEqual(h.state.roleForm.permissions,[])
  const projectResource={key:'application',name:'应用',items:catalog.filter(item=>item.resource==='application')}
  assert.equal(h.state.roleResourceChecked(projectResource),false)
  h.state.toggleRoleResource(projectResource,true)
  assert.deepEqual(h.state.roleForm.permissions,[])
})

test('平台角色选项排除纯项目角色和超出可委派范围的角色，自身访问不可提升',t=>{
  const h=access(t,{codes:['user.update','user.read']})
  h.state.roles.value=[role('platform',['user.read']),role('project',['application.read']),role('too-strong',['user.delete'])]
  assert.deepEqual(h.state.delegableRoleOptions.value.map(item=>[item.value,item.disabled]),[['platform',false],['project',true],['too-strong',true]])
  assert.equal(h.state.canManageUserAccess({...user('operator'),role_ids:['platform']}),false)
  assert.equal(h.state.canManageUserAccess({...user(),role_ids:['platform']}),true)
  assert.equal(h.state.canManageUserAccess({...user(),role_ids:['too-strong']}),false)
  assert.equal(h.state.canManageRolePermissions(role('too-strong',['user.delete'])),false)
})

test('用户级拒绝优先于角色允许，继承不产生额外规则，权限覆盖原子提交',async t=>{
  const h=access(t)
  h.state.roles.value=[role('reader',['user.read']),role('manager',['user.delete'])]
  const target={...user(),role_ids:['reader'],permission_overrides:{allow:['role.update'],deny:['user.read']}}
  h.state.editAccess(target)
  assert.equal(h.state.accessRolePermissionCodes.value.has('user.read'),true)
  assert.equal(h.state.accessEffectivePermissionCodes.value.has('user.read'),false)
  assert.equal(h.state.accessEffectivePermissionCodes.value.has('role.update'),true)
  h.state.accessForm.role_ids.push('manager')
  assert.deepEqual(target.role_ids,['reader'])
  h.state.accessForm.effects['role.update']='inherit'
  await h.state.saveAccess()
  assert.deepEqual(writes(h)[0],{method:'put',path:'/users/member/access',body:{role_ids:['reader','manager'],allow:[],deny:['user.read']},options:undefined})
  assert.equal(h.state.accessOpen.value,false)
})

test('用户权限保存失败保留目标用户和覆盖草稿，不关闭抽屉',async t=>{
  const h=access(t,{api:{put:async()=>{throw new Error('不能修改该用户')}}})
  h.state.editAccess(user('exact-user'))
  h.state.accessForm.effects['user.read']='deny'
  await h.state.saveAccess()
  assert.equal(h.state.accessOpen.value,true)
  assert.equal(h.state.editingUser.value.id,'exact-user')
  assert.equal(h.state.accessForm.effects['user.read'],'deny')
  assert.equal(h.state.saving.value,false)
  assert.deepEqual(h.notifications,[{kind:'error',value:'不能修改该用户'}])
})

test('角色删除先确认并禁止仍在使用的角色，成功才刷新角色列表',async t=>{
  const h=access(t)
  h.state.removeRole({...role('busy'),in_use:true,visible_member_count:2})
  assert.equal(h.confirmations[0].okButtonProps.disabled,true)
  assert.equal(writes(h).length,0)
  h.state.removeRole(role('unused'))
  assert.equal(h.confirmations[1].okButtonProps.disabled,false)
  await h.confirmations[1].onOk()
  assert.equal(writes(h)[0].path,'/roles/unused')
  assert.equal(writes(h)[0].method,'delete')
})

test('用户停用与删除分别确认，接口失败向确认层抛出且不显示成功',async t=>{
  const h=access(t,{api:{delete:async()=>{throw new Error('用户仍被引用')},patch:async()=>{throw new Error('禁止修改用户')}}})
  h.state.removeUser(user('exact-user'))
  assert.equal(writes(h).length,0)
  await assert.rejects(h.confirmations[0].onOk(),/用户仍被引用/)
  h.state.toggleUser(user('exact-user'))
  await assert.rejects(h.confirmations[1].onOk(),/禁止修改用户/)
  assert.deepEqual(writes(h).map(item=>[item.method,item.path,item.body]),[['delete','/users/exact-user',undefined],['patch','/users/exact-user/status',{active:false}]])
  assert.equal(h.notifications.some(item=>item.kind==='success'),false)
})

test('创建用户采用操作者部门，分页加载完整成员后角色和状态筛选组合生效',async t=>{
  const first=Array.from({length:200},(_,i)=>({...user('member-'+i),nickname:'成员'+i,department_name:'研发',role_ids:i===0?['reader']:[]}))
  const last={...user('last'),nickname:'最后一位',is_active:false,department_name:'财务',role_ids:['reader']}
  const h=access(t,{api:{get:async path=>path.startsWith('/users?')?{data:{users:path.endsWith('offset=0')?first:[last]}}:{data:{}}}})
  h.state.users.value=await h.state.loadAllUsers()
  h.state.roles.value=[role('reader',['user.read'])]
  assert.equal(h.state.users.value.length,201)
  assert.deepEqual(h.requests.filter(item=>item.path.startsWith('/users?')).map(item=>item.path),['/users?limit=200&offset=0','/users?limit=200&offset=200'])
  h.state.userRoleID.value='reader';h.state.userStatus.value='inactive';h.state.userQuery.value='最后'
  assert.deepEqual(h.state.filteredUsers.value.map(item=>item.id),['last'])
  h.state.resetUser()
  assert.equal(h.state.userForm.department_id,'department-a')
})
