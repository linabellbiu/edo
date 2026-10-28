package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	"edo/internal/cicd"
	"edo/internal/config"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/repository"
)

type authorizationBoundaryCheckout struct {
	buildExecutorCheckoutStub
	afterCheckout func() error
}

func (stub authorizationBoundaryCheckout) Checkout(ctx context.Context, repository model.GitRepository, credential, ref, commitSHA, destination string) error {
	if err := stub.buildExecutorCheckoutStub.Checkout(ctx, repository, credential, ref, commitSHA, destination); err != nil {
		return err
	}
	return stub.afterCheckout()
}

func TestPipelineRechecksExecutionAuthorizationAfterCheckout(t *testing.T) {
	for _, kind := range []string{"shell", "script-build", "docker-local", "docker-push"} {
		for _, change := range []string{"撤销角色动作", "移除成员", "个人拒绝", "停用用户", "停用项目"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				service, db, run, files := buildAuthorizationBoundaryFixture(t, kind)
				revoked := false
				service.repositories = repository.NewService(db, service.secrets, nil, authorizationBoundaryCheckout{
					buildExecutorCheckoutStub: buildExecutorCheckoutStub{files: files},
					afterCheckout: func() error {
						revoked = true
						return revokeBuildBoundaryAuthority(db, run, change)
					},
				}, 4)
				calls := 0
				service.scriptRunner = parallelScriptRunner(func(context.Context, dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
					calls++
					return dockerengine.ScriptContainerResult{}, nil
				})
				assertBuildBoundaryAuthorizationDenied(t, service, db, run, &calls)
				if !revoked {
					t.Fatal("没有在固定代码检出完成后执行撤权")
				}
			})
		}
	}
}

func TestPipelineRechecksExecutionAuthorizationAtNewSideEffect(t *testing.T) {
	for _, kind := range []string{"shell", "script-build", "docker-local", "docker-push"} {
		t.Run(kind, func(t *testing.T) {
			service, db, run, _ := buildAuthorizationBoundaryFixture(t, kind)
			phase := "build"
			if kind == "shell" {
				phase = "shell"
			}
			const callback = "test:revoke_before_build_side_effect"
			revoked := false
			if err := db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
				updates, ok := tx.Statement.Dest.(map[string]any)
				if !revoked && tx.Statement.Table == "pipeline_runs" && ok && updates["stage"] == phase {
					revoked = true
					if err := revokeBuildBoundaryAuthority(db, run, "撤销角色动作"); err != nil {
						t.Error(err)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
			calls := 0
			service.scriptRunner = parallelScriptRunner(func(context.Context, dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
				calls++
				return dockerengine.ScriptContainerResult{}, nil
			})
			assertBuildBoundaryAuthorizationDenied(t, service, db, run, &calls)
			if !revoked {
				t.Fatal("没有覆盖准备完成后、启动新副作用前的撤权时机")
			}
		})
	}
}

func buildAuthorizationBoundaryFixture(t *testing.T, kind string) (*Service, *gorm.DB, model.PipelineRun, map[string]string) {
	t.Helper()
	node := parallelShell("authorization-boundary")
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}}
	files := map[string]string{"source.txt": "固定代码", "Dockerfile": "FROM scratch\n"}
	if kind != "shell" {
		node.Type, node.Config.BuildPlanID = model.WorkflowNodeBuild, "boundary-build-plan"
		plan := workflowBuildPlanSnapshot{ID: node.Config.BuildPlanID, Kind: model.BuildPlanScript, Script: "true", WorkingDirectory: ".", RuntimeImage: model.DefaultRuntimeImage, TimeoutSeconds: 30}
		if strings.HasPrefix(kind, "docker-") {
			plan.Kind, plan.DockerfilePath, plan.ContextPath = model.BuildPlanDockerfile, "Dockerfile", "."
			if kind == "docker-push" {
				plan.ImageRegistryID = "boundary-image-registry"
			}
		}
		snapshot.BuildPlans = map[string]workflowBuildPlanSnapshot{node.ID: plan}
	}
	snapshot.Stages = []model.WorkflowStage{{ID: "stage", Tasks: []model.WorkflowNode{node}}}
	service, db, _, run := newBuildExecutorTestRun(t, node, snapshot, files)
	// 即使防线回归，非法协议也会在连接前失败，不接触开发机 Docker 或外部仓库。
	service.docker = dockerengine.NewService(db, service.secrets, config.Runtime{DockerBuilderHost: "test-disabled://builder"})
	if kind == "docker-push" {
		registry := model.ImageRegistry{ID: "boundary-image-registry", Name: "boundary_registry", Provider: model.RegistryGeneric, Endpoint: "https://registry.example.invalid", Namespace: "test", IsActive: true}
		if err := db.Create(&registry).Error; err != nil {
			t.Fatal(err)
		}
	}
	return service, db, run, files
}

func revokeBuildBoundaryAuthority(db *gorm.DB, run model.PipelineRun, change string) error {
	switch change {
	case "撤销角色动作":
		return db.Where("role_id = ? AND permission = ?", "pipeline-executor-role", "pipeline_run.execute").Delete(&model.RolePermission{}).Error
	case "移除成员":
		return db.Where("project_id = ? AND user_id = ?", run.ProjectID, "admin").Delete(&model.ProjectMember{}).Error
	case "个人拒绝":
		return db.Create(&model.UserPermission{UserID: "admin", Permission: "pipeline_run.execute", Effect: model.PermissionDeny}).Error
	case "停用用户":
		return db.Model(&model.User{}).Where("uuid = ?", "admin").Update("is_active", false).Error
	case "停用项目":
		return db.Model(&model.Project{}).Where("uuid = ?", run.ProjectID).Update("is_active", false).Error
	default:
		return errors.New("测试撤权类型无效")
	}
}

func assertBuildBoundaryAuthorizationDenied(t *testing.T, service *Service, db *gorm.DB, run model.PipelineRun, calls *int) {
	t.Helper()
	ctx := pipelineExecutionTestContext(context.Background(), run)
	err := service.ExecuteBuildTask(ctx, BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: run.CurrentNodeID}, run.ExecutionJobID)
	if !errors.Is(err, cicd.ErrExecutionAuthorization) || IsRetryableBuildTaskError(err) {
		t.Fatalf("撤权后没有以不可重试授权失败终止: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("撤权后仍新启动脚本: calls=%d", *calls)
	}
	var current model.PipelineRun
	if err := db.First(&current, "uuid = ?", run.ID).Error; err != nil || current.Status != model.PipelineRunFailed || !strings.Contains(current.Message, "授权") {
		t.Fatalf("撤权没有明确记录为授权失效: status=%s message=%s err=%v", current.Status, current.Message, err)
	}
	var count int64
	if err := db.Model(&model.Artifact{}).Where("pipeline_run_id = ?", run.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("撤权后仍登记构建制品: count=%d err=%v", count, err)
	}
}
