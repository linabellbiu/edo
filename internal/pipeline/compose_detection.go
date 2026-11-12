package pipeline

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"edo/internal/model"
)

// 与 Compose 官方 compose-go/v2/cli.DefaultFileNames 保持一致；升级 Compose 时复核此优先级。
// 不引入完整 CLI 加载器，避免仅检测文件名却新增 YAML/schema 解析依赖。
var composeRootFileNames = [...]string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

type ComposeFileDetection struct {
	Path      string `json:"path"`
	CommitSHA string `json:"commit_sha"`
}

// DetectRepositoryComposeFile 只检测固定 Commit 的仓库根目录；不返回正文，也不执行 Compose。
func (s *Service) DetectRepositoryComposeFile(ctx context.Context, applicationID, requestedRef string) (*ComposeFileDetection, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	app, err := s.FindApplication(ctx, applicationID)
	if err != nil {
		s.logger.WarnContext(ctx, "检测 Compose 文件时读取应用失败", "operation", "compose_file_detect_application", "application_id", applicationID, "err", err)
		return nil, err
	}
	scope, err := s.deliveryScope(ctx)
	if err != nil {
		return nil, s.composeSourceError(ctx, "compose_file_detect_scope", applicationID, err)
	}
	if err := s.requireDeliveryPermission(ctx, scope.UserID, "repository.read"); err != nil {
		return nil, err
	}
	ref, commit, err := s.composeRepositoryVersion(ctx, *app, requestedRef)
	if err != nil {
		return nil, s.composeSourceError(ctx, "compose_file_detect_ref", applicationID, err)
	}
	lease, err := s.repositories.AcquireTaskCheckout(ctx, app.RepositoryID, ref, commit, "compose-detect:"+app.ID)
	if err != nil {
		return nil, s.composeSourceError(ctx, "compose_file_detect_checkout", applicationID, err)
	}
	defer lease.Release()
	entries, err := os.ReadDir(lease.Directory)
	if err != nil {
		return nil, s.composeSourceError(ctx, "compose_file_detect_root", applicationID, err)
	}
	regular := map[string]bool{}
	for _, entry := range entries {
		// 自动检测不跟随符号链接，也不递归子目录；手工路径继续走原有安全读取校验。
		if entry.Type().IsRegular() {
			regular[entry.Name()] = true
		}
	}
	result := &ComposeFileDetection{CommitSHA: commit}
	// 仅采用官方文件名优先级，避免默认 CLI 搜索父目录的行为。
	for _, name := range composeRootFileNames {
		if regular[name] {
			result.Path = name
			break
		}
	}
	return result, nil
}

func (s *Service) composeRepositoryVersion(ctx context.Context, app model.Application, requestedRef string) (string, string, error) {
	if s.repositories == nil {
		return "", "", errors.New("代码仓库服务不可用")
	}
	ref := composePreviewRef(app, requestedRef)
	refs, err := s.repositories.TestConnection(ctx, app.RepositoryID)
	if err != nil {
		return "", "", err
	}
	for _, candidate := range refs.Branches {
		if "refs/heads/"+strings.TrimPrefix(candidate.Name, "refs/heads/") == ref {
			return ref, candidate.SHA, nil
		}
	}
	for _, candidate := range refs.Tags {
		if "refs/tags/"+strings.TrimPrefix(candidate.Name, "refs/tags/") == ref {
			return ref, candidate.SHA, nil
		}
	}
	return "", "", ErrComposeSource
}

func composePreviewRef(app model.Application, requestedRef string) string {
	ref := strings.TrimSpace(requestedRef)
	if ref == "" || strings.ContainsAny(ref, "*?[") {
		ref = app.Repository.DefaultBranch
	}
	if !strings.HasPrefix(ref, "refs/") {
		ref = "refs/heads/" + ref
	}
	return ref
}
