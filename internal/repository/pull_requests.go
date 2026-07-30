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

const (
	maxOpenPullRequests         = 1000
	maxRecentMergedPullRequests = 100
	pullRequestPageSize         = 100
)

type pullRequestQuery uint8

const (
	pullRequestQueryOpen pullRequestQuery = iota + 1
	pullRequestQueryMerged
)

var (
	pullRefPattern         = regexp.MustCompile(`^refs/pull/([0-9]+)/(head|merge)$`)
	mergeRequestRefPattern = regexp.MustCompile(`^refs/merge-requests/([0-9]+)/(head|merge)$`)
)

type pullRequestLister interface {
	ListPullRequests(context.Context, model.GitRepository, string, string) ([]PullRequestRef, error)
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

func pullRequestHeadRef(provider model.GitProvider, number int64) (string, bool) {
	if number < 1 {
		return "", false
	}
	suffix := strconv.FormatInt(number, 10) + "/head"
	switch provider {
	case model.GitProviderGitHub, model.GitProviderGitea, model.GitProviderGitee:
		return "refs/pull/" + suffix, true
	case model.GitProviderGitLab:
		return "refs/merge-requests/" + suffix, true
	default:
		return "", false
	}
}

func (c *GitClient) ListPullRequests(
	ctx context.Context,
	repository model.GitRepository,
	cloneCredential string,
	apiCredential string,
) ([]PullRequestRef, error) {
	if repository.Provider == model.GitProviderGeneric {
		return nil, ErrPullRequestMetadata
	}
	location, err := parseRepositoryLocation(repository.CloneURL)
	if err != nil {
		return nil, ErrInvalidRepository
	}
	queryContext, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	if apiCredential == "" && repository.AuthType == model.GitAuthToken {
		// HTTPS Token 克隆可以默认复用同一 Token；SSH 私钥永远不会落入该分支。
		apiCredential = cloneCredential
	}
	if strings.Contains(apiCredential, "PRIVATE KEY") {
		return nil, ErrInvalidCredential
	}
	openPullRequests, err := c.listPullRequests(queryContext, repository.Provider, location, apiCredential, pullRequestQueryOpen, maxOpenPullRequests)
	if err != nil {
		return nil, err
	}
	mergedPullRequests, err := c.listPullRequests(queryContext, repository.Provider, location, apiCredential, pullRequestQueryMerged, maxRecentMergedPullRequests)
	if err != nil {
		return nil, err
	}
	byNumber := make(map[int64]PullRequestRef, len(openPullRequests)+len(mergedPullRequests))
	for i := range openPullRequests {
		byNumber[openPullRequests[i].Number] = openPullRequests[i]
	}
	for i := range mergedPullRequests {
		// 同一编号不应同时处于开启和已合并状态；若平台短暂返回两份记录，
		// 以不可逆的已合并状态为准，避免漏掉合并事件。
		byNumber[mergedPullRequests[i].Number] = mergedPullRequests[i]
	}
	result := make([]PullRequestRef, 0, len(byNumber))
	for _, pullRequest := range byNumber {
		result = append(result, pullRequest)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Number < result[j].Number })
	return result, nil
}

func (c *GitClient) listPullRequests(
	ctx context.Context,
	provider model.GitProvider,
	location repositoryLocation,
	credential string,
	query pullRequestQuery,
	limit int,
) ([]PullRequestRef, error) {
	result := make([]PullRequestRef, 0)
	for pageNumber := 1; len(result) < limit; pageNumber++ {
		endpoint, err := pullRequestEndpoint(provider, location, pageNumber, query)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("创建 PR 查询请求失败: %w", err)
		}
		setProviderAPIHeaders(request, provider, credential)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			var requestError *url.Error
			if errors.As(err, &requestError) && requestError.Err != nil {
				// Gitee 按官方 API 约定把 access_token 放在查询参数中。剥离会包含
				// 完整 URL 的 url.Error 外层，防止令牌进入上层日志。
				err = requestError.Err
			}
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
		page, responseCount, err := decodePullRequestPage(provider, body)
		if err != nil {
			return nil, err
		}
		result = append(result, page...)
		if query == pullRequestQueryMerged || responseCount < pullRequestPageSize {
			break
		}
	}
	if len(result) > limit {
		result = result[:limit]
	}
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

func pullRequestEndpoint(provider model.GitProvider, location repositoryLocation, pageNumber int, query pullRequestQuery) (string, error) {
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
		if query == pullRequestQueryMerged {
			values.Set("state", "closed")
			values.Set("sort", "updated")
			values.Set("direction", "desc")
		} else {
			values.Set("state", "open")
		}
		values.Set("per_page", "100")
	case model.GitProviderGitLab:
		base = location.Scheme + "://" + location.Host
		endpointPath = "/api/v4/projects/" + url.PathEscape(location.Path) + "/merge_requests"
		values.Set("scope", "all")
		if query == pullRequestQueryMerged {
			values.Set("state", "merged")
			values.Set("order_by", "updated_at")
			values.Set("sort", "desc")
		} else {
			values.Set("state", "opened")
		}
		values.Set("per_page", "100")
	case model.GitProviderGitea:
		base = location.Scheme + "://" + location.Host
		endpointPath = "/api/v1/repos/" + escapeRepositoryPath(location.Path) + "/pulls"
		if query == pullRequestQueryMerged {
			values.Set("state", "closed")
			values.Set("sort", "recentupdate")
		} else {
			values.Set("state", "open")
		}
		values.Set("limit", "100")
	case model.GitProviderGitee:
		base = location.Scheme + "://" + location.Host
		endpointPath = "/api/v5/repos/" + escapeRepositoryPath(location.Path) + "/pulls"
		if query == pullRequestQueryMerged {
			values.Set("state", "closed")
			values.Set("sort", "updated")
			values.Set("direction", "desc")
		} else {
			values.Set("state", "open")
		}
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
	case model.GitProviderGitea:
		request.Header.Set("Authorization", "token "+credential)
	case model.GitProviderGitee:
		values := request.URL.Query()
		values.Set("access_token", credential)
		request.URL.RawQuery = values.Encode()
	}
}

type pullRequestAPIItem struct {
	Number         int64   `json:"number"`
	Index          int64   `json:"index"`
	IID            int64   `json:"iid"`
	SHA            string  `json:"sha"`
	SourceBranch   string  `json:"source_branch"`
	TargetBranch   string  `json:"target_branch"`
	State          string  `json:"state"`
	Merged         bool    `json:"merged"`
	MergedAt       *string `json:"merged_at"`
	MergeCommitSHA string  `json:"merge_commit_sha"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func decodePullRequestPage(provider model.GitProvider, body []byte) ([]PullRequestRef, int, error) {
	var items []pullRequestAPIItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, 0, fmt.Errorf("解析 PR 查询结果失败: %w", err)
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
		if provider == model.GitProviderGitLab || sha == "" || sourceBranch == "" || targetBranch == "" {
			sha, sourceBranch, targetBranch = item.SHA, item.SourceBranch, item.TargetBranch
		}
		state := strings.ToLower(strings.TrimSpace(item.State))
		mergedAt := item.MergedAt != nil && strings.TrimSpace(*item.MergedAt) != ""
		merged := item.Merged || state == "merged" || mergedAt
		action := "opened"
		if merged {
			state, action = "merged", "merged"
			sha = strings.TrimSpace(item.MergeCommitSHA)
		} else if state == "closed" {
			// 最近关闭列表还包含未合并的 PR；关闭事件当前不是可配置动作。
			continue
		} else {
			state = "open"
		}
		if number < 1 || sha == "" || sourceBranch == "" || targetBranch == "" {
			return nil, len(items), fmt.Errorf("%w: 平台返回的 PR/MR 数据缺少编号、Commit 或分支", ErrPullRequestMetadata)
		}
		ref, supported := pullRequestHeadRef(provider, number)
		if !supported {
			continue
		}
		result = append(result, PullRequestRef{
			Number: number, Ref: ref, SHA: sha, SourceBranch: sourceBranch, TargetBranch: targetBranch,
			State: state, Action: action,
		})
	}
	return result, len(items), nil
}
