package department

import (
	"context"
	"errors"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestDepartmentMemberCountsAndScope(t *testing.T) {
	assertDepartmentMemberCountsAndScope(t, testutil.NewDatabase(t))
}

func assertDepartmentMemberCountsAndScope(t *testing.T, db *gorm.DB) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("回滚部门测试数据失败: %v", err)
		}
	})
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID, AllDepartments: true})
	s := NewService(tx)
	prefix := "部门查询-" + uuid.NewString()[:8]
	parent, err := s.Create(ctx, Input{Name: prefix + "上级"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.Create(ctx, Input{Name: prefix + "下级", ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := s.Create(ctx, Input{Name: prefix + "空部门"})
	if err != nil {
		t.Fatal(err)
	}
	for i, departmentID := range []string{parent.ID, parent.ID, child.ID} {
		user := model.User{ID: uuid.NewString(), Username: "dept-" + uuid.NewString()[:20], DepartmentID: departmentID, IsActive: true}
		if err := tx.WithContext(ctx).Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			if err := tx.Model(&user).Update("is_active", false).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	expected := map[string]int64{parent.ID: 2, child.ID: 1, empty.ID: 0}
	rows, err := s.List(ctx)
	if err != nil {
		t.Fatalf("读取部门列表失败: %v", err)
	}
	seen := map[string]int{}
	for _, row := range rows {
		count, ok := expected[row.ID]
		if !ok {
			continue
		}
		seen[row.ID]++
		if row.MemberCount != count || row.RowID == 0 || row.IsDefault {
			t.Fatalf("部门成员统计或标识错误: %+v", row)
		}
	}
	for id, count := range expected {
		if seen[id] != 1 {
			t.Fatalf("部门列表遗漏或重复: id=%s count=%d", id, seen[id])
		}
		row, err := s.Get(ctx, id)
		if err != nil || row.MemberCount != count {
			t.Fatalf("部门详情统计错误: id=%s row=%+v err=%v", id, row, err)
		}
	}
	defaultDepartment, err := s.Get(ctx, database.DefaultDepartmentID)
	if err != nil || !defaultDepartment.IsDefault {
		t.Fatalf("默认部门标识错误: %+v %v", defaultDepartment, err)
	}
	scoped := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: parent.ID})
	rows, err = s.List(scoped)
	if err != nil || len(rows) != 1 || rows[0].ID != parent.ID || rows[0].MemberCount != 2 {
		t.Fatalf("部门范围或成员统计错误: %+v %v", rows, err)
	}
	if _, err := s.Get(scoped, child.ID); !errors.Is(err, ErrDepartmentNotFound) {
		t.Fatalf("不应读取其他部门的详情: %v", err)
	}
	if _, err := s.Get(ctx, uuid.NewString()); !errors.Is(err, ErrDepartmentNotFound) {
		t.Fatalf("不存在的部门应返回未找到: %v", err)
	}
	updated, err := s.Update(ctx, child.ID, Input{Name: prefix + "已修改", ParentID: parent.ID})
	if err != nil || updated.MemberCount != 1 || updated.ParentID != parent.ID {
		t.Fatalf("修改后的详情读取失败或部门关联变化: %+v %v", updated, err)
	}
	if err := s.Delete(ctx, empty.ID); err != nil {
		t.Fatalf("删除空部门失败: %v", err)
	}
}

func TestDepartmentHierarchyRejectsCyclesAndDeletesOnlyEmptyBranch(t *testing.T) {
	ctx := context.Background()
	db, err := testutil.OpenDatabase(t)
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
