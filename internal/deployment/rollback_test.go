package deployment

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bsm/redislock"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/client-go/kubernetes/scheme"

	"edo/internal/database"
	"edo/internal/kube"
	"edo/internal/model"
)

const rollbackGoodImage = "registry.example.test/team/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const rollbackBadImage = "registry.example.test/team/api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type rollbackFixture struct {
	service *Service
	db      *gorm.DB
	source  model.DeploymentRecord
	mu      sync.Mutex
	live    *appsv1.Deployment
	writes  int
}

func newKubernetesRollbackFixture(t *testing.T, status model.DeploymentStatus) *rollbackFixture {
	t.Helper()
	service, db, _ := newDeploymentTestService(t)
	f := &rollbackFixture{service: service, db: db}
	replicas := int32(2)
	f.live = &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "acceptance", UID: "original-uid", Generation: 1, ResourceVersion: "11"},
		Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"edo.io/deployment-id": "source-release"}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: rollbackBadImage}, {Name: "sidecar", Image: "sidecar:unchanged"}}},
		}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2},
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = io.WriteString(w, `{"gitVersion":"v1.35.2"}`)
			return
		}
		if r.URL.Path != "/apis/apps/v1/namespaces/acceptance/deployments/api" {
			t.Errorf("越界访问了其他工作负载: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			object, _, err := protobuf.NewSerializer(scheme.Scheme, scheme.Scheme).Decode(body, nil, nil)
			next, ok := object.(*appsv1.Deployment)
			if err != nil || !ok {
				t.Errorf("解码 Kubernetes 更新失败: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if next.UID != f.live.UID || next.ResourceVersion != f.live.ResourceVersion || next.Spec.Template.Spec.Containers[1].Image != "sidecar:unchanged" {
				t.Error("回退丢失对象身份或修改了其他容器")
			}
			next.Generation++
			next.ResourceVersion = "12"
			next.Status.ObservedGeneration = next.Generation
			f.live = next
			f.writes++
		}
		_ = json.NewEncoder(w).Encode(f.live)
	}))
	t.Cleanup(server.Close)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	kubeconfig := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: %s\n    certificate-authority-data: %s\nusers:\n- name: edo\n  user:\n    token: test\ncontexts:\n- name: test\n  context:\n    cluster: test\n    user: edo\ncurrent-context: test\n", server.URL, base64.StdEncoding.EncodeToString(certificate))
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "operator", DepartmentID: database.DefaultDepartmentID, ProjectID: "project-a"})
	cluster, err := service.kube.Create(ctx, "operator", kube.Input{Name: "验收集群", Mode: model.KubernetesKubeconfig, Kubeconfig: &kubeconfig})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	resources := []any{
		&model.GitRepository{ID: "rollback-repo", Name: "回退仓库", IsActive: true},
		&model.Application{ID: "rollback-app", Name: "rollback_app", RepositoryID: "rollback-repo", IsActive: true},
		&model.PipelineRun{ID: "source-run", ApplicationID: "rollback-app", Status: model.PipelineRunFailed},
		&model.DeploymentTarget{ID: "source-target", Name: "独立验收目标", Platform: model.DeploymentKubernetes, RuntimeID: cluster.ID, Namespace: "acceptance", WorkloadName: "api", ContainerName: "api", RolloutTimeout: 30, IsActive: true},
		&model.DeploymentPlan{ID: "source-plan", Name: "验收部署", Kind: model.DeploymentPlanKubernetes, DeploymentTargetID: "source-target", IsActive: true},
		&model.Artifact{ID: "good-artifact", ApplicationID: "rollback-app", PipelineRunID: "source-run", ImageRef: rollbackGoodImage, Kind: model.ArtifactKindOCIImage, Digest: "sha256:" + strings.Repeat("a", 64), Status: model.ArtifactStatusAvailable},
		&model.Artifact{ID: "bad-artifact", ApplicationID: "rollback-app", PipelineRunID: "source-run", ImageRef: rollbackBadImage, Kind: model.ArtifactKindOCIImage, Digest: "sha256:" + strings.Repeat("b", 64), Status: model.ArtifactStatusAvailable},
	}
	for _, resource := range resources {
		if err := db.WithContext(ctx).Create(resource).Error; err != nil {
			t.Fatalf("创建回退验收数据失败: %T %v", resource, err)
		}
	}
	f.source = model.DeploymentRecord{
		ID: "source-release", PipelineRunID: "source-run", ApplicationID: "rollback-app", WorkflowNodeID: "deploy-api", ArtifactID: "bad-artifact",
		TargetID: "source-target", TargetName: "独立验收目标", Platform: model.DeploymentKubernetes, RuntimeID: cluster.ID,
		Namespace: "acceptance", WorkloadName: "api", ContainerName: "api", KubernetesUID: "original-uid", RolloutTimeout: 30,
		DeploymentPlanID: "source-plan", DeploymentPlanKind: model.DeploymentPlanKubernetes, Operation: model.DeploymentRelease,
		Image: rollbackBadImage, PreviousImage: rollbackGoodImage, Status: status, RequestedBy: "operator", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.WithContext(ctx).Create(&f.source).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *rollbackFixture) confirmation() RollbackConfirmation {
	return RollbackConfirmation{ExpectedCurrentImage: f.source.Image, ExpectedTargetImage: f.source.PreviousImage,
		ExpectedKubernetesUID: f.source.KubernetesUID, ExpectedSourceUpdatedAt: f.source.UpdatedAt.Format(time.RFC3339Nano)}
}

func TestKubernetesRollbackPreservesOwnershipAndPreviousArtifact(t *testing.T) {
	for _, status := range []model.DeploymentStatus{model.DeploymentSucceeded, model.DeploymentFailed} {
		t.Run(string(status), func(t *testing.T) {
			f := newKubernetesRollbackFixture(t, status)
			preview, err := f.service.RollbackPreview(context.Background(), f.source.ID)
			if err != nil || !preview.CanRollback || preview.TargetImage != rollbackGoodImage || preview.ClusterName != "验收集群" {
				t.Fatalf("已实际应用镜像的发布无法预检回退: %+v %v", preview, err)
			}
			record, err := f.service.RollbackChecked(context.Background(), f.source.ID, "operator", f.confirmation())
			if err != nil {
				t.Fatal(err)
			}
			if record.ApplicationID != f.source.ApplicationID || record.PipelineRunID != f.source.PipelineRunID ||
				record.WorkflowNodeID != f.source.WorkflowNodeID || record.ProjectID != f.source.ProjectID ||
				record.ArtifactID != "good-artifact" || record.Image != rollbackGoodImage || record.KubernetesUID != f.source.KubernetesUID ||
				record.DeploymentPlanID != f.source.DeploymentPlanID || record.Namespace != f.source.Namespace || record.ContainerName != f.source.ContainerName {
				t.Fatalf("回退归属、上一镜像制品或目标快照错误: %+v", record)
			}
			if err := f.service.Run(context.Background(), record.ID); err != nil {
				t.Fatal(err)
			}
			reused, err := f.service.RollbackChecked(context.Background(), f.source.ID, "operator", f.confirmation())
			if err != nil || reused.ID != record.ID || reused.Status != model.DeploymentSucceeded || f.writes != 1 {
				t.Fatalf("重复确认触发了重复回退: %+v %v writes=%d", reused, err, f.writes)
			}
			var original model.DeploymentRecord
			if err := f.db.First(&original, "uuid = ?", f.source.ID).Error; err != nil || original.Status != status || original.ArtifactID != "bad-artifact" || original.Image != rollbackBadImage {
				t.Fatalf("回退改写了来源历史: %+v %v", original, err)
			}
			records, err := f.service.ListForPipelineRun(context.Background(), f.source.PipelineRunID, 50)
			if err != nil || len(records) != 2 {
				t.Fatalf("原流水线上下文没有包含回退进度: %+v %v", records, err)
			}
		})
	}
}

func TestKubernetesRollbackRejectsChangedIdentityAndScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*rollbackFixture, *RollbackConfirmation)
	}{
		{"旧记录没有UID", func(f *rollbackFixture, c *RollbackConfirmation) {
			f.db.Model(&model.DeploymentRecord{}).Where("uuid = ?", f.source.ID).Update("kubernetes_uid", "")
		}},
		{"同名对象重建", func(f *rollbackFixture, c *RollbackConfirmation) { f.live.UID = "replacement-uid" }},
		{"镜像被其他操作更改", func(f *rollbackFixture, c *RollbackConfirmation) {
			f.live.Spec.Template.Spec.Containers[0].Image = rollbackGoodImage
		}},
		{"发布归属被覆盖", func(f *rollbackFixture, c *RollbackConfirmation) {
			f.live.Spec.Template.Annotations["edo.io/deployment-id"] = "foreign-release"
		}},
		{"方案目标迁移", func(f *rollbackFixture, c *RollbackConfirmation) {
			f.db.Model(&model.DeploymentTarget{}).Where("uuid = ?", f.source.TargetID).Update("namespace", "other")
		}},
		{"来源运行属于其他应用", func(f *rollbackFixture, c *RollbackConfirmation) {
			other := model.Application{ID: "foreign-app", Name: "foreign_app", RepositoryID: "rollback-repo", ProjectID: f.source.ProjectID, DepartmentID: f.source.DepartmentID}
			if err := f.db.Create(&other).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Model(&model.PipelineRun{}).Where("uuid = ?", f.source.PipelineRunID).Update("application_id", other.ID).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"确认目标被篡改", func(f *rollbackFixture, c *RollbackConfirmation) { c.ExpectedTargetImage = rollbackBadImage }},
		{"确认记录版本过期", func(f *rollbackFixture, c *RollbackConfirmation) {
			c.ExpectedSourceUpdatedAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newKubernetesRollbackFixture(t, model.DeploymentFailed)
			confirmation := f.confirmation()
			tc.change(f, &confirmation)
			if _, err := f.service.RollbackChecked(context.Background(), f.source.ID, "operator", confirmation); !errors.Is(err, ErrRollbackTargetChanged) {
				t.Fatalf("变化的目标仍允许回退: %v", err)
			}
			if f.writes != 0 {
				t.Fatalf("拒绝回退后仍更新了集群: %d", f.writes)
			}
		})
	}
	f := newKubernetesRollbackFixture(t, model.DeploymentFailed)
	foreign := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "operator", DepartmentID: f.source.DepartmentID, ProjectID: "project-b"})
	if _, err := f.service.RollbackPreview(foreign, f.source.ID); !errors.Is(err, ErrDeploymentNotFound) {
		t.Fatalf("跨项目预检读取了部署: %v", err)
	}
	if _, err := f.service.RollbackChecked(foreign, f.source.ID, "operator", f.confirmation()); !errors.Is(err, ErrDeploymentNotFound) {
		t.Fatalf("跨项目发起了回退: %v", err)
	}
}

func TestKubernetesRollbackRechecksAfterQueueAndNeverInventsArtifact(t *testing.T) {
	f := newKubernetesRollbackFixture(t, model.DeploymentFailed)
	if err := f.db.Delete(&model.Artifact{}, "uuid = ?", "good-artifact").Error; err != nil {
		t.Fatal(err)
	}
	other := model.Application{ID: "other-artifact-app", Name: "other_artifact_app", RepositoryID: "rollback-repo", ProjectID: f.source.ProjectID, DepartmentID: f.source.DepartmentID}
	if err := f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	foreignArtifact := model.Artifact{ID: "foreign-good-artifact", ApplicationID: other.ID, ImageRef: rollbackGoodImage, Kind: model.ArtifactKindOCIImage, Digest: "sha256:" + strings.Repeat("a", 64), Status: model.ArtifactStatusAvailable}
	if err := f.db.Create(&foreignArtifact).Error; err != nil {
		t.Fatal(err)
	}
	record, err := f.service.RollbackChecked(context.Background(), f.source.ID, "operator", f.confirmation())
	if err != nil || record.ArtifactID != "" {
		t.Fatalf("没有上一镜像制品时不应借用失败制品: %+v %v", record, err)
	}
	f.live.UID = "recreated-while-queued"
	if err := f.service.Run(context.Background(), record.ID); err == nil {
		t.Fatal("排队后目标变化仍执行了回退")
	}
	var stored model.DeploymentRecord
	if err := f.db.First(&stored, "uuid = ?", record.ID).Error; err != nil || stored.Status != model.DeploymentFailed || f.writes != 0 {
		t.Fatalf("失效回退没有安全终止: %+v %v writes=%d", stored, err, f.writes)
	}
}

func TestKubernetesReleaseSavesIdentityEvenWhenReadinessFails(t *testing.T) {
	f := newKubernetesRollbackFixture(t, model.DeploymentQueued)
	if err := f.db.Model(&model.DeploymentRecord{}).Where("uuid = ?", f.source.ID).
		Updates(map[string]any{"kubernetes_uid": "", "previous_image": "", "rollout_timeout": 1}).Error; err != nil {
		t.Fatal(err)
	}
	f.live.Spec.Template.Spec.Containers[0].Image = rollbackGoodImage
	f.live.Spec.Template.Annotations["edo.io/deployment-id"] = "previous-release"
	f.live.Status.ReadyReplicas, f.live.Status.AvailableReplicas = 0, 0
	if err := f.service.Run(context.Background(), f.source.ID); err == nil {
		t.Fatal("未就绪的新镜像被报告为成功")
	}
	var record model.DeploymentRecord
	if err := f.db.First(&record, "uuid = ?", f.source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.KubernetesUID != "original-uid" || record.PreviousImage != rollbackGoodImage || record.Status != model.DeploymentFailed || f.writes != 1 {
		t.Fatalf("实际更新后丢失安全回退所需身份: %+v writes=%d", record, f.writes)
	}
	preview, err := f.service.RollbackPreview(context.Background(), record.ID)
	if err != nil || !preview.CanRollback {
		t.Fatalf("真实就绪失败记录不能安全回退: %+v %v", preview, err)
	}
}

func TestKubernetesDeploymentLockUsesPhysicalWorkload(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := NewService(nil, nil, nil, nil, redislock.New(client), "test:deployment", nil)
	first := &model.DeploymentRecord{ID: "first", TargetID: "plan-a-target", Platform: model.DeploymentKubernetes, RuntimeID: "cluster", Namespace: "ns", WorkloadName: "api", RolloutTimeout: 30}
	second := *first
	second.ID, second.TargetID = "second", "plan-b-target"
	lock, err := service.acquireTargetLock(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if competing, err := service.acquireTargetLock(ctx, &second); err == nil || competing != nil {
		t.Fatalf("不同方案仍能同时修改同一工作负载: lock=%v err=%v", competing, err)
	}
	second.Namespace = "other"
	independent, err := service.acquireTargetLock(context.Background(), &second)
	if err != nil {
		t.Fatalf("不同工作负载被全局串行化: %v", err)
	}
	defer independent.Release(context.Background())
}

func TestKubernetesDeploymentTargetDoesNotRequireSSHHostMembership(t *testing.T) {
	f := newKubernetesRollbackFixture(t, model.DeploymentSucceeded)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "operator", DepartmentID: f.source.DepartmentID, ProjectID: f.source.ProjectID})
	environment := model.Environment{ID: "independent-environment", Name: "独立集群环境", IsActive: true}
	if err := f.db.WithContext(ctx).Create(&environment).Error; err != nil {
		t.Fatal(err)
	}
	target, err := f.service.CreateTarget(ctx, "operator", TargetInput{Name: "独立集群部署", Platform: model.DeploymentKubernetes,
		EnvironmentID: environment.ID, HostID: "client-must-not-select-host", RuntimeID: f.source.RuntimeID,
		Namespace: "acceptance", WorkloadName: "api", ContainerName: "api"})
	if err != nil || target.HostID != "" || target.RuntimeID != f.source.RuntimeID {
		t.Fatalf("独立集群仍要求环境关联 SSH 主机: %+v %v", target, err)
	}
	plan := model.DeploymentPlan{ID: "independent-plan", Name: "独立集群方案", Kind: model.DeploymentPlanKubernetes, DeploymentTargetID: target.ID, IsActive: true}
	if err := f.db.WithContext(ctx).Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return f.service.ReconcileEnvironmentTargets(ctx, tx, environment.ID) }); err != nil {
		t.Fatalf("调整空环境仍要求集群绑定 SSH 主机: %v", err)
	}
	foreign := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "operator", DepartmentID: f.source.DepartmentID, ProjectID: "project-b"})
	otherEnvironment := model.Environment{ID: "foreign-cluster-environment", Name: "其他项目环境", IsActive: true}
	if err := f.db.WithContext(foreign).Create(&otherEnvironment).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CreateTarget(foreign, "operator", TargetInput{Name: "越界集群部署", Platform: model.DeploymentKubernetes, EnvironmentID: otherEnvironment.ID,
		RuntimeID: f.source.RuntimeID, Namespace: "acceptance", WorkloadName: "api", ContainerName: "api"}); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("独立集群绕过了项目权限: %v", err)
	}
}
