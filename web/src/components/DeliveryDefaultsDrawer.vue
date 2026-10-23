<script setup lang="ts">
import {computed,ref,watch} from 'vue'
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
const row=computed(()=>rows.value.find(item=>item.kind===kind.value))
const canSave=computed(()=>ready.value&&auth.canAny(['workflow.update'])&&auth.canAny(['deployment_plan.update']))
const workflowOptions=computed(()=>[{value:'',label:t('delivery.builtin')},...templates.value.filter(item=>item.is_active&&oneDeliveryTask(item)).map(item=>({value:item.id,label:item.name}))])
const planOptions=computed(()=>[{value:'',label:t('delivery.builtin')},...plans.value.filter(item=>item.is_active&&item.kind===kind.value).map(item=>({value:item.id,label:item.name}))])
function selectKind(){workflow.value=row.value?.workflow_template_id||'';plan.value=row.value?.deployment_plan_id||''}
async function load(){loading.value=true;ready.value=false;try{const [defaults,p,w]=await Promise.all([getDeliveryDefaults(),client.get<{deployment_plans:DeliveryPlan[]}>('/deployment-plans'),client.get<{workflow_templates:DeliveryTemplate[]}>('/workflow-templates')]);rows.value=defaults;plans.value=p.data.deployment_plans;templates.value=w.data.workflow_templates;kind.value=props.initialKind||'docker';selectKind();if(props.workflowId)workflow.value=props.workflowId;if(props.planId)plan.value=props.planId;ready.value=true}catch(error){message.error(apiErrorMessage(error))}finally{loading.value=false}}
async function save(){if(!row.value||!canSave.value)return;saving.value=true;try{const result=await client.put<{defaults:DeliveryDefault[]}>(`/delivery-defaults/${kind.value}`,{workflow_template_id:workflow.value,deployment_plan_id:plan.value,expected_version:row.value.version});rows.value=result.data.defaults;message.success(t('delivery.saved'))}catch(error){message.error(apiErrorMessage(error));await load()}finally{saving.value=false}}
watch(()=>props.open,value=>{if(value)void load()})
</script>
<template>
<a-drawer :open="open" :title="t('delivery.defaults')" width="640" @update:open="(value:boolean)=>emit('update:open',value)">
<a-spin :spinning="loading"><a-alert type="info" show-icon :message="t('delivery.defaultsHint')" style="margin-bottom:20px"/>
<a-form layout="vertical"><a-form-item :label="t('delivery.kind')"><a-select :aria-label="t('delivery.kind')" v-model:value="kind" :options="deliveryKinds.map(value=>({value,label:t(`delivery.${value}`)}))" @change="selectKind"/></a-form-item>
<a-alert v-if="row&&(!row.workflow_available||!row.deployment_available)" type="warning" :message="t('delivery.unavailable')" style="margin-bottom:16px"/>
<a-form-item :label="t('delivery.workflow')"><a-select :aria-label="t('delivery.workflow')" v-model:value="workflow" :options="workflowOptions"/></a-form-item>
<a-form-item :label="t('delivery.deployment')"><a-select :aria-label="t('delivery.deployment')" v-model:value="plan" :options="planOptions"/></a-form-item>
<p class="delivery-copy">{{ t('delivery.independent') }}</p>
<a-button v-if="canSave" type="primary" :loading="saving" @click="save">{{ t('delivery.save') }}</a-button></a-form></a-spin>
</a-drawer>
</template>
<style scoped>.delivery-copy{color:var(--edo-muted);line-height:1.7}</style>
