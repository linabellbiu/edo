import type {KubeObject} from '@/api/kubernetes'
export interface KubeListState {query:string;label:string;status:string;sort:string;order:'asc'|'desc';page:number;limit:number}
export const kubeListQueryKeys=['q','label','status','sort','order','page','limit'] as const
const text=(value:unknown)=>typeof value==='string'?value:''
export function readKubeListState(query:Record<string,unknown>):KubeListState {
 const sort=['name','namespace','created','status'].includes(text(query.sort))?String(query.sort):'name'
 return {query:text(query.q),label:text(query.label),status:text(query.status),sort,order:query.order==='desc'?'desc':'asc',page:/^[1-9]\d{0,4}$/.test(text(query.page))?Number(query.page):1,limit:[10,20,50,100].includes(Number(text(query.limit)))?Number(text(query.limit)):10}
}
export function kubeListQuery(state:KubeListState){return {q:state.query||undefined,label:state.label||undefined,status:state.status||undefined,sort:state.sort==='name'?undefined:state.sort,order:state.order==='asc'?undefined:state.order,page:state.page===1?undefined:String(state.page),limit:state.limit===10?undefined:String(state.limit)}}
export function withoutKubeListQuery(query:Record<string,any>){const result={...query};for(const key of kubeListQueryKeys)delete result[key];return result}
export function sortKubeRows<T extends KubeObject>(rows:T[],key:string,order:'asc'|'desc'):T[]{
 const value=(row:T)=>key==='created'?Date.parse(row.metadata?.creationTimestamp??''):key==='namespace'?row.metadata?.namespace:key==='status'?row.state?.text:row.metadata?.name
 return [...rows].sort((a,b)=>{const x=value(a),y=value(b),empty=(v:unknown)=>v===undefined||v===''||typeof v==='number'&&!Number.isFinite(v);if(empty(x)!==empty(y))return empty(x)?1:-1;const compared=empty(x)?0:typeof x==='number'&&typeof y==='number'?x-y:String(x).localeCompare(String(y),'zh-CN',{numeric:true});return compared*(order==='desc'?-1:1)||String(a.metadata?.name).localeCompare(String(b.metadata?.name),'zh-CN',{numeric:true})||String(a.metadata?.uid).localeCompare(String(b.metadata?.uid))})
}
