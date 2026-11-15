import type {WorkflowDeploymentOverride,WorkflowDeploymentPlan} from '@/types/pipeline'

type ObjectValue=Record<string,unknown>
const object=(value:unknown):value is ObjectValue=>Boolean(value)&&typeof value==='object'&&!Array.isArray(value)
const clone=<T>(value:T):T=>value===undefined?value:JSON.parse(JSON.stringify(value))

// 对象字段递归覆盖，数组整体替换；null 清除可选字段，显式 false 和 0 保留。
export function mergeDeploymentPatch(base:unknown,patch:unknown):unknown {
 if(!object(patch))return clone(patch)
 const result:ObjectValue=object(base)?clone(base):{}
 for(const [key,value]of Object.entries(patch)){
  if(value===null)delete result[key]
  else result[key]=mergeDeploymentPatch(result[key],value)
 }
 return result
}
export function deploymentDifference(base:unknown,current:unknown):ObjectValue {
 const result:ObjectValue={},left=object(base)?base:{},right=object(current)?current:{}
 for(const key of new Set([...Object.keys(left),...Object.keys(right)])){
  if(JSON.stringify(left[key])===JSON.stringify(right[key]))continue
  if(right[key]===undefined)result[key]=null
  else if(object(left[key])&&object(right[key])){const diff=deploymentDifference(left[key],right[key]);if(Object.keys(diff).length)result[key]=diff}
  else result[key]=clone(right[key])
 }
 return result
}
export function effectiveKubernetesPlan(base:WorkflowDeploymentPlan,override?:WorkflowDeploymentOverride):WorkflowDeploymentPlan {
 const plan=clone(base)
 plan.deployment_target=clone(override?.deployment_target||base.deployment_target||{platform:'kubernetes',environment_id:''})
 plan.timeout_seconds=override?.timeout_seconds??base.timeout_seconds
 plan.execution_config=clone(base.execution_config||{})
 for(const key of ['deployment_registry_id','deployment_input_version','deployment_inputs'] as const)if(override?.[key]!==undefined)(plan.execution_config as Record<string,unknown>)[key]=clone(override[key])
 if(override?.kubernetes)plan.execution_config.kubernetes=mergeDeploymentPatch(plan.execution_config.kubernetes,override.kubernetes) as NonNullable<typeof plan.execution_config.kubernetes>
 if(override?.artifact_node_id!==undefined){plan.execution_config.artifact_node_id=override.artifact_node_id||undefined;delete plan.execution_config.artifact_build_plan_id}
 return plan
}
function stageTarget(target:WorkflowDeploymentPlan['deployment_target']):WorkflowDeploymentPlan['deployment_target'] {
 if(!target)return undefined
 return {platform:'kubernetes',environment_id:'',runtime_id:target.runtime_id,namespace:target.namespace,workload_name:target.workload_name,container_name:target.container_name}
}
export function stageKubernetesOverride(base:WorkflowDeploymentPlan,current:WorkflowDeploymentPlan):WorkflowDeploymentOverride|undefined {
 const result:WorkflowDeploymentOverride={}
 if(current.deployment_target)result.deployment_target=stageTarget(current.deployment_target)
 for(const key of ['deployment_registry_id','deployment_input_version','deployment_inputs'] as const)if(JSON.stringify(current.execution_config?.[key])!==JSON.stringify(base.execution_config?.[key]))(result as Record<string,unknown>)[key]=clone(current.execution_config?.[key]??(key==='deployment_inputs'?[]:key==='deployment_input_version'?1:''))
 const diff=deploymentDifference(base.execution_config?.kubernetes,current.execution_config?.kubernetes)
 if(Object.keys(diff).length)result.kubernetes=diff
 if(current.timeout_seconds!==base.timeout_seconds)result.timeout_seconds=current.timeout_seconds
 if(current.execution_config?.artifact_node_id!==base.execution_config?.artifact_node_id||current.execution_config?.artifact_build_plan_id!==base.execution_config?.artifact_build_plan_id)result.artifact_node_id=current.execution_config?.artifact_node_id||''
 return Object.keys(result).length?result:undefined
}
