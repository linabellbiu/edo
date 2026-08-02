package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"

	"edo/internal/config"
	"edo/internal/model"
)

var scpURLPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)

const (
	maxRemoteRefs          = 100_000
	exactCheckoutReference = "refs/edo/checkout/exact"
)

var checkoutFallbackDepths = [...]int{1, 32, 128, 512, 2048, 4096}

type GitRef struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

type RefResult struct {
	Branches     []GitRef         `json:"branches"`
	Tags         []GitRef         `json:"tags"`
	PullRequests []PullRequestRef `json:"pull_requests,omitempty"`
	// PullRequestError 只在进程内传递 PR/MR 元数据同步诊断，不序列化到接口响应。
	PullRequestError error `json:"-"`
}

type PullRequestRef struct {
	Number       int64  `json:"number"`
	Ref          string `json:"ref"`
	SHA          string `json:"sha"`
	SourceBranch string `json:"source_branch,omitempty"`
	TargetBranch string `json:"target_branch,omitempty"`
	State        string `json:"state,omitempty"`
	Action       string `json:"action,omitempty"`
}

type GitClient struct {
	config config.Git
}

func NewGitClient(cfg config.Git) *GitClient { return &GitClient{config: cfg} }

func validateCloneURL(rawURL string, allowInsecureHTTP bool) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || len(rawURL) > 1024 || strings.HasPrefix(rawURL, "-") {
		return "", ErrInvalidRepository
	}
	if scpURLPattern.MatchString(rawURL) {
		return rawURL, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.Fragment != "" {
		return "", ErrInvalidRepository
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if parsed.User != nil {
			return "", ErrInvalidRepository
		}
	case "ssh":
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword || parsed.User.Username() == "" {
				return "", ErrInvalidRepository
			}
		}
	case "http":
		if parsed.User != nil {
			return "", ErrInvalidRepository
		}
		if !allowInsecureHTTP {
			return "", ErrInsecureRepository
		}
	default:
		return "", ErrInvalidRepository
	}
	return parsed.String(), nil
}

func (c *GitClient) ListRefs(
	ctx context.Context,
	repository model.GitRepository,
	credential string,
) (RefResult, error) {
	queryContext, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	auth, err := c.authMethod(repository, credential)
	if err != nil {
		return RefResult{}, err
	}
	remote := git.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{
		Name: "origin",
		URLs: []string{repository.CloneURL},
	})
	refs, err := remote.ListContext(queryContext, &git.ListOptions{
		Auth:          auth,
		PeelingOption: git.IgnorePeeled,
	})
	if err != nil {
		return RefResult{}, fmt.Errorf("查询 Git 远程引用失败: %w", err)
	}
	return refsToResult(refs)
}

func (c *GitClient) CommitMessage(
	ctx context.Context,
	repository model.GitRepository,
	credential, ref, commitSHA string,
) (string, error) {
	return c.commitMessage(ctx, repository, credential, ref, commitSHA, 1)
}

// HistoricalCommitMessage 仅在升级回填时使用；历史深度有明确上限，不下载完整仓库。
func (c *GitClient) HistoricalCommitMessage(
	ctx context.Context,
	repository model.GitRepository,
	credential, ref, commitSHA string,
) (string, error) {
	return c.commitMessage(ctx, repository, credential, ref, commitSHA, 100)
}

// 使用内存仓库避免把仅用于展示的 Git 对象写入工作目录。
func (c *GitClient) commitMessage(
	ctx context.Context,
	repository model.GitRepository,
	credential, ref, commitSHA string,
	depth int,
) (string, error) {
	queryContext, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	auth, err := c.authMethod(repository, credential)
	if err != nil {
		return "", err
	}
	referenceName := plumbing.ReferenceName(strings.TrimSpace(ref))
	if !referenceName.IsBranch() && !referenceName.IsTag() && !isPullRequestRef(referenceName.String()) {
		return "", errors.New("Git 引用格式无效")
	}
	hash := plumbing.NewHash(strings.TrimSpace(commitSHA))
	if hash.IsZero() {
		return "", errors.New("Git Commit 格式无效")
	}
	memoryRepository, err := git.Init(memory.NewStorage(), nil)
	if err != nil {
		return "", fmt.Errorf("初始化 Git 元数据仓库失败: %w", err)
	}
	remote, err := memoryRepository.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{repository.CloneURL}})
	if err != nil {
		return "", fmt.Errorf("创建 Git 元数据远端失败: %w", err)
	}
	localReference := plumbing.ReferenceName("refs/edo/commit-message")
	refspec := gitconfig.RefSpec("+" + referenceName.String() + ":" + localReference.String())
	if err := remote.FetchContext(queryContext, &git.FetchOptions{
		Auth: auth, RefSpecs: []gitconfig.RefSpec{refspec}, Depth: depth, Tags: git.NoTags,
	}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return "", fmt.Errorf("读取 Git 提交信息失败: %w", err)
	}
	commit, err := memoryRepository.CommitObject(hash)
	if err != nil {
		tag, tagErr := memoryRepository.TagObject(hash)
		if tagErr != nil {
			return "", fmt.Errorf("读取 Git Commit 失败: %w", err)
		}
		commit, err = memoryRepository.CommitObject(tag.Target)
		if err != nil {
			return "", fmt.Errorf("读取 Git Tag 目标 Commit 失败: %w", err)
		}
	}
	message := strings.TrimSpace(strings.SplitN(commit.Message, "\n", 2)[0])
	return truncateString(message, 255), nil
}

func (c *GitClient) Checkout(
	ctx context.Context,
	repository model.GitRepository,
	credential, ref, commitSHA, destination string,
) error {
	_, err := c.CheckoutCached(ctx, repository, credential, ref, commitSHA, destination)
	return err
}

// CheckoutCached keeps one bare object cache per repository and materializes an isolated,
// immutable workspace per Tag or Commit. It never switches a shared working tree.
func (c *GitClient) CheckoutCached(
	ctx context.Context,
	repository model.GitRepository,
	credential, ref, commitSHA, destination string,
) (bool, error) {
	return c.CheckoutCachedAt(ctx, repository, credential, ref, commitSHA, destination, filepath.Join(filepath.Dir(destination), ".cache"))
}

func (c *GitClient) CheckoutCachedAt(
	ctx context.Context,
	repository model.GitRepository,
	credential, ref, commitSHA, destination, cacheDirectory string,
) (bool, error) {
	checkoutContext, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	auth, err := c.authMethod(repository, credential)
	if err != nil {
		return false, err
	}
	referenceName := plumbing.ReferenceName(strings.TrimSpace(ref))
	if !referenceName.IsBranch() && !referenceName.IsTag() && !isPullRequestRef(referenceName.String()) {
		return false, errors.New("Git 引用格式无效")
	}
	commitSHA = strings.TrimSpace(commitSHA)
	if !plumbing.IsHash(commitSHA) {
		return false, errors.New("Git Commit 格式无效")
	}
	hash := plumbing.NewHash(commitSHA)
	if hash.IsZero() {
		return false, errors.New("Git Commit 格式无效")
	}
	if cachedWorkspaceMatches(cacheDirectory, destination, hash) {
		return true, nil
	}
	cachedRepository, remote, err := openCheckoutCache(cacheDirectory, repository.CloneURL)
	if err != nil {
		return false, err
	}

	// 优先直接请求固定 SHA，避免分支、Tag 或 PR Ref 推进后把构建静默切换到最新提交。
	exactRefspec := gitconfig.RefSpec("+" + hash.String() + ":" + exactCheckoutReference)
	exactErr := remote.FetchContext(checkoutContext, &git.FetchOptions{
		Auth: auth, RefSpecs: []gitconfig.RefSpec{exactRefspec}, Depth: 1, Tags: git.NoTags,
	})
	if exactErr == nil || errors.Is(exactErr, git.NoErrAlreadyUpToDate) {
		return materializeCachedCommit(cachedRepository, hash, cacheDirectory, destination)
	}

	// 部分服务端不允许按 SHA 请求对象。此时仅沿原始 Ref 有界补充历史，
	// 每一轮仍验证固定 SHA 是否存在，绝不把 Ref 当前 tip 当作替代结果。
	localReference := plumbing.ReferenceName("refs/edo/" + strings.TrimPrefix(referenceName.String(), "refs/"))
	refspec := gitconfig.RefSpec("+" + referenceName.String() + ":" + localReference.String())
	for _, depth := range checkoutFallbackDepths {
		if err := remote.FetchContext(checkoutContext, &git.FetchOptions{
			Auth: auth, RefSpecs: []gitconfig.RefSpec{refspec}, Depth: depth, Tags: git.NoTags,
		}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return false, fmt.Errorf("按 Git 引用补充固定 Commit 历史失败: %w", err)
		}
		if checkoutCommitAvailable(cachedRepository, hash) {
			return materializeCachedCommit(cachedRepository, hash, cacheDirectory, destination)
		}
	}
	return false, errors.New("指定的 Git Commit 不在该引用允许拉取的历史范围内")
}

func openCheckoutCache(directory, cloneURL string) (*git.Repository, *git.Remote, error) {
	if err := os.MkdirAll(filepath.Dir(directory), 0o700); err != nil {
		return nil, nil, fmt.Errorf("创建 Git 对象缓存目录失败: %w", err)
	}
	repository, err := git.PlainOpen(directory)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repository, err = git.PlainInit(directory, true)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("打开 Git 对象缓存失败: %w", err)
	}
	remote, err := repository.Remote("origin")
	if errors.Is(err, git.ErrRemoteNotFound) {
		remote, err = repository.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{cloneURL}})
	}
	if err != nil {
		return nil, nil, fmt.Errorf("打开 Git 缓存远端失败: %w", err)
	}
	configuration := remote.Config()
	if len(configuration.URLs) != 1 || configuration.URLs[0] != cloneURL {
		return nil, nil, errors.New("Git 对象缓存与当前仓库地址不匹配")
	}
	return repository, remote, nil
}

func materializeCachedCommit(
	repository *git.Repository,
	hash plumbing.Hash,
	cacheDirectory, destination string,
) (bool, error) {
	commit, err := pinnedCommitObject(repository, hash)
	if err != nil {
		return false, fmt.Errorf("读取 Git 固定 Commit 失败: %w", err)
	}
	markerDirectory := filepath.Join(cacheDirectory, "edo-workspaces")
	marker := filepath.Join(markerDirectory, filepath.Base(destination)+".commit")
	if cachedWorkspaceMatches(cacheDirectory, destination, hash) {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return false, fmt.Errorf("创建 Git 版本工作区目录失败: %w", err)
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".edo-workspace-*")
	if err != nil {
		return false, fmt.Errorf("创建 Git 版本临时工作区失败: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.RemoveAll(temporary)
		}
	}()
	tree, err := commit.Tree()
	if err != nil {
		return false, fmt.Errorf("读取 Git Commit 文件树失败: %w", err)
	}
	if err := materializeGitTree(repository, tree, temporary); err != nil {
		return false, err
	}
	if err := os.RemoveAll(destination); err != nil {
		return false, fmt.Errorf("替换 Git 版本工作区失败: %w", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		return false, fmt.Errorf("启用 Git 版本工作区失败: %w", err)
	}
	removeTemporary = false
	if err := os.MkdirAll(markerDirectory, 0o700); err != nil {
		return false, fmt.Errorf("创建 Git 工作区标记目录失败: %w", err)
	}
	markerTemporary, err := os.CreateTemp(markerDirectory, ".commit-*")
	if err != nil {
		return false, fmt.Errorf("创建 Git 工作区标记失败: %w", err)
	}
	markerTemporaryName := markerTemporary.Name()
	defer os.Remove(markerTemporaryName)
	if _, err := markerTemporary.WriteString(hash.String() + "\n"); err != nil {
		_ = markerTemporary.Close()
		return false, fmt.Errorf("写入 Git 工作区标记失败: %w", err)
	}
	if err := markerTemporary.Close(); err != nil {
		return false, fmt.Errorf("关闭 Git 工作区标记失败: %w", err)
	}
	if err := os.Rename(markerTemporaryName, marker); err != nil {
		return false, fmt.Errorf("更新 Git 工作区标记失败: %w", err)
	}
	return false, nil
}

func cachedWorkspaceMatches(cacheDirectory, destination string, hash plumbing.Hash) bool {
	marker := filepath.Join(cacheDirectory, "edo-workspaces", filepath.Base(destination)+".commit")
	content, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(content)) != hash.String() {
		return false
	}
	info, err := os.Stat(destination)
	return err == nil && info.IsDir()
}

func pinnedCommitObject(repository *git.Repository, hash plumbing.Hash) (*object.Commit, error) {
	commit, err := repository.CommitObject(hash)
	if err == nil {
		return commit, nil
	}
	tag, tagErr := repository.TagObject(hash)
	if tagErr != nil || tag.TargetType != plumbing.CommitObject {
		return nil, err
	}
	return repository.CommitObject(tag.Target)
}

func materializeGitTree(repository *git.Repository, tree *object.Tree, directory string) error {
	for _, entry := range tree.Entries {
		name := filepath.Clean(filepath.FromSlash(entry.Name))
		if name == "." || name == ".." || filepath.IsAbs(name) || filepath.Base(name) != name {
			return errors.New("Git 工作区包含无效文件路径")
		}
		target := filepath.Join(directory, name)
		switch entry.Mode {
		case filemode.Dir:
			child, err := repository.TreeObject(entry.Hash)
			if err != nil {
				return fmt.Errorf("读取 Git 子目录失败: %w", err)
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("创建 Git 工作区子目录失败: %w", err)
			}
			if err := materializeGitTree(repository, child, target); err != nil {
				return err
			}
		case filemode.Regular, filemode.Deprecated, filemode.Executable, filemode.Symlink:
			blob, err := repository.BlobObject(entry.Hash)
			if err != nil {
				return fmt.Errorf("读取 Git 文件对象失败: %w", err)
			}
			reader, err := blob.Reader()
			if err != nil {
				return fmt.Errorf("打开 Git 文件对象失败: %w", err)
			}
			if entry.Mode == filemode.Symlink {
				content, readErr := io.ReadAll(io.LimitReader(reader, 4097))
				closeErr := reader.Close()
				if readErr != nil || closeErr != nil || len(content) > 4096 {
					return errors.New("读取 Git 符号链接失败")
				}
				if err := os.Symlink(string(content), target); err != nil {
					return fmt.Errorf("创建 Git 工作区符号链接失败: %w", err)
				}
				continue
			}
			mode := os.FileMode(0o644)
			if entry.Mode == filemode.Executable {
				mode = 0o755
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				_ = reader.Close()
				return fmt.Errorf("创建 Git 工作区文件失败: %w", err)
			}
			_, copyErr := io.Copy(file, reader)
			readerCloseErr := reader.Close()
			fileCloseErr := file.Close()
			if copyErr != nil || readerCloseErr != nil || fileCloseErr != nil {
				return errors.New("写入 Git 工作区文件失败")
			}
		case filemode.Submodule:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("创建 Git 子模块占位目录失败: %w", err)
			}
		default:
			return fmt.Errorf("Git 工作区包含不支持的文件类型: %s", entry.Name)
		}
	}
	return nil
}

func checkoutCommitAvailable(repository *git.Repository, hash plumbing.Hash) bool {
	gitObject, err := repository.Object(plumbing.AnyObject, hash)
	if err != nil {
		return false
	}
	switch typed := gitObject.(type) {
	case *object.Commit:
		return true
	case *object.Tag:
		if typed.TargetType != plumbing.CommitObject {
			return false
		}
		_, err = repository.CommitObject(typed.Target)
		return err == nil
	default:
		return false
	}
}

func (c *GitClient) authMethod(repository model.GitRepository, credential string) (transport.AuthMethod, error) {
	switch repository.AuthType {
	case model.GitAuthNone:
		return nil, nil
	case model.GitAuthToken:
		username := repository.Username
		if username == "" {
			username = defaultTokenUsername(repository.Provider)
		}
		return &githttp.BasicAuth{Username: username, Password: credential}, nil
	case model.GitAuthSSHKey:
		if strings.TrimSpace(c.config.KnownHostsFile) == "" {
			return nil, ErrKnownHostsRequired
		}
		endpoint, err := transport.NewEndpoint(repository.CloneURL)
		if err != nil {
			return nil, fmt.Errorf("解析 SSH 仓库地址失败: %w", err)
		}
		username := endpoint.User
		if username == "" {
			username = "git"
		}
		publicKeys, err := gitssh.NewPublicKeys(username, []byte(credential), "")
		if err != nil {
			return nil, fmt.Errorf("解析 SSH 私钥失败: %w", err)
		}
		hostKeyCallback, err := gitssh.NewKnownHostsCallback(c.config.KnownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("读取 SSH known_hosts 文件失败: %w", err)
		}
		publicKeys.HostKeyCallback = hostKeyCallback
		return publicKeys, nil
	default:
		return nil, ErrInvalidCredential
	}
}

func refsToResult(refs []*plumbing.Reference) (RefResult, error) {
	if len(refs) > maxRemoteRefs {
		return RefResult{}, errors.New("Git 远程引用数量过多")
	}
	branches := make([]GitRef, 0)
	tags := make([]GitRef, 0)
	pullRequests := make(map[int64]PullRequestRef)
	for _, ref := range refs {
		name := ref.Name()
		switch {
		case name.IsBranch():
			branches = append(branches, GitRef{Name: name.Short(), SHA: ref.Hash().String()})
		case name.IsTag():
			tags = append(tags, GitRef{Name: name.Short(), SHA: ref.Hash().String()})
		default:
			if pullRequest, ok := parsePullRequestGitRef(name.String(), ref.Hash().String()); ok {
				// 同一 PR 同时暴露 head 和 merge 时使用 head，保证构建的是提交者实际推送的版本。
				if existing, exists := pullRequests[pullRequest.Number]; !exists || strings.HasSuffix(pullRequest.Ref, "/head") || strings.HasSuffix(existing.Ref, "/merge") {
					pullRequests[pullRequest.Number] = pullRequest
				}
			}
		}
	}
	sort.Slice(branches, func(i, j int) bool { return branches[i].Name < branches[j].Name })
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
	resultPullRequests := make([]PullRequestRef, 0, len(pullRequests))
	for _, pullRequest := range pullRequests {
		resultPullRequests = append(resultPullRequests, pullRequest)
	}
	sort.Slice(resultPullRequests, func(i, j int) bool { return resultPullRequests[i].Number < resultPullRequests[j].Number })
	return RefResult{Branches: branches, Tags: tags, PullRequests: resultPullRequests}, nil
}

func defaultTokenUsername(provider model.GitProvider) string {
	switch provider {
	case model.GitProviderGitHub:
		return "x-access-token"
	case model.GitProviderGitLab:
		return "oauth2"
	default:
		return "git"
	}
}

func truncateString(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
