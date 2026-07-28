package repository

import (
	"context"
	"errors"
	"os"
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
