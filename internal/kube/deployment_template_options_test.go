package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func optionTestPointer[T any](value T) *T { return &value }

func templateOptionFixtureReferences(f *templateAPIFixture) {
	for _, item := range []struct{ resource, kind, name string }{{"configmaps", "ConfigMap", "app-config"}, {"secrets", "Secret", "app-secret"}, {"secrets", "Secret", "existing-registry"}, {"persistentvolumeclaims", "PersistentVolumeClaim", "app-data"}} {
		object := map[string]any{"apiVersion": "v1", "kind": item.kind, "metadata": map[string]any{"name": item.name, "namespace": "demo", "uid": item.name, "resourceVersion": "1"}}
		if item.resource == "configmaps" {
			object["data"] = map[string]any{"port": "8080", "settings.yaml": "private-reference-content"}
		}
		if item.resource == "secrets" {
			object["data"] = map[string]any{"token": "bmV2ZXItZXhwb3NlLXNlY3JldC1jb250ZW50"}
			object["type"] = "Opaque"
			if item.name == "existing-registry" {
				object["type"] = "kubernetes.io/dockerconfigjson"
			}
		}
		f.objects["/api/v1/namespaces/demo/"+item.resource+"/"+item.name] = object
	}
}

func templateOptionConfig() *model.KubernetesDeploymentOptions {
	return &model.KubernetesDeploymentOptions{
		Labels: map[string]string{"app.example.com/tier": "web"}, Annotations: map[string]string{"prometheus.io/scrape": "true"},
		Environment:  []model.KubernetesDeploymentEnvironment{{Name: "MODE", Kind: "value", Value: "production"}, {Name: "PORT", Kind: "configmap", SourceName: "app-config", SourceKey: "port"}, {Name: "TOKEN", Kind: "secret", SourceName: "app-secret", SourceKey: "token"}},
		Volumes:      []model.KubernetesDeploymentVolume{{Name: "data", Kind: "pvc", SourceName: "app-data", MountPath: "/data"}, {Name: "configuration", Kind: "configmap", SourceName: "app-config", MountPath: "/app/config", SubPath: "settings.yaml"}, {Name: "private", Kind: "secret", SourceName: "app-secret", MountPath: "/app/private"}, {Name: "cache", Kind: "emptyDir", MountPath: "/cache"}},
		NodeSelector: map[string]string{"kubernetes.io/arch": "arm64"}, Tolerations: []model.KubernetesDeploymentToleration{{Key: "dedicated", Operator: "Equal", Value: "web", Effect: "NoSchedule"}, {Operator: "Exists", Effect: "NoExecute", TolerationSeconds: optionTestPointer(int64(60))}},
		ImagePullSecrets: []string{"existing-registry"}, MinReadySeconds: optionTestPointer(int32(10)), RevisionHistoryLimit: optionTestPointer(int32(0)), ProgressDeadlineSeconds: optionTestPointer(int32(120)),
	}
}

func TestKubernetesDeploymentTemplateOptionsReachAPIAndClearOwnedFields(t *testing.T) {
	s, clusterID, f := newTemplateAPIFixture(t)
	templateOptionFixtureReferences(f)
	r := templateTestRecord(clusterID)
	cfg := model.DefaultKubernetesDeploymentSettings()
	cfg.Options = templateOptionConfig()
	registry := RegistryPullConfig("registry.example.test", "user", "do-not-log-password")
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, registry, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	location := "/apis/apps/v1/namespaces/demo/deployments/api"
	var deployment appsv1.Deployment
	if err := jsonRoundTrip(f.objects[location], &deployment); err != nil {
		t.Fatal(err)
	}
	pod := deployment.Spec.Template.Spec
	c := pod.Containers[0]
	if len(c.Env) != 3 || c.Env[0].Value != "production" || c.Env[1].ValueFrom.ConfigMapKeyRef.Name != "app-config" || c.Env[2].ValueFrom.SecretKeyRef.Key != "token" || c.Env[2].Value != "" {
		t.Fatal("环境变量没有保存准确引用或展开了密钥", c.Env)
	}
	if len(pod.Volumes) != 4 || pod.Volumes[0].PersistentVolumeClaim.ClaimName != "app-data" || pod.Volumes[1].ConfigMap.Name != "app-config" || pod.Volumes[2].Secret.SecretName != "app-secret" || pod.Volumes[3].EmptyDir == nil || len(c.VolumeMounts) != 4 || c.VolumeMounts[1].SubPath != "settings.yaml" || !c.VolumeMounts[1].ReadOnly || !c.VolumeMounts[2].ReadOnly {
		t.Fatal("存储挂载未进入准确类型或路径", pod.Volumes, c.VolumeMounts)
	}
	if pod.HostNetwork || pod.HostPID || pod.HostIPC || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("通用参数扩展了宿主机或服务账号权限")
	}
	if len(pod.NodeSelector) != 1 || pod.NodeSelector["kubernetes.io/arch"] != "arm64" || len(pod.Tolerations) != 2 || pod.Tolerations[1].Operator != corev1.TolerationOpExists || *pod.Tolerations[1].TolerationSeconds != 60 || len(pod.ImagePullSecrets) != 2 {
		t.Fatal("调度或已有镜像密钥引用未保存", pod)
	}
	if deployment.Spec.MinReadySeconds != 10 || *deployment.Spec.RevisionHistoryLimit != 0 || *deployment.Spec.ProgressDeadlineSeconds != 120 || deployment.Labels["app.example.com/tier"] != "web" || deployment.Spec.Template.Labels["app.example.com/tier"] != "web" || deployment.Annotations["prometheus.io/scrape"] != "true" || deployment.Spec.Template.Annotations["prometheus.io/scrape"] != "true" {
		t.Fatal("发布参数或元数据未进入生成资源", deployment.Spec)
	}
	if _, customSelector := deployment.Spec.Selector.MatchLabels["app.example.com/tier"]; customSelector {
		t.Fatal("自定义标签污染身份选择器")
	}
	// 仅清理由该扩展明确管理过的键，保留其他工具写入的标签和注解。
	live := f.objects[location]
	liveMeta := live["metadata"].(map[string]any)
	liveMeta["labels"].(map[string]any)["external.example.com/keep"] = "yes"
	liveMeta["annotations"].(map[string]any)["external.example.com/keep"] = "yes"
	cfg.Options = nil
	r.ID = "release-options-cleared"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	deployment = appsv1.Deployment{}
	if err := jsonRoundTrip(f.objects[location], &deployment); err != nil {
		t.Fatal(err)
	}
	pod = deployment.Spec.Template.Spec
	if len(pod.Containers[0].Env) != 0 || len(pod.Containers[0].VolumeMounts) != 0 || len(pod.Volumes) != 0 || len(pod.NodeSelector) != 0 || len(pod.Tolerations) != 0 || len(pod.ImagePullSecrets) != 0 || deployment.Spec.MinReadySeconds != 0 || deployment.Spec.RevisionHistoryLimit != nil || deployment.Spec.ProgressDeadlineSeconds != nil {
		t.Fatal("取消扩展没有清除上次模板管理的字段", deployment.Spec)
	}
	if _, stale := deployment.Labels["app.example.com/tier"]; stale || deployment.Labels["external.example.com/keep"] != "yes" || deployment.Annotations["external.example.com/keep"] != "yes" {
		t.Fatal("清理标签时误删外部键或遗留旧键", deployment.Labels, deployment.Annotations)
	}
	for _, resource := range []string{"configmaps/app-config", "secrets/app-secret", "persistentvolumeclaims/app-data"} {
		if f.objects["/api/v1/namespaces/demo/"+resource] == nil {
			t.Fatal("取消挂载删除了引用资源", resource)
		}
	}
}

func TestKubernetesDeploymentTemplateOptionsRejectMissingReferencesBeforeWrites(t *testing.T) {
	for _, scenario := range []string{"missing secret", "missing key", "missing pvc", "wrong image secret type"} {
		t.Run(scenario, func(t *testing.T) {
			s, clusterID, f := newTemplateAPIFixture(t)
			templateOptionFixtureReferences(f)
			cfg := model.DefaultKubernetesDeploymentSettings()
			cfg.Options = templateOptionConfig()
			switch scenario {
			case "missing secret":
				delete(f.objects, "/api/v1/namespaces/demo/secrets/app-secret")
			case "missing key":
				cfg.Options.Environment[2].SourceKey = "missing"
			case "missing pvc":
				delete(f.objects, "/api/v1/namespaces/demo/persistentvolumeclaims/app-data")
			case "wrong image secret type":
				f.objects["/api/v1/namespaces/demo/secrets/existing-registry"]["type"] = "Opaque"
			}
			_, _, err := s.DeployTemplate(context.Background(), templateTestRecord(clusterID), cfg, nil, time.Second)
			if !errors.Is(err, ErrDeploymentTemplateReference) || f.writes != 0 || strings.Contains(err.Error(), "private-reference-content") || strings.Contains(err.Error(), "token") {
				t.Fatal("缺失引用未在写入前安全阻断", err, f.writes)
			}
		})
	}
}

func TestKubernetesDeploymentTemplateOptionsReferencePermissionsAreExact(t *testing.T) {
	s, clusterID, f := newTemplateAPIFixture(t)
	templateOptionFixtureReferences(f)
	policies := newIdentityTestPolicies()
	policies.grant("alice", map[string][]string{"demo": {"kubernetes.config.read"}})
	s.authorizationResolver = policies.resolve
	ctx := identityTestContext(t, s, clusterID, "alice")
	target := model.DeploymentTarget{RuntimeID: clusterID, Namespace: "demo"}
	options := &model.KubernetesDeploymentOptions{Environment: []model.KubernetesDeploymentEnvironment{{Name: "TOKEN", Kind: "secret", SourceName: "app-secret", SourceKey: "token"}}}
	if err := s.checkDeploymentTemplateOptions(ctx, target, options); !errors.Is(err, ErrRBACForbidden) || f.writes != 0 {
		t.Fatal("配置查看权限获得了密钥查看能力", err)
	}
	policies.grant("alice", map[string][]string{"demo": {"kubernetes.secret.read"}})
	if err := s.checkDeploymentTemplateOptions(ctx, target, options); err != nil {
		t.Fatal("准确命名空间的密钥引用被拒绝", err)
	}
	target.Namespace = "another"
	if err := s.checkDeploymentTemplateOptions(ctx, target, options); !errors.Is(err, ErrRBACForbidden) {
		t.Fatal("引用越过命名空间授权", err)
	}
}

func TestKubernetesDeploymentTemplateOptionsDoNotCleanUnmarkedLegacyObject(t *testing.T) {
	current := map[string]any{"metadata": map[string]any{"annotations": map[string]any{"external.example.com/keep": "value"}}}
	desired := map[string]any{"metadata": map[string]any{"annotations": map[string]any{"edo.io/target-id": "target"}}, "spec": map[string]any{"template": map[string]any{"metadata": map[string]any{}, "spec": map[string]any{}}}}
	clearDeploymentTemplateOptions(desired, current, nil)
	if len(desired["spec"].(map[string]any)) != 1 || len(desired["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)) != 0 || len(desired["metadata"].(map[string]any)["annotations"].(map[string]any)) != 1 {
		t.Fatal("旧配置被扩展清理流程接管", desired)
	}
}

func TestKubernetesDeploymentTemplateMetadataOnlyOptionsPreserveExternalPodFields(t *testing.T) {
	s, clusterID, f := newTemplateAPIFixture(t)
	r := templateTestRecord(clusterID)
	cfg := model.DefaultKubernetesDeploymentSettings()
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	location := "/apis/apps/v1/namespaces/demo/deployments/api"
	object := f.objects[location]
	spec := object["spec"].(map[string]any)
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	pod["nodeSelector"] = map[string]any{"external.example.com/node": "private"}
	pod["tolerations"] = []any{map[string]any{"key": "external", "operator": "Exists", "effect": "NoSchedule"}}
	pod["volumes"] = []any{map[string]any{"name": "external", "emptyDir": map[string]any{}}}
	pod["imagePullSecrets"] = []any{map[string]any{"name": "external-registry"}}
	container := pod["containers"].([]any)[0].(map[string]any)
	container["env"] = []any{map[string]any{"name": "EXTERNAL", "value": "keep"}}
	container["volumeMounts"] = []any{map[string]any{"name": "external", "mountPath": "/external"}}
	spec["minReadySeconds"], spec["revisionHistoryLimit"], spec["progressDeadlineSeconds"] = float64(5), float64(3), float64(100)
	for _, options := range []*model.KubernetesDeploymentOptions{{Labels: map[string]string{"tier": "web"}}, {}, nil, nil} {
		cfg.Options = options
		r.ID += "-next"
		if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		var deployment appsv1.Deployment
		if err := jsonRoundTrip(f.objects[location], &deployment); err != nil {
			t.Fatal(err)
		}
		p := deployment.Spec.Template.Spec
		if p.NodeSelector["external.example.com/node"] != "private" || len(p.Tolerations) != 1 || len(p.Volumes) != 1 || len(p.ImagePullSecrets) != 1 || p.ImagePullSecrets[0].Name != "external-registry" || len(p.Containers[0].Env) != 1 || p.Containers[0].Env[0].Name != "EXTERNAL" || len(p.Containers[0].VolumeMounts) != 1 {
			t.Fatal("仅修改标签或空选项接管了外部容器参数", p)
		}
		if deployment.Spec.MinReadySeconds != 5 || deployment.Spec.RevisionHistoryLimit == nil || *deployment.Spec.RevisionHistoryLimit != 3 || deployment.Spec.ProgressDeadlineSeconds == nil || *deployment.Spec.ProgressDeadlineSeconds != 100 {
			t.Fatal("没有声明的发布字段被清空", deployment.Spec)
		}
	}
}

func TestKubernetesDeploymentTemplateOptionsRemovePriorNodeSelectorKeys(t *testing.T) {
	s, clusterID, f := newTemplateAPIFixture(t)
	r := templateTestRecord(clusterID)
	cfg := model.DefaultKubernetesDeploymentSettings()
	cfg.Options = &model.KubernetesDeploymentOptions{NodeSelector: map[string]string{"kubernetes.io/arch": "arm64", "example.com/old": "selected"}}
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	cfg.Options.NodeSelector = map[string]string{"kubernetes.io/arch": "amd64"}
	r.ID = "release-new-selector"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := jsonRoundTrip(f.objects["/apis/apps/v1/namespaces/demo/deployments/api"], &deployment); err != nil {
		t.Fatal(err)
	}
	if len(deployment.Spec.Template.Spec.NodeSelector) != 1 || deployment.Spec.Template.Spec.NodeSelector["kubernetes.io/arch"] != "amd64" {
		t.Fatal("更新节点标签残留旧调度条件", deployment.Spec.Template.Spec.NodeSelector)
	}
}
