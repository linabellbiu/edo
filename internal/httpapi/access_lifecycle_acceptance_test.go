package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

func accessLifecycleError(t *testing.T, responseCode int, body []byte, wantStatus int, wantCode, wantMessage string) {
	t.Helper()
	var failure errorResponse
	if err := json.Unmarshal(body, &failure); err != nil || responseCode != wantStatus || failure.Code != wantCode || failure.RequestID == "" {
		t.Fatalf("错误边界不符合预期: status=%d want=%d code=%s want_code=%s body=%s err=%v", responseCode, wantStatus, failure.Code, wantCode, body, err)
	}
	if wantMessage != "" && failure.Message != wantMessage {
		t.Fatalf("对外文案不稳定: got=%q want=%q", failure.Message, wantMessage)
	}
}

func accessLifecycleRoleCounts(t *testing.T, db *gorm.DB, roleID string) []int64 {
	t.Helper()
	counts := make([]int64, 4)
	for index, item := range []struct{ model, condition any }{
		{&model.Role{}, &model.Role{ID: roleID}},
		{&model.RolePermission{}, &model.RolePermission{RoleID: roleID}},
		{&model.UserRole{}, &model.UserRole{RoleID: roleID}},
		{&model.ProjectMember{}, &model.ProjectMember{RoleID: roleID}},
	} {
		if err := db.Model(item.model).Where(item.condition).Count(&counts[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func accessLifecycleRoleState(t *testing.T, db *gorm.DB, roleID string) string {
	t.Helper()
	var roles []model.Role
	var permissions []model.RolePermission
	var users []model.UserRole
	var members []model.ProjectMember
	for _, item := range []struct{ rows, condition any }{
		{&roles, &model.Role{ID: roleID}}, {&permissions, &model.RolePermission{RoleID: roleID}},
		{&users, &model.UserRole{RoleID: roleID}}, {&members, &model.ProjectMember{RoleID: roleID}},
	} {
		if err := db.Where(item.condition).Order("id ASC").Find(item.rows).Error; err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal([]any{roles, permissions, users, members})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestAccessLifecycleRoleDeletionGuardsAndSuccessHTTP(t *testing.T) {
	var db *gorm.DB
	var deps Dependencies
	router, cleanup := newAuthTestRouter(t, func(value *Dependencies, database *gorm.DB) { deps, db = *value, database })
	t.Cleanup(cleanup)
	if err := deps.Access.EnsureProjectRoles(context.Background()); err != nil {
		t.Fatal(err)
	}
	admin := loginForDepartmentTest(t, router, "admin")
	operatorRole := createRoleForHTTPTest(t, router, admin, "role-delete-operator", []string{access.PermissionRoleDelete, access.PermissionRoleRead})
	operatorID := createUserForHTTPTest(t, router, admin, "role-delete-operator", "", []string{operatorRole})
	operator := loginForDepartmentTest(t, router, "role-delete-operator")
	readRole := createRoleForHTTPTest(t, router, admin, "role-delete-reader", []string{access.PermissionRoleRead})
	createUserForHTTPTest(t, router, admin, "role-delete-reader", "", []string{readRole})
	reader := loginForDepartmentTest(t, router, "role-delete-reader")
	unused := createRoleForHTTPTest(t, router, admin, "role-delete-unused", []string{access.PermissionUserRead})
	beforeUnused := accessLifecycleRoleState(t, db, unused)
	denied := performJSONRequest(t, router, http.MethodDelete, "/api/v1/roles/"+unused, nil, reader)
	accessLifecycleError(t, denied.Code, denied.Body.Bytes(), http.StatusForbidden, "permission_denied", "")
	if accessLifecycleRoleState(t, db, unused) != beforeUnused {
		t.Fatal("无删除权限的请求改变了角色或关联")
	}
	foreignDepartment := createDepartmentForHTTPTest(t, router, admin, "角色删除外部部门")
	used := createRoleForHTTPTest(t, router, admin, "role-delete-in-use", []string{access.PermissionUserRead})
	createUserForHTTPTest(t, router, admin, "role-delete-foreign-member", foreignDepartment, []string{used})
	projectRole := createRoleForHTTPTest(t, router, admin, "role-delete-project-use", []string{"application.read"})
	projectUser := createUserForHTTPTest(t, router, admin, "role-delete-project-member", "", nil)
	member := model.ProjectMember{ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), UserID: projectUser, RoleID: projectRole, CreatedAt: time.Now().UTC()}
	if err := db.Create(&member).Error; err != nil {
		t.Fatal(err)
	}
	for _, roleID := range []string{used, projectRole, access.ProjectOwnerRoleID} {
		before := accessLifecycleRoleState(t, db, roleID)
		counts := accessLifecycleRoleCounts(t, db, roleID)
		if roleID == access.ProjectOwnerRoleID && (counts[2] != 0 || counts[3] != 0) {
			t.Fatal("内置管理员保护测试必须没有成员，不能由在用保护掩盖")
		}
		for _, cookie := range []*http.Cookie{operator, admin} {
			response := performJSONRequest(t, router, http.MethodDelete, "/api/v1/roles/"+roleID, nil, cookie)
			accessLifecycleError(t, response.Code, response.Body.Bytes(), http.StatusConflict, "role_in_use", access.ErrRoleInUse.Error())
			if accessLifecycleRoleState(t, db, roleID) != before {
				t.Fatalf("拒绝删除改变了角色、权限或成员关联: role_id=%s", roleID)
			}
		}
	}
	for _, deny := range [][]string{{access.PermissionRoleDelete}, {}} {
		response := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+operatorID+"/permissions", map[string]any{"allow": []string{}, "deny": deny}, admin)
		if response.Code != http.StatusNoContent {
			t.Fatalf("设置测试用户覆盖失败: status=%d body=%s", response.Code, response.Body.String())
		}
		if len(deny) > 0 {
			response = performJSONRequest(t, router, http.MethodDelete, "/api/v1/roles/"+unused, nil, operator)
			accessLifecycleError(t, response.Code, response.Body.Bytes(), http.StatusForbidden, "permission_denied", "")
			if accessLifecycleRoleState(t, db, unused) != beforeUnused {
				t.Fatal("个人拒绝未保留角色及其权限")
			}
		}
	}
	deleted := performJSONRequest(t, router, http.MethodDelete, "/api/v1/roles/"+unused, nil, operator)
	if deleted.Code != http.StatusNoContent || !slices.Equal(accessLifecycleRoleCounts(t, db, unused), []int64{0, 0, 0, 0}) {
		t.Fatalf("普通授权用户不能完整删除空闲角色: status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := performJSONRequest(t, router, http.MethodDelete, "/api/v1/roles/"+unused, nil, operator)
	accessLifecycleError(t, missing.Code, missing.Body.Bytes(), http.StatusNotFound, "role_not_found", access.ErrRoleNotFound.Error())
	for _, result := range []model.AuditResult{model.AuditSucceeded, model.AuditFailed, model.AuditDenied} {
		var count int64
		if err := db.Model(&model.AuditLog{}).Where(&model.AuditLog{Action: "role.delete", Result: result}).Count(&count).Error; err != nil || count == 0 {
			t.Fatalf("角色删除缺少成功/失败/拒绝审计: result=%s count=%d err=%v", result, count, err)
		}
	}
}

func TestAccessLifecycleRoleDeleteFailureRollsBackHTTP(t *testing.T) {
	var db *gorm.DB
	var logs bytes.Buffer
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, value *gorm.DB) {
		db = value
		deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	})
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	roleID := createRoleForHTTPTest(t, router, admin, "role-delete-rollback", []string{access.PermissionUserRead, access.PermissionAuditRead})
	before := accessLifecycleRoleState(t, db, roleID)
	const diagnostic = "synthetic-role-delete-storage-error"
	const callback = "test:access_role_delete_rollback"
	if err := db.Callback().Delete().Before("gorm:delete").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "roles" {
			tx.AddError(errors.New(diagnostic))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Delete().Remove(callback) })
	logs.Reset()
	response := performJSONRequest(t, router, http.MethodDelete, "/api/v1/roles/"+roleID, nil, admin)
	accessLifecycleError(t, response.Code, response.Body.Bytes(), http.StatusInternalServerError, "internal_error", "服务暂时不可用，请稍后重试")
	if strings.Contains(response.Body.String(), diagnostic) || !strings.Contains(logs.String(), diagnostic) || !strings.Contains(logs.String(), "role_delete") {
		t.Fatal("删除故障没有安全地区分对外提示和内部诊断")
	}
	if accessLifecycleRoleState(t, db, roleID) != before {
		t.Fatal("删除角色失败后未回滚先前删除的权限关联")
	}
}

func accessLifecycleAuditPage(t *testing.T, router *gin.Engine, cookie *http.Cookie, query string) []model.AuditLog {
	t.Helper()
	entries, _ := accessLifecycleAuditPageCursor(t, router, cookie, query)
	return entries
}

func accessLifecycleAuditPageCursor(t *testing.T, router *gin.Engine, cookie *http.Cookie, query string) ([]model.AuditLog, string) {
	t.Helper()
	page := accessLifecycleAuditPageDetails(t, router, cookie, query)
	return page.Logs, page.NextCursor
}

type accessLifecycleAuditPageResult struct {
	Logs       []model.AuditLog `json:"audit_logs"`
	NextCursor string           `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

func accessLifecycleAuditPageDetails(t *testing.T, router *gin.Engine, cookie *http.Cookie, query string) accessLifecycleAuditPageResult {
	t.Helper()
	response := performJSONRequest(t, router, http.MethodGet, "/api/v1/audit-logs"+query, nil, cookie)
	var payload accessLifecycleAuditPageResult
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil {
		t.Fatalf("查询审计页失败: status=%d body=%s", response.Code, response.Body.String())
	}
	return payload
}

func TestAuditLifecycleHTTPPaginationDepartmentScopeAndFailure(t *testing.T) {
	var db *gorm.DB
	var logs bytes.Buffer
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, value *gorm.DB) {
		db = value
		deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	})
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	roleID := createRoleForHTTPTest(t, router, admin, "audit-lifecycle-reader", []string{access.PermissionAuditRead})
	createUserForHTTPTest(t, router, admin, "audit-lifecycle-reader", "", []string{roleID})
	reader := loginForDepartmentTest(t, router, "audit-lifecycle-reader")
	foreignDepartment := createDepartmentForHTTPTest(t, router, admin, "外部审计部门")
	base := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	entries := make([]model.AuditLog, 55)
	for index := range entries {
		entries[index] = model.AuditLog{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), Action: "audit.pagination", ResourceType: "acceptance", ResourceID: fmt.Sprint(index), Result: model.AuditSucceeded, Metadata: datatypes.JSON(`{}`), CreatedAt: base.Add(time.Duration(index) * time.Second)}
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	foreign := model.AuditLog{ID: uuid.NewString(), DepartmentID: foreignDepartment, ProjectID: database.DefaultProjectID(foreignDepartment), Action: "audit.foreign", ResourceType: "acceptance", Result: model.AuditSucceeded, Metadata: datatypes.JSON(`{}`), CreatedAt: base.Add(time.Hour)}
	if err := db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	first := accessLifecycleAuditPage(t, router, reader, "?limit=2")
	if len(first) != 2 || first[0].ID != entries[54].ID || first[1].ID != entries[53].ID {
		t.Fatalf("审计首屏未按内部顺序限量或泄露其他部门: %+v", first)
	}
	second := accessLifecycleAuditPage(t, router, reader, "?limit=2&before="+url.QueryEscape(first[1].CreatedAt.Format(time.RFC3339Nano)))
	if len(second) != 2 || second[0].ID != entries[52].ID || second[1].ID != entries[51].ID {
		t.Fatalf("审计分页游标没有排除边界或保持顺序: %+v", second)
	}
	for _, query := range []string{"", "?limit=0", "?limit=201", "?limit=not-a-number"} {
		if count := len(accessLifecycleAuditPage(t, router, reader, query)); count != 50 {
			t.Fatalf("无效或缺省limit未按50回退: query=%q count=%d", query, count)
		}
	}
	// 审计是平台级部门权限；伪造项目参数不能扩大到其他部门，也不会变成业务项目查询。
	spoofed := accessLifecycleAuditPage(t, router, reader, "?limit=200&project_id="+foreign.ProjectID)
	for _, entry := range spoofed {
		if entry.DepartmentID != database.DefaultDepartmentID {
			t.Fatalf("伪造项目参数越过审计部门边界: id=%s", entry.ID)
		}
	}
	adminPage := accessLifecycleAuditPage(t, router, admin, "?limit=2")
	if len(adminPage) != 2 || adminPage[0].ID != foreign.ID {
		t.Fatal("超级管理员全局审计范围被意外缩小")
	}
	invalid := performJSONRequest(t, router, http.MethodGet, "/api/v1/audit-logs?before=invalid", nil, reader)
	accessLifecycleError(t, invalid.Code, invalid.Body.Bytes(), http.StatusBadRequest, "invalid_cursor", "审计日志分页游标无效")
	const callback = "test:audit_list_storage_failure"
	const diagnostic = "synthetic-audit-storage-error"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			tx.AddError(errors.New(diagnostic))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	logs.Reset()
	failed := performJSONRequest(t, router, http.MethodGet, "/api/v1/audit-logs", nil, reader)
	accessLifecycleError(t, failed.Code, failed.Body.Bytes(), http.StatusInternalServerError, "internal_error", "服务暂时不可用，请稍后重试")
	if strings.Contains(failed.Body.String(), diagnostic) || !strings.Contains(logs.String(), diagnostic) || !strings.Contains(logs.String(), "audit_list") {
		t.Fatal("审计存储故障未区分诊断日志与安全提示")
	}
}

func TestAuditLifecycleCredentialOperationsExcludeSensitiveInputHTTP(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	const token = "synthetic-audit-sensitive-git-token"
	const password = "synthetic-audit-password-123456"
	created := performJSONRequest(t, router, http.MethodPost, "/api/v1/git-credentials?token="+token, map[string]any{"name": "audit-secret", "provider": "github", "auth_type": "token", "secret": token}, admin)
	if created.Code != http.StatusCreated {
		t.Fatalf("创建凭据失败: status=%d body=%s", created.Code, created.Body.String())
	}
	credentialID := operationsID(t, created, "credential")
	revealed := performJSONRequest(t, router, http.MethodGet, "/api/v1/git-credentials/"+credentialID+"/secret", nil, admin)
	if revealed.Code != http.StatusOK || !strings.Contains(revealed.Body.String(), token) {
		t.Fatal("用例未实际读取敏感凭据，无法验证读取操作审计")
	}
	user := performJSONRequest(t, router, http.MethodPost, "/api/v1/users", map[string]any{"username": "audit-password-user", "nickname": "审计密码用户", "password": password, "role_ids": []string{}}, admin)
	if user.Code != http.StatusCreated {
		t.Fatalf("创建用户失败: status=%d body=%s", user.Code, user.Body.String())
	}
	var credential model.GitCredential
	if err := db.Where(&model.GitCredential{ID: credentialID}).First(&credential).Error; err != nil {
		t.Fatal(err)
	}
	page := accessLifecycleAuditPage(t, router, admin, "?limit=200")
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{token, password, admin.Value, credential.SecretCiphertext} {
		if sensitive == "" || bytes.Contains(encoded, []byte(sensitive)) {
			t.Fatal("审计响应出现请求密钥、密码、会话或加密凭据")
		}
	}
	seen := map[string]bool{}
	for _, entry := range page {
		if entry.ResourceID != credentialID || (entry.Action != "credential.create" && entry.Action != "credential.reveal") {
			continue
		}
		seen[entry.Action] = true
		var metadata map[string]any
		if err := json.Unmarshal(entry.Metadata, &metadata); err != nil || len(metadata) != 3 || metadata["path"] == nil || metadata["method"] == nil || metadata["status"] == nil {
			t.Fatalf("凭据操作审计没有保留安全最小元数据: action=%s metadata=%s err=%v", entry.Action, entry.Metadata, err)
		}
	}
	if !seen["credential.create"] || !seen["credential.reveal"] {
		t.Fatalf("密钥创建或读取缺少审计，不能靠不记录来避免泄露: %v", seen)
	}
}

func TestAuditLifecyclePaginationKeepsEqualTimestampRowsHTTP(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	stamp := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	entries := make([]model.AuditLog, 5)
	for index := range entries {
		entries[index] = model.AuditLog{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), Action: "audit.same_timestamp", ResourceType: "acceptance", Result: model.AuditSucceeded, Metadata: datatypes.JSON(`{}`), CreatedAt: stamp}
	}
	// 时间可由历史导入或事务交错而不随写入顺序递增，游标必须遵守实际的行号排序。
	entries[0].CreatedAt = stamp.Add(time.Hour)
	entries[1].CreatedAt = stamp.Add(-time.Hour)
	if err := db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	want := []string{entries[4].ID, entries[3].ID, entries[2].ID, entries[1].ID, entries[0].ID}
	var got []string
	query := "?limit=2"
	for pageNumber := range 5 {
		page, next := accessLifecycleAuditPageCursor(t, router, admin, query)
		if pageNumber == 0 && (len(page) != 2 || page[0].ID != want[0] || page[1].ID != want[1]) {
			t.Fatalf("没有建立同时间戳分页边界: %+v", page)
		}
		for _, entry := range page {
			if slices.Contains(want, entry.ID) {
				got = append(got, entry.ID)
			}
		}
		if len(got) >= len(want) || len(page) < 2 {
			break
		}
		if next != "" {
			query = "?limit=2&cursor=" + url.QueryEscape(next)
		} else {
			// 旧接口没有稳定游标，沿用旧客户端可用的before以复现原缺陷。
			query = "?limit=2&before=" + url.QueryEscape(page[len(page)-1].CreatedAt.Format(time.RFC3339Nano))
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("审计分页必须按写入顺序不重不漏，不能由同时间戳或历史时间跳过记录: got=%v want=%v", got, want)
	}
}

func TestAuditLifecycleCursorSurvivesConcurrentWritesAndDeletedAnchorHTTP(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	departmentID := createDepartmentForHTTPTest(t, router, admin, "审计游标甲部门")
	foreignDepartment := createDepartmentForHTTPTest(t, router, admin, "审计游标乙部门")
	roleID := createRoleForHTTPTest(t, router, admin, "audit-cursor-reader", []string{access.PermissionAuditRead})
	createUserForHTTPTest(t, router, admin, "audit-cursor-reader", departmentID, []string{roleID})
	reader := loginForDepartmentTest(t, router, "audit-cursor-reader")
	stamp := time.Now().UTC().Truncate(time.Second)
	entries := make([]model.AuditLog, 5)
	for index := range entries {
		entries[index] = model.AuditLog{ID: uuid.NewString(), DepartmentID: departmentID, ProjectID: database.DefaultProjectID(departmentID), Action: "audit.cursor", ResourceType: "acceptance", Result: model.AuditSucceeded, Metadata: datatypes.JSON(`{}`), CreatedAt: stamp.Add(time.Duration(index) * time.Second)}
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	first := accessLifecycleAuditPageDetails(t, router, reader, "?limit=2")
	anchor := entries[3]
	if len(first.Logs) != 2 || first.Logs[0].ID != entries[4].ID || first.Logs[1].ID != anchor.ID || !first.HasMore || first.NextCursor != strconv.FormatUint(anchor.RowID, 10) {
		t.Fatalf("首屏游标必须指向实际返回的末条，而非多读的探测行: %+v", first)
	}
	deleted := db.Where(&model.AuditLog{ID: anchor.ID}).Delete(&model.AuditLog{})
	if deleted.Error != nil || deleted.RowsAffected != 1 {
		t.Fatalf("未删除本用例准确anchor样本: rows=%d err=%v", deleted.RowsAffected, deleted.Error)
	}
	newEntry := entries[0]
	newEntry.RowID, newEntry.ID, newEntry.CreatedAt = 0, uuid.NewString(), stamp.Add(-time.Hour)
	if err := db.Create(&newEntry).Error; err != nil {
		t.Fatal(err)
	}
	second := accessLifecycleAuditPageDetails(t, router, reader, "?limit=2&cursor="+url.QueryEscape(first.NextCursor))
	if len(second.Logs) != 2 || second.Logs[0].ID != entries[2].ID || second.Logs[1].ID != entries[1].ID || !second.HasMore || second.NextCursor != strconv.FormatUint(entries[1].RowID, 10) {
		t.Fatalf("删除anchor或新增历史时间记录改变了正在读取的后续页: %+v", second)
	}
	last := accessLifecycleAuditPageDetails(t, router, reader, "?limit=2&cursor="+url.QueryEscape(second.NextCursor))
	if len(last.Logs) != 1 || last.Logs[0].ID != entries[0].ID || last.HasMore || last.NextCursor != "" {
		t.Fatalf("最后一页必须返回剩余记录并清空游标和has_more: %+v", last)
	}
	got := []string{first.Logs[0].ID, first.Logs[1].ID, second.Logs[0].ID, second.Logs[1].ID, last.Logs[0].ID}
	want := []string{entries[4].ID, entries[3].ID, entries[2].ID, entries[1].ID, entries[0].ID}
	if !slices.Equal(got, want) {
		t.Fatalf("变化期间的已存在记录必须不重不漏: got=%v want=%v", got, want)
	}
	refresh := accessLifecycleAuditPageDetails(t, router, reader, "?limit=2")
	if len(refresh.Logs) != 2 || refresh.Logs[0].ID != newEntry.ID {
		t.Fatal("新增记录应在刷新后按行号出现，不能因历史时间被遗漏")
	}
	combined := accessLifecycleAuditPageDetails(t, router, reader, "?limit=2&cursor="+first.NextCursor+"&before="+url.QueryEscape(entries[2].CreatedAt.Format(time.RFC3339Nano)))
	if len(combined.Logs) != 2 || combined.Logs[0].ID != entries[1].ID || combined.Logs[1].ID != entries[0].ID || combined.HasMore || combined.NextCursor != "" {
		t.Fatalf("before与行号游标必须同时收窄查询，且边界严格排除: %+v", combined)
	}
	empty := accessLifecycleAuditPageDetails(t, router, reader, "?limit=2&cursor="+strconv.FormatUint(entries[0].RowID, 10))
	if len(empty.Logs) != 0 || empty.HasMore || empty.NextCursor != "" {
		t.Fatalf("空末页不可返回继续翻页状态: %+v", empty)
	}
	foreign := newEntry
	foreign.RowID, foreign.ID, foreign.DepartmentID, foreign.ProjectID = 0, uuid.NewString(), foreignDepartment, database.DefaultProjectID(foreignDepartment)
	if err := db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	forged := accessLifecycleAuditPageDetails(t, router, reader, "?limit=200&cursor="+strconv.FormatUint(foreign.RowID+1, 10)+"&project_id="+foreign.ProjectID)
	if len(forged.Logs) != 5 || forged.HasMore || forged.NextCursor != "" {
		t.Fatalf("伪造其他部门行号不应改变可见记录集合: %+v", forged)
	}
	for _, entry := range forged.Logs {
		if entry.DepartmentID != departmentID || entry.ID == foreign.ID {
			t.Fatal("行号游标绕过了当前用户的审计部门范围")
		}
	}
	maximum := accessLifecycleAuditPageDetails(t, router, reader, "?limit=200&cursor=9223372036854775807")
	if len(maximum.Logs) != len(forged.Logs) {
		t.Fatal("共同BIGINT最大合法行号被拒绝或改变了部门范围")
	}
}

func TestAuditLifecycleRejectsMalformedCursorSafelyHTTP(t *testing.T) {
	var logs bytes.Buffer
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, _ *gorm.DB) { deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil)) })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	for _, cursor := range []string{"", "0", "-1", "9223372036854775808", "18446744073709551616", "synthetic-audit-cursor-secret", "01", "+1", " 1", "1.0"} {
		t.Run(fmt.Sprintf("cursor=%q", cursor), func(t *testing.T) {
			logs.Reset()
			response := performJSONRequest(t, router, http.MethodGet, "/api/v1/audit-logs?cursor="+url.QueryEscape(cursor), nil, admin)
			accessLifecycleError(t, response.Code, response.Body.Bytes(), http.StatusBadRequest, "invalid_cursor", "审计日志分页游标无效")
			if !strings.Contains(logs.String(), "audit_list_cursor") || !strings.Contains(logs.String(), `"err"`) {
				t.Fatal("无效游标拒绝前缺少带操作及诊断上下文的日志")
			}
			if cursor == "synthetic-audit-cursor-secret" && (strings.Contains(response.Body.String(), cursor) || strings.Contains(logs.String(), cursor)) {
				t.Fatal("游标解析错误不应回显或记录不可信原始参数")
			}
		})
	}
}
