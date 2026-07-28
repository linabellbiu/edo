package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"zrt/internal/config"
	"zrt/internal/model"
)

func TestGitClientListsGiteaPullRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/repos/team/app/pulls" || request.URL.Query().Get("state") != "open" ||
			request.URL.Query().Get("limit") != "100" || request.Header.Get("Authorization") != "token saved-token" {
			t.Fatalf("Gitea PR 请求错误: %s %s header=%q", request.Method, request.URL.String(), request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[{"number":17,"head":{"ref":"feature/payment","sha":"abc123"},"base":{"ref":"release/2026.08"}}]`))
	}))
	defer server.Close()

	client := NewGitClient(config.Git{Timeout: time.Second})
	result, err := client.ListPullRequests(context.Background(), model.GitRepository{
		Provider: model.GitProviderGitea, CloneURL: server.URL + "/team/app.git", AuthType: model.GitAuthToken,
	}, "saved-token")
	if err != nil {
		t.Fatalf("读取 Gitea PR 失败: %v", err)
	}
	if len(result) != 1 || result[0].Number != 17 || result[0].Ref != "refs/pull/17/head" ||
		result[0].SHA != "abc123" || result[0].SourceBranch != "feature/payment" || result[0].TargetBranch != "release/2026.08" {
		t.Fatalf("Gitea PR 解析错误: %+v", result)
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
