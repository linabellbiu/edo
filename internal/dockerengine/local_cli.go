package dockerengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ManualCLIConnection 只提供手动命令所需的连接位置，不返回证书内容或临时会话环境。
func (s *Service) ManualCLIConnection(ctx context.Context) (host, certificateDirectory, tlsVerify string, err error) {
	defer func() {
		if err != nil {
			slog.WarnContext(ctx, "读取手动命令 Docker 连接位置失败", "operation", "docker_manual_cli_connection", "err", err)
		}
	}()
	endpoint, err := s.Find(ctx, LocalEndpointID)
	if err != nil {
		return "", "", "", err
	}
	if !endpoint.IsActive {
		return "", "", "", ErrEndpointNotFound
	}
	api, err := s.BuilderClient()
	if err != nil {
		return "", "", "", err
	}
	defer api.Close()
	host = api.DaemonHost()
	parsed, parseErr := url.Parse(host)
	if parseErr != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", "", ErrInvalidEndpoint
	}
	certificateDirectory = strings.TrimSpace(s.config.DockerBuilderTLSCertPath)
	if certificateDirectory != "" {
		tlsVerify = "1"
	}
	if strings.TrimSpace(s.config.DockerBuilderHost) == "" {
		certificateDirectory = os.Getenv("DOCKER_CERT_PATH")
		if certificateDirectory != "" && os.Getenv("DOCKER_TLS_VERIFY") != "" {
			tlsVerify = "1"
		}
	}
	return host, certificateDirectory, tlsVerify, nil
}

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
