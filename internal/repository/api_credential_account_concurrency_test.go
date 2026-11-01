package repository

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/account"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/model"
)

func TestRepositoryAPIUpdateRejectsDeletedOwnerAfterNormalization(t *testing.T) {
	testRepositoryAPIUpdateAfterAccountMutation(t, "delete")
}

func TestRepositoryAPIUpdateRejectsMovedOwnerAfterNormalization(t *testing.T) {
	testRepositoryAPIUpdateAfterAccountMutation(t, "move")
}

// 只暂停本请求正式 FindOwned 的无事务、无连接读取边界；不在持锁事务内等待。
func testRepositoryAPIUpdateAfterAccountMutation(t *testing.T, operation string) {
	t.Helper()
	s, db := newRepositoryTestService(t)
	accounts := account.NewService(db)
	owner, err := accounts.CreateUser(context.Background(), "a02-api-update-"+operation, "本例API凭据所有者", "synthetic-password-only-123")
	if err != nil {
		t.Fatal(err)
	}
	beforeOwner, err := accounts.FindByID(context.Background(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var deleteUser func(context.Context, string) error
	if operation == "delete" {
		permissions, err := access.NewService(db)
		if err != nil {
			t.Fatal(err)
		}
		deleteUser = permissions.DeleteUser
	}
	var targetDepartment model.Department
	if operation == "move" {
		now := time.Now().UTC()
		targetDepartment = model.Department{ID: uuid.NewString(), Name: "本例API凭据调入部门", IsActive: true, CreatedAt: now, UpdatedAt: now}
		if err := db.Create(&targetDepartment).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		UserID: owner.ID, DepartmentID: owner.DepartmentID, ProjectID: database.DefaultProjectID(owner.DepartmentID),
	})
	input := Input{Name: "a02-api-update-original", Provider: model.GitProviderGitea, CloneURL: "https://example.invalid/a02/api-update.git", DefaultBranch: "main", AuthType: model.GitAuthNone}
	repository, _, err := s.Create(ctx, owner.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	var before model.GitRepository
	if err := db.First(&before, "uuid = ?", repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	token := "a02-synthetic-api-token-not-a-real-secret"
	selected, err := s.credentials.Create(context.Background(), owner.ID, credential.Input{Name: "本例API令牌", Provider: model.GitProviderGitea, AuthType: model.GitAuthToken, Secret: &token})
	if err != nil {
		t.Fatal(err)
	}
	var beforeCredential model.GitCredential
	if err := db.First(&beforeCredential, "uuid = ?", selected.ID).Error; err != nil {
		t.Fatal(err)
	}
	count := func(value any, column, id string) int64 {
		t.Helper()
		var rows int64
		query := db.Model(value)
		if column != "" {
			query = query.Where(column+" = ?", id)
		}
		if err := query.Count(&rows).Error; err != nil {
			t.Fatalf("读取本例计数失败: %v", err)
		}
		return rows
	}
	beforeUsers := count(&model.User{}, "", "")
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	updateContext, cancelUpdate := context.WithTimeout(ctx, 15*time.Second)
	updateContext = context.WithValue(updateContext, apiUpdateAccountGateKey{}, selected.ID)
	release := make(chan struct{})
	updateDone, mutationDone := make(chan struct{}), make(chan struct{})
	entered, gateFailed := make(chan struct{}, 1), make(chan struct{}, 1)
	var releaseOnce sync.Once
	var hits atomic.Int32
	var gateNoTransaction atomic.Bool
	var gatePoolInUse atomic.Int64
	var updateErr, mutationErr error
	var updated *model.GitRepository
	updateLaunched, mutationLaunched := false, false
	callback := "test:a02_api_update_after_credential_read_" + operation
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(apiUpdateAccountGateKey{}) != selected.ID || tx.Error != nil || tx.RowsAffected != 1 || len(tx.Statement.Selects) != 0 {
			return
		}
		value, ok := tx.Statement.Dest.(*model.GitCredential)
		if !ok || tx.Statement.Table != "git_credentials" || value.ID != selected.ID || value.UserID != owner.ID || hits.Add(1) != 1 {
			return
		}
		_, inTransaction := tx.Statement.ConnPool.(gorm.TxCommitter)
		gateNoTransaction.Store(!inTransaction)
		gatePoolInUse.Store(int64(pool.Stats().InUse))
		// gorm:query 已关闭 Rows；持锁复核的字段 Select 不属于本检测点。
		if inTransaction || gatePoolInUse.Load() != 0 {
			gateFailed <- struct{}{}
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
		cancelUpdate()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		cancelUpdate()
		for _, task := range []struct {
			name     string
			launched bool
			done     <-chan struct{}
		}{{"仓库更新", updateLaunched, updateDone}, {"账户变更", mutationLaunched, mutationDone}} {
			if task.launched {
				select {
				case <-task.done:
				case <-time.After(5 * time.Second):
					t.Errorf("%s未确认退出，不能声称清理完成", task.name)
				}
			}
		}
		if err := db.Callback().Query().Remove(callback); err != nil {
			t.Errorf("移除本例数据库检测点失败: %v", err)
		}
	})
	input.Name, input.APICredentialID = "a02-api-update-new-reference", &selected.ID
	if input.AuthType != model.GitAuthNone || input.CredentialID != nil || before.APICredentialID != nil || before.CredentialID != nil {
		t.Fatal("本例必须只有新的API引用，不得借clone分支覆盖")
	}
	updateLaunched = true
	go func() {
		defer close(updateDone)
		updated, _, updateErr = s.Update(updateContext, owner.ID, repository.ID, input)
	}()
	select {
	case <-entered:
	case <-gateFailed:
		t.Fatal("检测点占事务或连接，不把等待当产品缺陷")
	case <-updateDone:
		t.Fatalf("正常API引用Update未命中FindOwned检测点: err=%v", updateErr)
	case <-time.After(5 * time.Second):
		t.Fatal("未确认Update已读API凭据，不把超时当产品缺陷")
	}
	mutationLaunched = true
	go func() {
		defer close(mutationDone)
		mutationContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if operation == "delete" {
			mutationErr = deleteUser(mutationContext, owner.ID)
		} else {
			mutationErr = accounts.SetDepartment(mutationContext, owner.ID, targetDepartment.ID)
		}
	}()
	select {
	case <-mutationDone:
	case <-time.After(5 * time.Second):
		t.Fatal("正常账户变更未先完成，不把锁等待当产品缺陷")
	}
	if mutationErr != nil {
		t.Fatalf("无既有引用时的账户变更前提失败: operation=%s err=%v", operation, mutationErr)
	}
	if operation == "delete" {
		if count(&model.User{}, "uuid", owner.ID) != 0 || count(&model.GitCredential{}, "uuid", selected.ID) != 0 || count(&model.GitCredential{}, "user_id", owner.ID) != 0 || count(&model.User{}, "", "") != beforeUsers-1 {
			t.Fatal("删除回执后用户或个人API凭据未真实删除")
		}
	} else {
		afterOwner, err := accounts.FindByID(context.Background(), owner.ID)
		if err != nil || afterOwner.DepartmentID != targetDepartment.ID || afterOwner.AuthVersion != beforeOwner.AuthVersion+1 {
			t.Fatalf("调部门回执与持久AuthVersion不符: err=%v", err)
		}
		expectedOwner := *beforeOwner
		expectedOwner.DepartmentID, expectedOwner.AuthVersion, expectedOwner.UpdatedAt = targetDepartment.ID, beforeOwner.AuthVersion+1, afterOwner.UpdatedAt
		if !reflect.DeepEqual(expectedOwner, *afterOwner) || count(&model.User{}, "", "") != beforeUsers {
			t.Fatal("正常调部门改变了其他用户字段或用户计数")
		}
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-updateDone:
	case <-time.After(5 * time.Second):
		t.Fatal("释放检测点后Update未结束，不把锁等待当产品缺陷")
	}
	if updated != nil || !errors.Is(updateErr, ErrInvalidCredential) {
		t.Fatalf("旧请求未安全拒绝失效API引用: operation=%s result_non_nil=%t err=%v", operation, updated != nil, updateErr)
	}
	var after model.GitRepository
	if err := db.First(&after, "uuid = ?", repository.ID).Error; err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("失败Update改变或丢失旧仓库完整数据/时间: err=%v", err)
	}
	if count(&model.GitRepository{}, "", "") != 1 {
		t.Fatal("失败Update新增仓库或删除了旧业务资源")
	}
	if operation == "delete" {
		if count(&model.User{}, "uuid", owner.ID) != 0 || count(&model.GitCredential{}, "uuid", selected.ID) != 0 || count(&model.GitCredential{}, "user_id", owner.ID) != 0 || count(&model.User{}, "", "") != beforeUsers-1 {
			t.Fatal("失败Update恢复了已删除的用户/凭据或改变用户计数")
		}
	} else {
		var afterCredential model.GitCredential
		if err := db.First(&afterCredential, "uuid = ?", selected.ID).Error; err != nil || !reflect.DeepEqual(beforeCredential, afterCredential) || count(&model.GitCredential{}, "user_id", owner.ID) != 1 {
			t.Fatalf("失败Update改变或丢失原API凭据/时间: err=%v", err)
		}
		afterOwner, err := accounts.FindByID(context.Background(), owner.ID)
		if err != nil || afterOwner.DepartmentID != targetDepartment.ID || afterOwner.AuthVersion != beforeOwner.AuthVersion+1 {
			t.Fatalf("失败Update改变了已提交部门/AuthVersion: err=%v", err)
		}
	}
	if hits.Load() != 1 || !gateNoTransaction.Load() || gatePoolInUse.Load() != 0 || pool.Stats().InUse != 0 {
		t.Fatal("检测点次数/无事务/连接释放前提不成立")
	}
	t.Logf("正常交错闭合: operation=%s gate_hits=%d gate_no_transaction=%t gate_pool_in_use=%d mutation_committed=true update_rejected=true repository_full_unchanged=true selected_credential_state_verified=true user_auth_version_or_deletion_verified=true repository_count=1 both_goroutines_closed=true final_pool_in_use=%d", operation, hits.Load(), gateNoTransaction.Load(), gatePoolInUse.Load(), pool.Stats().InUse)
}

type apiUpdateAccountGateKey struct{}
