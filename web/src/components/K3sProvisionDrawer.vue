<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ChevronLeft, CircleDot, Download, FileCode2, RefreshCw, Server } from 'lucide-vue-next'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { listHosts, type InfrastructureHost } from '@/api/infrastructure'
import { apiErrorMessage } from '@/api/resources'
import {
  getK3sCatalog, getK3sInstallation, getLastFailedK3sInstallation, checkK3sPreflight, previewK3sPlan, startK3sInstallation,
  type K3sCatalog, type K3sInstallation, type K3sPlan, type K3sPlanHost, type K3sPlanInput, type K3sPreflightReport, type K3sPreflightCheck, type K3sJoinRole, type K3sInstallMode, type K3dConfig, type K3dImageSource, type K3sDownloadSource,
} from '@/api/k3s'
import K3sOptionFields from '@/components/K3sOptionFields.vue'
import K3dImageFields from '@/components/K3dImageFields.vue'
import K3sPreflightReportView from '@/components/K3sPreflightReport.vue'
import K3sInstallationProgress from '@/components/K3sInstallationProgress.vue'
import HostDrawer from '@/components/HostDrawer.vue'
import YamlCodeEditor from '@/components/YamlCodeEditor.vue'
import { hasCustomK3sDatastore } from '@/utils/k3sDatastore'
import { useAuthStore } from '@/stores/auth'
import { availableK3sHosts, k3sAutomaticEndpoint, k3sHostDisabledReason, k3sInstallationLabel, k3sInstallationLogView, k3sInstallationRunning, k3sMasterRawArgumentOptions, k3sMasterRawArgumentReason, k3sPlanHostRoleLabels, k3sServerAgentEnabled, validateK3sOverrides } from '@/utils/k3sProvision'
import { defaultK3dConfig, defaultK3dBindAddress, k3dResourceError, sameK3dResources, validK3dBindAddress, validK3dImageSelection, k3dImageSourceLabel } from '@/utils/k3dProvision'

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
const previousFailure = ref<K3sInstallation>(),failureError = ref(''),failureLoading = ref(false)
const previousFailureLogs = computed(()=>k3sInstallationLogView(previousFailure.value))
const plan = ref<K3sPlan>(),installation = ref<K3sInstallation>(),optionTab = ref('server'),now = ref(Date.now())
const preflight = ref<K3sPreflightReport>()
const hostConfigOpen=ref(false),privilegeEditingHost=ref<InfrastructureHost>()
const checking=ref(false),checkedInput=ref(''),checkedContext=ref('')
const k3dImageReady=ref(false)
const preflightView=ref<{focus:()=>void}>(),errorRegion=ref<HTMLElement>()
const emptyForm = () => ({
  mode:'native' as K3sInstallMode,k3d:defaultK3dConfig(),
  k3d_image_drafts:{} as Partial<Record<K3dImageSource,string>>,
  datastore:'sqlite' as 'sqlite'|'etcd',
  download_source:'official_cn' as K3sDownloadSource,system_image_source:'community_cn' as 'official'|'community_cn'|'custom',custom_system_image_registry:'',
  name:'',server_host_id:'',worker_host_ids:[] as string[],profile:'default' as 'default'|'custom',endpoint:'',
  join_role:'agent' as K3sJoinRole,join_host_ids:[] as string[],server_token:'',
  server_options:{} as Record<string,unknown>,agent_options:{} as Record<string,unknown>,installer:{} as Record<string,unknown>,overrides:'',
})
const form = reactive(emptyForm())
let modeDrafts: Partial<Record<K3sInstallMode,ReturnType<typeof emptyForm>>> = {},restoringMode=false
let mounted = true,revision = 0,request:AbortController|undefined,pollRequest:AbortController|undefined,failureRequest:AbortController|undefined
let pollTimer:ReturnType<typeof setTimeout>|undefined,clock:ReturnType<typeof setInterval>|undefined,completedID = '',operationContext = ''
let pollFailures=0
const selectedProject = ref(readSelectedProject())
const context = computed(() => JSON.stringify([auth.user?.id??'',auth.user?.project_id??'',selectedProject.value,props.action,props.cluster]))
const hostChoices = computed(() => availableK3sHosts(hosts.value))
const hostOptions = computed(() => hostChoices.value.map(host=>({
  value:host.id,label:`${host.name} · ${host.address}${k3sHostDisabledReason(host)?`（${k3sHostDisabledReason(host)}）`:''}`,
  disabled:Boolean(k3sHostDisabledReason(host)),
})))
const isK3d = computed(() => props.action==='create'&&form.mode==='k3d')
const serverRunsAgent = computed(() => props.action==='create'&&!isK3d.value&&k3sServerAgentEnabled(form.profile==='custom'?form.server_options:{},form.profile==='custom'?form.overrides:''))
const agentOptions = computed(() => hostOptions.value.filter(option=>serverRunsAgent.value||option.value!==form.server_host_id))
// 回填只补空值，保留用户后来分别选择的控制平面和 Worker。
const workerHostSelection = computed({
  get:()=>[...new Set(form.worker_host_ids)].filter(id=>serverRunsAgent.value||id!==form.server_host_id),
  set:(value:string[])=>{
    const ids=[...new Set(value)]
    form.worker_host_ids=ids
    if(serverRunsAgent.value&&!form.server_host_id&&ids.length)form.server_host_id=ids[0]!
  },
})
const serverHostSelection = computed({
  get:()=>form.server_host_id,
  set:(value:string)=>{
    form.server_host_id=value
    if(serverRunsAgent.value&&value&&!workerHostSelection.value.length)form.worker_host_ids=[value]
  },
})
const displayMode = computed(() => installation.value?modeOf(installation.value):plan.value?modeOf(plan.value):isK3d.value?'k3d':'native')
const k3dConfig = computed(() => installation.value?installation.value.k3d:plan.value?.k3d)
const k3dResourceRoles = [
  {key:'server_resources',label:'Server'},
  {key:'worker_resources',label:'Worker（每个）'},
] as const
const k3dResourceErrors = computed(() => ({
  server_resources:k3dResourceError(form.k3d.server_resources),
  worker_resources:k3dResourceError(form.k3d.worker_resources),
}))
function setK3dCPU(role:typeof k3dResourceRoles[number]['key'],value:number|string|null) {
  form.k3d[role].cpu_millis=typeof value==='number'?Math.round(value*1000):NaN
}
const systemImageRegistry = computed(() => form.system_image_source==='community_cn'?'registry.cn-hangzhou.aliyuncs.com':form.system_image_source==='custom'?form.custom_system_image_registry.trim():'')
const useSystemImageRegistry = computed({
  get:()=>form.system_image_source!=='official',
  set:(checked:boolean)=>{form.system_image_source=checked?'community_cn':'official'},
})
const sourceInputValid = computed(() => isK3d.value||(['official','official_cn'].includes(form.download_source)&&(props.action==='join'||form.system_image_source!=='custom'||Boolean(systemImageRegistry.value))))
const frozenSources = computed(() => installation.value?installation.value.sources:plan.value?.sources)
const sourceFacts = computed(() => {
  const sources=frozenSources.value
  return [
    {key:'installer_url',label:t('k8s.installerSourceURL'),value:sources?.installer_url||t('k8s.sourceNotRecorded')},
    {key:'artifact_url',label:t('k8s.artifactSourceURL'),value:sources?.artifact_url||t('k8s.sourceNotRecorded')},
    {key:'system_image_registry',label:t('k8s.systemImageSource'),value:typeof sources?.system_image_registry==='string'?(sources.system_image_registry||t('k8s.systemImageOfficial')):t(props.action==='join'?'k8s.systemImageInheritedUnrecorded':'k8s.sourceNotRecorded')},
  ]
})
const summarySourceFacts = computed(()=>sourceFacts.value.filter(fact=>fact.key==='system_image_registry'))
const downloadSourceFacts = computed(()=>sourceFacts.value.filter(fact=>fact.key!=='system_image_registry'))
const k3dEndpoint = computed(() => {const address=form.k3d.bind_address.trim();return validK3dBindAddress(address)?`https://${address}:${form.k3d.api_port}`:''})
const joinMaster = computed(() => props.action==='join'&&form.join_role==='server')
const joinHostSelection = computed({
  get:()=>joinMaster.value?form.join_host_ids[0]:form.join_host_ids,
  set:(value:string|string[]|undefined)=>{form.join_host_ids=Array.isArray(value)?value:value?[value]:[]},
})
const showServerOptions = computed(() => !isK3d.value&&(props.action==='create'||joinMaster.value))
const showAgentOptions = computed(() => !isK3d.value&&(props.action==='create'||!joinMaster.value))
const customDatastore = computed(() => props.action==='create'&&form.profile==='custom'&&hasCustomK3sDatastore(form.server_options,form.overrides))
const selectedHosts = computed(() => hosts.value.filter(host=>props.action==='join'?form.join_host_ids.includes(host.id):host.id===form.server_host_id||!isK3d.value&&form.worker_host_ids.includes(host.id)))
const reviewHosts = computed(() => {
  const targets: K3sPlanHost[] = plan.value?.hosts ?? selectedHosts.value
  return targets.map(host=>({...host,roleLabels:displayMode.value==='k3d'?[t('k8s.k3dHost')]:k3sPlanHostRoleLabels(host,plan.value?.server_config)}))
})
const hasHostConfig = computed(() => plan.value?.hosts?.some(host=>Boolean(host.config)))
const endpointPlaceholder = computed(() => k3sAutomaticEndpoint(hosts.value.find(host=>host.id===form.server_host_id),form.profile==='custom'?form.server_options:{},form.profile==='custom'?form.overrides:'')||'https://192.168.1.10:6443')
const canManageClusterResources = computed(() => auth.canAny(['kubernetes.node.read','kubernetes.workload.read','kubernetes.config.read','kubernetes.storage.read','kubernetes.network.read']))
const canExecute = computed(() => auth.canAny([props.action==='create'?'cluster.create':'kubernetes.node.create'])&&auth.canAny(['cluster.execute'])&&auth.canAny(['cluster.update'])&&auth.canAny(['cluster.read','deployment.read']))
const canCheck = computed(() => Boolean(catalog.value)&&!loadError.value&&canExecute.value&&!hostConfigOpen.value&&!loading.value&&!busy.value&&sourceInputValid.value&&(props.action==='create'?Boolean(form.name.trim()&&form.server_host_id&&(!isK3d.value||k3dImageReady.value&&validK3dConfig(form.k3d))):Boolean(props.cluster&&form.join_host_ids.length&&form.join_host_ids.length<=(joinMaster.value?1:20)&&(!joinMaster.value||form.server_token.trim()))))
const canPreview = computed(() => canCheck.value&&(!isK3d.value||reportPassed(preflight.value)&&checkedInput.value===signature.value&&checkedContext.value===context.value))
const running = computed(() => k3sInstallationRunning(installation.value))
const planExpired = computed(() => Boolean(plan.value)&&(!Number.isFinite(Date.parse(plan.value!.expires_at))||Date.parse(plan.value!.expires_at)<=now.value))
const visiblePreflight = computed(() => readPreflight(plan.value?.preflight)??preflight.value)
const planPreflightPassed = computed(() => {
  if(plan.value&&modeOf(plan.value)==='native')return true
  const report=readPreflight(plan.value?.preflight)
  return reportPassed(report)
})
const planModeValid = computed(() => !!plan.value&&modeOf(plan.value)===(isK3d.value?'k3d':'native')&&(modeOf(plan.value)!=='k3d'||k3dPlanMatches(plan.value)))
const planSourceValid = computed(() => !!plan.value&&(modeOf(plan.value)==='k3d'||nativeSourcesMatch(plan.value,input())))
const currentStep = computed(() => displayMode.value==='k3d'?(installation.value?3:plan.value?2:preflight.value||checking.value?1:0):(installation.value?2:plan.value?1:0))
const title = computed(() => props.action==='create'?t('k8s.createClusterTitle'):'添加 K3s 节点')
const installerOptions = computed(() => catalog.value?.installer.filter(option=>!['INSTALL_K3S_VERSION','INSTALL_K3S_CHANNEL'].includes(option.name))??[])
const serverOptionChoices = computed(() => catalog.value?.server.map(option=>{
  if(props.action==='create'&&option.name==='cluster-init')return {...option,managed:true,description:t('k8s.datastoreManaged')}
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
  if(isK3d.value)return {action:'create',mode:'k3d',name:form.name.trim(),server_host_id:form.server_host_id,profile:'default',endpoint:k3dEndpoint.value,k3d:{...form.k3d,bind_address:form.k3d.bind_address.trim(),server_resources:{...form.k3d.server_resources},worker_resources:{...form.k3d.worker_resources}}}
  const custom=form.profile==='custom'
  const installer=custom?Object.fromEntries(Object.entries(form.installer).filter(([key,value])=>!['version','channel'].includes(key)||String(value??'').trim())):{}
  return {
    action:props.action,mode:'native',download_source:form.download_source,...(props.action==='join'?{cluster_id:props.cluster,join_role:form.join_role,join_host_ids:[...new Set(form.join_host_ids)],...(joinMaster.value?{server_token:form.server_token}:{})}:{server_host_id:form.server_host_id,agent_host_ids:[...new Set(form.worker_host_ids)].filter(id=>id!==form.server_host_id),system_image_registry:systemImageRegistry.value}),name:form.name.trim(),
    profile:form.profile,server_options:custom&&showServerOptions.value?{...form.server_options}:{},agent_options:custom&&showAgentOptions.value?{...form.agent_options}:{},
    ...(props.action==='create'&&!customDatastore.value?{datastore:form.datastore}:{}),
    installer,overrides:custom?form.overrides:'',endpoint:form.endpoint.trim(),
  }
}
function modeOf(value:{mode?:K3sInstallMode}) {return value.mode==='k3d'?'k3d':'native'}
function nativeSourcesMatch(value:K3sPlan,payload:K3sPlanInput) {
  const sources=value.sources
  return !!sources&&sources.download_source===payload.download_source&&Boolean(sources.installer_url&&sources.artifact_url)&&(payload.action!=='create'||sources.system_image_registry===(payload.system_image_registry??''))
}
function validK3dConfig(value?:K3dConfig) {return !!value&&Number.isInteger(value.agents)&&value.agents>=0&&value.agents<=8&&Number.isInteger(value.api_port)&&value.api_port>=1024&&value.api_port<=65535&&validK3dBindAddress(value.bind_address)&&validK3dImageSelection(value)&&!k3dResourceError(value.server_resources)&&!k3dResourceError(value.worker_resources)}
function k3dPlanMatches(value:K3sPlan) {const config=value.k3d;return validK3dConfig(config)&&value.hosts?.length===1&&value.hosts[0]?.role==='host'&&value.hosts[0]?.id===form.server_host_id&&config?.agents===form.k3d.agents&&config?.api_port===form.k3d.api_port&&config?.bind_address===form.k3d.bind_address.trim()&&config?.image_source===form.k3d.image_source&&config?.image===form.k3d.image&&sameK3dResources(config?.server_resources,form.k3d.server_resources)&&sameK3dResources(config?.worker_resources,form.k3d.worker_resources)&&value.endpoint===k3dEndpoint.value}
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
  if(!report.hosts.every(host=>{
    const value=host.privilege
    if(value===undefined)return true
    if(!value||typeof value.distribution!=='string'||value.distribution.length>80||!['root','standard','unknown'].includes(value.account)||!['not_needed','not_installed','passwordless','password','unverified','denied','unknown'].includes(value.sudo)||typeof value.sudo_enabled!=='boolean'||typeof value.ready!=='boolean'||typeof value.message!=='string'||value.message.length>1024||!Array.isArray(value.guidance)||value.guidance.length>6||!value.guidance.every(step=>typeof step==='string'&&step.length<=512))return false
    return !value.ready||value.account==='root'&&value.sudo==='not_needed'||value.account==='standard'&&value.sudo_enabled&&['passwordless','password'].includes(value.sudo)
  }))return
  return report
}
function failurePreflight(error:unknown) {
  return readPreflight((error as {response?:{data?:{preflight?:unknown}}}|undefined)?.response?.data?.preflight)
}
function reportPassed(report?:K3sPreflightReport) {
  return Boolean(report?.passed&&report.hosts.length&&report.hosts.every(host=>host.passed&&(!host.privilege||host.privilege.ready)&&host.checks.length&&host.checks.every(check=>check.status!=='failed'))&&report.checks.every(check=>check.status!=='failed'))
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
function cancelRequests() {revision++;request?.abort();request=undefined;failureRequest?.abort();failureRequest=undefined;stopPoll()}
function resetForm() {
  previousFailure.value=undefined;failureError.value='';failureLoading.value=false
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
  void loadPreviousFailure()
  try {
    const [options,available]=await Promise.all([getK3sCatalog(props.action==='join'?props.cluster:'',controller.signal),listHosts()])
    if(!mounted||!props.open||current!==revision||!matchesContext(currentContext))return
    catalog.value=options;hosts.value=available
    if(installation.value&&operationContext===context.value)void refreshInstallation()
  } catch(err) {
    if(mounted&&props.open&&current===revision&&!controller.signal.aborted&&matchesContext(currentContext))loadError.value=apiErrorMessage(err)
  } finally {if(mounted&&current===revision)loading.value=false}
}
async function loadPreviousFailure() {
  if(!syncProject()||!props.open||!canExecute.value)return
  failureRequest?.abort();const controller=new AbortController();failureRequest=controller;const current=revision,currentContext=context.value
  failureLoading.value=true;failureError.value=''
  try {
    const result=await getLastFailedK3sInstallation(props.action==='join'?props.cluster:'',props.action,controller.signal)
    if(mounted&&props.open&&current===revision&&matchesContext(currentContext))previousFailure.value=result
  } catch(err) {if(mounted&&props.open&&current===revision&&!controller.signal.aborted&&matchesContext(currentContext)){previousFailure.value=undefined;failureError.value=apiErrorMessage(err)}}
  finally {if(mounted&&current===revision)failureLoading.value=false}
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
  } catch(err) {if(mounted&&props.open&&current===revision&&!controller.signal.aborted&&matchesContext(currentContext)){const report=failurePreflight(err);if(report){preflight.value=report;plan.value=undefined}error.value=apiErrorMessage(err)}}
  finally {if(mounted&&current===revision)busy.value=false}
}
function handleProgress(result:K3sInstallation) {
  if(['succeeded','failed','interrupted'].includes(result.status)&&completedID!==result.id){
    completedID=result.id
    if(result.status==='succeeded'&&props.action==='create'&&result.cluster)emit('created',result.cluster)
    emit('completed',result)
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
  void loadPreviousFailure()
}
function configurePrivilegeHost(id:string) {
  if(!props.open||busy.value||!canExecute.value||!auth.canAny(['cluster.update']))return
  const result=visiblePreflight.value?.hosts.find(host=>host.host_id===id)
  const host=selectedHosts.value.find(host=>host.id===id)
  if(!result?.privilege||result.privilege.ready||!host)return
  privilegeEditingHost.value=host;hostConfigOpen.value=true
}
async function privilegeHostSaved() {
  if(!props.open||!hostConfigOpen.value||!canExecute.value)return
  hostConfigOpen.value=false;privilegeEditingHost.value=undefined
  plan.value=undefined;preflight.value=undefined;checkedInput.value='';checkedContext.value='';error.value=''
  await initialize()
}
watch([context,()=>props.open,canExecute,signature],()=>{hostConfigOpen.value=false;privilegeEditingHost.value=undefined},{flush:'sync'})
function close() {if(!busy.value||checking.value)emit('update:open',false)}
function manageCreatedCluster() {
  const id=installation.value?.cluster?.id
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
  if(props.open){if(!catalog.value)void initialize();else void loadPreviousFailure()}
},{flush:'sync'})
watch(()=>form.download_source,source=>{
  if(restoringMode||props.action!=='create')return
  if(source==='official_cn'&&form.system_image_source==='official')form.system_image_source='community_cn'
  if(source==='official'&&form.system_image_source==='community_cn')form.system_image_source='official'
},{flush:'sync'})
watch(()=>form.server_host_id,(id,previous)=>{
  if(isK3d.value&&(!form.k3d.bind_address||form.k3d.bind_address===defaultK3dBindAddress(hosts.value.find(host=>host.id===previous))))form.k3d.bind_address=defaultK3dBindAddress(hosts.value.find(host=>host.id===id))
})
watch(()=>form.join_role,()=>{
  if(props.action!=='join'||installation.value)return
  cancelRequests();busy.value=false;loading.value=false
  Object.assign(form,{join_host_ids:[],server_token:'',server_options:{},agent_options:{},installer:{},overrides:''})
  optionTab.value=joinMaster.value?'server':'agent';plan.value=undefined;preflight.value=undefined;error.value=''
  if(props.open){if(!catalog.value)void initialize();else void loadPreviousFailure()}
})
watch(()=>props.open,open=>{
  if(!open){form.server_token='';cancelRequests();busy.value=false;checking.value=false;loading.value=false;if(!installation.value){preflight.value=undefined;checkedInput.value='';checkedContext.value=''}return}
  if(operationContext!==context.value||!k3sInstallationRunning(installation.value))resetForm()
  void initialize()
})
watch(context,()=>{cancelRequests();busy.value=false;loading.value=false;previousFailure.value=undefined;failureError.value='';failureLoading.value=false;catalog.value=undefined;hosts.value=[];loadError.value='';resetForm();if(props.open)void initialize()})
watch(canExecute,allowed=>{if(!allowed){form.server_token='';cancelRequests();busy.value=false;checking.value=false;checkedInput.value='';checkedContext.value='';loading.value=false;loadError.value='';failureError.value='';failureLoading.value=false;plan.value=undefined;preflight.value=undefined;installation.value=undefined;previousFailure.value=undefined;catalog.value=undefined;hosts.value=[]}else if(props.open)void initialize()})
onMounted(()=>{window.addEventListener('storage',projectChanged);window.addEventListener('edo:projects-changed',projectChanged);clock=setInterval(()=>{if(props.open)now.value=Date.now()},1000);if(props.open){resetForm();void initialize()}})
onBeforeUnmount(()=>{mounted=false;form.server_token='';cancelRequests();window.removeEventListener('storage',projectChanged);window.removeEventListener('edo:projects-changed',projectChanged);if(clock)clearInterval(clock)})
</script>

<template>
  <a-drawer :open="open" :title="title" width="min(1060px,96vw)" :closable="!busy||checking" :mask-closable="!busy||checking" @close="close">
    <div class="provision-content">
      <a-steps :current="currentStep" size="small" :items="displayMode==='k3d'?[{title:'安装配置'},{title:'安装前检测'},{title:'确认计划'},{title:'执行结果'}]:[{title:'安装配置'},{title:'确认安装'},{title:'安装进度'}]" />
      <a-alert v-if="!installation&&!visiblePreflight&&!checking&&displayMode==='k3d'" type="info" show-icon :message="t('k8s.k3dInstallTitle')" :description="t('k8s.k3dInstallHint')" />
      <a-alert v-if="!canExecute" type="warning" show-icon message="需要主机读取、集群执行和关联权限，以及对应的创建权限。" />
      <a-alert v-if="loadError" type="error" show-icon message="安装配置读取失败" :description="loadError"><template #action><a-button size="small" @click="initialize">重试</a-button></template></a-alert>
      <div v-if="error" ref="errorRegion" tabindex="-1"><a-alert type="error" show-icon :message="error" /></div>
      <a-alert v-if="checking" type="info" show-icon message="正在进行安装前检测" description="只读检测所选主机，不会安装或自动修复。" role="status" />
      <K3sPreflightReportView v-if="isK3d&&visiblePreflight&&!installation" ref="preflightView" :report="visiblePreflight" :mode="displayMode" :standalone="!plan" :can-configure-host="canExecute&&auth.canAny(['cluster.update'])" @configure-host="configurePrivilegeHost" />
      <details v-if="!installation&&(previousFailure||failureError)" class="previous-failure">
        <summary>上一次安装失败日志</summary>
        <a-alert v-if="failureError" type="warning" :message="failureError"><template #action><a-button size="small" :loading="failureLoading" @click="loadPreviousFailure">重新读取</a-button></template></a-alert>
        <template v-else><p v-if="previousFailureLogs.truncated" class="field-help">仅显示末尾 64 KiB 日志。</p><pre tabindex="0" aria-label="上一次安装失败日志">{{ previousFailureLogs.text||'暂无可显示的失败日志。' }}</pre></template>
      </details>
      <a-spin :spinning="loading">
        <a-form v-if="!plan&&!installation" layout="vertical" :disabled="busy||!canExecute" class="provision-form">
          <a-form-item v-if="action==='create'" :label="t('k8s.installMode')"><a-radio-group v-model:value="form.mode" :aria-label="t('k8s.installMode')" button-style="solid"><a-radio-button value="native">{{ t('k8s.installModeNative') }}</a-radio-button><a-radio-button value="k3d">{{ t('k8s.installModeK3d') }}</a-radio-button></a-radio-group></a-form-item>
          <div class="form-columns">
            <a-form-item v-if="action==='join'" label="新增节点角色" required class="wide"><a-radio-group v-model:value="form.join_role" button-style="solid" aria-label="新增节点角色"><a-radio-button value="agent">工作节点（Worker）</a-radio-button><a-radio-button value="server">控制平面（Master）</a-radio-button></a-radio-group></a-form-item><a-form-item v-if="action==='create'" label="集群名称" required><a-input v-model:value="form.name" :maxlength="64" placeholder="例如：生产集群" /></a-form-item>
            <a-form-item v-if="!isK3d" :label="action==='create'?'API 地址（可选）':'API 地址'" :required="action==='join'" :class="{'wide':action==='join'}"><a-input v-model:value="form.endpoint" :placeholder="endpointPlaceholder" /><div class="field-help">{{ action==='create'?'留空使用控制平面主机地址和最终监听端口；可填写负载均衡或其他访问地址。':'EDO 和新节点都需要能访问此地址。' }}</div></a-form-item>
            <a-form-item v-if="action==='create'" :label="isK3d?t('k8s.k3dHost'):'控制平面主机'" required><a-select v-model:value="serverHostSelection" :aria-label="isK3d?t('k8s.k3dHost'):'控制平面主机'" show-search option-filter-prop="label" :options="hostOptions" :placeholder="isK3d?t('k8s.k3dHostPlaceholder'):'选择安装 K3s Server 的主机'" /></a-form-item>
            <a-form-item v-if="action==='create'&&!isK3d" :label="t('k8s.workerHosts')"><a-select v-model:value="workerHostSelection" :aria-label="t('k8s.workerHosts')" mode="multiple" show-search option-filter-prop="label" :options="agentOptions" :placeholder="t('k8s.workerHostsPlaceholder')" /></a-form-item><a-form-item v-else-if="action==='join'" :label="joinMaster?'控制平面主机（Master）':'工作节点主机（Worker）'" required><a-select v-model:value="joinHostSelection" :mode="joinMaster?undefined:'multiple'" show-search option-filter-prop="label" :options="hostOptions" :placeholder="joinMaster?'选择加入控制平面的新主机':'选择加入工作节点的新主机'" /><div class="field-help">{{ joinMaster?'Master 每个计划仅加入 1 台，须等待当前节点就绪后逐台添加。':'Worker 每次最多选择 20 台新主机。' }}</div></a-form-item><a-form-item v-if="joinMaster" label="目标集群 Server Token" required class="wide"><a-input-password v-model:value="form.server_token" aria-label="目标集群 Server Token" :visibility-toggle="false" :maxlength="4096" autocomplete="off" placeholder="输入目标集群的 Server Token" /><div class="field-help">仅用于本次控制平面加入。切换角色或关闭窗口后清除，不用于工作节点加入。</div></a-form-item>
            <template v-if="isK3d"><a-form-item :label="t('k8s.k3dAgents')" required><a-input-number v-model:value="form.k3d.agents" :aria-label="t('k8s.k3dAgents')" :min="0" :max="8" :precision="0" /></a-form-item><a-form-item :label="t('k8s.k3dApiPort')" required><a-input-number v-model:value="form.k3d.api_port" :aria-label="t('k8s.k3dApiPort')" :min="1024" :max="65535" :precision="0" /></a-form-item><a-form-item :label="t('k8s.k3dBindAddress')" required class="wide"><a-input v-model:value="form.k3d.bind_address" :aria-label="t('k8s.k3dBindAddress')" :placeholder="t('k8s.k3dBindPlaceholder')" /><div class="field-help">{{ t('k8s.k3dBindHint') }}</div></a-form-item><a-form-item :label="t('k8s.k3dEndpoint')" class="wide"><a-input :value="k3dEndpoint" readonly :aria-label="t('k8s.k3dEndpoint')" /></a-form-item></template>
          </div>
          <K3dImageFields v-if="isK3d" v-model:value="form.k3d" v-model:drafts="form.k3d_image_drafts" :architecture="hosts.find(host=>host.id===form.server_host_id)?.architecture||''" :disabled="!canExecute" :scope="context" @valid="k3dImageReady=$event" />
          <section v-if="isK3d" class="k3d-resources" aria-label="节点资源配置">
            <h4 class="k3d-resource-heading">节点资源</h4>
            <div v-for="role in k3dResourceRoles" :key="role.key" class="k3d-resource-row">
              <h5>{{ role.label }}</h5>
              <a-form-item label="CPU" required :validate-status="k3dResourceErrors[role.key]?'error':undefined">
                <div class="resource-input"><a-input-number :value="Number.isFinite(form.k3d[role.key].cpu_millis)?form.k3d[role.key].cpu_millis/1000:null" :aria-label="`${role.label} CPU`" :min="0.01" :step="0.1" :precision="3" :disabled="busy||!canExecute" @update:value="setK3dCPU(role.key,$event)" /><span>核</span></div>
              </a-form-item>
              <a-form-item label="内存" required :validate-status="k3dResourceErrors[role.key]?'error':undefined">
                <div class="resource-input"><a-input-number v-model:value="form.k3d[role.key].memory_mib" :aria-label="`${role.label} 内存`" :min="6" :step="128" :precision="0" :disabled="busy||!canExecute" /><span>MiB</span></div>
              </a-form-item>
              <p v-if="k3dResourceErrors[role.key]" class="k3d-resource-error" role="alert">{{ k3dResourceErrors[role.key] }}</p>
            </div>
          </section>
          <a-alert v-if="joinMaster" type="info" show-icon message="目标集群需已启用内嵌 etcd" description="生成计划会核对 Server Token、目标控制平面、etcd 成员与关键配置。SQLite、外置数据存储或无法确认的目标会被阻断；不会自动转换数据库。关键配置自动继承，冲突覆盖会被拒绝。" /><a-alert v-if="!loading&&!loadError&&!hostChoices.length" type="info" show-icon message="暂无可安装的远程主机" description="请先在主机页面配置启用的 SSH 主机和登录凭据。" />
          <a-alert v-if="isK3d" type="info" show-icon :message="t('k8s.k3dSingleHostWarning')" />
          <template v-else>
          <div class="form-columns source-options">
            <a-form-item :label="t('k8s.installDownloadSource')" required><a-select v-model:value="form.download_source" :aria-label="t('k8s.installDownloadSource')" :options="[{value:'official',label:t('k8s.downloadOfficial')},{value:'official_cn',label:t('k8s.downloadOfficialCn')}]" /></a-form-item>
            <a-form-item :label="t('k8s.systemImageSource')">
              <div v-if="action==='create'" class="system-image-controls">
                <a-checkbox v-model:checked="useSystemImageRegistry" :aria-label="t('k8s.useSystemImageRegistry')">{{ t('k8s.useSystemImageRegistry') }}</a-checkbox>
                <a-select v-if="useSystemImageRegistry" v-model:value="form.system_image_source" :aria-label="t('k8s.systemImageSource')" :options="[{value:'community_cn',label:t('k8s.systemImageCommunityCn')},{value:'custom',label:t('k8s.systemImageCustom')}]" />
              </div>
              <a-input v-else :value="t('k8s.systemImageInherited')" disabled :aria-label="t('k8s.systemImageSource')" />
              <code v-if="action==='create'&&form.system_image_source==='community_cn'" class="field-help">{{ systemImageRegistry }}</code>
              <div class="field-help">{{ t(action==='create'?'k8s.systemImageRegistryOptOut':'k8s.systemImageJoinHint') }}</div>
            </a-form-item>
            <a-form-item v-if="action==='create'&&form.system_image_source==='custom'" :label="t('k8s.systemImageRegistryPrefix')" required class="wide"><a-input v-model:value="form.custom_system_image_registry" :aria-label="t('k8s.systemImageRegistryPrefix')" placeholder="registry.example.com:5000/team" :maxlength="512" /><div class="field-help">{{ t('k8s.systemImagePrefixHint') }}</div></a-form-item>
          </div>
          <a-form-item label="安装方式"><a-radio-group v-model:value="form.profile" button-style="solid"><a-radio-button value="default">默认安装</a-radio-button><a-radio-button value="custom">自定义安装</a-radio-button></a-radio-group></a-form-item>
          <a-form-item v-if="action==='create'" :label="t('k8s.datastoreType')">
            <a-input v-if="customDatastore" :value="t('k8s.datastoreCustom')" :aria-label="t('k8s.datastoreType')" readonly />
            <a-radio-group v-else v-model:value="form.datastore" role="radiogroup" :aria-label="t('k8s.datastoreType')" button-style="solid"><a-radio-button value="sqlite">SQLite</a-radio-button><a-radio-button value="etcd">{{ t('k8s.datastoreEtcd') }}</a-radio-button></a-radio-group>
            <div class="field-help"><div>{{ t('k8s.datastoreSqliteHint') }}</div><div>{{ t('k8s.datastoreEtcdHint') }}</div><div v-if="customDatastore">{{ t('k8s.datastoreCustomHint') }}</div></div>
          </a-form-item>
          <template v-if="form.profile==='custom'&&catalog">
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
          <div v-if="isK3d&&!visiblePreflight&&!checking" class="install-note">{{ t('k8s.k3dPreflightHint') }}</div>
        </a-form>

        <div v-else-if="plan&&!installation" class="plan-review">
          <a-alert v-if="displayMode==='k3d'" type="warning" show-icon :message="t('k8s.k3dInstallConfirmTitle')" :description="t('k8s.k3dSingleHostWarning')" />
          <a-alert v-if="!planModeValid" type="error" show-icon :message="t('k8s.installModeMismatch')" />
          <a-alert v-if="!planSourceValid" type="error" show-icon :message="t('k8s.installSourceMismatch')" />
          <a-alert v-if="planExpired" type="warning" show-icon message="计划已过期，请返回配置后重新预览。" />
          <a-alert v-if="displayMode==='k3d'&&!planPreflightPassed" type="error" show-icon message="缺少通过的前置检查，请返回配置后重新检查。" />

          <section class="review-section" aria-label="基础信息">
            <h4 class="review-section-heading"><CircleDot :size="15" aria-hidden="true" />基础信息</h4>
            <dl class="review-facts review-section-content">
              <div v-if="action==='create'"><dt>集群名称</dt><dd>{{ plan.name||form.name }}</dd></div>
              <div><dt>{{ t('k8s.installMode') }}</dt><dd><a-tag>{{ t(displayMode==='k3d'?'k8s.installModeK3d':'k8s.installModeNative') }}</a-tag></dd></div>
              <div v-if="plan.version"><dt>安装版本</dt><dd><code>{{ plan.version }}</code></dd></div>
              <div v-if="action==='join'&&plan.target_version"><dt>目标集群版本</dt><dd><code>{{ plan.target_version }}</code></dd></div>
              <div><dt>API 地址</dt><dd><code>{{ plan.endpoint||form.endpoint }}</code></dd></div>
              <template v-if="displayMode==='k3d'">
                <div><dt>{{ t('k8s.k3dTopologyLabel') }}</dt><dd>{{ t('k8s.k3dTopology', { agents: k3dConfig?.agents ?? '—' }) }}</dd></div>
                <div><dt>{{ t('k8s.k3dBindAddress') }}</dt><dd><code>{{ k3dConfig?.bind_address || '—' }}</code></dd></div>
                <div><dt>{{ t('k8s.k3dApiPort') }}</dt><dd>{{ k3dConfig?.api_port ?? '—' }}</dd></div>
              </template>
            </dl>
          </section>

          <section v-if="displayMode==='k3d'" class="review-section" aria-label="节点资源配置">
            <h4 class="review-section-heading"><Server :size="15" aria-hidden="true" />节点资源</h4>
            <dl class="review-facts review-section-content">
              <div v-for="role in k3dResourceRoles" :key="role.key"><dt>{{ role.label }}</dt><dd>{{ k3dConfig?.[role.key] ? `${k3dConfig[role.key]!.cpu_millis/1000} 核 / ${k3dConfig[role.key]!.memory_mib} MiB` : '—' }}</dd></div>
            </dl>
          </section>

          <section v-if="displayMode==='k3d'" class="review-section" aria-label="K3s 镜像">
            <h4 class="review-section-heading"><Download :size="15" aria-hidden="true" />K3s 镜像</h4>
            <dl class="review-facts review-section-content"><div><dt>镜像来源</dt><dd>{{ k3dImageSourceLabel(k3dConfig?.image_source) }}</dd></div><div class="wide"><dt>镜像地址</dt><dd><code>{{ k3dConfig?.image||'—' }}</code></dd></div></dl>
          </section>

          <section class="review-section" aria-label="目标主机">
            <h4 class="review-section-heading"><Server :size="15" aria-hidden="true" />目标主机</h4>
            <ul class="plan-hosts" tabindex="0" aria-label="安装目标主机">
              <li v-for="host in reviewHosts" :key="host.id"><span class="review-host-identity"><strong>{{ host.name }}</strong><code>{{ host.address }}</code></span><span class="review-host-roles"><a-tag v-for="role in host.roleLabels" :key="role">{{ role }}</a-tag></span></li>
            </ul>
          </section>

          <section v-if="displayMode==='native'" class="review-section" aria-label="安装来源">
            <h4 class="review-section-heading"><Download :size="15" aria-hidden="true" />安装来源</h4>
            <div class="review-section-content">
              <dl class="review-facts"><div v-for="fact in summarySourceFacts" :key="fact.key"><dt>{{ fact.label }}</dt><dd>{{ fact.value }}</dd></div></dl>
              <details class="review-disclosure download-addresses">
                <summary>下载地址</summary>
                <dl class="review-facts download-facts"><div v-for="fact in downloadSourceFacts" :key="fact.key"><dt>{{ fact.label }}</dt><dd><code>{{ fact.value }}</code></dd></div></dl>
              </details>
            </div>
          </section>

          <a-alert v-for="warning in plan.warnings" :key="warning" type="warning" show-icon :message="warning" />
          <section v-if="displayMode==='native'&&(hasHostConfig||plan.server_config||plan.agent_config||plan.installer_config)||plan.commands?.length" class="review-section review-configurations" aria-label="安装配置">
            <h4 class="review-section-heading"><FileCode2 :size="15" aria-hidden="true" />安装配置</h4>
            <div class="review-section-content configuration-stack">
              <template v-if="displayMode==='native'">
                <template v-if="hasHostConfig">
                  <YamlCodeEditor v-for="host in reviewHosts.filter(item=>item.config)" :key="`${plan.id}:${host.id}`" :model-value="host.config||''" readonly fullscreen collapsible default-collapsed :label="`最终主机配置：${host.name} · ${host.roleLabels.join('、')}`"><template #toolbar><span>{{ host.name }}</span><a-tag v-for="role in host.roleLabels" :key="role">{{ role }}</a-tag></template></YamlCodeEditor>
                </template>
                <YamlCodeEditor v-if="!hasHostConfig&&plan.server_config" :model-value="plan.server_config" readonly fullscreen collapsible default-collapsed label="控制平面最终配置"><template #toolbar><span>控制平面最终配置</span></template></YamlCodeEditor>
                <YamlCodeEditor v-if="!hasHostConfig&&plan.agent_config" :model-value="plan.agent_config" readonly fullscreen collapsible default-collapsed label="工作节点最终配置"><template #toolbar><span>工作节点最终配置</span></template></YamlCodeEditor>
                <YamlCodeEditor v-if="plan.installer_config" :model-value="plan.installer_config" readonly fullscreen collapsible default-collapsed label="安装器配置"><template #toolbar><span>安装器配置</span></template></YamlCodeEditor>
              </template>
              <details v-if="plan.commands?.length" class="review-disclosure review-commands"><summary>执行命令</summary><pre v-for="(command,index) in plan.commands" :key="index" tabindex="0">{{ command }}</pre></details>
            </div>
          </section>
        </div>

        <div v-else-if="installation" class="installation-result">
          <a-tag>{{ t(displayMode==='k3d'?'k8s.installModeK3d':'k8s.installModeNative') }}</a-tag><a-alert v-if="displayMode==='k3d'" type="info" :message="t('k8s.k3dTopology', { agents: k3dConfig?.agents ?? '—' })" :description="t('k8s.k3dSingleHostWarning')" />
          <dl v-if="displayMode==='native'" class="plan-facts source-facts"><div v-for="fact in sourceFacts" :key="fact.label"><dt>{{ fact.label }}</dt><dd><code>{{ fact.value }}</code></dd></div></dl>
          <a-alert v-if="!running||installation.error" :type="progressTone" show-icon :message="k3sInstallationLabel(installation.status)" :description="installation.error||installation.message||installation.phase" />
          <a-alert v-if="installation.status==='failed'&&installation.cluster&&displayMode==='native'" type="warning" show-icon :message="action==='create'?'集群已创建，部分安装步骤未完成':'部分节点未完成加入'" description="已完成的节点会保留。请进入集群检查状态，再从添加节点中选择剩余主机。" />
          <a-alert v-else-if="installation.status==='failed'&&displayMode==='k3d'" type="warning" show-icon :message="t('k8s.k3dFailureTitle')" :description="t('k8s.k3dFailureHint')" />
          <a-button v-if="!running&&installation.cluster" class="manage-created-cluster" @click="manageCreatedCluster">{{ canManageClusterResources?'管理已创建集群':'查看已创建集群' }}</a-button>
          <p class="operation-id">任务编号：{{ installation.id }}</p>
          <a-alert v-if="pollError" type="warning" show-icon message="进度读取失败，安装任务可能仍在运行" :description="pollError"><template #action><a-button size="small" @click="refreshInstallation"><RefreshCw :size="13" />重新读取</a-button></template></a-alert>
          <K3sInstallationProgress :key="installation.id" :installation="installation" :now="now" />
        </div>
      </a-spin>
    </div>
    <template #footer><div class="provision-footer"><a-button :disabled="busy&&!checking" @click="close">{{ checking?'取消检测':running?'关闭窗口':'关闭' }}</a-button><a-button v-if="plan&&!installation" :disabled="busy" @click="plan=undefined;preflight=undefined;checkedInput='';checkedContext=''"><ChevronLeft :size="14" />返回配置</a-button><a-button v-if="installation&&!running" :disabled="busy" @click="configureAgain">{{ displayMode==='k3d'?t('k8s.k3dConfigureAgain'):installation.status==='succeeded'?'继续添加':'重新配置' }}</a-button><template v-if="!plan&&!installation"><a-button v-if="isK3d" :type="canPreview?'default':'primary'" :loading="checking" :disabled="!canCheck" @click="checkPreflight">{{ preflight?'重新检测':'安装前检测' }}</a-button><a-button type="primary" :loading="busy&&!checking" :disabled="!canPreview" @click="preview">生成安装计划</a-button></template><a-button v-else-if="plan&&!installation" type="primary" :loading="busy" :disabled="planExpired||!canExecute||!planPreflightPassed||!planModeValid||!planSourceValid" @click="execute">开始安装</a-button></div></template>
  </a-drawer>
  <HostDrawer v-if="privilegeEditingHost&&props.open&&canExecute" v-model:open="hostConfigOpen" :host="privilegeEditingHost" :can-test="auth.canAny(['cluster.execute'])" @saved="privilegeHostSaved" />
</template>

<style scoped>
.provision-content{grid-template-columns:minmax(0,1fr);min-width:0}.provision-content>*{min-width:0}.provision-content :deep(.ant-alert-content),.provision-content :deep(.ant-form-item),.provision-content :deep(.ant-form-item-control-input-content){min-width:0;overflow-wrap:anywhere}.provision-content :deep(.ant-select-selection-item){min-width:0}.field-help,.catalog-caption,.plan-hosts{overflow-wrap:anywhere}
.source-options{margin-top:16px}.source-options :deep(.ant-select){width:100%}.source-suggestion{margin-top:-10px;margin-bottom:22px}.source-facts{padding:14px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg)}
.previous-failure{min-width:0;border-top:1px solid var(--edo-border);padding-top:10px}.previous-failure summary{width:fit-content;font-size:12px;color:var(--edo-muted);cursor:pointer;line-height:1.8}.previous-failure[open] summary{margin-bottom:10px}.previous-failure pre{margin:0;padding:12px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg);color:var(--edo-text);font:12px/1.7 SFMono-Regular,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere;max-height:280px;overflow:auto;tab-size:2}.previous-failure summary:focus-visible,.previous-failure pre:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
.provision-content{display:grid;gap:22px}.provision-form{margin-top:6px}.form-columns{display:grid;grid-template-columns:1fr 1fr;gap:0 16px}.wide{grid-column:1/-1}.field-help{margin-top:6px;font-size:12px;line-height:1.65;color:var(--edo-muted)}.install-note{padding-top:18px;font-size:12px;color:var(--edo-muted);line-height:1.7}.catalog-caption{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:14px}.catalog-caption strong{font-size:14px;font-weight:600}.catalog-caption span{font-size:11px;color:var(--edo-muted)}.option-tabs{margin:8px 0 20px}.override-field{margin-top:22px}.override-field :deep(textarea){font-family:Consolas,monospace;font-size:12px}.provision-footer{display:flex;align-items:center;justify-content:flex-end;gap:8px}.plan-review,.installation-result{display:grid;gap:16px}.plan-facts{margin:0;display:grid;grid-template-columns:1fr 1fr;gap:14px}.plan-facts div{min-width:0}.plan-facts dt{color:var(--edo-muted);font-size:12px;margin-bottom:5px}.plan-facts dd{margin:0;font-size:13px;overflow-wrap:anywhere}.plan-hosts{border:1px solid var(--edo-border);border-radius:6px}.plan-hosts>div{display:flex;align-items:center;gap:10px;padding:12px;border-bottom:1px solid var(--edo-border);flex-wrap:wrap;font-size:12px}.plan-hosts>div:last-child{border:0}.plan-hosts strong{font-weight:500}.plan-hosts code{color:var(--edo-muted)}.plan-hosts .ant-tag{margin-left:auto}.plan-review h4,.installation-result h4{margin:0 0 8px;font-size:13px;font-weight:600}.plan-review pre,.installation-logs{margin:0;padding:14px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-bg);font-size:12px;line-height:1.7;white-space:pre-wrap;overflow-wrap:anywhere;max-height:360px;overflow:auto}.plan-review pre+pre{margin-top:10px}.operation-id{font-size:11px;color:var(--edo-muted);margin:0;overflow-wrap:anywhere}.operation-hosts>div{display:flex;align-items:center;gap:10px;padding:12px 0;flex-wrap:wrap;border-bottom:1px solid var(--edo-border)}.operation-hosts span{flex:1}.operation-hosts strong,.operation-hosts small{display:block}.operation-hosts strong{font-size:13px;font-weight:500}.operation-hosts small{font-size:11px;color:var(--edo-muted);margin-top:3px}.operation-hosts p{flex-basis:100%;margin:0 0 0 26px;font-size:12px;color:var(--edo-muted)}@media(max-width:650px){.form-columns,.plan-facts{grid-template-columns:1fr}.catalog-caption{align-items:flex-start;flex-direction:column}.provision-footer{flex-wrap:wrap}}
.provision-content .form-columns,.provision-content .plan-facts{grid-template-columns:repeat(2,minmax(0,1fr))}.plan-review,.installation-result{grid-template-columns:minmax(0,1fr);min-width:0}.plan-review>*,.installation-result>*{min-width:0}@media(max-width:650px){.provision-content .form-columns,.provision-content .plan-facts{grid-template-columns:minmax(0,1fr)}}

.plan-review .review-section{min-width:0}.plan-review .review-section-heading{display:flex;align-items:center;gap:8px;margin:0;padding:9px 14px;border-block:1px solid var(--edo-border);background:color-mix(in srgb,var(--edo-primary) 6%,var(--edo-surface));color:var(--edo-text);font-size:14px;font-weight:600}.review-section-heading>svg{flex-shrink:0;color:var(--edo-primary)}.review-section-content{padding:12px 14px;min-width:0}.review-facts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px 24px;margin:0;min-width:0}.review-facts>div{display:flex;align-items:baseline;gap:10px;min-width:0;font-size:13px;line-height:1.7}.review-facts dt{flex:0 0 88px;color:var(--edo-muted);font-size:12px}.review-facts dd{flex:1;min-width:0;margin:0;overflow-wrap:anywhere}.review-facts code{font-size:12px}.review-facts .ant-tag{margin:0}.plan-review .plan-hosts{list-style:none;margin:0;padding:0;border:0;border-radius:0;max-height:240px;overflow:auto}.plan-hosts>li{display:grid;grid-template-columns:minmax(0,1fr) auto;align-items:center;gap:12px;padding:10px 14px;border-bottom:1px solid var(--edo-border);font-size:13px}.plan-hosts>li:last-child{border-bottom:0}.review-host-identity{display:flex;align-items:baseline;flex-wrap:wrap;gap:4px 14px;min-width:0}.review-host-identity strong{min-width:0;overflow-wrap:anywhere;font-weight:500}.review-host-identity code{min-width:0;color:var(--edo-muted);font-size:12px;overflow-wrap:anywhere}.plan-hosts>li .ant-tag{margin:0;white-space:normal;text-align:center}.review-disclosure{min-width:0}.review-disclosure summary{width:fit-content;max-width:100%;cursor:pointer;font-size:12px;line-height:1.8;color:var(--edo-text);overflow-wrap:anywhere}.download-addresses{margin-top:12px;padding-top:10px;border-top:1px solid var(--edo-border)}.review-disclosure[open]>summary{margin-bottom:10px}.download-facts{grid-template-columns:minmax(0,1fr)}.download-facts dt{flex-basis:112px}.configuration-stack{display:grid;gap:12px}.review-configurations :deep(.yaml-editor-heading){font-size:13px;overflow-wrap:anywhere}.review-configurations :deep(.yaml-editor-heading .ant-tag){margin:0}.review-configurations :deep(.yaml-code-editor .cm-scroller){min-height:112px;max-height:320px}.plan-review .review-commands pre{padding:12px;max-height:240px;min-width:0;font:12px/1.7 SFMono-Regular,Consolas,monospace}.review-disclosure summary:focus-visible,.plan-hosts:focus-visible,.review-commands pre:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}@media(max-width:650px){.review-facts{grid-template-columns:minmax(0,1fr);gap:10px}.review-facts dt{flex-basis:88px}.download-facts>div{display:grid;grid-template-columns:minmax(0,1fr);gap:2px}.download-facts dt{flex-basis:auto}.review-host-identity{display:grid;gap:2px}.review-section-content{padding:12px}.plan-hosts>li{padding:10px 12px;gap:8px}}
.system-image-controls{display:grid;gap:8px;min-width:0}.system-image-controls :deep(.ant-checkbox-wrapper){align-items:flex-start;overflow-wrap:anywhere}.review-host-roles{display:flex;flex-wrap:wrap;justify-content:flex-end;gap:4px 6px;max-width:280px;min-width:0}@media(max-width:650px){.plan-hosts>li{grid-template-columns:minmax(0,1fr)}.review-host-roles{justify-content:flex-start;max-width:100%}}
.k3d-resources{margin-bottom:20px;border-top:1px solid var(--edo-border)}.k3d-resource-heading{margin:0;padding:10px 12px;background:var(--edo-bg);font-size:13px;font-weight:600}.k3d-resource-row{display:grid;grid-template-columns:120px minmax(0,180px) minmax(0,200px);align-items:center;gap:8px 20px;padding:12px;border-bottom:1px solid var(--edo-border)}.k3d-resource-row h5{margin:0;font-size:13px;font-weight:500}.k3d-resource-row :deep(.ant-form-item){margin:0}.resource-input{display:flex;align-items:center;gap:8px}.resource-input :deep(.ant-input-number){width:130px;max-width:100%}.resource-input>span{color:var(--edo-muted);font-size:12px;white-space:nowrap}.k3d-resource-error{grid-column:2/-1;margin:0;color:var(--ant-color-error,#ff4d4f);font-size:12px}@media(max-width:650px){.k3d-resource-row{grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.k3d-resource-row h5{grid-column:1/-1}.resource-input :deep(.ant-input-number){width:100%;min-width:0}.k3d-resource-error{grid-column:1/-1}}
</style>
