package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

type approvalViewHTTP struct {
	NodeID            string `json:"node_id"`
	RequiredApprovals int    `json:"required_approvals"`
	ApprovedCount     int    `json:"approved_count"`
	ApprovedByMe      bool   `json:"approved_by_me"`
	CanApprove        bool   `json:"can_approve"`
	Reason            string `json:"reason"`
}

func TestPipelineApprovalViewUsesCurrentReaderAndRealVotesHTTP(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, storage *gorm.DB) { db = storage })
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	roleID := createRoleForHTTPTest(t, router, admin, "approval-view-reviewer", []string{"project.read", "pipeline_run.read", "deployment.review"})
	readerRole := createRoleForHTTPTest(t, router, admin, "approval-view-reader", []string{"project.read", "pipeline_run.read"})
	aID := createUserForHTTPTest(t, router, admin, "approval-view-a", "", []string{roleID})
	bID := createUserForHTTPTest(t, router, admin, "approval-view-b", "", []string{roleID})
	createUserForHTTPTest(t, router, admin, "approval-view-outsider", "", []string{roleID})
	readonlyID := createUserForHTTPTest(t, router, admin, "approval-view-readonly", "", []string{roleID})
	readonlyAssignment := performJSONRequest(t, router, http.MethodPut, "/api/v1/projects/"+database.DefaultProjectID(database.DefaultDepartmentID)+"/members/"+readonlyID, map[string]string{"role_id": readerRole}, admin)
	if readonlyAssignment.Code != http.StatusNoContent {
		t.Fatal("只读项目角色分配失败")
	}
	aCookie := loginForDepartmentTest(t, router, "approval-view-a")
	bCookie := loginForDepartmentTest(t, router, "approval-view-b")
	outsider := loginForDepartmentTest(t, router, "approval-view-outsider")
	readonly := loginForDepartmentTest(t, router, "approval-view-readonly")
	var administrator model.User
	if err := db.First(&administrator, "username = ?", "admin").Error; err != nil {
		t.Fatal(err)
	}
	projectID := database.DefaultProjectID(database.DefaultDepartmentID)
	now := time.Now().UTC()
	repositoryID := createRepositoryForDepartmentTest(t, router, admin, "审批投影测试仓库")
	app := model.Application{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, ProjectID: projectID, RepositoryID: repositoryID, Name: "approval_view_fixture", IsActive: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	gate := "approval-gate"
	snapshot, err := json.Marshal(map[string]any{
		"schema_version": model.WorkflowSchemaVersion, "execution_version": 2, "approval_enabled": true, "authority_id": administrator.ID,
		"source":           model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger},
		"stages":           []model.WorkflowStage{{ID: "release", Tasks: []model.WorkflowNode{{ID: gate, Type: model.WorkflowNodeApproval}, {ID: "other-node", Type: model.WorkflowNodeApproval}}}},
		"release_policies": map[string]model.EnvironmentReleasePolicy{gate: {RequireApproval: true, ReviewerIDs: []string{aID, bID}, MinimumApprovals: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := model.PipelineRun{ID: uuid.NewString(), ProjectID: projectID, DepartmentID: database.DefaultDepartmentID, ApplicationID: app.ID, Trigger: "manual", Ref: "refs/heads/main", CommitSHA: "abcdef", Status: model.PipelineRunAwaitingApproval, Stage: "approval", CurrentNodeID: gate, WorkflowSnapshot: string(snapshot), CreatedBy: administrator.ID, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	get := func(cookie *http.Cookie, query ...string) *approvalViewHTTP {
		t.Helper()
		path := "/api/v1/pipeline-runs/" + run.ID
		if len(query) != 0 {
			path += query[0]
		}
		response := performJSONRequest(t, router, http.MethodGet, path, nil, cookie)
		var payload struct {
			Run struct {
				Approval *approvalViewHTTP `json:"approval"`
			} `json:"pipeline_run"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil {
			t.Fatalf("读取审核进度失败: status=%d body=%s", response.Code, response.Body.String())
		}
		if payload.Run.Approval == nil {
			t.Fatal("运行详情缺少当前读取者的审核进度")
		}
		return payload.Run.Approval
	}
	initial := get(aCookie)
	if initial.NodeID != gate || initial.RequiredApprovals != 2 || initial.ApprovedCount != 0 || initial.ApprovedByMe || !initial.CanApprove {
		t.Fatalf("初始审核投影错误: %+v", initial)
	}
	if value := get(admin); value.CanApprove || value.Reason == "" {
		t.Fatal("超级管理员也不得审核自己的运行")
	}
	var authoritySnapshot map[string]any
	if err := json.Unmarshal(snapshot, &authoritySnapshot); err != nil {
		t.Fatal(err)
	}
	authoritySnapshot["authority_id"] = aID
	modifiedSnapshot, err := json.Marshal(authoritySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("workflow_snapshot", string(modifiedSnapshot)).Error; err != nil {
		t.Fatal(err)
	}
	if value := get(aCookie); value.CanApprove {
		t.Fatal("执行授权人虽不是创建者仍可自审")
	}
	if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("workflow_snapshot", string(snapshot)).Error; err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []*http.Cookie{outsider, readonly} {
		if value := get(cookie); value.CanApprove || value.Reason == "" {
			t.Fatal("名单外或缺少审核权限的读取者获得了审核入口")
		}
	}
	vote := func(runID, nodeID, actorID string) {
		t.Helper()
		if err := db.Create(&model.PipelineRunApproval{ID: uuid.NewString(), PipelineRunID: runID, NodeID: nodeID, RequestedBy: run.CreatedBy, ApprovedBy: actorID, ApprovedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	firstApproval := performJSONRequest(t, router, http.MethodPost, "/api/v1/pipeline-runs/"+run.ID+"/approve", map[string]any{}, aCookie)
	if firstApproval.Code != http.StatusOK {
		t.Fatalf("第一位审核人通过真实接口投票失败: status=%d body=%s", firstApproval.Code, firstApproval.Body.String())
	}
	repeatedApproval := performJSONRequest(t, router, http.MethodPost, "/api/v1/pipeline-runs/"+run.ID+"/approve", map[string]any{}, aCookie)
	if repeatedApproval.Code != http.StatusConflict {
		t.Fatalf("同一审核人能重复投票: status=%d", repeatedApproval.Code)
	}
	otherRun := run
	otherRun.ID, otherRun.RowID = uuid.NewString(), 0
	if err := db.Create(&otherRun).Error; err != nil {
		t.Fatal(err)
	}
	vote(otherRun.ID, gate, bID)
	vote(run.ID, "other-node", bID)
	if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("approved_by", bID).Error; err != nil {
		t.Fatal(err)
	}
	first := get(aCookie)
	if first.ApprovedCount != 1 || !first.ApprovedByMe || first.CanApprove || first.Reason != "已审核，等待其他审核人" {
		t.Fatalf("第一票本人没有禁用重复审核: %+v", first)
	}
	forged := get(aCookie, "?actor_id="+bID+"&approved_count=99&required_approvals=1")
	if !forged.ApprovedByMe || forged.CanApprove || forged.ApprovedCount != 1 || forged.RequiredApprovals != 2 {
		t.Fatal("查询参数能伪造读取者身份或审核票数")
	}
	second := get(bCookie)
	if second.ApprovedCount != 1 || second.ApprovedByMe || !second.CanApprove {
		t.Fatalf("第二位合格审核人被已投票者或汇总字段误判: %+v", second)
	}
	var member model.ProjectMember
	if err := db.First(&member, "project_id = ? AND user_id = ?", projectID, bID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("role_id = ? AND permission = ?", member.RoleID, "deployment.review").Delete(&model.RolePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	if value := get(bCookie); value.CanApprove || value.Reason == "" || value.ApprovedCount != 1 {
		t.Fatal("当前会话没有使用最新数据库权限，或撤权改变了已记录票数")
	}
	if err := db.Create(&model.RolePermission{RoleID: member.RoleID, Permission: "deployment.review"}).Error; err != nil {
		t.Fatal(err)
	}
	if value := get(bCookie); !value.CanApprove {
		t.Fatal("恢复合法权限后当前读取者仍不可审核")
	}
	if err := db.Create(&model.UserPermission{UserID: bID, Permission: "deployment.review", Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	if value := get(bCookie); value.CanApprove {
		t.Fatal("个人显式拒绝未覆盖项目角色")
	}
	if err := db.Where("user_id = ? AND permission = ?", bID, "deployment.review").Delete(&model.UserPermission{}).Error; err != nil {
		t.Fatal(err)
	}
	vote(run.ID, gate, bID)
	if value := get(aCookie); value.ApprovedCount != 2 || value.CanApprove {
		t.Fatalf("达到人数后仍可审核或计数错误: %+v", value)
	}
	if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Updates(map[string]any{"status": model.PipelineRunCanceled}).Error; err != nil {
		t.Fatal(err)
	}
	if value := get(bCookie); value.CanApprove {
		t.Fatal("已停止运行仍可审核")
	}
	otherProject := model.Project{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, Name: "审批隔离空间", IsActive: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&otherProject).Error; err != nil {
		t.Fatal(err)
	}
	assigned := performJSONRequest(t, router, http.MethodPut, "/api/v1/projects/"+otherProject.ID+"/members/"+bID, map[string]string{"role_id": member.RoleID}, admin)
	if assigned.Code != http.StatusNoContent {
		t.Fatalf("添加另一项目的合法成员失败: status=%d", assigned.Code)
	}
	testProjectScopes.Store(bCookie.Value, otherProject.ID)
	defer testProjectScopes.Delete(bCookie.Value)
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/pipeline-runs/"+run.ID, nil, bCookie)
	if response.Code != http.StatusNotFound {
		t.Fatalf("另一合法项目读取了工单审批投影: status=%d", response.Code)
	}
	if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("current_node_id", "source").Error; err != nil {
		t.Fatal(err)
	}
	response = performJSONRequest(t, router, http.MethodGet, "/api/v1/pipeline-runs/"+run.ID, nil, aCookie)
	var nonApproval struct {
		Run struct {
			Approval *approvalViewHTTP `json:"approval"`
		} `json:"pipeline_run"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &nonApproval) != nil || nonApproval.Run.Approval != nil {
		t.Fatal("非审核节点仍返回可审核投影")
	}
	if err := db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("current_node_id", gate).Error; err != nil {
		t.Fatal(err)
	}
	const callback = "test:approval_view_read_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "pipeline_run_approvals" {
			tx.AddError(errors.New("approval-diagnostic-private"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(callback)
	response = performJSONRequest(t, router, http.MethodGet, "/api/v1/pipeline-runs/"+run.ID, nil, aCookie)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "approval-diagnostic-private") || !strings.Contains(response.Body.String(), "暂时无法读取审核进度") {
		t.Fatalf("读取失败未安全拒绝: status=%d body=%s", response.Code, response.Body.String())
	}
}
