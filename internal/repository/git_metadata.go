package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"edo/internal/model"
)

// 元数据读取只缓存少量解码对象，大对象直接从磁盘流式读取。
const metadataObjectCacheBytes = 4 * cache.MiByte

type cachedCommitMessageClient interface {
	CommitMessageCachedAt(context.Context, model.GitRepository, string, string, string, string, bool) (string, error)
}

type cachedTagCommitClient interface {
	ResolveTagCommitCachedAt(context.Context, model.GitRepository, string, string, string, string) (string, error)
}

// CommitMessageCachedAt 与检出复用同一落盘对象库；调用方负责持有对应仓库缓存锁。
func (c *GitClient) CommitMessageCachedAt(ctx context.Context, repository model.GitRepository, credential, ref, commitSHA, directory string, historical bool) (string, error) {
	depth := 1
	if historical {
		depth = 100
	}
	return c.commitMessageCachedAt(ctx, repository, credential, ref, commitSHA, directory, depth)
}

func (c *GitClient) commitMessageCachedAt(ctx context.Context, repository model.GitRepository, credential, ref, commitSHA, directory string, depth int) (string, error) {
	commit, err := c.fixedCommitCachedAt(ctx, repository, credential, ref, commitSHA, directory, depth)
	if err != nil {
		return "", err
	}
	return commitSubject(commit.Message), nil
}

// ResolveTagCommitCachedAt 只解析事件携带的固定对象，不以 Tag 当前指向代替。
func (c *GitClient) ResolveTagCommitCachedAt(ctx context.Context, repository model.GitRepository, credential, ref, objectSHA, directory string) (string, error) {
	if !plumbing.ReferenceName(strings.TrimSpace(ref)).IsTag() {
		return "", errors.New("Git Tag 引用格式无效")
	}
	commit, err := c.fixedCommitCachedAt(ctx, repository, credential, ref, objectSHA, directory, 1)
	if err != nil {
		return "", err
	}
	return commit.Hash.String(), nil
}

func (c *GitClient) fixedCommitCachedAt(ctx context.Context, repository model.GitRepository, credential, ref, commitSHA, directory string, depth int) (*object.Commit, error) {
	queryContext, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	if err := queryContext.Err(); err != nil {
		return nil, err
	}
	auth, err := c.authMethod(repository, credential)
	if err != nil {
		return nil, err
	}
	reference := plumbing.ReferenceName(strings.TrimSpace(ref))
	if !reference.IsBranch() && !reference.IsTag() && !isPullRequestRef(reference.String()) {
		return nil, errors.New("Git 引用格式无效")
	}
	commitSHA = strings.TrimSpace(commitSHA)
	if !plumbing.IsHash(commitSHA) || plumbing.NewHash(commitSHA).IsZero() {
		return nil, errors.New("Git Commit 格式无效")
	}
	if directory == "" {
		directory, err = os.MkdirTemp("", "edo-git-metadata-*")
		if err != nil {
			return nil, fmt.Errorf("创建 Git 元数据临时目录失败: %w", err)
		}
		defer os.RemoveAll(directory)
	}
	opened, _, err := openCheckoutCache(directory, repository.CloneURL)
	if err != nil {
		return nil, err
	}
	// PlainOpen 的默认对象 LRU 为 96 MiB；标题查询使用较小缓存并允许大对象流式读取。
	storage := opened.Storer.(*filesystem.Storage)
	bounded := filesystem.NewStorageWithOptions(storage.Filesystem(), cache.NewObjectLRU(metadataObjectCacheBytes), filesystem.Options{
		LargeObjectThreshold: 1 << 20,
	})
	defer bounded.Close()
	objects, err := git.Open(bounded, nil)
	if err != nil {
		return nil, fmt.Errorf("打开 Git 元数据缓存失败: %w", err)
	}
	remote, err := objects.Remote("origin")
	if err != nil {
		return nil, fmt.Errorf("读取 Git 元数据远端失败: %w", err)
	}
	hash := plumbing.NewHash(commitSHA)
	if commit, err := pinnedCommitObject(objects, hash); err == nil {
		return commit, nil
	} else if !errors.Is(err, plumbing.ErrObjectNotFound) {
		return nil, fmt.Errorf("读取 Git 元数据缓存失败: %w", err)
	}
	// 先固定 SHA；Ref 推进后也不能把旧运行的标题替换成最新提交的标题。
	exact := gitconfig.RefSpec("+" + hash.String() + ":refs/edo/metadata/exact")
	fetchErr := remote.FetchContext(queryContext, &git.FetchOptions{Auth: auth, RefSpecs: []gitconfig.RefSpec{exact}, Depth: 1, Tags: git.NoTags})
	if fetchErr == nil || errors.Is(fetchErr, git.NoErrAlreadyUpToDate) {
		commit, err := pinnedCommitObject(objects, hash)
		if err != nil {
			return nil, fmt.Errorf("读取 Git 固定 Commit 失败: %w", err)
		}
		return commit, nil
	}
	if !errors.Is(fetchErr, git.ErrExactSHA1NotSupported) {
		return nil, fmt.Errorf("读取 Git 提交信息失败: %w", fetchErr)
	}
	if err := queryContext.Err(); err != nil {
		return nil, err
	}
	// 仅在服务端明确不支持按 SHA 获取时回退；认证、证书、网络和协议错误不得重复请求。
	refspec := gitconfig.RefSpec("+" + reference.String() + ":refs/edo/metadata/ref")
	if err := remote.FetchContext(queryContext, &git.FetchOptions{Auth: auth, RefSpecs: []gitconfig.RefSpec{refspec}, Depth: depth, Tags: git.NoTags}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil, fmt.Errorf("读取 Git 提交信息失败: %w", err)
	}
	commit, err := pinnedCommitObject(objects, hash)
	if err != nil {
		return nil, fmt.Errorf("读取 Git 固定 Commit 失败: %w", err)
	}
	return commit, nil
}

func commitSubject(message string) string {
	subject, _, _ := strings.Cut(message, "\n")
	subject = strings.TrimSpace(subject)
	characters := 0
	for offset := range subject {
		if characters == 255 {
			subject = subject[:offset]
			break
		}
		characters++
	}
	// 防止短标题仍持有整个大提交说明的底层字符串。
	return strings.Clone(subject)
}
