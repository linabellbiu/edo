package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

func TestUserDeletionCannotLeaveConcurrentRepositoryCredentialReferenceHTTP(t *testing.T) {
	for _, reference := range []string{"credential_id", "api_credential_id"} {
		t.Run(reference, func(t *testing.T) {
			var db *gorm.DB
			router, cleanup := newAuthTestRouter(t, func(_ *Dependencies, value *gorm.DB) { db = value })
			t.Cleanup(cleanup)
			pool, err := db.DB()
			if err != nil {
				t.Fatalf("读取本例连接池失败: %v", err)
			}
			admin := loginForDepartmentTest(t, router, "admin")
			roleID := createRoleForHTTPTest(t, router, admin, "a02-concurrent-reference", []string{
				access.PermissionRepositoryRead, access.PermissionRepositoryCreate,
				access.PermissionCredentialRead, access.PermissionCredentialCreate,
			})
			ownerID := createUserForHTTPTest(t, router, admin, "a02-concurrent-owner", "", []string{roleID})
			owner := loginForDepartmentTest(t, router, "a02-concurrent-owner")
			created := performJSONRequest(t, router, http.MethodPost, "/api/v1/git-credentials", map[string]any{
				"name": "a02-concurrent-token", "provider": "gitea", "auth_type": "token",
				"secret": "a02-synthetic-token-not-a-real-secret",
			}, owner)
			var credentialPayload struct {
				Credential struct {
					ID string `json:"id"`
				} `json:"credential"`
			}
			if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &credentialPayload) != nil {
				t.Fatalf("创建本例个人凭据前提失败: status=%d", created.Code)
			}
			credentialID := credentialPayload.Credential.ID
			if _, err := uuid.Parse(credentialID); err != nil {
				t.Fatal("创建凭据没有返回有效标识")
			}
			repositoryName := "a02-concurrent-" + reference
			body := map[string]any{
				"name": repositoryName, "provider": "gitea", "auth_type": "none",
				"clone_url": "https://example.invalid/a02/repository.git", "webhook_enabled": false,
				reference: credentialID,
			}
			if reference == "credential_id" {
				body["auth_type"] = "token"
			}
			payload, err := json.Marshal(body)
			if err != nil {
				t.Fatal("序列化本例仓库请求失败")
			}
			postContext, cancelPost := context.WithTimeout(context.Background(), 10*time.Second)
			requestContext := context.WithValue(postContext, userCredentialReferenceRequestKey{}, credentialID)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/repositories", bytes.NewReader(payload)).WithContext(requestContext)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-EDO-Project-ID", database.DefaultProjectID(database.DefaultDepartmentID))
			request.AddCookie(owner)
			response := httptest.NewRecorder()
			entered := make(chan struct{}, 1)
			preconditionChanged := make(chan struct{}, 1)
			release := make(chan struct{})
			postDone := make(chan struct{})
			var releaseOnce sync.Once
			var gateHits atomic.Int32
			launched := false
			callback := "test:a02_credential_query_after_" + reference
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Context.Value(userCredentialReferenceRequestKey{}) != credentialID || tx.Error != nil || tx.RowsAffected != 1 {
					return
				}
				selected, ok := tx.Statement.Dest.(*model.GitCredential)
				if !ok || tx.Statement.Table != "git_credentials" || selected.ID != credentialID || selected.UserID != ownerID || gateHits.Add(1) != 1 {
					return
				}
				// gorm:query 返回前已自然关闭 Rows；只暂停无事务、无占用连接的正式 FindOwned 读取。
				_, inTransaction := tx.Statement.ConnPool.(gorm.TxCommitter)
				if inTransaction || pool.Stats().InUse != 0 {
					preconditionChanged <- struct{}{}
					tx.AddError(errors.New("测试凭据查询仍占用事务或连接，不能按原交错继续"))
					return
				}
				entered <- struct{}{}
				select {
				case <-release:
				case <-tx.Statement.Context.Done():
					tx.AddError(tx.Statement.Context.Err())
				}
			}); err != nil {
				cancelPost()
				t.Fatalf("注册本例交错检测点失败: %v", err)
			}
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				cancelPost()
				if launched {
					select {
					case <-postDone:
					case <-time.After(15 * time.Second):
						t.Error("本例仓库请求未确认退出，不能认为清理完成")
						return
					}
				}
				if err := db.Callback().Query().Remove(callback); err != nil {
					t.Errorf("移除本例交错检测点失败: %v", err)
				}
			})
			launched = true
			go func() {
				defer close(postDone)
				router.ServeHTTP(response, request)
			}()
			select {
			case <-entered:
			case <-preconditionChanged:
				t.Fatal("测试交错前提已变化，不把事务或连接等待当引用保护漏洞")
			case <-postDone:
				t.Fatalf("仓库请求未进入检测点: status=%d", response.Code)
			case <-time.After(5 * time.Second):
				t.Fatal("未确认仓库落库前检测点，不把超时当引用保护漏洞")
			}
			count := func(value any, column, id string) int64 {
				t.Helper()
				var rows int64
				if err := db.Model(value).Where(column+" = ?", id).Count(&rows).Error; err != nil {
					t.Fatalf("读取本例准确对象状态失败: %v", err)
				}
				return rows
			}
			if count(&model.User{}, "uuid", ownerID) != 1 || count(&model.GitCredential{}, "uuid", credentialID) != 1 || count(&model.GitRepository{}, "name", repositoryName) != 0 {
				t.Fatal("检测点前的用户、凭据或仓库状态不满足交错前提")
			}
			deleted := performJSONRequest(t, router, http.MethodDelete, "/api/v1/users/"+ownerID, nil, admin)
			if deleted.Code != http.StatusNoContent {
				t.Fatalf("无已有引用时的正常删除未完成，不能判断后续引用竞态: status=%d", deleted.Code)
			}
			if count(&model.User{}, "uuid", ownerID) != 0 || count(&model.GitCredential{}, "uuid", credentialID) != 0 {
				t.Fatal("正常删除回执后用户或凭据仍存在，交错前提不成立")
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case <-postDone:
			case <-time.After(5 * time.Second):
				t.Fatal("释放检测点后仓库请求未结束，不把锁等待当引用保护漏洞")
			}
			var stored []model.GitRepository
			if err := db.Where("name = ? AND project_id = ?", repositoryName, database.DefaultProjectID(database.DefaultDepartmentID)).Find(&stored).Error; err != nil {
				t.Fatalf("读取本例仓库最终状态失败: %v", err)
			}
			if response.Code == http.StatusCreated {
				var createdRepository struct {
					Repository struct {
						ID string `json:"id"`
					} `json:"repository"`
				}
				if json.Unmarshal(response.Body.Bytes(), &createdRepository) != nil || len(stored) != 1 || stored[0].ID != createdRepository.Repository.ID {
					t.Fatal("成功创建回执与本例唯一仓库记录不一致")
				}
				if _, err := uuid.Parse(createdRepository.Repository.ID); err != nil {
					t.Fatal("成功创建回执缺少有效仓库标识")
				}
			}
			dangling := false
			for _, repository := range stored {
				selected := repository.CredentialID
				if reference == "api_credential_id" {
					selected = repository.APICredentialID
				}
				dangling = dangling || selected != nil && *selected == credentialID && count(&model.GitCredential{}, "uuid", credentialID) == 0
			}
			t.Logf("既定交错完成: delete_status=%d repository_status=%d repository_count=%d selected_credential_missing=%t gate_hits=%d", deleted.Code, response.Code, len(stored), dangling, gateHits.Load())
			// 既有业务 CreatedBy 保留已删除账号是合法历史；这里只禁止新增不存在凭据的引用。
			if dangling {
				t.Fatalf("删除完成后仍新增悬空的所选凭据引用: reference=%s repository_status=%d", reference, response.Code)
			}
			if response.Code < http.StatusBadRequest || response.Code >= http.StatusInternalServerError || len(stored) != 0 {
				t.Fatalf("删除完成后的新绑定未安全拒绝: status=%d repository_count=%d", response.Code, len(stored))
			}
		})
	}
}

type userCredentialReferenceRequestKey struct{}
