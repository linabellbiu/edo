package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"zrt/internal/model"
)

const maxOpenPullRequests = 1000

var (
	pullRefPattern         = regexp.MustCompile(`^refs/pull/([0-9]+)/(head|merge)$`)
	mergeRequestRefPattern = regexp.MustCompile(`^refs/merge-requests/([0-9]+)/(head|merge)$`)
)

type pullRequestLister interface {
	ListPullRequests(context.Context, model.GitRepository, string) ([]PullRequestRef, error)
}

func parsePullRequestGitRef(ref, sha string) (PullRequestRef, bool) {
	match := pullRefPattern.FindStringSubmatch(ref)
	if len(match) == 0 {
		match = mergeRequestRefPattern.FindStringSubmatch(ref)
	}
	if len(match) == 0 {
		return PullRequestRef{}, false
	}
	number, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || number < 1 || sha == "" {
		return PullRequestRef{}, false
	}
	return PullRequestRef{Number: number, Ref: ref, SHA: sha}, true
}

func isPullRequestRef(ref string) bool {
	return pullRefPattern.MatchString(ref) || mergeRequestRefPattern.MatchString(ref)
}

func (c *GitClient) ListPullRequests(ctx context.Context, repository model.GitRepository, credential string) ([]PullRequestRef, error) {
	if repository.Provider == model.GitProviderGeneric {
		return nil, nil
	}
	location, err := parseRepositoryLocation(repository.CloneURL)
	if err != nil {
		return nil, ErrInvalidRepository
	}
	queryContext, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	result := make([]PullRequestRef, 0)
	for pageNumber := 1; len(result) < maxOpenPullRequests; pageNumber++ {
		endpoint, err := pullRequestEndpoint(repository.Provider, location, pageNumber)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(queryContext, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("创建 PR 查询请求失败: %w", err)
		}
		apiCredential := credential
		if repository.AuthType != model.GitAuthToken {
			// SSH 私钥只能用于 Git 传输，绝不能作为 HTTP Authorization 值发送给托管平台。
			apiCredential = ""
		}
		setProviderAPIHeaders(request, repository.Provider, apiCredential)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return nil, fmt.Errorf("查询 PR 失败: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		closeErr := response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("读取 PR 查询结果失败: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("关闭 PR 查询响应失败: %w", closeErr)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("PR 查询返回 HTTP %d", response.StatusCode)
		}
		page, err := decodePullRequestPage(repository.Provider, body)
		if err != nil {
			return nil, err
		}
		result = append(result, page...)
		if len(page) < 100 {
			break
		}
	}
	if len(result) > maxOpenPullRequests {
		result = result[:maxOpenPullRequests]
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Number < result[j].Number })
	return result, nil
}

type repositoryLocation struct {
	Scheme string
	Host   string
	Path   string
}

func parseRepositoryLocation(cloneURL string) (repositoryLocation, error) {
	if scpURLPattern.MatchString(cloneURL) {
		parts := strings.SplitN(cloneURL, ":", 2)
		host := parts[0]
		if separator := strings.LastIndex(host, "@"); separator >= 0 {
			host = host[separator+1:]
		}
		return normalizeRepositoryLocation("https", host, parts[1])
	}
	parsed, err := url.Parse(cloneURL)
	if err != nil || parsed.Host == "" {
		return repositoryLocation{}, ErrInvalidRepository
	}
	scheme := parsed.Scheme
	if scheme == "ssh" {
		scheme = "https"
	}
	return normalizeRepositoryLocation(scheme, parsed.Host, parsed.Path)
}

func normalizeRepositoryLocation(scheme, host, repositoryPath string) (repositoryLocation, error) {
	repositoryPath = strings.Trim(strings.TrimSuffix(repositoryPath, ".git"), "/")
	if scheme == "" || host == "" || repositoryPath == "" || strings.Contains(repositoryPath, "..") {
		return repositoryLocation{}, ErrInvalidRepository
	}
	return repositoryLocation{Scheme: scheme, Host: host, Path: repositoryPath}, nil
}

func pullRequestEndpoint(provider model.GitProvider, location repositoryLocation, pageNumber int) (string, error) {
	values := url.Values{"page": {strconv.Itoa(pageNumber)}}
	var base, endpointPath string
	switch provider {
	case model.GitProviderGitHub:
		base = location.Scheme + "://" + location.Host
		if strings.EqualFold(location.Hostname(), "github.com") {
			base = "https://api.github.com"
			endpointPath = "/repos/" + escapeRepositoryPath(location.Path) + "/pulls"
		} else {
			endpointPath = "/api/v3/repos/" + escapeRepositoryPath(location.Path) + "/pulls"
		}
		values.Set("state", "open")
		values.Set("per_page", "100")
	case model.GitProviderGitLab:
		base = location.Scheme + "://" + location.Host
		endpointPath = "/api/v4/projects/" + url.PathEscape(location.Path) + "/merge_requests"
		values.Set("state", "opened")
		values.Set("per_page", "100")
	case model.GitProviderGitea:
		base = location.Scheme + "://" + location.Host
		endpointPath = "/api/v1/repos/" + escapeRepositoryPath(location.Path) + "/pulls"
		values.Set("state", "open")
		values.Set("limit", "100")
	case model.GitProviderGitee:
		base = location.Scheme + "://" + location.Host
		endpointPath = "/api/v5/repos/" + escapeRepositoryPath(location.Path) + "/pulls"
		values.Set("state", "open")
		values.Set("per_page", "100")
	default:
		return "", errors.New("当前代码仓库平台不支持查询 PR")
	}
	return base + endpointPath + "?" + values.Encode(), nil
}

func (location repositoryLocation) Hostname() string {
	parsed, err := url.Parse(location.Scheme + "://" + location.Host)
	if err != nil {
		return location.Host
	}
	return parsed.Hostname()
}

func escapeRepositoryPath(repositoryPath string) string {
	parts := strings.Split(repositoryPath, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return path.Join(parts...)
}

func setProviderAPIHeaders(request *http.Request, provider model.GitProvider, credential string) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "zrt")
	if credential == "" {
		return
	}
	switch provider {
	case model.GitProviderGitHub:
		request.Header.Set("Authorization", "Bearer "+credential)
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	case model.GitProviderGitLab:
		request.Header.Set("PRIVATE-TOKEN", credential)
	case model.GitProviderGitea, model.GitProviderGitee:
		request.Header.Set("Authorization", "token "+credential)
	}
}

type pullRequestAPIItem struct {
	Number       int64  `json:"number"`
	Index        int64  `json:"index"`
	IID          int64  `json:"iid"`
	SHA          string `json:"sha"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Head         struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func decodePullRequestPage(provider model.GitProvider, body []byte) ([]PullRequestRef, error) {
	var items []pullRequestAPIItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("解析 PR 查询结果失败: %w", err)
	}
	result := make([]PullRequestRef, 0, len(items))
	for i := range items {
		item := items[i]
		number := item.Number
		if number == 0 {
			number = item.Index
		}
		if number == 0 {
			number = item.IID
		}
		sha, sourceBranch, targetBranch := item.Head.SHA, item.Head.Ref, item.Base.Ref
		if provider == model.GitProviderGitLab {
			sha, sourceBranch, targetBranch = item.SHA, item.SourceBranch, item.TargetBranch
		}
		if number < 1 || sha == "" || sourceBranch == "" || targetBranch == "" {
			continue
		}
		ref := "refs/pull/" + strconv.FormatInt(number, 10) + "/head"
		if provider == model.GitProviderGitLab {
			ref = "refs/merge-requests/" + strconv.FormatInt(number, 10) + "/head"
		}
		result = append(result, PullRequestRef{
			Number: number, Ref: ref, SHA: sha, SourceBranch: sourceBranch, TargetBranch: targetBranch,
		})
	}
	return result, nil
}
