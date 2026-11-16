package dockerengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// LocalCLIEnvironment 与本地 Docker SDK 共用连接及项目边界，CLI 不继承用户的 Docker Context。
// 返回环境仅供子进程使用，禁止写入任务、日志或 API 响应。
func (s *Service) LocalCLIEnvironment(ctx context.Context) (environment []string, revision string, cleanup func(), err error) {
	cleanup = func() {}
	endpoint, err := s.Find(ctx, LocalEndpointID)
	if err != nil || !endpoint.IsActive {
		if err == nil {
			err = ErrEndpointNotFound
		}
		return nil, "", cleanup, err
	}
	revision, err = s.TerminalRevision(ctx, LocalEndpointID)
	if err != nil {
		return nil, "", cleanup, err
	}
	api, err := s.BuilderClient()
	if err != nil {
		return nil, "", cleanup, err
	}
	host := api.DaemonHost()
	_ = api.Close()
	environment, cleanup, err = s.composeCLIEnvironment(endpoint, "", RegistryAuth{})
	if err != nil {
		cleanup()
		return nil, "", func() {}, err
	}
	filtered := environment[:0]
	digest := sha256.New()
	_, _ = digest.Write([]byte(revision))
	certPath := ""
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "DOCKER_HOST", "DOCKER_CONTEXT":
			continue
		case "DOCKER_CONFIG":
		case "DOCKER_CERT_PATH":
			certPath = value
			_, _ = digest.Write([]byte(entry))
		default:
			if strings.HasPrefix(key, "DOCKER_") {
				_, _ = digest.Write([]byte(entry))
			}
		}
		filtered = append(filtered, entry)
	}
	environment = append(filtered, "DOCKER_HOST="+host)
	_, _ = digest.Write([]byte(host))
	if certPath != "" {
		for _, name := range []string{"ca.pem", "cert.pem", "key.pem"} {
			data, readErr := os.ReadFile(filepath.Join(certPath, name))
			if readErr != nil {
				cleanup()
				return nil, "", func() {}, readErr
			}
			_, _ = digest.Write(data)
		}
	}
	return environment, hex.EncodeToString(digest.Sum(nil)), cleanup, nil
}
