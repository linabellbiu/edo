<script setup lang="ts">
import {computed,reactive,ref,watch} from 'vue'
import {useI18n} from 'vue-i18n'
import {message} from 'ant-design-vue'
import client from '@/api/client'
import {apiErrorMessage} from '@/api/resources'
import {deliveryKinds,getDeliveryDefaults,oneDeliveryTask,type DeliveryKind,type DeliveryDefault,type DeliveryPlan,type DeliveryTemplate} from '@/api/delivery'
import {listHosts,listEnvironments,environmentIDsOf,type InfrastructureHost,type InfrastructureEnvironment} from '@/api/infrastructure'
import type {WorkflowResponse} from '@/types/pipeline'
const props=defineProps<{open:boolean;applicationId?:string;initialKind?:DeliveryKind;initialCluster?:string;initialNamespace?:string;initialWorkload?:string;initialContainer?:string}>()
const emit=defineEmits<{'update:open':[boolean];created:[WorkflowResponse]}>()
const {t}=useI18n()
const defaults=ref<DeliveryDefault[]>([]),plans=ref<DeliveryPlan[]>([]),templates=ref<DeliveryTemplate[]>([]),builds=ref<Array<{id:string;name:string;kind:string;is_active:boolean;image_registry_id?:string;artifact_path?:string}>>([]),applications=ref<Array<{id:string;name:string}>>([])
const environments=ref<InfrastructureEnvironment[]>([]),hosts=ref<InfrastructureHost[]>([]),clusters=ref<Array<{id:string;name:string;is_active:boolean}>>([])
const loading=ref(false),saving=ref(false),ready=ref(false)
const form=reactive({application:'',kind:'docker' as DeliveryKind,name:'',workflow:'',plan:'',build:'',environment:'',host:'',cluster:'',namespace:'default',workload:'',container:'',directory:''})
const currentDefault=computed(()=>defaults.value.find(item=>item.kind===form.kind))
const workflowOptions=computed(()=>[{value:'',label:t('delivery.builtin')},...templates.value.filter(item=>item.is_active&&oneDeliveryTask(item)).map(item=>({value:item.id,label:item.name}))])
const planOptions=computed(()=>[{value:'',label:t('delivery.builtin')},...plans.value.filter(item=>item.is_active&&item.kind===form.kind).map(item=>({value:item.id,label:item.name}))])
const buildOptions=computed(()=>builds.value.filter(item=>item.is_active&&(form.kind==='script'?item.kind==='script'&&Boolean(item.artifact_path):item.kind==='dockerfile'&&(form.kind!=='kubernetes'||Boolean(item.image_registry_id)))).map(item=>({value:item.id,label:item.name})))
const environmentHosts=computed(()=>hosts.value.filter(host=>host.is_active&&environmentIDsOf(host).includes(form.environment)))
function supported(host:InfrastructureHost){return host.capabilities.some(cap=>cap.status==='ready'&&(form.kind==='kubernetes'?cap.kind==='kubernetes':form.kind==='script'?['ssh','local_exec'].includes(cap.kind):cap.kind==='docker'))}
const hostOptions=computed(()=>environmentHosts.value.filter(supported).map(host=>({value:host.id,label:host.name})))
const clusterOptions=computed(()=>clusters.value.filter(cluster=>cluster.is_active&&environmentHosts.value.some(host=>host.capabilities.some(cap=>cap.kind==='kubernetes'&&cap.status==='ready'&&cap.runtime_id===cluster.id))).map(cluster=>({value:cluster.id,label:cluster.name})))
function selectKind(){form.workflow=currentDefault.value?.workflow_template_id||'';form.plan=currentDefault.value?.deployment_plan_id||'';form.build='';form.host='';form.cluster=''}
async function load(){loading.value=true;ready.value=false;try{const [d,p,w,b,a,e,h,c]=await Promise.all([getDeliveryDefaults(),client.get<{deployment_plans:DeliveryPlan[]}>('/deployment-plans'),client.get<{workflow_templates:DeliveryTemplate[]}>('/workflow-templates'),client.get<{build_plans:typeof builds.value}>('/build-plans'),props.applicationId?Promise.resolve({data:{applications:[]}}):client.get<{applications:typeof applications.value}>('/applications'),listEnvironments(),listHosts(),client.get<{clusters:typeof clusters.value}>('/kubernetes/clusters')]);defaults.value=d;plans.value=p.data.deployment_plans;templates.value=w.data.workflow_templates;builds.value=b.data.build_plans;applications.value=a.data.applications;environments.value=e;hosts.value=h;clusters.value=c.data.clusters;Object.assign(form,{application:props.applicationId||'',kind:props.initialKind&&deliveryKinds.includes(props.initialKind)?props.initialKind:'docker',name:'',environment:'',namespace:props.initialNamespace||'default',workload:props.initialWorkload||'',container:props.initialContainer||'',directory:''});selectKind();form.cluster=props.initialCluster||'';if(form.cluster){const found=hosts.value.find(host=>host.is_active&&host.capabilities.some(cap=>cap.kind==='kubernetes'&&cap.runtime_id===form.cluster));form.environment=found?environmentIDsOf(found)[0]||'':''}ready.value=true}catch(error){message.error(apiErrorMessage(error))}finally{loading.value=false}}
async function create(){
 if(!ready.value||!form.application||!form.build||!form.environment||(form.kind==='kubernetes'?!form.cluster||!form.namespace||!form.workload||!form.container:!form.host)){message.error(t('delivery.required'));return}
 const host=hosts.value.find(item=>item.id===form.host)
 const runtime=form.kind==='kubernetes'?form.cluster:form.kind==='script'?'':host?.capabilities.find(cap=>cap.kind==='docker')?.runtime_id||''
 saving.value=true
 try{const result=await client.post<WorkflowResponse>(`/applications/${form.application}/delivery-workflows`,{kind:form.kind,name:form.name,build_plan_id:form.build,workflow_template_id:form.workflow||'builtin',deployment_plan_id:form.plan||'builtin',expected_default_version:currentDefault.value?.version||0,target:{name:'application_delivery',platform:form.kind==='kubernetes'?'kubernetes':form.kind==='script'?'ssh':'docker',environment_id:form.environment,host_id:form.host,runtime_id:runtime,namespace:form.kind==='kubernetes'?form.namespace:'',workload_name:form.kind==='kubernetes'?form.workload:'',container_name:form.kind==='kubernetes'?form.container:'',working_directory:form.kind==='script'?form.directory:''}});message.success(t('delivery.created'));emit('update:open',false);emit('created',result.data)}catch(error){message.error(apiErrorMessage(error))}finally{saving.value=false}
}
watch(()=>props.open,value=>{if(value)void load()})
</script>
<template>
<a-drawer :open="open" :title="t('delivery.configure')" width="680" @update:open="(value:boolean)=>emit('update:open',value)">
<a-spin :spinning="loading"><a-alert type="info" show-icon :message="t('delivery.independent')" class="hint"/>
<a-alert v-if="currentDefault&&(!currentDefault.workflow_available||!currentDefault.deployment_available)" type="warning" :message="t('delivery.unavailable')" class="hint"/>
<a-form layout="vertical"><div class="grid">
<a-form-item v-if="!applicationId" class="wide" :label="t('delivery.application')" required><a-select :aria-label="t('delivery.application')" v-model:value="form.application" :options="applications.map(item=>({value:item.id,label:item.name}))"/></a-form-item>
<a-form-item :label="t('delivery.kind')"><a-select :aria-label="t('delivery.kind')" v-model:value="form.kind" :options="deliveryKinds.map(value=>({value,label:t(`delivery.${value}`)}))" @change="selectKind"/></a-form-item>
<a-form-item :label="t('delivery.name')"><a-input :aria-label="t('delivery.name')" v-model:value="form.name" :maxlength="128"/></a-form-item>
<a-form-item :label="t('delivery.workflow')"><a-select :aria-label="t('delivery.workflow')" v-model:value="form.workflow" :options="workflowOptions"/></a-form-item>
<a-form-item :label="t('delivery.deployment')"><a-select :aria-label="t('delivery.deployment')" v-model:value="form.plan" :options="planOptions"/></a-form-item>
<a-form-item class="wide" :label="t('delivery.build')" :help="t('delivery.buildHint')" required><a-select :aria-label="t('delivery.build')" v-model:value="form.build" :options="buildOptions"/></a-form-item>
<a-form-item :label="t('delivery.environment')" required><a-select :aria-label="t('delivery.environment')" v-model:value="form.environment" :options="environments.filter(item=>item.is_active).map(item=>({value:item.id,label:item.name}))" @change="()=>{form.host='';form.cluster=''}"/></a-form-item>
<a-form-item v-if="form.kind==='kubernetes'" :label="t('delivery.cluster')" required><a-select :aria-label="t('delivery.cluster')" v-model:value="form.cluster" :options="clusterOptions"/></a-form-item>
<a-form-item v-else :label="t('delivery.host')" required><a-select :aria-label="t('delivery.host')" v-model:value="form.host" :options="hostOptions"/></a-form-item>
<template v-if="form.kind==='kubernetes'">
<a-form-item :label="t('delivery.namespace')" required><a-input :aria-label="t('delivery.namespace')" v-model:value="form.namespace"/></a-form-item><a-form-item :label="t('delivery.workload')" required><a-input :aria-label="t('delivery.workload')" v-model:value="form.workload"/></a-form-item><a-form-item class="wide" :label="t('delivery.container')" required><a-input :aria-label="t('delivery.container')" v-model:value="form.container"/></a-form-item>
</template>
<a-form-item v-if="form.kind==='script'" class="wide" :label="t('delivery.directory')"><a-input :aria-label="t('delivery.directory')" v-model:value="form.directory"/></a-form-item>
</div>
<a-alert v-if="form.kind==='kubernetes'" type="info" :message="t('delivery.targetHint')" class="hint"/>
<a-alert v-if="form.kind==='script'" type="warning" :message="t('delivery.scriptHint')" class="hint"/>
<a-button type="primary" :disabled="!ready" :loading="saving" @click="create">{{ t('delivery.create') }}</a-button>
</a-form></a-spin></a-drawer>
</template>
<style scoped>.grid{display:grid;grid-template-columns:1fr 1fr;gap:0 16px}.wide{grid-column:1/-1}.hint{margin-bottom:20px}@media(max-width:600px){.grid{grid-template-columns:1fr}}</style>
