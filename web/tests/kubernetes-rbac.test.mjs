import assert from 'node:assert/strict'
import {test} from 'node:test'
import {readFileSync} from 'node:fs'
import * as vue from 'vue'
import {componentHarness,loadFrontend} from './acceptanceHarness.mjs'

const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
const clone=value=>JSON.parse(JSON.stringify(value))
const role={id:'role-a',name:'project-developer',display_name:'测试开发者',permissions:['kubernetes.workload.read','kubernetes.workload.update','kubernetes.node.read','kubernetes.config.read']}
const catalog=[
  {code:'kubernetes.workload.read',name:'查看工作负载',resource_scopes:['namespace']},
  {code:'kubernetes.workload.update',name:'修改工作负载',dangerous:true,resource_scopes:['namespace']},
  {code:'kubernetes.node.read',name:'查看节点',resource_scopes:['cluster']},
  {code:'kubernetes.config.read',name:'查看配置',resource_scopes:['namespace','cluster']},
  {code:'kubernetes.secret.read',name:'查看Secret内容',dangerous:true,resource_scopes:['namespace']},
]
const initial={cluster_id:'cluster-a',enabled:false,revision:3,status:'disabled',last_error:'',grants:[],roles:[role],catalog,namespaces:['test','prod']}
const proposal=input=>({grant:{id:'draft',...input},rules:[{namespace:input.namespace,cluster_scope:input.cluster_scope,rules:[{apiGroups:['apps'],resources:['deployments'],verbs:['get','list','watch']}]}],subjects:['edo:project-a:user-a:hash'],affected_users:2,warnings:[],revision:3})

function harness({codes=['kubernetes.rbac.read','kubernetes.rbac.update'],api={},cluster='cluster-a'}={}){
  const calls=[]
  const methods={
    getKubernetesRbac:async(id,signal)=>{calls.push({method:'get',id,signal});return clone(initial)},
    previewKubernetesRbacGrant:async(id,input,grant,signal)=>{calls.push({method:'preview',id,input:clone(input),grant,signal});return proposal(input)},
    saveKubernetesRbacGrant:async(id,input,grant,signal)=>{calls.push({method:'save',id,input:clone(input),grant,signal});return {...clone(initial),revision:4,grants:[{id:grant||'grant-new',...input}]}},
    deleteKubernetesRbacGrant:async(id,grant,version,signal)=>{calls.push({method:'delete',id,grant,version,signal});return {...clone(initial),revision:4}},
    setKubernetesRbacMode:async(id,enabled,version,signal)=>{calls.push({method:'mode',id,enabled,version,signal});return {...clone(initial),enabled,revision:4,status:enabled?'synced':'disabled'}},
    syncKubernetesRbac:async(id,version,signal)=>{calls.push({method:'sync',id,version,signal});return {...clone(initial),enabled:true,status:'synced'}},
    ...api,
  }
  const props=vue.reactive({open:false,cluster,roleId:'role-a'})
  const project={id:'project-a'}
  const auth=vue.reactive({user:{id:'operator',project_id:'project-a'},codes:[...codes],canAny(required){return required.some(code=>this.codes.includes(code))}})
  const ctx=componentHarness('../src/components/ClusterRbacDrawer.vue',{
    codes,props,modules:{'@/api/kubernetesRbac':methods,'@/stores/auth':{useAuthStore:()=>auth}},
    globals:{localStorage:{getItem:()=>project.id}},
  })
  return {...ctx,auth,calls,props,project,async open(){props.open=true;await settle()},async ready(){await this.open();ctx.state.configure();ctx.state.form.namespace='test';ctx.state.form.permissions=['kubernetes.workload.read'];await settle()}}
}

test('默认读取不启用或同步集群权限，角色权限和范围独立选择',async()=>{
  const ctx=harness();try{
    await ctx.ready()
    assert.equal(ctx.state.statusLabel.value,'未启用')
    assert.equal(ctx.calls.filter(item=>item.method!=='get').length,0)
    assert.deepEqual(ctx.state.permissionChoices.value.map(item=>item.code),['kubernetes.workload.read','kubernetes.workload.update','kubernetes.config.read'])
    assert.equal(ctx.state.canPreview.value,true)
    assert.equal(ctx.state.canSave.value,false)
  }finally{ctx.stop()}
})

test('保存必须经过当前版本预览，修改配置后丢弃旧预览',async()=>{
  const ctx=harness();try{
    await ctx.ready();await ctx.state.save()
    assert.equal(ctx.calls.some(item=>item.method==='save'),false)
    await ctx.state.checkGrant();assert.equal(ctx.state.canSave.value,true)
    ctx.state.form.namespace='prod';await settle()
    assert.equal(ctx.state.preview.value,undefined);assert.equal(ctx.state.canSave.value,false)
    await ctx.state.checkGrant();await ctx.state.save()
    const saved=ctx.calls.find(item=>item.method==='save')
    assert.equal(saved.input.namespace,'prod');assert.equal(saved.input.expected_revision,3)
    assert.equal(saved.input.cluster_scope,false);assert.deepEqual(saved.input.permissions,['kubernetes.workload.read'])
    assert.equal(ctx.state.state.value.revision,4)
  }finally{ctx.stop()}
})

test('拒绝空或通配命名空间、超出角色的Secret权限和不适用的集群级操作',async()=>{
  const ctx=harness();try{
    await ctx.ready()
    for(const namespace of ['','*','ALL','a/b']){ctx.state.form.namespace=namespace;await settle();await ctx.state.checkGrant();assert.equal(ctx.state.canPreview.value,false)}
    ctx.state.form.namespace='test';ctx.state.form.permissions=['kubernetes.secret.read'];await settle()
    assert.equal(ctx.state.canPreview.value,false);await ctx.state.checkGrant()
    assert.equal(ctx.calls.some(item=>item.method==='preview'),false)
    ctx.state.form.cluster_scope=true;ctx.state.form.permissions=['kubernetes.workload.read'];await settle()
    assert.equal(ctx.state.canPreview.value,false)
    ctx.state.form.permissions=['kubernetes.node.read'];await settle();await ctx.state.checkGrant()
    const preview=ctx.calls.find(item=>item.method==='preview');assert.equal(preview.input.namespace,'');assert.equal(preview.input.cluster_scope,true)
  }finally{ctx.stop()}
})

test('授权范围完全使用服务端目录元数据，缺失或未知范围不得提交',async()=>{
  const ctx=harness()
  try{
    await ctx.ready()
    const entries=[
      {code:'kubernetes.storage.read',name:'查看存储',resource_scopes:['namespace','cluster']},
      {code:'kubernetes.storage.create',name:'创建存储',resource_scopes:['namespace']},
      {code:'kubernetes.storage.update',name:'更新存储',resource_scopes:['namespace']},
      {code:'kubernetes.storage.delete',name:'删除存储',resource_scopes:['namespace','cluster']},
      {code:'future.missing',name:'缺少范围'},
      {code:'future.unknown',name:'未知范围',resource_scopes:['future']},
    ]
    ctx.state.state.value.catalog.push(...entries);ctx.state.state.value.roles[0].permissions.push(...entries.map(item=>item.code))
    ctx.state.form.cluster_scope=true;await settle()
    const clusterCodes=ctx.state.permissionChoices.value.map(item=>item.code)
    assert.ok(clusterCodes.includes('kubernetes.storage.read'));assert.ok(clusterCodes.includes('kubernetes.storage.delete'))
    for(const code of ['kubernetes.storage.create','kubernetes.storage.update','future.missing','future.unknown']){
      assert.equal(clusterCodes.includes(code),false,code);ctx.state.form.permissions=[code];await settle();await ctx.state.checkGrant();assert.equal(ctx.state.canPreview.value,false,code)
    }
    assert.equal(ctx.calls.some(item=>item.method==='preview'),false)
    ctx.state.form.cluster_scope=false;await settle();assert.ok(ctx.state.permissionChoices.value.some(item=>item.code==='kubernetes.storage.create'))
  }finally{ctx.stop()}
})

test('只读用户不能发起预览、保存、启用、同步或撤销',async()=>{
  const ctx=harness({codes:['kubernetes.rbac.read']});try{
    await ctx.open();ctx.state.configure();ctx.state.form.role_id='role-a';ctx.state.form.namespace='test';ctx.state.form.permissions=['kubernetes.workload.read']
    await ctx.state.checkGrant();await ctx.state.save();ctx.state.reviewMode();await ctx.state.confirmMode();await ctx.state.sync();ctx.state.reviewDelete({id:'grant-a'});await ctx.state.remove()
    assert.equal(ctx.state.editorOpen.value,false)
    assert.equal(ctx.calls.some(item=>item.method!=='get'),false)
  }finally{ctx.stop()}
})

test('启用和撤销都需明确复核，不会因打开抽屉自动生效',async()=>{
  const ctx=harness();try{
    await ctx.open();await ctx.state.confirmMode();assert.equal(ctx.calls.some(item=>item.method==='mode'),false)
    ctx.state.reviewMode();assert.equal(ctx.state.modeReview.value,true);assert.equal(ctx.calls.some(item=>item.method==='mode'),false)
    await ctx.state.confirmMode();assert.deepEqual(ctx.calls.find(item=>item.method==='mode').enabled,true)
    ctx.state.reviewDelete({id:'grant-a'});assert.equal(ctx.calls.some(item=>item.method==='delete'),false)
    await ctx.state.remove();assert.equal(ctx.calls.find(item=>item.method==='delete').version,4)
  }finally{ctx.stop()}
})

test('项目、集群和关闭状态变化后忽略迟到预览及保存响应',async()=>{
  for(const change of ['project','cluster','close']){
    const pending=deferred()
    const ctx=harness({api:{previewKubernetesRbacGrant:()=>pending.promise}})
    try{
      await ctx.ready();const operation=ctx.state.checkGrant();await settle()
      if(change==='project'){ctx.project.id='project-b';ctx.state.syncProject()}
      else if(change==='cluster')ctx.props.cluster='cluster-b'
      else ctx.props.open=false
      await settle();pending.resolve(proposal({namespace:'test'}));await operation;await settle()
      assert.equal(ctx.state.preview.value,undefined,change);assert.equal(ctx.state.canSave.value,false,change)
      await ctx.state.save();assert.equal(ctx.calls.some(item=>item.method==='save'),false)
    }finally{ctx.stop()}
  }
})

test('配置变化后迟到失败不覆盖新配置的错误状态',async()=>{
  const pending=deferred(),ctx=harness({api:{previewKubernetesRbacGrant:()=>pending.promise}})
  try{await ctx.ready();const operation=ctx.state.checkGrant();ctx.state.form.namespace='prod';await settle();pending.reject(new Error('旧配置失败'));await operation;assert.equal(ctx.state.error.value,'')}
  finally{ctx.stop()}
})

test('预览或同步失败保留受控错误并需要重新预览',async()=>{
  const ctx=harness({api:{saveKubernetesRbacGrant:async()=>{throw new Error('授权版本已变化，请重新读取')}}})
  try{await ctx.ready();await ctx.state.checkGrant();await ctx.state.save();assert.match(ctx.state.error.value,/授权版本已变化/);assert.equal(ctx.state.preview.value,undefined);assert.equal(ctx.state.canSave.value,false);assert.equal(ctx.state.stale.value,true);assert.equal(ctx.state.statusLabel.value,'需刷新确认');await ctx.state.load();assert.equal(ctx.state.stale.value,false)}finally{ctx.stop()}
})

test('关闭后迟到保存不会发出授权已变更事件或恢复旧状态',async()=>{
  const pending=deferred(),ctx=harness({api:{saveKubernetesRbacGrant:()=>pending.promise}})
  try{await ctx.ready();await ctx.state.checkGrant();const operation=ctx.state.save();await settle();ctx.props.open=false;await settle();pending.resolve({...initial,revision:4});await operation;assert.equal(ctx.state.state.value,undefined);assert.equal(ctx.events.some(item=>item.name==='changed'),false)}finally{ctx.stop()}
})

test('撤销进行中保持集群约束，同revision轮询不清除正在编辑的授权',async()=>{
  let reads=0
  const ctx=harness({api:{getKubernetesRbac:async()=>++reads===1?clone(initial):{...clone(initial),enabled:true,desired_enabled:true,status:'synced'}}})
  try{
    await ctx.ready();await ctx.state.checkGrant()
    ctx.state.state.value={...ctx.state.state.value,enabled:true,desired_enabled:true,status:'pending'};await settle()
    await ctx.state.poll();assert.equal(ctx.state.state.value.status,'synced');assert.equal(ctx.state.form.namespace,'test');assert.ok(ctx.state.preview.value)
    ctx.state.state.value={...ctx.state.state.value,enabled:true,desired_enabled:false,status:'pending'};await settle()
    assert.equal(ctx.state.statusLabel.value,'正在关闭映射');ctx.state.reviewMode();assert.equal(ctx.state.modeReview.value,undefined)
  }finally{ctx.state.stopPoll();ctx.stop()}
})

test('撤销查看权限会中止请求并清理已读取的授权状态',async()=>{
  const ctx=harness()
  try{await ctx.ready();await ctx.state.checkGrant();ctx.auth.codes=[];await settle();assert.equal(ctx.state.state.value,undefined);assert.equal(ctx.state.preview.value,undefined);await ctx.state.sync();assert.equal(ctx.calls.some(item=>item.method==='sync'),false)}finally{ctx.stop()}
})

test('从角色入口打开时明确选择集群，角色授权列表只呈现当前角色',async()=>{
  const ctx=harness({cluster:''})
  try{await ctx.open();assert.equal(ctx.calls.length,0);ctx.state.chosenCluster.value='cluster-a';await settle();assert.equal(ctx.calls.filter(item=>item.method==='get').length,1);ctx.state.state.value.grants=[{id:'own',role_id:'role-a',namespace:'test',cluster_scope:false,permissions:[]},{id:'other',role_id:'role-b',namespace:'prod',cluster_scope:false,permissions:[]}];assert.deepEqual(ctx.state.visibleGrants.value.map(item=>item.id),['own'])}finally{ctx.stop()}
})

test('HTTP客户端保留revision、取消信号、编码ID与DELETE请求体',async()=>{
  const calls=[]
  const client=Object.fromEntries(['get','post','put','delete'].map(method=>[method,async(...args)=>{calls.push({method,args});return {data:initial}}]))
  const api=loadFrontend('../src/api/kubernetesRbac.ts',{'@/api/client':{__esModule:true,default:client}})
  const controller=new AbortController(),input={role_id:'role-a',namespace:'test',cluster_scope:false,permissions:['kubernetes.workload.read'],expected_revision:3}
  await api.getKubernetesRbac('cluster/a',controller.signal)
  await api.previewKubernetesRbacGrant('cluster/a',input,'grant/a',controller.signal)
  await api.saveKubernetesRbacGrant('cluster/a',input,'',controller.signal)
  await api.saveKubernetesRbacGrant('cluster/a',input,'grant/a',controller.signal)
  await api.deleteKubernetesRbacGrant('cluster/a','grant/a',3,controller.signal)
  await api.setKubernetesRbacMode('cluster/a',true,3,controller.signal)
  await api.syncKubernetesRbac('cluster/a',3,controller.signal)
  assert.ok(calls.every(item=>item.args[0].includes('cluster%2Fa')))
  const deletion=calls.find(item=>item.method==='delete');assert.match(deletion.args[0],/grant%2Fa$/);assert.deepEqual(deletion.args[1].data,{expected_revision:3});assert.equal(deletion.args[1].signal,controller.signal)
  assert.equal(calls[1].args[1].id,'grant/a');assert.equal(calls[1].args[2].signal,controller.signal)
  assert.deepEqual(calls.at(-1).args[1],{expected_revision:3})
  assert.equal(calls.at(-1).args[2].timeout,150_000)
})

test('角色、集群列表和工作区提供授权入口，页面显示原生规则与失败状态',()=>{
  const roleView=readFileSync(new URL('../src/views/AccessView.vue',import.meta.url),'utf8')
  const workspace=readFileSync(new URL('../src/views/KubernetesView.vue',import.meta.url),'utf8')
  const inventory=readFileSync(new URL('../src/components/ClusterInventory.vue',import.meta.url),'utf8')
  const drawer=readFileSync(new URL('../src/components/ClusterRbacDrawer.vue',import.meta.url),'utf8')
  assert.match(roleView,/openClusterRbac\(row as Role\)/);assert.match(workspace,/clusterRbacOpen=true/);assert.match(inventory,/openRbac\(record\)/)
  assert.match(drawer,/最终 Kubernetes RBAC 规则/);assert.match(drawer,/state.last_error/);assert.match(drawer,/Secret 内容读取权限/)
})
