package httpapi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/kube"
	"github.com/gin-gonic/gin"
)

type kubernetesWatchDeadlineRecorder struct {
	*httptest.ResponseRecorder
	calls       []string
	deadline    time.Time
	fail        string
	invalidTime bool
}

func (w *kubernetesWatchDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	operation := "arm"
	if deadline.IsZero() {
		operation = "clear"
	} else if remaining := time.Until(deadline); remaining <= 4*time.Second || remaining > 5*time.Second {
		w.invalidTime = true
	}
	w.calls = append(w.calls, operation)
	if w.fail == operation {
		return errors.New("测试写入期限失败")
	}
	w.deadline = deadline
	return nil
}

func (w *kubernetesWatchDeadlineRecorder) Write(data []byte) (int, error) {
	w.calls = append(w.calls, "write")
	if w.deadline.IsZero() {
		w.invalidTime = true
	}
	if w.fail == "write" {
		return 0, errors.New("测试写入失败")
	}
	return w.ResponseRecorder.Write(data)
}

func (w *kubernetesWatchDeadlineRecorder) Flush() {
	w.calls = append(w.calls, "legacy_flush")
	w.ResponseRecorder.Flush()
}

func (w *kubernetesWatchDeadlineRecorder) FlushError() error {
	w.calls = append(w.calls, "flush")
	if w.deadline.IsZero() {
		w.invalidTime = true
	}
	if w.fail == "flush" {
		return errors.New("测试刷新失败")
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestKubernetesWatchWriteDeadlineClearedAroundEveryFlush(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	w := &kubernetesWatchDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/watch", nil).WithContext(f.scope())
	c.Request.AddCookie(f.cookie)
	c.Set(currentUserKey, f.user)
	c.Params = gin.Params{{Key: "id", Value: "cluster"}}
	changes := make(chan kube.ResourceChange, 1)
	changes <- kube.ResourceChange{Type: "reset"}
	close(changes)
	watchTestHandler(f, kubernetesWatchStub{changes: changes}).stream(c)
	want := []string{"arm", "write", "flush", "clear", "arm", "write", "flush", "clear", "clear"}
	if !reflect.DeepEqual(w.calls, want) || !w.deadline.IsZero() || w.invalidTime {
		t.Fatalf("写入未独立受五秒期限保护，或刷新及退出后未清除期限：calls=%v invalid_time=%v", w.calls, w.invalidTime)
	}
}

func TestKubernetesWatchWriteFailureStopsAndClearsDeadline(t *testing.T) {
	for _, test := range []struct {
		name string
		want []string
	}{
		{"arm", []string{"arm", "clear"}},
		{"write", []string{"arm", "write", "clear"}},
		{"flush", []string{"arm", "write", "flush", "clear"}},
		{"clear", []string{"arm", "write", "flush", "clear", "clear"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newStreamBoundaryFixture(t)
			w := &kubernetesWatchDeadlineRecorder{ResponseRecorder: httptest.NewRecorder(), fail: test.name}
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/watch", nil).WithContext(f.scope())
			c.Request.AddCookie(f.cookie)
			c.Set(currentUserKey, f.user)
			c.Params = gin.Params{{Key: "id", Value: "cluster"}}
			changes := make(chan kube.ResourceChange, 1)
			changes <- kube.ResourceChange{Type: "reset"}
			close(changes)
			var subscription context.Context
			watchTestHandler(f, kubernetesWatchStub{changes: changes, subscribe: func(ctx context.Context, _ string, _ []string, _ map[string]bool) error {
				subscription = ctx
				return nil
			}}).stream(c)
			if !reflect.DeepEqual(w.calls, test.want) || strings.Contains(w.Body.String(), "event: change") {
				t.Fatalf("写入失败后继续推送，或未尝试清除期限：calls=%v", w.calls)
			}
			if subscription == nil || subscription.Err() != context.Canceled {
				t.Fatal("写入失败未取消订阅")
			}
		})
	}
}

func TestKubernetesWatchIdleRevocationEndsCleanly(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		name := "HTTP1"
		if http2 {
			name = "HTTP2"
		}
		t.Run(name, func(t *testing.T) {
			f := newStreamBoundaryFixture(t)
			changes := make(chan kube.ResourceChange, 1)
			var authorized atomic.Bool
			authorized.Store(true)
			handler := watchTestHandler(f, kubernetesWatchStub{changes: changes})
			handler.authorization = func(*gin.Context, ...string) func() bool { return authorized.Load }
			scope, _ := database.DepartmentScopeFromContext(f.scope())
			router := gin.New()
			router.GET("/watch", func(c *gin.Context) {
				c.Request = c.Request.WithContext(database.WithDepartmentScope(c.Request.Context(), scope))
				c.Request.AddCookie(f.cookie)
				c.Set(currentUserKey, f.user)
				c.Params = gin.Params{{Key: "id", Value: "cluster"}}
				handler.stream(c)
			})
			server := httptest.NewUnstartedServer(router)
			server.EnableHTTP2 = http2
			if http2 {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/watch", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK || (response.ProtoMajor == 2) != http2 {
				t.Fatal("未使用指定协议建立正常资源流", response.StatusCode, response.ProtoMajor)
			}
			reader := bufio.NewReader(response.Body)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal("未收到完整就绪事件", err)
				}
				if line == "\n" {
					break
				}
			}
			// 越过真实五秒写入期限，验证空闲不会保留旧期限或触发 HTTP/2 重置。
			timer := time.NewTimer(5200 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal("测试自身超时，不能当作服务端关闭")
			}
			authorized.Store(false)
			changes <- kube.ResourceChange{Type: "reset"}
			remaining, err := io.ReadAll(reader)
			if err != nil || ctx.Err() != nil {
				t.Fatalf("空闲后撤权未以正常服务端 EOF 结束：err=%v context=%v", err, ctx.Err())
			}
			if len(remaining) != 0 {
				t.Fatal("撤权后仍发送资源变更")
			}
		})
	}
}
