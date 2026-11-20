import type {KubeObject,KubeOverview} from '@/api/kubernetes'
export interface KubeListState {query:string;label:string;status:string;sort:string;order:'asc'|'desc';page:number;limit:number}
export const kubeSortableColumns=['name','namespace','created','status','replicas','currentReplicas','restarts','completions','activeJobs','lastSchedule']
export const kubeListQueryKeys=['q','label','status','sort','order','page','limit'] as const
const text=(value:unknown)=>typeof value==='string'?value:''
export function readKubeListState(query:Record<string,unknown>):KubeListState {
 const sort=kubeSortableColumns.includes(text(query.sort))?String(query.sort):'name'
 return {query:text(query.q),label:text(query.label),status:text(query.status),sort,order:query.order==='desc'?'desc':'asc',page:/^[1-9]\d{0,4}$/.test(text(query.page))?Number(query.page):1,limit:[10,20,50,100].includes(Number(text(query.limit)))?Number(text(query.limit)):10}
}
export function kubeListQuery(state:KubeListState){return {q:state.query||undefined,label:state.label||undefined,status:state.status||undefined,sort:state.sort==='name'?undefined:state.sort,order:state.order==='asc'?undefined:state.order,page:state.page===1?undefined:String(state.page),limit:state.limit===10?undefined:String(state.limit)}}
export function withoutKubeListQuery(query:Record<string,any>){const result={...query};for(const key of kubeListQueryKeys)delete result[key];return result}

export interface KubeWorkspacePreferences {
 cluster?:string
 clusters?:string[]
 scopes?:Record<string,string[]>
 autoRefresh?:boolean
 pageSize?:number
}
// 复用项目现有的浏览器 Storage API；不把浏览偏好作为鉴权依据或发送到服务端。
// 全选、部分选择和空选择分别保存，不能用空值回退悄悄扩大查询范围。
export function kubeWorkspacePreferenceKey(userID?:string,projectID?:string):string {
 return userID&&projectID?`edo.kubernetes.preferences.v1:${encodeURIComponent(userID)}:${encodeURIComponent(projectID)}`:''
}
function sanitizeKubeWorkspacePreferences(value:unknown):KubeWorkspacePreferences {
 if(!value||typeof value!=='object'||Array.isArray(value))return {}
 const input=value as Record<string,unknown>,result:KubeWorkspacePreferences={}
 if(typeof input.cluster==='string'&&input.cluster.length<=128)result.cluster=input.cluster
 if(Array.isArray(input.clusters)&&input.clusters.length<=100)result.clusters=[...new Set(input.clusters.filter((id):id is string=>typeof id==='string'&&Boolean(id)&&id.length<=128&&!['__proto__','constructor','prototype'].includes(id)))]
 if(typeof input.autoRefresh==='boolean')result.autoRefresh=input.autoRefresh
 if(typeof input.pageSize==='number'&&[10,20,50,100].includes(input.pageSize))result.pageSize=input.pageSize
 if(input.scopes&&typeof input.scopes==='object'&&!Array.isArray(input.scopes)) {
  result.scopes={}
  for(const [id,names] of Object.entries(input.scopes).slice(-100)) {
   if(!id||id.length>128||['__proto__','constructor','prototype'].includes(id)||!Array.isArray(names)||names.length>1000)continue
   if(names.some(name=>typeof name!=='string'||name!=='*'&&(name.length>63||!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name))))continue
   result.scopes[id]=names.includes('*')?['*']:[...new Set(names)].sort()
  }
 }
 return result
}
export interface KubeWorkspaceScope {clusters:string[];scopes:Record<string,string[]>}
const safeCluster=(id:unknown):id is string=>typeof id==='string'&&Boolean(id)&&id.length<=128&&!['__proto__','constructor','prototype'].includes(id)
const scopeNames=(value:unknown)=>{
 const names=Array.isArray(value)?value:[value]
 return names.includes('*')?['*']:[...new Set(names.filter((name):name is string=>typeof name==='string'&&name.length<=63&&/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name)))].sort()
}
// 单集群链接沿用 cluster/namespace，多集群逐项编码范围，空选择也有明确表示。
export function readKubeScopeQuery(query:Record<string,unknown>):KubeWorkspaceScope|undefined {
 if(query.cluster===undefined)return
 const clusters=[...new Set((Array.isArray(query.cluster)?query.cluster:[query.cluster]).filter(safeCluster))].slice(0,100)
 const scopes:Record<string,string[]>={}
 for(const raw of (Array.isArray(query.scope)?query.scope:[query.scope]).slice(0,100)){
  if(typeof raw!=='string'||raw.length>65536)continue
  try{const entry=JSON.parse(raw);if(Array.isArray(entry)&&safeCluster(entry[0])&&clusters.includes(entry[0])&&Array.isArray(entry[1])&&entry[1].length<=1000)scopes[entry[0]]=scopeNames(entry[1])}catch{/* 损坏的浏览范围不扩大查询。 */}
 }
 if(clusters.length===1&&query.namespace!==undefined)scopes[clusters[0]!]=scopeNames(query.namespace)
 return {clusters,scopes}
}
export function kubeScopeQuery(scope:KubeWorkspaceScope){
 if(scope.clusters.length===1){const id=scope.clusters[0]!,names=scope.scopes[id]??[];return {cluster:id,namespace:names.length?names:[''],scope:undefined}}
 return {cluster:scope.clusters.length?scope.clusters:[''],namespace:undefined,scope:scope.clusters.map(id=>JSON.stringify([id,scope.scopes[id]??[]]))}
}
// 来源只存在于界面对象的 Symbol 属性中，不进入 YAML、JSON 或写请求正文。
const sourceCluster=Symbol('edo.kubernetes.cluster')
export function withKubeCluster(object:KubeObject,cluster:string):KubeObject {return {...object,[sourceCluster]:cluster}}
export function kubeObjectCluster(object:KubeObject|undefined):string {return object?(object as Record<symbol,string>)[sourceCluster]??'':''}
export function mergeClusterOverviews(entries:Array<{cluster:string;data:KubeOverview}>,expected:number):KubeOverview|null {
 if(!entries.length)return null
 if(entries.length===1&&expected===1)return entries[0]!.data
 const results=entries.map(entry=>entry.data),complete=entries.length===expected
 const counts:KubeOverview['counts']={}
 for(const key of new Set(results.flatMap(result=>Object.keys(result.counts)))){
  const values=results.map(result=>result.counts[key])
  const available=values.filter(value=>value&&value.state!=='unavailable')
  counts[key]={count:available.reduce((sum,value)=>sum+value!.count,0),state:!available.length?'unavailable':complete&&values.every(value=>value?.state==='ok')?'ok':'partial'}
 }
 const nodes=complete&&results.every(result=>Array.isArray(result.nodes))?entries.flatMap(({cluster,data})=>data.nodes!.map(node=>({...node,name:JSON.stringify([cluster,node.name]),uid:JSON.stringify([cluster,node.uid])}))):null
 const phases=complete&&results.every(result=>result.phases!=null)?results.reduce<Record<string,number>>((sum,result)=>{for(const [key,count] of Object.entries(result.phases!))sum[key]=(sum[key]??0)+count;return sum},{}):null
 const usage:KubeOverview['usage']={nodes:{},pods:{}}
 for(const {cluster,data} of entries)for(const [kind,values] of Object.entries(data.usage??{})){
  usage[kind]??={};for(const [name,value] of Object.entries(values))usage[kind]![JSON.stringify([cluster,name])]=value
 }
 const metrics_state=Object.fromEntries(['nodes','pods'].map(kind=>[kind,complete&&results.every(result=>result.metrics_state?.[kind]==='ok')?'ok':'unavailable']))
 const monitoring=results[0]!.monitoring_state
 return {counts,nodes,phases,usage,metrics_state,monitoring_state:complete&&results.every(result=>result.monitoring_state===monitoring)?monitoring:'unavailable',collected_at:results.map(result=>result.collected_at).filter(Boolean).sort()[0]??''}
}
export function readKubeWorkspacePreferences(userID?:string,projectID?:string):KubeWorkspacePreferences {
 const key=kubeWorkspacePreferenceKey(userID,projectID)
 if(!key)return {}
 try {
  const raw=localStorage.getItem(key)
  return raw&&raw.length<=256*1024?sanitizeKubeWorkspacePreferences(JSON.parse(raw)):{}
 }catch{return {}}
}
export function writeKubeWorkspacePreferences(userID:string|undefined,projectID:string|undefined,value:KubeWorkspacePreferences):void {
 const key=kubeWorkspacePreferenceKey(userID,projectID)
 if(!key)return
 try{localStorage.setItem(key,JSON.stringify(sanitizeKubeWorkspacePreferences(value)))}catch{/* 禁用存储或空间不足时保留本次页面选择。 */}
}
export function sortKubeRows<T extends KubeObject>(rows:T[],key:string,order:'asc'|'desc'):T[]{
 const value=(row:T)=>key==='created'?Date.parse(row.metadata?.creationTimestamp??''):key==='namespace'?row.metadata?.namespace:key==='status'?row.state?.text:key==='lastSchedule'?Date.parse(row.status?.lastScheduleTime??''):key!=='name'?row.cells?.[key]?.sortValue:row.metadata?.name
 return [...rows].sort((a,b)=>{const x=value(a),y=value(b),empty=(v:unknown)=>v===undefined||v===''||typeof v==='number'&&!Number.isFinite(v);if(empty(x)!==empty(y))return empty(x)?1:-1;const compared=empty(x)?0:typeof x==='number'&&typeof y==='number'?x-y:String(x).localeCompare(String(y),'zh-CN',{numeric:true});return compared*(order==='desc'?-1:1)||String(a.metadata?.name).localeCompare(String(b.metadata?.name),'zh-CN',{numeric:true})||String(a.metadata?.uid).localeCompare(String(b.metadata?.uid))})
}

export interface KubeListPage {items:KubeObject[];cursor:Record<string,string>;collectedAt?:string;stale?:boolean}
export function podContainerChoices(pod:KubeObject):Array<{name:string;kind:'app'|'init'|'ephemeral';running:boolean}> {
 const spec=pod.spec??{},status=pod.status??{}
 return (['app','init','ephemeral'] as const).flatMap(kind=>{
  const [definitions,states]=kind==='app'?[spec.containers,status.containerStatuses]:kind==='init'?[spec.initContainers,status.initContainerStatuses]:[spec.ephemeralContainers,status.ephemeralContainerStatuses]
  return (definitions??[]).filter((item:KubeObject)=>typeof item.name==='string'&&item.name).map((item:KubeObject)=>({name:item.name,kind,running:pod.status?.phase==='Running'&&kind!=='init'&&Boolean(states?.find((state:KubeObject)=>state.name===item.name)?.state?.running)}))
 })
}
// 从首屏重新读取已加载的窗口，避免刷新后续页时丢掉列表上下文或沿用过期游标。
export async function reloadKubeWindow(read:(cursor?:Record<string,string>)=>Promise<KubeListPage>,pages:number,signal?:AbortSignal,minimumItems=0):Promise<KubeListPage&{pages:number}> {
 const items:KubeObject[]=[]
 // 深链接仅有界补齐当前页；更深的窗口必须来自用户此前明确加载，不能由 URL 无限展开。
 const retainedPages=Number.isSafeInteger(pages)&&pages>0?pages:1,maximumPages=Math.max(20,retainedPages)
 const minimum=Number.isSafeInteger(minimumItems)&&minimumItems>0?minimumItems:0
 let cursor:Record<string,string>|undefined,loaded=0
 let collectedAt: string|undefined, stale=false
 for(;loaded<maximumPages;){
  signal?.throwIfAborted()
  const result=await read(cursor)
  signal?.throwIfAborted()
  items.push(...result.items);cursor=result.cursor;loaded++
  if(result.collectedAt&&(!collectedAt||result.collectedAt<collectedAt))collectedAt=result.collectedAt
  stale ||= Boolean(result.stale)
  if(!Object.keys(cursor).length||loaded>=retainedPages&&items.length>=minimum)break
 }
 return {items,cursor:cursor??{},pages:loaded,...(collectedAt?{collectedAt}:{}),...(stale?{stale:true}:{})}
}
