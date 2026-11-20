<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { composeRuntimeStatus } from '@/utils/composeRuntime'
import { formatDateTime } from '@/utils/time'

interface HistoryRow { id:string; location:string; source:string; deployedAt:string; removedAt?:string }
interface Observation { collected_at:string|null; state?:string; running:boolean }
const props=defineProps<{rows:HistoryRow[];scopeKey:string}>()
const open=ref(false), revision=ref(0)
const observations=ref<Record<string,Observation>>({}), errors=ref<Record<string,string>>({})
const ids=computed(()=>props.rows.map(row=>row.id).join(','))
watch([open,ids,()=>props.scopeKey,revision],async ([visible],_,onCleanup)=>{
 observations.value={};errors.value={}
 if(!visible)return
 const controller=new AbortController()
 onCleanup(()=>controller.abort())
 const queue=props.rows.values()
 async function read(){
  for(const row of queue){
   if(controller.signal.aborted)return
   try{
    const {data}=await client.get<Observation>(`/deployments/${encodeURIComponent(row.id)}/observation`,{signal:controller.signal})
    if(!controller.signal.aborted)observations.value[row.id]=data
   }catch(error){if(!controller.signal.aborted)errors.value[row.id]=apiErrorMessage(error)}
  }
 }
 await Promise.all(Array.from({length:Math.min(4,props.rows.length)},read))
})
function observedLabel(value:Observation){
 if(value.running)return '运行中'
 if(value.state==='stopped')return '已停止'
 if(value.state==='progressing')return '调整中'
 if(value.state==='unverified')return '运行状态未确认'
 return composeRuntimeStatus(value.state||'').label
}
</script>

<template>
 <details class="location-history" @toggle="open=($event.currentTarget as HTMLDetailsElement).open">
  <summary>历史部署位置 <span>{{ rows.length }}</span></summary>
  <ul v-if="open">
   <li v-for="row in rows" :key="row.id">
    <div><strong>{{ row.location }}</strong><span v-if="row.source">{{ row.source }}</span></div>
    <div><span>最后部署 <time :datetime="row.deployedAt">{{ formatDateTime(row.deployedAt) }}</time></span>
     <span v-if="row.removedAt">实例已移除</span>
     <span v-if="errors[row.id]">{{ errors[row.id] }} <button type="button" @click="revision++">重试</button></span>
     <span v-else-if="observations[row.id]?.collected_at">上次同步为{{ observedLabel(observations[row.id]) }} · <time :datetime="observations[row.id].collected_at!">{{ formatDateTime(observations[row.id].collected_at!) }}</time></span>
     <span v-else>{{ observations[row.id]?'暂无同步记录':'正在读取同步记录…' }}</span>
    </div>
   </li>
  </ul>
 </details>
</template>

<style scoped>
.location-history { border-top:1px solid var(--edo-border); color:var(--edo-text); font-size:13px; }
summary { padding:10px 14px; cursor:pointer; font-weight:600; }
summary span { color:var(--edo-muted); font-weight:400; }
ul { padding:0; margin:0; list-style:none; }
li { display:flex; flex-wrap:wrap; justify-content:space-between; gap:4px 16px; padding:8px 14px; border-top:1px solid var(--edo-border); }
li div { display:grid; gap:3px; min-width:0; overflow-wrap:anywhere; }
li span { color:var(--edo-muted); font-size:12px; }
button { border:0; background:none; color:var(--edo-primary); cursor:pointer; }
summary:focus-visible,button:focus-visible { outline:2px solid var(--edo-primary); outline-offset:-2px; }
</style>
