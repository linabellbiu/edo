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

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
)

func TestGitResolveTagCommitKeepsCachedEventObjectAfterTagMoves(t *testing.T) {
	path, first := newMetadataGitFixture(t, 64)
	remote, err := git.PlainOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := remote.CreateTag("v1.0", first, &git.CreateTagOptions{
		Tagger: &object.Signature{Name: "EDO Test", Email: "edo@example.com", When: time.Now().UTC()}, Message: "固定版本",
	})
	if err != nil {
		t.Fatal(err)
	}
	client := NewGitClient(config.Git{Timeout: 5 * time.Second})
	repository := model.GitRepository{CloneURL: path, AuthType: model.GitAuthNone}
	directory := filepath.Join(t.TempDir(), "objects")
	// 在事件对象仍可读取时固定缓存；不假设移除引用后服务端仍会提供旧对象。
	initial, err := client.ResolveTagCommitCachedAt(context.Background(), repository, "", "refs/tags/v1.0", tag.Hash().String(), directory)
	if err != nil || initial != first.String() {
		t.Fatalf("没有读取当前标签的固定目标: commit=%s err=%v", initial, err)
	}
	second := commitVersionFile(t, remote, path, "next\n", "最新版本不可替代事件版本")
	if err := remote.Storer.SetReference(plumbing.NewHashReference(plumbing.NewTagReferenceName("v1.0"), second)); err != nil {
		t.Fatal(err)
	}
	for _, pinned := range []plumbing.Hash{tag.Hash(), first} {
		commit, err := client.ResolveTagCommitCachedAt(context.Background(), repository, "", "refs/tags/v1.0", pinned.String(), directory)
		if err != nil || commit != first.String() {
			t.Fatalf("移动后的 Tag 不得替代固定事件: commit=%s err=%v", commit, err)
		}
	}
	if err := os.Rename(path, path+"-offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(path+"-offline", path) })
	commit, err := client.ResolveTagCommitCachedAt(context.Background(), repository, "unused-private-test-credential", "refs/tags/v1.0", tag.Hash().String(), directory)
	if err != nil || commit != first.String() {
		t.Fatalf("固定对象缓存未复用: commit=%s err=%v", commit, err)
	}
	content, err := os.ReadFile(filepath.Join(directory, "config"))
	if err != nil || strings.Contains(string(content), "unused-private-test-credential") {
		t.Fatal("固定对象缓存不得保存凭据")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ResolveTagCommitCachedAt(ctx, repository, "", "refs/tags/v1.0", first.String(), directory); !errors.Is(err, context.Canceled) {
		t.Fatalf("缓存命中也必须尊重取消: %v", err)
	}
}

func TestGitResolveTagCommitRefOnlyFallbackKeepsPinnedObject(t *testing.T) {
	path, first := newMetadataGitFixture(t, 64)
	remote, err := git.PlainOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := remote.CreateTag("v1.0", first, &git.CreateTagOptions{
		Tagger: &object.Signature{Name: "EDO Test", Email: "edo@example.com", When: time.Now().UTC()}, Message: "固定版本",
	})
	if err != nil {
		t.Fatal(err)
	}
	client := NewGitClient(config.Git{Timeout: 5 * time.Second})
	server, trace := metadataRefOnlyHTTPServer(t, path)
	repository := model.GitRepository{CloneURL: server.URL + "/repo.git", AuthType: model.GitAuthNone}
	commit, err := client.ResolveTagCommitCachedAt(context.Background(), repository, "", "refs/tags/v1.0", tag.Hash().String(), filepath.Join(t.TempDir(), "objects"))
	if err != nil || commit != first.String() {
		t.Fatalf("原 Ref 回退未解析固定对象: commit=%s err=%v", commit, err)
	}
	trace.assertFetch(t, 1, tag.Hash())

	second := commitVersionFile(t, remote, path, "next\n", "不得返回当前 Tag")
	if err := remote.Storer.SetReference(plumbing.NewHashReference(plumbing.NewTagReferenceName("v1.0"), second)); err != nil {
		t.Fatal(err)
	}
	otherServer, otherTrace := metadataRefOnlyHTTPServer(t, path)
	repository.CloneURL = otherServer.URL + "/repo.git"
	commit, err = client.ResolveTagCommitCachedAt(context.Background(), repository, "", "refs/tags/v1.0", tag.Hash().String(), filepath.Join(t.TempDir(), "objects"))
	if err == nil || commit != "" || !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Fatalf("旧对象缺失不得采用当前 Tag: commit=%s err=%v", commit, err)
	}
	otherTrace.assertFetch(t, 1, second)
}

type tagResolutionClient struct {
	*serviceMetadataClient
	resolve func(context.Context, serviceMetadataCall) (string, error)
}

func (c *tagResolutionClient) ResolveTagCommitCachedAt(ctx context.Context, repository model.GitRepository, credential, ref, objectSHA, directory string) (string, error) {
	return c.resolve(ctx, serviceMetadataCall{repository: repository, credential: credential, ref: ref, commitSHA: objectSHA, directory: directory})
}

func TestServiceResolveTagCommitRequiresScopeActiveCredentialsAndResolver(t *testing.T) {
	for _, failure := range []string{"跨项目", "停用仓库", "失效凭据", "缺少解析能力", "内部错误"} {
		t.Run(failure, func(t *testing.T) {
			service, db := newRepositoryTestService(t)
			serviceMetadataDirectories(t, service)
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "project-a", DepartmentID: database.DefaultDepartmentID})
			repository := createServiceMetadataRepository(t, service, ctx, "tag-boundary")
			var calls atomic.Int32
			service.git = &tagResolutionClient{serviceMetadataClient: &serviceMetadataClient{}, resolve: func(_ context.Context, call serviceMetadataCall) (string, error) {
				calls.Add(1)
				if call.directory == "" || call.ref != "refs/tags/v1.0" || call.commitSHA != strings.Repeat("a", 40) {
					t.Error("未传入固定对象或独立缓存目录")
				}
				if failure == "内部错误" {
					return "", errors.New("诊断信息仅进入日志")
				}
				return strings.Repeat("b", 40), nil
			}}
			switch failure {
			case "跨项目":
				ctx = database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: "project-b", DepartmentID: database.DefaultDepartmentID, AllDepartments: true})
			case "停用仓库":
				if err := service.SetActive(ctx, repository.ID, false); err != nil {
					t.Fatal(err)
				}
			case "失效凭据":
				if err := db.Model(&model.GitRepository{}).Where("uuid = ?", repository.ID).Updates(map[string]any{"auth_type": model.GitAuthToken, "credential_id": "deleted-credential"}).Error; err != nil {
					t.Fatal(err)
				}
			case "缺少解析能力":
				service.git = &serviceMetadataClient{}
			}
			commit, raw, err := service.ResolveTagCommit(ctx, repository.ID, "refs/tags/v1.0", strings.Repeat("a", 40))
			if !errors.Is(err, ErrTagCommitResolution) || commit != "" || raw != "" || strings.Contains(err.Error(), "诊断信息") {
				t.Fatalf("失败不得泄漏诊断或返回未经验证对象: commit=%q raw=%q err=%v", commit, raw, err)
			}
			if failure != "内部错误" && calls.Load() != 0 {
				t.Fatal("边界检查失败后仍进入解析缓存")
			}
		})
	}
}

func TestServiceResolveTagCommitAliasesAndSharedCacheLock(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	serviceMetadataDirectories(t, service)
	ctx := context.Background()
	repository := createServiceMetadataRepository(t, service, ctx, "tag-cache-lock")
	started := make(chan serviceMetadataCall, 1)
	metadata := make(chan serviceMetadataCall, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	client := &tagResolutionClient{serviceMetadataClient: &serviceMetadataClient{metadata: func(_ context.Context, call serviceMetadataCall) (string, error) {
		metadata <- call
		return "标题", nil
	}}, resolve: func(_ context.Context, call serviceMetadataCall) (string, error) {
		started <- call
		<-release
		return strings.Repeat("b", 40), nil
	}}
	service.git = client
	resolved := make(chan error, 1)
	go func() {
		commit, raw, err := service.ResolveTagCommit(ctx, repository.ID, "refs/tags/v1.0", strings.Repeat("a", 40))
		if err == nil && (commit != strings.Repeat("b", 40) || raw != strings.Repeat("a", 40)) {
			err = errors.New("未返回实际 Commit 与原 Tag OID")
		}
		resolved <- err
	}()
	call := waitServiceMetadataCall(t, started)
	if _, err := service.ClearCacheDirectory(); !errors.Is(err, ErrDirectoryBusy) {
		t.Fatalf("解析期间允许清理对象缓存: %v", err)
	}
	messaged := make(chan error, 1)
	go func() {
		_, err := service.CommitMessage(ctx, repository.ID, "refs/tags/v1.0", strings.Repeat("b", 40))
		messaged <- err
	}()
	waitServiceMetadataLockReferences(t, service, "repository:"+call.directory, 2)
	select {
	case <-metadata:
		t.Fatal("标题读取未与 Tag 解析共享仓库锁")
	default:
	}
	unblock()
	waitServiceMetadataResult(t, resolved)
	if next := waitServiceMetadataCall(t, metadata); next.directory != call.directory {
		t.Fatal("标题与 Tag 解析使用了不同缓存目录")
	}
	waitServiceMetadataResult(t, messaged)
	client.resolve = func(_ context.Context, call serviceMetadataCall) (string, error) { return call.commitSHA, nil }
	commit, raw, err := service.ResolveTagCommit(ctx, repository.ID, "refs/tags/v1.0", strings.Repeat("b", 40))
	if err != nil || commit != strings.Repeat("b", 40) || raw != "" {
		t.Fatalf("Commit 输入不得伪造 Tag 别名: commit=%s raw=%q err=%v", commit, raw, err)
	}
}
