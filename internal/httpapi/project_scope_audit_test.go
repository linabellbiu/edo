package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

func TestProjectScopeWriteRejectionAuditHTTP(t *testing.T) {
	var db *gorm.DB
	var logs bytes.Buffer
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, value *gorm.DB) {
		db = value
		deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	})
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	userID := createUserForHTTPTest(t, router, admin, "scope-audit-member", "", nil)
	member := loginForDepartmentTest(t, router, "scope-audit-member")
	projectID := database.DefaultProjectID(database.DefaultDepartmentID)
	// 既有平台审计仅沿actor部门默认归属；这不代表拒绝目标获得业务准入。
	expectedActorDefault := database.DefaultProjectID(database.DefaultDepartmentID)
	appID, workflowID := uuid.NewString(), uuid.NewString()
	path := "/api/v1/applications/" + appID + "/workflows/" + workflowID + "/pipeline-runs"
	fullPath := "/api/v1/applications/:id/workflows/:workflow_id/pipeline-runs"

	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/projects", map[string]string{"name": "审计停用项目"}, admin)
	var project struct{ Project struct{ ID string } }
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &project) != nil || project.Project.ID == "" {
		t.Fatalf("创建独立项目失败: status=%d", created.Code)
	}
	for _, operation := range []struct {
		path string
		body any
	}{
		{"/api/v1/projects/" + project.Project.ID + "/members/" + userID, map[string]string{"role_id": "bbf0404f-7165-5119-8620-194a3fab6c02"}},
		{"/api/v1/projects/" + project.Project.ID, map[string]any{"name": "审计停用项目", "is_active": false}},
	} {
		if got := performJSONRequest(t, router, http.MethodPut, operation.path, operation.body, admin); got.Code != http.StatusNoContent {
			t.Fatalf("准备停用项目失败: status=%d", got.Code)
		}
	}
	entries := func(requestID string) []model.AuditLog {
		t.Helper()
		var rows []model.AuditLog
		if err := db.Where("request_id = ?", requestID).Find(&rows).Error; err != nil {
			t.Fatalf("读取本请求审计失败: %v", err)
		}
		return rows
	}
	counts := func() [2]int64 {
		t.Helper()
		var result [2]int64
		for i, value := range []any{&model.PipelineRun{}, &model.Job{}} {
			if err := db.Model(value).Count(&result[i]).Error; err != nil {
				t.Fatalf("读取副作用基线失败: %v", err)
			}
		}
		return result
	}
	request := func(method, target, header string, cookie *http.Cookie) (*httptest.ResponseRecorder, string) {
		t.Helper()
		id := uuid.NewString()
		r := httptest.NewRequest(method, target, strings.NewReader(`{"password":"scope-body-canary"}`))
		r.Header.Set("X-Request-ID", id)
		r.Header.Set("User-Agent", "scope-agent-canary")
		if header != "" {
			r.Header.Set("X-EDO-Project-ID", header)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w, id
	}
	for _, tc := range []struct {
		name, header, resource, reason string
		status                         int
	}{
		{"缺项目", "", "", "project_required", http.StatusBadRequest},
		{"无成员", projectID, projectID, "project_denied", http.StatusForbidden},
		{"已停用_仅actor默认审计归属不代表目标准入", project.Project.ID, project.Project.ID, "project_denied", http.StatusForbidden},
		{"无效项目头脱敏", "scope-header-canary", "", "project_denied", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := counts()
			logs.Reset()
			response, requestID := request(http.MethodPost, path+"?private=scope-query-canary", tc.header, member)
			var rejected errorResponse
			if response.Code != tc.status || json.Unmarshal(response.Body.Bytes(), &rejected) != nil || rejected.Code != tc.reason || rejected.RequestID != requestID {
				t.Fatalf("原拒绝结果发生变化: status=%d code=%s", response.Code, rejected.Code)
			}
			if after := counts(); after != before {
				t.Fatalf("被拒绝请求产生运行或任务: before=%v after=%v", before, after)
			}
			rows := entries(requestID)
			if len(rows) != 1 {
				t.Fatalf("项目范围写拒绝应有且仅有一条审计: got=%d status=%d code=%s", len(rows), response.Code, rejected.Code)
			}
			entry := rows[0]
			result := model.AuditDenied
			if tc.status == http.StatusBadRequest {
				result = model.AuditFailed
			}
			if entry.Action != "project.access.denied" || entry.ResourceType != "project" || entry.ResourceID != tc.resource || entry.ActorUserID == nil || *entry.ActorUserID != userID || entry.DepartmentID != database.DefaultDepartmentID || entry.Result != result {
				t.Fatalf("项目范围审计身份或结果不符: action=%s resource=%s result=%s", entry.Action, entry.ResourceID, entry.Result)
			}
			if entry.ProjectID != expectedActorDefault {
				t.Fatalf("拒绝审计应仅沿actor部门默认归属: got=%s want=%s", entry.ProjectID, expectedActorDefault)
			}
			if rejectedProject := projectAuditUUID(tc.header); rejectedProject != "" && rejectedProject != expectedActorDefault && entry.ProjectID == rejectedProject {
				t.Fatal("非默认拒绝目标不应成为审计业务归属")
			}
			var metadata map[string]any
			if err := json.Unmarshal(entry.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
			if len(metadata) != 5 || metadata["method"] != http.MethodPost || metadata["path"] != fullPath || metadata["status"] != float64(tc.status) || metadata["reason"] != tc.reason || metadata["target_id"] != appID {
				t.Fatalf("项目范围审计元数据不符: %v", metadata)
			}
			for _, secret := range []string{"scope-body-canary", "scope-query-canary", "scope-header-canary", "scope-agent-canary", member.Value} {
				if strings.Contains(string(entry.Metadata), secret) || strings.Contains(entry.UserAgent, secret) || strings.Contains(entry.ResourceID, secret) || strings.Contains(logs.String(), secret) {
					t.Fatal("拒绝审计或诊断日志包含未授权输入")
				}
			}
		})
	}
	t.Run("无效路由目标不回显", func(t *testing.T) {
		response, requestID := request(http.MethodPost, strings.Replace(path, appID, "scope-target-canary", 1), projectID, member)
		rows := entries(requestID)
		if response.Code != http.StatusForbidden || len(rows) != 1 {
			t.Fatalf("无效路由目标拒绝或审计不符: status=%d rows=%d", response.Code, len(rows))
		}
		if rows[0].ProjectID != expectedActorDefault {
			t.Fatalf("无效目标拒绝审计应仅沿actor部门默认归属: got=%s", rows[0].ProjectID)
		}
		var metadata map[string]any
		if err := json.Unmarshal(rows[0].Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		if _, exists := metadata["target_id"]; exists || len(metadata) != 4 || strings.Contains(string(rows[0].Metadata), "scope-target-canary") {
			t.Fatal("无效路由目标被写入审计")
		}
	})
	for _, tc := range []struct{ method, suffix string }{{http.MethodPut, ""}, {http.MethodPatch, "/status"}, {http.MethodDelete, ""}} {
		t.Run("真实仓库路由非成员拒绝_"+tc.method, func(t *testing.T) {
			targetID := uuid.NewString()
			target := "/api/v1/repositories/" + targetID + tc.suffix
			route := "/api/v1/repositories/:id" + tc.suffix
			registered := false
			for _, item := range router.Routes() {
				registered = registered || item.Method == tc.method && item.Path == route
			}
			if !registered {
				t.Fatal("必须使用当前已登记的真实业务路由")
			}
			before := counts()
			response, requestID := request(tc.method, target, projectID, member)
			var rejected errorResponse
			if response.Code != http.StatusForbidden || json.Unmarshal(response.Body.Bytes(), &rejected) != nil || rejected.Code != "project_denied" || rejected.RequestID != requestID {
				t.Fatalf("非成员写未保持原拒绝: method=%s status=%d code=%s", tc.method, response.Code, rejected.Code)
			}
			if after := counts(); after != before {
				t.Fatalf("非成员写产生运行或任务: before=%v after=%v", before, after)
			}
			rows := entries(requestID)
			if len(rows) != 1 || rows[0].Action != "project.access.denied" || rows[0].Result != model.AuditDenied || rows[0].ActorUserID == nil || *rows[0].ActorUserID != userID || rows[0].ResourceID != projectID {
				t.Fatalf("真实写拒绝审计不唯一或身份不符: method=%s rows=%d", tc.method, len(rows))
			}
			var metadata map[string]any
			if err := json.Unmarshal(rows[0].Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
			if len(metadata) != 5 || metadata["method"] != tc.method || metadata["path"] != route || metadata["status"] != float64(http.StatusForbidden) || metadata["reason"] != "project_denied" || metadata["target_id"] != targetID {
				t.Fatalf("真实写拒绝元数据不符: method=%s metadata=%v", tc.method, metadata)
			}
			if rows[0].ProjectID != expectedActorDefault {
				t.Fatalf("非成员写审计应仅沿actor部门默认归属: method=%s got=%s", tc.method, rows[0].ProjectID)
			}
		})
	}
	t.Run("读拒绝及未认证写不扩充审计", func(t *testing.T) {
		for _, tc := range []struct {
			method string
			cookie *http.Cookie
			status int
		}{{http.MethodGet, member, http.StatusForbidden}, {http.MethodPost, nil, http.StatusUnauthorized}} {
			target := "/api/v1/repositories"
			if tc.method == http.MethodPost {
				target = path
			}
			response, requestID := request(tc.method, target, projectID, tc.cookie)
			if response.Code != tc.status || len(entries(requestID)) != 0 {
				t.Fatalf("不应新增范围审计: method=%s status=%d", tc.method, response.Code)
			}
		}
	})
	t.Run("审计存储失败不改变拒绝", func(t *testing.T) {
		const callback, diagnostic = "test:project_scope_audit_failure", "synthetic-project-scope-audit-failure"
		if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "audit_logs" {
				tx.AddError(errors.New(diagnostic))
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Callback().Create().Remove(callback) }()
		logs.Reset()
		response, _ := request(http.MethodPost, path, projectID, member)
		if response.Code != http.StatusForbidden || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"project_denied"`)) || strings.Contains(response.Body.String(), diagnostic) || !strings.Contains(logs.String(), diagnostic) || !strings.Contains(logs.String(), "project_scope_audit") {
			t.Fatalf("审计故障没有保留原拒绝与安全日志: status=%d", response.Code)
		}
	})
	t.Run("已准入写沿用原审计且不重复", func(t *testing.T) {
		if response := performJSONRequest(t, router, http.MethodPut, "/api/v1/projects/"+projectID+"/members/"+userID, map[string]string{"role_id": "bbf0404f-7165-5119-8620-194a3fab6c02"}, admin); response.Code != http.StatusNoContent {
			t.Fatalf("正常加入测试项目失败: %d", response.Code)
		}
		response, requestID := request(http.MethodPost, path, projectID, member)
		rows := entries(requestID)
		if response.Code != http.StatusForbidden || len(rows) != 1 || rows[0].Action != "pipeline.prepare" || rows[0].Result != model.AuditDenied {
			t.Fatalf("普通权限拒绝被重复或改写审计: status=%d rows=%d", response.Code, len(rows))
		}
		response = performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories", map[string]any{"name": "scope-audit-allowed", "provider": "generic", "clone_url": "https://example.com/scope-audit.git", "default_branch": "main", "auth_type": "none"}, admin)
		rows = entries(response.Header().Get("X-Request-ID"))
		if response.Code != http.StatusCreated || len(rows) != 1 || rows[0].Action != "repository.create" || rows[0].Result != model.AuditSucceeded {
			t.Fatalf("允许写出现额外范围审计: status=%d rows=%d", response.Code, len(rows))
		}
	})
}

func TestProjectAuditUUID(t *testing.T) {
	const canonical = "4c4ef6df-265f-44ce-a87e-6a8019f4b1de"
	for _, tc := range []struct{ name, input, expected string }{
		{"小写", canonical, canonical},
		{"大写规范化", strings.ToUpper(canonical), canonical},
		{"nil", uuid.Nil.String(), ""},
		{"等长解析失败", "zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz", ""},
		{"URN不接受", "urn:uuid:" + canonical, ""},
		{"brace不接受", "{" + canonical + "}", ""},
		{"空字符串", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectAuditUUID(tc.input); got != tc.expected {
				t.Fatalf("审计UUID规范化不符: got=%q want=%q", got, tc.expected)
			}
		})
	}
}
