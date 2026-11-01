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

	"edo/internal/account"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/model"
)

// 本例只暂停正式无锁凭据读取之后，不占事务或连接；调部门与更新均走正常服务入口。
func TestRepositoryUpdateRejectsCredentialOwnerMovedAfterNormalization(t *testing.T) {
	s, db := newRepositoryTestService(t)
	accounts := account.NewService(db)
	owner, err := accounts.CreateUser(context.Background(), "a02-update-transfer", "本例调部门用户", "synthetic-password-only-123")
	if err != nil {
		t.Fatal(err)
	}
	beforeOwner, err := accounts.FindByID(context.Background(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	targetDepartment := model.Department{ID: uuid.NewString(), Name: "本例调入部门", IsActive: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&targetDepartment).Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		UserID: owner.ID, DepartmentID: owner.DepartmentID, ProjectID: database.DefaultProjectID(owner.DepartmentID),
	})
	input := Input{Name: "a02-update-original", Provider: model.GitProviderGitea, CloneURL: "https://example.invalid/a02/update.git", DefaultBranch: "main", AuthType: model.GitAuthNone}
	repository, _, err := s.Create(ctx, owner.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	var before model.GitRepository
	if err := db.First(&before, "uuid = ?", repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	token := "a02-synthetic-token-not-a-real-secret"
	selected, err := s.credentials.Create(context.Background(), owner.ID, credential.Input{Name: "本例克隆凭据", Provider: model.GitProviderGitea, AuthType: model.GitAuthToken, Secret: &token})
	if err != nil {
		t.Fatal(err)
	}
	var beforeCredential model.GitCredential
	if err := db.First(&beforeCredential, "uuid = ?", selected.ID).Error; err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	updateContext, cancelUpdate := context.WithTimeout(ctx, 15*time.Second)
	updateContext = context.WithValue(updateContext, updateCredentialDepartmentGateKey{}, selected.ID)
	release := make(chan struct{})
	updateDone, moveDone := make(chan struct{}), make(chan struct{})
	entered := make(chan struct{}, 1)
	gateFailed := make(chan struct{}, 1)
	var releaseOnce sync.Once
	var hits atomic.Int32
	var gateNoTransaction atomic.Bool
	var gatePoolInUse atomic.Int64
	var updateErr, moveErr error
	var updated *model.GitRepository
	updateLaunched, moveLaunched := false, false
	callback := "test:a02_update_after_credential_read"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(updateCredentialDepartmentGateKey{}) != selected.ID || tx.Error != nil || tx.RowsAffected != 1 || len(tx.Statement.Selects) != 0 {
			return
		}
		value, ok := tx.Statement.Dest.(*model.GitCredential)
		if !ok || tx.Statement.Table != "git_credentials" || value.ID != selected.ID || value.UserID != owner.ID || hits.Add(1) != 1 {
			return
		}
		_, inTransaction := tx.Statement.ConnPool.(gorm.TxCommitter)
		gateNoTransaction.Store(!inTransaction)
		gatePoolInUse.Store(int64(pool.Stats().InUse))
		// gorm:query 已关闭 Rows，禁止在事务或持连接时制造等待。
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
		}{{"仓库更新", updateLaunched, updateDone}, {"用户调部门", moveLaunched, moveDone}} {
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
	input.Name, input.AuthType, input.CredentialID = "a02-update-new-reference", model.GitAuthToken, &selected.ID
	updateLaunched = true
	go func() {
		defer close(updateDone)
		updated, _, updateErr = s.Update(updateContext, owner.ID, repository.ID, input)
	}()
	select {
	case <-entered:
	case <-gateFailed:
		t.Fatal("检测点占事务或连接，不把等待当引用保护缺陷")
	case <-updateDone:
		t.Fatalf("正常Update未命中凭据读取检测点: err=%v", updateErr)
	case <-time.After(5 * time.Second):
		t.Fatal("未确认Update已读凭据，不把超时当产品缺陷")
	}
	moveLaunched = true
	go func() {
		defer close(moveDone)
		moveContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		moveErr = accounts.SetDepartment(moveContext, owner.ID, targetDepartment.ID)
	}()
	select {
	case <-moveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("正常SetDepartment未先完成，不把锁等待当产品缺陷")
	}
	if moveErr != nil {
		t.Fatalf("无既有引用时调部门前提失败: %v", moveErr)
	}
	afterOwner, err := accounts.FindByID(context.Background(), owner.ID)
	if err != nil || afterOwner.DepartmentID != targetDepartment.ID || afterOwner.AuthVersion != beforeOwner.AuthVersion+1 {
		t.Fatalf("调部门回执与持久状态不符: err=%v", err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-updateDone:
	case <-time.After(5 * time.Second):
		t.Fatal("释放检测点后Update未结束，不把锁等待当产品缺陷")
	}
	if updated != nil || !errors.Is(updateErr, ErrInvalidCredential) {
		t.Fatalf("旧部门Update未安全拒绝迁出用户凭据: result_non_nil=%t err=%v", updated != nil, updateErr)
	}
	var after model.GitRepository
	if err := db.First(&after, "uuid = ?", repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("失败的Update改变了旧仓库完整持久数据")
	}
	var afterCredential model.GitCredential
	if err := db.First(&afterCredential, "uuid = ?", selected.ID).Error; err != nil || !reflect.DeepEqual(beforeCredential, afterCredential) {
		t.Fatalf("失败的Update改变或丢失原个人凭据: err=%v", err)
	}
	var repositoryCount int64
	if err := db.Model(&model.GitRepository{}).Count(&repositoryCount).Error; err != nil || repositoryCount != 1 {
		t.Fatalf("失败Update新增仓库或记录丢失: count=%d err=%v", repositoryCount, err)
	}
	if hits.Load() != 1 || !gateNoTransaction.Load() || gatePoolInUse.Load() != 0 || pool.Stats().InUse != 0 {
		t.Fatal("检测点次数/无事务/连接释放前提不成立")
	}
	t.Logf("正常交错闭合: gate_hits=%d gate_no_transaction=%t gate_pool_in_use=%d move_completed=true update_rejected=true repository_full_unchanged=true credential_unchanged=true auth_version_incremented=true repository_count=%d both_goroutines_closed=true final_pool_in_use=%d", hits.Load(), gateNoTransaction.Load(), gatePoolInUse.Load(), repositoryCount, pool.Stats().InUse)
}

type updateCredentialDepartmentGateKey struct{}
