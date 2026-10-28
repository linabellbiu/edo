package k3s

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

func TestInstallationAuthorizationRejectsStaleRealCasbinAfterRevocation(t *testing.T) {
	for _, mode := range []string{"native-create", "native-join", "k3d-create"} {
		for _, permission := range []string{"cluster.execute", "cluster.update", "cluster.read", "target-action", "kubernetes.node.create"} {
			if mode == "k3d-create" && permission == "kubernetes.node.create" {
				continue
			}
			for _, mutation := range []string{"role-revoked", "explicit-deny"} {
				t.Run(mode+"/"+permission+"/"+mutation, func(t *testing.T) {
					f := newFixture(t)
					scope, _ := database.DepartmentScopeFromContext(f.ctx)
					if err := f.db.Model(&model.User{}).Where("uuid = ?", f.actor.ID).Update("is_superuser", false).Error; err != nil {
						t.Fatal(err)
					}
					f.actor.IsSuperuser = false
					role := model.Role{ID: uuid.NewString(), Name: "fresh-k3s-" + uuid.NewString(), DisplayName: "K3s 安装角色"}
					for _, value := range []any{&role, &model.ProjectMember{ProjectID: scope.ProjectID, UserID: f.actor.ID, RoleID: role.ID}} {
						if err := f.db.Create(value).Error; err != nil {
							t.Fatal(err)
						}
					}
					for _, code := range []string{"cluster.create", "cluster.execute", "cluster.update", "cluster.read", "kubernetes.node.create"} {
						if err := f.db.Create(&model.RolePermission{RoleID: role.ID, Permission: code}).Error; err != nil {
							t.Fatal(err)
						}
					}
					cached, err := access.NewService(f.db)
					if err != nil {
						t.Fatal(err)
					}
					f.service.authorizer = cached
					action, selectedMode := "create", "native"
					if mode == "native-join" {
						action = "join"
					}
					if mode == "k3d-create" {
						selectedMode = "k3d"
					}
					code := permission
					if code == "target-action" {
						code = "cluster.create"
						if action == "join" {
							code = "kubernetes.node.create"
						}
					}
					if _, err := f.service.authorize(f.ctx, f.actor.ID, action); err != nil {
						t.Fatal("授权基线不成立:", err)
					}
					if mutation == "role-revoked" {
						err = f.db.Where("role_id = ? AND permission = ?", role.ID, code).Delete(&model.RolePermission{}).Error
					} else {
						err = f.db.Create(&model.UserPermission{UserID: f.actor.ID, Permission: code, Effect: model.PermissionDeny}).Error
					}
					if err != nil {
						t.Fatal(err)
					}
					allowed, err := cached.HasPermission(f.ctx, &f.actor, code)
					if err != nil || !allowed {
						t.Fatal("必须保留真实 Casbin 旧缓存以复现撤权窗口")
					}
					input := Input{Action: action, Mode: selectedMode}
					var denied error
					if permission == "kubernetes.node.create" && action == "create" {
						denied = f.service.authorizeNativeNodes(f.ctx, &f.actor, input)
					} else {
						_, denied = f.service.authorize(f.ctx, f.actor.ID, action)
					}
					if !errors.Is(denied, ErrPermission) {
						t.Fatalf("当前数据库撤权未生效: %v", denied)
					}
					snapshot := savedInput{Input: input, HostDigests: map[string]string{f.host.ID: hostDigest(f.host)}, AuthVersion: f.actor.AuthVersion}
					if err := f.service.recheckInstallationAuthority(f.ctx, f.actor.ID, action, snapshot); !errors.Is(err, ErrPermission) {
						t.Fatalf("后台重新检查忽略撤权: %v", err)
					}
					if len(f.transport.writes) != 0 {
						t.Fatal("撤权后不应执行任何远端写入")
					}
				})
			}
		}
	}
}
