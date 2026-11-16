<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { K3sPreflightReport, K3sInstallMode } from '@/api/k3s'
import { useAuthStore } from '@/stores/auth'
import { terminalActorContext } from '@/stores/hostTerminals'
import { createPreflightLogSource } from '@/utils/preflightLogChannel'
import SessionPopoutLink from '@/components/SessionPopoutLink.vue'

const props = defineProps<{report:K3sPreflightReport;mode?:K3sInstallMode;standalone?:boolean;canConfigureHost?:boolean}>()
const emit = defineEmits<{'configure-host':[id:string]}>()
const root=ref<HTMLElement>()
defineExpose({focus:()=>root.value?.focus()})
const auth=useAuthStore()
const selectedProject=()=>typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')??''
const project=ref(selectedProject()),channels=ref<Record<string,string>>({})
const identity=computed(()=>terminalActorContext(auth.user,project.value))
const allowed=computed(()=>auth.authorizationState==='ready'&&auth.canAny(['cluster.create','kubernetes.node.create']))
let mounted=false,reportIdentity='',reportProject=''
const sources:Array<ReturnType<typeof createPreflightLogSource>>=[]
function closeChannels(){for(const source of sources)source.close();sources.length=0;channels.value={}}
function syncChannels(){
  closeChannels()
  if(!mounted||!allowed.value||identity.value!==reportIdentity||project.value!==reportProject||typeof BroadcastChannel==='undefined')return
  const context=identity.value,scope=project.value
  for(const host of props.report.hosts){
    if(!host.logs)continue
    const source=createPreflightLogSource(host.logs,context,()=>allowed.value&&identity.value===context&&selectedProject()===scope)
    sources.push(source);channels.value[host.host_id]=source.channel
  }
}
function syncProject(){project.value=selectedProject()}
watch([identity,allowed],syncChannels,{flush:'sync'})
watch(()=>props.report,()=>{reportIdentity=identity.value;reportProject=project.value;syncChannels()},{flush:'sync'})
onMounted(()=>{mounted=true;reportIdentity=identity.value;reportProject=project.value;syncChannels();window.addEventListener('storage',syncProject);window.addEventListener('edo:projects-changed',syncProject)})
onBeforeUnmount(()=>{mounted=false;closeChannels();window.removeEventListener('storage',syncProject);window.removeEventListener('edo:projects-changed',syncProject)})
const groups = computed(() => [
  ...(props.report.checks.length?[{id:'cluster',name:'集群检查',checks:props.report.checks}]:[]),
  ...props.report.hosts.map(host=>({id:host.host_id,name:host.host_name,checks:host.checks})),
])
const checks = computed(() => groups.value.flatMap(group=>group.checks))
const failed = computed(() => checks.value.filter(check=>check.status==='failed').length)
const privilegeHosts=computed(()=>props.report.hosts.filter(host=>host.privilege))
const failedGroups=computed(()=>groups.value.map(group=>({...group,checks:group.checks.filter(check=>check.status==='failed'&&!(check.id==='privilege'&&privilegeHosts.value.some(host=>host.host_id===group.id)))})).filter(group=>group.checks.length))
const safetyMessage=computed(()=>{
  if(!props.report.hosts.length||props.report.hosts.some(host=>!host.checks.length))return '检测结果不完整，请重新检测。'
  if(props.report.passed&&(failed.value>0||props.report.hosts.some(host=>!host.passed||host.privilege&&!host.privilege.ready))||!props.report.passed&&failed.value===0||props.report.hosts.some(host=>host.passed&&host.checks.some(check=>check.status==='failed')))return '检测结果未确认通过，请重新检测。'
  return ''
})
</script>

<template>
  <section ref="root" class="preflight-report" aria-label="安装前置检查报告" tabindex="-1">
    <section v-for="host in privilegeHosts" :key="host.host_id" class="privilege-result" :aria-label="`${host.host_name} 的安装权限检测`">
      <header><h4>{{ host.host_name }}</h4><span>{{ host.privilege!.distribution }}</span><a-tag :color="host.privilege!.ready?'green':'red'">{{ host.privilege!.ready?'安装权限已就绪':'安装权限未就绪' }}</a-tag></header>
      <p role="status">{{ host.privilege!.message }}</p>
      <ol v-if="!host.privilege!.ready"><li v-for="step in host.privilege!.guidance" :key="step">{{ step }}</li></ol>
      <a-button v-if="!host.privilege!.ready&&canConfigureHost" size="small" @click="emit('configure-host',host.host_id)">打开主机配置</a-button>
      <p v-else-if="!host.privilege!.ready" class="privilege-permission">请联系有主机编辑权限的管理员处理。</p>
    </section>
    <a-alert v-if="safetyMessage" type="error" show-icon :message="safetyMessage" role="status" />
    <p v-else-if="!failed" class="preflight-clear" role="status">未发现不满足项</p>
    <section v-for="group in failedGroups" :key="group.id" class="preflight-group">
      <h4>{{ group.name }}</h4>
      <ul class="preflight-checks"><li v-for="check in group.checks" :key="check.id"><a-tag color="red">不满足</a-tag><div><strong>{{ check.name }}</strong><p>{{ check.message }}</p></div></li></ul>
    </section>
    <details v-for="host in report.hosts.filter(host=>host.logs)" :key="`logs-${host.host_id}`" class="preflight-terminal">
      <summary>{{ report.hosts.length>1?`${host.host_name} · `:'' }}执行日志 <SessionPopoutLink v-if="channels[host.host_id]" kind="preflight-log" :channel="channels[host.host_id]" :title="`${host.host_name} · 检测执行日志`" /></summary>
      <pre tabindex="0" role="log" :aria-label="`${host.host_name} 检测执行日志`">{{ host.logs }}</pre>
    </details>
    <a class="preflight-docs" href="https://docs.k3s.io/zh/installation/configuration" target="_blank" rel="noopener noreferrer">官方安装说明</a>
  </section>
</template>

<style scoped>
.preflight-terminal{min-width:0;font-size:12px}.preflight-terminal summary{cursor:pointer;padding:8px 0}.preflight-terminal pre{margin:4px 0;padding:12px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg);color:var(--edo-text);font:12px/1.7 SFMono-Regular,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere;max-height:320px;overflow:auto}.preflight-terminal :is(summary,pre):focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
.privilege-result{min-width:0;padding:12px 0;border-bottom:1px solid var(--edo-border);font-size:13px;line-height:1.65;overflow-wrap:anywhere}.privilege-result header{display:flex;align-items:center;gap:8px 12px;flex-wrap:wrap}.privilege-result h4{margin:0;font-size:13px;font-weight:500}.privilege-result header>span{color:var(--edo-muted)}.privilege-result p{margin:8px 0}.privilege-result ol{margin:8px 0 12px;padding-left:22px;color:var(--edo-text)}.privilege-result li+li{margin-top:5px}.privilege-permission{color:var(--edo-muted)}
.preflight-report{display:grid;grid-template-columns:minmax(0,1fr);min-width:0;gap:12px}.preflight-report>*{min-width:0}.preflight-report :deep(.ant-alert-content){min-width:0;overflow-wrap:anywhere}.preflight-clear{margin:0;font-size:13px}.preflight-group{min-width:0}.preflight-group h4{margin:0 0 4px;font-size:12px;font-weight:600;overflow-wrap:anywhere}.preflight-checks{list-style:none;margin:0;padding:0}.preflight-checks li{display:flex;align-items:flex-start;gap:10px;padding:10px 0;border-bottom:1px solid var(--edo-border);font-size:12px}.preflight-checks .ant-tag{margin:0;flex:none}.preflight-checks strong{font-weight:500}.preflight-checks p{margin:4px 0 0;color:var(--edo-muted);line-height:1.65;white-space:pre-wrap;overflow-wrap:anywhere}.preflight-checks li>div{min-width:0}.preflight-docs{justify-self:start;font-size:12px}
</style>
