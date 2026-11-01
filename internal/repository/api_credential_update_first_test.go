package repository

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"edo/internal/access"
	"edo/internal/account"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/model"
)

func TestRepositoryAPIUpdateCommittedBeforeDeleteUser(t *testing.T) {
	testRepositoryAPIUpdateCommittedBeforeAccountMutation(t, "delete")
}

func TestRepositoryAPIUpdateCommittedBeforeSetDepartment(t *testing.T) {
	testRepositoryAPIUpdateCommittedBeforeAccountMutation(t, "move")
}

// 只验证正常更新先提交的顺序，不把无重叠的数据库请求称为锁竞争。
func testRepositoryAPIUpdateCommittedBeforeAccountMutation(t *testing.T, operation string) {
	t.Helper()
	s, db := newRepositoryTestService(t)
	accounts := account.NewService(db)
	owner, err := accounts.CreateUser(context.Background(), "a02-api-first-"+operation, "本例API引用所有者", "synthetic-password-only-123")
	if err != nil {
		t.Fatal(err)
	}
	var mutate func(context.Context) error
	switch operation {
	case "delete":
		permissions, err := access.NewService(db)
		if err != nil {
			t.Fatal(err)
		}
		mutate = func(ctx context.Context) error { return permissions.DeleteUser(ctx, owner.ID) }
	case "move":
		now := time.Now().UTC()
		target := model.Department{ID: uuid.NewString(), Name: "本例API引用调入部门", IsActive: true, CreatedAt: now, UpdatedAt: now}
		if err := db.Create(&target).Error; err != nil {
			t.Fatal(err)
		}
		mutate = func(ctx context.Context) error { return accounts.SetDepartment(ctx, owner.ID, target.ID) }
	default:
		t.Fatal("未登记的账户变更")
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		UserID: owner.ID, DepartmentID: owner.DepartmentID, ProjectID: database.DefaultProjectID(owner.DepartmentID),
	})
	input := Input{Name: "a02-api-first-original", Provider: model.GitProviderGitea, CloneURL: "https://example.invalid/a02/api-first.git", DefaultBranch: "main", AuthType: model.GitAuthNone}
	repository, _, err := s.Create(ctx, owner.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	token := "a02-synthetic-api-token-not-a-real-secret"
	selected, err := s.credentials.Create(context.Background(), owner.ID, credential.Input{
		Name: "本例先提交API令牌", Provider: model.GitProviderGitea, AuthType: model.GitAuthToken, Secret: &token,
	})
	if err != nil {
		t.Fatal(err)
	}
	var beforeRepository model.GitRepository
	var beforeCredential model.GitCredential
	var beforeOwner model.User
	readRows := func(repo *model.GitRepository, cred *model.GitCredential, user *model.User) {
		t.Helper()
		if err := db.First(repo, "uuid = ?", repository.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(cred, "uuid = ?", selected.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(user, "uuid = ?", owner.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	readRows(&beforeRepository, &beforeCredential, &beforeOwner)
	counts := func() []int64 {
		t.Helper()
		var rows []int64
		for _, value := range []any{&model.GitRepository{}, &model.GitCredential{}, &model.User{}, &model.Department{}, &model.UserRole{}, &model.UserPermission{}, &model.ProjectMember{}, &model.ExternalIdentity{}} {
			var count int64
			if err := db.Model(value).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			rows = append(rows, count)
		}
		return rows
	}
	beforeCounts := counts()
	if beforeRepository.CredentialID != nil || beforeRepository.APICredentialID != nil || input.AuthType != model.GitAuthNone || input.CredentialID != nil {
		t.Fatal("本例必须只新增API凭据引用，不能借clone引用触发保护")
	}
	input.Name, input.APICredentialID = "a02-api-first-bound", &selected.ID
	updateContext, cancelUpdate := context.WithTimeout(ctx, 15*time.Second)
	mutationContext, cancelMutation := context.WithTimeout(context.Background(), 15*time.Second)
	updateDone, mutationDone := make(chan struct{}), make(chan struct{})
	allowMutation := make(chan struct{})
	var updated *model.GitRepository
	var updateErr, mutationErr error
	t.Cleanup(func() {
		cancelUpdate()
		cancelMutation()
		for _, done := range []<-chan struct{}{updateDone, mutationDone} {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("本例goroutine未确认退出，不能声称清理完成")
			}
		}
	})
	go func() {
		defer close(updateDone)
		updated, _, updateErr = s.Update(updateContext, owner.ID, repository.ID, input)
	}()
	go func() {
		defer close(mutationDone)
		select {
		case <-allowMutation:
			mutationErr = mutate(mutationContext)
		case <-mutationContext.Done():
			mutationErr = mutationContext.Err()
		}
	}()
	select {
	case <-updateDone:
	case <-time.After(5 * time.Second):
		t.Fatal("正常仓库更新未完成，不把超时当产品引用缺陷")
	}
	if updateErr != nil || updated == nil || updated.ID != repository.ID || updated.APICredentialID == nil || *updated.APICredentialID != selected.ID {
		t.Fatalf("正常API引用更新前提失败: err=%v", updateErr)
	}
	// 以正常服务返回后的独立读取证明引用已提交，之后才放行账户操作。
	var committed model.GitRepository
	var committedCredential model.GitCredential
	var committedOwner model.User
	readRows(&committed, &committedCredential, &committedOwner)
	expected := beforeRepository
	expected.Name, expected.APICredentialID, expected.UpdatedAt = input.Name, &selected.ID, committed.UpdatedAt
	if !reflect.DeepEqual(expected, committed) || committed.UpdatedAt.Before(beforeRepository.UpdatedAt) || !reflect.DeepEqual(beforeCredential, committedCredential) || !reflect.DeepEqual(beforeOwner, committedOwner) || !reflect.DeepEqual(beforeCounts, counts()) {
		t.Fatal("API引用未按预期持久化，或正常更新影响了其它数据")
	}
	close(allowMutation)
	select {
	case <-mutationDone:
	case <-time.After(5 * time.Second):
		t.Fatal("账户操作未完成，不把锁等待当产品引用缺陷")
	}
	if !errors.Is(mutationErr, account.ErrUserCredentialUsed) {
		t.Fatalf("已提交API引用未阻止账户变更: operation=%s err=%v", operation, mutationErr)
	}
	var afterRepository model.GitRepository
	var afterCredential model.GitCredential
	var afterOwner model.User
	readRows(&afterRepository, &afterCredential, &afterOwner)
	if !reflect.DeepEqual(committed, afterRepository) || !reflect.DeepEqual(beforeCredential, afterCredential) || !reflect.DeepEqual(beforeOwner, afterOwner) || !reflect.DeepEqual(beforeCounts, counts()) {
		t.Fatal("被拒绝的账户操作改变了仓库、凭据、用户或关联计数")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if pool.Stats().InUse != 0 {
		t.Fatal("本例结束仍占用数据库连接")
	}
	t.Logf("正常反向顺序闭合: operation=%s update_committed_and_independently_read=true account_rejected=true repository_credential_user_full_unchanged=true auth_version_updated_at_unchanged=true counts_unchanged=true both_goroutines_closed=true pool_in_use=0", operation)
}
