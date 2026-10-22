package realtime

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"edo/internal/database"
)

func TestBusRetainsScopeAfterRequestCanceled(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), DB: 2})
	defer client.Close()
	bus := New(nil, client, "edo-test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer bus.Close()
	subscription, err := bus.SubscribeState(context.Background(), "department-a", false)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	ctx, cancel := context.WithCancel(database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: "department-a"}))
	bus.Changed(ctx, "applications", "")
	cancel()
	select {
	case message := <-subscription.Channel():
		if message.Channel != "edo-test:db2:department:department-a" || message.Payload != "applications" {
			t.Fatalf("通知部门或数据库隔离不正确: %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("请求结束后通知丢失")
	}
}

func TestBusEnqueueDoesNotBlockWhenPublisherIsUnavailable(t *testing.T) {
	// 模拟发送协程阻塞：没有消费者时仍必须保持有限缓冲和非阻塞入口。
	bus := &Bus{ctx: context.Background(), queue: make(chan change, 1), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := range 5000 {
			bus.LogsChanged(context.Background(), fmt.Sprint(index))
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("通知入口阻塞了业务调用")
	}
	if len(bus.queue) != 1 || bus.lastOverflow.Load() == 0 {
		t.Fatal("通知队列未保持容量或未记录过载")
	}
}
