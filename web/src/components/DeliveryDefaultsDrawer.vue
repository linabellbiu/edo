<script setup lang="ts">
import {computed,onBeforeUnmount,onMounted,ref,watch} from 'vue'
import {useI18n} from 'vue-i18n'
import {message} from 'ant-design-vue'
import client from '@/api/client'
import {apiErrorMessage} from '@/api/resources'
import {deliveryKinds,getDeliveryDefaults,oneDeliveryTask,type DeliveryKind,type DeliveryDefault,type DeliveryPlan,type DeliveryTemplate} from '@/api/delivery'
import {useAuthStore} from '@/stores/auth'
const props=defineProps<{open:boolean;workflowId?:string;planId?:string;initialKind?:DeliveryKind}>()
const emit=defineEmits<{ 'update:open':[boolean] }>()
const {t}=useI18n(),auth=useAuthStore()
const rows=ref<DeliveryDefault[]>([]),plans=ref<DeliveryPlan[]>([]),templates=ref<DeliveryTemplate[]>([]),kind=ref<DeliveryKind>('docker'),workflow=ref(''),plan=ref(''),loading=ref(false),saving=ref(false),ready=ref(false)
const selectedProject=ref(localStorage.getItem('edo.project')||'')
const scopeIdentity=computed(()=>JSON.stringify([auth.user?.id,auth.user?.department_id,auth.user?.project_id,auth.user?.is_superuser,[...(auth.user?.permissions||[])].sort(),selectedProject.value]))
interface DefaultScope{identity:string;projectID:string;epoch:number}
const loadedScope=ref<DefaultScope>()
let epoch=0,disposed=false,loadController:AbortController|undefined,saveController:AbortController|undefined
const row=computed(()=>rows.value.find(item=>item.kind===kind.value))
const workflowInvalid=computed(()=>Boolean(workflow.value&&!templates.value.some(item=>item.id===workflow.value&&item.is_active&&oneDeliveryTask(item))))
const planInvalid=computed(()=>Boolean(plan.value&&!plans.value.some(item=>item.id===plan.value&&item.is_active&&item.kind===kind.value)))
const canSave=computed(()=>Boolean(ready.value&&row.value&&loadedScope.value&&currentScope(loadedScope.value)&&!workflowInvalid.value&&!planInvalid.value&&auth.canAny(['workflow.update'])&&auth.canAny(['deployment_plan.update'])))
const workflowOptions=computed(()=>{
 const options:Array<{value:string;label:string;disabled?:boolean}>=[{value:'',label:t('delivery.builtin')},...templates.value.filter(item=>item.is_active&&oneDeliveryTask(item)).map(item=>({value:item.id,label:item.name}))]
 if(workflowInvalid.value)options.push({value:workflow.value,label:templates.value.find(item=>item.id===workflow.value)?.name||t('delivery.selectedUnavailable'),disabled:true})
 return options
})
const planOptions=computed(()=>{
 const options:Array<{value:string;label:string;disabled?:boolean}>=[{value:'',label:t('delivery.builtin')},...plans.value.filter(item=>item.is_active&&item.kind===kind.value).map(item=>({value:item.id,label:item.name}))]
 if(planInvalid.value)options.push({value:plan.value,label:plans.value.find(item=>item.id===plan.value)?.name||t('delivery.selectedUnavailable'),disabled:true})
 return options
})
function captureScope():DefaultScope|undefined{
 const projectID=selectedProject.value
 if(disposed||!props.open||!auth.user||!projectID||auth.user.project_id!==projectID||localStorage.getItem('edo.project')!==projectID||!auth.canAny(['workflow.read'])||!auth.canAny(['deployment_plan.read']))return
 return {identity:scopeIdentity.value,projectID,epoch}
}
function currentScope(scope:DefaultScope){const current=captureScope();return Boolean(current&&current.identity===scope.identity&&current.projectID===scope.projectID&&current.epoch===scope.epoch)}
function reset(){epoch++;loadController?.abort();saveController?.abort();loadedScope.value=undefined;ready.value=false;loading.value=false;saving.value=false;rows.value=[];plans.value=[];templates.value=[];workflow.value='';plan.value=''}
function syncProject(){selectedProject.value=localStorage.getItem('edo.project')||''}
function selectKind(){workflow.value=row.value?.workflow_template_id||'';plan.value=row.value?.deployment_plan_id||''}
async function load(){
 const scope=captureScope();if(!scope)return
 loadController?.abort();const controller=new AbortController();loadController=controller
 const options={headers:{'X-EDO-Project-ID':scope.projectID},signal:controller.signal}
 loading.value=true;ready.value=false
 try{
  const [defaults,p,w]=await Promise.all([getDeliveryDefaults(options),client.get<{deployment_plans:DeliveryPlan[]}>('/deployment-plans',options),client.get<{workflow_templates:DeliveryTemplate[]}>('/workflow-templates',options)])
  if(controller.signal.aborted||!currentScope(scope))return
  rows.value=defaults;plans.value=p.data.deployment_plans;templates.value=w.data.workflow_templates;kind.value=props.initialKind||'docker';selectKind();if(props.workflowId)workflow.value=props.workflowId;if(props.planId)plan.value=props.planId;loadedScope.value=scope;ready.value=true
 }catch(error){if(!controller.signal.aborted&&currentScope(scope))message.error(apiErrorMessage(error))}finally{if(loadController===controller){loadController=undefined;loading.value=false}}
}
async function save(){
 const scope=loadedScope.value;if(saving.value||!scope||!currentScope(scope)||!row.value||!canSave.value)return
 const controller=new AbortController();saveController=controller;saving.value=true
 try{
  const result=await client.put<{defaults:DeliveryDefault[]}>(`/delivery-defaults/${kind.value}`,{workflow_template_id:workflow.value,deployment_plan_id:plan.value,expected_version:row.value.version},{headers:{'X-EDO-Project-ID':scope.projectID},signal:controller.signal})
  if(controller.signal.aborted||!currentScope(scope))return
  rows.value=result.data.defaults;message.success(t('delivery.saved'))
 }catch(error){if(!controller.signal.aborted&&currentScope(scope))message.error(apiErrorMessage(error))}finally{if(saveController===controller){saveController=undefined;saving.value=false}}
}
watch(()=>props.open,value=>{if(value)void load();else reset()})
watch(scopeIdentity,()=>{reset();emit('update:open',false)},{flush:'sync'})
onMounted(()=>window.addEventListener('edo:projects-changed',syncProject))
onBeforeUnmount(()=>{disposed=true;reset();window.removeEventListener('edo:projects-changed',syncProject)})
</script>
<template>
<a-drawer :open="open" :title="t('delivery.defaults')" width="min(640px, 100vw)" @update:open="(value:boolean)=>emit('update:open',value)">
<a-spin :spinning="loading"><a-alert type="info" show-icon :message="t('delivery.defaultsHint')" style="margin-bottom:20px"/>
<a-form layout="vertical"><a-form-item :label="t('delivery.kind')"><a-select :aria-label="t('delivery.kind')" v-model:value="kind" :disabled="saving||!ready" :options="deliveryKinds.map(value=>({value,label:t(`delivery.${value}`)}))" @change="selectKind"/></a-form-item>
<a-alert v-if="row&&(!row.workflow_available||!row.deployment_available)" type="warning" :message="t('delivery.unavailable')" style="margin-bottom:16px"/>
<a-form-item :label="t('delivery.workflow')" :validate-status="workflowInvalid?'error':undefined" :help="workflowInvalid?t('delivery.workflowRequirement'):undefined"><a-select :aria-label="t('delivery.workflow')" v-model:value="workflow" :disabled="saving||!ready" :options="workflowOptions"/></a-form-item>
<a-form-item :label="t('delivery.deployment')" :validate-status="planInvalid?'error':undefined" :help="planInvalid?t('delivery.planRequirement'):undefined"><a-select :aria-label="t('delivery.deployment')" v-model:value="plan" :disabled="saving||!ready" :options="planOptions"/></a-form-item>
<p class="delivery-copy">{{ t('delivery.independent') }}</p>
<a-button v-if="auth.canAny(['workflow.update'])&&auth.canAny(['deployment_plan.update'])" type="primary" :disabled="!canSave" :loading="saving" @click="save">{{ t('delivery.save') }}</a-button></a-form></a-spin>
</a-drawer>
</template>
<style scoped>.delivery-copy{color:var(--edo-muted);line-height:1.7}</style>
