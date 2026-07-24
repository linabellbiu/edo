package repository

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"

	"zrt/internal/config"
	"zrt/internal/model"
)

var scpURLPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)

const maxRemoteRefs = 100_000

type GitRef struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

type RefResult struct {
	Branches []GitRef `json:"branches"`
	Tags     []GitRef `json:"tags"`
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
	for _, ref := range refs {
		name := ref.Name()
		switch {
		case name.IsBranch():
			branches = append(branches, GitRef{Name: name.Short(), SHA: ref.Hash().String()})
		case name.IsTag():
			tags = append(tags, GitRef{Name: name.Short(), SHA: ref.Hash().String()})
		}
	}
	sort.Slice(branches, func(i, j int) bool { return branches[i].Name < branches[j].Name })
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
	return RefResult{Branches: branches, Tags: tags}, nil
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
