package repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/database"
)

type cachedAuthorServiceClient struct {
	*serviceMetadataClient
	author func(context.Context, string, string) (string, error)
}

func (c *cachedAuthorServiceClient) CachedCommitAuthorAt(ctx context.Context, directory, sha string) (string, error) {
	return c.author(ctx, directory, sha)
}

func TestCachedCommitAuthorDoesNotWaitForCheckoutOrCleanup(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	serviceMetadataDirectories(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	checkoutStarted := make(chan serviceMetadataCall, 1)
	checkoutRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(checkoutRelease) }) }
	defer release()
	var calls atomic.Int32
	service.git = &cachedAuthorServiceClient{
		serviceMetadataClient: &serviceMetadataClient{checkout: func(ctx context.Context, call serviceMetadataCall, destination string) (bool, error) {
			checkoutStarted <- call
			select {
			case <-checkoutRelease:
				return false, os.MkdirAll(destination, 0o700)
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}},
		author: func(context.Context, string, string) (string, error) {
			calls.Add(1)
			return "固定提交作者", nil
		},
	}
	repo := createServiceMetadataRepository(t, service, ctx, "busy-author")
	other := createServiceMetadataRepository(t, service, ctx, "available-author")
	done := make(chan error, 1)
	go func() {
		lease, err := service.AcquireTaskCheckout(ctx, repo.ID, "refs/heads/main", strings.Repeat("a", 40), "executing")
		if lease != nil {
			lease.Release()
		}
		done <- err
	}()
	checkout := waitServiceMetadataCall(t, checkoutStarted)
	read := func(id, expected string) {
		t.Helper()
		result := make(chan error, 1)
		go func() {
			author, err := service.CachedCommitAuthor(ctx, id, strings.Repeat("a", 40))
			if err == nil && author != expected {
				err = errors.New("繁忙缓存应保留空值，可用缓存应显示真实作者")
			}
			result <- err
		}()
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("提交作者展示被执行任务或目录维护阻塞")
		}
	}
	read(repo.ID, "")
	if calls.Load() != 0 {
		t.Fatal("仓库写入期间绕过锁读取了缓存")
	}
	waitServiceMetadataLockReferences(t, service, "repository:"+checkout.directory, 1)
	read(other.ID, "固定提交作者")
	if _, err := service.ClearCacheDirectory(); !errors.Is(err, ErrDirectoryBusy) {
		t.Fatalf("辅助读取误释放了执行任务的目录维护锁: %v", err)
	}
	release()
	waitServiceMetadataResult(t, done)
	read(repo.ID, "固定提交作者")
	service.maintenance.Lock()
	read(repo.ID, "")
	service.maintenance.Unlock()
	if calls.Load() != 2 || len(service.checkoutLocks) != 0 {
		t.Fatalf("繁忙读取不应调用缓存或遗留锁: calls=%d locks=%d", calls.Load(), len(service.checkoutLocks))
	}
}

func TestCachedCommitAuthorPreservesProjectBoundary(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	serviceMetadataDirectories(t, service)
	var calls atomic.Int32
	service.git = &cachedAuthorServiceClient{serviceMetadataClient: &serviceMetadataClient{}, author: func(context.Context, string, string) (string, error) {
		calls.Add(1)
		return "不可见作者", nil
	}}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-a", DepartmentID: database.DefaultDepartmentID})
	repo := createServiceMetadataRepository(t, service, ctx, "author-boundary")
	other := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-b", DepartmentID: database.DefaultDepartmentID})
	if author, err := service.CachedCommitAuthor(other, repo.ID, strings.Repeat("a", 40)); err == nil || author != "" || calls.Load() != 0 {
		t.Fatalf("辅助读取绕过了项目范围: author=%q err=%v calls=%d", author, err, calls.Load())
	}
}
