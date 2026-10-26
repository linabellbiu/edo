<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Check, ChevronLeft, RefreshCw, Server } from 'lucide-vue-next'
import { useRouter } from 'vue-router'
import { listHosts, type InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage } from '@/api/resources'
import {
  getK3sCatalog, getK3sInstallation, listK3sInstallations, previewK3sPlan, startK3sInstallation,
  type K3sCatalog, type K3sInstallation, type K3sPlan, type K3sPlanInput, type K3sPreflightReport, type K3sPreflightCheck,
} from '@/api/k3s'
import K3sOptionFields from '@/components/K3sOptionFields.vue'
import K3sPreflightReportView from '@/components/K3sPreflightReport.vue'
import { useAuthStore } from '@/stores/auth'
import { availableK3sHosts, k3sAutomaticEndpoint, k3sHostDisabledReason, k3sHostStatusLabel, k3sInstallationLabel, k3sInstallationLogs, k3sInstallationRunning, validateK3sOverrides } from '@/utils/k3sProvision'
import { formatDateTime } from '@/utils/time'

const props = withDefaults(defineProps<{open:boolean;action?:'create'|'join';cluster?:string;endpoint?:string;hosts?:InfrastructureHost[]}>(), {
  action:'create',cluster:'',endpoint:'',
})
const emit = defineEmits<{
  'update:open':[value:boolean]
  created:[cluster:NonNullable<K3sInstallation['cluster']>]
  completed:[installation:K3sInstallation]
}>()
const auth = useAuthStore()
const router = useRouter()
const catalog = ref<K3sCatalog>()
const hosts = ref<InfrastructureHost[]>([])
const loading = ref(false),busy = ref(false),error = ref(''),loadError = ref(''),pollError = ref('')
const recent = ref<K3sInstallation[]>([]),recentError = ref(''),recentLoading = ref(false)
const plan = ref<K3sPlan>(),installation = ref<K3sInstallation>(),optionTab = ref('server'),now = ref(Date.now())
const preflight = ref<K3sPreflightReport>()
const form = reactive({
  name:'',server_host_id:'',agent_host_ids:[] as string[],profile:'default' as 'default'|'custom',endpoint:'',
  server_options:{} as Record<string,unknown>,agent_options:{} as Record<string,unknown>,installer:{} as Record<string,unknown>,overrides:'',
})
let mounted = true,revision = 0,request:AbortController|undefined,pollRequest:AbortController|undefined,recentRequest:AbortController|undefined
let pollTimer:ReturnType<typeof setTimeout>|undefined,clock:ReturnType<typeof setInterval>|undefined,completedID = '',operationContext = ''
const selectedProject = ref(readSelectedProject())
const context = computed(() => JSON.stringify([auth.user?.id??'',auth.user?.project_id??'',selectedProject.value,props.action,props.cluster]))
const hostChoices = computed(() => availableK3sHosts(hosts.value))
const hostOptions = computed(() => hostChoices.value.map(host=>({
  value:host.id,label:`${host.name} · ${host.address}${k3sHostDisabledReason(host)?`（${k3sHostDisabledReason(host)}）`:''}`,
  disabled:Boolean(k3sHostDisabledReason(host)),
})))
const agentOptions = computed(() => hostOptions.value.filter(option=>option.value!==form.server_host_id))
const selectedHosts = computed(() => hosts.value.filter(host=>host.id===form.server_host_id||form.agent_host_ids.includes(host.id)))
const hasHostConfig = computed(() => plan.value?.hosts?.some(host=>Boolean(host.config)))
const endpointPlaceholder = computed(() => k3sAutomaticEndpoint(hosts.value.find(host=>host.id===form.server_host_id),form.profile==='custom'?form.server_options:{},form.profile==='custom'?form.overrides:'')||'https://192.168.1.10:6443')
const canManageClusterResources = computed(() => auth.canAny(['kubernetes.node.read','kubernetes.workload.read','kubernetes.config.read','kubernetes.storage.read','kubernetes.network.read']))
const canExecute = computed(() => auth.canAny([props.action==='create'?'cluster.create':'kubernetes.node.create'])&&auth.canAny(['cluster.execute'])&&auth.canAny(['cluster.update'])&&auth.canAny(['cluster.read','deployment.read']))
const canPreview = computed(() => Boolean(catalog.value)&&!loadError.value&&canExecute.value&&!loading.value&&!busy.value&&(props.action==='create'?Boolean(form.name.trim()&&form.server_host_id):Boolean(props.cluster&&form.agent_host_ids.length)))
const running = computed(() => k3sInstallationRunning(installation.value))
const planExpired = computed(() => Boolean(plan.value)&&(!Number.isFinite(Date.parse(plan.value!.expires_at))||Date.parse(plan.value!.expires_at)<=now.value))
const visiblePreflight = computed(() => readPreflight(plan.value?.preflight)??preflight.value)
const planPreflightPassed = computed(() => {
  const report=readPreflight(plan.value?.preflight)
  return Boolean(report?.passed&&report.hosts.length&&report.hosts.every(host=>host.passed&&host.checks.length&&host.checks.every(check=>check.status!=='failed'))&&report.checks.every(check=>check.status!=='failed'))
})
const currentStep = computed(() => installation.value?2:plan.value?1:0)
const title = computed(() => props.action==='create'?'创建 K3s 集群':'添加 K3s 节点')
const installerOptions = computed(() => catalog.value?.installer.filter(option=>!['INSTALL_K3S_VERSION','INSTALL_K3S_CHANNEL'].includes(option.name))??[])
const defaultVersion = computed(() => catalog.value?.default_version||catalog.value?.target_version||catalog.value?.version||'')
const versionMismatch = computed(() => {
  const version=String(form.installer.version??'').trim()||defaultVersion.value
  return form.profile==='custom'&&version&&catalog.value&&version!==catalog.value.version
})
const signature = computed(() => JSON.stringify(input()))
const progressTone = computed(() => installation.value?.status==='succeeded'?'success':installation.value?.status==='failed'||installation.value?.status==='interrupted'?'error':'info')
const overridesPlaceholder = computed(() => `${props.action==='create'?'server:\n  disable:\n    - traefik\n':''}agent:\n  node-label:\n    - environment=production\ninstaller:\n  version: ${defaultVersion.value||'v1.37.1+k3s1'}`)

function input(): K3sPlanInput {
  const custom=form.profile==='custom'
  const installer=custom?Object.fromEntries(Object.entries(form.installer).filter(([key,value])=>!['version','channel'].includes(key)||String(value??'').trim())):{}
  return {
    action:props.action,...(props.action==='join'?{cluster_id:props.cluster}:{}),name:form.name.trim(),
    server_host_id:props.action==='create'?form.server_host_id:'',agent_host_ids:[...new Set(form.agent_host_ids)],
    profile:form.profile,server_options:custom?{...form.server_options}:{},agent_options:custom?{...form.agent_options}:{},
    installer,overrides:custom?form.overrides:'',endpoint:form.endpoint.trim(),
  }
}
function readPreflight(value:unknown):K3sPreflightReport|undefined {
  if(!value||typeof value!=='object')return
  const report=value as K3sPreflightReport
  const validCheck=(value:unknown) => {
    if(!value||typeof value!=='object')return false
    const check=value as K3sPreflightCheck
    return typeof check.id==='string'&&typeof check.name==='string'&&typeof check.message==='string'&&['passed','warning','failed'].includes(check.status)
  }
  if(typeof report.passed!=='boolean'||typeof report.checked_at!=='string'||!Number.isFinite(Date.parse(report.checked_at))||!Array.isArray(report.checks)||!report.checks.every(validCheck)||!Array.isArray(report.hosts))return
  if(!report.hosts.every(host=>host&&typeof host.host_id==='string'&&typeof host.host_name==='string'&&['server','agent'].includes(host.role)&&typeof host.hostname==='string'&&typeof host.passed==='boolean'&&Array.isArray(host.checks)&&host.checks.every(validCheck)))return
  return report
}
function failurePreflight(error:unknown) {
  return readPreflight((error as {response?:{data?:{preflight?:unknown}}}|undefined)?.response?.data?.preflight)
}
function readSelectedProject() {return typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')??''}
function syncProject() {
  const project=readSelectedProject()
  if(project===selectedProject.value)return true
  selectedProject.value=project;return false
}
function matchesContext(expected:string) {return syncProject()&&expected===context.value}
function projectChanged(event:Event) {
  if(event.type==='storage'&&(event as StorageEvent).key!==null&&(event as StorageEvent).key!=='edo.project')return
  syncProject()
}
function stopPoll() {
  if(pollTimer)clearTimeout(pollTimer)
  pollTimer=undefined;pollRequest?.abort();pollRequest=undefined
}
function cancelRequests() {revision++;request?.abort();request=undefined;recentRequest?.abort();recentRequest=undefined;stopPoll()}
function resetForm() {
  Object.assign(form,{name:'',server_host_id:'',agent_host_ids:[],profile:'default',endpoint:props.action==='join'?props.endpoint:'',server_options:{},agent_options:{},installer:{},overrides:''})
  optionTab.value=props.action==='join'?'agent':'server';plan.value=undefined;preflight.value=undefined;installation.value=undefined;completedID='';operationContext=context.value
  error.value='';pollError.value=''
}
async function initialize() {
  if(!syncProject()||!canExecute.value)return
  cancelRequests();const current=revision,currentContext=context.value,controller=new AbortController();request=controller
  loading.value=true;loadError.value=''
  void loadRecent()
  try {
    const [options,available]=await Promise.all([getK3sCatalog(props.action==='join'?props.cluster:'',controller.signal),listHosts()])
    if(!mounted||!props.open||current!==revision||!matchesContext(currentContext))return
    catalog.value=options;hosts.value=available
    if(installation.value&&operationContext===context.value)void refreshInstallation()
  } catch(err) {
    if(mounted&&props.open&&current===revision&&!controller.signal.aborted&&matchesContext(currentContext))loadError.value=apiErrorMessage(err)
  } finally {if(mounted&&current===revision)loading.value=false}
}
async function loadRecent() {
  if(!syncProject()||!props.open||!canExecute.value)return
  recentRequest?.abort();const controller=new AbortController();recentRequest=controller;const current=revision,currentContext=context.value
  recentLoading.value=true;recentError.value=''
  try {
    const result=await listK3sInstallations(props.action==='join'?props.cluster:'',props.action,controller.signal)
    if(mounted&&props.open&&current===revision&&matchesContext(currentContext))recent.value=result.filter(item=>!item.action||item.action===props.action)
  } catch(err) {if(mounted&&props.open&&current===revision&&!controller.signal.aborted&&matchesContext(currentContext))recentError.value=apiErrorMessage(err)}
  finally {if(mounted&&current===revision)recentLoading.value=false}
}
function viewInstallation(item:K3sInstallation) {
  if(busy.value)return
  stopPoll();installation.value=item;plan.value=undefined;preflight.value=undefined;pollError.value='';operationContext=context.value
  completedID=['succeeded','failed','interrupted'].includes(item.status)?item.id:''
  void refreshInstallation()
}
async function preview() {
  if(!syncProject()||!props.open||!canPreview.value||running.value)return
  error.value='';preflight.value=undefined;plan.value=undefined
  try {if(form.profile==='custom')validateK3sOverrides(form.overrides)} catch(err) {error.value=err instanceof Error?err.message:'覆盖配置无效';return}
  const payload=input(),currentSignature=signature.value,currentContext=context.value,current=revision,controller=new AbortController();request?.abort();request=controller;busy.value=true
  try {
    const result=await previewK3sPlan(payload,controller.signal)
    if(mounted&&props.open&&current===revision&&signature.value===currentSignature&&matchesContext(currentContext)){plan.value=result;now.value=Date.now()}
  } catch(err) {if(mounted&&props.open&&current===revision&&signature.value===currentSignature&&!controller.signal.aborted&&matchesContext(currentContext)){preflight.value=failurePreflight(err);error.value=apiErrorMessage(err)}}
  finally {if(mounted&&current===revision)busy.value=false}
}
async function execute() {
  if(!syncProject()||!props.open||!plan.value||planExpired.value||!planPreflightPassed.value||busy.value||!canExecute.value)return
  const planID=plan.value.id,currentContext=context.value,current=revision,controller=new AbortController();request?.abort();request=controller;busy.value=true;error.value=''
  try {
    const result=await startK3sInstallation(planID,controller.signal)
    if(!mounted||!props.open||current!==revision||!matchesContext(currentContext))return
    installation.value=result;operationContext=context.value
    handleProgress(result)
  } catch(err) {if(mounted&&props.open&&current===revision&&!controller.signal.aborted&&matchesContext(currentContext)){const report=failurePreflight(err);if(report){preflight.value=report;plan.value=undefined}error.value=apiErrorMessage(err);void loadRecent()}}
  finally {if(mounted&&current===revision)busy.value=false}
}
function handleProgress(result:K3sInstallation) {
  if(['succeeded','failed','interrupted'].includes(result.status)&&completedID!==result.id){
    completedID=result.id
    if(result.status==='succeeded'&&props.action==='create'&&result.cluster)emit('created',result.cluster)
    emit('completed',result);void loadRecent()
  }
  if(k3sInstallationRunning(result)&&props.open&&mounted)pollTimer=setTimeout(()=>void refreshInstallation(),1500)
}
async function refreshInstallation() {
  const id=installation.value?.id
  if(!syncProject()||!props.open||!id||!mounted)return
  stopPoll();const current=revision,currentContext=context.value,controller=new AbortController();pollRequest=controller
  try {
    const result=await getK3sInstallation(id,controller.signal)
    if(!mounted||!props.open||current!==revision||installation.value?.id!==id||!matchesContext(currentContext))return
    installation.value=result;pollError.value='';handleProgress(result)
  } catch(err) {
    if(mounted&&props.open&&current===revision&&!controller.signal.aborted&&matchesContext(currentContext))pollError.value=apiErrorMessage(err)
  }
}
function configureAgain() {
  if(running.value||busy.value)return
  plan.value=undefined;preflight.value=undefined;installation.value=undefined;completedID='';error.value='';pollError.value=''
}
function close() {if(!busy.value)emit('update:open',false)}
function manageCreatedCluster() {
  const id=installation.value?.cluster?.id??installation.value?.cluster_id
  if(!id)return
  void router.push(canManageClusterResources.value?{path:'/kubernetes',query:{cluster:id,view:'overview'}}:{path:'/hosts',query:{cluster:id,view:'clusters'}})
  close()
}
watch(signature,()=>{if(!installation.value){plan.value=undefined;preflight.value=undefined;error.value=''}})
watch(()=>form.server_host_id,id=>{
  form.agent_host_ids=form.agent_host_ids.filter(value=>value!==id)
})
watch(()=>props.open,open=>{
  if(!open){cancelRequests();busy.value=false;loading.value=false;return}
  if(operationContext!==context.value||!installation.value)resetForm()
  void initialize()
})
watch(context,()=>{cancelRequests();busy.value=false;loading.value=false;recent.value=[];recentError.value='';recentLoading.value=false;catalog.value=undefined;hosts.value=[];loadError.value='';resetForm();if(props.open)void initialize()})
watch(canExecute,allowed=>{if(!allowed){cancelRequests();busy.value=false;loading.value=false;loadError.value='';recentError.value='';recentLoading.value=false;plan.value=undefined;preflight.value=undefined;installation.value=undefined;recent.value=[];catalog.value=undefined;hosts.value=[]}else if(props.open)void initialize()})
onMounted(()=>{window.addEventListener('storage',projectChanged);window.addEventListener('edo:projects-changed',projectChanged);clock=setInterval(()=>{if(props.open)now.value=Date.now()},1000);if(props.open){resetForm();void initialize()}})
onBeforeUnmount(()=>{mounted=false;cancelRequests();window.removeEventListener('storage',projectChanged);window.removeEventListener('edo:projects-changed',projectChanged);if(clock)clearInterval(clock)})
</script>

<template>
  <a-drawer :open="open" :title="title" width="min(1060px,96vw)" :closable="!busy" :mask-closable="!busy" @close="close">
    <div class="provision-content">
      <a-steps :current="currentStep" size="small" :items="[{title:'安装配置'},{title:'预检与确认计划'},{title:'执行结果'}]" />
      <a-alert v-if="!canExecute" type="warning" show-icon message="需要主机读取、集群执行和关联权限，以及对应的创建权限。" />
      <a-alert v-if="loadError" type="error" show-icon message="安装配置读取失败" :description="loadError"><template #action><a-button size="small" @click="initialize">重试</a-button></template></a-alert>
      <a-alert v-if="error" type="error" show-icon :message="error" />
      <K3sPreflightReportView v-if="visiblePreflight&&!installation" :report="visiblePreflight" />
      <section v-if="!installation&&(recent.length||recentError||recentLoading)" class="recent-operations"><details><summary>最近安装任务 <span v-if="recent.length">{{ recent.length }}</span><a-button type="link" size="small" :loading="recentLoading" @click.stop.prevent="loadRecent">刷新</a-button></summary><a-alert v-if="recentError" type="warning" message="最近任务读取失败" :description="recentError"><template #action><a-button size="small" @click="loadRecent">重试</a-button></template></a-alert><div v-for="item in recent" :key="item.id" class="recent-operation"><div><strong>{{ item.name||item.id }}</strong><small>{{ item.created_at?formatDateTime(item.created_at,'zh-CN'):item.id }}</small></div><a-tag>{{ k3sInstallationLabel(item.status) }}</a-tag><a-button size="small" :disabled="busy" @click="viewInstallation(item)">查看进度</a-button></div></details></section>
      <a-spin :spinning="loading">
        <a-form v-if="!plan&&!installation" layout="vertical" :disabled="busy||!canExecute" class="provision-form">
          <div class="form-columns">
            <a-form-item v-if="action==='create'" label="集群名称" required><a-input v-model:value="form.name" :maxlength="64" placeholder="例如：生产集群" /></a-form-item>
            <a-form-item :label="action==='create'?'API 地址（可选）':'API 地址'" :required="action==='join'" :class="{'wide':action==='join'}"><a-input v-model:value="form.endpoint" :placeholder="endpointPlaceholder" /><div class="field-help">{{ action==='create'?'留空使用控制平面主机地址和最终监听端口；可填写负载均衡或其他访问地址。':'EDO 和新节点都需要能访问此地址。' }}</div></a-form-item>
            <a-form-item v-if="action==='create'" label="控制平面主机" required><a-select v-model:value="form.server_host_id" show-search option-filter-prop="label" :options="hostOptions" placeholder="选择安装 K3s Server 的主机" /></a-form-item>
            <a-form-item :label="action==='create'?'工作节点（可选）':'工作节点主机'" :required="action==='join'"><a-select v-model:value="form.agent_host_ids" mode="multiple" show-search option-filter-prop="label" :options="agentOptions" placeholder="选择安装 K3s Agent 的主机" /></a-form-item>
          </div>
          <a-alert v-if="!loading&&!loadError&&!hostChoices.length" type="info" show-icon message="暂无可安装的远程主机" description="请先在主机页面配置启用的 SSH 主机和登录凭据。" />
          <a-form-item label="安装方式"><a-radio-group v-model:value="form.profile" button-style="solid"><a-radio-button value="default">默认安装</a-radio-button><a-radio-button value="custom">自定义安装</a-radio-button></a-radio-group></a-form-item>
          <div v-if="form.profile==='default'" class="default-install"><Check :size="17" /><div><strong>使用 K3s 默认配置</strong><p>{{ action==='create'?'安装控制平面及所选工作节点，完成后自动接入集群并关联主机。':'在所选主机安装工作节点，加入当前集群并关联主机。' }}</p><small v-if="catalog">安装版本：{{ defaultVersion }}{{ action==='join'?'（跟随目标集群）':'' }}</small></div></div>
          <template v-else-if="catalog">
            <div class="catalog-caption"><strong>K3s 安装选项</strong><span>参数基于 {{ catalog.version }} · {{ catalog.catalog_version }}</span></div>
            <div class="form-columns"><a-form-item label="K3s 版本"><a-input v-model:value="form.installer.version" :placeholder="defaultVersion" /><div class="field-help">{{ action==='join'?`留空跟随目标集群 ${defaultVersion}；自定义版本需符合目标集群的版本偏差限制。`:`留空使用 ${defaultVersion}。` }}</div></a-form-item><a-form-item label="发布通道"><a-input v-model:value="form.installer.channel" placeholder="stable" /><div v-if="action==='join'" class="field-help">加入节点必须固定精确版本，不能仅填写通道；使用通道时请同时指定版本。</div></a-form-item></div>
            <a-alert v-if="versionMismatch" type="warning" show-icon message="安装版本与参数目录版本不同，请核对选项兼容性。" />
            <a-tabs v-model:active-key="optionTab" class="option-tabs">
              <a-tab-pane v-if="action==='create'" key="server" :tab="`控制平面（${catalog.server.length}）`"><K3sOptionFields v-model:value="form.server_options" :options="catalog.server" :disabled="busy" /></a-tab-pane>
              <a-tab-pane key="agent" :tab="`工作节点（${catalog.agent.length}）`"><K3sOptionFields v-model:value="form.agent_options" :options="catalog.agent" :disabled="busy" /></a-tab-pane>
              <a-tab-pane key="installer" :tab="`安装器（${catalog.installer.length}）`"><K3sOptionFields v-model:value="form.installer" :options="installerOptions" :disabled="busy" /></a-tab-pane>
            </a-tabs>
            <a-form-item label="覆盖配置（YAML）" class="override-field"><a-textarea v-model:value="form.overrides" :rows="8" spellcheck="false" :placeholder="overridesPlaceholder" /><div class="field-help">优先级：K3s 默认值 → 上方选项 → 此处覆盖。使用 server、agent、installer 分组；false 和空列表会覆盖原值。最终配置在下一步显示。</div></a-form-item>
          </template>
          <div class="install-note">下一步先只读检查所选控制平面和工作节点的环境、资源、已有安装与连接，再生成安装计划。检查不会安装软件或修改主机；开始安装前会实时复查。</div>
        </a-form>

        <div v-else-if="plan&&!installation" class="plan-review">
          <a-alert type="info" show-icon message="请核对目标主机和最终配置" description="点击开始安装后，将通过保存的 SSH 连接执行计划。" />
          <a-alert v-if="planExpired" type="warning" show-icon message="计划已过期，请返回配置后重新预览。" />
          <a-alert v-if="!planPreflightPassed" type="error" show-icon message="缺少通过的前置检查，请返回配置后重新检查。" />
          <dl class="plan-facts"><div v-if="action==='create'"><dt>集群名称</dt><dd>{{ plan.name||form.name }}</dd></div><div v-if="plan.version"><dt>安装版本</dt><dd><code>{{ plan.version }}</code></dd></div><div v-if="plan.target_version"><dt>目标集群版本</dt><dd><code>{{ plan.target_version }}</code></dd></div><div><dt>API 地址</dt><dd><code>{{ plan.endpoint||form.endpoint }}</code></dd></div><div><dt>计划有效期</dt><dd>{{ formatDateTime(plan.expires_at,'zh-CN') }}</dd></div></dl>
          <div class="plan-hosts"><div v-for="host in plan.hosts||selectedHosts" :key="host.id"><Server :size="16" /><strong>{{ host.name }}</strong><code>{{ host.address }}</code><a-tag>{{ 'role' in host&&host.role==='server'?'控制平面':'工作节点' }}</a-tag></div></div>
          <a-alert v-for="warning in plan.warnings" :key="warning" type="warning" show-icon :message="warning" />
          <section v-if="hasHostConfig" class="host-configs"><h4>最终主机配置</h4><details v-for="host in plan.hosts?.filter(item=>item.config)" :key="host.id" open><summary>{{ host.name }} · {{ host.role==='server'?'控制平面':'工作节点' }}</summary><pre>{{ host.config }}</pre></details></section>
          <section v-if="plan.server_config&&!hasHostConfig"><h4>控制平面最终配置</h4><pre>{{ plan.server_config }}</pre></section>
          <section v-if="plan.agent_config&&!hasHostConfig"><h4>工作节点最终配置</h4><pre>{{ plan.agent_config }}</pre></section>
          <section v-if="plan.installer_config"><h4>安装器配置</h4><pre>{{ plan.installer_config }}</pre></section>
          <section v-if="plan.commands?.length"><h4>执行命令</h4><pre v-for="(command,index) in plan.commands" :key="index">{{ command }}</pre></section>
        </div>

        <div v-else-if="installation" class="installation-result">
          <a-alert :type="progressTone" show-icon :message="k3sInstallationLabel(installation.status)" :description="installation.error||installation.message||installation.phase||(running?'可以关闭窗口，安装会继续执行。':'')" />
          <a-alert v-if="installation.status==='failed'&&installation.cluster" type="warning" show-icon :message="action==='create'?'集群已创建，部分安装步骤未完成':'部分节点未完成加入'" description="已完成的节点会保留。请进入集群检查状态，再从添加节点中选择剩余主机。" />
          <a-button v-if="!running&&installation.cluster" class="manage-created-cluster" @click="manageCreatedCluster">{{ canManageClusterResources?'管理已创建集群':'查看已创建集群' }}</a-button>
          <p class="operation-id">任务编号：{{ installation.id }}</p>
          <a-alert v-if="pollError" type="warning" show-icon message="进度读取失败，安装任务可能仍在运行" :description="pollError"><template #action><a-button size="small" @click="refreshInstallation"><RefreshCw :size="13" />重新读取</a-button></template></a-alert>
          <div class="operation-hosts"><div v-for="host in installation.hosts" :key="host.id"><Server :size="16" /><span><strong>{{ host.name }}</strong><small>{{ host.address }}</small></span><a-tag :title="host.status">{{ k3sHostStatusLabel(host.status) }}</a-tag><p v-if="host.message">{{ host.message }}</p></div></div>
          <section><h4>安装日志</h4><pre class="installation-logs" aria-live="polite">{{ k3sInstallationLogs(installation)||'等待安装日志…' }}</pre></section>
        </div>
      </a-spin>
    </div>
    <template #footer><div class="provision-footer"><a-button :disabled="busy" @click="close">{{ running?'关闭窗口':'关闭' }}</a-button><a-button v-if="plan&&!installation" :disabled="busy" @click="plan=undefined;preflight=undefined"><ChevronLeft :size="14" />返回配置</a-button><a-button v-if="installation&&!running" :disabled="busy" @click="configureAgain">{{ installation.status==='succeeded'?'继续添加':'重新配置' }}</a-button><a-button v-if="!plan&&!installation" type="primary" :loading="busy" :disabled="!canPreview" @click="preview">检查并预览安装计划</a-button><a-button v-else-if="plan&&!installation" type="primary" :loading="busy" :disabled="planExpired||!canExecute||!planPreflightPassed" @click="execute">开始安装</a-button></div></template>
  </a-drawer>
</template>

<style scoped>
.host-configs details+details{margin-top:12px}.host-configs summary{padding:9px 0;cursor:pointer;font-size:12px}.recent-operations summary>.ant-btn{float:right;margin-top:-4px}
.recent-operations{border:1px solid var(--edo-border);border-radius:6px}.recent-operations summary{cursor:pointer;padding:11px 14px;font-size:12px;background:var(--edo-bg)}.recent-operations summary>span{margin-left:6px;color:var(--edo-muted)}.recent-operation{display:flex;align-items:center;gap:12px;padding:12px 14px;border-top:1px solid var(--edo-border)}.recent-operation>div{flex:1;min-width:0}.recent-operation strong,.recent-operation small{display:block;overflow-wrap:anywhere}.recent-operation strong{font-size:12px;font-weight:500}.recent-operation small{margin-top:4px;font-size:11px;color:var(--edo-muted)}
.provision-content{display:grid;gap:22px}.provision-form{margin-top:6px}.form-columns{display:grid;grid-template-columns:1fr 1fr;gap:0 16px}.wide{grid-column:1/-1}.field-help{margin-top:6px;font-size:12px;line-height:1.65;color:var(--edo-muted)}.default-install{display:flex;gap:12px;padding:18px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg)}.default-install>svg{color:#1d9459;flex:none;margin-top:3px}.default-install strong{font-size:13px;font-weight:600}.default-install p{margin:5px 0;font-size:12px;line-height:1.7;color:var(--edo-muted)}.default-install small{color:var(--edo-muted);font-size:11px}.install-note{padding-top:18px;font-size:12px;color:var(--edo-muted);line-height:1.7}.catalog-caption{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:14px}.catalog-caption strong{font-size:14px;font-weight:600}.catalog-caption span{font-size:11px;color:var(--edo-muted)}.option-tabs{margin:8px 0 20px}.override-field{margin-top:22px}.override-field :deep(textarea){font-family:Consolas,monospace;font-size:12px}.provision-footer{display:flex;align-items:center;justify-content:flex-end;gap:8px}.plan-review,.installation-result{display:grid;gap:16px}.plan-facts{margin:0;display:grid;grid-template-columns:1fr 1fr;gap:14px}.plan-facts div{min-width:0}.plan-facts dt{color:var(--edo-muted);font-size:12px;margin-bottom:5px}.plan-facts dd{margin:0;font-size:13px;overflow-wrap:anywhere}.plan-hosts{border:1px solid var(--edo-border);border-radius:6px}.plan-hosts>div{display:flex;align-items:center;gap:10px;padding:12px;border-bottom:1px solid var(--edo-border);flex-wrap:wrap;font-size:12px}.plan-hosts>div:last-child{border:0}.plan-hosts strong{font-weight:500}.plan-hosts code{color:var(--edo-muted)}.plan-hosts .ant-tag{margin-left:auto}.plan-review h4,.installation-result h4{margin:0 0 8px;font-size:13px;font-weight:600}.plan-review pre,.installation-logs{margin:0;padding:14px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg);font-size:12px;line-height:1.7;white-space:pre-wrap;overflow-wrap:anywhere;max-height:360px;overflow:auto}.plan-review pre+pre{margin-top:10px}.operation-id{font-size:11px;color:var(--edo-muted);margin:0;overflow-wrap:anywhere}.operation-hosts>div{display:flex;align-items:center;gap:10px;padding:12px 0;flex-wrap:wrap;border-bottom:1px solid var(--edo-border)}.operation-hosts span{flex:1}.operation-hosts strong,.operation-hosts small{display:block}.operation-hosts strong{font-size:13px;font-weight:500}.operation-hosts small{font-size:11px;color:var(--edo-muted);margin-top:3px}.operation-hosts p{flex-basis:100%;margin:0 0 0 26px;font-size:12px;color:var(--edo-muted)}@media(max-width:650px){.form-columns,.plan-facts{grid-template-columns:1fr}.catalog-caption{align-items:flex-start;flex-direction:column}.provision-footer{flex-wrap:wrap}}
</style>
