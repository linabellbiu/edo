<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Modal, message } from 'ant-design-vue'
import type { UploadProps } from 'ant-design-vue'
import { Boxes, ChevronRight, Clock3, Download, GitBranch, GitCommit, Package, Play, Plus, RefreshCw, Settings2, Upload, Workflow } from 'lucide-vue-next'

import client from '@/api/client'
import { apiErrorMessage, type ResourceRecord } from '@/api/resources'
import PageToolbar from '@/components/PageToolbar.vue'
import PipelineLogDrawer from '@/components/PipelineLogDrawer.vue'
import PipelineRunGraph from '@/components/PipelineRunGraph.vue'
import ReleasePlanEditorDrawer from '@/components/ReleasePlanEditorDrawer.vue'
import ReleasePlanExecuteModal from '@/components/ReleasePlanExecuteModal.vue'
import ReleasePlanWorkspace from '@/components/ReleasePlanWorkspace.vue'
import ResourceTable from '@/components/ResourceTable.vue'
import { useAuthStore } from '@/stores/auth'
import type { PipelineExecutionGraph, WorkflowNode } from '@/types/pipeline'
import { formatGitReference } from '@/utils/gitReference'

type Section='applications'|'repositories'|'build-plans'|'image-registries'|'release-plans'
const props=defineProps<{section:Section}>(),route=useRoute(),router=useRouter(),auth=useAuthStore()
const {t,locale}=useI18n()
interface Repository extends ResourceRecord{id:string;name:string;provider:string;clone_url:string;default_branch:string;webhook_enabled:boolean;webhook_url?:string;is_active:boolean;auth_type:string;username?:string;allow_insecure_http:boolean;has_credential?:boolean;credential_id?:string;api_credential_id?:string}
interface Credential{id:string;name:string;provider:string;auth_type:string;username?:string;secret_hint:string}
interface BuildPlan extends ResourceRecord{id:string;name:string;kind:'dockerfile'|'script';description:string;script?:string;dockerfile_path?:string;context_path:string;working_directory:string;artifact_path?:string;runtime_image?:string;image_registry_id?:string;target_stage?:string;platform?:string;pull:boolean;cache_enabled:boolean;build_args:Record<string,string>;environment_variables:Record<string,string>;timeout_seconds:number;is_active:boolean;created_at:string;updated_at:string;image_registry?:Registry}
interface Registry extends ResourceRecord{id:string;name:string;provider:string;endpoint:string;namespace:string;has_credential:boolean;is_active:boolean}
interface Artifact extends ResourceRecord{id:string;application_id:string;build_run_id:string;pipeline_run_id?:string;kind:'oci_image'|'file_bundle';status:'available'|'expired'|'corrupt';name:string;original_name?:string;media_type?:string;digest:string;size_bytes:number;storage_kind:'local_file'|'registry'|'docker_daemon';image_ref?:string;created_at:string;updated_at:string}
interface WorkflowTemplate{id:string;name:string;revision:number;is_active:boolean}
 interface Application extends ResourceRecord{id:string;name:string;description:string;repository_id:string;poll_interval_seconds:number;workflow_template_id?:string;last_observed_commit?:string;sync_status:string;sync_message?:string;last_checked_at?:string;is_active:boolean;repository?:Repository;workflow_template?:WorkflowTemplate;workflow?:{is_active:boolean;revision:number;source?:WorkflowNode} }
 interface Run extends ResourceRecord{id:string;application_id:string;trigger:string;ref:string;commit_sha:string;commit_message?:string;status:string;stage:string;message?:string;created_at:string;updated_at?:string;application?:Application;environment?:string;current_node_id?:string;current_node_name?:string;created_by?:string;image?:string;execution_graph?:PipelineExecutionGraph}
type StatusTone='neutral'|'info'|'success'|'warning'|'danger'
interface StatusMeta{tone:StatusTone;label:string;live:boolean}
 interface ReleasePlan extends ResourceRecord{id:string;name:string;version:string;description:string;status:string;is_active?:boolean;created_at:string;updated_at?:string;latest_execution?:{id:string;status:string;created_at:string;finished_at?:string};groups?:Array<{id:string;name:string;mode:string;failure_policy:string;sort_order?:number;dependencies?:Array<{depends_on_group_id:string}>;applications:Array<{id:string;application_id:string;application?:Application;manual_deploy?:boolean;source_type?:string;source_value?:string;sort_order?:number}>}>}
type ReleasePlanGroup=NonNullable<ReleasePlan['groups']>[number]
type ReleasePlanGroupApplication=ReleasePlanGroup['applications'][number]
interface GitRef{name:string;sha:string} interface RefResult{branches:GitRef[];tags:GitRef[];manual_sources?:Array<{id:string;name:string;environment?:string}>}
type PlanExecutionLoadState='idle'|'loading'|'ready'|'blocked'|'error'
interface PlanExecutionReference{kind:'branch'|'tag';ref:string;name:string;sha:string}
interface PlanExecutionSource{id:string;name:string;environment?:string}
interface PlanExecutionItem{membershipID:string;applicationID:string;applicationName:string;workflowRevision:number;loadState:PlanExecutionLoadState;reason?:string;staticBlocked:boolean;sources:PlanExecutionSource[];refs:PlanExecutionReference[];selectedSourceID:string;selectedRef:string}
interface PlanExecutionGroup{id:string;name:string;mode:string;failurePolicy:string;dependencies:string[];items:PlanExecutionItem[]}
interface ReleasePlanEditorValue{id:string;description:string;groups:Array<{id:string;name:string;mode:'parallel'|'sequential';failure_policy:'stop'|'continue';depends_on_group_ids:string[];applications:Array<{application_id:string;manual_deploy:boolean;source_type:string;source_value:string}>}>}

const applications=ref<Application[]>([]),repositories=ref<Repository[]>([]),credentials=ref<Credential[]>([]),buildPlans=ref<BuildPlan[]>([]),registries=ref<Registry[]>([]),workflowTemplates=ref<WorkflowTemplate[]>([]),runs=ref<Run[]>([]),releasePlans=ref<ReleasePlan[]>([]),deployments=ref<ResourceRecord[]>([]),artifacts=ref<Artifact[]>([])
const loading=ref(false),saving=ref(false),resourceMutationID=ref(''),formOpen=ref(false),editingID=ref(''),registryTested=ref(false),testing=ref(false),manualOpen=ref(false),manualApplicationID=ref(''),manualApplications=ref<Application[]>([]),commitOpen=ref(false),commitOptions=ref<Array<{ref:string;name:string;sha:string;kind:'branch'|'tag'}>>([]),selectedRef=ref(''),selectedSource=ref(''),manualSources=ref<Array<{id:string;name:string;environment?:string}>>([]),currentRun=ref<Run|null>(null),currentRunSelectionKey=ref(''),selectedRunID=ref(''),log=ref({open:false,runID:'',title:'',status:''})
const buildImageDestination=ref<'local'|'registry'>('local'),buildArgsText=ref(''),buildEnvironmentText=ref(''),artifactApplicationID=ref(''),artifactLoading=ref(false),artifactUploading=ref(false),artifactDownloadingID=ref('')
const planExecutionOpen=ref(false),planExecutionPlanID=ref(''),planExecutionTitle=ref(''),planExecutionExpectedUpdatedAt=ref(''),planExecutionRequestID=ref(''),planExecutionGroups=ref<PlanExecutionGroup[]>([]),planExecutionSubmitting=ref(false)
const releasePlanEditorOpen=ref(false),releasePlanEditorPlan=ref<ReleasePlan|null>(null),releasePlanMutationID=ref('')
const releasePlanAddApplicationOpen=ref(false),releasePlanAddApplicationPlanID=ref(''),releasePlanAddApplicationGroupID=ref(''),releasePlanAddApplicationIDs=ref<string[]>([])
let releaseTimer=0
let planExecutionController:AbortController|null=null
const appForm=reactive({name:'',description:'',repository_id:'',poll_interval_seconds:5,workflow_template_id:''})
const repoForm=reactive({name:'',provider:'github',clone_url:'',default_branch:'main',auth_type:'none',username:'',credential_id:'',api_credential_id:'',webhook_enabled:true,allow_insecure_http:false})
const DEFAULT_RUNTIME_IMAGE='alpine:3.22'
const runtimeImageOptions=['alpine:3.22','node:24-alpine','golang:1.26-alpine','maven:3.9-eclipse-temurin-21-alpine'].map(value=>({value}))
const buildForm=reactive({name:'',kind:'dockerfile' as 'dockerfile'|'script',description:'',script:'',dockerfile_path:'Dockerfile',context_path:'.',working_directory:'.',artifact_path:'',runtime_image:DEFAULT_RUNTIME_IMAGE,image_registry_id:'',target_stage:'',platform:'',pull:true,cache_enabled:true,timeout_seconds:1800})
const registryForm=reactive({name:'',provider:'harbor',endpoint:'https://',namespace:'',username:'',credential:'',allow_insecure_http:false})
const releaseForm=reactive({description:'',application_ids:[] as string[]})

const copy:Record<Section,{description:string}>={applications:{description:'一个应用对应一个代码仓库；分支、事件、构建和部署都由流水线方案定义。'},repositories:{description:'统一管理 Git 来源和可选 Webhook；凭据来自当前用户自己的令牌。'},'build-plans':{description:'保存可复用的 Dockerfile 或脚本构建配置。'},'image-registries':{description:'管理 Harbor、Docker Hub 或其他 OCI Registry；保存前必须完成真实登录测试。'},'release-plans':{description:'发布计划组织人工批量发布；流水线运行与发布记录独立展示。'}}
const releaseView=computed(()=>route.query.view==='runs'?'runs':route.query.view==='records'?'records':'plans')
const canManage=computed(()=>props.section==='repositories'?auth.canAny(['repository.manage']):auth.canAny(['delivery.manage']))
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
const currentDescription=computed(()=>props.section==='release-plans'?(releaseView.value==='runs'?'查看代码事件或手动操作触发的执行、当前任务和实时日志。':releaseView.value==='records'?'查看已经进入真实部署环节的执行结果。':copy[props.section].description):copy[props.section].description)
const selectedRun=computed(()=>runs.value.find(item=>item.id===selectedRunID.value)||runs.value[0]||null)
const activeRunCount=computed(()=>runs.value.filter(item=>['running','awaiting_approval'].includes(item.status)).length)
const activeRows=computed<ResourceRecord[]>(()=>props.section==='applications'?applications.value:props.section==='repositories'?repositories.value:props.section==='build-plans'?buildPlans.value:props.section==='image-registries'?registries.value:[])
const activeColumns=computed(()=>props.section==='applications'?[{key:'name',label:'应用'},{key:'repository',label:'代码仓库'},{key:'workflow_template',label:'流水线方案'},{key:'sync_status',label:'代码状态'},{key:'last_checked_at',label:'最近检查'}]:props.section==='repositories'?[{key:'name',label:'名称'},{key:'provider',label:'平台'},{key:'clone_url',label:'Git 地址'},{key:'default_branch',label:'默认分支'},{key:'webhook_enabled',label:'Webhook'},{key:'is_active',label:'状态'}]:props.section==='build-plans'?[{key:'name',label:'名称'},{key:'is_active',label:'状态'},{key:'kind',label:'类型'},{key:'build_entry',label:'构建入口'},{key:'context_path',label:'执行目录'},{key:'artifact_path',label:'制品'},{key:'image_registry',label:'镜像输出'},{key:'timeout_seconds',label:'超时'},{key:'updated_at',label:'最近修改'}]:props.section==='image-registries'?[{key:'name',label:'名称'},{key:'provider',label:'类型'},{key:'endpoint',label:'地址'},{key:'namespace',label:'命名空间'},{key:'has_credential',label:'凭据'}]:[])
const artifactColumns=[{key:'name',label:'制品名称'},{key:'kind',label:'类型'},{key:'status',label:'状态'},{key:'size_bytes',label:'大小'},{key:'storage_kind',label:'存储位置'},{key:'digest',label:'摘要'},{key:'created_at',label:'创建时间'}]
const selectedArtifactApplication=computed(()=>applications.value.find(item=>item.id===artifactApplicationID.value))

const activeApplicationRunStatuses=new Set(['running','awaiting_approval','ready'])
const applicationCards=computed(()=>applications.value.map(application=>{
 const related=runs.value.filter(run=>run.application_id===application.id)
 const run=related.find(item=>activeApplicationRunStatuses.has(item.status))||related[0]
 const repository=application.repository?.id?application.repository:repositories.value.find(item=>item.id===application.repository_id)
 const workflowTemplate=application.workflow_template?.id?application.workflow_template:workflowTemplates.value.find(item=>item.id===application.workflow_template_id)
 return {application,run,repository,workflowTemplate,state:applicationRunStatus(run),sync:applicationSyncStatus(application.sync_status)}
}))
const releasePlanRunnableCounts=computed<Record<string,number>>(()=>Object.fromEntries(releasePlans.value.map(plan=>[plan.id,releasePlanManualApplicationCount(plan)])))

function applicationRunStatus(run?:Run):StatusMeta{
 if(!run)return {tone:'neutral',label:t('applicationCard.runStatus.none'),live:false}
 const states:Record<string,StatusMeta>={
  detected:{tone:'info',label:t('applicationCard.runStatus.detected'),live:false},ready:{tone:'warning',label:t('applicationCard.runStatus.ready'),live:true},
  running:{tone:'info',label:t('applicationCard.runStatus.running'),live:true},awaiting_approval:{tone:'warning',label:t('applicationCard.runStatus.awaitingApproval'),live:true},
  succeeded:{tone:'success',label:t('applicationCard.runStatus.succeeded'),live:false},failed:{tone:'danger',label:t('applicationCard.runStatus.failed'),live:false},
  blocked:{tone:'danger',label:t('applicationCard.runStatus.blocked'),live:false},canceled:{tone:'neutral',label:t('applicationCard.runStatus.canceled'),live:false},
 }
 return states[run.status]||{tone:'neutral',label:run.status,live:false}
}
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
 if(['failed','blocked','canceled'].includes(run.status))return t('applicationCard.stopped')
 return t('applicationCard.notStarted')
}
function workflowSupportsManualRelease(application?:Application){return Boolean(application?.workflow?.source?.type==='trigger'&&application.workflow.source.config?.events?.includes('manual'))}
function applicationCanManualRelease(application?:Application){return Boolean(application?.is_active&&application.workflow?.is_active&&workflowSupportsManualRelease(application))}
function releasePlanManualApplicationCount(plan:ReleasePlan){
 return (plan.groups||[]).flatMap(group=>group.applications||[]).filter(item=>applicationCanManualRelease(applications.value.find(application=>application.id===item.application_id))).length
}
function formatApplicationTime(value?:string){return value?new Date(value).toLocaleString(locale.value==='en-US'?'en-US':'zh-CN',{hour12:false}):t('applicationCard.noTime')}
function openApplicationLink(card:(typeof applicationCards.value)[number],kind:'repository'|'workflow'){
 if(kind==='workflow'){
  void router.push(card.workflowTemplate?`/pipeline-plans/editor?template=${card.workflowTemplate.id}`:`/pipeline-plans/editor?application=${card.application.id}`)
  return
 }
 const target=card.repository
 if(!target){edit(card.application);return}
 void router.push('/repositories')
}
function createWorkflowTemplate(){void router.push({path:'/pipeline-plans/editor',query:{create:'1'}})}
function createImageRegistry(){
 formOpen.value=false
 void router.push({path:'/image-registries',query:{create:'1',return_to:route.fullPath}})
}

async function refresh(){loading.value=true;try{const requests=await Promise.all([auth.canAny(['delivery.read'])?client.get<{applications:Application[]}>('/applications'):null,auth.canAny(['repository.read'])?client.get<{repositories:Repository[]}>('/repositories'):null,auth.canAny(['credential.read'])?client.get<{credentials:Credential[]}>('/git-credentials'):null,auth.canAny(['delivery.read'])?client.get<{build_plans:BuildPlan[]}>('/build-plans'):null,auth.canAny(['delivery.read'])?client.get<{image_registries:Registry[]}>('/image-registries'):null,auth.canAny(['delivery.read'])?client.get<{workflow_templates:WorkflowTemplate[]}>('/workflow-templates'):null,auth.canAny(['delivery.read'])?client.get<{pipeline_runs:Run[]}>('/pipeline-runs?limit=200'):null,auth.canAny(['delivery.read'])?client.get<{release_plans:ReleasePlan[]}>('/release-plans'):null,auth.canAny(['deployment.read'])?client.get<{deployments:ResourceRecord[]}>('/deployments'):null]);applications.value=requests[0]?.data.applications||[];repositories.value=requests[1]?.data.repositories||[];credentials.value=requests[2]?.data.credentials||[];buildPlans.value=requests[3]?.data.build_plans||[];registries.value=requests[4]?.data.image_registries||[];workflowTemplates.value=requests[5]?.data.workflow_templates||[];runs.value=requests[6]?.data.pipeline_runs||[];if(!selectedRunID.value||!runs.value.some(item=>item.id===selectedRunID.value))selectedRunID.value=runs.value[0]?.id||'';releasePlans.value=requests[7]?.data.release_plans||[];deployments.value=requests[8]?.data.deployments||[]}catch(error){message.error(apiErrorMessage(error))}finally{loading.value=false}}
let stateRefreshing=false
async function refreshApplicationState(){
 if(stateRefreshing||!auth.canAny(['delivery.read']))return
 stateRefreshing=true
 try{
 const [applicationResult,runResult]=await Promise.all([client.get<{applications:Application[]}>('/applications'),client.get<{pipeline_runs:Run[]}>('/pipeline-runs?limit=200')])
  applications.value=applicationResult.data.applications||[]
  runs.value=runResult.data.pipeline_runs||[]
 }catch{}finally{stateRefreshing=false}
}
async function refreshRunState(){
 if(stateRefreshing||!auth.canAny(['delivery.read']))return
 stateRefreshing=true
 try{
  runs.value=(await client.get<{pipeline_runs:Run[]}>('/pipeline-runs?limit=200')).data.pipeline_runs||[]
  if(!selectedRunID.value||!runs.value.some(item=>item.id===selectedRunID.value))selectedRunID.value=runs.value[0]?.id||''
 }catch{}finally{stateRefreshing=false}
}
function formatVariableText(values?:Record<string,string>){return Object.entries(values||{}).map(([name,value])=>`${name}=${value}`).join('\n')}
function parseVariableText(source:string,label:string){
 const values:Record<string,string>={}
 const reservedEnvironmentNames=new Set(['CI','HOME','TMPDIR','ZRT_PIPELINE_RUN_ID','ZRT_APPLICATION_ID','ZRT_GIT_REF','ZRT_COMMIT_SHA'])
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
 editingID.value='';registryTested.value=false;buildImageDestination.value='local';buildArgsText.value='';buildEnvironmentText.value=''
 Object.assign(appForm,{name:'',description:'',repository_id:'',poll_interval_seconds:5,workflow_template_id:''})
	 Object.assign(repoForm,{name:'',provider:'github',clone_url:'',default_branch:'main',auth_type:'none',username:'',credential_id:'',api_credential_id:'',webhook_enabled:true,allow_insecure_http:false})
 Object.assign(buildForm,{name:'',kind:'dockerfile',description:'',script:'',dockerfile_path:'Dockerfile',context_path:'.',working_directory:'.',artifact_path:'',runtime_image:DEFAULT_RUNTIME_IMAGE,image_registry_id:'',target_stage:'',platform:'',pull:true,cache_enabled:true,timeout_seconds:1800})
 Object.assign(registryForm,{name:'',provider:'harbor',endpoint:'https://',namespace:'',username:'',credential:'',allow_insecure_http:false})
 Object.assign(releaseForm,{description:'',application_ids:[]})
}
function create(){
 resetForms()
 if(props.section==='applications'&&!applications.value.length){
  const activeRepositories=repositories.value.filter(item=>item.is_active)
  const activeTemplates=workflowTemplates.value.filter(item=>item.is_active)
  if(activeRepositories.length===1){
   appForm.repository_id=activeRepositories[0].id
   appForm.name=activeRepositories[0].name
  }
  if(activeTemplates.length===1)appForm.workflow_template_id=activeTemplates[0].id
 }
 formOpen.value=true
}
function edit(row:ResourceRecord){
 editingID.value=String(row.id)
 if(props.section==='applications'){
  const item=row as Application
  Object.assign(appForm,{name:item.name,description:item.description||'',repository_id:item.repository_id,poll_interval_seconds:item.poll_interval_seconds||5,workflow_template_id:item.workflow_template_id||''})
 }
 if(props.section==='repositories'){
  const item=row as Repository
	  Object.assign(repoForm,{name:item.name,provider:item.provider,clone_url:item.clone_url,default_branch:item.default_branch,auth_type:item.auth_type,username:item.username||'',credential_id:item.credential_id||'',api_credential_id:item.api_credential_id||'',webhook_enabled:item.webhook_enabled,allow_insecure_http:item.allow_insecure_http})
 }
 if(props.section==='build-plans'){
  const item=row as BuildPlan
  Object.assign(buildForm,{name:item.name,kind:item.kind,description:item.description||'',script:item.script||'',dockerfile_path:item.dockerfile_path||'Dockerfile',context_path:item.context_path||'.',working_directory:item.working_directory||'.',artifact_path:item.artifact_path||'',runtime_image:item.runtime_image||DEFAULT_RUNTIME_IMAGE,image_registry_id:item.image_registry_id||'',target_stage:item.target_stage||'',platform:item.platform||'',pull:item.pull!==false,cache_enabled:item.cache_enabled!==false,timeout_seconds:item.timeout_seconds||1800})
  buildImageDestination.value=item.image_registry_id?'registry':'local'
  buildArgsText.value=formatVariableText(item.build_args)
  buildEnvironmentText.value=formatVariableText(item.environment_variables)
 }
 formOpen.value=true
}
async function save(){
 saving.value=true
 try{
  let endpoint='',payload:unknown={},method:'post'|'put'='post'
  if(props.section==='applications'){
   if(!appForm.repository_id){message.error('请选择代码仓库');return}
   if(!appForm.workflow_template_id){message.error('请选择流水线方案');return}
   endpoint=editingID.value?`/applications/${editingID.value}`:'/applications'
   payload={...appForm}
   method=editingID.value?'put':'post'
  }
  if(props.section==='repositories'){
   endpoint=editingID.value?`/repositories/${editingID.value}`:'/repositories'
	   payload={...repoForm,credential_id:repoForm.auth_type==='none'?null:repoForm.credential_id||null,api_credential_id:repoForm.provider==='generic'?'':repoForm.api_credential_id||'',regenerate_webhook:false}
   method=editingID.value?'put':'post'
  }
  if(props.section==='build-plans'){
   if(!buildForm.name.trim()){message.error('请输入构建方案名称');return}
   if(buildForm.kind==='dockerfile'&&(!buildForm.dockerfile_path.trim()||!buildForm.context_path.trim())){message.error('请填写 Dockerfile 路径和构建上下文');return}
   if(buildForm.kind==='dockerfile'&&buildImageDestination.value==='registry'&&!buildForm.image_registry_id){message.error('请选择镜像仓库');return}
   if(buildForm.kind==='script'&&(!buildForm.script.trim()||!buildForm.artifact_path.trim()||!buildForm.runtime_image.trim())){message.error('请填写构建脚本、运行镜像和产物路径');return}
   const buildArgs=parseVariableText(buildArgsText.value,'构建参数')
   const environmentVariables=parseVariableText(buildEnvironmentText.value,'环境变量')
   if(buildArgs.error||environmentVariables.error){message.error(buildArgs.error||environmentVariables.error);return}
   endpoint=editingID.value?`/build-plans/${editingID.value}`:'/build-plans'
   payload={
    ...buildForm,
    dockerfile_path:buildForm.kind==='dockerfile'?buildForm.dockerfile_path:'',
    working_directory:buildForm.kind==='script'?buildForm.working_directory:'.',
    artifact_path:buildForm.kind==='script'?buildForm.artifact_path:'',
    runtime_image:buildForm.kind==='script'?buildForm.runtime_image:'',
    image_registry_id:buildForm.kind==='dockerfile'&&buildImageDestination.value==='registry'?buildForm.image_registry_id:'',
    target_stage:buildForm.kind==='dockerfile'?buildForm.target_stage:'',
    platform:buildForm.kind==='dockerfile'?buildForm.platform:'',
    build_args:buildForm.kind==='dockerfile'?buildArgs.values:{},
    environment_variables:buildForm.kind==='script'?environmentVariables.values:{},
   }
   method=editingID.value?'put':'post'
  }
  if(props.section==='image-registries'){
   if(!registryTested.value){message.error('请先测试镜像仓库登录');return}
   endpoint='/image-registries';payload={...registryForm,credential:registryForm.credential||null}
  }
  if(props.section==='release-plans'){
   if(!releaseForm.application_ids.length){message.error('请至少选择一个应用');return}
   endpoint='/release-plans';payload={description:releaseForm.description,applications:releaseForm.application_ids.map(application_id=>({application_id,manual_deploy:false,source_type:'',source_value:''}))}
  }
  await client[method](endpoint,payload)
  message.success('配置已保存');formOpen.value=false;resetForms();await refresh()
 }catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}
}
function buildPlanKindLabel(kind:string){return kind==='dockerfile'?'Dockerfile':'构建脚本'}
function buildPlanEntry(plan:BuildPlan){
 if(plan.kind==='dockerfile')return plan.dockerfile_path||'Dockerfile'
 const lines=(plan.script||'').split(/\r?\n/).filter(line=>line.trim()).length
 return lines?`${lines} 行脚本`:'构建脚本'
}
function buildPlanDirectory(plan:BuildPlan){return plan.kind==='dockerfile'?plan.context_path||'.':plan.working_directory||'.'}
function buildPlanArtifact(plan:BuildPlan){return plan.kind==='dockerfile'?'OCI 镜像':`${plan.artifact_path||'—'} · 自动识别`}
function buildPlanRegistry(plan:BuildPlan){return plan.kind==='script'?'不适用':plan.image_registry?.name||registries.value.find(item=>item.id===plan.image_registry_id)?.name||'构建运行时本地镜像'}
function artifactKindLabel(kind:Artifact['kind']){return kind==='oci_image'?'OCI 镜像':'文件包'}
function artifactStatusLabel(status:Artifact['status']){return ({available:'可用',expired:'已过期',corrupt:'已损坏'} as const)[status]||status}
function artifactStorageLabel(kind:Artifact['storage_kind']){return ({local_file:'ZRT 制品存储',registry:'镜像仓库',docker_daemon:'构建运行时'} as const)[kind]||kind}
function formatFileSize(value:number){
 if(!Number.isFinite(value)||value<=0)return '0 B'
 const units=['B','KB','MB','GB','TB']
 const index=Math.min(Math.floor(Math.log(value)/Math.log(1024)),units.length-1)
 return `${(value/1024**index).toFixed(index===0?0:1)} ${units[index]}`
}
async function loadArtifacts(){
 const applicationID=artifactApplicationID.value
 if(props.section!=='build-plans'||!applicationID||!auth.canAny(['delivery.read'])){artifacts.value=[];return}
 artifactLoading.value=true
 try{
  const result=await client.get<{artifacts:Artifact[]}>(`/applications/${applicationID}/artifacts`)
  if(artifactApplicationID.value===applicationID)artifacts.value=result.data.artifacts||[]
 }catch(error){
  if(artifactApplicationID.value===applicationID){artifacts.value=[];message.error(apiErrorMessage(error))}
 }finally{if(artifactApplicationID.value===applicationID)artifactLoading.value=false}
}
async function uploadArtifact(file:File){
 if(!artifactApplicationID.value||artifactUploading.value)return
 artifactUploading.value=true
 try{
  const body=new FormData()
  body.append('file',file,file.name)
  await client.post(`/applications/${artifactApplicationID.value}/artifacts/upload`,body,{timeout:120_000})
  message.success('制品上传完成，已计算摘要并登记')
  await loadArtifacts()
 }catch(error){message.error(apiErrorMessage(error))}finally{artifactUploading.value=false}
}
const beforeArtifactUpload:UploadProps['beforeUpload']=(file)=>{void uploadArtifact(file);return false}
async function downloadArtifact(item:Artifact){
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
function confirmBuildPlanStatus(plan:BuildPlan){
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
function confirmDeleteBuildPlan(plan:BuildPlan){
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
async function testRepository(){testing.value=true;try{const payload={...repoForm,credential_id:repoForm.auth_type==='none'?null:repoForm.credential_id||null,api_credential_id:repoForm.provider==='generic'?'':repoForm.api_credential_id||'',credential:null,regenerate_webhook:false};const result=editingID.value?await client.post<RefResult>(`/repositories/${editingID.value}/test`,undefined,{timeout:35000}):await client.post<RefResult>('/repositories/test',payload,{timeout:35000});message.success(`连接成功：${result.data.branches?.length||0} 个分支，${result.data.tags?.length||0} 个标签`)}catch(error){message.error(apiErrorMessage(error))}finally{testing.value=false}}
async function testRegistry(){testing.value=true;registryTested.value=false;try{await client.post('/image-registries/test',{...registryForm,credential:registryForm.credential||null},{timeout:35000});registryTested.value=true;message.success('镜像仓库登录成功')}catch(error){message.error(apiErrorMessage(error))}finally{testing.value=false}}
async function action(path:string){try{await client.post(path,undefined,{timeout:35000});await refresh()}catch(error){message.error(apiErrorMessage(error))}}
function openLogs(run:Run){log.value={open:true,runID:run.id,title:`${applications.value.find(item=>item.id===run.application_id)?.name||'应用'} · 流水线日志`,status:run.status}}
function resetManualFlow(){
 manualOpen.value=false
 commitOpen.value=false
 manualApplicationID.value=''
 manualApplications.value=[]
 commitOptions.value=[]
 selectedRef.value=''
 selectedSource.value=''
 manualSources.value=[]
 currentRun.value=null
 currentRunSelectionKey.value=''
}
function openManual(){
 resetManualFlow()
 manualApplications.value=applications.value.filter(applicationCanManualRelease)
 if(!manualApplications.value.length){message.warning(t('manualRun.noApplication'));return}
 manualApplicationID.value=manualApplications.value[0].id
 manualOpen.value=true
}
async function nextManual(){
 if(!manualApplicationID.value)return
 saving.value=true
 try{
  const data=(await client.get<RefResult>(`/applications/${manualApplicationID.value}/repository-refs`,{timeout:35000})).data
  commitOptions.value=[...(data.branches||[]).map(item=>({ref:`refs/heads/${item.name}`,name:item.name,sha:item.sha,kind:'branch' as const})),...(data.tags||[]).map(item=>({ref:`refs/tags/${item.name}`,name:item.name,sha:item.sha,kind:'tag' as const}))]
  manualSources.value=(data.manual_sources||[]).map(item=>({id:item.id,name:item.name,environment:item.environment}))
  const application=applications.value.find(item=>item.id===manualApplicationID.value)
  selectedRef.value=commitOptions.value.find(item=>item.kind==='branch'&&item.name===application?.repository?.default_branch)?.ref||commitOptions.value.find(item=>item.kind==='branch')?.ref||commitOptions.value[0]?.ref||''
  selectedSource.value=manualSources.value[0]?.id||''
  if(!commitOptions.value.length||!manualSources.value.length){message.warning(t('manualRun.noVersionOrSource'));return}
  manualOpen.value=false
  commitOpen.value=true
 }catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}
}
async function executeCommit(){
 const selected=commitOptions.value.find(item=>item.ref===selectedRef.value)
 if(!selected||!selectedSource.value||!manualApplicationID.value)return
 saving.value=true
 try{
  const selectionKey=[manualApplicationID.value,selected.ref,selected.sha,selectedSource.value].join('\u0000')
  if(!currentRun.value||currentRunSelectionKey.value!==selectionKey){
   currentRun.value=(await client.post<{pipeline_run:Run}>(`/applications/${manualApplicationID.value}/pipeline-runs`)).data.pipeline_run
   currentRunSelectionKey.value=selectionKey
  }
  await client.post(`/pipeline-runs/${currentRun.value.id}/execute`,{ref:selected.ref,commit_sha:selected.sha,source_node_id:selectedSource.value})
  message.success(t('manualRun.started'))
  resetManualFlow()
  await refresh()
 }catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}
}
function releasePlanTitle(plan:ReleasePlan){
 const legacyName=plan.name?.trim()
 return plan.description?.trim()||(legacyName&&!/^发布计划-[0-9a-f]{8}$/i.test(legacyName)?legacyName:'')||t('releasePlan.unnamed')
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
 if(!releasePlans.value.some(item=>item.id===value.id))return
 saving.value=true
 releasePlanMutationID.value=value.id
 try{
  await client.put(`/release-plans/${value.id}/configuration`,{description:value.description,groups:value.groups})
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
  name:group.name,mode:group.mode,failure_policy:group.failure_policy,
  depends_on_group_ids:(group.dependencies||[]).map(item=>item.depends_on_group_id),
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
 planExecutionSubmitting.value=false
}
function planExecutionItems(){return planExecutionGroups.value.flatMap(group=>group.items)}
function planApplicationBlockReason(application?:Application){
 if(!application)return t('releasePlanExecution.reason.applicationMissing')
 if(!application.is_active)return t('releasePlanExecution.reason.applicationDisabled')
 if(!application.workflow?.is_active)return t('releasePlanExecution.reason.workflowDisabled')
 if(!workflowSupportsManualRelease(application))return t('releasePlanExecution.reason.manualSourceMissing')
 return ''
}
function buildPlanExecutionGroups(plan:ReleasePlan){
 const groupNames=new Map((plan.groups||[]).map(group=>[group.id,group.name]))
 const applicationCounts=new Map<string,number>()
 for(const membership of (plan.groups||[]).flatMap(group=>group.applications||[]))applicationCounts.set(membership.application_id,(applicationCounts.get(membership.application_id)||0)+1)
 return [...(plan.groups||[])].sort((left,right)=>(left.sort_order||0)-(right.sort_order||0)).map(group=>({
  id:group.id,
  name:group.name,
  mode:group.mode,
  failurePolicy:group.failure_policy,
  dependencies:(group.dependencies||[]).map(item=>groupNames.get(item.depends_on_group_id)||item.depends_on_group_id),
  items:[...(group.applications||[])].sort((left,right)=>(left.sort_order||0)-(right.sort_order||0)).map(membership=>{
   const application=applications.value.find(item=>item.id===membership.application_id)
   const relationMissing=!membership.id
   const duplicated=(applicationCounts.get(membership.application_id)||0)>1
   const reason=relationMissing?t('releasePlanExecution.reason.membershipMissing'):duplicated?t('releasePlanExecution.reason.duplicateApplication'):planApplicationBlockReason(application)
   return {
    membershipID:membership.id||`${group.id}:${membership.application_id}`,
    applicationID:membership.application_id,
    applicationName:application?.name||membership.application?.name||t('releasePlan.unknownApplication'),
    workflowRevision:application?.workflow?.revision||0,
    loadState:reason?'blocked' as const:'idle' as const,
    reason,
    staticBlocked:Boolean(reason),
    sources:[],
    refs:[],
    selectedSourceID:'',
    selectedRef:'',
   }
  }),
 }))
}
function updatePlanExecutionItems(applicationID:string, update:(item:PlanExecutionItem)=>void){
 for(const item of planExecutionItems())if(item.applicationID===applicationID&&!item.staticBlocked)update(item)
}
async function loadPlanExecutionApplication(applicationID:string,signal:AbortSignal){
 const application=applications.value.find(item=>item.id===applicationID)
 const blockedReason=planApplicationBlockReason(application)
 const candidates=planExecutionItems().filter(item=>item.applicationID===applicationID&&!item.staticBlocked)
 if(!candidates.length)return
 if(blockedReason){updatePlanExecutionItems(applicationID,item=>{item.loadState='blocked';item.reason=blockedReason});return}
 updatePlanExecutionItems(applicationID,item=>{item.loadState='loading';item.reason=''})
 try{
  const data=(await client.get<RefResult>(`/applications/${applicationID}/repository-refs`,{timeout:35000,signal})).data
  if(signal.aborted)return
  const sources=(data.manual_sources||[]).map(item=>({id:item.id,name:item.name,environment:item.environment}))
  const refs:PlanExecutionReference[]=[...(data.branches||[]).map(item=>({kind:'branch' as const,ref:`refs/heads/${item.name}`,name:item.name,sha:item.sha})),...(data.tags||[]).map(item=>({kind:'tag' as const,ref:`refs/tags/${item.name}`,name:item.name,sha:item.sha}))]
  if(!sources.length||!refs.length){
   const reason=!sources.length?t('releasePlanExecution.reason.manualSourceMissing'):t('releasePlanExecution.reason.referenceMissing')
   updatePlanExecutionItems(applicationID,item=>{item.loadState='blocked';item.reason=reason;item.sources=sources;item.refs=refs})
   return
  }
  const defaultRef=refs.find(item=>item.kind==='branch'&&item.name===application?.repository?.default_branch)?.ref||''
  updatePlanExecutionItems(applicationID,item=>{
   item.loadState='ready'
   item.reason=''
   item.sources=sources
   item.refs=refs
   item.selectedSourceID=sources.length===1?sources[0].id:''
   item.selectedRef=defaultRef
  })
 }catch(error){
  if(signal.aborted)return
  updatePlanExecutionItems(applicationID,item=>{item.loadState='error';item.reason=apiErrorMessage(error)})
 }
}
async function loadPlanExecutionApplications(applicationIDs:string[],signal:AbortSignal){
 const queue=[...new Set(applicationIDs)]
 const worker=async()=>{for(;;){const applicationID=queue.shift();if(!applicationID||signal.aborted)return;await loadPlanExecutionApplication(applicationID,signal)}}
 await Promise.all(Array.from({length:Math.min(4,queue.length)},()=>worker()))
}
function openReleasePlan(planID:string){
 const plan=releasePlans.value.find(item=>item.id===planID)
 if(!plan)return
 resetPlanExecution()
 planExecutionPlanID.value=plan.id
 planExecutionTitle.value=releasePlanTitle(plan)
 planExecutionExpectedUpdatedAt.value=plan.updated_at||plan.created_at
 planExecutionRequestID.value=crypto.randomUUID()
 planExecutionGroups.value=buildPlanExecutionGroups(plan)
 planExecutionOpen.value=true
 planExecutionController=new AbortController()
 const loadable=planExecutionItems().filter(item=>item.loadState==='idle').map(item=>item.applicationID)
 void loadPlanExecutionApplications(loadable,planExecutionController.signal)
}
function updatePlanExecutionSource(membershipID:string,value:string){const item=planExecutionItems().find(candidate=>candidate.membershipID===membershipID);if(item)item.selectedSourceID=value}
function updatePlanExecutionRef(membershipID:string,value:string){const item=planExecutionItems().find(candidate=>candidate.membershipID===membershipID);if(item)item.selectedRef=value}
function retryPlanExecutionApplication(applicationID:string){if(planExecutionController)void loadPlanExecutionApplication(applicationID,planExecutionController.signal)}
function applyPlanExecutionIssues(error:unknown){
 const issues=(error as {response?:{data?:{issues?:Array<{release_group_application_id?:string;message?:string}>}}}).response?.data?.issues
 if(!Array.isArray(issues))return
 for(const issue of issues){
  const item=planExecutionItems().find(candidate=>candidate.membershipID===issue.release_group_application_id)
  if(item){item.loadState='error';item.reason=issue.message||t('releasePlanExecution.reason.preflightFailed')}
 }
}
async function executeReleasePlan(){
 const selections=planExecutionItems().map(item=>{
  const reference=item.refs.find(candidate=>candidate.ref===item.selectedRef)
  return {release_group_application_id:item.membershipID,expected_workflow_revision:item.workflowRevision,source_node_id:item.selectedSourceID,ref:item.selectedRef,commit_sha:reference?.sha||''}
 })
 if(!planExecutionPlanID.value||selections.some(item=>!item.source_node_id||!item.ref||!item.commit_sha))return
 planExecutionSubmitting.value=true
 try{
  await client.post(`/release-plans/${planExecutionPlanID.value}/executions`,{request_id:planExecutionRequestID.value,expected_plan_updated_at:planExecutionExpectedUpdatedAt.value,selections},{timeout:60000})
  message.success(t('releasePlanExecution.started'))
  resetPlanExecution()
  await refresh()
  await router.push({path:'/release-plans',query:{view:'runs'}})
 }catch(error){applyPlanExecutionIssues(error);message.error(apiErrorMessage(error))}finally{planExecutionSubmitting.value=false}
}
function showWebhook(item:Repository){void client.get<{webhook_url:string;webhook_secret:string}>(`/repositories/${item.id}/webhook`).then(result=>Modal.info({title:`${item.name} Webhook`,width:650,content:()=>`${result.data.webhook_url}\n${result.data.webhook_secret}`})).catch(error=>message.error(apiErrorMessage(error)))}
function applicationName(run:Run){return applications.value.find(item=>item.id===run.application_id)?.name||run.application?.name||'未命名应用'}
function runReferenceLabel(run?:Run){return run?formatGitReference({ref:run.ref,sha:run.commit_sha,trigger:run.trigger}):'—'}
function selectableReferenceLabel(reference:{ref:string;name:string;sha:string;kind:'branch'|'tag'}){return formatGitReference(reference)}
function formatRunTime(value:string){return new Date(value).toLocaleString('zh-CN',{hour12:false})}
function runStatusLabel(status:string){return ({detected:'已发现',ready:'准备就绪',blocked:'已阻塞',awaiting_approval:'等待审核',running:'执行中',succeeded:'已成功',failed:'已失败',canceled:'已取消'} as Record<string,string>)[status]||status}
function runStatusColor(status:string){return status==='succeeded'?'success':status==='failed'||status==='blocked'?'error':status==='running'?'processing':status==='awaiting_approval'?'warning':'default'}
function refreshVisibleState(){
 if(document.hidden)return
 if(props.section==='applications')void refreshApplicationState()
 else if(props.section==='release-plans'&&releaseView.value==='runs')void refreshRunState()
 else if(props.section==='release-plans')void refresh()
}
function syncArtifactApplication(){
 if(props.section!=='build-plans'){artifacts.value=[];return}
 if(!applications.value.some(item=>item.id===artifactApplicationID.value))artifactApplicationID.value=applications.value[0]?.id||''
}
function consumeBuildCreateRequest(){
 if(!['build-plans','image-registries'].includes(props.section)||route.query.create!=='1'||!canManage.value)return
 create()
 const query={...route.query}
 delete query.create
 void router.replace({query})
}

watch(()=>props.section,()=>{formOpen.value=false;resetForms();resetManualFlow();resetPlanExecution();syncArtifactApplication();void refresh().then(()=>{syncArtifactApplication();consumeBuildCreateRequest()})})
watch(()=>registryForm,()=>registryTested.value=false,{deep:true})
watch(()=>buildForm.kind,(kind)=>{if(kind==='dockerfile'){buildForm.dockerfile_path||='Dockerfile';buildForm.context_path||='.'}else{buildForm.working_directory||='.';buildForm.runtime_image||=DEFAULT_RUNTIME_IMAGE}})
watch(()=>repoForm.provider,(provider)=>{
 if(repoForm.credential_id&&!credentials.value.some(item=>item.id===repoForm.credential_id&&item.auth_type===repoForm.auth_type&&(item.provider===provider||item.provider==='generic'||provider==='generic')))repoForm.credential_id=''
 if(repoForm.api_credential_id&&!credentials.value.some(item=>item.id===repoForm.api_credential_id&&item.auth_type==='token'&&item.provider===provider))repoForm.api_credential_id=''
})
watch(()=>repoForm.auth_type,(authType)=>{if(authType==='none'||!credentials.value.some(item=>item.id===repoForm.credential_id&&item.auth_type===authType))repoForm.credential_id=''})
watch(()=>applications.value.map(item=>item.id).join(','),syncArtifactApplication)
watch(artifactApplicationID,()=>{void loadArtifacts()})
watch(()=>route.query.create,consumeBuildCreateRequest)
onMounted(()=>{void refresh().then(()=>{syncArtifactApplication();consumeBuildCreateRequest()});releaseTimer=window.setInterval(refreshVisibleState,5000);document.addEventListener('visibilitychange',refreshVisibleState);window.addEventListener('focus',refreshVisibleState)})
onBeforeUnmount(()=>{resetPlanExecution();clearInterval(releaseTimer);document.removeEventListener('visibilitychange',refreshVisibleState);window.removeEventListener('focus',refreshVisibleState)})
</script>

<template><section><PageToolbar :description="currentDescription"><a-tag v-if="props.section==='applications'||props.section==='release-plans'" color="processing">{{ t('applicationCard.autoRefresh') }}</a-tag><a-button :loading="loading" @click="refresh"><RefreshCw :size="15"/>刷新</a-button><a-button v-if="props.section==='release-plans'&&releaseView==='runs'&&auth.canAny(['delivery.run'])" type="primary" @click="openManual()"><Play :size="15"/>手动执行</a-button><a-button v-else-if="canManage&&(props.section!=='release-plans'||releaseView==='plans')" type="primary" @click="create"><Plus :size="15"/>{{ props.section==='release-plans'?'创建发布计划':'新建' }}</a-button></PageToolbar>
<div v-if="props.section==='applications'" class="application-grid">
  <article v-for="card in applicationCards" :key="card.application.id" class="application-card vben-card" :class="[`state-${card.state.tone}`,{'is-live':card.state.live}]">
    <header class="application-head">
      <div class="application-identity">
        <span class="application-mark"><Boxes/></span>
        <div><div class="application-title"><h3>{{ card.application.name }}</h3><span class="application-enabled" :class="{inactive:!card.application.is_active}">{{ card.application.is_active?t('applicationCard.active'):t('applicationCard.inactive') }}</span></div><p :title="card.application.description">{{ card.application.description||t('applicationCard.noDescription') }}</p></div>
      </div>
      <div class="application-state"><i/><span><small>{{ t('applicationCard.currentPipeline') }}</small><strong>{{ card.state.label }}</strong></span></div>
    </header>
    <section class="application-run">
      <div class="application-commit"><small>{{ t('applicationCard.latestRun') }}</small><strong :title="card.run?.commit_message||t('applicationCard.noRun')">{{ card.run?.commit_message||t('applicationCard.noRun') }}</strong><span><GitBranch/>{{ runReferenceLabel(card.run) }}</span></div>
      <div class="application-node"><span><Workflow/></span><div><small>{{ t('applicationCard.currentNode') }}</small><strong :title="applicationCurrentNode(card.run)">{{ applicationCurrentNode(card.run) }}</strong><time><Clock3/>{{ formatApplicationTime(card.run?.updated_at||card.run?.created_at) }}</time></div></div>
    </section>
    <div class="application-links">
      <button type="button" @click="openApplicationLink(card,'repository')"><span><GitBranch/>{{ t('applicationCard.repository') }}</span><strong :title="card.repository?.name">{{ card.repository?.name||t('applicationCard.unbound') }}</strong><ChevronRight/></button>
      <button type="button" @click="openApplicationLink(card,'workflow')"><span><Workflow/>{{ t('applicationCard.workflow') }}</span><strong :title="card.workflowTemplate?.name">{{ card.workflowTemplate?.name||t('applicationCard.customWorkflow') }}</strong><ChevronRight/></button>
    </div>
    <footer class="application-footer">
      <div class="application-sync" :class="[`tone-${card.sync.tone}`,{'is-live':card.sync.live}]"><i/><span><small>{{ t('applicationCard.codeStatus') }}</small><strong>{{ card.sync.label }}</strong></span><time>{{ t('applicationCard.lastChecked') }} {{ formatApplicationTime(card.application.last_checked_at) }}</time></div>
      <div class="application-actions"><a-button v-if="canManage" @click="edit(card.application)"><Settings2/>{{ t('applicationCard.configure') }}</a-button><a-button @click="router.push(`/pipeline-plans/editor?application=${card.application.id}`)"><Workflow/>{{ t('applicationCard.pipeline') }}</a-button><a-button v-if="auth.canAny(['delivery.run'])" type="primary" @click="action(`/applications/${card.application.id}/sync`)"><RefreshCw/>{{ t('applicationCard.checkUpdates') }}</a-button></div>
    </footer>
  </article>
  <a-empty v-if="!applicationCards.length&&!loading" :description="t('applicationCard.empty')"/>
</div>
<div v-else-if="props.section!=='release-plans'" class="resource-section-stack">
 <div class="vben-card">
  <ResourceTable :rows="activeRows" :columns="activeColumns" :loading="loading">
   <template #cell-sync_status="{value}"><a-tag :color="value==='changed'?'warning':value==='synced'?'success':'default'">{{ value }}</a-tag></template>
   <template #cell-kind="{value}"><a-tag color="blue">{{ props.section==='build-plans'?buildPlanKindLabel(String(value)):value }}</a-tag></template>
   <template #cell-build_entry="{row}"><code>{{ buildPlanEntry(row as BuildPlan) }}</code></template>
   <template #cell-context_path="{row}"><code>{{ buildPlanDirectory(row as BuildPlan) }}</code></template>
   <template #cell-artifact_path="{row}"><code>{{ buildPlanArtifact(row as BuildPlan) }}</code></template>
   <template #cell-image_registry="{row}">{{ buildPlanRegistry(row as BuildPlan) }}</template>
   <template #cell-description="{value}"><span :title="String(value||'')">{{ value||'—' }}</span></template>
   <template #cell-timeout_seconds="{value}">{{ value }} 秒</template>
   <template #actions="{row}">
    <a-button v-if="canManage&&(props.section==='repositories'||props.section==='build-plans')" type="link" @click="edit(row)">编辑</a-button>
    <a-button v-if="props.section==='build-plans'&&canManage" type="link" :loading="resourceMutationID===String(row.id)" @click="confirmBuildPlanStatus(row as BuildPlan)">{{ row.is_active?'停用':'启用' }}</a-button>
    <a-button v-if="props.section==='build-plans'&&canManage" type="link" danger :disabled="resourceMutationID===String(row.id)" @click="confirmDeleteBuildPlan(row as BuildPlan)">删除</a-button>
    <a-button v-if="props.section==='repositories'" type="link" @click="action(`/repositories/${row.id}/test`)">测试</a-button>
    <a-button v-if="props.section==='repositories'&&auth.canAny(['repository.secret.read'])" type="link" @click="showWebhook(row as Repository)">Webhook</a-button>
   </template>
  </ResourceTable>
 </div>
 <section v-if="props.section==='build-plans'" class="artifact-panel vben-card">
  <header class="artifact-panel-head">
   <div><span><Package/></span><div><strong>应用制品</strong><small>构建结果与手动上传统一登记；同次流水线只部署其构建任务固定的制品。</small></div></div>
   <div class="artifact-panel-actions">
    <a-select v-model:value="artifactApplicationID" show-search option-filter-prop="label" placeholder="选择应用" :options="applications.map(item=>({value:item.id,label:item.name}))"/>
    <a-button :loading="artifactLoading" :disabled="!artifactApplicationID" @click="loadArtifacts"><RefreshCw :size="14"/>刷新</a-button>
    <a-upload v-if="canManage" :show-upload-list="false" :before-upload="beforeArtifactUpload" :disabled="!artifactApplicationID||artifactUploading">
     <a-button type="primary" :loading="artifactUploading" :disabled="!artifactApplicationID"><Upload :size="14"/>上传制品</a-button>
    </a-upload>
   </div>
  </header>
  <a-alert v-if="selectedArtifactApplication" class="artifact-note" type="info" show-icon :message="`当前查看：${selectedArtifactApplication.name}`" description="上传文件会流式保存、计算 SHA256 摘要并创建可审计记录，可用于归档和下载；不会替换运行中流水线已经固定的构建制品。"/>
  <ResourceTable :rows="artifacts" :columns="artifactColumns" :loading="artifactLoading" empty-text="该应用还没有制品">
   <template #cell-kind="{value}"><a-tag color="blue">{{ artifactKindLabel(value as Artifact['kind']) }}</a-tag></template>
   <template #cell-status="{value}"><a-tag :color="value==='available'?'success':value==='corrupt'?'error':'default'">{{ artifactStatusLabel(value as Artifact['status']) }}</a-tag></template>
   <template #cell-size_bytes="{value}">{{ formatFileSize(Number(value)) }}</template>
   <template #cell-storage_kind="{value}">{{ artifactStorageLabel(value as Artifact['storage_kind']) }}</template>
   <template #cell-digest="{value}"><code class="artifact-digest" :title="String(value)">{{ String(value).slice(0,19) }}…</code></template>
   <template #actions="{row}"><a-button v-if="(row as Artifact).storage_kind==='local_file'&&(row as Artifact).status==='available'" type="link" :loading="artifactDownloadingID===String(row.id)" @click="downloadArtifact(row as Artifact)"><Download :size="14"/>下载</a-button></template>
  </ResourceTable>
 </section>
</div>
<div v-else-if="releaseView==='runs'" class="run-workspace vben-card">
  <aside class="run-index">
    <header><div><strong>运行记录</strong><small>{{ runs.length }} 次执行</small></div><span :class="{active:activeRunCount}">{{ activeRunCount }} 个进行中</span></header>
    <div class="run-index-list">
      <button v-for="run in runs" :key="run.id" type="button" :class="{active:selectedRun?.id===run.id}" @click="selectedRunID=run.id">
        <span class="run-dot" :class="run.status"/>
        <span class="run-index-copy">
          <strong>{{ applicationName(run) }}</strong>
          <span>{{ run.commit_message||'未记录提交说明' }}</span>
          <small>{{ runReferenceLabel(run) }} · {{ run.current_node_name||run.current_node_id||runStatusLabel(run.status) }}</small>
        </span>
        <ChevronRight :size="16"/>
      </button>
      <a-empty v-if="!runs.length&&!loading" description="还没有流水线运行"/>
    </div>
  </aside>
  <main v-if="selectedRun" class="run-detail">
    <header class="run-detail-heading">
      <div><span class="run-status-orb" :class="selectedRun.status"><i/></span><div><small>流水线运行</small><h3>{{ applicationName(selectedRun) }}</h3></div></div>
      <a-tag :color="runStatusColor(selectedRun.status)">{{ runStatusLabel(selectedRun.status) }}</a-tag>
    </header>
    <section class="run-commit-panel">
      <GitCommit :size="18"/>
      <div><small>提交说明</small><strong>{{ selectedRun.commit_message||'未记录提交说明' }}</strong><span>{{ runReferenceLabel(selectedRun) }}</span></div>
      <time>{{ formatRunTime(selectedRun.created_at) }}</time>
    </section>
    <PipelineRunGraph :key="selectedRun.id" :graph="selectedRun.execution_graph" :current-node-id="selectedRun.current_node_id" :status="selectedRun.status" :stage="selectedRun.stage"/>
    <dl class="run-facts">
      <div><dt>当前任务</dt><dd>{{ selectedRun.current_node_name||selectedRun.current_node_id||'尚未开始' }}</dd></div>
      <div><dt>触发方式</dt><dd>{{ selectedRun.trigger||'—' }}</dd></div>
      <div><dt>状态说明</dt><dd>{{ selectedRun.message||runStatusLabel(selectedRun.status) }}</dd></div>
    </dl>
    <footer class="run-actions"><a-button @click="openLogs(selectedRun)">查看实时日志</a-button><a-button v-if="selectedRun.status==='failed'&&auth.canAny(['delivery.run'])" type="primary" @click="action(`/pipeline-runs/${selectedRun.id}/retry`)">重新执行</a-button><a-button v-if="selectedRun.status==='awaiting_approval'&&auth.canAny(['deployment.review'])" type="primary" @click="action(`/pipeline-runs/${selectedRun.id}/approve`)">通过审核</a-button><a-button v-if="selectedRun.stage==='manual'&&auth.canAny(['delivery.run'])" type="primary" @click="action(`/pipeline-runs/${selectedRun.id}/advance`)">放行并继续</a-button></footer>
  </main>
  <div v-else class="run-detail-empty"><a-empty description="选择一条流水线运行查看执行拓扑"/></div>
</div>
<div v-else-if="releaseView==='records'" class="vben-card"><ResourceTable :rows="deployments" :loading="loading" :columns="[{key:'target_name',label:'运行位置'},{key:'platform',label:'方式'},{key:'operation',label:'操作'},{key:'image',label:'镜像'},{key:'status',label:'状态'},{key:'requested_by',label:'申请人'},{key:'approved_by',label:'审核人'},{key:'error_message',label:'失败原因'},{key:'created_at',label:'时间'}]"/></div>
<ReleasePlanWorkspace
 v-else
 :plans="releasePlans"
 :applications="applications"
 :loading="loading"
 :can-manage="canManage"
 :can-run="auth.canAny(['delivery.run'])"
 :runnable-counts="releasePlanRunnableCounts"
 :mutatingPlanID="releasePlanMutationID"
 @create="create"
 @execute="openReleasePlan"
 @edit="openReleasePlanEditor"
 @add-application="openReleaseGroupApplicationPicker"
 @toggle="toggleReleasePlan"
 @remove="removeReleasePlan"
 @remove-application="removeReleaseGroupApplication"
/>

<a-drawer v-model:open="formOpen" :title="editingID?'编辑配置':props.section==='release-plans'?'创建发布计划':'新建配置'" width="700"><a-form layout="vertical">
<template v-if="props.section==='applications'">
 <section class="application-form-section">
  <header><strong>应用信息</strong><small>应用只负责绑定代码来源与流水线，不再重复绑定构建或部署资源。</small></header>
  <div class="form-grid">
   <a-form-item label="应用名称" required><a-input v-model:value="appForm.name"/></a-form-item>
   <a-form-item label="说明"><a-input v-model:value="appForm.description"/></a-form-item>
   <a-form-item class="span2" label="代码仓库" required><a-select v-model:value="appForm.repository_id" show-search option-filter-prop="label" :options="repositories.filter(item=>item.is_active).map(item=>({value:item.id,label:`${item.name} · ${item.default_branch}`}))"/></a-form-item>
   <a-form-item class="span2" label="流水线方案" required>
    <div class="resource-picker"><a-select v-model:value="appForm.workflow_template_id" show-search option-filter-prop="label" placeholder="选择已启用的流水线方案" :options="workflowTemplates.filter(item=>item.is_active).map(item=>({value:item.id,label:`${item.name} · 第 ${item.revision} 版`}))"/><a-button v-if="canManage" class="resource-create" aria-label="创建流水线方案" title="创建流水线方案" @click="createWorkflowTemplate"><Plus :size="16"/></a-button></div>
   </a-form-item>
   <a-alert v-if="!workflowTemplates.some(item=>item.is_active)" class="span2" type="warning" show-icon message="还没有已启用的流水线方案，请先创建并启用方案。"/>
  </div>
 </section>
 <section class="application-form-section">
  <header><strong>代码检查</strong><small>分支、PR/MR、Tag 和手动启动规则在流水线的代码源中配置；Webhook 只用于降低延迟。</small></header>
  <div class="form-grid">
   <a-form-item label="检查间隔"><a-select v-model:value="appForm.poll_interval_seconds" :options="[3,5,10,60].map(value=>({value,label:`${value} 秒`}))"/></a-form-item>
  </div>
 </section>
</template>
<template v-if="props.section==='repositories'">
 <div class="form-grid">
  <a-form-item label="名称" required><a-input v-model:value="repoForm.name"/></a-form-item>
  <a-form-item label="平台"><a-select v-model:value="repoForm.provider" :options="['github','gitlab','gitea','gitee','generic'].map(value=>({value,label:value}))"/></a-form-item>
  <a-form-item class="span2" label="Git 地址" required><a-input v-model:value="repoForm.clone_url"/></a-form-item>
  <a-form-item label="默认分支"><a-input v-model:value="repoForm.default_branch"/></a-form-item>
  <a-form-item label="Git 克隆认证"><a-select v-model:value="repoForm.auth_type" :options="[{value:'none',label:'无需认证'},{value:'token',label:'访问令牌'},{value:'ssh_key',label:'SSH 私钥'}]"/></a-form-item>
  <a-form-item v-if="repoForm.auth_type!=='none'" class="span2" label="Git 克隆凭据">
   <a-select v-model:value="repoForm.credential_id" show-search option-filter-prop="label" :options="credentials.filter(item=>item.auth_type===repoForm.auth_type&&(item.provider===repoForm.provider||item.provider==='generic'||repoForm.provider==='generic')).map(item=>({value:item.id,label:`${item.name} · ${item.secret_hint}`}))"/>
   <small>仅用于 Git clone/fetch，仓库只能引用当前操作者自己的凭据。</small>
  </a-form-item>
  <a-form-item v-if="repoForm.provider!=='generic'" class="span2" label="平台 API 令牌（可选）">
   <a-select v-model:value="repoForm.api_credential_id" allow-clear show-search option-filter-prop="label" placeholder="公开仓库可留空" :options="credentials.filter(item=>item.auth_type==='token'&&item.provider===repoForm.provider).map(item=>({value:item.id,label:`${item.name} · ${item.secret_hint}`}))"/>
   <small>用于主动读取私有 PR/MR。SSH 私钥绝不会发送到平台 API；使用 Token 克隆时留空可复用克隆 Token。</small>
  </a-form-item>
  <a-checkbox v-model:checked="repoForm.webhook_enabled">启用 Webhook</a-checkbox>
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
  <header><strong>{{ buildForm.kind==='dockerfile'?'镜像构建':'文件制品构建' }}</strong><small>{{ buildForm.kind==='dockerfile'?'默认读取仓库根目录 Dockerfile，并将镜像保留在当前构建运行时。':'脚本在已固定 Commit 的检出目录执行，目录产物自动打包，普通文件保持原格式。' }}</small></header>
  <div v-if="buildForm.kind==='dockerfile'" class="form-grid">
   <a-form-item label="Dockerfile 路径" required><a-input v-model:value="buildForm.dockerfile_path" placeholder="Dockerfile"/></a-form-item>
   <a-form-item label="构建上下文" required><a-input v-model:value="buildForm.context_path" placeholder="."/></a-form-item>
   <a-alert class="span2" type="success" show-icon message="默认输出本地 OCI 镜像" description="未配置镜像仓库时，镜像保留在 ZRT 当前构建运行时，可直接发布到同一 Docker 运行时。远程 Docker 和 Kubernetes 发布请在高级配置中选择镜像仓库。"/>
  </div>
  <div v-else class="form-grid">
   <a-form-item class="span2" label="构建脚本" required><a-textarea v-model:value="buildForm.script" :rows="8" placeholder="例如：npm ci&#10;npm run build"/></a-form-item>
   <a-form-item class="span2" label="运行镜像" required><a-auto-complete v-model:value="buildForm.runtime_image" :options="runtimeImageOptions" placeholder="alpine:3.22"/><small class="field-hint">默认 alpine:3.22，也可输入其他镜像。镜像必须提供 /bin/sh，并使用明确 tag 或 digest；不接受裸镜像名和 latest。</small></a-form-item>
   <a-form-item label="工作目录" required><a-input v-model:value="buildForm.working_directory" placeholder="."/></a-form-item>
   <a-form-item label="产物路径" required><a-input v-model:value="buildForm.artifact_path" placeholder="例如 dist 或 bin/server"/></a-form-item>
   <a-alert class="span2" type="info" show-icon message="产物上传由 ZRT 自动完成" description="脚本成功后，ZRT 会校验产物路径：目录确定性打包为 tar.gz，普通文件保持原格式，然后计算 SHA256 摘要并登记制品；脚本无需自行上传。"/>
  </div>
 </section>
 <a-collapse class="build-advanced" :bordered="false">
  <a-collapse-panel key="advanced">
   <template #header><span class="build-advanced-title"><strong>高级配置</strong><small>{{ buildForm.kind==='dockerfile'?'镜像仓库、平台、缓存、构建参数和超时':'环境变量和超时' }}</small></span></template>
   <div class="form-grid">
    <a-form-item v-if="buildForm.kind==='dockerfile'" class="span2" label="镜像输出">
     <a-radio-group v-model:value="buildImageDestination" button-style="solid"><a-radio-button value="local">构建运行时本地镜像</a-radio-button><a-radio-button value="registry">推送镜像仓库</a-radio-button></a-radio-group>
    </a-form-item>
    <a-form-item v-if="buildForm.kind==='dockerfile'&&buildImageDestination==='registry'" class="span2" label="镜像仓库" required><div class="resource-picker"><a-select v-model:value="buildForm.image_registry_id" show-search option-filter-prop="label" placeholder="选择已测试可用的镜像仓库" :options="registries.filter(item=>item.is_active).map(item=>({value:item.id,label:item.name}))"/><a-button v-if="canManage" class="resource-create" aria-label="创建镜像仓库" title="创建镜像仓库" @click="createImageRegistry"><Plus :size="16"/></a-button></div></a-form-item>
    <a-form-item v-if="buildForm.kind==='dockerfile'" label="Docker target"><a-input v-model:value="buildForm.target_stage" placeholder="可选，多阶段构建目标"/></a-form-item>
    <a-form-item v-if="buildForm.kind==='dockerfile'" label="目标平台"><a-input v-model:value="buildForm.platform" placeholder="例如 linux/amd64"/></a-form-item>
    <a-form-item v-if="buildForm.kind==='dockerfile'" label="拉取基础镜像"><a-switch v-model:checked="buildForm.pull"/><small class="field-hint">默认开启，确保基础镜像及时更新。</small></a-form-item>
    <a-form-item v-if="buildForm.kind==='dockerfile'" label="启用构建缓存"><a-switch v-model:checked="buildForm.cache_enabled"/><small class="field-hint">默认开启，缓存不可用不会导致正式构建失败。</small></a-form-item>
    <a-form-item v-if="buildForm.kind==='dockerfile'" class="span2" label="构建参数（每行 KEY=value）"><a-textarea v-model:value="buildArgsText" :rows="5" placeholder="VERSION=1.0.0&#10;ENABLE_FEATURE=true"/></a-form-item>
    <a-form-item v-if="buildForm.kind==='script'" class="span2" label="环境变量（每行 KEY=value）"><a-textarea v-model:value="buildEnvironmentText" :rows="5" placeholder="NODE_ENV=production&#10;CGO_ENABLED=0"/></a-form-item>
    <a-form-item label="超时（秒）" required><a-input-number v-model:value="buildForm.timeout_seconds" :min="30" :max="7200"/></a-form-item>
    <a-form-item class="span2" label="说明"><a-input v-model:value="buildForm.description" placeholder="可选，说明适用应用或制品用途"/></a-form-item>
   </div>
  </a-collapse-panel>
 </a-collapse>
</template>
<template v-if="props.section==='image-registries'"><div class="form-grid"><a-form-item label="名称" required><a-input v-model:value="registryForm.name"/></a-form-item><a-form-item label="类型"><a-select v-model:value="registryForm.provider" :options="[{value:'harbor',label:'Harbor'},{value:'docker_hub',label:'Docker Hub'},{value:'generic',label:'通用 Registry'}]"/></a-form-item><a-form-item class="span2" label="地址"><a-input v-model:value="registryForm.endpoint"/></a-form-item><a-form-item label="命名空间"><a-input v-model:value="registryForm.namespace"/></a-form-item><a-form-item label="用户名"><a-input v-model:value="registryForm.username"/></a-form-item><a-form-item class="span2" label="密码或 Token"><a-input-password v-model:value="registryForm.credential"/></a-form-item><a-checkbox v-model:checked="registryForm.allow_insecure_http">允许 HTTP（仅可信内网）</a-checkbox></div><a-alert v-if="registryTested" type="success" show-icon message="镜像仓库登录测试成功"/></template>
<template v-if="props.section==='release-plans'"><a-form-item label="说明" required><a-textarea v-model:value="releaseForm.description" :rows="3"/></a-form-item><a-form-item label="应用" required><a-select v-model:value="releaseForm.application_ids" mode="multiple" :options="applications.filter(item=>item.is_active).map(item=>({value:item.id,label:item.name}))"/><small>创建计划时至少选择一个应用。执行时选择代码分支，环境和部署配置以应用当前启用的流水线为准。</small></a-form-item></template>
</a-form><template #footer><div class="drawer-actions"><a-button @click="formOpen=false">取消</a-button><a-button v-if="props.section==='repositories'" :loading="testing" @click="testRepository">测试连接</a-button><a-button v-if="props.section==='image-registries'" :loading="testing" @click="testRegistry">测试登录</a-button><a-button type="primary" :loading="saving" :disabled="props.section==='image-registries'&&!registryTested" @click="save">保存</a-button></div></template></a-drawer>
<a-modal v-model:open="manualOpen" :title="t('manualRun.title')" :confirm-loading="saving" :ok-text="t('manualRun.chooseVersion')" @ok="nextManual" @cancel="resetManualFlow"><a-form layout="vertical"><a-form-item :label="t('manualRun.application')"><a-select v-model:value="manualApplicationID" :options="manualApplications.map(item=>({value:item.id,label:item.name}))"/></a-form-item></a-form></a-modal>
<a-modal v-model:open="commitOpen" :title="t('manualRun.versionTitle')" :confirm-loading="saving" :ok-text="t('manualRun.confirm')" @ok="executeCommit" @cancel="resetManualFlow"><a-form layout="vertical"><a-alert class="manual-release-note" type="info" show-icon :message="t('manualRun.executionHint')"/><a-form-item v-if="manualSources.length" :label="t('manualRun.source')"><a-select v-model:value="selectedSource" :options="manualSources.map(item=>({value:item.id,label:item.environment?`${item.name} · ${item.environment}`:item.name}))"/></a-form-item><a-form-item :label="t('manualRun.ref')"><a-select v-model:value="selectedRef"><a-select-opt-group :label="t('manualRun.branch')"><a-select-option v-for="item in commitOptions.filter(item=>item.kind==='branch')" :key="item.ref" :value="item.ref">{{ selectableReferenceLabel(item) }}</a-select-option></a-select-opt-group><a-select-opt-group :label="t('manualRun.tag')"><a-select-option v-for="item in commitOptions.filter(item=>item.kind==='tag')" :key="item.ref" :value="item.ref">{{ selectableReferenceLabel(item) }}</a-select-option></a-select-opt-group></a-select></a-form-item></a-form></a-modal>
<a-modal
 :open="releasePlanAddApplicationOpen"
 :title="t('releasePlan.addApplicationDialog.title', { name: releasePlanAddApplicationTarget?.group.name || '' })"
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
 :submitting="planExecutionSubmitting"
 @cancel="resetPlanExecution"
 @submit="executeReleasePlan"
 @retry="retryPlanExecutionApplication"
 @update-source="updatePlanExecutionSource"
 @update-ref="updatePlanExecutionRef"
/>
<ReleasePlanEditorDrawer
 v-model:open="releasePlanEditorOpen"
 :plan="releasePlanEditorPlan"
 :applications="applications"
 :saving="saving"
 @save="saveReleasePlanEditor"
/>
<PipelineLogDrawer v-model:open="log.open" :runID="log.runID" :title="log.title" :initial-status="log.status"/>
</section></template>

<style scoped>
.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.span2{grid-column:1/-1}.drawer-actions{display:flex;justify-content:flex-end;gap:8px}.release-plan-add-hint{display:block;margin-top:6px;color:var(--zrt-muted);font-size:11px}
.resource-section-stack{display:grid;gap:14px}.artifact-panel{overflow:hidden}.artifact-panel-head{display:flex;align-items:center;justify-content:space-between;gap:20px;padding:17px 18px;border-bottom:1px solid var(--zrt-border)}.artifact-panel-head>div:first-child{display:flex;min-width:0;align-items:center;gap:11px}.artifact-panel-head>div:first-child>span{display:grid;width:38px;height:38px;flex:0 0 38px;place-items:center;border-radius:11px;color:var(--zrt-primary);background:var(--zrt-primary-soft)}.artifact-panel-head svg{width:18px}.artifact-panel-head strong,.artifact-panel-head small{display:block}.artifact-panel-head small{margin-top:2px;color:var(--zrt-muted);font-size:11px}.artifact-panel-actions{display:flex;flex:0 0 auto;align-items:center;gap:8px}.artifact-panel-actions :deep(.ant-select){width:220px}.artifact-panel-actions :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}.artifact-note{margin:14px 16px}.artifact-digest{white-space:nowrap}
.build-form-section,.application-form-section{margin-bottom:16px;padding:15px 15px 0;border:1px solid var(--zrt-border);border-radius:12px;background:var(--zrt-surface-soft)}.build-form-section>header,.application-form-section>header{margin-bottom:13px}.build-form-section>header strong,.build-form-section>header small,.application-form-section>header strong,.application-form-section>header small{display:block}.build-form-section>header strong,.application-form-section>header strong{font-size:14px}.build-form-section>header small,.application-form-section>header small{margin-top:3px;color:var(--zrt-muted);font-size:11px}.resource-picker{display:flex;align-items:stretch;gap:8px}.resource-picker :deep(.ant-select){min-width:0;flex:1}.resource-create{width:34px;flex:0 0 34px;padding:0}.build-advanced{margin-bottom:12px;border:1px solid var(--zrt-border);border-radius:12px;background:var(--zrt-surface-soft)}.build-advanced :deep(.ant-collapse-header){align-items:center!important}.build-advanced :deep(.ant-collapse-content-box){padding-top:4px!important}.build-advanced-title{display:flex;align-items:baseline;gap:9px}.build-advanced-title small,.field-hint{color:var(--zrt-muted);font-size:11px}.field-hint{display:block;margin-top:5px}.build-form-section :deep(.ant-alert),.application-form-section :deep(.ant-alert){margin-bottom:16px}
.application-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(620px,100%),880px));justify-content:start;gap:14px}.application-card{--application-state:#9ba1ad;position:relative;min-width:0;overflow:hidden;padding:18px;transition:transform 180ms cubic-bezier(.2,0,0,1),border-color 180ms ease,box-shadow 180ms ease}.application-card::before{position:absolute;inset:0 auto 0 0;width:3px;background:var(--application-state);content:"";opacity:.8}.application-card:hover{transform:translateY(-1px);border-color:color-mix(in srgb,var(--application-state) 36%,var(--zrt-border));box-shadow:0 10px 28px rgb(35 45 70 / 8%)}.application-card.state-info{--application-state:#4f7df3}.application-card.state-success{--application-state:#2ab573}.application-card.state-warning{--application-state:#dfa126}.application-card.state-danger{--application-state:#ed5965}
.application-head{display:flex;align-items:flex-start;justify-content:space-between;gap:20px}.application-identity{display:flex;min-width:0;align-items:center;gap:12px}.application-mark{display:grid;width:42px;height:42px;flex:0 0 42px;place-items:center;border-radius:12px;color:var(--zrt-primary);background:var(--zrt-primary-soft)}.application-mark svg{width:21px}.application-identity>div{min-width:0}.application-title{display:flex;align-items:center;gap:9px}.application-title h3{overflow:hidden;margin:0;font-size:17px;text-overflow:ellipsis;white-space:nowrap}.application-enabled{padding:2px 7px;border-radius:999px;color:#168b57;background:color-mix(in srgb,#2ab573 12%,var(--zrt-surface));font-size:11px;white-space:nowrap}.application-enabled.inactive{color:var(--zrt-muted);background:var(--zrt-surface-soft)}.application-identity p{overflow:hidden;margin:3px 0 0;color:var(--zrt-muted);text-overflow:ellipsis;white-space:nowrap}
.application-state{display:flex;flex:0 0 auto;align-items:center;gap:9px;padding:7px 11px;border-radius:10px;color:var(--application-state);background:color-mix(in srgb,var(--application-state) 9%,var(--zrt-surface))}.application-state>i,.application-sync>i{width:8px;height:8px;flex:0 0 8px;border-radius:50%;background:currentColor;box-shadow:0 0 0 4px color-mix(in srgb,currentColor 10%,transparent)}.application-card.is-live .application-state>i,.application-sync.is-live>i{animation:application-pulse 2s ease-out infinite}.application-state small,.application-state strong,.application-sync small,.application-sync strong{display:block}.application-state small,.application-sync small{color:var(--zrt-muted);font-size:11px;font-weight:400}.application-state strong{font-size:13px}
.application-run{display:grid;grid-template-columns:minmax(0,1.35fr) minmax(210px,.65fr);gap:12px;margin-top:17px;padding:14px;border:1px solid color-mix(in srgb,var(--application-state) 15%,var(--zrt-border));border-radius:12px;background:linear-gradient(135deg,color-mix(in srgb,var(--application-state) 5%,var(--zrt-surface-soft)),var(--zrt-surface-soft))}.application-commit{min-width:0}.application-commit small,.application-commit strong,.application-commit span{display:block}.application-commit small,.application-node small{color:var(--zrt-muted);font-size:11px}.application-commit strong{overflow:hidden;margin:4px 0 7px;font-size:14px;text-overflow:ellipsis;white-space:nowrap}.application-commit span{display:flex;align-items:center;gap:6px;color:var(--zrt-muted);font-size:12px}.application-commit span svg{width:14px}.application-commit code{padding:1px 5px;border-radius:5px;background:var(--zrt-surface);color:var(--zrt-text)}.application-node{display:flex;min-width:0;align-items:center;gap:10px;padding-left:14px;border-left:1px solid var(--zrt-border)}.application-node>span{display:grid;width:34px;height:34px;flex:0 0 34px;place-items:center;border-radius:10px;color:var(--application-state);background:var(--zrt-surface)}.application-node svg{width:17px}.application-node>div{min-width:0}.application-node strong{display:block;overflow:hidden;margin-top:2px;text-overflow:ellipsis;white-space:nowrap}.application-node time{display:flex;align-items:center;gap:4px;margin-top:4px;color:var(--zrt-muted);font-size:11px}.application-node time svg{width:12px}
.application-links{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px;margin:12px 0 0}.application-links>button{display:grid;grid-template-columns:minmax(0,1fr) 14px;min-width:0;padding:10px 10px 10px 11px;border:1px solid transparent;border-radius:9px;color:var(--zrt-text);background:var(--zrt-surface-soft);cursor:pointer;text-align:left;transition:transform 160ms ease,border-color 160ms ease,background 160ms ease}.application-links>button:hover{transform:translateY(-1px);border-color:color-mix(in srgb,var(--zrt-primary) 28%,var(--zrt-border));background:color-mix(in srgb,var(--zrt-primary) 5%,var(--zrt-surface))}.application-links>button:focus-visible{outline:2px solid color-mix(in srgb,var(--zrt-primary) 48%,transparent);outline-offset:2px}.application-links button>span{display:flex;min-width:0;align-items:center;gap:5px;color:var(--zrt-muted);font-size:11px}.application-links button>span svg{width:13px}.application-links button>strong{overflow:hidden;margin-top:4px;font-size:12px;text-overflow:ellipsis;white-space:nowrap}.application-links button>svg{grid-area:1/2/3/3;width:14px;align-self:center;color:var(--zrt-muted);transition:transform 160ms ease}.application-links>button:hover>svg{transform:translateX(2px);color:var(--zrt-primary)}.application-footer{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-top:14px;padding-top:14px;border-top:1px solid var(--zrt-border)}.application-sync{display:flex;min-width:0;align-items:center;gap:8px;color:#9ba1ad}.application-sync.tone-info{color:#4f7df3}.application-sync.tone-success{color:#2ab573}.application-sync.tone-warning{color:#dfa126}.application-sync.tone-danger{color:#ed5965}.application-sync time{margin-left:4px;color:var(--zrt-muted);font-size:11px;white-space:nowrap}.application-actions{display:flex;flex:0 0 auto;gap:7px}.application-actions :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}.application-actions svg{width:14px}
.run-workspace{display:grid;min-height:620px;grid-template-columns:310px minmax(0,1fr);gap:8px;overflow:hidden;padding:8px;background:var(--zrt-surface-soft)}
.run-index,.run-detail,.run-detail-empty{min-width:0;border-radius:11px;background:var(--zrt-surface)}.run-index{overflow:hidden}.run-index>header{display:flex;min-height:62px;align-items:center;justify-content:space-between;padding:10px 14px}.run-index>header strong,.run-index>header small{display:block}.run-index>header small{margin-top:1px;color:var(--zrt-muted);font-size:11px}.run-index>header>span{padding:4px 8px;border-radius:999px;color:var(--zrt-muted);background:var(--zrt-surface-soft);font-size:11px}.run-index>header>span.active{color:var(--zrt-primary);background:var(--zrt-primary-soft)}
.run-index-list{min-height:540px;max-height:calc(100vh - 242px);overflow-y:auto;padding:0 7px 9px;scrollbar-width:thin}.run-index-list>button{display:grid;width:100%;min-height:86px;align-items:center;grid-template-columns:10px minmax(0,1fr) 17px;gap:10px;margin:3px 0;padding:10px 9px;border:0;border-radius:11px;outline:0;color:var(--zrt-text);background:transparent;cursor:pointer;text-align:left;transition:background-color 160ms ease,box-shadow 160ms ease}.run-index-list>button:hover{background:var(--zrt-surface-soft)}.run-index-list>button:focus-visible{box-shadow:inset 0 0 0 2px color-mix(in srgb,var(--zrt-primary) 45%,transparent)}.run-index-list>button.active{background:var(--zrt-primary-soft);box-shadow:inset 3px 0 var(--zrt-primary)}.run-index-list>button>svg{color:var(--zrt-muted)}
.run-dot{width:8px;height:8px;border-radius:50%;background:#a5aab3}.run-dot.running{background:var(--zrt-primary);box-shadow:0 0 0 5px color-mix(in srgb,var(--zrt-primary) 11%,transparent)}.run-dot.awaiting_approval{background:#dfa03c}.run-dot.succeeded{background:#2ab573}.run-dot.failed,.run-dot.blocked{background:#ed5965}.run-dot.canceled{background:#9299a6}.run-index-copy{min-width:0}.run-index-copy strong,.run-index-copy>span,.run-index-copy small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.run-index-copy strong{font-size:13px}.run-index-copy>span{margin-top:3px;color:var(--zrt-text);font-size:12px}.run-index-copy small{margin-top:3px;color:var(--zrt-muted);font-size:11px}
.run-detail{padding:20px}.run-detail-heading{display:flex;align-items:center;justify-content:space-between;gap:14px}.run-detail-heading>div{display:flex;min-width:0;align-items:center;gap:11px}.run-detail-heading small{color:var(--zrt-muted);font-size:11px}.run-detail-heading h3{overflow:hidden;margin:1px 0 0;text-overflow:ellipsis;white-space:nowrap;font-size:19px}.run-status-orb{position:relative;display:grid;width:40px;height:40px;flex:0 0 40px;place-items:center;border-radius:12px;background:var(--zrt-surface-soft)}.run-status-orb i{width:10px;height:10px;border-radius:50%;background:#9aa1ad}.run-status-orb.running{background:var(--zrt-primary-soft)}.run-status-orb.running i{background:var(--zrt-primary)}.run-status-orb.running::after{position:absolute;inset:7px;border:1px solid color-mix(in srgb,var(--zrt-primary) 42%,transparent);border-radius:8px;content:"";animation:run-breathe 1.8s ease-in-out infinite}.run-status-orb.awaiting_approval i{background:#dfa03c}.run-status-orb.succeeded i{background:#2ab573}.run-status-orb.failed i,.run-status-orb.blocked i{background:#ed5965}
.run-commit-panel{display:grid;align-items:center;grid-template-columns:22px minmax(0,1fr) auto;gap:11px;margin:18px 0 14px;padding:13px 14px;border:1px solid var(--zrt-border);border-radius:12px;background:var(--zrt-surface-soft)}.run-commit-panel>svg{color:var(--zrt-primary)}.run-commit-panel>div{min-width:0}.run-commit-panel small,.run-commit-panel strong,.run-commit-panel span{display:block}.run-commit-panel small{color:var(--zrt-muted);font-size:10px}.run-commit-panel strong{overflow:hidden;margin:2px 0;text-overflow:ellipsis;white-space:nowrap}.run-commit-panel span,.run-commit-panel time{color:var(--zrt-muted);font-size:11px}.run-commit-panel code{color:var(--zrt-text)}
.run-facts{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:8px;margin:14px 0 0}.run-facts>div{min-width:0;padding:11px 12px;border:1px solid var(--zrt-border);border-radius:10px;background:var(--zrt-surface-soft)}.run-facts dt{color:var(--zrt-muted);font-size:10px}.run-facts dd{overflow:hidden;margin:4px 0 0;text-overflow:ellipsis;white-space:nowrap;font-size:12px}.run-actions{display:flex;flex-wrap:wrap;gap:7px;margin-top:15px}.run-detail-empty{display:grid;place-items:center}
.manual-release-note{margin-bottom:16px}
@keyframes application-pulse{0%{box-shadow:0 0 0 0 color-mix(in srgb,currentColor 32%,transparent)}70%{box-shadow:0 0 0 7px transparent}100%{box-shadow:0 0 0 0 transparent}}@keyframes run-breathe{0%,100%{opacity:.45;transform:scale(.86)}50%{opacity:1;transform:scale(1.08)}}
@media(max-width:1100px){.application-links{grid-template-columns:repeat(2,minmax(0,1fr))}.application-footer{align-items:flex-start;flex-direction:column}.application-actions{width:100%;justify-content:flex-end}.run-workspace{grid-template-columns:270px minmax(0,1fr)}.run-facts{grid-template-columns:repeat(2,minmax(0,1fr))}}
@media(max-width:820px){.artifact-panel-head{align-items:flex-start;flex-direction:column}.artifact-panel-actions{width:100%;flex-wrap:wrap}.artifact-panel-actions :deep(.ant-select){min-width:200px;flex:1}.run-workspace{grid-template-columns:1fr}.run-index-list{display:flex;min-height:0;max-height:none;overflow-x:auto;overflow-y:hidden;padding:0 7px 8px}.run-index-list>button{width:270px;flex:0 0 270px}.run-detail{padding:16px}}
@media(max-width:640px){.form-grid{grid-template-columns:1fr}.span2{grid-column:auto}.build-advanced-title{align-items:flex-start;flex-direction:column;gap:2px}.application-head,.application-footer{align-items:flex-start;flex-direction:column}.application-state{align-self:stretch}.application-run{grid-template-columns:1fr}.application-node{padding-top:12px;padding-left:0;border-top:1px solid var(--zrt-border);border-left:0}.application-actions{justify-content:flex-start;flex-wrap:wrap}.application-sync{flex-wrap:wrap}.run-commit-panel{align-items:start;grid-template-columns:22px minmax(0,1fr)}.run-commit-panel time{grid-column:2}.run-facts{grid-template-columns:1fr}.run-detail-heading{align-items:flex-start}.run-detail-heading h3{font-size:17px}}
@media(max-width:480px){.application-links{grid-template-columns:1fr}.application-actions :deep(.ant-btn){flex:1}.application-sync time{width:100%;margin-left:16px}}
@media(prefers-reduced-motion:reduce){.application-card.is-live .application-state>i,.application-sync.is-live>i,.run-status-orb.running::after{animation:none}}
</style>
