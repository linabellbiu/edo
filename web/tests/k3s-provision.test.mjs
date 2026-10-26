import assert from 'node:assert/strict'
import {test} from 'node:test'
import {readFileSync} from 'node:fs'
import * as vue from 'vue'
import * as yaml from 'yaml'
import {loadFrontend,componentHarness} from './acceptanceHarness.mjs'

const helpers=loadFrontend('../src/utils/k3sProvision.ts',{yaml})
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
const catalog={version:'v1.37.1+k3s1',default_version:'v1.37.1+k3s1',catalog_version:'2026-10-26',sources:['https://docs.k3s.io/cli/server'],server:[],agent:[],installer:[]}
const targetVersion='v1.35.5+k3s1'
const host=id=>({id,name:`Host ${id}`,mode:'ssh',address:`192.0.2.${id}`,is_builtin:false,is_active:true,capabilities:[],credential_configured:true})
const preflightCheck=(status='passed')=>({id:'memory',name:'内存',status,message:status==='failed'?'可用内存不足，请扩容后重试':'满足角色最低要求'})
const preflightReport=(passed=true)=>({checked_at:new Date().toISOString(),passed,checks:[],hosts:[{host_id:'1',host_name:'Host 1',role:'server',hostname:'master-a',passed,checks:[preflightCheck(passed?'passed':'failed')]}]})
const preflightFailure=report=>({message:'前置检查未通过',response:{status:409,data:{code:'k3s_preflight_failed',message:'前置检查未通过',preflight:report}}})
const plan=()=>({id:'plan-1',action:'create',expires_at:new Date(Date.now()+300000).toISOString(),hosts:[{id:'1',name:'Host 1',address:'192.0.2.1',role:'server'}],server_config:'disable: []',agent_config:'',commands:[],warnings:[],preflight:preflightReport()})
const permissions=['cluster.create','cluster.execute','cluster.update','cluster.read','kubernetes.node.create']

function harness({action='create',codes=permissions,api={}}={}){
  const requests=[],timers=[],mounts=[],unmounts=[],listeners=new Map(),storage={project:'project-a'}
  const auth=vue.reactive({user:{id:'operator',project_id:'project-a'},canAny(required){return Boolean(this.user)&&required.some(code=>codes.includes(code))}})
  const props=vue.reactive({open:false,action,cluster:action==='join'?'cluster-a':'',endpoint:action==='join'?'https://cluster.example:6443':'',hosts:[host('1'),host('2')]})
  const methods={
    getK3sCatalog:async(cluster,signal)=>{requests.push({method:'catalog',cluster,signal});return cluster?{...catalog,default_version:targetVersion,target_version:targetVersion}:catalog},
    listK3sInstallations:async(cluster,action,signal)=>{requests.push({method:'list',cluster,action,signal});return []},
    previewK3sPlan:async(body,signal)=>{requests.push({method:'preview',body,signal});return plan()},
    startK3sInstallation:async(id,signal)=>{requests.push({method:'start',id,signal});return {id:'run-1',action,status:'pending',hosts:[],logs:''}},
    getK3sInstallation:async(id,signal)=>{requests.push({method:'poll',id,signal});return {id,action,status:'running',hosts:[],logs:'installing'}},
    ...api,
  }
  const ctx=componentHarness('../src/components/K3sProvisionDrawer.vue',{
    codes,props,modules:{vue:{...vue,onMounted:fn=>mounts.push(fn),onBeforeUnmount:fn=>unmounts.push(fn)},'@/api/k3s':methods,'@/utils/k3sProvision':helpers,'@/api/infrastructure':{listHosts:async()=>{requests.push({method:'hosts',project:storage.project});return props.hosts}},'@/utils/time':{formatDateTime:String},'@/stores/auth':{useAuthStore:()=>auth}},
    globals:{window:{addEventListener:(name,fn)=>listeners.set(name,fn),removeEventListener:(name,fn)=>{if(listeners.get(name)===fn)listeners.delete(name)}},localStorage:{getItem:()=>storage.project},setTimeout:fn=>{timers.push(fn);return timers.length},clearTimeout(){},setInterval(){return 1},clearInterval(){}},
  })
  mounts.forEach(fn=>fn())
  return {...ctx,auth,props,requests,timers,listeners,storage,stop(){unmounts.forEach(fn=>fn());ctx.stop()},async open(){props.open=true;await settle()},ready(){Object.assign(ctx.state.form,{name:'Production',server_host_id:'1',agent_host_ids:[]})}}
}

test('K3s 参数目录按用途和搜索词筛选，默认值保留 false、0 和独立数组',()=>{
  const options=[{name:'secrets-encryption',label:'加密',description:'保护 Secret',group:'安全',type:'boolean',default:false},{name:'etcd-expose-metrics',label:'指标',description:'etcd 指标',group:'存储',type:'boolean',default:false}]
  assert.deepEqual(helpers.k3sOptionGroups(options,'SECRET').map(group=>group.name),['安全'])
  assert.equal(helpers.k3sDefaultValue(options[0]),false)
  assert.equal(helpers.k3sDefaultValue({type:'integer',default:0}),0)
  const list={type:'string[]',default:['a']},value=helpers.k3sDefaultValue(list);value.push('b');assert.deepEqual(list.default,['a'])
  assert.equal(helpers.k3sOptionDefaultText({type:'boolean',default:false}),'关闭')
})

test('覆盖 YAML 允许 false 和空列表，拒绝重复键、未知范围和非对象',()=>{
  helpers.validateK3sOverrides('server:\n  disable: []\n  secrets-encryption: false\nagent:\n  node-label: []')
  for(const text of ['server: 1','server: []','- server','server: {}\nserver: {}','unexpected: {}'])assert.throws(()=>helpers.validateK3sOverrides(text))
})

test('安装主机只显示启用的远程主机，已有 Kubernetes 关联与缺少凭据分别说明',()=>{
  assert.deepEqual(helpers.availableK3sHosts([host('1'),{...host('2'),is_active:false},{...host('3'),mode:'local'},{...host('4'),is_builtin:true}]).map(item=>item.id),['1'])
  assert.match(helpers.k3sHostDisabledReason({...host('1'),credential_configured:false}),/凭据/)
  assert.match(helpers.k3sHostDisabledReason({...host('1'),capabilities:[{kind:'kubernetes',runtime_id:'c'}]}),/已关联/)
})

test('默认安装不提交隐藏的自定义参数；自定义参数和覆盖文本完整进入预览',async()=>{
  const ctx=harness();try{
    await ctx.open();ctx.ready();ctx.state.form.server_options={'secrets-encryption':false};ctx.state.form.overrides='server:\n  disable: []'
    await ctx.state.preview();let payload=ctx.requests.find(item=>item.method==='preview').body
    assert.deepEqual(payload.server_options,{});assert.deepEqual(payload.installer,{});assert.equal(payload.overrides,'')
    ctx.state.form.profile='custom';ctx.state.form.installer={version:'v1.37.1+k3s1',channel:'',INSTALL_K3S_BIN_DIR:'/opt/bin'};await settle();assert.equal(ctx.state.plan.value,undefined)
    await ctx.state.preview();payload=ctx.requests.filter(item=>item.method==='preview').at(-1).body
    assert.deepEqual(payload.server_options,{'secrets-encryption':false});assert.equal(payload.overrides,'server:\n  disable: []')
    assert.deepEqual(payload.installer,{version:'v1.37.1+k3s1',INSTALL_K3S_BIN_DIR:'/opt/bin'})
  }finally{ctx.stop()}
})

test('创建默认使用目录版本，加入旧集群默认跟随目标精确版本，参数目录基线保持独立',async()=>{
  for(const action of ['create','join']){
    const ctx=harness({action})
    try{
      await ctx.open();const expected=action==='join'?targetVersion:catalog.version
      assert.equal(ctx.requests.find(item=>item.method==='catalog').cluster,action==='join'?'cluster-a':'')
      assert.equal(ctx.state.defaultVersion.value,expected);assert.equal(ctx.state.catalog.value.version,catalog.version)
      ctx.state.form.profile='custom';await settle()
      assert.match(ctx.state.overridesPlaceholder.value,new RegExp(expected.replaceAll('+','\\+').replaceAll('.','\\.')))
      assert.equal(Boolean(ctx.state.versionMismatch.value),action==='join')
      assert.equal(ctx.state.input().installer.version,undefined)
      ctx.state.form.installer.version=catalog.version;assert.equal(Boolean(ctx.state.versionMismatch.value),false)
    }finally{ctx.stop()}
  }
  const template=readFileSync(new URL('../src/components/K3sProvisionDrawer.vue',import.meta.url),'utf8')
  assert.match(template,/安装版本：\{\{ defaultVersion \}\}/)
  assert.match(template,/:placeholder="defaultVersion"/)
  assert.match(template,/参数基于 \{\{ catalog.version \}\}/)
  assert.match(template,/加入节点必须固定精确版本，不能仅填写通道/)
})

test('预览显示服务端确认的最终安装版本和目标版本，覆盖选择不沿用表单默认猜测',async()=>{
  for(const action of ['create','join']){
    const finalVersion=action==='join'?'v1.35.4+k3s1':'v1.36.2+k3s1'
    const ctx=harness({action,api:{previewK3sPlan:async()=>({...plan(),action,version:finalVersion,...(action==='join'?{target_version:targetVersion}:{})})}})
    try{
      await ctx.open();ctx.ready();if(action==='join')ctx.state.form.agent_host_ids=['2']
      ctx.state.form.profile='custom';ctx.state.form.overrides=`installer:\n  version: ${finalVersion}`;await settle();await ctx.state.preview()
      assert.equal(ctx.state.plan.value.version,finalVersion)
      assert.equal(ctx.state.plan.value.target_version,action==='join'?targetVersion:undefined)
      assert.notEqual(ctx.state.plan.value.version,ctx.state.defaultVersion.value)
    }finally{ctx.stop()}
  }
  const template=readFileSync(new URL('../src/components/K3sProvisionDrawer.vue',import.meta.url),'utf8')
  assert.match(template,/v-if="plan.version"><dt>安装版本<\/dt><dd><code>\{\{ plan.version \}\}/)
  assert.match(template,/v-if="plan.target_version"><dt>目标集群版本<\/dt>/)
})

test('参数目录读取只为加入节点传递 cluster_id 并保留取消信号',async()=>{
  const requests=[],api=loadFrontend('../src/api/k3s.ts',{'@/api/client':{__esModule:true,default:{get:async(path,options)=>{requests.push({path,options});return {data:catalog}}}}})
  const controller=new AbortController()
  await api.getK3sCatalog('',controller.signal);await api.getK3sCatalog('cluster/a',controller.signal)
  assert.deepEqual(requests.map(item=>item.options.params),[{}, {cluster_id:'cluster/a'}])
  assert.ok(requests.every(item=>item.path==='/kubernetes/k3s/options'&&item.options.signal===controller.signal))
})

test('自定义 YAML 无效时不调用预览，调整主机后清除重复工作节点并更新默认地址',async()=>{
  const ctx=harness();try{
    await ctx.open();ctx.ready();ctx.state.form.profile='custom';ctx.state.form.overrides='server: []';await settle();await ctx.state.preview()
    assert.equal(ctx.requests.some(item=>item.method==='preview'),false);assert.match(ctx.state.error.value,/YAML 对象/)
    ctx.state.form.server_host_id='';await settle();ctx.state.form.agent_host_ids=['1','2'];ctx.state.form.server_host_id='1';await settle();assert.deepEqual(ctx.state.form.agent_host_ids,['2']);assert.equal(ctx.state.form.endpoint,'');assert.equal(ctx.state.endpointPlaceholder.value,'https://192.0.2.1:6443')
    ctx.state.form.server_host_id='2';await settle();assert.deepEqual(ctx.state.form.agent_host_ids,[]);assert.equal(ctx.state.form.endpoint,'');assert.equal(ctx.state.endpointPlaceholder.value,'https://192.0.2.2:6443')
  }finally{ctx.stop()}
})

test('自定义监听端口与 YAML 覆盖由预览解析，自动地址不作为手动 6443 传给后端',async()=>{
  const ctx=harness();try{
    await ctx.open();ctx.ready();ctx.state.form.profile='custom';ctx.state.form.server_options={'https-listen-port':7443};await settle()
    assert.equal(ctx.state.endpointPlaceholder.value,'https://192.0.2.1:7443');assert.equal(ctx.state.input().endpoint,'')
    ctx.state.form.overrides='server:\n  https-listen-port: 8443';assert.equal(ctx.state.endpointPlaceholder.value,'https://192.0.2.1:8443');await ctx.state.preview()
    assert.equal(ctx.requests.find(item=>item.method==='preview').body.endpoint,'')
    ctx.state.form.endpoint='https://proxy.example:6443';ctx.state.form.server_host_id='2';await settle()
    assert.equal(ctx.state.input().endpoint,'https://proxy.example:6443')
    assert.equal(helpers.k3sAutomaticEndpoint({...host('1'),address:'2001:db8::1'},{'https-listen-port':7443}),'https://[2001:db8::1]:7443')
    assert.equal(helpers.k3sAutomaticEndpoint(host('1'),{'https-listen-port':7443},'server:\n  https-listen-port: null'),'https://192.0.2.1:6443')
  }finally{ctx.stop()}
})

test('配置变更和关闭后返回的预览不覆盖新状态，过期计划不能开始安装',async()=>{
  const pending=deferred(),ctx=harness({api:{previewK3sPlan:()=>pending.promise}});try{
    await ctx.open();ctx.ready();const request=ctx.state.preview();ctx.state.form.name='Changed';pending.resolve(plan());await request;assert.equal(ctx.state.plan.value,undefined)
    ctx.state.plan.value={...plan(),expires_at:new Date(Date.now()-1000).toISOString()};ctx.state.now.value=Date.now();await ctx.state.execute();assert.equal(ctx.requests.some(item=>item.method==='start'),false)
  }finally{ctx.stop()}
  const late=deferred(),closed=harness({api:{previewK3sPlan:()=>late.promise}});try{
    await closed.open();closed.ready();const request=closed.state.preview();closed.props.open=false;await settle();late.resolve(plan());await request;assert.equal(closed.state.plan.value,undefined)
  }finally{closed.stop()}
})

test('节点安装使用当前集群，选择工作节点是必要条件，缺少执行或关联权限不能预览',async()=>{
  const ctx=harness({action:'join'});try{
    await ctx.open();assert.equal(ctx.state.canPreview.value,false);ctx.state.form.agent_host_ids=['2'];assert.equal(ctx.state.canPreview.value,true)
    await ctx.state.preview();const payload=ctx.requests.find(item=>item.method==='preview').body
    assert.equal(payload.action,'join');assert.equal(payload.cluster_id,'cluster-a');assert.equal(payload.server_host_id,'');assert.deepEqual(payload.agent_host_ids,['2'])
  }finally{ctx.stop()}
  for(const missing of ['cluster.execute','cluster.update']){
    const denied=harness({codes:permissions.filter(code=>code!==missing)});try{await denied.open();denied.ready();await denied.state.preview();assert.equal(denied.requests.some(item=>item.method==='preview'),false)}finally{denied.stop()}
  }
})

test('前置检查失败展示逐主机阻断报告，不创建可执行计划，配置变化后清空结果',async()=>{
  const report=preflightReport(false),ctx=harness({api:{previewK3sPlan:async()=>{throw preflightFailure(report)}}})
  try{
    await ctx.open();ctx.ready();await settle();await ctx.state.preview()
    assert.deepEqual(ctx.state.visiblePreflight.value,report);assert.equal(ctx.state.plan.value,undefined)
    assert.match(ctx.state.error.value,/前置检查/);await ctx.state.execute()
    assert.equal(ctx.requests.some(item=>item.method==='start'),false)
    ctx.state.form.agent_host_ids=['2'];await settle();assert.equal(ctx.state.visiblePreflight.value,undefined)
    assert.equal(ctx.state.error.value,'')
  }finally{ctx.stop()}
})

test('迟到的预检失败不能覆盖修改后的主机、项目或已关闭的配置',async()=>{
  for(const change of ['host','project','close']){
    const pending=deferred(),ctx=harness({api:{previewK3sPlan:()=>pending.promise}})
    try{
      await ctx.open();ctx.ready();await settle();const request=ctx.state.preview()
      if(change==='host')ctx.state.form.server_host_id='2'
      if(change==='project')ctx.storage.project='project-b'
      if(change==='close')ctx.props.open=false
      await settle();pending.reject(preflightFailure(preflightReport(false)));await request;await settle()
      assert.equal(ctx.state.visiblePreflight.value,undefined,change);assert.equal(ctx.state.plan.value,undefined,change)
      assert.equal(ctx.state.error.value,'',change)
    }finally{ctx.stop()}
  }
})

test('缺失、不完整或有阻断的旧预检不能开始安装，提示项允许已通过的计划执行',async()=>{
  const ctx=harness()
  try{
    await ctx.open();ctx.ready();await settle()
    const invalid=[undefined,{}, {...preflightReport(),hosts:[]}, {...preflightReport(),checked_at:'invalid'}, {...preflightReport(),checks:[preflightCheck('failed')]}, {...preflightReport(),hosts:[{...preflightReport().hosts[0],passed:false}]}, {...preflightReport(),hosts:[{...preflightReport().hosts[0],checks:[preflightCheck('failed')]}]}]
    for(const report of invalid){
      ctx.state.plan.value={...plan(),preflight:report};assert.equal(ctx.state.planPreflightPassed.value,false)
      await ctx.state.execute();assert.equal(ctx.requests.some(item=>item.method==='start'),false)
    }
    const report=preflightReport();report.hosts[0].checks.push({...preflightCheck('warning'),id:'swap',name:'Swap',message:'Swap 已开启，请核对 kubelet 配置'})
    ctx.state.plan.value={...plan(),preflight:report};assert.equal(ctx.state.planPreflightPassed.value,true)
    await ctx.state.execute();assert.equal(ctx.requests.filter(item=>item.method==='start').length,1)
  }finally{ctx.stop()}
})

test('开始安装时的预检冲突返回配置页显示新报告，重新检查前不能重用旧计划',async()=>{
  const report=preflightReport(false),ctx=harness({api:{startK3sInstallation:async()=>{throw preflightFailure(report)}}})
  try{
    await ctx.open();ctx.ready();await settle();await ctx.state.preview();assert.equal(ctx.state.planPreflightPassed.value,true)
    await ctx.state.execute();assert.equal(ctx.state.plan.value,undefined);assert.deepEqual(ctx.state.visiblePreflight.value,report)
    assert.equal(ctx.state.installation.value,undefined);assert.equal(ctx.state.planPreflightPassed.value,false)
    await ctx.state.execute();assert.equal(ctx.state.installation.value,undefined)
    ctx.auth.user=null;await settle();assert.equal(ctx.state.visiblePreflight.value,undefined)
  }finally{ctx.stop()}
})

test('报告按集群和 Master、Worker 分组，保留阻断原因和提示计数',()=>{
  const report=preflightReport(false)
  report.checks=[{id:'target',name:'目标集群',status:'passed',message:'集群版本已核对'}]
  report.hosts.push({host_id:'2',host_name:'Host 2',role:'agent',hostname:'worker-a',passed:true,checks:[preflightCheck('warning')]})
  const ctx=componentHarness('../src/components/K3sPreflightReport.vue',{props:{report},modules:{'@/utils/time':{formatDateTime:String}}})
  try{
    assert.deepEqual(ctx.state.groups.value.map(group=>group.role),['','控制平面（Master）','工作节点（Worker）'])
    assert.equal(ctx.state.failed.value,1);assert.equal(ctx.state.warnings.value,1)
    assert.match(ctx.state.groups.value[1].checks[0].message,/扩容/)
    assert.equal(ctx.state.groups.value[2].hostname,'worker-a')
  }finally{ctx.stop()}
})

test('预检预览沿用取消信号并给多主机检查预留请求预算',async()=>{
  const requests=[],api=loadFrontend('../src/api/k3s.ts',{'@/api/client':{__esModule:true,default:{post:async(path,body,options)=>{requests.push({path,body,options});return {data:plan()}}}}})
  const controller=new AbortController(),body={action:'create'}
  await api.previewK3sPlan(body,controller.signal)
  assert.equal(requests[0].options.signal,controller.signal);assert.equal(requests[0].options.timeout,180000)
})

test('安装任务可从最近记录恢复，轮询失败保留真实状态，恢复成功只通知一次',async()=>{
  let fail=true
  const ctx=harness({api:{listK3sInstallations:async()=>[{id:'old-run',action:'create',name:'Existing',status:'running',logs:'old'}],getK3sInstallation:async id=>{if(fail)throw Error('network unavailable');return {id,action:'create',status:'succeeded',logs:'ready',cluster:{id:'cluster-b',name:'Existing'}}}}})
  try{
    await ctx.open();assert.equal(ctx.state.recent.value[0].id,'old-run');ctx.state.viewInstallation(ctx.state.recent.value[0]);await settle()
    assert.equal(ctx.state.installation.value.status,'running');assert.equal(ctx.state.pollError.value,'network unavailable')
    fail=false;await ctx.state.refreshInstallation();await ctx.state.refreshInstallation()
    assert.equal(ctx.state.installation.value.status,'succeeded');assert.equal(ctx.events.filter(item=>item.name==='created').length,1);assert.equal(ctx.events.filter(item=>item.name==='completed').length,1)
    ctx.props.open=false;await settle();ctx.props.open=true;await settle();assert.equal(ctx.state.installation.value.id,'old-run')
  }finally{ctx.stop()}
})

test('部分安装失败仍通知父页面刷新，并可进入已创建集群继续添加剩余节点',async()=>{
  const ctx=harness({codes:[...permissions,'kubernetes.node.read'],api:{getK3sInstallation:async id=>({id,action:'create',status:'failed',error:'agent failed',cluster_id:'partial-cluster',cluster:{id:'partial-cluster',name:'Production'},hosts:[{id:'1',name:'Host 1',status:'succeeded'},{id:'2',name:'Host 2',status:'failed'}]})}})
  try{
    await ctx.open();ctx.ready();await settle();ctx.state.plan.value=plan();await ctx.state.execute();await ctx.state.refreshInstallation();await ctx.state.refreshInstallation()
    assert.equal(ctx.events.filter(item=>item.name==='completed').length,1);assert.equal(ctx.events.filter(item=>item.name==='created').length,0)
    assert.equal(ctx.state.installation.value.cluster.id,'partial-cluster');ctx.state.manageCreatedCluster()
    assert.deepEqual(ctx.replacements.at(-1),{path:'/kubernetes',query:{cluster:'partial-cluster',view:'overview'}})
    assert.deepEqual(ctx.events.at(-1),{name:'update:open',args:[false]})
  }finally{ctx.stop()}
})

test('最近任务按动作在服务器分页前筛选，项目变化丢弃迟到计划，退出登录清空安装记录',async()=>{
  const pending=deferred(),ctx=harness({api:{previewK3sPlan:()=>pending.promise}})
  try{
    await ctx.open();assert.equal(ctx.requests.find(item=>item.method==='list').action,'create');ctx.ready();const request=ctx.state.preview()
    ctx.auth.user.project_id='project-b';await settle();pending.resolve(plan());await request
    assert.equal(ctx.state.plan.value,undefined);assert.equal(ctx.state.form.name,'')
    ctx.state.installation.value={id:'private',status:'running',logs:'private log'};ctx.auth.user=null;await settle()
    assert.equal(ctx.state.installation.value,undefined);assert.deepEqual(ctx.state.hosts.value,[]);assert.equal(ctx.state.loading.value,false)
  }finally{ctx.stop()}
  const requests=[],api=loadFrontend('../src/api/k3s.ts',{'@/api/client':{__esModule:true,default:{get:async(path,options)=>{requests.push({path,options});return {data:{installations:[]}}}}}})
  await api.listK3sInstallations('cluster-a','join')
  assert.deepEqual(requests[0].options.params,{limit:10,action:'join',cluster_id:'cluster-a'})
})

test('跨标签页切换实际项目清空旧安装数据并取消请求，重新读取主机，不依赖旧 auth 项目',async()=>{
  const pending=deferred(),ctx=harness({api:{previewK3sPlan:()=>pending.promise}})
  try{
    await ctx.open();ctx.ready();const request=ctx.state.preview(),signal=ctx.state.request
    ctx.state.installation.value={id:'old-run',status:'running',logs:'private old-project log'}
    ctx.state.recent.value=[{id:'old-history',status:'failed'}]
    ctx.props.hosts=[host('3')];ctx.storage.project='project-b';ctx.listeners.get('storage')({type:'storage',key:'edo.project'});await settle()
    assert.equal(signal.signal.aborted,true);assert.equal(ctx.state.installation.value,undefined);assert.deepEqual(ctx.state.recent.value,[])
    assert.equal(ctx.state.form.name,'');assert.equal(ctx.auth.user.project_id,'project-a');assert.equal(ctx.requests.filter(item=>item.method==='hosts').at(-1).project,'project-b')
    pending.resolve(plan());await request;assert.equal(ctx.state.plan.value,undefined)
    ctx.state.plan.value=plan();ctx.storage.project='';ctx.listeners.get('storage')({type:'storage',key:null});await settle();assert.equal(ctx.state.plan.value,undefined)
    ctx.state.plan.value=plan();ctx.storage.project='project-c';ctx.listeners.get('edo:projects-changed')({type:'edo:projects-changed'});await settle();assert.equal(ctx.state.plan.value,undefined)
  }finally{ctx.stop()}
  assert.equal(ctx.listeners.size,0)
})

test('未触发项目事件时也丢弃旧项目迟到响应并阻止提交旧计划',async()=>{
  const pending=deferred(),ctx=harness({api:{previewK3sPlan:()=>pending.promise}})
  try{
    await ctx.open();ctx.ready();const request=ctx.state.preview();ctx.storage.project='project-b';pending.resolve(plan());await request;await settle()
    assert.equal(ctx.state.plan.value,undefined);assert.equal(ctx.state.form.name,'')
    ctx.state.plan.value=plan();ctx.storage.project='project-c';await ctx.state.execute();await settle()
    assert.equal(ctx.requests.some(item=>item.method==='start'),false);assert.equal(ctx.state.plan.value,undefined)
  }finally{ctx.stop()}
})

const kubeAPI=loadFrontend('../src/api/kubernetes.ts')
const workspace=loadFrontend('../src/utils/kubernetesWorkspace.ts')
function completionHarness(view){
  const pending=deferred(),storage={project:'project-a'},auth=vue.reactive({user:{id:'operator',project_id:'project-a',permissions:[...permissions]},canAny:codes=>codes.some(code=>permissions.includes(code))})
  const ctx=componentHarness(`../src/views/${view}.vue`,{codes:permissions,query:{view:view==='HostView'?'clusters':'overview'},api:{get:async()=>pending.promise},modules:{
    '@/stores/auth':{useAuthStore:()=>auth},'@/api/kubernetes':kubeAPI,'@/utils/kubernetesWorkspace':workspace,
    '@/utils/resourceList':{useResourceList:()=>({pagination:{},changePage(){}})},'@/api/infrastructure':{listHosts:async()=>[],listEnvironments:async()=>[]},
  },globals:{localStorage:{getItem:()=>storage.project},document:{hidden:false}}})
  return {...ctx,pending,storage,auth}
}

test('两个父页面的安装完成回调在等待刷新后核对用户、实际项目、权限和集群选择',async()=>{
  for(const view of ['HostView','KubernetesView'])for(const change of ['project','user','permission','cluster']){
    const ctx=completionHarness(view)
    try{
      const request=view==='HostView'?ctx.state.clusterProvisionCompleted({action:'create',status:'succeeded',cluster_id:'new-cluster'}):ctx.state.provisionCompleted({action:'create',status:'succeeded',cluster_id:'new-cluster'})
      if(change==='project')ctx.storage.project='project-b'
      if(change==='user')ctx.auth.user.id='other-user'
      if(change==='permission')ctx.auth.user.permissions=[]
      if(change==='cluster'){if(view==='HostView')ctx.route.query.cluster='user-picked';else ctx.state.cluster.value='user-picked'}
      ctx.pending.resolve({data:{clusters:[{id:'new-cluster',is_active:true,default_namespace:'default'}]}});await request;await settle()
      if(view==='HostView')assert.equal(ctx.replacements.length,0,`${view}:${change}`)
      else assert.notEqual(ctx.state.cluster.value,'new-cluster',`${view}:${change}`)
    }finally{ctx.stop()}
  }
})

test('上下文保持不变时安装完成仍选中新集群',async()=>{
  for(const view of ['HostView','KubernetesView']){
    const ctx=completionHarness(view)
    try{
      const request=view==='HostView'?ctx.state.clusterProvisionCompleted({action:'create',status:'succeeded',cluster_id:'new-cluster'}):ctx.state.provisionCompleted({action:'create',status:'succeeded',cluster_id:'new-cluster'})
      ctx.pending.resolve({data:{clusters:[{id:'old-cluster',is_active:true},{id:'new-cluster',is_active:true,default_namespace:'default'}]}});await request;await settle()
      if(view==='HostView')assert.equal(ctx.replacements.at(-1).query.cluster,'new-cluster')
      else assert.equal(ctx.state.cluster.value,'new-cluster')
    }finally{ctx.stop()}
  }
})
