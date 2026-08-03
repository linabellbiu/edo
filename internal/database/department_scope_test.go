package database

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/model"
)

func TestDepartmentScopeFiltersAndAssignsResources(t *testing.T) {
	db, err := Open(context.Background(), config.Database{Driver: "sqlite", DSN: ":memory:", MaxOpenConns: 1, MaxIdleConns: 1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	for _, item := range []model.Department{
		{ID: "department-a", Name: "研发部", IsActive: true, CreatedAt: now, UpdatedAt: now},
		{ID: "department-b", Name: "运维部", IsActive: true, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}

	ctxA := WithDepartmentScope(context.Background(), DepartmentScope{UserID: "user-a", DepartmentID: "department-a"})
	ctxB := WithDepartmentScope(context.Background(), DepartmentScope{UserID: "user-b", DepartmentID: "department-b"})
	create := func(ctx context.Context, id, name string) {
		item := model.Environment{
			ID: id, Name: name, Level: model.EnvironmentGlobal, IsActive: true,
			DepartmentID: "attempted-cross-department", CreatedBy: "actor", CreatedAt: now, UpdatedAt: now,
		}
		if err := db.WithContext(ctx).Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	create(ctxA, "environment-a", "研发环境")
	create(ctxB, "environment-b", "运维环境")

	var departmentA []model.Environment
	if err := db.WithContext(ctxA).Order("id ASC").Find(&departmentA).Error; err != nil {
		t.Fatal(err)
	}
	if len(departmentA) != 1 || departmentA[0].ID != "environment-a" || departmentA[0].DepartmentID != "department-a" {
		t.Fatalf("部门 A 查询或创建归属错误: %#v", departmentA)
	}
	var hidden model.Environment
	if err := db.WithContext(ctxA).First(&hidden, "id = ?", "environment-b").Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("跨部门详情应按不存在处理，得到 %v", err)
	}
	result := db.WithContext(ctxA).Model(&model.Environment{}).
		Where("id = ?", "environment-b").Update("description", "越权修改")
	if result.Error != nil || result.RowsAffected != 0 {
		t.Fatalf("跨部门修改未被拦截: rows=%d err=%v", result.RowsAffected, result.Error)
	}

	allCtx := WithDepartmentScope(context.Background(), DepartmentScope{UserID: "admin", AllDepartments: true})
	var all []model.Environment
	if err := db.WithContext(allCtx).Order("id ASC").Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("超级管理员应看到全部部门资源，得到 %d", len(all))
	}

	adminCtx := WithDepartmentScope(context.Background(), DepartmentScope{
		UserID: "admin", DepartmentID: "department-a", AllDepartments: true,
	})
	adminEnvironment := model.Environment{
		ID: "environment-admin", Name: "管理员创建的环境", Level: model.EnvironmentGlobal,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.WithContext(adminCtx).Create(&adminEnvironment).Error; err != nil {
		t.Fatal(err)
	}
	if adminEnvironment.DepartmentID != "department-a" {
		t.Fatalf("超级管理员创建资源仍应归属自己的部门，得到 %q", adminEnvironment.DepartmentID)
	}
	explicitDepartment := model.Environment{
		ID: "environment-admin-explicit", DepartmentID: "department-b", Name: "管理员指定部门的环境",
		Level: model.EnvironmentGlobal, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.WithContext(adminCtx).Create(&explicitDepartment).Error; err != nil {
		t.Fatal(err)
	}
	if explicitDepartment.DepartmentID != "department-b" {
		t.Fatalf("超级管理员显式指定的资源部门不应被覆盖，得到 %q", explicitDepartment.DepartmentID)
	}
}

func TestDepartmentScopeFailsClosedWithoutDepartment(t *testing.T) {
	db, err := Open(context.Background(), config.Database{Driver: "sqlite", DSN: ":memory:", MaxOpenConns: 1, MaxIdleConns: 1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err := db.AutoMigrate(&model.Environment{}); err != nil {
		t.Fatal(err)
	}
	ctx := WithDepartmentScope(context.Background(), DepartmentScope{UserID: "user-without-department"})
	var items []model.Environment
	if err := db.WithContext(ctx).Find(&items).Error; !errors.Is(err, ErrDepartmentScopeRequired) {
		t.Fatalf("缺少部门必须拒绝查询，得到 %v", err)
	}
}
