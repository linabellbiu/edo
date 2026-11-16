package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/cicd"
	"edo/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func holdFirstKubernetesManifest(t *testing.T, f *kubernetesRegistryFixture) (<-chan struct{}, func()) {
	t.Helper()
	started, gate := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	f.beforeManifest = func(r *http.Request) {
		if first.CompareAndSwap(false, true) {
			close(started)
			select {
			case <-gate:
			case <-r.Context().Done():
			}
		}
	}
	return started, release
}

func startHeldKubernetesRegistry(t *testing.T, f *kubernetesRegistryFixture, started <-chan struct{}) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	t.Cleanup(cancel)
	go func() { done <- f.s.loadKubernetesRegistryImage(ctx, f.prepared) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("远程镜像请求没有开始: %v", err)
	case <-ctx.Done():
		t.Fatal("等待远程镜像请求超时")
	}
	return done
}

func finishHeldKubernetesRegistry(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("镜像冻结没有在响应后完成")
		return nil
	}
}

func TestKubernetesRegistrySlowRequestDoesNotLockOtherNodesAndMergesSnapshots(t *testing.T) {
	f := newKubernetesRegistryFixture(t, "v1")
	other := *f.prepared
	other.node.ID = "other-deploy"
	other.node.Config.DeploymentPlanID = "other-plan"
	other.node.Config.DeploymentInputs = append([]model.DeploymentInput(nil), other.node.Config.DeploymentInputs...)
	other.node.Config.DeploymentInputs[0].Tag = "v2"
	other.deploymentPlan.ID = "other-plan"
	other.target.ID, other.target.WorkloadName = "other-target", "other-api"
	f.prepared.snapshot.Stages[0].Tasks = append(f.prepared.snapshot.Stages[0].Tasks, other.node)
	f.prepared.snapshot.DeploymentPlans[other.node.ID] = workflowDeploymentPlanSnapshot{ID: other.deploymentPlan.ID, Kind: model.DeploymentPlanKubernetes}
	f.prepared.snapshot.DeploymentTargets[other.node.ID] = workflowDeploymentTargetSnapshot{ID: other.target.ID, Platform: other.target.Platform, RuntimeID: other.target.RuntimeID, Namespace: other.target.Namespace, WorkloadName: other.target.WorkloadName, ContainerName: other.target.ContainerName}
	f.saveSnapshot(t)
	other.snapshot = f.prepared.snapshot
	for _, prepared := range []*executionContext{f.prepared, &other} {
		prepared.run.NodeExecution, prepared.run.ExecutionJobID = true, "job-"+prepared.node.ID
		record := model.PipelineNodeRun{ID: uuid.NewString(), ProjectID: prepared.run.ProjectID, DepartmentID: prepared.run.DepartmentID, PipelineRunID: prepared.run.ID, NodeID: prepared.node.ID, JobID: prepared.run.ExecutionJobID, Type: model.WorkflowNodeDeploy, Status: model.PipelineRunRunning}
		if err := f.s.db.WithContext(f.ctx).Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	started, release := holdFirstKubernetesManifest(t, f)
	done := startHeldKubernetesRegistry(t, f, started)
	ctx, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	if err := f.s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.PipelineNodeRun{}).Where("pipeline_run_id = ? AND node_id = ?", other.run.ID, other.node.ID).Update("message", "另一个节点仍在运行").Error; err != nil {
			return err
		}
		return tx.Model(&model.PipelineRun{}).Where("uuid = ?", other.run.ID).Update("message", "并行任务继续").Error
	}); err != nil {
		t.Fatalf("慢仓库占用了其他节点写状态所需的事务或父运行锁: %v", err)
	}
	if err := f.s.loadKubernetesRegistryImage(ctx, &other); err != nil {
		t.Fatalf("另一个部署节点不能独立冻结镜像: %v", err)
	}
	release()
	if err := finishHeldKubernetesRegistry(t, done); err != nil {
		t.Fatal(err)
	}
	var run model.PipelineRun
	if err := f.s.db.First(&run, "uuid = ?", f.prepared.run.ID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(&run)
	if err != nil || len(snapshot.RegistryInputs[f.prepared.node.ID]) != 1 || len(snapshot.RegistryInputs[other.node.ID]) != 1 || snapshot.RegistryInputs[other.node.ID][0].Tag != "v2" || snapshot.RegistryInputs[f.prepared.node.ID][0].Tag != "v1" || run.Message != "并行任务继续" {
		t.Fatalf("迟到冻结覆盖了其他节点快照或运行状态: err=%v snapshot=%+v", err, snapshot)
	}
}

func TestKubernetesRegistryPendingRequestRechecksExecutionAndAuthority(t *testing.T) {
	for _, mutation := range []string{"停止中", "已取消", "任务已替换", "并行任务已失败", "身份已停用", "仓库已停用", "仓库端点变化", "仓库认证变化", "节点输入变化"} {
		t.Run(mutation, func(t *testing.T) {
			f := newKubernetesRegistryFixture(t, "v1")
			if mutation == "并行任务已失败" {
				f.prepared.run.NodeExecution, f.prepared.run.ExecutionJobID = true, "job-deploy"
				if err := f.s.db.WithContext(f.ctx).Create(&model.PipelineNodeRun{ID: uuid.NewString(), ProjectID: f.prepared.run.ProjectID, DepartmentID: f.prepared.run.DepartmentID, PipelineRunID: f.prepared.run.ID, NodeID: f.prepared.node.ID, JobID: "job-deploy", Type: model.WorkflowNodeDeploy, Status: model.PipelineRunRunning}).Error; err != nil {
					t.Fatal(err)
				}
			}
			started, release := holdFirstKubernetesManifest(t, f)
			done := startHeldKubernetesRegistry(t, f, started)
			ctx, cancel := context.WithTimeout(f.ctx, time.Second)
			defer cancel()
			db := f.s.db.WithContext(ctx)
			var err error
			switch mutation {
			case "停止中":
				err = db.Model(&model.PipelineRun{}).Where("uuid = ?", f.prepared.run.ID).Update("stage", stoppingStage).Error
			case "已取消":
				err = db.Model(&model.PipelineRun{}).Where("uuid = ?", f.prepared.run.ID).Update("status", model.PipelineRunCanceled).Error
			case "任务已替换":
				err = db.Model(&model.PipelineRun{}).Where("uuid = ?", f.prepared.run.ID).Update("execution_job_id", "different-job").Error
			case "并行任务已失败":
				err = db.Model(&model.PipelineNodeRun{}).Where("pipeline_run_id = ? AND node_id = ?", f.prepared.run.ID, f.prepared.node.ID).Update("status", model.PipelineRunFailed).Error
			case "身份已停用":
				err = db.Model(&model.User{}).Where("uuid = ?", "admin").Update("is_active", false).Error
			case "仓库已停用":
				err = db.Model(&model.ImageRegistry{}).Where("uuid = ?", f.registry.ID).Update("is_active", false).Error
			case "仓库端点变化":
				err = db.Model(&model.ImageRegistry{}).Where("uuid = ?", f.registry.ID).Update("endpoint", "https://registry.invalid").Error
			case "仓库认证变化":
				err = db.Model(&model.ImageRegistry{}).Where("uuid = ?", f.registry.ID).Update("username", "new-reader").Error
			case "节点输入变化":
				var document map[string]json.RawMessage
				_ = json.Unmarshal([]byte(f.prepared.run.WorkflowSnapshot), &document)
				stages := strings.Replace(string(document["stages"]), `"tag":"v1"`, `"tag":"v2"`, 1)
				document["stages"] = json.RawMessage(stages)
				raw, _ := json.Marshal(document)
				err = db.Model(&model.PipelineRun{}).Where("uuid = ?", f.prepared.run.ID).Update("workflow_snapshot", string(raw)).Error
			}
			if err != nil {
				t.Fatalf("远程查询阻塞了状态变更: %v", err)
			}
			release()
			if err := finishHeldKubernetesRegistry(t, done); err == nil || mutation == "身份已停用" && !errors.Is(err, cicd.ErrExecutionAuthorization) {
				t.Fatalf("迟到查询没有拒绝变化后的执行边界: %v", err)
			}
			var run model.PipelineRun
			if err := f.s.db.First(&run, "uuid = ?", f.prepared.run.ID).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, err := parseWorkflowSnapshot(&run)
			if err != nil || len(snapshot.RegistryInputs[f.prepared.node.ID]) != 0 {
				t.Fatalf("失效任务仍写入镜像摘要: %v", err)
			}
		})
	}
}

func TestKubernetesRegistryConcurrentRedeliveryKeepsFirstFrozenDigest(t *testing.T) {
	f := newKubernetesRegistryFixture(t, "v1")
	redelivery := *f.prepared
	started, release := holdFirstKubernetesManifest(t, f)
	done := startHeldKubernetesRegistry(t, f, started)
	f.revision.Store(1)
	ctx, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	if err := f.s.loadKubernetesRegistryImage(ctx, &redelivery); err != nil {
		t.Fatal(err)
	}
	f.revision.Store(0)
	release()
	if err := finishHeldKubernetesRegistry(t, done); err != nil {
		t.Fatal(err)
	}
	if redelivery.artifact.Digest != "sha256:"+strings.Repeat("b", 64) || f.prepared.artifact.Digest != redelivery.artifact.Digest || f.heads.Load() != 2 {
		t.Fatal("迟到的可变 Tag 查询覆盖了已固定摘要")
	}
}
