package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

func TestRepositoryAPIUpdateRejectsOwnerMutationBeforePersistHTTP(t *testing.T) {
	for _, operation := range []string{"delete", "move"} {
		t.Run(operation, func(t *testing.T) {
			var db *gorm.DB
			router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
			t.Cleanup(cleanup)
			pool, err := db.DB()
			if err != nil {
				t.Fatal("读取本例连接池失败")
			}
			admin := loginForDepartmentTest(t, router, "admin")
			roleID := createRoleForHTTPTest(t, router, admin, "a02-http-api-update-"+operation, []string{
				access.PermissionRepositoryRead, access.PermissionRepositoryCreate, access.PermissionRepositoryUpdate,
				access.PermissionCredentialRead, access.PermissionCredentialCreate,
			})
			username := "a02-http-api-owner-" + operation
			ownerID := createUserForHTTPTest(t, router, admin, username, "", []string{roleID})
			owner := loginForDepartmentTest(t, router, username)
			targetDepartmentID := ""
			if operation == "move" {
				targetDepartmentID = createDepartmentForHTTPTest(t, router, admin, "a02-http-api-target")
			}
			body := map[string]any{
				"name": "a02-http-api-original-" + operation, "provider": "gitea", "auth_type": "none",
				"clone_url": "https://example.invalid/a02/http-update.git", "default_branch": "main", "webhook_enabled": false,
			}
			created := performJSONRequest(t, router, http.MethodPost, "/api/v1/repositories", body, owner)
			var repositoryPayload struct {
				Repository struct {
					ID string `json:"id"`
				} `json:"repository"`
			}
			if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &repositoryPayload) != nil {
				t.Fatalf("正常创建无凭据仓库前提失败: status=%d", created.Code)
			}
			repositoryID := repositoryPayload.Repository.ID
			if _, err := uuid.Parse(repositoryID); err != nil {
				t.Fatal("创建仓库没有返回有效标识")
			}
			createdCredential := performJSONRequest(t, router, http.MethodPost, "/api/v1/git-credentials", map[string]any{
				"name": "a02-http-api-token", "provider": "gitea", "auth_type": "token",
				"secret": "a02-synthetic-http-api-token-not-a-real-secret",
			}, owner)
			var credentialPayload struct {
				Credential struct {
					ID string `json:"id"`
				} `json:"credential"`
			}
			if createdCredential.Code != http.StatusCreated || json.Unmarshal(createdCredential.Body.Bytes(), &credentialPayload) != nil {
				t.Fatalf("正常创建个人API凭据前提失败: status=%d", createdCredential.Code)
			}
			credentialID := credentialPayload.Credential.ID
			if _, err := uuid.Parse(credentialID); err != nil {
				t.Fatal("创建凭据没有返回有效标识")
			}
			var beforeRepository model.GitRepository
			var beforeCredential model.GitCredential
			var beforeOwner, adminUser model.User
			for _, read := range []struct {
				value any
				id    string
			}{{&beforeRepository, repositoryID}, {&beforeCredential, credentialID}, {&beforeOwner, ownerID}} {
				if err := db.First(read.value, "uuid = ?", read.id).Error; err != nil {
					t.Fatal("读取本例完整行前提失败")
				}
			}
			if err := db.First(&adminUser, "username = ?", "admin").Error; err != nil {
				t.Fatal("读取本例管理员标识失败")
			}
			if beforeRepository.CredentialID != nil || beforeRepository.APICredentialID != nil || beforeRepository.AuthType != model.GitAuthNone || beforeCredential.UserID != ownerID || beforeCredential.Provider != model.GitProviderGitea || beforeCredential.AuthType != model.GitAuthToken {
				t.Fatal("本例必须从无引用仓库新增API引用，不能借clone分支触发保护")
			}
			count := func(value any, column, id string) int64 {
				t.Helper()
				var rows int64
				query := db.Model(value)
				if column != "" {
					query = query.Where(column+" = ?", id)
				}
				if err := query.Count(&rows).Error; err != nil {
					t.Fatal("读取本例精确计数失败")
				}
				return rows
			}
			beforeUsers, beforeCredentials, beforeRepositories := count(&model.User{}, "", ""), count(&model.GitCredential{}, "", ""), count(&model.GitRepository{}, "", "")
			relations := []any{&model.UserRole{}, &model.UserPermission{}, &model.ProjectMember{}, &model.ExternalIdentity{}}
			beforeRelations := make([]int64, len(relations))
			for index, value := range relations {
				beforeRelations[index] = count(value, "user_id", ownerID)
			}
			if beforeRepositories != 1 {
				t.Fatal("本例必须只有一个原仓库")
			}
			body["name"], body["api_credential_id"] = "a02-http-api-new-reference-"+operation, credentialID
			payload, err := json.Marshal(body)
			if err != nil {
				t.Fatal("序列化本例公开仓库参数失败")
			}
			putContext, cancelPut := context.WithTimeout(context.Background(), 15*time.Second)
			mutationContext, cancelMutation := context.WithTimeout(context.Background(), 15*time.Second)
			requestContext := context.WithValue(putContext, repositoryAPIUpdateOwnerFirstKey{}, credentialID)
			putRequest := httptest.NewRequest(http.MethodPut, "/api/v1/repositories/"+repositoryID, bytes.NewReader(payload)).WithContext(requestContext)
			putRequest.Header.Set("Content-Type", "application/json")
			putRequest.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
			putID, mutationID := uuid.NewString(), uuid.NewString()
			putRequest.Header.Set("X-Request-ID", putID)
			putRequest.AddCookie(owner)
			mutationMethod, mutationPath, mutationAction := http.MethodDelete, "/api/v1/users/"+ownerID, "user.delete"
			var mutationBody []byte
			if operation == "move" {
				mutationMethod, mutationPath, mutationAction = http.MethodPut, mutationPath+"/department", "user.department.update"
				mutationBody, err = json.Marshal(map[string]string{"department_id": targetDepartmentID})
				if err != nil {
					t.Fatal("序列化本例部门参数失败")
				}
			}
			mutationRequest := httptest.NewRequest(mutationMethod, mutationPath, bytes.NewReader(mutationBody)).WithContext(mutationContext)
			mutationRequest.Header.Set("Content-Type", "application/json")
			mutationRequest.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
			mutationRequest.Header.Set("X-Request-ID", mutationID)
			mutationRequest.AddCookie(admin)
			putResponse, mutationResponse := httptest.NewRecorder(), httptest.NewRecorder()
			entered, invalidGate, release := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
			putDone, mutationDone := make(chan struct{}), make(chan struct{})
			var hits atomic.Int32
			var noTransaction atomic.Bool
			var inUseAtGate atomic.Int64
			putLaunched, mutationLaunched := false, false
			callback := "test:a02_http_api_update_owner_first_" + operation
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Context.Value(repositoryAPIUpdateOwnerFirstKey{}) != credentialID || tx.Error != nil || tx.RowsAffected != 1 || len(tx.Statement.Selects) != 0 {
					return
				}
				value, ok := tx.Statement.Dest.(*model.GitCredential)
				if !ok || tx.Statement.Table != "git_credentials" || value.ID != credentialID || value.UserID != ownerID || hits.Add(1) != 1 {
					return
				}
				// 只暂停本次正式FindOwned：Rows已关闭，不持事务、连接或凭据锁。
				_, inTransaction := tx.Statement.ConnPool.(gorm.TxCommitter)
				noTransaction.Store(!inTransaction)
				inUseAtGate.Store(int64(pool.Stats().InUse))
				if inTransaction || inUseAtGate.Load() != 0 {
					invalidGate <- struct{}{}
					tx.AddError(errors.New("测试读取仍占事务或连接，交错前提不成立"))
					return
				}
				entered <- struct{}{}
				select {
				case <-release:
				case <-tx.Statement.Context.Done():
					tx.AddError(tx.Statement.Context.Err())
				}
			}); err != nil {
				cancelPut()
				cancelMutation()
				t.Fatal("注册本例查询检测点失败")
			}
			t.Cleanup(func() {
				// 异常清理只取消，不关闭放行门；未确认前提时不得让旧PUT继续写入。
				cancelPut()
				cancelMutation()
				closed := true
				for _, task := range []struct {
					launched bool
					done     <-chan struct{}
				}{{putLaunched, putDone}, {mutationLaunched, mutationDone}} {
					if task.launched {
						select {
						case <-task.done:
						case <-time.After(5 * time.Second):
							closed = false
							t.Error("本例请求未确认退出，不能声称清理完成")
						}
					}
				}
				if closed {
					if err := db.Callback().Query().Remove(callback); err != nil {
						t.Error("移除本例查询检测点失败")
					}
				}
			})
			putLaunched = true
			go func() { defer close(putDone); router.ServeHTTP(putResponse, putRequest) }()
			select {
			case <-entered:
			case <-invalidGate:
				t.Fatal("检测点仍持事务或连接，不把测试前提失败当产品问题")
			case <-putDone:
				t.Fatalf("原owner的PUT未到达FindOwned检测点: status=%d", putResponse.Code)
			case <-time.After(5 * time.Second):
				t.Fatal("未确认PUT读取检测点，不把超时当引用保护问题")
			}
			mutationLaunched = true
			go func() { defer close(mutationDone); router.ServeHTTP(mutationResponse, mutationRequest) }()
			select {
			case <-mutationDone:
			case <-time.After(5 * time.Second):
				t.Fatal("管理员请求未先闭合，不把锁等待当产品问题")
			}
			if mutationResponse.Code != http.StatusNoContent {
				t.Fatalf("无既有引用时的账户请求前提失败: operation=%s status=%d", operation, mutationResponse.Code)
			}
			var committedOwner model.User
			assertAccountState := func() {
				t.Helper()
				if operation == "delete" {
					if count(&model.User{}, "uuid", ownerID) != 0 || count(&model.GitCredential{}, "uuid", credentialID) != 0 || count(&model.GitCredential{}, "user_id", ownerID) != 0 || count(&model.User{}, "", "") != beforeUsers-1 || count(&model.GitCredential{}, "", "") != beforeCredentials-1 {
						t.Fatal("删除回执与用户、凭据真实删除或全表计数不符")
					}
				} else {
					var current model.User
					if err := db.First(&current, "uuid = ?", ownerID).Error; err != nil {
						t.Fatal("调部门后原用户不存在")
					}
					expected := beforeOwner
					expected.DepartmentID, expected.AuthVersion, expected.UpdatedAt = targetDepartmentID, beforeOwner.AuthVersion+1, current.UpdatedAt
					if !reflect.DeepEqual(expected, current) || current.UpdatedAt.Before(beforeOwner.UpdatedAt) || count(&model.User{}, "", "") != beforeUsers || count(&model.GitCredential{}, "", "") != beforeCredentials {
						t.Fatal("调部门后的完整用户、AuthVersion或全表计数不符")
					}
					if committedOwner.ID != "" && !reflect.DeepEqual(committedOwner, current) {
						t.Fatal("被拒PUT改变了已提交的用户行或时间")
					}
					committedOwner = current
					var currentCredential model.GitCredential
					if err := db.First(&currentCredential, "uuid = ?", credentialID).Error; err != nil || !reflect.DeepEqual(beforeCredential, currentCredential) {
						t.Fatal("调部门或被拒PUT改变、丢失了完整API凭据")
					}
				}
				for index, value := range relations {
					expected := beforeRelations[index]
					if operation == "delete" {
						expected = 0
					}
					if count(value, "user_id", ownerID) != expected {
						t.Fatal("账户操作或被拒PUT的身份关联计数不符")
					}
				}
			}
			assertAccountState()
			oldMe := performJSONRequest(t, router, http.MethodGet, "/api/v1/auth/me", nil, owner)
			if oldMe.Code != http.StatusUnauthorized {
				t.Fatalf("账户变更后旧会话未失效: status=%d", oldMe.Code)
			}
			close(release)
			select {
			case <-putDone:
			case <-time.After(5 * time.Second):
				t.Fatal("放行后原PUT未确认退出，不把等待当产品问题")
			}
			var rejected errorResponse
			if putResponse.Code != http.StatusBadRequest || json.Unmarshal(putResponse.Body.Bytes(), &rejected) != nil || rejected.Code != "invalid_credential" || rejected.RequestID != putID {
				t.Fatalf("已失效API引用没有在正常边界安全拒绝: operation=%s status=%d", operation, putResponse.Code)
			}
			var afterRepository model.GitRepository
			if err := db.First(&afterRepository, "uuid = ?", repositoryID).Error; err != nil || !reflect.DeepEqual(beforeRepository, afterRepository) || count(&model.GitRepository{}, "", "") != beforeRepositories {
				t.Fatal("被拒PUT改变、删除旧仓库完整数据/时间，或新增了仓库")
			}
			assertAccountState()
			for _, expected := range []struct {
				request, actor, action, resourceType, resourceID, method, path string
				status                                                         int
				result                                                         model.AuditResult
			}{{putID, ownerID, "repository.update", "repository", repositoryID, http.MethodPut, "/api/v1/repositories/:id", http.StatusBadRequest, model.AuditFailed}, {mutationID, adminUser.ID, mutationAction, "user", ownerID, mutationMethod, "/api/v1/users/:id", http.StatusNoContent, model.AuditSucceeded}} {
				if operation == "move" && expected.request == mutationID {
					expected.path += "/department"
				}
				var rows []model.AuditLog
				if err := db.Where("request_id = ?", expected.request).Find(&rows).Error; err != nil || len(rows) != 1 {
					t.Fatal("本例请求缺少唯一真实审计，或出现重复记录")
				}
				entry := rows[0]
				var metadata struct {
					Method string `json:"method"`
					Path   string `json:"path"`
					Status int    `json:"status"`
				}
				if entry.ActorUserID == nil || *entry.ActorUserID != expected.actor || entry.Action != expected.action || entry.ResourceType != expected.resourceType || entry.ResourceID != expected.resourceID || entry.Result != expected.result || json.Unmarshal(entry.Metadata, &metadata) != nil || metadata.Method != expected.method || metadata.Path != expected.path || metadata.Status != expected.status {
					t.Fatal("真实审计的操作者、目标、结果或安全请求元数据不符")
				}
			}
			if hits.Load() != 1 || !noTransaction.Load() || inUseAtGate.Load() != 0 || pool.Stats().InUse != 0 {
				t.Fatal("检测点次数、无事务、连接释放前提不成立")
			}
			t.Logf("正常HTTP交错闭合: operation=%s account_status=%d update_status=%d gate_hits=1 gate_no_transaction=true gate_pool_in_use=0 account_committed_before_release=true repository_full_unchanged=true api_only=true account_state_verified=true audit_requests=2 both_requests_closed=true final_pool_in_use=0", operation, mutationResponse.Code, putResponse.Code)
		})
	}
}

type repositoryAPIUpdateOwnerFirstKey struct{}
