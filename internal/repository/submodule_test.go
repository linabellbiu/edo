package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"edo/internal/config"
	"edo/internal/model"
)

func TestSubmoduleCredentialsStayOnSameRemoteIdentity(t *testing.T) {
	parent := model.GitRepository{CloneURL: "https://git.example.com/team/app.git", AuthType: model.GitAuthToken, Username: "owner"}
	for _, tc := range []struct {
		url, want string
		reuse     bool
	}{
		{"../rules.git", "https://git.example.com/team/rules.git", true},
		{"https://git.example.com/other/rules.git", "https://git.example.com/other/rules.git", true},
		{"https://github.com/public/rules.git", "https://github.com/public/rules.git", false},
		{"https://git.example.com:8443/team/rules.git", "https://git.example.com:8443/team/rules.git", false},
	} {
		child, credential, err := submoduleRepository(parent, "private-test-token", tc.url)
		if err != nil || child.CloneURL != tc.want || (credential != "") != tc.reuse {
			t.Fatalf("子模块地址或认证边界错误: url=%s reuse=%v err=%v", tc.url, tc.reuse, err)
		}
		if !tc.reuse && (child.AuthType != model.GitAuthNone || child.Username != "") {
			t.Fatal("跨远端子模块继承了认证信息")
		}
	}
	for _, raw := range []string{"file:///etc", "/srv/repo", "http://git.example.com/rules.git", "https://owner:password@git.example.com/rules.git"} {
		if _, _, err := submoduleRepository(parent, "private-test-token", raw); err == nil {
			t.Fatalf("危险子模块地址未被拒绝: %s", raw)
		}
	}
}

func TestSubmoduleDirectoryRejectsSymlinkAndTraversal(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "rules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../rules", "/rules", "a/../rules", ".git", "linked", "linked/rules"} {
		if _, err := submoduleDirectory(root, name); err == nil {
			t.Fatalf("危险子模块路径未被拒绝: %s", name)
		}
	}
}

func TestSubmoduleCheckoutUsesLockedGitlinkAndIsolatedWorkspace(t *testing.T) {
	cache := t.TempDir()
	parent := model.GitRepository{CloneURL: "https://git.example.com/team/app.git", AuthType: model.GitAuthNone}
	childURL := "https://github.com/public/rules.git"
	key := sha256.Sum256([]byte(childURL))
	child, _, err := openCheckoutCache(filepath.Join(cache, "edo-submodules", fmt.Sprintf("%x", key)), childURL)
	if err != nil {
		t.Fatal(err)
	}
	locked := writeSubmoduleTestCommit(t, child, []object.TreeEntry{{Name: "rule.txt", Mode: filemode.Regular, Hash: writeSubmoduleTestBlob(t, child, "locked")}})
	// 缓存里还存在其他版本，必须使用父提交固定的 gitlink。
	_ = writeSubmoduleTestCommit(t, child, []object.TreeEntry{{Name: "rule.txt", Mode: filemode.Regular, Hash: writeSubmoduleTestBlob(t, child, "newer")}})
	root, _, err := openCheckoutCache(cache, parent.CloneURL)
	if err != nil {
		t.Fatal(err)
	}
	modules := "[submodule \"rules\"]\npath = rules\nurl = " + childURL + "\nbranch = main\n"
	commit := writeSubmoduleTestCommit(t, root, []object.TreeEntry{
		{Name: ".gitmodules", Mode: filemode.Regular, Hash: writeSubmoduleTestBlob(t, root, modules)},
		{Name: "rules", Mode: filemode.Submodule, Hash: locked},
	})
	client := NewGitClient(config.Git{Timeout: time.Second})
	for range 2 {
		workspace := t.TempDir()
		if _, err := materializeCachedCommit(root, commit, cache, workspace); err != nil {
			t.Fatal(err)
		}
		if err := client.checkoutSubmodules(context.Background(), parent, "", commit, workspace, cache, 0, new(int)); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(workspace, "rules", "rule.txt"))
		if err != nil || string(content) != "locked" {
			t.Fatalf("子模块未检出固定版本或工作区串用: err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(workspace, "rules", ".git")); !os.IsNotExist(err) {
			t.Fatal("子模块工作区不应携带 Git 配置")
		}
	}
}

func writeSubmoduleTestBlob(t *testing.T, repo *git.Repository, content string) plumbing.Hash {
	t.Helper()
	encoded := repo.Storer.NewEncodedObject()
	blob := &object.Blob{}
	blob.Size = int64(len(content))
	encoded.SetType(plumbing.BlobObject)
	encoded.SetSize(int64(len(content)))
	w, err := encoded.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	hash, err := repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func writeSubmoduleTestCommit(t *testing.T, repo *git.Repository, entries []object.TreeEntry) plumbing.Hash {
	t.Helper()
	tree := &object.Tree{Entries: entries}
	encoded := repo.Storer.NewEncodedObject()
	if err := tree.Encode(encoded); err != nil {
		t.Fatal(err)
	}
	hash, err := repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	commit := &object.Commit{TreeHash: hash, Message: "子模块测试", Author: object.Signature{Name: "EDO", Email: "edo@example.com", When: time.Now()}, Committer: object.Signature{Name: "EDO", Email: "edo@example.com", When: time.Now()}}
	encoded = repo.Storer.NewEncodedObject()
	if err := commit.Encode(encoded); err != nil {
		t.Fatal(err)
	}
	hash, err = repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
