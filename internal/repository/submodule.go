package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"edo/internal/model"
)

// 子模块沿父提交的 gitlink 固定版本，不跟踪 .gitmodules 中的分支。
// 使用现有 go-git 解析、认证和对象缓存；凭据只在相同远端身份内复用。
func (c *GitClient) checkoutSubmodules(ctx context.Context, parent model.GitRepository, credential string, hash plumbing.Hash, directory, cacheDirectory string, depth int, count *int) error {
	repo, _, err := openCheckoutCache(cacheDirectory, parent.CloneURL)
	if err != nil {
		return err
	}
	commit, err := pinnedCommitObject(repo, hash)
	if err != nil {
		return fmt.Errorf("读取子模块父提交失败: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return fmt.Errorf("读取子模块文件树失败: %w", err)
	}
	links := map[string]plumbing.Hash{}
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	for {
		name, entry, err := walker.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("读取子模块版本失败: %w", err)
		}
		if entry.Mode == filemode.Submodule {
			links[name] = entry.Hash
		}
	}
	if len(links) == 0 {
		return nil
	}
	if depth >= 4 || *count+len(links) > 64 {
		return errors.New("Git 子模块数量或嵌套层级超过限制")
	}
	*count += len(links)
	file, err := tree.File(".gitmodules")
	if err != nil || file.Mode != filemode.Regular || file.Size > 64*1024 {
		return errors.New("Git 子模块配置缺失或无效")
	}
	content, err := file.Contents()
	if err != nil {
		return fmt.Errorf("读取 Git 子模块配置失败: %w", err)
	}
	modules := gitconfig.NewModules()
	if err := modules.Unmarshal([]byte(content)); err != nil {
		return fmt.Errorf("解析 Git 子模块配置失败: %w", err)
	}
	byPath := map[string]*gitconfig.Submodule{}
	for _, module := range modules.Submodules {
		if err := module.Validate(); err != nil || byPath[module.Path] != nil {
			return errors.New("Git 子模块路径无效或重复")
		}
		byPath[module.Path] = module
	}
	names := make([]string, 0, len(links))
	for name := range links {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		module := byPath[name]
		if module == nil {
			return errors.New("Git 子模块缺少对应配置")
		}
		target, err := submoduleDirectory(directory, name)
		if err != nil {
			return err
		}
		child, childCredential, err := submoduleRepository(parent, credential, module.URL)
		if err != nil {
			return err
		}
		key := sha256.Sum256([]byte(child.CloneURL))
		childCache := filepath.Join(cacheDirectory, "edo-submodules", fmt.Sprintf("%x", key))
		childRepo, remote, err := openCheckoutCache(childCache, child.CloneURL)
		if err != nil {
			return err
		}
		locked := links[name]
		if _, err := childRepo.CommitObject(locked); err != nil {
			auth, err := c.authMethod(child, childCredential)
			if err != nil {
				return err
			}
			if err := remote.FetchContext(ctx, &git.FetchOptions{Auth: auth, Depth: 1, Tags: git.NoTags, RefSpecs: []gitconfig.RefSpec{gitconfig.RefSpec("+" + locked.String() + ":" + exactCheckoutReference)}}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
				return fmt.Errorf("获取 Git 子模块固定版本失败: %w", err)
			}
		}
		if _, err := materializeCachedCommit(childRepo, locked, childCache, target); err != nil {
			return err
		}
		if err := c.checkoutSubmodules(ctx, child, childCredential, locked, target, childCache, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func submoduleRepository(parent model.GitRepository, credential, raw string) (model.GitRepository, string, error) {
	base, err := transport.NewEndpoint(parent.CloneURL)
	if err != nil {
		return model.GitRepository{}, "", errors.New("Git 子模块父仓库地址无效")
	}
	if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
		resolved := *base
		resolved.Path = path.Join(base.Path, raw)
		raw = resolved.String()
	}
	resolved, err := validateCloneURL(raw, parent.AllowInsecureHTTP)
	if err != nil {
		return model.GitRepository{}, "", errors.New("Git 子模块地址无效或未允许不安全连接")
	}
	endpoint, err := transport.NewEndpoint(resolved)
	if err != nil {
		return model.GitRepository{}, "", errors.New("Git 子模块地址无效")
	}
	child := parent
	child.CloneURL = resolved
	if endpoint.Protocol != base.Protocol || endpoint.Host != base.Host || endpoint.Port != base.Port || endpoint.User != base.User {
		child.AuthType, child.Username, credential = model.GitAuthNone, "", ""
	}
	return child, credential, nil
}

func submoduleDirectory(root, name string) (string, error) {
	if name == "" || path.Clean(name) != name || path.IsAbs(name) || strings.ContainsAny(name, "\\\x00") {
		return "", errors.New("Git 子模块路径无效")
	}
	current := root
	for _, segment := range strings.Split(name, "/") {
		if segment == "." || segment == ".." || strings.EqualFold(segment, ".git") {
			return "", errors.New("Git 子模块路径无效")
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("Git 子模块目录缺失或包含符号链接")
		}
	}
	return current, nil
}
