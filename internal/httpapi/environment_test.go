package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"zrt/internal/model"
)

func TestEnvironmentHostsRequestRequiresExplicitCollection(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{name: "missing collection", payload: `{}`, wantErr: true},
		{name: "explicit empty collection", payload: `{"host_ids":[]}`},
		{name: "selected hosts", payload: `{"host_ids":["host-a"]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(http.MethodPut, "/environments/environment-a/hosts", strings.NewReader(test.payload))
			context.Request.Header.Set("Content-Type", "application/json")
			var request environmentHostsRequest
			err := context.ShouldBindJSON(&request)
			if (err != nil) != test.wantErr {
				t.Fatalf("请求绑定结果错误: err=%v", err)
			}
			if !test.wantErr && request.HostIDs == nil {
				t.Fatal("显式主机集合不应被绑定为 nil")
			}
		})
	}
}

func TestInfrastructureResponsesHideLegacySafetyLevels(t *testing.T) {
	tests := []struct {
		name      string
		value     any
		forbidden string
	}{
		{
			name:      "environment",
			value:     model.Environment{Level: model.EnvironmentProduction},
			forbidden: `"level":`,
		},
		{
			name: "deployment target",
			value: model.DeploymentTarget{
				Environment: model.EnvironmentProduction,
			},
			forbidden: `"environment":`,
		},
		{
			name: "deployment record",
			value: model.DeploymentRecord{
				Environment: model.EnvironmentProduction,
			},
			forbidden: `"environment":`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(payload), test.forbidden) {
				t.Fatalf("旧安全级别不应通过接口暴露: %s", payload)
			}
		})
	}
}
