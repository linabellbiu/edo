package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/model"
)

type serviceMetadataCall struct {
	repository model.GitRepository
	credential string
	ref        string
	commitSHA  string
	directory  string
	historical bool
}

type serviceMetadataClient struct {
	metadata func(context.Context, serviceMetadataCall) (string, error)
	checkout func(context.Context, serviceMetadataCall, string) (bool, error)
}

func (*serviceMetadataClient) ListRefs(context.Context, model.GitRepository, string) (RefResult, error) {
	return RefResult{}, nil
}

func (s *serviceMetadataClient) CommitMessageCachedAt(ctx context.Context, repository model.GitRepository, credential, ref, commitSHA, directory string, historical bool) (string, error) {
	return s.metadata(ctx, serviceMetadataCall{repository, credential, ref, commitSHA, directory, historical})
}

func (s *serviceMetadataClient) Checkout(ctx context.Context, repository model.GitRepository, credential, ref, commitSHA, destination string) error {
	_, err := s.CheckoutCachedAt(ctx, repository, credential, ref, commitSHA, destination, "")
	return err
}

func (s *serviceMetadataClient) CheckoutCachedAt(ctx context.Context, repository model.GitRepository, credential, ref, commitSHA, destination, directory string) (bool, error) {
	call := serviceMetadataCall{repository: repository, credential: credential, ref: ref, commitSHA: commitSHA, directory: directory}
	if s.checkout != nil {
		return s.checkout(ctx, call, destination)
	}
	return false, os.MkdirAll(destination, 0o700)
}

func createServiceMetadataRepository(t *testing.T, service *Service, ctx context.Context, name string) *model.GitRepository {
	t.Helper()
	repository, _, err := service.Create(ctx, "admin", Input{
		Name: name, Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/" + name + ".git", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func serviceMetadataDirectories(t *testing.T, service *Service) DirectorySettings {
	t.Helper()
	directories, err := service.PrepareDirectories(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service.ApplyDirectories(directories)
	return directories
}

func waitServiceMetadataCall(t *testing.T, calls <-chan serviceMetadataCall) serviceMetadataCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("缓存客户端未在限定时间内开始读取")
		return serviceMetadataCall{}
	}
}

func waitServiceMetadataResult(t *testing.T, results <-chan error) {
	t.Helper()
	select {
	case err := <-results:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("缓存读取未在限定时间内退出")
	}
}

func waitServiceMetadataLockReferences(t *testing.T, service *Service, key string, want int) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		service.checkoutLocksMu.Lock()
		lock := service.checkoutLocks[key]
		got := 0
		if lock != nil {
			got = lock.references
		}
		service.checkoutLocksMu.Unlock()
		if got == want {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("仓库锁引用未达到预期: got=%d want=%d", got, want)
		}
	}
}

func TestServiceMetadataCacheStillChecksProjectActiveStateAndCredentials(t *testing.T) {
	for _, failure := range []string{"跨项目", "停用仓库", "失效凭据引用", "损坏凭据密文"} {
		t.Run(failure, func(t *testing.T) {
			service, db := newRepositoryTestService(t)
			serviceMetadataDirectories(t, service)
			var calls atomic.Int32
			service.git = &serviceMetadataClient{metadata: func(_ context.Context, call serviceMetadataCall) (string, error) {
				calls.Add(1)
				if call.ref != "refs/heads/main" || call.commitSHA != strings.Repeat("a", 40) || call.directory == "" || call.historical {
					t.Error("缓存客户端收到的固定版本或路径不正确")
				}
				return "已有缓存标题", nil
			}}
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-a", DepartmentID: database.DefaultDepartmentID})
			repository := createServiceMetadataRepository(t, service, ctx, "metadata-boundaries")
			if message, err := service.CommitMessage(ctx, repository.ID, "refs/heads/main", strings.Repeat("a", 40)); err != nil || message != "已有缓存标题" {
				t.Fatalf("预热缓存失败: message=%q err=%v", message, err)
			}
			want := ErrRepositoryNotFound
			switch failure {
			case "跨项目":
				ctx = database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-b", DepartmentID: database.DefaultDepartmentID, AllDepartments: true})
			case "停用仓库":
				if err := service.SetActive(ctx, repository.ID, false); err != nil {
					t.Fatal(err)
				}
			case "失效凭据引用":
				want = credential.ErrCredentialNotFound
				if err := db.Model(&model.GitRepository{}).Where("id = ?", repository.ID).Updates(map[string]any{"auth_type": model.GitAuthToken, "credential_id": "deleted-credential"}).Error; err != nil {
					t.Fatal(err)
				}
			case "损坏凭据密文":
				want = nil
				if err := db.Model(&model.GitRepository{}).Where("id = ?", repository.ID).Updates(map[string]any{"auth_type": model.GitAuthToken, "credential_ciphertext": "invalid-ciphertext"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			for _, read := range []func(context.Context, string, string, string) (string, error){service.CommitMessage, service.HistoricalCommitMessage} {
				message, err := read(ctx, repository.ID, "refs/heads/main", strings.Repeat("a", 40))
				if message != "" || err == nil || want != nil && !errors.Is(err, want) {
					t.Fatalf("缓存绕过了项目、状态或凭据检查: message=%q err=%v", message, err)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("校验失败后仍访问缓存客户端: calls=%d", calls.Load())
			}
		})
	}
}

func TestServiceMetadataAndCheckoutShareRepositoryLockAndBlockCleanup(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	serviceMetadataDirectories(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	metadataCalls, checkoutCalls := make(chan serviceMetadataCall, 1), make(chan serviceMetadataCall, 1)
	service.git = &serviceMetadataClient{
		metadata: func(ctx context.Context, call serviceMetadataCall) (string, error) {
			metadataCalls <- call
			select {
			case <-gate:
				return "缓存标题", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		checkout: func(_ context.Context, call serviceMetadataCall, destination string) (bool, error) {
			checkoutCalls <- call
			return false, os.MkdirAll(destination, 0o700)
		},
	}
	repository := createServiceMetadataRepository(t, service, ctx, "metadata-checkout")
	metadataResults := make(chan error, 1)
	go func() {
		_, err := service.CommitMessage(ctx, repository.ID, "refs/heads/main", strings.Repeat("a", 40))
		metadataResults <- err
	}()
	metadataCall := waitServiceMetadataCall(t, metadataCalls)
	if _, err := service.ClearCacheDirectory(); !errors.Is(err, ErrDirectoryBusy) {
		t.Fatalf("读取对象缓存期间仍允许清理: %v", err)
	}
	type checkoutResult struct {
		lease *CheckoutLease
		err   error
	}
	checkoutResults := make(chan checkoutResult, 1)
	go func() {
		lease, err := service.AcquireCheckout(ctx, repository.ID, "refs/heads/main", strings.Repeat("a", 40))
		checkoutResults <- checkoutResult{lease, err}
	}()
	waitServiceMetadataLockReferences(t, service, "repository:"+metadataCall.directory, 2)
	select {
	case <-checkoutCalls:
		t.Fatal("同仓检出没有等待元数据读取释放对象缓存锁")
	default:
	}
	release()
	waitServiceMetadataResult(t, metadataResults)
	checkoutCall := waitServiceMetadataCall(t, checkoutCalls)
	if checkoutCall.directory != metadataCall.directory {
		t.Fatal("元数据和检出没有使用同一个对象缓存路径")
	}
	select {
	case result := <-checkoutResults:
		if result.err != nil || result.lease == nil {
			t.Fatalf("释放元数据锁后检出失败: %v", result.err)
		}
		result.lease.Release()
	case <-time.After(5 * time.Second):
		t.Fatal("检出没有在释放元数据锁后继续")
	}
	waitServiceMetadataLockReferences(t, service, "repository:"+metadataCall.directory, 0)
	if _, err := service.ClearCacheDirectory(); err != nil {
		t.Fatalf("读取与检出结束后缓存清理仍然被阻塞: %v", err)
	}
}

func TestServiceMetadataReadsDifferentRepositoriesConcurrently(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	serviceMetadataDirectories(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	calls, results := make(chan serviceMetadataCall, 2), make(chan error, 2)
	service.git = &serviceMetadataClient{metadata: func(ctx context.Context, call serviceMetadataCall) (string, error) {
		calls <- call
		select {
		case <-gate:
			return "缓存标题", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	first := createServiceMetadataRepository(t, service, ctx, "metadata-first")
	second := createServiceMetadataRepository(t, service, ctx, "metadata-second")
	for _, repository := range []*model.GitRepository{first, second} {
		go func() {
			_, err := service.CommitMessage(ctx, repository.ID, "refs/heads/main", strings.Repeat("a", 40))
			results <- err
		}()
	}
	firstCall, secondCall := waitServiceMetadataCall(t, calls), waitServiceMetadataCall(t, calls)
	if firstCall.directory == secondCall.directory || firstCall.repository.ID == secondCall.repository.ID {
		t.Fatal("不同仓库没有使用独立缓存路径")
	}
	// 两个回调均已进入，验证第一条尚未结束时第二条已经开始。
	release()
	waitServiceMetadataResult(t, results)
	waitServiceMetadataResult(t, results)
}

func TestServiceMetadataDirectorySwitchKeepsInflightPathAndUsesNewPath(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	previous := serviceMetadataDirectories(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	calls, results := make(chan serviceMetadataCall, 2), make(chan error, 2)
	service.git = &serviceMetadataClient{metadata: func(ctx context.Context, call serviceMetadataCall) (string, error) {
		calls <- call
		select {
		case <-gate:
			return "缓存标题", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	repository := createServiceMetadataRepository(t, service, ctx, "metadata-directory-switch")
	read := func() {
		_, err := service.HistoricalCommitMessage(ctx, repository.ID, "refs/heads/main", strings.Repeat("a", 40))
		results <- err
	}
	go read()
	firstCall := waitServiceMetadataCall(t, calls)
	current := serviceMetadataDirectories(t, service)
	go read()
	secondCall := waitServiceMetadataCall(t, calls)
	if !strings.HasPrefix(firstCall.directory, previous.CacheDirectory+string(filepath.Separator)) || !strings.HasPrefix(secondCall.directory, current.CacheDirectory+string(filepath.Separator)) || firstCall.directory == secondCall.directory || !firstCall.historical || !secondCall.historical {
		t.Fatalf("目录切换改写了在途路径或后续请求未使用新目录: first=%+v second=%+v", firstCall, secondCall)
	}
	release()
	waitServiceMetadataResult(t, results)
	waitServiceMetadataResult(t, results)
}

func TestServiceMetadataWaitingCancellationNeverCallsClient(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	serviceMetadataDirectories(t, service)
	var calls atomic.Int32
	var directory string
	service.git = &serviceMetadataClient{metadata: func(_ context.Context, call serviceMetadataCall) (string, error) {
		calls.Add(1)
		directory = call.directory
		return "缓存标题", nil
	}}
	repository := createServiceMetadataRepository(t, service, context.Background(), "metadata-cancel")
	if _, err := service.CommitMessage(context.Background(), repository.ID, "refs/heads/main", strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	key := "repository:" + directory
	release, err := service.acquireCheckoutLock(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	results := make(chan error, 1)
	go func() {
		_, err := service.CommitMessage(ctx, repository.ID, "refs/heads/main", strings.Repeat("a", 40))
		results <- err
	}()
	waitServiceMetadataLockReferences(t, service, key, 2)
	cancel()
	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("等待取消未保留上下文错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待仓库锁的取消请求未退出")
	}
	if calls.Load() != 1 {
		t.Fatalf("等待取消后仍调用缓存客户端: calls=%d", calls.Load())
	}
	waitServiceMetadataLockReferences(t, service, key, 1)
	release()
	waitServiceMetadataLockReferences(t, service, key, 0)
}

func TestServiceMetadataUsesWorkspaceCacheWhenSeparateCacheIsUnset(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	directory := t.TempDir()
	service.ApplyDirectories(DirectorySettings{WorkspaceDirectory: directory})
	var actual serviceMetadataCall
	service.git = &serviceMetadataClient{metadata: func(_ context.Context, call serviceMetadataCall) (string, error) {
		actual = call
		return "缓存标题", nil
	}}
	repository := createServiceMetadataRepository(t, service, context.Background(), "metadata-workspace-cache")
	if _, err := service.CommitMessage(context.Background(), repository.ID, "refs/tags/v1.0", strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(actual.directory, directory+string(filepath.Separator)) || filepath.Base(actual.directory) != ".cache" || actual.ref != "refs/tags/v1.0" {
		t.Fatalf("未配置独立缓存时工作区缓存路径不正确: %+v", actual)
	}
}
