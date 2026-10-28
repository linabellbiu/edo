package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

func identityDelegationRole(t *testing.T, db *gorm.DB, name string, permissions ...string) *model.Role {
	t.Helper()
	role := &model.Role{ID: uuid.NewString(), Name: name, DisplayName: name}
	if err := db.Create(role).Error; err != nil {
		t.Fatal(err)
	}
	for _, permission := range permissions {
		if err := db.Create(&model.RolePermission{RoleID: role.ID, Permission: permission}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return role
}

func identityDelegationActor(t *testing.T, db *gorm.DB, permissions ...string) *model.User {
	t.Helper()
	actor := &model.User{ID: uuid.NewString(), Username: "identity-" + uuid.NewString()[:8], DepartmentID: database.DefaultDepartmentID, IsActive: true}
	if err := db.Create(actor).Error; err != nil {
		t.Fatal(err)
	}
	role := identityDelegationRole(t, db, "role-"+actor.Username, permissions...)
	if err := db.Create(&model.UserRole{RoleID: role.ID, UserID: actor.ID}).Error; err != nil {
		t.Fatal(err)
	}
	return actor
}

func delegatedOAuthInput(name, roleID string) ProviderInput {
	key := "synthetic-oauth-secret"
	return ProviderInput{Type: TypeGenericOAuth, Name: name, DisplayName: name, IsActive: true, AutoCreate: true, DefaultRoleID: roleID,
		ClientID: "synthetic", ClientSecret: &key, AuthorizationURL: "https://example.invalid/authorize", TokenURL: "https://example.invalid/token",
		UserInfoURL: "https://example.invalid/userinfo", RedirectURL: "https://edo.invalid/callback", SubjectField: "sub", UsernameField: "username"}
}

func TestIdentityDefaultRoleDelegationRejectsStrongerRoleAndPreservesLegalProvisioning(t *testing.T) {
	s, db, done := newIdentityTestService(t)
	defer done()
	ctx := context.Background()
	actor := identityDelegationActor(t, db, "identity.create", "identity.update", "audit.read")
	safe := identityDelegationRole(t, db, "identity-safe", "audit.read")
	strong := identityDelegationRole(t, db, "identity-strong", "user.update")
	if _, err := s.CreateAs(ctx, actor, delegatedOAuthInput("unsafe_oauth", strong.ID)); !errors.Is(err, ErrProviderAccessDenied) {
		t.Fatalf("创建登录方式时越权默认角色未被拒绝: %v", err)
	}
	var count int64
	if err := db.Model(&model.IdentityProvider{}).Where("name = ?", "unsafe_oauth").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("越权请求留下配置: count=%d err=%v", count, err)
	}
	input := delegatedOAuthInput("safe_oauth", safe.ID)
	provider, err := s.Create(ctx, input, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.DefaultRoleID, input.DisplayName = strong.ID, "不得保存的修改"
	if _, err = s.UpdateAs(ctx, actor, provider.ID, input); !errors.Is(err, ErrProviderAccessDenied) {
		t.Fatalf("更新登录方式绕过默认角色委派: %v", err)
	}
	var stored model.IdentityProvider
	if err = db.First(&stored, "uuid = ?", provider.ID).Error; err != nil || stored.DefaultRoleID != safe.ID || stored.DisplayName != "safe_oauth" {
		t.Fatalf("失败更新没有回滚: role=%s name=%s err=%v", stored.DefaultRoleID, stored.DisplayName, err)
	}
	user, err := s.resolveUser(ctx, &stored, Profile{Subject: "synthetic-person", Username: "normal-new-user"})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&model.UserRole{}).Where("user_id = ? AND role_id = ?", user.ID, safe.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("合法默认角色不再自动授予: count=%d err=%v", count, err)
	}
	empty := delegatedOAuthInput("no_provisioning", "")
	empty.AutoCreate = false
	if _, err = s.CreateAs(ctx, actor, empty); err != nil {
		t.Fatalf("关闭自动开户且无默认角色不能保存: %v", err)
	}
}

func TestIdentityDefaultRoleRechecksEnableAndAllowsDisablingStrongerProvider(t *testing.T) {
	s, db, done := newIdentityTestService(t)
	defer done()
	ctx := context.Background()
	actor := identityDelegationActor(t, db, "identity.update")
	strong := identityDelegationRole(t, db, "identity-admin-role", "user.update")
	provider, err := s.CreateAs(ctx, identityTestAdmin(t, s), delegatedOAuthInput("privileged_oauth", strong.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetActiveAs(ctx, actor, provider.ID, false); err != nil {
		t.Fatalf("受权管理员不能停用高权限登录方式: %v", err)
	}
	if err = s.SetActiveAs(ctx, actor, provider.ID, true); !errors.Is(err, ErrProviderAccessDenied) {
		t.Fatalf("重新启用绕过默认角色委派: %v", err)
	}
	var stored model.IdentityProvider
	if err = db.First(&stored, "uuid = ?", provider.ID).Error; err != nil || stored.IsActive {
		t.Fatalf("被拒绝启用仍改变配置: active=%v err=%v", stored.IsActive, err)
	}
	if err = s.SetActiveAs(ctx, identityTestAdmin(t, s), provider.ID, true); err != nil {
		t.Fatalf("合法管理员不能重新启用: %v", err)
	}
}

func TestIdentityDelegationUsesCurrentActorAndDenyAndRestrictsRoleChoices(t *testing.T) {
	s, db, done := newIdentityTestService(t)
	defer done()
	ctx := context.Background()
	actor := identityDelegationActor(t, db, "identity.create", "identity.update", "audit.read")
	safe := identityDelegationRole(t, db, "identity-choice-safe", "audit.read")
	strong := identityDelegationRole(t, db, "identity-choice-strong", "user.delete")
	choices, err := s.RoleChoices(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	seenSafe := false
	for _, role := range choices {
		if role.ID == strong.ID {
			t.Fatal("默认角色选择泄漏不可委派的角色")
		}
		seenSafe = seenSafe || role.ID == safe.ID
	}
	if !seenSafe {
		t.Fatal("默认角色选择遗漏合法角色")
	}
	if err = db.Create(&model.UserPermission{UserID: actor.ID, Permission: "audit.read", Effect: model.PermissionDeny}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateAs(ctx, actor, delegatedOAuthInput("denied_role", safe.ID)); !errors.Is(err, ErrProviderAccessDenied) {
		t.Fatalf("个人显式拒绝未参与委派: %v", err)
	}
	if err = db.Model(&model.User{}).Where("uuid = ?", actor.ID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	actor.IsActive, actor.IsSuperuser = true, true
	if _, err = s.CreateAs(ctx, actor, delegatedOAuthInput("stale_actor", "")); !errors.Is(err, ErrProviderAccessDenied) {
		t.Fatalf("旧内存操作者或伪造超级管理员字段生效: %v", err)
	}
	if _, err = s.RoleChoices(ctx, actor); !errors.Is(err, ErrProviderAccessDenied) {
		t.Fatalf("停用后仍可选择默认角色: %v", err)
	}
	if _, err = s.Create(ctx, delegatedOAuthInput("missing_actor", ""), "missing"); !errors.Is(err, ErrProviderAccessDenied) {
		t.Fatalf("公开创建入口接受不存在的操作者: %v", err)
	}
}
