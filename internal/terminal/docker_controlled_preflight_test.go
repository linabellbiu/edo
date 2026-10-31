package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"edo/internal/config"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/terminalwire"
	"edo/internal/testutil"
)

func dockerSDKService(t *testing.T, address string) (*Service, string) {
	t.Helper()
	db := testutil.NewDatabase(t)
	manager, err := secret.New("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := model.DockerEndpoint{ID: uuid.NewString(), Name: "controlled-terminal-test", Host: strings.Replace(address, "http://", "tcp://", 1), IsActive: true, CreatedBy: uuid.NewString(), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&endpoint).Error; err != nil {
		t.Fatal(err)
	}
	return NewService(dockerengine.NewService(db, manager, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: time.Second}), nil, time.Hour), endpoint.ID
}

func TestDockerControlledMissingToolHasNoDirectShellFallback(t *testing.T) {
	id, execID := strings.Repeat("e", 64), strings.Repeat("a", 64)
	var creates, attaches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.55")
			_, _ = io.WriteString(w, "OK")
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/"+id+"/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "State": map[string]any{"Running": true, "Pid": 101, "StartedAt": "2026-10-31T00:00:00Z"}, "HostConfig": map[string]any{"Init": true, "PidMode": ""}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/"+id+"/exec"):
			creates.Add(1)
			var v struct{ Cmd []string }
			_ = json.NewDecoder(r.Body).Decode(&v)
			if len(v.Cmd) != 2 || v.Cmd[0] != terminalwire.HelperPath || v.Cmd[1] != "controller" {
				t.Error("出现直接 Shell 回退")
			}
			_, _ = fmt.Fprintf(w, `{"Id":%q}`, execID)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec/"+execID+"/start"):
			attaches.Add(1)
			http.Error(w, "private helper startup diagnostic", http.StatusInternalServerError)
		default:
			t.Error("出现额外操作")
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	service, endpointID := dockerSDKService(t, server.URL)
	session, err := service.OpenDocker(context.Background(), endpointID, id, "sh", Size{Columns: 100, Rows: 24})
	if session != nil || !errors.Is(err, ErrDockerTerminalUnavailable) || creates.Load() != 1 || attaches.Load() != 1 {
		t.Fatal("缺少工具未按受控条件失败，或出现自动回退", err)
	}
}

// 此处只验证官方 SDK 接缝与阻断顺序，不冒充真实 Linux 进程回收。
func TestDockerControlledRejectsMissingInitAndSharedPIDWithoutExec(t *testing.T) {
	for _, c := range []struct {
		name string
		init bool
		pid  string
	}{{"未启用 init", false, ""}, {"共享宿主 PID", true, "host"}, {"共享其他容器 PID", true, "container:other"}} {
		t.Run(c.name, func(t *testing.T) {
			id := strings.Repeat("e", 64)
			var creates atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/_ping"):
					w.Header().Set("API-Version", "1.55")
					_, _ = io.WriteString(w, "OK")
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/"+id+"/json"):
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "State": map[string]any{"Running": true, "Pid": 101, "StartedAt": "2026-10-31T00:00:00Z"}, "HostConfig": map[string]any{"Init": c.init, "PidMode": c.pid}})
				case strings.HasSuffix(r.URL.Path, "/exec"):
					creates.Add(1)
					_, _ = io.WriteString(w, `{"Id":"unexpected-exec"}`)
				default:
					http.Error(w, "unexpected", http.StatusInternalServerError)
				}
			}))
			defer server.Close()
			service, endpointID := dockerSDKService(t, server.URL)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			session, err := service.OpenDocker(ctx, endpointID, id, "sh", Size{Columns: 100, Rows: 24})
			if session != nil {
				_ = session.Close()
			}
			if err == nil || creates.Load() != 0 {
				t.Fatalf("不满足条件时必须在创建 exec 前失败：err=%v，creates=%d", err, creates.Load())
			}
		})
	}
}
