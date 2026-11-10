export type WorkflowNodeType =
  | 'trigger'
  | 'build'
  | 'shell'
  | 'manual'
  | 'approval'
  | 'deploy'

export interface WorkflowNotificationRule {
  id: string
  channel_id: string
  on_success: boolean
  on_failure: boolean
  title?: string
  message?: string
}

export interface DeploymentInput {
 artifact_reference?:string
 kind: 'configuration'|'upstream'|'registry'
 path?:string
 configuration_id?:string
 artifact_node_id?:string
 artifact_build_plan_id?:string
 artifact_name?:string
 registry_id?:string
 image?:string
 tag?:string
}

export interface WorkflowNodeConfig {
 deployment_registry_id?: string
 inline_build?: PipelineBuildPlan
 build_outputs?: Array<{name:string;paths:string[]}>
 runtime_services?: Array<{name:string;restart_script:string;recreate_script:string}>
 compose_source?: {artifact_reference?:string;kind:'repository'|'configuration'|'upstream';path?:string;configuration_id?:string;artifact_node_id?:string;artifact_name?:string}

 artifact_build_plan_id?:string
 deployment_input_version?:number
 deployment_inputs?:DeploymentInput[]
 deployment_script?:string
 deployment_tag_reference?:string
	deployment_source?: 'inline' | 'plan'
	inline_deployment_plan?: WorkflowDeploymentPlan
  configuration_files?: Array<{id:string;path:string}>
  deployment_parameters?: string
 status_writeback?:boolean
  branch?: string
  events?: string[]
  tag_pattern?: string
  pr_target_pattern?: string
  pr_source_pattern?: string
  pr_actions?: string[]
  build_plan_id?: string
  image_name?: string
  image_tag?: string
  deployment_plan_id?: string
  script?: string
  runtime_image?: string
  toolchain_language?: 'go' | 'nodejs' | 'python'
  toolchain_version?: string
  timeout_seconds?: number
  working_directory?: string
  environment_variables?: Record<string, string>
  variable_names?: string[]
  variable_environment_id?: string
  artifact_node_id?: string
  artifact_node_ids?: string[]
  compose_images?: Array<{service:string;artifact_node_id:string}>
  compose?: {resolved_project_name?:string;services?:string[];env_file?:string;tag_variable:string;use_sudo:boolean;health_url?:string;adopt_existing:boolean}
  reports?: Array<{format:string;paths:string[];required:boolean;minimum_coverage?:number;fail_severity?:string}>
  check?: {tool:string;dialect?:string;path:string;config_path?:string}
  cache?: {key:string;lock_files:string[];paths:string[]}
  migration?: {database_id:string;directory:string}
  description?: string
  notifications?: WorkflowNotificationRule[]
}

export type DeploymentExecutionConfig = Pick<WorkflowNodeConfig,
  | 'deployment_input_version' | 'deployment_inputs' | 'deployment_script' | 'deployment_tag_reference'
  | 'deployment_registry_id' | 'configuration_files' | 'deployment_parameters' | 'environment_variables' | 'variable_names'
  | 'artifact_node_id' | 'artifact_build_plan_id' | 'artifact_node_ids' | 'compose_images' | 'compose'>

export interface WorkflowDeploymentPlan {
  execution_config?:DeploymentExecutionConfig
  name:string
  kind:'script'|'docker'|'compose'|'compose_stack'|'kubernetes'
  description?:string
  script?:string
  compose_yaml?:string
  service_name?:string
  docker_config?:unknown
  timeout_seconds:number
  deployment_target?:{platform:'ssh'|'docker'|'kubernetes';environment_id:string;host_ids?:string[]|null;host_id?:string;runtime_id?:string;working_directory?:string;namespace?:string;workload_name?:string;container_name?:string;rollout_timeout?:number}
}

export interface WorkflowNode {
  id: string
  type: WorkflowNodeType
  name: string
  config: WorkflowNodeConfig
}

export interface WorkflowStage {
  id: string
  name: string
  tasks: WorkflowNode[]
}

export interface Workflow {
  schema_version: 1
  id: string
  application_id?: string
  workflow_template_id?: string
  workflow_template_revision?: number
  workflow_template?: { id: string; name: string }
  name: string
  description?: string
  revision: number
  is_active: boolean
  source: WorkflowNode
  stages: WorkflowStage[]
}

export interface WorkflowCatalogEntry extends Workflow {
  kind: 'template' | 'application'
  project_id: string
  application_name?: string
  application_count: number
  updated_at: string
}

export interface WorkflowIssue {
  code: string
  message: string
  node_id?: string
  stage_id?: string
}

export interface WorkflowResponse {
  workflow: Workflow
  valid: boolean
  issues: WorkflowIssue[]
}

export interface WorkflowTemplateResponse {
  workflow_template: Workflow
  valid: boolean
  issues: WorkflowIssue[]
}

export interface PipelineRunGraphNode {
  id: string
  type: WorkflowNodeType
  name: string
  environment?: string
}

export interface PipelineExecutionStage {
 batches?:string[][]
  id: string
  name: string
  tasks: PipelineRunGraphNode[]
  timing?: PipelineStageTiming
}

export interface PipelineStageTiming {
  state: string
  started_at?: string
  finished_at?: string
  duration_ms: number | null
  running: boolean
}

export interface PipelineExecutionGraph {
  schema_version: 1
  source: PipelineRunGraphNode
  stages: PipelineExecutionStage[]
}

export type PipelineStageDraft = WorkflowStage

export interface PipelineBuildPlan {
  id: string
  name: string
  kind: 'dockerfile' | 'script'
  description?: string
  script?: string
  dockerfile_path?: string
  context_path?: string
  working_directory?: string
  artifact_path?: string
  runtime_image?: string
  image_registry_id?: string
  pull?: boolean
  cache_enabled?: boolean
  build_args?: Record<string, string>
  environment_variables?: Record<string, string>
  timeout_seconds: number
  is_active: boolean
  created_at?: string
  updated_at?: string
}
