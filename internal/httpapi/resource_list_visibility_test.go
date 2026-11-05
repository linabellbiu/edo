package httpapi

import (
	"bytes"
	"edo/internal/database"
	environmentmanager "edo/internal/environment"
	hostmanager "edo/internal/host"
	"edo/internal/model"
	"encoding/json"
	"gorm.io/gorm"
	"net/http"
	"testing"
)

func TestResourceListsShowOtherCreatorsAndRespectProjectRole(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, value *gorm.DB) {
		db = value
		deps.Environments = environmentmanager.NewService(db)
		deps.Hosts = hostmanager.NewService(db, nil, nil, nil)
	})
	defer cleanup()
	admin := loginForDepartmentTest(t, router, "admin")
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	repo := model.GitRepository{ID: "visible-repo", Name: "其他成员仓库", CloneURL: "https://example.com/app.git", Provider: model.GitProviderGeneric, AuthType: model.GitAuthNone, ProjectID: project, DepartmentID: database.DefaultDepartmentID, CreatedBy: "another-user"}
	app := model.Application{ID: "visible-app", Name: "其他成员应用", RepositoryID: repo.ID, ProjectID: project, DepartmentID: database.DefaultDepartmentID, CreatedBy: "another-user"}
	build := model.BuildPlan{ID: "visible-build", Name: "其他成员构建方案", Kind: model.BuildPlanDockerfile, DockerfilePath: "Dockerfile", ProjectID: project, DepartmentID: database.DefaultDepartmentID, CreatedBy: "another-user"}
	plan := model.DeploymentPlan{ID: "visible-deploy", Name: "其他成员部署方案", Kind: model.DeploymentPlanComposeStack, ProjectID: project, DepartmentID: database.DefaultDepartmentID, CreatedBy: "another-user"}
	registry := model.ImageRegistry{ID: "visible-registry", Name: "其他成员镜像仓库", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com", ProjectID: project, DepartmentID: database.DefaultDepartmentID, CreatedBy: "another-user"}
	environment := model.Environment{ID: "visible-env", Name: "其他成员环境", ProjectID: project, DepartmentID: database.DefaultDepartmentID}
	host := model.Host{ID: "visible-host", Name: "其他成员主机", Mode: model.HostModeSSH, Address: "192.0.2.50", SSHPort: 22, SSHUsername: "deployer", ProjectID: project, DepartmentID: database.DefaultDepartmentID, CreatedBy: "another-user"}
	for _, row := range []any{&repo, &app, &build, &plan, &registry, &environment, &host} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 禁用资源仍然属于可查看列表，不应与选择器的可用选项规则混用。
	for _, row := range []any{&repo, &app, &build, &plan, &registry, &environment, &host} {
		if err := db.Model(row).Update("is_active", false).Error; err != nil {
			t.Fatal(err)
		}
	}
	foreign := repo
	foreign.RowID = 0
	foreign.ID = "foreign-visible-repo"
	foreign.ProjectID = "other-project"
	if err := db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	permissions := []string{"repository.read", "application.read", "build_plan.read", "deployment_plan.read", "image_registry.read", "deployment.read"}
	role := createRoleForHTTPTest(t, router, admin, "lists-reader", permissions)
	userID := createUserForHTTPTest(t, router, admin, "lists-reader", database.DefaultDepartmentID, []string{role})
	reader := loginForDepartmentTest(t, router, "lists-reader")
	cases := []struct{ path, key, id string }{{"/repositories", "repositories", repo.ID}, {"/applications", "applications", app.ID}, {"/build-plans", "build_plans", build.ID}, {"/deployment-plans", "deployment_plans", plan.ID}, {"/image-registries", "image_registries", registry.ID}, {"/environments", "environments", environment.ID}, {"/hosts", "hosts", host.ID}}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			r := performJSONRequest(t, router, http.MethodGet, "/api/v1"+tc.path, nil, reader)
			if r.Code != 200 {
				t.Fatalf("有对应查看权限应返回列表: %d %s", r.Code, r.Body.String())
			}
			var body map[string][]json.RawMessage
			if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, raw := range body[tc.key] {
				var value struct {
					ID       string `json:"id"`
					IsActive bool   `json:"is_active"`
				}
				if err := json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				if value.ID == tc.id {
					found = true
					if value.IsActive {
						t.Fatal("夹具未保持禁用状态")
					}
				}
			}
			if !found {
				t.Fatalf("遗漏其他成员的可查看资源: %s", r.Body.String())
			}
			if bytes.Contains(r.Body.Bytes(), []byte(foreign.ID)) {
				t.Fatal("列表混入其他项目")
			}
		})
	}
	if err := db.Create(&model.UserPermission{UserID: userID, Permission: "repository.read", Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	if r := performJSONRequest(t, router, http.MethodGet, "/api/v1/repositories", nil, reader); r.Code != http.StatusForbidden {
		t.Fatal("个人拒绝未生效")
	}
	if r := performJSONRequest(t, router, http.MethodGet, "/api/v1/build-plans", nil, reader); r.Code != http.StatusOK {
		t.Fatal("不应因另一类资源无权查看而隐藏构建方案")
	}
	if r := performJSONRequest(t, router, http.MethodPost, "/api/v1/build-plans", map[string]string{}, reader); r.Code != http.StatusForbidden {
		t.Fatal("列表查看权限不能授权写入")
	}
}
