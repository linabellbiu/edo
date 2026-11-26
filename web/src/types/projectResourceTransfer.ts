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

export interface ProjectTransferCatalog {
  kinds: Array<{ kind: string; label: string }>
  resources: ProjectTransferResource[]
  projects: Array<{ id: string; name: string }>
}

export interface ProjectTransferPlan {
  source_project_id: string
  target_project_id: string
  resources: Array<ProjectTransferResource & { automatic: boolean }>
  blockers: Array<ProjectTransferResource & { message: string }>
  history_count: number
  confirmation: string
}
