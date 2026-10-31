package repository

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/account"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/model"
)

func TestRepositoryPersistRechecksOwnerDepartmentAfterNormalization(t *testing.T) {
	for _, scopeKind := range []string{"ordinary", "all", "none"} {
		t.Run(scopeKind, func(t *testing.T) {
			s, db := newRepositoryTestService(t)
			ctx := context.Background()
			if scopeKind != "none" {
				ctx = database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: "owner", DepartmentID: database.DefaultDepartmentID,
					ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), AllDepartments: scopeKind == "all"})
			}
			token := "synthetic-reference-test-token"
			selected, err := s.credentials.Create(context.Background(), "owner", credential.Input{Name: "department token", Provider: model.GitProviderGeneric, AuthType: model.GitAuthToken, Secret: &token})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			department := model.Department{ID: uuid.NewString(), Name: "本例新部门", IsActive: true, CreatedAt: now, UpdatedAt: now}
			if err := db.Create(&department).Error; err != nil {
				t.Fatal(err)
			}
			var changed atomic.Bool
			var moveErr error
			callback := "test:move_credential_owner_after_read"
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				value, ok := tx.Statement.Dest.(*model.GitCredential)
				if !ok || value.ID != selected.ID || tx.Error != nil {
					return
				}
				if _, locked := tx.Statement.ConnPool.(gorm.TxCommitter); locked || changed.Swap(true) {
					return
				}
				// 正式无锁读取已结束，使用正常业务接口完成调动，不在事务内注入状态。
				moveErr = account.NewService(db).SetDepartment(context.Background(), "owner", department.ID)
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
			repo, _, err := s.Create(ctx, "owner", Input{Name: "department-reference", Provider: model.GitProviderGeneric,
				CloneURL: "https://example.invalid/a.git", AuthType: model.GitAuthToken, CredentialID: &selected.ID})
			if !changed.Load() || moveErr != nil {
				t.Fatalf("正常调部门交错未成立: changed=%t err=%v", changed.Load(), moveErr)
			}
			if scopeKind == "ordinary" {
				var count int64
				if queryErr := db.Model(&model.GitRepository{}).Where("name = ?", "department-reference").Count(&count).Error; queryErr != nil || count != 0 || !errors.Is(err, ErrInvalidCredential) {
					t.Fatalf("原部门请求仍绑定迁出用户的凭据: count=%d err=%v query_err=%v", count, err, queryErr)
				}
			} else if err != nil || repo == nil {
				t.Fatalf("无普通部门限制时误拒绝既有凭据: scope=%s err=%v", scopeKind, err)
			}
		})
	}
}

func TestRepositoryStaleEditorCannotRestoreOtherOwnersCredential(t *testing.T) {
	for _, replacement := range []string{"clear", "rebind"} {
		t.Run(replacement, func(t *testing.T) {
			s, db := newRepositoryTestService(t)
			ctx := context.Background()
			token := "synthetic-stale-reference-token"
			original, err := s.credentials.Create(ctx, "token-owner", credential.Input{Name: "original token", Provider: model.GitProviderGeneric, AuthType: model.GitAuthToken, Secret: &token})
			if err != nil {
				t.Fatal(err)
			}
			other, err := s.credentials.Create(ctx, "different-editor", credential.Input{Name: "replacement token", Provider: model.GitProviderGeneric, AuthType: model.GitAuthToken, Secret: &token})
			if err != nil {
				t.Fatal(err)
			}
			input := Input{Name: "stale-reference", Provider: model.GitProviderGeneric, CloneURL: "https://example.invalid/a.git", AuthType: model.GitAuthToken, CredentialID: &original.ID}
			repo, _, err := s.Create(ctx, "token-owner", input)
			if err != nil {
				t.Fatal(err)
			}
			input.CredentialID = nil
			var changed atomic.Bool
			var updateErr error
			callback := "test:replace_reference_after_repository_read"
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				value, ok := tx.Statement.Dest.(*model.GitRepository)
				if !ok || value.ID != repo.ID || tx.Error != nil {
					return
				}
				if _, locked := tx.Statement.ConnPool.(gorm.TxCommitter); locked || changed.Swap(true) {
					return
				}
				newInput := input
				newInput.Name = "newer-reference"
				if replacement == "clear" {
					newInput.AuthType = model.GitAuthNone
				} else {
					newInput.CredentialID = &other.ID
				}
				_, _, updateErr = s.Update(ctx, "different-editor", repo.ID, newInput)
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
			_, _, err = s.Update(ctx, "different-editor", repo.ID, input)
			if !changed.Load() || updateErr != nil || !errors.Is(err, ErrRepositoryChanged) {
				t.Fatalf("陈旧更新未按真实改绑结果拒绝: changed=%t update_err=%v err=%v", changed.Load(), updateErr, err)
			}
			var current model.GitRepository
			if err := db.First(&current, "uuid = ?", repo.ID).Error; err != nil || current.Name != "newer-reference" || referenceValue(current.CredentialID) == original.ID {
				t.Fatalf("陈旧更新恢复了他人的旧凭据: err=%v", err)
			}
			if replacement == "rebind" && referenceValue(current.CredentialID) != other.ID || replacement == "clear" && current.CredentialID != nil {
				t.Fatal("并发完成的解除或重绑被覆盖")
			}
		})
	}
}

func TestRepositoryRetainsInactiveOwnersExistingCloneReference(t *testing.T) {
	s, db := newRepositoryTestService(t)
	token := "synthetic-retained-reference-token"
	selected, err := s.credentials.Create(context.Background(), "token-owner", credential.Input{Name: "retained token", Provider: model.GitProviderGeneric, AuthType: model.GitAuthToken, Secret: &token})
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Name: "retained-reference", Provider: model.GitProviderGeneric, CloneURL: "https://example.invalid/a.git", AuthType: model.GitAuthToken, CredentialID: &selected.ID}
	repo, _, err := s.Create(context.Background(), "token-owner", input)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.User{}).Where("uuid = ?", "token-owner").Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	input.CredentialID = nil
	input.Name = "retained-renamed"
	updated, _, err := s.Update(context.Background(), "different-editor", repo.ID, input)
	if err != nil || referenceValue(updated.CredentialID) != selected.ID {
		t.Fatalf("既有合法保留路径被新增停用限制改变: err=%v", err)
	}
}

func TestRepositoryStaleUpdateCannotOverwriteSecurityConfiguration(t *testing.T) {
	for _, field := range []string{"insecure_http", "webhook"} {
		t.Run(field, func(t *testing.T) {
			s, db := newRepositoryTestService(t)
			ctx := context.Background()
			input := Input{Name: "security-reference", Provider: model.GitProviderGeneric, CloneURL: "https://example.invalid/a.git", AuthType: model.GitAuthNone}
			repo, _, err := s.Create(ctx, "admin", input)
			if err != nil {
				t.Fatal(err)
			}
			var changed atomic.Bool
			var updateErr error
			callback := "test:replace_repository_security_after_read"
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				value, ok := tx.Statement.Dest.(*model.GitRepository)
				if !ok || value.ID != repo.ID || tx.Error != nil {
					return
				}
				if _, locked := tx.Statement.ConnPool.(gorm.TxCommitter); locked || changed.Swap(true) {
					return
				}
				newInput := input
				newInput.AllowInsecureHTTP = field == "insecure_http"
				newInput.WebhookEnabled = field == "webhook"
				_, _, updateErr = s.Update(ctx, "admin", repo.ID, newInput)
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
			_, _, err = s.Update(ctx, "admin", repo.ID, input)
			if !changed.Load() || updateErr != nil || !errors.Is(err, ErrRepositoryChanged) {
				t.Fatalf("陈旧编辑未拒绝安全配置覆盖: field=%s changed=%t update_err=%v err=%v", field, changed.Load(), updateErr, err)
			}
			var current model.GitRepository
			if err := db.First(&current, "uuid = ?", repo.ID).Error; err != nil || current.AllowInsecureHTTP != (field == "insecure_http") || current.WebhookEnabled != (field == "webhook") || field == "webhook" && current.WebhookSecretCiphertext == "" {
				t.Fatalf("并发完成的安全配置被陈旧请求覆盖: field=%s err=%v", field, err)
			}
		})
	}
}
