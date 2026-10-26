import assert from 'node:assert/strict'
import {test} from 'node:test'
import * as vue from 'vue'
import * as yaml from 'yaml'
import {loadFrontend,componentHarness} from './acceptanceHarness.mjs'

const helpers=loadFrontend('../src/utils/k3sProvision.ts',{yaml})
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
const catalog={version:'v1.37.1+k3s1',catalog_version:'2026-10-26',sources:['https://docs.k3s.io/cli/server'],server:[],agent:[],installer:[]}
const host=id=>({id,name:`Host ${id}`,mode:'ssh',address:`192.0.2.${id}`,is_builtin:false,is_active:true,capabilities:[],credential_configured:true})
const plan=()=>({id:'plan-1',action:'create',expires_at:new Date(Date.now()+300000).toISOString(),hosts:[{id:'1',name:'Host 1',address:'192.0.2.1',role:'server'}],server_config:'disable: []',agent_config:'',commands:[],warnings:[]})
const permissions=['cluster.create','cluster.execute','cluster.update','cluster.read','kubernetes.node.create']

function harness({action='create',codes=permissions,api={}}={}){
  const requests=[],timers=[]
  const auth=vue.reactive({user:{id:'operator',project_id:'project-a'},canAny(required){return Boolean(this.user)&&required.some(code=>codes.includes(code))}})
  const props=vue.reactive({open:false,action,cluster:action==='join'?'cluster-a':'',endpoint:action==='join'?'https://cluster.example:6443':'',hosts:[host('1'),host('2')]})
  const methods={
    getK3sCatalog:async signal=>{requests.push({method:'catalog',signal});return catalog},
    listK3sInstallations:async(cluster,action,signal)=>{requests.push({method:'list',cluster,action,signal});return []},
    previewK3sPlan:async(body,signal)=>{requests.push({method:'preview',body,signal});return plan()},
    startK3sInstallation:async(id,signal)=>{requests.push({method:'start',id,signal});return {id:'run-1',action,status:'pending',hosts:[],logs:''}},
    getK3sInstallation:async(id,signal)=>{requests.push({method:'poll',id,signal});return {id,action,status:'running',hosts:[],logs:'installing'}},
    ...api,
  }
  const ctx=componentHarness('../src/components/K3sProvisionDrawer.vue',{
    codes,props,modules:{'@/api/k3s':methods,'@/utils/k3sProvision':helpers,'@/api/infrastructure':{listHosts:async()=>props.hosts},'@/utils/time':{formatDateTime:String},'@/stores/auth':{useAuthStore:()=>auth}},
    globals:{setTimeout:fn=>{timers.push(fn);return timers.length},clearTimeout(){},setInterval(){return 1},clearInterval(){}},
  })
  return {...ctx,auth,props,requests,timers,async open(){props.open=true;await settle()},ready(){Object.assign(ctx.state.form,{name:'Production',server_host_id:'1',agent_host_ids:[]})}}
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
