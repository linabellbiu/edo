package pipeline

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"edo/internal/database"
	"edo/internal/model"
)

func TestCreateDeliveryWorkflowNameCollationGate(t *testing.T) {
	for _, storageFailure := range []bool{false, true} {
		name := "数据库collation冲突"
		if storageFailure {
			name = "名称复核存储失败"
		}
		t.Run(name, func(t *testing.T) {
			s, mock := newPipelineMockService(t)
			// 预加载的独立查询允许GORM按关联名称排序；每条SQL仍核准确参数。
			mock.MatchExpectationsInOrder(false)
			project := database.DefaultProjectID(database.DefaultDepartmentID)
			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE "applications" SET "updated_at"=updated_at WHERE uuid = \$1`).WithArgs("app").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(`SELECT "uuid" FROM "applications".*FOR UPDATE`).WithArgs("app", 1).WillReturnRows(sqlmock.NewRows([]string{"uuid"}).AddRow("app"))
			mock.ExpectQuery(`SELECT .* FROM "projects".*FOR UPDATE`).WithArgs(project, 1).WillReturnRows(sqlmock.NewRows([]string{"uuid", "department_id", "is_active"}).AddRow(project, database.DefaultDepartmentID, true))
			mock.ExpectQuery(`SELECT .* FROM "applications" WHERE uuid =`).WithArgs("app", 1).WillReturnRows(sqlmock.NewRows([]string{"uuid", "name", "repository_id", "is_active", "project_id", "department_id"}).AddRow("app", "test_app", "repo", true, project, database.DefaultDepartmentID))
			mock.ExpectQuery(`SELECT .* FROM "application_repositories"`).WithArgs("app").WillReturnRows(sqlmock.NewRows([]string{"uuid", "application_id", "repository_id"}).AddRow("link", "app", "repo"))
			mock.ExpectQuery(`SELECT .* FROM "application_repository_observations"`).WithArgs("link").WillReturnRows(sqlmock.NewRows([]string{"uuid"}))
			for range 2 {
				mock.ExpectQuery(`SELECT .* FROM "git_repositories"`).WithArgs("repo").WillReturnRows(sqlmock.NewRows([]string{"uuid", "name", "is_active", "project_id", "department_id"}).AddRow("repo", "repo", true, project, database.DefaultDepartmentID))
			}
			mock.ExpectQuery(`SELECT .* FROM "release_workflows" WHERE .*application_id`).WithArgs("app").WillReturnRows(sqlmock.NewRows([]string{"uuid"}))
			mock.ExpectQuery(`SELECT .* FROM "delivery_defaults"`).WithArgs(model.DeploymentPlanDocker, 1).WillReturnRows(sqlmock.NewRows([]string{"uuid"}))
			mock.ExpectQuery(`SELECT .* FROM "build_plans"`).WithArgs("build", true, 1).WillReturnRows(sqlmock.NewRows([]string{"uuid", "kind", "is_active"}).AddRow("build", model.BuildPlanDockerfile, true))
			mock.ExpectQuery(`SELECT "name" FROM "release_workflows".*FOR UPDATE`).WithArgs("app").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("new"))
			marker := errors.New("本例名称SQL读取失败")
			gate := mock.ExpectQuery(`SELECT "uuid" FROM "release_workflows".*application_id.*name.*FOR UPDATE`).WithArgs("app", "New", 1)
			want := ErrWorkflowExists
			if storageFailure {
				gate.WillReturnError(marker)
				want = marker
			} else {
				gate.WillReturnRows(sqlmock.NewRows([]string{"uuid"}).AddRow("existing"))
			}
			mock.ExpectRollback()
			// 仅允许App no-op锁；没有计划、目标、WF持久写或后续运行的任何mock放行。
			result, err := s.CreateDeliveryWorkflow(deliveryTestContext(), "app", "admin", DeliveryWorkflowInput{
				Kind: model.DeploymentPlanDocker, Name: "New", BuildPlanID: "build", WorkflowTemplateID: "builtin", DeploymentPlanID: "builtin",
			})
			if result != nil || !errors.Is(err, want) {
				t.Fatalf("名称SQL必须在创建任何新配置前收口: result=%+v err=%v want=%v", result, err, want)
			}
		})
	}
}
