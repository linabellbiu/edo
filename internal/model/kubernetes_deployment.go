package model

import (
	"errors"
	"log/slog"
	"net/netip"
	"path"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

type KubernetesDeploymentPort struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Port       int32  `json:"port"`
	TargetPort int32  `json:"target_port"`
	NodePort   int32  `json:"node_port,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

// EffectiveProtocol 按 Kubernetes 默认解释旧配置，不修改已保存配置或运行快照。
func (p KubernetesDeploymentPort) EffectiveProtocol() string {
	if p.Protocol == "" {
		return "TCP"
	}
	return p.Protocol
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
	Options                       *KubernetesDeploymentOptions `json:"options,omitempty"`
	Command                       []string                     `json:"command,omitempty"`
	Args                          []string                     `json:"args,omitempty"`
	WorkingDir                    string                       `json:"working_dir,omitempty"`
	ImagePullPolicy               string                       `json:"image_pull_policy,omitempty"`
	ReadinessProbe                *KubernetesDeploymentProbe   `json:"readiness_probe,omitempty"`
	LivenessProbe                 *KubernetesDeploymentProbe   `json:"liveness_probe,omitempty"`
	StartupProbe                  *KubernetesDeploymentProbe   `json:"startup_probe,omitempty"`
	TerminationGracePeriodSeconds *int64                       `json:"termination_grace_period_seconds,omitempty"`
	Strategy                      string                       `json:"strategy,omitempty"`
	MaxSurge                      string                       `json:"max_surge,omitempty"`
	MaxUnavailable                string                       `json:"max_unavailable,omitempty"`
	Template                      string                       `json:"template"`
	Replicas                      int32                        `json:"replicas"`
	CPURequest                    string                       `json:"cpu_request"`
	MemoryRequest                 string                       `json:"memory_request"`
	CPULimit                      string                       `json:"cpu_limit"`
	MemoryLimit                   string                       `json:"memory_limit"`
	ServiceType                   string                       `json:"service_type,omitempty"`
	ExternalName                  string                       `json:"external_name,omitempty"`
	NodePort                      bool                         `json:"node_port"`
	Ports                         []KubernetesDeploymentPort   `json:"ports"`
}

func DefaultKubernetesDeploymentSettings() *KubernetesDeploymentSettings {
	return &KubernetesDeploymentSettings{Template: "web", Replicas: 1, CPURequest: "200m", MemoryRequest: "300Mi", CPULimit: "1", MemoryLimit: "1Gi", Ports: []KubernetesDeploymentPort{{Name: "http", Port: 8080, TargetPort: 8080}}}
}

// EffectiveServiceType 显式类型优先，缺省时继续解释历史 NodePort 开关，不回写快照。
func (c *KubernetesDeploymentSettings) EffectiveServiceType() string {
	if c != nil {
		if c.ServiceType != "" {
			return c.ServiceType
		}
		if c.NodePort {
			return "NodePort"
		}
	}
	return "ClusterIP"
}

var ErrKubernetesDeploymentSettings = errors.New("Kubernetes 模板配置无效，请检查副本、资源、端口、启动参数和健康检查")

func ValidateKubernetesDeploymentSettings(c *KubernetesDeploymentSettings) error {
	if c == nil {
		return nil
	}
	serviceType := c.EffectiveServiceType()
	valid := c.Template == "web" && c.Replicas >= 1 && c.Replicas <= 100 && len(c.Ports) <= 32 && len(c.ExternalName) <= 254
	switch serviceType {
	case "ClusterIP", "NodePort", "LoadBalancer":
	case "ExternalName":
		host := strings.TrimSuffix(c.ExternalName, ".")
		_, ipErr := netip.ParseAddr(host)
		valid = valid && host != "" && len(validation.IsDNS1123Subdomain(host)) == 0 && ipErr != nil
		for _, label := range strings.Split(host, ".") {
			valid = valid && len(validation.IsDNS1123Label(label)) == 0
		}
	default:
		valid = false
	}
	for _, pair := range [][2]string{{c.CPURequest, c.CPULimit}, {c.MemoryRequest, c.MemoryLimit}} {
		a, errA := resource.ParseQuantity(pair[0])
		b, errB := resource.ParseQuantity(pair[1])
		valid = valid && len(pair[0]) <= 32 && len(pair[1]) <= 32 && errA == nil && errB == nil && a.Sign() > 0 && b.Sign() > 0 && a.Cmp(b) <= 0
	}
	type servicePort struct {
		port     int32
		protocol string
	}
	names, ports, nodes := map[string]bool{}, map[servicePort]bool{}, map[int32]bool{}
	for _, p := range c.Ports {
		if serviceType == "ExternalName" {
			continue // 外部 DNS 别名不使用端口，保留其他类型尚未填写完成的草稿。
		}
		protocol := p.EffectiveProtocol()
		valid = valid && len(validation.IsValidPortName(p.Name)) == 0 && !names[p.Name] && len(validation.IsValidPortNum(int(p.Port))) == 0 && len(validation.IsValidPortNum(int(p.TargetPort))) == 0 && (protocol == "TCP" || protocol == "UDP" || protocol == "SCTP")
		names[p.Name] = true
		if (serviceType == "NodePort" || serviceType == "LoadBalancer") && p.NodePort != 0 {
			valid = valid && p.NodePort >= 30000 && p.NodePort <= 32767 && !nodes[p.NodePort]
			nodes[p.NodePort] = true
		}
		if p.Enabled {
			key := servicePort{port: p.Port, protocol: protocol}
			valid = valid && !ports[key]
			ports[key] = true
		}
	}
	if valid && validKubernetesRuntimeSettings(c) && ValidateKubernetesDeploymentOptions(c.Options) == nil {
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
