<script setup>
import { ref } from 'vue'
import KubeWorkloadPodRuntime from '/src/components/KubeWorkloadPodRuntime.vue'
import ContainerLogDrawer from '/src/components/ContainerLogDrawer.vue'
import TerminalDrawer from '/src/components/TerminalDrawer.vue'
import PipelineLogDrawer from '/src/components/PipelineLogDrawer.vue'
import SessionPopoutLink from '/src/components/SessionPopoutLink.vue'
import K3sInstallationProgress from '/src/components/K3sInstallationProgress.vue'
import { useAuthStore } from '/src/stores/auth'
const props = defineProps(['pod','installation'])
const auth = useAuthStore(), container = ref('web')
const logs = ref({open:false,path:'',title:''}), terminal = ref({open:false,path:'',title:''}), pipelineOpen = ref(false)
function openStream(target, isTerminal = false) {
  if(target.podUID !== props.pod.metadata.uid || target.container !== container.value)return
  const base = isTerminal ? '/api/v1/terminals/kubernetes/preview-cluster' : '/api/v1/kubernetes/clusters/preview-cluster'
  const path = `${base}/namespaces/preview/pods/preview-web/containers/${target.container}/${isTerminal?'ws':'logs/ws'}?pod_uid=${target.podUID}`
  ;(isTerminal?terminal:logs).value={open:true,path,title:`preview-web / ${target.container}`}
}
</script>
<template>
  <main class="fixture">
    <header><h1>日志与终端新标签页</h1><span>本地交互夹具 · API 与终端均为模拟数据</span></header>
    <section class="kube-detail-drawer workload-panel"><KubeWorkloadPodRuntime cluster="preview-cluster" :pod="pod" :container="container" :auto-refresh="false" :refresh-token="0" :can="permission=>auth.canAny([permission])" :can-follow="()=>false" @container="container=$event.container" @logs="openStream($event)" @terminal="openStream($event,true)" /></section>
    <section class="other-entries"><h2>其他入口</h2><div><a-button @click="pipelineOpen=true">流水线日志</a-button><SessionPopoutLink kind="pipeline-log" runID="preview-run" nodeID="preview-task" title="任务日志：镜像构建" /></div><div><a-button @click="terminal={open:true,path:'/api/v1/terminals/hosts/preview-host/ws',title:'主机终端：本地主机'}">主机终端</a-button><SessionPopoutLink kind="terminal" path="/api/v1/terminals/hosts/preview-host/ws" title="主机终端：本地主机" /></div></section>
    <section><K3sInstallationProgress :installation="installation" :now="Date.now()" /></section>
    <ContainerLogDrawer v-model:open="logs.open" :path="logs.path" :title="logs.title" />
    <TerminalDrawer v-model:open="terminal.open" :path="terminal.path" :title="terminal.title" :permission="terminal.path.includes('/hosts/')?'host.terminal.open':'terminal.open'" />
    <PipelineLogDrawer v-model:open="pipelineOpen" runID="preview-run" nodeID="preview-task" title="任务日志：镜像构建" initial-status="succeeded" />
  </main>
</template>
<style scoped>
.fixture{max-width:1000px;margin:auto;padding:20px;color:var(--edo-text)}.fixture>header{display:flex;align-items:center;flex-wrap:wrap;gap:10px;margin-bottom:18px}.fixture h1{font-size:18px;margin:0}.fixture>header>span{font-size:12px;color:var(--edo-muted)}.fixture>section{margin-bottom:20px}.other-entries{display:flex;align-items:center;gap:24px;flex-wrap:wrap;padding:14px;border:1px solid var(--edo-border);border-radius:4px}.other-entries h2{font-size:13px;margin:0}.other-entries>div{display:flex;gap:4px;align-items:center}.fixture .workload-panel{--workload-text:var(--edo-text);--workload-title:var(--edo-text);--workload-muted:var(--edo-muted);--workload-border:var(--edo-border);--workload-column:var(--edo-surface);--workload-heading:var(--edo-bg);--workload-idle-border:var(--edo-border);--workload-link:var(--edo-primary);--workload-focus:var(--edo-primary);--workload-selected-border:var(--edo-primary);--workload-selected:var(--edo-primary-soft)}@media(max-width:600px){.fixture{padding:12px}}
</style>
