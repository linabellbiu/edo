import assert from 'node:assert/strict'
import {test} from 'node:test'
import {readFileSync} from 'node:fs'
import ts from 'typescript'
import {parse,compileScript} from '@vue/compiler-sfc'
import * as vue from 'vue'
import {renderToString} from '@vue/server-renderer'
function load(path,modules={},inlineTemplate=false){
 const source=readFileSync(new URL(path,import.meta.url),'utf8'),code=path.endsWith('.vue')?compileScript(parse(source).descriptor,{id:'diagnostics-test',inlineTemplate}).content:source
 const output=ts.transpileModule(code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
 new Function('require','module','exports',output)(key=>modules[key]??{},module,module.exports);return module.exports
}
const api=load('../src/api/kubernetes.ts'),form=load('../src/utils/kubernetesWorkloadForm.ts',{'@/api/kubernetes':api}),diagnostics=load('../src/utils/kubernetesDiagnostics.ts',{'@/api/kubernetes':api})
function metricSummaryHarness(){
 const sampledAt=Date.parse('2026-10-27T04:00:00Z')
 const labels={'k8s.nodeUsed':'使用量','k8s.resourceRequests':'资源申请','k8s.nodeDeclaredLimits':'已配置限制'}
 const component=load('../src/components/KubeResourceDiagnostics.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},
  'vue-i18n':{useI18n:()=>({t:key=>labels[key]??key,te:()=>false,locale:vue.ref('zh-CN')})},
  'echarts/core':{use(){}},
  '@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})},
  '@/api/kubernetes':api,'@/utils/kubernetesDiagnostics':diagnostics,
 }).default
 const props=vue.reactive({cluster:'cluster',object:{kind:'Pod',metadata:{name:'web',namespace:'team',uid:'pod-current',creationTimestamp:'2026-10-27T03:00:00Z'},spec:{containers:[
  {name:'app',resources:{requests:{cpu:'100m'},limits:{memory:'1Gi'}}},
  {name:'sidecar',resources:{requests:{cpu:'50m',memory:'16Mi'},limits:{cpu:'200m',memory:'64Mi'}}},
 ]}},type:api.kubeTypes.find(type=>type.kind==='Pod'),autoRefresh:false,canFollow:()=>false,compact:true})
 const state=component.setup(props,{expose(){},emit(){}})
 state.now.value=sampledAt
 return {state,props,sampledAt,sample(containers,changes={}){state.data.value={uid:'pod-current',state:'ok',metrics_at:new Date(sampledAt).toISOString(),containers,...changes}}}
}
test('容器表单编辑镜像时保留原探针、扩展资源、密钥引用、启动参数和挂载选项',()=>{
 const raw={name:'app',image:'old:1',resources:{requests:{cpu:'100m','nvidia.com/gpu':'1'},limits:{cpu:'1','nvidia.com/gpu':'1'}},readinessProbe:{httpGet:{path:'/ready',port:'http',httpHeaders:[{name:'X-Probe',value:'yes'}]}},env:[{name:'TOKEN',valueFrom:{secretKeyRef:{name:'private',key:'token'}}}],envFrom:[{secretRef:{name:'private'}}],securityContext:{runAsNonRoot:true},command:['sh','-c','echo one\necho two'],volumeMounts:[{name:'data',mountPath:'/data',subPathExpr:'$(POD_NAME)',mountPropagation:'HostToContainer'}]}
 const original=JSON.stringify(raw),state=form.containerForm(raw);state.image='new:2'
 const result=form.buildContainer(state)
 for(const key of ['resources','readinessProbe','env','envFrom','securityContext','command'])assert.deepEqual(result[key],raw[key],key)
 assert.equal(result.volumeMounts[0].subPathExpr,'$(POD_NAME)');assert.equal(result.volumeMounts[0].mountPropagation,'HostToContainer')
 assert.equal(JSON.stringify(raw),original);assert.equal(result.image,'new:2')
 state.probes.readinessProbe.enabled=false;state.cpuRequest='';assert.equal(form.buildContainer(state).readinessProbe,undefined);assert.equal(form.buildContainer(state).resources.requests.cpu,undefined);assert.equal(form.buildContainer(state).resources.requests['nvidia.com/gpu'],'1')
})
test('探针类型切换清除互斥处理器，Init Sidecar 与资源数量按真实规则构建',()=>{
 const state=form.containerForm({name:'init',image:'busybox:1',livenessProbe:{httpGet:{port:80,path:'/old'},terminationGracePeriodSeconds:10}})
 state.sidecar=true;state.cpuRequest='100m';state.cpuLimit='1';state.memoryRequest='128Mi';state.memoryLimit='1Gi';state.probes.livenessProbe.kind='exec';state.probes.livenessProbe.command='sh\n-c\ntest -f /ready'
 const result=form.buildContainer(state,true);assert.equal(result.restartPolicy,'Always');assert.equal(result.livenessProbe.httpGet,undefined);assert.deepEqual(result.livenessProbe.exec.command,['sh','-c','test -f /ready']);assert.equal(result.livenessProbe.terminationGracePeriodSeconds,10)
 state.cpuRequest='2';assert.throws(()=>form.buildContainer(state),/requestExceedsLimit/);state.cpuRequest='bad';assert.throws(()=>form.buildContainer(state),/invalidResources/);state.cpuRequest='100m';state.probes.livenessProbe.timeoutSeconds=0;assert.throws(()=>form.buildContainer(state),/invalidProbe/)
})
test('卷表单保留投射键和未知卷类型，拒绝悬空挂载、重复路径和重复卷名',()=>{
 const config={name:'config',configMap:{name:'old',items:[{key:'app.yaml',path:'app.yaml'}],defaultMode:420}},projected={name:'projected',projected:{sources:[{serviceAccountToken:{path:'token',expirationSeconds:3600}}]}}
 const rows=[form.volumeForm(config),form.volumeForm(projected)];rows[0].source='new'
 let result=form.buildVolumes(rows,[{volumeMounts:[{name:'config',mountPath:'/etc/app'}]}]);assert.equal(result[0].configMap.name,'new');assert.deepEqual(result[0].configMap.items,config.configMap.items);assert.deepEqual(result[1],projected)
 assert.throws(()=>form.buildVolumes(rows,[{volumeMounts:[{name:'absent',mountPath:'/data'}]}]),/invalidMount/)
 assert.throws(()=>form.buildVolumes(rows,[{volumeMounts:[{name:'config',mountPath:'/data'},{name:'projected',mountPath:'/data'}]}]),/invalidMount/)
 assert.throws(()=>form.buildVolumes([...rows,rows[0]],[]),/invalidVolume/)
})
test('Pod 指标缺失、旧采样、同名新 UID 不伪造零，Init 和临时容器均保留',()=>{
 const now=Date.now(),pod={metadata:{uid:'p1',creationTimestamp:new Date(now-3600000).toISOString()},spec:{containers:[{name:'app',resources:{requests:{cpu:'100m'},limits:{memory:'1Gi'}}}],initContainers:[{name:'init',image:'init:1'}],ephemeralContainers:[{name:'debug'}]}}
 const metrics={uid:'p1',state:'ok',metrics_at:new Date(now).toISOString(),containers:[{name:'app',cpu:0,memory:128*1024**2}]}
 let rows=diagnostics.podMetricRows(pod,metrics,now);assert.equal(rows.length,3);assert.equal(rows[0].cpu.used,0);assert.equal(rows[0].cpu.requests,.1);assert.equal(rows[0].cpu.limits,null);assert.equal(rows[1].cpu.used,null)
 for(const change of [{uid:'old'},{state:'unavailable'},{metrics_at:new Date(now-180000).toISOString()}])assert.equal(diagnostics.podMetricRows(pod,{...metrics,...change},now)[0].cpu.used,null)
})
test('Pod 指标标题区分完整的真实零、完整总量与没有容器的数据',()=>{
 const h=metricSummaryHarness()
 h.sample([{name:'app',cpu:0,memory:0},{name:'sidecar',cpu:0,memory:0}])
 assert.equal(h.state.metricTotal('cpu'),'0 m');assert.equal(h.state.metricTotal('memory'),'0 MiB')
 h.sample([{name:'app',cpu:.1,memory:32*1024**2},{name:'sidecar',cpu:.05,memory:16*1024**2}])
 assert.equal(h.state.metricTotal('cpu'),'150 m');assert.equal(h.state.metricTotal('memory'),'48 MiB')
 h.props.object.spec.containers=[]
 assert.equal(h.state.metricTotal('cpu'),'—');assert.equal(h.state.metricTotal('memory'),'—')
})
test('Pod 指标标题不能把缺测容器、旧采样或其他 UID 的部分用量当作总量',()=>{
 const h=metricSummaryHarness(),complete=[{name:'app',cpu:.1,memory:32*1024**2},{name:'sidecar',cpu:.05,memory:16*1024**2}]
 for(const [containers,changes] of [
  [[complete[0]],{}],
  [complete,{metrics_at:new Date(h.sampledAt-120001).toISOString()}],
  [complete,{uid:'previous-pod'}],
  [complete,{state:'unavailable'}],
 ]){
  h.sample(containers,changes)
  assert.equal(h.state.metricTotal('cpu'),'—');assert.equal(h.state.metricTotal('memory'),'—')
 }
 h.sample([complete[0],{name:'sidecar',cpu:0}])
 assert.equal(h.state.metricTotal('cpu'),'100 m');assert.equal(h.state.metricTotal('memory'),'—')
 h.sample(complete);h.state.now.value=h.sampledAt+120001
 assert.equal(h.state.metricTotal('cpu'),'—');assert.equal(h.state.metricTotal('memory'),'—')
})
test('Pod 指标无采样时可访问描述仍保留各容器申请与限制，并用破折号表示缺失',()=>{
 const h=metricSummaryHarness()
 h.sample([{name:'app',cpu:.1,memory:32*1024**2}],{metrics_at:new Date(h.sampledAt-120001).toISOString()})
 const cpu=h.state.metricDescription('cpu'),memory=h.state.metricDescription('memory')
 assert.match(cpu,/app: 使用量 —/);assert.match(cpu,/资源申请 100 m/);assert.match(cpu,/已配置限制 —/)
 assert.match(cpu,/sidecar: 使用量 —/);assert.match(cpu,/资源申请 50 m/);assert.match(cpu,/已配置限制 200 m/)
 assert.match(memory,/app: 使用量 —/);assert.match(memory,/资源申请 —/);assert.match(memory,/已配置限制 1 GiB/)
 assert.match(memory,/sidecar: 使用量 —/);assert.match(memory,/资源申请 16 MiB/);assert.match(memory,/已配置限制 64 MiB/)
 assert.doesNotMatch(cpu,/使用量 0 m/);assert.doesNotMatch(memory,/使用量 0 MiB/)
})
test('生命周期按真实时间排序并区分当前与上次退出，不补造就绪时间',()=>{
 const pod={metadata:{creationTimestamp:'2026-01-01T00:00:00Z'},spec:{containers:[{name:'app'}]},status:{conditions:[{type:'Ready',status:'False',lastTransitionTime:'2026-01-01T00:02:00Z',reason:'ContainersNotReady'},{type:'Unknown'}],containerStatuses:[{name:'app',state:{waiting:{reason:'CrashLoopBackOff'}},lastState:{terminated:{exitCode:137,reason:'OOMKilled',startedAt:'2026-01-01T00:00:30Z',finishedAt:'2026-01-01T00:01:00Z'}}}]}}
 const points=diagnostics.podLifecycle(pod);assert.equal(points.length,4);assert.equal(points[0].key,'created');assert.equal(points.at(-1).key,'Ready');assert.match(points[2].reason,/OOMKilled.*137/);assert.equal(points.at(-1).status,'False')
})
test('诊断关闭或切换对象后丢弃迟到响应；只允许同命名空间且有权限的 Pod 跳转',async()=>{
 let unmount,requests=[]
 const component=load('../src/components/KubeResourceDiagnostics.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(fn){unmount=fn},watch(){}},'vue-i18n':{useI18n:()=>({t:k=>k,locale:vue.ref('zh-CN')})},'echarts/core':{use(){}},'@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})},'@/api/kubernetes':{...api,kubeErrorMessage:e=>e.message},'@/utils/kubernetesDiagnostics':diagnostics,'@/api/client':{__esModule:true,default:{get:(url,options)=>new Promise(resolve=>requests.push({url,options,resolve}))}}}).default
 const props={cluster:'cluster',object:{kind:'Service',metadata:{name:'web',namespace:'team',uid:'s1'}},type:api.kubeTypes.find(t=>t.kind==='Service'),autoRefresh:true,canFollow:()=>true}
 const state=component.setup(props,{expose(){},emit(){}})
 const a=state.load(),b=state.load();requests[1].resolve({data:{uid:'s1',state:'ok',endpoints:[{addresses:['10.0.0.2']}]}});await b;requests[0].resolve({data:{uid:'old',state:'ok',endpoints:[]}});await a;assert.equal(state.endpoints.value.length,1);assert.equal(requests[0].options.signal.aborted,true)
 assert.equal(state.targetReference({target:{kind:'Pod',name:'web',uid:'p1',namespace:'other'}}),undefined)
 assert.equal(state.targetReference({target:{kind:'Pod',name:'web',uid:'p1'}}).namespace,'team')
 const c=state.load();globalThis.document={removeEventListener(){}};unmount();requests[2].resolve({data:{uid:'s1',state:'ok',endpoints:[]}});await c;assert.equal(state.endpoints.value.length,1);assert.equal(requests[2].options.signal.aborted,true)
})
test('未修改端口与环境变量时保留同端口多协议和变量展开顺序，空参数不丢失',()=>{
 const raw={name:'app',image:'app:1',ports:[{name:'dns-tcp',containerPort:53,protocol:'TCP'},{name:'dns-udp',containerPort:53,protocol:'UDP'}],env:[{name:'PREFIX',value:'base'},{name:'SECRET',valueFrom:{secretKeyRef:{name:'secret',key:'value'}}},{name:'EXPANDED',value:'$(PREFIX)/$(SECRET)'}],args:['']}
 const state=form.containerForm(raw);state.image='app:2';const result=form.buildContainer(state)
 assert.deepEqual(result.ports,raw.ports);assert.deepEqual(result.env,raw.env);assert.deepEqual(result.args,[''])
})
test('Pod 折线只接收当前 UID 的新鲜真实指标，零值保留、未知值不补零',()=>{
 const now=Date.parse('2026-10-27T08:00:00Z'),pod={metadata:{uid:'pod-a',creationTimestamp:new Date(now-3600000).toISOString()},spec:{containers:[{name:'web'},{name:'sidecar'}]}}
 const data={uid:'pod-a',state:'ok',metrics_at:new Date(now).toISOString(),containers:[{name:'web',cpu:0,memory:0}]}
 const history=diagnostics.appendPodMetricSample(null,pod,data,now)
 assert.equal(history.points.length,1);assert.equal(history.points[0].at,now)
 assert.deepEqual(diagnostics.podMetricTrendPoints(history,'web','cpu'),[[now,0]])
 assert.deepEqual(diagnostics.podMetricTrendPoints(history,'web','memory'),[[now,0]])
 assert.deepEqual(diagnostics.podMetricTrendPoints(history,'sidecar','cpu'),[[now,null]])
 for(const changes of [{uid:'other'},{state:'forbidden'},{metrics_at:new Date(now-120001).toISOString()},{metrics_at:new Date(now+60001).toISOString()},{metrics_at:'invalid'},{containers:[{name:'web',cpu:-1,memory:NaN}]}]){
  const result=diagnostics.appendPodMetricSample(null,pod,{...data,...changes},now)
  assert.equal(result.points.length,0,JSON.stringify(changes))
 }
 assert.equal(diagnostics.appendPodMetricSample(null,{...pod,metadata:{}},data,now),null)
 const newer={...pod,metadata:{...pod.metadata,creationTimestamp:new Date(now+1).toISOString()}}
 assert.equal(diagnostics.appendPodMetricSample(null,newer,data,now).points.length,0)
})
test('Pod 折线按指标时间去重与排序，上限 60 点，不改写已采集历史',()=>{
 const first=Date.parse('2026-10-27T08:00:00Z'),pod={metadata:{uid:'pod-a'},spec:{containers:[{name:'web'}]}}
 const data=at=>({uid:'pod-a',state:'ok',metrics_at:new Date(at).toISOString(),containers:[{name:'web',cpu:at-first,memory:1024}]})
 let history=diagnostics.appendPodMetricSample(null,pod,data(first),first)
 const original=JSON.stringify(history)
 const duplicate=diagnostics.appendPodMetricSample(history,pod,{...data(first),containers:[{name:'web',cpu:99,memory:0}]},first)
 assert.deepEqual(duplicate,history);assert.equal(JSON.stringify(history),original)
 history=diagnostics.appendPodMetricSample(history,pod,data(first+2000),first+2000)
 history=diagnostics.appendPodMetricSample(history,pod,data(first+1000),first+2000)
 assert.deepEqual(history.points.map(point=>point.at),[first,first+1000,first+2000])
 for(let index=3;index<70;index++)history=diagnostics.appendPodMetricSample(history,pod,data(first+index*1000),first+index*1000)
 assert.equal(history.points.length,60);assert.equal(history.points[0].at,first+10000);assert.equal(history.points.at(-1).at,first+69000)
 assert.equal(diagnostics.appendPodMetricSample(history,pod,data(first+70000),first+70000,500).points.length,60)
 assert.deepEqual(diagnostics.appendPodMetricSample(history,pod,null,first+70000).points,history.points)
})
test('Pod 替换 UID 时清除折线，容器重启前采样不归给新实例',()=>{
 const now=Date.parse('2026-10-27T08:00:00Z'),pod={metadata:{uid:'pod-a'},spec:{containers:[{name:'web'},{name:'sidecar'}]},status:{containerStatuses:[{name:'web',state:{running:{startedAt:new Date(now-1000).toISOString()}}}]}}
 const data={uid:'pod-a',state:'ok',metrics_at:new Date(now).toISOString(),containers:[{name:'web',cpu:.2,memory:1024},{name:'sidecar',cpu:.1}]}
 const history=diagnostics.appendPodMetricSample(null,pod,data,now)
 assert.equal(history.points.length,1)
 const recreated={...pod,metadata:{uid:'pod-b'}}
 assert.deepEqual(diagnostics.appendPodMetricSample(history,recreated,data,now),{uid:'pod-b',points:[]})
 const next=diagnostics.appendPodMetricSample(history,recreated,{...data,uid:'pod-b'},now)
 assert.equal(next.uid,'pod-b');assert.equal(next.points.length,1)
 const old={...data,metrics_at:new Date(now-2000).toISOString()}
 const restarted=diagnostics.appendPodMetricSample(null,pod,old,now)
 assert.deepEqual(diagnostics.podMetricTrendPoints(restarted,'web','cpu'),[[now-2000,null]])
 assert.deepEqual(diagnostics.podMetricTrendPoints(restarted,'sidecar','cpu'),[[now-2000,.1]])
 assert.deepEqual(diagnostics.podMetricTrendPoints(restarted,'sidecar','memory'),[[now-2000,null]])
})
test('等待重启的容器不能显示上次实例用量，已采集历史和配置限制继续保留',()=>{
 const now=Date.parse('2026-10-27T08:00:00Z'),pod={metadata:{uid:'pod-a'},spec:{containers:[{name:'web',resources:{requests:{cpu:'100m'},limits:{memory:'1Gi'}}}]},status:{containerStatuses:[{name:'web',state:{running:{startedAt:new Date(now-3600000).toISOString()}}}]}}
 const metrics={uid:'pod-a',state:'ok',metrics_at:new Date(now-30000).toISOString(),containers:[{name:'web',cpu:.2,memory:128*1024**2}]}
 const history=diagnostics.appendPodMetricSample(null,pod,metrics,now)
 pod.status.containerStatuses[0].state={waiting:{reason:'CrashLoopBackOff'}}
 const rows=diagnostics.podMetricRows(pod,metrics,now)
 assert.equal(rows[0].cpu.used,null);assert.equal(rows[0].memory.used,null)
 assert.equal(rows[0].cpu.requests,.1);assert.equal(rows[0].memory.limits,1024**3)
 const retained=diagnostics.appendPodMetricSample(history,pod,{...metrics,metrics_at:new Date(now).toISOString()},now)
 assert.deepEqual(retained.points,history.points);assert.equal(retained.interrupted,true);assert.deepEqual(diagnostics.podMetricTrendPoints(retained,'web','cpu'),[[now-30000,.2]])
 pod.status.containerStatuses[0].state={running:{startedAt:new Date(now-10000).toISOString()}}
 assert.equal(diagnostics.podMetricRows(pod,metrics,now)[0].cpu.used,null)
 assert.equal(diagnostics.podMetricRows(pod,{...metrics,metrics_at:new Date(now).toISOString()},now)[0].cpu.used,.2)
})
test('多容器长时间采样内存有界，每个容器的缺失样本独立保留为空值',()=>{
 const start=Date.parse('2026-10-27T08:00:00Z'),pod={metadata:{uid:'pod-a'},spec:{containers:[{name:'web'},{name:'sidecar'},{name:'metrics'}]}}
 let history=null
 for(let index=0;index<1000;index++){
  const at=start+index*30000,data={uid:'pod-a',state:'ok',metrics_at:new Date(at).toISOString(),containers:[{name:'web',cpu:index,memory:0},{name:'sidecar',cpu:0},...(index%2?[{name:'metrics',memory:1024}]:[])]}
  history=diagnostics.appendPodMetricSample(history,pod,data,at)
  assert.ok(history.points.length<=60)
 }
 assert.equal(history.points.length,60);assert.equal(history.points.reduce((total,point)=>total+point.containers.length,0),180)
 assert.deepEqual(diagnostics.podMetricTrendPoints(history,'web','cpu').at(-1),[start+999*30000,999])
 assert.ok(diagnostics.podMetricTrendPoints(history,'sidecar','memory').every(point=>point[1]===null))
 const metrics=diagnostics.podMetricTrendPoints(history,'metrics','memory');assert.equal(metrics[0][1],null);assert.equal(metrics[1][1],1024)
})
test('时间轴只在真实采样范围外留白，单点约一分钟视野且不增加数据',()=>{
 const now=Date.parse('2026-10-27T08:00:00Z'),history={uid:'pod-a',points:[{at:now-60000,containers:[{name:'web',cpu:1,memory:null}]},{at:now,containers:[{name:'web',cpu:0,memory:128}]}]},original=structuredClone(history)
 assert.deepEqual(diagnostics.podMetricTrendRange(history,'cpu'),{min:now-63000,max:now+3000})
 assert.deepEqual(diagnostics.podMetricTrendRange(history,'memory'),{min:now-30000,max:now+30000})
 assert.deepEqual(history,original);assert.equal(diagnostics.podMetricTrendPoints(history,'web','memory').length,2)
 assert.equal(diagnostics.podMetricTrendRange(null,'cpu'),undefined)
 assert.equal(diagnostics.podMetricTrendRange({uid:'pod-a',points:[{at:now,containers:[{name:'web',cpu:null,memory:NaN}]}]},'memory'),undefined)
})
test('不可用和全空响应只标记有界断档，恢复点强制拆线且不增加失败采样',()=>{
 const start=Date.parse('2026-10-27T08:00:00Z'),pod={metadata:{uid:'pod-a'},spec:{containers:[{name:'web'}]}},data=at=>({uid:'pod-a',state:'ok',metrics_at:new Date(at).toISOString(),containers:[{name:'web',cpu:.2,memory:128}]})
 const failures=[null,{...data(start+30000),state:'forbidden'},{...data(start+30000),state:'unavailable'},{...data(start+30000),containers:[]},{...data(start+30000),containers:[{name:'web',cpu:null,memory:null}]},{...data(start-180000)}]
 for(const failed of failures){
  const initial=diagnostics.appendPodMetricSample(null,pod,data(start),start),original=structuredClone(initial)
  let history=diagnostics.appendPodMetricSample(initial,pod,failed,start+30000)
  assert.equal(history.interrupted,true);assert.deepEqual(history.points,initial.points);assert.deepEqual(initial,original)
  history=diagnostics.appendPodMetricSample(history,pod,data(start),start+60000)
  assert.equal(history.interrupted,true);assert.equal(history.points.length,1)
  history=diagnostics.appendPodMetricSample(history,pod,data(start+60000),start+60000)
  assert.equal(history.interrupted,undefined);assert.equal(history.points.length,2);assert.equal(history.points[1].breakBefore,true)
  assert.deepEqual(diagnostics.podMetricTrendPoints(history,'web','cpu'),[[start,.2],[start+60000,.2]])
  assert.deepEqual(diagnostics.podMetricTrendSegments(history,'web','cpu'),[[[start,.2]],[[start+60000,.2]]])
  history=diagnostics.appendPodMetricSample(history,pod,data(start+90000),start+90000)
  assert.deepEqual(diagnostics.podMetricTrendSegments(history,'web','memory'),[[[start,128]],[[start+60000,128],[start+90000,128]]])
 }
})
test('断档后迟到的旧时间采样不能清除待恢复标志，重复失败不会积累额外历史',()=>{
 const start=Date.parse('2026-10-27T08:00:00Z'),pod={metadata:{uid:'pod-a'},spec:{containers:[{name:'web'}]}},data=at=>({uid:'pod-a',state:'ok',metrics_at:new Date(at).toISOString(),containers:[{name:'web',cpu:0,memory:0}]})
 let history=diagnostics.appendPodMetricSample(diagnostics.appendPodMetricSample(null,pod,data(start),start),pod,data(start+60000),start+60000)
 history=diagnostics.interruptPodMetricHistory(history,pod)
 history=diagnostics.appendPodMetricSample(history,pod,data(start+30000),start+90000)
 assert.equal(history.interrupted,true)
 for(let index=0;index<1000;index++)history=diagnostics.interruptPodMetricHistory(history,pod)
 assert.equal(history.points.length,3);assert.deepEqual(Object.keys(history).sort(),['interrupted','points','uid'])
 history=diagnostics.appendPodMetricSample(history,pod,data(start+90000),start+90000)
 assert.deepEqual(diagnostics.podMetricTrendSegments(history,'web','cpu'),[[[start,0],[start+30000,0],[start+60000,0]],[[start+90000,0]]])
 for(let index=4;index<1000;index++){
  history=diagnostics.interruptPodMetricHistory(history,pod)
  history=diagnostics.appendPodMetricSample(history,pod,data(start+index*30000),start+index*30000)
 }
 assert.equal(history.points.length,60);assert.equal(diagnostics.podMetricTrendSegments(history,'web','cpu').length,60)
 assert.ok(history.points.every(point=>point.containers.length===1&&point.containers[0].cpu===0))
 assert.deepEqual(diagnostics.interruptPodMetricHistory(history,{metadata:{uid:'other'}}),{uid:'other',points:[]})
})
test('折线采集错误与过期不显示为当前用量，切换对象及卸载清理采样并取消请求',async()=>{
 let unmount;const requests=[],watchers=[]
 const component=load('../src/components/KubeResourceDiagnostics.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(fn){unmount=fn},watch(source,callback){watchers.push({source,callback})}},'vue-i18n':{useI18n:()=>({t:k=>k,locale:vue.ref('zh-CN')})},'echarts/core':{use(){}},'@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})},'@/api/kubernetes':{...api,kubeErrorMessage:e=>e.message},'@/utils/kubernetesDiagnostics':diagnostics,'@/api/client':{__esModule:true,default:{get:(url,options)=>new Promise((resolve,reject)=>requests.push({url,options,resolve,reject}))}}}).default
 const pod=uid=>({kind:'Pod',metadata:{name:'web',namespace:'team',uid},spec:{containers:[{name:'web',resources:{requests:{cpu:'100m'},limits:{memory:'1Gi'}}}]}}),props=vue.reactive({cluster:'cluster',object:pod('p1'),type:api.kubeTypes.find(type=>type.kind==='Pod'),trend:true,autoRefresh:false,canFollow:()=>true})
 const state=component.setup(props,{expose(){},emit(){}}),now=Date.now(),metrics={uid:'p1',state:'ok',metrics_at:new Date(now).toISOString(),containers:[{name:'web',cpu:0,memory:1024}]}
 const first=state.load();requests[0].resolve({data:metrics});await first
 assert.equal(state.history.value.points.length,1);assert.equal(state.trendCurrent.value,true);assert.equal(state.rows.value[0].cpu.used,0)
 state.now.value=now+120001;assert.equal(state.trendCurrent.value,false);assert.equal(state.rows.value[0].cpu.used,null);assert.equal(state.trendHint.value,'k8s.podTrendStale');assert.equal(state.history.value.points.length,1)
 const failed=state.load();requests[1].reject(new Error('metrics unavailable'));await failed
 assert.equal(state.history.value.points.length,1);assert.equal(state.trendCurrent.value,false);assert.equal(state.trendHint.value,'k8s.podTrendUnavailable');assert.equal(state.rows.value[0].cpu.requests,.1);assert.equal(state.rows.value[0].memory.limits,1024**3)
 const pending=state.load(),identity=watchers.find(watch=>typeof watch.source==='function'&&watch.source()===state.context.value)
 props.object=pod('p2');identity.callback();assert.equal(state.history.value,null);assert.equal(requests[2].options.signal.aborted,true)
 requests[2].resolve({data:metrics});await pending;assert.equal(state.history.value,null)
 requests[3].resolve({data:{...metrics,uid:'p2'}});await vue.nextTick();assert.equal(state.history.value.uid,'p2')
 const last=state.load(),previousDocument=globalThis.document;globalThis.document={removeEventListener(){}};unmount();requests[4].resolve({data:{...metrics,uid:'p2'}});await last;globalThis.document=previousDocument
 assert.equal(state.history.value,null);assert.equal(requests[4].options.signal.aborted,true)
})
test('折线使用真实时间轴、断档不连线且单点可见，默认诊断仍使用柱状图',async()=>{
 const options=[],plots=[]
 const component=load('../src/components/KubeResourceDiagnostics.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},'vue-i18n':{useI18n:()=>({t:k=>k,locale:vue.ref('zh-CN')})},'echarts/core':{use(){},init(){const plot={setOption(value){options.push(value)},resize(){}};plots.push(plot);return plot}},'@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})},'@/api/kubernetes':api,'@/utils/kubernetesDiagnostics':diagnostics,'@/utils/time':{formatDateTime:value=>new Date(value).toISOString()}}).default
 const now=Date.now(),pod={kind:'Pod',metadata:{name:'web',namespace:'team',uid:'p1'},spec:{containers:[{name:'web',resources:{requests:{cpu:'100m'},limits:{memory:'1Gi'}}},{name:'metrics'}],initContainers:[{name:'prepare',resources:{requests:{cpu:'25m'}}}]}},props=vue.reactive({cluster:'cluster',object:pod,type:api.kubeTypes.find(type=>type.kind==='Pod'),trend:true,compact:true,autoRefresh:false,canFollow:()=>true}),state=component.setup(props,{expose(){},emit(){}})
 const data=at=>({uid:'p1',state:'ok',metrics_at:new Date(at).toISOString(),containers:[{name:'web',cpu:.2,memory:128*1024**2},{name:'metrics',cpu:0}]})
 state.data.value=data(now);state.now.value=now;state.history.value=diagnostics.appendPodMetricSample(diagnostics.appendPodMetricSample(null,pod,data(now-300000),now-300000),pod,data(now),now)
 state.root.value={querySelector:()=>({clientWidth:640})}
 const previousStyle=globalThis.getComputedStyle;globalThis.getComputedStyle=()=>({getPropertyValue:key=>({'--edo-muted':'#888','--edo-border':'#ddd','--edo-primary':'#5276ff'}[key]??'')})
 try{
  await state.render();assert.equal(plots.length,2);assert.equal(options[0].xAxis.type,'time');assert.equal(options[1].xAxis.type,'time');assert.equal(state.chartHeight.value,125)
  props.compact=false;assert.equal(state.chartHeight.value,180)
  assert.equal(options[0].series.length,4);assert.ok(options[0].series.every(series=>series.type==='line'&&series.connectNulls===false&&series.symbolSize===6))
  assert.deepEqual(options[0].legend.data,['web','metrics']);assert.deepEqual(options[1].legend.data,['web'])
  assert.deepEqual(options[0].series.flatMap(series=>series.data),[[now-300000,200],[now,200],[now-300000,0],[now,0]])
  assert.deepEqual(options[1].series.flatMap(series=>series.data),[[now-300000,128],[now,128]])
  assert.equal(options[0].xAxis.min,now-315000);assert.equal(options[0].xAxis.max,now+15000)
  assert.equal(state.metricValue('cpu',1.5),'1500 m');assert.equal(state.metricValue('cpu',.000001),'<0.01 m');assert.equal(state.metricValue('memory',1),'<0.01 MiB');assert.equal(state.metricValue('cpu',null),'—')
  assert.match(options[0].tooltip.formatter([{seriesName:'web',value:[now,200]}]),/web  200 m/)
  state.history.value=diagnostics.appendPodMetricSample(null,pod,data(now),now);await state.render()
  const single=options.at(-2);assert.equal(single.xAxis.min,now-30000);assert.equal(single.xAxis.max,now+30000);assert.deepEqual(single.series[0].data,[[now,200]])
  state.data.value={uid:'p1',state:'forbidden'};assert.equal(state.trendHint.value,'k8s.podTrendForbidden');assert.equal(state.history.value.points.length,1)
  props.trend=false;await state.render();assert.equal(options.at(-2).xAxis.type,'value');assert.ok(options.at(-2).series.every(series=>series.type==='bar'))
  assert.equal(options.at(-2).series[1].itemStyle.color,'#93a6c3');assert.equal(options.at(-2).series[2].itemStyle.color,'#cdd5e1');assert.equal(state.chartHeight.value,262)
 }finally{globalThis.getComputedStyle=previousStyle}
})
test('诊断请求冻结完整对象范围，集群切换但 UID 相同也丢弃迟到结果与错误',async()=>{
 const requests=[],component=load('../src/components/KubeResourceDiagnostics.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},'vue-i18n':{useI18n:()=>({t:k=>k,locale:vue.ref('zh-CN')})},'echarts/core':{use(){}},'@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})},'@/api/kubernetes':{...api,kubeErrorMessage:e=>e.message},'@/utils/kubernetesDiagnostics':diagnostics,'@/api/client':{__esModule:true,default:{get:(url,options)=>new Promise((resolve,reject)=>requests.push({url,options,resolve,reject}))}}}).default
 for(const change of [props=>{props.cluster='cluster-b'},props=>{props.object.metadata.namespace='other'},props=>{props.object.metadata.name='other'},props=>{props.object.metadata.uid='p2'},props=>{props.type={...props.type,resource:'services'}},props=>{props.type={...props.type,apiVersion:'other/v1'}}]){
  const props=vue.reactive({cluster:'cluster-a',object:{kind:'Pod',metadata:{name:'web',namespace:'team',uid:'p1'},spec:{containers:[{name:'web'}]}},type:api.kubeTypes.find(type=>type.kind==='Pod'),trend:true,autoRefresh:false,canFollow:()=>true}),state=component.setup(props,{expose(){},emit(){}})
  const pending=state.load(),request=requests.at(-1)
  change(props)
  assert.equal(request.url,'/kubernetes/clusters/cluster-a/resources/pods/web/diagnostics');assert.deepEqual(request.options.params,{namespace:'team',uid:'p1'})
  request.resolve({data:{uid:'p1',state:'ok',metrics_at:new Date().toISOString(),containers:[{name:'web',cpu:0,memory:0}]}});await pending
  assert.equal(state.data.value,null);assert.equal(state.history.value,null);assert.equal(state.error.value,'')
  const failed=state.load(),failure=requests.at(-1);props.cluster='cluster-c';failure.reject(new Error('旧范围错误'));await failed
  assert.equal(state.error.value,'')
 }
})
test('实际诊断请求失败、拒绝、不可用或全空后恢复，短于两分钟也不跨故障连线',async()=>{
 const requests=[],component=load('../src/components/KubeResourceDiagnostics.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){},watch(){}},'vue-i18n':{useI18n:()=>({t:k=>k,locale:vue.ref('zh-CN')})},'echarts/core':{use(){}},'@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})},'@/api/kubernetes':{...api,kubeErrorMessage:e=>e.message},'@/utils/kubernetesDiagnostics':diagnostics,'@/api/client':{__esModule:true,default:{get:(url,options)=>new Promise((resolve,reject)=>requests.push({url,options,resolve,reject}))}}}).default
 const now=Date.now(),data=at=>({uid:'p1',state:'ok',metrics_at:new Date(at).toISOString(),containers:[{name:'web',cpu:.2,memory:128}]})
 for(const failure of ['request','forbidden','unavailable','empty']){
  const props={cluster:'cluster-a',object:{kind:'Pod',metadata:{name:'web',namespace:'team',uid:'p1'},spec:{containers:[{name:'web'}]}},type:api.kubeTypes.find(type=>type.kind==='Pod'),trend:true,autoRefresh:false,canFollow:()=>true},state=component.setup(props,{expose(){},emit(){}})
  const first=state.load();requests.at(-1).resolve({data:data(now-60000)});await first
  const failed=state.load()
  if(failure==='request')requests.at(-1).reject(new Error('指标读取失败'))
  else requests.at(-1).resolve({data:failure==='empty'?{...data(now-30000),containers:[]}:{...data(now-30000),state:failure}})
  await failed
  assert.equal(state.history.value.interrupted,true);assert.equal(state.history.value.points.length,1)
  const recovered=state.load();requests.at(-1).resolve({data:data(now)});await recovered
  assert.equal(state.history.value.points.length,2);assert.equal(state.history.value.points[1].breakBefore,true)
  assert.deepEqual(state.trendSeries('cpu',.001,'#5276ff').map(series=>series.data),[[[now-60000,200]],[[now,200]]])
  assert.deepEqual(state.trendSeries('memory',1,'#5276ff').map(series=>series.data),[[[now-60000,128]],[[now,128]]])
 }
})
test('紧凑趋势的资源核对默认折叠，普通趋势默认展开且所有容器配置仍可查看',async()=>{
 const component=load('../src/components/KubeResourceDiagnostics.vue',{vue:{...vue,onMounted(){},onBeforeUnmount(){}},'vue-i18n':{useI18n:()=>({t:key=>key==='k8s.podTrendResources'?'当前用量与资源配置':key,te:()=>false,locale:vue.ref('zh-CN')})},'echarts/core':{use(){}},'@/stores/preferences':{usePreferencesStore:()=>({theme:'light'})},'@/api/kubernetes':api,'@/utils/kubernetesDiagnostics':diagnostics,'./ResourceTable.vue':{__esModule:true,default:{render:()=>vue.h('div')}}},true).default
 const props={cluster:'cluster',object:{kind:'Pod',metadata:{name:'web',namespace:'team',uid:'p1'},spec:{containers:[{name:'web',resources:{requests:{cpu:'100m'},limits:{memory:'1Gi'}}}],initContainers:[{name:'prepare',resources:{requests:{cpu:'25m'}}}]}},type:api.kubeTypes.find(type=>type.kind==='Pod'),trend:true,autoRefresh:false,canFollow:()=>true}
 const render=async compact=>{
  const app=vue.createSSRApp(component,{...props,compact}),stub={setup:(_props,{slots})=>()=>vue.h('div',slots.default?.())}
  for(const name of ['a-alert','a-button','a-card','a-tag','a-timeline','a-timeline-item'])app.component(name,stub)
  return renderToString(app)
 }
 const compact=await render(true),expanded=await render(false)
 assert.doesNotMatch(compact.match(/<details\b([^>]*)>/)[1],/\bopen\b/)
 assert.match(expanded.match(/<details\b([^>]*)>/)[1],/\bopen\b/)
 for(const html of [compact,expanded]){
  assert.match(html,/<summary>当前用量与资源配置<\/summary>/)
  for(const value of ['prepare','25 m','100 m','1 GiB'])assert.ok(html.includes(value),value)
 }
})
