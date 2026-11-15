package kube

import (
	"context"
	"errors"
	"testing"
	"time"

	"edo/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func templateHPAConfig() *model.KubernetesDeploymentSettings {
	cfg := model.DefaultKubernetesDeploymentSettings()
	cfg.HPA = &model.KubernetesDeploymentHPA{Enabled: true, MinReplicas: 2, MaxReplicas: 6, CPUUtilization: new(int32(80)), MemoryUtilization: new(int32(75))}
	return cfg
}

func TestDeploymentTemplateHPACreateRerunAndDisable(t *testing.T) {
	s, id, f := newTemplateAPIFixture(t)
	r, cfg := templateTestRecord(id), templateHPAConfig()
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	hpaPath, deploymentPath := "/apis/autoscaling/v2/namespaces/demo/horizontalpodautoscalers/api", "/apis/apps/v1/namespaces/demo/deployments/api"
	replicas := func() int32 {
		var workload appsv1.Deployment
		if err := jsonRoundTrip(f.objects[deploymentPath], &workload); err != nil || workload.Spec.Replicas == nil {
			t.Fatal("无法读取实际副本数", err)
		}
		return *workload.Spec.Replicas
	}
	var hpa autoscalingv2.HorizontalPodAutoscaler
	if err := jsonRoundTrip(f.objects[hpaPath], &hpa); err != nil {
		t.Fatal(err)
	}
	if hpa.Spec.ScaleTargetRef.Name != r.WorkloadName || hpa.Spec.ScaleTargetRef.APIVersion != "apps/v1" || *hpa.Spec.MinReplicas != 2 || hpa.Spec.MaxReplicas != 6 || len(hpa.Spec.Metrics) != 2 || *hpa.Spec.Metrics[0].Resource.Target.AverageUtilization != 80 || *hpa.Spec.Metrics[1].Resource.Target.AverageUtilization != 75 {
		t.Fatal("HPA 未使用准确目标和指标", hpa.Spec)
	}
	if replicas() != 2 {
		t.Fatal("首次部署未使用最小副本数")
	}
	f.objects[deploymentPath]["spec"].(map[string]any)["replicas"] = float64(5)
	cfg.HPA.MemoryUtilization = nil
	r.ID = "release-2"
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if replicas() != 5 {
		t.Fatal("再次发布重置了 HPA 调整的副本数")
	}
	if err := jsonRoundTrip(f.objects[hpaPath], &hpa); err != nil || len(hpa.Spec.Metrics) != 1 {
		t.Fatal("关闭内存目标没有生效", err, hpa.Spec)
	}
	cfg.HPA.Enabled, cfg.Replicas = false, 3
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if f.objects[hpaPath] != nil || replicas() != 3 {
		t.Fatal("关闭 HPA 后未恢复固定副本")
	}
	cfg.HPA.Enabled = true
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	cfg.HPA = nil
	if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if f.objects[hpaPath] != nil || replicas() != 3 {
		t.Fatal("阶段恢复无 HPA 的初始配置后未移除原控制器")
	}
}

func TestDeploymentTemplateHPANativeRejectionBeforeWorkloadUpdate(t *testing.T) {
	s, id, f := newTemplateAPIFixture(t)
	r := templateTestRecord(id)
	if _, _, err := s.DeployTemplate(context.Background(), r, model.DefaultKubernetesDeploymentSettings(), nil, time.Second); err != nil {
		t.Fatal(err)
	}
	before := f.writes
	f.deny = "/horizontalpodautoscalers"
	if _, _, err := s.DeployTemplate(context.Background(), r, templateHPAConfig(), nil, time.Second); !errors.Is(err, ErrResourceForbidden) || f.writes != before {
		t.Fatal("原生 HPA 拒绝仍更新了工作负载", err, f.writes, before)
	}
}

func TestDeploymentTemplateHPARejectsOtherOwnerOrTargetBeforeWrites(t *testing.T) {
	for _, kind := range []string{"其他应用", "其他目标", "另一个名称的控制器"} {
		t.Run(kind, func(t *testing.T) {
			s, id, f := newTemplateAPIFixture(t)
			r, cfg := templateTestRecord(id), templateHPAConfig()
			annotations := deploymentTemplateOwner(r.ProjectID, r.ApplicationID, r.TargetID)
			name, target := r.WorkloadName, r.WorkloadName
			switch kind {
			case "其他应用":
				annotations["edo.io/application-id"] = "other"
			case "其他目标":
				target = "other"
			case "另一个名称的控制器":
				name = "external"
			}
			f.objects["/apis/autoscaling/v2/namespaces/demo/horizontalpodautoscalers/"+name] = templateObject(&autoscalingv2.HorizontalPodAutoscaler{TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.Namespace, Annotations: annotations}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: target}}})
			if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, time.Second); !errors.Is(err, ErrDeploymentTemplateConflict) || f.writes != 0 {
				t.Fatal("冲突 HPA 被接管或目标已被修改", err, f.writes)
			}
		})
	}
}

func TestDeploymentTemplateHPARequiresExactRBACAction(t *testing.T) {
	for _, action := range []string{"create", "update", "delete"} {
		t.Run(action, func(t *testing.T) {
			s, id, f := newTemplateAPIFixture(t)
			r, cfg := templateTestRecord(id), templateHPAConfig()
			if action != "create" {
				if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, time.Second); err != nil {
					t.Fatal(err)
				}
			}
			if action == "delete" {
				cfg.HPA.Enabled = false
			}
			before := f.writes
			codes := []string{"kubernetes.workload.read", "kubernetes.workload.create", "kubernetes.workload.update", "kubernetes.workload.delete", "kubernetes.config.read", "kubernetes.config.create", "kubernetes.network.read"}
			filtered := []string{}
			for _, code := range codes {
				if code != "kubernetes.workload."+action {
					filtered = append(filtered, code)
				}
			}
			s.authorizationResolver = func(context.Context, *model.KubernetesCluster, bool) (*clusterAuthorization, error) {
				return &clusterAuthorization{Enforced: true, Permissions: map[string][]string{"demo": filtered, "": filtered}, Namespaces: []string{"demo"}}, nil
			}
			if _, _, err := s.DeployTemplate(context.Background(), r, cfg, nil, time.Second); !errors.Is(err, ErrRBACForbidden) || f.writes != before {
				t.Fatal("缺少真实动作权限仍修改了目标", err, f.writes, before)
			}
		})
	}
	for _, action := range []string{"read", "create", "update", "delete"} {
		rules := rbacRules(map[string]bool{"kubernetes.workload." + action: true}, "demo", false)
		verb := action
		if verb == "read" {
			verb = "list"
		}
		if !mappedRuleAllows(rules, "autoscaling", "horizontalpodautoscalers", verb) {
			t.Fatal("HPA 原生权限映射缺失", action)
		}
	}
}

func TestDeployImageHPAAllowsOnlyReplicaChangesDuringReadiness(t *testing.T) {
	for _, changedTemplate := range []bool{false, true} {
		s, id := deployReadinessFixture(t, func(current *appsv1.Deployment) {
			current.Generation = 3
			current.Spec.Replicas = new(int32(4))
			current.Status = appsv1.DeploymentStatus{ObservedGeneration: 3, Replicas: 4, UpdatedReplicas: 4, ReadyReplicas: 4, AvailableReplicas: 4}
			if changedTemplate {
				current.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "EXTERNAL_CHANGE", Value: "changed"}}
			}
		})
		_, err := s.DeployImageIfCurrent(context.Background(), id, "acceptance", "api", "api", readinessTargetImage, readinessDeploymentID, time.Second, DeploymentImageIdentity{allowReplicaChanges: true, UID: "original-deployment", Image: readinessPreviousImage})
		if changedTemplate && !errors.Is(err, ErrDeploymentTargetChanged) || !changedTemplate && err != nil {
			t.Fatal("HPA 扩缩容或模板变更边界不正确", changedTemplate, err)
		}
	}
}
