package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"edo/internal/model"
)

type checkTransport func(*http.Request) (*http.Response, error)

func (f checkTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestProviderCheckStatusUsesCorrectCredentialAndEndpoint(t *testing.T) {
	for _, test := range []struct {
		provider            model.GitProvider
		clone, path, header string
	}{{model.GitProviderGitHub, "https://github.com/team/app.git", "https://api.github.com/repos/team/app/statuses/", "Authorization"}, {model.GitProviderGitLab, "https://git.example/team/app.git", "https://git.example/api/v4/projects/team%2Fapp/statuses/", "PRIVATE-TOKEN"}, {model.GitProviderGitea, "https://git.example/team/app.git", "https://git.example/api/v1/repos/team/app/statuses/", "Authorization"}, {model.GitProviderGitee, "https://gitee.com/team/app.git", "https://gitee.com/api/v5/repos/team/app/statuses/", ""}} {
		t.Run(string(test.provider), func(t *testing.T) {
			called := false
			transport := checkTransport(func(request *http.Request) (*http.Response, error) {
				called = true
				if request.Method != "POST" || !strings.HasPrefix(request.URL.String(), test.path) {
					t.Fatalf("请求路径不正确: %s", request.URL.String())
				}
				if test.header != "" && !strings.Contains(request.Header.Get(test.header), "api-token") {
					t.Fatal("未使用平台 API 令牌")
				}
				if test.provider == model.GitProviderGitee && request.URL.Query().Get("access_token") != "api-token" {
					t.Fatal("Gitee 认证不正确")
				}
				var payload map[string]string
				if json.NewDecoder(request.Body).Decode(&payload) != nil {
					t.Fatal("请求内容不正确")
				}
				expected := "failure"
				if test.provider == model.GitProviderGitLab {
					expected = "failed"
				}
				if payload["state"] != expected {
					t.Fatal("状态映射不正确")
				}
				return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
			})
			err := writeProviderCheckStatus(context.Background(), model.GitRepository{Provider: test.provider, CloneURL: test.clone}, "api-token", strings.Repeat("a", 40), "edo/workflow", "failure", transport)
			if err != nil || !called {
				t.Fatalf("回写没有执行: %v", err)
			}
		})
	}
}
func TestCheckStatusClassifiesPermanentFailure(t *testing.T) {
	for _, item := range []struct {
		code      int
		permanent bool
	}{{401, true}, {403, true}, {422, true}, {429, false}, {500, false}} {
		if (&CheckStatusError{Code: item.code}).Permanent() != item.permanent {
			t.Fatal("重试分类不正确")
		}
	}
}
