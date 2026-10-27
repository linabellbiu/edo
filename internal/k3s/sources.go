package k3s

import (
	"maps"
	"net/url"
	"strconv"
	"strings"

	"github.com/distribution/reference"
)

const (
	officialInstallerURL = "https://get.k3s.io"
	officialArtifactURL  = "https://github.com/k3s-io/k3s/releases/download"
	chinaInstallerURL    = "https://rancher-mirror.rancher.cn/k3s/k3s-install.sh"
	chinaMirrorURL       = "https://rancher-mirror.rancher.cn"
	chinaArtifactURL     = chinaMirrorURL + "/k3s"
)

func validSystemImageRegistry(value string) bool {
	if value == "" {
		return true
	}
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "@?#\\\r\n\x00") || strings.Contains(value, "://") {
		return false
	}
	// 用标准镜像引用解析器校验仓库前缀，禁止把凭据、Tag 或 Digest 当作前缀。
	named, err := reference.ParseNormalizedNamed(value + "/edo-source-check")
	if err != nil || named.String() != value+"/edo-source-check" || !reference.IsNameOnly(named) {
		return false
	}
	domain := reference.Domain(named)
	u, err := url.Parse("https://" + domain)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return false
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return false
		}
	}
	return true
}

func validateSourceSelection(input Input) error {
	if input.DownloadSource != "" && input.DownloadSource != "official" && input.DownloadSource != "official_cn" {
		return &ValidationError{Message: "请选择官方或官方国内下载源"}
	}
	if input.Action == "join" && input.SystemImageRegistry != nil {
		return &ValidationError{Message: "加入节点沿用现有集群的系统镜像仓库，不能在此修改全局仓库"}
	}
	if input.SystemImageRegistry != nil && !validSystemImageRegistry(*input.SystemImageRegistry) {
		return &ValidationError{Message: "系统镜像仓库需要无凭据、协议、Tag 和 Digest 的有效仓库前缀"}
	}
	return nil
}

func resolveInstallSources(input *Input) error {
	if err := validateSourceSelection(*input); err != nil {
		return err
	}
	if input.DownloadSource == "" {
		input.DownloadSource = "official"
	}
	registry, exists := input.ServerOptions["system-default-registry"]
	if input.SystemImageRegistry != nil {
		if exists && registry != *input.SystemImageRegistry {
			return &ValidationError{Message: "系统镜像仓库与高级配置不一致，请统一后重新检查"}
		}
		if input.ServerOptions == nil {
			input.ServerOptions = map[string]any{}
		}
		input.ServerOptions["system-default-registry"] = *input.SystemImageRegistry
		registry = *input.SystemImageRegistry
	}
	resolved := &Sources{DownloadSource: input.DownloadSource, InstallerURL: officialInstallerURL}
	artifact, err := serverArtifactBase(input.Installer)
	if err != nil {
		return err
	}
	resolved.ArtifactURL = artifact
	if input.DownloadSource == "official_cn" {
		if custom, _ := input.Installer["INSTALL_K3S_ARTIFACT_URL"].(string); custom != "" {
			return &ValidationError{Message: "官方国内下载源不能同时配置自定义制品地址"}
		}
		resolved.InstallerURL, resolved.ArtifactURL, resolved.Mirror = chinaInstallerURL, chinaArtifactURL, "cn"
	}
	if input.Action == "create" {
		effective, _ := registry.(string)
		if !validSystemImageRegistry(effective) {
			return &ValidationError{Message: "系统镜像仓库需要有效的无凭据仓库前缀"}
		}
		resolved.SystemImageRegistry = &effective
	}
	input.sources = resolved
	return nil
}

func sourcesForInput(input Input) (*Sources, error) {
	if input.sources != nil {
		return input.sources, nil
	}
	// 旧快照仍按原官方入口执行，但展示层不为旧任务补造来源记录。
	input.Installer, input.ServerOptions = maps.Clone(input.Installer), maps.Clone(input.ServerOptions)
	if err := resolveInstallSources(&input); err != nil {
		return nil, err
	}
	return input.sources, nil
}

func installerForSource(input Input) (map[string]any, error) {
	sources, err := sourcesForInput(input)
	if err != nil {
		return nil, err
	}
	installer := maps.Clone(input.Installer)
	if installer == nil {
		installer = map[string]any{}
	}
	if sources.Mirror == "cn" {
		delete(installer, "INSTALL_K3S_ARTIFACT_URL")
		installer["INSTALL_K3S_MIRROR"] = "cn"
		// 官方国内脚本的默认值不含协议，显式固定 HTTPS，不依赖 curl 的隐式 HTTP。
		installer["INSTALL_K3S_MIRROR_URL"] = strings.TrimSuffix(sources.ArtifactURL, "/k3s")
	} else {
		installer["INSTALL_K3S_ARTIFACT_URL"] = sources.ArtifactURL
	}
	return installer, nil
}

func sourceReleaseURL(sources *Sources, version string) string {
	if sources.DownloadSource == "official_cn" {
		version = strings.ReplaceAll(version, "+", "-")
	} else {
		version = strings.ReplaceAll(version, "+", "%2B")
	}
	return strings.TrimRight(sources.ArtifactURL, "/") + "/" + version
}

func sourceChannelURL(sources *Sources, installer map[string]any) string {
	if sources.DownloadSource == "official_cn" {
		return strings.TrimRight(sources.ArtifactURL, "/") + "/channels"
	}
	if custom, _ := installer["INSTALL_K3S_CHANNEL_URL"].(string); custom != "" {
		return custom
	}
	return "https://update.k3s.io/v1-release/channels"
}

func systemRegistryProbeScript(input Input) string {
	sources, err := sourcesForInput(input)
	if err != nil {
		return "edo_check system_registry failed\n"
	}
	if sources.SystemImageRegistry == nil || *sources.SystemImageRegistry == "" {
		return "edo_check system_registry warning\n"
	}
	named, err := reference.ParseNormalizedNamed(*sources.SystemImageRegistry + "/edo-source-check")
	if err != nil {
		return "edo_check system_registry failed\n"
	}
	endpoint := "https://" + reference.Domain(named) + "/v2/"
	// Registry 401 是标准认证挑战，只证明基础端点可达；不跟随重定向或读取响应正文。
	return "if edo_registry_status=$(timeout 5 curl --silent --output /dev/null --head --write-out '%{http_code}' --proto '=https' --tlsv1.2 --connect-timeout 2 --max-time 4 " + shellQuote(endpoint) + " 2>/dev/null); then\ncase \"$edo_registry_status\" in 200|401) edo_check system_registry passed;; *) edo_check system_registry failed;; esac\nelse edo_check system_registry failed; fi\nunset edo_registry_status\n"
}
