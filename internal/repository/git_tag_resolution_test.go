package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"edo/internal/config"
	"edo/internal/model"
)

func TestFixedCommitCacheKeepsTagObjectAfterTagMoves(t *testing.T) {
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
	// 在标签对象仍可读取时固定缓存；不假设移除引用后服务端仍会提供旧对象。
	initial, err := client.fixedCommitCachedAt(context.Background(), repository, "", "refs/tags/v1.0", tag.Hash().String(), directory, 1)
	if err != nil || initial == nil || initial.Hash != first {
		t.Fatalf("没有读取当前标签的固定目标: commit=%v err=%v", initial, err)
	}
	second := commitVersionFile(t, remote, path, "next\n", "最新版本不可替代固定版本")
	if err := remote.Storer.SetReference(plumbing.NewHashReference(plumbing.NewTagReferenceName("v1.0"), second)); err != nil {
		t.Fatal(err)
	}
	for _, pinned := range []plumbing.Hash{tag.Hash(), first} {
		commit, err := client.fixedCommitCachedAt(context.Background(), repository, "", "refs/tags/v1.0", pinned.String(), directory, 1)
		if err != nil || commit == nil || commit.Hash != first {
			t.Fatalf("移动后的 Tag 不得替代固定对象: commit=%v err=%v", commit, err)
		}
	}
	if err := os.Rename(path, path+"-offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(path+"-offline", path) })
	commit, err := client.fixedCommitCachedAt(context.Background(), repository, "unused-private-test-credential", "refs/tags/v1.0", tag.Hash().String(), directory, 1)
	if err != nil || commit == nil || commit.Hash != first {
		t.Fatalf("固定对象缓存未复用: commit=%v err=%v", commit, err)
	}
	content, err := os.ReadFile(filepath.Join(directory, "config"))
	if err != nil || strings.Contains(string(content), "unused-private-test-credential") {
		t.Fatal("固定对象缓存不得保存凭据")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.fixedCommitCachedAt(ctx, repository, "", "refs/tags/v1.0", first.String(), directory, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("缓存命中也必须尊重取消: %v", err)
	}
}

func TestFixedCommitCacheRefOnlyFallbackKeepsPinnedTagObject(t *testing.T) {
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
	commit, err := client.fixedCommitCachedAt(context.Background(), repository, "", "refs/tags/v1.0", tag.Hash().String(), filepath.Join(t.TempDir(), "objects"), 1)
	if err != nil || commit == nil || commit.Hash != first {
		t.Fatalf("原 Ref 回退未解析固定对象: commit=%v err=%v", commit, err)
	}
	trace.assertFetch(t, 1, tag.Hash())

	second := commitVersionFile(t, remote, path, "next\n", "不得返回当前 Tag")
	if err := remote.Storer.SetReference(plumbing.NewHashReference(plumbing.NewTagReferenceName("v1.0"), second)); err != nil {
		t.Fatal(err)
	}
	otherServer, otherTrace := metadataRefOnlyHTTPServer(t, path)
	repository.CloneURL = otherServer.URL + "/repo.git"
	commit, err = client.fixedCommitCachedAt(context.Background(), repository, "", "refs/tags/v1.0", tag.Hash().String(), filepath.Join(t.TempDir(), "objects"), 1)
	if err == nil || commit != nil || !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Fatalf("旧对象缺失不得采用当前 Tag: commit=%v err=%v", commit, err)
	}
	otherTrace.assertFetch(t, 1, second)
}
