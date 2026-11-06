import type { ComposeContainerState, ComposeServiceState } from '@/utils/composeRuntime'

export type ContainerControlAction = 'restart' | 'stop' | 'recreate'
export interface ContainerControlPreview {
  service: string; action: ContainerControlAction; container_id: string; digest: string
  run_to_completion: boolean; containers: ComposeContainerState[]
}
export interface DeploymentRuntimeState {
  runtime_id?: string; services?: ComposeServiceState[]; deployment_id: string; kind: string
  resource_id?: string; name: string; namespace?: string; state: string; running: boolean; count?: number
  replicas?: number; ready_replicas?: number; available_replicas?: number
  restart_configured?: boolean; stop_configured?: boolean; output?: string
}
