package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"edo/internal/cicd"
	"edo/internal/config"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/repository"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"time"
)

type composeDetectionCheckout struct {
	files       map[string]string
	err         error
	calls       int
	ref, commit string
}

func (c *composeDetectionCheckout) ListRefs(context.Context, model.GitRepository, string) (repository.RefResult, error) {
	return repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: strings.Repeat("a", 40)}, {Name: "release", SHA: strings.Repeat("b", 40)}}, Tags: []repository.GitRef{{Name: "v1", SHA: strings.Repeat("c", 40)}}}, nil
}
func (c *composeDetectionCheckout) Checkout(ctx context.Context, _ model.GitRepository, _, ref, commit, directory string) error {
	c.calls++
	c.ref, c.commit = ref, commit
	if c.err != nil {
		return c.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for name, body := range c.files {
		target := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if body == "SYMLINK" {
			if err := os.Symlink("/private/outside-compose.yaml", target); err != nil {
				return err
			}
			continue
		}
		if err := os.WriteFile(target, []byte(body), 0600); err != nil {
			return err
		}
	}
	return nil
}

func TestDetectRepositoryComposeFileRootAndPriority(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"标准名称优先", map[string]string{"compose.yaml": "secret", "compose.yml": "x", "docker-compose.yml": "x", "docker-compose.yaml": "x"}, "compose.yaml"},
		{"标准短后缀", map[string]string{"compose.yml": "x", "docker-compose.yaml": "x"}, "compose.yml"},
		{"传统短后缀优先", map[string]string{"docker-compose.yml": "x", "docker-compose.yaml": "x"}, "docker-compose.yml"},
		{"传统yaml后缀", map[string]string{"docker-compose.yaml": "secret"}, "docker-compose.yaml"},
		{"不递归或读override", map[string]string{"deploy/compose.yaml": "secret", "compose.override.yaml": "x"}, ""},
		{"不跟随链接", map[string]string{"compose.yaml": "SYMLINK", "docker-compose.yaml": "x"}, "docker-compose.yaml"},
		{"没有根目录定义", map[string]string{"README.md": "x"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, keys, repoID := newPipelineTestService(t)
			checkout := &composeDetectionCheckout{files: tc.files}
			s.repositories = repository.NewService(db, keys, credential.NewService(db, keys), checkout, 4, repository.WithCheckoutDirectory(t.TempDir()))
			authorizeRegistryTestActors(t, s, "admin")
			app, err := s.CreateApplication(deliveryTestContext(), "admin", ApplicationInput{Name: "compose_detect", RepositoryID: repoID})
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.DetectRepositoryComposeFile(deliveryTestContext(), app.ID, "main")
			if err != nil || result.Path != tc.want || result.CommitSHA != strings.Repeat("a", 40) {
				t.Fatalf("根目录检测错误: %+v %v", result, err)
			}
		})
	}
}

func TestDetectRepositoryComposeFileScopeRefsAndSafeFailure(t *testing.T) {
	s, db, keys, repoID := newPipelineTestService(t)
	checkout := &composeDetectionCheckout{files: map[string]string{"docker-compose.yaml": "private body"}}
	s.repositories = repository.NewService(db, keys, credential.NewService(db, keys), checkout, 4, repository.WithCheckoutDirectory(t.TempDir()))
	authorizeRegistryTestActors(t, s, "admin")
	ctx := deliveryTestContext()
	app, err := s.CreateApplication(ctx, "admin", ApplicationInput{Name: "compose_refs", RepositoryID: repoID})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ input, ref, commit string }{{"release", "refs/heads/release", "b"}, {"refs/tags/v1", "refs/tags/v1", "c"}, {"*", "refs/heads/main", "a"}} {
		result, err := s.DetectRepositoryComposeFile(ctx, app.ID, tc.input)
		if err != nil || result.CommitSHA != strings.Repeat(tc.commit, 40) || checkout.ref != tc.ref || checkout.commit != result.CommitSHA {
			t.Fatalf("未使用准确版本: %+v %v", result, err)
		}
	}
	calls := checkout.calls
	foreign := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "foreign", DepartmentID: app.DepartmentID, UserID: "admin"})
	if _, err := s.DetectRepositoryComposeFile(foreign, app.ID, "main"); !errors.Is(err, ErrApplicationNotFound) || checkout.calls != calls {
		t.Fatal("跨项目应用不能被检测", err)
	}
	if err := db.Create(&model.User{ID: "reader", Username: "compose_reader", DepartmentID: app.DepartmentID, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	reader := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: app.ProjectID, DepartmentID: app.DepartmentID, UserID: "reader"})
	if _, err := s.DetectRepositoryComposeFile(reader, app.ID, "main"); !errors.Is(err, cicd.ErrExecutionAuthorization) || checkout.calls != calls {
		t.Fatal("无仓库查看权限不得检出", err)
	}

	checkout.err = errors.New("PRIVATE_CHECKOUT_CANARY")
	if _, err := s.DetectRepositoryComposeFile(ctx, app.ID, "main"); err != ErrComposeSource || strings.Contains(err.Error(), "PRIVATE_CHECKOUT_CANARY") {
		t.Fatal("检测错误泄露内部细节", err)
	}
}

func TestDetectRepositoryComposeFileRealGitCheckout(t *testing.T) {
	s, db, keys, repoID := newPipelineTestService(t)
	directory := t.TempDir()
	remote, err := git.PlainInit(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	content := "services:\n  api:\n    image: alpine:3.22\n"
	if err := os.WriteFile(filepath.Join(directory, "docker-compose.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	tree, err := remote.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Add("docker-compose.yaml"); err != nil {
		t.Fatal(err)
	}
	commit, err := tree.Commit("测试根目录 Compose", &git.CommitOptions{Author: &object.Signature{Name: "EDO", Email: "test@example.invalid", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), commit)); err != nil {
		t.Fatal(err)
	}
	// 仅在隔离测试库将已创建仓库指向本地 Git，真实调用官方 Go Git 客户端检出。
	if err := db.Model(&model.GitRepository{}).Where("uuid = ?", repoID).Update("clone_url", directory).Error; err != nil {
		t.Fatal(err)
	}
	s.repositories = repository.NewService(db, keys, credential.NewService(db, keys), repository.NewGitClient(config.Git{Timeout: 10 * time.Second}), 4, repository.WithCheckoutDirectory(t.TempDir()))
	authorizeRegistryTestActors(t, s, "admin")
	app, err := s.CreateApplication(deliveryTestContext(), "admin", ApplicationInput{Name: "compose_real_git", RepositoryID: repoID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DetectRepositoryComposeFile(deliveryTestContext(), app.ID, "main")
	if err != nil || result.Path != "docker-compose.yaml" || result.CommitSHA != commit.String() {
		t.Fatalf("真实检出检测错误: %+v %v", result, err)
	}
	preview, err := s.PreviewComposeServices(deliveryTestContext(), app.ID, ComposeServicesInput{Source: &model.WorkflowComposeSource{Kind: "repository", Path: result.Path}, Ref: "main"})
	if err != nil || len(preview.Services) != 1 || preview.Services[0] != "api" || preview.CommitSHA != result.CommitSHA {
		t.Fatalf("检测路径未复用实际解析与固定版本: %+v %v", preview, err)
	}
}
