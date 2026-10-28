package kube

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/model"
)

type authorizationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f authorizationRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newAuthorizationTransportTest(t *testing.T, resolver func(context.Context, *model.KubernetesCluster, bool) (*clusterAuthorization, error)) (*Service, *clusterQueries) {
	t.Helper()
	server := newKubeVersionServer(t)
	service := newKubeTestService(t, 5*time.Second)
	service.authorizationResolver = resolver
	clusterID := createKubeTestCluster(t, service, server)
	query, err := service.clusterQueries(context.Background(), clusterID)
	if err != nil {
		t.Fatalf("初始化授权传输测试连接失败: %v", err)
	}
	t.Cleanup(query.stop)
	return service, query
}

func assertAuthorizationRequestReleased(t *testing.T, query *clusterQueries) {
	t.Helper()
	query.mu.Lock()
	defer query.mu.Unlock()
	if query.activeRequests != 0 {
		t.Fatalf("授权传输请求未释放: %d", query.activeRequests)
	}
}

func TestAuthorizationRoundTripperPreservesCallerDeadline(t *testing.T) {
	service, query := newAuthorizationTransportTest(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	called := false
	transport := &authorizationRoundTripper{
		service: service,
		query:   query,
		next: authorizationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			called = true
			<-request.Context().Done()
			// 固定下游取消错误，验证上游超时原因不会被传输层覆盖。
			return nil, context.Canceled
		}),
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://kubernetes.example.com/version", nil)
	if err != nil {
		t.Fatalf("创建授权传输测试请求失败: %v", err)
	}
	_, err = transport.RoundTrip(request)
	if !called || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("下游未在调用方实际超时后返回: called=%t, context=%v", called, ctx.Err())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("授权传输丢失调用方超时原因: %v", err)
	}
	assertAuthorizationRequestReleased(t, query)
}

func TestAuthorizationRoundTripperPreservesCallerCancellation(t *testing.T) {
	service, query := newAuthorizationTransportTest(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	transport := &authorizationRoundTripper{
		service: service,
		query:   query,
		next: authorizationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			called = true
			cancel()
			select {
			case <-request.Context().Done():
				return nil, request.Context().Err()
			case <-time.After(time.Second):
				t.Fatal("调用方取消未及时传递到下游请求")
				return nil, nil
			}
		}),
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://kubernetes.example.com/version", nil)
	if err != nil {
		t.Fatalf("创建授权传输测试请求失败: %v", err)
	}
	_, err = transport.RoundTrip(request)
	if !called || !errors.Is(err, context.Canceled) {
		t.Fatalf("授权传输丢失调用方主动取消: called=%t, error=%v", called, err)
	}
	assertAuthorizationRequestReleased(t, query)
}

func TestAuthorizationRoundTripperCancelsRevokedQuery(t *testing.T) {
	service, query := newAuthorizationTransportTest(t, nil)
	called := false
	transport := &authorizationRoundTripper{
		service: service,
		query:   query,
		next: authorizationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			called = true
			query.cancel()
			select {
			case <-request.Context().Done():
				return nil, request.Context().Err()
			case <-time.After(time.Second):
				t.Fatal("权限连接取消未及时停止下游请求")
				return nil, nil
			}
		}),
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://kubernetes.example.com/version", nil)
	if err != nil {
		t.Fatalf("创建授权传输测试请求失败: %v", err)
	}
	_, err = transport.RoundTrip(request)
	if !called || !errors.Is(err, context.Canceled) || request.Context().Err() != nil {
		t.Fatalf("权限连接取消被忽略或错误取消调用方: called=%t, error=%v, context=%v", called, err, request.Context().Err())
	}
	assertAuthorizationRequestReleased(t, query)
}

func TestAuthorizationRoundTripperRejectsPermissionChanges(t *testing.T) {
	var denied atomic.Bool
	service, query := newAuthorizationTransportTest(t, func(context.Context, *model.KubernetesCluster, bool) (*clusterAuthorization, error) {
		if denied.Load() {
			return nil, ErrRBACForbidden
		}
		return &clusterAuthorization{}, nil
	})
	denied.Store(true)
	called := false
	transport := &authorizationRoundTripper{
		service: service,
		query:   query,
		next: authorizationRoundTripFunc(func(*http.Request) (*http.Response, error) {
			called = true
			return nil, nil
		}),
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://kubernetes.example.com/version", nil)
	if err != nil {
		t.Fatalf("创建授权传输测试请求失败: %v", err)
	}
	_, err = transport.RoundTrip(request)
	if called || !errors.Is(err, ErrRBACForbidden) || !errors.Is(query.ctx.Err(), context.Canceled) {
		t.Fatalf("权限变更后错误放行请求: downstream=%t, error=%v, query=%v", called, err, query.ctx.Err())
	}
	assertAuthorizationRequestReleased(t, query)
}
