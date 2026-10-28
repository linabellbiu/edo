<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import type { InfrastructureHost } from '@/api/infrastructure'
import { getK3sInstallation, listK3sInstallations, listK3sUninstallTargets, previewK3sUninstallPlan, startK3sUninstallation, type K3sInstallation, type K3sPlan, type K3sUninstallTarget } from '@/api/k3s'
import { apiErrorMessage } from '@/api/resources'
import K3sInstallationProgress from '@/components/K3sInstallationProgress.vue'
import { useAuthStore } from '@/stores/auth'
import { k3sHostRoleLabel, k3sInstallationLabel, k3sInstallationRunning } from '@/utils/k3sProvision'
import { k3sUninstallResultUncertain, sameK3sUninstallTarget, validK3sUninstallPlan, validK3sUninstallTarget, validK3sUninstallation } from '@/utils/k3sUninstall'
import { formatDateTime } from '@/utils/time'

const props=defineProps<{open:boolean;host?:InfrastructureHost}>()
const emit=defineEmits<{'update:open':[value:boolean];completed:[installation:K3sInstallation]}>()
const auth=useAuthStore()
const targets=ref<K3sUninstallTarget[]>([]),selectedID=ref(''),loading=ref(false),loadError=ref('')
const recent=ref<K3sInstallation[]>([]),recentLoading=ref(false),recentError=ref('')
const plan=ref<K3sPlan>(),installation=ref<K3sInstallation>(),confirmation=ref(''),error=ref(''),pollError=ref('')
const previewing=ref(false),submitting=ref(false),now=ref(Date.now()),selectedProject=ref(readProject())
const reviewRegion=ref<HTMLElement>(),errorRegion=ref<HTMLElement>()
let mounted=true,revision=0,planRevision=0,operationContext='',pollFailures=0
let targetRequest:AbortController|undefined,recentRequest:AbortController|undefined,previewRequest:AbortController|undefined,submitRequest:AbortController|undefined,pollRequest:AbortController|undefined
let pollTimer:ReturnType<typeof setTimeout>|undefined,clock:ReturnType<typeof setInterval>|undefined
const completedIDs=new Set<string>()
const context=computed(()=>JSON.stringify([auth.user?.id??'',auth.user?.department_id??'',auth.user?.project_id??'',auth.user?.is_superuser??false,[...(auth.user?.permissions??[])].sort(),selectedProject.value,props.host?.id??'',props.host?.address??'',props.host?.ssh_port,props.host?.ssh_username,props.host?.mode,props.host?.is_active,props.host?.is_builtin,props.host?.credential_configured,props.host?.use_sudo]))
const canOperate=computed(()=>Boolean(auth.user?.id&&selectedProject.value&&auth.user.project_id===selectedProject.value&&auth.canAny(['cluster.read'])&&auth.canAny(['kubernetes.k3s.uninstall'])&&props.host?.mode==='ssh'&&!props.host.is_builtin&&props.host.is_active&&props.host.credential_configured!==false))
const selectedTarget=computed(()=>targets.value.find(target=>target.source_installation_id===selectedID.value))
const busy=computed(()=>previewing.value||submitting.value)
const running=computed(()=>k3sInstallationRunning(installation.value))
const uncertain=computed(()=>k3sUninstallResultUncertain(installation.value))
const hostLocked=computed(()=>uncertain.value||recent.value.some(item=>k3sUninstallResultUncertain(item)))
const expired=computed(()=>Boolean(plan.value)&&(!Number.isFinite(Date.parse(plan.value!.expires_at))||Date.parse(plan.value!.expires_at)<=now.value))
const remaining=computed(()=>plan.value&&!expired.value?Math.max(0,Math.ceil((Date.parse(plan.value.expires_at)-now.value)/1000)):0)
const canPreview=computed(()=>canOperate.value&&!hostLocked.value&&!recentLoading.value&&!recentError.value&&!loading.value&&!loadError.value&&!busy.value&&!installation.value&&Boolean(selectedTarget.value))
const canSubmit=computed(()=>canOperate.value&&!hostLocked.value&&!busy.value&&!installation.value&&!expired.value&&confirmation.value==='确认'&&Boolean(plan.value&&selectedTarget.value&&validK3sUninstallPlan(plan.value,selectedTarget.value,props.host?.address??'')))
const targetOptions=computed(()=>targets.value.map(target=>({value:target.source_installation_id,label:`${target.installation_name} · ${k3sHostRoleLabel(target.role)} · ${k3sInstallationLabel(target.status)}${target.created_at?` · ${formatDateTime(target.created_at)}`:''}`})))
const facts=computed(()=>plan.value?.uninstall?[
  {label:'来源安装',value:selectedTarget.value?.installation_name||plan.value.uninstall.source_installation_id},
  {label:'主机',value:plan.value.hosts?.[0]?.name??''},{label:'地址',value:plan.value.hosts?.[0]?.address??''},
  {label:'节点角色',value:k3sHostRoleLabel(plan.value.uninstall.role)},{label:'官方卸载脚本',value:plan.value.uninstall.script_path},
  {label:'系统服务',value:plan.value.uninstall.service_name},{label:'数据目录',value:plan.value.uninstall.data_dir},
]:[])
const progressTone=computed(()=>uncertain.value?'warning':installation.value?.status==='succeeded'?'success':['failed','interrupted'].includes(installation.value?.status??'')?'error':'info')
const uncertainMessage='主机锁已保留，当前禁止再次安装或卸载。请核对安全日志和主机状态。'
function taskLabel(item:K3sInstallation){return k3sUninstallResultUncertain(item)?'卸载结果未确认':k3sInstallationLabel(item.status,'uninstall')}

function readProject(){return typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')??''}
function syncProject(){const current=readProject();if(current===selectedProject.value)return true;selectedProject.value=current;return false}
function currentScope(expected:string,current:number){return mounted&&props.open&&syncProject()&&canOperate.value&&revision===current&&context.value===expected}
function projectChanged(event:Event){if(event.type==='storage'&&(event as StorageEvent).key!==null&&(event as StorageEvent).key!=='edo.project')return;syncProject()}
function stopPoll(){if(pollTimer)clearTimeout(pollTimer);pollTimer=undefined;pollRequest?.abort();pollRequest=undefined}
function invalidatePlan(){planRevision++;previewRequest?.abort();submitRequest?.abort();previewRequest=undefined;submitRequest=undefined;previewing.value=false;submitting.value=false;plan.value=undefined;confirmation.value='';error.value=''}
function cancelRequests(){revision++;invalidatePlan();targetRequest?.abort();recentRequest?.abort();stopPoll();loading.value=false;recentLoading.value=false}
function reset(){cancelRequests();targets.value=[];recent.value=[];selectedID.value='';installation.value=undefined;operationContext='';loadError.value='';recentError.value='';pollError.value='';pollFailures=0}
async function focusError(){await nextTick();if(mounted&&props.open)errorRegion.value?.focus()}

async function loadTargets(){
  if(!syncProject()||!props.open||!canOperate.value||!props.host)return
  targetRequest?.abort();const controller=new AbortController();targetRequest=controller
  const expected=context.value,current=revision,hostID=props.host.id
  loading.value=true;loadError.value=''
  try{
    const result=await listK3sUninstallTargets(hostID,controller.signal)
    if(!currentScope(expected,current)||controller.signal.aborted)return
    if(!Array.isArray(result)||result.length>200||result.some(item=>!validK3sUninstallTarget(item,hostID))||new Set(result.map(item=>item.source_installation_id)).size!==result.length){targets.value=[];loadError.value='卸载来源信息不完整，请重新读取。';invalidatePlan();return}
    const previous=selectedTarget.value,next=result.find(target=>target.source_installation_id===selectedID.value)
    if(previous&&next&&!sameK3sUninstallTarget(previous,next))invalidatePlan()
    targets.value=result
    if(selectedID.value&&!result.some(target=>target.source_installation_id===selectedID.value))selectedID.value=''
  }catch(err){if(currentScope(expected,current)&&!controller.signal.aborted){targets.value=[];loadError.value=apiErrorMessage(err);invalidatePlan()}}
  finally{if(currentScope(expected,current)&&targetRequest===controller)loading.value=false}
}
async function loadRecent(){
  if(!syncProject()||!props.open||!canOperate.value||!props.host)return
  recentRequest?.abort();const controller=new AbortController();recentRequest=controller
  const expected=context.value,current=revision,hostID=props.host.id
  recentLoading.value=true;recentError.value=''
  try{
    const result=await listK3sInstallations('','uninstall',controller.signal,hostID)
    if(!currentScope(expected,current)||controller.signal.aborted)return
    if(!Array.isArray(result)||result.length>10||result.some(item=>!validK3sUninstallation(item,hostID))){recent.value=[];recentError.value='卸载记录信息不完整，请重新读取。';return}
    recent.value=result
    if(result.some(item=>k3sUninstallResultUncertain(item)))invalidatePlan()
  }catch(err){if(currentScope(expected,current)&&!controller.signal.aborted)recentError.value=apiErrorMessage(err)}
  finally{if(currentScope(expected,current)&&recentRequest===controller)recentLoading.value=false}
}
async function preview(){
  if(!syncProject()||!props.open||!canPreview.value||!props.host||!selectedTarget.value)return
  invalidatePlan();previewing.value=true
  const target=selectedTarget.value,expected=context.value,current=revision,operation=planRevision,controller=new AbortController();previewRequest=controller
  try{
    const result=await previewK3sUninstallPlan(target.source_installation_id,props.host.id,controller.signal)
    if(!currentScope(expected,current)||operation!==planRevision||controller.signal.aborted)return
    now.value=Date.now()
    if(!validK3sUninstallPlan(result,target,props.host.address)||Date.parse(result.expires_at)<=now.value){error.value='卸载计划与当前目标不一致或已失效，请重新核对。';await focusError();return}
    plan.value=result;await nextTick();if(currentScope(expected,current)&&operation===planRevision)reviewRegion.value?.focus()
  }catch(err){if(currentScope(expected,current)&&operation===planRevision&&!controller.signal.aborted){error.value=apiErrorMessage(err);await focusError()}}
  finally{if(currentScope(expected,current)&&operation===planRevision){previewing.value=false;previewRequest=undefined}}
}
async function execute(){
  if(!syncProject())return
  now.value=Date.now()
  if(!props.open||!canSubmit.value||!plan.value||!props.host)return
  const snapshot=plan.value,expected=context.value,current=revision,operation=planRevision,controller=new AbortController();submitRequest=controller
  submitting.value=true;error.value=''
  try{
    const result=await startK3sUninstallation(snapshot.id,confirmation.value,controller.signal)
    if(!currentScope(expected,current)||operation!==planRevision||controller.signal.aborted)return
    confirmation.value='';plan.value=undefined
    if(!validK3sUninstallation(result,props.host.id)||!sameK3sUninstallTarget(result.uninstall,snapshot.uninstall)){error.value='未能确认卸载任务目标，请重新读取卸载记录。';void loadRecent();await focusError();return}
    installation.value=result;operationContext=context.value;pollError.value='';pollFailures=0;handleProgress(result);void loadRecent()
  }catch(err){if(currentScope(expected,current)&&operation===planRevision&&!controller.signal.aborted){plan.value=undefined;confirmation.value='';error.value=apiErrorMessage(err);void loadRecent();await focusError()}}
  finally{if(currentScope(expected,current)&&operation===planRevision){submitting.value=false;submitRequest=undefined}}
}
function handleProgress(result:K3sInstallation){
  if(['succeeded','failed','interrupted'].includes(result.status)&&!completedIDs.has(result.id)){completedIDs.add(result.id);emit('completed',result);void loadTargets();void loadRecent()}
  if(k3sInstallationRunning(result)&&props.open&&mounted)pollTimer=setTimeout(()=>void refreshInstallation(),1500)
}
async function refreshInstallation(){
  if(!syncProject()||!props.open||!canOperate.value||!installation.value||!props.host)return
  stopPoll();const snapshot=installation.value,expected=context.value,current=revision,controller=new AbortController();pollRequest=controller
  try{
    const result=await getK3sInstallation(snapshot.id,controller.signal)
    if(!currentScope(expected,current)||installation.value?.id!==snapshot.id||controller.signal.aborted)return
    if(!validK3sUninstallation(result,props.host.id)||!sameK3sUninstallTarget(result.uninstall,snapshot.uninstall)){pollError.value='卸载任务目标信息发生变化，请重新读取卸载记录。';return}
    installation.value=result;pollError.value='';pollFailures=0;handleProgress(result)
  }catch(err){if(currentScope(expected,current)&&!controller.signal.aborted){pollError.value=apiErrorMessage(err);if(running.value)pollTimer=setTimeout(()=>void refreshInstallation(),Math.min(30_000,5000*2**Math.min(pollFailures++,3)))}}
}
function viewInstallation(item:K3sInstallation){
  if(!syncProject()||!props.open||!canOperate.value||busy.value||!props.host)return
  const record=recent.value.find(value=>value.id===item.id)
  if(!record||!validK3sUninstallation(record,props.host.id)||!sameK3sUninstallTarget(item.uninstall,record.uninstall)||hostLocked.value&&!k3sUninstallResultUncertain(record))return
  stopPoll();invalidatePlan();installation.value=record;operationContext=context.value;pollError.value='';pollFailures=0
  if(!k3sInstallationRunning(record))completedIDs.add(record.id)
  void refreshInstallation()
}
function configureAgain(){if(busy.value||running.value||hostLocked.value)return;stopPoll();invalidatePlan();installation.value=undefined;pollError.value='';void loadTargets();void loadRecent()}
function close(){emit('update:open',false)}
watch(selectedID,invalidatePlan,{flush:'sync'})
watch(expired,value=>{if(value)confirmation.value=''},{flush:'sync'})
watch(context,()=>{reset();if(props.open&&canOperate.value){void loadTargets();void loadRecent()}},{flush:'sync'})
watch(canOperate,allowed=>{if(!allowed)reset();else if(props.open){void loadTargets();void loadRecent()}},{flush:'sync'})
watch(()=>props.open,open=>{
  if(!open){cancelRequests();return}
  if(!installation.value||operationContext!==context.value)reset()
  void loadTargets();void loadRecent();if(installation.value)void refreshInstallation()
})
onMounted(()=>{window.addEventListener('storage',projectChanged);window.addEventListener('edo:projects-changed',projectChanged);clock=setInterval(()=>{if(props.open){now.value=Date.now();syncProject()}},1000);if(props.open){reset();void loadTargets();void loadRecent()}})
onBeforeUnmount(()=>{mounted=false;cancelRequests();window.removeEventListener('storage',projectChanged);window.removeEventListener('edo:projects-changed',projectChanged);if(clock)clearInterval(clock)})
</script>

<template>
  <a-drawer :open="open" title="卸载 K3s" width="min(820px,96vw)" @close="close">
    <div class="uninstall-content">
      <header v-if="host" class="uninstall-host"><strong>{{ host.name }}</strong><code>{{ host.address }}:{{ host.ssh_port }}</code></header>
      <a-alert v-if="!canOperate" type="warning" show-icon message="需要集群查看与 K3s 卸载权限，并使用已启用的远程 SSH 主机。" />
      <div v-if="error" ref="errorRegion" tabindex="-1"><a-alert type="error" show-icon :message="error" /></div>
      <a-alert v-if="hostLocked&&!uncertain" type="warning" show-icon message="卸载结果未确认" :description="uncertainMessage" />
      <template v-if="!installation&&!hostLocked">
        <a-alert type="warning" show-icon message="官方卸载会停止 K3s 和本机 Pod，永久删除本地集群数据、本地持久化卷及相关工具。" description="这不是删除节点或断开集群连接。请先确认备份；外部存储的数据不在此卸载范围内。" />
        <a class="uninstall-docs" href="https://docs.k3s.io/zh/installation/uninstall" target="_blank" rel="noopener noreferrer">官方卸载说明</a>
        <a-alert v-if="loadError" type="error" show-icon message="卸载来源读取失败" :description="loadError"><template #action><a-button size="small" @click="loadTargets">重试</a-button></template></a-alert>
        <a-form v-if="!plan" layout="vertical" :disabled="busy||!canOperate">
          <a-form-item label="来源安装" required><a-select v-model:value="selectedID" :loading="loading" :options="targetOptions" placeholder="选择此主机的 EDO 原生 K3s 安装" aria-label="来源安装" /></a-form-item>
          <a-empty v-if="!loading&&!loadError&&canOperate&&!targets.length" description="没有可卸载的 EDO 原生 K3s 安装记录" />
        </a-form>
        <section v-else ref="reviewRegion" class="uninstall-review" tabindex="-1" aria-label="卸载目标确认">
          <dl class="uninstall-facts"><div v-for="fact in facts" :key="fact.label"><dt>{{ fact.label }}</dt><dd>{{ fact.value }}</dd></div></dl>
          <a-alert v-if="expired" type="warning" show-icon message="卸载计划已过期，请返回重新核对。" />
          <p v-else class="plan-expiry" role="status">计划有效期剩余 {{ remaining }} 秒</p>
          <a-form layout="vertical"><a-form-item label="手动输入“确认”以卸载" required><a-input v-model:value="confirmation" :disabled="submitting||expired||!canOperate" :maxlength="20" autocomplete="off" aria-label="卸载确认" placeholder="请输入“确认”" /></a-form-item></a-form>
        </section>
      </template>
      <section v-else-if="installation" class="uninstall-result">
        <a-alert :type="progressTone" show-icon :message="taskLabel(installation)" :description="uncertain?[uncertainMessage,installation.error||installation.message].filter(Boolean).join(' '):installation.error||installation.message||(running?'可以关闭窗口，卸载任务会在后台继续。':'')" />
        <p class="operation-id">任务编号：{{ installation.id }}</p>
        <a-alert v-if="pollError" type="warning" show-icon message="进度读取失败，后台卸载可能仍在运行" :description="pollError"><template #action><a-button size="small" @click="refreshInstallation"><RefreshCw :size="13" />重新读取</a-button></template></a-alert>
        <K3sInstallationProgress :installation="installation" :now="now" />
      </section>
      <section v-if="recent.length||recentError||recentLoading" class="uninstall-history"><header><h4>卸载记录</h4><a-button size="small" :loading="recentLoading" :disabled="!canOperate" @click="loadRecent">刷新</a-button></header><a-alert v-if="recentError" type="warning" :message="recentError" /><div v-for="item in recent" :key="item.id" class="history-row"><span><strong>{{ item.name||item.id }}</strong><small>{{ formatDateTime(item.created_at) }}</small></span><a-tag>{{ taskLabel(item) }}</a-tag><a-button size="small" :disabled="busy||!canOperate||hostLocked&&!k3sUninstallResultUncertain(item)" @click="viewInstallation(item)">查看进度</a-button></div></section>
    </div>
    <template #footer><div class="uninstall-footer"><a-button @click="close">{{ running?'关闭窗口':'关闭' }}</a-button><a-button v-if="hostLocked" :disabled="!canOperate" @click="installation?refreshInstallation():loadRecent()">刷新日志</a-button><a-button v-if="plan&&!installation&&!hostLocked" :disabled="busy" @click="invalidatePlan">返回核对</a-button><a-button v-if="!plan&&!installation&&!hostLocked" type="primary" :loading="previewing" :disabled="!canPreview" @click="preview">预览卸载计划</a-button><a-button v-else-if="plan&&!installation&&!hostLocked" type="primary" danger :loading="submitting" :disabled="!canSubmit" @click="execute">确认卸载</a-button><a-button v-if="installation&&!running&&!hostLocked" :disabled="busy||!canOperate" @click="configureAgain">返回来源选择</a-button></div></template>
  </a-drawer>
</template>

<style scoped>
.uninstall-content{display:grid;grid-template-columns:minmax(0,1fr);gap:18px;min-width:0}.uninstall-content>*{min-width:0}.uninstall-content :deep(.ant-alert-content),.uninstall-content :deep(.ant-form-item){min-width:0;overflow-wrap:anywhere}.uninstall-host{display:flex;gap:8px 18px;align-items:center;flex-wrap:wrap}.uninstall-host strong{font-size:15px;font-weight:600}.uninstall-host code{color:var(--edo-muted);overflow-wrap:anywhere;font-size:12px}.uninstall-docs{justify-self:start;font-size:12px}.uninstall-review,.uninstall-result{display:grid;grid-template-columns:minmax(0,1fr);gap:16px;min-width:0}.uninstall-facts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px 20px;margin:0;padding:14px 0;border-block:1px solid var(--edo-border)}.uninstall-facts>div{min-width:0}.uninstall-facts dt{font-size:12px;color:var(--edo-muted);margin-bottom:5px}.uninstall-facts dd{margin:0;font-size:13px;overflow-wrap:anywhere;white-space:pre-wrap}.plan-expiry,.operation-id{margin:0;color:var(--edo-muted);font-size:12px;font-variant-numeric:tabular-nums;overflow-wrap:anywhere}.uninstall-history{border-top:1px solid var(--edo-border);padding-top:14px;min-width:0}.uninstall-history header{display:flex;justify-content:space-between;align-items:center;gap:12px;margin-bottom:8px}.uninstall-history h4{margin:0;font-size:13px;font-weight:600}.history-row{display:flex;align-items:center;flex-wrap:wrap;gap:8px;padding:12px 0;border-bottom:1px solid var(--edo-border)}.history-row>span:first-child{flex:1;min-width:120px}.history-row strong,.history-row small{display:block;overflow-wrap:anywhere}.history-row strong{font-size:12px;font-weight:500}.history-row small{font-size:11px;color:var(--edo-muted);margin-top:4px}.uninstall-footer{display:flex;justify-content:flex-end;gap:8px;flex-wrap:wrap}.uninstall-review:focus-visible{outline:2px solid var(--edo-primary);outline-offset:3px}@media(max-width:650px){.uninstall-facts{grid-template-columns:minmax(0,1fr)}}
</style>
