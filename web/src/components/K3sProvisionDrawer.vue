<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Check, ChevronLeft, RefreshCw, Server } from 'lucide-vue-next'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { listHosts, type InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage } from '@/api/resources'
import {
  getK3sCatalog, getK3sInstallation, listK3sInstallations, checkK3sPreflight, previewK3sPlan, startK3sInstallation,
  type K3sCatalog, type K3sInstallation, type K3sPlan, type K3sPlanInput, type K3sPreflightReport, type K3sPreflightCheck, type K3sJoinRole, type K3sInstallMode, type K3dConfig, type K3sDownloadSource,
} from '@/api/k3s'
import K3sOptionFields from '@/components/K3sOptionFields.vue'
import K3sPreflightReportView from '@/components/K3sPreflightReport.vue'
import K3sInstallationProgress from '@/components/K3sInstallationProgress.vue'
import { useAuthStore } from '@/stores/auth'
import { availableK3sHosts, k3sAutomaticEndpoint, k3sHostDisabledReason, k3sHostRoleLabel, k3sInstallationLabel, k3sInstallationRunning, k3sMasterRawArgumentOptions, k3sMasterRawArgumentReason, validateK3sOverrides } from '@/utils/k3sProvision'
import { formatDateTime } from '@/utils/time'
import { defaultK3dBindAddress, validK3dBindAddress } from '@/utils/k3dProvision'

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
const { t } = useI18n()
const catalog = ref<K3sCatalog>()
const hosts = ref<InfrastructureHost[]>([])
const loading = ref(false),busy = ref(false),error = ref(''),loadError = ref(''),pollError = ref('')
const recent = ref<K3sInstallation[]>([]),recentError = ref(''),recentLoading = ref(false)
const plan = ref<K3sPlan>(),installation = ref<K3sInstallation>(),optionTab = ref('server'),now = ref(Date.now())
const preflight = ref<K3sPreflightReport>()
const checking=ref(false),checkedInput=ref(''),checkedContext=ref('')
const preflightView=ref<{focus:()=>void}>(),errorRegion=ref<HTMLElement>()
const emptyForm = () => ({
  mode:'native' as K3sInstallMode,k3d:{agents:1,api_port:16443,bind_address:''} as K3dConfig,
  download_source:'official' as K3sDownloadSource,system_image_source:'official' as 'official'|'community_cn'|'custom',custom_system_image_registry:'',
  name:'',server_host_id:'',agent_host_ids:[] as string[],profile:'default' as 'default'|'custom',endpoint:'',
  join_role:'agent' as K3sJoinRole,join_host_ids:[] as string[],server_token:'',
  server_options:{} as Record<string,unknown>,agent_options:{} as Record<string,unknown>,installer:{} as Record<string,unknown>,overrides:'',
})
const form = reactive(emptyForm())
let modeDrafts: Partial<Record<K3sInstallMode,ReturnType<typeof emptyForm>>> = {},restoringMode=false
let mounted = true,revision = 0,request:AbortController|undefined,pollRequest:AbortController|undefined,recentRequest:AbortController|undefined
let pollTimer:ReturnType<typeof setTimeout>|undefined,clock:ReturnType<typeof setInterval>|undefined,completedID = '',operationContext = ''
let pollFailures=0
const selectedProject = ref(readSelectedProject())
const context = computed(() => JSON.stringify([auth.user?.id??'',auth.user?.project_id??'',selectedProject.value,props.action,props.cluster]))
const hostChoices = computed(() => availableK3sHosts(hosts.value))
const hostOptions = computed(() => hostChoices.value.map(host=>({
  value:host.id,label:`${host.name} · ${host.address}${k3sHostDisabledReason(host)?`（${k3sHostDisabledReason(host)}）`:''}`,
  disabled:Boolean(k3sHostDisabledReason(host)),
})))
const agentOptions = computed(() => hostOptions.value.filter(option=>option.value!==form.server_host_id))
const isK3d = computed(() => props.action==='create'&&form.mode==='k3d')
const displayMode = computed(() => installation.value?modeOf(installation.value):plan.value?modeOf(plan.value):isK3d.value?'k3d':'native')
const k3dConfig = computed(() => installation.value?installation.value.k3d:plan.value?.k3d)
const systemImageRegistry = computed(() => form.system_image_source==='community_cn'?'registry.cn-hangzhou.aliyuncs.com':form.system_image_source==='custom'?form.custom_system_image_registry.trim():'')
const sourceInputValid = computed(() => isK3d.value||(['official','official_cn'].includes(form.download_source)&&(props.action==='join'||form.system_image_source!=='custom'||Boolean(systemImageRegistry.value))))
const frozenSources = computed(() => installation.value?installation.value.sources:plan.value?.sources)
const sourceFacts = computed(() => {
  const sources=frozenSources.value
  return [
    {label:t('k8s.installDownloadSource'),value:sources?.download_source==='official'?t('k8s.downloadOfficial'):sources?.download_source==='official_cn'?t('k8s.downloadOfficialCn'):t('k8s.sourceNotRecorded')},
    {label:t('k8s.installerSourceURL'),value:sources?.installer_url||t('k8s.sourceNotRecorded')},
    {label:t('k8s.artifactSourceURL'),value:sources?.artifact_url||t('k8s.sourceNotRecorded')},
    {label:t('k8s.systemImageSource'),value:typeof sources?.system_image_registry==='string'?(sources.system_image_registry||t('k8s.systemImageOfficial')):t(props.action==='join'?'k8s.systemImageInheritedUnrecorded':'k8s.sourceNotRecorded')},
    ...(sources?.mirror?[{label:t('k8s.installerMirror'),value:sources.mirror}]:[]),
  ]
})
const k3dEndpoint = computed(() => {const address=form.k3d.bind_address.trim();return validK3dBindAddress(address)?`https://${address}:${form.k3d.api_port}`:''})
const joinMaster = computed(() => props.action==='join'&&form.join_role==='server')
const joinHostSelection = computed({
  get:()=>joinMaster.value?form.join_host_ids[0]:form.join_host_ids,
  set:(value:string|string[]|undefined)=>{form.join_host_ids=Array.isArray(value)?value:value?[value]:[]},
})
const showServerOptions = computed(() => !isK3d.value&&(props.action==='create'||joinMaster.value))
const showAgentOptions = computed(() => !isK3d.value&&(props.action==='create'||!joinMaster.value))
const selectedHosts = computed(() => hosts.value.filter(host=>props.action==='join'?form.join_host_ids.includes(host.id):host.id===form.server_host_id||!isK3d.value&&form.agent_host_ids.includes(host.id)))
const hasHostConfig = computed(() => plan.value?.hosts?.some(host=>Boolean(host.config)))
const endpointPlaceholder = computed(() => k3sAutomaticEndpoint(hosts.value.find(host=>host.id===form.server_host_id),form.profile==='custom'?form.server_options:{},form.profile==='custom'?form.overrides:'')||'https://192.168.1.10:6443')
const canManageClusterResources = computed(() => auth.canAny(['kubernetes.node.read','kubernetes.workload.read','kubernetes.config.read','kubernetes.storage.read','kubernetes.network.read']))
const canExecute = computed(() => auth.canAny([props.action==='create'?'cluster.create':'kubernetes.node.create'])&&auth.canAny(['cluster.execute'])&&auth.canAny(['cluster.update'])&&auth.canAny(['cluster.read','deployment.read']))
const canCheck = computed(() => Boolean(catalog.value)&&!loadError.value&&canExecute.value&&!loading.value&&!busy.value&&sourceInputValid.value&&(props.action==='create'?Boolean(form.name.trim()&&form.server_host_id&&(!isK3d.value||validK3dConfig(form.k3d))):Boolean(props.cluster&&form.join_host_ids.length&&form.join_host_ids.length<=(joinMaster.value?1:20)&&(!joinMaster.value||form.server_token.trim()))))
const canPreview = computed(() => canCheck.value&&reportPassed(preflight.value)&&checkedInput.value===signature.value&&checkedContext.value===context.value)
const running = computed(() => k3sInstallationRunning(installation.value))
const planExpired = computed(() => Boolean(plan.value)&&(!Number.isFinite(Date.parse(plan.value!.expires_at))||Date.parse(plan.value!.expires_at)<=now.value))
const visiblePreflight = computed(() => readPreflight(plan.value?.preflight)??preflight.value)
const planPreflightPassed = computed(() => {
  const report=readPreflight(plan.value?.preflight)
  return reportPassed(report)
})
const planModeValid = computed(() => !!plan.value&&modeOf(plan.value)===(isK3d.value?'k3d':'native')&&(modeOf(plan.value)!=='k3d'||k3dPlanMatches(plan.value)))
const planSourceValid = computed(() => !!plan.value&&(modeOf(plan.value)==='k3d'||nativeSourcesMatch(plan.value,input())))
const currentStep = computed(() => installation.value?3:plan.value?2:preflight.value||checking.value?1:0)
const title = computed(() => props.action==='create'?t('k8s.createClusterTitle'):'添加 K3s 节点')
const installerOptions = computed(() => catalog.value?.installer.filter(option=>!['INSTALL_K3S_VERSION','INSTALL_K3S_CHANNEL'].includes(option.name))??[])
const serverOptionChoices = computed(() => catalog.value?.server.map(option=>{
  if(props.action==='create'&&option.name==='system-default-registry')return {...option,managed:true,description:t('k8s.systemImageManaged')}
  if(!joinMaster.value)return option
  if(k3sMasterRawArgumentOptions.some(name=>name===option.name))return {...option,unsupported_reason:k3sMasterRawArgumentReason}
  if(['cluster-init','cluster-reset','cluster-reset-restore-path','disable-agent','disable-apiserver','disable-controller-manager','disable-scheduler','disable-etcd'].includes(option.name))return {...option,unsupported_reason:'加入已有控制平面时不允许初始化、重置或拆分控制平面角色。'}
  return option
})??[])
const defaultVersion = computed(() => catalog.value?.default_version||catalog.value?.target_version||catalog.value?.version||'')
const versionMismatch = computed(() => {
  const version=String(form.installer.version??'').trim()||defaultVersion.value
  return form.profile==='custom'&&version&&catalog.value&&version!==catalog.value.version
})
const signature = computed(() => JSON.stringify(input()))
const progressTone = computed(() => installation.value?.status==='succeeded'?'success':installation.value?.status==='failed'||installation.value?.status==='interrupted'?'error':'info')
const overridesPlaceholder = computed(() => `${props.action==='create'?'server:\n  disable:\n    - traefik\nagent:':joinMaster.value?'server:':'agent:'}\n  node-label:\n    - environment=production\ninstaller:\n  version: ${defaultVersion.value||'v1.37.1+k3s1'}`)

function input(): K3sPlanInput {
  if(isK3d.value)return {action:'create',mode:'k3d',name:form.name.trim(),server_host_id:form.server_host_id,profile:'default',endpoint:k3dEndpoint.value,k3d:{...form.k3d,bind_address:form.k3d.bind_address.trim()}}
  const custom=form.profile==='custom'
  const installer=custom?Object.fromEntries(Object.entries(form.installer).filter(([key,value])=>!['version','channel'].includes(key)||String(value??'').trim())):{}
  return {
    action:props.action,mode:'native',download_source:form.download_source,...(props.action==='join'?{cluster_id:props.cluster,join_role:form.join_role,join_host_ids:[...new Set(form.join_host_ids)],...(joinMaster.value?{server_token:form.server_token}:{})}:{server_host_id:form.server_host_id,agent_host_ids:[...new Set(form.agent_host_ids)],system_image_registry:systemImageRegistry.value}),name:form.name.trim(),
    profile:form.profile,server_options:custom&&showServerOptions.value?{...form.server_options}:{},agent_options:custom&&showAgentOptions.value?{...form.agent_options}:{},
    installer,overrides:custom?form.overrides:'',endpoint:form.endpoint.trim(),
  }
}
function modeOf(value:{mode?:K3sInstallMode}) {return value.mode==='k3d'?'k3d':'native'}
function nativeSourcesMatch(value:K3sPlan,payload:K3sPlanInput) {
  const sources=value.sources
  return !!sources&&sources.download_source===payload.download_source&&Boolean(sources.installer_url&&sources.artifact_url)&&(payload.action!=='create'||typeof sources.system_image_registry==='string'&&sources.system_image_registry===payload.system_image_registry)
}
function validK3dConfig(value?:K3dConfig) {return !!value&&Number.isInteger(value.agents)&&value.agents>=0&&value.agents<=8&&Number.isInteger(value.api_port)&&value.api_port>=1024&&value.api_port<=65535&&validK3dBindAddress(value.bind_address)}
function k3dPlanMatches(value:K3sPlan) {const config=value.k3d;return validK3dConfig(config)&&value.hosts?.length===1&&value.hosts[0]?.role==='host'&&value.hosts[0]?.id===form.server_host_id&&config?.agents===form.k3d.agents&&config?.api_port===form.k3d.api_port&&config?.bind_address===form.k3d.bind_address.trim()&&value.endpoint===k3dEndpoint.value}
function readPreflight(value:unknown):K3sPreflightReport|undefined {
  if(!value||typeof value!=='object')return
  const report=value as K3sPreflightReport
  const validCheck=(value:unknown) => {
    if(!value||typeof value!=='object')return false
    const check=value as K3sPreflightCheck
    return typeof check.id==='string'&&typeof check.name==='string'&&typeof check.message==='string'&&['passed','warning','failed'].includes(check.status)
  }
  if(typeof report.passed!=='boolean'||typeof report.checked_at!=='string'||!Number.isFinite(Date.parse(report.checked_at))||!Array.isArray(report.checks)||!report.checks.every(validCheck)||!Array.isArray(report.hosts))return
  if(!report.hosts.every(host=>host&&typeof host.host_id==='string'&&typeof host.host_name==='string'&&['server','agent','host'].includes(host.role)&&typeof host.hostname==='string'&&typeof host.passed==='boolean'&&Array.isArray(host.checks)&&host.checks.every(validCheck)))return
  return report
}
function failurePreflight(error:unknown) {
  return readPreflight((error as {response?:{data?:{preflight?:unknown}}}|undefined)?.response?.data?.preflight)
}
function reportPassed(report?:K3sPreflightReport) {
  return Boolean(report?.passed&&report.hosts.length&&report.hosts.every(host=>host.passed&&host.checks.length&&host.checks.every(check=>check.status!=='failed'))&&report.checks.every(check=>check.status!=='failed'))
}
function reportMatchesHosts(report:K3sPreflightReport,payload:K3sPlanInput) {
  const expected=payload.action==='join'?(payload.join_host_ids??[]).map(id=>({id,role:payload.join_role??'agent'})):[{id:payload.server_host_id,role:payload.mode==='k3d'?'host':'server'},...(payload.mode==='k3d'?[]:(payload.agent_host_ids??[]).map(id=>({id,role:'agent'})))]
  return report.hosts.length===expected.length&&new Set(report.hosts.map(host=>host.host_id)).size===expected.length&&expected.every(host=>report.hosts.some(actual=>actual.host_id===host.id&&actual.role===host.role))
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
  restoringMode=true;modeDrafts={};Object.assign(form,{...emptyForm(),endpoint:props.action==='join'?props.endpoint:''});restoringMode=false
  optionTab.value=props.action==='join'?'agent':'server';plan.value=undefined;preflight.value=undefined;installation.value=undefined;completedID='';operationContext=context.value
  error.value='';pollError.value=''
  checking.value=false;checkedInput.value='';checkedContext.value=''
  pollFailures=0
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
  if(busy.value||!props.open||!canExecute.value)return
  stopPoll();pollFailures=0;installation.value=item;form.server_token='';plan.value=undefined;preflight.value=undefined;pollError.value='';operationContext=context.value
  completedID=['succeeded','failed','interrupted'].includes(item.status)?item.id:''
  void refreshInstallation()
}
async function checkPreflight() {
  if(!syncProject()||!props.open||!canCheck.value||installation.value||plan.value)return
  error.value='';preflight.value=undefined;checkedInput.value='';checkedContext.value=''
  try {if(form.profile==='custom')validateK3sOverrides(form.overrides,props.action==='join'?form.join_role:undefined)} catch(err) {error.value=err instanceof Error?err.message:'覆盖配置无效';return}
  const payload=input(),currentSignature=signature.value,currentContext=context.value,current=revision,controller=new AbortController();request?.abort();request=controller;busy.value=true;checking.value=true
  try {
    const result=readPreflight(await checkK3sPreflight(payload,controller.signal))
    if(!mounted||!props.open||current!==revision||signature.value!==currentSignature||!matchesContext(currentContext))return
    if(!result||!reportMatchesHosts(result,payload)||result.hosts.some(host=>!host.checks.length)){error.value='安装前检测结果不完整，请重新检测。'}
    else{preflight.value=result;checkedInput.value=currentSignature;checkedContext.value=currentContext}
    await nextTick()
    if(mounted&&props.open&&current===revision&&matchesContext(currentContext)){if(preflight.value)preflightView.value?.focus();else errorRegion.value?.focus()}
  }catch(err){if(mounted&&props.open&&current===revision&&signature.value===currentSignature&&!controller.signal.aborted&&matchesContext(currentContext)){preflight.value=failurePreflight(err);error.value=apiErrorMessage(err);await nextTick();if(mounted&&props.open&&current===revision&&matchesContext(currentContext))errorRegion.value?.focus()}}
  finally{if(mounted&&current===revision){busy.value=false;checking.value=false}}
}
async function preview() {
  if(!syncProject()||!props.open||!canPreview.value||running.value)return
  error.value='';plan.value=undefined
  try {if(form.profile==='custom')validateK3sOverrides(form.overrides,props.action==='join'?form.join_role:undefined)} catch(err) {error.value=err instanceof Error?err.message:'覆盖配置无效';return}
  const payload=input(),currentSignature=signature.value,currentContext=context.value,current=revision,controller=new AbortController();request?.abort();request=controller;busy.value=true
  try {
    const result=await previewK3sPlan(payload,controller.signal)
    if(mounted&&props.open&&current===revision&&signature.value===currentSignature&&matchesContext(currentContext)){
      if(modeOf(result)!==payload.mode||payload.mode==='k3d'&&!k3dPlanMatches(result)){error.value=t('k8s.installModeMismatch');return}
      if(payload.mode==='native'&&!nativeSourcesMatch(result,payload)){error.value=t('k8s.installSourceMismatch');return}
      plan.value=result;now.value=Date.now()
    }
  } catch(err) {if(mounted&&props.open&&current===revision&&signature.value===currentSignature&&!controller.signal.aborted&&matchesContext(currentContext)){preflight.value=failurePreflight(err);checkedInput.value='';checkedContext.value='';error.value=apiErrorMessage(err)}}
  finally {if(mounted&&current===revision)busy.value=false}
}
async function execute() {
  if(!syncProject()||!props.open||!plan.value||planExpired.value||!planPreflightPassed.value||!planModeValid.value||!planSourceValid.value||busy.value||!canExecute.value)return
  const planID=plan.value.id,currentContext=context.value,current=revision,controller=new AbortController();request?.abort();request=controller;busy.value=true;error.value=''
  try {
    const result=await startK3sInstallation(planID,controller.signal)
    if(!mounted||!props.open||current!==revision||!matchesContext(currentContext))return
    installation.value=result;form.server_token='';operationContext=context.value
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
  if(!syncProject()||!props.open||!canExecute.value||!id||!mounted)return
  stopPoll();const current=revision,currentContext=context.value,controller=new AbortController();pollRequest=controller
  try {
    const result=await getK3sInstallation(id,controller.signal)
    if(!mounted||!props.open||current!==revision||installation.value?.id!==id||!matchesContext(currentContext))return
    installation.value=result;pollError.value='';pollFailures=0;handleProgress(result)
  } catch(err) {
    if(mounted&&props.open&&current===revision&&!controller.signal.aborted&&matchesContext(currentContext)){
      pollError.value=apiErrorMessage(err)
      if(running.value)pollTimer=setTimeout(()=>void refreshInstallation(),Math.min(30_000,5000*2**Math.min(pollFailures++,3)))
    }
  }
}
function configureAgain() {
  if(running.value||busy.value)return
  const mode=installation.value?modeOf(installation.value):form.mode
  plan.value=undefined;preflight.value=undefined;checkedInput.value='';checkedContext.value='';installation.value=undefined;completedID='';error.value='';pollError.value=''
  if(props.action==='create')form.mode=mode
}
function close() {if(!busy.value||checking.value)emit('update:open',false)}
function manageCreatedCluster() {
  const id=installation.value?.cluster?.id??installation.value?.cluster_id
  if(!id)return
  void router.push(canManageClusterResources.value?{path:'/kubernetes',query:{cluster:id,view:'overview'}}:{path:'/hosts',query:{cluster:id,view:'clusters'}})
  close()
}
watch(signature,()=>{if(!installation.value){if(busy.value){cancelRequests();busy.value=false;checking.value=false}plan.value=undefined;preflight.value=undefined;checkedInput.value='';checkedContext.value='';error.value=''}},{flush:'sync'})
watch(()=>form.mode,(mode,previous)=>{
  if(restoringMode||props.action!=='create')return
  modeDrafts[previous]={...JSON.parse(JSON.stringify(form)),mode:previous}
  cancelRequests();busy.value=false;loading.value=false;plan.value=undefined;preflight.value=undefined;error.value=''
  restoringMode=true;Object.assign(form,{...(modeDrafts[mode]??emptyForm()),mode});restoringMode=false
  if(props.open){if(!catalog.value)void initialize();else void loadRecent()}
},{flush:'sync'})
watch(()=>form.server_host_id,(id,previous)=>{
  form.agent_host_ids=form.agent_host_ids.filter(value=>value!==id)
  if(isK3d.value&&(!form.k3d.bind_address||form.k3d.bind_address===defaultK3dBindAddress(hosts.value.find(host=>host.id===previous))))form.k3d.bind_address=defaultK3dBindAddress(hosts.value.find(host=>host.id===id))
})
watch(()=>form.join_role,()=>{
  if(props.action!=='join'||installation.value)return
  cancelRequests();busy.value=false;loading.value=false
  Object.assign(form,{join_host_ids:[],server_token:'',server_options:{},agent_options:{},installer:{},overrides:''})
  optionTab.value=joinMaster.value?'server':'agent';plan.value=undefined;preflight.value=undefined;error.value=''
  if(props.open&&!catalog.value)void initialize()
})
watch(()=>props.open,open=>{
  if(!open){form.server_token='';cancelRequests();busy.value=false;checking.value=false;loading.value=false;if(!installation.value){preflight.value=undefined;checkedInput.value='';checkedContext.value=''}return}
  if(operationContext!==context.value||!installation.value)resetForm()
  void initialize()
})
watch(context,()=>{cancelRequests();busy.value=false;loading.value=false;recent.value=[];recentError.value='';recentLoading.value=false;catalog.value=undefined;hosts.value=[];loadError.value='';resetForm();if(props.open)void initialize()})
watch(canExecute,allowed=>{if(!allowed){form.server_token='';cancelRequests();busy.value=false;checking.value=false;checkedInput.value='';checkedContext.value='';loading.value=false;loadError.value='';recentError.value='';recentLoading.value=false;plan.value=undefined;preflight.value=undefined;installation.value=undefined;recent.value=[];catalog.value=undefined;hosts.value=[]}else if(props.open)void initialize()})
onMounted(()=>{window.addEventListener('storage',projectChanged);window.addEventListener('edo:projects-changed',projectChanged);clock=setInterval(()=>{if(props.open)now.value=Date.now()},1000);if(props.open){resetForm();void initialize()}})
onBeforeUnmount(()=>{mounted=false;form.server_token='';cancelRequests();window.removeEventListener('storage',projectChanged);window.removeEventListener('edo:projects-changed',projectChanged);if(clock)clearInterval(clock)})
</script>

<template>
  <a-drawer :open="open" :title="title" width="min(1060px,96vw)" :closable="!busy||checking" :mask-closable="!busy||checking" @close="close">
    <div class="provision-content">
      <a-steps :current="currentStep" size="small" :items="[{title:'安装配置'},{title:'安装前检测'},{title:'确认计划'},{title:'执行结果'}]" />
      <a-alert v-if="!installation&&!visiblePreflight&&!checking&&displayMode==='native'" type="info" show-icon :message="t('k8s.nativeInstallTitle')" :description="t('k8s.nativeInstallHint')" />
      <a-alert v-else-if="!installation&&!visiblePreflight&&!checking" type="info" show-icon :message="t('k8s.k3dInstallTitle')" :description="t('k8s.k3dInstallHint')" />
      <a-alert v-if="!canExecute" type="warning" show-icon message="需要主机读取、集群执行和关联权限，以及对应的创建权限。" />
      <a-alert v-if="loadError" type="error" show-icon message="安装配置读取失败" :description="loadError"><template #action><a-button size="small" @click="initialize">重试</a-button></template></a-alert>
      <div v-if="error" ref="errorRegion" tabindex="-1"><a-alert type="error" show-icon :message="error" /></div>
      <a-alert v-if="checking" type="info" show-icon message="正在进行安装前检测" description="只读检测所选主机，不会安装或自动修复。" role="status" />
      <K3sPreflightReportView v-if="visiblePreflight&&!installation" ref="preflightView" :report="visiblePreflight" :mode="displayMode" :standalone="!plan" />
      <section v-if="recent.length||recentError||recentLoading" class="recent-operations">
        <details :open="!installation">
          <summary>最近安装任务 <span v-if="recent.length">{{ recent.length }}</span></summary>
          <div class="recent-actions"><a-button class="recent-refresh" type="link" size="small" :loading="recentLoading" :disabled="recentLoading" @click="loadRecent">刷新</a-button></div>
          <a-alert v-if="recentError" type="warning" message="最近任务读取失败" :description="recentError"><template #action><a-button size="small" @click="loadRecent">重试</a-button></template></a-alert>
          <div v-for="item in recent" :key="item.id" class="recent-operation" :class="{'current-operation':installation?.id===item.id}">
            <div><strong>{{ item.name||item.id }}</strong><small>{{ item.created_at?formatDateTime(item.created_at,'zh-CN'):item.id }}</small></div>
            <a-tag>{{ t(modeOf(item)==='k3d'?'k8s.installModeK3d':'k8s.installModeNative') }}</a-tag><a-tag v-if="item.action==='join'&&item.hosts?.length">{{ k3sHostRoleLabel(item.hosts[0]?.role) }}</a-tag><a-tag>{{ k3sInstallationLabel(item.status) }}</a-tag>
            <a-button size="small" :disabled="busy" :aria-pressed="installation?.id===item.id" @click="viewInstallation(item)">查看进度</a-button>
          </div>
        </details>
      </section>
      <a-spin :spinning="loading">
        <a-form v-if="!plan&&!installation" layout="vertical" :disabled="busy||!canExecute" class="provision-form">
          <a-form-item v-if="action==='create'" :label="t('k8s.installMode')"><a-radio-group v-model:value="form.mode" :aria-label="t('k8s.installMode')" button-style="solid"><a-radio-button value="native">{{ t('k8s.installModeNative') }}</a-radio-button><a-radio-button value="k3d">{{ t('k8s.installModeK3d') }}</a-radio-button></a-radio-group></a-form-item>
          <div class="form-columns">
            <a-form-item v-if="action==='join'" label="新增节点角色" required class="wide"><a-radio-group v-model:value="form.join_role" button-style="solid" aria-label="新增节点角色"><a-radio-button value="agent">工作节点（Worker）</a-radio-button><a-radio-button value="server">控制平面（Master）</a-radio-button></a-radio-group></a-form-item><a-form-item v-if="action==='create'" label="集群名称" required><a-input v-model:value="form.name" :maxlength="64" placeholder="例如：生产集群" /></a-form-item>
            <a-form-item v-if="!isK3d" :label="action==='create'?'API 地址（可选）':'API 地址'" :required="action==='join'" :class="{'wide':action==='join'}"><a-input v-model:value="form.endpoint" :placeholder="endpointPlaceholder" /><div class="field-help">{{ action==='create'?'留空使用控制平面主机地址和最终监听端口；可填写负载均衡或其他访问地址。':'EDO 和新节点都需要能访问此地址。' }}</div></a-form-item>
            <a-form-item v-if="action==='create'" :label="isK3d?t('k8s.k3dHost'):'控制平面主机'" required><a-select v-model:value="form.server_host_id" :aria-label="isK3d?t('k8s.k3dHost'):'控制平面主机'" show-search option-filter-prop="label" :options="hostOptions" :placeholder="isK3d?t('k8s.k3dHostPlaceholder'):'选择安装 K3s Server 的主机'" /></a-form-item>
            <a-form-item v-if="action==='create'&&!isK3d" label="工作节点（可选）"><a-select v-model:value="form.agent_host_ids" mode="multiple" show-search option-filter-prop="label" :options="agentOptions" placeholder="选择安装 K3s Agent 的主机" /></a-form-item><a-form-item v-else-if="action==='join'" :label="joinMaster?'控制平面主机（Master）':'工作节点主机（Worker）'" required><a-select v-model:value="joinHostSelection" :mode="joinMaster?undefined:'multiple'" show-search option-filter-prop="label" :options="hostOptions" :placeholder="joinMaster?'选择加入控制平面的新主机':'选择加入工作节点的新主机'" /><div class="field-help">{{ joinMaster?'Master 每个计划仅加入 1 台，须等待当前节点就绪后逐台添加。':'Worker 每次最多选择 20 台新主机。' }}</div></a-form-item><a-form-item v-if="joinMaster" label="目标集群 Server Token" required class="wide"><a-input-password v-model:value="form.server_token" aria-label="目标集群 Server Token" :visibility-toggle="false" :maxlength="4096" autocomplete="off" placeholder="输入目标集群的 Server Token" /><div class="field-help">仅用于本次控制平面加入。切换角色或关闭窗口后清除，不用于工作节点加入。</div></a-form-item>
            <template v-if="isK3d"><a-form-item :label="t('k8s.k3dAgents')" required><a-input-number v-model:value="form.k3d.agents" :aria-label="t('k8s.k3dAgents')" :min="0" :max="8" :precision="0" /></a-form-item><a-form-item :label="t('k8s.k3dApiPort')" required><a-input-number v-model:value="form.k3d.api_port" :aria-label="t('k8s.k3dApiPort')" :min="1024" :max="65535" :precision="0" /></a-form-item><a-form-item :label="t('k8s.k3dBindAddress')" required class="wide"><a-input v-model:value="form.k3d.bind_address" :aria-label="t('k8s.k3dBindAddress')" :placeholder="t('k8s.k3dBindPlaceholder')" /><div class="field-help">{{ t('k8s.k3dBindHint') }}</div></a-form-item><a-form-item :label="t('k8s.k3dEndpoint')" class="wide"><a-input :value="k3dEndpoint" readonly :aria-label="t('k8s.k3dEndpoint')" /></a-form-item></template>
          </div>
          <a-alert v-if="joinMaster" type="warning" show-icon message="目标集群需已启用内嵌 etcd" description="预检会实时核对 Server Token、目标控制平面、etcd 成员与关键配置。SQLite、外置数据存储或无法确认的目标会被阻断；不会自动转换数据库。关键配置自动继承，冲突覆盖会被拒绝。" /><a-alert v-if="!loading&&!loadError&&!hostChoices.length" type="info" show-icon message="暂无可安装的远程主机" description="请先在主机页面配置启用的 SSH 主机和登录凭据。" />
          <a-alert v-if="isK3d" type="warning" show-icon :message="t('k8s.k3dSingleHostWarning')" />
          <template v-else>
          <div class="form-columns source-options">
            <a-form-item :label="t('k8s.installDownloadSource')" required><a-select v-model:value="form.download_source" :aria-label="t('k8s.installDownloadSource')" :options="[{value:'official',label:t('k8s.downloadOfficial')},{value:'official_cn',label:t('k8s.downloadOfficialCn')}]" /><div class="field-help">{{ t('k8s.downloadSourceHint') }}</div></a-form-item>
            <a-form-item :label="t('k8s.systemImageSource')" :required="action==='create'">
              <a-select v-if="action==='create'" v-model:value="form.system_image_source" :aria-label="t('k8s.systemImageSource')" :options="[{value:'official',label:t('k8s.systemImageOfficial')},{value:'community_cn',label:t('k8s.systemImageCommunityCn')},{value:'custom',label:t('k8s.systemImageCustom')}]" />
              <a-input v-else :value="t('k8s.systemImageInherited')" disabled :aria-label="t('k8s.systemImageSource')" />
              <code v-if="action==='create'&&form.system_image_source==='community_cn'" class="field-help">{{ systemImageRegistry }}</code>
              <div class="field-help">{{ t(action==='create'?'k8s.systemImageSourceHint':'k8s.systemImageJoinHint') }}</div>
            </a-form-item>
            <a-form-item v-if="action==='create'&&form.system_image_source==='custom'" :label="t('k8s.systemImageRegistryPrefix')" required class="wide"><a-input v-model:value="form.custom_system_image_registry" :aria-label="t('k8s.systemImageRegistryPrefix')" placeholder="registry.example.com:5000/team" :maxlength="512" /><div class="field-help">{{ t('k8s.systemImagePrefixHint') }}</div></a-form-item>
            <div v-if="action==='create'&&form.download_source==='official_cn'&&form.system_image_source==='official'" class="field-help source-suggestion wide">{{ t('k8s.sourceIndependentHint') }}</div>
          </div>
          <a-form-item label="安装方式"><a-radio-group v-model:value="form.profile" button-style="solid"><a-radio-button value="default">默认安装</a-radio-button><a-radio-button value="custom">自定义安装</a-radio-button></a-radio-group></a-form-item>
          <div v-if="form.profile==='default'" class="default-install"><Check :size="17" /><div><strong>使用 K3s 默认配置</strong><p>{{ action==='create'?'安装控制平面及所选工作节点，完成后自动接入集群并关联主机。':joinMaster?'在所选主机添加控制平面，加入现有集群并继承已核对的关键配置。':'在所选主机安装工作节点，加入当前集群并关联主机。' }}</p><p>{{ t('k8s.nativeRuntimeHint') }}</p><small v-if="catalog">安装版本：{{ defaultVersion }}{{ action==='join'?'（跟随目标集群）':'' }}</small></div></div>
          <template v-else-if="catalog">
            <div class="catalog-caption"><strong>K3s 安装选项</strong><span>参数基于 {{ catalog.version }} · {{ catalog.catalog_version }}</span></div>
            <div class="form-columns"><a-form-item label="K3s 版本"><a-input v-model:value="form.installer.version" :placeholder="defaultVersion" /><div class="field-help">{{ action==='join'?joinMaster?`Master 必须与目标集群 ${defaultVersion} 的精确版本一致；留空自动跟随。`:`留空跟随目标集群 ${defaultVersion}；自定义版本需符合目标集群的版本偏差限制。`:`留空使用 ${defaultVersion}。` }}</div></a-form-item><a-form-item label="发布通道"><a-input v-model:value="form.installer.channel" placeholder="stable" /><div v-if="action==='join'" class="field-help">加入节点必须固定精确版本，不能仅填写通道；使用通道时请同时指定版本。</div></a-form-item></div>
            <a-alert v-if="versionMismatch" type="warning" show-icon message="安装版本与参数目录版本不同，请核对选项兼容性。" />
            <a-tabs v-model:active-key="optionTab" class="option-tabs">
              <a-tab-pane v-if="showServerOptions" key="server" :tab="`控制平面（${serverOptionChoices.length}）`"><K3sOptionFields v-model:value="form.server_options" :options="serverOptionChoices" :disabled="busy" /></a-tab-pane>
              <a-tab-pane v-if="showAgentOptions" key="agent" :tab="`工作节点（${catalog.agent.length}）`"><K3sOptionFields v-model:value="form.agent_options" :options="catalog.agent" :disabled="busy" /></a-tab-pane>
              <a-tab-pane key="installer" :tab="`安装器（${catalog.installer.length}）`"><K3sOptionFields v-model:value="form.installer" :options="installerOptions" :disabled="busy" /></a-tab-pane>
            </a-tabs>
            <a-form-item label="覆盖配置（YAML）" class="override-field"><a-textarea v-model:value="form.overrides" :rows="8" spellcheck="false" :placeholder="overridesPlaceholder" /><div class="field-help">优先级：K3s 默认值 → 上方选项 → 此处覆盖。使用 server、agent、installer 分组；false 和空列表会覆盖原值。最终配置在下一步显示。</div></a-form-item>
          </template>
          </template>
          <div v-if="!visiblePreflight&&!checking" class="install-note">{{ isK3d?t('k8s.k3dPreflightHint'):'先进行安装前检测，通过后再生成安装计划。检测不会安装软件或修改主机；开始安装前仍会实时复查。' }}</div>
        </a-form>

        <div v-else-if="plan&&!installation" class="plan-review">
          <a-alert v-if="displayMode==='native'" type="info" show-icon :message="t('k8s.nativeInstallConfirmTitle')" :description="t('k8s.nativeInstallConfirmHint')" />
          <a-alert v-else type="warning" show-icon :message="t('k8s.k3dInstallConfirmTitle')" :description="t('k8s.k3dSingleHostWarning')" />
          <a-alert v-if="!planModeValid" type="error" show-icon :message="t('k8s.installModeMismatch')" />
          <a-alert v-if="!planSourceValid" type="error" show-icon :message="t('k8s.installSourceMismatch')" />
          <dl class="plan-facts"><div><dt>{{ t('k8s.installMode') }}</dt><dd>{{ t(displayMode==='k3d'?'k8s.installModeK3d':'k8s.installModeNative') }}</dd></div><template v-if="displayMode==='k3d'"><div><dt>{{ t('k8s.k3dTopologyLabel') }}</dt><dd>{{ t('k8s.k3dTopology', { agents: k3dConfig?.agents ?? '—' }) }}</dd></div><div><dt>{{ t('k8s.k3dBindAddress') }}</dt><dd>{{ k3dConfig?.bind_address || '—' }}</dd></div><div><dt>{{ t('k8s.k3dApiPort') }}</dt><dd>{{ k3dConfig?.api_port ?? '—' }}</dd></div></template></dl>
          <dl v-if="displayMode==='native'" class="plan-facts source-facts"><div v-for="fact in sourceFacts" :key="fact.label"><dt>{{ fact.label }}</dt><dd><code>{{ fact.value }}</code></dd></div></dl>
          <a-alert v-if="planExpired" type="warning" show-icon message="计划已过期，请返回配置后重新预览。" />
          <a-alert v-if="!planPreflightPassed" type="error" show-icon message="缺少通过的前置检查，请返回配置后重新检查。" />
          <dl class="plan-facts"><div v-if="action==='create'"><dt>集群名称</dt><dd>{{ plan.name||form.name }}</dd></div><div v-if="plan.version"><dt>安装版本</dt><dd><code>{{ plan.version }}</code></dd></div><div v-if="action==='join'&&plan.target_version"><dt>目标集群版本</dt><dd><code>{{ plan.target_version }}</code></dd></div><div><dt>API 地址</dt><dd><code>{{ plan.endpoint||form.endpoint }}</code></dd></div><div><dt>计划有效期</dt><dd>{{ formatDateTime(plan.expires_at,'zh-CN') }}</dd></div></dl>
          <div class="plan-hosts"><div v-for="host in plan.hosts||selectedHosts" :key="host.id"><Server :size="16" /><strong>{{ host.name }}</strong><code>{{ host.address }}</code><a-tag>{{ displayMode==='k3d'?t('k8s.k3dHost'):k3sHostRoleLabel('role' in host?String(host.role):undefined) }}</a-tag></div></div>
          <a-alert v-for="warning in plan.warnings" :key="warning" type="warning" show-icon :message="warning" />
          <section v-if="displayMode==='native'&&hasHostConfig" class="host-configs"><h4>最终主机配置</h4><details v-for="host in plan.hosts?.filter(item=>item.config)" :key="host.id" open><summary>{{ host.name }} · {{ k3sHostRoleLabel(host.role) }}</summary><pre>{{ host.config }}</pre></details></section>
          <section v-if="displayMode==='native'&&plan.server_config&&!hasHostConfig"><h4>控制平面最终配置</h4><pre>{{ plan.server_config }}</pre></section>
          <section v-if="displayMode==='native'&&plan.agent_config&&!hasHostConfig"><h4>工作节点最终配置</h4><pre>{{ plan.agent_config }}</pre></section>
          <section v-if="displayMode==='native'&&plan.installer_config"><h4>安装器配置</h4><pre>{{ plan.installer_config }}</pre></section>
          <section v-if="plan.commands?.length"><h4>执行命令</h4><pre v-for="(command,index) in plan.commands" :key="index">{{ command }}</pre></section>
        </div>

        <div v-else-if="installation" class="installation-result">
          <a-tag>{{ t(displayMode==='k3d'?'k8s.installModeK3d':'k8s.installModeNative') }}</a-tag><a-alert v-if="displayMode==='k3d'" type="info" :message="t('k8s.k3dTopology', { agents: k3dConfig?.agents ?? '—' })" :description="t('k8s.k3dSingleHostWarning')" />
          <dl v-if="displayMode==='native'" class="plan-facts source-facts"><div v-for="fact in sourceFacts" :key="fact.label"><dt>{{ fact.label }}</dt><dd><code>{{ fact.value }}</code></dd></div></dl>
          <a-alert :type="progressTone" show-icon :message="k3sInstallationLabel(installation.status)" :description="installation.error||installation.message||installation.phase||(running?'可以关闭窗口，安装会继续执行。':'')" />
          <a-alert v-if="installation.status==='failed'&&installation.cluster&&displayMode==='native'" type="warning" show-icon :message="action==='create'?'集群已创建，部分安装步骤未完成':'部分节点未完成加入'" description="已完成的节点会保留。请进入集群检查状态，再从添加节点中选择剩余主机。" />
          <a-alert v-else-if="installation.status==='failed'&&displayMode==='k3d'" type="warning" show-icon :message="t('k8s.k3dFailureTitle')" :description="t('k8s.k3dFailureHint')" />
          <a-button v-if="!running&&installation.cluster" class="manage-created-cluster" @click="manageCreatedCluster">{{ canManageClusterResources?'管理已创建集群':'查看已创建集群' }}</a-button>
          <p class="operation-id">任务编号：{{ installation.id }}</p>
          <a-alert v-if="pollError" type="warning" show-icon message="进度读取失败，安装任务可能仍在运行" :description="pollError"><template #action><a-button size="small" @click="refreshInstallation"><RefreshCw :size="13" />重新读取</a-button></template></a-alert>
          <K3sInstallationProgress :key="installation.id" :installation="installation" :now="now" />
        </div>
      </a-spin>
    </div>
    <template #footer><div class="provision-footer"><a-button :disabled="busy&&!checking" @click="close">{{ checking?'取消检测':running?'关闭窗口':'关闭' }}</a-button><a-button v-if="plan&&!installation" :disabled="busy" @click="plan=undefined;preflight=undefined;checkedInput='';checkedContext=''"><ChevronLeft :size="14" />返回配置</a-button><a-button v-if="installation&&!running" :disabled="busy" @click="configureAgain">{{ displayMode==='k3d'?t('k8s.k3dConfigureAgain'):installation.status==='succeeded'?'继续添加':'重新配置' }}</a-button><template v-if="!plan&&!installation"><a-button :type="canPreview?'default':'primary'" :loading="checking" :disabled="!canCheck" @click="checkPreflight">{{ preflight?'重新检测':'安装前检测' }}</a-button><a-button type="primary" :loading="busy&&!checking" :disabled="!canPreview" @click="preview">生成安装计划</a-button></template><a-button v-else-if="plan&&!installation" type="primary" :loading="busy" :disabled="planExpired||!canExecute||!planPreflightPassed||!planModeValid||!planSourceValid" @click="execute">开始安装</a-button></div></template>
  </a-drawer>
</template>

<style scoped>
.provision-content{grid-template-columns:minmax(0,1fr);min-width:0}.provision-content>*{min-width:0}.provision-content :deep(.ant-alert-content),.provision-content :deep(.ant-form-item),.provision-content :deep(.ant-form-item-control-input-content){min-width:0;overflow-wrap:anywhere}.provision-content :deep(.ant-select-selection-item){min-width:0}.default-install>div{min-width:0}.field-help,.catalog-caption,.plan-hosts{overflow-wrap:anywhere}
.source-options{margin-top:16px}.source-options :deep(.ant-select){width:100%}.source-suggestion{margin-top:-10px;margin-bottom:22px}.source-facts{padding:14px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg)}
.host-configs details+details{margin-top:12px}.host-configs summary{padding:9px 0;cursor:pointer;font-size:12px}
.recent-actions{display:flex;justify-content:flex-end;padding:4px 14px;background:var(--edo-bg)}.recent-actions :deep(.recent-refresh.ant-btn-link:not(:disabled)){color:var(--edo-nav-active-color)}.recent-actions :deep(.recent-refresh.ant-btn-link:not(:disabled):hover){color:var(--edo-text)}.recent-actions :deep(.recent-refresh.ant-btn-link:focus-visible){outline:2px solid var(--edo-primary);outline-offset:2px}
.recent-operations{border:1px solid var(--edo-border);border-radius:6px}.recent-operations summary{cursor:pointer;padding:11px 14px;font-size:12px;background:var(--edo-bg)}.recent-operations summary>span{margin-left:6px;color:var(--edo-muted)}.recent-operation{display:flex;align-items:center;gap:8px 12px;flex-wrap:wrap;padding:12px 14px;border-top:1px solid var(--edo-border)}.recent-operation.current-operation{background:color-mix(in srgb,var(--edo-primary) 6%,var(--edo-surface))}.recent-operation>div{flex:1;min-width:120px}.recent-operation strong,.recent-operation small{display:block;overflow-wrap:anywhere}.recent-operation strong{font-size:12px;font-weight:500}.recent-operation small{margin-top:4px;font-size:11px;color:var(--edo-muted)}
.provision-content{display:grid;gap:22px}.provision-form{margin-top:6px}.form-columns{display:grid;grid-template-columns:1fr 1fr;gap:0 16px}.wide{grid-column:1/-1}.field-help{margin-top:6px;font-size:12px;line-height:1.65;color:var(--edo-muted)}.default-install{display:flex;gap:12px;padding:18px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg)}.default-install>svg{color:#1d9459;flex:none;margin-top:3px}.default-install strong{font-size:13px;font-weight:600}.default-install p{margin:5px 0;font-size:12px;line-height:1.7;color:var(--edo-muted)}.default-install small{color:var(--edo-muted);font-size:11px}.install-note{padding-top:18px;font-size:12px;color:var(--edo-muted);line-height:1.7}.catalog-caption{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:14px}.catalog-caption strong{font-size:14px;font-weight:600}.catalog-caption span{font-size:11px;color:var(--edo-muted)}.option-tabs{margin:8px 0 20px}.override-field{margin-top:22px}.override-field :deep(textarea){font-family:Consolas,monospace;font-size:12px}.provision-footer{display:flex;align-items:center;justify-content:flex-end;gap:8px}.plan-review,.installation-result{display:grid;gap:16px}.plan-facts{margin:0;display:grid;grid-template-columns:1fr 1fr;gap:14px}.plan-facts div{min-width:0}.plan-facts dt{color:var(--edo-muted);font-size:12px;margin-bottom:5px}.plan-facts dd{margin:0;font-size:13px;overflow-wrap:anywhere}.plan-hosts{border:1px solid var(--edo-border);border-radius:6px}.plan-hosts>div{display:flex;align-items:center;gap:10px;padding:12px;border-bottom:1px solid var(--edo-border);flex-wrap:wrap;font-size:12px}.plan-hosts>div:last-child{border:0}.plan-hosts strong{font-weight:500}.plan-hosts code{color:var(--edo-muted)}.plan-hosts .ant-tag{margin-left:auto}.plan-review h4,.installation-result h4{margin:0 0 8px;font-size:13px;font-weight:600}.plan-review pre,.installation-logs{margin:0;padding:14px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg);font-size:12px;line-height:1.7;white-space:pre-wrap;overflow-wrap:anywhere;max-height:360px;overflow:auto}.plan-review pre+pre{margin-top:10px}.operation-id{font-size:11px;color:var(--edo-muted);margin:0;overflow-wrap:anywhere}.operation-hosts>div{display:flex;align-items:center;gap:10px;padding:12px 0;flex-wrap:wrap;border-bottom:1px solid var(--edo-border)}.operation-hosts span{flex:1}.operation-hosts strong,.operation-hosts small{display:block}.operation-hosts strong{font-size:13px;font-weight:500}.operation-hosts small{font-size:11px;color:var(--edo-muted);margin-top:3px}.operation-hosts p{flex-basis:100%;margin:0 0 0 26px;font-size:12px;color:var(--edo-muted)}@media(max-width:650px){.form-columns,.plan-facts{grid-template-columns:1fr}.catalog-caption{align-items:flex-start;flex-direction:column}.provision-footer{flex-wrap:wrap}}
.provision-content .form-columns,.provision-content .plan-facts{grid-template-columns:repeat(2,minmax(0,1fr))}.plan-review,.installation-result{grid-template-columns:minmax(0,1fr);min-width:0}.plan-review>*,.installation-result>*{min-width:0}@media(max-width:650px){.provision-content .form-columns,.provision-content .plan-facts{grid-template-columns:minmax(0,1fr)}}
</style>
