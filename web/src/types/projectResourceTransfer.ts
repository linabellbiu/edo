export interface ProjectTransferResourceRef {
  kind: string
  id: string
}

export interface ProjectTransferResource extends ProjectTransferResourceRef {
  name: string
}

export interface ProjectTransferInput {
  target_project_id: string
  resources: ProjectTransferResourceRef[]
  include_dependencies?: boolean
  confirmation?: string
}

export interface ProjectTransferProject {
  id: string
  name: string
  department_name?: string
}

export interface ProjectTransferCatalog {
  kinds: Array<{ kind: string; label: string }>
  resources: ProjectTransferResource[]
  source_project?: ProjectTransferProject
  projects: ProjectTransferProject[]
}

export interface ProjectTransferPlan {
  source_project_id: string
  target_project_id: string
  resources: Array<ProjectTransferResource & { automatic: boolean }>
  blockers: Array<ProjectTransferResource & { message: string }>
  history_count: number
  confirmation: string
}
