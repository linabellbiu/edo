export type WorkflowNodeType =
  | 'trigger'
  | 'build'
  | 'shell'
  | 'manual'
  | 'approval'
  | 'deploy'

export interface WorkflowNodeConfig {
  branch?: string
  events?: string[]
  tag_pattern?: string
  pr_target_pattern?: string
  pr_source_pattern?: string
  pr_actions?: string[]
  build_plan_id?: string
  deployment_plan_id?: string
  script?: string
  runtime_image?: string
  toolchain_language?: 'go' | 'nodejs' | 'python'
  toolchain_version?: string
  timeout_seconds?: number
  working_directory?: string
  environment_variables?: Record<string, string>
  description?: string
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
  name: string
  description?: string
  revision: number
  is_active: boolean
  source: WorkflowNode
  stages: WorkflowStage[]
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
  id: string
  name: string
  tasks: PipelineRunGraphNode[]
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
  target_stage?: string
  pull?: boolean
  cache_enabled?: boolean
  build_args?: Record<string, string>
  environment_variables?: Record<string, string>
  timeout_seconds: number
  is_active: boolean
  created_at?: string
  updated_at?: string
}
