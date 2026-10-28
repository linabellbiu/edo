export interface PipelineRunApproval {
  node_id: string
  required_approvals: number
  approved_count: number
  approved_by_me: boolean
  can_approve: boolean
  reason: string
}
