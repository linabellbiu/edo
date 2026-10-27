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
export function podMetricsFresh(pod:KubeObject,data:KubeObject|null,now=Date.now()){
 const at=Date.parse(data?.metrics_at??''),created=Date.parse(pod.metadata?.creationTimestamp??'1970-01-01')
 return Boolean(pod.metadata?.uid&&data?.uid===pod.metadata.uid&&data?.state==='ok'&&Number.isFinite(at)&&now-at<=120000&&at<=now+60000&&at>=created)
}
export function podMetricRows(pod:KubeObject,data:KubeObject|null,now=Date.now()){
 const at=Date.parse(data?.metrics_at??''),fresh=podMetricsFresh(pod,data,now)
 return podRuntimeContainers(pod).map(c=>{
  const started=Date.parse(c.runtime?.state?.running?.startedAt??''),currentInstance=!c.runtime?.state?.terminated&&!c.runtime?.state?.waiting&&(!Number.isFinite(started)||at>=started)
  const metric=fresh&&currentInstance?data?.containers?.find((m:KubeObject)=>m.name===c.name):undefined
  const valid=(v:unknown)=>typeof v==='number'&&Number.isFinite(v)&&v>=0?v:null
  return {name:c.name,cpu:{used:valid(metric?.cpu),requests:parseKubeQuantity(c.resources?.requests?.cpu)??null,limits:parseKubeQuantity(c.resources?.limits?.cpu)??null},memory:{used:valid(metric?.memory),requests:parseKubeQuantity(c.resources?.requests?.memory)??null,limits:parseKubeQuantity(c.resources?.limits?.memory)??null}}
 })
}
export interface PodMetricSample {at:number;containers:Array<{name:string;cpu:number|null;memory:number|null}>;breakBefore?:boolean}
export interface PodMetricHistory {uid:string;points:PodMetricSample[];interrupted?:boolean}
// 断档只有一个待恢复标记，不制造失败时间戳或用量；恢复标记随真实采样一起受 60 点上限约束。
export function interruptPodMetricHistory(history:PodMetricHistory|null,pod:KubeObject):PodMetricHistory|null {
 const uid=pod.metadata?.uid
 if(typeof uid!=='string'||!uid)return null
 const points=history?.uid===uid?history.points:[]
 return {uid,points,...(points.length?{interrupted:true}:{})}
}
// 只保存当前 Pod 已收到的真实采样；不插值、不补零，也不把旧实例用量当成当前实例。
export function appendPodMetricSample(history:PodMetricHistory|null,pod:KubeObject,data:KubeObject|null,now=Date.now(),limit=60):PodMetricHistory|null {
 const uid=pod.metadata?.uid
 if(typeof uid!=='string'||!uid)return null
 const points=history?.uid===uid?history.points:[]
 if(!podMetricsFresh(pod,data,now))return interruptPodMetricHistory(history,pod)
 const at=Date.parse(data!.metrics_at),containers=podMetricRows(pod,data,now).map(row=>({name:row.name,cpu:row.cpu.used,memory:row.memory.used}))
 if(!containers.some(row=>row.cpu!==null||row.memory!==null))return interruptPodMetricHistory(history,pod)
 const interrupted=history?.uid===uid&&history.interrupted
 if(points.some(point=>point.at===at))return {uid,points,...(interrupted?{interrupted:true}:{})}
 const breakBefore=Boolean(interrupted&&at>(points.at(-1)?.at??-Infinity))
 const count=Number.isFinite(limit)?Math.max(1,Math.min(60,Math.floor(limit))):60
 return {uid,points:[...points,{at,containers,...(breakBefore?{breakBefore:true}:{})}].sort((a,b)=>a.at-b.at).slice(-count),...(interrupted&&!breakBefore?{interrupted:true}:{})}
}
export function podMetricTrendPoints(history:PodMetricHistory|null,container:string,kind:'cpu'|'memory'):Array<[number,number|null]> {
 return (history?.points??[]).map(point=>[point.at,point.containers.find(row=>row.name===container)?.[kind]??null])
}
export function podMetricTrendSegments(history:PodMetricHistory|null,container:string,kind:'cpu'|'memory'):Array<Array<[number,number|null]>> {
 const segments:Array<Array<[number,number|null]>>=[[]]
 for(const sample of history?.points??[]){
  const last=segments.at(-1)!.at(-1)
  if(last&&(sample.breakBefore||sample.at-last[0]>120000))segments.push([])
  segments.at(-1)!.push([sample.at,sample.containers.find(row=>row.name===container)?.[kind]??null])
 }
 return segments.filter(points=>points.some(point=>point[1]!==null))
}
// 只扩展时间轴视野，不增加采样点；单点避免被时间轴默认扩成全天。
export function podMetricTrendRange(history:PodMetricHistory|null,kind:'cpu'|'memory'):{min:number;max:number}|undefined {
 const times=(history?.points??[]).filter(point=>Number.isFinite(point.at)&&point.containers.some(container=>typeof container[kind]==='number'&&Number.isFinite(container[kind])&&container[kind]!>=0)).map(point=>point.at)
 if(!times.length)return undefined
 const first=Math.min(...times),last=Math.max(...times),padding=last===first?30000:Math.max(1000,Math.min(30000,(last-first)*.05))
 return {min:first-padding,max:last+padding}
}
