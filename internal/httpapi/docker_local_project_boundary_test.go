package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"edo/internal/access"
	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
	terminalservice "edo/internal/terminal"
	"gorm.io/gorm"
)

func TestDockerLocalProjectBoundaryRejectsBeforeDaemonAndPreservesOwner(t *testing.T) {
	// 只连接本测试的空 Unix HTTP 服务，不读取开发机 Docker 或真实容器。
	directory, err := os.MkdirTemp("/tmp", "edo-rbac-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var daemonCalls, logCalls, execCalls atomic.Int32
	var failLogs atomic.Bool
	fakeDaemon := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		daemonCalls.Add(1)
		w.Header().Set("API-Version", "1.53")
		w.Header().Set("OSType", "linux")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = io.WriteString(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": strings.Repeat("a", 64), "Names": []string{"/owner-fixture"}, "Image": "fixture:1", "State": "running"}})
		case strings.HasSuffix(r.URL.Path, "/logs"):
			logCalls.Add(1)
			if failLogs.Load() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"message":"fixture-daemon-transport-failure"}`)
				return
			}
			_, _ = io.WriteString(w, "synthetic-fixture-output")
		case strings.Contains(r.URL.Path, "/exec"):
			execCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": strings.Repeat("a", 64), "Config": map[string]any{"Tty": true}, "State": map[string]any{"Running": true}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	go func() { _ = fakeDaemon.Serve(listener) }()
	t.Cleanup(func() { _ = fakeDaemon.Close() })
	var deps Dependencies
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, valueDB *gorm.DB) {
		value.Docker = dockerengine.NewService(valueDB, nil, config.Runtime{
			DockerBuilderHost: "unix://" + socket, ConnectTimeout: time.Second, RequestTimeout: time.Second,
		})
		value.Terminal = terminalservice.NewService(value.Docker, nil, time.Minute)
		deps, db = *value, valueDB
	})
	t.Cleanup(cleanup)
	admin, err := deps.Accounts.FindByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	user, err := deps.Accounts.CreateUser(context.Background(), "docker-local-boundary", "本地运行时项目边界", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	project, err := deps.Access.CreateProject(context.Background(), admin, "没有本地主机的隔离项目", "", user.DepartmentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.Access.SetProjectMember(context.Background(), admin, project.ID, user.ID, access.ProjectOwnerRoleID); err != nil {
		t.Fatal(err)
	}
	cookie := loginForDepartmentTest(t, router, user.Username)
	containerID := strings.Repeat("a", 64)
	request := func(method, path, projectID, body, protocol string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-EDO-Project-ID", projectID)
		if protocol != "" {
			r.Header.Set("Sec-WebSocket-Protocol", protocol)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		return response
	}
	for _, test := range []struct {
		name, method, path, body, protocol string
		status                             int
	}{
		{"目标列表", "GET", "/docker/endpoints", "", "", http.StatusOK},
		{"容器列表", "GET", "/docker/endpoints/edo-local-docker/containers?all=true", "", "", http.StatusNotFound},
		{"运行时检测", "POST", "/docker/endpoints/edo-local-docker/ping", "{}", "", http.StatusNotFound},
		{"容器日志", "GET", "/docker/endpoints/edo-local-docker/containers/" + containerID + "/logs/ws?tail=10&follow=false", "", containerLogSubprotocol, http.StatusNotFound},
		{"容器文件", "GET", "/terminals/docker/edo-local-docker/containers/" + containerID + "/files?path=/", "", "", http.StatusNotFound},
		{"内置连接改名", "PATCH", "/docker/endpoints/edo-local-docker/name", `{"name":"不允许其他项目改名"}`, "", http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			daemonCalls.Store(0)
			logCalls.Store(0)
			execCalls.Store(0)
			response := request(test.method, test.path, project.ID, test.body, test.protocol)
			if response.Code != test.status {
				t.Errorf("本地运行时没有按目标项目边界拒绝: status=%d want=%d", response.Code, test.status)
			}
			if test.name == "目标列表" && strings.Contains(response.Body.String(), dockerengine.LocalEndpointID) {
				t.Fatal("未授权项目列出了内置运行时")
			}
			if daemonCalls.Load() != 0 || logCalls.Load() != 0 || execCalls.Load() != 0 {
				t.Fatal("拒绝前已访问 Docker、日志或 Exec")
			}
		})
	}
	scope := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: user.ID, DepartmentID: project.DepartmentID, ProjectID: project.ID})
	if _, err := deps.Terminal.OpenDocker(scope, dockerengine.LocalEndpointID, containerID, "sh", terminalservice.Size{Columns: 80, Rows: 24}); !errors.Is(err, dockerengine.ErrEndpointNotFound) {
		t.Fatalf("直接容器终端未在 Exec 前拒绝: %v", err)
	}
	if daemonCalls.Load() != 0 || execCalls.Load() != 0 {
		t.Fatal("未授权终端接触了 Docker 传输")
	}
	ownerProject := database.DefaultProjectID(user.DepartmentID)
	if err := deps.Access.SetProjectMember(context.Background(), admin, ownerProject, user.ID, access.ProjectOwnerRoleID); err != nil {
		t.Fatal(err)
	}
	positive := request(http.MethodGet, "/docker/endpoints/edo-local-docker/containers?all=true", ownerProject, "", "")
	var payload struct {
		Containers []json.RawMessage `json:"containers"`
	}
	if err := json.Unmarshal(positive.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if positive.Code != http.StatusOK || len(payload.Containers) != 1 || daemonCalls.Load() == 0 {
		t.Fatalf("原本拥有内置主机的项目无法读取自己的运行时: status=%d count=%d", positive.Code, len(payload.Containers))
	}
	failLogs.Store(true)
	failedLogs := request(http.MethodGet, "/docker/endpoints/edo-local-docker/containers/"+containerID+"/logs/ws?tail=10&follow=false", ownerProject, "", containerLogSubprotocol)
	if failedLogs.Code != http.StatusBadGateway || !strings.Contains(failedLogs.Body.String(), `"code":"container_logs_unavailable"`) || logCalls.Load() != 1 || execCalls.Load() != 0 {
		t.Fatalf("已授权项目的真实 Docker 日志故障应保持网关错误: status=%d calls=%d", failedLogs.Code, logCalls.Load())
	}
	if strings.Contains(failedLogs.Body.String(), "fixture-daemon-transport-failure") {
		t.Fatal("Docker 诊断信息泄露到客户端")
	}
	var untouched model.DockerEndpoint
	if err := db.First(&untouched, "uuid = ?", dockerengine.LocalEndpointID).Error; err == nil && untouched.Name == "不允许其他项目改名" {
		t.Fatal("未授权项目改写了其他项目的内置连接名称")
	}
}

func TestContainerLogsSeparatesProjectBoundaryFromDaemonFailure(t *testing.T) {
	for _, test := range []struct {
		name, code string
		err        error
		status     int
	}{
		{"项目资源不可见", "endpoint_not_found", fmt.Errorf("检查日志目标: %w", dockerengine.ErrEndpointNotFound), http.StatusNotFound},
		{"Docker 连接故障", "container_logs_unavailable", errors.New("fixture-daemon-transport-failure"), http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			var diagnostic strings.Builder
			handler := containerLogHandler{
				docker: failingBoundaryContainerLogService{err: test.err},
				logger: slog.New(slog.NewTextHandler(&diagnostic, nil)),
			}
			router := gin.New()
			router.GET("/docker/endpoints/:id/containers/:container_id/logs/ws", handler.stream)
			request := httptest.NewRequest(http.MethodGet, "/docker/endpoints/fixture-endpoint/containers/fixture-container/logs/ws?tail=10&follow=false", nil)
			request.Header.Set("Sec-WebSocket-Protocol", containerLogSubprotocol)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("日志错误语义不正确: status=%d code=%s", response.Code, test.code)
			}
			if strings.Contains(response.Body.String(), "fixture-daemon-transport-failure") {
				t.Fatal("内部 Docker 错误泄露到对外响应")
			}
			if !strings.Contains(diagnostic.String(), "docker_container_logs_open") || !strings.Contains(diagnostic.String(), "fixture-endpoint") || !strings.Contains(diagnostic.String(), test.err.Error()) {
				t.Fatal("日志失败缺少操作、目标或原始诊断")
			}
		})
	}
}

type failingBoundaryContainerLogService struct {
	err error
}

func (service failingBoundaryContainerLogService) ContainerLogs(context.Context, string, string, dockerengine.ContainerLogOptions) (*dockerengine.ContainerLogStream, error) {
	return nil, service.err
}
