import type { DeploymentExecutionConfig, WorkflowNodeConfig, WorkflowNode } from '@/types/pipeline'

export const deploymentExecutionKeys = ['deployment_registry_id', 'deployment_input_version', 'deployment_inputs', 'deployment_script', 'deployment_tag_reference',
  'configuration_files', 'deployment_parameters', 'environment_variables', 'variable_names', 'artifact_node_id', 'artifact_build_plan_id',
  'artifact_node_ids', 'compose_images', 'compose'] as const

export function deploymentExecution(config: WorkflowNodeConfig, builds: WorkflowNode[] = []): DeploymentExecutionConfig {
  const result: DeploymentExecutionConfig = JSON.parse(JSON.stringify(Object.fromEntries(deploymentExecutionKeys.filter(key => config[key] !== undefined).map(key => [key, config[key]]))))
  const planFor = (id?:string) => {
    const plan = builds.find(item=>item.id===id)?.config.build_plan_id
    return plan && builds.filter(item=>item.config.build_plan_id===plan).length===1 ? plan : undefined
  }
  if (planFor(result.artifact_node_id)) { result.artifact_build_plan_id=planFor(result.artifact_node_id); delete result.artifact_node_id }
  for (const input of result.deployment_inputs || []) if (input.kind==='upstream' && !input.artifact_reference && planFor(input.artifact_node_id)) { input.artifact_build_plan_id=planFor(input.artifact_node_id); delete input.artifact_node_id }
  return result
}

export function clearTaskDeploymentExecution(): Partial<WorkflowNodeConfig> {
  return Object.fromEntries(deploymentExecutionKeys.map(key => [key, undefined]))
}

export function savedDeploymentRegistry(config?: DeploymentExecutionConfig): string {
  return config?.deployment_registry_id || config?.deployment_inputs?.find(input=>input.kind==='registry' && input.registry_id)?.registry_id || ''
}
