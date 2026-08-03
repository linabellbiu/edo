package dockerengine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRemoveContainerForcesRemovalAndAcceptsMissingResource(t *testing.T) {
	for _, test := range []struct {
		name       string
		statusCode int
	}{
		{name: "存在的容器", statusCode: http.StatusNoContent},
		{name: "已经不存在的容器", statusCode: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodDelete || !strings.HasSuffix(request.URL.Path, "/containers/edo-test-app") {
					http.NotFound(response, request)
					return
				}
				calls.Add(1)
				if request.URL.Query().Get("force") != "1" {
					http.Error(response, `{"message":"必须强制删除"}`, http.StatusBadRequest)
					return
				}
				if test.statusCode == http.StatusNotFound {
					response.Header().Set("Content-Type", "application/json")
					response.WriteHeader(http.StatusNotFound)
					_, _ = response.Write([]byte(`{"message":"No such container: edo-test-app"}`))
					return
				}
				response.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)

			service, db := newMonitorTestService(t)
			host := strings.Replace(server.URL, "http://", "tcp://", 1)
			endpoint := createMonitorEndpoint(t, db, "remove-container-endpoint", host, "")
			if err := service.RemoveContainer(context.Background(), endpoint.ID, "edo-test-app"); err != nil {
				t.Fatalf("删除容器实例失败: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("Docker 删除请求次数错误: %d", calls.Load())
			}
		})
	}
}
