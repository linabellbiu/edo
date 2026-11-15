<script setup lang="ts">
import {computed} from 'vue'
import {useAuthStore} from '@/stores/auth'
import PipelineDeployFields from '@/components/PipelineDeployFields.vue'
import {effectiveKubernetesPlan,stageKubernetesOverride} from '@/utils/kubernetesPlanOverride'
import type {WorkflowDeploymentPlan,WorkflowDeploymentOverride,WorkflowNode,WorkflowNodeConfig,PipelineBuildPlan} from '@/types/pipeline'
const props=defineProps<{node:WorkflowNode;plan:WorkflowDeploymentPlan&{id:string;is_active?:boolean};disabled:boolean;applicationID?:string;applicationName?:string;branch?:string;buildNodes:WorkflowNode[];buildPlans?:PipelineBuildPlan[];buildTaskLabels?:Record<string,string>}>()
const emit=defineEmits<{change:[patch:Partial<WorkflowNodeConfig>];invalid:[error:string]}>()
const auth=useAuthStore()
const networkOverrideKeys=['node_port','ports','service_type','external_name']
const canRestore=computed(()=>canEdit.value&&auth.canAny(['kubernetes.workload.update'])&&(!networkOverrideKeys.some(key=>key in (props.node.config.deployment_override?.kubernetes||{}))||auth.canAny(['kubernetes.network.update'])))
const effective=computed(()=>effectiveKubernetesPlan(props.plan,props.node.config.deployment_override))
const editableNode=computed<WorkflowNode>(()=>({...props.node,config:{...props.node.config,inline_deployment_plan:effective.value}}))
const canEdit=computed(()=>!props.disabled&&props.plan.is_active!==false)
const overrideCount=computed(()=>Object.keys(props.node.config.deployment_override?.kubernetes||{}).length+(props.node.config.deployment_override?.timeout_seconds===undefined?0:1))
function change(patch:Partial<WorkflowNodeConfig>){
 if(!canEdit.value||!patch.inline_deployment_plan)return
 emit('change',{deployment_override:stageKubernetesOverride(props.plan,patch.inline_deployment_plan)})
}
function restore(){
 if(!canRestore.value)return
 const next:WorkflowDeploymentOverride={...props.node.config.deployment_override}
 delete next.kubernetes;delete next.timeout_seconds
 emit('change',{deployment_override:Object.keys(next).length?next:undefined})
}
</script>
<template>
 <section class="kubernetes-plan-stage-fields">
  <header><span>本阶段配置 <a-tag v-if="overrideCount">已覆盖 {{overrideCount}} 项</a-tag><span v-else class="stage-default">资源参数使用方案默认值</span></span><a-button v-if="overrideCount" type="link" size="small" :disabled="!canRestore" @click="restore">恢复参数默认</a-button></header>
  <a-alert v-if="plan.is_active===false" type="warning" show-icon message="原部署方案已停用，已保留关联。"/>
  <PipelineDeployFields @invalid="emit('invalid',$event)" :node="editableNode" :disabled="!canEdit" :applicationID="applicationID" :applicationName="applicationName" :branch="branch" :build-nodes="buildNodes" :build-plans="buildPlans" :build-task-labels="buildTaskLabels" @change="change"/>
 </section>
</template>
<style scoped>
.kubernetes-plan-stage-fields{display:grid;gap:14px;min-width:0}.kubernetes-plan-stage-fields>header{display:flex;align-items:center;justify-content:space-between;gap:12px;border-top:1px solid var(--edo-border);padding-top:12px}.kubernetes-plan-stage-fields>header>span{display:flex;align-items:center;gap:8px;flex-wrap:wrap;font-weight:500}.stage-default{font-size:12px;color:var(--edo-muted);font-weight:400}@media(max-width:420px){.kubernetes-plan-stage-fields>header{align-items:flex-start;flex-wrap:wrap}}
</style>
