package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/pipeline"
	"gorm.io/gorm"
)

func TestWorkflowCatalogListsAllVisibleKindsWithoutApplicationPermission(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	repo := createRepositoryForDepartmentTest(t, router, admin, "目录测试仓库")
	app := model.Application{ID: "catalog-app", Name: "其他成员的应用", RepositoryID: repo, ProjectID: project, DepartmentID: database.DefaultDepartmentID, CreatedBy: "other-user", IsActive: false}
	template := model.ReleaseWorkflowTemplate{ID: "catalog-template", Name: "停用的共用方案", ProjectID: project, DepartmentID: database.DefaultDepartmentID, CreatedBy: "other-user", Stages: []model.WorkflowStage{}, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}}
	flow := model.ReleaseWorkflow{ID: "catalog-flow", Name: "真实应用流水线", ApplicationID: app.ID, ProjectID: project, DepartmentID: database.DefaultDepartmentID, CreatedBy: "other-user", IsActive: true, Source: template.Source, Stages: []model.WorkflowStage{{ID: "build", Name: "构建", Tasks: []model.WorkflowNode{{ID: "build", Type: model.WorkflowNodeBuild}}}, {ID: "deploy", Name: "部署", Tasks: []model.WorkflowNode{{ID: "deploy", Type: model.WorkflowNodeDeploy}}}}}
	foreign := flow
	foreign.ID = "foreign-flow"
	foreign.Name = "不得返回的跨项目流水线"
	foreign.ProjectID = "foreign-project"
	for _, row := range []any{&app, &template, &flow, &foreign} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []any{&app, &template} {
		if err := db.Model(row).Update("is_active", false).Error; err != nil {
			t.Fatal(err)
		}
	}
	role := createRoleForHTTPTest(t, router, admin, "catalog-reader", []string{"workflow.read"})
	createUserForHTTPTest(t, router, admin, "catalog-reader", database.DefaultDepartmentID, []string{role})
	reader := loginForDepartmentTest(t, router, "catalog-reader")
	missingScope := httptest.NewRequest(http.MethodGet, "/api/v1/workflows", nil)
	missingScope.AddCookie(reader)
	missingScopeResult := httptest.NewRecorder()
	router.ServeHTTP(missingScopeResult, missingScope)
	if missingScopeResult.Code != http.StatusBadRequest {
		t.Fatalf("未选项目应拒绝读取列表: %d", missingScopeResult.Code)
	}
	if r := performJSONRequest(t, router, http.MethodGet, "/api/v1/workflows?project_id=foreign-project", nil, reader); r.Code != http.StatusForbidden {
		t.Fatalf("无成员资格的项目不可读取: %d", r.Code)
	}
	r := performJSONRequest(t, router, http.MethodGet, "/api/v1/workflows", nil, reader)
	var body struct {
		Workflows []pipeline.WorkflowCatalogEntry `json:"workflows"`
	}
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &body) != nil {
		t.Fatalf("只拥有流水线查看权限应能读取列表: %d %s", r.Code, r.Body.String())
	}
	found := map[string]pipeline.WorkflowCatalogEntry{}
	for _, row := range body.Workflows {
		found[row.ID] = row
	}
	if found[flow.ID].Kind != "application" || found[flow.ID].ApplicationName != app.Name || len(found[flow.ID].Stages) != 2 || found[template.ID].Kind != "template" {
		t.Fatalf("遗漏其他成员、停用或应用流水线: %+v", found)
	}
	if _, ok := found[foreign.ID]; ok {
		t.Fatal("泄露跨项目流水线")
	}
	if found[template.ID].IsActive {
		t.Fatal("目录未保留停用方案状态")
	}
	if r := performJSONRequest(t, router, http.MethodGet, "/api/v1/applications", nil, reader); r.Code != http.StatusForbidden {
		t.Fatal("没有应用查看权限却返回了应用列表")
	}
	if r := performJSONRequest(t, router, http.MethodGet, "/api/v1/applications/"+app.ID+"/workflows/"+flow.ID, nil, reader); r.Code != http.StatusOK {
		t.Fatalf("流水线详情应独立受查看权限保护: %d", r.Code)
	}
	denied := performJSONRequest(t, router, http.MethodPut, "/api/v1/applications/"+app.ID+"/workflows/"+flow.ID, map[string]any{}, reader)
	if denied.Code != http.StatusForbidden {
		t.Fatal("只读角色可以修改流水线")
	}
	for _, row := range []any{&model.Job{}, &model.PipelineRun{}, &model.DeploymentRecord{}} {
		var count int64
		db.Model(row).Count(&count)
		if count != 0 {
			t.Fatal("读取列表不应触发执行")
		}
	}
	// 个人显式拒绝须立即覆盖项目角色，旧会话不能继续读取。
	var user model.User
	if err := db.First(&user, "username = ?", "catalog-reader").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.UserPermission{UserID: user.ID, Permission: "workflow.read", Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	denied = performJSONRequest(t, router, http.MethodGet, "/api/v1/workflows", nil, reader)
	if denied.Code != http.StatusForbidden || bytes.Contains(denied.Body.Bytes(), []byte(flow.Name)) {
		t.Fatalf("撤权后仍返回资源: %d %s", denied.Code, denied.Body.String())
	}
}
