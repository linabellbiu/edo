package dockerengine

import (
	"errors"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"zrt/internal/model"
)

var ErrInvalidContainerConfig = errors.New("Docker 容器配置无效")

var (
	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	networkNamePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	volumeNamePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

const (
	defaultDockerNetwork       = "bridge"
	defaultDockerRestartPolicy = "unless-stopped"
	maximumDockerConfigBytes   = 256 * 1024
)

// NormalizeContainerConfig 同时承担部署方案写入和运行快照执行前的校验。
// 端口默认只绑定回环地址；需要公网监听时必须在高级配置中显式填写 0.0.0.0 或 ::。
func NormalizeContainerConfig(input model.DockerContainerConfig) (model.DockerContainerConfig, error) {
	if len(input.PortMappings) > 32 || len(input.EnvironmentVariables) > 100 || len(input.VolumeMounts) > 32 ||
		len(input.Command) > 32 || len(input.HealthCheck.Command) > 32 {
		return input, ErrInvalidContainerConfig
	}

	result := model.DockerContainerConfig{
		PortMappings:         slices.Clone(input.PortMappings),
		EnvironmentVariables: make(map[string]string, len(input.EnvironmentVariables)),
		VolumeMounts:         slices.Clone(input.VolumeMounts),
		Network:              strings.TrimSpace(input.Network),
		DeploymentScript:     strings.TrimSpace(input.DeploymentScript),
		Command:              slices.Clone(input.Command),
		HealthCheck:          input.HealthCheck,
		RestartPolicy:        strings.TrimSpace(input.RestartPolicy),
	}
	result.HealthCheck.Command = slices.Clone(input.HealthCheck.Command)
	if result.Network == "" {
		result.Network = defaultDockerNetwork
	}
	if result.RestartPolicy == "" {
		result.RestartPolicy = defaultDockerRestartPolicy
	}
	if !networkNamePattern.MatchString(result.Network) || result.Network != defaultDockerNetwork ||
		result.RestartPolicy != defaultDockerRestartPolicy {
		return input, ErrInvalidContainerConfig
	}

	portTargets := make(map[string]struct{}, len(result.PortMappings))
	hostBindings := make(map[string]struct{}, len(result.PortMappings))
	for index := range result.PortMappings {
		mapping := &result.PortMappings[index]
		mapping.HostIP, mapping.Protocol = strings.TrimSpace(mapping.HostIP), strings.ToLower(strings.TrimSpace(mapping.Protocol))
		if mapping.HostIP == "" {
			mapping.HostIP = "127.0.0.1"
		}
		if mapping.Protocol == "" {
			mapping.Protocol = "tcp"
		}
		address, err := netip.ParseAddr(mapping.HostIP)
		if err != nil || address.Zone() != "" || address.IsMulticast() || (mapping.Protocol != "tcp" && mapping.Protocol != "udp") ||
			mapping.HostPort < 1 || mapping.HostPort > 65535 || mapping.ContainerPort < 1 || mapping.ContainerPort > 65535 {
			return input, ErrInvalidContainerConfig
		}
		targetKey := strconv.Itoa(mapping.ContainerPort) + "/" + mapping.Protocol
		hostKey := mapping.HostIP + ":" + strconv.Itoa(mapping.HostPort) + "/" + mapping.Protocol
		if _, exists := portTargets[targetKey]; exists {
			return input, ErrInvalidContainerConfig
		}
		if _, exists := hostBindings[hostKey]; exists {
			return input, ErrInvalidContainerConfig
		}
		portTargets[targetKey], hostBindings[hostKey] = struct{}{}, struct{}{}
	}

	valueBytes := 0
	for rawName, value := range input.EnvironmentVariables {
		name := strings.TrimSpace(rawName)
		if name != rawName || !environmentNamePattern.MatchString(name) || len(name) > 128 || len(value) > 32*1024 ||
			!utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return input, ErrInvalidContainerConfig
		}
		valueBytes += len(name) + len(value)
		result.EnvironmentVariables[name] = value
	}

	mountTargets := make(map[string]struct{}, len(result.VolumeMounts))
	for index := range result.VolumeMounts {
		mount := &result.VolumeMounts[index]
		mount.Type, mount.Source, mount.Target = strings.TrimSpace(mount.Type), strings.TrimSpace(mount.Source), strings.TrimSpace(mount.Target)
		if mount.Type == "" {
			mount.Type = "volume"
		}
		if !validContainerPath(mount.Target) || mount.Target == "/" || sensitiveContainerPath(mount.Target) {
			return input, ErrInvalidContainerConfig
		}
		if _, exists := mountTargets[mount.Target]; exists {
			return input, ErrInvalidContainerConfig
		}
		mountTargets[mount.Target] = struct{}{}
		switch mount.Type {
		case "volume":
			if !volumeNamePattern.MatchString(mount.Source) {
				return input, ErrInvalidContainerConfig
			}
		default:
			return input, ErrInvalidContainerConfig
		}
	}

	if result.DeploymentScript != "" {
		if len(result.DeploymentScript) > maximumDockerConfigBytes || !utf8.ValidString(result.DeploymentScript) ||
			strings.ContainsRune(result.DeploymentScript, '\x00') {
			return input, ErrInvalidContainerConfig
		}
		if _, err := parseDockerRunTemplate(result.DeploymentScript); err != nil || len(result.PortMappings) > 0 ||
			len(result.EnvironmentVariables) > 0 || len(result.VolumeMounts) > 0 || result.HealthCheck.Enabled {
			return input, ErrInvalidContainerConfig
		}
		// 主机侧 Docker 命令完整替代结构化容器参数，不能与历史 Command 同时生效。
		result.Command = nil
		valueBytes += len(result.DeploymentScript)
	} else {
		for index := range result.Command {
			result.Command[index] = strings.TrimSpace(result.Command[index])
			if !validExecArgument(result.Command[index]) {
				return input, ErrInvalidContainerConfig
			}
			valueBytes += len(result.Command[index])
		}
	}
	if result.HealthCheck.Enabled {
		if len(result.HealthCheck.Command) == 0 {
			return input, ErrInvalidContainerConfig
		}
		if result.HealthCheck.IntervalSeconds == 0 {
			result.HealthCheck.IntervalSeconds = 30
		}
		if result.HealthCheck.TimeoutSeconds == 0 {
			result.HealthCheck.TimeoutSeconds = 5
		}
		if result.HealthCheck.Retries == 0 {
			result.HealthCheck.Retries = 3
		}
		if result.HealthCheck.StartPeriodSeconds == 0 {
			result.HealthCheck.StartPeriodSeconds = 10
		}
		if result.HealthCheck.IntervalSeconds < 2 || result.HealthCheck.IntervalSeconds > 3600 ||
			result.HealthCheck.TimeoutSeconds < 1 || result.HealthCheck.TimeoutSeconds > 300 ||
			result.HealthCheck.TimeoutSeconds >= result.HealthCheck.IntervalSeconds ||
			result.HealthCheck.Retries < 1 || result.HealthCheck.Retries > 20 ||
			result.HealthCheck.StartPeriodSeconds < 0 || result.HealthCheck.StartPeriodSeconds > 3600 {
			return input, ErrInvalidContainerConfig
		}
		for index := range result.HealthCheck.Command {
			result.HealthCheck.Command[index] = strings.TrimSpace(result.HealthCheck.Command[index])
			if !validExecArgument(result.HealthCheck.Command[index]) {
				return input, ErrInvalidContainerConfig
			}
			valueBytes += len(result.HealthCheck.Command[index])
		}
	} else {
		result.HealthCheck = model.DockerHealthCheck{}
	}
	if valueBytes > maximumDockerConfigBytes {
		return input, ErrInvalidContainerConfig
	}
	return result, nil
}

func validExecArgument(value string) bool {
	return value != "" && len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func validContainerPath(value string) bool {
	return value != "" && len(value) <= 1024 && strings.HasPrefix(value, "/") && path.Clean(value) == value &&
		utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func sensitiveContainerPath(value string) bool {
	return value == "/proc" || strings.HasPrefix(value, "/proc/") || value == "/sys" || strings.HasPrefix(value, "/sys/") ||
		value == "/dev" || strings.HasPrefix(value, "/dev/") || value == "/var/run/docker.sock" || value == "/run/docker.sock"
}
