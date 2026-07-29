package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	hostmanager "zrt/internal/host"
	"zrt/internal/model"
)

func TestHostResponseKeepsDockerSudoCapability(t *testing.T) {
	response := toHostResponse(hostmanager.Detail{
		Host:           model.Host{ID: "host-1", Name: "构建主机"},
		EnvironmentIDs: []string{"environment-a", "environment-b"},
		Capabilities: []model.HostCapability{{
			HostID: "host-1", Kind: model.HostCapabilityDocker, RuntimeID: "docker-1",
			Status: model.HostCapabilityReady, UseSudo: true,
		}},
	})
	if len(response.Capabilities) != 1 || !response.Capabilities[0].UseSudo {
		t.Fatalf("主机响应丢失 Docker sudo 能力: %+v", response.Capabilities)
	}
	if len(response.EnvironmentIDs) != 2 || response.EnvironmentID != "" {
		t.Fatalf("主机多环境响应错误: %+v", response)
	}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"use_sudo":true`) {
		t.Fatalf("主机 JSON 响应没有返回 use_sudo: %s", payload)
	}
}

func TestHostPingRejectsUnknownCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/hosts/host-1/ping?capability=shell", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "host-1"}}
	hostHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}.ping(ctx)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_host_capability") {
		t.Fatalf("未知主机能力没有返回稳定错误: code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
