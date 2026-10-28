package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"edo/internal/access"
	"edo/internal/artifact"
	"edo/internal/cicd"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/repository"
)

func pipelineExecutionTestContext(ctx context.Context, run model.PipelineRun) context.Context {
	return database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: run.ProjectID, DepartmentID: run.DepartmentID})
}

func configurePipelineExecutionTestAuthority(t *testing.T, service *Service, projectID, departmentID, actorID string) {
	t.Helper()
	actor := model.User{ID: actorID, Username: "pipeline_executor", DepartmentID: departmentID, IsActive: true}
	role := model.Role{ID: "pipeline-executor-role", Name: "pipeline_executor_role", DisplayName: "流水线执行角色"}
	for _, item := range []any{&actor, &role, &model.RolePermission{RoleID: role.ID, Permission: "pipeline_run.execute"}, &model.ProjectMember{ProjectID: projectID, UserID: actorID, RoleID: role.ID}} {
		if err := service.db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	permissions, err := access.NewService(service.db)
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureDeliveryAccess(permissions)
}

type executionAuthorizationCheckout struct{ calls atomic.Int64 }

func (*executionAuthorizationCheckout) ListRefs(context.Context, model.GitRepository, string) (repository.RefResult, error) {
	return repository.RefResult{}, nil
}

func (p *executionAuthorizationCheckout) Checkout(context.Context, model.GitRepository, string, string, string, string) error {
	p.calls.Add(1)
	return errors.New("授权检查前不得检出代码")
}

func TestPipelineTasksWithoutVariablesRecheckExecutionPermission(t *testing.T) {
	for _, kind := range []string{"shell", "script-build", "docker-build", "deploy"} {
		for _, change := range []string{"撤销角色权限", "移除成员", "个人拒绝", "停用用户", "停用项目", "快照无授权者", "未配置授权服务"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				node := parallelShell("authorization-task")
				snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}}
				if kind == "script-build" || kind == "docker-build" {
					node.Type, node.Config.BuildPlanID = model.WorkflowNodeBuild, "authorization-plan"
					plan := workflowBuildPlanSnapshot{ID: node.Config.BuildPlanID, Kind: model.BuildPlanScript, Script: "true", RuntimeImage: model.DefaultRuntimeImage, TimeoutSeconds: 30}
					if kind == "docker-build" {
						plan.Kind, plan.DockerfilePath, plan.ContextPath = model.BuildPlanDockerfile, "Dockerfile", "."
					}
					snapshot.BuildPlans = map[string]workflowBuildPlanSnapshot{node.ID: plan}
				}
				if kind == "deploy" {
					node.Type, node.Config.DeploymentPlanID = model.WorkflowNodeDeploy, "authorization-deploy-plan"
					snapshot.DeploymentPlans = map[string]workflowDeploymentPlanSnapshot{node.ID: {ID: node.Config.DeploymentPlanID, Kind: model.DeploymentPlanKubernetes, TimeoutSeconds: 30}}
					snapshot.DeploymentTargets = map[string]workflowDeploymentTargetSnapshot{node.ID: {ID: "authorization-target", Platform: model.DeploymentKubernetes, RuntimeID: "authorization-cluster", Namespace: "default", WorkloadName: "app", ContainerName: "app", RolloutTimeout: 30}}
				}
				snapshot.Stages = []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}
				service, db, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
				ctx := pipelineExecutionTestContext(context.Background(), run)
				if kind == "deploy" {
					registry := model.ImageRegistry{ID: "authorization-registry", Name: "authorization_registry", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.com", Namespace: "team", IsActive: true}
					digest := "sha256:" + strings.Repeat("a", 64)
					stored := model.Artifact{ID: "authorization-image", ApplicationID: run.ApplicationID, PipelineRunID: run.ID, BuildRunID: "authorization-build", Kind: model.ArtifactKindOCIImage, Status: model.ArtifactStatusAvailable, StorageKind: model.ArtifactStorageKindRegistry, ImageRegistryID: registry.ID, Digest: digest, ImageRef: "registry.example.com/team/app@" + digest}
					for _, item := range []any{&registry, &stored} {
						if err := db.WithContext(ctx).Create(item).Error; err != nil {
							t.Fatal(err)
						}
					}
					run.ArtifactID = stored.ID
					if err := db.WithContext(ctx).Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("artifact_id", stored.ID).Error; err != nil {
						t.Fatal(err)
					}
				}
				probe := &executionAuthorizationCheckout{}
				service.repositories = repository.NewService(db, service.secrets, nil, probe, 4)
				var scriptCalls atomic.Int64
				service.scriptRunner = parallelScriptRunner(func(context.Context, dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
					scriptCalls.Add(1)
					return dockerengine.ScriptContainerResult{}, nil
				})
				var mutationErr error
				switch change {
				case "撤销角色权限":
					mutationErr = db.Where("role_id = ? AND permission = ?", "pipeline-executor-role", "pipeline_run.execute").Delete(&model.RolePermission{}).Error
				case "移除成员":
					mutationErr = db.Where("project_id = ? AND user_id = ?", run.ProjectID, "admin").Delete(&model.ProjectMember{}).Error
				case "个人拒绝":
					mutationErr = db.Create(&model.UserPermission{UserID: "admin", Permission: "pipeline_run.execute", Effect: model.PermissionDeny}).Error
				case "停用用户":
					mutationErr = db.Model(&model.User{}).Where("uuid = ?", "admin").Update("is_active", false).Error
				case "停用项目":
					mutationErr = db.Model(&model.Project{}).Where("uuid = ?", run.ProjectID).Update("is_active", false).Error
				case "快照无授权者":
					var frozen workflowSnapshot
					if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &frozen); err != nil {
						t.Fatal(err)
					}
					frozen.AuthorityID = ""
					raw, _ := json.Marshal(frozen)
					mutationErr = db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("workflow_snapshot", string(raw)).Error
				case "未配置授权服务":
					service.ConfigureDeliveryAccess(nil)
				}
				if mutationErr != nil {
					t.Fatal(mutationErr)
				}
				var executionErr error
				if kind == "deploy" {
					executionErr = service.ExecuteDeployTask(ctx, DeployTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, run.ExecutionJobID)
				} else {
					executionErr = service.ExecuteBuildTask(ctx, BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, run.ExecutionJobID)
				}
				if !errors.Is(executionErr, cicd.ErrExecutionAuthorization) || IsRetryableBuildTaskError(executionErr) {
					t.Fatalf("撤权任务未以不可重试授权失败终止: %v", executionErr)
				}
				if probe.calls.Load() != 0 || scriptCalls.Load() != 0 {
					t.Fatal("授权失败后仍然检出代码或执行脚本")
				}
				var stored model.PipelineRun
				if err := db.First(&stored, "uuid = ?", run.ID).Error; err != nil || stored.Status != model.PipelineRunFailed || stored.Stage != "failed" || !strings.Contains(stored.Message, "授权") {
					t.Fatalf("授权失败未持久化到运行: status=%s stage=%s err=%v", stored.Status, stored.Stage, err)
				}
				var records int64
				if err := db.Model(&model.DeploymentRecord{}).Count(&records).Error; err != nil || records != 0 {
					t.Fatalf("授权失败后创建了发布记录: count=%d err=%v", records, err)
				}
				var logs []model.PipelineRunLog
				if err := db.Where("pipeline_run_id = ?", run.ID).Find(&logs).Error; err != nil || len(logs) == 0 {
					t.Fatalf("授权失败没有运行日志: %v", err)
				}
				for _, entry := range logs {
					if strings.Contains(entry.Message, "自动重试") {
						t.Fatal("永久授权失败被安排自动重试")
					}
				}
			})
		}
	}
}

func TestPipelineExecutionPermissionRequiresFrozenProjectScope(t *testing.T) {
	node := parallelShell("project-scope")
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}}
	service, _, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
	frozen, err := parseWorkflowSnapshot(&run)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.requirePipelineExecutionPermission(pipelineExecutionTestContext(context.Background(), run), run, *frozen); err != nil {
		t.Fatalf("有效冻结项目授权被拒绝: %v", err)
	}
	for _, ctx := range []context.Context{
		context.Background(),
		database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: "other-project", DepartmentID: run.DepartmentID, AllDepartments: true}),
		database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: run.ProjectID, DepartmentID: "other-department", AllDepartments: true}),
	} {
		if err := service.requirePipelineExecutionPermission(ctx, run, *frozen); !errors.Is(err, cicd.ErrExecutionAuthorization) {
			t.Fatalf("执行器接受了与运行不匹配的任务上下文: %v", err)
		}
	}
}

func TestBuildArtifactRecoveryRequiresCurrentExecutionPermission(t *testing.T) {
	node := model.WorkflowNode{ID: "authorized-recovery", Type: model.WorkflowNodeBuild, Config: model.WorkflowNodeConfig{BuildPlanID: "recovery-plan"}}
	plan := workflowBuildPlanSnapshot{ID: node.Config.BuildPlanID, Kind: model.BuildPlanScript, ConfigVersion: 1, Script: "exit 42", RuntimeImage: model.DefaultRuntimeImage, ArtifactPath: "result.bin", TimeoutSeconds: 30}
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}, BuildPlans: map[string]workflowBuildPlanSnapshot{node.ID: plan}}
	service, db, artifacts, run := newBuildExecutorTestRun(t, node, snapshot, nil)
	ctx := pipelineExecutionTestContext(context.Background(), run)
	var component model.PipelineRunRepository
	if err := db.First(&component, "pipeline_run_id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	planJSON, _ := json.Marshal(plan)
	output := filepath.Join(t.TempDir(), "result.bin")
	if err := os.WriteFile(output, []byte("already-built-output"), 0o600); err != nil {
		t.Fatal(err)
	}
	stored, err := artifacts.CreateFileFromPath(ctx, artifact.BuildOutputInput{
		BuildMetadata: artifact.BuildMetadata{
			ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, ApplicationID: run.ApplicationID,
			PipelineRunID: run.ID, RepositoryID: component.RepositoryID, WorkflowNodeID: node.ID, BuildPlanID: plan.ID,
			ProducerKind: model.BuildRunProducerScript, Ref: run.Ref, CommitSHA: run.CommitSHA,
			PlanSnapshot: string(planJSON), PlanDigest: sha256Digest(planJSON), CreatedBy: "admin",
		},
		SourcePath: output, Name: "result.bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("role_id = ? AND permission = ?", "pipeline-executor-role", "pipeline_run.execute").Delete(&model.RolePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	err = service.ExecuteBuildTask(ctx, BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, run.ExecutionJobID)
	if !errors.Is(err, cicd.ErrExecutionAuthorization) || IsRetryableBuildTaskError(err) {
		t.Fatalf("已有制品绕过了当前执行授权: %v", err)
	}
	var updated model.PipelineRun
	if err := db.First(&updated, "uuid = ?", run.ID).Error; err != nil || updated.Status != model.PipelineRunFailed || updated.ArtifactID != "" {
		t.Fatalf("撤权后仍绑定制品并推进运行: status=%s artifact=%s err=%v", updated.Status, updated.ArtifactID, err)
	}
	if _, err := artifacts.Find(ctx, stored.ID); err != nil {
		t.Fatalf("拒绝执行时删除了此前已完成的制品: %v", err)
	}
}
