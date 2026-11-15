package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func optionPointer[T any](value T) *T { return &value }

func validDeploymentOptions() *KubernetesDeploymentOptions {
	return &KubernetesDeploymentOptions{
		Labels: map[string]string{"app.example.com/tier": "web"}, Annotations: map[string]string{"example.com/revision": "a/first\nsecond"},
		Environment:  []KubernetesDeploymentEnvironment{{Name: "MODE", Kind: "value", Value: "production"}, {Name: "PORT", Kind: "configmap", SourceName: "app-config", SourceKey: "port"}, {Name: "TOKEN", Kind: "secret", SourceName: "app-secret", SourceKey: "token"}},
		Volumes:      []KubernetesDeploymentVolume{{Name: "data", Kind: "pvc", SourceName: "app-data", MountPath: "/data"}, {Name: "configuration", Kind: "configmap", SourceName: "app-config", MountPath: "/app/config", SubPath: "settings.yaml"}, {Name: "private", Kind: "secret", SourceName: "app-secret", MountPath: "/app/private", ReadOnly: true}, {Name: "cache", Kind: "emptyDir", MountPath: "/cache"}},
		NodeSelector: map[string]string{"kubernetes.io/arch": "arm64"}, Tolerations: []KubernetesDeploymentToleration{{Key: "dedicated", Operator: "Equal", Value: "web", Effect: "NoSchedule"}, {Operator: "Exists", Effect: "NoExecute", TolerationSeconds: optionPointer(int64(60))}},
		ImagePullSecrets: []string{"existing-registry"}, MinReadySeconds: optionPointer(int32(10)), RevisionHistoryLimit: optionPointer(int32(0)), ProgressDeadlineSeconds: optionPointer(int32(120)),
	}
}

func TestKubernetesDeploymentOptionsValidateAndRejectUnsafeConfiguration(t *testing.T) {
	if err := ValidateKubernetesDeploymentOptions(nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateKubernetesDeploymentOptions(validDeploymentOptions()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*KubernetesDeploymentOptions){
		"reserved label":        func(o *KubernetesDeploymentOptions) { o.Labels["edo.io/target-id"] = "other" },
		"managed by":            func(o *KubernetesDeploymentOptions) { o.Annotations["app.kubernetes.io/managed-by"] = "other" },
		"reserved annotation":   func(o *KubernetesDeploymentOptions) { o.Annotations["edo.dev/restarted-at"] = "other" },
		"invalid node label":    func(o *KubernetesDeploymentOptions) { o.NodeSelector["Upper_bad/invalid?key"] = "web" },
		"duplicate env":         func(o *KubernetesDeploymentOptions) { o.Environment = append(o.Environment, o.Environment[0]) },
		"env reference literal": func(o *KubernetesDeploymentOptions) { o.Environment[2].Value = "do-not-store" },
		"source namespace":      func(o *KubernetesDeploymentOptions) { o.Environment[2].SourceName = "another/private" },
		"invalid env key":       func(o *KubernetesDeploymentOptions) { o.Environment[2].SourceKey = "../token" },
		"literal null":          func(o *KubernetesDeploymentOptions) { o.Environment[0].Value = "private\x00value" },
		"host path":             func(o *KubernetesDeploymentOptions) { o.Volumes[0].Kind = "hostPath" },
		"duplicate mount":       func(o *KubernetesDeploymentOptions) { o.Volumes[1].MountPath = o.Volumes[0].MountPath },
		"path traversal":        func(o *KubernetesDeploymentOptions) { o.Volumes[0].MountPath = "/app/../data" },
		"sub path traversal":    func(o *KubernetesDeploymentOptions) { o.Volumes[0].SubPath = "../data" },
		"root replacement":      func(o *KubernetesDeploymentOptions) { o.Volumes[0].MountPath = "/" },
		"empty dir source":      func(o *KubernetesDeploymentOptions) { o.Volumes[3].SourceName = "host" },
		"invalid secret name":   func(o *KubernetesDeploymentOptions) { o.ImagePullSecrets = []string{"secret/other"} },
		"duplicate secret":      func(o *KubernetesDeploymentOptions) { o.ImagePullSecrets = []string{"same", "same"} },
		"exists value":          func(o *KubernetesDeploymentOptions) { o.Tolerations[1].Value = "value" },
		"seconds wrong effect":  func(o *KubernetesDeploymentOptions) { o.Tolerations[1].Effect = "NoSchedule" },
		"negative seconds":      func(o *KubernetesDeploymentOptions) { o.Tolerations[1].TolerationSeconds = optionPointer(int64(-1)) },
		"no progress possible":  func(o *KubernetesDeploymentOptions) { o.MinReadySeconds = optionPointer(int32(120)) },
		"invalid history":       func(o *KubernetesDeploymentOptions) { o.RevisionHistoryLimit = optionPointer(int32(101)) },
		"too many env":          func(o *KubernetesDeploymentOptions) { o.Environment = make([]KubernetesDeploymentEnvironment, 65) },
		"too large annotation":  func(o *KubernetesDeploymentOptions) { o.Annotations["example.com/large"] = strings.Repeat("x", 4097) },
	} {
		t.Run(name, func(t *testing.T) {
			o := validDeploymentOptions()
			mutate(o)
			if err := ValidateKubernetesDeploymentOptions(o); !errors.Is(err, ErrKubernetesDeploymentSettings) {
				t.Fatal("无效的通用参数被接受", err)
			}
		})
	}
}

func TestKubernetesDeploymentOptionsParticipateInStagePatchAndFreeze(t *testing.T) {
	cfg := DefaultKubernetesDeploymentSettings()
	cfg.Options = validDeploymentOptions()
	patched, err := ApplyWorkflowKubernetesPatch(cfg, json.RawMessage(`{"options":{"environment":[],"labels":{"app.example.com/tier":"worker"},"revision_history_limit":0}}`))
	if err != nil || len(patched.Options.Environment) != 0 || patched.Options.Labels["app.example.com/tier"] != "worker" || *patched.Options.RevisionHistoryLimit != 0 || cfg.Options.Labels["app.example.com/tier"] != "web" || len(cfg.Options.Environment) != 3 {
		t.Fatal("阶段参数覆盖没有保持独立或丢失空数组、零值", patched, err)
	}
	cleared, err := ApplyWorkflowKubernetesPatch(cfg, json.RawMessage(`{"options":null}`))
	if err != nil || cleared.Options != nil || cfg.Options == nil {
		t.Fatal("删除可选参数失败", cleared, err)
	}
	if _, err := ApplyWorkflowKubernetesPatch(cfg, json.RawMessage(`{"options":{"privileged":true}}`)); !errors.Is(err, ErrDeploymentOverride) {
		t.Fatal("阶段覆盖接受了未登记的安全配置", err)
	}
}
