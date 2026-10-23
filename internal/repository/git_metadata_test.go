package repository

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"

	"edo/internal/config"
	"edo/internal/model"
)

func newMetadataGitFixture(t testing.TB, blobBytes int64) (string, plumbing.Hash) {
	t.Helper()
	directory := t.TempDir()
	repository, err := git.PlainInit(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(directory, "source.bin"))
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.CopyN(file, rand.Reader, blobBytes)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("source.bin"); err != nil {
		t.Fatal(err)
	}
	hash, err := worktree.Commit("固定版本标题\n\n只读取首行", &git.CommitOptions{Author: &object.Signature{
		Name: "EDO Test", Email: "edo@example.com", When: time.Now().UTC(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return directory, hash
}

func TestGitCommitMetadataReusesCheckoutObjectsWithoutRemote(t *testing.T) {
	remote, hash := newMetadataGitFixture(t, 1<<20)
	client := NewGitClient(config.Git{Timeout: 10 * time.Second})
	repository := model.GitRepository{CloneURL: remote, AuthType: model.GitAuthNone}
	cacheDirectory := filepath.Join(t.TempDir(), "objects")
	if _, err := client.CheckoutCachedAt(context.Background(), repository, "", "refs/heads/master", hash.String(), filepath.Join(t.TempDir(), "workspace"), cacheDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(remote, remote+"-offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(remote+"-offline", remote) })
	message, err := client.CommitMessageCachedAt(context.Background(), repository, "unused-private-test-credential", "refs/heads/master", hash.String(), cacheDirectory, false)
	if err != nil || message != "固定版本标题" {
		t.Fatalf("已有检出对象不应再次访问远端: message=%q err=%v", message, err)
	}
	contents, err := os.ReadFile(filepath.Join(cacheDirectory, "config"))
	if err != nil || strings.Contains(string(contents), "unused-private-test-credential") {
		t.Fatalf("缓存配置不应保存认证信息: err=%v", err)
	}
}

func TestGitCommitMetadataKeepsFixedVersionWhenBranchAdvances(t *testing.T) {
	remote, first := newMetadataGitFixture(t, 64)
	client := NewGitClient(config.Git{Timeout: 10 * time.Second})
	repository := model.GitRepository{CloneURL: remote, AuthType: model.GitAuthNone}
	directory := filepath.Join(t.TempDir(), "objects")
	if _, err := client.CommitMessageCachedAt(context.Background(), repository, "", "refs/heads/master", first.String(), directory, false); err != nil {
		t.Fatal(err)
	}
	local, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := local.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "change.txt"), []byte("next"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("change.txt"); err != nil {
		t.Fatal(err)
	}
	second, err := worktree.Commit("新版本标题", &git.CommitOptions{Author: &object.Signature{Name: "EDO Test", Email: "edo@example.com", When: time.Now().UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		hash plumbing.Hash
		want string
	}{{first, "固定版本标题"}, {second, "新版本标题"}} {
		message, err := client.CommitMessageCachedAt(context.Background(), repository, "", "refs/heads/master", tc.hash.String(), directory, false)
		if err != nil || message != tc.want {
			t.Fatalf("标题未保持固定提交: message=%q want=%q err=%v", message, tc.want, err)
		}
	}
	other, _ := newMetadataGitFixture(t, 64)
	repository.CloneURL = other
	if _, err := client.CommitMessageCachedAt(context.Background(), repository, "", "refs/heads/master", first.String(), directory, false); err == nil {
		t.Fatal("不应复用其他仓库地址的缓存")
	}
}

func TestGitCommitMetadataAnnotatedTagAndCancellation(t *testing.T) {
	remote, hash := newMetadataGitFixture(t, 64)
	local, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := local.CreateTag("v1.0", hash, &git.CreateTagOptions{Tagger: &object.Signature{Name: "EDO Test", Email: "edo@example.com", When: time.Now().UTC()}, Message: "版本说明"})
	if err != nil {
		t.Fatal(err)
	}
	client := NewGitClient(config.Git{Timeout: 10 * time.Second})
	repository := model.GitRepository{CloneURL: remote, AuthType: model.GitAuthNone}
	directory := filepath.Join(t.TempDir(), "objects")
	for _, pinned := range []plumbing.Hash{tag.Hash(), hash} {
		message, err := client.CommitMessageCachedAt(context.Background(), repository, "", "refs/tags/v1.0", pinned.String(), directory, false)
		if err != nil || message != "固定版本标题" {
			t.Fatalf("注解 Tag 标题不正确: message=%q err=%v", message, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.CommitMessageCachedAt(ctx, repository, "", "refs/heads/master", hash.String(), directory, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("缓存命中也应尊重取消: %v", err)
	}
	if _, err := client.CommitMessageCachedAt(context.Background(), repository, "", "refs/heads/master", "invalid", directory, false); err == nil {
		t.Fatal("无效 Commit 不应进入缓存")
	}
}

func TestCommitSubjectDoesNotRetainLargeDescription(t *testing.T) {
	message := "首行标题\n" + strings.Repeat("x", 1<<20)
	subject := commitSubject(message)
	if subject != "首行标题" || unsafe.StringData(subject) == unsafe.StringData(message) {
		t.Fatal("短标题仍持有整个提交说明")
	}
	if subject := commitSubject(strings.Repeat("中", 1<<20)); subject != strings.Repeat("中", 255) {
		t.Fatal("长标题未按字符上限截断")
	}
}

func TestGitCommitMetadataDoesNotRetryUnrelatedFetchFailures(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
			}))
			t.Cleanup(server.Close)
			client := NewGitClient(config.Git{Timeout: 5 * time.Second})
			_, err := client.CommitMessage(context.Background(), model.GitRepository{CloneURL: server.URL, AuthType: model.GitAuthNone}, "", "refs/heads/main", strings.Repeat("a", 40))
			if err == nil || calls.Load() != 1 {
				t.Fatalf("非SHA能力错误触发了第二次Ref请求: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func BenchmarkGitCommitMessageCache(b *testing.B) {
	remote, hash := newMetadataGitFixture(b, 4<<20)
	client := NewGitClient(config.Git{Timeout: time.Minute})
	repository := model.GitRepository{CloneURL: remote, AuthType: model.GitAuthNone}
	b.Run("旧内存仓库获取", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			// 保留改动前的实际元数据路径作为基线：只 fetch 当前 Ref，所有对象进入内存。
			cloned, err := git.Init(memory.NewStorage(), nil)
			if err != nil {
				b.Fatal(err)
			}
			origin, err := cloned.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{remote}})
			if err != nil {
				b.Fatal(err)
			}
			if err := origin.FetchContext(context.Background(), &git.FetchOptions{RefSpecs: []gitconfig.RefSpec{"+refs/heads/master:refs/edo/commit-message"}, Depth: 1, Tags: git.NoTags}); err != nil {
				b.Fatal(err)
			}
			if _, err := cloned.CommitObject(hash); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("磁盘冷缓存", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := client.CommitMessage(context.Background(), repository, "", "refs/heads/master", hash.String()); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("磁盘缓存命中", func(b *testing.B) {
		directory := filepath.Join(b.TempDir(), "objects")
		if _, err := client.CommitMessageCachedAt(context.Background(), repository, "", "refs/heads/master", hash.String(), directory, false); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if _, err := client.CommitMessageCachedAt(context.Background(), repository, "", "refs/heads/master", hash.String(), directory, false); err != nil {
				b.Fatal(err)
			}
		}
	})
}
