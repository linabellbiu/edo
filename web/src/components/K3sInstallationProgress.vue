<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { Server } from 'lucide-vue-next'
import type { K3sInstallation } from '@/api/k3s'
import { k3sElapsed, k3sHostStatusLabel, k3sInstallationLogView, k3sInstallationRunning, k3sStageLabel } from '@/utils/k3sProvision'
import { formatDateTime } from '@/utils/time'

const props=defineProps<{installation:K3sInstallation;now:number}>()
const actionLabel=computed(()=>props.installation.action==='uninstall'?'卸载':'安装')
const running=computed(()=>k3sInstallationRunning(props.installation))
const end=computed(()=>running.value?props.now:props.installation.finished_at)
const elapsed=computed(()=>k3sElapsed(props.installation.started_at,end.value))
const logs=computed(()=>k3sInstallationLogView(props.installation))
const viewport=ref<HTMLElement>()
const uninstall=computed(()=>props.installation.action==='uninstall')
const hosts=computed(()=>(props.installation.hosts??[]).map(host=>({
  ...host,stageLabel:k3sStageLabel(host.stage,props.installation.action),
  seconds:confirmedSeconds(host.stage_started_at,host.progress_updated_at??(!running.value?props.installation.finished_at:undefined)),
})))
const activeStage=computed(()=>hosts.value.find(host=>['running','installing','pending'].includes(host.status??''))?.stageLabel)
function confirmedSeconds(start?:string,end?:string){
  const from=Date.parse(start??''),to=Date.parse(end??'')
  return Number.isFinite(from)&&Number.isFinite(to)&&to>=from?Math.floor((to-from)/1000):undefined
}
const following=ref(true)
function onLogScroll(){const element=viewport.value;if(element)following.value=element.scrollHeight-element.scrollTop-element.clientHeight<32}
function showLatest(){following.value=true;if(viewport.value)viewport.value.scrollTop=viewport.value.scrollHeight}
watch(()=>logs.value.text,async()=>{const shouldFollow=following.value;await nextTick();if(shouldFollow)showLatest()},{immediate:true})
</script>

<template>
  <section class="installation-progress" :aria-label="`${actionLabel}任务进度`">
    <dl v-if="!uninstall" class="progress-times"><div><dt>开始时间</dt><dd>{{ formatDateTime(installation.started_at) }}</dd></div><div><dt>总耗时</dt><dd>{{ elapsed }}</dd></div><div v-if="!running"><dt>结束时间</dt><dd>{{ formatDateTime(installation.finished_at) }}</dd></div></dl>
    <p v-if="uninstall&&running&&activeStage" class="compact-stage" role="status" aria-live="polite">{{ activeStage }}</p>
    <div v-if="!uninstall" class="progress-hosts">
      <article v-for="host in hosts" :key="host.id" :aria-label="`${host.name} ${actionLabel}进度`">
        <header><Server :size="16" aria-hidden="true" /><span class="host-identity"><strong>{{ host.name }}</strong><small>{{ host.address }}</small></span><a-tag :color="host.status==='succeeded'?'success':host.status==='failed'?'error':host.status==='installing'||host.status==='running'?'processing':undefined">{{ k3sHostStatusLabel(host.status,installation.action) }}</a-tag></header>
        <div v-if="host.seconds!==undefined" class="host-stage" role="status" aria-live="polite">已持续 {{ host.seconds }} 秒</div>
        <p v-if="host.status==='failed'&&host.message">{{ host.message }}</p>
      </article>
    </div>
    <section class="progress-logs"><header><div class="log-heading"><h4>{{ actionLabel }}日志</h4><span v-if="running" class="background-hint">可以关闭窗口，{{ actionLabel }}会继续执行。</span></div><a-button v-if="!uninstall" size="small" @click="showLatest">查看最新</a-button></header><p v-if="logs.truncated" class="log-limit">仅显示最近 2 MiB 日志</p><pre ref="viewport" @scroll="onLogScroll" role="log" :aria-label="uninstall?'卸载安全日志':'安装执行日志'" aria-live="off" tabindex="0">{{ logs.text||`等待${actionLabel}日志…` }}</pre></section>
  </section>
</template>

<style scoped>
.installation-progress{display:grid;gap:18px;min-width:0}.progress-times{display:flex;gap:20px 32px;flex-wrap:wrap;margin:0;padding:12px 0;border-block:1px solid var(--edo-border)}.progress-times dt{font-size:12px;color:var(--edo-muted);margin-bottom:5px}.progress-times dd{margin:0;font-size:13px;font-variant-numeric:tabular-nums}.progress-hosts{display:grid;gap:0}.progress-hosts article{padding:14px 0;border-bottom:1px solid var(--edo-border);min-width:0}.progress-hosts header{display:flex;gap:10px;align-items:center}.progress-hosts .host-identity{flex:1;min-width:0}.progress-hosts strong,.progress-hosts small{display:block;overflow-wrap:anywhere}.progress-hosts strong{font-size:13px;font-weight:500}.progress-hosts small{font-size:12px;color:var(--edo-muted);margin-top:3px}.host-stage{display:flex;align-items:center;flex-wrap:wrap;gap:6px 16px;margin:10px 0 0 26px;font-size:12px}.host-stage>span{color:var(--edo-muted);font-variant-numeric:tabular-nums}.progress-hosts p{margin:7px 0 0 26px;color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}.compact-stage{margin:0;color:var(--edo-muted);font-size:12px;line-height:1.6}.progress-logs{min-width:0}.progress-logs header{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:8px}.log-heading{display:flex;align-items:center;flex-wrap:wrap;gap:6px 12px;min-width:0}.background-hint{font-size:12px;color:#946200;line-height:1.6}:global(:root[data-theme="dark"] .installation-progress .background-hint){color:#f2cc60}.progress-logs h4{margin:0;font-size:13px;font-weight:600}.progress-logs pre{margin:0;padding:14px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg);color:var(--edo-text);font:12px/1.7 SFMono-Regular,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere;max-height:360px;overflow:auto;tab-size:2}.progress-logs pre:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.log-limit{font-size:12px;color:var(--edo-muted);margin:0 0 8px}@media(max-width:650px){.progress-times{gap:14px 20px}.host-stage{margin-left:0}}
</style>
