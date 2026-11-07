package dockerengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"sort"
	"strings"

	"edo/internal/configfile"
)

const ConfigurationRevisionLabel = "edo.configuration.revision"

type configurationRevisionKey struct{}

func WithConfigurationRevision(ctx context.Context, revision string) context.Context {
	return context.WithValue(ctx, configurationRevisionKey{}, revision)
}
func configurationRevision(ctx context.Context) string {
	value, _ := ctx.Value(configurationRevisionKey{}).(string)
	return value
}

// WriteConfigurationFiles 只写入经验证的部署目录；SSH 使用登录用户而非 Docker 的 sudo 身份。
func (s *Service) WriteConfigurationFiles(ctx context.Context, endpointID, directory string, files map[string]string) error {
	paths := []string{}
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	writes, err := configfile.WriteScript(directory, paths)
	if err != nil {
		return err
	}
	var script strings.Builder
	script.WriteString("set -eu\numask 077\n")
	for _, name := range paths {
		fmt.Fprintf(&script, "export %s=%s\n", configfile.TransportKey(name), configfile.Quote(files[name]))
	}
	script.WriteString(writes)
	endpoint, err := s.Find(ctx, endpointID)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(endpoint.Host)
	if err != nil {
		return ErrInvalidEndpoint
	}
	if parsed.Scheme == "ssh" {
		host, bundle, fingerprint, err := s.sshConfiguration(ctx, endpoint)
		if err != nil {
			return err
		}
		connector, err := newSSHConnector(host, bundle, fingerprint, s.config.ConnectTimeout)
		if err != nil {
			return err
		}
		client, err := connector.connectPinned(ctx)
		if err != nil {
			return err
		}
		defer client.Close()
		return runSSHCommandWithStreams(ctx, client, "sh -se", strings.NewReader(script.String()), io.Discard, io.Discard)
	}
	if !IsLocalEndpointID(endpointID) {
		return errors.New("当前 Docker 连接不支持生成主机配置文件，请使用 SSH 主机连接")
	}
	command := exec.CommandContext(ctx, "sh", "-se")
	command.Stdin = strings.NewReader(script.String())
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}
