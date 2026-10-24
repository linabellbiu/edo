import {parseKubeQuantity,type KubeObject} from '@/api/kubernetes'
type Pair={key:string;value:string}
export type WorkloadSettingsForm={parallelism:number|null;completions:number|null;backoffLimit:number|null;ttlSecondsAfterFinished:number|null;activeDeadlineSeconds:number|null;timeZone:string;concurrencyPolicy:string;successfulJobsHistoryLimit:number|null;failedJobsHistoryLimit:number|null;startingDeadlineSeconds:number|null;updateStrategy:string;partition:number|null;daemonMaxSurge:string;daemonMaxUnavailable:string;serviceAccountName:string;nodeSelector:Pair[]}
export type ProbeForm={enabled:boolean;kind:string;path:string;port:string;command:string;scheme:string;initialDelaySeconds:number;periodSeconds:number;timeoutSeconds:number;successThreshold:number;failureThreshold:number;raw:KubeObject}
export type ContainerForm={name:string;image:string;port:string;env:Pair[];imagePullPolicy:string;cpuRequest:string;cpuLimit:string;memoryRequest:string;memoryLimit:string;command:string;args:string;mounts:Array<{name:string;mountPath:string;subPath:string;readOnly:boolean;raw:KubeObject}>;probes:Record<string,ProbeForm>;sidecar:boolean;raw:KubeObject}
const copy=(v:any)=>JSON.parse(JSON.stringify(v))
export function probeForm(raw:KubeObject={}):ProbeForm {
 const kind=['httpGet','tcpSocket','exec','grpc'].find(key=>raw[key])??'httpGet'
 return {enabled:Object.keys(raw).length>0,kind,path:raw.httpGet?.path??'/',port:String(raw[kind]?.port??8080),command:(raw.exec?.command??[]).join('\n'),scheme:raw.httpGet?.scheme??'HTTP',initialDelaySeconds:raw.initialDelaySeconds??0,periodSeconds:raw.periodSeconds??10,timeoutSeconds:raw.timeoutSeconds??1,successThreshold:raw.successThreshold??1,failureThreshold:raw.failureThreshold??3,raw:copy(raw)}
}
export function containerForm(raw:KubeObject={name:'app',image:''}):ContainerForm {
 return {name:raw.name,image:raw.image??'',port:(raw.ports??[]).map((p:KubeObject)=>p.containerPort).join(','),env:(raw.env??[]).filter((e:KubeObject)=>e.value!==undefined).map((e:KubeObject)=>({key:e.name,value:e.value})),imagePullPolicy:raw.imagePullPolicy??'',cpuRequest:raw.resources?.requests?.cpu??'',cpuLimit:raw.resources?.limits?.cpu??'',memoryRequest:raw.resources?.requests?.memory??'',memoryLimit:raw.resources?.limits?.memory??'',command:(raw.command??[]).join('\n'),args:(raw.args??[]).join('\n'),mounts:(raw.volumeMounts??[]).map((m:KubeObject)=>({name:m.name,mountPath:m.mountPath,subPath:m.subPath??'',readOnly:m.readOnly??false,raw:copy(m)})),probes:Object.fromEntries(['startupProbe','readinessProbe','livenessProbe'].map(key=>[key,probeForm(raw[key])])),sidecar:raw.restartPolicy==='Always',raw:copy(raw)}
}
function fail(key:string):never{throw new Error(`k8s.${key}`)}
function portValue(text:string){if(/^\d+$/.test(text)){const n=Number(text);if(n<1||n>65535)fail('invalidPort');return n}if(!/^[a-z]([-a-z0-9]*[a-z0-9])?$/.test(text)||text.length>15)fail('invalidPort');return text}
function probeObject(form:ProbeForm,key:string){
 if(!form.enabled)return undefined
 if(JSON.stringify(form)===JSON.stringify(probeForm(form.raw)))return copy(form.raw)
 const object=copy(form.raw)
 for(const kind of ['httpGet','tcpSocket','exec','grpc'])delete object[kind]
 const old=form.raw[form.kind]??{}
 if(form.kind==='exec'){if(!form.command.trim())fail('invalidProbe');object.exec={...old,command:form.command===(old.command??[]).join('\n')?copy(old.command):form.command.split('\n')}}
 else {const port=portValue(form.port);if(form.kind==='grpc'&&typeof port!=='number')fail('invalidProbe');object[form.kind]={...old,port};if(form.kind==='httpGet'){if(!form.path.startsWith('/'))fail('invalidProbe');Object.assign(object.httpGet,{path:form.path,scheme:form.scheme})}}
 for(const field of ['initialDelaySeconds','periodSeconds','timeoutSeconds','successThreshold','failureThreshold'] as const){const n=form[field];if(!Number.isInteger(n)||n<(field==='initialDelaySeconds'?0:1)||key!=='readinessProbe'&&field==='successThreshold'&&n!==1)fail('invalidProbe');object[field]=n}
 return object
}
export function buildContainer(form:ContainerForm,init=false):KubeObject {
 if(!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(form.name)||form.name.length>63||!form.image.trim())fail('imageRequired')
 const old=form.raw??{},object={...copy(old),name:form.name,image:form.image.trim()}
 const ports=form.port.trim()?form.port.split(',').map(p=>Number(p.trim())):[]
 const portsUnchanged=form.port===(old.ports??[]).map((p:KubeObject)=>p.containerPort).join(',')
 if(ports.some(n=>!Number.isInteger(n)||n<1||n>65535)||!portsUnchanged&&new Set(ports).size!==ports.length)fail('invalidPort')
 object.ports=portsUnchanged?copy(old.ports??[]):ports.map(containerPort=>({...old.ports?.find((p:KubeObject)=>p.containerPort===containerPort),containerPort}))
 const names=new Set<string>();for(const row of form.env){if(!row.key.trim()||names.has(row.key.trim()))fail('invalidKeys');names.add(row.key.trim())}
 const values=new Map(form.env.map(e=>[e.key.trim(),e.value])),written=new Set<string>()
 object.env=(old.env??[]).flatMap((e:KubeObject)=>{if(values.has(e.name)){written.add(e.name);return [{name:e.name,value:values.get(e.name)}]}return e.valueFrom?[copy(e)]:[]})
 object.env.push(...[...values].filter(([name])=>!written.has(name)).map(([name,value])=>({name,value})))
 if(form.imagePullPolicy)object.imagePullPolicy=form.imagePullPolicy;else delete object.imagePullPolicy
 for(const field of ['command','args'] as const){if(old[field]!==undefined&&form[field]===old[field].join('\n'))object[field]=copy(old[field]);else if(form[field]!=='')object[field]=form[field].split('\n');else delete object[field]}
 const resources=copy(old.resources??{})
 for(const [field,suffix] of [['requests','Request'],['limits','Limit']]){
  const values=copy(resources[field]??{})
  for(const resource of ['cpu','memory']){const value=String((form as any)[resource+suffix]).trim();if(value){if(parseKubeQuantity(value)===undefined)fail('invalidResources');values[resource]=value}else delete values[resource]}
  if(Object.keys(values).length)resources[field]=values;else delete resources[field]
 }
 for(const resource of ['cpu','memory']){const request=parseKubeQuantity(resources.requests?.[resource]),limit=parseKubeQuantity(resources.limits?.[resource]);if(request!==undefined&&limit!==undefined&&request>limit)fail('requestExceedsLimit')}
 if(Object.keys(resources).length)object.resources=resources;else delete object.resources
 for(const key of ['startupProbe','readinessProbe','livenessProbe']){const probe=probeObject(form.probes[key]!,key);if(probe)object[key]=probe;else delete object[key]}
 object.volumeMounts=form.mounts.map(m=>{if(!m.name||!m.mountPath.startsWith('/'))fail('invalidMount');const value:KubeObject={...m.raw,name:m.name,mountPath:m.mountPath,readOnly:m.readOnly};if(m.subPath){value.subPath=m.subPath;delete value.subPathExpr;}else delete value.subPath;return value})
 if(!object.volumeMounts.length)delete object.volumeMounts
 if(init){if(form.sidecar)object.restartPolicy='Always';else{delete object.restartPolicy;for(const key of ['startupProbe','readinessProbe','livenessProbe'])delete object[key]}}
 return object
}
export type VolumeForm={name:string;kind:string;source:string;medium:string;sizeLimit:string;raw:KubeObject}
export function volumeForm(raw:KubeObject):VolumeForm {const kind=['emptyDir','configMap','secret','persistentVolumeClaim'].find(key=>raw[key])??'preserve';return {name:raw.name,kind,source:raw[kind]?.name??raw[kind]?.secretName??raw[kind]?.claimName??'',medium:raw.emptyDir?.medium??'',sizeLimit:raw.emptyDir?.sizeLimit??'',raw:copy(raw)}}
export function buildVolumes(rows:VolumeForm[],containers:KubeObject[]):KubeObject[]{
 const names=new Set<string>()
 const result=rows.map(row=>{if(!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(row.name)||names.has(row.name))fail('invalidVolume');names.add(row.name);if(row.kind==='preserve')return {...copy(row.raw),name:row.name}
  const source={...copy(row.raw[row.kind]??{})}
  if(row.kind==='emptyDir'){if(row.medium)source.medium=row.medium;else delete source.medium;if(row.sizeLimit){if(parseKubeQuantity(row.sizeLimit)===undefined)fail('invalidResources');source.sizeLimit=row.sizeLimit}else delete source.sizeLimit}
  else{if(!/^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/.test(row.source))fail('invalidVolume');source[row.kind==='secret'?'secretName':row.kind==='persistentVolumeClaim'?'claimName':'name']=row.source}
  return {name:row.name,[row.kind]:source}
 })
 for(const container of containers){const paths=new Set<string>();for(const mount of container.volumeMounts??[]){if(!names.has(mount.name)||paths.has(mount.mountPath))fail('invalidMount');paths.add(mount.mountPath)}}
 return result
}

export function workloadSettings(object:KubeObject,kind=object.kind??''):WorkloadSettingsForm {
 const spec=object.spec??{},job=kind==='CronJob'?spec.jobTemplate?.spec??{}:spec
 const pod=kind==='Pod'?spec:kind==='CronJob'?job.template?.spec??{}:spec.template?.spec??{}
 const rolling=spec.updateStrategy?.rollingUpdate??{}
 return {parallelism:job.parallelism??null,completions:job.completions??null,backoffLimit:job.backoffLimit??null,ttlSecondsAfterFinished:job.ttlSecondsAfterFinished??null,activeDeadlineSeconds:job.activeDeadlineSeconds??null,timeZone:spec.timeZone??'',concurrencyPolicy:spec.concurrencyPolicy??'',successfulJobsHistoryLimit:spec.successfulJobsHistoryLimit??null,failedJobsHistoryLimit:spec.failedJobsHistoryLimit??null,startingDeadlineSeconds:spec.startingDeadlineSeconds??null,updateStrategy:spec.updateStrategy?.type??'RollingUpdate',partition:rolling.partition??null,daemonMaxSurge:String(rolling.maxSurge??0),daemonMaxUnavailable:String(rolling.maxUnavailable??1),serviceAccountName:pod.serviceAccountName??'',nodeSelector:Object.entries(pod.nodeSelector??{}).map(([key,value])=>({key,value:String(value)}))}
}

type WorkloadNumberField='parallelism'|'completions'|'backoffLimit'|'ttlSecondsAfterFinished'|'activeDeadlineSeconds'|'successfulJobsHistoryLimit'|'failedJobsHistoryLimit'|'startingDeadlineSeconds'|'partition'
const dnsSubdomain=/^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/
const labelPart=/^[a-zA-Z0-9]([-a-zA-Z0-9_.]*[a-zA-Z0-9])?$/
function validSubdomain(value:string){return value.length<=253&&dnsSubdomain.test(value)}
function validLabelKey(value:string){const parts=value.split('/'),name=parts.at(-1)!;return parts.length<=2&&name.length<=63&&labelPart.test(name)&&(parts.length===1||validSubdomain(parts[0]!))}
function strategyAmount(value:string):string|number {
 if(typeof value!=='string'||!/^(\d+%|0|[1-9]\d*)$/.test(value))fail('invalidWorkloadStrategy')
 const amount=Number(value.replace('%',''))
 if(!Number.isSafeInteger(amount)||amount>(value.endsWith('%')?100:2147483647))fail('invalidWorkloadStrategy')
 return value.endsWith('%')?value:amount
}

export function buildWorkloadSettings(kind:string,settings:WorkloadSettingsForm,original:KubeObject={}):{spec:KubeObject;pod:KubeObject} {
 const initial=workloadSettings(original,kind),spec:KubeObject={},pod:KubeObject={}
 // 只提交改动，避免把服务端默认值或未曝光字段覆盖回去。
 const numberField=(target:KubeObject,field:WorkloadNumberField,error:string)=>{
  const value=settings[field]
  if(Object.is(value,initial[field]))return
  const maximum=field==='activeDeadlineSeconds'||field==='startingDeadlineSeconds'?Number.MAX_SAFE_INTEGER:2147483647
  if(value!==null&&(!Number.isSafeInteger(value)||value<0||value>maximum))fail(error)
  target[field]=value
 }
 if(kind==='Job'||kind==='CronJob'){
  const job:KubeObject={}
  for(const field of ['parallelism','completions','backoffLimit','ttlSecondsAfterFinished','activeDeadlineSeconds'] as const)numberField(job,field,'invalidJobSettings')
  if(kind==='Job')Object.assign(spec,job)
  else{
   if(Object.keys(job).length)spec.jobTemplate={spec:job}
   for(const field of ['successfulJobsHistoryLimit','failedJobsHistoryLimit','startingDeadlineSeconds'] as const)numberField(spec,field,'invalidCronJobSettings')
   if(settings.concurrencyPolicy!==initial.concurrencyPolicy){
    if(!['','Allow','Forbid','Replace'].includes(settings.concurrencyPolicy))fail('invalidCronJobSettings')
    spec.concurrencyPolicy=settings.concurrencyPolicy||null
   }
   if(settings.timeZone!==initial.timeZone){
    if(typeof settings.timeZone!=='string')fail('invalidCronJobSettings')
    const timeZone=settings.timeZone.trim()
    if(timeZone){
     if(timeZone!=='UTC'&&!timeZone.includes('/'))fail('invalidCronJobSettings')
     try{new Intl.DateTimeFormat('en-US',{timeZone})}catch{fail('invalidCronJobSettings')}
    }
    spec.timeZone=timeZone||null
   }
  }
 }
 if(kind==='StatefulSet'||kind==='DaemonSet'){
  const strategyChanged=settings.updateStrategy!==initial.updateStrategy
  const rollingChanged=kind==='StatefulSet'?settings.partition!==initial.partition:settings.daemonMaxSurge!==initial.daemonMaxSurge||settings.daemonMaxUnavailable!==initial.daemonMaxUnavailable
  if(strategyChanged||rollingChanged){
   if(!['RollingUpdate','OnDelete'].includes(settings.updateStrategy))fail('invalidWorkloadStrategy')
   const strategy:KubeObject={}
   if(strategyChanged)strategy.type=settings.updateStrategy
   if(settings.updateStrategy==='OnDelete'){
    if(strategyChanged)strategy.rollingUpdate=null
   }else if(kind==='StatefulSet'){
    const rolling:KubeObject={};numberField(rolling,'partition','invalidWorkloadStrategy')
    if(Object.keys(rolling).length)strategy.rollingUpdate=rolling
   }else{
    const rolling:KubeObject={}
    const surge=strategyAmount(settings.daemonMaxSurge),unavailable=strategyAmount(settings.daemonMaxUnavailable)
    for(const [field,key,value] of [['daemonMaxSurge','maxSurge',surge],['daemonMaxUnavailable','maxUnavailable',unavailable]] as const){if(settings[field]!==initial[field])rolling[key]=value}
    // DaemonSet 的两项预算必须恰好一项非零，与 Deployment 不同。
    if((Number(String(surge).replace('%',''))===0)===(Number(String(unavailable).replace('%',''))===0))fail('invalidWorkloadStrategy')
    if(Object.keys(rolling).length)strategy.rollingUpdate=rolling
   }
   if(Object.keys(strategy).length)spec.updateStrategy=strategy
  }
 }
 if(settings.serviceAccountName!==initial.serviceAccountName){
  if(typeof settings.serviceAccountName!=='string')fail('invalidServiceAccount')
  const name=settings.serviceAccountName.trim()
  if(name&&!validSubdomain(name))fail('invalidServiceAccount')
  pod.serviceAccountName=name||null
 }
 if(JSON.stringify(settings.nodeSelector)!==JSON.stringify(initial.nodeSelector)){
  if(!Array.isArray(settings.nodeSelector))fail('invalidNodeSelector')
  const keys=new Set<string>(),rows:Array<[string,string|null]>=[]
  for(const row of settings.nodeSelector){
   if(typeof row?.key!=='string'||typeof row.value!=='string'||!validLabelKey(row.key)||keys.has(row.key)||row.value.length>63||row.value!==''&&!labelPart.test(row.value))fail('invalidNodeSelector')
   keys.add(row.key);rows.push([row.key,row.value])
  }
  for(const {key} of initial.nodeSelector)if(!keys.has(key))rows.push([key,null])
  pod.nodeSelector=Object.fromEntries(rows)
 }
 return {spec,pod}
}
