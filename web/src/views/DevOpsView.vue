<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Modal, message } from 'ant-design-vue'
import { Boxes, ChevronRight, Clock3, GitBranch, GitCommit, Package, Play, Plus, RefreshCw, Rocket, Settings2, Workflow } from 'lucide-vue-next'

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

type Section='applications'|'repositories'|'build-plans'|'image-registries'|'deployment-plans'|'release-plans'
const props=defineProps<{section:Section}>(),route=useRoute(),router=useRouter(),auth=useAuthStore()
const {t,locale}=useI18n()
interface Repository extends ResourceRecord{id:string;name:string;provider:string;clone_url:string;default_branch:string;webhook_enabled:boolean;webhook_url?:string;is_active:boolean;auth_type:string;username?:string;allow_insecure_http:boolean;has_credential?:boolean;credential_id?:string}
interface Credential{id:string;name:string;provider:string;auth_type:string;username?:string;secret_hint:string}
interface BuildPlan extends ResourceRecord{id:string;name:string;kind:string;description:string;dockerfile_path?:string;context_path:string;timeout_seconds:number;is_active:boolean}
interface Registry extends ResourceRecord{id:string;name:string;provider:string;endpoint:string;namespace:string;has_credential:boolean;is_active:boolean}
interface DeploymentPlan extends ResourceRecord{id:string;name:string;kind:string;description:string;helm_chart?:string;helm_values?:string;compose_file?:string;service_name?:string;script?:string;timeout_seconds:number;deployment_target_id?:string;deployment_target?:{id:string;platform:string;workload_name?:string;namespace?:string};is_active:boolean}
interface WorkflowTemplate{id:string;name:string;revision:number;is_active:boolean}
 interface Application extends ResourceRecord{id:string;name:string;description:string;repository_id:string;branch:string;poll_interval_seconds:number;build_plan_id?:string;image_registry_id?:string;deployment_plan_id?:string;deployment_target_id?:string;workflow_template_id?:string;last_observed_commit?:string;sync_status:string;sync_message?:string;last_checked_at?:string;is_active:boolean;repository?:Repository;build_plan?:BuildPlan;image_registry?:Registry;deployment_plan?:DeploymentPlan;workflow_template?:WorkflowTemplate;workflow?:{is_active:boolean;revision:number;nodes?:Array<{id:string;type:'trigger'|'manual_release'|'manual'|'approval'|'deploy';name:string;config?:{environment?:string;events?:string[]}}>} }
interface ExecutionGraph{nodes:Array<{id:string;type:'trigger'|'manual_release'|'manual'|'approval'|'deploy';name:string;position:{x:number;y:number};environment?:string}>;edges:Array<{id:string;source:string;target:string;label?:string}>}
interface Run extends ResourceRecord{id:string;application_id:string;trigger:string;ref:string;commit_sha:string;commit_message?:string;status:string;stage:string;message?:string;created_at:string;updated_at?:string;application?:Application;environment?:string;current_node_id?:string;current_node_name?:string;created_by?:string;image?:string;execution_graph?:ExecutionGraph}
type StatusTone='neutral'|'info'|'success'|'warning'|'danger'
interface StatusMeta{tone:StatusTone;label:string;live:boolean}
 interface ReleasePlan extends ResourceRecord{id:string;name:string;version:string;description:string;status:string;is_active?:boolean;created_at:string;updated_at?:string;latest_execution?:{id:string;status:string;created_at:string;finished_at?:string};groups?:Array<{id:string;name:string;mode:string;failure_policy:string;sort_order?:number;dependencies?:Array<{depends_on_group_id:string}>;applications:Array<{id:string;application_id:string;application?:Application;manual_deploy?:boolean;source_type?:string;source_value?:string;sort_order?:number}>}>}
interface GitRef{name:string;sha:string} interface RefResult{branches:GitRef[];tags:GitRef[];manual_sources?:Array<{id:string;name:string;environment?:string}>}
type PlanExecutionLoadState='idle'|'loading'|'ready'|'blocked'|'error'
interface PlanExecutionReference{kind:'branch'|'tag';ref:string;name:string;sha:string}
interface PlanExecutionSource{id:string;name:string;environment?:string}
interface PlanExecutionItem{membershipID:string;applicationID:string;applicationName:string;workflowRevision:number;loadState:PlanExecutionLoadState;reason?:string;staticBlocked:boolean;sources:PlanExecutionSource[];refs:PlanExecutionReference[];selectedSourceID:string;selectedRef:string}
interface PlanExecutionGroup{id:string;name:string;mode:string;failurePolicy:string;dependencies:string[];items:PlanExecutionItem[]}
interface ReleasePlanEditorValue{id:string;description:string;groups:Array<{id:string;name:string;mode:'parallel'|'sequential';failure_policy:'stop'|'continue';depends_on_group_ids:string[];applications:Array<{application_id:string;manual_deploy:boolean;source_type:string;source_value:string}>}>}

const applications=ref<Application[]>([]),repositories=ref<Repository[]>([]),credentials=ref<Credential[]>([]),buildPlans=ref<BuildPlan[]>([]),registries=ref<Registry[]>([]),deploymentPlans=ref<DeploymentPlan[]>([]),workflowTemplates=ref<WorkflowTemplate[]>([]),runs=ref<Run[]>([]),releasePlans=ref<ReleasePlan[]>([]),deployments=ref<ResourceRecord[]>([])
const loading=ref(false),saving=ref(false),formOpen=ref(false),editingID=ref(''),registryTested=ref(false),testing=ref(false),manualOpen=ref(false),manualApplicationID=ref(''),manualApplications=ref<Application[]>([]),commitOpen=ref(false),commitOptions=ref<Array<{ref:string;name:string;sha:string;kind:'branch'|'tag'}>>([]),selectedRef=ref(''),selectedSource=ref(''),manualSources=ref<Array<{id:string;name:string;environment?:string}>>([]),currentRun=ref<Run|null>(null),currentRunSelectionKey=ref(''),selectedRunID=ref(''),log=ref({open:false,runID:'',title:'',status:''})
const planExecutionOpen=ref(false),planExecutionPlanID=ref(''),planExecutionTitle=ref(''),planExecutionExpectedUpdatedAt=ref(''),planExecutionRequestID=ref(''),planExecutionGroups=ref<PlanExecutionGroup[]>([]),planExecutionSubmitting=ref(false)
const releasePlanEditorOpen=ref(false),releasePlanEditorPlan=ref<ReleasePlan|null>(null),releasePlanEditorFocusGroupID=ref(''),releasePlanEditorAddGroup=ref(false),releasePlanMutationID=ref('')
let releaseTimer=0
let planExecutionController:AbortController|null=null
const appForm=reactive({name:'',description:'',repository_id:'',branch:'main',poll_enabled:true,poll_interval_seconds:5,watch_push:true,watch_pull_request:true,watch_tags:true,tag_pattern:'v*',build_plan_id:'',image_registry_id:'',deployment_plan_id:'',deployment_target_id:'',workflow_template_id:''})
const repoForm=reactive({name:'',provider:'github',clone_url:'',default_branch:'main',auth_type:'none',username:'',credential_id:'',webhook_enabled:true,allow_insecure_http:false})
const buildForm=reactive({name:'',kind:'dockerfile',description:'',script:'',dockerfile_path:'Dockerfile',context_path:'.',artifact_path:'',timeout_seconds:1800})
const registryForm=reactive({name:'',provider:'harbor',endpoint:'https://',namespace:'',username:'',credential:'',allow_insecure_http:false})
const deployForm=reactive({name:'',kind:'helm',description:'',script:'',helm_chart:'',helm_values:'',compose_file:'docker-compose.yml',service_name:'',timeout_seconds:600})
const releaseForm=reactive({description:'',application_ids:[] as string[]})

const copy:Record<Section,{description:string}>={applications:{description:'一个应用对应一个代码仓库，并维护自己的构建、部署和流水线配置。'},repositories:{description:'统一管理 Git 来源和可选 Webhook；凭据来自当前用户自己的令牌。'},'build-plans':{description:'保存可复用的 Dockerfile 或脚本构建配置。'},'image-registries':{description:'管理 Harbor、Docker Hub 或其他 OCI Registry；保存前必须完成真实登录测试。'},'deployment-plans':{description:'定义部署节点如何通过 Helm、Docker Compose、Docker 或受控脚本执行。'},'release-plans':{description:'发布计划组织人工批量发布；流水线运行与发布记录独立展示。'}}
const releaseView=computed(()=>route.query.view==='runs'?'runs':route.query.view==='records'?'records':'plans')
const canManage=computed(()=>props.section==='repositories'?auth.canAny(['repository.manage']):auth.canAny(['delivery.manage']))
const currentDescription=computed(()=>props.section==='release-plans'?(releaseView.value==='runs'?'查看代码事件或手动操作触发的执行、当前节点和实时日志。':releaseView.value==='records'?'查看已经进入真实部署环节的执行结果。':copy[props.section].description):copy[props.section].description)
const selectedApplicationDeploymentPlan=computed(()=>deploymentPlans.value.find(item=>item.id===appForm.deployment_plan_id))
const applicationUsesSSHDeployment=computed(()=>selectedApplicationDeploymentPlan.value?.kind==='script')
const selectedRun=computed(()=>runs.value.find(item=>item.id===selectedRunID.value)||runs.value[0]||null)
const activeRunCount=computed(()=>runs.value.filter(item=>['running','awaiting_approval'].includes(item.status)).length)
const activeRows=computed<ResourceRecord[]>(()=>props.section==='applications'?applications.value:props.section==='repositories'?repositories.value:props.section==='build-plans'?buildPlans.value:props.section==='image-registries'?registries.value:props.section==='deployment-plans'?deploymentPlans.value:[])
const activeColumns=computed(()=>props.section==='applications'?[{key:'name',label:'应用'},{key:'repository',label:'代码仓库'},{key:'build_plan',label:'构建方案'},{key:'deployment_plan',label:'部署方案'},{key:'sync_status',label:'代码状态'},{key:'last_checked_at',label:'最近检查'}]:props.section==='repositories'?[{key:'name',label:'名称'},{key:'provider',label:'平台'},{key:'clone_url',label:'Clone 地址'},{key:'default_branch',label:'默认分支'},{key:'webhook_enabled',label:'Webhook'},{key:'is_active',label:'状态'}]:props.section==='build-plans'?[{key:'name',label:'名称'},{key:'kind',label:'类型'},{key:'description',label:'说明'},{key:'context_path',label:'构建上下文'},{key:'timeout_seconds',label:'超时'}]:props.section==='image-registries'?[{key:'name',label:'名称'},{key:'provider',label:'类型'},{key:'endpoint',label:'地址'},{key:'namespace',label:'命名空间'},{key:'has_credential',label:'凭据'}]:[{key:'name',label:'名称'},{key:'kind',label:'部署方式'},{key:'description',label:'说明'},{key:'timeout_seconds',label:'超时'}])

const activeApplicationRunStatuses=new Set(['running','awaiting_approval','ready'])
const applicationCards=computed(()=>applications.value.map(application=>{
 const related=runs.value.filter(run=>run.application_id===application.id)
 const run=related.find(item=>activeApplicationRunStatuses.has(item.status))||related[0]
 const repository=application.repository?.id?application.repository:repositories.value.find(item=>item.id===application.repository_id)
 const buildPlan=application.build_plan?.id?application.build_plan:buildPlans.value.find(item=>item.id===application.build_plan_id)
 const deploymentPlan=application.deployment_plan?.id?application.deployment_plan:deploymentPlans.value.find(item=>item.id===application.deployment_plan_id)
 const workflowTemplate=application.workflow_template?.id?application.workflow_template:workflowTemplates.value.find(item=>item.id===application.workflow_template_id)
 return {application,run,repository,buildPlan,deploymentPlan,workflowTemplate,state:applicationRunStatus(run),sync:applicationSyncStatus(application.sync_status)}
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
function workflowSupportsManualRelease(application?:Application){return Boolean(application?.workflow?.nodes?.some(node=>node.type==='trigger'&&node.config?.events?.includes('manual')))}
function applicationCanManualRelease(application?:Application){return Boolean(application?.is_active&&application.workflow?.is_active&&workflowSupportsManualRelease(application))}
function releasePlanManualApplicationCount(plan:ReleasePlan){
 return (plan.groups||[]).flatMap(group=>group.applications||[]).filter(item=>applicationCanManualRelease(applications.value.find(application=>application.id===item.application_id))).length
}
function formatApplicationTime(value?:string){return value?new Date(value).toLocaleString(locale.value==='en-US'?'en-US':'zh-CN',{hour12:false}):t('applicationCard.noTime')}
function openApplicationLink(card:(typeof applicationCards.value)[number],kind:'repository'|'build'|'deployment'|'workflow'){
 if(kind==='workflow'){
  void router.push(card.workflowTemplate?`/pipeline-plans/editor?template=${card.workflowTemplate.id}`:`/pipeline-plans/editor?application=${card.application.id}`)
  return
 }
 const target=kind==='repository'?card.repository:kind==='build'?card.buildPlan:card.deploymentPlan
 if(!target){edit(card.application);return}
 void router.push(kind==='repository'?'/repositories':kind==='build'?'/build-plans':`/deployment-plans?plan=${target.id}`)
}

async function refresh(){loading.value=true;try{const requests=await Promise.all([auth.canAny(['delivery.read'])?client.get<{applications:Application[]}>('/applications'):null,auth.canAny(['repository.read'])?client.get<{repositories:Repository[]}>('/repositories'):null,auth.canAny(['credential.read'])?client.get<{credentials:Credential[]}>('/git-credentials'):null,auth.canAny(['delivery.read'])?client.get<{build_plans:BuildPlan[]}>('/build-plans'):null,auth.canAny(['delivery.read'])?client.get<{image_registries:Registry[]}>('/image-registries'):null,auth.canAny(['delivery.read'])?client.get<{deployment_plans:DeploymentPlan[]}>('/deployment-plans'):null,auth.canAny(['delivery.read'])?client.get<{workflow_templates:WorkflowTemplate[]}>('/workflow-templates'):null,auth.canAny(['delivery.read'])?client.get<{pipeline_runs:Run[]}>('/pipeline-runs?limit=200'):null,auth.canAny(['delivery.read'])?client.get<{release_plans:ReleasePlan[]}>('/release-plans'):null,auth.canAny(['deployment.read'])?client.get<{deployments:ResourceRecord[]}>('/deployments'):null]);applications.value=requests[0]?.data.applications||[];repositories.value=requests[1]?.data.repositories||[];credentials.value=requests[2]?.data.credentials||[];buildPlans.value=requests[3]?.data.build_plans||[];registries.value=requests[4]?.data.image_registries||[];deploymentPlans.value=requests[5]?.data.deployment_plans||[];workflowTemplates.value=requests[6]?.data.workflow_templates||[];runs.value=requests[7]?.data.pipeline_runs||[];if(!selectedRunID.value||!runs.value.some(item=>item.id===selectedRunID.value))selectedRunID.value=runs.value[0]?.id||'';releasePlans.value=requests[8]?.data.release_plans||[];deployments.value=requests[9]?.data.deployments||[]}catch(error){message.error(apiErrorMessage(error))}finally{loading.value=false}}
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
function resetForms(){editingID.value='';registryTested.value=false;Object.assign(appForm,{name:'',description:'',repository_id:'',branch:'main',poll_enabled:true,poll_interval_seconds:5,watch_push:true,watch_pull_request:true,watch_tags:true,tag_pattern:'v*',build_plan_id:'',image_registry_id:'',deployment_plan_id:'',deployment_target_id:'',workflow_template_id:''});Object.assign(repoForm,{name:'',provider:'github',clone_url:'',default_branch:'main',auth_type:'none',username:'',credential_id:'',webhook_enabled:true,allow_insecure_http:false});Object.assign(buildForm,{name:'',kind:'dockerfile',description:'',script:'',dockerfile_path:'Dockerfile',context_path:'.',artifact_path:'',timeout_seconds:1800});Object.assign(registryForm,{name:'',provider:'harbor',endpoint:'https://',namespace:'',username:'',credential:'',allow_insecure_http:false});Object.assign(deployForm,{name:'',kind:'helm',description:'',script:'',helm_chart:'',helm_values:'',compose_file:'docker-compose.yml',service_name:'',timeout_seconds:600});Object.assign(releaseForm,{description:'',application_ids:[]})}
function create(){resetForms();formOpen.value=true}
function edit(row:ResourceRecord){editingID.value=String(row.id);if(props.section==='applications'){const item=row as Application;Object.assign(appForm,{name:item.name,description:item.description||'',repository_id:item.repository_id,branch:item.branch||'main',poll_enabled:true,poll_interval_seconds:item.poll_interval_seconds||5,watch_push:true,watch_pull_request:true,watch_tags:true,tag_pattern:'v*',build_plan_id:item.build_plan_id||'',image_registry_id:item.image_registry_id||'',deployment_plan_id:item.deployment_plan_id||'',deployment_target_id:item.deployment_target_id||item.deployment_plan?.deployment_target_id||'',workflow_template_id:item.workflow_template_id||''})}if(props.section==='repositories'){const item=row as Repository;Object.assign(repoForm,{name:item.name,provider:item.provider,clone_url:item.clone_url,default_branch:item.default_branch,auth_type:item.auth_type,username:item.username||'',credential_id:item.credential_id||'',webhook_enabled:item.webhook_enabled,allow_insecure_http:item.allow_insecure_http})}if(props.section==='deployment-plans'){const item=row as DeploymentPlan;Object.assign(deployForm,{name:item.name,kind:item.kind,description:item.description||'',script:item.script||'',helm_chart:item.helm_chart||'',helm_values:item.helm_values||'',compose_file:item.compose_file||'docker-compose.yml',service_name:item.service_name||'',timeout_seconds:item.timeout_seconds||600})}formOpen.value=true}
async function save(){saving.value=true;try{let endpoint='',payload:unknown={},method:'post'|'put'='post';if(props.section==='applications'){const plan=selectedApplicationDeploymentPlan.value;if(!plan){message.error('请选择部署方案');return}const targetID=plan.deployment_target_id||plan.deployment_target?.id;if(!targetID){message.error('所选部署方案尚未补全，请先编辑部署方案');return}if(plan.kind!=='script'&&!appForm.build_plan_id){message.error('Docker 或 Kubernetes 发布必须选择构建方案');return}endpoint=editingID.value?`/applications/${editingID.value}`:'/applications';payload={...appForm,deployment_target_id:targetID,build_plan_id:plan.kind==='script'?'':appForm.build_plan_id,image_registry_id:plan.kind==='script'?'':appForm.image_registry_id,environments:[]};method=editingID.value?'put':'post'}if(props.section==='repositories'){endpoint=editingID.value?`/repositories/${editingID.value}`:'/repositories';payload={...repoForm,credential_id:repoForm.auth_type==='none'?null:repoForm.credential_id||null,regenerate_webhook:false};method=editingID.value?'put':'post'}if(props.section==='build-plans'){endpoint='/build-plans';payload=buildForm}if(props.section==='image-registries'){if(!registryTested.value){message.error('请先测试镜像仓库登录');return}endpoint='/image-registries';payload={...registryForm,credential:registryForm.credential||null}}if(props.section==='deployment-plans'){endpoint=editingID.value?`/deployment-plans/${editingID.value}`:'/deployment-plans';payload=deployForm;method=editingID.value?'put':'post'}if(props.section==='release-plans'){if(!releaseForm.application_ids.length){message.error('请至少选择一个应用');return}endpoint='/release-plans';payload={description:releaseForm.description,applications:releaseForm.application_ids.map(application_id=>({application_id,manual_deploy:false,source_type:'',source_value:''}))}}await client[method](endpoint,payload);message.success('配置已保存');formOpen.value=false;resetForms();await refresh()}catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}}
async function testRepository(){testing.value=true;try{const payload={...repoForm,credential_id:repoForm.auth_type==='none'?null:repoForm.credential_id||null,credential:null,regenerate_webhook:false};const result=editingID.value?await client.post<RefResult>(`/repositories/${editingID.value}/test`,undefined,{timeout:35000}):await client.post<RefResult>('/repositories/test',payload,{timeout:35000});message.success(`连接成功：${result.data.branches?.length||0} 个分支，${result.data.tags?.length||0} 个标签`)}catch(error){message.error(apiErrorMessage(error))}finally{testing.value=false}}
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
  selectedRef.value=commitOptions.value.find(item=>item.kind==='branch'&&item.name===application?.branch)?.ref||commitOptions.value.find(item=>item.kind==='branch')?.ref||commitOptions.value[0]?.ref||''
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
function openReleasePlanEditor(planID:string,groupID='',addGroup=false){
 const plan=releasePlans.value.find(item=>item.id===planID)
 if(!plan||releasePlanMutationBlocked(plan))return
 releasePlanEditorPlan.value=plan
 releasePlanEditorFocusGroupID.value=groupID
 releasePlanEditorAddGroup.value=addGroup
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
function removeReleaseGroup(planID:string,groupID:string){
 const plan=releasePlans.value.find(item=>item.id===planID)
 if(!plan||releasePlanMutationBlocked(plan))return
 Modal.confirm({
  title:t('releasePlan.editor.removeGroupConfirm'),
  content:t('releasePlan.editor.removeGroupHint'),
  okText:t('releasePlan.editor.remove'),cancelText:t('releasePlan.editor.cancel'),okType:'danger',
  async onOk(){
   releasePlanMutationID.value=planID
   try{await client.delete(`/release-plans/${planID}/groups/${groupID}`);message.success(t('releasePlan.editor.groupRemoved'));await refresh()}
   catch(error){message.error(apiErrorMessage(error))}finally{releasePlanMutationID.value=''}
  },
 })
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
  const defaultRef=refs.find(item=>item.kind==='branch'&&item.name===application?.branch)?.ref||''
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
function removeRun(run:Run){Modal.confirm({title:'删除这次流水线运行？',content:'运行数据无法恢复，独立发布记录不会删除。',okType:'danger',async onOk(){try{await client.delete(`/pipeline-runs/${run.id}`);await refresh()}catch(error){message.error(apiErrorMessage(error))}}})}
function showWebhook(item:Repository){void client.get<{webhook_url:string;webhook_secret:string}>(`/repositories/${item.id}/webhook`).then(result=>Modal.info({title:`${item.name} Webhook`,width:650,content:()=>`${result.data.webhook_url}\n${result.data.webhook_secret}`})).catch(error=>message.error(apiErrorMessage(error)))}
function linkedName(row:ResourceRecord,key:'repository'|'build_plan'|'deployment_plan'){return (row as Application)[key]?.name||'未绑定'}
function applicationName(run:Run){return applications.value.find(item=>item.id===run.application_id)?.name||run.application?.name||'未命名应用'}
function shortRef(ref?:string){return ref?.replace(/^refs\/(heads|tags)\//,'')||t('applicationCard.noRef')}
function formatRunTime(value:string){return new Date(value).toLocaleString('zh-CN',{hour12:false})}
function runStatusLabel(status:string){return ({detected:'已发现',ready:'准备就绪',blocked:'已阻塞',awaiting_approval:'等待审核',running:'执行中',succeeded:'已成功',failed:'已失败',canceled:'已取消'} as Record<string,string>)[status]||status}
function runStatusColor(status:string){return status==='succeeded'?'success':status==='failed'||status==='blocked'?'error':status==='running'?'processing':status==='awaiting_approval'?'warning':'default'}
function refreshVisibleState(){
 if(document.hidden)return
 if(props.section==='applications')void refreshApplicationState()
 else if(props.section==='release-plans'&&releaseView.value==='runs')void refreshRunState()
 else if(props.section==='release-plans')void refresh()
}

watch(()=>props.section,()=>{formOpen.value=false;resetForms();resetManualFlow();resetPlanExecution();void refresh()})
watch(()=>registryForm,()=>registryTested.value=false,{deep:true})
watch(applicationUsesSSHDeployment,(enabled)=>{if(enabled){appForm.build_plan_id='';appForm.image_registry_id=''}})
onMounted(()=>{void refresh();releaseTimer=window.setInterval(refreshVisibleState,5000);document.addEventListener('visibilitychange',refreshVisibleState);window.addEventListener('focus',refreshVisibleState)})
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
      <div class="application-commit"><small>{{ t('applicationCard.latestRun') }}</small><strong :title="card.run?.commit_message||t('applicationCard.noRun')">{{ card.run?.commit_message||t('applicationCard.noRun') }}</strong><span><GitBranch/>{{ shortRef(card.run?.ref) }}<code>{{ card.run?.commit_sha?.slice(0,8)||'—' }}</code></span></div>
      <div class="application-node"><span><Workflow/></span><div><small>{{ t('applicationCard.currentNode') }}</small><strong :title="applicationCurrentNode(card.run)">{{ applicationCurrentNode(card.run) }}</strong><time><Clock3/>{{ formatApplicationTime(card.run?.updated_at||card.run?.created_at) }}</time></div></div>
    </section>
    <div class="application-links">
      <button type="button" @click="openApplicationLink(card,'repository')"><span><GitBranch/>{{ t('applicationCard.repository') }}</span><strong :title="card.repository?.name">{{ card.repository?.name||t('applicationCard.unbound') }}</strong><ChevronRight/></button>
      <button type="button" @click="openApplicationLink(card,'build')"><span><Package/>{{ t('applicationCard.buildPlan') }}</span><strong :title="card.buildPlan?.name">{{ card.buildPlan?.name||t('applicationCard.unbound') }}</strong><ChevronRight/></button>
      <button type="button" @click="openApplicationLink(card,'deployment')"><span><Rocket/>{{ t('applicationCard.deploymentPlan') }}</span><strong :title="card.deploymentPlan?.name">{{ card.deploymentPlan?.name||t('applicationCard.unbound') }}</strong><ChevronRight/></button>
      <button type="button" @click="openApplicationLink(card,'workflow')"><span><Workflow/>{{ t('applicationCard.workflow') }}</span><strong :title="card.workflowTemplate?.name">{{ card.workflowTemplate?.name||t('applicationCard.customWorkflow') }}</strong><ChevronRight/></button>
    </div>
    <footer class="application-footer">
      <div class="application-sync" :class="[`tone-${card.sync.tone}`,{'is-live':card.sync.live}]"><i/><span><small>{{ t('applicationCard.codeStatus') }}</small><strong>{{ card.sync.label }}</strong></span><time>{{ t('applicationCard.lastChecked') }} {{ formatApplicationTime(card.application.last_checked_at) }}</time></div>
      <div class="application-actions"><a-button v-if="canManage" @click="edit(card.application)"><Settings2/>{{ t('applicationCard.configure') }}</a-button><a-button @click="router.push(`/pipeline-plans/editor?application=${card.application.id}`)"><Workflow/>{{ t('applicationCard.pipeline') }}</a-button><a-button v-if="auth.canAny(['delivery.run'])" type="primary" @click="action(`/applications/${card.application.id}/sync`)"><RefreshCw/>{{ t('applicationCard.checkUpdates') }}</a-button></div>
    </footer>
  </article>
  <a-empty v-if="!applicationCards.length&&!loading" :description="t('applicationCard.empty')"/>
</div>
<div v-else-if="props.section!=='release-plans'" class="vben-card"><ResourceTable :rows="activeRows" :columns="activeColumns" :loading="loading"><template #cell-repository="{row}">{{ linkedName(row,'repository') }}</template><template #cell-build_plan="{row}">{{ linkedName(row,'build_plan') }}</template><template #cell-deployment_plan="{row}">{{ linkedName(row,'deployment_plan') }}</template><template #cell-sync_status="{value}"><a-tag :color="value==='changed'?'warning':value==='synced'?'success':'default'">{{ value }}</a-tag></template><template #cell-kind="{value}"><a-tag color="blue">{{ value }}</a-tag></template><template #cell-timeout_seconds="{value}">{{ value }} 秒</template><template #actions="{row}"><a-button v-if="canManage&&(props.section==='repositories'||props.section==='deployment-plans')" type="link" @click="edit(row)">编辑</a-button><a-button v-if="props.section==='repositories'" type="link" @click="action(`/repositories/${row.id}/test`)">测试</a-button><a-button v-if="props.section==='repositories'&&auth.canAny(['repository.secret.read'])" type="link" @click="showWebhook(row as Repository)">Webhook</a-button></template></ResourceTable></div>
<div v-else-if="releaseView==='runs'" class="run-workspace vben-card">
  <aside class="run-index">
    <header><div><strong>运行记录</strong><small>{{ runs.length }} 次执行</small></div><span :class="{active:activeRunCount}">{{ activeRunCount }} 个进行中</span></header>
    <div class="run-index-list">
      <button v-for="run in runs" :key="run.id" type="button" :class="{active:selectedRun?.id===run.id}" @click="selectedRunID=run.id">
        <span class="run-dot" :class="run.status"/>
        <span class="run-index-copy">
          <strong>{{ applicationName(run) }}</strong>
          <span>{{ run.commit_message||'未记录提交说明' }}</span>
          <small>{{ shortRef(run.ref) }} · {{ run.current_node_name||run.current_node_id||runStatusLabel(run.status) }}</small>
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
      <div><small>提交说明</small><strong>{{ selectedRun.commit_message||'未记录提交说明' }}</strong><span>{{ shortRef(selectedRun.ref) }} · <code>{{ selectedRun.commit_sha?.slice(0,8)||'—' }}</code></span></div>
      <time>{{ formatRunTime(selectedRun.created_at) }}</time>
    </section>
    <PipelineRunGraph :key="selectedRun.id" :graph="selectedRun.execution_graph" :current-node-id="selectedRun.current_node_id" :status="selectedRun.status" :stage="selectedRun.stage"/>
    <dl class="run-facts">
      <div><dt>当前节点</dt><dd>{{ selectedRun.current_node_name||selectedRun.current_node_id||'尚未开始' }}</dd></div>
      <div><dt>流程阶段</dt><dd>{{ selectedRun.environment||'未指定' }}</dd></div>
      <div><dt>触发方式</dt><dd>{{ selectedRun.trigger||'—' }}</dd></div>
      <div><dt>状态说明</dt><dd>{{ selectedRun.message||runStatusLabel(selectedRun.status) }}</dd></div>
    </dl>
    <footer class="run-actions"><a-button @click="openLogs(selectedRun)">查看实时日志</a-button><a-button v-if="selectedRun.status==='failed'&&auth.canAny(['delivery.run'])" type="primary" @click="action(`/pipeline-runs/${selectedRun.id}/retry`)">重新执行</a-button><a-button v-if="selectedRun.status==='awaiting_approval'&&auth.canAny(['deployment.review'])" type="primary" @click="action(`/pipeline-runs/${selectedRun.id}/approve`)">通过审核</a-button><a-button v-if="selectedRun.stage==='manual'&&auth.canAny(['delivery.run'])" type="primary" @click="action(`/pipeline-runs/${selectedRun.id}/advance`)">放行并继续</a-button><a-button v-if="auth.canAny(['delivery.manage'])" danger @click="removeRun(selectedRun)">删除记录</a-button></footer>
  </main>
  <div v-else class="run-detail-empty"><a-empty description="选择一条流水线运行查看执行拓扑"/></div>
</div>
<div v-else-if="releaseView==='records'" class="vben-card"><ResourceTable :rows="deployments" :loading="loading" :columns="[{key:'target_name',label:'部署到'},{key:'platform',label:'方式'},{key:'operation',label:'操作'},{key:'image',label:'镜像'},{key:'status',label:'状态'},{key:'requested_by',label:'申请人'},{key:'approved_by',label:'审核人'},{key:'error_message',label:'失败原因'},{key:'created_at',label:'时间'}]"/></div>
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
 @toggle="toggleReleasePlan"
 @remove="removeReleasePlan"
 @remove-group="removeReleaseGroup"
/>

<a-drawer v-model:open="formOpen" :title="editingID?'编辑配置':props.section==='release-plans'?'创建发布计划':'新建配置'" width="700"><a-form layout="vertical">
<template v-if="props.section==='applications'"><div class="form-grid"><a-form-item label="应用名称" required><a-input v-model:value="appForm.name"/></a-form-item><a-form-item label="说明"><a-input v-model:value="appForm.description"/></a-form-item><a-form-item class="span2" label="代码仓库" required><a-select v-model:value="appForm.repository_id" show-search :options="repositories.filter(item=>item.is_active).map(item=>({value:item.id,label:`${item.name} · ${item.default_branch}`}))"/></a-form-item><a-form-item :label="applicationUsesSSHDeployment?'构建方案（命令部署不需要）':'构建方案'" :required="!applicationUsesSSHDeployment"><a-select v-model:value="appForm.build_plan_id" allow-clear :disabled="applicationUsesSSHDeployment" :options="buildPlans.filter(item=>item.is_active).map(item=>({value:item.id,label:item.name}))"/></a-form-item><a-form-item label="部署方案" required><a-select v-model:value="appForm.deployment_plan_id" :options="deploymentPlans.filter(item=>item.is_active&&item.deployment_target).map(item=>({value:item.id,label:`${item.name} · ${item.kind}`}))"/></a-form-item><a-form-item label="镜像仓库"><a-select v-model:value="appForm.image_registry_id" allow-clear :disabled="applicationUsesSSHDeployment" :options="registries.filter(item=>item.is_active).map(item=>({value:item.id,label:item.name}))"/></a-form-item><a-form-item label="流水线方案" required><a-select v-model:value="appForm.workflow_template_id" :options="workflowTemplates.filter(item=>item.is_active).map(item=>({value:item.id,label:`${item.name} · 第 ${item.revision} 版`}))"/></a-form-item><a-form-item label="检查间隔"><a-select v-model:value="appForm.poll_interval_seconds" :options="[3,5,10,60].map(value=>({value,label:`${value} 秒`}))"/></a-form-item><a-alert v-if="applicationUsesSSHDeployment" class="span2" type="info" show-icon message="命令脚本部署直接执行部署方案脚本，不构建或传输容器镜像。"/></div></template>
<template v-if="props.section==='repositories'"><div class="form-grid"><a-form-item label="名称" required><a-input v-model:value="repoForm.name"/></a-form-item><a-form-item label="平台"><a-select v-model:value="repoForm.provider" :options="['github','gitlab','gitea','gitee','generic'].map(value=>({value,label:value}))"/></a-form-item><a-form-item class="span2" label="Clone 地址" required><a-input v-model:value="repoForm.clone_url"/></a-form-item><a-form-item label="默认分支"><a-input v-model:value="repoForm.default_branch"/></a-form-item><a-form-item label="认证方式"><a-select v-model:value="repoForm.auth_type" :options="[{value:'none',label:'无需认证'},{value:'token',label:'访问令牌'},{value:'ssh_key',label:'SSH 私钥'}]"/></a-form-item><a-form-item v-if="repoForm.auth_type!=='none'" class="span2" label="我的令牌"><a-select v-model:value="repoForm.credential_id" :options="credentials.filter(item=>item.auth_type===repoForm.auth_type).map(item=>({value:item.id,label:`${item.name} · ${item.secret_hint}`}))"/><small>仓库只能引用当前操作者自己的令牌。</small></a-form-item><a-checkbox v-model:checked="repoForm.webhook_enabled">启用 Webhook</a-checkbox></div></template>
<template v-if="props.section==='build-plans'"><div class="form-grid"><a-form-item label="名称" required><a-input v-model:value="buildForm.name"/></a-form-item><a-form-item label="类型"><a-select v-model:value="buildForm.kind" :options="[{value:'dockerfile',label:'Dockerfile'},{value:'script',label:'打包脚本'}]"/></a-form-item><a-form-item class="span2" label="说明"><a-input v-model:value="buildForm.description"/></a-form-item><template v-if="buildForm.kind==='dockerfile'"><a-form-item label="Dockerfile 路径"><a-input v-model:value="buildForm.dockerfile_path"/></a-form-item><a-form-item label="构建上下文"><a-input v-model:value="buildForm.context_path"/></a-form-item></template><a-form-item v-else class="span2" label="打包脚本"><a-textarea v-model:value="buildForm.script" :rows="8"/></a-form-item><a-form-item label="超时（秒）"><a-input-number v-model:value="buildForm.timeout_seconds" :min="30" :max="7200"/></a-form-item></div></template>
<template v-if="props.section==='image-registries'"><div class="form-grid"><a-form-item label="名称" required><a-input v-model:value="registryForm.name"/></a-form-item><a-form-item label="类型"><a-select v-model:value="registryForm.provider" :options="[{value:'harbor',label:'Harbor'},{value:'docker_hub',label:'Docker Hub'},{value:'generic',label:'通用 Registry'}]"/></a-form-item><a-form-item class="span2" label="地址"><a-input v-model:value="registryForm.endpoint"/></a-form-item><a-form-item label="命名空间"><a-input v-model:value="registryForm.namespace"/></a-form-item><a-form-item label="用户名"><a-input v-model:value="registryForm.username"/></a-form-item><a-form-item class="span2" label="密码或 Token"><a-input-password v-model:value="registryForm.credential"/></a-form-item><a-checkbox v-model:checked="registryForm.allow_insecure_http">允许 HTTP（仅可信内网）</a-checkbox></div><a-alert v-if="registryTested" type="success" show-icon message="镜像仓库登录测试成功"/></template>
<template v-if="props.section==='deployment-plans'"><div class="form-grid"><a-form-item label="名称"><a-input v-model:value="deployForm.name"/></a-form-item><a-form-item label="部署方式"><a-select v-model:value="deployForm.kind" :options="[{value:'helm',label:'Helm'},{value:'compose',label:'Docker Compose'},{value:'docker',label:'Docker'},{value:'script',label:'自定义脚本'}]"/></a-form-item><a-form-item class="span2" label="说明"><a-input v-model:value="deployForm.description"/></a-form-item><a-form-item v-if="deployForm.kind==='helm'" class="span2" label="Chart 路径"><a-input v-model:value="deployForm.helm_chart"/></a-form-item><a-form-item v-if="deployForm.kind==='compose'" class="span2" label="Compose 文件"><a-input v-model:value="deployForm.compose_file"/></a-form-item><a-form-item v-if="deployForm.kind==='docker'" class="span2" label="Docker 容器名称"><a-input v-model:value="deployForm.service_name"/></a-form-item><a-form-item v-if="deployForm.kind==='script'" class="span2" label="部署脚本"><a-textarea v-model:value="deployForm.script" :rows="8"/></a-form-item><a-form-item label="超时（秒）"><a-input-number v-model:value="deployForm.timeout_seconds" :min="30" :max="3600"/></a-form-item></div></template>
<template v-if="props.section==='release-plans'"><a-form-item label="说明" required><a-textarea v-model:value="releaseForm.description" :rows="3"/></a-form-item><a-form-item label="应用" required><a-select v-model:value="releaseForm.application_ids" mode="multiple" :options="applications.filter(item=>item.is_active).map(item=>({value:item.id,label:item.name}))"/><small>创建计划时至少选择一个应用。执行时选择代码分支，环境和部署配置以应用当前启用的流水线为准。</small></a-form-item></template>
<div class="drawer-actions"><a-button v-if="props.section==='repositories'" :loading="testing" @click="testRepository">测试连接</a-button><a-button v-if="props.section==='image-registries'" :loading="testing" @click="testRegistry">测试登录</a-button><a-button type="primary" :loading="saving" :disabled="props.section==='image-registries'&&!registryTested" @click="save">保存</a-button></div></a-form></a-drawer>
<a-modal v-model:open="manualOpen" :title="t('manualRun.title')" :confirm-loading="saving" :ok-text="t('manualRun.chooseVersion')" @ok="nextManual" @cancel="resetManualFlow"><a-form layout="vertical"><a-form-item :label="t('manualRun.application')"><a-select v-model:value="manualApplicationID" :options="manualApplications.map(item=>({value:item.id,label:item.name}))"/></a-form-item></a-form></a-modal>
<a-modal v-model:open="commitOpen" :title="t('manualRun.versionTitle')" :confirm-loading="saving" :ok-text="t('manualRun.confirm')" @ok="executeCommit" @cancel="resetManualFlow"><a-form layout="vertical"><a-alert class="manual-release-note" type="info" show-icon :message="t('manualRun.executionHint')"/><a-form-item v-if="manualSources.length" :label="t('manualRun.source')"><a-select v-model:value="selectedSource" :options="manualSources.map(item=>({value:item.id,label:item.environment?`${item.name} · ${item.environment}`:item.name}))"/></a-form-item><a-form-item :label="t('manualRun.ref')"><a-select v-model:value="selectedRef"><a-select-opt-group :label="t('manualRun.branch')"><a-select-option v-for="item in commitOptions.filter(item=>item.kind==='branch')" :key="item.ref" :value="item.ref">{{ item.name }} · {{ item.sha.slice(0,8) }}</a-select-option></a-select-opt-group><a-select-opt-group :label="t('manualRun.tag')"><a-select-option v-for="item in commitOptions.filter(item=>item.kind==='tag')" :key="item.ref" :value="item.ref">{{ item.name }} · {{ item.sha.slice(0,8) }}</a-select-option></a-select-opt-group></a-select></a-form-item></a-form></a-modal>
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
 :focusGroupID="releasePlanEditorFocusGroupID"
 :add-group-on-open="releasePlanEditorAddGroup"
 @save="saveReleasePlanEditor"
/>
<PipelineLogDrawer v-model:open="log.open" :runID="log.runID" :title="log.title" :initial-status="log.status"/>
</section></template>

<style scoped>
.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.span2{grid-column:1/-1}.drawer-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:18px}
.application-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(620px,100%),880px));justify-content:start;gap:14px}.application-card{--application-state:#9ba1ad;position:relative;min-width:0;overflow:hidden;padding:18px;transition:transform 180ms cubic-bezier(.2,0,0,1),border-color 180ms ease,box-shadow 180ms ease}.application-card::before{position:absolute;inset:0 auto 0 0;width:3px;background:var(--application-state);content:"";opacity:.8}.application-card:hover{transform:translateY(-1px);border-color:color-mix(in srgb,var(--application-state) 36%,var(--zrt-border));box-shadow:0 10px 28px rgb(35 45 70 / 8%)}.application-card.state-info{--application-state:#4f7df3}.application-card.state-success{--application-state:#2ab573}.application-card.state-warning{--application-state:#dfa126}.application-card.state-danger{--application-state:#ed5965}
.application-head{display:flex;align-items:flex-start;justify-content:space-between;gap:20px}.application-identity{display:flex;min-width:0;align-items:center;gap:12px}.application-mark{display:grid;width:42px;height:42px;flex:0 0 42px;place-items:center;border-radius:12px;color:var(--zrt-primary);background:var(--zrt-primary-soft)}.application-mark svg{width:21px}.application-identity>div{min-width:0}.application-title{display:flex;align-items:center;gap:9px}.application-title h3{overflow:hidden;margin:0;font-size:17px;text-overflow:ellipsis;white-space:nowrap}.application-enabled{padding:2px 7px;border-radius:999px;color:#168b57;background:color-mix(in srgb,#2ab573 12%,var(--zrt-surface));font-size:11px;white-space:nowrap}.application-enabled.inactive{color:var(--zrt-muted);background:var(--zrt-surface-soft)}.application-identity p{overflow:hidden;margin:3px 0 0;color:var(--zrt-muted);text-overflow:ellipsis;white-space:nowrap}
.application-state{display:flex;flex:0 0 auto;align-items:center;gap:9px;padding:7px 11px;border-radius:10px;color:var(--application-state);background:color-mix(in srgb,var(--application-state) 9%,var(--zrt-surface))}.application-state>i,.application-sync>i{width:8px;height:8px;flex:0 0 8px;border-radius:50%;background:currentColor;box-shadow:0 0 0 4px color-mix(in srgb,currentColor 10%,transparent)}.application-card.is-live .application-state>i,.application-sync.is-live>i{animation:application-pulse 2s ease-out infinite}.application-state small,.application-state strong,.application-sync small,.application-sync strong{display:block}.application-state small,.application-sync small{color:var(--zrt-muted);font-size:11px;font-weight:400}.application-state strong{font-size:13px}
.application-run{display:grid;grid-template-columns:minmax(0,1.35fr) minmax(210px,.65fr);gap:12px;margin-top:17px;padding:14px;border:1px solid color-mix(in srgb,var(--application-state) 15%,var(--zrt-border));border-radius:12px;background:linear-gradient(135deg,color-mix(in srgb,var(--application-state) 5%,var(--zrt-surface-soft)),var(--zrt-surface-soft))}.application-commit{min-width:0}.application-commit small,.application-commit strong,.application-commit span{display:block}.application-commit small,.application-node small{color:var(--zrt-muted);font-size:11px}.application-commit strong{overflow:hidden;margin:4px 0 7px;font-size:14px;text-overflow:ellipsis;white-space:nowrap}.application-commit span{display:flex;align-items:center;gap:6px;color:var(--zrt-muted);font-size:12px}.application-commit span svg{width:14px}.application-commit code{padding:1px 5px;border-radius:5px;background:var(--zrt-surface);color:var(--zrt-text)}.application-node{display:flex;min-width:0;align-items:center;gap:10px;padding-left:14px;border-left:1px solid var(--zrt-border)}.application-node>span{display:grid;width:34px;height:34px;flex:0 0 34px;place-items:center;border-radius:10px;color:var(--application-state);background:var(--zrt-surface)}.application-node svg{width:17px}.application-node>div{min-width:0}.application-node strong{display:block;overflow:hidden;margin-top:2px;text-overflow:ellipsis;white-space:nowrap}.application-node time{display:flex;align-items:center;gap:4px;margin-top:4px;color:var(--zrt-muted);font-size:11px}.application-node time svg{width:12px}
.application-links{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:8px;margin:12px 0 0}.application-links>button{display:grid;grid-template-columns:minmax(0,1fr) 14px;min-width:0;padding:10px 10px 10px 11px;border:1px solid transparent;border-radius:9px;color:var(--zrt-text);background:var(--zrt-surface-soft);cursor:pointer;text-align:left;transition:transform 160ms ease,border-color 160ms ease,background 160ms ease}.application-links>button:hover{transform:translateY(-1px);border-color:color-mix(in srgb,var(--zrt-primary) 28%,var(--zrt-border));background:color-mix(in srgb,var(--zrt-primary) 5%,var(--zrt-surface))}.application-links>button:focus-visible{outline:2px solid color-mix(in srgb,var(--zrt-primary) 48%,transparent);outline-offset:2px}.application-links button>span{display:flex;min-width:0;align-items:center;gap:5px;color:var(--zrt-muted);font-size:11px}.application-links button>span svg{width:13px}.application-links button>strong{overflow:hidden;margin-top:4px;font-size:12px;text-overflow:ellipsis;white-space:nowrap}.application-links button>svg{grid-area:1/2/3/3;width:14px;align-self:center;color:var(--zrt-muted);transition:transform 160ms ease}.application-links>button:hover>svg{transform:translateX(2px);color:var(--zrt-primary)}.application-footer{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-top:14px;padding-top:14px;border-top:1px solid var(--zrt-border)}.application-sync{display:flex;min-width:0;align-items:center;gap:8px;color:#9ba1ad}.application-sync.tone-info{color:#4f7df3}.application-sync.tone-success{color:#2ab573}.application-sync.tone-warning{color:#dfa126}.application-sync.tone-danger{color:#ed5965}.application-sync time{margin-left:4px;color:var(--zrt-muted);font-size:11px;white-space:nowrap}.application-actions{display:flex;flex:0 0 auto;gap:7px}.application-actions :deep(.ant-btn){display:inline-flex;align-items:center;gap:5px}.application-actions svg{width:14px}
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
@media(max-width:820px){.run-workspace{grid-template-columns:1fr}.run-index-list{display:flex;min-height:0;max-height:none;overflow-x:auto;overflow-y:hidden;padding:0 7px 8px}.run-index-list>button{width:270px;flex:0 0 270px}.run-detail{padding:16px}}
@media(max-width:640px){.form-grid{grid-template-columns:1fr}.span2{grid-column:auto}.application-head,.application-footer{align-items:flex-start;flex-direction:column}.application-state{align-self:stretch}.application-run{grid-template-columns:1fr}.application-node{padding-top:12px;padding-left:0;border-top:1px solid var(--zrt-border);border-left:0}.application-actions{justify-content:flex-start;flex-wrap:wrap}.application-sync{flex-wrap:wrap}.run-commit-panel{align-items:start;grid-template-columns:22px minmax(0,1fr)}.run-commit-panel time{grid-column:2}.run-facts{grid-template-columns:1fr}.run-detail-heading{align-items:flex-start}.run-detail-heading h3{font-size:17px}}
@media(max-width:480px){.application-links{grid-template-columns:1fr}.application-actions :deep(.ant-btn){flex:1}.application-sync time{width:100%;margin-left:16px}}
@media(prefers-reduced-motion:reduce){.application-card.is-live .application-state>i,.application-sync.is-live>i,.run-status-orb.running::after{animation:none}}
</style>
