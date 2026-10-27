import assert from 'node:assert/strict'
import {test} from 'node:test'
import {readFileSync} from 'node:fs'
import ts from 'typescript'
import {parse,compileScript} from '@vue/compiler-sfc'
import * as vue from 'vue'
function load(path,modules={}){
 const source=readFileSync(new URL(path,import.meta.url),'utf8'),code=path.endsWith('.vue')?compileScript(parse(source).descriptor,{id:'diagnostics-test'}).content:source
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
