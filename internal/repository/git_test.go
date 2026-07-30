package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"zrt/internal/config"
	"zrt/internal/model"
)

func TestValidateCloneURL(t *testing.T) {
	valid := []string{
		"https://github.com/example/repo.git",
		"ssh://git@git.example.com:2222/team/repo.git",
		"git@git.example.com:team/repo.git",
	}
	for _, value := range valid {
		if _, err := validateCloneURL(value, false); err != nil {
			t.Fatalf("合法仓库地址被拒绝 %q: %v", value, err)
		}
	}
	invalid := []string{
		"file:///etc/passwd", "/srv/repo", "https://user:password@example.com/repo.git", "--upload-pack=evil",
	}
	for _, value := range invalid {
		if _, err := validateCloneURL(value, false); !errors.Is(err, ErrInvalidRepository) {
			t.Fatalf("非法仓库地址未被拒绝 %q: %v", value, err)
		}
	}
	if _, err := validateCloneURL("http://git.example.com/repo.git", false); !errors.Is(err, ErrInsecureRepository) {
		t.Fatalf("HTTP 地址未要求显式确认: %v", err)
	}
}

func TestRefsToResult(t *testing.T) {
	result, err := refsToResult([]*plumbing.Reference{
		plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), plumbing.NewHash("0123456789012345678901234567890123456789")),
		plumbing.NewHashReference(plumbing.NewTagReferenceName("v1.0.0"), plumbing.NewHash("abcdefabcdefabcdefabcdefabcdefabcdefabcd")),
	})
	if err != nil {
		t.Fatalf("转换 Git 引用失败: %v", err)
	}
	if len(result.Branches) != 1 || result.Branches[0].Name != "main" || len(result.Tags) != 1 || result.Tags[0].Name != "v1.0.0" {
		t.Fatalf("转换 Git 引用失败: %+v", result)
	}
}

func TestCommitMessageReadsSubjectFromTargetRef(t *testing.T) {
	repositoryPath := t.TempDir()
	local, err := git.PlainInit(repositoryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repositoryPath+"/README.md", []byte("ZRT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree, err := local.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	hash, err := worktree.Commit("修复订单发布状态\n\n补充详细说明", &git.CommitOptions{Author: &object.Signature{
		Name: "ZRT Test", Email: "zrt@example.com", When: time.Now().UTC(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	client := NewGitClient(config.Git{Timeout: 5 * time.Second})
	message, err := client.CommitMessage(context.Background(), model.GitRepository{
		CloneURL: repositoryPath, AuthType: model.GitAuthNone,
	}, "", "refs/heads/master", hash.String())
	if err != nil {
		t.Fatal(err)
	}
	if message != "修复订单发布状态" {
		t.Fatalf("提交标题读取错误: %q", message)
	}
}

func TestCheckoutPullRequestHeadRefsWithShallowFetch(t *testing.T) {
	repositoryPath := t.TempDir()
	remote, err := git.PlainInit(repositoryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := remote.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, "version.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("version.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("base", &git.CommitOptions{Author: &object.Signature{
		Name: "ZRT Test", Email: "zrt@example.com", When: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature/pr"), Create: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, "version.txt"), []byte("pull request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("version.txt"); err != nil {
		t.Fatal(err)
	}
	head, err := worktree.Commit("pull request head", &git.CommitOptions{Author: &object.Signature{
		Name: "ZRT Test", Email: "zrt@example.com", When: time.Now().UTC(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"refs/pull/17/head", "refs/merge-requests/18/head"} {
		if err := remote.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(ref), head)); err != nil {
			t.Fatalf("创建测试 PR Ref 失败: %v", err)
		}
	}

	client := NewGitClient(config.Git{Timeout: 5 * time.Second})
	for _, ref := range []string{"refs/pull/17/head", "refs/merge-requests/18/head"} {
		t.Run(ref, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "checkout")
			if err := client.Checkout(context.Background(), model.GitRepository{
				CloneURL: repositoryPath, AuthType: model.GitAuthNone,
			}, "", ref, head.String(), destination); err != nil {
				t.Fatalf("浅拉取并检出公开 PR Ref 失败: %v", err)
			}
			content, err := os.ReadFile(filepath.Join(destination, "version.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != "pull request\n" {
				t.Fatalf("检出的不是 PR 来源 Commit: %q", content)
			}
		})
	}
}

func TestCheckoutKeepsPinnedBranchCommitAfterBranchAdvances(t *testing.T) {
	repositoryPath := t.TempDir()
	remote, err := git.PlainInit(repositoryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	pinned := commitVersionFile(t, remote, repositoryPath, "pinned branch commit\n", "pinned branch commit")
	commitVersionFile(t, remote, repositoryPath, "new branch tip\n", "advance branch")

	destination := filepath.Join(t.TempDir(), "checkout")
	client := NewGitClient(config.Git{Timeout: 5 * time.Second})
	if err := client.Checkout(context.Background(), model.GitRepository{
		CloneURL: repositoryPath, AuthType: model.GitAuthNone,
	}, "", "refs/heads/master", pinned.String(), destination); err != nil {
		t.Fatalf("分支推进后检出固定 Commit 失败: %v", err)
	}
	assertVersionFile(t, destination, "pinned branch commit\n")
}

func TestCheckoutKeepsPinnedTagCommitAfterTagAdvances(t *testing.T) {
	repositoryPath := t.TempDir()
	remote, err := git.PlainInit(repositoryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	pinned := commitVersionFile(t, remote, repositoryPath, "pinned tag commit\n", "pinned tag commit")
	tagRef := plumbing.NewTagReferenceName("v1.0.0")
	if err := remote.Storer.SetReference(plumbing.NewHashReference(tagRef, pinned)); err != nil {
		t.Fatal(err)
	}
	advanced := commitVersionFile(t, remote, repositoryPath, "new tag target\n", "advance tag")
	if err := remote.Storer.SetReference(plumbing.NewHashReference(tagRef, advanced)); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "checkout")
	client := NewGitClient(config.Git{Timeout: 5 * time.Second})
	if err := client.Checkout(context.Background(), model.GitRepository{
		CloneURL: repositoryPath, AuthType: model.GitAuthNone,
	}, "", tagRef.String(), pinned.String(), destination); err != nil {
		t.Fatalf("Tag 推进后检出固定 Commit 失败: %v", err)
	}
	assertVersionFile(t, destination, "pinned tag commit\n")
}

func TestCheckoutKeepsPinnedPullRequestCommitAfterRefAdvances(t *testing.T) {
	repositoryPath := t.TempDir()
	remote, err := git.PlainInit(repositoryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	commitVersionFile(t, remote, repositoryPath, "base\n", "base")
	worktree, err := remote.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature/pinned-pr"), Create: true,
	}); err != nil {
		t.Fatal(err)
	}
	pinned := commitVersionFile(t, remote, repositoryPath, "pinned pull request commit\n", "pinned PR commit")
	advanced := commitVersionFile(t, remote, repositoryPath, "new pull request tip\n", "advance PR")

	for _, ref := range []string{"refs/pull/21/head", "refs/merge-requests/22/head"} {
		if err := remote.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(ref), advanced)); err != nil {
			t.Fatalf("更新测试 PR Ref 失败: %v", err)
		}
		t.Run(ref, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "checkout")
			client := NewGitClient(config.Git{Timeout: 5 * time.Second})
			if err := client.Checkout(context.Background(), model.GitRepository{
				CloneURL: repositoryPath, AuthType: model.GitAuthNone,
			}, "", ref, pinned.String(), destination); err != nil {
				t.Fatalf("PR Ref 推进后检出固定 Commit 失败: %v", err)
			}
			assertVersionFile(t, destination, "pinned pull request commit\n")
		})
	}
}

func TestCheckoutRejectsMissingPinnedCommitInsteadOfUsingRefTip(t *testing.T) {
	repositoryPath := t.TempDir()
	remote, err := git.PlainInit(repositoryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	commitVersionFile(t, remote, repositoryPath, "current branch tip\n", "current tip")

	destination := filepath.Join(t.TempDir(), "checkout")
	client := NewGitClient(config.Git{Timeout: 5 * time.Second})
	err = client.Checkout(context.Background(), model.GitRepository{
		CloneURL: repositoryPath, AuthType: model.GitAuthNone,
	}, "", "refs/heads/master", "ffffffffffffffffffffffffffffffffffffffff", destination)
	if err == nil {
		t.Fatal("固定 Commit 不存在时不应回退检出分支最新 tip")
	}
	if _, statErr := os.Stat(filepath.Join(destination, "version.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("固定 Commit 不存在时工作区不应出现分支最新内容: %v", statErr)
	}
}

func commitVersionFile(t *testing.T, repository *git.Repository, repositoryPath, content, message string) plumbing.Hash {
	t.Helper()
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, "version.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("version.txt"); err != nil {
		t.Fatal(err)
	}
	hash, err := worktree.Commit(message, &git.CommitOptions{Author: &object.Signature{
		Name: "ZRT Test", Email: "zrt@example.com", When: time.Now().UTC(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func assertVersionFile(t *testing.T, checkoutPath, expected string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(checkoutPath, "version.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != expected {
		t.Fatalf("检出的文件内容错误: got %q want %q", content, expected)
	}
}
