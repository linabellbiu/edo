package kube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestDeployImageRequiresAllUpdatedReplicasToBeAvailable(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status appsv1.DeploymentStatus
	}{
		// 真实 K3s 验收中记录到的旧新副本混合状态，原先 unavailable 检查也会继续等待。
		{"真实旧新副本混合", appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2, UnavailableReplicas: 1}},
		{"新副本未全部更新", appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 1, ReadyReplicas: 2, AvailableReplicas: 2}},
		{"新副本未全部可用", appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 1, AvailableReplicas: 1, UnavailableReplicas: 1}},
		// 防御性模拟不一致快照，并非宣称真实控制器曾产生 unavailable=0 的上述混合状态。
		{"防御旧新混合但缺失不可用计数", appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2}},
		{"防御异常超出预期新副本数", appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 3, ReadyReplicas: 3, AvailableReplicas: 3}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service, clusterID := deployReadinessFixture(t, func(current *appsv1.Deployment) { current.Status = tt.status })
			previous, err := service.DeployImage(context.Background(), clusterID, "acceptance", "api", "api", readinessTargetImage, readinessDeploymentID, 100*time.Millisecond)
			if !errors.Is(err, ErrDeploymentRolloutTimeout) || previous != readinessPreviousImage {
				t.Fatalf("不完整发布不应成功，且必须保留上一镜像: previous=%q err=%v", previous, err)
			}
		})
	}
}

func TestDeployImageDoesNotAcceptAChangedRolloutTarget(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*appsv1.Deployment)
	}{
		{"同名对象被重建", func(current *appsv1.Deployment) { current.UID = "replacement-deployment" }},
		{"目标版本已被后续更新取代", func(current *appsv1.Deployment) { current.Generation = 3; current.Status.ObservedGeneration = 3 }},
		{"目标容器已改成其他镜像", func(current *appsv1.Deployment) {
			current.Generation = 3
			current.Status.ObservedGeneration = 3
			current.Spec.Template.Spec.Containers[0].Image = readinessPreviousImage
		}},
		{"发布标识属于其他运行", func(current *appsv1.Deployment) {
			current.Generation = 3
			current.Status.ObservedGeneration = 3
			current.Spec.Template.Annotations["edo.io/deployment-id"] = "another-pipeline-deployment"
		}},
		{"防御镜像漂移但版本号未改变", func(current *appsv1.Deployment) {
			current.Spec.Template.Spec.Containers[0].Image = readinessPreviousImage
		}},
		{"防御发布归属漂移但版本号未改变", func(current *appsv1.Deployment) {
			current.Spec.Template.Annotations["edo.io/deployment-id"] = "another-pipeline-deployment"
		}},
		{"目标容器被移除", func(current *appsv1.Deployment) {
			current.Spec.Template.Spec.Containers = current.Spec.Template.Spec.Containers[1:]
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service, clusterID := deployReadinessFixture(t, tt.change)
			previous, err := service.DeployImage(context.Background(), clusterID, "acceptance", "api", "api", readinessTargetImage, readinessDeploymentID, 100*time.Millisecond)
			if !errors.Is(err, ErrDeploymentTargetChanged) {
				t.Fatalf("不应将已经变化的目标判定为本次发布成功: previous=%q", previous)
			}
			if previous != readinessPreviousImage {
				t.Fatalf("目标变化后丢失发布前的镜像: %q", previous)
			}
		})
	}
}

func TestDeployImageReportsObservedProgressDeadlineImmediately(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(fmt.Sprintf("控制器已观察本次版本=%v", observed), func(t *testing.T) {
			service, clusterID := deployReadinessFixture(t, func(current *appsv1.Deployment) {
				if !observed {
					current.Status.ObservedGeneration = 1
				}
				current.Status.Conditions = []appsv1.DeploymentCondition{{
					Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
					Reason: "ProgressDeadlineExceeded", Message: "internal API address must stay in diagnostics",
				}}
			})
			previous, err := service.DeployImage(context.Background(), clusterID, "acceptance", "api", "api", readinessTargetImage, readinessDeploymentID, 100*time.Millisecond)
			want := ErrDeploymentRolloutTimeout
			if observed {
				want = ErrDeploymentProgressDeadline
			}
			if !errors.Is(err, want) || previous != readinessPreviousImage {
				t.Fatalf("进度期限未按本次观察版本判定: previous=%q err=%v", previous, err)
			}
			if observed && err.Error() != ErrDeploymentProgressDeadline.Error() {
				t.Fatalf("错误包含控制器原始消息: %v", err)
			}
		})
	}
}

func TestDeployImagePreservesParentCancellationAndDeadline(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("主动取消=%v", canceled), func(t *testing.T) {
			var cancel context.CancelFunc
			service, clusterID := deployReadinessFixture(t, func(current *appsv1.Deployment) {
				current.Status.ObservedGeneration = 1
				if canceled {
					cancel()
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			previous, err := service.DeployImage(ctx, clusterID, "acceptance", "api", "api", readinessTargetImage, readinessDeploymentID, time.Second)
			want := context.DeadlineExceeded
			if canceled {
				want = context.Canceled
			}
			if !errors.Is(err, want) || errors.Is(err, ErrDeploymentRolloutTimeout) || previous != readinessPreviousImage {
				t.Fatalf("取消与父级超时被误报为就绪超时: previous=%q err=%v", previous, err)
			}
		})
	}
}

func TestDeployImageClassifiesUnreadableAndRemovedTargets(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		reason string
		want   error
	}{
		{"持续读取失败", http.StatusInternalServerError, "InternalError", ErrDeploymentStatusUnavailable},
		{"权限丢失", http.StatusForbidden, "Forbidden", ErrDeploymentStatusUnavailable},
		{"登录失效", http.StatusUnauthorized, "Unauthorized", ErrDeploymentStatusUnavailable},
		{"对象已删除", http.StatusNotFound, "NotFound", ErrDeploymentTargetChanged},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service, clusterID := deployReadinessFixture(t, nil, func(w http.ResponseWriter, r *http.Request, updated bool) bool {
				if r.Method != http.MethodGet || !updated {
					return false
				}
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":%q,"code":%d,"message":"internal cluster diagnostics"}`, tt.reason, tt.status)
				return true
			})
			previous, err := service.DeployImage(context.Background(), clusterID, "acceptance", "api", "api", readinessTargetImage, readinessDeploymentID, 100*time.Millisecond)
			if !errors.Is(err, tt.want) || previous != readinessPreviousImage {
				t.Fatalf("读取故障分类或上一镜像丢失: previous=%q err=%v", previous, err)
			}
		})
	}
}

func TestDeployImageRecoversFromTransientStatusReadFailure(t *testing.T) {
	var reads atomic.Int32
	service, clusterID := deployReadinessFixture(t, nil, func(w http.ResponseWriter, r *http.Request, updated bool) bool {
		if r.Method != http.MethodGet || !updated || reads.Add(1) != 1 {
			return false
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"InternalError","code":500}`))
		return true
	})
	previous, err := service.DeployImage(context.Background(), clusterID, "acceptance", "api", "api", readinessTargetImage, readinessDeploymentID, 3*time.Second)
	if err != nil || previous != readinessPreviousImage || reads.Load() < 2 {
		t.Fatalf("短暂读取故障恢复后未正确完成: previous=%q reads=%d err=%v", previous, reads.Load(), err)
	}
}

func TestDeployImageAllowsZeroReplicasOnlyWhenAllReplicasAreGone(t *testing.T) {
	for _, remaining := range []int32{0, 1} {
		t.Run(fmt.Sprintf("剩余副本=%d", remaining), func(t *testing.T) {
			service, clusterID := deployReadinessFixtureForReplicas(t, 0, func(current *appsv1.Deployment) {
				current.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: remaining}
			})
			previous, err := service.DeployImage(context.Background(), clusterID, "acceptance", "api", "api", readinessTargetImage, readinessDeploymentID, 100*time.Millisecond)
			if remaining == 0 && err != nil || remaining != 0 && !errors.Is(err, ErrDeploymentRolloutTimeout) || previous != readinessPreviousImage {
				t.Fatalf("零副本发布没有等待旧副本全部退出: previous=%q err=%v", previous, err)
			}
		})
	}
}

func TestDeployImageWaitsForItsObservedGeneration(t *testing.T) {
	service, clusterID := deployReadinessFixture(t, func(current *appsv1.Deployment) {
		current.Status.ObservedGeneration = 1
	})
	_, err := service.DeployImage(context.Background(), clusterID, "acceptance", "api", "api", readinessTargetImage, readinessDeploymentID, 100*time.Millisecond)
	if !errors.Is(err, ErrDeploymentRolloutTimeout) {
		t.Fatalf("控制器尚未观察到本次版本时不应成功: %v", err)
	}
}

func TestDeployImageSucceedsForTheCompleteUnchangedRollout(t *testing.T) {
	service, clusterID := deployReadinessFixture(t, nil)
	previous, err := service.DeployImage(context.Background(), clusterID, "acceptance", "api", "api", readinessTargetImage, readinessDeploymentID, time.Second)
	if err != nil || previous != readinessPreviousImage {
		t.Fatalf("本次目标和镜像的全部副本就绪后应该成功: previous=%q err=%v", previous, err)
	}
}
