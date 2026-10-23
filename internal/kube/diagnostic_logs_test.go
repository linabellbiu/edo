package kube

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestPodLogDiagnosticSnapshotIsBoundedAndSupportsPreviousContainer(t *testing.T) {
	server := resourceTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/test/pods/api-1/log" || query.Get("container") != "api" || query.Get("tailLines") != "100" || query.Get("follow") == "true" || query.Get("previous") != "true" || query.Get("timestamps") != "true" {
			t.Errorf("日志快照参数不正确: %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, "2026-10-24T01:00:00Z exited\n")
	})
	service := newKubeTestService(t, time.Second)
	id := createKubeTestCluster(t, service, server)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := service.PodLogSnapshot(ctx, id, "test", "api-1", "api", 100, true)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil || len(data) == 0 {
		t.Fatalf("日志快照读取失败: %v", err)
	}
}
