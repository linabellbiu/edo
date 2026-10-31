package department

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	"edo/internal/auth"
	"edo/internal/cache"
	"edo/internal/config"
	"edo/internal/testutil"
)

func TestDepartmentDeleteNotifiesOnlyAfterCommit(t *testing.T) {
	service := NewService(testutil.NewDatabase(t))
	ctx := context.Background()
	item, err := service.Create(ctx, Input{Name: "身份通知空部门"})
	if err != nil {
		t.Fatal(err)
	}
	server := miniredis.RunT(t)
	client, err := cache.Open(ctx, config.Redis{URL: "redis://" + server.Addr() + "/0", KeyPrefix: "edo:", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	bus := auth.NewInvalidations(client, nil)
	service.SetInvalidations(bus)
	sub, err := bus.Subscribe(ctx, "user", "synthetic-session")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := service.Delete(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	message, err := sub.ReceiveMessage(readCtx)
	if err != nil || message.Payload != "stale" {
		t.Fatal("已提交的部门删除未通知在线身份")
	}
	if _, err := service.Get(ctx, item.ID); err == nil {
		t.Fatal("通知不能代替真实删除")
	}
}
