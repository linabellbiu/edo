package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestApplicationRequestAcceptsMultipleEnvironments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	payload := `{
		"name":"发布计划界面验收",
		"repository_id":"637a764b-e79e-41a2-8dd4-cc038479ebee",
		"poll_interval_seconds":60,
		"release_approval_enabled":true,
		"environments":[
			{"key":"test","name":"测试环境","branch":"test","watch_push":true,"watch_pull_request":true,"tag_pattern":"v*","sort_order":0},
			{"key":"prod","name":"生产环境","branch":"release","watch_tags":true,"tag_pattern":"v*","sort_order":1}
		]
	}`
	context.Request = httptest.NewRequest("POST", "/api/v1/applications", strings.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	var request applicationRequest
	if err := context.ShouldBindJSON(&request); err != nil {
		t.Fatalf("多环境应用请求不应被拒绝: %v", err)
	}
	if len(request.Environments) != 2 || request.Environments[0].Key != "test" || request.Environments[1].Key != "prod" {
		t.Fatalf("多环境应用请求解析错误: %+v", request.Environments)
	}
	if !request.Environments[0].WatchPush || !request.Environments[0].WatchPullRequest || !request.Environments[1].WatchTags {
		t.Fatalf("多环境触发方式解析错误: %+v", request.Environments)
	}
}
