package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"zrt/internal/config"
	"zrt/internal/model"
)

var scpURLPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)

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
	commandContext, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	args := []string{"ls-remote", "--heads", "--tags", "--", repository.CloneURL}
	command := exec.CommandContext(commandContext, c.config.Command, args...)
	command.Env = cleanGitEnvironment(os.Environ())
	cleanup := func() {}

	switch repository.AuthType {
	case model.GitAuthNone:
	case model.GitAuthToken:
		username := repository.Username
		if username == "" {
			username = defaultTokenUsername(repository.Provider)
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + credential))
		command.Env = append(command.Env,
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+encoded,
			"GIT_CONFIG_KEY_1=http.followRedirects",
			"GIT_CONFIG_VALUE_1=false",
		)
	case model.GitAuthSSHKey:
		if strings.TrimSpace(c.config.KnownHostsFile) == "" {
			return RefResult{}, ErrKnownHostsRequired
		}
		if _, err := os.Stat(c.config.KnownHostsFile); err != nil {
			return RefResult{}, fmt.Errorf("读取 SSH known_hosts 文件失败: %w", err)
		}
		keyFile, err := os.CreateTemp("", "zrt-git-key-*")
		if err != nil {
			return RefResult{}, fmt.Errorf("创建临时 SSH 私钥文件失败: %w", err)
		}
		keyPath := keyFile.Name()
		cleanup = func() { _ = os.Remove(keyPath) }
		defer cleanup()
		if err := keyFile.Chmod(0o600); err != nil {
			_ = keyFile.Close()
			return RefResult{}, fmt.Errorf("设置临时 SSH 私钥权限失败: %w", err)
		}
		if _, err := keyFile.WriteString(credential); err != nil {
			_ = keyFile.Close()
			return RefResult{}, fmt.Errorf("写入临时 SSH 私钥失败: %w", err)
		}
		if err := keyFile.Close(); err != nil {
			return RefResult{}, fmt.Errorf("关闭临时 SSH 私钥失败: %w", err)
		}
		command.Env = append(command.Env, "GIT_SSH_COMMAND=ssh -i "+shellQuote(keyPath)+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile="+shellQuote(c.config.KnownHostsFile))
	default:
		return RefResult{}, ErrInvalidCredential
	}

	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			stderr := truncateString(strings.TrimSpace(string(exitError.Stderr)), 2048)
			return RefResult{}, fmt.Errorf("Git 远程查询失败: %w; stderr=%s", err, stderr)
		}
		return RefResult{}, fmt.Errorf("执行 Git 远程查询失败: %w", err)
	}
	if len(output) > 4*1024*1024 {
		return RefResult{}, errors.New("Git 远程引用数量过多")
	}
	return parseRefs(string(output)), nil
}

func parseRefs(output string) RefResult {
	branches := make([]GitRef, 0)
	tags := make([]GitRef, 0)
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 40 {
			continue
		}
		switch {
		case strings.HasPrefix(fields[1], "refs/heads/"):
			branches = append(branches, GitRef{Name: strings.TrimPrefix(fields[1], "refs/heads/"), SHA: fields[0]})
		case strings.HasPrefix(fields[1], "refs/tags/") && !strings.HasSuffix(fields[1], "^{}"):
			tags = append(tags, GitRef{Name: strings.TrimPrefix(fields[1], "refs/tags/"), SHA: fields[0]})
		}
	}
	sort.Slice(branches, func(i, j int) bool { return branches[i].Name < branches[j].Name })
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
	return RefResult{Branches: branches, Tags: tags}
}

func cleanGitEnvironment(environment []string) []string {
	blocked := []string{"GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_SSH=", "GIT_SSH_COMMAND=", "GIT_CONFIG_COUNT=", "GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"}
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		skip := false
		for _, prefix := range blocked {
			if strings.HasPrefix(item, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			result = append(result, item)
		}
	}
	return append(result, "GIT_TERMINAL_PROMPT=0")
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

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func truncateString(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
