package model

import (
	"errors"
	"log/slog"
	"path"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

type KubernetesDeploymentPort struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Port       int32  `json:"port"`
	TargetPort int32  `json:"target_port"`
	NodePort   int32  `json:"node_port,omitempty"`
}

// KubernetesDeploymentProbe 只声明容器自身的 HTTP/TCP 检测，不执行任意命令。
type KubernetesDeploymentProbe struct {
	Kind                string `json:"kind"`
	Port                int32  `json:"port"`
	Path                string `json:"path,omitempty"`
	Scheme              string `json:"scheme,omitempty"`
	InitialDelaySeconds int32  `json:"initial_delay_seconds,omitempty"`
	PeriodSeconds       int32  `json:"period_seconds,omitempty"`
	TimeoutSeconds      int32  `json:"timeout_seconds,omitempty"`
	FailureThreshold    int32  `json:"failure_threshold,omitempty"`
}

type KubernetesDeploymentSettings struct {
	Command                       []string                   `json:"command,omitempty"`
	Args                          []string                   `json:"args,omitempty"`
	WorkingDir                    string                     `json:"working_dir,omitempty"`
	ImagePullPolicy               string                     `json:"image_pull_policy,omitempty"`
	ReadinessProbe                *KubernetesDeploymentProbe `json:"readiness_probe,omitempty"`
	LivenessProbe                 *KubernetesDeploymentProbe `json:"liveness_probe,omitempty"`
	StartupProbe                  *KubernetesDeploymentProbe `json:"startup_probe,omitempty"`
	TerminationGracePeriodSeconds *int64                     `json:"termination_grace_period_seconds,omitempty"`
	Strategy                      string                     `json:"strategy,omitempty"`
	MaxSurge                      string                     `json:"max_surge,omitempty"`
	MaxUnavailable                string                     `json:"max_unavailable,omitempty"`
	Template                      string                     `json:"template"`
	Replicas                      int32                      `json:"replicas"`
	CPURequest                    string                     `json:"cpu_request"`
	MemoryRequest                 string                     `json:"memory_request"`
	CPULimit                      string                     `json:"cpu_limit"`
	MemoryLimit                   string                     `json:"memory_limit"`
	NodePort                      bool                       `json:"node_port"`
	Ports                         []KubernetesDeploymentPort `json:"ports"`
}

func DefaultKubernetesDeploymentSettings() *KubernetesDeploymentSettings {
	return &KubernetesDeploymentSettings{Template: "web", Replicas: 1, CPURequest: "200m", MemoryRequest: "300Mi", CPULimit: "1", MemoryLimit: "1Gi", Ports: []KubernetesDeploymentPort{{Name: "http", Port: 8080, TargetPort: 8080}, {Name: "tcp", Port: 8888, TargetPort: 8888}}}
}

var ErrKubernetesDeploymentSettings = errors.New("Kubernetes 模板配置无效，请检查副本、资源、端口、启动参数和健康检查")

func ValidateKubernetesDeploymentSettings(c *KubernetesDeploymentSettings) error {
	if c == nil {
		return nil
	}
	valid := c.Template == "web" && c.Replicas >= 1 && c.Replicas <= 100 && len(c.Ports) == 2
	for _, pair := range [][2]string{{c.CPURequest, c.CPULimit}, {c.MemoryRequest, c.MemoryLimit}} {
		a, errA := resource.ParseQuantity(pair[0])
		b, errB := resource.ParseQuantity(pair[1])
		valid = valid && len(pair[0]) <= 32 && len(pair[1]) <= 32 && errA == nil && errB == nil && a.Sign() > 0 && b.Sign() > 0 && a.Cmp(b) <= 0
	}
	names, ports, nodes := map[string]bool{}, map[int32]bool{}, map[int32]bool{}
	for _, p := range c.Ports {
		valid = valid && (p.Name == "http" || p.Name == "tcp") && !names[p.Name] && p.Port >= 1 && p.Port <= 65535 && p.TargetPort >= 1 && p.TargetPort <= 65535 && (p.NodePort == 0 || p.NodePort >= 30000 && p.NodePort <= 32767)
		names[p.Name] = true
		if p.Enabled {
			valid = valid && !ports[p.Port] && (p.NodePort == 0 || !nodes[p.NodePort])
			ports[p.Port] = true
			if p.NodePort != 0 {
				nodes[p.NodePort] = true
			}
		}
	}
	if valid && validKubernetesRuntimeSettings(c) {
		return nil
	}
	slog.Warn("Kubernetes 部署模板参数无效", "operation", "kubernetes_template_normalize", "err", ErrKubernetesDeploymentSettings)
	return ErrKubernetesDeploymentSettings
}

func validKubernetesRuntimeSettings(c *KubernetesDeploymentSettings) bool {
	if !validKubernetesArguments(c.Command) || !validKubernetesArguments(c.Args) {
		return false
	}
	if c.WorkingDir != "" && (len(c.WorkingDir) > 4096 || !path.IsAbs(c.WorkingDir) || path.Clean(c.WorkingDir) != c.WorkingDir || strings.ContainsAny(c.WorkingDir, "\x00\r\n")) {
		return false
	}
	switch c.ImagePullPolicy {
	case "", "Always", "IfNotPresent", "Never":
	default:
		return false
	}
	if c.TerminationGracePeriodSeconds != nil && (*c.TerminationGracePeriodSeconds < 0 || *c.TerminationGracePeriodSeconds > 3600) {
		return false
	}
	switch c.Strategy {
	case "", "RollingUpdate":
	case "Recreate":
		if c.MaxSurge != "" || c.MaxUnavailable != "" {
			return false
		}
	default:
		return false
	}
	if !validKubernetesRolloutValue(c.MaxSurge) || !validKubernetesRolloutValue(c.MaxUnavailable) {
		return false
	}
	if c.MaxSurge != "" && c.MaxUnavailable != "" && strings.Trim(strings.TrimSuffix(c.MaxSurge, "%"), "0") == "" && strings.Trim(strings.TrimSuffix(c.MaxUnavailable, "%"), "0") == "" {
		return false
	}
	for _, p := range []*KubernetesDeploymentProbe{c.ReadinessProbe, c.LivenessProbe, c.StartupProbe} {
		if p == nil {
			continue
		}
		if (p.Kind != "http" && p.Kind != "tcp") || p.Port < 1 || p.Port > 65535 || p.InitialDelaySeconds < 0 || p.InitialDelaySeconds > 3600 || p.PeriodSeconds < 0 || p.PeriodSeconds > 3600 || p.TimeoutSeconds < 0 || p.TimeoutSeconds > 3600 || p.FailureThreshold < 0 || p.FailureThreshold > 100 {
			return false
		}
		if p.Kind == "http" {
			if len(p.Path) > 2048 || strings.ContainsAny(p.Path, "\x00\r\n") || p.Path != "" && !strings.HasPrefix(p.Path, "/") || (p.Scheme != "" && p.Scheme != "HTTP" && p.Scheme != "HTTPS") {
				return false
			}
		} else if p.Path != "" || p.Scheme != "" {
			return false
		}
	}
	return true
}
func validKubernetesArguments(values []string) bool {
	if len(values) > 64 {
		return false
	}
	total := 0
	for _, v := range values {
		total += len(v)
		if len(v) > 4096 || strings.ContainsAny(v, "\x00\r\n") {
			return false
		}
	}
	return total <= 32768
}
func validKubernetesRolloutValue(value string) bool {
	if value == "" {
		return true
	}
	number := strings.TrimSuffix(value, "%")
	if number == "" || len(number) > 4 || strings.Trim(number, "0123456789") != "" {
		return false
	}
	parsed, err := strconv.Atoi(number)
	if err != nil || parsed < 0 {
		return false
	}
	if strings.HasSuffix(value, "%") {
		return parsed <= 100
	}
	return parsed <= 1000
}
