package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestScriptBuildPlanRejectsUnsafePathsBeforeSavingHTTP(t *testing.T) {
	router, closeTest := newAuthTestRouter(t)
	defer closeTest()
	login := performJSONRequest(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "correct horse battery staple",
	}, nil)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) == 0 {
		t.Fatal("测试管理员登录失败")
	}
	cookie := login.Result().Cookies()[0]
	input := func() map[string]any {
		return map[string]any{
			"name": "路径校验测试构建", "kind": "script", "script": "printf ready",
			"working_directory": ".", "artifact_path": "dist", "timeout_seconds": 120,
		}
	}
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/build-plans", input(), cookie)
	var payload struct {
		BuildPlan struct {
			ID string `json:"id"`
		} `json:"build_plan"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &payload) != nil || payload.BuildPlan.ID == "" {
		t.Fatal("合法构建方案创建失败")
	}
	before := performJSONRequest(t, router, http.MethodGet, "/api/v1/build-plans", nil, cookie)
	if before.Code != http.StatusOK {
		t.Fatal("读取修改前构建方案失败")
	}
	for _, field := range []string{"working_directory", "artifact_path"} {
		for _, invalid := range []string{"../outside", "/etc"} {
			for _, method := range []string{http.MethodPost, http.MethodPut} {
				t.Run(field+"/"+method+"/"+invalid, func(t *testing.T) {
					value := input()
					value["name"] = "不应保存的路径配置"
					value[field] = invalid
					path := "/api/v1/build-plans"
					if method == http.MethodPut {
						path += "/" + payload.BuildPlan.ID
					}
					rejected := performJSONRequest(t, router, method, path, value, cookie)
					if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "构建方案配置无效") {
						t.Fatalf("无效路径未返回稳定中文400: status=%d", rejected.Code)
					}
					if strings.Contains(rejected.Body.String(), invalid) {
						t.Fatal("拒绝响应不应回显路径")
					}
					after := performJSONRequest(t, router, http.MethodGet, "/api/v1/build-plans", nil, cookie)
					if after.Code != http.StatusOK || !bytes.Equal(before.Body.Bytes(), after.Body.Bytes()) {
						t.Fatal("无效路径请求改变了已有方案或新增方案")
					}
				})
			}
		}
	}
}
