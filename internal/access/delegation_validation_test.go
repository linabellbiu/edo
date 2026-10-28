package access

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/testutil"
)

func TestValidateRoleDelegationRejectsMissingInactiveAndForgedActor(t *testing.T) {
	db := testutil.NewDatabase(t)
	testutil.CreateAdmin(t, db)
	var admin model.User
	if err := db.First(&admin, "username = ?", "admin").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ValidateRoleDelegation(db.WithContext(ctx), &admin, PermissionIdentityCreate, nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRoleDelegation(db, nil, PermissionIdentityCreate, nil); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("缺失操作者仍获授权: %v", err)
	}
	if err := ValidateRoleDelegation(db, &model.User{ID: uuid.NewString(), IsActive: true, IsSuperuser: true}, PermissionIdentityCreate, nil); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("内存超级管理员字段可绕过数据库身份: %v", err)
	}
	if err := db.Model(&admin).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := ValidateRoleDelegation(db, &admin, PermissionIdentityCreate, nil); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("停用的管理员仍可委派: %v", err)
	}
	actor := model.User{ID: uuid.NewString(), Username: "limited-delegator", DepartmentID: database.DefaultDepartmentID, IsActive: true, IsSuperuser: false}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	actor.IsSuperuser = true
	if err := ValidateRoleDelegation(db, &actor, PermissionIdentityCreate, nil); !errors.Is(err, ErrAccessDelegationDenied) {
		t.Fatalf("伪造超级管理员字段可授予平台动作: %v", err)
	}
}
