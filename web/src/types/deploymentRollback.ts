export interface RollbackPreview {
  can_rollback: boolean
  reason: string
  source_deployment_id: string
  source_updated_at: string
  current_image: string
  target_image: string
  kubernetes_uid: string
  cluster_name: string
  runtime_id: string
  namespace: string
  workload_name: string
  container_name: string
}

export interface RollbackRecord {
  id: string
  status: string
  pipeline_run_id?: string
  rollback_source_id?: string
  image: string
  error_message?: string
}

export function rollbackConfirmation(preview: RollbackPreview) {
  return {
    expected_current_image: preview.current_image,
    expected_target_image: preview.target_image,
    expected_kubernetes_uid: preview.kubernetes_uid,
    expected_source_updated_at: preview.source_updated_at,
  }
}

export function rollbackInProgress(record?: RollbackRecord | null) {
  return Boolean(record && ['queued', 'running', 'awaiting_approval'].includes(record.status))
}
