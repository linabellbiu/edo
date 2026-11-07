<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import client from '@/api/client'
import { apiErrorMessage } from '@/api/resources'
import { useAuthStore } from '@/stores/auth'
import { configurationFormat } from '@/utils/configText'
import ConfigCodeEditor from '@/components/ConfigCodeEditor.vue'

interface Target { id:string; project_id:string; name:string; kind:string; is_secret:boolean; version:number }
interface Revision { version:number; is_secret:boolean; created_by:string; created_by_name?:string; created_at:string; value?:string; hidden:boolean }
const props=defineProps<{open:boolean;target?:Target}>()
const emit=defineEmits<{'update:open':[value:boolean]}>()
const auth=useAuthStore(),revisions=ref<Revision[]>([]),total=ref(0),page=ref(0),currentVersion=ref(0)
const loading=ref(false),reading=ref(false),error=ref(''),beforeVersion=ref<number>(),afterVersion=ref<number>()
const beforeContent=ref<string>(),afterContent=ref<string>()
let generation=0,readGeneration=0,disposed=false,listController:AbortController|undefined,readController:AbortController|undefined
function project(){const id=localStorage.getItem('edo.project')||'';return id===auth.user?.project_id?id:''}
function canRead(){return !!props.open&&!!props.target&&props.target.project_id===project()&&!!configurationFormat(props.target.kind)&&auth.canAny(['delivery_variable.read'])}
const canReveal=computed(()=>canRead()&&auth.canAny(['delivery_variable.reveal']))
function identity(){return JSON.stringify([props.open,props.target?.id,props.target?.project_id,props.target?.version,auth.user?.id,auth.user?.project_id,auth.user?.department_id,auth.user?.is_superuser,[...(auth.user?.permissions||[])].sort(),project()])}
function current(epoch:number,context:string){return !disposed&&epoch===generation&&context===identity()&&canRead()}
function clearComparison(){readGeneration++;readController?.abort();reading.value=false;beforeContent.value=undefined;afterContent.value=undefined}
function clear(){listController?.abort();clearComparison();revisions.value=[];total.value=0;page.value=0;currentVersion.value=0;beforeVersion.value=undefined;afterVersion.value=undefined;loading.value=false;error.value=''}
const selected=computed(()=>[beforeVersion.value,afterVersion.value].map(version=>revisions.value.find(row=>row.version===version)))
const protectedContent=computed(()=>!!props.target?.is_secret||selected.value.some(row=>row?.is_secret))
const canCompare=computed(()=>canRead()&&!loading.value&&!reading.value&&selected.value.every(Boolean)&&(!protectedContent.value||canReveal.value))
const options=computed(()=>revisions.value.map(row=>({value:row.version,label:`v${row.version}${row.version===currentVersion.value?'（当前）':''}`})))
function formatDate(value:string){const date=new Date(value);return Number.isNaN(date.getTime())?'—':date.toLocaleString('zh-CN',{hour12:false})}
async function loadMore(){
  if(!canRead()||loading.value||page.value>0&&revisions.value.length>=total.value)return
  const epoch=generation,context=identity(),target=props.target!,next=page.value+1,controller=new AbortController()
  listController=controller;loading.value=true;error.value=''
  try{
    const {data}=await client.get<{revisions:Revision[];total:number;current_version:number}>(`/delivery-variables/${encodeURIComponent(target.id)}/revisions`,{headers:{'X-EDO-Project-ID':project()},signal:controller.signal,params:{page:next,page_size:20}})
    if(!current(epoch,context)||controller.signal.aborted)return
    revisions.value.push(...data.revisions);total.value=data.total;currentVersion.value=data.current_version;page.value=next
    if(next===1){afterVersion.value=revisions.value[0]?.version;beforeVersion.value=revisions.value[1]?.version??afterVersion.value}
  }catch(cause){if(current(epoch,context)&&!controller.signal.aborted){if((cause as {response?:{status?:number}})?.response?.status===403)clear();error.value=apiErrorMessage(cause)}}
  finally{if(current(epoch,context))loading.value=false}
}
async function compare(){
  if(!canCompare.value)return
  clearComparison()
  const epoch=generation,request=readGeneration,context=identity(),target=props.target!,controller=new AbortController(),versions=[beforeVersion.value!,afterVersion.value!]
  readController=controller;reading.value=true;error.value=''
  const options={headers:{'X-EDO-Project-ID':project()},signal:controller.signal}
  try{
    const result=await Promise.all([...new Set(versions)].map(async version=>{
      const path=`/delivery-variables/${encodeURIComponent(target.id)}/revisions/${version}`
      const response=protectedContent.value?await client.post<{revision:Revision}>(`${path}/reveal`,undefined,options):await client.get<{revision:Revision}>(path,options)
      if(response.data.revision.version!==version||response.data.revision.hidden||typeof response.data.revision.value!=='string')throw new Error('无法读取所选配置版本，请刷新后重试')
      return [version,response.data.revision.value] as const
    }))
    if(!current(epoch,context)||controller.signal.aborted||request!==readGeneration)return
    const values=new Map(result);beforeContent.value=values.get(versions[0]);afterContent.value=values.get(versions[1])
  }catch(cause){if(current(epoch,context)&&!controller.signal.aborted&&request===readGeneration){clearComparison();error.value=apiErrorMessage(cause)}}
  finally{if(current(epoch,context)&&request===readGeneration)reading.value=false}
}
function close(){generation++;clear();emit('update:open',false)}
watch([beforeVersion,afterVersion],clearComparison,{flush:'sync'})
watch(identity,()=>{generation++;clear();if(canRead())void loadMore()},{immediate:true,flush:'sync'})
onBeforeUnmount(()=>{disposed=true;generation++;clear()})
</script>

<template>
  <a-drawer :open="open" :title="`${target?.name||'配置'} · 历史版本`" width="min(1080px, 96vw)" destroy-on-close @close="close">
    <section class="configuration-history" aria-label="配置历史版本">
      <a-alert v-if="!canRead()" type="warning" show-icon message="没有查看当前项目配置的权限"/>
      <template v-else>
        <a-alert v-if="error" type="error" show-icon :message="error"/>
        <a-spin :spinning="loading">
          <div class="revision-list" tabindex="0" aria-label="版本记录">
            <table><thead><tr><th>版本</th><th>修改时间</th><th>修改人</th><th>内容</th></tr></thead><tbody><tr v-for="row in revisions" :key="row.version"><td>v{{row.version}}<a-tag v-if="row.version===currentVersion" color="blue">当前</a-tag></td><td>{{formatDate(row.created_at)}}</td><td>{{row.created_by_name||'未知用户'}}</td><td>{{row.is_secret?'密钥 · 已隐藏':'普通配置'}}</td></tr></tbody></table>
          </div>
          <a-empty v-if="!loading&&!revisions.length" description="暂无历史版本"/>
        </a-spin>
        <a-button v-if="revisions.length<total" :loading="loading" @click="loadMore">加载更早版本（{{revisions.length}} / {{total}}）</a-button>
        <div v-if="revisions.length" class="revision-comparison">
          <label>修改前<a-select v-model:value="beforeVersion" :options="options" aria-label="修改前版本" :disabled="reading"/></label>
          <label>修改后<a-select v-model:value="afterVersion" :options="options" aria-label="修改后版本" :disabled="reading"/></label>
          <a-button :loading="reading" :disabled="!canCompare" @click="compare">{{protectedContent?'读取正文并': ''}}{{beforeVersion===afterVersion?'查看此版本':'对比版本'}}</a-button>
        </div>
        <p v-if="protectedContent" class="revision-note">{{canReveal?'读取密钥正文会记录操作审计，关闭后清除本次读取内容。':'读取密钥正文需要独立的“读取正文”权限。'}}</p>
        <ConfigCodeEditor v-if="afterContent!==undefined&&target" :model-value="afterContent" :original="beforeVersion===afterVersion?undefined:beforeContent" :original-label="`v${beforeVersion}`" :modified-label="`v${afterVersion}`" :format="configurationFormat(target.kind)!" readonly label="历史版本内容对比"/>
      </template>
    </section>
  </a-drawer>
</template>

<style scoped>
.configuration-history{display:grid;grid-template-columns:minmax(0,1fr);gap:16px;min-width:0}.configuration-history>*{min-width:0}.configuration-history>.ant-btn{justify-self:start}.revision-list{overflow:auto;max-height:280px;border:1px solid var(--edo-border);border-radius:6px}.revision-list:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.revision-list table{width:100%;border-collapse:collapse;font-size:13px;white-space:nowrap}.revision-list th,.revision-list td{padding:10px 12px;text-align:left;border-bottom:1px solid var(--edo-border)}.revision-list th{background:var(--edo-bg);font-weight:500;position:sticky;top:0}.revision-list tr:last-child td{border-bottom:0}.revision-list .ant-tag{margin-left:8px}.revision-comparison{display:flex;align-items:end;flex-wrap:wrap;gap:12px}.revision-comparison label{display:grid;gap:6px;min-width:180px;flex:1}.revision-note{color:var(--edo-muted);font-size:12px;margin:0}@media(max-width:600px){.revision-comparison label{min-width:0;flex-basis:100%}}
</style>
