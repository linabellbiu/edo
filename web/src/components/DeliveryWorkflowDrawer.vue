<script setup lang="ts">
import {computed,onBeforeUnmount,onMounted,reactive,ref,watch} from 'vue'
import {useI18n} from 'vue-i18n'
import {message} from 'ant-design-vue'
import client from '@/api/client'
import {apiErrorMessage} from '@/api/resources'
import {deliveryKinds,getDeliveryDefaults,oneDeliveryTask,type DeliveryKind,type DeliveryDefault,type DeliveryPlan,type DeliveryTemplate} from '@/api/delivery'
import {listHosts,listEnvironments,environmentIDsOf,type InfrastructureHost,type InfrastructureEnvironment} from '@/api/infrastructure'
import type {WorkflowResponse} from '@/types/pipeline'
import ComposeDeliveryFields from '@/components/ComposeDeliveryFields.vue'
import {newComposeDeliveryForm} from '@/utils/composeDelivery'
import {useAuthStore} from '@/stores/auth'
const props=defineProps<{open:boolean;applicationId?:string;initialKind?:DeliveryKind;initialCluster?:string;initialNamespace?:string;initialWorkload?:string;initialContainer?:string}>()
const emit=defineEmits<{'update:open':[boolean];created:[WorkflowResponse]}>()
const {t}=useI18n()
const auth=useAuthStore()
const composeForm=ref(newComposeDeliveryForm()),composeFields=ref<{validate:()=>boolean}>()
const existingCompose=computed(()=>form.kind==='compose_existing')
const canCreateCompose=computed(()=>auth.canAny?.(['build_plan.create'])||false)
const defaults=ref<DeliveryDefault[]>([]),plans=ref<DeliveryPlan[]>([]),templates=ref<DeliveryTemplate[]>([]),builds=ref<Array<{id:string;name:string;kind:string;is_active:boolean;image_registry_id?:string;artifact_path?:string}>>([]),applications=ref<Array<{id:string;name:string}>>([])
const environments=ref<InfrastructureEnvironment[]>([]),hosts=ref<InfrastructureHost[]>([]),clusters=ref<Array<{id:string;name:string;is_active:boolean}>>([])
const loading=ref(false),saving=ref(false),ready=ref(false)
const form=reactive({application:'',kind:'docker' as DeliveryKind|'compose_existing',name:'',workflow:'',plan:'',build:'',environment:'',host:'',cluster:'',namespace:'default',workload:'',container:'',directory:''})
const currentDefault=computed(()=>defaults.value.find(item=>item.kind===form.kind))
const workflowOptions=computed(()=>[{value:'',label:t('delivery.builtin')},...templates.value.filter(item=>item.is_active&&oneDeliveryTask(item)).map(item=>({value:item.id,label:item.name}))])
const planOptions=computed(()=>[{value:'',label:t('delivery.builtin')},...plans.value.filter(item=>item.is_active&&item.kind===form.kind).map(item=>({value:item.id,label:item.name}))])
const buildOptions=computed(()=>builds.value.filter(item=>item.is_active&&(form.kind==='script'?item.kind==='script'&&Boolean(item.artifact_path):item.kind==='dockerfile'&&(form.kind!=='kubernetes'||Boolean(item.image_registry_id)))).map(item=>({value:item.id,label:item.name})))
const environmentHosts=computed(()=>hosts.value.filter(host=>host.is_active&&environmentIDsOf(host).includes(form.environment)))
function supported(host:InfrastructureHost){if(existingCompose.value)return host.mode==='ssh'&&host.capabilities.some(cap=>cap.kind==='ssh'&&cap.status==='ready')&&host.capabilities.some(cap=>cap.kind==='docker'&&cap.status==='ready');return host.capabilities.some(cap=>cap.status==='ready'&&(form.kind==='kubernetes'?cap.kind==='kubernetes':form.kind==='script'?['ssh','local_exec'].includes(cap.kind):cap.kind==='docker'))}
const hostOptions=computed(()=>environmentHosts.value.filter(supported).map(host=>({value:host.id,label:host.name})))
// 集群列表已由接口按项目和权限过滤，独立集群不依赖环境中的 SSH 主机。
const clusterOptions=computed(()=>clusters.value.filter(cluster=>cluster.is_active).map(cluster=>({value:cluster.id,label:cluster.name})))
function selectKind(){form.workflow=currentDefault.value?.workflow_template_id||'';form.plan=currentDefault.value?.deployment_plan_id||'';form.build='';form.host='';form.cluster=''}
let epoch=0,disposed=false,controller:AbortController|undefined
const loadedIdentity=ref(''),loadedProject=ref('')
function identity(){return JSON.stringify([auth.user?.id,auth.user?.project_id,auth.user?.department_id,auth.user?.is_superuser,[...(auth.user?.permissions||[])].sort(),typeof localStorage==='undefined'?'':localStorage.getItem('edo.project')])}
function reset(){epoch++;controller?.abort();ready.value=false;loading.value=false;saving.value=false;loadedIdentity.value='';loadedProject.value='';defaults.value=[];plans.value=[];templates.value=[];builds.value=[];applications.value=[];environments.value=[];hosts.value=[];clusters.value=[];composeForm.value=newComposeDeliveryForm()}
function current(){return !disposed&&props.open&&loadedIdentity.value===identity()&&!!loadedProject.value}
function projectChanged(){if(loadedIdentity.value&&loadedIdentity.value!==identity()){reset();emit('update:open',false)}}
async function load(){
 const project=auth.user?.project_id
 if(!project||typeof localStorage==='undefined'||localStorage.getItem('edo.project')!==project)return
 const request=++epoch,scope=identity();controller?.abort();controller=new AbortController()
 const options={headers:{'X-EDO-Project-ID':project},signal:controller.signal}
 loading.value=true;ready.value=false
 try{
  const [d,p,w,b,a,e,h,c]=await Promise.all([getDeliveryDefaults(options),client.get<{deployment_plans:DeliveryPlan[]}>('/deployment-plans',options),client.get<{workflow_templates:DeliveryTemplate[]}>('/workflow-templates',options),client.get<{build_plans:typeof builds.value}>('/build-plans',options),props.applicationId?Promise.resolve({data:{applications:[]}}):client.get<{applications:typeof applications.value}>('/applications',options),listEnvironments(options.signal),listHosts(options.signal),client.get<{clusters:typeof clusters.value}>('/kubernetes/clusters',options)])
  if(disposed||request!==epoch||scope!==identity()||!props.open)return
  defaults.value=d;plans.value=p.data.deployment_plans;templates.value=w.data.workflow_templates;builds.value=b.data.build_plans;applications.value=a.data.applications;environments.value=e;hosts.value=h;clusters.value=c.data.clusters
  Object.assign(form,{application:props.applicationId||'',kind:props.initialKind&&deliveryKinds.includes(props.initialKind)?props.initialKind:'docker',name:'',environment:'',namespace:props.initialNamespace||'default',workload:props.initialWorkload||'',container:props.initialContainer||'',directory:''})
  selectKind();form.cluster=props.initialCluster||''
  if(form.cluster){const found=hosts.value.find(host=>host.is_active&&host.capabilities.some(cap=>cap.kind==='kubernetes'&&cap.runtime_id===form.cluster));form.environment=found?environmentIDsOf(found)[0]||'':''}
  loadedIdentity.value=scope;loadedProject.value=project;ready.value=true
 }catch(error){if(request===epoch&&scope===identity())message.error(apiErrorMessage(error))}finally{if(request===epoch)loading.value=false}
}
async function submit(path:string,body:unknown){
 if(!current())return
 const scope=identity();controller?.abort();controller=new AbortController();const signal=controller.signal
 saving.value=true
 try{
  const result=await client.post<WorkflowResponse>(path,body,{headers:{'X-EDO-Project-ID':loadedProject.value},signal})
  if(signal.aborted||!current()||identity()!==scope)return
  message.success(t('delivery.created'));emit('created',result.data);emit('update:open',false)
 }catch(error){if(!signal.aborted&&current()&&identity()===scope)message.error(apiErrorMessage(error))}finally{if(identity()===scope)saving.value=false}
}
async function create(){
 if(saving.value||!current())return
 if(!ready.value||!form.application||(!existingCompose.value&&!form.build)||!form.environment||(form.kind==='kubernetes'?!form.cluster||!form.namespace||!form.workload||!form.container:!form.host)){message.error(t('delivery.required'));return}
 if(existingCompose.value){
  if(!canCreateCompose.value)return
  if(!composeFields.value?.validate())return
  if(!form.directory.startsWith('/')||form.directory.split('/').includes('..')){message.error('请输入目标主机上已有项目的绝对部署目录');return}
  await submit(`/applications/${form.application}/compose-delivery-workflows`,{...composeForm.value,name:form.name,target:{name:'application_compose',platform:'ssh',environment_id:form.environment,host_id:form.host,working_directory:form.directory}})
  return
 }
 const host=hosts.value.find(item=>item.id===form.host)
 const runtime=form.kind==='kubernetes'?form.cluster:form.kind==='script'?'':host?.capabilities.find(cap=>cap.kind==='docker')?.runtime_id||''
 await submit(`/applications/${form.application}/delivery-workflows`,{kind:form.kind,name:form.name,build_plan_id:form.build,workflow_template_id:form.workflow||'builtin',deployment_plan_id:form.plan||'builtin',expected_default_version:currentDefault.value?.version||0,target:{name:'application_delivery',platform:form.kind==='kubernetes'?'kubernetes':form.kind==='script'?'ssh':'docker',environment_id:form.environment,host_id:form.host,runtime_id:runtime,namespace:form.kind==='kubernetes'?form.namespace:'',workload_name:form.kind==='kubernetes'?form.workload:'',container_name:form.kind==='kubernetes'?form.container:'',working_directory:form.kind==='script'?form.directory:''}})
}
watch(()=>props.open,value=>{if(value)void load();else reset()})
watch(identity,()=>{if(props.open){reset();emit('update:open',false)}},{flush:'sync'})
onMounted(()=>window.addEventListener('edo:projects-changed',projectChanged))
onBeforeUnmount(()=>{disposed=true;reset();window.removeEventListener('edo:projects-changed',projectChanged)})
watch(()=>form.host,()=>{if(existingCompose.value)composeForm.value.use_sudo=Boolean(hosts.value.find(item=>item.id===form.host)?.use_sudo)})
watch(()=>props.open,value=>{if(value)composeForm.value=newComposeDeliveryForm()})
</script>
<template>
<a-drawer :open="open" :title="t('delivery.configure')" width="min(760px, 100vw)" @update:open="(value:boolean)=>emit('update:open',value)">
<a-spin :spinning="loading"><a-alert type="info" show-icon :message="t('delivery.independent')" class="hint"/>
<a-alert v-if="currentDefault&&(!currentDefault.workflow_available||!currentDefault.deployment_available)" type="warning" :message="t('delivery.unavailable')" class="hint"/>
<a-form layout="vertical"><div class="grid">
<a-form-item v-if="!applicationId" class="wide" :label="t('delivery.application')" required><a-select :aria-label="t('delivery.application')" v-model:value="form.application" :options="applications.map(item=>({value:item.id,label:item.name}))"/></a-form-item>
<a-form-item :label="t('delivery.kind')"><a-select :aria-label="t('delivery.kind')" v-model:value="form.kind" :options="[...deliveryKinds.map(value=>({value,label:t(`delivery.${value}`)})),{value:'compose_existing',label:'已有 Compose 项目（多镜像）',disabled:!canCreateCompose}]" @change="selectKind"/></a-form-item>
<a-form-item :label="t('delivery.name')"><a-input :aria-label="t('delivery.name')" v-model:value="form.name" :maxlength="128"/></a-form-item>
<a-form-item v-if="!existingCompose" :label="t('delivery.workflow')"><a-select :aria-label="t('delivery.workflow')" v-model:value="form.workflow" :options="workflowOptions"/></a-form-item>
<a-form-item v-if="!existingCompose" :label="t('delivery.deployment')"><a-select :aria-label="t('delivery.deployment')" v-model:value="form.plan" :options="planOptions"/></a-form-item>
<a-form-item v-if="!existingCompose" class="wide" :label="t('delivery.build')" :help="t('delivery.buildHint')" required><a-select :aria-label="t('delivery.build')" v-model:value="form.build" :options="buildOptions"/></a-form-item>
<ComposeDeliveryFields v-if="existingCompose" ref="composeFields" v-model="composeForm" :builds="builds" :disabled="saving||!ready"/>
<a-form-item :label="t('delivery.environment')" required><a-select :aria-label="t('delivery.environment')" v-model:value="form.environment" :options="environments.filter(item=>item.is_active).map(item=>({value:item.id,label:item.name}))" @change="()=>{form.host='';form.cluster=''}"/></a-form-item>
<a-form-item v-if="form.kind==='kubernetes'" :label="t('delivery.cluster')" required><a-select :aria-label="t('delivery.cluster')" v-model:value="form.cluster" :options="clusterOptions"/></a-form-item>
<a-form-item v-else :label="t('delivery.host')" required><a-select :aria-label="t('delivery.host')" v-model:value="form.host" :options="hostOptions"/></a-form-item>
<template v-if="form.kind==='kubernetes'">
<a-form-item :label="t('delivery.namespace')" required><a-input :aria-label="t('delivery.namespace')" v-model:value="form.namespace"/></a-form-item><a-form-item :label="t('delivery.workload')" required><a-input :aria-label="t('delivery.workload')" v-model:value="form.workload"/></a-form-item><a-form-item class="wide" :label="t('delivery.container')" required><a-input :aria-label="t('delivery.container')" v-model:value="form.container"/></a-form-item>
</template>
<a-form-item v-if="form.kind==='script'||existingCompose" class="wide" :label="t('delivery.directory')" :required="existingCompose"><a-input :aria-label="t('delivery.directory')" v-model:value="form.directory" placeholder="目标主机上的绝对路径"/></a-form-item>
</div>
<a-alert v-if="form.kind==='kubernetes'" type="info" :message="t('delivery.targetHint')" class="hint"/>
<a-alert v-if="form.kind==='script'" type="warning" :message="t('delivery.scriptHint')" class="hint"/>
<a-button type="primary" :disabled="!ready" :loading="saving" @click="create">{{ t('delivery.create') }}</a-button>
</a-form></a-spin></a-drawer>
</template>
<style scoped>.grid{display:grid;grid-template-columns:1fr 1fr;gap:0 16px}.wide{grid-column:1/-1}.hint{margin-bottom:20px}@media(max-width:600px){.grid{grid-template-columns:1fr}}</style>
