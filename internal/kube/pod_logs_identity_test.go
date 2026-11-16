package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/database"
	"edo/internal/model"
)

func TestPodLogsRetainSelectedPodIdentity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		initialUID string
		recreated  bool
		wantErr    error
		wantLogs   int32
	}{
		{name: "原 Pod 正常读取", initialUID: "selected", wantLogs: 1},
		{name: "同名新 Pod 拒绝开流", initialUID: "replacement", wantErr: ErrResourceConflict},
		{name: "开流期间重建拒绝输出", initialUID: "selected", recreated: true, wantErr: ErrResourceConflict, wantLogs: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logRequests atomic.Int32
			var uid atomic.Value
			uid.Store(tc.initialUID)
			server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/namespaces/team/pods/web":
					fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web","namespace":"team","uid":%q}}`, uid.Load())
				case "/api/v1/namespaces/team/pods/web/log":
					logRequests.Add(1)
					if r.URL.Query().Get("container") != "app" {
						t.Error("日志请求改变了选中的容器")
					}
					if tc.recreated {
						uid.Store("replacement")
					}
					w.Header().Set("Content-Type", "text/plain")
					_, _ = io.WriteString(w, "selected log\n")
				default:
					t.Errorf("意外请求 %s", r.URL.Path)
					http.NotFound(w, r)
				}
			})
			service := newKubeTestService(t, time.Second)
			id := createKubeTestCluster(t, service, server)
			stream, err := service.PodLogs(context.Background(), id, "team", "web", "app", 100, false, true, "selected")
			if !errors.Is(err, tc.wantErr) || logRequests.Load() != tc.wantLogs {
				t.Fatalf("目标身份检查错误: err=%v, 日志请求数=%d", err, logRequests.Load())
			}
			if tc.wantErr != nil {
				if stream != nil {
					_ = stream.Close()
					t.Fatal("目标已变化但仍返回日志流")
				}
				return
			}
			defer stream.Close()
			body, err := io.ReadAll(stream)
			if err != nil || string(body) != "selected log\n" {
				t.Fatal("原目标日志读取失败", string(body), err)
			}
		})
	}
}

func TestPodLogsIdentityReadFailureDoesNotOpenStream(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
		want   error
	}{
		{http.StatusNotFound, "NotFound", ErrResourceNotFound},
		{http.StatusForbidden, "Forbidden", ErrResourceForbidden},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			var logs atomic.Int32
			server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/log") {
					logs.Add(1)
				}
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":%q,"code":%d}`, tc.reason, tc.status)
			})
			service := newKubeTestService(t, time.Second)
			id := createKubeTestCluster(t, service, server)
			stream, err := service.PodLogs(context.Background(), id, "team", "web", "app", 100, false, true, "selected")
			if !errors.Is(err, tc.want) || stream != nil || logs.Load() != 0 {
				t.Fatal("身份无法核验时仍打开了日志", err, logs.Load())
			}
		})
	}
}

func TestPodLogsIdentityValidationKeepsProjectBoundary(t *testing.T) {
	var calls atomic.Int32
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	for _, ids := range [][]string{{strings.Repeat("a", 129)}, {"one", "two"}} {
		if stream, err := service.PodLogs(context.Background(), id, "team", "web", "app", 100, false, true, ids...); stream != nil || !errors.Is(err, ErrInvalidResource) {
			t.Fatal("无效 Pod 身份未拒绝", err)
		}
	}
	if err := service.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", id).Update("project_id", "project-a").Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-b", DepartmentID: database.DefaultDepartmentID, AllDepartments: true})
	if stream, err := service.PodLogs(ctx, id, "team", "web", "app", 100, false, true, "selected"); stream != nil || !errors.Is(err, ErrClusterNotFound) {
		t.Fatal("跨项目 Pod 日志未拒绝", err)
	}
	if calls.Load() != 0 {
		t.Fatal("身份参数或项目校验失败仍请求了集群", calls.Load())
	}
}
