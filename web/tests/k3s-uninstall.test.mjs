import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import * as vue from 'vue'
import * as yaml from 'yaml'
import { compileScript, compileStyle, compileTemplate, parse } from '@vue/compiler-sfc'
import { renderToString } from '@vue/server-renderer'
import ts from 'typescript'
import { componentHarness, loadFrontend } from './acceptanceHarness.mjs'

const helpers=loadFrontend('../src/utils/k3sProvision.ts',{yaml})
const uninstall=loadFrontend('../src/utils/k3sUninstall.ts')
const read=path=>readFileSync(new URL(path,import.meta.url),'utf8')
const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
const deferred=()=>{let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return {promise,resolve,reject}}
const codes=['cluster.read','kubernetes.k3s.uninstall']
const host=(id='host-a')=>({id,name:'测试主机',mode:'ssh',address:'192.0.2.24',ssh_port:22,ssh_username:'operator',is_active:true,is_builtin:false,credential_configured:true,capabilities:[]})
const summary=(patch={})=>({source_installation_id:'source-a',host_id:'host-a',role:'server',script_path:'/usr/local/bin/k3s-uninstall.sh',service_name:'k3s',data_dir:'/volume2/edo-k3s/test',...patch})
const target=(patch={})=>({...summary(),installation_name:'原生测试安装',status:'failed',created_at:'2026-10-29T01:00:00+08:00',...patch})
const plan=(patch={})=>({id:'uninstall-plan-a',action:'uninstall',mode:'native',expires_at:new Date(Date.now()+300_000).toISOString(),hosts:[{id:'host-a',name:'测试主机',address:'192.0.2.24',role:'server'}],uninstall:summary(),...patch})
const task=(patch={})=>({id:'uninstall-task-a',action:'uninstall',mode:'native',status:'pending',created_at:'2026-10-29T01:00:00+08:00',uninstall:summary(),hosts:[{id:'host-a',name:'测试主机',address:'192.0.2.24',role:'server',status:'pending'}],logs:[],...patch})

function harness({permissions=codes,api={},value=host()}={}){
  const requests=[],mounts=[],unmounts=[],timers=[],clocks=[],listeners=new Map(),storage={project:'project-a'},time={now:Date.now()},granted=vue.reactive(new Set(permissions))
  const auth=vue.reactive({user:{id:'operator',project_id:'project-a',department_id:'department-a',permissions:[...permissions]},canAny(required){return Boolean(this.user)&&required.some(code=>granted.has(code))}})
  const props=vue.reactive({open:false,host:value})
  const defaults={listK3sUninstallTargets:async()=>[target()],listK3sInstallations:async()=>[],previewK3sUninstallPlan:async()=>plan(),startK3sUninstallation:async()=>task(),getK3sInstallation:async id=>task({id,status:'running'})}
  const methods=Object.fromEntries(Object.entries(defaults).map(([name,fn])=>[name,async(...args)=>{requests.push({name,args});return (api[name]??fn)(...args)}]))
  const h=componentHarness('../src/components/K3sUninstallDrawer.vue',{
    props,modules:{vue:{...vue,onMounted:fn=>mounts.push(fn),onBeforeUnmount:fn=>unmounts.push(fn)},'@/stores/auth':{useAuthStore:()=>auth},'@/api/k3s':methods,'@/utils/k3sProvision':helpers,'@/utils/k3sUninstall':uninstall,'@/utils/time':{formatDateTime:String}},
    globals:{Date:{now:()=>time.now,parse:Date.parse},localStorage:{getItem:()=>storage.project},window:{addEventListener:(name,fn)=>listeners.set(name,fn),removeEventListener:(name,fn)=>{if(listeners.get(name)===fn)listeners.delete(name)}},setInterval:fn=>{clocks.push(fn);return clocks.length},clearInterval(){},setTimeout:(fn,delay)=>{const item={fn,delay,cancelled:false};timers.push(item);return timers.length},clearTimeout:id=>{if(timers[id-1])timers[id-1].cancelled=true}},
  })
  mounts.forEach(fn=>fn())
  return {...h,props,auth,requests,timers,clocks,listeners,storage,time,granted,async open(){props.open=true;await settle()},async ready(){h.state.selectedID.value='source-a';await settle()},async preview(){await this.ready();await h.state.preview();await settle()},async close(){h.state.close();props.open=false;await settle()},async advance(ms){time.now+=ms;clocks.forEach(fn=>fn());await settle()},stop(){unmounts.forEach(fn=>fn());h.stop()}}
}

test('独立卸载权限加cluster.read即可使用，不要求创建/加入/cluster.execute，缺一项不能读取或提交',async()=>{
  const h=harness();try{await h.open();assert.equal(h.state.canOperate.value,true);assert.equal(h.state.canPreview.value,false);await h.ready();assert.equal(h.state.canPreview.value,true);assert.equal(h.state.canSubmit.value,false);assert.deepEqual(h.requests.map(r=>r.name).sort(),['listK3sInstallations','listK3sUninstallTargets']);assert.deepEqual(h.requests.find(r=>r.name==='listK3sInstallations').args.slice(0,2),['','uninstall']);assert.equal(h.requests.find(r=>r.name==='listK3sInstallations').args[3],'host-a')}finally{h.stop()}
  for(const missing of codes){const h=harness({permissions:codes.filter(code=>code!==missing)});try{await h.open();await h.ready();await h.state.preview();h.state.confirmation.value='确认';await h.state.execute();assert.equal(h.state.canOperate.value,false);assert.deepEqual(h.requests,[],missing)}finally{h.stop()}}
})

test('内置、本地、停用或无凭据主机不能走原生卸载，不能借Docker能力放行',async()=>{
  for(const patch of [{is_builtin:true},{mode:'local'},{is_active:false},{credential_configured:false}]){const h=harness({value:{...host(),...patch}});try{await h.open();await h.state.preview();await h.state.execute();assert.equal(h.state.canOperate.value,false);assert.deepEqual(h.requests,[])}finally{h.stop()}}
})

test('来源允许失败的EDO原生安装，错误主机/角色/脚本/重复来源拒绝，不把任意主机当来源',async()=>{
  for(const result of [[target({host_id:'host-b'})],[target({role:'host'})],[target({script_path:'/tmp/uninstall.sh'})],[target(),target()],{}]){
    const h=harness({api:{listK3sUninstallTargets:async()=>result}});try{await h.open();assert.match(h.state.loadError.value,/信息不完整/);await h.ready();await h.state.preview();assert.equal(h.requests.filter(r=>r.name==='previewK3sUninstallPlan').length,0)}finally{h.stop()}
  }
  const h=harness();try{await h.open();assert.equal(h.state.targets.value[0].status,'failed');assert.match(h.state.targetOptions.value[0].label,/安装失败/)}finally{h.stop()}
})

test('预览只核对计划不执行，准确显示Server或Agent默认脚本与数据目录',async()=>{
  for(const role of ['server','agent']){
    const snapshot=summary(role==='agent'?{role,script_path:'/usr/local/bin/k3s-agent-uninstall.sh',service_name:'k3s-agent'}:{})
    const h=harness({api:{listK3sUninstallTargets:async()=>[target(snapshot)],previewK3sUninstallPlan:async()=>plan({uninstall:snapshot,hosts:[{...plan().hosts[0],role}]})}})
    try{await h.open();await h.preview();assert.ok(h.state.plan.value);assert.ok(h.state.facts.value.some(f=>f.value===snapshot.script_path));assert.ok(h.state.facts.value.some(f=>f.value===snapshot.data_dir));assert.equal(h.state.canSubmit.value,false);assert.equal(h.requests.some(r=>r.name==='startK3sUninstallation'),false);assert.equal(h.requests.find(r=>r.name==='previewK3sUninstallPlan').args[0],'source-a')}finally{h.stop()}
  }
})

test('预览错目标、k3d、缺摘要、多主机、错误地址、过期或矛盾预检全部阻断',async()=>{
  const invalid=[plan({action:'create'}),plan({mode:'k3d'}),plan({uninstall:undefined}),plan({uninstall:summary({source_installation_id:'other'})}),plan({uninstall:summary({data_dir:'/other'})}),plan({hosts:[...plan().hosts,...plan().hosts]}),plan({hosts:[{...plan().hosts[0],address:'192.0.2.25'}]}),plan({expires_at:'bad'}),plan({expires_at:'2020-01-01T00:00:00Z'}),plan({preflight:{passed:true,checks:null,hosts:[]}})]
  for(const result of invalid){const h=harness({api:{previewK3sUninstallPlan:async()=>result}});try{await h.open();await h.preview();assert.equal(h.state.plan.value,undefined);assert.match(h.state.error.value,/不一致|失效/);h.state.confirmation.value='确认';await h.state.execute();assert.equal(h.requests.some(r=>r.name==='startK3sUninstallation'),false)}finally{h.stop()}}
})

test('必须精确手输确认不Trim，空白英文和换行不能提交，合法提交仅发送plan_id与原确认',async()=>{
  const h=harness();try{await h.open();await h.preview();for(const value of ['',' 确认','确认 ','确认\n','CONFIRM','确认确认']){h.state.confirmation.value=value;await h.state.execute();assert.equal(h.requests.filter(r=>r.name==='startK3sUninstallation').length,0,value)}h.state.confirmation.value='确认';assert.equal(h.state.canSubmit.value,true);await h.state.execute();const request=h.requests.find(r=>r.name==='startK3sUninstallation');assert.deepEqual(request.args.slice(0,2),['uninstall-plan-a','确认']);assert.equal(h.state.installation.value.status,'pending');assert.equal(h.state.confirmation.value,'');assert.equal(h.events.some(e=>e.name==='completed'),false)}finally{h.stop()}
})

test('过期自动清空确认，提交前再次核对实际时钟；返回/改来源不会复用旧计划',async()=>{
  const h=harness();try{await h.open();await h.preview();h.state.confirmation.value='确认';await h.advance(301_000);assert.equal(h.state.expired.value,true);assert.equal(h.state.confirmation.value,'');await h.state.execute();assert.equal(h.requests.some(r=>r.name==='startK3sUninstallation'),false)}finally{h.stop()}
  const other=target({source_installation_id:'source-b',installation_name:'另一来源'}),h2=harness({api:{listK3sUninstallTargets:async()=>[target(),other]}});try{await h2.open();await h2.preview();h2.state.confirmation.value='确认';h2.state.selectedID.value='source-b';await settle();assert.equal(h2.state.plan.value,undefined);assert.equal(h2.state.confirmation.value,'');await h2.state.execute();assert.equal(h2.requests.some(r=>r.name==='startK3sUninstallation'),false)}finally{h2.stop()}
})

test('重新读取同来源发现脚本或数据目录变化也清空已确认的旧计划',async()=>{
  let result=target();const h=harness({api:{listK3sUninstallTargets:async()=>[result]}})
  try{await h.open();await h.preview();h.state.confirmation.value='确认';result=target({data_dir:'/volume2/changed'});await h.state.loadTargets();assert.equal(h.state.plan.value,undefined);assert.equal(h.state.confirmation.value,'');assert.equal(h.state.targets.value[0].data_dir,'/volume2/changed')}finally{h.stop()}
})

test('并发点击只提交一次，关闭只取消前台等待，迟到受理不恢复；重开可回看后台任务',async()=>{
  const pending=deferred();let history=[]
  const h=harness({api:{startK3sUninstallation:()=>pending.promise,listK3sInstallations:async()=>history}})
  try{await h.open();await h.preview();h.state.confirmation.value='确认';const request=h.state.execute();await h.state.execute();assert.equal(h.requests.filter(r=>r.name==='startK3sUninstallation').length,1);const signal=h.requests.find(r=>r.name==='startK3sUninstallation').args[2];await h.close();assert.equal(signal.aborted,true);history=[task()];pending.resolve(task());await request;assert.equal(h.state.installation.value,undefined);assert.equal(h.state.plan.value,undefined);await h.open();assert.equal(h.state.recent.value.length,1);h.state.viewInstallation(h.state.recent.value[0]);await settle();assert.equal(h.state.installation.value.id,'uninstall-task-a');assert.equal(h.state.installation.value.status,'running');assert.equal(h.requests.filter(r=>r.name==='startK3sUninstallation').length,1)}finally{h.stop()}
})

test('关闭或改来源后的迟到预览不能恢复计划，不触发执行',async()=>{
  for(const change of ['close','target']){const pending=deferred(),h=harness({api:{previewK3sUninstallPlan:()=>pending.promise}});try{await h.open();await h.ready();const request=h.state.preview();if(change==='close')await h.close();else h.state.selectedID.value='other';await settle();assert.equal(h.requests.find(r=>r.name==='previewK3sUninstallPlan').args[2].aborted,true);pending.resolve(plan());await request;assert.equal(h.state.plan.value,undefined);assert.equal(h.state.previewing.value,false)}finally{h.stop()}}
})

test('项目、部门、主机连接或权限变化立即作废计划并取消请求，旧结果不能进入新范围',async()=>{
  for(const change of ['project','department','host','permission','logout']){const pending=deferred(),h=harness({api:{previewK3sUninstallPlan:()=>pending.promise}});try{await h.open();await h.ready();const request=h.state.preview();if(change==='project'){h.storage.project='project-b';h.listeners.get('edo:projects-changed')({type:'edo:projects-changed'})}else if(change==='department')h.auth.user.department_id='other';else if(change==='host')h.props.host.address='192.0.2.25';else if(change==='permission')h.granted.delete('kubernetes.k3s.uninstall');else h.auth.user=null;await settle();pending.resolve(plan());await request;assert.equal(h.state.plan.value,undefined,change);assert.equal(h.state.confirmation.value,'',change);assert.equal(h.state.installation.value,undefined,change);assert.equal(h.requests.some(r=>r.name==='startK3sUninstallation'),false,change)}finally{h.stop()}}
})

test('本地项目变化未发事件也禁止旧计划提交，后台受理不能用新项目身份回填',async()=>{
  const h=harness();try{await h.open();await h.preview();h.state.confirmation.value='确认';h.storage.project='project-b';await h.state.execute();assert.equal(h.state.plan.value,undefined);assert.equal(h.requests.some(r=>r.name==='startK3sUninstallation'),false)}finally{h.stop()}
  const pending=deferred(),h2=harness({api:{startK3sUninstallation:()=>pending.promise}});try{await h2.open();await h2.preview();h2.state.confirmation.value='确认';const request=h2.state.execute();h2.storage.project='project-b';pending.resolve(task());await request;assert.equal(h2.state.installation.value,undefined);assert.equal(h2.events.some(e=>e.name==='completed'),false)}finally{h2.stop()}
})

test('受理目标不完整或后台拒绝保留安全失败，清除确认并要求新计划，不自动重试提交',async()=>{
  for(const response of [()=>task({mode:'k3d'}),()=>task({uninstall:summary({host_id:'wrong'})}),()=>{throw new Error('官方卸载核对失败')}]){const h=harness({api:{startK3sUninstallation:async()=>response()}});try{await h.open();await h.preview();h.state.confirmation.value='确认';await h.state.execute();assert.equal(h.state.installation.value,undefined);assert.equal(h.state.plan.value,undefined);assert.equal(h.state.confirmation.value,'');assert.ok(h.state.error.value);await h.state.execute();assert.equal(h.requests.filter(r=>r.name==='startK3sUninstallation').length,1)}finally{h.stop()}}
})

test('后台成功或失败保留真实日志状态并只通知一次，不将pending受理当完成',async()=>{
  for(const status of ['succeeded','failed','interrupted']){let result=task({status:'running'});const h=harness({api:{getK3sInstallation:async()=>result}});try{await h.open();await h.preview();h.state.confirmation.value='确认';await h.state.execute();assert.equal(h.events.filter(e=>e.name==='completed').length,0);result=task({status,logs:['安全阶段：执行官方卸载脚本'],error:status==='failed'?'卸载验证未通过':''});await h.state.refreshInstallation();await h.state.refreshInstallation();assert.equal(h.state.installation.value.status,status);assert.deepEqual(h.state.installation.value.logs,result.logs);assert.equal(h.events.filter(e=>e.name==='completed').length,1,status);assert.equal(h.state.running.value,false)}finally{h.stop()}}
})

test('不明卸载结果只按校验后的失败状态与固定执行阶段识别，不匹配中文错误',()=>{
  for(const status of ['failed','interrupted'])for(const stage of ['uninstall','uninstall_verify'])assert.equal(uninstall.k3sUninstallResultUncertain(task({status,hosts:[{...task().hosts[0],stage}]})),true,`${status}/${stage}`)
  for(const stage of ['uninstall_guard','other','uninstall_verify_extra',undefined])assert.equal(uninstall.k3sUninstallResultUncertain(task({status:'failed',error:'卸载结果未确认，主机锁保留',hosts:[{...task().hosts[0],stage}]})),false)
  for(const status of ['pending','running','succeeded'])assert.equal(uninstall.k3sUninstallResultUncertain(task({status,hosts:[{...task().hosts[0],stage:'uninstall_verify'}]})),false)
  for(const patch of [{action:'create'},{mode:'k3d'},{uninstall:undefined},{uninstall:summary({script_path:'/tmp/arbitrary.sh'})},{hosts:[{...task().hosts[0],id:'wrong',stage:'uninstall_verify'}]}])assert.equal(uninstall.k3sUninstallResultUncertain(task({status:'failed',hosts:[{...task().hosts[0],stage:'uninstall_verify'}],...patch})),false)
})

test('卸载派发后结果不明显示保留锁，不能回来源/重新预览/执行，日志与刷新保留',async()=>{
  const unknown=task({status:'failed',hosts:[{...task().hosts[0],stage:'uninstall_verify',status:'failed'}],logs:['安全日志：等待结果核验'],error:'无法安全确认此目标'})
  const h=harness({api:{getK3sInstallation:async()=>unknown,listK3sInstallations:async()=>[]}})
  try{await h.open();await h.preview();h.state.confirmation.value='确认';await h.state.execute();await h.state.refreshInstallation();assert.equal(h.state.uncertain.value,true);assert.equal(h.state.hostLocked.value,true);assert.equal(h.state.progressTone.value,'warning');assert.equal(h.state.taskLabel(h.state.installation.value),'卸载结果未确认');assert.match(h.state.uncertainMessage,/主机锁已保留/);assert.deepEqual(h.state.installation.value.logs,unknown.logs);h.state.configureAgain();assert.equal(h.state.installation.value.id,unknown.id);await h.state.preview();await h.state.execute();assert.equal(h.requests.filter(r=>r.name==='previewK3sUninstallPlan').length,1);assert.equal(h.requests.filter(r=>r.name==='startK3sUninstallation').length,1);await h.close();await h.open();assert.equal(h.state.hostLocked.value,true)}finally{h.stop()}
})

test('重新打开即从准确主机历史识别保留锁，未派发guard失败不冒充不明结果',async()=>{
  const unknown=task({status:'failed',hosts:[{...task().hosts[0],stage:'uninstall'}]})
  const h=harness({api:{listK3sInstallations:async()=>[unknown],getK3sInstallation:async()=>unknown}})
  try{await h.open();await h.ready();assert.equal(h.state.hostLocked.value,true);assert.equal(h.state.canPreview.value,false);await h.state.preview();assert.equal(h.requests.some(r=>r.name==='previewK3sUninstallPlan'),false);h.state.viewInstallation(h.state.recent.value[0]);await settle();assert.equal(h.state.uncertain.value,true);h.state.configureAgain();assert.equal(h.state.installation.value.id,unknown.id)}finally{h.stop()}
  const guard=task({status:'failed',hosts:[{...task().hosts[0],stage:'uninstall_guard'}]}),h2=harness({api:{getK3sInstallation:async()=>guard}})
  try{await h2.open();await h2.preview();h2.state.confirmation.value='确认';await h2.state.execute();await h2.state.refreshInstallation();assert.equal(h2.state.hostLocked.value,false);h2.state.configureAgain();assert.equal(h2.state.installation.value,undefined)}finally{h2.stop()}
})

test('进度失败有界退避并可恢复，错任务目标不替换，关闭与卸载释放轮询/请求',async()=>{
  let fail=true;const h=harness({api:{getK3sInstallation:async()=>{if(fail)throw new Error('进度暂不可用');return task({status:'running'})}}})
  try{await h.open();await h.preview();h.state.confirmation.value='确认';await h.state.execute();for(const delay of [5000,10000,20000,30000,30000]){await h.state.refreshInstallation();assert.equal(h.timers.at(-1).delay,delay);assert.equal(h.state.installation.value.status,'pending')}fail=false;await h.state.refreshInstallation();assert.equal(h.state.pollError.value,'');assert.equal(h.timers.at(-1).delay,1500);await h.close();assert.equal(h.requests.filter(r=>r.name==='getK3sInstallation').at(-1).args[1].aborted,true);assert.equal(h.timers.at(-1).cancelled,true)}finally{h.stop()}
  const wrong=harness({api:{getK3sInstallation:async()=>task({uninstall:summary({source_installation_id:'other'})})}});try{await wrong.open();await wrong.preview();wrong.state.confirmation.value='确认';await wrong.state.execute();await wrong.state.refreshInstallation();assert.match(wrong.state.pollError.value,/目标信息发生变化/);assert.equal(wrong.state.installation.value.uninstall.source_installation_id,'source-a')}finally{wrong.stop()}
})

test('来源和历史旧响应在关闭/切主机/失权后丢弃，卸载只读历史也核对host与action',async()=>{
  const pending=deferred(),h=harness({api:{listK3sUninstallTargets:()=>pending.promise,listK3sInstallations:async()=>[task({action:'create'})]}})
  try{const opening=h.open();await settle();await h.close();pending.resolve([target()]);await opening;await settle();assert.deepEqual(h.state.targets.value,[]);assert.equal(h.state.loading.value,false);await h.open();assert.match(h.state.recentError.value,/信息不完整/);assert.deepEqual(h.state.recent.value,[])}finally{h.stop()}
})

test('卸载API使用独立三条路由与精确确认，历史携host过滤且保留AbortSignal',async()=>{
  const requests=[],client={get:async(path,options)=>{requests.push({method:'get',path,options});return {data:path.endsWith('uninstall-targets')?{targets:[target()]}:{installations:[task()]}}},post:async(path,body,options)=>{requests.push({method:'post',path,body,options});return {data:path.endsWith('/plans')?plan():task()}}}
  const api=loadFrontend('../src/api/k3s.ts',{'@/api/client':{__esModule:true,default:client}}),controller=new AbortController()
  await api.listK3sUninstallTargets('host-a',controller.signal);await api.previewK3sUninstallPlan('source-a','host-a',controller.signal);await api.startK3sUninstallation('plan-a','确认',controller.signal);await api.listK3sInstallations('','uninstall',controller.signal,'host-a')
  assert.deepEqual(requests.map(r=>r.path),['/kubernetes/k3s/uninstall-targets','/kubernetes/k3s/uninstall/plans','/kubernetes/k3s/uninstallations','/kubernetes/k3s/installations'])
  assert.deepEqual(requests[1].body,{installation_id:'source-a',host_id:'host-a'});assert.deepEqual(requests[2].body,{plan_id:'plan-a',confirmation:'确认'});assert.equal(requests[1].options.timeout,180_000);assert.equal(requests[2].options.timeout,180_000);assert.deepEqual(requests[3].options.params,{limit:10,action:'uninstall',host_id:'host-a'});assert.ok(requests.every(r=>r.options.signal===controller.signal))
})

test('主机菜单独立授权非内置主机，不依赖创建权限；旧地址与旧项目入口拒绝',async()=>{
  const project={id:'project-a'},value=host(),list=loadFrontend('../src/utils/resourceList.ts',{vue,'vue-i18n':{useI18n:()=>({t:key=>key})}})
  const permissions=vue.reactive(new Set(codes)),auth=vue.reactive({user:{id:'operator',project_id:'project-a',department_id:'department-a',permissions:[...codes]},canAny:required=>required.some(code=>permissions.has(code))})
  const h=componentHarness('../src/views/HostView.vue',{codes,modules:{'@/stores/auth':{useAuthStore:()=>auth},'@/api/infrastructure':{listHosts:async()=>[value],listEnvironments:async()=>[]},'@/utils/resourceList':list,'@/utils/infrastructureRelations':loadFrontend('../src/utils/infrastructureRelations.ts')},globals:{localStorage:{getItem:()=>project.id}}})
  try{await h.state.refresh();assert.equal(h.state.showK3sUninstall(value),true);assert.equal(h.state.showK3sUninstall({...value,is_builtin:true}),false);h.state.openK3sUninstall({...value,address:'192.0.2.25'});assert.equal(h.state.k3sUninstallOpen.value,false);h.state.openK3sUninstall(value);assert.equal(h.state.k3sUninstallOpen.value,true);assert.equal(h.state.k3sUninstallHostID.value,'host-a');h.state.k3sUninstallOpen.value=false;project.id='other';h.state.openK3sUninstall(value);assert.equal(h.state.k3sUninstallOpen.value,false);permissions.delete('kubernetes.k3s.uninstall');assert.equal(h.state.showK3sUninstall(value),false)}finally{h.stop()}
})

function compile(source){const module={exports:{}};new Function('require','module','exports',ts.transpileModule(compileScript(parse(source).descriptor,{id:'uninstall-render',inlineTemplate:true}).content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText)(name=>name==='vue'?vue:{},module,module.exports);return module.exports.default}
test('真实卸载模板编译，固定官方链接和毁损警告，Footer精确确认禁用语义且窄屏可收缩',async()=>{
  const source=read('../src/components/K3sUninstallDrawer.vue'),{descriptor,errors}=parse(source);assert.deepEqual(errors,[]);compileScript(descriptor,{id:'uninstall-ui'});assert.deepEqual(compileTemplate({source:descriptor.template.content,id:'uninstall-ui'}).errors,[]);assert.deepEqual(compileStyle({source:descriptor.styles[0].content,id:'uninstall-ui',scoped:true}).errors,[])
  assert.match(source,/永久删除本地集群数据、本地持久化卷/);assert.match(source,/不是删除节点或断开集群连接/);assert.match(source,/href="https:\/\/docs.k3s.io\/zh\/installation\/uninstall" target="_blank" rel="noopener noreferrer"/);assert.match(source,/aria-label="卸载确认"/);assert.match(source,/grid-template-columns:minmax\(0,1fr\)/);assert.doesNotMatch(source,/overflow(?:-x)?:hidden|v-html|\/assistant/)
  let footer;function walk(node){if(node.type===1&&node.tag==='template'&&node.props.some(p=>p.type===7&&p.name==='slot'&&p.arg?.content==='footer'))footer=node;for(const child of node.children??[])walk(child)}walk(descriptor.template.ast)
  const Footer=compile(`<script setup>import {toRefs} from 'vue';const props=defineProps({state:Object});const {running,plan,installation,busy,canPreview,canSubmit,previewing,submitting,canOperate,hostLocked}=toRefs(props.state);const close=()=>{},invalidatePlan=()=>{},preview=()=>{},execute=()=>{},configureAgain=()=>{},refreshInstallation=()=>{},loadRecent=()=>{};</script><template>${footer.children.map(node=>node.loc.source).join('')}</template>`)
  async function render(state){const app=vue.createSSRApp(Footer,{state});app.component('a-button',{inheritAttrs:false,setup:(_p,{attrs,slots})=>()=>vue.h('button',attrs,slots.default?.())});return renderToString(app)}
  const state=vue.reactive({running:false,plan:plan(),installation:undefined,busy:false,canPreview:true,canSubmit:false,previewing:false,submitting:false,canOperate:true,hostLocked:false})
  assert.match(await render(state),/<button[^>]*danger[^>]*disabled[^>]*>确认卸载<\/button>/);state.canSubmit=true;assert.doesNotMatch(await render(state),/<button[^>]*danger[^>]*disabled[^>]*>确认卸载<\/button>/)
  state.hostLocked=true;state.installation=task({status:'failed'});const locked=await render(state);assert.match(locked,/刷新日志/);assert.doesNotMatch(locked,/返回来源|返回核对|预览卸载|确认卸载/)
})
