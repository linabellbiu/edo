package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/account"
	"edo/internal/database"
	"edo/internal/model"
)

func platformUserState(t *testing.T, db *gorm.DB, userID string) [32]byte {
	t.Helper()
	var users []model.User
	var roles []model.UserRole
	var overrides []model.UserPermission
	var members []model.ProjectMember
	var credentials []model.GitCredential
	for _, item := range []struct{ rows, condition any }{
		{&users, &model.User{ID: userID}}, {&roles, &model.UserRole{UserID: userID}},
		{&overrides, &model.UserPermission{UserID: userID}}, {&members, &model.ProjectMember{UserID: userID}},
		{&credentials, &model.GitCredential{UserID: userID}},
	} {
		if err := db.Where(item.condition).Order("id ASC").Find(item.rows).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 比较完整存储状态，但不把口令摘要或凭据密文输出到测试日志。
	encoded, err := json.Marshal([]any{users, roles, overrides, members, credentials})
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(encoded)
}

func TestSolePlatformAdminGuardsHaveValidOrdinaryUserControlsHTTP(t *testing.T) {
	var db *gorm.DB
	var accounts *account.Service
	router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, value *gorm.DB) { db, accounts = value, deps.Accounts })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	adminInfo := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/me", nil, admin)
	adminID := currentUserIDFromResponse(t, adminInfo.Body.Bytes())
	managerRole := createRoleForHTTPTest(t, router, admin, "platform-guard-manager", []string{
		access.PermissionUserRead, access.PermissionUserUpdate, access.PermissionUserDelete,
	})
	createUserForHTTPTest(t, router, admin, "platform-guard-manager", "", []string{managerRole})
	manager := loginForDepartmentTest(t, router, "platform-guard-manager")
	readerRole := createRoleForHTTPTest(t, router, admin, "platform-guard-reader", []string{access.PermissionUserRead})
	createUserForHTTPTest(t, router, admin, "platform-guard-reader", "", []string{readerRole})
	reader := loginForDepartmentTest(t, router, "platform-guard-reader")

	for _, step := range []struct {
		name, method, suffix, message string
		body                          any
	}{
		{"status", http.MethodPatch, "/status", account.ErrSuperuserImmutable.Error(), map[string]any{"active": false}},
		{"roles", http.MethodPut, "/roles", "超级管理员不需要配置角色或权限", map[string]any{"role_ids": []string{readerRole}}},
		{"permissions", http.MethodPut, "/permissions", "超级管理员不需要配置角色或权限", map[string]any{"allow": []string{access.PermissionUserRead}, "deny": []string{access.PermissionUserDelete}}},
		{"access", http.MethodPut, "/access", "超级管理员不需要配置角色或权限", map[string]any{"role_ids": []string{readerRole}, "allow": []string{access.PermissionUserUpdate}, "deny": []string{access.PermissionUserRead}}},
		{"delete", http.MethodDelete, "", "超级管理员不能删除", nil},
	} {
		t.Run(step.name, func(t *testing.T) {
			controlID := createUserForHTTPTest(t, router, admin, "platform-control-"+step.name, "", nil)
			controlCookie := loginForDepartmentTest(t, router, "platform-control-"+step.name)
			if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", controlCookie); response.Code != http.StatusOK {
				t.Fatalf("普通对照账号的原会话前提未满足: status=%d", response.Code)
			}
			beforeAdmin, beforeControl := platformUserState(t, db, adminID), platformUserState(t, db, controlID)
			for _, target := range []string{adminID, controlID} {
				denied := performJSONRequest(t, router, step.method, "/api/v1/users/"+target+step.suffix, step.body, reader)
				accessLifecycleError(t, denied.Code, denied.Body.Bytes(), http.StatusForbidden, "permission_denied", "")
			}
			if platformUserState(t, db, adminID) != beforeAdmin || platformUserState(t, db, controlID) != beforeControl {
				t.Fatal("无权用户的请求改变了账号或授权状态")
			}
			response := performJSONRequest(t, router, step.method, "/api/v1/users/"+adminID+step.suffix, step.body, manager)
			accessLifecycleError(t, response.Code, response.Body.Bytes(), http.StatusConflict, "superuser_immutable", step.message)
			if platformUserState(t, db, adminID) != beforeAdmin {
				t.Fatal("非自身超级管理员保护仍改变了账号或授权状态")
			}
			response = performJSONRequest(t, router, step.method, "/api/v1/users/"+controlID+step.suffix, step.body, manager)
			if response.Code != http.StatusNoContent {
				t.Fatalf("相同操作者和有效请求未能修改普通对照用户: status=%d body=%s", response.Code, response.Body.String())
			}
			switch step.name {
			case "status":
				stored, err := accounts.FindByID(context.Background(), controlID)
				if err != nil || stored.IsActive {
					t.Fatalf("普通对照账号没有实际停用: err=%v", err)
				}
			case "roles":
				var ids []string
				if err := db.Model(&model.UserRole{}).Where("user_id = ?", controlID).Pluck("role_id", &ids).Error; err != nil || !slices.Equal(ids, []string{readerRole}) {
					t.Fatalf("普通对照账号的角色未实际保存: err=%v", err)
				}
			case "permissions", "access":
				var values []model.UserPermission
				if err := db.Where("user_id = ?", controlID).Order("permission ASC").Find(&values).Error; err != nil || len(values) != 2 {
					t.Fatalf("普通对照账号的允许/拒绝未实际保存: count=%d err=%v", len(values), err)
				}
				wantAllow, wantDeny := access.PermissionUserRead, access.PermissionUserDelete
				if step.name == "access" {
					wantAllow, wantDeny = access.PermissionUserUpdate, access.PermissionUserRead
				}
				for _, value := range values {
					if value.Effect == model.PermissionAllow && value.Permission != wantAllow || value.Effect == model.PermissionDeny && value.Permission != wantDeny {
						t.Fatal("普通对照账号保存了错误的权限内容")
					}
				}
				if step.name == "access" {
					var ids []string
					if err := db.Model(&model.UserRole{}).Where("user_id = ?", controlID).Pluck("role_id", &ids).Error; err != nil || !slices.Equal(ids, []string{readerRole}) {
						t.Fatalf("原子访问配置未同时保存普通账号角色: err=%v", err)
					}
				}
			case "delete":
				var count int64
				if err := db.Model(&model.User{}).Where("uuid = ?", controlID).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("普通对照账号未实际删除: count=%d err=%v", count, err)
				}
			}
			if step.name == "status" || step.name == "delete" {
				if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", controlCookie); response.Code != http.StatusUnauthorized {
					t.Fatalf("停用或删除对照账号后原会话未失效: status=%d", response.Code)
				}
			}
			var count int64
			if err := db.Model(&model.User{}).Where("is_superuser = ? AND is_active = ?", true, true).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("唯一活动超级管理员未被保留: count=%d err=%v", count, err)
			}
			if response := performJSONRequest(t, router, http.MethodGet, "/api/v1/users", nil, admin); response.Code != http.StatusOK {
				t.Fatalf("拒绝修改后原管理员会话不可用: status=%d", response.Code)
			}
		})
	}

	t.Run("department", func(t *testing.T) {
		departmentID := createDepartmentForHTTPTest(t, router, admin, "管理员保护目标部门")
		controlID := createUserForHTTPTest(t, router, admin, "platform-control-department", "", nil)
		controlCookie := loginForDepartmentTest(t, router, "platform-control-department")
		if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", controlCookie); response.Code != http.StatusOK {
			t.Fatalf("调部门对照的原会话前提未满足: status=%d", response.Code)
		}
		body := map[string]string{"department_id": departmentID}
		before := platformUserState(t, db, adminID)
		response := performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+adminID+"/department", body, admin)
		accessLifecycleError(t, response.Code, response.Body.Bytes(), http.StatusConflict, "superuser_immutable", account.ErrSuperuserImmutable.Error())
		if platformUserState(t, db, adminID) != before {
			t.Fatal("调部门保护改变了唯一管理员状态")
		}
		response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+controlID+"/department", body, manager)
		accessLifecycleError(t, response.Code, response.Body.Bytes(), http.StatusForbidden, "department_scope_denied", "只有超级管理员可以调整用户所属部门")
		response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+controlID+"/department", body, admin)
		if response.Code != http.StatusNoContent {
			t.Fatalf("有效普通账号调部门对照失败: status=%d body=%s", response.Code, response.Body.String())
		}
		stored, err := accounts.FindByID(context.Background(), controlID)
		if err != nil || stored.DepartmentID != departmentID {
			t.Fatalf("普通账号部门未实际更新: err=%v", err)
		}
		if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", controlCookie); response.Code != http.StatusUnauthorized {
			t.Fatalf("普通账号调部门后原会话未失效: status=%d", response.Code)
		}
	})
}

func TestReferencedUserDeletionProtectsBothGitCredentialReferencesHTTP(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	roleID := createRoleForHTTPTest(t, router, admin, "credential-delete-manager", []string{access.PermissionUserRead, access.PermissionUserDelete})
	createUserForHTTPTest(t, router, admin, "credential-delete-manager", "", []string{roleID})
	manager := loginForDepartmentTest(t, router, "credential-delete-manager")
	for _, reference := range []string{"credential_id", "api_credential_id"} {
		t.Run(reference, func(t *testing.T) {
			userID := createUserForHTTPTest(t, router, admin, "guard-owner-"+reference, "", []string{roleID})
			userCookie := loginForDepartmentTest(t, router, "guard-owner-"+reference)
			if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", userCookie); response.Code != http.StatusOK {
				t.Fatalf("有引用用户的原会话前提未满足: status=%d", response.Code)
			}
			now := time.Now().UTC()
			credential := model.GitCredential{ID: uuid.NewString(), UserID: userID, Name: "引用保护夹具", Provider: model.GitProviderGitea, AuthType: model.GitAuthToken, SecretCiphertext: "test-encrypted-placeholder", CreatedAt: now, UpdatedAt: now}
			repository := model.GitRepository{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), Name: "引用保护-" + reference, Provider: model.GitProviderGitea, CloneURL: "https://git.example.test/team/repository.git", DefaultBranch: "main", AuthType: model.GitAuthNone, IsActive: true, CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
			if reference == "credential_id" {
				repository.AuthType, repository.CredentialID = model.GitAuthToken, &credential.ID
			} else {
				repository.APICredentialID = &credential.ID
			}
			if err := db.Create(&credential).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&repository).Error; err != nil {
				t.Fatal(err)
			}
			before := platformUserState(t, db, userID)
			response := performJSONRequest(t, router, http.MethodDelete, "/api/v1/users/"+userID, nil, manager)
			accessLifecycleError(t, response.Code, response.Body.Bytes(), http.StatusConflict, "user_credential_in_use", account.ErrUserCredentialUsed.Error())
			if platformUserState(t, db, userID) != before {
				t.Fatal("拒绝删除仍改变了用户、角色、成员或个人凭据")
			}
			if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", userCookie); response.Code != http.StatusOK {
				t.Fatalf("被拒绝的删除使用户原会话失效: status=%d", response.Code)
			}
			// 这里只调整本测试夹具的引用，不把它当作真实仓库凭据交接验收。
			if err := db.Model(&repository).Updates(map[string]any{"credential_id": nil, "api_credential_id": nil, "auth_type": model.GitAuthNone}).Error; err != nil {
				t.Fatal(err)
			}
			response = performJSONRequest(t, router, http.MethodDelete, "/api/v1/users/"+userID, nil, manager)
			if response.Code != http.StatusNoContent {
				t.Fatalf("解除引用后合法删除失败: status=%d body=%s", response.Code, response.Body.String())
			}
			for _, value := range []any{&model.UserRole{}, &model.UserPermission{}, &model.ProjectMember{}, &model.GitCredential{}} {
				var count int64
				if err := db.Model(value).Where("user_id = ?", userID).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("合法删除后用户身份关联仍残留: count=%d err=%v", count, err)
				}
			}
			var stored model.GitRepository
			if err := db.First(&stored, "uuid = ?", repository.ID).Error; err != nil || stored.CreatedBy != userID || stored.DepartmentID != repository.DepartmentID || stored.ProjectID != repository.ProjectID {
				t.Fatalf("删除用户改变或删除了既有业务仓库归属: err=%v", err)
			}
			var roleCount int64
			if err := db.Model(&model.Role{}).Where("uuid = ?", roleID).Count(&roleCount).Error; err != nil || roleCount != 1 {
				t.Fatalf("删除用户错误地删除了共享角色: count=%d err=%v", roleCount, err)
			}
			if response := sessionBoundaryRequest(t, router, "/api/v1/auth/me", userCookie); response.Code != http.StatusUnauthorized {
				t.Fatalf("删除账号后原会话未失效: status=%d", response.Code)
			}
		})
	}
}

func TestUserCredentialReferencesCannotBeHiddenByProjectContextHTTP(t *testing.T) {
	var db *gorm.DB
	router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
	t.Cleanup(cleanup)
	admin := loginForDepartmentTest(t, router, "admin")
	managerRole := createRoleForHTTPTest(t, router, admin, "project-context-user-manager", []string{access.PermissionUserRead, access.PermissionUserDelete})
	managerID := createUserForHTTPTest(t, router, admin, "project-context-user-manager", "", []string{managerRole})
	projectRole := createRoleForHTTPTest(t, router, admin, "project-context-reader", []string{"project.read"})
	defaultProject := database.DefaultProjectID(database.DefaultDepartmentID)
	response := performJSONRequest(t, router, http.MethodPut, "/api/v1/projects/"+defaultProject+"/members/"+managerID, map[string]string{"role_id": projectRole}, admin)
	if response.Code != http.StatusNoContent {
		t.Fatalf("建立当前项目准入前提失败: status=%d body=%s", response.Code, response.Body.String())
	}
	manager := loginForDepartmentTest(t, router, "project-context-user-manager")
	if response := performJSONRequest(t, router, http.MethodGet, "/api/v1/projects/"+defaultProject, nil, manager); response.Code != http.StatusOK {
		t.Fatalf("当前项目必须对操作者真实可访问: status=%d", response.Code)
	}
	now := time.Now().UTC()
	otherProject := model.Project{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, Name: "同部门其他项目引用", IsActive: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&otherProject).Error; err != nil {
		t.Fatal(err)
	}
	targetDepartment := createDepartmentForHTTPTest(t, router, admin, "凭据引用调动目标部门")
	for _, reference := range []string{"credential_id", "api_credential_id"} {
		for _, operation := range []string{"delete", "move"} {
			t.Run(reference+"/"+operation, func(t *testing.T) {
				userID := createUserForHTTPTest(t, router, admin, "cross-"+reference+"-"+operation, "", nil)
				credential := model.GitCredential{ID: uuid.NewString(), UserID: userID, Name: "其他项目引用夹具", Provider: model.GitProviderGitea, AuthType: model.GitAuthToken, SecretCiphertext: "test-encrypted-placeholder", CreatedAt: now, UpdatedAt: now}
				repository := model.GitRepository{ID: uuid.NewString(), DepartmentID: database.DefaultDepartmentID, ProjectID: otherProject.ID, Name: reference + "-" + operation, Provider: model.GitProviderGitea, CloneURL: "https://git.example.test/team/repository.git", DefaultBranch: "main", AuthType: model.GitAuthNone, IsActive: true, CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
				if reference == "credential_id" {
					repository.AuthType, repository.CredentialID = model.GitAuthToken, &credential.ID
				} else {
					repository.APICredentialID = &credential.ID
				}
				if err := db.Create(&credential).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&repository).Error; err != nil {
					t.Fatal(err)
				}
				var storedCredential model.GitCredential
				var storedRepository model.GitRepository
				if err := db.First(&storedCredential, "uuid = ?", credential.ID).Error; err != nil || storedCredential.UserID != userID {
					t.Fatalf("跨项目凭据所有权前提未满足: err=%v", err)
				}
				if err := db.First(&storedRepository, "uuid = ?", repository.ID).Error; err != nil || storedRepository.ProjectID != otherProject.ID || storedRepository.DepartmentID != database.DefaultDepartmentID {
					t.Fatalf("同部门其他项目仓库前提未满足: err=%v", err)
				}
				linked := storedRepository.CredentialID
				if reference == "api_credential_id" {
					linked = storedRepository.APICredentialID
				}
				if linked == nil || *linked != credential.ID {
					t.Fatal("跨项目仓库未真实引用准确的用户凭据")
				}
				before := platformUserState(t, db, userID)
				if operation == "delete" {
					response = performJSONRequest(t, router, http.MethodDelete, "/api/v1/users/"+userID, nil, manager)
				} else {
					response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userID+"/department", map[string]string{"department_id": targetDepartment}, admin)
				}
				var failure errorResponse
				if err := json.Unmarshal(response.Body.Bytes(), &failure); response.Code != http.StatusConflict || err != nil || failure.Code != "user_credential_in_use" {
					var count int64
					if err := db.Model(&model.GitRepository{}).Where("uuid = ?", repository.ID).Count(&count).Error; err != nil {
						t.Fatal(err)
					}
					t.Fatalf("当前项目隐藏了其他项目的凭据引用: operation=%s reference=%s status=%d expected=409 user_state_changed=%v referenced_repository_count=%d", operation, reference, response.Code, platformUserState(t, db, userID) != before, count)
				}
				if platformUserState(t, db, userID) != before {
					t.Fatal("其他项目引用保护未保持账号和凭据状态")
				}
				var afterRepository model.GitRepository
				if err := db.First(&afterRepository, "uuid = ?", repository.ID).Error; err != nil {
					t.Fatal(err)
				}
				beforeJSON, _ := json.Marshal(storedRepository)
				afterJSON, _ := json.Marshal(afterRepository)
				if sha256.Sum256(beforeJSON) != sha256.Sum256(afterJSON) {
					t.Fatal("拒绝账号操作仍改变了另一项目的仓库或凭据引用")
				}
				// 仅解除本测试夹具引用，再用相同操作者与接口确认合法路径没有被一刀切禁止。
				if err := db.Model(&repository).Updates(map[string]any{"credential_id": nil, "api_credential_id": nil, "auth_type": model.GitAuthNone}).Error; err != nil {
					t.Fatal(err)
				}
				if operation == "delete" {
					response = performJSONRequest(t, router, http.MethodDelete, "/api/v1/users/"+userID, nil, manager)
				} else {
					response = performJSONRequest(t, router, http.MethodPut, "/api/v1/users/"+userID+"/department", map[string]string{"department_id": targetDepartment}, admin)
				}
				if response.Code != http.StatusNoContent {
					t.Fatalf("解除其他项目引用后合法账号操作未恢复: operation=%s status=%d body=%s", operation, response.Code, response.Body.String())
				}
				var remaining int64
				if err := db.Model(&model.GitRepository{}).Where("uuid = ? AND project_id = ? AND department_id = ?", repository.ID, otherProject.ID, database.DefaultDepartmentID).Count(&remaining).Error; err != nil || remaining != 1 {
					t.Fatalf("合法账号操作删除或改变了业务仓库归属: count=%d err=%v", remaining, err)
				}
			})
		}
	}
}
