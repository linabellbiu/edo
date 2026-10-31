package access

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"edo/internal/auth"
	"edo/internal/cache"
	"edo/internal/config"
	"edo/internal/model"
)

func accessInvalidationSubscription(t *testing.T, service *Service, userID string) *redis.PubSub {
	t.Helper()
	server := miniredis.RunT(t)
	client, err := cache.Open(context.Background(), config.Redis{URL: "redis://" + server.Addr() + "/0", KeyPrefix: "edo:", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	bus := auth.NewInvalidations(client, nil)
	service.SetInvalidations(bus)
	sub, err := bus.Subscribe(context.Background(), userID, "synthetic-session")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return sub
}

func accessExpectInvalidation(t *testing.T, sub *redis.PubSub, kind string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	message, err := sub.ReceiveMessage(ctx)
	if err != nil || message.Payload != "stale" || !strings.Contains(message.Channel, ":"+kind+":") {
		t.Fatalf("权限变更缺少失效信号: %v", err)
	}
}

func TestAccessPolicyAndSafeDisableNotifyAfterCommit(t *testing.T) {
	f := newCurrentPermissionFixture(t)
	sub := accessInvalidationSubscription(t, f.service, f.user.ID)
	if err := f.service.SetUserPermissions(context.Background(), f.user.ID, UserPermissionOverrides{Deny: []string{PermissionAuditRead}}); err != nil {
		t.Fatal(err)
	}
	accessExpectInvalidation(t, sub, "policy")
	f.service.watcher = failingRoleWatcher{}
	if err := f.service.SetUserPermissions(context.Background(), f.user.ID, UserPermissionOverrides{Deny: []string{PermissionRoleRead}}); !errors.Is(err, ErrUserAccessSyncDisabled) {
		t.Fatalf("未进入安全停用分支: %v", err)
	}
	accessExpectInvalidation(t, sub, "policy")
	accessExpectInvalidation(t, sub, "user")
	var stored model.User
	if err := f.db.First(&stored, "uuid = ?", f.user.ID).Error; err != nil || stored.IsActive {
		t.Fatal("通知不能替代真实账户停用")
	}
}

func TestProjectStatusAndDeletionNotify(t *testing.T) {
	f := newCurrentPermissionFixture(t)
	admin := *f.user
	admin.IsSuperuser = true
	if err := f.db.Model(&model.User{}).Where("uuid = ?", admin.ID).Update("is_superuser", true).Error; err != nil {
		t.Fatal(err)
	}
	sub := accessInvalidationSubscription(t, f.service, admin.ID)
	for _, active := range []bool{false, true} {
		if err := f.service.UpdateProject(context.Background(), &admin, f.project.ID, f.project.Name, "", active); err != nil {
			t.Fatal(err)
		}
		accessExpectInvalidation(t, sub, "policy")
	}
	if err := f.service.DeleteProject(context.Background(), &admin, f.project.ID); err != nil {
		t.Fatal(err)
	}
	accessExpectInvalidation(t, sub, "policy")
}
