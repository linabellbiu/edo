package department

import (
	"context"
	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func TestDepartmentHierarchyRejectsCyclesAndDeletesOnlyEmptyBranch(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "organization.db"), MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(db)
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := NewService(db)
	parent, err := s.Create(ctx, Input{Name: "研发部"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.Create(ctx, Input{Name: "交付组", ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(ctx, parent.ID, Input{Name: parent.Name, ParentID: child.ID}); !errors.Is(err, ErrInvalidDepartment) {
		t.Fatal("不得形成部门环", err)
	}
	if err = s.Delete(ctx, parent.ID); !errors.Is(err, ErrDepartmentInUse) {
		t.Fatal("不得删除有下级的部门", err)
	}
	if err = s.Delete(ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&model.Project{}).Where("department_id = ?", child.ID).Count(&count)
	if count != 0 {
		t.Fatal("删除空部门残留可访问项目")
	}
	if err = s.Delete(ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
}
