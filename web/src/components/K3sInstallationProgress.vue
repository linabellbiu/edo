<script setup lang="ts">
import { computed, ref } from 'vue'
import { Server } from 'lucide-vue-next'
import type { K3sInstallation } from '@/api/k3s'
import { k3sElapsed, k3sHostStatusLabel, k3sInstallationLogView, k3sInstallationRunning, k3sStageLabel } from '@/utils/k3sProvision'
import { formatDateTime } from '@/utils/time'

const props=defineProps<{installation:K3sInstallation;now:number}>()
const running=computed(()=>k3sInstallationRunning(props.installation))
const end=computed(()=>running.value?props.now:props.installation.finished_at)
const elapsed=computed(()=>k3sElapsed(props.installation.started_at,end.value))
const logs=computed(()=>k3sInstallationLogView(props.installation))
const viewport=ref<HTMLElement>()
const hosts=computed(()=>(props.installation.hosts??[]).map(host=>({
  ...host,stageLabel:k3sStageLabel(host.stage),elapsed:k3sElapsed(host.stage_started_at,running.value&&['running','installing'].includes(host.status??'')?props.now:host.progress_updated_at??props.installation.finished_at),
  waiting:running.value&&['running','installing'].includes(host.status??'')&&Number.isFinite(Date.parse(host.progress_updated_at??''))&&props.now-Date.parse(host.progress_updated_at!)>90_000,
})))
function showLatest(){if(viewport.value)viewport.value.scrollTop=viewport.value.scrollHeight}
</script>

<template>
  <section class="installation-progress" aria-label="安装任务进度">
    <dl class="progress-times"><div><dt>开始时间</dt><dd>{{ formatDateTime(installation.started_at) }}</dd></div><div><dt>总耗时</dt><dd>{{ elapsed }}</dd></div><div v-if="!running"><dt>结束时间</dt><dd>{{ formatDateTime(installation.finished_at) }}</dd></div></dl>
    <div class="progress-hosts">
      <article v-for="host in hosts" :key="host.id" :aria-label="`${host.name} 安装进度`">
        <header><Server :size="16" aria-hidden="true" /><span class="host-identity"><strong>{{ host.name }}</strong><small>{{ host.address }}</small></span><a-tag :color="host.status==='succeeded'?'success':host.status==='failed'?'error':host.status==='installing'||host.status==='running'?'processing':undefined">{{ k3sHostStatusLabel(host.status) }}</a-tag></header>
        <div v-if="host.stageLabel||host.message" class="host-stage"><strong role="status" aria-live="polite">{{ host.stageLabel||host.message }}</strong><span v-if="host.stage_started_at">本阶段 {{ host.elapsed }}</span></div>
        <p v-if="host.message&&host.stageLabel&&host.message!==host.stageLabel">{{ host.message }}</p>
        <div v-if="host.progress_updated_at" class="host-feedback"><span>最近反馈 {{ formatDateTime(host.progress_updated_at) }}</span><span v-if="host.waiting" class="feedback-delayed">暂未收到新反馈</span></div>
      </article>
    </div>
    <section class="progress-logs"><header><h4>安装日志</h4><a-button size="small" @click="showLatest">查看最新</a-button></header><p v-if="logs.truncated" class="log-limit">仅显示最近 64 KiB 日志</p><pre ref="viewport" role="log" aria-label="安装安全日志" aria-live="off" tabindex="0">{{ logs.text||'等待安装日志…' }}</pre></section>
  </section>
</template>

<style scoped>
.installation-progress{display:grid;gap:18px;min-width:0}.progress-times{display:flex;gap:20px 32px;flex-wrap:wrap;margin:0;padding:12px 0;border-block:1px solid var(--edo-border)}.progress-times dt{font-size:12px;color:var(--edo-muted);margin-bottom:5px}.progress-times dd{margin:0;font-size:13px;font-variant-numeric:tabular-nums}.progress-hosts{display:grid;gap:0}.progress-hosts article{padding:14px 0;border-bottom:1px solid var(--edo-border);min-width:0}.progress-hosts header{display:flex;gap:10px;align-items:center}.progress-hosts .host-identity{flex:1;min-width:0}.progress-hosts strong,.progress-hosts small{display:block;overflow-wrap:anywhere}.progress-hosts strong{font-size:13px;font-weight:500}.progress-hosts small{font-size:12px;color:var(--edo-muted);margin-top:3px}.host-stage,.host-feedback{display:flex;align-items:center;flex-wrap:wrap;gap:6px 16px;margin:10px 0 0 26px;font-size:12px}.host-stage>span,.host-feedback{color:var(--edo-muted);font-variant-numeric:tabular-nums}.progress-hosts p{margin:7px 0 0 26px;color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}.feedback-delayed{color:var(--edo-warning,#b7791f)}.progress-logs{min-width:0}.progress-logs header{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:8px}.progress-logs h4{margin:0;font-size:13px;font-weight:600}.progress-logs pre{margin:0;padding:14px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg);color:var(--edo-text);font:12px/1.7 SFMono-Regular,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere;max-height:360px;overflow:auto;tab-size:2}.progress-logs pre:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}.log-limit{font-size:12px;color:var(--edo-muted);margin:0 0 8px}@media(max-width:650px){.progress-times{gap:14px 20px}.host-stage,.host-feedback{margin-left:0}}
</style>
