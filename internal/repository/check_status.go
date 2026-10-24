package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"edo/internal/model"
)

var ErrCheckCredentials = errors.New("仓库未配置可用的平台 API 令牌")

type CheckStatusError struct{ Code int }

func (e *CheckStatusError) Error() string {
	return fmt.Sprintf("仓库检查结果回写失败，状态码 %d", e.Code)
}
func (e *CheckStatusError) Permanent() bool {
	return e.Code >= 400 && e.Code < 500 && e.Code != 429 && e.Code != 408 && e.Code != 409
}

var statusSHA = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

func (s *Service) WriteCheckStatus(ctx context.Context, repositoryID, sha, contextName, state string) error {
	repository, err := s.Find(ctx, repositoryID)
	if err != nil {
		return err
	}
	if !repository.IsActive || repository.Provider == model.GitProviderGeneric {
		return ErrInvalidRepository
	}
	clone, err := s.resolveCredential(ctx, repository)
	if err != nil {
		return err
	}
	token, err := s.resolveAPICredential(ctx, repository, clone)
	if err != nil || token == "" || strings.Contains(token, "PRIVATE KEY") {
		return ErrCheckCredentials
	}
	return writeProviderCheckStatus(ctx, *repository, token, sha, contextName, state, http.DefaultTransport)
}
func writeProviderCheckStatus(ctx context.Context, repo model.GitRepository, token, sha, contextName, state string, transport http.RoundTripper) error {
	if !statusSHA.MatchString(sha) || len(contextName) > 128 || contextName == "" {
		return ErrInvalidRepository
	}
	switch state {
	case "pending", "success", "failure", "error":
	default:
		return ErrInvalidRepository
	}
	location, err := parseRepositoryLocation(repo.CloneURL)
	if err != nil {
		return err
	}
	base := location.Scheme + "://" + location.Host
	repositoryPath := escapeRepositoryPath(location.Path)
	endpoint := ""
	payload := map[string]string{"state": state, "context": contextName, "description": "EDO 流水线"}
	switch repo.Provider {
	case model.GitProviderGitHub:
		if strings.EqualFold(location.Hostname(), "github.com") {
			base = "https://api.github.com"
			endpoint = "/repos/"
		} else {
			endpoint = "/api/v3/repos/"
		}
		endpoint += repositoryPath + "/statuses/" + sha
	case model.GitProviderGitea:
		endpoint = "/api/v1/repos/" + repositoryPath + "/statuses/" + sha
	case model.GitProviderGitLab:
		endpoint = "/api/v4/projects/" + url.PathEscape(location.Path) + "/statuses/" + sha
		payload["name"] = contextName
		delete(payload, "context")
		if state == "failure" || state == "error" {
			payload["state"] = "failed"
		}
	case model.GitProviderGitee:
		endpoint = "/api/v5/repos/" + repositoryPath + "/statuses/" + sha
	default:
		return ErrInvalidRepository
	}
	data, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+endpoint, bytes.NewReader(data))
	if err != nil {
		return ErrInvalidRepository
	}
	request.Header.Set("Content-Type", "application/json")
	setProviderAPIHeaders(request, repo.Provider, token)
	client := http.Client{Timeout: 15 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		var detail *url.Error
		if errors.As(err, &detail) {
			return detail.Err
		}
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 65536))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &CheckStatusError{Code: response.StatusCode}
	}
	return nil
}
