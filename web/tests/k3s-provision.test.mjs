import assert from 'node:assert/strict'
import {test} from 'node:test'
import {readFileSync} from 'node:fs'
import * as vue from 'vue'
import * as yaml from 'yaml'
import {parse,compileTemplate} from '@vue/compiler-sfc'
import {loadFrontend,componentHarness} from './acceptanceHarness.mjs'

const helpers=loadFrontend('../src/utils/k3sProvision.ts',{yaml})
const k3dHelpers=loadFrontend('../src/utils/k3dProvision.ts')
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
const catalog={version:'v1.37.1+k3s1',default_version:'v1.37.1+k3s1',catalog_version:'2026-10-26',sources:['https://docs.k3s.io/cli/server'],server:[],agent:[],installer:[]}
const targetVersion='v1.35.5+k3s1'
const host=id=>({id,name:`Host ${id}`,mode:'ssh',address:`192.0.2.${id}`,is_builtin:false,is_active:true,capabilities:[],credential_configured:true})
const preflightCheck=(status='passed')=>({id:'memory',name:'内存',status,message:status==='failed'?'可用内存不足，请扩容后重试':'满足角色最低要求'})
const preflightReport=(passed=true)=>({checked_at:new Date().toISOString(),passed,checks:[],hosts:[{host_id:'1',host_name:'Host 1',role:'server',hostname:'master-a',passed,checks:[preflightCheck(passed?'passed':'failed')]}]})
const preflightFailure=report=>({message:'前置检查未通过',response:{status:409,data:{code:'k3s_preflight_failed',message:'前置检查未通过',preflight:report}}})
const plan=()=>({id:'plan-1',action:'create',expires_at:new Date(Date.now()+300000).toISOString(),hosts:[{id:'1',name:'Host 1',address:'192.0.2.1',role:'server'}],server_config:'disable: []',agent_config:'',commands:[],warnings:[],preflight:preflightReport()})
const k3dPlan=(config={agents:1,api_port:16443,bind_address:'192.0.2.1'})=>({...plan(),mode:'k3d',k3d:{...config},endpoint:`https://${config.bind_address}:${config.api_port}`,hosts:[{id:'1',name:'Host 1',address:'192.0.2.1',role:'host'}],preflight:{...preflightReport(),hosts:[{...preflightReport().hosts[0],role:'host'}]}})
const permissions=['cluster.create','cluster.execute','cluster.update','cluster.read','kubernetes.node.create']

function harness({action='create',codes=permissions,api={}}={}){
  const requests=[],timers=[],mounts=[],unmounts=[],listeners=new Map(),storage={project:'project-a'}
  let previewed
  const auth=vue.reactive({user:{id:'operator',project_id:'project-a'},canAny(required){return Boolean(this.user)&&required.some(code=>codes.includes(code))}})
  const props=vue.reactive({open:false,action,cluster:action==='join'?'cluster-a':'',endpoint:action==='join'?'https://cluster.example:6443':'',hosts:[host('1'),host('2')]})
  const methods={
    getK3sCatalog:async(cluster,signal)=>{requests.push({method:'catalog',cluster,signal});return cluster?{...catalog,default_version:targetVersion,target_version:targetVersion}:catalog},
    listK3sInstallations:async(cluster,action,signal)=>{requests.push({method:'list',cluster,action,signal});return []},
    previewK3sPlan:async(body,signal)=>{requests.push({method:'preview',body,signal});previewed=body;return body.mode==='k3d'?k3dPlan(body.k3d):plan()},
    startK3sInstallation:async(id,signal)=>{requests.push({method:'start',id,signal});return {id:'run-1',action,mode:previewed?.mode,k3d:previewed?.k3d,status:'pending',hosts:[],logs:''}},
    getK3sInstallation:async(id,signal)=>{requests.push({method:'poll',id,signal});return {id,action,mode:previewed?.mode,k3d:previewed?.k3d,status:'running',hosts:[],logs:'installing'}},
    ...api,
  }
  const ctx=componentHarness('../src/components/K3sProvisionDrawer.vue',{
    codes,props,modules:{vue:{...vue,onMounted:fn=>mounts.push(fn),onBeforeUnmount:fn=>unmounts.push(fn)},'@/api/k3s':methods,'@/utils/k3sProvision':helpers,'@/utils/k3dProvision':k3dHelpers,'@/api/infrastructure':{listHosts:async()=>{requests.push({method:'hosts',project:storage.project});return props.hosts}},'@/utils/time':{formatDateTime:String},'@/stores/auth':{useAuthStore:()=>auth}},
    globals:{window:{addEventListener:(name,fn)=>listeners.set(name,fn),removeEventListener:(name,fn)=>{if(listeners.get(name)===fn)listeners.delete(name)}},localStorage:{getItem:()=>storage.project},setTimeout:fn=>{timers.push(fn);return timers.length},clearTimeout(){},setInterval(){return 1},clearInterval(){}},
  })
  mounts.forEach(fn=>fn())
  return {...ctx,auth,props,requests,timers,listeners,storage,stop(){unmounts.forEach(fn=>fn());ctx.stop()},async open(){props.open=true;await settle()},ready(){Object.assign(ctx.state.form,{name:'Production',server_host_id:action==='create'?'1':'',agent_host_ids:[],join_host_ids:action==='join'?['2']:[]})}}
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

test('表单与确认计划明确官方脚本、宿主机原生服务和默认containerd，中英文模板均有效',()=>{
  const source=readFileSync(new URL('../src/components/K3sProvisionDrawer.vue',import.meta.url),'utf8')
  const {descriptor,errors}=parse(source)
  assert.deepEqual(errors,[])
  assert.deepEqual(compileTemplate({source:descriptor.template.content,filename:'K3sProvisionDrawer.vue',id:'native-k3s-install'}).errors,[])
  const {kubernetesZh:zh,kubernetesEn:en}=loadFrontend('../src/locales/kubernetes.ts')
  for(const key of ['nativeInstallTitle','nativeInstallHint','nativeRuntimeHint','nativeInstallConfirmTitle','nativeInstallConfirmHint']){
    assert.equal(typeof zh[key],'string',key);assert.equal(typeof en[key],'string',key)
    assert.ok(descriptor.template.content.includes(`t('k8s.${key}')`),key)
  }
  for(const locale of [zh,en]){
    assert.match(locale.nativeInstallHint,/https:\/\/get\.k3s\.io/)
    assert.match(locale.nativeInstallHint,/SSH/);assert.match(locale.nativeInstallHint,/systemd\/OpenRC/)
    assert.match(locale.nativeInstallHint,/Docker\/k3d/);assert.match(locale.nativeRuntimeHint,/containerd/)
  }
  assert.match(zh.nativeInstallHint,/宿主机/);assert.match(zh.nativeInstallHint,/不.*Docker\/k3d/)
  assert.match(en.nativeInstallHint,/host/);assert.match(en.nativeInstallHint,/No Docker\/k3d/)
})

test('原生安装不要求Docker主机能力，默认不注入运行时参数且保留官方自选cri-dockerd',async()=>{
  const ctx=harness({api:{getK3sCatalog:async()=>({...catalog,server:[{name:'docker',type:'boolean',default:false}]})}})
  try{
    await ctx.open();ctx.ready();assert.deepEqual(ctx.props.hosts[0].capabilities,[])
    assert.equal(ctx.state.hostOptions.value.find(item=>item.value==='1').disabled,false)
    assert.equal(ctx.state.canPreview.value,true);await ctx.state.preview()
    const input=ctx.requests.find(item=>item.method==='preview').body
    assert.deepEqual(input.server_options,{});assert.deepEqual(input.agent_options,{});assert.deepEqual(input.installer,{})
    ctx.state.form.profile='custom';ctx.state.form.server_options={docker:true};await settle()
    assert.equal(ctx.state.serverOptionChoices.value.find(item=>item.name==='docker').unsupported_reason,undefined)
    assert.deepEqual(ctx.state.input().server_options,{docker:true})
  }finally{ctx.stop()}
})

test('官方主机命令预览不触发安装或容器回退，确认仅提交计划ID且受理不冒充成功',async()=>{
  const commands=['curl --fail --location https://get.k3s.io -o /tmp/edo-k3s-install.sh\nsudo env INSTALL_K3S_VERSION=v1.37.1+k3s1 sh /tmp/edo-k3s-install.sh server --config /etc/rancher/k3s/config.yaml']
  const starts=[],pending=deferred(),ctx=harness({api:{previewK3sPlan:async()=>({...plan(),commands}),startK3sInstallation:(id,signal)=>{starts.push({id,signal});return pending.promise}}})
  try{
    await ctx.open();ctx.ready();await ctx.state.preview()
    assert.deepEqual(ctx.state.plan.value.commands,commands);assert.deepEqual(starts,[])
    const source=readFileSync(new URL('../src/components/K3sProvisionDrawer.vue',import.meta.url),'utf8')
    assert.match(source,/v-for="\(command,index\) in plan.commands"[^>]*>\{\{ command \}\}/)
    const saving=ctx.state.execute();await ctx.state.execute();assert.equal(starts.length,1)
    assert.equal(starts[0].id,'plan-1');assert.equal(starts[0].signal.aborted,false)
    pending.resolve({id:'native-install-1',action:'create',status:'pending',hosts:[],logs:''});await saving
    assert.equal(ctx.state.installation.value.status,'pending')
    assert.ok(!ctx.events.some(event=>['created','completed'].includes(event.name)))
    assert.ok(!ctx.requests.some(item=>/docker|k3d|container/i.test(item.method)))
  }finally{ctx.stop()}
})

test('原生创建仍要求全部动作权限，deployment.read可替代cluster.read而不放宽写权限',async()=>{
  const required=['cluster.create','cluster.execute','cluster.update','cluster.read']
  for(const missing of required){
    const ctx=harness({codes:permissions.filter(code=>code!==missing)})
    try{await ctx.open();ctx.ready();assert.equal(ctx.state.canExecute.value,false,missing);await ctx.state.preview();assert.ok(!ctx.requests.some(item=>item.method==='preview'),missing)}finally{ctx.stop()}
  }
  const ctx=harness({codes:[...permissions.filter(code=>code!=='cluster.read'),'deployment.read']})
  try{await ctx.open();ctx.ready();assert.equal(ctx.state.canExecute.value,true);await ctx.state.preview();assert.equal(ctx.requests.filter(item=>item.method==='preview').length,1)}finally{ctx.stop()}
})

test('创建提供原生与k3d模式，k3d只发送单宿主和容器配置，切换分别保留草稿',async()=>{
  const ctx=harness()
  try{
    await ctx.open();ctx.ready();assert.equal(ctx.state.form.mode,'native')
    Object.assign(ctx.state.form,{name:'Native draft',agent_host_ids:['2'],profile:'custom',server_options:{disable:['traefik']},agent_options:{'node-label':['worker']},installer:{version:'v1.37.1+k3s1'},overrides:'server:\n  disable: []',endpoint:'https://native.example:6443'})
    ctx.state.form.mode='k3d';await settle();Object.assign(ctx.state.form,{name:'Container draft',server_host_id:'1'});await settle()
    assert.equal(ctx.state.form.k3d.bind_address,'192.0.2.1');assert.equal(ctx.state.form.k3d.agents,1);assert.equal(ctx.state.form.k3d.api_port,16443)
    ctx.state.form.k3d.agents=3;await ctx.state.preview()
    const payload=ctx.requests.find(item=>item.method==='preview').body
    assert.deepEqual(payload,{action:'create',mode:'k3d',name:'Container draft',server_host_id:'1',profile:'default',endpoint:'https://192.0.2.1:16443',k3d:{agents:3,api_port:16443,bind_address:'192.0.2.1'}})
    assert.equal(ctx.state.showServerOptions.value,false);assert.equal(ctx.state.showAgentOptions.value,false);assert.deepEqual(ctx.state.selectedHosts.value.map(host=>host.id),['1'])
    ctx.state.form.mode='native';await settle();assert.equal(ctx.state.plan.value,undefined);assert.equal(ctx.state.form.name,'Native draft');assert.deepEqual(ctx.state.form.agent_host_ids,['2'])
    assert.deepEqual(ctx.state.form.server_options,{disable:['traefik']});assert.equal(ctx.state.form.endpoint,'https://native.example:6443');assert.equal(ctx.state.input().mode,'native');assert.equal('k3d' in ctx.state.input(),false)
    ctx.state.form.mode='k3d';await settle();assert.equal(ctx.state.form.name,'Container draft');assert.equal(ctx.state.form.k3d.agents,3)
  }finally{ctx.stop()}
})

test('加入节点固定原生模式，不允许切换字段泄漏k3d或绕过原生参数',async()=>{
  const ctx=harness({action:'join'})
  try{
    await ctx.open();ctx.ready();ctx.state.form.mode='k3d';ctx.state.form.k3d.agents=8;await settle();await ctx.state.preview()
    const payload=ctx.requests.find(item=>item.method==='preview').body
    assert.equal(payload.mode,'native');assert.equal('k3d' in payload,false);assert.equal(ctx.state.isK3d.value,false);assert.equal(ctx.state.showAgentOptions.value,true)
    const source=readFileSync(new URL('../src/components/K3sProvisionDrawer.vue',import.meta.url),'utf8')
    assert.match(source,/<a-form-item v-if="action==='create'" :label="t\('k8s.installMode'\)"/)
  }finally{ctx.stop()}
})

test('k3d地址必须显式IP且节点数端口有界，域名SSH地址不猜NAS网卡，零worker可用',async()=>{
  const ctx=harness()
  try{
    await ctx.open();ctx.props.hosts[0].address='nas-forward.example';ctx.state.form.mode='k3d';await settle();ctx.ready();await settle()
    assert.equal(ctx.state.form.k3d.bind_address,'');assert.equal(ctx.state.canPreview.value,false)
    for(const address of ['0.0.0.0','127.0.0.1','::1','nas.example','192.0.2.1:16443']){ctx.state.form.k3d.bind_address=address;assert.equal(ctx.state.canPreview.value,false,address)}
    ctx.state.form.k3d.bind_address='192.168.10.24'
    for(const [key,value] of [['agents',-1],['agents',9],['agents',1.5],['api_port',0],['api_port',1],['api_port',1023],['api_port',65536]]){
      const previous=ctx.state.form.k3d[key];ctx.state.form.k3d[key]=value;assert.equal(ctx.state.canPreview.value,false,`${key}=${value}`);ctx.state.form.k3d[key]=previous
    }
    ctx.state.form.k3d.agents=0;ctx.state.form.k3d.api_port=1024;assert.equal(ctx.state.canPreview.value,true);await ctx.state.preview()
    assert.equal(ctx.requests.find(item=>item.method==='preview').body.k3d.agents,0)
    assert.equal(ctx.requests.find(item=>item.method==='preview').body.k3d.api_port,1024)
  }finally{ctx.stop()}
})

test('切换模式取消旧预览并清除计划，切回相同草稿也不接受旧响应',async()=>{
  const pending=deferred(),ctx=harness({api:{previewK3sPlan:()=>pending.promise}})
  try{
    await ctx.open();ctx.ready();const waiting=ctx.state.preview(),old=ctx.state.input()
    ctx.state.form.mode='k3d';await settle();assert.equal(ctx.state.busy.value,false);assert.equal(ctx.state.plan.value,undefined)
    ctx.state.form.mode='native';await settle();assert.deepEqual(ctx.state.input(),old)
    pending.resolve(plan());await waiting;assert.equal(ctx.state.plan.value,undefined)
  }finally{ctx.stop()}
})

test('k3d预览必须回显正确模式、单宿主和完整配置，旧native或不完整响应不允许确认',async()=>{
  for(const response of [plan(),{...k3dPlan(),k3d:undefined},{...k3dPlan(),hosts:[]},{...k3dPlan(),hosts:[...k3dPlan().hosts,...k3dPlan().hosts]},{...k3dPlan(),hosts:[{...k3dPlan().hosts[0],id:'wrong-host'}]},{...k3dPlan(),hosts:[{...k3dPlan().hosts[0],role:'server'}]},{...k3dPlan(),endpoint:'https://wrong.example:16443'},k3dPlan({agents:7,api_port:16443,bind_address:'192.0.2.1'})]){
    const ctx=harness({api:{previewK3sPlan:async()=>response}})
    try{await ctx.open();ctx.state.form.mode='k3d';await settle();ctx.ready();await settle();await ctx.state.preview();assert.equal(ctx.state.plan.value,undefined);assert.equal(ctx.state.error.value,'k8s.installModeMismatch');await ctx.state.execute();assert.ok(!ctx.requests.some(item=>item.method==='start'))}finally{ctx.stop()}
  }
})

test('k3d接受host角色预检但不绕过失败项，确认使用plan ID且安装显示服务端模式',async()=>{
  const ctx=harness()
  try{
    await ctx.open();ctx.state.form.mode='k3d';await settle();ctx.ready();await settle();await ctx.state.preview()
    assert.equal(ctx.state.planPreflightPassed.value,true);assert.equal(ctx.state.planModeValid.value,true);assert.equal(ctx.state.displayMode.value,'k3d');assert.equal(ctx.state.k3dConfig.value.agents,1)
    ctx.state.plan.value.preflight.hosts[0].checks[0].status='failed';await ctx.state.execute();assert.ok(!ctx.requests.some(item=>item.method==='start'))
    ctx.state.plan.value.preflight.hosts[0].checks[0].status='passed';await ctx.state.execute()
    assert.equal(ctx.requests.find(item=>item.method==='start').id,'plan-1');assert.equal(ctx.state.installation.value.status,'pending');assert.equal(ctx.state.displayMode.value,'k3d')
    assert.ok(!ctx.events.some(event=>event.name==='created'))
    const report=componentHarness('../src/components/K3sPreflightReport.vue',{props:{report:k3dPlan().preflight},modules:{'@/utils/time':{formatDateTime:String}}})
    try{assert.equal(report.state.groups.value[0].role,'k8s.installationHost')}finally{report.stop()}
  }finally{ctx.stop()}
})

test('历史缺少mode按native显示，k3d详情只显示服务端容器数量，不沿用当前草稿',async()=>{
  const ctx=harness()
  try{
    await ctx.open();ctx.state.form.mode='k3d';await settle();ctx.state.form.k3d.agents=8
    ctx.state.installation.value={id:'legacy',status:'succeeded'};assert.equal(ctx.state.displayMode.value,'native');assert.equal(ctx.state.k3dConfig.value,undefined)
    ctx.state.installation.value={id:'k3d-run',mode:'k3d',k3d:{agents:2,api_port:17443,bind_address:'192.168.10.24'},status:'succeeded'}
    assert.equal(ctx.state.displayMode.value,'k3d');assert.equal(ctx.state.k3dConfig.value.agents,2);assert.equal(ctx.state.k3dConfig.value.api_port,17443)
    ctx.state.form.mode='native';await settle();ctx.state.configureAgain();assert.equal(ctx.state.form.mode,'k3d');assert.equal(ctx.state.installation.value,undefined)
  }finally{ctx.stop()}
})

test('k3d计划不渲染原生主机安装器YAML，新建不冒充已有目标版本',()=>{
  const {descriptor}=parse(readFileSync(new URL('../src/components/K3sProvisionDrawer.vue',import.meta.url),'utf8'))
  const elements=[]
  function visit(node){if(node.type===1)elements.push(node);for(const child of node.children??[])visit(child)}
  visit(descriptor.template.ast)
  const heading=node=>node.children?.find(child=>child.tag==='h4'||child.tag==='dt')?.children?.map(child=>child.content??'').join('')
  for(const title of ['最终主机配置','控制平面最终配置','工作节点最终配置','安装器配置']){
    const node=elements.find(node=>node.tag==='section'&&heading(node)===title)
    assert.ok(node,title);assert.match(node.props.find(prop=>prop.type===7&&prop.name==='if').exp.content,/^displayMode==='native'&&/)
  }
  const target=elements.find(node=>node.tag==='div'&&heading(node)==='目标集群版本')
  assert.equal(target.props.find(prop=>prop.type===7&&prop.name==='if').exp.content,"action==='join'&&plan.target_version")
})

test('K3s安装角色明确区分Master和Worker，未知值不推断为工作节点',()=>{
  assert.equal(helpers.k3sHostRoleLabel('server'),'控制平面（Master）')
  assert.equal(helpers.k3sHostRoleLabel('agent'),'工作节点（Worker）')
  assert.equal(helpers.k3sHostRoleLabel(undefined),'节点')
})

test('添加Master需要明确Server凭据并发送统一join字段，创建流程保持原字段',async()=>{
  const ctx=harness({action:'join'}),created=harness()
  try{
    await ctx.open();assert.equal(ctx.state.form.join_role,'agent')
    ctx.state.form.join_role='server';await settle();ctx.state.form.join_host_ids=['1']
    assert.equal(ctx.state.canPreview.value,false);ctx.state.form.server_token='   ';assert.equal(ctx.state.canPreview.value,false)
    ctx.state.form.server_token='K10fixture-ca::server-fixture-password';assert.equal(ctx.state.canPreview.value,true)
    await ctx.state.preview();const request=ctx.requests.find(item=>item.method==='preview').body
    assert.equal(request.join_role,'server');assert.deepEqual(request.join_host_ids,['1']);assert.equal(request.server_token,'K10fixture-ca::server-fixture-password')
    assert.equal('agent_host_ids' in request,false);assert.equal('server_host_id' in request,false)
    assert.equal(ctx.state.showServerOptions.value,true);assert.equal(ctx.state.showAgentOptions.value,false);assert.equal(ctx.state.defaultVersion.value,targetVersion)
    await created.open();created.ready();created.state.form.server_token='must-not-send';created.state.form.join_host_ids=['2'];await created.state.preview()
    const create=created.requests.find(item=>item.method==='preview').body
    assert.equal(create.server_host_id,'1');assert.deepEqual(create.agent_host_ids,[]);assert.equal('join_role' in create,false);assert.equal('join_host_ids' in create,false);assert.equal('server_token' in create,false)
  }finally{ctx.stop();created.stop()}
})

test('Worker不发送Server凭据或隐藏控制平面参数，Master不发送工作节点参数',async()=>{
  for(const role of ['agent','server']){
    const ctx=harness({action:'join'});try{
      await ctx.open();ctx.state.form.join_role=role;await settle();ctx.state.form.join_host_ids=['2'];ctx.state.form.profile='custom';ctx.state.form.server_token='fixture-server-credential'
      ctx.state.form.server_options={'node-label':['master-only']};ctx.state.form.agent_options={'node-label':['worker-only']};await settle();await ctx.state.preview()
      const request=ctx.requests.find(item=>item.method==='preview').body
      assert.deepEqual(request.server_options,role==='server'?{'node-label':['master-only']}:{})
      assert.deepEqual(request.agent_options,role==='agent'?{'node-label':['worker-only']}:{})
      assert.equal('server_token' in request,role==='server')
    }finally{ctx.stop()}
  }
})

test('Master选项目录标明禁止初始化与拆分控制平面，覆盖配置按当前角色校验',async()=>{
  const ctx=harness({action:'join',api:{getK3sCatalog:async()=>({...catalog,default_version:targetVersion,target_version:targetVersion,server:[{name:'cluster-init'},{name:'disable-agent'},{name:'node-label'}]})}})
  try{
    await ctx.open();ctx.state.form.join_role='server';await settle()
    assert.ok(ctx.state.serverOptionChoices.value.find(item=>item.name==='cluster-init').unsupported_reason)
    assert.ok(ctx.state.serverOptionChoices.value.find(item=>item.name==='disable-agent').unsupported_reason)
    assert.equal(ctx.state.serverOptionChoices.value.find(item=>item.name==='node-label').unsupported_reason,undefined)
    assert.match(ctx.state.overridesPlaceholder.value,/^server:/);assert.doesNotMatch(ctx.state.overridesPlaceholder.value,/^agent:/m)
    helpers.validateK3sOverrides('server:\n  node-label: [master]\ninstaller:\n  version: v1.35.5+k3s1','server')
    assert.throws(()=>helpers.validateK3sOverrides('agent:\n  node-label: [worker]','server'),/仅接受 server/)
    assert.throws(()=>helpers.validateK3sOverrides('server:\n  disable: []','agent'),/仅接受 agent/)
  }finally{ctx.stop()}
})

test('添加Master禁用原始组件参数并拒绝非空YAML，创建和Worker规则不变',async()=>{
  const names=['etcd-arg','kube-apiserver-arg','kube-controller-manager-arg','kube-scheduler-arg','kube-cloud-controller-manager-arg','kube-controller-arg','kube-cloud-controller-arg']
  const options=names.map(name=>({name,type:'string[]',description:'原始参数'}))
  const create=harness({api:{getK3sCatalog:async()=>({...catalog,server:options})}})
  const join=harness({action:'join',api:{getK3sCatalog:async()=>({...catalog,server:options})}})
  try{
    await create.open();assert.ok(create.state.serverOptionChoices.value.every(option=>!option.unsupported_reason))
    await join.open();join.state.form.join_role='server';await settle()
    for(const name of names){
      assert.match(join.state.serverOptionChoices.value.find(option=>option.name===name).unsupported_reason,/原始参数覆盖目标集群/)
      assert.throws(()=>helpers.validateK3sOverrides(`server:\n  ${name}: [unsafe=value]`,'server'),/成员、认证、TLS/)
      assert.doesNotThrow(()=>helpers.validateK3sOverrides(`server:\n  ${name}: [custom=value]`))
      assert.doesNotThrow(()=>helpers.validateK3sOverrides(`agent:\n  ${name}: [custom=value]`,'agent'))
      assert.doesNotThrow(()=>helpers.validateK3sOverrides(`server:\n  ${name}: []`,'server'))
      assert.doesNotThrow(()=>helpers.validateK3sOverrides(`server:\n  ${name}: null`,'server'))
    }
    assert.equal(join.state.serverOptionChoices.value.some(option=>option.name==='cloud-controller-manager-arg'),false)
    join.state.form.join_host_ids=['1'];join.state.form.server_token='fixture-server-credential';join.state.form.profile='custom'
    for(const name of [...names,'cloud-controller-manager-arg']){
      join.state.form.overrides=`server:\n  ${name}: [unsafe=value]`;await settle();await join.state.preview()
      assert.equal(join.requests.some(item=>item.method==='preview'),false,name);assert.match(join.state.error.value,/原始参数/)
    }
  }finally{create.stop();join.stop()}
})

test('切换节点角色清理目标主机、凭据、自定义参数与旧计划',async()=>{
  const ctx=harness({action:'join'});try{
    await ctx.open();ctx.state.form.join_role='server';await settle();Object.assign(ctx.state.form,{join_host_ids:['1'],server_token:'fixture-server-credential',profile:'custom',server_options:{'node-label':['master']},overrides:'server:\n  node-label: [master]'})
    await settle();await ctx.state.preview();assert.ok(ctx.state.plan.value)
    ctx.state.form.join_role='agent';await settle()
    assert.equal(ctx.state.form.server_token,'');assert.deepEqual(ctx.state.form.join_host_ids,[]);assert.deepEqual(ctx.state.form.server_options,{});assert.deepEqual(ctx.state.form.agent_options,{});assert.equal(ctx.state.form.overrides,'')
    assert.equal(ctx.state.plan.value,undefined);assert.equal(ctx.state.visiblePreflight.value,undefined);assert.equal(ctx.state.optionTab.value,'agent')
  }finally{ctx.stop()}
})

test('Master凭据在关闭、项目或集群切换、权限撤销和卸载后清空',async()=>{
  for(const change of ['close','project','cluster','permission','unmount']){
    const ctx=harness({action:'join'});try{
      await ctx.open();ctx.state.form.join_role='server';await settle();ctx.state.form.server_token='fixture-server-credential'
      if(change==='close')ctx.props.open=false
      if(change==='project'){ctx.storage.project='project-b';ctx.listeners.get('edo:projects-changed')?.({type:'edo:projects-changed'})}
      if(change==='cluster')ctx.props.cluster='cluster-b'
      if(change==='permission')ctx.auth.user=null
      if(change==='unmount')ctx.stop()
      await settle();assert.equal(ctx.state.form.server_token,'',change)
    }finally{ctx.stop()}
  }
})

test('角色切换后的迟到Master预览不能恢复旧计划，任务接收后清除凭据',async()=>{
  const pending=deferred(),ctx=harness({action:'join',api:{previewK3sPlan:()=>pending.promise}})
  const accepted=harness({action:'join'})
  try{
    await ctx.open();ctx.state.form.join_role='server';await settle();ctx.state.form.join_host_ids=['1'];ctx.state.form.server_token='fixture-server-credential';await settle()
    const operation=ctx.state.preview();ctx.state.form.join_role='agent';await settle();pending.resolve(plan());await operation
    assert.equal(ctx.state.plan.value,undefined);await ctx.state.execute();assert.equal(ctx.requests.some(item=>item.method==='start'),false)
    await accepted.open();accepted.state.form.join_role='server';await settle();accepted.state.form.join_host_ids=['1'];accepted.state.form.server_token='fixture-server-credential';await settle();await accepted.state.preview();await accepted.state.execute()
    assert.ok(accepted.state.installation.value);assert.equal(accepted.state.form.server_token,'')
  }finally{ctx.stop();accepted.stop()}
})

test('Master与Worker的加入批次均不超过20台，凭据只通过密码控件输入',async()=>{
  for(const role of ['agent','server']){const ctx=harness({action:'join'});try{await ctx.open();ctx.state.form.join_role=role;await settle();ctx.state.form.server_token='fixture-server-credential';ctx.state.form.join_host_ids=Array.from({length:21},(_,index)=>String(index));assert.equal(ctx.state.canPreview.value,false);await ctx.state.preview();assert.equal(ctx.requests.some(item=>item.method==='preview'),false)}finally{ctx.stop()}}
  const source=readFileSync(new URL('../src/components/K3sProvisionDrawer.vue',import.meta.url),'utf8')
  assert.match(source,/<a-input-password v-model:value="form.server_token"/);assert.doesNotMatch(source,/\{\{\s*form.server_token\s*\}\}/)
  assert.match(source,/SQLite、外置数据存储或无法确认的目标会被阻断/)
})

test('Master每计划只能单台逐次加入，Worker保留多选并仍发送数组',async()=>{
  const ctx=harness({action:'join'})
  try{
    await ctx.open();ctx.state.form.join_role='server';await settle();ctx.state.form.server_token='fixture-server-credential'
    ctx.state.form.join_host_ids=['1','2'];assert.equal(ctx.state.canPreview.value,false);await ctx.state.preview();assert.equal(ctx.requests.some(item=>item.method==='preview'),false)
    ctx.state.joinHostSelection.value='1';assert.deepEqual(ctx.state.form.join_host_ids,['1']);assert.equal(ctx.state.canPreview.value,true);await ctx.state.preview();assert.deepEqual(ctx.requests.find(item=>item.method==='preview').body.join_host_ids,['1'])
    ctx.state.form.join_role='agent';await settle();ctx.state.joinHostSelection.value=['1','2'];assert.deepEqual(ctx.state.form.join_host_ids,['1','2']);assert.equal(ctx.state.canPreview.value,true);await ctx.state.preview();assert.deepEqual(ctx.requests.filter(item=>item.method==='preview').at(-1).body.join_host_ids,['1','2'])
    const source=readFileSync(new URL('../src/components/K3sProvisionDrawer.vue',import.meta.url),'utf8');assert.match(source,/Master 每个计划仅加入 1 台/)
  }finally{ctx.stop()}
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
      await ctx.open();ctx.ready();if(action==='join')ctx.state.form.join_host_ids=['2']
      ctx.state.form.profile='custom';ctx.state.form.overrides=`installer:\n  version: ${finalVersion}`;await settle();await ctx.state.preview()
      assert.equal(ctx.state.plan.value.version,finalVersion)
      assert.equal(ctx.state.plan.value.target_version,action==='join'?targetVersion:undefined)
      assert.notEqual(ctx.state.plan.value.version,ctx.state.defaultVersion.value)
    }finally{ctx.stop()}
  }
  const template=readFileSync(new URL('../src/components/K3sProvisionDrawer.vue',import.meta.url),'utf8')
  assert.match(template,/v-if="plan.version"><dt>安装版本<\/dt><dd><code>\{\{ plan.version \}\}/)
  assert.match(template,/v-if="action==='join'&&plan.target_version"><dt>目标集群版本<\/dt>/)
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
    await ctx.open();assert.equal(ctx.state.canPreview.value,false);ctx.state.form.join_host_ids=['2'];assert.equal(ctx.state.canPreview.value,true)
    await ctx.state.preview();const payload=ctx.requests.find(item=>item.method==='preview').body
    assert.equal(payload.action,'join');assert.equal(payload.cluster_id,'cluster-a');assert.equal(payload.join_role,'agent');assert.deepEqual(payload.join_host_ids,['2']);assert.equal('server_host_id' in payload,false);assert.equal('agent_host_ids' in payload,false);assert.equal('server_token' in payload,false)
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
