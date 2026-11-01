package access

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"edo/internal/testutil"
)

func TestRoleDisplayNamesUsesOneNarrowBatchQuery(t *testing.T) {
	db, mock := testutil.NewSQLMock(t)
	service := &Service{db: db}
	names, err := service.RoleDisplayNames(context.Background(), nil)
	if err != nil || names == nil || len(names) != 0 {
		t.Fatalf("空角色集合应返回空映射且不查询数据库: names=%v err=%v", names, err)
	}
	mock.ExpectQuery(`SELECT "uuid","display_name" FROM "roles" WHERE uuid IN \(\$1,\$2,\$3\)`).
		WithArgs("missing", "role-a", "role-b").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "display_name"}).AddRow("role-a", "审核员").AddRow("role-b", "只读成员"))
	names, err = service.RoleDisplayNames(context.Background(), []string{"role-b", "role-a", "role-a", "missing"})
	if err != nil || !maps.Equal(names, map[string]string{"role-a": "审核员", "role-b": "只读成员"}) {
		t.Fatalf("批量名称查询没有去重或伪造缺失角色名称: names=%v err=%v", names, err)
	}
}

func TestRoleDisplayNamesPreservesStorageFailure(t *testing.T) {
	db, mock := testutil.NewSQLMock(t)
	storageErr := errors.New("模拟角色名称存储故障")
	mock.ExpectQuery(`SELECT "uuid","display_name" FROM "roles" WHERE uuid IN \(\$1\)`).
		WithArgs("role-a").WillReturnError(storageErr)
	names, err := (&Service{db: db}).RoleDisplayNames(context.Background(), []string{"role-a"})
	if names != nil || !errors.Is(err, storageErr) {
		t.Fatalf("角色名称查询故障被吞掉或返回伪造映射: names=%v err=%v", names, err)
	}
}
