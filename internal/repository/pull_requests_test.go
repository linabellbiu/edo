package repository

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"edo/internal/config"
	"edo/internal/model"
)

func TestGitClientListsGiteaPullRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/repos/team/app/pulls" || request.URL.Query().Get("limit") != "100" ||
			request.Header.Get("Authorization") != "token saved-token" {
			t.Fatalf("Gitea PR 请求错误: %s %s header=%q", request.Method, request.URL.String(), request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Query().Get("state") {
		case "open":
			_, _ = writer.Write([]byte(`[{"number":17,"state":"open","head":{"ref":"feature/payment","sha":"abc123"},"base":{"ref":"release/2026.08"}}]`))
		case "closed":
			if request.URL.Query().Get("sort") != "recentupdate" {
				t.Fatalf("Gitea 最近关闭 PR 没有按关闭时间排序: %s", request.URL.String())
			}
			_, _ = writer.Write([]byte(`[
				{"number":18,"state":"closed","merged":true,"merge_commit_sha":"def456","head":{"ref":"feature/refund","sha":"old-head"},"base":{"ref":"release/2026.08"}},
				{"number":19,"state":"closed","merged":false,"head":{"ref":"feature/closed","sha":"closed-head"},"base":{"ref":"release/2026.08"}}
			]`))
		default:
			t.Fatalf("Gitea PR 状态参数错误: %s", request.URL.String())
		}
	}))
	defer server.Close()

	client := NewGitClient(config.Git{Timeout: time.Second})
	result, err := client.ListPullRequests(context.Background(), model.GitRepository{
		Provider: model.GitProviderGitea, CloneURL: server.URL + "/team/app.git", AuthType: model.GitAuthToken,
	}, "saved-token", "")
	if err != nil {
		t.Fatalf("读取 Gitea PR 失败: %v", err)
	}
	if len(result) != 2 || result[0].Number != 17 || result[0].Ref != "refs/pull/17/head" ||
		result[0].SHA != "abc123" || result[0].SourceBranch != "feature/payment" || result[0].TargetBranch != "release/2026.08" ||
		result[0].State != "open" || result[0].Action != "opened" || result[1].Number != 18 ||
		result[1].SHA != "def456" || result[1].State != "merged" || result[1].Action != "merged" {
		t.Fatalf("Gitea PR 解析错误: %+v", result)
	}
}

func TestGitClientUsesOnlyExplicitAPITokenForSSHRepository(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		if got := request.Header.Get("Authorization"); got != "token private-api-token" {
			t.Fatalf("SSH 仓库 API 请求没有使用独立 Token，Authorization=%q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewGitClient(config.Git{Timeout: time.Second})
	privateKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-key-material\n-----END OPENSSH PRIVATE KEY-----"
	_, err := client.ListPullRequests(context.Background(), model.GitRepository{
		Provider: model.GitProviderGitea, CloneURL: server.URL + "/team/private.git", AuthType: model.GitAuthSSHKey,
	}, privateKey, "private-api-token")
	if err != nil {
		t.Fatalf("SSH 私有仓库 PR API 查询失败: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("开启和最近合并 PR 查询次数错误: %d", requestCount)
	}

	requestCount = 0
	_, err = client.ListPullRequests(context.Background(), model.GitRepository{
		Provider: model.GitProviderGitea, CloneURL: server.URL + "/team/private.git", AuthType: model.GitAuthSSHKey,
	}, privateKey, privateKey)
	if !errors.Is(err, ErrInvalidCredential) || requestCount != 0 {
		t.Fatalf("GitClient 尝试把 SSH 私钥发送给平台 API: requests=%d err=%v", requestCount, err)
	}
}

func TestPlatformAPITokenHeaders(t *testing.T) {
	tests := []struct {
		provider model.GitProvider
		header   string
		want     string
		query    string
	}{
		{provider: model.GitProviderGitHub, header: "Authorization", want: "Bearer private-token"},
		{provider: model.GitProviderGitLab, header: "PRIVATE-TOKEN", want: "private-token"},
		{provider: model.GitProviderGitea, header: "Authorization", want: "token private-token"},
		{provider: model.GitProviderGitee, query: "private-token"},
	}
	for _, test := range tests {
		t.Run(string(test.provider), func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "https://git.example.com/api", nil)
			setProviderAPIHeaders(request, test.provider, "private-token")
			if test.header != "" && request.Header.Get(test.header) != test.want {
				t.Fatalf("平台 API Token Header 错误: got=%q want=%q", request.Header.Get(test.header), test.want)
			}
			if got := request.URL.Query().Get("access_token"); got != test.query {
				t.Fatalf("平台 API Token 查询参数错误: got=%q want=%q", got, test.query)
			}
			if test.provider == model.GitProviderGitee && request.Header.Get("Authorization") != "" {
				t.Fatal("Gitee API Token 不应同时写入非官方 Authorization Header")
			}
		})
	}
}

func TestGiteeNetworkErrorDoesNotExposeQueryToken(t *testing.T) {
	const token = "gitee-private-token-must-not-leak"
	client := NewGitClient(config.Git{Timeout: time.Second})
	_, err := client.ListPullRequests(context.Background(), model.GitRepository{
		Provider: model.GitProviderGitee, CloneURL: "http://127.0.0.1:1/team/private.git", AuthType: model.GitAuthSSHKey,
	}, "-----BEGIN OPENSSH PRIVATE KEY-----", token)
	if err == nil {
		t.Fatal("不可达 Gitee API 未返回错误")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("Gitee API 网络错误泄露了查询参数令牌: %v", err)
	}
}

func TestDecodePullRequestPageUsesMergeCommitAcrossProviders(t *testing.T) {
	tests := []struct {
		name     string
		provider model.GitProvider
		body     string
		wantRef  string
	}{
		{
			name: "GitHub", provider: model.GitProviderGitHub, wantRef: "refs/pull/12/head",
			body: `[{"number":12,"state":"closed","merged_at":"2026-07-30T01:02:03Z","merge_commit_sha":"merge-github","head":{"ref":"feature/github","sha":"head-github"},"base":{"ref":"main"}}]`,
		},
		{
			name: "GitLab", provider: model.GitProviderGitLab, wantRef: "refs/merge-requests/13/head",
			body: `[{"iid":13,"state":"merged","sha":"head-gitlab","merge_commit_sha":"merge-gitlab","source_branch":"feature/gitlab","target_branch":"main"}]`,
		},
		{
			name: "Gitee", provider: model.GitProviderGitee, wantRef: "refs/pull/14/head",
			body: `[{"number":14,"state":"closed","merged":true,"merge_commit_sha":"merge-gitee","head":{"ref":"feature/gitee","sha":"head-gitee"},"base":{"ref":"main"}}]`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, responseCount, err := decodePullRequestPage(test.provider, []byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if responseCount != 1 || len(result) != 1 || result[0].Ref != test.wantRef ||
				result[0].SHA == "" || result[0].SHA[:6] != "merge-" || result[0].Action != "merged" || result[0].State != "merged" {
				t.Fatalf("合并 PR 解析错误: count=%d result=%+v", responseCount, result)
			}
		})
	}
}

func TestDecodePullRequestPageRejectsMissingBranchMetadata(t *testing.T) {
	_, _, err := decodePullRequestPage(model.GitProviderGitHub, []byte(`[
		{"number":12,"state":"open","head":{"sha":"head-without-ref"},"base":{"ref":"main"}}
	]`))
	if !errors.Is(err, ErrPullRequestMetadata) {
		t.Fatalf("缺少源分支的 PR API 响应未被标记为不可用: %v", err)
	}
}

func TestPullRequestEndpointQueriesRecentMerged(t *testing.T) {
	location := repositoryLocation{Scheme: "https", Host: "git.example.com", Path: "team/app"}
	tests := []struct {
		provider model.GitProvider
		want     map[string]string
	}{
		{model.GitProviderGitHub, map[string]string{"state": "closed", "sort": "updated", "direction": "desc"}},
		{model.GitProviderGitLab, map[string]string{"state": "merged", "order_by": "updated_at", "sort": "desc", "scope": "all"}},
		{model.GitProviderGitea, map[string]string{"state": "closed", "sort": "recentupdate"}},
		{model.GitProviderGitee, map[string]string{"state": "merged", "sort": "updated", "direction": "desc"}},
	}
	for _, test := range tests {
		endpoint, err := pullRequestEndpoint(test.provider, location, 1, pullRequestQueryMerged)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range test.want {
			if request.URL.Query().Get(key) != value {
				t.Fatalf("%s 最近合并 PR 参数 %s 错误: %s", test.provider, key, endpoint)
			}
		}
	}
}

func TestGitClientQueriesGiteeMergedStateWithoutGuessingMergeCommit(t *testing.T) {
	states := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		state := request.URL.Query().Get("state")
		states <- state
		writer.Header().Set("Content-Type", "application/json")
		switch state {
		case "open", "closed":
			_, _ = writer.Write([]byte(`[]`))
		case "merged":
			// Gitee 的已合并列表可能不提供合并 Commit；当前目标分支和 PR 头均不能替代它。
			_, _ = writer.Write([]byte(`[{"number":1,"state":"merged","merged_at":"2026-11-03T23:27:29+08:00","head":{"ref":"feature/test","sha":"pr-head"},"base":{"ref":"main","sha":"later-main"}}]`))
		default:
			writer.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := NewGitClient(config.Git{Timeout: time.Second})
	result, err := client.ListPullRequests(context.Background(), model.GitRepository{
		Provider: model.GitProviderGitee, CloneURL: server.URL + "/team/app.git", AuthType: model.GitAuthNone,
	}, "", "")
	if len(states) != 2 {
		t.Fatalf("Gitee 开启及已合并查询次数错误: %d", len(states))
	}
	if open, merged := <-states, <-states; open != "open" || merged != "merged" {
		t.Fatalf("Gitee 查询状态错误: open=%q merged=%q", open, merged)
	}
	if !errors.Is(err, ErrPullRequestMetadata) || len(result) != 0 {
		t.Fatalf("缺少合并 Commit 时不应猜测已合并 PR 的执行版本: result=%+v err=%v", result, err)
	}
}

func TestGitClientPreservesOnlyValidatedPullRequestsOnPartialFailure(t *testing.T) {
	for _, mergedStatus := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(mergedStatus), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if request.URL.Query().Get("state") == "open" {
					_, _ = writer.Write([]byte(`[
						{"number":1,"state":"open","head":{"ref":"feature/stale","sha":"stale-open"},"base":{"ref":"main"}},
						{"number":2,"state":"open","head":{"ref":"feature/open","sha":"open-head"},"base":{"ref":"main"}}
					]`))
					return
				}
				writer.WriteHeader(mergedStatus)
				if mergedStatus == http.StatusOK {
					_, _ = writer.Write([]byte(`[
						{"number":1,"state":"merged","head":{"ref":"feature/missing","sha":"not-the-merge"},"base":{"ref":"main"}},
						{"number":3,"state":"merged","merge_commit_sha":"exact-merge","head":{"ref":"feature/valid","sha":"old-head"},"base":{"ref":"main"}}
					]`))
				}
			}))
			defer server.Close()

			client := NewGitClient(config.Git{Timeout: time.Second})
			result, err := client.ListPullRequests(context.Background(), model.GitRepository{
				Provider: model.GitProviderGitee, CloneURL: server.URL + "/team/app.git", AuthType: model.GitAuthNone,
			}, "", "")
			var partial *partialPullRequestError
			if !errors.As(err, &partial) || len(result) == 0 || len(partial.validated) != len(result) {
				t.Fatalf("部分失败没有保留已验证 API 记录及诊断: result=%+v err=%v", result, err)
			}
			if mergedStatus == http.StatusOK {
				if !errors.Is(err, ErrPullRequestMetadata) || len(result) != 2 || result[0].Number != 2 || result[0].SHA != "open-head" || result[0].Action != "opened" || result[1].Number != 3 || result[1].SHA != "exact-merge" {
					t.Fatalf("无效合并记录影响了有效记录或被猜测补全: %+v err=%v", result, err)
				}
			} else if len(result) != 2 || result[0].Number != 1 || result[1].Number != 2 || result[0].Action != "opened" || result[1].Action != "opened" {
				t.Fatalf("API 失败不应生成合并事件或破坏已验证开启事件: %+v", result)
			}
		})
	}
}

func TestGitClientBoundsPullRequestPagesByRawRecords(t *testing.T) {
	requests := make(chan struct{}, 3)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case requests <- struct{}{}:
		default:
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("[" + strings.Repeat(`{"number":1,"state":"closed"},`, 99) + `{"number":1,"state":"closed"}]`))
	}))
	defer server.Close()
	client := NewGitClient(config.Git{Timeout: time.Second})
	location, err := parseRepositoryLocation(server.URL + "/team/app.git")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := client.listPullRequests(ctx, model.GitProviderGitee, location, "", pullRequestQueryOpen, 200)
	if err != nil || len(result) != 0 || len(requests) != 2 {
		t.Fatalf("过滤记录后仍应遵守原始分页预算: result=%+v requests=%d err=%v", result, len(requests), err)
	}
}

func TestGitClientBoundsPartialPullRequestResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[
			{"number":1,"state":"open","head":{"ref":"feature/one","sha":"one"},"base":{"ref":"main"}},
			{"number":2,"state":"open","head":{"ref":"feature/two","sha":"two"},"base":{"ref":"main"}},
			{"number":3,"state":"merged","head":{"ref":"feature/missing","sha":"not-merge"},"base":{"ref":"main"}}
		]`))
	}))
	defer server.Close()
	location, err := parseRepositoryLocation(server.URL + "/team/app.git")
	if err != nil {
		t.Fatal(err)
	}
	client := NewGitClient(config.Git{Timeout: time.Second})
	result, err := client.listPullRequests(context.Background(), model.GitProviderGitee, location, "", pullRequestQueryOpen, 1)
	var partial *partialPullRequestError
	if !errors.As(err, &partial) || len(result) != 1 || result[0].Number != 1 {
		t.Fatalf("部分结果也必须遵守条数上限: result=%+v err=%v", result, err)
	}
}

func TestRefsToResultIncludesAdvertisedPullRequestRefs(t *testing.T) {
	result, err := refsToResult([]*plumbing.Reference{
		plumbing.NewHashReference("refs/heads/feature/demo", plumbing.NewHash("1111111111111111111111111111111111111111")),
		plumbing.NewHashReference("refs/pull/12/merge", plumbing.NewHash("2222222222222222222222222222222222222222")),
		plumbing.NewHashReference("refs/pull/12/head", plumbing.NewHash("3333333333333333333333333333333333333333")),
		plumbing.NewHashReference("refs/merge-requests/7/head", plumbing.NewHash("4444444444444444444444444444444444444444")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PullRequests) != 2 || result.PullRequests[0].Number != 7 || result.PullRequests[1].Number != 12 ||
		result.PullRequests[1].Ref != "refs/pull/12/head" {
		t.Fatalf("没有正确保留远端 PR Ref: %+v", result.PullRequests)
	}
}
