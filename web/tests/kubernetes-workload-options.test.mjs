import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {parse,compileScript,compileTemplate} from '@vue/compiler-sfc'
import ts from 'typescript'
import * as vue from 'vue'

function load(path,modules={},globals={}){
 const source=readFileSync(new URL(path,import.meta.url),'utf8')
 const script=path.endsWith('.vue')?compileScript(parse(source).descriptor,{id:'workload-options-test'}).content:source
 const output=ts.transpileModule(script,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,module={exports:{}}
 new Function('require','module','exports',...Object.keys(globals),output)(name=>modules[name]??{},module,module.exports,...Object.values(globals))
 return module.exports
}
const api=load('../src/api/kubernetes.ts')
const workload=load('../src/utils/kubernetesWorkloadForm.ts',{'@/api/kubernetes':api})
const {workloadSettings,buildWorkloadSettings}=workload
const clone=value=>JSON.parse(JSON.stringify(value))
function mergePatch(before,patch){
 const result=clone(before)
 for(const [key,value] of Object.entries(patch)){
  if(value===null)delete result[key]
  else if(value&&typeof value==='object'&&!Array.isArray(value))result[key]=mergePatch(result[key]??{},value)
  else result[key]=clone(value)
 }
 return result
}
const failure=(fn,key)=>assert.throws(fn,error=>error instanceof Error&&error.message===`k8s.${key}`)

test('任务默认项留空，零值及显式类型正确读取，节点选择器草稿不修改原对象',()=>{
 const empty=workloadSettings({},'Job')
 for(const key of ['parallelism','completions','backoffLimit','ttlSecondsAfterFinished','activeDeadlineSeconds','successfulJobsHistoryLimit','failedJobsHistoryLimit','startingDeadlineSeconds','partition'])assert.equal(empty[key],null,key)
 assert.equal(empty.updateStrategy,'RollingUpdate');assert.equal(empty.daemonMaxSurge,'0');assert.equal(empty.daemonMaxUnavailable,'1')
 const original={metadata:{name:'timer'},spec:{successfulJobsHistoryLimit:0,jobTemplate:{spec:{parallelism:0,backoffLimit:0,ttlSecondsAfterFinished:0,template:{spec:{serviceAccountName:'runner',nodeSelector:{pool:'batch'}}}}}}}
 const settings=workloadSettings(original,'CronJob')
 assert.equal(settings.parallelism,0);assert.equal(settings.backoffLimit,0);assert.equal(settings.ttlSecondsAfterFinished,0);assert.equal(settings.successfulJobsHistoryLimit,0)
 assert.equal(settings.serviceAccountName,'runner');settings.nodeSelector[0].value='other';assert.equal(original.spec.jobTemplate.spec.template.spec.nodeSelector.pool,'batch')
 assert.equal(workloadSettings({kind:'Pod',spec:{serviceAccountName:'pod-account'}}).serviceAccountName,'pod-account')
})

test('未修改选项不写回默认值，保留原 int-or-percent 类型及未知配置',()=>{
 for(const kind of ['Pod','Deployment','StatefulSet','DaemonSet','Job','CronJob']){
  const original={kind,spec:{futureField:{kept:true},updateStrategy:{type:'RollingUpdate',rollingUpdate:{maxSurge:0,maxUnavailable:'25%'}},template:{spec:{hostNetwork:true,affinity:{future:true}}}}}
  assert.deepEqual(buildWorkloadSettings(kind,workloadSettings(original,kind),original),{spec:{},pod:{}},kind)
  assert.deepEqual(buildWorkloadSettings(kind,workloadSettings({},kind)),{spec:{},pod:{}},`${kind} defaults`)
 }
})

test('Job 新建选项保持真实零，只写任务字段，不接收 CronJob 或 Deployment 专属设置',()=>{
 const settings=workloadSettings({},'Job')
 Object.assign(settings,{parallelism:0,completions:1,backoffLimit:0,ttlSecondsAfterFinished:0,activeDeadlineSeconds:1,timeZone:'not-a-zone',successfulJobsHistoryLimit:-1,updateStrategy:'Recreate'})
 assert.deepEqual(buildWorkloadSettings('Job',settings),{spec:{parallelism:0,completions:1,backoffLimit:0,ttlSecondsAfterFinished:0,activeDeadlineSeconds:1},pod:{}})
 const deployment=workloadSettings({},'Deployment');Object.assign(deployment,{parallelism:-1,updateStrategy:'Recreate',partition:-1})
 assert.deepEqual(buildWorkloadSettings('Deployment',deployment),{spec:{},pod:{}})
})

test('CronJob 显式类型将任务字段写入 jobTemplate，账号与调度字段独立返回',()=>{
 const original={metadata:{name:'timer'},spec:{jobTemplate:{metadata:{labels:{keep:'yes'}},spec:{completionMode:'Indexed',template:{spec:{hostNetwork:true}}}}}}
 const settings=workloadSettings(original,'CronJob')
 Object.assign(settings,{parallelism:2,completions:3,backoffLimit:4,ttlSecondsAfterFinished:0,activeDeadlineSeconds:30,timeZone:'Asia/Shanghai',concurrencyPolicy:'Forbid',successfulJobsHistoryLimit:0,failedJobsHistoryLimit:2,startingDeadlineSeconds:1,serviceAccountName:'batch.runner',nodeSelector:[{key:'kubernetes.io/arch',value:'arm64'}]})
 const result=buildWorkloadSettings('CronJob',settings,original)
 assert.deepEqual(result.spec,{jobTemplate:{spec:{parallelism:2,completions:3,backoffLimit:4,ttlSecondsAfterFinished:0,activeDeadlineSeconds:30}},successfulJobsHistoryLimit:0,failedJobsHistoryLimit:2,startingDeadlineSeconds:1,concurrencyPolicy:'Forbid',timeZone:'Asia/Shanghai'})
 assert.deepEqual(result.pod,{serviceAccountName:'batch.runner',nodeSelector:{'kubernetes.io/arch':'arm64'}})
 const merged=mergePatch(original.spec,result.spec);assert.equal(merged.jobTemplate.metadata.labels.keep,'yes');assert.equal(merged.jobTemplate.spec.completionMode,'Indexed');assert.equal(merged.jobTemplate.spec.template.spec.hostNetwork,true)
 assert.equal(Object.hasOwn(result.spec.jobTemplate.spec,'template'),false)
})

test('清空已有可选任务、CronJob 和账号字段发 null，删除节点标签发逐键 tombstone',()=>{
 const job={spec:{parallelism:2,completions:3,backoffLimit:4,ttlSecondsAfterFinished:5,activeDeadlineSeconds:6,template:{spec:{serviceAccountName:'old',nodeSelector:{remove:'old',keep:'yes'},hostPID:true}}}}
 const settings=workloadSettings(job,'Job')
 for(const field of ['parallelism','completions','backoffLimit','ttlSecondsAfterFinished','activeDeadlineSeconds'])settings[field]=null
 settings.serviceAccountName='';settings.nodeSelector=[{key:'keep',value:'changed'}]
 const result=buildWorkloadSettings('Job',settings,job)
 assert.deepEqual(result.spec,{parallelism:null,completions:null,backoffLimit:null,ttlSecondsAfterFinished:null,activeDeadlineSeconds:null})
 assert.deepEqual(result.pod,{serviceAccountName:null,nodeSelector:{keep:'changed',remove:null}})
 const merged=mergePatch(job.spec.template.spec,result.pod);assert.equal(merged.hostPID,true);assert.equal(merged.serviceAccountName,undefined);assert.deepEqual(merged.nodeSelector,{keep:'changed'})
 const cron={spec:{timeZone:'UTC',concurrencyPolicy:'Replace',successfulJobsHistoryLimit:1,failedJobsHistoryLimit:2,startingDeadlineSeconds:3}}
 const cleared=workloadSettings(cron,'CronJob');Object.assign(cleared,{timeZone:'',concurrencyPolicy:'',successfulJobsHistoryLimit:null,failedJobsHistoryLimit:null,startingDeadlineSeconds:null})
 assert.deepEqual(buildWorkloadSettings('CronJob',cleared,cron).spec,{successfulJobsHistoryLimit:null,failedJobsHistoryLimit:null,startingDeadlineSeconds:null,concurrencyPolicy:null,timeZone:null})
})

test('任务数字仅接受安全整数，类型专用校验错误稳定',()=>{
 for(const kind of ['Job','CronJob'])for(const [field,bad] of [['parallelism',-1],['completions',-1],['backoffLimit',1.2],['ttlSecondsAfterFinished',NaN],['activeDeadlineSeconds',-1],['parallelism','2'],['completions',Infinity],['backoffLimit',Number.MAX_SAFE_INTEGER+1]]){
  const settings=workloadSettings({},kind);settings[field]=bad
  failure(()=>buildWorkloadSettings(kind,settings),'invalidJobSettings')
 }
 for(const [field,bad] of [['successfulJobsHistoryLimit',-1],['failedJobsHistoryLimit',1.2],['startingDeadlineSeconds',-1],['startingDeadlineSeconds','3']]){
  const settings=workloadSettings({},'CronJob');settings[field]=bad
  failure(()=>buildWorkloadSettings('CronJob',settings),'invalidCronJobSettings')
 }
 const invalid=workloadSettings({},'CronJob');invalid.concurrencyPolicy='Overlap';failure(()=>buildWorkloadSettings('CronJob',invalid),'invalidCronJobSettings')
})

test('Job 和 CronJob 接受零值，int32 字段拒绝溢出，int64 截止时间仅保留安全整数',()=>{
 for(const kind of ['Job','CronJob']){
  const settings=workloadSettings({},kind)
  for(const field of ['parallelism','completions','backoffLimit','ttlSecondsAfterFinished','activeDeadlineSeconds'])settings[field]=0
  const result=buildWorkloadSettings(kind,settings),job=kind==='CronJob'?result.spec.jobTemplate.spec:result.spec
  assert.deepEqual(job,{parallelism:0,completions:0,backoffLimit:0,ttlSecondsAfterFinished:0,activeDeadlineSeconds:0})
 }
 for(const field of ['parallelism','completions','backoffLimit','ttlSecondsAfterFinished']){
  const settings=workloadSettings({},'Job');settings[field]=2147483647;assert.equal(buildWorkloadSettings('Job',settings).spec[field],2147483647)
  settings[field]=2147483648;failure(()=>buildWorkloadSettings('Job',settings),'invalidJobSettings')
 }
 for(const field of ['successfulJobsHistoryLimit','failedJobsHistoryLimit']){
  const settings=workloadSettings({},'CronJob');settings[field]=2147483647;assert.equal(buildWorkloadSettings('CronJob',settings).spec[field],2147483647)
  settings[field]=2147483648;failure(()=>buildWorkloadSettings('CronJob',settings),'invalidCronJobSettings')
 }
 for(const field of ['activeDeadlineSeconds','startingDeadlineSeconds']){
  const settings=workloadSettings({},'CronJob');settings[field]=Number.MAX_SAFE_INTEGER
  const result=buildWorkloadSettings('CronJob',settings);assert.equal(field==='activeDeadlineSeconds'?result.spec.jobTemplate.spec[field]:result.spec[field],Number.MAX_SAFE_INTEGER)
  settings[field]=Number.MAX_SAFE_INTEGER+1;failure(()=>buildWorkloadSettings('CronJob',settings),field==='activeDeadlineSeconds'?'invalidJobSettings':'invalidCronJobSettings')
 }
 const cron=workloadSettings({},'CronJob');cron.startingDeadlineSeconds=0;assert.equal(buildWorkloadSettings('CronJob',cron).spec.startingDeadlineSeconds,0)
})

test('CronJob 时区支持 UTC 与可识别 IANA 名称，不接受本地缩写和不存在的区域',()=>{
 for(const timeZone of ['UTC','Asia/Shanghai','America/New_York','Etc/GMT+8']){
  const settings=workloadSettings({},'CronJob');settings.timeZone=timeZone;assert.equal(buildWorkloadSettings('CronJob',settings).spec.timeZone,timeZone)
 }
 for(const timeZone of ['CST','EST','Shanghai','+08:00','Asia/NoSuchPlace','not/a/zone']){
  const settings=workloadSettings({},'CronJob');settings.timeZone=timeZone;failure(()=>buildWorkloadSettings('CronJob',settings),'invalidCronJobSettings')
 }
})

test('旧对象未编辑的未知值不阻断其他字段保存，也不重新回写未知字段',()=>{
 const cron={spec:{timeZone:'CST',concurrencyPolicy:'FuturePolicy',successfulJobsHistoryLimit:-1,jobTemplate:{spec:{parallelism:'future',template:{spec:{serviceAccountName:'old'}}}}}}
 const settings=workloadSettings(cron,'CronJob');settings.serviceAccountName='new'
 assert.deepEqual(buildWorkloadSettings('CronJob',settings,cron),{spec:{},pod:{serviceAccountName:'new'}})
 const daemon={spec:{updateStrategy:{type:'FutureStrategy',rollingUpdate:{maxSurge:'future',maxUnavailable:'future'}},template:{spec:{serviceAccountName:'old'}}}}
 const updated=workloadSettings(daemon,'DaemonSet');updated.serviceAccountName='new'
 assert.deepEqual(buildWorkloadSettings('DaemonSet',updated,daemon),{spec:{},pod:{serviceAccountName:'new'}})
})

test('StatefulSet 分区补丁不丢滚动更新扩展字段，OnDelete 明确移除 rollingUpdate',()=>{
 const original={spec:{updateStrategy:{type:'RollingUpdate',future:'kept',rollingUpdate:{partition:2,maxUnavailable:'20%'}},persistentVolumeClaimRetentionPolicy:{whenDeleted:'Retain'}}}
 const settings=workloadSettings(original,'StatefulSet');settings.partition=0
 const result=buildWorkloadSettings('StatefulSet',settings,original)
 assert.deepEqual(result.spec,{updateStrategy:{rollingUpdate:{partition:0}}})
 assert.deepEqual(mergePatch(original.spec,result.spec).updateStrategy,{type:'RollingUpdate',future:'kept',rollingUpdate:{partition:0,maxUnavailable:'20%'}})
 settings.partition=null;assert.deepEqual(buildWorkloadSettings('StatefulSet',settings,original).spec,{updateStrategy:{rollingUpdate:{partition:null}}})
 settings.updateStrategy='OnDelete';assert.deepEqual(buildWorkloadSettings('StatefulSet',settings,original).spec,{updateStrategy:{type:'OnDelete',rollingUpdate:null}})
 const onDelete={spec:{updateStrategy:{type:'OnDelete'}}},next=workloadSettings(onDelete,'StatefulSet');next.updateStrategy='RollingUpdate'
 assert.deepEqual(buildWorkloadSettings('StatefulSet',next,onDelete).spec,{updateStrategy:{type:'RollingUpdate'}})
})

test('StatefulSet 校验分区整数和专用策略，DaemonSet 不接受 Deployment 的 Recreate',()=>{
 for(const partition of [-1,1.2,NaN,'3']){const settings=workloadSettings({},'StatefulSet');settings.partition=partition;failure(()=>buildWorkloadSettings('StatefulSet',settings),'invalidWorkloadStrategy')}
 for(const kind of ['StatefulSet','DaemonSet']){const settings=workloadSettings({},kind);settings.updateStrategy='Recreate';failure(()=>buildWorkloadSettings(kind,settings),'invalidWorkloadStrategy')}
})

test('DaemonSet 保留 IntOrString 类型，数量使用 int32、百分数最大 100 且恰好一项非零',()=>{
 for(const [surge,unavailable,expected] of [['2147483647','0',{maxSurge:2147483647,maxUnavailable:0}],['100%','0%',{maxSurge:'100%',maxUnavailable:'0%'}],['0%','100%',{maxSurge:'0%',maxUnavailable:'100%'}]]){
  const settings=workloadSettings({},'DaemonSet');settings.daemonMaxSurge=surge;settings.daemonMaxUnavailable=unavailable
  assert.deepEqual(buildWorkloadSettings('DaemonSet',settings).spec,{updateStrategy:{rollingUpdate:expected}})
 }
 for(const [surge,unavailable] of [['0','0'],['0%','0%'],['1','1'],['10%','10%'],['101%','0'],['2147483648','0'],['-1','1'],['01','0'],['.5','0'],['1','bad'],['1','']]){
  const settings=workloadSettings({},'DaemonSet');settings.daemonMaxSurge=surge;settings.daemonMaxUnavailable=unavailable
  failure(()=>buildWorkloadSettings('DaemonSet',settings),'invalidWorkloadStrategy')
 }
 const original={spec:{updateStrategy:{type:'RollingUpdate',rollingUpdate:{maxSurge:'25%',maxUnavailable:1}}}}
 const settings=workloadSettings(original,'DaemonSet');settings.updateStrategy='OnDelete'
 assert.deepEqual(buildWorkloadSettings('DaemonSet',settings,original).spec,{updateStrategy:{type:'OnDelete',rollingUpdate:null}})
 const onDelete={spec:{updateStrategy:{type:'OnDelete',rollingUpdate:{maxSurge:1,maxUnavailable:1}}}},invalid=workloadSettings(onDelete,'DaemonSet');invalid.updateStrategy='RollingUpdate'
 failure(()=>buildWorkloadSettings('DaemonSet',invalid,onDelete),'invalidWorkloadStrategy')
 const unknown={spec:{updateStrategy:{type:'OnDelete',rollingUpdate:{maxSurge:'future',maxUnavailable:0}}}},unrecognized=workloadSettings(unknown,'DaemonSet');unrecognized.updateStrategy='RollingUpdate'
 failure(()=>buildWorkloadSettings('DaemonSet',unrecognized,unknown),'invalidWorkloadStrategy')
})

test('DaemonSet 保留合法百分数前导零，修改另一预算不拒绝或回写未改值',()=>{
 const original={spec:{updateStrategy:{type:'RollingUpdate',rollingUpdate:{maxSurge:'00%',maxUnavailable:1}}}}
 const settings=workloadSettings(original,'DaemonSet');assert.equal(settings.daemonMaxSurge,'00%');settings.daemonMaxUnavailable='2'
 const result=buildWorkloadSettings('DaemonSet',settings,original)
 assert.deepEqual(result.spec,{updateStrategy:{rollingUpdate:{maxUnavailable:2}}})
 assert.equal(mergePatch(original.spec,result.spec).updateStrategy.rollingUpdate.maxSurge,'00%')
 const next=workloadSettings({},'DaemonSet');next.daemonMaxSurge='001%';next.daemonMaxUnavailable='00%'
 assert.deepEqual(buildWorkloadSettings('DaemonSet',next).spec,{updateStrategy:{rollingUpdate:{maxSurge:'001%',maxUnavailable:'00%'}}})
 next.daemonMaxSurge='00101%';failure(()=>buildWorkloadSettings('DaemonSet',next),'invalidWorkloadStrategy')
})

test('账号采用 DNS 子域名，节点标签允许合法前缀与空值，拒绝非法及重复标签',()=>{
 const settings=workloadSettings({},'Deployment');settings.serviceAccountName='batch.runner-2';settings.nodeSelector=[{key:'example.com/Pool_Name',value:'Arm_64'},{key:'empty',value:''}]
 assert.deepEqual(buildWorkloadSettings('Deployment',settings).pod,{serviceAccountName:'batch.runner-2',nodeSelector:{'example.com/Pool_Name':'Arm_64',empty:''}})
 for(const name of ['Bad','bad_name','bad..name','-bad','bad.','a'.repeat(254)]){
  const invalid=workloadSettings({},'Pod');invalid.serviceAccountName=name;failure(()=>buildWorkloadSettings('Pod',invalid),'invalidServiceAccount')
 }
 for(const rows of [[{key:'example.com/a/b',value:'ok'}],[{key:'UPPER.example/key',value:'ok'}],[{key:'a'.repeat(64),value:'ok'}],[{key:'bad key',value:'ok'}],[{key:'key',value:'has space'}],[{key:'key',value:'a'.repeat(64)}],[{key:'same',value:'a'},{key:'same',value:'b'}],[{key:'',value:'ok'}]]){
  const invalid=workloadSettings({},'Pod');invalid.nodeSelector=rows;failure(()=>buildWorkloadSettings('Pod',invalid),'invalidNodeSelector')
 }
})

test('构建不修改原始对象或草稿，未曝光 Pod 配置只由合并补丁保留',()=>{
 const original={kind:'Pod',metadata:{uid:'p1'},spec:{serviceAccountName:'before',nodeSelector:{pool:'old'},affinity:{nodeAffinity:{future:true}},tolerations:[{key:'dedicated',operator:'Exists'}],securityContext:{runAsNonRoot:true}}}
 const settings=workloadSettings(original);settings.serviceAccountName='after';settings.nodeSelector=[]
 const before=JSON.stringify(original),draft=JSON.stringify(settings),result=buildWorkloadSettings('Pod',settings,original)
 assert.equal(JSON.stringify(original),before);assert.equal(JSON.stringify(settings),draft)
 assert.deepEqual(Object.keys(result.pod).sort(),['nodeSelector','serviceAccountName'])
 const merged=mergePatch(original.spec,result.pod);assert.deepEqual(merged.affinity,original.spec.affinity);assert.deepEqual(merged.tolerations,original.spec.tolerations);assert.deepEqual(merged.securityContext,original.spec.securityContext);assert.deepEqual(merged.nodeSelector,{})
})

function editorHarness(kind,{object,request,validate,currentObject=object}={}){
 const requests=[],reads=[],validations=[],order=[],dialogs=[],dialogUpdates=[],events=[],unmounts=[]
 const client={
  get:async(url,options)=>{order.push('read');reads.push({url,options});return {data:{object:clone(currentObject)}}},
  post:async(url,body,options)=>{order.push(url.endsWith('/validate-create')?'validate-create':'validate');validations.push({url,body:clone(body),options});return validate?validate(url,body,options):{data:{valid:true}}},
  request:async options=>{order.push(`write:${options.method}`);requests.push(clone(options));return request?request(options):{data:{}}},
 }
 const liveAPI=load('../src/api/kubernetes.ts',{'./client':{__esModule:true,default:client},axios:{__esModule:true,default:{isAxiosError:error=>Boolean(error.response)}},'./resources':{apiErrorMessage:()=> '保存失败，请重试'}})
 const component=load('../src/components/KubeResourceEditor.vue',{
  vue:{...vue,onMounted(){},onBeforeUnmount:fn=>unmounts.push(fn)},'vue-i18n':{useI18n:()=>({t:key=>key})},
  'ant-design-vue':{Modal:{confirm:options=>{dialogs.push(options);return {destroy(){},update(value){dialogUpdates.push(value)}}}},message:{success(){}}},
  '@/api/client':{__esModule:true,default:client},'@/api/kubernetes':liveAPI,'@/utils/kubernetesWorkloadForm':workload,
 },{window:{addEventListener(){},removeEventListener(){}}}).default
 const props=vue.reactive({open:true,cluster:'cluster-a',namespace:'team',type:api.kubeTypes.find(type=>type.kind===kind),object})
 const scope=vue.effectScope();let state
 scope.run(()=>{state=component.setup(props,{expose(){},emit:(...args)=>events.push(args)})})
 if(!object){state.form.name='web';state.form.containers[0].image='app:1'}
 let stopped=false
 const stop=()=>{if(stopped)return;stopped=true;unmounts.forEach(fn=>fn());scope.stop()}
 return {state,props,requests,reads,validations,order,dialogs,dialogUpdates,events,unmount:stop,stop}
}
const podFixture=()=>({containers:[{name:'app',image:'app:1',resources:{limits:{cpu:'1'}},livenessProbe:{httpGet:{path:'/ready',port:8080}},env:[{name:'TOKEN',valueFrom:{secretKeyRef:{name:'token',key:'value'}}}],volumeMounts:[{name:'projected',mountPath:'/config'}],securityContext:{runAsNonRoot:true}}],initContainers:[{name:'init',image:'busybox:1',command:['true']}],volumes:[{name:'projected',projected:{sources:[{configMap:{name:'config'}}]}}],serviceAccountName:'old',nodeSelector:{pool:'batch'},affinity:{nodeAffinity:{future:true}},tolerations:[{key:'batch',operator:'Exists'}]})
function workloadFixture(kind){
 const template={metadata:{labels:{app:'web'},annotations:{keep:'yes'}},spec:podFixture()}
 const spec=kind==='CronJob'?{schedule:'0 * * * *',timeZone:'UTC',jobTemplate:{metadata:{labels:{future:'yes'}},spec:{parallelism:2,completionMode:'NonIndexed',template}}}:{replicas:2,serviceName:'web-headless',parallelism:2,completionMode:'NonIndexed',template,updateStrategy:{type:'RollingUpdate',rollingUpdate:{partition:2,maxSurge:0,maxUnavailable:1}}}
 return {apiVersion:kind==='Job'||kind==='CronJob'?'batch/v1':'apps/v1',kind,metadata:{name:'web',namespace:'team',uid:'uid-1',resourceVersion:'12'},spec,status:{readyReplicas:2}}
}

test('真实编辑器创建 Job 提交零值选项与 Pod 账号，不把任务配置写进 template',async t=>{
 const h=editorHarness('Job');t.after(h.stop)
 Object.assign(h.state.workloadOptions,{parallelism:0,completions:0,backoffLimit:0,ttlSecondsAfterFinished:0,activeDeadlineSeconds:0,serviceAccountName:'batch.runner',nodeSelector:[{key:'pool',value:'batch'}]})
 await h.state.save()
 assert.equal(h.requests.length,1);assert.equal(h.requests[0].method,'POST');assert.equal(h.dialogs.length,0)
 assert.deepEqual(h.order,['validate-create','write:POST']);assert.equal(h.validations.length,1);assert.equal(h.validations[0].url,`${h.requests[0].url}/validate-create`);assert.deepEqual(h.validations[0].body,h.requests[0].data)
 const object=h.requests[0].data
 assert.equal(object.spec.parallelism,0);assert.equal(object.spec.completions,0);assert.equal(object.spec.activeDeadlineSeconds,0)
 assert.equal(object.spec.template.spec.serviceAccountName,'batch.runner');assert.deepEqual(object.spec.template.spec.nodeSelector,{pool:'batch'});assert.equal(object.spec.template.spec.parallelism,undefined)
 assert.equal(object.spec.template.spec.restartPolicy,'OnFailure');assert.deepEqual(h.events,[['update:open',false],['saved']])
})

test('真实编辑器 CronJob 创建保留 template 并嵌套任务选项，Cron 规则留在根 spec',t=>{
 const h=editorHarness('CronJob');t.after(h.stop)
 h.state.form.schedule='30 2 * * *';Object.assign(h.state.workloadOptions,{parallelism:2,completions:3,backoffLimit:0,ttlSecondsAfterFinished:30,timeZone:'Asia/Shanghai',concurrencyPolicy:'Forbid',successfulJobsHistoryLimit:0,failedJobsHistoryLimit:1,startingDeadlineSeconds:0})
 const object=h.state.buildObject(),job=object.spec.jobTemplate.spec
 assert.equal(object.spec.schedule,'30 2 * * *');assert.equal(object.spec.timeZone,'Asia/Shanghai');assert.equal(object.spec.concurrencyPolicy,'Forbid');assert.equal(object.spec.startingDeadlineSeconds,0)
 assert.equal(object.spec.parallelism,undefined);assert.equal(job.parallelism,2);assert.equal(job.completions,3);assert.equal(job.ttlSecondsAfterFinished,30);assert.equal(job.template.spec.containers[0].image,'app:1');assert.equal(job.template.spec.restartPolicy,'OnFailure')
})

test('真实编辑器 StatefulSet 和 DaemonSet 使用各自更新策略，不改变 Deployment 路径',t=>{
 const stateful=editorHarness('StatefulSet');t.after(stateful.stop);stateful.state.form.serviceName='web-headless';stateful.state.workloadOptions.partition=0
 let object=stateful.state.buildObject();assert.equal(object.spec.serviceName,'web-headless');assert.deepEqual(object.spec.updateStrategy,{rollingUpdate:{partition:0}});assert.equal(object.spec.strategy,undefined)
 stateful.state.workloadOptions.updateStrategy='OnDelete';object=stateful.state.buildObject();assert.deepEqual(object.spec.updateStrategy,{type:'OnDelete',rollingUpdate:null})
 const daemon=editorHarness('DaemonSet');t.after(daemon.stop);daemon.state.workloadOptions.daemonMaxSurge='100%';daemon.state.workloadOptions.daemonMaxUnavailable='0'
 object=daemon.state.buildObject();assert.deepEqual(object.spec.updateStrategy,{rollingUpdate:{maxSurge:'100%',maxUnavailable:0}})
 daemon.state.workloadOptions.updateStrategy='OnDelete';assert.deepEqual(daemon.state.buildObject().spec.updateStrategy,{type:'OnDelete',rollingUpdate:null})
 const deployment=editorHarness('Deployment');t.after(deployment.stop);deployment.state.form.strategy='Recreate';assert.deepEqual(deployment.state.buildObject().spec.strategy,{type:'Recreate',rollingUpdate:null});assert.equal(deployment.state.buildObject().spec.updateStrategy,undefined)
})

test('真实编辑器修改选项保留容器与 Init 配置、身份及未曝光 Pod 字段',t=>{
 for(const kind of ['Job','CronJob','StatefulSet','DaemonSet']){
  const original=workloadFixture(kind),before=JSON.stringify(original),h=editorHarness(kind,{object:original});t.after(h.stop)
  h.state.form.containers[0].image='app:2';h.state.workloadOptions.serviceAccountName='next';h.state.workloadOptions.nodeSelector=[]
  if(kind==='Job'||kind==='CronJob')h.state.workloadOptions.parallelism=0
  else if(kind==='StatefulSet')h.state.workloadOptions.partition=1
  else h.state.workloadOptions.daemonMaxUnavailable='2'
  const object=h.state.buildObject(),job=kind==='CronJob'?object.spec.jobTemplate.spec:object.spec,pod=job.template.spec
  assert.equal(object.metadata.uid,'uid-1',kind);assert.equal(object.metadata.resourceVersion,'12',kind);assert.equal(object.status,undefined)
  assert.equal(pod.containers[0].image,'app:2');assert.equal(pod.containers[0].livenessProbe.httpGet.path,'/ready');assert.deepEqual(pod.containers[0].env[0].valueFrom,{secretKeyRef:{name:'token',key:'value'}});assert.equal(pod.containers[0].securityContext.runAsNonRoot,true)
  assert.deepEqual(pod.initContainers[0].command,['true']);assert.equal(pod.volumes[0].projected.sources[0].configMap.name,'config');assert.equal(pod.serviceAccountName,'next');assert.deepEqual(pod.nodeSelector,{pool:null})
  assert.equal(Object.hasOwn(pod,'affinity'),false);assert.equal(Object.hasOwn(pod,'tolerations'),false)
  const merged=mergePatch(original,object),mergedJob=kind==='CronJob'?merged.spec.jobTemplate.spec:merged.spec
  assert.deepEqual(mergedJob.template.spec.affinity,podFixture().affinity);assert.deepEqual(mergedJob.template.spec.tolerations,podFixture().tolerations);assert.equal(mergedJob.template.metadata.annotations.keep,'yes');assert.equal(mergedJob.completionMode,'NonIndexed')
  if(kind==='CronJob')assert.equal(merged.spec.jobTemplate.metadata.labels.future,'yes')
  assert.equal(JSON.stringify(original),before)
 }
})

test('真实编辑器清空已有 CronJob 选项和账号时生成明确删除补丁',t=>{
 const original=workloadFixture('CronJob'),h=editorHarness('CronJob',{object:original});t.after(h.stop)
 h.state.workloadOptions.timeZone='';h.state.workloadOptions.parallelism=null;h.state.workloadOptions.serviceAccountName=''
 const object=h.state.buildObject();assert.equal(object.spec.timeZone,null);assert.equal(object.spec.jobTemplate.spec.parallelism,null);assert.equal(object.spec.jobTemplate.spec.template.spec.serviceAccountName,null)
})

test('真实编辑器按任务、Cron 选项、策略和调度错误定位页签，不提交无效设置',async t=>{
 for(const [kind,field,value,tab,key] of [['Job','parallelism',-1,'job','invalidJobSettings'],['CronJob','timeZone','CST','basic','invalidCronJobSettings'],['StatefulSet','partition',-1,'strategy','invalidWorkloadStrategy'],['DaemonSet','daemonMaxUnavailable','0','strategy','invalidWorkloadStrategy'],['Job','serviceAccountName','Bad_Name','scheduling','invalidServiceAccount'],['Job','nodeSelector',[{key:'bad key',value:'value'}],'scheduling','invalidNodeSelector']]){
  const h=editorHarness(kind);t.after(h.stop);if(kind==='StatefulSet')h.state.form.serviceName='web-headless'
  h.state.workloadOptions[field]=value;await h.state.save()
  assert.equal(h.state.editorTab.value,tab,`${kind} ${field}`);assert.equal(h.state.error.value,`k8s.${key}`);assert.equal(h.requests.length,0);assert.equal(h.dialogs.length,0)
 }
})

test('真实 CronJob 编辑器选项变更标记草稿，确认提交失败后保留配置且不关闭抽屉',async t=>{
 const original=workloadFixture('CronJob'),h=editorHarness('CronJob',{object:original,request:async()=>{throw new Error('private infrastructure failure')}});t.after(h.stop)
 assert.equal(h.props.type.update,true)
 assert.equal(h.state.dirty.value,false);h.state.workloadOptions.parallelism=3;h.state.workloadOptions.serviceAccountName='next';assert.equal(h.state.dirty.value,true)
 const draft=JSON.stringify(h.state.workloadOptions)
 await h.state.save();assert.equal(h.dialogs.length,1);assert.equal(h.state.confirming.value,true)
 await h.state.save();assert.equal(h.dialogs.length,1)
 await assert.rejects(h.dialogs[0].onOk())
 assert.equal(h.requests.length,1);assert.equal(h.requests[0].method,'PATCH');assert.equal(h.reads.length,1);assert.equal(h.requests[0].data.metadata.resourceVersion,'12');assert.equal(h.requests[0].data.spec.jobTemplate.spec.parallelism,3)
 assert.deepEqual(h.order,['read','validate','write:PATCH']);assert.equal(h.validations.length,1);assert.deepEqual(h.validations[0].body,h.requests[0].data)
 assert.equal(h.state.error.value,'保存失败，请重试');assert.equal(h.state.saving.value,false);assert.equal(h.state.dirty.value,true);assert.equal(JSON.stringify(h.state.workloadOptions),draft);assert.equal(h.state.form.containers[0].image,'app:1');assert.deepEqual(h.events,[])
})

test('真实 CronJob 编辑先读取最新版本，再 dry-run 同一补丁，最后才 PATCH',async t=>{
 const original=workloadFixture('CronJob'),currentObject=clone(original);currentObject.metadata.resourceVersion='13';currentObject.status={active:[]}
 const h=editorHarness('CronJob',{object:original,currentObject});t.after(h.stop)
 h.state.workloadOptions.parallelism=3;await h.state.save();assert.equal(h.dialogs.length,1);assert.equal(h.validations.length,0);assert.equal(h.requests.length,0)
 await h.dialogs[0].onOk()
 assert.deepEqual(h.order,['read','validate','write:PATCH']);assert.equal(h.reads.length,1);assert.equal(h.validations[0].url,`${h.requests[0].url}/validate`)
 assert.deepEqual(h.validations[0].body,h.requests[0].data);assert.equal(h.validations[0].body.metadata.resourceVersion,'13');assert.equal(h.validations[0].body.spec.jobTemplate.spec.parallelism,3)
 assert.equal(h.validations[0].options.params.namespace,'team');assert.equal(h.validations[0].options.timeout,30000);assert.ok(h.validations[0].options.signal instanceof AbortSignal)
 assert.equal(h.reads[0].options.signal,h.validations[0].options.signal)
 assert.deepEqual(h.events,[['update:open',false],['saved']])
})

test('创建及编辑 dry-run 失败不产生真实写入或成功关闭，保留原草稿',async t=>{
 for(const editing of [false,true]){
  const h=editorHarness(editing?'CronJob':'Job',{object:editing?workloadFixture('CronJob'):undefined,validate:async()=>{throw new Error('private dry-run failure')}});t.after(h.stop)
  h.state.workloadOptions.parallelism=3;const draft=JSON.stringify(h.state.workloadOptions)
  await h.state.save()
  if(editing){assert.equal(h.dialogs.length,1);await assert.rejects(h.dialogs[0].onOk())}
  assert.deepEqual(h.order,editing?['read','validate']:['validate-create']);assert.equal(h.validations.length,1);assert.equal(h.requests.length,0);assert.deepEqual(h.events,[])
  assert.equal(h.state.error.value,'保存失败，请重试');assert.equal(h.state.saving.value,false);assert.equal(h.state.dirty.value,true);assert.equal(JSON.stringify(h.state.workloadOptions),draft)
 }
})

const settle=async()=>{await vue.nextTick();await new Promise(resolve=>setImmediate(resolve));await vue.nextTick()}
function changeEditorContext(h,change){
 if(change==='cluster')h.props.cluster='cluster-b'
 else if(change==='name'||change==='uid')h.props.object={...clone(h.props.object),metadata:{...h.props.object.metadata,[change]:`${change}-other`}}
 else if(change==='close')h.props.open=false
 else h.unmount()
}

test('编辑验证中切换集群、名称、UID、关闭或卸载会中止旧请求，迟到响应不写入或污染新状态',async t=>{
 for(const change of ['cluster','name','uid','close','unmount']){
  let finish
  const h=editorHarness('CronJob',{object:workloadFixture('CronJob'),validate:()=>new Promise(resolve=>{finish=resolve})});t.after(h.stop)
  h.state.workloadOptions.parallelism=3;await h.state.save();const saving=h.dialogs[0].onOk();await settle()
  assert.equal(h.validations.length,1,change);const signal=h.validations[0].options.signal;assert.equal(signal.aborted,false)
  changeEditorContext(h,change);await settle();assert.equal(signal.aborted,true,change)
  h.state.error.value='新上下文提示';h.state.saving.value=true
  finish({data:{valid:true}});await Promise.allSettled([saving]);await settle()
  assert.equal(h.requests.length,0,change);assert.deepEqual(h.events,[],change);assert.equal(h.state.error.value,'新上下文提示',change);assert.equal(h.state.saving.value,true,change)
 }
})

test('创建验证中关闭抽屉会中止验证，迟到成功不创建资源或关闭新的抽屉',async t=>{
 let finish
 const h=editorHarness('Job',{validate:()=>new Promise(resolve=>{finish=resolve})});t.after(h.stop)
 h.state.workloadOptions.parallelism=0;const saving=h.state.save();await settle();assert.equal(h.validations.length,1)
 h.props.open=false;await settle();assert.equal(h.validations[0].options.signal.aborted,true)
 h.state.error.value='新上下文提示';h.state.saving.value=true;finish({data:{valid:true}});await Promise.allSettled([saving]);await settle()
 assert.equal(h.requests.length,0);assert.deepEqual(h.events,[]);assert.equal(h.state.error.value,'新上下文提示');assert.equal(h.state.saving.value,true)
})

test('确认期间上下文失效后，旧确认回调不读取、验证或写入资源',async t=>{
 for(const change of ['cluster','name','uid','close','unmount']){
  const h=editorHarness('CronJob',{object:workloadFixture('CronJob')});t.after(h.stop)
  h.state.workloadOptions.parallelism=3;await h.state.save();assert.equal(h.dialogs.length,1)
  const confirm=h.dialogs[0];changeEditorContext(h,change);await settle();h.state.error.value='新上下文提示'
  await Promise.allSettled([Promise.resolve().then(()=>confirm.onOk())]);await settle()
  assert.deepEqual(h.order,[],change);assert.equal(h.reads.length,0);assert.equal(h.validations.length,0);assert.equal(h.requests.length,0);assert.deepEqual(h.events,[]);assert.equal(h.state.error.value,'新上下文提示')
 }
})

test('同一确认窗口 dry-run 失败可重试，重试保存中重复确认不重复验证或写入',async t=>{
 let attempts=0,finish
 const h=editorHarness('CronJob',{object:workloadFixture('CronJob'),validate:()=>{if(++attempts===1)throw new Error('private dry-run failure');return new Promise(resolve=>{finish=resolve})}});t.after(h.stop)
 h.state.workloadOptions.parallelism=3;const draft=JSON.stringify(h.state.workloadOptions)
 await h.state.save();const confirmation=h.dialogs[0]
 await assert.rejects(confirmation.onOk())
 assert.equal(h.state.saving.value,false);assert.equal(h.state.error.value,'保存失败，请重试');assert.equal(JSON.stringify(h.state.workloadOptions),draft);assert.equal(h.requests.length,0);assert.deepEqual(h.events,[])
 assert.deepEqual(h.dialogUpdates.at(-1),{cancelButtonProps:{disabled:false},keyboard:true})
 const retry=confirmation.onOk();await settle();assert.equal(h.state.saving.value,true);assert.equal(h.validations.length,2);assert.equal(h.reads.length,2)
 await confirmation.onOk();assert.equal(h.validations.length,2);assert.equal(h.reads.length,2);assert.equal(h.requests.length,0)
 finish({data:{valid:true}});await retry
 assert.deepEqual(h.order,['read','validate','read','validate','write:PATCH']);assert.equal(h.requests.length,1);assert.deepEqual(h.validations[1].body,h.requests[0].data);assert.equal(h.state.saving.value,false);assert.equal(h.state.error.value,'');assert.deepEqual(h.events,[['update:open',false],['saved']])
})

test('表单确认保存中锁定取消与关闭入口，强制取消或关闭仍会中止校验且迟到结果不写入',async t=>{
 for(const close of ['onCancel','afterClose']){
  let finish
  const h=editorHarness('CronJob',{object:workloadFixture('CronJob'),validate:()=>new Promise(resolve=>{finish=resolve})});t.after(h.stop)
  h.state.workloadOptions.parallelism=3;const draft=JSON.stringify(h.state.workloadOptions)
  await h.state.save();const confirmation=h.dialogs[0],pending=confirmation.onOk();await settle()
  assert.equal(confirmation.maskClosable,false);assert.equal(confirmation.closable,false);assert.deepEqual(h.dialogUpdates.at(-1),{cancelButtonProps:{disabled:true},keyboard:false})
  const signal=h.validations[0].options.signal;assert.equal(signal.aborted,false)
  confirmation[close]();assert.equal(signal.aborted,true);assert.equal(h.state.saving.value,false);assert.equal(h.state.confirming.value,false);assert.equal(h.state.dirty.value,true)
  assert.equal(JSON.stringify(h.state.workloadOptions),draft);await confirmation.onOk();assert.equal(h.validations.length,1);assert.equal(h.reads.length,1)
  h.state.error.value='新窗口提示';h.state.saving.value=true;finish({data:{valid:true}});await pending
  assert.equal(h.requests.length,0);assert.deepEqual(h.events,[]);assert.equal(h.state.error.value,'新窗口提示');assert.equal(h.state.saving.value,true)
 }
})

test('确认校验失败后可以正常取消保留草稿，旧取消窗口不再接受确认',async t=>{
 const h=editorHarness('CronJob',{object:workloadFixture('CronJob'),validate:async()=>{throw Error('private dry-run failure')}});t.after(h.stop)
 h.state.workloadOptions.parallelism=3;await h.state.save();const confirmation=h.dialogs[0],draft=JSON.stringify(h.state.workloadOptions)
 await assert.rejects(confirmation.onOk());assert.deepEqual(h.dialogUpdates.at(-1),{cancelButtonProps:{disabled:false},keyboard:true})
 confirmation.onCancel();confirmation.afterClose();assert.equal(h.state.confirming.value,false);assert.equal(h.state.saving.value,false);assert.equal(JSON.stringify(h.state.workloadOptions),draft)
 await confirmation.onOk();assert.equal(h.validations.length,1);assert.equal(h.requests.length,0);assert.deepEqual(h.events,[])
 await h.state.save();assert.equal(h.dialogs.length,2)
})

test('旧确认窗口关闭与迟到校验不影响新确认窗口的提交',async t=>{
 const finishes=[]
 const h=editorHarness('CronJob',{object:workloadFixture('CronJob'),validate:()=>new Promise(resolve=>finishes.push(resolve))});t.after(h.stop)
 h.state.workloadOptions.parallelism=3;await h.state.save();const old=h.dialogs[0],oldPending=old.onOk();await settle();old.onCancel()
 await h.state.save();const fresh=h.dialogs[1],freshPending=fresh.onOk();await settle();assert.equal(h.validations.length,2)
 old.afterClose();assert.equal(h.state.confirming.value,true);assert.equal(h.state.saving.value,true);assert.equal(h.validations[1].options.signal.aborted,false)
 finishes[0]({data:{valid:true}});await oldPending;assert.equal(h.requests.length,0);assert.equal(h.state.saving.value,true)
 finishes[1]({data:{valid:true}});await freshPending;assert.equal(h.requests.length,1);assert.deepEqual(h.events,[['update:open',false],['saved']])
})

test('工作负载表单与容器字段模板可编译，每个控件可访问名称复用对应可见标签',()=>{
 const controls=new Set(['a-input','a-input-number','a-select','a-switch','a-textarea'])
 const property=(node,name)=>node.props.find(prop=>prop.type===6&&prop.name===name||prop.name==='bind'&&prop.arg?.content===name)
 const value=prop=>prop.type===6?{text:prop.value?.content??''}:{expression:prop.exp?.content??''}
 for(const file of ['KubeResourceEditor.vue','KubeContainerFields.vue']){
  const source=readFileSync(new URL(`../src/components/${file}`,import.meta.url),'utf8'),{descriptor,errors}=parse(source)
  assert.deepEqual(errors,[],file)
  const compiled=compileTemplate({source:descriptor.template.content,filename:file,id:'workload-accessible-form',compilerOptions:{expressionPlugins:['typescript']}})
  assert.deepEqual(compiled.errors,[],file)
  let count=0
  const visit=(node,label)=>{
   if(node.type!==1)return
   if(node.tag==='a-form-item'){
    assert.equal(property(node,'name'),undefined,`${file} 不新增表单模型字段`)
    const visible=property(node,'label');if(visible)label=value(visible)
   }
   if(controls.has(node.tag)){
    count++;const accessible=property(node,'aria-label')
    assert.ok(accessible,`${file}: ${node.loc.source}`)
    if(label)assert.deepEqual(value(accessible),label,`${file} 可访问名称与可见标签一致`)
    else assert.deepEqual(value(accessible),{expression:'t(`k8s.${key}`)'},`${file} 独立探针开关使用对应分区标题`)
   }
   for(const child of node.children)visit(child,label)
  }
  descriptor.template.ast.children.forEach(node=>visit(node))
  assert.equal(count,file==='KubeResourceEditor.vue'?55:22,`${file} 必须遍历全部实际表单控件`)
 }
})
