<script setup lang="ts">
import {computed,onBeforeUnmount,ref,watch} from 'vue'
import {PlusCircle,MinusCircle} from 'lucide-vue-next'
import client from '@/api/client'
import {apiErrorMessage} from '@/api/resources'
import {useAuthStore} from '@/stores/auth'
import {listEnvironments,type InfrastructureEnvironment} from '@/api/infrastructure'
import DeploymentExecutionFields from '@/components/DeploymentExecutionFields.vue'
import DeploymentHostSelection from '@/components/DeploymentHostSelection.vue'
import DeploymentScriptEditor from '@/components/DeploymentScriptEditor.vue'
import YamlCodeEditor from '@/components/YamlCodeEditor.vue'
import {readKubeYaml} from '@/utils/kubernetesYaml'
import type {WorkflowNode,WorkflowNodeConfig,WorkflowDeploymentPlan,DeploymentExecutionConfig,PipelineBuildPlan} from '@/types/pipeline'
const props=defineProps<{node:WorkflowNode;disabled:boolean;applicationID?:string;applicationName?:string;branch?:string;buildNodes:WorkflowNode[];buildPlans?:PipelineBuildPlan[]}>()
const emit=defineEmits<{change:[patch:Partial<WorkflowNodeConfig>]}>()
const auth=useAuthStore(),environments=ref<InfrastructureEnvironment[]>([]),configs=ref<Array<{id:string;name:string}>>([]),clusters=ref<Array<{id:string;name:string}>>([]),error=ref(''),services=ref<string[]>([]),serviceError=ref(''),serviceLoading=ref(false),repoPreviewCommit=ref('')
const registryHost=ref<HTMLElement>(),groupsReady=ref(false)
const plan=computed(()=>props.node.config.inline_deployment_plan)
const hostOptions=computed(()=>Object.fromEntries(environments.value.filter(group=>group.is_active).map(group=>[group.id,(group.hosts||[]).filter(host=>host.is_active&&host.capabilities.some(cap=>cap.kind===(host.mode==='local'?'local_exec':'ssh')&&cap.status==='ready')&&(plan.value?.kind!=='compose_stack'||host.capabilities.some(cap=>cap.kind==='docker'&&cap.status==='ready'&&cap.runtime_id)))])))
const groupHosts=computed(()=>hostOptions.value[plan.value?.deployment_target?.environment_id||'']||[])
const execution=computed(()=>plan.value?.execution_config||{})
const sourceKind=computed(()=>props.node.config.compose_source?.kind||'editor')
const runtimeServices=computed(()=>props.node.config.runtime_services||[])
const buildPlanFor=(node:WorkflowNode)=>node.config.inline_build||props.buildPlans?.find(plan=>plan.id===node.config.build_plan_id)
const fileBuildNodes=computed(()=>props.buildNodes.filter(node=>node.config.build_outputs?.length||buildPlanFor(node)?.kind==='script'&&Boolean(buildPlanFor(node)?.artifact_path)))
const imageBuildNodes=computed(()=>props.buildNodes.filter(node=>buildPlanFor(node)?.kind==='dockerfile'&&Boolean(buildPlanFor(node)?.image_registry_id)))
let controller:AbortController|undefined,previewController:AbortController|undefined,timer:ReturnType<typeof setTimeout>|undefined
const identity=()=>JSON.stringify([auth.user?.id,auth.user?.project_id,auth.user?.permissions,localStorage.getItem('edo.project')])
watch(identity,async()=>{controller?.abort();previewController?.abort();controller=new AbortController();const current=identity(),signal=controller.signal;environments.value=[];configs.value=[];clusters.value=[];error.value='';groupsReady.value=false;const results=await Promise.allSettled([listEnvironments(signal),auth.canAny(['delivery_variable.read'])?client.get('/delivery-variables',{signal}):Promise.resolve({data:{variables:[]}}),auth.canAny(['cluster.read'])?client.get('/kubernetes/clusters',{signal}):Promise.resolve({data:{clusters:[]}})]);if(signal.aborted||current!==identity())return;for(const [i,result]of results.entries()){if(result.status==='rejected'){error.value=[error.value,apiErrorMessage(result.reason)].filter(Boolean).join('；');continue}if(i===0){environments.value=result.value as InfrastructureEnvironment[];groupsReady.value=true;}if(i===1)configs.value=(result.value as {data:{variables:Array<{id:string;name:string;kind:string;is_active:boolean;is_secret?:boolean;project_id?:string;application_id?:string;environment_id?:string}>}}).data.variables.filter(v=>v.is_active&&v.kind==='config_yaml'&&!v.is_secret&&(!v.project_id||v.project_id===auth.user?.project_id)&&(!v.application_id||!props.applicationID||v.application_id===props.applicationID));if(i===2)clusters.value=(result.value as {data:{clusters:Array<{id:string;name:string}>}}).data.clusters||[]}},{immediate:true})
function change(patch:Partial<WorkflowNodeConfig>){if(!props.disabled)emit('change',patch)}
function update(patch:Partial<WorkflowDeploymentPlan>){if(plan.value)change({inline_deployment_plan:{...plan.value,...patch,name:props.node.name}})}
function updateExecution(patch:Partial<DeploymentExecutionConfig>){update({execution_config:{...execution.value,...patch}})}
function target(patch:Record<string,unknown>){if(plan.value?.deployment_target)update({deployment_target:{...plan.value.deployment_target,...patch}})}
function source(kind:string){services.value=[];serviceError.value='';change({compose_source:kind==='editor'?undefined:{kind:kind as 'repository',path:kind==='repository'?'compose.yaml':undefined}})}
function yaml(value:string){update({compose_yaml:value})}
async function upload(file:File){if(file.size>128*1024){serviceError.value='compose.yaml 不能超过 128 KiB';return false}const scope=identity(),nodeID=props.node.id,value=await file.text();if(!props.disabled&&scope===identity()&&nodeID===props.node.id){source('editor');yaml(value)}return false}
async function preview(){
 previewController?.abort();previewController=new AbortController()
 const signal=previewController.signal,current=identity(),nodeID=props.node.id
 services.value=[];serviceError.value='';repoPreviewCommit.value=''
 if(!plan.value||plan.value.kind!=='compose_stack')return
 const selected=props.node.config.compose_source
 let payload:Record<string,unknown>
 if(sourceKind.value==='editor'){
  const content=plan.value.compose_yaml||'',parsed=readKubeYaml(content)
  if(parsed.issues.length||!parsed.object?.services){serviceError.value='请填写有效的 compose.yaml，并检查编辑器诊断';return}
  payload={content}
 }else{
  if(!selected)return
  if(selected.kind==='upstream'){serviceError.value='本次上游产物生成后，部署前读取并解析服务；解析失败会阻断部署。';return}
  if(!props.applicationID){serviceError.value='关联应用后可读取并解析服务';return}
  if(selected.kind==='repository'&&!selected.path||selected.kind==='configuration'&&!selected.configuration_id)return
  payload={source:selected,environment_id:plan.value.deployment_target?.environment_id,ref:props.branch}
 }
 serviceLoading.value=true
 const path=props.applicationID?`/applications/${encodeURIComponent(props.applicationID)}/compose-services`:'/workflows/compose-services'
 try{
  const {data}=await client.post(path,payload,{signal,timeout:35_000})
  if(!signal.aborted&&current===identity()&&nodeID===props.node.id){services.value=data.services;repoPreviewCommit.value=data.commit_sha||''}
 }catch(e){if(!signal.aborted&&current===identity()&&nodeID===props.node.id)serviceError.value=apiErrorMessage(e)}
 finally{if(!signal.aborted&&nodeID===props.node.id)serviceLoading.value=false}
}

watch(()=>JSON.stringify([identity(),props.node.id,props.node.config.compose_source,plan.value?.compose_yaml,plan.value?.deployment_target?.environment_id,props.applicationID,props.branch]),()=>{clearTimeout(timer);previewController?.abort();services.value=[];serviceLoading.value=false;timer=setTimeout(preview,500)},{immediate:true})
onBeforeUnmount(()=>{controller?.abort();previewController?.abort();clearTimeout(timer)})
function binding(index:number,patch:Partial<{name:string;restart_script:string;recreate_script:string}>){change({runtime_services:runtimeServices.value.map((v,i)=>i===index?{...v,...patch}:v)})}
const settings=computed(()=>execution.value.compose||{project_name:'',tag_variable:'${EDO_IMAGE_TAG}',use_sudo:false,adopt_existing:false})
function compose(patch:Record<string,unknown>){updateExecution({compose:{...settings.value,...patch}})}
</script>
<template>
 <section v-if="plan" class="pipeline-deploy-fields">
  <a-alert v-if="error" type="warning" show-icon :message="error"/>
  <DeploymentHostSelection :groupID="plan.deployment_target?.environment_id" :groups="environments.filter(group=>group.is_active)" :model-value="plan.deployment_target?.host_ids" :legacyHostID="plan.deployment_target?.host_id" :hosts="groupHosts" :host-options="hostOptions" :group-only="plan.kind==='kubernetes'" :ready="groupsReady" :disabled="disabled" @change="target({environment_id:$event.groupID,host_ids:plan.kind==='kubernetes'?undefined:$event.hostIDs,host_id:undefined,runtime_id:plan.kind==='kubernetes'?plan.deployment_target?.runtime_id:undefined})" @update:model-value="target({host_ids:$event,host_id:undefined,runtime_id:undefined})"/>
  <div ref="registryHost"/>
  <template v-if="plan.kind==='compose_stack'">
   <a-form-item label="compose.yaml 来源" required><a-radio-group :value="sourceKind" :disabled="disabled" :options="[{value:'repository',label:'仓库文件'},{value:'editor',label:'上传 / 编辑'},{value:'configuration',label:'配置文件'},{value:'upstream',label:'上游产物'}]" @update:value="source(String($event))"/></a-form-item>
   <a-form-item v-if="sourceKind==='repository'" required><template #label><span>仓库内文件路径 <a-tooltip title="相对于代码仓库根目录；预览读取当前分支，实际部署读取本次运行固定 Commit。所有来源都要解析 services，读取或解析失败会阻断部署。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="仓库 Compose 文件说明">?</button></a-tooltip></span></template><a-input :value="node.config.compose_source?.path" :disabled="disabled" placeholder="deploy/compose.yaml" @update:value="change({compose_source:{kind:'repository',path:String($event)}})"/></a-form-item>
   <template v-else-if="sourceKind==='editor'"><a-form-item required><YamlCodeEditor :model-value="plan.compose_yaml||''" :readonly="disabled" label="compose.yaml" fullscreen collapsible default-collapsed @update:model-value="yaml($event)"><template #toolbar><span>compose.yaml</span><a-upload :show-upload-list="false" accept=".yaml,.yml" :before-upload="upload" :disabled="disabled"><a-button :disabled="disabled">上传compose.yaml</a-button></a-upload></template></YamlCodeEditor></a-form-item></template>
   <a-form-item v-else-if="sourceKind==='configuration'" required><template #label><span>配置文件 <a-tooltip title="选择 YAML 配置，EDO 只返回解析后的服务名。部署时固定所用版本；敏感值请放在单独的 ENV 配置或密钥变量中。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="Compose 配置来源说明">?</button></a-tooltip></span></template><a-select :value="node.config.compose_source?.configuration_id||undefined" :disabled="disabled" :options="configs.map(v=>({value:v.id,label:v.name}))" placeholder="选择 YAML 配置，自动解析服务" @update:value="change({compose_source:{kind:'configuration',configuration_id:String($event)}})"/></a-form-item>
   <template v-else><a-form-item label="上游构建产出环境变量" required><a-input :value="node.config.compose_source?.artifact_reference || (node.config.compose_source?.artifact_name?'${'+node.config.compose_source.artifact_name+'}':'')" :disabled="disabled" placeholder="例如 ${BUILD_ARTIFACT}" @update:value="change({compose_source:{kind:'upstream',artifact_reference:String($event),path:node.config.compose_source?.path||'compose.yaml'}})"/></a-form-item><a-form-item label="产物内文件路径" required><a-input :value="node.config.compose_source?.path" :disabled="disabled" placeholder="deploy/compose.yaml" @update:value="change({compose_source:{...node.config.compose_source!,path:String($event)}})"/></a-form-item></template>
   <a-alert v-if="serviceError" :type="sourceKind==='upstream'?'info':'warning'" show-icon :message="serviceError"/>
   <a-form-item><template #label><span>部署服务 <a-tooltip title="自动解析所选文件中的 services。留空执行整个 compose.yaml 项目；选择后只启动指定服务，依赖由 docker compose 处理。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="部署服务说明">?</button></a-tooltip></span></template><a-select mode="multiple" :value="settings.services||[]" :loading="serviceLoading" :disabled="disabled" :options="services.map(v=>({value:v,label:v}))" placeholder="全部服务" @update:value="compose({services:$event})"/><small v-if="repoPreviewCommit">预览版本：{{repoPreviewCommit.slice(0,12)}}</small></a-form-item>
   <a-form-item label="Docker Compose 项目名" required><a-input :value="settings.project_name" :disabled="disabled" placeholder="例如 my_app_test" @update:value="compose({project_name:String($event)})"/></a-form-item>
  </template>
  <template v-else-if="plan.kind==='kubernetes'"><a-form-item label="上游镜像"><a-select allow-clear :value="execution.artifact_node_id||undefined" :disabled="disabled" :options="imageBuildNodes.map(v=>({value:v.id,label:v.name}))" placeholder="只有一个上游镜像时自动选择" @update:value="updateExecution({artifact_node_id:String($event||'')||undefined})"/></a-form-item><a-form-item label="集群" required><a-select :value="plan.deployment_target?.runtime_id||undefined" :disabled="disabled" :options="clusters.map(v=>({value:v.id,label:v.name}))" @update:value="target({runtime_id:String($event)})"/></a-form-item><a-form-item label="命名空间" required><a-input :value="plan.deployment_target?.namespace" :disabled="disabled" @update:value="target({namespace:String($event)})"/></a-form-item><a-form-item label="Deployment" required><a-input :value="plan.deployment_target?.workload_name" :disabled="disabled" @update:value="target({workload_name:String($event)})"/></a-form-item><a-form-item label="容器名称" required><a-input :value="plan.deployment_target?.container_name" :disabled="disabled" @update:value="target({container_name:String($event)})"/></a-form-item></template>
  <template v-else>
   <a-form-item required><DeploymentScriptEditor :model-value="plan.script||''" kind="script" :disabled="disabled" edit-permission="workflow.update" @update:model-value="update({script:$event})"/></a-form-item>
   <a-form-item label="工作目录"><a-input :value="plan.deployment_target?.working_directory" :disabled="disabled" placeholder="例如 /srv/my_app" @update:value="target({working_directory:String($event)})"/></a-form-item>
   <section class="runtime-bindings"><header><span>关联 Docker 服务 <a-tooltip title="可选。只有手动填写的容器名才会出现在该应用中，不自动发现同主机其他容器。重启和重建只执行本次成功部署保存的命令；命令修改需下一次部署成功后生效。配置正文会读取最新内容；更新容器环境时请使用你填写的重建命令。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="关联 Docker 服务说明">?</button></a-tooltip></span><a-button type="link" :disabled="disabled||runtimeServices.length>=32" @click="change({runtime_services:[...runtimeServices,{name:'',restart_script:'',recreate_script:''}]})"><PlusCircle :size="15"/>添加</a-button></header>
    <a-collapse v-for="(service,index) in runtimeServices" :key="index" :default-active-key="['runtime']"><a-collapse-panel key="runtime" :header="service.name||'Docker 服务'"><template #extra><a-button type="text" :disabled="disabled" aria-label="取消服务关联" @click.stop="change({runtime_services:runtimeServices.filter((_,i)=>i!==index)})"><MinusCircle :size="16"/></a-button></template><a-form-item label="Docker 服务名（容器名）" required><a-input :value="service.name" :disabled="disabled" placeholder="例如 my_app_api" @update:value="binding(index,{name:String($event)})"/></a-form-item><a-form-item required><DeploymentScriptEditor :model-value="service.restart_script" label="重启命令" :example-text="'docker restart my_app_api'" kind="script" :disabled="disabled" edit-permission="workflow.update" @update:model-value="binding(index,{restart_script:$event})"/></a-form-item><a-form-item required><DeploymentScriptEditor :model-value="service.recreate_script" label="重建命令" :example-text="'docker compose up -d --force-recreate api'" kind="script" :disabled="disabled" edit-permission="workflow.update" @update:model-value="binding(index,{recreate_script:$event})"/></a-form-item></a-collapse-panel></a-collapse>
   </section>
  </template>
  <DeploymentExecutionFields v-if="plan.kind!=='kubernetes'" :model-value="execution" :plan="plan" :disabled="disabled" edit-permission="workflow.update" :applicationID="applicationID" :applicationName="applicationName" :build-nodes="buildNodes" hide-script hide-compose-settings :registry-target="registryHost" :timeout-min="60" @update:timeout-seconds="update({timeout_seconds:$event})" @update:model-value="update({execution_config:$event})">
   <template #before-advanced>
  <a-form-item v-if="plan.kind==='compose_stack'"><template #label><span>自定义参数 <a-tooltip title="可重复填写 --env-file 相对路径，只能引用已选择的配置文件；参数随成功部署保存，后续重启或重建沿用。" :trigger="['hover','focus','click']"><button type="button" class="field-help" aria-label="Docker Compose 自定义参数说明">?</button></a-tooltip></span></template><a-input :value="execution.deployment_parameters" :disabled="disabled" placeholder="例如 --env-file .prod.env" @update:value="updateExecution({deployment_parameters:String($event)})"/></a-form-item>
   </template>
  </DeploymentExecutionFields>
  <a-collapse v-if="plan.kind==='kubernetes'" class="deployment-advanced-options"><a-collapse-panel key="advanced" header="高级选项"><a-form-item label="超时（秒）"><a-input-number :value="plan.timeout_seconds" :disabled="disabled" :min="60" :max="3600" @update:value="update({timeout_seconds:Number($event)})"/></a-form-item></a-collapse-panel></a-collapse>
 </section>
</template>
<style scoped>
.compose-field-header{display:flex;align-items:center;flex-wrap:wrap;gap:12px;margin-bottom:8px}.pipeline-deploy-fields,.runtime-bindings{display:grid;gap:12px;min-width:0}.pipeline-deploy-fields :deep(.ant-form-item){margin-bottom:0}.pipeline-deploy-fields :deep(.ant-form-item-label){padding-bottom:4px}.runtime-bindings>header{display:flex;align-items:center;justify-content:space-between}.field-help{width:20px;height:20px;padding:0;border:1px solid var(--edo-border);border-radius:50%;background:transparent;color:var(--edo-muted);cursor:help}.field-help:focus-visible{outline:2px solid var(--edo-primary);outline-offset:2px}
</style>
