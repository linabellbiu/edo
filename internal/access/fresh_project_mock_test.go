package access

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestCurrentProjectPermissionPreservesDatabaseFailures(t *testing.T) {
	for _, candidate := range []string{"读取用户", "读取项目"} {
		t.Run(candidate, func(t *testing.T) {
			db, mock := testutil.NewSQLMock(t)
			service := &Service{db: db}
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-a", DepartmentID: database.DefaultDepartmentID})
			storageErr := errors.New("模拟数据库连接故障")
			mock.ExpectBegin()
			if candidate == "读取用户" {
				mock.ExpectQuery(`SELECT .* FROM "users"`).WillReturnError(storageErr)
			} else {
				mock.ExpectQuery(`SELECT .* FROM "users"`).WillReturnRows(sqlmock.NewRows([]string{"uuid", "department_id", "is_active"}).AddRow("member", database.DefaultDepartmentID, true))
				mock.ExpectQuery(`SELECT .* FROM "projects"`).WillReturnError(storageErr)
			}
			mock.ExpectRollback()
			allowed, err := service.HasCurrentProjectPermission(ctx, &model.User{ID: "member"}, "workflow.update")
			if allowed || !errors.Is(err, storageErr) {
				t.Fatalf("存储故障被误报为权限不足: allowed=%v err=%v", allowed, err)
			}
		})
	}
}

func TestCurrentProjectPermissionDeniesMissingAndInactiveIdentity(t *testing.T) {
	for _, candidate := range []string{"用户不存在", "用户已停用", "项目不存在", "项目已停用", "项目部门不匹配"} {
		t.Run(candidate, func(t *testing.T) {
			db, mock := testutil.NewSQLMock(t)
			service := &Service{db: db}
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-a", DepartmentID: database.DefaultDepartmentID})
			mock.ExpectBegin()
			users := sqlmock.NewRows([]string{"uuid", "department_id", "is_active"})
			if candidate != "用户不存在" {
				users.AddRow("member", database.DefaultDepartmentID, candidate != "用户已停用")
			}
			mock.ExpectQuery(`SELECT .* FROM "users"`).WillReturnRows(users)
			if candidate != "用户不存在" && candidate != "用户已停用" {
				projects := sqlmock.NewRows([]string{"uuid", "department_id", "is_active"})
				if candidate != "项目不存在" {
					departmentID := database.DefaultDepartmentID
					if candidate == "项目部门不匹配" {
						departmentID = "other-department"
					}
					projects.AddRow("project-a", departmentID, candidate != "项目已停用")
				}
				mock.ExpectQuery(`SELECT .* FROM "projects"`).WillReturnRows(projects)
			}
			mock.ExpectRollback()
			allowed, err := service.HasCurrentProjectPermission(ctx, &model.User{ID: "member"}, "workflow.update")
			if allowed || err != nil {
				t.Fatalf("不可用身份没有稳定映射为拒绝: allowed=%v err=%v", allowed, err)
			}
		})
	}
}
