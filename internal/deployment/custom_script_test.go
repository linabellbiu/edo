package deployment

import (
	"testing"

	"github.com/google/uuid"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/sshdeploy"
)

func TestDockerAndComposeExecuteFrozenHostScriptInsteadOfBuiltin(t *testing.T) {
	for _, kind := range []model.DeploymentPlanKind{model.DeploymentPlanDocker, model.DeploymentPlanCompose} {
		t.Run(string(kind), func(t *testing.T) {
			s, db, endpoint := newDeploymentTestService(t)
			project, department := database.DefaultProjectID(database.DefaultDepartmentID), database.DefaultDepartmentID
			runner := &hostScriptRunnerStub{result: sshdeploy.Result{Started: true, ExitCode: 0}}
			s.ssh = runner
			runID := uuid.NewString()
			target := model.DeploymentTarget{ID: uuid.NewString(), ProjectID: project, DepartmentID: department, Name: "自定义脚本", Platform: model.DeploymentDocker, EnvironmentID: dockerTestEnvironmentID, HostID: dockerTestHostID, RuntimeID: endpoint, WorkingDirectory: "/srv/app", WorkloadName: "app", RolloutTimeout: 120, IsActive: true}
			ctx := deploymentPipelineExecutionContext(t, db, &target, runID)
			script := "# EDO 用户部署脚本 v1\nprintf '%s' \"${IMAGE_TAG}\"\n"
			input := CommandRequestInput{TargetID: target.ID, ApplicationID: deploymentTestApplicationID, PipelineRunID: runID, WorkflowNodeID: "deploy", DeploymentPlanID: uuid.NewString(), PlanKind: kind, Script: script, ScriptDigest: model.DeploymentPlanExecutionDigest(kind, script, 120), TimeoutSeconds: 120, Environment: map[string]string{"IMAGE_TAG": "build-v1"}}
			record, err := s.RequestCommandSnapshotAndRun(ctx, "operator", target, input)
			if err != nil || record == nil || record.Status != model.DeploymentSucceeded || runner.input.Script != script || runner.input.Environment["IMAGE_TAG"] != "build-v1" {
				t.Fatalf("未执行冻结脚本: record=%+v err=%v", record, err)
			}
			if !isShellRuntime(record) {
				t.Fatal("自定义脚本不能冒充内置容器实例")
			}
			state, err := s.readRuntimeState(ctx, record)
			if err != nil || state.Kind != "script" {
				t.Fatalf("运行类型不正确: %+v %v", state, err)
			}
			original := record.ID
			record, err = s.RequestCommandSnapshotAndRun(ctx, "operator", target, input)
			if err != nil || record.ID != original {
				t.Fatalf("重复请求未复用发布记录: %v", err)
			}
		})
	}
}
