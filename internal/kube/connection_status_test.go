package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/model"
)

func connectionStatusTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func setConnectionStatusServer(t *testing.T, f rbacFixture, server *httptest.Server) {
	t.Helper()
	encrypted, err := f.service.secrets.Encrypt(kubeconfigForServer(server), kubeconfigAAD(f.cluster.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.db.Model(f.cluster).Updates(map[string]any{"api_server": server.URL, "kubeconfig_ciphertext": encrypted}).Error; err != nil {
		t.Fatal(err)
	}
}

func awaitConnectionStatus(t *testing.T, s *Service, ctx context.Context, id string, want string) ConnectionStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		values, err := s.ConnectionStatuses(ctx, []string{id}, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(values) != 1 {
			t.Fatalf("缺少集群状态: %+v", values)
		}
		if values[0].Status == want {
			return values[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("未等到集群状态 %s", want)
	return ConnectionStatus{}
}

func TestConnectionStatusesAreIndependentAndNeverReadNodes(t *testing.T) {
	f := newRBACFixture(t)
	release := make(chan struct{})
	defer close(release)
	var slowRequests, fastRequests atomic.Int32
	slow := connectionStatusTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		slowRequests.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	setConnectionStatusServer(t, f, slow)
	fast := connectionStatusTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		fastRequests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/version" {
			t.Error("连接检查不应读取节点或修改集群")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `{"gitVersion":"v1.35.5+k3s1"}`)
	})
	fastID := uuid.NewString()
	copy := *f.cluster
	copy.RowID, copy.ID, copy.Name = 0, fastID, "fast-cluster"
	copy.KubeconfigCiphertext, _ = f.service.secrets.Encrypt(kubeconfigForServer(fast), kubeconfigAAD(fastID))
	copy.APIServer = fast.URL
	if err := f.service.db.Create(&copy).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	values, err := f.service.ConnectionStatuses(f.adminCtx, nil, false)
	if err != nil || len(values) != 2 {
		t.Fatalf("批量状态失败: %+v %v", values, err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("状态接口等待了远程慢集群")
	}
	value := awaitConnectionStatus(t, f.service, f.adminCtx, fastID, "connected")
	if value.CheckedAt == nil || value.ExpiresAt == nil {
		t.Fatal("缺少真实检查时间")
	}
	for range 4 {
		awaitConnectionStatus(t, f.service, f.adminCtx, fastID, "connected")
	}
	if fastRequests.Load() != 1 {
		t.Fatal("缓存命中仍在重复请求集群")
	}
	if slowRequests.Load() != 1 {
		t.Fatal("慢集群检查没有去重")
	}
	if f.peer.writeCount() != 0 {
		t.Fatal("状态检查不得写入 RBAC")
	}
}

func TestConnectionStatusesClassifyAPIAndNetworkFailures(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		code          int
		state, reason string
	}{
		{"unauthorized", 401, "restricted", "credentials"},
		{"forbidden", 403, "restricted", "permission"},
		{"server", 500, "unreachable", "server_error"},
		{"refused", 0, "unreachable", "network"},
		{"timeout", -1, "unreachable", "timeout"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newRBACFixture(t)
			server := connectionStatusTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if scenario.code == -1 {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(scenario.code)
				_, _ = io.WriteString(w, "private-api.example.com secret-token")
			})
			setConnectionStatusServer(t, f, server)
			if scenario.code == 0 {
				server.Close()
			}
			value := awaitConnectionStatus(t, f.service, f.adminCtx, f.cluster.ID, scenario.state)
			if value.Reason != scenario.reason {
				t.Fatalf("原因错误: %+v", value)
			}
			encoded, _ := json.Marshal(value)
			if strings.Contains(string(encoded), "private-api") || strings.Contains(string(encoded), "secret-token") || strings.Contains(string(encoded), server.URL) {
				t.Fatalf("公开状态泄漏内部错误: %s", encoded)
			}
		})
	}
}

func TestConnectionStatusesRecheckIdentityConfigurationAndActivation(t *testing.T) {
	f := newRBACFixture(t)
	awaitConnectionStatus(t, f.service, f.adminCtx, f.cluster.ID, "connected")
	values, err := f.service.ConnectionStatuses(f.userCtx, []string{f.cluster.ID}, false)
	if err != nil || values[0].Status != "checking" {
		t.Fatalf("不同操作者不能借用管理员缓存: %+v %v", values, err)
	}
	awaitConnectionStatus(t, f.service, f.userCtx, f.cluster.ID, "connected")
	if err := f.service.db.Where("role_id = ?", f.roleID).Delete(&model.RolePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ConnectionStatuses(f.userCtx, []string{f.cluster.ID}, false); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("撤销权限后仍读取旧缓存: %v", err)
	}
	if err := f.service.db.Model(f.cluster).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	values, err = f.service.ConnectionStatuses(f.adminCtx, []string{f.cluster.ID}, false)
	if err != nil || values[0].Status != "unknown" || values[0].Reason != "disabled" {
		t.Fatalf("停用状态仍显示连接正常: %+v %v", values, err)
	}
	if err := f.service.db.Model(f.cluster).Update("is_active", true).Error; err != nil {
		t.Fatal(err)
	}
	server := connectionStatusTestServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"gitVersion":"v1.35.5"}`) })
	setConnectionStatusServer(t, f, server)
	server.Close()
	values, err = f.service.ConnectionStatuses(f.adminCtx, []string{f.cluster.ID}, false)
	if err != nil || values[0].Status != "checking" {
		t.Fatalf("连接修改后命中旧结果: %+v %v", values, err)
	}
	awaitConnectionStatus(t, f.service, f.adminCtx, f.cluster.ID, "unreachable")
	if err := f.service.db.Model(&model.User{}).Where("uuid = ?", f.adminID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ConnectionStatuses(f.adminCtx, nil, false); !errors.Is(err, ErrRBACForbidden) {
		t.Fatalf("禁用管理员仍可以读状态: %v", err)
	}
}

func TestConnectionStatusesPendingRBACNeverWritesOrUsesAdminCache(t *testing.T) {
	f := newRBACFixture(t)
	awaitConnectionStatus(t, f.service, f.adminCtx, f.cluster.ID, "connected")
	state := f.enable(t)
	before := f.peer.writeCount()
	value := awaitConnectionStatus(t, f.service, f.userCtx, f.cluster.ID, "restricted")
	if value.Reason != "rbac_pending" {
		t.Fatalf("授权待同步原因不正确: %+v", value)
	}
	if f.peer.writeCount() != before {
		t.Fatal("只读连接检查同步了原生授权")
	}
	if _, err := f.service.SyncClusterRBAC(f.adminCtx, f.adminID, f.cluster.ID, state.Revision); err != nil {
		t.Fatal(err)
	}
	awaitConnectionStatus(t, f.service, f.userCtx, f.cluster.ID, "connected")
}

func TestConnectionStatusesQueriesStayConstantWithClusterCount(t *testing.T) {
	f := newRBACFixture(t)
	var queries atomic.Int32
	if err := f.service.db.Callback().Query().After("gorm:query").Register("connection_test_count", func(*gorm.DB) { queries.Add(1) }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.service.db.Callback().Query().Remove("connection_test_count") })
	if _, err := f.service.ConnectionStatuses(f.userCtx, nil, false); err != nil {
		t.Fatal(err)
	}
	one := queries.Load()
	for range 8 {
		copy := *f.cluster
		copy.RowID, copy.ID, copy.Name = 0, uuid.NewString(), uuid.NewString()
		copy.KubeconfigCiphertext = "invalid-fixture"
		if err := f.service.db.Create(&copy).Error; err != nil {
			t.Fatal(err)
		}
	}
	queries.Store(0)
	values, err := f.service.ConnectionStatuses(f.userCtx, nil, false)
	if err != nil || len(values) != 9 {
		t.Fatalf("批量读取失败: %v %v", len(values), err)
	}
	if queries.Load() != one {
		t.Fatalf("集群数量增长导致 SQL 增加: single=%d batch=%d", one, queries.Load())
	}
}

func TestConnectionStatusExpirationAndBoundedChecks(t *testing.T) {
	f := newRBACFixture(t)
	awaitConnectionStatus(t, f.service, f.adminCtx, f.cluster.ID, "connected")
	cache := f.service.connections
	cache.mu.Lock()
	past := time.Now().Add(-time.Second)
	for _, entry := range cache.entries {
		entry.value.ExpiresAt = &past
	}
	cache.mu.Unlock()
	values, err := f.service.ConnectionStatuses(f.adminCtx, nil, false)
	if err != nil || values[0].Status != "checking" {
		t.Fatalf("过期状态仍显示连接正常: %+v %v", values, err)
	}
	awaitConnectionStatus(t, f.service, f.adminCtx, f.cluster.ID, "connected")
	f.service.invalidateConnectionStatuses(f.cluster.ID)
	for range maxConnectionChecks {
		cache.slots <- struct{}{}
	}
	values, err = f.service.ConnectionStatuses(f.adminCtx, nil, false)
	if err != nil || values[0].Reason != "queued" {
		t.Fatalf("并发满后没有立即排队返回: %+v %v", values, err)
	}
	for range maxConnectionChecks {
		<-cache.slots
	}
	awaitConnectionStatus(t, f.service, f.adminCtx, f.cluster.ID, "connected")
}

func TestConnectionStatusesQueueStartsBeforeExpiredResults(t *testing.T) {
	f := newRBACFixture(t)
	firstRelease, lastRelease := make(chan struct{}), make(chan struct{})
	defer close(lastRelease)
	var firstOnce sync.Once
	defer firstOnce.Do(func() { close(firstRelease) })
	var starts [8]atomic.Int32
	server := connectionStatusTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var index int
		if _, err := fmt.Sscanf(r.Header.Get("Authorization"), "Bearer connection-%d", &index); err != nil || index < 0 || index >= len(starts) {
			t.Error("检查请求身份错误")
			return
		}
		starts[index].Add(1)
		release := firstRelease
		if index >= maxConnectionChecks {
			release = lastRelease
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `{"gitVersion":"v1.35.5"}`)
	})
	for i := range starts {
		cluster := *f.cluster
		cluster.Name = fmt.Sprintf("connection-%02d", i)
		if i > 0 {
			cluster.ID, cluster.RowID = uuid.NewString(), 0
		}
		config := strings.ReplaceAll(kubeconfigForServer(server), "test-token", fmt.Sprintf("connection-%d", i))
		cluster.APIServer = server.URL
		cluster.KubeconfigCiphertext, _ = f.service.secrets.Encrypt(config, kubeconfigAAD(cluster.ID))
		if err := f.service.db.Save(&cluster).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.ConnectionStatuses(f.adminCtx, nil, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		started := 0
		for i := range maxConnectionChecks {
			started += int(starts[i].Load())
		}
		if started == maxConnectionChecks {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for i := range maxConnectionChecks {
		if starts[i].Load() != 1 {
			t.Fatalf("首批目标未开始: %d", i)
		}
	}
	firstOnce.Do(func() { close(firstRelease) })
	deadline = time.Now().Add(time.Second)
	for len(f.service.connections.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cache := f.service.connections
	cache.mu.Lock()
	past := time.Now().Add(-time.Second)
	for _, entry := range cache.entries {
		if entry.value.CheckedAt != nil {
			entry.value.ExpiresAt = &past
		}
	}
	cache.mu.Unlock()
	// 首批过期后也先进入等待状态；不能因此与尚未执行的尾部重新按名称抢槽。
	for range maxConnectionChecks {
		cache.slots <- struct{}{}
	}
	if _, err := f.service.ConnectionStatuses(f.adminCtx, nil, false); err != nil {
		t.Fatal(err)
	}
	for range maxConnectionChecks {
		<-cache.slots
	}
	if _, err := f.service.ConnectionStatuses(f.adminCtx, nil, false); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		started := 0
		for i := maxConnectionChecks; i < len(starts); i++ {
			started += int(starts[i].Load())
		}
		if started == maxConnectionChecks {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for i := maxConnectionChecks; i < len(starts); i++ {
		if starts[i].Load() != 1 {
			t.Fatalf("TTL 已过期的首批抢占了等待目标: %d", i)
		}
	}
	for i := range maxConnectionChecks {
		if starts[i].Load() != 1 {
			t.Fatalf("优先刷新了已有结果: %d", i)
		}
	}
}

func TestConnectionStatusesDiscardOldConfigurationResult(t *testing.T) {
	f := newRBACFixture(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	old := connectionStatusTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		defer close(done)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `{"gitVersion":"v1.35.5"}`)
	})
	setConnectionStatusServer(t, f, old)
	if _, err := f.service.ConnectionStatuses(f.adminCtx, nil, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("旧连接未开始检查")
	}
	next := connectionStatusTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	setConnectionStatusServer(t, f, next)
	next.Close()
	awaitConnectionStatus(t, f.service, f.adminCtx, f.cluster.ID, "unreachable")
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("旧连接没有结束")
	}
	awaitConnectionStatus(t, f.service, f.adminCtx, f.cluster.ID, "unreachable")
}

func TestConnectionStatusesManualRefreshRechecksWithoutDuplicateRequests(t *testing.T) {
	f := newRBACFixture(t)
	awaitConnectionStatus(t, f.service, f.adminCtx, f.cluster.ID, "connected")
	before := f.peer.requests.Load()
	values, err := f.service.ConnectionStatuses(f.adminCtx, nil, true)
	if err != nil || values[0].Status != "connected" || f.peer.requests.Load() != before {
		t.Fatal("短时间重复刷新没有合并")
	}
	cache := f.service.connections
	cache.mu.Lock()
	prior := time.Now().Add(-3 * time.Second)
	for _, entry := range cache.entries {
		entry.value.CheckedAt = &prior
	}
	cache.mu.Unlock()
	values, err = f.service.ConnectionStatuses(f.adminCtx, nil, true)
	if err != nil || values[0].Status != "checking" {
		t.Fatalf("手动刷新没有重检: %+v %v", values, err)
	}
	awaitConnectionStatus(t, f.service, f.adminCtx, f.cluster.ID, "connected")
	if f.peer.requests.Load() != before+1 {
		t.Fatal("刷新应产生一次真实请求")
	}
}
