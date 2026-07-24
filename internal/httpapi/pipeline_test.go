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

func TestApplicationRequestAcceptsPublicWorkflowTemplate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	payload := `{
		"name":"选择公共发布计划",
		"repository_id":"637a764b-e79e-41a2-8dd4-cc038479ebee",
		"workflow_template_id":"dd448d0b-df10-45c2-9436-42ee44817399",
		"poll_interval_seconds":60,
		"release_approval_enabled":true
	}`
	context.Request = httptest.NewRequest("POST", "/api/v1/applications", strings.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	var request applicationRequest
	if err := context.ShouldBindJSON(&request); err != nil {
		t.Fatalf("公共发布计划选择不应被拒绝: %v", err)
	}
	if request.WorkflowTemplateID != "dd448d0b-df10-45c2-9436-42ee44817399" {
		t.Fatalf("公共发布计划未正确解析: %+v", request)
	}
}

func TestApplicationRequestRestrictsPullCheckInterval(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, interval := range []string{"3", "5", "10", "60"} {
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		payload := `{"name":"Pull 检查间隔","repository_id":"637a764b-e79e-41a2-8dd4-cc038479ebee","poll_interval_seconds":` + interval + `}`
		context.Request = httptest.NewRequest("POST", "/api/v1/applications", strings.NewReader(payload))
		context.Request.Header.Set("Content-Type", "application/json")
		var request applicationRequest
		if err := context.ShouldBindJSON(&request); err != nil {
			t.Fatalf("Pull 检查间隔 %s 秒不应被拒绝: %v", interval, err)
		}
	}

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("POST", "/api/v1/applications", strings.NewReader(`{"name":"无效间隔","repository_id":"637a764b-e79e-41a2-8dd4-cc038479ebee","poll_interval_seconds":30}`))
	context.Request.Header.Set("Content-Type", "application/json")
	var request applicationRequest
	if err := context.ShouldBindJSON(&request); err == nil {
		t.Fatal("未拒绝设置项之外的 Pull 检查间隔")
	}
}
