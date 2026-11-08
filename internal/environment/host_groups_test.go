package environment

import (
	"context"
	"errors"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
)

func TestHostGroupHierarchyAndMembership(t *testing.T) {
	s, db := newEnvironmentTestService(t)
	ctx := context.Background()
	host := createEnvironmentTestHost(t, db, "host-tree", "分组主机", "")
	root, err := s.Create(ctx, "admin", Input{Name: "研发主机"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.Create(ctx, "admin", Input{Name: "上海研发", ParentID: root.Environment.ID, HostIDs: []string{host.ID}})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := s.Create(ctx, "admin", Input{Name: "一组", ParentID: child.Environment.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateProfile(ctx, root.Environment.ID, ProfileInput{Name: "研发主机", ParentID: &leaf.Environment.ID}); !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("必须阻止移到下级形成循环: %v", err)
	}
	if _, err = s.UpdateProfile(ctx, child.Environment.ID, ProfileInput{Name: "重命名上海组"}); err != nil {
		t.Fatal(err)
	}
	retained, err := s.Get(ctx, child.Environment.ID)
	if err != nil || retained.Environment.ParentID != root.Environment.ID || len(retained.Hosts) != 1 {
		t.Fatalf("改名损坏层级或成员: %+v %v", retained, err)
	}
	if err = s.Remove(ctx, root.Environment.ID); !errors.Is(err, ErrGroupHasChildren) {
		t.Fatalf("有下级时不能删除: %v", err)
	}
	empty := ""
	if _, err = s.UpdateProfile(ctx, child.Environment.ID, ProfileInput{Name: "重命名上海组", ParentID: &empty}); err != nil {
		t.Fatal(err)
	}
	if err = s.Remove(ctx, root.Environment.ID); err != nil {
		t.Fatal(err)
	}
	retained, err = s.Get(ctx, child.Environment.ID)
	if err != nil || retained.Environment.ParentID != "" || len(retained.Hosts) != 1 {
		t.Fatalf("移动丢失主机: %+v %v", retained, err)
	}
}

func TestHostGroupParentIsolationAndGlobalName(t *testing.T) {
	s, db := newEnvironmentTestService(t)
	ctx := func(project, department string) context.Context {
		return database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: project, DepartmentID: department, UserID: "admin"})
	}
	a, b := ctx("project-a", "department-a"), ctx("project-b", "department-b")
	root, err := s.Create(a, "admin", Input{Name: "唯一主机组"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(b, "admin", Input{Name: "唯一主机组"}); !errors.Is(err, ErrEnvironmentExists) {
		t.Fatalf("跨项目名称也应唯一: %v", err)
	}
	if _, err = s.Create(b, "admin", Input{Name: "外部下级", ParentID: root.Environment.ID}); !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("不得引用其他项目父组: %v", err)
	}
	child, err := s.Create(b, "admin", Input{Name: "另一个组"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateProfile(b, child.Environment.ID, ProfileInput{Name: "唯一主机组"}); !errors.Is(err, ErrEnvironmentExists) {
		t.Fatalf("改名也必须全局唯一: %v", err)
	}
	if _, err = s.Update(b, child.Environment.ID, Input{Name: "另一个组", ParentID: root.Environment.ID}); !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("PUT 不得跨项目移动: %v", err)
	}
	if _, err = s.Get(b, root.Environment.ID); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("不能泄露其他项目主机组: %v", err)
	}
	var count int64
	if err = db.Model(&model.Environment{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("失败事务不能创建额外主机组: %d %v", count, err)
	}
}
