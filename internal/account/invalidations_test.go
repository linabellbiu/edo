package account

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"edo/internal/auth"
	"edo/internal/cache"
	"edo/internal/config"
	"edo/internal/model"
)

func accountInvalidationSubscription(t *testing.T, service *Service, userID string) *redis.PubSub {
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

func accountExpectInvalidation(t *testing.T, sub *redis.PubSub, kind string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	message, err := sub.ReceiveMessage(ctx)
	if err != nil || message.Payload != "stale" || !strings.Contains(message.Channel, ":"+kind+":") {
		t.Fatalf("账号变更缺少准确失效信号: %v", err)
	}
}

func TestAccountCommittedIdentityMutationsNotify(t *testing.T) {
	db := openAccountTestDatabase(t, "identity-invalidation")
	service := NewService(db)
	ctx := context.Background()
	user, err := service.CreateUser(ctx, "identity-notify", "测试", "old-password-123")
	if err != nil {
		t.Fatal(err)
	}
	sub := accountInvalidationSubscription(t, service, user.ID)
	department := model.Department{ID: "invalidation-department", Name: "测试部门", IsActive: true}
	if err := db.Create(&department).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SetDepartment(ctx, user.ID, department.ID); err != nil {
		t.Fatal(err)
	}
	accountExpectInvalidation(t, sub, "user")
	if err := service.SetActive(ctx, user.ID, false); err != nil {
		t.Fatal(err)
	}
	accountExpectInvalidation(t, sub, "user")
	if err := service.SetActive(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	accountExpectInvalidation(t, sub, "user")
	if err := service.ChangePassword(ctx, user.ID, "old-password-123", "new-password-456"); err != nil {
		t.Fatal(err)
	}
	accountExpectInvalidation(t, sub, "user")
	if _, err := service.ResetPassword(ctx, user.Username, "135790"); err != nil {
		t.Fatal(err)
	}
	accountExpectInvalidation(t, sub, "user")
}

func TestAccountResetPasswordNotifiesEvenWhenReloadFails(t *testing.T) {
	db := openAccountTestDatabase(t, "identity-reset-reload-failure")
	service := NewService(db)
	user, err := service.CreateUser(context.Background(), "reset-notify", "测试", "old-password-123")
	if err != nil {
		t.Fatal(err)
	}
	sub := accountInvalidationSubscription(t, service, user.ID)
	const callback = "test:reset_reload_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("合成读取失败"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	if _, err := service.ResetPassword(context.Background(), user.Username, "reset-password-456"); err == nil {
		t.Fatal("读取失败应保留原失败结果")
	}
	accountExpectInvalidation(t, sub, "policy")
	_ = db.Callback().Query().Remove(callback)
	stored, err := service.FindByID(context.Background(), user.ID)
	if err != nil || stored.AuthVersion != user.AuthVersion+1 {
		t.Fatal("通知不得掩盖已提交的密码变更")
	}
}
