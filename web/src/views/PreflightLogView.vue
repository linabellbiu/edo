<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { terminalActorContext } from '@/stores/hostTerminals'
import { requestPreflightLog } from '@/utils/preflightLogChannel'

const props=defineProps<{channel:string;title:string}>()
const auth=useAuthStore(),text=ref(''),error=ref(''),loading=ref(false)
const identity=computed(()=>terminalActorContext(auth.user,typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')??''))
let disposed=false,revision=0,controller:AbortController|undefined
function stop(){revision++;controller?.abort();controller=undefined;loading.value=false}
async function load(){
  stop();text.value='';error.value=''
  if(disposed||auth.authorizationState!=='ready'||!auth.canAny(['cluster.create','kubernetes.node.create']))return
  const request=new AbortController(),current=revision,context=identity.value,channel=props.channel
  controller=request;loading.value=true
  try{
    const snapshot=await requestPreflightLog(channel,context,request.signal)
    if(!disposed&&!request.signal.aborted&&current===revision&&identity.value===context&&props.channel===channel)text.value=snapshot
  }catch(cause){
    if(!disposed&&!request.signal.aborted&&current===revision)error.value=cause instanceof Error?cause.message:'检测日志读取失败，请重试。'
  }finally{if(controller===request){controller=undefined;loading.value=false}}
}
watch([()=>props.channel,identity,()=>auth.authorizationState],()=>void load(),{immediate:true})
onBeforeUnmount(()=>{disposed=true;stop();text.value=''})
</script>

<template>
  <section class="preflight-log-view" :aria-label="title">
    <header><strong>{{ title }}</strong><a-button size="small" :loading="loading" @click="load"><RefreshCw :size="14" />重新读取</a-button></header>
    <a-alert v-if="error" type="warning" show-icon :message="error"><template #action><a-button size="small" :loading="loading" @click="load">重试</a-button></template></a-alert>
    <pre role="log" :aria-label="title" aria-live="off" tabindex="0">{{ text || (loading ? '正在读取检测日志…' : error ? '' : '暂无检测日志。') }}</pre>
  </section>
</template>

<style scoped>
.preflight-log-view{display:flex;flex-direction:column;gap:12px;min-height:0;height:100%;padding:16px;color:var(--edo-text)}.preflight-log-view header{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}.preflight-log-view strong{font-size:13px;font-weight:600;overflow-wrap:anywhere}.preflight-log-view :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}.preflight-log-view pre{flex:1;min-height:180px;margin:0;padding:14px;border:1px solid var(--edo-border);border-radius:5px;background:var(--edo-bg);color:var(--edo-text);font:13px/1.7 SFMono-Regular,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere;overflow:auto;tab-size:2}.preflight-log-view pre:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}@media(max-width:600px){.preflight-log-view{padding:10px}}
</style>
