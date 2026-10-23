import {parseKubeQuantity,type KubeObject} from '@/api/kubernetes'
export function podRuntimeContainers(pod:KubeObject){
 return [['containers','containerStatuses','app'],['initContainers','initContainerStatuses','init'],['ephemeralContainers','ephemeralContainerStatuses','ephemeral']].flatMap(([spec,status,kind])=>(pod.spec?.[spec!]??[]).map((c:KubeObject)=>({...c,containerKind:kind,runtime:(pod.status?.[status!]??[]).find((s:KubeObject)=>s.name===c.name)})))
}
export function podLifecycle(pod:KubeObject){
 const points:Array<{key:string;at:string;reason:string;status?:string}>=[]
 if(pod.metadata?.creationTimestamp)points.push({key:'created',at:pod.metadata.creationTimestamp,reason:'',status:'True'})
 for(const c of pod.status?.conditions??[])if(c.lastTransitionTime)points.push({key:c.type,at:c.lastTransitionTime,reason:c.reason??'',status:c.status})
 for(const c of podRuntimeContainers(pod))for(const [key,state] of [['current',c.runtime?.state],['previous',c.runtime?.lastState]] as const){
  if(state?.running?.startedAt)points.push({key:`${c.name} / ${key} / started`,at:state.running.startedAt,reason:'',status:'True'})
  if(state?.terminated){const exit=state.terminated;for(const field of ['startedAt','finishedAt'])if(exit[field])points.push({key:`${c.name} / ${key} / ${field}`,at:exit[field],reason:`${exit.reason??''} · exit ${exit.exitCode??'—'}`,status:exit.exitCode===0?'True':'False'})}
 }
 if(pod.metadata?.deletionTimestamp)points.push({key:'deleting',at:pod.metadata.deletionTimestamp,reason:'',status:'False'})
 return points.filter(p=>Number.isFinite(Date.parse(p.at))).sort((a,b)=>Date.parse(a.at)-Date.parse(b.at))
}
export function podMetricRows(pod:KubeObject,data:KubeObject|null,now=Date.now()){
 const at=Date.parse(data?.metrics_at??''),created=Date.parse(pod.metadata?.creationTimestamp??'1970-01-01')
 const fresh=data?.uid===pod.metadata?.uid&&data?.state==='ok'&&Number.isFinite(at)&&now-at<=120000&&at<=now+60000&&at>=created
 return podRuntimeContainers(pod).map(c=>{
  const started=Date.parse(c.runtime?.state?.running?.startedAt??''),currentInstance=!c.runtime?.state?.terminated&&(!Number.isFinite(started)||at>=started)
  const metric=fresh&&currentInstance?data?.containers?.find((m:KubeObject)=>m.name===c.name):undefined
  const valid=(v:unknown)=>typeof v==='number'&&Number.isFinite(v)&&v>=0?v:null
  return {name:c.name,cpu:{used:valid(metric?.cpu),requests:parseKubeQuantity(c.resources?.requests?.cpu)??null,limits:parseKubeQuantity(c.resources?.limits?.cpu)??null},memory:{used:valid(metric?.memory),requests:parseKubeQuantity(c.resources?.requests?.memory)??null,limits:parseKubeQuantity(c.resources?.limits?.memory)??null}}
 })
}
