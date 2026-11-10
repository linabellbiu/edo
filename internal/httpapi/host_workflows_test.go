package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/pipeline"
	"gorm.io/gorm"
)

func TestHostWorkflowsUsesSavedHostsAndRespectsPermissions(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	repo := createRepositoryForDepartmentTest(t, router, admin, "主机关联仓库")
	host := model.Host{ID: "linked-host", Name: "已移组主机", Mode: model.HostModeSSH, IsActive: true, ProjectID: project, DepartmentID: database.DefaultDepartmentID}
	unselected := host
	unselected.ID = "unselected-host"
	unselected.Name = "原组其他主机"
	app := model.Application{ID: "host-links-app", Name: "关联应用", RepositoryID: repo, ProjectID: project, DepartmentID: database.DefaultDepartmentID}
	target := model.DeploymentTarget{ID: "host-link-target", Name: "库方案目标", Platform: model.DeploymentSSH, HostIDs: []string{host.ID}, ProjectID: project, DepartmentID: database.DefaultDepartmentID}
	plan := model.DeploymentPlan{ID: "host-link-plan", Name: "库方案", DeploymentTargetID: target.ID, ProjectID: project, DepartmentID: database.DefaultDepartmentID}
	inline := model.WorkflowNode{ID: "inline", Name: "内联部署", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{InlineDeploymentPlan: &model.WorkflowDeploymentPlan{DeploymentTarget: &model.DeploymentTarget{Platform: model.DeploymentSSH, EnvironmentID: "old-group", HostIDs: []string{host.ID}}}}}
	library := model.WorkflowNode{ID: "library", Name: "库方案部署", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentPlanID: plan.ID}}
	flow := model.ReleaseWorkflow{ID: "host-linked-flow", Name: "关联流水线", ApplicationID: app.ID, ProjectID: project, DepartmentID: database.DefaultDepartmentID, Stages: []model.WorkflowStage{{ID: "deploy", Tasks: []model.WorkflowNode{inline, library}}}}
	foreign := flow
	foreign.ID = "foreign-flow"
	foreign.Name = "不可泄露的流水线"
	foreign.ProjectID = "foreign-project"
	for _, row := range []any{&host, &unselected, &app, &target, &plan, &flow, &foreign} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&flow).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	// 主机已不属于原组；关联仍由准确 UUID 得到，停用流水线不等于解除关联。
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/hosts/workflows", nil, admin)
	var body struct {
		References map[string][]pipeline.HostWorkflowReference `json:"host_workflows"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatalf("关联接口失败: %d %s", response.Code, response.Body.String())
	}
	refs := body.References[host.ID]
	if len(refs) != 1 || refs[0].ID != flow.ID || refs[0].ApplicationID != app.ID || refs[0].ApplicationName != app.Name || refs[0].IsActive {
		t.Fatalf("未按主机去重或泄露跨项目关联: %+v", refs)
	}
	if len(body.References[unselected.ID]) != 0 {
		t.Fatal("未选主机不应按分组推测关联")
	}
	role := createRoleForHTTPTest(t, router, admin, "host-reader", []string{"deployment.read"})
	createUserForHTTPTest(t, router, admin, "host-reader", database.DefaultDepartmentID, []string{role})
	reader := loginForDepartmentTest(t, router, "host-reader")
	if response = performJSONRequest(t, router, http.MethodGet, "/api/v1/hosts/workflows", nil, reader); response.Code != http.StatusForbidden {
		t.Fatalf("无流水线权限不得读取关联: %d", response.Code)
	}
	role = createRoleForHTTPTest(t, router, admin, "host-flow-reader", []string{"deployment.read", "workflow.read"})
	createUserForHTTPTest(t, router, admin, "host-flow-reader", database.DefaultDepartmentID, []string{role})
	reader = loginForDepartmentTest(t, router, "host-flow-reader")
	if response = performJSONRequest(t, router, http.MethodGet, "/api/v1/hosts/workflows", nil, reader); response.Code != http.StatusOK {
		t.Fatalf("查看者应能读取同项目关联: %d %s", response.Code, response.Body.String())
	}
}
