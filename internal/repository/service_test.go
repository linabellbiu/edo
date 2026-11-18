package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/testutil"
)

func TestRepositorySecretsAreEncryptedAndURLsAreValidated(t *testing.T) {
	service, db := newRepositoryTestService(t)
	token := "provider-access-token"
	repo, err := service.Create(context.Background(), "admin", Input{
		Name: "production-api", Provider: model.GitProviderGitHub,
		CloneURL: "https://github.example.com/team/api.git", AuthType: model.GitAuthToken,
		Credential: &token,
	})
	if err != nil {
		t.Fatalf("创建代码仓库失败: %v", err)
	}
	if repo.CredentialCiphertext == token {
		t.Fatal("代码仓库密钥未加密或未生成")
	}
	var stored model.GitRepository
	if err := db.First(&stored, "uuid = ?", repo.ID).Error; err != nil {
		t.Fatalf("读取代码仓库失败: %v", err)
	}
	if stored.CredentialCiphertext == token {
		t.Fatal("数据库中出现明文密钥")
	}

	credentialURL := "https://user:secret@example.com/team/api.git"
	_, err = service.Create(context.Background(), "admin", Input{
		Name: "bad-credentials", Provider: model.GitProviderGeneric,
		CloneURL: credentialURL, AuthType: model.GitAuthNone,
	})
	if !errors.Is(err, ErrInvalidRepository) {
		t.Fatalf("URL 中的明文凭据未被拒绝: %v", err)
	}
	_, err = service.Create(context.Background(), "admin", Input{
		Name: "bad-http", Provider: model.GitProviderGeneric,
		CloneURL: "http://git.example.com/team/api.git", AuthType: model.GitAuthNone,
	})
	if !errors.Is(err, ErrInsecureRepository) {
		t.Fatalf("未确认的不安全 HTTP 仓库未被拒绝: %v", err)
	}
}

func TestRepositoryNameSupportsChinese(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	service.git = staticRefLister{result: RefResult{Branches: []GitRef{{Name: "main"}}}}
	input := Input{
		Name: "我的", Provider: model.GitProviderGitea,
		CloneURL: "https://git.example.com/team/project.git", AuthType: model.GitAuthNone,
	}
	if _, err := service.TestInput(context.Background(), "admin", input); err != nil {
		t.Fatalf("连接测试不应拒绝中文仓库名称: %v", err)
	}
	repo, err := service.Create(context.Background(), "admin", input)
	if err != nil {
		t.Fatalf("创建仓库不应拒绝中文名称: %v", err)
	}
	if repo.Name != "我的" {
		t.Fatalf("中文仓库名称未正确保存: %+v", repo)
	}

	input.Name = "错误/名称"
	if _, err := service.TestInput(context.Background(), "admin", input); !errors.Is(err, ErrInvalidRepositoryName) {
		t.Fatalf("连接测试应返回明确的仓库名称错误: %v", err)
	}
	if _, err := service.Create(context.Background(), "admin", input); !errors.Is(err, ErrInvalidRepositoryName) {
		t.Fatalf("创建仓库应返回相同的名称错误: %v", err)
	}
}

func TestRepositoryCanBeUpdatedAndDeletedWhenUnused(t *testing.T) {
	service, db := newRepositoryTestService(t)
	repo, err := service.Create(context.Background(), "admin", Input{
		Name: "待修改仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/old.git", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatalf("创建待修改仓库失败: %v", err)
	}
	const legacyCiphertext = "obsolete-webhook-test-ciphertext"
	if err := db.Exec("UPDATE git_repositories SET webhook_secret_ciphertext = ? WHERE uuid = ?", legacyCiphertext, repo.ID).Error; err != nil {
		t.Fatalf("写入历史 Webhook 字段失败: %v", err)
	}
	updated, err := service.Update(context.Background(), "admin", repo.ID, Input{
		Name: "已修改仓库", Provider: model.GitProviderGitea,
		CloneURL: "https://git.example.com/team/new.git", DefaultBranch: "develop",
		AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatalf("修改代码仓库失败: %v", err)
	}
	if updated.Name != "已修改仓库" || updated.Provider != model.GitProviderGitea ||
		updated.CloneURL != "https://git.example.com/team/new.git" || updated.DefaultBranch != "develop" {
		t.Fatalf("修改后的代码仓库内容不正确: %+v", updated)
	}
	var storedCiphertext string
	if err := db.Raw("SELECT webhook_secret_ciphertext FROM git_repositories WHERE uuid = ?", repo.ID).Scan(&storedCiphertext).Error; err != nil {
		t.Fatalf("检查历史 Webhook 字段失败: %v", err)
	}
	if storedCiphertext != legacyCiphertext || updated.LegacyWebhookSecretCiphertext != "" {
		t.Fatal("修改仓库时应保留数据库历史值，仓库服务不得读取该字段")
	}

	now := time.Now().UTC()
	application := model.Application{
		ID: "application-delete", Name: "引用仓库的应用", RepositoryID: repo.ID,
		SyncStatus: model.ApplicationSyncIdle, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&application).Error; err != nil {
		t.Fatalf("创建仓库引用失败: %v", err)
	}
	if err := service.Delete(context.Background(), repo.ID); !errors.Is(err, ErrRepositoryInUse) {
		t.Fatalf("使用中的代码仓库未阻止删除: %v", err)
	}
	if err := db.Delete(&application).Error; err != nil {
		t.Fatalf("清理仓库引用失败: %v", err)
	}
	if err := service.Delete(context.Background(), repo.ID); err != nil {
		t.Fatalf("删除未使用的代码仓库失败: %v", err)
	}
	if err := service.Delete(context.Background(), repo.ID); !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("重复删除仓库未返回不存在: %v", err)
	}
}

func TestRepositoryUsesOwnedCredential(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	token := "saved-provider-token"
	saved, err := service.credentials.Create(context.Background(), "user-a", credential.Input{
		Name: "GitHub 生产令牌", Provider: model.GitProviderGitHub, AuthType: model.GitAuthToken, Secret: &token,
	})
	if err != nil {
		t.Fatalf("创建个人令牌失败: %v", err)
	}
	repo, err := service.Create(context.Background(), "user-a", Input{
		Name: "owned-token-repository", Provider: model.GitProviderGitHub,
		CloneURL: "https://github.com/example/project.git", AuthType: model.GitAuthToken,
		CredentialID: &saved.ID,
	})
	if err != nil {
		t.Fatalf("引用个人令牌创建仓库失败: %v", err)
	}
	if repo.CredentialID == nil || *repo.CredentialID != saved.ID || repo.CredentialCiphertext != "" {
		t.Fatalf("仓库未正确引用个人令牌: %+v", repo)
	}
	_, err = service.Create(context.Background(), "user-b", Input{
		Name: "foreign-token-repository", Provider: model.GitProviderGitHub,
		CloneURL: "https://github.com/example/other.git", AuthType: model.GitAuthToken, CredentialID: &saved.ID,
	})
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("其他用户引用非本人令牌未被拒绝: %v", err)
	}
}

func TestRepositorySeparatesCloneAndPlatformAPICredentials(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	privateKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-key-material\n-----END OPENSSH PRIVATE KEY-----"
	apiToken := "private-platform-api-token"
	foreignToken := "foreign-platform-api-token"
	sshCredential, err := service.credentials.Create(context.Background(), "user-a", credential.Input{
		Name: "GitHub SSH", Provider: model.GitProviderGitHub, AuthType: model.GitAuthSSHKey, Secret: &privateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	apiCredential, err := service.credentials.Create(context.Background(), "user-a", credential.Input{
		Name: "GitHub API", Provider: model.GitProviderGitHub, AuthType: model.GitAuthToken, Secret: &apiToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignCredential, err := service.credentials.Create(context.Background(), "user-b", credential.Input{
		Name: "其他用户 API", Provider: model.GitProviderGitHub, AuthType: model.GitAuthToken, Secret: &foreignToken,
	})
	if err != nil {
		t.Fatal(err)
	}

	lister := &credentialCapturingRefLister{refs: RefResult{
		Branches: []GitRef{{Name: "main", SHA: "main-sha"}},
		PullRequests: []PullRequestRef{{
			Number: 1, Ref: "refs/pull/1/head", SHA: "pr-sha", SourceBranch: "feature/private", TargetBranch: "main",
		}},
	}}
	service.git = lister
	repo, err := service.Create(context.Background(), "user-a", Input{
		Name: "private-ssh-repository", Provider: model.GitProviderGitHub,
		CloneURL: "git@github.com:example/private.git", AuthType: model.GitAuthSSHKey,
		CredentialID: &sshCredential.ID, APICredentialID: &apiCredential.ID,
	})
	if err != nil {
		t.Fatalf("创建 SSH 私有仓库失败: %v", err)
	}
	if repo.APICredentialID == nil || *repo.APICredentialID != apiCredential.ID {
		t.Fatalf("平台 API 令牌引用未保存: %+v", repo)
	}
	refs, err := service.PollState(context.Background(), repo.ID, true)
	if err != nil || refs.PullRequestError != nil || len(refs.PullRequests) != 1 {
		t.Fatalf("使用独立 API 令牌轮询失败: refs=%+v err=%v", refs, err)
	}
	if lister.cloneCredential != privateKey || lister.apiCredential != apiToken {
		t.Fatalf("克隆凭据与 API 令牌未隔离: clone=%q api=%q", lister.cloneCredential, lister.apiCredential)
	}

	_, err = service.Create(context.Background(), "user-a", Input{
		Name: "foreign-api-create", Provider: model.GitProviderGitHub,
		CloneURL: "git@github.com:example/foreign.git", AuthType: model.GitAuthSSHKey,
		CredentialID: &sshCredential.ID, APICredentialID: &foreignCredential.ID,
	})
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("创建仓库时引用其他用户 API 令牌未被拒绝: %v", err)
	}
	_, err = service.Create(context.Background(), "user-a", Input{
		Name: "ssh-as-api", Provider: model.GitProviderGitHub,
		CloneURL: "https://github.com/example/public.git", AuthType: model.GitAuthNone,
		APICredentialID: &sshCredential.ID,
	})
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("SSH 私钥被错误接受为平台 API 令牌: %v", err)
	}
	_, err = service.Create(context.Background(), "user-a", Input{
		Name: "provider-mismatch-api", Provider: model.GitProviderGitea,
		CloneURL: "https://gitea.example.com/example/public.git", AuthType: model.GitAuthNone,
		APICredentialID: &apiCredential.ID,
	})
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("平台不匹配的 API 令牌未被拒绝: %v", err)
	}
	_, err = service.Update(context.Background(), "user-a", repo.ID, Input{
		Name: repo.Name, Provider: repo.Provider, CloneURL: repo.CloneURL, AuthType: repo.AuthType,
		CredentialID: &sshCredential.ID, APICredentialID: &foreignCredential.ID,
	})
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("更新仓库时引用其他用户 API 令牌未被拒绝: %v", err)
	}
	emptyCredentialID := ""
	updated, err := service.Update(context.Background(), "user-a", repo.ID, Input{
		Name: repo.Name, Provider: repo.Provider, CloneURL: repo.CloneURL, AuthType: repo.AuthType,
		CredentialID: &sshCredential.ID, APICredentialID: &emptyCredentialID,
	})
	if err != nil || updated.APICredentialID != nil {
		t.Fatalf("显式清空平台 API 令牌失败: repository=%+v err=%v", updated, err)
	}
}

func TestTokenCloneCredentialIsDefaultPlatformAPIToken(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	token := "shared-clone-and-api-token"
	credentialItem, err := service.credentials.Create(context.Background(), "user-a", credential.Input{
		Name: "Gitea Token", Provider: model.GitProviderGitea, AuthType: model.GitAuthToken, Secret: &token,
	})
	if err != nil {
		t.Fatal(err)
	}
	lister := &credentialCapturingRefLister{}
	service.git = lister
	repo, err := service.Create(context.Background(), "user-a", Input{
		Name: "token-repository", Provider: model.GitProviderGitea,
		CloneURL: "https://gitea.example.com/team/private.git", AuthType: model.GitAuthToken,
		CredentialID: &credentialItem.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PollState(context.Background(), repo.ID, true); err != nil {
		t.Fatal(err)
	}
	if lister.cloneCredential != token || lister.apiCredential != token {
		t.Fatalf("Token 克隆凭据未默认复用于平台 API: clone=%q api=%q", lister.cloneCredential, lister.apiCredential)
	}
}

func TestCheckoutDirectoryVersionUsesTagOrCommit(t *testing.T) {
	commit := "0123456789012345678901234567890123456789"
	if got := checkoutDirectoryVersion("refs/tags/v1.0.10", commit); got != "v1.0.10" {
		t.Fatalf("Tag 工作区没有使用版本号: %q", got)
	}
	if got := checkoutDirectoryVersion("refs/heads/main", commit); got != commit {
		t.Fatalf("分支工作区没有使用固定 Commit: %q", got)
	}
	if got := checkoutDirectoryVersion("refs/tags/release/1", commit); !strings.HasPrefix(got, "version-") {
		t.Fatalf("包含路径分隔符的 Tag 没有安全哈希: %q", got)
	}
}

func TestCheckoutUsesSeparateCacheAndCleanupWaitsForActiveBuild(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	client := &separatedCheckoutStub{}
	service.git = client
	repository, err := service.Create(context.Background(), "admin", Input{
		Name: "目录测试仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://git.example.com/team/directory.git", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatalf("创建目录测试仓库失败: %v", err)
	}
	directories, err := service.PrepareDirectories(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("准备仓库运行目录失败: %v", err)
	}
	service.ApplyDirectories(directories)
	lease, err := service.AcquireCheckout(
		context.Background(), repository.ID, "refs/heads/main", strings.Repeat("a", 40),
	)
	if err != nil {
		t.Fatalf("检出目录测试仓库失败: %v", err)
	}
	if !strings.HasPrefix(client.cacheDirectory, directories.CacheDirectory+string(filepath.Separator)) ||
		!strings.HasPrefix(lease.Directory, directories.WorkspaceDirectory+string(filepath.Separator)) {
		t.Fatalf("仓库工作区和缓存未分开: lease=%s cache=%s", lease.Directory, client.cacheDirectory)
	}
	if _, err := service.ClearWorkspaceDirectory(); !errors.Is(err, ErrDirectoryBusy) {
		t.Fatalf("活动构建期间仍允许清理目录: %v", err)
	}
	lease.Release()
	buildReport, err := service.ClearWorkspaceDirectory()
	if err != nil || buildReport.FilesDeleted != 1 {
		t.Fatalf("清理仓库构建目录失败: report=%+v err=%v", buildReport, err)
	}
	cacheReport, err := service.ClearCacheDirectory()
	if err != nil || cacheReport.FilesDeleted != 1 {
		t.Fatalf("清理仓库缓存目录失败: report=%+v err=%v", cacheReport, err)
	}
}

func TestRepositoryInputCanBeTestedWithoutSaving(t *testing.T) {
	service, db := newRepositoryTestService(t)
	service.git = staticRefLister{result: RefResult{
		Branches: []GitRef{{Name: "main", SHA: "0123456789012345678901234567890123456789"}},
		Tags:     []GitRef{{Name: "v1.0.0", SHA: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}},
	}}

	result, err := service.TestInput(context.Background(), "admin", Input{
		Name: "临时仓库", Provider: model.GitProviderGeneric, CloneURL: "https://git.example.com/team/api.git", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatalf("测试未保存仓库失败: %v", err)
	}
	if len(result.Branches) != 1 || result.Branches[0].Name != "main" || len(result.Tags) != 1 || result.Tags[0].Name != "v1.0.0" {
		t.Fatalf("未正确读取未保存仓库的引用: %+v", result)
	}
	var count int64
	if err := db.Model(&model.GitRepository{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("测试仓库连接不应写入数据库: count=%d err=%v", count, err)
	}

	if _, err := service.TestInput(context.Background(), "admin", Input{
		Name: "令牌仓库", Provider: model.GitProviderGitHub, CloneURL: "https://github.example.com/team/api.git", AuthType: model.GitAuthToken,
	}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("缺少令牌的仓库测试未被拒绝: %v", err)
	}
}

func newRepositoryTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db := testutil.NewDatabase(t)
	// 个人凭据必须有真实账户行；无凭据仓库仍不依赖这些单测命名账户。
	for _, id := range []string{"user-a", "user-b", "owner", "token-owner", "different-editor"} {
		now := time.Now().UTC()
		if err := db.Create(&model.User{ID: id, Username: "repo-" + id, Nickname: id,
			DepartmentID: database.DefaultDepartmentID, IsActive: true, AuthVersion: 1,
			PasswordHash: "unused-unit-test-hash", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatalf("创建仓库单测凭据所有者失败: %v", err)
		}
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	secretManager, err := secret.New(key)
	if err != nil {
		t.Fatalf("初始化仓库测试密钥失败: %v", err)
	}
	credentialService := credential.NewService(db, secretManager)
	return NewService(
		db, secretManager, credentialService, NewGitClient(config.Git{Timeout: time.Second}),
	), db
}

type staticRefLister struct {
	result RefResult
	err    error
}

type separatedCheckoutStub struct {
	cacheDirectory string
}

func (s *separatedCheckoutStub) ListRefs(context.Context, model.GitRepository, string) (RefResult, error) {
	return RefResult{}, nil
}

func (s *separatedCheckoutStub) Checkout(
	ctx context.Context,
	repository model.GitRepository,
	credential, ref, commitSHA, destination string,
) error {
	_, err := s.CheckoutCachedAt(ctx, repository, credential, ref, commitSHA, destination, filepath.Join(filepath.Dir(destination), ".cache"))
	return err
}

func (s *separatedCheckoutStub) CheckoutCachedAt(
	_ context.Context,
	_ model.GitRepository,
	_, _, _, destination, cacheDirectory string,
) (bool, error) {
	s.cacheDirectory = cacheDirectory
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(destination, "source.go"), []byte("package main"), 0o600); err != nil {
		return false, err
	}
	if err := os.MkdirAll(cacheDirectory, 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(cacheDirectory, "pack"), []byte("cache"), 0o600); err != nil {
		return false, err
	}
	return false, nil
}

type credentialCapturingRefLister struct {
	refs            RefResult
	pullRequestErr  error
	cloneCredential string
	apiCredential   string
}

func (l *credentialCapturingRefLister) ListRefs(_ context.Context, _ model.GitRepository, credential string) (RefResult, error) {
	l.cloneCredential = credential
	return l.refs, nil
}

func (l *credentialCapturingRefLister) ListPullRequests(_ context.Context, _ model.GitRepository, cloneCredential, apiCredential string) ([]PullRequestRef, error) {
	l.cloneCredential = cloneCredential
	l.apiCredential = apiCredential
	return l.refs.PullRequests, l.pullRequestErr
}

func TestPollStatePartialFailureDoesNotTrustGitPullRefs(t *testing.T) {
	service, _ := newRepositoryTestService(t)
	repo, err := service.Create(context.Background(), "admin", Input{
		Name: "partial-pr-metadata", Provider: model.GitProviderGitee,
		CloneURL: "https://git.example.com/team/app.git", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	verified := PullRequestRef{Number: 2, Ref: "refs/pull/2/head", SHA: "verified-head", SourceBranch: "feature/valid", TargetBranch: "main", Action: "opened"}
	for _, partial := range []bool{false, true} {
		lister := &credentialCapturingRefLister{refs: RefResult{
			Branches:     []GitRef{{Name: "main", SHA: "main-sha"}},
			PullRequests: []PullRequestRef{{Number: 1, Ref: "refs/pull/1/head", SHA: "unverified-head"}},
		}, pullRequestErr: ErrPullRequestMetadata}
		if partial {
			lister.pullRequestErr = &partialPullRequestError{validated: []PullRequestRef{verified}, err: ErrPullRequestMetadata}
		}
		service.git = lister
		refs, err := service.PollState(context.Background(), repo.ID, true)
		if err != nil || !errors.Is(refs.PullRequestError, ErrPullRequestMetadata) || len(refs.Branches) != 1 {
			t.Fatalf("PR 诊断或普通分支丢失: refs=%+v err=%v", refs, err)
		}
		if partial {
			if len(refs.PullRequests) != 1 || refs.PullRequests[0] != verified {
				t.Fatalf("部分成功只能接收明确已验证的 API 记录: %+v", refs.PullRequests)
			}
		} else if len(refs.PullRequests) != 0 {
			t.Fatalf("API 失败时不得回退到裸 Git Ref: %+v", refs.PullRequests)
		}
	}
}

func (s staticRefLister) ListRefs(context.Context, model.GitRepository, string) (RefResult, error) {
	return s.result, s.err
}
