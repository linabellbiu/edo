import type { KubeObject } from '@/api/kubernetes'
export interface NodeAllocation {cpu_requests:number;cpu_limits:number;memory_requests:number;memory_limits:number}
export interface NodeSample {
 collected_at:string;metrics_at:string;cpu:number|null;memory:number|null;cpu_allocatable:number|null;memory_allocatable:number|null;pod_capacity:number|null;pods:number|null;allocation:NodeAllocation|null;metrics_state:string;pods_state:string
}
export interface NodeHistory {uid:string;from:string;to:string;interval_seconds:number;points:NodeSample[]}
export const nodeSampleFresh=(sample:NodeSample|undefined,now=Date.now())=>Boolean(sample&&now-Date.parse(sample.collected_at)<=120000&&Date.parse(sample.collected_at)<=now+60000)
export function nodeMetricValue(sample:NodeSample,key:'cpu'|'memory'|'pods'):number|null {
 const value=sample[key],capacity=sample[key==='cpu'?'cpu_allocatable':key==='memory'?'memory_allocatable':'pod_capacity']
 const metricsAt=Date.parse(sample.metrics_at)
 if(key!=='pods'&&(sample.metrics_state!=='ok'||!Number.isFinite(metricsAt)||Math.abs(Date.parse(sample.collected_at)-metricsAt)>120000))return null
 if(key==='pods'&&sample.pods_state!=='ok')return null
 return value!==null&&Number.isFinite(value)&&value>=0&&capacity!==null&&Number.isFinite(capacity)&&capacity>0?value/capacity*100:null
}
// 断档插入空点，不插值、不沿用旧样本，也不把失败当成零。
export function nodeChartPoints(history:NodeHistory,key:'cpu'|'memory'|'pods'):Array<[number,number|null]> {
 const values:Array<[number,number|null]>=[]
 for(const sample of history.points){
  const at=Date.parse(sample.collected_at);if(!Number.isFinite(at)||at<Date.parse(history.from)||at>Date.parse(history.to))continue
  const last=values.at(-1)?.[0]
  if(last!==undefined&&at-last>history.interval_seconds*2500)values.push([(at+last)/2,null])
  values.push([at,nodeMetricValue(sample,key)])
 }
 return values
}
export function nodeConditionState(condition:KubeObject):'success'|'error'|'default' {
 if(!['True','False'].includes(condition.status))return 'default'
 const healthy=condition.type==='Ready'?condition.status==='True':['MemoryPressure','DiskPressure','PIDPressure','NetworkUnavailable'].includes(condition.type)?condition.status==='False':undefined
 return healthy===undefined?'default':healthy?'success':'error'
}
