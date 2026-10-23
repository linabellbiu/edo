<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Modal, message } from 'ant-design-vue'
import { isAxiosError } from 'axios'
import { ArrowUpRight, Box, Boxes, ChevronDown, ChevronRight, Clock3, FileText, GitBranch, Layers3, Minus, Plus, Power, RefreshCw, RotateCw, Search, Server, Settings2, TerminalSquare, Trash2, Workflow } from 'lucide-vue-next'

import client from '@/api/client'
import { subscribeDeliveryEvents } from '@/utils/deliveryEvents'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import DeliveryWorkflowDrawer from '@/components/DeliveryWorkflowDrawer.vue'
import DeploymentRollbackModal from '@/components/DeploymentRollbackModal.vue'
import type {DeliveryKind} from '@/api/delivery'
import type {WorkflowResponse} from '@/types/pipeline'
import BuildPlanWorkspace from '@/components/BuildPlanWorkspace.vue'
import ResourceDetailDrawer from '@/components/ResourceDetailDrawer.vue'
import ResourceRowActions from '@/components/ResourceRowActions.vue'
import ContainerLogDrawer from '@/components/ContainerLogDrawer.vue'
import GitCredentialDrawer from '@/components/GitCredentialDrawer.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import PipelineLogDrawer from '@/components/PipelineLogDrawer.vue'
import PipelineRunGraph from '@/components/PipelineRunGraph.vue'
import ReleasePlanEditorDrawer from '@/components/ReleasePlanEditorDrawer.vue'
import ReleasePlanExecuteModal from '@/components/ReleasePlanExecuteModal.vue'
import ReleasePlanWorkspace from '@/components/ReleasePlanWorkspace.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import TerminalDrawer from '@/components/TerminalDrawer.vue'
import { useAuthStore } from '@/stores/auth'
import type { GitCredential as Credential } from '@/types/credential'
import type { PipelineExecutionGraph, Workflow as PipelineWorkflow } from '@/types/pipeline'
import { formatGitReference } from '@/utils/gitReference'
import { retainCurrentDeploymentState, selectCurrentDeploymentInstances, selectLatestFailedDeploymentAttempts } from '@/utils/deploymentInstances'
import type { RollbackRecord } from '@/types/deploymentRollback'
import { deploymentRollbackEn, deploymentRollbackZh } from '@/locales/deploymentRollback'
import { formatDateTime, formatRelativeTime } from '@/utils/time'

type Section='applications'|'repositories'|'build-plans'|'image-registries'|'release-plans'
const props=defineProps<{section:Section}>(),route=useRoute(),router=useRouter(),auth=useAuthStore()
const {t,locale}=useI18n()
const {t:rollbackT}=useI18n({messages:{'zh-CN':{rollback:deploymentRollbackZh},'en-US':{rollback:deploymentRollbackEn}}})
const deliveryOpen=ref(false), deliveryApplicationID=ref('')
const canConfigureDelivery=computed(()=>['application.read','application.update','workflow.read','workflow.create','deployment_plan.create','deployment_plan.read','build_plan.read','cluster.read','deployment.read'].every(permission=>auth.canAny([permission])))
function configureDelivery(applicationID=''){deliveryApplicationID.value=applicationID;deliveryOpen.value=true}
async function deliveryCreated(result:WorkflowResponse){await refresh();await router.push({path:'/pipeline-plans/editor',query:{application:result.workflow.application_id,workflow:result.workflow.id}})}
function consumeDeliveryRequest(){if(props.section==='applications'&&route.query.delivery_kind&&canConfigureDelivery.value)configureDelivery(typeof route.query.application==='string'?route.query.application:'')}

interface Repository extends ResourceRecord{id:string;name:string;provider:string;clone_url:string;default_branch:string;webhook_enabled:boolean;webhook_url?:string;is_active:boolean;auth_type:string;username?:string;allow_insecure_http:boolean;has_credential?:boolean;credential_id?:string;api_credential_id?:string}
interface BuildPlan extends ResourceRecord{id:string;name:string;kind:'dockerfile'|'script';description:string;script?:string;dockerfile_path?:string;context_path:string;working_directory:string;artifact_path?:string;runtime_image?:string;image_registry_id?:string;target_stage?:string;pull:boolean;cache_enabled:boolean;build_args:Record<string,string>;environment_variables:Record<string,string>;timeout_seconds:number;is_active:boolean;created_at:string;updated_at:string;image_registry?:Registry}
type RegistryProvider='harbor'|'docker_hub'|'generic'
interface Registry extends ResourceRecord{id:string;name:string;provider:RegistryProvider;endpoint:string;namespace:string;has_credential:boolean;is_active:boolean}
interface Artifact extends ResourceRecord{id:string;application_id:string;build_run_id:string;pipeline_run_id?:string;kind:'oci_image'|'file_bundle';status:'available'|'expired'|'corrupt';name:string;original_name?:string;media_type?:string;digest:string;size_bytes:number;storage_kind:'local_file'|'registry'|'docker_daemon';image_ref?:string;created_at:string;updated_at:string}
interface ApplicationWorkflow extends PipelineWorkflow{workflow_template?:{id:string;name:string}}
interface WorkflowTemplate extends PipelineWorkflow{description?:string}
interface Application extends ResourceRecord{id:string;name:string;description:string;repository_id:string;poll_interval_seconds:number;last_observed_commit?:string;sync_status:string;sync_message?:string;last_checked_at?:string;is_active:boolean;repository?:Repository;workflows:ApplicationWorkflow[]}
interface Run extends ResourceRecord{id:string;application_id:string;workflow_id?:string;deployment_id?:string;release_plan_id?:string;release_plan_execution_id?:string;release_plan_execution_item_id?:string;retry_of_id?:string;artifact_id?:string;selected_artifact?:{id:string;name:string;kind:'oci_image'|'file_bundle';digest:string};trigger:string;ref:string;commit_sha:string;commit_message?:string;status:string;stage:string;message?:string;created_at:string;updated_at?:string;application?:Application;environment?:string;current_node_id?:string;current_node_name?:string;created_by?:string;image?:string;execution_graph?:PipelineExecutionGraph}
interface DeploymentTarget extends ResourceRecord{id:string;platform:string;environment_id:string;host_id:string;runtime_id:string;namespace?:string;workload_name?:string;container_name?:string}
interface DeploymentPlan extends ResourceRecord{id:string;kind?:string;service_name?:string;deployment_target_id:string;deployment_target?:DeploymentTarget}
interface DeploymentRecord extends ResourceRecord{id:string;pipeline_run_id?:string;application_id?:string;target_id:string;target_name:string;platform:string;environment_id:string;host_id:string;runtime_id:string;namespace:string;workload_name:string;container_name:string;deployment_plan_id?:string;deployment_plan_kind?:string;compose_service?:string;operation:string;image:string;image_display?:string;status:string;error_message?:string;runtime_deleted_at?:string;created_at:string;updated_at:string;finished_at?:string}
interface DockerContainerRecord extends ResourceRecord{id:string;names:string[];state:string;status:string}
interface DeploymentRuntimeState{deployment_id:string;kind:string;resource_id?:string;name:string;namespace?:string;state:string;running:boolean;count?:number;replicas?:number;ready_replicas?:number;available_replicas?:number;restart_configured?:boolean;stop_configured?:boolean;output?:string}
interface ShellControlConfiguration{deployment_id:string;restart_script:string;stop_script:string;timeout_seconds:number}
type StatusTone='neutral'|'info'|'success'|'warning'|'danger'
interface StatusMeta{tone:StatusTone;label:string;live:boolean}
interface ReleasePlanExecutionRecordItem{id:string;release_group_application_id:string;application_id:string;pipeline_run_id:string;status:string}
interface ReleasePlanExecutionRecord{id:string;release_plan_id:string;status:string;created_at:string;finished_at?:string;items?:ReleasePlanExecutionRecordItem[]}
 interface ReleasePlan extends ResourceRecord{id:string;name:string;version:string;description:string;status:string;is_active?:boolean;created_at:string;updated_at?:string;latest_execution?:{id:string;status:string;created_at:string;finished_at?:string;items?:ReleasePlanExecutionRecordItem[]};groups?:Array<{id:string;name:string;mode:string;failure_policy:string;sort_order?:number;dependencies?:Array<{depends_on_group_id:string}>;applications:Array<{id:string;application_id:string;application?:Application;manual_deploy?:boolean;source_type?:string;source_value?:string;sort_order?:number}>}>}
type ReleasePlanGroup=NonNullable<ReleasePlan['groups']>[number]
type ReleasePlanGroupApplication=ReleasePlanGroup['applications'][number]
interface GitRef{name:string;sha:string}
interface ManualArtifact{id:string;name:string;kind:'oci_image'|'file_bundle';digest:string;build_plan_id:string;ref?:string;commit_sha?:string;created_at:string}
interface RefResult{branches:GitRef[];tags:GitRef[];manual_sources?:Array<{id:string;name:string;environment?:string}>;artifacts?:ManualArtifact[];reference_error?:string}
interface RetryRunOptions{ref:string;commit_sha:string;commit_message?:string;artifacts:ManualArtifact[]}
type PlanExecutionLoadState='idle'|'loading'|'ready'|'blocked'|'error'
interface PlanExecutionReference{kind:'branch'|'tag';ref:string;name:string;sha:string}
interface PlanExecutionSource{id:string;name:string;environment?:string}
interface PlanExecutionItem{membershipID:string;applicationID:string;applicationName:string;workflowID:string;workflowRevision:number;workflows:Array<{id:string;name:string;revision:number}>;loadState:PlanExecutionLoadState;reason?:string;staticBlocked:boolean;sources:PlanExecutionSource[];refs:PlanExecutionReference[];artifacts:ManualArtifact[];executionMode:'code'|'artifact';selectedSourceID:string;selectedRef:string;selectedArtifactID:string;referenceError?:string}
interface PlanExecutionGroup{id:string;name:string;mode:string;failurePolicy:string;dependencies:string[];items:PlanExecutionItem[]}
interface ReleasePlanEditorValue{id:string;description:string;groups:Array<{id:string;name:string;mode:'parallel'|'sequential';failure_policy:'stop'|'continue';depends_on_group_ids:string[];applications:Array<{application_id:string;manual_deploy:boolean;source_type:string;source_value:string}>}>}

const applications=ref<Application[]>([]),repositories=ref<Repository[]>([]),credentials=ref<Credential[]>([]),workflowTemplates=ref<WorkflowTemplate[]>([]),buildPlans=ref<BuildPlan[]>([]),registries=ref<Registry[]>([]),runs=ref<Run[]>([]),releasePlans=ref<ReleasePlan[]>([]),deployments=ref<DeploymentRecord[]>([]),deploymentPlans=ref<DeploymentPlan[]>([]),artifacts=ref<Artifact[]>([])
const loading=ref(false),saving=ref(false),workflowRemovalID=ref(''),resourceMutationID=ref(''),formOpen=ref(false),editingID=ref(''),selectedWorkflowTemplateID=ref(''),registryTested=ref(false),testing=ref(false),repositoryTestingID=ref(''),manualApplicationID=ref(''),manualWorkflowID=ref(''),commitOpen=ref(false),commitOptions=ref<Array<{ref:string;name:string;sha:string;kind:'branch'|'tag'}>>([]),manualArtifacts=ref<ManualArtifact[]>([]),manualExecutionMode=ref<'code'|'artifact'>('code'),selectedArtifactID=ref(''),manualReferenceError=ref(''),selectedRef=ref(''),selectedSource=ref(''),manualSources=ref<Array<{id:string;name:string;environment?:string}>>([]),currentRun=ref<Run|null>(null),currentRunSelectionKey=ref(''),selectedRunID=ref(''),log=ref({open:false,runID:'',title:'',status:''})
const expandedApplications=ref<Record<string,boolean>>({}),expandedDeployments=ref<Record<string,boolean>>({}),dockerRuntimeContainers=ref<Record<string,DockerContainerRecord[]>>({}),dockerRuntimeLoading=ref<Record<string,boolean>>({}),dockerRuntimeLoaded=ref<Record<string,boolean>>({}),dockerRuntimeErrors=ref<Record<string,string>>({})
type ApplicationFilter='all'|'running'|'attention'|'inactive'
type ApplicationDetailTab='workflows'|'deployments'
const applicationSearch=ref(''),applicationFilter=ref<ApplicationFilter>('all')
const applicationDetailTabs=ref<Record<string,ApplicationDetailTab>>({}),checkingApplicationIDs=ref<Record<string,boolean>>({})
const deploymentRuntimeStates=ref<Record<string,DeploymentRuntimeState>>({}),deploymentRuntimeLoading=ref<Record<string,boolean>>({}),deploymentRuntimeErrors=ref<Record<string,string>>({}),deploymentRuntimeAction=ref('')
const scaleDialog=reactive({open:false,record:null as DeploymentRecord|null,replicas:1,submitting:false})
const shellControlDialog=reactive({open:false,record:null as DeploymentRecord|null,restart_script:'',stop_script:'',timeout_seconds:300,loading:false,saving:false})
const lifecycleOutput=reactive({open:false,title:'',output:''})
const dockerRuntimeRequests=new Map<string,Promise<void>>()
const containerLogs=ref({open:false,title:'',path:''}),terminal=ref({open:false,title:'',path:''})
const releasePlanResources=reactive({open:false,loading:false,planID:'',applicationID:'',pipelineRunID:'',title:'',error:'',records:[] as DeploymentRecord[]})
const runDeploymentRecords=ref<Record<string,DeploymentRecord[]>>({}),runDeploymentLoading=ref<Record<string,boolean>>({}),runDeploymentErrors=ref<Record<string,string>>({})
const runDeploymentRequests=new Map<string,AbortController>()
let releasePlanResourcesController:AbortController|null=null
const buildImageDestination=ref<'local'|'registry'>('local'),buildSaveArtifact=ref(false),buildArgsText=ref(''),buildEnvironmentText=ref(''),selectedBuildPlanID=ref(''),buildPlanView=ref<'overview'|'artifacts'>(route.query.view==='artifacts'?'artifacts':'overview'),artifactApplicationID=ref(''),artifactLoading=ref(false),artifactUploading=ref(false),artifactDownloadingID=ref('')
const planExecutionOpen=ref(false),planExecutionPlanID=ref(''),planExecutionTitle=ref(''),planExecutionExpectedUpdatedAt=ref(''),planExecutionRequestID=ref(''),planExecutionGroups=ref<PlanExecutionGroup[]>([])
const executingReleasePlanIDs=ref<string[]>([])
const retryOpen=ref(false),retryLoading=ref(false),retrySubmitting=ref(false),retryRunID=ref(''),retryOptions=ref<RetryRunOptions|null>(null),retryMode=ref<'rebuild'|'artifact'>('rebuild'),retryArtifactID=ref('')
const stoppingRunID=ref('')
const manualPicker=reactive({open:false,applicationID:'',workflowID:''})
const rollbackDialog=reactive({open:false,sourceID:'',pipelineRunID:''})
const releasePlanEditorOpen=ref(false),releasePlanEditorPlan=ref<ReleasePlan|null>(null),releasePlanMutationID=ref('')
const releasePlanAddApplicationOpen=ref(false),releasePlanAddApplicationPlanID=ref(''),releasePlanAddApplicationGroupID=ref(''),releasePlanAddApplicationIDs=ref<string[]>([])
let releaseTimer=0
let stopDeliveryEvents: (()=>void)|undefined
let deliveryConnected=false
let lastDeliverySync=0
let deliveryRefreshTimer: ReturnType<typeof setTimeout>|undefined
let applicationSyncOnly=false
const relativeTimeNow=ref(Date.now())
let planExecutionController:AbortController|null=null
const DEFAULT_APPLICATION_POLL_INTERVAL=3
const appForm=reactive({name:'',description:'',repository_id:'',workflow_template_id:'',poll_interval_seconds:DEFAULT_APPLICATION_POLL_INTERVAL})
const repoForm=reactive({name:'',provider:'github',clone_url:'',default_branch:'main',auth_type:'none',username:'',credential_id:'',api_credential_id:'',webhook_enabled:false,allow_insecure_http:false})
const creatingApplicationRepository=ref(false)
const credentialCreation=reactive({open:false,target:'git' as 'git'|'api',provider:'github',authType:'token' as Credential['auth_type']})
const formSection=computed(()=>creatingApplicationRepository.value?'repositories':props.section)
const formEditingID=computed(()=>creatingApplicationRepository.value?'':editingID.value)
const DEFAULT_RUNTIME_IMAGE='alpine:3.22'
const runtimeImageOptions=['alpine:3.22','node:24-alpine','golang:1.26-alpine','maven:3.9-eclipse-temurin-21-alpine'].map(value=>({value}))
const buildForm=reactive({name:'',kind:'dockerfile' as 'dockerfile'|'script',description:'',script:'',dockerfile_path:'Dockerfile',context_path:'.',working_directory:'.',artifact_path:'',runtime_image:DEFAULT_RUNTIME_IMAGE,image_registry_id:'',target_stage:'',pull:true,cache_enabled:true,timeout_seconds:1800})
const registryForm=reactive({name:'',provider:'generic' as RegistryProvider,endpoint:'https://',namespace:'',username:'',credential:'',allow_insecure_http:false})
const dockerHubRegistry=computed(()=>registryForm.provider==='docker_hub')
const repositoryWebhookAPI=computed(()=>{
 if(!formEditingID.value)return ''
 const path=repositories.value.find(item=>item.id===formEditingID.value)?.webhook_url||`/api/v1/webhooks/git/${formEditingID.value}`
 return new URL(path,window.location.origin).toString()
})
const selectedBuildRegistry=computed(()=>registries.value.find(item=>item.id===buildForm.image_registry_id))
const buildImagePathPreview=computed(()=>{
 const registry=selectedBuildRegistry.value
 const endpoint=(registry?.provider==='docker_hub'?'docker.io':registry?.endpoint||'[镜像仓库地址]').replace(/^https?:\/\//i,'').replace(/\/+$/,'')
 const namespace=(registry?.namespace||'[命名空间]').replace(/^\/+|\/+$/g,'')
 return `${endpoint}/${namespace}/[应用名]:[版本标签]`
})
const copy:Record<Section,{description:string}>={applications:{description:'创建应用，关联仓库并配置流水线。'},repositories:{description:'管理代码仓库、Git 凭据和 Webhook。'},'build-plans':{description:'保存可复用的 Dockerfile 或脚本构建配置。'},'image-registries':{description:'管理镜像仓库，保存前请先测试连接。'},'release-plans':{description:'选择多个应用一起发布，或查看流水线运行记录。'}}
const releaseView=computed(()=>route.query.view==='runs'?'runs':'plans')
const requestedRunID=computed(()=>typeof route.query.run==='string'?route.query.run:'')
const sectionPermission=computed(()=>props.section==='build-plans'?'build_plan':props.section==='image-registries'?'image_registry':props.section==='release-plans'?(route.query.view==='runs'?'pipeline_run':'release_plan'):'application')
const canCreate=computed(()=>props.section==='repositories'?auth.canAny(['repository.create']):auth.canAny([`${sectionPermission.value}.create`]))
const canUpdate=computed(()=>props.section==='repositories'?auth.canAny(['repository.update']):auth.canAny([`${sectionPermission.value}.update`]))
const canDelete=computed(()=>props.section==='repositories'?auth.canAny(['repository.delete']):auth.canAny([`${sectionPermission.value}.delete`]))
const canExecute=computed(()=>props.section==='repositories'?auth.canAny(['repository.execute']):auth.canAny([`${sectionPermission.value}.execute`]))
const canSaveCurrent=computed(()=>creatingApplicationRepository.value?auth.canAny(['repository.create']):editingID.value?canUpdate.value:canCreate.value)
const releasePlanAddApplicationTarget=computed(()=>{
 const plan=releasePlans.value.find(item=>item.id===releasePlanAddApplicationPlanID.value)
 const group=plan?.groups?.find(item=>item.id===releasePlanAddApplicationGroupID.value)
 return plan&&group?{plan,group}:null
})
const releasePlanAddApplicationOptions=computed(()=>{
 const target=releasePlanAddApplicationTarget.value
 if(!target)return []
 const usedIDs=new Set((target.plan.groups||[]).flatMap(group=>group.applications.map(item=>item.application_id)))
 return applications.value.filter(item=>item.is_active&&!usedIDs.has(item.id)).map(item=>({value:item.id,label:item.name}))
})
const currentDescription=computed(()=>props.section==='release-plans'?(releaseView.value==='runs'?'查看代码事件或手动操作触发的执行、当前任务和实时日志。':copy[props.section].description):copy[props.section].description)
interface RunExecutionGroup{id:string;executionGroup:true;release_plan_id:string;release_plan_execution_id:string;created_at:string;runs:Run[]}
type RunListEntry=Run|RunExecutionGroup
const runListEntries=computed<RunListEntry[]>(()=>{
 const entries:RunListEntry[]=[],groups=new Map<string,RunExecutionGroup>()
 for(const run of runs.value){
  if(!run.release_plan_id||!run.release_plan_execution_id){entries.push(run);continue}
  const id=`execution:${run.release_plan_id}:${run.release_plan_execution_id}`
  let group=groups.get(id)
  if(!group){
   group={id,executionGroup:true,release_plan_id:run.release_plan_id,release_plan_execution_id:run.release_plan_execution_id,created_at:run.created_at,runs:[]}
   groups.set(id,group);entries.push(group)
  }
  group.runs.push(run)
  if(Date.parse(run.created_at)>Date.parse(group.created_at))group.created_at=run.created_at
 }
 return entries
})
const runPagination=reactive({current:1,pageSize:10,showSizeChanger:true,pageSizeOptions:['10','20','50','100'],showTotal:(total:number)=>`共 ${total} 项执行`})
const expandedRunIDs=ref<string[]>([])
const expandedExecutionIDs=ref<string[]>([])
const expandedRunListKeys=computed(()=>[...expandedExecutionIDs.value,...expandedRunIDs.value])
const runDrawerEntryID=ref('')
const runDrawerEntry=computed(()=>runListEntries.value.find(entry=>entry.id===runDrawerEntryID.value))
const runDrawerOpen=computed({get:()=>Boolean(runDrawerEntryID.value&&runDrawerEntry.value),set:(open:boolean)=>{if(!open){runDrawerEntryID.value='';collapseAllRuns()}}})
const runWorkspace=ref<HTMLElement|null>(null)
const runColumns=[
 {title:'发布计划',key:'release_plan',width:180},
 {title:'发布应用',key:'application',width:150},
 {title:'应用流水线',key:'workflow',width:180},
 {title:'提交 / 版本',key:'version'},
 {title:'状态 / 当前任务',key:'status',width:160},
 {title:'执行时间',key:'created_at',width:180},
 {title:'操作',key:'actions',width:175,fixed:'right' as const},
]
const activeRunCount=computed(()=>runs.value.filter(item=>['running','awaiting_approval'].includes(item.status)).length)
const canReadDeployments=computed(()=>auth.canAny(['deployment.read']))
const canReadContainerLogs=computed(()=>auth.canAny(['cluster.read']))
const canOpenContainerTerminal=computed(()=>auth.canAny(['terminal.open']))
const canUpdateDeployments=computed(()=>auth.canAny(['deployment.update']))
const canControlDeployments=computed(()=>auth.canAny(['deployment.execute']))
function visibleDeploymentRunIDs(){
 if(props.section!=='release-plans'||releaseView.value!=='runs'||!canReadDeployments.value||!runDrawerOpen.value||!runDrawerEntry.value)return []
 return runEntryRecords(runDrawerEntry.value).filter(run=>expandedRunIDs.value.includes(run.id)).map(run=>run.id)
}
function runHasDeployments(runID:string){return canReadDeployments.value&&Boolean(runDeploymentRecords.value[runID]?.length)}
async function loadRunDeploymentRecords(runID:string){
 if(!visibleDeploymentRunIDs().includes(runID))return
 runDeploymentRequests.get(runID)?.abort()
 const controller=new AbortController()
 runDeploymentRequests.set(runID,controller)
 runDeploymentLoading.value[runID]=true
 delete runDeploymentErrors.value[runID]
 const current=()=>runDeploymentRequests.get(runID)===controller&&!controller.signal.aborted&&visibleDeploymentRunIDs().includes(runID)
 try{
  const response=await client.get<{deployments:DeploymentRecord[]}>(`/deployments?pipeline_run_id=${encodeURIComponent(runID)}&limit=200`,{signal:controller.signal,timeout:35_000})
  if(!current())return
  const records=(response.data.deployments||[]).filter(record=>record.pipeline_run_id===runID)
  runDeploymentRecords.value[runID]=records
  const ids=new Set(records.map(record=>record.id))
  deployments.value=[...records,...deployments.value.filter(record=>!ids.has(record.id))]
 }catch(error){
  if(!current())return
  runDeploymentRecords.value[runID]=[]
  runDeploymentErrors.value[runID]=apiErrorMessage(error)
 }finally{
  if(runDeploymentRequests.get(runID)===controller){runDeploymentRequests.delete(runID);delete runDeploymentLoading.value[runID]}
 }
}
async function refreshVisibleRunDeployments(){await Promise.all(visibleDeploymentRunIDs().map(loadRunDeploymentRecords))}
watch(()=>visibleDeploymentRunIDs().join(','),()=>{
 const visible=new Set(visibleDeploymentRunIDs())
 for(const [runID,controller] of runDeploymentRequests)if(!visible.has(runID)){controller.abort();runDeploymentRequests.delete(runID);delete runDeploymentLoading.value[runID]}
 for(const runID of Object.keys(runDeploymentRecords.value))if(!visible.has(runID))delete runDeploymentRecords.value[runID]
 for(const runID of Object.keys(runDeploymentErrors.value))if(!visible.has(runID))delete runDeploymentErrors.value[runID]
 for(const runID of visible)if(!runDeploymentLoading.value[runID]&&!runDeploymentRecords.value[runID])void loadRunDeploymentRecords(runID)
},{immediate:true})
watch(()=>releasePlanResources.open,open=>{if(!open){releasePlanResourcesController?.abort();releasePlanResourcesController=null;releasePlanResources.loading=false}})
onBeforeUnmount(()=>{for(const controller of runDeploymentRequests.values())controller.abort();runDeploymentRequests.clear();releasePlanResourcesController?.abort();releasePlanResourcesController=null})
const activeRows=computed<ResourceRecord[]>(()=>props.section==='applications'?applications.value:props.section==='repositories'?repositories.value:props.section==='image-registries'?registries.value:[])
const editingApplication=computed(()=>applications.value.find(item=>item.id===editingID.value))
const availableWorkflowTemplates=computed(()=>{
 const associatedIDs=new Set((editingApplication.value?.workflows||[]).map(item=>item.workflow_template_id).filter(Boolean))
 return workflowTemplates.value.filter(item=>item.is_active&&!associatedIDs.has(item.id))
})
const repositoryOptions=computed(()=>repositories.value.filter(item=>item.is_active).map(item=>({
 value:item.id,label:item.name,details:[`默认分支：${item.default_branch||'未设置'}`],
})))
const cloneCredentialOptions=computed(()=>credentials.value
 .filter(item=>item.auth_type===repoForm.auth_type&&(item.provider===repoForm.provider||item.provider==='generic'||repoForm.provider==='generic'))
 .map(item=>({value:item.id,label:item.name,details:item.secret_hint?[`凭据提示：${item.secret_hint}`]:[]})))
const apiCredentialOptions=computed(()=>credentials.value
 .filter(item=>item.auth_type==='token'&&item.provider===repoForm.provider)
 .map(item=>({value:item.id,label:item.name,details:item.secret_hint?[`凭据提示：${item.secret_hint}`]:[]})))
const manualSourceOptions=computed(()=>manualSources.value.map(item=>({
 value:item.id,label:item.name,details:item.environment?[`环境：${item.environment}`]:[],
})))
const manualArtifactOptions=computed(()=>manualArtifacts.value.map(artifactSelectOption))
const retryArtifactOptions=computed(()=>(retryOptions.value?.artifacts||[]).map(artifactSelectOption))
const activeResourceID=computed(()=>props.section==='image-registries'&&typeof route.query.registry==='string'?route.query.registry:'')
const activeApplicationID=computed(()=>props.section==='applications'&&typeof route.query.application==='string'?route.query.application:'')
const activeReleasePlanID=computed(()=>props.section==='release-plans'&&typeof route.query.plan==='string'?route.query.plan:'')
const activeColumns=computed(()=>props.section==='applications'?[{key:'name',label:'应用'},{key:'repository',label:'代码仓库'},{key:'workflows',label:'流水线'},{key:'sync_status',label:'代码状态'},{key:'last_checked_at',label:'最近检查'}]:props.section==='repositories'?[{key:'name',label:'名称'},{key:'provider',label:'平台'},{key:'clone_url',label:'Git 地址'},{key:'default_branch',label:'默认分支'},{key:'webhook_enabled',label:'Webhook'},{key:'is_active',label:'状态'}]:props.section==='image-registries'?[{key:'name',label:'名称'},{key:'provider',label:'类型'},{key:'endpoint',label:'地址'},{key:'namespace',label:'命名空间'},{key:'has_credential',label:'凭据'}]:[])

function currentApplicationDeployments(application:Application,relatedRuns:Run[]){
 const runIDs=new Set(relatedRuns.map(item=>item.id))
 const currentPlanIDs=new Set((application.workflows||[]).flatMap(workflow=>workflow.stages.flatMap(stage=>stage.tasks)).filter(task=>task.type==='deploy').map(task=>task.config.deployment_plan_id).filter((id):id is string=>Boolean(id)))
 return selectCurrentDeploymentInstances(application.id,deployments.value,runIDs,currentPlanIDs,deploymentPlans.value)
}
function failedApplicationDeployments(application:Application){
 const runIDs=new Set(runs.value.filter(run=>run.application_id===application.id).map(run=>run.id))
 const planIDs=new Set((application.workflows||[]).flatMap(workflow=>workflow.stages.flatMap(stage=>stage.tasks)).filter(task=>task.type==='deploy').map(task=>task.config.deployment_plan_id).filter((id):id is string=>Boolean(id)))
 return selectLatestFailedDeploymentAttempts(application.id,deployments.value,runIDs,planIDs,deploymentPlans.value)
}

const applicationCards=computed(()=>applications.value.map(application=>{
 const related=runs.value.filter(run=>run.application_id===application.id).sort((left,right)=>Date.parse(right.created_at)-Date.parse(left.created_at))
 const run=related[0]
 const repository=application.repository?.id?application.repository:repositories.value.find(item=>item.id===application.repository_id)
 const workflowSummaries=(application.workflows||[]).map(workflow=>{
  const tasks=workflow.stages.flatMap(stage=>stage.tasks)
  const latestRun=related.find(item=>item.workflow_id===workflow.id)
  return {
   workflow,
   latestRun,
   state:applicationRunStatus(latestRun),
   taskCount:tasks.length,
   buildCount:tasks.filter(task=>task.type==='build').length,
   deployCount:tasks.filter(task=>task.type==='deploy').length,
   source:workflowSourceLabel(workflow),
  }
 })
 const deploymentInstances=currentApplicationDeployments(application,related)
 return {
  application,run,repository,workflowSummaries,deploymentInstances,
  enabledWorkflowCount:workflowSummaries.filter(item=>item.workflow.is_active).length,
  state:applicationRunStatus(run),sync:applicationSyncStatus(application.sync_status),
 }
}))
type ApplicationCard=(typeof applicationCards.value)[number]
function matchesApplicationFilter(card:ApplicationCard,filter:ApplicationFilter){
 if(filter==='running')return card.state.live
 if(filter==='attention')return card.state.tone==='danger'||card.sync.tone==='danger'||card.sync.tone==='warning'
 if(filter==='inactive')return !card.application.is_active
 return true
}
const searchedApplicationCards=computed(()=>{
 const query=applicationSearch.value.trim().toLocaleLowerCase()
 return applicationCards.value.filter(card=>!query||[card.application.name,card.application.description,card.repository?.name,...card.workflowSummaries.map(item=>item.workflow.name)].some(value=>value?.toLocaleLowerCase().includes(query)))
})
const applicationFilters=computed(()=>(['all','running','attention','inactive'] as ApplicationFilter[]).map(key=>({key,label:t(`applicationWorkspace.filters.${key}`),count:searchedApplicationCards.value.filter(card=>matchesApplicationFilter(card,key)).length})))
const visibleApplicationCards=computed(()=>searchedApplicationCards.value.filter(card=>matchesApplicationFilter(card,applicationFilter.value)))
const applicationDrawerID=ref('')
const applicationDrawerCard=computed(()=>applicationCards.value.find(card=>card.application.id===applicationDrawerID.value))
const applicationDrawerOpen=computed({get:()=>Boolean(applicationDrawerCard.value&&applicationDrawerID.value),set:(value:boolean)=>{if(!value)closeApplicationDetails()}})
const applicationPageState=reactive({current:1,pageSize:10})
const applicationPagination=computed(()=>({...applicationPageState,showSizeChanger:true,pageSizeOptions:['10','20','50','100'],showTotal:(count:number)=>t('planList.total',{count})}))
function changeApplicationPage(page:{current?:number;pageSize?:number}){const size=page.pageSize||applicationPageState.pageSize;applicationPageState.current=size===applicationPageState.pageSize?page.current||1:1;applicationPageState.pageSize=size}
function closeApplicationDetails(){expandedApplications.value={};applicationDrawerID.value=''}
watch(()=>visibleApplicationCards.value.map(card=>card.application.id).join(','),()=>{applicationPageState.current=Math.min(applicationPageState.current,Math.max(1,Math.ceil(visibleApplicationCards.value.length/applicationPageState.pageSize)))})
watch([applicationSearch,applicationFilter],()=>{applicationPageState.current=1})
watch(()=>applicationCards.value.map(card=>card.application.id).join(','),()=>{if(applicationDrawerID.value&&!applicationDrawerCard.value)closeApplicationDetails()})
const expandedApplicationIDs=computed(()=>visibleApplicationCards.value.filter(card=>applicationDetailsOpen(card.application.id)).map(card=>card.application.id))
const applicationColumns=computed(()=>[
 {key:'application',title:t('applicationWorkspace.columns.application'),width:230},
 {key:'repository',title:t('applicationCard.repository'),width:180},
 {key:'run',title:t('applicationCard.latestRun'),width:380},
 {key:'status',title:t('applicationCard.currentNode'),width:150},
 {key:'sync',title:t('applicationCard.codeStatus'),width:125},
 {key:'resources',title:t('applicationWorkspace.columns.resources'),width:195},
 {key:'actions',title:t('applicationWorkspace.columns.actions'),width:225,fixed:'right' as const,align:'right' as const},
])
const applicationWorkflowColumns=computed(()=>[
 {key:'name',title:t('applicationCard.workflow'),width:270},
 {key:'enabled',title:t('applicationWorkspace.columns.status'),width:85},
 {key:'source',title:t('applicationWorkspace.columns.source'),width:265},
 {key:'template',title:t('applicationWorkspace.columns.template'),width:195},
 {key:'tasks',title:t('applicationWorkspace.columns.tasks'),width:210},
 {key:'status',title:t('applicationCard.latestRun'),width:100},
 {key:'time',title:t('applicationWorkspace.columns.runTime'),width:175},
])
function applicationTableRow(card:ApplicationCard){return {id:`application-${card.application.id}`}}
function applicationTableRowClass(card:ApplicationCard){return ['apps-table-row',applicationDetailsOpen(card.application.id)?'is-expanded':'',activeApplicationID.value===card.application.id?'is-linked':''].join(' ')}
function resetApplicationFilters(){applicationSearch.value='';applicationFilter.value='all'}
const currentDeploymentRecords=computed(()=>applicationCards.value.flatMap(card=>card.deploymentInstances))
const currentDeploymentIDs=computed(()=>new Set(currentDeploymentRecords.value.map(record=>record.id)))
const staleDeploymentIDs=new Set<string>()

function clearStaleDeploymentState(){
 const ids=currentDeploymentIDs.value
 expandedDeployments.value=retainCurrentDeploymentState(expandedDeployments.value,ids)
 deploymentRuntimeStates.value=retainCurrentDeploymentState(deploymentRuntimeStates.value,ids)
 deploymentRuntimeErrors.value=retainCurrentDeploymentState(deploymentRuntimeErrors.value,ids)
 // 正在进行的请求保留 loading 标记，响应回来时再检查当前实例，避免并发重复请求。
 for(const id of staleDeploymentIDs)if(!ids.has(id))staleDeploymentIDs.delete(id)
}
watch(currentDeploymentIDs,clearStaleDeploymentState)
const releasePlanRunnableCounts=computed<Record<string,number>>(()=>Object.fromEntries(releasePlans.value.map(plan=>[plan.id,releasePlanManualApplicationCount(plan)])))
const releasePlanRunnableApplicationIDs=computed(()=>applications.value.filter(applicationCanManualRelease).map(application=>application.id))

function applicationRunStatus(run?:Run):StatusMeta{
 if(!run)return {tone:'neutral',label:t('applicationCard.runStatus.none'),live:false}
 if(run.stage==='stopping')return {tone:'warning',label:t('pipelineStop.stopping'),live:true}
 if(run.status==='canceled')return {tone:'neutral',label:t('pipelineStop.stopped'),live:false}
 const states:Record<string,StatusMeta>={
  detected:{tone:'info',label:t('applicationCard.runStatus.detected'),live:false},ready:{tone:'warning',label:t('applicationCard.runStatus.ready'),live:true},
  running:{tone:'info',label:t('applicationCard.runStatus.running'),live:true},awaiting_approval:{tone:'warning',label:t('applicationCard.runStatus.awaitingApproval'),live:true},
  succeeded:{tone:'success',label:t('applicationCard.runStatus.succeeded'),live:false},failed:{tone:'danger',label:t('applicationCard.runStatus.failed'),live:false},skipped:{tone:'neutral',label:t('applicationCard.runStatus.skipped'),live:false},
  blocked:{tone:'danger',label:t('applicationCard.runStatus.blocked'),live:false},canceled:{tone:'neutral',label:t('applicationCard.runStatus.canceled'),live:false},
 }
 return states[run.status]||{tone:'neutral',label:run.status,live:false}
}
function validApplicationName(value:string){return /^[a-z]+(?:_[a-z]+)*$/.test(value)}
function applicationSyncStatus(status:string):StatusMeta{
 const states:Record<string,StatusMeta>={
  checking:{tone:'info',label:t('applicationCard.syncStatus.checking'),live:true},synced:{tone:'success',label:t('applicationCard.syncStatus.synced'),live:false},
  changed:{tone:'warning',label:t('applicationCard.syncStatus.changed'),live:false},failed:{tone:'danger',label:t('applicationCard.syncStatus.failed'),live:false},
  idle:{tone:'neutral',label:t('applicationCard.syncStatus.idle'),live:false},
 }
 return states[status]||{tone:'neutral',label:status||t('applicationCard.syncStatus.idle'),live:false}
}
function applicationCurrentNode(run?:Run){
 if(!run)return t('applicationCard.notStarted')
 if(run.current_node_name||run.current_node_id)return run.current_node_name||run.current_node_id
 if(run.status==='succeeded')return t('applicationCard.finished')
 if(['failed','blocked','skipped','canceled'].includes(run.status))return t('applicationCard.stopped')
 return t('applicationCard.notStarted')
}
function workflowSourceLabel(workflow:ApplicationWorkflow){
 const branch=workflow.source.config.branch||'*'
 const events=(workflow.source.config.events||[]).map(event=>t(`applicationCard.trigger.${event}`)).join('、')
 return `分支：${branch}；触发：${events||t('applicationCard.trigger.none')}`
}
function workflowDisplayName(workflow?:ApplicationWorkflow){return workflow?.name||workflow?.workflow_template?.name||t('applicationCard.customWorkflow')}
function workflowOriginLabel(workflow:ApplicationWorkflow){return workflow.workflow_template?'公共流水线':'自定义流水线'}
function deploymentKind(record:DeploymentRecord){
 const kind=record.deployment_plan_kind||record.platform
 return ['docker','compose','kubernetes','script'].includes(kind)?kind:'script'
}
function deploymentKindLabel(record:DeploymentRecord){return t(`applicationCard.deploymentKind.${deploymentKind(record)}`)}
function deploymentInstanceName(record:DeploymentRecord){return record.container_name||record.compose_service||record.workload_name||record.target_name||t('applicationCard.unknownInstance')}
function deploymentImageLabel(record:DeploymentRecord){return record.image_display||record.image||t('applicationCard.noImage')}
function deploymentStatus(record:DeploymentRecord):StatusMeta{
 const states:Record<string,StatusMeta>={
  queued:{tone:'warning',label:t('applicationCard.deploymentStatus.queued'),live:true},
  running:{tone:'info',label:t('applicationCard.deploymentStatus.running'),live:true},
  succeeded:{tone:'success',label:t('applicationCard.deploymentStatus.succeeded'),live:false},
  failed:{tone:'danger',label:'部署失败',live:false},
 }
 return states[record.status]||{tone:'neutral',label:record.status,live:false}
}
function currentDeploymentStatus(record:DeploymentRecord):StatusMeta{
 const runtime=deploymentRuntimeStates.value[record.id]
 if(deploymentRuntimeLoading.value[record.id]&&!deploymentRuntimeStates.value[record.id]&&!deploymentRuntimeErrors.value[record.id])return {tone:'info',label:'检查中',live:true}
 if(deploymentRuntimeErrors.value[record.id])return {tone:'warning',label:'状态不可用',live:false}
 if(runtime){
  if(runtime.kind==='kubernetes'){
   if(runtime.state==='stopped')return {tone:'neutral',label:'已停止',live:false}
   if(runtime.state==='progressing')return {tone:'warning',label:'调整中',live:true}
   if(runtime.running)return {tone:'success',label:'运行中',live:true}
  }
  if(runtime.kind==='docker'||runtime.kind==='compose'){
   if(runtime.running)return {tone:'success',label:'运行中',live:true}
   return {tone:'neutral',label:'已停止',live:false}
  }
 }
 if(deploymentKind(record)!=='docker'||!canReadContainerLogs.value)return deploymentStatus(record)
 if(dockerRuntimeErrors.value[record.runtime_id])return {tone:'warning',label:t('applicationCard.containerState.unavailable'),live:false}
 if(!dockerRuntimeLoaded.value[record.runtime_id])return deploymentStatus(record)
 const container=dockerContainerForDeployment(record)
 if(!container)return {tone:'danger',label:t('applicationCard.containerState.missing'),live:false}
 const states:Record<string,StatusMeta>={
  running:{tone:'success',label:t('applicationCard.containerState.running'),live:true},
  restarting:{tone:'warning',label:t('applicationCard.containerState.restarting'),live:true},
  created:{tone:'warning',label:t('applicationCard.containerState.created'),live:false},
  paused:{tone:'warning',label:t('applicationCard.containerState.paused'),live:false},
  exited:{tone:'danger',label:t('applicationCard.containerState.exited'),live:false},
  dead:{tone:'danger',label:t('applicationCard.containerState.dead'),live:false},
 }
 return states[container.state]||{tone:'neutral',label:t('applicationCard.containerState.unknown'),live:false}
}
function applicationDetailsOpen(applicationID:string){return Boolean(expandedApplications.value[applicationID])}
function applicationDetailTab(applicationID:string){return applicationDetailTabs.value[applicationID]||'workflows'}
function toggleApplicationDetails(card:ApplicationCard,tab:ApplicationDetailTab=applicationDetailTab(card.application.id)){
 const applicationID=card.application.id
 const open=true
 applicationDrawerID.value=applicationID
 applicationDetailTabs.value[applicationID]=tab
 expandedApplications.value={[applicationID]:open}
 if(open&&tab==='deployments'&&canReadContainerLogs.value){
  for(const record of card.deploymentInstances)if(deploymentKind(record)==='docker')void loadDockerRuntime(record.runtime_id,true)
 }
}
async function checkApplicationUpdates(application:Application){
 if(!canExecute.value||checkingApplicationIDs.value[application.id]||application.sync_status==='checking')return
 checkingApplicationIDs.value[application.id]=true
 try{
  await client.post(`/applications/${encodeURIComponent(application.id)}/sync`,undefined,{timeout:35000})
  await refreshApplicationState()
 }catch(error){message.error(apiErrorMessage(error))}
 finally{delete checkingApplicationIDs.value[application.id]}
}
function deploymentDetailsOpen(record:DeploymentRecord){return Boolean(expandedDeployments.value[record.id])}
function lifecycleScriptAvailable(record:DeploymentRecord,action:'restart'|'stop'){
 if(deploymentKind(record)!=='script')return true
 const runtime=deploymentRuntimeStates.value[record.id]
 return Boolean(action==='restart'?runtime?.restart_configured:runtime?.stop_configured)
}
function runtimeActionKey(record:DeploymentRecord,action:string){return `${record.id}:${action}`}
function openDeploymentRollback(record:DeploymentRecord){
 if(!canReadDeployments.value||!canControlDeployments.value||deploymentKind(record)!=='kubernetes')return
 rollbackDialog.sourceID=record.id;rollbackDialog.pipelineRunID=record.pipeline_run_id||'';rollbackDialog.open=true
}
function acceptRollbackRecord(record:RollbackRecord){
 const existing=deployments.value.find(item=>item.id===record.id)
 deployments.value=[{...existing,...record} as DeploymentRecord,...deployments.value.filter(item=>item.id!==record.id)]
 if(releasePlanResources.open&&record.pipeline_run_id===releasePlanResources.pipelineRunID){
  releasePlanResources.records=[{...existing,...record} as DeploymentRecord,...releasePlanResources.records.filter(item=>item.id!==record.id)]
 }
 queueDeliveryRefresh()
}
function completeRollbackRecord(record:RollbackRecord){
 if(record.status==='succeeded'&&props.section==='applications')expandedDeployments.value[record.id]=true
 queueDeliveryRefresh()
}
async function refreshRollbackRecords(){
 const pipelineRunID=rollbackDialog.open?rollbackDialog.pipelineRunID:releasePlanResources.open?releasePlanResources.pipelineRunID:''
 if(!canReadDeployments.value||!pipelineRunID)return
 const response=await client.get<{deployments:DeploymentRecord[]}>(`/deployments?pipeline_run_id=${encodeURIComponent(pipelineRunID)}&limit=200`)
 const records=response.data.deployments||[],ids=new Set(records.map(record=>record.id))
 deployments.value=[...records,...deployments.value.filter(record=>!ids.has(record.id))]
 if(releasePlanResources.open&&pipelineRunID===releasePlanResources.pipelineRunID)releasePlanResources.records=records
}
async function loadDeploymentRuntime(record:DeploymentRecord,force=false){
 if(record.runtime_deleted_at||!currentDeploymentIDs.value.has(record.id)||staleDeploymentIDs.has(record.id)||!canReadDeployments.value||deploymentRuntimeLoading.value[record.id]||(!force&&deploymentRuntimeStates.value[record.id]))return
 deploymentRuntimeLoading.value[record.id]=true
 try{
  const response=await client.get<{runtime:DeploymentRuntimeState}>(`/deployments/${encodeURIComponent(record.id)}/runtime`,{timeout:35_000})
  if(currentDeploymentIDs.value.has(record.id)){
   deploymentRuntimeStates.value[record.id]=response.data.runtime
   delete deploymentRuntimeErrors.value[record.id]
  }
 }catch(error){
  if(!currentDeploymentIDs.value.has(record.id))return
  delete deploymentRuntimeStates.value[record.id]
  deploymentRuntimeErrors.value[record.id]=apiErrorMessage(error)
  const code=isAxiosError(error)?error.response?.data?.code:''
  if(code==='deployment_not_current'||code==='runtime_instance_removed'){
   staleDeploymentIDs.add(record.id)
   // 检查期间可能已完成新部署，重新加载列表，不再请求旧实例。
   void refreshApplicationState()
  }
 }finally{delete deploymentRuntimeLoading.value[record.id]}
}
async function executeDeploymentRuntimeAction(record:DeploymentRecord,action:'restart'|'stop',replicas?:number){
 const key=runtimeActionKey(record,action)
 deploymentRuntimeAction.value=key
 try{
  const response=await client.post<{runtime:DeploymentRuntimeState}>(`/deployments/${encodeURIComponent(record.id)}/runtime/${action}`,replicas===undefined?undefined:{replicas},{timeout:3700_000})
  deploymentRuntimeStates.value[record.id]=response.data.runtime
  delete deploymentRuntimeErrors.value[record.id]
  if(deploymentKind(record)==='docker')await loadDockerRuntime(record.runtime_id,true)
  const actionLabel=action==='restart'?'重启':'停止'
  message.success(`${deploymentInstanceName(record)}已${actionLabel}`)
  if(response.data.runtime.output){lifecycleOutput.open=true;lifecycleOutput.title=`${actionLabel}命令输出：${deploymentInstanceName(record)}`;lifecycleOutput.output=response.data.runtime.output}
 }catch(error){message.error(apiErrorMessage(error))}finally{deploymentRuntimeAction.value=''}
}
function confirmDeploymentRuntimeAction(record:DeploymentRecord,action:'restart'|'stop'){
 if(!lifecycleScriptAvailable(record,action)){message.warning(`请先在该 Shell 部署实例中保存${action==='restart'?'重启':'停止'}命令`);return}
 const actionLabel=action==='restart'?'重启':'停止'
 Modal.confirm({
  title:`确认${actionLabel}${deploymentInstanceName(record)}？`,
  content:deploymentKind(record)==='script'?`将执行该部署实例中已经保存的${actionLabel} Shell 命令。`:`操作当前${deploymentKindLabel(record)}运行资源。`,
  okText:actionLabel,cancelText:'取消',okType:action==='stop'?'danger':'primary',
 onOk:()=>executeDeploymentRuntimeAction(record,action),
 })
}
async function removeDeploymentRuntime(record:DeploymentRecord){
 const key=runtimeActionKey(record,'remove')
 deploymentRuntimeAction.value=key
 try{
  await client.delete(`/deployments/${encodeURIComponent(record.id)}/runtime`,{timeout:3700_000})
  const deletedAt=new Date().toISOString()
  deployments.value=deployments.value.map(item=>item.id===record.id?{...item,runtime_deleted_at:deletedAt}:item)
  delete expandedDeployments.value[record.id]
  delete deploymentRuntimeStates.value[record.id]
  delete deploymentRuntimeErrors.value[record.id]
  delete deploymentRuntimeLoading.value[record.id]
  message.success('容器实例已删除；下一次部署成功后将自动恢复监控')
 }catch(error){message.error(apiErrorMessage(error))}finally{deploymentRuntimeAction.value=''}
}
function confirmDeploymentRuntimeRemoval(record:DeploymentRecord){
 Modal.confirm({
  title:`确认删除容器实例 ${deploymentInstanceName(record)}？`,
  content:'容器会被强制停止并删除，但不会删除镜像和数据卷。删除后不再监控这条实例，直到下一次部署成功。',
  okText:'删除实例',cancelText:'取消',okType:'danger',
  onOk:()=>removeDeploymentRuntime(record),
 })
}
async function openShellControlConfiguration(record:DeploymentRecord){
 shellControlDialog.open=true;shellControlDialog.record=record;shellControlDialog.loading=true
 shellControlDialog.restart_script='';shellControlDialog.stop_script='';shellControlDialog.timeout_seconds=300
 try{
  const response=await client.get<{configuration:ShellControlConfiguration}>(`/deployments/${encodeURIComponent(record.id)}/runtime/configuration`)
  const configuration=response.data.configuration
  shellControlDialog.restart_script=configuration.restart_script||''
  shellControlDialog.stop_script=configuration.stop_script||''
  shellControlDialog.timeout_seconds=configuration.timeout_seconds||300
 }catch(error){message.error(apiErrorMessage(error));shellControlDialog.open=false;shellControlDialog.record=null}finally{shellControlDialog.loading=false}
}
async function saveShellControlConfiguration(){
 const record=shellControlDialog.record
 if(!record)return
 shellControlDialog.saving=true
 try{
  await client.put(`/deployments/${encodeURIComponent(record.id)}/runtime/configuration`,{
   restart_script:shellControlDialog.restart_script,stop_script:shellControlDialog.stop_script,
   timeout_seconds:shellControlDialog.timeout_seconds,
  })
  await loadDeploymentRuntime(record,true)
  message.success('Shell 部署实例运行命令已保存')
  shellControlDialog.open=false;shellControlDialog.record=null
 }catch(error){message.error(apiErrorMessage(error))}finally{shellControlDialog.saving=false}
}
function openScaleDialog(record:DeploymentRecord){
 const runtime=deploymentRuntimeStates.value[record.id]
 scaleDialog.record=record;scaleDialog.replicas=Math.max(0,runtime?.replicas??1);scaleDialog.open=true
}
async function submitScale(){
 const record=scaleDialog.record
 if(!record||!Number.isInteger(scaleDialog.replicas)||scaleDialog.replicas<0||scaleDialog.replicas>1000)return
 scaleDialog.submitting=true
 try{
  const response=await client.post<{runtime:DeploymentRuntimeState}>(`/deployments/${encodeURIComponent(record.id)}/runtime/scale`,{replicas:scaleDialog.replicas},{timeout:3700_000})
  deploymentRuntimeStates.value[record.id]=response.data.runtime
  message.success(`副本数已调整为 ${scaleDialog.replicas}`);scaleDialog.open=false
 }catch(error){message.error(apiErrorMessage(error))}finally{scaleDialog.submitting=false}
}
function normalizeContainerName(value:string){return value.trim().replace(/^\/+/, '')}
function deploymentContainerName(record:DeploymentRecord){return normalizeContainerName(record.container_name||record.workload_name||'')}
function dockerContainerForDeployment(record:DeploymentRecord){
 const name=deploymentContainerName(record)
 if(!name)return undefined
 return (dockerRuntimeContainers.value[record.runtime_id]||[]).find(container=>container.id===name||(container.names||[]).some(item=>normalizeContainerName(item)===name))
}
function dockerContainerState(record:DeploymentRecord){
 if(!canReadContainerLogs.value)return t('applicationCard.containerState.noPermission')
 if(dockerRuntimeErrors.value[record.runtime_id])return t('applicationCard.containerState.unavailable')
 const container=dockerContainerForDeployment(record)
 if(!container)return dockerRuntimeLoaded.value[record.runtime_id]?t('applicationCard.containerState.missing'):t('applicationCard.containerState.unchecked')
 const key=['running','created','restarting','paused','exited','dead'].includes(container.state)?container.state:'unknown'
 return t(`applicationCard.containerState.${key}`)
}
async function loadDockerRuntime(runtimeID:string,force=false,silent=false){
 if(!canReadContainerLogs.value||!runtimeID||(!force&&dockerRuntimeLoaded.value[runtimeID]))return
 const pending=dockerRuntimeRequests.get(runtimeID)
 if(pending){await pending;return}
 if(!silent)dockerRuntimeLoading.value[runtimeID]=true
 const request=(async()=>{
  try{
   const response=await client.get<{containers:DockerContainerRecord[]}>(`/docker/endpoints/${encodeURIComponent(runtimeID)}/containers?all=true`,{timeout:35_000})
   dockerRuntimeContainers.value[runtimeID]=response.data.containers||[]
   dockerRuntimeLoaded.value[runtimeID]=true
   delete dockerRuntimeErrors.value[runtimeID]
  }catch(error){
   if(!silent){dockerRuntimeContainers.value[runtimeID]=[];dockerRuntimeLoaded.value[runtimeID]=false}
   dockerRuntimeErrors.value[runtimeID]=apiErrorMessage(error)
  }
 })()
 dockerRuntimeRequests.set(runtimeID,request)
 try{await request}finally{
  if(dockerRuntimeRequests.get(runtimeID)===request)dockerRuntimeRequests.delete(runtimeID)
  if(!silent)dockerRuntimeLoading.value[runtimeID]=false
 }
}
function toggleDeploymentDetails(record:DeploymentRecord){
 const open=!expandedDeployments.value[record.id]
 expandedDeployments.value[record.id]=open
 if(open)void loadDeploymentRuntime(record)
 if(open&&deploymentKind(record)==='docker'&&canReadContainerLogs.value)void loadDockerRuntime(record.runtime_id)
}
async function openDeploymentLogs(record:DeploymentRecord){
 if(!canReadContainerLogs.value){message.warning('没有读取容器日志的权限');return}
 await loadDockerRuntime(record.runtime_id,true)
 const container=dockerContainerForDeployment(record)
 if(!container){message.warning('未在当前 Docker 运行时中找到对应容器');return}
 const name=deploymentContainerName(record)||container.names?.[0]||container.id
 containerLogs.value={
  open:true,
  title:t('containerLogs.title',{name}),
  path:`/api/v1/docker/endpoints/${encodeURIComponent(record.runtime_id)}/containers/${encodeURIComponent(container.id)}/logs/ws`,
 }
}
async function openDeploymentTerminal(record:DeploymentRecord){
 if(!canReadContainerLogs.value||!canOpenContainerTerminal.value){message.warning('没有打开容器终端的权限');return}
 await loadDockerRuntime(record.runtime_id,true)
 const container=dockerContainerForDeployment(record)
 if(!container){message.warning('未在当前 Docker 运行时中找到对应容器');return}
 if(container.state!=='running'){message.warning('容器当前未运行，不能打开终端');return}
 terminal.value={
  open:true,
  title:`容器终端：${deploymentContainerName(record)||container.names?.[0]||container.id}`,
  path:`/api/v1/terminals/docker/${encodeURIComponent(record.runtime_id)}/containers/${encodeURIComponent(container.id)}/ws`,
 }
}
function workflowSupportsManualRelease(workflow?:PipelineWorkflow){return Boolean(workflow?.is_active&&workflow.source?.type==='trigger'&&workflow.source.config?.events?.includes('manual'))}
function applicationManualWorkflows(application?:Application){return (application?.workflows||[]).filter(workflowSupportsManualRelease)}
function applicationCanManualRelease(application?:Application){return Boolean(application?.is_active&&applicationManualWorkflows(application).length)}
function manualWorkflowUnavailableReason(application?:Application,workflow?:ApplicationWorkflow){
 if(!auth.canAny(['pipeline_run.execute']))return t('manualRun.permissionDenied')
 if(!application)return t('releasePlanExecution.reason.applicationMissing')
 if(!application.is_active)return t('releasePlanExecution.reason.applicationDisabled')
 if(!workflow)return t('manualRun.workflowMissing')
 if(!workflow.is_active)return t('manualRun.workflowDisabled')
 if(!workflowSupportsManualRelease(workflow))return t('releasePlanExecution.reason.manualSourceMissing')
 return ''
}
function releasePlanManualApplicationCount(plan:ReleasePlan){
 return (plan.groups||[]).flatMap(group=>group.applications||[]).filter(item=>applicationCanManualRelease(applications.value.find(application=>application.id===item.application_id))).length
}
function formatApplicationTime(value?:string){return value?new Date(value).toLocaleString(locale.value==='en-US'?'en-US':'zh-CN',{hour12:false}):t('applicationCard.noTime')}
function openApplicationLink(card:(typeof applicationCards.value)[number]){
 const target=card.repository
 if(!target||!auth.canAny(['repository.read']))return
 void router.push('/repositories')
}
function openApplicationWorkflow(applicationID:string,workflowID:string){void router.push(`/pipeline-plans/editor?application=${applicationID}&workflow=${workflowID}`)}
function applicationLatestWorkflowName(card:ApplicationCard){return card.workflowSummaries.find(item=>item.workflow.id===card.run?.workflow_id)?.workflow.name||t('applicationCard.workflow')}
function openApplicationLatestRun(card:ApplicationCard){if(card.run)void router.push({path:'/release-plans',query:{view:'runs',run:card.run.id}})}
function editApplicationWorkflow(applicationID:string,workflowID:string){formOpen.value=false;openApplicationWorkflow(applicationID,workflowID)}
function createImageRegistry(){
 formOpen.value=false
 void router.push({path:'/image-registries',query:{create:'1',return_to:route.fullPath}})
}
function resourceViewHref(path:string,queryKey:string,id:string){return router.resolve({path,query:{[queryKey]:id}}).href}

let requestedRunRequestID='',revealedRunID=''
async function revealRequestedRun(){
 const runID=requestedRunID.value
 if(props.section!=='release-plans'||releaseView.value!=='runs')return
 if(!runID){selectedRunID.value='';expandedRunIDs.value=[];revealedRunID='';return}
 if(revealedRunID===runID)return
 const existing=runs.value.find(item=>item.id===runID)
 if(existing){revealPipelineRun(runID);return}
 if(requestedRunRequestID===runID)return
 requestedRunRequestID=runID
 try{
  const run=(await client.get<{pipeline_run:Run}>(`/pipeline-runs/${encodeURIComponent(runID)}`)).data.pipeline_run
  if(requestedRunID.value!==runID)return
  runs.value=[run,...runs.value.filter(item=>item.id!==run.id)]
  revealPipelineRun(runID)
 }catch(error){
  if(requestedRunID.value===runID)message.error(`无法定位流水线运行：${apiErrorMessage(error)}`)
 }finally{
  if(requestedRunRequestID===runID)requestedRunRequestID=''
 }
}
function revealPipelineRun(runID:string){
 selectedRunID.value=runID
 revealedRunID=runID
 const index=runListEntries.value.findIndex(item=>isRunExecutionGroup(item)?item.runs.some(run=>run.id===runID):item.id===runID)
 if(index>=0){
  const page=Math.floor(index/runPagination.pageSize)+1
  if(page!==runPagination.current){expandedRunIDs.value=[];expandedExecutionIDs.value=[]}
  runPagination.current=page
  const entry=runListEntries.value[index]!
  runDrawerEntryID.value=entry.id
  if(isRunExecutionGroup(entry)&&!expandedExecutionIDs.value.includes(entry.id))expandedExecutionIDs.value.push(entry.id)
 }
 if(!expandedRunIDs.value.includes(runID))expandedRunIDs.value.push(runID)
}
function isRunExecutionGroup(entry:RunListEntry):entry is RunExecutionGroup{return entry.executionGroup===true}
function runEntryRecords(entry:RunListEntry){return isRunExecutionGroup(entry)?entry.runs:[entry]}
function runEntryPlanName(entry:RunListEntry){return runReleasePlanName(runEntryRecords(entry)[0]!)}
function executionApplicationCount(group:RunExecutionGroup){return new Set(group.runs.map(run=>run.application_id)).size}
function executionRunCounts(group:RunExecutionGroup){
 const counts=new Map<string,number>()
 for(const run of group.runs){const label=runStatusLabel(run.status,run.stage);counts.set(label,(counts.get(label)||0)+1)}
 return Array.from(counts,([label,count])=>`${label} ${count}`).join('、')
}
function runListRowClass(entry:RunListEntry){
 return [expandedRunListKeys.value.includes(entry.id)?'run-row-expanded':'',runEntryRecords(entry).some(run=>run.id===selectedRunID.value)?'run-row-current':'',isRunExecutionGroup(entry)?'run-execution-row':''].join(' ')
}
function expandRunListEntry(expanded:boolean,entry:RunListEntry){
 if(expanded)runDrawerEntryID.value=entry.id
 if(!isRunExecutionGroup(entry)){expandPipelineRun(expanded,entry);return}
 if(expanded){
  if(!expandedExecutionIDs.value.includes(entry.id))expandedExecutionIDs.value.push(entry.id)
 }else{
  expandedExecutionIDs.value=expandedExecutionIDs.value.filter(id=>id!==entry.id)
  const childIDs=new Set(entry.runs.map(run=>run.id))
  expandedRunIDs.value=expandedRunIDs.value.filter(id=>!childIDs.has(id))
  if(childIDs.has(selectedRunID.value)){
   selectedRunID.value=expandedRunIDs.value.at(-1)||''
   revealedRunID=selectedRunID.value
   void router.replace({path:'/release-plans',query:{view:'runs',...(selectedRunID.value?{run:selectedRunID.value}:{})}})
  }
 }
}
function expandPipelineRun(expanded:boolean,run:Run){
 if(expanded){
  runDrawerEntryID.value=runListEntries.value.find(entry=>runEntryRecords(entry).some(item=>item.id===run.id))?.id||run.id
  if(!expandedRunIDs.value.includes(run.id))expandedRunIDs.value.push(run.id)
  selectedRunID.value=run.id
 }else{
  expandedRunIDs.value=expandedRunIDs.value.filter(id=>id!==run.id)
  if(selectedRunID.value===run.id)selectedRunID.value=expandedRunIDs.value.at(-1)||''
 }
 revealedRunID=selectedRunID.value
 void router.replace({path:'/release-plans',query:{view:'runs',...(selectedRunID.value?{run:selectedRunID.value}:{})}})
}
async function collapseRunDetail(run:Run){
 expandPipelineRun(false,run)
 await nextTick()
 const row=Array.from(runWorkspace.value?.querySelectorAll<HTMLElement>('.ant-table-row,.run-group-row')||[]).find(item=>item.dataset.rowKey===run.id)
 row?.querySelector<HTMLButtonElement>('.run-row-toggle')?.focus({preventScroll:true})
 row?.scrollIntoView({block:'nearest',behavior:'instant'})
}
function collapseAllRuns(){
 runDrawerEntryID.value=''
 expandedExecutionIDs.value=[]
 expandedRunIDs.value=[]
 selectedRunID.value=''
 revealedRunID=''
 if(requestedRunID.value)void router.replace({path:'/release-plans',query:{view:'runs'}})
}
function changeRunPage(pagination:{current?:number;pageSize?:number}){
 runDrawerEntryID.value=''
 const pageSize=pagination.pageSize||10
 runPagination.current=pageSize!==runPagination.pageSize?1:pagination.current||1
 runPagination.pageSize=pageSize
 expandedExecutionIDs.value=[]
 expandedRunIDs.value=[]
 selectedRunID.value=''
 revealedRunID=''
 if(requestedRunID.value)void router.replace({path:'/release-plans',query:{view:'runs'}})
}

async function refresh(){const locatedRun=runs.value.find(item=>item.id===requestedRunID.value);loading.value=true;try{const requests=await Promise.all([auth.canAny(['application.read'])?client.get<{applications:Application[]}>('/applications'):null,auth.canAny(['repository.read'])?client.get<{repositories:Repository[]}>('/repositories'):null,auth.canAny(['credential.read'])?client.get<{credentials:Credential[]}>('/git-credentials'):null,auth.canAny(['build_plan.read'])?client.get<{build_plans:BuildPlan[]}>('/build-plans'):null,auth.canAny(['image_registry.read'])?client.get<{image_registries:Registry[]}>('/image-registries'):null,auth.canAny(['pipeline_run.read'])?client.get<{pipeline_runs:Run[]}>('/pipeline-runs?limit=200'):null,auth.canAny(['release_plan.read'])?client.get<{release_plans:ReleasePlan[]}>('/release-plans'):null,canReadDeployments.value?client.get<{deployments:DeploymentRecord[]}>('/deployments?limit=200'):null,auth.canAny(['deployment_plan.read'])?client.get<{deployment_plans:DeploymentPlan[]}>('/deployment-plans'):null,auth.canAny(['workflow.read'])?client.get<{workflow_templates:WorkflowTemplate[]}>('/workflow-templates'):null]);applications.value=requests[0]?.data.applications||[];repositories.value=requests[1]?.data.repositories||[];credentials.value=requests[2]?.data.credentials||[];buildPlans.value=requests[3]?.data.build_plans||[];registries.value=requests[4]?.data.image_registries||[];runs.value=requests[5]?.data.pipeline_runs||[];if(locatedRun&&!runs.value.some(item=>item.id===locatedRun.id))runs.value=[locatedRun,...runs.value];releasePlans.value=requests[6]?.data.release_plans||[];deployments.value=requests[7]?.data.deployments||[];deploymentPlans.value=requests[8]?.data.deployment_plans||[];workflowTemplates.value=requests[9]?.data.workflow_templates||[];if(requestedRunID.value)void revealRequestedRun();await refreshVisibleRunDeployments()}catch(error){message.error(apiErrorMessage(error))}finally{loading.value=false}}
let stateRefreshing=false
async function refreshApplicationState(){
 if(stateRefreshing||!auth.canAny([`${sectionPermission.value}.read`]))return
 stateRefreshing=true
 try{
 const [applicationResult,runResult,deploymentResult,deploymentPlanResult]=await Promise.all([auth.canAny(['application.read'])?client.get<{applications:Application[]}>('/applications'):Promise.resolve({data:{applications:[] as Application[]}}),auth.canAny(['pipeline_run.read'])?client.get<{pipeline_runs:Run[]}>('/pipeline-runs?limit=200'):Promise.resolve({data:{pipeline_runs:[] as Run[]}}),canReadDeployments.value?client.get<{deployments:DeploymentRecord[]}>('/deployments?limit=200'):null,auth.canAny(['deployment_plan.read'])?client.get<{deployment_plans:DeploymentPlan[]}>('/deployment-plans'):Promise.resolve({data:{deployment_plans:[] as DeploymentPlan[]}})])
  applications.value=applicationResult.data.applications||[]
  runs.value=runResult.data.pipeline_runs||[]
  if(deploymentResult)deployments.value=deploymentResult.data.deployments||[]
  deploymentPlans.value=deploymentPlanResult.data.deployment_plans||[]
  clearStaleDeploymentState()
 }catch{}finally{stateRefreshing=false}
}
function refreshVisibleDeploymentRuntime(){
 const visibleRecords=applicationCards.value.filter(card=>applicationDetailsOpen(card.application.id)&&applicationDetailTab(card.application.id)==='deployments').flatMap(card=>card.deploymentInstances).filter(record=>expandedDeployments.value[record.id])
 const visibleRuntimeIDs=new Set(visibleRecords.filter(record=>deploymentKind(record)==='docker').map(record=>record.runtime_id).filter(Boolean))
 for(const runtimeID of visibleRuntimeIDs)void loadDockerRuntime(runtimeID,true,true)
 for(const record of visibleRecords)void loadDeploymentRuntime(record,true)
}
async function refreshReleasePlanState(){
 if(stateRefreshing||!auth.canAny([`${sectionPermission.value}.read`]))return
 stateRefreshing=true
 try{
  const [appResult,runResult,planResult]=await Promise.all([
   auth.canAny(['application.read'])?client.get<{applications:Application[]}>('/applications'):Promise.resolve({data:{applications:[] as Application[]}}),
   auth.canAny(['pipeline_run.read'])?client.get<{pipeline_runs:Run[]}>('/pipeline-runs?limit=200'):Promise.resolve({data:{pipeline_runs:[] as Run[]}}),
   client.get<{release_plans:ReleasePlan[]}>('/release-plans'),
  ])
  applications.value=appResult.data.applications||[]
  runs.value=runResult.data.pipeline_runs||[]
  releasePlans.value=planResult.data.release_plans||[]
 }catch{}finally{stateRefreshing=false}
}
async function refreshRunState(){
 if(stateRefreshing||!auth.canAny([`${sectionPermission.value}.read`]))return
 stateRefreshing=true
 try{
  const locatedRun=runs.value.find(item=>item.id===requestedRunID.value)
  runs.value=(await (auth.canAny(['pipeline_run.read'])?client.get<{pipeline_runs:Run[]}>('/pipeline-runs?limit=200'):Promise.resolve({data:{pipeline_runs:[] as Run[]}}))).data.pipeline_runs||[]
  if(locatedRun&&!runs.value.some(item=>item.id===locatedRun.id))runs.value=[locatedRun,...runs.value]
  if(requestedRunID.value)void revealRequestedRun()
  await refreshVisibleRunDeployments()
  if(rollbackDialog.open||releasePlanResources.open)await refreshRollbackRecords()
 }catch{}finally{stateRefreshing=false}
}
function formatVariableText(values?:Record<string,string>){return Object.entries(values||{}).map(([name,value])=>`${name}=${value}`).join('\n')}
function parseVariableText(source:string,label:string){
 const values:Record<string,string>={}
 const reservedEnvironmentNames=new Set(['CI','HOME','TMPDIR','EDO_PIPELINE_RUN_ID','EDO_APPLICATION_ID','EDO_GIT_REF','EDO_COMMIT_SHA','EDO_TARGET_PLATFORM','EDO_TARGET_ARCH','GOOS','GOARCH'])
 const lines=source.split(/\r?\n/)
 for(let index=0;index<lines.length;index+=1){
  const line=lines[index].trim()
  if(!line||line.startsWith('#'))continue
  const separator=line.indexOf('=')
  const name=separator<0?'':line.slice(0,separator).trim()
  const value=separator<0?'':line.slice(separator+1)
  if(!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(name))return {values,error:`${label}第 ${index+1} 行格式无效，请使用 KEY=value`}
  if(Object.hasOwn(values,name))return {values,error:`${label}第 ${index+1} 行的 ${name} 重复`}
  if(label==='环境变量'&&reservedEnvironmentNames.has(name))return {values,error:`${label}第 ${index+1} 行使用了系统保留变量 ${name}`}
  values[name]=value
 }
 if(Object.keys(values).length>100)return {values,error:`${label}最多配置 100 项`}
 return {values,error:''}
}
function resetForms(){
	 credentialCreation.open=false
	 creatingApplicationRepository.value=false
	 editingID.value='';selectedWorkflowTemplateID.value='';workflowRemovalID.value='';registryTested.value=false;buildImageDestination.value='local';buildSaveArtifact.value=false;buildArgsText.value='';buildEnvironmentText.value=''
 Object.assign(appForm,{name:'',description:'',repository_id:'',workflow_template_id:'',poll_interval_seconds:DEFAULT_APPLICATION_POLL_INTERVAL})
	 Object.assign(repoForm,{name:'',provider:'github',clone_url:'',default_branch:'main',auth_type:'none',username:'',credential_id:'',api_credential_id:'',webhook_enabled:false,allow_insecure_http:false})
	 Object.assign(buildForm,{name:'',kind:'dockerfile',description:'',script:'',dockerfile_path:'Dockerfile',context_path:'.',working_directory:'.',artifact_path:'',runtime_image:DEFAULT_RUNTIME_IMAGE,image_registry_id:'',target_stage:'',pull:true,cache_enabled:true,timeout_seconds:1800})
 Object.assign(registryForm,{name:'',provider:'generic',endpoint:'https://',namespace:'',username:'',credential:'',allow_insecure_http:false})
}
function create(){
 resetForms()
 if(props.section==='release-plans'){
  releasePlanEditorPlan.value={
   id:'',name:'',version:'',description:'',status:'draft',is_active:true,created_at:'',
	   groups:[{id:'',name:'应用列表',mode:'parallel',failure_policy:'stop',sort_order:0,dependencies:[],applications:[]}],
  }
  releasePlanEditorOpen.value=true
  return
 }
 if(props.section==='applications'&&!applications.value.length){
  const activeRepositories=repositories.value.filter(item=>item.is_active)
  if(activeRepositories.length===1){
   appForm.repository_id=activeRepositories[0].id
   if(validApplicationName(activeRepositories[0].name))appForm.name=activeRepositories[0].name
  }
 }
 formOpen.value=true
}
function createApplicationRepository(){
 if(saving.value||!auth.canAny(['repository.create']))return
 // 只切换仓库子表单，应用草稿和编辑中的应用 ID 保持不变。
 Object.assign(repoForm,{name:'',provider:'github',clone_url:'',default_branch:'main',auth_type:'none',username:'',credential_id:'',api_credential_id:'',webhook_enabled:false,allow_insecure_http:false})
 creatingApplicationRepository.value=true
}
function closeForm(){
 if(saving.value||testing.value)return
 if(credentialCreation.open){credentialCreation.open=false;return}
 if(creatingApplicationRepository.value)creatingApplicationRepository.value=false
 else formOpen.value=false
}
function createRepositoryCredential(target:'git'|'api'){
 if(saving.value||testing.value||formSection.value!=='repositories'||!auth.canAny(['credential.create']))return
 if(target==='git'&&repoForm.auth_type==='none')return
 if(target==='api'&&repoForm.provider==='generic')return
 Object.assign(credentialCreation,{open:true,target,provider:repoForm.provider,authType:target==='api'?'token':repoForm.auth_type})
}
function repositoryCredentialSaved(credential:Credential){
 credentials.value=[credential,...credentials.value.filter(item=>item.id!==credential.id)]
 // 保存过程中若仓库上下文已关闭或改变，只更新列表，不改写其他仓库草稿。
 if(!credentialCreation.open||formSection.value!=='repositories'||credential.provider!==repoForm.provider)return
 if(credentialCreation.target==='api'){
  if(credential.auth_type==='token')repoForm.api_credential_id=credential.id
 }else if(credential.auth_type===repoForm.auth_type){
  repoForm.credential_id=credential.id
 }
}
function edit(row:ResourceRecord){
 editingID.value=String(row.id)
 selectedWorkflowTemplateID.value=''
 workflowRemovalID.value=''
 if(props.section==='applications'){
  const item=row as Application
  Object.assign(appForm,{name:item.name,description:item.description||'',repository_id:item.repository_id,workflow_template_id:'',poll_interval_seconds:item.poll_interval_seconds||DEFAULT_APPLICATION_POLL_INTERVAL})
 }
 if(props.section==='repositories'){
  const item=row as Repository
	  Object.assign(repoForm,{name:item.name,provider:item.provider,clone_url:item.clone_url,default_branch:item.default_branch,auth_type:item.auth_type,username:item.username||'',credential_id:item.credential_id||'',api_credential_id:item.api_credential_id||'',webhook_enabled:item.webhook_enabled,allow_insecure_http:item.allow_insecure_http})
 }
 if(props.section==='build-plans'){
  const item=row as BuildPlan
	  Object.assign(buildForm,{name:item.name,kind:item.kind,description:item.description||'',script:item.script||'',dockerfile_path:item.dockerfile_path||'Dockerfile',context_path:item.context_path||'.',working_directory:item.working_directory||'.',artifact_path:item.artifact_path||'',runtime_image:item.runtime_image||DEFAULT_RUNTIME_IMAGE,image_registry_id:item.image_registry_id||'',target_stage:item.target_stage||'',pull:item.pull!==false,cache_enabled:item.cache_enabled!==false,timeout_seconds:item.timeout_seconds||1800})
	  buildSaveArtifact.value=item.kind==='script'&&Boolean(item.artifact_path)
  buildImageDestination.value=item.image_registry_id?'registry':'local'
  buildArgsText.value=formatVariableText(item.build_args)
  buildEnvironmentText.value=formatVariableText(item.environment_variables)
 }
 formOpen.value=true
}
async function save(){
 if(saving.value||testing.value||credentialCreation.open||!canSaveCurrent.value)return
 saving.value=true
 try{
  const section=formSection.value
  const resourceID=formEditingID.value
  const creatingRepository=section==='repositories'&&!resourceID
  let endpoint='',payload:unknown={},method:'post'|'put'='post'
  let pendingWorkflowTemplateID=''
  if(section==='applications'){
   if(!validApplicationName(appForm.name)){message.error('应用名必须以小写英文字母开头，只能使用小写英文字母和单个下划线');return}
   if(!appForm.repository_id){message.error('请选择代码仓库');return}
   endpoint=editingID.value?`/applications/${editingID.value}`:'/applications'
   payload=editingID.value?{name:appForm.name,description:appForm.description,repository_id:appForm.repository_id,poll_interval_seconds:appForm.poll_interval_seconds}:{...appForm,workflow_template_id:appForm.workflow_template_id||'',empty_workflow:!appForm.workflow_template_id}
   pendingWorkflowTemplateID=editingID.value?selectedWorkflowTemplateID.value:''
   method=editingID.value?'put':'post'
  }
  if(section==='repositories'){
   endpoint=resourceID?`/repositories/${resourceID}`:'/repositories'
	   payload={...repoForm,credential_id:repoForm.auth_type==='none'?null:repoForm.credential_id||null,api_credential_id:repoForm.provider==='generic'?'':repoForm.api_credential_id||'',regenerate_webhook:false}
   method=resourceID?'put':'post'
  }
  if(props.section==='build-plans'){
   if(!buildForm.name.trim()){message.error('请输入构建方案名称');return}
   if(buildForm.kind==='dockerfile'&&(!buildForm.dockerfile_path.trim()||!buildForm.context_path.trim())){message.error('请填写 Dockerfile 路径和构建上下文');return}
   if(buildForm.kind==='dockerfile'&&buildImageDestination.value==='registry'&&!buildForm.image_registry_id){message.error('请选择镜像仓库');return}
	   if(buildForm.kind==='script'&&(!buildForm.script.trim()||!buildForm.runtime_image.trim())){message.error('请填写构建脚本和运行镜像');return}
	   if(buildForm.kind==='script'&&buildSaveArtifact.value&&!buildForm.artifact_path.trim()){message.error('开启保存产物后，请填写产物路径');return}
   const buildArgs=parseVariableText(buildArgsText.value,'构建参数')
   const environmentVariables=parseVariableText(buildEnvironmentText.value,'环境变量')
   if(buildArgs.error||environmentVariables.error){message.error(buildArgs.error||environmentVariables.error);return}
   endpoint=editingID.value?`/build-plans/${editingID.value}`:'/build-plans'
   payload={
    ...buildForm,
    dockerfile_path:buildForm.kind==='dockerfile'?buildForm.dockerfile_path:'',
    working_directory:buildForm.kind==='script'?buildForm.working_directory:'.',
	    artifact_path:buildForm.kind==='script'&&buildSaveArtifact.value?buildForm.artifact_path:'',
    runtime_image:buildForm.kind==='script'?buildForm.runtime_image:'',
    image_registry_id:buildForm.kind==='dockerfile'&&buildImageDestination.value==='registry'?buildForm.image_registry_id:'',
    target_stage:buildForm.kind==='dockerfile'?buildForm.target_stage:'',
    build_args:buildForm.kind==='dockerfile'?buildArgs.values:{},
    environment_variables:buildForm.kind==='script'?environmentVariables.values:{},
   }
   method=editingID.value?'put':'post'
  }
  if(props.section==='image-registries'){
   if(!registryTested.value){message.error('请先测试镜像仓库登录');return}
   endpoint='/image-registries';payload=registryRequestPayload()
  }
  const creatingDeliveryApplication=section==='applications'&&!editingID.value&&!appForm.workflow_template_id
  const response=await client[method](endpoint,payload)
  if(section==='applications'&&editingID.value&&pendingWorkflowTemplateID){
   await client.post(`/applications/${editingID.value}/workflows`,{workflow_template_id:pendingWorkflowTemplateID})
  }
  const savedRepository=section==='repositories'?(response.data as {repository?:Repository}).repository:undefined
  const savedWebhookAPI=creatingRepository&&repoForm.webhook_enabled&&savedRepository?.webhook_url
   ?new URL(savedRepository.webhook_url,window.location.origin).toString()
   :''
  if(creatingApplicationRepository.value&&savedRepository){
   repositories.value=[savedRepository,...repositories.value.filter(item=>item.id!==savedRepository.id)]
   appForm.repository_id=savedRepository.id
   creatingApplicationRepository.value=false
   message.success(t('applicationForm.repositoryCreated'))
  }else{
   message.success('配置已保存');formOpen.value=false;resetForms();await refresh()
  }
  if(creatingDeliveryApplication&&canConfigureDelivery.value){configureDelivery((response.data as {application:Application}).application.id)}
  if(savedWebhookAPI)Modal.info({title:'Webhook 已启用',width:650,okText:'关闭',content:()=>`Webhook API：${savedWebhookAPI}`})
 }catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}
}

async function copyRepositoryWebhookAPI(){
 if(!repositoryWebhookAPI.value)return
 try{
  await navigator.clipboard.writeText(repositoryWebhookAPI.value)
  message.success('Webhook API 已复制')
 }catch{
  message.error('复制失败，请手动复制')
 }
}
function removeApplicationWorkflow(workflow:ApplicationWorkflow){
 if(!editingID.value||workflowRemovalID.value)return
 const templateLinked=Boolean(workflow.workflow_template_id)
 Modal.confirm({
  title:`${templateLinked?'解除关联并删除':'删除'}流水线“${workflowDisplayName(workflow)}”？`,
  content:'只删除该应用下的流水线定义，应用、公共流水线和历史运行记录不会被删除。正在执行的流水线不能删除。',
  okText:templateLinked?'解除并删除':'删除流水线',cancelText:'取消',okType:'danger',
  async onOk(){
   workflowRemovalID.value=workflow.id
   try{
    await client.delete(`/applications/${editingID.value}/workflows/${workflow.id}`)
    await refresh()
    message.success(templateLinked?'流水线关联已解除':'流水线已删除')
   }catch(error){message.error(apiErrorMessage(error))}finally{workflowRemovalID.value=''}
  },
 })
}
function registryProviderLabel(provider:RegistryProvider){return ({harbor:'Harbor',docker_hub:'Docker Hub',generic:'通用 Registry'} as const)[provider]||provider}
function registryEndpointLabel(registry:Registry){return registry.provider==='docker_hub'?'docker.io（系统固定）':registry.endpoint}
function registryRequestPayload(){return {...registryForm,endpoint:dockerHubRegistry.value?'':registryForm.endpoint,allow_insecure_http:dockerHubRegistry.value?false:registryForm.allow_insecure_http,credential:registryForm.credential||null}}
async function loadArtifacts(){
 const buildPlanID=selectedBuildPlanID.value
 const applicationID=artifactApplicationID.value
 if(props.section!=='build-plans'||buildPlanView.value!=='artifacts'||!buildPlanID||!auth.canAny(['artifact.read'])){artifacts.value=[];return}
 const requestKey=`${buildPlanID}:${applicationID}`
 artifactLoading.value=true
 try{
  const result=await client.get<{artifacts:Artifact[]}>(`/build-plans/${buildPlanID}/artifacts`,{params:applicationID?{application_id:applicationID}:undefined})
  if(`${selectedBuildPlanID.value}:${artifactApplicationID.value}`===requestKey&&buildPlanView.value==='artifacts')artifacts.value=result.data.artifacts||[]
 }catch(error){
  if(`${selectedBuildPlanID.value}:${artifactApplicationID.value}`===requestKey&&buildPlanView.value==='artifacts'){artifacts.value=[];message.error(apiErrorMessage(error))}
 }finally{if(`${selectedBuildPlanID.value}:${artifactApplicationID.value}`===requestKey)artifactLoading.value=false}
}
async function uploadArtifact(file:File){
 if(!selectedBuildPlanID.value||!artifactApplicationID.value||artifactUploading.value)return
 artifactUploading.value=true
 try{
  const body=new FormData()
  body.append('file',file,file.name)
  await client.post(`/build-plans/${selectedBuildPlanID.value}/applications/${artifactApplicationID.value}/artifacts/upload`,body,{timeout:120_000})
  message.success('制品上传完成，已记录到当前构建方案')
  await loadArtifacts()
 }catch(error){message.error(apiErrorMessage(error))}finally{artifactUploading.value=false}
}
async function downloadArtifact(item:Pick<Artifact,'id'|'name'|'original_name'|'storage_kind'|'status'>){
 if(item.storage_kind!=='local_file'||item.status!=='available')return
 artifactDownloadingID.value=item.id
 try{
  const response=await client.get<Blob>(`/artifacts/${item.id}/download`,{responseType:'blob',timeout:120_000})
  const url=URL.createObjectURL(response.data)
  const link=document.createElement('a')
  link.href=url;link.download=item.original_name||item.name;link.click()
  window.setTimeout(()=>URL.revokeObjectURL(url),1000)
 }catch(error){message.error(apiErrorMessage(error))}finally{artifactDownloadingID.value=''}
}
function confirmBuildPlanStatus(plan:Pick<BuildPlan,'id'|'name'|'is_active'>){
 const enabling=!plan.is_active
 Modal.confirm({
  title:`${enabling?'启用':'停用'}构建方案“${plan.name}”？`,
  content:enabling?'启用后，流水线构建任务可以选择并执行该方案。':'停用后，引用该方案的流水线任务将不能开始新的构建；历史运行不受影响。',
  okText:enabling?'启用':'停用',cancelText:'取消',
  onOk:async()=>{
   resourceMutationID.value=plan.id
   try{await client.patch(`/build-plans/${plan.id}/status`,{active:enabling});message.success(`构建方案已${enabling?'启用':'停用'}`);await refresh()}
   catch(error){message.error(apiErrorMessage(error));throw error}
   finally{resourceMutationID.value=''}
  },
 })
}
function confirmDeleteBuildPlan(plan:Pick<BuildPlan,'id'|'name'>){
 Modal.confirm({
  title:`删除构建方案“${plan.name}”？`,content:'删除后将从构建方案列表隐藏，历史流水线记录继续保留。仍被流水线任务引用时系统会拒绝删除。',
  okText:'删除',okType:'danger',cancelText:'取消',
  onOk:async()=>{
   resourceMutationID.value=plan.id
   try{await client.delete(`/build-plans/${plan.id}`);message.success('构建方案已删除');await refresh()}
   catch(error){message.error(apiErrorMessage(error));throw error}
   finally{resourceMutationID.value=''}
  },
 })
}
async function testRepository(){if(testing.value||saving.value)return;testing.value=true;try{const payload={...repoForm,credential_id:repoForm.auth_type==='none'?null:repoForm.credential_id||null,api_credential_id:repoForm.provider==='generic'?'':repoForm.api_credential_id||'',credential:null,regenerate_webhook:false};const result=formEditingID.value?await client.post<RefResult>(`/repositories/${formEditingID.value}/test`,undefined,{timeout:35000}):await client.post<RefResult>('/repositories/test',payload,{timeout:35000});message.success(`连接成功：${result.data.branches?.length||0} 个分支，${result.data.tags?.length||0} 个标签`)}catch(error){message.error(apiErrorMessage(error))}finally{testing.value=false}}
async function testStoredRepository(repository:Repository){repositoryTestingID.value=repository.id;try{const result=await client.post<RefResult>(`/repositories/${repository.id}/test`,undefined,{timeout:35000});message.success(`连接成功：${result.data.branches?.length||0} 个分支，${result.data.tags?.length||0} 个标签`);await refresh()}catch(error){message.error(apiErrorMessage(error))}finally{repositoryTestingID.value=''}}
async function testRegistry(){testing.value=true;registryTested.value=false;try{await client.post('/image-registries/test',registryRequestPayload(),{timeout:35000});registryTested.value=true;message.success('镜像仓库登录成功')}catch(error){message.error(apiErrorMessage(error))}finally{testing.value=false}}
async function action(path:string){try{await client.post(path,undefined,{timeout:35000});await refresh()}catch(error){message.error(apiErrorMessage(error))}}
function canStopRun(run:Run){return ['detected','ready','blocked','awaiting_approval','running'].includes(run.status)}
function confirmStopRun(run:Run){
 if(!auth.canAny(['pipeline_run.execute'])||!canStopRun(run)||run.stage==='stopping'||stoppingRunID.value)return
 const runID=run.id
 Modal.confirm({
  title:t('pipelineStop.title'),content:t('pipelineStop.hint'),okText:t('pipelineStop.confirm'),cancelText:t('pipelineStop.cancel'),okType:'danger',
  onOk:async()=>{
   if(stoppingRunID.value)return
   stoppingRunID.value=runID
   try{
    const stopped=(await client.post<{pipeline_run:Run}>(`/pipeline-runs/${encodeURIComponent(runID)}/stop`)).data.pipeline_run
    runs.value=runs.value.map(item=>item.id===runID?stopped:item)
    message.success(t(stopped.stage==='stopping'?'pipelineStop.accepted':'pipelineStop.stopped'))
    queueDeliveryRefresh()
   }catch(error){message.error(apiErrorMessage(error));throw error}
   finally{stoppingRunID.value=''}
  },
 })
}
function resetRetry(){retryOpen.value=false;retryLoading.value=false;retrySubmitting.value=false;retryRunID.value='';retryOptions.value=null;retryMode.value='rebuild';retryArtifactID.value=''}
async function openRetry(run:Run){
 resetRetry();retryOpen.value=true;retryLoading.value=true;retryRunID.value=run.id
 try{
  retryOptions.value=(await client.get<RetryRunOptions>(`/pipeline-runs/${run.id}/retry-options`,{timeout:35000})).data
  retryMode.value=retryOptions.value.artifacts.length?'artifact':'rebuild'
  retryArtifactID.value=retryOptions.value.artifacts.length===1?retryOptions.value.artifacts[0].id:''
 }catch(error){message.error(apiErrorMessage(error));resetRetry()}finally{retryLoading.value=false}
}
async function executeRetry(){
 if(!retryRunID.value||!retryOptions.value||(retryMode.value==='artifact'&&!retryArtifactID.value))return
 retrySubmitting.value=true
 try{
  const retried=(await client.post<{pipeline_run:Run}>(`/pipeline-runs/${retryRunID.value}/retry`,retryMode.value==='artifact'?{artifact_id:retryArtifactID.value}:{},{timeout:35000})).data.pipeline_run
  message.success(retryMode.value==='artifact'?'已使用原运行制品创建重试':'已按原固定版本重新构建')
  resetRetry();await refresh()
  if(!runs.value.some(item=>item.id===retried.id))runs.value=[retried,...runs.value]
  revealPipelineRun(retried.id)
  void router.replace({path:'/release-plans',query:{view:'runs',run:retried.id}})
 }catch(error){message.error(apiErrorMessage(error))}finally{retrySubmitting.value=false}
}
function openLogs(run:Run){log.value={open:true,runID:run.id,title:`流水线日志：${applications.value.find(item=>item.id===run.application_id)?.name||'应用'}`,status:run.status}}
function resetManualFlow(){
 commitOpen.value=false
 manualApplicationID.value=''
 manualWorkflowID.value=''
	 commitOptions.value=[]
	 manualArtifacts.value=[]
	 manualExecutionMode.value='code'
	 selectedArtifactID.value=''
	 manualReferenceError.value=''
	 selectedRef.value=''
 selectedSource.value=''
 manualSources.value=[]
 currentRun.value=null
 currentRunSelectionKey.value=''
}
const manualApplicationOptions=computed(()=>applications.value.filter(applicationCanManualRelease).map(application=>({value:application.id,label:application.name})))
const manualWorkflowOptions=computed(()=>applicationManualWorkflows(applications.value.find(application=>application.id===manualPicker.applicationID)).map(workflow=>({value:workflow.id,label:workflowDisplayName(workflow)})))
function openManualPicker(){
 if(!auth.canAny(['pipeline_run.execute'])||!auth.canAny(['application.read'])||saving.value)return
 resetManualFlow();manualPicker.applicationID='';manualPicker.workflowID='';manualPicker.open=true
}
function changeManualApplication(applicationID:string){manualPicker.applicationID=applicationID;manualPicker.workflowID=''}
async function continueManualPicker(){
 if(!manualPicker.applicationID||!manualPicker.workflowID)return
 await openManual(manualPicker.applicationID,manualPicker.workflowID)
 if(commitOpen.value)manualPicker.open=false
}
async function openManual(applicationID:string,workflowID:string){
 if(saving.value)return
 const application=applications.value.find(item=>item.id===applicationID)
 const workflow=application?.workflows.find(item=>item.id===workflowID)
 const reason=manualWorkflowUnavailableReason(application,workflow)
 if(reason){message.warning(reason);return}
 resetManualFlow()
 manualApplicationID.value=applicationID
 manualWorkflowID.value=workflowID
 await nextManual()
}
async function nextManual(){
	if(!manualApplicationID.value||!manualWorkflowID.value||saving.value)return
 const applicationID=manualApplicationID.value,workflowID=manualWorkflowID.value
 saving.value=true
 try{
			const data=(await client.get<RefResult>(`/applications/${applicationID}/workflows/${workflowID}/repository-refs`,{timeout:35000})).data
  if(applicationID!==manualApplicationID.value||workflowID!==manualWorkflowID.value)return
	  commitOptions.value=[...(data.branches||[]).map(item=>({ref:`refs/heads/${item.name}`,name:item.name,sha:item.sha,kind:'branch' as const})),...(data.tags||[]).map(item=>({ref:`refs/tags/${item.name}`,name:item.name,sha:item.sha,kind:'tag' as const}))]
	  manualArtifacts.value=data.artifacts||[]
	  manualReferenceError.value=data.reference_error||''
	  manualSources.value=(data.manual_sources||[]).map(item=>({id:item.id,name:item.name,environment:item.environment}))
  const application=applications.value.find(item=>item.id===manualApplicationID.value)
  selectedRef.value=commitOptions.value.find(item=>item.kind==='branch'&&item.name===application?.repository?.default_branch)?.ref||commitOptions.value.find(item=>item.kind==='branch')?.ref||commitOptions.value[0]?.ref||''
	  selectedSource.value=manualSources.value[0]?.id||''
	  manualExecutionMode.value=commitOptions.value.length?'code':'artifact'
	  selectedArtifactID.value=manualArtifacts.value.length===1?manualArtifacts.value[0].id:''
	  if((!commitOptions.value.length&&!manualArtifacts.value.length)||!manualSources.value.length){message.warning(t('manualRun.noVersionOrSource'));return}
  commitOpen.value=true
 }catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}
}
async function executeCommit(){
 if(saving.value)return
 const application=applications.value.find(item=>item.id===manualApplicationID.value)
 const reason=manualWorkflowUnavailableReason(application,application?.workflows.find(item=>item.id===manualWorkflowID.value))
 if(reason){message.warning(reason);return}
	 const selected=commitOptions.value.find(item=>item.ref===selectedRef.value)
	 const selectedArtifact=manualArtifacts.value.find(item=>item.id===selectedArtifactID.value)
		if(!selectedSource.value||!manualApplicationID.value||!manualWorkflowID.value||
		 (manualExecutionMode.value==='code'&&!selected)||(manualExecutionMode.value==='artifact'&&!selectedArtifact))return
	 saving.value=true
	 try{
			const selectionKey=[manualApplicationID.value,manualWorkflowID.value,manualExecutionMode.value,selected?.ref||'',selected?.sha||'',selectedArtifact?.id||'',selectedSource.value].join('\u0000')
  if(!currentRun.value||currentRunSelectionKey.value!==selectionKey){
		 currentRun.value=(await client.post<{pipeline_run:Run}>(`/applications/${manualApplicationID.value}/workflows/${manualWorkflowID.value}/pipeline-runs`)).data.pipeline_run
   currentRunSelectionKey.value=selectionKey
  }
	  await client.post(`/pipeline-runs/${currentRun.value.id}/execute`,manualExecutionMode.value==='artifact'?{
	   artifact_id:selectedArtifact?.id,source_node_id:selectedSource.value,
	  }:{ref:selected?.ref,commit_sha:selected?.sha,source_node_id:selectedSource.value})
  message.success(t('manualRun.started'))
  resetManualFlow()
  await refresh()
 }catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}
}
function releasePlanTitle(plan:ReleasePlan){
 const legacyName=plan.name?.trim()
 return plan.description?.trim()||(legacyName&&!/^发布计划-[0-9a-f]{8}$/i.test(legacyName)?legacyName:'')||t('releasePlan.unnamed')
}
function openReleasePlanFromRun(run:Run){
 if(!run.release_plan_id)return
 void router.push({path:'/release-plans',query:{view:'plans',plan:run.release_plan_id}})
}
function openApplicationFromRun(run:Run){
 if(!run.application_id)return
 void router.push({path:'/applications',query:{application:run.application_id}})
}
async function revealActiveApplication(){
 const applicationID=activeApplicationID.value
 if(!applicationID||!applications.value.some(item=>item.id===applicationID))return
 if(!visibleApplicationCards.value.some(card=>card.application.id===applicationID))resetApplicationFilters()
 const card=applicationCards.value.find(item=>item.application.id===applicationID)
 if(card){applicationPageState.current=Math.floor(visibleApplicationCards.value.indexOf(card)/applicationPageState.pageSize)+1;toggleApplicationDetails(card)}
 await nextTick()
 document.getElementById(`application-${applicationID}`)?.scrollIntoView({behavior:'instant',block:'nearest'})
}
async function loadReleasePlanApplicationResources(){
 if(!canReadDeployments.value||!releasePlanResources.pipelineRunID)return
 const pipelineRunID=releasePlanResources.pipelineRunID,applicationID=releasePlanResources.applicationID
 releasePlanResourcesController?.abort()
 const controller=new AbortController()
 releasePlanResourcesController=controller
 const current=()=>releasePlanResourcesController===controller&&!controller.signal.aborted&&releasePlanResources.open&&releasePlanResources.pipelineRunID===pipelineRunID&&releasePlanResources.applicationID===applicationID
 releasePlanResources.loading=true
 releasePlanResources.error=''
 try{
  const response=await client.get<{deployments:DeploymentRecord[]}>(`/deployments?pipeline_run_id=${encodeURIComponent(pipelineRunID)}&limit=200`,{signal:controller.signal,timeout:35_000})
  if(!current())return
  releasePlanResources.records=(response.data.deployments||[]).filter(record=>record.pipeline_run_id===pipelineRunID)
  if(canReadContainerLogs.value){
   const runtimeIDs=new Set(releasePlanResources.records.filter(record=>deploymentKind(record)==='docker').map(record=>record.runtime_id).filter(Boolean))
   await Promise.all([...runtimeIDs].map(runtimeID=>loadDockerRuntime(runtimeID,true)))
  }
 }catch(error){
  if(!current())return
  releasePlanResources.records=[]
  releasePlanResources.error=apiErrorMessage(error)
 }finally{if(releasePlanResourcesController===controller){releasePlanResourcesController=null;releasePlanResources.loading=false}}
}
function openReleasePlanApplicationRun(planID:string,applicationID:string,pipelineRunID:string){
 const plan=releasePlans.value.find(item=>item.id===planID)
 const member=plan?.groups?.[0]?.applications.some(item=>item.application_id===applicationID)
 const latest=runs.value.filter(item=>item.application_id===applicationID).reduce<Run|undefined>((current,item)=>!current||Date.parse(item.created_at)>Date.parse(current.created_at)?item:current,undefined)
 const linked=member&&latest?.id===pipelineRunID
 if(!linked){message.error('该发布计划应用没有可定位的流水线运行');return}
 selectedRunID.value=pipelineRunID
 void router.push({path:'/release-plans',query:{view:'runs',run:pipelineRunID}})
}
function openReleasePlanApplicationResources(planID:string,applicationID:string,pipelineRunID:string){
 const application=applications.value.find(item=>item.id===applicationID)
 Object.assign(releasePlanResources,{
  open:true,
  loading:false,
  planID,
  applicationID,
  pipelineRunID,
  title:`部署资源：${application?.name||'应用'}`,
  error:'',
  records:[],
 })
 void loadReleasePlanApplicationResources()
}
function releasePlanMutationBlocked(plan:ReleasePlan){
 return plan.latest_execution?.status==='pending'||plan.latest_execution?.status==='running'
}
function openReleasePlanEditor(planID:string){
 const plan=releasePlans.value.find(item=>item.id===planID)
 if(!plan||releasePlanMutationBlocked(plan))return
 releasePlanEditorPlan.value=plan
 releasePlanEditorOpen.value=true
}
async function saveReleasePlanEditor(value:ReleasePlanEditorValue){
 const creating=!value.id
 if(!creating&&!releasePlans.value.some(item=>item.id===value.id))return
 saving.value=true
 releasePlanMutationID.value=value.id||'creating'
 try{
  if(creating)await client.post('/release-plans',{
   description:value.description,
	   groups:value.groups.map(group=>({
	    name:'应用列表',mode:group.mode,failure_policy:group.failure_policy,applications:group.applications,
   })),
  })
  else await client.put(`/release-plans/${value.id}/configuration`,{description:value.description,groups:value.groups})
  message.success(t('releasePlan.editor.saved'))
  releasePlanEditorOpen.value=false
  releasePlanEditorPlan.value=null
  await refresh()
 }catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false;releasePlanMutationID.value=''}
}
async function toggleReleasePlan(planID:string,enabled:boolean){
 releasePlanMutationID.value=planID
 try{
  await client.patch(`/release-plans/${planID}/status`,{active:enabled})
  message.success(enabled?t('releasePlan.editor.enabled'):t('releasePlan.editor.disabled'))
  await refresh()
 }catch(error){message.error(apiErrorMessage(error))}finally{releasePlanMutationID.value=''}
}
function removeReleasePlan(planID:string){
 const plan=releasePlans.value.find(item=>item.id===planID)
 if(!plan||releasePlanMutationBlocked(plan))return
 Modal.confirm({
  title:t('releasePlan.editor.removePlanConfirm'),
  content:t('releasePlan.editor.removePlanHint'),
  okText:t('releasePlan.editor.remove'),cancelText:t('releasePlan.editor.cancel'),okType:'danger',
  async onOk(){
   releasePlanMutationID.value=planID
   try{await client.delete(`/release-plans/${planID}`);message.success(t('releasePlan.editor.removed'));await refresh()}
   catch(error){message.error(apiErrorMessage(error))}finally{releasePlanMutationID.value=''}
  },
 })
}
function openReleaseGroupApplicationPicker(planID:string,groupID:string){
 const plan=releasePlans.value.find(item=>item.id===planID)
 const group=plan?.groups?.find(item=>item.id===groupID)
 if(!plan||!group||releasePlanMutationBlocked(plan))return
 releasePlanAddApplicationPlanID.value=planID
 releasePlanAddApplicationGroupID.value=groupID
 releasePlanAddApplicationIDs.value=[]
 releasePlanAddApplicationOpen.value=true
}
function resetReleaseGroupApplicationPicker(){
 releasePlanAddApplicationOpen.value=false
 releasePlanAddApplicationPlanID.value=''
 releasePlanAddApplicationGroupID.value=''
 releasePlanAddApplicationIDs.value=[]
}
async function addReleaseGroupApplications(){
 const target=releasePlanAddApplicationTarget.value
 const selectedIDs=[...releasePlanAddApplicationIDs.value]
 if(!target||!selectedIDs.length||releasePlanMutationBlocked(target.plan))return
 const additions:ReleasePlanGroupApplication[]=selectedIDs.map(applicationID=>({id:'',application_id:applicationID,manual_deploy:false,source_type:'',source_value:''}))
 releasePlanMutationID.value=target.plan.id
 try{
  const result=await client.put<{release_plan:ReleasePlan}>(`/release-plans/${target.plan.id}/groups/${target.group.id}`,releaseGroupPayload(target.group,[...target.group.applications,...additions]))
  const planIndex=releasePlans.value.findIndex(item=>item.id===target.plan.id)
  if(planIndex>=0)releasePlans.value[planIndex]=result.data.release_plan
  message.success(t('releasePlan.addApplicationDialog.added',{count:selectedIDs.length}))
  resetReleaseGroupApplicationPicker()
 }catch(error){message.error(apiErrorMessage(error))}finally{releasePlanMutationID.value=''}
}
async function removeReleaseGroupApplication(planID:string,groupID:string,applicationID:string){
 const plan=releasePlans.value.find(item=>item.id===planID)
 const group=plan?.groups?.find(item=>item.id===groupID)
 if(!plan||!group||releasePlanMutationBlocked(plan))return
 releasePlanMutationID.value=planID
 try{
  const result=await client.put<{release_plan:ReleasePlan}>(`/release-plans/${planID}/groups/${groupID}`,releaseGroupPayload(group,group.applications.filter(item=>item.application_id!==applicationID)))
  const planIndex=releasePlans.value.findIndex(item=>item.id===planID)
  if(planIndex>=0)releasePlans.value[planIndex]=result.data.release_plan
  message.success(t('releasePlan.editor.applicationRemoved'))
 }catch(error){message.error(apiErrorMessage(error))}finally{releasePlanMutationID.value=''}
}
function releaseGroupPayload(group:ReleasePlanGroup,groupApplications:ReleasePlanGroupApplication[]){
 return {
	  name:'应用列表',mode:group.mode,failure_policy:group.failure_policy,
	  depends_on_group_ids:[],
  applications:groupApplications.map(item=>({
   application_id:item.application_id,manual_deploy:Boolean(item.manual_deploy),source_type:item.source_type||'',source_value:item.source_value||'',
  })),
 }
}
function resetPlanExecution(){
 planExecutionController?.abort()
 planExecutionController=null
 planExecutionOpen.value=false
 planExecutionPlanID.value=''
 planExecutionTitle.value=''
 planExecutionExpectedUpdatedAt.value=''
 planExecutionRequestID.value=''
 planExecutionGroups.value=[]
}
function planExecutionItems(){return planExecutionGroups.value.flatMap(group=>group.items)}
function planApplicationBlockReason(application?:Application){
 if(!application)return t('releasePlanExecution.reason.applicationMissing')
 if(!application.is_active)return t('releasePlanExecution.reason.applicationDisabled')
 if(!applicationManualWorkflows(application).length)return t('releasePlanExecution.reason.manualSourceMissing')
 return ''
}
function buildPlanExecutionGroups(plan:ReleasePlan){
	 if(plan.groups?.length!==1)return []
	 const applicationCounts=new Map<string,number>()
	 for(const membership of plan.groups[0].applications||[])applicationCounts.set(membership.application_id,(applicationCounts.get(membership.application_id)||0)+1)
	 return plan.groups.map(group=>({
  id:group.id,
  name:group.name,
  mode:group.mode,
  failurePolicy:group.failure_policy,
	  dependencies:[],
  items:[...(group.applications||[])].sort((left,right)=>(left.sort_order||0)-(right.sort_order||0)).map(membership=>{
   const application=applications.value.find(item=>item.id===membership.application_id)
   const relationMissing=!membership.id
   const duplicated=(applicationCounts.get(membership.application_id)||0)>1
   const reason=relationMissing?t('releasePlanExecution.reason.membershipMissing'):duplicated?t('releasePlanExecution.reason.duplicateApplication'):planApplicationBlockReason(application)
   const workflows=applicationManualWorkflows(application).map(item=>({id:item.id,name:workflowDisplayName(item),revision:item.revision}))
   const selectedWorkflow=workflows[0]
   return {
    membershipID:membership.id||`${group.id}:${membership.application_id}`,
    applicationID:membership.application_id,
    applicationName:application?.name||membership.application?.name||t('releasePlan.unknownApplication'),
    workflowID:selectedWorkflow?.id||'',
    workflowRevision:selectedWorkflow?.revision||0,
    workflows,
    loadState:reason?'blocked' as const:'idle' as const,
    reason,
    staticBlocked:Boolean(reason),
	    sources:[],
	    refs:[],
	    artifacts:[],
	    executionMode:'code' as const,
	    selectedSourceID:'',
	    selectedRef:'',
	    selectedArtifactID:'',
	    referenceError:'',
   }
  }),
 }))
}
async function loadPlanExecutionItem(membershipID:string,signal:AbortSignal){
 const item=planExecutionItems().find(candidate=>candidate.membershipID===membershipID)
 if(!item||item.staticBlocked||!item.workflowID)return
 const requestedWorkflowID=item.workflowID
 const application=applications.value.find(candidate=>candidate.id===item.applicationID)
 const blockedReason=planApplicationBlockReason(application)
 if(blockedReason){item.loadState='blocked';item.reason=blockedReason;return}
 item.loadState='loading';item.reason=''
 try{
  const data=(await client.get<RefResult>(`/applications/${item.applicationID}/workflows/${item.workflowID}/repository-refs`,{timeout:35000,signal})).data
  if(signal.aborted||item.workflowID!==requestedWorkflowID)return
	  const sources=(data.manual_sources||[]).map(item=>({id:item.id,name:item.name,environment:item.environment}))
	  const refs:PlanExecutionReference[]=[...(data.branches||[]).map(item=>({kind:'branch' as const,ref:`refs/heads/${item.name}`,name:item.name,sha:item.sha})),...(data.tags||[]).map(item=>({kind:'tag' as const,ref:`refs/tags/${item.name}`,name:item.name,sha:item.sha}))]
	  const availableArtifacts=data.artifacts||[]
	  if(!sources.length||(!refs.length&&!availableArtifacts.length)){
	   const reason=!sources.length?t('releasePlanExecution.reason.manualSourceMissing'):t('releasePlanExecution.reason.referenceOrArtifactMissing')
	   item.loadState='blocked';item.reason=reason;item.sources=sources;item.refs=refs;item.artifacts=availableArtifacts
	   return
	  }
	  const defaultRef=refs.find(item=>item.kind==='branch'&&item.name===application?.repository?.default_branch)?.ref||''
	  item.loadState='ready';item.reason='';item.sources=sources;item.refs=refs;item.artifacts=availableArtifacts;item.referenceError=data.reference_error||''
	  item.executionMode=refs.length?'code':'artifact'
	  item.selectedSourceID=sources[0]?.id||'';item.selectedRef=defaultRef
	  item.selectedArtifactID=availableArtifacts.length===1?availableArtifacts[0].id:''
 }catch(error){
  if(signal.aborted||item.workflowID!==requestedWorkflowID)return
  item.loadState='error';item.reason=apiErrorMessage(error)
 }
}
async function loadPlanExecutionItems(membershipIDs:string[],signal:AbortSignal){
 const queue=[...new Set(membershipIDs)]
 const worker=async()=>{for(;;){const membershipID=queue.shift();if(!membershipID||signal.aborted)return;await loadPlanExecutionItem(membershipID,signal)}}
 await Promise.all(Array.from({length:Math.min(4,queue.length)},()=>worker()))
}
function openReleasePlan(planID:string,membershipID=''){
 const plan=releasePlans.value.find(item=>item.id===planID)
 if(!plan)return
 resetPlanExecution()
 planExecutionPlanID.value=plan.id
 planExecutionExpectedUpdatedAt.value=plan.updated_at||plan.created_at
 planExecutionRequestID.value=crypto.randomUUID()
 const groups=buildPlanExecutionGroups(plan)
 if(membershipID){
  const selected=groups.flatMap(group=>group.items).find(item=>item.membershipID===membershipID)
  if(!selected)return
  planExecutionGroups.value=groups.map(group=>({...group,items:group.items.filter(item=>item.membershipID===membershipID)})).filter(group=>group.items.length)
  planExecutionTitle.value=`${releasePlanTitle(plan)} · ${selected.applicationName}`
 }else{
  planExecutionGroups.value=groups
  planExecutionTitle.value=releasePlanTitle(plan)
 }
 planExecutionOpen.value=true
 planExecutionController=new AbortController()
 const loadable=planExecutionItems().filter(item=>item.loadState==='idle'&&item.workflowID).map(item=>item.membershipID)
 void loadPlanExecutionItems(loadable,planExecutionController.signal)
}
function openReleasePlanApplication(planID:string,membershipID:string){openReleasePlan(planID,membershipID)}
function updatePlanExecutionRef(membershipID:string,value:string){const item=planExecutionItems().find(candidate=>candidate.membershipID===membershipID);if(item)item.selectedRef=value}
function updatePlanExecutionMode(membershipID:string,value:'code'|'artifact'){const item=planExecutionItems().find(candidate=>candidate.membershipID===membershipID);if(item)item.executionMode=value}
function updatePlanExecutionArtifact(membershipID:string,value:string){const item=planExecutionItems().find(candidate=>candidate.membershipID===membershipID);if(item)item.selectedArtifactID=value}
function retryPlanExecutionApplication(membershipID:string){if(planExecutionController)void loadPlanExecutionItem(membershipID,planExecutionController.signal)}
function setReleasePlanExecuting(planID:string,executing:boolean){
 executingReleasePlanIDs.value=executing
  ? [...new Set([...executingReleasePlanIDs.value,planID])]
  : executingReleasePlanIDs.value.filter(id=>id!==planID)
}
async function applyAcceptedReleasePlanExecution(planID:string,execution:ReleasePlanExecutionRecord){
 const plan=releasePlans.value.find(item=>item.id===planID)
 if(plan)plan.latest_execution={id:execution.id,status:execution.status,created_at:execution.created_at,finished_at:execution.finished_at,items:execution.items||[]}
 const runIDs=[...new Set((execution.items||[]).map(item=>item.pipeline_run_id).filter(Boolean))]
 if(!runIDs.length){
  message.error(t('releasePlanExecution.acceptedWithoutRuns'))
  await refresh()
  return
 }
 const results=await Promise.allSettled(runIDs.map(runID=>client.get<{pipeline_run:Run}>(`/pipeline-runs/${encodeURIComponent(runID)}`)))
 const acceptedRuns=results.flatMap(result=>result.status==='fulfilled'&&result.value.data.pipeline_run?[result.value.data.pipeline_run]:[])
 if(acceptedRuns.length){
  const acceptedIDs=new Set(acceptedRuns.map(run=>run.id))
  runs.value=[...acceptedRuns,...runs.value.filter(run=>!acceptedIDs.has(run.id))].sort((left,right)=>new Date(right.created_at).getTime()-new Date(left.created_at).getTime())
 }
}
function executeReleasePlan(){
	 const selections=planExecutionItems().map(item=>{
	  const reference=item.refs.find(candidate=>candidate.ref===item.selectedRef)
	  return {release_group_application_id:item.membershipID,workflow_id:item.workflowID,expected_workflow_revision:item.workflowRevision,source_node_id:item.selectedSourceID,ref:item.executionMode==='code'?item.selectedRef:'',commit_sha:item.executionMode==='code'?reference?.sha||'':'',artifact_id:item.executionMode==='artifact'?item.selectedArtifactID:''}
	 })
	 if(!planExecutionPlanID.value||selections.some(item=>!item.workflow_id||!item.source_node_id||(!item.artifact_id&&(!item.ref||!item.commit_sha)))){
  message.error(t('releasePlanExecution.selectionInvalid'))
  return
 }
 const planID=planExecutionPlanID.value
 const request={request_id:planExecutionRequestID.value,expected_plan_updated_at:planExecutionExpectedUpdatedAt.value,selections}
 setReleasePlanExecuting(planID,true)
 resetPlanExecution()
 message.success(t('releasePlanExecution.submitted'))
 void client.post<{release_plan_execution:ReleasePlanExecutionRecord}>(`/release-plans/${planID}/executions`,request,{timeout:60000}).then(async result=>{
  await applyAcceptedReleasePlanExecution(planID,result.data.release_plan_execution)
 }).catch(async error=>{
  message.error({content:t('releasePlanExecution.submitFailed',{message:apiErrorMessage(error)}),duration:8})
  await refresh()
 }).finally(()=>{
  setReleasePlanExecuting(planID,false)
 })
}
function showWebhook(item:Repository){void client.get<{webhook_url:string;webhook_secret:string}>(`/repositories/${item.id}/webhook`).then(result=>Modal.info({title:`${item.name} Webhook`,width:650,content:()=>`${result.data.webhook_url}\n${result.data.webhook_secret}`})).catch(error=>message.error(apiErrorMessage(error)))}
function applicationName(run:Run){return applications.value.find(item=>item.id===run.application_id)?.name||run.application?.name||'未命名应用'}
function applicationForRun(run:Run){return applications.value.find(item=>item.id===run.application_id)||run.application}
function workflowForRun(run:Run){return applicationForRun(run)?.workflows?.find(item=>item.id===run.workflow_id)}
function runWorkflowName(run:Run){
 const workflow=workflowForRun(run)
 if(workflow)return workflowDisplayName(workflow)
 return run.workflow_id?`流水线 ${run.workflow_id.slice(0,8)}`:'未关联流水线'
}
function releasePlanForRun(run:Run){return releasePlans.value.find(item=>item.id===run.release_plan_id)}
function runReleasePlanName(run:Run){
 if(!run.release_plan_id)return '未关联发布计划'
 const plan=releasePlanForRun(run)
 return plan?releasePlanTitle(plan):`发布计划 ${run.release_plan_id.slice(0,8)}`
}
function runReferenceLabel(run?:Run){
 if(!run)return '—'
 const source=formatGitReference({ref:run.ref,sha:run.commit_sha,trigger:run.trigger})
 return run.selected_artifact?`已有制品：${run.selected_artifact.name}${source&&source!=='—'?`；原始版本：${source}`:''}`:source
}
function artifactSelectOption(artifact:ManualArtifact){
 const reference=artifact.ref?.trim()||''
 const source=reference.startsWith('refs/heads/')?`来源：分支 ${reference.slice('refs/heads/'.length)}`:
  reference.startsWith('refs/tags/')?`来源：Tag ${reference.slice('refs/tags/'.length)}`:
  reference?`来源：版本 ${reference.replace(/^refs\//,'')}`:'来源：手工上传'
 const details=[`制品类型：${artifact.kind==='oci_image'?'OCI 镜像':'文件制品'}`,source]
 if(artifact.commit_sha)details.push(`提交：${artifact.commit_sha.slice(0,8)}`)
 details.push(`摘要：${artifact.digest.slice(0,19)}…`)
 return {value:artifact.id,label:artifact.name,details}
}
function formatRunTime(value:string){return formatDateTime(value,locale.value==='en-US'?'en-US':'zh-CN')}
function formatRunRelativeTime(value:string){return formatRelativeTime(value,relativeTimeNow.value,locale.value==='en-US'?'en-US':'zh-CN')}
function runStatusLabel(status:string,stage?:string){if(stage==='stopping')return t('pipelineStop.stopping');return ({detected:'已发现',ready:'准备就绪',blocked:'已阻塞',awaiting_approval:'等待审核',running:'执行中',succeeded:'已成功',failed:'已失败',skipped:'已跳过',canceled:t('pipelineStop.stopped')} as Record<string,string>)[status]||status}
function runStatusColor(status:string){return status==='succeeded'?'success':status==='failed'||status==='blocked'?'error':status==='running'?'processing':status==='awaiting_approval'?'warning':'default'}
function refreshVisibleState(){
 relativeTimeNow.value=Date.now()
 if(document.hidden)return
 if(props.section==='applications')refreshVisibleDeploymentRuntime()
 if(props.section==='build-plans'&&buildPlanView.value==='artifacts')void loadArtifacts()
 if(Date.now()-lastDeliverySync>=(deliveryConnected?60_000:30_000))queueDeliveryRefresh()
}
function queueDeliveryRefresh(syncOnly=false){
 if(!deliveryRefreshTimer)applicationSyncOnly=syncOnly
 else applicationSyncOnly=applicationSyncOnly&&syncOnly
 if(document.hidden||deliveryRefreshTimer||!['applications','release-plans'].includes(props.section))return
 deliveryRefreshTimer=setTimeout(async()=>{
  deliveryRefreshTimer=undefined
  if(document.hidden)return
  // 状态事件可能在上一轮 HTTP 同步期间到达，必须留到下一轮处理。
  if(stateRefreshing||loading.value){queueDeliveryRefresh(applicationSyncOnly);return}
  if(!applicationSyncOnly)lastDeliverySync=Date.now()
  if(props.section==='applications'&&applicationSyncOnly){
   stateRefreshing=true
   try{applications.value=(await (auth.canAny(['application.read'])?client.get<{applications:Application[]}>('/applications'):Promise.resolve({data:{applications:[] as Application[]}}))).data.applications||[]}catch{}finally{stateRefreshing=false}
  }
  else if(props.section==='applications')await refreshApplicationState()
  else if(props.section==='release-plans'&&releaseView.value==='runs')await refreshRunState()
  else if(props.section==='release-plans')await refreshReleasePlanState()
 },300)
}
function onDeliveryPageVisible(){
 relativeTimeNow.value=Date.now()
 if(!document.hidden)queueDeliveryRefresh()
}
function connectDeliveryEvents(){
 stopDeliveryEvents?.();stopDeliveryEvents=undefined;deliveryConnected=false
 clearTimeout(deliveryRefreshTimer);deliveryRefreshTimer=undefined
 if(!['applications','release-plans'].includes(props.section)||!auth.canAny([`${sectionPermission.value}.read`]))return
 stopDeliveryEvents=subscribeDeliveryEvents({
  event:event=>{
   if(event.type==='resync'){queueDeliveryRefresh();return}
   const resources=event.resources||[]
   if(props.section!=='applications'&&!rollbackDialog.open&&!releasePlanResources.open&&resources.every(resource=>resource==='application_sync'||resource==='deployments'||resource==='deployment_plans')&&(!visibleDeploymentRunIDs().length||!resources.includes('deployments')))return
   queueDeliveryRefresh(resources.length>0&&resources.every(resource=>resource==='application_sync'))
  },
  state:value=>{deliveryConnected=value==='connected'},
 })
}
function syncBuildPlanSelection(){
 if(props.section!=='build-plans'){
  selectedBuildPlanID.value=''
  buildPlanView.value='overview'
  artifactApplicationID.value=''
  artifacts.value=[]
  return
 }
 const requested=typeof route.query.plan==='string'?route.query.plan:''
 if(requested&&buildPlans.value.some(item=>item.id===requested))selectedBuildPlanID.value=requested
 else if(!buildPlans.value.some(item=>item.id===selectedBuildPlanID.value)){
  if(selectedBuildPlanID.value){buildPlanView.value='overview';artifactApplicationID.value=''}
  selectedBuildPlanID.value=buildPlans.value[0]?.id||''
 }
 if(artifactApplicationID.value&&!applications.value.some(item=>item.id===artifactApplicationID.value))artifactApplicationID.value=''
}
function selectBuildPlan(id:string){
 selectedBuildPlanID.value=id
 buildPlanView.value='overview'
}
function consumeBuildCreateRequest(){
 if(!['build-plans','image-registries'].includes(props.section)||route.query.create!=='1'||!canCreate.value)return
 create()
 const query={...route.query}
 delete query.create
 void router.replace({query})
}

watch(()=>props.section,()=>{formOpen.value=false;resetForms();resetManualFlow();resetPlanExecution();syncBuildPlanSelection();void refresh().then(()=>{syncBuildPlanSelection();consumeBuildCreateRequest();consumeDeliveryRequest();void revealActiveApplication()})})
watch(()=>registryForm,()=>registryTested.value=false,{deep:true})
watch(()=>registryForm.provider,(provider,previous)=>{
 if(provider==='docker_hub'){
  registryForm.endpoint=''
  registryForm.allow_insecure_http=false
 }else if(previous==='docker_hub'&&!registryForm.endpoint){
  registryForm.endpoint='https://'
 }
})
watch(()=>buildForm.kind,(kind)=>{if(kind==='dockerfile'){buildForm.dockerfile_path||='Dockerfile';buildForm.context_path||='.'}else{buildForm.working_directory||='.';buildForm.runtime_image||=DEFAULT_RUNTIME_IMAGE}})
watch(()=>repoForm.provider,(provider)=>{
 if(repoForm.credential_id&&!credentials.value.some(item=>item.id===repoForm.credential_id&&item.auth_type===repoForm.auth_type&&(item.provider===provider||item.provider==='generic'||provider==='generic')))repoForm.credential_id=''
 if(repoForm.api_credential_id&&!credentials.value.some(item=>item.id===repoForm.api_credential_id&&item.auth_type==='token'&&item.provider===provider))repoForm.api_credential_id=''
})
watch(()=>repoForm.auth_type,(authType)=>{if(authType==='none'||!credentials.value.some(item=>item.id===repoForm.credential_id&&item.auth_type===authType))repoForm.credential_id=''})
watch(()=>buildPlans.value.map(item=>item.id).join(','),syncBuildPlanSelection)
watch(()=>route.query.plan,syncBuildPlanSelection)
watch(()=>applications.value.map(item=>item.id).join(','),syncBuildPlanSelection)
watch([selectedBuildPlanID,artifactApplicationID,buildPlanView],()=>{void loadArtifacts()})
watch(()=>route.query.create,consumeBuildCreateRequest)
watch(()=>route.query.application,()=>{void revealActiveApplication()})
watch(()=>route.query.delivery_kind,consumeDeliveryRequest)
watch(()=>route.query.run,()=>{void revealRequestedRun()})
watch(()=>runListEntries.value.length,()=>{runPagination.current=Math.min(runPagination.current,Math.max(1,Math.ceil(runListEntries.value.length/runPagination.pageSize)))})
watch(()=>props.section,connectDeliveryEvents)
onMounted(()=>{connectDeliveryEvents();void refresh().then(()=>{lastDeliverySync=Date.now();syncBuildPlanSelection();consumeBuildCreateRequest();consumeDeliveryRequest();void revealActiveApplication()});releaseTimer=window.setInterval(refreshVisibleState,5000);document.addEventListener('visibilitychange',onDeliveryPageVisible);window.addEventListener('focus',onDeliveryPageVisible)})
onBeforeUnmount(()=>{stopDeliveryEvents?.();clearTimeout(deliveryRefreshTimer);resetPlanExecution();clearInterval(releaseTimer);document.removeEventListener('visibilitychange',onDeliveryPageVisible);window.removeEventListener('focus',onDeliveryPageVisible)})
</script>

<template><section :class="{'applications-page':props.section==='applications'}"><PageToolbar :description="props.section==='applications'?t('applicationWorkspace.description'):currentDescription"><a-tag v-if="props.section==='applications'||props.section==='release-plans'" color="processing">{{ t('applicationCard.autoRefresh') }}</a-tag><a-button :loading="loading" @click="refresh"><RefreshCw :size="15"/>刷新</a-button><a-button v-if="props.section==='release-plans'&&releaseView==='runs'&&auth.canAny(['pipeline_run.execute'])&&auth.canAny(['application.read'])" type="primary" :disabled="saving" @click="openManualPicker">{{ t('manualRun.execute') }}</a-button><a-button v-if="canCreate&&(props.section!=='release-plans'||releaseView==='plans')" type="primary" @click="create"><Plus :size="15"/>{{ props.section==='release-plans'?'创建发布计划':props.section==='applications'?t('applicationWorkspace.create'):'新建' }}</a-button></PageToolbar>
<div v-if="props.section==='applications'" class="apps-workspace">
  <div class="apps-filter-bar">
    <a-input v-model:value="applicationSearch" allow-clear :placeholder="t('applicationWorkspace.search')" :aria-label="t('applicationWorkspace.search')" class="apps-search"><template #prefix><Search :size="16"/></template></a-input>
    <div class="apps-filters" role="group" :aria-label="t('applicationWorkspace.filterLabel')">
      <button v-for="filter in applicationFilters" :key="filter.key" type="button" :class="{'is-selected':applicationFilter===filter.key}" :aria-pressed="applicationFilter===filter.key" @click="applicationFilter=filter.key">{{ filter.label }}<span>{{ filter.count }}</span></button>
    </div>
  </div>
  <a-table v-if="visibleApplicationCards.length" class="apps-table resource-list-table" size="small" table-layout="fixed"
    :columns="applicationColumns" :data-source="visibleApplicationCards" :row-key="(card:ApplicationCard)=>card.application.id"
    :pagination="applicationPagination" :scroll="{x:1485}" @change="changeApplicationPage"
    :row-class-name="applicationTableRowClass" :custom-row="applicationTableRow">
    <template #bodyCell="{column,record:card}">
      <div v-if="column.key==='application'" class="apps-identity">
        <span class="apps-mark"><Boxes :size="18"/></span>
        <div class="apps-identity-copy">
          <div class="apps-title"><a-tooltip :title="card.application.name" :mouse-enter-delay="0.1"><button class="resource-name" @click="toggleApplicationDetails(card, 'workflows')">{{ card.application.name }}</button></a-tooltip><span v-if="!card.application.is_active" class="apps-inactive">{{ t('applicationCard.inactive') }}</span></div>
          <a-tooltip :title="card.application.description" :mouse-enter-delay="0.1"><p class="apps-description">{{ card.application.description||t('applicationCard.noDescription') }}</p></a-tooltip>
        </div>
      </div>
      <a-tooltip v-else-if="column.key==='repository'" :title="card.repository?.name" :mouse-enter-delay="0.1"><button type="button" class="apps-link apps-repository" :disabled="!card.repository||!auth.canAny(['repository.read'])" @click="openApplicationLink(card)"><GitBranch :size="14"/><span>{{ card.repository?.name||(card.application.repository_id?t('applicationCard.repository'):t('applicationCard.unbound')) }}</span><ArrowUpRight v-if="card.repository&&auth.canAny(['repository.read'])" :size="12"/></button></a-tooltip>
      <div v-else-if="column.key==='run'" class="apps-run">
        <a-tooltip :title="card.run?.commit_message" :mouse-enter-delay="0.1"><button type="button" class="apps-run-title" :disabled="!card.run" @click="openApplicationLatestRun(card)"><span>{{ card.run?.commit_message||t('applicationCard.noRun') }}</span><ArrowUpRight v-if="card.run" :size="13"/></button></a-tooltip>
        <a-tooltip v-if="card.run" :title="runReferenceLabel(card.run)" :mouse-enter-delay="0.1"><div class="apps-reference">{{ runReferenceLabel(card.run) }}</div></a-tooltip>
        <div v-if="card.run" class="apps-run-context">
          <a-tooltip v-if="card.run.workflow_id" :title="applicationLatestWorkflowName(card)" :mouse-enter-delay="0.1"><span class="apps-workflow-caption"><Workflow :size="12"/><span>{{ applicationLatestWorkflowName(card) }}</span></span></a-tooltip>
          <time class="apps-time" :datetime="card.run.created_at">{{ formatApplicationTime(card.run.created_at) }}</time>
        </div>
      </div>
      <div v-else-if="column.key==='status'" class="apps-cell-stack">
        <span class="apps-status" :class="[`tone-${card.state.tone}`,{'is-live':card.state.live}]"><i/>{{ card.state.label }}</span>
        <a-tooltip :title="applicationCurrentNode(card.run)" :mouse-enter-delay="0.1"><span class="apps-node">{{ applicationCurrentNode(card.run) }}</span></a-tooltip>
      </div>
      <div v-else-if="column.key==='sync'" class="apps-cell-stack">
        <a-tooltip :title="card.application.sync_message" :mouse-enter-delay="0.1"><span class="apps-status" :class="[`tone-${card.sync.tone}`,{'is-live':card.sync.live}]"><i/>{{ card.sync.label }}</span></a-tooltip>
        <a-tooltip :title="t('applicationCard.lastChecked')+'：'+formatApplicationTime(card.application.last_checked_at)" :mouse-enter-delay="0.1"><time class="apps-time" :datetime="card.application.last_checked_at">{{ card.application.last_checked_at?formatRelativeTime(card.application.last_checked_at,relativeTimeNow,locale):t('applicationCard.noTime') }}</time></a-tooltip>
      </div>
      <div v-else-if="column.key==='resources'" class="apps-resource-switch">
        <button type="button" :class="{'is-selected':applicationDetailsOpen(card.application.id)&&applicationDetailTab(card.application.id)==='workflows'}" :aria-label="t('applicationCard.linkedWorkflows')+'：'+card.application.name" :aria-expanded="applicationDetailsOpen(card.application.id)&&applicationDetailTab(card.application.id)==='workflows'" :aria-controls="applicationDetailsOpen(card.application.id)?`application-details-${card.application.id}`:undefined" @click="toggleApplicationDetails(card,'workflows')"><Workflow :size="14"/>{{ t('applicationCard.pipeline') }}<span class="apps-count">{{ card.workflowSummaries.length }}</span></button>
        <button type="button" :class="{'is-selected':applicationDetailsOpen(card.application.id)&&applicationDetailTab(card.application.id)==='deployments'}" :aria-label="t('applicationCard.deploymentInstances')+'：'+card.application.name" :aria-expanded="applicationDetailsOpen(card.application.id)&&applicationDetailTab(card.application.id)==='deployments'" :aria-controls="applicationDetailsOpen(card.application.id)?`application-details-${card.application.id}`:undefined" @click="toggleApplicationDetails(card,'deployments')"><Boxes :size="14"/>{{ t('applicationWorkspace.instances') }}<span v-if="canReadDeployments" class="apps-count">{{ card.deploymentInstances.length }}</span></button>
      </div>
      <ResourceRowActions v-else-if="column.key==='actions'" :more="canUpdate">
        <a-button size="small" @click="toggleApplicationDetails(card,'workflows')">{{ t('resourceList.details') }}</a-button>
        <a-button v-if="canExecute" size="small" :loading="checkingApplicationIDs[card.application.id]" :disabled="card.sync.live" @click="checkApplicationUpdates(card.application)">{{ t('applicationCard.checkUpdates') }}</a-button>
        <template #more><a-menu-item v-if="canUpdate" @click="edit(card.application)">{{ t('applicationCard.configure') }}</a-menu-item></template>
      </ResourceRowActions>
    </template>
  </a-table>
  <ResourceDetailDrawer v-model:open="applicationDrawerOpen" :title="applicationDrawerCard?.application.name">
    <template #actions>
      <template v-if="applicationDrawerCard">
        <a-button size="small" :type="applicationDetailTab(applicationDrawerID)==='workflows'?'primary':'default'" @click="toggleApplicationDetails(applicationDrawerCard,'workflows')">{{ t('resourceList.workflows') }} ({{ applicationDrawerCard.workflowSummaries.length }})</a-button>
        <a-button size="small" :type="applicationDetailTab(applicationDrawerID)==='deployments'?'primary':'default'" @click="toggleApplicationDetails(applicationDrawerCard,'deployments')">{{ t('resourceList.instances') }} ({{ applicationDrawerCard.deploymentInstances.length }})</a-button>
        <a-button v-if="canConfigureDelivery" size="small" @click="configureDelivery(applicationDrawerCard.application.id)">{{ t('delivery.configure') }}</a-button>
        <a-button v-if="canUpdate" size="small" @click="edit(applicationDrawerCard.application)">{{ t('applicationCard.configure') }}</a-button>
      </template>
    </template>
    <section v-if="applicationDrawerCard" class="resource-detail-section">
      <h4>{{ t('resourceList.basic') }}</h4>
      <dl class="resource-facts">
        <div><dt>{{ t('applicationCard.repository') }}</dt><dd>{{ applicationDrawerCard.repository?.name || '—' }}</dd></div>
        <div><dt>{{ t('applicationCard.currentNode') }}</dt><dd>{{ applicationCurrentNode(applicationDrawerCard.run) }}</dd></div>
        <div><dt>{{ t('applicationCard.codeStatus') }}</dt><dd>{{ applicationDrawerCard.sync.label }}</dd></div>
        <div class="resource-facts-wide"><dt>{{ t('resourceList.description') }}</dt><dd>{{ applicationDrawerCard.application.description || '—' }}</dd></div>
      </dl>
    </section>
    <template v-for="card in applicationDrawerCard ? [applicationDrawerCard] : []" :key="card.application.id">

      <section v-if="applicationDetailsOpen(card.application.id)" :id="`application-details-${card.application.id}`" class="apps-details-content resource-detail-section" :aria-label="card.application.name+' · '+(applicationDetailTab(card.application.id)==='workflows'?t('applicationCard.linkedWorkflows'):t('applicationCard.deploymentInstances'))">
        <template v-if="applicationDetailTab(card.application.id)==='workflows'">
          <a-table v-if="card.workflowSummaries.length" class="apps-workflow-table" size="small" table-layout="fixed"
            :columns="applicationWorkflowColumns" :data-source="card.workflowSummaries" :row-key="(item:ApplicationCard['workflowSummaries'][number])=>item.workflow.id" :pagination="false" :scroll="{x:1435}">
            <template #bodyCell="{column:workflowColumn,record:item}">
              <a-tooltip v-if="workflowColumn.key==='name'" :title="workflowDisplayName(item.workflow)" :mouse-enter-delay="0.1"><button type="button" class="apps-workflow-name" @click="openApplicationWorkflow(card.application.id,item.workflow.id)"><Workflow :size="15"/><span>{{ workflowDisplayName(item.workflow) }}</span></button></a-tooltip>
              <span v-else-if="workflowColumn.key==='enabled'" class="apps-enabled" :class="{'is-inactive':!item.workflow.is_active}">{{ item.workflow.is_active?t('applicationCard.active'):t('applicationCard.inactive') }}</span>
              <a-tooltip v-else-if="workflowColumn.key==='source'" :title="item.source" :mouse-enter-delay="0.1"><span class="apps-cell-ellipsis">{{ item.source }}</span></a-tooltip>
              <a-tooltip v-else-if="workflowColumn.key==='template'" :title="item.workflow.workflow_template?.name||t('applicationCard.customWorkflow')" :mouse-enter-delay="0.1"><span class="apps-cell-ellipsis">{{ item.workflow.workflow_template?.name||t('applicationCard.customWorkflow') }}</span></a-tooltip>
              <span v-else-if="workflowColumn.key==='tasks'" class="apps-workflow-facts"><span>{{ t('applicationCard.taskCount',{count:item.taskCount}) }}</span><span>{{ t('applicationCard.buildCount',{count:item.buildCount}) }}</span><span>{{ t('applicationCard.deployCount',{count:item.deployCount}) }}</span></span>
              <span v-else-if="workflowColumn.key==='status'" class="apps-status" :class="[`tone-${item.state.tone}`,{'is-live':item.state.live}]"><i/>{{ item.state.label }}</span>
              <time v-else-if="workflowColumn.key==='time'" class="apps-time">{{ formatApplicationTime(item.latestRun?.created_at) }}</time>
            </template>
          </a-table>
          <div v-else class="apps-empty-inline"><Workflow :size="20"/><span>{{ t('applicationCard.noWorkflows') }}</span></div>
        </template>
        <div v-else class="apps-deployments">
        <p v-if="!canReadDeployments" class="apps-empty-inline">{{ t('applicationCard.deploymentPermissionDenied') }}</p>
        <div v-else-if="card.deploymentInstances.length" class="application-deployment-list">
          <div v-for="record in card.deploymentInstances" :key="record.id" class="application-deployment-item">
            <button type="button" class="application-deployment-summary" :aria-expanded="deploymentDetailsOpen(record)" @click="toggleDeploymentDetails(record)">
              <span class="application-deployment-icon" :class="`kind-${deploymentKind(record)}`"><Box v-if="deploymentKind(record)==='docker'||deploymentKind(record)==='compose'"/><Layers3 v-else-if="deploymentKind(record)==='kubernetes'"/><Server v-else/></span>
              <span class="application-deployment-copy"><strong :title="deploymentInstanceName(record)">{{ deploymentInstanceName(record) }}</strong><small>类型：{{ deploymentKindLabel(record) }}；运行位置：{{ record.target_name }}</small></span>
              <span class="application-deployment-state" :class="[`tone-${currentDeploymentStatus(record).tone}`,{'is-live':currentDeploymentStatus(record).live}]"><i/><strong>{{ currentDeploymentStatus(record).label }}</strong><time>{{ formatApplicationTime(record.finished_at||record.updated_at||record.created_at) }}</time></span>
              <ChevronDown :class="{expanded:deploymentDetailsOpen(record)}"/>
            </button>
            <Transition name="deployment-details">
              <div v-if="deploymentDetailsOpen(record)" class="application-deployment-details">
                <a-alert v-if="deploymentRuntimeErrors[record.id]" type="warning" show-icon :message="deploymentRuntimeErrors[record.id]" style="margin-bottom:8px"/>
                <dl>
                  <div v-if="deploymentKind(record)!=='script'"><dt>{{ t('applicationCard.image') }}</dt><dd :title="deploymentImageLabel(record)">{{ deploymentImageLabel(record) }}</dd></div>
                  <div v-if="deploymentRuntimeStates[record.id]"><dt>实时状态</dt><dd>{{ currentDeploymentStatus(record).label }}</dd></div>
                  <div v-if="deploymentRuntimeStates[record.id]?.kind==='kubernetes'"><dt>副本</dt><dd>{{ deploymentRuntimeStates[record.id]?.ready_replicas||0 }} / {{ deploymentRuntimeStates[record.id]?.replicas||0 }} 就绪</dd></div>
                  <div v-if="deploymentKind(record)==='docker'"><dt>{{ t('applicationCard.containerStatus') }}</dt><dd>{{ dockerContainerState(record) }}</dd></div>
                  <div v-if="deploymentKind(record)==='script'"><dt>停止命令</dt><dd>{{ lifecycleScriptAvailable(record,'stop')?'已配置':'未配置' }}</dd></div>
                  <div v-if="deploymentKind(record)==='script'"><dt>重启命令</dt><dd>{{ lifecycleScriptAvailable(record,'restart')?'已配置':'未配置' }}</dd></div>
                </dl>
                <div class="application-deployment-actions">
                  <a-button v-if="deploymentKind(record)==='docker'&&canReadContainerLogs" size="small" :loading="dockerRuntimeLoading[record.runtime_id]" :disabled="!dockerContainerForDeployment(record)" @click="openDeploymentLogs(record)"><FileText/>{{ t('containerLogs.button') }}</a-button>
                  <a-button v-if="deploymentKind(record)==='docker'&&canOpenContainerTerminal&&canReadContainerLogs" size="small" :disabled="dockerRuntimeLoading[record.runtime_id]||dockerContainerForDeployment(record)?.state!=='running'" :title="dockerContainerForDeployment(record)?.state==='running'?'':t('applicationCard.terminalRunningOnly')" @click="openDeploymentTerminal(record)"><TerminalSquare/>{{ t('applicationCard.terminal') }}</a-button>
                  <a-button v-if="canUpdateDeployments&&deploymentKind(record)==='script'" size="small" @click="openShellControlConfiguration(record)"><Settings2/>配置命令</a-button>
                  <a-button v-if="canControlDeployments" size="small" danger :loading="deploymentRuntimeAction===runtimeActionKey(record,'stop')" :disabled="Boolean(deploymentRuntimeAction)||!lifecycleScriptAvailable(record,'stop')" :title="lifecycleScriptAvailable(record,'stop')?'':'请先在该 Shell 部署实例中保存停止命令'" @click="confirmDeploymentRuntimeAction(record,'stop')"><Power/>停止</a-button>
                  <a-button v-if="canControlDeployments" size="small" :loading="deploymentRuntimeAction===runtimeActionKey(record,'restart')" :disabled="Boolean(deploymentRuntimeAction)||!lifecycleScriptAvailable(record,'restart')" :title="lifecycleScriptAvailable(record,'restart')?'':'请先在该 Shell 部署实例中保存重启命令'" @click="confirmDeploymentRuntimeAction(record,'restart')"><RotateCw/>重启</a-button>
                  <a-button v-if="canControlDeployments&&deploymentKind(record)==='kubernetes'" size="small" :disabled="Boolean(deploymentRuntimeAction)" @click="openScaleDialog(record)"><Minus/>扩缩容</a-button>
                  <a-button v-if="canReadDeployments&&canControlDeployments&&deploymentKind(record)==='kubernetes'" size="small" :disabled="Boolean(deploymentRuntimeAction)" @click="openDeploymentRollback(record)"><RotateCw/>{{ rollbackT('rollback.title') }}</a-button>
                  <a-button v-if="canControlDeployments&&deploymentKind(record)==='docker'" size="small" danger :loading="deploymentRuntimeAction===runtimeActionKey(record,'remove')" :disabled="Boolean(deploymentRuntimeAction)" @click="confirmDeploymentRuntimeRemoval(record)"><Trash2/>删除实例</a-button>
                </div>
              </div>
            </Transition>
          </div>
        </div>
        <p v-else class="apps-empty-inline">{{ t('applicationCard.noDeployments') }}</p>
        <section v-if="canReadDeployments&&failedApplicationDeployments(card.application).length" class="application-failed-deployments" :aria-label="rollbackT('rollback.recentFailure')">
          <h4>{{ rollbackT('rollback.recentFailure') }}</h4>
          <article v-for="record in failedApplicationDeployments(card.application)" :key="record.id">
            <strong>{{ deploymentInstanceName(record) }}</strong>
            <p>{{ record.target_name }} · {{ record.namespace }} / {{ record.workload_name }} / {{ record.container_name }}</p>
            <p class="resource-secondary">{{ record.error_message || rollbackT('rollback.failureFallback') }}</p>
            <a-button v-if="canControlDeployments" size="small" @click="openDeploymentRollback(record)"><RotateCw :size="13"/>{{ rollbackT('rollback.title') }}</a-button>
            <a-button v-if="record.pipeline_run_id" size="small" @click="openReleasePlanApplicationResources('',card.application.id,record.pipeline_run_id)">{{ rollbackT('rollback.viewRecords') }}</a-button>
          </article>
        </section>
        </div>
      </section>

    </template>
  </ResourceDetailDrawer>
  <div v-if="loading&&!applicationCards.length" class="apps-loading"><a-skeleton active :paragraph="{rows:3}"/></div>
  <div v-else-if="!visibleApplicationCards.length" class="apps-empty">
    <Search v-if="applicationCards.length" :size="28"/><Boxes v-else :size="28"/>
    <strong>{{ applicationCards.length?t('applicationWorkspace.noResults'):t('applicationCard.empty') }}</strong>
    <p>{{ applicationCards.length?t('applicationWorkspace.noResultsHint'):t('applicationWorkspace.emptyHint') }}</p>
    <a-button v-if="applicationCards.length" @click="resetApplicationFilters">{{ t('applicationWorkspace.clearFilters') }}</a-button>
  </div>
</div>
<BuildPlanWorkspace
 v-else-if="props.section==='build-plans'"
 :plans="buildPlans"
 :applications="applications"
 :artifacts="artifacts"
 :registries="registries"
 :selectedID="selectedBuildPlanID"
 :requestedID="typeof route.query.plan === 'string' ? route.query.plan : ''"
 :activeView="buildPlanView"
 :selectedApplicationID="artifactApplicationID"
 :loading="loading"
 :artifactLoading="artifactLoading"
 :artifactUploading="artifactUploading"
 :artifactDownloadingID="artifactDownloadingID"
 :can-read-artifacts="auth.canAny(['artifact.read'])"
 :can-upload-artifacts="auth.canAny(['artifact.create'])"
 :mutationID="resourceMutationID"
 :can-create="auth.canAny([`${sectionPermission}.create`])"
 :can-update="auth.canAny([`${sectionPermission}.update`])"
 :can-delete="auth.canAny([`${sectionPermission}.delete`])"
 @select-plan="selectBuildPlan"
 @select-view="buildPlanView=$event"
 @update:selectedApplicationID="artifactApplicationID=$event"
 @edit="edit"
 @toggle="confirmBuildPlanStatus"
 @remove="confirmDeleteBuildPlan"
 @refresh-artifacts="loadArtifacts"
 @upload="uploadArtifact"
 @download="downloadArtifact"
/>
<div v-else-if="props.section!=='release-plans'" class="resource-section-stack">
 <div class="vben-card">
  <ResourceTable :rows="activeRows" :columns="activeColumns" :loading="loading" :active-row-key="activeResourceID">
   <template #cell-sync_status="{value}"><a-tag :color="value==='changed'?'warning':value==='synced'?'success':'default'">{{ value }}</a-tag></template>
   <template #cell-provider="{value}"><a-tag color="blue">{{ registryProviderLabel(value as RegistryProvider) }}</a-tag></template>
   <template #cell-endpoint="{row}">{{ registryEndpointLabel(row as Registry) }}</template>
   <template #cell-description="{value}"><span :title="String(value||'')">{{ value||'—' }}</span></template>
   <template #cell-timeout_seconds="{value}">{{ value }} 秒</template>
   <template #actions="{row}">
    <a-button v-if="canUpdate&&props.section==='repositories'" type="link" @click="edit(row)">编辑</a-button>
    <a-button v-if="canExecute&&props.section==='repositories'" type="link" :loading="repositoryTestingID===String(row.id)" :disabled="Boolean(repositoryTestingID)&&repositoryTestingID!==String(row.id)" @click="testStoredRepository(row as Repository)">测试</a-button>
    <a-button v-if="props.section==='repositories'&&(row as Repository).webhook_enabled&&auth.canAny(['repository.secret.read'])" type="link" @click="showWebhook(row as Repository)">Webhook</a-button>
   </template>
  </ResourceTable>
 </div>
</div>
<div v-else-if="releaseView==='runs'" ref="runWorkspace" class="run-workspace vben-card">
  <header class="run-list-heading"><span>共 {{ runs.length }} 条流水线记录<a-tag v-if="activeRunCount" color="processing">{{ activeRunCount }} 个进行中</a-tag></span><small class="resource-secondary">同次发布归为一组</small></header>
  <a-table
    class="run-table resource-list-table"
    row-key="id"
    table-layout="fixed"
    :columns="runColumns"
    :data-source="runListEntries"
    :loading="loading&&!runs.length"
    :pagination="runPagination"
    :scroll="{x:1340}"
    :row-class-name="runListRowClass"
    @change="changeRunPage"
  >
    <template #emptyText><a-empty description="还没有流水线运行"/></template>
    <template #bodyCell="{column,record}">
      <ResourceRowActions v-if="column.key==='actions'" :more="false"><a-button size="small" @click="expandRunListEntry(true,record)">详情</a-button><a-button v-if="!record.executionGroup" size="small" @click="openLogs(record)">日志</a-button></ResourceRowActions>
      <template v-else-if="record.executionGroup">
        <template v-if="column.key==='release_plan'"><button type="button" class="run-resource-link" :title="runEntryPlanName(record)" @click.stop="openReleasePlanFromRun(record.runs[0])">{{ runEntryPlanName(record) }}</button><span class="run-cell-secondary">发布计划执行</span></template>
        <template v-else-if="column.key==='application'"><button class="resource-name" @click="expandRunListEntry(true,record)">{{ executionApplicationCount(record) }} 个应用</button><span class="run-cell-secondary">查看本次发布</span></template>
        <template v-else-if="column.key==='workflow'"><strong class="run-cell-title">{{ record.runs.length }} 条流水线记录</strong><span class="run-cell-secondary">各应用独立执行</span></template>
        <template v-else-if="column.key==='version'"><span class="run-cell-muted">各应用使用各自的代码版本或制品</span></template>
        <template v-else-if="column.key==='status'"><span class="run-group-counts"><span v-for="count in executionRunCounts(record).split('、')" :key="count">{{ count }}</span></span></template>
        <template v-else-if="column.key==='created_at'"><time class="run-cell-time" :datetime="record.created_at">{{ formatRunTime(record.created_at) }}</time><span class="run-cell-secondary">最近运行</span></template>
      </template>
      <template v-else-if="column.key==='release_plan'">
        <button v-if="record.release_plan_id" type="button" class="run-resource-link" :title="runReleasePlanName(record)" @click.stop="openReleasePlanFromRun(record)">{{ runReleasePlanName(record) }}</button>
        <span v-else class="run-cell-muted">未关联发布计划</span>
      </template>
      <template v-else-if="column.key==='application'">
        <button type="button" class="run-resource-link" :disabled="!record.application_id" :title="applicationName(record)" @click.stop="openApplicationFromRun(record)">{{ applicationName(record) }}</button>
      </template>
      <template v-else-if="column.key==='workflow'">
        <button type="button" class="run-resource-link" :disabled="!record.application_id||!record.workflow_id" :title="runWorkflowName(record)" @click.stop="openApplicationWorkflow(record.application_id||'',record.workflow_id||'')">{{ runWorkflowName(record) }}</button>
      </template>
      <template v-else-if="column.key==='version'">
        <button class="resource-name" @click="expandRunListEntry(true,record)">{{ record.commit_message||'未记录提交说明' }}</button>
        <span class="run-cell-secondary" :title="runReferenceLabel(record)">{{ runReferenceLabel(record) }}</span>
      </template>
      <template v-else-if="column.key==='status'">
        <a-tag :color="runStatusColor(record.status)">{{ runStatusLabel(record.status,record.stage) }}</a-tag>
        <span class="run-cell-secondary" :title="record.current_node_name||record.current_node_id">{{ record.current_node_name||record.current_node_id||'尚未开始' }}</span>
      </template>
      <template v-else-if="column.key==='created_at'">
        <time class="run-cell-time" :datetime="record.created_at">{{ formatRunTime(record.created_at) }}</time>
        <span class="run-cell-secondary">{{ formatRunRelativeTime(record.created_at) }}</span>
      </template>
    </template>
  </a-table>
  <ResourceDetailDrawer v-model:open="runDrawerOpen" :title="runDrawerEntry ? (isRunExecutionGroup(runDrawerEntry) ? runEntryPlanName(runDrawerEntry) : applicationName(runDrawerEntry)) : '运行详情'">
    <template v-for="record in runDrawerEntry ? [runDrawerEntry] : []" :key="record.id">
      <div :id="`run-entry-${record.id}`" :class="{'run-execution-detail':record.executionGroup}">
        <header v-if="isRunExecutionGroup(record)" class="run-group-caption"><span><Layers3 :size="15"/>本次发布的应用</span><small :title="record.release_plan_execution_id">执行编号：{{ record.release_plan_execution_id.slice(0,8) }}；已加载 {{ record.runs.length }} 条运行记录</small></header>
        <article v-for="run in runEntryRecords(record)" :key="run.id" :class="{'run-group-item':record.executionGroup,'is-open':record.executionGroup&&expandedRunIDs.includes(run.id)}">
          <div v-if="record.executionGroup" class="run-group-row" :class="{'is-current':selectedRunID===run.id}" :data-row-key="run.id" @click="expandPipelineRun(!expandedRunIDs.includes(run.id),run)">
            <button type="button" class="run-row-toggle" :aria-expanded="expandedRunIDs.includes(run.id)" :aria-controls="`run-detail-${run.id}`" :aria-label="expandedRunIDs.includes(run.id)?'收起运行详情':'展开运行详情'" @click.stop="expandPipelineRun(!expandedRunIDs.includes(run.id),run)"><ChevronRight :size="15" :class="{expanded:expandedRunIDs.includes(run.id)}"/></button>
            <div><small>应用</small><button type="button" class="run-resource-link" :title="applicationName(run)" @click.stop="openApplicationFromRun(run)">{{ applicationName(run) }}</button></div>
            <div><small>流水线</small><button type="button" class="run-resource-link" :disabled="!run.workflow_id" :title="runWorkflowName(run)" @click.stop="openApplicationWorkflow(run.application_id,run.workflow_id||'')">{{ runWorkflowName(run) }}</button></div>
            <div><strong class="run-cell-title" :title="run.commit_message||'未记录提交说明'">{{ run.commit_message||'未记录提交说明' }}</strong><span class="run-cell-secondary" :title="runReferenceLabel(run)">{{ runReferenceLabel(run) }}</span></div>
            <div><a-tag :color="runStatusColor(run.status)">{{ runStatusLabel(run.status,run.stage) }}</a-tag><span class="run-cell-secondary">{{ run.current_node_name||run.current_node_id||'尚未开始' }}</span></div>
            <div><time class="run-cell-time" :datetime="run.created_at">{{ formatRunTime(run.created_at) }}</time><span class="run-cell-secondary">{{ formatRunRelativeTime(run.created_at) }}</span></div>
          </div>
          <section v-if="expandedRunIDs.includes(run.id)" :id="`run-detail-${run.id}`" class="run-detail" :class="{'is-current':selectedRunID===run.id}" :aria-label="`运行详情：${formatRunTime(run.created_at)}`">
            <header class="run-detail-heading">
              <div class="run-detail-identity"><span class="run-detail-label">{{ selectedRunID===run.id?'当前查看':'运行详情' }}</span><time :datetime="run.created_at">{{ formatRunTime(run.created_at) }}</time><span class="run-detail-version">{{ runReferenceLabel(run) }}</span></div>
              <a-button size="small" @click="runDrawerOpen=false">关闭详情</a-button>
            </header>
            <PipelineRunGraph :key="run.id" compact :graph="run.execution_graph" :current-node-id="run.current_node_id" :status="run.status" :stage="run.stage"/>
            <dl class="run-facts">
              <div><dt>发布应用</dt><dd>{{ applicationName(run) }}</dd></div>
              <div><dt>应用流水线</dt><dd>{{ runWorkflowName(run) }}</dd></div>
              <div><dt>发布计划</dt><dd>{{ run.release_plan_id ? runReleasePlanName(run) : '未关联发布计划' }}</dd></div>
              <div><dt>当前任务</dt><dd>{{ run.current_node_name||run.current_node_id||'尚未开始' }}</dd></div>
              <div><dt>状态说明</dt><dd :title="run.message||runStatusLabel(run.status,run.stage)">{{ run.message||runStatusLabel(run.status,run.stage) }}</dd></div>
              <div><dt>运行编号</dt><dd :title="run.id">{{ run.id }}</dd></div>
            </dl>
            <a-alert v-if="canReadDeployments&&runDeploymentErrors[run.id]" type="error" show-icon :message="rollbackT('rollback.recordsReadFailed')" :description="runDeploymentErrors[run.id]">
              <template #action><a-button size="small" :loading="runDeploymentLoading[run.id]" @click="loadRunDeploymentRecords(run.id)">{{ rollbackT('rollback.retryRecords') }}</a-button></template>
            </a-alert>
            <footer class="run-actions">
              <a-button @click="openLogs(run)">查看实时日志</a-button>
              <a-button v-if="canStopRun(run)&&auth.canAny(['pipeline_run.execute'])" danger :loading="stoppingRunID===run.id||run.stage==='stopping'" :disabled="!!stoppingRunID||run.stage==='stopping'" @click="confirmStopRun(run)">{{ t(run.stage==='stopping'?'pipelineStop.stopping':'pipelineStop.confirm') }}</a-button>
              <a-button v-if="run.status==='failed'&&auth.canAny(['pipeline_run.execute'])" type="primary" @click="openRetry(run)">重试流水线</a-button>
              <a-button v-if="runHasDeployments(run.id)" @click="openReleasePlanApplicationResources('',run.application_id,run.id)">{{ rollbackT('rollback.recordsAndRollback') }}</a-button>
              <span v-else-if="canReadDeployments&&runDeploymentLoading[run.id]" class="resource-secondary">{{ rollbackT('rollback.recordsLoading') }}</span>
              <a-button v-if="run.status==='awaiting_approval'&&auth.canAny(['deployment.review'])" type="primary" @click="action(`/pipeline-runs/${run.id}/approve`)">通过审核</a-button>
              <a-button v-if="run.stage==='manual'&&auth.canAny(['pipeline_run.execute'])" type="primary" @click="action(`/pipeline-runs/${run.id}/advance`)">放行并继续</a-button>
            </footer>
          </section>
        </article>
      </div>
    </template>
  </ResourceDetailDrawer>
</div>
<ReleasePlanWorkspace
 v-else
 :plans="releasePlans"
 :applications="applications"
 :pipeline-runs="runs"
 :loading="loading"
 :can-create="auth.canAny([`${sectionPermission}.create`])"
 :can-update="auth.canAny([`${sectionPermission}.update`])"
 :can-delete="auth.canAny([`${sectionPermission}.delete`])"
 :can-execute="auth.canAny([`${sectionPermission}.execute`])"
 :can-read-deployments="canReadDeployments"
 :activePlanID="activeReleasePlanID"
 :runnable-counts="releasePlanRunnableCounts"
 :runnable-application-ids="releasePlanRunnableApplicationIDs"
 :executing-plan-ids="executingReleasePlanIDs"
 :relative-now="relativeTimeNow"
 :mutatingPlanID="releasePlanMutationID"
 @create="create"
 @execute="openReleasePlan"
 @execute-application="openReleasePlanApplication"
 @edit="openReleasePlanEditor"
 @add-application="openReleaseGroupApplicationPicker"
 @toggle="toggleReleasePlan"
 @remove="removeReleasePlan"
 @remove-application="removeReleaseGroupApplication"
 @open-application-run="openReleasePlanApplicationRun"
 @open-application-resources="openReleasePlanApplicationResources"
/>

<a-drawer :open="formOpen" :title="creatingApplicationRepository?t('applicationForm.createRepository'):props.section==='applications'?(editingID?'编辑应用':'创建应用'):(editingID?'编辑配置':'新建配置')" width="700" :closable="!saving&&!testing" :mask-closable="!saving&&!testing" :keyboard="!saving&&!testing" @close="closeForm"><a-form layout="vertical">
<template v-if="formSection==='applications'">
 <section class="application-form-section">
  <header><strong>应用信息</strong><small>配置应用名称、说明和唯一绑定的代码仓库。</small></header>
  <div class="form-grid">
   <a-form-item label="应用名称" required><a-input v-model:value="appForm.name" :maxlength="128" placeholder="例如 order_service"/><small class="field-hint">应用名同时作为镜像仓库名；以小写英文字母开头，仅使用小写英文字母和单个下划线。</small></a-form-item>
   <a-form-item label="说明"><a-input v-model:value="appForm.description"/></a-form-item>
   <a-form-item class="span2" label="代码仓库" required>
    <div class="resource-picker">
     <a-select v-model:value="appForm.repository_id" show-search option-filter-prop="label" :options="repositoryOptions">
      <template #option="{label,details}"><span class="resource-option"><strong>{{ label }}</strong><small v-for="detail in details" :key="detail">{{ detail }}</small></span></template>
     </a-select>
     <a-button v-if="auth.canAny(['repository.create'])" class="resource-create" :aria-label="t('applicationForm.createRepository')" :title="t('applicationForm.createRepository')" @click="createApplicationRepository"><Plus :size="16"/></a-button>
    </div>
   </a-form-item>
  </div>
 </section>
 <section class="application-form-section application-workflow-association">
  <header><strong>关联流水线</strong><small>{{ editingID?'查看应用已有流水线，或继续选择已启用的公共流水线。':'可选择已启用的公共流水线；不选择则在保存应用后配置发布流程。' }}</small></header>
  <template v-if="editingID">
   <div v-if="editingApplication?.workflows?.length" class="workflow-association-list">
    <div v-for="workflow in editingApplication.workflows" :key="workflow.id" class="workflow-association-item">
     <span class="workflow-association-icon"><Workflow/></span>
     <span class="workflow-association-copy"><strong>{{ workflowDisplayName(workflow) }}</strong><small>{{ workflowOriginLabel(workflow) }}</small></span>
     <span class="workflow-association-actions">
      <a-tag :color="workflow.is_active?'green':'default'">{{ workflow.is_active?'已启用':'未启用' }}</a-tag>
      <a-button v-if="auth.canAny(['workflow.update'])" size="small" @click="editApplicationWorkflow(editingID,workflow.id)">编辑</a-button>
      <a-button v-if="auth.canAny(['workflow.delete'])" size="small" danger :loading="workflowRemovalID===workflow.id" :disabled="Boolean(workflowRemovalID)&&workflowRemovalID!==workflow.id" @click="removeApplicationWorkflow(workflow)"><Trash2/>{{ workflow.workflow_template_id?'解除':'删除' }}</a-button>
     </span>
    </div>
   </div>
   <div v-else class="workflow-association-empty">当前应用还没有流水线</div>
   <a-form-item label="选择流水线">
    <a-select v-model:value="selectedWorkflowTemplateID" allow-clear show-search option-filter-prop="label" :placeholder="availableWorkflowTemplates.length?'请选择已启用的流水线':'没有可添加的流水线'" :options="availableWorkflowTemplates.map(item=>({value:item.id,label:item.name}))"/>
    <small class="field-hint">选择后点击底部“保存”生效；会新增一条独立流水线，不会覆盖现有配置。</small>
   </a-form-item>
  </template>
  <a-form-item v-else label="选择流水线">
   <a-select v-model:value="appForm.workflow_template_id" allow-clear show-search option-filter-prop="label" placeholder="可选；不选择则下一步配置发布流程" :options="workflowTemplates.filter(item=>item.is_active).map(item=>({value:item.id,label:item.name}))"/>
  </a-form-item>
 </section>
 <section class="application-form-section">
  <header><strong>代码检查</strong><small>分支、PR/MR、Tag 和手动启动规则在流水线的代码源中配置；Webhook 只用于降低延迟。</small></header>
  <div class="form-grid">
   <a-form-item label="检查间隔"><a-select v-model:value="appForm.poll_interval_seconds" :options="[3,5,10,60].map(value=>({value,label:`${value} 秒`}))"/></a-form-item>
  </div>
 </section>
</template>
<template v-if="formSection==='repositories'">
 <div class="form-grid">
  <a-form-item label="名称" required><a-input v-model:value="repoForm.name"/></a-form-item>
  <a-form-item label="平台"><a-select v-model:value="repoForm.provider" :options="['github','gitlab','gitea','gitee','generic'].map(value=>({value,label:value}))"/></a-form-item>
  <a-form-item class="span2" label="Git 地址" required><a-input v-model:value="repoForm.clone_url"/></a-form-item>
  <a-form-item label="默认分支"><a-input v-model:value="repoForm.default_branch"/></a-form-item>
  <a-form-item :label="t('repositoryForm.gitAuthentication')"><a-select v-model:value="repoForm.auth_type" :options="[{value:'none',label:'无需认证'},{value:'token',label:'访问令牌'},{value:'ssh_key',label:'SSH 私钥'}]"/></a-form-item>
  <a-form-item v-if="repoForm.auth_type!=='none'" class="span2" :label="t('repositoryForm.gitCredential')">
   <div class="resource-picker">
    <a-select v-model:value="repoForm.credential_id" show-search option-filter-prop="label" :options="cloneCredentialOptions">
     <template #option="{label,details}"><span class="resource-option"><strong>{{ label }}</strong><small v-for="detail in details" :key="detail">{{ detail }}</small></span></template>
    </a-select>
    <a-button v-if="auth.canAny(['credential.create'])" class="resource-create" :aria-label="t('gitCredential.create')" :title="t('gitCredential.create')" @click="createRepositoryCredential('git')"><Plus :size="16"/></a-button>
   </div>
   <small>{{ t('repositoryForm.gitCredentialHint') }}</small>
  </a-form-item>
  <a-form-item v-if="repoForm.provider!=='generic'" class="span2" :label="t('repositoryForm.apiToken')">
   <div class="resource-picker">
    <a-select v-model:value="repoForm.api_credential_id" allow-clear show-search option-filter-prop="label" :placeholder="t(repoForm.auth_type==='token'?'repositoryForm.apiReusePlaceholder':'repositoryForm.apiTokenPlaceholder')" :options="apiCredentialOptions">
     <template #option="{label,details}"><span class="resource-option"><strong>{{ label }}</strong><small v-for="detail in details" :key="detail">{{ detail }}</small></span></template>
    </a-select>
    <a-button v-if="auth.canAny(['credential.create'])" class="resource-create" :aria-label="t('repositoryForm.createApiToken')" :title="t('repositoryForm.createApiToken')" @click="createRepositoryCredential('api')"><Plus :size="16"/></a-button>
   </div>
   <small>{{ t(repoForm.auth_type==='token'?'repositoryForm.apiReuseHint':'repositoryForm.apiTokenHint') }}</small>
  </a-form-item>
  <a-checkbox v-model:checked="repoForm.webhook_enabled">启用 Webhook</a-checkbox>
  <a-form-item v-if="repoForm.webhook_enabled" class="span2" label="Webhook API">
   <div class="webhook-api-field">
    <a-input :value="repositoryWebhookAPI" readonly :placeholder="formEditingID?'正在获取 Webhook API':'保存仓库后生成 Webhook API'"/>
    <a-button :disabled="!repositoryWebhookAPI" @click="copyRepositoryWebhookAPI">复制</a-button>
   </div>
   <small class="field-hint">{{ repositoryWebhookAPI?'将此地址填写到代码托管平台的 Webhook 配置中。':'仓库 ID 由系统在保存时生成，创建成功后会显示实际地址。' }}</small>
  </a-form-item>
 </div>
</template>
<template v-if="props.section==='build-plans'">
 <section class="build-form-section">
  <header><strong>基本信息</strong><small>只需选择构建方式即可使用默认配置。</small></header>
  <div class="form-grid">
   <a-form-item label="名称" required><a-input v-model:value="buildForm.name" placeholder="例如 API 服务构建"/></a-form-item>
   <a-form-item label="构建方式" required><a-select v-model:value="buildForm.kind" :options="[{value:'dockerfile',label:'Docker 镜像构建'},{value:'script',label:'Shell 脚本构建'}]"/></a-form-item>
  </div>
 </section>
 <section class="build-form-section">
	  <header><strong>{{ buildForm.kind==='dockerfile'?'镜像构建':'Shell 构建' }}</strong><small>{{ buildForm.kind==='dockerfile'?'默认读取仓库根目录 Dockerfile，并将镜像保留在当前构建运行时。':'脚本在已固定 Commit 的隔离检出目录中执行，可按需保存文件制品。' }}</small></header>
  <div v-if="buildForm.kind==='dockerfile'" class="form-grid">
   <a-form-item label="Dockerfile 路径" required><a-input v-model:value="buildForm.dockerfile_path" placeholder="Dockerfile"/></a-form-item>
   <a-form-item label="构建上下文" required><a-input v-model:value="buildForm.context_path" placeholder="."/></a-form-item>
   <a-alert class="span2" type="success" show-icon message="默认输出本地 OCI 镜像" description="未配置镜像仓库时，镜像保留在 EDO 当前构建运行时，可直接发布到同一 Docker 运行时。远程 Docker 和 Kubernetes 发布请在高级配置中选择镜像仓库。"/>
  </div>
  <div v-else class="form-grid">
   <a-form-item class="span2" label="构建脚本" required><a-textarea v-model:value="buildForm.script" :rows="8" placeholder="例如：npm ci&#10;npm run build"/></a-form-item>
	   <a-form-item class="span2" label="运行镜像" required><a-auto-complete v-model:value="buildForm.runtime_image" :options="runtimeImageOptions" placeholder="alpine:3.22"/><small class="field-hint">默认 alpine:3.22，也可输入其他镜像。镜像需包含 /bin/sh，并指定版本 Tag 或 Digest，不能使用 latest。</small></a-form-item>
	   <a-form-item label="工作目录" required><a-input v-model:value="buildForm.working_directory" placeholder="."/></a-form-item>
	   <a-form-item label="保存产物"><a-switch v-model:checked="buildSaveArtifact"/><small class="field-hint">关闭时只执行 Shell 构建，不登记文件制品。</small></a-form-item>
	   <a-form-item v-if="buildSaveArtifact" class="span2" label="产物路径" required><a-input v-model:value="buildForm.artifact_path" placeholder="例如 dist 或 bin/server"/></a-form-item>
	   <a-alert v-if="buildSaveArtifact" class="span2" type="info" show-icon message="产物归档由 EDO 自动完成" description="脚本成功后，EDO 会校验产物路径：目录确定性打包为 tar.gz，普通文件保持原格式，然后计算 SHA256 摘要并登记制品；脚本无需自行上传。"/>
	   <a-alert v-else class="span2" type="warning" show-icon message="本方案不保存文件制品" description="可用于测试、检查或只需执行命令的流水线；若后续存在主机脚本部署任务，保存流水线时会要求开启产物归档。"/>
  </div>
 </section>
 <a-collapse class="build-advanced" :bordered="false">
  <a-collapse-panel key="advanced">
   <template #header><span class="build-advanced-title"><strong>高级配置</strong><small>{{ buildForm.kind==='dockerfile'?'镜像仓库、缓存、构建参数和超时':'环境变量和超时' }}</small></span></template>
   <div class="form-grid">
    <a-form-item v-if="buildForm.kind==='dockerfile'" class="span2" label="镜像输出">
     <a-radio-group v-model:value="buildImageDestination" button-style="solid"><a-radio-button value="local">构建运行时本地镜像</a-radio-button><a-radio-button value="registry">推送镜像仓库</a-radio-button></a-radio-group>
    </a-form-item>
    <a-form-item v-if="buildForm.kind==='dockerfile'&&buildImageDestination==='registry'" class="span2" label="镜像仓库" required>
     <div class="resource-picker">
      <a-select v-model:value="buildForm.image_registry_id" show-search option-filter-prop="label" placeholder="选择已测试可用的镜像仓库" :options="registries.filter(item=>item.is_active).map(item=>({value:item.id,label:item.name}))">
       <template #option="{value,label}"><span class="managed-resource-option"><span class="managed-resource-option-label">{{ label }}</span><a class="managed-resource-option-view" :href="resourceViewHref('/image-registries','registry',String(value))" target="_blank" rel="noopener noreferrer" @mousedown.stop @click.stop>查看</a></span></template>
      </a-select>
      <a-button v-if="canCreate" class="resource-create" aria-label="创建镜像仓库" title="创建镜像仓库" @click="createImageRegistry"><Plus :size="16"/></a-button>
     </div>
    </a-form-item>
    <a-alert v-if="buildForm.kind==='dockerfile'&&buildImageDestination==='registry'" class="span2 image-path-alert" type="info" show-icon message="当前镜像路径">
     <template #description><code class="image-path-preview">{{ buildImagePathPreview }}</code><small class="field-hint">应用名是实际仓库名；版本标签使用 12 位提交短哈希，例如 fea2410d1e47。部署时仍会在后台固定并校验完整 Digest。</small></template>
    </a-alert>
    <a-form-item v-if="buildForm.kind==='dockerfile'" label="Docker target"><a-input v-model:value="buildForm.target_stage" placeholder="可选，多阶段构建目标"/></a-form-item>
    <a-alert v-if="buildForm.kind==='dockerfile'" class="span2" type="info" show-icon message="目标架构自动跟随部署主机" description="主机接入时会检测 AMD64 或 ARM64。流水线执行时按下游部署目标自动选择构建平台，多种架构且使用镜像仓库时会生成多架构镜像。"/>
    <a-form-item v-if="buildForm.kind==='dockerfile'" label="拉取基础镜像"><a-switch v-model:checked="buildForm.pull"/><small class="field-hint">默认开启，确保基础镜像及时更新。</small></a-form-item>
    <a-form-item v-if="buildForm.kind==='dockerfile'" label="启用本地构建缓存"><a-switch v-model:checked="buildForm.cache_enabled"/><small class="field-hint">默认开启，仅复用当前构建节点的 Docker/BuildKit 层缓存，不会向镜像仓库推送缓存标签。</small></a-form-item>
    <a-form-item v-if="buildForm.kind==='dockerfile'" class="span2" label="构建参数（每行 KEY=value）"><a-textarea v-model:value="buildArgsText" :rows="5" placeholder="VERSION=1.0.0&#10;ENABLE_FEATURE=true"/></a-form-item>
    <a-form-item v-if="buildForm.kind==='script'" class="span2" label="环境变量（每行 KEY=value）"><a-textarea v-model:value="buildEnvironmentText" :rows="5" placeholder="NODE_ENV=production&#10;CGO_ENABLED=0"/></a-form-item>
    <a-form-item label="超时（秒）" required><a-input-number v-model:value="buildForm.timeout_seconds" :min="30" :max="7200"/></a-form-item>
    <a-form-item class="span2" label="说明"><a-input v-model:value="buildForm.description" placeholder="可选，说明适用应用或制品用途"/></a-form-item>
   </div>
  </a-collapse-panel>
 </a-collapse>
</template>
<template v-if="props.section==='image-registries'">
 <div class="form-grid">
  <a-form-item label="名称" required><a-input v-model:value="registryForm.name"/></a-form-item>
  <a-form-item label="类型" required><a-select v-model:value="registryForm.provider" :options="[{value:'generic',label:'通用 Registry（阿里云 ACR 等）'},{value:'harbor',label:'Harbor'},{value:'docker_hub',label:'Docker Hub（官方）'}]"/></a-form-item>
  <a-alert v-if="dockerHubRegistry" class="span2" type="info" show-icon message="Docker Hub 使用系统固定地址" description="固定使用 docker.io，无需填写仓库地址；命名空间请填写 Docker Hub 用户名或组织名。"/>
  <a-form-item v-else class="span2" label="地址" required><a-input v-model:value="registryForm.endpoint" placeholder="例如 https://registry.cn-shenzhen.aliyuncs.com"/><small class="field-hint">填写 OCI Registry 服务地址，不要填写具体镜像仓库或镜像名称。</small></a-form-item>
  <a-form-item label="命名空间" required><a-input v-model:value="registryForm.namespace" :placeholder="dockerHubRegistry?'用户名或组织名':'例如 linabellbiu'"/></a-form-item>
  <a-form-item label="用户名"><a-input v-model:value="registryForm.username"/></a-form-item>
  <a-form-item class="span2" label="密码或 Token"><a-input-password v-model:value="registryForm.credential"/></a-form-item>
  <a-checkbox v-if="!dockerHubRegistry" v-model:checked="registryForm.allow_insecure_http">允许 HTTP（仅可信内网）</a-checkbox>
 </div>
 <a-alert v-if="registryTested" type="success" show-icon message="镜像仓库登录测试成功"/>
</template>
</a-form><template #footer><div class="drawer-actions"><a-button :disabled="saving||testing" @click="closeForm">{{ creatingApplicationRepository?t('applicationForm.backToApplication'):'取消' }}</a-button><a-button v-if="formSection==='repositories'&&auth.canAny(['repository.execute'])" :loading="testing" :disabled="saving" @click="testRepository">测试连接</a-button><a-button v-if="props.section==='image-registries'&&auth.canAny([`${sectionPermission}.execute`])" :loading="testing" @click="testRegistry">测试登录</a-button><a-button v-if="canSaveCurrent" type="primary" :loading="saving" :disabled="testing||(props.section==='image-registries'&&!registryTested)" @click="save">保存</a-button></div></template></a-drawer>
<GitCredentialDrawer v-model:open="credentialCreation.open" :provider="credentialCreation.provider" :auth-type="credentialCreation.authType" lock-context @saved="repositoryCredentialSaved"/>
<DeliveryWorkflowDrawer v-model:open="deliveryOpen" :application-id="deliveryApplicationID" :initial-kind="typeof route.query.delivery_kind==='string'?route.query.delivery_kind as DeliveryKind:undefined" :initial-cluster="typeof route.query.cluster==='string'?route.query.cluster:undefined" :initial-namespace="typeof route.query.namespace==='string'?route.query.namespace:undefined" :initial-workload="typeof route.query.workload==='string'?route.query.workload:undefined" :initial-container="typeof route.query.container==='string'?route.query.container:undefined" @created="deliveryCreated"/>
<DeploymentRollbackModal v-model:open="rollbackDialog.open" :sourceID="rollbackDialog.sourceID" :records="deployments" :canExecute="canReadDeployments&&canControlDeployments" @accepted="acceptRollbackRecord" @completed="completeRollbackRecord"/>
<a-modal v-model:open="manualPicker.open" :title="t('manualRun.title')" :confirm-loading="saving" :ok-button-props="{disabled:!manualPicker.applicationID||!manualPicker.workflowID}" :ok-text="t('manualRun.chooseVersion')" @ok="continueManualPicker" @cancel="resetManualFlow">
 <a-form layout="vertical">
  <a-form-item :label="t('manualRun.application')" required><a-select :value="manualPicker.applicationID||undefined" show-search option-filter-prop="label" :options="manualApplicationOptions" @change="changeManualApplication"/></a-form-item>
  <a-form-item :label="t('applicationCard.workflow')" required><a-select v-model:value="manualPicker.workflowID" :disabled="!manualPicker.applicationID" show-search option-filter-prop="label" :options="manualWorkflowOptions"/></a-form-item>
  <a-alert v-if="!manualApplicationOptions.length" type="info" show-icon :message="t('manualRun.noApplication')"/>
 </a-form>
</a-modal>
<a-modal v-model:open="commitOpen" :title="t('manualRun.versionTitle')" :confirm-loading="saving" :ok-button-props="{disabled:!selectedSource||(manualExecutionMode==='code'?!selectedRef:!selectedArtifactID)}" :ok-text="t('manualRun.confirm')" @ok="executeCommit" @cancel="resetManualFlow">
 <a-form layout="vertical">
  <a-form-item :label="t('manualRun.title')"><strong>{{ applications.find(item=>item.id===manualApplicationID)?.name }} / {{ workflowDisplayName(applications.find(item=>item.id===manualApplicationID)?.workflows.find(item=>item.id===manualWorkflowID)) }}</strong></a-form-item>
  <a-alert class="manual-release-note" type="info" show-icon :message="manualExecutionMode==='artifact'?t('manualRun.artifactHint'):t('manualRun.executionHint')"/>
  <a-form-item v-if="manualSources.length" :label="t('manualRun.source')">
   <a-select v-model:value="selectedSource" :options="manualSourceOptions">
    <template #option="{label,details}"><span class="resource-option"><strong>{{ label }}</strong><small v-for="detail in details" :key="detail">{{ detail }}</small></span></template>
   </a-select>
  </a-form-item>
  <a-form-item label="执行内容"><a-radio-group v-model:value="manualExecutionMode" button-style="solid"><a-radio-button value="code" :disabled="!commitOptions.length">代码版本</a-radio-button><a-radio-button value="artifact" :disabled="!manualArtifacts.length">已有制品</a-radio-button></a-radio-group></a-form-item>
  <a-alert v-if="manualReferenceError&&manualExecutionMode==='artifact'" class="manual-release-note" type="warning" show-icon :message="manualReferenceError"/>
  <a-form-item v-if="manualExecutionMode==='code'" :label="t('manualRun.ref')">
   <a-select v-model:value="selectedRef">
    <a-select-opt-group :label="t('manualRun.branch')"><a-select-option v-for="item in commitOptions.filter(item=>item.kind==='branch')" :key="item.ref" :value="item.ref" :label="item.name"><span class="resource-option"><strong>{{ item.name }}</strong><small>类型：分支；提交：{{ item.sha.slice(0,8) }}</small></span></a-select-option></a-select-opt-group>
    <a-select-opt-group :label="t('manualRun.tag')"><a-select-option v-for="item in commitOptions.filter(item=>item.kind==='tag')" :key="item.ref" :value="item.ref" :label="item.name"><span class="resource-option"><strong>{{ item.name }}</strong><small>类型：Tag；提交：{{ item.sha.slice(0,8) }}</small></span></a-select-option></a-select-opt-group>
   </a-select>
  </a-form-item>
  <a-form-item v-else label="历史制品"><a-select v-model:value="selectedArtifactID" show-search option-filter-prop="label" placeholder="选择之前构建或手工上传的可用制品" :options="manualArtifactOptions"><template #option="{label,details}"><span class="resource-option"><strong>{{ label }}</strong><small v-for="detail in details" :key="detail">{{ detail }}</small></span></template></a-select></a-form-item>
 </a-form>
</a-modal>
<a-modal :open="retryOpen" title="重试流水线" :confirm-loading="retrySubmitting" :ok-button-props="{disabled:retryLoading||!retryOptions||(retryMode==='artifact'&&!retryArtifactID)}" ok-text="确认重试" @ok="executeRetry" @cancel="resetRetry">
 <a-spin :spinning="retryLoading">
  <a-form v-if="retryOptions" layout="vertical">
   <a-alert class="manual-release-note" type="info" show-icon message="代码版本已经由原流水线运行固定，重试不能重新选择分支或 Tag。"/>
   <a-form-item label="固定代码版本"><a-input :value="formatGitReference({ref:retryOptions.ref,sha:retryOptions.commit_sha})" readonly/></a-form-item>
   <a-form-item label="重试方式"><a-radio-group v-model:value="retryMode" button-style="solid"><a-radio-button value="rebuild">按固定版本重新构建</a-radio-button><a-radio-button value="artifact" :disabled="!retryOptions.artifacts.length">使用原运行制品</a-radio-button></a-radio-group></a-form-item>
   <a-form-item v-if="retryMode==='artifact'" label="该次运行已构建的制品" required><a-select v-model:value="retryArtifactID" show-search option-filter-prop="label" placeholder="选择原失败运行已经构建的可用制品" :options="retryArtifactOptions"><template #option="{label,details}"><span class="resource-option"><strong>{{ label }}</strong><small v-for="detail in details" :key="detail">{{ detail }}</small></span></template></a-select></a-form-item>
   <a-alert v-if="retryMode==='artifact'" type="warning" show-icon message="将跳过该制品对应的构建和紧随其后的 Shell 检查，从后续审核、人工放行或部署继续。"/>
   <a-alert v-else-if="!retryOptions.artifacts.length" type="warning" show-icon message="该次运行没有已构建的可用制品，将使用原 Commit 重新构建。"/>
  </a-form>
 </a-spin>
</a-modal>
<a-modal
 :open="releasePlanAddApplicationOpen"
	 :title="t('releasePlan.addApplicationDialog.title')"
 :ok-text="t('releasePlan.addApplicationDialog.confirm')"
 :cancel-text="t('releasePlan.editor.cancel')"
 :confirm-loading="releasePlanMutationID === releasePlanAddApplicationPlanID"
 :closable="releasePlanMutationID !== releasePlanAddApplicationPlanID"
 :mask-closable="releasePlanMutationID !== releasePlanAddApplicationPlanID"
 :ok-button-props="{ disabled: !releasePlanAddApplicationIDs.length || !releasePlanAddApplicationOptions.length }"
 :cancel-button-props="{ disabled: releasePlanMutationID === releasePlanAddApplicationPlanID }"
 @ok="addReleaseGroupApplications"
 @cancel="resetReleaseGroupApplicationPicker"
>
 <a-form layout="vertical">
  <a-form-item :label="t('releasePlan.addApplicationDialog.field')" required>
   <a-select
    v-model:value="releasePlanAddApplicationIDs"
    mode="multiple"
    show-search
    allow-clear
    option-filter-prop="label"
    :disabled="!releasePlanAddApplicationOptions.length"
    :placeholder="t('releasePlan.addApplicationDialog.placeholder')"
    :options="releasePlanAddApplicationOptions"
   />
   <small class="release-plan-add-hint">{{ t('releasePlan.addApplicationDialog.hint') }}</small>
  </a-form-item>
  <a-alert v-if="!releasePlanAddApplicationOptions.length" type="info" show-icon :message="t('releasePlan.addApplicationDialog.empty')" />
 </a-form>
</a-modal>
<ReleasePlanExecuteModal
 :open="planExecutionOpen"
 :plan-title="planExecutionTitle"
 :groups="planExecutionGroups"
 @cancel="resetPlanExecution"
 @submit="executeReleasePlan"
 @retry="retryPlanExecutionApplication"
	 @update-ref="updatePlanExecutionRef"
	 @update-mode="updatePlanExecutionMode"
	 @update-artifact="updatePlanExecutionArtifact"
/>
<ReleasePlanEditorDrawer
 v-model:open="releasePlanEditorOpen"
 :plan="releasePlanEditorPlan"
 :applications="applications"
 :saving="saving"
 @save="saveReleasePlanEditor"
/>
<a-modal v-model:open="scaleDialog.open" title="调整 Kubernetes 副本数" ok-text="确认调整" cancel-text="取消" :confirm-loading="scaleDialog.submitting" @ok="submitScale">
 <a-form layout="vertical">
  <a-form-item label="目标副本数" required><a-input-number v-model:value="scaleDialog.replicas" :min="0" :max="1000" :precision="0" style="width:100%"/></a-form-item>
  <a-alert type="info" show-icon message="设置为 0 会停止工作负载" description="以后点击重启时，EDO 会恢复停止前保存的副本数；设置为大于 0 时会等待目标副本就绪。"/>
 </a-form>
</a-modal>
<a-modal v-model:open="shellControlDialog.open" title="配置 Shell 部署实例运行命令" ok-text="保存" cancel-text="取消" width="720" :confirm-loading="shellControlDialog.saving" :ok-button-props="{disabled:shellControlDialog.loading}" @ok="saveShellControlConfiguration">
 <a-spin :spinning="shellControlDialog.loading">
  <a-alert type="info" show-icon message="配置只作用于当前部署实例" description="同一应用使用其他部署方案或目标位置时会使用独立配置；运行时只能执行这里保存的非交互式命令。"/>
  <a-form layout="vertical" style="margin-top:16px">
   <a-form-item label="停止命令"><a-textarea v-model:value="shellControlDialog.stop_script" :rows="5" :maxlength="262144" placeholder="例如：systemctl --user stop my-service"/><small class="field-hint">留空会禁用该部署实例的停止操作。</small></a-form-item>
   <a-form-item label="重启命令"><a-textarea v-model:value="shellControlDialog.restart_script" :rows="5" :maxlength="262144" placeholder="例如：systemctl --user restart my-service"/><small class="field-hint">留空会禁用该部署实例的重启操作。</small></a-form-item>
   <a-form-item label="命令超时" required><a-input-number v-model:value="shellControlDialog.timeout_seconds" :min="30" :max="3600" :step="30" addon-after="秒"/></a-form-item>
  </a-form>
 </a-spin>
</a-modal>
<a-modal v-model:open="lifecycleOutput.open" :title="lifecycleOutput.title" :footer="null" width="720">
 <pre class="lifecycle-command-output">{{ lifecycleOutput.output }}</pre>
</a-modal>
<a-drawer v-model:open="releasePlanResources.open" :title="releasePlanResources.title" width="720">
 <div class="release-plan-resource-toolbar">
  <span>流水线运行 {{ releasePlanResources.pipelineRunID.slice(0,12) }}</span>
  <a-button :loading="releasePlanResources.loading" @click="loadReleasePlanApplicationResources"><RefreshCw :size="14"/>刷新状态</a-button>
 </div>
 <a-alert v-if="!canReadDeployments" type="warning" show-icon message="没有查看部署记录的权限" description="需要 deployment.read 权限后才能查看该应用本次执行产生的部署资源。"/>
 <a-spin v-else :spinning="releasePlanResources.loading">
  <a-alert v-if="releasePlanResources.error" type="error" show-icon :message="releasePlanResources.error"/>
  <div v-else-if="releasePlanResources.records.length" class="release-plan-resource-list">
   <article v-for="record in releasePlanResources.records" :key="record.id" class="release-plan-resource-item">
    <header>
     <span class="application-deployment-icon" :class="`kind-${deploymentKind(record)}`"><Box v-if="deploymentKind(record)==='docker'||deploymentKind(record)==='compose'"/><Layers3 v-else-if="deploymentKind(record)==='kubernetes'"/><Server v-else/></span>
     <span class="release-plan-resource-copy"><strong>{{ deploymentInstanceName(record) }}</strong><small>类型：{{ deploymentKindLabel(record) }}；运行位置：{{ record.target_name }}</small></span>
     <span class="application-deployment-state" :class="[`tone-${deploymentStatus(record).tone}`,{'is-live':deploymentStatus(record).live}]"><i/><strong>{{ deploymentStatus(record).label }}</strong><time>{{ formatApplicationTime(record.finished_at||record.updated_at||record.created_at) }}</time></span>
    </header>
    <dl>
     <div><dt>镜像</dt><dd :title="deploymentImageLabel(record)">{{ deploymentImageLabel(record) }}</dd></div>
     <div v-if="deploymentKind(record)==='docker'"><dt>容器实时状态</dt><dd>{{ dockerContainerState(record) }}</dd></div>
     <div v-if="record.error_message"><dt>失败原因</dt><dd :title="record.error_message">{{ record.error_message }}</dd></div>
    </dl>
    <div v-if="deploymentKind(record)==='docker'" class="release-plan-resource-actions">
     <a-button v-if="canReadContainerLogs" :loading="dockerRuntimeLoading[record.runtime_id]" :disabled="!dockerContainerForDeployment(record)" @click="openDeploymentLogs(record)"><FileText :size="14"/>查看日志</a-button>
     <a-button v-if="canOpenContainerTerminal&&canReadContainerLogs" :disabled="dockerRuntimeLoading[record.runtime_id]||dockerContainerForDeployment(record)?.state!=='running'" :title="dockerContainerForDeployment(record)?.state==='running'?'':'容器只有在实时运行中才能打开终端'" @click="openDeploymentTerminal(record)"><TerminalSquare :size="14"/>打开终端</a-button>
     <span v-if="!canReadContainerLogs" class="release-plan-resource-permission">没有 cluster.read 权限，无法读取容器实时状态和日志。</span>
     <span v-else-if="!canOpenContainerTerminal" class="release-plan-resource-permission">没有 terminal.open 权限，无法打开容器终端。</span>
    </div>
    <div v-else-if="deploymentKind(record)==='kubernetes'" class="release-plan-resource-actions">
     <a-button v-if="canReadDeployments&&canControlDeployments" @click="openDeploymentRollback(record)"><RotateCw :size="14"/>{{ rollbackT('rollback.title') }}</a-button>
    </div>
    <small v-else class="release-plan-resource-hint">当前仅支持从 Docker 单容器部署记录直接打开容器日志和终端。</small>
   </article>
  </div>
  <a-empty v-else-if="!releasePlanResources.loading" description="该应用本次流水线运行还没有部署记录"/>
 </a-spin>
</a-drawer>
<PipelineLogDrawer v-model:open="log.open" :runID="log.runID" :title="log.title" :initial-status="log.status"/>
<ContainerLogDrawer v-model:open="containerLogs.open" :title="containerLogs.title" :path="containerLogs.path"/>
<TerminalDrawer v-model:open="terminal.open" :title="terminal.title" :path="terminal.path"/>
</section></template>

<style scoped>
.application-failed-deployments{margin-top:14px}.application-failed-deployments h4{margin:0 0 8px;color:var(--edo-text)}.application-failed-deployments article{padding:12px;border:1px solid var(--edo-border);border-radius:8px}.application-failed-deployments article+article{margin-top:8px}.application-failed-deployments p{margin:6px 0;overflow-wrap:anywhere}.application-failed-deployments .ant-btn{margin:4px 6px 0 0}

.resource-option{display:grid;gap:2px;padding-block:2px}.resource-option strong{overflow:hidden;color:var(--edo-text);font-weight:600;text-overflow:ellipsis;white-space:nowrap}.resource-option small{overflow:hidden;color:var(--edo-muted);font-size:11px;text-overflow:ellipsis;white-space:nowrap}
.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.span2{grid-column:1/-1}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}.release-plan-add-hint{display:block;margin-top:6px;color:var(--edo-muted);font-size:11px}
.webhook-api-field{display:flex;gap:8px}.webhook-api-field :deep(.ant-input){min-width:0}.webhook-api-field :deep(.ant-btn){flex:0 0 auto}
.lifecycle-command-output{max-height:420px;margin:0;overflow:auto;padding:12px;border-radius:9px;color:var(--edo-text);background:#111827;font:12px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace;white-space:pre-wrap;word-break:break-word}
.release-plan-resource-toolbar{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:14px;color:var(--edo-muted);font-size:11px}.release-plan-resource-toolbar :deep(.ant-btn),.release-plan-resource-actions :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}.release-plan-resource-list{display:grid;gap:10px}.release-plan-resource-item{padding:13px;border:1px solid var(--edo-border);border-radius:12px;background:var(--edo-surface-soft)}.release-plan-resource-item>header{display:grid;align-items:center;grid-template-columns:36px minmax(0,1fr) auto;gap:10px}.release-plan-resource-copy{min-width:0}.release-plan-resource-copy strong,.release-plan-resource-copy small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.release-plan-resource-copy strong{font-size:13px}.release-plan-resource-copy small{margin-top:2px;color:var(--edo-muted);font-size:10px}.release-plan-resource-item>dl{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:7px;margin:11px 0 0}.release-plan-resource-item>dl>div{min-width:0;padding:8px 9px;border-radius:8px;background:var(--edo-surface)}.release-plan-resource-item dt{color:var(--edo-muted);font-size:9px}.release-plan-resource-item dd{overflow:hidden;margin:3px 0 0;text-overflow:ellipsis;white-space:nowrap;font-size:11px}.release-plan-resource-actions{display:flex;align-items:center;flex-wrap:wrap;gap:7px;margin-top:10px}.release-plan-resource-permission,.release-plan-resource-hint{color:var(--edo-muted);font-size:10px}.release-plan-resource-hint{display:block;margin-top:9px}
.resource-section-stack{display:grid;gap:14px}
.build-form-section,.application-form-section{margin-bottom:16px;padding:15px 15px 0;border:1px solid var(--edo-border);border-radius:12px;background:var(--edo-surface-soft)}.build-form-section>header,.application-form-section>header{margin-bottom:13px}.build-form-section>header strong,.build-form-section>header small,.application-form-section>header strong,.application-form-section>header small{display:block}.build-form-section>header strong,.application-form-section>header strong{font-size:14px}.build-form-section>header small,.application-form-section>header small{margin-top:3px;color:var(--edo-muted);font-size:11px}.resource-picker{display:flex;align-items:stretch;gap:8px}.resource-picker :deep(.ant-select){min-width:0;flex:1}.resource-create{width:34px;flex:0 0 34px;padding:0}.build-advanced{margin-bottom:12px;border:1px solid var(--edo-border);border-radius:12px;background:var(--edo-surface-soft)}.build-advanced :deep(.ant-collapse-header){align-items:center!important}.build-advanced :deep(.ant-collapse-content-box){padding-top:4px!important}.build-advanced-title{display:flex;align-items:baseline;gap:9px}.build-advanced-title small,.field-hint{color:var(--edo-muted);font-size:11px}.field-hint{display:block;margin-top:5px}.build-form-section :deep(.ant-alert),.application-form-section :deep(.ant-alert){margin-bottom:16px}
.workflow-association-list{display:grid;gap:8px;margin-bottom:16px}.workflow-association-item{display:grid;min-width:0;align-items:center;grid-template-columns:34px minmax(0,1fr) auto;gap:9px;padding:9px;border:1px solid var(--edo-border);border-radius:10px;background:var(--edo-surface)}.workflow-association-icon{display:grid;width:34px;height:34px;place-items:center;border-radius:9px;color:var(--edo-primary);background:var(--edo-primary-soft)}.workflow-association-icon svg{width:17px}.workflow-association-copy{min-width:0}.workflow-association-copy strong,.workflow-association-copy small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.workflow-association-copy strong{font-size:13px}.workflow-association-copy small{margin-top:2px;color:var(--edo-muted);font-size:11px}.workflow-association-actions{display:flex;align-items:center;gap:6px}.workflow-association-actions :deep(.ant-tag){margin-inline-end:0}.workflow-association-actions :deep(.ant-btn){display:inline-flex;align-items:center;gap:4px}.workflow-association-actions svg{width:13px}.workflow-association-empty{margin-bottom:16px;padding:15px;border:1px dashed var(--edo-border);border-radius:10px;color:var(--edo-muted);background:var(--edo-surface);text-align:center}.application-workflow-association .resource-picker :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}.application-workflow-association .resource-picker svg{width:14px}
.image-path-preview{display:block;overflow-wrap:anywhere;color:var(--edo-text);font-size:12px}.image-path-alert .field-hint{line-height:1.55}.application-deployment-list{display:grid;max-height:360px;gap:6px;overflow-y:auto;scrollbar-width:thin}.application-deployment-icon{display:grid;width:30px;height:30px;place-items:center;border-radius:8px;color:var(--edo-primary);background:var(--edo-primary-soft)}.application-deployment-icon svg{width:15px}.application-deployment-state{display:grid;min-width:70px;grid-template-columns:7px auto;align-items:center;column-gap:5px;color:#949aa5}.application-deployment-state>i{width:6px;height:6px;border-radius:50%;background:currentColor}.application-deployment-state strong{font-size:10px;white-space:nowrap}.application-deployment-state time{grid-column:1/-1;margin-top:2px;color:var(--edo-muted);font-size:9px;white-space:nowrap}.application-deployment-state.tone-info{color:#4f7df3}.application-deployment-state.tone-success{color:#2ab573}.application-deployment-state.tone-warning{color:#dfa126}.application-deployment-state.tone-danger{color:#ed5965}.application-deployment-state.is-live>i{animation:application-pulse 2s ease-out infinite}.application-deployment-item{min-width:0;overflow:hidden;border-radius:9px;background:var(--edo-surface)}.application-deployment-summary{display:grid;width:100%;min-width:0;align-items:center;grid-template-columns:30px minmax(0,1fr) auto 14px;gap:8px;padding:8px;border:0;color:var(--edo-text);background:transparent;cursor:pointer;text-align:left}.application-deployment-summary:hover{background:color-mix(in srgb,var(--edo-primary) 4%,var(--edo-surface))}.application-deployment-summary:focus-visible{outline:2px solid color-mix(in srgb,var(--edo-primary) 45%,transparent);outline-offset:-2px}.application-deployment-summary>svg{width:14px;color:var(--edo-muted);transition:transform 180ms ease}.application-deployment-summary>svg.expanded{transform:rotate(180deg)}.application-deployment-icon.kind-compose{color:#1677ff;background:color-mix(in srgb,#1677ff 10%,var(--edo-surface))}.application-deployment-icon.kind-kubernetes{color:#326ce5;background:color-mix(in srgb,#326ce5 10%,var(--edo-surface))}.application-deployment-icon.kind-script{color:#596273;background:color-mix(in srgb,#596273 10%,var(--edo-surface))}.application-deployment-copy{min-width:0}.application-deployment-copy strong,.application-deployment-copy small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.application-deployment-copy strong{font-size:12px}.application-deployment-copy small{margin-top:2px;color:var(--edo-muted);font-size:10px}.application-deployment-details{padding:0 9px 9px 47px}.application-deployment-details dl{display:grid;gap:5px;margin:0;padding:8px 9px;border-radius:8px;background:var(--edo-surface-soft)}.application-deployment-details dl>div{display:grid;min-width:0;grid-template-columns:62px minmax(0,1fr);gap:8px}.application-deployment-details dt{color:var(--edo-muted);font-size:10px}.application-deployment-details dd{overflow:hidden;margin:0;text-overflow:ellipsis;white-space:nowrap;font-size:10px}.application-deployment-actions{display:flex;flex-wrap:wrap;justify-content:flex-end;gap:6px;margin-top:7px}.application-deployment-actions :deep(.ant-btn){display:inline-flex;align-items:center;gap:4px}.application-deployment-actions svg{width:13px}.deployment-details-enter-active,.deployment-details-leave-active{transition:opacity 140ms ease,transform 160ms ease}.deployment-details-enter-from,.deployment-details-leave-to{opacity:0;transform:translateY(-3px)}
.run-workspace{min-width:0;overflow:hidden;background:var(--edo-surface)}
.run-list-heading{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:10px 14px;border-bottom:1px solid var(--edo-border)}
.run-list-heading>span{display:flex;align-items:center;gap:12px;font-size:13px}.run-list-heading small{color:var(--edo-muted);font-size:12px}
.run-list-controls{display:flex;align-items:center;gap:12px}
.run-table :deep(.ant-table){font-size:13px}.run-table :deep(.ant-table-thead>tr>th){padding:10px;color:var(--edo-muted);background:var(--edo-surface-soft);font-weight:500;white-space:nowrap}
.run-table :deep(.ant-table-tbody>tr.ant-table-row>td){height:64px;padding:8px 10px;border-bottom:1px solid var(--edo-border);cursor:pointer}
.run-table :deep(.ant-table-row){scroll-margin-block:102px 16px}
.run-table :deep(.ant-table-tbody>tr.run-row-expanded>td){background:color-mix(in srgb,var(--edo-primary) 5%,var(--edo-surface));border-bottom-color:color-mix(in srgb,var(--edo-primary) 22%,var(--edo-border))}
.run-table :deep(.ant-table-tbody>tr.run-row-current>td){background:var(--edo-primary-soft)}
.run-table :deep(.run-row-expanded>td:first-child){box-shadow:inset 3px 0 color-mix(in srgb,var(--edo-primary) 40%,var(--edo-border))}
.run-table :deep(.run-row-current>td:first-child){box-shadow:inset 3px 0 var(--edo-primary)}
.run-table :deep(.ant-table-expanded-row>td){padding:10px 12px 14px 36px;border-bottom:2px solid var(--edo-border);background:color-mix(in srgb,var(--edo-border) 45%,var(--edo-bg))}
.run-table :deep(.ant-table-pagination){margin:16px;row-gap:10px}.run-table :deep(.ant-tag){margin-inline-end:0}
.run-row-toggle{display:grid;width:28px;height:28px;place-items:center;padding:0;border:0;border-radius:6px;color:var(--edo-muted);background:transparent;cursor:pointer}
.run-row-toggle:hover{color:var(--edo-primary);background:var(--edo-primary-soft)}.run-row-toggle:focus-visible,.run-resource-link:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
.run-row-toggle svg{transition:transform 160ms ease}.run-row-toggle svg.expanded{transform:rotate(90deg)}
.run-resource-link{display:-webkit-box;max-width:100%;overflow:hidden;padding:2px 0;border:0;color:var(--edo-primary);background:transparent;cursor:pointer;font:inherit;line-height:1.5;text-align:left;overflow-wrap:anywhere;-webkit-box-orient:vertical;-webkit-line-clamp:2}
.run-resource-link:hover{text-decoration:underline}.run-resource-link:disabled{color:var(--edo-muted);cursor:default;text-decoration:none}
.run-cell-title,.run-cell-secondary{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.run-cell-title{font-weight:600}
.run-cell-muted,.run-cell-secondary{color:var(--edo-muted);font-size:12px}.run-cell-secondary{margin-top:3px}.run-cell-time{white-space:nowrap;font-variant-numeric:tabular-nums}
.run-detail{min-width:0;padding:12px;border:1px solid var(--edo-border);border-radius:8px;background:var(--edo-surface);box-shadow:var(--edo-shadow)}
.run-detail.is-current{border-color:color-mix(in srgb,var(--edo-primary) 55%,var(--edo-border))}
.run-detail-heading{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:10px;padding-bottom:8px;border-bottom:1px solid var(--edo-border)}
.run-detail-identity{display:flex;flex-wrap:wrap;align-items:center;gap:8px 14px;min-width:0;font-size:13px}
.run-detail-label{padding:3px 8px;border-radius:5px;color:var(--edo-muted);background:var(--edo-surface-soft);font-weight:600}
.is-current .run-detail-label{color:var(--edo-primary);background:var(--edo-primary-soft)}
.run-detail-identity time{font-weight:600;font-variant-numeric:tabular-nums}.run-detail-version{color:var(--edo-muted);overflow-wrap:anywhere}
.run-detail-heading :deep(.ant-btn){display:inline-flex;flex-shrink:0;align-items:center;gap:5px}.run-collapse-icon{transform:rotate(180deg)}
.run-facts{display:grid;grid-template-columns:minmax(150px,.7fr) minmax(200px,1.4fr) minmax(240px,1fr);gap:8px;margin:10px 0 0}
.run-facts>div{min-width:0;padding:8px 10px;border:1px solid var(--edo-border);border-radius:6px;background:var(--edo-surface)}
.run-facts dt{color:var(--edo-muted);font-size:12px}.run-facts dd{overflow-wrap:anywhere;margin:3px 0 0;font-size:12px}
.run-actions{display:flex;flex-wrap:wrap;gap:8px;margin-top:10px}.run-actions .run-collapse{display:inline-flex;align-items:center;gap:5px;margin-left:auto;color:var(--edo-muted)}.run-collapse svg{transform:rotate(180deg)}
.run-group-caption{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:0 0 9px}.run-group-caption>span{display:flex;align-items:center;gap:6px;font-weight:600}.run-group-caption small{color:var(--edo-muted);font-size:12px}
.run-group-counts{display:flex;flex-wrap:wrap;gap:2px 8px;font-size:12px;line-height:1.7}.run-group-counts>span{white-space:nowrap}.run-group-item{overflow:hidden;margin-top:6px;border:1px solid var(--edo-border);border-radius:8px;background:var(--edo-surface)}
.run-group-row{display:grid;min-height:64px;grid-template-columns:28px minmax(100px,.8fr) minmax(150px,1fr) minmax(180px,1.4fr) 130px 170px;align-items:center;gap:10px;padding:8px 10px;cursor:pointer;scroll-margin-block:102px 16px}
.run-group-row>div{min-width:0}.run-group-row small{color:var(--edo-muted);font-size:12px}.run-group-row:hover{background:var(--edo-surface-soft)}.run-group-row.is-current{background:var(--edo-primary-soft);box-shadow:inset 3px 0 var(--edo-primary)}
.run-group-item>.run-detail{margin:0 10px 10px 38px;box-shadow:none}.run-group-item.is-open{border-color:color-mix(in srgb,var(--edo-primary) 30%,var(--edo-border))}
@media(prefers-reduced-motion:reduce){.run-row-toggle svg{transition:none}}
.manual-release-note{margin-bottom:16px}
@keyframes application-pulse{0%{box-shadow:0 0 0 0 color-mix(in srgb,currentColor 32%,transparent)}70%{box-shadow:0 0 0 7px transparent}100%{box-shadow:0 0 0 0 transparent}}
@media(max-width:1100px){.run-facts{grid-template-columns:repeat(2,minmax(0,1fr))}}
@media(max-width:640px){.form-grid{grid-template-columns:1fr}.span2{grid-column:auto}.build-advanced-title{align-items:flex-start;flex-direction:column;gap:2px}.workflow-association-item{align-items:start;grid-template-columns:34px minmax(0,1fr)}.workflow-association-actions{grid-column:2;flex-wrap:wrap}.run-facts{grid-template-columns:1fr}.release-plan-resource-item>header{grid-template-columns:36px minmax(0,1fr)}.release-plan-resource-item>header .application-deployment-state{grid-column:2}.release-plan-resource-item>dl{grid-template-columns:1fr}}
@media(prefers-reduced-motion:reduce){.application-deployment-state.is-live>i{animation:none}}
</style>
<style scoped src="../styles/application-workspace.css"></style>
