package model

import (
	"log/slog"
	"path"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// KubernetesDeploymentOptions 仅声明容器和容器组配置，不开放宿主机或权限扩展字段。
type KubernetesDeploymentOptions struct {
	Labels                  map[string]string                 `json:"labels,omitempty"`
	Annotations             map[string]string                 `json:"annotations,omitempty"`
	Environment             []KubernetesDeploymentEnvironment `json:"environment,omitempty"`
	Volumes                 []KubernetesDeploymentVolume      `json:"volumes,omitempty"`
	NodeSelector            map[string]string                 `json:"node_selector,omitempty"`
	Tolerations             []KubernetesDeploymentToleration  `json:"tolerations,omitempty"`
	ImagePullSecrets        []string                          `json:"image_pull_secrets,omitempty"`
	MinReadySeconds         *int32                            `json:"min_ready_seconds,omitempty"`
	RevisionHistoryLimit    *int32                            `json:"revision_history_limit,omitempty"`
	ProgressDeadlineSeconds *int32                            `json:"progress_deadline_seconds,omitempty"`
}

type KubernetesDeploymentEnvironment struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Value      string `json:"value,omitempty"`
	SourceName string `json:"source_name,omitempty"`
	SourceKey  string `json:"source_key,omitempty"`
}

type KubernetesDeploymentVolume struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	SourceName string `json:"source_name,omitempty"`
	MountPath  string `json:"mount_path"`
	SubPath    string `json:"sub_path,omitempty"`
	ReadOnly   bool   `json:"read_only,omitempty"`
}

type KubernetesDeploymentToleration struct {
	Key               string `json:"key,omitempty"`
	Operator          string `json:"operator"`
	Value             string `json:"value,omitempty"`
	Effect            string `json:"effect,omitempty"`
	TolerationSeconds *int64 `json:"toleration_seconds,omitempty"`
}

// ValidateKubernetesDeploymentOptions 复用官方标识校验，保留平台资源身份与有界配置。
func ValidateKubernetesDeploymentOptions(c *KubernetesDeploymentOptions) error {
	if c == nil {
		return nil
	}
	valid := validKubernetesMetadata(c.Labels, false, true) && validKubernetesMetadata(c.Annotations, true, true) && validKubernetesMetadata(c.NodeSelector, false, false)
	valid = valid && len(c.Environment) <= 64 && len(c.Volumes) <= 32 && len(c.Tolerations) <= 32 && len(c.ImagePullSecrets) <= 16
	names, paths := map[string]bool{}, map[string]bool{}
	envSize := 0
	for _, env := range c.Environment {
		valid = valid && len(validation.IsEnvVarName(env.Name)) == 0 && len(env.Name) <= 253 && !names[env.Name]
		names[env.Name] = true
		envSize += len(env.Name) + len(env.Value)
		switch env.Kind {
		case "value":
			valid = valid && len(env.Value) <= 4096 && !strings.ContainsRune(env.Value, 0) && env.SourceName == "" && env.SourceKey == ""
		case "configmap", "secret":
			valid = valid && env.Value == "" && len(validation.IsDNS1123Subdomain(env.SourceName)) == 0 && len(validation.IsConfigMapKey(env.SourceKey)) == 0
		default:
			valid = false
		}
	}
	valid = valid && envSize <= 64*1024
	names = map[string]bool{}
	for _, volume := range c.Volumes {
		valid = valid && len(validation.IsDNS1123Label(volume.Name)) == 0 && !names[volume.Name] && !paths[volume.MountPath] && validKubernetesMountPath(volume.MountPath, true) && (volume.SubPath == "" || validKubernetesMountPath(volume.SubPath, false))
		names[volume.Name], paths[volume.MountPath] = true, true
		switch volume.Kind {
		case "pvc", "configmap", "secret":
			valid = valid && len(validation.IsDNS1123Subdomain(volume.SourceName)) == 0
		case "emptyDir":
			valid = valid && volume.SourceName == ""
		default:
			valid = false
		}
	}
	names = map[string]bool{}
	for _, name := range c.ImagePullSecrets {
		valid = valid && len(validation.IsDNS1123Subdomain(name)) == 0 && !names[name]
		names[name] = true
	}
	for _, tolerance := range c.Tolerations {
		valid = valid && (tolerance.Key == "" || len(validation.IsQualifiedName(tolerance.Key)) == 0) && len(validation.IsValidLabelValue(tolerance.Value)) == 0
		switch tolerance.Operator {
		case "", "Equal":
			valid = valid && tolerance.Key != ""
		case "Exists":
			valid = valid && tolerance.Value == ""
		default:
			valid = false
		}
		valid = valid && (tolerance.Effect == "" || tolerance.Effect == "NoSchedule" || tolerance.Effect == "PreferNoSchedule" || tolerance.Effect == "NoExecute")
		if tolerance.TolerationSeconds != nil {
			valid = valid && *tolerance.TolerationSeconds >= 0 && *tolerance.TolerationSeconds <= 365*24*3600 && tolerance.Effect == "NoExecute"
		}
	}
	ready, deadline := int32(0), int32(600)
	if c.MinReadySeconds != nil {
		ready = *c.MinReadySeconds
		valid = valid && ready >= 0 && ready <= 86400
	}
	if c.ProgressDeadlineSeconds != nil {
		deadline = *c.ProgressDeadlineSeconds
		valid = valid && deadline >= 1 && deadline <= 86400
	}
	valid = valid && deadline > ready
	if c.RevisionHistoryLimit != nil {
		valid = valid && *c.RevisionHistoryLimit >= 0 && *c.RevisionHistoryLimit <= 100
	}
	if valid {
		return nil
	}
	slog.Warn("Kubernetes 通用部署参数无效", "operation", "kubernetes_options_normalize", "err", ErrKubernetesDeploymentSettings)
	return ErrKubernetesDeploymentSettings
}

func validKubernetesMetadata(values map[string]string, annotations, reserved bool) bool {
	if len(values) > 64 {
		return false
	}
	total := 0
	for key, value := range values {
		total += len(key) + len(value)
		if len(validation.IsQualifiedName(key)) != 0 || reserved && (strings.HasPrefix(key, "edo.io/") || strings.HasPrefix(key, "edo.dev/") || key == "app.kubernetes.io/managed-by") {
			return false
		}
		if annotations {
			if len(value) > 4096 || strings.ContainsRune(value, 0) {
				return false
			}
		} else if len(validation.IsValidLabelValue(value)) != 0 {
			return false
		}
	}
	return total <= 64*1024
}

func validKubernetesMountPath(value string, absolute bool) bool {
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") || path.IsAbs(value) != absolute || path.Clean(value) != value {
		return false
	}
	if absolute {
		return value != "/"
	}
	return value != "." && value != ".." && !strings.HasPrefix(value, "../")
}
