package pipeline

import (
	"context"
	"errors"
	"slices"

	"edo/internal/access"
	"edo/internal/cicd"
	"edo/internal/database"
	"edo/internal/model"
)

var ErrRunApprovalUnavailable = errors.New("暂时无法读取审核进度，请稍后重试")

// RunView 只为当前读取者投影操作能力，不进入运行快照或持久化模型。
type RunView struct {
	*model.PipelineRun
	Approval *RunApprovalView `json:"approval,omitempty"`
}

type RunApprovalView struct {
	NodeID            string `json:"node_id"`
	RequiredApprovals int    `json:"required_approvals"`
	ApprovedCount     int    `json:"approved_count"`
	ApprovedByMe      bool   `json:"approved_by_me"`
	CanApprove        bool   `json:"can_approve"`
	Reason            string `json:"reason"`
}

func (s *Service) FindRunView(ctx context.Context, id string) (*RunView, error) {
	run, err := s.FindRun(ctx, id)
	if err != nil {
		return nil, err
	}
	view := &RunView{PipelineRun: run}
	if run.WorkflowSnapshot == "" {
		return view, nil
	}
	scope, scoped := database.DepartmentScopeFromContext(ctx)
	failure := func(cause error) (*RunView, error) {
		s.logger.Error("读取流水线审核进度失败", "operation", "pipeline_run_approval_view", "pipeline_run_id", run.ID, "node_id", run.CurrentNodeID, "project_id", scope.ProjectID, "reader_id", scope.UserID, "err", cause)
		return nil, ErrRunApprovalUnavailable
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil {
		return failure(err)
	}
	node, found := workflowFindNode(snapshot.Source, snapshot.Stages, run.CurrentNodeID)
	if !found || node.Type != model.WorkflowNodeApproval || !snapshot.ApprovalEnabled {
		return view, nil
	}
	approval := &RunApprovalView{NodeID: node.ID, RequiredApprovals: 1}
	view.Approval = approval
	if policy, protected := snapshot.ReleasePolicies[node.ID]; protected && policy.MinimumApprovals > 1 {
		approval.RequiredApprovals = policy.MinimumApprovals
	}
	if !scoped || scope.UserID == "" || scope.ProjectID != run.ProjectID || scope.DepartmentID != run.DepartmentID {
		approval.Reason = "当前项目范围无效"
		return view, nil
	}
	var voters []string
	if err := s.db.WithContext(ctx).Model(&model.PipelineRunApproval{}).
		Where("pipeline_run_id = ? AND node_id = ? AND approved_by <> ?", run.ID, node.ID, "").
		Distinct("approved_by").Pluck("approved_by", &voters).Error; err != nil {
		return failure(err)
	}
	approval.ApprovedCount, approval.ApprovedByMe = len(voters), slices.Contains(voters, scope.UserID)
	if run.Status != model.PipelineRunAwaitingApproval {
		approval.Reason = "当前任务不在待审核状态"
		return view, nil
	}
	if approval.ApprovedCount >= approval.RequiredApprovals {
		approval.Reason = "审核人数已满足"
		return view, nil
	}
	if scope.UserID == run.CreatedBy || scope.UserID == snapshot.AuthorityID {
		approval.Reason = ErrWorkflowSelfApproval.Error()
		return view, nil
	}
	authorization, err := access.FreshProjectPermissions(s.db, ctx, scope.UserID, run.ProjectID)
	if errors.Is(err, access.ErrAccessDelegationDenied) {
		approval.Reason = "当前账户没有该项目的审核权限"
		return view, nil
	}
	if err != nil {
		return failure(err)
	}
	if !authorization.Permissions["deployment.review"] {
		approval.Reason = "当前账户没有该项目的审核权限"
		return view, nil
	}
	if policy, protected := snapshot.ReleasePolicies[node.ID]; protected && !cicd.PolicyReviewer(policy, scope.UserID) {
		approval.Reason = "当前账户不在本次运行的审核人名单中"
		return view, nil
	}
	if approval.ApprovedByMe {
		approval.Reason = "已审核，等待其他审核人"
		return view, nil
	}
	approval.CanApprove = true
	return view, nil
}
